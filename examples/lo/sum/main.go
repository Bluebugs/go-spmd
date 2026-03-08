// run -goexperiment spmd

// SPMD equivalent of lo's SumInt32 — replaces 30+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := make([]int32, 64)
	for i := range data {
		data[i] = int32(i + 1)
	}

	scalar := sumScalar(data)
	spmd := sumSPMD(data)
	expected := int32(64 * 65 / 2) // 2080

	fmt.Printf("Sum: scalar=%d spmd=%d expected=%d\n", scalar, spmd, expected)
	if scalar != expected || spmd != expected {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func sumScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total
}

func sumSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total)
}
