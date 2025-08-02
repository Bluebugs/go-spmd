// Array counting with divergent control flow in SPMD Go
// From: go-data-parallelism.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	// Different arrays with different lengths to demonstrate divergent control flow
	arrays := [][]int{
		{1, 2},     // Lane 0
		{3},        // Lane 1  
		{4},        // Lane 2
		{5, 6, 7},  // Lane 3
	}
	
	result := countArrays(arrays)
	fmt.Printf("Array sums: %v\n", result)
}

func countArrays(arrays [][]int) []int {
	result := make([]int, len(arrays))

	go for i, secondLevel := range arrays {
		// arr is varying (each lane processes different arrays)
		// Type in a SPMD context are varying by default
		t := 0

		// Each lane processes its own array length
		for _, value := range secondLevel {
			t += value
		}

		result[i] = t
	}

	return result
}