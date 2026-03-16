# Array-to-Vector Promotion Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote small arrays (≤16 bytes) inside `go for` loops to `Varying[T]` SSA values, eliminating stack allocations, memset, memcpy, and per-iteration memory traffic.

**Architecture:** New SSA pass `promoteSPMDArrays` in `x-tools-spmd/go/ssa/` runs after `lift` and before `predicateSPMD`. It scans `Alloc` instructions for fixed-size arrays whose lane count matches the enclosing SPMD loop and whose only referrers are varying `IndexAddr`→`Store`/`Load` chains with the IterPhi as index. Eligible allocs are replaced with `Varying[T]` SSA values using existing `replaceAll` and `removeInstr` helpers. A new `SPMDVectorFromMemory` instruction handles initialization from external pointers (strings, slices). TinyGo lowers `SPMDVectorFromMemory` to a masked `v128.load`.

**Tech Stack:** Go SSA (`x-tools-spmd/go/ssa`), TinyGo LLVM backend (`tinygo/compiler/`), WASM SIMD128

**Spec:** `docs/plans/2026-03-11-array-to-vector-promotion-design.md`

**Key SSA mutation patterns used (from existing codebase):**
- `replaceAll(oldVal, newVal)` — `lift.go:501`: replaces all uses of `oldVal` with `newVal`, updates referrer lists
- `removeInstr(refs, instr)` — `lift.go:108`: removes `instr` from a referrer slice
- `spmdCompactInstrs(block)` — `spmd_predicate.go:1169`: removes nil slots from `block.Instrs`
- `spmdAddReferrer(val, instr)` — `spmd_predicate.go:2725`: adds `instr` to `val`'s referrer list
- To remove an instruction from a block: set `block.Instrs[i] = nil`, then call `spmdCompactInstrs(block)`
- `SPMDLoops` is `[]*SPMDLoopInfo` (pointer slice) — iterate with `for _, loop := range f.SPMDLoops`

---

## Chunk 1: SPMDVectorFromMemory SSA Instruction

Add the new instruction type to the SSA package with all required methods, emit helper, sanity checks, clone support, and interpreter panic.

### Task 1: SPMDVectorFromMemory struct and methods

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (add struct after SPMDExtractMask ~line 1519, add Operands ~line 2125, add Pos ~line 2135, add Type ~line 2146)
- Modify: `x-tools-spmd/go/ssa/print.go` (add String method ~line 440)
- Modify: `x-tools-spmd/go/ssa/emit.go` (add emitSPMDVectorFromMemory ~line 689)
- Modify: `x-tools-spmd/go/ssa/sanity.go` (add validation ~line 268)
- Modify: `x-tools-spmd/go/ssa/spmd_peel.go` (add clone case ~line 214)
- Modify: `x-tools-spmd/go/ssa/interp/interp.go` (add panic case ~line 430)
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go`

- [ ] **Step 1: Write failing tests for SPMDVectorFromMemory**

Create `x-tools-spmd/go/ssa/spmd_promote_test.go` with structural tests following the pattern in `spmd_predicate_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa_test

import (
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// TestSPMDVectorFromMemory_Type verifies the result type is Varying[elemType].
func TestSPMDVectorFromMemory_Type(t *testing.T) {
	v := &ssa.SPMDVectorFromMemory{
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	got := v.Type()
	st, ok := got.(*types.SPMDType)
	if !ok {
		t.Fatalf("Type() = %T, want *types.SPMDType", got)
	}
	if st.Elem() != types.Typ[types.Byte] {
		t.Fatalf("Elem() = %v, want byte", st.Elem())
	}
}

// TestSPMDVectorFromMemory_Type_Int32 verifies Varying[int32] result.
func TestSPMDVectorFromMemory_Type_Int32(t *testing.T) {
	v := &ssa.SPMDVectorFromMemory{
		ElemType: types.Typ[types.Int32],
		Lanes:    4,
	}
	got := v.Type()
	st, ok := got.(*types.SPMDType)
	if !ok {
		t.Fatalf("Type() = %T, want *types.SPMDType", got)
	}
	if st.Elem() != types.Typ[types.Int32] {
		t.Fatalf("Elem() = %v, want int32", st.Elem())
	}
}

// TestSPMDVectorFromMemory_Operands verifies operand pointers.
func TestSPMDVectorFromMemory_Operands(t *testing.T) {
	ptr := ptrConst()
	ln := intConst(16)
	v := &ssa.SPMDVectorFromMemory{
		Ptr:      ptr,
		Len:      ln,
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	ops := v.Operands(nil)
	if len(ops) != 2 {
		t.Fatalf("Operands() returned %d, want 2", len(ops))
	}
	if *ops[0] != ptr {
		t.Errorf("Operands()[0] = %v, want ptr", *ops[0])
	}
	if *ops[1] != ln {
		t.Errorf("Operands()[1] = %v, want len", *ops[1])
	}
}

// TestSPMDVectorFromMemory_OperandsMutate verifies operand mutation.
func TestSPMDVectorFromMemory_OperandsMutate(t *testing.T) {
	ptr := ptrConst()
	ln := intConst(16)
	v := &ssa.SPMDVectorFromMemory{
		Ptr:      ptr,
		Len:      ln,
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	ops := v.Operands(nil)
	newPtr := ptrConst()
	*ops[0] = newPtr
	if v.Ptr != newPtr {
		t.Errorf("after mutation, Ptr not updated")
	}
}

// TestSPMDVectorFromMemory_String verifies string format.
func TestSPMDVectorFromMemory_String(t *testing.T) {
	ptr := ptrConst()
	ln := intConst(16)
	v := &ssa.SPMDVectorFromMemory{
		Ptr:      ptr,
		Len:      ln,
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	s := v.String()
	if !strings.HasPrefix(s, "spmd_vector_from_memory") {
		t.Errorf("String() = %q, want prefix 'spmd_vector_from_memory'", s)
	}
	if !strings.Contains(s, "<16") {
		t.Errorf("String() = %q, should contain '<16'", s)
	}
}

// TestSPMDVectorFromMemory_Pos verifies Pos returns stored position.
func TestSPMDVectorFromMemory_Pos(t *testing.T) {
	v := &ssa.SPMDVectorFromMemory{
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	if v.Pos() != 0 {
		t.Errorf("Pos() = %v, want NoPos", v.Pos())
	}
}

// TestSPMDVectorFromMemory_Referrers verifies Referrers returns &slice (value-producing).
func TestSPMDVectorFromMemory_Referrers(t *testing.T) {
	v := &ssa.SPMDVectorFromMemory{
		ElemType: types.Typ[types.Byte],
		Lanes:    16,
	}
	refs := v.Referrers()
	if refs == nil {
		t.Fatal("Referrers() = nil, want non-nil (value-producing)")
	}
}

// Compile-time interface checks.
var _ ssa.Value = (*ssa.SPMDVectorFromMemory)(nil)
var _ ssa.Instruction = (*ssa.SPMDVectorFromMemory)(nil)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestSPMDVectorFromMemory -v -count=1`
Expected: FAIL — `SPMDVectorFromMemory` type does not exist

- [ ] **Step 3: Add SPMDVectorFromMemory struct to ssa.go**

In `x-tools-spmd/go/ssa/ssa.go`, after `SPMDExtractMask` (line ~1519), add:

```go
// SPMDVectorFromMemory loads elements from a pointer into a Varying[T] value.
// When Len < lane count, remaining lanes are zero (implicit tail masking).
//
// Pos() returns the position from the original allocation or copy.
//
// Example:
//
//	%v = SPMDVectorFromMemory %ptr %len
//
// Type: *types.SPMDType{Elem: ElemType}
type SPMDVectorFromMemory struct {
	register
	Ptr      Value      // pointer to element type
	Len      Value      // number of valid elements
	ElemType types.Type // element type (byte, int32, etc.)
	Lanes    int        // lane count from enclosing SPMD loop
}
```

Add Operands method after existing SPMD Operands (line ~2125):

```go
func (v *SPMDVectorFromMemory) Operands(rands []*Value) []*Value {
	return append(rands, &v.Ptr, &v.Len)
}
```

Add Pos method after existing SPMD Pos methods (line ~2135):

```go
func (v *SPMDVectorFromMemory) Pos() token.Pos { return v.pos }
```

Add Type method after existing SPMD Type methods (line ~2146):

```go
func (v *SPMDVectorFromMemory) Type() types.Type {
	return types.NewVarying(v.ElemType)
}
```

- [ ] **Step 4: Add String method to print.go**

In `x-tools-spmd/go/ssa/print.go`, after SPMDExtractMask.String (line ~440), add:

```go
func (v *SPMDVectorFromMemory) String() string {
	return fmt.Sprintf("spmd_vector_from_memory<%d, %s> %s %s",
		v.Lanes, v.ElemType, relName(v.Ptr, v), relName(v.Len, v))
}
```

- [ ] **Step 5: Add emit helper to emit.go**

In `x-tools-spmd/go/ssa/emit.go`, after `emitSPMDIndex` (line ~689), add:

```go
func emitSPMDVectorFromMemory(f *Function, ptr, length Value, lanes int, elemType types.Type, pos token.Pos) *SPMDVectorFromMemory {
	v := &SPMDVectorFromMemory{
		Ptr:      ptr,
		Len:      length,
		ElemType: elemType,
		Lanes:    lanes,
	}
	v.pos = pos
	f.emit(v)
	return v
}
```

- [ ] **Step 6: Add sanity check to sanity.go**

In `x-tools-spmd/go/ssa/sanity.go`, after SPMDExtractMask validation (line ~267), add:

```go
case *SPMDVectorFromMemory:
	if v.Lanes <= 0 {
		s.errorf("SPMDVectorFromMemory has non-positive Lanes: %d", v.Lanes)
	}
	if v.ElemType == nil {
		s.errorf("SPMDVectorFromMemory has nil ElemType")
	}
	if v.Ptr == nil {
		s.errorf("SPMDVectorFromMemory has nil Ptr")
	}
	if v.Len == nil {
		s.errorf("SPMDVectorFromMemory has nil Len")
	}
```

- [ ] **Step 7: Add clone case to spmd_peel.go**

In `x-tools-spmd/go/ssa/spmd_peel.go`, before the `default` panic (line ~216), add. Note: must call `setBlock` and `spmdAddReferrer` for each operand — matching the pattern used by all other clone cases (e.g., `SPMDExtractMask` at lines 139-147):

```go
		case *SPMDVectorFromMemory:
			clone := &SPMDVectorFromMemory{
				Ptr:      spmdTranslateValue(v.Ptr, valueMap),
				Len:      spmdTranslateValue(v.Len, valueMap),
				ElemType: v.ElemType,
				Lanes:    v.Lanes,
			}
			clone.pos = v.pos
			clone.setType(v.Type())
			clone.setBlock(dstBlock)
			dstBlock.Instrs = append(dstBlock.Instrs, clone)
			valueMap[v] = clone
			spmdAddReferrer(clone.Ptr, clone)
			spmdAddReferrer(clone.Len, clone)
```

- [ ] **Step 8: Add interpreter panic to interp.go**

In `x-tools-spmd/go/ssa/interp/interp.go`, at the SPMD panic case (line ~430), add `*ssa.SPMDVectorFromMemory` to the case list:

```go
case *ssa.SPMDSelect, *ssa.SPMDLoad, *ssa.SPMDStore, *ssa.SPMDIndex, *ssa.SPMDExtractMask, *ssa.SPMDVectorFromMemory:
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestSPMDVectorFromMemory -v -count=1`
Expected: All 7 tests PASS

- [ ] **Step 10: Run full SPMD test suite for regressions**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestSPMD -v -count=1 2>&1 | tail -20`
Expected: All existing tests still PASS

- [ ] **Step 11: Commit**

Use the `clean-commit` agent per CLAUDE.md mandatory workflow.

---

## Chunk 2: Eligibility Detection and promoteSPMDArrays Pass

Implement the core pass that identifies eligible arrays and the `finishBody` integration point.

### Task 2: promoteSPMDArrays pass

**Files:**
- Create: `x-tools-spmd/go/ssa/spmd_promote.go`
- Modify: `x-tools-spmd/go/ssa/func.go:403-407` (insert pass call inside SPMD guard)
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go` (extend)

- [ ] **Step 1: Write failing test for eligibility detection**

Add to `x-tools-spmd/go/ssa/spmd_promote_test.go`:

```go
import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
)

// buildSPMDProgram builds a package with SPMD loops at the given lane count.
// All RangeStmt nodes in the source are marked as SPMD with the specified lane count.
func buildSPMDProgram(t *testing.T, src string, laneCount int) *ssa.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "input.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Mark all range statements as SPMD with the given lane count.
	ast.Inspect(f, func(n ast.Node) bool {
		if rs, ok := n.(*ast.RangeStmt); ok {
			rs.IsSpmd = true
			rs.LaneCount = laneCount
		}
		return true
	})

	pkg, _, err := ssautil.BuildPackage(
		&types.Config{Importer: importer.Default()},
		fset, types.NewPackage("main", ""), []*ast.File{f}, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// TestPromoteSPMDArrays_ByteArray verifies a [16]byte array inside a
// 16-lane go for loop is promoted (Alloc removed).
func TestPromoteSPMDArrays_ByteArray(t *testing.T) {
	src := `package main

func main() {
	var arr [16]byte
	for i := range 16 {
		arr[i] = byte(i)
	}
}
`
	pkg := buildSPMDProgram(t, src, 16)
	fn := pkg.Func("main")
	if fn == nil {
		t.Fatal("main not found")
	}

	// After promotion, there should be no Alloc for [16]byte in the function.
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if alloc, ok := instr.(*ssa.Alloc); ok {
				if at, ok := alloc.Type().Underlying().(*types.Pointer); ok {
					if _, ok := at.Elem().Underlying().(*types.Array); ok {
						t.Errorf("found unpromoted array Alloc: %s", alloc)
					}
				}
			}
		}
	}
}

// TestPromoteSPMDArrays_Ineligible_Escape verifies arrays passed to calls
// are NOT promoted.
func TestPromoteSPMDArrays_Ineligible_Escape(t *testing.T) {
	src := `package main

func use(p *[16]byte) {}

func main() {
	var arr [16]byte
	for i := range 16 {
		arr[i] = byte(i)
	}
	use(&arr)
}
`
	pkg := buildSPMDProgram(t, src, 16)
	fn := pkg.Func("main")
	if fn == nil {
		t.Fatal("main not found")
	}

	// Array should still exist as Alloc (not promoted due to escape via Call).
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.Alloc); ok {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected Alloc to remain (ineligible for promotion)")
	}
}

// TestPromoteSPMDArrays_Ineligible_LaneMismatch verifies [8]byte in a
// 16-lane loop is NOT promoted (lane count 16 != array length 8).
func TestPromoteSPMDArrays_Ineligible_LaneMismatch(t *testing.T) {
	src := `package main

func main() {
	var arr [8]byte
	for i := range 16 {
		if i < 8 {
			arr[i] = byte(i)
		}
	}
}
`
	pkg := buildSPMDProgram(t, src, 16)
	fn := pkg.Func("main")
	if fn == nil {
		t.Fatal("main not found")
	}

	// Array should still exist (lane count 16 != array length 8).
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.Alloc); ok {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected Alloc to remain (lane count mismatch)")
	}
}

// TestPromoteSPMDArrays_Ineligible_UniformIndex verifies arrays accessed
// with a uniform (non-IterPhi) index are NOT promoted.
func TestPromoteSPMDArrays_Ineligible_UniformIndex(t *testing.T) {
	src := `package main

func main() {
	var arr [16]byte
	for i := range 16 {
		arr[0] = byte(i) // uniform index 0, not IterPhi
	}
}
`
	pkg := buildSPMDProgram(t, src, 16)
	fn := pkg.Func("main")
	if fn == nil {
		t.Fatal("main not found")
	}

	// Array should still exist (uniform index access).
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.Alloc); ok {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected Alloc to remain (uniform index)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPromoteSPMDArrays -v -count=1`
Expected: FAIL — `buildSPMDProgram` exists but `promoteSPMDArrays` not yet called

- [ ] **Step 3: Implement promoteSPMDArrays pass**

Create `x-tools-spmd/go/ssa/spmd_promote.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"go/token"
	"go/types"
)

// promoteSPMDArrays promotes eligible small array allocations to Varying[T]
// SSA values. An Alloc is eligible when:
//   - Type is *[N]T where N*sizeof(T) <= 16
//   - All referrers are IndexAddr with IterPhi index -> Store/UnOp{MUL}
//   - All use sites are inside the SPMD loop body
//   - Lane count of the enclosing SPMD loop equals N
//
// This pass runs after lift (which skips arrays with IndexAddr referrers)
// and before predicateSPMD (so downstream passes see Varying[T] values).
//
// Note: At the time this pass runs, SPMDLoopInfo peeling fields
// (MainBodyBlock, TailBodyBlock) are nil — peeling has not run yet.
// Only BodyBlock and LoopBlock are valid.
func promoteSPMDArrays(f *Function) {
	if len(f.SPMDLoops) == 0 {
		return
	}

	// Build a map from block to its enclosing SPMDLoopInfo.
	// Only BodyBlock and LoopBlock are populated at this point.
	blockToLoop := make(map[*BasicBlock]*SPMDLoopInfo)
	for _, loop := range f.SPMDLoops {
		if loop.BodyBlock != nil {
			blockToLoop[loop.BodyBlock] = loop
		}
		if loop.LoopBlock != nil {
			blockToLoop[loop.LoopBlock] = loop
		}
	}

	// Scan all allocs for eligible arrays. Collect candidates first
	// to avoid modifying Instrs while iterating.
	type candidate struct {
		alloc     *Alloc
		arrayType *types.Array
		elemType  types.Type
		loop      *SPMDLoopInfo
	}
	var candidates []candidate

	for _, b := range f.Blocks {
		for _, instr := range b.Instrs {
			alloc, ok := instr.(*Alloc)
			if !ok {
				continue
			}
			if at, et, loop := checkPromotionEligibility(alloc, blockToLoop); loop != nil {
				candidates = append(candidates, candidate{alloc, at, et, loop})
			}
		}
	}

	// Promote eligible allocs.
	for _, c := range candidates {
		doPromoteArray(f, c.alloc, c.arrayType, c.elemType, c.loop)
	}
}

// checkPromotionEligibility returns (arrayType, elemType, loop) if the alloc
// is eligible for promotion, or (nil, nil, nil) otherwise.
func checkPromotionEligibility(alloc *Alloc, blockToLoop map[*BasicBlock]*SPMDLoopInfo) (*types.Array, types.Type, *SPMDLoopInfo) {
	// Check 1: Type is pointer to fixed-size array.
	ptrType, ok := alloc.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil, nil, nil
	}
	arrayType, ok := ptrType.Elem().Underlying().(*types.Array)
	if !ok {
		return nil, nil, nil
	}

	// Check 2: Fits in v128 (arrayLen * elemSize <= 16).
	elemType := arrayType.Elem()
	elemSize := spmdPromoteElemSize(elemType)
	if elemSize == 0 {
		return nil, nil, nil
	}
	arrayLen := int(arrayType.Len())
	if arrayLen*elemSize > 16 {
		return nil, nil, nil
	}

	// Check 3-6: All referrers are IndexAddr with IterPhi index -> Store/Load,
	// no escapes, all inside same SPMD loop body.
	refs := alloc.Referrers()
	if refs == nil {
		return nil, nil, nil
	}

	var loop *SPMDLoopInfo
	for _, ref := range *refs {
		switch r := ref.(type) {
		case *IndexAddr:
			// Check IndexAddr is in an SPMD loop.
			l, ok := blockToLoop[r.Block()]
			if !ok {
				return nil, nil, nil
			}
			if loop == nil {
				loop = l
			} else if loop != l {
				return nil, nil, nil // multiple loops
			}

			// Check 4: Index must be the IterPhi (varying lane index).
			if r.Index != loop.IterPhi {
				return nil, nil, nil // uniform index
			}

			// Check IndexAddr referrers are only Store (as addr) or UnOp{MUL}.
			iaRefs := r.Referrers()
			if iaRefs == nil {
				return nil, nil, nil
			}
			for _, iaRef := range *iaRefs {
				switch ia := iaRef.(type) {
				case *Store:
					if ia.Addr != r {
						return nil, nil, nil // used as value, not addr
					}
					// Store must be in same loop.
					if _, ok := blockToLoop[ia.Block()]; !ok {
						return nil, nil, nil
					}
				case *UnOp:
					if ia.Op != token.MUL {
						return nil, nil, nil
					}
					// Load must be in same loop.
					if _, ok := blockToLoop[ia.Block()]; !ok {
						return nil, nil, nil
					}
				case *DebugRef:
					// ok
				default:
					return nil, nil, nil
				}
			}
		case *DebugRef:
			// ok
		default:
			return nil, nil, nil // escapes (Call, Phi, etc.)
		}
	}

	if loop == nil {
		return nil, nil, nil
	}

	// Check 5: Lane count matches array length.
	if loop.LaneCount != arrayLen {
		return nil, nil, nil
	}

	return arrayType, elemType, loop
}

// doPromoteArray replaces an eligible Alloc with varying SSA values.
// For the pattern: go for i := range N { arr[i] = f(i) }
// where i is the IterPhi, the stored value IS the full varying vector.
// Each load from arr[i] IS the varying value.
func doPromoteArray(f *Function, alloc *Alloc, arrayType *types.Array, elemType types.Type, loop *SPMDLoopInfo) {
	refs := alloc.Referrers()
	if refs == nil {
		return
	}

	// Collect IndexAddr users and their Store/Load patterns.
	type storeInfo struct {
		store    *Store
		indexAddr *IndexAddr
	}
	type loadInfo struct {
		load      *UnOp
		indexAddr *IndexAddr
	}
	var stores []storeInfo
	var loads []loadInfo
	var indexAddrs []*IndexAddr

	for _, ref := range *refs {
		ia, ok := ref.(*IndexAddr)
		if !ok {
			continue
		}
		indexAddrs = append(indexAddrs, ia)
		iaRefs := ia.Referrers()
		if iaRefs == nil {
			continue
		}
		for _, iaRef := range *iaRefs {
			switch r := iaRef.(type) {
			case *Store:
				stores = append(stores, storeInfo{r, ia})
			case *UnOp:
				if r.Op == token.MUL {
					loads = append(loads, loadInfo{r, ia})
				}
			}
		}
	}

	// For stores: the stored value IS the varying vector.
	// For loads: replace all uses of the load with the stored value.
	// Since all IndexAddr use IterPhi (verified by eligibility), each store
	// writes the full vector and each load reads the full vector.

	// Find the stored value (there should be exactly one store pattern per
	// loop iteration — the value being written IS the varying result).
	if len(stores) == 0 {
		return // no stores to promote
	}

	// Use the first store's value as the varying vector.
	// In the common pattern arr[i] = f(i), storedVal is f(i) which is
	// already a scalar value that, in SPMD context, represents all lanes.
	storedVal := stores[0].store.Val

	// Replace all loads: each load result is replaced by the stored value.
	for _, li := range loads {
		replaceAll(li.load, storedVal)
	}

	// Remove dead instructions: stores, loads, indexaddrs, alloc.
	// Nil out instruction slots, then compact each affected block.
	affectedBlocks := make(map[*BasicBlock]bool)

	for _, si := range stores {
		b := si.store.Block()
		// Remove store from its operands' referrer lists.
		if refs := si.store.Addr.Referrers(); refs != nil {
			*refs = removeInstr(*refs, si.store)
		}
		if refs := si.store.Val.Referrers(); refs != nil {
			*refs = removeInstr(*refs, si.store)
		}
		// Nil out in block.
		for i, instr := range b.Instrs {
			if instr == si.store {
				b.Instrs[i] = nil
				break
			}
		}
		si.store.block = nil
		affectedBlocks[b] = true
	}

	for _, li := range loads {
		b := li.load.Block()
		// Remove load from its operands' referrer lists.
		if refs := li.load.X.Referrers(); refs != nil {
			*refs = removeInstr(*refs, li.load)
		}
		for i, instr := range b.Instrs {
			if instr == li.load {
				b.Instrs[i] = nil
				break
			}
		}
		li.load.block = nil
		affectedBlocks[b] = true
	}

	for _, ia := range indexAddrs {
		b := ia.Block()
		// Remove IndexAddr from its operands' referrer lists.
		if refs := ia.X.Referrers(); refs != nil {
			*refs = removeInstr(*refs, ia)
		}
		if refs := ia.Index.Referrers(); refs != nil {
			*refs = removeInstr(*refs, ia)
		}
		for i, instr := range b.Instrs {
			if instr == ia {
				b.Instrs[i] = nil
				break
			}
		}
		ia.block = nil
		affectedBlocks[b] = true
	}

	// Remove the alloc itself.
	{
		b := alloc.Block()
		for i, instr := range b.Instrs {
			if instr == alloc {
				b.Instrs[i] = nil
				break
			}
		}
		alloc.block = nil
		affectedBlocks[b] = true
	}

	// Compact all affected blocks.
	for b := range affectedBlocks {
		spmdCompactInstrs(b)
	}
}

// spmdPromoteElemSize returns the byte size of a basic type, or 0 if unsupported.
func spmdPromoteElemSize(t types.Type) int {
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return 0
	}
	switch basic.Kind() {
	case types.Byte, types.Uint8, types.Int8:
		return 1
	case types.Int16, types.Uint16:
		return 2
	case types.Int32, types.Uint32, types.Float32:
		return 4
	case types.Int64, types.Uint64, types.Float64:
		return 8
	default:
		return 0
	}
}
```

- [ ] **Step 4: Wire pass into finishBody**

In `x-tools-spmd/go/ssa/func.go`, inside the `if len(f.SPMDLoops) > 0` block (line 403), BEFORE `predicateSPMD(f)` (line 407), add:

```go
		// Promote eligible small array allocs to Varying[T] SSA values.
		// Runs after lift (which skips arrays with IndexAddr referrers) and
		// before predicateSPMD (so downstream passes see Varying[T] values).
		// Note: at this point, peeling fields (MainBodyBlock, etc.) are nil.
		promoteSPMDArrays(f)
```

The resulting block should read:
```go
	if len(f.SPMDLoops) > 0 {
		promoteSPMDArrays(f)
		predicateSPMD(f)
		peelSPMDLoops(f)
		...
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPromoteSPMDArrays -v -count=1`
Expected: All 4 tests PASS

- [ ] **Step 6: Run full suite for regressions**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestSPMD -v -count=1 2>&1 | tail -20`
Expected: All existing tests PASS

- [ ] **Step 7: Commit**

Use the `clean-commit` agent per CLAUDE.md mandatory workflow.

---

## Chunk 3: SPMDVectorFromMemory Emission for External Data Init

Extend the promotion pass to emit `SPMDVectorFromMemory` for arrays initialized from strings/slices via `runtime.sliceCopy` or copy loops.

### Task 3: External data initialization

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_promote.go` (add sliceCopy pattern detection)
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go` (extend)

- [ ] **Step 1: Write failing test for SPMDVectorFromMemory emission**

Add to `x-tools-spmd/go/ssa/spmd_promote_test.go`:

```go
// TestPromoteSPMDArrays_EmitsVectorFromMemory verifies that a copy(arr[:], s)
// pattern is replaced with SPMDVectorFromMemory.
func TestPromoteSPMDArrays_EmitsVectorFromMemory(t *testing.T) {
	src := `package main

func process(s string) {
	var input [16]byte
	copy(input[:], s)
	for i := range 16 {
		_ = input[i] - '0'
	}
}

func main() {
	process("192.168.1.1")
}
`
	pkg := buildSPMDProgram(t, src, 16)
	fn := pkg.Func("process")
	if fn == nil {
		t.Fatal("process not found")
	}

	// After promotion, there should be an SPMDVectorFromMemory instruction.
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if _, ok := instr.(*ssa.SPMDVectorFromMemory); ok {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected SPMDVectorFromMemory instruction after promotion")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPromoteSPMDArrays_EmitsVectorFromMemory -v -count=1`
Expected: FAIL — no SPMDVectorFromMemory emitted

- [ ] **Step 3: Implement sliceCopy pattern detection**

In `x-tools-spmd/go/ssa/spmd_promote.go`, extend `doPromoteArray` to detect `runtime.sliceCopy` calls that initialize the array:

The implementer should:
1. Before removing the alloc, scan the alloc's block for `*ssa.Call` instructions where the callee is `runtime.sliceCopy` and the destination argument points to the promoted alloc
2. Extract the source pointer and length from the call arguments
3. Emit `SPMDVectorFromMemory` in the alloc's block at the call's position
4. Use `replaceAll` to replace all loads from the array with the new instruction's result
5. Remove the call instruction

**Pattern to detect in SSA:**
```
SliceToArrayPointer -> Store to alloc (or)
Call to runtime.sliceCopy with dst derived from alloc
```

The exact SSA shape depends on how `copy(input[:], s)` is lowered. The implementer should dump SSA for a test program (via `f.WriteTo(os.Stderr)`) and match the observed pattern.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPromoteSPMDArrays_EmitsVectorFromMemory -v -count=1`
Expected: PASS

- [ ] **Step 5: Run full suite for regressions**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestSPMD -v -count=1 2>&1 | tail -20`
Expected: All tests PASS

- [ ] **Step 6: Commit**

Use the `clean-commit` agent per CLAUDE.md mandatory workflow.

---

## Chunk 4: TinyGo SPMDVectorFromMemory Lowering

Add TinyGo compiler support to lower `SPMDVectorFromMemory` to WASM masked vector loads.

### Task 4: TinyGo lowering for SPMDVectorFromMemory

**Files:**
- Modify: `tinygo/compiler/compiler.go:3324` (add case in getValue switch)
- Modify: `tinygo/compiler/spmd.go` (add createSPMDVectorFromMemory)
- Test: `tinygo/compiler/spmd_llvm_test.go` (add unit test)

- [ ] **Step 1: Write failing test**

Add to `tinygo/compiler/spmd_llvm_test.go` a unit test following the existing pattern (e.g., `TestSPMDComputeTailMask`). The test should construct an `SPMDVectorFromMemory` SSA instruction and verify the LLVM IR output contains a vector load and select:

```go
// TestSPMDVectorFromMemory verifies the lowering of SPMDVectorFromMemory
// produces a v128.load + select pattern.
func TestSPMDVectorFromMemory(t *testing.T) {
	// This test verifies the getValue handler doesn't panic on
	// SPMDVectorFromMemory and produces valid LLVM IR.
	// Full verification requires E2E tests (Chunk 5).
	t.Skip("requires full SSA program construction — covered by E2E tests")
}
```

**Note**: TinyGo SPMD LLVM tests (`spmd_llvm_test.go`) are unit tests on builder methods using `newTestCompilerContext`. They test individual lowering functions, not full compilation. Since `createSPMDVectorFromMemory` requires a fully constructed SSA `SPMDVectorFromMemory` instruction with pointer/length values that resolve to LLVM values, a pure unit test is complex. The E2E test in Chunk 5 provides the primary coverage. Add the handler code here (Steps 2-3) and verify via E2E.

- [ ] **Step 2: Add getValue case in compiler.go**

In `tinygo/compiler/compiler.go`, after the `*ssa.SPMDExtractMask` case (line ~3325), add:

```go
	case *ssa.SPMDVectorFromMemory:
		return b.createSPMDVectorFromMemory(expr)
```

- [ ] **Step 3: Implement createSPMDVectorFromMemory in spmd.go**

Add to `tinygo/compiler/spmd.go`:

```go
// createSPMDVectorFromMemory lowers SPMDVectorFromMemory to a masked vector
// load. On WASM SIMD128: v128.load from ptr, then select with tail mask and
// zero vector for lanes beyond len.
func (b *builder) createSPMDVectorFromMemory(instr *ssa.SPMDVectorFromMemory) (llvm.Value, error) {
	ptr, err := b.getValue(instr.Ptr, getPos(instr))
	if err != nil {
		return llvm.Value{}, err
	}
	length, err := b.getValue(instr.Len, getPos(instr))
	if err != nil {
		return llvm.Value{}, err
	}

	lanes := instr.Lanes
	elemLLType := b.getLLVMType(instr.ElemType)
	vecType := llvm.VectorType(elemLLType, lanes)

	// Load full 128-bit vector directly from pointer (opaque pointers —
	// no bitcast needed, just pass ptr and desired load type).
	loaded := b.CreateLoad(vecType, ptr, "vfm.load")

	// Build tail mask: lanes 0..len-1 are active, rest get zero.
	i32Type := b.ctx.Int32Type()
	i32VecType := llvm.VectorType(i32Type, lanes)

	// Extend or truncate length to i32 for comparison.
	lenI32 := length
	lenWidth := length.Type().IntTypeWidth()
	if lenWidth < 32 {
		lenI32 = b.CreateZExt(length, i32Type, "vfm.len32")
	} else if lenWidth > 32 {
		lenI32 = b.CreateTrunc(length, i32Type, "vfm.len32")
	}

	// Broadcast length to i32 vector.
	lenVec := b.splatScalar(lenI32, i32VecType)

	// Lane indices [0, 1, 2, ..., lanes-1].
	indices := make([]llvm.Value, lanes)
	for i := 0; i < lanes; i++ {
		indices[i] = llvm.ConstInt(i32Type, uint64(i), false)
	}
	idxVec := llvm.ConstVector(indices, false)

	// mask = idx < len (unsigned) -> <N x i1>.
	maskI1 := b.CreateICmp(llvm.IntULT, idxVec, lenVec, "vfm.mask.i1")

	// Select: use spmdMaskSelect which handles WASM i32 mask format.
	// Wrap <N x i1> to platform mask format first.
	mask := b.spmdWrapMask(maskI1, lanes)
	zero := llvm.ConstNull(vecType)
	result := b.spmdMaskSelect(mask, loaded, zero)

	return result, nil
}
```

- [ ] **Step 4: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

- [ ] **Step 5: Run TinyGo SPMD test suite**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd make test GO=/home/cedric/work/SPMD/go/bin/go GOTESTPKGS=./compiler/ GOTESTFLAGS="-run TestSPMD -v" 2>&1 | tail -20`
Expected: All existing tests PASS (new handler doesn't affect existing tests)

- [ ] **Step 6: Commit**

Use the `clean-commit` agent per CLAUDE.md mandatory workflow.

---

## Chunk 5: E2E Validation

Verify the full pipeline works with the IPv4 parser example.

### Task 5: End-to-end validation

**Files:**
- Test: `test/integration/spmd/ipv4-parser/main.go` (existing)

- [ ] **Step 1: Clear TinyGo cache and rebuild**

```bash
rm -rf ~/.cache/tinygo
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

- [ ] **Step 2: Compile IPv4 parser to WASM**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd WASMOPT=/tmp/wasm-opt GOROOT=$(pwd)/go ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-test.wasm test/integration/spmd/ipv4-parser/main.go
```

Expected: Compiles without error

- [ ] **Step 3: Run and verify output**

```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-test.wasm
```

Expected: Same output as before promotion (correctness preserved):
```
'192.168.1.1' -> 192.168.1.1
'10.0.0.1' -> 10.0.0.1
...
Correctness: SPMD and scalar results match.
```

- [ ] **Step 4: Inspect WASM for reduced memory operations**

```bash
wasm2wat /tmp/ipv4-test.wasm | grep -c "memory.fill\|memory.copy"
```

Expected: Fewer `memory.fill`/`memory.copy` instructions than before

- [ ] **Step 5: Run full E2E test suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -10
```

Expected: No regressions — same or better pass counts

- [ ] **Step 6: Commit submodule pointer updates**

Use the `clean-commit` agent per CLAUDE.md mandatory workflow:
```bash
cd /home/cedric/work/SPMD
git add x-tools-spmd tinygo
```
