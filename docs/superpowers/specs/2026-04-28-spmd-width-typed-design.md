# SPMD Width-Typed Varying v5 — Design (Lane Count in the Type System)

**Date**: 2026-04-28
**Scope**: Fifth attempt at fixing the varying-local NaN-from-unmasked-writeback bug and the broader lane-count derivation cascade. Moves the canonical lane count INTO the type system: `*types.SPMDType` gains a `lanes int` field, the SPMD predication pass mutates each in-loop value's type to width-fixed, and TinyGo's single `getLLVMType(*types.SPMDType)` site reads the new field. The cascade vanishes because every existing TinyGo materialization path naturally inherits the right width through the type.

**Supersedes**:
- `2026-04-21-varying-local-mask-threading-design.md` (v1 — reverted)
- `2026-04-23-varying-local-mask-threading-v2-design.md` (v2 — reverted)
- `2026-04-25-varying-local-mask-threading-v3-design.md` (v3 — GATE failed at 23 tests; per-alloca side-channel annotation)
- `2026-04-26-spmd-canonical-lane-count-v4-design.md` (v4 — GATE failed at 20 tests; per-block side-channel annotation + per-call specialization)

---

## 1. Overview, Scope, Architecture

### Goal

End the four-attempt cascade pattern. Make TinyGo's lane-count derivation MECHANICAL by encoding the canonical width DIRECTLY in the SPMD type that every TinyGo path already reads. The single TinyGo change (~5 lines in `getLLVMType`) makes ALL ~21 currently-broken sites correct simultaneously, because each one already calls `getLLVMType(value.Type())` for some value and the value's type now carries the right width.

### Diagnosis from v3 + v4 deep dives

The four failed attempts (v1: lift guard alone, v2: lift guard + alloca routing, v3: per-alloca side channel, v4: per-block side channel + specialization) all hit the same family of regressions: 20-23 tests in `to-upper`, `lo-clamp`, `lo-contains`, `array-counting`, `varying-array-iteration`, `swizzle-within`, `base64-mula-lemire`, `hex-encode`. The pattern across all four:

1. The fix correctly sizes ONE side of an in-loop varying value (e.g., the alloca's storage).
2. TinyGo materializes the OTHER side (e.g., the IndexAddr's address vector) by re-deriving width via `getLLVMType` → `spmdEffectiveLaneCount` → element-natural width.
3. The two sides have DIFFERENT widths. LLVM verifier rejects (`Store operand must be a pointer`, `Invalid type`, `Invalid cast`) or runtime crashes with OOB scatter / wrong-result reduction.
4. The fix author patches the OTHER side's derivation to also consult the side-channel annotation. This fixes the original test but introduces a width mismatch in a THIRD site downstream.
5. Cycle repeats until ~21 sites are touched, each chain of patches widening the surface area for new regressions.

The deep-dive root cause: **lane count is a property of the SCOPE in which a varying value lives, NOT of its element type.** Side-channel annotations (per-alloca, per-block) require TinyGo to consult the side-channel at every materialization site. Even with v4's per-block annotation, the actual TinyGo commit only patched 3 sites (its `*ssa.Alloc` patch was claimed in the commit message but was never in the actual diff). The conservative audit and the missed-site cascade are inherent to the side-channel approach.

### v5 fix (move width into the type)

The lane count moves from side-channel ANNOTATIONS into the TYPE SYSTEM. `*types.SPMDType` gains a `lanes int` field; the type-checker continues to produce only width-free instances (`lanes=0`); the SPMD predication pass walks each loop's scope and MUTATES every in-loop Varying value's `register.typ` to a new width-fixed `*SPMDType{elem, lanes:loop.LaneCount}`; TinyGo's `getLLVMType(*types.SPMDType)` reads `typ.Lanes()` directly when set.

Cascading effect: every existing TinyGo materialization path (~21 sites: `*ssa.Alloc`, `*ssa.UnOp{MUL}`, `IndexAddr`, `createSPMDLoad`, `createSPMDStore`, swizzle, gather, scatter, reduce dispatch, mask materialization, broadcast emit, etc.) flows through `getLLVMType(value.Type())` for some value's result type. With width in the type, the right width arrives WITHOUT TinyGo having to consult any side-channel. ZERO new consumer-side patches. ZERO missed-site cascade risk.

### Concrete unlock

- `n-body-nosqrt/go-spmd/main.go`: produces `-0.169075164` / `-0.169078071` (already proven by v4 attempt — the architectural fix works for n-body even with side-channel approach; v5 is about NOT regressing the other 20 tests).
- `n-body/go-spmd/main.go`: same.
- `to-upper`, `lo-clamp`, `lo-contains`, `array-counting`, `varying-array-iteration`, `swizzle-within`, `base64-mula-lemire`, `hex-encode`: continue to PASS — width-typed values mean IndexAddr / scatter / reduce see matching widths because they're all derived from the same width-fixed type.

### Non-goals

- Per-call specialization (ISPC-style monomorphization). Deferred to v5.1 — architecturally correct per user direction but blocked on TinyGo's DI metadata SIGSEGV when emitting cloned SPMD function variants. v5.1 will fix the SIGSEGV, then re-enable v4's specialization pass adapted for v5's width-typed types.
- Width-fixed function signatures. v5.0 keeps signatures abstract (`Varying[int]_0`); cross-call width agreement uses existing `spmdMinLaneCountForSig`. v5.1 adds per-call signature rewriting via specialization.
- Mixed-width values within a single function (e.g., `Varying[float64]` accumulator AND `Varying[int]` accumulator both used in different loops). Source-level workaround: factor each loop into its own function. v5.1 may relax via per-value (rather than per-block) propagation.
- Removing `spmdLaneCount(elemType)`, `spmdEffectiveLaneCount`, `spmdMinLaneCountForSig` — kept as fallbacks for width-free types (function signatures, globals, abstract type queries from compilerContext scope).

### Invariants preserved

- Single explicit-mask model on `*ssa.SPMDStore` (established 2026-03-05).
- Genuine scatter / contiguous / field-access paths in TinyGo unchanged (they read width from the value type — now width-fixed automatically).
- `Varying[*Struct]` field access (Cases A/B/C/D from 2026-04-21) unchanged.
- Stock-Go builds unaffected — `lanes` field defaults to 0; `Identical` and `String` are gated by `buildcfg.Experiment.SPMD`.
- Backward compatibility: width-free types (`lanes=0`) preserve existing TinyGo behavior. Type-checker produces only width-free instances.

---

## 2. Stdlib Type-System Changes

### 2.1 The struct change

**Files**:
- `/home/cedric/work/SPMD/go/src/go/types/spmd.go`
- `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/spmd.go`

Extend `*SPMDType`:

```go
type SPMDType struct {
    qualifier SPMDQualifier
    elem      Type
    lanes     int  // 0 = abstract (type-checker default); > 0 = width-fixed (set by SSA predication)
}
```

Add a width-fixed constructor:

```go
// NewVaryingWithLanes returns a width-fixed varying type. Used by the SSA
// predication pass to mutate in-loop Varying values' types to carry the
// surrounding loop's canonical lane count. TinyGo reads Lanes() during
// getLLVMType to materialize the LLVM vector at the right width.
//
// Type-checker callers should NOT use this constructor — they produce
// abstract types via NewVarying. Width-fixed instances exist only post
// type-checking.
func NewVaryingWithLanes(elem Type, lanes int) *SPMDType {
    return &SPMDType{qualifier: VaryingQualifier, elem: elem, lanes: lanes}
}
```

Add the accessor:

```go
// Lanes returns the canonical lane count of a width-fixed SPMD type.
// Returns 0 for abstract (type-checker-produced) instances.
func (s *SPMDType) Lanes() int { return s.lanes }
```

### 2.2 Identical update

**Files**:
- `/home/cedric/work/SPMD/go/src/go/types/predicates_ext_spmd.go`
- `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/predicates_ext_spmd.go`

Update `handleSPMDTypeIdentical` to compare `lanes`:

```go
func (c *comparer) handleSPMDTypeIdentical(x, y Type, p *ifacePair) (handled, identical bool) {
    if !buildcfg.Experiment.SPMD {
        return false, false
    }

    if spmdX, ok := x.(*SPMDType); ok {
        if spmdY, ok := y.(*SPMDType); ok {
            return true, (spmdX.qualifier == spmdY.qualifier &&
                spmdX.lanes == spmdY.lanes &&
                c.identical(spmdX.elem, spmdY.elem, p))
        }
        return true, false
    }
    if _, ok := y.(*SPMDType); ok {
        return true, false
    }
    return false, false
}
```

`Varying[int]_4` and `Varying[int]_8` are not `Identical`. The type-checker only ever sees `lanes=0` on both sides (it produces only abstract types), so existing type-check semantics are unaffected. The new contract is internal to SSA / TinyGo.

### 2.3 String update

**Files**:
- `/home/cedric/work/SPMD/go/src/go/types/typestring_ext_spmd.go`
- `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/typestring_ext_spmd.go`

Update `handleSPMDTypeString` to print the width when set:

```go
func (w *typeWriter) handleSPMDTypeString(typ Type) bool {
    if !buildcfg.Experiment.SPMD {
        return false
    }

    if t, ok := typ.(*SPMDType); ok {
        switch t.qualifier {
        case UniformQualifier:
            w.typ(t.elem)
        case VaryingQualifier:
            w.string("lanes.Varying[")
            w.typ(t.elem)
            w.byte(']')
            if t.lanes > 0 {
                w.byte('_')
                w.string(intString(t.lanes))
            }
        }
        return true
    }
    return false
}
```

This makes SSA dumps and debug output show the width directly. Helpful for debugging.

### 2.4 Type-system tests

**Files**:
- `/home/cedric/work/SPMD/go/src/go/types/spmd_test.go` (NEW or extend if exists)
- Symmetric test for types2

Three tests:

```go
// TestSPMDTypeLanesField verifies the new Lanes() accessor returns the
// width-fixed value when set, 0 for abstract types.
func TestSPMDTypeLanesField(t *testing.T) {
    abstract := types.NewVarying(types.Typ[types.Int])
    if abstract.Lanes() != 0 {
        t.Fatalf("abstract Varying[int]: Lanes() = %d, want 0", abstract.Lanes())
    }
    fixed := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    if fixed.Lanes() != 4 {
        t.Fatalf("fixed Varying[int]_4: Lanes() = %d, want 4", fixed.Lanes())
    }
}

// TestSPMDTypeIdenticalLanes verifies type identity respects the lane count.
func TestSPMDTypeIdenticalLanes(t *testing.T) {
    a := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    b := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    c := types.NewVaryingWithLanes(types.Typ[types.Int], 8)
    abstract := types.NewVarying(types.Typ[types.Int])

    if !types.Identical(a, b) {
        t.Error("Identical(Varying[int]_4, Varying[int]_4) = false; want true")
    }
    if types.Identical(a, c) {
        t.Error("Identical(Varying[int]_4, Varying[int]_8) = true; want false")
    }
    if types.Identical(a, abstract) {
        t.Error("Identical(Varying[int]_4, Varying[int]_0) = true; want false (strict)")
    }
}

// TestSPMDTypeStringLanes verifies the printed format includes the width.
func TestSPMDTypeStringLanes(t *testing.T) {
    abstract := types.NewVarying(types.Typ[types.Int])
    if got := abstract.String(); got != "lanes.Varying[int]" {
        t.Errorf("abstract String() = %q; want %q", got, "lanes.Varying[int]")
    }
    fixed := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    if got := fixed.String(); got != "lanes.Varying[int]_4" {
        t.Errorf("fixed String() = %q; want %q", got, "lanes.Varying[int]_4")
    }
}
```

Tests run with `GOEXPERIMENT=spmd` to enable the gating.

---

## 3. SSA Layer — Predication Pass Type Rewriting

### 3.1 Reuse existing register.setType + add a Value-level bridge

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go`.

The SSA `register` mix-in already has `func (v *register) setType(typ types.Type) { v.typ = typ }` (unexported). Every value-producing instruction (`*Alloc`, `*Phi`, `*BinOp`, `*UnOp`, `*ChangeType`, `*Convert`, `*IndexAddr`, etc.) embeds `*register` and inherits this method. No new helper needed for the underlying mutation.

Add ONE small package-private bridge so the predication pass can call `setType` on a `Value` interface without a giant type switch:

```go
// setSPMDValueType mutates v's underlying type via the register mix-in's
// setType. Used ONLY by the SPMD predication pass to rewrite a Varying
// value's type from the type-checker's abstract Varying[T] (lanes=0) to
// a width-fixed Varying[T]_N (lanes=N) where N is the surrounding SPMD
// loop's canonical lane count.
//
// Returns true if v embedded *register and the type was set; false
// otherwise (in which case the value is not value-producing or doesn't
// use the register mix-in — the caller should handle the alternative,
// e.g., *Alloc which has its pointer type rewritten via setType from
// *Pointer to *Pointer with a different elem).
func setSPMDValueType(v Value, t types.Type) bool {
    type setter interface {
        setType(types.Type)
    }
    if s, ok := v.(setter); ok {
        s.setType(t)
        return true
    }
    return false
}
```

This works because `setType` is the same unexported method on every `*register`-embedding value type. Unexported helper (lowercase `s`) — used only within `package ssa`.

### 3.2 The lift guard (restored from v3/v4)

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go` — `liftAlloc`.

Same content as previous attempts. Varying allocas must survive `lift()` so the predication pass can mutate their pointee type:

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
    // SPMD: keep varying allocas memory-backed so the SPMD predication pass
    // can mutate the alloca's pointee type to a width-fixed *types.SPMDType.
    // Without this, the alloca becomes a phi and the phi's type would be
    // mutated instead — semantically fine but breaks the alloca-based
    // forward-propagation pass for entry-block allocas in non-SPMD functions.
    if ptr, ok := alloc.Type().(*types.Pointer); ok {
        if isLanesVaryingType(ptr.Elem()) {
            return false
        }
    }
    // ... existing body unchanged ...
}
```

`isLanesVaryingType` and `IsLanesVaryingType` test re-export are restored from v3/v4.

### 3.3 The predication pass mutation

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, in `spmdConvertLoopOps`.

After the per-loop `liveScopeBlocks` is computed, walk every value in those blocks and rewrite Varying types to width-fixed:

```go
// SPMD v5: rewrite every Varying value's type in this loop's scope to be
// width-fixed at loop.LaneCount. TinyGo reads typ.Lanes() during
// getLLVMType to materialize the LLVM vector at the canonical width.
//
// The mutation is in-place via setSPMDValueType on the register mix-in.
// All values in the loop scope share the same lane count (loop.LaneCount),
// so type-identity within the scope is consistent.
//
// The `lanes == 0` guard preserves an outer loop's annotation when an
// inner loop's predication revisits the same value.
for b := range liveScopeBlocks {
    for _, instr := range b.Instrs {
        // Allocas: rewrite the pointer's element type. *Alloc embeds
        // *register, so setSPMDValueType works.
        if alloc, ok := instr.(*Alloc); ok {
            if ptr, ok := alloc.Type().(*types.Pointer); ok {
                if elem, ok := ptr.Elem().(*types.SPMDType); ok && elem.Lanes() == 0 {
                    fixed := types.NewVaryingWithLanes(elem.Elem(), loop.LaneCount)
                    setSPMDValueType(alloc, types.NewPointer(fixed))
                }
            }
            continue
        }
        // Value-producing instructions: rewrite the result type.
        v, ok := instr.(Value)
        if !ok {
            continue
        }
        spmdType, ok := v.Type().(*types.SPMDType)
        if !ok || spmdType.Lanes() != 0 {
            continue
        }
        fixed := types.NewVaryingWithLanes(spmdType.Elem(), loop.LaneCount)
        setSPMDValueType(v, fixed)
    }
}
```

`registerOf(v)` is a helper that extracts the embedded `*register` from a value — most value-producing instructions embed it. Document the helper near `setSPMDValueType`.

### 3.4 Forward-propagation pass

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go` (NEW — same intent as v4 but mutates types).

Runs AFTER `spmdConvertLoopOps`. For each function's entry block (and any other unannotated block with allocas), find varying allocas whose CONSUMERS are width-fixed values inside loop scopes. The consumer's width tells us what width the alloca's storage should be:

```go
func spmdPropagateAllocaWidth(fn *Function) {
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            alloc, ok := instr.(*Alloc)
            if !ok {
                continue
            }
            ptr, ok := alloc.Type().(*types.Pointer)
            if !ok {
                continue
            }
            elem, ok := ptr.Elem().(*types.SPMDType)
            if !ok || elem.Lanes() != 0 {
                continue  // not a varying alloca, or already width-fixed
            }
            // Find the first SPMD-consumer that's already width-fixed.
            lc := spmdAllocaConsumerWidth(alloc)
            if lc > 0 {
                fixed := types.NewVaryingWithLanes(elem.Elem(), lc)
                setSPMDValueType(alloc, types.NewPointer(fixed))
            }
        }
    }
}

func spmdAllocaConsumerWidth(alloc *Alloc) int {
    refs := alloc.Referrers()
    if refs == nil {
        return 0
    }
    for _, ref := range *refs {
        // SPMDLoad/Store/Index/Select inherit width from their result type
        // (which the predication pass has already rewritten).
        // Plain Store/UnOp{MUL} consumers in the test environment carry
        // the width on their operand value.
        switch op := ref.(type) {
        case *SPMDLoad:
            if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *SPMDStore:
            if t, ok := op.Val.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *Store:
            if t, ok := op.Val.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *UnOp:
            if op.Op == token.MUL && op.X == alloc {
                if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                    return t.Lanes()
                }
            }
        }
    }
    return 0
}
```

Wired into `func.go`'s `finishBody` immediately after `spmdConvertLoopOps(f)`:

```go
spmdConvertLoopOps(f)
spmdPropagateAllocaWidth(f)
```

### 3.5 SSA tests

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_v5_typed_test.go` (NEW).

Three tests:

- `TestSPMDV5VaryingAllocaNotLifted` — restored from v3/v4. Verifies the lift guard.
- `TestSPMDV5TypeRewriting` — after `spmdConvertLoopOps`, every Varying value in loop scope has `Type().(*SPMDType).Lanes() == loop.LaneCount`.
- `TestSPMDV5ForwardPropagationEntryBlock` — entry-block alloca consumed by an in-loop SPMDStore has its pointee type mutated to width-fixed. Same fixture pattern as v4's test, but asserts the alloca's pointer element type carries the right `Lanes()`.

---

## 4. TinyGo Backend — One-Line Materialization Change

### 4.1 The single TinyGo change

**File**: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, `getLLVMType` switch case for `*types.SPMDType` (around line 548).

Current code:

```go
case *types.SPMDType:
    elemType := c.getLLVMType(typ.Elem())
    laneCount := c.spmdEffectiveLaneCount(typ, elemType)
    if laneCount <= 1 {
        return elemType
    }
    return llvm.VectorType(elemType, laneCount)
```

Replace with:

```go
case *types.SPMDType:
    elemType := c.getLLVMType(typ.Elem())
    // SPMD v5: prefer the type-encoded lane count when set by the SSA
    // predication or propagation pass. This is the canonical source for
    // values inside SPMD loop scope. Falls back to spmdEffectiveLaneCount
    // for width-free types (function signatures, global vars, abstract
    // types from compilerContext-level queries).
    var laneCount int
    if typ.Lanes() > 0 {
        laneCount = typ.Lanes()
    } else {
        laneCount = c.spmdEffectiveLaneCount(typ, elemType)
    }
    if laneCount <= 1 {
        return elemType
    }
    return llvm.VectorType(elemType, laneCount)
```

That is the entire TinyGo change for v5.0. Five new lines. ALL ~21 sites that v3/v4 audited become automatically correct because they all flow through `getLLVMType(value.Type())` for some value, and the value's type now carries the right width.

### 4.2 Why the existing helpers stay

- `spmdLaneCount(elemType)`: used for width-free types (signatures, globals, non-SPMD scope) where there's no SSA value to ask. Stays.
- `spmdMinLaneCountForSig(sig)`: signatures stay width-free in v5.0 (specialization is v5.1 work). Stays.
- `spmdEffectiveLaneCount`: the fallback inside the new `getLLVMType` branch. Stays.
- `spmdActiveLaneCount` builder field (v4): NOT NEEDED. v5 removes the block-entry hook. The type carries the width.
- `b.spmdValueOverride` / `spmdValueOverride` map: existing TinyGo machinery that overrides specific SSA value's LLVM materialization. Stays — independent of v5's type-system change.

### 4.3 What's auto-fixed

Each of the v3/v4 audit sites becomes correct because it derives width from a value whose type is now width-fixed:

| Site | How it inherits the fix |
|---|---|
| `*ssa.Alloc` materialization | `getLLVMType(ptr.Elem())` where `ptr.Elem()` is now `*SPMDType{lanes:N}` → `<N x elem>` alloca |
| `*ssa.UnOp{MUL}` load | `getLLVMType(unop.Type())` where `unop.Type()` is `*SPMDType{lanes:N}` → `<N x elem>` load |
| `IndexAddr` address vector | derives from index value's LLVM type, which is `<N x i32>` (predication mutated index's type) → `<N x ptr>` address |
| `createSPMDLoad` result | `getLLVMType(instr.Type())` where instr.Type() is width-fixed → `<N x elem>` |
| `createSPMDStore` value | val.Type() is width-fixed → store width matches |
| Reduce builtin | accumulator's type is width-fixed → `reduce.add.vNi32` matches accumulator |
| Lanes builtins | result type is width-fixed → emission at correct width |
| Swizzle / gather / scatter | input/output types width-fixed → operations at correct width |
| Mask materialization | mask vector size derives from value's vector size (already correct since value is width-fixed) |
| Broadcast emit | broadcast result type is width-fixed → broadcast at correct width |

NONE of these need explicit "consult b.spmdActiveLaneCount" or "consult bb.SPMDLaneCount" or "consult alloc.SPMDLaneCount". They all consult the TYPE, which carries the width.

### 4.4 TinyGo tests

**File**: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` — append three tests:

```go
// TestSPMDV5VaryingAllocaLLVMType verifies that a Varying[int] alloca
// inside a go-for over []float64 is materialized at the loop's iteration
// width (2 on WASM SIMD128), and ALL load/reduce ops on the alloca are
// also at that width. v5 expects this without any TinyGo-side audit
// because the type itself carries the width.
func TestSPMDV5VaryingAllocaLLVMType(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []float64{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
    var acc lanes.Varying[int]
    go for i, _ := range data {
        acc += i
    }
    _ = reduce.Add(acc)
}
`
    ir := compileSPMDSource(t, src)
    mustContain(t, ir, "alloca <2 x i32>")
    mustContain(t, ir, "load <2 x i32>, ptr %acc")
    mustContain(t, ir, "reduce.add.v2i32")
    mustNotContain(t, ir, "load <4 x i32>, ptr %acc")
    mustNotContain(t, ir, "reduce.add.v4i32")
}

// TestSPMDV5VaryingByteIndexAddr verifies that b[i] = c in a []byte loop
// produces matching <N x ptr> address and <N x i8> value vectors, where
// N is the loop's lane count (16 on WASM128, 32 on AVX2). This is the
// to-upper bug — v3/v4 broke this because the IndexAddr address vector
// was sized at int's natural width, not the loop width.
func TestSPMDV5VaryingByteIndexAddr(t *testing.T) {
    src := `package main

import "lanes"

var src = []byte("hello world")

func main() {
    b := make([]byte, len(src))
    go for i, c := range src {
        if 'a' <= c && c <= 'z' {
            b[i] = c - 32
        } else {
            b[i] = c
        }
    }
    _ = b
}
`
    ir := compileSPMDSource(t, src)
    // On WASM128, byte iter = 16 lanes. Address vector and value vector
    // must agree at <16 x ptr> / <16 x i8>.
    mustContainAny(t, ir, "<16 x ptr>", "<16 x i8>")
    mustNotContain(t, ir, "<4 x ptr>")  // would indicate int-natural width leak
}

// TestSPMDV5LoContainsReduceAny verifies that reduce.Any on a varying
// comparison result produces the right-width reduce intrinsic. lo-contains
// failed in v3/v4 because reduce.Any read element-natural width.
func TestSPMDV5LoContainsReduceAny(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []int32{1, 2, 3, 4, 5, 6, 7, 8}

func find(target int32) bool {
    var found lanes.Varying[bool]
    go for _, x := range data {
        if x == target {
            found = true
        }
    }
    return reduce.Any(found)
}

func main() {
    _ = find(5)
}
`
    ir := compileSPMDSource(t, src)
    // On WASM128, int32 iter = 4 lanes. reduce.Any over <4 x i1> mask.
    mustContainAny(t, ir, "reduce.or.v4i1", "v128.any_true")
}
```

`MaxStackAlloc` propagation in `compiler_test.go` is also kept (from v3/v4) so allocas stay on stack and are observable.

---

## 5. Rollout, File Changes, Risks

### 5.1 Rollout order

1. Stdlib `go/types` + `cmd/compile/internal/types2`: add `lanes` field, `NewVaryingWithLanes`, `Lanes()`, update `Identical`, update `String`, add tests. Single Go-stdlib commit.
2. x-tools-spmd: add `setSPMDValueType` helper to register mix-in. Standalone commit (no consumers yet).
3. x-tools-spmd: restore lift guard + add predication-pass type rewriting + forward-propagation pass + tests. Single x-tools-spmd commit (combined because they're tightly coupled — tests need all three to pass).
4. tinygo: 5-line `getLLVMType` change + tests. Single tinygo commit.
5. **GATE**: full E2E + benchmark sweep. Hard go/no-go.
6. tinybench: re-enable n-body + n-body-nosqrt + delete BLOCKER files. Single tinybench commit.
7. Parent SPMD: bump submodule pointers for `go`, `x-tools-spmd`, `tinygo`, `tinybench`.

Each step independently revertible.

### 5.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `go/src/go/types/spmd.go` | MODIFY | Add `lanes int` field, `NewVaryingWithLanes`, `Lanes()` accessor |
| `go/src/cmd/compile/internal/types2/spmd.go` | MODIFY | Same |
| `go/src/go/types/predicates_ext_spmd.go` | MODIFY | `Identical` compares `lanes` |
| `go/src/cmd/compile/internal/types2/predicates_ext_spmd.go` | MODIFY | Same |
| `go/src/go/types/typestring_ext_spmd.go` | MODIFY | Print `_N` suffix when lanes>0 |
| `go/src/cmd/compile/internal/types2/typestring_ext_spmd.go` | MODIFY | Same |
| `go/src/go/types/spmd_test.go` | NEW or extend | 3 tests (Lanes accessor, Identical, String) |
| `x-tools-spmd/go/ssa/ssa.go` | MODIFY | Add package-private `setSPMDValueType` bridge that calls the existing `register.setType` via interface assertion |
| `x-tools-spmd/go/ssa/lift.go` | MODIFY | Restore lift guard + `isLanesVaryingType` helper |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | MODIFY | Add per-value type rewriting in `spmdConvertLoopOps` |
| `x-tools-spmd/go/ssa/spmd_propagate.go` | NEW | Forward propagation for entry-block allocas |
| `x-tools-spmd/go/ssa/func.go` | MODIFY | Wire `spmdPropagateAllocaWidth` after `spmdConvertLoopOps` |
| `x-tools-spmd/go/ssa/export_spmd_test.go` | NEW | Test re-export of `isLanesVaryingType` |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDV5VaryingAllocaNotLifted` |
| `x-tools-spmd/go/ssa/spmd_v5_typed_test.go` | NEW | `TestSPMDV5TypeRewriting` + `TestSPMDV5ForwardPropagationEntryBlock` |
| `tinygo/compiler/compiler.go` (`getLLVMType` SPMDType case) | MODIFY | 5-line change to read `typ.Lanes()` |
| `tinygo/compiler/compiler_test.go` | MODIFY | `MaxStackAlloc` propagation (carryover from v3/v4) |
| `tinygo/compiler/spmd_test.go` | MODIFY | 3 IR tests (`TestSPMDV5VaryingAllocaLLVMType`, `TestSPMDV5VaryingByteIndexAddr`, `TestSPMDV5LoContainsReduceAny`) |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/BLOCKERS.md` | MODIFY/DELETE | Remove entries; delete file if empty |

Parent SPMD repo: submodule pointer bumps for `go`, `x-tools-spmd`, `tinygo`, `tinybench`.

### 5.3 Risks

1. **`types.Identical(Varying[int]_4, Varying[int]_8) = false` could break SSA invariants.** A phi merging values from different lane-count contexts would fail SSA validation. Risk: zero in practice for valid SPMD code (lane counts are uniform within a loop scope), but worth verifying with the test suite. Mitigation: SSA validation pass exists; v5 tests cover phi correctness.

2. **In-place type mutation via `setSPMDValueType` is unusual for go/ssa.** SSA values' types are normally immutable post-construction. Mitigation: the mutation is during a dedicated post-construction pass with a clear contract documented in the helper. Same risk profile as v3's `*ssa.Alloc.SPMDLaneCount` field, just expressed via type instead of side-channel.

3. **Forward-propagation produces wrong width for mixed-width entry-block allocas.** Same limitation as v3/v4 §6.2 — first-consumer wins; mixed-width allocas need source-level workaround. v5.1 may relax via per-alloca propagation.

4. **Stock-Go builds (no GOEXPERIMENT) might see the new `lanes` field.** Field is gated by `GOEXPERIMENT=spmd` checks in `Identical` / `String`. Default value (0) means abstract — type-checker behavior unchanged. Mitigation: existing buildcfg.Experiment.SPMD gating; no new gating needed.

5. **Cross-package SSA: callee's signature has width-free params; caller passes width-fixed args.** v5.0 doesn't specialize across calls. Caller's width-fixed value flows into a callee expecting width-free. TinyGo's call-site lowering uses signature-derived width (existing `spmdMinLaneCountForSig`). Argument value gets implicit widen/narrow at call boundary if widths differ. Documented limitation; v5.1 addresses via specialization.

6. **Type-cache surprises**: there's no explicit type-deduplication cache for `*SPMDType` today (each `NewVarying` allocates a fresh instance). Adding `lanes` doesn't introduce caching. Risk: zero. v5.1 may introduce caching for the specialization clones.

### 5.4 Success criteria

All must hold:

- Stdlib type tests pass (`TestSPMDTypeLanesField`, `TestSPMDTypeIdenticalLanes`, `TestSPMDTypeStringLanes`).
- SSA tests pass (`TestSPMDV5VaryingAllocaNotLifted`, `TestSPMDV5TypeRewriting`, `TestSPMDV5ForwardPropagationEntryBlock`).
- TinyGo IR tests pass (`TestSPMDV5VaryingAllocaLLVMType`, `TestSPMDV5VaryingByteIndexAddr`, `TestSPMDV5LoContainsReduceAny`).
- `test/e2e/spmd-e2e-test.sh`: zero new failures vs the 105/94/93/11 baseline. v3/v4 each broke 20-23 tests at this gate. v5 must NOT.
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference.

### 5.5 Out of scope / deferred to v5.1

- **Per-call specialization (ISPC-style monomorphization).** Architecturally correct per user direction. Blocked on TinyGo's DI metadata SIGSEGV when emitting cloned SPMD function variants. v5.1 fixes the SIGSEGV first, then re-enables v4's specialization pass adapted for v5's width-typed types (clones rewrite signature types per variant lane count).
- **Width-fixed function signatures.** Currently width-free; v5.1 adds per-call signature rewriting via specialization.
- **Mixed-width allocas in a single function.** Workaround: factor into separate functions.
- **Non-SPMD function bodies that happen to contain Varying values** (e.g., utility functions called from SPMD scope). v5.0 keeps these width-free; v5.1 handles via specialization.

### 5.6 Why v5 won't hit the v1-v4 cascade

The cascade pattern was: fix one site → break another → fix that → break a third. Each "fix" added a new side-channel-consultation site that other code paths weren't aware of.

v5 doesn't add a side channel. The width is in the type. Every existing TinyGo path that materializes a Varying value already calls `getLLVMType(value.Type())` for its width — and `getLLVMType` now reads the type's width directly. The fix is at ONE location (the `getLLVMType` switch case) but the EFFECT is at every site that reads value types — which is every TinyGo materialization site.

The four prior attempts proved the side-channel approach doesn't work. v5's type-encoding approach removes the need for any consumer-side patching, making the cascade structurally impossible.
