// run -goexperiment spmd -target=wasi

// Example demonstrating pointer operations with varying types
// Shows pointer to varying values and varying pointers
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Data structure for pointer examples
type Point struct {
	X, Y int
}

// processVaryingArray demonstrates pointer to varying array
func processVaryingArray(data *lanes.Varying[[8]int]) {
	fmt.Println("=== Pointer to Varying Array ===")
	
	go for i := range 8 {
		// Access varying array through pointer
		(*data)[i] *= 2
		(*data)[i] += lanes.Index()
	}
	
	// Convert to uniform for display
	result := reduce.From(*data)
	fmt.Printf("Processed array: %v\n", result)
}

// scatterGatherOperations demonstrates varying pointers
func scatterGatherOperations() {
	fmt.Println("\n=== Scatter/Gather with Varying Pointers ===")
	
	// Create separate memory locations
	var targets [8]int
	for i := range targets {
		targets[i] = i * 10
	}
	
	go for i := range 8 {
		// Each lane gets its own pointer
		var ptr lanes.Varying[*int] = &targets[i]
		
		// Gather: read from different memory locations
		value := *ptr
		
		// Process the value
		processed := value + lanes.Index()
		
		// Scatter: write back to different memory locations
		*ptr = processed
	}
	
	fmt.Printf("Scatter/Gather result: %v\n", targets)
}

// pointerArithmetic demonstrates varying pointer indexing
func pointerArithmetic() {
	fmt.Println("\n=== Varying Pointer Indexing ===")
	
	data := [16]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	
	go for i := range 8 {
		// Create varying indices
		index := i * 2  // index is varying: each lane has different value
		
		// Create varying pointers to different array elements
		varyingPtr := &data[index]  // Each lane points to different element
		
		// Dereference varying pointers
		value := *varyingPtr
		
		// Modify through varying pointer
		*varyingPtr = value + lanes.Index()
		
		fmt.Printf("Lane %v: index %v, original value %v\n", 
			lanes.Index(), index, value)
	}
	
	fmt.Printf("Modified data: %v\n", data[:16])
}

// indirectAccess demonstrates indirect access patterns
func indirectAccess() {
	fmt.Println("\n=== Indirect Access Patterns ===")
	
	// Array of pointers (uniform)
	var data [4]int = [4]int{100, 200, 300, 400}
	var ptrs [4]*int
	for i := range ptrs {
		ptrs[i] = &data[i]
	}
	
	go for i := range 4 {
		// Get varying pointer from uniform array
		ptr := ptrs[i] // ptr is varying (each lane gets different pointer)
		
		// Indirect access through varying pointer
		value := *ptr
		*ptr = value + lanes.Index()
	}
	
	fmt.Printf("Indirect access result: %v\n", data)
}

// mixedPointerOperations demonstrates complex pointer scenarios
func mixedPointerOperations() {
	fmt.Println("\n=== Mixed Pointer Operations ===")
	
	// Create varying data
	uniformArray := [4]int{10, 20, 30, 40}
	varyingData := lanes.From(uniformArray)
	
	// Pointer to varying array
	arrayPtr := &varyingData
	
	go for i := range 4 {
		// Access through pointer to varying
		(*arrayPtr)[i] += lanes.Index() * 5
		
		// Get address of specific varying element
		elemPtr := &(*arrayPtr)[i] // This creates varying *int
		
		// Modify through element pointer
		*elemPtr *= 2
	}
	
	// Display result
	result := reduce.From(varyingData)
	fmt.Printf("Mixed operations result: %v\n", result)
}

// structPointers demonstrates struct access with varying pointers
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
		// Get varying pointer to struct
		pointPtr := &points[i] // varying *Point
		
		// Access struct fields through varying pointer
		pointPtr.X += lanes.Index()
		pointPtr.Y += lanes.Index() * 2
	}
	
	fmt.Printf("Modified points: %+v\n", points)
}

// demonstrateAddressOperations shows taking addresses of varying values
func demonstrateAddressOperations() {
	fmt.Println("\n=== Address Operations ===")
	
	var varyingValues lanes.Varying[int] = 0
	
	go for i := range 4 {
		// Assign different values to each lane
		varyingValues = i * 10 + lanes.Index()
	}
	
	// Take address of varying value - each lane gets address of its data
	var varyingAddresses lanes.Varying[*int] = &varyingValues
	
	go for i := range 4 {
		// Dereference varying addresses
		value := *varyingAddresses
		
		// Modify through address
		*varyingAddresses = value + 100
	}
	
	// Show results
	finalValues := reduce.From(varyingValues)
	fmt.Printf("Address operations result: %v\n", finalValues)
}

func main() {
	fmt.Println("=== Pointer Operations with Varying Types ===")
	
	// Example 1: Pointer to varying array
	uniformTestArray := [8]int{1, 2, 3, 4, 5, 6, 7, 8}
	testArray := lanes.From(uniformTestArray)
	processVaryingArray(&testArray)
	
	// Example 2: Varying pointers for scatter/gather
	scatterGatherOperations()
	
	// Example 3: Pointer arithmetic
	pointerArithmetic()
	
	// Example 4: Indirect access patterns
	indirectAccess()
	
	// Example 5: Mixed pointer operations
	mixedPointerOperations()
	
	// Example 6: Struct pointers
	structPointers()
	
	// Example 7: Address operations
	demonstrateAddressOperations()
	
	fmt.Println("\n✓ All pointer varying operations completed successfully")
}