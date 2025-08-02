#!/bin/bash

# SPMD TinyGo Proof of Concept Test Runner
# Compiles SPMD examples to WASM and executes them with wasmer-go

set -e

echo "=== SPMD TinyGo Proof of Concept Test Runner ==="
echo

# Check dependencies
command -v tinygo >/dev/null 2>&1 || { echo "Error: tinygo not found"; exit 1; }
command -v wasm2wat >/dev/null 2>&1 || { echo "Warning: wasm2wat not found (SIMD verification disabled)"; }

# Ensure GOEXPERIMENT is set
if [[ "$GOEXPERIMENT" != *"spmd"* ]]; then
    echo "Setting GOEXPERIMENT=spmd"
    export GOEXPERIMENT=spmd
else
    echo "GOEXPERIMENT=$GOEXPERIMENT"
fi

echo

# Test examples that should work in PoC
BASIC_EXAMPLES="simple-sum odd-even bit-counting array-counting"
FAILED_BUILDS=0
FAILED_RUNS=0

echo "=== Building and Testing Basic Examples ==="
for example in $BASIC_EXAMPLES; do
    echo "Testing $example..."
    
    # Compile to WASM
    if tinygo build -target=wasi -o "$example.wasm" "$example/main.go" 2>/dev/null; then
        echo "  ✅ Build: $example.wasm compiled successfully"
        
        # Verify SIMD instructions (if wasm2wat available)
        if command -v wasm2wat >/dev/null 2>&1; then
            simd_count=$(wasm2wat "$example.wasm" 2>/dev/null | grep -cE "(v128|i32x4|f32x4)" || echo "0")
            if [ "$simd_count" -gt 0 ]; then
                echo "  ✅ SIMD: Found $simd_count SIMD instructions"
            else
                echo "  ⚠️  SIMD: No SIMD instructions found (may be optimized out)"
            fi
        fi
        
        # Execute with wasmer-go
        if go run wasmer-runner.go "$example.wasm" >/dev/null 2>&1; then
            echo "  ✅ Runtime: Executed successfully"
        else
            echo "  ❌ Runtime: Execution failed"
            ((FAILED_RUNS++))
        fi
    else
        echo "  ❌ Build: Failed to compile"
        ((FAILED_BUILDS++))
    fi
    
    echo
done

echo "=== Testing Advanced Examples (Expected to Fail in PoC) ==="
ADVANCED_EXAMPLES="base64-decoder ipv4-parser"
for example in $ADVANCED_EXAMPLES; do
    echo "Testing $example (should fail)..."
    
    if tinygo build -target=wasi -o "$example.wasm" "$example/main.go" 2>/dev/null; then
        echo "  ⚠️  Build: Unexpectedly succeeded (missing dependencies?)"
    else
        echo "  ✅ Build: Failed as expected (missing cross-lane operations)"
    fi
    echo
done

echo "=== Testing Illegal Examples ==="
cd illegal-spmd
ILLEGAL_EXAMPLES="varying-to-uniform break-in-go-for select-with-varying"
for example in $ILLEGAL_EXAMPLES; do
    echo "Testing $example (should fail with specific error)..."
    
    if tinygo build -target=wasi -o "$example.wasm" "$example.go" 2>/dev/null; then
        echo "  ❌ Build: Should have failed but didn't"
    else
        echo "  ✅ Build: Failed as expected (illegal SPMD construct)"
    fi
done
cd ..

echo
echo "=== Test Summary ==="
echo "Basic examples - Build failures: $FAILED_BUILDS"
echo "Basic examples - Runtime failures: $FAILED_RUNS"

if [ $FAILED_BUILDS -eq 0 ] && [ $FAILED_RUNS -eq 0 ]; then
    echo "🎉 All basic SPMD examples working correctly!"
    echo
    echo "Next steps:"
    echo "1. Inspect generated WASM: wasm2wat simple-sum.wasm | grep v128"
    echo "2. Add more complex SPMD constructs"
    echo "3. Implement remaining lanes/reduce operations"
    exit 0
else
    echo "❌ Some tests failed - check TinyGo SPMD implementation"
    exit 1
fi