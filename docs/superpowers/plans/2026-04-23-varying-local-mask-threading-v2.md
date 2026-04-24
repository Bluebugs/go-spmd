# Varying-Local Mask Threading v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Fix the varying-local NaN-from-unmasked-writeback compiler bug that produces `NaN` output in `n-body` and `n-body-nosqrt`. Two-part change: restore the SSA lift guard from the reverted prior attempt, and add a TinyGo alloca-store routing safety net that avoids the latent scatter bug the prior attempt exposed.

**Architecture:** SSA `lift()` is excluded for `Varying[T]` allocas (same one-line guard as the prior reverted attempt). TinyGo's `createSPMDStore` gets a new branch that detects `*ssa.Alloc` addresses with `SPMDType` element and routes them to `spmdMaskedStore` (contiguous masked store) — bypassing the scatter dispatch entirely. Varying locals have a single alloca address, not per-lane pointers, so contiguous masked store is semantically correct; this avoids `spmdMaskedScatter`'s lane-count bug that broke 21 tests last time.

**Tech Stack:**
- Forked `x/tools` (`/home/cedric/work/SPMD/x-tools-spmd/`, branch `spmd`) — `go/ssa` lift pass
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`) — `createSPMDStore` in `compiler/spmd.go`
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`) — n-body and n-body-nosqrt ports
- LLVM vector intrinsics: `@llvm.masked.store.<ValueType>.p0`

**Spec:** `docs/superpowers/specs/2026-04-23-varying-local-mask-threading-v2-design.md`

**Reverted prior attempt:** `docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md` — this plan restores its lift guard + adds the new safety net.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` — verifies varying alloca survives lift |
| `x-tools-spmd/go/ssa/lift.go` (top of `liftAlloc` around line 402) | MODIFY | `isLanesVaryingType` helper + guard returning false for SPMD varying allocas |
| `tinygo/compiler/spmd_test.go` | MODIFY | Restore `TestSPMDVaryingLocalMaskedInTail` (masked writeback in tail) + add new `TestSPMDVaryingAllocaStoreUsesMaskedStore` (alloca-routing contract) |
| `tinygo/compiler/spmd.go` (in `createSPMDStore` after operand resolution ~line 8652, before contiguous-path check ~line 8687) | MODIFY | Alloca-store routing block — detect `*ssa.Alloc` + SPMDType element + call `spmdMaskedStore` + `return` |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (conditional on §4.4) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (conditional on §4.5) | — |
| `tinybench/BLOCKERS.md` | MODIFY or DELETE | Remove both blocker entries |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`. `go` submodule unchanged this round.

---

## Task 0: Pre-flight

**Files:** None (verification only).

- [ ] **Step 1: Submodule state**

```bash
cd /home/cedric/work/SPMD
for sub in x-tools-spmd tinygo tinybench go; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. `tinygo` tip should include `5ab97d8` (minimum/maximum stem fix) or later.

- [ ] **Step 2: Confirm NaN baseline**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
/tmp/nbns-spmd-bin 50000
```

Expected: starts with `NaN` (the second print may or may not reach; first line is sufficient proof of the bug).

Also check n-body:

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```

Expected: `NaN` output (same root cause).

- [ ] **Step 3: Capture E2E baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-baseline.txt 2>&1
tail -10 /tmp/e2e-baseline.txt
```

Expected: `Compile pass: 94, Compile fail: 0, Run pass: 93, Run fail: 0, Reject pass: 11` (or equivalent — these are the current totals after the 2026-04-22 lanes FP math primitives landed). Save the file for comparison later.

- [ ] **Step 4: Capture benchmark baseline**

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-baseline.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-baseline.txt | head -10
```

Expected: benchmark script completes; ratios captured for later comparison.

- [ ] **Step 5: Cleanup probe binaries**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nb-spmd-bin
```

---

## Task 1: SSA failing test

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`

TDD red baseline at the SSA layer. Restore the test from the prior reverted attempt. It fails until Task 3's lift guard lands.

- [ ] **Step 1: Create the test file**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go` with this complete content (copied verbatim from the prior attempt's committed version before revert):

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

// isVaryingElem reports whether t is a lanes.Varying[T] type, matching both
// the *types.SPMDType representation (produced when GOEXPERIMENT=spmd is active
// and the forked type-checker intercepts the lanes.Varying[T] type expression)
// and the *types.Named representation (produced without GOEXPERIMENT, where
// lanes.Varying[T] is a raw generic instantiation from the standard importer).
func isVaryingElem(t types.Type) bool {
	if _, ok := t.(*types.SPMDType); ok {
		return true
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj.Name() == "Varying" && obj.Pkg() != nil && obj.Pkg().Path() == "lanes" {
			return true
		}
	}
	return false
}

// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca
// survives the SSA lift() pass — its Alloc instruction remains in the
// function body so the SPMD predication pass can thread the active mask
// into its Store instructions.
//
// Without this property, lift promotes the alloca to phi-nodes whose
// edge values are computed unconditionally on inactive lanes — causing
// NaN/Inf to leak through partial-mask go-for iterations.
//
// Note: the SPMDStore conversion (Store -> SPMDStore with mask) is
// validated at the TinyGo LLVM IR layer in TestSPMDVaryingLocalMaskedInTail.
func TestSPMDVaryingAllocaNotLifted(t *testing.T) {
	// buildSPMDFuncBody builds SSA for a function with lanes.Varying[T]
	// parameters (an SPMD function body). The type checker allows
	// lanes.Varying[int](i) conversions inside such functions.
	//
	// acc is a Varying[int] alloca; the Store inside the loop must survive
	// lift so the predication pass can convert it to an SPMDStore with the
	// active mask. Without the fix, lift promotes the alloca to phi-nodes
	// and the Store disappears before predication runs.
	src := `package main

import "lanes"

func accumulate(v lanes.Varying[int], n int) lanes.Varying[int] {
	var acc lanes.Varying[int]
	for i := range n {
		_ = i
		acc = v
	}
	return acc
}

func main() {}
`
	pkg := buildSPMDFuncBody(t, src)
	fn := pkg.Func("accumulate")
	if fn == nil {
		t.Fatal("accumulate function not found in SSA")
	}

	// Verify the acc alloca survived lift: it must appear as *ssa.Alloc
	// with a lanes.Varying[T] element (either *types.SPMDType when
	// GOEXPERIMENT=spmd is active, or *types.Named for lanes.Varying[T]
	// when the standard importer treats it as a generic instantiation).
	var gotAlloc bool
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			if alloc, ok := instr.(*ssa.Alloc); ok {
				if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
					if isVaryingElem(ptr.Elem()) {
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

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `FAIL` with `varying alloca was lifted; expected memory-backed *ssa.Alloc with Varying[T] element in function body`.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_lift_test.go
```

Do NOT commit — clean-commit handles after review.

---

## Task 2: TinyGo tail-mask IR test

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Restore `TestSPMDVaryingLocalMaskedInTail` from the prior reverted attempt. Fails until Task 3 + Task 4 land (needs both lift guard and alloca routing).

- [ ] **Step 1: Append the test**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (before the `compileSPMDSource` / `mustContain` helpers at the bottom, or at the end of the file — anywhere after other test functions):

```go
// TestSPMDVaryingLocalMaskedInTail verifies that a varying-local
// compound assignment in a partial-mask go-for gets masked in the
// tail-body block, preventing inactive-lane NaN leaks.
//
// 5 iterations on 4-wide SIMD => main=4 iters, tail=1 iter (lanes 1-3
// inactive). The tail-body store of the accumulator must be masked,
// either via @llvm.masked.store or a load-select-store blend with a
// <4 x i1> select.
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
	mustContainAny(t, ir, "masked.store", "select <4 x i1>")
}
```

The helpers `compileSPMDSource` and `mustContainAny` already exist in `spmd_test.go` (introduced during the 2026-04-21 Varying[*Struct] work).

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingLocalMaskedInTail -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -20
```

Expected: FAIL with `IR missing all of: [masked.store select <4 x i1>]`.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

Do NOT commit.

---

## Task 3: TinyGo alloca-routing IR test

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

New test (not from prior attempt). Asserts the specific contract: alloca-store of varying value NEVER emits a scatter intrinsic.

- [ ] **Step 1: Append the test**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (right after `TestSPMDVaryingLocalMaskedInTail` from Task 2):

```go
// TestSPMDVaryingAllocaStoreUsesMaskedStore verifies that a varying-typed
// alloca's writeback in a partial-mask go-for emits @llvm.masked.store
// (the contiguous path) rather than @llvm.masked.scatter. This catches
// regressions where the alloca-store routing in createSPMDStore is bypassed
// and the store falls through to the scatter dispatch (which would emit
// @llvm.masked.scatter with potentially mismatched lane counts, as observed
// in the prior reverted lift-guard-only attempt).
func TestSPMDVaryingAllocaStoreUsesMaskedStore(t *testing.T) {
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
	mustContain(t, ir, "masked.store")
	mustNotContain(t, ir, "masked.scatter")
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingAllocaStoreUsesMaskedStore -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -15
```

Expected: FAIL (missing `masked.store`, OR present `masked.scatter`, OR both).

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

Do NOT commit.

---

## Task 4: SSA lift guard

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`

The SSA-layer fix. One helper + one guard at the top of `liftAlloc`. Makes Task 1's test pass.

- [ ] **Step 1: Add the helper + guard**

Open `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`. Add `"go/types"` to the imports if not already present. Find `func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {` (around line 402).

Insert the helper definition immediately BEFORE `liftAlloc`:

```go
// isLanesVaryingType reports whether t is a lanes.Varying[T] type, either as
// *types.SPMDType (created by GOEXPERIMENT=spmd type-checker interception) or
// as *types.Named with Obj().Name()=="Varying" in package "lanes" (the raw
// generic instantiation stored on types.Var objects when GOEXPERIMENT is not
// active during type-check).
func isLanesVaryingType(t types.Type) bool {
	if _, ok := t.(*types.SPMDType); ok {
		return true
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj.Name() == "Varying" && obj.Pkg() != nil && obj.Pkg().Path() == "lanes" {
			return true
		}
	}
	return false
}
```

Then insert the guard as the FIRST statement inside `liftAlloc`'s body (before the existing `Recover` check):

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
	// SPMD: keep varying allocas memory-backed so the SPMD predication
	// pass can thread the current active mask into their writebacks.
	// Without this, lift promotes them to phi-nodes whose edge values
	// are computed unconditionally on inactive lanes — causing NaN/Inf
	// to leak through partial-mask go-for iterations.
	if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
		if isLanesVaryingType(ptr.Elem()) {
			return false
		}
	}

	// ... existing body unchanged ...
```

- [ ] **Step 2: Run Task 1's test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDVaryingAllocaNotLifted`.

- [ ] **Step 3: Run broader SSA tests for regression**

```bash
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | tail -5
```

Expected: PASS. A pre-existing `TestSPMDCloneBlock_TranslateValue` crash may be present — confirmed unrelated in prior sessions. Everything else must pass.

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/lift.go
```

Do NOT commit.

---

## Task 5: TinyGo alloca-store routing

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go` (inside `createSPMDStore`, after operand resolution ~line 8652, before contiguous-path check ~line 8687)

The TinyGo-layer fix. Detects alloca-backed varying stores and routes to `spmdMaskedStore`. Makes Task 2 + Task 3 tests pass.

- [ ] **Step 1: Insert the routing block**

Open `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`. Find `createSPMDStore` (around line 8606). Locate the operand resolution + laneCount derivation (lines 8645-8652):

```go
addr := b.getValue(instr.Addr, instr.Pos())
val := b.getValue(instr.Val, instr.Pos())
mask := b.getValue(instr.Mask, instr.Pos())

// Derive lane count from the mask vector, which always has the correct
// target-specific width.
laneCount := mask.Type().VectorSize()
```

And the non-vectorizable-type handling that follows (lines 8654-8672). The new routing goes AFTER the non-vectorizable early-return (it should handle vectorizable types only) but BEFORE the contiguous-path check at line 8687.

Actually, the cleanest placement is immediately after the splat-scalar handling (line 8678-8683) and before the contiguous-path check at line 8687. Find the `// Contiguous access: use vector store instead of scatter.` comment. Directly above that comment, insert:

```go
// SPMD alloca-store: when the store's address is a varying-typed alloca
// (e.g., `var acc Varying[float64]; acc += x`), the address is a single
// scalar pointer — not a vector of per-lane pointers. Route directly to
// spmdMaskedStore (contiguous masked store), bypassing the scatter dispatch.
//
// This handles the writeback path that becomes reachable when the SSA lift()
// guard keeps Varying[T] allocas memory-backed. Without this routing,
// downstream code may try to interpret the alloca address as a per-lane
// pointer vector and emit a malformed @llvm.masked.scatter intrinsic with
// mismatched value/address lane counts (e.g., v32i8.v4p0 for Varying[byte]).
if alloc, ok := instr.Addr.(*ssa.Alloc); ok {
	if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
		if _, ok := ptr.Elem().(*types.SPMDType); ok {
			// Reshape a bool (i1) value element to i8 to match memory layout
			// (mirrors the existing line 8700-8704 normalization).
			if val.Type().TypeKind() == llvm.VectorTypeKind &&
				val.Type().ElementType().TypeKind() == llvm.IntegerTypeKind &&
				val.Type().ElementType().IntTypeWidth() == 1 {
				val = b.CreateZExt(val, llvm.VectorType(b.ctx.Int8Type(), val.Type().VectorSize()), "spmdstore.bool2byte")
			}
			b.spmdMaskedStore(val, addr, mask)
			return
		}
	}
}
```

- [ ] **Step 2: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH make build-tinygo 2>&1 | tail -3
```

Expected: clean rebuild.

- [ ] **Step 3: Run Task 2's test — expect PASS**

```bash
cd /home/cedric/work/SPMD/tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingLocalMaskedInTail -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDVaryingLocalMaskedInTail`.

- [ ] **Step 4: Run Task 3's test — expect PASS**

```bash
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingAllocaStoreUsesMaskedStore -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDVaryingAllocaStoreUsesMaskedStore`.

If either test fails:
- Inspect the generated IR via `-internal-printir` to see whether `masked.store` or `masked.scatter` was emitted.
- The alloca-detection may need to trace through `ChangeType` or other SSA wrappers if the direct `*ssa.Alloc` assertion fails. Report as DONE_WITH_CONCERNS if so.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
```

Do NOT commit.

---

## Task 6: Regression sweep — the critical gate

**Files:** None (verification only).

This is the go/no-go gate for C1. If `to-upper` or other tests regress, revert Tasks 4+5 and escalate.

- [ ] **Step 1: Full E2E sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after.txt 2>&1
tail -10 /tmp/e2e-after.txt
```

Expected totals: `Compile pass: 94, Compile fail: 0, Run pass: 93, Run fail: 0` (unchanged from baseline).

- [ ] **Step 2: Diff to baseline — catch any net losses**

```bash
diff <(grep -E "^\s*[A-Z]+ (compile|run|pass|fail)" /tmp/e2e-baseline.txt) \
     <(grep -E "^\s*[A-Z]+ (compile|run|pass|fail)" /tmp/e2e-after.txt)
```

Or more directly:

```bash
grep -c "COMPILE FAIL\|RUN FAIL\|WRONG OUTPUT" /tmp/e2e-after.txt
```

Expected: 0 failures.

If any test regresses:
- **STOP.** Do not proceed to Task 7.
- Note which tests failed (`to-upper`, `lo-clamp`, etc.) and the exact error shape.
- Revert Tasks 4 and 5: `cd x-tools-spmd && git checkout HEAD -- go/ssa/lift.go` (unstage) and `cd tinygo && git checkout HEAD -- compiler/spmd.go` (unstage).
- Escalate: the C1 hypothesis is wrong. Go back to brainstorming with C2 (narrow lift guard) or comprehensive scatter-bug fix.
- The test commits from Tasks 1-3 remain staged; they can stay as TDD failing tests pending C2 design, OR revert them too if we want a clean slate.

- [ ] **Step 3: Benchmark sweep**

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-after.txt | head -10
echo "---baseline---"
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-baseline.txt | head -10
```

Acceptance: each benchmark within ±10% of baseline. Likely movers: `lo-sum` / `lo-min` / `lo-max` (accumulator patterns that now keep their alloca memory-backed).

If any benchmark drops >10%:
- Investigate IR for that kernel to see if `mem2reg` is running.
- Mitigation options: tighten lift guard scope (larger design change).
- For now, proceed with caution — the perf regression isn't a correctness issue but should be tracked.

---

## Task 7: Re-enable n-body-nosqrt

**Files:**
- Delete (conditional on §4.4 passing): `/home/cedric/work/SPMD/tinybench/n-body-nosqrt/go-spmd/BLOCKER.md`
- Modify: `/home/cedric/work/SPMD/tinybench/BLOCKERS.md`

- [ ] **Step 1: Compile and run n-body-nosqrt**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
/tmp/nbns-spmd-bin 50000
```

Expected: `-0.169075164` / `-0.169078071` (two finite numbers). NOT `NaN`.

- [ ] **Step 2: Byte-exact diff vs scalar reference**

```bash
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

- [ ] **Step 3a: SUCCESS PATH — empty diff**

If diff is empty:

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body-nosqrt/go-spmd/BLOCKER.md
```

Remove the `## n-body-nosqrt` section from `tinybench/BLOCKERS.md`. Update the top summary to reflect the change.

Stage:
```bash
git add -A
git status --short
```

Expected:
```
 M BLOCKERS.md
 D n-body-nosqrt/go-spmd/BLOCKER.md
```

- [ ] **Step 3b: PARTIAL SUCCESS PATH — last-digit mismatch**

If diff shows only last-digit `%.9f` differences (e.g., `-0.169075163` vs `-0.169075164`), the feature works but float-reassociation in `reduce.Add` produces non-bit-identical output vs sequential scalar accumulation.

Update `n-body-nosqrt/go-spmd/BLOCKER.md` with the new cause (float reassociation) and keep the blocker. Update `BLOCKERS.md`'s entry accordingly. Do NOT delete.

Stage:
```bash
git add n-body-nosqrt/go-spmd/BLOCKER.md BLOCKERS.md
```

- [ ] **Step 3c: UNEXPECTED FAILURE**

If output is still NaN or wildly wrong: STOP. The fix is incomplete. Do not commit anything. Report and investigate.

- [ ] **Step 4: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nbns-go-bin
```

---

## Task 8: Re-enable n-body

**Files:**
- Delete (conditional on §4.5 passing): `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/BLOCKER.md`
- Modify: `/home/cedric/work/SPMD/tinybench/BLOCKERS.md`

Symmetric to Task 7.

- [ ] **Step 1: Compile and run n-body**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```

Expected: `-0.169075164` / `-0.169078071` (finite values).

- [ ] **Step 2: Byte-exact diff**

```bash
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

- [ ] **Step 3a: SUCCESS PATH**

If empty diff:

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body/go-spmd/BLOCKER.md
```

Remove the `## n-body` section from `tinybench/BLOCKERS.md`. If both n-body and n-body-nosqrt unblocked, the file is empty of blockers — delete it entirely:

```bash
# If BLOCKERS.md still has no blocker entries after removing n-body:
test -z "$(grep -E '^## ' BLOCKERS.md)" && rm BLOCKERS.md
```

Stage:
```bash
git add -A
```

- [ ] **Step 3b: PARTIAL SUCCESS PATH**

Same as Task 7 Step 3b — last-digit float reassociation. Update BLOCKER.md with new cause.

- [ ] **Step 3c: UNEXPECTED FAILURE**

Same as Task 7 Step 3c — STOP.

- [ ] **Step 4: Cleanup**

```bash
rm -f /tmp/nb-spmd-bin /tmp/nb-go-bin
```

---

## Task 9: Commit all staged changes

Up to this point, work across 3 submodules is staged but uncommitted. Each sub-step dispatches `clean-commit` for one logical unit.

- [ ] **Step 1: Commit the SSA changes (x-tools-spmd)**

Dispatch `clean-commit` with context: "Restore the SSA lift guard from the reverted 2026-04-21 attempt (commits `238e6bd` + `68a5c87`, reverted in `2d6d235` + `7b35f48`). Same one-line guard in `liftAlloc` with the `isLanesVaryingType` helper. Plus the `TestSPMDVaryingAllocaNotLifted` test. This attempt differs from the prior one by adding a TinyGo-side safety net (next commit in sibling submodule) that bypasses the previously-triggered scatter bug."

Working dir: `/home/cedric/work/SPMD/x-tools-spmd`. Branch: `spmd`. Staged files: `go/ssa/lift.go` and `go/ssa/spmd_lift_test.go`.

- [ ] **Step 2: Commit the TinyGo changes**

Dispatch `clean-commit` with context: "Add alloca-store routing safety net in `createSPMDStore` that detects `*ssa.Alloc` addresses with SPMDType element and routes to `spmdMaskedStore` (contiguous masked store), bypassing the scatter dispatch. Plus the two tests: `TestSPMDVaryingLocalMaskedInTail` (restored from prior attempt) and `TestSPMDVaryingAllocaStoreUsesMaskedStore` (new — locks in the no-scatter contract for alloca-stores). Companion to the SSA lift guard in x-tools-spmd; the routing prevents the regression that sank the prior attempt (v32i8.v4p0 scatter lane-count mismatch)."

Working dir: `/home/cedric/work/SPMD/tinygo`. Branch: `spmd`. Staged files: `compiler/spmd.go` and `compiler/spmd_test.go`.

- [ ] **Step 3: Commit the tinybench changes**

Depending on Task 7 and Task 8 paths:

- **Both SUCCESS (3a)**: Dispatch `clean-commit` with context: "Re-enable n-body and n-body-nosqrt SPMD ports — both now produce byte-identical output to their scalar references after the varying-local mask threading fix (v2). Deletes both BLOCKER.md files and the top-level BLOCKERS.md (no remaining blockers)."

- **One or both PARTIAL (3b)**: Dispatch `clean-commit` with context describing that the original NaN blocker resolved but a new float-reassociation blocker surfaced, updated BLOCKER.md(s) accordingly.

Working dir: `/home/cedric/work/SPMD/tinybench`. Branch: `spmd`. Staged files: whichever BLOCKER.md / BLOCKERS.md changes from Tasks 7+8.

- [ ] **Step 4: Bump parent SPMD submodule pointers**

```bash
cd /home/cedric/work/SPMD
git add x-tools-spmd tinygo tinybench
git diff --cached --stat
```

Expected:
```
 tinybench    | 2 +-
 tinygo       | 2 +-
 x-tools-spmd | 2 +-
 3 files changed, 3 insertions(+), 3 deletions(-)
```

Dispatch `clean-commit` with context: "Land the varying-local mask threading v2 fix across the toolchain. Submodule bumps:
- `x-tools-spmd`: lift guard (Varying[T] allocas excluded from lift) + new SSA test.
- `tinygo`: alloca-store routing in createSPMDStore + two IR tests.
- `tinybench`: n-body and n-body-nosqrt [unblocked OR blocker updated to new cause].
The fix is the reverted 2026-04-21 lift guard PLUS a new TinyGo safety net that routes varying-typed alloca stores to masked.store instead of masked.scatter. This avoids the lane-count mismatch that regressed 21 tests last time."

Working dir: `/home/cedric/work/SPMD`. Branch: `main`.

---

## Self-Review

### 1. Spec coverage

- Spec §1 (overview/scope/non-goals): plan header + Task 0 baselines.
- Spec §2.1 (lift guard change): Task 4 Step 1.
- Spec §2.2 (SSA test): Task 1 (red) + Task 4 Step 2 (green).
- Spec §2.3 (why lift guard alone isn't enough): addressed architecturally by Task 5.
- Spec §3.1 (alloca routing block): Task 5 Step 1 (verbatim code).
- Spec §3.2 (why correct): documented in Task 5 Step 1 comment.
- Spec §3.3 (what it doesn't touch): implicit — routing only fires for `*ssa.Alloc` addresses.
- Spec §3.4 (alloca through casts edge case): documented as YAGNI in Task 5 Step 4 escape hatch.
- Spec §3.5 (no SSA changes for routing): Task 5 touches only `compiler/spmd.go`.
- Spec §4.1 (SSA test): Task 1.
- Spec §4.2 (tail-mask IR test): Task 2.
- Spec §4.3 (alloca-routing IR test): Task 3.
- Spec §4.4 (n-body-nosqrt unblock): Task 7.
- Spec §4.5 (n-body unblock): Task 8.
- Spec §4.6 (regression sweep): Task 6 Steps 1-2.
- Spec §4.7 (benchmark sweep): Task 6 Step 3.
- Spec §5.1 (rollout order): Tasks 1-9 follow it.
- Spec §5.2 (file-by-file): plan's File Structure table matches.
- Spec §5.3 (risks): addressed:
  - Risk 1 (C1 hypothesis wrong): Task 6 Step 2 STOP-and-escalate procedure.
  - Risk 2 (alloca through wrappers): Task 5 Step 4 fallback + §4.3 IR test.
  - Risk 3 (mem2reg perf): Task 6 Step 3 ±10% check.
  - Risk 4 (field-access disturbed): Task 6 Step 1 full E2E sweep.
  - Risk 5 (float reassociation): Task 7/8 Step 3b branch.
- Spec §5.4 (success criteria): mapped to Task 4 Step 2 (SSA test), Task 5 Steps 3-4 (IR tests), Task 6 Steps 1+3 (regression), Task 7/8 Steps 2 (E2E diffs).
- Spec §5.5 (out of scope): not implemented.

No gaps.

### 2. Placeholder scan

No TBD/TODO/FIXME/XXX. Task 5 Step 4's mention of `-internal-printir` is a diagnostic hint, not a placeholder. Task 7/8 Step 3b's BLOCKER.md wording is pre-baked ("the new cause") and expanded inline in the earlier prior attempts' similar steps; the engineer reads the preceding BLOCKER.md for template.

### 3. Type consistency

- `TestSPMDVaryingAllocaNotLifted` — Task 1 + Task 4.
- `TestSPMDVaryingLocalMaskedInTail` — Task 2 + Task 5.
- `TestSPMDVaryingAllocaStoreUsesMaskedStore` — Task 3 + Task 5.
- `isLanesVaryingType(t types.Type) bool` — Task 4 Step 1.
- `isVaryingElem(t types.Type) bool` — Task 1 (test-side twin, unexported-vs-unexported package split).
- `createSPMDStore` / `spmdMaskedStore` — existing TinyGo functions; signatures unchanged.
- `compileSPMDSource`, `mustContain`, `mustNotContain`, `mustContainAny` — pre-existing test helpers (from 2026-04-21 Varying[*Struct] work).

Consistent.
