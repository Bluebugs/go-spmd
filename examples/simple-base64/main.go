// Base64 decoding benchmark: SPMD vs Scalar
// Demonstrates group-based SPMD iteration with gather/scatter and interleaved stores.
package main

import (
	"fmt"
	"os"
	"time"
)

const b64chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

const dataSize = 1024
const iterations = 1000
const rounds = 5

var b64decode [256]byte

func init() {
	for i := range b64decode {
		b64decode[i] = 0xFF
	}
	for i := 0; i < len(b64chars); i++ {
		b64decode[b64chars[i]] = byte(i)
	}
	b64decode['='] = 0
}

func main() {
	// Generate deterministic test data
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xFF)
	}

	// Encode to base64 (scalar) to get input for decode benchmarks
	encoded := encodeScalar(data)

	// Correctness: verify round-trip
	scalarResult := decodeScalar(encoded)
	if len(scalarResult) != len(data) {
		fmt.Printf("FAIL: scalar round-trip length %d != %d\n", len(scalarResult), len(data))
		os.Exit(1)
	}
	for i := range data {
		if scalarResult[i] != data[i] {
			fmt.Printf("FAIL: scalar round-trip byte %d: got %d expected %d\n", i, scalarResult[i], data[i])
			os.Exit(1)
		}
	}

	spmdResult := decode(encoded)
	if len(spmdResult) != len(data) {
		fmt.Printf("FAIL: SPMD decode length %d != %d\n", len(spmdResult), len(data))
		os.Exit(1)
	}
	for i := range data {
		if spmdResult[i] != data[i] {
			fmt.Printf("FAIL: SPMD decode byte %d: got %d expected %d\n", i, spmdResult[i], data[i])
			os.Exit(1)
		}
	}
	fmt.Println("Correctness: scalar and SPMD decode match.")

	// Benchmark
	for round := 0; round < rounds; round++ {
		// Scalar decode
		start := time.Now()
		for n := 0; n < iterations; n++ {
			decodeScalar(encoded)
		}
		scalarTime := time.Since(start)

		// SPMD decode
		start = time.Now()
		for n := 0; n < iterations; n++ {
			decode(encoded)
		}
		spmdTime := time.Since(start)

		speedup := float64(0)
		if spmdTime > 0 {
			speedup = float64(scalarTime) / float64(spmdTime)
		}
		fmt.Printf("Round %d: Scalar=%v SPMD=%v (%.2fx)\n",
			round+1, scalarTime, spmdTime, speedup)
	}
}

func encodeScalar(src []byte) []byte {
	groups := len(src) / 3
	remainder := len(src) % 3
	outLen := groups * 4
	if remainder > 0 {
		outLen += 4
	}
	buf := make([]byte, outLen)
	for g := 0; g < groups; g++ {
		b0, b1, b2 := src[g*3], src[g*3+1], src[g*3+2]
		buf[g*4+0] = b64chars[b0>>2]
		buf[g*4+1] = b64chars[((b0&0x03)<<4)|(b1>>4)]
		buf[g*4+2] = b64chars[((b1&0x0F)<<2)|(b2>>6)]
		buf[g*4+3] = b64chars[b2&0x3F]
	}
	if remainder == 1 {
		b0 := src[groups*3]
		buf[groups*4+0] = b64chars[b0>>2]
		buf[groups*4+1] = b64chars[(b0&0x03)<<4]
		buf[groups*4+2] = '='
		buf[groups*4+3] = '='
	} else if remainder == 2 {
		b0, b1 := src[groups*3], src[groups*3+1]
		buf[groups*4+0] = b64chars[b0>>2]
		buf[groups*4+1] = b64chars[((b0&0x03)<<4)|(b1>>4)]
		buf[groups*4+2] = b64chars[(b1&0x0F)<<2]
		buf[groups*4+3] = '='
	}
	return buf
}

func decodeScalar(src []byte) []byte {
	groups := len(src) / 4
	if groups == 0 {
		return nil
	}
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if src[len(src)-2] == '=' {
		padCount++
	}
	buf := make([]byte, groups*3)
	for g := 0; g < groups; g++ {
		c0 := b64decode[src[g*4+0]]
		c1 := b64decode[src[g*4+1]]
		c2 := b64decode[src[g*4+2]]
		c3 := b64decode[src[g*4+3]]
		buf[g*3+0] = (c0 << 2) | (c1 >> 4)
		buf[g*3+1] = (c1 << 4) | (c2 >> 2)
		buf[g*3+2] = (c2 << 6) | c3
	}
	return buf[:groups*3-padCount]
}

func decode(src []byte) []byte {
	groups := len(src) / 4
	if groups == 0 {
		return nil
	}
	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if src[len(src)-2] == '=' {
		padCount++
	}

	// Pad src so inactive SPMD lanes (up to 3 beyond groups) don't OOB.
	// SPMD processes 4 groups/iteration (i32, 4 lanes); inactive lanes
	// read harmless padding rather than triggering bounds checks.
	padded := make([]byte, len(src)+4*4)
	copy(padded, src)

	buf := make([]byte, groups*3+3*4)

	go for g := range groups {
		c0 := b64decode[padded[g*4+0]]
		c1 := b64decode[padded[g*4+1]]
		c2 := b64decode[padded[g*4+2]]
		c3 := b64decode[padded[g*4+3]]

		buf[g*3+0] = (c0 << 2) | (c1 >> 4)
		buf[g*3+1] = (c1 << 4) | (c2 >> 2)
		buf[g*3+2] = (c2 << 6) | c3
	}

	return buf[:groups*3-padCount]
}
