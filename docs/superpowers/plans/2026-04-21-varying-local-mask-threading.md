# Varying-Local Mask Threading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Fix the SPMD compiler bug where varying-local compound assignments (`acc += expr`) inside partial-mask `go for` iterations don't get masked, causing inactive-lane NaN/Inf values to leak through `reduce.Add` and corrupt downstream computation. Unblocks `tinybench/n-body-nosqrt`.

**Architecture:** One-line guard in `x-tools-spmd/go/ssa/lift.go` `liftAlloc` that excludes allocas whose element type is `*types.SPMDType`. Stores survive lift, get processed by the existing SPMD predication pass, become `*ssa.SPMDStore` with the correct active mask (all-ones for main body, `loop.TailMask` for tail). TinyGo's existing `createSPMDStore` handles the masked-store emission. LLVM `mem2reg`/`SROA` cleans up main-body memory traffic at codegen time.

**Tech Stack:**
- Forked Go (`/home/cedric/work/SPMD/go/`, branch `spmd`)
- Forked `x/tools` (`/home/cedric/work/SPMD/x-tools-spmd/`, branch `spmd`)
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`)
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`)

**Spec:** `docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md`

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` — verifies varying allocas survive lift |
| `x-tools-spmd/go/ssa/lift.go:402` | MODIFY | Guard at top of `liftAlloc` rejecting SPMDType-element allocas |
| `tinygo/compiler/spmd_test.go` | MODIFY | `TestSPMDVaryingLocalMaskedInTail` — verifies tail-block writeback masking in IR |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE | After §3.3 of spec passes |
| `tinybench/BLOCKERS.md` | MODIFY | Remove n-body-nosqrt entry |

Parent SPMD repo: submodule pointer bumps for x-tools-spmd, tinygo, tinybench at the end.

---

## Task 0: Pre-flight

**Files:** None (verification only).

- [ ] **Step 1: Verify submodule state**

```bash
cd /home/cedric/work/SPMD
for sub in go x-tools-spmd tinygo tinybench; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. Last tinybench commit should be `5b99233` or later (the BLOCKER.md update from prior session).

- [ ] **Step 2: Confirm n-body-nosqrt currently produces NaN**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
/tmp/nbns-spmd-bin 50000
```

Expected output:
```
NaN
NaN
```

This confirms the bug baseline. If the output is something else (already correct, or a different error), STOP — the assumption is wrong.

- [ ] **Step 3: Capture baseline benchmark numbers**

Run the performance sweep once to lock in the comparison reference. Capture to a file for §3.5 use later.

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-baseline.txt
```

Expected: completes without error. Save the file path; it'll be referenced in Task 4 Step 3.

- [ ] **Step 4: Capture baseline E2E correctness sweep**

```bash
bash test/e2e/spmd-e2e-test.sh 2>&1 | tee /tmp/e2e-baseline.txt | tail -10
```

Expected (per project memory): `102 total, 90 run-pass, 91 compile-pass, 0 compile-fail, 0 run-fail, 11 reject OK`. Note any divergence — that's the baseline this fix must preserve.

- [ ] **Step 5: Clean up the probe binary**

```bash
rm /tmp/nbns-spmd-bin
```

---

## Task 1: SSA-level failing test

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`

This is a TDD red baseline at the SSA layer — without the fix, lift promotes the varying alloca to phi-nodes, the test sees no `*ssa.Alloc` for `acc` in the function body and fails.

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

// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca
// survives the SSA lift() pass — its Alloc, Store, and Load instructions
// remain in the function body so the SPMD predication pass can thread
// the active mask into them.
//
// Without this property, lift promotes the alloca to phi-nodes whose
// edge values are computed unconditionally on inactive lanes — causing
// NaN/Inf to leak through partial-mask go-for iterations.
func TestSPMDVaryingAllocaNotLifted(t *testing.T) {
	src := `package main

import "lanes"

func accumulate() lanes.Varying[float64] {
	var acc lanes.Varying[float64]
	go for i, x := range []float64{1, 2, 3, 4, 5} {
		acc += x
		_ = i
	}
	return acc
}

func main() {}
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("accumulate")
	if fn == nil {
		t.Fatal("accumulate function not found in SSA")
	}

	var gotAlloc bool
	var gotSPMDStore bool
	for _, bb := range fn.Blocks {
		for _, instr := range bb.Instrs {
			if alloc, ok := instr.(*ssa.Alloc); ok {
				if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
					if _, ok := ptr.Elem().(*types.SPMDType); ok {
						gotAlloc = true
					}
				}
			}
			if _, ok := instr.(*ssa.SPMDStore); ok {
				gotSPMDStore = true
			}
		}
	}
	if !gotAlloc {
		t.Fatal("varying alloca was lifted; expected memory-backed *ssa.Alloc with SPMDType element in function body")
	}
	if !gotSPMDStore {
		t.Fatal("no *ssa.SPMDStore found; predication pass didn't see a surviving Store")
	}
}
```

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `FAIL` with one of:
- `varying alloca was lifted; expected memory-backed *ssa.Alloc ...` (most likely — lift removed the alloca)
- `no *ssa.SPMDStore found ...` (alloca survived but no store remained for predication)

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_lift_test.go
```

Do NOT commit — clean-commit handles the commit after code review.

---

## Task 2: TinyGo LLVM IR failing test

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Adds an LLVM IR test that catches the bug at the codegen layer. Without the fix, the alloca is gone, no masked-store or `select <4 x i1>` appears in the tail block's IR.

- [ ] **Step 1: Add the test**

Open `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` and append at the end of the file:

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

	// Tail-body store must use the mask — either an explicit
	// masked.store intrinsic, or a load-select-store blend with
	// <4 x i1> select. Without the fix, the alloca is lifted away
	// and neither pattern appears.
	mustContainAny(t, ir, "masked.store", "select <4 x i1>")
}
```

The helpers `compileSPMDSource` and `mustContainAny` were added in commit `3753d2d` (the prior `Varying[*Struct]` test infrastructure) and live in the same file.

- [ ] **Step 2: Run the test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingLocalMaskedInTail -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -20
```

Expected: FAIL with `IR missing all of: [masked.store select <4 x i1>]` (or equivalent — depends on the exact wording of `mustContainAny`'s failure message).

If instead the test errors out at compile-time (e.g., a type checker complaint), that's also acceptable as red baseline — the fix in Task 3 will resolve both compile and assertion paths.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

Do NOT commit.

---

## Task 3: Apply the lift guard

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go` (add 6 lines at the top of `liftAlloc`, around line 402)

This is the actual fix. Tests from Tasks 1 and 2 turn green.

- [ ] **Step 1: Insert the guard at the top of `liftAlloc`**

Open `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`. Locate `func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {` (around line 402). Insert the guard as the FIRST statement inside the function body:

Before:
```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
	// Don't lift result values in functions that defer
	// calls that may recover from panic.
	if fn := alloc.Parent(); fn.Recover != nil {
```

After:
```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
	// SPMD: keep varying allocas memory-backed so the SPMD predication
	// pass can thread the current active mask into their writebacks.
	// Without this, lift promotes them to phi-nodes whose edge values
	// are computed unconditionally on inactive lanes — causing NaN/Inf
	// to leak through partial-mask go-for iterations.
	if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
		if _, ok := ptr.Elem().(*types.SPMDType); ok {
			return false
		}
	}

	// Don't lift result values in functions that defer
	// calls that may recover from panic.
	if fn := alloc.Parent(); fn.Recover != nil {
```

- [ ] **Step 2: Run the SSA test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `PASS`.

- [ ] **Step 3: Rebuild TinyGo and run the IR test — expect PASS**

The TinyGo binary embeds x-tools-spmd at link time, so we need a rebuild before the IR test sees the fix.

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH make build-tinygo 2>&1 | tail -3
cd tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingLocalMaskedInTail -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | tail -10
```

Expected: TinyGo build clean. Test reports `--- PASS: TestSPMDVaryingLocalMaskedInTail`.

- [ ] **Step 4: Run the broader x-tools-spmd SSA tests for regression**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | tail -5
```

Expected: PASS (pre-existing `TestStdlib` failure is unrelated and confirmed in prior session).

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/lift.go
```

Do NOT commit.

---

## Task 4: Regression sweep — correctness + performance

**Files:** None (verification only; uses pre-built binaries).

- [ ] **Step 1: E2E correctness sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh 2>&1 | tee /tmp/e2e-after.txt | tail -10
```

Expected: same totals as `/tmp/e2e-baseline.txt` from Task 0 Step 4 (typically `102 total, 90 run-pass, 91 compile-pass, 0 compile-fail, 0 run-fail, 11 reject OK`).

Compare:
```bash
diff /tmp/e2e-baseline.txt /tmp/e2e-after.txt | head -30
```

Expected: no meaningful differences (timing variations OK; test counts identical). If counts changed:
- New PASSes (e.g., n-body-nosqrt now compiles cleanly): ✅ expected.
- New FAILs: investigate. The fix must not regress existing tests.

- [ ] **Step 2: Existing pointer-varying tests still pass**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDPointerVarying|TestSPMDVaryingPointer' -count=1 -timeout=60s 2>&1 | tail -5

cd ../tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingPointerFieldAddr -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test 2>&1 | grep -E "PASS|FAIL|---" | head -10
```

Expected: all PASS (the 2026-04-21 pointer-varying feature must remain functional).

- [ ] **Step 3: Performance sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-after.txt
```

Compare to `/tmp/bench-baseline.txt` from Task 0 Step 3.

Acceptance criteria: each benchmark within **±10%** of baseline. Project-memory baselines (AVX2 8-wide vs scalar SPMD):

| Benchmark | Baseline ratio |
|---|---|
| `lo-min` | 7.27x |
| `lo-max` | 7.18x |
| `lo-sum` | 5.09x |
| `lo-clamp` | 4.82x |
| `lo-mean` | 3.66x |
| `mandelbrot` | 6.07x |
| `hex-encode` dst | 13.01x |

If any benchmark drops by >10%:
1. The likely cause is LLVM `mem2reg`/`SROA` failing to fully clean up a main-body alloca.
2. Inspect the generated IR for the affected kernel — look for surviving `alloca` + `store` patterns in the all-ones mask path.
3. If the IR shows unmem2reg'd allocas, the spec needs revising — escalate back to the design phase. The remediation would be tightening the lift guard to only kick in for functions containing peeled loops (more complex than the current one-line fix).

If all benchmarks are within ±10%: proceed.

- [ ] **Step 4: Base64 Mula-Lemire spot check**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark.sh base64-mula-lemire 2>&1 | tail -10
```

Expected throughput: ~17 GB/s AVX2 (within ±10%). This benchmark is the highest optimization-pressure target; deviation here is the loudest signal.

If clean: proceed to Task 5. If regressed: escalate per Step 3 remediation.

---

## Task 5: Re-enable n-body-nosqrt (or update blocker)

**Files:**
- Either DELETE `/home/cedric/work/SPMD/tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` OR update its content
- Modify: `/home/cedric/work/SPMD/tinybench/BLOCKERS.md`

- [ ] **Step 1: Compile and run n-body-nosqrt with the fixed toolchain**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
/tmp/nbns-spmd-bin 50000 > /tmp/nbns-spmd.out
cat /tmp/nbns-spmd.out
```

Expected: two finite numbers near `-0.169075164` / `-0.169078071`.

- [ ] **Step 2: Output parity diff**

```bash
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
/tmp/nbns-go-bin 50000 > /tmp/nbns-go.out
diff /tmp/nbns-go.out /tmp/nbns-spmd.out
```

- [ ] **Step 3a: SUCCESS PATH — output identical**

If `diff` is empty:

Delete the BLOCKER.md and update the top-level BLOCKERS.md.

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body-nosqrt/go-spmd/BLOCKER.md
```

Open `tinybench/BLOCKERS.md` and remove the `## n-body-nosqrt` section in its entirety. Update the summary line: `Summary: **2 of 5** benchmarks are blocked.` → `Summary: **1 of 5** benchmarks are blocked.`

Stage:
```bash
git add -A
git status --short
```

Expected status:
```
 M BLOCKERS.md
 D n-body-nosqrt/go-spmd/BLOCKER.md
```

Do NOT commit.

- [ ] **Step 3b: PARTIAL SUCCESS PATH — output differs only in last digit**

If `diff` shows last-digit `%.9f` differences (e.g., `-0.169075163` vs `-0.169075164`), the compiler fix is correct but float-reassociation in `reduce.Add` produces non-bit-identical output vs the scalar sequential sum. This is the spec §4.3 risk #5.

Update `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` with new content describing the new cause (float reassociation), and update `BLOCKERS.md` accordingly. Don't delete.

Suggested replacement BLOCKER.md content:

```markdown
# n-body-nosqrt go-spmd: BLOCKED

**Status (2026-04-21, post-mask-threading-fix):** Original NaN bug
(varying-local writeback not masked in partial-mask iterations) is
**resolved** by the fork change at
`docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md`.
The port now produces finite output.

A **new blocker** surfaced: the SPMD output differs from the scalar
reference in the last digit of `%.9f`. Cause: `reduce.Add` performs a
tree reduction (e.g., `((a+b)+(c+d))`) while the scalar variant
accumulates sequentially (`((a+b)+c)+d`). IEEE-754 float addition is
not associative; the two orders differ in the last bit.

**Symptom:**
- Scalar: `-0.169075164` / `-0.169078071`
- SPMD:   <fill in actual observed values>

**Reproducer:**
\`\`\`bash
PATH=../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
/tmp/nbns-spmd-bin 50000
\`\`\`

**Next steps:** Either:
1. Accept the difference and add output-tolerance to TestCorrectness for this benchmark (changes the gate from byte-exact to within-1-ULP).
2. Add a `reduce.AddOrdered` variant that preserves sequential accumulation order at the cost of vectorization.
3. Restructure the n-body inner loop to avoid the divergence (probably
   not feasible — pair-loop reduction is fundamental to the algorithm).

---

## History

- **2026-04-13 to 2026-04-21** — original blocker: type checker rejected `b2.x` etc. Resolved by the Varying[*Struct] field access work.
- **2026-04-21** — second blocker: NaN runtime output. Resolved by the varying-local mask threading fix.
- **2026-04-21 forward** — third blocker: `%.9f` last-digit mismatch from `reduce.Add` tree-vs-sequential reduction order.
```

Stage:
```bash
git add n-body-nosqrt/go-spmd/BLOCKER.md BLOCKERS.md
git status --short
```

- [ ] **Step 3c: UNEXPECTED FAILURE PATH — different output entirely**

If the SPMD output is wildly different (not last-digit, not NaN), the compiler fix is incomplete or has a different bug. STOP — escalate to investigation. Do not modify BLOCKER.md or proceed to Task 6.

- [ ] **Step 4: Clean up probe binaries**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nbns-go-bin /tmp/nbns-spmd.out /tmp/nbns-go.out
```

---

## Task 6: Bump submodule pointers in parent SPMD repo

**Files:**
- Modify: `/home/cedric/work/SPMD` (submodule pointers for x-tools-spmd, tinygo, tinybench)

- [ ] **Step 1: Verify staged commits across submodules**

By this point, three submodules should have new commits (one each for x-tools-spmd test+fix, tinygo test, and tinybench blocker update — but the actual commits will have been made by the clean-commit agent across Tasks 1, 2, 3, 5).

```bash
cd /home/cedric/work/SPMD
git status --short
```

Expected output (some subset of):
```
 M x-tools-spmd
 M tinygo
 M tinybench
```

(Plus pre-existing changes to `bluebugs.github.io` and untracked files — IGNORE those.)

- [ ] **Step 2: Stage only the relevant submodules**

```bash
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

If `tinybench` is not staged, that's because Task 5 took the partial-success path (3b) — it should still be staged but with a different diff. Verify accordingly.

Do NOT commit yet.

---

## Self-Review

### 1. Spec coverage

- Spec §1 (overview + scope): plan header + Task 0 baselines.
- Spec §2.1 (the change — guard in liftAlloc): Task 3 Step 1.
- Spec §2.2 (why type check is sufficient): inherent in Task 3's targeted code.
- Spec §2.3 (downstream flow): tested by Tasks 1, 2 (SSA + IR layers).
- Spec §2.4 (outside-scope stores): Task 4 Step 2 (negative-path regression).
- Spec §3.1 (SSA unit test): Task 1.
- Spec §3.2 (TinyGo IR test): Task 2.
- Spec §3.3 (n-body-nosqrt E2E): Task 5.
- Spec §3.4 (negative-path regression): Task 4 Step 2.
- Spec §3.5 (benchmark regression): Task 4 Steps 1, 3, 4.
- Spec §4.1 (rollout order): tasks 1-6 follow it.
- Spec §4.2 (file-by-file): plan top File Structure section.
- Spec §4.3 (risks): each risk addressed:
  - Risk 1 (mem2reg fails): Task 4 Step 3 with escalation procedure.
  - Risk 2 (existing tests regress): Task 4 Steps 1, 2.
  - Risk 3 (Mula-Lemire perf): Task 4 Step 4.
  - Risk 4 (over-broadening): Task 1 SSA test confirms exactly the right shape is matched.
  - Risk 5 (output non-identical): Task 5 Step 3b explicit handling.
- Spec §4.4 (success criteria): mapped to Task 3 (SSA test), Task 3 (IR test), Task 4 Steps 1+3 (correctness + perf), Task 5 (E2E unblock).
- Spec §4.5 (out of scope / deferred): not implemented by design — `lanes.Sqrt` is its own next plan.

No gaps.

### 2. Placeholder scan

No TBD/TODO/FIXME/XXX/???. Task 5 Step 3b's BLOCKER.md template uses `<fill in actual observed values>` as a placeholder for the engineer to fill during a partial-success path — that's intentional (the BLOCKER doc captures the actual diff observed at the time, which can't be predicted). It's a documented in-the-document placeholder, not a plan placeholder.

### 3. Type consistency

- `TestSPMDVaryingAllocaNotLifted` — Task 1 only.
- `TestSPMDVaryingLocalMaskedInTail` — Task 2 only.
- `liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool` — Task 3 only; signature matches the actual function.
- `*types.SPMDType` (assertion target) — used consistently across Task 1 (test assertion) and Task 3 (guard).
- `compileSPMDSource`, `mustContainAny` — Task 2 references these as pre-existing in `spmd_test.go` (added in commit `3753d2d`). Verified during Task 7 of the prior plan.

Consistent.
