// run -goexperiment spmd

// Regression test for fmt.Printf("%v", dst[i]) inside a go for loop
// where dst is a uniform slice and i is the varying loop index.
//
// Before fix (x-tools-spmd f3afc3fb): the SSA builder's addressable
// load path produced a plain-elem SSA value (e.g. byte) even though
// go/types had typed dst[i] as Varying[byte]. The MakeInterface
// boxing gating keyed on *types.SPMDType, so the value was packed
// with a scalar typecode and fmt printed only lane 0.
//
// After fix: a ChangeType wraps the loaded value as Varying[elem] when
// tv.Type indicates varying, so MakeInterface attaches SPMDMask and
// fmt.printSPMDVarying produces mask-aware "[v _ v _]" output.
package main

import "fmt"

const hextable = "0123456789abcdef"

//go:noinline
func encodeHi(dst, src []byte) {
	go for i := range dst {
		v := src[i>>1]
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
			fmt.Printf("hi: %v\n", dst[i])
		} else {
			dst[i] = hextable[v&0x0f]
			fmt.Printf("lo: %v\n", dst[i])
		}
	}
}

func main() {
	src := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}
	dst := make([]byte, len(src)*2)
	encodeHi(dst, src)
	fmt.Printf("dst: %s\n", string(dst))
}
