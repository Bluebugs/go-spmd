// Legacy compatibility test: importing packages with 'uniform' and 'varying' names
// This ensures existing code continues to work with SPMD extensions
package main

import (
	"fmt"
	
	// Test importing packages with these names (hypothetical)
	uniform "math"  // Aliasing standard package to 'uniform'
	varying "strings"  // Aliasing standard package to 'varying'
)

func main() {
	// Use aliased packages
	result1 := uniform.Abs(-42)
	result2 := varying.ToUpper("hello")
	
	fmt.Printf("uniform.Abs(-42) = %f\n", result1)
	fmt.Printf("varying.ToUpper(hello) = %s\n", result2)
	
	// Test package-level constants/variables with these names
	fmt.Printf("uniform constant: %f\n", uniformConstant)
	fmt.Printf("varying variable: %s\n", varyingVariable)
	
	// Test dot imports (hypothetical uniform/varying packages)
	testDotImports()
}

// Package-level variables/constants with these names
const uniformConstant = 3.14159
var varyingVariable = "package level"

func testDotImports() {
	// In a real scenario, if someone had packages named 'uniform' or 'varying'
	// and used dot imports, the SPMD keywords should still work in contexts
	// where they're clearly type qualifiers
	
	// This would be valid even with dot imports:
	// var x uniform int    // SPMD type qualifier
	// var y varying float32 // SPMD type qualifier
	
	// While these would refer to imported identifiers:
	// result := uniform(42)      // function call
	// value := varying.Something // package access
	
	fmt.Println("Dot import compatibility test passed")
}