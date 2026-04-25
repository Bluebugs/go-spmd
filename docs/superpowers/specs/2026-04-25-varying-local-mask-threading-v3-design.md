# Varying-Local Mask Threading v3 — Design (SSA-Annotated Lane Count)

**Date**: 2026-04-25
**Scope**: Third attempt at the varying-local NaN-from-unmasked-writeback fix. Supersedes the v1 (lift guard alone, reverted) and v2 (lift guard + TinyGo alloca-store routing, also reverted). The v3 design moves the lane-count knowledge into the SSA layer as an annotation on `*ssa.Alloc`, making TinyGo's lowering mechanical.

**Supersedes**:
- `2026-04-21-varying-local-mask-threading-design.md` (v1 — reverted; lift guard alone exposed latent TinyGo scatter bug)
- `2026-04-23-varying-local-mask-threading-v2-design.md` (v2 — reverted; alloca-store routing didn't help slice writes)

---

## 1. Overview, Scope, Architecture

### Goal

Properly fix the varying-local NaN-from-unmasked-writeback bug blocking `n-body` and `n-body-nosqrt` runtime correctness, without exposing the latent TinyGo scatter bug that broke 21 tests in v1 and v2.

### Diagnosis confirmed in v2

Walking through `to-upper`'s SSA dump with the v2 lift guard active showed:

```
4: if.then:
    t16 = spmd_load<32> t8 mask t30        # i (Varying[int]), <4 x i32> at LLVM
    t17 = spmd_load<32> t9 mask t30        # c (Varying[byte]), <32 x i8> at LLVM
    t18 = t17 - 32:lanes.Varying[byte]     # <32 x i8> result
    t19 = &t1[t16]                          # IndexAddr → <4 x ptr> (mismatch!)
    t20 = changetype byte <- lanes.Varying[byte] (t18)
    spmd_store<32> t19 t20 mask t30        # FAILS: scatter.v32i8.v4p0
```

Root cause: when the lift guard preserves a `Varying[int]` alloca, TinyGo's `getLLVMType` materializes its element as `<4 x i32>` (int's "effective" lane count from `spmdMinLaneCount`), not `<32 x i32>` (the loop's actual iteration width when iterating `[]byte`). The 4-wide load produces a 4-wide IndexAddr address vector, which then mismatches the 32-wide value vector at scatter time.

Without the lift guard, the alloca was phi-promoted before TinyGo's type materialization ran, so the phi inferred its type from loop-context propagation. With the guard, the alloca's element type is computed from the Go annotation alone, missing the loop context.

### v3 fix

Move the lane-count knowledge into the SSA layer:

1. **SSA**: Add `SPMDLaneCount int` field to `*ssa.Alloc` (zero = unset, fallback to existing behavior).
2. **SSA predication pass**: When walking live scope blocks, for each alloca whose element type is `*types.SPMDType`, set `alloc.SPMDLaneCount = loop.LaneCount`. The information is already in hand at that point.
3. **TinyGo lowering**: When materializing the LLVM type for an alloca whose element is a `*types.SPMDType`, if `SPMDLaneCount > 0`, use `llvm.VectorType(elemLLVM, SPMDLaneCount)` instead of consulting `spmdMinLaneCount`. Mechanical — no loop-state queries.

### Why this is more robust than v1 and v2

The v1 attempt applied the lift guard alone. That exposed a latent TinyGo scatter bug for sub-int varying types (`scatter.v32i8.v4p0`).

The v2 attempt added a TinyGo alloca-store routing safety net to bypass the scatter for alloca-stores. But the failing pattern in `to-upper` is a SLICE write `b[varying_i] = varying_byte`, NOT an alloca store — the routing didn't help. v2 was reverted.

The v3 fix addresses the root cause: the address vector starts at the right width (matching the value vector). No special routing needed — the existing scatter path emits valid IR because lane counts match. Cascading effect:

- Alloca element is `<32 x i32>` (matching the loop width).
- Load produces `<32 x i32>`.
- `IndexAddr(b, <32 x i32>)` produces `<32 x ptr>`.
- Scatter `<32 x i8>` value to `<32 x ptr>` addresses → matched lane counts → valid IR.
- `to-upper`, `lo-clamp`, and other sub-int tests no longer regress.

### Concrete unlock

- `n-body-nosqrt/go-spmd/main.go` produces `-0.169075164` / `-0.169078071` instead of NaN.
- `n-body/go-spmd/main.go` produces the same correct output.
- All previously-passing tests continue to pass.

### Non-goals

- Fixing the underlying TinyGo `spmdMaskedScatter` lane-count bug independently (the v3 fix avoids triggering it; the bug stays latent).
- Composite types containing varying fields.
- Changes to TinyGo's existing `spmdMinLaneCount` machinery — that continues to govern function-signature-based lane constraints. The new annotation overrides it ONLY for varying allocas inside SPMD loops.
- Inferring lane count from referrers as a fallback. The annotation is set explicitly during predication; no need for inference.

### Invariants preserved

- Single explicit-mask model on `*ssa.SPMDStore` (established 2026-03-05).
- Genuine scatter / contiguous / field-access paths in TinyGo unchanged.
- `Varying[*Struct]` field access (Cases A/B/C/D from 2026-04-21) unchanged.
- Stock-Go builds unaffected (annotation is gated by SPMDType detection).
- Backward compatibility: existing SSA without the annotation falls back to current `getLLVMType` behavior.

---

## 2. SSA Layer — `*ssa.Alloc.SPMDLaneCount`

### 2.1 The struct change

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go` (around lines 670-675).

Extend `*ssa.Alloc`:

```go
type Alloc struct {
    register
    Comment       string
    Heap          bool
    index         int // dense numbering; for lifting
    SPMDLaneCount int // 0 = unset; lane count for varying-typed alloca in SPMD loop
}
```

The `SPMDLaneCount` field is exported (capital `S`) so TinyGo can read it from outside the package.

### 2.2 The lift guard (restored from v1/v2)

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, function `liftAlloc` (around line 402).

Same guard as v1/v2 — exclude `Varying[T]` allocas from being phi-promoted, so they survive to the predication pass and to TinyGo lowering:

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
    // SPMD: keep varying allocas memory-backed so the SPMD predication
    // pass can annotate them with the loop's lane count and TinyGo can
    // materialize their element type at the correct vector width.
    if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
        if isLanesVaryingType(ptr.Elem()) {
            return false
        }
    }

    // ... existing body unchanged ...
}
```

`isLanesVaryingType` helper (also restored from v1/v2):

```go
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

Both representations matter (SPMDType in production, Named in test env where GOEXPERIMENT can't be activated).

### 2.3 The annotation pass

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, inside `spmdConvertLoopOps` (around line 401, after computing live scope blocks).

After the predication pass has determined the live scope blocks for a loop, walk those blocks and annotate any varying-typed allocas with the loop's lane count:

```go
// Annotate varying-typed allocas in this loop's scope with the loop's lane
// count. TinyGo reads alloc.SPMDLaneCount during type materialization to
// emit the correct vector width for the alloca's element type — matching
// the surrounding loop's iteration width rather than the type's
// register-natural width.
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

The `if alloc.SPMDLaneCount == 0` guard prevents nested-loop interference (an inner loop's predication can't override an outer loop's annotation).

### 2.4 SSA tests

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go` (the file already exists from the v2 attempt — staged but never committed).

Two tests:

```go
// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca survives
// the SSA lift() pass — its Alloc instruction remains in the function body so
// the SPMD predication pass can annotate it.
func TestSPMDVaryingAllocaNotLifted(t *testing.T) { ... }

// TestSPMDVaryingAllocaLaneCountSet verifies that the SPMD predication pass
// annotates varying-typed allocas with the surrounding loop's lane count via
// the SPMDLaneCount field. TinyGo reads this annotation during alloca-type
// materialization.
func TestSPMDVaryingAllocaLaneCountSet(t *testing.T) { ... }
```

The first test is already specified in v2's plan. The second is new for v3.

---

## 3. TinyGo Backend — Annotation-Aware Type Materialization

### 3.1 The change

**File**: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, the `*ssa.Alloc` case (around line 2746).

When materializing the LLVM type for an alloca whose element is `*types.SPMDType` and `SPMDLaneCount > 0`, override the default lane count:

```go
case *ssa.Alloc:
    elemType := expr.Type().Underlying().(*types.Pointer).Elem()

    // SPMD: when the alloca's element is a varying type and the SSA
    // predication pass has annotated the lane count, use that count rather
    // than the function's spmdMinLaneCount or the type's natural width.
    // This keeps the alloca's vector width aligned with the surrounding
    // SPMD loop's iteration width — required for IndexAddr lane consistency.
    var llvmElemType llvm.Type
    if expr.SPMDLaneCount > 0 {
        if spmdElem, ok := elemType.(*types.SPMDType); ok {
            llvmElemType = llvm.VectorType(b.getLLVMType(spmdElem.Elem()), expr.SPMDLaneCount)
        }
    }
    if llvmElemType.IsNil() {
        llvmElemType = b.getLLVMType(elemType)
    }
    // ... rest of alloca creation unchanged, using llvmElemType ...
```

The wrapping is targeted: only fires when both conditions hold. Falls back to existing behavior otherwise.

### 3.2 Why this is mechanical

- No `b.spmdLoopState` query — the annotation is on the SSA instruction.
- No reasoning about what loop the alloca belongs to.
- Symmetric with how `SPMDStore.Lanes` and `SPMDLoad.Lanes` are read directly.
- Visible in SSA dumps (debugging-friendly).

### 3.3 What it doesn't touch

- Slice writes (`b[varying_i] = v`): IndexAddr now produces the correct lane count automatically because `i`'s loaded value width matches the loop. No special handling needed.
- Genuine scatter (truly varying pointers): unchanged.
- Field access on `Varying[*Struct]` (Cases A/B/C/D): unchanged.
- Allocas without SPMD context (no annotation set): unchanged.

### 3.4 No alloca-store routing

The v2 design added a TinyGo routing block in `createSPMDStore` that detected `*ssa.Alloc` addresses and routed to `spmdMaskedStore`. **v3 does not need this.** With the lane-count annotation, the address vector emerges from upstream operations at the correct width, and the existing scatter / contiguous dispatch in `createSPMDStore` works correctly. Removing the routing keeps TinyGo simpler.

---

## 4. Testing

Three test layers + regression sweep.

### 4.1 SSA unit test — alloca survives lift

**File**: `x-tools-spmd/go/ssa/spmd_lift_test.go`. Same as v2's `TestSPMDVaryingAllocaNotLifted`. Verifies the lift guard works.

### 4.2 SSA unit test — annotation set

**File**: `x-tools-spmd/go/ssa/spmd_lift_test.go` (or sibling). New for v3:

```go
// TestSPMDVaryingAllocaLaneCountSet verifies that the SPMD predication pass
// sets alloc.SPMDLaneCount on Varying[T] allocas inside SPMD loop scope.
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

    var found bool
    var laneCount int
    for _, bb := range fn.Blocks {
        for _, instr := range bb.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if ptr, ok := alloc.Type().Underlying().(*types.Pointer); ok {
                    if isVaryingElem(ptr.Elem()) {
                        found = true
                        laneCount = alloc.SPMDLaneCount
                    }
                }
            }
        }
    }
    if !found {
        t.Fatal("varying alloca not found")
    }
    if laneCount == 0 {
        t.Fatal("alloc.SPMDLaneCount is 0; expected non-zero from predication pass")
    }
}
```

### 4.3 TinyGo IR test — alloca lane width matches loop

**File**: `tinygo/compiler/spmd_test.go`. Restore `TestSPMDVaryingLocalMaskedInTail` from v2 — this should now pass via the v3 fix because the alloca-typed values use the correct lane width naturally.

Add a new test that asserts the alloca's LLVM type matches the loop's natural width:

```go
// TestSPMDVaryingAllocaLLVMType verifies that a Varying[int] alloca inside
// a go-for loop iterating over []byte uses the loop's 32-lane width, not
// int's register-natural 4-lane width. The alloca should appear as
// `alloca <32 x i32>` in the IR.
func TestSPMDVaryingAllocaLLVMType(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []byte{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
    var acc lanes.Varying[int]
    go for i, _ := range data {
        acc += int(i)  // varying-int accumulator inside byte-iter loop
    }
    _ = reduce.Add(acc)
}
`
    ir := compileSPMDSource(t, src)
    // Alloca must use the loop's lane count (32 for byte iteration on AVX2),
    // not int's effective width (4-8).
    mustContain(t, ir, "alloca <32 x i32>")
}
```

### 4.4 End-to-end regression — n-body-nosqrt unblock

After all changes land:

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. Reference: `-0.169075164` / `-0.169078071`.

### 4.5 End-to-end regression — n-body unblock

Symmetric; same toolchain unblock. The `lanes.Sqrt` change already landed.

### 4.6 Regression sweep — to-upper et al

The critical safety check. v1 and v2 both regressed 21 tests (`to-upper`, `lo-clamp`, etc.). v3 should NOT regress them — it fixes the underlying lane-count mismatch at its source.

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after.txt 2>&1
tail -10 /tmp/e2e-after.txt
```

Acceptance: same totals as baseline (94 compile / 93 run / 0 fail) — or +tests if new annotations get picked up — but NO new fail entries.

If `to-upper` (or any other test) fails:
- The v3 hypothesis is wrong — there's another lane-count derivation path we missed.
- Revert all changes; escalate.

### 4.7 Benchmark sweep

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)" /tmp/bench-after.txt | head
```

Acceptance: each within ±10% of baseline. The annotation widens some allocas (e.g., `<4 x i32>` → `<32 x i32>` for an int alloca in a byte-iter loop). LLVM `mem2reg` should still clean these up. Most likely affected: `lo-sum` / `lo-min` / `lo-max` (accumulator patterns).

---

## 5. Rollout, File Changes, Risks

### 5.1 Rollout order

1. SSA struct extension (§2.1) + annotation pass (§2.3) + lift guard (§2.2) — all in x-tools-spmd. Single commit per logical change OR combined since they're tightly coupled.
2. SSA tests (§2.4 + §4.2) — separate commit (TDD red baseline, then green after step 1).
3. TinyGo lowering change (§3.1) — separate commit; depends on x-tools-spmd commit.
4. TinyGo IR test (§4.3) — separate commit (TDD red, then green after step 3).
5. Regression sweep (§4.6, §4.7) — must be clean. The explicit go/no-go gate.
6. Tinybench n-body-nosqrt unblock — delete BLOCKER, update BLOCKERS.
7. Tinybench n-body unblock — same.
8. Parent SPMD: bump submodule pointers.

Each step independently revertible. If §5 regresses, revert the whole stack.

### 5.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `x-tools-spmd/go/ssa/ssa.go` (Alloc struct, ~line 670) | MODIFY | Add `SPMDLaneCount int` field |
| `x-tools-spmd/go/ssa/lift.go:402` | MODIFY | `isLanesVaryingType` helper + lift guard (restored from v1/v2) |
| `x-tools-spmd/go/ssa/spmd_predicate.go` (in `spmdConvertLoopOps`, ~line 401) | MODIFY | Annotation walk over allocas in live scope blocks |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` + `TestSPMDVaryingAllocaLaneCountSet` |
| `tinygo/compiler/compiler.go` (`*ssa.Alloc` case, ~line 2746) | MODIFY | Annotation-aware type materialization |
| `tinygo/compiler/spmd_test.go` | MODIFY | Restore `TestSPMDVaryingLocalMaskedInTail` + add `TestSPMDVaryingAllocaLLVMType` |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (if §4.4 passes) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (if §4.5 passes) | — |
| `tinybench/BLOCKERS.md` | MODIFY or DELETE | Remove entries; delete file if empty |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`.

### 5.3 Risks

1. **Wrong lane count picked up.** If multiple SPMD loops in a function have different lane counts, the annotation might pick the wrong one. The `if alloc.SPMDLaneCount == 0` guard ensures only the first (outermost) loop sets the annotation. Edge case: an alloca in an outer non-SPMD scope used by an inner SPMD loop. The annotation walk over `liveScopeBlocks` should pick this up since the alloca's block becomes "live" via the inner loop's reach.

2. **Annotation visible in SSA dumps but no test ensures correctness across versions.** Mitigation: §4.2 test asserts the annotation is set; §4.3 asserts TinyGo reads it.

3. **mem2reg perf regression.** Wider allocas (e.g., `<32 x i32>` instead of `<4 x i32>`) may not lift cleanly. §4.7 benchmark sweep catches.

4. **Backward compatibility with existing TinyGo lowering.** The check `if expr.SPMDLaneCount > 0` falls back to the existing `getLLVMType` for unannotated allocas. No regression for non-SPMD code.

5. **Multiple allocations with conflicting lane counts in one function.** E.g., an outer loop with lane count 4 and an inner loop with lane count 32. Each loop's annotation walk only touches allocas in its own scope. No cross-contamination expected.

### 5.4 Success criteria

All must hold:

- `TestSPMDVaryingAllocaNotLifted` (x-tools-spmd): PASS.
- `TestSPMDVaryingAllocaLaneCountSet` (x-tools-spmd): PASS.
- `TestSPMDVaryingLocalMaskedInTail` (tinygo): PASS.
- `TestSPMDVaryingAllocaLLVMType` (tinygo): PASS.
- `test/e2e/spmd-e2e-test.sh`: no new failures vs baseline (94/93/0 unchanged or +tests with all pass).
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference.

### 5.5 Out of scope / deferred

- Refactoring `spmdMinLaneCount` to use this annotation pattern more broadly. Possible future cleanup.
- Composite types containing varying fields.
- Inferring lane count from referrers (fallback). Not needed because predication pass has direct access to the loop's lane count.
