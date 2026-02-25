# Alloca Fast-Path for SPMD Full Load/Store

**Date**: 2026-02-24
**Status**: Proposed
**Prerequisite**: Cap-based full-load and full-store optimizations

## Problem

The cap-based load and store optimizations emit a runtime branch at every masked load/store site to check `scalarIter + laneCount <= sliceCap`. When the pointer originates from a stack allocation (alloca), this branch is unnecessary — the stack frame memory is always fully accessible within the allocation size.

Additionally, arrays (`*types.Pointer` → `*types.Array`) currently have no cap-based optimization at all (sliceCap is nil), so they always take the scalarized masked load/store path. Stack-allocated arrays are the primary case where we can safely use full load/store without any runtime check.

## Solution

Add a fast-path before the cap check: trace `expr.X` (the IndexAddr source) back through the Go SSA to detect `*ssa.Alloc` with `Heap == false`. When detected, emit the full load+select or load-blend-store directly — no branch, no fallback.

### Optimization Layering (Updated)

```
1. Is mask all-ones?          → plain load/store (existing LLVM opt)
2. Is pointer from alloca?    → full load+select / load-blend-store (NO branch)
3. Has sliceCap available?    → runtime cap check → full or masked path
4. Otherwise                  → masked load/store (scalarized fallback)
```

## Safety

- **Stack allocations own their memory**: An alloca of size N bytes is fully readable/writable. Reading past the array bounds but within the alloca just reads adjacent stack data (no trap).
- **Load + select safety**: Garbage from out-of-bounds lanes is masked to zero by select. Never used.
- **Load-blend-store safety**: `load(ptr)` reads existing stack data, `select(mask, new, old)` preserves inactive lanes as-is, `store(ptr)` writes back exactly what was there for inactive lanes. Net effect: only active lanes change. Safe in single-threaded execution.
- **WASM specifics**: Stack is part of linear memory. Reads within the stack frame never trap (no page boundary within a function's stack).

## Design

### SSA Tracing Helper

```go
// spmdIsAllocaOrigin returns true if the SSA value traces back to a stack
// allocation (*ssa.Alloc with Heap=false) with enough space for a full
// vector load/store. Conservative: returns false for anything it can't prove.
func (b *builder) spmdIsAllocaOrigin(ssaVal ssa.Value, laneCount int, elemType types.Type) bool
```

Traces through:
- `*ssa.Alloc`: Check `!Heap` and `arrayLen >= laneCount` (for arrays) or `allocSize >= laneCount * elemSize`
- `*ssa.FieldAddr`: Trace through struct field access to parent alloc
- `*ssa.IndexAddr`: Already our entry point; `expr.X` is what we trace
- Returns `false` for: function params, phis over mixed sources, heap allocs, closures, anything uncertain

### Data Structure Change

Extend `spmdContiguousInfo` to carry the SSA address:

```go
type spmdContiguousInfo struct {
    scalarPtr  llvm.Value
    loop       *spmdActiveLoop
    sliceCap   llvm.Value   // cap of source slice (nil for arrays/strings)
    ssaSource  ssa.Value    // original IndexAddr.X for alloca tracing
}
```

### Integration in spmdContiguousIndexAddrCore

For the array case, store `expr.X`:

```go
case *types.Array:
    // ... existing GEP code ...
    if b.spmdContiguousPtr != nil {
        b.spmdContiguousPtr[expr] = &spmdContiguousInfo{
            scalarPtr: ptr, loop: loop, ssaSource: expr.X,
        }
    }
```

For the slice case (already stores sliceCap from load optimization), also store `expr.X`:

```go
case *types.Slice:
    // ... existing GEP + cap extraction ...
    if b.spmdContiguousPtr != nil {
        b.spmdContiguousPtr[expr] = &spmdContiguousInfo{
            scalarPtr: ptr, loop: loop, sliceCap: sliceCap, ssaSource: expr.X,
        }
    }
```

### Integration at Load/Store Sites

In both `spmdFullLoadWithSelect` and `spmdFullStoreWithBlend`, add before the cap check:

```go
// Fast path: alloca origin — always safe, no branch needed.
if ci.ssaSource != nil && b.spmdIsAllocaOrigin(ci.ssaSource, ci.loop.laneCount, elemType) {
    // Emit full load+select or load-blend-store directly.
    // No branch, no phi, no fallback.
}
```

For the **load** fast-path:
```go
raw := b.CreateLoad(vecType, ci.scalarPtr, "spmd.alloca.load")
return b.spmdMaskSelect(mask, raw, llvm.ConstNull(vecType))
```

For the **store** fast-path:
```go
old := b.CreateLoad(vecType, ci.scalarPtr, "spmd.alloca.old")
blended := b.spmdMaskSelect(mask, val, old)
b.CreateStore(blended, ci.scalarPtr)
```

### Array Size Validation

For `*ssa.Alloc` pointing to `*types.Array`, check that the array length accommodates a full vector:

```go
case *ssa.Alloc:
    if alloc.Heap {
        return false
    }
    ptrType := alloc.Type().Underlying().(*types.Pointer)
    if arrType, ok := ptrType.Elem().Underlying().(*types.Array); ok {
        return arrType.Len() >= int64(laneCount)
    }
    return false
```

This is conservative: only applies when we can statically prove the array is large enough.

## Edge Cases

- **Heap-escaped allocs**: `Heap == true` → returns false, falls through to cap check or masked path
- **Small arrays**: `arrayLen < laneCount` → returns false. Cannot safely read a full vector.
- **Phi over multiple allocs**: Conservative → returns false. Could be extended later.
- **Slice from local array**: The slice path already has sliceCap. The alloca fast-path adds coverage for direct array indexing (the `*types.Pointer → *types.Array` case).
- **MaxStackAlloc threshold**: TinyGo moves large allocs to heap. The `Heap` flag already reflects this.

## Scope

- **In scope**: Stack-allocated arrays via `*ssa.Alloc` with `Heap == false`
- **Out of scope**: Slices from stack arrays (already covered by sliceCap), phis over mixed sources, pointer arithmetic

## Testing

### Unit Tests
1. `TestSPMDIsAllocaOrigin` — verify detection of stack Alloc, rejection of heap Alloc, rejection of small arrays
2. `TestSPMDAllocaFastPathLoad` — verify load emits full load+select without branch for alloca
3. `TestSPMDAllocaFastPathStore` — verify store emits load-blend-store without branch for alloca

### E2E Validation
- Any example using local arrays in SPMD loops (or create a test case)
- Verify WASM output has no conditional branches around v128.load/v128.store for alloca paths

## Expected Impact

- **Arrays in SPMD loops**: Eliminates runtime branch entirely. 2 instructions (load) or 3 instructions (store) with zero overhead.
- **Slices**: No change (still uses cap check path).
- **Code size**: Slightly smaller for array cases (no branch/phi overhead).
