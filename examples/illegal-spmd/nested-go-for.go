// errorcheck -goexperiment spmd

package main

import "lanes"

func main() {
	data := make([][]int, 16)
	for i := range data {
		data[i] = make([]int, 16)
	}

	var total int

	// ERROR: Nested go for loops are not allowed (for now)
	go for i := range 16 { // ERROR "go for loops cannot be nested"
		go for j := range 16 { // ERROR "go for loops cannot be nested"
			total += data[i][j]
		}
	}
}