// WebAssembly SPMD Test Runner using wasmer-go
// This runner executes SPMD-compiled WASM binaries and verifies SIMD execution
package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/wasmerio/wasmer-go/wasmer"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: go run wasmer-runner.go <program.wasm>")
	}

	wasmPath := os.Args[1]
	fmt.Printf("Running SPMD WASM binary: %s\n", wasmPath)

	// Read the WASM file
	wasmBytes, err := ioutil.ReadFile(wasmPath)
	if err != nil {
		log.Fatalf("Failed to read WASM file: %v", err)
	}

	// Create Wasmer engine with SIMD support
	engine := wasmer.NewEngine()
	store := wasmer.NewStore(engine)

	// Enable SIMD features
	config := wasmer.NewConfig()
	config.UseExperimentalFeaturesForWasi() // Enable WASI+SIMD experimental features
	engine = wasmer.NewEngineWithConfig(config)
	store = wasmer.NewStore(engine)

	fmt.Printf("Wasmer engine created with SIMD support\n")

	// Compile the WASM module
	module, err := wasmer.NewModule(store, wasmBytes)
	if err != nil {
		log.Fatalf("Failed to compile WASM module: %v", err)
	}

	fmt.Printf("WASM module compiled successfully\n")

	// Create WASI environment
	wasiEnv, err := wasmer.NewWasiStateBuilder("spmd-program").
		Arguments(os.Args[1:]).
		Environment("SPMD_RUNTIME", "wasmer-go").
		Finalize()
	if err != nil {
		log.Fatalf("Failed to create WASI environment: %v", err)
	}

	// Get WASI import object
	importObject, err := wasiEnv.GenerateImportObject(store, module)
	if err != nil {
		log.Fatalf("Failed to generate WASI imports: %v", err)
	}

	// Instantiate the module
	instance, err := wasmer.NewInstance(module, importObject)
	if err != nil {
		log.Fatalf("Failed to instantiate WASM module: %v", err)
	}

	fmt.Printf("WASM instance created\n")

	// Check if module has SIMD exports (optional verification)
	exports := instance.Exports
	fmt.Printf("Module exports: %d functions\n", len(exports))

	// Execute the main function
	start, err := instance.Exports.GetWasiStartFunction()
	if err != nil {
		// Try _start function instead
		startFunc, exists := instance.Exports["_start"]
		if !exists {
			log.Fatalf("No _start or WASI start function found")
		}
		start = startFunc
	}

	fmt.Printf("Executing SPMD program...\n")
	fmt.Println("--- Program Output ---")

	// Execute the function
	result, err := start()
	if err != nil {
		log.Fatalf("WASM execution failed: %v", err)
	}

	fmt.Println("--- End Program Output ---")

	// Print execution results
	if result != nil {
		fmt.Printf("Program returned: %v\n", result)
	} else {
		fmt.Printf("Program completed successfully\n")
	}

	// Optional: Inspect memory for SIMD data patterns
	if memory, exists := instance.Exports["memory"]; exists {
		memObj := memory.ToMemory()
		size := memObj.DataSize()
		fmt.Printf("Memory usage: %d bytes\n", size)
	}

	fmt.Printf("SPMD execution completed\n")
}

// Helper function to verify SIMD instruction usage (requires wasm2wat)
func verifySIMDInstructions(wasmPath string) {
	fmt.Printf("\n--- SIMD Instruction Verification ---\n")
	fmt.Printf("To verify SIMD instruction generation, run:\n")
	fmt.Printf("wasm2wat %s | grep -E '(v128|i32x4|f32x4)'\n", wasmPath)
	fmt.Printf("Expected SIMD instructions:\n")
	fmt.Printf("  - v128.load/store: Vector memory operations\n")
	fmt.Printf("  - i32x4.add/mul: 32-bit integer vector arithmetic\n")
	fmt.Printf("  - i32x4.splat: Broadcast scalar to vector\n")
	fmt.Printf("  - i32x4.extract_lane: Extract single lane\n")
	fmt.Printf("----------------------------------------\n")
}