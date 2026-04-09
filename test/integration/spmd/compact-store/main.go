// run -goexperiment spmd

// E2E test for lanes.CompactStore.
// Tests constant-mask compaction (4→3 pattern) and runtime-mask compaction.
package main

import (
	"fmt"
	"lanes"
)

func main() {
	allPass := true

	// Test 1: Constant mask — every 4th lane inactive (base64 4→3 pattern).
	{
		src := []byte("ABCxDEFxGHIxJKLx")
		dst := make([]byte, 16)
		offset := 0
		go for i, ch := range src {
			pos := i % 4
			n := lanes.CompactStore(dst[offset:], ch, pos != 3)
			offset += n
		}

		expected := "ABCDEFGHIJKL"
		got := string(dst[:offset])
		if got != expected {
			fmt.Printf("FAIL test1: got %q, want %q (offset=%d)\n", got, expected, offset)
			allPass = false
		}
	}

	// Test 2: All lanes active.
	{
		src := []byte("Hello")
		dst := make([]byte, 8)
		offset := 0
		go for i, ch := range src {
			_ = i
			n := lanes.CompactStore(dst[offset:], ch, true)
			offset += n
		}

		expected := "Hello"
		got := string(dst[:offset])
		if got != expected {
			fmt.Printf("FAIL test2: got %q, want %q\n", got, expected)
			allPass = false
		}
	}

	// Test 3: Filter — only keep ASCII lowercase.
	{
		src := []byte("HeLLo WoRLd")
		dst := make([]byte, 16)
		offset := 0
		go for i, ch := range src {
			_ = i
			isLower := ch >= byte('a') && ch <= byte('z')
			n := lanes.CompactStore(dst[offset:], ch, isLower)
			offset += n
		}

		expected := "eood"
		got := string(dst[:offset])
		if got != expected {
			fmt.Printf("FAIL test3: got %q, want %q\n", got, expected)
			allPass = false
		}
	}

	if allPass {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}
