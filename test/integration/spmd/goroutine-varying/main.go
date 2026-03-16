// run -goexperiment spmd -target=wasi
//
// Example demonstrating goroutines processing varying values.
// Uses anonymous closures (not named SPMD functions) as goroutines
// because TinyGo's asyncify scheduler doesn't support SPMD function
// goroutine wrappers ($gowrapper) yet.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	fmt.Println("=== Goroutine with Varying Values Example ===")

	// Test 1: Goroutine capturing varying data, sending reduced result
	fmt.Println("\n--- Goroutine with Varying Capture ---")
	batch1 := lanes.From([]int{1, 2, 3, 4})
	batch2 := lanes.From([]int{5, 6, 7, 8})

	results := make(chan int, 2)
	go func() {
		squared := batch1 * batch1
		results <- reduce.Add(squared) // 1+4+9+16 = 30
	}()
	go func() {
		squared := batch2 * batch2
		results <- reduce.Add(squared) // 25+36+49+64 = 174
	}()

	r1 := <-results
	r2 := <-results
	fmt.Printf("Batch results: %d, %d\n", r1, r2)
	fmt.Printf("Total: %d\n", r1+r2) // 204

	// Test 2: Goroutine sending varying values through channel
	fmt.Println("\n--- Varying Values via Channel ---")
	vCh := make(chan lanes.Varying[int], 3)
	data := [][]int{{10, 20, 30, 40}, {100, 200, 300, 400}, {1, 1, 1, 1}}
	for _, d := range data {
		v := lanes.From(d)
		go func() {
			vCh <- v * 2
		}()
	}

	for i := 0; i < 3; i++ {
		v := <-vCh
		fmt.Printf("Doubled: %v (sum: %d)\n", v, reduce.Add(v))
	}

	// Test 3: Goroutine launched from go for context
	fmt.Println("\n--- Goroutine from SPMD Context ---")
	done := make(chan int, 4)

	go for _, val := range []int{10, 20, 30, 40} {
		go func() {
			done <- reduce.Add(val)
		}()
	}

	// 4 items / 4 lanes = 1 SPMD iteration = 1 goroutine
	r := <-done
	fmt.Printf("Sum from SPMD goroutine: %d\n", r) // 10+20+30+40 = 100

	fmt.Println("\nAll goroutine varying tests completed successfully")
}
