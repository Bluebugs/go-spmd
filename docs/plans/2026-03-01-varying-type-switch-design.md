# Plan: Varying Type Switch — Box/Unbox `lanes.Varying[T]` via `interface{}` with Mask

## Context

`lanes.Varying[T]` values cannot currently be boxed into `interface{}` and recovered via type switch (`case lanes.Varying[int]:`) or type assertion (`x.(lanes.Varying[int])`). This is blocked by:

1. **Type checker**: `assertableTo()` calls `hasAllMethods()` which fails because `SPMDType` has no method set (even though `interface{}` requires zero methods — the method lookup on SPMDType hits an edge case).
2. **TinyGo backend**: `createTypeAssert()` has no SPMDType handling. `createMakeInterface()` doesn't capture the execution mask.

**Goal**: Support Scenario 1 only — a uniform `interface{}` holding a boxed varying value + mask. When boxing, store the current SPMD execution mask alongside the vector value. When unboxing via type switch/assertion, restore the mask as the active execution mask.

**Scope**: NOT implementing `lanes.Varying[any]` (per-lane interface discrimination).

## Design

### Representation: Struct-Wrapped Boxing

When boxing `lanes.Varying[T]` into `interface{}`, store a **struct** `{value [N]T, mask [N]int32}` instead of just `[N]T`:

- `getTypeCode(SPMDType)` → `getTypeCode(struct{v [N]T; m [N]int32})` — unique type code
- `createMakeInterface` → packs `(vectorValue, maskValue)` via `emitPointerPack`
- `createTypeAssert` → unpacks struct, extracts value + mask, pushes mask

The `emitPointerPack/Unpack` functions already handle multi-value structs (heap-allocated when >pointer size). No changes needed there.

### Mask Push/Pop Strategy

On successful type assertion to `SPMDType`:
1. Extract mask from the struct
2. AND the extracted mask with the current mask (if any) to respect nesting
3. Push the result onto `spmdMaskStack`
4. The caller (type switch case body or subsequent code) operates under this mask

**Pop**: The mask remains on the stack until the enclosing scope pops it. In practice:
- If inside a varying-if, the existing pop mechanism handles it
- If at function level, the mask persists for the function duration (correct: the boxed mask represents which lanes are valid)
- For type switches (desugared to If chains by go/ssa), the mask is naturally scoped to the "ok" branch

**Key insight**: Unlike varying-if where we push/pop around blocks, here the mask is *data* that enters the execution context. It's more like `spmdEntryMask` — set once when unboxing, active until overridden.

## Implementation Steps

### Step 1: Fix `assertableTo` in go/types and types2

**Files**:
- `go/src/go/types/lookup.go:589` — `assertableTo()`
- `go/src/go/types/lookup.go:605` — `newAssertableTo()`
- `go/src/cmd/compile/internal/types2/lookup.go` — same two functions (mirror)

**Change**: Before falling through to `hasAllMethods`, unwrap SPMDType:

```go
func (check *Checker) assertableTo(V, T Type, cause *string) bool {
    if IsInterface(T) {
        return true
    }
    // SPMD: SPMDType has no methods of its own. Check assertability
    // against the element type. Varying[int] is assertable from
    // interface{} just like int is.
    if spmd, ok := T.(*SPMDType); ok && buildcfg.Experiment.SPMD {
        return check.assertableTo(V, spmd.Elem(), cause)
    }
    return check.hasAllMethods(T, V, false, Identical, cause)
}
```

Same pattern for `newAssertableTo` (delegates to `implements` instead of `hasAllMethods`).

**Why this works**: For `assertableTo(interface{}, Varying[int])`:
- Unwrap to `int` → `hasAllMethods(int, interface{})` → interface{} has zero methods → passes.
For `assertableTo(Stringer, Varying[int])`:
- Unwrap to `int` → `hasAllMethods(int, Stringer)` → int doesn't have `String()` → correctly fails.

### Step 2: Add `vectorToArray` helper in TinyGo

**File**: `tinygo/compiler/spmd.go`

Inverse of existing `arrayToVector` (spmd.go:337). Extract each element from a vector and insert into an array:

```go
func (b *builder) vectorToArray(vec llvm.Value) llvm.Value {
    n := vec.Type().VectorSize()
    elemType := vec.Type().ElementType()
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

### Step 3: Add `spmdBoxedStructType` helper

**File**: `tinygo/compiler/spmd.go`

Computes the Go `*types.Struct` used for both boxing and type code matching:

```go
func (c *compilerContext) spmdBoxedStructType(spmdType *types.SPMDType) *types.Struct {
    elemLLVM := c.getLLVMType(spmdType.Elem())
    laneCount := c.spmdEffectiveLaneCount(spmdType, elemLLVM)
    valueArray := types.NewArray(spmdType.Elem(), int64(laneCount))
    maskArray := types.NewArray(types.Typ[types.Int32], int64(laneCount))
    return types.NewStruct([]*types.Var{
        types.NewField(token.NoPos, nil, "v", valueArray, false),
        types.NewField(token.NoPos, nil, "m", maskArray, false),
    }, nil)
}
```

### Step 4: Add `spmdCurrentMaskOrAllOnes` helper

**File**: `tinygo/compiler/spmd.go`

Returns the current active mask (from stack, loop, entry, or all-ones fallback) without requiring a target function's signature:

```go
func (b *builder) spmdCurrentMaskOrAllOnes(laneCount int) llvm.Value {
    if mask := b.spmdCurrentMask(); !mask.IsNil() {
        return mask
    }
    if b.spmdLoopState != nil {
        for _, loop := range b.spmdLoopState.activeLoops {
            if !loop.tailMask.IsNil() {
                return loop.tailMask
            }
        }
    }
    if !b.spmdEntryMask.IsNil() {
        return b.spmdEntryMask
    }
    maskType := llvm.VectorType(b.spmdMaskElemType(laneCount), laneCount)
    return llvm.ConstAllOnes(maskType)
}
```

### Step 5: Update `getTypeCode` for SPMDType — struct boxing

**File**: `tinygo/compiler/interface.go:128-136`

Change existing array mapping to struct mapping:

```go
// Before (current):
if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
    elemLLVM := c.getLLVMType(spmdType.Elem())
    laneCount := c.spmdEffectiveLaneCount(spmdType, elemLLVM)
    arrayType := types.NewArray(spmdType.Elem(), int64(laneCount))
    return c.getTypeCode(arrayType)
}

// After:
if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
    return c.getTypeCode(c.spmdBoxedStructType(spmdType))
}
```

### Step 6: Update `createMakeInterface` for SPMDType boxing

**File**: `tinygo/compiler/compiler.go:3920-3922` (the `*ssa.MakeInterface` case)

Intercept before calling `createMakeInterface` when the value type is SPMDType:

```go
case *ssa.MakeInterface:
    val := b.getValue(expr.X, getPos(expr))
    if spmdType, ok := expr.X.Type().(*types.SPMDType); ok && spmdType.IsVarying() {
        return b.createMakeInterfaceSPMD(val, spmdType, expr.Pos()), nil
    }
    return b.createMakeInterface(val, expr.X.Type(), expr.Pos()), nil
```

**New function in `tinygo/compiler/spmd.go`**:

```go
func (b *builder) createMakeInterfaceSPMD(val llvm.Value, spmdType *types.SPMDType, pos token.Pos) llvm.Value {
    elemLLVM := b.getLLVMType(spmdType.Elem())
    laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)

    // Convert vector to array for storage
    valArray := b.vectorToArray(val)

    // Get current execution mask, normalize to i32, convert to array
    mask := b.spmdCurrentMaskOrAllOnes(laneCount)
    if !b.spmdIsWASM() {
        // Non-WASM: mask is <N x i1>, sign-extend to <N x i32> for storage
        mask = b.CreateSExt(mask, llvm.VectorType(b.ctx.Int32Type(), laneCount), "")
    }
    maskArray := b.vectorToArray(mask)

    // Pack {value_array, mask_array} into interface
    itfValue := b.emitPointerPack([]llvm.Value{valArray, maskArray})
    itfType := b.getTypeCode(spmdType)  // → struct type code via Step 5

    itf := llvm.Undef(b.getLLVMRuntimeType("_interface"))
    itf = b.CreateInsertValue(itf, itfType, 0, "")
    itf = b.CreateInsertValue(itf, itfValue, 1, "")
    return itf
}
```

### Step 7: Update `createTypeAssert` for SPMDType unboxing

**File**: `tinygo/compiler/interface.go:702-790`

Add SPMDType detection at the start of `createTypeAssert`. When the asserted type is SPMDType, use the struct type code for comparison and extract both value and mask:

```go
func (b *builder) createTypeAssert(expr *ssa.TypeAssert) llvm.Value {
    itf := b.getValue(expr.X, getPos(expr))

    // SPMD: type assert to lanes.Varying[T] — use struct type code
    if spmdType, ok := expr.AssertedType.(*types.SPMDType); ok && spmdType.IsVarying() {
        return b.createTypeAssertSPMD(itf, expr, spmdType)
    }

    // ... existing code unchanged ...
}
```

**New function in `tinygo/compiler/spmd.go`**:

```go
func (b *builder) createTypeAssertSPMD(itf llvm.Value, expr *ssa.TypeAssert, spmdType *types.SPMDType) llvm.Value {
    // The boxed type code is for struct{[N]T, [N]int32}
    structGoType := b.spmdBoxedStructType(spmdType)
    name, _ := getTypeCodeName(structGoType)
    globalName := "reflect/types.typeid:" + name
    assertedTypeCodeGlobal := b.mod.NamedGlobal(globalName)
    if assertedTypeCodeGlobal.IsNil() {
        assertedTypeCodeGlobal = llvm.AddGlobal(b.mod, b.ctx.Int8Type(), globalName)
        assertedTypeCodeGlobal.SetGlobalConstant(true)
    }

    actualTypeNum := b.CreateExtractValue(itf, 0, "interface.type")
    commaOk := b.createRuntimeCall("typeAssert", []llvm.Value{actualTypeNum, assertedTypeCodeGlobal}, "typecode")

    // Branch on type match
    prevBlock := b.GetInsertBlock()
    okBlock := b.insertBasicBlock("typeassert.spmd.ok")
    nextBlock := b.insertBasicBlock("typeassert.spmd.next")
    b.currentBlockInfo.exit = nextBlock
    b.CreateCondBr(commaOk, okBlock, nextBlock)

    // OK block: extract value + mask from struct
    b.SetInsertPointAtEnd(okBlock)

    elemLLVM := b.getLLVMType(spmdType.Elem())
    laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)
    i32Type := b.ctx.Int32Type()
    arrType := llvm.ArrayType(elemLLVM, laneCount)
    maskArrType := llvm.ArrayType(i32Type, laneCount)

    // Extract struct fields (value array + mask array)
    unpacked := b.emitPointerUnpack(
        b.CreateExtractValue(itf, 1, "typeassert.value.ptr"),
        []llvm.Type{arrType, maskArrType},
    )
    valArray := unpacked[0]
    maskArray := unpacked[1]

    // Convert arrays to vectors
    vecType := b.getLLVMType(spmdType)  // <N x T>
    maskI32VecType := llvm.VectorType(i32Type, laneCount)
    valueOk := b.arrayToVector(valArray, vecType)
    maskVec := b.arrayToVector(maskArray, maskI32VecType)

    // Restore platform mask format
    if !b.spmdIsWASM() {
        // Non-WASM: truncate <N x i32> back to <N x i1>
        maskVec = b.CreateTrunc(maskVec, llvm.VectorType(b.ctx.Int1Type(), laneCount), "")
    }

    // Push the extracted mask onto the mask stack (AND with current if any)
    if currentMask := b.spmdCurrentMask(); !currentMask.IsNil() {
        maskVec = b.CreateAnd(maskVec, currentMask, "spmd.mask.and")
    }
    b.spmdPushMask(maskVec)

    b.CreateBr(nextBlock)

    // Merge block: phi for the value
    b.SetInsertPointAtEnd(nextBlock)
    phi := b.CreatePHI(vecType, "typeassert.spmd.value")
    phi.AddIncoming(
        []llvm.Value{llvm.ConstNull(vecType), valueOk},
        []llvm.BasicBlock{prevBlock, okBlock},
    )

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

**Mask scoping note**: The mask is pushed when the type assert succeeds. For go/ssa's desugared type switches (chains of TypeAssert + If), the mask persists through the case body blocks. It will be naturally overridden by subsequent type asserts or varying-if pushes. If this proves too broad, we can add explicit pop points in the If handler, but for the initial implementation this is correct — the mask represents which lanes contain valid data.

### Step 8: Handle mask array element type consistency

**File**: `tinygo/compiler/spmd.go`

The mask is `<N x i32>` on WASM but `<N x i1>` elsewhere. We always store as `[N]int32` (Go-level type for the struct). This means:
- On WASM: mask is `<4 x i32>`, stored as `[4]int32` — direct match
- On non-WASM: mask is `<4 x i1>`, but struct field is `[4]int32` — need sign-extend before storage, truncate on load

This is handled inline in Steps 6 and 7:

In `createMakeInterfaceSPMD`, before `vectorToArray(mask)`:
```go
// Normalize mask to i32 elements for consistent storage
if !b.spmdIsWASM() {
    mask = b.CreateSExt(mask, llvm.VectorType(b.ctx.Int32Type(), laneCount), "")
}
```

In `createTypeAssertSPMD`, after `arrayToVector(maskArray, ...)`:
```go
// Restore platform mask format
if !b.spmdIsWASM() {
    maskVec = b.CreateTrunc(maskVec, llvm.VectorType(b.ctx.Int1Type(), laneCount), "")
}
```

### Step 9: Write integration test

**File**: `test/integration/spmd/type-switch-varying/main.go`

Simplify the existing test to only cover Scenario 1 (uniform interface{}):

```go
// run -goexperiment spmd -target=wasi
package main

import (
    "fmt"
    "lanes"
)

func processMixed(value interface{}) {
    switch v := value.(type) {
    case lanes.Varying[int]:
        result := v * 2
        fmt.Printf("Varying int: %v\n", result)
    case int:
        fmt.Printf("Uniform int: %d\n", v)
    default:
        fmt.Printf("Other: %T\n", v)
    }
}

func testTypeAssertion() {
    var x interface{} = lanes.Varying[int](42)
    if v, ok := x.(lanes.Varying[int]); ok {
        fmt.Printf("Assert ok: %v\n", v+10)
    }
    if _, ok := x.(lanes.Varying[string]); !ok {
        fmt.Println("Assert fail as expected")
    }
}

func main() {
    processMixed(lanes.Varying[int](42))
    processMixed(100)
    testTypeAssertion()
}
```

Remove `Varying[any]` cases and `go for` with type switch (Scenario 2).

### Step 10: Add TinyGo LLVM unit tests

**File**: `tinygo/compiler/spmd_llvm_test.go`

Add tests for:
1. `vectorToArray` (inverse of existing `arrayToVector` test)
2. `spmdBoxedStructType` returns correct Go struct
3. `createMakeInterfaceSPMD` produces correct LLVM IR
4. `createTypeAssertSPMD` produces correct LLVM IR with mask extraction

## Files Modified (Summary)

| File | Change | Lines |
|------|--------|-------|
| `go/src/go/types/lookup.go` | Unwrap SPMDType in assertableTo + newAssertableTo | ~8 |
| `go/src/cmd/compile/internal/types2/lookup.go` | Mirror of above | ~8 |
| `tinygo/compiler/spmd.go` | Add vectorToArray, spmdBoxedStructType, spmdCurrentMaskOrAllOnes, createMakeInterfaceSPMD, createTypeAssertSPMD | ~100 |
| `tinygo/compiler/interface.go` | Update getTypeCode SPMDType mapping, add SPMDType guard in createTypeAssert | ~8 |
| `tinygo/compiler/compiler.go` | Intercept MakeInterface for SPMDType | ~5 |
| `tinygo/compiler/spmd_llvm_test.go` | Unit tests for new helpers | ~50 |
| `test/integration/spmd/type-switch-varying/main.go` | Simplify to Scenario 1 only | rewrite |

**Total**: ~180 lines of new code + test simplification

## Verification

1. **Build Go toolchain**: `make build-go` — verify assertableTo changes compile
2. **Build TinyGo**: `make build-tinygo` — verify backend changes compile
3. **Run Go type checker tests**: `cd go && GOEXPERIMENT=spmd bin/go test go/types/...`
4. **Run TinyGo SPMD tests**: `cd tinygo && go test ./compiler/ -run TestSPMD`
5. **Compile integration test**: `make compile EXAMPLE=type-switch-varying`
6. **Run integration test**: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs output.wasm`
7. **Run full E2E suite**: `bash test/e2e/spmd-e2e-test.sh` — verify no regressions
