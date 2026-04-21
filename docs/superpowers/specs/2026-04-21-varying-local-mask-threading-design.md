# Varying-Local Mask Threading — Design

**Date**: 2026-04-21
**Scope**: Fix a compiler correctness bug in the SPMD fork where partial-mask `go for` iterations don't thread the active lane mask into compound-assignment writebacks on varying-local accumulators. Unblocks the `n-body-nosqrt` tinybench port.

---

## 1. Overview & Scope

### Goal

Exclude `Varying[T]` allocas from the generic SSA `lift()` pass so their `Store` instructions survive to the SPMD predication pass. There they are converted to `*ssa.SPMDStore` with the correct active mask (main body: all-ones; tail body: the peel mask from `loop.TailMask`). TinyGo's existing scatter/contiguous lowering emits masked stores for partial-mask blocks, preventing inactive-lane NaN/Inf from leaking into varying accumulators.

### Concrete unlock

`tinybench/n-body-nosqrt/go-spmd/main.go` currently compiles and runs but produces `NaN NaN` for args=50000. Root cause (confirmed via probe isolation, 2026-04-21 diagnostic):

- 5 bodies on 4-wide AVX2 f64 → inner pair loop has 4/3/2/1 iterations.
- Partial-mask iterations: inactive lanes compute `0 * Inf = NaN`.
- `acc += expr` writeback is NOT masked — NaN writes into `acc[inactive_lane]`.
- `reduce.Add(acc)` faithfully propagates the NaN to the scalar energy.

After this fix: the writeback is masked, inactive lanes retain their prior value, `reduce.Add` sees a clean vector, output matches the scalar reference `-0.169075164` / `-0.169078071`.

### Non-goals

- **Composite types containing varying fields** (e.g., `struct { x Varying[float64] }`). The struct alloca is uniform; field access already goes through SPMD-aware paths. Not triggered by any current port.
- **Varying allocas via embedding / interfaces**. Out of scope.
- **Back-end LLVM changes**. The existing SPMDStore lowering (commits `361aea9` x-tools-spmd + `84b6d0c` tinygo) already handles everything.
- **Other SSA passes beyond `lift()`**. The diagnostic surfaced `lift()` specifically; other passes (DCE, inlining, etc.) don't drop the stores in a way that matters for this bug.
- **`lanes.Sqrt`**. Separate brainstorming cycle; unblocks `n-body` (different benchmark, different blocker).

### Invariants preserved

- **Single-explicit-mask model** on `*ssa.SPMDStore` (established 2026-03-05). No new SSA opcodes, no new predication pass, no interaction between lift and peeling.
- **Non-SPMD builds unaffected**. The guard uses `*types.SPMDType` assertion, which never succeeds outside the forked type checker.
- **Existing scatter/gather tests** (2026-04-21 Varying[*T] work) continue to pass.

---

## 2. Detection & Implementation

### 2.1 The change

One file, one function, one guard.

**File**: `x-tools-spmd/go/ssa/lift.go`, function `liftAlloc` (around line 402). At the very top, before any other check:

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

    // ... existing liftability analysis unchanged ...
}
```

Not gated behind `buildcfg.Experiment.SPMD`. `*types.SPMDType` only exists when the forked type checker creates it; in stock builds the assertion never succeeds, so behavior is unchanged.

### 2.2 Why the type check is sufficient

A varying-typed alloca has Go type `Varying[T]`. The SSA builder represents it as `*Alloc` with `Type() = *types.Pointer` to `*types.SPMDType`. No other type shape produces an SPMDType element. The assertion catches exactly the intended set.

Composite types with SPMDType fields (e.g., `struct { x Varying[float64] }`) don't trigger — the alloca's element is the struct, not the SPMDType. The struct alloca can still be lifted; field access through the struct pointer goes through FieldAddr which is already SPMD-aware (Cases A/B/C/D from 2026-04-21).

### 2.3 Downstream flow after the change

For a varying accumulator `acc` inside a partial-mask go-for:

1. `*ssa.Alloc` at function entry — unchanged.
2. Initial `Store` of zero before/at loop entry — survives lift.
3. `+=` inside loop body — lowers to `Load + BinOp + Store`; all three survive lift.
4. SPMD predication pass (`spmdConvertScopedMemOps` in `x-tools-spmd/go/ssa/spmd_predicate.go`):
   - Main-body blocks: walks with `allOnesMask`, converts `Store` → `SPMDStore` with all-ones.
   - Tail-body blocks: walks with `loop.TailMask`, converts `Store` → `SPMDStore` with the peel mask.
5. TinyGo `createSPMDStore`:
   - All-ones mask: plain `store <N x T>` (existing fast path).
   - Partial mask: `@llvm.masked.store` or load-select-store blend. Inactive lanes retain pre-store value.
6. `Load`s of the alloca: same SPMD-aware path via `SPMDLoad`.

LLVM's `mem2reg` + `SROA` collapse the memory traffic for main-body blocks (all-ones mask paths) back to register-level, so the performance of hot paths is unaffected. Tail-body blocks retain the masked-store pattern because it's semantically required.

### 2.4 Outside-scope stores

Writes to the same alloca OUTSIDE the SPMD scope (e.g., the initial `var acc Varying[float64]` zero assignment, or assignments in enclosing regular `for` loops) remain as plain `*ssa.Store`. They flow through TinyGo's non-SPMD `*ssa.Store` handler and emit plain unmasked LLVM stores. Correct — no ambient SPMD mask applies there.

---

## 3. Testing

Three layers plus full regression sweeps.

### 3.1 SSA unit test (x-tools-spmd)

**File**: `x-tools-spmd/go/ssa/spmd_lift_test.go` (new or extension of existing).

```go
// TestSPMDVaryingAllocaNotLifted verifies that a Varying[T] alloca
// survives the SSA lift() pass — its Alloc, Store, and Load instructions
// remain in the function body so the SPMD predication pass can thread
// the active mask into them.
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
        t.Fatal("varying alloca was lifted; expected memory-backed *ssa.Alloc in function body")
    }
    if !gotSPMDStore {
        t.Fatal("no *ssa.SPMDStore found; predication pass didn't see the surviving Store")
    }
}
```

### 3.2 TinyGo LLVM IR test

**File**: `tinygo/compiler/spmd_test.go`.

```go
// TestSPMDVaryingLocalMaskedInTail verifies that a varying-local
// compound assignment in a partial-mask go-for gets masked in the
// tail-body block, preventing inactive-lane NaN leaks.
func TestSPMDVaryingLocalMaskedInTail(t *testing.T) {
    // 5 iterations on 4-wide SIMD => main=4 iters, tail=1 iter (lanes 1-3 inactive).
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

    // Tail body store must use the mask — either masked.store or a
    // load-select-store blend with a <4 x i1> select.
    mustContainAny(t, ir, "masked.store", "select <4 x i1>")
}
```

Without the fix, lift would eliminate the store, `acc` becomes a phi, and neither pattern appears. With the fix, at least one of them must show up.

### 3.3 End-to-end regression — n-body-nosqrt

After the fix lands:

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff (scalar reference: `-0.169075164` / `-0.169078071`).

If match: delete `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` and remove its entry from `tinybench/BLOCKERS.md`. If float reassociation produces last-digit mismatch, that's a different cause; preserve the BLOCKER with updated content.

### 3.4 Negative-path regression (Varying[*T] feature)

The 2026-04-21 pointer-varying tests must still pass:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDPointerVarying|TestSPMDVaryingPointer' -count=1 -timeout=60s

cd ../tinygo
GOEXPERIMENT=spmd GOTESTFLAGS="-run TestSPMDVaryingPointerFieldAddr -v" \
  GOTESTPKGS="./compiler" GO=../go/bin/go make -f GNUmakefile test
```

Expected: all PASS.

### 3.5 Benchmark regression verification

`mem2reg` should clean up main-body memory traffic, but that's a claim to verify, not assume.

**3.5.1 Correctness sweep** (fast, catches logic regressions):

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```

Expected: `102 total, 90 run-pass, 91 compile-pass, 0 compile-fail, 0 run-fail, 11 reject OK` — the current baseline from project memory. Any change in these totals is a real regression.

**3.5.2 Performance sweep** (slower, catches optimization regressions):

```bash
bash test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-after.txt
```

Baselines from project memory (AVX2 8-wide vs scalar SPMD):

| Benchmark | Baseline ratio |
|---|---|
| `lo-min` | 7.27x |
| `lo-max` | 7.18x |
| `lo-sum` | 5.09x |
| `lo-clamp` | 4.82x |
| `lo-mean` | 3.66x |
| `mandelbrot` | 6.07x |
| `hex-encode` dst | 13.01x |

Acceptance: each number within **±10%** of the baseline. A 10% band accounts for run-to-run noise; deviations beyond that indicate a real regression.

**3.5.3 Base64 Mula-Lemire spot check**:

```bash
bash test/e2e/spmd-benchmark.sh base64-mula-lemire 2>&1 | tail -10
```

Expected throughput: ~17 GB/s AVX2. Deviation >10% is a regression signal.

If the performance sweep regresses >10% on any benchmark, the likely cause is LLVM failing to fully lift a main-body alloca. Remediation options (in increasing invasiveness):

1. Investigate the generated IR for the affected kernel; confirm `mem2reg`/`SROA` ran.
2. Tighten the detection rule: only keep alloca memory-backed when the enclosing function contains a peeled loop (i.e., a partial-mask context exists). This is a larger design change and escalates back to brainstorming.

---

## 4. Rollout, File Changes, Risks

### 4.1 Rollout order

1. SSA unit test first (§3.1) — red baseline at SSA layer. Commit.
2. TinyGo LLVM IR test (§3.2) — red baseline at codegen layer. Commit.
3. Apply the lift guard (§2.1) — one-line change in `lift.go`. Both tests go green. Commit.
4. Regression sweep (§3.3, §3.4, §3.5) — must be clean.
5. If §3.3 passes: delete `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` and update `BLOCKERS.md`. Commit in tinybench submodule.
6. Parent SPMD repo: bump submodule pointers (x-tools-spmd + tinygo + tinybench).

### 4.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW or EXTEND | `TestSPMDVaryingAllocaNotLifted` |
| `x-tools-spmd/go/ssa/lift.go` (~line 402) | MODIFY | Guard in `liftAlloc` |
| `tinygo/compiler/spmd_test.go` | MODIFY | `TestSPMDVaryingLocalMaskedInTail` |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (if §3.3 passes) | — |
| `tinybench/BLOCKERS.md` | MODIFY | Remove n-body-nosqrt entry |

All on existing `spmd` branches. Parent SPMD repo gets a submodule-pointer-bump commit at the end.

### 4.3 Risks

1. **LLVM `mem2reg` fails to fully clean up main-body memory traffic.** The §3.5 benchmark sweep catches this. Remediation: tighten the detection rule as described in §3.5. Cost: a larger design change that escalates back to brainstorming.

2. **Existing tests depending on lifted varying allocas regress.** Unlikely but possible. §3.4 catches any. Remediation: case-by-case — either the test was wrong, or the exclusion rule needs narrowing.

3. **Base64 Mula-Lemire performance drop.** Highest-optimization-pressure benchmark. §3.5.3 catches. This benchmark has no tail-body (cascading `go for` reductions are all main-body), so SPMDStore all-ones → plain store → `mem2reg`. Low risk, but verified because cost-of-failure is high.

4. **Silent over-broadening of exclusion.** The guard uses direct SPMDType element check. Types like `*Varying[T]` or `Varying[*T]` as alloca element ALSO match (correctly — they're SPMDType at the element level). No regression risk; conservative and correct.

5. **n-body-nosqrt produces non-identical output.** E.g., float reassociation in `reduce.Add` vs scalar sequential sum. If diff shows last-digit differences, the compiler fix is complete but the benchmark blocker has a new cause. Update BLOCKER.md accordingly (don't delete).

### 4.4 Success criteria

All five must hold:

- `go test ./go/ssa -run TestSPMDVaryingAllocaNotLifted` in x-tools-spmd: PASS.
- TinyGo `TestSPMDVaryingLocalMaskedInTail` (with `GOEXPERIMENT=spmd`): PASS.
- `test/e2e/spmd-e2e-test.sh` — 102 total, no new failures (correctness preserved).
- `test/e2e/spmd-benchmark-x86.sh` — all measurements within ±10% of baselines (performance preserved).
- `diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)` — empty (n-body-nosqrt E2E unblocks).

### 4.5 Out of scope / deferred

- `lanes.Sqrt` builtin — separate brainstorming cycle; unblocks n-body.
- Composite types containing varying fields.
- Back-end performance investigation if benchmarks regress (would escalate design).
- General alloca scheduler changes.
