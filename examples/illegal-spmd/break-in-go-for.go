// ILLEGAL: Break statements are not allowed in SPMD go for loops
// Expected error: "break statement not allowed in SPMD for loop"
package main

import "reduce"

func main() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	// ILLEGAL: Direct break in go for loop
	go for i := range data {
		if data[i] > 5 {
			break  // ERROR: break statement not allowed in SPMD for loop
		}
		data[i] *= 2
	}

	// ILLEGAL: Break in nested structure within go for
	go for i := range data {
		switch data[i] {
		case 1:
			data[i] = 10
		case 2:
			break  // ERROR: break statement not allowed in SPMD for loop
		default:
			data[i] += 1
		}
	}

	// ILLEGAL: Labeled break targeting go for loop
	outer:
	go for i := range data {
		for j := 0; j < 3; j++ {
			if data[i]*j > 10 {
				break outer  // ERROR: break statement not allowed in SPMD for loop
			}
		}
	}

	// ILLEGAL: Break in nested go for loop (nesting prohibited for now)
	go for i := range data {
		go for j := range 5 { // ERROR: nested `go for` loop (prohibited for now)
			if i*j > 10 {
				break  // ERROR: break statement not allowed in SPMD for loop
			}
		}
	}
}

// ILLEGAL: Break with condition in go for
func conditionalBreak() {
	data := make([]int, 100)
	
	go for i := range data {
		var condition varying bool = (data[i] > 50)
		
		// Even with reduction, break is not allowed
		if reduce.Any(condition) {
			break  // ERROR: break statement not allowed in SPMD for loop
		}
		
		data[i] = process(data[i])
	}
}

func process(x int) int {
	return x * 2
}

// LEGAL: Continue statements are allowed (for comparison)
func legalContinue() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	go for i := range data {
		if data[i]%2 == 0 {
			continue  // LEGAL: continue is allowed in go for loops
		}
		data[i] *= 3
	}
}

// LEGAL: Break in regular for loop inside go for is allowed
func legalInnerBreak() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	go for i := range data {
		// Regular for loop inside go for - break is allowed here
		for j := 0; j < 10; j++ {
			if data[i]+j > 15 {
				break  // LEGAL: break in regular for loop
			}
			data[i] += j
		}
	}
}