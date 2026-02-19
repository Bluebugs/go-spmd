// ILLEGAL: Malformed SPMD syntax and constructs
// Expected errors: Various syntax and semantic errors
package main

import (
	"lanes"
	"reduce"
	_ "math"  // Legal: blank import
)

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

	_ = fn
}

// ILLEGAL: Function signature mixing uniform/varying incorrectly
func badSignature1(uniform_and_varying lanes.Varying[int]) {  // ERROR: cannot have both uniform and varying qualifiers
	// Implementation
}

func badSignature2(a int) {  // OK: uniform is the default - no qualifier needed
	// Implementation
}

func badSignature3() {  // NOTE: "varying uniform int" concept doesn't exist in package-based syntax
	// Implementation
}

// ILLEGAL: Return type with incorrect qualifier placement
// (These are just regular Go functions in package-based syntax)
func badReturn1() int {
	return 42
}

func badReturn2() int {
	return 42
}

// ILLEGAL: Variable declarations with malformed qualifiers
func badVariables() {
	// In package-based syntax, just use lanes.Varying[T] - no qualifier keywords

	// ILLEGAL: Missing type after qualifier
	// (Not applicable in package-based syntax - use lanes.Varying[T] directly)
}

// ILLEGAL: Type conversions between incompatible varying types
func badConversions() {
	var a lanes.Varying[int, 4]
	var b lanes.Varying[int, 8]

	// ERROR: cannot convert between different lane constraints
	a = lanes.Varying[int, 4](b)

	_, _ = a, b
}

// ILLEGAL: lanes functions with wrong argument types
func badLanesFunctions() {
	go for i := range 10 {
		var data lanes.Varying[int] = i

		// ERROR: lanes.Broadcast expects uniform lane index
		var varying_lane lanes.Varying[int] = i % 4
		result1 := lanes.Broadcast(data, varying_lane)

		// ERROR: lanes.Rotate expects uniform offset
		var varying_offset lanes.Varying[int] = i
		result2 := lanes.Rotate(data, varying_offset)

		// ERROR: lanes.Count expects type, not value
		count := lanes.Count(data)  // Should be lanes.Count[int]()

		_, _, _ = result1, result2, count
	}
}

// ILLEGAL: reduce functions with wrong types
func badReduceFunctions() {
	go for i := range 10 {
		var uniform_data int = 42

		// ERROR: reduce functions expect varying input
		sum := reduce.Add(uniform_data)

		// ERROR: reduce.FindFirstSet expects varying bool
		var varying_int lanes.Varying[int] = i
		first := reduce.FindFirstSet(varying_int)

		_, _ = sum, first
	}
}

// ILLEGAL: Constraint expressions that aren't compile-time constants
func badConstraints() {
	const CONST_4 = 4
	var var_4 int = 4

	// LEGAL: compile-time constant
	var legal lanes.Varying[int, CONST_4]

	// ILLEGAL: runtime variable
	// var illegal1 lanes.Varying[int, var_4]  // ERROR: constraint must be compile-time constant

	// ILLEGAL: complex expression
	// var illegal2 lanes.Varying[int, CONST_4 + var_4]  // ERROR: constraint not compile-time constant

	_ = legal
	_ = var_4
}

// ILLEGAL: Using SPMD constructs in wrong grammatical positions
func wrongPositions() {
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

// ILLEGAL: Package-level SPMD constructs
// go for i := range 10 {  // ERROR: SPMD constructs only allowed in function bodies
//     globalData[i] = i
// }

var globalData [10]int
