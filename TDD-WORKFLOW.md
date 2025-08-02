# SPMD Test-Driven Development Workflow

**Phase 0.6 - TDD Workflow Documentation**

This document defines the test-first development cycle procedures for SPMD implementation, ensuring systematic validation at each milestone.

## Overview

The SPMD implementation follows a strict test-driven development approach where **tests are written first** and **implementation follows** to make tests pass. This ensures:

- **Systematic validation** at each implementation milestone
- **Regression prevention** through comprehensive test coverage
- **Clear success criteria** for each phase
- **Continuous integration** readiness

## TDD Workflow Phases

### Phase Structure

Each implementation phase follows this TDD pattern:

```
Phase X.Y: Feature Implementation
├── X.Y.1: Write Tests First (Test Infrastructure)
├── X.Y.2: Run Tests (Expected Failures)
├── X.Y.3: Implement Feature (Make Tests Pass)
├── X.Y.4: Validate Tests Pass
├── X.Y.5: Refactor and Optimize
└── X.Y.6: Document and Commit
```

### Test-First Development Cycle

1. **Write Tests First**: Create comprehensive tests for the feature before implementation
2. **Verify Test Failures**: Ensure tests fail appropriately (red phase)
3. **Implement Minimum Code**: Write just enough code to make tests pass (green phase)
4. **Refactor**: Clean up code while keeping tests passing (refactor phase)
5. **Validate**: Run full test suite to ensure no regressions
6. **Document**: Update documentation and commit changes

## Test Categories and Automation

### Test Category Hierarchy

```
SPMD Test Suite
├── Unit Tests (Phase 0.1-0.6 Infrastructure)
│   ├── Parser Tests (Phase 1.2-1.3)
│   ├── Type Checker Tests (Phase 1.4-1.5)
│   └── SSA Generation Tests (Phase 1.7)
├── Integration Tests (Phase 0.5)
│   ├── Dual-mode Compilation
│   ├── SIMD Instruction Verification
│   └── Runtime Execution Validation
└── End-to-End Tests (Phase 3)
    ├── Example Validation
    ├── Performance Benchmarks
    └── Browser Integration
```

## Automated Test Runner Commands

### Phase-Specific Test Commands

#### Phase 0: Foundation Testing
```bash
# Run all Phase 0 infrastructure tests
make test-phase0

# Individual phase testing
make test-phase01  # GOEXPERIMENT integration
make test-phase02  # Parser test infrastructure
make test-phase03  # Type checker test infrastructure
make test-phase04  # SSA generation test infrastructure
make test-phase05  # Integration test infrastructure
make test-phase06  # TDD workflow validation
```

#### Phase 1: Frontend Testing
```bash
# Run all Phase 1 frontend tests
make test-phase1

# Individual feature testing
make test-lexer          # Phase 1.2 lexer modifications
make test-parser         # Phase 1.3 parser extensions
make test-typechecker    # Phase 1.4-1.5 type system
make test-ssa            # Phase 1.7 SSA generation
make test-stdlib         # Phase 1.8-1.9 standard library
```

#### Phase 2: Backend Testing
```bash
# Run all Phase 2 backend tests
make test-phase2

# TinyGo backend testing
make test-tinygo-integration    # Phase 2.1 SSA-to-LLVM
make test-dual-mode            # Phase 2.2 dual code generation
make test-simd-generation      # Phase 2.3 SIMD mode
make test-scalar-fallback      # Phase 2.4 scalar mode
make test-wasm-simd           # Phase 2.5 WebAssembly SIMD128
```

#### Phase 3: Validation Testing
```bash
# Run all Phase 3 validation tests
make test-phase3

# Comprehensive validation
make test-examples        # All example validation
make test-performance     # Performance benchmarks
make test-browser         # Browser integration
make test-compatibility   # Legacy compatibility
```

### Continuous Integration Commands

#### Quick Validation (< 5 minutes)
```bash
# Fast validation for development
make ci-quick

# Equivalent to:
make test-phase0-quick
make test-basic-examples
make test-illegal-examples
```

#### Full Validation (< 30 minutes)
```bash
# Complete test suite
make ci-full

# Equivalent to:
make test-phase0
make test-phase1
make test-phase2
make test-phase3
make test-performance
```

#### Regression Testing
```bash
# Regression test suite
make test-regression

# Tests all previously working functionality
make test-legacy-compatibility
make test-backward-compatibility
make test-no-regressions
```

## Test Success Criteria

### Phase 0: Foundation Success Criteria

**Phase 0.1: GOEXPERIMENT Integration**
- ✅ `GOEXPERIMENT=spmd` enables SPMD features
- ✅ Build constraint files generate correctly
- ✅ Graceful degradation when experiment disabled
- ✅ Go toolchain builds successfully with SPMD support

**Phase 0.2: Parser Test Infrastructure**
- ✅ Parser test framework detects SPMD syntax
- ✅ Tests fail appropriately for unimplemented syntax
- ✅ Backward compatibility tests pass

**Phase 0.3: Type Checker Test Infrastructure**
- ✅ Type checker test framework validates SPMD rules
- ✅ Error detection patterns work correctly
- ✅ GOEXPERIMENT gating functions properly

**Phase 0.4: SSA Generation Test Infrastructure**
- ✅ SSA test framework parses EXPECT SSA comments
- ✅ Opcode detection and validation ready
- ✅ 151 expected SSA opcodes catalogued

**Phase 0.5: Integration Test Infrastructure**
- ✅ Dual-mode compilation testing ready
- ✅ SIMD instruction verification framework
- ✅ Runtime execution validation with wasmer-go
- ✅ Browser integration testing

**Phase 0.6: TDD Workflow Documentation**
- ✅ Test-first procedures documented
- ✅ Automated test commands defined
- ✅ Success criteria established
- ✅ CI/CD procedures ready

### Phase 1: Frontend Success Criteria

**Phase 1.2: Lexer Modifications**
- 🎯 `uniform` and `varying` keywords recognized in type contexts
- 🎯 Keywords work as identifiers in non-type contexts
- 🎯 `go for` construct lexing implemented
- 🎯 All parser tests pass

**Phase 1.3: Parser Extensions**
- 🎯 Type qualifiers parse correctly (`uniform int`, `varying float32`)
- 🎯 `go for` SPMD loops parse successfully
- 🎯 Constrained varying syntax works (`varying[4] byte`)
- 🎯 All syntax tests pass

**Phase 1.4-1.5: Type System Implementation**
- 🎯 SPMD types integrate with Go type system
- 🎯 Assignment rules enforced correctly
- 🎯 SPMD function detection works
- 🎯 All type checker tests pass

**Phase 1.7: SSA Generation**
- 🎯 `go for` loops generate correct SSA opcodes
- 🎯 Vector operations produce OpVectorAdd, OpVectorMul, etc.
- 🎯 Mask propagation uses OpAnd, OpOr, OpNot
- 🎯 All 151 expected SSA opcodes generated correctly

**Phase 1.8-1.9: Standard Library Implementation 🔴 CRITICAL PoC DEPENDENCY**
- 🎯 `lanes` package fully implemented with all required functions
- 🎯 `reduce` package fully implemented with all required functions
- 🎯 All integration tests can successfully compile SPMD examples
- 🎯 Dual-mode compilation validation becomes possible

**⚠️ CRITICAL MILESTONE**: Phase 1.8-1.9 completion enables PoC validation. All integration tests, examples, and dual-mode compilation depend on standard library availability.

### Phase 2: Backend Success Criteria

**Phase 2.1-2.2: TinyGo Integration**
- 🎯 TinyGo recognizes SPMD SSA constructs
- 🎯 Dual-mode compilation (SIMD/scalar) works
- 🎯 Vector types map to LLVM correctly

**Phase 2.3: SIMD Mode Implementation**
- 🎯 SIMD WASM contains v128.* instructions
- 🎯 Vector arithmetic generates proper SIMD ops
- 🎯 Integration tests pass in SIMD mode

**Phase 2.4: Scalar Mode Implementation**
- 🎯 Scalar WASM contains no SIMD instructions
- 🎯 Identical behavior to SIMD mode
- 🎯 Integration tests pass in scalar mode

### Phase 3: Validation Success Criteria

**Phase 3.1: Example Validation**
- 🎯 ALL 22+ basic examples compile and run correctly
- 🎯 Advanced examples work (base64-decoder, ipv4-parser)
- 🎯 Identical output between SIMD and scalar modes

**Phase 3.2: Illegal Example Validation**
- 🎯 All 10 illegal examples fail compilation appropriately
- 🎯 Clear error messages for each violation
- 🎯 Compilation error handling robust

**Phase 3.3: Legacy Compatibility**
- 🎯 All legacy examples work without GOEXPERIMENT=spmd
- 🎯 No breaking changes to existing Go programs
- 🎯 Graceful degradation when experiment disabled

## Regression Testing Procedures

### Automated Regression Detection

1. **Test Matrix Execution**
   ```bash
   # Run all test combinations
   make test-matrix
   
   # Test with/without GOEXPERIMENT
   GOEXPERIMENT="" make test-legacy
   GOEXPERIMENT=spmd make test-spmd
   ```

2. **Git Bisect Integration**
   ```bash
   # Automated regression finding
   git bisect start
   git bisect bad HEAD
   git bisect good v0.5.0
   git bisect run make ci-quick
   ```

3. **Performance Regression Detection**
   ```bash
   # Benchmark comparison
   make benchmark-baseline
   make benchmark-current
   make benchmark-compare
   ```

### Regression Prevention

1. **Pre-commit Hooks**
   - Run quick test suite before each commit
   - Validate no test failures introduced
   - Check for compilation regressions

2. **Branch Protection**
   - Require all tests pass before merge
   - Mandate code review for test changes
   - Automated CI validation

3. **Release Validation**
   - Full test suite execution
   - Performance benchmark validation
   - Cross-platform compatibility testing

## Continuous Integration Procedures

### CI Pipeline Stages

#### Stage 1: Quick Validation (2-3 minutes)
```yaml
quick_validation:
  script:
    - make check-deps
    - make test-phase0-quick
    - make test-basic-syntax
    - make test-illegal-examples
```

#### Stage 2: Frontend Testing (5-10 minutes)
```yaml
frontend_testing:
  script:
    - make test-lexer
    - make test-parser
    - make test-typechecker
    - make test-ssa-generation
```

#### Stage 3: Backend Testing (10-15 minutes)
```yaml
backend_testing:
  script:
    - make test-tinygo-integration
    - make test-dual-mode-compilation
    - make test-simd-instruction-generation
    - make test-runtime-execution
```

#### Stage 4: Full Validation (15-30 minutes)
```yaml
full_validation:
  script:
    - make test-all-examples
    - make test-performance-benchmarks
    - make test-browser-integration
    - make test-legacy-compatibility
```

### CI Environment Requirements

#### Dependencies
- **Go 1.21+** with SPMD experiment support
- **TinyGo** with SPMD backend implementation
- **wasm2wat** for SIMD instruction verification
- **wasmer-go** dependencies for runtime testing

#### Environment Variables
```bash
export GOEXPERIMENT=spmd
export TINYGO_CACHE_DIR=/tmp/tinygo-cache
export WASM_RUNTIME=wasmer
```

#### Artifact Collection
- **Test Results**: JUnit XML format for CI integration
- **Coverage Reports**: Go coverage profiles
- **Performance Data**: Benchmark results for regression detection
- **WASM Binaries**: Generated binaries for inspection

## Development Workflow Integration

### Daily Development Cycle

1. **Morning Setup**
   ```bash
   git pull origin main
   make check-deps
   make test-quick-validation
   ```

2. **Feature Development**
   ```bash
   # Write tests first
   make test-feature-X  # Should fail initially
   
   # Implement feature
   # ... code changes ...
   
   # Validate implementation
   make test-feature-X  # Should pass now
   make test-regression # Ensure no breaks
   ```

3. **Pre-commit Validation**
   ```bash
   make test-affected-areas
   make lint
   make format
   git commit -m "Implement feature X"
   ```

### Weekly Integration

1. **Full Test Suite Execution**
   ```bash
   make ci-full
   make test-performance
   make test-all-browsers
   ```

2. **Performance Baseline Update**
   ```bash
   make benchmark-update-baseline
   make performance-report
   ```

3. **Documentation Sync**
   ```bash
   make update-test-docs
   make validate-examples
   ```

## Critical Dependencies and Adaptations

### Phase 1.8-1.9 Standard Library Dependency

**🔴 CRITICAL**: The PoC validation strategy requires adaptation once Phase 1.8-1.9 is implemented.

#### Current State (Phase 0 Complete)
- ✅ Test infrastructure ready and validated
- ✅ Integration tests copied but **cannot compile** yet
- ✅ Examples reference `lanes` and `reduce` packages that don't exist
- ✅ Dual-mode testing framework ready but **cannot execute** yet

#### Required Adaptations After Phase 1.8-1.9
1. **Update Integration Test Module**:
   ```bash
   # Update go.mod to reference actual standard library paths
   cd test/integration/spmd
   # Fix import paths in all examples
   # Enable actual compilation testing
   ```

2. **Enable Real Dual-Mode Testing**:
   ```bash
   # These commands become functional after Phase 1.8-1.9
   make test-examples        # Now compiles successfully
   make test-dual-mode       # Now generates real SIMD vs scalar WASM
   make test-performance     # Now measures actual performance difference
   ```

3. **Activate Full PoC Validation**:
   ```bash
   # Complete PoC validation becomes possible
   make test-all-examples    # All 22+ examples compile and run
   make verify-simd-instructions  # Real SIMD instruction verification
   make test-browser-integration  # Actual dual-mode browser testing
   ```

#### Timeline Impact
- **Phase 1.1-1.7**: Test infrastructure validates syntax and SSA generation
- **Phase 1.8-1.9**: **CRITICAL MILESTONE** - Enables full PoC validation
- **Phase 2**: Backend implementation with functional test validation
- **Phase 3**: Complete PoC success criteria validation

## Test Development Guidelines

### Writing New Tests

1. **Test Naming Convention**
   ```go
   func TestSPMD<Feature><Scenario>(t *testing.T)
   func TestSPMD<Feature>Error<ErrorType>(t *testing.T)
   func TestSPMD<Feature>Compatibility(t *testing.T)
   ```

2. **Test Structure**
   ```go
   func TestSPMDFeature(t *testing.T) {
       // Setup
       setupTest(t)
       
       // Test data
       testCases := []struct{
           name string
           input string
           expect string
       }{
           // Test cases
       }
       
       // Execute tests
       for _, tc := range testCases {
           t.Run(tc.name, func(t *testing.T) {
               // Test implementation
           })
       }
   }
   ```

3. **Error Test Pattern**
   ```go
   func TestSPMDErrorHandling(t *testing.T) {
       // Test should fail compilation
       err := compileSPMD(invalidCode)
       if err == nil {
           t.Error("Expected compilation to fail")
       }
       
       // Verify specific error message
       if !strings.Contains(err.Error(), expectedError) {
           t.Errorf("Expected error containing %q, got %q", 
                   expectedError, err.Error())
       }
   }
   ```

### Test Maintenance

1. **Regular Test Review**
   - Weekly review of test coverage
   - Monthly review of test performance
   - Quarterly review of test architecture

2. **Test Cleanup**
   - Remove obsolete tests
   - Refactor duplicated test logic
   - Update test documentation

3. **Test Performance**
   - Monitor test execution times
   - Parallelize slow tests
   - Optimize test data generation

## Success Metrics

### Coverage Targets

- **Line Coverage**: >90% for core SPMD functionality
- **Branch Coverage**: >85% for control flow paths
- **Function Coverage**: 100% for public APIs

### Performance Targets

- **Test Execution Time**: <30 minutes for full suite
- **Quick Validation**: <5 minutes for development cycle
- **Regression Detection**: <1 hour for full validation

### Quality Metrics

- **Zero Known Regressions**: All previously working functionality maintained
- **Clear Error Messages**: All compilation failures provide actionable feedback
- **Documentation Coverage**: All public APIs documented with examples

## Phase 0 Completion Checklist

- ✅ **Phase 0.1**: GOEXPERIMENT integration complete
- ✅ **Phase 0.2**: Parser test infrastructure ready
- ✅ **Phase 0.3**: Type checker test infrastructure ready
- ✅ **Phase 0.4**: SSA generation test infrastructure ready
- ✅ **Phase 0.5**: Integration test infrastructure complete
- ✅ **Phase 0.6**: TDD workflow documentation complete

**Phase 0 Status**: ✅ **COMPLETE** - Ready for Phase 1 Implementation

---

This TDD workflow ensures systematic, test-driven implementation of SPMD functionality with comprehensive validation at each milestone.