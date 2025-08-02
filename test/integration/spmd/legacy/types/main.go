// Legacy compatibility test: using 'uniform' and 'varying' as type names
// This ensures existing code continues to work with SPMD extensions
package main

import "fmt"

func main() {
	// Custom types named 'uniform' and 'varying'
	var u uniform = 42
	var v varying = "hello world"
	
	fmt.Printf("uniform type: %d\n", u)
	fmt.Printf("varying type: %s\n", v)
	
	// Test type assertions and conversions
	var i interface{} = uniform(100)
	if val, ok := i.(uniform); ok {
		fmt.Printf("type assertion uniform: %d\n", val)
	}
	
	var j interface{} = varying("test")
	if val, ok := j.(varying); ok {
		fmt.Printf("type assertion varying: %s\n", val)
	}
	
	// Test in struct embedding
	type Container struct {
		uniform
		varying
	}
	
	container := Container{
		uniform: 999,
		varying: "embedded",
	}
	fmt.Printf("embedded uniform: %d\n", container.uniform)
	fmt.Printf("embedded varying: %s\n", container.varying)
	
	// Test as interface names
	var uniformer UniformInterface = UniformImpl{value: 123}
	var varier VaryingInterface = VaryingImpl{text: "interface"}
	
	fmt.Printf("uniform interface: %d\n", uniformer.GetUniform())
	fmt.Printf("varying interface: %s\n", varier.GetVarying())
	
	// Test type switches
	testTypeSwitch(uniform(555))
	testTypeSwitch(varying("switch"))
}

// Custom types named 'uniform' and 'varying'
type uniform int
type varying string

// Interfaces using these names
type UniformInterface interface {
	GetUniform() int
}

type VaryingInterface interface {
	GetVarying() string
}

type UniformImpl struct {
	value int
}

func (u UniformImpl) GetUniform() int {
	return u.value
}

type VaryingImpl struct {
	text string
}

func (v VaryingImpl) GetVarying() string {
	return v.text
}

func testTypeSwitch(v interface{}) {
	switch val := v.(type) {
	case uniform:
		fmt.Printf("type switch uniform: %d\n", val)
	case varying:
		fmt.Printf("type switch varying: %s\n", val)
	default:
		fmt.Printf("type switch unknown: %T\n", val)
	}
}