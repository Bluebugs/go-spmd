# SPMD Go - Quick Reference

**Fast syntax guide for Single Program Multiple Data extensions to Go**

## Enable SPMD

```bash
export GOEXPERIMENT=spmd
tinygo build -target=wasi -o program.wasm program.go
```

## Type Qualifiers

```go
// Basic types
var x uniform int      // Same value across all lanes (default)
var y varying int      // Different value per lane
var z int             // Implicitly uniform

// Constrained varying (multiples of n lanes)
var data varying[4] byte    // Lane count must be multiple of 4
var mask varying[8] bool    // Lane count must be multiple of 8

// Universal constrained (accepts any constraint)
func process(data varying[] int) { /* ... */ }

// Pointers
var ptrToVarying *varying int    // Pointer to varying data
var varyingPtrs varying *int     // Varying pointers (different per lane)
```

## SPMD Loops

```go
// Basic SPMD loop
go for i := range data {
    // i is varying: [0, 1, 2, 3] for 4-lane SIMD
    data[i] = process(data[i])
}

// Grouped processing (multiples of n)
go for i := range[4] data {
    // Process in groups of multiple of 4 elements
    result[i] = transform(data[i])
}

// Range over numbers
go for i := range 100 {
    // Process up to 100 elements in parallel chunks depending on SIMD register size
    output[i] = compute(i)
}
```

## Assignment Rules

```go
var u uniform int = 42
var v varying int

// ✓ ALLOWED
u = 10              // uniform int = uniform int
v = varying(20)     // varying int = varying int
v = u              // varying int = uniform int(broadcast)

// ✗ PROHIBITED
u = v              // ERROR: cannot assign varying to uniform
```

## Type Casting

```go
// ✓ DOWNCASTING (larger → smaller) - ALLOWED
var large varying int64 = varying(0x123456789ABCDEF0)
var small varying int32 = varying int32(large)  // Truncates

var wide varying uint32 = varying(0x12345678)
var narrow varying uint16 = varying uint16(wide)  // Truncates

var double varying float64 = varying(3.141592653589793)
var single varying float32 = varying float32(double)  // Precision loss

// ✗ UPCASTING (smaller → larger) - PROHIBITED
var small varying uint16 = varying(0x1234)
var large varying uint32 = varying uint32(small)  // ERROR: exceeds register capacity
```

## Built-in Functions

### Lane Information

```go
lanes.Count(int{})     // Returns SIMD width (e.g., 4 for WASM128)
lanes.Index()          // Current lane [0,1,2,3] - SPMD context only
```

### Cross-Lane Operations

```go
// Broadcast from specific lane to all lanes
result := lanes.Broadcast(data, 0)  // Lane 0 value to all

// Rotate values across lanes
rotated := lanes.Rotate(data, 1)    // Each lane gets previous lane's value

// Arbitrary permutation
indices := lanes.From([4]int{3, 2, 1, 0})
swizzled := lanes.Swizzle(data, indices)

// Bit operations (integer types only)
shifted := lanes.ShiftLeft(data, lanes.From([4]int{1, 2, 3, 4}))
```

### Data Construction

```go
// Create varying from array
lookupTable := []byte{1, 2, 4, 8, 16, 32, 64, 128}
varyingLUT := lanes.From(lookupTable)
```

## Reduction Operations

```go
// Arithmetic
total := reduce.Add(values)      // Sum all lanes
maximum := reduce.Max(values)    // Maximum across lanes
minimum := reduce.Min(values)    // Minimum across lanes

// Boolean
allTrue := reduce.All(conditions)   // true if all lanes true
anyTrue := reduce.Any(conditions)   // true if any lane true

// Bitwise (integer types)
combined := reduce.Or(flags)     // Bitwise OR across lanes
masked := reduce.And(values)     // Bitwise AND across lanes
xored := reduce.Xor(values)      // Bitwise XOR across lanes

// Conversion
array := reduce.From(varying_data)  // Convert to []T for debugging

// Analysis
firstSet := reduce.FindFirstSet(conditions)  // Index of first true (-1 if none)
bitmask := reduce.Mask(conditions)          // Convert bool vector to bitmask int[] where length of the array depends on how many lanes are available in the current SPMD context
```

## Control Flow

### If Statements

```go
go for i := range data {
    if data[i] > threshold {    // Varying condition
        data[i] *= 2           // Only active lanes execute
    } else {
        data[i] += 1          // Complementary lanes execute
    }
}
```

### Continue (Allowed)

```go
go for i := range data {
    if data[i] < 0 {
        continue              // ✓ ALLOWED: Uses masking
    }
    data[i] = process(data[i])
}
```

### Break (Prohibited)

```go
go for i := range data {
    if data[i] > limit {
        break                 // ✗ ERROR: break not allowed in go for
    }
}
```

### Early Loop Termination

```go
go for i := range data {
    // Use reduction for uniform early exit
    if reduce.Any(data[i] > limit) {
        break                 // Still ERROR in go for
    }
    
    // Correct approach: use continue with masking
    if data[i] > limit {
        continue              // ✓ Per-lane termination
    }
}
```

## Function Types

### SPMD Functions (varying parameters)

```go
// Private SPMD function - has varying parameters
func process(data varying int) varying int {
    lane := lanes.Index()     // ✓ ALLOWED: SPMD context from varying param
    return data + lane
}

// ✗ PROHIBITED: Public SPMD functions not allowed (except builtin)
func Process(data varying int) varying int {  // ERROR: public varying param
    return data * 2
}
```

### Non-SPMD Functions (no varying parameters)

```go
// Can return varying but can't use lanes.Index()
func createVarying() varying int {
    // lane := lanes.Index()   // ✗ ERROR: no SPMD context
    return varying(42)         // ✓ Returns uniform broadcast to all lanes
}
```

## Advanced Features

### Goroutines with Varying

```go
go for i := range data {
    var results varying int = compute(data[i])
    
    // Single goroutine with all lane values
    go func(values varying int) {
        // This function becomes SPMD due to varying parameter
        processAsync(values)
    }(results)
}
```

### Defer with Varying

```go
go for i := range data {
    var temp varying int = allocate(data[i])
    
    // Capture varying value and execution mask
    defer func(allocated varying int) {
        cleanup(allocated)  // Executes with captured mask
    }(temp)
}
```

### Type Switches

```go
func handleDynamic(value interface{}) {
    switch v := value.(type) {
    case varying int:           // ✓ Explicit varying type
        result := v * 2
    case varying[4] byte:       // ✓ Constrained varying
        sum := v[0] + v[1] + v[2] + v[3]
    case int:                   // ✓ Uniform type
        fmt.Printf("Uniform: %d\n", v)
    }
}
```

## Error Handling

### Panic/Recover with Varying

```go
go for i := range data {
    defer func() {
        if r := recover(); r != nil {
            // r may contain varying panic data
            handleError(r)
        }
    }()
    
    if data[i] < 0 {
        panic(varying(42))  // ✓ Can panic with varying
    }
}
```

## Map Restrictions

```go
// ✗ PROHIBITED: Varying keys
var badMap map[varying string]int       // ERROR: varying keys not allowed

go for i := range data {
    key := varying(strconv.Itoa(i))
    value := someMap[key]               // ERROR: varying key access
}

// ✓ ALLOWED: Uniform keys, varying values
var goodMap map[string]varying int     // OK: uniform keys only

go for i := range data {
    uniformKey := "key" + strconv.Itoa(i)  // Must be uniform
    goodMap[uniformKey] = varying(data[i])  // OK
}
```

## Printf Integration

```go
var values varying int = 42
fmt.Printf("Values: %v\n", values)      // Auto-converts to [42, 42, 42, 42]

var data varying float32 = 3.14
fmt.Printf("Data: %v\n", data)          // Works with any numeric type
```

## Common Patterns

### Parallel Array Processing

```go
func transform(input []float32) []float32 {
    output := make([]float32, len(input))
    
    go for i := range input {
        output[i] = input[i] * 2.0
    }
    
    return output
}
```

### Conditional Processing with Early Exit

```go
func findFirst(data []byte, target byte) int {
    go for i := range data {
        if reduce.Any(data[i] == target) {
            return reduce.FindFirstSet(data[i] == target)
        }
    }
    return -1
}
```

### Cross-Lane Coordination

```go
func base64Decode(ascii []byte) []byte {
    output := make([]byte, 0, len(ascii)*3/4)
    
    go for _, chunk := range[4] ascii {  // Process 4 bytes at a time
        // Complex cross-lane operations
        sextets := lanes.Swizzle(lookupTable, chunk)
        rotated := lanes.Rotate(sextets, 1)
        decoded := lanes.Swizzle(rotated, outputPattern)
        
        output = append(output, decoded...)
    }
    
    return output
}
```

## Build and Test

```bash
# Build with SPMD enabled
GOEXPERIMENT=spmd tinygo build -target=wasi -o program.wasm program.go

# Build SIMD vs scalar versions
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o program-simd.wasm program.go
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o program-scalar.wasm program.go

# Run with wasmer
go run wasmer-runner.go program.wasm

# Verify SIMD instructions
wasm2wat program-simd.wasm | grep "v128"
```

## Common Errors

```go
// ✗ Assignment errors
uniform_var = varying_var               // cannot assign varying to uniform

// ✗ Control flow errors  
go for i := range data { break }        // break not allowed in go for
go for i := range data {                // nesting not allowed
    go for j := range other { }
}

// ✗ Context errors
func regular() { lanes.Index() }        // lanes.Index() needs SPMD context

// ✗ Type errors
func Public(v varying int) { }          // public functions can't have varying params
someMap[varying_key] = value            // varying keys not allowed in maps

// ✗ Casting errors
varying uint32(small_varying_uint16)    // upcasting not allowed
```

---

**See also**: [GLOSSARY.md](GLOSSARY.md) for detailed term definitions, [SPECIFICATIONS.md](SPECIFICATIONS.md) for complete language specification
