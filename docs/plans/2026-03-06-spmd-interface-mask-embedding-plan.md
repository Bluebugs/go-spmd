# SPMDType Interface Mask Embedding Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Embed the per-block condition mask alongside the value when boxing `lanes.Varying[T]` into `interface{}`, and thread the extracted mask as the base mask for subsequent operations on unbox.

**Architecture:** Add optional `SPMDMask` field to `MakeInterface` (set by predication pass in varying-if/switch blocks). Add new `SPMDExtractMask` SSA instruction for unboxing. Change boxed representation from `[N]T` to `struct{[N]T, [N]int32}`. TinyGo packs/unpacks the struct and lowers `SPMDExtractMask` to extract the mask vector. The extracted mask is AND'd with the scope's active mask and used for all subsequent masked operations.

**Tech Stack:** x-tools-spmd (go/ssa), TinyGo compiler (compiler.go, interface.go, spmd.go), LLVM IR

**Mandatory workflow:** `golang-pro` for implementation, `code-reviewer` for review, `clean-commit` for commits.

---

### Task 1: Add `SPMDMask` field to `MakeInterface` in go/ssa

Add optional mask field to existing `MakeInterface` instruction, following the `IndexAddr.SPMDMask` / `CallCommon.SPMDMask` pattern.

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go:873-876` (struct definition)
- Modify: `x-tools-spmd/go/ssa/ssa.go:1969-1971` (Operands method)

**Step 1: Add fields to MakeInterface struct**

At `ssa.go:873-876`, change:

```go
type MakeInterface struct {
	register
	X         Value
	SPMDMask  Value // nil = no masking; set for varying values boxed in masked context
	SPMDLanes int   // lane count; 0 when SPMDMask is nil
}
```

**Step 2: Update Operands method**

At `ssa.go:1969-1971`, change:

```go
func (v *MakeInterface) Operands(rands []*Value) []*Value {
	rands = append(rands, &v.X)
	if v.SPMDMask != nil {
		rands = append(rands, &v.SPMDMask)
	}
	return rands
}
```

**Step 3: Verify build**

Run: `cd x-tools-spmd && go build ./go/ssa/...`
Expected: compiles without errors

**Step 4: Commit**

```
feat: add SPMDMask field to MakeInterface for mask embedding
```

---

### Task 2: Add `SPMDExtractMask` SSA instruction

New instruction that extracts the embedded mask from an interface holding `Varying[T]`. Follows the `SPMDIndex` pattern (value-producing, synthetic, no source position).

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (add after SPMDIndex at ~line 1476)
- Modify: `x-tools-spmd/go/ssa/print.go` (add after SPMDIndex String at ~line 436)
- Modify: `x-tools-spmd/go/ssa/sanity.go` (add after SPMDIndex case at ~line 262)

**Step 1: Add instruction definition in `ssa.go`**

After the `SPMDIndex` definition (line 1476), add:

```go
// SPMDExtractMask extracts the embedded lane mask from an interface value
// that holds a boxed Varying[T]. The mask records which lanes were active
// when the value was boxed via MakeInterface with SPMDMask.
// X must be an interface value.
// Lanes is the expected lane count.
// The result type is Varying[mask].
//
// Example printed form:
//
//	t2 = spmd_extract_mask<4> t0
type SPMDExtractMask struct {
	register
	X     Value // interface value to extract mask from
	Lanes int   // expected lane count
}
```

**Step 2: Add Operands method**

After SPMDIndex Operands (line 2074), add:

```go
func (v *SPMDExtractMask) Operands(rands []*Value) []*Value {
	return append(rands, &v.X)
}
```

**Step 3: Add Pos method**

After SPMDIndex Pos (line 2083), add:

```go
func (v *SPMDExtractMask) Pos() token.Pos { return token.NoPos }
```

**Step 4: Add String method in `print.go`**

After SPMDIndex String (line 436), add:

```go
func (v *SPMDExtractMask) String() string {
	return fmt.Sprintf("spmd_extract_mask<%d> %s", v.Lanes, spmdRelName(v.X, v))
}
```

**Step 5: Add sanity case in `sanity.go`**

After SPMDIndex case (line 262), add:

```go
	case *SPMDExtractMask:
		if instr.Lanes <= 0 {
			s.errorf("SPMDExtractMask: Lanes must be > 0, got %d", instr.Lanes)
		}
```

**Step 6: Verify build**

Run: `cd x-tools-spmd && go build ./go/ssa/...`
Expected: compiles without errors

**Step 7: Commit**

```
feat: add SPMDExtractMask instruction for interface mask extraction
```

---

### Task 3: Set `SPMDMask` on `MakeInterface` in predication pass

Thread the per-block condition mask into `MakeInterface` instructions that box SPMDType values. Add cases in all four mask-setting functions.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go`
  - `spmdMaskMemOps` (~line 2703)
  - `spmdConvertScopedMemOps` (~line 1399)
  - `spmdConvertAllMemOps` (~line 2534)
  - `spmdMaskScopedCallOps` (~line 1492) or new sibling

**Step 1: Add MakeInterface case to `spmdMaskMemOps`**

At `spmd_predicate.go:~2808` (after the `case *Index` block, before the closing `}`), add:

```go
		case *MakeInterface:
			if instr.SPMDMask != nil {
				continue
			}
			if _, ok := instr.X.Type().(*types.SPMDType); ok {
				instr.SPMDMask = mask
				instr.SPMDLanes = lanes
				spmdAddReferrer(mask, instr)
			}
```

**Step 2: Add MakeInterface case to `spmdConvertScopedMemOps`**

At `spmd_predicate.go:~1483` (after the `case *Store` block, before the closing `}`), add the same pattern:

```go
			case *MakeInterface:
				if instr.SPMDMask != nil {
					continue
				}
				if _, ok := instr.X.Type().(*types.SPMDType); ok {
					instr.SPMDMask = mask
					instr.SPMDLanes = lanes
					spmdAddReferrer(mask, instr)
				}
```

**Step 3: Add MakeInterface case to `spmdConvertAllMemOps`**

At `spmd_predicate.go:~2600` (after the `case *Store` block in `spmdConvertAllMemOps`), add the same pattern.

**Step 4: Add new `spmdMaskScopedMakeInterfaceOps` or extend `spmdMaskScopedCallOps`**

Add a new function after `spmdMaskScopedCallOps` (~line 1510):

```go
// spmdMaskScopedMakeInterfaceOps sets SPMDMask on MakeInterface instructions
// that box SPMDType values in scopeBlocks. Skips instructions that already
// have SPMDMask set (e.g., by spmdMaskMemOps for varying-if blocks).
func spmdMaskScopedMakeInterfaceOps(fn *Function, scopeBlocks map[*BasicBlock]bool, defaultMask Value, lanes int) {
	for _, block := range fn.Blocks {
		if !scopeBlocks[block] {
			continue
		}
		for _, instr := range block.Instrs {
			mi, ok := instr.(*MakeInterface)
			if !ok || mi.SPMDMask != nil {
				continue
			}
			if _, ok := mi.X.Type().(*types.SPMDType); !ok {
				continue
			}
			mi.SPMDMask = defaultMask
			mi.SPMDLanes = lanes
			spmdAddReferrer(defaultMask, mi)
		}
	}
}
```

**Step 5: Wire new function into `spmdConvertLoopOps`**

At `spmd_predicate.go:205-216`, add calls after `spmdMaskScopedCallOps`:

```go
		// Convert main blocks with all-ones mask (every lane is active).
		spmdConvertScopedMemOps(fn, mainBlocks, allOnesMask, loop.LaneCount)
		spmdMaskScopedCallOps(fn, mainBlocks, allOnesMask)
		spmdMaskScopedIndexOps(fn, mainBlocks, allOnesMask)
		spmdMaskScopedMakeInterfaceOps(fn, mainBlocks, allOnesMask, loop.LaneCount)

		// Convert tail blocks with the tail mask (partial last iteration).
		// ...
		spmdConvertScopedMemOps(fn, tailBlocks, tailMask, loop.LaneCount)
		spmdMaskScopedCallOps(fn, tailBlocks, tailMask)
		spmdMaskScopedIndexOps(fn, tailBlocks, tailMask)
		spmdMaskScopedMakeInterfaceOps(fn, tailBlocks, tailMask, loop.LaneCount)
```

**Step 6: Wire into `predicateSPMDFuncBody`**

At `spmd_predicate.go:~389` (after `spmdMaskCallOps`), add:

```go
	// Step 4: Set mask on MakeInterface instructions that box varying values.
	spmdMaskAllMakeInterfaceOps(fn, activeMask, lanes)
```

And add the corresponding function:

```go
// spmdMaskAllMakeInterfaceOps sets SPMDMask on MakeInterface instructions
// that box SPMDType values anywhere in fn. Used for SPMD function bodies.
func spmdMaskAllMakeInterfaceOps(fn *Function, mask Value, lanes int) {
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			mi, ok := instr.(*MakeInterface)
			if !ok || mi.SPMDMask != nil {
				continue
			}
			if _, ok := mi.X.Type().(*types.SPMDType); !ok {
				continue
			}
			mi.SPMDMask = mask
			mi.SPMDLanes = lanes
			spmdAddReferrer(mask, mi)
		}
	}
}
```

**Step 7: Verify build**

Run: `cd x-tools-spmd && go build ./go/ssa/...`
Expected: compiles without errors

**Step 8: Commit**

```
feat: set SPMDMask on MakeInterface in predication pass
```

---

### Task 4: Insert `SPMDExtractMask` after TypeAssert on SPMDType

Add a new sub-pass that scans for `TypeAssert` on SPMDType and inserts `SPMDExtractMask` + mask AND to narrow the scope's active mask.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` (new function + wire into existing passes)

**Step 1: Add mask narrowing function**

Add after `spmdMaskAllMakeInterfaceOps`:

```go
// spmdNarrowMaskAtTypeAsserts scans blocks for TypeAssert instructions on
// SPMDType. For each, it inserts an SPMDExtractMask to extract the embedded
// mask from the interface, ANDs it with the current activeMask, and updates
// all subsequent masked instructions in the same block to use the narrowed mask.
//
// This ensures that when a varying value is unboxed from an interface, the
// embedded mask (recording which lanes were valid at boxing time) propagates
// to all downstream operations.
func spmdNarrowMaskAtTypeAsserts(fn *Function, activeMask Value, lanes int) {
	for _, block := range fn.Blocks {
		var narrowedMask Value
		for i, instr := range block.Instrs {
			// Check for TypeAssert on SPMDType.
			ta, ok := instr.(*TypeAssert)
			if ok {
				if _, isSPMD := ta.AssertedType.(*types.SPMDType); isSPMD {
					// Insert SPMDExtractMask after the TypeAssert.
					extract := &SPMDExtractMask{X: ta.X, Lanes: lanes}
					extract.setType(spmdpkg.NewVaryingMask())
					extract.setBlock(block)
					spmdAddReferrer(ta.X, extract)

					// AND with current active mask.
					andOp := &BinOp{Op: token.AND, X: activeMask, Y: extract}
					andOp.setType(spmdpkg.NewVaryingMask())
					andOp.setBlock(block)
					spmdAddReferrer(activeMask, andOp)
					spmdAddReferrer(extract, andOp)

					// Insert both instructions after the TypeAssert.
					// Shift instructions to make room.
					block.Instrs = slices.Insert(block.Instrs, i+1, Instruction(extract), Instruction(andOp))

					narrowedMask = andOp
					continue
				}
			}

			// If we have a narrowed mask, update subsequent masked instructions.
			if narrowedMask == nil {
				continue
			}
			switch v := instr.(type) {
			case *SPMDStore:
				if v.Mask == activeMask {
					v.Mask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			case *SPMDLoad:
				if v.Mask == activeMask {
					v.Mask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			case *Call:
				if v.Call.SPMDMask == activeMask {
					v.Call.SPMDMask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			case *IndexAddr:
				if v.SPMDMask == activeMask {
					v.SPMDMask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			case *Index:
				if v.SPMDMask == activeMask {
					v.SPMDMask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			case *MakeInterface:
				if v.SPMDMask == activeMask {
					v.SPMDMask = narrowedMask
					spmdAddReferrer(narrowedMask, v)
				}
			}
		}
	}
}
```

**Step 2: Wire into `spmdConvertLoopOps`**

At `spmd_predicate.go:~207` and `~216`, after the `spmdMaskScoped*` calls for main and tail blocks, add:

```go
		spmdNarrowMaskAtTypeAsserts(fn, allOnesMask, loop.LaneCount)
```

Note: Call once after all scope masking is done, so it can find the mask values set by prior passes.

**Step 3: Wire into `predicateSPMDFuncBody`**

At `spmd_predicate.go:~391`, after `spmdMaskAllMakeInterfaceOps`, add:

```go
	// Step 5: Narrow mask at TypeAssert sites that unbox varying interfaces.
	spmdNarrowMaskAtTypeAsserts(fn, activeMask, lanes)
```

**Step 4: Verify build**

Run: `cd x-tools-spmd && go build ./go/ssa/...`
Expected: compiles without errors

**Step 5: Commit**

```
feat: insert SPMDExtractMask and narrow mask at TypeAssert sites
```

---

### Task 5: x-tools-spmd unit tests

Test that `MakeInterface.SPMDMask` is set correctly and that `SPMDExtractMask` is inserted with mask narrowing.

**Files:**
- Create: `x-tools-spmd/go/ssa/spmd_interface_test.go`

**Step 1: Write test for MakeInterface mask in varying-if**

```go
package ssa_test

import (
	"go/token"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// TestMakeInterfaceSPMDMask verifies that MakeInterface instructions boxing
// SPMDType values inside a varying-if block receive the per-block mask.
func TestMakeInterfaceSPMDMask(t *testing.T) {
	src := `
package main

import "lanes"

func f(v lanes.Varying[int]) {
	var x interface{} = v
	_ = x
}
`
	fn := buildSPMDFunction(t, src, "f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			mi, ok := instr.(*ssa.MakeInterface)
			if !ok {
				continue
			}
			found = true
			if mi.SPMDMask == nil {
				t.Error("MakeInterface boxing SPMDType should have SPMDMask set")
			}
			if mi.SPMDLanes <= 0 {
				t.Errorf("MakeInterface.SPMDLanes should be > 0, got %d", mi.SPMDLanes)
			}
		}
	}
	if !found {
		t.Error("no MakeInterface instruction found in function f")
	}
}
```

Note: `buildSPMDFunction` is a test helper that builds SSA for a single-function SPMD source. It may need to be extracted from existing test helpers in `spmd_varying_test.go` or `spmd_loop_test.go`. Check those files for the pattern and reuse.

**Step 2: Write test for SPMDExtractMask insertion**

```go
// TestSPMDExtractMaskInsertion verifies that after TypeAssert on SPMDType,
// an SPMDExtractMask instruction is inserted and the mask is AND'd with
// the active mask.
func TestSPMDExtractMaskInsertion(t *testing.T) {
	src := `
package main

import "lanes"

func g(x interface{}) lanes.Varying[int] {
	v := x.(lanes.Varying[int])
	return v
}
`
	fn := buildSPMDFunction(t, src, "g")
	if fn == nil {
		t.Fatal("function g not found")
	}

	foundExtract := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.SPMDExtractMask); ok {
				foundExtract = true
			}
		}
	}
	if !foundExtract {
		t.Error("no SPMDExtractMask instruction found after TypeAssert on SPMDType")
	}
}
```

**Step 3: Run tests**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestMakeInterfaceSPMDMask -v`
Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestSPMDExtractMaskInsertion -v`
Expected: PASS

**Step 4: Commit**

```
test: add unit tests for MakeInterface mask and SPMDExtractMask
```

---

### Task 6: Change boxed representation to `struct{[N]T, [N]int32}`

Update TinyGo's type code mapping and boxing to use the struct format.

**Files:**
- Modify: `tinygo/compiler/interface.go:128-136` (getTypeCode SPMDType case)
- Modify: `tinygo/compiler/compiler.go:2949-2957` (MakeInterface case)
- Modify: `tinygo/compiler/spmd.go` (add helper for building the struct type)

**Step 1: Add helper to build the Go struct type for boxed varying**

In `spmd.go`, add after `vectorToArray` (~line 375):

```go
// spmdBoxedVaryingGoType returns the Go struct type used to box a varying value
// with its mask: struct{ Value [N]T; Mask [N]int32 }.
func (c *compilerContext) spmdBoxedVaryingGoType(spmdType *types.SPMDType, laneCount int) *types.Struct {
	arrayType := types.NewArray(spmdType.Elem(), int64(laneCount))
	maskArrayType := types.NewArray(types.Typ[types.Int32], int64(laneCount))
	return types.NewStruct([]*types.Var{
		types.NewVar(token.NoPos, nil, "Value", arrayType),
		types.NewVar(token.NoPos, nil, "Mask", maskArrayType),
	}, nil)
}
```

**Step 2: Update `getTypeCode` in `interface.go`**

At `interface.go:128-136`, change:

```go
	if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
		elemLLVM := c.getLLVMType(spmdType.Elem())
		laneCount := c.spmdEffectiveLaneCount(spmdType, elemLLVM)
		return c.getTypeCode(c.spmdBoxedVaryingGoType(spmdType, laneCount))
	}
```

**Step 3: Update MakeInterface case in `compiler.go`**

At `compiler.go:2949-2957`, change:

```go
	case *ssa.MakeInterface:
		val := b.getValue(expr.X, getPos(expr))
		if spmdType, ok := expr.X.Type().(*types.SPMDType); ok && spmdType.IsVarying() {
			elemLLVM := b.getLLVMType(spmdType.Elem())
			laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)

			// Convert value vector to array.
			valArr := b.vectorToArray(val)

			// Build mask array: per-block mask if set, all-ones otherwise.
			maskElemType := b.spmdMaskElemType(laneCount)
			maskVecType := llvm.VectorType(maskElemType, laneCount)
			maskArrType := llvm.ArrayType(maskElemType, laneCount)
			var maskArr llvm.Value
			if expr.SPMDMask != nil {
				maskVec := b.getValue(expr.SPMDMask, getPos(expr))
				maskArr = b.vectorToArray(maskVec)
			} else {
				maskArr = llvm.Undef(maskArrType)
				allOnes := llvm.ConstAllOnes(maskElemType)
				for i := 0; i < laneCount; i++ {
					maskArr = b.CreateInsertValue(maskArr, allOnes, i, "")
				}
			}

			// Pack struct{[N]T, [N]int32}.
			structType := b.ctx.StructType([]llvm.Type{valArr.Type(), maskArrType}, false)
			packed := llvm.Undef(structType)
			packed = b.CreateInsertValue(packed, valArr, 0, "")
			packed = b.CreateInsertValue(packed, maskArr, 1, "")

			return b.createMakeInterface(packed, expr.X.Type(), expr.Pos()), nil
		}
		return b.createMakeInterface(val, expr.X.Type(), expr.Pos()), nil
```

**Step 4: Verify build**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: compiles without errors

**Step 5: Commit**

```
feat: change boxed Varying[T] repr to struct{[N]T, [N]int32}
```

---

### Task 7: Update `createTypeAssertSPMD` for struct representation

Update the unboxing path to extract from the new `struct{[N]T, [N]int32}` format.

**Files:**
- Modify: `tinygo/compiler/spmd.go:4787-4831` (createTypeAssertSPMD)

**Step 1: Update `createTypeAssertSPMD`**

Replace the function at `spmd.go:4787`:

```go
func (b *builder) createTypeAssertSPMD(itf llvm.Value, expr *ssa.TypeAssert, spmdType *types.SPMDType, vecType llvm.Type) llvm.Value {
	// Build the struct type that matches the type code used in boxing.
	elemLLVM := b.getLLVMType(spmdType.Elem())
	laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)
	boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
	boxedLLVMType := b.getLLVMType(boxedGoType)

	// Compare type codes using the struct type (same as boxing path).
	actualTypeNum := b.CreateExtractValue(itf, 0, "interface.type")
	name, _ := getTypeCodeName(boxedGoType)
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

	// OK block: extract struct, split into value array + mask array, convert to vectors.
	b.SetInsertPointAtEnd(okBlock)
	boxedStruct := b.extractValueFromInterface(itf, boxedLLVMType)
	valArr := b.CreateExtractValue(boxedStruct, 0, "typeassert.spmd.valarr")
	valueOk := b.arrayToVector(valArr, vecType)
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
feat: update createTypeAssertSPMD for struct boxed representation
```

---

### Task 8: Lower `SPMDExtractMask` in TinyGo compiler

Add handler for the new `SPMDExtractMask` SSA instruction in TinyGo.

**Files:**
- Modify: `tinygo/compiler/compiler.go` (add case in main instruction switch)
- Modify: `tinygo/compiler/spmd.go` (add lowering function)

**Step 1: Add `createSPMDExtractMask` in `spmd.go`**

After `createTypeAssertSPMD` (~line 4831), add:

```go
// createSPMDExtractMask extracts the embedded lane mask from a boxed
// Varying[T] interface value. The interface holds struct{[N]T, [N]int32};
// this function extracts field 1 (mask array) and converts to a mask vector.
func (b *builder) createSPMDExtractMask(instr *ssa.SPMDExtractMask) llvm.Value {
	itf := b.getValue(instr.X, token.NoPos)

	// Determine the SPMD type from the TypeAssert that produced this interface usage.
	// We need to know T to compute laneCount and build the struct type.
	// Use the Lanes field which was set by the predication pass.
	laneCount := instr.Lanes

	// Build the boxed struct LLVM type: struct{[N]T_any, [N]maskElemType}.
	// We only need the mask part, but we must know the full struct layout
	// to extract from the interface correctly.
	// Use a generic approach: extract the raw pointer and offset to the mask field.
	maskElemType := b.spmdMaskElemType(laneCount)
	maskArrType := llvm.ArrayType(maskElemType, laneCount)
	maskVecType := llvm.VectorType(maskElemType, laneCount)

	// Extract the value pointer from the interface and read the mask field.
	// The mask is at a fixed offset in the struct (after the value array).
	// Since we don't know the value array type here, we use the pointer
	// from the interface and offset by the value array size.
	//
	// Alternative approach: store the mask in a known location that doesn't
	// depend on T. For now, we require the caller to provide type context.
	//
	// Practical approach: find the corresponding TypeAssert and use its type.
	// The SPMDExtractMask.X is the same interface value as the TypeAssert.X.
	// Walk referrers of X to find the TypeAssert and get the asserted type.
	var boxedLLVMType llvm.Type
	if refs := instr.X.Referrers(); refs != nil {
		for _, ref := range *refs {
			if ta, ok := ref.(*ssa.TypeAssert); ok {
				if spmdType, ok := ta.AssertedType.(*types.SPMDType); ok {
					boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
					boxedLLVMType = b.getLLVMType(boxedGoType)
					break
				}
			}
		}
	}
	if boxedLLVMType.IsNil() {
		// Fallback: cannot determine struct type. Return all-ones mask.
		return llvm.ConstAllOnes(maskVecType)
	}

	// Extract the struct from the interface, then extract mask array (field 1).
	boxedStruct := b.extractValueFromInterface(itf, boxedLLVMType)
	maskArr := b.CreateExtractValue(boxedStruct, 1, "spmd.extract.maskarr")
	return b.arrayToVector(maskArr, maskVecType)
}
```

**Step 2: Add case in `compiler.go` main switch**

Find the SPMD instruction cases (near lines 3210-3215). Add:

```go
	case *ssa.SPMDExtractMask:
		return b.createSPMDExtractMask(expr), nil
```

**Step 3: Verify build**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: compiles without errors

**Step 4: Commit**

```
feat: lower SPMDExtractMask to LLVM mask vector extraction
```

---

### Task 9: TinyGo LLVM tests

Add tests for the new boxing/unboxing with embedded mask.

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write test for boxing with mask**

Follow the `TestVectorToArray` pattern (lines 5250-5314) using `newTestCompilerContext`/`newTestBuilder`:

```go
func TestSPMDBoxedVaryingStruct(t *testing.T) {
	c := newTestCompilerContext(t)
	b := newTestBuilder(t, c)
	defer b.Dispose()

	// Create a 4xi32 value vector and a 4xi32 mask vector.
	i32Type := b.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, 4)
	val := llvm.ConstVector([]llvm.Value{
		llvm.ConstInt(i32Type, 10, false),
		llvm.ConstInt(i32Type, 20, false),
		llvm.ConstInt(i32Type, 30, false),
		llvm.ConstInt(i32Type, 40, false),
	}, false)

	// Convert value to array.
	valArr := b.vectorToArray(val)
	if valArr.Type().TypeKind() != llvm.ArrayTypeKind {
		t.Fatalf("expected array type, got %s", valArr.Type().String())
	}
	if valArr.Type().ArrayLength() != 4 {
		t.Fatalf("expected array length 4, got %d", valArr.Type().ArrayLength())
	}

	// Create mask array (all-ones).
	maskArrType := llvm.ArrayType(i32Type, 4)
	allOnes := llvm.ConstAllOnes(i32Type)
	maskArr := llvm.Undef(maskArrType)
	for i := 0; i < 4; i++ {
		maskArr = b.CreateInsertValue(maskArr, allOnes, i, "")
	}

	// Build struct{[4]i32, [4]i32}.
	structType := b.ctx.StructType([]llvm.Type{valArr.Type(), maskArrType}, false)
	packed := llvm.Undef(structType)
	packed = b.CreateInsertValue(packed, valArr, 0, "")
	packed = b.CreateInsertValue(packed, maskArr, 1, "")

	// Verify struct layout.
	if packed.Type().TypeKind() != llvm.StructTypeKind {
		t.Fatalf("expected struct type, got %s", packed.Type().String())
	}
	// Extract field 0 (value) and field 1 (mask) and verify types.
	valField := b.CreateExtractValue(packed, 0, "")
	maskField := b.CreateExtractValue(packed, 1, "")
	if valField.Type().TypeKind() != llvm.ArrayTypeKind {
		t.Errorf("field 0 should be array, got %s", valField.Type().String())
	}
	if maskField.Type().TypeKind() != llvm.ArrayTypeKind {
		t.Errorf("field 1 should be array, got %s", maskField.Type().String())
	}
}
```

**Step 2: Run tests**

Run: `cd tinygo && make test GO=/home/cedric/work/SPMD/go/bin/go GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDBoxedVaryingStruct" GOTESTPKGS="./compiler/"`
Expected: PASS

**Step 3: Commit**

```
test: add LLVM test for boxed varying struct{[N]T, [N]int32}
```

---

### Task 10: E2E validation

Verify `type-switch-varying` E2E test still passes with the new struct representation, and add a masked-context test.

**Files:**
- Modify: `test/integration/spmd/type-switch-varying/main.go` (add masked-context test case)
- Run: `test/e2e/spmd-e2e-test.sh`

**Step 1: Add masked-context test case**

Add to the existing `type-switch-varying/main.go`:

```go
// testMaskedBoxing demonstrates boxing in a masked context (varying if).
func testMaskedBoxing() {
	fmt.Println("\n=== Masked Boxing ===")

	data := []int{1, 2, 3, 4, 5, 6, 7, 8}
	go for _, v := range data {
		if v > 4 {
			// Boxing happens in masked context — only lanes where v > 4 are valid.
			var x interface{} = v
			if w, ok := x.(lanes.Varying[int]); ok {
				sum := reduce.Add(w)
				fmt.Printf("Masked box sum=%d\n", sum)
			}
		}
	}
}
```

Add `testMaskedBoxing()` call in `main()`.

**Step 2: Build and run the E2E test**

Run: `bash test/e2e/spmd-e2e-test.sh`
Expected: `type-switch-varying` passes at run level

**Step 3: Commit**

```
test: add masked-context boxing test to type-switch-varying
```

---

## Task Dependency Graph

```
Task 1 (MakeInterface fields) ──┐
                                 ├── Task 3 (set mask in predication) ──┐
Task 2 (SPMDExtractMask instr) ─┘                                      ├── Task 5 (x-tools tests)
                                    Task 4 (insert extract+narrow) ─────┘

Task 6 (boxed repr change) ────── Task 7 (update unboxing) ──┐
                                                               ├── Task 9 (LLVM tests)
Task 8 (lower SPMDExtractMask) ───────────────────────────────┘

All tasks ──────────────────────────────────────────────────── Task 10 (E2E)
```

Tasks 1 and 2 are independent and can be done in parallel.
Tasks 3 and 4 depend on 1+2 respectively but are independent of each other.
Task 5 depends on 3+4.
Tasks 6, 7, 8 are sequential (TinyGo side).
Task 9 depends on 6+7+8.
Task 10 depends on all.
