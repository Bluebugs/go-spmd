//go:build goexperiment.spmd

// Test cases for return/break restrictions in SPMD go for loops
// Following ISPC approach: return/break allowed under uniform conditions only

package main

import "lanes"

// Test 1: ALLOWED - Return/break under uniform conditions
func testAllowedUniformReturn(threshold uniform int) {
	go for i := range 10 {
		// ALLOWED: Uniform condition allows return/break
		if threshold < 0 {
			return // OK: uniform condition
		}
		
		if threshold > 1000 {
			break // OK: uniform condition
		}
	}
}

// Test 2: FORBIDDEN - Return/break under varying conditions
func testForbiddenVaryingReturn(data []int) {
	go for i := range len(data) {
		// FORBIDDEN: Varying condition forbids return/break
		if data[i] < 0 {
			return // SHOULD GENERATE ERROR: varying condition forbids return
		}
		
		if data[i] > 100 {
			break // SHOULD GENERATE ERROR: varying condition forbids break
		}
	}
}

// Test 3: ALWAYS ALLOWED - Continue statements
func testAllowedContinue(data []int, threshold uniform int) {
	go for i := range len(data) {
		// ALLOWED: Continue under uniform condition
		if threshold < 0 {
			continue // Always allowed
		}
		
		// ALLOWED: Continue under varying condition
		if data[i] > 10 {
			continue // Always allowed
		}
		
		// Process data[i]
		_ = data[i] * 2
	}
}

// Test 4: FORBIDDEN - Nested varying conditions
func testNestedVaryingConditions(data []int, mode uniform int) {
	go for i := range len(data) {
		// Uniform outer condition - return/break OK here
		if mode == 1 {
			// FORBIDDEN: Inner varying condition makes return/break illegal
			if data[i] > 50 {
				return // SHOULD GENERATE ERROR: enclosing varying condition
			}
			
			// ALLOWED: Still under uniform condition only
			if mode == 2 {
				return // OK: no varying conditions in scope
			}
		}
	}
}

// Test 5: ALLOWED - Regular for loop inside go for
func testRegularForInsideGoFor(data []int, threshold uniform int) {
	go for i := range len(data) {
		// ALLOWED: Uniform return/break at go for level
		if threshold < 0 {
			return // OK: uniform condition
		}
		
		// Regular for loop inside go for is allowed
		for j := 0; j < 5; j++ {
			if j == 3 {
				break // ALLOWED - breaks regular for loop only
			}
			if j == 2 {
				continue // ALLOWED - continues regular for loop only
			}
		}
		
		// FORBIDDEN: Return under varying condition
		if data[i] < 0 {
			// return // SHOULD GENERATE ERROR: varying condition forbids return
		}
	}
}

// Test 6: FORBIDDEN - Mixed uniform and varying nesting
func testMixedNestingScenarios(data []int, limit uniform int) {
	go for i := range len(data) {
		// Complex nesting scenarios
		if limit > 0 { // uniform condition
			if data[i] > limit { // varying condition - now in varying context
				// FORBIDDEN: Any return/break now forbidden
				if limit > 100 { // even uniform conditions can't rescue us
					return // SHOULD GENERATE ERROR: enclosing varying condition
				}
				continue // ALLOWED: continue always OK
			}
			
			// ALLOWED: Back to uniform-only context
			if limit > 500 {
				return // OK: only uniform conditions in scope
			}
		}
	}
}

// Test 7: FORBIDDEN - Switch on varying creates varying context
func testSwitchOnVarying(data []int) {
	go for i := range len(data) {
		switch data[i] { // varying condition
		case 1:
			return // SHOULD GENERATE ERROR: varying switch forbids return
		case 2:
			break // SHOULD GENERATE ERROR: varying switch forbids break
		default:
			continue // ALLOWED: continue always OK
		}
	}
}

// Test 8: ALLOWED - Switch on uniform is fine
func testSwitchOnUniform(data []int, mode uniform int) {
	go for i := range len(data) {
		switch mode { // uniform condition
		case 1:
			return // OK: uniform switch allows return
		case 2:
			break // OK: uniform switch allows break
		default:
			continue // Always OK
		}
	}
}

// Test 9: Edge case - Reduce operations still count as varying context
func testReduceOperations(data []int) {
	go for i := range len(data) {
		// Even though reduce produces uniform result, the input is varying
		varyingCondition := data[i] > 50
		
		if reduce.Any(varyingCondition) { // uniform result from varying input
			return // SHOULD GENERATE ERROR: still considered varying context
		}
	}
}

// Test 10: FORBIDDEN - Nested go for loops (still forbidden)
func testNestedGoFor() {
	go for i := range 10 {
		// ERROR: Nested go for loops not allowed
		go for j := range 10 { // SHOULD GENERATE ERROR: InvalidNestedSPMDFor
			_ = i + j
		}
	}
}

// Test 11: FORBIDDEN - Mask alteration scenarios 
func testMaskAlterationScenarios() {
	data := []int{1, -2, 3, -4, 5}
	mode := uniform int(1)
	
	// ERROR: Return after continue in varying context
	go for i := range len(data) {
		if data[i] < 0 { // varying condition
			continue  // OK: continue always allowed, but alters mask
		}
		
		// Mask has been altered by previous continue
		if mode == 1 { // uniform condition, but mask is altered
			return // SHOULD GENERATE ERROR: return forbidden due to mask alteration
		}
	}
	
	// ERROR: Break after continue in varying context
	go for i := range len(data) {
		if data[i] > 10 { // varying condition
			continue  // Alters mask
		}
		
		if mode > 0 { // uniform condition, but mask altered
			break // SHOULD GENERATE ERROR: break forbidden due to mask alteration
		}
	}
	
	// ERROR: Complex mask alteration with nested conditions
	go for i := range len(data) {
		if data[i] > 0 { // varying condition - depth 1
			if data[i] < 5 { // nested varying condition - depth 2
				continue  // Alters mask - some lanes skip remaining
			}
		}
		
		// Mask altered by continue above
		if mode > 5 { // uniform condition on remaining active lanes only
			return // SHOULD GENERATE ERROR: uniform condition but altered mask
		}
	}
	
	// ALLOWED: Continue in uniform context doesn't alter mask
	go for i := range len(data) {
		if mode < 0 { // uniform condition
			continue  // OK: continue in uniform context doesn't alter mask
		}
		
		// ALLOWED: No mask alteration occurred
		if mode > 10 { // uniform condition, no mask alteration
			return // SHOULD BE ALLOWED: clean uniform context
		}
	}
}

// Test 12: Function with varying parameters cannot contain go for
func testVaryingParamFunction(data varying int) {
	// ERROR: SPMD functions cannot contain go for loops
	go for i := range 10 { // SHOULD GENERATE ERROR: SPMD function restriction
		_ = data + varying(i)
	}
}

// Helper functions
func someUniformCondition() uniform bool {
	return true
}