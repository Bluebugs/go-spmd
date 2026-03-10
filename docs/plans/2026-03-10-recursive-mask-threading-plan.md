# Recursive Mask Threading Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the two-phase mask assignment (predication + flat stamping) with a single-pass recursive model where `predicateSPMDScope` threads masks through blocks and returns potentially-narrowed masks after `continue`.

**Architecture:** `predicateSPMDScope` returns `Value` (maskOut). It processes blocks in program order, masking all ops inline. Varying `if` computes then/else masks, recursively processes sub-scopes, merges returned masks via SPMDSelect. `continue` in varying context sets maskOut to zero for those lanes. `spmdConvertLoopOps` simplified to call `predicateSPMDScope` with entry mask.

**Tech Stack:** Go, x-tools-spmd go/ssa, TinyGo compiler

---

### Task 1: Add test for continue-in-varying-if mask narrowing

This test verifies the core bug: after `continue` inside a varying `if`, subsequent memory ops must use a narrowed mask.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Step 1: Write the failing test**

Add after `TestPredicateSPMD_GoForElseIfSanity` (line ~4260):

```go
// TestPredicateSPMD_GoForContinueMaskNarrowing verifies that a continue inside
// a varying if narrows the mask for subsequent memory ops in the same iteration.
// Before this fix, the store after the if used a flat mask (all-ones or TailMask)
// instead of activeMask & ~cond.
func TestPredicateSPMD_GoForContinueMaskNarrowing(t *testing.T) {
	src := `package main

func f(data []int, out []int) {
	for i, v := range data {
		if v == 0 {
			continue
		}
		out[i] = v * 2
	}
}

func main() { f(make([]int, 8), make([]int, 8)) }
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	var buf bytes.Buffer
	ssa.WriteFunction(&buf, fn)
	output := buf.String()

	// Find the SPMDStore for `out[i] = v * 2`.
	// Its mask must NOT be a flat all-ones or TailMask parameter.
	// It must be a BinOp (AND or ANDNOT) that narrows the mask based on the
	// varying condition `v == 0`.
	var storeCount int
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.SPMDStore)
			if !ok {
				continue
			}
			storeCount++
			// The mask should be derived from the condition, not a flat mask.
			// A flat mask would be a Const (all-ones) or a Parameter (TailMask).
			switch store.Mask.(type) {
			case *ssa.Const:
				t.Errorf("SPMDStore mask is a Const (flat mask); expected narrowed mask after continue:\n%s", output)
			case *ssa.Parameter:
				t.Errorf("SPMDStore mask is a Parameter (flat TailMask); expected narrowed mask after continue:\n%s", output)
			}
		}
	}
	if storeCount == 0 {
		t.Errorf("expected at least one SPMDStore for `out[i] = v * 2`:\n%s", output)
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_GoForContinueMaskNarrowing -v -count=1
```
Expected: FAIL — the SPMDStore mask is a Const (all-ones) or Parameter (flat mask)

**Step 3: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate_test.go
git commit -m "test: add failing test for continue-in-varying-if mask narrowing"
```

---

### Task 2: Add test for nested varying-if continue mask composition

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Step 1: Write the failing test**

```go
// TestPredicateSPMD_GoForNestedContinueMask verifies that continue inside a
// nested varying if composes masks correctly: the store between inner and outer
// if uses the inner-narrowed mask, and the store after the outer if uses the
// fully-narrowed mask.
func TestPredicateSPMD_GoForNestedContinueMask(t *testing.T) {
	src := `package main

func f(data []int, out []int) {
	for i, v := range data {
		if v > 0 {
			if v > 10 {
				continue
			}
			out[i] = v
		}
		out[i] = v * 3
	}
}

func main() { f(make([]int, 8), make([]int, 8)) }
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	var buf bytes.Buffer
	ssa.WriteFunction(&buf, fn)
	output := buf.String()

	// There should be SPMDStore instructions and none should have a flat mask.
	var storeCount int
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			store, ok := instr.(*ssa.SPMDStore)
			if !ok {
				continue
			}
			storeCount++
			switch store.Mask.(type) {
			case *ssa.Const:
				t.Errorf("SPMDStore mask is a Const (flat mask); expected narrowed mask in nested continue:\n%s", output)
			case *ssa.Parameter:
				t.Errorf("SPMDStore mask is a Parameter (flat TailMask); expected narrowed mask in nested continue:\n%s", output)
			}
		}
	}
	if storeCount == 0 {
		t.Errorf("expected SPMDStore instructions:\n%s", output)
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_GoForNestedContinueMask -v -count=1
```
Expected: FAIL

**Step 3: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate_test.go
git commit -m "test: add failing test for nested varying-if continue mask composition"
```

---

### Task 3: Make `predicateSPMDScope` return maskOut and thread inline masking

This is the core refactor. Change `predicateSPMDScope` to:
1. Return `Value` (the potentially-narrowed activeMask)
2. Call `spmdMaskMemOps` on every block as it processes them (not just if-bodies)
3. When a varying `if` is processed, use the returned mask for subsequent blocks

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:253-331`

**Step 1: Change the signature and add inline masking**

Change `predicateSPMDScope` signature from:
```go
func predicateSPMDScope(fn *Function, scopeBlocks map[*BasicBlock]bool, lanes int, spmdLoopBlock, spmdBodyBlock *BasicBlock, activeMask Value, deferred *[]*spmdDeferredMerge) {
```
to:
```go
func predicateSPMDScope(fn *Function, scopeBlocks map[*BasicBlock]bool, lanes int, spmdLoopBlock, spmdBodyBlock *BasicBlock, activeMask Value, deferred *[]*spmdDeferredMerge) Value {
```

After the boolean chain loop (line ~284) and after the varying If loop (line ~311), and after the varying switch loop (line ~330), add return:
```go
	return activeMask
}
```

But the key change is the varying If loop (lines 298-311). Currently it calls `predicateVaryingIf` without capturing a return. We need to:
1. Track which blocks have been processed (by varying if linearization)
2. After linearization, call `spmdMaskMemOps` on remaining (straight-line) blocks
3. Thread the returned mask through

The new loop structure:

```go
	// Phase 2: Linearize varying Ifs and thread mask through.
	// Process blocks in program order. When a varying If is linearized,
	// its returned mask becomes activeMask for subsequent blocks.
	processedBlocks := make(map[*BasicBlock]bool)

	for _, block := range fn.Blocks {
		if !scopeBlocks[block] {
			continue
		}
		if processedBlocks[block] {
			continue
		}

		vif, ok := block.Instrs[len(block.Instrs)-1].(*If)
		if ok && vif.IsVarying && !excludedIfs[vif] {
			// Linearize this varying If and get the output mask.
			maskOut := predicateVaryingIf(fn, lanes, block, vif, allowLoopHeaderMerge, activeMask, deferred)
			if maskOut != nil {
				activeMask = maskOut
			}
			// Mark then/else/merge blocks as processed (they were handled by predicateVaryingIf).
			// The blocks linearized by predicateVaryingIf already had spmdMaskMemOps called.
			thenBlock := block.Succs[0] // after linearization, block jumps to thenBlock
			processedBlocks[thenBlock] = true
			if len(block.Succs) > 1 {
				processedBlocks[block.Succs[1]] = true
			}
		} else {
			// Straight-line block: mask all ops with current activeMask.
			spmdMaskMemOps(block, activeMask, lanes)
		}
	}
```

**Important consideration:** The boolean chain and switch chain handlers also need to be aware of this. For now, they don't return masks (continue inside switch/boolean chain is a separate concern). They should mark their blocks as processed so the If loop doesn't re-process them.

**Step 2: Change `predicateVaryingIf` to return maskOut**

Change signature from:
```go
func predicateVaryingIf(fn *Function, lanes int, ifBlock *BasicBlock, vif *If, allowLoopHeaderMerge bool, activeMask Value, deferred *[]*spmdDeferredMerge) {
```
to:
```go
func predicateVaryingIf(fn *Function, lanes int, ifBlock *BasicBlock, vif *If, allowLoopHeaderMerge bool, activeMask Value, deferred *[]*spmdDeferredMerge) Value {
```

For if-else patterns, the maskOut is:
```go
	// After linearization, compute the output mask.
	// For if-without-else: maskOut is activeMask (no lanes deactivated by if itself).
	// For if-else: maskOut is activeMask (both paths rejoin, no lanes lost).
	// If either branch has a continue, the mask will be narrowed via SPMDSelect.
	return activeMask
```

**But** when the then-block or else-block contains a `continue` (i.e., its original successor was the loop block), the mask is narrowed. This is handled in Task 4.

For now, all paths return `activeMask` unchanged. The tests from Tasks 1-2 will still fail (continue narrowing is Task 4).

**Step 3: Update all callers of `predicateSPMDScope`**

There are 3 callers:
1. `predicateSPMDFuncBody` (line 386): `predicateSPMDScope(fn, scope, lanes, nil, nil, activeMask, nil)` → capture return value
2. `spmdConvertLoopOps` (not a direct caller — it calls the flat-stamp functions, which we'll replace in Task 5)
3. `predicateVaryingSwitch` calls it indirectly via case-block processing

Also update `predicateVaryingIf` callers:
- Line 310: capture return
- Line 1511 (recursive else-if call): capture return

**Step 4: Run existing tests**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD -v -count=1 2>&1 | tail -20
```
Expected: All existing tests PASS (behavior unchanged since we return activeMask unchanged for now). Tasks 1-2 tests still FAIL.

**Step 5: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate.go
git commit -m "refactor: make predicateSPMDScope return maskOut, add inline masking"
```

---

### Task 4: Implement continue-aware mask narrowing in `predicateVaryingIf`

This is where the core bug gets fixed. When `predicateVaryingIf` detects that the then-block (or else-block) is a `continue` (original successor is the loop block), it sets that branch's maskOut to zero, and the merge creates `SPMDSelect(cond, zero, elseMask)`.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go`

**Step 1: Detect continue blocks**

Add a helper function:
```go
// spmdIsContinueBlock reports whether block originally jumped to loopBlock
// (i.e., it represents a `continue` statement). After linearization by
// spmdReplaceIfWithJump, the block's successors are rewired, so we check
// the block's original successors before linearization.
//
// In the if-without-else pattern where the then-block IS the continue:
//   ifBlock: if cond goto thenBlock else mergeBlock
//   thenBlock: jump loopBlock   ← this is the continue
//   mergeBlock: ...
// After linearization: ifBlock→thenBlock→mergeBlock (fall-through).
// The thenBlock originally had Succs = [loopBlock].
//
// We detect this by checking if the then-block has exactly one instruction
// (the Jump terminator) and zero non-terminator instructions, indicating
// it's an empty block that just jumps somewhere. Combined with the loop
// block context, this identifies a continue.
func spmdIsContinueBlock(block *BasicBlock, loopBlock *BasicBlock) bool {
	if loopBlock == nil {
		return false
	}
	// Check if any successor is the loop block (before linearization rewires).
	for _, succ := range block.Succs {
		if succ == loopBlock {
			return true
		}
	}
	return false
}
```

**Step 2: Thread continue detection through `predicateVaryingIf`**

`predicateVaryingIf` needs access to the loop block to detect continues. Add `spmdLoopBlock *BasicBlock` parameter (already available from `predicateSPMDScope`'s `spmdLoopBlock` argument).

Change signature:
```go
func predicateVaryingIf(fn *Function, lanes int, ifBlock *BasicBlock, vif *If, allowLoopHeaderMerge bool, activeMask Value, deferred *[]*spmdDeferredMerge, spmdLoopBlock *BasicBlock) Value {
```

Before the `spmdReplaceIfWithJump` call, snapshot the continue status:
```go
	// Detect continue: check BEFORE linearization rewires the CFG.
	thenIsContinue := spmdIsContinueBlock(thenBlock, spmdLoopBlock)
	elseIsContinue := !ifWithoutElse && spmdIsContinueBlock(elseBlock, spmdLoopBlock)
```

After linearization and mem-op masking, compute maskOut:
```go
	// Compute output mask. If a branch contains continue, those lanes
	// are deactivated (maskOut for that branch = zero).
	if thenIsContinue || elseIsContinue {
		zeroMask := NewConst(constant.MakeBool(false), spmdpkg.NewVaryingMask())
		thenMaskOut := thenMask
		if thenIsContinue {
			thenMaskOut = zeroMask
		}
		var elseMaskOut Value
		if ifWithoutElse {
			// if-without-else with continue in then: elseMaskOut = activeMask
			// (the else path = merge = didn't enter the if, so full activeMask)
			elseMaskOut = activeMask
		} else if elseIsContinue {
			elseMaskOut = zeroMask
		} else {
			elseMaskOut = elseMask
		}
		// Merge: SPMDSelect(cond, thenMaskOut, elseMaskOut)
		maskCond := spmdInsertConvertToMask(/* appropriate block */, vif.Cond)
		sel := &SPMDSelect{
			Cond:  maskCond,
			True:  thenMaskOut,
			False: elseMaskOut,
			Lanes: lanes,
		}
		sel.setType(spmdpkg.NewVaryingMask())
		// Insert at the merge block (after linearization, this is where
		// control reconverges).
		// ... (insert into merge block's instruction list before terminator)
		return sel
	}
	return activeMask
```

**Note:** The exact insertion point for the SPMDSelect and maskCond depends on the linearized CFG structure. The maskCond was already computed during linearization (line 1239). We need to reuse it or compute it at the merge point. The safest approach is to insert the mask merge instruction at the merge block, just before its terminator.

**Step 3: Update all call sites**

All calls to `predicateVaryingIf` need the new `spmdLoopBlock` parameter:
- Line 310: `predicateVaryingIf(fn, lanes, block, vif, allowLoopHeaderMerge, activeMask, deferred, spmdLoopBlock)`
- Line 1174 (else-if): pass through the loopBlock
- Line 1511 (recursive): pass through the loopBlock

**Step 4: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_GoForContinueMaskNarrowing -v -count=1
```
Expected: PASS

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_GoForNestedContinueMask -v -count=1
```
Expected: PASS

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD -v -count=1 2>&1 | tail -20
```
Expected: All tests PASS (no regressions)

**Step 5: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate.go
git commit -m "feat: implement continue-aware mask narrowing in predicateVaryingIf"
```

---

### Task 5: Replace `spmdConvertLoopOps` flat-stamp with `predicateSPMDScope` call

Replace the flat-stamp functions in `spmdConvertLoopOps` with a single `predicateSPMDScope` call per scope. The entry mask (all-ones, TailMask, or loop-specific) is passed as `activeMask`.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:128-242`

**Step 1: Simplify `spmdConvertLoopOps`**

Replace the body of `spmdConvertLoopOps` (after scope block computation, lines 190-241):

```go
	if loop.IsPeeled {
		if loop.TailMask == nil {
			loop.TailMask = &Parameter{name: "spmd.tail.mask", typ: spmdpkg.NewVaryingMask(), parent: fn}
		}
		tailBlocks := spmdTailScopeBlocks(loop, liveScopeBlocks)
		mainBlocks := make(map[*BasicBlock]bool)
		for b := range liveScopeBlocks {
			if !tailBlocks[b] {
				mainBlocks[b] = true
			}
		}
		// Main phase: all lanes active.
		predicateSPMDScope(fn, mainBlocks, loop.LaneCount, loop.LoopBlock, loop.BodyBlock, allOnesMask, &deferred)
		// Tail phase: partial last iteration.
		predicateSPMDScope(fn, tailBlocks, loop.LaneCount, loop.LoopBlock, loop.BodyBlock, loop.TailMask, &deferred)
		// Narrow at TypeAsserts.
		spmdNarrowMaskAtTypeAsserts(fn, mainBlocks, allOnesMask, loop.LaneCount)
		spmdNarrowMaskAtTypeAsserts(fn, tailBlocks, loop.TailMask, loop.LaneCount)
	} else if loop.IsRangeIndex {
		if loop.TailMask == nil {
			loop.TailMask = &Parameter{name: "spmd.tail.mask", typ: spmdpkg.NewVaryingMask(), parent: fn}
		}
		predicateSPMDScope(fn, liveScopeBlocks, loop.LaneCount, loop.LoopBlock, loop.BodyBlock, loop.TailMask, &deferred)
		spmdNarrowMaskAtTypeAsserts(fn, liveScopeBlocks, loop.TailMask, loop.LaneCount)
	} else {
		predicateSPMDScope(fn, liveScopeBlocks, loop.LaneCount, loop.LoopBlock, loop.BodyBlock, allOnesMask, &deferred)
		spmdNarrowMaskAtTypeAsserts(fn, liveScopeBlocks, allOnesMask, loop.LaneCount)
	}
```

**Note:** `deferred` handling for loop-header merges still applies — declare `var deferred []*spmdDeferredMerge` and call `spmdConvertDeferredMerges` after the scope pass.

**Step 2: Remove dead flat-stamp functions**

Delete the following functions (they are no longer called):
- `spmdConvertScopedMemOps` (line ~1697)
- `spmdMaskScopedCallOps` (line ~1800)
- `spmdMaskScopedIndexOps` (line ~1824)
- `spmdMaskScopedMakeInterfaceOps` (line ~1855)

**Step 3: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD -v -count=1 2>&1 | tail -30
```
Expected: All tests PASS

**Step 4: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate.go
git commit -m "refactor: replace spmdConvertLoopOps flat-stamp with predicateSPMDScope"
```

---

### Task 6: Simplify `predicateSPMDFuncBody` to use recursive masking

Remove the separate Steps 2.5, 2.6, 3, 4 in `predicateSPMDFuncBody` — they are now handled by `predicateSPMDScope`'s inline masking.

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:360-425`

**Step 1: Simplify `predicateSPMDFuncBody`**

Replace lines 402-424 (Steps 2.5 through 5) with just the TypeAssert pass:

```go
	// Step 1: General varying control flow linearization + inline masking.
	scope := make(map[*BasicBlock]bool, len(fn.Blocks))
	for _, b := range fn.Blocks {
		scope[b] = true
	}
	predicateSPMDScope(fn, scope, lanes, nil, nil, activeMask, nil)

	// Step 2: Varying breaks in regular for-loops.
	forLoops := spmdFindRegularForLoops(fn)
	for _, fl := range forLoops {
		vbreaks := spmdFindVaryingBreaks(fn, fl)
		if len(vbreaks) == 0 {
			continue
		}
		predicateVaryingBreaks(fn, fl, vbreaks, lanes, activeMask)
	}

	// Step 3: Narrow mask at TypeAssert sites.
	allBlocks := make(map[*BasicBlock]bool, len(fn.Blocks))
	for _, b := range fn.Blocks {
		allBlocks[b] = true
	}
	spmdNarrowMaskAtTypeAsserts(fn, allBlocks, activeMask, lanes)
```

**Step 2: Delete dead global-scope functions**

Delete:
- `spmdConvertAllMemOps` (line ~3148)
- `spmdMaskAllIndexOps` (line ~3236)
- `spmdMaskCallOps` (line ~3451)
- `spmdMaskAllMakeInterfaceOps` (line ~1877)

**Step 3: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD -v -count=1 2>&1 | tail -30
```
Expected: All tests PASS

**Step 4: Commit**

```bash
git add x-tools-spmd/go/ssa/spmd_predicate.go
git commit -m "refactor: simplify predicateSPMDFuncBody, remove dead flat-stamp functions"
```

---

### Task 7: Remove TinyGo workarounds

**Files:**
- Modify: `tinygo/compiler/compiler.go:2373-2386`
- Modify: `tinygo/compiler/spmd.go:1068-1077`

**Step 1: Remove `spmd.tail.mask` Parameter fallback in `compiler.go`**

Delete the block at lines ~2373-2386:
```go
		if param, ok := expr.(*ssa.Parameter); ok && param.Name() == "spmd.tail.mask" {
			// ... fallback to all-ones ...
		}
```

**Step 2: Remove ssaLoopInfo lookup workaround in `spmd.go`**

Delete the block at lines ~1068-1077 that looks up ssaLoopInfo for non-peeled rangeindex:
```go
		var ssaLoop *ssa.SPMDLoopInfo
		for _, sl := range b.fn.SPMDLoops {
			// ...
		}
```

And the DoneBlock stop-set addition at lines ~1105-1110.

**Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```
Expected: Build succeeds

**Step 4: Run E2E tests**

```bash
cd /home/cedric/work/SPMD && rm -rf ~/.cache/tinygo && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```
Expected: No regressions from baseline (36 compile pass, 31 run pass, 7 compile fail, 10 reject OK)

**Step 5: Commit**

```bash
git add tinygo/compiler/compiler.go tinygo/compiler/spmd.go
git commit -m "cleanup: remove TinyGo TailMask workarounds, now handled by SSA-level masking"
```

---

### Task 8: Add E2E integration test for continue-after-varying-if

Create an integration test that exercises the continue-after-varying-if pattern end-to-end (compile + run + verify output).

**Files:**
- Create: `test/integration/spmd/varying-continue/main.go`
- Create: `test/integration/spmd/varying-continue/expected_output`

**Step 1: Write the integration test**

`test/integration/spmd/varying-continue/main.go`:
```go
// Test varying continue inside go for loop
// Verifies that continue narrows the mask for subsequent operations
package main

import "fmt"

func main() {
	data := []int{0, 1, 0, 2, 0, 3, 0, 4}
	out := make([]int, len(data))

	go for i, v := range data {
		if v == 0 {
			continue
		}
		out[i] = v * 2
	}

	for _, v := range out {
		fmt.Printf("%d ", v)
	}
	fmt.Println()
}
```

`test/integration/spmd/varying-continue/expected_output`:
```
0 2 0 4 0 6 0 8
```

**Step 2: Compile and run**

```bash
cd /home/cedric/work/SPMD && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd WASMOPT=/tmp/wasm-opt ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/varying-continue.wasm test/integration/spmd/varying-continue/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/varying-continue.wasm
```
Expected: `0 2 0 4 0 6 0 8`

**Step 3: Run full E2E suite to confirm no regressions**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```
Expected: Previous counts + 1 new run pass

**Step 4: Commit**

```bash
git add test/integration/spmd/varying-continue/
git commit -m "test: add E2E integration test for varying continue mask narrowing"
```

---

### Summary

| Task | Description | Type |
|------|-------------|------|
| 1 | Failing test: continue narrows mask | Test |
| 2 | Failing test: nested continue composes | Test |
| 3 | `predicateSPMDScope` returns maskOut + inline masking | Refactor |
| 4 | Continue-aware mask narrowing in `predicateVaryingIf` | Feature |
| 5 | Replace `spmdConvertLoopOps` flat-stamp | Refactor |
| 6 | Simplify `predicateSPMDFuncBody` | Cleanup |
| 7 | Remove TinyGo workarounds | Cleanup |
| 8 | E2E integration test | Test |
