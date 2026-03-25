# x86 SIMD Intrinsic Replacements (Phase 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace 6 WASM-only LLVM intrinsics with target-dispatching wrappers so SPMD code compiles and runs correctly on native x86-64 with SSE2/SSSE3.

**Architecture:** Create a single `spmd_x86.go` file with x86 intrinsic helpers. Refactor the 6 existing WASM intrinsic call sites into target-dispatching wrappers that call x86 intrinsics when `spmdIsX86()`. No new SSA instructions or Go frontend changes — purely TinyGo compiler work.

**Tech Stack:** TinyGo LLVM codegen, LLVM 19.1.2 x86 intrinsics (SSE2, SSSE3)

---

## Intrinsic Mapping

| WASM Intrinsic | Call sites | x86 Replacement | Required feature |
|---------------|-----------|-----------------|-----------------|
| `llvm.wasm.swizzle` | 3 (lines 5625, 5846, 5931) | `llvm.x86.ssse3.pshuf.b.128` | SSSE3 |
| `llvm.wasm.relaxed.swizzle` | Same 3 (conditional) | `llvm.x86.ssse3.pshuf.b.128` | SSSE3 |
| `llvm.wasm.bitmask.vNiW` | 1 (line 1963) | `llvm.x86.sse2.pmovmskb.128` | SSE2 |
| `llvm.wasm.anytrue.vNiW` | 1 (line 1931) | pmovmskb + `icmp ne 0` | SSE2 |
| `llvm.wasm.alltrue.vNiW` | 1 (line 1946) | pmovmskb + `icmp eq 0xFFFF` | SSE2 |
| `llvm.wasm.relaxed.dot...add.signed` | 1 (line 2023) | `pmaddubsw` + `pmaddwd` + `add` | SSSE3 |

## File Map

| File | Change |
|------|--------|
| `tinygo/compiler/spmd_x86.go` | Create: 4 x86 intrinsic helpers (pshufb, pmovmskb, pmaddubsw, pmaddwd) |
| `tinygo/compiler/spmd.go` | Modify: refactor 6 intrinsic call sites into target-dispatching wrappers |
| `tinygo/compiler/spmd_llvm_test.go` | Test: verify x86 intrinsics emitted for x86 triple |

---

## Chunk 1: x86 Intrinsic Helpers + Target Dispatch

### Task 1: Create `spmd_x86.go` with intrinsic helpers

**Files:**
- Create: `tinygo/compiler/spmd_x86.go`

- [ ] **Step 1: Create the file with 4 intrinsic wrappers**

```go
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
    return b.createCall(fnType, fn, []llvm.Value{table, indices}, "x86.pshufb")
}

// spmdX86Pmovmskb emits llvm.x86.sse2.pmovmskb.128.
// Extracts the MSB of each byte lane into a 16-bit scalar.
func (b *builder) spmdX86Pmovmskb(vec llvm.Value) llvm.Value {
    i32Type := b.ctx.Int32Type()
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    fnType := llvm.FunctionType(i32Type, []llvm.Type{v16i8}, false)
    fn := b.mod.NamedFunction("llvm.x86.sse2.pmovmskb.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.sse2.pmovmskb.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{vec}, "x86.pmovmskb")
}

// spmdX86Pmaddubsw emits llvm.x86.ssse3.pmadd.ub.sw.128.
// Multiplies u8×i8 pairs, horizontally adds adjacent products → i16.
func (b *builder) spmdX86Pmaddubsw(a, bVec llvm.Value) llvm.Value {
    v8i16 := llvm.VectorType(b.ctx.Int16Type(), 8)
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    fnType := llvm.FunctionType(v8i16, []llvm.Type{v16i8, v16i8}, false)
    fn := b.mod.NamedFunction("llvm.x86.ssse3.pmadd.ub.sw.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.ssse3.pmadd.ub.sw.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{a, bVec}, "x86.pmaddubsw")
}

// spmdX86Pmaddwd emits llvm.x86.sse2.pmadd.wd.128.
// Multiplies i16 pairs, horizontally adds → i32.
func (b *builder) spmdX86Pmaddwd(a, bVec llvm.Value) llvm.Value {
    v4i32 := llvm.VectorType(b.ctx.Int32Type(), 4)
    v8i16 := llvm.VectorType(b.ctx.Int16Type(), 8)
    fnType := llvm.FunctionType(v4i32, []llvm.Type{v8i16, v8i16}, false)
    fn := b.mod.NamedFunction("llvm.x86.sse2.pmadd.wd.128")
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, "llvm.x86.sse2.pmadd.wd.128", fnType)
    }
    return b.createCall(fnType, fn, []llvm.Value{a, bVec}, "x86.pmaddwd")
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go build ./compiler/ 2>&1 | head -10
```

- [ ] **Step 3: Commit**

```bash
git add compiler/spmd_x86.go
git commit -m "spmd: add x86 SSSE3/SSE2 intrinsic helpers (pshufb, pmovmskb, pmaddubsw, pmaddwd)"
```

---

### Task 2: Refactor swizzle call sites (3 sites)

**Files:**
- Modify: `tinygo/compiler/spmd.go`

All 3 swizzle call sites (lines ~5625, ~5846, ~5931) follow the same pattern:

```go
intrinsicName := "llvm.wasm.swizzle"
if b.spmdHasRelaxedSIMD() {
    intrinsicName = "llvm.wasm.relaxed.swizzle"
}
```

Replace each with a call to a new target-dispatching wrapper:

- [ ] **Step 1: Add `spmdSwizzle` dispatcher**

In `spmd.go`, add near the existing swizzle functions:

```go
// spmdSwizzle emits a byte-permute operation for the current target.
// On WASM: llvm.wasm.swizzle or llvm.wasm.relaxed.swizzle.
// On x86 with SSSE3: llvm.x86.ssse3.pshuf.b.128 (pshufb).
// Fallback: per-lane extractelement/insertelement.
func (b *builder) spmdSwizzle(table, indices llvm.Value) llvm.Value {
    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    if b.spmdIsWASM() {
        intrinsicName := "llvm.wasm.swizzle"
        if b.spmdHasRelaxedSIMD() {
            intrinsicName = "llvm.wasm.relaxed.swizzle"
        }
        fnType := llvm.FunctionType(v16i8, []llvm.Type{v16i8, v16i8}, false)
        fn := b.mod.NamedFunction(intrinsicName)
        if fn.IsNil() {
            fn = llvm.AddFunction(b.mod, intrinsicName, fnType)
        }
        return b.createCall(fnType, fn, []llvm.Value{table, indices}, "spmd.swizzle")
    }
    if b.spmdHasSSSE3() {
        return b.spmdX86Pshufb(table, indices)
    }
    // Scalar fallback: LLVM shufflevector with variable indices
    // (LLVM will scalarize this, but it's correct)
    return b.spmdSwizzleScalarFallback(table, indices)
}
```

Where `spmdSwizzleScalarFallback` does per-lane extract/insert:

```go
func (b *builder) spmdSwizzleScalarFallback(table, indices llvm.Value) llvm.Value {
    i8Type := b.ctx.Int8Type()
    i32Type := b.ctx.Int32Type()
    v16i8 := llvm.VectorType(i8Type, 16)
    result := llvm.ConstNull(v16i8)
    for i := 0; i < 16; i++ {
        idx := b.CreateExtractElement(indices, llvm.ConstInt(i32Type, uint64(i), false), "")
        val := b.CreateExtractElement(table, idx, "")
        // OOB → 0: check if idx >= 16 or bit 7 set
        oob := b.CreateICmp(llvm.IntUGE, idx, llvm.ConstInt(i8Type, 16, false), "")
        val = b.CreateSelect(oob, llvm.ConstInt(i8Type, 0, false), val, "")
        result = b.CreateInsertElement(result, val, llvm.ConstInt(i32Type, uint64(i), false), "")
    }
    return result
}
```

- [ ] **Step 2: Replace all 3 swizzle call sites with `b.spmdSwizzle(table, indices)`**

At each of the 3 locations, replace the inline intrinsic call block with a single call to `b.spmdSwizzle(tableVec, idxVec)`.

- [ ] **Step 3: Run WASM tests (must still pass)**

```bash
/home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
git add compiler/spmd.go
git commit -m "spmd: refactor swizzle to target-dispatching wrapper (WASM + x86 pshufb)"
```

---

### Task 3: Refactor bitmask/anytrue/alltrue (3 functions)

**Files:**
- Modify: `tinygo/compiler/spmd.go`

- [ ] **Step 1: Add target-dispatching wrappers**

Replace `spmdWasmBitmask`, `spmdWasmAnyTrue`, `spmdWasmAllTrue` with target-dispatching versions. On x86, ALL THREE use `pmovmskb` as the core operation:

```go
// spmdBitmask extracts the MSB of each byte lane into a scalar bitmask.
func (b *builder) spmdBitmask(vec llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        return b.spmdWasmBitmask(vec)
    }
    if b.spmdIsX86() {
        // pmovmskb requires <16 x i8> input. Bitcast if needed.
        v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
        if vec.Type() != v16i8 {
            vec = b.CreateBitCast(vec, v16i8, "bitmask.cast")
        }
        return b.spmdX86Pmovmskb(vec)
    }
    // Scalar fallback: loop over lanes, extract MSB, OR into result
    panic("spmdBitmask: unsupported target")
}

// spmdAnyTrue tests if any lane in the vector is nonzero.
func (b *builder) spmdAnyTrue(vec llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        return b.spmdWasmAnyTrue(vec)
    }
    if b.spmdIsX86() {
        v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
        if vec.Type() != v16i8 {
            vec = b.CreateBitCast(vec, v16i8, "anytrue.cast")
        }
        mask := b.spmdX86Pmovmskb(vec)
        zero := llvm.ConstInt(b.ctx.Int32Type(), 0, false)
        ne := b.CreateICmp(llvm.IntNE, mask, zero, "anytrue")
        return b.CreateZExt(ne, b.ctx.Int32Type(), "anytrue.i32")
    }
    panic("spmdAnyTrue: unsupported target")
}

// spmdAllTrue tests if all lanes in the vector are nonzero.
func (b *builder) spmdAllTrue(vec llvm.Value) llvm.Value {
    if b.spmdIsWASM() {
        return b.spmdWasmAllTrue(vec)
    }
    if b.spmdIsX86() {
        v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
        if vec.Type() != v16i8 {
            vec = b.CreateBitCast(vec, v16i8, "alltrue.cast")
        }
        mask := b.spmdX86Pmovmskb(vec)
        allOnes := llvm.ConstInt(b.ctx.Int32Type(), 0xFFFF, false)
        eq := b.CreateICmp(llvm.IntEQ, mask, allOnes, "alltrue")
        return b.CreateZExt(eq, b.ctx.Int32Type(), "alltrue.i32")
    }
    panic("spmdAllTrue: unsupported target")
}
```

- [ ] **Step 2: Update all callers of `spmdWasmAnyTrue`/`spmdWasmAllTrue`/`spmdWasmBitmask`**

Search for all calls to these 3 functions in `spmd.go` and replace with the new dispatcher names (`spmdBitmask`, `spmdAnyTrue`, `spmdAllTrue`). The existing `spmdWasm*` functions become private helpers called only by the dispatchers.

Note: callers that check `spmdIsWASM()` before calling `spmdWasm*` can now call the dispatcher unconditionally since it handles target dispatch internally.

- [ ] **Step 3: Run tests**

```bash
/home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
git add compiler/spmd.go
git commit -m "spmd: refactor bitmask/anytrue/alltrue to target-dispatching wrappers (WASM + x86 pmovmskb)"
```

---

### Task 4: Refactor DotProductI8x16Add for x86

**Files:**
- Modify: `tinygo/compiler/spmd.go`

- [ ] **Step 1: Add x86 path in `createLanesBuiltin` DotProductI8x16Add handler**

In the SIMD path (around line 2618), change the `!b.spmdHasRelaxedSIMD()` error to an x86 fallback:

```go
case name == "lanes.DotProductI8x16Add":
    pos := getPos(instr)
    aVal := b.getValue(instr.Args[0], pos)
    bVal := b.getValue(instr.Args[1], pos)
    accVal := b.getValue(instr.Args[2], pos)

    v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
    v4i32 := llvm.VectorType(b.ctx.Int32Type(), 4)

    // Aggregate → vector conversion
    if aVal.Type().TypeKind() == llvm.ArrayTypeKind {
        aVal = b.spmdAggregateToVector(aVal, v16i8, 16, "dot.a")
    }
    if bVal.Type().TypeKind() == llvm.ArrayTypeKind {
        bVal = b.spmdAggregateToVector(bVal, v16i8, 16, "dot.b")
    }
    if accVal.Type().TypeKind() == llvm.ArrayTypeKind {
        accVal = b.spmdAggregateToVector(accVal, v4i32, 4, "dot.acc")
    }

    if b.spmdIsWASM() && b.spmdHasRelaxedSIMD() {
        return b.spmdRelaxedDotI8x16Add(aVal, bVal, accVal), nil
    }
    if b.spmdHasSSSE3() {
        // x86: pmaddubsw(a, b) → <8 x i16>, then pmaddwd(result, ones) → <4 x i32>
        // Weight 100 fits in u8 natively — no decomposition needed!
        halfResult := b.spmdX86Pmaddubsw(aVal, bVal)  // <8 x i16>

        // pmaddwd with [1,1,1,1,1,1,1,1] to horizontally add adjacent i16 pairs → i32
        i16Type := b.ctx.Int16Type()
        ones := llvm.ConstVector([]llvm.Value{
            llvm.ConstInt(i16Type, 1, false), llvm.ConstInt(i16Type, 1, false),
            llvm.ConstInt(i16Type, 1, false), llvm.ConstInt(i16Type, 1, false),
            llvm.ConstInt(i16Type, 1, false), llvm.ConstInt(i16Type, 1, false),
            llvm.ConstInt(i16Type, 1, false), llvm.ConstInt(i16Type, 1, false),
        }, false)
        result := b.spmdX86Pmaddwd(halfResult, ones)  // <4 x i32>
        result = b.CreateAdd(result, accVal, "dot.add.acc")
        return result, nil
    }
    return llvm.Value{}, b.makeError(pos, "lanes.DotProductI8x16Add requires WASM +relaxed-simd or x86 +ssse3")
```

- [ ] **Step 2: Run tests**

```bash
/home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -20
```

- [ ] **Step 3: Commit**

```bash
git add compiler/spmd.go
git commit -m "spmd: add x86 path for DotProductI8x16Add via pmaddubsw + pmaddwd"
```

---

## Chunk 2: E2E Validation

### Task 5: Compile and run IPv4 parser on native x86-64

- [ ] **Step 1: Build TinyGo and compile IPv4 parser**

```bash
cd /home/cedric/work/SPMD && make build-tinygo GO=$(pwd)/go/bin/go
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features="+ssse3,+sse4.2,+avx2" \
  -o /tmp/ipv4-native test/integration/spmd/ipv4-parser/main.go
```

- [ ] **Step 2: Run and verify correctness**

```bash
/tmp/ipv4-native 2>&1 | head -15
```

Expected: all 10 test cases correct, "Correctness: SPMD and scalar results match."

- [ ] **Step 3: Inspect generated assembly**

```bash
objdump -d /tmp/ipv4-native | grep -A 200 'parseIPv4Inner' | grep -cE 'pshufb|pmovmskb|pmaddubsw|pmaddwd|paddd|pcmpeq|movdqu'
```

Verify SSE/SSSE3 instructions are present.

- [ ] **Step 4: Benchmark**

The benchmark is built into the binary. Run it:
```bash
/tmp/ipv4-native 2>&1 | grep -A 20 'Results'
```

Report the SPMD vs scalar speedup ratio on native x86-64.

- [ ] **Step 5: Compile all compile-pass E2E examples on native**

```bash
for dir in test/integration/spmd/*/; do
    name=$(basename $dir)
    echo -n "$name: "
    PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
      -llvm-features="+ssse3,+sse4.2,+avx2" \
      -o /tmp/native-$name $dir/main.go 2>&1 && echo "OK" || echo "FAIL"
done
```

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo && git add -A
git commit -m "spmd: validate x86-64 SPMD compilation with SSE2/SSSE3 intrinsics"
```

---

## Edge Cases

| Case | Handling |
|------|---------|
| x86 without SSSE3 (SSE2-only) | Swizzle falls back to scalar loop; DotProduct errors. SSE2 is baseline for x86-64, SSSE3 is x86-64-v2+ (2008+). |
| Non-i8x16 bitmask on x86 | Bitcast to `<16 x i8>` before `pmovmskb`. Wider masks (i16, i32) bitcast correctly since pmovmskb extracts MSB of every byte. |
| pmaddubsw saturation | `pmaddubsw` saturates to [-32768, 32767]. Max: 255*127 + 255*127 = 64770 > 32767. For the IPv4 parser weights [100,10,1,0], max = 9*100+9*10 = 990 — no saturation risk. For general use, document the saturation constraint. |
| WASM compilation unchanged | All WASM paths preserved as fallback in the dispatcher. Existing WASM tests must pass. |
