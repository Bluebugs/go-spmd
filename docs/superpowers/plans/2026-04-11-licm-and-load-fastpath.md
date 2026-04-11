# LICM + All-Ones Load Fast-Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate ~12 wasted instructions per SPMD loop iteration by (1) using plain vector loads when the mask is all-ones, and (2) running LICM to hoist loop-invariant constants for SPMD compilations.

**Architecture:** Two independent changes in TinyGo. The load fast-path is a conditional check in `createSPMDLoad`. The LICM is added to the pass pipeline in `transform/optimizer.go` conditioned on `GOEXPERIMENT=spmd`.

**Tech Stack:** Go (TinyGo compiler), LLVM IR, LLVM pass pipeline

**Spec:** `docs/superpowers/specs/2026-04-11-licm-and-load-fastpath-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### TinyGo (`tinygo/`)
- **Modify:** `compiler/spmd.go` — add all-ones mask fast-path in `createSPMDLoad`
- **Modify:** `transform/optimizer.go` — add LICM pass for SPMD compilations

---

## Task 1: All-Ones Mask Load Fast-Path

**Files:**
- Modify: `tinygo/compiler/spmd.go:8266-8273`

The peeled main body sets `tailMask = llvm.ConstAllOnes(maskType)` (line 1653). When `createSPMDLoad` is called for a contiguous access with this all-ones mask, it currently falls through to `spmdMaskedLoad` which emits `llvm.masked.load`. This should be a plain `CreateLoad` instead.

- [ ] **Step 1: Add all-ones fast-path**

In `tinygo/compiler/spmd.go`, find the contiguous load section in `createSPMDLoad` (around line 8266). The current code is:

```go
// Cap-based optimization: full load + select when safe.
var result llvm.Value
if !b.spmdIsConstAllOnesMask(mask) && (b.spmdIsAllocaOrigin(ci) || !ci.sliceCap.IsNil()) {
    result = b.spmdFullLoadWithSelect(vecType, ci, mask)
    b.currentBlockInfo.exit = b.GetInsertBlock()
} else {
    result = b.spmdMaskedLoad(vecType, ci.scalarPtr, mask)
}
```

Replace with:

```go
// Fast path: all-ones mask → unconditional vector load.
// The peeled main body always has ConstAllOnes mask.
var result llvm.Value
if b.spmdIsConstAllOnesMask(mask) {
    elemAlign := int(b.targetData.TypeAllocSize(vecType.ElementType()))
    ld := b.CreateLoad(vecType, ci.scalarPtr, "spmd.load.full")
    ld.SetAlignment(elemAlign)
    result = ld
} else if b.spmdIsAllocaOrigin(ci) || !ci.sliceCap.IsNil() {
    // Cap-based optimization: full load + select when safe.
    result = b.spmdFullLoadWithSelect(vecType, ci, mask)
    b.currentBlockInfo.exit = b.GetInsertBlock()
} else {
    result = b.spmdMaskedLoad(vecType, ci.scalarPtr, mask)
}
```

This intercepts the all-ones case before both the cap-based and masked-load paths, emitting a plain `CreateLoad` that lowers to `vmovdqu` (x86) or `v128.load` (WASM).

- [ ] **Step 2: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 3: Verify correctness on all targets**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-loadfix-ssse3 \
  test/integration/spmd/base64-mula-lemire/main.go && /tmp/base64-loadfix-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-loadfix-avx2 \
  test/integration/spmd/base64-mula-lemire/main.go && /tmp/base64-loadfix-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-loadfix.wasm test/integration/spmd/base64-mula-lemire/main.go \
  && wasmtime run /tmp/base64-loadfix.wasm

# CompactStore test
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/compact-loadfix.wasm test/integration/spmd/compact-store/main.go \
  && wasmtime run /tmp/compact-loadfix.wasm
```

Expected: `Correctness: PASS` / `PASS` for all.

- [ ] **Step 4: Run benchmarks**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-loadfix-ssse3 \
  test/integration/spmd/base64-mula-lemire/bench.go && /tmp/bench-loadfix-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-loadfix-avx2 \
  test/integration/spmd/base64-mula-lemire/bench.go && /tmp/bench-loadfix-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-loadfix.wasm test/integration/spmd/base64-mula-lemire/bench.go \
  && wasmtime run /tmp/bench-loadfix.wasm
```

Report results. Expected improvement from ~7 fewer instructions per iteration.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "perf: emit plain vector load for all-ones SPMD mask

When the SPMD load mask is a compile-time all-ones constant (peeled
main body), emit a plain CreateLoad instead of llvm.masked.load.
Lowers to vmovdqu (x86) or v128.load (WASM), eliminating ~7
instructions of mask computation per iteration.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: LICM Pass for SPMD Compilations

**Files:**
- Modify: `tinygo/transform/optimizer.go:52-64`

Add LICM to the LLVM pass pipeline when SPMD is enabled. This hoists loop-invariant constants (LUT tables, mask bytes, shuffle masks) out of hot loops.

- [ ] **Step 1: Add SPMD-aware LICM to the pass pipeline**

In `tinygo/transform/optimizer.go`, the `Optimize` function receives `config *compileopts.Config`. The preparatory passes at line 56 are:

```go
optPasses := "globaldce,globalopt,ipsccp,instcombine<no-verify-fixpoint>,adce,function-attrs"
```

After this pass string is built (around line 60, before `mod.RunPasses`), add LICM for SPMD:

```go
optPasses := "globaldce,globalopt,ipsccp,instcombine<no-verify-fixpoint>,adce,function-attrs"
if llvmutil.Version() < 18 {
    optPasses = "globaldce,globalopt,ipsccp,instcombine,adce,function-attrs"
}

// Add LICM for SPMD compilations to hoist loop-invariant constants
// (LUT tables, mask bytes, shuffle masks) out of SPMD hot loops.
if hasExperiment(config.GOExperiment(), "spmd") {
    optPasses += ",function(loop-simplify,lcssa,licm)"
}
```

Note: LICM requires `loop-simplify` and `lcssa` (Loop-Closed SSA) as prerequisites. The `function(...)` wrapper runs these as function-level passes. Without `loop-simplify` and `lcssa`, LICM may not fire or may produce incorrect results.

IMPORTANT: The `hasExperiment` function is defined in `compileopts/config.go` (line 180), not in this file. It may need to be called via `config.GOExperiment()`:

```go
if strings.Contains(config.GOExperiment(), "spmd") {
    optPasses += ",function(loop-simplify,lcssa,licm)"
}
```

Check if `strings` is already imported in `optimizer.go`. If not, add the import. Alternatively, call `hasExperiment` if it's accessible (it's in a different package — `compileopts` — so it would need to be exported or the check done differently).

The simplest approach: use `strings.Contains` directly since `config.GOExperiment()` returns the GOEXPERIMENT string.

- [ ] **Step 2: Also add LICM after the second preparatory pass run**

The same pass sequence runs again at line 84 (after TinyGo-specific lowering). Add the same LICM extension there:

Find the second `mod.RunPasses(optPasses, ...)` call (around line 84) and apply the same pattern — the `optPasses` variable may be reused or reconstructed. Read the code to verify.

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 4: Verify correctness**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-licm-ssse3 \
  test/integration/spmd/base64-mula-lemire/main.go && /tmp/base64-licm-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-licm-avx2 \
  test/integration/spmd/base64-mula-lemire/main.go && /tmp/base64-licm-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-licm.wasm test/integration/spmd/base64-mula-lemire/main.go \
  && wasmtime run /tmp/base64-licm.wasm

# Non-SPMD compilation (verify no regression)
./tinygo/build/tinygo build -target=wasi -o /tmp/non-spmd-test.wasm \
  test/integration/spmd/compact-store/main.go 2>&1 || echo "Expected: compile without SPMD flag"
```

Expected: `Correctness: PASS` for all SPMD tests. Non-SPMD compilation should not be affected (LICM not added).

- [ ] **Step 5: Run benchmarks**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-licm-ssse3 \
  test/integration/spmd/base64-mula-lemire/bench.go && /tmp/bench-licm-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-licm-avx2 \
  test/integration/spmd/base64-mula-lemire/bench.go && /tmp/bench-licm-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-licm.wasm test/integration/spmd/base64-mula-lemire/bench.go \
  && wasmtime run /tmp/bench-licm.wasm
```

Report results. Expected: constants hoisted → 4-5 fewer instructions per iteration on AVX2, ~3 on SSSE3.

- [ ] **Step 6: Verify constants are hoisted (optional)**

Disassemble the SSSE3 binary and check that `vpbroadcastb` no longer appears inside the hot loop:

```bash
objdump -d /tmp/base64-licm-ssse3 | grep -A5 "vpbroadcastb\|pshufb" | head -20
```

The `vpbroadcastb` should appear before the loop (in the prologue), not inside it.

- [ ] **Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add transform/optimizer.go
git commit -m "perf: add LICM to LLVM pass pipeline for SPMD compilations

When GOEXPERIMENT=spmd is enabled, add loop-simplify + lcssa + licm
to the preparatory LLVM passes. This hoists loop-invariant constants
(LUT tables, mask bytes, shuffle masks) out of SPMD hot loops,
saving 4-5 instructions per iteration. Scoped to SPMD compilations
only via GOExperiment check.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Final Benchmarks

**Files:** None (verification only)

- [ ] **Step 1: Run all three targets with both fixes applied**

```bash
cd /home/cedric/work/SPMD

echo "=== SSSE3 ===" && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -llvm-features=+ssse3,+sse4.2 \
  -o /tmp/bench-final-ssse3 test/integration/spmd/base64-mula-lemire/bench.go \
  && /tmp/bench-final-ssse3

echo "=== AVX2 ===" && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -llvm-features=+ssse3,+sse4.2,+avx2 \
  -o /tmp/bench-final-avx2 test/integration/spmd/base64-mula-lemire/bench.go \
  && /tmp/bench-final-avx2

echo "=== WASM ===" && WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-final.wasm test/integration/spmd/base64-mula-lemire/bench.go \
  && wasmtime run /tmp/bench-final.wasm
```

- [ ] **Step 2: Compare before/after**

| Target | Before | After | Improvement |
|--------|--------|-------|-------------|
| SSSE3 @ 1MB | 4505 MB/s | ? | ? |
| AVX2 @ 1MB | 4979 MB/s | ? | ? |
| WASM @ 1MB | 1636 MB/s | ? | ? |
