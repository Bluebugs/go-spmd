// ILLEGAL: Invalid type casting operations with varying types
// Expected errors: Various upcasting and invalid casting errors
package main

import "lanes"

func main() {
	// ILLEGAL: Upcasting uint16 to uint32 (exceeds register capacity)
	var small16 lanes.Varying[uint16] = lanes.Varying[uint16](0x1234)
	var large32 lanes.Varying[uint32] = lanes.Varying[uint32](small16)  // ERROR: upcasting not allowed

	// ILLEGAL: Upcasting int32 to int64 (exceeds register capacity)
	var narrow32 lanes.Varying[int32] = lanes.Varying[int32](42)
	var wide64 lanes.Varying[int64] = lanes.Varying[int64](narrow32)    // ERROR: upcasting not allowed

	// ILLEGAL: Upcasting float32 to float64 (exceeds register capacity)
	var single lanes.Varying[float32] = lanes.Varying[float32](3.14)
	var double lanes.Varying[float64] = lanes.Varying[float64](single)  // ERROR: upcasting not allowed

	// ILLEGAL: Upcasting with constrained varying
	var constrainedSmall lanes.Varying[uint16, 8] = lanes.From([]uint16{1, 2, 3, 4, 5, 6, 7, 8})
	var constrainedLarge lanes.Varying[uint32, 8] = lanes.Varying[uint32, 8](constrainedSmall)  // ERROR: would need 8×32=256 bits > 128-bit limit

	// ILLEGAL: Upcasting that would require multiple registers
	var bytes lanes.Varying[uint8, 16] = lanes.From([]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	var shorts lanes.Varying[uint16, 16] = lanes.Varying[uint16, 16](bytes)  // ERROR: would need 16×16=256 bits > 128-bit limit

	// ILLEGAL: Cross-constraint upcasting
	var small4 lanes.Varying[uint16, 4] = lanes.From([]uint16{1, 2, 3, 4})
	var large4 lanes.Varying[uint64, 4] = lanes.Varying[uint64, 4](small4)  // ERROR: 4×64=256 bits > 128-bit limit

	// ILLEGAL: Mixed type upcasting in expression
	var a lanes.Varying[uint16] = lanes.Varying[uint16](100)
	var b lanes.Varying[uint32] = lanes.Varying[uint32](200)
	var result lanes.Varying[uint32] = a + b  // ERROR: cannot mix uint16 and uint32 without explicit cast

	// ILLEGAL: Attempting to upcast through assignment
	var sourceSmall lanes.Varying[int16] = lanes.Varying[int16](1000)
	var destLarge lanes.Varying[int32]
	destLarge = sourceSmall  // ERROR: implicit upcasting not allowed

	// Use variables to avoid unused variable errors
	_, _, _, _, _, _, _, _, _ = large32, wide64, double, constrainedLarge, shorts, large4, result, destLarge, b
}
