# Varying Condition Tagging in go/ssa Design

**Date:** 2026-03-01
**Status:** Not Started
**Phase:** Post-Phase 2 (go/ssa SPMD Extensions)
**Depends On:** Phase 2 feature completion, stable varying if/else + varying switch in TinyGo

Annotate `If` and `Switch` instructions in `go/ssa` when their condition/tag has `*types.SPMDType`, eliminating TinyGo's `spmdValueHasVaryingSource` backward-tracing heuristic.

## Problem

TinyGo must determine whether an `If` or `Switch` instruction operates on a varying value (requiring mask-based linearization) or a uniform value (standard branch). Currently this uses `spmdValueHasVaryingSource`, which traces backward through SSA values:

```go
func (c *compilerContext) spmdValueHasVaryingSource(val ssa.Value, ...) bool {
    switch v := val.(type) {
    case *ssa.BinOp:
        return spmdValueHasVaryingSource(v.X, ...) || spmdValueHasVaryingSource(v.Y, ...)
    case *ssa.UnOp:
        return spmdValueHasVaryingSource(v.X, ...)
    case *ssa.Convert:
        return spmdValueHasVaryingSource(v.X, ...)
    case *ssa.Phi:
        // Check if phi is a loop iteration variable in active SPMD loops
    // ... many more cases
    }
}
```

This is fragile for several reasons:

1. **Convert strips SPMDType**: `byte(i % 3)` has type `byte` (not `SPMDType`), but is varying because `i` is the SPMD loop iterator. Must trace through the Convert and BinOp to discover this.

2. **BinOp type variance**: Switch tag may have `*types.Basic` while case constants carry `*types.SPMDType`. Must check both operands of comparison BinOps.

3. **Phi loop membership**: Must cross-reference phi values against `activeLoops` to determine if they're loop iterators — information that was directly available at AST construction time.

4. **Incomplete coverage**: Every new SSA value type (FieldAddr, TypeAssert, MakeInterface, etc.) needs a case in the tracing function or varying values go undetected.

Meanwhile, `go/types` already knows whether a value is varying — every expression's type is computed during type checking. The SSA builder has this type information when it creates instructions.

## Current TinyGo Analysis (What Gets Replaced)

### Varying If Detection (`spmdAnalyzeVaryingIf` in spmd.go)
```go
func (c *compilerContext) spmdAnalyzeVaryingIf(block *ssa.BasicBlock, ...) {
    ifInstr := block.Instrs[len(block.Instrs)-1].(*ssa.If)
    cond := ifInstr.Cond

    // Must trace backward through SSA to determine if cond is varying
    if c.spmdValueHasVaryingSource(cond, activeLoops, visited) {
        // Register as varying if for mask-based linearization
    }
}
```

### Varying Switch Detection
```go
// Must check BinOp operands, not just the switch tag
// because go/ssa lowers switch to comparison chains
for _, instr := range block.Instrs {
    if binop, ok := instr.(*ssa.BinOp); ok && binop.Op == token.EQL {
        if hasSPMDType(binop.X) || hasSPMDType(binop.Y) {
            // This is a varying switch comparison
        }
    }
}
```

## Proposed go/ssa Extension

### Approach: IsVarying Flag on Branch Instructions

Add a boolean flag to `If` instructions that indicates the condition is derived from a varying value. For switch statements (lowered to comparison chains), tag the first `If` in the chain.

```go
// In golang.org/x/tools/go/ssa

// If represents a conditional branch.
type If struct {
    // ... existing fields ...
    Cond      Value
    IsVarying bool  // true when Cond is or derives from a *types.SPMDType value
}
```

### Why a Flag Instead of Preserving SPMDType

The root problem is that `go/ssa` often strips `SPMDType` during lowering:
- `Convert` changes the type to the target type
- BinOp comparisons (`==`, `<`) produce `bool`, not `SPMDType(bool)`
- Switch lowering creates comparison `BinOp`s whose result type is plain `bool`

Rather than trying to preserve `SPMDType` through all these transformations (which would require changes throughout the SSA builder), a simple flag on the branch instruction captures the information that was known at construction time.

### Construction Site: If Statement Builder

```go
func (b *builder) ifStmt(s *ast.IfStmt, ...) {
    cond := b.expr(fn, s.Cond)
    ifInstr := &If{Cond: cond}

    // Tag as varying if the condition expression has SPMDType
    if _, ok := s.Cond.Type().Underlying().(*types.SPMDType); ok {
        ifInstr.IsVarying = true
    }
    // Also check if condition is derived from SPMD context
    // (e.g., comparison involving varying operands)
    if b.exprInvolvesVarying(s.Cond) {
        ifInstr.IsVarying = true
    }

    fn.currentBlock.emit(ifInstr)
}
```

### Construction Site: Switch Statement Builder

For switch statements, `go/ssa` lowers them to a chain of `If` blocks. The builder knows the switch tag type:

```go
func (b *builder) switchStmt(s *ast.SwitchStmt, ...) {
    tag := b.expr(fn, s.Tag)
    isVaryingSwitch := isTypeSPMD(tag.Type())

    // When lowering to If chain, propagate the flag
    for _, clause := range s.Body.List {
        // ... create comparison BinOp ...
        ifInstr := &If{Cond: cmp, IsVarying: isVaryingSwitch}
        fn.currentBlock.emit(ifInstr)
    }
}
```

### Additional Annotation: Switch Chain Metadata

To help TinyGo reconstruct switch structure (needed for linearization), add optional switch chain info:

```go
// SPMDSwitchChain groups the If instructions from a lowered varying switch.
type SPMDSwitchChain struct {
    TagValue   Value        // the original switch tag
    Cases      []*If        // ordered If instructions (one per case)
    DefaultBlock *BasicBlock // the default/else block
    DoneBlock    *BasicBlock // merge point after switch
}

// On Function:
type Function struct {
    // ... existing fields ...
    SPMDSwitchChains []*SPMDSwitchChain
}
```

## What TinyGo Code Gets Simplified

### Before (backward tracing)
```go
// ~80 lines: spmdValueHasVaryingSource recursive tracer
// ~40 lines: spmdAnalyzeVaryingIf with source tracing
// ~30 lines: switch detection with BinOp operand checking
// ~20 lines: spmdSwitchIfBlocks chain reconstruction
```

### After (flag check)
```go
func (c *compilerContext) spmdAnalyzeVaryingIf(block *ssa.BasicBlock, ...) {
    ifInstr := block.Instrs[len(block.Instrs)-1].(*ssa.If)
    if ifInstr.IsVarying {
        // Register for mask-based linearization — no tracing needed
    }
}

// Switch chains read directly from function metadata
for _, chain := range fn.SPMDSwitchChains {
    // All cases, default block, done block known upfront
}
```

### Eliminated Code
- `spmdValueHasVaryingSource()` — entire function (recursive tracer)
- BinOp operand type checking for switch detection
- `spmdSwitchIfBlocks` chain reconstruction heuristic
- Special-case handling for Convert-stripped SPMDType

## Scope of Changes

### x-tools-spmd/ (go/ssa)
- `go/ssa/ssa.go`: Add `IsVarying` to `If`, add `SPMDSwitchChain` struct, add field on `Function`
- `go/ssa/builder.go`: Set `IsVarying` during if/switch construction
- `go/ssa/print.go`: Print `IsVarying` flag in SSA dump
- Tests: Verify flag set for varying conditions, unset for uniform

### tinygo/compiler/spmd.go
- Simplify `spmdAnalyzeVaryingIf`: check flag instead of tracing
- Remove `spmdValueHasVaryingSource` (or reduce to fallback)
- Simplify switch detection: use `SPMDSwitchChain` metadata
- Remove `spmdSwitchIfBlocks` chain reconstruction

### Risks
- **exprInvolvesVarying helper**: Need a reliable check during SSA construction that handles all expression forms. Simpler than the current backward tracing because we have the typed AST (not just SSA values), but still needs careful implementation.
- **Dead code / optimization passes**: If `go/ssa` runs optimization passes that clone or restructure `If` instructions, the `IsVarying` flag must be preserved. Need to audit `go/ssa`'s optimization pipeline.
- **Switch lowering variations**: `go/ssa` may change how it lowers switch statements. The `SPMDSwitchChain` ties us to the current lowering strategy. If `go/ssa` changes, the chain structure must update.

## Migration Strategy

1. Add `IsVarying` flag to `If` instruction in `go/ssa`, populate during construction
2. Add `SPMDSwitchChain` metadata, populate during switch lowering
3. In TinyGo, check `IsVarying` flag first, fall back to `spmdValueHasVaryingSource` if unset
4. Run full E2E test suite, verify identical behavior
5. Remove `spmdValueHasVaryingSource` and old switch detection once validated

## Related Documents

- `docs/spmd-control-flow-masking.md` — control flow masking design
- `docs/plans/2026-02-22-store-coalescing-design.md` — store coalescing (depends on varying if detection)
