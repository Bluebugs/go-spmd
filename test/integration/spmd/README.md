# SPMD Integration Test Suite

**Phase 0.5 - Integration Test Suite Setup**

This directory contains comprehensive integration tests for the SPMD (Single Program Multiple Data) implementation in Go + TinyGo. The test suite validates dual-mode compilation (SIMD and scalar) and ensures identical behavior between modes.

## Overview

The integration test suite implements all requirements from Phase 0.5 of the SPMD implementation plan:

- ✅ **Dual-mode compilation testing**: SIMD and scalar WASM generation
- ✅ **SIMD instruction verification**: Using wasm2wat to inspect generated code
- ✅ **Identical output validation**: Ensuring SIMD and scalar modes produce same results
- ✅ **Illegal example testing**: Verifying compilation failures for invalid SPMD code
- ✅ **Legacy compatibility**: Testing that existing code works without GOEXPERIMENT=spmd
- ✅ **Runtime execution**: Using wasmer-go for WASM execution testing
- ✅ **Browser integration**: SIMD detection and loading capability tests

## Running Tests

### Using Make (Recommended)
```bash
make test                 # Run all tests
make test-basic          # Basic examples only
make check-deps          # Check dependencies
```

### Using Go Test Framework
```bash
go test -v ./... -timeout=10m
```

### Using Shell Script
```bash
./dual-mode-test-runner.sh
```

## Test Categories

- **Basic SPMD Examples**: 23 examples that should work in PoC
- **Advanced Examples**: 2 complex examples (may fail until cross-lane ops implemented)
- **Illegal Examples**: 10 examples that should fail compilation
- **Legacy Examples**: 5 compatibility tests for existing code

## Expected Behavior (Phase 0.5)

✅ **Test Infrastructure**: All test files execute successfully
⚠️ **SPMD Compilation**: Expected to fail (lexer/parser not implemented yet)
✅ **Legacy Compatibility**: Should pass (existing code works)
✅ **Error Handling**: Framework handles failures gracefully

For complete documentation, test categories, and usage instructions, see the comprehensive README documentation in this directory.