// run -goexperiment spmd -target=wasi

// Mandelbrot Set Computation in Go SPMD
// Based on Intel ISPC mandelbrot example
// Demonstrates complex mathematical computation with SIMD acceleration
package main

import (
	"fmt"
	"math"
	"time"
	"lanes"
	"reduce"
)

// Mandelbrot computation parameters
const (
	WIDTH          = 256
	HEIGHT         = 256
	MAX_ITERATIONS = int32(256)
	X0             = -2.5
	Y0             = -1.25
	X1             = 1.5
	Y1             = 1.25
)

// Serial version of mandelbrot computation (for comparison)
func mandelSerial(cRe, cIm float32, maxIter int32) int32 {
	var zRe, zIm float32 = cRe, cIm

	for i := int32(0); i < maxIter; i++ {
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

// SPMD version of mandelbrot computation (varying parameters).
// Uses int32 for iterations so all varying types are 4 bytes, giving the
// same lane count as float32 on any SIMD width (4 on SSE, 8 on AVX2).
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int32) lanes.Varying[int32] {
	var zRe lanes.Varying[float32] = cRe
	var zIm lanes.Varying[float32] = cIm
	var iterations lanes.Varying[int32] = maxIter

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

// Serial mandelbrot computation
func mandelbrotSerial(x0, y0, x1, y1 float32, width, height, maxIter int32, output []int32) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)

	for j := int32(0); j < height; j++ {
		for i := int32(0); i < width; i++ {
			x := x0 + float32(i)*dx
			y := y0 + float32(j)*dy

			index := j*width + i
			output[index] = mandelSerial(x, y, maxIter)
		}
	}
}

// SPMD mandelbrot computation
func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int32, output []int32) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)

	for j := int32(0); j < height; j++ {
		y := y0 + float32(j)*dy

		// SPMD processing across width — int32 loop variable matches float32 lane count
		go for i := range width {
			x := x0 + lanes.Varying[float32](i)*dx
			iterations := mandelSPMD(x, y, maxIter)
			index := j*width + i
			output[index] = iterations
		}
	}
}

// Generate a subset of the mandelbrot set for visual verification
func generateSample(output []int32, width, height int32) {
	fmt.Println("Sample of Mandelbrot set ('+' = low iterations, '*' = high iterations, ' ' = max):")

	sampleWidth := int32(64)
	sampleHeight := int32(32)

	for j := int32(0); j < sampleHeight; j++ {
		for i := int32(0); i < sampleWidth; i++ {
			fullI := i * width / sampleWidth
			fullJ := j * height / sampleHeight
			index := fullJ*width + fullI

			iterations := output[index]

			var char byte
			if iterations == MAX_ITERATIONS {
				char = ' '
			} else if iterations < MAX_ITERATIONS/4 {
				char = '+'
			} else if iterations < MAX_ITERATIONS/2 {
				char = 'o'
			} else {
				char = '*'
			}

			fmt.Printf("%c", char)
		}
		fmt.Println()
	}
}

// Verify correctness by comparing serial and SPMD results
func verifyCorrectness(serialOutput, spmdOutput []int32, width, height int32) bool {
	differences := 0
	maxDiff := int32(0)

	for i := int32(0); i < width*height; i++ {
		diff := int32(math.Abs(float64(serialOutput[i] - spmdOutput[i])))
		if diff > 0 {
			differences++
			if diff > maxDiff {
				maxDiff = diff
			}
		}
	}

	fmt.Printf("Verification: %d differences out of %d pixels\n", differences, width*height)
	fmt.Printf("Maximum difference: %d iterations\n", maxDiff)

	return differences == 0 || (float64(differences)/float64(width*height) < 0.01 && maxDiff <= 2)
}

// Benchmark performance comparison
func benchmark() bool {
	fmt.Printf("Computing Mandelbrot set (%dx%d, %d iterations)\n", WIDTH, HEIGHT, MAX_ITERATIONS)

	serialOutput := make([]int32, WIDTH*HEIGHT)
	spmdOutput := make([]int32, WIDTH*HEIGHT)

	fmt.Println("\n--- Serial Version ---")
	startTime := time.Now()
	mandelbrotSerial(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, serialOutput)
	serialTime := time.Since(startTime)
	fmt.Printf("Serial computation time: %v\n", serialTime)

	fmt.Println("\n--- SPMD Version ---")
	startTime = time.Now()
	mandelbrotSPMD(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, spmdOutput)
	spmdTime := time.Since(startTime)
	fmt.Printf("SPMD computation time: %v\n", spmdTime)

	speedup := float64(serialTime) / float64(spmdTime)
	fmt.Printf("SPMD speedup: %.2fx\n", speedup)

	fmt.Println("\n--- Verification ---")
	correct := verifyCorrectness(serialOutput, spmdOutput, WIDTH, HEIGHT)
	if correct {
		fmt.Println("Results match between serial and SPMD versions")
	} else {
		fmt.Println("Results differ significantly")
	}

	fmt.Println("\n--- Visual Sample ---")
	generateSample(spmdOutput, WIDTH, HEIGHT)

	return correct
}

// Demonstrate varying mandelbrot computation with different parameters
func demonstrateVaryingParameters() {
	fmt.Println("\n=== Varying Parameter Demonstration ===")

	xCoords := lanes.From([]float32{-0.5, 0.0, -0.75, 0.25})
	yCoords := lanes.From([]float32{0.0, 0.5, 0.1, -0.25})

	fmt.Printf("Testing points: x=%v, y=%v\n", reduce.From(xCoords), reduce.From(yCoords))

	iterations := mandelSPMD(xCoords, yCoords, MAX_ITERATIONS)

	fmt.Printf("Iterations: %v\n", reduce.From(iterations))

	xSlice := reduce.From(xCoords)
	ySlice := reduce.From(yCoords)
	iterSlice := reduce.From(iterations)

	for i := range iterSlice {
		if i < len(xSlice) && i < len(ySlice) {
			fmt.Printf("Point (%.2f, %.2f): diverged after %d iterations\n",
				xSlice[i], ySlice[i], iterSlice[i])
		}
	}
}

func main() {
	fmt.Println("=== Go SPMD Mandelbrot Set Computation ===")
	fmt.Println("Based on Intel ISPC mandelbrot example")

	demonstrateVaryingParameters()

	correct := benchmark()

	fmt.Println("\n=== Summary ===")
	fmt.Printf("Algorithm: Mandelbrot set computation\n")
	fmt.Printf("Image size: %dx%d pixels\n", WIDTH, HEIGHT)
	fmt.Printf("Max iterations: %d\n", MAX_ITERATIONS)

	if correct {
		fmt.Println("SPMD implementation produces correct results")
	} else {
		fmt.Println("Implementation needs debugging")
	}

	fmt.Println("\nMandelbrot SPMD example completed successfully!")
}
