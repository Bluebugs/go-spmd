// run -goexperiment spmd -target=wasi

// Example demonstrating goroutines launched with varying values
// Shows how a single goroutine can process all lane values
package main

import (
	"fmt"
	"lanes"
	"reduce"
	"sync"
)

// processAsync is an SPMD function when called from within `go for` context or directly as it receives varying parameters.
func processAsync(data varying int, results chan int, wg *sync.WaitGroup) {
	defer wg.Done()
	
	// When called from SPMD context, this function automatically becomes SPMD
	// and receives the execution mask, processing all active lanes
	processed := data * data  // Square each lane's value
	
	// Send processed data back
	results <- reduce.Mul(processed)
}

// asyncCompute demonstrates explicit SPMD function call
func asyncCompute(input []int) []int {
	output := make([]int, len(input))
	results := make(chan int, len(input))
	var wg sync.WaitGroup
	
	// Regular function call outside SPMD context
	fmt.Println("Calling processAsync as regular function:")	
	go for _, data := range input {
		wg.Add(1) // This work as is, because it called with uniform and behave as you would expect outside SPMD context

		// When called from within `go for`, processAsync is already part of a SPMD context
		// and can process all lanes in parallel, receiving the execution mask for all active lanes
		go processAsync(data, results, &wg) // Explicitly launching SPMD function
	}
	
	// Collect results
	go func() {
		wg.Wait()
		close(results)
	}()
	
	resultIndex := 0
	for result := range results {
		if resultIndex < len(output) {
			output[resultIndex] = result
			resultIndex++
		}
	}
	
	return output[:resultIndex]
}

// simpleGoroutineExample shows implicit SPMD conversion
func simpleGoroutineExample() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8}
	
	go for _, value := range data {	
		go func() {
			// This anonymous function implicitly becomes SPMD
			fmt.Printf("Processing value in goroutine: %d\n", value)
		}()

		go func(x int) {
			// This anonymous function also implicitly becomes SPMD even though it uses doesn't use `value`
			fmt.Printf("Anonymous SPMD processing: %d\n", x * 2)
		}(42)
	}
	
	// Note: In real code, you'd need proper synchronization
	// This is just a demonstration of implicit SPMD conversion rules for anonymous functions
}

func main() {
	fmt.Println("=== Goroutine with Varying Values Example ===")
	
	// Test simple goroutine launch
	simpleGoroutineExample()
	
	// Test async computation with result collection
	input := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	results := asyncCompute(input)
	
	fmt.Printf("Input:  %v\n", input)
	fmt.Printf("Output: %v\n", results)
	
	// Verify results (should be squares of input)
	allCorrect := true
	for i, result := range results {
		expected := input[i] * input[i]
		if result != expected {
			allCorrect = false
			break
		}
	}
	
	if allCorrect {
		fmt.Println("✓ All results correct - goroutine varying test passed")
	} else {
		fmt.Println("✗ Results incorrect - test failed")
	}
}