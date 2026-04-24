# Varying-Local Mask Threading v2 — Design (Approach C1)

**Date**: 2026-04-23
**Scope**: Second attempt at the varying-local NaN-from-unmasked-writeback fix. The previous attempt (`docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md`) applied the lift guard alone and regressed 21 tests by exposing a latent TinyGo `spmdMaskedScatter` lane-count bug for sub-int varying types. This design adds a TinyGo-side safety net: **route alloca-backed varying stores to `spmdMaskedStore` (contiguous) instead of letting them fall through to the scatter dispatch.**

**Supersedes**: `2026-04-21-varying-local-mask-threading-design.md` (first attempt — reverted).

---

## 1. Overview, Scope, Architecture

### Goal

Properly fix the varying-local NaN-from-unmasked-writeback bug blocking `n-body` and `n-body-nosqrt` runtime correctness. Two-part change:

1. **SSA layer** (x-tools-spmd): re-apply the lift guard from the prior attempt — exclude `Varying[T]` allocas from `lift()` so their stores survive into the SPMD predication pass, where masks get threaded.

2. **TinyGo backend**: add an alloca-store routing safety net in `createSPMDStore`. When the SSA `Store.Addr` is an `*ssa.Alloc` whose element type is a SPMDType (a varying-typed local), bypass the scatter dispatch entirely and emit a contiguous masked-store. Varying-locals semantically have a SINGLE memory address (the alloca slot), not N distinct per-lane pointers — they should never go through `@llvm.masked.scatter`.

### Why this is more robust than the prior attempt

The prior attempt applied the lift guard only. That exposed a latent TinyGo scatter bug (`@llvm.masked.scatter.v32i8.v4p0` — 32-lane value vs 4-lane address) that broke 21 tests including `to-upper`. The bug is in `spmdMaskedScatter` (line 4938 of `tinygo/compiler/spmd.go`): derives the address vector lane count from `ptrs.Type().VectorSize()` (loop default = 4) instead of the value vector's natural width (32 for byte).

C1's alloca routing sidesteps this entire code path for alloca-backed stores — which is correct semantically (alloca = single address) and avoids touching the scatter path that's working today for genuine per-lane scatter cases (slice writes with varying indices).

### Concrete unlock

- `n-body-nosqrt/go-spmd/main.go` produces `-0.169075164` / `-0.169078071` (matches scalar reference) instead of NaN.
- `n-body/go-spmd/main.go` produces the same correct output (already calls `lanes.Sqrt` from the 2026-04-22 plan).

### Non-goals

- Fixing the underlying TinyGo `spmdMaskedScatter` lane-count bug for genuine scatter (slice writes with varying indices and sub-int values). Documented as a separate latent issue; tracked but not addressed here. If a regression surfaces in `to-upper` etc., we'll address that path specifically.
- Composite types containing varying fields.
- LLVM scatter performance optimization.
- New SSA opcodes or predication-pass changes beyond the lift exclusion.

### Invariants preserved

- Single explicit-mask model on `*ssa.SPMDStore` (established 2026-03-05).
- Genuine scatter path for slice writes / per-lane addresses unchanged.
- `Varying[*Struct]` field access (Cases A/B/C/D from 2026-04-21) unchanged.
- Stock-Go builds unaffected (lift guard is gated by SPMDType detection; alloca routing is conditional on SPMDType element).

---

## 2. SSA Layer (x-tools-spmd)

Identical to the previous attempt — re-apply the lift guard. One file, one function, one guard at the top.

### 2.1 The change

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, function `liftAlloc` (around line 402). Insert at the very top, before the existing `Recover` check:

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

    // ... existing liftability analysis unchanged ...
}
```

The helper `isLanesVaryingType` (also restored from the previous attempt):

```go
// isLanesVaryingType reports whether t is a lanes.Varying[T] type, either as
// *types.SPMDType (created by GOEXPERIMENT=spmd type-checker interception) or
// as *types.Named with Obj().Name()=="Varying" in package "lanes" (the raw
// generic instantiation stored on types.Var objects when GOEXPERIMENT is not
// active during type-check, which happens in the test environment).
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

### 2.2 Test

Restore `TestSPMDVaryingAllocaNotLifted` in `x-tools-spmd/go/ssa/spmd_lift_test.go` from the prior attempt. Verifies that a `Varying[T]` alloca survives lift — the `*ssa.Alloc` instruction remains in the function body.

### 2.3 Why this alone isn't enough this time

The previous attempt stopped here and broke 21 tests. The TinyGo side-effect needs §3's alloca-store routing as the safety net.

---

## 3. TinyGo Backend (Alloca-Store Routing)

The new piece. Adds detection in `createSPMDStore` for "Store with `*ssa.Alloc` address whose element type is varying" and routes to `spmdMaskedStore` instead of letting it fall through to the scatter dispatch.

### 3.1 The detection

**File**: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`, function `createSPMDStore` (around line 8606). Insert AFTER operand resolution (line 8645-8652 — `addr`, `val`, `mask`, `laneCount` are computed) but BEFORE the contiguous-path check (around line 8687):

```go
// SPMD alloca-store: when the store's address is an alloca holding a varying
// value (e.g., `var acc Varying[float64]; acc += x`), the address is a single
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
            // Reshape the value's bool element to i8 if needed (matches the
            // existing line 8700-8704 normalization).
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

### 3.2 Why this is correct

A `*ssa.Alloc` represents stack-local memory. Its address is a single LLVM `ptr` (scalar). A varying value stored there occupies the full alloca slot — sized to hold one vector. The store is conceptually:

```
store <N x T> %val, ptr %alloca, mask <N x i1> %mask
```

Which is exactly what `spmdMaskedStore` emits via `@llvm.masked.store.<ValueType>.p0`. The mask gets reshaped internally by `spmdConvertMaskFormat` to match the value's lane count — already-working machinery.

The scatter path (line 8732+) requires the address to be a vector of per-lane pointers. For an alloca, that doesn't apply.

### 3.3 What it doesn't touch

- **Slice writes** (`b[varying_i] = v`): `instr.Addr` is `*ssa.IndexAddr`, not `*ssa.Alloc`. The routing doesn't fire. Whatever path handles them today continues to handle them.
- **Genuine scatter** (varying pointer to distinct slots): `instr.Addr` is some form of computed pointer vector, not `*ssa.Alloc`. Untouched.
- **Field access on Varying[*Struct]** (Cases A/B/C/D from 2026-04-21): `instr.Addr` is from `FieldAddr`, not directly `*ssa.Alloc`. Untouched.

### 3.4 Edge case: alloca through casts

If the alloca is wrapped in a `ChangeType` or trivial cast before reaching the store, `instr.Addr.(*ssa.Alloc)` would return false and the routing wouldn't fire. For the n-body-nosqrt case this is fine — the predication pass produces direct `Store(alloca, val)` instructions for varying-local accumulators. If we discover a real-world case where an alloca address is wrapped, extend the detection to trace through wrappers. YAGNI for now.

### 3.5 No SSA-layer changes for this part

The detection happens entirely in TinyGo's lowering. SSA's `*ssa.Store` and `*ssa.SPMDStore` instructions are unchanged. The predication pass continues to convert `Store` → `SPMDStore` with mask uniformly; the routing decision is at codegen time based on what the address operand resolves to.

---

## 4. Testing

Three test layers + a regression sweep. The key new assertion is that `to-upper` (and other sub-int tests) MUST continue to pass — the safety net validation.

### 4.1 SSA unit test (x-tools-spmd)

Restore `TestSPMDVaryingAllocaNotLifted` in `x-tools-spmd/go/ssa/spmd_lift_test.go`. Same code as the prior attempt — verifies `Varying[T]` alloca survives lift. Currently fails until §2's lift guard lands; passes after.

### 4.2 TinyGo IR unit test — masked writeback

Restore `TestSPMDVaryingLocalMaskedInTail` in `tinygo/compiler/spmd_test.go`. Same code as the prior attempt — verifies a partial-mask `go for` over `Varying[float64]` accumulator emits `masked.store` or `select <4 x i1>` in the tail block. Tests the §2 + §3 combination.

### 4.3 TinyGo IR unit test — alloca-store routing

**File**: `tinygo/compiler/spmd_test.go`. Add:

```go
// TestSPMDVaryingAllocaStoreUsesMaskedStore verifies that a varying-typed
// alloca's writeback in a partial-mask go-for emits @llvm.masked.store
// (the contiguous path) rather than @llvm.masked.scatter. This catches
// regressions where the alloca-store routing in createSPMDStore is bypassed
// and the store falls through to the scatter dispatch (which would emit
// @llvm.masked.scatter with potentially mismatched lane counts).
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

Catches the specific contract: **alloca-store of varying value never produces a scatter intrinsic**.

### 4.4 End-to-end regression — n-body-nosqrt unblock

After both changes land, n-body-nosqrt should produce correct output:

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. Reference: `-0.169075164` / `-0.169078071`.

If clean: delete `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` and remove from `BLOCKERS.md`.

### 4.5 End-to-end regression — n-body unblock

Symmetric verification (same toolchain unblock; the `lanes.Sqrt` change already landed):

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff.

If clean: delete `tinybench/n-body/go-spmd/BLOCKER.md` (currently documents the third blocker — varying-local mask threading).

### 4.6 Regression sweep — to-upper et al

The critical safety check. The prior attempt regressed 21 tests. C1's alloca routing should prevent that, but verify explicitly:

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after.txt 2>&1
tail -10 /tmp/e2e-after.txt
```

Acceptance: same totals as baseline (94 compile / 93 run / 0 fail). No new FAIL entries.

If `to-upper` (or any other test) fails:
- The C1 hypothesis is wrong — slice-writes ARE going through the broken scatter path.
- STOP, do NOT delete BLOCKER.md, escalate back to design with C2 (narrow lift guard).

### 4.7 Benchmark sweep

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)" /tmp/bench-after.txt | head
```

Acceptance: each within ±10% of baseline. The lift guard increases memory traffic for varying locals (LLVM mem2reg should clean up main-body paths), so benchmarks like `lo-sum` / `lo-min` are most likely to drift if mem2reg fails.

If a benchmark drops >10%: investigate IR for the affected kernel. Most likely cause is mem2reg not lifting the now-memory-backed alloca in the all-ones-mask main body. Mitigation options if found:
- Tune the alloca lifetime (small surgical fix).
- Escalate to scope-the-lift-guard-to-functions-with-peeled-loops (larger design change).

---

## 5. Rollout, File Changes, Risks

### 5.1 Rollout order

1. SSA unit test first (§4.1) — red baseline at SSA layer. Commit.
2. TinyGo tail-mask IR test (§4.2) — red baseline at codegen. Commit.
3. TinyGo alloca-routing IR test (§4.3) — red baseline for the new routing contract. Commit.
4. SSA lift guard (§2.1) — lift exclusion lands. Passes §4.1; §4.2 and §4.3 still fail (or compile fail; at least one) until §5.
5. TinyGo alloca-store routing (§3.1) — lands in `createSPMDStore`. Passes §4.2 and §4.3.
6. Regression sweep (§4.6, §4.7) — must be clean. Explicit go/no-go gate for C1.
7. Tinybench n-body-nosqrt unblock (§4.4) — delete BLOCKER.md, update BLOCKERS.md. Commit.
8. Tinybench n-body unblock (§4.5) — delete BLOCKER.md, update BLOCKERS.md. Commit.
9. Parent SPMD repo: bump submodule pointers for `x-tools-spmd`, `tinygo`, `tinybench`.

Each step is independently revertible. If §6 regresses, revert §4 + §5 cleanly — back to baseline.

### 5.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDVaryingAllocaNotLifted` (restored from prior attempt) |
| `x-tools-spmd/go/ssa/lift.go:402` | MODIFY | Guard + `isLanesVaryingType` helper (restored) |
| `tinygo/compiler/spmd_test.go` | MODIFY | Restore `TestSPMDVaryingLocalMaskedInTail` + add new `TestSPMDVaryingAllocaStoreUsesMaskedStore` |
| `tinygo/compiler/spmd.go` (createSPMDStore, around line 8645-8687) | MODIFY | Alloca-store routing block (§3.1 — the NEW piece this round) |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (if §4.4 passes) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (if §4.5 passes) | — |
| `tinybench/BLOCKERS.md` | MODIFY or DELETE | Remove both entries; file itself deleted if empty |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`. `go` unchanged this round.

### 5.3 Risks

1. **C1 hypothesis is wrong — slice-writes trigger the scatter bug too.** Then `to-upper` et al regress again (same as prior attempt). The §4.6 regression sweep catches this before n-body BLOCKER deletion. Remediation: revert §4 + §5, escalate to C2 (narrow lift guard). Cost: one session lost; no code pollution since commits are surgical and revertible.

2. **Alloca-address trace through wrappers** (`ChangeType`, cast): if SSA puts a trivial wrapper between alloc and store, the `*ssa.Alloc` type assertion fails and routing doesn't fire. §4.3 test catches: it asserts the alloca store produces `masked.store`, not `masked.scatter`. If the test passes, the direct-alloca case works; if it fails, extend detection to trace through wrappers. Likely not needed — the predication pass produces direct Store(alloca, val) for compound assignments.

3. **mem2reg doesn't clean up main-body memory traffic** (same risk as prior attempt). §4.7 benchmark sweep catches. Remediation: tighten the lift guard scope or add surgical alloca-elimination passes.

4. **`Varying[*Struct]` field access paths disturbed** (Cases A/B/C/D). Paranoid check: those tests are in the E2E suite; §4.6 covers them. Expected zero impact since the alloca routing only fires for `*ssa.Alloc` addresses, and field-access addresses come from `FieldAddr` instructions.

5. **n-body float reassociation**: `reduce.Add` tree-reduces while scalar accumulates sequentially. If bit-different output in §4.5, the feature works but n-body has a separate float-reassociation blocker. Update BLOCKER.md with the new cause; don't delete.

### 5.4 Success criteria

All must hold:

- `TestSPMDVaryingAllocaNotLifted` (x-tools-spmd): PASS.
- `TestSPMDVaryingLocalMaskedInTail` (tinygo): PASS.
- `TestSPMDVaryingAllocaStoreUsesMaskedStore` (tinygo): PASS.
- `test/e2e/spmd-e2e-test.sh`: no new failures vs baseline (94/93/0 unchanged, or +tests with all pass).
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: output byte-identical to scalar reference.

### 5.5 Out of scope / deferred

- The underlying TinyGo `spmdMaskedScatter` lane-count bug (value.width vs ptrs.width mismatch). If a regression surfaces in a non-alloca scatter case, address it separately. C1's whole point is to defer this.
- Integer `Abs`/`Min`/`Max` and other `lanes.*` math primitives for integer types.
- Composite types with varying fields.
