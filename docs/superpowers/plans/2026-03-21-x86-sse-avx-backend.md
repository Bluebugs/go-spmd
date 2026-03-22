# x86 SSE/AVX SPMD Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compile Go SPMD code to native x86-64 binaries using SSE2/SSSE3/AVX2 SIMD, producing near-Muła-Lemire instruction counts for the IPv4 parser (~28-32 instructions vs Muła's ~26).

**Architecture:** Start with SSE2/SSSE3 (128-bit, same width as WASM SIMD128) to minimize changes — same lane counts, same vector types. Replace 6 WASM-specific LLVM intrinsics with x86 equivalents. Generalize `spmdUsesSIMD()` from WASM-only to any target with vector support. Remove WASM-specific workarounds (decomposition, mask narrowing) on native targets. No dynamic dispatch — the binary requires the specified CPU features.

**Tech Stack:** TinyGo, LLVM 19.1.2 (x86-64 backend), Go SPMD

---

## Current State

### What works today
- TinyGo can compile Go to x86-64 native binaries (LLVM has full x86 backend)
- SPMD codegen emits target-independent LLVM IR (`<16 x i8>`, `<4 x i32>` ops)
- LLVM auto-lowers vector add/mul/cmp to SSE2/AVX instructions

### What blocks x86 SPMD
1. **No x86-64 Linux target file** — no `targets/linux-amd64.json`
2. **`spmdUsesSIMD()` hardcoded to WASM** — returns false for all non-WASM
3. **6 WASM intrinsics** with no x86 fallback — would crash LLVM codegen
4. **23 WASM-specific workarounds** — decomposition, mask narrowing, sub-128-bit vector workarounds that aren't needed on x86
5. **Lane count hardcoded to 128 bits** — correct for SSE, wrong for AVX2 (256-bit)

### What we keep unchanged
- **128-bit vector width** (SSE2 = same as WASM SIMD128) — same lane counts, no algorithm changes
- **Same Go source code** — `go for`, `lanes.Varying[T]`, `reduce.*` — zero changes
- **Same SSA passes** — x-tools-spmd promotion, predication, peeling all target-independent
- **Same LLVM IR shape** — `<16 x i8>`, `<4 x i32>` operations already correct

---

## Intrinsic Mapping: WASM → x86

| WASM Intrinsic | Usage | x86 Equivalent | Notes |
|---------------|-------|----------------|-------|
| `llvm.wasm.swizzle` | Byte permute (OOB → 0) | `llvm.x86.ssse3.pshuf.b.128` | SSSE3. OOB (bit 7 set) → 0, same semantics |
| `llvm.wasm.relaxed.swizzle` | Byte permute (relaxed OOB) | `llvm.x86.ssse3.pshuf.b.128` | Identical — pshufb IS the relaxed behavior |
| `llvm.wasm.bitmask.v16i8` | Extract MSB per byte → i32 | `llvm.x86.sse2.pmovmskb.128` | SSE2. Exact equivalent |
| `llvm.wasm.anytrue.v16i8` | Any lane nonzero? | `pmovmskb` + `test` + `jnz` | No single intrinsic; use movemask + scalar compare |
| `llvm.wasm.alltrue.v16i8` | All lanes nonzero? | `pmovmskb` + `cmp 0xFFFF` | No single intrinsic; use movemask + scalar compare |
| `llvm.wasm.relaxed.dot.i8x16.i7x16.add.signed` | Dot product u8×i8 → i32 | `llvm.x86.ssse3.pmadd.ub.sw.128` + `llvm.x86.sse2.pmadd.wd.128` | Two x86 intrinsics (pmaddubsw + pmaddwd) replace one WASM intrinsic. Weight 100 fits in u8 natively (no decomposition needed!) |

**Key insight for relaxed_dot**: On x86, `pmaddubsw` handles unsigned×signed byte multiplication natively. The weight value 100 fits in an unsigned byte (0-255), unlike WASM's `i7x16` limit of [-64, 63]. So the IPv4 parser's `100*h + 10*t + 1*o` can be done in ONE `pmaddubsw` + ONE `pmaddwd` = 2 instructions total (vs WASM's 2× relaxed_dot because 100 > 63).

---

## File Map

| File | Change | Phase |
|------|--------|-------|
| `tinygo/targets/linux-amd64-spmd.json` | Create: native x86-64 target spec | 1 |
| `tinygo/compileopts/config.go` | Generalize SIMD feature detection for x86 | 1 |
| `tinygo/compileopts/config_spmd_test.go` | Tests for x86 feature strings | 1 |
| `tinygo/compiler/spmd.go` | Replace WASM intrinsics with target-dispatched versions; remove WASM workarounds for native | 2-3 |
| `tinygo/compiler/spmd_x86.go` | New: x86-specific intrinsic implementations | 2 |
| `tinygo/compiler/spmd_llvm_test.go` | Tests for x86 intrinsic lowering | 2 |
| `go/src/lanes/lanes.go` | No change — API is target-independent | — |

---

## Phase 1: Target Infrastructure

### Task 1: Create x86-64 Linux SPMD target

**Files:**
- Create: `tinygo/targets/linux-amd64-spmd.json`

- [ ] **Step 1: Create target spec**

Based on TinyGo's existing x86-64 support (`compileopts/target.go:278`), create a minimal target JSON:

```json
{
  "inherits": ["linux"],
  "llvm-target": "x86_64-unknown-linux-gnu",
  "cpu": "x86-64-v3",
  "features": "+cmov,+cx8,+fxsr,+mmx,+sse,+sse2,+sse3,+ssse3,+sse4.1,+sse4.2,+popcnt,+avx,+avx2,+x87",
  "build-tags": ["linux", "amd64", "spmd_native"],
  "gc": "conservative",
  "scheduler": "threads",
  "linker": "cc",
  "cflags": ["-march=x86-64-v3"],
  "ldflags": ["-lpthread"]
}
```

Notes:
- `x86-64-v3` = Haswell+ (AVX2, FMA, BMI1/2, LZCNT). Most x86-64 CPUs since 2013.
- `+ssse3` gives us `pshufb` (byte swizzle)
- `+sse4.2` gives us `popcnt`
- `+avx2` gives us 256-bit vectors (for future width upgrade)
- `gc: conservative` — standard for TinyGo native Linux
- No dynamic dispatch — crash if CPU lacks features

- [ ] **Step 2: Verify TinyGo can compile to this target**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=linux-amd64-spmd -o /tmp/test-native test/integration/spmd/simple-sum/main.go
```

This will fail initially (SPMD SIMD not enabled for non-WASM), which is expected.

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add targets/linux-amd64-spmd.json
git commit -m "targets: add linux-amd64-spmd target for native x86-64 SPMD compilation"
```

---

### Task 2: Generalize `spmdUsesSIMD()` for x86

**Files:**
- Modify: `tinygo/compileopts/config.go`
- Modify: `tinygo/compiler/spmd.go`
- Test: `tinygo/compileopts/config_spmd_test.go`

- [ ] **Step 1: Add SIMD feature detection for x86**

In `config.go`, modify the SPMD feature detection block to also enable SIMD for x86 targets with SSE2+:

```go
// Auto-enable SIMD for SPMD targets.
if hasExperiment(c.Options.GOExperiment, "spmd") {
    if c.Target.GOARCH == "wasm" {
        // WASM: enable simd128 + relaxed-simd
        if !strings.Contains(features, "+simd128") {
            features += ",+simd128"
        }
        if !strings.Contains(features, "+relaxed-simd") {
            features += ",+relaxed-simd"
        }
    }
    // x86-64 with SSE2+: SIMD is implied by the CPU features.
    // No additional feature flags needed — SSE2 is baseline for x86-64.
}
```

- [ ] **Step 2: Generalize `spmdUsesSIMD()` in `spmd.go`**

Current:
```go
func (c *compilerContext) spmdUsesSIMD() bool {
    return c.spmdIsWASM() && c.simdEnabled
}
```

Change to:
```go
func (c *compilerContext) spmdUsesSIMD() bool {
    if c.spmdIsWASM() {
        return c.simdEnabled
    }
    // x86-64: SIMD is available when SSE2+ features are present.
    // SSE2 is baseline for x86-64, so always available.
    return c.spmdIsX86() && c.simdEnabled
}

func (c *compilerContext) spmdIsX86() bool {
    return strings.HasPrefix(c.Triple, "x86_64") || strings.HasPrefix(c.Triple, "i386")
}

func (c *compilerContext) spmdHasSSSE3() bool {
    return c.spmdIsX86() && strings.Contains(c.Features(), "+ssse3")
}

func (c *compilerContext) spmdHasAVX2() bool {
    return c.spmdIsX86() && strings.Contains(c.Features(), "+avx2")
}
```

Also set `c.simdEnabled = true` for x86 SPMD targets. Find where `simdEnabled` is set (likely in `compiler.go` init) and add the x86 path.

- [ ] **Step 3: Test**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compileopts/ -run TestSPMD -v
```

- [ ] **Step 4: Commit**

```bash
git add compileopts/config.go compiler/spmd.go compileopts/config_spmd_test.go
git commit -m "spmd: generalize SIMD detection from WASM-only to include x86-64"
```

---

## Phase 2: Intrinsic Replacements

### Task 3: Create `spmd_x86.go` with x86 intrinsic helpers

**Files:**
- Create: `tinygo/compiler/spmd_x86.go`

- [ ] **Step 1: Implement x86 intrinsic wrappers**

```go
// spmd_x86.go — x86-specific SIMD intrinsic implementations for SPMD.
package compiler

import "tinygo.org/x/go-llvm"

// spmdX86Pshufb emits llvm.x86.ssse3.pshuf.b.128 (byte permute).
// Equivalent to WASM's i8x16.swizzle: indices with bit 7 set produce 0.
func (b *builder) spmdX86Pshufb(table, indices llvm.Value) llvm.Value {
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    fnType := llvm.FunctionType(v16i8, []llvm.Type{v16i8, v16i8}, false)
    fn := b.mod.NamedFunction("llvm.x86.ssse3.pshuf.b.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.ssse3.pshuf.b.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{table, indices}, "spmd.pshufb")
}

// spmdX86Pmovmskb emits llvm.x86.sse2.pmovmskb.128 (extract MSBs → i32).
// Equivalent to WASM's i8x16.bitmask.
func (b *builder) spmdX86Pmovmskb(vec llvm.Value) llvm.Value {
    i32Type := b.ctx.Int32Type()
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    fnType := llvm.FunctionType(i32Type, []llvm.Type{v16i8}, false)
    fn := b.mod.NamedFunction("llvm.x86.sse2.pmovmskb.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.sse2.pmovmskb.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{vec}, "spmd.pmovmskb")
}

// spmdX86Pmaddubsw emits llvm.x86.ssse3.pmadd.ub.sw.128.
// Multiplies unsigned×signed byte pairs and horizontally adds → i16.
// This is the SSSE3 equivalent of WASM's relaxed_dot_i8x16_i7x16_signed
// but handles the FULL unsigned byte range (0-255), not just [-64,63].
func (b *builder) spmdX86Pmaddubsw(a, bVec llvm.Value) llvm.Value {
    v8i16 := llvm.VectorType(b.ctx.Int16Type(), 8)
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    fnType := llvm.FunctionType(v8i16, []llvm.Type{v16i8, v16i8}, false)
    fn := b.mod.NamedFunction("llvm.x86.ssse3.pmadd.ub.sw.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.ssse3.pmadd.ub.sw.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{a, bVec}, "spmd.pmaddubsw")
}

// spmdX86Pmaddwd emits llvm.x86.sse2.pmadd.wd.128.
// Multiplies i16 pairs and horizontally adds → i32.
// Used together with pmaddubsw for full decimal extraction.
func (b *builder) spmdX86Pmaddwd(a, bVec llvm.Value) llvm.Value {
    v4i32 := llvm.VectorType(b.ctx.Int32Type(), 4)
    v8i16 := llvm.VectorType(b.ctx.Int16Type(), 8)
    fnType := llvm.FunctionType(v4i32, []llvm.Type{v8i16, v8i16}, false)
    fn := b.mod.NamedFunction("llvm.x86.sse2.pmadd.wd.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.sse2.pmadd.wd.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{a, bVec}, "spmd.pmaddwd")
}
```

- [ ] **Step 2: Commit**

```bash
git add compiler/spmd_x86.go
git commit -m "spmd: add x86 SSSE3/SSE2 intrinsic helpers (pshufb, pmovmskb, pmaddubsw, pmaddwd)"
```

---

### Task 4: Target-dispatch all WASM intrinsic call sites

**Files:**
- Modify: `tinygo/compiler/spmd.go`

Replace each WASM-only intrinsic call with a target-dispatching wrapper.

- [ ] **Step 1: Replace swizzle calls (3 sites)**

Find every `"llvm.wasm.swizzle"` and `"llvm.wasm.relaxed.swizzle"` usage. Replace with:

```go
func (b *builder) spmdSwizzle(table, indices llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        intrinsicName := "llvm.wasm.swizzle"
        if b.spmdHasRelaxedSIMD() {
            intrinsicName = "llvm.wasm.relaxed.swizzle"
        }
        // ... existing WASM call ...
        return b.createCall(fnType, fn, []llvm.Value{table, indices}, "spmd.swizzle")
    }
    if b.spmdHasSSSE3() {
        return b.spmdX86Pshufb(table, indices)
    }
    // Scalar fallback: per-lane extract/insert
    return b.spmdSwizzleScalarFallback(table, indices)
}
```

- [ ] **Step 2: Replace bitmask calls (2 sites)**

```go
func (b *builder) spmdBitmask(vec llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        // existing llvm.wasm.bitmask code
    }
    if b.spmdIsX86() {
        return b.spmdX86Pmovmskb(vec)
    }
    // Scalar fallback
}
```

- [ ] **Step 3: Replace anytrue/alltrue calls**

```go
func (b *builder) spmdAnyTrue(vec llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        // existing llvm.wasm.anytrue code
    }
    if b.spmdIsX86() {
        // pmovmskb + test != 0
        mask := b.spmdX86Pmovmskb(vec)
        zero := llvm.ConstInt(b.ctx.Int32Type(), 0, false)
        return b.CreateICmp(llvm.IntNE, mask, zero, "spmd.anytrue")
    }
}
```

- [ ] **Step 4: Replace relaxed_dot with pmaddubsw + pmaddwd**

The `DotProductI8x16Add` builtin dispatch:

```go
case "DotProductI8x16Add":
    if b.spmdIsWASM() && b.spmdHasRelaxedSIMD() {
        // existing WASM relaxed_dot code
    } else if b.spmdHasSSSE3() {
        // x86: pmaddubsw(a, b) → <8 x i16>, then pmaddwd(result, ones) → <4 x i32>
        // Weight 100 fits in u8 natively — ONE pmaddubsw, no decomposition!
        i16Ones := llvm.ConstVector([]llvm.Value{
            llvm.ConstInt(b.ctx.Int16Type(), 1, false), // ... repeated 8 times
        }, false)
        halfResult := b.spmdX86Pmaddubsw(aVal, bVal)   // <8 x i16>
        result := b.spmdX86Pmaddwd(halfResult, i16Ones) // <4 x i32>
        result = b.CreateAdd(result, accVal, "")         // + accumulator
        return result, nil
    }
```

- [ ] **Step 5: Add tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

- [ ] **Step 6: Commit**

```bash
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "spmd: target-dispatch WASM intrinsics to x86 equivalents (pshufb, pmovmskb, pmaddubsw)"
```

---

## Phase 3: Remove WASM Workarounds for Native Targets

### Task 5: Skip WASM-specific codegen on x86

**Files:**
- Modify: `tinygo/compiler/spmd.go`

The 23 `spmdIsWASM()` call sites fall into these categories:

**Category A: Decomposed index (2 sites, lines 828, 1104)**
On WASM, rangeindex with >4 lanes decomposes vector indices into scalar base + byte offset (because WASM vectors are 128-bit). On x86, `<16 x i32>` (512 bits on AVX-512, or 4× 128-bit ops) works natively. For SSE (128-bit, same as WASM), the decomposition is the same — **no change needed for SSE. Change only if targeting AVX2/AVX-512.**

**Category B: Mask format (4 sites, lines 2025, 2053, 2093, 2110)**
WASM uses `<N x i32>` masks; x86 can use `<N x i1>` directly. On x86, skip the i32 mask wrapping.

```go
if b.spmdIsWASM() {
    // WASM: wrap mask as <N x i32>
} else {
    // x86: use <N x i1> directly
}
```

**Category C: Sub-128-bit vectors (5 sites, lines 4753, 4770, 4815, 4913, 5755)**
WASM can't handle `<4 x i8>` (32 bits). x86 CAN handle sub-128-bit vectors (LLVM widens them automatically). Skip the widening workaround on x86.

**Category D: Overread+mask (1 site, line 6558)**
WASM needs the bitselect-based zero-padding (16-byte guard zone). x86 can use `movdqu` (unaligned 16-byte load) without zero-padding — the OS handles page faults. But for safety with Go strings, keep the overread+mask approach (simpler, correct).

- [ ] **Step 1: Guard mask format conversions**

At each mask format site, add `if b.spmdIsWASM()` guards so x86 uses i1 masks:

```go
// Before (always wraps to i32):
mask = b.spmdWrapMask(mask)

// After:
if b.spmdIsWASM() {
    mask = b.spmdWrapMask(mask)
}
// On x86, mask stays as <N x i1> — LLVM handles it natively.
```

- [ ] **Step 2: Guard sub-128-bit vector workarounds**

At each widening site:
```go
if b.spmdIsWASM() {
    // WASM: widen <4 x i8> to <4 x i32> to avoid sub-128-bit vectors
} else {
    // x86: use <4 x i8> directly — LLVM widens automatically
}
```

- [ ] **Step 3: Test compilation on both targets**

```bash
# WASM (existing):
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o /tmp/test-wasm.wasm test/integration/spmd/simple-sum/main.go

# x86-64 (new):
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=linux-amd64-spmd -o /tmp/test-native test/integration/spmd/simple-sum/main.go
```

- [ ] **Step 4: Commit**

```bash
git add compiler/spmd.go
git commit -m "spmd: skip WASM-specific mask/widening workarounds on native x86-64 targets"
```

---

## Phase 4: E2E Validation

### Task 6: Compile and run IPv4 parser on native x86-64

- [ ] **Step 1: Build IPv4 parser as native binary**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -target=linux-amd64-spmd -o /tmp/ipv4-native \
  test/integration/spmd/ipv4-parser/main.go
```

- [ ] **Step 2: Run and verify correctness**

```bash
/tmp/ipv4-native
```

Expected: all 10 test cases correct, scalar and SPMD results match.

- [ ] **Step 3: Inspect generated assembly**

```bash
objdump -d /tmp/ipv4-native | grep -A 100 'parseIPv4Inner' | head -100
```

Look for:
- `pshufb` (SSSE3 swizzle) — should appear instead of scalar loops
- `pmovmskb` — bitmask extraction
- `pmaddubsw` — decimal extraction (if DotProductI8x16Add was used)
- `movdqu` — unaligned 16-byte load
- `popcnt` — dot count

- [ ] **Step 4: Count x86 SIMD instructions**

Count the total instructions in `parseIPv4Inner` on x86. Compare with:
- Our WASM: ~87 hot-path instructions
- Muła-Lemire x86: ~26-31 instructions
- Target: ~28-35 instructions

- [ ] **Step 5: Benchmark SPMD vs scalar on native**

The benchmark is built into the IPv4 parser binary. Run it and report the speedup ratio.

Expected: SPMD should be 0.8-1.2x vs scalar (near parity or slight win, vs 0.59x on WASM).

- [ ] **Step 6: Compile all E2E examples on native**

```bash
for dir in test/integration/spmd/*/; do
    name=$(basename $dir)
    echo -n "$name: "
    PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
      -target=linux-amd64-spmd -o /tmp/native-$name $dir/main.go 2>&1 && echo "OK" || echo "FAIL"
done
```

- [ ] **Step 7: Commit**

```bash
git commit -m "test: validate SPMD compilation and execution on native x86-64"
```

---

## Phase 5 (Future): AVX2 Width Upgrade

Not in scope for this plan, but documented for reference.

### What changes for AVX2 (256-bit):
- `spmdLaneCount()`: `32 / elemSize` instead of `16 / elemSize`
  - i8: 32 lanes (vs 16)
  - i32: 8 lanes (vs 4)
- `go/types`: `simd128CapacityBytes` → `simdCapacityBytes(target)` (parameterized)
- All `<16 x i8>` → `<32 x i8>`, all `<4 x i32>` → `<8 x i32>`
- Swizzle: `vpshufb` operates within 128-bit lanes (not across full 256 bits) — need `vperm2i128` for cross-lane shuffles
- IPv4 parser: outer SPMD would process 8 IPs simultaneously (vs 4 with 128-bit)

### What changes for AVX-512 (512-bit):
- 64 byte lanes, 16 i32 lanes
- Native masked operations (`vmovdqu8` with mask register)
- `vpermi2b` for full 64-byte byte permute
- IPv4 parser: 16 IPs simultaneously

---

## Risk Assessment

| Risk | Impact | Mitigation |
|------|--------|------------|
| TinyGo native Linux target may need runtime adjustments | Medium | Conservative GC + threads scheduler work today for ARM64 (nintendoswitch target) |
| LLVM x86 intrinsic names may differ from documented | Low | Verify with `grep` in LLVM source; all intrinsics are in IntrinsicsX86.td |
| Mask format (i1 vs i32) change cascades through many code paths | High | Start with WASM-compatible i32 masks on x86 (suboptimal but correct), then optimize |
| DotProductI8x16Add decomposition (2× relaxed_dot) → 1× pmaddubsw + 1× pmaddwd on x86 | Low | The x86 path is SIMPLER (weight 100 fits in u8) |
| IPv4 parser `createSPMDVectorFromMemory` overread+mask on x86 | Low | x86 has virtual memory + guard pages; overread from valid heap is safe. Keep bitselect approach for consistency. |
| Some SPMD examples may use WASM-specific features (wasi imports) | Medium | The native target needs its own I/O stubs (printf → libc) |

---

## Expected Results

| Metric | WASM (current) | x86-64 SSE/SSSE3 (expected) | Muła-Lemire |
|--------|---------------|----------------------------|-------------|
| IPv4 hot-path instrs | ~87 | **~28-35** | ~26 |
| `pshufb` / swizzle | 3 (relaxed) | **3** (native pshufb) | 2 |
| `pmaddubsw` | 0 (relaxed_dot×2 instead) | **1** (native, weight 100 OK) | 1 |
| `pmovmskb` / bitmask | 2 | **2** | 1 |
| Unaligned load | 1 (v128.load) + bitselect | **1** (movdqu, no masking) | 1 |
| Speedup vs scalar | 0.59x | **0.8-1.2x** (estimated) | — |
