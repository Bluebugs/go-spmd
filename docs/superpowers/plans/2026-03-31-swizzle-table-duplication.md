# Swizzle Table Duplication for Multi-Width SIMD Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `spmdWasmSwizzle` (renamed to `spmdConstTableSwizzle`) lane-count-aware, automatically duplicating constant tables when `laneCount` is a multiple of the table size, enabling correct 256-bit AVX2 byte lookups.

**Architecture:** When `laneCount > 16` and `laneCount` is a multiple of the table size (e.g., `laneCount=32` with a 16-byte hextable), duplicate the table constant to fill the register (`[table, table]` for AVX2). Dispatch to `vpshufb ymm` (256-bit) on AVX2 or split into multiple 128-bit pshufb calls. The index preparation and result extraction scale to the actual lane count.

**Tech Stack:** TinyGo compiler, LLVM IR, x86 AVX2 intrinsics

---

## File Map

| File | Change | Responsibility |
|------|--------|----------------|
| `tinygo/compiler/spmd.go` | Modify | `spmdWasmSwizzle` → lane-count-aware table duplication + index prep |
| `tinygo/compiler/spmd_x86.go` | Modify | Add 256-bit `vpshufb` dispatch in `spmdX86Pshufb` |
| `tinygo/compiler/spmd_llvm_test.go` | Modify | Tests for 32-lane swizzle with table duplication |

---

### Task 1: Add 256-bit vpshufb dispatch in spmdX86Pshufb

**Files:**
- Modify: `tinygo/compiler/spmd_x86.go:8-16`
- Test: `tinygo/compiler/spmd_llvm_test.go`

- [ ] **Step 1: Write failing test for 256-bit pshufb**

Add to `spmd_llvm_test.go`:

```go
func TestSPMDX86Pshufb256(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	v32i8 := llvm.VectorType(c.ctx.Int8Type(), 32)
	tableAlloca := b.CreateAlloca(v32i8, "table.alloca")
	table := b.CreateLoad(v32i8, tableAlloca, "table")
	idxAlloca := b.CreateAlloca(v32i8, "idx.alloca")
	indices := b.CreateLoad(v32i8, idxAlloca, "idx")

	result := b.spmdX86Pshufb(table, indices)

	if result.IsNil() {
		t.Fatal("spmdX86Pshufb result is nil for 256-bit")
	}
	if result.Type() != v32i8 {
		t.Errorf("result type = %v, want <32 x i8>", result.Type())
	}
	modIR := b.mod.String()
	if !strings.Contains(modIR, "llvm.x86.avx2.pshuf.b") {
		t.Error("expected llvm.x86.avx2.pshuf.b in module IR for 256-bit")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make GO=... GOTESTFLAGS="-run TestSPMDX86Pshufb256 -v" GOTESTPKGS="./compiler" test`
Expected: FAIL (spmdX86Pshufb doesn't handle `<32 x i8>`)

- [ ] **Step 3: Implement 256-bit dispatch in spmdX86Pshufb**

In `spmd_x86.go`, modify `spmdX86Pshufb` to dispatch based on vector size:

```go
func (b *builder) spmdX86Pshufb(table, indices llvm.Value) llvm.Value {
	vecType := table.Type()
	laneCount := vecType.VectorSize()

	switch laneCount {
	case 32:
		// AVX2 256-bit: vpshufb ymm
		v32i8 := llvm.VectorType(b.ctx.Int8Type(), 32)
		fnType := llvm.FunctionType(v32i8, []llvm.Type{v32i8, v32i8}, false)
		fn := b.mod.NamedFunction("llvm.x86.avx2.pshuf.b")
		if fn.IsNil() {
			fn = llvm.AddFunction(b.mod, "llvm.x86.avx2.pshuf.b", fnType)
		}
		return b.createCall(fnType, fn, []llvm.Value{table, indices}, "x86.vpshufb")
	default:
		// SSE/SSSE3 128-bit: pshufb
		v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
		fnType := llvm.FunctionType(v16i8, []llvm.Type{v16i8, v16i8}, false)
		fn := b.mod.NamedFunction("llvm.x86.ssse3.pshuf.b.128")
		if fn.IsNil() {
			fn = llvm.AddFunction(b.mod, "llvm.x86.ssse3.pshuf.b.128", fnType)
		}
		return b.createCall(fnType, fn, []llvm.Value{table, indices}, "x86.pshufb")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make GO=... GOTESTFLAGS="-run TestSPMDX86Pshufb -v" GOTESTPKGS="./compiler" test`
Expected: Both existing 128-bit and new 256-bit tests PASS

- [ ] **Step 5: Commit**

```
feat: add AVX2 256-bit vpshufb dispatch in spmdX86Pshufb
```

---

### Task 2: Make spmdSwizzle accept wider vectors

**Files:**
- Modify: `tinygo/compiler/spmd.go:2351-2374` (spmdSwizzle)

- [ ] **Step 1: Update spmdSwizzle to dispatch based on vector width**

Currently `spmdSwizzle` asserts `<16 x i8>`. Generalize:

```go
func (b *builder) spmdSwizzle(table, indices llvm.Value) llvm.Value {
	laneCount := table.Type().VectorSize()

	if b.spmdIsWASM() {
		// WASM always 128-bit (16 lanes).
		v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
		intrinsicName := "llvm.wasm.swizzle"
		if b.spmdHasRelaxedSIMD() {
			intrinsicName = "llvm.wasm.relaxed.swizzle"
		}
		fnType := llvm.FunctionType(v16i8, []llvm.Type{v16i8, v16i8}, false)
		fn := b.mod.NamedFunction(intrinsicName)
		if fn.IsNil() {
			fn = llvm.AddFunction(b.mod, intrinsicName, fnType)
		}
		return b.createCall(fnType, fn, []llvm.Value{table, indices}, "spmd.swizzle")
	}
	if b.spmdHasSSSE3() {
		// x86: handles both 128-bit (pshufb) and 256-bit (vpshufb) via spmdX86Pshufb.
		return b.spmdX86Pshufb(table, indices)
	}
	// Fallback: per-lane scalar.
	return b.spmdSwizzleScalarFallback(table, indices)
}
```

The key change: remove the hardcoded `v16i8` type for the non-WASM path. `spmdX86Pshufb` now handles both widths.

- [ ] **Step 2: Run existing swizzle tests**

Run: `make GO=... GOTESTFLAGS="-run TestSPMDSwizzle -v" GOTESTPKGS="./compiler" test`
Expected: All PASS

- [ ] **Step 3: Commit**

```
refactor: generalize spmdSwizzle to accept any vector width
```

---

### Task 3: Make spmdWasmSwizzle lane-count-aware with table duplication

**Files:**
- Modify: `tinygo/compiler/spmd.go:6240-6282` (spmdWasmSwizzle)
- Modify: `tinygo/compiler/spmd.go:6640-6667` (spmdSwizzlePrepareIndex)
- Test: `tinygo/compiler/spmd_llvm_test.go`

This is the core change. When `laneCount` is a multiple of the table size, duplicate the table constant.

- [ ] **Step 1: Write failing test for 32-lane const table swizzle**

```go
func TestSPMDConstTableSwizzle32Lane(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	c.SIMDRegisterBytes = 32 // AVX2

	// 16-byte hextable, 32 lanes — table must be duplicated.
	hextable := []byte("0123456789abcdef")
	// Create <32 x i8> index vector (all values 0-15).
	i8Type := c.ctx.Int8Type()
	v32i8 := llvm.VectorType(i8Type, 32)
	idxAlloca := b.CreateAlloca(v32i8, "idx.alloca")
	index := b.CreateLoad(v32i8, idxAlloca, "idx")

	result := b.spmdWasmSwizzle(hextable, index, 32)

	// Result must be <32 x i8>, not <16 x i8>.
	if result.Type().VectorSize() != 32 {
		t.Errorf("result lanes = %d, want 32", result.Type().VectorSize())
	}
	// Must use AVX2 vpshufb.
	modIR := b.mod.String()
	if !strings.Contains(modIR, "llvm.x86.avx2.pshuf.b") {
		t.Error("expected llvm.x86.avx2.pshuf.b for 32-lane swizzle")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL (current code returns `<16 x i8>` for laneCount=32)

- [ ] **Step 3: Rewrite spmdWasmSwizzle with table duplication**

Replace the function body:

```go
func (b *builder) spmdWasmSwizzle(tableBytes []byte, index llvm.Value, laneCount int) llvm.Value {
	i8Type := b.ctx.Int8Type()
	tableLen := len(tableBytes)

	// Determine the swizzle width: use the native register width for the target.
	// For WASM (always 128-bit) and SSE (128-bit): swizzleWidth=16.
	// For AVX2 (256-bit) when laneCount > 16: swizzleWidth=32.
	swizzleWidth := 16
	if laneCount > 16 && !b.spmdIsWASM() {
		swizzleWidth = laneCount
	}

	// Build the constant table vector with automatic duplication.
	// When swizzleWidth is a multiple of tableLen, duplicate the table
	// to fill the full register (required by AVX2 vpshufb which shuffles
	// each 128-bit half independently).
	tableElems := make([]llvm.Value, swizzleWidth)
	for i := 0; i < swizzleWidth; i++ {
		srcIdx := i % tableLen
		if srcIdx < tableLen {
			tableElems[i] = llvm.ConstInt(i8Type, uint64(tableBytes[srcIdx]), false)
		} else {
			tableElems[i] = llvm.ConstInt(i8Type, 0, false)
		}
	}
	tableVec := llvm.ConstVector(tableElems, false)

	// Prepare index to match swizzle width.
	idxVec := b.spmdSwizzlePrepareIndex(index, laneCount, swizzleWidth)

	// Identity swizzle optimization.
	var result llvm.Value
	if spmdIsIdentitySwizzleIndex(idxVec) {
		result = tableVec
	} else {
		result = b.spmdSwizzle(tableVec, idxVec)
	}

	// If laneCount < swizzleWidth, extract the first laneCount lanes.
	if laneCount < swizzleWidth {
		swizzleVecType := llvm.VectorType(i8Type, swizzleWidth)
		maskElems := make([]llvm.Value, laneCount)
		for i := 0; i < laneCount; i++ {
			maskElems[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
		}
		maskVec := llvm.ConstVector(maskElems, false)
		result = b.CreateShuffleVector(result, llvm.Undef(swizzleVecType), maskVec, "")
	}

	return result
}
```

- [ ] **Step 4: Update spmdSwizzlePrepareIndex to accept target width**

Change the signature to accept a `targetWidth` parameter:

```go
func (b *builder) spmdSwizzlePrepareIndex(index llvm.Value, laneCount, targetWidth int) llvm.Value {
	i8Type := b.ctx.Int8Type()

	// Truncate to i8 if wider.
	elemWidth := index.Type().ElementType().IntTypeWidth()
	if elemWidth > 8 {
		narrowType := llvm.VectorType(i8Type, laneCount)
		index = b.CreateTrunc(index, narrowType, "")
	}

	if laneCount == targetWidth {
		return index
	}

	// Pad or truncate to targetWidth lanes.
	maskElems := make([]llvm.Value, targetWidth)
	for i := 0; i < targetWidth; i++ {
		if i < laneCount {
			maskElems[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
		} else {
			maskElems[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(laneCount), false)
		}
	}
	maskVec := llvm.ConstVector(maskElems, false)
	return b.CreateShuffleVector(index, llvm.Undef(llvm.VectorType(i8Type, laneCount)), maskVec, "")
}
```

**Important:** Update ALL callers of `spmdSwizzlePrepareIndex` — search for other call sites. The current signature is `(index, laneCount)`. The new signature is `(index, laneCount, targetWidth)`. For existing callers that pass `laneCount`, pass `16` as `targetWidth` (preserving current behavior).

- [ ] **Step 5: Update the caller comment**

In `compiler.go` (or `spmd.go`) at the call site (~line 5546), update the comment:
```go
// SPMD: use byte-permute for const string lookups of <=16 bytes.
// Table is automatically duplicated for wider registers (AVX2 vpshufb).
```

- [ ] **Step 6: Run tests**

Run: `make GO=... GOTESTFLAGS="-run TestSPMD -count=1" GOTESTPKGS="./compiler" test`
Expected: All PASS including new 32-lane test

- [ ] **Step 7: Commit**

```
feat: auto-duplicate const tables for AVX2 256-bit swizzle lookups
```

---

### Task 4: End-to-End Validation

**Files:** None (testing only)

- [ ] **Step 1: Build TinyGo**

```bash
cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

- [ ] **Step 2: Test hex-encode on AVX2**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/hex-avx2 \
    test/integration/spmd/hex-encode/main.go && /tmp/hex-avx2
```

Note: hex-encode may still have Bug 2 (ChangeType narrowing) — this task only fixes Bug 3 (table truncation).

- [ ] **Step 3: Test hex-encode on SSE (must not regress)**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -llvm-features="+ssse3,+sse4.2" -o /tmp/hex-sse \
    test/integration/spmd/hex-encode/main.go && /tmp/hex-sse
```

- [ ] **Step 4: Test WASM (must not regress)**

```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
    ./tinygo/build/tinygo build -target=wasi -scheduler=none \
    -o /tmp/hex.wasm test/integration/spmd/hex-encode/main.go && wasmtime run /tmp/hex.wasm
```

- [ ] **Step 5: Run full E2E suite**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: 86+ run-pass, 0 run-fail

- [ ] **Step 6: Commit**

```
test: validate AVX2 const table swizzle on hex-encode
```
