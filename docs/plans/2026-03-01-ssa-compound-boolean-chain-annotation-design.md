# Compound Boolean Chain Annotation in go/ssa Design

**Date:** 2026-03-01
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

When `a`, `b`, or `c` are varying, TinyGo must recognize this chain to apply correct mask transitions. Currently, TinyGo reconstructs chains by analyzing CFG topology:

```go
func (c *compilerContext) spmdDetectBooleanChain(block *ssa.BasicBlock, ...) {
    // Walk successors: if false-branch of block N is the false-branch of block N+1,
    // they form an AND chain. If true-branch matches, it's an OR chain.
    // Must handle sub-chain absorption for triple &&
    // Must handle asymmetric DomPreorder visitation order for && vs ||
}
```

This reconstruction is one of the most subtle parts of the TinyGo SPMD compiler:

1. **DomPreorder asymmetry**: For `&&`, the then-body is dominated by the last inner block (visited after inner blocks). For `||`, the then-body is dominated by the outer block (visited before inner blocks). Mask transitions must be registered differently for each.

2. **Sub-chain absorption**: When visiting blocks in DomPreorder, `cond.true.1` may be detected as a chain head before `cond.true` is visited. The later detection must absorb the earlier sub-chain.

3. **Pre-registration of redirects**: Then-exit redirects must be registered during `spmdAnalyzeVaryingIf` (analysis phase), not just `spmdDetectVaryingIf` (compilation phase), because DomPreorder may visit then-exit before the last inner block.

4. **Mixed chains**: `a && b || c` creates nested chain structures that require careful ordering.

All of this complexity exists because `go/ssa` discards the AST structure (which clearly shows `&&` and `||` nodes) and replaces it with a flat CFG.

## Current TinyGo Analysis (What Gets Replaced)

### Chain Detection (~60 lines)
```go
func (c *compilerContext) spmdDetectBooleanChain(block *ssa.BasicBlock, ...) {
    ifInstr := block.Instrs[len(block.Instrs)-1].(*ssa.If)
    thenBlock := block.Succs[0]
    elseBlock := block.Succs[1]

    // Check if thenBlock is another comparison block with same else target (AND)
    if len(thenBlock.Instrs) > 0 {
        if innerIf, ok := thenBlock.Instrs[len(thenBlock.Instrs)-1].(*ssa.If); ok {
            if thenBlock.Succs[1] == elseBlock {
                // AND chain detected
                // But: is thenBlock already a chain head? Absorb it.
                // But: what about DomPreorder ordering?
                // But: pre-register redirects for then-exit...
            }
        }
    }
    // Similar for OR chains (true-branch matching)
}
```

### Mask Transition Registration (~40 lines)
```go
// && chains: register pushThen/swapElse for each inner block
// || chains: skip mask transitions (outer block dominates)
// Must be done at analysis time, not compilation time
```

## Proposed go/ssa Extension

### New Metadata: `SPMDBooleanChain`

```go
// SPMDBooleanChain represents a short-circuit boolean expression (&&/||)
// that was lowered into a chain of If blocks.
type SPMDBooleanChain struct {
    Op          token.Token    // token.LAND (&&) or token.LOR (||)
    Blocks      []*BasicBlock  // ordered chain: [outer, inner0, inner1, ...]
    ThenBlock   *BasicBlock    // target when chain evaluates to true
    ElseBlock   *BasicBlock    // target when chain evaluates to false (all share this for &&)
    IsVarying   bool           // true when any condition in chain is varying
}

// On Function:
type Function struct {
    // ... existing fields ...
    SPMDBooleanChains []*SPMDBooleanChain
}
```

### Construction Site

The `go/ssa` builder processes `&&` and `||` through `cond()` or `logicalBinop()`. This is where the chain of blocks is created:

```go
func (b *builder) logicalBinop(fn *Function, e *ast.BinaryExpr, ...) {
    // Currently creates the block chain implicitly
    // After: also record the chain structure

    chain := &SPMDBooleanChain{
        Op: e.Op,
    }

    // For a && b:
    //   block0: if a goto block1 else elseBlock
    //   block1: if b goto thenBlock else elseBlock
    outerBlock := fn.currentBlock
    chain.Blocks = append(chain.Blocks, outerBlock)

    // Recursively handle nested && / ||
    // Each creates an inner block appended to chain.Blocks
    innerBlock := fn.newBasicBlock("cond.true")
    chain.Blocks = append(chain.Blocks, innerBlock)

    chain.ThenBlock = thenBlock
    chain.ElseBlock = elseBlock

    // Check if any condition involves SPMDType
    chain.IsVarying = b.exprInvolvesVarying(e.X) || b.exprInvolvesVarying(e.Y)

    fn.SPMDBooleanChains = append(fn.SPMDBooleanChains, chain)
}
```

### Nested Chain Handling

For `a && b || c`, the builder produces:
```go
// Outer chain: LOR
//   Blocks: [block_or_outer, block_or_inner]
// Inner chain: LAND  (nested inside block_or_outer)
//   Blocks: [block_and_outer, block_and_inner]
```

The chain tree naturally matches the AST nesting. TinyGo walks the chains in order, with inner chains automatically contained within outer chain blocks.

## What TinyGo Code Gets Simplified

### Before (CFG reconstruction)
```go
// Chain detection: ~60 lines of successor/predecessor analysis
// Sub-chain absorption: ~20 lines of existing chain head checking
// DomPreorder workarounds: ~30 lines of pre-registration and ordering
// Mask transition asymmetry: ~40 lines of && vs || special cases
// Total: ~150 lines of subtle, bug-prone code
```

### After (metadata access)
```go
func (c *compilerContext) processCompoundBooleans(fn *ssa.Function) {
    for _, chain := range fn.SPMDBooleanChains {
        if !chain.IsVarying {
            continue
        }
        switch chain.Op {
        case token.LAND:
            // Register pushThen mask for each inner block
            for i := 1; i < len(chain.Blocks); i++ {
                c.registerMaskTransition(chain.Blocks[i], pushThen)
            }
            // Register swapElse for else block
            c.registerMaskTransition(chain.ElseBlock, swapElse)

        case token.LOR:
            // OR chains: outer block dominates, minimal mask work
            c.registerMaskTransition(chain.ElseBlock, pushElse)
        }
    }
}
```

### Eliminated Code
- `spmdDetectBooleanChain()` — entire CFG-based chain reconstruction
- Sub-chain absorption logic
- DomPreorder pre-registration workarounds
- `spmdLORChainBlocks` / `spmdLANDChainBlocks` tracking maps

## Scope of Changes

### x-tools-spmd/ (go/ssa)
- `go/ssa/ssa.go`: Add `SPMDBooleanChain` struct, add field on `Function`
- `go/ssa/builder.go`: Record chain during `logicalBinop`/`cond` lowering
- `go/ssa/print.go`: Optional debug printing
- Tests: Verify chains populated for `&&`, `||`, nested combinations

### tinygo/compiler/spmd.go
- Replace `spmdDetectBooleanChain` with direct chain metadata access
- Remove sub-chain absorption logic
- Simplify DomPreorder mask transition registration
- Remove `spmdLORChainBlocks` / `spmdLANDChainBlocks` maps

### Risks
- **Builder refactoring**: `go/ssa`'s `cond()`/`logicalBinop()` implementation may change. The chain annotation is tied to the current lowering approach.
- **Nested expression depth**: Deeply nested `a && b && c && d || e && f` must produce correct chain trees. Need thorough test cases.
- **Interaction with varying condition tagging**: The `IsVarying` flag on chains and on `If` instructions should be consistent. If both features are implemented, they should share the `exprInvolvesVarying` helper.

## Migration Strategy

1. Add `SPMDBooleanChain` to `go/ssa`, populate during logical expression lowering
2. In TinyGo, read chain metadata alongside existing CFG-based detection
3. Assert both approaches produce identical chain structures for all E2E tests
4. Remove CFG-based chain detection once validated

## Relationship to Varying Condition Tagging

This design complements the varying condition tagging design (`ssa-varying-condition-tagging-design.md`). Together they cover:

| Feature | Varying Condition Tagging | Compound Boolean Chains |
|---------|--------------------------|------------------------|
| Simple `if v { }` | IsVarying flag on If | Not needed |
| `if a && b { }` | IsVarying on each If in chain | Chain structure (blocks, op, targets) |
| `switch v { }` | SPMDSwitchChain metadata | Not needed |

Both share the `exprInvolvesVarying` helper in the SSA builder. Implementing them together would be efficient.

## Related Documents

- `docs/plans/2026-03-01-ssa-varying-condition-tagging-design.md` — companion design
- `docs/spmd-control-flow-masking.md` — control flow masking reference
