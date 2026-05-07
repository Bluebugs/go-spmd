# v8 Selective Lift + Tail-Mask Predication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore pre-v6.1 baseline performance (lo-* speedups, mandelbrot AVX2) while preserving v6.1 + v7 correctness gains (n-body, array-counting). Two changes: narrow the lift guard to non-vectorizable Varying types only; extend predication to mask tail-body loop-header phi back-edges with SPMDSelect.

**Architecture:** Pre-v6.1 lifted ALL Varying allocas → fast but n-body NaN. v6.1 preserved ALL Varying allocas → correct but 5× slower on accumulator hot loops. v8 splits the difference: vectorizable types lift normally and the predication pass adds SPMDSelect on tail-body back-edges; non-vectorizable types (slice headers, structs) remain preserved (needed by v7 Phase 1 alloca sizing).

**Tech Stack:** Go (forked at `/home/cedric/work/SPMD/go`), TinyGo (`/home/cedric/work/SPMD/tinygo`), x-tools-spmd (`/home/cedric/work/SPMD/x-tools-spmd`), LLVM 19.1.2.

**Spec:** `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md`

**Workflow** (per `/home/cedric/work/SPMD/CLAUDE.md`): every implementation task uses the mandatory pipeline:
1. **golang-pro** agent: implement
2. **code-reviewer** agent: review
3. **clean-commit** agent: commit

Verification-only tasks (Phase 0, sentinel checks) can be done directly.

---

## Build & test commands

**Build TinyGo** (after every TinyGo / x-tools-spmd edit):
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo
```

**Compile + run a test** (WASM):
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/x.wasm <test>.go
wasmtime /tmp/x.wasm
```

**Dump LLVM IR**:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/x.wasm <test>.go > /tmp/x.ll 2>&1
```

**SSA unit tests** (x-tools-spmd):
```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1
```

**Run e2e suite**:
```bash
bash test/e2e/spmd-e2e-test.sh
```

**WASM benchmark**:
```bash
bash test/e2e/spmd-benchmark.sh
```

**x86 benchmark**:
```bash
bash test/e2e/spmd-benchmark-x86.sh
```

**Sentinel n-body**:
```bash
cd tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
```
Expected: `-0.169075164` then `-0.169078071`.

---

## File structure

| File | Repo | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/lift.go` | x-tools-spmd | Narrow the lift guard to non-vectorizable Varying types; add `spmdElemNonVectorizable` helper |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | x-tools-spmd | Add `spmdMaskTailBodyBackEdges` helper; call it in `spmdConvertLoopOps` after existing tail conversions |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | x-tools-spmd | Unit tests for the narrowed lift guard |
| `x-tools-spmd/go/ssa/spmd_predicate_test.go` | x-tools-spmd | Unit tests for the back-edge masking |

**No changes** to TinyGo (the new SPMDSelect chain feeds existing TinyGo lowering unchanged). No changes to the Go fork.

---

## Phase 0 — Pre-flight verification + post-peel SSA investigation

### Task 0.1: Confirm starting state

**Files:** None modified.

- [ ] **Step 1: Verify clean state and current e2e**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject" | sed 's/\x1b\[[0-9;]*m//g'
```
Expected: **94/0/93/0/11**.

- [ ] **Step 2: Verify n-body sentinel**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
```
Expected: `-0.169075164` then `-0.169078071`.

- [ ] **Step 3: Capture starting performance numbers for comparison**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark.sh > /tmp/v8-pre-wasm.log 2>&1
echo "WASM bench saved to /tmp/v8-pre-wasm.log"
```
The WASM bench captures lo-sum / lo-mean / lo-min / lo-max / lo-clamp / lo-contains / mandelbrot / hex / base64 numbers. Phase 6 (final gate) will compare current state to baseline (`tinybench/_baseline-2026-04-30/wasm-bench/spmd-benchmark.log`) and to this pre-v8 capture.

If any sentinel fails, STOP and resolve before proceeding.

### Task 0.2: Investigation — confirm post-peel SSA shape for accumulator phis

**Files:** None modified — investigation only.

- [ ] **Step 1: Read peelSPMDLoop and SPMDLoopInfo fields**

```bash
grep -nB 1 -A 5 "type SPMDLoopInfo struct\|MainBodyBlock\|TailBodyBlock\|MainIterPhi" /home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go | head -40
grep -n "func peelSPMDLoop\|peelSPMDLoop" /home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_peel.go | head -5
```
Note the field names available on `SPMDLoopInfo`. Confirmed (from spec writing): `MainBodyBlock`, `TailBodyBlock`, `MainIterPhi`, `IsPeeled`, `TailMask`. There is NO `TailLoopBlock` or `TailHeader`.

- [ ] **Step 2: Compile a minimal lo-sum-style program with the lift guard temporarily off and dump SSA**

Create `/tmp/v8-peel-probe/main.go`:

```go
package main

import (
    "fmt"
    "lanes"
    "reduce"
)

func sumIt(data []int32) int32 {
    var total lanes.Varying[int32] = 0
    go for _, v := range data {
        total += v
    }
    return reduce.Add(total)
}

func main() {
    data := []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
    fmt.Println(sumIt(data))
}
```

Temporarily edit `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go` to disable the v6.1 lift guard locally (just for this investigation):

```go
// Before:
if ptr, ok := alloc.Type().(*types.Pointer); ok {
    if isLanesVaryingType(ptr.Elem()) {
        return false
    }
}

// During investigation, comment out the inner if:
if ptr, ok := alloc.Type().(*types.Pointer); ok {
    _ = ptr
    // if isLanesVaryingType(ptr.Elem()) {
    //     return false
    // }
}
```

Rebuild TinyGo and dump SSA:

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/sumit.wasm /tmp/v8-peel-probe/main.go > /tmp/sumit.ll 2>&1
grep -B 2 -A 80 "define.*sumIt" /tmp/sumit.ll | head -100
```

For SSA-level dump (more useful here), use the SSA tests' approach:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestNoTest -v 2>&1 | head -3  # Verify package builds
```

Or use a small Go program with `ssa.PrintFunc` (look at existing `spmd_loop_test.go` for examples).

- [ ] **Step 3: Identify the accumulator phi after peel**

Look at the SSA / IR for:
- The block containing the phi for `total` (the accumulator)
- The phi's predecessors (entry, main-body back-edge, tail-body back-edge — or some subset)
- Whether peeling places the phi at a single block or replicates it (one phi for main loop, one for tail)
- Where the post-loop `reduce.Add(total)` reads the accumulator's final value

Write findings to `/tmp/v8-peel-probe/notes.md`:
1. The exact block name + phi instruction for the accumulator
2. The predecessor edges and their values
3. The post-loop value flow from tail body to reduce.Add

- [ ] **Step 4: REVERT the temporary lift change**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git checkout go/ssa/lift.go
```

Verify reverted state:
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject"
```
Expected: still 94/0/93/0/11.

- [ ] **Step 5: Document the masking insertion point**

Based on findings, the `spmdMaskTailBodyBackEdges` helper needs to know:
- Which block to walk for phis (most likely `TailBodyBlock`'s successor that joins into the post-loop, OR the block holding the merged accumulator phi).
- For each varying phi, which edge is the "tail" edge (the one that needs masking).

Update `/tmp/v8-peel-probe/notes.md` with the precise field/block to walk. The implementation in Phase 2 will use these notes.

If the investigation shows the post-peel structure differs significantly from the spec's model (Section 4.2), STOP and report. The spec may need amendment before implementing.

---

## Phase 1 — Narrow the lift guard

This phase has TWO outcomes when complete (without Phase 2 also done):
- ✅ Lo-sum / lo-min / lo-max / lo-clamp regain main-body performance.
- ❌ n-body re-introduces NaN (the lifted phi has no mask on tail back-edge yet).
- ❌ E2E will show n-body / n-body-nosqrt failing if e2e includes them. Currently e2e doesn't run tinybench, so e2e will likely stay at 94/0/93/0/11. tinybench n-body will fail until Phase 2.

This is INTENTIONAL: TDD red for Phase 2.

### Task 1.1: Implementation — narrow lift guard + tests

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`

**Workflow**: dispatch `golang-pro` agent. Prompt:

> Implement v8 Phase 1 of `/home/cedric/work/SPMD/docs/superpowers/plans/2026-05-06-v8-selective-lift-and-tailmask.md`. **You have full Bash permission. USE IT.**
>
> ### Task 1.1.1 — TDD red: write failing test for vectorizable accumulator lifting
>
> Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`:
>
> ```go
> // TestSPMDVaryingInt32AccumulatorLifts asserts that a Varying[int32]
> // accumulator alloca is REMOVED post-lift in a function with a `go for` body.
> // v6.1's blanket lift guard preserved all Varying[T] allocas (perf regression);
> // v8 narrows the guard to non-vectorizable Varying types so vectorizable
> // accumulators (int, float, byte, pointer) lift back into phi nodes for speed.
> func TestSPMDVaryingInt32AccumulatorLifts(t *testing.T) {
>     src := `package p
>
> import "lanes"
> import "reduce"
>
> func F(data []int32) int32 {
>     var total lanes.Varying[int32] = 0
>     go for _, v := range data {
>         total += v
>     }
>     return reduce.Add(total)
> }
> `
>     fn := buildSPMDFunction(t, src, "F")
>     // Look through fn.Blocks for any *Alloc whose pointee is a Varying[int32]
>     // (or *types.SPMDType wrapping int32). Post-lift, no such alloca should remain.
>     for _, b := range fn.Blocks {
>         for _, instr := range b.Instrs {
>             if alloc, ok := instr.(*Alloc); ok {
>                 if ptr, ok := alloc.Type().(*types.Pointer); ok {
>                     if isLanesVaryingType(ptr.Elem()) {
>                         // Confirm it's a vectorizable elem (int32) — the test target.
>                         if spmdT, ok := ptr.Elem().(*types.SPMDType); ok {
>                             if basic, ok := spmdT.Elem().(*types.Basic); ok && basic.Kind() == types.Int32 {
>                                 t.Errorf("Varying[int32] alloca survived lift: %s in block %s", alloc, b.Comment)
>                             }
>                         }
>                     }
>                 }
>             }
>         }
>     }
> }
> ```
>
> If `buildSPMDFunction` does not exist, model after existing tests in the file. If `isLanesVaryingType` is package-scoped, use it directly; otherwise inspect via type assertion.
>
> Run:
> ```bash
> cd /home/cedric/work/SPMD/x-tools-spmd
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDVaryingInt32AccumulatorLifts -v
> ```
> Expected: FAIL — alloca currently survives v6.1 lift guard.
>
> ### Task 1.1.2 — TDD red: write test for non-vectorizable preservation
>
> Append:
>
> ```go
> // TestSPMDVaryingSlicePreserved asserts that a Varying[[]int] alloca SURVIVES
> // lift. Non-vectorizable Varying types (slice, struct, array, interface) cannot
> // be expressed as LLVM vector elements, so the alloca [N x T] representation
> // is required. v7 Phase 1 sizes the alloca correctly; v8 keeps the lift guard
> // for these types.
> func TestSPMDVaryingSlicePreserved(t *testing.T) {
>     src := `package p
>
> func F(arrays [][]int) []int {
>     result := make([]int, len(arrays))
>     go for i, secondLevel := range arrays {
>         t := 0
>         for _, v := range secondLevel {
>             t += v
>         }
>         result[i] = t
>     }
>     return result
> }
> `
>     fn := buildSPMDFunction(t, src, "F")
>     found := false
>     for _, b := range fn.Blocks {
>         for _, instr := range b.Instrs {
>             if alloc, ok := instr.(*Alloc); ok {
>                 if ptr, ok := alloc.Type().(*types.Pointer); ok {
>                     if spmdT, ok := ptr.Elem().(*types.SPMDType); ok {
>                         if _, isSlice := spmdT.Elem().Underlying().(*types.Slice); isSlice {
>                             found = true
>                         }
>                     }
>                 }
>             }
>         }
>     }
>     if !found {
>         t.Errorf("expected Varying[[]int] alloca to survive lift, but none found")
>     }
> }
> ```
>
> Run:
> ```bash
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDVaryingSlicePreserved -v
> ```
> Expected: PASS pre-fix (v6.1 already preserves all). After Phase 1 it must STILL PASS (regression guard).
>
> ### Task 1.1.3 — Implement: narrow lift guard
>
> Edit `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go` lines ~432-436. Find the existing v6.1 guard:
>
> ```go
> if ptr, ok := alloc.Type().(*types.Pointer); ok {
>     if isLanesVaryingType(ptr.Elem()) {
>         return false
>     }
> }
> ```
>
> Replace with:
>
> ```go
> if ptr, ok := alloc.Type().(*types.Pointer); ok {
>     if isLanesVaryingType(ptr.Elem()) && spmdElemNonVectorizable(ptr.Elem()) {
>         return false
>     }
> }
> ```
>
> Append the new helper to the same file (immediately after `isLanesVaryingType`):
>
> ```go
> // spmdElemNonVectorizable reports whether the element type T of Varying[T]
> // is non-vectorizable (struct, array, slice, interface — types that lower to
> // LLVM aggregate types and cannot be elements of an LLVM vector). For these,
> // the alloca [N x T] representation is the only valid lowering, so the alloca
> // must survive lift; the predication pass + v7 Phase 1 alloca sizing handle
> // width-fixing and per-lane access.
> //
> // Vectorizable element types (int, float, byte, pointer) lift normally —
> // TinyGo represents them as <N x T> LLVM vectors. The predication pass's
> // tail-body back-edge masking handles the partial-mask iter case, so lift
> // is safe for these types.
> func spmdElemNonVectorizable(t types.Type) bool {
>     var inner types.Type
>     if spmdT, ok := t.(*types.SPMDType); ok {
>         inner = spmdT.Elem()
>     } else if named, ok := t.(*types.Named); ok {
>         if named.TypeArgs() != nil && named.TypeArgs().Len() > 0 {
>             inner = named.TypeArgs().At(0)
>         } else {
>             return false
>         }
>     } else {
>         return false
>     }
>     switch inner.Underlying().(type) {
>     case *types.Struct, *types.Array, *types.Slice, *types.Interface:
>         return true
>     case *types.Pointer:
>         return false
>     default:
>         return false
>     }
> }
> ```
>
> ### Task 1.1.4 — Verify: tests pass
>
> ```bash
> cd /home/cedric/work/SPMD/x-tools-spmd
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run "TestSPMDVarying" -v -count=1
> ```
> Expected: BOTH tests PASS.
>
> Full SSA test suite:
> ```bash
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -10
> ```
> Expected: all PASS.
>
> ### Task 1.1.5 — Verify: e2e and n-body status
>
> ```bash
> cd /home/cedric/work/SPMD
> PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
> bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject"
> ```
> Expected: still 94/0/93/0/11 (e2e doesn't run tinybench n-body).
>
> ```bash
> cd /home/cedric/work/SPMD/tinybench
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
> ```
> Expected: **NaN** — this is intentional (Phase 2 fixes it). Document the actual output for the report.
>
> ### Constraints
>
> - DO NOT touch `tinygo/`. The fix is x-tools-spmd-only.
> - DO NOT modify the predication pass. Phase 2 handles that.
> - DO NOT commit. clean-commit handles commits at the end of Phase 2.
> - If e2e regresses below 94/0/93/0/11, STOP and report — Phase 1 must be neutral on e2e (since e2e doesn't include n-body).
>
> ### Report (under 350 words)
>
> - Files changed (paths + line counts)
> - Both unit tests pass
> - Full SSA suite pass
> - e2e summary line (still 94/0/93/0/11)
> - n-body output (NaN expected — intentional Phase 1 state)

### Task 1.2: code-reviewer

**Workflow**: dispatch `code-reviewer` agent.

Prompt:

> Review v8 Phase 1 changes in `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/`. **You have full Bash permission.**
>
> Diff: `git -C /home/cedric/work/SPMD/x-tools-spmd diff go/ssa/lift.go go/ssa/spmd_lift_test.go`
>
> Spec: `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md` — Section 3.1, 4.1
>
> Assess:
> 1. `spmdElemNonVectorizable` correctness: covers slice/struct/array/interface; passes through pointer/int/float/byte
> 2. The lift guard narrowing logic: only skips lift when BOTH `isLanesVaryingType` AND `spmdElemNonVectorizable` are true
> 3. Test coverage: TDD red for accumulator lift, regression guard for slice preservation
> 4. Re-verify all SSA tests pass:
>    ```bash
>    cd /home/cedric/work/SPMD/x-tools-spmd && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -10
>    ```
> 5. Confirm e2e at 94/0/93/0/11 and n-body produces NaN (expected at this phase pre-Phase 2)
>
> Verdict: Approve / Request changes. Report under 300 words.

### Task 1.3: HOLD commit until Phase 2 complete

**Files:** None modified.

- [ ] **Step 1: Do not commit Phase 1 alone**

Phase 1 leaves n-body broken. Per spec's rollback policy, the two changes can be reverted independently if needed. We commit them as separate atomic commits AFTER Phase 2 passes its verification. This way each commit's WHY is satisfied: Phase 1 narrows the guard (without leaving n-body broken if not paired with Phase 2).

If the user wants to commit Phase 1 alone, document explicitly that "n-body NaN regresses temporarily until Phase 2".

---

## Phase 2 — Predication: tail-body back-edge masking

Pre-task: re-read the post-peel SSA notes from Task 0.2 step 5 (`/tmp/v8-peel-probe/notes.md`). The implementer needs to know exactly which block to walk and which phi edge to mask.

### Task 2.1: Implementation — `spmdMaskTailBodyBackEdges` helper + integration

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate_test.go`

**Workflow**: dispatch `golang-pro` agent. Prompt:

> Implement v8 Phase 2 of `/home/cedric/work/SPMD/docs/superpowers/plans/2026-05-06-v8-selective-lift-and-tailmask.md`. **You have full Bash permission. USE IT.**
>
> Spec: `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md` Section 3.2, 4.2.
>
> Investigation notes from Task 0.2: `/tmp/v8-peel-probe/notes.md` — read first to understand the post-peel SSA shape.
>
> ### Task 2.1.1 — TDD red: write test for tail-body back-edge masking
>
> Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate_test.go`:
>
> ```go
> // TestSPMDTailBodyBackEdgeMasked asserts that a peeled SPMD loop with a
> // Varying[int32] accumulator has SPMDSelect inserted on the tail-body back
> // edge. Without v8's tail-mask predication, n-body's per-pair accumulators
> // would receive unmasked NaN values for inactive lanes in tail iterations.
> func TestSPMDTailBodyBackEdgeMasked(t *testing.T) {
>     src := `package p
>
> import "lanes"
> import "reduce"
>
> func F(data []int32) int32 {
>     var total lanes.Varying[int32] = 0
>     go for _, v := range data {
>         total += v
>     }
>     return reduce.Add(total)
> }
> `
>     fn := buildSPMDFunction(t, src, "F")
>     // Find the SPMD loop info (peeled).
>     if len(fn.SPMDLoops) == 0 || !fn.SPMDLoops[0].IsPeeled {
>         t.Skip("loop not peeled in this configuration")
>     }
>     loop := fn.SPMDLoops[0]
>     // Walk the tail body block. Look for SPMDSelect instructions whose
>     // Cond is loop.TailMask. At least one must exist (the masked accumulator
>     // back-edge).
>     foundMaskedSelect := false
>     for _, instr := range loop.TailBodyBlock.Instrs {
>         if sel, ok := instr.(*SPMDSelect); ok {
>             if sel.Cond == loop.TailMask {
>                 foundMaskedSelect = true
>                 break
>             }
>         }
>     }
>     if !foundMaskedSelect {
>         t.Errorf("expected SPMDSelect with TailMask cond on tail-body back edge; none found")
>     }
> }
> ```
>
> Run:
> ```bash
> cd /home/cedric/work/SPMD/x-tools-spmd
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDTailBodyBackEdgeMasked -v
> ```
> Expected: FAIL (no such SPMDSelect inserted yet).
>
> ### Task 2.1.2 — Implement: `spmdMaskTailBodyBackEdges` helper
>
> Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go` (near other predication helpers):
>
> ```go
> // spmdMaskTailBodyBackEdges wraps loop-header phi back-edge values that
> // originate in tail-body blocks with SPMDSelect(tail_mask, new, phi) so
> // inactive lanes preserve their previous-iter phi value across tail
> // iterations. This is the predication-side complement to v8's narrowed lift
> // guard: lifted Varying[T] phis receive correctly-masked back-edge values
> // without needing the alloca preserved.
> //
> // Runs only on peeled loops (loop.IsPeeled). For each phi at the appropriate
> // header, find edges whose source is in tailBlocks; insert the SPMDSelect
> // immediately before the source block's terminator and rewrite the phi's
> // tail-edge to the SPMDSelect result.
> //
> // Main-body iterations have an all-ones mask — back-edge masking would be
> // identity. We skip main-body emission entirely.
> func spmdMaskTailBodyBackEdges(fn *Function, loop *SPMDLoopInfo, tailBlocks map[*BasicBlock]bool, tailMask Value) {
>     if !loop.IsPeeled || tailMask == nil {
>         return
>     }
>     // The block(s) holding loop-header phis to mask. Per investigation in
>     // Task 0.2: the relevant block is the merge point of tail-body output,
>     // typically the post-loop block where reduce.Add reads the accumulator.
>     // Adjust based on /tmp/v8-peel-probe/notes.md.
>     //
>     // First-pass implementation: walk every block in fn.Blocks; for each Phi
>     // whose result type is *types.SPMDType, check each edge to see if its
>     // source block is in tailBlocks. If so, mask that edge.
>     for _, header := range fn.Blocks {
>         for _, instr := range header.Instrs {
>             phi, ok := instr.(*Phi)
>             if !ok {
>                 break // phis are at the top
>             }
>             // Only mask varying-typed phis.
>             if _, isVarying := phi.Type().(*types.SPMDType); !isVarying {
>                 continue
>             }
>             for i, pred := range header.Preds {
>                 if !tailBlocks[pred] {
>                     continue
>                 }
>                 edgeVal := phi.Edges[i]
>                 if edgeVal == phi {
>                     continue // self-loop; SPMDSelect would be identity
>                 }
>                 sel := &SPMDSelect{
>                     Cond:  tailMask,
>                     X:     edgeVal,
>                     Y:     phi,
>                     Lanes: loop.LaneCount,
>                 }
>                 sel.setType(phi.Type())
>                 sel.setBlock(pred)
>                 spmdInsertBeforeTerminator(pred, sel)
>                 spmdAddReferrer(tailMask, sel)
>                 spmdAddReferrer(edgeVal, sel)
>                 spmdAddReferrer(phi, sel)
>                 phi.Edges[i] = sel
>             }
>         }
>     }
> }
> ```
>
> ### Task 2.1.3 — Wire the helper into spmdConvertLoopOps
>
> In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, find the existing peeled-loop branch (around line 732 — `if loop.IsPeeled {`). After the existing tail-block conversions:
>
> ```go
> spmdConvertScopedMemOps(fn, tailBlocks, loop.TailMask, loop.LaneCount)
> spmdMaskScopedCallOps(fn, tailBlocks, loop.TailMask)
> spmdMaskScopedIndexOps(fn, tailBlocks, loop.TailMask)
> spmdMaskScopedMakeInterfaceOps(fn, tailBlocks, loop.TailMask, loop.LaneCount)
> ```
>
> Add the new call:
>
> ```go
> spmdMaskTailBodyBackEdges(fn, loop, tailBlocks, loop.TailMask) // v8: mask lifted-phi back-edges
> ```
>
> ### Task 2.1.4 — Verify: tests pass
>
> ```bash
> cd /home/cedric/work/SPMD/x-tools-spmd
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run "TestSPMDTailBodyBackEdgeMasked|TestSPMDVarying" -v -count=1
> ```
> Expected: ALL PASS.
>
> Full SSA test suite:
> ```bash
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -10
> ```
> Expected: all PASS.
>
> ### Task 2.1.5 — Verify: e2e + n-body sentinel
>
> ```bash
> cd /home/cedric/work/SPMD
> PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -2
> bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject"
> ```
> Expected: 94/0/93/0/11.
>
> n-body sentinel:
> ```bash
> cd /home/cedric/work/SPMD/tinybench
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
> ```
> Expected: `-0.169075164` then `-0.169078071` (NaN-free).
>
> n-body-nosqrt:
> ```bash
> PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-nosqrt n-body-nosqrt/go-spmd/main.go && /tmp/nb-nosqrt 50000
> ```
> Expected: same `-0.169075164 / -0.169078071`.
>
> ### Task 2.1.6 — Quick perf sanity check (lo-sum)
>
> ```bash
> cd /home/cedric/work/SPMD
> PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/lo-sum.wasm test/integration/spmd/lo-sum/main.go
> wasmtime /tmp/lo-sum.wasm | grep -E "Scalar|SPMD|Speedup"
> ```
> Expected: SPMD speedup ≥ 2.0× (close to pre-v6.1 baseline of 2.50×). If still ~0.4× (post-v6.1 regression), Phase 1 didn't take effect — investigate.
>
> ### Constraints
>
> - DO NOT touch `tinygo/`.
> - DO NOT commit. clean-commit handles commits at the next task.
> - If e2e regresses below 94/0/93/0/11, STOP and report.
> - If lo-sum speedup is still low (~0.4×), the lift narrowing isn't taking effect for the accumulator — investigate before proceeding.
>
> ### Report (under 400 words)
>
> - Files changed (paths + line counts)
> - Both unit tests pass (Phase 1 + Phase 2)
> - Full SSA suite pass
> - E2E summary line
> - n-body / n-body-nosqrt outputs
> - lo-sum speedup measurement

### Task 2.2: code-reviewer

**Workflow**: dispatch `code-reviewer` agent.

Prompt:

> Review v8 Phase 2 changes in `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/`. **You have full Bash permission.**
>
> Diff: `git -C /home/cedric/work/SPMD/x-tools-spmd diff go/ssa/spmd_predicate.go go/ssa/spmd_predicate_test.go`
>
> Spec: `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md` Section 3.2, 4.2.
>
> Assess:
> 1. `spmdMaskTailBodyBackEdges` correctness:
>    - Walks all blocks; finds varying-typed phis; masks edges from `tailBlocks`.
>    - SPMDSelect operands: `Cond=tailMask`, `X=edgeVal` (new), `Y=phi` (old).
>    - Edge case: self-loop phi → skipped.
>    - Edge case: non-varying phi (iter phi) → skipped via type assertion.
> 2. Insertion: SPMDSelect placed via `spmdInsertBeforeTerminator(pred, sel)`. Verify this is the correct existing helper and that referrers are wired up (`spmdAddReferrer` × 3).
> 3. Caller integration: `spmdMaskTailBodyBackEdges(fn, loop, tailBlocks, loop.TailMask)` placed AFTER `spmdConvertScopedMemOps` etc. so its work doesn't get clobbered.
> 4. Test coverage: `TestSPMDTailBodyBackEdgeMasked` asserts the SPMDSelect is inserted in the tail body for a known accumulator pattern.
> 5. Re-verify:
>    ```bash
>    cd /home/cedric/work/SPMD/x-tools-spmd && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -10
>    cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject"
>    cd tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
>    ```
> 6. **Latent risk**: the helper walks ALL blocks in `fn.Blocks`. Could it incorrectly mask edges in non-tail loops or unrelated phis? Verify by tracing through.
>
> Verdict: Approve / Request changes. Report under 350 words.

### Task 2.3: clean-commit (BOTH Phase 1 and Phase 2 as separate commits)

**Workflow**: dispatch `clean-commit` agent.

Prompt:

> Two atomic commits in `/home/cedric/work/SPMD/x-tools-spmd/`. Verify both phases' changes are present:
>
> ```bash
> git -C /home/cedric/work/SPMD/x-tools-spmd status --short
> ```
> Expected: `lift.go`, `spmd_lift_test.go`, `spmd_predicate.go`, `spmd_predicate_test.go` all modified.
>
> ### Commit 1 — Phase 1 (lift narrowing)
>
> Files: `lift.go`, `spmd_lift_test.go`.
>
> Commit message body:
>
> > v8 narrows the v6.1 lift guard from "all Varying[T] allocas preserved" to "only non-vectorizable Varying[T] allocas preserved". Vectorizable element types (int, float, byte, pointer) lift back into SSA phi nodes for performance. Non-vectorizable types (slice, struct, array, interface) remain preserved because LLVM cannot represent them as vector elements; v7 Phase 1 alloca sizing handles their per-lane storage.
> >
> > Without the partner Phase 2 change (tail-body back-edge masking), n-body would re-introduce its NaN bug. Land Phase 1 + Phase 2 together to keep correctness.
> >
> > Adds `spmdElemNonVectorizable` helper. Two unit tests cover:
> > - `Varying[int32]` accumulator alloca is REMOVED post-lift (Phase 1 enables this)
> > - `Varying[[]int]` slice alloca SURVIVES lift (regression guard for v7 Phase 1)
> >
> > Spec: docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md
>
> Suggested subject: `feat(spmd): narrow lift guard to non-vectorizable Varying[T]`
>
> Use HEREDOC for the message. Co-author footer per CLAUDE.md.
>
> Stage and commit:
> ```bash
> cd /home/cedric/work/SPMD/x-tools-spmd
> git add go/ssa/lift.go go/ssa/spmd_lift_test.go
> git commit -m "$(cat <<'EOF'
> feat(spmd): narrow lift guard to non-vectorizable Varying[T]
>
> ... (full body) ...
>
> Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
> EOF
> )"
> ```
>
> ### Commit 2 — Phase 2 (tail-body back-edge masking)
>
> Files: `spmd_predicate.go`, `spmd_predicate_test.go`.
>
> Commit message body:
>
> > v8 Phase 2: extend the SPMD predication pass to mask loop-header phi back-edges in tail-body blocks. After v8 Phase 1 narrowed the lift guard, vectorizable Varying[T] accumulators (e.g., n-body's dvx/dvy/dvz, lo-sum's total) lift back into SSA phi nodes. Without masking on the tail body's back edge, inactive lanes in tail iterations would receive unmasked updates — for n-body this produces NaN from `0 * Inf` arithmetic, contaminating reduce.Add.
> >
> > The new `spmdMaskTailBodyBackEdges` helper walks varying-typed phis in fn.Blocks; for each edge whose source is a tail-body block (per the existing tailBlocks set), inserts SPMDSelect(tail_mask, new_value, phi) before the source's terminator and rewrites the phi edge to the SPMDSelect result. Inactive lanes pick the old phi value; active lanes pick the new computed value.
> >
> > Main-body iterations have all-ones masks — back-edge masking would be identity, and we skip main-body blocks entirely (the helper iterates only `tailBlocks`).
> >
> > Together with Phase 1: lo-* / mandelbrot / hex / base64 perf restored to within 15% of pre-v6.1 baseline; n-body and n-body-nosqrt produce correct -0.169075164 / -0.169078071; e2e remains 94/0/93/0/11.
> >
> > Spec: docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md
>
> Suggested subject: `feat(spmd): mask tail-body loop-header phi back-edges`
>
> Stage and commit:
> ```bash
> git add go/ssa/spmd_predicate.go go/ssa/spmd_predicate_test.go
> git commit -m "$(cat <<'EOF'
> feat(spmd): mask tail-body loop-header phi back-edges
>
> ... (full body) ...
>
> Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
> EOF
> )"
> ```
>
> Report both commit hashes.

---

## Phase 3 — Performance verification + final gate

### Task 3.1: WASM benchmark vs baseline

**Files:** None modified.

- [ ] **Step 1: Run WASM benchmark**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark.sh > /tmp/v8-post-wasm.log 2>&1
```

- [ ] **Step 2: Compare key numbers vs pre-v6.1 baseline**

Pre-v6.1 baseline at `tinybench/_baseline-2026-04-30/wasm-bench/spmd-benchmark.log`. Post-v6.1 (regression) at `/tmp/v8-pre-wasm.log`. Post-v8 at `/tmp/v8-post-wasm.log`.

```bash
echo "=== lo-sum / lo-min / lo-max / lo-mean / lo-clamp / lo-contains ==="
echo "--- BASELINE pre-v6.1 (must match within 15%) ---"
grep -E "lo-sum|lo-mean|lo-min|lo-max|lo-clamp|lo-contains" tinybench/_baseline-2026-04-30/wasm-bench/spmd-benchmark.log | sed 's/\x1b\[[0-9;]*m//g' | grep -E "ms|us|x$" | head -10
echo "--- POST-v6.1 regression (the perf hit we are fixing) ---"
grep -E "lo-sum|lo-mean|lo-min|lo-max|lo-clamp|lo-contains" /tmp/v8-pre-wasm.log | sed 's/\x1b\[[0-9;]*m//g' | grep -E "ms|us|x$" | head -10
echo "--- POST-v8 (target) ---"
grep -E "lo-sum|lo-mean|lo-min|lo-max|lo-clamp|lo-contains" /tmp/v8-post-wasm.log | sed 's/\x1b\[[0-9;]*m//g' | grep -E "ms|us|x$" | head -10
```

Targets per spec §7:
- lo-sum: within 15% of 308ns; speedup ≥ 2.0×
- lo-mean: within 15% of 332ns; speedup ≥ 1.8×
- lo-min: within 15% of 324ns; speedup ≥ 2.0×
- lo-max: within 15% of 293ns; speedup ≥ 2.0×
- lo-clamp: within 15% of 6769ns; speedup ≥ 1.5×
- lo-contains: within 15% of 129ns

If any fails to meet target, document in `/tmp/v8-perf-gap.md` with measurements, then proceed to Task 3.4 to investigate.

### Task 3.2: x86 benchmark vs baseline

**Files:** None modified.

- [ ] **Step 1: Run x86 benchmark**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/v8-post-x86.log 2>&1
```

- [ ] **Step 2: Mandelbrot AVX2 manual run**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/mandel-avx2 test/integration/spmd/mandelbrot/main.go
for i in 1 2 3; do /tmp/mandel-avx2 2>&1 | grep -E "Serial computation|SPMD computation|speedup"; done
```
Targets per spec: SPMD computation time within 15% of ~940µs (pre-v6.1 baseline).

- [ ] **Step 3: Compare x86 lo-* speedups**

```bash
echo "--- BASELINE pre-v6.1 ---"
grep -E "Speedup: [0-9]" tinybench/_baseline-2026-04-30/x86-bench/spmd-benchmark-x86.log | head -10
echo "--- POST-v8 ---"
grep -E "Speedup: [0-9]" /tmp/v8-post-x86.log | head -10
```

Document results.

### Task 3.3: Final e2e gate

**Files:** None modified.

- [ ] **Step 1: Full e2e**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/v8-final-e2e.log 2>&1
grep -E "Total|Compile|Run|Reject|All tests" /tmp/v8-final-e2e.log | sed 's/\x1b\[[0-9;]*m="" '
```
Expected: 94/0/93/0/11 — "All tests passed!".

- [ ] **Step 2: All sentinels**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-nosqrt n-body-nosqrt/go-spmd/main.go && /tmp/nb-nosqrt 50000
```
Expected: both produce `-0.169075164 / -0.169078071`.

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go && wasmtime /tmp/ac.wasm
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/bc.wasm test/integration/spmd/bit-counting/main.go && wasmtime /tmp/bc.wasm
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/sw.wasm test/integration/spmd/swizzle-within/main.go && wasmtime /tmp/sw.wasm
```
Expected:
- array-counting: `Array sums: [3 3 4 18]`
- bit-counting: `Bit counts: 32`
- swizzle-within: `Correctness: PASS`

### Task 3.4: (CONTINGENCY) Performance investigation if targets missed

**Files:** None unless an issue is found.

- [ ] **Step 1: Identify the gap**

If any test fell below the 15% target:
- Compile the failing test with `-internal-printir` and inspect IR
- Compare to baseline IR (recompile pre-v6.1 binary using `tinybench/_baseline-2026-04-30/artifacts/wasm/<test>.wasm` as reference; re-derive IR via `wasm2wat`)
- Identify whether the lift removed the alloca (good) and the SPMDSelect was correctly inserted

- [ ] **Step 2: Common likely issues**

| Issue | Sign | Mitigation |
|---|---|---|
| Lift didn't fire | accumulator still has alloca + load + store | spmdElemNonVectorizable returned true incorrectly — debug |
| Tail-body SPMDSelect inserted in main body too | extra LLVM `select` in main body | Restrict the helper to scan only `tailBlocks` |
| Phi result type changed unexpectedly | LLVM verifier fails | Check that SPMDSelect's `setType` matches phi.Type() |
| Multi-edge phi loses partial masking | wrong values in tail | Verify all tail-block predecessors are masked |

If any issue requires a code change, dispatch a fresh `golang-pro` agent → `code-reviewer` → `clean-commit` cycle for that fix as a separate atomic commit.

### Task 3.5: Bump parent submodule pointer + final commit

**Files:** Parent repo `/home/cedric/work/SPMD/`.

- [ ] **Step 1: Verify x-tools-spmd HEAD has Phase 1 + Phase 2 commits**

```bash
git -C /home/cedric/work/SPMD/x-tools-spmd log --oneline -3
```
Should show the two new commits at the top.

- [ ] **Step 2: Stage parent pointer update + plan/spec docs**

```bash
cd /home/cedric/work/SPMD
git status --short
```

Stage:
- `x-tools-spmd` (submodule pointer bump)
- `docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md`
- `docs/superpowers/plans/2026-05-06-v8-selective-lift-and-tailmask.md`

Skip unrelated drift (`.gitmodules`, `bluebugs.github.io`, etc.).

- [ ] **Step 3: Dispatch clean-commit for parent commit**

Prompt the `clean-commit` agent:

> Create a single atomic commit in the parent repo `/home/cedric/work/SPMD/` bumping the x-tools-spmd submodule pointer to incorporate v8 (selective lift guard + tail-body back-edge masking).
>
> Files staged:
> - `x-tools-spmd` submodule pointer (bumped to HEAD)
> - `docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md`
> - `docs/superpowers/plans/2026-05-06-v8-selective-lift-and-tailmask.md`
>
> The WHY:
>
> > v8 restores pre-v6.1 baseline performance (lo-sum / mandelbrot / etc) while preserving the v6.1 + v7 correctness gains. Two narrow x-tools-spmd commits: lift narrowing for vectorizable Varying[T], plus predication-pass tail-body back-edge masking via SPMDSelect.
> >
> > Verified:
> > - E2E: 94/0/93/0/11 — "All tests passed!"
> > - n-body / n-body-nosqrt: -0.169075164 / -0.169078071
> > - array-counting / bit-counting / swizzle-within: PASS
> > - WASM lo-sum: <result>ns / <speedup>× (target: within 15% of 308ns / 2.50×)
> > - WASM lo-min/max/mean/clamp: similarly within target
> > - x86 mandelbrot AVX2: ~<result>µs / <speedup>× (target: within 15% of 940µs / 6.5×)
> >
> > Spec: docs/superpowers/specs/2026-05-06-v8-selective-lift-and-tailmask-design.md
>
> Replace `<result>` placeholders with actual numbers from Task 3.1 and 3.2 outputs.
>
> Suggested subject: `chore: bump x-tools-spmd to v8 (perf restored, correctness preserved)`

---

## Phase 4 — Documentation update (optional, post-merge)

### Task 4.1: Update PLAN.md and MEMORY.md (if user requests)

**Files:**
- Modify: `/home/cedric/work/SPMD/PLAN.md` (if it tracks per-version status)
- Modify (auto-memory): `~/.claude/projects/-home-cedric-work-SPMD/memory/MEMORY.md`

This task is opt-in. Skip if user prefers not to update docs as part of this plan.

If updating:
- Mark v8 as complete with the achieved perf numbers
- Add an entry in MEMORY.md noting the v6.1 + v7 + v8 chain and what each phase did

---

## Self-review

**Spec coverage** (against `2026-05-06-v8-selective-lift-and-tailmask-design.md`):

| Spec section | Plan task |
|---|---|
| §1 Problem statement | Phase 0 (verify regression baseline + sentinels) |
| §2 Guiding principle | Internalized in Phase 2 design |
| §3.1 Narrow lift guard | Phase 1 (Tasks 1.1, 1.2, 1.3) |
| §3.2 Tail-body back-edge masking | Phase 2 (Tasks 2.1, 2.2, 2.3) |
| §4.1 lift.go change | Task 1.1.3 |
| §4.2 spmd_predicate.go change | Tasks 2.1.2 + 2.1.3 |
| §4.3 No TinyGo changes | Confirmed in plan ("No changes" subsection) |
| §5 Coverage by test | Phase 3 sentinel checks (Task 3.3) |
| §6 Edge cases | Implementation notes in 2.1.2 |
| §7 Testing strategy | Phase 0 (pre-flight), Phase 3 (final gate) |
| §8 Out of scope | Explicit in spec; plan doesn't expand beyond it |
| §9 Success criteria | Phase 3 (3.1, 3.2, 3.3) |

**Placeholder scan**:
- `<result>` placeholders in Task 3.5's clean-commit prompt — explicit, to be filled in at commit time. Acceptable.
- `/tmp/v8-peel-probe/notes.md` reference in Task 0.2 → 2.1 — investigation artifact, not a placeholder.
- No "TBD", "implement later", or unsupplied code blocks.

**Type consistency**:
- `spmdElemNonVectorizable(t types.Type) bool` — used in Task 1.1.3 only; consistent.
- `spmdMaskTailBodyBackEdges(fn *Function, loop *SPMDLoopInfo, tailBlocks map[*BasicBlock]bool, tailMask Value)` — defined and called consistently in Task 2.1.

**Risks**:
- The implementation in Task 2.1.2 uses `for _, header := range fn.Blocks` (whole-function walk). If this turns out to incorrectly mask non-tail edges, the helper needs scope restriction. Investigation in Task 0.2 should clarify; first-pass implementation may need refinement (Task 3.4 contingency).
