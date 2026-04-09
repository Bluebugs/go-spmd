// run -goexperiment spmd

// Mula-Lemire base64 decoder using nibble-LUT (vpshufb) approach.
// The key insight: byte-array indexing with a varying index (lut[varyingByte])
// compiles to vpshufb (x86) / i8x16.swizzle (WASM) via the byte-array fast path.
// This eliminates the 256-entry scatter-gather table used in the original decoder.
//
// Algorithm: sextet = ch + decodeLUT[ch>>4], with an extra +3 for '+' (0x2B).
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

// decodeHotSPMD decodes base64 source bytes directly to output using SPMD.
// Returns the number of bytes written to dst (== len(src)*3/4).
// src must have length that is a multiple of 4 (whole groups only, no padding
// characters). The caller must ensure the backing array of src extends at least
// laneCount bytes past len(src) so that inactive tail-iteration lane reads stay
// within valid memory; the caller pads the backing array with 'A' for this.
func decodeHotSPMD(dst, src []byte) int {
	offset := 0
	go for i, ch := range src {
		// Step 1: ASCII → 6-bit sextet (nibble LUT via swizzle).
		// '+' (0x2B) shares hi-nibble 2 with '/' but needs offset 19 vs 16;
		// the conditional adds 3 and compiles to a branchless SPMD select.
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}

		// Step 2: Pack 4→3 using cross-lane access + CompactStore.
		// lanes.Rotate(s, +1) fetches the next lane's value for packing:
		// Rotate(<s0,s1,s2,s3>, +1) => <s1,s2,s3,s0>, so lane i gets s[i+1].
		// At SIMD register boundaries, lane 3's "next" wraps to s[0] (garbage
		// for this group), but pos==3 masks it off via CompactStore so it is
		// never written.
		next := lanes.Rotate(s, 1)
		pos := i % 4

		// Compute packing output for positions 0, 1, 2 (position 3 is suppressed
		// by CompactStore mask). Each expression is varying; the SPMD compiler
		// selects the correct one per-lane based on the varying pos condition.
		out0 := (s << 2) | (next >> 4)
		out1 := (s << 4) | (next >> 2)
		out2 := (s << 6) | next

		// Merge: select the right value per lane based on pos.
		out12 := out1
		if pos == 2 {
			out12 = out2
		}
		out := out0
		if pos != 0 {
			out = out12
		}

		n := lanes.CompactStore(dst[offset:], out, pos != 3)
		offset += n
	}
	return offset
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

	dst := make([]byte, groups*3)

	// Allocate hotSrc with at least 32 bytes of backing beyond hotBytes so that
	// inactive tail-iteration lanes can read without faulting on any target
	// (WASM: 16 byte-lanes, x86 SSE: 16 byte-lanes, x86 AVX2: 32 byte-lanes).
	// The slice passed to decodeHotSPMD is hotSrc[:hotBytes] (not padded length)
	// so the go-for iterates exactly hotBytes times; the SPMD tail mask
	// (i < hotBytes) suppresses CompactStore writes for inactive tail lanes.
	// Padding bytes are filled with 'A' (sextet 0); they are read but never
	// written due to the tail mask.
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

	// Run combined SPMD decode: lookup + pack + compact-store in one pass.
	// Pass hotSrc[:hotBytes] so the go-for iterates exactly hotBytes times; the
	// tail mask (i < hotBytes) suppresses CompactStore for inactive tail lanes
	// while hotSrc's padded backing ensures those lanes read valid memory.
	// Returns the number of bytes written into dst (== hotGroups*3).
	written := decodeHotSPMD(dst, hotSrc[:hotBytes])
	_ = written // hotGroups*3 bytes written; padding quartet appended below

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
