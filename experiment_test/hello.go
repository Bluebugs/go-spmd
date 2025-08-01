package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Println("Hello, World!")
	fmt.Printf("Current time: %v\n", time.Now())
	fmt.Printf("Args: %v\n", os.Args)
	
	// Test some basic Go functionality
	numbers := []int{1, 2, 3, 4, 5}
	sum := 0
	for _, n := range numbers {
		sum += n
	}
	fmt.Printf("Sum of %v = %d\n", numbers, sum)
	
	// Test map
	m := make(map[string]int)
	m["hello"] = 42
	fmt.Printf("Map: %v\n", m)
}