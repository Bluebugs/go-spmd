// run -goexperiment spmd -target=wasi

// Example demonstrating map restrictions with varying types
// Shows what's allowed and what's prohibited with maps in SPMD contexts
package main

import (
	"fmt"
	"lanes"
	"reduce"
	"strconv"
)

// validMapUsage demonstrates correct map usage with SPMD
func validMapUsage() {
	fmt.Println("=== Valid Map Usage ===")
	
	// ✓ ALLOWED: Uniform keys with varying values  
	counts := make(map[string]varying int)
	
	data := []int{10, 20, 30, 40}
	baseIndex := 0
	
	go for _, value := range data {
		// value is varying (each lane processes different elements)
		
		// ✓ ALLOWED: Uniform key generation using uniform baseIndex
		uniformKey := "batch_" + strconv.Itoa(baseIndex)
		
		// ✓ ALLOWED: Store varying value with uniform key
		counts[uniformKey] = value * 2
		
		// ✓ ALLOWED: Read with uniform key (returns varying value)
		stored := counts[uniformKey]
		
		// ✓ ALLOWED: Check existence with uniform key
		if storedVal, exists := counts[uniformKey]; exists {
			// Convert varying to uniform for printing
			values := reduce.From(storedVal)
			fmt.Printf("Key %s has value: %d\n", uniformKey, values)
		}
		
		// ✓ ALLOWED: Delete with uniform key
		delete(counts, uniformKey)
		
		// Increment uniform counter for next batch
		baseIndex += lanes.Count(value)
	}
}

// demonstrateWorkarounds shows how to work around varying key restrictions
func demonstrateWorkarounds() {
	fmt.Println("\n=== Workarounds for Varying Keys ===")
	
	// Use case: Want to group data by varying keys
	data := []string{"apple", "banana", "cherry", "date"}
	
	// ✓ WORKAROUND: Convert to uniform using reduce.From()
	go for i, keys := range data {
		// keys is varying (each lane processes different elements)
		values := i * 10 // varying value from iteration
		
		// Process with uniform keys (no conversion needed)
		value := reduce.From(values)
		for i, key := range reduce.From(keys) {
			groupByKey(key, value[i])
		}
	}
}

// groupByKey processes a single key-value pair
func groupByKey(key string, value int) {
	// In practice, you'd accumulate these in a shared data structure
	fmt.Printf("Processing: %s -> %d\n", key, value)
}

// Alternative: Use struct slices instead of maps
type KeyValuePair struct {
	Key   string
	Value int
}

func structAlternative() {
	fmt.Println("\n=== Struct Alternative to Maps ===")
	
	// ✓ ALTERNATIVE: Use slice of structs instead of map
	var pairs []KeyValuePair
	
	data := []string{"x", "y", "z", "w"}
	
	go for i, key := range data {
		// key is varying (each lane processes different elements)
		value := i     // varying value

		// ✓ ALLOWED: Append struct with varying key and value
		pairs = append(pairs, KeyValuePair{
			Key:   key,
			Value: value,
		})
	}
	
	// Process the pairs
	for _, pair := range pairs {
		fmt.Printf("Pair: %s -> %d\n", pair.Key, pair.Value) // Printf knows how to handle varying values
	}
}

// demonstrateMapValueUsage shows allowed map usage patterns
func demonstrateMapValueUsage() {
	fmt.Println("\n=== Standard Map Usage Patterns ===")
	
	// ✓ ALLOWED: Standard maps with uniform types
	cache := make(map[string]int)
	
	go for i := range 4 {
		key := fmt.Sprintf("cache_%d", i)  // uniform key
		value := i * 100                   // varying value

		cache[key] = reduce.Add(values) // Store uniform value sum of varying values

		// ✓ ALLOWED: Retrieve with uniform key
		retrieved := cache[key]
		fmt.Printf("Retrieved %s: %d\n", key, retrieved)
	}
}

func main() {
	fmt.Println("=== Map Restrictions with Varying Types ===")
	
	// Show valid usage patterns
	validMapUsage()
	
	// Show workarounds for varying key restrictions
	demonstrateWorkarounds()
	
	// Show struct alternative
	structAlternative()
	
	// Show varying values in maps (with caveats)
	demonstrateMapValueUsage()
	
	fmt.Println("\n✓ Map restrictions demonstration completed")
}