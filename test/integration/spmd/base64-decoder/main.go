// Base64 decoder with cross-lane communication using SPMD Go
// From: cross-lane-communication.md
// Based on: https://github.com/mcy/vb64/blob/main/src/simd.rs#L16-L144
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	testCases := []string{
		"SGVsbG8gV29ybGQ=", // "Hello World"
		"Zm9vYmFy",         // "foobar"
		"YWJjZA==",         // "abcd"
	}

	for _, input := range testCases {
		decoded, ok := Decode([]byte(input))
		if ok {
			fmt.Printf("'%s' -> '%s'\n", input, string(decoded))
		} else {
			fmt.Printf("'%s' -> ERROR\n", input)
		}
	}
}

func Decode(ascii []byte) ([]byte, bool) {
	if len(ascii) == 0 {
		return nil, true
	}
	if len(ascii)%4 != 0 {
		return nil, false // Base64 requires input length multiple of 4
	}

	decoded := make([]byte, 0, len(ascii)*3/4)
	pattern := outputPattern()

	go for _, v := range[4] ascii {
		decodedChunk, valid := decodeChunk(v, pattern)
		if !valid {
			return nil, false
		}
		decoded = append(decoded, decodedChunk...)
	}

	return decoded, true
}

func outputPattern() varying[4] uint8 {
	var r varying[4] uint8
	go for i := range[4] {
		r[i] = uint8(i + i/3) // Creates: [0,1,2,4]
	}
	return r
}

func decodeChunk(ascii varying[4] byte, pattern varying[4] uint8) ([]byte, bool) {
	// Step 1: Perfect hash function for table indexing
	hashes := lanes.ShiftRight(ascii, 4)
	if ascii == '/' {
		hashes += 1
	}

	// Step 2: Convert ASCII to 6-bit values via table lookup (Swizzle)
	offsetTable := []byte{255, 16, 19, 4, 191, 191, 185, 185}
	offsets := lanes.Swizzle(lanes.From(offsetTable), hashes)
	sextets := ascii + offsets

	// Step 3: Validate characters using parallel lookups (Swizzle + Reduction)
	loLUT := lanes.From([]byte{
		0b10101, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001,
		0b10001, 0b10001, 0b10011, 0b11010, 0b11011, 0b11011, 0b11011, 0b11010,
	})
	hiLUT := lanes.From([]byte{
		0b10000, 0b10000, 0b00001, 0b00010, 0b00100, 0b01000, 0b00100, 0b01000,
		0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000,
	})

	lo := lanes.Swizzle(loLUT, ascii&0x0f)
	hi := lanes.Swizzle(hiLUT, lanes.ShiftRight(ascii, 4))
	valid := reduce.Or(lo&hi) == 0

	// Step 4: Pack 6-bit values into bytes with cross-lane coordination (Rotation)
	shiftPattern := lanes.From([]uint16{2, 4, 6, 8})
	shifted := lanes.ShiftLeft(sextets, shiftPattern)

	shiftedLo := varying[4] byte(shifted)
	shiftedHi := varying[4] byte(lanes.ShiftRight(shifted, 8))
	decodedChunks := shiftedLo | lanes.Rotate(shiftedHi, 1)

	// Step 5: Extract final 3 bytes using output pattern (Swizzle)
	output := lanes.Swizzle(decodedChunks, pattern)
	return []byte(output), valid
}