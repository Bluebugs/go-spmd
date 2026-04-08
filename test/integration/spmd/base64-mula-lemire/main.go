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

// decodeHotSPMD fills sextets[i] = ASCII-to-sextet(src[i]) for each byte in src.
// src must be padded to a SIMD-width multiple (32 bytes for AVX2) by the caller
// so that inactive tail lanes always address valid memory.
// Packing sextets into output bytes is done by the caller using the real
// (unpadded) group count, not len(src)/4.
func decodeHotSPMD(sextets, src []byte) {
	// SPMD: each lane handles one byte. decodeLUT[ch>>4] compiles to vpshufb
	// (x86) or i8x16.swizzle (WASM), eliminating scatter-gather.
	go for i, ch := range src {
		// Nibble-LUT: sextet = ch + decodeLUT[ch>>4].
		// '+' (0x2B) shares hi-nibble 2 with '/' but needs offset 19 vs 16;
		// the conditional adds 3 and compiles to a branchless SPMD select.
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
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
	// hotGroups excludes the last quartet when it contains padding, so the
	// SPMD loop never sees '=' characters.
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}
	hotBytes := hotGroups * 4 // number of valid src bytes for the SPMD loop

	// Extra capacity avoids bounds panics when the scalar pack loop reads
	// sextets up to the hotBytes boundary.
	dst := make([]byte, groups*3+16)

	// Pre-allocate the sextets buffer so decodeHotSPMD avoids allocation in
	// the hot path.  Size matches hotSrc length (padded below).
	//
	// Pad hotSrc to a multiple of 32 bytes so inactive lanes in the SPMD tail
	// iteration always access valid memory on all targets (WASM: 16 lanes,
	// x86 SSE: 16 lanes, x86 AVX2: 32 lanes). range-over-slice elides bounds
	// checks only within the slice bounds; the pad region keeps indices valid.
	// Padding with 'A' (sextet 0) is safe: inactive-lane mask suppresses
	// writes, and the scalar pack loop only reads sextets[0..hotBytes-1].
	padded := hotBytes
	if rem := padded % 32; rem != 0 {
		padded += 32 - rem
	}

	var hotSrc []byte
	if padded <= len(src) {
		hotSrc = src[:padded]
	} else {
		hotSrc = make([]byte, padded)
		copy(hotSrc, src[:hotBytes])
		for i := hotBytes; i < padded; i++ {
			hotSrc[i] = 'A' // valid base64 character (sextet 0)
		}
	}

	// Run SPMD decode: fills sextets[0..hotBytes-1] with 6-bit values.
	// Only the first hotBytes entries are valid; the padded tail holds zeros
	// from 'A' padding and is ignored by the pack loop below.
	sextets := make([]byte, padded)
	decodeHotSPMD(sextets, hotSrc)

	// Pack hotGroups sextets into bytes (Step 2, scalar).
	// Use hotGroups (not len(hotSrc)/4) so we never read padding-region sextets
	// into dst, which would overflow dst (allocated for groups*3+16 bytes).
	for g := 0; g < hotGroups; g++ {
		s0 := sextets[g*4+0]
		s1 := sextets[g*4+1]
		s2 := sextets[g*4+2]
		s3 := sextets[g*4+3]
		dst[g*3+0] = (s0 << 2) | (s1 >> 4)
		dst[g*3+1] = (s1 << 4) | (s2 >> 2)
		dst[g*3+2] = (s2 << 6) | s3
	}

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
