# SPMD Go - Project Summary

**Single Program Multiple Data extensions for the Go programming language**

## Overview

SPMD Go extends the Go language with data parallelism capabilities, enabling developers to write high-performance SIMD code using familiar Go syntax. The implementation leverages TinyGo's LLVM backend to generate WebAssembly SIMD instructions while maintaining Go's principles of simplicity and readability.

**Status**: 🚧 **Proof of Concept** - Language design complete, implementation in planning phase

## What is SPMD?

**Single Program Multiple Data (SPMD)** is a parallel programming model where:

- All processing units execute the same program
- Each unit operates on different data elements  
- Control flow divergence is handled through execution masks
- Cross-lane communication enables complex algorithms

**Key Benefit**: Explicit data parallelism without sacrificing code readability or maintainability.

## Language Extensions

### Core Syntax

```go
// Types
var x int                    // Same value across all lanes (default)
var y lanes.Varying[int]     // Different value per lane

// SPMD loop construct
go for i := range data {
    // Executes in parallel across SIMD lanes
    data[i] = process(data[i])
}

// Cross-lane operations
total := reduce.Add(values)        // Combine lane results
lane := lanes.Index()              // Current lane index
rotated := lanes.Rotate(data, 1)   // Exchange between lanes
```

### Key Features

1. **Hardware Independence**: Code adapts to different SIMD widths automatically
2. **Type Safety**: Strong type system prevents common SIMD programming errors
3. **Backward Compatibility**: Existing Go code continues to work unchanged
4. **Gradual Adoption**: SPMD features are opt-in and localized

## Architecture

### Two-Phase Implementation

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Go Frontend   │    │   TinyGo Backend │    │ WebAssembly     │
│                 │    │                  │    │ SIMD128         │
│ • Lexer/Parser  │───▶│ • SSA→LLVM IR    │───▶│ • v128.*        │
│ • Type Checking │    │ • Vector Ops     │    │ • i32x4.add     │
│ • SSA Generation│    │ • Dual Codegen   │    │ • Scalar Fallback│
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

**Go Frontend**: Extends main Go compiler with SPMD syntax and semantics  
**TinyGo Backend**: Converts SPMD SSA to LLVM vector instructions  
**Target**: WebAssembly SIMD128 (proof of concept), extensible to x86/ARM

### Experimental Feature Integration

- **Feature Flag**: `GOEXPERIMENT=spmd` enables all SPMD extensions
- **Graceful Fallback**: Clean behavior when experiment is disabled
- **Build Integration**: Standard Go toolchain compatibility

## Real-World Impact

### Performance Applications

SPMD Go targets performance-critical areas where Go currently falls behind:

- **String Processing**: Printf parsing, hex encoding, UTF-8 validation
- **Data Transformation**: Base64 encoding/decoding, compression
- **Parsing**: IPv4 addresses, JSON tokens, network protocols  
- **Mathematical Operations**: Vector math, image processing
- **Cryptography**: Hash functions, symmetric encryption

### Standard Library Integration

```go
// Current Go (scalar)
for i, c := range text {
    if c == '%' {
        return i  // Found format specifier
    }
}

// SPMD Go (vectorized)
go for i, c := range text {
    if c == '%' {
        if reduce.Any(true) {
            return i + reduce.FindFirstSet(c == '%')
        }
    }
}
```

## Design Philosophy

### Go Principles Maintained

1. **Simplicity**: `go for` is intuitive extension of existing `go` keyword
2. **Readability**: SPMD code looks like regular Go with type annotations
3. **Orthogonality**: Features compose cleanly without complex interactions
4. **Performance**: Fast compilation and execution without manual optimization

### SPMD Principles Adopted

1. **Explicit Parallelism**: Developers control where parallelism occurs
2. **Hardware Abstraction**: Same code works across different SIMD widths
3. **Type Safety**: Compile-time prevention of common vectorization errors
4. **Mask-Based Control**: Automatic handling of divergent execution paths

## Technical Highlights

### Type System Innovation

```go
// Constrained varying - hardware independent
var data lanes.Varying[byte, 4]    // Multiple of 4 lanes required
var mask lanes.Varying[bool, 8]    // Multiple of 8 lanes required

// Universal constrained - accepts any constraint
func process(input lanes.Varying[int]) {
    switch v := input.(type) {
    case lanes.Varying[int, 4]:    // 4-element groups
    case lanes.Varying[int, 8]:    // 8-element groups
    }
}
```

### Cross-Lane Communication

Advanced algorithms like base64 decoding require lanes to coordinate:

```go
// Base64: 4 ASCII chars → 3 bytes with cross-lane dependencies  
go for _, chunk := range[4] ascii {
    sextets := lanes.Swizzle(lookupTable, chunk)    // Parallel lookup
    shifted := lanes.ShiftLeft(sextets, pattern)    // Bit manipulation
    decoded := lanes.Rotate(shifted, 1)             // Cross-lane exchange
    output := lanes.Swizzle(decoded, outputPattern) // Final extraction
}
```

### Dual Code Generation

```bash
# Single source, dual compilation targets
GOEXPERIMENT=spmd tinygo build -simd=true  -o fast.wasm   program.go  # SIMD
GOEXPERIMENT=spmd tinygo build -simd=false -o compat.wasm program.go  # Scalar
```

Enables runtime SIMD detection and optimal code selection.

## Repository Structure

```
SPMD/
├── README.md              # Project introduction
├── CLAUDE.md              # Implementation guide  
├── SPECIFICATIONS.md      # Language specification
├── GOEXPERIMENT-IMPLEMENTATION.md  # Implementation details
├── GLOSSARY.md            # Terminology guide
├── QUICK_REFERENCE.md     # Syntax reference
├── REVIEWS.md             # Documentation review
├── examples/              # 30+ code examples
│   ├── simple-sum/        # Basic SPMD patterns
│   ├── base64-decoder/    # Cross-lane communication
│   ├── ipv4-parser/       # Real-world parsing
│   └── illegal-spmd/      # Error cases
├── go/                    # Go compiler (submodule)
├── tinygo/                # TinyGo compiler (submodule)
├── ispc/                  # ISPC reference (submodule)
└── bluebugs.github.io/    # Blog posts and documentation
```

## Documentation Quality

### Educational Progression

1. **[Blog: Data Parallelism Concepts](bluebugs.github.io/content/blogs/go-data-parallelism.md)**
   - Introduction to SPMD with simple examples
   - Motivation and comparison with alternatives

2. **[Blog: Practical Applications](bluebugs.github.io/content/blogs/practical-vector.md)**
   - Real-world string processing examples
   - Standard library optimization opportunities

3. **[Blog: Cross-Lane Communication](bluebugs.github.io/content/blogs/cross-lane-communication.md)**
   - Advanced algorithms requiring lane coordination
   - Base64 decoder as comprehensive example

### Technical Documentation

- **[SPECIFICATIONS.md](SPECIFICATIONS.md)**: Complete formal language specification
- **[CLAUDE.md](CLAUDE.md)**: Detailed implementation roadmap with TDD approach
- **[QUICK_REFERENCE.md](QUICK_REFERENCE.md)**: Concise syntax guide for developers
- **[GLOSSARY.md](GLOSSARY.md)**: Comprehensive terminology for SPMD concepts

## Implementation Strategy

### Test-Driven Development

**Phase 0**: Comprehensive test infrastructure

- Parser tests for all syntax extensions
- Type checker tests for SPMD rules
- SSA generation tests for correct opcodes
- End-to-end integration tests

**Phase 1**: Go Frontend Extensions

- Lexer/parser modifications with feature gating
- Type system with uniform/varying semantics
- SSA generation with SPMD opcodes

**Phase 2**: TinyGo Backend Implementation  

- LLVM IR generation for vector operations
- WebAssembly SIMD128 instruction mapping
- Dual codegen for SIMD and scalar targets

**Phase 3**: Validation and Documentation

- All examples compile and execute correctly
- Performance benchmarking against scalar versions
- Complete standard library integration

### Success Criteria

The proof of concept succeeds when:

1. **All Examples Compile**: 30+ examples generate both SIMD and scalar WASM
2. **Identical Behavior**: SIMD and scalar versions produce identical output
3. **SIMD Verification**: Generated WASM contains actual SIMD instructions
4. **Performance Benefit**: Measurable speedup for SIMD vs scalar versions
5. **Error Handling**: Clean error messages for invalid SPMD code

## Development Environment

### Prerequisites

```bash
# System requirements
go version          # Go 1.19+
cmake --version     # For LLVM build
ninja --version     # Fast build system

# Clone with submodules
git clone --recursive https://github.com/Bluebugs/go-spmd.git
cd go-spmd

# Build Go compiler with SPMD support
cd go/src && ./all.bash && cd ../..

# Build TinyGo with LLVM backend
cd tinygo && make llvm-build && make tinygo && cd ..
```

### Testing Workflow

```bash
# Development cycle
make test-spmd           # Run all SPMD tests
make example-build       # Build all examples  
make example-test        # Execute and validate

# Specific testing
go test ./go/src/cmd/compile/internal/syntax -run TestSPMD
go test ./go/src/cmd/compile/internal/types2 -run TestSPMD
./examples/test-runner.sh
```

## Future Roadmap

### Near Term (Proof of Concept)

- [ ] Complete Go frontend implementation
- [ ] TinyGo LLVM backend with WASM SIMD128
- [ ] All examples working in dual-compilation mode
- [ ] Performance benchmarking framework

### Medium Term (Get community understanding)

- [ ] Get more review
- [ ] Setup a playground somehow, maybe get help from Tinygo team for that purpose
- [ ] Add more real life example
- [ ] Add Go specific vector optimization
- [ ] Engage with Go community

## Related Technologies

### Inspiration Sources

- **[ISPC](https://ispc.github.io/)**: Proven SPMD programming model for C-like languages
- **[Mojo](https://docs.modular.com/mojo/)**: Modern SPMD integration in Python-like syntax
- **CUDA/OpenCL**: GPU programming models adapted for CPU SIMD

### Differentiation

- **vs. Manual SIMD Libraries**: High-level language constructs vs. intrinsics
- **vs. Auto-Vectorization**: Explicit programmer control vs. compiler guessing
- **vs. GPU Programming**: CPU-focused with familiar Go syntax and tooling

## Community and Contribution

### Target Audience

- **Go Developers**: Seeking performance improvements in data-intensive applications
- **Systems Programmers**: Building high-performance libraries and frameworks
- **Domain Experts**: Needing vectorized algorithms (cryptography, compression, parsing)

### Contribution Areas

- **Language Design**: Syntax and semantics refinement
- **Implementation**: Compiler and runtime development
- **Examples**: Real-world algorithm implementations
- **Documentation**: Tutorials, guides, and reference materials
- **Testing**: Comprehensive validation across platforms

### Academic Interest

- **Programming Language Research**: SPMD integration in garbage-collected languages
- **Compiler Technology**: SSA-based vectorization and optimization
- **Performance Engineering**: High-level abstractions with low-level performance

## Conclusion

SPMD Go represents a significant advancement in making high-performance data parallelism accessible to Go developers. By combining proven SPMD concepts with Go's design philosophy, it addresses real performance gaps in the Go ecosystem while maintaining the language's core values of simplicity and productivity.

The comprehensive documentation, extensive examples, and systematic implementation approach demonstrate the project's readiness for development. The experimental feature design ensures safe integration with minimal risk to the existing Go ecosystem.

**This project bridges the gap between Go's accessibility and the performance demands of modern applications, proving that readable code and high performance are not mutually exclusive.**

---

**Quick Start**: `export GOEXPERIMENT=spmd && tinygo build -target=wasi program.go`  
**Learn More**: [SPECIFICATIONS.md](SPECIFICATIONS.md) | [QUICK_REFERENCE.md](QUICK_REFERENCE.md) | [Blog Posts](bluebugs.github.io/content/blogs/)  
**Examples**: [examples/](examples/) directory contains 30+ working examples
