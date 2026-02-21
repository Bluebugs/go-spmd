// run -goexperiment spmd -target=wasi

// Example demonstrating varying type casting rules
// Shows valid downcasting and invalid upcasting for SPMD types
package main

import (
	"fmt"
	"lanes"
)

// Demonstrate valid downcasting (larger to smaller types)
func demonstrateValidCasting() {
	fmt.Println("=== Valid Downcasting Examples ===")

	// Integer downcasting - 64-bit to 32-bit
	var large64 lanes.Varying[int64] = lanes.Varying[int64](0x123456789ABCDEF0)
	var narrow32 lanes.Varying[int32] = lanes.Varying[int32](large64) // Valid: truncates to lower 32 bits
	fmt.Printf("int64 -> int32: 0x%016x -> 0x%08x\n", large64, narrow32)

	// Unsigned integer downcasting - 32-bit to 16-bit
	var wide32 lanes.Varying[uint32] = lanes.Varying[uint32](0x12345678)
	var narrow16 lanes.Varying[uint16] = lanes.Varying[uint16](wide32) // Valid: truncates to lower 16 bits
	fmt.Printf("uint32 -> uint16: 0x%08x -> 0x%04x\n", wide32, narrow16)

	// Float downcasting - double to single precision
	var doublePrecision lanes.Varying[float64] = lanes.Varying[float64](3.141592653589793)
	var singlePrecision lanes.Varying[float32] = lanes.Varying[float32](doublePrecision) // Valid: precision loss
	fmt.Printf("float64 -> float32: %.15f -> %.7f\n", doublePrecision, singlePrecision)
}

// Demonstrate practical downcasting use cases
func demonstratePracticalUseCases() {
	fmt.Println("\n=== Practical Downcasting Use Cases ===")

	// Use case 1: High-precision calculation with lower-precision output
	go for i := range 4 {
		highPrecisionCalc := lanes.Varying[float64](float64(i) * 3.141592653589793)
		outputResult := lanes.Varying[float32](highPrecisionCalc) // Valid: truncate for output
		fmt.Printf("Lane %d: High precision %.15f -> Output %.7f\n",
			lanes.Index(), highPrecisionCalc, outputResult)
	}

	// Use case 2: Hash calculation with size reduction
	fmt.Println("\nHash value truncation:")
	go for i := range 4 {
		hash64 := lanes.Varying[uint64](uint64(i * 0x123456789ABCDEF))
		hash32 := lanes.Varying[uint32](hash64) // Valid: truncate hash to 32 bits
		hash16 := lanes.Varying[uint16](hash32) // Valid: further truncate to 16 bits
		fmt.Printf("Lane %d: 64-bit 0x%016x -> 32-bit 0x%08x -> 16-bit 0x%04x\n",
			lanes.Index(), hash64, hash32, hash16)
	}
}

func main() {
	fmt.Println("=== SPMD Type Casting Demonstration ===")

	// Test 1: Valid downcasting examples
	demonstrateValidCasting()

	// Test 2: Practical use cases for downcasting
	demonstratePracticalUseCases()

	// Summary of casting rules
	fmt.Println("\n=== Type Casting Rules Summary ===")
	fmt.Println("Valid downcasting (larger -> smaller): ALLOWED")
	fmt.Println("  - Explicit truncation with predictable behavior")
	fmt.Println("  - Examples: uint32->uint16, int64->int32, float64->float32")
	fmt.Println("")
	fmt.Println("Upcasting (smaller -> larger): PROHIBITED")
	fmt.Println("  - Would exceed SIMD register capacity")
	fmt.Println("  - Varying[uint32] (128 bits) -> Varying[uint64] (256 bits)")
	fmt.Println("  - WASM SIMD128 only provides 128-bit registers")
	fmt.Println("")
	fmt.Println("All type casting tests completed successfully!")
}
