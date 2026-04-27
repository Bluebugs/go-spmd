# SPMD Canonical Lane Count v4 — Design (Block Annotation + Per-Call Specialization)

**Date**: 2026-04-26
**Scope**: Fourth attempt at fixing the varying-local NaN-from-unmasked-writeback bug blocking `n-body` and `n-body-nosqrt`. Also addresses the broader "lane count derivation inconsistency" between TinyGo's element-natural-width assumptions and the SPMD predication pass's per-loop lane counts. Adopts an ISPC-style canonical-width model, with block annotations as the carrier and per-call specialization for SPMD function bodies.

**Supersedes**:
- `2026-04-21-varying-local-mask-threading-design.md` (v1 — reverted)
- `2026-04-23-varying-local-mask-threading-v2-design.md` (v2 — reverted)
- `2026-04-25-varying-local-mask-threading-v3-design.md` (v3 — attempted, GATE failed at 23 broken tests, reverted)

---

## 1. Overview, Scope, Architecture

### Goal

Adopt ISPC's canonical-width model for SPMD compilation: every `Varying[T]` value's lane count is determined by the surrounding SPMD scope (loop or specialized function variant), not by `T`'s element-natural register width. This eliminates the lane-count derivation mismatches that caused v3's GATE failure (alloca correctly sized, but downstream type-derivation paths still used element-natural width, producing 23 cascading test failures).

### Diagnosis from v3 attempt 1

The v3 SSA annotation correctly tagged the `Varying[int]` alloca in a `[]float64` go-for as `<2 x i32>` (matching the loop's 2-lane iteration width on WASM128, instead of int's natural `<4 x i32>`). TinyGo was patched at four sites to honor the annotation: `*ssa.Alloc` materialization, `createSPMDLoad`, `createSPMDStore`, and `*ssa.UnOp{MUL}` for plain dereferences. But many MORE sites in TinyGo derive lane count from element-natural width via `spmdLaneCount(elemType)`: `IndexAddr`, swizzle/gather helpers, reduce dispatch, broadcast, mask materialization, etc. Each unpatched site produced cascading width mismatches that LLVM verifier rejected ("Invalid type", "Invalid cast", "Store operand must be a pointer", "scalarize this operator's operand") or caused runtime panics ("index out of range", wrong results in `lo-contains`).

Concretely: `to-upper` iterates `[]byte` (32 lanes on AVX2). The loop index `i` is `Varying[int]`; under v3 the alloca for `i` would be `<32 x i32>` (loop width), but `IndexAddr(b, i)` derived its address-vector width from int-natural (8 lanes on AVX2), producing `<8 x ptr>` against a `<32 x i8>` value vector. Same family of bugs across `lo-clamp`, `lo-contains`, `integ_array-counting`, etc.

Patching each site reactively is the v1/v2 anti-pattern. The right fix is structural: TinyGo's lane-count derivations must consult ONE source of truth (the surrounding SPMD scope), not derive from element type.

### v4 fix (three-layer)

**Layer 1: SSA block annotation.** New field `*ssa.BasicBlock.SPMDLaneCount int` (0 = not in SPMD scope). The predication pass sets it for every block in a `go for` loop's scope to `loop.LaneCount`. This is the canonical lane count for any operation in that block.

**Layer 2: Per-call specialization.** When an SPMD function (one with `Varying[T]` params/results) is called from blocks with different lane counts, the SSA-level specialization pass clones the function's SSA per unique caller lane count, producing variants `f.spmd2`, `f.spmd4`, `f.spmd8`, etc. Each variant's blocks all have `SPMDLaneCount` set to the variant's lane count. Call sites are rewritten to dispatch to the right variant. Symmetric with TinyGo's existing generic monomorphization.

**Layer 3: TinyGo block-annotation-aware materialization.** TinyGo reads `bb.SPMDLaneCount` (the annotation on the block currently being emitted) for ALL `Varying[T]` lane-count derivations: type materialization (`getLLVMType(*types.SPMDType)`), alloca sizing (`*ssa.Alloc`), index/gather/swizzle widths, reduce dispatch, mask widths. When the block annotation is 0 (non-SPMD code), fall back to existing `spmdLaneCount(elemType)` and `spmdMinLaneCountForSig` derivations.

### Concrete unlock

- `n-body-nosqrt/go-spmd/main.go` produces `-0.169075164` / `-0.169078071` instead of NaN.
- `n-body/go-spmd/main.go` produces the same correct output.
- `to-upper`, `lo-clamp`, `lo-contains`, `integ_array-counting`, etc. continue to pass — block annotation makes ALL in-loop derivations use the loop's lane count, including the `IndexAddr` and `<N x ptr>` paths that broke v3.

### Non-goals

- Multi-target lane-count adaptation per call site (e.g., calling a Varying-param function from both an AVX2 8-wide context and a SSE 4-wide context within the same compilation unit). Specialization assumes one canonical width per call site, set at SSA construction time.
- Removing `spmdMinLaneCountForSig` and `spmdLaneCount(elemType)` entirely. They remain as fallbacks for non-annotated blocks (e.g., main()'s entry block before the first `go for`).
- Composite types containing varying fields.
- Function-body lane count negotiation across modules (treat one module per compilation).

### Invariants preserved

- Single explicit-mask model on `*ssa.SPMDStore` (established 2026-03-05).
- Genuine scatter / contiguous / field-access paths unchanged.
- `Varying[*Struct]` field access (Cases A/B/C/D from 2026-04-21) unchanged.
- Stock-Go builds unaffected (annotation field has zero impact when not set).
- Backward compatibility: existing SSA without block annotations falls back to current TinyGo derivations.

---

## 2. SSA Layer — Block Annotation + Predication

### 2.1 The struct change

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go` (BasicBlock struct).

Extend `*BasicBlock`:

```go
type BasicBlock struct {
    Index    int
    Comment  string
    parent   *Function
    Instrs   []Instruction
    Preds    []*BasicBlock
    Succs    []*BasicBlock
    Dominees []*BasicBlock
    succs2   [2]*BasicBlock
    dom      domInfo
    gaps     int
    rundefers int

    // SPMDLaneCount, when non-zero, is the canonical lane count for every
    // *types.SPMDType value materialized inside this block. Set by the SPMD
    // predication pass for blocks in a `go for` loop's scope (=
    // loop.LaneCount), by the specialization pass for blocks in a
    // specialized SPMD function variant, and by the forward-propagation
    // pass for entry-block allocas consumed by in-loop SPMD ops. TinyGo
    // reads this field during type materialization, alloca sizing, and
    // every other lane-count derivation; when 0, TinyGo falls back to its
    // existing element-natural / function-min derivations.
    SPMDLaneCount int
}
```

The field is exported so TinyGo can read it from outside the package.

### 2.2 The lift guard (restored from v1/v2/v3)

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, `liftAlloc`.

Same guard as previous attempts — exclude `Varying[T]` allocas from `lift()` so the predication/specialization/propagation passes can see them and the surrounding block's annotation governs their materialization:

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
    // ... existing body unchanged ...
}
```

The `isLanesVaryingType` helper (handles both `*types.SPMDType` production representation and `*types.Named` test-mode representation) and the `export_spmd_test.go` test re-export are restored from v3.

### 2.3 Predication pass — block annotation

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, in `spmdConvertLoopOps`.

After the per-loop `liveScopeBlocks` is computed and the empty-check guard, walk those blocks and set their annotation:

```go
// SPMD v4: annotate every in-scope block with the loop's canonical lane
// count. TinyGo reads this annotation for ALL lane-count derivations
// inside the block (type materialization, alloca sizing, gather/scatter
// widths, reduce dispatch, mask widths). The check is "annotation already
// set?" rather than "alloca-by-alloca" — the block annotation is the
// canonical source.
for b := range liveScopeBlocks {
    if b.SPMDLaneCount == 0 {
        b.SPMDLaneCount = loop.LaneCount
    }
}
```

The `if b.SPMDLaneCount == 0` guard prevents inner-loop predication from clobbering an outer loop's annotation when blocks are reused (rare but possible after peeling). This is the v3 outer-loop-wins semantics applied to blocks instead of allocas.

For peeled loops: BOTH the main blocks and the tail blocks get the annotation (same lane count; the difference is the mask, not the width).

### 2.4 Specialization pass — per-call SSA cloning

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_specialize.go` (NEW).

Runs AFTER `spmdConvertLoopOps` (so caller-side annotations are populated) and BEFORE TinyGo emission. Two phases:

**Phase A: Discover specialization requests.**

Walk every function in the program. For each `*ssa.Call` instruction:
- Check the callee: is it an SPMD function (has `Varying[T]` params or results)?
- Check the call's containing block's `SPMDLaneCount`: is it `> 0`?
- If both yes: record `(callee, callerLaneCount)` as a specialization request.

Collect requests into a map `map[*Function]map[int]bool` (function → set of requested lane counts).

**Phase B: Materialize variants.**

For each `(callee, laneCounts)` entry:
- For each `lc` in `laneCounts`:
  - Clone the callee's SSA: deep-copy of `Function` including all blocks, instructions, params, locals
  - Set every cloned block's `SPMDLaneCount = lc`
  - Rename the clone: `f.spmd<lc>` (e.g., `accumulate.spmd2`, `accumulate.spmd4`)
  - Add the clone to the package's `Members` map
- Rewrite call sites: each `Call` instruction whose target was the original `f` and whose containing block has `SPMDLaneCount = lc` gets its `Call.Call.Value` re-pointed to the `f.spmd<lc>` variant.

**Single-lane-count callee shortcut:** if a function is only ever called from one lane count, the specialization pass renames in place rather than cloning (reduces SSA bloat).

**Recursive callees:** the specialization is transitive. After cloning `f` to `f.spmd4`, the calls inside `f.spmd4` to other SPMD functions get re-evaluated with `SPMDLaneCount = 4` as the caller context. Iterate until fixed point.

**Detection helper** for "is this function SPMD?":

```go
func isSPMDFunction(fn *Function) bool {
    if fn.Signature == nil {
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
```

### 2.5 Forward-propagation pass — entry-block allocas

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go` (NEW).

Runs AFTER predication, BEFORE specialization (so specialization sees the propagated annotations and can dispatch correctly). Handles the case where an alloca lives in an unannotated block (typically the entry block of a non-SPMD function like `main()`) but is consumed by SPMD operations in annotated blocks.

Algorithm (initial — see §6.2 for known limitations):
- For each function, walk every basic block.
- For each block `b` with `SPMDLaneCount == 0`:
  - For each `Alloc` in `b`:
    - Check if any referrer of the alloca is an SPMD instruction (`SPMDLoad`, `SPMDStore`, `SPMDIndex`, `SPMDSelect`) in a block with `SPMDLaneCount > 0`
    - If yes: collect the set of consumer lane counts
    - If exactly one consumer lane count: tag this block with that count (sets `b.SPMDLaneCount`). This makes the alloca size correctly under TinyGo's block-annotation read.
    - If multiple different consumer lane counts: leave the block at 0 (forward-propagation cannot disambiguate); TinyGo falls back to existing derivation, the latent bug remains for that allocation. This is a known v4 limitation; see §6.2.

**Why annotate the BLOCK rather than the alloca**: keeps the canonical source single (block annotation). The downside is that any other allocas in the same block also get annotated; in practice the entry block contains ONLY allocas (Go SSA convention), so this is fine.

**Edge case**: an entry block that contains BOTH a `Varying[float64]` alloca (used by 2-wide loop) AND a `Varying[int]` alloca (used by 4-wide loop) — both can't be annotated to the same single block lane count. v4 takes the "first SPMD consumer wins" rule and leaves a known limitation; §6.2 documents the workaround (split into separate functions, or move declarations into the loop scope). A future v4.1 could split the entry block per-alloca if needed.

### 2.6 SSA tests

**File**: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_block_lanecount_test.go` (NEW).

Five tests:

```go
// TestSPMDBlockLaneCountSet — predication pass annotates loop-body blocks
//   with loop.LaneCount.
// TestSPMDSpecializationCloneVariants — calling an SPMD function from
//   two different lane counts produces two SSA variants (.spmd2, .spmd4)
//   with their blocks annotated.
// TestSPMDSpecializationRewriteCallSites — call sites in annotated
//   blocks dispatch to the correct variant.
// TestSPMDSpecializationSingleLaneShortcut — calling from one lane count
//   renames in place rather than cloning.
// TestSPMDForwardPropagationEntryBlock — entry-block allocas consumed by
//   in-loop SPMDLoad/SPMDStore in a single lane count get their block's
//   SPMDLaneCount set by propagation.
```

The lift test from v3 (`TestSPMDVaryingAllocaNotLifted`) and `export_spmd_test.go` are restored.

---

## 3. TinyGo Backend — Block-Annotation-Aware Materialization

### 3.1 The architectural shift

Currently TinyGo derives lane counts from many sources: element-natural width via `spmdLaneCount(elemType)`, function-min via `spmdMinLaneCountForSig`, mask vector size, instruction `Lanes` field, builder loop state. v4 adds ONE more source — the current block's `SPMDLaneCount` — and makes it the PRIMARY for `*types.SPMDType` materialization. The other sources become fallbacks for non-annotated contexts.

### 3.2 Builder context

Add to `*builder`:

```go
type builder struct {
    // ... existing fields ...

    // spmdActiveLaneCount is the block-level canonical lane count for the
    // block currently being emitted, mirroring (currentBlock).SPMDLaneCount
    // for fast access during nested helper calls. Set at block-entry,
    // restored at block-exit. 0 means non-SPMD context (use existing
    // derivations).
    spmdActiveLaneCount int
}
```

**Block-entry hook**: in TinyGo's existing `createBlock` / block iteration, before emitting any instruction in a block, set `b.spmdActiveLaneCount = block.SPMDLaneCount`. After the block's instructions are emitted, restore to whatever the previous block's value was (saved/restored stack-style). For the function entry, initialize to the entry block's annotation.

### 3.3 Type materialization

**File**: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`, `getLLVMType` for `*types.SPMDType`.

Currently `getLLVMType(*types.SPMDType)` calls `spmdEffectiveLaneCount` which calls `spmdLaneCount(elemType)`. Modify to consult `b.spmdActiveLaneCount` first:

```go
func (b *builder) spmdVaryingLLVMType(spmdType *types.SPMDType) llvm.Type {
    elemLLVM := b.getLLVMType(spmdType.Elem())
    var laneCount int
    if b.spmdActiveLaneCount > 0 {
        laneCount = b.spmdActiveLaneCount
    } else {
        laneCount = b.spmdEffectiveLaneCount(spmdType, elemLLVM)
    }
    if laneCount <= 1 {
        return elemLLVM // scalar fallback
    }
    return llvm.VectorType(elemLLVM, laneCount)
}
```

Note: `getLLVMType` runs in `compilerContext` (not `builder`) in some paths — those paths cannot consult `b.spmdActiveLaneCount`. For them, fall back to the existing derivation (lane count derives from element type or function-min). This is correct because `compilerContext`-level materialization happens at module-level (function signatures, global vars) where there's no in-block scope.

### 3.4 Alloca materialization

**File**: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, `*ssa.Alloc` case in `createExpr`.

Replace the v3 `expr.SPMDLaneCount` lookup with the block annotation:

```go
case *ssa.Alloc:
    elemType := expr.Type().Underlying().(*types.Pointer).Elem()

    // SPMD v4: when the alloca's element is varying and the alloca's
    // containing block carries an SPMDLaneCount, materialize the element
    // as a vector at that width. Block annotation is the canonical source;
    // see *ssa.BasicBlock.SPMDLaneCount and the SPMD v4 design spec.
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

Note: read `expr.Block().SPMDLaneCount` directly — this works regardless of whether `b.spmdActiveLaneCount` has been set yet (the alloca instruction is in some block, and that block's annotation is reachable).

### 3.5 Other lane-count derivation sites

`compiler/spmd.go` and `compiler/compiler.go` contain many calls to `spmdLaneCount(elemType)` and `spmdMinLaneCountForSig(sig)`. Each needs review:

- **In SPMD-context code paths** (e.g., `createSPMDLoad`, `createSPMDStore`, `IndexAddr` lowering for varying indices, gather/scatter/swizzle helpers, reduce dispatch, broadcast emit, mask materialization): replace with `b.spmdActiveLaneCount` (when > 0) or fall back to existing derivation. Each site is small.
- **In non-SPMD-context code paths** (e.g., function signature emission, global var sizing, type checking outside SPMD): keep the existing derivation. These run at compilerContext scope, not builder scope.

**Audit list** (per `grep -n "spmdLaneCount\|spmdMinLaneCount" compiler/`):
- `compiler/spmd.go`: 14 sites
- `compiler/compiler.go`: 5 sites
- `compiler/func.go`: 2 sites

A pass over each site, deciding "block-annotation-respect" vs "keep as-is", is part of the implementation plan.

### 3.6 Function emission — per-variant compilation

When TinyGo encounters a specialized variant function (named `f.spmd<lc>` with all blocks annotated to `lc`):
- Compile it as a separate LLVM function (no special handling needed — the SSA already has the right shape)
- The variant's name in LLVM IR follows the SSA name (just suffixed)
- Call sites already point to the variant via the SSA rewrite

No new TinyGo machinery needed beyond honoring the block annotations during emission.

---

## 4. Testing

Five test layers + regression sweep + benchmark sweep.

### 4.1 SSA unit tests

In `x-tools-spmd/go/ssa/spmd_block_lanecount_test.go`:
- `TestSPMDBlockLaneCountSet` — predication annotates loop-body blocks.
- `TestSPMDSpecializationCloneVariants` — two callers, two variants.
- `TestSPMDSpecializationRewriteCallSites` — call dispatch is rewritten.
- `TestSPMDSpecializationSingleLaneShortcut` — one caller, in-place rename.
- `TestSPMDForwardPropagationEntryBlock` — entry-block alloca annotation.

In `x-tools-spmd/go/ssa/spmd_lift_test.go` (restored from v3):
- `TestSPMDVaryingAllocaNotLifted` — varying allocas survive lift.

### 4.2 TinyGo IR tests

In `tinygo/compiler/spmd_test.go`:
- `TestSPMDVaryingAllocaLLVMType` (restored from v3, tightened): asserts alloca + load + reduce widths all `<2 x i32>`, no `<4 x i32>` on the alloca path.
- `TestSPMDVaryingLocalMaskedInTail` (restored from v3): regression smoke for masked stores in partial-mask tails.
- `TestSPMDSpecializedVariantEmitted` (NEW): asserts that when an SPMD function `f` is called from two lane counts, two LLVM functions appear (`f.spmd2`, `f.spmd4`) with the right vector widths.

The `MaxStackAlloc` test-config propagation from v3 attempt 1 is also kept (allocas must stay on stack to be observable).

### 4.3 End-to-end regression — n-body-nosqrt unblock

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. Reference: `-0.169075164` / `-0.169078071`.

### 4.4 End-to-end regression — n-body unblock

Symmetric; same toolchain unblock. The `lanes.Sqrt` change already landed.

### 4.5 Regression sweep — full E2E

The critical safety check. v1, v2, and v3-attempt-1 all regressed tests at this point.

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after-v4.txt 2>&1
diff <(grep -E "Compile|Run|Reject" /tmp/e2e-baseline-v3.txt) \
     <(grep -E "Compile|Run|Reject" /tmp/e2e-after-v4.txt)
```

Acceptance: identical totals (94 compile / 93 run / 11 reject) — or better. ZERO new failures.

If any pre-existing-passing test fails: STOP. Do not proceed. Revert all changes; iterate on the design.

### 4.6 Benchmark sweep

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after-v4.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-after-v4.txt
```

Acceptance: each ratio within ±10% of `/tmp/bench-baseline-v3.txt`. Specialization may slightly change inlining decisions; allocation widening may stress mem2reg differently.

### 4.7 Specialization correctness — synthetic test

Add a small E2E test that exercises specialization explicitly:

```go
// test/integration/spmd/dual-width-spmd-func/main.go
package main

import ("lanes"; "reduce")

func sum(v lanes.Varying[int]) int { return reduce.Add(v) }

var floats = []float64{1, 2, 3, 4, 5, 6, 7, 8}
var ints = []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func main() {
    var a lanes.Varying[int]
    go for _, x := range floats { a += int(x) }   // 2-wide on WASM128
    println(sum(a))                                 // call sum.spmd2

    var b lanes.Varying[int]
    go for _, x := range ints { b += int(x) }     // 4-wide on WASM128
    println(sum(b))                                 // call sum.spmd4
}
```

Expected output: `36` then `136`. SSA dump should show `sum.spmd2` and `sum.spmd4`.

---

## 5. Rollout, File Changes, Risks

### 5.1 Rollout order

1. SSA struct extension (BasicBlock.SPMDLaneCount + Alloc lift guard restoration) — single x-tools-spmd commit.
2. Predication pass: block annotation walk in `spmdConvertLoopOps`.
3. Specialization pass: new `spmd_specialize.go` file.
4. Forward-propagation pass: new `spmd_propagate.go` file.
5. SSA tests: new `spmd_block_lanecount_test.go` + restored `spmd_lift_test.go`.
6. TinyGo: builder field + block-entry hook + type materialization + alloca + audit of `spmdLaneCount` call sites.
7. TinyGo IR tests (restored from v3 + new specialization test).
8. Regression sweep — GATE.
9. Tinybench n-body-nosqrt + n-body unblock + BLOCKER cleanup.
10. Parent SPMD: submodule pointer bumps.

Each step independently revertible.

### 5.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `x-tools-spmd/go/ssa/ssa.go` | MODIFY | Add `SPMDLaneCount int` to `BasicBlock` |
| `x-tools-spmd/go/ssa/lift.go` | MODIFY | Restore lift guard + `isLanesVaryingType` helper |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | MODIFY | Block annotation walk in `spmdConvertLoopOps` |
| `x-tools-spmd/go/ssa/spmd_specialize.go` | NEW | Per-call specialization pass |
| `x-tools-spmd/go/ssa/spmd_propagate.go` | NEW | Forward-propagation for entry-block allocas |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | Restored lift test |
| `x-tools-spmd/go/ssa/spmd_block_lanecount_test.go` | NEW | 5 tests for v4 mechanisms |
| `x-tools-spmd/go/ssa/export_spmd_test.go` | NEW | Test re-export of `isLanesVaryingType` |
| `tinygo/compiler/compiler.go` (builder struct + `*ssa.Alloc` case) | MODIFY | Add `spmdActiveLaneCount`, block-entry hook, alloca materialization |
| `tinygo/compiler/spmd.go` (`getLLVMType` for SPMDType + ~14 derivation sites) | MODIFY | Block-annotation-aware lane count |
| `tinygo/compiler/func.go` (~2 sites) | MODIFY | Same |
| `tinygo/compiler/compiler_test.go` | MODIFY | `MaxStackAlloc` propagation (from v3 attempt 1) |
| `tinygo/compiler/spmd_test.go` | MODIFY | Restore v3 tests + add specialization test |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (cond. on §4.3) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (cond. on §4.4) | — |
| `tinybench/BLOCKERS.md` | MODIFY or DELETE | Remove entries; delete file if empty |
| `test/integration/spmd/dual-width-spmd-func/main.go` | NEW | Specialization correctness test |
| `test/e2e/spmd-e2e-test.sh` | MODIFY | Register new dual-width test |

Parent SPMD repo: submodule pointer bumps for `x-tools-spmd`, `tinygo`, `tinybench`.

### 5.3 Risks

1. **Specialization explosion.** A function called from N different lane counts produces N variants. Code size grows linearly. For the project's current scope (1-2 lane counts in practice), this is a non-issue; for future broad use, monitor.

2. **SSA cloning correctness.** Deep-copying a `*ssa.Function` requires careful handling of: shared Type instances (don't clone), Phi edge maps (preserve indices), block predecessor/successor pointers (rewrite to clones), instruction operand pointers (rewrite to cloned values). Risk of subtle aliasing bugs. Mitigation: extensive unit tests in §4.1; manual SSA-dump inspection during implementation.

3. **Forward-propagation under-coverage.** §2.5 only annotates the entry block with a SINGLE consumer lane count. Functions with mixed-width allocas (a `Varying[float64]` accumulator AND a `Varying[int]` accumulator, used in different loops with different widths) will only get one annotated correctly. Documented limitation in §6.2; workaround is to factor allocators into separate functions.

4. **Builder block-entry hook.** TinyGo's existing block-iteration code must be modified to set `b.spmdActiveLaneCount` before emitting instructions in each block. Easy to miss in some lowering path. Mitigation: add a sanity check that `b.spmdActiveLaneCount` matches `currentBlock.SPMDLaneCount` whenever it's read.

5. **Audit completeness.** §3.5 lists ~21 lane-count derivation sites in TinyGo. Missing one means the v3-attempt-1 cascading-mismatch failure mode could resurface. Mitigation: a comprehensive grep + checklist during implementation; the GATE in §4.5 catches anything missed.

6. **Backward compatibility with existing TinyGo lowering.** Block annotation is read with a `> 0` guard; unannotated blocks fall through to existing derivations. No regression for non-SPMD code or for SPMD code that doesn't go through the new annotation pass (yet).

7. **Performance regression from wider vectors.** A `Varying[int]` accumulator that used to be `<4 x i32>` (16 bytes, 1 register) might become `<2 x i32>` (8 bytes, 1 register) inside a 2-wide loop — or vice versa, `<8 x i32>` (32 bytes, 2 AVX2 registers, slight spill). Both are correctness improvements; perf may shift slightly. §4.6 catches >10% regressions.

### 5.4 Success criteria

All must hold:

- `TestSPMDVaryingAllocaNotLifted` (x-tools-spmd): PASS.
- `TestSPMDBlockLaneCountSet` (x-tools-spmd): PASS.
- `TestSPMDSpecializationCloneVariants`, `TestSPMDSpecializationRewriteCallSites`, `TestSPMDSpecializationSingleLaneShortcut`, `TestSPMDForwardPropagationEntryBlock` (x-tools-spmd): PASS.
- `TestSPMDVaryingAllocaLLVMType`, `TestSPMDVaryingLocalMaskedInTail`, `TestSPMDSpecializedVariantEmitted` (tinygo): PASS.
- `test/e2e/spmd-e2e-test.sh`: zero new failures vs the 105/94/93/11 baseline. Ideally `dual-width-spmd-func` adds one new pass entry.
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference.

### 5.5 Out of scope / deferred

- Removing `spmdMinLaneCountForSig` and `spmdLaneCount(elemType)` entirely (kept as fallbacks for non-annotated contexts).
- Per-target lane-count adaptation (e.g., one binary with both AVX2 8-wide and AVX-512 16-wide variants).
- Composite types containing varying fields.
- Specialization of methods (only top-level functions for now).
- Removing the `spmdActiveLaneCount` builder field once block annotations are universally read (would require all helpers to consult `instr.Block().SPMDLaneCount` directly; minor cleanup).

---

## 6. Open Design Questions and Known Limitations

### 6.1 Dispatch mechanism for multi-variant functions

If `f` is specialized to `f.spmd2` and `f.spmd4`, a call from a 4-wide block dispatches to `f.spmd4`. But what if the SAME source-level call appears in a block whose annotation is later forward-propagated? The specialization pass runs once before TinyGo emission; if forward-propagation changes annotations afterward, dispatch needs re-evaluation.

**Resolution**: run forward-propagation BEFORE specialization. Order: predicate → propagate → specialize → emit. Documented in §5.1.

### 6.2 Mixed-width entry-block allocas

A function with two varying allocas in its entry block, each consumed by loops with different lane counts:

```go
func main() {
    var a lanes.Varying[float64]  // used in 2-wide loop
    var b lanes.Varying[int]      // used in 4-wide loop
    go for ... range []float64 { a += ... }  // 2-wide
    go for ... range []int32   { b += ... }  // 4-wide
}
```

Forward-propagation can't annotate the entry block to BOTH 2 AND 4. v4 picks the first SPMD consumer's lane count and accepts that the other alloca will be sized wrong.

**Workarounds**:
- Move declarations into the loop scope (`var a lanes.Varying[float64]` inside the `go for` body).
- Factor each loop into its own function.

A future v4.1 could split the entry block per-alloca or introduce per-alloca overrides as a layered fallback. Out of v4 scope.

### 6.3 Recursive SPMD functions

If `f` calls itself, specialization needs to be careful about the fixed point. The implementation should iterate until no new specializations are requested, with a safety cap (e.g., 10 iterations) to prevent runaway.

In practice, the project doesn't currently use recursive SPMD functions. Documented limitation: the specialization pass detects recursion (a function calling itself directly or transitively) and emits a clear error if a recursive SPMD function would require multi-width specialization.

### 6.4 Block-entry hook implementation in TinyGo

TinyGo's existing emission walks blocks in topo-order. The hook needs to be inserted at exactly one place — the start of each block's instruction emission loop. If TinyGo has multiple emission entry points (e.g., separate paths for SPMD vs non-SPMD), each needs the hook.

Investigation during implementation: locate ALL block-entry sites; either centralize them or hook each independently.

---
