# Legacy Compatibility Examples

This directory contains examples that demonstrate backward compatibility with existing Go code that uses `uniform` and `varying` as regular identifiers (variable names, function names, type names, etc.). 

## Backward Compatibility Principle

The SPMD Go extension must not break existing code. The keywords `uniform` and `varying` should only be treated as type qualifiers in specific syntactic contexts, while preserving their use as regular identifiers everywhere else.

## Examples

Each example is a separate Go module that can be built and run independently.

**Test all examples at once:**
```bash
./build_all.sh
```

### [variables/](variables/)
Tests using `uniform` and `varying` as:
- Variable names in different scopes
- Map keys and values
- Loop variables
- Struct field names

**Build and run:**
```bash
cd variables && go build && go run .
```

### [functions/](functions/) 
Tests using `uniform` and `varying` as:
- Function names
- Method names  
- Function variable names
- Closure variable names

**Build and run:**
```bash
cd functions && go build && go run .
```

### [types/](types/)
Tests using `uniform` and `varying` as:
- Custom type names
- Interface names
- Embedded struct fields
- Type assertions and switches

**Build and run:**
```bash
cd types && go build && go run .
```

### [packages/](packages/)
Tests using `uniform` and `varying` as:
- Package import aliases
- Package-level constants and variables
- Dot import scenarios

**Build and run:**
```bash
cd packages && go build && go run .
```

### [labels/](labels/)
Tests using `uniform` and `varying` as:
- Goto labels
- Labeled loop breaks and continues

**Build and run:**
```bash
cd labels && go build && go run .
```

### [json_tags/](json_tags/)
Tests using `uniform` and `varying` in:
- Struct field names
- JSON/XML/YAML tags
- Database column mappings
- Method names on structs

**Build and run:**
```bash
cd json_tags && go build && go run .
```

## Expected Behavior

### Valid SPMD Type Qualifiers
These contexts should be parsed as SPMD type qualifiers:
```go
var x uniform int          // Type qualifier
var y varying float32      // Type qualifier  
func foo(a uniform int, b varying []byte) // Parameter type qualifiers
```

### Valid Regular Identifiers
These contexts should continue working as regular Go identifiers:
```go
var uniform = 42           // Variable named 'uniform'
func varying() string      // Function named 'varying'
type uniform struct{}      // Type named 'uniform'
uniform := math.Abs        // Package alias
goto uniform               // Label named 'uniform'
```

## Disambiguation Strategy

The compiler should disambiguate based on syntactic context:

1. **After `var`/parameter declarations**: Type qualifier if followed by a type
   - `var x uniform int` → SPMD type qualifier
   - `var uniform int` → Regular variable declaration

2. **In expressions**: Always regular identifier
   - `uniform()` → Function call
   - `uniform.Field` → Package/struct access
   - `uniform = 5` → Variable assignment

3. **After type keywords**: Always regular identifier
   - `type uniform int` → Type definition
   - `interface{ uniform() }` → Method name

4. **In control flow**: Always regular identifier
   - `goto uniform` → Label
   - `break uniform` → Labeled break

## Testing Strategy

Each example should:
1. Compile and run successfully with current Go compiler
2. Continue to work identically when SPMD extensions are added
3. Demonstrate that the keyword context doesn't interfere with regular usage
4. Cover edge cases where disambiguation might be challenging

## Implementation Notes

The lexer/parser changes must be careful to:
- Only recognize `uniform`/`varying` as keywords in type declaration contexts
- Preserve existing identifier behavior in all other contexts
- Provide clear error messages when ambiguous usage occurs
- Maintain full backward compatibility with existing Go code

This ensures that adding SPMD support doesn't break any existing Go programs that happen to use these common English words as identifiers.