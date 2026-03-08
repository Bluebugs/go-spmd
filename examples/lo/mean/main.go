// run -goexperiment spmd

// SPMD equivalent of lo's MeanInt32 — sum is SPMD, division is scalar
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int32{10, 20, 30, 40, 50, 60, 70, 80}

	scalar := meanScalar(data)
	spmd := meanSPMD(data)

	fmt.Printf("Mean: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 45 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func meanScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total / int32(len(data))
}

func meanSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total) / int32(len(data))
}
