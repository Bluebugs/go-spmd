# Hex-Encode SIMD Optimization Analysis

Analysis of the WASM output for the hex-encode benchmark, which tests two SPMD implementations:
- `main.Encode` (dst-centric): 16-lane `<16 x i8>` loop over `dst`, processes 8 src bytes/iter
- `main.EncodeSrc` (src-centric): 16-lane `<16 x i8>` loop over `src`, processes 16 src bytes/iter

## Current Performance (2026-02-24)

| Variant | Speedup | Instr/iter | Bytes/iter | Theoretical |
|---|---|---|---|---|
| Scalar | 1.0x | 35 | 1 src → 2 dst | — |
| Encode (dst) | ~3.0x | 35 | 8 src → 16 dst | 8.0x |
| EncodeSrc (src) | ~10-11x | 38 | 16 src → 32 dst | 13.3x |

**History**: 0.24x → 0.34x (bounds elision + store coalescing) → 2.68x (loop peeling) → 3.0x/11x (stride interleaved stores + source-centric variant) → 38 instr/iter (phi-ptr offset folding).

## Generated WASM Quality

Both SPMD functions compile to near-optimal WASM:
- **0 scalarized operations** — no `extract_lane`/`replace_lane` anywhere
- **0 bounds checks** in the hot loop body (elided or hoisted)
- Native `i8x16.swizzle` for hextable lookups
- Native `i8x16.shuffle` for byte duplication and high/low interleaving
- Plain `v128.store` (not masked) thanks to loop peeling
- `v128.load64_zero` (Encode) or `v128.load` (EncodeSrc) for contiguous loads

## EncodeSrc Main Loop (38 instructions — near optimal)

```wat
loop:
  ;; Bound check (4 instr)
  local.get i; i32.const 1023; i32.gt_u; br_if exit

  ;; Prepare dst phi pointer (1 instr)
  local.get dst_phi

  ;; Load hextable as v128 constant (2 instr)
  v128.const "0123456789abcdef"; local.tee hextable

  ;; Load 16 src bytes (4 instr)
  local.get src_ptr; local.get i; i32.add; v128.load

  ;; High nibble → hextable lookup (4 instr)
  local.tee src_bytes; i32.const 4; i8x16.shr_u; i8x16.swizzle

  ;; Low nibble → hextable lookup (6 instr)
  local.get hextable; local.get src_bytes; v128.const 0x0f..0f; v128.and; i8x16.swizzle

  ;; Interleave 2nd half + store to dst_phi+16 (2 instr)
  i8x16.shuffle [8,24,9,25,...,15,31]; v128.store offset=16

  ;; Interleave 1st half + store to dst_phi+0 (5 instr)
  local.get dst_phi; local.get high_chars; local.get low_chars
  i8x16.shuffle [0,16,1,17,...,7,23]; v128.store

  ;; Advance counters (8 instr)
  i += 16; dst_phi += 32; br loop
```

## Encode Main Loop (35 instructions)

```wat
loop:
  ;; Bound check (6 instr)
  local.get i; i32.const -16; i32.add; i32.const 2031; i32.gt_s; br_if exit

  ;; Compute dst+i write address (3 instr)
  local.get dst; local.get i; i32.add

  ;; Load hextable (2 instr)
  v128.const "0123456789abcdef"; local.tee hextable

  ;; Load 8 src bytes, duplicate each: [b0,b0,b1,b1,...,b7,b7] (3 instr)
  local.get src; v128.load64_zero
  local.get hextable; i8x16.shuffle [0,0,1,1,...,7,7]; local.tee dup_bytes

  ;; High nibble lookup (3 instr)
  i32.const 4; i8x16.shr_u; i8x16.swizzle

  ;; Low nibble lookup (5 instr)
  local.get hextable; local.get dup_bytes; v128.const 0x0f..0f; v128.and; i8x16.swizzle

  ;; Interleave + single store (2 instr)
  i8x16.shuffle [0,17,2,19,...,14,31]; v128.store

  ;; Advance (8 instr)
  src += 8; i += 16; br loop
```

## Completed Optimizations

### Issue 1: Per-Lane Scalar Gather for `src[i>>1]` — FIXED

**Impact**: ~40 instructions reduced to 2-3
**Commit**: 7afdad4 (gather shift-load expansion)

The `src[i>>1]` pattern previously generated 16 GEPs + 16 insertelements (~31 WASM instructions). Now: `v128.load64_zero` + `i8x16.shuffle [0,0,1,1,...,7,7]` = 2-3 instructions.

### Issue 2: Per-Lane Scalar Bounds Checks — FIXED

**Impact**: ~64 instructions per site eliminated
**Fix**: Loop peeling moves all bounds checks out of the main loop. The peeled main loop runs with an aligned bound guarantee, so no per-iteration bounds checks are needed.

### Issue 3: Hextable Bounds Checks — FIXED

**Impact**: ~128 instructions eliminated (64 per lookup site, 2 sites)
**Fix**: `i8x16.swizzle` replaces gather-with-bounds-check entirely. WASM's `swizzle` instruction returns 0 for out-of-range indices (0-15 range guaranteed by `v>>4` and `v&0x0f`), so no explicit bounds check is generated.

### Issue 5: `i%2==0` Comparison — N/A

The Encode function now uses `i8x16.shuffle`-based interleaving instead of a modulo condition. The EncodeSrc function iterates over `src` directly and uses stride-2 interleaved stores, avoiding the `i%2` pattern entirely.

### Issue 4: Dead Debug Vectors — N/A

Confirmed absent in release WASM output. TinyGo gates `DebugRef` emission on `b.Debug`.

### Issue 6: Dead Identity Select — N/A

Not present in current WASM output. Store coalescing + shuffle interleaving eliminated the merge point that produced identity selects.

## Remaining Optimization Opportunities

### OPT-1: Store Offset Folding in Interleaved Stores — DONE

**Impact**: 4 instructions per iteration (EncodeSrc), ~10% of loop body (42→38)
**Fix**: Advancing pointer phi + constant GEP offset

A simple two-level GEP (`GEP(GEP(buf, base), 16)`) is merged back to a single GEP by InstCombine. Instead, the main peeled loop uses an advancing pointer phi that starts at `dst[0]` and advances by `stride*N` bytes per iteration. Stores use `phi` (k=0) and `GEP(phi, k*N)` (k>0). Since the phi is a structural SSA value, InstCombine cannot merge it with the constant GEP, preserving the `base + const_offset` pattern that the WASM backend folds into `v128.store offset=16`.

**Location**: `tinygo/compiler/spmd.go` — `spmdCreateInterleavedPtrPhis`, `spmdEmitInterleavedStore`

### OPT-2: Loop Bound Check Simplification — MINOR

**Impact**: 2 instructions per iteration, both functions
**Difficulty**: Medium (general-purpose pattern, tradeoff with correctness for edge cases)

The `-16 + gt_s 2031` pattern uses 6 instructions. A simple `i32.ge_u 2048` would be 4. However, the current form handles arbitrary array sizes correctly (avoids unsigned overflow for sizes near UINT_MAX). Not worth changing for a benchmark-only benefit.

### OPT-3: Encode Architectural Limitation — NO FIX

Encode processes 8 src bytes per iteration vs 16 for EncodeSrc. This is inherent to the dst-centric formulation: iterating over 2048 dst bytes means each 16-lane iteration covers 16 dst bytes = 8 src bytes. The source-centric formulation is algorithmically 2x more efficient.

## Performance Gap Analysis

| Function | Theoretical | Measured | Gap | Likely Cause |
|---|---|---|---|---|
| Encode | 8.0x | ~3.0x | 2.7x | Memory latency (8-byte loads), WASM JIT overhead |
| EncodeSrc | 13.3x | ~10-11x | 1.2-1.3x | Memory bandwidth (32B writes/iter), store buffer |

The EncodeSrc function is within ~80% of theoretical instruction-count parity. The remaining gap is primarily memory subsystem overhead, not instruction inefficiency.
