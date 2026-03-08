// run -goexperiment spmd

// SPMD equivalent of lo's MaxInt32 — replaces 20+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"math"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	scalar := maxScalar(data)
	spmd := maxSPMD(data)

	fmt.Printf("Max: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 99 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func maxScalar(data []int32) int32 {
	result := int32(math.MinInt32)
	for _, v := range data {
		if v > result {
			result = v
		}
	}
	return result
}

func maxSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MinInt32
	go for _, v := range data {
		if v > result {
			result = v
		}
	}
	return reduce.Max(result)
}
