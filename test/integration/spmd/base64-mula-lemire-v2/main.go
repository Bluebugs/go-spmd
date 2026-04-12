// run -goexperiment spmd

// Mula-Lemire base64 decoder v2: uses pmaddubsw/pmaddwd packing.
// The key difference from v1: instead of shift/OR + CompactStore,
// this version uses cascading go-for loops at decreasing SIMD widths
// (byte → int16 → int32) to trigger pmaddubsw and pmaddwd pattern detection.
//
// The caller iterates over the source in register-sized chunks.
// decodeAndPack processes exactly one chunk: decode sextets + pack output.
// No Rotate, no SPMDMux, no CompactStore.
package main

import "fmt"

// Nibble-LUT decode table, indexed by the high nibble of the ASCII character.
//
// '+' (0x2B, sextet 62) and '/' (0x2F, sextet 63) share hi-nibble 2.
// decodeLUT[2] = 16 is correct for '/' (0x2F + 16 = 63).
// '+' needs offset 19, so we add 3 via a varying conditional (SPMD select).
//
// Values computed as (sextet - ASCII) & 0xFF:
//
//	hi=2: 16  (for '/'; '+' corrected via explicit if)
//	hi=3: 4   ('0'-'9')
//	hi=4: 191 ('A'-'O')
//	hi=5: 191 ('P'-'Z')
//	hi=6: 185 ('a'-'o')
//	hi=7: 185 ('p'-'z')
var decodeLUT = [16]byte{
	0, 0, 16, 4, 191, 191, 185, 185,
	0, 0, 0, 0, 0, 0, 0, 0,
}

// decodeAndPack processes one SIMD-register-width chunk of base64 source.
// src must be exactly laneCount bytes (16 on SSE, 32 on AVX2).
// Decodes sextets + packs via cascading multiply-add loops.
// Returns number of output bytes written to dst (= len(src) * 3/4).
func decodeAndPack(dst, src []byte) int {
	n := len(src)

	// Loop 1 (byte-width): decode ASCII → 6-bit sextets via nibble LUT.
	// 16 lanes on SSE, 32 on AVX2.
	sextets := make([]byte, n)
	go for i, ch := range src {
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
	}

	// Loop 2 (int16-width): merge adjacent sextet pairs.
	// 8 lanes on SSE, 16 on AVX2.
	// a*64 + b = (a<<6)|b → pmaddubsw pattern [64, 1, 64, 1, ...].
	halfLen := n / 2
	merged := make([]int16, halfLen)
	go for g := range merged {
		merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
	}

	// Loop 3 (int32-width): merge adjacent int16 pairs.
	// 4 lanes on SSE, 8 on AVX2.
	// a*4096 + b → pmaddwd pattern [4096, 1, 4096, 1, ...].
	// Result: (s0<<18)|(s1<<12)|(s2<<6)|s3
	quarterLen := halfLen / 2
	packed := make([]int32, quarterLen)
	go for g := range packed {
		packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
	}

	// Loop 4 (int32-width): extract 3 bytes per packed int32.
	go for g := range packed {
		dst[g*3+0] = byte(packed[g] >> 16)
		dst[g*3+1] = byte(packed[g] >> 8)
		dst[g*3+2] = byte(packed[g])
	}

	return quarterLen * 3
}

// spmdDecode decodes base64 using per-chunk SPMD processing.
// The caller iterates over the source in register-sized chunks,
// calling decodeAndPack for each chunk.
func spmdDecode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}

	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}
	hotBytes := hotGroups * 4

	dst := make([]byte, groups*3+64)

	// Determine chunk size = byte lane count. Use 32 as safe default
	// (works for both SSE 16-lane and AVX2 32-lane).
	chunkSize := 32
	outOffset := 0

	// Process full chunks.
	for off := 0; off+chunkSize <= hotBytes; off += chunkSize {
		n := decodeAndPack(dst[outOffset:], src[off:off+chunkSize])
		outOffset += n
	}

	// Handle remaining bytes (less than one full chunk).
	rem := hotBytes % chunkSize
	if rem > 0 && rem%4 == 0 {
		// Pad remainder to chunkSize with 'A' (sextet 0).
		padded := make([]byte, chunkSize)
		copy(padded, src[hotBytes-rem:hotBytes])
		for i := rem; i < chunkSize; i++ {
			padded[i] = 'A'
		}
		tmpDst := make([]byte, chunkSize) // oversize for safety
		n := decodeAndPack(tmpDst, padded)
		// Only copy the valid output bytes (rem*3/4).
		validOut := rem * 3 / 4
		copy(dst[outOffset:], tmpDst[:validOut])
		outOffset += validOut
		_ = n
	}

	// Handle padding quartet with scalar fallback.
	if hotGroups < groups {
		tail := src[hotGroups*4:]
		c0, _ := decodeSextet(tail[0])
		c1, _ := decodeSextet(tail[1])
		var c2, c3 byte
		if tail[2] != '=' {
			c2, _ = decodeSextet(tail[2])
		}
		if tail[3] != '=' {
			c3, _ = decodeSextet(tail[3])
		}
		dst[outOffset+0] = (c0 << 2) | (c1 >> 4)
		dst[outOffset+1] = (c1 << 4) | (c2 >> 2)
		dst[outOffset+2] = (c2 << 6) | c3
		outOffset += 3
	}

	return dst[:outOffset-padCount], true
}

// decodeSextet converts a single base64 ASCII character to its 6-bit value.
func decodeSextet(ch byte) (byte, bool) {
	switch {
	case 'A' <= ch && ch <= 'Z':
		return ch - 'A', true
	case 'a' <= ch && ch <= 'z':
		return ch - 'a' + 26, true
	case '0' <= ch && ch <= '9':
		return ch - '0' + 52, true
	case ch == '+':
		return 62, true
	case ch == '/':
		return 63, true
	}
	return 0, false
}

// scalarDecode is the reference implementation for correctness checking.
func scalarDecode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}

	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	dst := make([]byte, groups*3)

	for g := 0; g < groups; g++ {
		var sextets [4]byte
		for j := 0; j < 4; j++ {
			ch := src[g*4+j]
			if ch == '=' {
				sextets[j] = 0
				continue
			}
			s, ok := decodeSextet(ch)
			if !ok {
				return nil, false
			}
			sextets[j] = s
		}
		dst[g*3+0] = (sextets[0] << 2) | (sextets[1] >> 4)
		dst[g*3+1] = (sextets[1] << 4) | (sextets[2] >> 2)
		dst[g*3+2] = (sextets[2] << 6) | sextets[3]
	}

	return dst[:groups*3-padCount], true
}

func main() {

	testCases := []struct {
		input    string
		expected string
	}{
		{"SGVsbG8gV29ybGQ=", "Hello World"},
		{"Zm9vYmFy", "foobar"},
		{"YWJjZA==", "abcd"},
		{"", ""},
		{"YQ==", "a"},
		{"YWI=", "ab"},
		{"YWJj", "abc"},
		// Longer input exercises multiple SIMD register widths.
		{"VGhlIHF1aWNrIGJyb3duIGZveCBqdW1wcyBvdmVyIHRoZSBsYXp5IGRvZw==",
			"The quick brown fox jumps over the lazy dog"},
		// 'K' (0x4B) and 'k' (0x6B) share lo-nibble 0xB with '+' (0x2B);
		// a correctionLUT indexed by lo-nibble would corrupt these characters.
		{"S2VlcA==", "Keep"},
		{"a2VlcA==", "keep"},
	}

	allPass := true
	for _, tc := range testCases {
		// Verify scalar reference first.
		scalarResult, scalarOK := scalarDecode([]byte(tc.input))
		if !scalarOK {
			fmt.Printf("FAIL scalar decode error: '%s'\n", tc.input)
			allPass = false
			continue
		}
		if string(scalarResult) != tc.expected {
			fmt.Printf("FAIL scalar: '%s' -> got '%s', want '%s'\n",
				tc.input, string(scalarResult), tc.expected)
			allPass = false
			continue
		}

		// Verify SPMD decoder matches scalar.
		spmdResult, spmdOK := spmdDecode([]byte(tc.input))
		if !spmdOK {
			fmt.Printf("FAIL spmd decode error: '%s'\n", tc.input)
			allPass = false
			continue
		}
		if string(spmdResult) != tc.expected {
			fmt.Printf("FAIL spmd: '%s' -> got '%s', want '%s'\n",
				tc.input, string(spmdResult), tc.expected)
			allPass = false
		}
	}

	if allPass {
		fmt.Println("Correctness: PASS")
	} else {
		fmt.Println("Correctness: FAIL")
	}
}
