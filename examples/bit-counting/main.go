// Bit counting using nested loops in SPMD Go
// From: go-data-parallelism.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []uint8{0xFF, 0x0F, 0xF0, 0x55, 0xAA, 0x33, 0xCC, 0x00}
	result := countBits(data)
	fmt.Printf("Bit counts: %v\n", result)
}

func countBits(data []uint8) int {
	var count lanes.Varying[uint8]

	go for _, v := range data {
		// v is varying (each lane processes different elements)
        for it := range 8 {
            if v & (1 << it) != 0 {
                count++
            }
        }
	}

	return reduce.Add(count)
}