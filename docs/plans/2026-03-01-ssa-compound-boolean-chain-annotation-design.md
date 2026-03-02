# Compound Boolean Chain Annotation in go/ssa Design

**Date:** 2026-03-01
**Updated:** 2026-03-02 (corrected construction site from logicalBinop to cond)
**Status:** Not Started
**Phase:** Post-Phase 2 (go/ssa SPMD Extensions)
**Depends On:** Phase 2 feature completion, stable compound boolean handling in TinyGo

Annotate the block chains that `go/ssa` creates when lowering `&&` and `||` expressions so TinyGo can directly identify chain structure instead of reconstructing it from CFG topology.

## Problem

Go's `&&` and `||` operators use short-circuit evaluation, which `go/ssa` lowers into block chains:

```go
// Source
if a && b && c {
    body()
}
```

```
// go/ssa output
block0 (cond.true):      // test a
    if a goto block1 else block3

block1 (cond.true.0):    // test b
    if b goto block2 else block3

block2 (cond.true.1):    // test c
    if c goto block4 else block3

block3 (if.done):        // else merge
    ...

block4 (if.then):        // body
    body()
```

When `a`, `b`, or `c` are varying, TinyGo must recognize this chain to apply correct mask transitions. Currently, TinyGo reconstructs chains by analyzing CFG topology (~167 lines in `spmdDetectCondChains`):

1. **DomPreorder asymmetry**: For `&&`, the then-body is dominated by the last inner block (visited after inner blocks). For `||`, the then-body is dominated by the outer block (visited before inner blocks). Mask transitions must be registered differently for each.

2. **Sub-chain absorption**: When visiting blocks in DomPreorder, `cond.true.1` may be detected as a chain head before `cond.true` is visited. The later detection must absorb the earlier sub-chain.

3. **Pre-registration of redirects**: Then-exit redirects must be registered during `spmdAnalyzeVaryingIf` (analysis phase), not just `spmdDetectVaryingIf` (compilation phase), because DomPreorder may visit then-exit before the last inner block.

4. **Mixed chains**: `a && b || c` creates nested chain structures that require careful ordering.

All of this complexity exists because `go/ssa` discards the AST structure (which clearly shows `&&` and `||` nodes) and replaces it with a flat CFG.

## Current TinyGo Analysis (What Gets Replaced)

### Data Structures
```go
type spmdCondChain struct {
    outerIfBlock int         // block index of outermost If
    innerBlocks  []int       // cond.true/cond.false block indices (ordered outer→inner)
    op           token.Token // token.LAND or token.LOR
    thenTarget   int         // actual then-body entry block index
    elseTarget   int         // actual else-body entry (or merge for no-else)
    combinedCond llvm.Value  // filled during compilation: a & b [& c...]
}
```

### Chain Detection (~167 lines in `spmdDetectCondChains`)
Scans blocks for `cond.true`/`cond.false` SSA patterns, validates shared successor invariants, handles sub-chain absorption when inner blocks are visited before outer blocks.

### Integration Points
- `preDetectVaryingIfs()` calls `spmdDetectCondChains()` before block compilation
- `spmdAnalyzeVaryingIf()` unwraps chain targets, handles DomPreorder asymmetry
- `spmdDetectVaryingIf()` fills LLVM condition values during compilation
- `spmdCreateValueLOR()` handles value-context `||` chains specially
- Maps: `spmdCondChains`, `spmdCondChainInner`, `spmdMaskTransitions`

## Proposed go/ssa Extension

### New Metadata: `SPMDBooleanChain`

```go
// SPMDBooleanChain represents a short-circuit boolean expression (&&/||)
// that was lowered into a chain of If blocks.
type SPMDBooleanChain struct {
    Op        token.Token    // token.LAND (&&) or token.LOR (||)
    Blocks    []*BasicBlock  // ordered chain: [first, ..., last] condition blocks
    ThenBlock *BasicBlock    // true-exit target
    ElseBlock *BasicBlock    // false-exit target
    IsVarying bool           // true when any condition in chain is varying
}

// On Function:
type Function struct {
    // ... existing fields ...
    SPMDBooleanChains []*SPMDBooleanChain
}
```

### Construction Site: `cond()` with Recursive Accumulator

The block chains are created in `cond()` (`builder.go:180`), not `logicalBinop()`. The `cond()` function recursively lowers `&&`/`||` into block chains:

```go
func (b *builder) cond(fn *Function, e ast.Expr, t, f *BasicBlock) {
    case token.LAND:
        ltrue := fn.newBasicBlock("cond.true")
        b.cond(fn, e.X, ltrue, f)    // recurse left
        fn.currentBlock = ltrue
        b.cond(fn, e.Y, t, f)        // recurse right

    case token.LOR:
        lfalse := fn.newBasicBlock("cond.false")
        b.cond(fn, e.X, t, lfalse)   // recurse left
        fn.currentBlock = lfalse
        b.cond(fn, e.Y, t, f)        // recurse right
}
```

For `a && b && c` (AST: `(a && b) && c`), the recursion creates blocks inside-out:

```
cond("(a&&b)&&c", t, f)          ← LAND, creates ltrue_C
  cond("a&&b", ltrue_C, f)       ← LAND, creates ltrue_B
    cond("a", ltrue_B, f)        ← leaf: emitIf in B0
    cond("b", ltrue_C, f)        ← leaf: emitIf in ltrue_B
  cond("c", t, f)                ← leaf: emitIf in ltrue_C
```

Result: blocks `[B0, ltrue_B, ltrue_C]` all share `elseBlock=f`.

### Stack-Based Chain Accumulator

A temporary stack on Function tracks chains during construction:

```go
// booleanChainCtx is a temporary accumulator used during cond() recursion.
// Not exported; cleared after building.
type booleanChainCtx struct {
    op           token.Token    // LAND or LOR
    sharedTarget *BasicBlock    // f for LAND, t for LOR
    blocks       []*BasicBlock  // leaf If blocks, appended in execution order
    expr         ast.Expr       // the top-level expression (for IsVarying)
}

// On Function (cleared after building):
pendingBoolChains []*booleanChainCtx
```

#### Algorithm

**At LAND/LOR nodes in `cond()`**:

```go
case token.LAND:
    // Determine the shared target for this op
    sharedTarget := f  // LAND chains share the false target

    // Check if continuing an existing chain (flat a && b && c)
    continuing := false
    if n := len(fn.pendingBoolChains); n > 0 {
        top := fn.pendingBoolChains[n-1]
        if top.op == token.LAND && top.sharedTarget == sharedTarget {
            continuing = true
        }
    }
    if !continuing {
        fn.pendingBoolChains = append(fn.pendingBoolChains, &booleanChainCtx{
            op: token.LAND, sharedTarget: sharedTarget, expr: e,
        })
    }

    ltrue := fn.newBasicBlock("cond.true")
    b.cond(fn, e.X, ltrue, f)
    fn.currentBlock = ltrue
    b.cond(fn, e.Y, t, f)

    if !continuing {
        ctx := fn.pendingBoolChains[len(fn.pendingBoolChains)-1]
        fn.pendingBoolChains = fn.pendingBoolChains[:len(fn.pendingBoolChains)-1]
        if len(ctx.blocks) > 1 {
            chain := &SPMDBooleanChain{
                Op:        token.LAND,
                Blocks:    ctx.blocks,
                ElseBlock: ctx.sharedTarget,
                ThenBlock: ctx.blocks[len(ctx.blocks)-1].Succs[0],
                IsVarying: exprHasSPMDType(fn, ctx.expr),
            }
            fn.SPMDBooleanChains = append(fn.SPMDBooleanChains, chain)
        }
    }
    return
```

**At leaf nodes** (base case `emitIf`):

```go
// After emitIf(fn, val, t, f):
if n := len(fn.pendingBoolChains); n > 0 {
    top := fn.pendingBoolChains[n-1]
    // Only add if shared target matches (excludes NOT inversions)
    if (top.op == token.LAND && f == top.sharedTarget) ||
       (top.op == token.LOR && t == top.sharedTarget) {
        top.blocks = append(top.blocks, block)
    }
}
```

#### Target Derivation

Instead of recording targets from `cond()` parameters (which may be intermediate blocks), derive them from the actual block successors:

- **LAND**: `ElseBlock = sharedTarget` (all blocks' false branch), `ThenBlock = lastBlock.Succs[0]` (last block's true branch)
- **LOR**: `ThenBlock = sharedTarget` (all blocks' true branch), `ElseBlock = lastBlock.Succs[1]` (last block's false branch)

### NOT (!) Handling

When `!` appears inside a chain (e.g., `!a && b`), the `cond()` NOT handler swaps `t` and `f` before recursing. At the leaf, the swapped `f` parameter no longer matches the chain's `sharedTarget`, so the block is silently excluded from the chain.

Example: `!a && b`:
```
cond("!a&&b", t, f)     ← LAND, push chain{sharedTarget=f}
  cond("!a", ltrue, f)  ← NOT, swaps to:
    cond("a", f, ltrue)  ← leaf: emitIf(a, f, ltrue), this f_param=ltrue ≠ chain.sharedTarget=f
                           → NOT added to chain
  cond("b", t, f)        ← leaf: emitIf(b, t, f), f_param=f = chain.sharedTarget=f
                           → added, blocks=[ltrue]
→ Only 1 block, no chain created
```

This matches TinyGo's existing behavior — its pattern detection also fails for NOT-inverted chains because the successor pattern is broken.

### Nested Mixed Chains

For `(a && b) || c`, separate chains are produced:

```
cond("(a&&b)||c", t, f)        ← LOR, push LOR{sharedTarget=t}
  cond("a&&b", t, lfalse)      ← LAND, push LAND{sharedTarget=lfalse}
    cond("a", ltrue, lfalse)    ← leaf: added to LAND chain
    cond("b", t, lfalse)        ← leaf: added to LAND chain
  → LAND chain finalized: [B0, ltrue], then=t, else=lfalse  ✓
  cond("c", t, f)               ← leaf: t=t=LOR.sharedTarget → added to LOR
→ LOR chain: [lfalse], only 1 block → no chain created
```

Result: only the inner LAND chain is captured. This correctly reflects CFG structure — `lfalse` has 2 predecessors (B0 and ltrue), so it doesn't form a simple chain pattern.

### `logicalBinop()` Integration

`logicalBinop()` handles value-context `&&`/`||` (e.g., `x := a && b && c`). It calls `cond(fn, e.X, ...)` for the left-hand side, which naturally triggers chain accumulation:

```go
func (b *builder) logicalBinop(fn *Function, e *ast.BinaryExpr) Value {
    rhs := fn.newBasicBlock("binop.rhs")
    done := fn.newBasicBlock("binop.done")
    // For LAND: cond(fn, e.X, rhs, done) — chain with sharedTarget=done
    // For LOR:  cond(fn, e.X, done, rhs) — chain with sharedTarget=done
}
```

For `x := a && b && c`, the chain within `cond(e.X="a&&b", rhs, done)` produces: `[B0, ltrue]`, LAND, then=rhs, else=done. No changes to `logicalBinop()` are needed.

### Resolution After `optimizeBlocks()`

Following the same pattern as `SPMDSwitchChain`:

```go
func resolveSPMDBooleanChains(fn *Function) {
    for _, chain := range fn.SPMDBooleanChains {
        chain.ThenBlock = resolveBlock(fn, chain.ThenBlock)
        chain.ElseBlock = resolveBlock(fn, chain.ElseBlock)
        // Blocks themselves survive since they contain If instructions
    }
}
```

Called from `finishBody()` right after `optimizeBlocks()` and `resolveSPMDSwitchChains()`.

## What TinyGo Code Gets Simplified

### Before (CFG reconstruction)
```go
// spmdDetectCondChains: ~167 lines of block scanning, successor validation,
//   sub-chain absorption, pattern matching
// spmdAnalyzeVaryingIf chain unwrapping: ~15 lines
// DomPreorder asymmetry for LOR vs LAND: ~20 lines of conditional mask skipping
// Total: ~200 lines of subtle, bug-prone code
```

### After (metadata access)
```go
func (b *spmdBuilder) spmdPopulateCondChains() {
    for _, chain := range b.fn.SPMDBooleanChains {
        if !chain.IsVarying {
            continue
        }
        cc := &spmdCondChain{
            outerIfBlock: chain.Blocks[0].Index,
            op:           chain.Op,
            thenTarget:   chain.ThenBlock.Index,
            elseTarget:   chain.ElseBlock.Index,
        }
        for _, blk := range chain.Blocks[1:] {
            cc.innerBlocks = append(cc.innerBlocks, blk.Index)
        }
        b.spmdCondChains[cc.outerIfBlock] = cc
        for _, idx := range cc.innerBlocks {
            b.spmdCondChainInner[idx] = cc
        }
    }
}
```

### Eliminated Code
- `spmdDetectCondChains()` — entire CFG-based chain reconstruction (~167 lines)
- Sub-chain absorption logic
- Block comment matching (`"cond.true"`, `"cond.false"` pattern detection)
- Predecessor count validation
- Successor invariant checking

## Scope of Changes

### x-tools-spmd/ (go/ssa)
- `go/ssa/ssa.go`: Add `SPMDBooleanChain` struct, add field on `Function`, add `booleanChainCtx` and `pendingBoolChains` field (cleared after building)
- `go/ssa/builder.go`: Modify `cond()` to push/pop/continue chain contexts at LAND/LOR nodes, append blocks at leaf nodes
- `go/ssa/spmd_varying.go`: Add `resolveSPMDBooleanChains()`, call from `finishBody()`
- `go/ssa/func.go`: Call `resolveSPMDBooleanChains()` after `optimizeBlocks()`
- `go/ssa/print.go`: Optional debug printing of chain metadata
- Tests: Verify chains for `&&`, `||`, triple `&&`, triple `||`, mixed `(a&&b)||c`, NOT exclusion, value-context chains

### tinygo/compiler/spmd.go
- Replace `spmdDetectCondChains` with `spmdPopulateCondChains` reading `fn.SPMDBooleanChains`
- Remove sub-chain absorption logic
- Remove block comment pattern matching
- Keep existing `spmdCondChain`/`spmdCondChainInner` maps (just change how they're populated)
- Keep existing mask transition logic in `spmdAnalyzeVaryingIf` (already correct)

### Risks
- **Builder refactoring**: `go/ssa`'s `cond()` implementation may change. The chain annotation is tied to the current recursive lowering approach.
- **Nested expression depth**: Deeply nested `a && b && c && d || e && f` must produce correct chain trees. Need thorough test cases.
- **Interaction with varying condition tagging**: Already implemented — `If.IsVarying` and `exprHasSPMDType` are shared infrastructure.

## Migration Strategy

1. Add `SPMDBooleanChain` to `go/ssa`, populate during `cond()` lowering
2. Add tests verifying chains for all patterns
3. In TinyGo, replace `spmdDetectCondChains` with `spmdPopulateCondChains` reading metadata
4. Assert E2E tests still produce identical WASM output
5. Remove dead detection code

## Relationship to Varying Condition Tagging

This design complements the already-implemented varying condition tagging. Together they cover:

| Feature | Varying Condition Tagging | Compound Boolean Chains |
|---------|--------------------------|------------------------|
| Simple `if v { }` | IsVarying flag on If | Not needed |
| `if a && b { }` | IsVarying on each If in chain | Chain structure (blocks, op, targets) |
| `switch v { }` | SPMDSwitchChain metadata | Not needed |

Both use the `exprHasSPMDType` helper in `spmd_varying.go`.

## Related Documents

- `docs/plans/2026-03-01-ssa-varying-condition-tagging-design.md` — companion design (implemented)
- `docs/spmd-control-flow-masking.md` — control flow masking reference
