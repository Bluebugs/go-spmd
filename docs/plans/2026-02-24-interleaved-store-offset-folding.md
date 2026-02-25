# Interleaved Store Offset Folding Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable LLVM to fold constant offsets in interleaved stores into WASM `v128.store offset=K` instructions, eliminating redundant `i32.add` instructions.

**Architecture:** Change `spmdEmitInterleavedStore` to use a two-level GEP pattern: first GEP for the dynamic scalar base, then a second GEP with the constant stride offset. LLVM's instruction selection folds constant GEP offsets into memory operation offsets, producing `v128.store offset=16` instead of `i32.const 16; i32.add; v128.store`.

**Tech Stack:** TinyGo LLVM compiler (`tinygo/compiler/spmd.go`), LLVM IR GEP + masked.store, WASM SIMD128

---

## Background

### Current Code (`spmd.go:5532-5539`)

```go
for k := 0; k < stride; k++ {
    offset := llvm.ConstInt(b.uintptrType, uint64(k*N), false)
    ptr := b.CreateInBoundsGEP(elemType, bufptr,
        []llvm.Value{b.CreateAdd(scalarBase, offset, "interleaved.off")},
        "interleaved.store.ptr")
    b.spmdMaskedStore(outVecs[k], ptr, expandedMasks[k])
}
```

### Current LLVM IR Output

```llvm
;; k=0: useless add 0
%interleaved.off = add i32 %spmd.decomp.mul.base, 0
%interleaved.store.ptr = getelementptr inbounds i8, ptr %interleaved.ptr, i32 %interleaved.off
call void @llvm.masked.store.v16i8.p0(<16 x i8> %lo, ptr %interleaved.store.ptr, ...)

;; k=1: constant 16 baked into add, cannot fold into store offset
%interleaved.off22 = add i32 %spmd.decomp.mul.base, 16
%interleaved.store.ptr23 = getelementptr inbounds i8, ptr %interleaved.ptr, i32 %interleaved.off22
call void @llvm.masked.store.v16i8.p0(<16 x i8> %hi, ptr %interleaved.store.ptr23, ...)
```

### Current WASM Output (EncodeSrc)

```wat
;; Address for 2nd store: 3 instructions
local.get dst_ptr    ;; push base
i32.const 16         ;; push offset
i32.add              ;; dst_ptr + 16 — LLVM can't fold because add was in the GEP index
```

### Desired LLVM IR Output

```llvm
;; k=0: direct GEP from base
%interleaved.base.ptr = getelementptr inbounds i8, ptr %interleaved.ptr, i32 %spmd.decomp.mul.base
call void @llvm.masked.store.v16i8.p0(<16 x i8> %lo, ptr %interleaved.base.ptr, ...)

;; k=1: constant GEP from base pointer → LLVM folds into store offset
%interleaved.store.ptr.1 = getelementptr inbounds i8, ptr %interleaved.base.ptr, i32 16
call void @llvm.masked.store.v16i8.p0(<16 x i8> %hi, ptr %interleaved.store.ptr.1, ...)
```

### Desired WASM Output

```wat
;; 2nd store uses offset operand, no extra i32.add
v128.store offset=16 align=1    ;; LLVM folds constant GEP into store offset
```

### Impact

- Saves 2 WASM instructions per iteration for stride-2 (EncodeSrc)
- Saves 4 WASM instructions per iteration for stride-3
- Saves 6 WASM instructions per iteration for stride-4
- Eliminates dead `add i32 %base, 0` for k=0 in all strides

---

### Task 1: Write the failing LLVM IR test

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go` (append new test)

**Step 1: Write a test that verifies GEP offset pattern**

The test builds a mock interleaved store scenario with stride=2, N=16, and inspects the generated LLVM IR for the two-level GEP pattern. It checks:
1. k=0 store uses a GEP with just `scalarBase` (no `add 0`)
2. k=1 store uses a GEP with constant offset from the base pointer (not `add scalarBase, 16`)

```go
// TestSPMDInterleavedStoreOffsetFolding verifies that spmdEmitInterleavedStore
// generates a two-level GEP pattern enabling LLVM to fold constant offsets
// into store instructions.
//
// Expected IR pattern:
//   %base = getelementptr inbounds i8, ptr %buf, i32 %scalarBase
//   store <16 x i8> %lo, ptr %base                    ; k=0: no offset
//   %ptr1 = getelementptr inbounds i8, ptr %base, i32 16
//   store <16 x i8> %hi, ptr %ptr1                    ; k=1: constant GEP
//
// Must NOT contain: add i32 %scalarBase, 0
// Must NOT contain: add i32 %scalarBase, 16
func TestSPMDInterleavedStoreOffsetFolding(t *testing.T) {
```

The test should dump the LLVM IR of the function to a string and assert:
- Contains `getelementptr inbounds i8, ptr %interleaved.ptr, i32 %` (base GEP)
- Contains `getelementptr inbounds i8, ptr %interleaved.base.ptr, i32 16` (offset GEP for k=1)
- Does NOT contain `add i32 %spmd.decomp.mul.base, 0` or similar dead add-zero
- Does NOT contain `add i32 %spmd.decomp.mul.base, 16` or similar baked-in offset

**Note**: Since `spmdEmitInterleavedStore` requires significant setup (SSA values, decomposed index map, slice extraction), a simpler approach is to test via the E2E WASM output. However, if unit testing is preferred, the test can construct the necessary builder state. Choose whichever approach matches the existing test patterns.

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test -run TestSPMDInterleavedStoreOffsetFolding ./compiler/ -v`
Expected: FAIL — the IR still contains `add i32 %base, 0` and `add i32 %base, 16`

**Step 3: Commit**

```
test: add LLVM IR test for interleaved store offset folding
```

---

### Task 2: Implement two-level GEP in spmdEmitInterleavedStore

**Files:**
- Modify: `tinygo/compiler/spmd.go:5532-5539`

**Step 1: Replace the store emission loop**

Change the loop at line 5532-5539 from:

```go
// Emit S masked stores: outVecs[k] → bufptr[scalarBase + k*N].
for k := 0; k < stride; k++ {
    offset := llvm.ConstInt(b.uintptrType, uint64(k*N), false)
    ptr := b.CreateInBoundsGEP(elemType, bufptr,
        []llvm.Value{b.CreateAdd(scalarBase, offset, "interleaved.off")},
        "interleaved.store.ptr")
    b.spmdMaskedStore(outVecs[k], ptr, expandedMasks[k])
}
```

To:

```go
// Emit S masked stores: outVecs[k] → bufptr[scalarBase + k*N].
// Use two-level GEP so LLVM can fold the constant k*N offset into the
// store instruction's offset operand (e.g. v128.store offset=16 on WASM).
basePtr := b.CreateInBoundsGEP(elemType, bufptr,
    []llvm.Value{scalarBase}, "interleaved.base.ptr")
for k := 0; k < stride; k++ {
    ptr := basePtr
    if k > 0 {
        offset := llvm.ConstInt(b.uintptrType, uint64(k*N), false)
        ptr = b.CreateInBoundsGEP(elemType, basePtr,
            []llvm.Value{offset}, fmt.Sprintf("interleaved.store.ptr.%d", k))
    }
    b.spmdMaskedStore(outVecs[k], ptr, expandedMasks[k])
}
```

**Step 2: Run the LLVM tests**

Run: `cd tinygo && go test -run TestSPMD ./compiler/ -v`
Expected: All SPMD tests pass, including the new offset folding test.

**Step 3: Commit**

```
feat: use two-level GEP in interleaved stores for offset folding
```

---

### Task 3: Verify WASM output and benchmark

**Files:**
- None modified (verification only)

**Step 1: Build hex-encode and disassemble**

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./tinygo/build/tinygo build -target=wasi -o /tmp/hex-encode-simd.wasm examples/hex-encode/main.go
wasm2wat /tmp/hex-encode-simd.wasm -o /tmp/hex-encode-simd.wat
```

**Step 2: Verify the EncodeSrc main loop no longer contains the extra `i32.add` for stride offset**

Check the WAT for `$main.EncodeSrc`:
- Should see `v128.store offset=16 align=1` instead of `i32.const 16; i32.add; ... v128.store align=1`
- Loop body should be ~40 instructions (down from 42)

**Step 3: Verify LLVM IR shows the two-level GEP**

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./tinygo/build/tinygo build -target=wasi -internal-printir -o /dev/null examples/hex-encode/main.go 2>&1 \
  | grep 'interleaved\.\(off\|store\|base\)'
```

Should show:
- `getelementptr inbounds i8, ptr %interleaved.ptr, i32 %spmd.decomp.mul.base` (base GEP)
- `getelementptr inbounds i8, ptr %interleaved.base.ptr, i32 16` (constant offset GEP)
- No `add i32 %..., 0` or `add i32 %..., 16`

**Step 4: Run benchmark**

```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-simd.wasm
```

Record new speedup numbers. Expected: EncodeSrc improves slightly (42→40 instructions, ~5%).

**Step 5: Run E2E test suite to check for regressions**

```bash
bash test/e2e/spmd-e2e-test.sh
```

All previously passing tests should still pass.

**Step 6: Commit**

```
test: verify interleaved store offset folding in WASM output
```

---

### Task 4: Update documentation

**Files:**
- Modify: `docs/hex-encode-simd-analysis.md`

**Step 1: Update OPT-1 status from TODO to DONE**

In the "Remaining Optimization Opportunities" section, mark OPT-1 as completed with the measured results.

**Step 2: Commit**

```
docs: update hex-encode analysis with store offset folding results
```
