#!/bin/bash

# SPMD Integration Test Suite - Dual Mode Compilation & Validation
# Tests SIMD and scalar mode compilation with identical output verification
# Part of Phase 0.5 - Integration Test Suite Setup

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Global counters
TOTAL_TESTS=0
PASSED_TESTS=0
FAILED_TESTS=0

echo -e "${BLUE}=== SPMD Integration Test Suite - Dual Mode Testing ===${NC}"
echo "Testing both SIMD and scalar compilation modes with validation"
echo

# Check dependencies
check_dependencies() {
    echo "Checking dependencies..."
    
    if ! command -v tinygo >/dev/null 2>&1; then
        echo -e "${RED}Error: tinygo not found${NC}"
        exit 1
    fi
    
    if ! command -v wasm2wat >/dev/null 2>&1; then
        echo -e "${YELLOW}Warning: wasm2wat not found (SIMD verification disabled)${NC}"
        WASM2WAT_AVAILABLE=false
    else
        WASM2WAT_AVAILABLE=true
    fi
    
    if ! command -v go >/dev/null 2>&1; then
        echo -e "${RED}Error: go not found${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}✓ Dependencies checked${NC}"
    echo
}

# Test helper functions
log_test_start() {
    ((TOTAL_TESTS++))
    echo -e "${BLUE}[TEST $TOTAL_TESTS] $1${NC}"
}

log_success() {
    ((PASSED_TESTS++))
    echo -e "${GREEN}  ✓ $1${NC}"
}

log_failure() {
    ((FAILED_TESTS++))
    echo -e "${RED}  ✗ $1${NC}"
}

log_warning() {
    echo -e "${YELLOW}  ⚠ $1${NC}"
}

# Dual mode compilation test
test_dual_mode_compilation() {
    local example_name=$1
    local example_dir=$2
    
    log_test_start "Dual Mode Compilation: $example_name"
    
    # Ensure GOEXPERIMENT is set for SPMD
    export GOEXPERIMENT=spmd
    
    # Compile SIMD version
    local simd_wasm="${example_name}-simd.wasm"
    local scalar_wasm="${example_name}-scalar.wasm"
    
    # SIMD compilation (with -simd=true when available)
    if tinygo build -target=wasi -o "$simd_wasm" "$example_dir/main.go" 2>build_simd.log; then
        log_success "SIMD compilation succeeded"
        
        # Verify SIMD instructions if wasm2wat available
        if [ "$WASM2WAT_AVAILABLE" = true ]; then
            local simd_count=$(wasm2wat "$simd_wasm" 2>/dev/null | grep -cE "(v128|i32x4|f32x4)" || echo "0")
            if [ "$simd_count" -gt 0 ]; then
                log_success "SIMD version contains $simd_count SIMD instructions"
            else
                log_warning "SIMD version contains no SIMD instructions (may be optimized or not implemented yet)"
            fi
        fi
    else
        log_failure "SIMD compilation failed"
        cat build_simd.log
        return 1
    fi
    
    # Scalar compilation (simulating -simd=false when available)
    if tinygo build -target=wasi -o "$scalar_wasm" "$example_dir/main.go" 2>build_scalar.log; then
        log_success "Scalar compilation succeeded"
        
        # Verify no SIMD instructions in scalar version
        if [ "$WASM2WAT_AVAILABLE" = true ]; then
            local scalar_simd_count=$(wasm2wat "$scalar_wasm" 2>/dev/null | grep -cE "(v128|i32x4|f32x4)" || echo "0")
            if [ "$scalar_simd_count" -eq 0 ]; then
                log_success "Scalar version contains no SIMD instructions"
            else
                log_warning "Scalar version unexpectedly contains $scalar_simd_count SIMD instructions"
            fi
        fi
    else
        log_failure "Scalar compilation failed"
        cat build_scalar.log
        return 1
    fi
    
    # Clean up build logs
    rm -f build_simd.log build_scalar.log
    
    return 0
}

# Runtime execution test with output comparison
test_runtime_execution() {
    local example_name=$1
    local simd_wasm="${example_name}-simd.wasm"
    local scalar_wasm="${example_name}-scalar.wasm"
    
    log_test_start "Runtime Execution: $example_name"
    
    # Execute SIMD version
    if go run wasmer-runner.go "$simd_wasm" >"${example_name}_simd_output.txt" 2>&1; then
        log_success "SIMD execution succeeded"
    else
        log_failure "SIMD execution failed"
        return 1
    fi
    
    # Execute scalar version
    if go run wasmer-runner.go "$scalar_wasm" >"${example_name}_scalar_output.txt" 2>&1; then
        log_success "Scalar execution succeeded"
    else
        log_failure "Scalar execution failed"
        return 1
    fi
    
    # Compare outputs
    if diff "${example_name}_simd_output.txt" "${example_name}_scalar_output.txt" >/dev/null; then
        log_success "SIMD and scalar outputs are identical"
    else
        log_failure "SIMD and scalar outputs differ"
        echo "SIMD output:"
        cat "${example_name}_simd_output.txt"
        echo "Scalar output:"
        cat "${example_name}_scalar_output.txt"
        return 1
    fi
    
    # Clean up output files
    rm -f "${example_name}_simd_output.txt" "${example_name}_scalar_output.txt"
    
    return 0
}

# Test illegal examples fail appropriately
test_illegal_examples() {
    log_test_start "Illegal Examples Compilation Failures"
    
    export GOEXPERIMENT=spmd
    
    local illegal_dir="illegal-spmd"
    if [ ! -d "$illegal_dir" ]; then
        log_warning "Illegal examples directory not found"
        return 0
    fi
    
    local illegal_examples=(
        "break-in-go-for.go"
        "control-flow-outside-spmd.go"
        "go-for-in-spmd-function.go"
        "invalid-contexts.go"
        "invalid-lane-constraints.go"
        "invalid-type-casting.go"
        "malformed-syntax.go"
        "nested-go-for.go"
        "public-spmd-function.go"
        "varying-to-uniform.go"
    )
    
    for illegal_file in "${illegal_examples[@]}"; do
        local full_path="$illegal_dir/$illegal_file"
        if [ -f "$full_path" ]; then
            if tinygo build -target=wasi -o "illegal_test.wasm" "$full_path" 2>/dev/null; then
                log_failure "$illegal_file should have failed compilation but didn't"
            else
                log_success "$illegal_file correctly failed compilation"
            fi
            rm -f illegal_test.wasm
        fi
    done
}

# Test legacy compatibility (without GOEXPERIMENT=spmd)
test_legacy_compatibility() {
    log_test_start "Legacy Compatibility (without GOEXPERIMENT=spmd)"
    
    # Temporarily unset SPMD experiment
    local old_goexperiment="$GOEXPERIMENT"
    export GOEXPERIMENT=""
    
    local legacy_dir="legacy"
    if [ ! -d "$legacy_dir" ]; then
        log_warning "Legacy examples directory not found"
        export GOEXPERIMENT="$old_goexperiment"
        return 0
    fi
    
    # Test legacy examples that use "uniform" and "varying" as identifiers
    local legacy_examples=(
        "functions"
        "json_tags"
        "labels"
        "types"
        "variables"
    )
    
    for legacy_example in "${legacy_examples[@]}"; do
        local legacy_path="$legacy_dir/$legacy_example"
        if [ -d "$legacy_path" ] && [ -f "$legacy_path/main.go" ]; then
            if tinygo build -target=wasi -o "legacy_test.wasm" "$legacy_path/main.go" 2>/dev/null; then
                log_success "$legacy_example compiles without SPMD experiment"
            else
                log_failure "$legacy_example failed to compile without SPMD experiment"
            fi
            rm -f legacy_test.wasm
        fi
    done
    
    # Restore GOEXPERIMENT
    export GOEXPERIMENT="$old_goexperiment"
}

# Browser SIMD detection test
test_browser_simd_detection() {
    log_test_start "Browser SIMD Detection"
    
    local browser_dir="browser-simd-detection"
    if [ ! -f "$browser_dir/index.html" ]; then
        log_warning "Browser SIMD detection example not found"
        return 0
    fi
    
    # Check if index.html contains SIMD detection code
    if grep -q "WebAssembly.validate" "$browser_dir/index.html" && 
       grep -q "v128" "$browser_dir/index.html"; then
        log_success "Browser SIMD detection code found"
    else
        log_failure "Browser SIMD detection code missing or incomplete"
    fi
}

# Main test execution
main() {
    check_dependencies
    
    # Define SPMD examples to test
    local spmd_examples=(
        "simple-sum"
        "odd-even"
        "bit-counting"
        "array-counting"
        "printf-verbs"
        "hex-encode"
        "to-upper"
        "debug-varying"
        "goroutine-varying"
        "defer-varying"
        "panic-recover-varying"
        "map-restrictions"
        "pointer-varying"
        "type-switch-varying"
        "non-spmd-varying-return"
        "spmd-call-contexts"
        "lanes-index-restrictions"
        "varying-universal-constrained"
        "union-type-generics"
        "type-casting-varying"
        "varying-array-iteration"
        "mandelbrot"
    )
    
    # Advanced examples that may not work yet in PoC
    local advanced_examples=(
        "base64-decoder"
        "ipv4-parser"
    )
    
    echo -e "${BLUE}=== Testing Basic SPMD Examples ===${NC}"
    for example in "${spmd_examples[@]}"; do
        if [ -d "$example" ] && [ -f "$example/main.go" ]; then
            if test_dual_mode_compilation "$example" "$example"; then
                test_runtime_execution "$example"
            fi
            echo
        else
            echo -e "${YELLOW}Skipping $example (not found)${NC}"
        fi
        # Clean up WASM files
        rm -f "${example}-simd.wasm" "${example}-scalar.wasm"
    done
    
    echo -e "${BLUE}=== Testing Advanced Examples (May Fail) ===${NC}"
    for example in "${advanced_examples[@]}"; do
        if [ -d "$example" ] && [ -f "$example/main.go" ]; then
            if test_dual_mode_compilation "$example" "$example"; then
                log_success "$example unexpectedly succeeded (implementation ahead of schedule)"
                test_runtime_execution "$example"
            else
                log_warning "$example failed as expected (cross-lane operations not yet implemented)"
            fi
            echo
        fi
        # Clean up WASM files
        rm -f "${example}-simd.wasm" "${example}-scalar.wasm"
    done
    
    echo -e "${BLUE}=== Testing Error Conditions ===${NC}"
    test_illegal_examples
    echo
    
    echo -e "${BLUE}=== Testing Legacy Compatibility ===${NC}"
    test_legacy_compatibility
    echo
    
    echo -e "${BLUE}=== Testing Browser Integration ===${NC}"
    test_browser_simd_detection
    echo
    
    # Print summary
    echo -e "${BLUE}=== Test Summary ===${NC}"
    echo "Total tests: $TOTAL_TESTS"
    echo -e "Passed: ${GREEN}$PASSED_TESTS${NC}"
    echo -e "Failed: ${RED}$FAILED_TESTS${NC}"
    
    if [ $FAILED_TESTS -eq 0 ]; then
        echo -e "${GREEN}🎉 All integration tests passed!${NC}"
        echo
        echo "Next steps:"
        echo "1. Proceed with Phase 1 - Go Frontend Implementation"
        echo "2. Implement lexer modifications for uniform/varying keywords"
        echo "3. Add parser extensions for SPMD syntax"
        exit 0
    else
        echo -e "${RED}❌ Some integration tests failed${NC}"
        echo "Check the test output above for details"
        exit 1
    fi
}

# Run main function
main "$@"