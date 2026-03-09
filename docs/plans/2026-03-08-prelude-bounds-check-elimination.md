# Prelude Bounds Check Elimination for SPMD Vector Index

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace N per-lane scalar bounds checks with a single vectorized max-lane check in `spmdVectorIndexString` and `spmdVectorIndexArray`.

**Architecture:** Currently, each `s[varyingIndex]` in SPMD generates N scalar bounds checks (extract_lane + compare + branch per lane). Instead, compute the maximum index across all lanes using a single SIMD reduction (`i32x4.extract_lane` × N → `i32.gt_u` chain → one `br_if`), or when possible, use `spmdIndexMaxValue` extended with ADD support to elide bounds checks entirely against known lengths. The optimization has two independent parts: (1) extend `spmdIndexMaxValue` to handle ADD expressions, enabling static elision; (2) replace the per-lane OR-chain with a SIMD-aware max-reduction for runtime checks.

**Tech Stack:** Go, TinyGo compiler (`compiler/spmd.go`), LLVM IR, WebAssembly SIMD128

---

## Background

### Current Per-Lane Bounds Check Pattern

In `spmdVectorIndexString` (spmd.go:4108-4130), for each `s[varyingIndex]`:

```go
// Current: N scalar comparisons OR'd together
anyOOB := false
for lane := 0; lane < laneCount; lane++ {
    oob := laneIdxs[lane] >= length  // extract_lane + i32.ge_u
    anyOOB = anyOOB || oob           // i32.or
}
if anyOOB { panic("lookupPanic") }   // br_if
```

**WASM output per check** (5 ops × N lanes = 20 ops for 4 lanes):
```wat
i32x4.extract_lane 3    ; extract lane index
local.get $len          ; string length
i32.ge_u                ; index >= len?
;; OR with accumulator
i32x4.extract_lane 2    ; ...repeat for each lane
```

### The ipv4-Parser Problem

The digit conversion loop (`go for field, start := range starts`) generates 6 distinct string accesses per case × 3 cases = up to 18 bounds check groups (some shared), each with 4 per-lane scalar comparisons. The WAT output shows **~63 scalar bounds check ops** — the single largest cost in the function.

### Existing Elision: `spmdIndexMaxValue`

`spmdIndexMaxValue` (spmd.go:4413) computes a compile-time upper bound on an SSA index expression. It handles:
- Constants, ChangeType, Convert (narrowing)
- BinOp: `SHR`, `AND`, `REM`
- Type-based fallback (unsigned N-bit → 2^N - 1)

**Gap:** It does NOT handle `token.ADD`. For the ipv4-parser's `s[start + 1]`, `s[start + 2]`, it cannot compute a max, so all bounds checks fall through to the per-lane OR chain.

---

## Task 1: Extend `spmdIndexMaxValue` with ADD Support

**Files:**
- Modify: `tinygo/compiler/spmd.go` (function `spmdIndexMaxValue`, line 4439)

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_test.go` (after the existing `spmdIndexMaxValue` tests, or create a new test if none exist):

```go
func TestSPMDIndexMaxValueADD(t *testing.T) {
	// Build SSA for: x % 3 + 2
	// Expected max: (3-1) + 2 = 4
	src := `package main

func f(s string) {
	for i := range 16 {
		_ = s[i%3+2]
	}
}

func main() { f("hello world!!!!!") }
`
	pkg := buildSSAForTest(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	// Find the Index instruction's Index operand (the BinOp ADD).
	var indexOp ssa.Value
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if idx, ok := instr.(*ssa.Index); ok {
				indexOp = idx.Index
				break
			}
		}
		if indexOp != nil {
			break
		}
	}
	if indexOp == nil {
		t.Fatal("no Index instruction found")
	}

	maxVal, known := spmdIndexMaxValue(indexOp)
	if !known {
		t.Fatal("spmdIndexMaxValue returned unknown for i%3+2")
	}
	if maxVal != 4 {
		t.Errorf("spmdIndexMaxValue(i%%3+2) = %d, want 4", maxVal)
	}
}
```

**Note:** If `buildSSAForTest` or `spmdIndexMaxValue` are not exported/accessible from the test file, the test may need to be structured differently — check the existing test patterns in `spmd_test.go` and `spmd_llvm_test.go`. If `spmdIndexMaxValue` is unexported (lowercase), the test can still call it from within the same package. The important thing is to test the ADD pattern specifically.

**Step 2: Run the test to verify it fails**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go make GO=/home/cedric/work/SPMD/go/bin/go test-compiler 2>&1 | grep -A5 TestSPMDIndexMaxValueADD`

Or: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMDIndexMaxValueADD -v`

Expected: FAIL — `spmdIndexMaxValue` returns `(0, false)` for ADD expressions.

**Step 3: Implement ADD support**

In `spmdIndexMaxValue` (spmd.go:4439), inside the `case *ssa.BinOp:` switch, add a `token.ADD` case:

```go
		case token.ADD:
			// x + y: upper bound is maxOf(x) + maxOf(y), guarding against overflow.
			if maxX, okX := spmdIndexMaxValue(val.X); okX {
				if maxY, okY := spmdIndexMaxValue(val.Y); okY {
					sum := maxX + maxY
					if sum >= maxX && sum >= maxY { // overflow guard
						return sum, true
					}
				}
			}
```

Place this after the existing `case token.REM:` block (line ~4463).

**Step 4: Run the test to verify it passes**

Same command as Step 2.

Expected: PASS.

**Step 5: Run all existing SPMD tests to check for regressions**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run "TestSPMD" -v -count=1`

Expected: All PASS.

**Step 6: Commit**

```
git add compiler/spmd.go compiler/spmd_test.go
git commit -m "feat: extend spmdIndexMaxValue with ADD support for bounds elision"
```

---

## Task 2: Replace Per-Lane OR Chain with Vectorized Max Check

**Files:**
- Modify: `tinygo/compiler/spmd.go` (functions `spmdVectorIndexString` line 4122, `spmdVectorIndexArray` line 4215)

This is the main performance optimization. Instead of extracting each lane, comparing, and ORing scalars, compute `max(allLanes)` from the vector index directly, then do a single scalar comparison.

### Approach: Use SIMD `max` or Scalar Reduction

For a `<4 x i32>` index vector, the max across lanes can be computed as:

```
; Reduce <4 x i32> to scalar max
shuffled = i8x16.shuffle(index, index, [8,9,10,11, 12,13,14,15, 0,1,2,3, 4,5,6,7])
maxPair  = i32x4.max_u(index, shuffled)      ; max of (lane0,lane2) and (lane1,lane3)
shuffled2 = i8x16.shuffle(maxPair, maxPair, [4,5,6,7, 0,1,2,3, 8,9,10,11, 12,13,14,15])
maxAll   = i32x4.max_u(maxPair, shuffled2)   ; max of all 4 in lane 0
result   = i32x4.extract_lane 0              ; scalar max
```

This is 5 SIMD ops vs 12 scalar ops (4 extract + 4 compare + 4 OR) — a 2.4x reduction in bounds check instruction count.

**Step 1: Write the failing test**

This is a codegen/performance test. Add a test to `tinygo/compiler/spmd_llvm_test.go` that checks the generated LLVM IR pattern:

```go
func TestSPMDVectorIndexBoundsPrelude(t *testing.T) {
	src := `package main

func f(s string) byte {
	var result byte
	for i := range 4 {
		result += s[i]
	}
	return result
}

func main() { f("hello") }
`
	// Build and verify the IR does NOT contain per-lane extract+compare pattern
	// but instead uses a max reduction or equivalent.
	ir := buildSPMDIR(t, src, "f")

	// Count occurrences of "extractelement" in the bounds check section.
	// Before optimization: 4 extractelement + 4 icmp + 4 or = 12 ops
	// After optimization: should use shuffle+max reduction pattern
	extractCount := strings.Count(ir, "extractelement")
	// With the prelude optimization, we expect at most 1 extractelement
	// for the final scalar max extraction (vs 4+ without it).
	// The gather still needs extractelement, so just check the bounds section.

	// Alternative: check that "llvm.umax" or "shufflevector" appears near "icmp uge"
	// This test may need adjustment based on exact IR output.
	if extractCount > 20 {
		// Rough threshold: without optimization there are 4 extracts per bounds check.
		// With 6 string accesses that's 24 extract just for bounds.
		// With prelude: max 6 extract (one per bounds check) + gather extracts.
		t.Logf("extractelement count: %d (may indicate per-lane bounds checks)", extractCount)
	}
}
```

**Note:** The exact test assertions will depend on how `buildSPMDIR` works in the existing test infrastructure. Check `spmd_llvm_test.go` for the pattern used by other tests (e.g., `TestSPMDVectorIndexStringBoundsElision`). If no IR-level test helper exists, this test may need to be an E2E WASM inspection test instead.

**Step 2: Implement the vectorized max check**

Add a helper function in `spmd.go`:

```go
// spmdVectorMaxScalar reduces a vector of unsigned integers to the scalar
// maximum across all lanes. Uses pairwise shuffle+max for log2(N) reduction
// steps. Returns a scalar i32/i64 value.
func (b *builder) spmdVectorMaxScalar(vec llvm.Value) llvm.Value {
	vecType := vec.Type()
	laneCount := vecType.VectorSize()
	elemType := vecType.ElementType()

	// For single-lane vectors, just extract.
	if laneCount == 1 {
		return b.CreateExtractElement(vec, llvm.ConstInt(b.ctx.Int32Type(), 0, false), "")
	}

	// Pairwise reduction: shuffle and max until 1 lane remains.
	current := vec
	n := laneCount
	for n > 1 {
		half := n / 2
		// Build shuffle mask: swap pairs.
		// For n=4: [2,3,0,1] → then for n=2: [1,0,x,x]
		indices := make([]llvm.Value, laneCount)
		for i := 0; i < laneCount; i++ {
			var idx int
			if i < n {
				idx = (i + half) % n
			} else {
				idx = laneCount // undef
			}
			indices[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(idx), false)
		}
		mask := llvm.ConstVector(indices, false)
		shuffled := b.CreateShuffleVector(current, llvm.Undef(vecType), mask, "spmd.max.shuf")

		// Use unsigned max intrinsic or manual compare+select.
		// LLVM has @llvm.umax.vNiM intrinsics.
		maxIntrinsic := b.getLifetimeIntrinsic("llvm.umax", vecType)
		if maxIntrinsic.IsNil() {
			// Fallback: compare + select
			cmp := b.CreateICmp(llvm.IntUGT, current, shuffled, "")
			current = b.CreateSelect(cmp, current, shuffled, "spmd.max.sel")
		} else {
			current = b.CreateCall(maxIntrinsic.GlobalValueType(), maxIntrinsic, []llvm.Value{current, shuffled}, "spmd.max")
		}
		n = half
	}

	return b.CreateExtractElement(current, llvm.ConstInt(b.ctx.Int32Type(), 0, false), "spmd.max.scalar")
}
```

**Important:** The `llvm.umax` intrinsic may not be available via TinyGo's LLVM bindings. Check `b.mod.NamedFunction("llvm.umax.v4i32")` or use the fallback compare+select approach. The fallback is still much better than per-lane extraction:
- 4 lanes: 2 shuffle + 2 select + 1 extract = 5 ops (vs 12 scalar ops)
- 16 lanes: 4 shuffle + 4 select + 1 extract = 9 ops (vs 48 scalar ops)

**Step 3: Replace per-lane bounds check in `spmdVectorIndexString`**

In `spmdVectorIndexString` (line 4122), replace the per-lane OR chain:

```go
		if !canElide {
			// Vectorized prelude: compute max index across all lanes,
			// then do a single scalar bounds check.
			maxIdx := b.spmdVectorMaxScalar(index)
			// Extend to uintptr if needed (index is already in its vector elem type).
			if maxIdx.Type().IntTypeWidth() < b.uintptrType.IntTypeWidth() {
				maxIdx = b.CreateZExt(maxIdx, b.uintptrType, "")
			}
			oob := b.CreateICmp(llvm.IntUGE, maxIdx, length, "spmd.bounds.oob")
			b.createRuntimeAssert(oob, "lookup", "lookupPanic")
		}
```

This replaces the `for lane` loop (lines 4123-4128).

**Step 4: Apply the same change to `spmdVectorIndexArray`**

In `spmdVectorIndexArray` (line 4215), make the identical replacement:

```go
		if !canElide {
			maxIdx := b.spmdVectorMaxScalar(index)
			if maxIdx.Type().IntTypeWidth() < b.uintptrType.IntTypeWidth() {
				maxIdx = b.CreateZExt(maxIdx, b.uintptrType, "")
			}
			oob := b.CreateICmp(llvm.IntUGE, maxIdx, arrayLen, "spmd.bounds.oob")
			b.createRuntimeAssert(oob, "lookup", "lookupPanic")
		}
```

**Step 5: Run all SPMD tests**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run "TestSPMD" -v -count=1`

Expected: All PASS.

**Step 6: Commit**

```
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "perf: replace per-lane bounds checks with vectorized max reduction"
```

---

## Task 3: Rebuild and Benchmark

**Files:**
- No code changes — build and test only

**Step 1: Rebuild TinyGo**

Run: `cd /home/cedric/work/SPMD && rm -rf ~/.cache/tinygo && make build-tinygo`

**Step 2: Compile ipv4-parser**

Run: `cd /home/cedric/work/SPMD && make compile EXAMPLE=ipv4-parser`

Expected: SUCCESS.

**Step 3: Run ipv4-parser and verify correctness**

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs ipv4-parser.wasm`

Expected: All 10 test cases produce correct output.

**Step 4: Compile integration test with benchmark**

Run: `cd /home/cedric/work/SPMD && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o /tmp/spmd-e2e/integ_ipv4-parser.wasm test/integration/spmd/ipv4-parser/main.go`

**Step 5: Run benchmark**

Run: `wasmtime run --dir=. /tmp/spmd-e2e/integ_ipv4-parser.wasm`

Expected: SPMD should be faster than before (was 0.52x, target ~0.65x or better — 20% improvement from bounds check reduction).

**Step 6: Count bounds check instructions in WAT**

Run: `wasm2wat ipv4-parser.wasm | grep -c 'lookupPanic'`

Expected: Fewer `lookupPanic` call sites (still same count since each call site exists, but the per-lane extract+compare chain before each should be shorter).

Run: `wasm2wat ipv4-parser.wasm | grep -c 'i32.ge_u'`

Expected: Significantly fewer `i32.ge_u` comparisons (was ~21 per-lane checks with 4 compares each ≈ 84; should drop to ~21 single comparisons ≈ 21).

**Step 7: Run full E2E suite to verify no regressions**

Run: `bash test/e2e/spmd-e2e-test.sh`

Expected: 31 run pass (no regressions), 36 compile pass, 0 run fail.

---

## Task 4: Inspect and Fine-Tune WAT Output

**Files:**
- Possibly modify: `tinygo/compiler/spmd.go`

**Step 1: Disassemble and inspect the parseIPv4 bounds check section**

Run: `wasm2wat ipv4-parser.wasm > /tmp/ipv4-after.wat`

Compare the bounds check sections before and after:
- Before: `extract_lane × 4 + i32.ge_u × 4 + i32.or × 4 + br_if` per check (17 ops)
- After: `shuffle + max + shuffle + max + extract_lane + i32.ge_u + br_if` per check (7 ops)

**Step 2: Check if LLVM optimized the shuffle+max pattern**

LLVM may recognize the pairwise reduction pattern and optimize it further. Inspect whether `i32x4.max_u` is used or if it fell back to compare+select.

If LLVM didn't optimize well, consider using WASM-specific intrinsics:
- `i32x4.max_u` is a native WASM SIMD instruction
- The shuffle+max pattern maps directly to 2 shuffles + 2 max_u + 1 extract

**Step 3: If needed, adjust the shuffle mask generation**

The shuffle indices must match what LLVM's WASM backend expects. For `<4 x i32>`:
- Step 1: shuffle [2,3,0,1], max_u → max(lane0,lane2), max(lane1,lane3) in lanes 0,1
- Step 2: shuffle [1,0,2,3], max_u → max of all 4 in lane 0

For `<16 x i8>`:
- 4 reduction steps needed (16→8→4→2→1)

**Step 4: Commit any adjustments**

```
git commit -m "perf: fine-tune vectorized bounds check reduction for WASM"
```

---

## Edge Cases

1. **Masked indices (inactive lanes):** The existing `spmd.idx.clamp` select zeros inactive lane indices before the bounds check. With the max reduction, zeroed lanes don't affect the max (since 0 < any valid length). This is correct.

2. **Signed indices:** `spmdExtendIndex` handles sign/zero extension. The max reduction uses unsigned max (`IntUGT`), which is correct for bounds checking (negative indices, when reinterpreted as unsigned, are very large and will fail the check).

3. **16-lane byte vectors:** `<16 x i8>` needs 4 reduction steps. The shuffle masks must be byte-level (since WASM shuffle operates on `i8x16`). The max reduction works the same way but with `i8x16.max_u`.

4. **2-lane i64 vectors:** `<2 x i64>` needs 1 reduction step. `i64x2` doesn't have `max_u` on WASM — must use compare+select fallback.

5. **ADD overflow in `spmdIndexMaxValue`:** If `maxX + maxY` overflows uint64, the overflow guard `sum >= maxX && sum >= maxY` catches it and returns `(0, false)`, falling back to the runtime check.

6. **Non-constant string length:** The runtime check (`maxIdx >= length`) still works because `length` is extracted from the string header at runtime. Only the per-lane decomposition changes.

---

## Expected Impact

### Instruction Count Reduction

| Pattern | Before | After | Savings |
|---------|--------|-------|---------|
| 1 bounds check (4 lanes) | 12 ops (4×extract + 4×cmp + 4×or) | 5 ops (2×shuffle + 2×max + 1×extract) + 2 (cmp + br) = 7 ops | 42% per check |
| ipv4-parser total (21 checks) | 252 scalar ops | 147 ops | ~105 ops saved |
| ipv4-parser total function | 323 ops | ~218 ops | ~32% reduction |

### Benchmark Projection

- Current: SPMD 0.52x (1.92× slower than scalar)
- After bounds opt: SPMD ~0.68x estimated (still slower, but ~30% improvement)
- The remaining gap is from gather operations (96 ops, inherent to algorithm)

### Potential Static Elision Wins

With ADD support in `spmdIndexMaxValue`:
- `s[i % 3 + 2]` → max = 4, elide if `len(s) > 4` (known at compile time for const strings)
- `s[start + 1]` where `start` comes from dot positions → max unknown (dot positions are runtime), cannot statically elide
- But for patterns like array indexing with `i % N + K`, the check is fully eliminated

The ipv4-parser's string accesses use runtime-computed `start` values (from dot positions), so Task 1's static elision won't help there directly. Task 2's vectorized max is the primary win for this case.
