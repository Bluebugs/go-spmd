# Runtime Array Swizzle Optimization Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace per-lane GEP+load gather operations in `spmdVectorIndexArray` with a single `i8x16.swizzle` WASM instruction for byte arrays ≤ 16 elements.

**Architecture:** When a WASM target indexes a `[N]byte` array (N ≤ 16) with a varying index, load the array into a `<16 x i8>` register and use `llvm.wasm.swizzle` instead of spilling to alloca + per-lane GEP + per-lane scalar loads. For sub-128-bit results (e.g., 4 lanes from `<16 x i8>`), widen each byte to the mask element type (i32) to avoid WASM's sub-128-bit vector limitation. This replaces ~12 WASM ops per access (4 extract + 4 load + 4 insert) with 1 swizzle + 4 extract + 4 zext + 4 insert = ~9 ops, but the swizzle itself replaces the 4 memory loads with a single register-to-register operation.

**Tech Stack:** Go, TinyGo compiler (`compiler/spmd.go`), LLVM IR, WebAssembly SIMD128

---

## Background

### Current Array Index Path

`spmdVectorIndexArray` (spmd.go:4190) handles `array[varyingIndex]`:

1. Spills array to alloca: `store [N x T] %collection, ptr %alloca`
2. Per-lane GEP+load: `GEP %alloca[0][laneIdx]` → `load` → `insertelement` (× N lanes)
3. For byte arrays on WASM, each loaded `i8` is widened to `i32` to avoid sub-128-bit vectors

For `[16]byte` indexed by `<4 x i32>`, this generates:
- 1 alloca + 1 store (spill)
- 4 extract_lane (from index vector)
- 4 GEP + 4 memory loads + 4 zext + 4 insert = 20 ops per access

### Existing Const String Swizzle

`spmdVectorIndexString` (spmd.go:4154) already has a fast path for const strings ≤ 16 bytes:

```go
if b.spmdIsWASM() {
    if constVal, ok := expr.X.(*ssa.Const); ok {
        if constVal.Value.Kind() == constant.String {
            strVal := constant.StringVal(constVal.Value)
            if len(strVal) <= 16 {
                return b.spmdWasmSwizzle([]byte(strVal), index, laneCount), nil
            }
        }
    }
}
```

This uses `spmdWasmSwizzle` which builds a `<16 x i8>` **constant** vector from compile-time bytes. The limitation: it only works for compile-time-known string constants. Runtime arrays (like `input [16]byte`) cannot use this path.

### Helper Functions Available

- `spmdWasmSwizzle(tableBytes []byte, index, laneCount)` — builds const `<16 x i8>`, calls `llvm.wasm.swizzle`, extracts N lanes (spmd.go:4304)
- `spmdSwizzlePrepareIndex(index, laneCount)` — converts `<N x iM>` index to `<16 x i8>` via trunc + pad (spmd.go:4345)
- `spmdMaskElemType(laneCount)` — returns the wider element type for sub-128-bit widening (e.g., i32 for 4 lanes)

### The ipv4-parser Problem

`input[start]` where `input` is `[16]byte` and `start` is varying `int` (4 lanes). Currently generates ~20 WASM ops per byte access × 6 accesses in the digit conversion loop = ~120 gather ops. With swizzle: 1 WASM `i8x16.swizzle` instruction per access × 6 = 6 swizzle instructions (plus extraction/widening overhead).

---

## Task 1: Add `spmdSwizzleArrayBytes` Helper and Integrate into `spmdVectorIndexArray`

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add helper near line 4304, modify `spmdVectorIndexArray` at line 4190)
- Test: `tinygo/compiler/spmd_llvm_test.go`

### Step 1: Write the failing test

Add after `TestSPMDVectorIndexArrayLLVM` (line 3021) in `spmd_llvm_test.go`:

```go
func TestSPMDSwizzleArrayBytes(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	// Simulate a [16]byte array indexed by <4 x i32> on WASM.
	i8Type := c.ctx.Int8Type()
	i32Type := c.ctx.Int32Type()
	arrayLen := 16
	laneCount := 4

	// Build a [16 x i8] array value (all zeros for this test).
	arrayType := llvm.ArrayType(i8Type, arrayLen)
	arrayVal := llvm.ConstNull(arrayType)

	// Build index vector <4 x i32> = [0, 1, 2, 3].
	vecType := llvm.VectorType(i32Type, laneCount)
	indexVec := llvm.Undef(vecType)
	for i := 0; i < laneCount; i++ {
		indexVec = b.CreateInsertElement(indexVec,
			llvm.ConstInt(i32Type, uint64(i), false),
			llvm.ConstInt(i32Type, uint64(i), false), "")
	}

	// Call the swizzle helper.
	result, err := b.spmdSwizzleArrayBytes(arrayVal, indexVec, arrayLen, laneCount)
	if err != nil {
		t.Fatal(err)
	}

	// On WASM, result should be <4 x i32> (widened from i8 to avoid sub-128-bit).
	if result.Type().TypeKind() != llvm.VectorTypeKind {
		t.Fatalf("expected vector, got %v", result.Type().TypeKind())
	}
	if result.Type().VectorSize() != laneCount {
		t.Errorf("expected %d lanes, got %d", laneCount, result.Type().VectorSize())
	}

	// Verify the module IR contains llvm.wasm.swizzle.
	modIR := b.mod.String()
	if !strings.Contains(modIR, "llvm.wasm.swizzle") {
		t.Error("expected llvm.wasm.swizzle in module IR")
	}
}
```

**Note:** `spmdSwizzleArrayBytes` doesn't exist yet, so this test will fail to compile.

### Step 2: Implement `spmdSwizzleArrayBytes`

Add to `spmd.go`, after `spmdWasmSwizzle` (line 4343):

```go
// spmdSwizzleArrayBytes uses i8x16.swizzle to perform vectorized byte lookup
// from a runtime [N]byte array (N ≤ 16) on WASM. The array is loaded into a
// <16 x i8> register and swizzled with the index vector. For sub-128-bit
// results (laneCount < 16), each byte is widened to the mask element type.
func (b *builder) spmdSwizzleArrayBytes(collection, index llvm.Value, arrayLen, laneCount int) (llvm.Value, error) {
	i8Type := b.ctx.Int8Type()
	i32Type := b.ctx.Int32Type()
	v16i8 := llvm.VectorType(i8Type, 16)

	// Load the array value into a <16 x i8> vector.
	var tableVec llvm.Value
	if arrayLen == 16 {
		// Exact fit: store [16 x i8] to alloca, load back as <16 x i8>.
		alloca, allocaSize := b.createTemporaryAlloca(collection.Type(), "swizzle.alloca")
		b.CreateStore(collection, alloca)
		tableVec = b.CreateLoad(v16i8, alloca, "swizzle.table")
		b.emitLifetimeEnd(alloca, allocaSize)
	} else {
		// Pad to 16 bytes: extract each byte, insert into zero vector.
		tableVec = llvm.ConstNull(v16i8)
		for i := 0; i < arrayLen; i++ {
			val := b.CreateExtractValue(collection, i, "")
			tableVec = b.CreateInsertElement(tableVec, val,
				llvm.ConstInt(i32Type, uint64(i), false), "")
		}
	}

	// Prepare index as <16 x i8>.
	idxVec := b.spmdSwizzlePrepareIndex(index, laneCount)

	// Call @llvm.wasm.swizzle(<16 x i8>, <16 x i8>).
	intrinsicName := "llvm.wasm.swizzle"
	fnType := llvm.FunctionType(v16i8, []llvm.Type{v16i8, v16i8}, false)
	fn := b.mod.NamedFunction(intrinsicName)
	if fn.IsNil() {
		fn = llvm.AddFunction(b.mod, intrinsicName, fnType)
	}
	swizzled := b.createCall(fnType, fn, []llvm.Value{tableVec, idxVec}, "spmd.swizzle")

	// Determine result element type. On WASM, widen bytes to avoid sub-128-bit vectors.
	resultElemType := i8Type
	if b.spmdIsWASM() {
		vecBits := uint64(b.targetData.TypeAllocSize(llvm.VectorType(i8Type, laneCount))) * 8
		if vecBits < 128 {
			resultElemType = b.spmdMaskElemType(laneCount)
		}
	}

	// Fast path: 16 byte lanes, no widening needed.
	if laneCount == 16 && resultElemType == i8Type {
		return swizzled, nil
	}

	// Extract laneCount bytes from swizzle result and widen if needed.
	result := llvm.Undef(llvm.VectorType(resultElemType, laneCount))
	for i := 0; i < laneCount; i++ {
		val := b.CreateExtractElement(swizzled,
			llvm.ConstInt(i32Type, uint64(i), false), "")
		if resultElemType != i8Type {
			val = b.CreateZExt(val, resultElemType, "")
		}
		result = b.CreateInsertElement(result, val,
			llvm.ConstInt(i32Type, uint64(i), false), "")
	}
	return result, nil
}
```

### Step 3: Add swizzle fast path to `spmdVectorIndexArray`

In `spmdVectorIndexArray` (spmd.go:4190), restructure the function to:
1. Move the bounds check BEFORE the `laneIdxs` computation
2. Insert the swizzle fast path after the bounds check with an early return
3. Keep the existing alloca+GEP fallback for non-eligible arrays

The modified function structure becomes:

```go
func (b *builder) spmdVectorIndexArray(expr *ssa.Index, collection, index llvm.Value) (llvm.Value, error) {
	laneCount := index.Type().VectorSize()
	xType := expr.X.Type().Underlying().(*types.Array)

	// SPMD: clamp inactive lane indices to 0 using SSA-level mask.
	// [unchanged — lines 4196-4204]

	arrayType := collection.Type()

	// Reject arrays of varying elements.
	// [unchanged — lines 4209-4213]

	// Bounds check (moved before laneIdxs — works on vector index directly).
	arrayLen := llvm.ConstInt(b.uintptrType, uint64(xType.Len()), false)
	if !b.info.nobounds {
		canElide := false
		if maxVal, known := spmdIndexMaxValue(expr.Index); known {
			if maxVal < uint64(xType.Len()) {
				canElide = true
			}
		}
		if !canElide {
			maxIdx := b.spmdVectorReduceUmax(index)
			if maxIdx.Type().IntTypeWidth() < b.uintptrType.IntTypeWidth() {
				maxIdx = b.CreateZExt(maxIdx, b.uintptrType, "")
			}
			oob := b.CreateICmp(llvm.IntUGE, maxIdx, arrayLen, "spmd.bounds.oob")
			b.createRuntimeAssert(oob, "lookup", "lookupPanic")
		}
	}

	// WASM fast path: use i8x16.swizzle for byte arrays ≤ 16 elements.
	elemType := arrayType.ElementType()
	if b.spmdIsWASM() && elemType == b.ctx.Int8Type() && xType.Len() <= 16 {
		return b.spmdSwizzleArrayBytes(collection, index, int(xType.Len()), laneCount)
	}

	// Fallback: alloca + per-lane GEP + load.
	alloca, allocaSize := b.createTemporaryAlloca(arrayType, "index.alloca")
	b.CreateStore(collection, alloca)

	// Pre-compute extended lane indices for GEP.
	laneIdxs := make([]llvm.Value, laneCount)
	// [unchanged — remaining GEP code]
	...
}
```

**Key changes:**
1. `arrayLen` declaration and bounds check block moved ABOVE the `laneIdxs` loop
2. New swizzle eligibility check: `b.spmdIsWASM() && elemType == i8 && xType.Len() <= 16`
3. Early return via `spmdSwizzleArrayBytes` skips alloca, laneIdxs, and GEP entirely
4. The `elemType` variable declaration is moved up (it was previously at line 4256, now needed earlier for the swizzle check)

### Step 4: Run test to verify it passes

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run "TestSPMDSwizzleArrayBytes" -v -count=1`

If tests cannot be run directly due to LLVM linking, verify by building:

Run: `cd /home/cedric/work/SPMD && rm -rf ~/.cache/tinygo && make build-tinygo`

Expected: `✓ TinyGo built at tinygo/build/tinygo`

### Step 5: Run all existing SPMD tests

Run: `cd /home/cedric/work/SPMD && make build-tinygo`

Expected: No compilation errors.

### Step 6: Commit

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "perf: add i8x16.swizzle fast path for byte array indexing on WASM"
```

---

## Task 2: Rebuild, Test, and Benchmark

**Files:**
- No code changes — build and test only

### Step 1: Clear cache and rebuild TinyGo

Run: `cd /home/cedric/work/SPMD && rm -rf ~/.cache/tinygo && make build-tinygo`

Expected: `✓ TinyGo built at tinygo/build/tinygo`

### Step 2: Compile ipv4-parser (uses `s[start]` — string indexing, not array)

Run: `cd /home/cedric/work/SPMD && make compile EXAMPLE=ipv4-parser`

Expected: Compiles successfully. This example uses `s[start]` (string), so the swizzle optimization does NOT apply here. This verifies no regressions in string indexing.

### Step 3: Switch ipv4-parser to use `input[start]` and compile

Modify `examples/ipv4-parser/main.go` lines 129-140: change all `s[start]`, `s[start+1]`, `s[start+2]` to `input[start]`, `input[start+1]`, `input[start+2]`.

Do the same for `test/integration/spmd/ipv4-parser/main.go` lines 308-319.

Run: `cd /home/cedric/work/SPMD && make compile EXAMPLE=ipv4-parser`

Expected: Compiles successfully.

### Step 4: Verify correctness

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs ipv4-parser.wasm`

Expected: All 10 test cases produce correct output (same as before).

### Step 5: Inspect WAT for swizzle instructions

Run: `wasm2wat ipv4-parser.wasm | grep -c 'i8x16.swizzle'`

Expected: ≥ 6 `i8x16.swizzle` instructions (one per `input[start+N]` access in the digit conversion loop).

Run: `wasm2wat ipv4-parser.wasm | grep -c 'i32.load8_u'`

Expected: Significantly fewer `i32.load8_u` than the 423 seen without swizzle optimization.

### Step 6: Run benchmark

Run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/integ_ipv4-parser.wasm test/integration/spmd/ipv4-parser/main.go
wasmtime run --dir=. /tmp/integ_ipv4-parser.wasm
```

Expected: SPMD speedup should improve significantly from the previous 0.30x (which was array indexing WITHOUT swizzle). Target: ≥ 0.50x (matching or exceeding the original string indexing performance).

### Step 7: Run full E2E suite

Run: `bash test/e2e/spmd-e2e-test.sh`

Expected: 31 run pass, 36 compile pass, 0 run fail, 10 reject OK (no regressions).

### Step 8: Commit the ipv4-parser source change

```bash
cd /home/cedric/work/SPMD
git add examples/ipv4-parser/main.go test/integration/spmd/ipv4-parser/main.go
git commit -m "refactor: use input array instead of string for ipv4 digit lookup"
```

---

## Edge Cases

1. **Array length < 16:** The `extractvalue` + `insertelement` padding loop handles arrays smaller than 16 bytes. Remaining lanes in the `<16 x i8>` table are zero. Since `llvm.wasm.swizzle` returns 0 for OOB indices (≥ 16), and all valid indices are < N after bounds checking, padding values are never accessed.

2. **Array length == 16:** Uses the efficient `alloca` + `store` + `load as <16 x i8>` path. No extractvalue loop needed. With opaque pointers, loading `<16 x i8>` from a `[16 x i8]` alloca is valid (same size/alignment).

3. **laneCount == 16 (byte loops):** Result is `<16 x i8>` = 128 bits, no widening needed. Returns swizzle result directly.

4. **laneCount < 16 (e.g., 4 for int indices):** Result bytes are widened from `i8` to `i32` (via `spmdMaskElemType`) to produce `<4 x i32>`, avoiding WASM's sub-128-bit vector limitation. This matches the existing GEP path's widening behavior.

5. **Non-byte arrays (e.g., `[4]int32`):** Not eligible — the swizzle fast path only fires for `elemType == i8`. Falls through to existing alloca+GEP path.

6. **Non-WASM targets:** Not eligible — `b.spmdIsWASM()` check prevents using WASM-specific intrinsic on other targets.

7. **Inactive lanes:** Already clamped to index 0 by the mask clamp at the top of `spmdVectorIndexArray`. Swizzle with index 0 returns `table[0]`, which is a valid array element.

---

## Expected Impact

### ipv4-parser Digit Conversion Loop

Each `input[start+N]` access (6 total across switch cases):

| Metric | Before (GEP) | After (Swizzle) |
|--------|-------------|-----------------|
| Array spill | 1 alloca + 1 store | 1 alloca + 1 store + 1 v128.load |
| Index prep | 4 extract_lane | 4 trunc + shuffle (shared prep) |
| Data lookup | 4 GEP + 4 i32.load8_u | 1 i8x16.swizzle |
| Result build | 4 zext + 4 insert | 4 extract + 4 zext + 4 insert |
| **Total ops** | **~20** | **~14** (but swizzle replaces 4 mem loads) |

The key win is not instruction count but **memory access pattern**: 4 scalar loads from different addresses (gather) → 1 register-to-register swizzle. On WASM runtimes, memory loads are significantly more expensive than register operations.

### Benchmark Projection

- Previous with `s[start]` (string gather): 0.50x
- Previous with `input[start]` (array gather, no swizzle): 0.30x
- Expected with `input[start]` (array swizzle): ≥ 0.60x (swizzle is faster than string pointer chase)
