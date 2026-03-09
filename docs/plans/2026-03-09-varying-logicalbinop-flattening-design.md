# Varying LogicalBinop Flattening Design

## Goal

When `logicalBinop` builds SSA for `&&`/`||` expressions in varying context,
emit `BinOp(AND/OR)` instead of branches + Phi. No CFG, no predication needed.

## Motivation

The ipv4-parser examples version has:
```go
hasOverflow := fieldLen == 3 && (b0 > 2 || (b0 == 2 && (b1 > 5 || (b1 == 5 && b2 > 5))))
```

In varying context, `logicalBinop` creates `binop.rhs`/`binop.done` blocks with
short-circuit branches. The predicate pass only handles flat single-operator
chains, not nested mixed `&&`/`||`. The nested branches survive as `br <16 x i8>`
which is invalid LLVM IR (`br` requires `i1`).

Since all operands are comparisons (no side effects), short-circuit semantics
are unnecessary. Both sides can be evaluated unconditionally as parallel vector
boolean operations.

## Detection

In `logicalBinop()` (`x-tools-spmd/go/ssa/builder.go:295`), before creating
blocks, check:

1. Expression type is `*types.SPMDType` (varying bool)
2. Both operands are side-effect-free

## Side-Effect Check

New function `isSideEffectFreeBoolExpr(fn *Function, e ast.Expr) bool`:

- `*ast.BinaryExpr` with comparison ops (`<`, `>`, `==`, `!=`, `<=`, `>=`): true
- `*ast.BinaryExpr` with `&&`/`||`: recurse on both operands
- `*ast.UnaryExpr` with `!`: recurse
- `*ast.ParenExpr`: recurse
- `*ast.Ident` (variable reference): true
- Everything else (calls, index, channel ops, etc.): false

## Emitted SSA

Instead of branches + Phi:

```
a && b  →  BinOp(AND, expr(a), expr(b))
a || b  →  BinOp(OR,  expr(a), expr(b))
```

Both sides evaluated unconditionally. Nested example
`a && (b || (c && d))`:

```
t1 = BinOp(AND, c, d)
t2 = BinOp(OR,  b, t1)
t3 = BinOp(AND, a, t2)
```

No blocks created. Recursive `logicalBinop` calls for sub-expressions
naturally handle nesting — each level hits the same check and emits AND/OR.

## Scope

- Only in `logicalBinop`, only when result type is `*types.SPMDType`
- Does NOT change `cond()` or `SPMDBooleanChain` (if-condition context)
- Does NOT affect non-varying code

## Files

- Modify: `x-tools-spmd/go/ssa/builder.go` (logicalBinop + new helper)
- Test: `x-tools-spmd/go/ssa/spmd_predicate_test.go` or new test file
