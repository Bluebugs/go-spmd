// ILLEGAL: Using SPMD constructs in invalid contexts
// Expected errors: Various context-related errors
package main

import (
	"lanes"
	"reduce"
)

// ILLEGAL: Using lanes.Index() outside SPMD context
func outsideSPMDContext() {
	// ERROR: lanes.Index() requires SPMD context (go for) or SPMD function (varying params)
	current_lane := lanes.Index()
	
	// LEGAL: Most lanes functions work outside SPMD context
	data := []int{1, 2, 3, 4}
	broadcasted := lanes.Broadcast(varying(data[0]), 0)  // LEGAL: lanes.Broadcast works anywhere
	
	_, _ = current_lane, broadcasted
}

// ILLEGAL: Global varying variables
var global_varying varying int = 42  // ERROR: varying types not allowed at package level

// ILLEGAL: Varying in struct fields at package level
type GlobalStruct struct {
	field varying int  // ERROR: varying types not allowed in package-level structs
}

// ILLEGAL: goto jumping into/out of SPMD context
func invalidGoto() {
	data := []int{1, 2, 3, 4, 5}
	
	goto inside_spmd  // ERROR: cannot goto into SPMD context
	
	go for i := range data {
		if data[i] == 3 {
			goto outside_spmd  // ERROR: cannot goto out of SPMD context
		}
		
	inside_spmd:  // ERROR: label inside SPMD context unreachable from outside
		data[i] *= 2
	}
	
outside_spmd:
	println("done")
}

// ILLEGAL: Varying function receivers
type MyType struct {
	value int
}

func (m varying MyType) Process() {  // ERROR: varying receiver not allowed
	// Method implementation
}

// ILLEGAL: Varying in interface definitions
type Processor interface {
	Process(data varying int) varying int  // ERROR: varying types in interface methods
}

// ILLEGAL: Varying in type definitions at package level
type VaryingInt varying int  // ERROR: cannot define package-level type with varying

// LEGAL: Defer with varying values
func validDefer() {
	go for i := range 5 {
		var data varying int = i * 2
		
		defer func(x varying int) {  // LEGAL: defer can capture varying values
			processVarying(x)  // SPMD function processes all lanes
		}(data)
		
		// Also legal: defer with varying function call
		defer processVarying(data)  // LEGAL: deferred call can use varying arguments
	}
}

func processVarying(x varying int) {
	// Implementation
}

// LEGAL: Goroutines with varying values (now allowed
func validGoroutines() {
	go for i := range 5 {
		var data varying int = i + 10
		
		// LEGAL: can launch goroutine with varying values
		go func(x varying int) {
			processVarying(x)  // SPMD function processes all lanes
		}(data)
		
		// LEGAL: goroutine call can use varying arguments
		go processVarying(data)
	}
}

// ILLEGAL: Varying map keys at declaration and access sites
func invalidMaps() {
	// ERROR: map keys cannot be varying at declaration
	var m1 map[varying int]string
	var m2 map[varying string]int
	
	// LEGAL: map values can be varying (but not recommended)
	var validMap map[string]varying int
	
	data := []string{"a", "b", "c", "d"}
	
	go for i := range data {
		var key varying string = data[i]
		var value varying int = i * 10
		
		// ERROR: cannot use varying key in map access
		result := validMap[key]
		
		// ERROR: cannot use varying key in map assignment
		validMap[key] = value
		
		// ERROR: cannot use varying key in map delete
		delete(validMap, key)
		
		// ERROR: cannot use varying key in existence check
		_, exists := validMap[key]
		
		// LEGAL: uniform keys are allowed
		uniformKey := "key" + string(rune('0' + i))
		validMap[uniformKey] = value  // OK: uniform key, varying value
		
		_, _ = result, exists
	}
	
	_, _ = m1, m2
}

// ILLEGAL: Invalid pointer operations with varying types
func invalidPointers() {
	var data varying int = 42
	
	// ERROR: cannot assign varying pointer to uniform pointer
	var varyingPtr varying *int = &data
	var uniformPtr *int = varyingPtr  // ERROR: type mismatch
	
	// ERROR: cannot dereference varying pointer in uniform context
	var uniform_result uniform int = *varyingPtr  // ERROR: varying to uniform assignment
	
	// ERROR: invalid pointer arithmetic with varying types
	var basePtr *int
	var varyingOffset varying int = 5
	// This would be invalid: ptr := basePtr + varyingOffset
	
	_, _ = uniformPtr, uniform_result
}

// ILLEGAL: Incorrect type switch patterns with varying
func invalidTypeSwitch() {
	var varying_interface varying interface{} = varying(42)
	
	// ERROR: Cannot match varying interface{} with uniform types
	switch v := varying_interface.(type) {
	case int:          // ERROR: must use "case varying int:"
		println("int:", v)
	case string:       // ERROR: must use "case varying string:"
		println("string:", v)
	}
	
	// ERROR: Type assertion without explicit varying
	x := varying_interface.(int)  // ERROR: must use varying_interface.(varying int)
	println(x)
}

// LEGAL: Varying in recover()
func validRecover() {
	go for i := range 3 {
		defer func() {
			if r := recover(); r != nil {
				var varying_r varying interface{} = r  // LEGAL: recover() in varying context
				processVaryingInterface(varying_r)
			}
		}()
		
		// Simulate panic
		if i == 1 {
			panic(i) // THIS WILL ALWAYS PANIC AS IT WILL BE CALLED EVERYTIME JUST WITH DIFFERENT MASK, BUT IT'S LEGAL
		}
	}
}

func processVaryingInterface(x varying interface{}) {
	// Implementation
}

// ILLEGAL: Varying slice/array bounds
func invalidBounds() {
	var size varying int = 10
	
	// ERROR: array size must be uniform constant
	var arr [size]int
	
	// ERROR: slice bounds must be uniform
	data := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	var start varying int = 2
	var end varying int = 7
	slice := data[start:end]
	
	_, _ = arr, slice
}

// LEGAL: Return varying from non-SPMD function (now allowed)
func nonSPMDFunction() varying int {  // LEGAL: non-SPMD function can return varying
	return varying(42)  // All lanes get same value
}

// ILLEGAL: Reduce functions with uniform values (type mismatch)
func invalidReduce() {
	uniform_data := []int{1, 2, 3, 4}
	
	// ERROR: reduce functions require varying input (type mismatch)
	sum := reduce.Add(uniform_data[0])  // ERROR: uniform int, not varying int
	
	// LEGAL: reduce functions work outside SPMD context
	var varying_val varying int = 42
	result := reduce.Add(varying_val)  // LEGAL: reduce outside SPMD context is allowed
	
	_, _ = sum, result
}

// LEGAL examples for comparison
func legalUsage() {
	// This is how varying should be used properly
	data := []int{1, 2, 3, 4, 5}
	
	go for i := range data {
		var varying_data varying int = data[i]
		
		// Legal: lanes functions inside SPMD context
		lane_id := lanes.Index()
		
		// Legal: reduce functions with varying data in SPMD context
		if lane_id == 0 {
			total := reduce.Add(varying_data)
			println("Total:", total)
		}
		
		// Legal: varying arithmetic
		result := varying_data * 2
		data[i] = result
	}
}