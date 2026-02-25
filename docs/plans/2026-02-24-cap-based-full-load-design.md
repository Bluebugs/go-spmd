# Cap-Based Full-Load Optimization for SPMD Masked Loads

**Date**: 2026-02-24
**Status**: Proposed

## Problem

On WebAssembly, LLVM scalarizes `llvm.masked.load` intrinsics into per-lane conditional scalar loads (4-16 branch+load+insert sequences). This affects:

1. **Tail phase** of peeled SPMD loops (mask has trailing zeros)
2. **Main body** of non-peeled SPMD loops (loops with break, accumulators, closures) where the mask comes from break masks or varying conditions

Case 2 is the higher-impact target since it runs every iteration.

## Solution

When loading from a Go slice with contiguous access, use the slice's **capacity** to determine if a full-width `v128.load` is safe. If `scalarIter + laneCount <= cap`, the backing array has enough allocated memory for a full vector load regardless of which lanes are active.

Replace: `llvm.masked.load(ptr, mask, zeroinit)` (scalarizes to 4-16 conditional loads)
With: `v128.load(ptr)` + `select(mask, loaded, zeroinit)` (2 instructions)

## Safety Invariant

- Go slice backing arrays are allocated with at least `cap * elemSize` bytes
- TinyGo uses power-of-2 capacity growth, so cap is typically much larger than len
- Reading within `[len, cap)` produces garbage values but does not trap
- The `select` masks out inactive lanes, so garbage values are never used
- WASM linear memory traps on out-of-bounds access, so the cap check is mandatory

## Design

### Data Structure Change

Extend `spmdContiguousInfo` to carry the slice cap:

```go
type spmdContiguousInfo struct {
    scalarPtr llvm.Value
    loop      *spmdActiveLoop
    sliceCap  llvm.Value  // cap of source slice (nil for arrays/strings)
}
```

### Cap Extraction (IndexAddr Time)

In `spmdContiguousIndexAddrCore`, when the source is a `*types.Slice`:

```go
case *types.Slice:
    bufptr := b.CreateExtractValue(val, 0, "indexaddr.ptr")
    sliceCap := b.CreateExtractValue(val, 2, "indexaddr.cap")
    // ... store sliceCap in spmdContiguousInfo
```

For arrays and strings, `sliceCap` remains nil (existing masked load path).

### Full Load Emission (Load Site)

New function `spmdFullLoadWithSelect`:

```
iterPlusLanes = scalarIter + laneCount
canFullLoad = iterPlusLanes ule sliceCap      // unsigned comparison
br canFullLoad, fullBB, maskedBB

fullBB:
    raw = load <N x T>, ptr                   // full v128.load
    result = select(mask, raw, zeroinit)      // mask out inactive lanes
    br mergeBB

maskedBB:
    result = llvm.masked.load(ptr, mask)      // existing scalarized fallback
    br mergeBB

mergeBB:
    phi [fullBB: result1, maskedBB: result2]
```

### Call Site (token.MUL Dereference)

```go
// In the contiguous load path:
if ci.sliceCap.IsNil() || isConstAllOnes(mask) {
    return b.spmdMaskedLoad(vecType, ci.scalarPtr, mask), nil  // existing path
}
return b.spmdFullLoadWithSelect(vecType, ci, mask), nil  // optimized path
```

Skip when:
- `sliceCap` is nil (arrays, strings) — no cap to check
- Mask is all-ones (already optimized by LLVM to plain load)

## Edge Cases

- **Multiple slices**: Each `spmdContiguousInfo` carries its own `sliceCap`; independent checks per load
- **Pointer alignment**: Same scalarPtr from existing GEP; WASM v128.load supports unaligned
- **Decomposed loops (laneCount > 4)**: Same optimization applies to `<16 x i8>` paths
- **Select zero-fill**: Inactive lanes get ConstNull, matching masked.load passthru semantics
- **Loop-invariant cap**: Slice cap is extracted once at IndexAddr time; doesn't change during loop

## Scope

- **In scope**: Contiguous masked loads from slices (both tail and main body)
- **Out of scope (future)**: Masked stores (requires load-modify-store pattern), string loads, gather/scatter

## Testing

### Unit Tests (spmd_llvm_test.go)
1. `TestSPMDFullLoadWithSelect` — verify branch+phi IR structure
2. `TestSPMDFullLoadSkippedForArrays` — verify nil sliceCap fallback
3. `TestSPMDFullLoadSkippedForAllOnesMask` — verify all-ones skip
4. `TestSPMDContiguousInfoSliceCap` — verify cap extraction

### E2E Validation
- Hex-encode benchmark (peeled loop, tail improvement)
- Mandelbrot benchmark (non-peeled loop with break, main body improvement)
- Verify output correctness matches scalar

### WASM Inspection
- `wasm2wat | grep v128.load` — count full loads vs conditional scalar loads

## Expected Impact

| Case | Before | After | Improvement |
|------|--------|-------|-------------|
| Tail (peeled) | 4-16 cond loads | 1 load + 1 select | ~8x per tail iter |
| Main body (non-peeled) | 4-16 cond loads/iter | 1 load + 1 select/iter | ~8x per iter |

Non-peeled loops (mandelbrot) see the largest absolute gain since the optimization applies every iteration.

## ISPC Comparison

ISPC does not perform cap-based load optimization. Its `ImproveMemoryOps` pass classifies masks as all-on/all-off/mixed and converts gathers to masked loads for linear access patterns, but does not use buffer capacity to eliminate masking. A newer `ReplaceMaskedMemOps` pass handles constant prefix masks (N leading 1s) but not dynamic cap checks. This optimization is novel relative to ISPC.
