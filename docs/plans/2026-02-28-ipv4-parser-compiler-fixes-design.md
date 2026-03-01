# IPv4 Parser Compiler Fixes Design

**Date**: 2026-02-28
**Goal**: Fix TinyGo SPMD compiler bugs blocking the ipv4-parser example, then use it as a benchmark.

## Current State

The ipv4-parser exercises advanced SPMD features: 4 `go for` loops with mixed lane counts (16-lane byte, 16-lane bool, 4-lane int), varying switch, reduce operations, and varying array indexing. Compilation produces 17 LLVM verification errors across 4 root causes.

### Test Cases

- **Simplified test** (`/tmp/ipv4-test.go`): 4-lane only, scalar dot-finding + SPMD field conversion. 3 errors.
- **Minimal switch test** (`/tmp/ipv4-minimal.go`): Varying switch with constant case values. 2 errors.
- **Full ipv4-parser** (`examples/ipv4-parser/main.go`): All 4 loops, 17 errors.

## Bug Analysis

### Bug A: Deferred Switch Phi Has Scalar Type

**Errors**: 3 (empty phi, icmp type mismatch, trunc type mismatch)

**Root cause**: At `compiler.go:4055`, the deferred switch phi is created with `getLLVMType(expr.Type())`. The go/ssa phi's type comes from the declared Go variable (e.g., `var value int` → `int`), not the runtime varying type (`Varying[int]`). This produces a scalar `i32` phi instead of `<4 x i32>`.

After deferred resolution replaces the phi with a `<4 x i32>` cascaded select (via `ReplaceAllUsesWith`), all instructions that were compiled using the scalar phi now have type mismatches:
- `icmp sgt <4 x i32> %select, i32 255` — scalar 255 not broadcast
- `trunc <4 x i32> %select to i8` — should be `<4 x i8>`

Additionally, `spmdCreateSwitchMergeSelect` (`spmd.go:5361`) computes `phiType` from `phi.Type()` (scalar), so when all case edge values are scalar constants, the entire cascaded select stays scalar. The `spmdBroadcastMatch` at line 5384 doesn't fire because both operands are scalar.

**Fix**:
1. In `spmd.go:spmdCreateSwitchMergeSelect`, determine the correct vector type from the switch chain's lane count (already available via `chain → spmdSwitchChains[chainIdx]` → active loop).
2. Force-splat the initial result and all case values to the vector type before building the cascaded select.
3. In `compiler.go:4055`, create the deferred phi with the vectorized type.

### Bug B: Varying[bool] Representation Collision

**Errors**: ~8 (AND mismatches, PHI type mismatches, select mismatches, insertelement mismatches)

**Root cause**: `Varying[bool]` always maps to `<16 x i1>` (128 bits / 1 bit = 16 lanes) in `getLLVMType`, regardless of SPMD loop context. But comparisons in a 4-lane int loop produce `<4 x i1>` → `<4 x i32>` (WASM wrapped). The `changetype Varying[bool] <- bool` at `compiler.go:3552-3561` passes through the source value unchanged (correct behavior — preserves the actual lane count). But phi nodes and select instructions that merge these values with `<16 x i1>` constants (like `true:Varying[bool]` → `<16 x i1> splat(true)`) break.

The specific SSA pattern causing this:
```
t131 = phi [28: true:Varying[bool], 30: t130] #||
```
Edge from block 28 carries `true:Varying[bool]` → `<16 x i1> <true, true, ...>` (16 elements).
Edge from block 30 carries `t130` → `<4 x i32>` (WASM-wrapped comparison from 4-lane loop).
The phi tries to merge `<16 x i1>` with `<4 x i32>` — type mismatch.

**Fix**:
1. When creating LLVM constants for `Varying[bool]` values inside SPMD loops, use the loop's lane count (not 128/1=16).
2. Add a helper `spmdEffectiveBoolType(laneCount)` that returns the correct vector type for bool values in the current loop context: `<4 x i32>` for 4-lane loops on WASM, `<16 x i8>` for 16-lane loops on WASM.
3. In the `createConst` path for `Varying[bool]` constants, use the active loop's lane count.
4. In `changetype` for `Varying[bool]`, when the source is a WASM-wrapped mask, match the destination type to the source instead of using the SPMDType-derived type.

### Bug C: Deferred Phi Select Placement

**Errors**: 1 (domination error, minimal test only)

**Root cause**: The deferred resolution at `compiler.go:1728-1733` places the cascaded select at `SetInsertPointBefore(doneBlock.LastInstruction())` — just before the terminator. But the deferred phi's uses (compiled during block traversal) were placed earlier in the same block. After `ReplaceAllUsesWith`, uses reference the select which is defined after them → domination violation.

**Fix**: Move the select's insert point to just after the deferred phi's position (before the first use in the block), or create the select at the same position as the original phi. Since the phi is erased after resolution, insert the select at the phi's position using `SetInsertPointBefore(dsp.llvm)`.

### Bug D: Multi-Lane-Count Mask Interactions

**Errors**: Potentially 2-3 remaining after A-C fixes

**Root cause**: The mask stack contains masks from the current loop's lane count. When values flow between loops of different widths (e.g., 16-lane → 4-lane via shared `dotMask` array), mask operations may encounter mismatched widths.

**Fix**: This may be naturally resolved by fixes A-C, since the mask stack is re-initialized at each loop entry (line 1570/1676). Verify after implementing A-C and fix remaining issues if any.

## Implementation Order

1. **Bug A**: Deferred switch phi vectorization + cascaded select vectorization
2. **Bug C**: Select placement fix (test with minimal switch test)
3. **Bug B**: Varying[bool] lane-count-aware representation
4. **Bug D**: Verify and fix remaining multi-lane issues
5. **Benchmark**: Add timing harness, compile with SIMD, run benchmarks

## Test Strategy

- Verify minimal switch test compiles and runs after Bug A + C fixes
- Verify simplified ipv4 test compiles and runs after all fixes
- Verify full ipv4-parser compiles and runs
- Add as E2E test (promote from compile-fail to run-pass)
- Compare SPMD vs scalar performance

## Key Files

- `tinygo/compiler/compiler.go`: Deferred phi creation (line 4055), select placement (line 1728), ChangeType handler (line 3509)
- `tinygo/compiler/spmd.go`: `spmdCreateSwitchMergeSelect` (line 5322), mask helpers
- `examples/ipv4-parser/main.go`: Source example
- `test/integration/spmd/ipv4-parser/main.go`: Integration test copy
