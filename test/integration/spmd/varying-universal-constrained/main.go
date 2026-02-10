// run -goexperiment spmd -target=wasi

// Example demonstrating lanes.Varying[T, 0] (universal constrained varying) functionality
// Shows how to accept any constrained varying type with proper restrictions
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Function accepting any constrained varying using lanes.Varying[T, 0]
func processUniversalConstrained(data lanes.Varying[int, 0]) lanes.Varying[int, 0] {
	// ILLEGAL: Direct operations on lanes.Varying[T, 0] are forbidden
	// result := data + 10        // ERROR: operations forbidden on lanes.Varying[T, 0]
	// if data > 5 { ... }        // ERROR: control flow forbidden on lanes.Varying[T, 0]

	// LEGAL: Type switch to determine specific constraint size
	switch v := data.(type) {
	case lanes.Varying[int, 4]:
		// Can operate on specific constrained type
		fmt.Printf("Processing lanes.Varying[int, 4]: %v\n", v)
		return v * 2  // Returns lanes.Varying[int, 4], converted to lanes.Varying[int, 0]

	case lanes.Varying[int, 8]:
		// Can operate on specific constrained type
		fmt.Printf("Processing lanes.Varying[int, 8]: %v\n", v)
		return v + 100  // Returns lanes.Varying[int, 8], converted to lanes.Varying[int, 0]

	case lanes.Varying[int, 16]:
		// Can operate on specific constrained type
		fmt.Printf("Processing lanes.Varying[int, 16]: %v\n", v)
		return v / 2  // Returns lanes.Varying[int, 16], converted to lanes.Varying[int, 0]

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
				return lanes.Varying[int, 4](processed)  // Assumes original was multiple of 4
			}
		}

		// Fallback
		return lanes.Varying[int, 4](lanes.Varying[int](0))
	}
}

// Function that converts lanes.Varying[T, 0] to unconstrained for generic processing
func convertAndProcess(data lanes.Varying[byte, 0]) {
	fmt.Println("\n=== Converting Constrained to Unconstrained ===")

	// Convert using lanes.FromConstrained
	values, masks := lanes.FromConstrained(data)

	fmt.Printf("Original constrained lanes.Varying[T, 0] converted to %d unconstrained groups\n", len(values))

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

// Demonstrate type switch behavior with lanes.Varying[T, 0]
func demonstrateTypeSwitch(data lanes.Varying[float64, 0]) {
	fmt.Println("\n=== Type Switch with lanes.Varying[T, 0] ===")

	switch v := data.(type) {
	case lanes.Varying[float64, 2]:
		fmt.Printf("Detected lanes.Varying[float64, 2]: %v\n", v)
		result := v * 1.5
		fmt.Printf("Processed result: %v\n", result)

	case lanes.Varying[float64, 4]:
		fmt.Printf("Detected lanes.Varying[float64, 4]: %v\n", v)
		result := v + 0.5
		fmt.Printf("Processed result: %v\n", result)

	case lanes.Varying[float64, 8]:
		fmt.Printf("Detected lanes.Varying[float64, 8]: %v\n", v)
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
	data4 := lanes.Varying[int, 4]([4]int{10, 20, 30, 40})
	data8 := lanes.Varying[int, 8]([8]int{1, 2, 3, 4, 5, 6, 7, 8})
	data16 := lanes.Varying[int, 16]([16]int{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115})

	// All can be passed to function accepting lanes.Varying[T, 0]
	result4 := processUniversalConstrained(data4)
	result8 := processUniversalConstrained(data8)
	result16 := processUniversalConstrained(data16)

	fmt.Printf("Result from lanes.Varying[int, 4]: %v\n", result4)
	fmt.Printf("Result from lanes.Varying[int, 8]: %v\n", result8)
	fmt.Printf("Result from lanes.Varying[int, 16]: %v\n", result16)
}

// Demonstrate assignment restrictions
func demonstrateAssignmentRestrictions() {
	fmt.Println("\n=== Assignment Restrictions ===")

	// LEGAL: Constrained varying can be assigned to lanes.Varying[T, 0]
	data4 := lanes.Varying[int, 4]([4]int{1, 2, 3, 4})
	data8 := lanes.Varying[int, 8]([8]int{10, 20, 30, 40, 50, 60, 70, 80})

	// These assignments are legal
	var universal4 lanes.Varying[int, 0] = data4  // OK: lanes.Varying[int, 4] → lanes.Varying[int, 0]
	var universal8 lanes.Varying[int, 0] = data8  // OK: lanes.Varying[int, 8] → lanes.Varying[int, 0]

	fmt.Printf("Assigned lanes.Varying[int, 4] to lanes.Varying[int, 0]: %T\n", universal4)
	fmt.Printf("Assigned lanes.Varying[int, 8] to lanes.Varying[int, 0]: %T\n", universal8)

	// ILLEGAL: Unconstrained varying cannot be assigned to lanes.Varying[T, 0]
	unconstrained := lanes.Varying[int](42)
	// var invalid lanes.Varying[int, 0] = unconstrained  // ERROR: type mismatch

	fmt.Printf("Unconstrained varying type: %T\n", unconstrained)
	fmt.Println("Note: Cannot assign unconstrained varying to lanes.Varying[T, 0]")
}

// Helper function showing lanes.Varying[T, 0] in function signature
func crossConstraintOperation(a lanes.Varying[int, 0], b lanes.Varying[int, 0]) lanes.Varying[int, 0] {
	// Must determine types via type switch or convert to unconstrained

	// Simple approach: convert both to unconstrained and process
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

		// Return as some constrained type (demo assumes lanes.Varying[int, 4])
		return lanes.Varying[int, 4](result)
	}

	return lanes.Varying[int, 4](lanes.Varying[int](0))
}

func main() {
	fmt.Println("=== Universal Constrained Varying (lanes.Varying[T, 0]) ===")

	// Test 1: Assignment restrictions
	demonstrateAssignmentRestrictions()

	// Test 2: Multiple constraint processing
	processMultipleConstraints()

	// Test 3: Type switch behavior
	data2 := lanes.Varying[float64, 2]([2]float64{3.14, 2.71})
	data4 := lanes.Varying[float64, 4]([4]float64{1.0, 2.0, 3.0, 4.0})
	data8 := lanes.Varying[float64, 8]([8]float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8})

	demonstrateTypeSwitch(data2)
	demonstrateTypeSwitch(data4)
	demonstrateTypeSwitch(data8)

	// Test 4: Conversion to unconstrained
	byteData4 := lanes.Varying[byte, 4]([4]byte{10, 20, 30, 40})
	byteData8 := lanes.Varying[byte, 8]([8]byte{1, 2, 3, 4, 5, 6, 7, 8})

	convertAndProcess(byteData4)
	convertAndProcess(byteData8)

	// Test 5: Cross-constraint operations
	fmt.Println("\n=== Cross-Constraint Operations ===")
	intData4 := lanes.Varying[int, 4]([4]int{100, 200, 300, 400})
	intData8 := lanes.Varying[int, 8]([8]int{1, 2, 3, 4, 5, 6, 7, 8})

	crossResult := crossConstraintOperation(intData4, intData8)
	fmt.Printf("Cross-constraint result: %v\n", crossResult)

	// Test 6: Error cases (commented out to avoid compile errors)
	fmt.Println("\n=== Error Cases (Commented Out) ===")
	fmt.Println("// ILLEGAL: Direct operations on lanes.Varying[T, 0]")
	fmt.Println("// func badFunction(data lanes.Varying[int, 0]) {")
	fmt.Println("//     result := data + 10     // ERROR: operations forbidden")
	fmt.Println("//     if data > 5 { ... }     // ERROR: control flow forbidden")
	fmt.Println("//     for data != 0 { ... }   // ERROR: control flow forbidden")
	fmt.Println("// }")
	fmt.Println("")
	fmt.Println("// ILLEGAL: Assignment from unconstrained")
	fmt.Println("// var unconstrained lanes.Varying[int] = lanes.Varying[int](42)")
	fmt.Println("// var universal lanes.Varying[int, 0] = unconstrained  // ERROR")

	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ lanes.Varying[T, 0] accepts any constrained varying type")
	fmt.Println("✓ lanes.Varying[T, 0] does NOT accept unconstrained varying")
	fmt.Println("✓ Direct operations on lanes.Varying[T, 0] are forbidden")
	fmt.Println("✓ Type switch converts lanes.Varying[T, 0] to specific constraint")
	fmt.Println("✓ lanes.FromConstrained converts to unconstrained + mask")
	fmt.Println("✓ All lanes.Varying[T, 0] universal constrained operations completed successfully")
}
