// run -goexperiment spmd

// Lo Min SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"math"
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
		data[i] = int32((i*7 + 13) % 10000) // deterministic pseudo-random
	}
	data[777] = -999 // known minimum

	scalar := minScalar(data)
	spmd := minSPMD(data)
	fmt.Printf("Min: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != -999 {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			minScalar(data)
			minSPMD(data)
		}
	}

	scalarNs := bench(func() { minScalar(data) })
	spmdNs := bench(func() { minSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
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
