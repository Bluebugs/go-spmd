# SPMD Go Examples

This directory contains SPMD (Single Program Multiple Data) Go examples extracted from the blog post series on data parallelism in Go. These examples demonstrate experimental language extensions that enable explicit data parallelism using `go for` loops, `lanes.Varying[T]` types, and cross-lane operations.

## Examples Overview

### Basic SPMD Patterns

#### [simple-sum/](simple-sum/)
Basic parallel sum operation demonstrating the `go for` construct and `reduce.Add()`.
- **From**: [Data Parallelism: simpler solution for Golang?](../bluebugs.github.io/content/blogs/go-data-parallelism.md)
- **Concepts**: Basic SPMD execution, reduction operations

#### [debug-varying/](debug-varying/)
Shows how to inspect varying values during development using `reduce.From()` and automatic Printf conversion.
- **Concepts**: Type conversion from varying to array, Printf `%v` automatic conversion, debugging SPMD code

#### [goroutine-varying/](goroutine-varying/)
Demonstrates launching goroutines with varying values, with a single goroutine processing all lanes.
- **Concepts**: Goroutine launch with varying parameters, async SPMD processing, result collection

#### [defer-varying/](defer-varying/)
Shows defer statements capturing and processing varying values with proper masking and LIFO execution order.
- **Concepts**: Defer with varying parameters, execution mask preservation, deferred SPMD functions

#### [panic-recover-varying/](panic-recover-varying/)
Demonstrates explicit panic/recover support for varying values and workarounds for public API restrictions in SPMD contexts.
- **Concepts**: Explicit error handling with varying data, public API restrictions, reduce.From() workarounds, any type conversion

#### [map-restrictions/](map-restrictions/)
Shows map usage restrictions with varying types, including prohibited varying keys and allowed workarounds.
- **Concepts**: Map key restrictions, varying value limitations, reduce.From() workarounds, struct alternatives

#### [pointer-varying/](pointer-varying/)
Demonstrates pointer operations with varying types, including pointers to varying values and varying pointers for scatter/gather operations.
- **Concepts**: Pointer to varying data, varying pointers, address operations, scatter/gather patterns, indirect access

#### [type-switch-varying/](type-switch-varying/)
Shows type switches with varying interface{} values, requiring explicit varying type cases and supporting constrained varying types.
- **Concepts**: Type switches with varying interface{}, explicit varying type cases, constrained varying matching, type assertions

#### [non-spmd-varying-return/](non-spmd-varying-return/)
Demonstrates non-SPMD functions (no varying parameters) that can return varying values and their usage restrictions.
- **Concepts**: Non-SPMD functions returning varying, SPMD context restrictions, lanes.Index() limitations, uniform-to-varying conversion

#### [spmd-call-contexts/](spmd-call-contexts/)
Shows SPMD functions being called from different contexts with automatic mask handling and captured mask behavior.
- **Concepts**: SPMD functions callable everywhere, automatic mask handling, captured mask preservation, reduce functions in all contexts

#### [lanes-index-restrictions/](lanes-index-restrictions/)
Demonstrates the specific context requirements for lanes.Index() and shows valid vs invalid usage patterns.
- **Concepts**: lanes.Index() SPMD context requirements, compile-time enforcement, SPMD vs non-SPMD function distinction

#### [varying-universal-constrained/](varying-universal-constrained/)
Shows varying[] syntax for accepting any constrained varying type with lanes.FromConstrained conversion and type switch usage.
- **Concepts**: Universal constrained varying (varying[]), type switch conversion, lanes.FromConstrained, operation restrictions on varying[]

#### [union-type-generics/](union-type-generics/)
Demonstrates union type generics for reduce and lanes functions accepting both constrained and unconstrained varying types with automatic inlining.
- **Concepts**: Union type constraints (VaryingNumeric, VaryingBool, etc.), automatic inlining, type-safe operations, generic programming with varying types

#### [type-casting-varying/](type-casting-varying/)
Shows SPMD type casting rules with valid downcasting and SIMD register capacity constraints that prohibit upcasting.
- **Concepts**: Type casting rules, downcasting (larger→smaller), upcasting prohibition, SIMD register capacity limits, practical casting use cases

#### [mandelbrot/](mandelbrot/)
Complex mathematical computation demonstrating Mandelbrot set generation using SPMD, based on Intel ISPC mandelbrot example.
- **Concepts**: Complex number arithmetic, iterative algorithms, conditional masking, performance comparison, mathematical visualization
- **From**: Intel ISPC mandelbrot example adapted to Go SPMD

#### [odd-even/](odd-even/)
Conditional processing showing how `if` statements work with varying data.
- **From**: [Data Parallelism: simpler solution for Golang?](../bluebugs.github.io/content/blogs/go-data-parallelism.md)
- **Concepts**: Conditional execution with masks, lane-based processing

#### [bit-counting/](bit-counting/)
Nested loop example counting bits in parallel across multiple bytes.
- **From**: [Data Parallelism: simpler solution for Golang?](../bluebugs.github.io/content/blogs/go-data-parallelism.md)
- **Concepts**: Nested loops, uniform vs varying variables

#### [array-counting/](array-counting/)
Demonstrates divergent control flow where different lanes process different amounts of data.
- **From**: [Data Parallelism: simpler solution for Golang?](../bluebugs.github.io/content/blogs/go-data-parallelism.md)
- **Concepts**: Divergent execution, lane independence

### Practical Applications

#### [printf-verbs/](printf-verbs/)
Parallel string scanning for printf format verbs with early termination.
- **From**: [What if? Practical parallel data.](../bluebugs.github.io/content/blogs/practical-vector.md)
- **Concepts**: String processing, `reduce.Any()`, early exit patterns

#### [hex-encode/](hex-encode/)
Parallel hexadecimal encoding demonstrating efficient memory access patterns.
- **From**: [What if? Practical parallel data.](../bluebugs.github.io/content/blogs/practical-vector.md)
- **Concepts**: Data transformation, lane-based indexing

#### [to-upper/](to-upper/)
ASCII uppercase conversion with two-phase parallel processing.
- **From**: [What if? Practical parallel data.](../bluebugs.github.io/content/blogs/practical-vector.md)
- **Concepts**: Multi-phase algorithms, conditional execution

### Advanced Cross-Lane Communication

#### [base64-decoder/](base64-decoder/)
Complex base64 decoding using swizzle, rotation, and reduction operations.
- **From**: [Cross-Lane Communication: When Lanes Need to Talk](../bluebugs.github.io/content/blogs/cross-lane-communication.md)
- **Concepts**: Cross-lane operations, complex data transformations
- **Inspiration**: [Miguel Young de la Sota's SIMD base64 research](https://mcyoung.xyz/2023/11/27/simd-base64/)

#### [ipv4-parser/](ipv4-parser/)
High-performance IPv4 address parsing with parallel validation and field extraction.
- **From**: [Putting It All Together](../bluebugs.github.io/content/blogs/go-spmd-ipv4-parser.md)
- **Concepts**: Real-world application, error handling, bit manipulation
- **Inspiration**: [Wojciech Muła's SIMD IPv4 parsing](http://0x80.pl/notesen/2023-04-09-faster-parse-ipv4.html)

## Key SPMD Concepts Demonstrated

### Language Extensions
- **`go for`**: SPMD loop construct for data parallelism
- **`lanes.Varying[T]`**: Types that hold multiple values (one per lane)
- **Uniform values**: Regular Go variables, same across all lanes (no annotation needed)
- **`lanes.Count[T](v)`**: Number of SIMD lanes for a type
- **`lanes.Index()`**: Current lane index [0, 1, 2, 3, ...]

### Cross-Lane Operations
- **`lanes.Broadcast(value, lane)`**: Broadcast from one lane to all
- **`lanes.Rotate(value, offset)`**: Rotate values across lanes
- **`lanes.Swizzle(value, indices)`**: Arbitrary permutation
- **`lanes.ShiftLeft/Right()`**: Bit shifting operations

### Reduction Operations
- **`reduce.Add()`**: Sum across all lanes
- **`reduce.Any()`**: Logical OR across lanes
- **`reduce.All()`**: Logical AND across lanes
- **`reduce.Max/Min()`**: Maximum/minimum across lanes
- **`reduce.FindFirstSet()`**: Find first true lane
- **`reduce.Mask()`**: Convert boolean array to bitmask

## Implementation Notes

These examples represent **experimental Go language extensions** that require the `GOEXPERIMENT=spmd` flag. They will not compile with current Go compilers without the SPMD experimental feature enabled.

### Building Examples (TinyGo PoC)

```bash
# Compile SPMD examples to WebAssembly
GOEXPERIMENT=spmd tinygo build -target=wasi -o simple-sum.wasm simple-sum/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -o odd-even.wasm odd-even/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -o bit-counting.wasm bit-counting/main.go

# Or set environment variable  
export GOEXPERIMENT=spmd
tinygo build -target=wasi -o example.wasm simple-sum/main.go

# Execute with wasmer-go runtime
go run wasmer-runner.go simple-sum.wasm
```

### Verifying SIMD Generation

```bash
# Check that WASM contains SIMD instructions
wasm2wat simple-sum.wasm | grep -E "(v128|i32x4|f32x4)"

# Expected output should include SIMD128 instructions like:
# i32x4.splat
# i32x4.add  
# v128.load
# v128.store
```

### Example Test Headers

SPMD examples for TinyGo PoC should include:

```go
// run -goexperiment spmd -target=wasi
// errorcheck -goexperiment spmd  (for illegal examples)
```

### PoC Limitations

**What Works in TinyGo PoC:**
- ✅ Basic SPMD syntax (`lanes.Varying[T]`, `go for`)
- ✅ Core `lanes` functions (`Count()`, `Index()`)
- ✅ Basic `reduce` operations (`Add()`, `Any()`, `All()`)
- ✅ WASM SIMD128 instruction generation
- ✅ wasmer-go runtime execution

**What's Limited/Missing:**
- ❌ Complex cross-lane operations (`Swizzle`, `Rotate`, `Broadcast`)
- ❌ Advanced examples (base64-decoder, ipv4-parser) 
- ❌ Full standard library integration
- ❌ Performance benchmarking tools
- ❌ Comprehensive error messages

### Features Demonstrated

1. **Readability**: SPMD concepts expressed in familiar Go syntax
2. **Performance**: Potential for significant speedups through parallelism
3. **Portability**: Same code works across different SIMD widths
4. **Maintainability**: No platform-specific assembly required
5. **Experimental Gating**: Proper feature flag integration

## References

- **ISPC**: [Intel SPMD Program Compiler](https://ispc.github.io/ispc.html) - Reference implementation
- **Mojo**: [Modular Mojo language](https://docs.modular.com/mojo/) - Similar SPMD concepts
- **Blog Series**: Complete analysis of SPMD concepts for Go
  - [Data Parallelism: simpler solution for Golang?](../bluebugs.github.io/content/blogs/go-data-parallelism.md)
  - [What if? Practical parallel data.](../bluebugs.github.io/content/blogs/practical-vector.md)
  - [Cross-Lane Communication: When Lanes Need to Talk](../bluebugs.github.io/content/blogs/cross-lane-communication.md)
  - [Putting It All Together](../bluebugs.github.io/content/blogs/go-spmd-ipv4-parser.md)

Each example includes detailed comments explaining the SPMD concepts and their real-world applications.