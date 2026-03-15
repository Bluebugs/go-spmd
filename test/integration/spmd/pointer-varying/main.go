// run -goexperiment spmd -target=wasi

// Integration test for pointer operations with varying types.
// Covers: varying pointers (each lane holds a different pointer),
// gather/scatter through varying pointers, and struct field access
// through varying struct pointers.
//
// Deferred: (*Varying[array])[i] indexing, &varyingVar address-of.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

type Point struct {
	X, Y int
}

// checkGather verifies that gather via varying pointer returns expected values.
// Returns true if correct.
func checkGather() bool {
	var targets [4]int
	for i := range targets {
		targets[i] = (i + 1) * 100
	}
	var gathered lanes.Varying[int]
	go for i := range 4 {
		var ptr lanes.Varying[*int] = &targets[i]
		gathered = *ptr
	}
	result := reduce.From(gathered)
	// Expected: [100 200 300 400]
	for i, v := range result {
		if v != (i+1)*100 {
			fmt.Printf("gather: result[%d]=%d, want %d\n", i, v, (i+1)*100)
			return false
		}
	}
	return true
}

// checkPointerArithmetic verifies scatter/gather via varying pointer indexing.
// Returns true if correct.
func checkPointerArithmetic() bool {
	data := [8]int{10, 20, 30, 40, 50, 60, 70, 80}
	go for i := range 4 {
		ptr := &data[i]
		*ptr = *ptr + lanes.Index()
	}
	// Expected: data[i] += i, so [10+0, 20+1, 30+2, 40+3] = [10, 21, 32, 43]
	want := [4]int{10, 21, 32, 43}
	for i, w := range want {
		if data[i] != w {
			fmt.Printf("ptrArith: data[%d]=%d, want %d\n", i, data[i], w)
			return false
		}
	}
	return true
}

// checkIndirect verifies gather via uniform pointer array.
// Returns true if correct.
func checkIndirect() bool {
	var data [4]int = [4]int{100, 200, 300, 400}
	var ptrs [4]*int
	for i := range ptrs {
		ptrs[i] = &data[i]
	}
	var gathered lanes.Varying[int]
	go for i := range 4 {
		ptr := ptrs[i]
		gathered = *ptr
	}
	result := reduce.From(gathered)
	for i, v := range result {
		if v != data[i] {
			fmt.Printf("indirect: result[%d]=%d, want %d\n", i, v, data[i])
			return false
		}
	}
	return true
}

// checkStructPointers verifies field access through *Varying[Struct].
// Returns true if correct.
func checkStructPointers() bool {
	points := [4]Point{
		{X: 1, Y: 2},
		{X: 3, Y: 4},
		{X: 5, Y: 6},
		{X: 7, Y: 8},
	}
	go for i := range 4 {
		pointPtr := &points[i]
		pointPtr.X += lanes.Index()
		pointPtr.Y += lanes.Index() * 2
	}
	// Expected: X += lane index (0,1,2,3), Y += 2*lane index (0,2,4,6)
	wantX := [4]int{1, 4, 7, 10}
	wantY := [4]int{2, 6, 10, 14}
	for i := range points {
		if points[i].X != wantX[i] || points[i].Y != wantY[i] {
			fmt.Printf("struct: points[%d]={%d,%d}, want {%d,%d}\n",
				i, points[i].X, points[i].Y, wantX[i], wantY[i])
			return false
		}
	}
	return true
}

func main() {
	fmt.Println("=== Pointer Operations with Varying Types ===")

	pass := true
	pass = checkGather() && pass
	pass = checkPointerArithmetic() && pass
	pass = checkIndirect() && pass
	pass = checkStructPointers() && pass

	if pass {
		fmt.Println("Correctness: PASS")
	} else {
		fmt.Println("Correctness: FAIL")
	}
}
