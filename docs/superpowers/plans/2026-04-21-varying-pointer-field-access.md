# `Varying[*Struct]` Field Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Implement field access (read + write) through `lanes.Varying[*Struct]` in the SPMD fork — unblocks the two tinybench benchmarks (`n-body`, `n-body-nosqrt`) that currently have `BLOCKER.md` sentinels.

**Architecture:** Add a parallel code path next to each existing `*Varying[T]` path at four layers (go/types, types2, x-tools-spmd, tinygo) — surgical, non-refactoring. The TinyGo backend reuses the existing `spmdFieldAddrPerLane` helper via a new Case D branch in the `*ssa.FieldAddr` handler. Scatter-store and gather-load inherit from the existing Case B plumbing (pending verification in §5.5 of the spec).

**Tech Stack:**
- Forked Go toolchain (`go/` submodule at `/home/cedric/work/SPMD/go/`) — `go/types` and `cmd/compile/internal/types2`
- Forked `x/tools` (`x-tools-spmd/` submodule) — `go/ssa` package with SPMD extensions
- Forked TinyGo (`tinygo/` submodule) — `compiler/` package
- Tinybench submodule (`tinybench/`) — blocker sentinels to remove

**Spec:** `/home/cedric/work/SPMD/docs/superpowers/specs/2026-04-21-varying-pointer-field-access-design.md`

**Repositories touched:**

| Submodule | Branch | Role |
|---|---|---|
| `go/` | `spmd` | Forked Go toolchain (types + types2) |
| `x-tools-spmd/` | local main | SSA layer |
| `tinygo/` | `spmd` | LLVM backend |
| `tinybench/` | `spmd` | E2E regression (BLOCKER.md removal) |

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `go/src/go/types/lookup_ext_spmd.go` | NEW | `spmdUnwrapVaryingPointer(t Type) (Type, bool)` helper |
| `go/src/go/types/call_ext_spmd.go` | MODIFY | Extend `spmdWrapFieldType` with `Varying[*S]` branch |
| `go/src/go/types/call.go` | MODIFY | Selector-path call to `spmdUnwrapVaryingPointer` before `lookupFieldOrMethod` |
| `go/src/go/types/testdata/spmd/pointer_varying.go` | MODIFY | Replace stub with real field-access tests + ERROR-tagged rejections |
| `go/src/cmd/compile/internal/types2/lookup_ext_spmd.go` | NEW | Mirror of types helper |
| `go/src/cmd/compile/internal/types2/call_ext_spmd.go` | MODIFY | Mirror of types change |
| `go/src/cmd/compile/internal/types2/call.go` | MODIFY | Mirror of selector change |
| `go/src/cmd/compile/internal/types2/testdata/spmd/pointer_varying.go` | MODIFY/CREATE | Mirror of test fixture |
| `x-tools-spmd/go/ssa/spmd_varying.go` | MODIFY | `spmdFieldAddrResultType` helper |
| `x-tools-spmd/go/ssa/builder.go` | MODIFY | Use helper at FieldAddr construction site (line ~1418) |
| `x-tools-spmd/go/ssa/spmd_pointer_test.go` | MODIFY | Add gather + scatter SSA tests |
| `tinygo/compiler/compiler.go` | MODIFY | Add Case D branch in `*ssa.FieldAddr` handler (~line 3098) |
| `tinygo/compiler/spmd_test.go` | MODIFY | Add gather + scatter LLVM IR tests |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE | After E2E passes |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE | After E2E passes |
| `tinybench/BLOCKERS.md` | DELETE | No remaining blockers |

---

## Task 0: Pre-flight

**Files:** None (verification only).

- [ ] **Step 1: Verify all submodules are on expected branches**

```bash
cd /home/cedric/work/SPMD
git submodule status | grep -E "^(\+|\-|\s)[0-9a-f]+ (go|x-tools-spmd|tinygo|tinybench)" | head
cd go && git rev-parse --abbrev-ref HEAD && cd ..
cd x-tools-spmd && git rev-parse --abbrev-ref HEAD && cd ..
cd tinygo && git rev-parse --abbrev-ref HEAD && cd ..
cd tinybench && git rev-parse --abbrev-ref HEAD && cd ..
```

Expected:
- `go/` on branch `spmd`
- `x-tools-spmd/` on its working branch
- `tinygo/` on branch `spmd`
- `tinybench/` on branch `spmd`

- [ ] **Step 2: Baseline build**

```bash
cd /home/cedric/work/SPMD
make build
```

Expected: builds cleanly (forked Go toolchain + TinyGo binary). Failing here means a prior commit broke something; fix before proceeding.

- [ ] **Step 3: Baseline tests**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./go/types -run 'TestSpmd|TestCheck.*spmd' -timeout=60s -count=1
```

Expected: PASS. Captures the current-state baseline for the types tests that touch SPMD.

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMD' -timeout=60s -count=1
```

Expected: PASS (existing SPMD tests, including `TestSPMDPointerVaryingFieldAccess`).

- [ ] **Step 4: Confirm n-body is currently blocked**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-probe n-body/go-spmd/main.go 2>&1 | head -10
```

Expected: error lines containing `b2.x undefined (type lanes.Varying[*Planet] has no field or method x)`. Confirms the blocker baseline before the fix.

---

## Task 1: types checker — failing tests for `Varying[*T]` field access

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/go/types/testdata/spmd/pointer_varying.go`

The existing fixture has `_ = pointPtr` as a placeholder. Replace it with real field-access assertions that must compile. We write these FIRST (TDD), confirm they fail, then fix.

- [ ] **Step 1: Replace `structPointers()` and add ERROR-tagged cases**

Open `/home/cedric/work/SPMD/go/src/go/types/testdata/spmd/pointer_varying.go` and replace lines 13-24 (the current `structPointers()` function) with:

```go
func structPointers() {
	points := [4]IntPoint{}
	go for i := range 4 {
		pointPtr := &points[i] // &points[varyingIndex] gives Varying[*IntPoint]
		x := pointPtr.X        // gather: must produce Varying[int]
		pointPtr.Y = x + 1     // scatter-store: Varying[int] → field Y
		pointPtr.X += 10       // read-modify-write: gather + add + scatter
		_ = x
	}
	_ = points
}
```

(Leave `uniformPtrField()` at lines 27-33 and `varyingPtrDeref()` at lines 37-48 untouched — `varyingPtrDeref()` is the bare-deref pattern that remains deferred per the spec §1 non-goals, and the existing comment `// *Varying[*T] (deref of varying pointer vector) produces Varying[T] for scatter/gather` already documents it.)

- [ ] **Step 2: Add ERROR-tagged negative cases**

Append a new function to the same file, after `varyingPtrDeref()`:

```go
// Method calls on Varying[*T] must still be rejected per the design's
// non-goals. Address-of field is also rejected for now (YAGNI — loosen
// if a real use case appears).
type pointMethods struct{ X, Y int }

func (p *pointMethods) Scale(factor int) int { return p.X * factor }

func varyingPtrErrors() {
	pts := [4]pointMethods{}
	go for i := range 4 {
		ptr := &pts[i]
		_ = ptr.Scale(2) // ERROR "method calls on Varying\\[\\*pointMethods\\] not supported"
		q := &ptr.X      // ERROR "cannot take address of field through Varying\\[\\*T\\]"
		_ = q
	}
	_ = pts
}
```

The ERROR regex strings follow Go's type checker test conventions: the `// ERROR "regex"` comment must match the diagnostic the type checker emits (to be set in Task 2 Step 2).

- [ ] **Step 3: Run the types test — expect failures**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./go/types -run TestSpmd -v -count=1 -timeout=60s 2>&1 | tail -40
```

Expected: FAIL at `pointer_varying.go` with messages about undefined field X/Y/etc. on `Varying[*IntPoint]`. This is the TDD red state.

- [ ] **Step 4: Commit the failing test fixture**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/testdata/spmd/pointer_varying.go
git commit -m "types: add failing test for Varying[*Struct] field access

TDD red baseline. Extends the pointer_varying.go fixture with
concrete field-access expectations (gather read, scatter write,
read-modify-write) and ERROR-tagged rejections (methods,
address-of-field). Currently fails because the type checker does
not yet unwrap Varying[*T] for field lookup."
```

---

## Task 2: types checker — fix (go/types)

**Files:**
- Create: `/home/cedric/work/SPMD/go/src/go/types/lookup_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/go/types/call_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/go/types/call.go:870-884`

- [ ] **Step 1: Create the unwrap helper**

Create `/home/cedric/work/SPMD/go/src/go/types/lookup_ext_spmd.go` with:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types

import "internal/buildcfg"

// spmdUnwrapVaryingPointer returns the pointed-to type when t is Varying[*S],
// i.e., an SPMDType wrapping a Pointer. Returns (nil, false) otherwise.
//
// Used by the selector path to extend field-or-method lookup to
// varying-pointer receivers: for lookup purposes, Varying[*S].field behaves
// like (*S).field, while the returned value is later wrapped back into
// Varying[fieldT] by spmdWrapFieldType.
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

- [ ] **Step 2: Extend `spmdWrapFieldType`**

Open `/home/cedric/work/SPMD/go/src/go/types/call_ext_spmd.go` and replace the body of `spmdWrapFieldType` (currently lines 210-225) with:

```go
// spmdWrapFieldType returns the SPMD-adjusted type for a struct field access.
// When the receiver is *Varying[S] (a uniform pointer to a varying struct) OR
// Varying[*S] (a varying pointer vector to a uniform struct), field access
// should return Varying[fieldType] rather than bare fieldType.
// For all other receivers, the field type is returned unchanged.
func spmdWrapFieldType(receiverType, fieldType Type) Type {
	if !buildcfg.Experiment.SPMD {
		return fieldType
	}
	// Existing: *Varying[S] → Varying[fieldT]
	if ptr, ok := receiverType.(*Pointer); ok {
		if _, ok := ptr.Elem().(*SPMDType); ok {
			if _, alreadyVarying := fieldType.(*SPMDType); alreadyVarying {
				return fieldType
			}
			return NewVarying(fieldType)
		}
	}
	// New: Varying[*S] → Varying[fieldT] (symmetric wrap, different receiver shape)
	if _, ok := spmdUnwrapVaryingPointer(receiverType); ok {
		if _, alreadyVarying := fieldType.(*SPMDType); alreadyVarying {
			return fieldType
		}
		return NewVarying(fieldType)
	}
	return fieldType
}
```

- [ ] **Step 3: Thread the unwrap through the selector path**

Open `/home/cedric/work/SPMD/go/src/go/types/call.go` and locate the selector handler (around line 820 — look for `func (check *Checker) selector(...)` or similar). Find where `lookupFieldOrMethod(receiverType, ...)` is called; before that call, add:

```go
// SPMD: if receiver is Varying[*S], look up the field on *S and restore
// the varying wrap afterward via spmdWrapFieldType.
lookupType := receiverType
if innerT, ok := spmdUnwrapVaryingPointer(receiverType); ok {
	lookupType = NewPointer(innerT)
}
obj, index, indirect := lookupFieldOrMethod(lookupType, ...)
```

(The exact insertion point depends on the current selector implementation; the sub-agent will read the full selector function and splice the unwrap cleanly. The existing `spmdWrapFieldType` call at line 884 already runs on the ORIGINAL `receiverType`, not the unwrapped one — keep it that way so the varying wrap lands on the result.)

- [ ] **Step 4: Add the method/address-of rejection errors**

In the same selector handler, find the *Func (method) case (around line 886 per the earlier exploration). Add a guard that rejects method lookup when `spmdUnwrapVaryingPointer(originalReceiverType)` succeeded:

```go
case *Func:
	if _, ok := spmdUnwrapVaryingPointer(receiverType); ok {
		check.errorf(e, UndefinedOp, "method calls on %s not supported", receiverType)
		goto Error
	}
	// ... existing *Func handling continues ...
```

For address-of field rejection: the selector produces an lvalue via `x.mode_ = variable`. The address-of operation happens downstream; the type checker must reject `&(Varying[*S]).field`. Locate the unary `&` handler (in `expr.go` or `typexpr.go` — search for `token.AND` and `Pointer`) and add the rejection when the operand is a field selector through a `Varying[*S]` base:

```go
// In the & (address-of) operand check:
if sel, ok := e.X.(*ast.SelectorExpr); ok {
	baseType := check.singleValue(sel.X)   // or equivalent to get base type
	if _, ok := spmdUnwrapVaryingPointer(baseType); ok {
		check.errorf(e, UndefinedOp, "cannot take address of field through %s", baseType)
		goto Error
	}
}
```

(The sub-agent should verify the actual AST walk for address-of and place the check at the right level. Goal: the ERROR regex in Task 1 Step 2 matches the diagnostic wording `cannot take address of field through Varying[*T]`.)

- [ ] **Step 5: Re-run the types tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./go/types -run TestSpmd -v -count=1 -timeout=60s 2>&1 | tail -20
```

Expected: PASS for the `pointer_varying.go` cases including the new field accesses and ERROR rejections.

- [ ] **Step 6: Run the full go/types test suite to confirm no regression**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./go/types -timeout=300s -count=1
```

Expected: PASS. The new helper is gated by `buildcfg.Experiment.SPMD`, so non-SPMD runs are unaffected.

- [ ] **Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/lookup_ext_spmd.go src/go/types/call_ext_spmd.go src/go/types/call.go
git commit -m "types: accept field access through Varying[*Struct]

Adds spmdUnwrapVaryingPointer helper and extends spmdWrapFieldType
to recognize Varying[*S] receivers (mirror of the existing
*Varying[S] path). Selector path unwraps once for field lookup,
then wraps the result in Varying[fieldT]. Method calls and
address-of-field on Varying[*S] are rejected per the design's
non-goals."
```

---

## Task 3: types2 checker — mirror the fix

**Files:**
- Create: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/lookup_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/call_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/call.go` (selector path)
- Create/Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/testdata/spmd/pointer_varying.go`

types2 is the `cmd/compile` type checker (stock-compile path). It mirrors `go/types` almost line-for-line for SPMD logic.

- [ ] **Step 1: Check if types2 already has a `testdata/spmd/pointer_varying.go`**

```bash
ls /home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/testdata/spmd/ 2>&1 | head
```

If the file exists, copy the post-fix content from the `go/types` fixture (Task 1 Step 1 + Step 2). If it doesn't exist, create it with the same content.

- [ ] **Step 2: Create the types2 unwrap helper**

Create `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/lookup_ext_spmd.go` as a line-for-line mirror of the types version (Task 2 Step 1), substituting the package name and any receiver differences. The body of `spmdUnwrapVaryingPointer` is identical in structure.

- [ ] **Step 3: Extend types2 `spmdWrapFieldType`**

Open `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/call_ext_spmd.go` (around line 97 per the earlier exploration) and apply the same extension as Task 2 Step 2. The body is identical to go/types modulo cosmetic naming.

- [ ] **Step 4: Thread the unwrap through types2 selector**

Open `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/call.go` and find the types2 selector handler (around line 881 — look for the `check.recordSelection(..., FieldVal, ...)` call). Apply the same unwrap + error-gating logic as Task 2 Steps 3-4.

- [ ] **Step 5: Build and run types2 tests**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./cmd/compile/internal/types2 -run TestSpmd -v -count=1 -timeout=60s 2>&1 | tail -20
```

Expected: PASS.

- [ ] **Step 6: Full types2 test suite**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go test ./cmd/compile/internal/types2 -timeout=300s -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/cmd/compile/internal/types2/lookup_ext_spmd.go \
        src/cmd/compile/internal/types2/call_ext_spmd.go \
        src/cmd/compile/internal/types2/call.go \
        src/cmd/compile/internal/types2/testdata/spmd/pointer_varying.go
git commit -m "types2: mirror Varying[*Struct] field access

Line-for-line mirror of the go/types change so cmd/compile and
TinyGo accept the same SPMD field-access pattern."
```

---

## Task 4: SSA layer — failing tests

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_pointer_test.go`

Model the new tests on the existing `TestSPMDPointerVaryingFieldAccess` (same file, currently the top test function). Two new tests: gather (read) and scatter (write).

- [ ] **Step 1: Add `TestSPMDVaryingPointerFieldAccess` (gather)**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_pointer_test.go`:

```go
// TestSPMDVaryingPointerFieldAccess verifies that a field access through a
// Varying[*Struct] value (a per-lane pointer vector) produces:
//
//  1. FieldAddr.Type() == Varying[*fieldType]  (vector of per-lane field addresses)
//  2. UnOp(MUL).Type() == Varying[fieldType]   (gather-load of the field value)
//
// This is the mirror of TestSPMDPointerVaryingFieldAccess for the other
// direction of pointer-varying (varying pointer rather than pointer to
// varying).
func TestSPMDVaryingPointerFieldAccess(t *testing.T) {
	src := `package main

import "lanes"

type Point struct{ X, Y int }

func accessFields(pointPtrs lanes.Varying[*Point]) lanes.Varying[int] {
	return pointPtrs.X
}

func main() {
	for i := range 16 {
		_ = i
	}
}
`
	pkg := buildSSAWithSPMD(t, src)

	fn := pkg.Func("accessFields")
	if fn == nil {
		t.Fatal("accessFields function not found in SSA")
	}

	var faddr *ssa.FieldAddr
	var load *ssa.UnOp
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			switch v := instr.(type) {
			case *ssa.FieldAddr:
				faddr = v
			case *ssa.UnOp:
				if v.Op == token.MUL {
					load = v
				}
			}
		}
	}
	if faddr == nil {
		t.Fatal("no FieldAddr found in accessFields")
	}
	if load == nil {
		t.Fatal("no UnOp(MUL) load found in accessFields")
	}

	// FieldAddr on Varying[*Point] must produce Varying[*int].
	sv, ok := faddr.Type().(*types.SPMDType)
	if !ok {
		t.Fatalf("FieldAddr.Type() = %s; want Varying[*int]", faddr.Type())
	}
	if _, ok := sv.Elem().(*types.Pointer); !ok {
		t.Fatalf("FieldAddr.Type() inner = %s; want *int", sv.Elem())
	}

	// The load must produce Varying[int].
	lsv, ok := load.Type().(*types.SPMDType)
	if !ok {
		t.Fatalf("UnOp(MUL).Type() = %s; want Varying[int]", load.Type())
	}
	if basic, ok := lsv.Elem().(*types.Basic); !ok || basic.Kind() != types.Int {
		t.Fatalf("UnOp(MUL).Type() inner = %s; want int", lsv.Elem())
	}
}
```

- [ ] **Step 2: Add `TestSPMDVaryingPointerFieldStore` (scatter)**

Append to the same file:

```go
// TestSPMDVaryingPointerFieldStore verifies that a field assignment through a
// Varying[*Struct] value produces a Store with:
//
//   - Addr operand Type() == Varying[*fieldType]  (per-lane addresses)
//   - Val  operand Type() == Varying[fieldType]   (per-lane values)
//
// The backend is expected to lower this Store to a masked scatter.
func TestSPMDVaryingPointerFieldStore(t *testing.T) {
	src := `package main

import "lanes"

type Point struct{ X, Y int }

func writeFields(pointPtrs lanes.Varying[*Point], v lanes.Varying[int]) {
	pointPtrs.Y = v
}

func main() {
	for i := range 16 {
		_ = i
	}
}
`
	pkg := buildSSAWithSPMD(t, src)

	fn := pkg.Func("writeFields")
	if fn == nil {
		t.Fatal("writeFields function not found in SSA")
	}

	var store *ssa.Store
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			if s, ok := instr.(*ssa.Store); ok {
				store = s
			}
		}
	}
	if store == nil {
		t.Fatal("no Store found in writeFields")
	}

	// Addr must be Varying[*int].
	addrSv, ok := store.Addr.Type().(*types.SPMDType)
	if !ok {
		t.Fatalf("Store.Addr.Type() = %s; want Varying[*int]", store.Addr.Type())
	}
	if _, ok := addrSv.Elem().(*types.Pointer); !ok {
		t.Fatalf("Store.Addr.Type() inner = %s; want *int", addrSv.Elem())
	}

	// Val must be Varying[int].
	valSv, ok := store.Val.Type().(*types.SPMDType)
	if !ok {
		t.Fatalf("Store.Val.Type() = %s; want Varying[int]", store.Val.Type())
	}
	if basic, ok := valSv.Elem().(*types.Basic); !ok || basic.Kind() != types.Int {
		t.Fatalf("Store.Val.Type() inner = %s; want int", valSv.Elem())
	}
}
```

- [ ] **Step 3: Run the new tests — expect failure**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDVaryingPointerField' -v -count=1 -timeout=60s 2>&1 | tail -30
```

Expected: FAIL. Before Tasks 5, the SSA builder sets `FieldAddr.Type()` to `*int` (scalar Go), not `Varying[*int]`. Red baseline confirmed.

- [ ] **Step 4: Commit the failing tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_pointer_test.go
git commit -m "ssa: add failing tests for Varying[*Struct] field access

TDD red baseline for the gather and scatter paths through a
per-lane pointer vector. Asserts FieldAddr.Type() is Varying[*T],
the subsequent load is Varying[T], and a field-store produces a
Store with Varying[*T] address and Varying[T] value operands."
```

---

## Task 5: SSA layer — fix

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_varying.go` (add helper)
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/builder.go:1418-1423` (use helper)

The existing builder site at `builder.go:1418-1423` hardcodes `types.NewPointer(sf.Type())` as the FieldAddr's result type. We introduce a helper that returns the SPMD-aware result type and call it at the construction site. Other FieldAddr construction sites in builder.go (there may be more than one) receive the same treatment — sub-agent should grep for `FieldAddr{` to find them all.

- [ ] **Step 1: Add the helper to `spmd_varying.go`**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_varying.go`:

```go
// spmdFieldAddrResultType returns the SSA-level type for a FieldAddr
// whose base has type baseT and whose struct field has type fieldT.
//
//   - *Varying[S] base  → *Varying[fieldT]  (existing behavior; uniform ptr to varying struct).
//   - Varying[*S] base  → Varying[*fieldT]  (new; per-lane pointer vector).
//   - Otherwise         → *fieldT           (normal Go field-address).
//
// This mirrors the asymmetry between *Varying[T] and Varying[*T] at the
// type-checker level (see go/types/call_ext_spmd.go spmdWrapFieldType).
func spmdFieldAddrResultType(baseT, fieldT types.Type) types.Type {
	// *Varying[S] base → *Varying[fieldT]
	if ptr, ok := baseT.(*types.Pointer); ok {
		if sv, ok := ptr.Elem().(*types.SPMDType); ok {
			// Wrap the field type in Varying[] too, yielding *Varying[fieldT].
			// The inner struct's field type is whatever the selector returned.
			_ = sv
			return types.NewPointer(types.NewSPMDType(fieldT))
		}
	}
	// Varying[*S] base → Varying[*fieldT]
	if sv, ok := baseT.(*types.SPMDType); ok {
		if _, ok := sv.Elem().(*types.Pointer); ok {
			return types.NewSPMDType(types.NewPointer(fieldT))
		}
	}
	// Normal Go.
	return types.NewPointer(fieldT)
}
```

(Note: `types.NewSPMDType` is the existing constructor for the SSA-level SPMD-aware wrapper. If the package uses a different name — `NewVarying`, `NewSPMD`, etc. — the sub-agent should substitute appropriately. Grep `x-tools-spmd/go/ssa` for `SPMDType{` to find the constructor.)

- [ ] **Step 2: Use the helper at the FieldAddr construction site**

Open `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/builder.go` at line 1418 and change:

```go
faddr := &FieldAddr{
    X:     addr,
    Field: fieldIndex,
}
faddr.setPos(pos)
faddr.setType(types.NewPointer(sf.Type()))
```

to:

```go
faddr := &FieldAddr{
    X:     addr,
    Field: fieldIndex,
}
faddr.setPos(pos)
faddr.setType(spmdFieldAddrResultType(addr.Type(), sf.Type()))
```

- [ ] **Step 3: Find and update any other FieldAddr construction sites**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
grep -n "FieldAddr{" go/ssa/*.go
```

For each additional hit, inspect whether the `setType` call there uses `types.NewPointer(fieldT)` — if yes, replace with `spmdFieldAddrResultType(base.Type(), fieldT)`.

- [ ] **Step 4: Re-run the SSA tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDVaryingPointerField' -v -count=1 -timeout=60s 2>&1 | tail -15
```

Expected: PASS for both new tests.

- [ ] **Step 5: Run the full SSA test suite**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -timeout=300s -count=1
```

Expected: PASS (no regression in existing tests).

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_varying.go go/ssa/builder.go
git commit -m "ssa: set FieldAddr result type for Varying[*Struct]

Adds spmdFieldAddrResultType helper used at FieldAddr construction.
For Varying[*S] base types the result is Varying[*fieldT] (per-lane
pointer vector); for *Varying[S] the existing *Varying[fieldT] shape
is preserved. Enables downstream gather-load and scatter-store."
```

---

## Task 6: Verification probe — does Case B scatter-store work today?

**Files:** None (diagnostic only).

Per spec §5.5 and §4.3, verify whether the existing TinyGo Case B path (plain `*S` pointer with `<N x ptr>` LLVM value from a varying IndexAddr) already supports scatter-store. If yes, Task 8's Case D inherits for free. If no, Task 8 widens to add scatter-store dispatch.

- [ ] **Step 1: Write the probe fixture**

```bash
cat <<'EOF' > /tmp/scatter-probe.go
package main

import "lanes"

type Pt struct{ X, Y int }

var arr [16]Pt

func main() {
	go for i := range 16 {
		arr[i].X = int(i) * 3 // Case B: *S IndexAddr + FieldAddr + Store
	}
	_ = lanes.Count[int]() // keep lanes import used
}
EOF
```

- [ ] **Step 2: Compile and dump LLVM IR**

```bash
cd /home/cedric/work/SPMD
PATH=go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -print-llvm-ir -o /tmp/scatter-probe /tmp/scatter-probe.go 2>&1 > /tmp/scatter-probe.ll
```

(If `-print-llvm-ir` isn't a valid flag, use `-wasm-abi=generic -o /tmp/scatter-probe.ll` in WASI or inspect via `llvm-dis` on an emitted `.bc`. The sub-agent picks the right flag from `tinygo build -help`.)

- [ ] **Step 3: Inspect the IR for the scatter pattern**

```bash
grep -E "masked\.scatter|masked\.store" /tmp/scatter-probe.ll | head
```

- [ ] **Step 4a: If scatter is present (expected path)**

```bash
grep -E "masked\.scatter|masked\.store" /tmp/scatter-probe.ll | head
# Output expected to show @llvm.masked.scatter or @llvm.masked.store.vN
```

Case B already scatters. Task 8's Case D inherits for free — no plan widening needed. Document in the Task 8 commit message that this probe confirmed Case B scatter.

- [ ] **Step 4b: If scatter is absent (widen-scope path)**

```bash
grep -E "store " /tmp/scatter-probe.ll | head
# Output may show per-lane conditional stores or an unmasked scalar store
# that would overwrite all lanes (the bug).
```

If the Store handler does NOT dispatch to scatter for `<N x ptr>` addresses, this is a pre-existing latent bug in Case B. Widen the plan: insert a Task 8.5 that adds scatter-store dispatch in the `*ssa.Store` handler in `tinygo/compiler/compiler.go`, using the same `<N x ptr>` detection. The fix pattern is:

```go
// In the *ssa.Store case, after resolving addr/val:
if addr.Type().TypeKind() == llvm.VectorTypeKind &&
    addr.Type().ElementType() == b.dataPtrType {
    // Per-lane pointer vector: emit masked scatter.
    mask := b.spmdCurrentMask()  // may need to pull from an SSA operand
    b.CreateCall(b.getMaskedScatterIntrinsic(val.Type()),
        []llvm.Value{val, addr, /*alignment*/, mask}, "")
    return nil
}
```

(Exact intrinsic lookup + calling convention to be determined by the sub-agent from existing scatter call sites in `tinygo/compiler/spmd.go`.)

- [ ] **Step 5: Clean up the probe**

```bash
rm /tmp/scatter-probe.go /tmp/scatter-probe /tmp/scatter-probe.ll
```

No commit for this task — it's a one-shot diagnostic. Report the result (Case B works or widen-plan) in the implementation session summary.

---

## Task 7: TinyGo backend — failing tests

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Model on existing IR tests in the file. If the existing pattern is `compileTestAndAssertIR(t, source, wantIRPatterns)`, reuse it. Sub-agent should read the first 100 lines of `spmd_test.go` to identify the helper convention.

- [ ] **Step 1: Add `TestSPMDVaryingPointerFieldAddr_Gather`**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`:

```go
// TestSPMDVaryingPointerFieldAddr_Gather verifies that a field read through
// a Varying[*Struct] value compiles to per-lane GEPs (via spmdFieldAddrPerLane)
// followed by a masked gather or masked vector load.
func TestSPMDVaryingPointerFieldAddr_Gather(t *testing.T) {
	src := `package main

import "lanes"

type Point struct{ X, Y int }

var pts [16]Point

func main() {
	var acc lanes.Varying[int]
	go for i := range 16 {
		p := &pts[i]   // Varying[*Point]
		acc += p.X      // gather read
	}
	_ = acc
}
`
	ir := compileToLLVMIR(t, src)
	mustContain(t, ir, "extractelement")        // per-lane ptr extract in spmdFieldAddrPerLane
	mustContain(t, ir, "getelementptr")          // per-lane field GEP
	mustContain(t, ir, "insertelement")          // reassemble into <N x ptr>
	// After the FieldAddr builds <N x ptr>, the load must be a gather or
	// masked vector load (if the base access is flagged contiguous).
	mustContainAny(t, ir, "masked.gather", "masked.load")
}
```

(`compileToLLVMIR` and `mustContain`/`mustContainAny` assumed to be existing helpers in the file; sub-agent verifies and uses whatever helper signature is already in use. If they don't exist, add thin versions at the bottom of the test file.)

- [ ] **Step 2: Add `TestSPMDVaryingPointerFieldAddr_Scatter`**

Append:

```go
// TestSPMDVaryingPointerFieldAddr_Scatter verifies that a field write through
// a Varying[*Struct] value compiles to a masked scatter or masked vector store.
func TestSPMDVaryingPointerFieldAddr_Scatter(t *testing.T) {
	src := `package main

import "lanes"

type Point struct{ X, Y int }

var pts [16]Point

func main() {
	go for i := range 16 {
		p := &pts[i]   // Varying[*Point]
		p.Y = int(i)   // scatter write
	}
}
`
	ir := compileToLLVMIR(t, src)
	mustContain(t, ir, "extractelement")
	mustContain(t, ir, "getelementptr")
	mustContain(t, ir, "insertelement")
	mustContainAny(t, ir, "masked.scatter", "masked.store")
}
```

- [ ] **Step 3: Add `TestSPMDVaryingPointerFieldAddr_Contiguous`**

Append:

```go
// TestSPMDVaryingPointerFieldAddr_Contiguous verifies that when the base
// access is contiguous (derived from a uniform base + laneIndex), the
// FieldAddr propagates contiguous-ness via spmdFieldAddrForVaryingPtr and
// the backend emits masked vector load/store instead of scatter/gather.
func TestSPMDVaryingPointerFieldAddr_Contiguous(t *testing.T) {
	src := `package main

import "lanes"

type Point struct{ X, Y int }

var pts [16]Point

func kernel(base int) {
	go for i := range 16 {
		p := &pts[base+i]   // contiguous base: uniform base + varying i
		p.X = int(i)
	}
}
`
	ir := compileToLLVMIR(t, src)
	// Contiguous: expect masked.store, NOT masked.scatter.
	mustContain(t, ir, "masked.store")
	mustNotContain(t, ir, "masked.scatter")
}
```

- [ ] **Step 4: Run — expect failure**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -run 'TestSPMDVaryingPointerFieldAddr' -v -count=1 -timeout=120s 2>&1 | tail -30
```

Expected: FAIL (Case D doesn't exist yet — either a compile error in the generated source OR an assertion miss on missing IR patterns).

- [ ] **Step 5: Commit the failing tests**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
git commit -m "compiler: add failing LLVM IR tests for Varying[*T] field access

TDD red baseline for Case D: per-lane field GEPs + gather/scatter or
contiguous masked load/store. Tests fail today because the
FieldAddr handler has no branch for expr.X.Type() = Varying[*S]."
```

---

## Task 8: TinyGo backend — fix (Case D)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go:3098-3195` (add Case D)

- [ ] **Step 1: Insert Case D branch**

Open `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` at line 3098 (the `case *ssa.FieldAddr:` line). Immediately after the opening of the case and the `val := b.getValue(expr.X, getPos(expr))` line (line 3099), and BEFORE the `if ptr, ok := expr.X.Type().Underlying().(*types.Pointer); ok` branch (line 3121), insert:

```go
			// Case D: expr.X has SSA type Varying[*S] (a vector of per-lane pointers).
			// val is a <N x ptr> LLVM vector. Treat identically to Case B
			// (per-lane GEPs into the pointed-to struct) but source the struct
			// layout from the inner pointer's element type.
			if spmdType, ok := expr.X.Type().Underlying().(*types.SPMDType); ok {
				if ptr, ok := spmdType.Elem().(*types.Pointer); ok {
					structLLVMType := b.getLLVMType(ptr.Elem())
					if val.Type().TypeKind() == llvm.VectorTypeKind {
						laneCount := val.Type().VectorSize()
						result := b.spmdFieldAddrPerLane(val, structLLVMType, expr.Field, laneCount)
						b.spmdFieldAddrForVaryingPtr(expr, result)
						return result, nil
					}
				}
			}
```

The branch sits BEFORE the `*types.Pointer` dispatch because `Varying[*S]` is an `*SPMDType`, not a `*Pointer`. The existing Cases A/B/C are unchanged.

- [ ] **Step 2: Build TinyGo**

```bash
cd /home/cedric/work/SPMD
make build-tinygo
```

Expected: clean build.

- [ ] **Step 3: Run the new tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -run 'TestSPMDVaryingPointerFieldAddr' -v -count=1 -timeout=120s 2>&1 | tail -30
```

Expected: PASS for all three new tests.

- [ ] **Step 4: Run the broader compiler tests for regression**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -timeout=600s -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go
git commit -m "compiler: add Case D for FieldAddr on Varying[*Struct]

When expr.X has SSA type Varying[*S] (an *SPMDType wrapping a
pointer), the LLVM value is a <N x ptr> vector and the result is
a <N x ptr> vector of per-lane field addresses. Reuses the
existing spmdFieldAddrPerLane helper from Case B; contiguous
propagation via spmdFieldAddrForVaryingPtr enables subsequent
SPMDLoad/SPMDStore to use masked vector load/store on contiguous
access, falling back to gather/scatter otherwise."
```

---

## Task 9: E2E regression — re-enable tinybench blockers

**Files:**
- Delete: `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/BLOCKER.md`
- Delete: `/home/cedric/work/SPMD/tinybench/n-body-nosqrt/go-spmd/BLOCKER.md`
- Delete: `/home/cedric/work/SPMD/tinybench/BLOCKERS.md`

- [ ] **Step 1: Manually compile each blocked port — expect success**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
ls -la /tmp/nb-spmd-bin
```

Expected: binary produced. If compile fails, the fix is incomplete — stop and diagnose, do NOT delete BLOCKER.md.

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
ls -la /tmp/nbns-spmd-bin
```

Expected: binary produced.

- [ ] **Step 2: Manual output parity check (smallest args for speed)**

```bash
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
rm /tmp/nb-go-bin

go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
rm /tmp/nbns-go-bin
rm /tmp/nb-spmd-bin /tmp/nbns-spmd-bin
```

Expected: no diff output for either benchmark.

If `%.9f` last-digit float mismatch appears, this is the pre-existing float-reassociation risk from tinybench spec §5.2 (not this feature's fault). In that case: DO NOT proceed with blocker removal; instead, update the BLOCKER.md files with the new cause (`float reassociation in reduce.Add diverges from scalar sequential accumulation`) and commit that update as its own commit. The compiler feature is complete; the benchmark-level blocker has a different cause now.

- [ ] **Step 3: TestCorrectness gate**

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body/go-spmd/BLOCKER.md n-body-nosqrt/go-spmd/BLOCKER.md
go test -v -run 'TestCorrectness/(n-body|n-body-nosqrt)' -timeout=900s 2>&1 | tail -20
```

Expected: `--- PASS: TestCorrectness/n-body` and `--- PASS: TestCorrectness/n-body-nosqrt`.

- [ ] **Step 4: Full tinybench TestCorrectness sweep**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run TestCorrectness -timeout=1800s 2>&1 | tail -20
```

Expected: all 5 benchmark subtests PASS (fannkuch-redux, fasta, n-body, n-body-nosqrt, spectral-norm); `results/` still SKIPs (non-benchmark).

- [ ] **Step 5: Delete top-level BLOCKERS.md**

```bash
cd /home/cedric/work/SPMD/tinybench
rm BLOCKERS.md
ls BLOCKERS.md 2>&1
```

Expected: `ls: cannot access 'BLOCKERS.md': No such file or directory` — aggregation file is gone since no blockers remain.

- [ ] **Step 6: Commit the blocker removals (tinybench submodule)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add -A
git status --short
# Expected output:
#  D BLOCKERS.md
#  D n-body/go-spmd/BLOCKER.md
#  D n-body-nosqrt/go-spmd/BLOCKER.md
git commit -m "test: re-enable n-body and n-body-nosqrt go-spmd ports

The SPMD fork now supports field access through Varying[*Struct]
(per-lane pointer vectors), so the two blocked benchmarks compile
and produce byte-identical output to stock go. TestCorrectness
passes for both.

The top-level BLOCKERS.md is deleted per the convention
(absence = all clear)."
```

- [ ] **Step 7: Bump submodule pointers in the parent SPMD repo**

```bash
cd /home/cedric/work/SPMD
git add go x-tools-spmd tinygo tinybench
git status --short
git commit -m "deps: bump forks for Varying[*Struct] field access

- go: types + types2 accept field access through Varying[*S]
- x-tools-spmd: FieldAddr result type set for Varying[*S] base
- tinygo: compiler Case D emits per-lane GEPs + gather/scatter
- tinybench: n-body and n-body-nosqrt go-spmd ports re-enabled"
```

---

## Self-Review

### 1. Spec coverage

Each spec section mapped to a task:

- Spec §1 (overview/goals/scope/invariants): Plan header + Task 0 preflight confirms baseline.
- Spec §2.1 (field-lookup unwrap): Task 2 Step 1 (helper), Task 2 Step 3 (call site). Mirror in Task 3.
- Spec §2.2 (field-type wrap): Task 2 Step 2. Mirror in Task 3.
- Spec §2.3 (scatter-store acceptance): falls out automatically; Task 1 Step 1 tests the rvalue+lvalue forms.
- Spec §2.4 (types2 mirror): Task 3.
- Spec §2.5 (type-checker tests): Task 1.
- Spec §3.1 (SSA FieldAddr result type): Task 5 Steps 1-3.
- Spec §3.2 (no new instructions): implicit; plan adds no new SSA opcodes.
- Spec §3.3 (SSA tests): Task 4.
- Spec §3.4 (no predication changes): implicit; plan doesn't touch those files.
- Spec §4.1 (TinyGo Case D): Task 8 Step 1.
- Spec §4.2 (LLVM type): Task 8 Step 3 validates via test execution.
- Spec §4.3 (scatter-store path): Task 6 verification probe, Task 8.5 if probe fails.
- Spec §4.4 (gather-load path): Task 7 Step 1 test, Task 8.
- Spec §4.5 (mask threading): no changes; implicit.
- Spec §4.6 (TinyGo unit tests): Task 7.
- Spec §5.1-§5.4 (testing levels): distributed across Tasks 1, 4, 7, 9.
- Spec §5.5 (verification probe): Task 6.
- Spec §6.1 (rollout order): tasks are in the spec's recommended order.
- Spec §6.2 (file-by-file changes): File Structure table at plan top matches spec §6.2.
- Spec §6.3 (risks): Task 6 catches risk 1; Task 9 Step 2 catches risk 2 (float reassociation); Task 8 Step 3 catches risk 3 (LLVM type lowering); all mirrored tasks guard risk 4 (types/types2 divergence).
- Spec §6.4 (deferred): not implemented by design.
- Spec §6.5 (success criteria): Task 2 Step 6 (types), Task 3 Step 6 (types2), Task 5 Step 5 (SSA), Task 8 Step 4 (TinyGo), Task 9 Steps 3-4 (E2E).

No gaps.

### 2. Placeholder scan

- No "TBD/TODO/FIXME" in task content.
- Task 2 Step 3 and Step 4 say "The exact insertion point depends on the current selector implementation" and "The sub-agent should verify the actual AST walk". These are guidance instructions, not placeholders — the steps specify WHERE to look and WHAT to insert, with representative code. The sub-agent reads the real file to splice cleanly (same as Task 2 Step 2 reads the current body of `spmdWrapFieldType` to replace it). Accepted.
- Task 5 Step 3 ("find any other FieldAddr sites") is a grep instruction with a concrete command. Accepted.
- Task 6 Step 4b provides a widen-scope template with specific instructions. Not a placeholder — it's a conditional expansion triggered by Step 4a/4b decision.

### 3. Type consistency

- `spmdUnwrapVaryingPointer(t Type) (Type, bool)` — same signature across Tasks 2-3.
- `spmdWrapFieldType(receiverType, fieldType Type) Type` — unchanged signature, extended body.
- `spmdFieldAddrResultType(baseT, fieldT types.Type) types.Type` — introduced in Task 5, used consistently.
- `spmdFieldAddrPerLane(ptrVec llvm.Value, structType llvm.Type, fieldIndex, laneCount int) llvm.Value` — existing, reused in Task 8.
- `spmdFieldAddrForVaryingPtr(expr *ssa.FieldAddr, fieldGEP llvm.Value)` — existing, reused in Task 8.
- Test names: `TestSPMDVaryingPointerFieldAccess`, `TestSPMDVaryingPointerFieldStore`, `TestSPMDVaryingPointerFieldAddr_Gather/_Scatter/_Contiguous` — consistent.

Consistent.
