// run -goexperiment spmd -target=wasi

// Example demonstrating lanes.Index() context restrictions
// Shows valid and invalid usage patterns for lanes.Index()
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// VALID: SPMD function using lanes.Index() (has varying parameter)
func spmdWithLanesIndex(data varying int) varying int {
	// OK: SPMD function can use lanes.Index() because lane count inferred from varying parameter
	lane := lanes.Index()
	fmt.Printf("SPMD function - lane %v processing %v\n", lane, data)
	return data + lane
}

// INVALID: Non-SPMD function trying to use lanes.Index()
// func nonSPMDWithLanesIndex() varying int {
//     // ERROR: lanes.Index() requires varying parameters or go for context
//     lane := lanes.Index()  // Compile error
//     return varying(lane)
// }

// VALID: Non-SPMD function using other lanes functions
func nonSPMDWithOtherLanes() varying int {
	// OK: Most lanes functions work without SPMD context
	data := varying(100)
	broadcasted := lanes.Broadcast(data, 0)
	
	// OK: reduce functions work anywhere
	sum := reduce.Add(broadcasted)
	
	return lanes.Broadcast(sum, 0)
}

// VALID: lanes.Index() in go for loop
func directGoForUsage() {
	fmt.Println("\n=== Direct go for Usage ===")
	
	go for i := range 6 {
		// OK: inside go for loop provides SPMD context
		lane := lanes.Index()
		fmt.Printf("Direct go for - lane %v, iteration %v\n", lane, i)
		
		// Can use lane for computations
		result := i * 10 + lane
		fmt.Printf("Lane %v result: %v\n", lane, result)
	}
}

// VALID: SPMD function called from non-SPMD context
func demonstrateSPMDFromNonSPMD() {
	fmt.Println("\n=== SPMD Function from Non-SPMD Context ===")
	
	// Create varying data in non-SPMD context
	data := varying(50)
	
	// OK: SPMD function can use lanes.Index() even when called from non-SPMD context
	// because it can infer lane count from the varying parameter
	result := spmdWithLanesIndex(data)
	fmt.Printf("Result from SPMD function: %v\n", result)
	
	// OK: Call SPMD function multiple times
	for i := 0; i < 3; i++ {
		input := varying(i * 20)
		output := spmdWithLanesIndex(input)
		total := reduce.Add(output)
		fmt.Printf("Non-SPMD call %d: total = %d\n", i, total)
	}
}

// VALID: SPMD function called from go for context
func demonstrateSPMDFromGoFor() {
	fmt.Println("\n=== SPMD Function from go for Context ===")
	
	go for i := range 4 {
		// Create varying data in SPMD context
		data := varying(i * 15)
		
		// OK: SPMD function called from go for context
		result := spmdWithLanesIndex(data)
		fmt.Printf("go for iteration %v: result = %v\n", i, result)
	}
}

// VALID: Nested SPMD functions with lanes.Index()
func nestedSPMDFunction(base varying int, multiplier varying int) varying int {
	// OK: SPMD function with multiple varying parameters
	lane := lanes.Index()
	step1 := spmdWithLanesIndex(base)     // Call to another SPMD function
	step2 := step1 * multiplier
	
	fmt.Printf("Nested SPMD lane %v: base=%v, step1=%v, step2=%v\n", 
		lane, base, step1, step2)
	
	return step2
}

func demonstrateNestedSPMD() {
	fmt.Println("\n=== Nested SPMD Functions ===")
	
	// From non-SPMD context
	base := varying(10)
	multiplier := varying(3)
	result := nestedSPMDFunction(base, multiplier)
	fmt.Printf("Nested result: %v\n", result)
	
	// From go for context
	go for i := range 3 {
		localBase := varying(i * 5)
		localMult := varying(2)
		nestedResult := nestedSPMDFunction(localBase, localMult)
		total := reduce.Add(nestedResult)
		fmt.Printf("go for nested total: %d\n", total)
	}
}

// Demonstrate error cases (commented out to avoid compile errors)
func demonstrateErrorCases() {
	fmt.Println("\n=== Error Cases (Commented Out) ===")
	
	// 1. lanes.Index() in non-SPMD function
	// func errorCase1() varying int {
	//     return varying(lanes.Index())  // ERROR: no varying params, no go for
	// }
	
	// 2. lanes.Index() outside any context
	// func errorCase2() {
	//     lane := lanes.Index()  // ERROR: no SPMD context
	// }
	
	// 3. lanes.Index() in non-SPMD function called from go for
	// func errorCase3() varying int {
	//     return varying(lanes.Index())  // ERROR: still illegal even if called from go for
	// }
	// 
	// func caller() {
	//     go for i := range 4 {
	//         result := errorCase3()  // Would be compile error
	//     }
	// }
	
	fmt.Println("Error cases are commented out to prevent compile errors")
	fmt.Println("Key rule: lanes.Index() needs varying parameters (SPMD function) or go for context")
}

// Valid helper: shows lanes.Index() working in SPMD function
func processBatch(items varying int, offset varying int) varying int {
	// OK: SPMD function can use lanes.Index()
	lane := lanes.Index()
	baseValue := items + offset
	laneSpecific := baseValue + lane
	
	fmt.Printf("Batch processing lane %v: items=%v, offset=%v, result=%v\n",
		lane, items, offset, laneSpecific)
	
	return laneSpecific
}

func demonstrateBatchProcessing() {
	fmt.Println("\n=== Batch Processing with lanes.Index() ===")
	
	// Called from different contexts
	items := varying(100)
	offset := varying(10)
	
	// From non-SPMD context - still works!
	result1 := processBatch(items, offset)
	fmt.Printf("Non-SPMD context result: %v\n", result1)
	
	// From go for context
	go for i := range 2 {
		batchItems := varying(i * 50)
		batchOffset := varying(5)
		result2 := processBatch(batchItems, batchOffset)
		total := reduce.Add(result2)
		fmt.Printf("go for context total: %d\n", total)
	}
}

func main() {
	fmt.Println("=== lanes.Index() Context Restrictions ===")
	
	// Test 1: Direct go for usage
	directGoForUsage()
	
	// Test 2: SPMD function from non-SPMD context
	demonstrateSPMDFromNonSPMD()
	
	// Test 3: SPMD function from go for context
	demonstrateSPMDFromGoFor()
	
	// Test 4: Nested SPMD functions
	demonstrateNestedSPMD()
	
	// Test 5: Error cases (commented out)
	demonstrateErrorCases()
	
	// Test 6: Batch processing example
	demonstrateBatchProcessing()
	
	// Test 7: Non-SPMD functions with other lanes operations
	fmt.Println("\n=== Non-SPMD Functions with Other lanes Operations ===")
	nonSPMDResult := nonSPMDWithOtherLanes()
	fmt.Printf("Non-SPMD lanes operations result: %v\n", nonSPMDResult)
	
	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ lanes.Index() works in go for loops")
	fmt.Println("✓ lanes.Index() works in SPMD functions (with varying parameters)")
	fmt.Println("✓ SPMD functions with lanes.Index() can be called from any context")
	fmt.Println("✓ Non-SPMD functions cannot use lanes.Index()")
	fmt.Println("✓ Other lanes functions work in any context")
	fmt.Println("✓ All lanes.Index() restrictions demonstrated successfully")
}