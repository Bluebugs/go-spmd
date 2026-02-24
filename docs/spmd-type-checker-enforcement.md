# SPMD Control Flow Restrictions Enforcement Strategy

Reference document for the type checker enforcement of return/break restrictions and nested `go for` loop restrictions. Extracted from CLAUDE.md for on-demand search by agents.

## Overview

Return/break statement restrictions (following ISPC's approach) and nested `go for` loop restrictions are enforced during the **type checking** phase in `/src/cmd/compile/internal/types2/stmt.go`. The implementation tracks varying control flow depth to determine if return/break statements are allowed.

## Implementation Location: Type Checker

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

## Why Type Checking vs Parsing

- **Context Awareness**: Type checker tracks statement context and nested scopes
- **Labeled Breaks**: Handles `break label` where label targets SPMD loop
- **Nested Validation**: Correctly forbids breaks in switch/if inside SPMD loops and nested `go for` loops
- **Consistent Pattern**: All control flow restrictions are enforced in type checker

## Prerequisites

1. **Parser Changes**: `ForStmt` needs `IsSpmd` field to distinguish `go for` from regular `for`
2. **Syntax Recognition**: Parser must recognize `go for` syntax and set SPMD flag
3. **Error Definitions**: Add `InvalidSPMDBreak`, `InvalidSPMDReturn`, and `InvalidNestedSPMDFor` to error types in `internal/types/errors`

## Test Coverage

SPMD control flow restrictions must be validated for:

### Return/Break Control Flow Rules (following ISPC approach with mask alteration)

- **Allowed Cases**: Return/break statements in `go for` loops under uniform conditions only, with no prior mask alteration
- **Forbidden Cases**: Return/break statements under varying conditions (any enclosing varying if)
- **Mask Alteration Cases**: Return/break statements forbidden after continue in varying context, even under subsequent uniform conditions
- **Continue**: Always allowed in `go for` loops regardless of condition type or mask alteration state
- **Mixed Nesting**: Complex combinations of uniform and varying conditions with mask alteration tracking

### Uniform Condition Examples (allowed)

- Direct return/break in `go for` without any conditions
- Return/break under `if uniformVar` conditions
- Return/break under `if uniformFunc()` conditions

### Varying Condition Examples (forbidden)

- Return/break under `if varyingVar` conditions
- Return/break nested inside varying if statements
- Return/break where any enclosing if has varying condition

### Mask Alteration Examples (forbidden)

- Continue in varying context followed by return/break under uniform condition
- Mixed: `if varying { continue }; if uniform { return }` - return forbidden due to prior mask alteration
- Complex: varying condition -> continue -> uniform condition -> return/break (forbidden)

### Nesting Restrictions

- Direct nested `go for` loops (forbidden)
- `go for` loops inside regular for loops (allowed)
- Regular for loops inside `go for` loops (allowed)
- Complex nesting patterns with mixed control flow

**Test Location**: All SPMD control flow restriction tests should be in `/src/cmd/compile/internal/types2/testdata/spmd/` since these are semantic (type checking) errors, not syntax errors.
