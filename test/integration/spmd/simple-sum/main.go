// run -goexperiment spmd

// Simple sum operation using SPMD Go
// From: go-data-parallelism.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	result := sum(data)
	fmt.Printf("Sum: %d\n", result)
}

func sum(data []int) int {
	var total varying int = 0

	go for _, value := range data {
		// value is varying (each lane processes different elements)
		total += value
	}

	return reduce.Add(total)
}