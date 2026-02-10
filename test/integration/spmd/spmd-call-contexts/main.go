// run -goexperiment spmd -target=wasi

// Example demonstrating SPMD functions called from different contexts
// Shows mask handling for non-SPMD, SPMD, and captured varying scenarios
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// SPMD function (has varying parameter)
func processData(input lanes.Varying[int]) lanes.Varying[int] {
	// This function can be called from any context
	// Mask is automatically handled based on calling context
	return input * 2
}

// SPMD function for advanced processing
func advancedProcess(data lanes.Varying[int], multiplier lanes.Varying[int]) lanes.Varying[int] {
	// Demonstrates SPMD function with multiple varying parameters
	result := data * multiplier

	// Can use reduce operations within SPMD function
	if reduce.Any(result > 100) {
		// Some lanes exceed threshold
		return result / 2
	}
	return result
}

// Non-SPMD function calling SPMD functions
func nonSPMDCaller() {
	fmt.Println("\n=== Non-SPMD Context Calls ===")

	// Create varying data in non-SPMD context
	data := lanes.Varying[int](50)

	// LEGAL: Call SPMD function from non-SPMD context
	// Mask implicitly set to all lanes active
	result := processData(data)
	fmt.Printf("Non-SPMD call result: %v\n", result)

	// LEGAL: Call reduce functions from non-SPMD context
	sum := reduce.Add(result)
	fmt.Printf("Sum from non-SPMD context: %d\n", sum)

	// LEGAL: Call SPMD function with multiple varying parameters
	multiplier := lanes.Varying[int](3)
	advanced := advancedProcess(data, multiplier)
	fmt.Printf("Advanced processing: %v\n", advanced)

	// LEGAL: Chain SPMD function calls
	chained := processData(advanced)
	total := reduce.Add(chained)
	fmt.Printf("Chained result total: %d\n", total)
}

// Demonstrate captured mask behavior with defer
func demonstrateCapturedMask() {
	fmt.Println("\n=== Captured Mask Behavior ===")

	go for i := range 8 {
		data := lanes.Varying[int](i * 10)

		// Create conditional mask
		if data > 30 {  // Only lanes with data > 30 are active
			fmt.Printf("Active lane %v: data=%v\n", lanes.Index(), data)

			// Defer captures both varying data AND current execution mask
			defer func(captured lanes.Varying[int]) {
				fmt.Printf("Deferred processing captured data: %v\n", captured)

				// SPMD function call uses the captured mask
				processed := processData(captured)  // Uses mask from capture point
				fmt.Printf("Processed with captured mask: %v\n", processed)

				// Reduce operations work with captured mask
				total := reduce.Add(processed)  // Only sums originally active lanes
				fmt.Printf("Total from captured mask: %d\n", total)
			}(data)

			// Goroutine also captures mask
			go func(captured lanes.Varying[int]) {
				fmt.Printf("Goroutine processing: %v\n", captured)
				result := processData(captured)  // Uses captured mask
				sum := reduce.Add(result)
				fmt.Printf("Goroutine total: %d\n", sum)
			}(data)
		}
	}
}

// Demonstrate SPMD context calls
func demonstrateSPMDCalls() {
	fmt.Println("\n=== SPMD Context Calls ===")

	go for i := range 6 {
		data := lanes.Varying[int](i * 5)

		// LEGAL: Call SPMD function from SPMD context
		// Inherits current execution mask (all lanes active initially)
		processed := processData(data)
		fmt.Printf("SPMD call lane %v: input=%v, output=%v\n",
			lanes.Index(), data, processed)

		// Create conditional execution
		if processed > 25 {
			// Now mask is updated for conditional
			fmt.Printf("Conditional lane %v: %v\n", lanes.Index(), processed)

			// SPMD function call inherits conditional mask
			doubled := processData(processed)
			fmt.Printf("Conditional processing: %v\n", doubled)

			// Reduce operations work with current mask
			conditionalSum := reduce.Add(doubled)
			fmt.Printf("Conditional sum: %d\n", conditionalSum)
		}
	}
}

// Demonstrate nested SPMD function calls
func nestedSPMDProcessing(base lanes.Varying[int], factor lanes.Varying[int]) lanes.Varying[int] {
	// SPMD function that calls other SPMD functions
	step1 := processData(base)         // First SPMD call
	step2 := advancedProcess(step1, factor)  // Second SPMD call

	// Use reduce within SPMD function
	if reduce.All(step2 > 0) {
		return step2 + 10
	}
	return step2
}

func demonstrateNestedCalls() {
	fmt.Println("\n=== Nested SPMD Function Calls ===")

	// From non-SPMD context
	base := lanes.Varying[int](15)
	factor := lanes.Varying[int](2)

	result := nestedSPMDProcessing(base, factor)
	fmt.Printf("Nested result from non-SPMD: %v\n", result)

	// From SPMD context
	go for i := range 4 {
		localBase := lanes.Varying[int](i * 8)
		localFactor := lanes.Varying[int](3)

		nestedResult := nestedSPMDProcessing(localBase, localFactor)
		fmt.Printf("Lane %v nested result: %v\n", lanes.Index(), nestedResult)

		if nestedResult > 50 {
			// Further nesting with conditional mask
			final := processData(nestedResult)
			total := reduce.Add(final)
			fmt.Printf("Final total for lane %v: %d\n", lanes.Index(), total)
		}
	}
}

// Demonstrate reduce functions in all contexts
func demonstrateReduceEverywhere() {
	fmt.Println("\n=== Reduce Functions in All Contexts ===")

	// 1. Reduce in non-SPMD context
	data := lanes.Varying[int](100)
	sum1 := reduce.Add(data)
	max1 := reduce.Max(data)
	any1 := reduce.Any(data > 50)
	all1 := reduce.All(data > 0)

	fmt.Printf("Non-SPMD reduce: sum=%d, max=%d, any=%t, all=%t\n",
		sum1, max1, any1, all1)

	// 2. Reduce in SPMD context
	go for i := range 5 {
		localData := lanes.Varying[int](i * 20)

		// These work in SPMD context with current mask
		localSum := reduce.Add(localData)
		localMax := reduce.Max(localData)

		fmt.Printf("SPMD reduce lane %v: sum=%d, max=%d\n",
			lanes.Index(), localSum, localMax)

		if localData > 40 {
			// Reduce with conditional mask
			conditionalSum := reduce.Add(localData)
			fmt.Printf("Conditional reduce lane %v: sum=%d\n",
				lanes.Index(), conditionalSum)
		}
	}

	// 3. Reduce with captured varying (defer)
	go for i := range 3 {
		iterData := lanes.Varying[int](i * 30)

		if iterData > 15 {
			defer func(captured lanes.Varying[int]) {
				// Reduce with captured mask
				deferredSum := reduce.Add(captured)
				fmt.Printf("Deferred reduce: sum=%d\n", deferredSum)
			}(iterData)
		}
	}
}

func main() {
	fmt.Println("=== SPMD Function Call Contexts ===")

	// Test 1: Non-SPMD context calling SPMD functions
	nonSPMDCaller()

	// Test 2: Captured mask behavior (defer/goroutines)
	demonstrateCapturedMask()

	// Test 3: SPMD context calls
	demonstrateSPMDCalls()

	// Test 4: Nested SPMD function calls
	demonstrateNestedCalls()

	// Test 5: Reduce functions everywhere
	demonstrateReduceEverywhere()

	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ SPMD functions can be called from any context")
	fmt.Println("✓ Mask is automatically handled based on context")
	fmt.Println("✓ Non-SPMD context: all lanes active by default")
	fmt.Println("✓ SPMD context: inherits current execution mask")
	fmt.Println("✓ Captured varying: preserves mask from capture point")
	fmt.Println("✓ Reduce functions work in all contexts")
	fmt.Println("✓ All SPMD call context operations completed successfully")
}
