# Rangeint Loop Peeling + spmdFuncIsBody Clarification

**Date**: 2026-02-24
**Status**: Approved
**Scope**: Extend SPMD loop peeling to rangeint loops, add accumulator phi forwarding, clarify spmdFuncIsBody invariant, refactor unified helpers.

## Problem

Loop peeling is restricted to `rangeindex` (range-over-slice) loops. The mandelbrot benchmark uses `go for i := range width` — a `rangeint` loop — so it cannot benefit from peeling. This forces 4x `v128.store32_lane` with bitmask checks instead of a single `v128.store` per group.

The `spmdFuncIsBody` exclusion in `spmdShouldPeelLoop` is dead code (structurally unreachable) but undocumented, causing confusion about what blocks mandelbrot.

## Design

### Commit 1: Rangeint Loop Peeling + Accumulator Phi Forwarding

#### Gate Removal

Remove `!loop.isRangeIndex` from `spmdShouldPeelLoop`. The existing infrastructure already has rangeint-aware code paths at all 5 touch points:

| Touch point | Location | rangeint handling |
|---|---|---|
| Entry redirect | `compiler.go` Jump handler (~line 2220) | Checks both `bodyBlocks` and `loopBlocks` maps |
| Loop exit redirect | `compiler.go` If handler (~line 2107) | Loop block in `loopBlocks` for both patterns |
| Bound override | `spmd.go` `spmdIsLoopExitBound` (~line 558) | Checks `loopBlocks` — works for both |
| Tail.check phi wiring | `compiler.go` (~line 1801) | Has explicit rangeint branch (body pred != loop) |
| Tail body emission | `spmd.go` `emitSPMDTailBody` (~line 5388) | Detects iter phi, emits prologue, sets override |

#### spmdFuncIsBody Assertion

Replace silent `return false` with a panic assertion documenting the structural invariant:

```go
// spmdFuncIsBody implies spmdLoopState == nil, which means there are no
// go-for loops to peel. This function is only called for loops in
// spmdLoopState.activeLoops, so spmdFuncIsBody should never be true here.
if b.spmdFuncIsBody {
    panic("spmd: spmdShouldPeelLoop called with spmdFuncIsBody=true")
}
```

#### Accumulator Phi Forwarding

For loops with >1 phi in the loop block (e.g., `total += values[i]`), the accumulator's final value must flow from main loop to tail via phi-based forwarding.

**Data structure**: Add `accumulatorPhis` to `spmdPeeledLoop`:
```go
type spmdPeeledLoop struct {
    // ... existing fields ...
    accumulatorPhis map[*ssa.Phi]llvm.Value // SSA accumulator phi → tail.check LLVM phi
}
```

**Flow**:
1. `spmdShouldPeelLoop`: Remove `phiCount > 1` restriction.
2. `spmdCreateTailBlocks` or `emitSPMDTailCheck`: For each phi in the loop block beyond the iterator phi, create a corresponding LLVM phi in `tail.check`.
3. `emitSPMDTailBody`: When encountering an accumulator phi in a body/loop block, override `b.locals[phi]` to the corresponding tail.check accumulator phi.
4. Phi wiring (after `emitSPMDTailBody`): For each accumulator phi, add incoming values:
   - From entry predecessor: the phi's initial value (entry-edge SSA value, compiled via `b.getValue`)
   - From main loop exit: `b.locals[ssaPhi]` (main loop's final accumulator value)

**Note**: The iterator phi itself is already handled by the existing `tailIterPhi` mechanism. Accumulator phis are additional phis that carry state across iterations (e.g., running sums, min/max trackers).

### Commit 2: Unified CFG Helpers Refactor

Add helper methods to `spmdActiveLoop` to eliminate rangeint/rangeindex branches:

```go
// entryTargetBlockIndex returns the SSA block index that the entry
// predecessor jumps to: body block (rangeint) or loop block (rangeindex).
func (loop *spmdActiveLoop) entryTargetBlockIndex(state *spmdLoopState) int

// entryPredecessor returns the SSA block that enters the loop from outside
// (not a back-edge from inside the loop body).
func (loop *spmdActiveLoop) entryPredecessor(fn *ssa.Function, state *spmdLoopState) *ssa.BasicBlock

// iterPhiBlockIndex returns the SSA block index containing the iterator phi:
// body block (rangeint) or loop block (rangeindex).
func (loop *spmdActiveLoop) iterPhiBlockIndex(state *spmdLoopState) int
```

Replace `if loop.isRangeIndex { ... } else { ... }` branches in:
- Tail.check phi wiring (~lines 1784-1813)
- `emitSPMDTailBody` body block handling (~lines 5338-5398)

Keep `isRangeIndex` field for cases where the distinction genuinely matters (e.g., `bodyIterValue` semantics, `initEdgeIndex`, prologue emit location).

## Testing

- Run existing SPMD LLVM tests (107 tests) — no regressions
- Run mandelbrot-bench E2E — verify peeling activates (single `v128.store` in main loop)
- Run mandelbrot-bench benchmark — measure speedup improvement
- Add LLVM test for rangeint peeling (verify aligned bound, main/tail phases)
- Add LLVM test for accumulator phi forwarding (verify phi wiring across main/tail)
- Run full E2E test suite — no regressions

## Expected Impact

- Mandelbrot main loop: 4x `v128.store32_lane` → 1x `v128.store`
- Expected speedup improvement: ~2.9x → ~3.3-3.5x (eliminating store overhead)
- All other examples using rangeint `go for` loops also benefit
