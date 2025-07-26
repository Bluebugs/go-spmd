// ILLEGAL: Invalid lane count constraints and mismatched constraints
// Expected errors: Various constraint-related errors
package main

import "lanes"

func main() {
	// ILLEGAL: Zero constraint
	var data1 varying[0] int  // ERROR: constraint must be positive

	// ILLEGAL: Negative constraint  
	var data2 varying[-4] int  // ERROR: constraint must be positive

	// ILLEGAL: Non-constant constraint
	var n int = 4
	var data3 varying[n] int  // ERROR: constraint must be compile-time constant

	// ILLEGAL: Mismatched constraints in operations
	var a varying[4] int
	var b varying[8] int
	var result varying[4] int
	
	result = a + b  // ERROR: mismatched lane constraints (4 vs 8)

	// ILLEGAL: Range constraint doesn't match type constraint
	var constrained varying[8] int
	
	go for i := range[4] 100 {  // Processing in groups of 4
		constrained[i] = i  // ERROR: assigning to varying[8] in range[4] context
	}

	// ILLEGAL: Function parameter constraint mismatch
	processGrouped4(constrained)  // ERROR: cannot pass varying[8] to varying[4] parameter
}

func processGrouped4(data varying[4] int) {
	// Process data that must be in groups of 4
}

// ILLEGAL: Constraint that's not a factor/multiple relationship
func invalidFactorConstraints() {
	var data5 varying[5] int
	var data6 varying[3] int
	
	// These constraints don't have a simple multiple relationship
	// which makes it hard for the compiler to reconcile
	go for i := range[6] 100 {  // 6 is LCM of 3 and 2, but not related to 5
		data5[i] = i  // ERROR: constraint mismatch
		data6[i] = i  // This might work since 6 = 2*3
	}
}

// ILLEGAL: Runtime-varying constraint (conceptual)
func runtimeConstraint() {
	// This is conceptually what we can't do - 
	// constraints must be compile-time constants
	for constraint := 4; constraint <= 16; constraint *= 2 {
		// var data varying[constraint] int  // ERROR: non-constant constraint
		_ = constraint
	}
}

// ILLEGAL: Using varying constraint value from another lane
func varyingConstraintValue() {
	go for i := range 4 {
		var lane_constraint varying int = i + 4  // [4,5,6,7]
		
		// This is conceptually impossible - each lane would need
		// a different constraint, but constraints are compile-time
		// var data varying[lane_constraint] int  // ERROR: varying constraint
		_ = lane_constraint
	}
}

// ILLEGAL: Constraint larger than practical limits
func hugeLaneConstraint() {
	// Constraints have practical limits (512-bit maximum)
	var data1 varying[128] byte   // ERROR: 128×8 = 1024 bits > 512-bit limit
	var data2 varying[32] uint16  // OK: 32×16 = 512 bits (at limit)
	var data3 varying[17] uint32  // ERROR: 17×32 = 544 bits > 512-bit limit
	var data4 varying[9] uint64   // ERROR: 9×64 = 576 bits > 512-bit limit
	
	_, _, _, _ = data1, data2, data3, data4
}

// LEGAL: Arbitrary constraints are allowed (hardware-independent)
func arbitraryConstraints() {
	// Any constraint within the 512-bit limit is valid
	var data1 varying[3] int    // OK: compiler handles with unrolling/masking
	var data2 varying[5] int    // OK: compiler handles with unrolling/masking  
	var data3 varying[6] int    // OK: compiler handles with unrolling/masking
	var data4 varying[12] int   // OK: compiler handles with unrolling/masking
	
	_, _, _, _ = data1, data2, data3, data4
}

// ILLEGAL: Mixed constrained and unconstrained in same operation
func mixedConstraints() {
	var constrained varying[4] int
	var unconstrained varying int
	
	// ERROR: cannot mix constrained and unconstrained varying types
	result := constrained + unconstrained
	_ = result
}

// ILLEGAL: Trying to get constraint info at runtime
func runtimeConstraintQuery() {
	var data varying[8] int
	
	// This would be illegal - constraints are compile-time only
	// constraint := lanes.Constraint(data)  // ERROR: no such function
	// if constraint == 8 { ... }
	
	_ = data
}

// LEGAL: Proper constraint usage (for comparison)
func legalConstraints() {
	var data4 varying[4] int
	var data8 varying[8] int
	
	// Process data4 in groups of 4
	go for _, v := range[4] data4 {
		v = v * 2
	}

	// Process data8 in groups of 8
	go for i := range[8] data8 {
		data8[i] = i + 1
	}
	
	// Operations between same constraints are legal
	var result4 varying[4] int = data4 * 2
	var result8 varying[8] int = data8 + 10
	
	_, _ = result4, result8
}