// ILLEGAL: Malformed SPMD syntax and constructs
// Expected errors: Various syntax and semantic errors
package main

import "lanes"

func main() {
	// ILLEGAL: go for with wrong syntax
	// go i := range 10 {  // ERROR: missing 'for' keyword
	//     process(i)
	// }

	// ILLEGAL: go for without range
	// go for i := 0; i < 10; i++ {  // ERROR: SPMD for must use range
	//     process(i)
	// }

	// ILLEGAL: go for with multiple variables (not currently supported)
	// go for i, j := range someDoubleRange {  // ERROR: multiple variables not supported
	//     process(i, j)
	// }

	// ILLEGAL: Empty range expression
	// go for i := range {  // ERROR: missing range expression
	//     process(i)
	// }

	// ILLEGAL: Range with complex expression that can't be analyzed
	var fn func() int = func() int { return 10 }
	// go for i := range fn() {  // ERROR: range expression too complex
	//     process(i)
	// }

	// ILLEGAL: Nested go for with variable capture issues
	data := [][]int{{1, 2}, {3, 4}, {5, 6}}
	
	go for i := range len(data) {
		// The inner loop variable would conflict with outer
		// go for i := range len(data[i]) {  // ERROR: variable 'i' redeclared
		//     process(data[i][i])
		// }
		_ = data[i]
	}
}

// ILLEGAL: Function signature mixing uniform/varying incorrectly
func badSignature1(uniform varying int) {  // ERROR: cannot have both uniform and varying qualifiers
	// Implementation
}

func badSignature2(a int uniform) {  // ERROR: qualifier must come before type
	// Implementation  
}

func badSignature3(varying uniform int) {  // ERROR: conflicting qualifiers
	// Implementation
}

// ILLEGAL: Return type with incorrect qualifier placement
func badReturn1() int varying {  // ERROR: qualifier must come before type
	return 42
}

func badReturn2() uniform varying int {  // ERROR: conflicting qualifiers
	return 42
}

// ILLEGAL: Variable declarations with malformed qualifiers
func badVariables() {
	// var uniform varying x int  // ERROR: conflicting qualifiers on same variable
	// var x uniform varying int  // ERROR: conflicting qualifiers
	// var x int uniform  // ERROR: qualifier must come before type
	
	// ILLEGAL: Missing type after qualifier
	// var x uniform  // ERROR: missing type after uniform qualifier
	// var y varying  // ERROR: missing type after varying qualifier
}

// ILLEGAL: Type conversions between incompatible varying types
func badConversions() {
	var a varying[4] int
	var b varying[8] int
	
	// ERROR: cannot convert between different lane constraints
	a = varying[4] int(b)
	
	_, _ = a, constrained
}

// ILLEGAL: lanes functions with wrong argument types
func badLanesFunctions() {
	go for i := range 10 {
		var data varying int = i
		
		// ERROR: lanes.Broadcast expects uniform lane index
		var varying_lane varying int = i % 4
		result1 := lanes.Broadcast(data, varying_lane)
		
		// ERROR: lanes.Rotate expects uniform offset
		var varying_offset varying int = i
		result2 := lanes.Rotate(data, varying_offset)
		
		// ERROR: lanes.Count expects type, not value
		count := lanes.Count(data)  // Should be lanes.Count(int{})
		
		_, _, _ = result1, result2, count
	}
}

// ILLEGAL: reduce functions with wrong types
func badReduceFunctions() {
	go for i := range 10 {
		var uniform_data uniform int = 42
		
		// ERROR: reduce functions expect varying input
		sum := reduce.Add(uniform_data)
		
		// ERROR: reduce.FindFirstSet expects varying bool
		var varying_int varying int = i
		first := reduce.FindFirstSet(varying_int)
		
		_, _ = sum, first
	}
}

// ILLEGAL: Constraint expressions that aren't compile-time constants
func badConstraints() {
	const CONST_4 = 4
	var var_4 int = 4
	
	// LEGAL: compile-time constant
	var legal varying[CONST_4] int
	
	// ILLEGAL: runtime variable
	// var illegal1 varying[var_4] int  // ERROR: constraint must be compile-time constant
	
	// ILLEGAL: complex expression
	// var illegal2 varying[CONST_4 + var_4] int  // ERROR: constraint not compile-time constant
	
	_ = legal
}

// ILLEGAL: Using SPMD constructs in wrong grammatical positions
func wrongPositions() {
	// ERROR: varying qualifier in wrong position
	// func varying localFunc() {}  // ERROR: varying not allowed on local function
	
	// ERROR: go for in expression context
	// result := (go for i := range 10 { return i })  // ERROR: go for is statement, not expression
	
	// ERROR: Trying to assign go for loop
	// loop := go for i := range 10 { process(i) }  // ERROR: cannot assign statement
}

// ILLEGAL: Complex nesting that creates ambiguity
func ambiguousNesting() {
	// This creates parsing ambiguity - is the inner for SPMD or regular?
	go for i := range 10 {
		// Without explicit 'go', this should be regular for loop
		for j := range 5 {  // This is regular for loop
			if i+j > 10 {
				// break  // This should be legal since it's regular for
			}
		}
		
		// ILLEGAL: go for inside another go for
		func() {
			// Anonymous function inside SPMD context are SPMD
			// Nested `go for` inside a SPMD context are not allowed
			go for k := range 3 {
				process(k)
			}
		}()
	}
}

func process(x int) {
	// Implementation
}

// Helper imports that might cause issues
import (
	// ERROR: Cannot import packages with conflicting names
	// uniform "math"      // This conflicts with uniform keyword in some contexts
	// varying "strings"   // This conflicts with varying keyword in some contexts
	_ "math"  // Legal: blank import
)

// ILLEGAL: Package-level SPMD constructs
// go for i := range 10 {  // ERROR: SPMD constructs only allowed in function bodies
//     globalData[i] = i
// }

var globalData [10]int