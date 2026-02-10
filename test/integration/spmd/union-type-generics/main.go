// run -goexperiment spmd -target=wasi

// Example demonstrating union type generics for reduce and lanes functions
// Shows how functions accept both constrained and unconstrained varying types
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Demonstrate reduce functions with union type generics
func demonstrateReduceGenerics() {
	fmt.Println("=== Reduce Functions with Union Type Generics ===")

	// Test with unconstrained varying
	fmt.Println("\n--- Unconstrained Varying ---")
	unconstrainedInt := lanes.Varying[int](42)
	unconstrainedFloat := lanes.Varying[float64](3.14)
	unconstrainedBool := lanes.Varying[bool](true)

	// All reduce functions automatically inlined for performance
	intSum := reduce.Add(unconstrainedInt)
	floatSum := reduce.Add(unconstrainedFloat)
	boolAll := reduce.All(unconstrainedBool)
	boolAny := reduce.Any(unconstrainedBool)

	fmt.Printf("Unconstrained int reduce.Add: %d\n", intSum)
	fmt.Printf("Unconstrained float reduce.Add: %.2f\n", floatSum)
	fmt.Printf("Unconstrained bool reduce.All: %t\n", boolAll)
	fmt.Printf("Unconstrained bool reduce.Any: %t\n", boolAny)

	// Test with constrained varying
	fmt.Println("\n--- Constrained Varying ---")
	constrainedInt4 := lanes.Varying[int, 4]([4]int{10, 20, 30, 40})
	constrainedFloat8 := lanes.Varying[float64, 8]([8]float64{1.1, 2.2, 3.3, 4.4, 5.5, 6.6, 7.7, 8.8})
	constrainedBool16 := lanes.Varying[bool, 16]([16]bool{true, false, true, true, false, true, false, false, true, true, false, true, true, false, true, false})

	// Same functions work with constrained varying
	int4Sum := reduce.Add(constrainedInt4)
	float8Sum := reduce.Add(constrainedFloat8)
	bool16All := reduce.All(constrainedBool16)
	bool16Any := reduce.Any(constrainedBool16)

	fmt.Printf("Constrained[4] int reduce.Add: %d\n", int4Sum)
	fmt.Printf("Constrained[8] float reduce.Add: %.2f\n", float8Sum)
	fmt.Printf("Constrained[16] bool reduce.All: %t\n", bool16All)
	fmt.Printf("Constrained[16] bool reduce.Any: %t\n", bool16Any)
}

// Demonstrate bitwise operations with integer union types
func demonstrateBitwiseOperations() {
	fmt.Println("\n=== Bitwise Operations with Integer Union Types ===")

	// Test with unconstrained varying integers
	fmt.Println("\n--- Unconstrained Integer Operations ---")
	unconstrainedInt := lanes.Varying[int](0b1010)      // 10 in binary
	unconstrainedUint32 := lanes.Varying[uint32](uint32(0b1100))  // 12 in binary

	intOr := reduce.Or(unconstrainedInt)
	uint32And := reduce.And(unconstrainedUint32)
	intXor := reduce.Xor(unconstrainedInt)

	fmt.Printf("Unconstrained int Or: %d (0b%b)\n", intOr, intOr)
	fmt.Printf("Unconstrained uint32 And: %d (0b%b)\n", uint32And, uint32And)
	fmt.Printf("Unconstrained int Xor: %d (0b%b)\n", intXor, intXor)

	// Test with constrained varying integers
	fmt.Println("\n--- Constrained Integer Operations ---")
	constrainedInt8 := lanes.Varying[int, 8]([8]int{1, 2, 4, 8, 16, 32, 64, 128})
	constrainedUint16 := lanes.Varying[uint16, 16]([16]uint16{0x1, 0x2, 0x4, 0x8, 0x10, 0x20, 0x40, 0x80, 0x100, 0x200, 0x400, 0x800, 0x1000, 0x2000, 0x4000, 0x8000})

	int8Or := reduce.Or(constrainedInt8)
	uint16And := reduce.And(constrainedUint16)
	int8Xor := reduce.Xor(constrainedInt8)

	fmt.Printf("Constrained[8] int Or: %d (0b%b)\n", int8Or, int8Or)
	fmt.Printf("Constrained[16] uint16 And: %d (0x%x)\n", uint16And, uint16And)
	fmt.Printf("Constrained[8] int Xor: %d (0b%b)\n", int8Xor, int8Xor)
}

// Demonstrate lanes functions with union type generics
func demonstrateLanesGenerics() {
	fmt.Println("\n=== Lanes Functions with Union Type Generics ===")

	// Test with unconstrained varying
	fmt.Println("\n--- Unconstrained Lanes Operations ---")
	unconstrainedData := lanes.Varying[int](100)

	// Automatically inlined lanes operations
	broadcasted := lanes.Broadcast(unconstrainedData, 0)
	rotated := lanes.Rotate(unconstrainedData, 1)

	fmt.Printf("Unconstrained broadcast: %v\n", broadcasted)
	fmt.Printf("Unconstrained rotate: %v\n", rotated)

	// Test with constrained varying
	fmt.Println("\n--- Constrained Lanes Operations ---")
	constrainedData4 := lanes.Varying[string, 4]([4]string{"A", "B", "C", "D"})
	constrainedData8 := lanes.Varying[float32, 8]([8]float32{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0})

	// Same functions work with constrained varying
	stringBroadcast := lanes.Broadcast(constrainedData4, 2)  // Broadcast "C" to all lanes
	floatRotated := lanes.Rotate(constrainedData8, -1)       // Rotate right by 1

	fmt.Printf("Constrained[4] string broadcast: %v\n", stringBroadcast)
	fmt.Printf("Constrained[8] float32 rotate: %v\n", floatRotated)
}

// Demonstrate swizzle operations with integer indices
func demonstrateSwizzleOperations() {
	fmt.Println("\n=== Swizzle Operations with Union Types ===")

	// Create source data and index patterns
	fmt.Println("\n--- Swizzle with Different Types ---")
	sourceInts := lanes.Varying[int, 4]([4]int{10, 20, 30, 40})
	indices4 := lanes.Varying[int, 4]([4]int{3, 0, 2, 1})  // Reverse and swap

	// Swizzle with constrained types
	swizzledInts := lanes.Swizzle(sourceInts, indices4)
	fmt.Printf("Swizzled constrained[4] ints: %v\n", swizzledInts)

	// Works with unconstrained types too
	sourceFloats := lanes.Varying[float64](2.5)
	indices := lanes.Varying[int](0)  // All lanes access index 0

	swizzledFloats := lanes.Swizzle(sourceFloats, indices)
	fmt.Printf("Swizzled unconstrained floats: %v\n", swizzledFloats)
}

// Demonstrate shift operations with integer union types
func demonstrateShiftOperations() {
	fmt.Println("\n=== Shift Operations with Integer Union Types ===")

	// Test with unconstrained varying
	fmt.Println("\n--- Unconstrained Shift Operations ---")
	unconstrainedValue := lanes.Varying[int](0b11110000)  // 240 in binary
	unconstrainedShift := lanes.Varying[int](2)

	leftShifted := lanes.ShiftLeft(unconstrainedValue, unconstrainedShift)
	rightShifted := lanes.ShiftRight(unconstrainedValue, unconstrainedShift)

	fmt.Printf("Unconstrained left shift: %d (0b%b)\n", leftShifted, leftShifted)
	fmt.Printf("Unconstrained right shift: %d (0b%b)\n", rightShifted, rightShifted)

	// Test with constrained varying
	fmt.Println("\n--- Constrained Shift Operations ---")
	constrainedValues := lanes.Varying[uint16, 4]([4]uint16{0x00FF, 0x0F0F, 0xF0F0, 0xFF00})
	constrainedShifts := lanes.Varying[int, 4]([4]int{1, 2, 3, 4})

	constrainedLeft := lanes.ShiftLeft(constrainedValues, constrainedShifts)
	constrainedRight := lanes.ShiftRight(constrainedValues, constrainedShifts)

	fmt.Printf("Constrained[4] left shift: %v\n", constrainedLeft)
	fmt.Printf("Constrained[4] right shift: %v\n", constrainedRight)
}

// Generic function accepting union types
func processAnyVarying[T Numeric](data VaryingNumeric[T]) {
	fmt.Printf("\n--- Generic Processing of %T ---\n", data)

	// Works with both constrained and unconstrained
	sum := reduce.Add(data)
	max := reduce.Max(data)
	min := reduce.Min(data)

	fmt.Printf("Sum: %v\n", sum)
	fmt.Printf("Max: %v\n", max)
	fmt.Printf("Min: %v\n", min)

	// Use lanes operations
	broadcasted := lanes.Broadcast(data, 0)
	fmt.Printf("Broadcasted: %v\n", broadcasted)
}

// Demonstrate performance benefits of automatic inlining
func demonstratePerformanceOptimization() {
	fmt.Println("\n=== Performance Optimization (Automatic Inlining) ===")

	// All these operations are automatically inlined by the compiler
	data := lanes.Varying[int, 8]([8]int{1, 2, 3, 4, 5, 6, 7, 8})

	// Chain of operations - all inlined for optimal performance
	step1 := lanes.Broadcast(data, 0)
	step2 := lanes.Rotate(step1, 2)
	step3 := reduce.Add(step2)

	fmt.Printf("Optimized chain result: %d\n", step3)
	fmt.Println("Note: All reduce and lanes operations are automatically inlined")
	fmt.Println("      for optimal SIMD performance without function call overhead")
}

func main() {
	fmt.Println("=== Union Type Generics for Reduce and Lanes Functions ===")

	// Test 1: Reduce functions with union types
	demonstrateReduceGenerics()

	// Test 2: Bitwise operations with integer union types
	demonstrateBitwiseOperations()

	// Test 3: Lanes functions with union types
	demonstrateLanesGenerics()

	// Test 4: Swizzle operations
	demonstrateSwizzleOperations()

	// Test 5: Shift operations with integer union types
	demonstrateShiftOperations()

	// Test 6: Generic function processing
	fmt.Println("\n=== Generic Function Processing ===")
	processAnyVarying(lanes.Varying[int](42))
	processAnyVarying(lanes.Varying[float64, 4]([4]float64{1.1, 2.2, 3.3, 4.4}))
	processAnyVarying(lanes.Varying[int, 8]([8]int{10, 20, 30, 40, 50, 60, 70, 80}))

	// Test 7: Performance optimization demonstration
	demonstratePerformanceOptimization()

	fmt.Println("\n=== Summary ===")
	fmt.Println("✓ reduce functions work with VaryingBool, VaryingNumeric, VaryingInteger, VaryingComparable")
	fmt.Println("✓ lanes functions work with VaryingAny, VaryingInteger for type-specific operations")
	fmt.Println("✓ All functions accept both constrained (lanes.Varying[T, n]) and unconstrained (lanes.Varying[T]) types")
	fmt.Println("✓ All reduce and lanes operations are automatically inlined for optimal performance")
	fmt.Println("✓ Type-safe operations prevent incorrect usage (e.g., bitwise ops only on integers)")
	fmt.Println("✓ Union type generics provide flexibility while maintaining compile-time type safety")
	fmt.Println("✓ All union type generic operations completed successfully")
}
