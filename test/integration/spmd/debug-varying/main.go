// run -goexperiment spmd

// Debug example showing how to inspect varying values.
// Printf with %v on varying values shows mask-aware output:
// active lanes print their value, inactive lanes show "_".
package main

import (
	"fmt"
	"reduce"
)

func main() {
	data := []int{10, 20, 30, 40, 50, 60, 70, 80}
	debugProcessing(data)
}

func debugProcessing(data []int) {
	fmt.Printf("Processing %d elements\n", len(data))

	go for _, v := range data {
		doubled := v * 2

		// Printf with varying values: boxed as struct{Value, Mask}
		// All lanes active → shows all values
		fmt.Printf("Lane values: %v\n", v)
		fmt.Printf("Doubled: %v\n", doubled)

		// Inside varying if: partial mask → inactive lanes show "_"
		if v > 25 {
			fmt.Printf("Big values: %v\n", v)
		}

		// Manual conversion to uniform slice (strips mask)
		currentValues := reduce.From(v)
		fmt.Printf("Manual conversion: %v\n", currentValues)

		total := reduce.Add(doubled)
		fmt.Printf("Total for this iteration: %d\n", total)
	}
}
