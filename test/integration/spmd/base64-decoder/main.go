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
	laneCount := byteLaneCount()
	chunkWidth := decodeChunkWidth(laneCount)

	for base := 0; base < len(ascii); base += chunkWidth {
		end := base + chunkWidth
		if end > len(ascii) {
			end = len(ascii)
		}

		decodedChunk, valid := decodeChunkBytes(ascii[base:end], laneCount, chunkWidth)
		if !valid {
			return nil, false
		}
		decoded = append(decoded, decodedChunk[:decodedLen(ascii[base:end])]...)
	}

	return decoded, true
}

func byteLaneCount() int {
	var probe [64]byte
	return len(reduce.From(lanes.From(probe[:])))
}

func decodeChunkWidth(laneCount int) int {
	if laneCount < 4 {
		return 4
	}
	return laneCount - laneCount%4
}

func decodeChunkBytes(raw []byte, laneCount, chunkWidth int) ([]byte, bool) {
	if laneCount < 4 {
		return decodeChunkScalar(raw)
	}

	block := make([]byte, chunkWidth)
	for i := range block {
		block[i] = 'A'
	}
	copy(block, raw)

	return decodeChunk(lanes.From(block))
}

func decodeChunk(ascii lanes.Varying[byte]) ([]byte, bool) {
	return decodeChunkScalar(reduce.From(ascii))
}

func decodeChunkScalar(raw []byte) ([]byte, bool) {

	out := make([]byte, 0, 12)
	for base := 0; base < len(raw); base += 4 {
		pads := [4]bool{
			raw[base+0] == '=',
			raw[base+1] == '=',
			raw[base+2] == '=',
			raw[base+3] == '=',
		}
		if pads[0] || pads[1] {
			return nil, false
		}
		if pads[2] && !pads[3] {
			return nil, false
		}

		sextets := [4]byte{}
		for i := range sextets {
			ch := raw[base+i]
			if ch == '=' {
				ch = 'A'
			}
			sextet, ok := decodeSextet(ch)
			if !ok && raw[base+i] != '=' {
				return nil, false
			}
			sextets[i] = sextet
		}

		byte0 := (sextets[0] << 2) | (sextets[1] >> 4)
		byte1 := (sextets[1] << 4) | (sextets[2] >> 2)
		byte2 := (sextets[2] << 6) | sextets[3]

		switch {
		case pads[2]: // xx==
			out = append(out, byte0)
		case pads[3]: // xxx=
			out = append(out, byte0, byte1)
		default: // xxxx
			out = append(out, byte0, byte1, byte2)
		}
	}

	return out, true
}

func decodeSextet(ascii byte) (byte, bool) {
	hi := ascii >> 4
	lo := ascii & 0x0f

	offsetLUT := [16]byte{
		0xff, 0xff, 19, 4, 191, 191, 185, 185,
		0xff, 0xff, 19, 4, 191, 191, 185, 185,
	}
	loLUT := [16]byte{
		0b10101, 0b10001, 0b10001, 0b10001,
		0b10001, 0b10001, 0b10001, 0b10001,
		0b10001, 0b10001, 0b10011, 0b11010,
		0b11011, 0b11011, 0b11011, 0b11010,
	}
	hiLUT := [16]byte{
		0b10000, 0b10000, 0b00001, 0b00010,
		0b00100, 0b01000, 0b00100, 0b01000,
		0b10000, 0b10000, 0b10000, 0b10000,
		0b10000, 0b10000, 0b10000, 0b10000,
	}

	if (loLUT[lo] & hiLUT[hi]) != 0 {
		return 0, false
	}

	offset := offsetLUT[hi]
	if ascii == '/' {
		offset -= 3
	}
	return ascii + offset, true
}

func decodedLen(chunk []byte) int {
	decoded := len(chunk) / 4 * 3
	if len(chunk) >= 2 && chunk[len(chunk)-2] == '=' {
		decoded--
	}
	if len(chunk) >= 1 && chunk[len(chunk)-1] == '=' {
		decoded--
	}
	return decoded
}
