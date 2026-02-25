# Rangeint Loop Peeling Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Extend SPMD loop peeling from rangeindex-only to rangeint loops, enabling mandelbrot and all range-over-int examples to use unmasked main-phase stores.

**Architecture:** Remove the `isRangeIndex` gate in `spmdShouldPeelLoop`, generalize the accumulator phi check to cover both body-block and loop-block phis, add phi-based forwarding for accumulators through `tail.check`, and replace the `spmdFuncIsBody` silent return with a panic assertion. Follow-up commit unifies rangeint/rangeindex branches via helper methods on `spmdActiveLoop`.

**Tech Stack:** TinyGo compiler (Go), LLVM IR generation, WASM SIMD128

**Design doc:** `docs/plans/2026-02-24-rangeint-loop-peeling-design.md`

---

### Task 1: Update eligibility tests for rangeint peeling

**Files:**
- Modify: `tinygo/compiler/spmd_llvm_test.go:1322-1356` (TestSPMDLoopPeelingEligibility)

**Step 1: Update existing test expectations**

The test at line 1333 expects rangeint to be ineligible. Update it to expect eligible (will need a valid `incrBinOp` now). Also add new test cases for:
- rangeint with valid incrBinOp → eligible (true)
- spmdFuncIsBody → panic (not false)

```go
func TestSPMDLoopPeelingEligibility(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()

	tests := []struct {
		name      string
		loop      spmdActiveLoop
		funcBody  bool
		want      bool
		wantPanic bool
	}{
		// rangeint loops are now eligible for peeling (gate removed).
		// Without a real SSA BinOp (incrBinOp == nil) the nil guard returns false.
		{"rangeint_nil_incrBinOp", spmdActiveLoop{laneCount: 16}, false, false, false},
		// rangeindex loops: same nil guard behavior.
		{"rangeindex_nil_incrBinOp", spmdActiveLoop{laneCount: 4, isRangeIndex: true}, false, false, false},
		// Zero lane count is ineligible.
		{"zero_lane_count", spmdActiveLoop{laneCount: 0, isRangeIndex: true}, false, false, false},
		// SPMD function body triggers panic (invariant violation).
		{"spmd_func_body_panics", spmdActiveLoop{laneCount: 4}, true, false, true},
		{"spmd_func_body_rangeindex_panics", spmdActiveLoop{laneCount: 4, isRangeIndex: true}, true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBuilder(t, c)
			b.spmdFuncIsBody = tt.funcBody
			if tt.wantPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("spmdShouldPeelLoop() did not panic, expected panic")
					}
				}()
				b.spmdShouldPeelLoop(&tt.loop)
				return
			}
			got := b.spmdShouldPeelLoop(&tt.loop)
			if got != tt.want {
				t.Errorf("spmdShouldPeelLoop() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

**Step 2: Run the test to verify it fails**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMDLoopPeelingEligibility -v`
Expected: FAIL — rangeint test still expects false but we changed to expect different behavior; spmdFuncIsBody tests expect panic but get false.

**Step 3: Commit test update**

```bash
cd tinygo && git add compiler/spmd_llvm_test.go
git commit -m "test: update loop peeling eligibility tests for rangeint support"
```

---

### Task 2: Remove rangeint gate and update spmdFuncIsBody assertion

**Files:**
- Modify: `tinygo/compiler/spmd.go:485-535` (spmdShouldPeelLoop)

**Step 1: Modify spmdShouldPeelLoop**

Replace the function body (lines 491-534) with:

```go
func (b *builder) spmdShouldPeelLoop(loop *spmdActiveLoop) bool {
	// spmdFuncIsBody implies spmdLoopState == nil, which means there are no
	// go-for loops to peel. This function is only called for loops in
	// spmdLoopState.activeLoops, so spmdFuncIsBody should never be true here.
	// If this invariant is violated (e.g., go-for nesting becomes allowed),
	// peeling must be revisited to handle break mask interaction.
	if b.spmdFuncIsBody {
		panic("spmd: spmdShouldPeelLoop called with spmdFuncIsBody=true (invariant violation)")
	}
	// Don't peel loops inside closures (anonymous functions).
	// Closures have complex entry blocks (captured variables, recover setup)
	// that the entry predecessor detection doesn't handle.
	if b.fn != nil && b.fn.Parent() != nil {
		return false
	}
	// Lane count must be > 0 (sanity).
	if loop.laneCount <= 0 {
		return false
	}
	// Check for accumulator phis beyond the iterator phi.
	// Accumulator phis live in different blocks depending on the loop pattern:
	// - rangeindex: iterator phi + accumulators in loop block (rangeindex.loop)
	// - rangeint: iterator phi in body block (rangeint.body), accumulators also in body block
	// Both patterns may also have phis in the other block (though uncommon).
	// Count phis in both the body and loop blocks; only the single iterator phi is allowed.
	if loop.incrBinOp == nil {
		return false // defensive: incrBinOp must be set for any real loop
	}
	totalPhiCount := 0
	// Count phis in the loop block (where incrBinOp lives).
	loopBlock := loop.incrBinOp.Block()
	for _, instr := range loopBlock.Instrs {
		if _, isPhi := instr.(*ssa.Phi); isPhi {
			totalPhiCount++
		} else {
			break
		}
	}
	// Count phis in the body block (where iterPhi lives, for rangeint).
	if loop.iterPhi != nil {
		bodyBlock := loop.iterPhi.Block()
		if bodyBlock != loopBlock {
			for _, instr := range bodyBlock.Instrs {
				if _, isPhi := instr.(*ssa.Phi); isPhi {
					totalPhiCount++
				} else {
					break
				}
			}
		}
	}
	// The iterator phi is the one allowed phi. Any additional phis are accumulators
	// that need forwarding through tail.check.
	if totalPhiCount > 1 {
		// Accumulator phis present — still peel, but will need forwarding.
		// (Handled by accumulatorPhis in spmdPeeledLoop.)
	}
	return true
}
```

**Step 2: Run the eligibility test**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMDLoopPeelingEligibility -v`
Expected: PASS — rangeint now eligible (nil incrBinOp still returns false), spmdFuncIsBody panics.

**Step 3: Run full SPMD test suite to check for regressions**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1 2>&1 | tail -20`
Expected: All existing tests pass (rangeindex peeling unchanged).

**Step 4: Commit**

```bash
cd tinygo && git add compiler/spmd.go
git commit -m "feat: extend loop peeling eligibility to rangeint loops

Remove isRangeIndex gate. Replace spmdFuncIsBody silent return with
panic assertion documenting the structural invariant. Generalize
accumulator phi counting to check both body and loop blocks."
```

---

### Task 3: Add accumulatorPhis field to spmdPeeledLoop

**Files:**
- Modify: `tinygo/compiler/spmd.go:472-483` (spmdPeeledLoop struct)

**Step 1: Add the field**

Add after the `phase` field (line 483):

```go
type spmdPeeledLoop struct {
	loop           *spmdActiveLoop
	alignedBound   llvm.Value
	tailCheckBlock llvm.BasicBlock
	tailBlockInfo  map[int]blockInfo
	tailExitBlock  llvm.BasicBlock
	tailIterPhi    llvm.Value
	bodyBlockSet   map[int]bool
	phase          spmdLoopPhase
	accumulatorPhis map[*ssa.Phi]llvm.Value // SSA accumulator phi → tail.check LLVM phi
}
```

**Step 2: Run tests to verify no regressions**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1 2>&1 | tail -5`
Expected: PASS — struct field addition is backwards compatible.

**Step 3: Commit**

```bash
cd tinygo && git add compiler/spmd.go
git commit -m "feat: add accumulatorPhis field to spmdPeeledLoop struct"
```

---

### Task 4: Create accumulator phis in emitSPMDTailCheck

**Files:**
- Modify: `tinygo/compiler/spmd.go:5210-5242` (emitSPMDTailCheck)

**Step 1: After creating the iterator phi, create accumulator phis**

Add after line 5220 (`peeled.tailIterPhi = phi`):

```go
	// Create accumulator phis in tail.check for any additional phis beyond the
	// iterator. Accumulator phis carry values (running sums, etc.) from the main
	// loop exit into the tail iteration.
	// Collect phis from both body and loop blocks (rangeint has iter phi in body,
	// rangeindex has iter phi in loop; accumulators can be in either).
	peeled.accumulatorPhis = make(map[*ssa.Phi]llvm.Value)
	collectAccPhis := func(block *ssa.BasicBlock) {
		for _, instr := range block.Instrs {
			ssaPhi, ok := instr.(*ssa.Phi)
			if !ok {
				break // phis are always first
			}
			// Skip the iterator phi (already handled by tailIterPhi).
			if loopMatch, matched := b.spmdLoopState.activeLoops[ssaPhi]; matched && loopMatch == loop {
				continue
			}
			// This is an accumulator phi — create a corresponding LLVM phi in tail.check.
			accType := b.getLLVMType(ssaPhi.Type())
			accPhi := b.CreatePHI(accType, "spmd.tail.acc")
			peeled.accumulatorPhis[ssaPhi] = accPhi
		}
	}
	loopBlock := loop.incrBinOp.Block()
	collectAccPhis(loopBlock)
	if loop.iterPhi != nil {
		bodyBlock := loop.iterPhi.Block()
		if bodyBlock != loopBlock {
			collectAccPhis(bodyBlock)
		}
	}
```

**Step 2: Run tests**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1 2>&1 | tail -5`
Expected: PASS — no loops with accumulators exist in current tests, so this code path is not exercised but shouldn't break anything.

**Step 3: Commit**

```bash
cd tinygo && git add compiler/spmd.go
git commit -m "feat: create accumulator phis in tail.check for peeled loops"
```

---

### Task 5: Override accumulator phis in emitSPMDTailBody

**Files:**
- Modify: `tinygo/compiler/spmd.go:5388-5400` (emitSPMDTailBody, iter phi handling section)

**Step 1: After the existing iter phi override, add accumulator phi override**

Replace the phi handling block (lines 5388-5400) with:

```go
			// Skip the rangeint iter phi in tail — use tailIterPhi directly.
			// For rangeindex, bodyIterValue is incrBinOp (not a phi), handled via b.locals above.
			if phi, ok := instr.(*ssa.Phi); ok {
				if loopMatch, matched := b.spmdLoopState.activeLoops[phi]; matched && loopMatch == loop {
					// Override phi to use tailIterPhi from tail.check.
					b.locals[phi] = peeled.tailIterPhi
					// Emit prologue for rangeint pattern (rangeindex handled at body block entry).
					b.emitSPMDBodyPrologue(loop)
					b.spmdValueOverride[phi] = loop.laneIndices
					b.spmdMaskStack = []llvm.Value{loop.tailMask}
					continue // skip normal createInstruction for this phi
				}
				// Check for accumulator phi — override to tail.check accumulator phi.
				if accPhi, ok := peeled.accumulatorPhis[phi]; ok {
					b.locals[phi] = accPhi
					continue // skip normal createInstruction for this phi
				}
			}
```

**Step 2: Run tests**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1 2>&1 | tail -5`
Expected: PASS

**Step 3: Commit**

```bash
cd tinygo && git add compiler/spmd.go
git commit -m "feat: override accumulator phis in tail body with tail.check values"
```

---

### Task 6: Wire accumulator phi incoming values

**Files:**
- Modify: `tinygo/compiler/compiler.go:1831-1834` (after tailIterPhi.AddIncoming)

**Step 1: After wiring tailIterPhi, wire accumulator phis**

Add after line 1834 (`peeled.tailIterPhi.AddIncoming(...)`):

```go
			// Wire accumulator phi incoming values.
			for ssaPhi, accLLVMPhi := range peeled.accumulatorPhis {
				// Find the initial value from the entry edge.
				// For rangeint: iterPhi is in body block, accumulators also in body block.
				//   Body preds: [entry, loop]. Entry edge = the one that isn't a loop block.
				// For rangeindex: accumulators in loop block.
				//   Loop preds: [entry, body]. Entry edge = the one that isn't a body block.
				var initVal llvm.Value
				phiBlock := ssaPhi.Block()
				for i, pred := range phiBlock.Preds {
					_, isBody := b.spmdLoopState.bodyBlocks[pred.Index]
					_, isLoop := b.spmdLoopState.loopBlocks[pred.Index]
					isInterior := peeled.bodyBlockSet[pred.Index]
					if !isBody && !isLoop && !isInterior {
						// This is the entry predecessor edge.
						initVal = b.getValue(ssaPhi.Edges[i], getPos(ssaPhi))
						break
					}
				}
				if initVal.IsNil() {
					// Fallback: use zero value.
					initVal = llvm.ConstNull(b.getLLVMType(ssaPhi.Type()))
				}
				// Main loop's final accumulator value.
				mainAccVal := savedLocals[ssaPhi]
				if mainAccVal.IsNil() {
					mainAccVal = b.locals[ssaPhi]
				}
				if mainAccVal.IsNil() {
					mainAccVal = llvm.ConstNull(b.getLLVMType(ssaPhi.Type()))
				}
				accLLVMPhi.AddIncoming(
					[]llvm.Value{initVal, mainAccVal},
					[]llvm.BasicBlock{entryPredExit, mainLoopExit},
				)
			}
```

Note: `savedLocals` is not in scope here. The main loop's accumulator value is in `b.locals` because `emitSPMDTailBody` restores locals before returning. The value `b.locals[ssaPhi]` holds the main-phase final value.

Actually, looking more carefully at the code flow: `emitSPMDTailBody` saves and restores `b.locals`, so after it returns, `b.locals` has the main-phase values. So `b.locals[ssaPhi]` is the right source for the main loop's final accumulator value. Simplify to:

```go
			// Wire accumulator phi incoming values.
			for ssaPhi, accLLVMPhi := range peeled.accumulatorPhis {
				// Find the initial value from the entry edge.
				var initVal llvm.Value
				phiBlock := ssaPhi.Block()
				for i, pred := range phiBlock.Preds {
					_, isBody := b.spmdLoopState.bodyBlocks[pred.Index]
					_, isLoop := b.spmdLoopState.loopBlocks[pred.Index]
					isInterior := peeled.bodyBlockSet[pred.Index]
					if !isBody && !isLoop && !isInterior {
						initVal = b.getValue(ssaPhi.Edges[i], getPos(ssaPhi))
						break
					}
				}
				if initVal.IsNil() {
					initVal = llvm.ConstNull(b.getLLVMType(ssaPhi.Type()))
				}
				// Main loop's final value (b.locals restored to post-main-loop state).
				mainAccVal := b.locals[ssaPhi]
				if mainAccVal.IsNil() {
					mainAccVal = llvm.ConstNull(b.getLLVMType(ssaPhi.Type()))
				}
				accLLVMPhi.AddIncoming(
					[]llvm.Value{initVal, mainAccVal},
					[]llvm.BasicBlock{entryPredExit, mainLoopExit},
				)
			}
```

**Step 2: Run tests**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1 2>&1 | tail -5`
Expected: PASS

**Step 3: Commit**

```bash
cd tinygo && git add compiler/compiler.go
git commit -m "feat: wire accumulator phi incoming values for peeled loops"
```

---

### Task 7: Update entry Jump comment

**Files:**
- Modify: `tinygo/compiler/compiler.go:2215-2216` (comment in Jump handler)

**Step 1: Remove outdated comment**

The comment at line 2215 says `(rangeint loops are excluded from peeling by spmdShouldPeelLoop.)`. Remove this since rangeint is now supported:

```go
		// The aligned bound is computed here (in the entry block) so that the main loop
		// runs only when there is at least one full vector worth of elements.
		// If alignedBound == 0, skip directly to tail.check.
		// For rangeindex: entry → loop (succIdx is loop block).
		// For rangeint: entry → body (succIdx is body block).
		// IMPORTANT: Only match the actual entry block, NOT body/interior blocks
		// that also jump to the loop block (e.g., if.then, if.else in rangeindex).
```

**Step 2: No test needed — comment-only change**

**Step 3: Commit with Task 8 (below)**

---

### Task 8: Build TinyGo and run mandelbrot E2E

**Files:**
- No code changes — build and test

**Step 1: Build TinyGo**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`

**Step 2: Build mandelbrot-bench WASM**

Run: `cd tinygo && WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go ./build/tinygo build -target=wasi -scheduler=none -o /tmp/mandelbrot-simd.wasm /home/cedric/work/SPMD/examples/mandelbrot-bench/main.go`
Expected: Successful compilation (no panics, no errors).

**Step 3: Run mandelbrot-bench**

Run: `node --experimental-wasi-unstable-preview1 /home/cedric/work/SPMD/test/e2e/run-wasm.mjs /tmp/mandelbrot-simd.wasm`
Expected: Correct output (0 differences), speedup > 2.9x (ideally 3.3-3.5x).

**Step 4: Verify SIMD store optimization**

Run: `wasm2wat /tmp/mandelbrot-simd.wasm | grep -c 'v128.store32_lane'` and `wasm2wat /tmp/mandelbrot-simd.wasm | grep -c 'v128.store '`
Expected: Fewer `store32_lane` instructions than before (some remain in tail), more `v128.store` instructions (in main loop).

**Step 5: Commit comment update from Task 7**

```bash
cd tinygo && git add compiler/compiler.go
git commit -m "chore: update entry Jump comment for rangeint peeling support"
```

---

### Task 9: Run full test suite — no regressions

**Files:**
- No code changes — regression testing

**Step 1: Run all SPMD LLVM tests**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1`
Expected: All tests pass (107+).

**Step 2: Run E2E test suite**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -30`
Expected: Same pass/fail counts as before (20 run pass, 4 compile-only pass, 10 reject OK). No new failures.

**Step 3: Run mandelbrot benchmark 3 times for stable numbers**

Run 3x: `node --experimental-wasi-unstable-preview1 /home/cedric/work/SPMD/test/e2e/run-wasm.mjs /tmp/mandelbrot-simd.wasm`
Expected: Consistent speedup improvement, 0 differences.

---

### Task 10: Unified CFG helpers refactor (Commit 2)

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add helper methods, update callers)
- Modify: `tinygo/compiler/compiler.go` (replace branches with helper calls)

**Step 1: Add helper methods to spmdActiveLoop**

Add after the `spmdActiveLoop` struct definition (~line 470):

```go
// iterPhiBlock returns the SSA block containing the iterator phi.
// For rangeint: the body block (where iterPhi lives).
// For rangeindex: the loop block (where the rangeindex phi lives).
func (loop *spmdActiveLoop) iterPhiBlock() *ssa.BasicBlock {
	if loop.iterPhi != nil {
		return loop.iterPhi.Block()
	}
	// Fallback (shouldn't happen for properly initialized loops).
	return loop.incrBinOp.Block()
}

// entryPredecessor returns the SSA block that enters the loop from outside
// (not a back-edge from inside the loop). Uses the block structure:
// - rangeint: body block's predecessor that isn't the loop block
// - rangeindex: loop block's predecessor that isn't a body/interior block
func (loop *spmdActiveLoop) entryPredecessor(state *spmdLoopState, bodyBlockSet map[int]bool) *ssa.BasicBlock {
	if loop.isRangeIndex {
		loopBlock := loop.incrBinOp.Block()
		for _, pred := range loopBlock.Preds {
			if _, isBody := state.bodyBlocks[pred.Index]; !isBody {
				if bodyBlockSet != nil && bodyBlockSet[pred.Index] {
					continue
				}
				return pred
			}
		}
	} else {
		bodyBlock := loop.iterPhi.Block()
		for _, pred := range bodyBlock.Preds {
			if _, isLoop := state.loopBlocks[pred.Index]; !isLoop {
				return pred
			}
		}
	}
	return nil
}
```

**Step 2: Replace rangeint/rangeindex branches in phi wiring**

In `compiler.go` (~lines 1784-1813), replace the `if loop.isRangeIndex { ... } else { ... }` block with:

```go
			entryPred := loop.entryPredecessor(b.spmdLoopState, peeled.bodyBlockSet)
			if entryPred != nil {
				entryPredExit = b.blockInfo[entryPred.Index].exit
			}
```

**Step 3: Replace rangeint/rangeindex branches in emitSPMDTailBody**

In `spmd.go` the body block handling at line 5338 (`if loop.isRangeIndex { ... }`) stays as-is because it handles different prologue emission locations — this is a case where the distinction genuinely matters (rangeindex emits at block entry, rangeint emits at phi encounter). Keep `isRangeIndex` for this.

**Step 4: Run all tests**

Run: `cd tinygo && GO=/home/cedric/work/SPMD/go/bin/go go test ./compiler/ -run TestSPMD -v -count=1`
Expected: All tests pass — refactor is behavior-preserving.

**Step 5: Commit**

```bash
cd tinygo && git add compiler/spmd.go compiler/compiler.go
git commit -m "refactor: add helper methods to spmdActiveLoop for unified CFG access

Add iterPhiBlock() and entryPredecessor() methods to eliminate
rangeint/rangeindex branching in tail.check phi wiring."
```

---

### Task 11: Update SPMD workspace (submodule + PLAN.md)

**Files:**
- Modify: `/home/cedric/work/SPMD/PLAN.md` (update status)
- Update: TinyGo submodule reference

**Step 1: Update PLAN.md**

Add rangeint loop peeling to the completed items in Phase 2. Update the "Next Priority" section. Move the deferred item for rangeint loop peeling to completed status.

**Step 2: Update submodule**

```bash
cd /home/cedric/work/SPMD
git add tinygo
git commit -m "feat: update TinyGo submodule — rangeint loop peeling support"
```

**Step 3: Update memory file**

Update `/home/cedric/.claude/projects/-home-cedric-work-SPMD/memory/MEMORY.md` with new mandelbrot benchmark numbers and rangeint peeling status.
