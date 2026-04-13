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

// scalarDecodeBench is the scalar reference for comparison.
func scalarDecodeBench(dst, src []byte) int {
	groups := len(src) / 4
	for g := 0; g < groups; g++ {
		var s [4]byte
		for j := 0; j < 4; j++ {
			ch := src[g*4+j]
			switch {
			case 'A' <= ch && ch <= 'Z':
				s[j] = ch - 'A'
			case 'a' <= ch && ch <= 'z':
				s[j] = ch - 'a' + 26
			case '0' <= ch && ch <= '9':
				s[j] = ch - '0' + 52
			case ch == '+':
				s[j] = 62
			case ch == '/':
				s[j] = 63
			}
		}
		dst[g*3+0] = (s[0] << 2) | (s[1] >> 4)
		dst[g*3+1] = (s[1] << 4) | (s[2] >> 2)
		dst[g*3+2] = (s[2] << 6) | s[3]
	}
	return groups * 3
}

func main() {
	var bv lanes.Varying[byte]
	chunkSize := lanes.Count[byte](bv)

	sizes := []struct {
		name string
		raw  int
	}{
		{"1KB  ", 1024},
		{"10KB ", 10 * 1024},
		{"100KB", 100 * 1024},
		{"1MB  ", 1024 * 1024},
	}

	fmt.Printf("=== Base64 Decode Benchmark (chunkSize=%d) ===\n\n", chunkSize)

	for _, sz := range sizes {
		raw := make([]byte, sz.raw)
		for i := range raw {
			raw[i] = byte(i * 7)
		}
		encoded := encodeBase64Bench(raw)
		hotBytes := (len(encoded) / 4) * 4

		// Pre-pad source for SPMD.
		padded := hotBytes
		if rem := padded % chunkSize; rem != 0 {
			padded += chunkSize - rem
		}
		hotSrc := make([]byte, padded)
		copy(hotSrc, encoded[:hotBytes])
		for i := hotBytes; i < padded; i++ {
			hotSrc[i] = 'A'
		}

		dstSPMD := make([]byte, padded+64)
		dstScalar := make([]byte, hotBytes+64)

		// Determine iterations.
		iters := 5000000 / sz.raw
		if iters < 5 {
			iters = 5
		}

		// Warmup scalar.
		for w := 0; w < 3; w++ {
			scalarDecodeBench(dstScalar, encoded[:hotBytes])
		}
		// Benchmark scalar.
		start := time.Now()
		for iter := 0; iter < iters; iter++ {
			scalarDecodeBench(dstScalar, encoded[:hotBytes])
		}
		scalarElapsed := time.Since(start)
		scalarMBps := float64(len(encoded)) * float64(iters) / scalarElapsed.Seconds() / 1e6

		// Warmup SPMD.
		for w := 0; w < 3; w++ {
			outOff := 0
			for off := 0; off+chunkSize <= padded; off += chunkSize {
				n := benchDecodeAndPack(dstSPMD[outOff:], hotSrc[off:off+chunkSize])
				outOff += n
			}
		}
		// Benchmark SPMD.
		start = time.Now()
		for iter := 0; iter < iters; iter++ {
			outOff := 0
			for off := 0; off+chunkSize <= padded; off += chunkSize {
				n := benchDecodeAndPack(dstSPMD[outOff:], hotSrc[off:off+chunkSize])
				outOff += n
			}
		}
		spmdElapsed := time.Since(start)
		spmdMBps := float64(len(encoded)) * float64(iters) / spmdElapsed.Seconds() / 1e6

		speedup := spmdMBps / scalarMBps

		fmt.Printf("  [%s]  encoded=%dB  raw=%dB  iters=%d\n", sz.name, len(encoded), sz.raw, iters)
		fmt.Printf("    scalar:  %4.0f MB/s\n", scalarMBps)
		fmt.Printf("    spmd:    %4.0f MB/s\n", spmdMBps)
		fmt.Printf("    speedup: %.2fx\n\n", speedup)
	}
}
