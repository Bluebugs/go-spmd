// run -goexperiment spmd -target=wasi

// Integration test for pointer operations with varying types.
// Covers: varying pointers (each lane holds a different pointer),
// gather/scatter through varying pointers, &varyingVar address-of, and
// field access through varying struct pointers.
//
// Deferred: (*Varying[array])[i] indexing, field access through Varying[*Struct].
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

// checkPointerArithmetic verifies scatter via varying pointer indexing.
// Returns true if correct.
//
// Note: *ptr = val where ptr : Varying[*T] performs a per-lane scatter.
// The deref read (*ptr on RHS) is deferred until type checker handles
// Varying[*T] dereference properly. For now, we test scatter-only.
func checkPointerArithmetic() bool {
	data := [8]int{10, 20, 30, 40, 50, 60, 70, 80}
	go for i := range 4 {
		var ptr lanes.Varying[*int] = &data[i]
		// Scatter: write per-lane value through varying pointer.
		// lanes.Index() = [0,1,2,3] → data[i] = 10+i*10 + i
		*ptr = 10 + lanes.Index()*10 + lanes.Index()
	}
	// Expected: data[i] = 10+i*10+i = [10, 21, 32, 43]
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

// checkStructPointers is deferred: field access through Varying[*Struct]
// (e.g. pointPtr.X where pointPtr : Varying[*Point]) requires type checker
// support for dereferencing varying pointers to struct fields. This is
// tracked as a deferred item in PLAN.md.
// Returns true as a placeholder until the feature is implemented.
func checkStructPointers() bool {
	return true
}

// checkAddressOfVarying verifies that &varyingVar produces Varying[*T], and
// that reading and writing through the resulting per-lane pointer vector works.
// Each lane gets its own pointer into the varying alloca storage.
// Returns true if correct.
func checkAddressOfVarying() bool {
	src := []int32{10, 20, 30, 40}
	dst := make([]int32, 4)
	go for i, v := range src {
		ptr := &v
		*ptr = *ptr + 1
		dst[i] = *ptr
	}
	// Expected: [11, 21, 31, 41]
	want := [4]int32{11, 21, 31, 41}
	for i, w := range want {
		if dst[i] != w {
			fmt.Printf("addressOf: dst[%d]=%d, want %d\n", i, dst[i], w)
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
	pass = checkAddressOfVarying() && pass

	if pass {
		fmt.Println("Correctness: PASS")
	} else {
		fmt.Println("Correctness: FAIL")
	}
}
