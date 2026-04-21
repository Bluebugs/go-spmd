# `Varying[*Struct]` Field Access — Design

**Date**: 2026-04-21
**Scope**: Implement field access (read + write) through `lanes.Varying[*Struct]` in the SPMD fork of the Go toolchain, x-tools-spmd, and TinyGo. Unblocks the `n-body` and `n-body-nosqrt` tinybench ports.

---

## 1. Overview, Goals, Scope

### Goal

When the SPMD type checker sees `expr.field` where `expr` has type `Varying[*S]`, accept the access, return `Varying[fieldT]` for the loaded value, and have the TinyGo backend emit correct per-lane GEPs + gather-load / scatter-store with the active mask threaded in.

### Concrete unlock

The two blocked tinybench benchmarks (`n-body`, `n-body-nosqrt`) re-enable with `rm <bench>/go-spmd/BLOCKER.md` — no source changes to the ports. They become additional regression tests for this feature.

### Non-goals

- `*(Varying[*T])` bare dereference (gather/scatter without field access). Documented in `pointer_varying.go` as future work; no port needs it today.
- Chained / nested varying-pointer field access (`(Varying[*Outer]).inner.x`).
- `Varying[*[N]T]` (varying pointer to array) indexing.
- Method calls on `Varying[*T]`. Only field access.
- Runtime overlap detection. A future `-race` integration may add this; out of scope here.

### Semantics

- **Read**: `a := (Varying[*S]).field` → per-lane gather from N distinct struct slots, result type `Varying[fieldT]`.
- **Write**: `(Varying[*S]).field = x` (or `+=`, which is read-modify-write) → per-lane scatter to N distinct struct slots.
- **Aliasing is user-level UB**. Two lanes holding the same pointer writing to the same field has undefined behavior — aligned with ISPC's `varying T*` semantics and LLVM's `@llvm.masked.scatter` unordered-store contract. Document clearly; defer detection to a future `-race` integration.

### Invariants preserved

- No mask-stack reintroduction. All memory ops thread an explicit mask SSA operand (migrated 2026-03-05).
- No changes to predicated-SSA passes, loop peeling, or boolean-chain lowering.
- Contiguous-access propagation (`spmdContiguousPtr`) continues to work through `FieldAddr` — `spmdFieldAddrForVaryingPtr` keeps doing its job.

### Approach summary

Surgical. One parallel code path added next to each existing `*Varying[T]` path, at each of the four layers (types, types2, x-tools-spmd, TinyGo). No refactoring of working code.

---

## 2. Type System Changes

Mirrored between `go/src/go/types/` (TinyGo's type checker) and `go/src/cmd/compile/internal/types2/` (stock-compile's type checker). The existing `*Varying[T]` support already lives in both files; this change follows the same dual-layer pattern.

### 2.1 Field-lookup unwrap

**Problem**: `lookupFieldOrMethodImpl()` calls `deref(T)` which only unwraps `*Pointer`. For `Varying[*Planet]`, `deref` returns the type unchanged, then the struct-field scan finds no struct and the lookup fails with `type Varying[*Planet] has no field or method x`.

**Fix**: a new SPMD-aware helper, called from the selector path, NOT a change to generic `deref()` (which is used throughout the type checker and must not grow SPMD-awareness everywhere):

```go
// spmdUnwrapVaryingPointer returns the pointed-to struct type when t is
// Varying[*S]. Returns (nil, false) otherwise. Used by the selector to
// extend field-or-method lookup to varying-pointer receivers.
func spmdUnwrapVaryingPointer(t Type) (Type, bool) {
    if !buildcfg.Experiment.SPMD {
        return nil, false
    }
    sv, ok := t.(*SPMDType)
    if !ok {
        return nil, false
    }
    ptr, ok := sv.Elem().(*Pointer)
    if !ok {
        return nil, false
    }
    return ptr.Elem(), true
}
```

**Call site**: the selector handler in `call.go` (~line 880). Before the main `lookupFieldOrMethod` call, if `spmdUnwrapVaryingPointer(receiver)` returns `(innerT, true)`, run the lookup against `innerT` instead. Method lookup must be disabled for this receiver shape (per §1 non-goal).

### 2.2 Field-type wrap

**Existing**: `spmdWrapFieldType(receiverType, fieldType)` detects `*Varying[S]` receiver and wraps `fieldType` in `NewVarying(fieldType)`. It ignores `Varying[*S]` receivers today.

**Fix**: extend to also detect `Varying[*S]` and wrap identically.

```go
func spmdWrapFieldType(receiverType, fieldType Type) Type {
    if !buildcfg.Experiment.SPMD {
        return fieldType
    }
    // Existing: *Varying[S] → Varying[fieldT]
    if ptr, ok := receiverType.(*Pointer); ok {
        if _, ok := ptr.Elem().(*SPMDType); ok {
            if _, alreadyVarying := fieldType.(*SPMDType); !alreadyVarying {
                return NewVarying(fieldType)
            }
            return fieldType
        }
    }
    // New: Varying[*S] → Varying[fieldT] (same wrap, different receiver shape)
    if _, ok := spmdUnwrapVaryingPointer(receiverType); ok {
        if _, alreadyVarying := fieldType.(*SPMDType); !alreadyVarying {
            return NewVarying(fieldType)
        }
        return fieldType
    }
    return fieldType
}
```

Result for both shapes: `fieldT` becomes `Varying[fieldT]`. Subsequent assignment/load/arithmetic goes through the already-working varying-value machinery.

### 2.3 Scatter-store acceptance

The selector in §2.1 produces an lvalue. For `b2.vx = x` or `b2.vx += x`, the type checker must accept storing a `Varying[fieldT]` (or a uniform that broadcasts) to the lvalue.

No new code needed. The lvalue's type is `Varying[fieldT]` (via §2.2); the existing varying-value assignment rules already accept the store. LLVM-level scatter is a backend concern (§4). Type-checker acceptance falls out naturally once §2.1 and §2.2 land.

### 2.4 Mirror to `types2`

Identical changes to `go/src/cmd/compile/internal/types2/lookup.go` (+ new `lookup_ext_spmd.go`) and `call_ext_spmd.go`. Line-for-line copy with types2 receiver names. Same test data mirror in `testdata/spmd/pointer_varying.go`.

### 2.5 Type-checker tests

Extend `go/src/go/types/testdata/spmd/pointer_varying.go`:

Replace the `_ = pointPtr` stub with concrete field-access expectations:

```go
// Varying[*Struct] — field access must be accepted.
go for i := range 4 {
    pointPtr := &points[i]    // Varying[*IntPoint]
    x := pointPtr.X           // Varying[int] (gather lvalue)
    pointPtr.Y = x + 1        // scatter-store (rvalue)
    pointPtr.X += 10          // read-modify-write
    _ = x
}
```

Add ERROR-tagged rejections:

```go
go for i := range 4 {
    pointPtr := &points[i]
    _ = pointPtr.Method()     // ERROR "method calls on Varying[*T] not supported"
    q := &pointPtr.X          // ERROR "cannot take address of Varying[*T].field"
}
```

Mirror to `go/src/cmd/compile/internal/types2/testdata/spmd/pointer_varying.go`.

---

## 3. SSA Layer (`x-tools-spmd/go/ssa/`)

Minimal structural work. `FieldAddr` is already a generic instruction (`X Value, Field int`, computed Type). No new opcode, no new pass, no changes to predication or loop peeling.

### 3.1 Type inference for `FieldAddr` result

**Existing behavior**:
- `X.Type() = *Varying[S]` → `FieldAddr.Type() = *Varying[fieldT]`

**New behavior** (per §1 semantics — the address produced by `&(Varying[*S]).field` is a vector of per-lane pointers, not a pointer to a varying field):
- `X.Type() = Varying[*S]` → `FieldAddr.Type() = Varying[*fieldT]`

The SSA builder computes the address type when lowering `&expr` (address-of on a selector) into a `FieldAddr` instruction. A small helper in `x-tools-spmd/go/ssa/spmd_varying.go`:

```go
// spmdFieldAddrResultType returns the SSA-level type for a FieldAddr
// whose base has type baseT and whose field has type fieldT.
//   - *Varying[S] base: returns *Varying[fieldT] (existing behavior).
//   - Varying[*S] base: returns Varying[*fieldT] (new).
//   - Otherwise: returns *fieldT (normal Go).
func spmdFieldAddrResultType(baseT, fieldT types.Type) types.Type {
    // ... implementation dispatching on baseT shape
}
```

The SSA builder's FieldAddr construction calls this helper instead of hardcoding `types.NewPointer(fieldT)`.

### 3.2 No new instructions

All downstream SSA ops already handle `Varying[*T]` at the type level:

- `UnOp(token.MUL)` on `Varying[*fieldT]` → `Varying[fieldT]`. Standard SPMD varying-load.
- `Store` with `Varying[*fieldT]` address and `Varying[fieldT]` value → scatter-store. Backend lowers.
- Masks thread through `SPMDLoad`/`SPMDStore` at SSA level (migrated 2026-03-05). Reuses existing plumbing.

### 3.3 SSA test

Add to `x-tools-spmd/go/ssa/spmd_pointer_test.go`:

```go
// TestSPMDVaryingPointerFieldAccess — gather path.
// Fixture:
//   func accessFields(pointPtrs lanes.Varying[*Point]) lanes.Varying[int] {
//       return pointPtrs.X
//   }
// Assertions:
//   - Find *ssa.FieldAddr; Type() == Varying[*int]
//   - Find following *ssa.UnOp(MUL); Type() == Varying[int]
func TestSPMDVaryingPointerFieldAccess(t *testing.T) { ... }

// TestSPMDVaryingPointerFieldStore — scatter path.
// Fixture:
//   func writeFields(pointPtrs lanes.Varying[*Point], v lanes.Varying[int]) {
//       pointPtrs.Y = v
//   }
// Assertions:
//   - Find *ssa.Store; Addr operand Type() == Varying[*int]; Val operand Type() == Varying[int]
func TestSPMDVaryingPointerFieldStore(t *testing.T) { ... }
```

### 3.4 No changes to predication

SPMD predication passes operate on control flow and mask propagation; they don't care about element types. Field access on `Varying[*T]` produces ordinary `FieldAddr` + `UnOp(MUL)` / `Store`, later wrapped into `SPMDLoad` / `SPMDStore` by the same mechanism as any other varying memory op. Zero changes.

---

## 4. TinyGo Backend (`tinygo/compiler/`)

Two changes: a new Case D in the `FieldAddr` handler, and a verification that the downstream Store path correctly lowers `Store` on a `<N x ptr>` vector to a masked scatter.

### 4.1 Case D — FieldAddr on `Varying[*S]`

**Location**: `tinygo/compiler/compiler.go`, the `*ssa.FieldAddr` case (currently lines ~3098–3195, with Cases A/B/C).

**New branch**, inserted BEFORE the `*types.Pointer` check because `Varying[*S]` is an `*SPMDType`, not a `*Pointer`:

```go
// Case D: expr.X has SSA type Varying[*S] — a vector of per-lane pointers.
// val is a <N x ptr> LLVM vector. Treat identically to Case B (per-lane GEPs
// into the pointed-to struct) but source the struct layout from the inner
// pointer's element.
if spmdType, ok := expr.X.Type().Underlying().(*types.SPMDType); ok {
    if ptr, ok := spmdType.Elem().(*types.Pointer); ok {
        structLLVMType := b.getLLVMType(ptr.Elem())
        laneCount := val.Type().VectorSize()
        result := b.spmdFieldAddrPerLane(val, structLLVMType, expr.Field, laneCount)
        b.spmdFieldAddrForVaryingPtr(expr, result)
        return result, nil
    }
}
// ... existing *Pointer checks follow (Cases A/B/C unchanged)
```

Two observations:

1. **No new helper needed**. `spmdFieldAddrPerLane` (compiler.go ~4933) does exactly the right work: extract each pointer from the `<N x ptr>` vector, GEP to the field, reassemble. Written for Case B, fits Case D unchanged.
2. **`spmdFieldAddrForVaryingPtr` call preserved**. If `expr.X` traces to a contiguous access, the contiguous-access cache propagates through this `FieldAddr`, enabling masked vector load/store downstream.

### 4.2 Result LLVM type

The LLVM result of Case D is `<N x ptr>` — same as Case B. Matches the SSA type `Varying[*fieldT]` from §3.1.

**Verification check**: `getLLVMType(Varying[*fieldT])` must return `<N x ptr>`. `Varying[T]` generally lowers to `<N x T>`; `*fieldT` lowers to `ptr`; so `Varying[*fieldT]` should lower to `<N x ptr>` via the existing SPMDType branch in `getLLVMType`. Expected to already work because `*T` is a well-supported element type for SPMDType in the existing `*Varying[T]` read-of-varying-pointer path. If it doesn't, add the case.

### 4.3 Scatter-store path

When the user writes `b2.vx = value` or `b2.vx += value`, SSA produces `*ssa.Store` with:

- Address operand: the FieldAddr result (Case D → `<N x ptr>`).
- Value operand: a `<N x fieldT>` varying value.

**Required behavior**: when the address is a `<N x ptr>` vector AND the execution mask is not all-ones, emit `@llvm.masked.scatter`. When all-ones, unmasked scatter. When flagged contiguous via `spmdContiguousPtr`, fall through to the masked-vector-store fast path.

**Gap to verify**: does the current Store handler already dispatch to scatter when it sees a `<N x ptr>` address with a varying value? Case B (FieldAddr on plain `*S` with varying IndexAddr) produces the same `<N x ptr>` FieldAddr output. If Case B has no scatter-store support, it's latent-broken for writes — discovery scopes widen with one additional task.

**Verification probe**: see §5.5.

### 4.4 Gather-load path

Symmetric: `*ssa.UnOp(token.MUL)` with a `<N x ptr>` address operand → `@llvm.masked.gather`. Same argument as §4.3 — should already work via Case B. Verify during implementation.

### 4.5 Mask threading

No changes. Mask arrives on the SSA-level `SPMDLoad` / `SPMDStore` from the predication passes. The backend reads the mask operand when emitting the scatter/gather intrinsic. Existing plumbing.

### 4.6 TinyGo unit tests

Add to `tinygo/compiler/spmd_test.go`:

- `TestSPMDVaryingPointerFieldAddr_Gather` — compile a fixture calling `accessFields(ptrs Varying[*Point]) Varying[int]`, verify emitted IR contains the `extractelement` / `getelementptr inbounds %Point` / `insertelement` triplet of `spmdFieldAddrPerLane`, followed by `@llvm.masked.gather` (or the contiguous-masked-load fast path).
- `TestSPMDVaryingPointerFieldAddr_Scatter` — symmetric write test; verify `@llvm.masked.scatter` (or contiguous-masked-store).
- `TestSPMDVaryingPointerFieldAddr_Contiguous` — fixture with contiguous index derivation (`&arr[baseIdx + laneIdx]`, uniform `baseIdx`); verify `spmdFieldAddrForVaryingPtr` propagates the contiguous flag and the backend emits the masked vector load/store instead of scatter/gather.

---

## 5. Testing

Four layers, bottom-up, so a failure at one level doesn't mask a failure at another.

### 5.1 Type-checker tests (unit)

**Files**: `go/src/go/types/testdata/spmd/pointer_varying.go` + types2 mirror.

Replace the current `_ = pointPtr` stub with field-access fixtures (§2.5). Add ERROR-tagged cases for methods and address-of-field.

### 5.2 SSA tests (unit)

**File**: `x-tools-spmd/go/ssa/spmd_pointer_test.go`.

Add `TestSPMDVaryingPointerFieldAccess` (gather) and `TestSPMDVaryingPointerFieldStore` (scatter), both parallel to existing `TestSPMDPointerVaryingFieldAccess`. Assertions focus on SSA-level Types (§3.3).

### 5.3 TinyGo backend tests (unit)

**File**: `tinygo/compiler/spmd_test.go`. Gather / scatter / contiguous IR tests per §4.6.

### 5.4 End-to-end regression (integration)

No new test code. Remove the tinybench blocker sentinels:

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body/go-spmd/BLOCKER.md n-body-nosqrt/go-spmd/BLOCKER.md
go test -v -run 'TestCorrectness/(n-body|n-body-nosqrt)' -timeout=600s
```

Expected: both PASS, byte-identical output to stock go.

If byte-identical fails because of float reassociation in `reduce.Add` vs scalar sequential accumulation, that's the pre-existing risk called out in the tinybench plan §5.2 — a separate blocker cause, not this feature's fault. Document, don't scope-creep.

### 5.5 Verification probe for §4.3 scatter-store

Before declaring the design's backend work complete, run the explicit "does Case B scatter work today?" check:

```bash
cat <<'EOF' > /tmp/scatter-probe.go
package main
import "lanes"
type Pt struct{ X, Y int }
var arr [16]Pt
func main() {
    go for i := range 16 {
        arr[i].X = int(i) * 3        // Case B: *S IndexAddr + FieldAddr + Store
    }
}
EOF
PATH=.../go/bin:$PATH GOEXPERIMENT=spmd .../tinygo/build/tinygo build \
  -opt=2 -llvm-features=+avx2 -o /tmp/scatter-probe /tmp/scatter-probe.go
# Inspect generated LLVM IR (e.g. with -print-after-all or LLVM IR dump flag)
# for @llvm.masked.scatter or per-lane conditional stores.
```

- If present → Case D inherits for free.
- If absent → Case B Store is latent-broken; widen the plan to fix the Store dispatch for `<N x ptr>` addresses as an additional task.

---

## 6. Rollout, File-by-File Changes, Risks

### 6.1 Rollout order (smallest reversible steps)

1. Type-checker tests first — write the `pointer_varying.go` extensions; they fail today. Red baseline confirmed.
2. Type-checker fix (`go/types` → `types2`). `spmdUnwrapVaryingPointer` helper, selector-path call, `spmdWrapFieldType` extension. Re-run → green.
3. SSA test first (fail). `TestSPMDVaryingPointerFieldAccess` + `TestSPMDVaryingPointerFieldStore`.
4. SSA fix. `spmdFieldAddrResultType` helper + builder call. Re-run → green.
5. §5.5 verification probe. If green, proceed; if red, widen the implementation plan with a Case-B-Store fix task before continuing.
6. TinyGo backend test first (fail). Gather + scatter + contiguous IR tests.
7. TinyGo backend fix. Case D branch in `*ssa.FieldAddr`. Re-run → green.
8. E2E regression. Remove tinybench BLOCKER.md files. Run `TestCorrectness`. If green, feature done.
9. Commit the tinybench blocker removals as their own commit in the tinybench submodule; bump the parent SPMD repo's submodule pointer.

Each step is independently revertable. Steps 1-4 and 6-7 add new branches only — no destructive edits to existing code.

### 6.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `go/src/go/types/call_ext_spmd.go` | Modify | Extend `spmdWrapFieldType` with the `Varying[*S]` branch |
| `go/src/go/types/lookup_ext_spmd.go` | Create | `spmdUnwrapVaryingPointer(T) (Type, bool)` helper |
| `go/src/go/types/call.go` | Modify | Selector-path call to `spmdUnwrapVaryingPointer` before `lookupFieldOrMethod` |
| `go/src/go/types/testdata/spmd/pointer_varying.go` | Modify | Replace `_ = pointPtr` stub with real field-access tests + ERROR-tagged rejections |
| `go/src/cmd/compile/internal/types2/call_ext_spmd.go` | Modify | Mirror of the types change |
| `go/src/cmd/compile/internal/types2/lookup_ext_spmd.go` | Create | Mirror of the helper |
| `go/src/cmd/compile/internal/types2/call.go` | Modify | Mirror of the selector change |
| `go/src/cmd/compile/internal/types2/testdata/spmd/pointer_varying.go` | Create/Modify | Mirror test fixture |
| `x-tools-spmd/go/ssa/spmd_varying.go` | Modify | Add `spmdFieldAddrResultType` helper |
| `x-tools-spmd/go/ssa/builder.go` | Modify | Use helper at FieldAddr construction |
| `x-tools-spmd/go/ssa/spmd_pointer_test.go` | Modify | Add gather + scatter SSA tests |
| `tinygo/compiler/compiler.go` | Modify | Add Case D branch in `*ssa.FieldAddr` handler |
| `tinygo/compiler/spmd_test.go` | Modify | Add gather, scatter, contiguous IR tests |
| `tinybench/n-body/go-spmd/BLOCKER.md` | Delete | After E2E passes |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | Delete | After E2E passes |
| `tinybench/BLOCKERS.md` | Delete | No remaining blockers |

All changes on existing `spmd` branches in the forks. No main-repo changes except submodule pointer bumps.

### 6.3 Risk register

1. **§4.3 Case B Store is latent-broken**. Scatter-store path may not exist for Case B today (no port exercises it). The §5.5 probe catches this before it wastes implementation time. If triggered, one extra task is added; design doesn't change.

2. **Float reassociation in n-body**. `reduce.Add` tree reduction may produce last-digit differences vs scalar sequential accumulation. Not caused by this design; would surface only after BLOCKER.md removal. If hit, file as a new blocker with a different cause.

3. **`getLLVMType(Varying[*T]) == <N x ptr>` assumption**. If the existing SPMDType lowering doesn't handle pointer elements correctly, §4.2 needs a small addition. The TinyGo unit tests (§5.3) catch this.

4. **Mirrored edits diverge**. `go/types` and `types2` must stay in sync. Mitigation: submit mirrored changes in the same commit; mirrored tests catch divergence on next run.

### 6.4 Out of scope / deferred

- Runtime overlap detection (future `-race` integration).
- Nested / chained varying pointer field access.
- Varying-pointer method calls.
- `Varying[*[N]T]` array indexing.
- Bare `*ptr` deref of `Varying[*T]`. Stays deferred as noted in `pointer_varying.go`.

### 6.5 Success criteria

All four must hold:

- `go test ./... -run Spmd` in the forked Go stdlib passes (types + types2).
- `go test ./go/ssa -run SPMDVaryingPointer` in x-tools-spmd passes.
- `go test ./compiler -run SPMDVaryingPointerField` in TinyGo passes.
- `cd tinybench && go test -v -run 'TestCorrectness/(n-body|n-body-nosqrt)'` passes after the two BLOCKER.md deletes.
