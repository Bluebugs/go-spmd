// run -goexperiment spmd

// Lo Mean SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i + 1)
	}

	scalar := meanScalar(data)
	spmd := meanSPMD(data)
	expected := int32(dataSize*(dataSize+1)/2) / int32(dataSize) // 512

	fmt.Printf("Mean: scalar=%d spmd=%d expected=%d\n", scalar, spmd, expected)
	if scalar != expected || spmd != expected {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			meanScalar(data)
			meanSPMD(data)
		}
	}

	scalarNs := bench(func() { meanScalar(data) })
	spmdNs := bench(func() { meanSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
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

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
