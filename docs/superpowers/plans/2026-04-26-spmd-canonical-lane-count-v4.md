# SPMD Canonical Lane Count v4 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Adopt ISPC-style canonical-width for SPMD code by carrying lane count as a per-block SSA annotation, materializing per-call SSA variants of SPMD functions, and making TinyGo respect the block annotation for ALL `Varying[T]` lane-count derivations. Unblocks `n-body` / `n-body-nosqrt` runtime correctness; supersedes the v3 alloca-only annotation that broke 23 tests at the regression GATE.

**Architecture:** Three SSA passes (predicate → forward-propagate → specialize), one new SSA field (`*ssa.BasicBlock.SPMDLaneCount`), restored v3 lift guard so varying allocas survive `lift()`, and a TinyGo block-entry hook that drives a `b.spmdActiveLaneCount` builder field consulted by every `Varying[T]` materialization path (`getLLVMType` for `*types.SPMDType`, `*ssa.Alloc` lowering, swizzle/gather/scatter/reduce dispatch).

**Tech Stack:**
- Forked `x/tools` (`/home/cedric/work/SPMD/x-tools-spmd/`, branch `spmd`) — `go/ssa` block annotation, predication, propagation, specialization
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`) — `compiler/compiler.go` builder + `compiler/spmd.go` derivation sites
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`) — n-body / n-body-nosqrt unblock
- LLVM vector types: `<N x T>` materialized at the surrounding block's `SPMDLaneCount`

**Spec:** `docs/superpowers/specs/2026-04-26-spmd-canonical-lane-count-v4-design.md`

**Reverted prior attempts:**
- `docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md` (v1)
- `docs/superpowers/specs/2026-04-23-varying-local-mask-threading-v2-design.md` (v2)
- `docs/superpowers/specs/2026-04-25-varying-local-mask-threading-v3-design.md` (v3 — GATE failed at 23 broken tests)

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/ssa.go` (BasicBlock struct ~line 528) | MODIFY | Add `SPMDLaneCount int` exported field |
| `x-tools-spmd/go/ssa/lift.go` (`liftAlloc` ~line 397) | MODIFY | Restore lift guard + `isLanesVaryingType` helper |
| `x-tools-spmd/go/ssa/spmd_predicate.go` (in `spmdConvertLoopOps` ~line 354) | MODIFY | Block annotation walk: set `bb.SPMDLaneCount = loop.LaneCount` for in-scope blocks |
| `x-tools-spmd/go/ssa/spmd_propagate.go` | NEW | Forward-propagation pass — annotate entry blocks of non-SPMD functions whose allocas feed in-loop SPMD ops |
| `x-tools-spmd/go/ssa/spmd_specialize.go` | NEW | Per-call specialization pass — clone SSA per (function, lane-count) pair, rewrite call dispatch |
| `x-tools-spmd/go/ssa/func.go` (orchestration ~line 431-446) | MODIFY | Wire predicate → propagate → specialize ordering |
| `x-tools-spmd/go/ssa/export_spmd_test.go` | NEW | Test re-export of `isLanesVaryingType` |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` (restored from v3) |
| `x-tools-spmd/go/ssa/spmd_block_lanecount_test.go` | NEW | 5 tests for block annotation, propagation, specialization |
| `tinygo/compiler/compiler.go` (builder struct ~line 197 + `*ssa.Alloc` case ~line 2745 + block-entry hook) | MODIFY | Add `spmdActiveLaneCount` field, hook into block iteration, alloca materialization |
| `tinygo/compiler/spmd.go` (`spmdEffectiveLaneCount` + ~14 derivation sites) | MODIFY | Block-annotation-aware lane count |
| `tinygo/compiler/func.go` (~2 sites) | MODIFY | Same |
| `tinygo/compiler/compiler_test.go` (`testCompilePackage` ~line 235) | MODIFY | `MaxStackAlloc` propagation (from v3 attempt 1) |
| `tinygo/compiler/spmd_test.go` | MODIFY | Restored v3 IR tests + new specialization test |
| `test/integration/spmd/dual-width-spmd-func/main.go` | NEW | Specialization correctness E2E |
| `test/e2e/spmd-e2e-test.sh` | MODIFY | Register `dual-width-spmd-func` |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/BLOCKERS.md` | MODIFY/DELETE | Remove entries; delete if empty |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`. `go` submodule unchanged.

---

## Task 0: Pre-flight

**Files:** None (verification + cleanup).

- [ ] **Step 1: Confirm baseline**

```bash
cd /home/cedric/work/SPMD
for sub in x-tools-spmd tinygo tinybench go; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. `x-tools-spmd` tip is the v3 revert (`72d3b1a20` or later), `tinygo` tip is the v3 revert (`1d42ff4c` or later).

- [ ] **Step 2: Discard any leftover staged content**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && git status --short
cd /home/cedric/work/SPMD/tinygo && git status --short
cd /home/cedric/work/SPMD/tinybench && git status --short
```

If any has staged changes, restore: `git restore --staged . && git checkout -- .`. Nothing should be staged or modified.

- [ ] **Step 3: Capture E2E baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-baseline-v4.txt 2>&1
tail -10 /tmp/e2e-baseline-v4.txt
```

Expected: `Compile pass: 94, Compile fail: 0, Run pass: 93, Run fail: 0, Reject pass: 11`. Save for later GATE comparison.

- [ ] **Step 4: Capture benchmark baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-baseline-v4.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-baseline-v4.txt | head
```

Expected: benchmark runs cleanly; ratios captured.

- [ ] **Step 5: Confirm n-body-nosqrt NaN baseline**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -3
timeout 5 /tmp/nbns-spmd-bin 50000 || echo "(timed out — first line is sufficient)"
```

Expected: build succeeds, output starts with `NaN`. Timeout expected (NaN in second-pass Newton iteration hangs).

- [ ] **Step 6: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nb-spmd-bin
```

---

## Task 1: SSA struct field — `BasicBlock.SPMDLaneCount`

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go` (BasicBlock struct ~line 528)

This task adds the SSA field that all subsequent v4 work writes to and reads from. No test in this task (the field is just storage; tests come in Tasks 3+ when the field is populated).

- [ ] **Step 1: Read the current BasicBlock struct**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
grep -n "type BasicBlock struct" go/ssa/ssa.go
```

Read 20 lines starting from that line to confirm the current shape.

- [ ] **Step 2: Add the field**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go`, locate `type BasicBlock struct` and append the new field BEFORE the closing brace:

```go
type BasicBlock struct {
	// ... existing fields unchanged ...

	// SPMDLaneCount, when non-zero, is the canonical lane count for every
	// *types.SPMDType value materialized inside this block. Set by the SPMD
	// predication pass (spmdConvertLoopOps) for blocks in a `go for` loop's
	// scope, by the per-call specialization pass for blocks in a specialized
	// SPMD function variant, and by the forward-propagation pass for entry
	// blocks of non-SPMD functions whose allocas feed in-loop SPMD ops.
	// TinyGo reads this field during type materialization, alloca sizing,
	// and every other lane-count derivation; when 0, TinyGo falls back to
	// its existing element-natural / function-min derivations.
	SPMDLaneCount int
}
```

- [ ] **Step 3: Build the package**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go build ./go/ssa/... 2>&1 | tail -5
```

Expected: clean build. The new field compiles even though no consumer uses it yet.

- [ ] **Step 4: Run full SSA test suite — expect no regressions**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Expected baseline failures (acceptable, pre-existing): `TestSPMDPointerVaryingFieldAccess`, `TestSPMDVaryingPointerFieldAccess`, `TestSPMDVaryingPointerFieldStore`, `TestSPMDVectorFromMemory_Type_Byte`, `TestVaryingIf_Simple`, `TestVaryingSwitch`, `TestVaryingSwitch_ConvertStripsType`, `TestVaryingIf_String`, `TestVaryingSwitch_DefaultBlockResolution`, plus crash from `TestSPMDCloneBlock_TranslateValue` and `TestPeelSPMDLoopSimple`. NO new failures.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go
```

Do NOT commit — clean-commit handles after review.

---

## Task 2: SSA lift guard + isLanesVaryingType helper + export_spmd_test.go

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/export_spmd_test.go`

Restores the lift guard from the v3 spec (necessary for varying allocas to survive into the predication / propagation / specialization passes). Carries the same content as the v3 lift work; bundled here as one task.

- [ ] **Step 1: Create the failing test (TDD RED)**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa_test

import (
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// accumulateSrc is the shared SPMD-loop fixture: a function with no varying
// parameters that contains an SPMD loop using a Varying[int] accumulator.
// buildSSAWithSPMD (defined in spmd_loop_test.go) marks the first RangeStmt
// as IsSpmd=true so fn.SPMDLoops is non-empty and spmdConvertLoopOps runs.
const accumulateSrc = `package main

import "lanes"

func accumulate(data []int) int {
	var acc lanes.Varying[int]
	for i := range len(data) {
		_ = i
		_ = acc
	}
	return 0
}

func main() {}
`

// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca survives
// the SSA lift() pass — its Alloc instruction remains in the function body
// so the surrounding block's SPMDLaneCount governs its TinyGo materialization.
//
// Without this property, lift promotes the alloca to phi-nodes whose edge
// values are computed unconditionally on inactive lanes — causing NaN/Inf
// to leak through partial-mask go-for iterations.
func TestSPMDVaryingAllocaNotLifted(t *testing.T) {
	pkg := buildSSAWithSPMD(t, accumulateSrc)
	fn := pkg.Func("accumulate")
	if fn == nil {
		t.Fatal("accumulate function not found in SSA")
	}

	var gotAlloc bool
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			if alloc, ok := instr.(*ssa.Alloc); ok {
				if ptr, ok := alloc.Type().(*types.Pointer); ok {
					if ssa.IsLanesVaryingType(ptr.Elem()) {
						gotAlloc = true
					}
				}
			}
		}
	}
	if !gotAlloc {
		t.Fatal("varying alloca was lifted; expected memory-backed *ssa.Alloc with Varying[T] element in function body")
	}
}
```

- [ ] **Step 2: Create the test re-export**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/export_spmd_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

// Test-only re-exports.

// IsLanesVaryingType exposes the file-private isLanesVaryingType helper to
// the external ssa_test package. Production callers use the lowercase
// symbol within package ssa.
var IsLanesVaryingType = isLanesVaryingType
```

- [ ] **Step 3: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: compile error `isLanesVaryingType undefined` because the helper isn't in `lift.go` yet. That's the RED state.

- [ ] **Step 4: Add the helper + lift guard (GREEN)**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, add `"go/types"` to the import block (alphabetical, after `"go/token"`):

```go
import (
	"fmt"
	"go/token"
	"go/types"
	// ... rest unchanged ...
)
```

Add `isLanesVaryingType` helper just above `liftAlloc` (around line 395):

```go
// isLanesVaryingType reports whether t is a lanes.Varying[T] type. Matches
// both *types.SPMDType (the production representation when GOEXPERIMENT=spmd
// is active and the forked type-checker intercepts lanes.Varying[T]) and the
// raw *types.Named instantiation produced when the standard importer reads
// lanes.Varying[T] without GOEXPERIMENT.
func isLanesVaryingType(typ types.Type) bool {
	if _, ok := typ.(*types.SPMDType); ok {
		return true
	}
	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj.Name() == "Varying" && obj.Pkg() != nil && obj.Pkg().Path() == "lanes" {
			return true
		}
	}
	return false
}
```

In `liftAlloc`, insert the guard as the very first statement of the body (before the `Recover` check):

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
	// SPMD: keep varying allocas memory-backed so the surrounding block's
	// SPMDLaneCount governs the alloca's vector width during TinyGo
	// materialization.
	if ptr, ok := alloc.Type().(*types.Pointer); ok {
		if isLanesVaryingType(ptr.Elem()) {
			return false
		}
	}

	// Don't lift result values in functions that defer
	// calls that may recover from panic.
	if fn := alloc.Parent(); fn.Recover != nil {
		// ... existing body unchanged ...
```

- [ ] **Step 5: Run the test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDVaryingAllocaNotLifted`.

- [ ] **Step 6: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: same pre-existing failures from Task 1; no new ones.

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/lift.go go/ssa/spmd_lift_test.go go/ssa/export_spmd_test.go
```

---

## Task 3: Predication pass — block annotation

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go` (in `spmdConvertLoopOps`)
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go`

The annotation walk that sets `bb.SPMDLaneCount = loop.LaneCount` for every block in each `go for` loop's scope.

- [ ] **Step 1: Create the failing test (TDD RED)**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa_test

import (
	"testing"

	"golang.org/x/tools/go/ssa"
)

// TestSPMDBlockLaneCountSet verifies that the SPMD predication pass
// annotates every in-scope block of an SPMD loop with the loop's
// canonical lane count via *ssa.BasicBlock.SPMDLaneCount.
func TestSPMDBlockLaneCountSet(t *testing.T) {
	pkg := buildSSAWithSPMD(t, accumulateSrc)
	fn := pkg.Func("accumulate")
	if fn == nil {
		t.Fatal("accumulate function not found in SSA")
	}
	if len(fn.SPMDLoops) == 0 {
		t.Fatal("accumulate has no SPMD loops; buildSSAWithSPMD should mark one")
	}
	loop := fn.SPMDLoops[0]
	if loop.LaneCount <= 0 {
		t.Fatalf("loop.LaneCount = %d; expected > 0", loop.LaneCount)
	}

	// At least one block in the function must have SPMDLaneCount = loop.LaneCount.
	var annotated int
	for _, bb := range fn.Blocks {
		if bb.SPMDLaneCount == loop.LaneCount {
			annotated++
		}
	}
	if annotated == 0 {
		t.Fatalf("no blocks have SPMDLaneCount = %d; predication pass did not annotate", loop.LaneCount)
	}
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDBlockLaneCountSet -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `--- FAIL: TestSPMDBlockLaneCountSet` with `no blocks have SPMDLaneCount = N; predication pass did not annotate`.

- [ ] **Step 3: Add the annotation walk (GREEN)**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, locate `spmdConvertLoopOps` (around line 294). Inside the per-loop body, after `liveScopeBlocks` is computed and the `if len(liveScopeBlocks) == 0 { continue }` guard (around line 354), and BEFORE the `if loop.IsPeeled {` branch (around line 356), insert:

```go
		// Annotate every in-scope block with the loop's canonical lane count.
		// TinyGo reads bb.SPMDLaneCount for ALL Varying[T] lane-count
		// derivations inside the block (type materialization, alloca sizing,
		// gather/scatter widths, reduce dispatch, mask widths). The
		// `bb.SPMDLaneCount == 0` guard preserves an outer loop's annotation
		// when an inner loop's predication revisits the same block.
		for b := range liveScopeBlocks {
			if b.SPMDLaneCount == 0 {
				b.SPMDLaneCount = loop.LaneCount
			}
		}
```

- [ ] **Step 4: Run the test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDBlockLaneCountSet -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDBlockLaneCountSet`.

- [ ] **Step 5: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: same pre-existing failures only.

- [ ] **Step 6: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go go/ssa/spmd_block_lanecount_test.go
```

---

## Task 4: Forward-propagation pass

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go` (append test)
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go` (wire the call)

For an alloca declared in an entry block (no SPMD scope) but consumed by SPMD ops in a single loop, set the entry block's `SPMDLaneCount` to the consumer's lane count. This is what makes accumulator-pattern code (`var acc lanes.Varying[float64]` in `main()` above a `go for`) materialize the alloca at the correct width.

- [ ] **Step 1: Append the failing test**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go`:

```go
// TestSPMDForwardPropagationEntryBlock verifies that an entry-block alloca
// consumed by SPMD ops in an annotated block causes the alloca's containing
// block to receive an annotation matching the consumer's lane count.
//
// The fixture: main() declares `var acc lanes.Varying[int]` in its entry
// block, then uses it inside a go for that has SPMDLaneCount > 0. Forward
// propagation should walk the alloca's referrers, find the in-loop
// SPMDStore/SPMDLoad, and annotate main()'s entry block with that loop's
// lane count.
func TestSPMDForwardPropagationEntryBlock(t *testing.T) {
	src := `package main

import (
	"lanes"
	"reduce"
)

var data = []int{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
	var acc lanes.Varying[int]
	for i := range len(data) {
		_ = i
		acc = lanes.Varying[int](data[i])
	}
	_ = reduce.Add(acc)
}
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("main")
	if fn == nil {
		t.Fatal("main function not found in SSA")
	}
	if len(fn.Blocks) == 0 {
		t.Fatal("main has no blocks")
	}
	entry := fn.Blocks[0]
	if entry.SPMDLaneCount == 0 {
		t.Fatalf("entry block SPMDLaneCount = 0; expected forward-propagation to annotate it from the SPMD consumer")
	}
	if len(fn.SPMDLoops) > 0 && entry.SPMDLaneCount != fn.SPMDLoops[0].LaneCount {
		t.Fatalf("entry block SPMDLaneCount = %d; expected %d (loop's lane count)",
			entry.SPMDLaneCount, fn.SPMDLoops[0].LaneCount)
	}
}
```

- [ ] **Step 2: Run the new test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDForwardPropagationEntryBlock -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `--- FAIL: TestSPMDForwardPropagationEntryBlock` with `entry block SPMDLaneCount = 0; ...`.

- [ ] **Step 3: Create the propagation pass**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import "go/types"

// spmdPropagateBlockLaneCount annotates blocks containing varying allocas
// that are consumed by SPMD operations (SPMDLoad/SPMDStore/SPMDIndex/SPMDSelect)
// in already-annotated blocks. This handles the common pattern of an
// accumulator declared in a non-SPMD function's entry block but used
// inside a `go for` loop:
//
//	var acc lanes.Varying[float64]   // entry block, SPMDLaneCount=0
//	go for i, x := range data { ... acc += x }   // loop body, SPMDLaneCount=2
//
// Without this pass, TinyGo would size `acc`'s alloca via the element-natural
// derivation (4 lanes for float64 on WASM128), mismatching the in-loop
// store width (2 lanes). Forward propagation sets the entry block's
// SPMDLaneCount = 2 so TinyGo materializes the alloca correctly.
//
// Limitation: an entry block with two allocas of different consumer lane
// counts gets a single annotation (first SPMD consumer wins). Documented
// in the v4 design spec §6.2.
//
// Runs AFTER spmdConvertLoopOps (so consumer SPMD ops exist) and BEFORE
// spmdSpecializeFunctions (so specialization sees the propagated annotations).
func spmdPropagateBlockLaneCount(fn *Function) {
	for _, b := range fn.Blocks {
		if b.SPMDLaneCount != 0 {
			continue
		}
		// Find varying allocas in this block whose first SPMD consumer is
		// in an annotated block; adopt that consumer's lane count.
		var found int
		for _, instr := range b.Instrs {
			alloc, ok := instr.(*Alloc)
			if !ok {
				continue
			}
			ptr, ok := alloc.Type().(*types.Pointer)
			if !ok {
				continue
			}
			if !isLanesVaryingType(ptr.Elem()) {
				continue
			}
			lc := spmdAllocaConsumerLaneCount(alloc)
			if lc > 0 {
				found = lc
				break
			}
		}
		if found > 0 {
			b.SPMDLaneCount = found
		}
	}
}

// spmdAllocaConsumerLaneCount walks the alloca's referrer chain and returns
// the lane count of the first SPMDLoad/SPMDStore/SPMDIndex/SPMDSelect
// referrer found in an annotated block. Returns 0 if no annotated SPMD
// consumer is found.
func spmdAllocaConsumerLaneCount(alloc *Alloc) int {
	refs := alloc.Referrers()
	if refs == nil {
		return 0
	}
	for _, ref := range *refs {
		switch op := ref.(type) {
		case *SPMDLoad:
			if op.Block() != nil && op.Block().SPMDLaneCount > 0 {
				return op.Block().SPMDLaneCount
			}
		case *SPMDStore:
			if op.Block() != nil && op.Block().SPMDLaneCount > 0 {
				return op.Block().SPMDLaneCount
			}
		case *SPMDIndex:
			if op.Block() != nil && op.Block().SPMDLaneCount > 0 {
				return op.Block().SPMDLaneCount
			}
		case *SPMDSelect:
			if op.Block() != nil && op.Block().SPMDLaneCount > 0 {
				return op.Block().SPMDLaneCount
			}
		}
	}
	return 0
}
```

- [ ] **Step 4: Wire the propagation pass into `func.go`**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go`, locate the orchestration block in `finishBody` (around line 431, after `spmdConvertLoopOps(f)`):

```go
spmdConvertLoopOps(f)
```

Add immediately after:

```go
// SPMD v4: propagate loop-scope lane counts to entry-block allocas
// whose consumers are inside annotated blocks. Runs AFTER
// spmdConvertLoopOps (so SPMDLoad/SPMDStore exist) and BEFORE
// specialization (so specialization sees the propagated annotations).
spmdPropagateBlockLaneCount(f)
```

- [ ] **Step 5: Run the test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDForwardPropagationEntryBlock -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDForwardPropagationEntryBlock`.

- [ ] **Step 6: Run all v4 tests + full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDVaryingAllocaNotLifted|TestSPMDBlockLaneCountSet|TestSPMDForwardPropagationEntryBlock' -v -count=1 -timeout=60s 2>&1 | tail -10
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: 3 PASSes for the v4 tests; same pre-existing failures only.

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_propagate.go go/ssa/spmd_block_lanecount_test.go go/ssa/func.go
```

---

## Task 5: Specialization pass

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_specialize.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go` (append 3 tests)
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go` (wire the call)

This is the largest SSA-side task. Per-call specialization clones an SPMD function's SSA per unique caller lane count, producing variants like `f.spmd2`, `f.spmd4`. Call sites are rewritten to dispatch.

- [ ] **Step 1: Append three failing tests**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go`:

```go
// TestSPMDSpecializationCloneVariants verifies that calling an SPMD function
// from two different lane counts produces two SSA function variants
// (e.g., f.spmd2 and f.spmd4) with each variant's blocks annotated
// to the variant's lane count.
func TestSPMDSpecializationCloneVariants(t *testing.T) {
	src := `package main

import "lanes"

func sum(v lanes.Varying[int]) int {
	var s int
	_ = v
	return s
}

var floats = []float64{1, 2, 3, 4}
var ints = []int32{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
	var a lanes.Varying[int]
	for i := range len(floats) { _ = i; _ = a }
	_ = sum(a)

	var b lanes.Varying[int]
	for i := range len(ints) { _ = i; _ = b }
	_ = sum(b)
}
`
	pkg := buildSSAWithSPMD(t, src)
	// Look for sum.spmdN variants — at least two distinct ones.
	var variants []*ssa.Function
	for name, mem := range pkg.Members {
		if fn, ok := mem.(*ssa.Function); ok {
			if len(name) > len("sum.spmd") && name[:len("sum.spmd")] == "sum.spmd" {
				variants = append(variants, fn)
			}
		}
	}
	if len(variants) < 2 {
		t.Fatalf("expected ≥2 sum.spmd<N> variants, got %d", len(variants))
	}
	// Each variant's blocks should all be annotated with the variant's lane count.
	for _, fn := range variants {
		seen := map[int]bool{}
		for _, bb := range fn.Blocks {
			seen[bb.SPMDLaneCount] = true
		}
		// Allow 0 for blocks that aren't reached from any varying op,
		// but require at least one positive count.
		hasPos := false
		for k := range seen {
			if k > 0 {
				hasPos = true
			}
		}
		if !hasPos {
			t.Errorf("variant %s has no annotated blocks", fn.Name())
		}
	}
}

// TestSPMDSpecializationRewriteCallSites verifies that after specialization
// the call sites in annotated blocks point to the specialized variant,
// not the original function.
func TestSPMDSpecializationRewriteCallSites(t *testing.T) {
	src := `package main

import "lanes"

func sum(v lanes.Varying[int]) int { return 0 }

var floats = []float64{1, 2}

func main() {
	var a lanes.Varying[int]
	for i := range len(floats) { _ = i; _ = a }
	_ = sum(a)
}
`
	pkg := buildSSAWithSPMD(t, src)
	main := pkg.Func("main")
	if main == nil {
		t.Fatal("main not found")
	}
	// Find any Call instruction in main targeting sum or sum.spmdN.
	var found bool
	for _, bb := range main.Blocks {
		for _, instr := range bb.Instrs {
			if call, ok := instr.(*ssa.Call); ok {
				if call.Call.Value != nil {
					name := call.Call.Value.Name()
					if len(name) > len("sum.spmd") && name[:len("sum.spmd")] == "sum.spmd" {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("no call to sum.spmd<N> variant in main; specialization did not rewrite call sites")
	}
}

// TestSPMDSpecializationSingleLaneShortcut verifies that an SPMD function
// called from exactly one lane count is renamed in place (single variant)
// rather than cloned.
func TestSPMDSpecializationSingleLaneShortcut(t *testing.T) {
	src := `package main

import "lanes"

func sum(v lanes.Varying[int]) int { return 0 }

var data = []float64{1, 2, 3, 4}

func main() {
	var a lanes.Varying[int]
	for i := range len(data) { _ = i; _ = a }
	_ = sum(a)
}
`
	pkg := buildSSAWithSPMD(t, src)
	// Count sum and sum.spmdN entries — exactly one should remain.
	var sumCount, variantCount int
	for name := range pkg.Members {
		if name == "sum" {
			sumCount++
		}
		if len(name) > len("sum.spmd") && name[:len("sum.spmd")] == "sum.spmd" {
			variantCount++
		}
	}
	total := sumCount + variantCount
	if total != 1 {
		t.Fatalf("expected exactly 1 of sum / sum.spmdN, got sum=%d variants=%d", sumCount, variantCount)
	}
}
```

- [ ] **Step 2: Run the new tests — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDSpecialization' -v -count=1 -timeout=60s 2>&1 | tail -20
```

Expected: 3 FAILs (specialization pass doesn't exist yet).

- [ ] **Step 3: Create the specialization pass**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_specialize.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"fmt"
	"go/types"
)

// isSPMDFunction reports whether fn has any *types.SPMDType (or
// lanes.Varying[T] *types.Named) parameters or results — i.e., is a
// "SPMD function body" that processes per-lane varying values.
func isSPMDFunction(fn *Function) bool {
	if fn == nil || fn.Signature == nil {
		return false
	}
	sig := fn.Signature
	for i := 0; i < sig.Params().Len(); i++ {
		if isLanesVaryingType(sig.Params().At(i).Type()) {
			return true
		}
	}
	if sig.Results() != nil {
		for i := 0; i < sig.Results().Len(); i++ {
			if isLanesVaryingType(sig.Results().At(i).Type()) {
				return true
			}
		}
	}
	return false
}

// spmdSpecializeFunctions runs after predication + propagation.
// It walks all call sites in the program, finds calls to SPMD functions
// whose containing block has SPMDLaneCount > 0, and creates one specialized
// SSA variant per unique (callee, callerLaneCount) pair. Each variant's
// name is `<callee>.spmd<N>`. Call sites are rewritten to dispatch to the
// right variant.
//
// Single-lane-count callees are renamed in place (no clone) to keep SSA
// dump size manageable.
//
// Iterates until fixed-point: a variant's body may contain calls to other
// SPMD functions that also need specialization at the variant's lane count.
//
// Cap: 10 iterations as a runaway safety net (recursive SPMD function
// chains beyond this are reported as an error).
func spmdSpecializeFunctions(prog *Program) error {
	const maxIters = 10
	for iter := 0; iter < maxIters; iter++ {
		// Phase A: discover specialization requests.
		// requests[callee] = set of caller lane counts.
		requests := map[*Function]map[int]bool{}
		for _, pkg := range prog.packages {
			for _, mem := range pkg.Members {
				fn, ok := mem.(*Function)
				if !ok {
					continue
				}
				spmdGatherSpecializationRequests(fn, requests)
			}
		}
		if len(requests) == 0 {
			return nil
		}

		// Phase B: materialize variants for callees whose request set isn't yet a singleton renaming.
		anyChange := false
		for callee, lcSet := range requests {
			lcs := make([]int, 0, len(lcSet))
			for lc := range lcSet {
				lcs = append(lcs, lc)
			}
			if len(lcs) == 1 {
				// Single-lane shortcut: rename in place (don't clone).
				lc := lcs[0]
				newName := fmt.Sprintf("%s.spmd%d", callee.Name(), lc)
				if callee.Name() == newName {
					continue // already specialized
				}
				oldName := callee.Name()
				callee.object.SetName(newName)
				// Annotate every block of the function with this lane count.
				for _, bb := range callee.Blocks {
					if bb.SPMDLaneCount == 0 {
						bb.SPMDLaneCount = lc
					}
				}
				// Update package Members map.
				if pkg := callee.Pkg; pkg != nil {
					if _, ok := pkg.Members[oldName]; ok {
						delete(pkg.Members, oldName)
						pkg.Members[newName] = callee
					}
				}
				// Rewrite call sites.
				spmdRewriteCallSites(prog, callee, lc, callee)
				anyChange = true
				continue
			}
			// Multiple lane counts: clone per variant.
			for _, lc := range lcs {
				variantName := fmt.Sprintf("%s.spmd%d", callee.Name(), lc)
				if callee.Pkg != nil {
					if _, exists := callee.Pkg.Members[variantName]; exists {
						continue // already specialized for this lane count
					}
				}
				variant := spmdCloneFunction(callee, variantName, lc)
				if callee.Pkg != nil {
					callee.Pkg.Members[variantName] = variant
				}
				spmdRewriteCallSites(prog, callee, lc, variant)
				anyChange = true
			}
			// After all variants exist, the original `callee` is unreferenced
			// from annotated blocks. Leave it in place for non-SPMD callers
			// (if any); a future pass could remove orphaned originals.
		}
		if !anyChange {
			return nil
		}
	}
	return fmt.Errorf("spmdSpecializeFunctions: did not reach fixed point in %d iterations (recursive SPMD specialization?)", maxIters)
}

// spmdGatherSpecializationRequests walks fn.Blocks for Call instructions
// targeting SPMD functions, recording (callee, callerLaneCount) requests
// in the shared map.
func spmdGatherSpecializationRequests(fn *Function, requests map[*Function]map[int]bool) {
	for _, bb := range fn.Blocks {
		if bb.SPMDLaneCount == 0 {
			continue
		}
		lc := bb.SPMDLaneCount
		for _, instr := range bb.Instrs {
			call, ok := instr.(*Call)
			if !ok {
				continue
			}
			callee, ok := call.Call.Value.(*Function)
			if !ok {
				continue
			}
			if !isSPMDFunction(callee) {
				continue
			}
			// Skip already-specialized variants (name has .spmdN suffix matching lc).
			expectedName := fmt.Sprintf("%s.spmd%d", strippedSPMDName(callee.Name()), lc)
			if callee.Name() == expectedName {
				continue
			}
			if requests[callee] == nil {
				requests[callee] = map[int]bool{}
			}
			requests[callee][lc] = true
		}
	}
}

// strippedSPMDName returns the original function name without a .spmdN
// suffix (if present). Used to dedupe specialization requests.
func strippedSPMDName(name string) string {
	idx := -1
	for i := 0; i < len(name)-len(".spmd"); i++ {
		if name[i:i+len(".spmd")] == ".spmd" {
			// Check the rest is digits.
			isNum := true
			for _, c := range name[i+len(".spmd"):] {
				if c < '0' || c > '9' {
					isNum = false
					break
				}
			}
			if isNum {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return name
	}
	return name[:idx]
}

// spmdCloneFunction produces a deep-cloned Function variant with all blocks
// annotated to laneCount. Block predecessor/successor pointers, Phi edges,
// and instruction operand pointers are rewritten to refer to the cloned
// values.
func spmdCloneFunction(orig *Function, newName string, laneCount int) *Function {
	clone := &Function{
		Pkg:       orig.Pkg,
		Prog:      orig.Prog,
		Synthetic: "spmd-specialized variant of " + orig.Name(),
		Signature: orig.Signature,
		object:    orig.object, // share the types.Object; rename via object.SetName()? actually create a fresh
	}
	clone.object = nil // cloned function uses its name directly via Name() override

	// We can't easily clone *types.Func, so we expose the name via an
	// internal name field and Name() method override. ssa.Function already
	// supports this: setting fn.name and using setObject() workaround.
	// For simplicity in this implementation, store the name in a field and
	// override the Name() lookup via a helper map maintained by the
	// specialization pass — see spmd_specialize_naming.go for details.
	//
	// In practice, examine x-tools-spmd's existing function-cloning
	// machinery (e.g., the generic instantiation path) and reuse it. The
	// implementer should investigate `func.go` and `instantiate.go` for
	// existing clone primitives.
	clone.setNameForSpecialization(newName)

	// Clone basic blocks.
	clone.Blocks = make([]*BasicBlock, len(orig.Blocks))
	blockMap := make(map[*BasicBlock]*BasicBlock, len(orig.Blocks))
	for i, ob := range orig.Blocks {
		nb := &BasicBlock{
			Index:         ob.Index,
			Comment:       ob.Comment,
			parent:        clone,
			SPMDLaneCount: laneCount,
		}
		clone.Blocks[i] = nb
		blockMap[ob] = nb
	}
	for i, ob := range orig.Blocks {
		nb := clone.Blocks[i]
		nb.Preds = make([]*BasicBlock, len(ob.Preds))
		for j, p := range ob.Preds {
			nb.Preds[j] = blockMap[p]
		}
		nb.Succs = make([]*BasicBlock, len(ob.Succs))
		for j, s := range ob.Succs {
			nb.Succs[j] = blockMap[s]
		}
	}

	// Clone instructions and rewrite operand pointers.
	// (Use existing cloneFunction primitives from func.go where possible;
	// see implementation note above. This sketch shows intent — the
	// implementer must use real x-tools-spmd cloning machinery.)
	cloneFunctionInstructions(orig, clone, blockMap)

	clone.Params = make([]*Parameter, len(orig.Params))
	for i, p := range orig.Params {
		clone.Params[i] = &Parameter{
			name:   p.name,
			object: p.object,
			typ:    p.typ,
			parent: clone,
		}
	}

	return clone
}

// spmdRewriteCallSites walks all calls in the program; any Call whose target
// is `oldCallee` and whose containing block has SPMDLaneCount = matchLC
// is rewritten to target `newCallee`.
func spmdRewriteCallSites(prog *Program, oldCallee *Function, matchLC int, newCallee *Function) {
	for _, pkg := range prog.packages {
		for _, mem := range pkg.Members {
			fn, ok := mem.(*Function)
			if !ok {
				continue
			}
			for _, bb := range fn.Blocks {
				if bb.SPMDLaneCount != matchLC {
					continue
				}
				for _, instr := range bb.Instrs {
					call, ok := instr.(*Call)
					if !ok {
						continue
					}
					if call.Call.Value == oldCallee {
						call.Call.Value = newCallee
					}
				}
			}
		}
	}
}
```

NOTE: The `setNameForSpecialization` and `cloneFunctionInstructions` helpers are sketched above but use mechanisms specific to x-tools-spmd. The implementer should:
1. Locate any existing function-cloning code in `x-tools-spmd/go/ssa/func.go` or `instantiate.go` and reuse it.
2. For naming, look at how generic instantiation handles names (`fn.object` may need to be cloned via `types.NewFunc`).
3. Treat the helpers in this file as a high-level recipe; adapt to the actual primitives.

If function cloning is too complex to reimplement, fall back to a simpler design: since the issue is only about lane count, instead of cloning, run a separate emission pass per lane count. (The fallback is the "single SSA, emission-context lane count" Approach 2B from the spec brainstorming — explicitly a backup if the clone-per-variant approach proves intractable in x-tools-spmd's current architecture.)

- [ ] **Step 4: Wire the specialization pass into `func.go`**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go`, locate where `Build()` walks all packages (likely in `x-tools-spmd/go/ssa/builder.go`'s `Build` or similar). Specialization runs ONCE per program after all functions are finished:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
grep -n "func .*Program.*Build\|func .*Package.*Build" go/ssa/*.go | head -5
```

Add a call to `spmdSpecializeFunctions(prog)` at the end of the program-level Build, after all functions have run their `finishBody`.

- [ ] **Step 5: Run the specialization tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDSpecialization' -v -count=1 -timeout=60s 2>&1 | tail -15
```

Expected: 3 PASSes.

- [ ] **Step 6: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: same pre-existing failures only.

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_specialize.go go/ssa/spmd_block_lanecount_test.go go/ssa/func.go go/ssa/builder.go
```

(Adjust `git add` paths to match the actual files modified per Step 4's investigation.)

- [ ] **Step 8: Commit Tasks 1-5 (clean-commit pipeline)**

This is the SSA-side batch. Tasks 1-5 form one logical x-tools-spmd commit.

Suggested message:

```
feat: SPMD canonical lane count via block annotation + per-call specialization

Introduces *ssa.BasicBlock.SPMDLaneCount as the canonical source of
truth for Varying[T] lane counts. Three SSA passes populate it:

- spmdConvertLoopOps (extended) annotates every in-scope block of a
  `go for` loop with loop.LaneCount.
- spmdPropagateBlockLaneCount (new) propagates annotations onto entry
  blocks of non-SPMD functions whose allocas feed in-loop SPMD ops.
- spmdSpecializeFunctions (new) clones SSA per (function, lane-count)
  pair for SPMD functions called from multiple lane-count contexts,
  producing variants like sum.spmd2, sum.spmd4. Call sites are
  rewritten to dispatch to the correct variant.

Restores the lift guard from v3 so varying allocas survive lift() and
their containing block's annotation governs TinyGo materialization.

Tests: TestSPMDVaryingAllocaNotLifted, TestSPMDBlockLaneCountSet,
TestSPMDForwardPropagationEntryBlock, TestSPMDSpecializationCloneVariants,
TestSPMDSpecializationRewriteCallSites,
TestSPMDSpecializationSingleLaneShortcut.
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

---

## Task 6: TinyGo builder field + block-entry hook

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` (builder struct + block-entry site)

Adds `b.spmdActiveLaneCount` to the builder and hooks block iteration to set it from `bb.SPMDLaneCount`. No test in this task — the tests for block-aware materialization come in Tasks 7+ where consumers use the field.

- [ ] **Step 1: Rebuild TinyGo with the new x-tools-spmd**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
```

Expected: clean build. Confirms x-tools-spmd Tasks 1-5 are in place and TinyGo can compile against the new BasicBlock field.

- [ ] **Step 2: Add the builder field**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, locate the `builder` struct definition (around line 197 has `spmdFuncMinLaneCount`; the struct itself is earlier — find via `grep -n "^type builder" compiler/compiler.go`). Add a new field near `spmdFuncMinLaneCount`:

```go
// spmdActiveLaneCount is the canonical lane count for the basic block
// currently being emitted, mirroring (currentBlock).SPMDLaneCount for
// fast access during type materialization, alloca sizing, and other
// derivation paths inside SPMD scope. 0 means non-SPMD context (use
// existing element-natural / function-min derivations).
spmdActiveLaneCount int
```

- [ ] **Step 3: Locate the block-entry site**

```bash
cd /home/cedric/work/SPMD/tinygo
grep -n "for _, block := range\|fn.DomPreorder\|b.SetInsertPointAtEnd" compiler/compiler.go compiler/func.go | head -10
```

Identify the function-emission block-iteration loop (likely something like `for _, block := range fn.DomPreorder() {` or inside `createFunctionBody`).

- [ ] **Step 4: Add the block-entry hook**

At the top of the per-block body inside the iteration loop (before any instruction emission), set `b.spmdActiveLaneCount` from the block's annotation. Example pattern:

```go
for _, block := range fn.DomPreorder() {
	b.SetInsertPointAtEnd(b.blockEntries[block])
	// SPMD v4: set the block-level canonical lane count so type
	// materialization and other derivation paths inside this block
	// see the right width.
	prevLC := b.spmdActiveLaneCount
	b.spmdActiveLaneCount = block.SPMDLaneCount

	// ... existing instruction emission for this block ...

	b.spmdActiveLaneCount = prevLC
}
```

The save/restore pattern handles future reentrancy. If TinyGo's emission is currently single-pass-no-restore-needed, the simpler pattern works:

```go
for _, block := range fn.DomPreorder() {
	b.SetInsertPointAtEnd(b.blockEntries[block])
	b.spmdActiveLaneCount = block.SPMDLaneCount
	// ... existing emission ...
}
```

- [ ] **Step 5: Initialize at function entry**

When TinyGo starts emitting a function, before walking blocks, also handle the entry case (set `spmdActiveLaneCount` to `fn.Blocks[0].SPMDLaneCount` if you initialize the builder before the loop).

- [ ] **Step 6: Build the package**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
```

Expected: clean build. Field is unused so far (only set, not read), but compilation succeeds.

- [ ] **Step 7: Spot-check existing tests still pass**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-count=1 -timeout=10m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- FAIL' | sort
```

Expected pre-existing failures only: `TestSPMDVaryingPointerFieldAddr_Contiguous` (TDD RED for Task 8 elsewhere), `TestCompiler` (golden file mismatch). No new regressions.

- [ ] **Step 8: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go
```

(If the block-entry site is in a different file, e.g., `compiler/func.go`, stage that too.)

---

## Task 7: TinyGo `getLLVMType` for SPMDType — block-aware

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (`spmdEffectiveLaneCount` and `getLLVMType` callers for SPMDType)

The first reader of `b.spmdActiveLaneCount`. When the current block has an annotation, type materialization for `*types.SPMDType` uses the block's lane count instead of the element-natural derivation.

- [ ] **Step 1: Find the SPMDType type-materialization site**

```bash
cd /home/cedric/work/SPMD/tinygo
grep -n "SPMDType\|spmdEffectiveLaneCount\|VectorType.*spmd" compiler/spmd.go compiler/compiler.go | grep -v "_test.go" | head -20
```

Locate where `*types.SPMDType` becomes an LLVM vector type. Likely inside `getLLVMType` (in `compiler.go`) which calls `c.spmdEffectiveLaneCount(spmdType, elemLLVM)`.

- [ ] **Step 2: Modify the materialization**

Two-step: split the existing `compilerContext`-method into a `builder`-aware variant. In `compiler/spmd.go`:

```go
// spmdVaryingLLVMType returns the LLVM vector type for a *types.SPMDType
// using the builder's current block-level lane count when set, falling back
// to the element-natural derivation otherwise. Use this instead of
// b.getLLVMType(spmdType) inside SPMD codegen paths.
func (b *builder) spmdVaryingLLVMType(spmdType *types.SPMDType) llvm.Type {
	elemLLVM := b.getLLVMType(spmdType.Elem())
	var lc int
	if b.spmdActiveLaneCount > 0 {
		lc = b.spmdActiveLaneCount
	} else {
		lc = b.spmdEffectiveLaneCount(spmdType, elemLLVM)
	}
	if lc <= 1 {
		return elemLLVM // scalar fallback
	}
	return llvm.VectorType(elemLLVM, lc)
}
```

In `compiler.go`'s `getLLVMType`, when handling `*types.SPMDType`, prefer the builder method when a builder is in scope. Many call sites pass through `c.getLLVMType` (compilerContext) where there's no builder — those keep the existing derivation.

The cleanest pattern (not invasive): when emitting code from a builder, replace direct `b.getLLVMType(varyingType)` calls with `b.spmdVaryingLLVMType(varyingType)` for SPMDType-producing call sites. Use `grep` to find them (next step).

- [ ] **Step 3: Replace SPMDType materialization sites in builder context**

```bash
cd /home/cedric/work/SPMD/tinygo
grep -nE "b\.getLLVMType\(.*[Ss][Pp][Mm][Dd]" compiler/*.go | head -20
```

Each match where `b.getLLVMType(...)` resolves to a `*types.SPMDType` (likely many in `spmd.go`) is a candidate to switch to `b.spmdVaryingLLVMType`. Be conservative: only replace when:
- The call is inside an SPMD codegen path (function body of a builder that has SPMD context)
- The result is the LLVM type of a varying value being constructed in this block

For each replacement, verify the path makes sense by inspecting the surrounding code.

- [ ] **Step 4: Build and spot-check**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
cd tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-count=1 -timeout=10m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: clean build, no NEW test failures vs Task 6.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/compiler.go
```

---

## Task 8: TinyGo `*ssa.Alloc` materialization — block-aware

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` (`*ssa.Alloc` case in `createExpr` ~line 2745)
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler_test.go` (`MaxStackAlloc` propagation)

The `*ssa.Alloc` case reads the alloca's containing block's `SPMDLaneCount` and sizes the alloca's element accordingly. Mirrors v3-attempt-1's logic but reads from BLOCK rather than the alloca's per-instruction field.

- [ ] **Step 1: Patch the Alloc case**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, locate the `*ssa.Alloc` case in `createExpr` (around line 2745). Replace:

```go
case *ssa.Alloc:
	typ := b.getLLVMType(expr.Type().Underlying().(*types.Pointer).Elem())
	size := b.targetData.TypeAllocSize(typ)
	// ... rest unchanged ...
```

with:

```go
case *ssa.Alloc:
	elemType := expr.Type().Underlying().(*types.Pointer).Elem()

	// SPMD v4: when the alloca's element is varying and the alloca's
	// containing block carries an SPMDLaneCount, materialize the element
	// as a vector at that width. Block annotation is the canonical source.
	// See *ssa.BasicBlock.SPMDLaneCount and the v4 design spec.
	var typ llvm.Type
	if blockLC := expr.Block().SPMDLaneCount; blockLC > 0 {
		if spmdElem, ok := elemType.(*types.SPMDType); ok {
			typ = llvm.VectorType(b.getLLVMType(spmdElem.Elem()), blockLC)
		}
	}
	if typ.IsNil() {
		typ = b.getLLVMType(elemType)
	}
	size := b.targetData.TypeAllocSize(typ)
	// ... rest of the case body unchanged ...
```

- [ ] **Step 2: Propagate `MaxStackAlloc` in test config**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler_test.go`, locate `testCompilePackage` (around line 235). Add `MaxStackAlloc` to the `compilerConfig` literal:

```go
AutomaticStackSize: config.AutomaticStackSize(),
DefaultStackSize:   config.StackSize(),
NeedsStackObjects:  config.NeedsStackObjects(),
// MaxStackAlloc is propagated so that alloca vs heap decisions match
// the real compiler. Without it (0 default), every non-zero alloca
// goes to the heap, masking alloca instructions from IR pattern checks
// (e.g., TestSPMDVaryingAllocaLLVMType checks for `alloca <2 x i32>`).
MaxStackAlloc: config.MaxStackAlloc(),
```

- [ ] **Step 3: Build and spot-check**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
cd tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-count=1 -timeout=10m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- FAIL' | sort
```

Expected: same pre-existing failures only.

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/compiler_test.go
```

---

## Task 9: TinyGo lane-count derivation site audit

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (~14 derivation sites)
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/func.go` (~2 sites)
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` (a few non-Alloc sites)

The hardest task. Each call to `spmdLaneCount(elemType)` or `spmdMinLaneCountForSig(sig)` in TinyGo needs review: should it use `b.spmdActiveLaneCount` instead?

- [ ] **Step 1: Enumerate all sites**

```bash
cd /home/cedric/work/SPMD/tinygo
grep -nE "spmdLaneCount\(|spmdMinLaneCountForSig\(" compiler/*.go | grep -v "_test.go" > /tmp/v4_lc_sites.txt
cat /tmp/v4_lc_sites.txt
```

Expected ~21 lines. Each is a candidate site.

- [ ] **Step 2: Categorize each site**

For each site, note:
- File:line
- The surrounding function name
- Whether the call is inside an SPMD codegen path (function uses `*types.SPMDType` or operates on a block in SPMD scope)
- Recommendation: **REPLACE** (use `b.spmdActiveLaneCount` when > 0) or **KEEP** (non-SPMD context, e.g., function-signature emission, global vars)

This step is investigation; no code change. Save the categorization to a file or as inline comments while reviewing each match.

Heuristic: if the function takes a `b *builder`, it's in builder scope and a candidate for REPLACE. If it takes only `c *compilerContext`, it's likely module-level and KEEP.

- [ ] **Step 3: Apply REPLACE pattern site-by-site**

For each site marked REPLACE, change:

```go
laneCount := b.spmdLaneCount(elemType)
```

to:

```go
var laneCount int
if b.spmdActiveLaneCount > 0 {
	laneCount = b.spmdActiveLaneCount
} else {
	laneCount = b.spmdLaneCount(elemType)
}
```

Or, for a more compact form, introduce a helper:

```go
// In compiler/spmd.go:
// spmdContextLaneCount returns the surrounding block's annotated lane
// count when set, else the element-natural lane count. Use this in any
// SPMD codegen path that needs the canonical lane count for a Varying[T]
// value.
func (b *builder) spmdContextLaneCount(elemType llvm.Type) int {
	if b.spmdActiveLaneCount > 0 {
		return b.spmdActiveLaneCount
	}
	return b.spmdLaneCount(elemType)
}
```

Then each replacement becomes:

```go
laneCount := b.spmdContextLaneCount(elemType)
```

- [ ] **Step 4: Build incrementally and spot-check**

After every 3-5 REPLACE applications, rebuild and run a small test:

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
cd tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-run 'TestSPMD' -count=1 -timeout=10m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- FAIL' | sort
```

If any new SPMD test fails, the REPLACE was wrong; revert that one and continue.

- [ ] **Step 5: Final full test sweep**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-count=1 -timeout=15m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- FAIL' | sort
```

Expected pre-existing failures only (`TestSPMDVaryingPointerFieldAddr_Contiguous`, `TestCompiler`).

- [ ] **Step 6: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/func.go compiler/compiler.go
```

---

## Task 10: TinyGo IR tests + commit (Tasks 6-10 batch)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Restored v3 IR tests (alloca + load width consistency) plus a new specialization test.

- [ ] **Step 1: Append three tests**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (after other test functions, before helpers around line 737):

```go
// TestSPMDVaryingAllocaLLVMType verifies that a Varying[int] alloca inside
// a go-for loop iterating over []float64 uses the loop's lane width
// (2 on WASM SIMD128) rather than int's register-natural width (4).
// All loads from the alloca must also be at <2 x i32> width — without
// that, the IR has out-of-bounds reads from an 8-byte stack slot.
func TestSPMDVaryingAllocaLLVMType(t *testing.T) {
	src := `package main

import (
	"lanes"
	"reduce"
)

var data = []float64{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
	var acc lanes.Varying[int]
	go for i, _ := range data {
		acc += i
	}
	_ = reduce.Add(acc)
}
`
	ir := compileSPMDSource(t, src)
	mustContain(t, ir, "alloca <2 x i32>")
	mustContain(t, ir, "load <2 x i32>, ptr %acc")
	mustNotContain(t, ir, "load <4 x i32>, ptr %acc")
	mustNotContain(t, ir, "masked.store.v4i32")
}

// TestSPMDVaryingLocalMaskedInTail is a regression smoke check that the IR
// for a varying-local accumulator inside a partial-mask go-for still
// contains masked memory ops. Loose match — guards against wholesale loss
// of partial-mask machinery; the precise alloca lane width is asserted in
// TestSPMDVaryingAllocaLLVMType.
func TestSPMDVaryingLocalMaskedInTail(t *testing.T) {
	src := `package main

import (
	"lanes"
	"reduce"
)

var data = []float64{1, 2, 3, 4, 5}

func main() {
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += x
	}
	_ = reduce.Add(acc)
}
`
	ir := compileSPMDSource(t, src)
	mustContainAny(t, ir, "masked.store", "select <4 x i1>", "select <2 x i1>")
}

// TestSPMDSpecializedVariantEmitted verifies that calling an SPMD function
// from two different lane-count contexts produces two LLVM functions
// (e.g., sum.spmd2 and sum.spmd4), each with the appropriate vector width.
func TestSPMDSpecializedVariantEmitted(t *testing.T) {
	src := `package main

import (
	"lanes"
	"reduce"
)

func sum(v lanes.Varying[int]) int { return reduce.Add(v) }

var floats = []float64{1, 2, 3, 4}
var ints = []int32{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
	var a lanes.Varying[int]
	go for _, x := range floats { a += int(x) }
	_ = sum(a)

	var b lanes.Varying[int]
	go for _, x := range ints { b += int(x) }
	_ = sum(b)
}
`
	ir := compileSPMDSource(t, src)
	mustContainAny(t, ir, "@sum.spmd2", "define internal i32 @sum.spmd2")
	mustContainAny(t, ir, "@sum.spmd4", "define internal i32 @sum.spmd4")
}
```

- [ ] **Step 2: Run the three new tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-run 'TestSPMDVaryingAllocaLLVMType|TestSPMDVaryingLocalMaskedInTail|TestSPMDSpecializedVariantEmitted' -v -count=1" GOTESTPKGS="./compiler" 2>&1 | tail -15
```

Expected: 3 PASSes. (If the specialization variant names differ from the assertion — e.g., LLVM mangles `.` differently — adjust the assertion to match the actual variant name format.)

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

- [ ] **Step 4: Commit Tasks 6-10 (clean-commit pipeline)**

This is the TinyGo-side batch. Suggested message:

```
feat: respect block-level SPMDLaneCount for Varying[T] materialization

Adds b.spmdActiveLaneCount to the builder, populated from the current
basic block's SPMDLaneCount during emission. Type materialization
(spmdVaryingLLVMType), alloca sizing (*ssa.Alloc case), and ~14 other
lane-count derivation sites in compiler/spmd.go consult this field
when > 0, falling back to the element-natural / function-min derivation
otherwise.

This makes the loop's canonical lane count win over int's
register-natural width inside `go for []float64` loops with Varying[int]
accumulators — the specific mismatch that produced NaN in n-body and
broke 23 tests at the v3 attempt 1 GATE.

Adds TestSPMDVaryingAllocaLLVMType (alloca + load consistency at <2 x i32>),
TestSPMDVaryingLocalMaskedInTail (regression smoke), and
TestSPMDSpecializedVariantEmitted (two SPMD function variants emitted
from two-lane-count callers).

Test infrastructure: propagates MaxStackAlloc from real config to the
test compile config so SPMD allocas stay on stack and are observable
by IR pattern checks.
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

---

## Task 11: Specialization correctness E2E test

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/dual-width-spmd-func/main.go`
- Modify: `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh` (register the new test)

End-to-end test that the specialized variants produce correct results when called from different lane-count contexts.

- [ ] **Step 1: Create the test program**

Create `/home/cedric/work/SPMD/test/integration/spmd/dual-width-spmd-func/main.go`:

```go
package main

import (
	"lanes"
	"reduce"
)

// sum is an SPMD function called from two different lane-count contexts.
// v4 specialization should produce sum.spmd2 (called from float64 loop)
// and sum.spmd4 (called from int32 loop) variants.
func sum(v lanes.Varying[int]) int {
	return reduce.Add(v)
}

var floats = []float64{1, 2, 3, 4, 5, 6, 7, 8}
var ints = []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func main() {
	var a lanes.Varying[int]
	go for _, x := range floats {
		a += int(x)
	}
	println(sum(a))

	var b lanes.Varying[int]
	go for _, x := range ints {
		b += int(x)
	}
	println(sum(b))
}
```

Expected output: `36` then `136` (sums of 1+2+...+8 and 1+2+...+16).

- [ ] **Step 2: Register the test**

Find how other integration tests are registered in `/home/cedric/work/SPMD/test/e2e/spmd-e2e-test.sh`:

```bash
cd /home/cedric/work/SPMD
grep -n "test_compile_and_run\|integ_" test/e2e/spmd-e2e-test.sh | head -20
```

Add a new entry following the existing pattern, e.g.:

```bash
test_compile_and_run "integ_dual-width-spmd-func" "test/integration/spmd/dual-width-spmd-func/main.go" "36
136"
```

(Adjust syntax to match the script's existing pattern — there's likely a function or table the implementer should follow.)

- [ ] **Step 3: Run the new test**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep "dual-width-spmd-func"
```

Expected: `PASS  integ_dual-width-spmd-func` (or the script's success marker).

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/dual-width-spmd-func/main.go test/e2e/spmd-e2e-test.sh
```

(Note: this stage is in the parent SPMD repo, not a submodule.)

---

## Task 12: Regression sweep — GATE

**Files:** None (verification only).

The explicit go/no-go gate. v1, v2, v3 all regressed tests at this point. v4's design specifically addresses the cascading lane-count mismatch by making the block annotation canonical for ALL derivations; this gate confirms the design works.

- [ ] **Step 1: Rebuild full toolchain**

```bash
cd /home/cedric/work/SPMD
make build 2>&1 | tail -5
```

- [ ] **Step 2: Run E2E sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after-v4.txt 2>&1
tail -10 /tmp/e2e-after-v4.txt
```

- [ ] **Step 3: Compare against baseline**

```bash
diff <(grep -E "Compile pass|Compile fail|Run pass|Run fail|Reject pass" /tmp/e2e-baseline-v4.txt) \
     <(grep -E "Compile pass|Compile fail|Run pass|Run fail|Reject pass" /tmp/e2e-after-v4.txt)
```

Expected: identical totals, OR `Compile pass`/`Run pass` increased by 1 (because of the new `dual-width-spmd-func` test). Any new fail entry → STOP.

```bash
diff <(grep -E "(COMPILE FAIL|RUN FAIL|DUAL FAIL)" /tmp/e2e-baseline-v4.txt | sort) \
     <(grep -E "(COMPILE FAIL|RUN FAIL|DUAL FAIL)" /tmp/e2e-after-v4.txt | sort)
```

Expected: empty diff. Any `> COMPILE FAIL ...` line indicates a regression.

**GATE:**
- If empty diff (or only PASS additions for the new dual-width test) → proceed.
- If ANY new FAIL → STOP. Investigate before proceeding. The hypothesis is wrong; revert and iterate.

- [ ] **Step 4: Run benchmark sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after-v4.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-after-v4.txt | head
```

Compare against `/tmp/bench-baseline-v4.txt`. Acceptance: each ratio within ±10% of baseline.

If a benchmark regresses by >10%: note it; don't revert (correctness > performance for this fix). Document for follow-up.

---

## Task 13: Re-enable n-body-nosqrt and n-body

**Files:**
- Modify (or revert): `tinybench/n-body-nosqrt/go-spmd/main.go`, `tinybench/n-body/go-spmd/main.go` if their bodies were disabled
- Delete: `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md`, `tinybench/n-body/go-spmd/BLOCKER.md`
- Modify: `tinybench/BLOCKERS.md`

- [ ] **Step 1: Restore n-body-nosqrt**

```bash
cd /home/cedric/work/SPMD/tinybench
ls n-body-nosqrt/go-spmd/
cat n-body-nosqrt/go-spmd/BLOCKER.md 2>/dev/null | head -5
```

If `main.go` body is disabled (panic/empty), restore from git history (`git log --oneline -- n-body-nosqrt/go-spmd/main.go`).

- [ ] **Step 2: Build + diff vs scalar**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -3
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. Reference output starts `-0.169075164` and ends `-0.169078071`.

- [ ] **Step 3: Restore n-body**

Repeat for n-body:

```bash
cd /home/cedric/work/SPMD/tinybench
ls n-body/go-spmd/
cat n-body/go-spmd/BLOCKER.md 2>/dev/null | head -5
```

Restore `main.go` body from git if disabled. Build:

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go 2>&1 | tail -3
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff.

- [ ] **Step 4: Delete BLOCKER files + update BLOCKERS.md**

```bash
cd /home/cedric/work/SPMD/tinybench
rm -f n-body-nosqrt/go-spmd/BLOCKER.md n-body/go-spmd/BLOCKER.md
```

Edit `tinybench/BLOCKERS.md` to remove both entries. If the file becomes empty, replace contents with: `# Tinybench SPMD ports — current blockers\n\nNone as of 2026-04-26.` or `git rm` the file (project convention).

- [ ] **Step 5: Stage + commit (clean-commit pipeline)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body-nosqrt/go-spmd/main.go n-body/go-spmd/main.go BLOCKERS.md
git rm --quiet n-body-nosqrt/go-spmd/BLOCKER.md n-body/go-spmd/BLOCKER.md 2>/dev/null || true
```

Suggested commit:

```
feat: re-enable n-body and n-body-nosqrt SPMD ports

Both ports were blocked on the varying-local NaN-from-unmasked-writeback
bug. v4's canonical lane count fix (block annotation + per-call
specialization, landed across x-tools-spmd and tinygo) makes the
Varying[float64] accumulator alloca size correctly to the loop's
lane count, eliminating the inactive-lane NaN leak.

Output now byte-identical to the scalar Go reference:
  n-body-nosqrt/50000: -0.169075164 / -0.169078071
  n-body/50000:        (matching scalar reference)
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

- [ ] **Step 6: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nbns-go-bin /tmp/nb-spmd-bin /tmp/nb-go-bin
```

---

## Task 14: Parent SPMD submodule pointer bumps + final verification

**Files:**
- Modify: parent SPMD repo's submodule pointers for `x-tools-spmd`, `tinygo`, `tinybench`

- [ ] **Step 1: Verify each submodule has the expected commits**

```bash
cd /home/cedric/work/SPMD
for sub in x-tools-spmd tinygo tinybench; do
    echo "=== $sub ==="
    (cd $sub && git log --oneline -3)
done
```

Expected: each submodule's tip is the commit from Tasks 5 / 10 / 13.

- [ ] **Step 2: Stage submodule pointer updates + the in-tree integration test**

```bash
cd /home/cedric/work/SPMD
git add x-tools-spmd tinygo tinybench
git status --short
```

Expected: 3 modified submodule pointers + the `test/integration/spmd/dual-width-spmd-func/main.go` and `test/e2e/spmd-e2e-test.sh` from Task 11.

- [ ] **Step 3: Final E2E verification at parent repo**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-final-v4.txt 2>&1
tail -10 /tmp/e2e-final-v4.txt
```

Expected: same totals as `/tmp/e2e-after-v4.txt` (no regression after submodule pointer bump). n-body / n-body-nosqrt no longer in any failure category.

- [ ] **Step 4: Commit (clean-commit pipeline)**

Suggested commit:

```
deps: land SPMD canonical lane count v4 across toolchain

x-tools-spmd: introduce *ssa.BasicBlock.SPMDLaneCount with three
populating passes (predication, propagation, specialization).
tinygo: respect the block annotation for Varying[T] materialization,
alloca sizing, and ~14 other lane-count derivation sites.
tinybench: re-enable n-body and n-body-nosqrt SPMD ports.

Test additions: new integration test dual-width-spmd-func verifies
per-call specialization produces correct results when an SPMD
function is called from two different lane-count contexts.

Spec: docs/superpowers/specs/2026-04-26-spmd-canonical-lane-count-v4-design.md
Plan: docs/superpowers/plans/2026-04-26-spmd-canonical-lane-count-v4.md

Supersedes the v1, v2, v3 attempts (all reverted).
```

- [ ] **Step 5: Cleanup**

```bash
rm -f /tmp/e2e-baseline-v4.txt /tmp/e2e-after-v4.txt /tmp/e2e-final-v4.txt \
      /tmp/bench-baseline-v4.txt /tmp/bench-after-v4.txt /tmp/v4_lc_sites.txt
```

---

## Done

All success criteria from spec §5.4 are met when:

- `TestSPMDVaryingAllocaNotLifted`, `TestSPMDBlockLaneCountSet`, `TestSPMDForwardPropagationEntryBlock`, `TestSPMDSpecializationCloneVariants`, `TestSPMDSpecializationRewriteCallSites`, `TestSPMDSpecializationSingleLaneShortcut` (x-tools-spmd): PASS.
- `TestSPMDVaryingAllocaLLVMType`, `TestSPMDVaryingLocalMaskedInTail`, `TestSPMDSpecializedVariantEmitted` (tinygo): PASS.
- `test/e2e/spmd-e2e-test.sh`: zero new failures vs baseline; ideally `dual-width-spmd-func` adds one new pass entry.
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference.
- All BLOCKER.md files deleted; BLOCKERS.md updated/deleted.
- Parent SPMD submodule pointers bumped to new revisions.

Update `MEMORY.md` per CLAUDE.md auto-memory: record the v4 fix (canonical lane count via block annotation + per-call specialization).
