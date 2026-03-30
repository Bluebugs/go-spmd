// run -goexperiment spmd

// Lo Contains SPMD equivalent — E2E test with scalar vs SPMD benchmark
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
		data[i] = int32(i)
	}

	// Test found (worst case: at end)
	sf := containsScalar(data, int32(dataSize-1))
	pf := containsSPMD(data, int32(dataSize-1))
	// Test not found
	snf := containsScalar(data, -1)
	pnf := containsSPMD(data, -1)

	fmt.Printf("Contains (found): scalar=%v spmd=%v\n", sf, pf)
	fmt.Printf("Contains (not found): scalar=%v spmd=%v\n", snf, pnf)
	if sf != pf || snf != pnf || !sf || snf {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	target := int32(dataSize - 1) // worst case: last element

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			containsScalar(data, target)
			containsSPMD(data, target)
		}
	}

	scalarNs := bench(func() { containsScalar(data, target) })
	spmdNs := bench(func() { containsSPMD(data, target) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func containsScalar(data []int32, target int32) bool {
	for _, v := range data {
		if v == target {
			return true
		}
	}
	return false
}

func containsSPMD(data []int32, target int32) bool {
	var found lanes.Varying[bool] = false
	go for _, v := range data {
		if v == target {
			found = true
		}
		if reduce.Any(found) {
			return true
		}
	}
	return false
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
