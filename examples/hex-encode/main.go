// Hexadecimal encoding using SPMD Go
// From: practical-vector.md
package main

import (
	"fmt"
)

const hextable = "0123456789abcdef"

func main() {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}
	result := make([]byte, len(data)*2)
	_ = Encode(result, data)
	fmt.Printf("Input:  %x\n", data)
	fmt.Printf("Output: %s\n", result)
}

func Encode(dst, src []byte) int {
	j := 0
	for _, v := range src {
		// hextable[v>>4] is varying because v is varying
		// Store varying values at varying indices (i*2 and i*2+1 are varying)
		dst[j] = hextable[v>>4]
		dst[j+1] = hextable[v&0x0f]
		j += 2
	}
	return len(src) * 2
}
