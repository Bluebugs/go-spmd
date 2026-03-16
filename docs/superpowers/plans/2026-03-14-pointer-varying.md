# Pointer-Varying Field Access Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix `structPointers` compile failure — field access through `*Varying[Struct]` (pointer to varying struct) should produce `Varying[fieldType]`, not `fieldType`.

**Architecture:** Four-layer fix: (1) go/types selector wraps field type in Varying when receiver is `*Varying[Struct]`; (2) x-tools-spmd `typeparams.MustDeref` handles `Varying[*T]` so emitLoad doesn't panic; (3) x-tools-spmd `emitFieldSelection` emits `FieldAddr` of type `Varying[*fieldT]` for per-lane scatter/gather; (4) TinyGo FieldAddr handler produces a vector of per-lane field pointers `<N x ptr>`.

**Tech Stack:** Go type checker (go/types + types2), x-tools-spmd (SSA emit layer), TinyGo LLVM backend (compiler.go). All changes gated behind `buildcfg.Experiment.SPMD`.

---

## Background and Root Cause

The failing example `structPointers()` in `examples/pointer-varying/main.go`:

```go
points := [4]Point{{1,2},{3,4},{5,6},{7,8}}
go for i := range 4 {
    pointPtr := &points[i]       // varying i → pointPtr: *Varying[Point]
    pointPtr.X += lanes.Index()  // ERROR: Varying[int] cannot assign to int
    pointPtr.Y += lanes.Index() * 2
}
```

**Error**: `cannot use pointPtr.X (value of type lanes.Varying[int]) as int value in assignment`

Root cause: compound assignment `pointPtr.X += lanes.Index()` expands to:
1. Read `pointPtr.X` → type `int` (selector sets raw field type)
2. Compute `int + Varying[int]` → promoted to `Varying[int]` (binary op promotion)
3. Assign `Varying[int]` back to `pointPtr.X` (type `int`) → FAILS

The selector result type must be `Varying[int]` for a `*Varying[Point]` receiver.

**Already working (no changes needed):**
- `scatterGatherOperations`: explicit `var ptr lanes.Varying[*int] = &targets[i]` ✓
- `pointerArithmetic`: `varyingPtr := &data[index]`, `*varyingPtr`, `*varyingPtr = val` ✓
- `indirectAccess`: `ptr := ptrs[i]; _ = *ptr` ✓

**Deferred features in example (not fixed here, simplified/removed):**
- Lines 24,25,117,120: `(*Varying[array])[i]` — indexing a Varying array — deferred
- Line 169: unused `i` — example fix
- Line 188: `*Varying[int]` vs `*Varying[[8]int]` type mismatch — example redesign

## File Structure

**Modified/created:**
- `go/src/go/types/testdata/spmd/pointer_varying.go` — NEW: type checker test
- `go/src/go/types/call_ext_spmd.go` — NEW: `spmdWrapFieldType` helper
- `go/src/go/types/call.go:834,882` — save receiver type, apply wrapping
- `go/src/cmd/compile/internal/types2/call_ext_spmd.go` — NEW: mirror helper
- `go/src/cmd/compile/internal/types2/call.go` — mirror of call.go changes
- `x-tools-spmd/internal/typeparams/coretype.go` — `Deref`/`MustDeref` for `Varying[*T]`
- `x-tools-spmd/go/ssa/emit.go` — `emitFieldSelection` for `*Varying[Struct]`
- `x-tools-spmd/go/ssa/spmd_pointer_test.go` — NEW: SSA emit test
- `tinygo/compiler/compiler.go:2709` — FieldAddr handler for `*Varying[Struct]` X
- `tinygo/compiler/spmd_llvm_test.go` — NEW: `TestSPMDFieldAddrVaryingPtr`
- `examples/pointer-varying/main.go` — simplify/fix

---

## Chunk 1: Type Checker Fix (go/types + types2)

### Task 1: Write type-checker test capturing current behavior

**Files:**
- Create: `go/src/go/types/testdata/spmd/pointer_varying.go`

The go/types test runner (`TestSPMDTypeChecking`) expects `// ERROR "..."` annotations on lines that produce errors. The test PASSES when actual errors match annotations. Strategy: annotate current errors (test passes red-→-green), implement fix, remove annotations (test passes again).

- [ ] **Step 1: Create test file showing current errors**

```go
// go/src/go/types/testdata/spmd/pointer_varying.go
// -goexperiment spmd

// Test file for pointer-to-varying-struct field access.
// Before fix: field access through *Varying[Struct] gives uniform field type,
// so compound assignment fails. ERROR annotations capture current behavior.
// After fix: remove ERROR annotations — both lines should compile cleanly.
package p

import "lanes"

type Point struct{ X, Y int }

func structPointers() {
	points := [4]Point{}
	go for i := range 4 {
		pointPtr := &points[i] // *Varying[Point]
		pointPtr.X = lanes.Index() // ERROR "cannot assign varying"
		pointPtr.Y = lanes.Index() // ERROR "cannot assign varying"
	}
}

// Uniform pointer field access must still produce uniform field type (no change).
func uniformPtrField() {
	p := Point{1, 2}
	ptr := &p       // *Point (uniform)
	ptr.X = 42      // int assignment, must still compile without error
}
```

- [ ] **Step 2: Run to verify test PASSES with current code (errors match annotations)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go/bin/go test go/types -run TestSPMDTypeChecking -v 2>&1 | grep -E "pointer_varying|FAIL|PASS|ok"
```

Expected: `PASS` (the `// ERROR` annotations match the actual errors the current code produces).

- [ ] **Step 3: Commit initial test file**

```bash
git add go/src/go/types/testdata/spmd/pointer_varying.go
git commit -m "test: add pointer_varying type checker test (errors expected pre-fix)"
```

---

### Task 2: Fix go/types selector field type wrapping

**Files:**
- Create: `go/src/go/types/call_ext_spmd.go`
- Modify: `go/src/go/types/call.go`

- [ ] **Step 1: Create the helper in a new ext file**

```go
// go/src/go/types/call_ext_spmd.go
// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license.

package types

import "internal/buildcfg"

// spmdWrapFieldType wraps fieldType in Varying when the selector receiver is
// *Varying[Struct]. This ensures per-lane field access for each lane's struct.
//
// Example: pointPtr: *Varying[Point] → pointPtr.X: Varying[int]
//
// Only applies when:
//   - SPMD experiment is active
//   - receiverType is *Pointer{base: *SPMDType}
//   - fieldType is not already *SPMDType (avoid double-wrapping)
func spmdWrapFieldType(receiverType, fieldType Type) Type {
	if !buildcfg.Experiment.SPMD {
		return fieldType
	}
	ptr, ok := receiverType.(*Pointer)
	if !ok {
		return fieldType
	}
	if _, ok := ptr.base.(*SPMDType); !ok {
		return fieldType
	}
	if _, alreadyVarying := fieldType.(*SPMDType); alreadyVarying {
		return fieldType
	}
	return NewVarying(fieldType)
}
```

- [ ] **Step 2: Integrate into call.go**

In `go/src/go/types/call.go`, the `selector()` function:
- At line 834, `x.typ()` is still the receiver type (`*Varying[Point]`) — it has NOT been modified yet by `lookupFieldOrMethod`. Save it before the lookup.
- At line 882, `x.typ_` is set to `obj.typ` (the field's raw type). Apply wrapping after.

Make two edits:

**Edit A** — save receiver type immediately before line 834:
```go
receiverType := x.typ() // save for SPMD field type wrapping (SPMD experiment)
obj, index, indirect = lookupFieldOrMethod(x.typ(), x.mode() == variable, check.pkg, sel, false)
```

**Edit B** — apply wrapping after line 882:
```go
x.typ_ = obj.typ
if buildcfg.Experiment.SPMD {
	x.typ_ = spmdWrapFieldType(receiverType, x.typ_)
}
```

Note: `buildcfg` is already imported in call.go (check with grep if needed; if not, add the import).

- [ ] **Step 3: Update test file — remove ERROR annotations**

Since the fix makes `pointPtr.X = lanes.Index()` compile without error, remove the `// ERROR` annotations from `pointer_varying.go`:

```go
pointPtr.X = lanes.Index() // (no error annotation)
pointPtr.Y = lanes.Index() // (no error annotation)
```

- [ ] **Step 4: Run go/types tests — verify PASS**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go/bin/go test go/types -run TestSPMDTypeChecking -v 2>&1 | grep -E "pointer_varying|FAIL|PASS|ok"
```

Expected: `PASS` (no errors from annotations, no unexpected errors).

- [ ] **Step 5: Run full go/types test suite**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go/bin/go test go/types 2>&1 | tail -5
```

Expected: `ok go/types`

- [ ] **Step 6: Commit**

```bash
git add go/src/go/types/call.go go/src/go/types/call_ext_spmd.go go/src/go/types/testdata/spmd/pointer_varying.go
git commit -m "fix: wrap field type in Varying for *Varying[Struct] selector in go/types"
```

---

### Task 3: Mirror fix in types2

**Files:**
- Create: `go/src/cmd/compile/internal/types2/call_ext_spmd.go`
- Modify: `go/src/cmd/compile/internal/types2/call.go`

- [ ] **Step 1: Copy helper to types2**

```go
// go/src/cmd/compile/internal/types2/call_ext_spmd.go
// (same as go/types version but package types2)
package types2

import "internal/buildcfg"

func spmdWrapFieldType(receiverType, fieldType Type) Type {
	if !buildcfg.Experiment.SPMD {
		return fieldType
	}
	ptr, ok := receiverType.(*Pointer)
	if !ok {
		return fieldType
	}
	if _, ok := ptr.base.(*SPMDType); !ok {
		return fieldType
	}
	if _, alreadyVarying := fieldType.(*SPMDType); alreadyVarying {
		return fieldType
	}
	return NewVarying(fieldType)
}
```

- [ ] **Step 2: Apply same two edits to types2/call.go**

Find the equivalent selector field case in `go/src/cmd/compile/internal/types2/call.go`. The code structure mirrors go/types exactly. Apply identical edits (save `receiverType := x.typ()` before `lookupFieldOrMethod`, apply `spmdWrapFieldType` after `x.typ = obj.Type()`).

Note: types2 uses `x.typ` (direct field) rather than `x.typ_` (accessor). Verify the exact field names by reading the file first.

- [ ] **Step 3: Run types2 tests**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go/bin/go test cmd/compile/internal/types2 2>&1 | tail -5
```

Expected: `ok cmd/compile/internal/types2`

- [ ] **Step 4: Commit**

```bash
git add go/src/cmd/compile/internal/types2/call.go go/src/cmd/compile/internal/types2/call_ext_spmd.go
git commit -m "fix: wrap field type in Varying for *Varying[Struct] selector in types2"
```

---

## Chunk 2: x-tools-spmd SSA Layer

After the go/types fix, `pointPtr.X` has type `Varying[int]` in the type checker. Now x-tools-spmd's SSA builder must generate the right instruction types.

**Without this fix:** `emitFieldSelection` emits `FieldAddr{type=*int}` → `emitLoad` calls `typeparams.MustDeref(*int)=int` → `UnOp{type=int}` → `SPMDLoad{type=int}`. TinyGo tries a gather into `<4 x int>` but the FieldAddr result is a plain uniform pointer (not `<4 x ptr>`). Mismatch.

**With fix:** `emitFieldSelection` emits `FieldAddr{type=Varying[*int]}` → `typeparams.MustDeref(Varying[*int])=Varying[int]` → `UnOp{type=Varying[int]}` → `SPMDLoad{type=Varying[int]}`. TinyGo's FieldAddr handler produces `<4 x ptr>` (per-lane), then `spmdMaskedGather` produces `<4 x i64>`.

### Task 4: Fix typeparams.Deref/MustDeref for Varying[*T]

**Files:**
- Modify: `x-tools-spmd/internal/typeparams/coretype.go`

Note: `types.SPMDType` and `types.NewVarying` are accessible here — `free.go` in the same package already uses `*types.SPMDType`, and x-tools-spmd's `go.mod` replace directive points to the forked `go/types` which exports `NewVarying`.

- [ ] **Step 1: Modify Deref and MustDeref to handle Varying[*T]**

Current `Deref` (line 138) and `MustDeref` (line 150):

```go
func Deref(t types.Type) types.Type {
	// SPMD: Varying[*T] derefs to Varying[T].
	if spmd, ok := t.(*types.SPMDType); ok {
		if ptr, ok := spmd.Elem().(*types.Pointer); ok {
			return types.NewVarying(ptr.Elem())
		}
	}
	if ptr, ok := CoreType(t).(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

func MustDeref(t types.Type) types.Type {
	// SPMD: Varying[*T] derefs to Varying[T].
	if spmd, ok := t.(*types.SPMDType); ok {
		if ptr, ok := spmd.Elem().(*types.Pointer); ok {
			return types.NewVarying(ptr.Elem())
		}
	}
	if ptr, ok := CoreType(t).(*types.Pointer); ok {
		return ptr.Elem()
	}
	panic(fmt.Sprintf("%v is not a pointer", t))
}
```

- [ ] **Step 2: Run x-tools-spmd tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
go test ./go/ssa/... ./internal/typeparams/... 2>&1 | tail -10
```

Expected: PASS (no regressions in existing tests).

- [ ] **Step 3: Commit**

```bash
git add x-tools-spmd/internal/typeparams/coretype.go
git commit -m "fix: Deref/MustDeref handle Varying[*T] → Varying[T] in typeparams"
```

---

### Task 5: Fix emitFieldSelection for *Varying[Struct]

**Files:**
- Modify: `x-tools-spmd/go/ssa/emit.go`
- Create: `x-tools-spmd/go/ssa/spmd_pointer_test.go`

- [ ] **Step 1: Write a failing SSA test first**

The test builds a mini SPMD function with a `FieldAddr` via `emitFieldSelection` on a `*Varying[Point]` receiver and asserts the resulting `FieldAddr` instruction's type.

```go
// x-tools-spmd/go/ssa/spmd_pointer_test.go
package ssa_test

import (
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	// ... (follow imports from spmd_promote_test.go)
)

// TestSPMDFieldAddrVaryingPtrType verifies that emitFieldSelection for a
// *Varying[Struct] receiver produces a FieldAddr instruction with type
// Varying[*fieldType], enabling scatter/gather loads/stores.
func TestSPMDFieldAddrVaryingPtrType(t *testing.T) {
	const src = `
package p
import "lanes"
type Point struct{ X, Y int }
func structField() {
    points := [4]Point{}
    go for i := range 4 {
        ptr := &points[i]
        _ = ptr.X
    }
}
`
	// Build SSA with SPMD experiment active.
	// Follow the exact pattern from spmd_promote_test.go or spmd_loop_test.go.
	prog, pkg := buildSPMDSSA(t, src) // helper from spmd_loop_test.go
	_ = prog

	fn := pkg.Func("structField")
	if fn == nil {
		t.Fatal("function structField not found")
	}

	// Find FieldAddr instruction in the function body.
	var fieldAddr *ssa.FieldAddr
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if fa, ok := instr.(*ssa.FieldAddr); ok {
				fieldAddr = fa
				break
			}
		}
		if fieldAddr != nil {
			break
		}
	}
	if fieldAddr == nil {
		t.Fatal("no FieldAddr instruction found in function")
	}

	// Verify FieldAddr type is Varying[*int], not *int.
	spmdType, ok := fieldAddr.Type().(*types.SPMDType)
	if !ok {
		t.Fatalf("FieldAddr.Type() = %v, want *types.SPMDType (Varying[*int])", fieldAddr.Type())
	}
	ptrType, ok := spmdType.Elem().(*types.Pointer)
	if !ok {
		t.Fatalf("FieldAddr.Type().Elem() = %v, want *types.Pointer", spmdType.Elem())
	}
	basicType, ok := ptrType.Elem().(*types.Basic)
	if !ok || basicType.Kind() != types.Int {
		t.Fatalf("FieldAddr field elem type = %v, want int", ptrType.Elem())
	}
}
```

- [ ] **Step 2: Run to confirm test FAILS (FieldAddr currently has type *int)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
go test ./go/ssa/... -run TestSPMDFieldAddrVaryingPtrType -v 2>&1 | tail -10
```

Expected: FAIL with `FieldAddr.Type() = *int, want *types.SPMDType`.

- [ ] **Step 3: Fix emitFieldSelection in emit.go**

In `x-tools-spmd/go/ssa/emit.go`, the `emitFieldSelection` function (around line 575):

Add helper `isVaryingPtrStruct`:
```go
// isVaryingPtrStruct reports whether t is *Varying[Struct].
func isVaryingPtrStruct(t types.Type) bool {
	ptr, ok := t.(*types.Pointer)
	if !ok {
		return false
	}
	_, ok = ptr.Elem().(*types.SPMDType)
	return ok
}
```

In `emitFieldSelection`, after computing `fld`:
```go
fld := fieldOf(typeparams.MustDeref(v.Type()), index)
instr := &FieldAddr{X: v, Field: index}
instr.setPos(id.Pos())

// SPMD: *Varying[Struct] field access → each lane has its own field address.
// Use Varying[*fieldType] so subsequent loads/stores use scatter/gather.
if isVaryingPtrStruct(v.Type()) {
	instr.setType(types.NewVarying(types.NewPointer(fld.Type())))
} else {
	instr.setType(types.NewPointer(fld.Type()))
}
```

Also update the `lazyAddress` in `builder.go` (around line 589) that calls `emitFieldSelection`. The `addr()` function for `*ast.SelectorExpr` currently has:

```go
emit := func(fn *Function) Value {
    return emitFieldSelection(fn, v, index, true, e.Sel)
}
return &lazyAddress{addr: emit, t: fld.Type(), pos: e.Sel.Pos(), expr: e.Sel}
```

Change to (after the `fld` computation at line 581):

```go
emit := func(fn *Function) Value {
    return emitFieldSelection(fn, v, index, true, e.Sel)
}
// SPMD: *Varying[Struct] field address has type Varying[fieldType], not fieldType.
var lvalType types.Type = fld.Type()
if isVaryingPtrStruct(v.Type()) {
    lvalType = types.NewVarying(fld.Type())
}
return &lazyAddress{addr: emit, t: lvalType, pos: e.Sel.Pos(), expr: e.Sel}
```

This ensures `lazyAddress.typ()` returns `Varying[int]` (not `int`) so the SSA builder uses the correct type when checking or using the lvalue.

- [ ] **Step 4: Run test — verify PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
go test ./go/ssa/... -run TestSPMDFieldAddrVaryingPtrType -v 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 5: Run all x-tools-spmd tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
go test ./go/ssa/... 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add x-tools-spmd/go/ssa/emit.go x-tools-spmd/go/ssa/builder.go x-tools-spmd/go/ssa/spmd_pointer_test.go
git commit -m "fix: FieldAddr type Varying[*T] for *Varying[Struct] in x-tools-spmd emit"
```

---

## Chunk 3: TinyGo Backend

After x-tools-spmd fix, TinyGo receives `FieldAddr{X: *Varying[Point], type=Varying[*int]}`. The current handler calls `expr.X.Type().Underlying().(*types.Pointer).Elem()` = `Varying[Point]`, then `getLLVMType(Varying[Point])` — a struct SPMDType — which would fail. The fix: detect `*Varying[Struct]` and produce per-lane GEPs.

### Task 6: Write failing TinyGo LLVM test

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go`

The TinyGo LLVM tests directly call low-level functions. There's no `TestSPMDLLVM` runner — each test is its own `func Test...`. This test calls a new `spmdFieldAddrForVaryingPtr` helper function (which we'll extract from the FieldAddr handler).

- [ ] **Step 1: Add TestSPMDFieldAddrVaryingPtr to spmd_llvm_test.go**

```go
// TestSPMDFieldAddrVaryingPtr verifies that FieldAddr with *Varying[Struct] X
// produces a <N x ptr> vector of per-lane field pointers via per-lane GEPs.
func TestSPMDFieldAddrVaryingPtr(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	// Simulate a struct {X, Y int64} = two i64 fields.
	i64 := c.ctx.Int64Type()
	structType := c.ctx.StructType([]llvm.Type{i64, i64}, false)

	// Build a <4 x ptr> simulating what IndexAddr produces for *Varying[Point].
	ptrType := llvm.PointerType(structType, 0)
	vecPtrType := llvm.VectorType(ptrType, laneCount)

	// Allocate 4 structs and build a <4 x ptr> pointing to each.
	ptrVec := llvm.Undef(vecPtrType)
	for i := 0; i < laneCount; i++ {
		alloca := b.CreateAlloca(structType, fmt.Sprintf("struct.%d", i))
		idx := llvm.ConstInt(c.ctx.Int32Type(), uint64(i), false)
		ptrVec = b.CreateInsertElement(ptrVec, alloca, idx, "")
	}

	// Call the new helper (to be implemented in compiler.go):
	fieldIndex := 0 // field X
	result := b.spmdFieldAddrForVaryingPtr(ptrVec, structType, fieldIndex, laneCount)

	// Verify result is a <4 x ptr> vector.
	if result.Type().TypeKind() != llvm.VectorTypeKind {
		t.Fatalf("result type kind = %v, want VectorTypeKind", result.Type().TypeKind())
	}
	if result.Type().VectorSize() != laneCount {
		t.Fatalf("result vector size = %d, want %d", result.Type().VectorSize(), laneCount)
	}
	// Verify result element type is pointer.
	if result.Type().ElementType().TypeKind() != llvm.PointerTypeKind {
		t.Fatalf("result element type = %v, want pointer", result.Type().ElementType().TypeKind())
	}
}
```

Note: requires `fmt` import. The new `spmdFieldAddrForVaryingPtr` function will be extracted from the FieldAddr handler for testability.

- [ ] **Step 2: Run to confirm it fails (function doesn't exist yet)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go test ./tinygo/compiler/ -run TestSPMDFieldAddrVaryingPtr -v 2>&1 | tail -10
```

Expected: compile error `undefined: spmdFieldAddrForVaryingPtr`.

- [ ] **Step 3: Commit failing test**

```bash
git add tinygo/compiler/spmd_llvm_test.go
git commit -m "test: add TestSPMDFieldAddrVaryingPtr (failing, pre-implementation)"
```

---

### Task 7: Implement FieldAddr fix in TinyGo

**Files:**
- Modify: `tinygo/compiler/compiler.go`
- Modify: `tinygo/compiler/spmd.go` (add `spmdFieldAddrForVaryingPtr` helper)

- [ ] **Step 1: Add spmdFieldAddrForVaryingPtr helper to spmd.go**

```go
// spmdFieldAddrForVaryingPtr computes per-lane field addresses when the
// receiver is a *Varying[Struct] (i.e., ptrVec is a <N x ptr> vector, each
// lane pointing to a different struct instance).
//
// Returns a <N x ptr> vector where each element is a pointer to field
// fieldIndex within the corresponding lane's struct.
func (b *builder) spmdFieldAddrForVaryingPtr(ptrVec llvm.Value, structType llvm.Type, fieldIndex, laneCount int) llvm.Value {
	result := llvm.Undef(ptrVec.Type())
	for i := 0; i < laneCount; i++ {
		idx := llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
		lanePtr := b.CreateExtractElement(ptrVec, idx, "lane.ptr")
		fieldPtr := b.CreateInBoundsGEP(structType, lanePtr, []llvm.Value{
			llvm.ConstInt(b.ctx.Int32Type(), 0, false),
			llvm.ConstInt(b.ctx.Int32Type(), uint64(fieldIndex), false),
		}, "fieldaddr.lane")
		result = b.CreateInsertElement(result, fieldPtr, idx, "")
	}
	return result
}
```

- [ ] **Step 2: Integrate into FieldAddr handler in compiler.go**

In `compiler.go` at line 2709, add SPMD detection BEFORE `createNilCheck`:

```go
case *ssa.FieldAddr:
	val := b.getValue(expr.X, getPos(expr))

	// SPMD: *Varying[Struct] field access.
	// val is a <N x ptr> vector from IndexAddr with varying index.
	// Produce a new <N x ptr> vector with per-lane field GEPs.
	if buildcfg.Experiment.SPMD {
		if ptr, ok := expr.X.Type().(*types.Pointer); ok {
			if spmdType, ok := ptr.Elem().(*types.SPMDType); ok {
				structLLVMType := b.getLLVMType(spmdType.Elem())
				laneCount := val.Type().VectorSize()
				return b.spmdFieldAddrForVaryingPtr(val, structLLVMType, expr.Field, laneCount), nil
			}
		}
	}

	// Uniform pointer field access (original code):
	b.createNilCheck(expr.X, val, "gep")
	indices := []llvm.Value{
		llvm.ConstInt(b.ctx.Int32Type(), 0, false),
		llvm.ConstInt(b.ctx.Int32Type(), uint64(expr.Field), false),
	}
	elementType := b.getLLVMType(expr.X.Type().Underlying().(*types.Pointer).Elem())
	return b.CreateInBoundsGEP(elementType, val, indices, ""), nil
```

- [ ] **Step 3: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -5
```

Expected: successful build.

- [ ] **Step 4: Run the failing test — verify PASS**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go test ./tinygo/compiler/ -run TestSPMDFieldAddrVaryingPtr -v 2>&1 | tail -5
```

Expected: PASS.

- [ ] **Step 5: Run all TinyGo SPMD tests**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd go test ./tinygo/compiler/ -run TestSPMD -v 2>&1 | grep -E "--- PASS|--- FAIL" | head -40
```

Expected: no regressions.

- [ ] **Step 6: Commit**

```bash
git add tinygo/compiler/compiler.go tinygo/compiler/spmd.go
git commit -m "fix: FieldAddr for *Varying[Struct] emits per-lane GEPs in TinyGo"
```

---

## Chunk 4: E2E Validation and Example Fix

### Task 8: Validate structPointers compiles and runs

- [ ] **Step 1: Try compiling structPointers in isolation (temp file — delete after)**

Create a minimal test file `examples/pointer-varying/struct_only_main.go`:

```go
// run -goexperiment spmd -target=wasi
package main
import (
	"fmt"
	"lanes"
)
type Point struct{ X, Y int }
func main() {
	points := [4]Point{{1, 2}, {3, 4}, {5, 6}, {7, 8}}
	go for i := range 4 {
		pointPtr := &points[i]
		pointPtr.X += lanes.Index()
		pointPtr.Y += lanes.Index() * 2
	}
	fmt.Printf("Modified points: %+v\n", points)
}
```

- [ ] **Step 2: Compile and run**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o /tmp/struct-test.wasm examples/pointer-varying/struct_only_main.go 2>&1
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/struct-test.wasm 2>&1
```

Expected output: `Modified points: [{X:1 Y:2} {X:4 Y:8} {X:7 Y:14} {X:10 Y:20}]`
(X: 1+0, 3+1, 5+2, 7+3; Y: 2+0, 4+2, 6+4, 8+6)

- [ ] **Step 3: Delete the temp file**

```bash
rm examples/pointer-varying/struct_only_main.go
```

- [ ] **Step 4: Debug if compilation fails**

Common issues:
- `val.Type().VectorSize()` — `val` from `IndexAddr` may not be a vector if the IndexAddr SPMD path wasn't triggered. Check that `i` is recognized as varying in the `go for` context.
- `getLLVMType(spmdType.Elem())` fails for struct elem — verify `spmdType.Elem()` is `Point` (a `*types.Named`) and `getLLVMType` can handle it.

---

### Task 9: Fix and simplify the example program

**Files:**
- Modify: `examples/pointer-varying/main.go`

The current example has deferred-feature functions. Simplify to only include what works.

- [ ] **Step 1: Confirm E2E failure before the example fix**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o /tmp/pv.wasm examples/pointer-varying/main.go 2>&1 | grep -c "^examples" || true
```

Expected: still several errors on lines with deferred features (array indexing, unused var, type mismatch). This step confirms we know what's left to fix at the example level.

- [ ] **Step 2: Rewrite main.go**

Keep:
- `scatterGatherOperations` (works already)
- `pointerArithmetic` (works already)
- `indirectAccess` (works already)
- `structPointers` (now works after fix — keep as-is)

Replace `processVaryingArray` with a simpler function that doesn't require Varying[array] indexing:
```go
// processVaryingData demonstrates varying pointers to uniform data elements.
func processVaryingData() {
	fmt.Println("\n=== Varying Pointer to Uniform Data ===")
	data := [8]int{1, 2, 3, 4, 5, 6, 7, 8}
	go for i := range 8 {
		ptr := &data[i] // varying pointer: each lane points to data[i]
		*ptr *= 2
		*ptr += lanes.Index()
	}
	fmt.Printf("Processed data: %v\n", data)
}
```

Remove `mixedPointerOperations` (requires Varying[array] indexing — deferred).
Remove `demonstrateAddressOperations` (requires `&varyingVar` — semantically complex, deferred).
Fix `main()` to call only the kept functions.

- [ ] **Step 2: Compile the updated example**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o /tmp/pointer-varying.wasm examples/pointer-varying/main.go 2>&1
```

Expected: no compile errors.

- [ ] **Step 3: Run the example**

```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/pointer-varying.wasm 2>&1
```

Expected: all sections print correct output without errors.

- [ ] **Step 4: Commit**

```bash
git add examples/pointer-varying/main.go
git commit -m "fix: simplify pointer-varying example, enable structPointers with field access fix"
```

---

### Task 10: E2E test suite and plan update

- [ ] **Step 1: Run the full E2E test suite**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```

Expected: `pointer-varying` moves from compile-fail to run-pass. Overall run-pass count increases by 1.

- [ ] **Step 2: Add deferred items to PLAN.md**

In PLAN.md's "Deferred Items Collection" section, add:
```
- Task: Varying[array] indexing ((*Varying[array])[varyingIndex] → Varying[elem])
  Location: go/types/index.go:74-82 (hard rejection), x-tools-spmd SSA, TinyGo
  Status: NOT DONE
  Depends On: pointer-varying selector fix (now done)
  Implementation: Allow Varying[T] indexing when T is array, produce Varying[elem]
  Priority: Low
  Related: pointer-varying example lines 24, 25, 117, 120

- Task: &varyingVar address-of (Varying[*T] from taking address of varying variable)
  Location: go/types/pointer_ext_spmd.go:validateSPMDAddressOperation
  Status: NOT DONE
  Depends On: semantic design (per-lane addresses of register-held values)
  Implementation: Allow &varyingVar only for alloca-backed varying, produce Varying[*T]
  Priority: Low
  Related: pointer-varying example demonstrateAddressOperations
```

- [ ] **Step 3: Update E2E results counts in PLAN.md and MEMORY.md**

- [ ] **Step 4: Commit**

```bash
git add PLAN.md
git commit -m "docs: update PLAN.md with pointer-varying results and deferred items"
```

---

## Testing Checklist

| Layer | Test | Command | Expected |
|-------|------|---------|----------|
| go/types | TestSPMDTypeChecking | `go test go/types -run TestSPMDTypeChecking` | PASS |
| types2 | All types2 tests | `go test cmd/compile/internal/types2` | PASS |
| x-tools-spmd typeparams | typeparams package | `go test ./internal/typeparams/...` | PASS |
| x-tools-spmd SSA | TestSPMDFieldAddrVaryingPtrType | `go test ./go/ssa/... -run TestSPMDFieldAddrVaryingPtrType` | PASS |
| TinyGo unit | TestSPMDFieldAddrVaryingPtr | `go test ./tinygo/compiler/ -run TestSPMDFieldAddrVaryingPtr` | PASS |
| TinyGo regression | All SPMD tests | `go test ./tinygo/compiler/ -run TestSPMD` | No regressions |
| E2E | spmd-e2e-test.sh | `bash test/e2e/spmd-e2e-test.sh` | pointer-varying promoted |
