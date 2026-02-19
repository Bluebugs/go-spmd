// errorcheck -goexperiment spmd

package main

import (
	"lanes"
	"reduce"
)

// SPMD function (takes varying parameter)
func processSPMDData(data lanes.Varying[int]) lanes.Varying[int] {
	var result lanes.Varying[int] = data * 2

	// ERROR: go for loops are not allowed inside SPMD functions
	go for i := range 4 { // ERROR "go for loops not allowed in SPMD functions"
		result += lanes.Varying[int](i)
	}

	return result
}

// Regular function - this is allowed
func processRegularData(data []int) int {
	var total int

	// This is perfectly valid - regular function can have go for
	go for i := range len(data) {
		total += data[i]
	}

	return total
}

func main() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8}
	var results lanes.Varying[int]

	// This go for is valid (top-level)
	go for _, v := range data {
		// Calling SPMD function is fine, but the function itself is invalid due to internal go for
		results = processSPMDData(v)

		// Calling regular function with go for is perfectly fine
		_ = processRegularData(reduce.From(v))
	}
}
