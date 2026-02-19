// run -goexperiment spmd -target=wasi

// Example demonstrating varying[] (universal constrained varying) functionality
// Shows how to accept any constrained varying type with proper restrictions
//
// NOTE: varying[] T (universal constrained) has no direct package-based equivalent yet.
// Functions using varying[] T are marked with TODO comments below.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Function accepting any constrained varying using varying[] (TODO: no package equivalent)
// TODO: varying[] int has no direct lanes.Varying equivalent — kept as comment for reference
// func processUniversalConstrained(data varying[] int) varying[] int { ... }
// For now, using lanes.Varying[int] as a placeholder accepting a specific constraint.
func processUniversalConstrained(data lanes.Varying[int]) lanes.Varying[int] {
	// ILLEGAL: Direct operations on varying[] are forbidden
	// result := data + 10        // ERROR: operations forbidden on varying[]
	// if data > 5 { ... }        // ERROR: control flow forbidden on varying[]

	// LEGAL: Type switch to determine specific constraint size
	switch v := data.(type) {
	case lanes.Varying[int, 4]:
		// Can operate on specific constrained type
		fmt.Printf("Processing Varying[int, 4]: %v\n", v)
		return v * 2  // Returns lanes.Varying[int, 4]

	case lanes.Varying[int, 8]:
		// Can operate on specific constrained type
		fmt.Printf("Processing Varying[int, 8]: %v\n", v)
		return v + 100  // Returns lanes.Varying[int, 8]

	case lanes.Varying[int, 16]:
		// Can operate on specific constrained type
		fmt.Printf("Processing Varying[int, 16]: %v\n", v)
		return v / 2  // Returns lanes.Varying[int, 16]

	default:
		// Convert to unconstrained varying for generic processing
		values, masks := lanes.FromConstrained(data)
		fmt.Printf("Converting constrained to unconstrained: %d groups\n", len(values))

		// Process each unconstrained group
		for i, value := range values {
			mask := masks[i]
			fmt.Printf("Group %d - value: %v, mask: %v\n", i, value, mask)

			// Apply operations to unconstrained varying
			processed := value * 3

			// Could reassemble into constrained varying, but we'll return first group
			if i == 0 {
				// For demo, convert back to a known constraint size
				// In practice, you'd need to determine the original constraint
				return lanes.Varying[int, 4](processed)
			}
		}

		// Fallback
		return lanes.Varying[int, 4](0)
	}
}

// Function that converts constrained to unconstrained for generic processing
// TODO: varying[] byte has no direct package equivalent — using lanes.Varying[byte] as placeholder
func convertAndProcess(data lanes.Varying[byte]) {
	fmt.Println("\n=== Converting Constrained to Unconstrained ===")

	// Convert using lanes.FromConstrained
	values, masks := lanes.FromConstrained(data)

	fmt.Printf("Original constrained varying converted to %d unconstrained groups\n", len(values))

	// Process each unconstrained group
	for i, value := range values {
		mask := masks[i]

		fmt.Printf("Group %d:\n", i)
		fmt.Printf("  Values: %v\n", value)
		fmt.Printf("  Mask:   %v\n", mask)

		// Can now use all standard varying operations
		doubled := value * 2
		fmt.Printf("  Doubled: %v\n", doubled)

		// Use reduce operations
		sum := reduce.Add(doubled)
		fmt.Printf("  Sum: %d\n", sum)

		// Use conditional operations with mask
		if reduce.Any(mask) {
			fmt.Printf("  Some lanes active in this group\n")
		}
	}
}

// Demonstrate type switch behavior
// TODO: varying[] float64 has no direct package equivalent — using lanes.Varying[float64] as placeholder
func demonstrateTypeSwitch(data lanes.Varying[float64]) {
	fmt.Println("\n=== Type Switch with varying[] ===")

	switch v := data.(type) {
	case lanes.Varying[float64, 2]:
		fmt.Printf("Detected Varying[float64, 2]: %v\n", v)
		result := v * 1.5
		fmt.Printf("Processed result: %v\n", result)

	case lanes.Varying[float64, 4]:
		fmt.Printf("Detected Varying[float64, 4]: %v\n", v)
		result := v + 0.5
		fmt.Printf("Processed result: %v\n", result)

	case lanes.Varying[float64, 8]:
		fmt.Printf("Detected Varying[float64, 8]: %v\n", v)
		result := v / 2.0
		fmt.Printf("Processed result: %v\n", result)

	default:
		fmt.Printf("Unknown constraint size, converting to unconstrained\n")
		values, masks := lanes.FromConstrained(data)

		for i, value := range values {
			mask := masks[i]
			avg := reduce.Add(value) / float64(lanes.Count(value))
			activeLanes := reduce.Count(mask)
			fmt.Printf("Group %d: avg=%.2f, active_lanes=%d\n", i, avg, activeLanes)
		}
	}
}

// Generic function accepting multiple constrained types
func processMultipleConstraints() {
	fmt.Println("\n=== Multiple Constraint Processing ===")

	// Create different constrained varying types
	data4 := lanes.From([]int{10, 20, 30, 40})
	data8 := lanes.From([]int{1, 2, 3, 4, 5, 6, 7, 8})
	data16 := lanes.From([]int{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115})

	// All can be passed to function accepting lanes.Varying[int]
	result4 := processUniversalConstrained(data4)
	result8 := processUniversalConstrained(data8)
	result16 := processUniversalConstrained(data16)

	fmt.Printf("Result from Varying[int, 4]: %v\n", result4)
	fmt.Printf("Result from Varying[int, 8]: %v\n", result8)
	fmt.Printf("Result from Varying[int, 16]: %v\n", result16)
}

// Demonstrate assignment restrictions
func demonstrateAssignmentRestrictions() {
	fmt.Println("\n=== Assignment Restrictions ===")

	// LEGAL: Constrained varying can be assigned to lanes.Varying[int]
	data4 := lanes.From([]int{1, 2, 3, 4})
	data8 := lanes.From([]int{10, 20, 30, 40, 50, 60, 70, 80})

	// These assignments are legal (constrained to unconstrained)
	var universal4 lanes.Varying[int] = data4  // OK: Varying[int, 4] → Varying[int]
	var universal8 lanes.Varying[int] = data8  // OK: Varying[int, 8] → Varying[int]

	fmt.Printf("Assigned Varying[int, 4] to Varying[int]: %T\n", universal4)
	fmt.Printf("Assigned Varying[int, 8] to Varying[int]: %T\n", universal8)

	// ILLEGAL: Unconstrained varying cannot be assigned to specific constrained type
	unconstrained := lanes.Varying[int](42)  // broadcast 42 to all lanes
	// var invalid lanes.Varying[int, 4] = unconstrained  // ERROR: type mismatch

	fmt.Printf("Unconstrained varying type: %T\n", unconstrained)
	fmt.Println("Note: Cannot assign unconstrained varying to constrained Varying[T, N]")
}

// Helper function showing cross-constraint operations
// TODO: varying[] int parameters have no direct package equivalent — using lanes.Varying[int]
func crossConstraintOperation(a lanes.Varying[int], b lanes.Varying[int]) lanes.Varying[int] {
	// Must determine types via type switch or convert to unconstrained
	valuesA, masksA := lanes.FromConstrained(a)
	valuesB, masksB := lanes.FromConstrained(b)

	if len(valuesA) != len(valuesB) {
		fmt.Printf("Different constraint patterns: A=%d groups, B=%d groups\n",
			len(valuesA), len(valuesB))
		// Could handle mismatched constraints here
	}

	// Process first group for demo
	if len(valuesA) > 0 && len(valuesB) > 0 {
		result := valuesA[0] + valuesB[0]
		combinedMask := masksA[0] && masksB[0]

		fmt.Printf("Cross-constraint operation: %v + %v = %v\n",
			valuesA[0], valuesB[0], result)
		fmt.Printf("Combined mask: %v\n", combinedMask)

		// Return as some constrained type (demo assumes Varying[int, 4])
		return lanes.Varying[int, 4](result)
	}

	return lanes.Varying[int, 4](0)
}

func main() {
	fmt.Println("=== Universal Constrained Varying (varying[]) ===")

	// Test 1: Assignment restrictions
	demonstrateAssignmentRestrictions()

	// Test 2: Multiple constraint processing
	processMultipleConstraints()

	// Test 3: Type switch behavior
	data2 := lanes.From([]float64{3.14, 2.71})
	data4 := lanes.From([]float64{1.0, 2.0, 3.0, 4.0})
	data8 := lanes.From([]float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8})

	demonstrateTypeSwitch(data2)
	demonstrateTypeSwitch(data4)
	demonstrateTypeSwitch(data8)

	// Test 4: Conversion to unconstrained
	byteData4 := lanes.From([]byte{10, 20, 30, 40})
	byteData8 := lanes.From([]byte{1, 2, 3, 4, 5, 6, 7, 8})

	convertAndProcess(byteData4)
	convertAndProcess(byteData8)

	// Test 5: Cross-constraint operations
	fmt.Println("\n=== Cross-Constraint Operations ===")
	intData4 := lanes.From([]int{100, 200, 300, 400})
	intData8 := lanes.From([]int{1, 2, 3, 4, 5, 6, 7, 8})

	crossResult := crossConstraintOperation(intData4, intData8)
	fmt.Printf("Cross-constraint result: %v\n", crossResult)

	// Test 6: Error cases (commented out to avoid compile errors)
	fmt.Println("\n=== Error Cases (Commented Out) ===")
	fmt.Println("// ILLEGAL: Direct operations on varying[]")
	fmt.Println("// func badFunction(data varying[] int) {")
	fmt.Println("//     result := data + 10     // ERROR: operations forbidden")
	fmt.Println("//     if data > 5 { ... }     // ERROR: control flow forbidden")
	fmt.Println("//     for data != 0 { ... }   // ERROR: control flow forbidden")
	fmt.Println("// }")
	fmt.Println("")
	fmt.Println("// ILLEGAL: Assignment from unconstrained to constrained")
	fmt.Println("// var unconstrained lanes.Varying[int] = lanes.Varying[int](42)")
	fmt.Println("// var constrained lanes.Varying[int, 4] = unconstrained  // ERROR")

	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ lanes.Varying[T] accepts any constrained varying type")
	fmt.Println("✓ lanes.Varying[T] does NOT require a specific constraint N")
	fmt.Println("✓ Type switch converts to specific Varying[T, N] constraint")
	fmt.Println("✓ lanes.FromConstrained converts to unconstrained + mask")
	fmt.Println("✓ TODO: varying[] T (universal constrained) syntax not yet supported in package form")
	fmt.Println("✓ All varying universal constrained operations completed successfully")
}
