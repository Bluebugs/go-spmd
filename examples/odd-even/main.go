// Odd/even processing demonstrating if statements in SPMD Go
// From: go-data-parallelism.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8}
	odd, even := oddEvenCount(data)
	fmt.Printf("Result: Odd=%d, Even=%d\n", odd, even)
}

func oddEvenCount(data []int) (int, int) {
  	var odd varying int
  	var even varying int

  	go for _, value := range data {
		// value is varying (each lane processes different elements)
    	if value&1 == 1 { // Check if odd
        	odd++
    	} else {      // Else it's even
        	even++
    	}
  	}

  	return reduce.Add(odd), reduce.Add(even)
}
