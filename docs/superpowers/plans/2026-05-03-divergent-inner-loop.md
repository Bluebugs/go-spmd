# Divergent Inner-Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable `for _, v := range varyingSlice` inside a `go for` to iterate per-lane slices independently with mask narrowing as lanes complete or break, closing the array-counting regression and reaching e2e baseline 94/0/93/0/11.

**Architecture:** Pure TinyGo lowering (no new SSA opcodes). Three layered changes: (1) fix `*ssa.Alloc` materialization so a `Varying[[]T]` alloca is `[N x slice_struct]` not `[1 x slice_struct]`; (2) helper `spmdEmitDivergentInnerLoop` detects `Varying[[]T]` ranges, builds per-lane base-ptr / len vectors, emits an inner loop whose iter `j` runs `[0, max(lens))` with active mask = `outer_mask AND (j < len[lane]) AND ~broken_mask`; (3) per-lane gather inside the body, body lowered with the existing predication infrastructure consuming the new active mask.

**Tech Stack:** Go (forked at `/home/cedric/work/SPMD/go`), TinyGo (`/home/cedric/work/SPMD/tinygo`), x-tools-spmd (`/home/cedric/work/SPMD/x-tools-spmd`), LLVM 19.1.2.

**Spec:** `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-03-divergent-inner-loop-design.md`

---

## Build & test commands

**Forked Go on PATH** (required for SPMD compilation):
```bash
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd
```

**Build TinyGo** (after every TinyGo edit):
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo
```

**Compile + run a test** (WASM):
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/x.wasm <test>.go
wasmtime /tmp/x.wasm
```

**Dump LLVM IR** for inspection:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/x.wasm <test>.go > /tmp/x.ll 2>&1
```

**Validate IR** (catches the original UB silently):
```bash
tinygo/llvm-build/bin/llvm-as /tmp/x.ll -o /tmp/x.bc
```

**Run e2e suite**:
```bash
bash test/e2e/spmd-e2e-test.sh
```

**Sentinel n-body**:
```bash
cd tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
```
Expected output: `-0.169075164` then `-0.169078071`.

---

## File structure (this plan touches)

| File | Repo | Responsibility |
|---|---|---|
| `tinygo/compiler/compiler.go` | tinygo | `getLLVMType` for `*types.SPMDType` Struct/Array branch — honor `typ.Lanes()` |
| `tinygo/compiler/spmd.go` | tinygo | New helper functions for divergent inner loop emission, varying slice detection, per-lane extraction |
| `tinygo/compiler/spmd_test.go` | tinygo | Unit tests for the new helpers |
| `test/integration/spmd/array-counting/main.go` | parent | Existing test, expected to PASS after fix |
| `test/integration/spmd/varying-slice-empty/main.go` | parent | NEW — empty-lane edge case |
| `test/integration/spmd/varying-slice-continue/main.go` | parent | NEW — `continue` semantics |
| `test/integration/spmd/varying-slice-break/main.go` | parent | NEW — `break` semantics |
| `test/integration/spmd/varying-slice-multi/main.go` | parent | NEW — combined edge cases |
| `test/e2e/spmd-e2e-test.sh` | parent | NEW e2e entries for the four new tests |

---

## Phase 0 — Pre-flight verification

### Task 0.1: Confirm starting state and toolchain freshness

**Files:** None modified.

- [ ] **Step 1: Verify clean working tree on relevant repos**

```bash
cd /home/cedric/work/SPMD
git status --short
cd tinygo && git status --short
cd ../x-tools-spmd && git status --short
```
Expected: clean (or already-known uncommitted v6.1 work). Note any uncommitted changes — these are starting state.

- [ ] **Step 2: Build TinyGo and verify v6.1 baseline**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject" | sed 's/\x1b\[[0-9;]*m//g'
```
Expected: **94/0/92/1/11** with `array-counting` as the only run failure.

- [ ] **Step 3: Verify n-body sentinel**

Run the sentinel command from above. Expected: `-0.169075164` then `-0.169078071`. If anything fails, STOP — fix discrepancy before starting.

- [ ] **Step 4: Capture array-counting starting IR for later comparison**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/ac-before.wasm test/integration/spmd/array-counting/main.go > /tmp/ac-before.ll 2>&1
grep -B2 -A 80 "define.*countArrays" /tmp/ac-before.ll > /tmp/ac-before-snippet.ll
```
This snippet shows the broken state (`alloca [1 x { ptr, i32, i32 }]`, lane-0-only iteration). Keep for diff after fixes.

---

## Phase 1 — Alloca sizing fix

### Task 1.1: Add unit test for `Varying[[]int]` alloca size

**Files:**
- Test: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (append a new TestSPMD* function)

- [ ] **Step 1: Read existing spmd_test.go conventions**

```bash
grep -nB 1 -A 25 "^func TestSPMD" /home/cedric/work/SPMD/tinygo/compiler/spmd_test.go | head -60
```
Note how a small Go program is compiled to LLVM IR for inspection — use the same helper.

- [ ] **Step 2: Append the failing test**

```go
// TestSPMDVaryingSliceAllocaSize asserts that a Varying[[]int] alloca
// inside a SPMD loop is sized [N x slice_struct] where N is the loop's
// lane count, not [1 x slice_struct]. The latter caused the v6.1
// array-counting UB (write 4 lanes into 1-lane alloca).
func TestSPMDVaryingSliceAllocaSize(t *testing.T) {
	src := `package p

func F(arrays [][]int) []int {
	result := make([]int, len(arrays))
	go for i, secondLevel := range arrays {
		t := 0
		for _, v := range secondLevel {
			t += v
		}
		result[i] = t
	}
	return result
}
`
	ir := compileSPMDToLLVM(t, src, "F")
	// Look for the secondLevel alloca. Expected: [4 x { ptr, i32, i32 }] on WASM
	// (4-lane outer rangeindex over []int). The pre-fix value [1 x { ptr, i32, i32 }]
	// is the bug.
	if !strings.Contains(ir, "alloca [4 x { ptr, i32, i32 }]") {
		t.Errorf("expected secondLevel alloca [4 x { ptr, i32, i32 }] in IR; got:\n%s",
			extractAllocaLines(ir, "secondLevel"))
	}
	if strings.Contains(ir, "alloca [1 x { ptr, i32, i32 }]") {
		t.Errorf("found stale [1 x ...] alloca (pre-fix size); got:\n%s",
			extractAllocaLines(ir, "secondLevel"))
	}
}
```

If `compileSPMDToLLVM` and `extractAllocaLines` helpers don't exist, model after the existing `TestSPMD*` tests' helpers (they likely use a `compileSSA` or similar wrapper). Alternatively use a direct command invocation via `exec.Command` with `-internal-printir` flag and capture stdout.

- [ ] **Step 3: Run test to verify it FAILS (TDD red)**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMDVaryingSliceAllocaSize -v
```
Expected: FAIL with "expected secondLevel alloca [4 x { ptr, i32, i32 }] in IR".

### Task 1.2: Fix getLLVMType for non-vectorizable Varying types

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go:557-567`

- [ ] **Step 1: Read the current Struct/Array branch**

```bash
sed -n '545,590p' /home/cedric/work/SPMD/tinygo/compiler/compiler.go
```
The current code uses `c.spmdLaneCount(elemType)` for non-vectorizable types — this is the natural width for `T`, but for `[]int` it returns 1 because slice headers can't be vectorized. We need it to honor `typ.Lanes()` first (set by Pass A's classifier).

- [ ] **Step 2: Apply the fix**

Replace lines 557-567 (the `case llvm.StructTypeKind, llvm.ArrayTypeKind:` block) with:

```go
			case llvm.StructTypeKind, llvm.ArrayTypeKind:
				// LLVM does not support vectors of aggregate types (structs, arrays).
				// Slices, interfaces, and other composite Go types lower to structs.
				// Use [N x elemType] array representation so the LLVM IR stays valid.
				//
				// SPMD v7: prefer the type-encoded lane count when set by Pass A's
				// classifier in the SSA predication pass. This is the canonical source
				// for varying values inside SPMD loop scope. Falls back to
				// spmdLaneCount(elemType) (the elem-natural width) for width-free
				// types (function signatures, global vars, abstract types).
				var n int
				if typ.Lanes() > 0 {
					n = typ.Lanes()
				} else {
					n = c.spmdLaneCount(elemType)
				}
				if n < 1 {
					n = 1
				}
				return llvm.ArrayType(elemType, n)
```

- [ ] **Step 3: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
```
Expected: clean build.

- [ ] **Step 4: Run the unit test (TDD green)**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMDVaryingSliceAllocaSize -v
```
Expected: PASS.

### Task 1.3: Verify IR validates and sentinels still pass

**Files:** None modified.

- [ ] **Step 1: array-counting IR no longer has UB**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/ac-after.wasm test/integration/spmd/array-counting/main.go > /tmp/ac-after.ll 2>&1
tinygo/llvm-build/bin/llvm-as /tmp/ac-after.ll -o /tmp/ac-after.bc
echo "EXIT=$?"
```
Expected: EXIT=0 (IR validates clean — no more `[1 x slice]` alloca with `[4 x slice]` store).

- [ ] **Step 2: array-counting still produces wrong output (expected at this phase)**

```bash
wasmtime /tmp/ac-after.wasm
```
Expected: still `Array sums: [3 3 3 3]`. The alloca is now correctly sized but the inner loop still reads only lane 0. Phase 2-3 fix the iteration logic.

- [ ] **Step 3: Sentinels**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject" | sed 's/\x1b\[[0-9;]*m//g'
```
Expected: n-body still `-0.169075164 / -0.169078071`. E2E still **94/0/92/1/11** (no regression; only array-counting still failing because the iteration logic is unchanged).

---

## Phase 2 — Per-lane base/len extraction helper

### Task 2.1: Investigation — identify the inner-loop SSA shape

**Files:** None modified.

- [ ] **Step 1: Dump array-counting SSA before TinyGo compiles it**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go 2>&1 > /tmp/ac.ll
grep -B 2 -A 100 "define.*countArrays" /tmp/ac.ll
```
Identify:
- The `secondLevel` alloca and its store (now `[4 x slice]`)
- The inner `rangeindex.loop1` / `rangeindex.body2` blocks
- The IndexAddr inside `rangeindex.body2` that loads `value` from `secondLevel`'s ptr
- The bound check that uses lane 0's len (the bug)

Note the SSA-level `*ssa.Range`, `*ssa.Next`, or `*ssa.IndexAddr` instructions — they're what we'll intercept.

- [ ] **Step 2: Read TinyGo's range-statement compilation**

```bash
grep -nE "rangeindex.body|case \*ssa.IndexAddr|spmdLoopState\.activeLoops" /home/cedric/work/SPMD/tinygo/compiler/compiler.go | head -20
```
Find where the range body block is compiled. The IndexAddr that produces the inner loop's element address will likely be the dispatch hook for the divergent path.

- [ ] **Step 3: Document findings**

Write a short note to `/tmp/divergent-inner-loop-investigation.md` capturing:
- Which SSA blocks produce the inner loop (block names, instruction types)
- Which TinyGo function compiles `*ssa.IndexAddr` for slice indexing
- Where the inner-loop iter phi lives (likely `rangeindex.loop1.<iter>`)
- Where the inner-loop bound is computed (likely in `rangeindex.loop1`)

This investigation informs the dispatch design in Task 3.1.

### Task 2.2: Add helper `spmdExtractVaryingSliceParts`

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (append a new helper)
- Test: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (append)

- [ ] **Step 1: Write test (TDD red)**

Append to `spmd_test.go`:

```go
// TestSPMDExtractVaryingSliceParts verifies that the helper produces a
// <N x ptr> base vector and a <N x i32> len vector from an [N x slice_struct]
// in-memory representation.
func TestSPMDExtractVaryingSliceParts(t *testing.T) {
	src := `package p

func F(arrays [][]int) int {
	total := 0
	go for _, secondLevel := range arrays {
		_ = secondLevel
		total++
	}
	return total
}
`
	ir := compileSPMDToLLVM(t, src, "F")
	// After extraction, expect explicit insertelement <4 x ptr> and
	// insertelement <4 x i32> chains for base/len.
	if !strings.Contains(ir, "insertelement <4 x ptr>") {
		t.Errorf("expected per-lane base ptr insertelement chain in IR")
	}
	if !strings.Contains(ir, "insertelement <4 x i32>") {
		t.Errorf("expected per-lane len insertelement chain in IR")
	}
}
```

This test will fail until Task 3 wires the helper into the inner-loop dispatch. It's a pre-test for Phase 3.

- [ ] **Step 2: Implement the helper in `spmd.go`**

Append:

```go
// spmdExtractVaryingSliceParts loads the [N x slice_struct] representing a
// varying slice and extracts per-lane base pointers and lengths into vectors.
// Returns (basePtrsVec : <N x ptr>, lensVec : <N x i32>).
//
// Used by spmdEmitDivergentInnerLoop to set up the per-iter active mask
// computation and per-lane gather addresses for the body.
//
// laneCount is the SPMD loop's lane count (matches the [N x slice_struct] size).
func (b *builder) spmdExtractVaryingSliceParts(headersAlloca llvm.Value, sliceLLVMType llvm.Type, laneCount int) (basePtrs, lens llvm.Value) {
	arrType := llvm.ArrayType(sliceLLVMType, laneCount)
	headers := b.CreateLoad(arrType, headersAlloca, "spmd.varying.slice.load")

	basePtrs = llvm.Undef(llvm.VectorType(b.dataPtrType, laneCount))
	lens = llvm.Undef(llvm.VectorType(b.ctx.Int32Type(), laneCount))

	for lane := 0; lane < laneCount; lane++ {
		laneSlice := b.CreateExtractValue(headers, lane, "spmd.lane.slice")
		laneBase := b.CreateExtractValue(laneSlice, 0, "spmd.lane.base")
		laneLen := b.CreateExtractValue(laneSlice, 1, "spmd.lane.len")

		laneIdx := llvm.ConstInt(b.ctx.Int32Type(), uint64(lane), false)
		basePtrs = b.CreateInsertElement(basePtrs, laneBase, laneIdx, "")
		lens = b.CreateInsertElement(lens, laneLen, laneIdx, "")
	}
	return basePtrs, lens
}
```

- [ ] **Step 3: Build TinyGo and run tests**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
cd tinygo
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMDExtractVaryingSliceParts -v
```
Expected: TestSPMDExtractVaryingSliceParts FAILS (no caller wires the helper yet — that's Phase 3). The compilation must still succeed (helper just sits unused). Other tests continue to pass.

---

## Phase 3 — Divergent inner loop emission (no break support yet)

### Task 3.1: Identify the dispatch point and detection logic

**Files:** None modified.

- [ ] **Step 1: Trace the SSA → LLVM compilation of the inner range**

The inner `for _, value := range secondLevel` produces SSA pattern (from Task 2.1's investigation):
- `rangeindex.loop1`: phi for j, increment, bound check vs `extractvalue(load secondLevel, 0).len`
- `rangeindex.body2`: IndexAddr(slice_buf, j), load value, body, branch back

We need to detect this pattern WHEN we start compiling `rangeindex.loop1` (i.e., before any of its instructions are lowered). At that point we synthesize the divergent loop and SKIP the original blocks.

- [ ] **Step 2: Plan the detection helper**

A function `spmdIsVaryingSliceRangeLoop(loopBlock *ssa.BasicBlock) bool` that returns true if:
- `loopBlock.Comment == "rangeindex.loop"` (some inner-loop variant — find exact name)
- AND it's not the OUTER SPMD loop (already in `b.spmdLoopState.activeLoops`)
- AND following the bound-extraction back, we land at a `Varying[[]T]` value (the `secondLevel` alloca's load + extractvalue)

- [ ] **Step 3: Plan the redirect**

When detected, in `createBlock` (or the equivalent block-compilation entry point), branch to a new helper `spmdEmitDivergentInnerLoop(loopBlock, bodyBlock, doneBlock)` that emits LLVM IR for the divergent loop and pushes a new active-loop entry onto `b.spmdLoopState`. Then mark the original `rangeindex.loop1` and `rangeindex.body2` blocks as "already compiled" so the normal SSA-block walker skips them. The body's user instructions get lowered into the new emitted body block.

This is the architecturally hardest part — record concrete entry function name in `compiler.go` (likely `createBasicBlock` or a per-block dispatch in `createFunction`) before writing code.

- [ ] **Step 4: Document the dispatch design**

Append findings to `/tmp/divergent-inner-loop-investigation.md`:
- The function/method TinyGo uses to compile a single SSA basic block
- How `b.spmdLoopState.activeLoops` is populated (so a new inner active-loop entry follows the same pattern)
- The mechanism for "skip this block" when redirecting

### Task 3.2: Implement detection (`spmdIsVaryingSliceRangeLoop`)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (append helper)

- [ ] **Step 1: Write the helper**

```go
// spmdIsVaryingSliceRangeLoop reports whether loopBlock is the loop-header of
// an inner range-over-slice whose source is a Varying[[]T] value. This is the
// dispatch predicate for spmdEmitDivergentInnerLoop.
//
// The pattern recognized:
//   loopBlock.Comment == "rangeindex.loop"
//   AND loopBlock is NOT in b.spmdLoopState.activeLoops (so it's not the outer
//       SPMD loop already registered)
//   AND the bound expression in loopBlock derives from a Varying[[]T] value
//       (load of [N x slice_struct] + extractvalue at lane 0 + extract len)
//
// Returns true when all three conditions hold and the caller should emit a
// divergent inner loop instead of compiling loopBlock normally.
func (b *builder) spmdIsVaryingSliceRangeLoop(loopBlock *ssa.BasicBlock) bool {
	if loopBlock.Comment != "rangeindex.loop" {
		return false
	}
	if b.spmdLoopState == nil {
		return false
	}
	if _, isOuter := b.spmdLoopState.loopBlocks[loopBlock.Index]; isOuter {
		return false
	}
	// Walk loopBlock instructions to find the bound. The bound is typically
	// produced by extractvalue from a load of a [N x slice_struct]. Trace back.
	// (Refine this in Task 3.3 once the exact SSA is observed.)
	for _, instr := range loopBlock.Instrs {
		if binop, ok := instr.(*ssa.BinOp); ok && binop.Op == token.LSS {
			// binop.Y is the bound. Trace.
			return spmdBoundIsVaryingSliceLen(binop.Y)
		}
	}
	return false
}

// spmdBoundIsVaryingSliceLen traces a bound value back to determine if it
// originates from a Varying[[]T] slice header.
func spmdBoundIsVaryingSliceLen(v ssa.Value) bool {
	for {
		switch u := v.(type) {
		case *ssa.Extract:
			// extractvalue extracting len field
			v = u.Tuple
		case *ssa.UnOp:
			if u.Op != token.MUL {
				return false
			}
			v = u.X
		case *ssa.IndexAddr:
			// Trace through the array (may be the [N x slice] alloca)
			v = u.X
		case *ssa.Alloc:
			// Check if alloca's pointee is *types.SPMDType wrapping slice
			if ptr, ok := u.Type().(*types.Pointer); ok {
				if spmdT, ok := ptr.Elem().(*types.SPMDType); ok {
					if _, isSlice := spmdT.Elem().Underlying().(*types.Slice); isSlice {
						return true
					}
				}
			}
			return false
		default:
			return false
		}
	}
}
```

NOTE: This is a STARTING SCAFFOLD. The exact trace structure will need refinement based on Task 3.1's investigation. Refine the cases as needed when actual SSA is observed.

- [ ] **Step 2: Build and verify it compiles**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
```
Expected: clean build (no test passes yet — the helper isn't called).

### Task 3.3: Implement `spmdEmitDivergentInnerLoop` (no break support)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (append a major helper)

- [ ] **Step 1: Implement the emission helper**

```go
// spmdEmitDivergentInnerLoop synthesizes the LLVM IR for an inner
// `for _, v := range varyingSlice` loop. The standard SSA blocks
// loopBlock and bodyBlock are SKIPPED — we emit our own loop structure
// and lower the user body instructions into the synthesized body block.
//
// Caller is responsible for:
//   - Marking loopBlock and bodyBlock as already-compiled in b.fn.Blocks
//   - Continuing compilation from doneBlock (the post-loop merge)
//
// Produces:
//   pre-header (in current block):
//     load [N x slice_struct], extract base_ptrs <N x ptr> + lens <N x i32>,
//     compute max_len = reduce.smax(lens)
//   inner.loop:
//     j phi, j_lt_max check
//   inner.body:
//     active_mask = outer_mask AND (j_splat < lens),
//     reduce.any short-circuit,
//     gather v from base_ptrs + j*sizeof(T),
//     lower user body with active_mask threaded
//   inner.done:
//     resume normal flow
//
// laneCount: the SPMD lane count of the active outer loop.
func (b *builder) spmdEmitDivergentInnerLoop(
	loopBlock, bodyBlock, doneBlock *ssa.BasicBlock,
	headersAlloca llvm.Value,
	sliceLLVMType llvm.Type,
	laneCount int,
) {
	// 1. Pre-header (emit in current block).
	basePtrs, lens := b.spmdExtractVaryingSliceParts(headersAlloca, sliceLLVMType, laneCount)
	maxLen := b.spmdVectorReduceMax(lens) // existing reduce helper

	// 2. Create LLVM blocks.
	innerLoop := b.ctx.AddBasicBlock(b.llvmFn, "spmd.inner.loop")
	innerBody := b.ctx.AddBasicBlock(b.llvmFn, "spmd.inner.body")
	innerDone := b.ctx.AddBasicBlock(b.llvmFn, "spmd.inner.done")

	preHeaderBB := b.GetInsertBlock()
	b.CreateBr(innerLoop)

	// 3. Loop header.
	b.SetInsertPointAtEnd(innerLoop)
	jPhi := b.CreatePHI(b.ctx.Int32Type(), "j")
	zero := llvm.ConstInt(b.ctx.Int32Type(), 0, false)
	jPhi.AddIncoming([]llvm.Value{zero}, []llvm.BasicBlock{preHeaderBB})
	jLtMax := b.CreateICmp(llvm.IntSLT, jPhi, maxLen, "j.lt.max")
	b.CreateCondBr(jLtMax, innerBody, innerDone)

	// 4. Body: compute active mask + per-lane addresses + gather + body.
	b.SetInsertPointAtEnd(innerBody)
	jSplat := b.splatScalar(jPhi, llvm.VectorType(b.ctx.Int32Type(), laneCount))
	laneActive := b.CreateICmp(llvm.IntSLT, jSplat, lens, "spmd.inner.lane.active")

	outerMask := b.spmdLoopState.activeLoops[/* outer */].currentMask // pseudo — adapt to actual API
	activeMask := b.spmdAndMask(outerMask, laneActive)                // existing AND helper

	// Per-lane gather addresses
	elemSize := b.targetData.TypeAllocSize(/* elem type T */)
	jOffset := b.CreateMul(jPhi, llvm.ConstInt(b.ctx.Int32Type(), elemSize, false), "")
	jOffsetSplat := b.splatScalar(jOffset, llvm.VectorType(b.ctx.Int32Type(), laneCount))
	addrs := b.CreateGEP(b.ctx.Int8Type(), basePtrs, []llvm.Value{jOffsetSplat}, "")

	// Gather v
	elemLLVMType := /* T's LLVM type */
	v := b.spmdMaskedGather(llvm.VectorType(elemLLVMType, laneCount), addrs, activeMask)
	_ = v // will be bound to the body's value variable

	// 5. Push new active-loop entry, lower body, pop.
	// (Implementation detail: register a new spmdActiveLoop with mask=activeMask,
	// compile the bodyBlock's instructions into innerBody, increment j.)
	// TODO in this task: stub the body lowering to a simple "continue" that
	// just increments j and loops back.

	// 6. Increment j and loop back.
	jNext := b.CreateAdd(jPhi, llvm.ConstInt(b.ctx.Int32Type(), 1, false), "j.next")
	jPhi.AddIncoming([]llvm.Value{jNext}, []llvm.BasicBlock{innerBody})
	b.CreateBr(innerLoop)

	// 7. Done block: resume.
	b.SetInsertPointAtEnd(innerDone)
}
```

NOTE: The pseudo-code references `b.spmdLoopState.activeLoops[/* outer */].currentMask` and the body-lowering hooks — these need to be matched against actual TinyGo APIs from Task 2.1's investigation. Replace the pseudo-references with concrete code as you implement.

- [ ] **Step 2: Wire the dispatch**

In TinyGo's per-block compilation entry (find via Task 3.1's investigation; likely `createBasicBlock` or in `createFunction`'s block loop), add:

```go
if b.spmdIsVaryingSliceRangeLoop(block) {
    // Extract headers alloca, slice LLVM type, lane count from SSA inspection.
    // Then:
    b.spmdEmitDivergentInnerLoop(block, bodyBlock, doneBlock, headersAlloca, sliceLLVMType, laneCount)
    // Mark block and bodyBlock as compiled to skip them in the normal walk.
    continue
}
```

- [ ] **Step 3: Build and run array-counting**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go && wasmtime /tmp/ac.wasm
```
Expected: `Array sums: [3 3 4 18]`. If output is wrong (e.g., still `[3 3 3 3]` or some other pattern), inspect the IR and identify which step diverges.

### Task 3.4: Verify sentinels and e2e

**Files:** None modified.

- [ ] **Step 1: n-body**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
```
Expected: `-0.169075164 / -0.169078071`.

- [ ] **Step 2: Bucket-G + bit-counting + swizzle-within + goroutine-varying**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/v7-phase3-e2e.log 2>&1
grep -E "L0_cond|L4b_varying_break|integ_bit-counting|integ_printf-verbs|integ_swizzle-within|integ_goroutine-varying" /tmp/v7-phase3-e2e.log | sed 's/\x1b\[[0-9;]*m//g'
```
Expected: all PASS.

- [ ] **Step 3: E2E summary**

```bash
grep -E "Total|Compile|Run|Reject" /tmp/v7-phase3-e2e.log | sed 's/\x1b\[[0-9;]*m//g'
```
Expected at minimum: 94/0/93/0/11 (full baseline). If lower, identify which test regressed and fix before commit.

### Task 3.5: Commit Phase 1-3 progress

**Files:** None modified — this is a commit step.

- [ ] **Step 1: Inspect changes**

```bash
git -C /home/cedric/work/SPMD/tinygo status --short
git -C /home/cedric/work/SPMD/tinygo diff --stat
```
Expected: `compiler/spmd.go` and `compiler/compiler.go` modified (and possibly `compiler/spmd_test.go`).

- [ ] **Step 2: Commit via clean-commit agent**

Don't commit manually — dispatch the clean-commit agent per the project's mandatory workflow (CLAUDE.md). Provide:
- File list and diff stats
- WHY: Phase 1-3 of v7 (divergent inner loop). Closes array-counting v6.1 regression.
- Verified: array-counting produces `[3 3 4 18]`; n-body unchanged; e2e at 94/0/93/0/11.

Suggested subject: `feat(spmd): divergent inner loop for Varying[[]T]`

---

## Phase 4 — Break support

### Task 4.1: Add break test (TDD red)

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/varying-slice-break/main.go`
- Modify: `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh` (add e2e entry)

- [ ] **Step 1: Write the test source**

Create `/home/cedric/work/SPMD/test/integration/spmd/varying-slice-break/main.go`:

```go
// Tests per-lane break inside a divergent inner range loop.
package main

import "fmt"

func main() {
	arrays := [][]int{
		{1, 2, -1, 3},  // Lane 0: stops at -1 → sum = 3
		{10, 20},       // Lane 1: no break → sum = 30
		{-1, 5},        // Lane 2: stops at -1 → sum = 0
		{7, 8, 9},      // Lane 3: no break → sum = 24
	}

	result := countUntilNeg(arrays)
	fmt.Printf("Sums until neg: %v\n", result)
}

func countUntilNeg(arrays [][]int) []int {
	result := make([]int, len(arrays))
	go for i, secondLevel := range arrays {
		t := 0
		for _, value := range secondLevel {
			if value < 0 {
				break
			}
			t += value
		}
		result[i] = t
	}
	return result
}
```

Expected output: `Sums until neg: [3 30 0 24]`.

- [ ] **Step 2: Add e2e entry**

In `test/e2e/spmd-e2e-test.sh`, after the `integ_array-counting` entry, add:

```bash
test_compile_and_run "integ_varying-slice-break" "$INTEG/varying-slice-break/main.go" \
    "Sums until neg: [3 30 0 24]" "" "-scheduler=none"
```

- [ ] **Step 3: Run the test (FAIL expected — break not yet supported)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/vsb.wasm test/integration/spmd/varying-slice-break/main.go && wasmtime /tmp/vsb.wasm
```
Expected: a failure or wrong output (since `break` semantics aren't yet wired). Note the actual output for diff after fix.

### Task 4.2: Implement broken_mask infrastructure

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (extend `spmdEmitDivergentInnerLoop`)

- [ ] **Step 1: Detect break presence in body**

Before emitting the loop, scan `bodyBlock` and its successors-up-to-doneBlock for any `*ssa.Jump` whose target is `doneBlock` (a break). Record `bodyHasBreak bool`.

- [ ] **Step 2: Allocate broken_mask if needed**

In the pre-header, if `bodyHasBreak`:

```go
brokenMaskType := llvm.VectorType(b.spmdMaskElemType(laneCount), laneCount)
brokenMask := b.CreateAlloca(brokenMaskType, "spmd.broken.mask")
b.CreateStore(llvm.ConstNull(brokenMaskType), brokenMask)
```

- [ ] **Step 3: Update active mask computation**

In the body block, after computing `laneActive`:

```go
if bodyHasBreak {
    brk := b.CreateLoad(brokenMaskType, brokenMask, "spmd.broken.load")
    notBrk := b.spmdNotMask(brk) // existing helper, or write inline
    activeMask = b.spmdAndMask(activeMask, notBrk)
}
```

- [ ] **Step 4: Convert break SSA → masked store to broken_mask**

When the body block lowering encounters a `*ssa.Jump` to `doneBlock` under a varying condition (i.e., a `break`), redirect to: emit a masked store of the per-lane "breaking" bits into `brokenMask`, then jump to `inner.body.end` (NOT to `doneBlock` directly — we need the loop to continue for non-breaking lanes).

This requires intercepting the SSA-level break. One approach: detect during `spmdConvertScopedMemOps` (the pass that converts varying control flow to predicated form) that a break exists in the inner-loop scope, and redirect to broken_mask store.

Alternatively, scan the body during `spmdEmitDivergentInnerLoop` and rewrite the break path during emission.

Pick the simpler approach (the in-emission rewrite) and document.

- [ ] **Step 5: Build and verify the break test**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/vsb.wasm test/integration/spmd/varying-slice-break/main.go && wasmtime /tmp/vsb.wasm
```
Expected: `Sums until neg: [3 30 0 24]`.

- [ ] **Step 6: Verify n-body and array-counting still work**

```bash
cd /home/cedric/work/SPMD/tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
cd /home/cedric/work/SPMD && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go && wasmtime /tmp/ac.wasm
```
Expected: n-body unchanged; array-counting still `[3 3 4 18]`.

---

## Phase 5 — Edge cases

### Task 5.1: Empty-lane test

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/varying-slice-empty/main.go`
- Modify: `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh`

- [ ] **Step 1: Write test**

```go
package main

import "fmt"

func main() {
	arrays := [][]int{
		{1, 2}, // Lane 0
		{},     // Lane 1: empty
		{3},    // Lane 2
		{},     // Lane 3: empty
	}
	result := sumLanes(arrays)
	fmt.Printf("Sums (with empty lanes): %v\n", result)
}

func sumLanes(arrays [][]int) []int {
	result := make([]int, len(arrays))
	go for i, secondLevel := range arrays {
		t := 0
		for _, v := range secondLevel {
			t += v
		}
		result[i] = t
	}
	return result
}
```

Expected: `Sums (with empty lanes): [3 0 3 0]`.

- [ ] **Step 2: Add e2e entry**

```bash
test_compile_and_run "integ_varying-slice-empty" "$INTEG/varying-slice-empty/main.go" \
    "Sums (with empty lanes): [3 0 3 0]" "" "-scheduler=none"
```

- [ ] **Step 3: Run and verify**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/vse.wasm test/integration/spmd/varying-slice-empty/main.go && wasmtime /tmp/vse.wasm
```
Expected: `Sums (with empty lanes): [3 0 3 0]`. Should pass without code changes — the active mask `(j < len[lane])` correctly excludes lanes with `len=0` from j=0.

If it fails: investigate. Possible cause: `max_len = 0` produces UB in `reduce.smax(<empty>)`, or the all-zero gather addresses crash. Add a guard for `max_len <= 0` skipping the body entirely.

### Task 5.2: Continue test

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/varying-slice-continue/main.go`
- Modify: `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh`

- [ ] **Step 1: Write test**

```go
package main

import "fmt"

func main() {
	arrays := [][]int{
		{1, -1, 2, -1, 3}, // Lane 0: skips -1 → sum = 6
		{10, 20, 30},      // Lane 1: no skips → sum = 60
		{-1, -1, 5},       // Lane 2: only 5 counted → sum = 5
		{1, 1, 1, 1, 1},   // Lane 3: → sum = 5
	}
	result := sumPositive(arrays)
	fmt.Printf("Positive sums: %v\n", result)
}

func sumPositive(arrays [][]int) []int {
	result := make([]int, len(arrays))
	go for i, secondLevel := range arrays {
		t := 0
		for _, v := range secondLevel {
			if v < 0 {
				continue
			}
			t += v
		}
		result[i] = t
	}
	return result
}
```

Expected: `Positive sums: [6 60 5 5]`.

- [ ] **Step 2: Add e2e entry, build, verify**

Add to e2e script. Run. Expected: `Positive sums: [6 60 5 5]`. Should pass via Pass A's predication infrastructure (continue under varying condition narrows the body mask).

### Task 5.3: Combined break + continue + multi-slice test

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/varying-slice-multi/main.go`
- Modify: `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh`

- [ ] **Step 1: Write test**

```go
package main

import "fmt"

func main() {
	arrays := [][]int{
		{1, 2, 3, -2, 4},   // skip neg, no break
		{5, -1, 6},         // break at -1
		{},                 // empty
		{7, -2, 8, -1, 9},  // skip -2, break at -1
	}
	result := compute(arrays)
	fmt.Printf("Multi-feature sums: %v\n", result)
}

func compute(arrays [][]int) []int {
	result := make([]int, len(arrays))
	go for i, secondLevel := range arrays {
		t := 0
		for _, v := range secondLevel {
			if v == -1 {
				break
			}
			if v < 0 {
				continue
			}
			t += v
		}
		result[i] = t
	}
	return result
}
```

Expected per lane:
- Lane 0: `1+2+3+0+4 = 10` (skips -2)
- Lane 1: `5` (break at -1)
- Lane 2: `0` (empty)
- Lane 3: `7+0 = 7` (skips -2, break at -1)

Expected: `Multi-feature sums: [10 5 0 7]`.

- [ ] **Step 2: Add e2e entry, build, verify**

If output diverges, the combination of break and continue revealed an interaction bug. Diagnose: compile with `-internal-printir`, inspect the broken_mask updates relative to the body-mask narrowing. Fix iteratively.

---

## Phase 6 — Type checker rules

### Task 6.1: Forbid `return` inside divergent inner loop

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/go/types/` (or wherever the SPMD `go for` ISPC restrictions live)

- [ ] **Step 1: Locate existing return-restriction enforcement**

```bash
grep -rn "return.*forbidden\|return.*not allowed\|InvalidSPMDReturn\|ISPC restriction" /home/cedric/work/SPMD/go/src/go/types/ | head -10
```
Find the file/function that already forbids `return` under varying conditions in `go for`.

- [ ] **Step 2: Extend to inner divergent range loops**

Add an analogous check: when type-checking a `*ast.RangeStmt` whose X is `Varying[[]T]` AND we're inside a SPMD scope, treat its body the same as a `go for` body for return-restriction purposes.

- [ ] **Step 3: Add test for the new restriction**

Add a `test/integration/spmd/illegal/return-in-divergent-inner.go` that contains:

```go
package main

func F(arrays [][]int) int {
	go for _, secondLevel := range arrays {
		for _, v := range secondLevel {
			if v < 0 {
				return -1 // illegal
			}
		}
	}
	return 0
}
```

The compiler should reject this with a clear error message.

- [ ] **Step 4: Add reject entry to e2e**

In `test/e2e/spmd-e2e-test.sh`, in the "reject" section, add the test.

- [ ] **Step 5: Build, verify reject works**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
# Run the e2e suite to confirm the reject entry passes
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep "return-in-divergent-inner"
```

---

## Phase 7 — Final gate

### Task 7.1: Full e2e

**Files:** None modified.

- [ ] **Step 1: Run full e2e**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/v7-final-e2e.log 2>&1
grep -E "Total|Compile|Run|Reject|All tests" /tmp/v7-final-e2e.log | sed 's/\x1b\[[0-9;]*m//g'
```

Expected (with new tests added):
```
Total tests:     109   (was 105 + 4 new)
Compile pass:    98    (was 94 + 4 new)
Compile fail:    0
Run pass:        97    (was 93 + 4 new)
Run fail:        0
Reject pass:     12    (was 11 + 1 new for return-in-divergent-inner)
Reject fail:     0
All tests passed!
```

If any failure: list, diagnose, iterate.

- [ ] **Step 2: Sentinel n-body**

Run the sentinel command. Expected: `-0.169075164 / -0.169078071`.

- [ ] **Step 3: Sentinel benchmarks (light)**

```bash
# Compile and time mandelbrot (existing benchmark) AVX2 to confirm no perf regression
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/mandel-spmd test/integration/spmd/mandelbrot/main.go
/tmp/mandel-spmd | grep "speedup"
```
Compare to `tinybench/_baseline-2026-04-30/x86-bench/mandelbrot-x86.log` AVX2 entry. Expected: same speedup ±10%. A larger regression suggests the divergent-inner-loop changes leaked into hot paths — investigate.

### Task 7.2: Submodule pointer bumps

**Files:** Parent repo's submodule pointers.

- [ ] **Step 1: Verify all submodule commits made**

```bash
git -C /home/cedric/work/SPMD/tinygo log --oneline -5
git -C /home/cedric/work/SPMD/x-tools-spmd log --oneline -3
git -C /home/cedric/work/SPMD log --oneline -5
```
v7 should have 1+ tinygo commits, possibly 0 x-tools-spmd commits (no SSA changes per design), and the parent should have new tests + e2e entries committed.

- [ ] **Step 2: Bump pointers via clean-commit**

Don't commit manually — dispatch clean-commit agent. Provide:
- Files: `tinygo` submodule pointer + new test files + e2e script changes
- WHY: v7 divergent inner loop feature; closes array-counting and unblocks all `Varying[[]T]` range patterns

### Task 7.3: Update PLAN.md / MEMORY.md

**Files:**
- Modify: `/home/cedric/work/SPMD/PLAN.md` and/or `~/.claude/projects/-home-cedric-work-SPMD/memory/MEMORY.md`

- [ ] **Step 1: Mark v7 (divergent inner loop) DONE in PLAN.md**

- [ ] **Step 2: Update MEMORY.md** with the new e2e numbers + the "divergent inner loops over Varying[[]T] are supported with continue+break, return deferred" entry.

- [ ] **Step 3: Commit doc updates** via clean-commit.

---

## Risks and contingencies

### Risk: Detecting the dispatch point in TinyGo is more complex than expected

**Mitigation**: Task 2.1 + 3.1 are EXPLICITLY investigation tasks. If they reveal that intercepting at block-boundary is harder than expected (e.g., the inner-loop blocks are deeply integrated with SSA-level loop-info bookkeeping), pivot to:
- **Plan B**: Modify x-tools-spmd to lower `range Varying[[]T]` to a new SSA opcode (`*ssa.SPMDDivergentRange`). TinyGo handles only that opcode, no block-level interception. This is more work upfront but cleaner separation. If pivoting, write a new spec amendment.

### Risk: Active mask propagation into body interferes with Pass A's existing mask threading

**Mitigation**: The body's SPMDLoad/SPMDStore use `spmdLoopState` to find the active mask. By pushing a NEW spmdActiveLoop entry for the inner loop with `mask = active_mask`, existing handlers transparently use the inner mask. Verify by inspecting IR that the inner-body's masked.gather and masked.store use `active_mask`, not the outer's.

### Risk: Break-mask test (Task 4) reveals that break SSA shape requires SSA-side support

**Mitigation**: If Pass A's existing varying-break infrastructure assumes break is in `go for` (outer-only), we may need x-tools-spmd extensions. If so, scope to a new sub-task with its own design review.

### Risk: Performance regression on hot paths

**Mitigation**: Task 7.1 step 3 compares mandelbrot performance against baseline. If regression > 10%, identify which change is the culprit (most likely the alloca-sizing fix changing some IR pattern that triggers a regression in optimization). Roll back the specific change and find an alternative.

---

## Coverage check (against spec)

| Spec section | Plan task |
|---|---|
| §1 Problem statement | Phase 0 (verify regression), Phase 3 (fix) |
| §2 Scope | Tasks 1-6 cover slices only; out-of-scope items not implemented |
| §3 Architecture: detection | Task 3.2 |
| §3 Architecture: storage | Phase 1 (Tasks 1.1-1.3) |
| §3 Architecture: per-lane access | Task 2.2 |
| §3 Architecture: loop emission | Task 3.3 |
| §3 Architecture: active mask propagation | Task 3.3 step 1 (push spmdActiveLoop) |
| §3 Architecture: continue | Task 5.2 (test verifies it works via Pass A; no new code needed) |
| §3 Architecture: break | Phase 4 |
| §4 Edge cases | Phase 5 (Tasks 5.1, 5.2, 5.3) |
| §5 Testing strategy | Phase 0 (sentinels) + Phase 5 (new tests) + Phase 7 (final gate) |
| §6 Implementation order | Maps directly to Phase 1 → Phase 7 |
| §7 Risks | Risks section above |
| §8 Success criteria | Phase 7 (final gate) |
