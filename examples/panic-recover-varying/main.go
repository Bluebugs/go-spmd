// run -goexperiment spmd -target=wasi

// Example demonstrating panic/recover with varying values
// and workarounds for public API restrictions in SPMD contexts
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Private wrapper functions to access public APIs

// logValues processes uniform data and calls public APIs
func logValues(values []int) {
	for i, v := range values {
		fmt.Printf("Lane %d: %d\n", i, v)  // Regular public API call
	}
}

// logAny handles any type (including varying via reflection)
func logAny(value any) {
	fmt.Printf("Any value: %v\n", value)  // Printf handles varying automatically
}

// handlePanic processes panic recovery
func handlePanic(r any) {
	fmt.Printf("Recovered from panic: %v\n", r)
}

// validateAndProcess demonstrates panic/recover with varying values
func validateAndProcess(data []int) {
	fmt.Println("=== Panic/Recover with Varying Values ===")
	
	go for _, value := range data {
		
		// Set up panic recovery
		defer func() {
			if r := recover(); r != nil {
				// recover explicitly supports varying panic values
				handlePanic(r)  // Private function to handle recovery
			}
		}()
		
		// Validation that can panic with varying data
		if value < 0 {
			panic(value)  // panic explicitly supports varying data
		}
		
		if value > 100 {
			panic(value)  // panic explicitly supports varying data  
		}
		
		// Process valid values
		result := value * 2
		fmt.Printf("Processed: %d -> %d\n", value, result)
	}
}

// demonstratePublicAPIWorkarounds shows how to call public APIs from SPMD contexts
func demonstratePublicAPIWorkarounds(data []int) {
	fmt.Println("\n=== Public API Workarounds ===")
	
	go for _, values := range data {
		// values is varying (each lane processes different elements)
		
		// ✗ PROHIBITED: Direct public API calls
		// go func() {
		//     fmt.Printf("Value: %v\n", values)  // ERROR: public API conflict
		// }()
		
		// ✓ WORKAROUND 1: Use reduce operations to convert to uniform
		go func() {
			uniformValues := reduce.From(values)  // Convert to []int
			logValues(uniformValues)  // Private function with uniform parameter
		}()
		
		// ✓ WORKAROUND 2: Use any type for opaque conversion
		defer func() {
			logAny(any(values))  // Convert varying to any (opaque type)
		}()
		
		// ✓ ALLOWED: Direct calls to lanes/reduce functions
		laneId := lanes.Index()
		total := reduce.Add(values)
		fmt.Printf("Lane %v, Total: %d\n", laneId, total)
	}
}

// errorHandlingExample shows comprehensive error handling patterns
func errorHandlingExample(data []int) {
	fmt.Println("\n=== Error Handling Patterns ===")
	
	go for _, value := range data {
		
		// Nested defer for cleanup
		defer func() {
			fmt.Printf("Cleanup for value: %d\n", value)
		}()
		
		// Error recovery
		defer func() {
			if r := recover(); r != nil {
				// recover explicitly supports varying panic values
				// Handle different types of panic values
				switch v := r.(type) {
				case varying(string):
					// Handle varying string panic
					strings := reduce.From(v)
					for i, s := range strings {
						fmt.Printf("Lane %d panicked with: %s\n", i, s)
					}
				case varying(int):
					// Handle varying int panic
					values := reduce.From(v)
					for i, val := range values {
						fmt.Printf("Lane %d panicked with value: %d\n", i, val)
					}
				default:
					// Handle other panic types
					fmt.Printf("Unknown panic type: %T = %v\n", r, r)
				}
			}
		}()
		
		// Simulate different error conditions
		switch {
		case value < 0:
			panic(varying(-value))  // panic explicitly supports varying int
		case value == 13:
			panic(varying("unlucky number"))  // panic explicitly supports varying string
		case value > 50:
			panic("regular panic")  // Regular uniform panic
		default:
			fmt.Printf("Processing safe value: %d\n", value)
		}
	}
}

func main() {
	fmt.Println("=== Panic/Recover and Public API Restrictions Example ===")
	
	// Test data with various conditions
	testData1 := []int{5, -2, 15, 25}  // Contains negative value
	testData2 := []int{1, 2, 3, 4}     // All valid
	testData3 := []int{10, 13, 60, 5}  // Contains unlucky and large values
	
	// Test panic/recover with varying values
	fmt.Println("Testing with negative values:")
	validateAndProcess(testData1)
	
	fmt.Println("\nTesting with valid values:")
	validateAndProcess(testData2)
	
	// Demonstrate public API workarounds
	demonstratePublicAPIWorkarounds(testData2)
	
	// Comprehensive error handling
	errorHandlingExample(testData3)
	
	fmt.Println("\n✓ All panic/recover and public API restriction tests completed")
}