package main

import "fmt"

func main() {
	// Test 1: byte→int16 packing (maps to pmaddubsw on x86 with SSSE3)
	// packed[i] = src[2i]*64 + src[2i+1]*1
	src := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	packed := make([]int16, 8)
	go for i, _ := range packed {
		packed[i] = int16(src[i*2])*64 + int16(src[i*2+1])
	}

	ok := true
	for i := 0; i < 8; i++ {
		expected := int16(src[i*2])*64 + int16(src[i*2+1])
		if packed[i] != expected {
			fmt.Printf("FAIL packed[%d]: got %d, want %d\n", i, packed[i], expected)
			ok = false
		}
	}

	// Test 2: int16→int32 packing (maps to pmaddwd on x86)
	// packed32[i] = src16[2i]*4096 + src16[2i+1]*1
	src16 := []int16{100, 200, 300, 400, 500, 600, 700, 800}
	packed32 := make([]int32, 4)
	go for i, _ := range packed32 {
		packed32[i] = int32(src16[i*2])*4096 + int32(src16[i*2+1])
	}

	for i := 0; i < 4; i++ {
		expected := int32(src16[i*2])*4096 + int32(src16[i*2+1])
		if packed32[i] != expected {
			fmt.Printf("FAIL packed32[%d]: got %d, want %d\n", i, packed32[i], expected)
			ok = false
		}
	}

	if ok {
		fmt.Println("Correctness: PASS")
	} else {
		fmt.Println("Correctness: FAIL")
	}
}
