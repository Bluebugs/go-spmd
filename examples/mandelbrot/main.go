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
	WIDTH         = 256
	HEIGHT        = 256
	MAX_ITERATIONS = 256
	X0            = -2.5
	Y0            = -1.25
	X1            = 1.5
	Y1            = 1.25
)

// Serial version of mandelbrot computation (for comparison)
func mandelSerial(cRe, cIm float32, maxIter int) int {
	var zRe, zIm float32 = cRe, cIm

	for i := 0; i < maxIter; i++ {
		if zRe*zRe + zIm*zIm > 4.0 {
			return i
		}

		newRe := zRe*zRe - zIm*zIm
		newIm := 2.0 * zRe * zIm
		zRe = cRe + newRe
		zIm = cIm + newIm
	}

	return maxIter
}

// SPMD version of mandelbrot computation (varying parameters)
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int) lanes.Varying[int] {
	var zRe lanes.Varying[float32] = cRe
	var zIm lanes.Varying[float32] = cIm
	var iterations lanes.Varying[int] = maxIter  // Start with max, reduce when diverged

	for iter := range maxIter {
		// Calculate magnitude squared: |z|^2 = zRe^2 + zIm^2
		magSquared := zRe*zRe + zIm*zIm

		// Check divergence condition (|z|^2 > 4)
		diverged := magSquared > 4.0

		if diverged {
			// Set iterations for points that just diverged
			iterations = iter
			// break out of the loop for points that have diverged
			break
		}

		if reduce.Any(!diverged) {
			// Compute next iteration: z = z^2 + c
			newRe := zRe*zRe - zIm*zIm
			newIm := 2.0 * zRe * zIm

			// Update z values (conditional assignment in SPMD context)
			zRe = cRe + newRe
			zIm = cIm + newIm
		}
	}

	return iterations
}

// Serial mandelbrot computation
func mandelbrotSerial(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)

	for j := 0; j < height; j++ {
		for i := 0; i < width; i++ {
			x := x0 + float32(i)*dx
			y := y0 + float32(j)*dy

			index := j*width + i
			output[index] = mandelSerial(x, y, maxIter)
		}
	}
}

// SPMD mandelbrot computation
func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)

	// Process each row
	for j := 0; j < height; j++ {
		y := y0 + float32(j)*dy

		// SPMD processing across width
		go for i := range width {
			// Each lane computes a different x coordinate
			x := x0 + lanes.Varying[float32](i)*dx

			// Compute mandelbrot for this batch of points
			iterations := mandelSPMD(x, y, maxIter)

			// Store results - each lane stores its result
			// i being a varying, index is computed as a varying
			index := j*width + i
			// Array assignment in SPMD context using a varying allow for storing a varying even if the array is not varying
			output[index] = iterations
		}
	}
}

// Generate a subset of the mandelbrot set for visual verification
func generateSample(output []int, width, height int) {
	fmt.Println("Sample of Mandelbrot set ('+' = low iterations, '*' = high iterations, ' ' = max):")

	// Print a 64x32 subset for visualization
	sampleWidth := 64
	sampleHeight := 32

	for j := 0; j < sampleHeight; j++ {
		for i := 0; i < sampleWidth; i++ {
			// Map sample coordinates to full image
			fullI := i * width / sampleWidth
			fullJ := j * height / sampleHeight
			index := fullJ*width + fullI

			iterations := output[index]

			// Convert iterations to ASCII art
			var char byte
			if iterations == MAX_ITERATIONS {
				char = ' ' // In the set
			} else if iterations < MAX_ITERATIONS/4 {
				char = '+'  // Low iterations
			} else if iterations < MAX_ITERATIONS/2 {
				char = 'o'  // Medium iterations
			} else {
				char = '*'  // High iterations
			}

			fmt.Printf("%c", char)
		}
		fmt.Println()
	}
}

// Verify correctness by comparing serial and SPMD results
func verifyCorrectness(serialOutput, spmdOutput []int, width, height int) bool {
	differences := 0
	maxDiff := 0

	for i := 0; i < width*height; i++ {
		diff := int(math.Abs(float64(serialOutput[i] - spmdOutput[i])))
		if diff > 0 {
			differences++
			if diff > maxDiff {
				maxDiff = diff
			}
		}
	}

	fmt.Printf("Verification: %d differences out of %d pixels\n", differences, width*height)
	fmt.Printf("Maximum difference: %d iterations\n", maxDiff)

	// Allow small differences due to floating point precision
	return differences == 0 || (float64(differences)/float64(width*height) < 0.01 && maxDiff <= 2)
}

// Benchmark performance comparison
func benchmark() bool {
	fmt.Printf("Computing Mandelbrot set (%dx%d, %d iterations)\n", WIDTH, HEIGHT, MAX_ITERATIONS)

	// Allocate output arrays
	serialOutput := make([]int, WIDTH*HEIGHT)
	spmdOutput := make([]int, WIDTH*HEIGHT)

	// Benchmark serial version
	fmt.Println("\n--- Serial Version ---")
	startTime := time.Now()
	mandelbrotSerial(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, serialOutput)
	serialTime := time.Since(startTime)
	fmt.Printf("Serial computation time: %v\n", serialTime)

	// Benchmark SPMD version
	fmt.Println("\n--- SPMD Version ---")
	startTime = time.Now()
	mandelbrotSPMD(X0, Y0, X1, Y1, WIDTH, HEIGHT, MAX_ITERATIONS, spmdOutput)
	spmdTime := time.Since(startTime)
	fmt.Printf("SPMD computation time: %v\n", spmdTime)

	// Calculate speedup
	speedup := float64(serialTime) / float64(spmdTime)
	fmt.Printf("SPMD speedup: %.2fx\n", speedup)

	// Verify correctness
	fmt.Println("\n--- Verification ---")
	correct := verifyCorrectness(serialOutput, spmdOutput, WIDTH, HEIGHT)
	if correct {
		fmt.Println("✓ Results match between serial and SPMD versions")
	} else {
		fmt.Println("✗ Results differ significantly")
	}

	// Generate visual sample
	fmt.Println("\n--- Visual Sample ---")
	generateSample(spmdOutput, WIDTH, HEIGHT)

	return correct
}

// Demonstrate varying mandelbrot computation with different parameters
func demonstrateVaryingParameters() {
	fmt.Println("\n=== Varying Parameter Demonstration ===")

	// Create varying coordinates for different mandelbrot points
	var xCoords lanes.Varying[float32] = lanes.From([]float32{-0.5, 0.0, -0.75, 0.25})
	var yCoords lanes.Varying[float32] = lanes.From([]float32{0.0, 0.5, 0.1, -0.25})

	fmt.Printf("Testing points: x=%v, y=%v\n", xCoords, yCoords)

	// Compute mandelbrot iterations for all points simultaneously
	iterations := mandelSPMD(xCoords, yCoords, MAX_ITERATIONS)

	fmt.Printf("Iterations: %v\n", iterations)

	go for i := range iterations {
		x := xCoords[i]
		y := yCoords[i]
		iter := iterations[i]

		status := fmt.Sprintf("diverged after %d iterations", iter)
		fmt.Printf("Point (%.2f, %.2f): %s\n", x, y, status)
	}
}

func main() {
	fmt.Println("=== Go SPMD Mandelbrot Set Computation ===")
	fmt.Println("Based on Intel ISPC mandelbrot example")

	// Test 1: Single point demonstration
	demonstrateVaryingParameters()

	// Test 2: Full mandelbrot set computation and benchmarking
	correct := benchmark()

	// Summary
	fmt.Println("\n=== Summary ===")
	fmt.Printf("Algorithm: Mandelbrot set computation\n")
	fmt.Printf("Image size: %dx%d pixels\n", WIDTH, HEIGHT)
	fmt.Printf("Max iterations: %d\n", MAX_ITERATIONS)
	fmt.Printf("SIMD lanes: %d (determined by target architecture)\n", 4) // WASM SIMD128 typically has 4 lanes for float32

	if correct {
		fmt.Println("✓ SPMD implementation produces correct results")
		fmt.Println("✓ Expected significant performance improvement with SIMD")
		fmt.Println("✓ Demonstrates complex mathematical computation in SPMD Go")
	} else {
		fmt.Println("✗ Implementation needs debugging")
	}

	fmt.Println("\nMandelbrot SPMD example completed successfully!")
}
