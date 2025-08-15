# GOEXPERIMENT Implementation Plan: SPMD Extensions for Go + TinyGo

This document outlines the implementation plan for adding Single Program Multiple Data (SPMD) support to Go, protected behind the `GOEXPERIMENT=spmd` flag. The implementation uses a two-stage approach:

1. **Go Frontend**: Lexing, parsing, type checking, and SSA generation with SPMD support
2. **TinyGo Backend**: Converting Go SSA to LLVM IR and generating WebAssembly SIMD128 instructions

This separation allows leveraging Go's robust frontend while utilizing TinyGo's LLVM backend for SIMD code generation.

## Implementation Architecture

The SPMD implementation uses a **runtime experiment flag** approach with a single Go compiler binary:

### Go Frontend (Phase 1: `src/cmd/compile/`)

1. **Runtime Feature Flag**: `GOEXPERIMENT=spmd` enables SPMD syntax and semantics at compilation time
2. **Runtime Experiment Checks**: All SPMD functionality gated behind `buildcfg.Experiment.SPMD` runtime checks  
3. **Lexer/Parser**: Add `uniform`, `varying`, and `go for` syntax with runtime experiment gating
4. **Type System**: SPMD type checking and inference with runtime enabling/disabling
5. **SSA Generation**: Convert SPMD constructs to Go SSA IR with vector operations when experiment enabled

### TinyGo Backend (Phase 2: TinyGo consumes Go SSA)

1. **SSA Consumption**: TinyGo reads Go SSA with SPMD opcodes
2. **LLVM IR Generation**: Convert SPMD SSA to LLVM vector instructions
3. **WASM Target**: Generate WebAssembly SIMD128 instructions
4. **Runtime**: Execute with wasmer-go SIMD support

### ISPC/LLVM Reference Patterns

The SSA-to-LLVM conversion follows established patterns from:

- **ISPC**: `ispc/src/llvmutil.cpp` - Vector type mapping and mask operations
- **LLVM**: Vector intrinsics and SIMD instruction selection
- **WASM SIMD**: `llvm/lib/Target/WebAssembly/` - SIMD128 code generation

## Implementation Steps

### Phase 0: Test-Driven Development Setup

Before implementing any SPMD functionality, we leverage the extensive examples we've created to establish a comprehensive test suite. This ensures implementation is guided by concrete requirements and prevents regressions.

#### 0.1. Prepare Parser Test Suite

**Goal**: Test that SPMD syntax is correctly recognized (or rejected when experiment is disabled)

**Test Files**: Convert existing examples into parser tests

```bash
# Create parser test infrastructure
mkdir -p src/cmd/compile/internal/syntax/testdata/spmd

# Copy examples as parser test cases
cp examples/simple-sum/main.go src/cmd/compile/internal/syntax/testdata/spmd/simple_sum.go
cp examples/odd-even/main.go src/cmd/compile/internal/syntax/testdata/spmd/odd_even.go
cp examples/illegal-spmd/*.go src/cmd/compile/internal/syntax/testdata/spmd/
```

**Parser Test Structure**:

```go
// src/cmd/compile/internal/syntax/spmd_test.go
func TestSPMDParser(t *testing.T) {
    testCases := []struct {
        name     string
        file     string
        enabled  bool  // GOEXPERIMENT=spmd enabled
        wantFail bool  // expect parsing to fail
    }{
        {"simple-sum", "simple_sum.go", true, false},
        {"odd-even", "odd_even.go", true, false},
        {"varying-to-uniform", "varying-to-uniform.go", true, true}, // should fail
        {"break-in-go-for", "break-in-go-for.go", true, true},      // should fail
        {"nested-go-for", "nested-go-for.go", true, true},        // should fail - nesting not allowed
        {"go-for-in-spmd-func", "go-for-in-spmd-function.go", true, true}, // should fail - SPMD func restriction
        {"goroutine-varying", "goroutine-varying.go", true, false},        // should pass - goroutines now allowed
        {"defer-varying", "defer-varying.go", true, false},                // should pass - defer now allowed
        {"panic-recover-varying", "panic-recover-varying.go", true, false}, // should pass - panic/recover allowed
        {"disabled-uniform", "simple_sum.go", false, true},        // should fail when disabled
    }
    
    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Test with runtime experiment flag 
            // Note: In actual implementation, buildcfg.Experiment.SPMD is set by runtime
            // environment variable GOEXPERIMENT=spmd, not directly in tests
            
            // Parse file and check result
            src := readTestFile(tc.file)
            _, err := ParseFile("test.go", src, nil, 0)
            
            if tc.wantFail && err == nil {
                t.Errorf("expected parsing to fail, but it succeeded")
            }
            if !tc.wantFail && err != nil {
                t.Errorf("expected parsing to succeed, but got error: %v", err)
            }
        })
    }
}
```

#### 0.2. Prepare Type Checker Test Suite

**Goal**: Test that SPMD type rules are correctly enforced

**Test Files**: Use examples to test type checking behavior

```bash
# Create type checker test infrastructure
mkdir -p src/cmd/compile/internal/types2/testdata/spmd

# Copy examples as type checker test cases
cp examples/simple-sum/main.go src/cmd/compile/internal/types2/testdata/spmd/
cp examples/legacy/*/main.go src/cmd/compile/internal/types2/testdata/spmd/legacy/
cp examples/illegal-spmd/*.go src/cmd/compile/internal/types2/testdata/spmd/illegal/
```

**Type Checker Test Structure**:

```go
// src/cmd/compile/internal/types2/spmd_test.go
func TestSPMDTypeChecking(t *testing.T) {
    testCases := []struct {
        name        string
        file        string
        enabled     bool
        wantTypeErr bool
        expectedErr string
    }{
        {"simple-sum-valid", "simple_sum.go", true, false, ""},
        {"legacy-uniform-var", "legacy/uniform-variable.go", false, false, ""}, // backward compatibility
        {"varying-to-uniform", "illegal/varying-to-uniform.go", true, true, "cannot assign varying to uniform"},
        {"break-in-go-for", "illegal/break-in-go-for.go", true, true, "break not allowed in go for loops"},
        {"nested-go-for", "illegal/nested-go-for.go", true, true, "go for loops cannot be nested"},
        {"go-for-in-spmd-func", "illegal/go-for-in-spmd-function.go", true, true, "go for loops not allowed in SPMD functions"},
        {"public-spmd-func", "illegal/public-spmd-function.go", true, true, "varying parameters not allowed in public functions"},
        {"goroutine-varying", "goroutine-varying.go", true, false, ""}, // goroutines now allowed
        {"defer-varying", "defer-varying.go", true, false, ""}, // defer now allowed
        {"panic-recover-varying", "panic-recover-varying.go", true, false, ""}, // panic/recover now allowed
        {"map-restrictions", "map-restrictions.go", true, false, ""}, // map restrictions - valid usage patterns
        {"pointer-varying", "pointer-varying.go", true, false, ""}, // pointer operations with varying types
        {"type-switch-varying", "type-switch-varying.go", true, false, ""}, // type switches with varying interface{}
        {"non-spmd-varying-return", "non-spmd-varying-return.go", true, false, ""}, // non-SPMD functions returning varying
        {"spmd-call-contexts", "spmd-call-contexts.go", true, false, ""}, // SPMD functions callable from any context
        {"lanes-index-restrictions", "lanes-index-restrictions.go", true, false, ""}, // lanes.Index() context requirements
        {"varying-universal-constrained", "varying-universal-constrained.go", true, false, ""}, // varying[] universal constrained syntax
        {"union-type-generics", "union-type-generics.go", true, false, ""}, // union type generics for reduce/lanes functions
        {"disabled-spmd", "simple_sum.go", false, true, "uniform/varying require GOEXPERIMENT=spmd"},
    }
    
    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Test with runtime experiment flag
            // Note: buildcfg.Experiment.SPMD controlled by GOEXPERIMENT=spmd
            
            // Type check file and verify results
            src := readTestFile(tc.file)
            _, err := typeCheckFile("test.go", src)
            
            if tc.wantTypeErr {
                if err == nil {
                    t.Errorf("expected type error, but type checking succeeded")
                } else if tc.expectedErr != "" && !strings.Contains(err.Error(), tc.expectedErr) {
                    t.Errorf("expected error containing %q, got: %v", tc.expectedErr, err)
                }
            } else if err != nil {
                t.Errorf("expected type checking to succeed, but got error: %v", err)
            }
        })
    }
}
```

#### 0.3. Prepare SSA Generation Test Suite

**Goal**: Test that SPMD constructs generate correct SSA opcodes

```go
// src/cmd/compile/internal/ssagen/spmd_test.go
func TestSPMDSSAGeneration(t *testing.T) {
    testCases := []struct {
        name           string
        code           string
        expectedOpcodes []string
    }{
        {
            "simple-spmd-loop",
            `go for i := range 16 { sum += data[i] }`,
            []string{"OpPhi", "OpVectorAdd", "OpSelect"},
        },
        {
            "uniform-to-varying",
            `var x uniform int = 42; var y varying int = x`,
            []string{"OpCall"}, // Call to lanes.Broadcast
        },
        {
            "reduction",
            `total := reduce.Add(values)`,
            []string{"OpCall"}, // Call to reduce.Add with mask parameter
        },
    }
    
    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Test requires GOEXPERIMENT=spmd to be set at runtime
            
            // Generate SSA and verify opcodes
            fn := compileToSSA(tc.code)
            opcodes := extractOpcodes(fn)
            
            for _, expectedOp := range tc.expectedOpcodes {
                if !contains(opcodes, expectedOp) {
                    t.Errorf("expected SSA to contain %s, but got opcodes: %v", expectedOp, opcodes)
                }
            }
        })
    }
}
```

#### 0.4. Prepare Integration Test Suite

**Goal**: End-to-end testing with TinyGo backend and WASM execution

```bash
# Create integration test infrastructure
mkdir -p test/integration/spmd

# Copy all examples as integration tests
cp -r examples/* test/integration/spmd/
```

**Integration Test Structure**:

```bash
#!/bin/bash
# test/integration/spmd/run_tests.sh

set -e

echo "Running SPMD integration tests..."

# Test 1: Verify ALL examples compile to both SIMD and scalar WASM with GOEXPERIMENT=spmd
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions varying-universal-constrained union-type-generics; do
    echo "Testing $example dual compilation..."
    
    # Compile SIMD version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o "$example-simd.wasm" "$example/main.go"
    
    # Compile scalar version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o "$example-scalar.wasm" "$example/main.go"
    
    # Verify SIMD version contains SIMD instructions
    simd_count=$(wasm2wat "$example-simd.wasm" | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$simd_count" -eq 0 ]; then
        echo "ERROR: $example-simd.wasm contains no SIMD instructions"
        exit 1
    fi
    
    # Verify scalar version contains no SIMD instructions
    scalar_simd_count=$(wasm2wat "$example-scalar.wasm" | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$scalar_simd_count" -ne 0 ]; then
        echo "ERROR: $example-scalar.wasm contains SIMD instructions"
        exit 1
    fi
    
    echo "✓ $example: SIMD version ($simd_count instructions), scalar version (0 SIMD instructions)"
done

# Test 2: Verify ALL examples execute correctly in both modes and produce identical output
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions varying-universal-constrained union-type-generics; do
    echo "Testing $example execution in both modes..."
    
    # Execute SIMD version
    simd_output=$(go run ../wasmer-runner.go "$example-simd.wasm")
    
    # Execute scalar version
    scalar_output=$(go run ../wasmer-runner.go "$example-scalar.wasm")
    
    # Verify both succeed
    if [[ $simd_output == *"test passed"* ]] && [[ $scalar_output == *"test passed"* ]]; then
        # Verify outputs are identical
        if [ "$simd_output" = "$scalar_output" ]; then
            echo "✓ $example: both SIMD and scalar modes executed successfully with identical output"
        else
            echo "ERROR: $example outputs differ between SIMD and scalar modes"
            echo "SIMD: $simd_output"
            echo "Scalar: $scalar_output"
            exit 1
        fi
    else
        echo "ERROR: $example execution failed"
        echo "SIMD: $simd_output"
        echo "Scalar: $scalar_output"
        exit 1
    fi
done

# Test 3: Verify illegal examples fail compilation
for illegal in illegal-spmd/*.go; do
    echo "Testing illegal example: $illegal"
    if GOEXPERIMENT=spmd tinygo build -target=wasi "$illegal" 2>/dev/null; then
        echo "ERROR: $illegal should have failed compilation but succeeded"
        exit 1
    fi
    echo "✓ $illegal correctly failed compilation"
done

# Test 4: Verify legacy compatibility
for legacy in legacy/*/main.go; do
    echo "Testing legacy compatibility: $legacy"
    if ! tinygo build -target=wasi "$legacy" 2>/dev/null; then
        echo "ERROR: $legacy should compile without GOEXPERIMENT=spmd but failed"
        exit 1
    fi
    echo "✓ $legacy maintains backward compatibility"
done

echo "All SPMD integration tests passed!"
```

### Phase 1: Go Frontend Changes

**Important**: All implementation steps below should be driven by making the tests from Phase 0 pass.

#### 1. Runtime SPMD Experiment Flag Integration

The SPMD experiment is enabled via runtime environment variable `GOEXPERIMENT=spmd`, not build-time constraints.

**File**: `src/internal/buildcfg/exp.go` (Go compiler runtime experiment handling)

```go
// SPMD experiment is controlled by runtime GOEXPERIMENT environment variable
// buildcfg.Experiment.SPMD is set during compilation process based on environment

func init() {
    // Runtime experiment flag parsing handled by Go's build configuration
    // No separate build constraints needed - single binary supports both modes
}
```

**Runtime Usage**:

```bash
# Enable SPMD for single compilation
GOEXPERIMENT=spmd go build program.go

# Disable SPMD (default behavior) 
go build program.go
```

#### 2. Go Lexer Modifications with Runtime Gating

**File**: `src/cmd/compile/internal/syntax/tokens.go` (main Go compiler)

Add SPMD keyword recognition with runtime experiment checks:

```go
// SPMD tokens only recognized when experiment enabled
const (
    _Uniform token = iota + 100  // Only available when buildcfg.Experiment.SPMD
    _Varying                     // Only available when buildcfg.Experiment.SPMD
)

// Runtime SPMD token recognition
func IsUniformToken(t token) bool {
    if !buildcfg.Experiment.SPMD {
        return false  // Disabled at runtime
    }
    return t == _Uniform
}

func IsVaryingToken(t token) bool {
    if !buildcfg.Experiment.SPMD {
        return false  // Disabled at runtime  
    }
    return t == _Varying
}
```

**Reference**: Similar to ISPC's context-sensitive keyword handling in `ispc/src/lex.ll`

#### 3. Go Parser Extensions with Runtime Gating

**File**: `src/cmd/compile/internal/syntax/parser.go` (main Go compiler)

Add SPMD syntax parsing with runtime experiment checks:

```go
func (p *parser) type_() Expr {
    switch p.tok {
    // ... existing Go cases ...
    
    case _Uniform, _Varying:
        // SPMD qualified types (uniform/varying) when experiment enabled
        if buildcfg.Experiment.SPMD {
            return p.spmdType()  // Parse uniform/varying types
        }
        // Fall through to _Name case when SPMD disabled
        fallthrough
    case _Name:
        return p.qualifiedName(nil)
    }
}

func (p *parser) spmdType() *SPMDType {
    if !buildcfg.Experiment.SPMD {
        p.syntaxError("uniform/varying require GOEXPERIMENT=spmd")
        return nil
    }
    
    typ := new(SPMDType)
    typ.Qualifier = p.tok  // _Uniform or _Varying
    p.next()  // consume qualifier
    
    // Handle constraint for varying types: varying[n] or varying[]
    if typ.Qualifier == _Varying && p.got(_Lbrack) {
        if p.got(_Rbrack) {
            // varying[] - universal constraint  
        } else {
            typ.Constraint = p.expr()
            p.want(_Rbrack)
        }
    }
    
    typ.Elem = p.type_()  // Parse underlying type
    return typ
}
```

**Reference**: ISPC's parsing patterns in `ispc/src/parser.yy` for SPMD constructs

#### 4. Go Type System Integration

**File**: `src/cmd/compile/internal/types2/types.go` (main Go compiler)

Add SPMD type system to Go's type checker:

```go
// SPMD type qualifiers
const (
    Uniform TypeQualifier = 1 << iota
    Varying
)

type SPMDType struct {
    Qualifier TypeQualifier
    Elem      Type
    LaneCount int  // Determined at SSA generation time
}

func (t *SPMDType) String() string {
    if !buildcfg.Experiment.SPMD {
        return t.Elem.String() // Fallback when experiment disabled
    }
    
    switch t.Qualifier {
    case Uniform:
        return "uniform " + t.Elem.String()
    case Varying:
        return "varying " + t.Elem.String()
    default:
        return t.Elem.String()
    }
}
```

**Reference**: ISPC's type system in `ispc/src/type.h` and `ispc/src/type.cpp`

#### 5. Go Type Checking Rules

**File**: `src/cmd/compile/internal/types2/stmt.go` (main Go compiler)

Implement SPMD type checking in Go's type checker:

```go
func (check *Checker) assignment(x *operand, T Type, context string) {
    if !buildcfg.Experiment.SPMD {
        // Use existing assignment logic
        check.assignmentRegular(x, T, context)
        return
    }
    
    // SPMD-aware assignment checking
    if isSPMDType(T) || isSPMDType(x.typ) {
        check.spmdAssignment(x, T, context)
        return
    }
    
    // Regular assignment for non-SPMD types
    check.assignmentRegular(x, T, context)
}

func (check *Checker) spmdAssignment(x *operand, T Type, context string) {
    // SPMD assignment rules (will be encoded in SSA):
    // - varying to uniform: ERROR
    // - uniform to varying: implicit broadcast
    // - varying to varying: direct assignment
    // - uniform to uniform: direct assignment
}

func (check *Checker) spmdFunction(fn *FuncDecl) {
    if !buildcfg.Experiment.SPMD {
        return
    }
    
    // Check if this is an SPMD function (has varying parameters)
    varyingParams := check.findVaryingParameters(fn)
    if len(varyingParams) == 0 {
        return // Not an SPMD function
    }
    
    // Check SIMD register capacity constraints for SPMD function
    check.checkSPMDFunctionCapacity(fn, varyingParams)
}

func (check *Checker) checkSPMDFunctionCapacity(fn *FuncDecl, varyingParams []SPMDType) {
    // Find the largest varying parameter type (most constraining)
    var constrainingType SPMDType
    var minLaneCount int = math.MaxInt32
    
    for _, vParam := range varyingParams {
        paramElemSize := check.typeSize(vParam.Elem)
        maxLanesForParam := check.simdRegisterSize / paramElemSize
        
        if maxLanesForParam < minLaneCount {
            minLaneCount = maxLanesForParam
            constrainingType = vParam
        }
    }
    
    // Find all varying types used within the function body
    varyingTypes := check.findVaryingTypesInBlock(fn.Body)
    
    // Validate that all varying types in function body can fit the constraint
    for _, vType := range varyingTypes {
        varyingElemSize := check.typeSize(vType.Elem())
        maxLanesForVaryingType := check.simdRegisterSize / varyingElemSize
        
        if maxLanesForVaryingType < minLaneCount {
            // varying type cannot handle the lane count required by parameters
            check.errorf(fn, 
                "varying %s (%d bytes) cannot be used in SPMD function with varying %s parameter (%d bytes): "+
                "SIMD register capacity exceeded (local varying needs %d lanes max, parameter requires %d lanes)",
                vType.Elem(), varyingElemSize, 
                constrainingType.Elem(), check.typeSize(constrainingType.Elem()),
                maxLanesForVaryingType, minLaneCount)
        }
    }
    
    // Record the constraining lane count for code generation
    fn.SPMDLaneCount = minLaneCount
    fn.SPMDConstrainingType = constrainingType
}

func (check *Checker) findVaryingParameters(fn *FuncDecl) []SPMDType {
    var varyingParams []SPMDType
    
    for _, param := range fn.Type.Params.List {
        if spmdType, ok := param.Type.(*SPMDType); ok && spmdType.Qualifier == Varying {
            varyingParams = append(varyingParams, *spmdType)
        }
    }
    
    return varyingParams
}

func (check *Checker) spmdForStmt(stmt *SPMDForStmt) {
    if !buildcfg.Experiment.SPMD {
        check.errorf(stmt, "SPMD for loops require GOEXPERIMENT=spmd")
        return
    }
    
    // Check SIMD register capacity constraints for go for loops
    check.checkSIMDRegisterCapacity(stmt)
    
    // ... other SPMD for checking
}

func (check *Checker) checkSIMDRegisterCapacity(stmt *SPMDForStmt) {
    // Determine the range element type (what we're iterating over)
    var rangeElemType Type
    if rangeExpr := stmt.RangeExpr; rangeExpr != nil {
        switch rType := rangeExpr.Type().(type) {
        case *Slice:
            rangeElemType = rType.Elem()
        case *Array:
            rangeElemType = rType.Elem()
        default:
            // Range over integer - no element type constraint
            return
        }
    } else {
        // Infinite loop - no capacity constraint
        return
    }
    
    // Find all varying types used within the loop body
    varyingTypes := check.findVaryingTypesInBlock(stmt.Body)
    
    // Calculate lane capacity for range element type
    rangeElemSize := check.typeSize(rangeElemType)
    maxLanesForRangeType := check.simdRegisterSize / rangeElemSize
    
    // Validate that all varying types can fit in the same SIMD register width
    for _, vType := range varyingTypes {
        varyingElemSize := check.typeSize(vType.Elem())
        maxLanesForVaryingType := check.simdRegisterSize / varyingElemSize
        
        if maxLanesForVaryingType < maxLanesForRangeType {
            // varying type cannot fit the same number of elements as range type
            // This is ERROR - would exceed SIMD register capacity
            check.errorf(stmt, 
                "varying %s (%d bytes) cannot be used in loop over %s (%d bytes): "+
                "SIMD register capacity exceeded (local varying needs %d lanes max, range requires %d lanes)",
                vType.Elem(), varyingElemSize, rangeElemType, rangeElemSize,
                maxLanesForVaryingType, maxLanesForRangeType)
        }
    }
    
    // Adjust fetch size based on whether index is needed and largest varying type used
    minLaneCount := maxLanesForRangeType
    
    if stmt.IndexVar != nil {
        // Index variable is needed - must use int-sized lanes
        intSize := check.typeSize(check.typ[Typ[Int]])
        maxLanesForInt := check.simdRegisterSize / intSize
        
        // Limit to what int lanes can handle
        if maxLanesForInt < minLaneCount {
            minLaneCount = maxLanesForInt
        }
    }
    
    // Further limit by the most constraining varying type in the loop
    for _, vType := range varyingTypes {
        varyingElemSize := check.typeSize(vType.Elem())
        maxLanesForVaryingType := check.simdRegisterSize / varyingElemSize
        
        if maxLanesForVaryingType < minLaneCount {
            minLaneCount = maxLanesForVaryingType
        }
    }
    
    // Record the final lane count for code generation
    stmt.AdjustedLaneCount = minLaneCount
    
    if minLaneCount < maxLanesForRangeType {
        check.recordAdjustment(stmt, 
            "fetch limited to %d elements due to SIMD register capacity constraints", 
            minLaneCount)
    }
}

func (check *Checker) findVaryingTypesInBlock(block *Block) []SPMDType {
    var varyingTypes []SPMDType
    
    // Walk AST nodes in block to find varying variable declarations and uses
    ast.Inspect(block, func(n ast.Node) bool {
        switch node := n.(type) {
        case *ast.VarSpec:
            if spmdType, ok := node.Type.(*SPMDType); ok && spmdType.Qualifier == Varying {
                varyingTypes = append(varyingTypes, *spmdType)
            }
        case *ast.AssignStmt:
            // Check for varying types in assignments
            for _, expr := range node.Rhs {
                if exprType := check.typeOf(expr); isSPMDVaryingType(exprType) {
                    varyingTypes = append(varyingTypes, *exprType.(*SPMDType))
                }
            }
        }
        return true
    })
    
    return varyingTypes
}
```

**Reference**: ISPC's type checking patterns in `ispc/src/expr.cpp` and `ispc/src/stmt.cpp`

#### 6. Go SSA Generation

**File**: `src/cmd/compile/internal/ssagen/ssa.go` (main Go compiler)

Generate SPMD-aware SSA from Go AST:

```go
func (s *state) stmt(n ir.Node) {
    switch n.Op() {
    // ... existing Go cases ...
    
    case ir.OSPMDFOR:
        if buildcfg.Experiment.SPMD {
            s.spmdForStmt(n.(*ir.SPMDForStmt))
        } else {
            s.Fatalf("SPMD for statement without experiment enabled")
        }
    }
}

func (s *state) spmdForStmt(n *ir.SPMDForStmt) {
    // Generate standard SSA operations that map directly to LLVM IR:
    // - OpPhi: Loop counter and mask management
    // - OpCall: Function calls with implicit mask parameter (mask-first)
    // - OpVectorAdd/OpVectorMul: Vector arithmetic operations
    // - OpSelect: Masked conditional execution
    // - OpAnd/OpOr/OpNot: Mask combination operations
}
```

**Reference**: ISPC's SSA generation patterns in `ispc/src/stmt.cpp`

Constrained varying are implemented as static array of a multiple varying that are then used to unroll code with it. Multiple mask are maintained for each varying to allow for the code to operate on virtually all lanes of the constrained varying at once. If the multiple requested match the size of the varying of the platform, optimization is triggered and the code directly manipulate one varying.

Open ended constrained type, aka `varying[]` embed the static array in a type that contain enough information for FromConstrained to generated an array of varying.

#### 7. Go Standard Library Extensions

**File**: `src/lanes/lanes.go` (Go standard library)

builtin function are directly matched to actual SSA operation that take a mask as a parameter.

```go
//go:build goexperiment.spmd

package lanes

// Count returns the number of SIMD lanes for type T
// Implementation is target-dependent (provided by compiler backend)
func Count[T any]() int {
    // Compiler intrinsic - replaced during SSA generation
    return __builtin_spmd_lane_count[T]()
}

// Index returns the current lane index
func Index() varying int {
    // Compiler intrinsic - generates SSA OpSPMDLaneIndex
    return __builtin_spmd_lane_index()
}

// Broadcast broadcasts a uniform value to all lanes
func Broadcast[T any](value uniform T, lane int) varying T {
    // Compiler intrinsic - generates SSA OpSPMDBroadcast
    return __builtin_spmd_broadcast(value, lane)
}
```

**File**: `src/reduce/reduce.go` (Go standard library)

```go
//go:build goexperiment.spmd

package reduce

// Add performs horizontal addition across all lanes
func Add[T Numeric](v VaryingNumeric[T]) uniform T {
    // Check if this is a constrained varying[] type
    if constrainedData, ok := any(v).(varying[] T); ok {
        // Handle varying[] T - use lanes.FromConstrained to loop properly
        values, masks := lanes.FromConstrained(constrainedData)
        var total uniform T
        
        // Loop over each unconstrained group and accumulate
        go for i, value := range values {
           // Only add lanes that are active according to the mask
            if masks[i] {
                maskedSum := __builtin_spmd_reduce_add(value)
                total += maskedSum
            }
        }
        
        return total
    } else {
        // Handle varying T - direct compiler intrinsic
        return __builtin_spmd_reduce_add(v.(varying T))
    }
}

// Any returns true if any lane is true
func Any(v VaryingBool) uniform bool {
    // Check if this is a constrained varying[] type
    if constrainedData, ok := any(v).(varying[] bool); ok {
        // Handle varying[] bool - use lanes.FromConstrained to loop properly
        values, masks := lanes.FromConstrained(constrainedData)
        
        // Loop over each unconstrained group
        go for i, value := range values {
            // Check if any active lane in this group is true
            maskedValue := value && masks[i]
            if __builtin_spmd_reduce_or(maskedValue) {
                return true
            }
        }
        
        return false
    } else {
        // Handle varying bool - direct compiler intrinsic
        return __builtin_spmd_reduce_or(v.(varying bool))
    }
}

// All returns true if all lanes are true
func All(v VaryingBool) uniform bool {
    // Check if this is a constrained varying[] type
    if constrainedData, ok := any(v).(varying[] bool); ok {
        // Handle varying[] bool - use lanes.FromConstrained to loop properly
        values, masks := lanes.FromConstrained(constrainedData)
        
        // Loop over each unconstrained group
        go for i, value := range values {
            // For inactive lanes, treat as true (so they don't affect the result)
            // Only active lanes need to be true
            maskedValue := value || !masks[i]
            if !__builtin_spmd_reduce_and(maskedValue) {
                return false
            }
        }
        
        return true
    } else {
        // Handle varying bool - direct compiler intrinsic
        return __builtin_spmd_reduce_and(v.(varying bool))
    }
}

// From converts varying numerical/boolean types to array of underlying type
func From[T NumericOrBool](v varying T) []T {
    // Compiler intrinsic - extracts lane values to array
    return __builtin_spmd_to_array(v)
}

// NumericOrBool constraint for reduce.From - includes all numeric types plus bool
type NumericOrBool interface {
    bool | 
    int | int8 | int16 | int32 | int64 |
    uint | uint8 | uint16 | uint32 | uint64 |
    float32 | float64 | complex64 | complex128
}
```

#### 8. Standard Library Integration

**File**: `src/fmt/print.go` (Go standard library)

Extend Printf to automatically handle varying types:

```go
// Add to fmt package
func formatValue(v interface{}, verb rune, flag *flags) (result string, wasString bool) {
    if buildcfg.Experiment.SPMD && verb == 'v' {
        // Check if this is a varying type using reflection
        if isSPMDVaryingType(reflect.TypeOf(v)) {
            // Automatically convert varying to array for display
            array := callReduceFrom(v)  // Call reduce.From internally
            return formatValue(array, verb, flag)
        }
    }
    
    // Existing Printf logic
    return formatValueRegular(v, verb, flag)
}
```

**PoC Limitations**:

- Only basic numerical types (`int`, `float32`, `float64`, `uint`, etc.)
- Only works with `%v` verb (not `%d`, `%f`, etc.)
- Complex types, structs, and pointers not supported

### Phase 2: TinyGo Backend Changes

#### 8. TinyGo SSA-to-LLVM IR Conversion (Dual Mode)

**File**: `src/compiler/compiler.go` (TinyGo)

TinyGo consumes Go SSA and generates either SIMD or scalar LLVM IR based on build flags:

```go
func (c *compilerContext) parseSSA(fn *ssa.Function) {
    for _, block := range fn.Blocks {
        for _, instr := range block.Instrs {
            switch instr := instr.(type) {
            // ... existing TinyGo SSA handling ...
            
            case *ssa.Phi:
                // Standard phi nodes, now used for mask management
                c.compilePhi(instr)
            case *ssa.Call:
                // Function calls with mask-first parameter for SPMD functions
                c.compileCall(instr)
            case *ssa.BinOp:
                // Vector operations (when operands are vector types)
                c.compileBinOp(instr)
            case *ssa.Select:
                // Masked conditional execution
                c.compileSelect(instr)
            // All SPMD operations now use standard SSA instructions
            }
        }
    }
}

func (c *compilerContext) compileBinOp(instr *ssa.BinOp) {
    // Detect if operands are vector types (varying values)
    if c.isVectorType(instr.X.Type()) || c.isVectorType(instr.Y.Type()) {
        if c.simdEnabled {
            // SIMD mode: Generate LLVM vector operations
            vectorType := llvm.VectorType(llvm.Int32Type(), 4) // WASM SIMD128
            switch instr.Op {
            case token.ADD:
                c.builder.CreateAdd(lhs, rhs, "")
            case token.MUL:
                c.builder.CreateMul(lhs, rhs, "")
            }
        } else {
            // Scalar mode: Generate element-wise scalar operations
            c.generateScalarVectorOp(instr)
        }
    } else {
        // Regular scalar operation
        c.compileRegularBinOp(instr)
    }
}

func (c *compilerContext) compileCall(instr *ssa.Call) {
    // Check if this is an SPMD function call (has varying parameters)
    if c.isSPMDFunction(instr.Call.Value) {
        // Insert mask as first argument for SPMD functions
        maskArg := c.getCurrentMask()
        args := append([]llvm.Value{maskArg}, c.compileArgs(instr.Call.Args)...)
        c.builder.CreateCall(instr.Call.Value, args, "")
    } else {
        // Regular function call
        c.compileRegularCall(instr)
    }
}

func (c *compilerContext) generateScalarVectorOp(instr *ssa.BinOp) {
    // Generate traditional for loop instead of vector operations
    // for i := 0; i < laneCount; i++ { result[i] = lhs[i] + rhs[i] }
}
```

**File**: `src/main.go` (TinyGo CLI)

Add `-simd` build flag to TinyGo:

```go
func main() {
    var simdFlag bool
    
    flag.BoolVar(&simdFlag, "simd", true, "Enable SIMD code generation for SPMD constructs (default: true)")
    flag.Parse()
    
    config := &compileopts.Config{
        // ... existing config ...
        SIMDEnabled: simdFlag,
    }
    
    // Pass SIMD flag to compiler context
    compilerContext.simdEnabled = config.SIMDEnabled
}
```

**Reference Patterns from ISPC/LLVM**:

- `ispc/src/llvmutil.cpp`: Vector type creation and manipulation
- `llvm/include/llvm/IR/Intrinsics.td`: Vector intrinsic definitions
- `llvm/lib/Target/WebAssembly/WebAssemblyInstrSIMD.td`: WASM SIMD patterns

#### 9. WASM SIMD128 Code Generation

**File**: `src/targets/wasm.go` (TinyGo WASM target)

```go
func (c *compilerContext) generateWASMSIMD(vectorType llvm.Type, op string) llvm.Value {
    // Map LLVM vector operations to WASM SIMD128 instructions
    switch op {
    case "i32x4.add":
        // Generate WASM i32x4.add instruction
        return c.builder.CreateCall(c.mod.NamedFunction("llvm.wasm.add.v4i32"), args, "")
    case "i32x4.mul":
        // Generate WASM i32x4.mul instruction
        return c.builder.CreateCall(c.mod.NamedFunction("llvm.wasm.mul.v4i32"), args, "")
    case "i32x4.extract_lane":
        // Generate WASM i32x4.extract_lane instruction
        return c.builder.CreateCall(c.mod.NamedFunction("llvm.wasm.extract.lane.v4i32"), args, "")
    }
}
```

**Reference**: `llvm/lib/Target/WebAssembly/WebAssemblyInstrSIMD.td` for WASM SIMD instruction patterns

### Testing Integration

#### 10. Go Frontend Testing

**File**: `test/spmd_frontend.go` (Go compiler test)

```go
// run -goexperiment spmd

package main

import (
    "lanes"
    "reduce"
)

func main() {
    // Test that Go frontend correctly parses and type-checks SPMD code
    var x uniform int = 42
    var y varying int
    
    go for i := range 16 {
        y = x  // uniform to varying broadcast
    }
    
    total := reduce.Add(y)
    println("Frontend test passed:", total)
}
```

#### 11. TinyGo Backend Testing

**File**: `test/spmd_backend.go` (TinyGo WASM test)

```go
// run -goexperiment spmd -target=wasi

package main

import (
    "lanes"
    "reduce"
)

func main() {
    data := []int{1, 2, 3, 4, 5, 6, 7, 8}
    var total int
    
    // Test that TinyGo backend generates WASM SIMD from Go SSA
    go for i := range len(data) {
        total += reduce.Add(varying(data[i]))
    }
    
    if total != 36 {
        panic("Backend SIMD generation failed")
    }
    
    println("TinyGo WASM SIMD test passed:", total)
}
```

**TDD-Driven Testing Workflow**:

```bash
# Phase 0: Run all tests (should initially fail)
go test ./src/cmd/compile/internal/syntax -run TestSPMDParser        # Parser tests
go test ./src/cmd/compile/internal/types2 -run TestSPMDTypeChecking  # Type tests  
go test ./src/cmd/compile/internal/ssagen -run TestSPMDSSAGeneration # SSA tests
./test/integration/spmd/run_tests.sh                                 # Integration tests

# Phase 1: Implement Go frontend to make tests pass
# Each implementation step should be guided by failing tests
go test ./src/cmd/compile/internal/syntax -run TestSPMDParser        # Should pass after lexer/parser
go test ./src/cmd/compile/internal/types2 -run TestSPMDTypeChecking  # Should pass after type system
go test ./src/cmd/compile/internal/ssagen -run TestSPMDSSAGeneration # Should pass after SSA generation

# Phase 2: Implement TinyGo backend to make integration tests pass
./test/integration/spmd/run_tests.sh                                 # Should pass after backend implementation

# Continuous validation throughout development
make test-spmd  # Run all SPMD tests
```

## Build System Integration

### Go + TinyGo Build Pipeline

The SPMD build process involves both compilers:

```bash
# Two-stage build process

# Stage 1: Go frontend generates SPMD-aware SSA
GOEXPERIMENT=spmd go build -buildmode=archive -o spmd_program.a program.go

# Stage 2: TinyGo backend converts SSA to WASM SIMD
GOEXPERIMENT=spmd tinygo build -target=wasi -o program.wasm program.go
```

### Integration Points

**Go Compiler Integration**:

```bash
# Go must pass SPMD SSA using standard opcodes to TinyGo
if [[ "$GOEXPERIMENT" == *"spmd"* ]]; then
    echo "Go: Generating SPMD SSA with standard vector operations"
    # Generate standard SSA: OpPhi, OpCall, OpVectorAdd, OpSelect, etc.
fi
```

**TinyGo Backend Integration**:

```bash
# TinyGo must recognize and convert SPMD SSA to LLVM IR
if [[ "$GOEXPERIMENT" == *"spmd"* ]]; then
    echo "TinyGo: Converting SPMD SSA to WASM SIMD128"
    # Enable WASM SIMD target features
    export TINYGO_TARGET_FEATURES="+simd128"
fi
```

### TDD Testing Workflow

```bash
# Test-First Development Cycle

# 1. Run tests to see current failures
make test-spmd-parser      # See which parser features need implementation
make test-spmd-types       # See which type checking rules need implementation  
make test-spmd-ssa         # See which SSA opcodes need implementation
make test-spmd-integration # See which backend features need implementation

# 2. Implement smallest change to make one test pass
# (Repeat until all tests pass)

# 3. Verify full pipeline after each major milestone
GOEXPERIMENT=spmd tinygo build -target=wasi -o test.wasm examples/simple-sum/main.go
go run examples/wasmer-runner.go test.wasm

# 4. Regression testing with ALL examples (including complex ones)
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions varying-universal-constrained union-type-generics; do
    echo "Testing $example..."
    GOEXPERIMENT=spmd tinygo build -target=wasi -o temp.wasm "examples/$example/main.go"
    go run examples/wasmer-runner.go temp.wasm
done

# 5. Performance validation
wasm2wat examples/simple-sum.wasm | grep -c "v128"  # Count SIMD instructions
wasm2wat examples/simple-sum.wasm | grep -E "(i32x4.add|i32x4.mul)"  # Verify vector ops
```

## Error Handling Strategy

### Graceful Degradation

When SPMD experiment is disabled:

1. **Parser**: Treat `uniform`/`varying` as regular identifiers
2. **Type Checker**: No special SPMD type rules
3. **Standard Library**: Stub implementations that panic with helpful messages
4. **Error Messages**: Clear indication that GOEXPERIMENT=spmd is required

### Example Error Messages

```
error: uniform/varying type qualifiers require GOEXPERIMENT=spmd
  --> example.go:5:9
   |
5  |     var x uniform int
   |           ^^^^^^^
   | 
help: enable the SPMD experiment:
   |     GOEXPERIMENT=spmd go build example.go

error: go for loops require GOEXPERIMENT=spmd  
  --> example.go:10:5
   |
10 |     go for i := range data {
   |     ^^^^^^
   |
help: use regular for loop or enable SPMD experiment
```

## Documentation Updates

### Documentation Updates

Update documentation for Go+TinyGo SPMD implementation:

**Go Documentation**:

- `go help buildconstraint`: Mention `goexperiment.spmd` tag
- `go help experiment`: Document `GOEXPERIMENT=spmd` frontend support
- Language specification: Add experimental SPMD section

**TinyGo Documentation**:

- `tinygo help`: Document consuming Go SPMD SSA
- TinyGo website: Add SPMD backend section with WASM examples
- GitHub README: Include Go+TinyGo SPMD integration status

### Package Documentation

```go
// Package lanes provides cross-lane operations for SPMD programming.
//
// This package requires GOEXPERIMENT=spmd to be enabled at build time.
// When disabled, all functions will panic with appropriate error messages.
//
// The Go frontend generates SPMD SSA opcodes that TinyGo converts to
// WebAssembly SIMD128 instructions.
//
// Example usage:
//
//    GOEXPERIMENT=spmd tinygo build -target=wasi program.go
//
package lanes
```

## Migration and Compatibility

### Future Full Implementation

To move beyond PoC to full Go implementation:

1. **Main Go Backend**: Add native SPMD codegen to `cmd/compile` (beyond SSA generation)
2. **Multiple Targets**: Extend beyond WASM to x86 AVX, ARM NEON, etc.
3. **Advanced Features**: Full constraint system and comprehensive cross-lane operations
4. **Standard Library**: Complete `lanes` and `reduce` packages with all SPMD primitives
5. **Performance**: LLVM-level optimizations and vectorization passes

### Versioning

The SPMD experiment should be documented in:

- Go release notes when introduced
- `go version` output when experiment is enabled
- Build information in compiled binaries

## Implementation Checklist

### Phase 0: Test-Driven Development Setup

- [ ] Create parser test suite using existing examples
- [ ] Create type checker test suite with positive/negative cases
- [ ] Create SSA generation test suite to verify correct opcodes
- [ ] Create integration test suite for end-to-end WASM execution
- [ ] Set up automated test runner for continuous validation
- [ ] Document test-driven development workflow

### Phase 1: Go Frontend (main Go compiler)

- [ ] Add `SPMD bool` to `src/internal/goexperiment/flags.go`
- [ ] Generate Go experimental build constraint files
- [ ] **TDD**: Implement Go lexer modifications (guided by parser tests)
- [ ] **TDD**: Add Go parser support for SPMD syntax (make parser tests pass)
- [ ] **TDD**: Extend Go type system with SPMD types (guided by type checker tests)
- [ ] **TDD**: Implement Go type checking rules (make type checker tests pass)
- [ ] **TDD**: Add Go SSA generation for SPMD constructs (make SSA tests pass)
- [ ] Create `lanes` and `reduce` standard library packages with all functions needed for examples

### Phase 2: TinyGo Backend (Dual Code Generation)

- [ ] **TDD**: Add TinyGo SSA-to-LLVM IR conversion (guided by integration tests)
- [ ] **TDD**: Implement LLVM vector type mapping (verify SIMD instruction generation)
- [ ] **TDD**: Add WASM SIMD128 code generation patterns (make integration tests pass)
- [ ] **TDD**: Add scalar fallback code generation (when -simd=false flag used)
- [ ] **TDD**: Implement TinyGo `-simd=true/false` build flag for dual mode compilation
- [ ] **TDD**: Integrate with TinyGo's WASM target (verify wasmer-go execution in both modes)

### Phase 3: Validation and Documentation

- [ ] Ensure ALL examples compile to both SIMD and scalar WASM and execute correctly (including IPv4 parser and base64 decoder)
- [ ] Verify identical output between SIMD and scalar modes for all examples
- [ ] Benchmark performance differences between SIMD and scalar modes
- [ ] Verify backward compatibility tests pass (legacy examples still work)
- [ ] Confirm illegal examples fail with appropriate error messages
- [ ] Document Go+TinyGo build pipeline
- [ ] Create performance benchmarks using examples

### Reference Implementation Patterns

This implementation follows established SPMD patterns from:

- **ISPC SSA generation**: `ispc/src/stmt.cpp`, `ispc/src/expr.cpp`
- **LLVM vector codegen**: `llvm/lib/Target/WebAssembly/WebAssemblyInstrSIMD.td`
- **SIMD type mapping**: `ispc/src/llvmutil.cpp`

The Go frontend generates SPMD-aware SSA that TinyGo's LLVM backend converts to WebAssembly SIMD128 instructions for wasmer-go execution.
