# SPMDType Interface Boxing/Unboxing Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable `lanes.Varying[T]` values to be boxed into `interface{}` and recovered via type assertion/switch.

**Architecture:** Box varying vectors as `[N]T` arrays in interface values (type code already does this). Fix three layers: (1) go/types assertableTo to allow type assertions on SPMDType, (2) TinyGo MakeInterface to convert vector→array before packing, (3) TinyGo TypeAssert to convert array→vector after unpacking. No mask storage needed — SSA-level predication already bakes mask info into values via SPMDSelect.

**Tech Stack:** Go compiler (go/types, types2), TinyGo compiler (interface.go, spmd.go), LLVM IR generation

**Key insight from mask stack removal (2026-03-05):** The original design stored `{value [N]T, mask [N]int32}` in the interface. This is unnecessary now — SSA-level predication ensures inactive lanes are zeroed by SPMDSelect before boxing. We store just `[N]T`, matching the existing `getTypeCode` mapping.

---

### Task 1: Fix `assertableTo` in go/types

SPMDType has no methods, so `hasAllMethods(SPMDType, interface{})` fails even though `interface{}` requires zero methods. Unwrap SPMDType to its element type before the method check.

**Files:**
- Modify: `go/src/go/types/lookup.go:589-598`

**Step 1: Write the fix**

In `assertableTo`, add SPMDType unwrap before `hasAllMethods`:

```go
func (check *Checker) assertableTo(V, T Type, cause *string) bool {
	if IsInterface(T) {
		return true
	}
	// SPMD: SPMDType has no methods of its own. Unwrap to element type
	// so hasAllMethods checks the element (e.g., int) against V.
	// Varying[int] is assertable from interface{} just like int is.
	if spmd, ok := T.(*SPMDType); ok && buildcfg.Experiment.SPMD {
		return check.assertableTo(V, spmd.Elem(), cause)
	}
	return check.hasAllMethods(T, V, false, Identical, cause)
}
```

Apply the same pattern to `newAssertableTo` at line 605:

```go
func (check *Checker) newAssertableTo(V, T Type, cause *string) bool {
	if IsInterface(T) {
		return true
	}
	// SPMD: unwrap SPMDType to element type for method check.
	if spmd, ok := T.(*SPMDType); ok && buildcfg.Experiment.SPMD {
		return check.newAssertableTo(V, spmd.Elem(), cause)
	}
	return check.implements(T, V, false, cause)
}
```

**Step 2: Verify build**

Run: `cd go && GOEXPERIMENT=spmd bin/go build go/types`
Expected: compiles without errors

**Step 3: Commit**

```
feat: unwrap SPMDType in assertableTo for interface type assertions
```

---

### Task 2: Mirror `assertableTo` fix in types2

**Files:**
- Modify: `go/src/cmd/compile/internal/types2/lookup.go:588-611`

**Step 1: Write the fix**

Identical logic as Task 1, in the types2 package:

```go
func (check *Checker) assertableTo(V, T Type, cause *string) bool {
	if IsInterface(T) {
		return true
	}
	// SPMD: SPMDType has no methods of its own. Unwrap to element type.
	if spmd, ok := T.(*SPMDType); ok && buildcfg.Experiment.SPMD {
		return check.assertableTo(V, spmd.Elem(), cause)
	}
	return check.hasAllMethods(T, V, false, Identical, cause)
}

func (check *Checker) newAssertableTo(V, T Type, cause *string) bool {
	if IsInterface(T) {
		return true
	}
	// SPMD: unwrap SPMDType to element type for method check.
	if spmd, ok := T.(*SPMDType); ok && buildcfg.Experiment.SPMD {
		return check.newAssertableTo(V, spmd.Elem(), cause)
	}
	return check.implements(T, V, false, cause)
}
```

**Step 2: Verify build**

Run: `cd go && GOEXPERIMENT=spmd bin/go build cmd/compile/internal/types2`
Expected: compiles without errors

**Step 3: Build full Go toolchain**

Run: `make build-go`
Expected: success

**Step 4: Commit**

```
feat: mirror SPMDType assertableTo fix in types2
```

---

### Task 3: Add `vectorToArray` helper in TinyGo

Inverse of existing `arrayToVector` (spmd.go:349-358). Extracts each element from a vector and inserts into an array.

**Files:**
- Modify: `tinygo/compiler/spmd.go` (insert after `arrayToVector` at line 358)
- Modify: `tinygo/compiler/spmd_llvm_test.go` (add test)

**Step 1: Write the test**

Add to `spmd_llvm_test.go`:

```go
func TestVectorToArray(t *testing.T) {
	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("test_vectorToArray")
	defer mod.Dispose()
	builder := ctx.NewBuilder()
	defer builder.Dispose()

	i32Type := ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, 4)
	arrType := llvm.ArrayType(i32Type, 4)

	// Create test function
	fnType := llvm.FunctionType(arrType, []llvm.Type{vecType}, false)
	fn := llvm.AddFunction(mod, "test_v2a", fnType)
	bb := ctx.AddBasicBlock(fn, "entry")
	builder.SetInsertPointAtEnd(bb)

	c := &compilerContext{ctx: ctx, mod: mod}
	b := &builder{compilerContext: c}
	b.Builder = builder

	vec := fn.Param(0)
	arr := b.vectorToArray(vec)

	builder.CreateRet(arr)

	ir := mod.String()
	// Should extract 4 elements and insert into array
	if !strings.Contains(ir, "extractelement") {
		t.Error("IR should contain extractelement instructions")
	}
	if !strings.Contains(ir, "insertvalue") {
		t.Error("IR should contain insertvalue instructions")
	}
	if err := llvm.VerifyModule(mod, llvm.ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test ./compiler/ -run TestVectorToArray -v`
Expected: FAIL — `vectorToArray` not defined

**Step 3: Write the implementation**

Add after `arrayToVector` (spmd.go:358):

```go
// vectorToArray converts an LLVM <N x T> vector value to an [N x T] array
// by extracting each element and inserting it into an array. Inverse of arrayToVector.
func (b *builder) vectorToArray(vec llvm.Value) llvm.Value {
	vecType := vec.Type()
	n := vecType.VectorSize()
	elemType := vecType.ElementType()
	arrType := llvm.ArrayType(elemType, n)
	arr := llvm.Undef(arrType)
	for i := 0; i < n; i++ {
		idx := llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
		elem := b.CreateExtractElement(vec, idx, "")
		arr = b.CreateInsertValue(arr, elem, i, "")
	}
	return arr
}
```

**Step 4: Run test to verify it passes**

Run: `cd tinygo && go test ./compiler/ -run TestVectorToArray -v`
Expected: PASS

**Step 5: Commit**

```
feat: add vectorToArray helper (inverse of arrayToVector)
```

---

### Task 4: Update `createMakeInterface` for SPMDType boxing

When boxing `lanes.Varying[T]` into `interface{}`, convert the LLVM vector `<N x T>` to array `[N x T]` before packing. The type code already maps SPMDType to `[N]T` (interface.go:131-136), so the value representation must match.

**Files:**
- Modify: `tinygo/compiler/compiler.go:2949-2951` (the `*ssa.MakeInterface` case)

**Step 1: Write the fix**

Change the `*ssa.MakeInterface` case handler:

```go
case *ssa.MakeInterface:
	val := b.getValue(expr.X, getPos(expr))
	// SPMD: convert vector to array before boxing into interface.
	// getTypeCode maps SPMDType to [N]T array, so the packed value must be
	// an array too. Without this, emitPointerPack receives <N x T> vector
	// which doesn't match the [N]T type code.
	if _, ok := expr.X.Type().(*types.SPMDType); ok {
		val = b.vectorToArray(val)
	}
	return b.createMakeInterface(val, expr.X.Type(), expr.Pos()), nil
```

**Step 2: Verify build**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: compiles without errors

**Step 3: Compile a minimal test to check boxing works**

Create `/tmp/spmd-box-test/main.go`:
```go
package main

import "fmt"

func main() {
	data := []int{1, 2, 3, 4}
	go for _, v := range data {
		var x interface{} = v  // box varying int into interface{}
		fmt.Printf("boxed: %v\n", x)
	}
}
```

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go ./tinygo/build/tinygo build -target=wasi -o /tmp/spmd-box-test/output.wasm /tmp/spmd-box-test/main.go 2>&1`
Expected: May still fail at SPMDStore of struct (the interface struct stored under mask). That's the separate struct-store issue. But the MakeInterface itself should not crash.

**Step 4: Commit**

```
feat: convert vector to array in MakeInterface for SPMDType boxing
```

---

### Task 5: Update `createTypeAssert` for SPMDType unboxing

When type-asserting `interface{}` to `lanes.Varying[T]`, the unpacked value is an `[N]T` array (matching the type code). Convert it back to `<N x T>` vector using `arrayToVector`.

**Files:**
- Modify: `tinygo/compiler/interface.go:702-790` (`createTypeAssert`)

**Step 1: Write the fix**

Add SPMDType interception at the start of `createTypeAssert`, after the `itf` and `assertedType` assignments (line 703-704). The key: use the array type code for comparison but return a vector.

```go
func (b *builder) createTypeAssert(expr *ssa.TypeAssert) llvm.Value {
	itf := b.getValue(expr.X, getPos(expr))
	assertedType := b.getLLVMType(expr.AssertedType)

	// SPMD: type assertion to lanes.Varying[T].
	// The type code is [N]T (array), but the SSA result type is <N x T> (vector).
	// Compare against the array type code, then convert the extracted array to vector.
	if spmdType, ok := expr.AssertedType.(*types.SPMDType); ok && spmdType.IsVarying() {
		return b.createTypeAssertSPMD(itf, expr, spmdType, assertedType)
	}

	actualTypeNum := b.CreateExtractValue(itf, 0, "interface.type")
	// ... rest unchanged ...
```

Add `createTypeAssertSPMD` in `tinygo/compiler/spmd.go`:

```go
// createTypeAssertSPMD handles type assertions to lanes.Varying[T].
// The boxed representation uses [N]T array (matching getTypeCode), but the
// SSA result type is <N x T> vector. This function compares against the array
// type code, extracts the array value, and converts it to a vector.
func (b *builder) createTypeAssertSPMD(itf llvm.Value, expr *ssa.TypeAssert, spmdType *types.SPMDType, vecType llvm.Type) llvm.Value {
	// Build the array type that matches the type code.
	elemLLVM := b.getLLVMType(spmdType.Elem())
	laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)
	arrGoType := types.NewArray(spmdType.Elem(), int64(laneCount))
	arrLLVMType := b.getLLVMType(arrGoType)

	// Compare type codes (same as concrete type assert in createTypeAssert).
	actualTypeNum := b.CreateExtractValue(itf, 0, "interface.type")
	name, _ := getTypeCodeName(arrGoType)
	globalName := "reflect/types.typeid:" + name
	assertedTypeCodeGlobal := b.mod.NamedGlobal(globalName)
	if assertedTypeCodeGlobal.IsNil() {
		assertedTypeCodeGlobal = llvm.AddGlobal(b.mod, b.ctx.Int8Type(), globalName)
		assertedTypeCodeGlobal.SetGlobalConstant(true)
	}
	commaOk := b.createRuntimeCall("typeAssert", []llvm.Value{actualTypeNum, assertedTypeCodeGlobal}, "typecode")

	// Branch on type match.
	prevBlock := b.GetInsertBlock()
	okBlock := b.insertBasicBlock("typeassert.spmd.ok")
	nextBlock := b.insertBasicBlock("typeassert.spmd.next")
	b.currentBlockInfo.exit = nextBlock
	b.CreateCondBr(commaOk, okBlock, nextBlock)

	// OK block: extract array value, convert to vector.
	b.SetInsertPointAtEnd(okBlock)
	arrValue := b.extractValueFromInterface(itf, arrLLVMType)
	valueOk := b.arrayToVector(arrValue, vecType)
	b.CreateBr(nextBlock)

	// Merge block.
	b.SetInsertPointAtEnd(nextBlock)
	phi := b.CreatePHI(vecType, "typeassert.spmd.value")
	phi.AddIncoming([]llvm.Value{llvm.ConstNull(vecType), valueOk}, []llvm.BasicBlock{prevBlock, okBlock})

	if expr.CommaOk {
		tuple := b.ctx.ConstStruct([]llvm.Value{llvm.Undef(vecType), llvm.Undef(b.ctx.Int1Type())}, false)
		tuple = b.CreateInsertValue(tuple, phi, 0, "")
		tuple = b.CreateInsertValue(tuple, commaOk, 1, "")
		return tuple
	}
	b.createRuntimeCall("interfaceTypeAssert", []llvm.Value{commaOk}, "")
	return phi
}
```

**Step 2: Verify build**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: compiles without errors

**Step 3: Commit**

```
feat: add SPMDType unboxing in createTypeAssert with array-to-vector conversion
```

---

### Task 6: Rewrite integration test for Scenario 1

The existing `type-switch-varying/main.go` uses `Varying[any]` (Scenario 2, out of scope) and `Varying[[4]int]` (varying index, unsupported). Rewrite to test Scenario 1: uniform `interface{}` holding boxed varying values.

**Files:**
- Modify: `test/integration/spmd/type-switch-varying/main.go`

**Step 1: Rewrite the test**

```go
// run -goexperiment spmd -target=wasi

// Example demonstrating type assertions with varying values boxed in interface{}
// Scenario 1: uniform interface{} holds a boxed lanes.Varying[T] value
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// processMixed demonstrates type switch on interface{} with varying cases
func processMixed(value interface{}) {
	switch v := value.(type) {
	case lanes.Varying[int]:
		result := v * 2
		fmt.Printf("Varying int: sum=%d\n", reduce.Add(result))
	case int:
		fmt.Printf("Uniform int: %d\n", v)
	default:
		fmt.Printf("Other: %T\n", v)
	}
}

// testCommaOk demonstrates comma-ok type assertion
func testCommaOk() {
	fmt.Println("\n=== Comma-Ok Type Assertion ===")

	var x interface{} = lanes.Varying[int](42)

	if v, ok := x.(lanes.Varying[int]); ok {
		sum := reduce.Add(v + 10)
		fmt.Printf("Assert ok: sum=%d\n", sum)
	}

	if _, ok := x.(lanes.Varying[float64]); !ok {
		fmt.Println("Assert fail as expected (not Varying[float64])")
	}

	if _, ok := x.(int); !ok {
		fmt.Println("Assert fail as expected (not int)")
	}
}

func main() {
	fmt.Println("=== Type Switch with Varying Types ===")

	// Test 1: Type switch with varying int
	processMixed(lanes.Varying[int](42))

	// Test 2: Type switch with uniform int
	processMixed(100)

	// Test 3: Comma-ok assertions
	testCommaOk()

	fmt.Println("\nAll type switch varying tests completed")
}
```

**Step 2: Compile and run**

Run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/type-switch-test.wasm test/integration/spmd/type-switch-varying/main.go 2>&1
```
Expected: compiles successfully

Then run:
```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/type-switch-test.wasm
```
Expected output:
```
=== Type Switch with Varying Types ===
Varying int: sum=336
Uniform int: 100

=== Comma-Ok Type Assertion ===
Assert ok: sum=208
Assert fail as expected (not Varying[float64])
Assert fail as expected (not int)

All type switch varying tests completed
```

**Step 3: Commit**

```
test: rewrite type-switch-varying for Scenario 1 (uniform interface{})
```

---

### Task 7: Run full E2E suite and verify no regressions

**Step 1: Run E2E**

Run: `bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -30`

Expected:
- `type-switch-varying` should move from COMPILE FAIL to RUN PASS (or at least COMPILE PASS)
- All other tests should maintain their current status
- No new failures

**Step 2: If type-switch-varying still fails**

The test may fail at SPMDStore of the `runtime._interface` struct (the separate struct-store issue). If so, verify the failure is in the struct store path, not in the boxing/unboxing logic. The boxing fix is still correct — the struct store issue is a separate bug to fix next.

Check the error message: if it says `llvm.masked.store` with a struct type, that's the struct-store issue (not this task's scope).

**Step 3: Commit any test adjustments**

If needed, adjust the test to avoid varying-condition code paths that trigger struct SPMDStore (e.g., remove `if` conditions inside `go for` that would trigger predication on interface values).

---

### Important Notes

**What this plan does NOT fix:**
- SPMDStore/SPMDLoad of struct types (e.g., `runtime._interface` stored under a varying mask). That's the "Approach A: conditional scalar store/load" fix discussed separately. It requires changes to `createSPMDStore`/`createSPMDLoad` in spmd.go.
- `Varying[any]` (per-lane interface discrimination, Scenario 2). Out of PoC scope.

**Dependency:** The type-switch-varying integration test may need the struct SPMDStore fix to fully pass if the test has any varying conditions around interface operations. Task 6 is designed to minimize this by keeping interface operations outside varying `if` blocks.

**Build commands reference:**
- Go toolchain: `make build-go`
- TinyGo: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go` (then clear cache: `rm -rf ~/.cache/tinygo`)
- Compile SPMD: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go ./tinygo/build/tinygo build -target=wasi -scheduler=none -o output.wasm input.go`
- Run WASM: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs output.wasm`
