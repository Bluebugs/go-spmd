# AVX2 256-bit SIMD Width Propagation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `-llvm-features="+avx2"` is passed, propagate 256-bit register size through the type checker, SSA predication, and TinyGo LLVM backend so SPMD loops generate 8-wide i32 vectors instead of 4-wide.

**Architecture:** The `SIMDRegisterSize` field on `types.Config` already parameterizes the type checker. The change is: (1) detect AVX2 in LLVM features and set `SIMDRegisterSize=32`, (2) replace all hardcoded `16` / `128` in TinyGo's `spmd.go` and x-tools-spmd with the configured register size, (3) adapt mask format handling for `<8 x i16>` masks (AVX2 with 8 i32 lanes → `128/8 = i16` mask elements).

**Tech Stack:** Go, TinyGo, LLVM IR, x-tools-spmd (go/ssa)

---

## File Map

| File | Change | Responsibility |
|------|--------|----------------|
| `tinygo/compileopts/config.go` | Modify | Feature detection → `SIMDRegisterSize()` |
| `tinygo/compileopts/config_spmd_test.go` | Modify | Tests for feature detection |
| `tinygo/compiler/compiler.go` | Modify | Store + propagate register size in `compilerContext` |
| `tinygo/compiler/spmd.go` | Modify | Replace hardcoded 16/128 with `simdRegisterBytes`/`simdRegisterBits` |
| `tinygo/compiler/spmd_llvm_test.go` | Modify | Add AVX2 8-wide tests |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | Modify | Replace hardcoded 128 in `spmdLaneCountForFunc` + gather stride |
| `x-tools-spmd/go/ssa/spmd_promote.go` | Modify | Replace hardcoded 16 in `fits in v128` check |

---

### Task 1: Feature Detection in `SIMDRegisterSize()`

**Files:**
- Modify: `tinygo/compileopts/config.go:116-127`
- Test: `tinygo/compileopts/config_spmd_test.go`

- [ ] **Step 1: Write failing tests for AVX2/AVX-512 detection**

Add test cases to `config_spmd_test.go`:

```go
func TestSIMDRegisterSizeAVX2(t *testing.T) {
	c := &Config{
		Target:  &TargetSpec{GOARCH: "amd64"},
		Options: &Options{GOExperiment: "spmd", LLVMFeatures: "+ssse3,+sse4.2,+avx2"},
	}
	if got := c.SIMDRegisterSize(); got != 32 {
		t.Errorf("SIMDRegisterSize() = %d, want 32 for AVX2", got)
	}
}

func TestSIMDRegisterSizeAVX512(t *testing.T) {
	c := &Config{
		Target:  &TargetSpec{GOARCH: "amd64"},
		Options: &Options{GOExperiment: "spmd", LLVMFeatures: "+avx512f"},
	}
	if got := c.SIMDRegisterSize(); got != 64 {
		t.Errorf("SIMDRegisterSize() = %d, want 64 for AVX-512", got)
	}
}

func TestSIMDRegisterSizeSSEDefault(t *testing.T) {
	c := &Config{
		Target:  &TargetSpec{GOARCH: "amd64"},
		Options: &Options{GOExperiment: "spmd", LLVMFeatures: "+ssse3"},
	}
	if got := c.SIMDRegisterSize(); got != 16 {
		t.Errorf("SIMDRegisterSize() = %d, want 16 for SSE", got)
	}
}

func TestSIMDRegisterSizeWASM(t *testing.T) {
	c := &Config{
		Target:  &TargetSpec{GOARCH: "wasm"},
		Options: &Options{GOExperiment: "spmd"},
	}
	if got := c.SIMDRegisterSize(); got != 16 {
		t.Errorf("SIMDRegisterSize() = %d, want 16 for WASM", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd tinygo && go test ./compileopts/ -run TestSIMDRegisterSize -v`
Expected: AVX2 test FAIL (returns 16, want 32), AVX-512 test FAIL (returns 16, want 64)

- [ ] **Step 3: Implement feature detection**

In `config.go`, replace `SIMDRegisterSize()`:

```go
// SIMDRegisterSize returns the SIMD register width in bytes for the current target.
// Returns 1 when SIMD is disabled (-simd=false) to force laneCount=1 in the type checker.
// Detects AVX-512 (64 bytes), AVX2 (32 bytes), or defaults to 128-bit (16 bytes).
func (c *Config) SIMDRegisterSize() int64 {
	if c.Options.SIMD == "false" {
		return 1
	}
	if !c.SIMDEnabled() {
		return 16
	}
	// WASM always uses 128-bit SIMD.
	if c.Target.GOARCH == "wasm" {
		return 16
	}
	// x86-64: detect wider SIMD from features.
	features := c.Features()
	if strings.Contains(features, "+avx512f") {
		return 64
	}
	if strings.Contains(features, "+avx2") {
		return 32
	}
	return 16 // SSE2/SSE4 baseline
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd tinygo && go test ./compileopts/ -run TestSIMDRegisterSize -v`
Expected: All PASS

- [ ] **Step 5: Commit**

```
feat: detect AVX2/AVX-512 SIMD register width from LLVM features
```

---

### Task 2: Store Register Size in Compiler Context

**Files:**
- Modify: `tinygo/compiler/compiler.go:41-56` (Config struct)
- Modify: `tinygo/compiler/compiler.go:100-119` (newCompilerContext)
- Modify: `tinygo/compiler/spmd.go:270-283` (spmdLaneCount)
- Modify: `tinygo/compiler/spmd.go:2410-2414` (spmdMaskElemType)
- Test: `tinygo/compiler/spmd_llvm_test.go`

- [ ] **Step 1: Add `SIMDRegisterBytes` to compiler Config**

In `compiler.go`, add to the `Config` struct:

```go
type Config struct {
	// ...existing fields...
	SIMDEnabled        bool
	SIMDRegisterBytes  int    // SIMD register width in bytes: 16 (SSE/WASM), 32 (AVX2), 64 (AVX-512)
	// ...rest...
}
```

- [ ] **Step 2: Set `SIMDRegisterBytes` in builder**

In `builder/build.go`, where `compiler.Config` is created (~line 198-220), add:

```go
compilerConfig := &compiler.Config{
	// ...existing...
	SIMDEnabled:       config.SIMDEnabled(),
	SIMDRegisterBytes: int(config.SIMDRegisterSize()),
	// ...rest...
}
```

- [ ] **Step 3: Store in compilerContext and add accessor**

In `compiler.go:newCompilerContext()`:

```go
c := &compilerContext{
	Config:      config,
	simdEnabled: config.SIMDEnabled,
	// ...existing...
}
```

In `spmd.go`, add accessor:

```go
// spmdRegisterBytes returns the SIMD register width in bytes.
// 16 for SSE/WASM SIMD128, 32 for AVX2, 64 for AVX-512.
// Defaults to 16 if not configured.
func (c *compilerContext) spmdRegisterBytes() int {
	if c.SIMDRegisterBytes > 0 {
		return c.SIMDRegisterBytes
	}
	return 16
}
```

- [ ] **Step 4: Replace hardcoded 16 in `spmdLaneCount()`**

```go
func (c *compilerContext) spmdLaneCount(elemType llvm.Type) int {
	if !c.simdEnabled {
		return 1
	}
	elemSize := c.targetData.TypeAllocSize(elemType)
	if elemSize == 0 {
		return 1
	}
	return c.spmdRegisterBytes() / int(elemSize)
}
```

- [ ] **Step 5: Replace hardcoded 128 in `spmdMaskElemType()`**

```go
func (c *compilerContext) spmdMaskElemType(laneCount int) llvm.Type {
	if c.spmdUsesSIMD() {
		// Mask element fills remaining bits: registerBits / laneCount.
		// 4 lanes on 128-bit → i32; 8 lanes on 128-bit → i16; 8 lanes on 256-bit → i32.
		regBits := c.spmdRegisterBytes() * 8
		return c.ctx.IntType(regBits / laneCount)
	}
	return c.ctx.Int1Type()
}
```

**Important:** With AVX2 (256-bit) and 8 i32 lanes, this gives `256/8 = i32` masks — same element size as SSE 4-lane. This is actually cleaner than the `128/8 = i16` we feared. The mask vector becomes `<8 x i32>` (256-bit), fitting natively in a YMM register.

- [ ] **Step 6: Write LLVM test for 8-wide lane count**

Add to `spmd_llvm_test.go`:

```go
func TestSPMDLaneCountAVX2(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()

	// Override to AVX2 register size.
	c.SIMDRegisterBytes = 32

	i32 := c.ctx.Int32Type()
	i16 := c.ctx.Int16Type()
	i8 := c.ctx.Int8Type()

	if lc := c.spmdLaneCount(i32); lc != 8 {
		t.Errorf("spmdLaneCount(i32) = %d, want 8 for AVX2", lc)
	}
	if lc := c.spmdLaneCount(i16); lc != 16 {
		t.Errorf("spmdLaneCount(i16) = %d, want 16 for AVX2", lc)
	}
	if lc := c.spmdLaneCount(i8); lc != 32 {
		t.Errorf("spmdLaneCount(i8) = %d, want 32 for AVX2", lc)
	}
}

func TestSPMDMaskElemTypeAVX2(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()

	c.SIMDRegisterBytes = 32

	// 8 lanes on 256-bit → 256/8 = i32 mask elements.
	maskElem := c.spmdMaskElemType(8)
	if maskElem != c.ctx.Int32Type() {
		t.Errorf("spmdMaskElemType(8) on AVX2 = %v, want i32", maskElem)
	}
	// 16 lanes on 256-bit → 256/16 = i16 mask elements.
	maskElem16 := c.spmdMaskElemType(16)
	if maskElem16 != c.ctx.Int16Type() {
		t.Errorf("spmdMaskElemType(16) on AVX2 = %v, want i16", maskElem16)
	}
}
```

- [ ] **Step 7: Run tests**

Run: `cd tinygo && make GO=... GOTESTFLAGS="-run TestSPMDLaneCountAVX2|TestSPMDMaskElemTypeAVX2 -v" GOTESTPKGS="./compiler" test`
Expected: PASS

- [ ] **Step 8: Commit**

```
feat: parameterize SIMD register size in compiler context
```

---

### Task 3: Replace All Hardcoded 128-bit Assumptions in spmd.go

**Files:**
- Modify: `tinygo/compiler/spmd.go` — multiple sites
- Test: existing tests must still pass

This task audits and replaces every hardcoded `128` or `16` that represents the SIMD register width. Each site is listed with its line number (approximate — verify before editing).

- [ ] **Step 1: Replace `spmdBoxedVaryingGoType` mask switch**

At line ~585, replace `switch 128 / laneCount` with `switch c.spmdRegisterBytes() * 8 / laneCount`.

- [ ] **Step 2: Replace `emitSPMDBodyPrologue` narrowing threshold**

At lines ~1550 and ~1692, the narrowing logic uses `> 128`:
```go
if uint64(loop.laneCount)*elemBits > 128 {
    narrowBits := uint64(128) / uint64(loop.laneCount)
```

Replace with:
```go
regBits := uint64(b.spmdRegisterBytes()) * 8
if uint64(loop.laneCount)*elemBits > regBits {
    narrowBits := regBits / uint64(loop.laneCount)
```

This applies to BOTH the peeled path (~line 1550) and non-peeled path (~line 1692).

- [ ] **Step 3: Replace decomposed path threshold**

At lines ~1019 and ~1346, the decomposed path check uses `laneCount > 4`:
```go
isDecomposed := ssaLoop.IsRangeIndex && b.spmdIsWASM() && laneCount > 4
```

The decomposed path is WASM-only (stays within v128). No change needed here — the `spmdIsWASM()` guard already limits it. But verify the WASM-specific `v128` references at lines ~1484, ~1620 are still correct (they are — WASM always has 128-bit registers).

- [ ] **Step 4: Replace sub-128-bit checks in masked load/store**

At lines ~4014, ~4096, ~4165, ~4236, ~5394: `if vecBits < 128` and `if totalBits >= 128`. These are WASM-specific (sub-128-bit vectors are illegal on WASM). They should remain `128` since WASM register size is always 128. Verify each is guarded by `spmdIsWASM()`.

**Action:** No change needed for these — they are all WASM-specific paths.

- [ ] **Step 5: Replace `spmdMaterializeDecomposed` comment**

At line ~1743, update the comment to reflect that the concern is about WASM's 128-bit limit, not all targets.

- [ ] **Step 6: Run all SPMD tests**

Run: `cd tinygo && make GO=... GOTESTFLAGS="-run TestSPMD -count=1" GOTESTPKGS="./compiler" test`
Expected: All PASS

- [ ] **Step 7: Commit**

```
refactor: replace hardcoded 128-bit assumptions with spmdRegisterBytes
```

---

### Task 4: Replace Hardcoded 128 in x-tools-spmd

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:601-607`
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:241`
- Modify: `x-tools-spmd/go/ssa/spmd_promote.go:171`
- Test: existing tests

The x-tools-spmd code runs during SSA construction and has access to `types.Config.SIMDRegisterSize`. The `SPMDLoopInfo.LaneCount` is already set correctly by the type checker (which uses `SIMDRegisterSize`). The hardcoded `128` values are in helper functions.

- [ ] **Step 1: Fix `spmdLaneCountForFunc` in `spmd_predicate.go`**

At line ~601-606:
```go
// Current:
bits := spmdElemBits(elem)
if bits > 0 {
    return 128 / bits
}
```

This function computes lane count for SPMD body functions (varying params). It needs the register size. The function receives `*Function` which has access to `fn.Prog.Config` (the `types.Config`). Check if `fn.Prog` exposes `SIMDRegisterSize`.

**Approach:** `fn.Prog` has a `Config` field that is the `ssa.Config`. But `types.Config` is set during package building. The `SPMDLoopInfo.LaneCount` already has the correct value from the type checker. For body functions, we need the same register size.

Check: does `fn.Pkg` give access to `types.Config`? If not, the simplest approach is to add a `SIMDRegisterBits` field to `ssa.Program` during `BuildPackage`.

**Alternative:** Since `SPMDLoopInfo.LaneCount` is already correct, and body functions' lane count should match the calling loop's lane count, we can derive it from the call site rather than recomputing. But `spmdLaneCountForFunc` is called during predication when the call site isn't directly available.

**Pragmatic fix:** Add `SIMDRegisterBits int` to `ssa.Program` struct. Set it during `BuildPackage` from `types.Config.SIMDRegisterSize * 8`. Use it in `spmdLaneCountForFunc`.

```go
// In ssa.go, Program struct:
type Program struct {
    // ...existing fields...
    SIMDRegisterBits int // SIMD register width in bits (128, 256, 512). Set from types.Config.
}
```

In `spmd_predicate.go:spmdLaneCountForFunc`:
```go
regBits := fn.Prog.SIMDRegisterBits
if regBits == 0 {
    regBits = 128 // default
}
bits := spmdElemBits(elem)
if bits > 0 {
    return regBits / bits
}
```

- [ ] **Step 2: Fix gather stride computation**

At line ~241:
```go
// Current:
stride := 16 / loop.LaneCount
```

Replace with:
```go
regBytes := fn.Prog.SIMDRegisterBits / 8
if regBytes == 0 {
    regBytes = 16
}
stride := regBytes / loop.LaneCount
```

- [ ] **Step 3: Fix promote v128 size check**

At `spmd_promote.go:171`:
```go
// Current: Check 3: fits in v128 (16 bytes).
```

Replace `16` with the register size. But `spmdPromoteByteArrayCopyToVector` doesn't have direct access to the program. The promoted array must fit in a single SIMD register. Since this function operates on `*Function`, use `fn.Prog.SIMDRegisterBits / 8`.

- [ ] **Step 4: Set `SIMDRegisterBits` during BuildPackage**

In `x-tools-spmd/go/ssa/builder.go` or wherever `BuildPackage` / `ssautil.BuildPackage` is called, set `prog.SIMDRegisterBits` from the `types.Config`:

Search for where `types.Config` is available during SSA construction and propagate `SIMDRegisterSize * 8` to `prog.SIMDRegisterBits`.

- [ ] **Step 5: Run x-tools-spmd tests**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestPredicateSPMD -count=1 -v`
Expected: All PASS

- [ ] **Step 6: Commit**

```
feat: parameterize SIMD register width in go/ssa predication
```

---

### Task 5: Mask Format Adaptation for AVX2

**Files:**
- Modify: `tinygo/compiler/spmd.go` — mask handling functions
- Modify: `tinygo/compiler/compiler.go` — mask conversion paths
- Test: `tinygo/compiler/spmd_llvm_test.go`

With AVX2 (256-bit, 8 i32 lanes), `spmdMaskElemType(8)` returns `IntType(256/8)` = `i32`. This means:
- `<8 x i32>` masks (256-bit) — fits in a YMM register
- Same element type as SSE 4-lane masks (`<4 x i32>`)
- `spmdWrapMask` sign-extends `<8 x i1>` → `<8 x i32>` — same codepath, just wider
- `spmdUnwrapMaskForIntrinsic` truncates `<8 x i32>` → `<8 x i1>` — same codepath

**Key insight:** For AVX2 with i32 data, the mask element type stays i32. The mask handling code should "just work" because it already handles `<N x i32>` masks via `spmdMaskElemType`. The main concern is i16/i8 data types where laneCount > 8.

- [ ] **Step 1: Verify mask handling for 8-wide i32 (should work unchanged)**

Write test:
```go
func TestSPMDWrapMaskAVX2_8Wide(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	c.SIMDRegisterBytes = 32
	laneCount := 8 // 8 i32 lanes on AVX2

	// Create <8 x i1> comparison result.
	i1Vec := llvm.VectorType(c.ctx.Int1Type(), laneCount)
	cmpAlloca := b.CreateAlloca(i1Vec, "cmp.alloca")
	cmp := b.CreateLoad(i1Vec, cmpAlloca, "cmp")

	// Wrap to mask format.
	mask := b.spmdWrapMask(cmp, laneCount)

	// Should be <8 x i32> (256/8 = i32 mask elements).
	if mask.Type().TypeKind() != llvm.VectorTypeKind {
		t.Fatal("mask is not a vector")
	}
	if mask.Type().VectorSize() != 8 {
		t.Errorf("mask lanes = %d, want 8", mask.Type().VectorSize())
	}
	if mask.Type().ElementType() != c.ctx.Int32Type() {
		t.Errorf("mask elem = %v, want i32", mask.Type().ElementType())
	}
}
```

- [ ] **Step 2: Verify mask handling for 16-wide i16 (AVX2 with int16 data)**

With AVX2 and i16 data: laneCount = 32/2 = 16. `spmdMaskElemType(16)` = `IntType(256/16)` = `i16`. Mask = `<16 x i16>` (256-bit).

Write test:
```go
func TestSPMDWrapMaskAVX2_16Wide(t *testing.T) {
	c := newTestCompilerContextX86(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	c.SIMDRegisterBytes = 32
	laneCount := 16 // 16 i16 lanes on AVX2

	i1Vec := llvm.VectorType(c.ctx.Int1Type(), laneCount)
	cmpAlloca := b.CreateAlloca(i1Vec, "cmp.alloca")
	cmp := b.CreateLoad(i1Vec, cmpAlloca, "cmp")

	mask := b.spmdWrapMask(cmp, laneCount)

	if mask.Type().VectorSize() != 16 {
		t.Errorf("mask lanes = %d, want 16", mask.Type().VectorSize())
	}
	if mask.Type().ElementType() != c.ctx.Int16Type() {
		t.Errorf("mask elem = %v, want i16", mask.Type().ElementType())
	}
}
```

- [ ] **Step 3: Verify `spmdVectorAnyTrue` and `spmdVectorAllTrue` with 256-bit masks**

These functions bitcast the mask to `<16 x i8>` for WASM's `v128.any_true`. On x86, they use `icmp ne`. With `<8 x i32>` AVX2 masks, the x86 path should still work: bitcast `<8 x i32>` to `<32 x i8>`, then `icmp ne` against zero.

Verify by reading `spmdAnyTrue` and `spmdAllTrue` — they bitcast to `<16 x i8>` which is 128-bit. For 256-bit masks, they need to bitcast to `<32 x i8>`.

**Critical fix needed:** In `spmdAnyTrue` and `spmdAllTrue`, the bitcast target should be `<regBytes x i8>` not hardcoded `<16 x i8>`.

```go
// In spmdAnyTrue, replace:
i8x16 := llvm.VectorType(b.ctx.Int8Type(), 16)
// With:
regBytes := b.spmdRegisterBytes()
i8xN := llvm.VectorType(b.ctx.Int8Type(), regBytes)
```

Same for `spmdAllTrue`.

- [ ] **Step 4: Verify `spmdMaskSelect` with 256-bit masks**

`spmdMaskSelect` uses `v128.bitselect` on WASM. On x86, it uses `CreateSelect` or bitwise ops. With 256-bit masks the x86 path should work unchanged since it operates on vectors generically. Verify.

- [ ] **Step 5: Verify `spmdNormalizeBoolVecToI1` with wider masks**

This function truncates `<N x iW>` to `<N x i1>`. It should work with any lane count/element type. Verify.

- [ ] **Step 6: Run all SPMD tests**

Run: `cd tinygo && make GO=... GOTESTFLAGS="-run TestSPMD -count=1 -v" GOTESTPKGS="./compiler" test`
Expected: All PASS

- [ ] **Step 7: Commit**

```
feat: adapt mask handling for AVX2 256-bit vectors
```

---

### Task 6: End-to-End Validation

**Files:**
- No new files

- [ ] **Step 1: Build TinyGo**

```bash
cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

- [ ] **Step 2: Run WASM E2E (must not regress)**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: 66 run-pass, 0 run-fail, 1 compile-fail (base64-decoder)

- [ ] **Step 3: Run x86-64 SSE benchmarks (must not regress)**

Without `+avx2`, behavior should be identical to before:
```bash
bash test/e2e/spmd-benchmark-x86.sh
```

Expected: Same results as current (SSE 4-wide, all 6 lo-* pass)

- [ ] **Step 4: Run x86-64 AVX2 benchmarks**

With `+avx2`, lane count should double:
```bash
# Manual test — build lo-sum with AVX2 and check it uses 8-wide vectors
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/lo-sum-avx2 \
    test/integration/spmd/lo-sum/main.go && /tmp/lo-sum-avx2
```

Expected: Correctness PASS, potentially higher speedup (8-wide vs 4-wide)

- [ ] **Step 5: Verify all 6 lo-* with AVX2**

```bash
for fn in sum mean min max contains clamp; do
    PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
        -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/lo-${fn}-avx2 \
        test/integration/spmd/lo-${fn}/main.go && /tmp/lo-${fn}-avx2
done
```

Expected: All pass correctness, potentially higher speedups

- [ ] **Step 6: Commit**

```
test: validate AVX2 256-bit SPMD on x86-64
```

---

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|------------|
| WASM regression | High | WASM always uses 128-bit; all WASM-specific paths guarded by `spmdIsWASM()` |
| SSE regression (no AVX2 flag) | High | `SIMDRegisterSize()` defaults to 16 when no `+avx2` in features |
| Mask format mismatch | Medium | AVX2 8-lane i32 gives `<8 x i32>` masks (same elem type as SSE) |
| x-tools-spmd lane count wrong | Medium | `SPMDLoopInfo.LaneCount` from type checker already correct; only body func helper needs fix |
| Sub-128-bit vectors on AVX2 | Low | Only a concern for WASM; x86 handles any vector width |
| `getLLVMType(MaskType{})` latent bug | Low | Pre-existing; `createConvert` early-return guards it |

## Key Insight

For the common case (i32 data on AVX2): laneCount goes from 4→8, mask stays `<N x i32>`, vector becomes `<8 x i32>` (256-bit YMM register). The mask element type (`i32`) doesn't change — only the lane count doubles. This means most mask handling code works unchanged. The main work is replacing hardcoded constants.

For i16 data on AVX2: laneCount=16, mask=`<16 x i16>`. For i8 data: laneCount=32, mask=`<32 x i8>`. These are wider vectors but follow the same pattern.
