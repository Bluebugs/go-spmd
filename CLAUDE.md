# CLAUDE.md - SPMD Implementation for Go via TinyGo

## Project Overview

This workspace implements Single Program Multiple Data (SPMD) support for Go, similar to Intel's ISPC and Mojo's SIMD capabilities. The implementation extends TinyGo (which uses LLVM) rather than the main Go compiler, targeting WebAssembly SIMD128 as the proof of concept backend.

**Proof of Concept Scope:**

- **Frontend**: Full lexer, parser, and type checker support for SPMD constructs
- **Backend**: TinyGo LLVM integration with dual code generation: SIMD128 and scalar fallback
- **Standard Library**: `lanes` and `reduce` packages with all functions needed for examples
- **Testing**: wasmer-go runtime execution with SIMD detection and performance benchmarking
- **Goal**: Compile ALL examples to both SIMD and scalar WASM, enabling browser-side SIMD detection and performance comparison

## Key Concepts

### SPMD Programming Model

- **SPMD**: All lanes execute the same program but on different data elements
- **Uniform**: Values that are the same across all SIMD lanes (scalars)
- **Varying**: Values that differ across SIMD lanes (vectors)
- **Constrained Varying**: `varying[n]` syntax specifies constraint `n` (hardware-independent, compiler handles via unrolling/masking)
- **Execution Mask**: Tracks which lanes are active during control flow
- **lanes.Count(varying any v)**: Number of SIMD lanes for a specific type (e.g., 4 for WASM 128-bit SIMD), known at compile time
- **lanes.Index()**: Current lane index (0 to lanes.Count()-1) in the current SPMD context
- **lanes**: New Golang standard library module providing cross lane functions
- **reduce**: New Golang standard library module providing reduction operations (varying to uniform) and type conversion (varying to array)
- **Printf Integration**: `fmt.Printf` with `%v` automatically converts varying types to arrays for display

### Go SPMD Syntax Extensions

```go
// Type qualifiers
var x uniform int      // Scalar value, same across lanes
var y varying float32  // Vector value, different per lane

// Constrained varying - hardware-independent constraints
var data varying[4] byte   // Constraint 4 (compiler handles implementation)
var mask varying[8] bool   // Constraint 8 (compiler handles implementation)

// SPMD loop construct
go for i := range 16 {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [i, i+1, i+2, i+3]
}

// Constrained SPMD loop - process in groups of n
go for i := range[4] 16 {
    // Process 4 elements at a time per iteration
    // Useful for algorithms with specific data relationships
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

### GOEXPERIMENT Flag Integration

SPMD support is implemented as an experimental feature behind `GOEXPERIMENT=spmd`:

1. **Add SPMD flag to `internal/goexperiment/flags.go`**:

   ```go
   // SPMD enables Single Program Multiple Data extensions
   // including uniform/varying type qualifiers and go for loops
   SPMD bool
   ```

2. **Feature Gating in Compiler**:
   - All SPMD syntax parsing gated behind `buildcfg.Experiment.SPMD`
   - Lexer context-sensitive keyword recognition only when enabled
   - Type checker SPMD rules only active with flag
   - SSA generation conditional on experiment

3. **Build Tags**: Generated files use `//go:build goexperiment.spmd` constraints

### Phase 1: Go Frontend Changes

- Extend lexer with `uniform`, `varying` keywords (gated by GOEXPERIMENT)
- Make sure existing use of word `uniform` and `varying` still are permitted (gated by GOEXPERIMENT)
- Parse `go for` as SPMD loop construct (gated by GOEXPERIMENT)
- Add SPMD builtins to universe (conditional)
- Type checking for uniform/varying rules (gated)
- Mask propagation through control flow

### Phase 2: SSA Extensions

- Add a pass propagating and resolving call to lanes.Count with placeholder values until phase 3
- Add SPMD type information to SSA (varying vs uniform metadata)
- Convert `go for` loops directly to standard SSA with masking operations
- Generate mask tracking through existing SSA opcodes (OpAnd, OpOr, OpNot)
- Use vector types for varying values in SSA representation
- Track execution mask context through function parameters and local variables

### Phase 3: TinyGo LLVM Backend

- Provide backend information on varying size to the AST needed by lanes.Count placeholder
- Convert AST after type checking to SSA
- Map varying types to LLVM vector types (i32x4, f32x4 for WASM SIMD128)
- Generate standard LLVM vector instructions from SSA
- Handle masking through LLVM select instructions and conditional branches
- Target WASM SIMD128 instructions directly without custom opcodes

## SSA Generation Strategy (Following ISPC's Approach)

Based on ISPC's proven methodology, Go SPMD implementation generates SSA that directly maps to LLVM IR operations:

### Direct Vector SSA Generation

Instead of custom opcodes, use standard SSA operations with vector types:

```go
// Original Go SPMD code
var data [16]varying int32
go for i := range 16 {
    data[i] = i * 2
}

// Generated SSA (conceptual)
%lanes = OpConst <int> 4                    // WASM128 SIMD width
%indices = OpMakeSlice <[]int32> %lanes     // [0,1,2,3]
%multiplier = OpConst <int32> 2
%doubled = OpMul <%4 x int32> %indices %multiplier
%mask = OpConst <%4 x bool> [true,true,true,true]
OpVectorStore %data %doubled %mask
```

### SPMD Function Signature Generation

SPMD functions receive mask as **first parameter** in SSA:

```go
// Go source
func process(data varying int32) varying int32 {
    return data * 2
}

// Generated SSA signature (mask first!)
func @process(mask <%4 x bool>, data <%4 x int32>) <%4 x int32> {
    %multiplier = OpConst <%4 x int32> [2,2,2,2]
    %result = OpMul <%4 x int32> %data %multiplier
    %maskedResult = OpSelect <%4 x int32> %mask %result %zero
    OpReturn %maskedResult
}
```

### Mask Propagation Through Standard SSA

Track execution masks using regular SSA variables and operations:

```go
// Go SPMD conditional
var cond varying bool
if cond {
    // true branch  
} else {
    // false branch
}

// Generated SSA with mask tracking
%currentMask = OpPhi <%4 x bool> ...
%trueMask = OpAnd <%4 x bool> %currentMask %cond
%falseMask = OpAndNot <%4 x bool> %currentMask %cond

// True branch basic block
%trueResult = OpSelect <%4 x int32> %trueMask %trueValue %defaultValue

// False branch basic block  
%falseResult = OpSelect <%4 x int32> %falseMask %falseValue %defaultValue

// Merge results
%finalResult = OpOr <%4 x int32> %trueResult %falseResult
```

### Function Call Transformation

All SPMD function calls get mask as first argument:

```go
// Go source call
result := process(data)

// Generated SSA call
%result = OpCall <%4 x int32> @process %currentMask %data
```

### Key Advantages of This Approach

1. **No Custom Opcodes**: Uses standard SSA operations that LLVM understands natively
2. **Clear Mask Threading**: Explicit mask-first parameter makes mask flow obvious
3. **Incremental Implementation**: Can implement piece by piece without disrupting existing compiler
4. **Debugging Friendly**: Standard SSA passes work without modification
5. **Backend Agnostic**: Same SSA works for different LLVM targets
6. **Optimization Ready**: LLVM's vector optimizations work automatically

## Critical Implementation Rules

### Type System Rules

1. **Assignment Rule**: Varying values cannot be assigned to uniform variables
2. **Implicit Broadcast**: Uniform values when needed are automatically broadcast, but preserved for as long as possible as uniform
3. **Control Flow**: All control flow (if/for/switch) can use varying conditions via masking in SPMD context
4. **Select Support**: `select` statements can use channels carrying varying values
5. **Return/Break Control Flow Rules**: `return` and `break` statements in `go for` loops follow ISPC's proven approach with mask alteration tracking:
   - **Uniform Conditions**: Return/break statements are **allowed** when all enclosing `if` statements within the `go for` loop have uniform conditions AND no mask alteration has occurred
   - **Varying Conditions**: Return/break statements are **forbidden** when any enclosing `if` statement has a varying condition
   - **Mask Alteration**: Return/break statements are **forbidden** after any `continue` statement in a varying context, even if subsequent conditions are uniform
   - **Rationale**: Continue statements in varying contexts alter the execution mask, making subsequent uniform conditions affect only a subset of lanes
   - **Continue**: Always allowed in `go for` loops regardless of condition type or mask alteration state
6. **Nesting Restriction**: `go for` loops cannot be nested within other `go for` loops (enforced in type checking phase)
7. **SPMD Function Restriction**: Functions with varying parameters cannot contain `go for` loops (compile error)
8. **Public API Restriction**: Only private functions can have varying parameters (except builtin lanes/reduce functions)

### SPMD Control Flow Restrictions Enforcement Strategy

Return/break statement restrictions (following ISPC's approach) and nested `go for` loop restrictions are enforced during the **type checking** phase in `/src/cmd/compile/internal/types2/stmt.go`. The implementation tracks varying control flow depth to determine if return/break statements are allowed.

#### Implementation Location: Type Checker

```go
// Add new SPMD context flags to existing stmtContext flags
const (
    breakOk stmtContext = 1 << iota
    continueOk
    fallthroughOk
    inSPMDFor                        // inside SPMD go for loop - NEW
    varyingControlFlow               // inside varying if statement - NEW
    finalSwitchCase
    inTypeSwitch
)

// Track varying control flow depth for SPMD loops (following ISPC approach)
type SPMDControlFlowInfo struct {
    inSPMDLoop        bool
    varyingDepth      int  // depth of nested varying if statements
    maskAltered       bool // true if any continue in varying context has occurred
}

// Modify existing break/return validation in BranchStmt case (around line 554)
case syntax.Break:
    if ctxt&inSPMDFor != 0 && (check.spmdInfo.varyingDepth > 0 || check.spmdInfo.maskAltered) {
        if check.spmdInfo.maskAltered {
            check.error(s, InvalidSPMDBreak, "break statement not allowed after continue in varying context in SPMD for loop")
        } else {
            check.error(s, InvalidSPMDBreak, "break statement not allowed under varying conditions in SPMD for loop")
        }
    } else if ctxt&breakOk == 0 {
        check.error(s, MisplacedBreak, "break not in for, switch, or select statement")
    }

case syntax.Return:
    if ctxt&inSPMDFor != 0 && (check.spmdInfo.varyingDepth > 0 || check.spmdInfo.maskAltered) {
        if check.spmdInfo.maskAltered {
            check.error(s, InvalidSPMDReturn, "return statement not allowed after continue in varying context in SPMD for loop")
        } else {
            check.error(s, InvalidSPMDReturn, "return statement not allowed under varying conditions in SPMD for loop")
        }
    }

case syntax.Continue:
    // Track mask alteration when continue occurs in varying context
    if ctxt&inSPMDFor != 0 && check.spmdInfo.varyingDepth > 0 {
        check.spmdInfo.maskAltered = true
    }

// Track varying control flow in if statements
func (check *Checker) processIfStatement(stmt *syntax.IfStmt, ctxt stmtContext) {
    if ctxt&inSPMDFor != 0 {
        testType := stmt.Cond.GetType()
        if testType != nil && testType.IsVaryingType() {
            check.spmdInfo.varyingDepth++
            defer func() { check.spmdInfo.varyingDepth-- }()
        }
    }
    // Continue with normal if statement processing...
}

// Set context when processing SPMD ForStmt (around line 667)
if s.IsSpmd {  // ForStmt needs IsSpmd field from parser
    // Check for nested go for loops
    if ctxt&inSPMDFor != 0 {
        check.error(s, InvalidNestedSPMDFor, "nested go for loops not allowed")
    }
    inner |= continueOk | inSPMDFor  // allow continue, track SPMD context
    // Note: breakOk is conditionally set based on varying control flow depth
} else {
    inner |= breakOk | continueOk    // regular for loop
}
```

#### Why Type Checking vs Parsing

- **Context Awareness**: Type checker tracks statement context and nested scopes
- **Labeled Breaks**: Handles `break label` where label targets SPMD loop
- **Nested Validation**: Correctly forbids breaks in switch/if inside SPMD loops and nested `go for` loops
- **Consistent Pattern**: All control flow restrictions are enforced in type checker

#### Prerequisites

1. **Parser Changes**: `ForStmt` needs `IsSpmd` field to distinguish `go for` from regular `for`
2. **Syntax Recognition**: Parser must recognize `go for` syntax and set SPMD flag
3. **Error Definitions**: Add `InvalidSPMDBreak`, `InvalidSPMDReturn`, and `InvalidNestedSPMDFor` to error types in `internal/types/errors`

#### Test Coverage

SPMD control flow restrictions must be validated for:

- **Return/Break Control Flow Rules** (following ISPC approach with mask alteration):
  - **Allowed Cases**: Return/break statements in `go for` loops under uniform conditions only, with no prior mask alteration
  - **Forbidden Cases**: Return/break statements under varying conditions (any enclosing varying if)
  - **Mask Alteration Cases**: Return/break statements forbidden after continue in varying context, even under subsequent uniform conditions
  - **Continue**: Always allowed in `go for` loops regardless of condition type or mask alteration state
  - **Mixed Nesting**: Complex combinations of uniform and varying conditions with mask alteration tracking

- **Uniform Condition Examples** (allowed):
  - Direct return/break in `go for` without any conditions
  - Return/break under `if uniformVar` conditions
  - Return/break under `if uniformFunc()` conditions

- **Varying Condition Examples** (forbidden):
  - Return/break under `if varyingVar` conditions
  - Return/break nested inside varying if statements
  - Return/break where any enclosing if has varying condition

- **Mask Alteration Examples** (forbidden):
  - Continue in varying context followed by return/break under uniform condition
  - Mixed: `if varying { continue }; if uniform { return }` - return forbidden due to prior mask alteration
  - Complex: varying condition → continue → uniform condition → return/break (forbidden)

- **Nesting Restrictions**:
  - Direct nested `go for` loops (forbidden)
  - `go for` loops inside regular for loops (allowed)
  - Regular for loops inside `go for` loops (allowed)
  - Complex nesting patterns with mixed control flow

**Test Location**: All SPMD control flow restriction tests should be in `/src/cmd/compile/internal/types2/testdata/spmd/` since these are semantic (type checking) errors, not syntax errors.

### Function Semantics

1. Functions with varying parameters are "SPMD functions"
2. SPMD functions receive an implicit mask parameter **as the first parameter** in SSA
3. SPMD functions carry mask around all operations
4. Return behavior:
   - No varying params → returns unmasked varying
   - Has varying params → returns masked varying
5. Varying can be passed as `interface{}`/`any`
   - Reflect exposes as uniform arrays (mask + values) (for now)
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
        // This trigger a continue on all lanes at once and jump to restart the loop as if the loop wasn't SPMD
        continue
    }
    if reduce.Any(t) {
        // This trigger a break on all lanes at once, terminating the loop as if the loop wasn't SPMD
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
// Original SPMD code - return/break allowed when all lanes active
var data [16]int
var threshold uniform int
go for i := range 16 { // i is a varying with different value in each lane
    // Uniform condition - all lanes remain active, return/break allowed
    if threshold < 0 {
        return  // ALLOWED: uniform condition, all lanes agree
    }
    
    // Varying condition - creates partial mask, return/break forbidden
    if data[i] > threshold { // data[i] is varying - some lanes may continue, others skip
        continue  // continue still allowed
        // return would be FORBIDDEN here - not all lanes active
    }
    process(data[i]) // process receives varying data with lane masking
}

// Transformed execution
lanes := lanes.Count(16)        // e.g., 4 int for WASM128 (uniform)
var continueMask varying bool = false  // per-lane continue flags

for iteration := 0; iteration < 16; iteration += lanes {
    // Calculate lane indices: [iteration, iteration+1, ...]
    laneIndices := iteration + lanes.Index()

    // Bounds check
    validMask := (laneIndices < 16)

    // The code doesn't use reduce, so the compiler won't introduce it automatically
    
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

// Original nested loops - return/break rules depend on lane activity
go for i := range 4 {
    // Top-level: all lanes active, return/break allowed for uniform conditions
    if errorCondition { // uniform condition
        return  // ALLOWED: all lanes active, uniform decision
    }
    
    for j := range 4 {
        if matrix[i][j] == 0 {
            continue  // Inner continue (always allowed)
        }
        if sum > limit {
            break     // Inner break (allowed in regular for loop)
        }
        sum += matrix[i][j]
    }
    
    // Varying condition would create partial mask
    if allDone[i] { // varying condition - allDone[i] differs per lane
        // return would be FORBIDDEN here - not all lanes would return
        continue  // continue still allowed
    }
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
3. **Return/Break Rules**: Return/break statements are forbidden in `go for` loops generally when in varying/SPMD context, but are allowed in uniform context (The compiler has to be able to prove that a return/break are only inside uniform branch all the way to have them allowed)
4. **Continue Allowed**: Continue statements remain legal for per-lane loop control
5. **SPMD go for**: Processes multiple elements per iteration, mask tracks per-lane state
6. **Early Termination**: Loop exits when no lanes remain active (`!reduce.Any(activeMask)`)
7. **Mask Inheritance**: Inner scopes inherit masks from outer scopes via logical AND
8. **Alternative Patterns**: Use reduce operations and structured control flow instead of early exits

#### SPMD Return/Break Examples

The following examples demonstrate the ISPC-based return/break rules in `go for` loops:

```go
// Example 1: ALLOWED - Return/break under uniform conditions
func processDataWithUniformExit(data []int, threshold uniform int) {
    go for i := range len(data) {
        // ALLOWED: Uniform condition - all lanes make same decision
        if threshold < 0 {
            return  // OK: uniform condition, efficient direct return
        }
        
        // ALLOWED: Uniform function call condition
        if isErrorMode() { // uniform function
            break   // OK: uniform condition, efficient direct break
        }
        
        process(data[i])
    }
}

// Example 2: FORBIDDEN - Return/break under varying conditions
func processDataWithVaryingExit(data []int) {
    go for i := range len(data) {
        // FORBIDDEN: Varying condition creates partial mask
        if data[i] < 0 { // data[i] is varying - different per lane
            // return  // COMPILE ERROR: varying condition forbids return
            // break   // COMPILE ERROR: varying condition forbids break
            continue  // ALLOWED: continue always permitted
        }
        
        process(data[i])
    }
}

// Example 3: MIXED - Nested conditions follow deepest varying rule
func processWithNestedConditions(data []int, mode uniform int) {
    go for i := range len(data) {
        // Uniform outer condition
        if mode == DEBUG_MODE { // uniform condition
            // FORBIDDEN: Inner varying condition makes return/break illegal
            if data[i] > 100 { // varying condition - now in varying context
                // return  // COMPILE ERROR: enclosing varying condition
                // break   // COMPILE ERROR: enclosing varying condition
                continue  // ALLOWED: continue always permitted
            }
            
            // ALLOWED: Still under uniform condition only
            if mode == TRACE_MODE { // another uniform condition
                return  // OK: no varying conditions in scope
            }
        }
        
        process(data[i])
    }
}

// Example 4: PERFORMANCE - Compare uniform vs varying behavior
func demonstratePerformanceDifference(data []int, uniformThreshold uniform int) {
    // FAST: Uniform early exit - entire SIMD loop can terminate efficiently
    go for i := range len(data) {
        if uniformThreshold < 0 {
            return  // All lanes exit together - no mask tracking needed
        }
        process(data[i])
    }
    
    // SLOWER: Varying conditions require mask tracking throughout loop
    go for i := range len(data) {
        if data[i] < 0 { // varying condition - requires per-lane mask tracking
            continue // Must track which lanes continue vs process
        }
        process(data[i]) // Executed with mask for active lanes only
    }
}

// Example 5: MIGRATION - Convert old patterns to new approach
func searchPatternOldWay(data []varying byte, pattern uniform byte) uniform bool {
    // OLD: Set flags, use reduce operations
    var found varying bool = false
    
    go for i := range len(data) {
        if data[i] == pattern {
            found = true  // Set per-lane flag
            // Can't return here due to varying condition
        }
    }
    
    return reduce.Any(found)  // Reduce operation outside loop
}

func searchPatternNewWay(data []varying byte, pattern uniform byte, errorMode uniform bool) uniform bool {
    go for i := range len(data) {
        // NEW: Early uniform exit for error conditions
        if errorMode {
            return false  // ALLOWED: uniform early exit
        }
        
        if data[i] == pattern {
            // Still can't return here - varying condition
            return true  // COMPILE ERROR: varying condition forbids return
        }
    }
    
    return false
}

// Example 6: CORRECT USAGE - Structured approach with uniform exits
func processWithErrorHandling(data []int, params uniform ProcessParams) error {
    go for i := range len(data) {
        // Check uniform error conditions first - can exit immediately
        if params.AbortRequested {
            return ErrAborted  // ALLOWED: uniform condition
        }
        
        if params.Mode == INVALID_MODE {
            break  // ALLOWED: uniform condition
        }
        
        // Handle varying conditions without early exit
        if data[i] < 0 { // varying condition
            continue  // ALLOWED: continue to next iteration
        }
        
        if err := process(data[i]); err != nil {
            // Can't return here - would need to be handled differently
            markError(i, err)  // Log error, continue processing
            continue
        }
    }
    
    return nil
}
```

#### Return/Break Validation Rules

The type checker enforces ISPC-based rules during compilation:

1. **Uniform Condition Rule**: Return/break statements are **allowed** when all enclosing if statements have uniform conditions
2. **Varying Condition Rule**: Return/break statements are **forbidden** when any enclosing if statement has a varying condition
3. **Continue Always Allowed**: Continue statements remain legal for per-lane flow control in all cases
4. **Depth Tracking**: Implementation tracks varying control flow depth (following ISPC's `lHasVaryingBreakOrContinue` approach)

```go
// ISPC-based compile-time validation pseudocode
func validateReturnBreak(stmt Node, context TypeContext) {
    if !context.InSPMDFor() {
        return  // Normal Go rules apply
    }
    
    // ISPC approach: forbidden only under varying conditions
    if (stmt.IsReturn() || stmt.IsBreak()) && context.VaryingDepth() > 0 {
        error("return/break statements not allowed under varying conditions in go for loops")
    }
    
    // Continue always allowed
    if stmt.IsContinue() {
        // Always legal - no restrictions
    }
}

// Track varying control flow depth (following ISPC)
func processIfStatement(ifStmt *IfStmt, context *TypeContext) {
    if context.InSPMDFor() && ifStmt.Condition.Type().IsVaryingType() {
        context.IncrementVaryingDepth()
        defer context.DecrementVaryingDepth()
    }
    
    // Process if statement body with updated context
    processStatements(ifStmt.Body, context)
}
```

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

### Academic Papers

- **Predicated Static Single Assignment**: <https://cseweb.ucsd.edu/~calder/papers/PACT-99-PSSA.pdf>
  - Predicated SSA form for efficient control flow handling in SIMD architectures
  - Mask-based execution using predicates instead of control flow divergence
  - Relevant for SPMD mask propagation and conditional execution strategies

## Development Workflow

### TinyGo SPMD Development Setup

1. **Enable experiment for TinyGo**:

   ```bash
   export GOEXPERIMENT=spmd
   cd tinygo && go build  # Rebuild TinyGo with SPMD support
   ```

2. **Compile SPMD to WebAssembly**:

   ```bash
   GOEXPERIMENT=spmd tinygo build -target=wasi -o simple-sum.wasm examples/simple-sum/main.go
   ```

3. **Execute with wasmer-go**:

   ```bash
   go run wasmer-runner.go simple-sum.wasm
   ```

4. **Verify experiment gating**:

   ```bash
   # Should fail without experiment
   tinygo build -target=wasi examples/simple-sum/main.go
   ```

5. **Validate SIMD instruction generation**:

   ```bash
   # Inspect generated WASM for SIMD instructions
   wasm2wat simple-sum.wasm | grep "v128"
   ```

### Git Commit Guidelines

**All commits across the SPMD project must follow these strict rules:**

1. **Atomic Commits**: Each commit addresses exactly ONE logical change
   - One feature addition, one bug fix, one refactoring
   - Never mix unrelated changes in a single commit
   - Ensure each commit leaves the code in a working state

2. **Clear Scope and Goal**: Every commit has a specific, well-defined purpose
   - Add one parser rule, fix one type checker bug, implement one SSA opcode
   - Avoid vague changes like "various improvements" or "cleanup"
   - Each commit should be easily reviewable and understandable

3. **Concise Messages**: Commit messages are clear and informative
   - Start with imperative verb: "Add", "Fix", "Update", "Remove", "Implement"
   - One line summary (50 chars max), optional detailed description if needed
   - **NO EMOJIS EVER** - plain text only
   - Examples:
     - ✅ "Add uniform/varying keyword recognition to Go lexer"
     - ✅ "Fix SPMD loop type checking for constrained varying"
     - ✅ "Implement SIMD128 vector add instruction generation"
     - ❌ "🚀 Add cool SPMD features and fix some bugs ✨"

4. Maintain a `spmd` branch for each fork with all the commit properly lined up in them

5. **Repository Consistency**: Apply these rules to ALL repositories
   - Main Go compiler changes (golang/go fork)
   - TinyGo compiler changes (tinygo fork)
   - SPMD workspace documentation updates
   - Example code additions and modifications

6. Update README.md and PLAN.md according to progress.

### For Each Commit

1. **Single Focus**: Make focused changes addressing exactly one aspect
2. **Build Verification**: Ensure TinyGo compiles at each step (with and without GOEXPERIMENT)
3. **Immediate Testing**: Add tests immediately with the change:
   - Parser tests for syntax changes (TinyGo frontend)
   - Type checker tests for semantic rules (TinyGo frontend)
   - LLVM IR tests for correct vector generation (TinyGo backend)
   - WASM execution tests (wasmer-go runtime)
   - Feature gating tests (ensure graceful fallback when disabled)
4. **Reference Patterns**: Reference ISPC implementation for SPMD-specific patterns
5. **Commit Discipline**: Follow git guidelines above for every change

### Testing Strategy

- **Parser Tests**: Valid/invalid syntax recognition (Golang frontend)
- **Type Tests**: Uniform/varying rules, SPMD function marking (Golang frontend)
- **LLVM Tests**: Verify vector IR generation and WASM SIMD128 output (TinyGo backend)
- **Runtime Tests**: Execute WASM binaries with wasmer-go and verify behavior
- **SIMD Verification**: Inspect generated WASM for proper SIMD instruction usage

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

## Proof of Concept Success Criteria

The TinyGo PoC implementation succeeds when ALL examples compile to both SIMD and scalar WASM:

1. **Frontend Validation**: SPMD syntax parses and type-checks correctly for all examples
2. **Dual Code Generation**: All examples generate both SIMD128 and scalar fallback WASM
3. **SIMD Mode**: Varying arithmetic produces `v128.*` WASM instructions when SIMD enabled
4. **Scalar Mode**: Varying arithmetic produces scalar loops when SIMD disabled
5. **Runtime Execution**: Both SIMD and scalar WASM binaries execute correctly in wasmer-go
6. **Performance Benchmarking**: Measurable performance difference between SIMD vs scalar modes
7. **Browser Compatibility**: Ability to detect SIMD support and load appropriate WASM file
8. **Complete Libraries**: All `lanes` and `reduce` functions needed for the examples work in both modes
9. **Complex Examples**: IPv4 parser and base64 decoder compile and execute in both modes
10. **Error Handling**: Clean error messages for invalid SPMD code

## PoC Testing Workflow

### 1. Build Test Runner

```bash
# Create wasmer-go test runner
go mod init spmd-tester
go get github.com/wasmerio/wasmer-go/wasmer
```

### 2. Compile and Test Examples (Dual Mode)

```bash
# Compile ALL examples to both SIMD and scalar WASM
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions varying-universal-constrained union-type-generics infinite-loop-exit uniform-early-return; do
    echo "Testing $example in dual mode..."
    
    # Compile SIMD version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o $example-simd.wasm examples/$example/main.go
    
    # Compile scalar fallback version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o $example-scalar.wasm examples/$example/main.go
    
    # Verify SIMD version contains SIMD instructions
    simd_count=$(wasm2wat $example-simd.wasm | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$simd_count" -eq 0 ]; then
        echo "ERROR: SIMD version contains no SIMD instructions"
        exit 1
    fi
    
    # Verify scalar version contains no SIMD instructions
    scalar_simd_count=$(wasm2wat $example-scalar.wasm | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$scalar_simd_count" -ne 0 ]; then
        echo "ERROR: Scalar version contains SIMD instructions"
        exit 1
    fi
    
    # Execute both versions and verify identical output
    simd_output=$(go run wasmer-runner.go $example-simd.wasm)
    scalar_output=$(go run wasmer-runner.go $example-scalar.wasm)
    
    if [ "$simd_output" != "$scalar_output" ]; then
        echo "ERROR: SIMD and scalar outputs differ"
        echo "SIMD: $simd_output"
        echo "Scalar: $scalar_output"
        exit 1
    fi
    
    echo "✓ $example: SIMD($simd_count instructions) and scalar modes both work"
done
```

### 3. Performance Validation and Browser Integration

```bash
# Benchmark SIMD vs scalar performance
for example in simple-sum ipv4-parser base64-decoder; do
    echo "Benchmarking $example..."
    
    # Time SIMD version
    simd_time=$(time go run wasmer-runner.go $example-simd.wasm 2>&1 | grep real)
    
    # Time scalar version
    scalar_time=$(time go run wasmer-runner.go $example-scalar.wasm 2>&1 | grep real)
    
    echo "$example SIMD: $simd_time"
    echo "$example Scalar: $scalar_time"
done

# Create browser SIMD detection helper
cat > simd-loader.js << 'EOF'
// Browser-side SIMD detection and WASM loading
async function loadOptimalWasm(baseName) {
    const supportsSimd = WebAssembly.validate(new Uint8Array([
        0x00, 0x61, 0x73, 0x6d, // WASM magic
        0x01, 0x00, 0x00, 0x00, // Version
        0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b, // Type section (v128)
    ]));
    
    const wasmFile = supportsSimd ? `${baseName}-simd.wasm` : `${baseName}-scalar.wasm`;
    console.log(`Loading ${wasmFile} (SIMD support: ${supportsSimd})`);
    
    return await WebAssembly.instantiateStreaming(fetch(wasmFile));
}
EOF
```

## Debugging Tips

- Add `-d=ssa/all/dump` flag to see SSA generation
- Use `wasm2wat` to verify SIMD instructions in output
- Check LLVM IR for vector types and operations
- Verify mask propagation with control flow tests
- Compare against ISPC's generated code for similar patterns
