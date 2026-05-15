# Varying iterator printf lane-count fix

**Date:** 2026-05-14
**Status:** Design

## Problem

In a byte-element SPMD loop, `fmt.Printf("%v", i)` on the loop iterator
(or on any other `Varying[int]` value whose width has been fixed to the
loop's lane count) only prints the first 4 lanes instead of all 16.

Reproducer:

```go
go for i := range dst { // dst is []byte
    v := src[i>>1]
    fmt.Printf("%v := src[%v>>1]\n", v, i)
    ...
}
```

The `v` (`Varying[byte]`) prints all 16 lanes correctly; `i`
(`Varying[int]_16`) prints only `[0 1 2 3]`.

## Root cause

The boxed-varying memory layout is correct: the data array
allocated for `i` is `[16]int32`, populated via four
`v128.const + v128.or + v128.store` chunks (verified by inspecting the
emitted WASM). But the reflect *typecode* attached to the interface
value describes a `[4]int32` array.

Two code paths disagree about lane count:

1. `tinygo/compiler/compiler.go:548` (`getLLVMType` for `*types.SPMDType`):
   prefers `typ.Lanes()` when set, falls back to
   `spmdLaneCount(elemType)` (the native register-width-derived count).
   This is the v6+ width-typed Varying pattern — it produces
   `<16 x i32>` for `Varying[int]_16`.

2. `tinygo/compiler/interface.go:131` (`createMakeInterface` SPMD branch):
   unconditionally calls `c.spmdEffectiveLaneCount(spmdType, elemLLVM)`,
   which ignores `spmdType.Lanes()` and always returns the native
   `registerBytes/elemSize` count. For `Varying[int]_16` on WASM128
   it returns 4.

The runtime side (`go/src/fmt/print.go:928 printSPMDVarying`) iterates
`values.Len()` lanes drawn from the reflect type, so the typecode's
truncated width is what the user actually sees.

## Fix (Option A — minimal)

In `tinygo/compiler/interface.go`, the SPMD branch of
`createMakeInterface` (line 131) should honor `spmdType.Lanes()` when
set, exactly mirroring the rule already in `getLLVMType`:

```go
if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
    elemLLVM := c.getLLVMType(spmdType.Elem())
    laneCount := spmdType.Lanes()
    if laneCount <= 0 {
        laneCount = c.spmdEffectiveLaneCount(spmdType, elemLLVM)
    }
    return c.getTypeCode(c.spmdBoxedVaryingGoType(spmdType, laneCount))
}
```

This makes the reflect typecode describe a `[N]T` array of the same
width as the LLVM vector value that goes through `vectorToArray` in
`compiler.go:3792`, eliminating the data/typecode size mismatch.

## Non-changes

- No SSA-level changes are needed. Pass A/B already width-fixes the
  iter phi's `*types.SPMDType` to `Lanes()=16` for byte loops — that
  is precisely why the LLVM data layout was already correct.
- No `go/src/fmt` changes. The runtime is correct; it trusts the
  reflect type.
- No new lanes/reduce builtins.

## Regression test

Add an integ-level test analogous to the existing
`integ_printf-varying-index` (added in commit `f3afc3fb` /
`47db08b`):

`test/integration/spmd/integ_printf-varying-iter/main.go` — a
byte-element `go for i := range dst { fmt.Printf("%v\n", i) }` that
asserts the printed output contains all 16 lane values (`[0 1 2 ...
15]`) rather than `[0 1 2 3]`. Promote to Level 8 (`dual_`) so both
SIMD and scalar modes are validated. Scalar mode reduces lane count to
1 and should print a single value.

Optionally, exercise width propagation through arithmetic by also
printing `i*2` and `i&1` in the same loop body.

## Verification gates

1. Unit tests for the touched file pass.
2. Full E2E pass (`test/e2e/spmd-e2e-test.sh`) — expect the same
   `106/95/0/94/0/11 "All tests passed!"` baseline after the new
   integ test is counted in (so `107/96/0/95/0/11`).
3. E2E benchmark (`test/e2e/spmd-benchmark.sh`) shows no regression
   on hex-encode / lo-* / mandelbrot.
4. x86 E2E benchmark (`test/e2e/spmd-benchmark-x86.sh`) also clean.

## Follow-up — Option B (separate spec)

Audit the other `spmdEffectiveLaneCount` / native-lane-count uses on
the boxing and reflect/typecode paths in `interface.go` (notably
lines 613 and 1013, and `spmdBoxedVaryingGoType` itself) for the
same "ignore Lanes()" sin. Done in a follow-up after A is green.
