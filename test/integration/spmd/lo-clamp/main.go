// run -goexperiment spmd

// Lo Clamp SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"os"
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
		data[i] = int32(i - dataSize/2) // -512 to 511
	}
	lo := int32(-100)
	hi := int32(100)

	scalar := clampScalar(data, lo, hi)
	spmd := clampSPMD(data, lo, hi)

	pass := true
	for i := range data {
		if scalar[i] != spmd[i] {
			fmt.Printf("FAIL at index %d: scalar=%d spmd=%d\n", i, scalar[i], spmd[i])
			pass = false
			break
		}
	}
	if !pass {
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			clampScalar(data, lo, hi)
			clampSPMD(data, lo, hi)
		}
	}

	scalarNs := bench(func() { clampScalar(data, lo, hi) })
	spmdNs := bench(func() { clampSPMD(data, lo, hi) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
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
