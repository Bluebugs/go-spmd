# Varying Iterator Printf Lane-Count Fix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `fmt.Printf("%v", i)` in a byte-element `go for` loop print all lanes (16 on WASM128, 32 on AVX2) instead of only the first 4 — by honoring `spmdType.Lanes()` in `createMakeInterface`.

**Architecture:** One-line fix in `tinygo/compiler/interface.go:131-134` so the reflect typecode emitted for a boxed `Varying[T]_N` describes a `[N]T` array (matching the LLVM data layout produced by `vectorToArray` in `compiler.go:3792`) instead of `[native]T`. New integ-level regression test asserts the printed output has the loop-width number of lanes.

**Tech Stack:** TinyGo compiler (Go), LLVM bindings, WASM SIMD128 target.

**Spec:** `docs/superpowers/specs/2026-05-14-varying-iter-printf-lane-count-design.md`

**Workflow:** Per CLAUDE.md, all implementation goes through golang-pro → code-reviewer → clean-commit. Tasks below are sized for single-PR-style commits.

**Baseline (must match or improve after the fix):** E2E `106/95/0/94/0/11 "All tests passed!"` — after adding the new integ test, expect `107/96/0/95/0/11`.

---

## File Inventory

- Modify: `tinygo/compiler/interface.go` (~line 131-134) — honor `Lanes()`
- Create: `test/integration/spmd/printf-varying-iter/main.go` — regression test
- Modify: `test/e2e/spmd-e2e-test.sh` (~line 669) — wire new test into Level 5d

---

## Task 1: Add regression test (TDD — failing first)

**Files:**
- Create: `test/integration/spmd/printf-varying-iter/main.go`
- Modify: `test/e2e/spmd-e2e-test.sh`

- [ ] **Step 1: Write the failing integration test.**

Create `test/integration/spmd/printf-varying-iter/main.go`:

```go
// run -goexperiment spmd

// Regression test for fmt.Printf("%v", i) where i is the varying
// loop iterator inside a byte-element go for loop.
//
// Before fix: createMakeInterface used spmdEffectiveLaneCount (the
// native register-derived count, 4 for int32 on WASM128) instead of
// spmdType.Lanes() (the loop-fixed width, 16 in a byte loop). The
// data array was laid out at the correct 16-lane width, but the
// reflect typecode said 4 lanes, so fmt.printSPMDVarying iterated
// only the first 4 lanes.
//
// After fix: the typecode honors Lanes(), so fmt prints all 16
// (SIMD) or 1 (scalar) iterator lane values per chunk.
package main

import "fmt"

//go:noinline
func dump(dst []byte) {
	go for i := range dst {
		fmt.Printf("i=%v\n", i)
	}
}

func main() {
	dst := make([]byte, 16)
	dump(dst)
}
```

- [ ] **Step 2: Wire the test into the E2E script.**

In `test/e2e/spmd-e2e-test.sh`, immediately after the existing
`integ_printf-varying-index` entry (around line 669), add:

```bash
test_compile_and_run "integ_printf-varying-iter" "$INTEG/printf-varying-iter/main.go" \
    "contains:i=[0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15]" \
    "" "-scheduler=none"
```

The `contains:` match looks for the all-16-lanes output. Before the
fix this output is `i=[0 1 2 3]` and the test FAILS.

- [ ] **Step 3: Run the new test (expect FAIL).**

```bash
cd /home/cedric/work/SPMD
./test/e2e/spmd-e2e-test.sh 2>&1 | grep -A2 "integ_printf-varying-iter"
```

Expected output: contains `FAIL` (because the unfixed compiler emits
`i=[0 1 2 3]`, not the required 16-lane string). If instead it
reports PASS, the diagnosis is wrong — STOP and re-investigate
before proceeding.

- [ ] **Step 4: Do NOT commit yet.** The test must stay failing so
Task 2 demonstrates the fix.

---

## Task 2: Implement the fix in `createMakeInterface`

**Files:**
- Modify: `tinygo/compiler/interface.go:131-134`

- [ ] **Step 1: Edit `tinygo/compiler/interface.go`.**

Replace lines 131-134:

```go
	if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
		elemLLVM := c.getLLVMType(spmdType.Elem())
		laneCount := c.spmdEffectiveLaneCount(spmdType, elemLLVM)
		return c.getTypeCode(c.spmdBoxedVaryingGoType(spmdType, laneCount))
	}
```

with:

```go
	if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
		elemLLVM := c.getLLVMType(spmdType.Elem())
		// Prefer the type-encoded lane count when set (loop-fixed width
		// from Pass A/B in the SSA predication pass). This matches the
		// rule in getLLVMType (compiler.go:568) so the reflect typecode
		// describes the same [N]T array shape as the LLVM vector value
		// being boxed. Falls back to spmdEffectiveLaneCount for
		// abstract Varying[T] (Lanes()==0) from function signatures,
		// globals, and other width-free contexts.
		laneCount := spmdType.Lanes()
		if laneCount <= 0 {
			laneCount = c.spmdEffectiveLaneCount(spmdType, elemLLVM)
		}
		return c.getTypeCode(c.spmdBoxedVaryingGoType(spmdType, laneCount))
	}
```

- [ ] **Step 2: Rebuild TinyGo.**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -5
```

Expected: build succeeds.

- [ ] **Step 3: Re-run the failing test from Task 1.**

```bash
./test/e2e/spmd-e2e-test.sh 2>&1 | grep -A2 "integ_printf-varying-iter"
```

Expected: PASS (the test now sees `i=[0 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15]`).

- [ ] **Step 4: Scalar parity sanity check.**

Compile the new test in scalar mode and confirm it still produces a
single-lane iteration printout (since scalar mode collapses to
laneCount=1):

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -target=wasi -simd=false -scheduler=none \
    -o /tmp/printf-iter-scalar.wasm \
    test/integration/spmd/printf-varying-iter/main.go
wasmer run /tmp/printf-iter-scalar.wasm 2>&1 | head -20
```

Expected: 16 lines, each `i=N` for N=0..15 (scalar mode runs one
iteration per lane). No `_` placeholders since laneCount=1 boxed
varyings have a 1-element values array with mask=1.

If scalar output is malformed, STOP — the Lanes() value for the
iter phi may be set to 16 even in scalar mode, in which case the
`Lanes()<=0` fallback path is the right behaviour and we need to
investigate scalar mode's Lanes() instead. (Per CLAUDE.md, scalar
mode sets `spmdLaneCount` to 1; Pass A/B should also produce
Lanes()=1 since LaneCount=1 disables the rewrite.)

---

## Task 3: Full verification (E2E + benchmarks)

**Files:** none

- [ ] **Step 1: Run full E2E suite.**

```bash
cd /home/cedric/work/SPMD
./test/e2e/spmd-e2e-test.sh 2>&1 | tail -25
```

Expected last lines include:

```
Total tests run: 107
Compile passes: 96
Compile failures: 0
Run passes: 95
Run failures: 0
Reject passes: 11

All tests passed!
```

If totals are different from `107/96/0/95/0/11`, examine each
failure and STOP if any test regresses against the
`106/95/0/94/0/11` baseline.

- [ ] **Step 2: Run WASM SIMD-vs-scalar benchmark.**

```bash
./test/e2e/spmd-benchmark.sh 2>&1 | tee /tmp/bench-wasm-after.txt | tail -40
```

Expected: hex-encode Dst ~6-9x, Mandelbrot ~2.5-3.6x, lo-*
~2-3x. Compare ratios against the entries in CLAUDE.md "Key
Metrics (wasmtime, SIMD vs scalar SPMD)". A regression >10% on
any line is a STOP signal.

- [ ] **Step 3: Run x86-64 native benchmark (SSE + AVX2).**

```bash
./test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-x86-after.txt | tail -40
```

Expected (per CLAUDE.md): AVX2 lo-min 7.27x, lo-max 7.18x,
mandelbrot 6.07x, etc. SSE lo-min 2.63x, hex-encode dst 6.31x,
etc. A regression >10% on any line is a STOP signal.

- [ ] **Step 4: Spot-check the original repro.**

```bash
mkdir -p /tmp/hex-repro && cat > /tmp/hex-repro/main.go << 'EOF'
package main

import (
	"os"
	"fmt"
)

const hextable = "0123456789abcdef"

//go:noinline
func Encode(dst, src []byte) int {
	go for i := range dst {
		v := src[i>>1]
		fmt.Printf("%v := src[%v>>1]\n", v, i)
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
		} else {
			dst[i] = hextable[v&0x0f]
		}
	}
	return len(src) * 2
}

func main() {
	src := []byte("hello SPMD world")
	dst := make([]byte, len(src)*2)
	Encode(dst, src)
	println(string(dst))
}
EOF
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -target=wasi -simd=true -scheduler=none \
    -o /tmp/hex-repro/test.wasm /tmp/hex-repro/main.go 2>&1 | tail -3
wasmer run /tmp/hex-repro/test.wasm 2>&1 | head -20
```

Expected: each `i=...` printout shows 16 lane values
(e.g. `[104 101 108 108 111 32 83 80 77 68 32 119 111 114 108 100]
:= src[[0 0 1 1 2 2 3 3 4 4 5 5 6 6 7 7]>>1]`), matching the
user's reported missing-12-lanes complaint. If only 4 values show,
the fix did not land — STOP.

---

## Task 4: Code review

**Files:** none (review pass)

- [ ] **Step 1: Dispatch the code-reviewer agent.**

Per CLAUDE.md, all changes go through `code-reviewer` before commit.
The reviewer should specifically check:

1. The `Lanes() <= 0` fallback exactly mirrors the rule in
   `getLLVMType` (`tinygo/compiler/compiler.go:568`).
2. No regression risk in places that previously relied on the
   native lane count (e.g. abstract `Varying[T]` from function
   signatures — these have `Lanes()==0` and still take the
   `spmdEffectiveLaneCount` branch).
3. The new integ test is deterministic — the loop iterates 16
   bytes (a single SPMD iteration on WASM128), so the printf
   output is stable across runs.
4. The cache in `getTypeCode` (`interface.go`) keys by
   `*types.SPMDType`. Different `Lanes()` produce different
   `*types.SPMDType` pointers (via `NewVaryingWithLanes`), so
   there is no cache-collision hazard.

Resolve any issues raised before proceeding.

---

## Task 5: Commit

**Files:** none (commit step)

- [ ] **Step 1: Stage and commit via the `clean-commit` agent.**

The diff touches:
- `tinygo/compiler/interface.go` (one block, ~8 lines)
- `test/integration/spmd/printf-varying-iter/main.go` (new)
- `test/e2e/spmd-e2e-test.sh` (one new entry)

Suggested message body for the commit:

```
fix(spmd): honor Lanes() in createMakeInterface boxed typecode

When the SSA predication pass width-fixes a Varying[T] to the
loop's lane count (e.g. Varying[int]_16 inside a byte go for),
getLLVMType already produces <16 x i32>, but createMakeInterface
was computing the boxed reflect typecode via spmdEffectiveLaneCount,
which always returned the native register-derived count (4 for i32
on WASM128). The data array was laid out at 16 lanes, but the
reflect typecode advertised 4, so fmt.printSPMDVarying iterated
only the first 4 lanes — fmt.Printf("%v", i) showed [0 1 2 3]
instead of [0..15].

Mirror the Lanes()-honoring rule from getLLVMType so the typecode
describes the same [N]T array shape as the LLVM value. Abstract
Varying[T] from function signatures still falls back to
spmdEffectiveLaneCount.

Add integ_printf-varying-iter regression test (Level 5d).
```

- [ ] **Step 2: Verify commit landed and tree is clean.**

```bash
git log --oneline -3
git status
```

Expected: new commit at HEAD, working tree clean.

---

## Out-of-scope (Option B — separate plan)

Audit other `spmdEffectiveLaneCount` usages in:
- `tinygo/compiler/interface.go:613` (defensive fallback in `getTypeCode`)
- `tinygo/compiler/interface.go:1013` (type-assertion SPMD branch)
- `tinygo/compiler/spmd.go:631` (`spmdBoxedVaryingGoType` itself)

for the same Lanes()-ignoring pattern. Will be a separate spec +
plan + commit after this lands and verification is green.
