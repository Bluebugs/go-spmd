//go:build !goexperiment.spmd

package main

import "fmt"

func main() {
	fmt.Println("SPMD is OFF")
}