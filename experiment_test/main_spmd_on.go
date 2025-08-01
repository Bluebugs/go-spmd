//go:build goexperiment.spmd

package main

import "fmt"

func main() {
	fmt.Println("SPMD is ON")
	fmt.Println("Build completed successfully with SPMD experiment")
}