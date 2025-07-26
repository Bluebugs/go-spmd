// errorcheck -goexperiment spmd

// ILLEGAL: Cannot assign varying values to uniform variables
// Expected error: "cannot assign varying to uniform"
package main

import (
	"lanes"
	"reduce"
)

func main() {
	// ILLEGAL: Direct assignment of varying to uniform
	var uniform_val uniform int
	var varying_val varying int = 42
	
	uniform_val = varying_val  // ERROR: cannot assign varying to uniform

	// ILLEGAL: Trying to assign result of lanes.Index() to uniform
	var lane_id uniform int
	lane_id = lanes.Index()  // ERROR: cannot assign varying to uniform

	// ILLEGAL: Trying to use varying in uniform context
	go for i := range 10 {
		var data varying int = i * 2
		var result uniform int
		result = data  // ERROR: cannot assign varying to uniform
	}

	// ILLEGAL: Return varying from function expecting uniform (type mismatch)
	result := getUniformValue()
	_ = result
}

func getUniformValue() uniform int {
	var v varying int = 42
	return v  // ERROR: cannot return varying from uniform function (type mismatch)
}

// ILLEGAL: Function parameter mismatch
func processUniform(value uniform int) {
	// Implementation
}

func testParameterMismatch() {
	var v varying int = 10
	processUniform(v)  // ERROR: cannot pass varying to uniform parameter
}

// ILLEGAL: Array indexing with varying in uniform context
func testArrayIndexing() {
	data := [10]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	
	go for i := range data {
		var idx varying int = i
		var uniform_result uniform int
		uniform_result = data[idx]  // ERROR: array access with varying index produces varying result
	}
}