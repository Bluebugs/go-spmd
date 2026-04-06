# SPMD Store Masking Test

This directory contains tests for the SPMD store masking bug where inactive SIMD lanes write garbage values to arrays when iteration count < SIMD width.

## Test Files

- `store_mask_demo.go` - Comprehensive test with multiple scenarios
- `minimal_store_demo.go` - Minimal reproduction case

## Bug Description

When code inside `go for g := range N` where N < SIMD width (e.g., 4), inactive lanes (g >= N) still execute store operations, causing garbage to be written to output arrays.

**Expected behavior**: Only active lanes (0 to N-1) should write values.
**Bug behavior**: Inactive lanes (N to SIMD_WIDTH-1) write garbage.

## Current Status

⚠️ **COMPILATION ISSUE**: The tests currently fail to compile due to type system issues:
```
./minimal_store_demo.go:17:6: non-integer array index g
./store_mask_demo.go:34:30: cannot convert uint(4) (type uint) to type lanes.Varying[int]
```

These appear to be separate bugs in the SPMD type system that need to be resolved first.

## Expected Test Results

Once the type system issues are fixed:

**Scalar Mode** (`-simd=false`): Tests should PASS (no inactive lanes exist)
**SIMD Mode** (`-simd=true`): Tests should FAIL initially, then PASS after fix

## Test Scenarios

1. **Basic masking**: 2 iterations vs 4-wide SIMD
2. **Single iteration**: 1 iteration vs 4-wide SIMD  
3. **Memory corruption**: Detection of writes beyond intended boundaries
4. **Zero iterations**: Edge case where no lanes should be active

## Build Commands (when working)

```bash
# Scalar mode (should pass)
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=false -o test_scalar.wasm store_mask_demo.go

# SIMD mode (should fail until bug is fixed)  
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=true -o test_simd.wasm store_mask_demo.go
```

## Bug Location

The fix should be in the Go compiler SSA generation at:
- `go/src/cmd/compile/internal/ssagen` - SPMD loop lowering
- Focus on ensuring stores inside `go for` loops are properly masked