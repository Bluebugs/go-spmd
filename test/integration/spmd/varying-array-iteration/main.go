// run -goexperiment spmd -target=wasi

// Example demonstrating go for iteration over arrays of varying values.
// Shows the difference between uniform and varying array iteration.
// When go for iterates over []Varying[T], laneCount=1 for the outer loop:
// each iteration loads one full Varying[T] element (a complete SIMD vector).
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Demonstrate go for with array of varying values.
// The outer loop has laneCount=1: each step processes one Varying[int] element.
// idx is Varying[int] with laneCount=1 (prints as [N N N N] with same value).
// varyingData is Varying[int] with 4 lanes (the actual SIMD vector).
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

	// Iterate over array of varying values.
	// idx is UNIFORM within the outer 1-lane loop (processes one Varying per step).
	// varyingData is VARYING with 4 lanes (each array element is a complete varying value).
	go for idx, varyingData := range varyingArray {
		fmt.Printf("\nProcessing element %v:\n", idx)
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

		if reduce.Any(varyingData > 25) {
			fmt.Printf("  Some values > 25 in this batch\n")
		}
	}
}

// Demonstrate the difference between uniform and varying array iteration.
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
		// idx is Varying[int] with laneCount=1 (outer loop processes one varying per step)
		// varyingValue is Varying[int] with 4 lanes (different values per lane)
		fmt.Printf("Processing varying value %v at index %v\n",
			varyingValue, idx)

		// All lanes process different values from the same varying
		result := varyingValue * 2
		fmt.Printf("Result: %v\n", result)
	}
}

func main() {
	fmt.Println("=== Varying Array Iteration Examples ===")

	// Test 1: Basic varying array iteration
	demonstrateVaryingArrayIteration()

	// Test 2: Compare uniform vs varying array iteration
	compareIterationTypes()

	fmt.Println("\n=== Summary ===")
	fmt.Println("go for with []lanes.Varying: idx has laneCount=1, value is 4-lane varying")
	fmt.Println("go for with []uniform: idx is 4-lane varying, value is uniform")
	fmt.Println("All varying array iteration examples completed successfully")
}
