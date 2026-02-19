// run -goexperiment spmd -target=wasi

// Example demonstrating defer statements with varying values
// Shows how deferred functions can capture and process varying data
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// cleanup doesn't receive varying parameters, so it isn't a SPMD function.
func cleanup(resources int) {
	fmt.Printf("Cleaning up resources: %d\n", resources)
}

func allocateResource(id int) int {
	// Simulate allocation (in real code, this might allocate memory, files, etc.)
	return id * 100 // Return resource handle
}

// processWithDefer demonstrates implicit SPMD conversion with defer
func processWithDefer(data []int) {
	fmt.Println("=== Basic Defer with Implicit SPMD Conversion ===")
	
	// Regular function call outside SPMD context
	cleanup(999)
	
	go for _, value := range data {
		// When deferred from within `go for`, cleanup could implicitly becomes SPMD, but doesn't get varying values so isn't SPMD here
		defer cleanup(999)  // Fine to defer to a non-varying function
		
		// Also works with anonymous functions with no varying parameters
		defer func() {
			// This anonymous function implicitly becomes SPMD even if value wasn't captured			
			fmt.Printf("Anonymous deferred cleanup: %d\n", value)
		}()
		
		// Process the values
		processed := value * 2
		fmt.Printf("Processed value: %d\n", processed)
	}
	
	// Deferred functions execute here when function returns
}

// conditionalDeferExample shows defer with execution masking
func conditionalDeferExample(data []int) {
	fmt.Println("\n=== Conditional Defer with Masking ===")
	
	go for _, values := range data {
		// values is varying (each lane processes different elements)
		
		if values > 5 {
			// Only allocate resources for values > 5
			allocated := allocateResource(values)
			
			// Only lanes where values > 5 register this defer
			defer func(resource lanes.Varying[int]) {
				// This captured the mask and will only display for values > 5 and other lanes will have no value (either displayed as zero or '-')
				fmt.Printf("Releasing resources: %v\n", resource)
			}(allocated)
			
			fmt.Printf("Allocated for values > 5: %v\n", allocated)
		}
		
		// Regular processing continues
		fmt.Printf("Processing: %v\n", values)
	}
}

// multipleDeferExample shows LIFO order of multiple defers as expected
func multipleDeferExample(data []int) {
	fmt.Println("\n=== Multiple Defer Statements (LIFO Order) ===")
	
	go for _, values := range data {
		// values is varying (each lane processes different elements)
		
		// First defer (executes last)
		defer func(v lanes.Varying[int]) {
			fmt.Printf("First defer (last execution): %v\n", v)
		}(values)

		// Second defer (executes second-to-last)
		defer func(v lanes.Varying[int]) {
			fmt.Printf("Second defer (middle execution): %v\n", v)
		}(values * 10)

		// Third defer (executes first)
		defer func(v lanes.Varying[int]) {
			fmt.Printf("Third defer (first execution): %v\n", v)
		}(values * 100)
		
		fmt.Printf("Main processing: %v\n", values)
	}
}

// directDeferCall shows defer with direct function call
func directDeferCall(data []int) {
	fmt.Println("\n=== Direct Defer Function Call ===")
	
	go for _, values := range data {
		// values is varying (each lane processes different elements)
		
		// Direct defer call with uniform arguments
		defer cleanup(reduce.Add(values))

		// Some processing
		result := values + lanes.Index()
		fmt.Printf("Processing with lane index: %v\n", result)
	}
}

func main() {
	fmt.Println("=== Defer with Varying Values Example ===")
	
	// Test data
	testData := []int{3, 7, 2, 9, 1, 6, 4, 8}
	
	// Test basic defer functionality
	processWithDefer(testData[:4])
	
	// Test conditional defer with masking
	conditionalDeferExample(testData[:4])
	
	// Test multiple defer statements
	multipleDeferExample(testData[:2])
	
	// Test direct defer calls
	directDeferCall(testData[:4])
	
	fmt.Println("\n✓ All defer varying tests completed successfully")
}