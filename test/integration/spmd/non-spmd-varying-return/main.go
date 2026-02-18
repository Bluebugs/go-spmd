// run -goexperiment spmd -target=wasi

// Example demonstrating non-SPMD functions returning varying values
// Shows functions without varying parameters that can return varying data
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Non-SPMD function returning varying with constant data
func createConstantVarying() lanes.Varying[int] {
	// No varying parameters = non-SPMD function
	// Can return varying values with uniform data broadcast to all lanes
	return lanes.Varying[int](42)  // All lanes get same value
}

// Non-SPMD function using reduce operations
func processAndReduce() lanes.Varying[int] {
	// LEGAL: reduce functions work outside SPMD context
	data := lanes.Varying[int](100)
	sum := reduce.Add(data)  // sum is uniform

	// Return varying value using lanes function
	return lanes.Broadcast(sum, 0)  // All lanes get the sum
}

// Non-SPMD function creating varying from array
func arrayToVarying() lanes.Varying[int] {
	// Create varying from uniform array data
	data := [4]int{10, 20, 30, 40}
	return lanes.From(data[:])  // Convert array to varying
}

// Non-SPMD function that would be ILLEGAL
func illegalFunction() lanes.Varying[int] {
	// This would be ILLEGAL - lanes.Index() requires SPMD context
	// return lanes.Index() * 10  // ERROR: lanes.Index() needs SPMD context

	// Instead, can only return uniform data broadcast to varying
	return lanes.Varying[int](999)
}

// SPMD function for comparison (has varying parameter)
func spmdFunction(input lanes.Varying[int]) lanes.Varying[int] {
	// This IS an SPMD function because it has varying parameter
	// Can use all SPMD context functions
	laneId := lanes.Index()  // LEGAL: in SPMD context
	return input + laneId
}

// Non-SPMD function processing uniform data to varying result
func uniformToVarying(base int) lanes.Varying[int] {
	// Takes uniform parameter, returns varying
	// Can use most lanes/reduce functions except lanes.Index()

	// Create varying from uniform input
	varyingBase := lanes.Varying[int](base)

	// Apply some processing
	doubled := varyingBase * 2

	// Use reduce operation
	total := reduce.Add(doubled)

	// Return processed varying data
	return lanes.Broadcast(total, 0)
}

// Non-SPMD function demonstrating complex processing
func complexProcessing() lanes.Varying[float64] {
	// Create multiple varying values
	data1 := lanes.Varying[float64](3.14)
	data2 := lanes.Varying[float64](2.71)

	// Perform operations
	sum := data1 + data2

	// Use cross-lane operations
	rotated := sum // lanes.Rotate deferred; pass through

	// Use reduction
	max := reduce.Max(rotated)

	// Return final varying result
	return lanes.Broadcast(max, 0)
}

func main() {
	fmt.Println("=== Non-SPMD Functions Returning Varying ===")

	// Test 1: Constant varying from non-SPMD function
	fmt.Println("\n--- Constant Varying Creation ---")
	constantData := createConstantVarying()
	fmt.Printf("Constant varying: %v\n", constantData)

	// Test 2: Process and reduce in non-SPMD function
	fmt.Println("\n--- Process and Reduce ---")
	processedData := processAndReduce()
	fmt.Printf("Processed data: %v\n", processedData)

	// Test 3: Array to varying conversion
	fmt.Println("\n--- Array to Varying ---")
	arrayData := arrayToVarying()
	fmt.Printf("Array varying: %v\n", arrayData)

	// Test 4: Uniform to varying processing
	fmt.Println("\n--- Uniform to Varying Processing ---")
	result := uniformToVarying(50)
	fmt.Printf("Uniform to varying result: %v\n", result)

	// Test 5: Complex processing
	fmt.Println("\n--- Complex Processing ---")
	complexResult := complexProcessing()
	fmt.Printf("Complex result: %v\n", complexResult)

	// Test 6: Use non-SPMD varying results in SPMD context
	fmt.Println("\n--- Using Non-SPMD Results in SPMD Context ---")
	baseData := createConstantVarying()  // Non-SPMD function result

	go for i := range 8 {
		_ = i // suppress unused variable
		// Use the non-SPMD result in SPMD context
		laneSpecific := spmdFunction(baseData)  // SPMD function call
		combined := laneSpecific + lanes.Index()  // Use SPMD context function

		fmt.Printf("Lane %v: base=%v, lane-specific=%v, combined=%v\n",
			lanes.Index(), baseData, laneSpecific, combined)
	}

	// Test 7: Demonstrate calling restrictions
	fmt.Println("\n--- Calling Restrictions Demo ---")

	// These are all non-SPMD functions (no varying parameters)
	const1 := createConstantVarying()
	const2 := processAndReduce()
	const3 := arrayToVarying()

	// Can call them from anywhere, including SPMD context
	go for i := range 4 {
		_ = i // suppress unused variable
		// All these calls work because functions have no varying parameters
		local1 := createConstantVarying()
		local2 := uniformToVarying(int(lanes.Index()))  // uniform parameter OK

		result := local1 + local2 + const1 + const2 + const3
		fmt.Printf("SPMD result lane %v: %v\n", lanes.Index(), result)
	}

	fmt.Println("\n✓ All non-SPMD varying return operations completed successfully")
}
