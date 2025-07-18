# CLAUDE.md - SPMD Implementation for Go via TinyGo

## Project Overview

This workspace implements Single Program Multiple Data (SPMD) support for Go, similar to Intel's ISPC and Mojo's SIMD capabilities. The implementation extends TinyGo (which uses LLVM) rather than the main Go compiler, targeting WebAssembly SIMD as the initial backend. This is a rough proof of concept.

## Key Concepts

### SPMD Programming Model

- **SPMD**: All lanes execute the same program but on different data elements
- **Uniform**: Values that are the same across all SIMD lanes (scalars)
- **Varying**: Values that differ across SIMD lanes (vectors)
- **Execution Mask**: Tracks which lanes are active during control flow
- **lanes.Count(varying any v)**: Number of SIMD lanes for a specific type (e.g., 4 for WASM 128-bit SIMD), know at compile time
- **lanes.Index()**: Current lane index (0 to lanes.Count()-1) in the current SPMD context
- **lanes**: New Golang standard library module providing cross lane functions
- **reduce**: New Golang standard library module providing reduction operation converting varying to uniform

### Go SPMD Syntax Extensions

```go
// Type qualifiers
var x uniform int      // Scalar value, same across lanes
var y varying float32  // Vector value, different per lane

// SPMD loop construct
go for i := range 16 {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [i, i+1, i+2, i+3]
}

// Builtins
lanes.Count(any) uniform int   // Returns SIMD width (e.g., 4)
lanes.Index()   // Returns current lane [0,1,2,3]

// Cross-lane operations
lanes.Broadcast(value, lane)  // Broadcast from one lane to all
lanes.Rotate(value, offset)   // Rotate values across lanes
lanes.Shuffle(value, indices) // Arbitrary permutation
```

## Implementation Architecture

### Phase 1: Go Frontend Changes

- Extend lexer with `uniform`, `varying` keywords
- Parse `go for` as SPMD loop construct (not goroutine)
- Add SPMD builtins to universe
- Type checking for uniform/varying rules
- Mask propagation through control flow

### Phase 2: SSA Extensions

- Add a pass propagating and resolving call to lanes.Count with placeholder values until phase 3
- Add SPMD type information to SSA
- New opcodes: OpSPMDLoop, OpSPMDBroadcast, OpSPMDMaskAnd, etc.
- Track execution mask through SSA

### Phase 3: TinyGo LLVM Backend

- Provide backend information on varying size to the AST needed by lanes.Count placeholder
- Map varying types to LLVM vector types
- Generate LLVM vector instructions
- Handle mask operations for control flow
- Target WASM SIMD128 instructions

## Critical Implementation Rules

### Type System Rules

1. **Assignment Rule**: Varying values cannot be assigned to uniform variables
2. **Implicit Broadcast**: Uniform values when needed are automatically broadcast, but preserved for as long as possible as uniform
3. **Control Flow**: All control flow (if/for/switch) can use varying conditions via masking
4. **Exception**: `select` statements cannot use varying values (compile error)
5. **Break Restriction**: `break` statements are not allowed in `go for` loops (compile error)

### Function Semantics

1. Functions with varying parameters are "SPMD functions"
2. SPMD functions receive an implicit mask parameter
3. SPMD functions carry mask around all operations
4. Return behavior:
   - No varying params → returns unmasked varying
   - Has varying params → returns masked varying
5. Varying can be passed as `interface{}`/`any`
   - Reflect exposes as uniform arrays (mask + values)
   - Functions taking only `any` are not SPMD functions

### Control Flow Masking

#### Basic Conditional Masking

```go
// Original code
if varyingCond {
    // true branch
} else {
    // false branch
}

// Transformed execution in the SSA phase
trueMask = currentMask & varyingCond
falseMask = currentMask & ~varyingCond
// Execute both branches with respective masks
```

#### For Loop Masking

```go
var t varying bool

// Original code
for i := range 10 {
    if t && (i%2 == 0) {
        continue
    }
    if t && (i > 7) {
        break
    }
    if reduce.All(t) {
        continue
    }
    if reduce.Any(t) {
        break
    }
    // loop body
}

// Transformed execution with mask tracking
var loopMask varying bool = currentMask
var continueMask varying bool = false  // per-lane continue flags
var breakMask varying bool = false     // per-lane break flags

for iteration := 0; iteration < 10; iteration++ { // iteration is an uniform and so has same value in all lanes
    // Update loop condition for active lanes
    activeMask := loopMask & ~breakMask

    if !reduce.Any(activeMask) {
        break // All lanes done
    }
    
    // Execute loop body with active mask
    bodyMask := activeMask & ~continueMask
    
    // Handle continue statement
    if t && (i%2 == 0) {
        continueMask |= bodyMask & (i%2 == 0)
        bodyMask &= ~(i%2 == 0)
    }
    
    // Handle break statement  
    if t && (i > 7) {
        breakMask |= bodyMask & (i > 7)
        bodyMask &= ~(i > 7)
    }

    // The following to tests show the benefit of operating on uniform in a SPMD context, it allow for early loop continue/break behavior
    if reduce.All(t) {
        continue
    }
    if reduce.Any(t) {
        break
    }
    
    // Execute remaining loop body with final mask
    executeWithMask(bodyMask, /* loop body */)
    
    // Reset continue mask for next iteration
    continueMask = false
}
```

#### SPMD `go for` Loop Masking

```go
// Original SPMD code - note: break statements not allowed in go for loops
var data [16]int
go for i := range 16 { // i is a varying with different value in each lane
    if data[i] > threshold { // data[i] is a varying as lanes amount of data are fetched each time at position specified by i
        continue
    }
    process(data[i]) // process is receiving the varying data directly with the lane masked according to the previous tests
}

// Transformed execution
lanes := lanes.Count(16)        // e.g., 4 int for WASM128 (uniform)
var continueMask varying bool = false  // per-lane continue flags

for iteration := 0; iteration < 16; iteration += lanes {
    // Calculate lane indices: [iteration, iteration+1, ...]
    laneIndices := iteration + lanes.Index()

    // Bounds check
    validMask := (laneIndices < 16)
    if !reduce.Any(validMask) {
        break // All lanes out of bounds
    }
    
    // Load data for current lanes
    laneData = lanes.Load(data, laneIndices, validMask) // In practice we won't provide a Load function, but directly map it in the AST and then SSA correctly
    
    // Handle continue condition
    continueMask = validMask & (laneData > threshold)
    processMask = validMask & ~continueMask
    
    // Execute loop body for remaining active lanes
    if reduce.Any(processMask) {
        processedData = process(processMask /* implicitly added as the function is taking varying */, laneData)
    }
}
```

#### Nested Loop Masking

```go
var matrix [4][4]varying int
var sum varying int
var limit varying int
var allDone [4]varying bool

// Original nested loops - note: break statements not allowed in outer go for loop
go for i := range 4 {
    for j := range 4 {
        if matrix[i][j] == 0 {
            continue  // Inner continue
        }
        if sum > limit {
            break     // Inner break (allowed in regular for loop)
        }
        sum += matrix[i][j]
    }
    // Note: break statements not allowed in go for loops
    // if allDone[i] { break } // COMPILE ERROR - breaks not allowed in go for
}

// Transformed execution with SPMD outer loop
lanes := lanes.Count(4)                    // e.g., 4 for WASM128 (uniform)

for outerIteration := 0; outerIteration < 4; outerIteration += lanes {
    // Calculate outer lane indices: [outerIteration, outerIteration+1, ...]
    outerLaneIndices := outerIteration + lanes.Index()
    
    // Bounds check for outer loop
    outerValidMask := (outerLaneIndices < 4)
    if !reduce.Any(outerValidMask) {
        break // All lanes out of bounds for outer loop
    }
    
    // Inner loop - traditional for loop in SPMD context
    var innerMask varying bool = outerValidMask
    var innerBreakMask varying bool = false
    
    for innerIter := 0; innerIter < 4; innerIter++ { // innerIter is uniform
        innerActiveMask := innerMask & ~innerBreakMask
        var innerContinueMask varying bool = false
        
        if !reduce.Any(innerActiveMask) {
            break // All lanes done with inner loop
        }
        
        // Execute inner body with active mask
        innerBodyMask := innerActiveMask & ~innerContinueMask
        
        // Load matrix data for current outer lane indices and inner index
        matrixData := lanes.Load(matrix, outerLaneIndices, innerIter, innerBodyMask)
        
        // Handle inner continue
        innerContinueMask = innerBodyMask & (matrixData == 0)
        innerBodyMask &= ~innerContinueMask
        
        // Handle inner break
        innerBreakCond := innerBodyMask & (sum > limit)
        innerBreakMask |= innerBreakCond
        innerBodyMask &= ~innerBreakCond
        
        // Execute remaining inner body with final mask
        if reduce.Any(innerBodyMask) {
            sum += matrixData // sum is varying, operates per-lane with mask
        }
    }
}
```

#### Key Masking Principles

1. **Continue**: Sets temporary mask for current iteration, reset on next iteration
2. **Break**: Sets permanent mask that persists for remainder of loop
3. **Break Restriction**: Break statements are NOT allowed in `go for` loops (SPMD loops)
4. **Nested Breaks**: Inner breaks affect inner loop only, outer breaks affect all enclosing loops
5. **SPMD go for**: Processes multiple elements per iteration, mask tracks per-lane state
6. **Early Termination**: Loop exits when no lanes remain active (`!reduce.Any(activeMask)`)
7. **Mask Inheritance**: Inner scopes inherit masks from outer scopes via logical AND

## Reference Materials

### Blog Posts (in workspace)

- `bluebugs.github.io/content/blogs/go-data-parallelism.md`: SPMD concept introduction
- `bluebugs.github.io/content/blogs/practical-vector.md`: Practical and SIMD patterns
- `bluebugs.github.io/content/blogs/cross-lane-communication.md`: Cross-lane operation details with base64 Decode example
- `bluebugs.github.io/content/blogs/go-spmd-ipv4-parser.md`: Real-world IPv4 parser SPMD example

### Key Source References

- **ISPC**: `ispc/src/` - Reference implementation for SPMD concepts
  - `parser.yy`: Grammar for SPMD constructs
  - `type.cpp`: Type system with uniform/varying
  - `ctx.cpp`: Mask handling and control flow
  - `stmt.cpp`: Statement code generation
- **Go**: `golang/go/src/cmd/compile/` - Frontend to extend
  - `internal/syntax/`: Lexer and parser
  - `internal/types2/`: Type checking
  - `internal/ssagen/`: SSA generation
- **TinyGo**: `tinygo/` - LLVM-based Go compiler
  - `compiler/`: LLVM IR generation
  - `transform/`: Optimization passes
- **SPMD Go**: `bluebugs.github.io/examples` - Preliminary SPMD Go example from the blog

## Development Workflow

### For Each Commit

1. Make focused changes addressing one aspect
2. Ensure code compiles at each step
3. Add tests immediately:
   - Parser tests for syntax changes
   - Type checker tests for semantic rules
   - SSA tests for correct opcode generation
4. Reference ISPC implementation for SPMD-specific patterns

### Testing Strategy

- **Parser Tests**: Valid/invalid syntax recognition
- **Type Tests**: Uniform/varying rules, SPMD function marking
- **SSA Tests**: Verify vector operations and mask handling
- **End-to-end**: WASM output with actual SIMD instructions

### Common Pitfalls to Avoid

1. Don't confuse `go for` (SPMD) with `go func()` (goroutine)
2. Remember mask propagation through nested control flow
3. Ensure varying operations generate vector LLVM IR
4. Prevent LLVM from scalarizing vector operations
5. Handle edge cases like varying array indices

## Current Implementation Status

Starting fresh - no commits yet. Follow the implementation plan in order:

1. Lexer/parser changes first
2. Type system and validation
3. SSA generation
4. TinyGo LLVM backend
5. WASM target configuration
6. Testing and examples

## Success Criteria

The implementation succeeds when:

1. `go for` loops generate WASM SIMD instructions
2. Varying arithmetic uses vector operations
3. Control flow with varying conditions works via masking
4. Performance improvement demonstrated on examples
5. Clean error messages for invalid SPMD code

## Debugging Tips

- Add `-d=ssa/all/dump` flag to see SSA generation
- Use `wasm2wat` to verify SIMD instructions in output
- Check LLVM IR for vector types and operations
- Verify mask propagation with control flow tests
- Compare against ISPC's generated code for similar patterns
