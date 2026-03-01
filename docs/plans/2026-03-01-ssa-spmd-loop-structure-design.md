# SPMD Loop Structure in go/ssa Design

**Date:** 2026-03-01
**Status:** Not Started
**Phase:** Post-Phase 2 (go/ssa SPMD Extensions)
**Depends On:** Phase 2 feature completion, stable loop peeling in TinyGo

Move SPMD `go for` loop structure knowledge from TinyGo's pattern-matching layer into `go/ssa` construction, where the AST context (RangeStmt, IsSpmd, accumulator variables) is directly available. Eliminates fragile block-name heuristics and phi-tracing in TinyGo.

## Problem

TinyGo currently reverse-engineers SPMD loop structure from `go/ssa` output using heuristics:

1. **Block name matching**: Detects `rangeindex.body`, `rangeindex.loop`, `rangeint.body` strings to identify loop components
2. **Phi tracing**: Follows phi operands backward to find loop-carried accumulators
3. **DoneBlock guessing**: Walks successors to find the merge point after the loop
4. **Merged body+loop detection**: Checks if body and loop blocks were collapsed into one (`rangeint` pattern)

These heuristics are fragile because:
- `go/ssa` block naming is an implementation detail, not a contract
- `go/ssa` may merge or restructure blocks across versions
- Accumulator detection requires multi-hop phi analysis that duplicates knowledge the SSA builder already had
- The `switch.done` block may be eliminated, causing `doneBlock` to point at the wrong block

Meanwhile, `go/ssa` has all this information when it builds the loop:
- The AST `RangeStmt` with `IsSpmd=true` and `LaneCount`
- Which variables are loop-carried (accumulator phis)
- The exact entry, body, loop-test, and done blocks
- Whether the loop is range-over-int or range-over-index

## Current TinyGo Pattern Matching (What Gets Replaced)

### Loop Detection (`detectSPMDForLoops` in spmd.go)
```go
// Current: scan blocks by name
for _, block := range fn.Blocks {
    if strings.HasSuffix(block.Comment, ".body") {
        // Check if predecessor is "rangeindex" or "rangeint" entry
        // Trace phi values to find iter, bound, accumulators
    }
}
```

### Accumulator Detection (phi tracing)
```go
// Current: follow phi back-edges to identify loop-carried values
for _, instr := range bodyBlock.Instrs {
    if phi, ok := instr.(*ssa.Phi); ok {
        // Check if one edge comes from entry (init), other from body (update)
        // Heuristic: non-iter phis with body back-edge are accumulators
    }
}
```

### DoneBlock Detection (successor walking)
```go
// Current: walk from loop-test block to find merge point
// Fragile: go/ssa may eliminate done block or merge it elsewhere
loopBlock.Succs[1] // "false" branch = done block... usually
```

## Proposed go/ssa Extension

### New SSA Instruction: `SPMDLoop`

Add an SPMD-aware loop representation to `go/ssa` that preserves the semantic structure. This does NOT replace the existing block-based CFG — it annotates it.

```go
// In golang.org/x/tools/go/ssa (x-tools-spmd/)

// SPMDLoopInfo holds structured information about an SPMD go-for loop,
// attached during SSA construction when the source RangeStmt has IsSpmd=true.
type SPMDLoopInfo struct {
    EntryBlock *BasicBlock  // block containing loop setup (bound computation)
    BodyBlock  *BasicBlock  // first block of loop body
    LoopBlock  *BasicBlock  // loop-test/increment block (may be merged with body)
    DoneBlock  *BasicBlock  // merge point after loop exits
    MergedBodyLoop bool     // true if body and loop-test are in same block (rangeint pattern)

    // Iterator
    IterPhi    *Phi         // the loop iteration variable phi
    BoundValue Value        // the upper bound of the range
    LaneCount  int          // from AST RangeStmt.LaneCount

    // Accumulators: loop-carried values other than the iterator
    Accumulators []SPMDAccumulator
}

type SPMDAccumulator struct {
    Phi       *Phi   // the loop-carried phi
    InitValue Value  // value on entry edge (initial accumulator state)
    BackValue Value  // value on back-edge (updated accumulator state)
}
```

### Attachment Point

The `SPMDLoopInfo` is attached to the `Function` during SSA construction:

```go
// In go/ssa Function
type Function struct {
    // ... existing fields ...
    SPMDLoops []*SPMDLoopInfo  // populated when GOEXPERIMENT=spmd
}
```

### Construction Site

In `go/ssa`'s `rangeStmt` builder (currently `stmt.go` or equivalent), after building the loop blocks:

```go
func (b *builder) rangeStmt(s *ast.RangeStmt, ...) {
    // ... existing block creation ...

    if s.IsSpmd {
        info := &SPMDLoopInfo{
            EntryBlock:     entry,
            BodyBlock:      body,
            LoopBlock:      loop,
            DoneBlock:      done,
            MergedBodyLoop: (body == loop),
            IterPhi:        iterPhi,
            BoundValue:     bound,
            LaneCount:      s.LaneCount,
        }
        // Identify accumulators: non-iter phis in LoopBlock/BodyBlock
        // with one edge from entry and one from body
        for _, phi := range bodyPhis {
            if phi != iterPhi {
                info.Accumulators = append(info.Accumulators, ...)
            }
        }
        fn.SPMDLoops = append(fn.SPMDLoops, info)
    }
}
```

## What TinyGo Code Gets Simplified

### Before (pattern matching)
```go
func (c *compilerContext) detectSPMDForLoops(fn *ssa.Function) []*spmdActiveLoop {
    // ~150 lines of block scanning, name matching, phi tracing
    for _, block := range fn.Blocks {
        if strings.HasSuffix(block.Comment, ".body") { ... }
    }
}
```

### After (structured access)
```go
func (c *compilerContext) detectSPMDForLoops(fn *ssa.Function) []*spmdActiveLoop {
    var loops []*spmdActiveLoop
    for _, info := range fn.SPMDLoops {
        loops = append(loops, &spmdActiveLoop{
            entryBlock:     info.EntryBlock,
            bodyBlock:      info.BodyBlock,
            loopBlock:      info.LoopBlock,
            doneBlock:      info.DoneBlock,
            iterPhi:        info.IterPhi,
            bound:          info.BoundValue,
            laneCount:      info.LaneCount,
            accumulators:   convertAccumulators(info.Accumulators),
            mergedBodyLoop: info.MergedBodyLoop,
        })
    }
    return loops
}
```

### Loop Peeling Simplification

With explicit accumulator tagging, loop peeling no longer needs:
- `spmdDetectAccumulatorPhis()` — accumulators are pre-identified
- `spmdFindDoneBlock()` — done block is explicit
- Merged body+loop detection — `MergedBodyLoop` flag
- `entryPredecessor()` heuristic — entry block is explicit

The trampoline block logic for accumulators stays in TinyGo (LLVM-specific), but the *input* to peeling is clean.

## Scope of Changes

### x-tools-spmd/ (go/ssa)
- `go/ssa/ssa.go`: Add `SPMDLoopInfo`, `SPMDAccumulator` structs, `SPMDLoops` field on `Function`
- `go/ssa/builder.go` (or `stmt.go`): Populate `SPMDLoopInfo` during range statement construction
- `go/ssa/print.go`: Optional pretty-printing for debugging
- Tests: Verify loop info populated for SPMD range statements

### tinygo/compiler/spmd.go
- Simplify `detectSPMDForLoops`: direct field access instead of pattern matching
- Remove `spmdDetectAccumulatorPhis`: replaced by `SPMDAccumulator` list
- Remove block-name-based heuristics
- Simplify loop peeling input preparation

### Risks
- **x/tools version coupling**: `x-tools-spmd/` already diverges; this adds more surface area
- **go/ssa builder internals**: May need to understand `go/ssa`'s builder well to inject at the right point
- **Test coverage**: Must verify that SSA optimizations (dead code elimination, block merging) don't invalidate the `SPMDLoopInfo` pointers after construction

## Migration Strategy

1. Add `SPMDLoopInfo` to `go/ssa` with all fields populated
2. Add TinyGo code that reads `SPMDLoopInfo` alongside existing pattern matching
3. Verify both paths produce identical `spmdActiveLoop` structures for all E2E tests
4. Remove old pattern matching code once validated

## Related Documents

- `docs/plans/2026-02-23-spmd-loop-peeling-design.md` — current loop peeling design (TinyGo-side)
- `docs/plans/2026-02-24-rangeint-loop-peeling-design.md` — rangeint variant
- `docs/ssa-generation-strategy.md` — overall SSA strategy
