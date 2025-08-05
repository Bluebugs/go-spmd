# Expected Type Checker Errors for SPMD Return/Break Tests

This document lists the expected compilation errors when running type checking on `spmd-return-break-tests.go` with the simplified SPMD implementation.

## Error Types to be Defined

The following error types need to be added to `internal/types/errors`:

```go
InvalidSPMDReturn      // "return statement not allowed in SPMD for loop"  
InvalidSPMDBreak       // "break statement not allowed in SPMD for loop"
InvalidNestedSPMDFor   // "nested go for loops not allowed"
InvalidSPMDFunction    // "functions with varying parameters cannot contain go for loops"
```

## Expected Errors by Test Function

### testForbiddenReturn()
- **Line**: Return statement in go for loop
- **Error**: `InvalidSPMDReturn`
- **Message**: "return statement not allowed in SPMD for loop"
- **Reason**: All return statements are forbidden in go for loops

### testForbiddenBreak()
- **Line**: Break statement in go for loop
- **Error**: `InvalidSPMDBreak` 
- **Message**: "break statement not allowed in SPMD for loop"
- **Reason**: All break statements are forbidden in go for loops

### testNestedGoFor()
- **Line**: Inner `go for j := range 10`
- **Error**: `InvalidNestedSPMDFor`
- **Message**: "nested go for loops not allowed" 
- **Reason**: Go for loops cannot be nested within other go for loops

### testUniformConditionReturn()
- **Line 1**: Return statement with uniform condition
- **Error**: `InvalidSPMDReturn`
- **Message**: "return statement not allowed in SPMD for loop"

- **Line 2**: Break statement with uniform condition  
- **Error**: `InvalidSPMDBreak`
- **Message**: "break statement not allowed in SPMD for loop"

### testMixedScenarios()
- **Line 1**: Return statement (commented out)
- **Error**: `InvalidSPMDReturn` (if uncommented)
- **Message**: "return statement not allowed in SPMD for loop"

- **Line 2**: Break statement (commented out)
- **Error**: `InvalidSPMDBreak` (if uncommented)  
- **Message**: "break statement not allowed in SPMD for loop"

### testVaryingParamFunction()
- **Line**: `go for i := range 10` inside function with varying parameter
- **Error**: `InvalidSPMDFunction`
- **Message**: "functions with varying parameters cannot contain go for loops"
- **Reason**: SPMD functions (those with varying params) cannot contain go for loops

## No Errors Expected (Should Compile Successfully)

The following test functions should **NOT** generate errors:

- `testAllowedContinue()` - Continue statements are allowed in go for loops
- `testRegularForInsideGoFor()` - Regular for loops inside go for are allowed (break/continue in inner loop only)

## Test Validation Strategy

1. **Positive Tests**: Verify allowed cases compile without errors
2. **Negative Tests**: Verify forbidden cases generate specific expected errors
3. **Error Messages**: Confirm error messages match expected text
4. **Error Locations**: Verify errors point to correct source locations
5. **Error Recovery**: Ensure type checker continues after encountering errors

## Implementation Notes

The simplified type checker must:

1. **Simple Rule**: All return/break statements forbidden in `go for` loops
2. **Continue Allowed**: Continue statements remain legal
3. **No Complexity**: No lane activity tracking or mask state management
4. **Clear Errors**: Straightforward error messages for all violations

## Test Execution

```bash
# Run tests with SPMD experiment enabled
GOEXPERIMENT=spmd go test -c tests/spmd-return-break-tests.go

# Expected: Multiple compilation errors matching the patterns above
# Success: All expected errors are generated, no unexpected errors occur
```