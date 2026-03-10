// Test varying continue inside go for loop
// Verifies that continue narrows the mask for subsequent operations
package main

import "fmt"

func main() {
	data := []int{0, 1, 0, 2, 0, 3, 0, 4}
	out := make([]int, len(data))

	go for i, v := range data {
		if v == 0 {
			continue
		}
		out[i] = v * 2
	}

	for _, v := range out {
		fmt.Printf("%d ", v)
	}
	fmt.Println()
}
