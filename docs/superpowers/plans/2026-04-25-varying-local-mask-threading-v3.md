# Varying-Local Mask Threading v3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Fix the varying-local NaN-from-unmasked-writeback compiler bug that produces `NaN` output in `n-body` and `n-body-nosqrt`, by annotating the SPMD loop's lane count onto `*ssa.Alloc` at SSA-predication time so TinyGo materializes the alloca's element type at the right vector width. This avoids the latent scatter bug that broke 21 tests in v1 and the alloca-routing dead-end of v2.

**Architecture:** Three-layer change.
1. **SSA struct extension**: Add `SPMDLaneCount int` field to `*ssa.Alloc` (zero = unset, default behavior preserved).
2. **SSA lift guard + annotation pass**: Restore the lift guard from v1/v2 so varying allocas survive `lift()`. Then, inside `spmdConvertLoopOps`, walk each loop's live scope blocks and set `alloc.SPMDLaneCount = loop.LaneCount` for any varying-typed alloca.
3. **TinyGo lowering**: In the `*ssa.Alloc` case of `createExpr`, if `expr.SPMDLaneCount > 0` and the element is `*types.SPMDType`, materialize the element as `llvm.VectorType(getLLVMType(elem.Elem()), expr.SPMDLaneCount)` instead of consulting `spmdMinLaneCount`.

The address vector now starts at the loop's iteration width, so the existing scatter / contiguous dispatch in `createSPMDStore` works without per-call-site rerouting. No alloca-store routing is added in v3.

**Tech Stack:**
- Forked `x/tools` (`/home/cedric/work/SPMD/x-tools-spmd/`, branch `spmd`) — `go/ssa` lift pass, predicate pass, struct extension
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`) — `*ssa.Alloc` materialization in `compiler/compiler.go`
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`) — n-body and n-body-nosqrt unblock
- LLVM vector types: `<N x T>` for the alloca's underlying element

**Spec:** `docs/superpowers/specs/2026-04-25-varying-local-mask-threading-v3-design.md`

**Reverted prior attempts:**
- `docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md` (v1, lift guard alone — exposed scatter bug, reverted)
- `docs/superpowers/specs/2026-04-23-varying-local-mask-threading-v2-design.md` (v2, lift guard + alloca routing — didn't help slice writes, reverted at GATE)

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/ssa.go` (Alloc struct ~line 670-675) | MODIFY | Add `SPMDLaneCount int` field — annotation written by predication pass, read by TinyGo |
| `x-tools-spmd/go/ssa/lift.go` (`liftAlloc` ~line 402) | MODIFY | `isLanesVaryingType` helper + early-return guard so varying allocas survive lift |
| `x-tools-spmd/go/ssa/spmd_predicate.go` (`spmdConvertLoopOps` ~line 354) | MODIFY | After `liveScopeBlocks` resolution, walk allocas in scope and set `SPMDLaneCount = loop.LaneCount` |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` + `TestSPMDVaryingAllocaLaneCountSet` |
| `tinygo/compiler/compiler.go` (`*ssa.Alloc` case ~line 2745-2746) | MODIFY | If `expr.SPMDLaneCount > 0` and element is `*types.SPMDType`, override `getLLVMType` with `llvm.VectorType(elem, SPMDLaneCount)` |
| `tinygo/compiler/spmd_test.go` | MODIFY | Add `TestSPMDVaryingLocalMaskedInTail` (mask threading at IR level) + `TestSPMDVaryingAllocaLLVMType` (alloca lane width contract) |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (conditional on §9) | — |
| `tinybench/n-body-nosqrt/go-spmd/main.go` | MODIFY (conditional) | Re-enable code if disabled in body |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (conditional on §10) | — |
| `tinybench/n-body/go-spmd/main.go` | MODIFY (conditional) | Re-enable code if disabled in body |
| `tinybench/BLOCKERS.md` | MODIFY or DELETE | Remove both entries; delete file if empty |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`. `go` submodule unchanged.

---

## Task 0: Pre-flight

**Files:** None (verification + cleanup only).

- [ ] **Step 1: Submodule state**

```bash
cd /home/cedric/work/SPMD
for sub in x-tools-spmd tinygo tinybench go; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. `tinygo` tip should include `5ab97d8` (minimum/maximum stem fix) or later. `x-tools-spmd` tip should be `2d6d23503` (revert of v1 test) or later.

- [ ] **Step 2: Discard stale staged v2 leftovers**

The previous v2 attempt left two staged but uncommitted files. They contain test stubs that overlap with what this plan re-creates from scratch. Reset them so each task starts fresh:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git restore --staged go/ssa/spmd_lift_test.go 2>/dev/null || true
rm -f go/ssa/spmd_lift_test.go

cd /home/cedric/work/SPMD/tinygo
git restore --staged compiler/spmd_test.go 2>/dev/null || true
git checkout -- compiler/spmd_test.go
```

Verify clean:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && git status --short
cd /home/cedric/work/SPMD/tinygo && git status --short
```

Expected: empty output for both submodules.

- [ ] **Step 3: Confirm NaN baseline**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -3
```

If the build succeeds, run it:

```bash
/tmp/nbns-spmd-bin 50000
```

Expected: starts with `NaN` (the first line is sufficient proof of the bug). If the BLOCKER had the body disabled, the build will succeed but produce wrong / placeholder output instead — note that and continue.

- [ ] **Step 4: Capture E2E baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-baseline-v3.txt 2>&1
tail -10 /tmp/e2e-baseline-v3.txt
```

Expected last lines (totals): roughly `Compile pass: 94, Compile fail: 0, Run pass: 93, Run fail: 0, Reject pass: 11`. Save the exact totals for the §8 GATE.

- [ ] **Step 5: Capture benchmark baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-baseline-v3.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-baseline-v3.txt | head -10
```

Expected: benchmark script completes; ratios captured for later comparison.

- [ ] **Step 6: Cleanup probe binaries**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nb-spmd-bin
```

---

## Task 1: SSA test — varying alloca survives lift (TDD RED)

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`

Restore the failing-test baseline from the prior attempts. It fails until Task 2's lift guard lands.

- [ ] **Step 1: Create the test file**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go` with this complete content:

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

// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca survives
// the SSA lift() pass — its Alloc instruction remains in the function body so
// the SPMD predication pass can annotate it with the surrounding loop's lane
// count and TinyGo can materialize its element at the correct vector width.
//
// Without this property, lift promotes the alloca to phi-nodes whose edge
// values are computed unconditionally on inactive lanes — causing NaN/Inf to
// leak through partial-mask go-for iterations.
func TestSPMDVaryingAllocaNotLifted(t *testing.T) {
	// buildSPMDFuncBody (defined in spmd_predicate_test.go) builds SSA for a
	// function with lanes.Varying[T] parameters (an SPMD function body).
	// acc is a Varying[int] alloca; the Store inside the loop must survive
	// lift so the predication pass can annotate the Alloc and convert the
	// Store to an SPMDStore with the active mask. Without the fix, lift
	// promotes the alloca to phi-nodes and the Alloc disappears before
	// predication runs.
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

Expected: `--- FAIL: TestSPMDVaryingAllocaNotLifted` with `varying alloca was lifted; expected memory-backed *ssa.Alloc with Varying[T] element in function body`.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_lift_test.go
```

Do NOT commit — `clean-commit` runs after `code-reviewer` review.

---

## Task 2: SSA lift guard (TDD GREEN for Task 1)

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go` (around line 402, top of `liftAlloc`)

Add the helper and the guard so varying allocas exit `liftAlloc` with `false` (preserved as memory-backed).

- [ ] **Step 1: Add helper + guard**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, locate the `liftAlloc` function (signature `func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool` near line 402).

Add `isLanesVaryingType` helper just above `liftAlloc` (or in the same file, file-private):

```go
// isLanesVaryingType reports whether t is a lanes.Varying[T] type. Matches
// both *types.SPMDType (the production representation when GOEXPERIMENT=spmd
// is active and the forked type-checker intercepts lanes.Varying[T]) and the
// raw *types.Named instantiation produced when the standard importer reads
// lanes.Varying[T] without GOEXPERIMENT.
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

Then insert the guard at the very top of `liftAlloc`'s body (before the `Recover` check around line 405):

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
	// SPMD: keep varying allocas memory-backed so the SPMD predication pass
	// can annotate them with the surrounding loop's lane count (see
	// SPMDLaneCount field on *Alloc) and TinyGo can materialize the element
	// type at the correct vector width.
	if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
		if isLanesVaryingType(ptr.Elem()) {
			return false
		}
	}

	// Don't lift result values in functions that defer
	// calls that may recover from panic.
	if fn := alloc.Parent(); fn.Recover != nil {
		// ... existing body unchanged ...
```

Verify the existing imports in `lift.go` already include `go/types`. If not (it almost certainly does), add it.

- [ ] **Step 2: Run the test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `--- PASS: TestSPMDVaryingAllocaNotLifted`.

- [ ] **Step 3: Run the full SSA package test — expect no regressions**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | tail -20
```

Expected: `ok  golang.org/x/tools/go/ssa  ...s`. No new failures (some tests may be skipped — that's fine).

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/lift.go
```

---

## Task 3: SSA test — annotation lane count set (TDD RED)

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`

Append a second test asserting the annotation pass writes `SPMDLaneCount` on the surviving varying alloca.

- [ ] **Step 1: Append the test**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go` (after `TestSPMDVaryingAllocaNotLifted`):

```go
// TestSPMDVaryingAllocaLaneCountSet verifies that the SPMD predication pass
// annotates a Varying[T] alloca inside an SPMD loop scope with the loop's
// lane count via the new *Alloc.SPMDLaneCount field. TinyGo reads this
// annotation during alloca-type materialization to emit the correct vector
// width (matching the surrounding loop's iteration width rather than the
// type's register-natural width).
//
// The accumulate function below has its single SPMD loop iterate `n` times
// in a Varying[int] body — so the alloca's SPMDLaneCount should equal the
// loop's LaneCount (a non-zero positive integer).
func TestSPMDVaryingAllocaLaneCountSet(t *testing.T) {
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

	var found bool
	var laneCount int
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			alloc, ok := instr.(*ssa.Alloc)
			if !ok {
				continue
			}
			ptr, ok := alloc.Type().Underlying().(*types.Pointer)
			if !ok {
				continue
			}
			if !isVaryingElem(ptr.Elem()) {
				continue
			}
			found = true
			laneCount = alloc.SPMDLaneCount
		}
	}
	if !found {
		t.Fatal("varying alloca not found in function body")
	}
	if laneCount == 0 {
		t.Fatal("alloc.SPMDLaneCount is 0; expected non-zero from predication pass")
	}
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaLaneCountSet -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: compile error `alloc.SPMDLaneCount undefined (type *ssa.Alloc has no field or method SPMDLaneCount)` because the field doesn't exist yet. This is the desired RED state — the failing test will compile and pass after Task 4.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_lift_test.go
```

---

## Task 4: SSA struct field + annotation pass (TDD GREEN for Task 3)

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go` (Alloc struct ~line 670-675)
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go` (`spmdConvertLoopOps` after `liveScopeBlocks` resolution ~line 354)

Add the field to `*Alloc` and the annotation walk inside `spmdConvertLoopOps`.

- [ ] **Step 1: Extend the Alloc struct**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go`, locate the `Alloc` struct (around line 670):

```go
type Alloc struct {
	register
	Comment string
	Heap    bool
	index   int // dense numbering; for lifting
}
```

Replace with:

```go
type Alloc struct {
	register
	Comment string
	Heap    bool
	index   int // dense numbering; for lifting

	// SPMDLaneCount, when non-zero, records the lane count of the SPMD
	// loop whose scope contains this alloca and whose element type is
	// lanes.Varying[T]. Set by the SPMD predication pass
	// (spmdConvertLoopOps) on allocas whose Underlying().(*types.Pointer)
	// element is *types.SPMDType. Read by TinyGo during alloca-type
	// materialization to size the alloca's element as
	// llvm.VectorType(elem, SPMDLaneCount) — matching the surrounding
	// SPMD loop's iteration width rather than the type's natural width.
	// Zero means unset; back-end falls back to its existing lane-count
	// derivation (typically spmdMinLaneCount).
	SPMDLaneCount int
}
```

- [ ] **Step 2: Add the annotation walk**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, locate `spmdConvertLoopOps` (function start near line 294). Inside the per-loop section, after `liveScopeBlocks` is computed and the `if len(liveScopeBlocks) == 0 { continue }` guard (around line 352-354), and BEFORE the `if loop.IsPeeled {` branch (around line 356), insert:

```go
// SPMD v3: annotate varying-typed allocas in this loop's scope with the
// loop's lane count. TinyGo reads alloc.SPMDLaneCount during alloca-type
// materialization to emit the correct vector width — matching the
// surrounding loop's iteration width rather than the alloca's element
// type's register-natural width. The `if alloc.SPMDLaneCount == 0`
// guard prevents inner-loop predication from overriding an outer
// loop's annotation when nested loops share an alloca.
for b := range liveScopeBlocks {
	for _, instr := range b.Instrs {
		alloc, ok := instr.(*Alloc)
		if !ok {
			continue
		}
		ptr, ok := alloc.Type().Underlying().(*types.Pointer)
		if !ok {
			continue
		}
		if !isLanesVaryingType(ptr.Elem()) {
			continue
		}
		if alloc.SPMDLaneCount == 0 {
			alloc.SPMDLaneCount = loop.LaneCount
		}
	}
}
```

The `isLanesVaryingType` helper is the one defined in `lift.go` in Task 2 (file-private but same package — reachable from `spmd_predicate.go`).

- [ ] **Step 3: Build the package**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go build ./go/ssa/... 2>&1 | tail -10
```

Expected: no output (clean build). If `isLanesVaryingType` is reported missing, ensure both `lift.go` (defining it) and `spmd_predicate.go` (using it) are in the same package directory `go/ssa/` — they should be.

- [ ] **Step 4: Run both lift tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run "TestSPMDVaryingAllocaNotLifted|TestSPMDVaryingAllocaLaneCountSet" -v -count=1 -timeout=60s 2>&1 | tail -15
```

Expected: both `--- PASS`.

- [ ] **Step 5: Run the full SSA package test — expect no regressions**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | tail -20
```

Expected: `ok  golang.org/x/tools/go/ssa  ...s`.

- [ ] **Step 6: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/spmd_predicate.go
```

---

## Task 5: TinyGo IR tests (TDD RED)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Two new tests: one asserts the alloca's LLVM type matches the loop width (the v3-specific contract), one asserts mask threading at the IR level (the symptom-level contract).

- [ ] **Step 1: Append the tests**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (anywhere after other test functions, before the helper definitions starting around line 737). The helpers `compileSPMDSource`, `mustContain`, `mustContainAny`, and `mustNotContain` already exist in this file.

```go
// TestSPMDVaryingAllocaLLVMType verifies that a Varying[int] alloca inside
// a go-for loop iterating over []float64 uses the loop's lane width, not
// int's register-natural width. The alloca should appear as
// `alloca <N x i32>` where N matches the loop's iteration width derived
// from the float64 element type and the AVX2 SIMD register size — i.e.,
// 4 lanes for float64 on 256-bit AVX2 (32 bytes / 8 bytes per float64).
//
// This test fails until the v3 *ssa.Alloc.SPMDLaneCount annotation is
// honored by the TinyGo *ssa.Alloc lowering case.
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
		acc += int(i)
	}
	_ = reduce.Add(acc)
}
`
	ir := compileSPMDSource(t, src)
	// On AVX2 with float64 iteration (8 bytes/lane, 32-byte register),
	// the loop iterates 4 lanes wide, so the Varying[int] alloca should
	// be allocated as <4 x i32>.
	mustContain(t, ir, "alloca <4 x i32>")
}

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

- [ ] **Step 2: Build TinyGo with the current x-tools-spmd**

Before running the new tests, rebuild TinyGo so it picks up Task 4's `*ssa.Alloc.SPMDLaneCount` field (otherwise the build/test binary will fail to compile).

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -10
```

Expected: build success, no errors.

- [ ] **Step 3: Run the tests — expect FAIL**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -run "TestSPMDVaryingAllocaLLVMType|TestSPMDVaryingLocalMaskedInTail" -v -count=1 -timeout=120s 2>&1 | tail -25
```

Expected: both `--- FAIL`.
- `TestSPMDVaryingAllocaLLVMType` fails because the alloca is materialized at the wrong width (likely `alloca <8 x i32>` from the int's effective lane count for a byte-iter loop, or no `alloca <N x i32>` at all if it's fully scalar today — pre-fix behavior).
- `TestSPMDVaryingLocalMaskedInTail` fails because the alloca-store falls through to the unmasked path (no `masked.store` / no `select <4 x i1>`).

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

---

## Task 6: TinyGo Alloc annotation-aware materialization (TDD GREEN for Task 5)

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` (`*ssa.Alloc` case ~line 2745-2746)

Make the `*ssa.Alloc` case consult `expr.SPMDLaneCount` when the element type is `*types.SPMDType`.

- [ ] **Step 1: Patch the Alloc case**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, locate the `*ssa.Alloc` case in `createExpr` (around line 2745):

```go
case *ssa.Alloc:
	typ := b.getLLVMType(expr.Type().Underlying().(*types.Pointer).Elem())
	size := b.targetData.TypeAllocSize(typ)
	// ... rest unchanged ...
```

Replace with:

```go
case *ssa.Alloc:
	elemType := expr.Type().Underlying().(*types.Pointer).Elem()

	// SPMD v3: when the alloca's element is a varying type and the SSA
	// predication pass has annotated the lane count, materialize the
	// element as a vector of that width rather than consulting the
	// function's spmdMinLaneCount or the type's natural width. This
	// keeps the alloca's vector width aligned with the surrounding SPMD
	// loop's iteration width — required for IndexAddr / load lane
	// consistency when the alloca's loaded value is fed into address
	// arithmetic. See *ssa.Alloc.SPMDLaneCount and
	// docs/superpowers/specs/2026-04-25-varying-local-mask-threading-v3-design.md.
	var typ llvm.Type
	if expr.SPMDLaneCount > 0 {
		if spmdElem, ok := elemType.(*types.SPMDType); ok {
			typ = llvm.VectorType(b.getLLVMType(spmdElem.Elem()), expr.SPMDLaneCount)
		}
	}
	if typ.IsNil() {
		typ = b.getLLVMType(elemType)
	}
	size := b.targetData.TypeAllocSize(typ)
	// ... rest unchanged (Heap / size / alloc / zero-init paths use `typ`) ...
```

The remaining body of the `*ssa.Alloc` case (the heap/stack branch, `MaxStackAlloc` check, `CreateEntryBlockAlloca`, zero-init) is unchanged — it already uses the local `typ` variable.

Verify `llvm` import is already present (it is — TinyGo's compiler.go uses `llvm.Value`, `llvm.Type`, etc. throughout).

Verify the `*types.SPMDType` reference is reachable. TinyGo's `compiler.go` already imports `"go/types"` (or the forked stdlib's equivalent) and references `*types.SPMDType` elsewhere (e.g., in `getLLVMType` or `spmd.go`). If the build fails on `types.SPMDType`, search for an existing reference (`grep -n 'types.SPMDType' compiler/compiler.go`) and follow that import.

- [ ] **Step 2: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -10
```

Expected: build success.

- [ ] **Step 3: Run both new tests — expect PASS**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -run "TestSPMDVaryingAllocaLLVMType|TestSPMDVaryingLocalMaskedInTail" -v -count=1 -timeout=120s 2>&1 | tail -15
```

Expected: both `--- PASS`.

- [ ] **Step 4: Run the full TinyGo compiler test — expect no regressions**

```bash
cd /home/cedric/work/SPMD/tinygo
../go/bin/go test ./compiler -count=1 -timeout=600s 2>&1 | tail -20
```

Expected: `ok  github.com/tinygo-org/tinygo/compiler  ...s`. No new failures.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go
```

---

## Task 7: Regression sweep — GATE

**Files:** None (verification only).

This is the explicit go/no-go gate. v1 and v2 both regressed 21 tests; v3's design is to fix the lane-count mismatch at its source so this should NOT happen. If any test that previously passed now fails, the v3 hypothesis is wrong — revert all changes.

- [ ] **Step 1: Rebuild full toolchain**

```bash
cd /home/cedric/work/SPMD
make build 2>&1 | tail -5
```

Expected: clean build.

- [ ] **Step 2: Run E2E sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after-v3.txt 2>&1
tail -10 /tmp/e2e-after-v3.txt
```

- [ ] **Step 3: Compare against baseline**

```bash
diff <(grep -E '^(PASS|FAIL|REJECT)' /tmp/e2e-baseline-v3.txt | sort) \
     <(grep -E '^(PASS|FAIL|REJECT)' /tmp/e2e-after-v3.txt | sort)
```

Expected: empty diff (same per-test outcomes) — or only added PASS lines for newly enabled tests, never any new FAIL.

Also compare totals:

```bash
diff <(grep -E '^(Compile pass|Compile fail|Run pass|Run fail|Reject pass)' /tmp/e2e-baseline-v3.txt) \
     <(grep -E '^(Compile pass|Compile fail|Run pass|Run fail|Reject pass)' /tmp/e2e-after-v3.txt)
```

Expected: identical, OR additions to pass / no additions to fail.

**GATE:**
- If the diff shows ANY new `FAIL` entry → STOP. Do not proceed. Revert all staged changes from Tasks 1-6 and escalate. Likely cause: another lane-count derivation path missed by the v3 design.
- If the diff is empty or shows only added PASS → proceed.

- [ ] **Step 4: Run benchmark sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after-v3.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-after-v3.txt | head -10
```

Compare against `/tmp/bench-baseline-v3.txt`. Acceptance: each ratio within ±10% of baseline. Wider allocas (e.g., `<8 x i32>` instead of `<4 x i32>` for an int alloca in a float-iter loop) may affect mem2reg cleanup; LLVM should still optimize most cases.

If any benchmark regresses by >10% → note it in the commit message; do not revert (the correctness fix takes priority). Document for follow-up investigation.

- [ ] **Step 5: Commit Tasks 1-6 (clean-commit pipeline)**

The clean-commit agent runs three commits in submodule order:

1. `x-tools-spmd`: SSA struct field + lift guard + annotation pass + tests
2. `tinygo`: alloca lowering + tests
3. (Parent SPMD submodule pointer bump — deferred to Task 10.)

The clean-commit agent decides commit boundaries; the suggested split is:

- `x-tools-spmd` commit 1: `feat: annotate varying allocas with SPMD lane count` (covers `ssa.go` Alloc field + `spmd_predicate.go` annotation walk + `lift.go` guard + `spmd_lift_test.go`)
- `tinygo` commit 1: `feat: materialize varying allocas at SPMD loop lane width` (covers `compiler/compiler.go` Alloc case + `compiler/spmd_test.go`)

The agent may split further if it judges the changes as logically separable. Either way, all changes from Tasks 1-6 must be committed before §8.

---

## Task 8: Re-enable n-body-nosqrt

**Files:**
- Modify (or revert): `/home/cedric/work/SPMD/tinybench/n-body-nosqrt/go-spmd/main.go` (if the body was disabled)
- Delete: `/home/cedric/work/SPMD/tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` (if it exists)

- [ ] **Step 1: Check current state of the port**

```bash
cd /home/cedric/work/SPMD/tinybench
ls n-body-nosqrt/go-spmd/
cat n-body-nosqrt/go-spmd/BLOCKER.md 2>/dev/null || echo "No BLOCKER.md"
```

If `main.go` has its body disabled (panic/empty/early-return placeholder), restore the original SPMD implementation. The expected program uses `lanes.Varying[float64]` accumulators — exactly the failing pattern this plan fixes. Use `git log -- n-body-nosqrt/go-spmd/main.go` to find the working pre-disable revision and restore from there.

- [ ] **Step 2: Build**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -5
```

Expected: clean build.

- [ ] **Step 3: Build the scalar Go reference**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
```

- [ ] **Step 4: Compare outputs**

```bash
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. The output should start with `-0.169075164` and end with `-0.169078071` (per the spec).

If the diff is non-empty → STOP. Capture both outputs, revert the port re-enable, and escalate. Do NOT delete the BLOCKER yet.

- [ ] **Step 5: Delete the BLOCKER**

If the diff was empty:

```bash
cd /home/cedric/work/SPMD/tinybench
rm -f n-body-nosqrt/go-spmd/BLOCKER.md
```

- [ ] **Step 6: Update tinybench/BLOCKERS.md**

Edit `/home/cedric/work/SPMD/tinybench/BLOCKERS.md` to remove the `n-body-nosqrt` entry. If both `n-body` and `n-body-nosqrt` were the only entries, leave the file with just a "no current blockers" note (or delete it entirely after Task 9 if the file becomes empty).

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body-nosqrt/go-spmd/main.go BLOCKERS.md
git rm --quiet n-body-nosqrt/go-spmd/BLOCKER.md 2>/dev/null || true
```

- [ ] **Step 8: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nbns-go-bin
```

---

## Task 9: Re-enable n-body

Symmetric to Task 8 — `n-body` shares the same root cause and unblocks via the same fix. The `lanes.Sqrt` change already landed (commit `5ab97d8`), so the port should build cleanly.

**Files:**
- Modify (or revert): `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/main.go`
- Delete: `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/BLOCKER.md` (if it exists)

- [ ] **Step 1: Check + restore body**

```bash
cd /home/cedric/work/SPMD/tinybench
ls n-body/go-spmd/
cat n-body/go-spmd/BLOCKER.md 2>/dev/null || echo "No BLOCKER.md"
```

If `main.go` was disabled, restore from git history.

- [ ] **Step 2: Build SPMD + scalar**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go 2>&1 | tail -5
go build -o /tmp/nb-go-bin ./n-body/go/main.go
```

- [ ] **Step 3: Compare outputs**

```bash
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff.

If diff non-empty → STOP. Revert. Escalate.

- [ ] **Step 4: Delete the BLOCKER**

```bash
cd /home/cedric/work/SPMD/tinybench
rm -f n-body/go-spmd/BLOCKER.md
```

- [ ] **Step 5: Update tinybench/BLOCKERS.md**

Edit `/home/cedric/work/SPMD/tinybench/BLOCKERS.md` to remove the `n-body` entry. If now empty, replace contents with a brief "no current compiler blockers as of YYYY-MM-DD" note, OR `git rm BLOCKERS.md` if the project convention is to omit the file when empty.

- [ ] **Step 6: Stage**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body/go-spmd/main.go BLOCKERS.md
git rm --quiet n-body/go-spmd/BLOCKER.md 2>/dev/null || true
```

- [ ] **Step 7: Commit Tasks 8 + 9 (clean-commit pipeline)**

Suggested commit: `feat: re-enable n-body and n-body-nosqrt SPMD ports`.

- [ ] **Step 8: Cleanup**

```bash
rm -f /tmp/nb-spmd-bin /tmp/nb-go-bin
```

---

## Task 10: Parent SPMD submodule pointer bumps

**Files:**
- Modify: parent SPMD repo's submodule pointers for `x-tools-spmd`, `tinygo`, `tinybench`.

After all submodule commits land in their own branches, the parent repo's submodule pointers must be updated so the workspace tracks the new revisions.

- [ ] **Step 1: Verify each submodule has the expected commits**

```bash
cd /home/cedric/work/SPMD
for sub in x-tools-spmd tinygo tinybench; do
    echo "=== $sub ==="
    (cd $sub && git log --oneline -3)
done
```

Expected: each submodule's tip is the new commit from Tasks 4 / 6 / 9.

- [ ] **Step 2: Stage submodule pointer updates**

```bash
cd /home/cedric/work/SPMD
git add x-tools-spmd tinygo tinybench
git status --short | grep '^M ' | grep -E 'x-tools-spmd|tinygo|tinybench'
```

Expected: three lines showing each submodule with `(new commits)` annotations in detailed `git diff --cached`.

- [ ] **Step 3: Commit (clean-commit pipeline)**

Suggested commit message:

```
deps: land varying-local mask threading v3 across toolchain

x-tools-spmd: annotate varying allocas with SPMD loop lane count.
tinygo: materialize varying allocas at the annotated lane width.
tinybench: re-enable n-body and n-body-nosqrt SPMD ports.

Fixes the NaN-from-unmasked-writeback bug. Supersedes the v1 (lift
guard alone, reverted) and v2 (lift guard + alloca-store routing,
reverted) attempts.

Spec: docs/superpowers/specs/2026-04-25-varying-local-mask-threading-v3-design.md
Plan: docs/superpowers/plans/2026-04-25-varying-local-mask-threading-v3.md
```

- [ ] **Step 4: Final E2E verification at parent repo**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-final-v3.txt 2>&1
tail -10 /tmp/e2e-final-v3.txt
```

Expected: same totals as `/tmp/e2e-after-v3.txt`. n-body / n-body-nosqrt no longer in any failure category.

- [ ] **Step 5: Cleanup**

```bash
rm -f /tmp/e2e-baseline-v3.txt /tmp/e2e-after-v3.txt /tmp/e2e-final-v3.txt \
      /tmp/bench-baseline-v3.txt /tmp/bench-after-v3.txt
```

---

## Done

All success criteria from §5.4 of the spec are met when:

- `TestSPMDVaryingAllocaNotLifted` (x-tools-spmd): PASS
- `TestSPMDVaryingAllocaLaneCountSet` (x-tools-spmd): PASS
- `TestSPMDVaryingLocalMaskedInTail` (tinygo): PASS
- `TestSPMDVaryingAllocaLLVMType` (tinygo): PASS
- `test/e2e/spmd-e2e-test.sh`: no new failures vs baseline
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference
- All BLOCKER.md files deleted; BLOCKERS.md updated/deleted
- Parent SPMD submodule pointers bumped to new revisions

Update memory (per CLAUDE.md auto-memory): record the v3 fix in `MEMORY.md` so future sessions know varying-local mask threading is solved via SSA-annotated lane counts on `*ssa.Alloc`.
