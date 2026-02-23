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

func main() {
	// Generate pseudo-random test data (deterministic)
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i*31 + 17) & 0xFF)
	}
	dst1 := make([]byte, len(data)*2)
	dst2 := make([]byte, len(data)*2)

	// Correctness check
	Encode(dst1, data)
	EncodeScalar(dst2, data)
	if string(dst1) != string(dst2) {
		fmt.Println("FAIL: SPMD and Scalar results mismatch!")
		os.Exit(1)
	}
	fmt.Println("Correctness: SPMD and Scalar results match.")

	// Benchmark scalar
	start := time.Now()
	for n := 0; n < iterations; n++ {
		EncodeScalar(dst2, data)
	}
	scalarTime := time.Since(start)
	fmt.Printf("Scalar: %v (%d iterations, %d bytes each)\n", scalarTime, iterations, dataSize)

	// Benchmark SPMD
	start = time.Now()
	for n := 0; n < iterations; n++ {
		Encode(dst1, data)
	}
	spmdTime := time.Since(start)
	fmt.Printf("SPMD:   %v (%d iterations, %d bytes each)\n", spmdTime, iterations, dataSize)

	// Speedup
	if spmdTime > 0 {
		speedup := float64(scalarTime) / float64(spmdTime)
		fmt.Printf("Speedup: %.2fx\n", speedup)
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

func EncodeScalar(dst, src []byte) int {
	j := 0
	for _, v := range src {
		dst[j] = hextable[v>>4]
		dst[j+1] = hextable[v&0x0f]
		j += 2
	}

	return len(src) * 2
}
