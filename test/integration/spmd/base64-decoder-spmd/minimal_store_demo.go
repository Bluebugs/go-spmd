// Minimal SPMD Store Masking Test
package main

import "fmt"

func main() {
	dst := [8]byte{}
	src := [8]byte{10, 11, 12, 13, 14, 15, 16, 17}
	
	// Fill dst with sentinel
	for i := range dst {
		dst[i] = 0xFF
	}
	
	// 2 iterations on 4-wide SIMD - should only write dst[0], dst[1]
	go for g := range 2 {
		dst[g] = src[g] // Simple copy, no type conversion
	}
	
	// Check results
	fmt.Printf("dst: %v\n", dst[:])
	
	// Verify correctness
	if dst[0] != 10 || dst[1] != 11 {
		fmt.Printf("FAIL: Active lanes incorrect: dst[0]=%d, dst[1]=%d\n", dst[0], dst[1])
		return
	}
	
	// Check inactive lanes didn't write
	for i := 2; i < 8; i++ {
		if dst[i] != 0xFF {
			fmt.Printf("FAIL: dst[%d] = %d, inactive lane wrote garbage\n", i, dst[i])
			return
		}
	}
	
	fmt.Println("PASS: Store masking works correctly")
}