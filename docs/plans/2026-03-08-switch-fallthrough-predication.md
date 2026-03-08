# Switch Fallthrough Predication Fix

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix `predicateVaryingSwitch` to handle `fallthrough` in switch cases inside `go for` loops.

**Architecture:** When a switch case uses `fallthrough`, go/ssa emits a Jump from the body block to the *next case's body block* rather than to `switch.done`. The current `spmdRewireBodyToNext` silently fails because it looks for `switch.done` as the successor to replace, but finds the fallthrough target instead. After `spmdReplaceIfWithJump` disconnects comparison blocks from their predecessors, those blocks (containing mask computations) become unreachable and get deleted. The fix: detect fallthrough bodies (successor != doneBlock), skip rewiring them (they already point to the right place), and relocate mask computations from unreachable comparison blocks into live blocks.

**Tech Stack:** Go, x-tools-spmd (`go/ssa` package)

---

## Bug Summary

In `predicateVaryingSwitch` (spmd_predicate.go:2536):

1. `spmdReplaceIfWithJump(compBlock3, body3, compBlock2)` removes `compBlock3` from `compBlock2.Preds`
2. `compBlock2` becomes unreachable (its only predecessor was `compBlock3`)
3. `caseMask2` (inserted into `compBlock2`) is orphaned
4. `spmdMaskMemOps(body2, caseMask2, lanes)` sets `SPMDMask` referencing the orphaned value
5. TinyGo's `getValue` panics: "SSA value not previously found in function"

## CFG With Fallthrough (Before Predication)

```
compBlock3 --If--> body3, compBlock2
body3      --Jump--> body2           ← fallthrough! NOT switch.done
compBlock2 --If--> body2, compBlock1
body2      --Jump--> body1           ← fallthrough! NOT switch.done
compBlock1 --If--> body1, switch.done
body1      --Jump--> switch.done     ← no fallthrough, normal
```

## Desired CFG After Predication

```
compBlock3 --Jump--> body3           (mask3 computed here)
body3      --Jump--> compBlock2      ← MUST rewire: was body2, now compBlock2
compBlock2 --Jump--> body2           (mask2 computed here, compBlock2 stays reachable)
body2      --Jump--> compBlock1      ← MUST rewire: was body1, now compBlock1
compBlock1 --Jump--> body1           (mask1 computed here, compBlock1 stays reachable)
body1      --Jump--> switch.done     ← already correct
```

## Fix Strategy

The key insight: with fallthrough, the body block's successor is the *next case body*, not `switch.done`. We need to rewire fallthrough bodies to point to the next *comparison block* instead, keeping comparison blocks reachable.

In Phase 1 of `predicateVaryingSwitch`:
- **Non-fallthrough body** (successor == doneBlock): current `spmdRewireBodyToNext(body, doneBlock, nextBlock)` works correctly
- **Fallthrough body** (successor == next case body): call `spmdRewireBodyToNext(body, nextCaseBody, nextBlock)` where `nextBlock` is the next comparison block

This is a one-line change in the condition passed to `spmdRewireBodyToNext` — use the body's *actual successor* as `oldDest`, not hardcode `doneBlock`.

---

### Task 1: Add Failing Unit Test — Basic Fallthrough

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Step 1: Write the failing test**

Add after `TestPredicateSPMD_SwitchSanityCheck` (~line 1240):

```go
// TestPredicateSPMD_SwitchFallthrough verifies that a varying switch with
// fallthrough is correctly linearized without orphaning mask values.
func TestPredicateSPMD_SwitchFallthrough(t *testing.T) {
	src := `package main

func f(dst []int) {
	for i := range dst {
		v := 0
		switch i % 3 {
		case 2:
			v += 100
			fallthrough
		case 1:
			v += 10
			fallthrough
		case 0:
			v += 1
		}
		dst[i] = v
	}
}

func main() { f(make([]int, 16)) }
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	// SPMDSwitchChain must be present.
	if len(fn.SPMDSwitchChains) == 0 {
		t.Fatal("expected SPMDSwitchChains to be populated")
	}

	// All switch-chain Ifs must have been linearized (Block() == nil).
	chain := fn.SPMDSwitchChains[0]
	for i, caseIf := range chain.Cases {
		if caseIf.Block() != nil {
			t.Errorf("case %d: If still has Block(): should be linearized", i)
		}
	}

	// No varying If should remain.
	for _, block := range fn.Blocks {
		if len(block.Instrs) == 0 {
			continue
		}
		if vif, ok := block.Instrs[len(block.Instrs)-1].(*ssa.If); ok {
			if vif.IsVarying {
				t.Errorf("block %d: varying If not linearized", block.Index)
			}
		}
	}

	// Mask computation instructions must be present in live blocks.
	var buf bytes.Buffer
	fn.WriteTo(&buf)
	output := buf.String()
	if !strings.Contains(output, "lanes.Varying[mask]") {
		t.Error("expected Varying[mask] mask computation instructions")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_SwitchFallthrough -v`

Expected: PANIC with "unreachable block" or sanity check failure (the build triggers `SanityCheckFunctions`).

**Step 3: Commit the failing test**

```
git add go/ssa/spmd_predicate_test.go
git commit -m "test: add failing test for switch fallthrough predication"
```

---

### Task 2: Add Failing Unit Test — Fallthrough With Stores (MemOps)

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Step 1: Write the failing test**

Add after the test from Task 1:

```go
// TestPredicateSPMD_SwitchFallthroughMemOps verifies that stores in
// fallthrough case bodies are properly masked with per-case masks.
func TestPredicateSPMD_SwitchFallthroughMemOps(t *testing.T) {
	src := `package main

func f(dst []int) {
	for i := range dst {
		switch i % 3 {
		case 2:
			dst[i] = 300
			fallthrough
		case 1:
			dst[i] = 200
			fallthrough
		case 0:
			dst[i] = 100
		}
	}
}

func main() { f(make([]int, 16)) }
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	if len(fn.SPMDSwitchChains) == 0 {
		t.Fatal("expected SPMDSwitchChains to be populated")
	}

	// Each case body has a store → should produce SPMDStore.
	// With fallthrough: case 2 falls into case 1 falls into case 0.
	// But predication should still mask each body's stores independently.
	spmdStoreCount := 0
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if _, ok := instr.(*ssa.SPMDStore); ok {
				spmdStoreCount++
			}
		}
	}
	// 3 cases, each with one store = 3 SPMDStore.
	if spmdStoreCount < 3 {
		t.Errorf("expected at least 3 SPMDStore instructions, got %d", spmdStoreCount)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_SwitchFallthroughMemOps -v`

Expected: PANIC or failure (same root cause as Task 1).

**Step 3: Commit**

```
git add go/ssa/spmd_predicate_test.go
git commit -m "test: add failing test for fallthrough switch store masking"
```

---

### Task 3: Add Failing Unit Test — Fallthrough Sanity Check

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Step 1: Write the failing test**

```go
// TestPredicateSPMD_SwitchFallthroughSanity verifies that the SSA sanity
// checker passes after predication of a switch with fallthrough.
func TestPredicateSPMD_SwitchFallthroughSanity(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		v := 0
		switch i % 4 {
		case 3:
			v += 1000
			fallthrough
		case 2:
			v += 100
			fallthrough
		case 1:
			v += 10
		default:
			v = -1
		}
		_ = v
	}
}
`
	// buildSSAWithSPMD uses SanityCheckFunctions mode — if predication
	// leaves orphaned values or broken CFG, this will panic.
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")
	if mainFn == nil {
		t.Fatal("main function not found")
	}

	if len(mainFn.SPMDSwitchChains) == 0 {
		t.Fatal("expected SPMDSwitchChains to be populated")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_SwitchFallthroughSanity -v`

Expected: PANIC (sanity check detects orphaned values or broken CFG).

**Step 3: Commit**

```
git add go/ssa/spmd_predicate_test.go
git commit -m "test: add sanity check test for fallthrough switch predication"
```

---

### Task 4: Fix `predicateVaryingSwitch` for Fallthrough

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` (lines 2600-2616)

**Step 1: Implement the fix**

The fix is in the Phase 1 loop of `predicateVaryingSwitch`. The current code at line 2611 passes `doneBlock` as `oldDest` to `spmdRewireBodyToNext`, assuming every body block jumps to `doneBlock`. With fallthrough, the body jumps to the next case body instead.

Replace lines 2598-2619 (the rewiring section in Phase 1):

```go
		// Replace the If with a Jump to the body block.
		// This removes compBlock from nextBlock's predecessors.
		spmdReplaceIfWithJump(compBlock, bodyBlock, nextBlock)

		// Rewire: redirect the body's jump so control flows sequentially
		// through all cases and the default before reaching done.
		//
		// With fallthrough, the body block's successor is the NEXT case body
		// (not doneBlock). We must detect this and rewire accordingly:
		// - Normal body (successor == doneBlock): rewire to nextBlock
		// - Fallthrough body (successor == next case body): rewire to nextBlock
		//   (the next comparison block, keeping it reachable)
		//
		// NOTE: spmdRewireBodyToNext calls oldDest.removePred(bodyBlock),
		// which also compacts oldDest's Phi.Edges. For doneBlock, this is safe
		// because we already snapshotted all Phi edge values in Phase 0.
		isLastCase := idx == len(chain.Cases)-1
		if !isLastCase {
			// Determine the body's actual successor (doneBlock or fallthrough target).
			bodySucc := bodyBlock.Succs[0] // Jump has exactly one successor
			// Non-last case: rewire body → nextBlock (the next comparison block).
			spmdRewireBodyToNext(bodyBlock, bodySucc, nextBlock)
		} else if chain.DefaultBlock != nil {
			bodySucc := bodyBlock.Succs[0]
			// Last case with a default: rewire body → defaultBlock.
			spmdRewireBodyToNext(bodyBlock, bodySucc, chain.DefaultBlock)
		}
		// Last case without default: body already jumps to doneBlock — leave it.
```

The key change: replace the hardcoded `doneBlock` in `spmdRewireBodyToNext(bodyBlock, doneBlock, nextBlock)` with `bodyBlock.Succs[0]` — the body's *actual* successor. For non-fallthrough cases this is `doneBlock` (same behavior). For fallthrough cases this is the next body block (which gets replaced with `nextBlock`, the next comparison block, keeping it reachable).

**Step 2: Run all three failing tests**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run "TestPredicateSPMD_SwitchFallthrough" -v`

Expected: All 3 tests PASS.

**Step 3: Run ALL existing switch predication tests to check for regressions**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run "TestPredicateSPMD_Switch" -v`

Expected: All existing tests PASS (the fix doesn't change behavior for non-fallthrough switches since `bodyBlock.Succs[0]` == `doneBlock` in that case).

**Step 4: Run the full SPMD test suite**

Run: `cd /home/cedric/work/SPMD/x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run "TestSPMD|TestPredicateSPMD|TestVarying" -v -count=1`

Expected: All PASS.

**Step 5: Commit**

```
git add go/ssa/spmd_predicate.go
git commit -m "fix: handle fallthrough in varying switch predication"
```

---

### Task 5: Rebuild TinyGo and Verify E2E

**Files:**
- No code changes — build and test only

**Step 1: Rebuild Go toolchain (if x-tools-spmd changes require it)**

Run: `cd /home/cedric/work/SPMD && make build-go`

**Step 2: Clear cache and rebuild TinyGo**

Run: `rm -rf ~/.cache/tinygo && cd /home/cedric/work/SPMD && make build-tinygo`

**Step 3: Compile ipv4-parser**

Run: `cd /home/cedric/work/SPMD && make compile EXAMPLE=ipv4-parser`

Expected: SUCCESS (no panic).

**Step 4: Run ipv4-parser**

Run: `node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs ipv4-parser.wasm`

Expected: Correct output for all 10 test cases.

**Step 5: Run full E2E suite**

Run: `bash test/e2e/spmd-e2e-test.sh`

Expected: No regressions (27+ run pass, 0 run fail).

**Step 6: Commit (if any fixups needed)**

---

### Task 6: Update PLAN.md Deferred Items

**Files:**
- Modify: `PLAN.md`

**Step 1:** Remove any entry about "fallthrough in varying switch" from deferred items (if one exists). Add a note in the completed items section that switch fallthrough predication is now supported.

**Step 2: Commit**

```
git commit -m "docs: mark switch fallthrough predication as done"
```

---

## Edge Cases to Consider

1. **Partial fallthrough**: Case 3 falls through to case 2, but case 2 does NOT fall through to case 1. The fix handles this because `bodyBlock.Succs[0]` correctly reflects each body's actual successor.

2. **Fallthrough to default**: If the last explicit case uses `fallthrough` and the default case follows, the body jumps to the default body. The Phase 0 phi snapshotting and Phase 2 masking should still work.

3. **Fallthrough with no stores**: Pure computation cases (no memory ops). `spmdMaskMemOps` finds nothing to mask — this is fine. The mask computations are still needed for SPMDSelect at the merge point.

4. **All cases fallthrough**: Every case falls through to the next. This is the hardest case (the ipv4-parser pattern). All comparison blocks must remain reachable.

## Risks

- **Phi edge corruption**: The Phase 0 snapshotting was designed for `doneBlock.removePred`. With fallthrough, `spmdRewireBodyToNext` calls `bodySucc.removePred(bodyBlock)` where `bodySucc` is the *next body block* (not doneBlock). If that next body block has Phi instructions, their edges could be corrupted. This should not happen because fallthrough body blocks do not have Phi instructions (they're simple Jump targets, not merge points). But verify this in the sanity test (Task 3).

- **DomPreorder ordering**: After rewiring, the block order must still allow DomPreorder traversal where all mask values are defined before use. Since comparison blocks remain reachable and dominate their body blocks, this should hold.
