# Illegal SPMD Examples

This directory contains examples of Go code that should **fail to compile** with the SPMD extensions. These examples demonstrate the compile-time restrictions and error conditions defined in the SPMD Go specification.

## Purpose

These examples serve multiple purposes:

1. **Specification Validation**: Ensure the compiler correctly rejects invalid SPMD code
2. **Error Message Testing**: Verify that error messages are clear and helpful
3. **Documentation**: Show developers what patterns to avoid
4. **Regression Testing**: Prevent accidental acceptance of illegal constructs

## Examples Overview

### [varying-to-uniform.go](varying-to-uniform.go)
**Expected Error**: `cannot assign varying to uniform`

Demonstrates the fundamental type system rule that varying values cannot be assigned to uniform variables:

```go
var uniform_val uniform int
var varying_val varying int = 42
uniform_val = varying_val  // ERROR: cannot assign varying to uniform
```

Key violations:
- Direct varying-to-uniform assignment
- Function parameter mismatches
- Return type violations
- Array indexing producing varying results assigned to uniform

### [break-in-go-for.go](break-in-go-for.go)
**Expected Error**: `break statement not allowed in SPMD for loop`

Shows that `break` statements are prohibited in SPMD `go for` loops to maintain execution coherency:

```go
go for i := range len(data) {
    if data[i] > 5 {
        break  // ERROR: break statement not allowed in SPMD for loop
    }
}
```

Key violations:
- Direct `break` in `go for` loop
- Labeled `break` targeting `go for` loop
- `break` in nested constructs within `go for`
- Comparison with legal `continue` statements

### [nested-go-for.go](nested-go-for.go)
**Expected Error**: `go for loops cannot be nested`

Shows that SPMD `go for` loops cannot be nested within other `go for` loops (prohibited for now) to avoid complex mask management:

```go
go for i := range 16 {
    go for j := range 16 {  // ERROR: go for loops cannot be nested (for now)
        total += data[i][j]
    }
}
```

Key violations:
- Direct nesting of `go for` loops
- `go for` inside regular for loop inside another `go for`
- Deep nesting scenarios

### [go-for-in-spmd-function.go](go-for-in-spmd-function.go)
**Expected Error**: `go for loops not allowed in SPMD functions`

Demonstrates that functions with varying parameters (SPMD functions) cannot contain `go for` loops:

```go
func processSPMDData(data varying int) varying int {
    // ERROR: go for loops not allowed in SPMD functions
    go for i := range 4 {
        result += varying(i)
    }
    return result
}
```

Key violations:
- `go for` inside function with varying parameters
- Complex SPMD function scenarios
- Contrast with regular functions (which can have `go for`)

### [public-spmd-function.go](public-spmd-function.go)
**Expected Error**: `varying parameters not allowed in public functions`

Shows that public functions (exported with uppercase names) cannot have varying parameters:

```go
func ProcessData(data varying int) varying int {  // ERROR: public function with varying parameter
    return data * 2
}

func processData(data varying int) varying int {  // OK: private function allowed
    return data * 2
}
```

Key violations:
- Public functions with varying parameters
- Public functions returning varying types
- Contrast with private functions (which are allowed)

### [select-with-varying-channels.go](select-with-varying-channels.go)
**Expected Error**: `cannot use varying channel in select statement`

Demonstrates that `select` statements cannot operate on varying channels (`varying chan T`), but channels carrying varying values (`chan varying T`) are now **LEGAL**:

```go
// ILLEGAL: Varying channel type
var ch varying chan int  // ERROR: varying chan T syntax not supported
select {
case data := <-ch:  // ERROR: cannot use varying channel in select statement
    process(data)
}

// LEGAL: Channel carrying varying values (implemented in separate example)
var dataCh chan varying int = make(chan varying int, 5)
select {
case data := <-dataCh:  // OK: chan varying T is legal
    fmt.Printf("Received: %v\n", data)
}
```

Key violations:
- Varying channel types (`varying chan T`) in select cases
- Operations on channels that are themselves varying per-lane
- Contrast with legal channels carrying varying data (`chan varying T`)

**Note**: This example focuses on the still-illegal varying channel syntax. See `examples/select-with-varying-channels/` for comprehensive examples of the now-legal select with channels carrying varying values and infinite SPMD loops (`go for {}`).

### [invalid-lane-constraints.go](invalid-lane-constraints.go)
**Expected Errors**: Various constraint-related errors

Shows invalid uses of lane count constraints:

```go
var data1 varying[0] int      // ERROR: constraint must be positive
var data2 varying[-4] int     // ERROR: constraint must be positive
var data3 varying[n] int      // ERROR: constraint must be compile-time constant
var data4 varying[128] byte   // ERROR: 128×8 = 1024 bits > 512-bit limit
```

Key violations:
- Zero or negative constraints
- Non-constant constraints  
- Constraints exceeding 512-bit practical limit
- Mismatched constraints in operations

**Note**: Constrained varying types are hardware-independent. The compiler handles any valid constraint through loop unrolling, masking, or multiple iterations regardless of the target SIMD capabilities.

### [invalid-contexts.go](invalid-contexts.go)
**Expected Errors**: Various context-related errors

Demonstrates SPMD constructs used in invalid contexts:

```go
// ERROR: lanes.Index() can only be used inside SPMD context
current_lane := lanes.Index()

// ERROR: varying types not allowed at package level
var global_varying varying int = 42

// ERROR: cannot use varying key in map access
var key varying string = data[i]
result := someMap[key]
```

Key violations:
- Using `lanes.*` functions outside SPMD context
- Package-level varying variables
- Varying in interface definitions
- `goto` jumping into/out of SPMD contexts
- **Map restrictions**: Varying map keys prohibited at declaration and access sites
  - `map[varying int]string` not allowed at declaration
  - `someMap[varyingKey]` not allowed at access sites
  - `delete(someMap, varyingKey)` not allowed
  - Only uniform keys permitted for deterministic behavior

### [malformed-syntax.go](malformed-syntax.go)
**Expected Errors**: Various syntax and semantic errors

Shows malformed SPMD syntax and incorrect usage patterns:

```go
func badSignature(uniform varying int) {}  // ERROR: conflicting qualifiers
var x int uniform                          // ERROR: qualifier must come before type
```

Key violations:
- Conflicting type qualifiers
- Incorrect qualifier placement
- Malformed `go for` syntax
- Wrong argument types to built-in functions
- Complex expressions in constraints

## Error Categories

### Type System Violations
- **Varying-to-Uniform Assignment**: Core rule preventing undefined behavior
- **Constraint Mismatches**: Ensuring lane count compatibility
- **Context Violations**: Restricting where SPMD constructs can be used

### Control Flow Restrictions
- **Break Prohibition**: Maintaining SIMD execution coherency
- **Nesting Restrictions**: Avoiding complex mask management in nested `go for` loops
- **SPMD Function Restrictions**: Preventing `go for` in functions that already handle mask parameters
- **Public API Restrictions**: Preventing varying parameters in public functions during experimental phase
- **Goto Restrictions**: Preventing jumps across execution contexts
- **Select Limitations**: Varying channels (`varying chan T`) incompatible with lane-based execution (but channels carrying varying data `chan varying T` are legal)

### Syntax and Semantic Errors
- **Qualifier Conflicts**: Preventing ambiguous type declarations
- **Invalid Expressions**: Ensuring compile-time analyzability
- **Scope Violations**: Package-level vs function-level restrictions

## Testing Guidelines

When implementing the SPMD compiler extensions, these examples should:

1. **Fail to compile** with the specified error messages
2. **Provide clear diagnostics** indicating the violation and suggesting fixes
3. **Fail early** in the compilation pipeline (lexer/parser/type-checker)
4. **Be consistent** with existing Go error message patterns

## Error Message Quality

Good error messages for SPMD violations should:

- **Explain the rule**: Why the construct is illegal
- **Show the location**: Precise source location of the error
- **Suggest alternatives**: When possible, hint at legal alternatives
- **Use consistent terminology**: Match the specification language

Example of a good error message:
```
error: cannot assign varying to uniform
  --> example.go:15:5
   |
15 |     uniform_val = varying_val
   |     ^^^^^^^^^^^^^^^^^^^^^^^^^ varying value cannot be assigned to uniform variable
   |
help: use a reduction operation to convert varying to uniform:
   |     uniform_val = reduce.Add(varying_val)
```

## Legal Alternatives

Each illegal example includes comments showing the legal way to achieve similar functionality:

- **Varying-to-Uniform**: Use reduction operations (`reduce.Add`, `reduce.Any`, etc.) or `reduce.From` for array conversion
- **Early Exit**: Use `continue` with conditions instead of `break`
- **Channel Operations**: Use uniform channels with careful synchronization
- **Cross-Lane Communication**: Use proper `lanes.*` functions in SPMD context
- **Debugging**: Use `reduce.From(varying_value)` to convert varying to array for inspection

## Implementation Notes

These examples assume the full SPMD specification is implemented. Some errors might not occur in partial implementations:

- **Phase 1 (Parser)**: Syntax errors and malformed constructs
- **Phase 2 (Type Checker)**: Type system violations and constraint mismatches  
- **Phase 3 (SSA/Codegen)**: Context violations and optimization conflicts

## Running These Examples

**DO NOT** attempt to run these examples with current Go compilers - they contain experimental language extensions and will fail to compile for different reasons than intended.

When the SPMD Go compiler is implemented with `GOEXPERIMENT=spmd`, these examples should be used in the test suite:

```bash
# These should fail with specific SPMD-related errors
GOEXPERIMENT=spmd go build varying-to-uniform.go        # Expected: "cannot assign varying to uniform"
GOEXPERIMENT=spmd go build break-in-go-for.go          # Expected: "break statement not allowed in SPMD for loop"
GOEXPERIMENT=spmd go build nested-go-for.go            # Expected: "go for loops cannot be nested"
GOEXPERIMENT=spmd go build go-for-in-spmd-function.go  # Expected: "go for loops not allowed in SPMD functions"
GOEXPERIMENT=spmd go build select-with-varying-channels.go # Expected: "cannot use varying channel in select statement"

# Test file headers should include:
// errorcheck -goexperiment spmd
```

The examples serve as regression tests to ensure proper error handling and rejection of illegal constructs when the experimental feature is enabled.