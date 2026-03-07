// run -goexperiment spmd -target=wasi

// Example demonstrating panic with varying values in SPMD contexts.
// Panic must be called under uniform conditions only (e.g., reduce.Any guard).
// The varying data is boxed into interface{} when passed to panic.
package main

import (
	"fmt"
	"reduce"
)

// validatePositive panics if any lane has a negative value.
// The reduce.Any guard provides a uniform condition for panic.
func validatePositive(data []int) {
	go for _, value := range data {
		if reduce.Any(value < 0) {
			panic(value) // OK: uniform condition, varying value boxed to interface{}
		}
		result := value * 2
		fmt.Printf("Processed: %v -> %v\n", value, result)
	}
}

// validateRange panics if any lane exceeds a limit.
func validateRange(data []int, limit int) {
	go for _, value := range data {
		if reduce.Any(value > limit) {
			panic(value) // OK: uniform condition
		}
	}
}

// processWithDefer demonstrates defer alongside panic guards in SPMD.
func processWithDefer(data []int) {
	go for _, value := range data {
		defer func() {
			fmt.Printf("Cleanup: %v\n", value)
		}()

		// Multiple uniform guards with panic
		if reduce.Any(value < 0) {
			panic(value)
		}
		if reduce.Any(value > 1000) {
			panic(value)
		}

		fmt.Printf("OK: %v\n", value)
	}
}

func main() {
	fmt.Println("=== Panic with Varying Values in SPMD ===")

	// All positive: no panic triggered
	validatePositive([]int{5, 10, 15, 25})

	// All in range: no panic triggered
	validateRange([]int{10, 20, 30, 40}, 100)

	// Process with defers, no panic triggered
	processWithDefer([]int{1, 2, 3, 4})

	fmt.Println("Done")
}
