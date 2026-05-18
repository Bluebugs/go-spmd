# IPv4 Parser AVX2 Codegen Fixes (Bug 2.b + 2.c) — Design

Date: 2026-05-17
Status: REVISED — original root cause was mislocated; see "Corrected Root
Cause" below. Scope: TinyGo backend only (`tinygo/compiler/`). No
`x-tools-spmd` or Go-frontend changes.

> **Revision note (2026-05-17, post-disasm-guard):** The original root-cause
> analysis (the "Root Cause" / "Fixes" sections below) was implemented and
> committed (tinygo `5c44de50` for 2.b, `03f76d91` for 2.c) and reviewed/
> tested — those commits are CORRECT and retained — but the disasm guard
> proved them INSUFFICIENT/MISDIRECTED for `parseIPv4Inner`. The authoritative
> analysis is now the **"Corrected Root Cause"** section at the end of this
> document. Read that section, not the original "Root Cause"/"Fixes", for what
> actually needs doing. The original sections are kept for history.

## Background

`parseIPv4Inner` in `test/integration/spmd/ipv4-parser/main.go` underperforms its
Mula-Lemire theoretical ceiling on x86-64 AVX2. A perf-analyzer investigation
(2026-05-17) identified the final `go for field, value := range values` loop
(`values [4]uint16`, `flens [4]uint8`, `shuffled [16]byte`, `ip [4]byte`) as the
source of two independent codegen defects:

- **Bug 2.b — sub-128-bit element width inflation.** The varying range variable
  `field` is `Varying[int]` (i64 on x86-64). Indexed reads `flens[field]`
  (uint8) and `values[field]` (uint16) are promoted to `<4 x i64>` instead of
  `<4 x i8>` / `<4 x i16>`. The `flen == uint8(2)` comparison then emits
  `vpcmpeqq` plus a `vextracti128`/`vpackssdw`/`vpmovsxbd` mask-narrowing chain;
  `values[field]` becomes 4× scalar `load i16 + zext i64 + insertelement`.
- **Bug 2.c — scalarized result store.** `ip[field] = uint8(value)` packs the 4
  result bytes into an xmm register via `vpshufb`, then discards it and scatters
  with 3× `vpextrb` + `vmovd` instead of a single 32-bit packed store.

Both replaced code paths are already semantically **correct** — they produce the
right values, only slowly. These are performance defects, not correctness bugs.

The two easy fixes from the same investigation (valid-only benchmark
`benchCases`, decimal loop → `go for`) have already landed on `main`
(commits `68cd811e`, `a078aa78`). This design covers only the two compiler
codegen fixes.

## Root Cause

### 2.b

Three sites in `tinygo/compiler/spmd.go` compute `resultElemType` for a
varying-indexed `*ssa.Index`:

- Site 1 — `spmdVectorIndexArray` identity-load sub-path (~6382–6387):
  unconditional `if vecBits < 128 { resultElemType = b.spmdMaskElemType(laneCount) }`.
- Site 2 — `spmdVectorIndexArray` GEP-fallback sub-path (~6517–6522):
  gated on `b.spmdUsesSIMD()`.
- Site 3 — `spmdVectorIndexString` per-lane path (~6326–6329):
  gated on `b.spmdUsesSIMD()`.

`spmdMaskElemType(N)` returns `IntType(regBits / N)`. On AVX2 with
`laneCount = 4` (capped at the `[4]` array length), that is `IntType(256/4) =
i64`, so `<4 x i8>` / `<4 x i16>` widen to `<4 x i64>`.

The widening exists solely because **WASM SIMD128 cannot lower sub-128-bit
vector types** (e.g. `<4 x i8>` = 32 bits is not a valid `v128`). On x86-64
SSE/AVX2 those sub-128-bit vectors are valid LLVM types that lower to SSE
sub-registers. The guard (`spmdUsesSIMD()` / unconditional) is too broad: it
fires on x86 where the widening is both unnecessary and wrong.

### 2.c

The SPMD range variable is `field = ChangeType(incrBinOp)` (go/ssa wraps the
iterator with the `Varying[int]` SPMD type). Contiguous-store detection does not
peel `*ssa.ChangeType`:

- `spmdAnalyzeContiguousIndex` (`tinygo/compiler/spmd.go` ~5257): `unwrapLoad`
  traces `SPMDLoad` chains but not `ChangeType`, so the `activeLoops` lookup on
  the wrapped index fails.
- `compiler.go` fast path (~3498–3503): `activeLoops[expr.Index]` is keyed on
  the raw `incrBinOp`/`loopPhi`, not the ChangeType wrapper.

With detection failing, `ip[field]` falls to the scatter-GEP path
(`<4 x ptr>` → `spmdMaskedScatter` → 4× `vpextrb` + per-lane store).
`spmdAnalyzeStrideIndex` already solves the identical problem with a local
`unwrapCT` helper (~2164) — that pattern is the proven template.

## Fixes

### 2.b — Approach A (targeted `spmdIsWASM()` guard)

At all three sites, apply the sub-128-bit widening to `spmdMaskElemType` **only
when `b.spmdIsWASM()`**. On non-WASM targets keep the natural `elemType`
(`i8`/`i16`). Add a one-line comment at each site stating the widening is a
WASM-only requirement. No behavioral change for WASM; x86 keeps natural width.

### 2.c — Approach A (localized ChangeType unwrap)

- In `spmdAnalyzeContiguousIndex`, peel `*ssa.ChangeType` chains before the
  `activeLoops` lookup. Factor the existing `unwrapCT` (spmd.go ~2164) into a
  shared helper rather than duplicating it; both `spmdAnalyzeStrideIndex` and
  `spmdAnalyzeContiguousIndex` use the shared helper.
- In the `compiler.go` fast path (~3498–3503), attempt the `activeLoops` lookup
  with the ChangeType-unwrapped key in addition to `expr.Index`.

Strictly additive: only previously-missed contiguous accesses are promoted;
ChangeType is a pure type annotation, so the unwrapped index is provably the
same sequential SSA value. No path is demoted.

Both fixes are independent and land as two separate atomic commits (2.b, then
2.c), each with its own tests.

## Testing Strategy

Chosen: compiler unit tests **and** x86 E2E coverage (maximum confidence).

1. **Compiler unit tests** (`tinygo/compiler/spmd_llvm_test.go` or sibling):
   - 2.b/x86: a `go for` over `[N]uint8`/`[N]uint16` indexed by the varying
     range var on an AVX2 target — assert emitted IR uses `<N x i8>`/`<N x i16>`
     (no i64 widening; no `<N x i64>` compare).
   - 2.b/WASM regression: same loop on a WASM target — assert the widening to the
     mask element type still occurs.
   - 2.c: a `go for` whose ChangeType-wrapped range var indexes a `[N]byte`
     store — assert the contiguous-store path is taken (no `<N x ptr>` scatter).
2. **x86 E2E**: add `integ_ipv4-parser` to Levels 10 (SSE) and 11 (AVX2) in
   `test/e2e/spmd-e2e-test.sh`, asserting the same program output as the WASM
   level, plus a disasm assertion on `parseIPv4Inner`: no `vpextrb` scatter for
   the result store, no `vpcmpeqq` for the `flen == 2` compare.
3. **Benchmark sanity**: re-run the valid-only ipv4 benchmark
   (`test/e2e/spmd-benchmark-x86.sh` or the binary) — confirm the SPMD ratio
   improves vs the current ~1.0x with no scalar regression.
4. **Full E2E**: confirm the baseline **106/95/0/94/0/11 — "All tests passed!"**
   holds, plus the two new x86 levels.

## Risk & Rollout

- Correctness risk **low**: replaced paths (scatter, wide compare) are already
  correct; the fixes change codegen shape, not semantics. 2.c promotion is
  provably value-preserving.
- WASM blast radius **zero**: 2.b stays WASM-only via `spmdIsWASM()`; 2.c is
  additive and WASM contiguous detection already works.
- x86 blast radius: 2.b affects any x86 `go for` doing `arr[varyingIdx]` with
  sub-128-bit element types (base64 / hex-encode / lo-* — covered by full E2E +
  benchmarks); 2.c affects any x86 `IndexAddr` with a ChangeType-wrapped index
  (same coverage).
- Rollout: golang-pro → code-reviewer → clean-commit per CLAUDE.md; two atomic
  commits, each carrying its own tests.

## Out of Scope (Deferred, Not Fixed)

- The `laneIndices` narrowing guard at `spmd.go` ~1790 uses strict `>`
  (`laneCount*elemBits > regBits`); for AVX2 `laneCount=4, int=i64`,
  `4*64 == 256` so i64 indices are not narrowed to i32. This is a separate
  latent issue (indices are used for GEP, not as data — correct at i64 width);
  not required to fix 2.b/2.c.
- Restructuring `shuffled` to a stride-2 byte-pair layout so the
  `vpmaddubsw`/`vpmaddwd` fast path (`spmdExtractPmaddSide`, requires
  `pat.stride == 2`) fires for the decimal-conversion loop. Separate
  optimization task.

Both deferred items should be recorded in PLAN.md's Deferred Items Collection
per CLAUDE.md.

---

## Corrected Root Cause (2026-05-17, AUTHORITATIVE)

A disasm guard (`test/e2e/ipv4-disasm-check.sh`) built after the two original
commits showed `vpcmpeqq` and `vpextrb` still present in `parseIPv4Inner`
despite correct results. A second perf-analyzer pass (grounded by building the
AVX2 binary + IR dumps) found the original analysis mislocated both paths.

### 2.b (the real one) — `spmdMaskedLoadNarrow`, `tinygo/compiler/spmd.go:4848`

`flens[field]` / `values[field]` do **not** go through `spmdVectorIndexArray`
(so commit `5c44de50` / `spmdSubVectorElemType` never runs for them). go/ssa
predication turns them into a contiguous `*ssa.SPMDLoad`, dispatched via
`createSPMDLoad` (~`spmd.go:8782`) → `spmdNarrowLoadElemBits(uint8, 4)` (~4806,
gated only by `spmdUsesSIMD()`, not WASM) → `spmdMaskedLoadNarrow` (~4848).

There, `wideElemType := b.spmdMaskElemType(laneCount)` is **i64** on AVX2 with
`laneCount==4` (256/4 = 64). Each loaded byte is ZExt'd to i64, the result is
`<4 x i64>`, and `flen == uint8(2)` becomes `icmp eq <4 x i64> ...` →
`vpcmpeqq`. The broadened `<i64 2,...>` constant follows automatically from the
`sext <4 x i1> → <4 x i64>` boolean-chain representation; capping the load
element width makes the constants fold to the narrower width too — no separate
comparison-site fix needed.

**Fix (TinyGo only, `spmd.go:4848`):** cap `wideElemType` at i32 instead of
`spmdMaskElemType(laneCount)`. The packed scalar for a `[4]uint8` load is i32
(8*4), so i32 makes the existing trunc/zext at ~4860–4866 a no-op and yields
`<4 x i32>` (→ `vpcmpeqd`/`vpcmpeqb` after LLVM combines). WASM is unaffected
(`spmdMaskElemType(4)` is already i32 there). Blast radius: only
`spmdMaskedLoadNarrow` (contiguous sub-128-bit array loads). E2E risk: low —
load values unchanged, only vector element width narrows.

Concrete shape (final wording at implementation time):

```go
// Cap at i32. spmdMaskElemType(laneCount) is i64 on AVX2 4-lane, which
// forces vpcmpeqq for byte/half comparisons. i32 is sufficient on every
// target (WASM already yields i32 here) and keeps trunc/zext a no-op.
maskElemBits := b.spmdRegisterBytes() * 8 / laneCount
if maskElemBits > 32 {
    maskElemBits = 32
}
wideElemType := b.ctx.IntType(maskElemBits)
```

### 2.c — already fixed by `03f76d91`; the residual `vpextrb` is return ABI

Commit `03f76d91` **did** fix the `ip[field]` scatter: it now compiles to a
packed blend-store (`store i32 %_, ptr %spmd.contiguous.ptr...`), verified in
IR — no scatter, no per-lane `vpextrb` for `ip[field]`. The `vpextrb` that
remain are the System V AMD64 return-ABI decomposition of the `[4]byte` return
value (after a `vpshufb` that extracts the low byte of each `values` uint16).
They were present before AND after `03f76d91`. This is **not a compiler bug**;
eliminating it would require changing `parseIPv4Inner`'s return to an output
pointer (`*[4]byte`) — a source/ABI change, explicitly out of scope.

**Therefore 2.c needs NO further compiler change.** The original disasm guard's
blanket `vpextrb` assertion tests the wrong thing and must be corrected:

- Keep/strengthen the `vpcmpeqq` assertion (real 2.b signal; must vanish after
  the `spmdMaskedLoadNarrow` fix).
- Replace the blanket `vpextrb` check with a **scatter** check: assert no
  `vpscatter`/`vpgather` and no per-lane scatter in the SPMD loop body. A
  practical proxy: assert `vpextrb` appears only in the function's return/exit
  path (after the final `vpshufb`), not in the loop body — or simply drop the
  `vpextrb` check and instead positively assert the IR/asm contains the packed
  contiguous store. Implementation chooses the most robust available check.

### Revised scope summary

| Item | Status | Action |
|---|---|---|
| Original 2.b (`spmdSubVectorElemType`, `5c44de50`) | DONE, correct, retained | none (helps other paths) |
| **Real 2.b (`spmdMaskedLoadNarrow` i32 cap)** | TODO | new implementation task |
| 2.c (`03f76d91`) | DONE, correct, sufficient | none |
| Disasm guard `vpextrb` criterion | wrong | revise to scatter/positive check |
| Disasm guard `vpcmpeqq` criterion | correct | keep; must pass after real 2.b |

x-tools-spmd untouched. Baseline to preserve: 106/95/0/94/0/11.
