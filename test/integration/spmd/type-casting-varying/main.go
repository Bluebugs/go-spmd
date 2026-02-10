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
	var narrow32 lanes.Varying[int32] = lanes.Varying[int32](large64)  // Valid: truncates to lower 32 bits
	fmt.Printf("int64 → int32: 0x%016x → 0x%08x\n", large64, narrow32)

	// Unsigned integer downcasting - 32-bit to 16-bit
	var wide32 lanes.Varying[uint32] = lanes.Varying[uint32](0x12345678)
	var narrow16 lanes.Varying[uint16] = lanes.Varying[uint16](wide32)  // Valid: truncates to lower 16 bits
	fmt.Printf("uint32 → uint16: 0x%08x → 0x%04x\n", wide32, narrow16)

	// Float downcasting - double to single precision
	var doublePrecision lanes.Varying[float64] = lanes.Varying[float64](3.141592653589793)
	var singlePrecision lanes.Varying[float32] = lanes.Varying[float32](doublePrecision)  // Valid: precision loss
	fmt.Printf("float64 → float32: %.15f → %.7f\n", doublePrecision, singlePrecision)

	// Demonstrate with constrained varying
	var constrainedWide lanes.Varying[uint32, 2] = lanes.Varying[uint32, 2]([2]uint32{0xAABBCCDD, 0x11223344})
	var constrainedNarrow lanes.Varying[uint16, 2] = lanes.Varying[uint16, 2](constrainedWide)  // Valid
	fmt.Printf("lanes.Varying[uint32, 2] → lanes.Varying[uint16, 2]: %v → %v\n", constrainedWide, constrainedNarrow)
}

// Demonstrate register capacity constraints
func demonstrateRegisterConstraints() {
	fmt.Println("\n=== SIMD Register Capacity Analysis ===")

	// Show bit usage for different types (WASM SIMD128 = 128 bits)
	var v4_uint32 lanes.Varying[uint32, 4] = lanes.Varying[uint32, 4]([4]uint32{1, 2, 3, 4})
	fmt.Printf("lanes.Varying[uint32, 4]: 4 × 32 = %d bits (fits in 128-bit SIMD)\n", 4*32)
	fmt.Printf("Data: %v\n", v4_uint32)

	var v8_uint16 lanes.Varying[uint16, 8] = lanes.Varying[uint16, 8]([8]uint16{1, 2, 3, 4, 5, 6, 7, 8})
	fmt.Printf("lanes.Varying[uint16, 8]: 8 × 16 = %d bits (fits in 128-bit SIMD)\n", 8*16)
	fmt.Printf("Data: %v\n", v8_uint16)

	var v16_uint8 lanes.Varying[uint8, 16] = lanes.Varying[uint8, 16]([16]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	fmt.Printf("lanes.Varying[uint8, 16]: 16 × 8 = %d bits (fits in 128-bit SIMD)\n", 16*8)
	fmt.Printf("Data: %v\n", v16_uint8)

	// This would exceed capacity if upcasting were allowed:
	fmt.Printf("\nWhy upcasting is prohibited:\n")
	fmt.Printf("lanes.Varying[uint32, 4] → uint64 would need: 4 × 64 = %d bits (exceeds 128-bit limit!)\n", 4*64)
	fmt.Printf("lanes.Varying[uint16, 8] → uint32 would need: 8 × 32 = %d bits (exceeds 128-bit limit!)\n", 8*32)
}

// Demonstrate practical downcasting use cases
func demonstratePracticalUseCases() {
	fmt.Println("\n=== Practical Downcasting Use Cases ===")

	// Use case 1: High-precision calculation with lower-precision output
	go for i := range 4 {
		highPrecisionCalc := lanes.Varying[float64](float64(i) * 3.141592653589793)
		outputResult := lanes.Varying[float32](highPrecisionCalc)  // Valid: truncate for output
		fmt.Printf("Lane %d: High precision %.15f → Output %.7f\n",
			lanes.Index(), highPrecisionCalc, outputResult)
	}

	// Use case 2: Hash calculation with size reduction
	fmt.Println("\nHash value truncation:")
	go for i := range 4 {
		hash64 := lanes.Varying[uint64](uint64(i * 0x123456789ABCDEF))
		hash32 := lanes.Varying[uint32](hash64)  // Valid: truncate hash to 32 bits
		hash16 := lanes.Varying[uint16](hash32)  // Valid: further truncate to 16 bits
		fmt.Printf("Lane %d: 64-bit 0x%016x → 32-bit 0x%08x → 16-bit 0x%04x\n",
			lanes.Index(), hash64, hash32, hash16)
	}
}

// Function demonstrating cross-type operations after casting
func demonstrateCrossTypeOperations() {
	fmt.Println("\n=== Cross-Type Operations After Casting ===")

	// Original data in different sizes
	var wide lanes.Varying[uint32, 4] = lanes.Varying[uint32, 4]([4]uint32{0x12345678, 0x9ABCDEF0, 0x13579BDF, 0x2468ACE0})
	var narrow lanes.Varying[uint16, 8] = lanes.Varying[uint16, 8]([8]uint16{0x1111, 0x2222, 0x3333, 0x4444, 0x5555, 0x6666, 0x7777, 0x8888})

	// Cast down to same size for operations
	var wideAsNarrow lanes.Varying[uint16, 4] = lanes.Varying[uint16, 4](wide)   // Downcast 32→16
	var narrowSlice lanes.Varying[uint16, 4] = lanes.Varying[uint16, 4]([4]uint16{narrow[0], narrow[1], narrow[2], narrow[3]})  // Take first 4 elements

	// Now both are uint16, can combine
	var combined lanes.Varying[uint16, 4] = wideAsNarrow + narrowSlice

	fmt.Printf("Original wide (uint32): %v\n", wide)
	fmt.Printf("Wide cast to uint16: %v\n", wideAsNarrow)
	fmt.Printf("Narrow slice (uint16): %v\n", narrowSlice)
	fmt.Printf("Combined result: %v\n", combined)
}

func main() {
	fmt.Println("=== SPMD Type Casting Demonstration ===")

	// Test 1: Valid downcasting examples
	demonstrateValidCasting()

	// Test 2: SIMD register capacity constraints
	demonstrateRegisterConstraints()

	// Test 3: Practical use cases for downcasting
	demonstratePracticalUseCases()

	// Test 4: Cross-type operations after casting
	demonstrateCrossTypeOperations()

	// Summary of casting rules
	fmt.Println("\n=== Type Casting Rules Summary ===")
	fmt.Println("✓ Downcasting (larger → smaller): ALLOWED")
	fmt.Println("  - Explicit truncation with predictable behavior")
	fmt.Println("  - Results in same or fewer SIMD registers needed")
	fmt.Println("  - Examples: uint32→uint16, int64→int32, float64→float32")
	fmt.Println("")
	fmt.Println("✗ Upcasting (smaller → larger): PROHIBITED")
	fmt.Println("  - Would exceed SIMD register capacity")
	fmt.Println("  - lanes.Varying[uint32, 4] (128 bits) → lanes.Varying[uint64, 4] (256 bits)")
	fmt.Println("  - WASM SIMD128 only provides 128-bit registers")
	fmt.Println("  - Future: may be supported via lanes operations with register splitting")
	fmt.Println("")
	fmt.Println("All type casting tests completed successfully!")
}
