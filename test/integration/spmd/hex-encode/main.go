// run -goexperiment spmd -target=wasi

// Hexadecimal encoding benchmark: SPMD vs Scalar
// From: practical-vector.md
package main

import (
	"fmt"
	"os"
	"time"
)

const hextable = "0123456789abcdef"
const dataSize = 1024
const iterations = 1000

const (
	WARMUP_RUNS = 3
	BENCH_RUNS  = 7
)

func main() {
	fmt.Println("=== Hex-Encode SPMD Benchmark ===")
	fmt.Printf("Data: %d bytes, Iterations: %d per run\n", dataSize, iterations)
	fmt.Printf("Warmup: %d runs, Bench: %d runs\n\n", WARMUP_RUNS, BENCH_RUNS)

	// Generate pseudo-random test data (deterministic)
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xFF)
	}
	dst1 := make([]byte, len(data)*2)
	dst2 := make([]byte, len(data)*2)
	dst3 := make([]byte, len(data)*2)

	// --- Correctness check ---
	Encode(dst1, data)
	EncodeScalar(dst2, data)
	EncodeSrc(dst3, data)
	if string(dst1) != string(dst2) || string(dst1) != string(dst3) {
		fmt.Println("FAIL: SPMD and Scalar results mismatch!")
		os.Exit(1)
	}
	fmt.Println("Correctness: SPMD and Scalar results match.")

	// --- Warmup phase (not timed) ---
	fmt.Println("Warming up...")
	for i := 0; i < WARMUP_RUNS; i++ {
		for n := 0; n < iterations; n++ {
			EncodeScalar(dst2, data)
		}
		for n := 0; n < iterations; n++ {
			Encode(dst1, data)
		}
		for n := 0; n < iterations; n++ {
			EncodeSrc(dst3, data)
		}
	}

	// --- Benchmark scalar ---
	fmt.Println("Benchmarking scalar...")
	scalarTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			EncodeScalar(dst2, data)
		}
		scalarTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Benchmark SPMD (dst-centric) ---
	fmt.Println("Benchmarking SPMD (dst-centric)...")
	spmdDstTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			Encode(dst1, data)
		}
		spmdDstTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Benchmark SPMD (src-centric) ---
	fmt.Println("Benchmarking SPMD (src-centric)...")
	spmdSrcTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			EncodeSrc(dst3, data)
		}
		spmdSrcTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Statistics ---
	scalarMin, scalarAvg, scalarMax := stats(scalarTimes)
	dstMin, dstAvg, dstMax := stats(spmdDstTimes)
	srcMin, srcAvg, srcMax := stats(spmdSrcTimes)

	fmt.Println("\n--- Results ---")
	fmt.Printf("Scalar:         min=%s  avg=%s  max=%s\n",
		fmtDur(scalarMin), fmtDur(scalarAvg), fmtDur(scalarMax))
	fmt.Printf("SPMD dst:       min=%s  avg=%s  max=%s\n",
		fmtDur(dstMin), fmtDur(dstAvg), fmtDur(dstMax))
	fmt.Printf("SPMD src:       min=%s  avg=%s  max=%s\n",
		fmtDur(srcMin), fmtDur(srcAvg), fmtDur(srcMax))

	fmt.Printf("\nSpeedup dst (avg): %.2fx\n", float64(scalarAvg)/float64(dstAvg))
	fmt.Printf("Speedup dst (min): %.2fx\n", float64(scalarMin)/float64(dstMin))
	fmt.Printf("Speedup src (avg): %.2fx\n", float64(scalarAvg)/float64(srcAvg))
	fmt.Printf("Speedup src (min): %.2fx\n", float64(scalarMin)/float64(srcMin))

	// --- Per-run detail ---
	fmt.Println("\n--- Per-run times ---")
	fmt.Println("Run  Scalar        SPMD dst      Ratio   SPMD src      Ratio")
	for i := 0; i < BENCH_RUNS; i++ {
		dstRatio := float64(scalarTimes[i]) / float64(spmdDstTimes[i])
		srcRatio := float64(scalarTimes[i]) / float64(spmdSrcTimes[i])
		fmt.Printf("%2d   %-13s %-13s %.2fx  %-13s %.2fx\n",
			i+1, fmtDur(scalarTimes[i]), fmtDur(spmdDstTimes[i]), dstRatio,
			fmtDur(spmdSrcTimes[i]), srcRatio)
	}
}

func Encode(dst, src []byte) int {
	go for i := range dst {
		v := src[i>>1]
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
		} else {
			dst[i] = hextable[v&0x0f]
		}
	}

	return len(src) * 2
}

func EncodeSrc(dst, src []byte) int {
	go for i := range src {
		dst[i*2] = hextable[src[i]>>4]
		dst[i*2+1] = hextable[src[i]&0x0f]
	}

	return len(src) * 2
}

func EncodeScalar(dst, src []byte) int {
	j := 0
	for _, v := range src {
		dst[j] = hextable[v>>4]
		dst[j+1] = hextable[v&0x0f]
		j += 2
	}

	return len(src) * 2
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
