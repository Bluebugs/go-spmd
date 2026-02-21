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
- **Constrained Varying**: `lanes.Varying[T, N]` specifies constraint `N` (hardware-independent, compiler handles via unrolling/masking)
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

// Constrained varying - hardware-independent constraints
var data lanes.Varying[byte, 4]   // Constraint 4 (compiler handles implementation)
var mask lanes.Varying[bool, 8]   // Constraint 8 (compiler handles implementation)

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
lanes.Count[int](v)          // Returns SIMD width (e.g., 4)
lanes.Index()                // Returns current lane [0,1,2,3]

// Cross-lane operations
lanes.Broadcast(value, lane)  // Broadcast from one lane to all
lanes.Rotate(value, offset)   // Rotate values across lanes
lanes.Swizzle(value, indices) // Arbitrary permutation
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
- **Type Checker Intercept**: `lanes.Varying[T]` and `lanes.Varying[T, N]` are intercepted before generic instantiation and converted to internal `SPMDType`
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
- `go/ast`: COMPLETED — `IsSpmd`, `LaneCount`, `Constraint` fields on `RangeStmt`
- `go/parser`: COMPLETED — `go for` loop detection + `range[N]` constraint parsing
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
- Constrained `Varying[T, N]` → `<N x T>` vector types with `spmdEffectiveLaneCount()` — DONE (Phase 2.8c)
- SPMD function body mask infrastructure: `spmdFuncIsBody` flag, mask stack from entry mask — DONE (Phase 2.9a)
- Per-lane break mask support: break mask alloca, break redirects, active mask computation — DONE (Phase 2.9b)
- Vector IndexAddr + break result tracking: vector of GEPs, per-lane bounds check, break result allocas — DONE (Phase 2.9c)
- **Mandelbrot running**: 0 differences vs serial, ~2.98x SPMD speedup (6 performance optimizations applied)
- Key files: `compiler/compiler.go` (getLLVMType, createBinOp, createExpr, createFunction, createConvert, *ssa.If/*ssa.Jump/*ssa.Phi), `compiler/spmd.go`, `compiler/symbol.go`, `compiler/func.go`, `compiler/interface.go`

## SSA Generation Strategy (Following ISPC's Approach)

Based on ISPC's proven methodology, Go SPMD implementation generates SSA that directly maps to LLVM IR operations:

### Direct Vector SSA Generation

Instead of custom opcodes, use standard SSA operations with vector types:

```go
// Original Go SPMD code
var data [16]lanes.Varying[int32]
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
func process(data lanes.Varying[int32]) lanes.Varying[int32] {
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
var cond lanes.Varying[bool]
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

Go frontend implementation (Phase 1) is complete with 53 commits on the `spmd` branch:

1. **Phase 1.1-1.5: COMPLETED** - Lexer, parser, type system, type checking (keyword-based)
2. **Phase 1.6: COMPLETED** - Migration to package-based types (`lanes.Varying[T]` replaces `varying` keyword)
3. **Phase 1.7: COMPLETED** - SIMD lane count calculation and recording (laneCountForType, computeEffectiveLaneCount, ForStmt.LaneCount)
4. **Phase 1.8: COMPLETED** - `lanes` package with compiler builtin stubs, `Count()` PoC
5. **Phase 1.9: PARTIALLY DONE** - `reduce` package stubs (all functions panic, not implemented)
6. **Phase 1.10: COMPLETED** - Go SSA Generation for SPMD
   - 1.10a: COMPLETED - SPMD field propagation through noder/IR pipeline (IsSpmd, LaneCount wired to SSA)
   - 1.10b: COMPLETED - Scalar fallback SSA generation for SPMD go for loops
   - 1.10c: COMPLETED - 42 SPMD vector opcodes added to SSA generic ops (arithmetic, mask, memory, reduction, cross-lane)
   - 1.10d: COMPLETED - IR opcodes for vectorized loop index (OSPMDLaneIndex, OSPMDSplat, OSPMDAdd) + walk/range.go stride
   - 1.10e: COMPLETED - Tail masking for non-multiple loop bounds (spmdBodyWithTailMask, SPMDLess + SPMDMaskAnd)
   - 1.10f: COMPLETED - Mask propagation through varying if/else (IsVaryingCond flag, spmdIfStmt, SPMDSelect merge)
   - 1.10i: COMPLETED - Switch masking (IsVaryingSwitch, spmdSwitchStmt, per-case masks, N-way merge, varying case values)
   - 1.10g: COMPLETED - Varying for-loop masking (spmdLoopMaskState, spmdMaskedBranchStmt, spmdRegularForStmt, spmdVaryingDepth tracking)
   - 1.10j: COMPLETED - lanes/reduce builtin call interception (16 functions -> SPMD opcodes, 7 deferred)
   - 1.10h: COMPLETED - Function call mask insertion (OpSPMDCallSetMask, OpSPMDFuncEntryMask, isSPMDCallTarget, spmdFuncEntry)
   - 1.10L: COMPLETED - Fixed all 6 pre-existing all.bash test failures (copyright, stdPkgs, deps, error codes, gcimporter, go/types)
7. **Phase 2: IN PROGRESS** - TinyGo LLVM backend with WASM SIMD128 target
   - **Phase 2.0: Go Standard Library Porting** (prerequisite before TinyGo work):
     - `go/ast`: COMPLETED — `IsSpmd`, `LaneCount`, `Constraint` fields on `RangeStmt`
     - `go/parser`: COMPLETED — `go for` parsing + `range[N]` constraints + `Varying[T, N]` in type and expression contexts (20 tests)
     - `go/types`: COMPLETED — 10 `*_ext_spmd.go` files ported from types2, 10 test files, 8 commits
     - `go/ssa`: No changes needed — SPMD metadata extracted from typed AST in TinyGo's compiler
   - **Phase 2.0d: COMPLETED** — SPMD metadata extraction in TinyGo compiler
     - `compiler/spmd.go`: SPMDInfo side-table with loop/function metadata extraction from typed AST
     - `compiler/spmd_test.go`: 13 tests (extraction, signature analysis, binary search queries)
     - `compiler/compiler.go`: `spmdInfo` field on `compilerContext`, `loadSPMDInfo()` call in `CompilePackage()`
     - Query helpers: `isInSPMDLoop()`, `getSPMDLoopAt()`, `getSPMDFuncInfo()`, `isSPMDFunction()`, `hasSPMDCode()`
   - **Phase 2.1: COMPLETED** — GOEXPERIMENT support + auto-SIMD128 for WASM
     - `goenv/goenv.go`: GOEXPERIMENT in Keys + Get()
     - `compileopts/config.go`: GOExperiment() method, hasExperiment() helper, Features() auto-adds +simd128
     - `compileopts/config_spmd_test.go`: 12 test cases (auto-SIMD128 logic + accessor)
     - `main.go`: Wire GOExperiment from environment; `loader/list.go`: pass to go list subprocess
   - **Phase 2.2: COMPLETED** — LLVM vector type generation for `lanes.Varying[T]`
     - `compiler/spmd.go`: `spmdLaneCount()`, `splatScalar()`, `spmdBroadcastMatch()`, `createSPMDConst()`
     - `compiler/compiler.go`: `*types.SPMDType` case in `makeLLVMType()`, bypass `typeutil.Map` for SPMDType in `getLLVMType()`, broadcast in `createBinOp()`, SPMD pre-check in `createConst()`, vector-safe `ConstNull`/`ConstAllOnes` in `createUnOp()`
     - `compiler/spmd_llvm_test.go`: 6 tests, 34 cases (lane count, type mapping, constants, broadcast, splat, consistency)
     - `typeutil.Map` from `x/tools` can't hash `*types.SPMDType`; **fixed** with patched x-tools copy at `x-tools-spmd/` (SPMDType cases in hash()/shallowHash(), prime 9181). TinyGo `go.mod` has `replace golang.org/x/tools v0.30.0 => ../x-tools-spmd`. The `getLLVMType()` bypass remains as defense-in-depth.
   - **Phase 2.3: COMPLETED** — SPMD loop lowering (`go for` range loops)
     - `compiler/spmd.go`: `analyzeSPMDLoops()` detects rangeint SSA patterns, `emitSPMDBodyPrologue()` generates lane indices + tail mask
     - `compiler/compiler.go`: `spmdLoopState`/`spmdValueOverride` fields, `getValue()` override, `createFunction()` hooks, BinOp `+1` → `+laneCount`
     - `compiler/spmd_llvm_test.go`: 4 new tests (lane offset, lane indices, tail mask, analyze nil)
     - Key insight: SSA `rangeint.iter` phi detected by comment, scalar phi stays for loop logic, vector override for body instructions
   - **Phase 2.4: COMPLETED** (via Phase 2.2) — Varying arithmetic uses LLVM auto-vectorization
   - **Phase 2.5: COMPLETED** — Control flow masking (varying if/else linearization)
     - `compiler/spmd.go`: `spmdVaryingIf` type, `isBlockInSPMDBody()`, `spmdDetectVaryingIf()`, `spmdFindMerge()`, `spmdFindThenExits()`, `spmdShouldRedirectJump()`, `spmdCreateMergeSelect()`, `spmdVectorAnyTrue()`, `spmdIsReachableFrom()`
     - `compiler/compiler.go`: 3 new builder fields (`spmdVaryingIfs`, `spmdThenExitRedirects`, `spmdMergeSelects`), modified `*ssa.If` (linearize vector conditions), `*ssa.Jump` (then-exit redirects), `*ssa.Phi` (merge select conversion), fixed `spmdValueOverride` scope for if/then/else/done blocks
     - `compiler/spmd_llvm_test.go`: 4 new tests (vector any-true, select creation, block detection, broadcast for select)
     - Key insight: go/ssa creates separate "if.done" merge per if statement with exactly 2 predecessors; nesting works by construction
   - **Phase 2.6: COMPLETED** — SPMD function call handling
     - `compiler/spmd.go`: `spmdMaskType()`, `spmdMaskTypeFromSig()`, `spmdCallMask()` — compute mask type from first varying param, resolve mask at call site (tail mask > entry mask > all-ones)
     - `compiler/symbol.go`: `getFunction()` inserts `<N x i1>` mask as first parameter for non-exported SPMD functions
     - `compiler/func.go`: `getLLVMFunctionType()` inserts mask type for SPMD function pointer types
     - `compiler/compiler.go`: `spmdEntryMask` field on builder, mask extraction in `createFunctionStart()`, mask insertion in `createFunctionCall()` with exported function guard
     - `compiler/spmd_llvm_test.go`: 2 new tests (mask type computation, all-ones mask creation) — 9 cases total
     - Key insight: Mask param must be consistently present/absent across declaration (`getFunction`), type (`getLLVMFunctionType`), definition (`createFunctionStart`), and call site (`createFunctionCall`). Exported SPMD functions are forbidden by type checker.
   - **Phase 2.7: COMPLETED** — lanes/reduce builtin interception
     - `compiler/spmd.go`: `spmdVectorTypeSuffix()`, `spmdCallVectorReduce()`, `spmdCallVectorReduceFloat()`, `spmdIsSignedInt()`, `spmdIsFloat()`, `createLanesBuiltin()`, `createReduceBuiltin()`
     - `compiler/compiler.go`: 2 new switch cases in `createFunctionCall()` — `lanes.*` and `reduce.*` prefix matching before SPMD mask insertion
     - `compiler/spmd_llvm_test.go`: 9 new tests (vector type suffix, vector reduce, float reduce, reduce all, reduce count, lanes index, lanes broadcast, is-signed-int, is-float) — 32 cases total
     - Implemented: 6 lanes builtins (Index, Count, Broadcast, ShiftLeft, ShiftRight, From) + 13 reduce builtins (Add, Mul, All, Any, Max, Min, Or, And, Xor, From, Count, FindFirstSet, Mask)
     - Key insight: LLVM float reductions (`fadd`/`fmul`) take extra start value parameter (0.0 for add, 1.0 for mul). Signed/unsigned dispatch for `smax`/`umax`/`fmax`. `fmax`/`fmin` are correct for Go semantics (IEEE maxNum, non-NaN propagating).
     - Deferred: `lanes.Rotate`, `lanes.Swizzle`
   - **Phase 2.7c: COMPLETED** — FromConstrained/ToConstrained LLVM lowering
     - `compiler/spmd.go`: `createFromConstrained()` decomposes `<N x T>` into `ceil(N/P)` groups of `<P x T>` + masks, `createToConstrained()` reconstructs `<N x T>` from groups
     - `compiler/compiler.go`: Wired into `createLanesBuiltin()` switch
     - `go/src/go/types/unify_ext_spmd.go` + `operand_ext_spmd.go`: Relaxed unifier + assignability to allow constrained→unconstrained varying flow
     - `go/src/cmd/compile/internal/types2/`: Mirror changes for types2
     - Fix: constraintN type erasure — derive from `max(spmdEffectiveLaneCount, vec.Type().VectorSize())` when Go type constraint lost after type relaxation
     - **Known limitation**: `[]Varying[bool]` mask slices blocked by WASM `<N x i1>` memory limitation (see `docs/fromconstrained_mask_issue.md`). Value decomposition works correctly.
   - **Phase 2.8: COMPLETED** — Execution mask stack + vector memory operations
     - `compiler/spmd.go`: `spmdMaskTransition` + `spmdContiguousInfo` types, mask stack ops (`spmdPushMask`/`spmdPopMask`/`spmdCurrentMask`), 4 LLVM intrinsic helpers (`spmdMaskedLoad`/`spmdMaskedStore`/`spmdMaskedGather`/`spmdMaskedScatter`), `spmdContiguousIndexAddr()`, `scalarIterVal` on `spmdActiveLoop`
     - `compiler/compiler.go`: 3 new builder fields (`spmdMaskStack`, `spmdMaskTransitions`, `spmdContiguousPtr`), mask transition application in DomPreorder loop, mask stack initialization at body prologue, contiguous detection in `*ssa.IndexAddr`, contiguous load in `token.MUL` UnOp + gather fallback, contiguous store in `*ssa.Store` + scatter fallback
     - `compiler/spmd_llvm_test.go`: 6 new tests (mask stack, masked load/store intrinsics, mask transitions, contiguous info, mask AND)
     - Key insight: Phase 2.5 linearization is correct for value computation but wrong for side effects (stores) — both branches always execute, so stores write for ALL lanes. Mask stack fixes this by tracking active lanes through nested if/else. Contiguous `data[i]` uses scalar GEP + masked load/store; non-contiguous uses gather/scatter fallback.
     - Deferred to Phase 2.8b: ~~Range-over-slice loop detection~~ — **COMPLETED**
   - **Phase 2.8b: COMPLETED** — Range-over-slice SPMD loop detection
     - `compiler/spmd.go`: Extended `spmdActiveLoop` with `isRangeIndex`/`bodyIterValue`/`initEdgeIndex`, second detection pass in `analyzeSPMDLoops()` for `rangeindex.body` blocks, `emitSPMDBodyPrologue()` branches on `isRangeIndex`
     - `compiler/compiler.go`: Rangeindex body prologue trigger at block entry (body has no iter phi), phi init override (-1 to -laneCount on entry edge)
     - `compiler/spmd_llvm_test.go`: 3 new tests (rangeindex fields, body iter value, phi init override)
     - Key insight: rangeindex phi is in loop block (not body), starts at -1 (pre-increment), body uses `incr` BinOp as index. Both `loopPhi` and `incrBinOp` registered in `activeLoops` for contiguous detection. All Phase 2.8 infrastructure (masked load/store, gather/scatter, mask stack) works automatically.
   - **Phase 2.8c: COMPLETED** — Constrained Varying[T,N] backend support
     - `compiler/spmd.go`: `spmdEffectiveLaneCount()` respects constraint N from `Varying[T, N]`, `arrayToVector()` converts `[N]T` to `<N x T>`, fixed `spmdMaskTypeFromSig()` to use effective lane count
     - `compiler/compiler.go`: `makeLLVMType()` and `createDIType()` use `spmdEffectiveLaneCount()`, `createConvert()` array-to-SPMD path with bounds check
     - `compiler/interface.go`: `getTypeCode()` uses `spmdEffectiveLaneCount()` for interface boxing
     - `compiler/spmd_llvm_test.go`: 5 new tests (constrained lane count, effective lane count, array-to-vector, constrained const, constrained mask type)
     - Key insight: Backend support is complete but 3 E2E programs using constrained types fail at go/parser level (`expected type, found 2`) because multi-arg index expressions in standalone type contexts aren't supported. Backend works correctly for constrained types that reach it.
   - **Phase 2.9a: COMPLETED** — SPMD function body mask infrastructure
     - `compiler/spmd.go`: `spmdFuncIsBody` flag, extended `isBlockInSPMDBody()` for function bodies
     - `compiler/compiler.go`: Detect SPMD function bodies in `createFunction()`, initialize mask stack from entry mask, extend `*ssa.If` linearization check
   - **Phase 2.9b: COMPLETED** — Per-lane break mask support
     - `compiler/spmd.go`: `spmdForLoopInfo`/`spmdBreakRedirect` types, `detectSPMDForLoops()` for regular for-range in SPMD bodies, `spmdIsVaryingBreak()` detection
     - `compiler/compiler.go`: Break mask alloca init, break redirect in `*ssa.Jump`, active mask computation (entryMask & ~breakMask) after rangeint.iter phi
   - **Phase 2.9c: COMPLETED** — Vector IndexAddr + break result tracking + mandelbrot
     - `compiler/spmd.go`: `spmdBreakResult` type, merged body+loop detection, break result alloca population from done-block phis
     - `compiler/compiler.go`: Vector IndexAddr (vector of GEPs + per-lane bounds check), break result accumulation (`select(mask, breakVal, oldResult)`), break result phi compilation (`select(breakMask, breakResult, phi)`), break edge skip in phi resolution
     - Mandelbrot: Removed reduce.Any guard, rewrote demonstrateVaryingParameters with reduce.From
   - **E2E Infrastructure: COMPLETED** — GOEXPERIMENT passthrough fix + go/ssa SPMDType support + Node.js test runner
     - `tinygo/loader/list.go`: Fixed GOEXPERIMENT stripping that prevented `lanes.go`/`reduce.go` from being visible to `go list`
     - `x-tools-spmd/go/ssa/subst.go`: Added `*types.SPMDType` cases to `subster.typ()` and `reaches()` for generic instantiation
     - `test/e2e/run-wasm.mjs`: Node.js WASI WASM runner with asyncify stubs for TinyGo
     - `test/e2e/spmd-e2e-test.sh`: Progressive 8-level E2E test script (46 tests)
   - **Range-over-slice type fix: COMPLETED** — Fixed `NewVarying(expr.typ())` → `NewVarying(rVal)` using `rangeKeyVal()`
     - `go/src/go/types/stmt_ext_spmd.go`: Call `rangeKeyVal()` for proper element type extraction, use `Typ[Int]` for key
     - `go/src/cmd/compile/internal/types2/stmt_ext_spmd.go`: Identical fix for types2
     - Test files added in both `go/types/testdata/spmd/` and `types2/testdata/spmd/` (range_over_slice.go)
   - **createConvert SPMDType fix: COMPLETED** — Defensive handling in TinyGo `createConvert()`
     - `tinygo/compiler/compiler.go`: Intercept `*types.SPMDType` before `Underlying()` assertions
     - Four branches: SPMD-to-SPMD (recurse with elem), SPMD-to-scalar (recurse with elem), array-to-SPMD (arrayToVector), scalar-to-SPMD (convert + splat)
   - **E2E Test Results** (17 run pass, 20 compile pass, 47 total):
     - Inline tests (11): L0_store, L0_cond, L0_func, L1_reduce_add, L2_lanes_index, L3_varying_var, L4_range_slice, L4b_varying_break, L5a_simple_sum, L5b_odd_even, L5e_from_constrained
     - Integration run-pass (6): integ_simple-sum, integ_odd-even, integ_hex-encode, integ_debug-varying, integ_lanes-index-restrictions, integ_mandelbrot (0 differences, ~2.98x speedup)
     - Compile-only pass (5): integ_to-upper, integ_goroutine-varying, integ_select-with-varying-channels, integ_type-casting-varying, integ_varying-universal-constrained
     - Reject OK (11): All illegal examples correctly rejected
   - **Test program fixes: COMPLETED** — Fixed 12 buggy example programs across 6 commits
     - hex-encode: removed phantom `lanes.Encode` call (now compiles)
     - bit-counting: fixed `reduce.Add(count)` return type (uint8 vs int)
     - map-restrictions: fixed undefined `values` var, split if-init scope issue; then fixed varying-to-uniform struct literal using reduce.From()
     - defer-varying: fixed `reduce.From` → `reduce.Add`, `allocateResource` signature type mismatch
     - select-with-varying-channels: fixed `chan int` → `chan string`, unused variable in `go for`; then fixed invalid `go for { }` → `for { }`
     - to-upper: rewrote as self-contained ASCII SPMD (now compiles after vector width fix)
     - mandelbrot: fixed `cIm`/`zIm` param types, `benchmark()` missing return type
     - union-type-generics: fixed shift type mismatch (`[]int` → `[]uint16`)
     - pointer-varying: fixed `lanes.From(array)` → `lanes.From(array[:])` (remaining errors are unsupported-but-desired patterns)
     - base64-decoder: fixed constrained range syntax (`go for i := range[4]` → `go for i := range[4] 4`)
     - All examples/ synced with test/integration/spmd/ copies
   - **E2E suite expanded: COMPLETED** — From 32 → 46 tests across multiple expansions
   - **Compiler quick-fixes: COMPLETED** — 2 TinyGo compiler fixes
     - `compiler/llvm.go`: Added `llvm.VectorTypeKind` to `getPointerBitmap()` no-pointer case (unblocked goroutine-varying)
     - `compiler/compiler.go`: Added `types.UntypedInt` to `makeLLVMType()` Int/Uint case
   - **Nested loop deduplication: COMPLETED** — Fixed `analyzeSPMDLoops()` incorrectly vectorizing nested regular `for` loops
     - `compiler/spmd.go`: Added unified `seenLoopInfo` deduplication map shared across rangeint/rangeindex passes
     - Only the first (outermost) body block per `SPMDLoopInfo` is registered; nested regular loops are skipped
     - Handles cross-pattern nesting (rangeint outer + rangeindex inner, or vice versa)
   - **Performance Optimization Round 1: COMPLETED** — 3 optimizations, ~1.2x → ~2.81x speedup
     - Early exit when all lanes broken (spmdVectorAllTrue + condBr to done block)
     - inlinehint on SPMD functions + spmdCallMask uses narrowed mask from stack
     - Generalized contiguous detection (spmdAnalyzeContiguousIndex traces scalar+iter through BinOp ADD)
   - **Performance Optimization Round 2: COMPLETED** — 2 optimizations + 1 investigation, ~2.81x → ~2.91x speedup
     - `compiler/spmd.go`: Added `spmdUnwrapScalar()` to peel `*ssa.ChangeType` chains in contiguous detection — fixes `output[j*width+i]` scatter store. ~38% improvement.
     - `compiler/spmd.go`: Changed mask format from `<N x i1>` to `<N x i32>` on WASM — added `spmdIsWASM()`, `spmdMaskElemType()`, `spmdWrapMask()`, `spmdUnwrapMaskForIntrinsic()`, `spmdMaskSelect()` with width-safety fallback, `spmdNormalizeBoolVecToI1()`. 8 new tests. ~3% improvement.
     - Tail mask hoisting: verified via LLVM IR + WAT analysis that V8 TurboFan JIT handles loop-invariant hoisting. No code change needed.
   - **Performance Optimization Round 3: COMPLETED** — 1 optimization, ~2.91x → ~2.98x speedup
     - `compiler/spmd.go`: Native WASM `v128.any_true`/`i32x4.all_true` intrinsics via `@llvm.wasm.anytrue`/`@llvm.wasm.alltrue` — replaces bitcast+icmp pattern. Added `spmdWasmAnyTrue()`, `spmdWasmAllTrue()`. 2 new tests. ~2.4% improvement.
     - WAT verification: `v128.any_true` in inner loop, old `i64x2.extract_lane` pattern eliminated.
   - **SPMDType interface boxing fix: COMPLETED** — TinyGo `getTypeCode()` now handles `*types.SPMDType`
     - `tinygo/compiler/interface.go`: Intercept SPMDType in `getTypeCode()`, redirect to `[laneCount]T` array representation
     - Defensive fallbacks in `getTypeCodeName()` and `typestring()` for `*types.SPMDType`
     - Fixes `debug-varying` panic when passing `lanes.Varying[int]` to `fmt.Printf("%v", ...)`
   - **Vector width mismatch fix: COMPLETED** — `spmdBroadcastMatch()` handles vector-vector width normalization
     - `tinygo/compiler/spmd.go`: Extended `spmdBroadcastMatch()` with vector-vector width case, added `spmdResizeVector()` (shuffle-based truncation)
     - `tinygo/compiler/spmd_llvm_test.go`: 3 new test cases (narrower_wins, resize_vector_truncate, same_width_noop)
     - Fixes `to-upper` ICmp type mismatch: byte constants get 16 lanes (128/8) but loop effective lane count is 4 (128/32)
   - **Constrained Varying[T,N] parser fix: COMPLETED** — both type and expression contexts support non-type arguments
     - `go/src/go/parser/parser.go`: SPMD-gated fallback in `parseTypeInstance()` (type contexts) and `parseIndexOrSliceOrInstance()` (expression contexts: conversions, type switch cases, composite literals)
     - `go/src/go/parser/parser_spmd_test.go`: 9 new test cases (6 type-context + 3 expression-context: conversion, type switch case, conversion assign)
     - Unblocks all constrained type programs past parse stage (type-casting-varying, type-switch-varying, varying-array-iteration, varying-universal-constrained, mandelbrot)
   - **SPMD varying upcast restriction: COMPLETED** — rejects Varying[smallerType] to Varying[largerType] conversions
     - `go/src/go/types/operand_ext_spmd.go` + `types2/operand_ext_spmd.go`: `spmdBasicSize()` helper, upcast checks in `convertibleToSPMD()` and `checkSPMDtoSPMDAssignability()`
     - `go/src/go/types/conversions.go` + `types2/conversions.go`: SPMD-to-SPMD guard in `convertibleTo()` prevents standard Go numeric conversion fallthrough
     - Downcasts (e.g., Varying[uint32] to Varying[uint16]) and same-size conversions (e.g., Varying[int32] to Varying[float32]) remain allowed
     - `illegal_invalid-type-casting` now correctly rejected with 8 error sites
   - **Constrained type checker fixes: COMPLETED** — 4 commits in go/types + types2, 3 new test files each
     - `go/src/go/types/operand_ext_spmd.go` + types2 mirror: Array-to-constrained-Varying conversion `[N]T` → `Varying[T, N]` in `convertibleToSPMD()`
     - `go/src/go/types/index.go` + types2 mirror: Clear "varying types are not indexable" error before `Underlying()` type switch in `indexExpr()`
     - `go/src/go/types/stmt.go` + `lookup.go` + types2 mirrors: Type switch on `Varying[T, 0]` via `isSPMDUniversalConstrained()` + `assertableTo()` SPMD cases
     - New test files: `array_to_varying.go`, `varying_index.go`, `type_switch_constrained.go` in both go/types and types2 testdata
     - type-casting-varying promoted from compile fail to compile pass; varying-universal-constrained still SIGSEGV (TinyGo type assert on SPMDType)
   - **Bug Fixes** (3 commits):
     - fix: shift bounds check for vector operands — vector-aware splat helpers in asserts.go + compiler.go
     - fix: non-SPMD varying return call signature — spmdMaskType() consistency at declaration/call/type
     - fix: varying if/else phi merge inside loop bodies — spmdFindMerge ifBlock barrier + multi-pred merge select + deferred select in else-exit block (L5b 800→404)
   - **Known E2E Failures** (15 compile fail, categorized):
     - Constrained type backend (1): varying-array-iteration (array-to-constrained-Varying conversion at TinyGo level)
     - SIGSEGV (3): array-counting (CreateExtractValue on vector), spmd-call-contexts (wrong arg count in closure), type-switch-varying (nil pointer in compiler)
     - LLVM verification (3): defer-varying (wrong arg count in closure), panic-recover-varying (masked load of struct type), non-spmd-varying-return (varying value passed to uniform param inside go for)
     - Compiler bugs (2): map-restrictions (masked load of `%runtime._string`), union-type-generics (generic SPMD function panic in typeparams)
     - Scalar-to-SPMD convert (1): bit-counting (scalar-to-SPMD convert received vector value in nested loop)
     - Design issues (2): pointer-varying (unsupported varying pointer patterns), base64-decoder (constrained types + Rotate/Swizzle)
     - Missing package (1): ipv4-parser (math/bits not in TinyGo std)
     - Printf (1): printf-verbs (nil pointer)
   - **Phase 2.9-2.10: TinyGo Compiler Work (remaining)**:
     - TinyGo uses `golang.org/x/tools/go/ssa` (NOT Go's `cmd/compile/internal/ssa`)
     - LLVM auto-vectorizes: `CreateAdd(<4 x i32>, <4 x i32>)` → WASM `v128.add`
     - Missing: varying switch/for-loop masking, lanes.Rotate/Swizzle, scalar fallback mode
     - **Performance**: ~2.98x SPMD speedup on mandelbrot (256x256, 256 iterations, 0 differences vs serial)
8. **Phase 3: NOT STARTED** - Validation and dual-mode testing

   - **Syntax Migration: COMPLETED** — All examples, docs, and tests migrated from old keyword syntax to package-based types
     - 5 commits: example programs (22 files), illegal-spmd tests (10 files), integration test sync (13 files), markdown docs (10 files), blog config + final cleanup
     - Old `varying T` → `lanes.Varying[T]`, `uniform T` → plain `T`, `varying[N] T` → `lanes.Varying[T, N]`
     - Legacy backward-compat files intentionally preserved (use `varying`/`uniform` as identifiers)
     - `reduce.Uniform[T]` type alias skipped (Go 1.27dev rejects `type Uniform[T any] = T` with MisplacedTypeParam)

Next priority: Resolve `[]Varying[bool]` mask issue for FromConstrained (see docs/fromconstrained_mask_issue.md), fix SIGSEGV crashes (array-counting, spmd-call-contexts, type-switch-varying, varying-universal-constrained), fix LLVM masked load of struct types (unblocks map-restrictions, panic-recover-varying), fix closure arg count (defer-varying, spmd-call-contexts), fix array-to-constrained-Varying conversion (varying-array-iteration), then varying switch/for-loop masking

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
