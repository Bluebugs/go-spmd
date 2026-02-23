# Hex-Encode SIMD Optimization Analysis

Analysis of the LLVM IR (376 lines) and WASM output for `main.Encode`, which uses a 16-lane `<16 x i8>` SPMD loop with base+offset decomposition. The benchmark currently shows SPMD at 0.24x scalar speed, confirming these optimization issues are impactful.

## Current WASM Instruction Profile

```
39 v128.store           — masked store scalarization
34 v128.const           — constant vectors
14 i8x16.replace_lane   — gather scalarization
 2 v128.load            — contiguous loads
 2 i8x16.swizzle        — hextable lookups (good!)
 1 v128.load8_splat
 1 v128.and             — nibble masking (good!)
 1 i8x16.shuffle        — interleave (good!)
 1 i8x16.shr_u          — shift right (good!)
```

## What Works Well

- `i8x16.swizzle` for hextable lookup (native WASM instruction)
- `i8x16.shr_u` for `v>>4` (native SIMD shift)
- `v128.and` for `v&0x0f` (native SIMD mask)
- `i8x16.shuffle` to interleave high/low nibble results (after LLVM opt)
- Contiguous `llvm.masked.store.v16i8` for `dst[i]` output
- `<16 x i8>` tail masks with `v128.any_true` for early exit

## Issue 1: Per-Lane Scalar Gather for `src[i>>1]`

**Impact**: ~40 instructions instead of 2

The `src[i>>1]` pattern with varying offset `[0,0,1,1,...,7,7]` generates 16 GEPs + 16 insertelements + `llvm.masked.gather.v16i8`. Only 8 unique bytes are accessed per iteration.

**Current codegen**: 16 scalar loads + 14 `i8x16.replace_lane` + `v128.load8_splat` = ~16 WASM instructions.

**Ideal**: `v128.load64_zero` of 8 bytes at `&src[base>>1]` + `i8x16.shuffle` with constant pattern `[0,0,1,1,2,2,...,7,7]` = 2 instructions. (`i8x16.shuffle` is the compile-time constant variant; `i8x16.swizzle` is for runtime indices.)

**Fix approach**: Detect gather patterns where indices form a known permutation of a contiguous range. When the gather base is contiguous and the permutation is a compile-time constant, replace `llvm.masked.gather` with `v128.load64_zero` + `i8x16.shuffle`.

## Issue 2: Per-Lane Scalar Bounds Checks for Gather

**Impact**: ~32 instructions per bounds-check site

Each bounds check for the gather (`src[i>>1]`) generates 16 `extractelement` + 16 `zext` + 16 `icmp` + 15 `or` = ~64 scalar instructions chained together.

**Current**: The compiler checks each of the 16 lane indices individually against `len(src)`.

**Ideal**: Since the max offset within the gather is known at compile time (`base>>1 + 7`), a single scalar comparison `base>>1 + 7 < len(src)` suffices for all lanes simultaneously.

**Fix approach**: When a gather index is `base + constant_offset`, compute `max(constant_offsets)` and emit a single scalar bounds check `base + max_offset < len`. This is the same optimization as contiguous access bounds checking, generalized to known-offset gathers.

## Issue 3: Provably Unnecessary Hextable Bounds Checks

**Impact**: ~128 instructions wasted (64 per lookup site, 2 sites)

`hextable[v>>4]` where `v` is a byte: `v>>4` is always in range 0-15. `hextable` is exactly 16 bytes. The bounds check can never fail. Same for `hextable[v&0x0f]`.

**Current**: Each site generates 16 `extractelement` + 16 `zext` + 16 `icmp` + 16 `or` = 64 instructions per site (128 total for both high and low nibble lookups).

**Ideal**: Zero bounds check instructions. The index is provably in-range.

**Fix approach**: This is a value-range analysis optimization. When the compiler can prove that a varying index is always within bounds (e.g., result of `>>4` on a byte, or `&0x0f` on any value), skip bounds check generation entirely. This could be done either:
- In the TinyGo compiler during gather emission (check if index SSA value is a shift/mask with known range)
- As an LLVM optimization pass (dead branch elimination after range proof)

## Issue 4: Dead `<16 x i32>` Vector Materializations

**Impact**: 6 unused 512-bit vector constructions in pre-opt IR

Every `getValue(loopVar)` call for debug info materializes a full `<16 x i32>` vector (splat + add offset). These are only referenced by `#dbg_value` metadata.

**Current**: 6 `<16 x i32>` constructions (each: `insertelement` x16 + `add`) that exist solely for debug info.

**Ideal**: Zero cost in release builds.

**Status**: TinyGo already gates `DebugRef` emission on the `b.Debug` flag, so these vectors should not be materialized in non-debug builds. Verify that no SPMD-specific `getValue` calls for loop variables outside of `DebugRef` instructions trigger spurious vector materialization. If confirmed absent in release WASM, this issue is already handled.

## Issue 5: Non-Constant-Folded `i%2==0` Comparison

**Impact**: 7 intermediate instructions instead of 1 constant

The `i%2==0` comparison on the varying loop index generates: diff, clamp, trunc, splat, cmp, neg.sel, over.sel = 7 intermediate values.

**Current**: The SPMD compiler already recognizes `varying_index % constant` patterns and decomposes them via base+offset (in `spmd.go` lines 3272-3305). However, the subsequent EQL comparison against 0 still generates 7 intermediate LLVM values (`diff`, `clamp`, `trunc`, `splat`, `cmp`, `neg.sel`, `over.sel`) because the scalar base contribution (`urem(loopBase, 2)`) is computed dynamically rather than folded to zero.

**Ideal**: Since the SPMD loop base is always a multiple of 16 (the lane count), and `16 % 2 == 0`, the scalar base modulo is statically zero. The EQL comparison reduces to comparing the constant offset vector `[0,1,0,1,...,0,1]` against `zeroinitializer`, which LLVM folds to a single `v128.const`.

**Fix approach**: In the decomposed REM path, when `loopBase` is provably a multiple of `laneCount` and `laneCount % k == 0`, fold the scalar base `urem(loopBase, k)` to a constant zero. This makes the subsequent EQL comparison a constant-vs-constant vector comparison that LLVM can fold entirely.

## Issue 6: Dead Select at Merge Point

**Impact**: 1 unnecessary instruction

A `select i1 %cond, i32 %val, i32 %val` at the if/else merge point — both arms produce the same value. This is a no-op.

**Current**: Generated from phi-to-select conversion when both branches assign the same value to a variable.

**Ideal**: Zero instructions (identity select elimination).

**Status**: LLVM's InstCombine pass should eliminate this. If it persists in final WASM, the phi-to-select conversion in the TinyGo SPMD backend should check for identity selects before emitting.

## Optimization Priority

| Issue | Impact | Difficulty | Priority |
|-------|--------|------------|----------|
| 1. Gather → load64_zero+shuffle | High (~40 → 2 inst) | Medium | P1 |
| 3. Hextable bounds elim | High (~128 inst wasted) | Medium | P1 |
| 2. Scalar bounds check | Medium (~64 inst/site) | Medium | P2 |
| 5. Const-fold i%2==0 | Low (7 → 1 inst) | Low | P2 |
| 4. Dead debug vectors | Low (gated by b.Debug) | N/A | P3 |
| 6. Dead identity select | Minimal (1 inst) | N/A | P3 |

## Expected Speedup

Fixing issues 1-3 alone would eliminate approximately 200 of the ~300 non-trivial instructions in the SPMD loop body. Combined with the existing good SIMD usage (swizzle, shift, and, shuffle), the hex-encode benchmark should achieve meaningful speedup over scalar.
