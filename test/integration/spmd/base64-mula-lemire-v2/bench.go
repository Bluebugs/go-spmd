// run -goexperiment spmd
package main

import (
	"fmt"
	"lanes"
	"time"
)

func encodeBase64Bench(raw []byte) []byte {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	n := len(raw)
	out := make([]byte, ((n+2)/3)*4)
	j := 0
	for i := 0; i+3 <= n; i += 3 {
		v := uint(raw[i])<<16 | uint(raw[i+1])<<8 | uint(raw[i+2])
		out[j] = chars[v>>18&0x3F]
		out[j+1] = chars[v>>12&0x3F]
		out[j+2] = chars[v>>6&0x3F]
		out[j+3] = chars[v&0x3F]
		j += 4
	}
	return out
}

var benchDecodeLUT = [16]byte{
	0, 0, 16, 4, 191, 191, 185, 185,
	0, 0, 0, 0, 0, 0, 0, 0,
}

func benchDecodeAndPack(dst, src []byte) int {
	n := len(src)
	sextets := make([]byte, n)
	go for i, ch := range src {
		s := ch + benchDecodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
	}
	halfLen := n / 2
	merged := make([]int16, halfLen)
	go for g := range merged {
		merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
	}
	quarterLen := halfLen / 2
	packed := make([]int32, quarterLen)
	go for g := range packed {
		packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
	}
	go for g := range packed {
		dst[g*3+0] = byte(packed[g] >> 16)
		dst[g*3+1] = byte(packed[g] >> 8)
		dst[g*3+2] = byte(packed[g])
	}
	return quarterLen * 3
}

func main() {
	raw := make([]byte, 1024*1024)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	encoded := encodeBase64Bench(raw)

	var bv lanes.Varying[byte]
	chunkSize := lanes.Count[byte](bv)
	hotBytes := (len(encoded) / 4) * 4
	padded := hotBytes
	if rem := padded % chunkSize; rem != 0 {
		padded += chunkSize - rem
	}
	hotSrc := make([]byte, padded)
	copy(hotSrc, encoded[:hotBytes])
	for i := hotBytes; i < padded; i++ {
		hotSrc[i] = 'A'
	}
	dst := make([]byte, padded+64)

	iters := 10

	// Warmup
	for w := 0; w < 3; w++ {
		outOff := 0
		for off := 0; off+chunkSize <= padded; off += chunkSize {
			n := benchDecodeAndPack(dst[outOff:], hotSrc[off:off+chunkSize])
			outOff += n
		}
	}

	start := time.Now()
	for iter := 0; iter < iters; iter++ {
		outOff := 0
		for off := 0; off+chunkSize <= padded; off += chunkSize {
			n := benchDecodeAndPack(dst[outOff:], hotSrc[off:off+chunkSize])
			outOff += n
		}
	}
	elapsed := time.Since(start)

	bytesPerSec := float64(len(encoded)) * float64(iters) / elapsed.Seconds()
	fmt.Printf("=== Base64 v2 (pmaddubsw, chunkSize=%d) @ 1MB ===\n", chunkSize)
	fmt.Printf("  %.0f MB/s  (%v)\n", bytesPerSec/1e6, elapsed)
}
