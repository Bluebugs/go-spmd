// run -goexperiment spmd -target=wasi

// Example demonstrating pointer operations with varying types.
// Shows varying pointers (each lane holds a different pointer),
// gather/scatter through varying pointers, and struct field access
// through varying struct pointers.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Point is a simple struct for demonstrating struct pointer access.
type Point struct {
	X, Y int
}

// scatterGatherOperations demonstrates varying pointers for gather/scatter.
func scatterGatherOperations() {
	fmt.Println("=== Scatter/Gather with Varying Pointers ===")

	// Create separate memory locations
	var targets [4]int
	for i := range targets {
		targets[i] = i * 10
	}

	// Gather: read from different memory locations via varying pointer
	var gathered lanes.Varying[int]
	go for i := range 4 {
		// Each lane gets its own pointer to a different element
		var ptr lanes.Varying[*int] = &targets[i]
		gathered = *ptr
	}
	fmt.Printf("Gathered: %v\n", reduce.From(gathered))
}

// pointerArithmetic demonstrates varying pointer indexing.
func pointerArithmetic() {
	fmt.Println("\n=== Varying Pointer Indexing ===")

	data := [8]int{0, 1, 2, 3, 4, 5, 6, 7}

	go for i := range 4 {
		// Create varying pointer to different array elements
		varyingPtr := &data[i]

		// Gather: dereference varying pointer
		value := *varyingPtr

		// Scatter: modify through varying pointer
		*varyingPtr = value + lanes.Index()
	}

	fmt.Printf("Modified data: %v\n", data[:4])
}

// structPointers demonstrates struct access with varying pointers.
// This is the key pattern fixed by Tasks 1-7: field access through *Varying[Struct].
func structPointers() {
	fmt.Println("\n=== Struct Pointers with Varying ===")

	// Array of structs
	points := [4]Point{
		{X: 1, Y: 2},
		{X: 3, Y: 4},
		{X: 5, Y: 6},
		{X: 7, Y: 8},
	}

	go for i := range 4 {
		// Get varying pointer to struct — each lane points to a different Point
		pointPtr := &points[i] // Varying[*Point]

		// Access struct fields through varying pointer (fixed by Tasks 1-7)
		pointPtr.X += lanes.Index()
		pointPtr.Y += lanes.Index() * 2
	}

	fmt.Printf("Modified points: %+v\n", points)
}

// indirectAccess demonstrates indirect access from a uniform pointer array.
func indirectAccess() {
	fmt.Println("\n=== Indirect Access Patterns ===")

	// Array of values and uniform pointer array
	var data [4]int = [4]int{100, 200, 300, 400}
	var ptrs [4]*int
	for i := range ptrs {
		ptrs[i] = &data[i]
	}

	// Gather read via varying pointer from uniform pointer array
	var gathered lanes.Varying[int]
	go for i := range 4 {
		ptr := ptrs[i] // varying *int — each lane gets a different pointer
		gathered = *ptr
	}
	fmt.Printf("Indirect gather result: %v\n", reduce.From(gathered))
}

func main() {
	fmt.Println("=== Pointer Operations with Varying Types ===")

	// Example 1: Scatter/gather via varying pointers
	scatterGatherOperations()

	// Example 2: Pointer arithmetic with varying index
	pointerArithmetic()

	// Example 3: Struct field access through varying pointer (key fix)
	structPointers()

	// Example 4: Indirect access via uniform pointer array
	indirectAccess()

	fmt.Println("\nAll pointer varying operations completed successfully")
}
