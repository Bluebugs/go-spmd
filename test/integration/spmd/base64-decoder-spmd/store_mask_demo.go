// SPMD Store Masking Test
// Tests that inactive SIMD lanes do not write garbage when N < SIMD width
package main

import (
	"fmt"
	"os"
)

// TestSPMDStoreMasking tests that inactive SIMD lanes in SPMD loops
// do not write garbage values to output arrays when iteration count < SIMD width.
//
// This test reproduces the bug where regular scalar stores inside SPMD `go for`
// loops are not masked, causing out-of-bounds writes when N < SIMD width.
//
// Expected behavior: Only active lanes (0 to N-1) should write values.
// Bug behavior: Inactive lanes (N to SIMD_WIDTH-1) write garbage.
func testSPMDStoreMasking() bool {
	// Test with 2 iterations but SIMD width is 4 (typical for i32 on WASM128)
	dst := [16]byte{}
	input := [8]byte{'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H'}
	
	// Fill dst with sentinel values to detect unwanted writes
	for i := range dst {
		dst[i] = 0xAA // Sentinel value (unlikely garbage)
	}
	
	// SPMD loop with 2 iterations (less than typical SIMD width of 4)
	go for g := range 2 {
		// Each iteration writes 3 bytes
		// g=0: dst[0,1,2] = input[0,1,2] = A,B,C  
		// g=1: dst[3,4,5] = input[4,5,6] = E,F,G
		// Inactive lanes (g=2,3) should NOT write dst[6,7,8,9,10,11]
		dst[g*3+0] = input[g*4+0]
		dst[g*3+1] = input[g*4+1]  
		dst[g*3+2] = input[g*4+2]
	}
	
	// Verify active lanes wrote correct values
	expected := [6]byte{'A', 'B', 'C', 'E', 'F', 'G'}
	for i := 0; i < 6; i++ {
		if dst[i] != expected[i] {
			fmt.Printf("FAIL: dst[%d] = %c (0x%02x), want %c (0x%02x)\n", 
				i, dst[i], dst[i], expected[i], expected[i])
			return false
		}
	}
	
	// Critical test: positions 6-15 should remain untouched (0xAA sentinel)
	// If inactive lanes write garbage, this will fail
	for i := 6; i < 16; i++ {
		if dst[i] != 0xAA {
			fmt.Printf("FAIL: dst[%d] = 0x%02x, want 0xAA (inactive lane should not write)\n", 
				i, dst[i])
			
			// Additional debugging info
			fmt.Printf("Full dst array: %x\n", dst[:])
			fmt.Printf("This indicates inactive SIMD lanes are writing garbage\n")
			fmt.Printf("Expected: only positions 0-5 should be modified\n")
			return false
		}
	}
	
	return true
}

// testSPMDStoreMaskingLargerGap tests with an even smaller iteration count
// to make the masking bug more obvious (1 iteration vs 4-wide SIMD)
func testSPMDStoreMaskingLargerGap() bool {
	dst := [20]byte{}
	input := [8]byte{'X', 'Y', 'Z', 'W', 'P', 'Q', 'R', 'S'}
	
	// Fill with sentinel pattern
	for i := range dst {
		dst[i] = 0xBB
	}
	
	// Only 1 iteration - 3 out of 4 SIMD lanes should be inactive
	go for g := range 1 {
		dst[g*3+0] = input[g*4+0] // Should write X to dst[0]
		dst[g*3+1] = input[g*4+1] // Should write Y to dst[1]
		dst[g*3+2] = input[g*4+2] // Should write Z to dst[2]
	}
	
	// Only dst[0,1,2] should be modified
	if dst[0] != 'X' || dst[1] != 'Y' || dst[2] != 'Z' {
		fmt.Printf("FAIL: Active lane failed: dst[0:3] = [%c,%c,%c], want [X,Y,Z]\n",
			dst[0], dst[1], dst[2])
		return false
	}
	
	// Everything else should be untouched
	for i := 3; i < 20; i++ {
		if dst[i] != 0xBB {
			fmt.Printf("FAIL: dst[%d] = 0x%02x, want 0xBB (inactive lanes wrote garbage)\n", 
				i, dst[i])
			fmt.Printf("Full dst array: %x\n", dst[:])
			return false
		}
	}
	
	return true
}

// testSPMDStoreMaskingMemoryCorruption verifies that the store masking bug
// can cause memory corruption by writing to unintended locations
func testSPMDStoreMaskingMemoryCorruption() bool {
	// Create a carefully arranged memory layout to detect corruption
	guard1 := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	target := [12]byte{} // 3 iterations * 3 bytes = 9 bytes needed, 3 extra
	guard2 := [4]byte{0xCA, 0xFE, 0xBA, 0xBE}
	
	// Fill target with pattern
	for i := range target {
		target[i] = 0xCC
	}
	
	input := [16]byte{'1', '2', '3', '4', '5', '6', '7', '8', '9', 'A', 'B', 'C', 'D', 'E', 'F', '0'}
	
	// 3 iterations should write exactly 9 bytes (target[0:9])
	// Inactive lanes should not touch target[9:12]
	go for g := range 3 {
		target[g*3+0] = input[g*4+0]
		target[g*3+1] = input[g*4+1]
		target[g*3+2] = input[g*4+2]
	}
	
	// Verify guards are intact (detects severe memory corruption)
	if guard1 != [4]byte{0xDE, 0xAD, 0xBE, 0xEF} {
		fmt.Printf("FATAL: Guard1 corrupted: %x\n", guard1)
		return false
	}
	if guard2 != [4]byte{0xCA, 0xFE, 0xBA, 0xBE} {
		fmt.Printf("FATAL: Guard2 corrupted: %x\n", guard2)
		return false  
	}
	
	// Verify expected writes
	expected := []byte{'1', '2', '3', '5', '6', '7', '9', 'A', 'B'}
	for i := 0; i < 9; i++ {
		if target[i] != expected[i] {
			fmt.Printf("FAIL: target[%d] = %c, want %c\n", i, target[i], expected[i])
			return false
		}
	}
	
	// Verify inactive lane positions remain untouched  
	for i := 9; i < 12; i++ {
		if target[i] != 0xCC {
			fmt.Printf("FAIL: target[%d] = 0x%02x, want 0xCC (inactive lane corruption)\n", 
				i, target[i])
			
			// Print memory layout for debugging (without unsafe)
			fmt.Printf("Memory layout:\n")
			fmt.Printf("guard1: %x\n", guard1)
			fmt.Printf("target: %x\n", target)
			fmt.Printf("guard2: %x\n", guard2)
			return false
		}
	}
	
	return true
}

// testSPMDStoreMaskingZeroIterations tests edge case of zero iterations
// All SIMD lanes should be inactive
func testSPMDStoreMaskingZeroIterations() bool {
	dst := [16]byte{}
	
	// Fill with sentinel  
	for i := range dst {
		dst[i] = 0xDD
	}
	
	// Zero iterations - no lanes should be active
	go for g := range 0 {
		dst[g] = 0x42 // Should never execute
	}
	
	// Nothing should be written
	for i := 0; i < 16; i++ {
		if dst[i] != 0xDD {
			fmt.Printf("FAIL: dst[%d] = 0x%02x, want 0xDD (zero iterations should write nothing)\n", 
				i, dst[i])
			return false
		}
	}
	
	return true
}

func main() {
	fmt.Println("Testing SPMD Store Masking Behavior")
	fmt.Println("===================================")
	
	allPassed := true
	
	fmt.Print("Test 1: Basic store masking (2 iterations, 4-wide SIMD)... ")
	if testSPMDStoreMasking() {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
		allPassed = false
	}
	
	fmt.Print("Test 2: Larger gap masking (1 iteration, 4-wide SIMD)... ")
	if testSPMDStoreMaskingLargerGap() {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
		allPassed = false
	}
	
	fmt.Print("Test 3: Memory corruption detection (3 iterations)... ")
	if testSPMDStoreMaskingMemoryCorruption() {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
		allPassed = false
	}
	
	fmt.Print("Test 4: Zero iterations edge case... ")
	if testSPMDStoreMaskingZeroIterations() {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
		allPassed = false
	}
	
	fmt.Println()
	if allPassed {
		fmt.Println("All tests PASSED - No store masking bug detected")
		os.Exit(0)
	} else {
		fmt.Println("Some tests FAILED - Store masking bug confirmed")
		os.Exit(1)
	}
}