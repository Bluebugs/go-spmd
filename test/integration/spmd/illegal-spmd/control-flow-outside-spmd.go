// ILLEGAL: Control flow operations with varying values outside SPMD context
// Expected errors: Various "varying control flow outside SPMD context" errors
package main

import "lanes"

func main() {
	var data varying int = varying(42)
	var values varying[4] int = varying[4]([4]int{10, 20, 30, 40})
	
	// ILLEGAL: if statement with varying condition outside SPMD context
	if data > varying(30) {  // ERROR: varying condition outside SPMD context
		// This would be confusing - what does this mean without SPMD context?
	}
	
	// ILLEGAL: for loop with varying condition outside SPMD context  
	for data != varying(0) {  // ERROR: varying loop condition outside SPMD context
		data = data - varying(1)
	}
	
	// ILLEGAL: switch statement with varying expression outside SPMD context
	switch data {  // ERROR: varying switch expression outside SPMD context
	case varying(42):
		// Handle case
	default:
		// Handle default
	}
	
	// ILLEGAL: range over varying value outside SPMD context
	for i, v := range values {  // ERROR: varying range outside SPMD context
		_ = i
		_ = v
	}
	
	// ILLEGAL: while-style loop with varying condition
	for values[0] > varying(5) {  // ERROR: varying condition outside SPMD context
		values = values - varying(1)
	}
	
	// ILLEGAL: Complex varying expressions in control flow
	if reduce.Any(data > varying(25)) {  // This is actually OK - reduce.Any returns uniform bool
		// This part is fine
		if data > varying(25) {  // ERROR: but this varying condition is still illegal outside SPMD
			// Illegal nested varying condition
		}
	}
	
	// Show what should be used instead - explicit SPMD context
	go for i := range 1 {  // Single iteration for demonstration
		// All of the above operations would be LEGAL inside this go for:
		if data > varying(30) {     // OK: varying condition in SPMD context
			// Clear SPMD intent
		}
		
		switch data {               // OK: varying switch in SPMD context  
		case varying(42):
			// Handle case in SPMD context
		}
		
		// Range over varying arrays is legal in SPMD context
		for idx, val := range values {  // OK: SPMD context makes intent clear
			_ = idx  // varying in SPMD context
			_ = val  // uniform in SPMD context
		}
	}
	
	// Use data to avoid unused variable errors
	_ = data
	_ = values
}