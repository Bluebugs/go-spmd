# Design Spec: LICM for SPMD Functions + All-Ones Load Fast-Path

**Date**: 2026-04-11
**Status**: Draft
**Motivation**: SPMD base64 decoder wastes ~14 instructions per iteration on (A) masked loads with an all-ones mask (should be plain vmovdqu) and (B) loop-invariant constants reloaded from memory instead of cached in registers. Fixing both brings the instruction count from 49 to ~35 per 16-byte iteration on SSSE3.

## 1. Scope

Two optimizations:

**A. All-ones mask load fast-path** (`tinygo/compiler/spmd.go`): In `createSPMDLoad`, when the mask is compile-time all-ones and the access is contiguous, emit a plain `CreateLoad` instead of `llvm.masked.load`. Eliminates mask computation and conditional dispatch for the peeled main body.

**B. Per-function LICM for SPMD** (`tinygo/compiler/spmd.go` or `compiler.go`): After completing SPMD codegen for a function, run LLVM's LICM pass on just that function. Hoists loop-invariant constants (LUT tables, mask bytes, shuffle masks) out of the hot loop. Scoped to SPMD functions only — no impact on non-SPMD code.

**Benefits all targets**: WASM, SSE, AVX2.

## 2. All-Ones Mask Load Fast-Path

### Location

`tinygo/compiler/spmd.go`, function `createSPMDLoad` (line ~8218).

### Change

Add an early check before the existing contiguous/scatter dispatch:

```go
// Fast path: all-ones mask → unconditional contiguous vector load.
// The peeled main body always has ConstAllOnes mask (set in
// emitSPMDBodyPrologue line 1653). Emit a plain vector load
// instead of llvm.masked.load to avoid mask computation.
if b.spmdIsConstAllOnesMask(mask) {
    if b.spmdContiguousPtr != nil {
        if ci, ok := b.spmdContiguousPtr[instr.Addr]; ok {
            elemAlign := int(b.targetData.TypeAllocSize(vecType.ElementType()))
            st := b.CreateLoad(vecType, ci.scalarPtr, "spmd.load.full")
            st.SetAlignment(elemAlign)
            return st
        }
    }
}
```

### Expected impact

- Eliminates `pmaxub` + `pcmpeqb` + `movd` + `pshufb` mask computation (5 instrs)
- Eliminates `jbe` branch dispatch (1 instr)
- Eliminates `vpand` masked selection (1 instr)
- Replaces `llvm.masked.load` with `vmovdqu` / `v128.load` (1 instr)
- Net: **~7 instructions saved** per iteration on SSSE3

### Verify stores

Check if `createSPMDStore` at line ~7893 already has the all-ones fast-path (the existing comment at line 7893 suggests it does: "All lanes active — direct contiguous store"). If so, no store change needed.

## 3. Per-Function LICM for SPMD

### Approach

After TinyGo finishes compiling an SPMD function (after `createFunction` returns), run LLVM's LICM pass on just that function. This is done using LLVM's per-function pass runner.

### Location

`tinygo/compiler/compiler.go`, in `createFunction` (or its caller), after the function's IR is fully generated. Alternatively in `createPackage` after each SPMD function is compiled.

### Implementation

Check if the function is SPMD (has SPMD loops or SPMD parameters). If so, run the LICM pass:

```go
// After SPMD function codegen is complete:
if isSPMDFunction {
    // Run LICM to hoist loop-invariant constants out of SPMD hot loops.
    err := fn.RunPasses("licm", targetMachine, passOptions)
}
```

The key question is whether TinyGo's LLVM Go bindings support `RunPasses` on individual functions (not modules). If `llvm.Value.RunPasses()` is not available, alternatives:

1. **Use the legacy FunctionPassManager**: Create an `llvm.PassManager` for the function, add LICM, and run it. The legacy PM API supports per-function execution.
2. **Use the new pass manager with a function adaptor**: `function(licm)` in the pass pipeline string.
3. **Run on the module but only for SPMD functions**: Mark SPMD functions with `optnone` initially, run global passes, then remove `optnone` and run LICM. (Hacky, not recommended.)

### What LICM hoists

For the base64 SPMD loop:
- `vpbroadcastb` of LUT table → loaded once into ymm register before loop
- `vpbroadcastb` of mask constants (0x0F, 0x3F, etc.) → hoisted
- Constant shuffle mask vectors → hoisted
- Estimated: **4-5 instructions per iteration saved** on AVX2, ~3 on SSSE3, ~2-3 on WASM

### Scoping to SPMD only

Detection: a function is SPMD if:
- It contains `SPMDLoad`/`SPMDStore`/`SPMDMux`/`SPMDInterleaveStore` instructions (checked during codegen)
- Or it has `CallCommon.SPMDMask` set on any call
- Simplest: track a `bool` flag `b.hasSpmdCode` set during SPMD instruction emission

This flag is already effectively tracked — `b.spmdLoopState != nil` during SPMD loop codegen. After the function is complete, check if any SPMD loops were processed.

## 4. Files Modified

| File | Change |
|------|--------|
| `tinygo/compiler/spmd.go` | Add all-ones mask check in `createSPMDLoad` |
| `tinygo/compiler/compiler.go` | Run per-function LICM after SPMD function codegen |

## 5. Testing

Verify correctness on all targets (WASM, SSSE3, AVX2) with both base64 and compact-store tests. Run benchmarks to measure instruction count reduction.

Expected improvements at 1MB:
- SSSE3: 4505 → ~6000+ MB/s
- AVX2: 4979 → ~7000+ MB/s  
- WASM: 1636 → ~2000+ MB/s
