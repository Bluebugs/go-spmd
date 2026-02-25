# Cap-Based Full-Store (Load-Blend-Store) Optimization — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace scalarized `llvm.masked.store` with load-blend-store (`v128.load` + `v128.bitselect` + `v128.store`) when the slice backing array has enough capacity, eliminating 4-16 conditional scalar stores per access.

**Architecture:** Create `spmdFullStoreWithBlend` that emits a runtime cap check and, when safe, uses load-blend-store instead of `llvm.masked.store`. Wire it into both contiguous store call sites (regular + coalesced). Reuse existing `spmdMaskSelect` for the blend step and `spmdContiguousInfo.sliceCap` from the load optimization.

**Tech Stack:** TinyGo compiler (Go), LLVM IR generation via go-llvm bindings, WASM SIMD128 target.

**Design doc:** `docs/plans/2026-02-24-cap-based-full-store-design.md`

**Prerequisite:** The cap-based full-load optimization must be implemented first (Tasks 1-4 of `docs/plans/2026-02-24-cap-based-full-load-plan.md`), which adds `sliceCap` to `spmdContiguousInfo` and provides `spmdIsConstAllOnesMask`.

---

### Task 1: Implement spmdFullStoreWithBlend

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add new function after `spmdMaskedStore` at ~line 3502)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullStoreWithBlend verifies that spmdFullStoreWithBlend emits the
// correct branch structure: cap check → blend BB (load+select+store) | masked-store BB → merge.
func TestSPMDFullStoreWithBlend(t *testing.T) {
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

	// Create a pointer to store to.
	arrType := llvm.ArrayType(i32Type, 16)
	alloca := b.CreateAlloca(arrType, "test.buf")
	zero := llvm.ConstInt(i32Type, 0, false)
	scalarPtr := b.CreateInBoundsGEP(arrType, alloca, []llvm.Value{zero, zero}, "test.ptr")

	// Create a partial mask (not all-ones).
	maskElemType := i32Type
	mask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstAllOnes(maskElemType),
		llvm.ConstNull(maskElemType),
		llvm.ConstNull(maskElemType),
	}, false)

	// Create a value to store.
	storeVal := llvm.ConstNull(vecType)

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		sliceCap: sliceCap,
	}

	// Call spmdFullStoreWithBlend.
	b.spmdFullStoreWithBlend(storeVal, ci, mask)

	// Create a terminator so the IR is well-formed.
	b.CreateRetVoid()

	// Verify the IR structure.
	ir := c.mod.String()

	if !strings.Contains(ir, "llvm.masked.store") {
		t.Error("IR should contain llvm.masked.store for the fallback path")
	}
	if !strings.Contains(ir, "spmd.blend") {
		t.Error("IR should contain spmd.blend basic block")
	}
	if !strings.Contains(ir, "spmd.maskedstore") {
		t.Error("IR should contain spmd.maskedstore basic block")
	}
	if !strings.Contains(ir, "spmd.store.merge") {
		t.Error("IR should contain spmd.store.merge basic block")
	}
	// The blend BB should have a plain load (for reading existing data).
	if !strings.Contains(ir, "spmd.blend.old") {
		t.Error("IR should contain spmd.blend.old load in the blend path")
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullStoreWithBlend' -count=1 -v 2>&1 | tail -10
```
Expected: FAIL — `spmdFullStoreWithBlend` doesn't exist yet.

**Step 3: Implement spmdFullStoreWithBlend**

Add to `tinygo/compiler/spmd.go` after `spmdMaskedStore` (after line ~3502):

```go
// spmdFullStoreWithBlend emits a runtime cap check and, when safe, replaces a
// scalarized llvm.masked.store with a load-blend-store pattern:
//   v128.load(ptr) → v128.bitselect(mask, newVal, oldVal) → v128.store(ptr)
//
// On WASM, llvm.masked.store scalarizes to 4-16 conditional scalar stores. The
// load-blend-store pattern is only 3 instructions when the slice backing array
// has enough capacity (scalarIter + laneCount <= sliceCap).
//
// This is the same pattern ISPC uses (__masked_store_blend_*). It is safe in
// single-threaded WASM (no concurrent writes can interleave between load and store).
//
// Emits:
//   iterPlusLanes = scalarIter + laneCount
//   canFullStore = iterPlusLanes ule sliceCap
//   br canFullStore, blendBB, maskedBB
//   blendBB: old = load ptr; blended = select(mask, val, old); store blended, ptr; br mergeBB
//   maskedBB: masked.store(val, ptr, mask); br mergeBB
//   mergeBB: (void, no phi)
func (b *builder) spmdFullStoreWithBlend(val llvm.Value, ci *spmdContiguousInfo, mask llvm.Value) {
	vecType := val.Type()
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
	canFullStore := b.CreateICmp(llvm.IntULE, iterPlusLanes, capVal, "spmd.can.fullstore")

	// Create basic blocks.
	blendBB := b.insertBasicBlock("spmd.blend")
	maskedBB := b.insertBasicBlock("spmd.maskedstore")
	mergeBB := b.insertBasicBlock("spmd.store.merge")

	b.CreateCondBr(canFullStore, blendBB, maskedBB)

	// Blend path: load existing → select → store.
	b.SetInsertPointAtEnd(blendBB)
	oldVal := b.CreateLoad(vecType, ci.scalarPtr, "spmd.blend.old")
	blended := b.spmdMaskSelect(mask, val, oldVal)
	b.CreateStore(blended, ci.scalarPtr)
	b.CreateBr(mergeBB)

	// Masked store path: existing scalarized fallback.
	b.SetInsertPointAtEnd(maskedBB)
	b.spmdMaskedStore(val, ci.scalarPtr, mask)
	b.CreateBr(mergeBB)

	// Merge (void return, no phi).
	b.SetInsertPointAtEnd(mergeBB)
}
```

**Step 4: Run the test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullStoreWithBlend' -count=1 -v 2>&1 | tail -20
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
cd /home/cedric/work/SPMD/tinygo && git add compiler/spmd.go compiler/spmd_llvm_test.go && git commit -m "feat: implement spmdFullStoreWithBlend for cap-based load-blend-store optimization"
```

---

### Task 2: Wire regular contiguous store call site

**Files:**
- Modify: `tinygo/compiler/compiler.go:2441-2456` (regular contiguous store path)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write the test for skip-on-all-ones**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullStoreSkippedForAllOnesMask verifies that when the mask is ConstAllOnes,
// the store optimization is skipped and regular spmdMaskedStore is used.
func TestSPMDFullStoreSkippedForAllOnesMask(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	laneCount := 4
	i32Type := c.ctx.Int32Type()
	maskType := llvm.VectorType(i32Type, laneCount)

	// All-ones mask should skip blend optimization.
	allOnesMask := llvm.ConstAllOnes(maskType)
	if !b.spmdIsConstAllOnesMask(allOnesMask) {
		t.Error("all-ones mask should be detected as const all-ones")
	}

	// Non-const mask (e.g., from a phi or instruction result) should not be all-ones.
	// We test with a partial constant mask.
	partialMask := llvm.ConstVector([]llvm.Value{
		llvm.ConstAllOnes(i32Type),
		llvm.ConstNull(i32Type),
		llvm.ConstNull(i32Type),
		llvm.ConstNull(i32Type),
	}, false)
	if b.spmdIsConstAllOnesMask(partialMask) {
		t.Error("partial mask should not be detected as const all-ones")
	}
}
```

**Step 2: Run test to verify it passes**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullStoreSkippedForAllOnesMask' -count=1 -v 2>&1 | tail -10
```
Expected: PASS (uses `spmdIsConstAllOnesMask` from the load optimization prerequisite).

**Step 3: Modify the regular contiguous store call site**

In `tinygo/compiler/compiler.go`, replace lines 2441-2456:

```go
		// SPMD: contiguous vector store via masked.store intrinsic.
		// When the address is a contiguous SPMD IndexAddr, store a full vector.
		if b.spmdContiguousPtr != nil {
			if ci, ok := b.spmdContiguousPtr[instr.Addr]; ok {
				mask := b.spmdCurrentMask()
				if mask.IsNil() {
					mask = llvm.ConstAllOnes(llvm.VectorType(b.spmdMaskElemType(ci.loop.laneCount), ci.loop.laneCount))
				}
				// Splat scalar values to vector for masked store.
				if llvmVal.Type().TypeKind() != llvm.VectorTypeKind {
					vecType := llvm.VectorType(llvmVal.Type(), ci.loop.laneCount)
					llvmVal = b.splatScalar(llvmVal, vecType)
				}
				// Cap-based optimization: use load-blend-store when safe.
				if !ci.sliceCap.IsNil() && !b.spmdIsConstAllOnesMask(mask) {
					b.spmdFullStoreWithBlend(llvmVal, ci, mask)
				} else {
					b.spmdMaskedStore(llvmVal, ci.scalarPtr, mask)
				}
				return
			}
		}
```

**Step 4: Build TinyGo and run all tests**

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
cd /home/cedric/work/SPMD/tinygo && git add compiler/compiler.go compiler/spmd_llvm_test.go && git commit -m "feat: wire cap-based load-blend-store at regular contiguous store call site"
```

---

### Task 3: Wire coalesced store call site

**Files:**
- Modify: `tinygo/compiler/compiler.go:2403-2408` (coalesced store path)

**Step 1: Modify the coalesced store call site**

In `tinygo/compiler/compiler.go`, replace lines 2403-2408:

```go
					llvmAddr := b.getValue(instr.Addr, getPos(instr))
					if b.spmdContiguousPtr != nil {
						if ci, ok := b.spmdContiguousPtr[instr.Addr]; ok {
							// Cap-based optimization: use load-blend-store when safe.
							if !ci.sliceCap.IsNil() && !b.spmdIsConstAllOnesMask(parentMask) {
								b.spmdFullStoreWithBlend(selected, ci, parentMask)
							} else {
								b.spmdMaskedStore(selected, ci.scalarPtr, parentMask)
							}
							return
						}
					}
```

**Step 2: Build TinyGo and run all tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMD' -count=1 -timeout 120s 2>&1 | tail -20
```
Expected: Build succeeds, all tests pass.

**Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add compiler/compiler.go && git commit -m "feat: wire cap-based load-blend-store at coalesced store call site"
```

---

### Task 4: E2E validation — hex-encode

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
Expected: Output matches expected hex-encoded values (no corruption from blend stores).

**Step 3: Compare against scalar baseline**

Run:
```bash
cd /home/cedric/work/SPMD && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./tinygo/build/tinygo build -target=wasi -simd=false -o /tmp/hex-encode-scalar.wasm examples/hex-encode/main.go 2>&1 | tail -5
```
Then:
```bash
cd /home/cedric/work/SPMD && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-simd.wasm && echo "---SCALAR---" && node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-scalar.wasm
```
Expected: SPMD speedup >= 2.68x (current baseline).

**Step 4: Inspect WASM for blend pattern**

Run:
```bash
wasm2wat /tmp/hex-encode-simd.wasm | grep -c "v128.store" && echo "---" && wasm2wat /tmp/hex-encode-simd.wasm | grep -c "v128.bitselect"
```
Expected: `v128.store` and `v128.bitselect` present, indicating the load-blend-store pattern is active in the generated WASM.

---

### Task 5: E2E validation — mandelbrot (non-peeled loop)

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
Expected: 0 differences vs serial output. Speedup reported.

**Step 3: Compare speedup**

Current baseline: ~2.98x SPMD speedup.
Expected: If mandelbrot stores to slices in the break-mask loop body, speedup should improve. If stores are to arrays or use direct math, the store optimization may not apply — check the compiler output for `spmd.blend` blocks.

---

### Task 6: Handle type mismatch edge case

**Files:**
- Modify: `tinygo/compiler/spmd.go` (in spmdFullStoreWithBlend, if needed)
- Test: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write test for type mismatch**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDFullStoreCapTypeMismatch verifies that spmdFullStoreWithBlend handles
// the case where scalarIterVal and sliceCap have different integer widths.
func TestSPMDFullStoreCapTypeMismatch(t *testing.T) {
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

	storeVal := llvm.ConstNull(vecType)

	ci := &spmdContiguousInfo{
		scalarPtr: scalarPtr,
		loop: &spmdActiveLoop{
			laneCount:     laneCount,
			scalarIterVal: scalarIter,
		},
		sliceCap: sliceCap,
	}

	// Should not panic despite type mismatch.
	b.spmdFullStoreWithBlend(storeVal, ci, mask)
	b.CreateRetVoid()

	// Verify IR is valid (no LLVM type errors).
	ir := c.mod.String()
	if !strings.Contains(ir, "spmd.blend") {
		t.Error("IR should contain spmd.blend block even with type mismatch")
	}
}
```

**Step 2: Run test**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && go test ./compiler/ -run 'TestSPMDFullStoreCapTypeMismatch' -count=1 -v 2>&1 | tail -10
```
Expected: PASS (the ZExt in spmdFullStoreWithBlend handles this). If it fails, add the reverse case handling (truncation when cap is narrower than iter).

**Step 3: Commit if changes needed**

Only commit if the test revealed a bug that needed fixing.

---

## Summary

| Task | Description | Key Files |
|------|-------------|-----------|
| 1 | Implement spmdFullStoreWithBlend | spmd.go (new func), spmd_llvm_test.go |
| 2 | Wire regular contiguous store site | compiler.go:2441, spmd_llvm_test.go |
| 3 | Wire coalesced store site | compiler.go:2403 |
| 4 | E2E: hex-encode validation | (no code changes) |
| 5 | E2E: mandelbrot validation | (no code changes) |
| 6 | Edge case: type mismatch | spmd.go, spmd_llvm_test.go |

**Total: 6 tasks, ~3 commits of code + 2 validation tasks + 1 edge case.**

**Dependency**: This plan depends on the cap-based full-load plan being implemented first (provides `sliceCap` field and `spmdIsConstAllOnesMask` helper).
