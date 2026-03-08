// run -goexperiment spmd

// SPMD equivalent of lo's MinInt32 — replaces 20+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"math"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	scalar := minScalar(data)
	spmd := minSPMD(data)

	fmt.Printf("Min: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 3 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func minScalar(data []int32) int32 {
	result := int32(math.MaxInt32)
	for _, v := range data {
		if v < result {
			result = v
		}
	}
	return result
}

func minSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MaxInt32
	go for _, v := range data {
		if v < result {
			result = v
		}
	}
	return reduce.Min(result)
}
