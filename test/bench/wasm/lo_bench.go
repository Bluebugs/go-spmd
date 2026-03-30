// lo_bench.go — TinyGo WASM benchmark for samber/lo-equivalent generic functions.
//
// Implements the same operations as samber/lo (Sum, Mean, Min, Max, Contains,
// Clamp) using identical pure-Go generic range loops so the generated WASM code
// is directly comparable to the SPMD SIMD examples.  The benchmark is built
// with standard TinyGo (no SPMD) and run with wasmtime.
//
// Input sizes and data construction match the SPMD lo-* integration tests
// exactly:
//   - sum / mean / min / max / contains: 1024 x int32
//   - clamp: 8192 x int32
//
// Output format:  "<op>: <ns>ns/op" (one line per operation, minimum of 7 runs)
// so the results can be compared directly with the SPMD example output.
package main

import (
	"fmt"
	"math"
	"time"
)

// ---- constants (match SPMD lo-* examples) ----------------------------------

const (
	smallSize  = 1024
	largeSize  = 8192
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

// ---- lo-equivalent generic functions (same as samber/lo internals) ---------
// These are pure-Go generic range loops, identical in structure to what
// samber/lo generates after inlining.  TinyGo compiles them without SPMD.

func Sum[T int32 | int64 | float32 | float64](collection []T) T {
	var sum T
	for _, v := range collection {
		sum += v
	}
	return sum
}

func Mean[T int32 | int64 | float32 | float64](collection []T) T {
	if len(collection) == 0 {
		return 0
	}
	return Sum(collection) / T(len(collection))
}

func Min[T int32 | int64 | float32 | float64](collection []T) T {
	var result T
	if len(collection) == 0 {
		return result
	}
	result = collection[0]
	for _, v := range collection[1:] {
		if v < result {
			result = v
		}
	}
	return result
}

func Max[T int32 | int64 | float32 | float64](collection []T) T {
	var result T
	if len(collection) == 0 {
		return result
	}
	result = collection[0]
	for _, v := range collection[1:] {
		if v > result {
			result = v
		}
	}
	return result
}

func Contains[T comparable](collection []T, element T) bool {
	for _, v := range collection {
		if v == element {
			return true
		}
	}
	return false
}

func Clamp[T int32 | int64 | float32 | float64](v, lo, hi T) T {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ClampSlice[T int32 | int64 | float32 | float64](src, dst []T, lo, hi T) {
	for i, v := range src {
		dst[i] = Clamp(v, lo, hi)
	}
}

// ---- bench helper -----------------------------------------------------------

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	// Return minimum (best-case, eliminates scheduling noise)
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}

// ---- main ------------------------------------------------------------------

func main() {
	// ---- sum ---------------------------------------------------------------
	sumData := make([]int32, smallSize)
	for i := range sumData {
		sumData[i] = int32(i + 1)
	}
	expectedSum := int32(smallSize * (smallSize + 1) / 2)

	// Warm up
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			_ = Sum(sumData)
		}
	}
	gotSum := Sum(sumData)
	if gotSum != expectedSum {
		fmt.Printf("sum FAIL: got %d want %d\n", gotSum, expectedSum)
		return
	}
	sumNs := bench(func() { _ = Sum(sumData) })
	fmt.Printf("sum:      %dns/op\n", sumNs)

	// ---- mean --------------------------------------------------------------
	meanData := sumData // same data
	expectedMean := int32(smallSize*(smallSize+1)/2) / int32(smallSize) // 512
	gotMean := Mean(meanData)
	if gotMean != expectedMean {
		fmt.Printf("mean FAIL: got %d want %d\n", gotMean, expectedMean)
		return
	}
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			_ = Mean(meanData)
		}
	}
	meanNs := bench(func() { _ = Mean(meanData) })
	fmt.Printf("mean:     %dns/op\n", meanNs)

	// ---- min ---------------------------------------------------------------
	minData := make([]int32, smallSize)
	for i := range minData {
		minData[i] = int32((i*7 + 13) % 10000)
	}
	minData[777] = -999 // planted minimum (matches SPMD lo-min)
	expectedMin := int32(-999)
	gotMin := Min(minData)
	if gotMin != expectedMin {
		fmt.Printf("min FAIL: got %d want %d\n", gotMin, expectedMin)
		return
	}
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			_ = Min(minData)
		}
	}
	minNs := bench(func() { _ = Min(minData) })
	fmt.Printf("min:      %dns/op\n", minNs)

	// ---- max ---------------------------------------------------------------
	maxData := make([]int32, smallSize)
	for i := range maxData {
		maxData[i] = int32((i*7 + 13) % 10000)
	}
	maxData[333] = 99999 // planted maximum (matches SPMD lo-max)
	expectedMax := int32(99999)
	gotMax := Max(maxData)
	if gotMax != expectedMax {
		fmt.Printf("max FAIL: got %d want %d\n", gotMax, expectedMax)
		return
	}
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			_ = Max(maxData)
		}
	}
	maxNs := bench(func() { _ = Max(maxData) })
	fmt.Printf("max:      %dns/op\n", maxNs)

	// ---- contains (worst-case: target is last element) ---------------------
	containsData := make([]int32, smallSize)
	for i := range containsData {
		containsData[i] = int32(i)
	}
	target := int32(smallSize - 1)
	if !Contains(containsData, target) {
		fmt.Println("contains FAIL: target not found")
		return
	}
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			_ = Contains(containsData, target)
		}
	}
	containsNs := bench(func() { _ = Contains(containsData, target) })
	fmt.Printf("contains: %dns/op\n", containsNs)

	// ---- clamp (8192 elements, lo=-100 hi=100) ----------------------------
	clampData := make([]int32, largeSize)
	for i := range clampData {
		clampData[i] = int32(i - largeSize/2)
	}
	clampOut := make([]int32, largeSize)
	loV, hiV := int32(-100), int32(100)

	// Correctness: check a few boundary values
	ClampSlice(clampData, clampOut, loV, hiV)
	if clampOut[0] != loV || clampOut[largeSize-1] != hiV {
		fmt.Printf("clamp FAIL: got [0]=%d [%d]=%d\n", clampOut[0], largeSize-1, clampOut[largeSize-1])
		return
	}

	_ = math.MaxInt32 // prevent dead-code elimination of math import
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			ClampSlice(clampData, clampOut, loV, hiV)
		}
	}
	clampNs := bench(func() { ClampSlice(clampData, clampOut, loV, hiV) })
	fmt.Printf("clamp:    %dns/op\n", clampNs)
}
