# IPv4 Parser Compiler Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix 4 TinyGo SPMD compiler bugs so the ipv4-parser example compiles, runs, and can serve as a benchmark.

**Architecture:** Incremental fixes to the TinyGo SPMD compiler's deferred switch phi handling, Varying[bool] constant generation, and multi-lane-count support. Each fix is validated with a progressively more complex test case: minimal switch → simplified 4-lane → full 4-loop ipv4-parser.

**Tech Stack:** Go, TinyGo compiler (LLVM IR generation), WASM SIMD128

**Design doc:** `docs/plans/2026-02-28-ipv4-parser-compiler-fixes-design.md`

---

### Task 1: Fix Deferred Switch Phi Vectorization (Bug A + C)

The deferred switch phi at `compiler.go:4055` gets scalar type `i32` from `getLLVMType(expr.Type())` because go/ssa's phi type is plain `int` (not `Varying[int]`). And `spmdCreateSwitchMergeSelect` at `spmd.go:5361` uses this same scalar type, so the cascaded select stays scalar. After `ReplaceAllUsesWith`, downstream icmp/trunc instructions have type mismatches. Additionally, the select is placed before the block terminator, but phi uses are earlier in the block (domination error).

**Files:**
- Modify: `tinygo/compiler/spmd.go:5322-5396` (`spmdCreateSwitchMergeSelect`)
- Modify: `tinygo/compiler/compiler.go:4054-4063` (deferred phi creation)
- Modify: `tinygo/compiler/compiler.go:1725-1743` (deferred phi resolution)

**Step 1: Add `spmdSwitchLaneCount` helper to `spmd.go`**

Add after `spmdIsSwitchDoneBlock` (line 5311):

```go
// spmdSwitchLaneCount returns the SPMD lane count for a switch chain by looking up
// which active loop contains the switch's done block. Returns 0 if not in a loop.
func (b *builder) spmdSwitchLaneCount(chainIdx int) int {
	chain := &b.spmdSwitchChains[chainIdx]
	if b.spmdLoopState == nil {
		return 0
	}
	// Check body blocks first, then loop blocks.
	if loop, ok := b.spmdLoopState.bodyBlocks[chain.doneBlock]; ok {
		return loop.laneCount
	}
	if loop, ok := b.spmdLoopState.loopBlocks[chain.doneBlock]; ok {
		return loop.laneCount
	}
	// Try case body blocks — doneBlock might be collapsed.
	for _, c := range chain.cases {
		if loop, ok := b.spmdLoopState.bodyBlocks[c.bodyBlock]; ok {
			return loop.laneCount
		}
	}
	return 0
}
```

**Step 2: Fix `spmdCreateSwitchMergeSelect` to force vectorization**

In `spmd.go:5359-5393`, replace the cascaded select logic:

```go
// Build cascaded select. Start with the entry/default value.
var result llvm.Value
phiType := b.getLLVMType(phi.Type())

// SPMD: if phi type is scalar but we're in an SPMD loop, vectorize it.
laneCount := b.spmdSwitchLaneCount(chainIdx)
if laneCount > 0 && phiType.TypeKind() != llvm.VectorTypeKind {
	phiType = llvm.VectorType(phiType, laneCount)
}

if defaultEdgeIdx >= 0 {
	result = b.getValue(phi.Edges[defaultEdgeIdx], getPos(phi))
} else if entryEdgeIdx >= 0 {
	result = b.getValue(phi.Edges[entryEdgeIdx], getPos(phi))
} else {
	result = llvm.ConstNull(phiType)
}

// Ensure result is vectorized for SPMD context.
if laneCount > 0 && result.Type().TypeKind() != llvm.VectorTypeKind {
	result = b.splatScalar(result, phiType)
}

// Apply cascaded selects from first case to last.
for ci := 0; ci < len(chain.cases); ci++ {
	edgeIdx, ok := caseEdges[ci]
	if !ok {
		continue
	}
	caseVal := b.getValue(phi.Edges[edgeIdx], getPos(phi))
	caseMask := chain.cases[ci].caseMask
	if caseMask.IsNil() {
		continue
	}

	// Ensure case value is vectorized for SPMD context.
	if laneCount > 0 && caseVal.Type().TypeKind() != llvm.VectorTypeKind {
		caseVal = b.splatScalar(caseVal, phiType)
	}

	// Broadcast match if needed.
	caseVal, result = b.spmdBroadcastMatch(caseVal, result)

	// Always use vector select in SPMD context.
	result = b.spmdMaskSelect(caseMask, caseVal, result)
}
```

**Step 3: Fix deferred phi type in `compiler.go`**

At `compiler.go:4054-4063`, vectorize the phi type:

```go
if isMergePhi {
	phiType := b.getLLVMType(expr.Type())
	// SPMD: vectorize phi type for switch merge inside SPMD loop.
	laneCount := b.spmdSwitchLaneCount(chainIdx)
	if laneCount > 0 && phiType.TypeKind() != llvm.VectorTypeKind {
		phiType = llvm.VectorType(phiType, laneCount)
	}
	phi := b.CreatePHI(phiType, "switch.merge.deferred")
	// ... rest unchanged
```

**Step 4: Fix select placement in deferred resolution**

At `compiler.go:1725-1743`, change insert point from "before terminator" to "before the deferred phi":

```go
for _, dsp := range b.spmdDeferredSwitchPhis {
	chain := &b.spmdSwitchChains[dsp.chainIdx]
	// Insert the cascaded select right before the deferred phi instruction.
	// This ensures the select dominates all uses of the phi in the same block.
	b.SetInsertPointBefore(dsp.llvm)
	selectVal, ok := b.spmdCreateSwitchMergeSelect(dsp.phi, dsp.chainIdx)
	// ... rest unchanged
```

**Step 5: Rebuild TinyGo**

Run: `cd /home/cedric/work/SPMD && make build-tinygo`
Expected: Build succeeds.

**Step 6: Test with minimal switch test**

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-minimal.wasm /tmp/ipv4-minimal.go`
Expected: Compiles without LLVM verification errors (the 2 errors from before — empty phi + domination — should be gone).

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-minimal.wasm`
Expected: Output shows `10 20 30 10` (values from switch cases).

**Step 7: Test with simplified ipv4 test**

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-test.wasm /tmp/ipv4-test.go`
Expected: Compiles without the 3 errors from before (empty phi, icmp mismatch, trunc mismatch).

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-test.wasm`
Expected: Correct IPv4 parsing output for test cases.

**Step 8: Check full ipv4-parser error count**

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-parser.wasm examples/ipv4-parser/main.go 2>&1 | grep -c 'error\|mismatch\|Invalid'`
Expected: Error count reduced from 17. Remaining errors should be from Bug B (Varying[bool] collision in 16-lane loops).

**Step 9: Commit**

```bash
cd tinygo && git add compiler/compiler.go compiler/spmd.go && \
git commit -m "fix: vectorize deferred switch phi type and select placement

Switch merge phis inside SPMD loops had scalar types from go/ssa
(e.g., i32 instead of <4 x i32>) because the SSA phi type comes from
the declared Go variable, not the runtime varying type. This caused
cascaded selects to stay scalar, producing type mismatches at icmp/trunc
downstream. Also fix select placement to use the phi position instead
of the block terminator, preventing domination violations."
```

---

### Task 2: Fix Varying[bool] Constant Lane Count (Bug B)

`Varying[bool]` constants (like `true:Varying[bool]`) map to `<16 x i1>` via `getLLVMType(Varying[bool])` because `128/1 = 16`. But in a 4-lane int loop, comparison results are `<4 x i32>` (WASM mask). Phi nodes that merge `<16 x i1>` constants with `<4 x i32>` values break. The fix: when creating `Varying[bool]` constants inside SPMD loops, use the loop's mask format instead of the SPMDType-derived lane count.

**Files:**
- Modify: `tinygo/compiler/spmd.go:393-411` (`createSPMDConst`)
- Modify: `tinygo/compiler/compiler.go:4730-4734` (`createConst` SPMD branch)
- Modify: `tinygo/compiler/compiler.go` (phi resolution for Varying[bool])

**Step 1: Override Varying[bool] constant creation in SPMD loop context**

The issue is that `createConst` (line 4732) dispatches to `createSPMDConst` which uses `getLLVMType(Varying[bool])` = `<16 x i1>`. But in a 4-lane loop on WASM, bool values should be `<4 x i32>` (the mask format).

In `compiler.go`, `createConst` is on `compilerContext` (not `builder`), so it has no access to the loop state. The fix goes in the caller: `getValue` which calls `createConst`. When `getValue` returns a `Varying[bool]` constant and we're in an SPMD loop, resize the vector to match the loop's mask format.

Find the `getValue` function and look at how it handles constants. The fix: after `createConst` produces a `<16 x i1>` for `Varying[bool]`, check if we're in an SPMD loop and convert to the loop's mask format.

In `compiler.go`, add a post-processing step in `getValue` (or in the `*ssa.Const` case of `createExpr`) that checks for Varying[bool] constants in SPMD context:

```go
// In createExpr, case *ssa.Const (around line 3403):
// After: return c.createConst(expr, getPos(expr)), nil
// Add SPMD Varying[bool] fixup:
val := c.createConst(expr, getPos(expr))
if b.spmdLoopState != nil {
	if spmdType, ok := expr.Type().(*types.SPMDType); ok && spmdType.IsVarying() {
		elemKind := spmdType.Elem().Underlying().(*types.Basic).Kind()
		if elemKind == types.Bool || elemKind == types.UntypedBool {
			// Varying[bool] constant: convert from <16 x i1> to loop's mask format.
			block := b.currentBlock
			if block != nil {
				if loop, ok := b.spmdLoopState.bodyBlocks[block.Index]; ok {
					maskType := llvm.VectorType(b.spmdMaskElemType(loop.laneCount), loop.laneCount)
					if val.Type() != maskType {
						// Convert: extract the scalar bool, then splat to mask format.
						if val.IsNull() {
							val = llvm.ConstNull(maskType)
						} else {
							val = llvm.ConstAllOnes(maskType)
						}
					}
				}
			}
		}
	}
}
return val, nil
```

**Step 2: Fix phi resolution for Varying[bool] type mismatches**

When the phi resolution loop adds incoming values to phis, it may encounter type mismatches between `<16 x i1>` (from Varying[bool] constant edges) and `<4 x i32>` (from WASM-wrapped comparison edges). The phi's LLVM type was set from one edge; the other edge's value has a different type.

In the phi resolution code (around `compiler.go:2030-2050`), after computing the incoming value, add a type reconciliation step:

```go
// After getting phiVal for each edge:
// If phi type and value type don't match, reconcile.
if phiVal.Type() != phi.llvm.Type() {
	// Both should be mask-like vectors. Convert to phi's type.
	if phi.llvm.Type().TypeKind() == llvm.VectorTypeKind && phiVal.Type().TypeKind() == llvm.VectorTypeKind {
		// Resize: e.g., <16 x i1> → <4 x i32> or vice versa.
		targetLanes := phi.llvm.Type().VectorSize()
		targetElem := phi.llvm.Type().ElementType()
		phiVal = b.spmdConvertMaskFormat(phiVal, targetLanes, targetElem)
	}
}
```

Add a helper `spmdConvertMaskFormat` to `spmd.go`:

```go
// spmdConvertMaskFormat converts a mask-like vector to a different format.
// Handles conversions like <16 x i1> → <4 x i32> (unwrap + resize + wrap)
// and <4 x i32> → <16 x i1> (unwrap + resize).
func (b *builder) spmdConvertMaskFormat(mask llvm.Value, targetLanes int, targetElem llvm.Type) llvm.Value {
	srcLanes := mask.Type().VectorSize()
	srcElem := mask.Type().ElementType()

	// If already matching, return as-is.
	if srcLanes == targetLanes && srcElem == targetElem {
		return mask
	}

	// Step 1: Unwrap to <N x i1> if needed.
	if srcElem != b.ctx.Int1Type() {
		mask = b.CreateTrunc(mask, llvm.VectorType(b.ctx.Int1Type(), srcLanes), "")
	}

	// Step 2: Resize if needed (truncate via shuffle or extend via splat).
	if srcLanes != targetLanes {
		mask = b.spmdResizeVector(mask, targetLanes, b.ctx.Int1Type())
	}

	// Step 3: Wrap to target element type if needed.
	if targetElem != b.ctx.Int1Type() {
		mask = b.CreateSExt(mask, llvm.VectorType(targetElem, targetLanes), "")
	}

	return mask
}
```

**Step 3: Rebuild and test**

Run: `cd /home/cedric/work/SPMD && make build-tinygo`
Expected: Build succeeds.

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-parser.wasm examples/ipv4-parser/main.go 2>&1 | head -30`
Expected: Significant error reduction. Count remaining errors and diagnose.

**Step 4: Iterate on remaining errors**

After each fix, rebuild and re-check error count. The remaining errors from Bug D (multi-lane-count mask interactions) may need:
- Mask format conversion when entering/leaving loops with different lane counts
- Resize operations for values crossing loop boundaries (e.g., `dotMask` array written in 16-lane loop, read via reduce in subsequent code)

Each iteration: identify error → trace LLVM types → add conversion/resize → rebuild → verify.

**Step 5: Commit**

```bash
cd tinygo && git add compiler/compiler.go compiler/spmd.go && \
git commit -m "fix: Varying[bool] constants use loop-aware mask format

Varying[bool] mapped to <16 x i1> (128/1=16 lanes) regardless of loop
context. In 4-lane int loops, comparisons produce <4 x i32> (WASM mask).
Phi nodes merging these types broke. Now Varying[bool] constants use the
active loop's mask format, and phi resolution converts between mask
formats when edges have mismatched types."
```

---

### Task 3: Fix Remaining Multi-Lane Errors and Get Full ipv4-parser Compiling (Bug D)

After Tasks 1-2, recompile the full ipv4-parser and fix any remaining LLVM verification errors. These likely involve mask format mismatches at loop boundaries or in reduce operations that cross lane counts.

**Files:**
- Modify: `tinygo/compiler/spmd.go` (mask conversion helpers)
- Modify: `tinygo/compiler/compiler.go` (reduce builtins, loop transitions)

**Step 1: Compile full ipv4-parser and catalog remaining errors**

Run: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd GOROOT=$(pwd)/go WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-parser.wasm examples/ipv4-parser/main.go 2>&1`
Expected: Either compiles cleanly or has a small number of remaining errors.

**Step 2: Fix each remaining error**

For each error:
1. Identify the LLVM instruction and its operand types
2. Trace back to the SSA instruction and SPMD loop context
3. Add type conversion/broadcast at the point where types diverge
4. Rebuild: `make build-tinygo`
5. Re-check: recompile ipv4-parser

**Step 3: Run the compiled ipv4-parser**

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-parser.wasm`
Expected output:
```
'192.168.1.1' -> 192.168.1.1
'10.0.0.1' -> 10.0.0.1
'255.255.255.255' -> 255.255.255.255
'0.0.0.0' -> 0.0.0.0
'127.0.0.1' -> 127.0.0.1
'192.168.01.1' -> ERROR: ...
'256.1.1.1' -> ERROR: ...
'192.168.1' -> ERROR: ...
'192.168.1.1.1' -> ERROR: ...
'192.168.1.a' -> ERROR: ...
```

**Step 4: Run existing SPMD tests to check for regressions**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -20`
Expected: All existing SPMD tests pass.

**Step 5: Commit**

```bash
cd tinygo && git add compiler/compiler.go compiler/spmd.go && \
git commit -m "fix: resolve multi-lane-count mask interactions for ipv4-parser

Fix remaining LLVM verification errors from mask format mismatches
when values cross between loops of different lane counts (16-lane byte
vs 4-lane int). The ipv4-parser now compiles and runs correctly."
```

---

### Task 4: Add Benchmark Harness and Measure Performance

**Files:**
- Modify: `examples/ipv4-parser/main.go` (add benchmark timing)

**Step 1: Add benchmark timing to ipv4-parser**

Modify `examples/ipv4-parser/main.go` to add a benchmark loop similar to other examples (hex-encode pattern: N iterations, M runs, print ns/op):

```go
func main() {
	testCases := []string{
		"192.168.1.1",
		"10.0.0.1",
		"255.255.255.255",
		"0.0.0.0",
		"127.0.0.1",
		"192.168.01.1",
		"256.1.1.1",
		"192.168.1",
		"192.168.1.1.1",
		"192.168.1.a",
	}

	// Functional test
	for _, addr := range testCases {
		ip, err := parseIPv4(addr)
		if err != nil {
			fmt.Printf("'%s' -> ERROR: %v\n", addr, err)
		} else {
			fmt.Printf("'%s' -> %d.%d.%d.%d\n", addr, ip[0], ip[1], ip[2], ip[3])
		}
	}

	// Benchmark: parse valid addresses N times
	validAddrs := []string{
		"192.168.1.1",
		"10.0.0.1",
		"255.255.255.255",
		"0.0.0.0",
		"127.0.0.1",
	}
	const iterations = 10000
	const runs = 7

	for run := 0; run < runs; run++ {
		start := nanotime()
		for i := 0; i < iterations; i++ {
			for _, addr := range validAddrs {
				parseIPv4(addr)
			}
		}
		elapsed := nanotime() - start
		nsPerOp := elapsed / int64(iterations*len(validAddrs))
		fmt.Printf("Run %d: %d ns/op (%d ops)\n", run+1, nsPerOp, iterations*len(validAddrs))
	}
}

//go:linkname nanotime runtime.nanotime
func nanotime() int64
```

**Step 2: Compile SIMD and scalar versions**

Run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -simd=true -o /tmp/ipv4-simd.wasm examples/ipv4-parser/main.go
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -simd=false -o /tmp/ipv4-scalar.wasm examples/ipv4-parser/main.go
```

**Step 3: Run benchmarks with wasmtime**

Run:
```bash
wasmtime run /tmp/ipv4-simd.wasm
wasmtime run /tmp/ipv4-scalar.wasm
```

Compare ns/op between SIMD and scalar versions to compute speedup ratio.

**Step 4: Commit**

```bash
git add examples/ipv4-parser/main.go && \
git commit -m "feat: add benchmark harness to ipv4-parser example"
```

---

### Task 5: Update E2E Tests and Documentation

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh` (promote ipv4-parser from compile-fail to run-pass)
- Modify: `docs/ipv4-parser-status.md` (update status)

**Step 1: Promote ipv4-parser in E2E test script**

Move ipv4-parser from the compile-fail category to run-pass in the E2E test script.

**Step 2: Run full E2E test suite**

Run: `./test/e2e/spmd-e2e-test.sh`
Expected: ipv4-parser passes as run-pass. No regressions in other tests.

**Step 3: Update ipv4-parser status doc**

Update `docs/ipv4-parser-status.md` to reflect all bugs fixed, compilation and execution successful.

**Step 4: Commit**

```bash
git add test/e2e/spmd-e2e-test.sh docs/ipv4-parser-status.md && \
git commit -m "feat: promote ipv4-parser to run-pass in E2E tests"
```
