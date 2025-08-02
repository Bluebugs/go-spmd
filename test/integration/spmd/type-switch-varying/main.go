// run -goexperiment spmd -target=wasi

// Example demonstrating type switches with varying types
// Shows explicit varying type cases and constrained varying support
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// processVaryingInterface demonstrates type switch with varying interface{}
func processVaryingInterface(value varying interface{}) {
	fmt.Printf("Processing varying interface{}: %T\n", value)
	
	// Type switch with explicit varying type cases
	switch v := value.(type) {
	case varying int:
		// Handle varying int - each lane processes its own int value
		result := v * 2
		fmt.Printf("Varying int case: %v\n", result)
		
	case varying string:
		// Handle varying string - each lane processes its own string
		length := len(v)
		fmt.Printf("Varying string case, length: %v\n", length)
		
	case varying float64:
		// Handle varying float64 - each lane processes its own float
		squared := v * v
		fmt.Printf("Varying float64 case, squared: %v\n", squared)
		
	case varying [4]int:
		// Handle varying array - each lane processes its own array
		sum := v[0] + v[1] + v[2] + v[3]
		fmt.Printf("Varying [4]int case, sum: %v\n", sum)
		
	case varying[8] byte:
		// Handle constrained varying - requires multiple of 8 lanes
		processed := v + 10
		fmt.Printf("Constrained varying[8] byte case: %v\n", processed)
		
	case int: // NOT PERMITTED: must be explicit varying type
		// Handle uniform int type
		fmt.Printf("Uniform int case: %d\n", v)
		
	case string: // NOT PERMITTED: must be explicit varying type
		// Handle uniform string type
		fmt.Printf("Uniform string case: %s\n", v)
		
	default:
		// Handle other types
		fmt.Printf("Unknown type case: %T\n", v)
	}
}

// processMixedInterface demonstrates mixed uniform/varying handling
func processMixedInterface(value interface{}) {
	fmt.Printf("Processing mixed interface{}: %T\n", value)
	
	switch v := value.(type) {
	case varying int:
		// This can handle varying int values
		doubled := v * 2
		fmt.Printf("Received varying int: %v\n", doubled)
		
	case int:
		// This handles uniform int values
		fmt.Printf("Received uniform int: %d\n", v)
		
	case varying string:
		// This can handle varying string values
		lengths := len(v)
		fmt.Printf("Received varying string, lengths: %v\n", lengths)
		
	case string:
		// This handles uniform string values
		fmt.Printf("Received uniform string: %s\n", v)
		
	default:
		fmt.Printf("Unknown mixed type: %T\n", v)
	}
}

// demonstrateConstrainedTypes shows constrained varying type switches
func demonstrateConstrainedTypes() {
	fmt.Println("\n=== Constrained Varying Type Switches ===")
	
	// Create constrained varying types using lanes.From
	uniformData := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	constrainedData := lanes.From[8](uniformData)  // Creates varying byte
	
	// Process as varying interface{}
	var varyingInterface varying interface{} = constrainedData
	
	switch v := varyingInterface.(type) {
	case varying[8] byte:
		fmt.Printf("Matched varying[8] byte: %v\n", v)
		
	case varying[4] int:
		fmt.Printf("Matched varying[4] int: %v\n", v)
		
	case varying byte:
		fmt.Printf("Matched unconstrained varying byte: %v\n", v)
		
	default:
		fmt.Printf("No constrained match: %T\n", v)
	}
}

// demonstrateTypeAssertions shows type assertions with varying
func demonstrateTypeAssertions() {
	fmt.Println("\n=== Type Assertions with Varying ===")
	
	var varyingInterface varying interface{} = varying(42)
	
	// Correct type assertion with explicit varying
	if v, ok := varyingInterface.(varying int); ok {
		result := v + 10
		fmt.Printf("Type assertion success: %v\n", result)
	}
	
	// Demonstrate failure case handling
	if _, ok := varyingInterface.(varying string); !ok {
		fmt.Println("Type assertion failed as expected (not a varying string)")
	}
	
	// Mixed interface with uniform value
	var mixedInterface interface{} = 24
	if v, ok := mixedInterface.(int); ok {
		fmt.Printf("Uniform type assertion: %d\n", v)
	}
}

// processDataWithTypeSwitch demonstrates realistic usage
func processDataWithTypeSwitch(data []interface{}) {
	fmt.Println("\n=== Processing Mixed Data Types ===")
	
	go for _, item := range data {
		// item is varying interface{} here
		switch v := item.(type) {
		case varying int:
			// Process varying integers
			processed := v * v
			fmt.Printf("Squared varying int: %v\n", processed)
			
		case varying float64:
			// Process varying floats
			rounded := int(v + 0.5)
			fmt.Printf("Rounded varying float: %v\n", rounded)
			
		case varying string:
			// Process varying strings
			upperLength := len(v)
			fmt.Printf("Varying string length: %v\n", upperLength)
			
		case int:
			// Process uniform integers
			fmt.Printf("Uniform int: %d\n", v)
			
		case float64:
			// Process uniform floats
			fmt.Printf("Uniform float: %f\n", v)
			
		case string:
			// Process uniform strings
			fmt.Printf("Uniform string: %s\n", v)
			
		default:
			fmt.Printf("Unhandled type: %T\n", v)
		}
	}
}

// genericProcessor demonstrates polymorphic processing
func genericProcessor(value interface{}) {
	// This function works with any type - uniform or varying
	switch v := value.(type) {
	case varying int:
		// Reduce varying to uniform for generic processing
		sum := reduce.Add(v)
		fmt.Printf("Varying int sum: %d\n", sum)
		
	case int:
		// Process uniform directly
		fmt.Printf("Uniform int: %d\n", v)
		
	case varying float64:
		// Reduce varying to uniform
		avg := reduce.Add(v) / float64(lanes.Count(v))
		fmt.Printf("Varying float64 average: %f\n", avg)
		
	case float64:
		// Process uniform directly
		fmt.Printf("Uniform float64: %f\n", v)
		
	default:
		fmt.Printf("Generic processor - unhandled type: %T\n", v)
	}
}

func main() {
	fmt.Println("=== Type Switch with Varying Types ===")
	
	// Test 1: Basic varying type switches
	fmt.Println("\n--- Basic Varying Type Switches ---")
	varyingInt := varying(42)
	varyingStr := varying("hello")
	varyingFloat := varying(3.14)
	
	processVaryingInterface(varyingInt)
	processVaryingInterface(varyingStr)
	processVaryingInterface(varyingFloat)
	
	// Test 2: Mixed uniform/varying handling
	fmt.Println("\n--- Mixed Type Handling ---")
	processMixedInterface(varyingInt)
	processMixedInterface(42)        // uniform int
	processMixedInterface("world")   // uniform string
	
	// Test 3: Constrained varying types
	demonstrateConstrainedTypes()
	
	// Test 4: Type assertions
	demonstrateTypeAssertions()
	
	// Test 5: Realistic mixed data processing
	mixedData := []interface{}{
		varying(10),
		varying(3.14),
		varying("test"),
		20,           // uniform int
		2.71,         // uniform float
		"uniform",    // uniform string
	}
	processDataWithTypeSwitch(mixedData)
	
	// Test 6: Generic processing
	fmt.Println("\n--- Generic Processing ---")
	values := []interface{}{
		varying(100),
		200,
		varying(1.5),
		2.5,
	}
	
	for _, v := range values {
		genericProcessor(v)
	}
	
	fmt.Println("\n✓ All type switch varying operations completed successfully")
}