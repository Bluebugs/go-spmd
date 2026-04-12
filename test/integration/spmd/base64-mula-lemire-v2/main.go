// run -goexperiment spmd

// Mula-Lemire base64 decoder v2: uses pmaddubsw/pmaddwd packing.
// The key difference from v1: instead of shift/OR + CompactStore,
// this version uses three cascading go-for loops at decreasing
// SIMD widths (byte -> int16 -> int32) to trigger pmaddubsw and
// pmaddwd pattern detection.
//
// Algorithm:
//
//	Step 1 (decodeSextets): go for over []byte  -> vpshufb sextet decode
//	Step 2 (packChunk):     go for over []int16 -> pmaddubsw (a*64+b)
//	Step 3 (packChunk):     go for over []int32 -> pmaddwd   (a*4096+b)
//	Step 4 (packChunk):     go for over []int32 -> extract 3 bytes per int32
//
// No Rotate, no SPMDMux, no CompactStore.
package main

import (
	"fmt"
	"lanes"
)

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

// decodeSextets fills sextets[] with 6-bit values decoded from base64 src bytes.
// Uses a nibble-LUT go-for loop (byte-width SIMD, e.g. 16 lanes on SSE, 32 on AVX2)
// that compiles to vpshufb on x86 / i8x16.swizzle on WASM.
// '+' (0x2B) shares hi-nibble 2 with '/' but needs offset 19 vs 16; the conditional
// adds 3 and compiles to a branchless SPMD select.
func decodeSextets(sextets, src []byte) {
	go for i, ch := range src {
		_ = i
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
	}
}

// packChunk packs sextets into output bytes using multiply-add cascades.
//
// The three go-for loops target decreasing element widths:
//
//	Loop A ([]int16, 8 lanes on SSE, 16 on AVX2):
//	  merged[g] = sextets[g*2]*64 + sextets[g*2+1]
//	  Pattern: a*64+b  =>  pmaddubsw (multiply unsigned bytes, add adjacent pairs)
//
//	Loop B ([]int32, 4 lanes on SSE, 8 on AVX2):
//	  packed[g] = merged[g*2]*4096 + merged[g*2+1]
//	  Pattern: a*4096+b  =>  pmaddwd (multiply signed int16s, add adjacent pairs)
//	  Result: packed[g] = (s0<<18)|(s1<<12)|(s2<<6)|s3
//
//	Loop C ([]int32, same width as B):
//	  Extracts the 3 output bytes from each packed int32:
//	    byte0 = packed[g]>>16   (bits 23-16)
//	    byte1 = packed[g]>>8    (bits 15-8)
//	    byte2 = packed[g]       (bits  7-0)
//
// sextets length must be a multiple of 4.
// Returns the number of bytes written to dst (== len(sextets)*3/4).
func packChunk(dst []byte, sextets []byte) int {
	// Loop A: merge adjacent sextet pairs into int16 via pmaddubsw pattern.
	halfLen := len(sextets) / 2
	merged := make([]int16, halfLen)
	go for g := range merged {
		a := int16(sextets[g*2])
		b := int16(sextets[g*2+1])
		// a*64 + b: multiply-add of two unsigned bytes -> int16
		// Matches the pmaddubsw multiplier vector [64, 1, 64, 1, ...].
		merged[g] = a*64 + b
	}

	// Loop B: merge adjacent int16 pairs into int32 via pmaddwd pattern.
	quarterLen := halfLen / 2
	packed := make([]int32, quarterLen)
	go for g := range packed {
		a := int32(merged[g*2])
		b := int32(merged[g*2+1])
		// a*4096 + b: multiply-add of two signed int16s -> int32
		// Matches the pmaddwd multiplier vector [4096, 1, 4096, 1, ...].
		// Result: (s0<<18)|(s1<<12)|(s2<<6)|s3
		packed[g] = a*4096 + b
	}

	// Loop C: extract 3 output bytes per int32.
	// packed[g] = (s0<<18)|(s1<<12)|(s2<<6)|s3
	//   dst[g*3+0] = bits 23-16 (byte0 = packed[g]>>16 & 0xFF)
	//   dst[g*3+1] = bits 15-8  (byte1 = packed[g]>>8  & 0xFF)
	//   dst[g*3+2] = bits  7-0  (byte2 = packed[g]     & 0xFF)
	// The stride-3 stores may produce interleaved or scalar stores depending
	// on compiler support; correctness is guaranteed regardless.
	go for g := range packed {
		dst[g*3+0] = byte(packed[g] >> 16)
		dst[g*3+1] = byte(packed[g] >> 8)
		dst[g*3+2] = byte(packed[g])
	}
	return quarterLen * 3
}

// spmdDecode decodes a base64-encoded src using SPMD sextet decode +
// pmaddubsw/pmaddwd packing. Returns the decoded bytes and a success flag.
func spmdDecode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}

	// Count trailing padding characters.
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	// hotGroups excludes the last quartet when it contains padding, so the
	// SPMD loops never see '=' characters.
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}
	hotBytes := hotGroups * 4

	// Extra slack for SIMD overwrite: Loop C writes stride-3 into dst but
	// the go-for bounds cover quarterLen groups, so total written is hotGroups*3.
	// Allocate groups*3+32 to give headroom for any SIMD over-store.
	dst := make([]byte, groups*3+32)

	// Pad hotSrc to a multiple of 32 bytes for AVX2 (32 byte-lanes) compatibility.
	// Inactive tail lanes may read the padding but will never write output due to
	// the tail mask applied by the SPMD loop lowering pass.
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
		// Fill padding with 'A' (sextet 0) so inactive lane reads are benign.
		for i := hotBytes; i < padded; i++ {
			hotSrc[i] = 'A'
		}
	}

	// Step 1: decode ASCII characters to 6-bit sextets.
	sextets := make([]byte, padded)
	decodeSextets(sextets, hotSrc)

	// Steps 2-4: pack sextets -> int16 -> int32 -> output bytes.
	// Pass the full padded sextets slice (length = multiple of 32) so that the
	// cascading go-for loops (byte->int16->int32) never encounter a tail partial
	// register: all three loops see lengths that are exact multiples of their
	// respective lane counts.  The extra groups beyond hotGroups decode from 'A'
	// (sextet 0) into harmless zeros that land in the slack area of dst.
	packChunk(dst, sextets)
	written := hotGroups * 3

	// Handle the optional padding quartet with a scalar fallback.
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
		dst[written+0] = (c0 << 2) | (c1 >> 4)
		dst[written+1] = (c1 << 4) | (c2 >> 2)
		dst[written+2] = (c2 << 6) | c3
		written += 3
	}

	return dst[:written-padCount], true
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

// verifyLaneCount prints the effective SIMD widths to confirm the compiler
// targeted the expected element types. This is informational only.
func verifyLaneCount() {
	var b byte
	var s int16
	var i int32
	fmt.Printf("Lane counts: byte=%d int16=%d int32=%d\n",
		lanes.Count[byte](b),
		lanes.Count[int16](s),
		lanes.Count[int32](i),
	)
}

func main() {
	verifyLaneCount()

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
