// ILLEGAL: Explicit cast from varying to its own uniform element type
// int(Varying[int]) silently strips the varying qualifier without reducing
// across lanes. Use reduce.From() to extract elements instead.
// Note: cross-type conversions like int32(Varying[int]) are valid and produce
// Varying[int32] in the backend.
package main

import "lanes"

func main() {
	go for i := range 4 {
		// ILLEGAL: int(Varying[int]) — same element type, strips varying qualifier
		_ = int(i)

		// ILLEGAL: float32(Varying[float32]) — same element type
		var vf lanes.Varying[float32]
		_ = float32(vf)
	}
}
