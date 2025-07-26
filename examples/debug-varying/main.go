// run -goexperiment spmd

// Debug example showing how to inspect varying values
// Demonstrates reduce.From[T any](varying T) []T function
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int{10, 20, 30, 40, 50, 60, 70, 80}
	debugProcessing(data)
}

func debugProcessing(data []int) {
	fmt.Printf("Processing %d elements\n", len(data))

	go for _, v := range data {
		// v is already varying (each lane gets different data elements)
		doubled := v * 2
		
		// Printf automatically converts varying to array with %v
		fmt.Printf("Lane values: %v\n", v)       // Automatic reduce.From conversion
		fmt.Printf("Doubled: %v\n", doubled)     // Automatic reduce.From conversion
		
		// Manual conversion still available if needed
		currentValues := reduce.From(v)
		fmt.Printf("Manual conversion: %v\n", currentValues)
		
		// Normal SPMD processing continues...
		total := reduce.Add(doubled)
		fmt.Printf("Total for this iteration: %d\n", total)
	}
}