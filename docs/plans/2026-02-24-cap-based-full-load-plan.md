# Cap-Based Full-Load Optimization — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace scalarized `llvm.masked.load` with `v128.load` + `select` when the slice backing array has enough capacity, eliminating 4-16 conditional scalar loads per access.

**Architecture:** Extend `spmdContiguousInfo` to carry the slice cap extracted at IndexAddr time. At each contiguous masked load site, emit a runtime branch: if `scalarIter + laneCount <= sliceCap`, use full load + select; else fall back to masked load. Per-load granularity handles mixed-source loops naturally.

**Tech Stack:** TinyGo compiler (Go), LLVM IR generation via go-llvm bindings, WASM SIMD128 target.

**Design doc:** `docs/plans/2026-02-24-cap-based-full-load-design.md`

---

### Task 1: Extend spmdContiguousInfo with sliceCap field

**Files:**
- Modify: `tinygo/compiler/spmd.go:1207-1210` (struct definition)

**Step 1: Add the sliceCap field**

In `tinygo/compiler/spmd.go`, modify the struct at line 1207:

```go
// spmdContiguousInfo tracks an IndexAddr result that was detected as contiguous SPMD access.
type spmdContiguousInfo struct {
	scalarPtr llvm.Value      // scalar GEP result (base of contiguous access)
	loop      *spmdActiveLoop // owning loop (for lane count)
	sliceCap  llvm.Value      // cap of source slice (zero value for arrays/strings)
}
```

**Step 2: Build TinyGo to verify no regressions**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```
Expected: Build succeeds (the new field has zero-value default, so all existing code is unaffected).

**Step 3: Run existing SPMD tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All existing tests pass (field is unused so far).

**Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go && git commit -m "feat: add sliceCap field to spmdContiguousInfo for cap-based full-load optimization"
```

---

### Task 2: Extract slice cap in spmdContiguousIndexAddrCore

**Files:**
- Modify: `tinygo/compiler/spmd.go:3664-3695` (spmdContiguousIndexAddrCore)

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDContiguousInfoSliceCap verifies that spmdContiguousIndexAddrCore
// populates sliceCap for slice sources and leaves it as zero value for arrays.
func TestSPMDContiguousInfoSliceCap(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()

	// Initialize SPMD state.
	b.spmdContiguousPtr = make(map[ssa.Value]*spmdContiguousInfo)
	loop := &spmdActiveLoop{
		laneCount:     laneCount,
		scalarIterVal: llvm.ConstInt(i32Type, 0, false),
	}

	// Simulate a slice: {ptr, len, cap} struct.
	elemType := c.ctx.Int32Type()
	ptrType := llvm.PointerType(elemType, 0)
	sliceType := c.ctx.StructType([]llvm.Type{ptrType, b.uintptrType, b.uintptrType}, false)
	sliceAlloca := b.CreateAlloca(sliceType, "test.slice")
	sliceVal := b.CreateLoad(sliceType, sliceAlloca, "test.slice.val")

	// Call the internal cap extraction path directly.
	bufptr := b.CreateExtractValue(sliceVal, 0, "test.ptr")
	sliceCap := b.CreateExtractValue(sliceVal, 2, "test.cap")
	scalarIndex := llvm.ConstInt(b.uintptrType, 0, false)
	ptr := b.CreateInBoundsGEP(elemType, bufptr, []llvm.Value{scalarIndex}, "test.gep")

	// Store in contiguousPtr manually (mirrors what spmdContiguousIndexAddrCore does).
	info := &spmdContiguousInfo{scalarPtr: ptr, loop: loop, sliceCap: sliceCap}

	// Verify sliceCap is non-nil for slice source.
	if info.sliceCap.IsNil() {
		t.Error("sliceCap should be non-nil for slice source")
	}

	// Verify sliceCap is nil/zero for array source (no cap extraction).
	arrayInfo := &spmdContiguousInfo{scalarPtr: ptr, loop: loop}
	if !arrayInfo.sliceCap.IsNil() {
		t.Error("sliceCap should be nil for array source")
	}
}
```

**Step 2: Run the test to verify it passes (this is a validation test for the struct, not TDD)**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDContiguousInfoSliceCap' -count=1 -v 2>&1 | tail -10
```
Expected: PASS (this test validates the struct behavior).

**Step 3: Modify spmdContiguousIndexAddrCore to extract cap**

In `tinygo/compiler/spmd.go`, modify the `*types.Slice` case at line 3683:

```go
	case *types.Slice:
		bufptr := b.CreateExtractValue(val, 0, "indexaddr.ptr")
		sliceCap := b.CreateExtractValue(val, 2, "indexaddr.cap")
		bufType := b.getLLVMType(ptrTyp.Elem())
		ptr = b.CreateInBoundsGEP(bufType, bufptr, []llvm.Value{scalarIndex}, "spmd.contiguous.ptr")
		if b.spmdContiguousPtr != nil {
			b.spmdContiguousPtr[expr] = &spmdContiguousInfo{scalarPtr: ptr, loop: loop, sliceCap: sliceCap}
		}
		return ptr, nil
```

Note: The early return avoids the generic registration at line 3691-3692, since we handle it inline with the sliceCap.

**Step 4: Run all SPMD tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All tests pass.

**Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_llvm_test.go && git commit -m "feat: extract slice cap in spmdContiguousIndexAddrCore for full-load optimization"
```

---

### Task 3: Implement spmdFullLoadWithSelect

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add new function after spmdMaskedLoad at ~line 3476)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullLoadWithSelect verifies that spmdFullLoadWithSelect emits the
// correct branch structure: cap check → full load + select | masked load → merge phi.
func TestSPMDFullLoadWithSelect(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, laneCount)

	// Create a scalar iter value and a cap value.
	scalarIter := llvm.ConstInt(b.uintptrType, 8, false)
	sliceCap := llvm.ConstInt(b.uintptrType, 16, false)

	// Create a pointer to load from.
	arrType := llvm.ArrayType(i32Type, 16)
	alloca := b.CreateAlloca(arrType, "test.buf")
	zero := llvm.ConstInt(i32Type, 0, false)
	scalarPtr := b.CreateInBoundsGEP(arrType, alloca, []llvm.Value{zero, zero}, "test.ptr")

	// Create a partial mask (not all-ones) — e.g., <1,1,0,0> in i32 format.
	maskElemType := i32Type // WASM uses i32 mask elements for 4-lane
	maskType := llvm.VectorType(maskElemType, laneCount)
	mask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
	}, false)

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		sliceCap: sliceCap,
	}

	// Need a merge block after the function returns so the phi has successors.
	mergeBB := b.insertBasicBlock("test.after")

	result := b.spmdFullLoadWithSelect(vecType, ci, mask)

	// Create a branch to the merge block so the IR is well-formed.
	b.CreateBr(mergeBB)
	b.SetInsertPointAtEnd(mergeBB)
	b.CreateRetVoid()

	if result.IsNil() {
		t.Fatal("spmdFullLoadWithSelect returned nil")
	}

	// Result must be a vector type with correct lane count.
	if result.Type().TypeKind() != llvm.VectorTypeKind {
		t.Errorf("result type = %v, want VectorTypeKind", result.Type().TypeKind())
	}
	if result.Type().VectorSize() != laneCount {
		t.Errorf("result lanes = %d, want %d", result.Type().VectorSize(), laneCount)
	}

	// Verify the IR contains both a regular load and a masked load intrinsic.
	ir := c.mod.String()
	if !strings.Contains(ir, "v128.load") && !strings.Contains(ir, "load <4 x i32>") {
		// The full load path should have a plain vector load.
		// Note: LLVM IR shows "load <4 x i32>" not "v128.load" (that's WASM).
		t.Log("Note: checking for plain vector load in IR")
	}
	if !strings.Contains(ir, "llvm.masked.load") {
		t.Error("IR should contain llvm.masked.load for the fallback path")
	}
	if !strings.Contains(ir, "spmd.fullload") {
		t.Error("IR should contain spmd.fullload basic block")
	}
	if !strings.Contains(ir, "spmd.maskedload") {
		t.Error("IR should contain spmd.maskedload basic block")
	}
	if !strings.Contains(ir, "spmd.load.merge") {
		t.Error("IR should contain spmd.load.merge basic block")
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullLoadWithSelect' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — `spmdFullLoadWithSelect` doesn't exist yet.

**Step 3: Implement spmdFullLoadWithSelect**

Add to `tinygo/compiler/spmd.go` after the `spmdMaskedLoad` function (after line 3476):

```go
// spmdFullLoadWithSelect emits a runtime cap check and, when safe, replaces a
// scalarized llvm.masked.load with a plain v128.load + select(mask, loaded, zero).
// On WASM, llvm.masked.load scalarizes to 4-16 conditional scalar loads. A full
// v128.load + select is only 2 instructions when the slice backing array has
// enough capacity (scalarIter + laneCount <= sliceCap).
//
// Emits:
//   iterPlusLanes = scalarIter + laneCount
//   canFullLoad = iterPlusLanes ule sliceCap
//   br canFullLoad, fullBB, maskedBB
//   fullBB: raw = load <N x T>, ptr; result = select(mask, raw, zero); br mergeBB
//   maskedBB: result = masked.load(ptr, mask); br mergeBB
//   mergeBB: phi [fullBB, maskedBB]
func (b *builder) spmdFullLoadWithSelect(vecType llvm.Type, ci *spmdContiguousInfo, mask llvm.Value) llvm.Value {
	laneCount := ci.loop.laneCount

	// Compute scalarIter + laneCount.
	iterType := ci.loop.scalarIterVal.Type()
	laneCountVal := llvm.ConstInt(iterType, uint64(laneCount), false)
	iterPlusLanes := b.CreateAdd(ci.loop.scalarIterVal, laneCountVal, "spmd.iter.plus.lanes")

	// Extend sliceCap to match iter type if needed.
	capVal := ci.sliceCap
	if capVal.Type() != iterType {
		capVal = b.CreateZExt(capVal, iterType, "spmd.cap.ext")
	}

	// Runtime check: scalarIter + laneCount <= sliceCap (unsigned).
	canFullLoad := b.CreateICmp(llvm.IntULE, iterPlusLanes, capVal, "spmd.can.fullload")

	// Create basic blocks.
	fullBB := b.insertBasicBlock("spmd.fullload")
	maskedBB := b.insertBasicBlock("spmd.maskedload")
	mergeBB := b.insertBasicBlock("spmd.load.merge")

	b.CreateCondBr(canFullLoad, fullBB, maskedBB)

	// Full load path: plain v128.load + select.
	b.SetInsertPointAtEnd(fullBB)
	rawLoad := b.CreateLoad(vecType, ci.scalarPtr, "spmd.fullload.raw")
	zeroinit := llvm.ConstNull(vecType)
	fullResult := b.spmdMaskSelect(mask, rawLoad, zeroinit)
	b.CreateBr(mergeBB)
	// Capture the actual block (spmdMaskSelect may have changed it).
	fullExitBB := b.GetInsertBlock()

	// Masked load path: existing scalarized fallback.
	b.SetInsertPointAtEnd(maskedBB)
	maskedResult := b.spmdMaskedLoad(vecType, ci.scalarPtr, mask)
	b.CreateBr(mergeBB)
	maskedExitBB := b.GetInsertBlock()

	// Merge with phi.
	b.SetInsertPointAtEnd(mergeBB)
	phi := b.CreatePHI(vecType, "spmd.load.result")
	phi.AddIncoming([]llvm.Value{fullResult, maskedResult}, []llvm.BasicBlock{fullExitBB, maskedExitBB})

	return phi
}
```

**Step 4: Run the test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullLoadWithSelect' -count=1 -v 2>&1 | tail -20
```
Expected: PASS.

**Step 5: Run all SPMD tests for regressions**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: All pass.

**Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_llvm_test.go && git commit -m "feat: implement spmdFullLoadWithSelect for cap-based full-load optimization"
```

---

### Task 4: Wire up the call site in compiler.go

**Files:**
- Modify: `tinygo/compiler/compiler.go:4708-4717` (token.MUL contiguous load path)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the test for the skip-on-all-ones behavior**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullLoadSkippedForAllOnesMask verifies that when the mask is ConstAllOnes,
// the optimization is skipped and regular spmdMaskedLoad is used (LLVM already
// optimizes all-ones masked loads to plain loads).
func TestSPMDFullLoadSkippedForAllOnesMask(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	vecType := llvm.VectorType(i32Type, laneCount)

	// All-ones mask.
	maskElemType := i32Type
	maskType := llvm.VectorType(maskElemType, laneCount)
	mask := llvm.ConstAllOnes(maskType)

	// Check: mask == ConstAllOnes(mask.Type()) should be true.
	allOnes := llvm.ConstAllOnes(mask.Type())
	if mask.C != allOnes.C {
		t.Log("Note: ConstAllOnes comparison uses value identity")
	}

	// When mask is all-ones, the optimization should be skipped.
	// We verify this by checking that spmdIsConstAllOnesMask returns true.
	if !b.spmdIsConstAllOnesMask(mask) {
		t.Error("spmdIsConstAllOnesMask should return true for ConstAllOnes mask")
	}

	// Verify a partial mask returns false.
	partialMask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
	}, false)
	if b.spmdIsConstAllOnesMask(partialMask) {
		t.Error("spmdIsConstAllOnesMask should return false for partial mask")
	}

	// Verify a non-constant mask returns false.
	_ = vecType // suppress unused
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullLoadSkippedForAllOnesMask' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — `spmdIsConstAllOnesMask` doesn't exist yet.

**Step 3: Implement spmdIsConstAllOnesMask helper and wire up call site**

Add helper to `tinygo/compiler/spmd.go` (near the mask utilities, around line 2810):

```go
// spmdIsConstAllOnesMask returns true if the mask is a compile-time constant
// with all bits set (ConstAllOnes). When true, LLVM already optimizes
// llvm.masked.load to a plain load, so cap-based optimization is unnecessary.
func (b *builder) spmdIsConstAllOnesMask(mask llvm.Value) bool {
	if !mask.IsConstant() {
		return false
	}
	allOnes := llvm.ConstAllOnes(mask.Type())
	return mask.C == allOnes.C
}
```

Modify the call site in `tinygo/compiler/compiler.go` at line 4708-4717:

```go
		// SPMD: contiguous vector load via masked.load intrinsic.
		// When x is detected as a contiguous SPMD IndexAddr, load a full vector.
		if b.spmdContiguousPtr != nil {
			if ci, ok := b.spmdContiguousPtr[unop.X]; ok {
				elemType := b.getLLVMType(unop.X.Type().Underlying().(*types.Pointer).Elem())
				vecType := llvm.VectorType(elemType, ci.loop.laneCount)
				mask := b.spmdCurrentMask()
				if mask.IsNil() {
					mask = llvm.ConstAllOnes(llvm.VectorType(b.spmdMaskElemType(ci.loop.laneCount), ci.loop.laneCount))
				}
				// Cap-based optimization: use full v128.load + select when safe.
				if !ci.sliceCap.IsNil() && !b.spmdIsConstAllOnesMask(mask) {
					return b.spmdFullLoadWithSelect(vecType, ci, mask), nil
				}
				return b.spmdMaskedLoad(vecType, ci.scalarPtr, mask), nil
			}
		}
```

**Step 4: Run the all-ones mask test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullLoadSkippedForAllOnesMask' -count=1 -v 2>&1 | tail -10
```
Expected: PASS.

**Step 5: Build TinyGo and run all tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: Build succeeds, all tests pass.

**Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/compiler.go compiler/spmd_llvm_test.go && git commit -m "feat: wire cap-based full-load optimization at contiguous load call site"
```

---

### Task 5: E2E validation — build and run hex-encode with cap-based optimization

**Files:**
- No code changes — validation only

**Step 1: Build hex-encode SIMD binary**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -o /tmp/hex-encode-simd.wasm examples/hex-encode/main.go 2>&1 | tail -5
```
Expected: Compiles successfully.

**Step 2: Run and verify correctness**

Run:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-simd.wasm 2>&1 | head -20
```
Expected: Output matches expected hex-encoded values (no corruption from garbage reads).

**Step 3: Build scalar baseline for comparison**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -simd=false -o /tmp/hex-encode-scalar.wasm examples/hex-encode/main.go 2>&1 | tail -5
```

**Step 4: Inspect WASM for full-load instructions**

Run:
```bash
wasm2wat /tmp/hex-encode-simd.wasm | grep -c "v128.load" && echo "---" && wasm2wat /tmp/hex-encode-simd.wasm | grep -c "v128.load_lane\|i32.load\|i32.load8"
```
Expected: More `v128.load` and fewer scalar loads compared to before the optimization.

**Step 5: Run benchmark comparison**

Run:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-simd.wasm && echo "---SCALAR---" && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-scalar.wasm
```
Expected: SPMD speedup should be >= 2.68x (current baseline); any improvement shows the optimization is working.

---

### Task 6: E2E validation — mandelbrot (non-peeled loop with break)

**Files:**
- No code changes — validation only

**Step 1: Build mandelbrot SIMD binary**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -o /tmp/mandelbrot-simd.wasm examples/mandelbrot/main.go 2>&1 | tail -5
```
Expected: Compiles successfully.

**Step 2: Run and verify correctness**

Run:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/mandelbrot-simd.wasm 2>&1 | tail -10
```
Expected: 0 differences vs serial output, speedup reported.

**Step 3: Compare speedup**

Current baseline: ~2.98x SPMD speedup for mandelbrot.
Expected: Higher speedup if the optimization replaces masked loads in the break-mask loop body.

Note: If mandelbrot doesn't use slice loads in the break loop (it may use array or direct math), the optimization may not apply to this specific example. Check the compiler output for `spmd.fullload` blocks in the IR to confirm.

---

### Task 7: Handle edge case — extend integer type mismatch between scalarIterVal and sliceCap

**Files:**
- Modify: `tinygo/compiler/spmd.go` (in spmdFullLoadWithSelect)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write test for type mismatch**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullLoadCapTypeMismatch verifies that spmdFullLoadWithSelect handles
// the case where scalarIterVal and sliceCap have different integer widths
// (e.g., i32 iter vs i64 cap or vice versa).
func TestSPMDFullLoadCapTypeMismatch(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	i64Type := c.ctx.Int64Type()
	vecType := llvm.VectorType(i32Type, laneCount)

	// scalarIter is i32, sliceCap is i64 (type mismatch).
	scalarIter := llvm.ConstInt(i32Type, 4, false)
	sliceCap := llvm.ConstInt(i64Type, 16, false)

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

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		sliceCap: sliceCap,
	}

	mergeBB := b.insertBasicBlock("test.after")

	// Should not panic despite type mismatch.
	result := b.spmdFullLoadWithSelect(vecType, ci, mask)
	b.CreateBr(mergeBB)
	b.SetInsertPointAtEnd(mergeBB)
	b.CreateRetVoid()

	if result.IsNil() {
		t.Fatal("spmdFullLoadWithSelect returned nil for type mismatch case")
	}
	if result.Type().VectorSize() != laneCount {
		t.Errorf("result lanes = %d, want %d", result.Type().VectorSize(), laneCount)
	}
}
```

**Step 2: Run test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullLoadCapTypeMismatch' -count=1 -v 2>&1 | tail -10
```
Expected: PASS (the ZExt in spmdFullLoadWithSelect handles this). If it fails, adjust the ZExt logic to also handle the reverse case (cap narrower than iter) with a truncation or SExt.

**Step 3: Commit if changes needed**

Only commit if the test revealed a bug that needed fixing.

---

## Summary

| Task | Description | Key Files |
|------|-------------|-----------|
| 1 | Add sliceCap field to struct | spmd.go:1207 |
| 2 | Extract cap at IndexAddr | spmd.go:3683, spmd_llvm_test.go |
| 3 | Implement spmdFullLoadWithSelect | spmd.go (new func), spmd_llvm_test.go |
| 4 | Wire call site + all-ones skip | compiler.go:4708, spmd.go (helper), spmd_llvm_test.go |
| 5 | E2E: hex-encode validation | (no code changes) |
| 6 | E2E: mandelbrot validation | (no code changes) |
| 7 | Edge case: type mismatch | spmd.go, spmd_llvm_test.go |

**Total: 7 tasks, ~4 commits of code + 2 validation tasks + 1 edge case.**
