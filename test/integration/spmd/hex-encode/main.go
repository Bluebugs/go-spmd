// Hexadecimal encoding using SPMD Go
// From: practical-vector.md
package main

import (
	"fmt"
	"os"
)

const hextable = "0123456789abcdef"

func main() {
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}
	result1 := make([]byte, len(data)*2)
	result2 := make([]byte, len(data)*2)
	r1 := Encode(result1, data)
	r2 := EncodeScalar(result2, data)
	fmt.Printf("Input:  %x\n", data)
	fmt.Printf("Output SPMD: %s\n", result1)
	fmt.Printf("Output Scalar: %s\n", result2)
	fmt.Printf("SPMD length: %d, Scalar length: %d\n", r1, r2)

	if string(result1) != string(result2) && r1 == r2 {
		fmt.Println("Mismatch between SPMD and Scalar results!")
		os.Exit(1)
	} else {
		fmt.Println("SPMD and Scalar results match.")
	}
}

func Encode(dst, src []byte) int {
  	go for i := range 2 * len(src) {
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