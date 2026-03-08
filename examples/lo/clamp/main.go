// run -goexperiment spmd

// SPMD equivalent of lo's ClampInt32 — replaces 20+ hand-written SIMD functions
// Pure element-wise operation: no reduction needed
package main

import "fmt"

func main() {
	data := []int32{-5, 0, 3, 10, 15, 20, 25, 30}
	lo := int32(0)
	hi := int32(20)

	scalar := clampScalar(data, lo, hi)
	spmd := clampSPMD(data, lo, hi)
	expected := []int32{0, 0, 3, 10, 15, 20, 20, 20}

	fmt.Printf("Clamp: scalar=%v spmd=%v\n", scalar, spmd)
	pass := true
	for i := range expected {
		if scalar[i] != expected[i] || spmd[i] != expected[i] {
			pass = false
		}
	}
	if pass {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}

func clampScalar(data []int32, lo, hi int32) []int32 {
	result := make([]int32, len(data))
	for i, v := range data {
		if v < lo {
			result[i] = lo
		} else if v > hi {
			result[i] = hi
		} else {
			result[i] = v
		}
	}
	return result
}

func clampSPMD(data []int32, lo, hi int32) []int32 {
	result := make([]int32, len(data))
	go for i := range len(data) {
		v := data[i]
		if v < lo {
			result[i] = lo
		} else if v > hi {
			result[i] = hi
		} else {
			result[i] = v
		}
	}
	return result
}
