// Simple Base64 encode/decode with round-trip verification using SPMD Go
// Demonstrates lane-independent go for with lookup-table indexing (no cross-lane ops)
package main

import (
//	"bytes"
	"fmt"
)

const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// b64decode maps ASCII byte -> 6-bit value (0-63). Invalid entries are 0xFF.
var b64decode [256]byte

func init() {
	for i := range b64decode {
		b64decode[i] = 0xFF
	}
	for i := 0; i < len(b64chars); i++ {
		b64decode[b64chars[i]] = byte(i)
	}
	b64decode['='] = 0 // padding decodes as zero bits
}

func main() {
	testCases := []string{
		"SGVsbG8gV29ybGQ=", // "Hello World"
		"Zm9vYmFy",         // "foobar"
		"YWJjZA==",         // "abcd"
	}

	for _, input := range testCases {
		fmt.Printf("decoding '%s' (len=%d)...\n", input, len(input))
		decoded := decode([]byte(input))
		fmt.Printf("'%s' -> '%s'\n", string(input), string(decoded))
//		encoded := encode(decoded)
//		match := string(encoded) == input
//		fmt.Printf("'%s' -> '%s' -> '%s' (match: %v)\n", input, string(decoded), string(encoded), match)
	}
}

func decode(src []byte) []byte {
	fmt.Printf("  decode: src len=%d\n", len(src))
	groups := len(src) / 4
	fmt.Printf("  decode: groups=%d\n", groups)
	if groups == 0 {
		return nil
	}

	// Count padding characters
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if src[len(src)-2] == '=' {
		padCount++
	}

	buf := make([]byte, groups*3)
	fmt.Printf("  decode: buf len=%d, entering SPMD loop\n", len(buf))

	go for i := range buf {
		group := byte(i / 3)
		pos := byte(i % 3)

		// Gather the 4 base64 sextets for this group (widen to int to stay in 4-lane width)
		c0 := b64decode[src[group*4+0]]
		c1 := b64decode[src[group*4+1]]
		c2 := b64decode[src[group*4+2]]
		c3 := b64decode[src[group*4+3]]

		// Compute all three possible output bytes unconditionally (all int, 4-lane)
		switch (pos) {
		case 0:
			buf[i] = (c0 << 2) | (c1 >> 4)
		case 1:
			buf[i] = (c1 << 4) | (c2 >> 2)
		case 2:
			buf[i] = (c2 << 6) | c3
		}
	}

	return buf[:groups*3-padCount]
}

/*
func encode(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}

	// Pad input to multiple of 3
	padCount := (3 - len(src)%3) % 3
	padded := make([]byte, len(src)+padCount)
	copy(padded, src)
	// padded trailing bytes are already zero

	groups := len(padded) / 3
	buf := make([]byte, groups*4)

	go for i := range buf {
		group := i / 4
		pos := i % 4

		// Gather the 3 input bytes for this group (widen to int to stay in 4-lane width)
		b0 := int(padded[group*3+0])
		b1 := int(padded[group*3+1])
		b2 := int(padded[group*3+2])

		// Compute all four possible 6-bit indices unconditionally (all int, 4-lane)
		v0 := b0 >> 2
		v1 := ((b0 & 0x03) << 4) | (b1 >> 4)
		v2 := ((b1 & 0x0F) << 2) | (b2 >> 6)
		v3 := b2 & 0x3F

		// Select based on position (int conditions, 4-lane masks)
		idx := v0
		if pos == 1 {
			idx = v1
		}
		if pos == 2 {
			idx = v2
		}
		if pos == 3 {
			idx = v3
		}
		buf[i] = b64chars[idx]
	}

	// Replace trailing chars with '=' for padding
	for p := 0; p < padCount; p++ {
		buf[len(buf)-1-p] = '='
	}

	return buf
}
*/