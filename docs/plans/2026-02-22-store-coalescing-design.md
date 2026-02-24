# Store Coalescing Optimization for Varying If/Else Branches

**Date:** 2026-02-22
**Status:** Implemented
**Phase:** 2.9d (TinyGo LLVM Backend Optimization)

## Problem

When a varying if/else writes to the same destination in both branches:

```go
go for i := range N {
    if a {          // a is varying
        d[i] = b
    } else {
        d[i] = c
    }
}
```

The compiler currently emits two masked stores:

```
masked_store(d[i], b, parentMask & cond)      // then-branch
masked_store(d[i], c, parentMask & ~cond)      // else-branch
```

This is suboptimal. Since both branches write to the same destination and exactly one branch executes per lane, we can replace this with:

```
tmp = select(cond, b, c)
store(d[i], tmp, parentMask)    // single store with parent mask
```

When `parentMask` is all-ones (common case in top-level go-for body), this becomes a simple unmasked contiguous store.

## Scope

- Contiguous array stores (`d[i]` where `i` is the SPMD loop iterator)
- Non-contiguous scatter stores (varying pointer destinations)
- Nested varying if/else chains (`if a { ... } else if b { ... } else { ... }`)
- Future: varying switch cases (same pattern, N-way select)

## Design

### Pipeline Stage: SSA Pre-Analysis

Consistent with TinyGo's SPMD architecture, this optimization runs as a **pre-codegen SSA analysis pass** alongside `preDetectVaryingIfs`. The go/ssa IR provides structured CFG information (block relationships, store instructions, typed values) that is lost after LLVM lowering.

This matches the existing pattern used by `spmdAnalyzeVaryingIf`, `spmdAnalyzeContiguousIndex`, `detectSPMDForLoops`, etc.

### SSA Pattern

In go/ssa, the pattern looks like:

```
block if.then:                          (preds: if.block)
    t10 = &d[i]                         // *ssa.IndexAddr
    *t10 = b                            // *ssa.Store {Addr: t10, Val: b}
    jump merge

block if.else:                          (preds: if.block)
    t11 = &d[i]                         // *ssa.IndexAddr (same d, same i)
    *t11 = c                            // *ssa.Store {Addr: t11, Val: c}
    jump merge
```

Detection matches stores where both `Addr` values refer to the same destination:
- Same SSA value (identical pointer)
- Both `*ssa.IndexAddr` with same base (`.X`) and same index (`.Index`)

### Data Structures

```go
// spmdCoalescedStore represents a pair of stores in then/else branches
// that write to the same destination and can be merged into select + single store.
type spmdCoalescedStore struct {
    thenStore  *ssa.Store     // store in then-branch
    elseStore  *ssa.Store     // store in else-branch
    ifInfo     *spmdVaryingIf // the varying if that contains them
}

// Map from *ssa.Store → coalescing info.
// Both the then-store and else-store are keys, pointing to the same info.
spmdCoalescedStores map[*ssa.Store]*spmdCoalescedStore
```

### Analysis: `spmdAnalyzeCoalescedStores`

Called during `preDetectVaryingIfs`, after each `spmdAnalyzeVaryingIf` identifies a varying if's then/else/merge structure:

1. **Collect stores**: Walk all `*ssa.Store` instructions in then-branch blocks and else-branch blocks
2. **Match stores**: For each then-store, find a matching else-store where `spmdSameStoreAddr(thenStore.Addr, elseStore.Addr)` returns true
3. **Last-store wins**: If multiple stores to the same destination exist in one branch, match the last one (earlier stores are dead within that branch)
4. **Record pairs**: Add matched pairs to `spmdCoalescedStores`; unmatched stores remain as masked stores

### Address Matching: `spmdSameStoreAddr`

```go
func spmdSameStoreAddr(a, b ssa.Value) bool {
    // Case 1: identical SSA value
    if a == b {
        return true
    }
    // Case 2: both IndexAddr with same base and index
    idxA, okA := a.(*ssa.IndexAddr)
    idxB, okB := b.(*ssa.IndexAddr)
    if okA && okB && idxA.X == idxB.X && idxA.Index == idxB.Index {
        return true
    }
    return false
}
```

### Codegen Changes

In the `*ssa.Store` case in `compiler.go`:

1. **Check coalescing map**: Look up the store in `spmdCoalescedStores`
2. **Then-store**: Skip emission entirely (the else-store will handle both)
3. **Else-store**: Emit the coalesced store:
   a. Get `thenVal` = getValue(coalescedInfo.thenStore.Val)
   b. Get `elseVal` = getValue(coalescedInfo.elseStore.Val)
   c. Get `cond` = the varying if condition (from ifInfo)
   d. Emit `result = select(cond, thenVal, elseVal)`
   e. Emit single store of `result` to the destination address
      - For contiguous: `spmdMaskedStore(result, scalarGEP, parentMask)`
      - For scatter: `spmdMaskedScatter(result, ptrVec, parentMask)`
      - `parentMask` is the mask from the stack **before** the varying if pushed its mask

### Nested If/Else Handling

For `if a { d[i] = x } else if b { d[i] = y } else { d[i] = z }`:

go/ssa produces nested varying ifs. The inner if (`b`) is analyzed first during `preDetectVaryingIfs`. Its then/else stores (`y`/`z`) are coalesced. The outer if then sees the then-branch stores `x` and the else-branch contains the inner coalesced store.

Since the inner coalesced store emits at the else-store position (which is inside the outer else-branch), the outer analysis detects: then-branch has store to `addr`, else-branch has store to `addr` → coalesce again.

The LLVM IR result is: `select(a, x, select(b, y, z))` + single store — optimal.

### Varying Switch (Future)

Varying switch with stores to the same destination in each case follows the same pattern, extended to N-way. Each case's store value feeds into a chain of selects:

```
result = select(case1_cond, val1, select(case2_cond, val2, select(case3_cond, val3, default_val)))
```

This is deferred until varying switch masking is implemented (Phase 2.9+).

## Edge Cases

1. **Computation before store**: Both branches' computations still execute (linearized CFG). Only the final store is coalesced. This is correct — we merge the store, not the computation.

2. **Multiple stores to same destination in one branch**: Match the last store; earlier stores are dead within that branch.

3. **Store in one branch only**: No match. The store remains a masked store as today.

4. **Contiguous vs scatter mismatch**: Both stores must use the same access pattern. If they differ (shouldn't happen with same addr), skip coalescing.

5. **Type mismatch**: Both stored values must have the same type. Skip coalescing if they differ.

6. **Store to non-varying destination**: If the stored value is uniform (not varying), no benefit from coalescing. Skip — the existing unmasked store path handles this.

## Testing Plan

### Unit Tests (spmd_llvm_test.go)

1. **Simple if/else contiguous**: Verify `select` + single store emitted, no `llvm.masked.store` calls
2. **Simple if/else scatter**: Verify `select` + single masked scatter with parent mask
3. **Nested if/else chain**: Verify nested `select(a, x, select(b, y, z))` + single store
4. **Partial match**: One branch has store, other doesn't — masked store preserved
5. **Multiple stores same branch**: Last store matched correctly

### E2E Test

New example exercising the pattern with verifiable output, testing both SIMD and scalar modes produce identical results.

### Benchmark

Compare mandelbrot performance before/after, and add a micro-benchmark specifically for the if/else store pattern.

## ISPC Comparison

ISPC does not have a dedicated store-coalescing pass. It relies on:
1. Straight-line predication (both branches execute sequentially with mask updates)
2. `varyingCFDepth` trick (variables declared at same depth use unmasked stores)
3. LLVM standard passes (DSE, SimplifyCFG with HoistCommonInsts, GVN, InstCombine)

Our approach is more direct: detect the pattern at the SSA level and emit optimal code immediately. This is necessary because TinyGo uses LLVM masked store intrinsics (`llvm.masked.store`, `llvm.masked.scatter`) that LLVM's standard passes cannot optimize through as effectively as plain stores.
