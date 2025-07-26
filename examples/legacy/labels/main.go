// Legacy compatibility test: using 'uniform' and 'varying' as labels
// This ensures existing code continues to work with SPMD extensions
package main

import "fmt"

func main() {
	fmt.Println("Testing labels named 'uniform' and 'varying'")
	
	// Test goto with labels named 'uniform' and 'varying'
	testGotoLabels()
	
	// Test break/continue with labeled loops
	testLabeledLoops()
}

func testGotoLabels() {
	fmt.Println("Testing goto labels:")
	
	x := 1
	if x == 1 {
		goto uniform
	}
	
	fmt.Println("This should be skipped")
	
uniform:
	fmt.Println("Reached uniform label")
	
	y := 2
	if y == 2 {
		goto varying
	}
	
	fmt.Println("This should also be skipped")
	
varying:
	fmt.Println("Reached varying label")
}

func testLabeledLoops() {
	fmt.Println("Testing labeled loops:")
	
uniform:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if i == 1 && j == 1 {
				fmt.Printf("Breaking uniform loop at i=%d, j=%d\n", i, j)
				break uniform
			}
			fmt.Printf("uniform: i=%d, j=%d\n", i, j)
		}
	}
	
	fmt.Println("After uniform labeled loop")
	
varying:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if i == 1 && j == 1 {
				fmt.Printf("Continuing varying loop at i=%d, j=%d\n", i, j)
				continue varying
			}
			if i == 2 {
				fmt.Printf("Breaking varying loop at i=%d, j=%d\n", i, j)
				break varying
			}
			fmt.Printf("varying: i=%d, j=%d\n", i, j)
		}
	}
	
	fmt.Println("After varying labeled loop")
}