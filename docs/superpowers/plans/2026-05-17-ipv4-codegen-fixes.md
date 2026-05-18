# IPv4 Parser AVX2 Codegen Fixes (2.b + 2.c) Implementation Plan

> **REVISED 2026-05-17 — read the "PLAN REVISION" section at the bottom first.**
> Original Task 1 (tinygo `5c44de50`) and Task 2 (tinygo `03f76d91`) are DONE,
> reviewed, and committed (retained — correct). The disasm guard revealed the
> original 2.b root cause was mislocated and the original 2.c is already fully
> fixed by Task 2. The authoritative remaining work is in "PLAN REVISION"; the
> original Tasks 1–4 below are kept for history. Execute the REVISED tasks.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the TinyGo backend from inflating sub-128-bit varying-indexed gathers to `<N x i64>` on x86, and let ChangeType-wrapped SPMD range indices reach the contiguous-store fast path — eliminating the `vpcmpeqq` chains and `vpextrb` scatter in `parseIPv4Inner` on AVX2.

**Architecture:** Two independent, additive TinyGo-only changes. (2.b) Extract the sub-128-bit gather element-type decision into one helper `spmdSubVectorElemType` that widens only on WASM, and route the three duplicated call sites through it. (2.c) Extract the duplicated ChangeType-peel closures into one shared helper `spmdUnwrapChangeType`, and apply it in contiguous-index detection (analyzer + the compiler.go fast path) so `field = ChangeType(incrBinOp)` is recognized as the loop iterator.

**Tech Stack:** Go, TinyGo compiler (`tinygo/compiler/`), LLVM Go bindings, `go/ssa` (patched `x-tools-spmd`), bash E2E harness, `objdump`.

**Process note (CLAUDE.md, mandatory):** Every code-implementation task in this plan MUST be executed via the `golang-pro` → `code-reviewer` → `clean-commit` agent pipeline. Never commit without review approval. Subagent-driven-development satisfies this by dispatching the implementer subagent and reviewing before the commit step.

**Baseline to preserve:** E2E **106/95/0/94/0/11 — "All tests passed!"** (plus the two new x86 levels added in Task 3).

---

## File Structure

- `tinygo/compiler/spmd.go` — add `spmdSubVectorElemType` (near `spmdMaskElemType`, ~line 2839) and package-level `spmdUnwrapChangeType`; route 3 width sites + contiguous/stride analyzers through them.
- `tinygo/compiler/compiler.go` — unwrap ChangeType in the `*ssa.IndexAddr` contiguous fast path (~line 3498).
- `tinygo/compiler/spmd_llvm_test.go` — add `TestSPMDSubVectorElemType` and `TestSPMDContiguousIndexChangeType`.
- `test/e2e/spmd-e2e-test.sh` — add `integ_ipv4-parser` to Level 10 (SSE) + Level 11 (AVX2).
- `test/e2e/ipv4-disasm-check.sh` — new: assert `parseIPv4Inner` AVX2 disasm has no `vpextrb` scatter / no `vpcmpeqq`.
- `PLAN.md` — record the two deferred items.
- `/home/cedric/.claude/projects/-home-cedric-work-SPMD/memory/ipv4_inner_perf_analysis.md` + `MEMORY.md` — update outcome.

---

## Task 1: Bug 2.b — WASM-only sub-128-bit gather widening

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add helper; sites ~6324–6331, ~6383–6387, ~6517–6523)
- Test: `tinygo/compiler/spmd_llvm_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDSubVectorElemType verifies that sub-128-bit varying-indexed gather
// results keep their natural element width on x86 (valid SSE/AVX2 sub-vectors)
// and are only widened to the mask element type on WASM (which cannot lower
// sub-128-bit vector types). Regression guard for Bug 2.b.
func TestSPMDSubVectorElemType(t *testing.T) {
	t.Run("x86 keeps natural width", func(t *testing.T) {
		c := newTestCompilerContextX86(t)
		defer c.dispose()
		b := newTestBuilder(t, c)
		defer b.Dispose()

		i8 := c.ctx.Int8Type()
		i16 := c.ctx.Int16Type()
		if got := b.spmdSubVectorElemType(i8, 4); got != i8 {
			t.Errorf("x86 spmdSubVectorElemType(i8,4) = %v, want i8 (no widening)", got)
		}
		if got := b.spmdSubVectorElemType(i16, 4); got != i16 {
			t.Errorf("x86 spmdSubVectorElemType(i16,4) = %v, want i16 (no widening)", got)
		}
	})

	t.Run("WASM widens sub-128-bit", func(t *testing.T) {
		c := newTestCompilerContext(t) // WASM
		defer c.dispose()
		b := newTestBuilder(t, c)
		defer b.Dispose()

		i8 := c.ctx.Int8Type()
		// <4 x i8> = 32 bits < 128 → widen to mask elem type on WASM.
		want := c.spmdMaskElemType(4)
		if got := b.spmdSubVectorElemType(i8, 4); got != want {
			t.Errorf("WASM spmdSubVectorElemType(i8,4) = %v, want %v", got, want)
		}
		// <16 x i8> = 128 bits, not sub-128 → unchanged even on WASM.
		if got := b.spmdSubVectorElemType(i8, 16); got != i8 {
			t.Errorf("WASM spmdSubVectorElemType(i8,16) = %v, want i8 (128-bit, no widen)", got)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tinygo/compiler && go test -run TestSPMDSubVectorElemType ./... 2>&1 | tail -5`
Expected: build failure — `undefined: b.spmdSubVectorElemType` (helper does not exist yet).

- [ ] **Step 3: Add the helper**

In `tinygo/compiler/spmd.go`, immediately before `func (c *compilerContext) spmdMaskElemType` (so it sits next to the related width logic; if that exact anchor is not at ~2839, place it directly above `spmdMaskElemType`'s definition), add:

```go
// spmdSubVectorElemType returns the element type in which to build a
// varying-indexed gather result vector of laneCount lanes.
//
// WASM SIMD128 cannot lower sub-128-bit vector types (e.g. <4 x i8> = 32 bits,
// <4 x i16> = 64 bits), so on WASM such results are widened to the mask
// element type (e.g. <4 x i32>), zero-extending each loaded element.
//
// On x86 (SSE/AVX2) sub-128-bit vectors are valid LLVM types that lower to
// SSE sub-registers; widening there spuriously promotes byte/half results to
// the register-width integer (e.g. <4 x i64> on AVX2), forcing 64-bit
// compares (vpcmpeqq) and scalar reconstruction. Keep the natural width.
func (b *builder) spmdSubVectorElemType(elemType llvm.Type, laneCount int) llvm.Type {
	if !b.spmdIsWASM() {
		return elemType
	}
	vecBits := uint64(b.targetData.TypeAllocSize(llvm.VectorType(elemType, laneCount))) * 8
	if vecBits < 128 {
		return b.spmdMaskElemType(laneCount)
	}
	return elemType
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tinygo/compiler && go test -run TestSPMDSubVectorElemType ./... 2>&1 | tail -5`
Expected: PASS (both subtests).

- [ ] **Step 5: Route the three sites through the helper**

In `tinygo/compiler/spmd.go`:

Site 1 — `spmdVectorIndexString` per-lane path. Replace:

```go
	bufElemType := b.ctx.Int8Type()
	resultElemType := bufElemType
	if b.spmdUsesSIMD() {
		vecBits := uint64(b.targetData.TypeAllocSize(llvm.VectorType(bufElemType, laneCount))) * 8
		if vecBits < 128 {
			resultElemType = b.spmdMaskElemType(laneCount)
		}
	}
```

with:

```go
	bufElemType := b.ctx.Int8Type()
	resultElemType := b.spmdSubVectorElemType(bufElemType, laneCount)
```

Site 2 — `spmdVectorIndexArray` identity-load sub-path. Replace:

```go
			resultElemType := elemType
			vecBits := uint64(b.targetData.TypeAllocSize(llvm.VectorType(elemType, laneCount))) * 8
			if vecBits < 128 {
				resultElemType = b.spmdMaskElemType(laneCount)
			}
```

with:

```go
			resultElemType := b.spmdSubVectorElemType(elemType, laneCount)
```

Site 3 — `spmdVectorIndexArray` GEP-fallback path. Replace:

```go
	resultElemType := elemType
	if b.spmdUsesSIMD() {
		vecBits := uint64(b.targetData.TypeAllocSize(llvm.VectorType(elemType, laneCount))) * 8
		if vecBits < 128 {
			resultElemType = b.spmdMaskElemType(laneCount)
		}
	}
```

with:

```go
	resultElemType := b.spmdSubVectorElemType(elemType, laneCount)
```

- [ ] **Step 6: Build the compiler and run the SPMD unit suite for regressions**

Run: `cd tinygo/compiler && go test -run 'TestSPMD' ./... 2>&1 | tail -15`
Expected: PASS (no regressions; `TestSPMDVectorIndexArrayLLVM`, `TestSPMDVectorIndexStringLLVM`, `TestSPMDIndexNarrowingX86`, `TestSPMDSubVectorElemType` all pass).

- [ ] **Step 7: Commit** (via clean-commit agent per CLAUDE.md)

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/spmd_llvm_test.go
git commit -m "fix: keep sub-128-bit varying gather width on x86"
```
Message body must explain: widening to spmdMaskElemType is a WASM-only requirement; on x86 it caused <4 x i64> promotion (vpcmpeqq + scalar reconstruction) for `flens[field]`/`values[field]` in parseIPv4Inner. End with the CLAUDE.md `Co-Authored-By:` trailer.

---

## Task 2: Bug 2.c — ChangeType-aware contiguous store detection

**Files:**
- Modify: `tinygo/compiler/spmd.go` (add `spmdUnwrapChangeType`; refactor local closures at ~2164 and ~9463; peel ChangeType in `spmdAnalyzeContiguousIndex`'s `unwrapLoad` ~5219)
- Modify: `tinygo/compiler/compiler.go` (fast path ~3498–3503)
- Test: `tinygo/compiler/spmd_llvm_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tinygo/compiler/spmd_llvm_test.go`:

```go
// TestSPMDContiguousIndexChangeType verifies that a ChangeType-wrapped loop
// iterator (the SPMD range variable, e.g. field = ChangeType(incrBinOp)) is
// recognized by spmdAnalyzeContiguousIndex, so ip[field] reaches the
// contiguous-store fast path instead of a per-lane vpextrb scatter.
// Regression guard for Bug 2.c.
func TestSPMDContiguousIndexChangeType(t *testing.T) {
	c := newTestCompilerContext(t)
	defer c.dispose()
	b := newTestBuilder(t, c)
	defer b.Dispose()

	iterPhi := &ssa.Phi{}
	loop := &spmdActiveLoop{
		bodyIterValue: iterPhi,
		laneCount:     4,
		scalarIterVal: llvm.ConstInt(c.ctx.Int32Type(), 0, false),
	}
	b.spmdLoopState = &spmdLoopState{
		activeLoops: map[ssa.Value]*spmdActiveLoop{iterPhi: loop},
		bodyBlocks:  map[int]*spmdActiveLoop{},
		loopBlocks:  map[int]*spmdActiveLoop{},
	}

	// field = ChangeType(iterPhi) — how go/ssa tags the SPMD range variable.
	ct := &ssa.ChangeType{X: iterPhi}

	gotLoop, _, ok := b.spmdAnalyzeContiguousIndex(ct)
	if !ok {
		t.Fatal("spmdAnalyzeContiguousIndex(ChangeType(iterPhi)) = false, want true")
	}
	if gotLoop != loop {
		t.Errorf("returned loop = %p, want %p", gotLoop, loop)
	}

	// Sanity: nested ChangeType chains also unwrap.
	ct2 := &ssa.ChangeType{X: &ssa.ChangeType{X: iterPhi}}
	if _, _, ok := b.spmdAnalyzeContiguousIndex(ct2); !ok {
		t.Error("nested ChangeType not unwrapped")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tinygo/compiler && go test -run TestSPMDContiguousIndexChangeType ./... 2>&1 | tail -5`
Expected: FAIL — `spmdAnalyzeContiguousIndex(ChangeType(iterPhi)) = false, want true` (ChangeType is not currently peeled, so the `activeLoops` lookup misses).

- [ ] **Step 3: Add the shared helper and apply it**

In `tinygo/compiler/spmd.go`, add a package-level function (place it just above `func (b *builder) spmdAnalyzeStrideIndex`):

```go
// spmdUnwrapChangeType peels *ssa.ChangeType wrappers. ChangeType is a pure
// type annotation (e.g. tagging a value with an SPMD Varying[T] type) and
// never changes the underlying SSA value, so unwrapping it is always safe for
// identity and loop-membership checks.
func spmdUnwrapChangeType(v ssa.Value) ssa.Value {
	for {
		if ct, ok := v.(*ssa.ChangeType); ok {
			v = ct.X
		} else {
			break
		}
	}
	return v
}
```

In `spmdAnalyzeStrideIndex` (~2164), replace the local closure:

```go
	// Unwrap ChangeType chains on the top-level index value.
	unwrapCT := func(v ssa.Value) ssa.Value {
		for {
			if ct, ok := v.(*ssa.ChangeType); ok {
				v = ct.X
			} else {
				break
			}
		}
		return v
	}
```

with:

```go
	unwrapCT := spmdUnwrapChangeType
```

In the second identical closure (~9463), replace the same closure block:

```go
	// Unwrap ChangeType wrappers.
	unwrapCT := func(v ssa.Value) ssa.Value {
		for {
			if ct, ok := v.(*ssa.ChangeType); ok {
				v = ct.X
			} else {
				break
			}
		}
		return v
	}
```

with:

```go
	// Unwrap ChangeType wrappers.
	unwrapCT := spmdUnwrapChangeType
```

In `spmdAnalyzeContiguousIndex`, make `unwrapLoad` peel ChangeType first. Replace the opening of the closure:

```go
	unwrapLoad = func(v ssa.Value) ssa.Value {
		load, ok := v.(*ssa.SPMDLoad)
		if !ok {
			return v
		}
```

with:

```go
	unwrapLoad = func(v ssa.Value) ssa.Value {
		v = spmdUnwrapChangeType(v)
		load, ok := v.(*ssa.SPMDLoad)
		if !ok {
			return v
		}
```

- [ ] **Step 4: Fix the compiler.go fast path**

In `tinygo/compiler/compiler.go`, the `*ssa.IndexAddr` contiguous fast path. Replace:

```go
				if _, isOverridden := b.spmdValueOverride[expr.Index]; isOverridden {
					if loop, ok := b.spmdLoopState.activeLoops[expr.Index]; ok {
						if result, err := b.spmdContiguousIndexAddr(expr, loop); err == nil {
							return result, nil
						}
					}
				}
```

with:

```go
				if _, isOverridden := b.spmdValueOverride[expr.Index]; isOverridden {
					idxKey := spmdUnwrapChangeType(expr.Index)
					if loop, ok := b.spmdLoopState.activeLoops[idxKey]; ok {
						if result, err := b.spmdContiguousIndexAddr(expr, loop); err == nil {
							return result, nil
						}
					}
				}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tinygo/compiler && go test -run TestSPMDContiguousIndexChangeType ./... 2>&1 | tail -5`
Expected: PASS.

- [ ] **Step 6: Run the SPMD unit suite + stride regression**

Run: `cd tinygo/compiler && go test -run 'TestSPMD' ./... 2>&1 | tail -15`
Expected: PASS (especially `TestSPMDAnalyzeStrideIndex` — the refactored `unwrapCT` must behave identically).

- [ ] **Step 7: Commit** (via clean-commit agent per CLAUDE.md)

```bash
git add tinygo/compiler/spmd.go tinygo/compiler/compiler.go tinygo/compiler/spmd_llvm_test.go
git commit -m "fix: unwrap ChangeType in contiguous index detection"
```
Message body: SPMD range var is `ChangeType(incrBinOp)`; contiguous detection missed it so `ip[field]` scattered via vpextrb instead of a packed store. Factor the duplicated ChangeType-peel closures into `spmdUnwrapChangeType`. End with the CLAUDE.md `Co-Authored-By:` trailer.

---

## Task 3: x86 E2E coverage + disasm assertion for ipv4-parser

**Files:**
- Create: `test/e2e/ipv4-disasm-check.sh`
- Modify: `test/e2e/spmd-e2e-test.sh` (Level 10 ~line 846; Level 11 ~line 861)

- [ ] **Step 1: Build the TinyGo compiler with both fixes**

Run:
```bash
cd /home/cedric/work/SPMD && make build-tinygo 2>&1 | tail -5
```
Expected: builds cleanly (`tinygo/build/tinygo` updated).

- [ ] **Step 2: Add ipv4-parser to Level 10 (SSE) and Level 11 (AVX2)**

In `test/e2e/spmd-e2e-test.sh`, in the Level 10 block, after the `x86_odd-even` line add:

```bash
test_x86 "x86_ipv4-parser" "$INTEG/ipv4-parser/main.go" \
    "contains:'192.168.1.1' -> 192.168.1.1|||'127.0.0.1' -> 127.0.0.1|||'192.168.1.a' -> ERROR: parse 192.168.1.a at position 10: unexpected character|||'256.1.1.1' -> ERROR: parse 256.1.1.1 at position 0: IPv4 field has value >255|||'192.168.01.1' -> ERROR: parse 192.168.01.1 at position 0: IPv4 field has octet with leading zero"
```

In the Level 11 block, after the `avx2_mandelbrot` line add:

```bash
test_x86_avx2 "avx2_ipv4-parser" "$INTEG/ipv4-parser/main.go" \
    "contains:'192.168.1.1' -> 192.168.1.1|||'127.0.0.1' -> 127.0.0.1|||'192.168.1.a' -> ERROR: parse 192.168.1.a at position 10: unexpected character|||'256.1.1.1' -> ERROR: parse 256.1.1.1 at position 0: IPv4 field has value >255|||'192.168.01.1' -> ERROR: parse 192.168.01.1 at position 0: IPv4 field has octet with leading zero"
```

- [ ] **Step 3: Create the disasm assertion script**

Create `test/e2e/ipv4-disasm-check.sh`:

```bash
#!/usr/bin/env bash
# Asserts the AVX2 codegen for parseIPv4Inner no longer contains the
# scalarized result store (vpextrb scatter) or the i64-inflated flen
# comparison (vpcmpeqq). Regression guard for Bug 2.b / 2.c.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TINYGO="$ROOT/tinygo/build/tinygo"
GOROOT_SPMD="$ROOT/go"
SRC="$ROOT/test/integration/spmd/ipv4-parser/main.go"
BIN="$(mktemp -d)/ipv4-avx2"

PATH="$GOROOT_SPMD/bin:$PATH" GOEXPERIMENT=spmd \
    "$TINYGO" build -llvm-features="+ssse3,+sse4.2,+avx2" -o "$BIN" "$SRC"

# Slice the parseIPv4Inner function body out of the disassembly: from its
# symbol label to the next symbol label or blank line.
DIS="$(objdump -d --no-show-raw-insn "$BIN" \
    | awk '/<[^>]*parseIPv4Inner[^>]*>:/{f=1} f{print} f&&/^$/{exit}')"

if [ -z "$DIS" ]; then
    echo "FAIL: parseIPv4Inner symbol not found in disassembly"
    exit 1
fi

fail=0
if echo "$DIS" | grep -qiw 'vpextrb'; then
    echo "FAIL: vpextrb present in parseIPv4Inner (2.c: scalarized result store)"
    fail=1
fi
if echo "$DIS" | grep -qiw 'vpcmpeqq'; then
    echo "FAIL: vpcmpeqq present in parseIPv4Inner (2.b: i64-inflated compare)"
    fail=1
fi

if [ "$fail" -ne 0 ]; then
    echo "--- parseIPv4Inner disassembly ---"
    echo "$DIS"
    exit 1
fi
echo "PASS: parseIPv4Inner AVX2 disasm clean (no vpextrb scatter, no vpcmpeqq)"
```

Then: `chmod +x test/e2e/ipv4-disasm-check.sh`

- [ ] **Step 4: Run the disasm check**

Run: `bash test/e2e/ipv4-disasm-check.sh`
Expected: `PASS: parseIPv4Inner AVX2 disasm clean (no vpextrb scatter, no vpcmpeqq)`.
If it FAILs, the fixes did not take effect for this loop — stop and re-investigate Task 1/2 before proceeding (do not weaken the assertion).

- [ ] **Step 5: Commit** (via clean-commit agent per CLAUDE.md)

```bash
git add test/e2e/spmd-e2e-test.sh test/e2e/ipv4-disasm-check.sh
git commit -m "test: x86 E2E + disasm guard for ipv4-parser codegen"
```
End with the CLAUDE.md `Co-Authored-By:` trailer.

---

## Task 4: Full validation, benchmark, and bookkeeping

**Files:**
- Modify: `PLAN.md` (Deferred Items Collection)
- Modify: `/home/cedric/.claude/projects/-home-cedric-work-SPMD/memory/ipv4_inner_perf_analysis.md`, `MEMORY.md`

- [ ] **Step 1: Run the full E2E suite**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -25`
Expected: **"All tests passed!"**, counts at least the prior baseline 106/95/0/94/0/11, and the two new lines `x86_ipv4-parser` / `avx2_ipv4-parser` show `PASS`.
If any prior-passing test regresses, stop — the change is not complete; re-open Task 1/2.

- [ ] **Step 2: Benchmark sanity (valid-only ipv4)**

Run:
```bash
cd /home/cedric/work/SPMD && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -llvm-features="+ssse3,+sse4.2,+avx2" \
  -o /tmp/ipv4-avx2 test/integration/spmd/ipv4-parser/main.go && /tmp/ipv4-avx2 2>&1 | tail -20
```
Expected: `Correctness: SPMD and scalar results match.` and the SPMD/scalar `Speedup (min)` is **≥ the pre-fix ~1.02x** (improvement expected; record the exact number — no scalar regression, SPMD not slower than before).

- [ ] **Step 3: Record the two deferred items in PLAN.md**

In `PLAN.md`'s "Deferred Items Collection" section, add two entries (Task / Location / Status / Depends On / Implementation / Priority / Related), exactly:

- **laneIndices narrowing strict-`>`**: Location `tinygo/compiler/spmd.go` `emitSPMDBodyPrologue` (~1790); Status DEFERRED; Implementation: change `laneCount*elemBits > regBits` to `>=` so AVX2 `[4]int` (4*64==256) narrows i64→i32 lane indices; Priority LOW (indices used for GEP, correct at i64); Related: 2026-05-17 ipv4 codegen fixes.
- **stride-2 layout for pmaddubsw decimal conv**: Location `test/integration/spmd/ipv4-parser/main.go` decimal-conversion `go for`; Status DEFERRED; Implementation: restructure `shuffled` to stride-2 byte pairs so `spmdExtractPmaddSide` (requires `pat.stride==2`) fires; Priority MEDIUM; Related: same.

- [ ] **Step 4: Update memory**

Append the outcome (final AVX2 ipv4 speedup number, "2.b/2.c fixed", commit hashes) to `/home/cedric/.claude/projects/-home-cedric-work-SPMD/memory/ipv4_inner_perf_analysis.md` and refresh the one-line `MEMORY.md` pointer.

- [ ] **Step 5: Commit** (docs/bookkeeping; clean-commit agent)

```bash
git add PLAN.md
git commit -m "docs: record deferred ipv4 codegen follow-ups"
```
End with the CLAUDE.md `Co-Authored-By:` trailer. (Memory files live outside the repo — no commit needed for those.)

---

## Notes / Out of Scope

- No `x-tools-spmd` changes. No Go-frontend changes.
- Out of scope (deferred, recorded in Task 4): the `laneIndices` strict-`>` narrowing latent issue and the stride-2 `shuffled` restructuring for the pmaddubsw decimal-conv fast path.
- Each code task (1, 2, 3) is independent and self-contained; Task 4 validates the whole.

---

# PLAN REVISION (2026-05-17) — AUTHORITATIVE

Original Tasks 1 & 2 are DONE/committed/retained (tinygo `5c44de50`,
`03f76d91`). The disasm guard proved: original-2.b fixed the wrong path;
original-2.c (`03f76d91`) already fully fixes the real `ip[field]` scatter and
the residual `vpextrb` is return-ABI (not a bug). Execute the tasks below.

Process unchanged: each code task via `golang-pro` (implement+test, NO commit)
→ `code-reviewer` (spec + quality) → `clean-commit`. tinygo changes commit in
the `tinygo/` submodule on branch `spmd`; parent (test/e2e, PLAN.md, docs,
submodule bump) on `main`.

## Task R1: Real 2.b — cap `spmdMaskedLoadNarrow` element width at i32

**Files:** Modify `tinygo/compiler/spmd.go` (`spmdMaskedLoadNarrow`, the
`wideElemType := b.spmdMaskElemType(laneCount)` line — currently spmd.go:4848,
locate by exact string). Test: `tinygo/compiler/spmd_llvm_test.go`.

Root cause (authoritative): `flens[field]`/`values[field]` compile to a
contiguous `*ssa.SPMDLoad` → `createSPMDLoad` → `spmdNarrowLoadElemBits` →
`spmdMaskedLoadNarrow`, where `wideElemType = spmdMaskElemType(laneCount)` is
**i64** on AVX2 4-lane (256/4), producing `<4 x i64>` and `vpcmpeqq`. Cap at
i32. Do NOT use a blanket `b.ctx.Int32Type()` — that would break WASM sub-cases
where `spmdMaskElemType` is intentionally < i32 (e.g. WASM 8-lane → i16);
`<8 x i32>`=256 bits is not a valid WASM v128. Use a min() cap so the value
only ever shrinks from the original.

- [ ] **Step 1: Write the failing test** — append to `spmd_llvm_test.go`:

```go
// TestSPMDMaskedLoadNarrowWidthCap verifies the narrow contiguous load builds
// its result vector at a capped element width: on AVX2 4-lane it must be i32
// (not the i64 that spmdMaskElemType(4)=256/4 would give, which forces
// vpcmpeqq for byte comparisons), while WASM behavior is unchanged.
// Regression guard for the real Bug 2.b.
func TestSPMDMaskedLoadNarrowWidthCap(t *testing.T) {
	t.Run("AVX2 4-lane caps at i32 (was i64)", func(t *testing.T) {
		c := newTestCompilerContextX86(t)
		defer c.dispose()
		b := newTestBuilder(t, c)
		defer b.Dispose()
		c.SIMDRegisterBytes = 32 // AVX2 256-bit
		laneCount := 4
		// targetElemBits=8 (uint8 load), 4 lanes → packed i32.
		ptrTy := llvm.PointerType(c.ctx.Int32Type(), 0)
		ptr := llvm.ConstPointerNull(ptrTy)
		mask := llvm.ConstNull(llvm.VectorType(c.ctx.Int1Type(), laneCount))
		res := b.spmdMaskedLoadNarrow(8, ptr, laneCount, mask)
		if got := res.Type().ElementType(); got != c.ctx.Int32Type() {
			t.Errorf("AVX2 narrow-load elem = %v, want i32 (capped, not i64)", got)
		}
	})
	t.Run("WASM 4-lane stays i32", func(t *testing.T) {
		c := newTestCompilerContext(t) // WASM 128-bit
		defer c.dispose()
		b := newTestBuilder(t, c)
		defer b.Dispose()
		laneCount := 4
		ptrTy := llvm.PointerType(c.ctx.Int32Type(), 0)
		ptr := llvm.ConstPointerNull(ptrTy)
		mask := llvm.ConstNull(llvm.VectorType(c.ctx.Int1Type(), laneCount))
		res := b.spmdMaskedLoadNarrow(8, ptr, laneCount, mask)
		if got := res.Type().ElementType(); got != c.ctx.Int32Type() {
			t.Errorf("WASM narrow-load elem = %v, want i32 (unchanged)", got)
		}
	})
}
```

(If `newTestCompilerContextX86` does not expose `SIMDRegisterBytes` assignment
the same way `TestSPMDWrapMaskAVX2_8Wide` does, follow that test's exact
mechanism for selecting AVX2 width. If `spmdMaskedLoadNarrow` cannot run with a
null ptr in the test harness, construct a minimal real alloca like
`TestSPMDVectorIndexArrayLLVM` does — match an existing working pattern; do not
invent harness APIs.)

- [ ] **Step 2: Run, expect FAIL** — `cd tinygo/compiler && go test -run TestSPMDMaskedLoadNarrowWidthCap ./... 2>&1 | tail -6`. Expected: AVX2 subtest fails (`elem = i64, want i32`).

- [ ] **Step 3: Implement** — in `spmd.go`, replace exactly:

```go
	wideElemType := b.spmdMaskElemType(laneCount) // i32 on WASM
```

with:

```go
	// Cap the result element width at i32. spmdMaskElemType(laneCount) is
	// regBits/laneCount = i64 on AVX2 4-lane, which forces <4 x i64> and
	// vpcmpeqq for the byte/half comparisons that consume this load. i32 is
	// sufficient (this is the sub-128-bit narrow path; targetElemBits<=16).
	// Use min() so WASM, where spmdMaskElemType is intentionally <=i32 (and
	// <i32 for >4 lanes), is unchanged — a blanket i32 would create invalid
	// sub-128-bit-element WASM vectors.
	maskElemBits := b.spmdRegisterBytes() * 8 / laneCount
	if maskElemBits > 32 {
		maskElemBits = 32
	}
	wideElemType := b.ctx.IntType(maskElemBits)
```

- [ ] **Step 4: Run, expect PASS** — same command as Step 2 → PASS (both subtests).

- [ ] **Step 5: Regression** — `cd tinygo/compiler && go test -run 'TestSPMD' ./... 2>&1 | tail -20`. Expected: only the two known pre-existing failures (`TestSPMDVaryingPointerFieldAddr_Contiguous`, `TestSPMDV5VaryingAllocaLLVMType`); everything else (incl. new test, `TestSPMDSubVectorElemType`, `TestSPMDContiguousIndexChangeType`) PASS.

- [ ] **Step 6: Review + commit** — golang-pro reports (no commit) → code-reviewer (spec: only the one line changed + test; quality: min-cap correct, WASM-safe) → clean-commit in `tinygo/` submodule. Suggested summary: `fix: cap narrow contiguous load width at i32`. CLAUDE.md trailer.

## Task R2: Correct the disasm guard + keep x86 E2E lines

**Files:** Modify `test/e2e/ipv4-disasm-check.sh` (parent repo, `main`).
`test/e2e/spmd-e2e-test.sh` already has the Level 10/11 ipv4-parser lines from
the original Task 3 attempt (left in the working tree, uncommitted) — keep
them; they are correct (program-output verification).

- [ ] **Step 1: Rebuild compiler with R1** — `cd /home/cedric/work/SPMD && make build-tinygo 2>&1 | tail -5`.

- [ ] **Step 2: Rewrite the disasm criterion.** The `vpcmpeqq` check stays
  (real 2.b signal; must now be absent after R1). Replace the blanket
  `vpextrb` check: the surviving `vpextrb` is the return-ABI byte
  decomposition (after the final `vpshufb`), not scatter. Assert instead that
  there is **no scatter/gather** and no per-lane scatter in the loop body.
  Edit `test/e2e/ipv4-disasm-check.sh` so the failure conditions are:
  - FAIL if `vpcmpeqq` appears in the `parseIPv4Inner` slice (unchanged check).
  - FAIL if any `vpscatter` or `vpgather` (any suffix) appears in the slice.
  - FAIL if `vpextrb` appears **before** the last `vpshufb` in the slice (i.e.
    in the loop body rather than only the return path). Implement by finding
    the line number of the last `vpshufb` and asserting no `vpextrb` line
    precedes it. If that proves brittle, fall back to: assert the slice
    contains a packed store to a `spmd.contiguous`/aligned 32-bit store and
    that the total `vpextrb` count is ≤ 4 (the [4]byte return). Pick whichever
    is robust against the actual disassembly; document the chosen rule in a
    comment in the script.
  Keep the `set -euo pipefail`, the symbol-slice awk, and the
  symbol-not-found FAIL. Keep a clear PASS line.

- [ ] **Step 3: Run the disasm check** — `bash test/e2e/ipv4-disasm-check.sh`.
  Expected: PASS — no `vpcmpeqq`, no scatter/gather, no loop-body `vpextrb`.
  If `vpcmpeqq` still present → R1 did not take effect; STATUS: BLOCKED with
  the sliced disasm (do NOT weaken the `vpcmpeqq` rule).

- [ ] **Step 4: Run the two new E2E lines** —
  `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E 'ipv4-parser|Summary|All tests' | tail -8`.
  Expected: `x86_ipv4-parser` and `avx2_ipv4-parser` show PASS.

- [ ] **Step 5: Review + commit** — golang-pro (no commit) → code-reviewer
  (the criterion genuinely distinguishes scatter from return-ABI; doesn't
  rubber-stamp) → clean-commit in PARENT repo (`test/e2e/ipv4-disasm-check.sh`
  + `test/e2e/spmd-e2e-test.sh`). Suggested summary:
  `test: x86 E2E + scatter/vpcmpeqq guard for ipv4-parser`. CLAUDE.md trailer.

## Task R3: Full validation + bookkeeping + submodule bump

**Files:** `PLAN.md`; memory files; parent submodule pointer.

- [ ] **Step 1: Full E2E** — `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -25`. Expected: "All tests passed!", ≥ baseline 106/95/0/94/0/11, plus `x86_ipv4-parser`/`avx2_ipv4-parser` PASS. Any regression → STATUS: BLOCKED.

- [ ] **Step 2: Benchmark sanity** — build valid-only ipv4 AVX2 binary (`-llvm-features=+ssse3,+sse4.2,+avx2`) and run it; confirm `Correctness: SPMD and scalar results match.` and record the SPMD `Speedup (min)` (expect ≥ the pre-fix ~1.02x; report the number). No scalar regression.

- [ ] **Step 3: PLAN.md Deferred Items** — add THREE entries (Task/Location/Status/DependsOn/Implementation/Priority/Related): (a) laneIndices strict-`>` narrowing (spmd.go ~1790); (b) stride-2 `shuffled` layout for pmaddubsw decimal conv; (c) NEW — `parseIPv4Inner` return-ABI `vpextrb` removable only by switching the return to an `*[4]byte` out-param (source/ABI change), Priority LOW, Related: this plan/2.c.

- [ ] **Step 4: Update memory** — append outcome (real-2.b fix `spmdMaskedLoadNarrow` i32 cap; 2.c was return-ABI not a bug; final AVX2 ipv4 speedup; all commit hashes) to `ipv4_inner_perf_analysis.md` + refresh `MEMORY.md` one-liner.

- [ ] **Step 5: Bump parent submodule pointer + commit** — in the parent repo (`main`): `git add tinygo PLAN.md` and commit. This records the tinygo submodule advance (`5c44de50`, `03f76d91`, R1) plus deferred items. Suggested summary: `chore: bump tinygo for ipv4 AVX2 codegen fixes`. CLAUDE.md trailer. (Memory files are outside the repo — no commit.) Do NOT push unless the user asks.

## Notes

- 2.c requires NO further compiler change (`03f76d91` is sufficient; residual
  `vpextrb` is return ABI). Do not "fix" it in the compiler.
- The blanket-`i32` shortcut for R1 is explicitly rejected (WASM-unsafe). Use
  the min-cap.
- If R1's disasm check still shows `vpcmpeqq`, the real path may have a second
  i64 widening site — escalate with the IR/asm rather than guessing.
