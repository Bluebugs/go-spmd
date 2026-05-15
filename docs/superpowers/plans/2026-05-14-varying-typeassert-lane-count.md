# Varying type-assertion lane-count fix (Option B) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply the same `Lanes()`-honoring rule to `createTypeAssertSPMD` that Option A applied to `createMakeInterface`, so boxing and unboxing of width-fixed `Varying[T]_N` use matching typecodes.

**Architecture:** One-line fix in `tinygo/compiler/spmd.go:9104-9106`, symmetric with the Option A patch at `tinygo/compiler/interface.go:131-145`. No new regression test (the fix is forward-compatibility; nothing currently produces `Lanes()>0` on a `TypeAssert.AssertedType`).

**Tech Stack:** TinyGo compiler (Go), LLVM bindings.

**Spec:** `docs/superpowers/specs/2026-05-14-varying-typeassert-lane-count-design.md`

**Baseline:** post-Option-A E2E `107/96/0/95/0/11 "All tests passed!"`. Must remain unchanged.

---

## File Inventory

- Modify: `tinygo/compiler/spmd.go` (~line 9104-9106) — `createTypeAssertSPMD`

That's it. No new files. No E2E changes.

---

## Task 1: Apply the symmetric fix

**Files:**
- Modify: `tinygo/compiler/spmd.go:9104-9106`

- [ ] **Step 1: Edit `tinygo/compiler/spmd.go`.**

Current code (line 9104-9106):

```go
	// Build the struct type that matches the type code used in boxing.
	elemLLVM := b.getLLVMType(spmdType.Elem())
	laneCount := b.spmdEffectiveLaneCount(spmdType, elemLLVM)
	boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
```

Replace with:

```go
	// Build the struct type that matches the type code used in boxing.
	// Honor spmdType.Lanes() when set, mirroring createMakeInterface
	// (interface.go:131-145). Without this symmetry, a Varying[T]_N
	// width-fixed by the SSA predication pass would be boxed with a
	// [N]T typecode but looked up here with a [native]T typecode,
	// breaking the runtime type assertion. In practice nothing today
	// puts Lanes()>0 on a TypeAssert.AssertedType, so this is a
	// forward-compatibility safety net.
	elemLLVM := b.getLLVMType(spmdType.Elem())
	laneCount := spmdType.Lanes()
	if laneCount <= 0 {
		laneCount = b.spmdEffectiveLaneCount(spmdType, elemLLVM)
	}
	boxedGoType := b.spmdBoxedVaryingGoType(spmdType, laneCount)
```

- [ ] **Step 2: Rebuild TinyGo.**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -5
```

Expected: build succeeds, no new warnings.

---

## Task 2: Verification

**Files:** none

- [ ] **Step 1: Full E2E.**

```bash
cd /home/cedric/work/SPMD
./test/e2e/spmd-e2e-test.sh 2>&1 | tee /tmp/e2e-after-b.txt | tail -25
```

Expected: `Total tests run: 107`, `Compile passes: 96`, `Compile failures: 0`, `Run passes: 95`, `Run failures: 0`, `Reject passes: 11`, `All tests passed!`. Identical to the post-Option-A baseline.

If any test regresses, STOP and investigate. The fix should have zero observable behaviour change today (nothing puts `Lanes()>0` on `AssertedType`).

- [ ] **Step 2: WASM SIMD-vs-scalar benchmark.**

```bash
./test/e2e/spmd-benchmark.sh 2>&1 | tee /tmp/bench-wasm-after-b.txt | tail -40
```

Expected: ratios match or improve on the post-Option-A measurements:
- hex-encode dst ~4-9x
- mandelbrot ~2.5-3.6x
- lo-* ~2-3x
- lo-clamp ~1.7-3x

Regression >10% on any ratio = STOP.

- [ ] **Step 3: x86-64 benchmark (SSE + AVX2).**

```bash
./test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-x86-after-b.txt | tail -60
```

Expected: ratios match or improve on post-Option-A. Same >10% regression STOP rule.

---

## Task 3: Update PLAN.md

**Files:**
- Modify: `PLAN.md` (mark Option B as DONE in the Deferred Items list)

- [ ] **Step 1: Edit the Option B entry.**

Find the `Option B: audit other Lanes()-ignoring callsites` entry
added in commit `20b324c`. Change `- [ ]` to `- [x]`, change
`NOT STARTED` to `DONE (2026-05-14)`, and add a brief outcome line:

```markdown
- [x] **Option B: audit other Lanes()-ignoring callsites in interface.go** — DONE (2026-05-14)
  - Outcome: The audit found that the candidates named in the
    original Option A spec (`interface.go:613`, `:1013`,
    `spmdBoxedVaryingGoType`) are not Lanes()-ignoring —
    `interface.go:624` and `:1024` are defensive name-generation
    fallbacks that don't compute lane counts, and `spmdBoxedVaryingGoType`
    accepts laneCount as a parameter. The actual symmetric callsite
    is `tinygo/compiler/spmd.go:9104-9106` in `createTypeAssertSPMD`.
    Fixed by applying the same `Lanes() > 0 ? Lanes() : spmdEffectiveLaneCount(...)`
    rule. Forward-compatibility safety net; no observable behaviour
    change today since nothing puts Lanes()>0 on TypeAssert.AssertedType.
  - Spec: `docs/superpowers/specs/2026-05-14-varying-typeassert-lane-count-design.md`
  - Plan: `docs/superpowers/plans/2026-05-14-varying-typeassert-lane-count.md`
```

(Keep the existing Location/Implementation/Priority/Related fields
above — only change the status line and append the Outcome and
Spec/Plan lines.)

---

## Task 4: Code review

**Files:** none

- [ ] **Step 1: Dispatch code-reviewer.** Same checklist as Option A:
  symmetry with `createMakeInterface`, no cache-collision hazard, no
  scalar-mode regression, no extra changes outside scope.

---

## Task 5: Commit

**Files:** none (commit step)

- [ ] **Step 1: Two commits, same pattern as Option A.**

**Commit 1 (in `tinygo/`, branch `spmd`):**
- Stage: `compiler/spmd.go`
- Summary: `fix(spmd): honor Lanes() in createTypeAssertSPMD`
- Body: explain the symmetry with the Option A fix and the
  forward-compatibility motivation.

**Commit 2 (main repo, branch `main`):**
- Stage: `PLAN.md`, `docs/superpowers/specs/...typeassert...`,
  `docs/superpowers/plans/...typeassert...`, `tinygo` submodule pointer.
- Summary: `docs(spmd): Option B typeassert audit + tinygo bump`
- Body: note that the audit reframed Option B (different lines than
  the Option A spec named) and that the fix is a forward-compat
  safety net with no observable behaviour change today.

End with the standard `Co-Authored-By: Claude Opus 4.7 (1M context)
<noreply@anthropic.com>` trailer in both.

- [ ] **Step 2: Verify clean trees.**

```bash
cd /home/cedric/work/SPMD/tinygo && git status
cd /home/cedric/work/SPMD && git status
git log --oneline -3
cd tinygo && git log --oneline -3
```

Both working trees should be clean. Two new commit SHAs visible.

---

## Out-of-scope

- Helper extraction (`spmdResolvedLaneCount`) — defer until a third
  callsite surfaces.
- x-tools-spmd-side audit — separate work.
- Pass-A widening to propagate Lanes() into asserted types.
