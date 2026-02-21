# Design: Remove Constrained Varying Types

**Date**: 2026-02-21
**Status**: Approved
**Scope**: Full removal of `Varying[T, N]` from language, compiler, and examples

## Motivation

Constrained varying (`Varying[T, N]`) adds significant implementation complexity across the Go frontend, TinyGo backend, and standard library. Key issues:

1. **WASM `<N x i1>` mask limitation** blocks `FromConstrained` mask return — a fundamental platform blocker
2. **Several SIGSEGV crashes** are potentially caused by constrained type handling
3. **Most use cases can be expressed** with unconstrained `Varying[T]` and accumulation patterns
4. **`go for` should be a pure abstraction** — algorithms should work regardless of hardware lane count, not be tied to a specific `N`

The one use case that genuinely needs group semantics (base64 decoder with 4-byte groups) can be solved by adding group-parameter cross-lane operations.

## Decision

Remove all constrained varying support and add `*Within` cross-lane operations as the replacement for group-based algorithms.

## What Gets Removed

### Language Specification
- `Varying[T, N]` constrained type syntax (only `Varying[T]` remains)
- `range[N]` constrained loop syntax (only `go for i := range expr` remains)
- `Varying[T, 0]` universal constraint type
- `FromConstrained` and `ToConstrained` builtin functions

### Go Frontend (`go/src/`)
- `Constraint` field on `RangeStmt` in `go/ast/ast.go` and `cmd/compile/internal/syntax/nodes.go`
- `parseSpmdConstraint()` and constrained parsing branches in both parsers
- Constraint field and methods on `SPMDType` struct in `go/types/spmd.go` and `types2/spmd.go`
- Capacity validation for constrained types in type checker (`typexpr_ext_spmd.go`)
- Constrained parser tests (~11 test cases) and type checker test files (~3 files per package)

### TinyGo Backend (`tinygo/compiler/`)
- `Constraint` field on `SPMDLoopInfo` and `SPMDTypeInfo` in `spmd.go`
- `spmdEffectiveLaneCount()` simplified to always use hardware lane count
- `createFromConstrained()` implementation (~130 lines)
- `createToConstrained()` implementation (~85 lines)
- `spmdResizeVector()` helper
- Constrained builtin interception branches
- ~10 constrained LLVM test cases

### Standard Library
- `FromConstrained` function in `lanes/lanes.go`
- `ToConstrained` function in `lanes/lanes.go`

### Examples
- `varying-universal-constrained/` — deleted entirely
- `illegal-spmd/invalid-lane-constraints.go` — deleted entirely
- `ipv4-parser/main.go` — rewritten with unconstrained varying
- `base64-decoder/main.go` — rewritten with `*Within` group functions
- `type-casting-varying/main.go` — constrained sections removed
- `type-switch-varying/main.go` — constrained switch cases removed

### Documentation
- `docs/fromconstrained_mask_issue.md` — deleted (issue no longer exists)
- Blog posts updated: `cross-lane-communication.md`, `go-spmd-ipv4-parser.md`
- `CLAUDE.md` updated: remove all `Varying[T, N]` references

## What Gets Added

### `*Within` Cross-Lane Operations

New functions in the `lanes` package for group-based cross-lane operations:

```go
// RotateWithin rotates values within groups of groupSize lanes.
// Groups are independent: lanes 0..groupSize-1 form one group,
// groupSize..2*groupSize-1 form another, etc.
// groupSize must be a compile-time constant that evenly divides the lane count.
func RotateWithin[T any](v Varying[T], offset int, groupSize int) Varying[T]

// ShiftLeftWithin shifts values left within groups, filling with zero.
func ShiftLeftWithin[T any](v Varying[T], amount int, groupSize int) Varying[T]

// ShiftRightWithin shifts values right within groups, filling with zero.
func ShiftRightWithin[T any](v Varying[T], amount int, groupSize int) Varying[T]

// SwizzleWithin permutes values within groups using indices.
func SwizzleWithin[T any](v Varying[T], indices Varying[int], groupSize int) Varying[T]
```

**Semantics:**
- `groupSize` must be a compile-time constant
- `groupSize` must evenly divide the platform lane count (compile-time check)
- Operations are completely independent between groups
- Example: `RotateWithin(v, 1, 4)` on 16 byte lanes rotates [0-3], [4-7], [8-11], [12-15] independently

**TinyGo LLVM implementation:**
- Lower to LLVM `shufflevector` with computed per-group masks
- Same builtin interception pattern as existing cross-lane ops in `compiler/spmd.go`

## Example Rewrites

### IPv4 Parser (Unconstrained)

Instead of `range[16]` forcing 16 lanes, use unconstrained `go for` and accumulate per-lane results with post-loop reduction:

```go
var dotMaskTotal lanes.Varying[uint8]
var validChars lanes.Varying[bool]

go for i, c := range input {
    isDot := c == '.'
    if isDot {
        dotMaskTotal = 1
    }
    validChars = isDot || (c >= '0' && c <= '9') || c == 0
}

// Reduce outside the loop
dotCount := reduce.Add(dotMaskTotal)
dotPositionMask := reduce.Mask(lanes.Varying[bool](dotMaskTotal > 0))
```

On WASM128, `Varying[byte]` = 16 lanes, so `[16]byte` processes in one iteration. On narrower hardware, the compiler handles cross-iteration state for varying accumulators.

### Base64 Decoder (With `*Within`)

Cross-lane operations use `*Within` with group size 4 instead of constrained types:

```go
func decodeChunk(ascii lanes.Varying[byte]) lanes.Varying[byte] {
    sextet := decodeSextet(ascii)
    shifted := lanes.Varying[uint16](sextet) << lanes.ShiftLeftWithin(
        lanes.Varying[uint16](lanes.From([]uint16{0, 6, 4, 2})), 0, 4)
    shiftedLo := lanes.Varying[byte](shifted)
    shiftedHi := lanes.Varying[byte](shifted >> 8)
    rotatedHi := lanes.RotateWithin(shiftedHi, -1, 4)
    return shiftedLo | rotatedHi
}
```

## Impact Analysis

### Complexity Budget
- **Removed**: ~1200 lines (600 Go frontend + 350 TinyGo + 250 tests/examples)
- **Added**: ~200 lines (30 stubs + 150 LLVM lowering + 20 tests)
- **Net**: ~1000 lines removed

### Issues Resolved
- `[]Varying[bool]` WASM `<N x i1>` mask limitation — gone
- `FromConstrained` mask return blocker — gone
- Potential SIGSEGV causes in constrained code paths — gone
- Constrained backend failures — gone

### E2E Test Impact
- `varying-universal-constrained` — removed (was SIGSEGV)
- `type-switch-varying` — simplified (constrained cases removed)
- `type-casting-varying` — simplified (constrained sections removed)
- `varying-array-iteration` — simplified (constrained backend issue gone)
- `base64-decoder` — rewritten with `*Within`
- `ipv4-parser` — rewritten with unconstrained
- Several compile failures may be resolved by removing constrained code paths

### Multi-Iteration Semantics Note

On WASM128, `Varying[byte]` = 16 lanes, so most byte-processing algorithms run in single iterations matching the array size. For hypothetical narrower hardware, the compiler must handle cross-iteration accumulation of varying variables used after `go for` loops. This semantic is independent of constrained varying removal and applies to all `go for` loops.
