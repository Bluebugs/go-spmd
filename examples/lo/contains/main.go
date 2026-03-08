// run -goexperiment spmd

// SPMD equivalent of lo's Contains — replaces 30+ hand-written SIMD functions
// Uses reduce.Any to collapse varying bool to uniform bool
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	// Test found case
	found := containsSPMD(data, 28)
	fmt.Printf("Contains 28: %v\n", found) // true

	// Test not-found case
	notFound := containsSPMD(data, 100)
	fmt.Printf("Contains 100: %v\n", notFound) // false

	scalarFound := containsScalar(data, 28)
	scalarNotFound := containsScalar(data, 100)
	if found == scalarFound && notFound == scalarNotFound && found && !notFound {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}

func containsScalar(data []int32, target int32) bool {
	for _, v := range data {
		if v == target {
			return true
		}
	}
	return false
}

func containsSPMD(data []int32, target int32) bool {
	var found lanes.Varying[bool] = false
	go for _, v := range data {
		if v == target {
			found = true
		}
	}
	return reduce.Any(found)
}
