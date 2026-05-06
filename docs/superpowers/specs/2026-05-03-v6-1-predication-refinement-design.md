# SPMD v6.1 — Predication-Pass Refinement Design

**Date**: 2026-05-03
**Status**: Design — pending user review
**Predecessors**: v6 Phase 2 Part B (commits `2dd8d1357` x-tools-spmd, `dda72362` tinygo)
**Goal**: Restore e2e to 105/94/0/93/0/11 baseline by refining the SSA predication pass and TinyGo lowering, without reverting the v5 lift guard.

## 1. Problem statement

After v6 Phase 2 Part B (re-applying v5's SSA lift guard + width-typed predication), 4 e2e tests regress:

| Test | Symptom | Root cause family |
|---|---|---|
| `ipv4-parser` | Compile/runtime: mask width mismatch | Operation derives natural width (8 for byte) when it should match loop iter width (4) |
| `varying-array-iteration` | Wrong values | Pass A over-fixes alloca for value loaded from `[]Varying[T]` to Lanes=1 |
| `array-counting` | All slots same value | Bucket-G's `UnOp{MUL}` canonical-trace makes per-iteration scalar store look contiguous-vector |
| `pointer-varying` | Lane-0 only behavior | Same `UnOp{MUL}` over-eager trace breaks per-lane scatter/gather addressing |

The shared dysfunction: **the predication pass conflates "loop-derived" with "stored in alloca during loop"**. It width-fixes every `*Varying[T]` alloca to `loop.LaneCount`, even when the alloca's data is loaded from external memory whose natural width is independent of the loop iter.

## 2. Guiding principles

### 2.1 Width principle

Inside an SPMD loop body, a Varying value's effective width = the loop's iter element width — EXCEPT for values loaded directly from `[]Varying[T]` slice elements (where the slice element IS the vector and keeps its natural width).

### 2.2 Mask principle

The mask propagated to operations inside the loop body matches the loop iter width. Operations consuming the mask should not need width transformation. If a mismatch occurs, the operation's width was derived wrong — the bug lives at the derivation site, not in mask reshape logic.

### 2.3 Contiguity principle

An `IndexAddr` is contiguous IFF the address truly walks consecutive elements at the operation's element stride. Tracing through a `*Varying[T]` alloca breaks this — the loaded value is per-lane data, not a contiguous range.

## 3. Architecture: responsibility split

| Layer | Owns | Invariant |
|---|---|---|
| **x-tools-spmd predication pass** | Setting `types.SPMDType.Lanes()` on Varying values | A Varying value's `Lanes()` equals the width its data ACTUALLY occupies in LLVM. Set to `loop.LaneCount` for loop-local values; left abstract (Lanes()==0) for external values |
| **TinyGo backend** | Lowering SSA → LLVM with correct codegen | Trust the SSA types: `getLLVMType(Varying[T] with Lanes=N)` → `<N x T>`. Lanes()==0 → natural width via existing `spmdEffectiveLaneCount`. Contiguous detection must NOT trace through `*Varying[T]` allocas |

**Single source of truth**: the SSA type. No active-loop tracking duplicated in TinyGo; no width-computation drift.

## 4. x-tools-spmd predication pass refinement

**Change site**: `x-tools-spmd/go/ssa/spmd_predicate.go` Pass A (currently lines ~462-476 of `spmdConvertLoopOps`).

### 4.1 Today

Pass A walks every `*Alloc` in the function whose pointee is `*Varying[T]` (Lanes()==0) and width-fixes it to `loop.LaneCount` unconditionally. Then propagates via SPMDLoad/FieldAddr referrers (lines 478-521).

### 4.2 Change

Add a classification step before width-fixing each alloca. The alloca gets width-fixed (`Lanes = loop.LaneCount`) ONLY when its data is **loop-local-derived**. Otherwise, leave Lanes()==0 (TinyGo will use natural width).

### 4.3 Loop-local classifier

`spmdAllocaIsLoopLocal(alloc *Alloc, loop *spmdLoopInfo) bool` — examine every `*ssa.Store` and `*ssa.SPMDStore` whose `Addr == alloc`. The alloca is **loop-local** if EVERY stored value satisfies one of:

- `*SPMDIndex` — the iter lane vector
- `*ssa.Const` — uniform compile-time constant (e.g., zero-init)
- `*ChangeType` / `*ssa.Convert` of a value classified loop-local
- `*ssa.BinOp` / `*ssa.UnOp` (other than `UnOp{token.MUL}` which dereferences) whose all operands are classified loop-local
- `*ssa.Phi` whose all incoming values are classified loop-local (terminate cycles via visited set; revisits treat as loop-local — phi cycles within the loop body are loop-local by construction)
- `*SPMDSelect` whose Cond, X, Y are all classified loop-local
- `*SPMDLoad` whose Addr is itself a `*ssa.Alloc` already classified loop-local (gather of values produced inside the loop)
- The loop's iter phi (`loop.IterPhi`) directly

Otherwise — parameter, function call result, `*ssa.UnOp{MUL}` (pointer dereference of external memory), `*SPMDLoad` from a non-alloca address such as `*ssa.IndexAddr` of a slice / `*ssa.FieldAddr` of a struct, anything reaching outside the function — the alloca is **external**: leave Lanes()==0.

Conservative default: any value not matching a loop-local case is treated as external.

### 4.4 Termination

The classifier walks SSA def-use chains backward, terminating at:
- A "loop-local" leaf (yes)
- An "external" leaf (no, short-circuit)
- A revisit (visited set; treat as loop-local since cycles inside the loop are loop-local)

Bounded recursion depth via a visited set keyed on `Value` identity.

### 4.5 Edge cases

- **Bucket-G sentinel tests** (`L0_cond`, `L4b_varying_break`, `bit-counting`, `printf-verbs`): all use loop-local accumulators (BinOps + iter + constants). Classifier returns true → Lanes=loop.LaneCount → unchanged behavior. ✓
- **n-body**: per-pair accumulators (dvx/dvy/dvz/ej allocas) initialized from arithmetic of iter-derived values → loop-local → unchanged. ✓
- **`[]Varying[T]` element load** (varying-array-iteration): the alloca's Store is `SPMDLoad(IndexAddr(slice, iter))`. The IndexAddr's slice operand is a function parameter or external — classifier walks back, hits parameter → external. Alloca stays Lanes()==0. ✓
- **Nested SPMD function call** receiving a Varying parameter: parameter is a leaf at the boundary; classified external (its width comes from caller, not local loop). ✓

## 5. TinyGo backend refinement

### 5.1 Change 1: revert bucket-G `UnOp{MUL}` extension

In `tinygo/compiler/spmd.go` `spmdCanonicalSSAIndex` (line ~5120), drop the `*ssa.UnOp` case (lines ~5142-5150 added during bucket-G). Keep the original `*ssa.SPMDLoad`, `*ssa.ChangeType`, and the `*ssa.Convert` (added for swizzle-within) cases.

The `UnOp{MUL}` extension reached into per-lane gather/scatter patterns (array-counting, pointer-varying) and made them look contiguous when they aren't.

### 5.2 Change 2: revert mask-helper switches

Restore the original raw `CreateTrunc(mask, llvm.VectorType(b.ctx.Int1Type(), laneCount), "spmd.idx.mask")` at:
- `tinygo/compiler/spmd.go:6031` (spmdSpmdVectorOffset clamp)
- `tinygo/compiler/spmd.go:6164` (spmdVectorIndexString clamp)
- `tinygo/compiler/spmd.go:6267` (spmdVectorIndexArray clamp)
- `tinygo/compiler/compiler.go:3465` (varying-index IndexAddr clamp)

Today these use `spmdUnwrapMaskForIntrinsic` which silently reshapes mismatched widths via `spmdConvertMaskFormat`. Per Section 2.2, mismatches must be fixed at the source. With the Width principle enforced, mask and operation widths match — `CreateTrunc` works directly.

### 5.3 Change 3: keep getLLVMType as-is

`getLLVMType` for `*types.SPMDType` already reads `typ.Lanes()` when set, falls back to `spmdEffectiveLaneCount` (natural width via existing logic) when Lanes()==0. This is exactly what Pass A's refinement needs:
- Loop-local alloca → Pass A sets Lanes=N → getLLVMType returns `<N x T>`
- External alloca → Pass A leaves Lanes()==0 → getLLVMType returns natural width `<M x T>`

### 5.4 Change 4: keep Phase 1 Part A and swizzle-within Convert trace

`spmdReshapeVector` defensive scatter fix (Phase 1 Part A) and the `*ssa.Convert` case in `spmdCanonicalSSAIndex` (today's swizzle-within fix) remain. Both correct beyond v6.1's scope.

### 5.5 Change 5: remove reduce-builtin `scalarInput` shortcut

Remove the `scalarInput` early-fallback path in `createReduceBuiltin` added today as a band-aid. With Pass A correctly classifying allocas, `reduce.Add(varyingData)` where `varyingData` is an external `[]Varying[int]` element load produces a true 4-lane vector — the reduction works on a vector by design.

## 6. Per-test trace

### 6.1 ipv4-parser
- **Today**: 4-wide loop, `Varying[byte]` index computed at natural width 8 → mask `<4 x i32>` vs index `<8 x i32>` mismatch → invalid `trunc`.
- **After**: Pass A classifies the byte-index alloca as **loop-local** (built from iter+arithmetic) → Lanes=4. `getLLVMType` returns `<4 x i8>`. Mask is `<4 x i32>`. `CreateTrunc` works (4 → 4). No reshape needed. ✓

### 6.2 varying-array-iteration
- **Today**: 1-lane outer loop, `varyingData` alloca loaded from `[]Varying[int]` slice. Pass A width-fixes to Lanes=1 → reduce.Add operates on scalar.
- **After**: Pass A classifies the alloca as **external** (Store is `SPMDLoad(IndexAddr(slice, iter))`; slice origin traces back to function parameter → external) → Lanes()==0. `getLLVMType` returns natural width `<4 x int>`. reduce.Add operates on a true 4-lane vector. ✓

### 6.3 array-counting
- **Today**: 1-lane outer loop, uniform `t int` accumulator. Bucket-G's `UnOp{MUL}` trace makes `result[i] = t` look contiguous-vector → broadcast write → all 4 slots get same value.
- **After**: `UnOp{MUL}` extension removed from `spmdCanonicalSSAIndex`. `result[i]` falls through to per-iteration scalar GEP+store. 4 iterations → 4 distinct writes. ✓
- **Risk**: removal may re-surface the original Part B compile-fail (`store <4 x i32>, <4 x ptr>`). See contingency in Section 8.

### 6.4 pointer-varying (checkGather, checkPointerArithmetic)
- **Today**: bucket-G's `UnOp{MUL}` trace fires for `&targets[i]` and `&data[i]` patterns, making per-lane scatter/gather look contiguous → all 4 lanes write to data[0] (last wins) or read from targets[0] (broadcast).
- **After**: `UnOp{MUL}` removal → IndexAddr with iter-derived varying address falls through to true scatter/gather path. Per-lane addresses preserved. ✓

### 6.5 Critical sentinel: n-body
- The original Part B fix was for n-body: per-pair accumulators in the inner loop need width-fixed alloca + masked stores.
- Pass A still classifies these as **loop-local** (initialized from BinOps of iter-derived float values) → Lanes=loop.LaneCount. n-body keeps working. ✓

## 7. Testing & validation strategy

### 7.1 Per-step verification
After each implementation step, run:
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi \
  -o /tmp/<test>.wasm test/integration/spmd/<test>/main.go && wasmtime /tmp/<test>.wasm
```

Expected progression:
1. **After Pass A refinement** (x-tools-spmd): `varying-array-iteration` produces correct values; `ipv4-parser` may still fail on mask trunc until step 3
2. **After TinyGo `UnOp{MUL}` revert**: `array-counting`, `pointer-varying` produce correct output
3. **After TinyGo mask-helper revert**: `ipv4-parser` produces correct output
4. **After reduce-builtin scalar-shortcut cleanup**: no regression

### 7.2 Sentinel checks at every step
- **n-body**: `cd tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go && /tmp/nb-spmd-bin 50000` — must produce `-0.169075164 / -0.169078071`. Run after EVERY change.
- **Bucket-G tests**: `bash test/e2e/spmd-e2e-test.sh | grep -E "L0_cond|L4b_varying_break|bit-counting|printf-verbs"` — must keep passing.

### 7.3 Final gate
- **E2E**: `bash test/e2e/spmd-e2e-test.sh` → must reach **105/94/0/93/0/11** (baseline parity).
- **Unit tests**: `cd tinygo && go test ./compiler/...` and `cd x-tools-spmd && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/...` → no new failures vs Part B baseline.
- **Phase 4 benchmarks** begin once gate is green.

### 7.4 Rollback policy
If a sentinel breaks at any step:
1. Revert the latest change (don't pile fixes on a broken base)
2. Re-run sentinel to confirm restoration
3. Investigate root cause before re-attempting
4. Document deviation inline in the design doc

## 8. Known risks & contingencies

### 8.1 array-counting compile-fail re-surfaces
**Risk**: Removing the `UnOp{MUL}` trace may re-introduce the original Part B `store <4 x i32>, <4 x ptr>` compile failure for array-counting.

**Contingency**: Scope contiguous detection to exclude **1-lane outer loops iterating over slices-of-aggregates** (the array-counting / nested loop pattern). Specifically, in `spmdAnalyzeContiguousIndex`, return false when:
- The current loop is the innermost and has `loop.LaneCount == 1`
- AND the loop iterates over a slice whose element type is itself an aggregate (slice, array, struct)

Implement only if the checkpoint after Step 2 (Section 7.1) shows the regression. If implemented, add an SSA test that demonstrates the contiguous detection skip.

### 8.2 Pass A classifier misclassifies edge cases
**Risk**: A loop-local pattern might be mistakenly classified external (or vice-versa), causing a different test to regress.

**Mitigation**:
- Run full e2e (105 tests) at every step, not just the 4 target tests
- The classifier defaults conservatively: when in doubt, leave abstract (Lanes==0). TinyGo's natural-width fallback is correct in more cases than width-fixing-to-1.

### 8.3 Slice-origin tracing depth
**Risk**: For complex SSA shapes (e.g., a slice obtained from a struct field of a function parameter), the classifier might fail to recognize "external" without traversing many layers.

**Mitigation**: The classifier's "external when in doubt" default means a missed external classification (over-classifying as loop-local) is the dangerous direction; under-classifying as external just means natural width, which is safe per Section 5.3.

## 9. Out of scope

- Changes to `*types.SPMDType.Lanes()` API surface (already adequate from v5 commit `6523f50aa9`)
- Predication pass changes outside Pass A (Pass B and downstream behavior unchanged)
- The reduce-builtin scalar-fallback for `-simd=false` mode (existing path retained; only the new `scalarInput` shortcut is removed)
- Performance optimization beyond restoring baseline functionality
- AVX2 / x86-specific lane-count handling (the principles apply uniformly across targets)

## 10. Success criteria

- ✅ All 4 target tests (ipv4-parser, varying-array-iteration, array-counting, pointer-varying) PASS
- ✅ n-body produces `-0.169075164 / -0.169078071` (NOT NaN)
- ✅ E2E summary: 105/94/0/93/0/11 (baseline parity)
- ✅ No regression in unit tests (TinyGo compiler tests + x-tools-spmd SSA tests)
- ✅ Bucket-G sentinel tests still pass
