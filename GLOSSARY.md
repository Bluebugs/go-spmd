# SPMD Go - Glossary

**A comprehensive guide to Single Program Multiple Data terminology for Go developers**

## Core SPMD Concepts

### **SPMD (Single Program Multiple Data)**

A parallel programming model where multiple processing units execute the same program simultaneously on different data elements. Unlike traditional parallelism that focuses on running different tasks concurrently, SPMD focuses on running the same task on multiple data points in parallel.

*Example*: Processing an array where each SIMD lane handles different array elements using identical code.

### **Lane**

An individual execution unit within a SIMD operation. Each lane processes one data element independently but follows the same program flow as other lanes.

*Go Context*: In `go for` loops, each lane processes different array elements simultaneously.

```go
go for i := range data {
    // Each lane processes data[i] where i differs per lane
}
```

### **Execution Mask**

A boolean vector that tracks which lanes are active during execution. Used to handle divergent control flow where different lanes take different execution paths.

*Example*: When some lanes meet an `if` condition and others don't, the mask determines which lanes execute each branch.

### **Lane Count**

The number of parallel processing units available on the target hardware. This varies by architecture (e.g., 4 for WASM SIMD128, 8 for AVX2).

*Go Function*: `lanes.Count(type)` returns the lane count for a specific type.

## Type Qualifiers

### **Uniform**

A type qualifier indicating that a variable has the same value across all SIMD lanes. Equivalent to regular Go variables.

```go
var x uniform int = 42    // Same value (42) in all lanes
var y int = 42           // Implicitly uniform - same as above
```

### **Varying**

A type qualifier indicating that a variable may have different values across SIMD lanes. Implemented as SIMD register with one element per lane.

```go
var data varying int     // Different value per lane: [1, 2, 3, 4]
```

### **Constrained Varying**

A varying type with a constraint requiring the lane count to be a multiple of a specified number. Syntax: `varying[n]`.

```go
var data varying[4] byte    // Requires lane count to be multiple of 4
var mask varying[8] bool    // Requires lane count to be multiple of 8
```

**Important**: `varying[4]` means "multiples of 4 lanes", not exactly 4 lanes.

### **Universal Constrained Varying**

A special type `varying[]` that accepts any constrained varying type but has restricted operations.

```go
func process(data varying[] int) {
    // Accepts varying[4] int, varying[8] int, etc.
    // Limited operations - mainly type switching
}
```

## Go Language Extensions

### **go for**

An SPMD loop construct that executes iterations in parallel across SIMD lanes. Extension of Go's `go` keyword.

```go
go for i := range data {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [0, 1, 2, 3] for 4-lane SIMD
}
```

### **Range Grouping**

Optional syntax `range[n]` in `go for` loops to process data in groups where lane count must be a multiple of `n`. The index and value are of a `varying[n]` in that case.

```go
go for i := range[4] data {
    // Process 4 elements at a time per iteration
    // Essential for algorithms like base64 (4:3 transformation)
}
```

### **SPMD Function**

A function that becomes SPMD-aware when called with varying parameters or from SPMD contexts. Receives implicit execution mask.

```go
func process(data varying int) varying int {  // SPMD function
    return data * 2  // Operates per-lane with mask
}
```

### **Non-SPMD Function**

A regular Go function with no varying parameters. Can return varying values but cannot use `lanes.Index()`.

```go
func createData() varying int {  // Non-SPMD function
    return varying(42)  // Returns uniform value broadcast to all lanes
}
```

## Standard Library Packages

### **lanes Package**

Provides cross-lane operations and lane information functions.

**Key Functions**:

- `lanes.Count(type)` - Number of SIMD lanes for a type
- `lanes.Index()` - Current lane index (requires SPMD context)
- `lanes.Broadcast(value, lane)` - Broadcast from one lane to all
- `lanes.Rotate(value, offset)` - Rotate values across lanes
- `lanes.Swizzle(value, indices)` - Arbitrary permutation

### **reduce Package**

Provides reduction operations that combine varying values into uniform results.

**Key Functions**:

- `reduce.Add(values)` - Sum across all lanes
- `reduce.Any(conditions)` - True if any lane is true
- `reduce.All(conditions)` - True if all lanes are true
- `reduce.From(varying)` - Convert varying to array

## Control Flow Concepts

### **Divergent Control Flow**

When different lanes take different execution paths (e.g., different branches of an `if` statement). Handled automatically through execution masks.

```go
go for i := range data {
    if data[i] > threshold {  // Creates divergent control flow
        // Only some lanes execute this
    }
}
```

### **Masking**

The technique used to handle divergent control flow by tracking which lanes should execute specific code blocks.

### **Early Termination**

Optimization where loops exit when no lanes remain active, determined by reduction operations like `reduce.Any()`.

```go
go for i := range data {
    if reduce.Any(data[i] > limit) {
        break  // ERROR: break not allowed in go for
    }
    // Use continue instead for per-lane termination
}
```

## Cross-Lane Communication

### **Broadcast**

Operation that takes a value from one specific lane and copies it to all other lanes.

```go
// Get value from lane 0 and copy to all lanes
result := lanes.Broadcast(data, 0)
```

### **Rotation**

Operation that shifts values between adjacent lanes in a circular fashion.

```go
// Each lane gets value from previous lane
rotated := lanes.Rotate(data, 1)
```

### **Swizzle/Shuffle**

Operation that allows arbitrary reordering of values across lanes based on an index pattern.

```go
// Each lane accesses data[indices[lane]]
permuted := lanes.Swizzle(data, indices)
```

### **Reduction**

Operation that combines values from all lanes into a single uniform result.

```go
total := reduce.Add(data)        // Sum all lane values
any := reduce.Any(conditions)    // Logical OR of all lanes
```

## Type System Rules

### **Assignment Rules**

- **Uniform → Uniform**: Direct assignment ✓
- **Varying → Varying**: Direct assignment ✓
- **Uniform → Varying**: Implicit broadcast ✓
- **Varying → Uniform**: Prohibited ✗

### **Type Casting Rules**

- **Downcasting (larger → smaller)**: Allowed ✓
- **Upcasting (smaller → larger)**: Prohibited ✗ (register capacity)

### **SPMD Context**

Execution environment where lane information is available. Required for `lanes.Index()`. Created by:

1. `go for` loops
2. Functions with varying parameters

## Implementation Concepts

### **GOEXPERIMENT Flag**

Environment variable that enables SPMD language extensions: `GOEXPERIMENT=spmd`

### **Vector Registers**

Hardware SIMD registers that store multiple values. SPMD varying types map to these registers.

### **TinyGo Backend**

The LLVM-based Go compiler used to generate WebAssembly SIMD instructions from SPMD Go code.

### **SSA (Static Single Assignment)**

Intermediate representation used by Go compiler. SPMD constructs generate special SSA opcodes that TinyGo converts to SIMD instructions.

## Architecture-Specific Terms

### **WASM SIMD128**

WebAssembly's 128-bit SIMD instruction set. Primary target for the proof-of-concept implementation.

### **Lane Width**

Number of elements that fit in a SIMD register for a given data type:

- `int32`: 4 lanes in 128-bit SIMD
- `int16`: 8 lanes in 128-bit SIMD  
- `int8`: 16 lanes in 128-bit SIMD

### **Register Capacity**

The bit width limitation of SIMD registers that constrains type casting operations.

## Error Categories

### **Assignment Errors**

- `cannot assign varying to uniform`
- `cannot pass varying to uniform parameter`

### **Control Flow Errors**

- `break statement not allowed in SPMD for loop`
- `go for loops cannot be nested` (for now)

### **Context Errors**

- `lanes.Index() requires SPMD context`
- `go for loops not allowed in SPMD functions`

### **Type Errors**

- `varying parameters not allowed in public functions`
- `varying map keys not allowed`

## Performance Concepts

### **Vectorization**

The process of converting scalar operations to vector (SIMD) operations for parallel execution.

### **Memory Access Patterns**

Sequential memory access is critical for SIMD performance. Random access patterns can negate SIMD benefits.

### **Instruction Level Parallelism**

The ability to execute multiple operations simultaneously at the hardware level.

## Comparison with Other Technologies

### **vs. Goroutines**

- **Goroutines**: Concurrent execution of different tasks
- **SPMD**: Parallel execution of same task on different data

### **vs. Traditional SIMD Libraries**

- **Libraries**: Manual vector programming with explicit instructions
- **SPMD Go**: High-level language constructs that compile to SIMD

### **vs. ISPC**

- **ISPC**: C-like language specifically for SPMD programming
- **SPMD Go**: SPMD extensions to existing Go language

### **vs. GPU Programming**

- **CUDA/OpenCL**: Explicit GPU programming with kernels
- **SPMD Go**: CPU SIMD programming with familiar Go syntax

## Advanced Concepts

### **Mask Propagation**

How execution masks are automatically passed through function calls and control structures.

### **Constrained Compilation**

Compiler techniques to handle constrained varying types across different hardware lane counts.

### **Cross-Lane Dependencies**

Algorithms that require coordination between lanes, like the base64 decoder example.

### **Register Pressure**

When algorithms require more SIMD registers than available, forcing spills to memory.

---

## Quick Lookup

**Essential Functions**:

- `lanes.Count()` - Get lane count
- `lanes.Index()` - Get current lane (SPMD context only)
- `reduce.Add()` - Sum across lanes
- `reduce.Any()` - Any lane true?
- `reduce.All()` - All lanes true?

**Essential Types**:

- `uniform T` - Same value all lanes
- `varying T` - Different value per lane
- `varying[n] T` - Constrained to multiples of n lanes

**Essential Syntax**:

- `go for` - SPMD loop
- `range[n]` - Grouped processing

**Essential Rules**:

- No `break` in `go for`
- No varying → uniform assignment
- `lanes.Index()` needs SPMD context
- Public functions can't have varying parameters
