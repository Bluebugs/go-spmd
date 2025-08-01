//go:build !goexperiment.spmd

package main

import "fmt"

func main() {
	fmt.Println("SPMD is OFF")
	fmt.Println("Build completed successfully without SPMD experiment")
}