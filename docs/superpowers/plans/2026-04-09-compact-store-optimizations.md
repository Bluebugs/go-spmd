# CompactStore Codegen Optimizations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace CompactStore's scalar extract+store codegen with SIMD shuffle-based compaction, fix x86 feature detection, and fix the swizzle fallback lane count bug.

**Architecture:** All changes in `tinygo/compiler/spmd.go` plus two doc updates. Ordered to fix prerequisites first (feature detection, fallback), then optimize (constant-mask, runtime-mask). Each task is independently testable.

**Tech Stack:** Go (TinyGo compiler), LLVM IR, WASM SIMD128, x86 SSE/SSSE3/AVX2

**Spec:** `docs/superpowers/specs/2026-04-09-compact-store-optimizations-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### TinyGo (`tinygo/`)
- **Modify:** `compiler/spmd.go` — all four optimizations

### Go fork (`go/`)
- **Modify:** `src/lanes/lanes.go` — update CompactStore doc comment

### Main repo
- **Modify:** `docs/superpowers/specs/2026-04-08-compact-store-design.md` — add overwrite documentation

### Test artifacts (not committed)
- `/tmp/base64-*.wasm`, `/tmp/base64-*` — build artifacts for correctness and benchmark verification

---

## Task 1: x86 Feature Implication Chain

**Files:**
- Modify: `tinygo/compiler/spmd.go:2359-2363` (replace `spmdHasSSSE3`)

- [ ] **Step 1: Add `spmdHasX86Feature` function**

In `tinygo/compiler/spmd.go`, add after `spmdIsX86()` (line 2357), before the current `spmdHasSSSE3`:

```go
// x86FeatureChain lists x86 SIMD features in implication order.
// Each feature implies all features before it in the chain.
var x86FeatureChain = []string{
	"sse2", "sse3", "ssse3", "sse4.1", "sse4.2", "avx", "avx2", "avx512f",
}

// spmdHasX86Feature returns true when the target is x86 and has the named
// feature (or any feature that implies it). For example, "+avx2" implies "+ssse3".
func (c *compilerContext) spmdHasX86Feature(name string) bool {
	if !c.spmdIsX86() {
		return false
	}
	reqIdx := -1
	for i, f := range x86FeatureChain {
		if f == name {
			reqIdx = i
			break
		}
	}
	if reqIdx < 0 {
		return false
	}
	for i := reqIdx; i < len(x86FeatureChain); i++ {
		if strings.Contains(c.Features, "+"+x86FeatureChain[i]) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Replace `spmdHasSSSE3` with wrapper**

Replace the existing `spmdHasSSSE3` (lines 2359-2363):

```go
// spmdHasSSSE3 returns true when the target is x86 with SSSE3 or higher.
func (c *compilerContext) spmdHasSSSE3() bool {
	return c.spmdHasX86Feature("ssse3")
}
```

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 4: Verify AVX2 flag alone now enables pshufb path**

Test that `-llvm-features=+avx2` (without explicit `+ssse3`) produces correct output:

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+avx2 -o /tmp/base64-avx2-feat \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-avx2-feat
```

Expected: `Correctness: PASS`

Also verify SSE still works:

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -o /tmp/base64-sse-feat test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-sse-feat
```

Expected: `Correctness: PASS`

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "feat: add x86 feature implication chain for SIMD detection

spmdHasX86Feature('ssse3') now returns true when +avx2 or higher
is present, matching the x86 ISA hierarchy. Fixes silent fallback
to scalar swizzle path when only +avx2 is specified.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Fix `spmdSwizzleScalarFallback` Lane Count

**Files:**
- Modify: `tinygo/compiler/spmd.go:2418-2433` (the `spmdSwizzleScalarFallback` function)

- [ ] **Step 1: Fix the function to use actual lane count**

Replace the current `spmdSwizzleScalarFallback` (lines 2418-2433):

```go
// spmdSwizzleScalarFallback emits a per-lane byte-permute via extractelement/insertelement.
// Used when neither WASM swizzle nor x86 pshufb is available.
// Indices with value >= laneCount or bit 7 set produce 0 (matching i8x16.swizzle semantics).
func (b *builder) spmdSwizzleScalarFallback(table, indices llvm.Value) llvm.Value {
	i8Type := b.ctx.Int8Type()
	i32Type := b.ctx.Int32Type()
	laneCount := table.Type().VectorSize()
	resultType := llvm.VectorType(i8Type, laneCount)
	result := llvm.ConstNull(resultType)
	threshold := llvm.ConstInt(i8Type, uint64(laneCount), false)
	for i := 0; i < laneCount; i++ {
		laneConst := llvm.ConstInt(i32Type, uint64(i), false)
		idx := b.CreateExtractElement(indices, laneConst, "")
		oob := b.CreateICmp(llvm.IntUGE, idx, threshold, "")
		elem := b.CreateExtractElement(table, idx, "")
		elem = b.CreateSelect(oob, llvm.ConstInt(i8Type, 0, false), elem, "")
		result = b.CreateInsertElement(result, elem, laneConst, "")
	}
	return result
}
```

Key changes: `v16i8` → `resultType` using actual `laneCount`, loop bound `16` → `laneCount`, threshold `16` → `laneCount`.

- [ ] **Step 2: Build and test**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

Test with SSE (the fallback is only used without SSSE3, which is rare, but verify compilation succeeds):

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -o /tmp/base64-sse-fallback test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-sse-fallback
```

Expected: `Correctness: PASS`

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "fix: use actual lane count in spmdSwizzleScalarFallback

Was hardcoded to 16 lanes (<16 x i8>), causing corruption on AVX2
(32 lanes) when the scalar fallback was used. Now reads lane count
from the input vector type.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Vectorized Constant-Mask CompactStore

**Files:**
- Modify: `tinygo/compiler/spmd.go:3285-3327` (replace `createCompactStoreConst`)

- [ ] **Step 1: Replace `createCompactStoreConst` with shuffle+store**

Replace the current implementation (lines 3285-3327) with:

```go
// createCompactStoreConst handles lanes.CompactStore with a compile-time constant mask.
// Active lanes are compacted to the front via a byte-shuffle (pshufb/i8x16.swizzle/tbl),
// then stored as a single full-width vector write (overwrite pattern — the caller advances
// by activeCount and the next call overwrites trailing garbage).
func (b *builder) createCompactStoreConst(ptr, val llvm.Value, mask []bool, elemType llvm.Type, laneCount int) llvm.Value {
	activeCount := 0
	for _, m := range mask {
		if m {
			activeCount++
		}
	}
	if activeCount == 0 {
		return llvm.ConstInt(b.intType, 0, false)
	}

	i8Type := b.ctx.Int8Type()
	i32Type := b.ctx.Int32Type()
	elemSize := int(b.targetData.TypeAllocSize(elemType))

	// For byte-width elements, use the target's native byte-shuffle (pshufb/swizzle/tbl)
	// which operates on <N x i8> directly. For wider elements, use LLVM shufflevector.
	if elemSize == 1 {
		// Build <N x i8> shuffle indices: active lanes packed to front.
		// Inactive output positions get 0x80 (produces 0 for pshufb/swizzle semantics).
		indexElts := make([]llvm.Value, laneCount)
		outIdx := 0
		for i := 0; i < laneCount; i++ {
			if mask[i] {
				indexElts[outIdx] = llvm.ConstInt(i8Type, uint64(i), false)
				outIdx++
			}
		}
		for i := outIdx; i < laneCount; i++ {
			indexElts[i] = llvm.ConstInt(i8Type, 0x80, false) // out-of-range → 0
		}
		indexVec := llvm.ConstVector(indexElts, false)

		// Use the target's native byte-shuffle.
		compacted := b.spmdSwizzle(val, indexVec)

		// Single full-width vector store (overwrite pattern).
		st := b.CreateStore(compacted, ptr)
		st.SetAlignment(int(b.targetData.TypeAllocSize(elemType)))
		return llvm.ConstInt(b.intType, uint64(activeCount), false)
	}

	// Wider types: use LLVM shufflevector.
	indices := make([]llvm.Value, laneCount)
	outIdx := 0
	for i := 0; i < laneCount; i++ {
		if mask[i] {
			indices[outIdx] = llvm.ConstInt(i32Type, uint64(i), false)
			outIdx++
		}
	}
	for i := outIdx; i < laneCount; i++ {
		indices[i] = llvm.Undef(i32Type)
	}
	shuffleMask := llvm.ConstVector(indices, false)
	compacted := b.CreateShuffleVector(val, llvm.Undef(val.Type()), shuffleMask, "compact.const.shuffle")

	// Single full-width vector store.
	st := b.CreateStore(compacted, ptr)
	st.SetAlignment(int(b.targetData.TypeAllocSize(elemType)))
	return llvm.ConstInt(b.intType, uint64(activeCount), false)
}
```

- [ ] **Step 2: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 3: Verify correctness on all targets**

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-wasm-opt.wasm test/integration/spmd/base64-mula-lemire/main.go 2>&1 \
  && wasmtime run /tmp/base64-wasm-opt.wasm
```

SSE:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-ssse3-opt \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-ssse3-opt
```

AVX2:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-avx2-opt \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-avx2-opt
```

Also run the CompactStore integration test:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/compact-store-opt.wasm test/integration/spmd/compact-store/main.go 2>&1 \
  && wasmtime run /tmp/compact-store-opt.wasm
```

Expected: `Correctness: PASS` / `PASS` for all.

- [ ] **Step 4: Run benchmarks**

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-wasm-opt.wasm test/integration/spmd/base64-mula-lemire/bench.go 2>&1 \
  && wasmtime run /tmp/bench-wasm-opt.wasm
```

SSSE3:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-ssse3-opt \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-ssse3-opt
```

AVX2:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-avx2-opt \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-avx2-opt
```

Report the results — expected improvement from ~10x to ~14-16x on SSSE3 at 1MB.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "perf: vectorize CompactStore constant-mask path

Replace per-element extractelement+store (89 instructions on SSE)
with a single pshufb/i8x16.swizzle compaction shuffle followed by
one full-width vector store (overwrite pattern). For byte-width
types, uses the target's native byte-shuffle (spmdSwizzle). For
wider types, uses LLVM shufflevector.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Vectorized Runtime-Mask CompactStore

**Files:**
- Modify: `tinygo/compiler/spmd.go:3329-3375` (replace `createCompactStoreRuntime`)

- [ ] **Step 1: Add `spmdByteShiftLeft` helper**

Add before `createCompactStoreRuntime`:

```go
// spmdByteShiftLeft shifts a <N x i8> vector left by `count` byte positions,
// filling vacated positions with zero. This is a lane-level shift (not bit shift):
// element[i] = (i+count < N) ? vec[i+count] : 0.
//
// On x86: maps to pslldq (SSE) or vpslldq (AVX2 — within each 128-bit lane).
// On WASM: uses i8x16.shuffle with a constant mask.
// Fallback: LLVM shufflevector.
func (b *builder) spmdByteShiftLeft(vec llvm.Value, count int) llvm.Value {
	laneCount := vec.Type().VectorSize()
	if count <= 0 || count >= laneCount {
		if count >= laneCount {
			return llvm.ConstNull(vec.Type())
		}
		return vec
	}
	// Build shuffle mask: [count, count+1, ..., N-1, N, N+1, ..., N+count-1]
	// where indices >= N select from the second operand (zero).
	i32Type := b.ctx.Int32Type()
	indices := make([]llvm.Value, laneCount)
	for i := 0; i < laneCount; i++ {
		if i+count < laneCount {
			indices[i] = llvm.ConstInt(i32Type, uint64(i+count), false)
		} else {
			// Select from second operand (zeroinitializer) — index = laneCount + (i+count-laneCount)
			indices[i] = llvm.ConstInt(i32Type, uint64(laneCount+i+count-laneCount), false)
		}
	}
	shuffleMask := llvm.ConstVector(indices, false)
	zero := llvm.ConstNull(vec.Type())
	return b.CreateShuffleVector(vec, zero, shuffleMask, "compact.bsl")
}
```

Note: LLVM will lower `shufflevector` with this pattern to `pslldq`/`vpslldq` on x86 and the appropriate shuffle on WASM. No need for target-specific intrinsics.

- [ ] **Step 2: Replace `createCompactStoreRuntime` with binary-tree compaction**

Replace the current implementation (lines 3329-3375):

```go
// createCompactStoreRuntime handles lanes.CompactStore with a runtime mask.
// Uses a binary-tree compaction: log2(N) levels of conditional shuffles.
// At each level, elements shift left to fill gaps left by inactive lanes.
// Fully SIMD — no scalar extraction, no branches.
func (b *builder) createCompactStoreRuntime(ptr, val, mask llvm.Value, elemType llvm.Type, laneCount int) llvm.Value {
	i8Type := b.ctx.Int8Type()
	elemSize := int(b.targetData.TypeAllocSize(elemType))

	// For non-byte types, fall back to per-lane scalar stores (correct but slow).
	// The binary-tree approach works on <N x i8>; wider types would need a different
	// shuffle strategy. Deferred to a follow-up optimization.
	if elemSize != 1 {
		return b.createCompactStoreRuntimeScalar(ptr, val, mask, elemType, laneCount)
	}

	// Step 1: Convert mask to <N x i8> gap counts (1 where inactive, 0 where active).
	i8VecType := llvm.VectorType(i8Type, laneCount)
	maskI8 := b.CreateZExt(mask, i8VecType, "compact.rt.mask.i8")
	ones := b.spmdSplatConstI8(laneCount, 1)
	gaps := b.CreateXor(maskI8, ones, "compact.rt.gaps") // 1 = inactive, 0 = active

	// Step 2: Inclusive prefix-sum of gaps via parallel doubling.
	// After this, g[i] = number of inactive lanes in positions [0..i].
	g := gaps
	for stride := 1; stride < laneCount; stride *= 2 {
		shifted := b.spmdByteShiftLeft(g, stride)
		g = b.CreateAdd(g, shifted, "compact.rt.pfx")
	}

	// Step 3: Build source indices. Output position j reads from source j + g[j].
	// identity = <0, 1, 2, ..., N-1>
	identityElts := make([]llvm.Value, laneCount)
	for i := 0; i < laneCount; i++ {
		identityElts[i] = llvm.ConstInt(i8Type, uint64(i), false)
	}
	identity := llvm.ConstVector(identityElts, false)
	srcIndices := b.CreateAdd(identity, g, "compact.rt.idx")

	// Step 4: Shuffle to compact active lanes to front.
	compacted := b.spmdSwizzle(val, srcIndices)

	// Step 5: Full-width vector store (overwrite pattern).
	st := b.CreateStore(compacted, ptr)
	st.SetAlignment(int(b.targetData.TypeAllocSize(elemType)))

	// Step 6: Popcount for return value.
	// n = laneCount - g[laneCount-1] (total gaps = total inactive lanes).
	// Or equivalently: extract the bitmask and popcount.
	bitmask := b.spmdBitmask(mask)
	popcount := b.spmdPopcount(bitmask, laneCount)

	return popcount
}
```

- [ ] **Step 3: Add `spmdSplatConstI8` helper**

```go
// spmdSplatConstI8 creates a <N x i8> vector with all elements set to `val`.
func (b *builder) spmdSplatConstI8(laneCount int, val uint8) llvm.Value {
	i8Type := b.ctx.Int8Type()
	elts := make([]llvm.Value, laneCount)
	c := llvm.ConstInt(i8Type, uint64(val), false)
	for i := range elts {
		elts[i] = c
	}
	return llvm.ConstVector(elts, false)
}
```

- [ ] **Step 4: Add `spmdPopcount` helper**

Check if a popcount helper already exists. If not, add:

```go
// spmdPopcount computes popcount of a scalar integer bitmask.
// Returns the count as the compiler's int type (b.intType).
func (b *builder) spmdPopcount(bitmask llvm.Value, maxBits int) llvm.Value {
	// Use LLVM's ctpop intrinsic.
	bitmaskType := bitmask.Type()
	fnName := "llvm.ctpop." + llvmTypeName(bitmaskType)
	fnType := llvm.FunctionType(bitmaskType, []llvm.Type{bitmaskType}, false)
	fn := b.mod.NamedFunction(fnName)
	if fn.IsNil() {
		fn = llvm.AddFunction(b.mod, fnName, fnType)
	}
	count := b.createCall(fnType, fn, []llvm.Value{bitmask}, "compact.popcount")
	if count.Type() != b.intType {
		count = b.CreateZExt(count, b.intType, "compact.popcount.ext")
	}
	return count
}
```

Note: Check how `llvm.ctpop` is named for the specific integer type. LLVM intrinsic names include the type, e.g., `llvm.ctpop.i32`. The `llvmTypeName` function may not exist — adapt to use the correct naming. Alternatively, check if there's an existing ctpop usage in the codebase.

- [ ] **Step 5: Rename old runtime function as scalar fallback**

Rename the original per-lane branch chain to `createCompactStoreRuntimeScalar`:

```go
// createCompactStoreRuntimeScalar is the per-lane fallback for runtime-mask CompactStore
// on non-byte types. Uses conditional branches and PHI-threaded output index.
func (b *builder) createCompactStoreRuntimeScalar(ptr, val, mask llvm.Value, elemType llvm.Type, laneCount int) llvm.Value {
	// ... existing implementation from createCompactStoreRuntime (lines 3333-3375) ...
}
```

Copy the current body of `createCompactStoreRuntime` into this function verbatim.

- [ ] **Step 6: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 7: Verify correctness**

Run the CompactStore integration test (which has a runtime-mask test case — "filter lowercase"):

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/compact-store-rt.wasm test/integration/spmd/compact-store/main.go 2>&1 \
  && wasmtime run /tmp/compact-store-rt.wasm
```

SSSE3:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/compact-store-rt-ssse3 \
  test/integration/spmd/compact-store/main.go 2>&1 && /tmp/compact-store-rt-ssse3
```

Expected: `PASS` for both.

Also verify base64 still works (it uses the constant-mask path, but this confirms nothing regressed):

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-rt-opt \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-rt-opt
```

Expected: `Correctness: PASS`

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "perf: vectorize CompactStore runtime-mask path

Replace the per-lane serial branch chain (89 instructions on SSE)
with a binary-tree compaction: log2(N) levels of prefix-sum +
shuffle. For 16 byte lanes: ~10 SIMD instructions. For 32 lanes
(AVX2): ~12 instructions. Non-byte types fall back to the scalar
per-lane path.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Documentation Update

**Files:**
- Modify: `go/src/lanes/lanes.go:190-198`
- Modify: `docs/superpowers/specs/2026-04-08-compact-store-design.md`

- [ ] **Step 1: Update lanes.go doc comment**

In `go/src/lanes/lanes.go`, replace the CompactStore comment (lines 190-198):

```go
// CompactStore writes the active lanes of v contiguously to dst.
// Active means both the explicit mask lane is true AND the current
// execution mask lane is active. Returns the number of elements written.
//
// The underlying store may write up to lanes.Count[T]() elements to the
// destination memory, even though only n elements contain valid data.
// The caller must ensure the destination slice's backing array has at
// least lanes.Count[T]() elements of accessible memory beyond the current
// offset. In practice, allocate output_len + lanes.Count[T]() and advance
// by the returned n. Trailing bytes are overwritten by subsequent calls.
//
// COMPILER BUILTIN: replaced with SIMD compress-store instructions.
//
//go:noinline
func CompactStore[T any](dst []T, v Varying[T], mask Varying[bool]) int {
	panic("lanes.CompactStore is a compiler builtin and should be replaced during compilation")
}
```

- [ ] **Step 2: Update the original spec**

In `docs/superpowers/specs/2026-04-08-compact-store-design.md`, add a new section after "Bounds Checking" (Section 4):

```markdown
## 4b. Overwrite Semantics

The underlying store writes a full SIMD-width vector to memory, even though
only `n` elements contain valid data. Trailing bytes beyond position `n` are
garbage that will be overwritten by the next CompactStore call. This matches
the standard SIMD string-processing pattern (Mula-Lemire, simdutf).

The caller must ensure `dst`'s backing memory has at least `lanes.Count[T]()`
elements of accessible space beyond the current write offset. In practice:

```go
// Allocate with SIMD-width slack
dst := make([]byte, outputLen + lanes.Count[byte](v))
```

The slice bounds check validates `len(dst) >= popcount(mask)`, but the physical
store writes up to `lanes.Count[T]()` bytes starting at `&dst[0]`.
```

- [ ] **Step 3: Commit both**

```bash
cd /home/cedric/work/SPMD/go
git add src/lanes/lanes.go
git commit -m "docs: add CompactStore overwrite semantics to doc comment

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"

cd /home/cedric/work/SPMD
git add docs/superpowers/specs/2026-04-08-compact-store-design.md
git commit -m "docs: add overwrite semantics section to CompactStore spec

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Final Benchmarks and E2E Validation

**Files:** None (verification only)

- [ ] **Step 1: Run full E2E test suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```

Expected: No regressions from the baseline (90 run-pass).

- [ ] **Step 2: Run benchmarks on all targets**

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-final-wasm.wasm test/integration/spmd/base64-mula-lemire/bench.go 2>&1 \
  && wasmtime run /tmp/bench-final-wasm.wasm
```

SSSE3:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-final-ssse3 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-final-ssse3
```

AVX2:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-final-avx2 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-final-avx2
```

- [ ] **Step 3: Report results table**

Compare before/after for each target at 1MB:

| Target | Before | After | Improvement |
|--------|--------|-------|-------------|
| WASM | 0.94x / 136 MB/s | ? | ? |
| SSSE3 | 10.17x / ~1900 MB/s | ? | ? |
| AVX2 | 10.77x / 2032 MB/s | ? | ? |

---

## Deferred Items

Add to PLAN.md "Deferred Items Collection":

1. **AVX-512 `vpcompressb` / RVV `vcompress.vm`**: Use native compress-store when available. Priority: Low. Depends on: AVX-512/RVV target support.

2. **Runtime-mask for wider types**: Binary-tree compaction for i16/i32/i64 (currently uses scalar fallback). Priority: Medium. Depends on: Task 4.

3. **Bounds check for overwrite semantics**: Validate `len(dst) >= lanes.Count[T]()` at the store site, not just `popcount(mask)`. Priority: Medium.

4. **AVX2 `vpslldq` cross-lane fix**: The prefix-sum stride-16 step needs `vperm2i128` to shift across 128-bit lane boundary. May already work via LLVM shufflevector lowering — verify. Priority: Medium. Depends on: Task 4.
