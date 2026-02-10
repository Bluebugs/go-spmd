// ILLEGAL: Invalid lane count constraints and mismatched constraints
// Expected errors: Various constraint-related errors
package main

import "lanes"

func main() {
	// LEGAL: Universal constraint (0 in new syntax represents universal [])
	var data1 lanes.Varying[int, 0]  // This is LEGAL - universal constraint

	// NOTE: Negative constraints not expressible in new package-based syntax
	// var data2 lanes.Varying[int, -4]  // Syntax error, can't have negative type parameter

	// NOTE: Non-constant constraints not expressible in new package-based syntax
	var n int = 4
	// var data3 lanes.Varying[int, n]  // Syntax error: type parameter must be constant

	// ILLEGAL: Mismatched constraints in operations
	var a lanes.Varying[int, 4]
	var b lanes.Varying[int, 8]
	var result lanes.Varying[int, 4]

	result = a + b  // ERROR: mismatched lane constraints (4 vs 8)

	// ILLEGAL: Range constraint doesn't match type constraint
	var constrained lanes.Varying[int, 8]

	go for i := range[4] 100 {  // Processing in groups of 4
		constrained[i] = i  // ERROR: assigning to lanes.Varying[int, 8] in range[4] context
	}

	// ILLEGAL: Function parameter constraint mismatch
	processGrouped4(constrained)  // ERROR: cannot pass lanes.Varying[int, 8] to lanes.Varying[int, 4] parameter
}

func processGrouped4(data lanes.Varying[int, 4]) {
	// Process data that must be in groups of 4
}

// ILLEGAL: Constraint that's not a factor/multiple relationship
func invalidFactorConstraints() {
	var data5 lanes.Varying[int, 5]
	var data6 lanes.Varying[int, 3]

	// These constraints don't have a simple multiple relationship
	// which makes it hard for the compiler to reconcile
	go for i := range[6] 100 {  // 6 is LCM of 3 and 2, but not related to 5
		data5[i] = i  // ERROR: constraint mismatch
		data6[i] = i  // This might work since 6 = 2*3
	}
}

// NOTE: Runtime-varying constraint conceptually impossible with package-based syntax
func runtimeConstraint() {
	// This is conceptually what we can't do -
	// constraints must be compile-time constants
	for constraint := 4; constraint <= 16; constraint *= 2 {
		// var data lanes.Varying[int, constraint]  // Syntax error: non-constant constraint
		_ = constraint
	}
}

// NOTE: Using varying constraint value conceptually impossible with package-based syntax
func varyingConstraintValue() {
	go for i := range 4 {
		var lane_constraint lanes.Varying[int] = i + 4  // [4,5,6,7]

		// This is conceptually impossible - each lane would need
		// a different constraint, but constraints are compile-time
		// var data lanes.Varying[int, lane_constraint]  // Syntax error: varying constraint
		_ = lane_constraint
	}
}

// ILLEGAL: Constraint larger than practical limits
func hugeLaneConstraint() {
	// Constraints have practical limits (512-bit maximum)
	var data1 lanes.Varying[byte, 128]    // ERROR: 128×8 = 1024 bits > 512-bit limit
	var data2 lanes.Varying[uint16, 32]   // OK: 32×16 = 512 bits (at limit)
	var data3 lanes.Varying[uint32, 17]   // ERROR: 17×32 = 544 bits > 512-bit limit
	var data4 lanes.Varying[uint64, 9]    // ERROR: 9×64 = 576 bits > 512-bit limit

	_, _, _, _ = data1, data2, data3, data4
}

// LEGAL: Arbitrary constraints are allowed (hardware-independent)
func arbitraryConstraints() {
	// Any constraint within the 512-bit limit is valid
	var data1 lanes.Varying[int, 3]    // OK: compiler handles with unrolling/masking
	var data2 lanes.Varying[int, 5]    // OK: compiler handles with unrolling/masking
	var data3 lanes.Varying[int, 6]    // OK: compiler handles with unrolling/masking
	var data4 lanes.Varying[int, 12]   // OK: compiler handles with unrolling/masking

	_, _, _, _ = data1, data2, data3, data4
}

// ILLEGAL: Mixed constrained and unconstrained in same operation
func mixedConstraints() {
	var constrained lanes.Varying[int, 4]
	var unconstrained lanes.Varying[int]

	// ERROR: cannot mix constrained and unconstrained varying types
	result := constrained + unconstrained
	_ = result
}

// NOTE: Runtime constraint query not available (constraints are compile-time only)
func runtimeConstraintQuery() {
	var data lanes.Varying[int, 8]

	// This would be illegal - constraints are compile-time only
	// constraint := lanes.Constraint(data)  // ERROR: no such function
	// if constraint == 8 { ... }

	_ = data
}

// LEGAL: Proper constraint usage (for comparison)
func legalConstraints() {
	var data4 lanes.Varying[int, 4]
	var data8 lanes.Varying[int, 8]

	// Process data4 in groups of 4
	go for _, v := range[4] data4 {
		v = v * 2
	}

	// Process data8 in groups of 8
	go for i := range[8] data8 {
		data8[i] = i + 1
	}

	// Operations between same constraints are legal
	var result4 lanes.Varying[int, 4] = data4 * 2
	var result8 lanes.Varying[int, 8] = data8 + 10

	_, _ = result4, result8
}
