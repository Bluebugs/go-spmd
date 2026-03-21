# Relaxed SIMD Support for Go SPMD

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable WASM Relaxed SIMD and use `i32x4.relaxed_dot_i8x16_i7x16_add_s` to replace the IPv4 parser's 13-instruction decimal extraction with a single intrinsic call.

**Architecture:** Three layers: (1) enable `+relaxed-simd` LLVM feature flag alongside `+simd128` in TinyGo config, (2) upgrade `llvm.wasm.swizzle` calls to `llvm.wasm.relaxed.swizzle` when available, (3) add `lanes.DotProductI8x16Add` builtin that TinyGo intercepts and lowers to the relaxed dot intrinsic. The IPv4 parser replaces its `h*100 + t*10 + o` computation with a single call to this builtin.

**Tech Stack:** TinyGo (LLVM 19.1.2), Go frontend (lanes package), WASM Relaxed SIMD

---

## Background

### The decimal extraction gap

After swizzle, bytes are arranged as `[h0,t0,o0,0, h1,t1,o1,0, h2,t2,o2,0, h3,t3,o3,0]`. Computing `h*100 + t*10 + o` for all 4 fields currently takes ~13 WASM instructions (3× swizzle to extract h/t/o columns, 3× i16x8.extend, 2× i32x4.mul, 2× i32x4.add).

### The relaxed_dot solution

`i32x4.relaxed_dot_i8x16_i7x16_add_s(shuffled, weights, zero)` computes:
```
result[0] = shuffled[0]*weights[0] + shuffled[1]*weights[1] + shuffled[2]*weights[2] + shuffled[3]*weights[3] + 0
         = h0*100 + t0*10 + o0*1 + 0*0 = h0*100 + t0*10 + o0
result[1] = h1*100 + t1*10 + o1
result[2] = h2*100 + t2*10 + o2
result[3] = h3*100 + t3*10 + o3
```

With `weights = [16]byte{100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0}` and accumulator = `[4]int{0,0,0,0}`.

**One instruction replaces ~13.** The result is `<4 x i32>` with all 4 field values.

### LLVM intrinsic

```
@llvm.wasm.relaxed.dot.i8x16.i7x16.add.signed(<16 x i8>, <16 x i8>, <4 x i32>) → <4 x i32>
```

Available in LLVM 19.1.2 when `+relaxed-simd` feature is enabled.

---

## File Map

| File | Change |
|------|--------|
| `tinygo/compileopts/config.go` | Add `+relaxed-simd` alongside `+simd128` |
| `tinygo/compileopts/config_spmd_test.go` | Test for relaxed-simd feature |
| `tinygo/compiler/spmd.go` | Upgrade swizzle calls to relaxed variant; add `spmdRelaxedDotI8x16Add` helper; intercept `lanes.DotProductI8x16Add` builtin |
| `tinygo/compiler/spmd_llvm_test.go` | Tests for relaxed swizzle and dot product lowering |
| `go/src/lanes/lanes.go` | Add `DotProductI8x16Add` function stub |
| `test/integration/spmd/ipv4-parser/main.go` | Restructure decimal extraction to use `lanes.DotProductI8x16Add` |

---

## Chunk 1: Feature Flag + Relaxed Swizzle

### Task 1: Enable `+relaxed-simd` feature flag

**Files:**
- Modify: `tinygo/compileopts/config.go:71-82`
- Test: `tinygo/compileopts/config_spmd_test.go`

- [ ] **Step 1: Write test for relaxed-simd feature**

Add a test to `config_spmd_test.go` that verifies `+relaxed-simd` appears in the feature string when SPMD + WASM:

```go
func TestSPMDAutoRelaxedSIMD(t *testing.T) {
    // ... setup config with GOEXPERIMENT=spmd, GOARCH=wasm ...
    features := config.Features()
    if !strings.Contains(features, "+relaxed-simd") {
        t.Errorf("Features() = %q, want +relaxed-simd", features)
    }
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compileopts/ -run TestSPMDAutoRelaxedSIMD -v
```

- [ ] **Step 3: Add `+relaxed-simd` to the feature string**

In `config.go`, after the `+simd128` append block, add:

```go
if !strings.Contains(features, "+relaxed-simd") {
    features = features + ",+relaxed-simd"
}
```

- [ ] **Step 4: Run test — expect PASS**

- [ ] **Step 5: Run full config tests**

```bash
/home/cedric/work/SPMD/go/bin/go test ./compileopts/ -run TestSPMD -v
```

- [ ] **Step 6: Commit**

```bash
git add compileopts/config.go compileopts/config_spmd_test.go
git commit -m "compileopts: auto-enable +relaxed-simd for WASM SPMD targets"
```

---

### Task 2: Upgrade swizzle to relaxed variant

**Files:**
- Modify: `tinygo/compiler/spmd.go`
- Test: `tinygo/compiler/spmd_llvm_test.go`

When `+relaxed-simd` is available, emit `llvm.wasm.relaxed.swizzle` instead of `llvm.wasm.swizzle`. The relaxed variant maps directly to `pshufb` on x86 without index sanitization overhead.

- [ ] **Step 1: Add helper to check for relaxed-simd**

In `spmd.go`, add:

```go
// spmdHasRelaxedSIMD returns true if the target supports Relaxed SIMD.
func (b *builder) spmdHasRelaxedSIMD() bool {
    return b.spmdIsWASM() && strings.Contains(b.machine.Features(), "+relaxed-simd")
}
```

If `b.machine.Features()` is not accessible, check `b.Config` or the target features string from compileopts.

- [ ] **Step 2: Replace swizzle calls**

Find all 3 locations where `llvm.wasm.swizzle` is emitted (in `spmdSwizzleWithTable`, `spmdCoalescedGather`, `spmdExtractGatherColumn`). At each location, change:

```go
intrinsicName := "llvm.wasm.swizzle"
// becomes:
intrinsicName := "llvm.wasm.swizzle"
if b.spmdHasRelaxedSIMD() {
    intrinsicName = "llvm.wasm.relaxed.swizzle"
}
```

- [ ] **Step 3: Add LLVM test**

```go
func TestSPMDRelaxedSwizzle(t *testing.T) {
    // Compile SPMD program with swizzle, verify LLVM IR contains
    // "llvm.wasm.relaxed.swizzle" instead of "llvm.wasm.swizzle"
}
```

- [ ] **Step 4: Run tests**

```bash
/home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

- [ ] **Step 5: Commit**

```bash
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "spmd: use relaxed swizzle when +relaxed-simd is available"
```

---

## Chunk 2: DotProduct Builtin + IPv4 Restructuring

### Task 3: Add `lanes.DotProductI8x16Add` to Go frontend

**Files:**
- Modify: `go/src/lanes/lanes.go`

- [ ] **Step 1: Add function stub to lanes package**

```go
// DotProductI8x16Add computes a SIMD dot product of two [16]byte vectors with
// horizontal accumulation into [4]int32. Each group of 4 input bytes produces
// one i32: result[i] = a[4i]*b[4i] + a[4i+1]*b[4i+1] + a[4i+2]*b[4i+2] + a[4i+3]*b[4i+3] + acc[i].
//
// On WASM with Relaxed SIMD, this maps to i32x4.relaxed_dot_i8x16_i7x16_add_s.
// The second argument's values must be in [-64, 63] (signed 7-bit).
//
// Example: decimal extraction from shuffled IPv4 digits:
//   weights := [16]byte{100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0}
//   values := lanes.DotProductI8x16Add(shuffled, weights, [4]int{})
//   // values[i] = h*100 + t*10 + o for each field
func DotProductI8x16Add(a, b [16]byte, acc [4]int) [4]int {
    panic("lanes.DotProductI8x16Add is a compiler intrinsic")
}
```

- [ ] **Step 2: Build Go toolchain**

```bash
cd /home/cedric/work/SPMD && make build-go
```

- [ ] **Step 3: Commit Go changes**

```bash
cd /home/cedric/work/SPMD/go
git add src/lanes/lanes.go
git commit -m "lanes: add DotProductI8x16Add for Relaxed SIMD dot product"
```

---

### Task 4: Intercept `lanes.DotProductI8x16Add` in TinyGo

**Files:**
- Modify: `tinygo/compiler/spmd.go`
- Test: `tinygo/compiler/spmd_llvm_test.go`

- [ ] **Step 1: Add the intrinsic helper**

In `spmd.go`, add:

```go
// spmdRelaxedDotI8x16Add emits i32x4.relaxed_dot_i8x16_i7x16_add_s.
// Input: two <16 x i8> vectors + one <4 x i32> accumulator.
// Output: <4 x i32> where each lane is the dot product of 4 adjacent byte pairs + acc.
func (b *builder) spmdRelaxedDotI8x16Add(a, bVec, acc llvm.Value) llvm.Value {
    i8Type := b.ctx.Int8Type()
    i32Type := b.ctx.Int32Type()
    v16i8 := llvm.VectorType(i8Type, 16)
    v4i32 := llvm.VectorType(i32Type, 4)

    intrinsicName := "llvm.wasm.relaxed.dot.i8x16.i7x16.add.signed"
    fnType := llvm.FunctionType(v4i32, []llvm.Type{v16i8, v16i8, v4i32}, false)
    fn := b.mod.NamedFunction(intrinsicName)
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, intrinsicName, fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{a, bVec, acc}, "spmd.relaxed.dot")
}
```

- [ ] **Step 2: Intercept the builtin call**

In `spmdHandleBuiltinCall` (or equivalent function that intercepts lanes.* calls), add a case for `DotProductI8x16Add`:

```go
case "DotProductI8x16Add":
    // Args: a [16]byte, b [16]byte, acc [4]int
    aVal := b.getValue(call.Args[0], pos)    // [16 x i8] or <16 x i8>
    bVal := b.getValue(call.Args[1], pos)    // [16 x i8]
    accVal := b.getValue(call.Args[2], pos)  // [4 x i32]

    // Ensure a and b are <16 x i8> vectors (may need bitcast from [16 x i8] aggregate)
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    if aVal.Type().TypeKind() == llvm.ArrayTypeKind {
        aVal = b.CreateBitCast(aVal, v16i8, "dot.a")
    }
    if bVal.Type().TypeKind() == llvm.ArrayTypeKind {
        bVal = b.CreateBitCast(bVal, v16i8, "dot.b")
    }

    // Ensure acc is <4 x i32> vector
    v4i32 := llvm.VectorType(b.ctx.Int32Type(), 4)
    if accVal.Type().TypeKind() == llvm.ArrayTypeKind {
        accVal = b.CreateBitCast(accVal, v4i32, "dot.acc")
    }

    return b.spmdRelaxedDotI8x16Add(aVal, bVal, accVal)
```

- [ ] **Step 3: Add LLVM test**

```go
func TestSPMDRelaxedDotProduct(t *testing.T) {
    // Compile SPMD program calling lanes.DotProductI8x16Add
    // Verify LLVM IR contains "llvm.wasm.relaxed.dot.i8x16.i7x16.add.signed"
}
```

- [ ] **Step 4: Run tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

- [ ] **Step 5: Commit**

```bash
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "spmd: intercept lanes.DotProductI8x16Add as relaxed dot intrinsic"
```

---

### Task 5: Restructure IPv4 parser decimal extraction

**Files:**
- Modify: `test/integration/spmd/ipv4-parser/main.go`

- [ ] **Step 1: Replace the `go for field := range 4` multiply-add loop**

Current code:
```go
go for field := range 4 {
    h := int(shuffled[field*4+0])
    t := int(shuffled[field*4+1])
    o := int(shuffled[field*4+2])
    flen := flens[field]
    value := h*100 + t*10 + o
    hasLeadingZero := (flen == 2 && t == 0) || (flen == 3 && h == 0)
    if reduce.Any(hasLeadingZero) { return [4]byte{}, 5, 0 }
    if reduce.Any(value > 255) { return [4]byte{}, 6, 0 }
    ip[field] = uint8(value)
}
```

Replace with:
```go
// Decimal extraction via relaxed dot product: h*100 + t*10 + o*1 + pad*0
// for all 4 fields simultaneously in one SIMD instruction.
weights := [16]byte{100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0}
values := lanes.DotProductI8x16Add(shuffled, weights, [4]int{})

// Leading zero check: still needs h/t/flen per field.
// Extract h and t columns from shuffled for the leading-zero check.
go for field := range 4 {
    h := int(shuffled[field*4+0])
    t := int(shuffled[field*4+1])
    flen := flens[field]
    value := values[field]

    hasLeadingZero := (flen == 2 && t == 0) || (flen == 3 && h == 0)
    if reduce.Any(hasLeadingZero) {
        return [4]byte{}, 5, 0
    }
    if reduce.Any(value > 255) {
        return [4]byte{}, 6, 0
    }
    ip[field] = uint8(value)
}
```

Note: The `go for field := range 4` loop still exists for leading-zero validation and result extraction. The `h*100 + t*10 + o` multiply-add is replaced with `lanes.DotProductI8x16Add`. The `values[field]` access inside the loop extracts the pre-computed dot product result per field.

- [ ] **Step 2: Verify correctness**

```bash
cd /home/cedric/work/SPMD
make build-go && make build-tinygo GO=$(pwd)/go/bin/go
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-relaxed.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-relaxed.wasm 2>&1 | head -15
```

Expected: all 10 test cases correct, "Correctness: SPMD and scalar results match."

- [ ] **Step 3: Verify relaxed_dot in WASM output**

```bash
wasm2wat /tmp/ipv4-relaxed.wasm > /tmp/ipv4r.wat
LINE=$(grep -n 'func \$main.parseIPv4Inner ' /tmp/ipv4r.wat | head -1 | cut -d: -f1)
NEXT=$(awk -v s=$((LINE+1)) 'NR>=s && /^  \(func /{print NR; exit}' /tmp/ipv4r.wat)
echo "relaxed_dot: $(sed -n "${LINE},${NEXT}p" /tmp/ipv4r.wat | grep -c 'i32x4.dot_i8x16_i7x16_add_s')"
echo "relaxed_swizzle: $(sed -n "${LINE},${NEXT}p" /tmp/ipv4r.wat | grep -c 'i8x16.relaxed_swizzle')"
echo "i32x4.mul: $(sed -n "${LINE},${NEXT}p" /tmp/ipv4r.wat | grep -c 'i32x4.mul')"
echo "total lines: $((NEXT - LINE))"
```

Targets:
- `relaxed_dot`: 1 (the decimal extraction)
- `relaxed_swizzle`: 4 (upgraded from standard swizzle)
- `i32x4.mul`: 0 (replaced by relaxed_dot)
- Total lines: ~220 (down from 233)

- [ ] **Step 4: Run full E2E**

```bash
bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -5
```

- [ ] **Step 5: Commit IPv4 parser changes**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/ipv4-parser/main.go
git commit -m "perf: use lanes.DotProductI8x16Add for IPv4 decimal extraction"
```

---

## Chunk 3: E2E Verification

### Task 6: Performance analysis

- [ ] **Step 1: Build and benchmark**

```bash
cd /home/cedric/work/SPMD
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-relaxed-final.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-relaxed-final.wasm 2>&1 | head -25
```

- [ ] **Step 2: Instruction count comparison**

Count total lines, compare with 233 (current). Expected reduction: ~10-13 instructions (from eliminating the 3× swizzle + 3× extend + 2× mul + 2× add chain, replaced by 1 relaxed_dot + some residual h/t extraction for leading-zero check).

- [ ] **Step 3: Update submodule pointers and commit**

```bash
cd /home/cedric/work/SPMD
git add tinygo go
git commit -m "chore: update submodules for Relaxed SIMD support"
```

---

## Edge Cases

| Case | Handling |
|------|---------|
| Non-WASM targets | `spmdHasRelaxedSIMD()` returns false; standard swizzle used |
| WASM without relaxed-simd | Feature string check returns false; standard path |
| Weights value > 127 | Not applicable — weights are [100, 10, 1, 0], all ≤ 127 (i7 range) |
| Digit values > 127 | Not applicable — digits are 0-9 (unsigned bytes), `relaxed_dot` treats first arg as unsigned |
| Accumulator non-zero | Supported but IPv4 parser uses zero accumulator |
| `lanes.DotProductI8x16Add` called outside SPMD | Panics (stub function in lanes package) |
| Leading-zero check | Still needs h/t extraction — the relaxed_dot computes the sum but doesn't expose individual terms. The `go for field := range 4` loop remains for this check. |

## Expected Results

| Metric | Before | After Relaxed SIMD |
|--------|--------|--------------------|
| `i32x4.mul` | 2 | 0 |
| `i8x16.swizzle` (standard) | 4 | 0 |
| `i8x16.relaxed_swizzle` | 0 | 4 |
| `i32x4.dot_i8x16...` | 0 | 1 |
| Total function lines | 233 | ~215-220 |
| Hot-path instrs | ~179 | ~165-170 |
