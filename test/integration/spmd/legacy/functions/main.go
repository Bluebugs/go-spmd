// Legacy compatibility test: using 'uniform' and 'varying' as function names
// This ensures existing code continues to work with SPMD extensions
package main

import "fmt"

func main() {
	result1 := uniform(10, 20)
	result2 := varying("hello", "world")
	
	fmt.Printf("uniform(10, 20) = %d\n", result1)
	fmt.Printf("varying(hello, world) = %s\n", result2)
	
	// Test as method names
	calc := Calculator{}
	val1 := calc.uniform(5)
	val2 := calc.varying(3)
	
	fmt.Printf("calc.uniform(5) = %d\n", val1)
	fmt.Printf("calc.varying(3) = %d\n", val2)
	
	// Test as function variables
	var uniformFunc func(int) int = uniform2
	var varyingFunc func(string) string = varying2
	
	fmt.Printf("uniformFunc(7) = %d\n", uniformFunc(7))
	fmt.Printf("varyingFunc(test) = %s\n", varyingFunc("test"))
	
	// Test in closures
	makeUniform := func(x int) func() int {
		return func() int { return x * x }
	}
	
	makeVarying := func(s string) func() string {
		return func() string { return s + "!" }
	}
	
	uniform := makeUniform(4)
	varying := makeVarying("closure")
	
	fmt.Printf("uniform closure: %d\n", uniform())
	fmt.Printf("varying closure: %s\n", varying())
}

// Functions named 'uniform' and 'varying'
func uniform(a, b int) int {
	return a + b
}

func varying(a, b string) string {
	return a + " " + b
}

func uniform2(x int) int {
	return x * 2
}

func varying2(s string) string {
	return "prefix_" + s
}

// Methods named 'uniform' and 'varying'
type Calculator struct{}

func (c Calculator) uniform(x int) int {
	return x * x
}

func (c Calculator) varying(x int) int {
	return x * x * x
}