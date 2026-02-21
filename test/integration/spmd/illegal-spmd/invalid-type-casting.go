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

	// ILLEGAL: Mixed type upcasting in expression
	var a lanes.Varying[uint16] = lanes.Varying[uint16](100)
	var b lanes.Varying[uint32] = lanes.Varying[uint32](200)
	var result lanes.Varying[uint32] = a + b  // ERROR: cannot mix uint16 and uint32 without explicit cast

	// ILLEGAL: Attempting to upcast through assignment
	var sourceSmall lanes.Varying[int16] = lanes.Varying[int16](1000)
	var destLarge lanes.Varying[int32]
	destLarge = sourceSmall  // ERROR: implicit upcasting not allowed

	// Use variables to avoid unused variable errors
	_, _, _, _, _, _ = large32, wide64, double, result, destLarge, b
}
