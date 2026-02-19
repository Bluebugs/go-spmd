# SPMD Go - Frequently Asked Questions

This FAQ addresses common questions about SPMD (Single Program Multiple Data) extensions for Go, based on the comprehensive specification and examples in this repository.

## Table of Contents

1. [General Concepts](#general-concepts)
2. [Language Syntax](#language-syntax)
3. [Type System](#type-system)
4. [Performance and SIMD](#performance-and-simd)
5. [Browser and WebAssembly](#browser-and-webassembly)
6. [Implementation Details](#implementation-details)
7. [Compatibility and Migration](#compatibility-and-migration)
8. [Troubleshooting](#troubleshooting)

## General Concepts

### Q: What is SPMD and why do we need it in Go?

**A:** SPMD (Single Program Multiple Data) is a parallel programming model where the same program executes on multiple data elements simultaneously. Think of it as "data parallelism" - processing arrays of data in parallel rather than one element at a time.

Go currently lacks explicit data parallelism constructs. While goroutines excel at task parallelism (different tasks running concurrently), SPMD excels at data parallelism (same operation on multiple data points). This is particularly valuable for:

- **High-performance computing**: Mathematical operations on large datasets
- **Image/video processing**: Pixel-level operations
- **Cryptography**: Parallel hash calculations
- **Network processing**: Packet analysis and transformation
- **Parsing**: All strings manipulation can benefit from it

### Q: How does SPMD differ from goroutines?

**A:**

- **Goroutines**: Task parallelism - different tasks running concurrently
- **SPMD**: Data parallelism - same task on multiple data elements simultaneously

```go
// Goroutines (task parallelism)
go processImage(img1)
go processImage(img2)
go processImage(img3)

// SPMD (data parallelism)
go for i := range len(pixels) {
    pixels[i] = enhance(pixels[i])  // Same operation on all pixels
}
```

### Q: Why not just use existing vectorization libraries?

**A:** Existing solutions have limitations:

- **Manual vectorization**: Error-prone, platform-specific assembly
- **C libraries**: FFI overhead, memory management complexity
- **Compiler auto-vectorization**: Limited, unpredictable, not portable

SPMD Go provides:

- **Explicit control**: Developer controls what gets vectorized
- **Portability**: Same code works across different SIMD widths
- **Type safety**: Compile-time validation of vector operations
- **Integration**: Native Go syntax and tooling (at some point)

## Language Syntax

### Q: What's the difference between `uniform` and `varying`?

**A:**

- **`uniform`**: Single value shared across all SIMD lanes (scalar), today default in Golang
- **`varying`**: Different value in each SIMD lane (vector)

```go
var count int = 0                    // Same value in all lanes: [0, 0, 0, 0]
var indices lanes.Varying[int]       // Different per lane: [0, 1, 2, 3]

go for i := range 4 {
    indices = i                  // i is automatically varying
    count += 1                   // count is uniform, same increment everywhere
}
```

### Q: Why `go for` instead of a new keyword?

**A:** Design principles:

1. **Familiarity**: Reuses existing `go` keyword (like `go func`)
2. **Distinction**: Clearly different from regular `for` loops
3. **Intuition**: "go for" suggests "go do this for multiple data"
4. **Consistency**: Follows Go's existing parallelism patterns

### Q: Can I nest `go for` loops?

**A:** **No**, nested `go for` loops are prohibited for now:

```go
// ILLEGAL - compile error
go for i := range 4 {
    go for j := range 4 {  // ERROR: nested go for not allowed (for now)
        // ...
    }
}

// LEGAL - mix regular for with go for
go for i := range 4 {
    for j := range 4 {     // OK: regular for loop inside go for
        // ...
    }
}
```

**Rationale**: Nested SPMD contexts create complex mask interactions and unclear semantics. This restriction may be relaxed in future versions once proper semantics are defined.

### Q: How do `go for` loops work with arrays of varying values?

**A:** The variable types depend on what you're iterating over:

```go
// Array of uniform values (regular integers)
uniformArray := []int{1, 2, 3}
go for i, v := range uniformArray {
    // i is VARYING (different index per lane)
    // v is UNIFORM (same value across all lanes)
}

// Array of varying values
varyingArray := []lanes.Varying[int]{values1, values2, values3}
go for idx, vData := range varyingArray {
    // idx is UNIFORM (processing one varying at a time)
    // vData is VARYING (each array element is a complete varying value)
}
```

**Key insight**: When ranging over `[]varying T`, you process one `varying T` at a time sequentially, but each `varying T` contains multiple lane values that can be processed in parallel.

### Q: Can I use control flow with varying values outside `go for` loops?

**A:** **No**, this is prohibited by design for code maintainability:

```go
func regularFunction() {
    var data lanes.Varying[int] = lanes.Varying[int](42)

    // All COMPILE ERRORS outside go for:
    if data > lanes.Varying[int](30) { ... }        // ERROR: varying condition outside SPMD context
    for data != lanes.Varying[int](0) { ... }       // ERROR: varying loop condition outside SPMD context
    switch data { ... }                              // ERROR: varying switch outside SPMD context
}

// Must use explicit SPMD context:
func spmdFunction() {
    go for i := range 1 {
        var data lanes.Varying[int] = lanes.Varying[int](42)

        if data > lanes.Varying[int](30) { ... }    // OK: clear SIMD intent
    }
}
```

**Rationale**: This restriction is **intentionally non-technical** - it's designed to:

- **Improve readability**: Makes SIMD code explicit and identifiable
- **Clarify intent**: `go for` makes parallel processing intent obvious
- **Organize code**: Encourages grouping SIMD operations in clear blocks
- **Aid maintenance**: SIMD code is easy to find, review, and optimize

### Q: What about `break` and `continue` in `go for` loops?

**A:**

- **`continue`**: ✅ Allowed - masks out current lanes for remaining iteration
- **`break`**: ❌ Prohibited - would create complex inter-lane dependencies

```go
go for i := range 8 {
    if data[i] == 0 {
        continue  // OK: skip processing for lanes with zero data
    }
    // break      // ERROR: break not allowed in go for loops
    process(data[i])
}
```

## Type System

### Q: How do I convert between `uniform` and `varying`?

**A:**

- **Uniform → Varying**: Automatic broadcast
- **Varying → Uniform**: Use `reduce` functions

```go
var u int = 42
var v lanes.Varying[int] = u           // OK: broadcasts 42 to all lanes

var v2 lanes.Varying[int] = lanes.Varying[int](10)
var u2 int = v2                        // ERROR: cannot assign varying to uniform
var u3 int = reduce.Add(v2)            // OK: sum all lanes to get uniform
```

### Q: What are constrained varying types?

**A:** Constrained varying (`varying[n]`) specifies exact lane count, enabling:

- **Hardware independence**: Algorithm works regardless of SIMD width
- **Explicit control**: Developer chooses grouping size
- **Type safety**: Prevents mismatched operations

```go
var v4 lanes.Varying[int, 4]    // Always 4 lanes, regardless of hardware
var v8 lanes.Varying[int, 8]    // Always 8 lanes

// These would be type errors:
// result := v4 + v8     // ERROR: mismatched constraints
```

### Q: Can I cast between different `varying` types?

**A:**

- **Downcasting (larger→smaller)**: ✅ Allowed
- **Upcasting (smaller→larger)**: ❌ Prohibited

```go
var large lanes.Varying[uint32] = lanes.Varying[uint32](0x12345678)
var small lanes.Varying[uint16] = lanes.Varying[uint16](large)  // OK: truncates

var small2 lanes.Varying[uint16] = lanes.Varying[uint16](0x1234)
var large2 lanes.Varying[uint32] = lanes.Varying[uint32](small2)  // ERROR: would exceed SIMD register capacity
```

**Reason**: Upcasting would require more bits than available in SIMD registers (e.g., 4×64-bit = 256 bits > 128-bit WASM SIMD).

### Q: What is `varying[]` and when do I use it?

**A:** `varying[]` (universal constrained varying) accepts any constrained varying type:

```go
func processAnySize(data lanes.Varying[int]) {
    // Can accept lanes.Varying[int, 4], lanes.Varying[int, 8], etc.

    // Must use type switch to operate on data
    switch v := data.(type) {
    case lanes.Varying[int, 4]:
        return v * 2
    case lanes.Varying[int, 8]:
        return v + 100
    default:
        // Convert to unconstrained for generic processing
        values, masks := lanes.FromConstrained(data)
        // Process values...
    }
}
```

### Q: How do I process results from `lanes.FromConstrained`?

**A:** Use `go for` over the returned array of varying values:

```go
func processUniversalConstrained(data lanes.Varying[int]) {
    values, masks := lanes.FromConstrained(data)

    // Natural processing pattern: go for over array of varying values
    go for idx, varyingGroup := range values {
        // idx is uniform (processing one group at a time)
        // varyingGroup is varying (contains multiple lane values)
        mask := masks[idx]  // Get corresponding mask (varying for this iteration)

        fmt.Printf("Processing group %d: %v\n", idx, varyingGroup)

        // Process this varying group with its mask
        if reduce.Any(mask) {  // Check if any lanes are active
            if mask {
                processed := varyingGroup * lanes.Varying[int](2)
                result := reduce.Add(processed)
                fmt.Printf("Group %d result: %d\n", idx, result)
            }
        }
    }
}
```

This pattern leverages the fact that `go for` over `[]varying T` processes one `varying T` at a time, which perfectly matches the structure returned by `lanes.FromConstrained`.

## Performance and SIMD

### Q: What performance improvements can I expect?

**A:** Expected speedups vary by algorithm and hardware. Write benchmark to verify your result.

**Factors affecting performance**:

- **Data layout**: Contiguous memory access is crucial
- **Algorithm complexity**: Simple operations vectorize better
- **SIMD width**: Wider SIMD (AVX-512) provides more speedup than narrow (SSE)
- **Memory bandwidth**: Some algorithms become memory-bound
- **Uniform**: Use uniform as often as possible

### Q: Which SIMD instruction sets does SPMD Go target?

**A:** The PoC targets **WebAssembly SIMD128** initially, but the architecture supports:

- **WASM SIMD128**: 128-bit vectors (4×32-bit or 2×64-bit)
- **Future targets**: x86 SSE/AVX, ARM NEON, RISC-V Vector extension
- **Portability**: Same code adapts to different SIMD widths automatically

### Q: How does the compiler choose SIMD width?

**A:**

- **Unconstrained varying**: Compiler chooses optimal width for target (e.g., 4 lanes with int for WASM128)
- **Constrained varying**: Developer specifies exact width (`varying[8]`)
- **Runtime adaptation**: Hardware with different SIMD widths handle constraints via unrolling/masking

### Q: What happens on CPUs without SIMD support?

**A:** The dual compilation approach provides graceful fallback:

```bash
# Compile SIMD version
GOEXPERIMENT=spmd tinygo build -simd=true -target=wasi program.go

# Compile scalar fallback
GOEXPERIMENT=spmd tinygo build -simd=false -target=wasi program.go
```

The scalar version generates regular loops with identical functionality but no SIMD acceleration.

## Browser and WebAssembly

### Q: Which browsers support WebAssembly SIMD?

**A:** Current support status:

| Browser | SIMD Support | Status |
|---------|-------------|---------|
| Chrome 91+ | ✅ Full | Stable |
| Firefox 89+ | ✅ Full | Stable |
| Edge 91+ | ✅ Full | Stable |
| Safari | ❌ Limited | Experimental |
| Mobile browsers | ⚠️ Varies | Platform-dependent |

### Q: How does browser SIMD detection work?

**A:** The provided JavaScript automatically detects and loads optimal version:

```javascript
// Detects SIMD support and loads appropriate WASM
const wasmModule = await loadOptimalWasm('my-algorithm');
// Loads either my-algorithm-simd.wasm or my-algorithm-scalar.wasm
```

### Q: Can I deploy SPMD Go to production web apps?

**A:** For production deployment: **NOPE**, this is a Proof Of Concept!

## Implementation Details

### Q: Do I need to modify existing Go code?

**A:** Existing Go code remains 100% compatible. SPMD is purely additive:

```go
// Existing code works unchanged
func oldFunction(data []int) int {
    sum := 0
    for _, v := range data {
        sum += v
    }
    return sum
}

// New SPMD version provides additional performance
func spmdFunction(data []int) int {
    var sum lanes.Varying[int]
    go for i := range len(data) {
        sum += lanes.Varying[int](data[i])
    }
    return reduce.Add(sum)
}
```

### Q: When should I use SPMD vs regular Go?

**A:** Use SPMD when:

- ✅ **Data parallelism**: Same operation on multiple data elements
- ✅ **Numerical computing**: Mathematical operations, algorithms
- ✅ **Bulk processing**: Image/video/audio processing
- ✅ **Performance critical**: Hot code paths where speed matters
- ✅ **Experiment**: You want to know how it feels, what it could do

Stick with regular Go for:

- ❌ **Production**: Really don't ever use this PoC for production!
- ❌ **Control-heavy code**: Complex branching, state machines
- ❌ **I/O bound operations**: Network, file system access
- ❌ **Small datasets**: SIMD overhead exceeds benefits
- ❌ **Goroutine coordination**: Use channels and goroutines instead

### Q: How do I debug SPMD code?

**A:** Debugging strategies:

1. **Use `reduce.From()`**: Convert varying to arrays for inspection
2. **Printf integration**: `fmt.Printf("%v", varying_value)` automatically converts
3. **Scalar fallback**: Debug with `-simd=false` to isolate vectorization issues
4. **Unit tests**: Test both SIMD and scalar versions for identical output

```go
var data lanes.Varying[int] = calculateSomething()
fmt.Printf("Debug varying data: %v\n", data)  // Automatically shows all lanes
```

### Q: What's the difference between SPMD functions and regular functions?

**A:**

- **SPMD functions**: Accept varying parameters, execute with implicit mask
- **Regular functions**: Only uniform parameters, no mask context

```go
// Regular function
func regular(x int) int {
    return x * 2
}

// SPMD function (varying parameter makes it SPMD)
func spmd(x lanes.Varying[int]) lanes.Varying[int] {
    return x * 2  // Executes per-lane with mask
}

// Mixed function (both uniform and varying parameters)
func mixed(config Settings, data lanes.Varying[int]) lanes.Varying[int] {
    return data * lanes.Varying[int](config.multiplier)
}
```

### Q: Can SPMD functions call other SPMD functions?

**A:** Yes, SPMD functions can call each other and propagate masks automatically:

```go
func helper(v lanes.Varying[int]) lanes.Varying[int] {
    return v * 2
}

func main() {
    go for _, value := range data {
        if value > 0 {  // Creates a mask
            result := helper(value)  // Mask propagates to helper
            process(result)
        }
    }
}
```

## Compatibility and Migration

### Q: How do I migrate existing performance-critical code to SPMD?

**A:** Migration strategy:

1. **Identify candidates**: Look for loops over arrays with simple operations
2. **Create SPMD version**: Keep original function, add SPMD variant
3. **Benchmark both**: Measure performance improvement
4. **Gradual adoption**: Replace hot paths incrementally

```go
// Step 1: Keep existing version
func processArrayOld(data []float32) []float32 {
    result := make([]float32, len(data))
    for i, v := range data {
        result[i] = math.Sqrt(v * 2.5)
    }
    return result
}

// Step 2: Add SPMD version
func processArraySPMD(data []float32) []float32 {
    result := make([]float32, len(data))
    go for i := range len(data) {
        result[i] = math.Sqrt(lanes.Varying[float32](data[i]) * 2.5)
    }
    return result
}

// Step 3: Choose based on performance testing
var processArray = processArraySPMD  // Use faster version
```

### Q: Will my dependencies work with SPMD Go?

**A:** Yes, SPMD Go is fully compatible with existing Go ecosystem:

- ✅ **Import existing packages**: All standard library and third-party packages work
- ✅ **Use with everything**: Web server, databases, etc. work normally
- ✅ **Mixed codebase**: SPMD and regular Go code coexist seamlessly

### Q: What about Go modules and versioning?

**A:** SPMD support is gated behind `GOEXPERIMENT=spmd`:

- **Without flag**: Code compiles as regular Go (SPMD syntax rejected)
- **With flag**: SPMD syntax enabled, full functionality available
- **Dependencies**: Packages using SPMD should document the requirement

## Troubleshooting

### Q: Why do I get "uniform/varying require GOEXPERIMENT=spmd"?

**A:** Enable the experimental flag:

```bash
# Set environment variable
export GOEXPERIMENT=spmd

# Or use inline
GOEXPERIMENT=spmd tinygo build -target=wasi program.go
```

### Q: My WASM binary doesn't contain SIMD instructions - why?

**A:** Check these common issues:

1. **SIMD flag**: Ensure `-simd=true` (default) is set
2. **Target platform**: Verify WASM SIMD128 is supported
3. **Code structure**: Simple operations vectorize better than complex control flow
4. **Build optimization**: Use release builds for optimal SIMD generation

```bash
# Verify SIMD instructions in output
wasm2wat program.wasm | grep -E "(v128|i32x4|f32x4)"
```

### Q: Performance is worse with SPMD - what's wrong?

**A:** Common performance pitfalls:

1. **Memory layout**: Ensure data is contiguous and aligned
2. **Small datasets**: SIMD overhead may exceed benefits for small arrays
3. **Complex control flow**: Heavy branching reduces SIMD efficiency
4. **Memory bandwidth**: Some algorithms become memory-bound rather than compute-bound
5. **Compiler missed optimization**: As this is a PoC, it is possible that some optimization are missing, reporting bug can help

**Solution**: Profile both versions and ensure the algorithm is SIMD-friendly.

### Q: I get type errors mixing varying and uniform - how do I fix this?

**A:** Common patterns and solutions:

```go
// Problem: Direct assignment
var u int = 42
var v lanes.Varying[int] = 100
u = v  // ERROR: cannot assign varying to uniform

// Solution: Use reduce functions
u = reduce.Add(v)        // Sum all lanes
u = reduce.Max(v)        // Maximum across lanes
u = reduce.Any(v > 50)   // Check if any lane satisfies condition

// Problem: Mixed arithmetic
var result = u + v  // Works: uniform broadcasts to varying

// Problem: Function parameter mismatch
func takesUniform(x int) { ... }
takesUniform(v)  // ERROR: cannot pass varying to uniform parameter

// Solution: Extract or reduce first
takesUniform(reduce.First(v))  // Use first lane value
```

### Q: Error: "go for loops cannot be nested" - how do I work around this?

**A:** Use regular `for` inside `go for` (nesting is prohibited for now):

```go
// Instead of nested go for (illegal for now)
go for i := range height {
    go for j := range width {  // ERROR: nesting prohibited for now
        process(matrix[i][j])
    }
}

// Use regular for inside go for (legal)
go for i := range height {
    for j := range width {     // OK
        process(matrix[i][j])
    }
}

// Or process in chunks
go for chunk := range (height * width) {
    i := chunk / width
    j := chunk % width
    process(matrix[i][j])
}
```

---

## Additional Resources

- **Specifications**: [SPECIFICATIONS.md](SPECIFICATIONS.md) - Complete language specification
- **Examples**: [examples/](examples/) - Comprehensive example collection
- **Implementation**: [GOEXPERIMENT-IMPLEMENTATION.md](GOEXPERIMENT-IMPLEMENTATION.md) - Implementation roadmap
- **Blog Posts**: [bluebugs.github.io](https://bluebugs.github.io) - In-depth explanations and tutorials

For specific questions not covered here, please check the [GitHub Issues](https://github.com/Bluebugs/go-spmd/issues).
