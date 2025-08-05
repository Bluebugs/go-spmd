// Package main demonstrates uniform early return patterns in SPMD go for loops
// Following ISPC's approach: return/break allowed under uniform conditions only
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

// Constants for processing modes
const (
	DEBUG_MODE = 1
	TRACE_MODE = 2
	ABORT_MODE = 3
)

func main() {
	fmt.Printf("=== Uniform Early Return Examples ===\n")
	
	data := []int{1, -2, 3, 4, -5, 6, 7, 8}
	
	// Example 1: Uniform early return for error conditions
	fmt.Println("\n1. Uniform early return for error conditions:")
	if err := processWithUniformEarlyReturn(data, 10); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
	
	// Example 2: Mixed uniform/varying conditions
	fmt.Println("\n2. Mixed uniform/varying conditions:")
	processWithMixedConditions(data, DEBUG_MODE)
	
	// Example 3: Performance comparison
	fmt.Println("\n3. Performance comparison:")
	demonstratePerformance(data, 5)
	
	// Example 4: Structured approach with uniform guards
	fmt.Println("\n4. Structured approach with uniform guards:")
	params := ProcessParams{
		AbortRequested: false,
		MaxIterations:  4,
		Mode:          DEBUG_MODE,
	}
	
	if err := processWithStructuredExits(data, params); err != nil {
		fmt.Printf("Processing error: %v\n", err)
	}
	
	// Example 5: Mask alteration scenarios  
	fmt.Println("\n5. Mask alteration scenarios:")
	demonstrateMaskAlteration(data, DEBUG_MODE)
}

// Example 1: Uniform early return for error conditions
func processWithUniformEarlyReturn(data []int, threshold uniform int) error {
	go for i := range len(data) {
		// ALLOWED: Uniform condition - all lanes make same decision
		if threshold < 0 {
			return fmt.Errorf("invalid threshold: %d", threshold)
		}
		
		// ALLOWED: Uniform function call condition
		if isShutdownRequested() { // uniform function
			return fmt.Errorf("shutdown requested")
		}
		
		// Process data - varying operations
		if data[i] > threshold {
			data[i] = process(data[i])
		}
	}
	
	return nil
}

// Example 2: Mixed uniform/varying conditions 
func processWithMixedConditions(data []int, mode uniform int) {
	go for i := range len(data) {
		// Uniform outer condition - return/break allowed here
		if mode == DEBUG_MODE {
			fmt.Printf("Debug mode: processing index %d\n", i)
			
			// FORBIDDEN: Inner varying condition makes return/break illegal
			if data[i] > 100 { // varying condition
				// return  // COMPILE ERROR: enclosing varying condition
				fmt.Printf("Large value %d at index %d, continuing\n", data[i], i)
				continue  // ALLOWED: continue always permitted
			}
			
			// ALLOWED: Still under uniform condition only
			if mode == TRACE_MODE {
				fmt.Println("Trace mode activated, returning early")
				return  // OK: no varying conditions in scope
			}
		}
		
		data[i] = process(data[i])
		
		// ALLOWED: Direct uniform condition at loop level
		if mode == ABORT_MODE {
			fmt.Println("Abort mode activated, breaking")
			break  // OK: uniform condition allows direct break
		}
	}
}

// Example 3: Performance comparison - uniform vs varying exits
func demonstratePerformance(data []int, uniformThreshold uniform int) {
	fmt.Println("Fast path: uniform early exit")
	
	// FAST: Uniform early exit - entire SIMD loop can terminate efficiently
	go for i := range len(data) {
		if uniformThreshold < 0 {
			fmt.Println("All lanes exit together - no mask tracking needed")
			return  // All lanes exit together
		}
		
		data[i] = process(data[i])
	}
	
	fmt.Println("Slow path: varying conditions require mask tracking")
	
	// SLOWER: Varying conditions require mask tracking throughout loop
	go for i := range len(data) {
		if data[i] < 0 { // varying condition - requires per-lane mask tracking
			fmt.Printf("Skipping negative value %d at index %d\n", data[i], i)
			continue // Must track which lanes continue vs process
		}
		
		data[i] = process(data[i]) // Executed with mask for active lanes only
		
		// FORBIDDEN: Can't use early exit under varying condition
		// if data[i] > 1000 {
		//     return  // COMPILE ERROR: varying condition forbids return
		// }
	}
}

// Example 4: Correct structured approach with uniform guards
func processWithStructuredExits(data []int, params uniform ProcessParams) error {
	go for i := range len(data) {
		// Check uniform error conditions first - can exit immediately
		if params.AbortRequested {
			return fmt.Errorf("processing aborted")  // ALLOWED: uniform condition
		}
		
		if params.MaxIterations > 0 && i >= params.MaxIterations {
			fmt.Printf("Reached max iterations %d, breaking\n", params.MaxIterations)
			break  // ALLOWED: uniform condition
		}
		
		// Handle varying conditions without early exit
		if data[i] < 0 { // varying condition
			fmt.Printf("Error at index %d: negative value %d\n", i, data[i])
			continue  // ALLOWED: continue to next iteration
		}
		
		// Process valid data
		result := process(data[i])
		if result < 0 {
			// Can't return here due to varying result - handle differently
			fmt.Printf("Processing error at index %d: result %d\n", i, result)
			continue
		}
		
		data[i] = result
	}
	
	return nil
}

// Helper types and functions
type ProcessParams struct {
	AbortRequested  uniform bool
	MaxIterations   uniform int
	Mode           uniform int
}

func isShutdownRequested() uniform bool {
	// Simulate uniform shutdown check
	return false
}

func process(x int) int {
	if x < 0 {
		return -1  // Simulate processing error
	}
	return x * 2
}

// Example 5: Mask alteration scenarios - continue in varying context affects subsequent uniform conditions
func demonstrateMaskAlteration(data []int, mode uniform int) {
	fmt.Println("FORBIDDEN: Return/break after continue in varying context")
	
	// This function demonstrates why return/break is forbidden after mask alteration
	go for i := range len(data) {
		if data[i] < 0 { // varying condition
			fmt.Printf("Negative value %d at index %d, continuing\n", data[i], i)
			continue  // OK: continue always allowed, but alters execution mask
		}
		
		// At this point, the execution mask has been altered by the continue above
		// Some lanes may have exited early, so uniform conditions no longer affect all lanes
		if mode == DEBUG_MODE { // uniform condition, but mask is altered
			fmt.Println("Debug mode: would like to return here")
			// return  // COMPILE ERROR: return forbidden due to mask alteration
			// break   // COMPILE ERROR: break forbidden due to mask alteration
			fmt.Println("Must use continue instead or restructure logic")
			continue  // OK: continue always allowed
		}
		
		data[i] = process(data[i])
	}
	
	fmt.Println("ALLOWED: No mask alteration - continue in uniform context")
	
	// This shows the distinction: continue in uniform context doesn't alter mask
	go for i := range len(data) {
		if mode < 0 { // uniform condition
			fmt.Println("Uniform condition: continuing doesn't alter mask")
			continue  // OK: continue in uniform context doesn't alter mask
		}
		
		// ALLOWED: No mask alteration occurred
		if mode == TRACE_MODE { // uniform condition, no mask alteration
			fmt.Println("Trace mode: can return safely")
			return // OK: clean uniform context, no mask alteration
		}
		
		data[i] = process(data[i])
	}
	
	fmt.Println("WORKAROUND: Structured approach avoids mask alteration")
	
	// Better approach: structure code to avoid needing return/break after varying continue
	hasNegativeValues := false
	go for i := range len(data) {
		if data[i] < 0 { // varying condition
			hasNegativeValues = true
			continue  // Process positive values, remember we had negatives
		}
		
		data[i] = process(data[i])
	}
	
	// Handle uniform decisions outside the SPMD loop
	if hasNegativeValues && mode == DEBUG_MODE {
		fmt.Println("Debug mode: found negative values, handled outside loop")
		return // OK: uniform return outside SPMD loop
	}
}