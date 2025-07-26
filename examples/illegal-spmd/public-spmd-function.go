// errorcheck -goexperiment spmd

package main

import "lanes"

// ILLEGAL: Public functions cannot have varying parameters
func ProcessData(data varying int) varying int {  // ERROR "varying parameters not allowed in public functions"
	return data * 2
}

// ILLEGAL: Public functions cannot return varying types either
func GenerateData() varying int {  // ERROR "varying return types not allowed in public functions"
	return varying(42)
}

// LEGAL: Private functions can have varying parameters
func processData(data varying int) varying int {  // OK: private function
	return data * 2
}

// LEGAL: Private functions can return varying types
func generateData() varying int {  // OK: private function
	return varying(42)
}

// LEGAL: Public functions can use varying types internally
func PublicFunction(data []int) int {
	var total int
	
	go for i := range len(data) {
		// Use private SPMD functions internally
		processed := processData(varying(data[i]))
		total += reduce.Add(processed)
	}
	
	return total
}

func main() {
	data := []int{1, 2, 3, 4}
	result := PublicFunction(data)
	println("Result:", result)
}