// run -goexperiment spmd

// Regression test for fmt.Printf("%v", i) where i is the varying
// loop iterator inside a byte-element go for loop.
//
// Before fix: createMakeInterface used spmdEffectiveLaneCount (the
// native register-derived count, 4 for int32 on WASM128) instead of
// spmdType.Lanes() (the loop-fixed width, 16 in a byte loop). The
// data array was laid out at the correct 16-lane width, but the
// reflect typecode said 4 lanes, so fmt.printSPMDVarying iterated
// only the first 4 lanes.
//
// After fix: the typecode honors Lanes(), so fmt prints all 16
// (SIMD) or 1 (scalar) iterator lane values per chunk.
package main

import "fmt"

//go:noinline
func dump(dst []byte) {
	go for i := range dst {
		fmt.Printf("i=%v\n", i)
	}
}

func main() {
	dst := make([]byte, 16)
	dump(dst)
}
