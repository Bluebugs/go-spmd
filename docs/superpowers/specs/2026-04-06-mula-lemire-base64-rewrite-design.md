# Mula-Lemire Base64 Decoder Rewrite Design

## Goal

Rewrite the SPMD base64 decoder to use a Mula-Lemire-style byte-stream pipeline, eliminating scatter-gather memory access and replacing 256-entry table lookups with nibble-LUT `vpshufb` operations. Target: x86-64 (SSE + AVX2) and WASM SIMD128.

## Current State

The existing decoder at `test/integration/spmd/base64-decoder-spmd/main.go` uses `go for g := range groups` where each SPMD lane processes one 4-byte quartet:

- 4 gather loads per quartet (`decodeTable[src[g*4+i]]`) — 62% of hot loop
- 256-byte lookup table via scatter-gather
- Bounds checks per access — 17-39% of hot loop
- Full `Decode()`: ~350 MB/s on AVX2 (3x slower than scalar TinyGo)
- Hot loop only: ~2050 MB/s on AVX2 int32 8-wide

Mula-Lemire reference: ~4500 MB/s on AVX2.

## Architecture

Replace quartet-per-lane scatter-gather with contiguous byte-stream processing using `Varying[byte]`:

```
Current:  go for g := range groups → each lane = one quartet → scatter-gather
New:      for offset := range chunks → Varying[byte] = 16/32 contiguous bytes → register pipeline
```

### Pipeline (per 16/32-byte chunk)

| Step | Operation | SPMD Expression | x86 Instruction |
|------|-----------|-----------------|-----------------|
| 1 | Load 16/32 bytes | `lanes.From[byte](src[offset:])` | `vmovdqu` |
| 2 | Nibble decompose | `hi := input >> 4; lo := input & 0x0F` | `vpsrlw` + `vpand` |
| 3 | Classify nibbles | `lanes.Swizzle(hiLUT, hi)` | `vpshufb` |
| 4 | Validate | `(hiClass & loClass) != 0` | `vpand` + `vptest` |
| 5 | Decode to sextets | `input + lanes.Swizzle(offsetLUT, hi)` | `vpshufb` + `vpaddb` |
| 6 | Pack 4×6-bit→3×8-bit | shift/mask/or | `vpsrlw` + `vpand` + `vpor` |
| 7 | Compact output | `lanes.Swizzle(packed, compactLUT)` | `vpshufb` |
| 8 | Store 12/24 bytes | store | `vmovdqu` |

Steps 2-7 are register-to-register — zero memory indirection in the hot loop.

### Lane Counts

| Target | Register Width | `Varying[byte]` Lanes | Bytes/Iteration | Output/Iteration |
|--------|---------------|----------------------|-----------------|------------------|
| WASM SIMD128 | 16 bytes | 16 | 16 | 12 |
| x86 SSE | 16 bytes | 16 | 16 | 12 |
| x86 AVX2 | 32 bytes | 32 | 32 | 24 |

### Loop Structure

Uses a scalar `for` loop (not `go for`) because the 16/32 bytes within one chunk ARE the SIMD lanes. The parallelism is within each iteration, not across iterations:

```go
func decodeHot(dst, src []byte) {
    lc := lanes.Count[byte]()  // 16 or 32
    for offset := 0; offset+lc <= len(src); offset += lc {
        input := lanes.From[byte](src[offset:])
        // ... pipeline steps 2-7 ...
        // store to dst[outputOffset:]
    }
}
```

### Nibble LUTs

Three 16-byte constant tables (from Mula-Lemire's paper):

- **`hiLUT`**: Maps high nibble → character class bitmask. Used for validation.
- **`loLUT`**: Maps low nibble → character class bitmask. Used for validation.
- **`offsetLUT`**: Maps high nibble → offset to subtract from ASCII to get 6-bit sextet value.

Each fits in one 128-bit register. On AVX2, `spmdWasmSwizzle` auto-duplicates to both 128-bit halves.

### Packing (Shift/Mask/Or)

The 16/32 decoded sextets (each 6 bits in a byte) need packing into 12/24 output bytes. Four sextets → three bytes:

```
sextet layout:  [00aaaaaa] [00bbbbbb] [00cccccc] [00dddddd]
output layout:  [aaaaaabb] [bbbbcccc] [ccdddddd]
```

Expressed as:
```go
b0 := (s0 << 2) | (s1 >> 4)
b1 := (s1 << 4) | (s2 >> 2)
b2 := (s2 << 6) | s3
```

This requires deinterleaving sextets into s0/s1/s2/s3 groups first (via SwizzleWithin or Swizzle), then shifting, then compacting to remove the 4th-byte gap. ~9 instructions vs Mula-Lemire's 2 (`pmaddubsw` + `pmaddwd`).

**Future optimization**: The compiler could detect this shift/or pattern and replace with `pmaddubsw` + `pmaddwd`. This is a separate follow-up, not part of this rewrite.

### AVX2 vpshufb Semantics

`vpshufb` on AVX2 operates within 128-bit halves independently — each half has its own 16-byte LUT. This means:
- Indices 0-15 select from the same-half table
- Index bit 7 set → output byte is zero (useful for validation)
- The 16-entry nibble LUT is replicated in both halves automatically

This matches `lanes.Swizzle` on `Varying[byte]` with 32 lanes — the swizzle operates within each 16-byte group naturally.

## Sub-Projects

This decomposes into three sequential sub-projects, each independently testable.

### Sub-project 1: Fix Varying[byte] SIMD on x86-64

**Problem**: `Varying[byte]` operations on x86-64 reportedly fall back to scalar `movq` instead of generating vector instructions.

**Scope**: Diagnose the byte-width SIMD issue in `tinygo/compiler/spmd.go`. Fix vector type generation and arithmetic for `Varying[byte]` on x86-64 (SSE + AVX2). The decomposed addressing path (`spmdAddress` with scalar base + `<N x i8>` offset) may need fixes for x86 where full `<N x i32>` index vectors are available.

**Key files**: `tinygo/compiler/spmd.go` (vector type generation, arithmetic ops, load/store), `tinygo/compiler/compiler.go` (instruction lowering).

**Success criteria**:
- A `go for` loop with `Varying[byte]` arithmetic generates `<16 x i8>` (SSE) or `<32 x i8>` (AVX2) LLVM vector operations
- Simple byte arithmetic test passes E2E on WASM, x86 SSE, and x86 AVX2
- No scalar fallback for basic operations (add, sub, shift, and, or, xor)

**Risk**: May be a small gating fix or a deep issue. Investigation required before estimating effort.

### Sub-project 2: Optimize lanes.Swizzle for byte vectors

**Problem**: `createSwizzle` in `tinygo/compiler/spmd.go` emits per-lane `extractelement`/`insertelement` for all types. For `Varying[byte]` on AVX2 (32 lanes), that's 64+ instructions. The hardware has single-instruction byte permutation.

**Fix**: In `createSwizzle`, when `value.Type()` is `<N x i8>`, emit `spmdWasmSwizzle` (which already dispatches to `vpshufb` on x86 SSSE3/AVX2 and `i8x16.swizzle` on WASM) instead of the extract/insert loop.

**Semantic decision**: `vpshufb` zeros output when index bit 7 is set. Current `lanes.Swizzle` wraps indices with `urem`. For the byte-width optimization path, we apply `& 0x0F` to indices before calling vpshufb, preserving the existing `lanes.Swizzle` modular wrap-around contract. This means byte-width Swizzle never produces zero-on-high-bit behavior — it always wraps. Code that wants the zero-on-high-bit vpshufb behavior should use `SwizzleWithin` or a future explicit intrinsic.

**Key files**: `tinygo/compiler/spmd.go` (`createSwizzle`, `spmdWasmSwizzle`, `spmdX86Pshufb`).

**Success criteria**:
- `lanes.Swizzle` on `Varying[byte]` generates 1 `vpshufb` (x86) or 1 `i8x16.swizzle` (WASM) instead of 32+ extract/insert pairs
- Existing hex-encode and byte-swizzle tests still pass
- New unit test verifying instruction selection for byte vectors

**Dependencies**: Sub-project 1 (Varying[byte] must generate correct vector types first).

### Sub-project 3: Rewrite base64 decoder

**Scope**: Replace `test/integration/spmd/base64-decoder-spmd/main.go` with a Mula-Lemire-style pipeline using `Varying[byte]`, contiguous loads, and nibble-LUT decoding via `lanes.Swizzle`.

**Key components**:
- Nibble LUT tables (16 bytes each): `hiLUT`, `loLUT`, `offsetLUT`
- Hot loop: scalar `for` over 16/32-byte chunks with `Varying[byte]` pipeline
- Packing: shift/mask/or (not pmaddubsw)
- Output compaction: `lanes.Swizzle` with constant compact pattern
- Validation: `(hiClass & loClass) != 0` check per chunk
- Tail handling: scalar fallback for remaining bytes and padding

**Success criteria**:
- Identical output to current decoder for all test cases
- Hot loop generates contiguous loads, `vpshufb` for LUT lookups, vector shift/or for packing — no scatter-gather
- Throughput significantly better than current 350 MB/s full Decode on AVX2
- Works on both WASM SIMD128 and x86-64 (SSE + AVX2)

**Dependencies**: Sub-projects 1 and 2.

## What This Design Does NOT Include

- **`pmaddubsw` pattern detection**: The compiler optimization to detect shift/or packing patterns and replace with multiply-add instructions. This is a follow-up that benefits base64, IPv4 parsing, and other byte-packing workloads.
- **`DotProductI8x16Add` removal**: The existing builtin stays as-is. The pattern detector would make it unnecessary for new code but doesn't require removing it.
- **AVX-512 / VBMI**: Mula-Lemire's fastest path uses `vpermi2b` (AVX-512 VBMI). Out of scope — AVX2 is the current ceiling.
- **Varying[byte] on ARM NEON**: Portability to ARM is not targeted in this rewrite.
- **Outer SPMD parallelism**: Processing multiple chunks in parallel via `go for` over chunk indices. The current design uses scalar loop + intra-chunk SIMD. Outer parallelism could be a future enhancement.

## Performance Expectations

| Component | Current | Expected After Rewrite |
|-----------|---------|----------------------|
| Memory access | 32 scatter loads/iter | 1 contiguous load/iter |
| LUT decode | 32 dependent table loads | 2-3 vpshufb (register) |
| Packing | 9 shift/or instructions | 9 shift/or instructions (same, future: 2) |
| Output | scatter stores | 1 contiguous store |
| Bounds checks | 36 instructions | 0-1 (single length check) |
| **Hot loop estimate** | ~2050 MB/s (int32 8-wide) | ~3000-4000 MB/s |
| **Full Decode estimate** | ~350 MB/s | ~1500-2500 MB/s |

The biggest win comes from eliminating scatter-gather (steps 1, 2) and bounds checks. The shift/or packing stays the same until the compiler pattern detector is added.
