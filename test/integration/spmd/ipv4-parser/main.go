// IPv4 parser using SPMD Go
// From: go-spmd-ipv4-parser.md
// Based on: Wojciech Muła's SIMD IPv4 parsing research
package main

import (
	"bits"
	"fmt"
	"lanes"
	"reduce"
)

type parseAddrError struct {
	in  string
	at  int
	msg string
}

func (e parseAddrError) Error() string {
	if e.at >= 0 {
		return fmt.Sprintf("parse %s at position %d: %s", e.in, e.at, e.msg)
	}
	return fmt.Sprintf("parse %s: %s", e.in, e.msg)
}

func main() {
	testCases := []string{
		"192.168.1.1",
		"10.0.0.1",
		"255.255.255.255",
		"0.0.0.0",
		"127.0.0.1",
		"192.168.01.1", // Invalid: leading zero
		"256.1.1.1",    // Invalid: >255
		"192.168.1",    // Invalid: too few octets
		"192.168.1.1.1", // Invalid: too many octets
		"192.168.1.a",   // Invalid: non-digit
	}

	for _, addr := range testCases {
		ip, err := parseIPv4(addr)
		if err != nil {
			fmt.Printf("'%s' -> ERROR: %v\n", addr, err)
		} else {
			fmt.Printf("'%s' -> %d.%d.%d.%d\n", addr, ip[0], ip[1], ip[2], ip[3])
		}
	}
}

func parseIPv4(s string) ([4]byte, error) {
	if len(s) < 7 || len(s) > 15 {
		return [4]byte{}, parseAddrError{in: s, msg: "IPv4 address string too short or too long"}
	}

	// Pad string to 16 bytes with null terminators (like SSE register)
	input := [16]byte{}
	copy(input[:], s)

	// Process all 16 elements using SIMD lanes
	var dotMaskTotal lanes.Varying[uint8, 16]
	var dotMask lanes.Varying[bool, 16]
	var digitMask lanes.Varying[bool, 16]
	var validChars lanes.Varying[bool, 16]

	go for i, c := range[16] input {
		dotMask[i] = c == '.'
		if dotMask[i] {
			dotMaskTotal[i] = 1
		}
		digitMask[i] = (c >= '0' && c <= '9')

		// Valid if dot, digit, or null (padding)
		validChars[i] = dotMask[i] || digitMask[i] || c == 0
	}

	// Check character validity with precise error location
	if !reduce.All(validChars) {
		return [4]byte{}, parseAddrError{in: s, at: reduce.FindFirstSet(validChars), msg: "unexpected character"}
	}

	// Count dots using reduction
	dotCount := reduce.Sum(dotMaskTotal)
	if dotCount != 3 {
		return [4]byte{}, parseAddrError{in: s, msg: "invalid dot count"}
	}

	// Create dot position bitmask (mimics _mm_movemask_epi8)
	dotPositionMask := reduce.Mask(dotMask)

	// Extract dot positions using bit manipulation
	var dotPositions [3]int
	mask := dotPositionMask
	for i := 0; i < 3; i++ {
		pos := bits.TrailingZeros16(mask)
		dotPositions[i] = pos
		mask &= mask - 1 // Clear lowest set bit
	}

	// Define field boundaries as separate arrays for efficient range processing
	starts := [4]int{0, dotPositions[0], dotPositions[1], dotPositions[2]}
	ends := [4]int{dotPositions[0], dotPositions[1], dotPositions[2], len(s)}

	// Validate field lengths in parallel
	go for i, start := range starts {
		end := ends[i]
		if i > 0 {
			start++ // Skip the dot
		}
		fieldLen := end - start
		if reduce.Any(fieldLen < 1 || fieldLen > 3) {
			return [4]byte{}, parseAddrError{in: s, msg: "invalid field length"}
		}
	}

	// Process all four fields in parallel
	var ip [4]byte
	var errors [4]parseAddrError
	var hasError lanes.Varying[bool, 4]

	go for field, start := range starts {
		end := ends[field]

		if field > 0 {
			start++ // Skip the dot
		}

		fieldLen := end - start
		var value int
		var hasLeadingZero bool

		// Convert field using optimized digit processing
		switch fieldLen {
		case 1:
			value = int(s[start] - '0')
		case 2:
			d1 := int(s[start] - '0')
			d0 := int(s[start+1] - '0')
			value = d1*10 + d0
			hasLeadingZero = (d1 == 0)
		case 3:
			d2 := int(s[start] - '0')
			d1 := int(s[start+1] - '0')
			d0 := int(s[start+2] - '0')
			value = d2*100 + d1*10 + d0
			hasLeadingZero = (d2 == 0)
		}

		// Validation and error handling
		if hasLeadingZero {
			errors[field] = parseAddrError{in: s, msg: "IPv4 field has octet with leading zero"}
			hasError[field] = true
		} else if value > 255 {
			errors[field] = parseAddrError{in: s, msg: "IPv4 field has value >255"}
			hasError[field] = true
		} else {
			ip[field] = uint8(value)
		}
	}

	// Check for errors using reduction
	if reduce.Any(hasError) {
		return [4]byte{}, errors[reduce.FindFirstSet(hasError)]
	}

	return ip, nil
}
