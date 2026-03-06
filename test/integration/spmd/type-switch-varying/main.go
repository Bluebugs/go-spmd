// run -goexperiment spmd -target=wasi

// Example demonstrating type assertions with varying values boxed in interface{}
// Scenario 1: uniform interface{} holds a boxed lanes.Varying[T] value
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// processMixed demonstrates type switch on interface{} with varying cases
func processMixed(value interface{}) {
	switch v := value.(type) {
	case lanes.Varying[int]:
		result := v * 2
		fmt.Printf("Varying int: sum=%d\n", reduce.Add(result))
	case int:
		fmt.Printf("Uniform int: %d\n", v)
	default:
		fmt.Printf("Other: %T\n", v)
	}
}

// testCommaOk demonstrates comma-ok type assertion
func testCommaOk() {
	fmt.Println("\n=== Comma-Ok Type Assertion ===")

	var x interface{} = lanes.Varying[int](42)

	if v, ok := x.(lanes.Varying[int]); ok {
		sum := reduce.Add(v + 10)
		fmt.Printf("Assert ok: sum=%d\n", sum)
	}

	if _, ok := x.(lanes.Varying[float64]); !ok {
		fmt.Println("Assert fail as expected (not Varying[float64])")
	}

	if _, ok := x.(int); !ok {
		fmt.Println("Assert fail as expected (not int)")
	}
}

func main() {
	fmt.Println("=== Type Switch with Varying Types ===")

	// Test 1: Type switch with varying int
	processMixed(lanes.Varying[int](42))

	// Test 2: Type switch with uniform int
	processMixed(100)

	// Test 3: Comma-ok assertions
	testCommaOk()

	fmt.Println("\nAll type switch varying tests completed")
}
