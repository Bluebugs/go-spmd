// run -goexperiment spmd -target=wasi

// Example demonstrating varying[] (universal constrained varying) functionality
// Shows how to accept any constrained varying type with proper restrictions
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Function accepting any constrained varying using varying[]
func processUniversalConstrained(data varying[] int) varying[] int {
	// ILLEGAL: Direct operations on varying[] are forbidden
	// result := data + 10        // ERROR: operations forbidden on varying[]
	// if data > 5 { ... }        // ERROR: control flow forbidden on varying[]
	
	// LEGAL: Type switch to determine specific constraint size
	switch v := data.(type) {
	case varying[4] int:
		// Can operate on specific constrained type
		fmt.Printf("Processing varying[4]: %v\n", v)
		return v * 2  // Returns varying[4] int, converted to varying[] int
		
	case varying[8] int:
		// Can operate on specific constrained type
		fmt.Printf("Processing varying[8]: %v\n", v)
		return v + 100  // Returns varying[8] int, converted to varying[] int
		
	case varying[16] int:
		// Can operate on specific constrained type
		fmt.Printf("Processing varying[16]: %v\n", v)
		return v / 2  // Returns varying[16] int, converted to varying[] int
		
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
				return varying[4](processed)  // Assumes original was multiple of 4
			}
		}
		
		// Fallback
		return varying[4](varying(0))
	}
}

// Function that converts varying[] to unconstrained for generic processing
func convertAndProcess(data varying[] byte) {
	fmt.Println("\n=== Converting Constrained to Unconstrained ===")
	
	// Convert using lanes.FromConstrained
	values, masks := lanes.FromConstrained(data)
	
	fmt.Printf("Original constrained varying[] converted to %d unconstrained groups\n", len(values))
	
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

// Demonstrate type switch behavior with varying[]
func demonstrateTypeSwitch(data varying[] float64) {
	fmt.Println("\n=== Type Switch with varying[] ===")
	
	switch v := data.(type) {
	case varying[2] float64:
		fmt.Printf("Detected varying[2] float64: %v\n", v)
		result := v * 1.5
		fmt.Printf("Processed result: %v\n", result)
		
	case varying[4] float64:
		fmt.Printf("Detected varying[4] float64: %v\n", v)
		result := v + 0.5
		fmt.Printf("Processed result: %v\n", result)
		
	case varying[8] float64:
		fmt.Printf("Detected varying[8] float64: %v\n", v)
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
	data4 := varying[4]([4]int{10, 20, 30, 40})
	data8 := varying[8]([8]int{1, 2, 3, 4, 5, 6, 7, 8})
	data16 := varying[16]([16]int{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115})
	
	// All can be passed to function accepting varying[]
	result4 := processUniversalConstrained(data4)
	result8 := processUniversalConstrained(data8)
	result16 := processUniversalConstrained(data16)
	
	fmt.Printf("Result from varying[4]: %v\n", result4)
	fmt.Printf("Result from varying[8]: %v\n", result8)  
	fmt.Printf("Result from varying[16]: %v\n", result16)
}

// Demonstrate assignment restrictions
func demonstrateAssignmentRestrictions() {
	fmt.Println("\n=== Assignment Restrictions ===")
	
	// LEGAL: Constrained varying can be assigned to varying[]
	data4 := varying[4]([4]int{1, 2, 3, 4})
	data8 := varying[8]([8]int{10, 20, 30, 40, 50, 60, 70, 80})
	
	// These assignments are legal
	var universal4 varying[] int = data4  // OK: varying[4] → varying[]
	var universal8 varying[] int = data8  // OK: varying[8] → varying[]
	
	fmt.Printf("Assigned varying[4] to varying[]: %T\n", universal4)
	fmt.Printf("Assigned varying[8] to varying[]: %T\n", universal8)
	
	// ILLEGAL: Unconstrained varying cannot be assigned to varying[]
	unconstrained := varying(42)
	// var invalid varying[] int = unconstrained  // ERROR: type mismatch
	
	fmt.Printf("Unconstrained varying type: %T\n", unconstrained)
	fmt.Println("Note: Cannot assign unconstrained varying to varying[]")
}

// Helper function showing varying[] in function signature
func crossConstraintOperation(a varying[] int, b varying[] int) varying[] int {
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
		
		// Return as some constrained type (demo assumes varying[4])
		return varying[4](result)
	}
	
	return varying[4](varying(0))
}

func main() {
	fmt.Println("=== Universal Constrained Varying (varying[]) ===")
	
	// Test 1: Assignment restrictions
	demonstrateAssignmentRestrictions()
	
	// Test 2: Multiple constraint processing
	processMultipleConstraints()
	
	// Test 3: Type switch behavior
	data2 := varying[2]([2]float64{3.14, 2.71})
	data4 := varying[4]([4]float64{1.0, 2.0, 3.0, 4.0})
	data8 := varying[8]([8]float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8})
	
	demonstrateTypeSwitch(data2)
	demonstrateTypeSwitch(data4)
	demonstrateTypeSwitch(data8)
	
	// Test 4: Conversion to unconstrained
	byteData4 := varying[4]([4]byte{10, 20, 30, 40})
	byteData8 := varying[8]([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	
	convertAndProcess(byteData4)
	convertAndProcess(byteData8)
	
	// Test 5: Cross-constraint operations
	fmt.Println("\n=== Cross-Constraint Operations ===")
	intData4 := varying[4]([4]int{100, 200, 300, 400})
	intData8 := varying[8]([8]int{1, 2, 3, 4, 5, 6, 7, 8})
	
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
	fmt.Println("// ILLEGAL: Assignment from unconstrained")
	fmt.Println("// var unconstrained varying int = varying(42)")
	fmt.Println("// var universal varying[] int = unconstrained  // ERROR")
	
	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ varying[] accepts any constrained varying type")
	fmt.Println("✓ varying[] does NOT accept unconstrained varying")
	fmt.Println("✓ Direct operations on varying[] are forbidden")
	fmt.Println("✓ Type switch converts varying[] to specific constraint")
	fmt.Println("✓ lanes.FromConstrained converts to unconstrained + mask")
	fmt.Println("✓ All varying[] universal constrained operations completed successfully")
}