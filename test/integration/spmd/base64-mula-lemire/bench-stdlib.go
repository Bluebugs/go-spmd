// bench-stdlib.go
//
// Benchmark for Go stdlib encoding/base64 decode.
// Used to establish a baseline for comparison with SPMD and simdutf.
//
// Build and run:
//
//	go run test/integration/spmd/base64-mula-lemire/bench-stdlib.go
package main

import (
	"encoding/base64"
	"fmt"
	"time"
)

// lcgNext is a simple 32-bit LCG for deterministic, reproducible data.
// Matches the payload generator in bench.go for apples-to-apples comparison.
func lcgNextStdlib(state uint32) uint32 {
	return state*1664525 + 1013904223
}

func makePayloadStdlib(n int) []byte {
	buf := make([]byte, n)
	state := uint32(0xdeadbeef)
	for i := range buf {
		state = lcgNextStdlib(state)
		buf[i] = byte(state >> 24)
	}
	return buf
}

func main() {
	sizes := []struct {
		name string
		raw  int
		// iters tuned so each round is ~50-200ms for stable timing
		iters int
	}{
		{"1KB  ", 1 * 1024, 5000},
		{"10KB ", 10 * 1024, 500},
		{"100KB", 100 * 1024, 50},
		{"1MB  ", 1024 * 1024, 5},
	}

	const warmupRounds = 3
	const benchRounds = 7

	fmt.Println("=== Go stdlib encoding/base64 Decode Benchmark ===")
	fmt.Println()

	for _, sz := range sizes {
		// Round down to multiple of 3 (matches bench.go convention — no '=' padding).
		rawBytes := sz.raw / 3 * 3
		raw := makePayloadStdlib(rawBytes)
		encoded := base64.StdEncoding.EncodeToString(raw)
		encodedBytes := []byte(encoded)
		encLen := len(encodedBytes)

		dst := make([]byte, base64.StdEncoding.DecodedLen(encLen))

		// Warmup.
		for i := 0; i < warmupRounds; i++ {
			for j := 0; j < sz.iters; j++ {
				base64.StdEncoding.Decode(dst, encodedBytes) //nolint:errcheck
			}
		}

		// Timed rounds.
		var minNs int64
		for r := 0; r < benchRounds; r++ {
			start := time.Now()
			for i := 0; i < sz.iters; i++ {
				base64.StdEncoding.Decode(dst, encodedBytes) //nolint:errcheck
			}
			ns := time.Since(start).Nanoseconds()
			if minNs == 0 || ns < minNs {
				minNs = ns
			}
		}

		mbps := float64(int64(encLen)*int64(sz.iters)) / float64(minNs) * 1000.0
		nsPerIter := minNs / int64(sz.iters)
		fmt.Printf("  [%s]  encoded=%dB  raw=%dB  iters=%d\n", sz.name, encLen, rawBytes, sz.iters)
		fmt.Printf("    stdlib: %6.0f MB/s  min=%dns/iter\n", mbps, nsPerIter)
		fmt.Println()
	}
}
