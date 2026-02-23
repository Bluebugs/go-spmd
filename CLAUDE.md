# CLAUDE.md - SPMD Implementation for Go via TinyGo

## Project Overview

This workspace implements Single Program Multiple Data (SPMD) support for Go, similar to Intel's ISPC and Mojo's SIMD capabilities. The implementation extends TinyGo (which uses LLVM) rather than the main Go compiler, targeting WebAssembly SIMD128 as the proof of concept backend.

**Proof of Concept Scope:**

- **Frontend**: Full lexer, parser, and type checker support for SPMD constructs
- **Backend**: TinyGo LLVM integration with dual code generation: SIMD128 and scalar fallback
- **Standard Library**: `lanes` and `reduce` packages with all functions needed for examples
- **Testing**: Node.js WASI runtime execution (test/e2e/), with planned wasmer-go integration and performance benchmarking
- **Goal**: Compile ALL examples to both SIMD and scalar WASM, enabling browser-side SIMD detection and performance comparison

## Key Concepts

### SPMD Programming Model

- **SPMD**: All lanes execute the same program but on different data elements
- **Uniform**: Values that are the same across all SIMD lanes (scalars)
- **Varying**: Values that differ across SIMD lanes (vectors)
- **Execution Mask**: Tracks which lanes are active during control flow
- **lanes.Count(lanes.Varying[T])**: Number of SIMD lanes for a specific type (e.g., 4 for WASM 128-bit SIMD), known at compile time
- **lanes.Index()**: Current lane index (0 to lanes.Count()-1) in the current SPMD context
- **lanes**: New Golang standard library module providing cross lane functions
- **reduce**: New Golang standard library module providing reduction operations (varying to uniform) and type conversion (varying to array)
- **Printf Integration**: `fmt.Printf` with `%v` automatically converts varying types to arrays for display

### Go SPMD Syntax

SPMD types use the `lanes` package instead of keywords:

```go
import "lanes"

// Uniform values are regular Go values (no annotation needed)
var x int              // Scalar value, same across all lanes
var y float32          // Regular Go value = uniform

// Varying values use lanes.Varying[T]
var v lanes.Varying[float32]  // Vector value, different per lane

// SPMD loop construct
go for i := range 16 {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [i, i+1, i+2, i+3]
}

// Builtins
lanes.Count[int](v)          // Returns SIMD width (e.g., 4)
lanes.Index()                // Returns current lane [0,1,2,3]

// Cross-lane operations
lanes.Broadcast(value, lane)         // Broadcast from one lane to all
lanes.Rotate(value, offset)          // Rotate values across lanes
lanes.Swizzle(value, indices)        // Arbitrary permutation
lanes.RotateWithin(value, offset, n) // Rotate within groups of n lanes
lanes.SwizzleWithin(value, idx, n)   // Swizzle within groups of n lanes
lanes.ShiftLeftWithin(value, cnt, n) // Shift left within groups of n lanes
lanes.ShiftRightWithin(val, cnt, n)  // Shift right within groups of n lanes
```

## Implementation Architecture

### Runtime GOEXPERIMENT Flag Integration

SPMD support is implemented as a **runtime experimental feature** behind `GOEXPERIMENT=spmd`:

1. **Runtime Experiment Flag**: `GOEXPERIMENT=spmd` environment variable enables SPMD functionality

2. **Single Compiler Binary**: One Go compiler binary handles both SPMD and standard Go compilation modes

3. **Runtime Feature Gating**: All SPMD functionality gated behind `buildcfg.Experiment.SPMD` runtime checks:
   - SPMD syntax parsing enabled/disabled per compilation
   - `lanes.Varying[T]` type recognition controlled by runtime flag
   - Type checker SPMD rules activated when experiment set
   - SSA generation conditional on experiment flag

4. **No Build Constraints**: Standard Go files work in both modes - no special build tags required

### Phase 1: Go Frontend Changes (Runtime Gated)

- **Package-Based Types**: Varying types use `lanes.Varying[T]` generic type (compiler magic, not regular generics). Uniform values are regular Go types (no annotation needed).
- **Type Checker Intercept**: `lanes.Varying[T]` is intercepted before generic instantiation and converted to internal `SPMDType`
- **Backward Compatibility**: `uniform` and `varying` are regular identifiers (not keywords), no backward compatibility issues
- **Conditional Parsing**: `go for` SPMD loop parsing only active when `buildcfg.Experiment.SPMD` is true
- **Gated Type System**: SPMD type checking rules only apply when `buildcfg.Experiment.SPMD` is true
- **Runtime Extensions**: All SPMD extensions check experiment flag before executing
- **Flexible Compilation**: Same source files compile with/without SPMD depending on flag

### Phase 2: Go SSA Extensions (COMPLETED in Phase 1.10)

- 42 SPMD vector opcodes in Go's `cmd/compile/internal/ssa` (arithmetic, mask, memory, reduction, cross-lane)
- SPMD type information propagated through noder/IR pipeline (IsSpmd, LaneCount, IsVaryingCond)
- `go for` loops generate vectorized SSA with masking operations
- Mask tracking via SPMDMaskAnd/Or/Not opcodes and SPMDSelect merge
- lanes/reduce builtin interception maps 16 functions to SPMD opcodes
- Function call mask insertion via OpSPMDCallSetMask/OpSPMDFuncEntryMask

### Phase 3: TinyGo LLVM Backend (IN PROGRESS - Phase 2 in PLAN.md)

**Critical Architecture Note**: TinyGo uses `go/parser` + `go/types` + `golang.org/x/tools/go/ssa` (standard library), NOT the compiler-internal packages. The 42 SPMD opcodes from Phase 1.10c are invisible to TinyGo.

**Step 1 — Go Standard Library Porting (Phase 2.0 in PLAN.md)**:

Before TinyGo can compile any SPMD code, the standard library toolchain must be ported:
- `go/ast`: COMPLETED — `IsSpmd`, `LaneCount` fields on `RangeStmt`
- `go/parser`: COMPLETED — `go for` loop detection
- `go/types`: COMPLETED — 10 `*_ext_spmd.go` files ported from types2, 13 test files, hooks in stmt/expr/typexpr/check/decl/call/index
- `go/ssa`: NO changes — `golang.org/x/tools/go/ssa` is an external module, not in our Go fork
  - SPMD metadata extracted from typed AST in TinyGo's loader instead (avoids forking x/tools)

**Step 2 — TinyGo Compiler Work (Phase 2.1-2.10 in PLAN.md)**:

- Detect `lanes.Varying[T]` types and map to LLVM vector types (`<4 x i32>`, `<4 x float>`, etc.) — DONE (Phase 2.2)
- LLVM auto-vectorizes: `CreateAdd(<4 x i32>, <4 x i32>)` generates WASM `v128.add` automatically — DONE (Phase 2.2/2.4)
- Add GOEXPERIMENT support to TinyGo — DONE (Phase 2.1)
- Enable `+simd128` in WASM target configurations — DONE (Phase 2.1)
- SPMD loop lowering: lane indices, tail mask, +laneCount increment — DONE (Phase 2.3)
- Handle varying if/else via CFG linearization and LLVM select instructions — DONE (Phase 2.5)
- Handle SPMD function call mask insertion — DONE (Phase 2.6)
- Intercept lanes/reduce function calls and lower to LLVM vector intrinsics — DONE (Phase 2.7)
- Patched `x/tools` (`x-tools-spmd/`) so `typeutil.Map` can hash `*types.SPMDType` — DONE (SPMDType cases in hash/shallowHash, prime 9181, `go.mod` replace directive)
- SPMD function body mask infrastructure: `spmdFuncIsBody` flag, mask stack from entry mask — DONE (Phase 2.9a)
- Per-lane break mask support: break mask alloca, break redirects, active mask computation — DONE (Phase 2.9b)
- Vector IndexAddr + break result tracking: vector of GEPs, per-lane bounds check, break result allocas — DONE (Phase 2.9c)
- **Mandelbrot running**: 0 differences vs serial, ~2.98x SPMD speedup (6 performance optimizations applied)
- Key files: `compiler/compiler.go` (getLLVMType, createBinOp, createExpr, createFunction, createConvert, *ssa.If/*ssa.Jump/*ssa.Phi), `compiler/spmd.go`, `compiler/symbol.go`, `compiler/func.go`, `compiler/interface.go`

## SSA Generation Strategy (Following ISPC's Approach)

Go SPMD implementation follows ISPC's proven SSA strategy: direct mapping to LLVM operations using standard SSA constructs (no custom opcodes). Key design principles:

1. **Vector Types and Operations**: LLVM auto-vectorizes `CreateAdd(<4 x i32>, <4 x i32>)` → WASM `v128.add`
2. **Mask Threading**: Explicit execution masks propagate through vector operations as `<N x i1>` values
3. **SPMD Function Signatures**: Receive mask as **first implicit parameter** in SSA, allowing selective execution of function bodies
4. **Control Flow Linearization**: Varying if/else conditions converted to CFG with LLVM select merges (no control flow divergence)
5. **Per-Lane State Tracking**: Loop breaks and early exits track active lanes via per-lane break masks
6. **Memory Access**: Contiguous array access uses scalar GEPs + masked load/store; non-contiguous uses gather/scatter intrinsics

See `/docs/ssa-generation-strategy.md` for detailed examples and implementation patterns used in Phase 1 Go compiler and Phase 2 TinyGo backend.

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
var t lanes.Varying[bool]

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
var loopMask lanes.Varying[bool] = currentMask
var continueMask lanes.Varying[bool] = false  // per-lane continue flags
var breakMask lanes.Varying[bool] = false     // per-lane break flags

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
var threshold int
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
var continueMask lanes.Varying[bool] = false  // per-lane continue flags

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
var matrix [4][4]lanes.Varying[int]
var sum lanes.Varying[int]
var limit lanes.Varying[int]
var allDone [4]lanes.Varying[bool]

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
    var innerMask lanes.Varying[bool] = outerValidMask
    var innerBreakMask lanes.Varying[bool] = false

    for innerIter := 0; innerIter < 4; innerIter++ { // innerIter is uniform
        innerActiveMask := innerMask & ~innerBreakMask
        var innerContinueMask lanes.Varying[bool] = false
        
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
func processDataWithUniformExit(data []int, threshold int) {
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
func processWithNestedConditions(data []int, mode int) {
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
func demonstratePerformanceDifference(data []int, uniformThreshold int) {
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
func searchPatternOldWay(data []lanes.Varying[byte], pattern byte) bool {
    // OLD: Set flags, use reduce operations
    var found lanes.Varying[bool] = false
    
    go for i := range len(data) {
        if data[i] == pattern {
            found = true  // Set per-lane flag
            // Can't return here due to varying condition
        }
    }
    
    return reduce.Any(found)  // Reduce operation outside loop
}

func searchPatternNewWay(data []lanes.Varying[byte], pattern byte, errorMode bool) bool {
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
func processWithErrorHandling(data []int, params ProcessParams) error {
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

### TinyGo SPMD Development Setup (Runtime Flag)

1. **No rebuild required**: Single TinyGo binary handles both modes

2. **Compile SPMD to WebAssembly**:

   ```bash
   # Enable SPMD for compilation
   GOEXPERIMENT=spmd tinygo build -target=wasi -o simple-sum.wasm examples/simple-sum/main.go
   ```

3. **Execute with wasmer-go**:

   ```bash
   go run wasmer-runner.go simple-sum.wasm
   ```

4. **Verify experiment gating**:

   ```bash
   # Should treat SPMD syntax as regular identifiers without experiment
   tinygo build -target=wasi examples/simple-sum/main.go  # Works if no SPMD syntax
   ```

5. **Dual mode testing**:

   ```bash
   # SIMD mode
   GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o test-simd.wasm main.go
   
   # Scalar mode  
   GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o test-scalar.wasm main.go
   
   # Inspect SIMD instructions
   wasm2wat test-simd.wasm | grep "v128"
   ```

### Agent Workflow (MANDATORY)

All implementation work MUST follow this 3-step pipeline using Claude Code agents:

1. **`golang-pro` agent**: Performs all code implementation (writing, editing, fixing Go code)
2. **`code-reviewer` agent**: Reviews all changes for correctness, style, and safety - only proceed to commit if the reviewer approves
3. **`clean-commit` agent**: Creates the final git commit only after the code review passes

Never skip steps. Never commit without review approval. This ensures code quality and catches issues before they enter the commit history.

### Deferred Items Management (MANDATORY)

All implementation work that is explicitly postponed for future phases MUST be tracked in the centralized deferred items collection:

**IMPORTANT**: Any work identified during implementation that needs to be deferred must be:
1. **Documented**: Add entry to the appropriate Phase Deferred Subtask in PLAN.md (Phase 1, Phase 2, or Phase 3)
2. **Categorized**: Include task description, location, status, dependencies, and priority
3. **Rationalized**: Explain WHY it was deferred (blocked on other work, requires extensive refactoring, intentional design decision, etc.)
4. **Tracked**: Update the corresponding Deferred Subtask status in PLAN.md

**Deferred Item Structure** (see PLAN.md "Deferred Items Collection" section for full format):
- **Task**: What needs to be done
- **Location**: Which phase/component/file
- **Status**: Why deferred (Deferred, Blocked, Partially Done, etc.)
- **Depends On**: Prerequisites that must be completed first
- **Implementation**: High-level approach when it's resumed
- **Priority**: High/Medium/Low
- **Related**: Cross-references to related work

**Examples**:
- **lanes.Rotate() builtin** → Deferred to Phase 2.7b (requires shuffle vector generation pattern)
- **Varying switch masking** → Deferred to Phase 2.5 (requires N-way merge similar to if/else linearization)

**Anti-Pattern**: Do NOT mark items as "deferred" without documenting them in the collection:
- ❌ Leaving code marked `// TODO` or `// FIXME` without PLAN.md entry
- ❌ Mentioning deferral in commit messages without updating PLAN.md
- ❌ Planning deferred work verbally — must be in PLAN.md for tracking
- ❌ Losing track of deferred items as they're scattered across files

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
     - ✅ "Add lanes.Varying type recognition to type checker"
     - ✅ "Fix SPMD loop type checking for varying types"
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

**Phase Summary**: Phase 1 (Go frontend) complete with 53 commits; Phase 2 (TinyGo LLVM backend) in progress with 43 commits; Phase 3 (validation) not started. See PLAN.md for detailed task breakdown and deferred items tracking.

### Phase 1: Go Frontend (COMPLETED)
- Lexer, parser, and type system with package-based types (`lanes.Varying[T]`)
- Full SPMD type checking with return/break restrictions per ISPC semantics
- 42 SPMD vector opcodes in Go SSA (arithmetic, mask, memory, reduction, cross-lane)
- Mask propagation through if/else, switch, and regular for-loops via masking
- lanes/reduce builtin interception (6 lanes functions + 13 reduce functions)
- SPMD function signatures with implicit mask as first parameter
- All SPMD code gated behind `GOEXPERIMENT=spmd`

### Phase 2: TinyGo LLVM Backend (IN PROGRESS)
- **2.0-2.0d** (COMPLETED): Go stdlib porting (go/ast, go/parser, go/types); SPMD metadata extraction in TinyGo
- **2.1-2.9c** (COMPLETED): GOEXPERIMENT support, LLVM vector types, SPMD loop lowering, control flow masking, function call handling, builtin interception, mask stack, break mask support, *Within cross-lane operations (RotateWithin, ShiftLeftWithin, ShiftRightWithin via shufflevector)
- **Varying switch masking** (COMPLETED): Switch chain detection, sequential mask narrowing, CFG linearization, deferred phi resolution for DomPreorder ordering
- **2.9-2.10** (REMAINING): Varying for-loop masking in regular for loops, lanes.Rotate/Swizzle, scalar fallback mode
- **Key Metrics**: Mandelbrot runs at ~2.98x SPMD speedup (256x256, 256 iterations, 0 differences vs serial) with 6 performance optimizations applied; hex-encode benchmark at 0.24x (optimization roadmap in `docs/hex-encode-simd-analysis.md`)
- **E2E Test Results**: 19 run pass, 4 compile-only pass, 13 compile fail, 10 reject OK (46 total)

### Phase 3: Validation (NOT STARTED)
- **Syntax Migration** (COMPLETED): All examples/docs/tests migrated from keyword syntax to package-based types (5 commits, ~55 files)
- Dual-mode testing (SIMD vs scalar WASM) and performance benchmarking remain for Phase 3

**E2E Compile Failure Analysis** (13 failures by root cause):
- **Compiler backend bugs (8)**: bit-counting (scalar-to-SPMD convert), array-counting (SIGSEGV), map-restrictions (LLVM masked load of struct), defer-varying (closure mask arg count), printf-verbs (nil deref crash), panic-recover-varying (struct masked load), non-spmd-varying-return (mask type mismatch), spmd-call-contexts (closure arg count)
- **Complex SPMD patterns (3)**: ipv4-parser (25 LLVM verification errors — multi-lane-count loops, Varying[bool] collision; see `docs/ipv4-parser-status.md`), base64-decoder (type inference mismatch), union-type-generics (x-tools-spmd generic panic)
- **Missing features (2)**: pointer-varying (pointer ops with varying), type-switch-varying (type switch on varying)

**Next Priority** (see PLAN.md for full tracking):
1. Fix closure mask parameter handling (defer-varying, spmd-call-contexts)
2. Fix LLVM masked load of struct types (map-restrictions, panic-recover-varying)
3. Fix SIGSEGV crashes (array-counting)
4. Fix ipv4-parser: multi-lane-count loops, Varying[bool] type collision (see `docs/ipv4-parser-status.md`)
5. Implement varying for-loop masking, scalar fallback mode

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
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions union-type-generics infinite-loop-exit uniform-early-return; do
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
