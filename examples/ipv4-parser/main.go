// IPv4 parser using SPMD Go
// From: go-spmd-ipv4-parser.md
// Based on: Wojciech Muła's SIMD IPv4 parsing research
package main

import (
	"fmt"
	"math/bits"

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
		"192.168.01.1",  // Invalid: leading zero
		"256.1.1.1",     // Invalid: >255
		"192.168.1",     // Invalid: too few octets
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

	// Process all 16 bytes in parallel: classify chars and build dot bitmask
	var dotBitmask uint16
	var loop int
	go for _, c := range input {
		isDot := c == '.'
		digitMask := (c >= '0' && c <= '9')

		// Valid if dot, digit, or null (padding)
		validChars := isDot || digitMask || c == 0

		// Check character validity with precise error location
		if !reduce.All(validChars) {
			return [4]byte{}, parseAddrError{in: s, at: reduce.FindFirstSet(!validChars) + loop, msg: "unexpected character"}
		}

		// Build dot position bitmask directly (mimics _mm_movemask_epi8)
		dotBitmask |= uint16(reduce.Mask(isDot)) << loop
		loop += lanes.Count(c)
	}

	// Count dots using popcount on the bitmask
	dotCount := bits.OnesCount16(dotBitmask)
	if dotCount != 3 {
		return [4]byte{}, parseAddrError{in: s, msg: fmt.Sprintf("invalid dot count: %d", dotCount)}
	}

	// Extract dot positions using bit manipulation
	var dotPositions [3]int
	for i := 0; i < 3; i++ {
		pos := bits.TrailingZeros16(dotBitmask)
		dotPositions[i] = pos
		dotBitmask &= dotBitmask - 1 // Clear lowest set bit
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

	go for field, start := range starts {
		end := ends[field]

		if field > 0 {
			start++ // Skip the dot
		}

		fieldLen := end - start
		var value int
		var hasLeadingZero bool

		// Convert field using per-case digit processing.
		// Each case computes the full value independently to avoid
		// out-of-bounds accesses on inactive SPMD lanes with fallthrough.
		switch fieldLen {
		case 3:
			d2 := int(s[start] - '0')
			d1 := int(s[start+1] - '0')
			d0 := int(s[start+2] - '0')
			value = d2*100 + d1*10 + d0
			hasLeadingZero = (d2 == 0)
		case 2:
			d1 := int(s[start] - '0')
			d0 := int(s[start+1] - '0')
			value = d1*10 + d0
			hasLeadingZero = (d1 == 0)
		case 1:
			value = int(s[start] - '0')
		}

		// Validation: check each error condition across all lanes
		if reduce.Any(hasLeadingZero) {
			return [4]byte{}, parseAddrError{in: s, msg: "IPv4 field has octet with leading zero"}
		}
		if reduce.Any(value > 255) {
			return [4]byte{}, parseAddrError{in: s, msg: "IPv4 field has value >255"}
		}
		ip[field] = uint8(value)
	}

	return ip, nil
}
