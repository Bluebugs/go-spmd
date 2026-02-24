// Store coalescing: matching stores in both branches of varying if/else
// are merged into select(cond, thenVal, elseVal) + single store.
package main

import (
	"fmt"
)

func main() {
	dst := make([]byte, 32)

	// Test: fill even-indexed bytes with 0xAA, odd with 0xBB.
	// Both branches store to dst[i] — a store coalescing candidate.
	Fill(dst)
	fmt.Printf("Result: %v\n", dst)

	// Verify expected values.
	ok := true
	for i := range dst {
		var expect byte
		if i%2 == 0 {
			expect = 0xAA
		} else {
			expect = 0xBB
		}
		if dst[i] != expect {
			fmt.Printf("MISMATCH at %d: got %d, want %d\n", i, dst[i], expect)
			ok = false
		}
	}
	if ok {
		fmt.Println("PASS")
	}
}

// Fill stores different constant bytes depending on even/odd index.
// Both branches store to dst[i] — the store coalescing optimization
// merges them into select(cond, thenVal, elseVal) + single store.
func Fill(dst []byte) {
	go for i := range dst {
		if i%2 == 0 {
			dst[i] = 0xAA
		} else {
			dst[i] = 0xBB
		}
	}
}
