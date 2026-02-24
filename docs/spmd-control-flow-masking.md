# SPMD Control Flow Masking

Reference document for SPMD control flow masking transformations, return/break rules, and validation logic. Extracted from CLAUDE.md for on-demand search by agents.

## Basic Conditional Masking

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

## For Loop Masking

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

## SPMD `go for` Loop Masking

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

## Nested Loop Masking

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

## Key Masking Principles

1. **Continue**: Sets temporary mask for current iteration, reset on next iteration
2. **Break**: Sets permanent mask that persists for remainder of loop
3. **Return/Break Rules**: Return/break statements are forbidden in `go for` loops generally when in varying/SPMD context, but are allowed in uniform context (The compiler has to be able to prove that a return/break are only inside uniform branch all the way to have them allowed)
4. **Continue Allowed**: Continue statements remain legal for per-lane loop control
5. **SPMD go for**: Processes multiple elements per iteration, mask tracks per-lane state
6. **Early Termination**: Loop exits when no lanes remain active (`!reduce.Any(activeMask)`)
7. **Mask Inheritance**: Inner scopes inherit masks from outer scopes via logical AND
8. **Alternative Patterns**: Use reduce operations and structured control flow instead of early exits

## SPMD Return/Break Examples

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

## Return/Break Validation Rules

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
