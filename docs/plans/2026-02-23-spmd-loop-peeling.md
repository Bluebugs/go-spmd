# SPMD Loop Peeling Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Split SPMD `go for` loops into a main loop (all-ones mask, plain `v128.store`) and a tail (0-1 masked iterations) to eliminate per-lane store scalarization overhead on WASM SIMD128.

**Architecture:** Add a `spmdPeeledLoop` struct that tracks aligned bound, tail LLVM blocks, and tail iter phi. During the existing DomPreorder pass, the main loop emits body with `ConstAllOnes` masks. After DomPreorder, a new `emitSPMDTailBody()` re-processes body blocks into separate tail LLVM blocks with computed masks. The main loop exit branches to a `tail.check` block which conditionally enters the tail body or exits.

**Tech Stack:** Go, TinyGo compiler (`compiler/spmd.go`, `compiler/compiler.go`), LLVM IR via Go bindings, WASM SIMD128 target.

**Design doc:** `docs/plans/2026-02-23-spmd-loop-peeling-design.md`

---

### Task 1: Infrastructure — Struct Definitions and Peeling Eligibility

**Files:**
- Modify: `tinygo/compiler/spmd.go:430-460` (after `spmdActiveLoop` struct)
- Test: `tinygo/compiler/spmd_llvm_test.go` (append new test)

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDLoopPeelingEligibility(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()

	tests := []struct {
		name     string
		loop     spmdActiveLoop
		funcBody bool // spmdFuncIsBody
		want     bool
	}{
		{"rangeint_no_break", spmdActiveLoop{laneCount: 16}, false, true},
		{"rangeindex_no_break", spmdActiveLoop{laneCount: 4, isRangeIndex: true}, false, true},
		{"spmd_func_body", spmdActiveLoop{laneCount: 4}, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBuilder(t, c)
			b.spmdFuncIsBody = tt.funcBody
			got := b.spmdShouldPeelLoop(&tt.loop)
			if got != tt.want {
				t.Errorf("spmdShouldPeelLoop() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDLoopPeelingEligibility -v`
Expected: FAIL — `spmdShouldPeelLoop` not defined

**Step 3: Write minimal implementation**

Add to `tinygo/compiler/spmd.go` after the `spmdActiveLoop` struct (after line ~460):

```go
// spmdLoopPhase tracks whether we're emitting the main loop body (all-ones mask)
// or the tail body (computed mask) during loop peeling.
type spmdLoopPhase int

const (
	spmdLoopPhaseMain spmdLoopPhase = iota
	spmdLoopPhaseTail
)

// spmdPeeledLoop holds state for an SPMD loop that has been split into
// a main loop (full vectors, plain stores) and a tail (0-1 masked iterations).
type spmdPeeledLoop struct {
	loop           *spmdActiveLoop
	alignedBound   llvm.Value            // bound & ~(laneCount-1)
	tailCheckBlock llvm.BasicBlock        // phi + branch: hasTail → tail.body | exit
	tailBlockInfo  map[int]blockInfo      // SSA block index → tail LLVM blocks
	tailExitBlock  llvm.BasicBlock        // convergence point (original loop exit)
	tailIterPhi    llvm.Value             // phi in tailCheck for the iter value at main loop exit
	bodyBlockSet   map[int]bool           // set of SSA block indices belonging to this loop body
}

// spmdShouldPeelLoop returns true if the given SPMD loop is eligible for peeling.
// Loops inside SPMD function bodies (with break masks) are excluded.
func (b *builder) spmdShouldPeelLoop(loop *spmdActiveLoop) bool {
	// Don't peel loops in SPMD function bodies — they have break masks
	// that interact with the iteration mask.
	if b.spmdFuncIsBody {
		return false
	}
	// Lane count must be > 0 (sanity).
	return loop.laneCount > 0
}
```

**Step 4: Run test to verify it passes**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDLoopPeelingEligibility -v`
Expected: PASS

**Step 5: Commit**

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/spmd_llvm_test.go
git commit -m "feat: add SPMD loop peeling struct definitions and eligibility check"
```

---

### Task 2: Aligned Bound Computation and Tail Block Creation

**Files:**
- Modify: `tinygo/compiler/spmd.go` (new function `spmdSetupPeeledLoop`)
- Modify: `tinygo/compiler/compiler.go:180-204` (add `spmdPeeledLoops` field to builder)
- Modify: `tinygo/compiler/compiler.go:1314-1325` (create tail blocks after main blocks)
- Test: `tinygo/compiler/spmd_llvm_test.go` (append new test)

**Step 1: Write the failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDAlignedBound(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)

	i32Type := c.ctx.Int32Type()

	tests := []struct {
		name      string
		laneCount int
		// We verify the AND mask constant: ~(laneCount-1)
		wantMask uint64
	}{
		{"4_lanes", 4, ^uint64(3)},    // 0xFFFF...FFFC
		{"8_lanes", 8, ^uint64(7)},    // 0xFFFF...FFF8
		{"16_lanes", 16, ^uint64(15)}, // 0xFFFF...FFF0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bound := llvm.ConstInt(i32Type, 100, false)
			result := b.spmdComputeAlignedBound(bound, tt.laneCount)
			if result.IsNil() {
				t.Fatal("spmdComputeAlignedBound returned nil")
			}
			// The result should be an AND instruction.
			if result.IsAAndInst().IsNil() {
				t.Errorf("expected AND instruction, got %v", result)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDAlignedBound -v`
Expected: FAIL — `spmdComputeAlignedBound` not defined

**Step 3: Write minimal implementation**

Add to `tinygo/compiler/spmd.go`:

```go
// spmdComputeAlignedBound computes bound & ~(laneCount-1) to get the last
// multiple of laneCount that is <= bound. This is the exit condition for the
// main (unmasked) loop; the remaining 0 to laneCount-1 elements are handled
// by the tail.
func (b *builder) spmdComputeAlignedBound(bound llvm.Value, laneCount int) llvm.Value {
	mask := llvm.ConstInt(bound.Type(), ^uint64(laneCount-1), true)
	return b.CreateAnd(bound, mask, "spmd.aligned.bound")
}
```

Add field to builder struct in `tinygo/compiler/compiler.go` (after line ~201, after `spmdCondChainInner`):

```go
	spmdPeeledLoops map[*spmdActiveLoop]*spmdPeeledLoop // peeled loop state (nil if not peeled)
```

**Step 4: Run test to verify it passes**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDAlignedBound -v`
Expected: PASS

**Step 5: Add tail block creation to function setup**

Add new function to `tinygo/compiler/spmd.go`:

```go
// spmdCreateTailBlocks creates the LLVM basic blocks needed for the tail body
// of a peeled loop. This includes a tail.check block, tail versions of each
// body/interior block, and uses the original loop exit as the tail exit.
func (b *builder) spmdCreateTailBlocks(loop *spmdActiveLoop) *spmdPeeledLoop {
	peeled := &spmdPeeledLoop{
		loop:          loop,
		tailBlockInfo: make(map[int]blockInfo),
		bodyBlockSet:  make(map[int]bool),
	}

	// Identify all SSA blocks belonging to this loop body.
	for _, block := range b.fn.DomPreorder() {
		if _, isBody := b.spmdLoopState.bodyBlocks[block.Index]; isBody {
			peeled.bodyBlockSet[block.Index] = true
			continue
		}
		if _, isLoop := b.spmdLoopState.loopBlocks[block.Index]; isLoop {
			// Loop block is NOT part of the tail body (tail doesn't loop).
			continue
		}
		// Check if this block is interior to the loop body (if.then/else/done etc).
		if b.isBlockInSPMDBody(block) != nil && !b.spmdFuncIsBody {
			peeled.bodyBlockSet[block.Index] = true
		}
	}

	// Create tail LLVM blocks for each body/interior block.
	peeled.tailCheckBlock = b.ctx.AddBasicBlock(b.llvmFn, "spmd.tail.check")
	for idx := range peeled.bodyBlockSet {
		block := b.fn.Blocks[idx]
		tailBlock := b.ctx.AddBasicBlock(b.llvmFn, block.Comment+".tail")
		peeled.tailBlockInfo[idx] = blockInfo{entry: tailBlock, exit: tailBlock}
	}

	return peeled
}
```

In `tinygo/compiler/compiler.go`, in `createFunction()` after the SPMD maps initialization (~line 1505, after `b.preDetectVaryingIfs()`), add:

```go
	// SPMD: set up loop peeling for eligible loops.
	if b.spmdLoopState != nil {
		b.spmdPeeledLoops = make(map[*spmdActiveLoop]*spmdPeeledLoop)
		for _, loop := range b.spmdLoopState.activeLoops {
			if b.spmdShouldPeelLoop(loop) {
				peeled := b.spmdCreateTailBlocks(loop)
				b.spmdPeeledLoops[loop] = peeled
			}
		}
	}
```

**Step 6: Run full SPMD test suite**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All existing tests pass (no behavioral change yet)

**Step 7: Commit**

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/compiler.go tinygo/compiler/spmd_llvm_test.go
git commit -m "feat: add aligned bound computation and tail block creation for loop peeling"
```

---

### Task 3: Main Loop — Override Tail Mask to ConstAllOnes

**Files:**
- Modify: `tinygo/compiler/spmd.go:786-882` (`emitSPMDBodyPrologue`)
- Modify: `tinygo/compiler/compiler.go:1518-1532` (body block detection in DomPreorder)
- Test: `tinygo/compiler/spmd_llvm_test.go` (append new test)

**Step 1: Write the failing test**

This test verifies that when a loop is peeled, `emitSPMDBodyPrologue` produces a `ConstAllOnes` tail mask instead of a computed one. The actual test here needs a fully wired SSA function, which is hard to construct in unit tests. Instead, test the mask override logic directly:

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDPeeledMainMask(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)

	laneCount := 16
	maskType := llvm.VectorType(c.spmdMaskElemType(laneCount), laneCount)

	// Simulate main phase: spmdPeeledMainMask should return ConstAllOnes.
	mask := b.spmdPeeledMainMask(laneCount)
	if mask.IsNil() {
		t.Fatal("spmdPeeledMainMask returned nil")
	}

	// Verify it's ConstAllOnes by checking it matches the expected value.
	expected := llvm.ConstAllOnes(maskType)
	if mask.Type() != expected.Type() {
		t.Errorf("mask type = %v, want %v", mask.Type(), expected.Type())
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && go test ./compiler/ -run TestSPMDPeeledMainMask -v`
Expected: FAIL — `spmdPeeledMainMask` not defined

**Step 3: Implement the mask override**

Add to `tinygo/compiler/spmd.go`:

```go
// spmdPeeledMainMask returns a ConstAllOnes mask for the main (unmasked) loop phase.
// When LLVM sees masked.store/load with this mask, it optimizes to plain store/load.
func (b *builder) spmdPeeledMainMask(laneCount int) llvm.Value {
	maskType := llvm.VectorType(b.spmdMaskElemType(laneCount), laneCount)
	return llvm.ConstAllOnes(maskType)
}
```

Modify `emitSPMDBodyPrologue()` in `tinygo/compiler/spmd.go` (~line 786). At the very beginning of the function, before the existing logic, add a check for peeled main phase:

```go
func (b *builder) emitSPMDBodyPrologue(loop *spmdActiveLoop) {
	// SPMD loop peeling: in main phase, use all-ones mask (no tail mask computation).
	if b.spmdPeeledLoops != nil {
		if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain {
			loop.tailMask = b.spmdPeeledMainMask(loop.laneCount)
			// Lane indices and decomposed state still need to be set up
			// for contiguous/shifted detection. Fall through to the
			// scalar phi and lane index setup, but skip tail mask computation.
		}
	}
	// ... existing code continues ...
```

Wait — this requires splitting `emitSPMDBodyPrologue` so that the scalar phi setup and lane index creation still run, but the tail mask computation is skipped in main phase. Add a `phase` field to `spmdPeeledLoop`:

Add to `spmdPeeledLoop` struct:

```go
	phase spmdLoopPhase // current emission phase (Main or Tail)
```

Then modify the tail mask computation section of `emitSPMDBodyPrologue`. After the lane indices are set up but before the tail mask is computed (~line 868), add:

```go
	// SPMD loop peeling: skip tail mask computation in main phase.
	if b.spmdPeeledLoops != nil {
		if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain {
			loop.tailMask = b.spmdPeeledMainMask(loop.laneCount)
			loop.laneIndices = laneIndices
			return
		}
	}
```

And for the decomposed path (~line 840), add the same check before the tail mask computation:

```go
	// SPMD loop peeling: skip tail mask computation in decomposed main phase.
	if b.spmdPeeledLoops != nil {
		if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain {
			loop.tailMask = b.spmdPeeledMainMask(loop.laneCount)
			return
		}
	}
```

**Step 4: Run tests to verify**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All tests pass. The new code path is only taken when `spmdPeeledLoops` is populated AND `phase == spmdLoopPhaseMain`. Since `phase` defaults to `spmdLoopPhaseMain` (0), we need to ensure the peeled loop setup initializes `phase` to `spmdLoopPhaseMain` and the DomPreorder code uses it.

**Step 5: Commit**

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/spmd_llvm_test.go
git commit -m "feat: override tail mask to ConstAllOnes in peeled main loop phase"
```

---

### Task 4: Main Loop — Override Exit Condition and Branch Target

**Files:**
- Modify: `tinygo/compiler/compiler.go:2869-2881` (BinOp LSS in loop block)
- Modify: `tinygo/compiler/compiler.go:1966-1970` (*ssa.If branch targets)
- Modify: `tinygo/compiler/spmd.go` (new helper `spmdIsLoopExitBound`)

**Step 1: Override the loop exit comparison to use alignedBound**

In the BinOp handler in `compiler.go` (~line 2869), after the existing `+laneCount` transformation, add a check for the loop block's LSS comparison. The SSA pattern is `incr < bound` where `incr` is `loop.incrBinOp` and the second operand is `loop.boundValue`.

Add to `tinygo/compiler/spmd.go`:

```go
// spmdIsLoopExitBound checks if a BinOp is the loop exit comparison (incr < bound)
// for a peeled SPMD loop, and returns the aligned bound to use instead.
func (b *builder) spmdIsLoopExitBound(expr *ssa.BinOp) (llvm.Value, bool) {
	if b.spmdPeeledLoops == nil || b.spmdLoopState == nil {
		return llvm.Value{}, false
	}
	// Check if this BinOp is in a loop block.
	loop, ok := b.spmdLoopState.loopBlocks[b.currentBlock.Index]
	if !ok {
		return llvm.Value{}, false
	}
	peeled, ok := b.spmdPeeledLoops[loop]
	if !ok || peeled.phase != spmdLoopPhaseMain {
		return llvm.Value{}, false
	}
	// Check if this is the LSS comparison: X is the incr, Y is the bound.
	if expr.Op != token.LSS {
		return llvm.Value{}, false
	}
	if expr.X != loop.incrBinOp {
		return llvm.Value{}, false
	}
	return peeled.alignedBound, true
}
```

In `compiler.go` BinOp handler (~line 2869), after the increment override and before the generic BinOp code, add:

```go
	// SPMD loop peeling: replace bound with alignedBound in loop exit comparison.
	if expr.Op == token.LSS {
		if alignedBound, ok := b.spmdIsLoopExitBound(expr); ok {
			return b.CreateICmp(llvm.IntSLT, x, alignedBound, ""), nil
		}
	}
```

**Step 2: Override the loop block's branch target**

In `compiler.go` `*ssa.If` case (~line 1966), after computing `blockElse`, add a redirect for the loop block exit:

```go
	case *ssa.If:
		cond := b.getValue(instr.Cond, getPos(instr))
		block := instr.Block()
		blockThen := b.blockInfo[block.Succs[0].Index].entry
		blockElse := b.blockInfo[block.Succs[1].Index].entry
		// SPMD loop peeling: redirect loop exit to tail.check.
		if b.spmdPeeledLoops != nil && b.spmdLoopState != nil {
			if loop, ok := b.spmdLoopState.loopBlocks[block.Index]; ok {
				if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain {
					blockElse = peeled.tailCheckBlock
				}
			}
		}
```

**Step 3: Compute alignedBound in the entry block**

The `alignedBound` needs to be computed before the loop starts. Add it to the peeled loop setup in `compiler.go`, in the `createFunction()` peeling setup code (added in Task 2, step 5). After `spmdCreateTailBlocks`:

```go
		if b.spmdShouldPeelLoop(loop) {
			peeled := b.spmdCreateTailBlocks(loop)
			peeled.phase = spmdLoopPhaseMain
			b.spmdPeeledLoops[loop] = peeled
		}
```

The actual `alignedBound` computation must happen AFTER the bound value is available (it's an SSA value that gets compiled during block processing). We compute it lazily in `emitSPMDBodyPrologue`:

In `emitSPMDBodyPrologue`, right after getting `boundScalar` (~line 824 for decomposed, ~line 869 for non-decomposed), if in peeled main phase:

```go
	// SPMD loop peeling: compute aligned bound if not yet computed.
	if b.spmdPeeledLoops != nil {
		if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.alignedBound.IsNil() {
			peeled.alignedBound = b.spmdComputeAlignedBound(boundScalar, loop.laneCount)
		}
	}
```

**Step 4: Run full SPMD test suite**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All existing tests pass. The exit condition override only fires for peeled loops.

**Step 5: Commit**

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/compiler.go
git commit -m "feat: override loop exit condition and branch target for peeled main loop"
```

---

### Task 5: Tail Body Emission — Core Implementation

**Files:**
- Modify: `tinygo/compiler/spmd.go` (new functions `emitSPMDTailBody`, `emitSPMDTailCheck`)
- Modify: `tinygo/compiler/compiler.go:1679` (after DomPreorder loop, call tail emission)

**Step 1: Implement tail check block emission**

Add to `tinygo/compiler/spmd.go`:

```go
// emitSPMDTailCheck emits the tail.check block that bridges the main loop exit
// to either the tail body (if there are remaining elements) or the exit block.
// It creates a phi node for the iterator value from the main loop.
func (b *builder) emitSPMDTailCheck(peeled *spmdPeeledLoop) {
	loop := peeled.loop

	b.SetInsertPointAtEnd(peeled.tailCheckBlock)

	// Get the main loop's scalar iter value at exit.
	// This comes from the main loop block (after increment).
	// We need a phi because tail.check has two predecessors:
	// 1. Entry (if main loop was skipped: alignedBound == 0)
	// 2. Main loop block (after last iteration)
	i32Type := b.ctx.Int32Type()
	phi := b.CreatePHI(i32Type, "spmd.tail.iter")

	// The phi incoming values are set up later when all blocks are finalized.
	peeled.tailIterPhi = phi

	// Branch: tailIter < bound → tail.body, else → exit.
	boundScalar := b.getValue(loop.boundValue, token.NoPos)
	hasTail := b.CreateICmp(llvm.IntSLT, phi, boundScalar, "spmd.has.tail")

	// Find the first tail body block (the body entry point in the tail).
	var tailBodyEntry llvm.BasicBlock
	for _, block := range b.fn.DomPreorder() {
		if _, isBody := b.spmdLoopState.bodyBlocks[block.Index]; isBody {
			if _, ok := b.spmdPeeledLoops[b.spmdLoopState.bodyBlocks[block.Index]]; ok {
				if info, exists := peeled.tailBlockInfo[block.Index]; exists {
					tailBodyEntry = info.entry
					break
				}
			}
		}
	}
	if tailBodyEntry.IsNil() {
		// Fallback: should not happen for well-formed loops.
		b.CreateBr(peeled.tailExitBlock)
		return
	}

	b.CreateCondBr(hasTail, tailBodyEntry, peeled.tailExitBlock)
}
```

**Step 2: Implement tail body re-emission**

Add to `tinygo/compiler/spmd.go`:

```go
// emitSPMDTailBody re-processes the SPMD body blocks to emit the tail iteration
// with a computed tail mask. The tail runs at most once (no loop).
func (b *builder) emitSPMDTailBody(peeled *spmdPeeledLoop) {
	loop := peeled.loop

	// Switch to tail phase.
	peeled.phase = spmdLoopPhaseTail

	// Save builder state.
	savedLocals := b.locals
	savedOverride := b.spmdValueOverride
	savedDecomposed := b.spmdDecomposed
	savedContiguous := b.spmdContiguousPtr
	savedShifted := b.spmdShiftedPtr
	savedMaskStack := b.spmdMaskStack
	savedBlockInfo := make([]blockInfo, len(b.blockInfo))
	copy(savedBlockInfo, b.blockInfo)

	// Clone locals: keep values defined outside the SPMD body.
	b.locals = make(map[ssa.Value]llvm.Value)
	for k, v := range savedLocals {
		if !peeled.bodyBlockSet[k.Block().Index] {
			b.locals[k] = v
		}
	}

	// Override the iterator phi's value to the tail iter from tail.check.
	b.locals[loop.bodyIterValue] = peeled.tailIterPhi

	// Swap block info to tail blocks.
	for idx, info := range peeled.tailBlockInfo {
		b.blockInfo[idx] = info
	}

	// Fresh SPMD maps for tail phase.
	b.spmdValueOverride = make(map[ssa.Value]llvm.Value)
	b.spmdDecomposed = make(map[ssa.Value]*spmdDecomposedIndex)
	b.spmdContiguousPtr = make(map[ssa.Value]*spmdContiguousInfo)
	b.spmdShiftedPtr = make(map[ssa.Value]*spmdShiftedLoadInfo)
	b.spmdMaskStack = nil

	// Process body blocks in DomPreorder order (tail phase).
	for _, block := range b.fn.DomPreorder() {
		if !peeled.bodyBlockSet[block.Index] {
			continue
		}
		b.currentBlock = block
		b.currentBlockInfo = &b.blockInfo[block.Index]
		b.SetInsertPointAtEnd(b.currentBlockInfo.entry)

		// Handle SPMD body block entry.
		if _, isBody := b.spmdLoopState.bodyBlocks[block.Index]; isBody {
			b.spmdValueOverride = make(map[ssa.Value]llvm.Value)
			b.spmdDecomposed = make(map[ssa.Value]*spmdDecomposedIndex)

			if loop.isRangeIndex {
				b.emitSPMDBodyPrologue(loop)
				if !loop.isDecomposed {
					b.spmdValueOverride[loop.bodyIterValue] = loop.laneIndices
				}
				b.spmdMaskStack = []llvm.Value{loop.tailMask}
			}
		} else if b.isBlockInSPMDBody(block) != nil {
			// Interior block (if.then/else/done): keep existing overrides.
		}

		// Apply mask transitions.
		if b.spmdMaskTransitions != nil {
			if tr, ok := b.spmdMaskTransitions[block.Index]; ok {
				switch tr.kind {
				case "pushThen":
					parentMask := b.spmdCurrentMask()
					if !parentMask.IsNil() {
						thenMask := b.CreateAnd(parentMask, tr.cond, "spmd.then.mask")
						b.spmdPushMask(thenMask)
					}
				case "swapElse":
					b.spmdPopMask()
					parentMask := b.spmdCurrentMask()
					if !parentMask.IsNil() {
						notCond := b.CreateNot(tr.cond, "")
						elseMask := b.CreateAnd(parentMask, notCond, "spmd.else.mask")
						b.spmdPushMask(elseMask)
					}
				case "pop":
					b.spmdPopMask()
				case "pushDirect":
					b.spmdPushMask(tr.cond)
				}
			}
		}

		// Process instructions.
		for _, instr := range block.Instrs {
			if _, ok := instr.(*ssa.DebugRef); ok {
				continue
			}
			b.createInstruction(instr)

			// Handle iter phi prologue trigger (rangeint pattern).
			if b.spmdValueOverride != nil && b.spmdLoopState != nil {
				if phi, ok := instr.(*ssa.Phi); ok {
					if loopMatch, ok := b.spmdLoopState.activeLoops[phi]; ok && loopMatch == loop {
						b.emitSPMDBodyPrologue(loop)
						b.spmdValueOverride[phi] = loop.laneIndices
						b.spmdMaskStack = []llvm.Value{loop.tailMask}
					}
				}
			}
		}
	}

	// Restore builder state.
	b.locals = savedLocals
	b.spmdValueOverride = savedOverride
	b.spmdDecomposed = savedDecomposed
	b.spmdContiguousPtr = savedContiguous
	b.spmdShiftedPtr = savedShifted
	b.spmdMaskStack = savedMaskStack
	copy(b.blockInfo, savedBlockInfo)

	// Restore phase for any subsequent processing.
	peeled.phase = spmdLoopPhaseMain
}
```

**Step 3: Wire tail emission into compiler.go**

In `compiler.go`, after the DomPreorder loop (after line ~1679), add:

```go
	// SPMD loop peeling: emit tail check and tail body for each peeled loop.
	if b.spmdPeeledLoops != nil {
		for _, peeled := range b.spmdPeeledLoops {
			// Determine the exit block (the original loop exit = rangeint.done).
			loop := peeled.loop
			// Find the loop block's false successor (the original exit).
			for loopIdx := range b.spmdLoopState.loopBlocks {
				loopBlock := b.fn.Blocks[loopIdx]
				if loopBlock.Succs != nil && len(loopBlock.Succs) > 1 {
					peeled.tailExitBlock = b.blockInfo[loopBlock.Succs[1].Index].entry
					break
				}
			}

			b.emitSPMDTailCheck(peeled)
			b.emitSPMDTailBody(peeled)

			// Wire tail body exit: last tail body block → tailExitBlock.
			// The tail body's terminal instructions (Jump/If) were emitted by
			// createInstruction but targeted main blocks. We need to patch
			// the tail body's last block to branch to tailExitBlock instead.
			// This is handled by checking the tail phase in the Jump/If handlers.
		}
	}
```

**Step 4: Handle tail body terminal branches**

In `compiler.go` `*ssa.Jump` handler (~line 2053), add a redirect for tail body blocks jumping to the loop block:

```go
	case *ssa.Jump:
		// SPMD loop peeling: tail body Jump to loop block → redirect to tail exit.
		if b.spmdPeeledLoops != nil && b.spmdLoopState != nil {
			succIdx := instr.Block().Succs[0].Index
			if loop, ok := b.spmdLoopState.loopBlocks[succIdx]; ok {
				if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseTail {
					b.CreateBr(peeled.tailExitBlock)
					break
				}
			}
		}
```

Similarly, in the `*ssa.If` handler, add a redirect for tail body blocks:

```go
		// SPMD loop peeling: tail body If that exits to loop block → redirect both targets.
		if b.spmdPeeledLoops != nil && b.spmdLoopState != nil {
			// Check if either successor is a loop block that should redirect in tail.
			for i, succ := range block.Succs {
				if loop, ok := b.spmdLoopState.loopBlocks[succ.Index]; ok {
					if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseTail {
						if i == 0 {
							blockThen = peeled.tailExitBlock
						} else {
							blockElse = peeled.tailExitBlock
						}
					}
				}
			}
		}
```

**Step 5: Set up the tail.check phi incoming values**

After both the main DomPreorder and tail emission, the phi in tail.check needs incoming values from both entry (0) and main loop block (mainNext). Add this after the tail body emission in `compiler.go`:

```go
			// Set up tail.check phi incoming values.
			i32Type := b.ctx.Int32Type()
			zero := llvm.ConstInt(i32Type, 0, false)

			// Find the entry block and main loop block.
			entryBlock := b.blockInfo[0].exit // function entry flows into loop
			var mainLoopExit llvm.BasicBlock
			var mainIterNext llvm.Value
			for loopIdx, loopMatch := range b.spmdLoopState.loopBlocks {
				if loopMatch == peeled.loop {
					mainLoopExit = b.blockInfo[loopIdx].exit
					// The last instruction in the loop block is the conditional branch.
					// The iter next value is loop.scalarIterVal after increment.
					mainIterNext = b.locals[loopMatch.incrBinOp]
					break
				}
			}

			if !mainLoopExit.IsNil() && !mainIterNext.IsNil() {
				phi := peeled.tailIterPhi
				phi.AddIncoming([]llvm.Value{zero, mainIterNext},
					[]llvm.BasicBlock{entryBlock, mainLoopExit})
			}
```

**Step 6: Run full SPMD test suite**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All tests pass

**Step 7: Commit**

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/compiler.go
git commit -m "feat: implement tail body emission for SPMD loop peeling"
```

---

### Task 6: Handle Tail Body Phi Override for Iterator

**Files:**
- Modify: `tinygo/compiler/spmd.go` (`emitSPMDBodyPrologue` tail phase)
- Modify: `tinygo/compiler/compiler.go` (phi compilation in tail phase)

**Step 1: Override phi value in tail phase**

The rangeint body block has an iter phi: `iterPhi = phi [entry: 0, loop: next]`. In the tail, the iterator value comes from `peeled.tailIterPhi` (computed in tail.check). The phi should NOT be emitted as an LLVM phi (it has only one predecessor). Instead, override its value directly.

In the phi compilation section of the DomPreorder loop (or the tail body emission), detect when compiling a phi for a peeled tail body and override with `tailIterPhi`:

In `emitSPMDTailBody`, before processing instructions, override the iter phi's local value:

The code already has `b.locals[loop.bodyIterValue] = peeled.tailIterPhi` (from Step 2 of Task 5). This ensures that when the phi is compiled, `b.getValue(iterPhi)` returns `tailIterPhi`. However, the phi compilation creates a new LLVM phi node. We need to skip phi compilation for the iter phi in the tail and use the override.

Add to `emitSPMDTailBody`, inside the instruction loop, before `b.createInstruction`:

```go
			// Skip iter phi in tail — use tailIterPhi directly.
			if phi, ok := instr.(*ssa.Phi); ok {
				if loopMatch, ok := b.spmdLoopState.activeLoops[phi]; ok && loopMatch == loop {
					// Override phi to use tailIterPhi from tail.check.
					b.locals[phi] = peeled.tailIterPhi
					// Still trigger prologue.
					b.emitSPMDBodyPrologue(loop)
					b.spmdValueOverride[phi] = loop.laneIndices
					b.spmdMaskStack = []llvm.Value{loop.tailMask}
					continue // skip createInstruction for this phi
				}
			}
```

**Step 2: Run full SPMD test suite**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All tests pass

**Step 3: Commit**

```bash
git add tinygo/compiler/spmd.go
git commit -m "feat: handle iterator phi override in peeled tail body"
```

---

### Task 7: Entry Block — Aligned Bound Branch

**Files:**
- Modify: `tinygo/compiler/compiler.go` (entry block → main body or tail.check)

**Step 1: Add entry-to-main-loop branch override**

Currently the entry block flows unconditionally into the body block. With peeling, entry should branch: `if alignedBound > 0 → main.body else → tail.check`.

In `compiler.go`, in the `*ssa.Jump` handler, add a check for the entry block jumping to a peeled body block:

```go
	case *ssa.Jump:
		// SPMD loop peeling: entry block Jump to body → conditional on alignedBound.
		if b.spmdPeeledLoops != nil && b.spmdLoopState != nil {
			succIdx := instr.Block().Succs[0].Index
			if loop, ok := b.spmdLoopState.bodyBlocks[succIdx]; ok {
				if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain && !peeled.alignedBound.IsNil() {
					mainBody := b.blockInfo[succIdx].entry
					zero := llvm.ConstInt(peeled.alignedBound.Type(), 0, false)
					hasMain := b.CreateICmp(llvm.IntSGT, peeled.alignedBound, zero, "spmd.has.main")
					b.CreateCondBr(hasMain, mainBody, peeled.tailCheckBlock)
					break
				}
			}
		}
```

Note: The `alignedBound` is computed in `emitSPMDBodyPrologue` which runs in the body block. But we need it in the entry block (which runs before the body). This is a sequencing issue — `alignedBound` must be computed in the entry block, not the body block.

**Fix**: Move `alignedBound` computation to the entry block setup. In the peeling setup code (Task 2, step 5), the bound value isn't available yet (it's an SSA value compiled later). Instead, compute `alignedBound` in the entry block before the jump to body:

In `compiler.go`, in the `*ssa.Jump` handler before the peeling check:

```go
		// SPMD loop peeling: compute aligned bound in entry and branch conditionally.
		if b.spmdPeeledLoops != nil && b.spmdLoopState != nil {
			succIdx := instr.Block().Succs[0].Index
			if loop, ok := b.spmdLoopState.bodyBlocks[succIdx]; ok {
				if peeled, ok := b.spmdPeeledLoops[loop]; ok && peeled.phase == spmdLoopPhaseMain {
					// Compute aligned bound now (bound is available as an SSA value).
					boundScalar := b.getValue(loop.boundValue, getPos(instr))
					peeled.alignedBound = b.spmdComputeAlignedBound(boundScalar, loop.laneCount)

					mainBody := b.blockInfo[succIdx].entry
					zero := llvm.ConstInt(peeled.alignedBound.Type(), 0, false)
					hasMain := b.CreateICmp(llvm.IntSGT, peeled.alignedBound, zero, "spmd.has.main")
					b.CreateCondBr(hasMain, mainBody, peeled.tailCheckBlock)
					break
				}
			}
		}
```

Remove the earlier `alignedBound` computation from `emitSPMDBodyPrologue` (from Task 4 step 3) since it's now computed in the entry block.

**Step 2: Run full test suite**

Run: `cd tinygo && go test ./compiler/ -run TestSPMD -v`
Expected: All tests pass

**Step 3: Commit**

```bash
git add tinygo/compiler/compiler.go tinygo/compiler/spmd.go
git commit -m "feat: add entry block conditional branch for loop peeling"
```

---

### Task 8: E2E Testing and Benchmarking

**Files:**
- Test: existing `test/e2e/spmd-e2e-test.sh` (run full suite)
- Test: `examples/hex-encode/main.go` (benchmark)
- Modify: `test/integration/spmd/hex-encode/main.go` (add non-aligned variant)

**Step 1: Build TinyGo**

```bash
cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

**Step 2: Run existing E2E tests**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: All previously passing tests still pass. Failures should be investigated.

**Step 3: Compile hex-encode and inspect WASM**

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/hex-encode-peeled.wasm \
  examples/hex-encode/main.go

/usr/bin/wasm2wat /tmp/hex-encode-peeled.wasm > /tmp/hex-encode-peeled.wat
```

Verify in the WAT output for `main.Encode`:
- The main loop body should contain `v128.store` (NOT 16× `v128.store8_lane`)
- There should be a `spmd.tail.check` section with a conditional branch
- The tail body should still contain `v128.store8_lane` (masked path)
- Count: `v128.store8_lane` should appear only in the tail, not the main loop

**Step 4: Run hex-encode benchmark**

```bash
~/.wasmtime/bin/wasmtime /tmp/hex-encode-peeled.wasm
```

Expected: Speedup should improve from ~0.36x to significantly above 1.0x.

Also run with Node.js:

```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/hex-encode-peeled.wasm
```

**Step 5: Test edge cases — create non-aligned test**

Add a test file `test/integration/spmd/hex-encode-unaligned/main.go`:

```go
package main

import "fmt"

const hextable = "0123456789abcdef"

func main() {
	// Test with non-aligned sizes to exercise the tail.
	for _, size := range []int{0, 1, 7, 15, 16, 17, 31, 32, 33} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i & 0xFF)
		}
		dst1 := make([]byte, len(data)*2)
		dst2 := make([]byte, len(data)*2)
		Encode(dst1, data)
		EncodeScalar(dst2, data)
		if string(dst1) != string(dst2) {
			fmt.Printf("FAIL: size=%d mismatch\n", size)
			fmt.Printf("  SPMD:   %s\n", string(dst1))
			fmt.Printf("  Scalar: %s\n", string(dst2))
			return
		}
	}
	fmt.Println("PASS: all sizes match")
}

func Encode(dst, src []byte) int {
	go for i := range dst {
		v := src[i>>1]
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
		} else {
			dst[i] = hextable[v&0x0f]
		}
	}
	return len(src) * 2
}

func EncodeScalar(dst, src []byte) int {
	j := 0
	for _, v := range src {
		dst[j] = hextable[v>>4]
		dst[j+1] = hextable[v&0x0f]
		j += 2
	}
	return len(src) * 2
}
```

Compile and run:

```bash
WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/hex-unaligned.wasm \
  test/integration/spmd/hex-encode-unaligned/main.go

~/.wasmtime/bin/wasmtime /tmp/hex-unaligned.wasm
```

Expected: `PASS: all sizes match`

**Step 6: Run full E2E regression**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: Same pass/fail counts as before (20 run pass, 4 compile-only pass, etc.)

**Step 7: Commit**

```bash
git add test/integration/spmd/hex-encode-unaligned/main.go
git commit -m "test: add non-aligned hex-encode test for loop peeling tail correctness"
```

---

### Task 9: Debugging and Iteration

This task is a catch-all for issues discovered during Tasks 1-8. Common problems to watch for:

1. **LLVM verification errors**: Missing phi incoming values, type mismatches, unterminated blocks. Fix by carefully checking all phi predecessors and block terminators in both phases.

2. **Locals map pollution**: Values from the main phase leaking into the tail or vice versa. Fix by reviewing the save/restore logic in `emitSPMDTailBody`.

3. **Mask transitions in tail**: The `spmdMaskTransitions` map references block indices but the condition values (`tr.cond`) are LLVM values from the main phase. The tail needs its own condition values. Fix: re-detect varying ifs for the tail phase, or re-compute conditions during tail emission.

4. **Store coalescing in tail**: `spmdCoalescedStores` references SSA `*ssa.Store` values and stores condition values from main phase. The tail needs fresh coalescing detection. Fix: either share the SSA-level detection (ok) and re-compute LLVM values, or re-detect during tail.

5. **blockInfo restoration**: After tail emission, `b.blockInfo` must be fully restored to main blocks. Verify with `copy(b.blockInfo, savedBlockInfo)`.

**Debugging strategy**: If LLVM verification fails, add `b.mod.Dump()` before the verification call to print the full IR, then identify the problematic instruction.

---

### Summary of All Commits

1. `feat: add SPMD loop peeling struct definitions and eligibility check`
2. `feat: add aligned bound computation and tail block creation for loop peeling`
3. `feat: override tail mask to ConstAllOnes in peeled main loop phase`
4. `feat: override loop exit condition and branch target for peeled main loop`
5. `feat: implement tail body emission for SPMD loop peeling`
6. `feat: handle iterator phi override in peeled tail body`
7. `feat: add entry block conditional branch for loop peeling`
8. `test: add non-aligned hex-encode test for loop peeling tail correctness`
