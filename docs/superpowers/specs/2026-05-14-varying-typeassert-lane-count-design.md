# Varying type-assertion lane-count fix (Option B)

**Date:** 2026-05-14
**Status:** Design
**Related:** Option A in `2026-05-14-varying-iter-printf-lane-count-design.md` (landed `20b324c`)

## Problem

Option A's audit followup. The original Option B target list in the
Option A spec named `interface.go:613` and `:1013`, but a fresh audit
of the working tree shows those branches don't use lane count —
they're defensive name-generation fallbacks (`getTypeCodeName` and
`typestring`) that return `"spmd:elemname"` and never compute a
lane count. `spmdBoxedVaryingGoType` is also fine: it accepts
`laneCount` as a parameter, no internal computation.

The real Lanes()-ignoring callsite — the symmetric counterpart of
the Option A fix — is in `tinygo/compiler/spmd.go:9105`,
`createTypeAssertSPMD`:

```go
elemLLVM := b.getLLVMType(spmdType.Elem())
laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)
boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
```

This is the *unboxing* side of a `Varying[T]` interface. Post-Option-A,
the boxing side uses `Lanes()` when set. If the boxing side stamps a
typecode for `struct{[16]int32,[16]int32}` (e.g. width-fixed
`Varying[int]_16` from a byte loop) and the assertion side looks up
`struct{[4]int32,[4]int32}` (native), the type-code names won't
match and the runtime type assertion will spuriously fail.

In practice the bug is latent today because `expr.AssertedType` for
user-written `iface.(lanes.Varying[T])` carries `Lanes()==0`
(abstract — the type checker doesn't width-fix syntactic type
expressions). So `Lanes()` and `spmdEffectiveLaneCount` agree on the
native count. But this is brittle: any future change that propagates
Lanes() into `AssertedType` (e.g. through type inference, generic
specialisation, or a wider Pass A scope) would silently break
assertions on width-fixed values.

## Fix

Apply the same `Lanes() > 0 ? Lanes() : spmdEffectiveLaneCount(...)`
rule in `createTypeAssertSPMD`:

```go
elemLLVM := b.getLLVMType(spmdType.Elem())
laneCount := spmdType.Lanes()
if laneCount <= 0 {
    laneCount = b.spmdEffectiveLaneCount(spmdType, elemLLVM)
}
boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
```

Symmetric with the Option A change in `createMakeInterface`. Comment
should reference the matching rule in `createMakeInterface`
(`interface.go:131`).

`createSPMDExtractMask` at spmd.go:9160 already takes `instr.Lanes`
from the SSA instruction itself, so it's already consistent with
whatever lane count was used at boxing time. No change needed there.

## Non-changes

- Defensive `getTypeCodeName` and `typestring` branches stay
  unchanged — they don't compute lane counts and the comments
  already mark them as "should not normally be reached".
- No new helper extracted yet (`spmdResolvedLaneCount`); two
  callsites doesn't justify the abstraction. If a third surfaces,
  reconsider.

## Regression test

`test/integration/spmd/printf-varying-iter` (added in Option A)
exercises boxing only. To exercise the boxing↔unboxing symmetry, the
existing `integ_type-switch-varying` test already covers
`Varying[int]` assertions. The new symmetric path would only differ
behaviourally if Lanes() were non-zero on the AssertedType — which
nothing currently produces. So no new regression test is added; the
fix is a forward-compatibility safety net.

If a future Pass-A change starts propagating Lanes() into asserted
types, the existing `integ_type-switch-varying` test (which passes
on the post-Option-A baseline) will catch any regression.

## Verification gates

1. Full E2E `107/96/0/95/0/11 "All tests passed!"` matches the
   post-Option-A baseline. No regression.
2. WASM SIMD-vs-scalar benchmark: no >10% regression on any
   line vs the post-Option-A measurements.
3. x86-64 SSE + AVX2 benchmark: no >10% regression on any
   line vs the post-Option-A measurements.

## Out-of-scope

- Extracting a `spmdResolvedLaneCount` helper.
- Widening Pass A to propagate Lanes() into asserted/declared types.
- Audit of x-tools-spmd-side SPMDType uses.
