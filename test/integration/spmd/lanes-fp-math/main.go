// run -goexperiment spmd -target=wasi

// Integration test for lanes FP math primitives (float64 path).
// Exercises all 9 builtins: Sqrt, Abs, Floor, Ceil, Round, Trunc, Min, Max, FMA.
//
// Each test uses a 16-element dataset with the builtin applied inside a go-for
// loop and accumulated into a Varying, then reduce.Add at the end. The expected
// output sums are lane-count-invariant (same result on 2-wide WASM f64,
// 4-wide AVX2 f64, 8-wide AVX-512 f64, etc.).
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func testSqrt() float64 {
	// Perfect squares 1..16^2. sqrt sum = 1+2+...+16 = 136.
	data := []float64{1, 4, 9, 16, 25, 36, 49, 64, 81, 100, 121, 144, 169, 196, 225, 256}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Sqrt(x)
	}
	return reduce.Add(acc)
}

func testAbs() float64 {
	// Signed values summing to 136 in absolute magnitude: 1..16.
	data := []float64{-1, 2, -3, 4, -5, 6, -7, 8, -9, 10, -11, 12, -13, 14, -15, 16}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Abs(x)
	}
	return reduce.Add(acc) // 1+2+...+16 = 136
}

func testFloor() float64 {
	// Fractional x.5 values. Floor(x.5) = x. Sum = 1+2+...+16 = 136.
	data := []float64{1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5, 9.5, 10.5, 11.5, 12.5, 13.5, 14.5, 15.5, 16.5}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Floor(x)
	}
	return reduce.Add(acc)
}

func testCeil() float64 {
	// Same x.5 values. Ceil(x.5) = x+1. Sum = 2+3+...+17 = 152.
	data := []float64{1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5, 9.5, 10.5, 11.5, 12.5, 13.5, 14.5, 15.5, 16.5}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Ceil(x)
	}
	return reduce.Add(acc)
}

func testRound() float64 {
	// Same x.5 values. Round(x.5) with half-away-from-zero = x+1 for positive.
	// Sum = 2+3+...+17 = 152.
	data := []float64{1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5, 9.5, 10.5, 11.5, 12.5, 13.5, 14.5, 15.5, 16.5}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Round(x)
	}
	return reduce.Add(acc)
}

func testTrunc() float64 {
	// Same x.5 values. Trunc(x.5) = x (toward zero). Sum = 136.
	data := []float64{1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5, 9.5, 10.5, 11.5, 12.5, 13.5, 14.5, 15.5, 16.5}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Trunc(x)
	}
	return reduce.Add(acc)
}

func testMin() float64 {
	// a = 1..16, b = 2..17. Min(a, b) = a. Sum = 136.
	dataA := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	dataB := []float64{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
	var acc lanes.Varying[float64]
	go for i, a := range dataA {
		b := dataB[i]
		acc += lanes.Min(a, b)
	}
	return reduce.Add(acc)
}

func testMax() float64 {
	// a = 1..16, b = 2..17. Max(a, b) = b. Sum = 152.
	dataA := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	dataB := []float64{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
	var acc lanes.Varying[float64]
	go for i, a := range dataA {
		b := dataB[i]
		acc += lanes.Max(a, b)
	}
	return reduce.Add(acc)
}

func testFMA() float64 {
	// FMA(a, b, c) = a*b + c. a = 1..16, b = 2 (uniform broadcast), c = 0.
	// Result per lane = 2*a. Sum = 2*(1+2+...+16) = 272.
	dataA := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	var acc lanes.Varying[float64]
	go for i, a := range dataA {
		_ = i
		acc += lanes.FMA(a, 2.0, 0.0)
	}
	return reduce.Add(acc)
}

func testComposed() float64 {
	// Sqrt(Abs(-x^2)) = x. Sum = 1+2+...+16 = 136.
	data := []float64{-1, -4, -9, -16, -25, -36, -49, -64, -81, -100, -121, -144, -169, -196, -225, -256}
	var acc lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		acc += lanes.Sqrt(lanes.Abs(x))
	}
	return reduce.Add(acc)
}

func main() {
	fmt.Printf("Sqrt sum = %.2f (expect 136.00)\n", testSqrt())
	fmt.Printf("Abs sum = %.2f (expect 136.00)\n", testAbs())
	fmt.Printf("Floor sum = %.2f (expect 136.00)\n", testFloor())
	fmt.Printf("Ceil sum = %.2f (expect 152.00)\n", testCeil())
	fmt.Printf("Round sum = %.2f (expect 152.00)\n", testRound())
	fmt.Printf("Trunc sum = %.2f (expect 136.00)\n", testTrunc())
	fmt.Printf("Min sum = %.2f (expect 136.00)\n", testMin())
	fmt.Printf("Max sum = %.2f (expect 152.00)\n", testMax())
	fmt.Printf("FMA sum = %.2f (expect 272.00)\n", testFMA())
	fmt.Printf("Sqrt(Abs) sum = %.2f (expect 136.00)\n", testComposed())
}
