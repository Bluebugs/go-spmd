// bench-stdlib.go
//
// Benchmark for Go stdlib encoding/hex encode.
// Used to establish a baseline for comparison with SPMD hex-encode paths.
//
// Build and run natively:
//
//	go run test/integration/spmd/hex-encode/bench-stdlib.go
//
// Build for WASI:
//
//	tinygo build -target=wasi -scheduler=none \
//	    -o /tmp/hex-stdlib.wasm \
//	    test/integration/spmd/hex-encode/bench-stdlib.go
package main

import (
	"encoding/hex"
	"fmt"
	"time"
)

const dataSize = 1024
const iterations = 1000

const (
	WARMUP_RUNS = 3
	BENCH_RUNS  = 7
)

func main() {
	fmt.Println("=== Go stdlib encoding/hex Encode Benchmark ===")
	fmt.Printf("Data: %d bytes, Iterations: %d per run\n", dataSize, iterations)
	fmt.Printf("Warmup: %d runs, Bench: %d runs\n\n", WARMUP_RUNS, BENCH_RUNS)

	// Same deterministic payload as main.go so numbers are comparable.
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xFF)
	}
	dst := make([]byte, len(data)*2)

	// Warmup.
	for i := 0; i < WARMUP_RUNS; i++ {
		for n := 0; n < iterations; n++ {
			hex.Encode(dst, data)
		}
	}

	// Timed rounds.
	times := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			hex.Encode(dst, data)
		}
		times[i] = time.Since(start).Nanoseconds()
	}

	min, avg, max := stats(times)
	fmt.Printf("Stdlib: min=%s  avg=%s  max=%s\n", fmtDur(min), fmtDur(avg), fmtDur(max))
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
