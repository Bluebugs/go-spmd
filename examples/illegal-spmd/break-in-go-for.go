// ILLEGAL: Break/return statements under varying conditions in SPMD go for loops
// Following ISPC approach: forbidden only under varying conditions
// Expected error: "break/return statement not allowed under varying conditions in SPMD for loop"
package main

import "reduce"

func main() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	// ILLEGAL: Break under varying condition
	go for i := range data {
		if data[i] > 5 { // varying condition
			break  // ERROR: varying condition forbids break
		}
		data[i] *= 2
	}

	// ILLEGAL: Return under varying condition
	go for i := range data {
		if data[i] < 0 { // varying condition
			return  // ERROR: varying condition forbids return
		}
		data[i] *= 2
	}

	// ILLEGAL: Break in nested varying condition
	go for i := range data {
		switch data[i] { // varying condition
		case 1:
			data[i] = 10
		case 2:
			break  // ERROR: varying condition (switch on varying) forbids break
		default:
			data[i] += 1
		}
	}

	// ILLEGAL: Nested varying conditions make return/break forbidden
	go for i := range data {
		uniformCondition := true
		if uniformCondition { // uniform condition - return/break would be OK here
			if data[i] > 5 { // varying condition - now return/break forbidden
				return  // ERROR: enclosing varying condition forbids return
			}
		}
	}
}

// ILLEGAL: Break with varying condition in go for
func conditionalBreak() {
	data := make([]int, 100)
	
	go for i := range data {
		// Even with reduction producing uniform result, the condition is varying
		var condition varying bool = (data[i] > 50)
		
		if condition { // varying condition
			if reduce.Any(condition) { // uniform result, but in varying context
				break  // ERROR: still considered under varying context
			}
		}
		if reduce.All(!condition) {
			// All lanes false - uniform context - loop would exit here, but this is OK
			return  // LEGAL: all lanes return is OK
		}

		data[i] = process(data[i])
	}
}

func process(x int) int {
	return x * 2
}

// LEGAL: Continue statements are always allowed (for comparison)
func legalContinue() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	go for i := range data {
		if data[i]%2 == 0 { // varying condition
			continue  // LEGAL: continue always allowed, even under varying conditions
		}
		data[i] *= 3
	}
}

// LEGAL: Return/break under uniform conditions
func legalUniformExit() {
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	threshold := uniform int(15)
	
	go for i := range data {
		// LEGAL: Uniform condition allows return/break
		if threshold < 0 {
			return  // LEGAL: uniform condition
		}
		
		if threshold > 20 {
			break  // LEGAL: uniform condition
		}
		
		data[i] *= 2
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