// bytes.ToUpper ASCII fast path using SPMD Go
// From: practical-vector.md
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	testStrings := []string{
		"hello world",
		"Hello World",
		"HELLO WORLD",
		"hello123WORLD",
		"café", // Non-ASCII to test fallback
	}

	for _, s := range testStrings {
		result := toUpper([]byte(s))
		fmt.Printf("'%s' -> '%s'\n", s, string(result))
	}
}


func ToUpper(s []byte) []byte {
    var hasLower lanes.Varying[bool]

	isASCII := true
    go for _, c := range s {
        if reduce.Any(c >= utf8.RuneSelf) {
            isASCII = false
            break
        }
        hasLower = hasLower || ('a' <= c && c <= 'z')
    }
    if isASCII { // optimize for ASCII-only byte slices.
        if reduce.All(!hasLower) {
            return append([]byte(""), s...)
        }
        b := bytealg.MakeNoZero(len(s))[:len(s):len(s)]
        go for i, c := range s {
            if 'a' <= c && c <= 'z' {
                c -= 'a' - 'A'
            }
            b[i] = c
        }
        return b
    }

	// Fallback to unicode.ToUpper for strings with non-ASCII characters.
	return strings.Map(unicode.ToUpper, s)
}
