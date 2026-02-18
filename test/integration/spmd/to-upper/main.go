// ASCII ToUpper using SPMD Go
// From: practical-vector.md
package main

import "fmt"

func main() {
	testStrings := []string{
		"hello world",
		"Hello World",
		"HELLO WORLD",
		"hello123WORLD",
	}

	for _, s := range testStrings {
		result := toUpper([]byte(s))
		fmt.Printf("'%s' -> '%s'\n", s, string(result))
	}
}

func toUpper(s []byte) []byte {
	b := make([]byte, len(s))
	go for i, c := range s {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		} else {
			b[i] = c
		}
	}
	return b
}
