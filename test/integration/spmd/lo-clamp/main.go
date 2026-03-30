// run -goexperiment spmd
//
// Lo Clamp SPMD equivalent — E2E test with scalar vs SPMD benchmark.
// Pre-allocates output buffer to measure pure compute, not allocation.
package main

import (
	"fmt"
	"os"
	"time"
)

const (
	dataSize   = 8192
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i - dataSize/2)
	}
	lo := int32(-100)
	hi := int32(100)

	// Correctness check (allocating versions for simplicity).
	scalarResult := make([]int32, len(data))
	spmdResult := make([]int32, len(data))
	clampScalar(data, scalarResult, lo, hi)
	clampSPMD(data, spmdResult, lo, hi)

	pass := true
	for i := range data {
		if scalarResult[i] != spmdResult[i] {
			fmt.Printf("FAIL at index %d: scalar=%d spmd=%d\n", i, scalarResult[i], spmdResult[i])
			pass = false
			break
		}
	}
	if !pass {
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	// Benchmark: pre-allocated buffers, measuring pure compute.
	scalarBuf := make([]int32, len(data))
	spmdBuf := make([]int32, len(data))

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			clampScalar(data, scalarBuf, lo, hi)
			clampSPMD(data, spmdBuf, lo, hi)
		}
	}

	scalarNs := bench(func() { clampScalar(data, scalarBuf, lo, hi) })
	spmdNs := bench(func() { clampSPMD(data, spmdBuf, lo, hi) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func clampScalar(data, result []int32, lo, hi int32) {
	for i, v := range data {
		if v < lo {
			result[i] = lo
		} else if v > hi {
			result[i] = hi
		} else {
			result[i] = v
		}
	}
}

func clampSPMD(data, result []int32, lo, hi int32) {
	go for i, v := range data {
		if v < lo {
			result[i] = lo
		} else if v > hi {
			result[i] = hi
		} else {
			result[i] = v
		}
	}
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
