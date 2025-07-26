// ILLEGAL: Invalid type casting operations with varying types
// Expected errors: Various upcasting and invalid casting errors
package main

func main() {
	// ILLEGAL: Upcasting uint16 to uint32 (exceeds register capacity)
	var small16 varying uint16 = varying(0x1234)
	var large32 varying uint32 = varying uint32(small16)  // ERROR: upcasting not allowed
	
	// ILLEGAL: Upcasting int32 to int64 (exceeds register capacity)
	var narrow32 varying int32 = varying(42)
	var wide64 varying int64 = varying int64(narrow32)    // ERROR: upcasting not allowed
	
	// ILLEGAL: Upcasting float32 to float64 (exceeds register capacity)
	var single varying float32 = varying(3.14)
	var double varying float64 = varying float64(single)  // ERROR: upcasting not allowed
	
	// ILLEGAL: Upcasting with constrained varying
	var constrainedSmall varying[8] uint16 = varying[8]([8]uint16{1, 2, 3, 4, 5, 6, 7, 8})
	var constrainedLarge varying[8] uint32 = varying[8] uint32(constrainedSmall)  // ERROR: would need 8×32=256 bits > 128-bit limit
	
	// ILLEGAL: Upcasting that would require multiple registers
	var bytes varying[16] uint8 = varying[16]([16]uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	var shorts varying[16] uint16 = varying[16] uint16(bytes)  // ERROR: would need 16×16=256 bits > 128-bit limit
	
	// ILLEGAL: Cross-constraint upcasting
	var small4 varying[4] uint16 = varying[4]([4]uint16{1, 2, 3, 4})
	var large4 varying[4] uint64 = varying[4] uint64(small4)  // ERROR: 4×64=256 bits > 128-bit limit
	
	// ILLEGAL: Mixed type upcasting in expression
	var a varying uint16 = varying(100)
	var b varying uint32 = varying(200)
	var result varying uint32 = a + b  // ERROR: cannot mix uint16 and uint32 without explicit cast
	
	// ILLEGAL: Attempting to upcast through assignment
	var sourceSmall varying int16 = varying(1000)
	var destLarge varying int32
	destLarge = sourceSmall  // ERROR: implicit upcasting not allowed
	
	// Use variables to avoid unused variable errors
	_, _, _, _, _, _, _, _, _ = large32, wide64, double, constrainedLarge, shorts, large4, result, destLarge, b
}