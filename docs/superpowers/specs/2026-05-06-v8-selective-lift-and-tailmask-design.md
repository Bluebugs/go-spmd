# SPMD v8 — Selective Lift Guard + Tail-Mask Back-Edge Predication

**Date**: 2026-05-06
**Status**: Design — pending user review
**Predecessors**: v6.1 final + v7 Phase 1 (e2e 94/0/93/0/11). x86 lo-* benchmarks regressed 5–6× and AVX2 mandelbrot regressed ~30% versus pre-v6.1 baseline.
**Goal**: Restore baseline performance while preserving the v6.1 + v7 correctness gains (n-body, n-body-nosqrt, array-counting, bit-counting, swizzle-within).

## 1. Problem statement

v6.1 introduced a blanket "skip lift for all `Varying[T]` allocas" guard so the SSA predication pass could thread masks through SPMDStore/SPMDLoad — necessary to fix n-body's NaN bug from unmasked tail-iter writes. The cost is that lift-able accumulator allocas (lo-sum's `total`, n-body's `dvx/dvy/dvz`, etc.) now stay as memory ops in the inner loop, producing vector load + store per iteration instead of an SSA phi. Measured impact:

- WASM: lo-sum 308ns → 1638ns (5.3× slower); lo-mean / lo-min / lo-max similarly regressed
- WASM: lo-clamp 6769ns → 17804ns (2.6× slower)
- x86 AVX2: mandelbrot ~940µs → ~1340µs (~30% slower)
- x86: lo-* speedups dropped from 2.5–3.2× to 0.2–0.6× (SPMD slower than scalar)
- lo-contains held (already-fast pattern, ~5×)

The unmasked-write semantic only matters in **tail iterations** of peeled SPMD loops. Main body iterations have all-ones masks; unmasked updates are correct there. So the right fix is to allow lift for vectorizable Varying types AND mask the loop-header phi back-edges in the tail body.

## 2. Guiding principle

A `Varying[T]` value crossing a loop iteration boundary needs masking precisely in tail iterations. The pre-v6 architecture handled main-body iterations correctly via lifted phi → SPMDSelect at varying-If merges. It did not handle the tail-body case where the partial mask must apply to the phi back-edge of an SPMD loop header.

This spec adds the missing piece: **insert SPMDSelect on tail-body loop-header phi back-edges** so inactive lanes preserve their previous-iter values.

## 3. Architecture: two narrow changes

### 3.1 Narrow the lift guard

Today (v6.1) skips lift for ALL `Varying[T]` allocas. v8 narrows to:

> Skip lift only when T is non-vectorizable (struct, array, slice, interface).

Vectorizable element types — int, float, byte, pointer — are eligible to lift back into phi nodes. The narrowing preserves the v7 Phase 1 alloca-sizing fix for `Varying[[]int]` (slice headers stay as `[N x slice_struct]` allocas).

### 3.2 Predication: mask tail-body loop-header phi back-edges

The predication pass already converts varying-If phis to SPMDSelect at merge blocks. v8 extends it: for peeled SPMD loops, walk the tail body. For each loop-header phi with a tail-body edge value, insert `SPMDSelect(tail_mask, edge_value, phi)` before the back-edge branch and replace the phi's tail-edge value with the SPMDSelect result.

Main body unchanged: all-ones mask makes SPMDSelect an identity; we skip emission there.

## 4. Implementation

### 4.1 File: `x-tools-spmd/go/ssa/lift.go`

**Current** (lines 432–436):
```go
if ptr, ok := alloc.Type().(*types.Pointer); ok {
    if isLanesVaryingType(ptr.Elem()) {
        return false
    }
}
```

**v8**:
```go
if ptr, ok := alloc.Type().(*types.Pointer); ok {
    if isLanesVaryingType(ptr.Elem()) && spmdElemNonVectorizable(ptr.Elem()) {
        return false
    }
}
```

**New helper** (in same file):

```go
// spmdElemNonVectorizable reports whether the element type T of Varying[T]
// is non-vectorizable (struct, array, slice, interface — types that lower to
// LLVM aggregate types and cannot be elements of an LLVM vector). For these
// the alloca representation [N x T] is the only valid lowering, so the alloca
// must survive lift; the v6.1 predication pass + v7 Phase 1 alloca sizing
// handle width-fixing and per-lane access.
//
// Vectorizable element types (int, float, byte, pointer) lift normally —
// TinyGo represents them as <N x T> LLVM vectors. The predication pass's
// tail-body back-edge masking (see spmdMaskTailBodyBackEdges) handles the
// partial-mask iter case, so lift is safe for these types.
func spmdElemNonVectorizable(t types.Type) bool {
    var inner types.Type
    if spmdT, ok := t.(*types.SPMDType); ok {
        inner = spmdT.Elem()
    } else if named, ok := t.(*types.Named); ok {
        if named.TypeArgs() != nil && named.TypeArgs().Len() > 0 {
            inner = named.TypeArgs().At(0)
        } else {
            return false
        }
    } else {
        return false
    }
    switch inner.Underlying().(type) {
    case *types.Struct, *types.Array, *types.Slice, *types.Interface:
        return true
    case *types.Pointer:
        return false // vector of pointers is fine
    default:
        return false
    }
}
```

### 4.2 File: `x-tools-spmd/go/ssa/spmd_predicate.go`

**Insert** the new helper near other predication helpers, then call it from `spmdConvertLoopOps` immediately after the existing tail-block conversions (`spmdConvertScopedMemOps`, `spmdMaskScopedCallOps`, etc.).

```go
// spmdMaskTailBodyBackEdges wraps loop-header phi back-edge values that
// originate in tail-body blocks with SPMDSelect(tail_mask, new, phi) so
// inactive lanes preserve their previous-iter phi value across tail
// iterations. This is the predication-side complement to v8's narrowed lift
// guard: lifted Varying[T] phis receive correctly-masked back-edge values
// without needing the alloca preserved.
//
// Runs only on peeled loops (loop.IsPeeled). For each tail-body block, find
// loop-header phis whose tail-edge value is produced in that block; insert
// the SPMDSelect immediately before the block's terminator and rewrite the
// phi's tail-edge to the SPMDSelect result.
//
// Main-body iterations have an all-ones mask — back-edge masking would be
// identity. We skip main-body emission entirely.
func spmdMaskTailBodyBackEdges(fn *Function, loop *SPMDLoopInfo, tailBlocks map[*BasicBlock]bool, tailMask Value) {
    if !loop.IsPeeled || tailMask == nil {
        return
    }
    // Locate the tail-side loop header (the block whose phis we'll mutate).
    tailHeader := loop.TailLoopBlock
    if tailHeader == nil {
        return
    }
    for _, instr := range tailHeader.Instrs {
        phi, ok := instr.(*Phi)
        if !ok {
            break // phis are at the top of a block
        }
        // Skip non-varying phis (iter phis, etc.).
        if _, isVarying := phi.Type().(*types.SPMDType); !isVarying {
            continue
        }
        // For each predecessor that is a tail-body block, mask the edge.
        for i, pred := range tailHeader.Preds {
            if !tailBlocks[pred] {
                continue
            }
            edgeVal := phi.Edges[i]
            if edgeVal == phi {
                continue // self-loop; SPMDSelect would be identity
            }
            if isLoopInvariant(edgeVal, loop) {
                continue
            }
            sel := &SPMDSelect{
                Cond:  tailMask,
                X:     edgeVal,
                Y:     phi,
                Lanes: loop.LaneCount,
            }
            sel.setType(phi.Type())
            sel.setBlock(pred)
            // Insert before pred's terminator (last instruction).
            spmdInsertBeforeTerminator(pred, sel)
            spmdAddReferrer(tailMask, sel)
            spmdAddReferrer(edgeVal, sel)
            spmdAddReferrer(phi, sel)
            phi.Edges[i] = sel
        }
    }
}
```

**Caller integration** (in `spmdConvertLoopOps`, immediately after existing tail-block conversions):

```go
if loop.IsPeeled {
    // ... existing tail conversions ...
    spmdConvertScopedMemOps(fn, tailBlocks, loop.TailMask, loop.LaneCount)
    spmdMaskScopedCallOps(fn, tailBlocks, loop.TailMask)
    spmdMaskScopedIndexOps(fn, tailBlocks, loop.TailMask)
    spmdMaskScopedMakeInterfaceOps(fn, tailBlocks, loop.TailMask, loop.LaneCount)
    spmdMaskTailBodyBackEdges(fn, loop, tailBlocks, loop.TailMask) // ← v8
}
```

### 4.3 No changes required

- **TinyGo backend**: zero changes. Lifted phis flow through existing codegen. SPMDSelect is already lowered correctly.
- **v7 Phase 1 alloca sizing**: still applies — non-vectorizable Varying allocas remain preserved and now correctly sized.
- **v6.1 Pass A classifier / scope check**: still applies — gates type width-fixing for the non-vectorizable allocas that survive lift.

## 5. Coverage by test

| Test | Element type | Lift behavior | Mask handling | Outcome |
|---|---|---|---|---|
| lo-sum | `Varying[int32]` | LIFTED | tail-body SPMDSelect | restored to baseline (~308ns) |
| lo-mean / lo-min / lo-max | `Varying[int32]` | LIFTED | tail-body SPMDSelect | restored |
| lo-clamp | `Varying[int]` | LIFTED | tail-body SPMDSelect | restored |
| lo-contains | `Varying[bool]` reduce | LIFTED | tail-body SPMDSelect | unchanged (already fast) |
| n-body | `Varying[float64]` accumulators | LIFTED | tail-body SPMDSelect | NaN-free, fast |
| array-counting | `Varying[[]int]` (slice) | PRESERVED (non-vectorizable) | v6.1 SPMDStore mask | unchanged correctness, no perf change |
| bit-counting | `Varying[uint8]` | LIFTED | tail-body SPMDSelect | unchanged |
| swizzle-within | `Varying[int32]` indices | LIFTED | tail-body SPMDSelect | unchanged |
| pointer-varying | `Varying[*int]` | LIFTED (pointer = vectorizable) | tail-body SPMDSelect | unchanged |
| mandelbrot | `Varying[int32]` iterations | LIFTED | tail-body SPMDSelect | restored to ~940µs / ~6.5× |

## 6. Edge cases

| Case | Handling |
|---|---|
| Phi with multiple tail predecessors (varying-If inside tail body) | Iterate edges independently; each tail predecessor gets its own SPMDSelect |
| Iter phi (loop counter, non-Varying type) | Skipped via `isVarying` check |
| Self-loop phi (edge value is the phi itself) | Skipped — SPMDSelect would be identity |
| Loop-invariant back-edge value | Skipped via `isLoopInvariant` |
| Non-peeled SPMD loop (rare) | No tail body; helper returns early via `loop.IsPeeled` guard |
| Outer regular `for` accumulator wrapping a `go for` (n-body's structure) | The accumulator is declared in outer regular `for` scope. After lift, its phi is at the outer regular loop header, but it ALSO appears at the SPMD loop header (since it's mutated inside the go for). The SPMD-header phi is the one we mask. Outer regular header phi just chains the SPMD-header phi's final value across outer iterations — no masking needed (the outer regular loop is sequential). |

## 7. Testing strategy

### Sentinel checks (every implementation step)

- n-body: `-0.169075164 / -0.169078071`
- array-counting: `Array sums: [3 3 4 18]`
- bit-counting: `Bit counts: 32`
- swizzle-within: `Correctness: PASS`
- Bucket-G + goroutine-varying: PASS
- E2E summary: 94/0/93/0/11

### Performance checks (after both changes land)

Compare against `tinybench/_baseline-2026-04-30/`:

| Bench | Pre-v6.1 baseline | v8 target |
|---|---|---|
| lo-sum (WASM) | 308ns / 2.50× | within 15%, ≥ 2.0× |
| lo-mean (WASM) | 332ns / 2.19× | within 15%, ≥ 1.8× |
| lo-min (WASM) | 324ns / 2.31× | within 15%, ≥ 2.0× |
| lo-max (WASM) | 293ns / 2.41× | within 15%, ≥ 2.0× |
| lo-clamp (WASM) | 6769ns / 1.71× | within 15%, ≥ 1.5× |
| lo-contains (WASM) | 129ns / 5.26× | within 15% |
| mandelbrot AVX2 | ~940µs / ~6.5× | within 15% |
| Hex-encode AVX2 | per baseline | within 15% |
| Base64 AVX2 | per baseline | within 15% |

The 15% tolerance allows for run-to-run noise; any larger regression must be investigated before declaring v8 done.

### TDD ordering

1. Add a unit test in `x-tools-spmd/go/ssa/spmd_lift_test.go` asserting that a `Varying[int32]` accumulator alloca is REMOVED post-lift in a function with a `go for` body. Pre-v8 fails (alloca preserved by current guard).
2. Add a unit test in `x-tools-spmd/go/ssa/spmd_predicate_test.go` asserting that an SSA Function with a peeled `go for` and a `Varying[T]` accumulator phi has SPMDSelect inserted on the tail-body back edge. Pre-v8 fails (no such insertion).
3. Implement Section 3.1 lift narrowing → test 1 passes; n-body NaN appears in e2e (test 2 fails).
4. Implement Section 3.2 tail-body back-edge masking → test 2 passes; e2e returns to 94/0/93/0/11.
5. Run full benchmarks → confirm within 15% of baseline.

### Rollback

The two changes can be reverted independently:
- Revert lift narrowing alone → back to 94/0/93/0/11 (slow but correct)
- Revert tail-mask alone → n-body NaN reappears, lo-* still fast (broken correctness)

If a deeper bug surfaces, revert in this order: tail-mask first (reverts to v6.1 + v7 known-good correctness state at slower perf), then re-investigate.

## 8. Out of scope

- Outer regular `for` loop accumulator masking (independent topic; n-body's structure is handled because the SPMD-header phi is the masked one).
- Per-store mask threading without lift — alternative architecture; v8 keeps lift+SPMDSelect.
- Main-body SPMDSelect optimization (already elided since main mask is all-ones).
- Cross-target lift behavior tuning (x86 vs WASM) — v8 applies uniformly.

## 9. Success criteria

- ✅ E2E remains 94/0/93/0/11 (no correctness regression)
- ✅ n-body and n-body-nosqrt produce `-0.169075164 / -0.169078071`
- ✅ array-counting `[3 3 4 18]`
- ✅ lo-sum / lo-mean / lo-min / lo-max / lo-clamp restored to within 15% of pre-v6.1 baseline
- ✅ mandelbrot AVX2 within 15% of ~940µs / ~6.5×
- ✅ All existing sentinels (bucket-G, swizzle-within, goroutine-varying) PASS
