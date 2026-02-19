# SPMD TinyGo Proof of Concept - Scope and Expectations

This document clarifies exactly what will and won't work in the TinyGo SPMD proof of concept implementation.

## What Works in the PoC

### ✅ Frontend (Parsing & Type Checking)
- **Lexer**: Recognizes `uniform` and `varying` keywords in type contexts
- **Parser**: Accepts `go for` SPMD loop syntax with range expressions
- **Type System**: Validates uniform/varying type qualifiers and assignment rules
- **Error Messages**: Reports SPMD-specific errors for illegal constructs

### ✅ Basic Standard Library
**`lanes` package** (minimal implementation):
- `lanes.Count(T)` - Returns 4 for WASM SIMD128 (compile-time constant)
- `lanes.Index()` - Returns `lanes.Varying[int]` with values [0, 1, 2, 3]

**`reduce` package** (basic operations):
- `reduce.Add(lanes.Varying[T])` - Sum across all lanes
- `reduce.Any(lanes.Varying[bool])` - Logical OR across lanes
- `reduce.All(lanes.Varying[bool])` - Logical AND across lanes

### ✅ Code Generation
- **LLVM IR**: TinyGo generates vector IR for varying operations
- **WASM SIMD128**: Outputs WebAssembly SIMD instructions (v128.*, i32x4.*, etc.)
- **Memory Layout**: Varying types stored as SIMD vectors

### ✅ Runtime Execution
- **wasmer-go**: Executes generated WASM binaries with SIMD support
- **Basic Operations**: Vector arithmetic, loads, stores work correctly
- **Test Framework**: Automated testing with SIMD instruction verification

## What's Limited/Missing in the PoC

### ❌ Advanced Cross-Lane Operations
These require complex LLVM IR generation not implemented in PoC:
- `lanes.Broadcast(value, lane)` - Broadcast from specific lane
- `lanes.Rotate(value, offset)` - Rotate values across lanes
- `lanes.Swizzle(value, indices)` - Arbitrary permutation
- `lanes.ShiftLeft/Right()` - Bit shifting operations

### ❌ Complex Reduction Operations
Advanced reductions that need custom LLVM passes:
- `reduce.Max/Min()` - Maximum/minimum across lanes
- `reduce.FindFirstSet()` - Find first true lane
- `reduce.Mask()` - Convert boolean vector to bitmask

### ❌ Advanced Control Flow
Complex masking and divergent execution:
- Nested `go for` loops with different constraints
- Complex conditional masking in deeply nested contexts
- `continue` statement masking (basic implementation only)

### ❌ Constraint System
Full lane count constraint enforcement:
- `varying[n]` type constraints (syntax recognized, not enforced)
- `range[n]` grouping (syntax accepted, no validation)
- Multiple-of-n lane count requirements

### ❌ Full Standard Library Integration
Missing integrations that would require extensive work:
- SPMD-optimized string operations
- Vector math functions
- Parallel encoding/decoding libraries

## Examples That Work

### ✅ Simple Examples
These should compile and run correctly:

```go
// simple-sum: Basic parallel addition
go for i := range len(data) {
    total += reduce.Add(varying(data[i]))
}

// odd-even: Basic conditional processing
go for i := range len(data) {
    if data[i] % 2 == 0 {
        data[i] *= 2
    } else {
        data[i] += 1
    }
}

// bit-counting: Nested loops with uniform inner loop
go for i := range len(data) {
    var count lanes.Varying[int]
    for bit := 0; bit < 8; bit++ {
        if data[i] & (1 << bit) != 0 {
            count++
        }
    }
    result[i] = count
}
```

### ❌ Advanced Examples
These will fail due to missing cross-lane operations:

```go
// base64-decoder: Requires Swizzle, Rotate operations
decoded := lanes.Swizzle(data, pattern)  // NOT IMPLEMENTED

// ipv4-parser: Requires FindFirstSet, Mask operations  
first := reduce.FindFirstSet(conditions)  // NOT IMPLEMENTED
```

## Testing Strategy

### Build Testing
```bash
# These should compile successfully
GOEXPERIMENT=spmd tinygo build -target=wasi simple-sum/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi odd-even/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi bit-counting/main.go

# These should fail (missing operations)
GOEXPERIMENT=spmd tinygo build -target=wasi base64-decoder/main.go  # FAIL
GOEXPERIMENT=spmd tinygo build -target=wasi ipv4-parser/main.go     # FAIL
```

### Runtime Testing
```bash
# Execute basic examples
go run wasmer-runner.go simple-sum.wasm
go run wasmer-runner.go odd-even.wasm

# Verify SIMD instruction generation
wasm2wat simple-sum.wasm | grep -E "(v128|i32x4)"
```

### Expected WASM SIMD Instructions
The PoC should generate these WebAssembly SIMD instructions:

```wasm
;; Vector loads/stores
v128.load
v128.store

;; Integer vector operations  
i32x4.splat     ;; Broadcast uniform to varying
i32x4.add       ;; Varying arithmetic
i32x4.mul
i32x4.extract_lane  ;; Extract for reductions

;; Conditional operations (basic masking)
v128.bitselect  ;; Conditional selection
```

## Success Criteria

The PoC is successful if:

1. **✅ Basic SPMD syntax** parses and type-checks correctly
2. **✅ Simple examples compile** to WASM with SIMD instructions
3. **✅ WASM binaries execute** correctly in wasmer-go
4. **✅ Vector operations** produce expected results
5. **❌ Advanced examples fail** gracefully with clear error messages
6. **✅ SIMD instructions** are visible in generated WASM

## Future Work Beyond PoC

To make this production-ready:

1. **Implement cross-lane operations** with complex LLVM IR generation
2. **Add full constraint system** with compile-time validation
3. **Extend standard library** with comprehensive SPMD functions
4. **Optimize performance** with better vectorization
5. **Add debugging support** for SPMD programs
6. **Port to native Go compiler** for full ecosystem integration

## Expected Development Timeline

- **Week 1-2**: Frontend implementation (lexer, parser, type checker)
- **Week 3-4**: Basic code generation and standard library
- **Week 5-6**: WASM integration and testing framework
- **Week 7-8**: Bug fixing and demonstration preparation

The PoC demonstrates the feasibility of SPMD Go while clearly identifying the scope of work needed for a full implementation.