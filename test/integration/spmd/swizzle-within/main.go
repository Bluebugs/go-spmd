package main

import (
	"fmt"
	"lanes"
)

// reverseWithin4 reverses elements within groups of 4.
// 3 - lanes.Index() produces LLVM constant <3,2,1,0> via constant folding.
func reverseWithin4(v lanes.Varying[int32]) lanes.Varying[int32] {
	idx := lanes.Varying[int32](3) - lanes.Varying[int32](lanes.Index())
	return lanes.SwizzleWithin(v, idx, 4)
}

// swapPairs swaps adjacent elements within groups of 2.
// 1 - (lanes.Index() & 1) produces LLVM constant <1,0,1,0>.
func swapPairs(v lanes.Varying[int32]) lanes.Varying[int32] {
	idx := lanes.Varying[int32](1) - (lanes.Varying[int32](lanes.Index()) & lanes.Varying[int32](1))
	return lanes.SwizzleWithin(v, idx, 2)
}

func main() {
	input := []int32{0, 1, 2, 3, 4, 5, 6, 7}
	output := make([]int32, 8)
	output2 := make([]int32, 8)

	// Test 1: Reverse within groups of 4
	go for i, v := range input {
		output[i] = reverseWithin4(v)
	}
	fmt.Printf("Reverse: %v\n", output)

	// Test 2: Swap adjacent pairs within groups of 2
	go for i, v := range input {
		output2[i] = swapPairs(v)
	}
	fmt.Printf("Swap pairs: %v\n", output2)

	// Verify
	expected1 := []int32{3, 2, 1, 0, 7, 6, 5, 4}
	expected2 := []int32{1, 0, 3, 2, 5, 4, 7, 6}
	ok := true
	for i := range 8 {
		if output[i] != expected1[i] {
			ok = false
		}
		if output2[i] != expected2[i] {
			ok = false
		}
	}
	if ok {
		fmt.Println("Correctness: PASS")
	} else {
		fmt.Println("Correctness: FAIL")
	}
}
