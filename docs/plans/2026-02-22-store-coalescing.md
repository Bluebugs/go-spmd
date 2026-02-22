# Store Coalescing Optimization Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Detect matching stores in both branches of varying if/else and emit `select(cond, thenVal, elseVal)` + one store instead of two masked stores.

**Architecture:** SSA pre-analysis pass in `spmd.go` runs alongside `preDetectVaryingIfs`, collecting store pairs into `spmdCoalescedStores` map. During LLVM codegen, the `*ssa.Store` handler in `compiler.go` checks this map: skips then-stores, emits select + single store for else-stores. Parent mask (before the if pushed its mask) is used for the coalesced store.

**Tech Stack:** Go, go/ssa, LLVM IR via TinyGo's go-llvm bindings

**Design Doc:** `docs/plans/2026-02-22-store-coalescing-design.md`

---

### Task 1: Add Data Structures and Address Matching

**Files:**
- Modify: `tinygo/compiler/spmd.go` (after `spmdContiguousInfo` struct at line ~838)

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDSameStoreAddr(t *testing.T) {
	// Test that spmdSameStoreAddr correctly identifies matching store destinations.

	// Case 1: identical SSA value → match
	// We can't easily construct real ssa.Values in a unit test without a full
	// SSA program, so we test via the integration test in Task 3.
	// This test verifies the function exists and handles nil gracefully.
	if spmdSameStoreAddr(nil, nil) {
		t.Error("nil values should not match")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDSameStoreAddr -v`
Expected: FAIL — `spmdSameStoreAddr` undefined

**Step 3: Write the data structures and address matching function**

Add to `tinygo/compiler/spmd.go` after `spmdContiguousInfo` (line ~838):

```go
// spmdCoalescedStore represents a pair of stores in then/else branches of a varying
// if/else that write to the same destination. Instead of two masked stores, codegen
// emits select(cond, thenVal, elseVal) + one store with the parent mask.
type spmdCoalescedStore struct {
	thenStore *ssa.Store     // store in then-branch (skipped during codegen)
	elseStore *ssa.Store     // store in else-branch (emits the coalesced store)
	ifInfo    *spmdVaryingIf // the varying if that contains them
}

// spmdSameStoreAddr checks whether two SSA values represent the same store destination.
// Returns true if they are the same SSA value, or both are *ssa.IndexAddr with the
// same base (.X) and index (.Index).
func spmdSameStoreAddr(a, b ssa.Value) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b {
		return true
	}
	idxA, okA := a.(*ssa.IndexAddr)
	idxB, okB := b.(*ssa.IndexAddr)
	if okA && okB && idxA.X == idxB.X && idxA.Index == idxB.Index {
		return true
	}
	return false
}
```

Add the map field to the `builder` struct in `compiler.go` (near line ~188, after `spmdContiguousPtr`):

```go
spmdCoalescedStores   map[*ssa.Store]*spmdCoalescedStore  // store → coalescing info (then/else pairs)
```

Initialize it in `createFunction()` (near line ~1485, after `spmdContiguousPtr` init):

```go
b.spmdCoalescedStores = make(map[*ssa.Store]*spmdCoalescedStore)
```

**Step 4: Run test to verify it passes**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDSameStoreAddr -v`
Expected: PASS

**Step 5: Commit**

```bash
cd tinygo && git add compiler/spmd.go compiler/compiler.go compiler/spmd_llvm_test.go
git commit -m "feat: add store coalescing data structures and address matching"
```

---

### Task 2: Add SSA Pre-Analysis for Store Coalescing

**Files:**
- Modify: `tinygo/compiler/spmd.go` (new function `spmdAnalyzeCoalescedStores`, call from `preDetectVaryingIfs`)

**Step 1: Write the analysis function**

Add to `tinygo/compiler/spmd.go` after the `spmdSameStoreAddr` function:

```go
// spmdCollectBranchStores collects all *ssa.Store instructions reachable from
// entryBlock without crossing the merge block or the barrier (if-block).
// Returns a map from store destination (Addr) to the last store to that address.
func (b *builder) spmdCollectBranchStores(entryBlock, merge, barrier *ssa.BasicBlock) map[ssa.Value]*ssa.Store {
	stores := make(map[ssa.Value]*ssa.Store)
	visited := make(map[int]bool)

	var walk func(*ssa.BasicBlock)
	walk = func(block *ssa.BasicBlock) {
		if block == nil || block == merge || block == barrier || visited[block.Index] {
			return
		}
		visited[block.Index] = true
		for _, instr := range block.Instrs {
			if store, ok := instr.(*ssa.Store); ok {
				// Last-store-wins: overwrite previous store to same addr.
				// Use the raw Addr SSA value as key; spmdSameStoreAddr is
				// used later for cross-branch matching.
				stores[store.Addr] = store
			}
		}
		for _, succ := range block.Succs {
			walk(succ)
		}
	}
	walk(entryBlock)
	return stores
}

// spmdAnalyzeCoalescedStores detects matching stores in the then and else branches
// of a varying if/else and records them in spmdCoalescedStores for codegen.
func (b *builder) spmdAnalyzeCoalescedStores(info *spmdVaryingIf) {
	if !info.hasElse {
		return // No else branch → nothing to coalesce
	}

	ifBlock := b.fn.Blocks[info.ifBlockIndex]
	thenEntry := b.fn.Blocks[info.thenEntryIndex]
	elseEntry := b.fn.Blocks[info.elseEntryIndex]
	merge := b.fn.Blocks[info.mergeIndex]

	thenStores := b.spmdCollectBranchStores(thenEntry, merge, ifBlock)
	elseStores := b.spmdCollectBranchStores(elseEntry, merge, ifBlock)

	// Match then-stores to else-stores by destination address.
	for thenAddr, thenStore := range thenStores {
		for elseAddr, elseStore := range elseStores {
			if spmdSameStoreAddr(thenAddr, elseAddr) {
				// Verify both values have the same type.
				if thenStore.Val.Type() != elseStore.Val.Type() {
					continue
				}
				coal := &spmdCoalescedStore{
					thenStore: thenStore,
					elseStore: elseStore,
					ifInfo:    info,
				}
				b.spmdCoalescedStores[thenStore] = coal
				b.spmdCoalescedStores[elseStore] = coal
				break // One match per then-store
			}
		}
	}
}
```

**Step 2: Call the analysis from `preDetectVaryingIfs`**

In `preDetectVaryingIfs()` (spmd.go line ~912), after the call to `b.spmdAnalyzeVaryingIf(block)`, add:

```go
		if _, ok := ifInstr.Cond.Type().(*types.SPMDType); ok {
			b.spmdAnalyzeVaryingIf(block)
			// Detect matching stores in then/else branches for coalescing.
			if info, ok := b.spmdVaryingIfs[block.Index]; ok {
				b.spmdAnalyzeCoalescedStores(info)
			}
		}
```

Note: This replaces the existing `b.spmdAnalyzeVaryingIf(block)` call — wrap it to also call coalescing.

**Step 3: Run existing tests to verify no regressions**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All existing SPMD tests pass (the analysis populates a map but codegen doesn't use it yet)

**Step 4: Commit**

```bash
cd tinygo && git add compiler/spmd.go
git commit -m "feat: add SSA pre-analysis for store coalescing detection"
```

---

### Task 3: Add LLVM Integration Test for Store Coalescing

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the failing LLVM IR test**

This test constructs a mock SPMD varying if/else with stores to the same contiguous address in both branches, runs the coalescing codegen path, and verifies the output IR.

```go
func TestSPMDStoreCoalescing(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, laneCount)
	maskType := llvm.VectorType(c.ctx.Int32Type(), laneCount) // WASM i32 mask

	// Create array alloca + contiguous GEP (simulating d[i])
	arrType := llvm.ArrayType(i32Type, 16)
	arrPtr := b.CreateAlloca(arrType, "d")
	zero := llvm.ConstInt(i32Type, 0, false)
	scalarPtr := b.CreateInBoundsGEP(arrType, arrPtr, []llvm.Value{zero, zero}, "d.ptr")

	// Create condition, then-value, else-value
	cond := llvm.ConstAllOnes(maskType)              // all-true condition (for testing)
	thenVal := llvm.ConstNull(vecType)               // then: store zeros
	elseVal := llvm.ConstInt(vecType.ElementType(), 42, false)
	elseVec := b.CreateVectorSplat(laneCount, elseVal, "else.splat")

	// Create parent mask (all-ones = top-level go-for body)
	parentMask := llvm.ConstAllOnes(maskType)

	// Emit the coalesced store: select(cond, thenVal, elseVal) + single store
	selected := b.spmdMaskSelect(cond, thenVal, elseVec)
	b.spmdMaskedStore(selected, scalarPtr, parentMask)

	// Verify: exactly one llvm.masked.store call in the module
	maskedStoreFn := c.mod.NamedFunction("llvm.masked.store.v4i32.p0")
	if maskedStoreFn.IsNil() {
		t.Fatal("expected llvm.masked.store.v4i32.p0 to be declared")
	}

	// Verify the function has instructions (basic sanity)
	fn := c.mod.FirstFunction()
	for !fn.IsNil() {
		if fn.Name() == "test_func" {
			bb := fn.FirstBasicBlock()
			instrCount := 0
			for instr := bb.FirstInstruction(); !instr.IsNil(); instr = llvm.NextInstruction(instr) {
				instrCount++
			}
			// Should have: alloca, GEP, splat, select/bitwise-ops, masked-store, terminator
			if instrCount < 3 {
				t.Errorf("expected at least 3 instructions, got %d", instrCount)
			}
			break
		}
		fn = llvm.NextFunction(fn)
	}
}
```

**Step 2: Run test to verify it passes**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDStoreCoalescing -v`
Expected: PASS (this test directly calls the LLVM emit functions, verifying the select+store pattern works)

**Step 3: Commit**

```bash
cd tinygo && git add compiler/spmd_llvm_test.go
git commit -m "test: add LLVM integration test for store coalescing"
```

---

### Task 4: Add Codegen for Store Coalescing

**Files:**
- Modify: `tinygo/compiler/compiler.go` (the `*ssa.Store` case at line ~2088)
- Modify: `tinygo/compiler/spmd.go` (add `spmdParentMask` helper)

**Step 1: Add the parent mask helper**

In `spmd.go`, after `spmdCurrentMask()` (line ~765):

```go
// spmdParentMask returns the mask one level below the top of the stack.
// This is the mask that was active before the current varying if pushed its mask.
// Returns nil Value if the stack has fewer than 2 elements.
func (b *builder) spmdParentMask() llvm.Value {
	if len(b.spmdMaskStack) >= 2 {
		return b.spmdMaskStack[len(b.spmdMaskStack)-2]
	}
	return llvm.Value{}
}
```

**Step 2: Modify the `*ssa.Store` handler**

In `compiler.go`, at the start of the `*ssa.Store` case (line ~2088), add coalescing check before the existing contiguous/scatter logic:

```go
	case *ssa.Store:
		// SPMD: check for coalesced store (matching stores in then/else branches).
		if b.spmdCoalescedStores != nil {
			if coal, ok := b.spmdCoalescedStores[instr]; ok {
				if instr == coal.thenStore {
					// Then-store: skip emission. The else-store will emit the coalesced store.
					return
				}
				if instr == coal.elseStore {
					// Else-store: emit select(cond, thenVal, elseVal) + single store.
					thenVal := b.getValue(coal.thenStore.Val, getPos(instr))
					elseVal := b.getValue(coal.elseStore.Val, getPos(instr))

					// Broadcast scalars to vectors if needed.
					thenVal, elseVal = b.spmdBroadcastMatch(thenVal, elseVal)

					// Select using the varying if condition.
					cond := coal.ifInfo.cond
					selected := b.spmdMaskSelect(cond, thenVal, elseVal)

					// Use parent mask (before the varying if pushed its mask).
					parentMask := b.spmdParentMask()
					if parentMask.IsNil() {
						// Fallback: if no parent mask, use all-ones.
						laneCount := selected.Type().VectorSize()
						parentMask = llvm.ConstAllOnes(llvm.VectorType(b.spmdMaskElemType(), laneCount))
					}

					// Emit single store with parent mask.
					llvmAddr := b.getValue(instr.Addr, getPos(instr))
					if b.spmdContiguousPtr != nil {
						if ci, ok := b.spmdContiguousPtr[instr.Addr]; ok {
							b.spmdMaskedStore(selected, ci.scalarPtr, parentMask)
							return
						}
					}
					if llvmAddr.Type().TypeKind() == llvm.VectorTypeKind {
						b.spmdMaskedScatter(selected, llvmAddr, parentMask)
						return
					}
					// Fallback: plain store (non-SPMD address).
					b.CreateStore(selected, llvmAddr)
					return
				}
			}
		}

		// ... existing Store handling follows unchanged ...
```

**Step 3: Run all SPMD tests**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All tests pass

**Step 4: Commit**

```bash
cd tinygo && git add compiler/compiler.go compiler/spmd.go
git commit -m "feat: implement store coalescing codegen for varying if/else"
```

---

### Task 5: Add Comprehensive LLVM IR Tests

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write tests for the SSA analysis path**

These tests construct real go/ssa programs with varying if/else stores and verify the analysis detects them correctly. Follow the pattern from existing tests like `TestSPMDAnalyzeSPMDLoops`.

Tests to add:

1. `TestSPMDStoreCoalescingAnalysis` — Verify `spmdAnalyzeCoalescedStores` populates `spmdCoalescedStores` for matching IndexAddr stores
2. `TestSPMDStoreCoalescingNoElse` — Verify no coalescing when there's no else branch
3. `TestSPMDStoreCoalescingTypeMismatch` — Verify no coalescing when stored value types differ
4. `TestSPMDStoreCoalescingPartialMatch` — Verify only matching stores are coalesced, non-matching remain

**Step 2: Run tests**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDStoreCoalescing -v`
Expected: All pass

**Step 3: Commit**

```bash
cd tinygo && git add compiler/spmd_llvm_test.go
git commit -m "test: add comprehensive store coalescing analysis tests"
```

---

### Task 6: E2E Test with Real SPMD Code

**Files:**
- Create: `examples/store-coalescing/main.go`
- Modify: `test/e2e/spmd-e2e-test.sh` (add new example to test list)

**Step 1: Write the example**

```go
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	// Test 1: Simple if/else store coalescing
	var data [16]int
	go for i := range 16 {
		if lanes.Varying[int](i) % 2 == 0 {
			data[i] = 100
		} else {
			data[i] = 200
		}
	}
	fmt.Printf("Test 1: %v\n", data)

	// Test 2: Nested if/else
	var result [16]int
	go for i := range 16 {
		v := lanes.Varying[int](i)
		if v < 4 {
			result[i] = 1
		} else if v < 8 {
			result[i] = 2
		} else {
			result[i] = 3
		}
	}
	fmt.Printf("Test 2: %v\n", result)

	// Test 3: Store coalescing with computation
	var computed [16]int
	go for i := range 16 {
		v := lanes.Varying[int](i)
		if v % 3 == 0 {
			computed[i] = v * 10
		} else {
			computed[i] = v * 20
		}
	}
	fmt.Printf("Test 3: %v\n", computed)
}
```

**Step 2: Compile and run**

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./build/tinygo build -target=wasi -scheduler=none -o /tmp/store-coalescing.wasm \
  examples/store-coalescing/main.go

node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/store-coalescing.wasm
```

Expected: Output matches serial execution (correct values per lane)

**Step 3: Verify SIMD instructions**

```bash
wasm2wat /tmp/store-coalescing.wasm | grep -c "v128"
```

Expected: Non-zero count of v128 instructions

**Step 4: Add to E2E test script**

Add `store-coalescing` to the appropriate test level in `test/e2e/spmd-e2e-test.sh`.

**Step 5: Commit**

```bash
git add examples/store-coalescing/main.go test/e2e/spmd-e2e-test.sh
cd tinygo && git add -A
git commit -m "test: add store coalescing E2E example"
```

---

### Task 7: Verify No Regressions and Run Full Test Suite

**Files:** None (verification only)

**Step 1: Run all TinyGo SPMD tests**

```bash
cd tinygo && go test ./compiler/ -run TestSPMD -v -count=1
```

Expected: All 73+ tests pass

**Step 2: Run the full E2E test suite**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: All previously passing tests still pass; store-coalescing example passes

**Step 3: Check mandelbrot still works**

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./build/tinygo build -target=wasi -scheduler=none -o /tmp/mandelbrot.wasm \
  examples/mandelbrot/main.go

node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/mandelbrot.wasm
```

Expected: Same output as before, 0 differences vs serial

**Step 4: Commit any fixes if needed, then final verification commit**

```bash
git commit -m "chore: verify store coalescing passes full test suite"
```

---

### Task 8: Update Documentation

**Files:**
- Modify: `CLAUDE.md` (update Phase 2 status)
- Modify: `docs/plans/2026-02-22-store-coalescing-design.md` (update status to Implemented)

**Step 1: Update CLAUDE.md**

Add to the Phase 2.9 section:
- **Phase 2.9d**: Store coalescing — matching stores in varying if/else branches coalesced into select + single store

**Step 2: Update design doc status**

Change `**Status:** Design` to `**Status:** Implemented`

**Step 3: Commit**

```bash
git add CLAUDE.md docs/plans/2026-02-22-store-coalescing-design.md
git commit -m "docs: update status for store coalescing optimization (Phase 2.9d)"
```
