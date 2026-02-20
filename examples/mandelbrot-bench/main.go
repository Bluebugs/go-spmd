// run -goexperiment spmd -target=wasi

// Mandelbrot Benchmark - isolates SPMD vs serial computation time
// Eliminates initialization overhead via pre-allocation and warmup
package main

import (
	"fmt"
	"lanes"
	"math"
	"time"
)

const (
	WIDTH          = 256
	HEIGHT         = 256
	MAX_ITERATIONS = 256
	X0             = -2.5
	Y0             = -1.25
	X1             = 1.5
	Y1             = 1.25
	WARMUP_RUNS    = 3
	BENCH_RUNS     = 10
)

// Serial mandelbrot kernel
func mandelSerial(cRe, cIm float32, maxIter int) int {
	var zRe, zIm float32 = cRe, cIm
	for i := 0; i < maxIter; i++ {
		if zRe*zRe+zIm*zIm > 4.0 {
			return i
		}
		newRe := zRe*zRe - zIm*zIm
		newIm := 2.0 * zRe * zIm
		zRe = cRe + newRe
		zIm = cIm + newIm
	}
	return maxIter
}

// SPMD mandelbrot kernel
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int) lanes.Varying[int] {
	var zRe lanes.Varying[float32] = cRe
	var zIm lanes.Varying[float32] = cIm
	var iterations lanes.Varying[int] = maxIter

	for iter := range maxIter {
		magSquared := zRe*zRe + zIm*zIm
		diverged := magSquared > 4.0
		if diverged {
			iterations = iter
			break
		}
		newRe := zRe*zRe - zIm*zIm
		newIm := 2.0 * zRe * zIm
		zRe = cRe + newRe
		zIm = cIm + newIm
	}
	return iterations
}

// Serial outer loop
func mandelbrotSerial(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)
	for j := 0; j < height; j++ {
		for i := 0; i < width; i++ {
			x := x0 + float32(i)*dx
			y := y0 + float32(j)*dy
			output[j*width+i] = mandelSerial(x, y, maxIter)
		}
	}
}

// SPMD outer loop
func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)
	for j := 0; j < height; j++ {
		y := y0 + float32(j)*dy
		go for i := range width {
			x := x0 + lanes.Varying[float32](i)*dx
			iterations := mandelSPMD(x, y, maxIter)
			index := j*width + i
			output[index] = iterations
		}
	}
}

func main() {
	fmt.Println("=== Mandelbrot SPMD Benchmark ===")
	fmt.Printf("Image: %dx%d, Max iterations: %d\n", WIDTH, HEIGHT, MAX_ITERATIONS)
	fmt.Printf("Warmup: %d runs, Bench: %d runs\n\n", WARMUP_RUNS, BENCH_RUNS)

	// Pre-allocate once, reuse across all runs
	serialOutput := make([]int, WIDTH*HEIGHT)
	spmdOutput := make([]int, WIDTH*HEIGHT)

	// --- Warmup phase (not timed) ---
	fmt.Println("Warming up...")
	for i := 0; i < WARMUP_RUNS; i++ {
		mandelbrotSerial(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, serialOutput)
		mandelbrotSPMD(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, spmdOutput)
	}

	// --- Benchmark serial ---
	fmt.Println("Benchmarking serial...")
	serialTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		mandelbrotSerial(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, serialOutput)
		serialTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Benchmark SPMD ---
	fmt.Println("Benchmarking SPMD...")
	spmdTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		mandelbrotSPMD(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, spmdOutput)
		spmdTimes[i] = time.Since(start).Nanoseconds()
	}

	// --- Verify correctness ---
	differences := 0
	for i := 0; i < WIDTH*HEIGHT; i++ {
		diff := int(math.Abs(float64(serialOutput[i] - spmdOutput[i])))
		if diff > 0 {
			differences++
		}
	}

	// --- Statistics ---
	serialMin, serialAvg, serialMax := stats(serialTimes)
	spmdMin, spmdAvg, spmdMax := stats(spmdTimes)

	fmt.Println("\n--- Results ---")
	fmt.Printf("Serial:  min=%s  avg=%s  max=%s\n",
		fmtDur(serialMin), fmtDur(serialAvg), fmtDur(serialMax))
	fmt.Printf("SPMD:    min=%s  avg=%s  max=%s\n",
		fmtDur(spmdMin), fmtDur(spmdAvg), fmtDur(spmdMax))
	fmt.Printf("\nSpeedup (avg): %.2fx\n", float64(serialAvg)/float64(spmdAvg))
	fmt.Printf("Speedup (min): %.2fx\n", float64(serialMin)/float64(spmdMin))
	fmt.Printf("Correctness: %d differences out of %d pixels\n", differences, WIDTH*HEIGHT)

	// --- Per-run detail ---
	fmt.Println("\n--- Per-run times ---")
	fmt.Println("Run  Serial        SPMD          Ratio")
	for i := 0; i < BENCH_RUNS; i++ {
		ratio := float64(serialTimes[i]) / float64(spmdTimes[i])
		fmt.Printf("%2d   %-13s %-13s %.2fx\n",
			i+1, fmtDur(serialTimes[i]), fmtDur(spmdTimes[i]), ratio)
	}
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
