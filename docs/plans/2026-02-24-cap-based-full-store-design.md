# Cap-Based Full-Store Optimization (Load-Blend-Store) for SPMD Masked Stores

**Date**: 2026-02-24
**Status**: Proposed
**Prerequisite**: Cap-based full-load optimization (adds `sliceCap` to `spmdContiguousInfo`)

## Problem

On WebAssembly, LLVM scalarizes `llvm.masked.store` intrinsics into per-lane conditional stores (4-16 extract+GEP+conditional-store sequences). Two store paths are affected:

1. **Regular contiguous stores** (`compiler.go:2443-2456`) — tail phase or non-peeled loop body
2. **Coalesced stores** (`compiler.go:2403-2406`) — merged if/else stores with parent mask

Non-peeled loops (with break, accumulators, closures) use masked stores every iteration, making this a high-impact optimization target.

## Solution: Load-Blend-Store Pattern

Use ISPC's proven pattern: `v128.load(ptr)` → `v128.bitselect(mask, newValue, oldValue)` → `v128.store(ptr)`. 3 instructions instead of 4-16 scalarized conditional stores.

The same cap check as the load optimization gates safety: `scalarIter + laneCount <= sliceCap` ensures both the read and write of the full 128-bit vector don't trap.

## Safety

- **Single-threaded WASM**: No race condition between load and store — no concurrent writes can interleave
- **Cap guarantee**: Go slice backing arrays have at least `cap * elemSize` allocated bytes
- **Blend correctness**: `bitselect(mask, newValue, oldValue)` preserves existing data in inactive lanes — no corruption
- **Read-before-write**: For fresh allocations, `oldValue` is zeros (Go zero-init). For overwrites, `oldValue` is previous data. Both cases are correct.

## ISPC Reference

ISPC uses the identical load-blend-store pattern on WASM (`__masked_store_blend_*` in `target-wasm-i32x4.ll`):
```llvm
%old = load <WIDTH x T>* %ptr
%mask1 = trunc <WIDTH x MASK> %mask to <WIDTH x i1>
%result = select <WIDTH x i1> %mask1, <WIDTH x T> %new, <WIDTH x T> %old
store <WIDTH x T> %result, <WIDTH x T>* %ptr
```

ISPC additionally restricts this to stack-allocated pointers (`lIsSafeToBlend`), but in our case the cap check ensures safety for heap-allocated slices too.

## Design

### New Function: spmdFullStoreWithBlend

```go
func (b *builder) spmdFullStoreWithBlend(val llvm.Value, ci *spmdContiguousInfo, mask llvm.Value)
```

Emits:
```
iterPlusLanes = scalarIter + laneCount
canFullStore = iterPlusLanes ule sliceCap
br canFullStore, blendBB, maskedBB

blendBB:
    old = load <N x T>, ptr              // v128.load existing data
    blended = spmdMaskSelect(mask, val, old)  // v128.bitselect
    store blended, ptr                   // v128.store
    br mergeBB

maskedBB:
    llvm.masked.store(val, ptr, mask)    // existing scalarized fallback
    br mergeBB

mergeBB:
    // void return — no phi needed
```

### Call Site 1: Regular Contiguous Store (compiler.go:2443-2456)

```go
if !ci.sliceCap.IsNil() && !b.spmdIsConstAllOnesMask(mask) {
    b.spmdFullStoreWithBlend(llvmVal, ci, mask)
} else {
    b.spmdMaskedStore(llvmVal, ci.scalarPtr, mask)
}
```

### Call Site 2: Coalesced Store (compiler.go:2403-2406)

```go
if !ci.sliceCap.IsNil() && !b.spmdIsConstAllOnesMask(parentMask) {
    b.spmdFullStoreWithBlend(selected, ci, parentMask)
} else {
    b.spmdMaskedStore(selected, ci.scalarPtr, parentMask)
}
```

### Key Reuse: spmdMaskSelect for Blend

The existing `spmdMaskSelect(mask, trueVal, falseVal)` already emits bitwise AND/OR on WASM when type sizes match — this produces the equivalent of `v128.bitselect`. No new blend function is needed.

## Edge Cases

- **No phi needed**: Stores are void. Merge block is just a control flow join.
- **Store coalescing**: Coalesced stores pre-compute `selected = spmdMaskSelect(cond, then, else)`. The blend uses `parentMask` to preserve inactive lanes. Correct.
- **Interleaved stores**: Out of scope — `spmdEmitInterleavedStore` has its own store logic.
- **Alignment**: WASM v128.load/store don't require 16-byte alignment.
- **Decomposed paths**: `<16 x i8>` decomposed loops work identically.
- **Write-only buffers**: Extra v128.load reads zeros from fresh allocations. Acceptable overhead vs scalarization.

## Scope

- **In scope**: Contiguous masked stores to slices (regular + coalesced)
- **Out of scope**: Scatter stores, interleaved stores, non-slice stores (arrays, strings)

## Testing

### Unit Tests (spmd_llvm_test.go)
1. `TestSPMDFullStoreWithBlend` — verify branch+BB structure (cap check → blend BB with load+select+store | masked-store BB → merge)
2. `TestSPMDFullStoreSkippedForAllOnesMask` — verify all-ones mask skips optimization
3. `TestSPMDFullStoreSkippedForArrays` — verify nil sliceCap falls back to masked store

### E2E Validation
- Hex-encode benchmark (peeled loop, tail stores)
- Mandelbrot benchmark (non-peeled loop with break, main body stores)
- Verify output correctness matches scalar

### WASM Inspection
- `wasm2wat | grep v128.store` — count full stores vs conditional scalar stores
- Look for `v128.load` + `v128.bitselect` + `v128.store` sequence

## Expected Impact

| Case | Before | After | Improvement |
|------|--------|-------|-------------|
| Tail stores (peeled) | 4-16 cond stores | 1 load + 1 bitselect + 1 store | ~5x per tail iter |
| Main body stores (non-peeled) | 4-16 cond stores/iter | 1 load + 1 bitselect + 1 store/iter | ~5x per iter |

Non-peeled loops see the largest absolute gain since the optimization applies every iteration.
