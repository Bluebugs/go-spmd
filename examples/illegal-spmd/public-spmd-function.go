// errorcheck -goexperiment spmd

package main

import (
	"lanes"
	"reduce"
)

// ILLEGAL: Public functions cannot have varying parameters
func ProcessData(data lanes.Varying[int]) lanes.Varying[int] {  // ERROR "varying parameters not allowed in public functions"
	return data * 2
}

// ILLEGAL: Public functions cannot return varying types either
func GenerateData() lanes.Varying[int] {  // ERROR "varying return types not allowed in public functions"
	return lanes.Varying[int](42)
}

// LEGAL: Private functions can have varying parameters
func processData(data lanes.Varying[int]) lanes.Varying[int] {  // OK: private function
	return data * 2
}

// LEGAL: Private functions can return varying types
func generateData() lanes.Varying[int] {  // OK: private function
	return lanes.Varying[int](42)
}

// LEGAL: Public functions can use varying types internally
func PublicFunction(data []int) int {
	var total int

	go for i := range len(data) {
		// Use private SPMD functions internally
		processed := processData(data[i])
		total += reduce.Add(processed)
	}

	return total
}

func main() {
	data := []int{1, 2, 3, 4}
	result := PublicFunction(data)
	println("Result:", result)
}
