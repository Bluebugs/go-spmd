// run -goexperiment spmd

// Simple sum benchmark: SPMD vs Scalar
// From: go-data-parallelism.md
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
	"time"
)

const (
	dataSize    = 1024
	iterations  = 10000
	WARMUP_RUNS = 3
	BENCH_RUNS  = 7
)

func main() {
	fmt.Println("=== Simple Sum SPMD Benchmark ===")
	fmt.Printf("Data: %d ints, Iterations: %d per run\n", dataSize, iterations)
	fmt.Printf("Warmup: %d runs, Bench: %d runs\n\n", WARMUP_RUNS, BENCH_RUNS)

	data := make([]int, dataSize)
	for i := range data {
		data[i] = i + 1
	}

	// --- Correctness check ---
	scalarResult := sumScalar(data)
	spmdResult := sumSPMD(data)
	expected := dataSize * (dataSize + 1) / 2 // 524800
	fmt.Printf("Expected: %d\n", expected)
	fmt.Printf("Scalar:   %d\n", scalarResult)
	fmt.Printf("SPMD:     %d\n", spmdResult)
	if scalarResult != expected || spmdResult != expected {
		fmt.Println("FAIL: results mismatch!")
		os.Exit(1)
	}
	fmt.Println("Correctness: OK")

	// --- Warmup phase (not timed) ---
	fmt.Println("\nWarming up...")
	for i := 0; i < WARMUP_RUNS; i++ {
		for n := 0; n < iterations; n++ {
			sumScalar(data)
		}
		for n := 0; n < iterations; n++ {
			sumSPMD(data)
		}
	}

	// --- Benchmark scalar ---
	fmt.Println("Benchmarking scalar...")
	scalarTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			sumScalar(data)
		}
		scalarTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Benchmark SPMD ---
	fmt.Println("Benchmarking SPMD...")
	spmdTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			sumSPMD(data)
		}
		spmdTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Statistics ---
	scalarMin, scalarAvg, scalarMax := stats(scalarTimes)
	spmdMin, spmdAvg, spmdMax := stats(spmdTimes)

	fmt.Println("\n--- Results ---")
	fmt.Printf("Scalar:  min=%s  avg=%s  max=%s\n",
		fmtDur(scalarMin), fmtDur(scalarAvg), fmtDur(scalarMax))
	fmt.Printf("SPMD:    min=%s  avg=%s  max=%s\n",
		fmtDur(spmdMin), fmtDur(spmdAvg), fmtDur(spmdMax))
	fmt.Printf("\nSpeedup (avg): %.2fx\n", float64(scalarAvg)/float64(spmdAvg))
	fmt.Printf("Speedup (min): %.2fx\n", float64(scalarMin)/float64(spmdMin))

	// --- Per-run detail ---
	fmt.Println("\n--- Per-run times ---")
	fmt.Println("Run  Scalar        SPMD          Ratio")
	for i := 0; i < BENCH_RUNS; i++ {
		ratio := float64(scalarTimes[i]) / float64(spmdTimes[i])
		fmt.Printf("%2d   %-13s %-13s %.2fx\n",
			i+1, fmtDur(scalarTimes[i]), fmtDur(spmdTimes[i]), ratio)
	}
}

func sumScalar(data []int) int {
	total := 0
	for _, v := range data {
		total += v
	}
	return total
}

func sumSPMD(data []int) int {
	var total lanes.Varying[int] = 0

	go for _, value := range data {
		total += value
	}

	return reduce.Add(total)
}

func stats(times []int64) (min, avg, max int64) {
	min = times[0]
	max = times[0]
	var sum int64
	for _, t := range times {
		sum += t
		if t < min {
			min = t
		}
		if t > max {
			max = t
		}
	}
	avg = sum / int64(len(times))
	return
}

func fmtDur(ns int64) string {
	if ns < 1000 {
		return fmt.Sprintf("%dns", ns)
	}
	if ns < 1000000 {
		return fmt.Sprintf("%.1fus", float64(ns)/1000)
	}
	return fmt.Sprintf("%.3fms", float64(ns)/1000000)
}
