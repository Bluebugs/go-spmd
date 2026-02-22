// run -goexperiment spmd -target=wasi

// Example demonstrating go for iteration over arrays of varying values
// Shows the difference between uniform and varying array iteration
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Demonstrate go for with array of varying values
func demonstrateVaryingArrayIteration() {
	fmt.Println("=== Iteration Over Arrays of Varying Values ===")

	// Create some varying values to process
	values1 := lanes.From([]int{10, 20, 30, 40})
	values2 := lanes.From([]int{100, 200, 300, 400})
	values3 := lanes.From([]int{1, 2, 3, 4})

	varyingArray := []lanes.Varying[int]{values1, values2, values3}

	fmt.Printf("Processing array of %d varying values:\n", len(varyingArray))
	fmt.Printf("values1: %v\n", values1)
	fmt.Printf("values2: %v\n", values2)
	fmt.Printf("values3: %v\n", values3)

	// Iterate over array of varying values
	go for idx, varyingData := range varyingArray {
		// idx is UNIFORM (processing one varying at a time): 0, then 1, then 2
		// varyingData is VARYING (each array element is a complete varying value)

		fmt.Printf("\nProcessing element %d:\n", idx)
		fmt.Printf("  Input varying data: %v\n", varyingData)

		// Can perform SPMD operations on the varying data
		doubled := varyingData * 2
		fmt.Printf("  Doubled: %v\n", doubled)

		// Use lanes operations
		laneIndices := lanes.Index()
		fmt.Printf("  Lane indices: %v\n", laneIndices)

		// Use reduce operations
		sum := reduce.Add(varyingData)
		max := reduce.Max(varyingData)
		fmt.Printf("  Sum: %d, Max: %d\n", sum, max)

		// Conditional processing within SPMD context
		if reduce.Any(varyingData > 25) {
			filtered := varyingData
			// Apply processing only to lanes > 25
			go for i, v := range filtered {
				if v > 25 {
					fmt.Printf("    Lane %v has value > 25: %v\n", i, v)
				}
			}
		}
	}
}

// processVaryingGroups demonstrates direct processing of varying data groups
func processVaryingGroups() {
	fmt.Println("\n=== Processing Varying Data Groups ===")

	data4 := lanes.From([]int{1, 2, 3, 4})
	data8 := lanes.From([]int{10, 20, 30, 40, 50, 60, 70, 80})

	// Process each varying group directly
	for _, data := range []lanes.Varying[int]{data4, data8} {
		fmt.Printf("\nProcessing varying of type %T\n", data)

		processed := data * 3
		result := reduce.Add(processed)
		fmt.Printf("  Values: %v\n", data)
		fmt.Printf("  Processed (*3): %v\n", processed)
		fmt.Printf("  Sum: %d\n", result)
	}
}

// Demonstrate the difference between uniform and varying array iteration
func compareIterationTypes() {
	fmt.Println("\n=== Comparison: Uniform vs Varying Array Iteration ===")

	// Array of uniform values (regular integers)
	uniformArray := []int{100, 200, 300}

	// Array of varying values
	varyingArray := []lanes.Varying[int]{
		lanes.From([]int{10, 20}),
		lanes.From([]int{30, 40}),
		lanes.From([]int{50, 60}),
	}

	fmt.Println("\n--- Uniform Array Iteration ---")
	go for i, uniformValue := range uniformArray {
		// i is VARYING (different index per lane)
		// uniformValue is UNIFORM (same value across all lanes)
		fmt.Printf("Lane varies, processing uniform value %d at varying index %v\n",
			uniformValue, i)

		// Each lane processes the same value but at different indices
		result := uniformValue + reduce.Add(i) // Add sum of varying indices
		fmt.Printf("Result: %d\n", result)
	}

	fmt.Println("\n--- Varying Array Iteration ---")
	go for idx, varyingValue := range varyingArray {
		// idx is UNIFORM (same index across all lanes - processing one varying at a time)
		// varyingValue is VARYING (different values per lane)
		fmt.Printf("Processing varying value %v at uniform index %d\n",
			varyingValue, idx)

		// All lanes process different values from the same varying
		result := varyingValue * (idx + 1) // Multiply by (index + 1)
		fmt.Printf("Result: %v\n", result)
	}
}

// Example showing control flow restrictions outside SPMD context
func demonstrateControlFlowRestrictions() {
	fmt.Println("\n=== Control Flow Restrictions Outside SPMD Context ===")

	data := lanes.From([]int{10, 20, 30, 40})
	fmt.Printf("Varying data: %v\n", data)

	// These operations would be COMPILE ERRORS outside go for:
	fmt.Println("\nThe following would be compile errors outside SPMD context:")
	fmt.Println("// if data > 25 { ... }        // ERROR: varying condition outside SPMD context")
	fmt.Println("// for data != 0 { ... }        // ERROR: varying loop condition outside SPMD context")
	fmt.Println("// switch data { ... }           // ERROR: varying switch outside SPMD context")

	// But inside go for, they're all legal:
	fmt.Println("\nInside SPMD context (go for), all control flow is legal:")

	go for i := range 1 { // Single iteration to demonstrate
		fmt.Printf("Processing in SPMD context: %v\n", data)

		// All of these are legal inside go for:
		if data > 25 {
			fmt.Printf("  Values > 25: %v\n", data)
		}

		// Could use switch, for loops, etc. with varying values here
		count := reduce.Count(data > 15)
		fmt.Printf("  Count of values > 15: %d\n", count)
	}
}

func main() {
	fmt.Println("=== Varying Array Iteration Examples ===")

	// Test 1: Basic varying array iteration
	demonstrateVaryingArrayIteration()

	// Test 2: Processing varying data groups directly
	processVaryingGroups()

	// Test 3: Compare uniform vs varying array iteration
	compareIterationTypes()

	// Test 4: Show control flow restrictions
	demonstrateControlFlowRestrictions()

	fmt.Println("\n=== Summary ===")
	fmt.Println("go for with []lanes.Varying: idx is uniform, value is varying")
	fmt.Println("go for with []uniform: idx is varying, value is uniform")
	fmt.Println("Control flow with varying only allowed inside SPMD contexts")
	fmt.Println("All varying array iteration examples completed successfully")
}
