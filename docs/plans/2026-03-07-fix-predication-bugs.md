# Fix SPMD Predication Bugs Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix two SSA predication bugs in x-tools-spmd that prevent lo-min, lo-max, lo-contains, and lo-clamp SPMD examples from compiling.

**Architecture:** Both bugs are in `x-tools-spmd/go/ssa/spmd_predicate.go`. Bug 1: SPMDSelect placed at loop header references mask from body block (DomPreorder violation). Fix by detecting loop-header merge in if-without-else and placing SPMDSelect in thenBlock instead. Bug 2: `findMergeBlock` can't handle if/else-if chains because elseBlock has 2 successors. Fix by recursively linearizing inner varying Ifs before the outer, with narrowed activeMask, then following single-successor chains to find the merge.

**Tech Stack:** Go, go/ssa (x-tools-spmd fork), TinyGo LLVM backend, WASM SIMD128

---

## Background

### Bug 1: "SSA value not previously found" (lo-min, lo-max, lo-contains)

**Pattern:** `if v < result { result = v }` inside `go for` (if-without-else accumulator)

**Root cause:** `predicateVaryingIf` handles if-without-else by calling `spmdReplaceBooleanChainPhis(mergeBlock, ...)` which replaces the Phi at mergeBlock with SPMDSelect. When mergeBlock is the loop header (rangeindex pattern where `thenBlock → loopHeader` and `elseBlock == loopHeader`), the SPMDSelect is placed at the loop header but references a mask BinOp defined in the body block. TinyGo's DomPreorder visits the loop header before the body, so `getValue()` panics.

**Evidence (SSA dump):**
```
Block 1 (rangeindex.loop): t1 = spmd_select<4> t11 t7 t1  ← t11 from Block 2!
Block 2 (rangeindex.body): t11 = true & t10               ← mask defined here
Block 4 (if.then):         jump 1
```

**Why `findMergeBlock` returns the loop header:** The if-without-else check at line 1668 (`thenBlock.Succs[0] == elseBlock`) matches because thenBlock(4) → loopHeader(1) and elseBlock == loopHeader(1). But there's no guard on pred count — the loop header has 3 preds (entry, body, thenBlock), unlike a normal merge which has 2.

### Bug 2: "Branch condition is not i1 type" (lo-clamp)

**Pattern:** `if v < lo { ... } else if v > hi { ... } else { ... }` inside `go for`

**Root cause:** `findMergeBlock` can't find a merge for the outer If because elseBlock has 2 successors (the inner varying If). Returns nil, silently skipping the outer If. The inner If IS linearized (it's a simple diamond), but the outer If survives to TinyGo with a vector condition.

**Evidence (debug output):**
```
Block 1 (rangeint.body):  if varying (v < lo)      → Block 3, Block 5   ← NOT linearized
Block 5 (if.else):        mask ops; jump Block 6                         ← linearized
```

**Why predication skips the outer If:** `findMergeBlock(thenBlock=3, elseBlock=5)` — Block 3 has 1 succ (Block 4), Block 5 has 2 succs (inner If not yet linearized). Neither the diamond check nor the if-without-else check matches. Returns nil.

---

## Task 1: Bug 1 — Guard `findMergeBlock` against loop-header if-without-else

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1667-1670`

**Step 1: Add pred count guard to if-without-else check**

In `findMergeBlock`, the if-without-else check returns elseBlock as the merge without verifying it's safe. Add a guard matching the diamond pattern's guard:

```go
// If-without-else: then-block's sole successor is the else-block (fall-through).
if len(thenBlock.Succs) == 1 && thenBlock.Succs[0] == elseBlock {
	// Guard: if elseBlock has more than 2 predecessors, it is likely a
	// loop header with an entry edge. Placing SPMDSelect there would
	// reference a mask from a body block that is visited later in
	// DomPreorder (domination violation). Return nil so the caller
	// can handle this as a loop-header merge pattern.
	if len(elseBlock.Preds) > 2 {
		return nil
	}
	return elseBlock
}
```

**Step 2: Run existing tests to verify no regressions**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDPredicate -count=1 -v 2>&1 | tail -20`

Expected: All existing predicate tests pass. The guard only triggers when elseBlock has >2 preds, which none of the existing test patterns produce.

**Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go
git commit -m "fix: guard findMergeBlock if-without-else against loop-header merge"
```

---

## Task 2: Bug 1 — Handle if-without-else loop-header merge in predicateVaryingIf

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1133-1234` (predicateVaryingIf)

After Task 1, `findMergeBlock` returns nil for the if-without-else loop-header pattern. The code at lines 1148-1174 checks for various nil-merge patterns but none match this case:
- LOR short-circuit: `elseBlock.Succs[0] == thenBlock` — NO (elseBlock is loop header, has multiple succs)
- Loop-header merge (trampoline): `thenBlock.Succs[0] == elseBlock.Succs[0]` — NO (they don't share a successor)
- Loop-header merge (deferred): same check — NO

**Step 1: Add new handler for if-without-else loop-header pattern**

After the existing loop-header merge checks (line 1171) and before the `if mergeBlock == nil { return }` (line 1173), add:

```go
// If-without-else where the else block (merge) is a loop header:
// thenBlock's sole successor IS elseBlock, but elseBlock has >2 preds
// (entry edge + back-edge from thenBlock + ifBlock edge).
// We cannot place SPMDSelect at the loop header (mask is defined in
// ifBlock, visited after loopHeader in DomPreorder). Instead:
// 1. Snapshot phi edges at loopHeader before CFG changes
// 2. Compute masks, replace If with Jump
// 3. Insert SPMDSelect in thenBlock (before its Jump)
// 4. Update the Phi's thenBlock edge to reference SPMDSelect
// 5. Mask mem ops in thenBlock
if len(thenBlock.Succs) == 1 && thenBlock.Succs[0] == elseBlock &&
	len(elseBlock.Preds) > 2 {
	spmdLinearizeIfWithoutElseLoopHeader(fn, ifBlock, vif, thenBlock, elseBlock, activeMask, lanes)
	return
}
```

**Step 2: Implement `spmdLinearizeIfWithoutElseLoopHeader`**

Add this function after `spmdLinearizeLoopHeaderMerge` (around line 1302):

```go
// spmdLinearizeIfWithoutElseLoopHeader handles the case where a varying
// if-without-else has the loop header as its merge block (elseBlock).
// thenBlock's sole successor is elseBlock (the loop header), but the loop
// header has >2 predecessors (entry, ifBlock, thenBlock) making it unsafe
// to place SPMDSelect there (the mask is defined in ifBlock, which is
// visited after the loop header in DomPreorder).
//
// Instead, we insert SPMDSelect in thenBlock and keep the Phi at the loop
// header. The Phi's thenBlock edge is updated to reference the SPMDSelect.
//
// Before:
//   loopHeader: Phi [entry: init, ifBlock: accum, thenBlock: newVal]; ...
//   ifBlock:    if varying cond goto thenBlock else loopHeader
//   thenBlock:  jump loopHeader
//
// After:
//   loopHeader: Phi [entry: init, thenBlock: sel]; ...
//   ifBlock:    maskCond, thenMask; jump thenBlock
//   thenBlock:  sel = SPMDSelect(thenMask, newVal, accum); jump loopHeader
func spmdLinearizeIfWithoutElseLoopHeader(fn *Function, ifBlock *BasicBlock, vif *If, thenBlock, loopHeader *BasicBlock, activeMask Value, lanes int) {
	// Step 1: Snapshot phi edges at loopHeader BEFORE any CFG changes.
	type phiEdge struct {
		phi     *Phi
		thenVal Value // value from thenBlock edge
		elseVal Value // value from ifBlock edge (the "else"/unchanged value)
	}
	var edges []phiEdge
	for _, instr := range loopHeader.Instrs {
		phi, ok := instr.(*Phi)
		if !ok {
			break
		}
		var tv, ev Value
		for j, pred := range loopHeader.Preds {
			if pred == thenBlock {
				tv = phi.Edges[j]
			} else if pred == ifBlock {
				ev = phi.Edges[j]
			}
		}
		if tv != nil && ev != nil {
			edges = append(edges, phiEdge{phi: phi, thenVal: tv, elseVal: ev})
		}
	}

	// Step 2: Compute masks (insert before the If terminator in ifBlock).
	maskCond := spmdInsertConvertToMask(ifBlock, vif.Cond)
	thenMask := spmdInsertMaskAnd(ifBlock, activeMask, maskCond)

	// Step 3: Replace the If with a Jump to thenBlock.
	// This removes ifBlock from loopHeader.Preds and compacts Phi edges.
	spmdReplaceIfWithJump(ifBlock, thenBlock, loopHeader)

	// Step 4: For each Phi with different then/else values, insert SPMDSelect
	// in thenBlock and update the Phi's thenBlock edge.
	for _, e := range edges {
		if e.thenVal == e.elseVal {
			continue // no select needed
		}
		sel := &SPMDSelect{
			Mask:  thenMask,
			X:     e.thenVal,
			Y:     e.elseVal,
			Lanes: lanes,
		}
		sel.setType(e.phi.Type())
		spmdInsertBeforeTerminator(thenBlock, sel)
		spmdAddReferrer(thenMask, sel)
		spmdAddReferrer(e.thenVal, sel)
		spmdAddReferrer(e.elseVal, sel)

		// Update the Phi's thenBlock edge to reference SPMDSelect.
		for j, pred := range loopHeader.Preds {
			if pred == thenBlock {
				oldEdge := e.phi.Edges[j]
				if oldEdge != nil {
					if refs := oldEdge.Referrers(); refs != nil {
						*refs = spmdRemoveOneReferrer(*refs, e.phi)
					}
				}
				e.phi.Edges[j] = sel
				spmdAddReferrer(sel, e.phi)
				break
			}
		}
	}

	// Step 5: Mask memory operations in thenBlock.
	spmdMaskMemOps(thenBlock, thenMask, lanes)
}
```

**Step 3: Run existing tests**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMD -count=1 -v 2>&1 | tail -20`

Expected: All existing tests pass.

**Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go
git commit -m "fix: handle if-without-else loop-header merge by placing SPMDSelect in thenBlock"
```

---

## Task 3: Bug 1 — Write E2E test and verify lo-min, lo-max, lo-contains compile and run

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh` (promote lo-min, lo-max, lo-contains from compile-only to run)

**Step 1: Rebuild TinyGo and clear cache**

Run: `cd /home/cedric/work/SPMD && make build-tinygo && rm -rf ~/.cache/tinygo`

**Step 2: Compile and run lo-min**

Run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -target=wasi -scheduler=none -o /tmp/lo-min.wasm test/integration/spmd/lo-min/main.go && \
  node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/lo-min.wasm
```

Expected: Compilation succeeds. Output includes "Correctness: PASS".

**Step 3: Compile and run lo-max and lo-contains**

Run same command with `lo-max` and `lo-contains`. Expected: All three pass correctness check.

**Step 4: Promote tests in E2E script**

In `test/e2e/spmd-e2e-test.sh`, change the lo-min, lo-max, lo-contains entries from `test_compile` to `test_compile_and_run`. Remove the "TODO: promote" comments.

**Step 5: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/e2e/spmd-e2e-test.sh
git commit -m "fix: promote lo-min, lo-max, lo-contains from compile-only to run-pass"
```

---

## Task 4: Bug 2 — Extend `predicateVaryingIf` with recursive else-if handling

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1133-1234` (predicateVaryingIf)
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1649-1675` (findMergeBlock)

The if/else-if pattern requires:
1. Linearizing the inner (else-if) varying If FIRST with a narrowed activeMask
2. Following single-successor chains to find the common merge block
3. Using the chain terminal (not elseBlock) for Phi edge lookup

**Step 1: Extend `findMergeBlock` to follow single-successor chains**

After the simple diamond check (line 1665) and before the if-without-else check (line 1667), add an extended diamond check:

```go
// Extended diamond: thenBlock has 1 succ, elseBlock has a chain of
// single-successor blocks (from linearized inner if/else-if) that
// eventually reaches the same merge as thenBlock.
if len(thenBlock.Succs) == 1 {
	merge := thenBlock.Succs[0]
	// Follow elseBlock's single-successor chain looking for merge.
	b := elseBlock
	visited := map[*BasicBlock]bool{b: true}
	for len(b.Succs) == 1 && b.Succs[0] != merge {
		next := b.Succs[0]
		if visited[next] {
			break // cycle
		}
		visited[next] = true
		b = next
	}
	if len(b.Succs) == 1 && b.Succs[0] == merge {
		if len(merge.Preds) != 2 {
			return nil
		}
		return merge
	}
}
```

**Step 2: Restructure `predicateVaryingIf` to handle recursive else-if**

The current flow in `predicateVaryingIf` is:
1. Find merge (line 1139)
2. Handle nil-merge special cases (lines 1140-1176)
3. Compute masks (lines 1178-1186)
4. Replace If with Jump (line 1210)
5. Handle if-without-else or if-else (lines 1212-1233)

For the if/else-if pattern, we need to:
1. Compute masks FIRST
2. Replace If with Jump
3. Recursively linearize inner varying If in elseBlock with narrowed elseMask
4. THEN find merge (inner blocks now linearized, chain-following works)
5. Handle Phi conversion at merge

Restructure the function by adding an early detection of the else-if pattern. After computing masks and replacing the If, check if elseBlock has a varying If and recurse:

At the beginning of `predicateVaryingIf`, add early else-if detection:

```go
func predicateVaryingIf(fn *Function, lanes int, ifBlock *BasicBlock, vif *If, allowLoopHeaderMerge bool, activeMask Value, deferred *[]*spmdDeferredMerge) {
	thenBlock := ifBlock.Succs[0]
	elseBlock := ifBlock.Succs[1]

	// Detect if/else-if pattern: elseBlock has a varying If terminator.
	// Must linearize the inner If FIRST with narrowed activeMask so that
	// findMergeBlock can follow the resulting single-successor chain.
	innerVif, hasInnerVaryingIf := elseBlock.Instrs[len(elseBlock.Instrs)-1].(*If)
	if hasInnerVaryingIf && !innerVif.IsVarying {
		hasInnerVaryingIf = false
	}

	if hasInnerVaryingIf {
		// Compute outer masks early (before inner recursion needs elseMask).
		maskCond := spmdInsertConvertToMask(ifBlock, vif.Cond)
		thenMask := spmdInsertMaskAnd(ifBlock, activeMask, maskCond)
		elseMask := spmdInsertMaskAndNot(ifBlock, activeMask, maskCond)

		// Replace outer If with Jump to thenBlock.
		spmdReplaceIfWithJump(ifBlock, thenBlock, elseBlock)

		// Recursively linearize inner varying If with narrowed mask.
		predicateVaryingIf(fn, lanes, elseBlock, innerVif, allowLoopHeaderMerge, elseMask, deferred)

		// After inner linearization, find merge via chain-following.
		mergeBlock := findMergeBlock(thenBlock, elseBlock)
		if mergeBlock == nil {
			// Check loop-header patterns for the outer if.
			if allowLoopHeaderMerge &&
				len(thenBlock.Succs) == 1 {
				// Follow else chain to terminal.
				elseEnd := elseBlock
				for len(elseEnd.Succs) == 1 {
					elseEnd = elseEnd.Succs[0]
				}
				if len(elseEnd.Succs) == 0 {
					return // dead end, skip
				}
			}
			return // complex, skip
		}

		// Snapshot merge block Phis AFTER inner linearization but BEFORE
		// rewiring thenBlock. The inner linearization may have changed
		// mergeBlock.Preds (removed inner blocks, kept terminal).
		var phiSnaps []spmdPhiSnapshot
		for _, instr := range mergeBlock.Instrs {
			phi, ok := instr.(*Phi)
			if !ok {
				break
			}
			snap := spmdPhiSnapshot{phi: phi, edgeVal: make(map[*BasicBlock]Value, len(mergeBlock.Preds))}
			for j, pred := range mergeBlock.Preds {
				snap.edgeVal[pred] = phi.Edges[j]
			}
			phiSnaps = append(phiSnaps, snap)
		}

		// Find the actual else-side predecessor of mergeBlock.
		// After inner linearization, it's the terminal of the else chain.
		elseTerminal := elseBlock
		for len(elseTerminal.Succs) == 1 && elseTerminal.Succs[0] != mergeBlock {
			elseTerminal = elseTerminal.Succs[0]
		}

		// Rewire thenBlock from mergeBlock to elseBlock (B→T→E chain).
		spmdRewireThenToElse(thenBlock, mergeBlock, elseBlock)

		// Replace Phis at merge with SPMDSelect.
		// Use thenBlock and elseTerminal as the Phi edge sources.
		spmdReplacePhisWithSelect(mergeBlock, thenBlock, elseTerminal, thenMask, elseMask, lanes)

		// Mask mem ops in thenBlock only (elseBlock chain already masked by recursion).
		spmdMaskMemOps(thenBlock, thenMask, lanes)
		return
	}

	// Original code for non-else-if patterns follows unchanged...
	mergeBlock := findMergeBlock(thenBlock, elseBlock)
	// ... rest of existing function ...
```

**Step 3: Run existing tests**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMD -count=1 -v 2>&1 | tail -20`

Expected: All existing tests pass.

**Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go
git commit -m "fix: handle if/else-if chains by recursive predication with narrowed masks"
```

---

## Task 5: Bug 2 — E2E test for lo-clamp

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh` (promote lo-clamp from compile-only to run)

**Step 1: Rebuild TinyGo and clear cache**

Run: `cd /home/cedric/work/SPMD && make build-tinygo && rm -rf ~/.cache/tinygo`

**Step 2: Compile and run lo-clamp**

Run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -target=wasi -scheduler=none -o /tmp/lo-clamp.wasm test/integration/spmd/lo-clamp/main.go && \
  node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/lo-clamp.wasm
```

Expected: Compilation succeeds. Output includes "Correctness: PASS".

**Step 3: Promote in E2E script**

In `test/e2e/spmd-e2e-test.sh`, change lo-clamp from `test_compile` to `test_compile_and_run`. Remove the "TODO: promote" comment.

**Step 4: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/e2e/spmd-e2e-test.sh
git commit -m "fix: promote lo-clamp from compile-only to run-pass"
```

---

## Task 6: Run full E2E suite and update docs

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh` (run full suite)
- Modify: `docs/lo-spmd-comparison.md` (update backend status)

**Step 1: Run full E2E suite**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -30`

Expected: lo-min, lo-max, lo-contains, lo-clamp all pass. No regressions in other tests.

**Step 2: Update comparison doc**

In `docs/lo-spmd-comparison.md`, update the "Current Backend Status" section. Change Min, Max, Contains, Clamp from "Compile fail" to "Run pass". Update the summary text to reflect that all 6 operations now compile and run correctly.

**Step 3: Commit**

```bash
cd /home/cedric/work/SPMD
git add docs/lo-spmd-comparison.md
git commit -m "docs: update lo-spmd comparison to reflect all 6 operations now run-pass"
```

---

## Implementation Notes

### Testing approach

Both fixes modify x-tools-spmd SSA predication. The primary validation is:
1. Existing x-tools-spmd unit tests (`go test ./go/ssa/ -run TestSPMD`)
2. E2E compile-and-run of lo-min, lo-max, lo-contains, lo-clamp
3. Full E2E suite to check for regressions

### Risk areas

- **Bug 1 fix**: The new `spmdLinearizeIfWithoutElseLoopHeader` creates SPMDSelect in thenBlock and modifies Phi edges. The Phi edge update must happen AFTER `spmdReplaceIfWithJump` compacts edges (ifBlock is removed as a pred). The snapshotted edges capture the pre-compaction state.

- **Bug 2 fix**: Recursive `predicateVaryingIf` changes the activeMask for inner Ifs. The inner masks become `elseMask & innerCond` instead of `activeMask & innerCond`. This is correct — it narrows the mask so only lanes where the outer condition was false participate in the inner comparison. The `spmdReplacePhisWithSelect` call must use `elseTerminal` (the last block in the linearized else chain) as the else-side predecessor, not `elseBlock` itself.

- **Order of fixes**: Bug 1 (Tasks 1-3) should be implemented and tested before Bug 2 (Tasks 4-5). They are independent fixes but Bug 1 is simpler and affects more examples.
