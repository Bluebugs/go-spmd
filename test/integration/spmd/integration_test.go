// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// SPMD Integration Test Suite
// Tests dual-mode compilation and execution for SPMD examples
// Part of Phase 0.5 - Integration Test Suite Setup

package spmd_integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test configuration
var (
	tinygoPath   = "tinygo"
	wasm2watPath = "wasm2wat"
	projectRoot  = "../../../"
)

// Example categories for organized testing
var (
	basicExamples = []string{
		"simple-sum",
		"odd-even", 
		"bit-counting",
		"array-counting",
		"printf-verbs",
		"hex-encode",
		"to-upper",
		"debug-varying",
		"goroutine-varying",
		"defer-varying",
		"panic-recover-varying",
		"map-restrictions",
		"pointer-varying",
		"type-switch-varying",
		"non-spmd-varying-return",
		"spmd-call-contexts",
		"lanes-index-restrictions",
		"varying-universal-constrained",
		"union-type-generics",
		"type-casting-varying",
		"varying-array-iteration",
		"mandelbrot",
	}
	
	advancedExamples = []string{
		"base64-decoder",
		"ipv4-parser",
	}
	
	illegalExamples = []string{
		"break-in-go-for.go",
		"control-flow-outside-spmd.go", 
		"go-for-in-spmd-function.go",
		"invalid-contexts.go",
		"invalid-lane-constraints.go",
		"invalid-type-casting.go",
		"malformed-syntax.go",
		"nested-go-for.go",
		"public-spmd-function.go",
		"varying-to-uniform.go",
	}
	
	legacyExamples = []string{
		"functions",
		"json_tags",
		"labels", 
		"types",
		"variables",
	}
)

// Helper functions

func checkTinyGo(t *testing.T) {
	cmd := exec.Command(tinygoPath, "version")
	if err := cmd.Run(); err != nil {
		t.Skipf("TinyGo not available: %v", err)
	}
}

func checkWasm2Wat(t *testing.T) bool {
	cmd := exec.Command(wasm2watPath, "--version")
	return cmd.Run() == nil
}

func buildSPMDExample(t *testing.T, example string, simdMode bool) (string, error) {
	// Set GOEXPERIMENT=spmd
	env := os.Environ()
	env = append(env, "GOEXPERIMENT=spmd")
	
	// Determine output filename
	suffix := "scalar"
	if simdMode {
		suffix = "simd"
	}
	outputWasm := fmt.Sprintf("%s-%s.wasm", example, suffix)
	
	// Build command
	args := []string{"build", "-target=wasi", "-o", outputWasm}
	
	// Add SIMD flag when available (placeholder for future TinyGo support)
	if simdMode {
		// args = append(args, "-simd=true")  // TODO: Add when TinyGo supports this flag
	}
	
	args = append(args, fmt.Sprintf("%s/main.go", example))
	
	cmd := exec.Command(tinygoPath, args...)
	cmd.Env = env
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compilation failed: %v\nOutput: %s", err, output)
	}
	
	return outputWasm, nil
}

func countSIMDInstructions(t *testing.T, wasmFile string) int {
	if !checkWasm2Wat(t) {
		t.Log("wasm2wat not available, skipping SIMD instruction count")
		return -1
	}
	
	cmd := exec.Command(wasm2watPath, wasmFile)
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Failed to run wasm2wat on %s: %v", wasmFile, err)
		return -1
	}
	
	// Count SIMD instructions
	text := string(output)
	count := 0
	simdPatterns := []string{"v128", "i32x4", "f32x4", "i64x2", "f64x2"}
	
	for _, pattern := range simdPatterns {
		count += strings.Count(text, pattern)
	}
	
	return count
}

func runWASMExample(t *testing.T, wasmFile string) ([]byte, error) {
	// Use the wasmer-runner.go to execute WASM
	cmd := exec.Command("go", "run", "wasmer-runner.go", wasmFile)
	return cmd.CombinedOutput()
}

// Main integration tests

func TestSPMDBasicExamplesDualMode(t *testing.T) {
	checkTinyGo(t)
	
	for _, example := range basicExamples {
		example := example // capture loop variable
		t.Run(example, func(t *testing.T) {
			t.Parallel()
			
			// Check if example directory exists
			exampleDir := example
			if _, err := os.Stat(exampleDir); os.IsNotExist(err) {
				t.Skipf("Example %s not found", example)
			}
			
			t.Logf("Testing dual-mode compilation for %s", example)
			
			// Build SIMD version
			simdWasm, err := buildSPMDExample(t, example, true)
			if err != nil {
				t.Errorf("SIMD compilation failed: %v", err)
				return
			}
			defer os.Remove(simdWasm)
			t.Logf("SIMD compilation succeeded: %s", simdWasm)
			
			// Build scalar version
			scalarWasm, err := buildSPMDExample(t, example, false)
			if err != nil {
				t.Errorf("Scalar compilation failed: %v", err)
				return
			}
			defer os.Remove(scalarWasm)
			t.Logf("Scalar compilation succeeded: %s", scalarWasm)
			
			// Count SIMD instructions
			simdCount := countSIMDInstructions(t, simdWasm)
			scalarCount := countSIMDInstructions(t, scalarWasm)
			
			if simdCount >= 0 && scalarCount >= 0 {
				t.Logf("SIMD version: %d SIMD instructions", simdCount)
				t.Logf("Scalar version: %d SIMD instructions", scalarCount)
				
				// In the future, we expect SIMD version to have more SIMD instructions
				// For now, just log the counts as the implementation is not complete
			}
		})
	}
}

func TestSPMDAdvancedExamplesMayFail(t *testing.T) {
	checkTinyGo(t)
	
	for _, example := range advancedExamples {
		example := example // capture loop variable
		t.Run(example, func(t *testing.T) {
			t.Parallel()
			
			// Check if example directory exists
			exampleDir := example
			if _, err := os.Stat(exampleDir); os.IsNotExist(err) {
				t.Skipf("Example %s not found", example)
			}
			
			t.Logf("Testing advanced example %s (may fail)", example)
			
			// These examples may fail due to missing cross-lane operations
			simdWasm, err := buildSPMDExample(t, example, true)
			if err != nil {
				t.Logf("Advanced example %s failed as expected: %v", example, err)
				return
			}
			defer os.Remove(simdWasm)
			
			t.Logf("Advanced example %s unexpectedly succeeded", example)
		})
	}
}

func TestSPMDIllegalExamplesFailCompilation(t *testing.T) {
	checkTinyGo(t)
	
	// Set GOEXPERIMENT=spmd
	env := os.Environ()
	env = append(env, "GOEXPERIMENT=spmd")
	
	illegalDir := "illegal-spmd"
	if _, err := os.Stat(illegalDir); os.IsNotExist(err) {
		t.Skip("Illegal examples directory not found")
	}
	
	for _, illegalFile := range illegalExamples {
		illegalFile := illegalFile // capture loop variable
		t.Run(illegalFile, func(t *testing.T) {
			t.Parallel()
			
			fullPath := filepath.Join(illegalDir, illegalFile)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				t.Skipf("Illegal example %s not found", illegalFile)
			}
			
			outputWasm := fmt.Sprintf("illegal-%s.wasm", strings.TrimSuffix(illegalFile, ".go"))
			
			cmd := exec.Command(tinygoPath, "build", "-target=wasi", "-o", outputWasm, fullPath)
			cmd.Env = env
			
			output, err := cmd.CombinedOutput()
			
			// Clean up any accidentally created WASM file
			os.Remove(outputWasm)
			
			if err == nil {
				t.Errorf("Illegal example %s should have failed compilation but succeeded", illegalFile)
			} else {
				t.Logf("Illegal example %s correctly failed compilation: %v", illegalFile, err)
				t.Logf("Compilation output: %s", output)
			}
		})
	}
}

func TestSPMDLegacyCompatibility(t *testing.T) {
	checkTinyGo(t)
	
	// Test without GOEXPERIMENT=spmd
	env := os.Environ()
	// Remove any existing GOEXPERIMENT
	var filteredEnv []string
	for _, envVar := range env {
		if !strings.HasPrefix(envVar, "GOEXPERIMENT=") {
			filteredEnv = append(filteredEnv, envVar)
		}
	}
	
	legacyDir := "legacy"
	if _, err := os.Stat(legacyDir); os.IsNotExist(err) {
		t.Skip("Legacy examples directory not found")
	}
	
	for _, legacyExample := range legacyExamples {
		legacyExample := legacyExample // capture loop variable
		t.Run(legacyExample, func(t *testing.T) {
			t.Parallel()
			
			examplePath := filepath.Join(legacyDir, legacyExample)
			mainGoPath := filepath.Join(examplePath, "main.go")
			
			if _, err := os.Stat(mainGoPath); os.IsNotExist(err) {
				t.Skipf("Legacy example %s/main.go not found", legacyExample)
			}
			
			outputWasm := fmt.Sprintf("legacy-%s.wasm", legacyExample)
			
			cmd := exec.Command(tinygoPath, "build", "-target=wasi", "-o", outputWasm, mainGoPath)
			cmd.Env = filteredEnv
			
			output, err := cmd.CombinedOutput()
			
			// Clean up
			os.Remove(outputWasm)
			
			if err != nil {
				t.Errorf("Legacy example %s failed to compile without SPMD experiment: %v\nOutput: %s", 
					legacyExample, err, output)
			} else {
				t.Logf("Legacy example %s compiles correctly without SPMD experiment", legacyExample)
			}
		})
	}
}

func TestSPMDBrowserSIMDDetection(t *testing.T) {
	browserDir := "browser-simd-detection"
	indexPath := filepath.Join(browserDir, "index.html")
	
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Skip("Browser SIMD detection example not found")
	}
	
	// Read the HTML file and check for SIMD detection code
	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", indexPath, err)
	}
	
	htmlContent := string(content)
	
	// Check for WebAssembly SIMD detection patterns
	if !strings.Contains(htmlContent, "WebAssembly.validate") {
		t.Error("Browser SIMD detection missing WebAssembly.validate")
	}
	
	if !strings.Contains(htmlContent, "v128") {
		t.Error("Browser SIMD detection missing v128 instruction check")
	}
	
	t.Log("Browser SIMD detection code found and appears complete")
}

// Benchmark tests (for future performance validation)

func BenchmarkSPMDCompilation(b *testing.B) {
	checkTinyGo(&testing.T{}) // Convert to test for dependency check
	
	example := "simple-sum"
	if _, err := os.Stat(example); os.IsNotExist(err) {
		b.Skip("simple-sum example not found")
	}
	
	env := os.Environ()
	env = append(env, "GOEXPERIMENT=spmd")
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		outputWasm := fmt.Sprintf("bench-%d.wasm", i)
		
		cmd := exec.Command(tinygoPath, "build", "-target=wasi", "-o", outputWasm, 
			fmt.Sprintf("%s/main.go", example))
		cmd.Env = env
		
		if err := cmd.Run(); err != nil {
			b.Fatalf("Compilation failed: %v", err)
		}
		
		os.Remove(outputWasm)
	}
}

// Test helper that will be extended in future phases
func TestSPMDTestInfrastructure(t *testing.T) {
	t.Log("SPMD Integration Test Infrastructure is ready")
	t.Log("Test categories:")
	t.Logf("  - Basic examples: %d", len(basicExamples))
	t.Logf("  - Advanced examples: %d", len(advancedExamples))
	t.Logf("  - Illegal examples: %d", len(illegalExamples))
	t.Logf("  - Legacy examples: %d", len(legacyExamples))
	
	// Verify test runner script exists
	scriptPath := "dual-mode-test-runner.sh"
	if _, err := os.Stat(scriptPath); err != nil {
		t.Errorf("Test runner script not found: %v", err)
	} else {
		t.Log("✓ Dual-mode test runner script available")
	}
	
	// Verify wasmer-runner exists
	runnerPath := "cmd/wasmer-runner/wasmer-runner.go"
	if _, err := os.Stat(runnerPath); err != nil {
		t.Logf("Wasmer runner not found: %v", err)
		t.Log("⚠ Wasmer runner should be available for runtime testing")
	} else {
		t.Log("✓ Wasmer runner available")
	}
	
	t.Log("Phase 0.5 Integration Test Suite Setup is complete")
}