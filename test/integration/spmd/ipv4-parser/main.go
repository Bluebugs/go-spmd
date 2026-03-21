// IPv4 parser using SPMD Go
// From: go-spmd-ipv4-parser.md
// Based on: Wojciech Muła's SIMD IPv4 parsing research
package main

import (
	"fmt"
	"math/bits"
	"time"

	"lanes"
	"reduce"
)

// lemireEntry packs four fields into a single uint32 for a single i32.load:
//
//	bits  0-15: expectedMask — the 16-bit dot bitmask this entry was built for
//	bits 16-23: code3x3     — (l0-1)*9+(l1-1)*3+(l2-1)) * 3, precomputed to
//	                          avoid a runtime multiply when indexing shuffleTable
//	bits 24-31: startF3     — start position of field 3 = p3+1
//
// A zero entry (expectedMask==0) indicates an unused table slot; no valid
// dotBitmask can be zero since it always has exactly 3 bits set.
type lemireEntry uint32

func (e lemireEntry) expectedMask() uint16 { return uint16(e & 0xFFFF) }

// code3x3 returns (l0-1)*9+(l1-1)*3+(l2-1) already multiplied by 3,
// ready to add (l3-1) to get the final shuffleTable/flensTable index.
func (e lemireEntry) code3x3() int { return int((e >> 16) & 0xFF) }
func (e lemireEntry) startF3() int { return int(e >> 24) }

// lemireTable is indexed by the Lemire compact hash of the 16-bit dotBitmask:
//
//	hash = (dotBitmask >> 5) ^ (dotBitmask & 0x03ff)
//
// The hash maps into [0, 2047], replacing the separate dotCodeTable (4096 B)
// and shuffleTable (81×16 = 1296 B) with a single 2048-entry table.
// Collision validation is done by comparing entry.expectedMask == dotBitmask.
var lemireTable [2048]lemireEntry

// shuffleTable holds all 81 complete 16-byte swizzle masks (27 l0/l1/l2
// combinations × 3 l3 variants).  Index = code3*3 + (l3-1).
var shuffleTable [81][16]byte

// flensTable holds all 81 precomputed [l0, l1, l2, l3] field-length arrays.
// Index = code3*3 + (l3-1), same as shuffleTable.
// Use int so varying indexing matches the original [4]int{l0,l1,l2,l3} pattern.
var flensTable [81][4]int

func init() {
	// Iterate all 81 combinations of (l0, l1, l2, l3) to precompute complete
	// shuffle masks and field-length arrays, eliminating runtime patching.
	for l0 := 1; l0 <= 3; l0++ {
		for l1 := 1; l1 <= 3; l1++ {
			for l2 := 1; l2 <= 3; l2++ {
				p1 := l0
				p2 := l0 + l1 + 1
				p3 := l0 + l1 + l2 + 2
				dotBitmask := uint16(1<<p1) | uint16(1<<p2) | uint16(1<<p3)
				h := (dotBitmask >> 5) ^ (dotBitmask & 0x03ff)
				code3 := uint8((l0-1)*9 + (l1-1)*3 + (l2-1))

				starts := [4]int{0, l0 + 1, l0 + l1 + 2, l0 + l1 + l2 + 3}

				for l3 := 1; l3 <= 3; l3++ {
					idx := int(code3)*3 + (l3 - 1)
					lens := [4]int{l0, l1, l2, l3}

					var sm [16]byte
					for f := 0; f < 4; f++ {
						sf, lf := starts[f], lens[f]
						switch lf {
						case 3:
							sm[f*4+0] = byte(sf)
							sm[f*4+1] = byte(sf + 1)
							sm[f*4+2] = byte(sf + 2)
						case 2:
							sm[f*4+0] = 0xFF
							sm[f*4+1] = byte(sf)
							sm[f*4+2] = byte(sf + 1)
						case 1:
							sm[f*4+0] = 0xFF
							sm[f*4+1] = 0xFF
							sm[f*4+2] = byte(sf)
						}
						sm[f*4+3] = 0xFF
					}
					shuffleTable[idx] = sm
					flensTable[idx] = [4]int{l0, l1, l2, l3}
				}

				// Pack into uint32: expectedMask | (code3*3)<<16 | startF3<<24.
				// Precomputing code3*3 eliminates the multiply in the hot path.
				lemireTable[h] = lemireEntry(uint32(dotBitmask) | uint32(code3)*3<<16 | uint32(p3+1)<<24)
			}
		}
	}
}

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

const iterations = 10000

const (
	WARMUP_RUNS = 3
	BENCH_RUNS  = 7
)

var testCases = []string{
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

func main() {
	for _, addr := range testCases {
		ip, err := parseIPv4(addr)
		if err != nil {
			fmt.Printf("'%s' -> ERROR: %v\n", addr, err)
		} else {
			fmt.Printf("'%s' -> %d.%d.%d.%d\n", addr, ip[0], ip[1], ip[2], ip[3])
		}
	}

	benchmark()
}

func benchmark() {
	fmt.Println("\n=== IPv4 Parser SPMD Benchmark ===")
	fmt.Printf("Test cases: %d, Iterations: %d per run\n", len(testCases), iterations)
	fmt.Printf("Warmup: %d runs, Bench: %d runs\n\n", WARMUP_RUNS, BENCH_RUNS)

	// Correctness check: scalar and SPMD must agree on every test case.
	fmt.Println("Verifying correctness...")
	for _, addr := range testCases {
		spmdIP, spmdErr := parseIPv4(addr)
		scalarIP, scalarErr := parseIPv4Scalar(addr)
		// Both must error or both must succeed.
		if (spmdErr == nil) != (scalarErr == nil) {
			fmt.Printf("FAIL: mismatch for %q: SPMD=%v scalar=%v\n", addr, spmdErr, scalarErr)
			return
		}
		// On success the byte values must match.
		if spmdErr == nil && spmdIP != scalarIP {
			fmt.Printf("FAIL: result mismatch for %q: SPMD=%v scalar=%v\n", addr, spmdIP, scalarIP)
			return
		}
	}
	fmt.Println("Correctness: SPMD and scalar results match.")

	// Warmup (not timed).
	fmt.Println("Warming up...")
	for r := 0; r < WARMUP_RUNS; r++ {
		for n := 0; n < iterations; n++ {
			for _, addr := range testCases {
				parseIPv4Scalar(addr)
			}
		}
		for n := 0; n < iterations; n++ {
			for _, addr := range testCases {
				parseIPv4(addr)
			}
		}
	}

	// Benchmark scalar.
	fmt.Println("Benchmarking scalar...")
	scalarTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			for _, addr := range testCases {
				parseIPv4Scalar(addr)
			}
		}
		scalarTimes[i] = time.Since(start).Nanoseconds()
	}

	// Benchmark SPMD.
	fmt.Println("Benchmarking SPMD...")
	spmdTimes := make([]int64, BENCH_RUNS)
	for i := 0; i < BENCH_RUNS; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			for _, addr := range testCases {
				parseIPv4(addr)
			}
		}
		spmdTimes[i] = time.Since(start).Nanoseconds()
	}

	// Statistics.
	scalarMin, scalarAvg, scalarMax := stats(scalarTimes)
	spmdMin, spmdAvg, spmdMax := stats(spmdTimes)

	fmt.Println("\n--- Results ---")
	fmt.Printf("Scalar: min=%s  avg=%s  max=%s\n",
		fmtDur(scalarMin), fmtDur(scalarAvg), fmtDur(scalarMax))
	fmt.Printf("SPMD:   min=%s  avg=%s  max=%s\n",
		fmtDur(spmdMin), fmtDur(spmdAvg), fmtDur(spmdMax))

	fmt.Printf("\nSpeedup (avg): %.2fx\n", float64(scalarAvg)/float64(spmdAvg))
	fmt.Printf("Speedup (min): %.2fx\n", float64(scalarMin)/float64(spmdMin))

	fmt.Println("\n--- Per-run times ---")
	fmt.Println("Run  Scalar        SPMD          Ratio")
	for i := 0; i < BENCH_RUNS; i++ {
		ratio := float64(scalarTimes[i]) / float64(spmdTimes[i])
		fmt.Printf("%2d   %-13s %-13s %.2fx\n",
			i+1, fmtDur(scalarTimes[i]), fmtDur(spmdTimes[i]), ratio)
	}
}

func stats(times []int64) (min, avg, max int64) {
	min = times[0]
	max = times[0]
	var sum int64
	for _, t := range times {
		sum += t
		if t < min {
			min = t
		}
		if t > max {
			max = t
		}
	}
	avg = sum / int64(len(times))
	return
}

func fmtDur(ns int64) string {
	if ns < 1000 {
		return fmt.Sprintf("%dns", ns)
	}
	if ns < 1000000 {
		return fmt.Sprintf("%.1fus", float64(ns)/1000)
	}
	return fmt.Sprintf("%.3fms", float64(ns)/1000000)
}

// parseIPv4Scalar parses an IPv4 address string using plain serial Go.
// It enforces the same rules as parseIPv4: no leading zeros, values 0-255,
// exactly three dots, valid digit/dot characters only.
func parseIPv4Scalar(s string) ([4]byte, error) {
	if len(s) < 7 || len(s) > 15 {
		return [4]byte{}, parseAddrError{in: s, msg: "IPv4 address string too short or too long"}
	}

	var ip [4]byte
	field := 0   // current octet index (0-3)
	value := 0   // accumulated decimal value of current octet
	digitCount := 0 // digits seen in current octet

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digit := int(c - '0')
			// Leading-zero check: first digit is 0 and more digits follow in this field.
			if digitCount == 1 && value == 0 {
				return [4]byte{}, parseAddrError{in: s, msg: "IPv4 field has octet with leading zero"}
			}
			value = value*10 + digit
			digitCount++
			if value > 255 {
				return [4]byte{}, parseAddrError{in: s, msg: "IPv4 field has value >255"}
			}
		case c == '.':
			if field == 3 {
				// Fourth dot means too many octets.
				return [4]byte{}, parseAddrError{in: s, msg: "invalid dot count"}
			}
			if digitCount == 0 {
				return [4]byte{}, parseAddrError{in: s, msg: "invalid field length"}
			}
			ip[field] = byte(value)
			field++
			value = 0
			digitCount = 0
		default:
			return [4]byte{}, parseAddrError{in: s, at: i, msg: "unexpected character"}
		}
	}

	// Flush the last field.
	if digitCount == 0 {
		return [4]byte{}, parseAddrError{in: s, msg: "invalid field length"}
	}
	if field != 3 {
		return [4]byte{}, parseAddrError{in: s, msg: "invalid dot count"}
	}
	ip[field] = byte(value)

	return ip, nil
}

//go:noinline
func parseIPv4Inner(s string) (ip [4]byte, errCode uint8, errAt int) {
	if len(s) < 7 || len(s) > 15 {
		return [4]byte{}, 1, 0
	}

	// Pad string to 16 bytes with null terminators (like SSE register).
	input := [16]byte{}
	copy(input[:], s)

	// Loop 1: classify every byte in parallel, build dot bitmask and prepare digits computation.
	var dotBitmask uint16
	var loop int
	var digits [16]byte

	go for i, c := range input {
		isDot := c == '.'
		digitMask := (c >= '0' && c <= '9')

		// Use a lane-index bound check instead of a null-byte check.
		// Both approaches mark padding positions as valid; the index form is
		// explicit about intent and does not depend on the zero-fill value.
		pastEnd := i >= len(s)
		validChars := isDot || digitMask || pastEnd

		// Check character validity with precise error location.
		if !reduce.All(validChars) {
			return [4]byte{}, 2, reduce.FindFirstSet(!validChars) + loop
		}

		digits[i] = c - '0'

		// Build dot position bitmask (mimics _mm_movemask_epi8).
		dotBitmask |= uint16(reduce.Mask(isDot)) << loop
		loop += lanes.Count(c)
	}

	// Count dots using popcount on the bitmask.
	dotCount := bits.OnesCount16(dotBitmask)
	if dotCount != 3 {
		// errAt carries the actual count so the wrapper can format the message.
		return [4]byte{}, 3, dotCount
	}

	// Lemire compact hash: maps 16-bit dotBitmask → 11-bit index in [0,2047].
	// Replaces 3× CTZ + dotCodeTable lookup with 2 bitops + 1 table lookup.
	hash := (dotBitmask >> 5) ^ (dotBitmask & 0x03ff)
	entry := lemireTable[hash]
	// Validate: if no valid layout hashes here, expectedMask is zero.
	if entry.expectedMask() != dotBitmask {
		return [4]byte{}, 4, 0
	}
	// Field-3 start is precomputed in the table; l3 is derived from string length.
	sf3 := entry.startF3()
	l3 := len(s) - sf3
	if uint(l3-1) > 2 {
		return [4]byte{}, 4, 0
	}

	// Look up the complete precomputed shuffle mask and field lengths for this
	// (l0, l1, l2, l3) combination — no runtime patching needed.
	// code3x3() already holds code3*3; add (l3-1) to get the final index.
	code := entry.code3x3() + (l3 - 1)
	shuffleMask := shuffleTable[code]
	flens := flensTable[code]

	// Apply the swizzle: gather [h0,t0,o0,0, h1,t1,o1,0, h2,t2,o2,0, h3,t3,o3,0].
	var shuffled [16]byte
	go for i, m := range shuffleMask {
		shuffled[i] = digits[m]
	}

	// Extract columns and validate all four fields in parallel.
	go for field := range 4 {
		h := int(shuffled[field*4+0])
		t := int(shuffled[field*4+1])
		o := int(shuffled[field*4+2])
		flen := flens[field]

		// h*100 + t*10 + o gives the correct decimal value because unused
		// h/t positions were mapped to index 0xFF by the shuffle mask, which
		// causes swizzle to return 0.
		value := h*100 + t*10 + o

		// Leading zero: the first significant digit is zero in a multi-digit field.
		hasLeadingZero := (flen == 2 && t == 0) || (flen == 3 && h == 0)
		if reduce.Any(hasLeadingZero) {
			return [4]byte{}, 5, 0
		}
		if reduce.Any(value > 255) {
			return [4]byte{}, 6, 0
		}
		ip[field] = uint8(value)
	}

	return ip, 0, 0
}

func parseIPv4(s string) ([4]byte, error) {
	ip, code, at := parseIPv4Inner(s)
	if code == 0 {
		return ip, nil
	}
	return [4]byte{}, buildParseError(s, code, at)
}

//go:noinline
func buildParseError(s string, code uint8, at int) error {
	switch code {
	case 1:
		return parseAddrError{in: s, msg: "IPv4 address string too short or too long"}
	case 2:
		return parseAddrError{in: s, at: at, msg: "unexpected character"}
	case 3:
		return parseAddrError{in: s, msg: fmt.Sprintf("invalid dot count: %d", at)}
	case 4:
		return parseAddrError{in: s, msg: "invalid field length"}
	case 5:
		return parseAddrError{in: s, msg: "IPv4 field has octet with leading zero"}
	case 6:
		return parseAddrError{in: s, msg: "IPv4 field has value >255"}
	}
	return nil
}
