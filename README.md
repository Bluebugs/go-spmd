# SPMD Implementation for Go via TinyGo

> **⚠️ WORK IN PROGRESS - NOT USABLE YET**
>
> This is an experimental proof-of-concept that is currently under development. The SPMD language extensions and compiler modifications are not implemented yet. This repository serves as a research workspace and implementation planning document.

This repository implements Single Program Multiple Data (SPMD) support for Go, extending TinyGo with SIMD capabilities similar to Intel's ISPC and Mojo's SIMD features. The implementation targets WebAssembly SIMD as the initial backend.

## Repository Structure

This is a workspace containing three main components as git submodules:

- **`go/`** - Modified Go compiler (Bluebugs fork) with SPMD language extensions
- **`tinygo/`** - Modified TinyGo compiler (Bluebugs fork) with LLVM-based SPMD backend
- **`ispc/`** - Reference Intel ISPC implementation for comparison
- **`bluebugs.github.io/`** - Documentation and blog posts explaining SPMD concepts

## What is SPMD?

SPMD (Single Program Multiple Data) allows writing data-parallel code where:

- All SIMD lanes execute the same program
- Each lane operates on different data elements
- Execution masks handle control flow divergence
- Cross-lane operations enable efficient vectorization

### Key Language Extensions

```go
// Type qualifiers
var x uniform int      // Scalar value, same across lanes
var y varying float32  // Vector value, different per lane

// SPMD loop construct
go for i := range 16 {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [i, i+1, i+2, i+3]
}

// Cross-lane operations
lanes.Count(any) uniform int   // Returns SIMD width (e.g., 4)
lanes.Index()                  // Returns current lane [0,1,2,3]
reduce.All(varying bool)       // Reduce to uniform bool
```

## Prerequisites

### System Requirements

- **Go 1.19+** - Required for building both compilers
- **LLVM/Clang** - For TinyGo compilation (installed automatically)
- **CMake** - For building LLVM components
- **Ninja** - Fast build system for LLVM
- **Git** - For submodule management
- **Standard build tools** (gcc/clang, make)

### Supported Platforms

- Linux (primary development platform)
- macOS (should work with minor adjustments)
- Windows (via WSL recommended)

## Quick Start

### 1. Clone with Submodules

```bash
git clone --recursive https://github.com/Bluebugs/go-spmd.git
cd go-spmd
```

If you already cloned without `--recursive`:

```bash
git submodule update --init --recursive
```

### 2. Build the Modified Go Compiler

```bash
cd go/src
./all.bash  # This builds and tests the Go toolchain
cd ../..
```

The modified Go compiler will be available at `go/bin/go`.

### 3. Build TinyGo with SPMD Support

First, download and build LLVM (this takes 1+ hours):

```bash
cd tinygo
make llvm-source
make llvm-build
```

Then build TinyGo itself:

```bash
make tinygo
cd ..
```

The SPMD-enabled TinyGo compiler will be available at `tinygo/build/tinygo`.

### 4. Verify Installation

✅ **Note**: The following steps will now build the SPMD-enabled Go compiler with lexer modifications.

Test the SPMD-enabled Go compiler:

```bash
./go/bin/go version
```

Test TinyGo (currently unmodified):

```bash
./tinygo/build/tinygo help
```

Check that TinyGo is statically linked (shouldn't show libLLVM/libclang):

```bash
ldd ./tinygo/build/tinygo
```

## Development Workflow

### Building Changes

After making changes to the Go frontend:

```bash
cd go/src
GOEXPERIMENT=spmd ./make.bash  # Build with SPMD experiment enabled
cd ../..
```

After making changes to TinyGo:

```bash
cd tinygo
make tinygo
cd ..
```

### Testing

Run Go compiler tests:

```bash
cd go/src
./run.bash
cd ../..
```

Run TinyGo tests:

```bash
cd tinygo
make test
cd ..
```

### Running SPMD Programs

⚠️ **IMPORTANT**: SPMD syntax is not implemented yet. The following is a planned example of what the syntax will look like:

```go
package main

import (
    "fmt"
    "reduce"  // ❌ NOT IMPLEMENTED - New standard library module
)

// Simple sum function demonstrating SPMD concepts
func sum(a []int) int {
    var s varying int         // ❌ NOT IMPLEMENTED - Different value per lane
    go for _, v := range a {  // ❌ NOT IMPLEMENTED - SPMD loop construct
        s += v                // Each lane accumulates different elements
    }
    return reduce.Add(s)      // ❌ NOT IMPLEMENTED - Combine lane results
}

func main() {
    data := []int{1, 2, 3, 4, 5, 6}
    result := sum(data)
    fmt.Printf("Sum: %d\n", result) // Expected: 21
}
```

**How this would work when implemented:**

- Array `[1,2,3,4,5,6]` gets distributed across 4 SIMD lanes
- Lane 1 processes elements 1,5 (sum=6)
- Lane 2 processes elements 2,6 (sum=8)
- Lane 3 processes element 3 (sum=3)
- Lane 4 processes element 4 (sum=4)
- `reduce.Add([6,8,3,4])` combines partial sums → final result: 21

When implemented, it would compile with TinyGo targeting WebAssembly SIMD:

```bash
# ❌ THIS WILL NOT WORK YET - SPMD SYNTAX NOT IMPLEMENTED
./tinygo/build/tinygo build -target=wasm -o example.wasm example.go
```

## Implementation Status

This is a proof-of-concept implementation with the following status:

### ✅ Completed

- **Phase 0**: Foundation infrastructure (GOEXPERIMENT setup, test framework)
- **Phase 1.1**: GOEXPERIMENT integration for SPMD flag
- **Phase 1.2**: Lexer modifications for `uniform`/`varying` keywords with conditional recognition
- Repository structure and submodule setup
- Basic project documentation and implementation planning

**Phase 1.2 Key Achievement**: Context-sensitive lexer with GOEXPERIMENT integration. Architectural decision made to defer full backward compatibility to parser level (Phase 1.3).

### 🚧 In Progress  

- **Phase 1.3**: Parser extensions for SPMD syntax constructs

### 📋 Planned (Not Yet Started)

- **Phase 1.4+**: Type system implementation and type checking rules
- **Phase 1.7**: SPMD SSA generation with predicated operations
- **Phase 1.8-1.9**: Standard library extensions (`lanes` and `reduce` packages)
- **Phase 2**: TinyGo LLVM backend with dual SIMD/scalar code generation
- WebAssembly SIMD code generation
- Cross-lane communication primitives
- Advanced control flow masking
- Performance optimization passes
- Complete test suite
- Real-world examples

## Project Structure

```
go-spmd/
├── README.md              # This file
├── CLAUDE.md              # Detailed implementation guide
├── LICENSE                # Project license
├── go/                    # Modified Go compiler (submodule)
├── tinygo/                # Modified TinyGo compiler (submodule)  
├── ispc/                  # Reference ISPC implementation (submodule)
└── bluebugs.github.io/    # Documentation and examples
```

## Key Implementation Files

### Go Compiler Extensions

- `go/src/cmd/compile/internal/syntax/` - Lexer and parser changes
- `go/src/cmd/compile/internal/types2/` - Type system with uniform/varying
- `go/src/cmd/compile/internal/ssagen/` - SSA generation for SPMD

### TinyGo Backend

- `tinygo/compiler/` - LLVM IR generation for SPMD
- `tinygo/transform/` - SPMD-specific optimization passes
- `tinygo/targets/wasm.json` - WebAssembly SIMD target configuration

## Contributing

This is experimental research code. Before contributing:

1. Read `CLAUDE.md` for detailed implementation guidelines
2. Study the ISPC reference implementation in `ispc/src/`
3. Review existing SPMD literature and examples
4. Follow the development workflow described above

### Commit Guidelines

- Keep commits focused on single features
- Test changes thoroughly before committing
- Reference ISPC patterns for SPMD-specific implementations
- Update documentation for user-visible changes

## Resources

### Documentation

- [CLAUDE.md](CLAUDE.md) - Complete implementation guide
- [ISPC Documentation](https://ispc.github.io/ispc.html) - Reference SPMD implementation
- [Go Compiler Internals](https://golang.org/s/go13compiler) - Understanding Go's compilation
- [TinyGo Compiler](https://tinygo.org/docs/reference/compiler-internals/) - TinyGo architecture

### Research Papers

- "ispc: A SPMD Compiler for High-Performance CPU Programming" - Intel
- "Mojo SIMD Programming Model" - Modular AI

### Examples and Tutorials

- `bluebugs.github.io/content/blogs/` - SPMD programming tutorials
- `ispc/examples/` - Reference SPMD implementations

## License

This project maintains compatibility with the licenses of its components:

- Go components: BSD-style license (see `go/LICENSE`)
- TinyGo components: BSD-style license (see `tinygo/LICENSE`)
- ISPC components: BSD-style license (see `ispc/LICENSE.txt`)

## Troubleshooting

### Build Issues

**LLVM build fails:**

```bash
# Ensure you have enough disk space (>10GB) and memory (>8GB)
# Try using Clang instead of GCC:
export CC=clang CXX=clang++
cd tinygo && make llvm-build
```

**Go build fails:**

```bash
# Ensure you have Go 1.19+ installed
go version
cd go/src && ./clean.bash && ./all.bash
```

### Runtime Issues

**WebAssembly SIMD not working:**

- Ensure your JavaScript runtime supports WASM SIMD
- Check that TinyGo is targeting the correct WASM features
- Verify vector operations are generating SIMD instructions

For more troubleshooting, see the individual component documentation:

- [TinyGo Building Guide](tinygo/BUILDING.md)
- [Go Contributing Guide](go/CONTRIBUTING.md)

## Contact

This is a research project by Bluebugs. For questions or discussions:

- GitHub Issues: <https://github.com/Bluebugs/go-spmd/issues>
- Blog: <https://bluebugs.github.io/>

---

## A Note on Naming 🐹

In the spirit of Go's creative terminology, we'd like to suggest that `go for` SPMD contexts could be whimsically referred to as **"gophers"** - just as `go func` gave birth to "goroutines."

After all, if goroutines are individual gophers running around concurrently, then gophers would be synchronized squads of gophers marching in perfect SIMD formation!

*(This terminology is purely for entertainment and should not be used in formal documentation to avoid confusion.)*

---

*This implementation is experimental and not intended for production use. It serves as a proof-of-concept for SPMD programming in Go.*
