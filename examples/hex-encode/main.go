// Hexadecimal encoding benchmark: SPMD vs Scalar
// From: practical-vector.md
package main

import (
	"fmt"
	"os"
	"time"
)

const hextable = "0123456789abcdef"
const dataSize = 1024
const iterations = 1000
const rounds = 5

func main() {
	// Generate pseudo-random test data (deterministic)
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xFF)
	}
	dst1 := make([]byte, len(data)*2)
	dst2 := make([]byte, len(data)*2)
	dst3 := make([]byte, len(data)*2)

	// Correctness check: all three must match
	EncodeScalar(dst2, data)

	Encode(dst1, data)
	if string(dst1) != string(dst2) {
		fmt.Println("FAIL: Encode (dst-centric) and Scalar results mismatch!")
		os.Exit(1)
	}

	EncodeSrc(dst3, data)
	if string(dst3) != string(dst2) {
		fmt.Println("FAIL: EncodeSrc (src-centric) and Scalar results mismatch!")
		os.Exit(1)
	}
	fmt.Println("Correctness: all three implementations match.")

	// Run multiple benchmark rounds for stable results
	for round := 0; round < rounds; round++ {
		// Benchmark scalar
		start := time.Now()
		for n := 0; n < iterations; n++ {
			EncodeScalar(dst2, data)
		}
		scalarTime := time.Since(start)

		// Benchmark SPMD (dst-centric, original)
		start = time.Now()
		for n := 0; n < iterations; n++ {
			Encode(dst1, data)
		}
		spmdDstTime := time.Since(start)

		// Benchmark SPMD (src-centric, new)
		start = time.Now()
		for n := 0; n < iterations; n++ {
			EncodeSrc(dst3, data)
		}
		spmdSrcTime := time.Since(start)

		// Speedups
		dstSpeedup := float64(0)
		if spmdDstTime > 0 {
			dstSpeedup = float64(scalarTime) / float64(spmdDstTime)
		}
		srcSpeedup := float64(0)
		if spmdSrcTime > 0 {
			srcSpeedup = float64(scalarTime) / float64(spmdSrcTime)
		}
		fmt.Printf("Round %d: Scalar=%v DstSPMD=%v (%.2fx) SrcSPMD=%v (%.2fx)\n",
			round+1, scalarTime, spmdDstTime, dstSpeedup, spmdSrcTime, srcSpeedup)
	}
}

func Encode(dst, src []byte) int {
	go for i := range dst {
		v := src[i>>1]
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
		} else {
			dst[i] = hextable[v&0x0f]
		}
	}

	return len(src) * 2
}

func EncodeSrc(dst, src []byte) int {
	go for i := range src {
		dst[i*2] = hextable[src[i]>>4]
		dst[i*2+1] = hextable[src[i]&0x0f]
	}

	return len(src) * 2
}

func EncodeScalar(dst, src []byte) int {
	j := 0
	for _, v := range src {
		dst[j] = hextable[v>>4]
		dst[j+1] = hextable[v&0x0f]
		j += 2
	}

	return len(src) * 2
}
