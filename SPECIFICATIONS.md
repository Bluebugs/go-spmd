# Go Language Specification: SPMD Extensions

**Version**: Go 1.22+ (proposed)  
**Status**: Draft Specification - Experimental Feature  
**Authors**: Cedric Bail  
**GOEXPERIMENT**: `spmd`  
**Proof of Concept**: TinyGo + WebAssembly SIMD128  

## Abstract

This specification defines extensions to the Go programming language to support Single Program Multiple Data (SPMD) parallelism. These extensions enable explicit data parallelism through type qualifiers, specialized control flow constructs, and built-in functions for cross-lane operations, while maintaining full backward compatibility with existing Go code.

## Table of Contents

1. [Introduction](#introduction)
2. [Lexical Elements](#lexical-elements)
3. [Types](#types)
4. [Variables](#variables)
5. [Statements](#statements)
6. [Built-in Functions](#built-in-functions)
7. [Standard Library Extensions](#standard-library-extensions)
8. [Type System Rules](#type-system-rules)
9. [Function Semantics](#function-semantics)
10. [Execution Model](#execution-model)
11. [Backward Compatibility](#backward-compatibility)
12. [Implementation Notes](#implementation-notes)

## Introduction

SPMD (Single Program Multiple Data) is a parallel programming model where multiple processing elements execute the same program simultaneously on different data elements. This specification extends Go to support SPMD programming through:

- **Type qualifiers** (`uniform`, `varying`) that distinguish between scalar and vector data
- **SPMD loops** (`go for`) that execute iterations in parallel across SIMD lanes
- **Built-in functions** for cross-lane communication and reduction operations
- **Execution masks** that handle divergent control flow

The design draws inspiration from Intel ISPC and Modular Mojo, adapted to Go's philosophy of simplicity and readability.

## Lexical Elements

### Keywords

Two new keywords are added to the Go language:

```
uniform    varying
```

These keywords act as **type qualifiers** and are only recognized as keywords in specific syntactic contexts (see [Backward Compatibility](#backward-compatibility)).

### Operators

No new operators are introduced. Existing Go operators work with both uniform and varying types according to the rules defined in [Type System Rules](#type-system-rules).

## Types

### Type Qualifiers

Two new type qualifiers modify the semantics of existing Go types:

#### Uniform Types

```go
var x int              // Scalar integer, same value across all lanes
var f func()           // Function pointer, same across all lanes
var s []byte           // Slice header, same across all lanes
```

**Uniform types** represent values that are identical across all SIMD lanes. They correspond to traditional Go types and consume the same memory as their unqualified equivalents. `uniform` is entirely optional and match exactly match unqualified type. They exist for better readability.

#### Varying Types

```go
var y lanes.Varying[int]      // Vector integer, potentially different per lane
var a lanes.Varying[[4]byte]  // Vector array, different per lane
var p lanes.Varying[*Data]    // Vector of pointers, different per lane
```

**Varying types** represent values that may differ across SIMD lanes. They are implemented as vectors with one element per lane. The number of lanes is determined by the target architecture and can be queried using `lanes.Count()`.

**Constrained Varying Types** specify lane count requirements using the syntax `lanes.Varying[T, n]`:

```go
var data lanes.Varying[byte, 4]    // Lane count must be multiple of 4
var mask lanes.Varying[bool, 8]    // Lane count must be multiple of 8
```

**Universal Constrained Varying** accepts any constrained varying:

```go
func processAnyConstrained(data lanes.Varying[byte]) {
    // Accepts lanes.Varying[byte, 4], lanes.Varying[byte, 8], lanes.Varying[byte, 16], etc.
    // Does NOT accept unconstrained lanes.Varying[byte]
}
```

**Important**: The `[n]` constraint means "**multiples of n**", not "exactly n lanes". This is called a **constrained varying** type in short.

**Semantics**:

- `lanes.Varying[int, 4]` requires the SIMD width to be a multiple of 4 (e.g., 4, 8, 12, 16 lanes)
- `lanes.Varying[bool, 8]` requires the SIMD width to be a multiple of 8 (e.g., 8, 16, 24, 32 lanes)
- This enables algorithms that process data in specific group sizes (like base64's 4:3 transformation)

**Hardware Independence**:
Constrained varying types are **independent of hardware capabilities**. The compiler handles implementation through:

- **Loop unrolling**: For constraints larger than hardware SIMD width
- **Masking**: For constraints that don't evenly divide hardware width
- **Multiple iterations**: Processing data in chunks that fit the constraint

**Examples**:

```go
// These are valid regardless of hardware SIMD width:
var data1 lanes.Varying[int, 4]     // ✓ Always valid
var data2 lanes.Varying[int, 8]     // ✓ Always valid
var data3 lanes.Varying[int, 16]    // ✓ Always valid
var data4 lanes.Varying[int, 32]    // ✓ Valid (compiler handles with unrolling/masking)

// On WASM SIMD128 (4 lanes), lanes.Varying[int, 8] is implemented as:
// - 2 SIMD registers or 2 unrolled loop iterations
// On AVX2 (8 lanes), lanes.Varying[int, 16] is implemented as:
// - 2 SIMD registers or 2 unrolled loop iterations
// On scalar hardware, lanes.Varying[int, 4] is implemented as:
// - 4 scalar operations or an unrolled loop with 4 iterations
```

**Practical Limits**:
To ensure reasonable compilation times and memory usage:

- **Maximum constraint**: 512 bits total
- `lanes.Varying[byte, 64]` (64×8 = 512 bits) - maximum for 8-bit types
- `lanes.Varying[uint16, 32]` (32×16 = 512 bits) - maximum for 16-bit types
- `lanes.Varying[uint32, 16]` (16×32 = 512 bits) - maximum for 32-bit types
- `lanes.Varying[uint64, 8]` (8×64 = 512 bits) - maximum for 64-bit types

#### Pointer Support for Varying Types

SPMD supports both pointers to varying values and varying pointers, enabling flexible memory access patterns:

```go
// Pointer to varying value (single pointer, varying data)
var dataPtr *lanes.Varying[int]        // Pointer to varying int data
var arrayPtr *lanes.Varying[[4]int]    // Pointer to varying array

// Varying pointers (different pointers per lane)
var varyingPtrs lanes.Varying[*int]             // Each lane has different pointer
var varyingArrayPtrs lanes.Varying[*[4]int]     // Each lane points to different arrays

// Mixed pointer patterns
var mixedPtr *lanes.Varying[*int]      // Pointer to varying pointers
```

**Pointer Semantics**:

1. **Pointer to Varying (`*varying T`)**:
   - Single pointer pointing to varying data
   - Dereferencing yields varying values
   - Common for accessing shared varying data structures

2. **Varying Pointer (`varying *T`)**:
   - Each lane has its own pointer value
   - Dereferencing yields varying values (different memory locations per lane)
   - Used for scatter/gather operations

3. **Address Operations**:
   - `&varyingValue` produces `varying *T` (each lane gets address of its data)
   - `*varyingPtr` yields varying values when dereferencing varying pointers
   - `*ptrToVarying` yields varying values when dereferencing pointer to varying data

**Examples**:

```go
// Example 1: Pointer to varying array for bulk operations
func processVaryingArray(data *lanes.Varying[[16]int]) {
    go for i := range 16 {
        // Access varying array through pointer
        (*data)[i] *= 2
    }
}

// Example 2: Varying pointers for scatter access
func scatterGather(ptrs lanes.Varying[*int], values lanes.Varying[int]) {
    go for i := range len(values) {
        // Each lane writes to its own memory location
        *ptrs = values  // Scatter operation
    }
}

// Example 3: Mixed pointer operations
func complexPointerOps() {
    var data lanes.Varying[[4]int] = [4]int{1, 2, 3, 4}
    var dataPtr *lanes.Varying[[4]int] = &data

    go for i := range 4 {
        // Access through pointer to varying array
        (*dataPtr)[i] += lanes.Index()
    }
}
```

### Type Qualification Rules

1. **Default qualification**: All existing Go types are implicitly uniform (scalar)
2. **Explicit qualification**: Varying types use the `lanes.Varying[T]` generic type
3. **Nested qualification**: Qualification applies to the immediate type, not nested elements
   - `lanes.Varying[[]int]` - varying slice of uniform integers
   - `[]lanes.Varying[int]` - uniform slice of varying integers

### Type Compatibility

- Uniform `T` and regular `T` are identical types (uniform is the default)
- `lanes.Varying[T]` is a distinct type from `T`
- Conversions between qualified types follow assignment rules (see [Type System Rules](#type-system-rules))

## Variables

### Variable Declarations

Variables may be declared with type qualifiers:

```go
var (
    a int = 42                        // Scalar, same across lanes
    b lanes.Varying[int] = data[i]    // Vector, different per lane
    c int = 10                        // Implicitly uniform
)
```

### Short Variable Declarations

Type qualification is inferred from the right-hand side:

```go
a := 42             // int (uniform)
b := lanes.From(data)  // lanes.Varying[ElementType]
c := lanes.Index()     // lanes.Varying[int] (from built-in)
```

### Zero Values

- Uniform types have their usual Go zero values
- Varying types are zero-initialized across all lanes

## Statements

### SPMD For Statement

New statement forms enable SPMD execution:

```go
// Range-based SPMD loop
go for variable := range expression {
    // body executes in SPMD fashion
}

// Infinite SPMD loop  
go for {
    // infinite SPMD loop until explicit termination
}
```

**Syntax:**

```
SPMDForStmt = "go" "for" ( ( identifier ":=" )? "range" ( "[" Expression "]" )? Expression | ) Block .
```

**Range Grouping:**
The optional `[n]` syntax specifies that the data should be processed in groups where the number of lanes is constrained to be a multiple of `n`. This is essential for algorithms that require specific lane count relationships (e.g., base64 decoding requires multiples of 4 lanes for the 4:3 byte transformation).

**Important**: `range[n]` does not mean exactly `n` lanes are used, but rather that the actual lane count must be a multiple of `n`. For example:

- `range[4] data` with 8-lane hardware: processes 8 elements per iteration (2×4)  
- `range[4] data` with 16-lane hardware: processes 16 elements per iteration (4×4)
- `range[4] data` with 2-lane hardware: compiler unrolls to ensure 4-element groups

**Semantics:**

- The loop variable is implicitly `varying`
- Each lane processes a different element of the range
- The number of parallel lanes depends on the target architecture
- `break` and `return` statements follow ISPC-based control flow rules:
  - **Allowed** when all enclosing `if` statements have uniform conditions
  - **Forbidden** when any enclosing `if` statement has a varying condition or for return inside any nested `for` loop (for now)
- `continue` statements are always allowed and use masking
- **Nesting restriction**: `go for` loops cannot be nested within other `go for` loops (prohibited for now)
- **SPMD function restriction**: Functions with varying parameters cannot contain `go for` loops

**Examples:**

```go
// Process array elements in parallel (uniform element type)
go for i := range len(data) {
    data[i] = process(data[i])  // Each lane processes different element
}

// Range over explicit bounds
go for i := range 1000 {
    result[i] = compute(i)  // i is varying: [base, base+1, base+2, ...]
}

// Range with grouped processing (multiples of 4 elements)
go for i := range[4] data {  // Process in groups of multiples of 4
    result[i] = transform(data[i])
}

// Range over array of varying values
go for idx, varyingData := range []lanes.Varying[int]{values1, values2, values3} {
    // idx is uniform (processing one varying at a time): 0, then 1, then 2
    // varyingData is varying (each array element is a complete varying value)
    processed := varyingData * lanes.Varying[int](2)  // SPMD operations on each varying
}

// Infinite SPMD loop
go for {
    // Process data from channels until termination condition  
    select {
    case data := <-inputChannel:  // Channel carries varying values and define the number of active lanes
        result := processData(data)
        outputChannel <- result
    case <-terminateChannel:
        return  // Exit infinite loop
    }
}
```

**Variable Types in `go for` Range:**

1. **Range over uniform arrays**: `idx` is varying, `value` is uniform
2. **Range over varying arrays**: `idx` is uniform, `value` is varying  
3. **Range over numbers**: `idx` is varying

### Implicit SPMD Function Conversion

**Important**: Any function called from within a `go for` context (SPMD context) that could potentially operate on varying data automatically becomes an SPMD function with mask propagation, regardless of how it's invoked:

#### Automatic SPMD Conversion Rules

1. **Goroutine Launch**: `go func()` called within `go for` → function becomes SPMD function
2. **Defer Registration**: `defer func()` called within `go for` → function becomes SPMD function  
3. **Direct Function Call**: `func()` called within `go for` with varying parameters → function becomes SPMD function
4. **Mask Propagation**: The current execution mask is automatically passed to all SPMD functions

#### Example of Implicit Conversion

```go
func regularFunction(data int) {
    // This is a regular function when called outside SPMD context
    fmt.Printf("Processing: %d\n", data)
}

func main() {
    values := []int{1, 2, 3, 4}
    
    // Outside SPMD context - regular function call
    regularFunction(42)
    
    go for i := range len(values) {
        // Inside SPMD context - all of these non SPMD functions call are allowed and well defined:
        
        // 1. Goroutine launch - regularFunction no propagation of mask, no SPMD context.
        go regularFunction(values[i])
        
        // 2. Defer registration - regularFunction no propagation of mask, no SPMD context.
        defer regularFunction(values[i])
        
        // 3. Direct call with varying data - regularFunction taking uniform no propagation of mask, no SPMD context.
        if values[i] > 3 {
            // Will always call regularFunction with the sum of all the value that are > 3. If none are, it will just be called with 0.
            regularFunction(reduce.Add(values[i]))
        }
    }
}
```

**Key Point**: The same regular function can be called both from a non SPMD context or a SPMD context and behave exactly the same way. SPMD context does not affect non SPMD function.

#### Public API Restrictions in SPMD Contexts

**Important Limitation**: Since goroutines and defer statements within `go for` implicitly become SPMD functions, and public SPMD functions are prohibited (except for builtin lanes/reduce packages), there are restrictions on calling public APIs from these contexts:

```go
go for i := range len(data) {
    var values lanes.Varying[int] = data[i]
    
    // ✓ ALLOWED: lanes and reduce package functions (builtin public SPMD functions)
    laneId := lanes.Index()
    total := reduce.Add(values)
    
    // ✓ ALLOWED: panic and recover (explicit SPMD support)
    if values < 0 {
        panic(values)  // panic with varying data
    }
    
    defer func() {
        if r := recover(); r != nil {
            // recover can handle varying panic values
            handlePanic(r)  // Private function to handle recovery
        }
    }()
    
    // ✓ ALLOWED:: Direct calls to public non-SPMD APIs as there is no context propagation
    go func() {
        fmt.Printf("Value: %v\n", values)  // ERROR: public API conflict
    }()
    
    // ✓ WORKAROUND 1: Use reduce operations to convert to uniform
    go func() {
        uniformValues := reduce.From(values)  // Convert to []int
        logValues(uniformValues)  // Private function with uniform parameter
    }()
    
    // ✓ WORKAROUND 2: Use any type for opaque conversion
    go func() {
        logAny(any(values))  // Convert varying to any (opaque type)
    }()
}

// Private wrapper functions (uniform parameters only)
func logValues(values []int) {
    for _, v := range values {
        fmt.Printf("Value: %d\n", v)  // Regular public API call
    }
}

// Private wrapper using any type (not an SPMD function)
func logAny(value any) {
    fmt.Printf("Any value: %v\n", value)  // Printf handles varying via reflection
}

// Private error handler
func handlePanic(r any) {
    fmt.Printf("Recovered from panic: %v\n", r)
}
```

**Key Rules**:

1. **Reduce First**: Use `reduce.From()` to convert varying to uniform arrays before calling wrappers
2. **Any Type Conversion**: Convert varying to `any` for opaque handling by non-SPMD functions  
3. **Private Wrappers Only**: Wrapper functions must be private and take uniform or `any` parameters
4. **Explicit SPMD Support**: `panic` and `recover` are explicitly designed to handle varying data in SPMD contexts

### Control Flow in SPMD Context

#### Conditional Statements

`if` statements work with varying conditions using execution masks:

```go
go for i := range len(data) {
    if data[i] > threshold {    // varying condition
        data[i] = process(data[i])  // Only active lanes execute
    } else {
        data[i] = defaultValue     // Complementary lanes execute  
    }
}
```

#### Goroutine Launch with Varying Values

Goroutines can be launched with varying values, with a single goroutine processing all lanes at once:

```go
go for i := range len(data) {
    var results lanes.Varying[int] = compute(data[i])

    // Single goroutine launched with all lane values
    go func(values lanes.Varying[int]) {
        // Process all lanes within the goroutine
        processed := processAsync(values)  // SPMD function
        // Store results back (implementation-dependent)
    }(results)  // All lane values passed to goroutine
}
```

**Semantics:**

- A single goroutine is launched that receives all lane values
- **The goroutine function implicitly becomes an SPMD function when called from within a `go for` context**
- The execution mask is automatically preserved and passed to the goroutine
- The goroutine can use SPMD constructs and cross-lane operations internally
- Only active lanes (according to current execution mask) contribute values to the launched goroutine
- Synchronization between the parent SPMD context and spawned goroutines is the programmer's responsibility

**Example with Masking:**

```go
go for i := range len(data) {
    if data[i] > 0 {  // Creates execution mask
        // Only positive data values are passed to goroutine
        go processPositive(lanes.Varying[int](data[i]))  // Masked varying value
    }
}
```

#### Defer Statements with Varying Values

Defer statements can capture and use varying values, with each deferred function receiving the varying data:

```go
go for i := range len(data) {
    var results lanes.Varying[int] = process(data[i])

    // Defer function with varying parameter
    defer func(values lanes.Varying[int]) {
        cleanup(values)  // SPMD function processes all lanes
    }(results)

    // Direct defer call with varying arguments
    defer finalizeResults(results)
}
```

**Semantics:**

- **Deferred functions implicitly become SPMD functions when registered from within a `go for` context**
- The execution mask at defer registration time is automatically preserved and used when the deferred function executes
- Varying values are captured by value at defer registration time (standard defer behavior)
- Multiple defer statements in SPMD context accumulate in LIFO order per the standard defer semantics
- Each deferred function executes with the mask that was active when the defer was registered

**Example with Conditional Defer:**

```go
go for i := range len(data) {
    if data[i] > threshold {  // Creates execution mask
        var temp lanes.Varying[int] = allocateTemp(data[i])

        // Only lanes where data[i] > threshold register this defer
        defer func(allocated lanes.Varying[int]) {
            releaseTemp(allocated)  // Cleanup only for active lanes
        }(temp)
    }

    // Process data...
}
```

#### Regular For Loops within SPMD

Traditional `for` loops within SPMD contexts support `break` and `continue`:

```go
go for i := range len(items) {
    for j := 0; j < maxTries; j++ {  // j is uniform across lanes
        if process(items[i], j) {
            break    // Allowed: breaks inner loop per lane
        }
    }
}
```

#### Loop Control Restrictions

**Control Flow Rules (following ISPC approach with mask alteration tracking):**

- `break` and `return` statements in `go for` loops are **conditionally allowed**:
  - **Allowed** when all enclosing `if` statements have uniform conditions AND no mask alteration has occurred
  - **Forbidden** when any enclosing `if` statement has a varying condition
  - **Forbidden** after any `continue` statement in a varying context, even if subsequent conditions are uniform
- `continue` statements are always allowed in `go for` loops (implemented via masking)
- **Mask Alteration**: `continue` in varying contexts alters the execution mask, making subsequent uniform conditions affect only a subset of lanes
- `goto` statements cannot jump into or out of `go for` loops

**Examples:**

```go
// ALLOWED: Return/break under uniform conditions
go for i := range data {
    if uniformCondition {
        return  // OK: uniform condition allows direct return
    }
    if data[i] < 0 { // varying condition
        continue  // OK: continue always allowed
        // return  // COMPILE ERROR: varying condition forbids return
    }
}

// FORBIDDEN: Return/break under varying conditions forces mask tracking
go for i := range data {
    if data[i] < threshold { // varying condition
        // break  // COMPILE ERROR: varying condition forbids break
        continue  // OK: continue always allowed
    }
}

// FORBIDDEN: Return/break after continue in varying context
go for i := range data {
    if data[i] < 0 { // varying condition
        continue  // OK: continue always allowed, but alters mask
    }
    
    // Mask has been altered by previous continue
    if uniformCondition { // uniform condition, but mask is altered
        // return  // COMPILE ERROR: return forbidden due to mask alteration  
        // break   // COMPILE ERROR: break forbidden due to mask alteration
        continue  // OK: continue always allowed
    }
}

// FORBIDDEN: Complex mask alteration scenario
go for i := range data {
    if data[i] > 0 { // varying condition
        if data[i] < 10 { // nested varying condition
            continue  // Alters mask - some lanes skip remaining iterations
        }
    }
    
    // Execution mask altered by continue above
    if uniformCondition { // uniform condition on remaining active lanes only
        // return  // COMPILE ERROR: uniform condition but altered mask
    }
}
```

#### Type Switch Support for Varying Types

Type switches with varying values are **allowed anywhere in the code** when using explicit varying type cases:

```go
func processInterface(value interface{}) {
    // Type switch with varying value - can be used anywhere, not just in SPMD context
    switch v := value.(type) {
    case lanes.Varying[int]:
        // Handle varying int - each lane processes its own int value
        result := v * 2
        fmt.Printf("Varying int: %v\n", result)

    case lanes.Varying[string]:
        // Handle varying string - each lane processes its own string
        length := len(v)
        fmt.Printf("Varying string length: %v\n", length)

    case lanes.Varying[[4]byte]:
        // Handle varying constrained array - each lane processes its own array
        sum := v[0] + v[1] + v[2] + v[3]
        fmt.Printf("Array sum: %v\n", sum)

    case lanes.Varying[int, 8]:
        // Handle constrained varying type - requires multiple of 8 lanes
        processed := v + 100
        fmt.Printf("Constrained varying: %v\n", processed)

    case int:
        // Handle uniform int type
        fmt.Printf("Uniform int: %d\n", v)

    default:
        // Handle other types uniformly
        fmt.Printf("Unknown type: %T\n", v)
    }
}

// Type switches work in both SPMD and non-SPMD contexts
func demonstrateTypeSwitches() {
    // Outside SPMD context
    var varyingInt lanes.Varying[int] = 42
    var uniformInt int = 24

    processInterface(varyingInt)  // Works with varying interface{}
    processInterface(uniformInt)  // Works with uniform interface{}

    // Inside SPMD context
    go for i := range 4 {
        var dynamicValue lanes.Varying[interface{}] = i * 10
        processInterface(dynamicValue)  // Works in SPMD context too
    }
}
```

**Type Switch Rules**:

1. **Explicit Varying Cases**: Cases must explicitly specify `lanes.Varying[T]` for varying values
2. **Constrained Varying**: Cases can specify `lanes.Varying[T, n]` for constrained types
3. **Type Safety**: Each case receives the correctly typed varying value
4. **Mixed Cases**: Can mix varying and uniform type cases in same switch
5. **Context Independent**: Type switches work in both SPMD and non-SPMD contexts
6. **Interface Conversion**: `lanes.Varying[interface{}]` can hold both varying and uniform values

**Invalid Type Switch Examples**:

```go
// ✗ PROHIBITED: Implicit varying types in cases when value is varying interface{}
var varyingInterface lanes.Varying[interface{}] = lanes.Varying[int](42)
switch varyingInterface.(type) {
case int:          // ERROR: Cannot match varying interface{} with uniform int
    // Must use: case lanes.Varying[int]:
}

// ✗ PROHIBITED: Type assertion without explicit varying
var v lanes.Varying[interface{}] = lanes.Varying[int](42)
x := v.(int)       // ERROR: Must use v.(lanes.Varying[int])
```

### Map Restrictions

Maps have strict restrictions regarding varying types to maintain deterministic behavior:

#### Map Key Restrictions

**Varying types are prohibited as map keys**, both at declaration and access sites:

```go
// ✗ PROHIBITED: Varying keys at declaration
var m1 map[lanes.Varying[int]]string           // ERROR: varying map keys not allowed
var m2 map[lanes.Varying[string]]int           // ERROR: varying map keys not allowed

// ✗ PROHIBITED: Varying keys at access sites
go for i := range len(data) {
    var key lanes.Varying[string] = data[i]

    value := someMap[key]               // ERROR: varying key in map access
    someMap[key] = newValue             // ERROR: varying key in map assignment
    delete(someMap, key)                // ERROR: varying key in map delete
    _, exists := someMap[key]           // ERROR: varying key in map existence check
}
```

#### Map Value Restrictions

**Varying values in maps are allowed with limitations**:

```go
// ✓ ALLOWED: Uniform keys with varying values
var validMap map[string]lanes.Varying[int]     // OK: uniform keys, varying values

// ✓ ALLOWED: Access with uniform keys
go for i := range len(data) {
    uniformKey := "key" + strconv.Itoa(i)
    validMap[uniformKey] = lanes.Varying[int](data[i])  // OK: uniform key
}
```

**Rationale**: Varying map keys would create non-deterministic behavior since each lane could access different map entries, making it impossible to maintain consistent map state across SIMD lanes.

### Select Statements with Varying Channels

Select statements can operate on channels that carry varying values, enabling SPMD-aware concurrent processing:

```go
func spmdChannelProcessor() {
    inputCh := make(chan lanes.Varying[int], 10)
    outputCh := make(chan lanes.Varying[int], 10)
    terminateCh := make(chan bool)

    // SPMD infinite loop with channel processing
    go for {
        select {
        case data := <-inputCh:
            // data is lanes.Varying[int] - process all lanes simultaneously
            processed := data * lanes.Varying[int](2)
            select {
            case outputCh <- processed:
                // Successfully sent varying result
            default:
                // Output channel full, handle appropriately
            }
            
        case <-terminateCh:
            return  // Exit SPMD loop
            
        default:
            // No channels ready, continue processing
            continue
        }
    }
}
```

**Select Rules with Varying:**

1. **Channel Types**: Channels can carry `lanes.Varying[T]` or uniform `T` values
2. **Lane Coordination**: All lanes participate in the same select operation
3. **Channel Operations**: Send/receive operations work per-lane for varying channels
4. **Blocking Behavior**: Select blocks until at least one channel operation can proceed on any lane

## Built-in Functions

### Lane Information

#### `lanes.Count(type) int`

Returns the number of SIMD lanes for the given type.

```go
width := lanes.Count(int32{})  // e.g., 4 for 128-bit SIMD
count := lanes.Count(byte{})   // e.g., 16 for 128-bit SIMD
```

#### `lanes.Index() lanes.Varying[int]`

Returns the current lane index within an SPMD context. **IMPORTANT**: `lanes.Index()` can **only** be called from within `go for` loops and is enforced at compile time.

```go
go for i := range 100 {
    laneId := lanes.Index()  // [0, 1, 2, 3] for 4-lane SIMD
    // Use laneId for lane-specific logic
}
```

**Compile-Time Restriction:**

```go
func invalidUsage() {
    // ERROR: lanes.Index() requires SPMD context (go for loop)
    lane := lanes.Index()  // Compile error
}

func validUsage() {
    go for i := range 8 {
        lane := lanes.Index()  // OK: inside go for loop
    }
}

**Performance**: All `lanes` operations are **automatically inlined** by the compiler for optimal performance.

### Cross-Lane Operations

#### `lanes.Broadcast[T any](value VaryingAny[T], lane int) VaryingAny[T]`

Broadcasts the value from the specified lane to all lanes.

```go
// Broadcast lane 0's value to all lanes - works with lanes.Varying[T] or lanes.Varying[T, N]
broadcastValue := lanes.Broadcast(varyingData, 0)
```

#### `lanes.Rotate[T any](value VaryingAny[T], offset int) VaryingAny[T]`

Rotates values across lanes by the specified offset.

```go
// Each lane gets value from (current_lane - 1) % lane_count - works with lanes.Varying[T] or lanes.Varying[T, N]
rotated := lanes.Rotate(varyingData, 1)
```

#### `lanes.Swizzle[T any](value VaryingAny[T], indices VaryingInteger[int]) VaryingAny[T]`

Performs arbitrary permutation of values across lanes.

```go
// Each lane accesses value[indices[lane]] - works with lanes.Varying[T] or lanes.Varying[T, N]
permuted := lanes.Swizzle(sourceData, indexPattern)
```

#### `lanes.ShiftLeft[T Integer](value VaryingInteger[T], count VaryingInteger[int]) VaryingInteger[T]`

#### `lanes.ShiftRight[T Integer](value VaryingInteger[T], count VaryingInteger[int]) VaryingInteger[T]`

Performs bit shifting operations across lane.

```go
shifted := lanes.ShiftLeft(data, shiftCounts)  // Across-lane shift amounts
```

### Error Handling Functions

#### `panic(value any) // Explicit SPMD support`

The built-in `panic` function is explicitly designed to handle varying values in SPMD contexts:

```go
go for i := range len(data) {
    if data[i] < 0 {
        panic(lanes.Varying[string]("negative value"))  // Can panic with varying data
    }
}
```

#### `recover() any // Explicit SPMD support`

The built-in `recover` function is explicitly designed to handle varying panic values:

```go
defer func() {
    if r := recover(); r != nil {
        // r may contain varying data from SPMD panic
        handleVaryingPanic(r)  // Process varying panic value
    }
}()
```

### Data Construction

#### `lanes.From[T any](slice []T) lanes.Varying[T]`

Creates varying data from a uniform slice.

```go
lookupTable := []byte{1, 2, 4, 8, 16, 32, 64, 128}
varyingLUT := lanes.From(lookupTable)  // Ready for swizzle operations
```

### Constrained Varying Conversion

#### `lanes.FromConstrained[T any](data lanes.Varying[T]) ([]lanes.Varying[T], []lanes.Varying[bool])`

Converts universal constrained varying to unconstrained varying with explicit mask.

```go
func processUniversalConstrained(data lanes.Varying[int]) {
    // Convert constrained varying to unconstrained varying + mask
    values, masks := lanes.FromConstrained(data)

    // Now can work with unconstrained varying and explicit masks
    for i, value := range values {
        mask := masks[i]
        result := process(value, mask)
        // Handle result...
    }
}
```

**Important Restrictions for universal constrained varying:**

1. **Assignment**: Cannot assign unconstrained `lanes.Varying[T]` to a constrained `lanes.Varying[T, N]`
2. **Operations**: Most operations fail at type checking without an explicit constraint
3. **Control Flow**: if/for/switch statements require a known constraint
4. **Type Switch Only**: Only way to convert back to sized constrained varying

```go
func handleConstrainedVarying(data lanes.Varying[byte]) {
    // ILLEGAL: Direct operations without known constraint
    // result := data + 10        // ERROR: no operations without constraint
    // if data > 5 { ... }        // ERROR: no control flow without constraint

    // LEGAL: Type switch to convert to specific constrained size
    switch v := data.(type) {
    case lanes.Varying[byte, 4]:
        // Now can operate on lanes.Varying[byte, 4]
        result := v + 10
    case lanes.Varying[byte, 8]:
        // Now can operate on lanes.Varying[byte, 8]
        result := v * 2
    default:
        // Handle other constraint sizes or convert to unconstrained
        values, masks := lanes.FromConstrained(data)
        // Process using unconstrained varying...
    }
}
```

## Standard Library Extensions

### Package `reduce`

The `reduce` package provides operations that combine varying values into uniform results.

**Generic Type Constraints for Varying Types:**

```go
// Standard numeric constraint
type Numeric interface {
    int | int8 | int16 | int32 | int64 |
    uint | uint8 | uint16 | uint32 | uint64 |
    float32 | float64 | complex64 | complex128
}

// Standard integer constraint  
type Integer interface {
    int | int8 | int16 | int32 | int64 |
    uint | uint8 | uint16 | uint32 | uint64
}

// Boolean operations (All, Any)
type VaryingBool interface {
    lanes.Varying[bool]
}

// Numeric operations (Add, Max, Min, etc.)
type VaryingNumeric[T Numeric] interface {
    lanes.Varying[T]
}

// Integer bitwise operations (Or, And, Xor)
type VaryingInteger[T Integer] interface {
    lanes.Varying[T]
}

// Comparable operations (Equal, NotEqual)
type VaryingComparable[T comparable] interface {
    lanes.Varying[T]
}

// Generic operations (Count, From)
type VaryingAny[T any] interface {
    lanes.Varying[T]
}
```

**Performance**: All `reduce` operations are **automatically inlined** by the compiler for optimal performance.

#### Boolean Reductions

```go
// reduce.All(data VaryingBool) bool
allTrue := reduce.All(conditions)    // true if all lanes are true - works with lanes.Varying[bool]

// reduce.Any(data VaryingBool) bool
anyTrue := reduce.Any(conditions)    // true if any lane is true - works with lanes.Varying[bool]
```

#### Bitwise Integer Reductions

```go
// reduce.Or[T Integer](data VaryingInteger[T]) T - bitwise OR across lanes
combined := reduce.Or(flags)         // Works with lanes.Varying[int], lanes.Varying[uint32], etc.

// reduce.And[T Integer](data VaryingInteger[T]) T - bitwise AND across lanes
masked := reduce.And(values)         // Works with lanes.Varying[int], lanes.Varying[uint64], etc.

// reduce.Xor[T Integer](data VaryingInteger[T]) T - bitwise XOR across lanes
xored := reduce.Xor(values)          // Works with lanes.Varying[int], etc.
```

#### Arithmetic Reductions

```go
// reduce.Add[T Numeric](data VaryingNumeric[T]) T
sum := reduce.Add(values)            // Works with lanes.Varying[int], lanes.Varying[float64], etc.

// reduce.Max[T comparable](data VaryingComparable[T]) T
maximum := reduce.Max(values)        // Works with lanes.Varying[float64], lanes.Varying[string], etc.

// reduce.Min[T comparable](data VaryingComparable[T]) T
minimum := reduce.Min(values)        // Works with lanes.Varying[int], lanes.Varying[float32], etc.
```

#### Type Conversion

```go
// reduce.From[T NumericOrBool](lanes.Varying[T]) []T - only accepts numeric types and bool
array := reduce.From(values)         // Convert varying to array of underlying type
// Useful for debugging and interfacing with non-SPMD code

// NumericOrBool constraint - includes all numeric types plus bool  
type NumericOrBool interface {
    bool | 
    int | int8 | int16 | int32 | int64 |
    uint | uint8 | uint16 | uint32 | uint64 |
    float32 | float64 | complex64 | complex128
}
```

### Standard Library Integration

#### Printf Support for Varying Types

The `fmt.Printf` function automatically detects varying types when using the `%v` verb and converts them to arrays for display:

```go
var values lanes.Varying[int] = 42
fmt.Printf("Values: %v\n", values)   // Automatically uses reduce.From(values)
// Output: Values: [42 42 42 42]  (assuming 4 lanes)

var data lanes.Varying[float32] = 3.14
fmt.Printf("Data: %v\n", data)       // Works with numeric types and bool
// Output: Data: [3.14 3.14 3.14 3.14]

var flags lanes.Varying[bool] = true
fmt.Printf("Flags: %v\n", flags)     // Also works with bool
// Output: Flags: [true true true true]
```

**PoC Limitations**:

- Only numerical types and `bool` supported in `reduce.From()` and printf
- Complex types, structs, and pointers not supported in the proof of concept
- Other format verbs (`%d`, `%f`, etc.) not automatically converted

#### Lane Analysis

```go
// reduce.FindFirstSet(lanes.Varying[bool]) int
firstTrue := reduce.FindFirstSet(conditions)  // Index of first true lane (-1 if none)

// reduce.Mask(lanes.Varying[bool]) uint16
bitmask := reduce.Mask(conditions)            // Convert boolean vector to bitmask
```

## Type System Rules

### SPMD Context Enforcement

`lanes.Index()` requires SPMD context and is enforced at compile time. The key rule is that lane information must be available either from `go for` loops or from varying parameters in SPMD functions.

**Valid Contexts for `lanes.Index()`:**

1. **Inside `go for` loops directly:**

```go
func validDirectUsage() {
    go for i := range 8 {
        lane := lanes.Index()  // OK: go for provides lane context
    }
}
```

2. **Inside SPMD functions (with varying parameters):**

```go
func spmdFunction(data lanes.Varying[int]) lanes.Varying[int] {
    lane := lanes.Index()  // OK: lane count inferred from varying parameter
    return data + lane
}

// Called from go for context
func fromGoFor() {
    go for i := range 8 {
        result := spmdFunction(lanes.Varying[int](i))  // OK
    }
}

// Called from non-SPMD context - still OK!
func fromNonSPMD() {
    data := lanes.Varying[int](42)
    result := spmdFunction(data)  // OK: SPMD function can infer lanes from varying
}
```

**Invalid Contexts (Compile Errors):**

1. **Non-SPMD functions (no varying parameters):**

```go
func nonSPMDFunction() lanes.Varying[int] {
    lane := lanes.Index()  // ERROR: no varying parameters to infer lane count
    return lanes.Varying[int](lane)
}

func caller() {
    go for i := range 8 {
        result := nonSPMDFunction()  // ERROR: non-SPMD function can't use lanes.Index()
    }
}
```

2. **Outside any function with lane information:**

```go
func invalidUsage() {
    lane := lanes.Index()  // ERROR: no SPMD context (no go for, no varying params)
}
```

**Key Rule**: `lanes.Index()` is legal when lane count can be determined - either from `go for` context or from varying parameters in SPMD functions.

### Control Flow Restrictions Outside SPMD Context

**Important Design Decision**: Control flow operations on varying values are **prohibited outside SPMD contexts** to maintain code readability and prevent confusion.

**Prohibited Outside SPMD Context:**

```go
func nonSPMDFunction() {
    var data lanes.Varying[int] = lanes.Varying[int](42)

    // All of these are COMPILE ERRORS outside go for loops:
    if data > 30 { ... }           // ERROR: varying condition outside SPMD context
    for data != 0 { ... }          // ERROR: varying loop condition outside SPMD context
    switch data { ... }            // ERROR: varying switch expression outside SPMD context
    for i, v := range data { ... } // ERROR: varying range outside SPMD context
}
```

**Legal Inside SPMD Context:**

```go
func processData() {
    go for i := range 8 {
        var data lanes.Varying[int] = lanes.Varying[int](i * 10)

        // All control flow operations are legal inside go for:
        if data > 30 {                    // OK: varying condition in SPMD context
            data = data * 2
        }

        switch data {                     // OK: varying switch in SPMD context
        case lanes.Varying[int](0):
            // Handle zero case
        default:
            // Handle other cases
        }
    }
}
```

**Rationale (Non-Technical):**

This restriction is **intentionally designed for code maintainability and clarity**:

1. **Readability**: Control flow with varying outside SPMD context is confusing - it's unclear what the control flow means without explicit SIMD context
2. **Intent Clarity**: `go for` makes SIMD intent explicit, while scattered varying control flow throughout regular code obscures the parallel processing intent  
3. **Code Organization**: Encourages developers to group SIMD operations in clear, identifiable blocks rather than spreading them throughout regular code
4. **Maintenance**: Makes SIMD code easy to identify, review, and optimize as a cohesive unit

**Processing `lanes.FromConstrained` Results:**

This design makes `go for` the natural way to process `lanes.FromConstrained` results:

```go
func processUniversalConstrained(data lanes.Varying[int]) {
    values, masks := lanes.FromConstrained(data)

    // Natural processing pattern: go for over array of varying values
    go for idx, varyingGroup := range values {
        mask := masks[idx]  // Get corresponding mask (uniform index for this iteration, but varying mask value)

        // Process this varying group with its mask
        if mask {  // Use the mask to enable only the correct lanes for operation
            processed := varyingGroup * lanes.Varying[int](2)
            result := reduce.Add(processed)
            fmt.Printf("Group %d result: %d\n", idx, result)
        }
    }
}
```

### Universal Constrained Varying Rules

Functions with `lanes.Varying[T]` parameters (without a constraint) accept any constrained varying type but have strict usage restrictions:

**Valid Assignment:**

```go
func processAnyConstrained(data lanes.Varying[int]) {
    // Function accepts lanes.Varying[int, 4], lanes.Varying[int, 8], lanes.Varying[int, 16], etc.
}

func caller() {
    data4 := lanes.Varying[int, 4](someArray)
    data8 := lanes.Varying[int, 8](otherArray)

    processAnyConstrained(data4)  // OK: lanes.Varying[int, 4] → lanes.Varying[int]
    processAnyConstrained(data8)  // OK: lanes.Varying[int, 8] → lanes.Varying[int]
}
```

**Invalid Assignment:**

```go
func invalidAssignment() {
    var unconstrained lanes.Varying[int] = lanes.Varying[int](42)
    var universal lanes.Varying[int]

    universal = unconstrained  // ERROR: cannot assign unconstrained to constrained varying
}
```

**Operations Restrictions:**

```go
func restrictedOperations(data lanes.Varying[int]) {
    // ILLEGAL: Arithmetic operations on unconstrained lanes.Varying[T] are restricted
    // result := data + 10      // ERROR: operations forbidden without constraint

    // ILLEGAL: Comparisons
    // if data > 5 { ... }      // ERROR: comparisons forbidden without constraint

    // ILLEGAL: Control flow without constraint
    // for data > 0 { ... }     // ERROR: control flow forbidden without constraint

    // LEGAL: Type switch to convert to specific size
    switch v := data.(type) {
    case lanes.Varying[int, 4]:
        result := v + 10     // OK: operations allowed on lanes.Varying[int, 4]
    case lanes.Varying[int, 8]:
        result := v * 2      // OK: operations allowed on lanes.Varying[int, 8]
    default:
        // LEGAL: Convert to unconstrained varying
        values, masks := lanes.FromConstrained(data)
        // Process using standard varying operations...
    }
}
```

### Assignment Rules

1. **Uniform to Uniform**: Direct assignment (existing Go behavior)

   ```go
   var a, b int
   a = b  // Valid
   ```

2. **Varying to Varying**: Direct assignment

   ```go
   var x, y lanes.Varying[int]
   x = y  // Valid
   ```

3. **Uniform to Varying**: Implicit broadcast

   ```go
   var u int = 42
   var v lanes.Varying[int]
   v = u  // Valid: broadcasts u to all lanes
   ```

4. **Varying to Uniform**: **Prohibited** (compile error)

   ```go
   var v lanes.Varying[int]
   var u int
   u = v  // ERROR: cannot assign varying to uniform
   ```

### Type Casting Rules

SPMD type casting follows SIMD register capacity constraints:

1. **Downcasting (Larger to Smaller)**: **Allowed** - fits in same or fewer registers

   ```go
   var large lanes.Varying[uint32] = lanes.Varying[uint32](0x12345678)
   var small lanes.Varying[uint16] = lanes.Varying[uint16](large)  // Valid: truncates, same lane count fits in smaller registers

   var wide lanes.Varying[int64] = lanes.Varying[int64](1000)
   var narrow lanes.Varying[int32] = lanes.Varying[int32](wide)    // Valid: 4×64-bit → 4×32-bit still fits

   var double lanes.Varying[float64] = lanes.Varying[float64](3.14159)
   var single lanes.Varying[float32] = lanes.Varying[float32](double)  // Valid: precision loss, but smaller total size
   ```

2. **Upcasting (Smaller to Larger)**: **Prohibited** - exceeds SIMD register capacity

   ```go
   var small lanes.Varying[uint16] = lanes.Varying[uint16](0x1234)
   var large lanes.Varying[uint32] = lanes.Varying[uint32](small)  // ERROR: upcasting doubles total bit size

   var narrow lanes.Varying[int32] = lanes.Varying[int32](42)
   var wide lanes.Varying[int64] = lanes.Varying[int64](narrow)    // ERROR: 4×32-bit → 4×64-bit exceeds 128-bit SIMD

   var single lanes.Varying[float32] = lanes.Varying[float32](2.7)
   var double lanes.Varying[float64] = lanes.Varying[float64](single)  // ERROR: 4×32-bit → 4×64-bit exceeds capacity
   ```

**Register Capacity Problem**:

- WASM SIMD128 provides 128-bit registers
- `lanes.Varying[uint32, 4]` uses exactly 128 bits (4 × 32 = 128)
- `lanes.Varying[uint64, 4]` would require 256 bits (4 × 64 = 256) - doesn't fit!
- Upcasting would require splitting into multiple varying values or reducing lane count

3. **Future Enhancement**: Upcasting via lanes operations (not in PoC)

   ```go
   // Future: lanes operations that handle register splitting
   var small lanes.Varying[uint16] = lanes.Varying[uint16](0x1234)
   var large1, large2 lanes.Varying[uint32] = lanes.SplitUpcast[uint32](small)  // Future: returns 2 varying

   // Or: reduce lane count to fit larger elements
   var reduced lanes.Varying[uint64, 2] = lanes.ReduceUpcast[uint64](narrow)     // Future: fewer lanes
   ```

**Key Constraint**: SIMD register width limits total bit size, making upcasting complex and requiring explicit handling of capacity overflow.

5. **Pointer Assignment Rules**: Follow the same type qualification rules

   ```go
   // Pointer to varying assignments
   var vData lanes.Varying[int] = 42
   var ptrToVarying *lanes.Varying[int] = &vData  // Valid: address of varying

   // Varying pointer assignments
   var data [4]int = [4]int{1, 2, 3, 4}
   var vPtrs lanes.Varying[*int]                  // Each lane gets different pointer
   go for i := range 4 {
       vPtrs = &data[i]                    // Valid: each lane points to different element
   }

   // Dereferencing rules
   var varyingValue lanes.Varying[int] = *ptrToVarying  // Valid: yields varying
   var varyingResult lanes.Varying[int] = *vPtrs        // Valid: yields varying

   // Invalid pointer assignments
   var uniformPtr *int
   uniformPtr = vPtrs  // ERROR: cannot assign varying pointer to uniform
   ```

6. **Address Operation Rules**:

   ```go
   // Taking address of varying yields varying pointer
   var vData lanes.Varying[int] = 42
   var vPtrs lanes.Varying[*int] = &vData  // Valid: each lane gets address of its data

   // Taking address of uniform yields uniform pointer
   var uData int = 42
   var uPtr *int = &uData           // Valid: single pointer to uniform data
   ```

### Function Parameters

Functions become **SPMD functions** when they accept varying parameters:

```go
// Regular function (uniform parameters)
func process(data []byte) int { ... }

// SPMD function (varying parameter)
func transform(value lanes.Varying[int]) lanes.Varying[int] { ... }

// Mixed function (both uniform and varying)
func compute(config Settings, data lanes.Varying[[]byte]) lanes.Varying[Result] { ... }
```

### Return Values

Both SPMD and non-SPMD functions may return varying values:

```go
// SPMD function returning varying (has varying parameters)
func analyze(input lanes.Varying[Data]) (result lanes.Varying[int], err error) {
    // Implementation handles per-lane processing
    return result, err
}

// Non-SPMD function returning varying (no varying parameters)
func createVaryingData() lanes.Varying[int] {
    // Creates varying data without requiring varying inputs
    // Can only return uniform values broadcast to all lanes
    return lanes.Varying[int](42)  // All lanes get same value
}

// Non-SPMD functions can use most lanes/reduce functions, except lanes.Index()
func createProcessedData() lanes.Varying[int] {
    // LEGAL: Non-SPMD function (no varying parameters) returning varying
    data := lanes.Varying[int](100)

    // LEGAL: reduce functions work outside SPMD context
    sum := reduce.Add(data)

    // LEGAL: most lanes functions work outside SPMD context
    return lanes.Broadcast(sum, 0)
}

// This would be ILLEGAL:
// func generateLaneData() lanes.Varying[int] {
//     return lanes.Index() * 10  // ERROR: lanes.Index() needs varying params or go for
// }
```

### Interface Compatibility

Varying values can be passed as `interface{}` or `any`:

```go
func generic(value any) {
    // Reflection reveals varying types as uniform arrays + mask
    // This function is NOT an SPMD function
}

func spmdGeneric(value lanes.Varying[any]) {
    // This function IS an SPMD function
    // Each lane may have different types
}
```

## Function Semantics

### SPMD Function Visibility Restrictions

**Public API Restriction**: Functions with varying parameters are **not allowed** in public APIs (exported functions), except for builtin functions in the `lanes` and `reduce` packages.

```go
// ILLEGAL: Public SPMD functions not allowed
func Process(data lanes.Varying[int]) lanes.Varying[int] {  // ERROR: public varying parameters not allowed
    return data * 2
}

// LEGAL: Private SPMD functions allowed within package
func process(data lanes.Varying[int]) lanes.Varying[int] {  // OK: private function with varying parameters
    return data * 2
}

// LEGAL: Builtin public functions in lanes/reduce packages
sum := reduce.Add(varyingData)     // OK: builtin public SPMD function
rotated := lanes.Rotate(data, 1)   // OK: builtin public SPMD function
```

**Justification**:

- Prevents SPMD functions from spreading through public APIs during experimental phase
- Avoids complications with constrained varying type matching across package boundaries
- Allows internal use within packages for implementation flexibility
- Keeps experimental feature from appearing in external APIs until mature

### SPMD Function Execution

SPMD functions (both private user functions and builtin functions) receive an implicit execution mask that tracks active lanes:

```go
func process(data lanes.Varying[int]) lanes.Varying[int] {  // Private function - allowed
    // Compiler adds: mask lanes.Varying[bool] (implicit parameter)

    if data < 0 {
        // Mask updated: only lanes with data < 0 remain active
        return -data
    }
    return data * 2
}
```

### Calling Conventions

1. **Non-SPMD functions (no varying parameters)**: Return unmasked varying results
2. **SPMD functions (with varying parameters)**: Can be called from any context with automatic mask handling
3. **Built-in functions**: Follow standard Go calling conventions

### SPMD Function Call Context

SPMD functions (functions with varying parameters) can be called from **any context**, not just SPMD contexts:

**From Non-SPMD Context:**

```go
func regularFunction() {
    data := lanes.Varying[int](42)

    // LEGAL: Call SPMD function from non-SPMD context
    result := processVarying(data)  // Mask implicitly set to all lanes active

    // LEGAL: Call reduce functions from non-SPMD context
    sum := reduce.Add(data)  // All lanes active by default
}

func processVarying(input lanes.Varying[int]) lanes.Varying[int] {  // SPMD function
    return input * 2
}
```

**From SPMD Context:**

```go
go for i := range 8 {
    data := lanes.Varying[int](i * 10)

    // LEGAL: Call SPMD function from SPMD context
    result := processVarying(data)  // Inherits current execution mask

    if data > 50 {
        // LEGAL: Call reduce with masked data
        sum := reduce.Add(result)  // Only processes active lanes
    }
}
```

**Captured Mask Behavior:**

```go
go for i := range 8 {
    data := lanes.Varying[int](i * 10)

    if data > 30 {  // Creates mask: lanes where data > 30
        // Defer captures both varying data AND execution mask
        defer func(captured lanes.Varying[int]) {
            // This SPMD function call uses the captured mask
            processed := processVarying(captured)  // Uses mask from capture point
            total := reduce.Add(processed)         // Reduces only originally active lanes
            fmt.Printf("Deferred total: %d\n", total)
        }(data)
    }
}
```

### Mask Propagation

Execution masks are automatically handled in these contexts:

**Implicit Mask Creation:**

- **Non-SPMD context calls**: Mask set to all lanes active when calling SPMD functions
- **SPMD context calls**: Current execution mask passed to SPMD functions  
- **Captured varying**: Mask captured at the point of capture and preserved

**Automatic Mask Propagation:**

- Function calls with varying parameters (SPMD functions)
- Control flow statements (`if`, `for`, `switch`) within SPMD contexts
- Built-in operations on varying data (reduce, lanes functions)
- Deferred function calls that captured varying data with masks

**Key Rules:**

1. **SPMD functions can always be called** - context determines mask behavior
2. **Reduce functions work everywhere** - no SPMD context requirement
3. **Mask inheritance**: Captured varying data preserves its execution mask
4. **Default behavior**: When no mask exists, all lanes are considered active

## Execution Model

### Lane-Based Execution

SPMD code executes across multiple lanes simultaneously:

```go
go for i := range 16 {  
    // If hardware has 4 lanes, this executes as:
    // Iteration 1: lanes process indices [0,1,2,3]
    // Iteration 2: lanes process indices [4,5,6,7]  
    // Iteration 3: lanes process indices [8,9,10,11]
    // Iteration 4: lanes process indices [12,13,14,15]
}
```

### Masking and Control Flow

Divergent control flow is handled through execution masks:

```go
go for i := range data {
    if data[i] > threshold {  // Creates mask: [true, false, true, false]
        data[i] *= 2          // Only lanes 0,2 execute this
    } else {
        data[i] += 1          // Only lanes 1,3 execute this
    }
    // All lanes continue here
}
```

### Early Termination

Loops terminate early when no lanes remain active:

```go
go for i := range data {
    if process(data[i]) {
        continue  // This lane skips to next iteration
    }
    // If reduce.Any(activeMask) == false, loop terminates
}
```

## Backward Compatibility

### No New Keywords

SPMD types use the `lanes` package (`lanes.Varying[T]`) instead of new keywords. The identifiers `uniform` and `varying` are **not** reserved words and remain valid Go identifiers:

**Valid Go identifiers (not keywords):**

- Variable names: `var uniform = 42`
- Function names: `func varying() {}`
- Package aliases: `import uniform "math"`
- Labels: `goto uniform`
- Struct fields: `type T struct { uniform int }`

**New SPMD syntax (package-based):**

- Type declarations: `var x lanes.Varying[int]`
- Parameter lists: `func f(a lanes.Varying[byte])`
- Type assertions: `value.(lanes.Varying[int])`

### Migration Path

Existing Go code continues to work unchanged:

1. **No breaking changes**: All existing Go programs compile and run identically
2. **Opt-in adoption**: SPMD features are only active when explicitly used
3. **Gradual migration**: Applications can adopt SPMD incrementally

## Implementation Notes

### Experimental Feature Status

SPMD support is implemented as an experimental feature controlled by the `GOEXPERIMENT=spmd` environment variable.

**Enabling SPMD Extensions:**

```bash
# TinyGo Proof of Concept - Build WASM with SIMD
GOEXPERIMENT=spmd tinygo build -target=wasi -o myprogram.wasm myprogram.go

# Run in WebAssembly runtime with SIMD support
go run wasmer-runner.go myprogram.wasm

# Validate syntax without execution
GOEXPERIMENT=spmd tinygo build -target=wasi -o /dev/null myprogram.go

# In test files
// run -goexperiment spmd -target=wasi
// errorcheck -goexperiment spmd
```

**Proof of Concept Scope:**
The TinyGo PoC implementation includes:

- ✅ Parser accepts SPMD syntax (`go for`, `lanes.Varying[T]` type expressions)
- ✅ Type checker recognizes `lanes.Varying[T]` as an SPMD type
- ✅ Type checker validates SPMD type system rules
- ✅ Basic `lanes` and `reduce` package implementations
- ✅ LLVM backend generates WebAssembly SIMD128 instructions
- ✅ Executable WASM binaries that can be tested with wasmer-go
- ❌ Full standard library integration (limited subset only)
- ❌ All cross-lane operations (basic set for demonstration)

**Feature Gating:**

- All SPMD syntax (`lanes.Varying[T]`, `go for`) is gated behind `buildcfg.Experiment.SPMD`
- Parser recognizes `go for` and `lanes.Varying[T]` type expressions only when experiment is enabled
- Type checker validates SPMD rules only when experiment is enabled
- Standard library extensions (`lanes`, `reduce`) require experimental flag

**Implementation Requirements:**

- Add `SPMD bool` field to `internal/goexperiment/flags.go`
- Generate `exp_spmd_on.go` and `exp_spmd_off.go` build constraint files
- Guard all SPMD-related code with `if buildcfg.Experiment.SPMD` checks
- Ensure graceful fallback when experiment is disabled

### Target Architectures

**Proof of Concept Target:**

- **WebAssembly SIMD128**: 4x32-bit or 16x8-bit lanes (via TinyGo LLVM backend)
- **Runtime**: wasmer-go with SIMD support enabled
- **Testing**: Execute WASM binaries and verify SIMD instruction generation

**Future Implementation Targets:**

- **x86-64 SSE/AVX**: Variable lane counts based on instruction set  
- **ARM NEON**: 4x32-bit or 8x16-bit lanes
- **Native Go compiler**: Full toolchain integration

### Compiler Phases

1. **Lexical Analysis**: Context-sensitive keyword recognition (gated by experiment)
2. **Parsing**: New grammar rules for SPMD constructs (gated by experiment)
3. **Type Checking**: Uniform/varying type validation (gated by experiment)
4. **SSA Generation**: SPMD-aware intermediate representation
5. **Code Generation**: Vector instruction emission

### Error Handling

SPMD-specific compile-time errors:

- `cannot assign varying to uniform`
- `break/return statement not allowed under varying conditions in SPMD for loop`
- `go for loops cannot be nested` (for now)
- `go for loops not allowed in SPMD functions`
- `varying parameters not allowed in public functions`
- `select can only use channels with uniform or varying data types`
- `varying map keys not allowed`
- `cannot use varying key in map access`
- `cannot assign varying pointer to uniform pointer`
- `cannot take address of varying value in uniform context`
- `invalid pointer operation on varying type`
- `cannot match varying interface with uniform type in type switch`
- `type assertion must explicitly specify varying type`
- `varying type not supported in this context`

### Runtime Behavior

- **Memory layout**: Varying types stored as contiguous vector data
- **Function calls**: Automatic mask parameter insertion for SPMD functions
- **Garbage collection**: Standard Go GC handles vector data transparently
- **Reflection**: Varying types appear as arrays with associated masks

---

## Examples

### Basic SPMD Loop

```go
func sumArray(data []int) int {
    var total lanes.Varying[int]

    go for _, v := range data {
        total += v
    }

    return reduce.Add(total)
}
```

### Conditional Processing

```go
func processConditional(input []byte, threshold byte) []byte {
    result := make([]byte, len(input))

    go for i := range len(input) {
        var value lanes.Varying[byte] = input[i]

        if value > threshold {
            value = value * 2
        } else {
            value = value + 1
        }

        result[i] = value
    }

    return result
}
```

### Cross-Lane Communication

```go
func randomDecode(ascii []byte) []byte {
    output := make([]byte, 0, len(ascii)*3/4)

    go for _, chunk := range[4] ascii {  // Process in multiples of 4 bytes
        // Complex cross-lane operations
        sextets := lanes.Swizzle(lookupTable, chunk)
        shifted := lanes.ShiftLeft(sextets, shiftPattern)
        decoded := lanes.Rotate(shifted, 1)

        result := lanes.Swizzle(decoded, outputPattern)
        output = append(output, result...)
    }

    return output
}
```

---

This specification defines a complete extension to Go that enables high-performance data parallelism while maintaining the language's core principles of simplicity, readability, and backward compatibility.
