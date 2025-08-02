// Printf verbs finder using SPMD Go
// From: practical-vector.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	formats := []string{
		"Hello %s, you are %d years old",
		"Temperature: %f degrees",
		"No verbs here",
		"Multiple %s verbs %d here %f",
	}
	
	for _, format := range formats {
		pos := findFirstVerb(format)
		if pos >= 0 {
			fmt.Printf("Found first '%%' at position %d in: %s\n", pos, format)
		} else {
			fmt.Printf("No '%%' found in: %s\n", format)
		}
	}
}

func findFirstVerb(s string) int {
	// Things stays the same outside of SPMD context, i is by default an uniform int
	i := 0
  	
	go for _, c := range s {
    	check := c == '%'
    
		if reduce.Any(check) {
			// This is legal, as we are outside of SPMD context due to the if being on uniform value
        	return i + reduce.FindFirstSet(check)
    	}
    
		i += lanes.Count(c)
  	}
  	
	return len(s)
}