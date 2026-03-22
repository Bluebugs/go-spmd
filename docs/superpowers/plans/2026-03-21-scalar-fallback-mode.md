# Scalar Fallback Mode Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `-simd=true|false` flag to TinyGo so SPMD programs can compile to either SIMD128 WASM or pure scalar WASM, producing identical output. This is the Phase 3 gate — it enables dual-mode testing to prove correctness.

**Architecture:** TinyGo-only override. When `-simd=false`: (1) suppress SIMD128/relaxed-simd LLVM features, (2) force `spmdLaneCount()` to return 1, (3) `makeLLVMType` for `SPMDType` returns scalar `T` (not `<1 x T>`), (4) gate WASM-specific SIMD paths behind `spmdUsesSIMD()` helper. No Go toolchain or SSA changes needed — existing `laneCount == 1` code paths (SSA peeling skip, scalar load returns, identity ChangeType) handle the rest.

**Tech Stack:** Go (TinyGo CLI + compileopts + compiler)

---

## Design

### Why TinyGo-Only

The lane count flows: type checker → AST → SSA → TinyGo. Changing the type checker would require modifying the forked Go toolchain (invasive). Instead, TinyGo overrides at code generation time — the SSA still has `LaneCount = 4` and peeled loops, but TinyGo generates scalar code for each phase. The peeled tail phase runs 0 iterations with laneCount=1 and gets optimized away by LLVM.

### Key Existing laneCount == 1 Paths

- **SSA peeling** (`spmd_peel.go:299`): `if laneCount <= 1 { return }` — but this reads from SSA metadata (still 4), so peeling still happens. TinyGo handles peeled structure with laneCount=1.
- **Scalar load** (`spmd.go:6610`): `if laneCount == 1 && resultType != VectorTypeKind { return loaded }` — returns plain scalar.
- **ChangeType** (`compiler.go:2619`): scalar-to-scalar is identity when both types map to the same LLVM type.

### Critical Design: Scalar Types, Not `<1 x T>`

When `spmdLaneCount()` returns 1, `makeLLVMType` for `SPMDType` MUST return the scalar element type (`i32`), NOT `<1 x i32>`. A `<1 x T>` vector is legal in LLVM but causes cascading problems:
- Phi nodes from peeled SSA expect consistent types
- `splatScalar` creates `<1 x T>` splat with unnecessary insert+shuffle
- WASM mask intrinsics (`@llvm.wasm.anytrue.v1i32`) don't exist
- `spmdMaskSelect` TypeAllocSize comparisons break

With scalar types, SPMDLoad/SPMDStore/SPMDSelect degenerate to plain load/store/select.

### Critical Design: `spmdUsesSIMD()` Helper

`spmdIsWASM()` returns true based on the target triple, regardless of SIMD features. This means WASM-specific paths (i32 mask format, `@llvm.wasm.anytrue`, relaxed swizzle) fire even with SIMD disabled. A new helper gates these:

```go
func (c *compilerContext) spmdUsesSIMD() bool {
    return c.spmdIsWASM() && c.simdEnabled
}
```

Replace `spmdIsWASM()` with `spmdUsesSIMD()` at all SIMD-specific callsites (mask wrapping, WASM intrinsics, sub-128-bit widening). Keep `spmdIsWASM()` for platform checks unrelated to SIMD (pointer size, etc.).

### Printf Output Differences

`%v` on varying values prints `[x y z w]` (4 lanes) in SIMD mode vs `[x]` (1 lane) in scalar mode. This is correct behavior — the varying value has different lane counts. Dual-mode tests must compare **scalar results** (from `reduce.Add`, `reduce.Max`, etc.), not varying format strings. Tests that only print scalar outputs (e.g., `"Sum: 136"`) will match exactly.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `tinygo/compileopts/options.go` | Modify | Add `SIMD` field to Options struct |
| `tinygo/main.go` | Modify | Add `-simd` flag to CLI (using `flag.String` pattern) |
| `tinygo/compileopts/config.go` | Modify | `SIMDEnabled()` helper + skip SIMD128 injection |
| `tinygo/compileopts/config_spmd_test.go` | Modify | Tests for feature suppression |
| `tinygo/compiler/compiler.go` | Modify | Pass `simdEnabled` to compilerContext + `makeLLVMType` guard |
| `tinygo/compiler/spmd.go` | Modify | `spmdUsesSIMD()` + `spmdLaneCount` override + lane count clamping |
| `tinygo/compiler/spmd_llvm_test.go` | Modify | Unit tests for scalar override |
| `test/e2e/spmd-e2e-test.sh` | Modify | Dual-mode test level |

---

## Task 1: Add `-simd` flag to TinyGo CLI and config

**Files:**
- Modify: `tinygo/compileopts/options.go:23-64`
- Modify: `tinygo/main.go` (flag definitions area)
- Modify: `tinygo/compileopts/config.go:71-83`
- Test: `tinygo/compileopts/config_spmd_test.go`

### Step 1: Add SIMD field to Options

In `options.go`, add to the Options struct:

```go
SIMD            string // -simd flag: "true" (default), "false" for scalar fallback
```

- [ ] **Step 1a:** Add the field

### Step 2: Add `-simd` flag to CLI

In `main.go`, TinyGo uses `flag.String()` returning `*string` (not `flag.StringVar`). Follow the same pattern:

```go
simd := flag.String("simd", "", "SIMD mode: true (default for SPMD+WASM), false (scalar fallback)")
```

Then where other flag values are copied into `options` (search for `options.Opt = *opt`), add:

```go
options.SIMD = *simd
```

- [ ] **Step 2a:** Add flag and assignment

### Step 3: Add SIMDEnabled helper and gate feature injection

In `config.go`:

```go
// SIMDEnabled returns whether SIMD code generation is enabled.
func (c *Config) SIMDEnabled() bool {
    if c.Options.SIMD == "false" {
        return false
    }
    return hasExperiment(c.Options.GOExperiment, "spmd") && c.Target.GOARCH == "wasm"
}
```

Then in `Features()`, change the SIMD128 injection guard from:
```go
if hasExperiment(c.Options.GOExperiment, "spmd") && c.Target.GOARCH == "wasm" {
```
to:
```go
if c.SIMDEnabled() {
```

- [ ] **Step 3a:** Add `SIMDEnabled()` method
- [ ] **Step 3b:** Gate SIMD128 injection on `SIMDEnabled()`

### Step 4: Add config tests

```go
func TestSIMDDisabledSuppressesFeatures(t *testing.T) {
    config := makeTestConfig("spmd", "wasm")
    config.Options.SIMD = "false"
    features := config.Features()
    if strings.Contains(features, "+simd128") {
        t.Errorf("expected no +simd128 with -simd=false, got: %s", features)
    }
}

func TestSIMDDefaultEnabled(t *testing.T) {
    config := makeTestConfig("spmd", "wasm")
    features := config.Features()
    if !strings.Contains(features, "+simd128") {
        t.Errorf("expected +simd128 by default with SPMD+WASM, got: %s", features)
    }
}

func TestSIMDEnabledMethod(t *testing.T) {
    config := makeTestConfig("spmd", "wasm")
    if !config.SIMDEnabled() {
        t.Error("expected SIMDEnabled() == true for SPMD+WASM")
    }
    config.Options.SIMD = "false"
    if config.SIMDEnabled() {
        t.Error("expected SIMDEnabled() == false with -simd=false")
    }
}
```

- [ ] **Step 4a:** Add tests
- [ ] **Step 4b:** Run: `cd tinygo && make GO=... && go test ./compileopts/ -run TestSIMD -v`

### Step 5: Commit

```
feat: add -simd flag for scalar fallback mode

Add -simd=true|false flag to TinyGo CLI. When -simd=false, SIMD128
and relaxed-simd LLVM features are not injected. SIMDEnabled() helper
centralizes the check. Default remains true for SPMD+WASM targets.
```

---

## Task 2: Scalar code generation in compiler

**Files:**
- Modify: `tinygo/compiler/compiler.go` (`compilerContext` struct + `makeLLVMType`)
- Modify: `tinygo/compiler/spmd.go` (`spmdUsesSIMD` + `spmdLaneCount` + loop analysis)
- Test: `tinygo/compiler/spmd_llvm_test.go`

### Step 1: Add `simdEnabled` to compilerContext

In `compiler.go`, add to `compilerContext`:

```go
simdEnabled bool // false for scalar fallback mode (-simd=false)
```

Set it during initialization. Search for where `compilerContext` is populated (likely in `NewCompiler` or `Compile` function) and add:

```go
c.simdEnabled = config.SIMDEnabled()
```

- [ ] **Step 1a:** Add field and set from config

### Step 2: Add `spmdUsesSIMD()` helper

In `spmd.go`, add:

```go
// spmdUsesSIMD returns true when WASM SIMD instructions should be emitted.
// False in scalar fallback mode (-simd=false) even though spmdIsWASM() is true.
func (c *compilerContext) spmdUsesSIMD() bool {
    return c.spmdIsWASM() && c.simdEnabled
}
```

Then replace `spmdIsWASM()` with `spmdUsesSIMD()` at these SIMD-dependent callsites:

- `spmdWrapMask()` — i32 mask wrapping (only for WASM SIMD)
- `spmdUnwrapMaskForIntrinsic()` — i32→i1 conversion for intrinsics
- `spmdMaskElemType()` — i32 vs i1 mask element selection
- `spmdVectorAnyTrue()` / `spmdVectorAllTrue()` — WASM `anytrue`/`alltrue` intrinsics
- Sub-128-bit widening check in `createSPMDLoad` contiguous path
- `spmdNarrowLoadElemBits()` — narrow load WASM path
- Relaxed swizzle intrinsic calls
- `DotProductI8x16Add` intrinsic

**Do NOT replace** `spmdIsWASM()` at:
- `isDecomposed` checks (platform-specific, not SIMD-specific)
- Pointer size / int size queries
- Any check that's about the WASM platform, not SIMD instructions

Strategy: search for all `spmdIsWASM()` callsites, classify each as "SIMD-dependent" or "platform-dependent", replace the SIMD-dependent ones.

- [ ] **Step 2a:** Add `spmdUsesSIMD()` helper
- [ ] **Step 2b:** Replace SIMD-dependent `spmdIsWASM()` calls

### Step 3: Override `spmdLaneCount` for scalar mode

In `spmd.go`, function `spmdLaneCount`:

```go
func (c *compilerContext) spmdLaneCount(elemType llvm.Type) int {
    if !c.simdEnabled {
        return 1
    }
    elemSize := c.targetData.TypeAllocSize(elemType)
    if elemSize == 0 {
        return 1
    }
    return 16 / int(elemSize)
}
```

- [ ] **Step 3a:** Add early return 1 when `!simdEnabled`

### Step 4: `makeLLVMType` returns scalar for laneCount=1

In `compiler.go`, find the `SPMDType` case in `makeLLVMType` (search for `types.SPMDType`). After computing `n := c.spmdLaneCount(elemType)`, add:

```go
n := c.spmdLaneCount(elemType)
if n <= 1 {
    return elemType // scalar mode: Varying[T] == T
}
```

This prevents `<1 x T>` vector creation. With this, all SPMDType values are plain scalars in scalar mode — ChangeType is identity, loads/stores are plain, and no vector operations are emitted.

- [ ] **Step 4a:** Add laneCount=1 guard in `makeLLVMType`

### Step 5: Override lane count in `analyzeSPMDLoops`

In all paths in `analyzeSPMDLoops` where `laneCount` is computed (Pass 0 around line 807, Pass 1 around line 970), add after the computation:

```go
if !b.simdEnabled {
    laneCount = 1
}
```

- [ ] **Step 5a:** Add scalar override in loop analysis paths

### Step 6: Add unit tests

In `spmd_llvm_test.go`:

```go
func TestSPMDScalarModeReturnsLaneCount1(t *testing.T) {
    c := newTestCompilerContext(t)
    defer c.dispose()
    c.simdEnabled = false
    got := c.spmdLaneCount(c.ctx.Int32Type())
    if got != 1 {
        t.Errorf("spmdLaneCount with simdEnabled=false: got %d, want 1", got)
    }
}

func TestSPMDScalarModeUsesSIMDFalse(t *testing.T) {
    c := newTestCompilerContext(t)
    defer c.dispose()
    c.simdEnabled = false
    if c.spmdUsesSIMD() {
        t.Error("spmdUsesSIMD() should be false when simdEnabled=false")
    }
}
```

- [ ] **Step 6a:** Add unit tests
- [ ] **Step 6b:** Run tests (with CGO flags from TinyGo Makefile)

### Step 7: Rebuild and test basic scalar compilation

```bash
cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
rm -rf ~/.cache/tinygo
cd /home/cedric/work/SPMD

# Compile simple-sum in scalar mode
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
    -target=wasi -simd=false -scheduler=none \
    -o /tmp/simple-sum-scalar.wasm test/integration/spmd/simple-sum/main.go

# Run and verify output matches SIMD version
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/simple-sum-scalar.wasm

# Verify no SIMD instructions in output
wasm2wat /tmp/simple-sum-scalar.wasm | grep -c "v128" || echo "0 v128 instructions (correct)"
```

Expected: `Sum: 136`, 0 v128 instructions.

- [ ] **Step 7a:** Compile and run simple-sum in scalar mode
- [ ] **Step 7b:** Verify output matches SIMD mode
- [ ] **Step 7c:** Verify no v128 instructions

### Step 8: Commit

```
feat: implement scalar code generation for SPMD

When -simd=false: spmdLaneCount returns 1, makeLLVMType for SPMDType
returns scalar T (not <1 x T>), spmdUsesSIMD() gates WASM-specific
SIMD paths (mask wrapping, anytrue/alltrue intrinsics, relaxed
swizzle). Combined with SIMD128 feature suppression, produces pure
scalar WASM with identical computation results.
```

---

## Task 3: Dual-mode E2E testing

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh`

### Step 1: Add dual-mode test level

Add Level 8 to the E2E script. Only test examples that produce **scalar output** (via `reduce.Add`, etc.) — tests that print varying values with `%v` will have format differences (`[x]` vs `[x y z w]`) which are correct but cosmetically different.

```bash
# ========== LEVEL 8: Dual-mode testing (SIMD vs scalar) ==========
printf "\n${BLUE}--- Level 8: Dual-mode (SIMD vs scalar) ---${NC}\n"

test_dual_mode() {
    local name="$1" src="$2" extra="${3:--scheduler=none}"
    local simd_out="$OUTDIR/${name}-simd.wasm"
    local scalar_out="$OUTDIR/${name}-scalar.wasm"
    TOTAL=$((TOTAL + 1))

    # Compile SIMD version
    if ! compile "$src" "$simd_out" "$extra" >/dev/null 2>&1; then
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "SIMD compile failed"
        return 1
    fi
    # Compile scalar version
    if ! compile "$src" "$scalar_out" "$extra -simd=false" >/dev/null 2>&1; then
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "Scalar compile failed"
        return 1
    fi
    COMPILE_PASS=$((COMPILE_PASS + 1))

    # Run both and compare output
    local simd_output scalar_output
    simd_output=$(run_wasm "$simd_out" "" 2>&1 | grep -v "ExperimentalWarning\|trace-warnings")
    scalar_output=$(run_wasm "$scalar_out" "" 2>&1 | grep -v "ExperimentalWarning\|trace-warnings")

    if [ "$simd_output" = "$scalar_output" ]; then
        RUN_PASS=$((RUN_PASS + 1))
        printf "${GREEN}DUAL PASS${NC}    %-40s\n" "$name"
    else
        RUN_FAIL=$((RUN_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "outputs differ"
        diff <(echo "$simd_output") <(echo "$scalar_output") | head -5
    fi
}

# Tests with scalar-only output (reduce results, not varying %v format).
# These produce identical output regardless of lane count.
test_dual_mode "dual_simple-sum"       "$INTEG/simple-sum/main.go"
test_dual_mode "dual_odd-even"         "$INTEG/odd-even/main.go"
test_dual_mode "dual_hex-encode"       "$INTEG/hex-encode/main.go"
test_dual_mode "dual_mandelbrot"       "$INTEG/mandelbrot/main.go"
test_dual_mode "dual_bit-counting"     "$INTEG/bit-counting/main.go"
test_dual_mode "dual_lo-sum"           "$INTEG/lo-sum/main.go"
test_dual_mode "dual_lo-mean"          "$INTEG/lo-mean/main.go"
test_dual_mode "dual_lo-min"           "$INTEG/lo-min/main.go"
test_dual_mode "dual_lo-max"           "$INTEG/lo-max/main.go"
test_dual_mode "dual_lo-contains"      "$INTEG/lo-contains/main.go"
test_dual_mode "dual_lo-clamp"         "$INTEG/lo-clamp/main.go"
```

Note: tests like `debug-varying`, `to-upper`, `ipv4-parser`, `store-coalescing` print varying values with `%v` and will have format differences between modes. These can be added later with output normalization or by modifying the tests to print scalar results only.

- [ ] **Step 1a:** Add `test_dual_mode` function and Level 8
- [ ] **Step 1b:** Run: `bash test/e2e/spmd-e2e-test.sh`
- [ ] **Step 1c:** Verify all dual-mode tests pass

### Step 2: Commit

```
feat: add dual-mode E2E testing (SIMD vs scalar)

Level 8 compiles each test in both -simd=true and -simd=false modes
and verifies identical output. Validates scalar fallback correctness.
```

---

## Task 4: Fix scalar mode issues (iteration)

Expected issues and fixes:

1. **WASM intrinsic calls without SIMD128 feature**: If `spmdUsesSIMD()` replacement was incomplete, LLVM will fail to lower intrinsics. Fix: audit remaining `spmdIsWASM()` calls.

2. **Type mismatches in peeled SSA**: If any code path creates vector types despite `spmdLaneCount()` returning 1, phi nodes will have type mismatches. Fix: trace the error to the callsite, add laneCount=1 guard.

3. **`lanes.Count()` returns 1**: Code using `i += lanes.Count(c)` increments by 1 instead of 4. This is correct (scalar processes one element at a time) and should produce identical final results.

4. **`spmdVectorAnyTrue`/`AllTrue` with scalar mask**: With scalar bool type (i1 or i32), these functions should return the scalar directly (a single lane is trivially "any" and "all"). Add: `if !c.spmdUsesSIMD() { return mask }` early return.

- [ ] **Step 1:** Debug and fix compilation failures
- [ ] **Step 2:** Debug and fix output mismatches
- [ ] **Step 3:** Run full E2E in both modes
- [ ] **Step 4:** Commit fixes

---

## Risk Notes

- **Peeled SSA with laneCount=1**: The SSA peeling (computed from type checker's laneCount=4) creates main+tail phases. With TinyGo laneCount=1, the main loop processes 1 element/iteration. The tail loop also processes 1 element/iteration but runs for 0 iterations (bound = `N - (N % 1) = N`, so main handles everything). LLVM should eliminate the dead tail path.

- **Printf output format**: `%v` on varying shows `[x]` vs `[x y z w]`. Dual-mode tests use only scalar outputs to avoid this.

- **Performance**: Scalar mode is for correctness testing only. The loop body has SPMD overhead (mask checks, SPMDSelect) with no vectorization. This is intentional — it validates the SPMD semantics, not performance.

- **No Go toolchain changes**: Type checker still computes laneCount=4/16. SSA still peels. Only TinyGo overrides. Same Go build cache works for both modes.
