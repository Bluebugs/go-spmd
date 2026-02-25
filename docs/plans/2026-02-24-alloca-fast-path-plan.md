# Alloca Fast-Path for SPMD Full Load/Store — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate the runtime cap-check branch for stack-allocated arrays by detecting `*ssa.Alloc` with `Heap == false` and emitting full load+select or load-blend-store directly.

**Architecture:** Add `spmdIsAllocaOrigin` helper that traces IndexAddr.X back to `*ssa.Alloc`. Store `ssaSource` in `spmdContiguousInfo`. Add fast-path before cap check in both `spmdFullLoadWithSelect` and `spmdFullStoreWithBlend`.

**Tech Stack:** TinyGo compiler (Go), Go SSA tracing, LLVM IR generation via go-llvm bindings, WASM SIMD128 target.

**Design doc:** `docs/plans/2026-02-24-alloca-fast-path-design.md`

**Prerequisite:** Cap-based full-load plan (Tasks 1-4) and cap-based full-store plan (Tasks 1-3) must be implemented first.

---

### Task 1: Add ssaSource field to spmdContiguousInfo

**Files:**
- Modify: `tinygo/compiler/spmd.go:1207-1210` (struct definition)
- Modify: `tinygo/compiler/spmd.go:3664-3695` (spmdContiguousIndexAddrCore — store expr.X)

**Step 1: Add ssaSource field and store it at both array and slice paths**

In `tinygo/compiler/spmd.go`, modify the struct at line 1207:

```go
type spmdContiguousInfo struct {
	scalarPtr llvm.Value      // scalar GEP result (base of contiguous access)
	loop      *spmdActiveLoop // owning loop (for lane count)
	sliceCap  llvm.Value      // cap of source slice (zero value for arrays/strings)
	ssaSource ssa.Value       // original IndexAddr.X for alloca origin tracing
}
```

In `spmdContiguousIndexAddrCore`, store `expr.X` in both the array and slice paths. The array path at line 3691-3692 becomes:

```go
	if b.spmdContiguousPtr != nil {
		b.spmdContiguousPtr[expr] = &spmdContiguousInfo{scalarPtr: ptr, loop: loop, ssaSource: expr.X}
	}
```

And the slice path (already modified by the load optimization to set sliceCap) adds ssaSource:

```go
	case *types.Slice:
		bufptr := b.CreateExtractValue(val, 0, "indexaddr.ptr")
		sliceCap := b.CreateExtractValue(val, 2, "indexaddr.cap")
		bufType := b.getLLVMType(ptrTyp.Elem())
		ptr = b.CreateInBoundsGEP(bufType, bufptr, []llvm.Value{scalarIndex}, "spmd.contiguous.ptr")
		if b.spmdContiguousPtr != nil {
			b.spmdContiguousPtr[expr] = &spmdContiguousInfo{scalarPtr: ptr, loop: loop, sliceCap: sliceCap, ssaSource: expr.X}
		}
		return ptr, nil
```

**Step 2: Build and run tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: Build succeeds, all tests pass (ssaSource is not used yet).

**Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go && git commit -m "feat: add ssaSource field to spmdContiguousInfo for alloca origin tracing"
```

---

### Task 2: Implement spmdIsAllocaOrigin helper

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add new function)
- Test: `tinygo/compiler/spmd_test.go`

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_test.go`:

```go
// TestSPMDIsAllocaOrigin verifies that spmdIsAllocaOrigin correctly identifies
// stack-allocated arrays and rejects heap allocations and small arrays.
func TestSPMDIsAllocaOrigin(t *testing.T) {
	// This test operates on go/ssa values, not LLVM IR.
	// We need to construct minimal SSA Alloc values.

	// Create a types.Array of length 16 (enough for 4-lane i32 or 16-lane i8).
	i32Type := types.Typ[types.Int32]
	arrType16 := types.NewArray(i32Type, 16)
	ptrToArr16 := types.NewPointer(arrType16)

	// Stack alloc (Heap=false) with array length 16.
	stackAlloc := &ssa.Alloc{}
	stackAlloc.SetType(ptrToArr16)
	// Note: ssa.Alloc.Heap defaults to false (stack).

	if !spmdIsAllocaOriginStatic(stackAlloc, 4) {
		t.Error("stack alloc with array[16] should be detected as alloca for laneCount=4")
	}
	if !spmdIsAllocaOriginStatic(stackAlloc, 16) {
		t.Error("stack alloc with array[16] should be detected as alloca for laneCount=16")
	}

	// Heap alloc.
	heapAlloc := &ssa.Alloc{Heap: true}
	heapAlloc.SetType(ptrToArr16)

	if spmdIsAllocaOriginStatic(heapAlloc, 4) {
		t.Error("heap alloc should NOT be detected as alloca")
	}

	// Small array (length 2, less than laneCount=4).
	arrType2 := types.NewArray(i32Type, 2)
	ptrToArr2 := types.NewPointer(arrType2)
	smallAlloc := &ssa.Alloc{}
	smallAlloc.SetType(ptrToArr2)

	if spmdIsAllocaOriginStatic(smallAlloc, 4) {
		t.Error("stack alloc with array[2] should NOT be detected as alloca for laneCount=4")
	}

	// Non-Alloc value (e.g., function parameter) should return false.
	if spmdIsAllocaOriginStatic(nil, 4) {
		t.Error("nil SSA value should NOT be detected as alloca")
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDIsAllocaOrigin' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — `spmdIsAllocaOriginStatic` doesn't exist yet.

**Step 3: Implement spmdIsAllocaOriginStatic**

Add to `tinygo/compiler/spmd.go`:

```go
// spmdIsAllocaOriginStatic checks if an SSA value is a stack-allocated array
// (*ssa.Alloc with Heap=false) large enough for a full vector operation.
// This is a static analysis helper that does not require a builder context.
// Returns true only when it can prove the pointer is stack-allocated with
// array length >= laneCount. Conservative: returns false for anything uncertain.
func spmdIsAllocaOriginStatic(ssaVal ssa.Value, laneCount int) bool {
	if ssaVal == nil {
		return false
	}
	alloc, ok := ssaVal.(*ssa.Alloc)
	if !ok || alloc.Heap {
		return false
	}
	ptrType, ok := alloc.Type().Underlying().(*types.Pointer)
	if !ok {
		return false
	}
	arrType, ok := ptrType.Elem().Underlying().(*types.Array)
	if !ok {
		return false
	}
	return arrType.Len() >= int64(laneCount)
}

// spmdIsAllocaOrigin checks if the source of a contiguous SPMD access is a
// stack-allocated array large enough for a full vector load/store. When true,
// the full load+select or load-blend-store can be emitted without any runtime
// cap check — the stack frame memory is always fully accessible.
func (b *builder) spmdIsAllocaOrigin(ci *spmdContiguousInfo) bool {
	if ci.ssaSource == nil {
		return false
	}
	return spmdIsAllocaOriginStatic(ci.ssaSource, ci.loop.laneCount)
}
```

**Step 4: Run the test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDIsAllocaOrigin' -count=1 -v 2>&1 | tail -10
```
Expected: PASS.

**Step 5: Run all SPMD tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All pass.

**Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_test.go && git commit -m "feat: implement spmdIsAllocaOrigin for stack-allocated array detection"
```

---

### Task 3: Add alloca fast-path to spmdFullLoadWithSelect

**Files:**
- Modify: `tinygo/compiler/spmd.go` (spmdFullLoadWithSelect)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDAllocaFastPathLoad verifies that when the source is a stack-allocated
// array (alloca origin), spmdFullLoadWithSelect emits a plain load+select
// without any runtime cap-check branch.
func TestSPMDAllocaFastPathLoad(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, laneCount)

	scalarIter := llvm.ConstInt(b.uintptrType, 0, false)

	// Create a pointer (simulating an alloca).
	arrType := llvm.ArrayType(i32Type, 16)
	alloca := b.CreateAlloca(arrType, "test.buf")
	zero := llvm.ConstInt(i32Type, 0, false)
	scalarPtr := b.CreateInBoundsGEP(arrType, alloca, []llvm.Value{zero, zero}, "test.ptr")

	// Partial mask.
	maskElemType := i32Type
	mask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
	}, false)

	// Create a mock SSA Alloc (stack, array[16]).
	goI32 := types.Typ[types.Int32]
	goArrType := types.NewArray(goI32, 16)
	goPtrType := types.NewPointer(goArrType)
	mockAlloc := &ssa.Alloc{}
	mockAlloc.SetType(goPtrType)

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		ssaSource: mockAlloc,
		// sliceCap is nil — this is an array, not a slice.
	}

	mergeBB := b.insertBasicBlock("test.after")
	result := b.spmdFullLoadWithSelect(vecType, ci, mask)
	b.CreateBr(mergeBB)
	b.SetInsertPointAtEnd(mergeBB)
	b.CreateRetVoid()

	if result.IsNil() {
		t.Fatal("spmdFullLoadWithSelect returned nil for alloca fast-path")
	}

	// The alloca fast-path should NOT emit a cap-check branch.
	ir := c.mod.String()
	if strings.Contains(ir, "spmd.can.fullload") {
		t.Error("alloca fast-path should NOT emit spmd.can.fullload cap check")
	}
	if strings.Contains(ir, "spmd.maskedload") {
		t.Error("alloca fast-path should NOT emit spmd.maskedload fallback block")
	}
	// Should still have a plain vector load.
	if !strings.Contains(ir, "spmd.alloca.load") {
		t.Error("alloca fast-path should emit spmd.alloca.load")
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDAllocaFastPathLoad' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — the alloca fast-path is not yet in `spmdFullLoadWithSelect`.

**Step 3: Add alloca fast-path to spmdFullLoadWithSelect**

In `tinygo/compiler/spmd.go`, at the top of `spmdFullLoadWithSelect` (before the cap check logic):

```go
func (b *builder) spmdFullLoadWithSelect(vecType llvm.Type, ci *spmdContiguousInfo, mask llvm.Value) llvm.Value {
	// Fast path: alloca origin — stack memory is always fully accessible.
	// No runtime branch needed.
	if b.spmdIsAllocaOrigin(ci) {
		raw := b.CreateLoad(vecType, ci.scalarPtr, "spmd.alloca.load")
		return b.spmdMaskSelect(mask, raw, llvm.ConstNull(vecType))
	}

	// ... existing cap-check code ...
```

**Step 4: Run the test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDAllocaFastPathLoad' -count=1 -v 2>&1 | tail -10
```
Expected: PASS.

**Step 5: Run all SPMD tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All pass.

**Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_llvm_test.go && git commit -m "feat: add alloca fast-path to spmdFullLoadWithSelect — no branch for stack arrays"
```

---

### Task 4: Add alloca fast-path to spmdFullStoreWithBlend

**Files:**
- Modify: `tinygo/compiler/spmd.go` (spmdFullStoreWithBlend)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDAllocaFastPathStore verifies that when the source is a stack-allocated
// array (alloca origin), spmdFullStoreWithBlend emits a load-blend-store
// without any runtime cap-check branch.
func TestSPMDAllocaFastPathStore(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, laneCount)

	scalarIter := llvm.ConstInt(b.uintptrType, 0, false)

	arrType := llvm.ArrayType(i32Type, 16)
	alloca := b.CreateAlloca(arrType, "test.buf")
	zero := llvm.ConstInt(i32Type, 0, false)
	scalarPtr := b.CreateInBoundsGEP(arrType, alloca, []llvm.Value{zero, zero}, "test.ptr")

	maskElemType := i32Type
	mask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
	}, false)

	storeVal := llvm.ConstNull(vecType)

	goI32 := types.Typ[types.Int32]
	goArrType := types.NewArray(goI32, 16)
	goPtrType := types.NewPointer(goArrType)
	mockAlloc := &ssa.Alloc{}
	mockAlloc.SetType(goPtrType)

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		ssaSource: mockAlloc,
	}

	b.spmdFullStoreWithBlend(storeVal, ci, mask)
	b.CreateRetVoid()

	ir := c.mod.String()
	if strings.Contains(ir, "spmd.can.fullstore") {
		t.Error("alloca fast-path should NOT emit spmd.can.fullstore cap check")
	}
	if strings.Contains(ir, "spmd.maskedstore") {
		t.Error("alloca fast-path should NOT emit spmd.maskedstore fallback block")
	}
	if !strings.Contains(ir, "spmd.alloca.old") {
		t.Error("alloca fast-path should emit spmd.alloca.old load for blend")
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDAllocaFastPathStore' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — alloca fast-path not yet in `spmdFullStoreWithBlend`.

**Step 3: Add alloca fast-path to spmdFullStoreWithBlend**

In `tinygo/compiler/spmd.go`, at the top of `spmdFullStoreWithBlend`:

```go
func (b *builder) spmdFullStoreWithBlend(val llvm.Value, ci *spmdContiguousInfo, mask llvm.Value) {
	vecType := val.Type()

	// Fast path: alloca origin — stack memory is always fully accessible.
	// No runtime branch needed. Emit load-blend-store directly.
	if b.spmdIsAllocaOrigin(ci) {
		old := b.CreateLoad(vecType, ci.scalarPtr, "spmd.alloca.old")
		blended := b.spmdMaskSelect(mask, val, old)
		b.CreateStore(blended, ci.scalarPtr)
		return
	}

	// ... existing cap-check code ...
```

**Step 4: Run the test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDAllocaFastPathStore' -count=1 -v 2>&1 | tail -10
```
Expected: PASS.

**Step 5: Run all SPMD tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All pass.

**Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_llvm_test.go && git commit -m "feat: add alloca fast-path to spmdFullStoreWithBlend — no branch for stack arrays"
```

---

### Task 5: Wire alloca fast-path at load/store call sites for arrays

**Files:**
- Modify: `tinygo/compiler/compiler.go:4708-4717` (load call site)
- Modify: `tinygo/compiler/compiler.go:2441-2456` (regular store call site)
- Modify: `tinygo/compiler/compiler.go:2403-2408` (coalesced store call site)

**Step 1: Update load call site**

In `tinygo/compiler/compiler.go`, the contiguous load path at line 4708. Currently the cap-based optimization checks `!ci.sliceCap.IsNil()`. For the alloca fast-path, arrays have nil sliceCap but have ssaSource. Update the condition:

```go
		if b.spmdContiguousPtr != nil {
			if ci, ok := b.spmdContiguousPtr[unop.X]; ok {
				elemType := b.getLLVMType(unop.X.Type().Underlying().(*types.Pointer).Elem())
				vecType := llvm.VectorType(elemType, ci.loop.laneCount)
				mask := b.spmdCurrentMask()
				if mask.IsNil() {
					mask = llvm.ConstAllOnes(llvm.VectorType(b.spmdMaskElemType(ci.loop.laneCount), ci.loop.laneCount))
				}
				// Cap-based or alloca-based optimization: use full load + select when safe.
				if !b.spmdIsConstAllOnesMask(mask) && (b.spmdIsAllocaOrigin(ci) || !ci.sliceCap.IsNil()) {
					return b.spmdFullLoadWithSelect(vecType, ci, mask), nil
				}
				return b.spmdMaskedLoad(vecType, ci.scalarPtr, mask), nil
			}
		}
```

**Step 2: Update regular store call site**

Similarly at line 2441-2456:

```go
				// Cap-based or alloca-based optimization: use load-blend-store when safe.
				if !b.spmdIsConstAllOnesMask(mask) && (b.spmdIsAllocaOrigin(ci) || !ci.sliceCap.IsNil()) {
					b.spmdFullStoreWithBlend(llvmVal, ci, mask)
				} else {
					b.spmdMaskedStore(llvmVal, ci.scalarPtr, mask)
				}
```

**Step 3: Update coalesced store call site**

Similarly at line 2403-2408:

```go
						if !b.spmdIsConstAllOnesMask(parentMask) && (b.spmdIsAllocaOrigin(ci) || !ci.sliceCap.IsNil()) {
							b.spmdFullStoreWithBlend(selected, ci, parentMask)
						} else {
							b.spmdMaskedStore(selected, ci.scalarPtr, parentMask)
						}
```

**Step 4: Build and run all tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: Build succeeds, all tests pass.

**Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/compiler.go && git commit -m "feat: wire alloca fast-path at all contiguous load/store call sites"
```

---

### Task 6: E2E validation

**Files:**
- No code changes — validation only

**Step 1: Build and run hex-encode**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -o /tmp/hex-encode-simd.wasm examples/hex-encode/main.go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-simd.wasm 2>&1 | head -20
```
Expected: Correct output, no regressions.

**Step 2: Build and run mandelbrot**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -o /tmp/mandelbrot-simd.wasm examples/mandelbrot/main.go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/mandelbrot-simd.wasm 2>&1 | tail -10
```
Expected: 0 differences, speedup reported.

**Step 3: Run full E2E test suite**

Run:
```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -30
```
Expected: No regressions from existing pass counts (20 run pass, 4 compile-only, 10 reject OK).

---

## Summary

| Task | Description | Key Files |
|------|-------------|-----------|
| 1 | Add ssaSource to spmdContiguousInfo | spmd.go:1207, spmd.go:3664 |
| 2 | Implement spmdIsAllocaOrigin | spmd.go (new func), spmd_test.go |
| 3 | Alloca fast-path in load | spmd.go (spmdFullLoadWithSelect), spmd_llvm_test.go |
| 4 | Alloca fast-path in store | spmd.go (spmdFullStoreWithBlend), spmd_llvm_test.go |
| 5 | Wire at all call sites | compiler.go (3 call sites) |
| 6 | E2E validation | (no code changes) |

**Total: 6 tasks, ~4 commits of code + 1 validation task.**

**Execution order**: Load plan → Store plan → This alloca plan. The alloca plan adds to functions created by the first two plans.
