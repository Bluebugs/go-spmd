// run -goexperiment spmd

// Mula-Lemire base64 decoder using nibble-LUT (vpshufb) approach.
// The key insight: byte-array indexing with a varying index (lut[varyingByte])
// compiles to vpshufb (x86) / i8x16.swizzle (WASM) via the byte-array fast path.
// This eliminates the 256-entry scatter-gather table used in the original decoder.
//
// Algorithm: sextet = ch + decodeLUT[ch>>4], with an extra +3 for '+' (0x2B).
package main

import "fmt"

// Nibble-LUT decode table, indexed by the high nibble of the ASCII character.
//
// '+' (0x2B, sextet 62) and '/' (0x2F, sextet 63) share hi-nibble 2.
// decodeLUT[2] = 16 is correct for '/' (0x2F + 16 = 63).
// '+' needs offset 19, so we add 3 via a varying conditional (SPMD select).
// A correctionLUT indexed by lo-nibble would also corrupt 'K' (0x4B) and
// 'k' (0x6B) which share lo-nibble 0xB — hence the per-character branch.
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

// decodeHotSPMD decodes pre-validated base64 using nibble-LUT (vpshufb).
// src must have length divisible by 4 and contain no padding characters.
// dst must have capacity for at least len(src)/4*3 bytes.
//
// Each decodeLUT[c>>4] with a varying byte index compiles to vpshufb (x86)
// or i8x16.swizzle (WASM) via the byte-array indexing fast path, eliminating
// the 256-entry scatter-gather table of the original decoder.
func decodeHotSPMD(dst, src []byte) {
	groups := len(src) / 4

	go for g := range groups {
		// Load 4 bytes for this quartet.
		c0 := src[g*4+0]
		c1 := src[g*4+1]
		c2 := src[g*4+2]
		c3 := src[g*4+3]

		// Nibble-LUT decode: each lookup compiles to vpshufb / i8x16.swizzle.
		// ASCII + offset = sextet; byte wrapping (no-op for valid sextets 0-63).
		// '+' (0x2B) needs offset 19 but decodeLUT[2]=16 (correct for '/'),
		// so add 3 when ch == '+' — this compiles to a SPMD select instruction.
		s0 := c0 + decodeLUT[c0>>4]
		if c0 == byte('+') {
			s0 += 3
		}
		s1 := c1 + decodeLUT[c1>>4]
		if c1 == byte('+') {
			s1 += 3
		}
		s2 := c2 + decodeLUT[c2>>4]
		if c2 == byte('+') {
			s2 += 3
		}
		s3 := c3 + decodeLUT[c3>>4]
		if c3 == byte('+') {
			s3 += 3
		}

		// Pack 4×6-bit sextets → 3×8-bit bytes.
		dst[g*3+0] = (s0 << 2) | (s1 >> 4)
		dst[g*3+1] = (s1 << 4) | (s2 >> 2)
		dst[g*3+2] = (s2 << 6) | s3
	}
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

	// Count padding characters at the end.
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

// spmdDecode decodes base64 using the SPMD nibble-LUT hot loop with scalar
// fallback for the padding quartet.
func spmdDecode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}

	// Count padding characters at the end.
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	// hotGroups excludes the last quartet if it contains padding.
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}

	// Extra capacity avoids bounds panics when SIMD writes up to laneCount-1
	// bytes past the end of the logical output in the tail group.
	dst := make([]byte, groups*3+16)

	// Pad hotSrc to a multiple of 16 bytes so inactive lanes in the SPMD tail
	// pass always access valid memory. The SPMD compiler elides bounds checks
	// only for contiguous range-over-slice loops; range-over-int with computed
	// indices still bounds-checks inactive lanes. Padding with 'A' (sextet 0)
	// keeps the values valid and produces zero bits that the inactive-lane mask
	// suppresses before any store.
	padded := hotGroups * 4
	if rem := padded % 16; rem != 0 {
		padded += 16 - rem
	}
	var hotSrc []byte
	if padded <= len(src) {
		hotSrc = src[:padded]
	} else {
		hotSrc = make([]byte, padded)
		copy(hotSrc, src[:hotGroups*4])
		for i := hotGroups * 4; i < padded; i++ {
			hotSrc[i] = 'A' // valid base64 sextet (value 0)
		}
	}
	decodeHotSPMD(dst, hotSrc)

	// Handle the padding quartet with scalar fallback.
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
		dst[hotGroups*3+0] = (c0 << 2) | (c1 >> 4)
		dst[hotGroups*3+1] = (c1 << 4) | (c2 >> 2)
		dst[hotGroups*3+2] = (c2 << 6) | c3
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
		// Longer input to exercise multiple SIMD lanes.
		{"VGhlIHF1aWNrIGJyb3duIGZveCBqdW1wcyBvdmVyIHRoZSBsYXp5IGRvZw==",
			"The quick brown fox jumps over the lazy dog"},
		// 'K' (0x4B) and 'k' (0x6B) share lo-nibble 0xB with '+' (0x2B).
		// A correctionLUT indexed by lo-nibble would corrupt these characters.
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
