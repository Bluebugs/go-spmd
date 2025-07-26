// Legacy compatibility test: using 'uniform' and 'varying' as variable names
// This ensures existing code continues to work with SPMD extensions
package main

import "fmt"

func main() {
	// Variables named 'uniform' and 'varying' should work normally
	var uniform int = 42
	var varying string = "hello"
	
	fmt.Printf("uniform variable: %d\n", uniform)
	fmt.Printf("varying variable: %s\n", varying)
	
	// Test in different scopes
	if true {
		uniform := 100  // Shadow outer uniform
		varying := "world"  // Shadow outer varying
		fmt.Printf("scoped uniform: %d\n", uniform)
		fmt.Printf("scoped varying: %s\n", varying)
	}
	
	// Test as slice/map keys
	data := map[string]int{
		"uniform": 1,
		"varying": 2,
	}
	fmt.Printf("map[uniform]: %d\n", data["uniform"])
	fmt.Printf("map[varying]: %d\n", data["varying"])
	
	// Test in for loops
	for uniform := 0; uniform < 3; uniform++ {
		varying := uniform * 2
		fmt.Printf("loop uniform=%d, varying=%d\n", uniform, varying)
	}
	
	// Test in struct fields
	type Config struct {
		uniform int
		varying string
	}
	
	config := Config{
		uniform: 123,
		varying: "test",
	}
	fmt.Printf("struct.uniform: %d\n", config.uniform)
	fmt.Printf("struct.varying: %s\n", config.varying)
}