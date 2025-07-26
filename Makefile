# SPMD Implementation for Go via TinyGo
# Makefile for building, testing, and managing the SPMD project

.PHONY: help install build test clean examples wasmer-test dual-test benchmark submodules

# Configuration
GOEXPERIMENT = spmd
TINYGO_TARGET = wasi
EXAMPLES_DIR = examples
WASMER_RUNNER = $(EXAMPLES_DIR)/wasmer-runner.go

# All example directories
EXAMPLES = simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper \
          base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying \
          panic-recover-varying map-restrictions pointer-varying type-switch-varying \
          non-spmd-varying-return spmd-call-contexts lanes-index-restrictions \
          varying-universal-constrained union-type-generics

# Help target
help:
	@echo "SPMD Implementation for Go via TinyGo"
	@echo ""
	@echo "Available targets:"
	@echo "  help            - Show this help message"
	@echo "  install         - Install all dependencies and setup submodules"
	@echo "  build           - Build TinyGo with SPMD support"
	@echo "  test            - Run all tests"
	@echo "  examples        - Build all SPMD examples"
	@echo "  wasmer-test     - Test examples with wasmer runtime"
	@echo "  dual-test       - Test both SIMD and scalar modes for all examples"
	@echo "  benchmark       - Run performance benchmarks"
	@echo "  clean           - Clean build artifacts"
	@echo "  submodules      - Initialize and update git submodules"
	@echo ""
	@echo "Environment:"
	@echo "  GOEXPERIMENT=$(GOEXPERIMENT)"
	@echo "  TINYGO_TARGET=$(TINYGO_TARGET)"

# Install dependencies and setup environment
install: submodules
	@echo "Installing Go dependencies..."
	cd $(EXAMPLES_DIR) && go mod download
	@echo "Installing wasmer-go for runtime testing..."
	cd $(EXAMPLES_DIR) && go get github.com/wasmerio/wasmer-go/wasmer
	@echo "Checking for required tools..."
	@which wasm2wat > /dev/null || (echo "Warning: wasm2wat not found. Install wabt for WASM inspection."; exit 0)
	@which wasmer > /dev/null || (echo "Warning: wasmer not found. Install wasmer for WASM execution."; exit 0)
	@echo "Installation complete!"

# Initialize and update git submodules
submodules:
	@echo "Initializing git submodules..."
	git submodule init
	git submodule update --recursive
	@echo "Submodules updated!"

# Build TinyGo with SPMD support
build:
	@echo "Building TinyGo with GOEXPERIMENT=$(GOEXPERIMENT)..."
	cd tinygo && GOEXPERIMENT=$(GOEXPERIMENT) go build
	@echo "TinyGo build complete!"

# Test the project
test: test-frontend test-examples

# Test Go frontend changes (when implemented)
test-frontend:
	@echo "Testing Go frontend changes..."
	cd go && ./src/run.bash
	@echo "Frontend tests complete!"

# Test all examples
test-examples: examples wasmer-test
	@echo "All example tests complete!"

# Build all SPMD examples
examples:
	@echo "Building all SPMD examples..."
	@for example in $(EXAMPLES); do \
		echo "Building $$example..."; \
		if [ -d "$(EXAMPLES_DIR)/$$example" ]; then \
			GOEXPERIMENT=$(GOEXPERIMENT) ./tinygo/tinygo build -target=$(TINYGO_TARGET) -o $(EXAMPLES_DIR)/$$example.wasm $(EXAMPLES_DIR)/$$example/main.go || exit 1; \
		else \
			echo "Warning: Example directory $$example not found"; \
		fi; \
	done
	@echo "All examples built successfully!"

# Test examples with wasmer runtime
wasmer-test: examples
	@echo "Testing examples with wasmer runtime..."
	@for example in $(EXAMPLES); do \
		if [ -f "$(EXAMPLES_DIR)/$$example.wasm" ]; then \
			echo "Testing $$example.wasm..."; \
			cd $(EXAMPLES_DIR) && go run wasmer-runner.go $$example.wasm || exit 1; \
		fi; \
	done
	@echo "Wasmer tests complete!"

# Test both SIMD and scalar modes (dual compilation)
dual-test:
	@echo "Testing dual-mode compilation (SIMD + scalar)..."
	@for example in $(EXAMPLES); do \
		if [ -d "$(EXAMPLES_DIR)/$$example" ]; then \
			echo "Testing $$example in dual mode..."; \
			\
			echo "  Compiling SIMD version..."; \
			GOEXPERIMENT=$(GOEXPERIMENT) ./tinygo/tinygo build -target=$(TINYGO_TARGET) -simd=true -o $(EXAMPLES_DIR)/$$example-simd.wasm $(EXAMPLES_DIR)/$$example/main.go || exit 1; \
			\
			echo "  Compiling scalar version..."; \
			GOEXPERIMENT=$(GOEXPERIMENT) ./tinygo/tinygo build -target=$(TINYGO_TARGET) -simd=false -o $(EXAMPLES_DIR)/$$example-scalar.wasm $(EXAMPLES_DIR)/$$example/main.go || exit 1; \
			\
			echo "  Verifying SIMD instructions..."; \
			simd_count=$$(wasm2wat $(EXAMPLES_DIR)/$$example-simd.wasm 2>/dev/null | grep -cE "(v128|i32x4|f32x4)" || echo "0"); \
			if [ "$$simd_count" -eq 0 ]; then \
				echo "    Warning: SIMD version contains no SIMD instructions"; \
			else \
				echo "    ✓ SIMD version contains $$simd_count SIMD instructions"; \
			fi; \
			\
			echo "  Verifying scalar version..."; \
			scalar_simd_count=$$(wasm2wat $(EXAMPLES_DIR)/$$example-scalar.wasm 2>/dev/null | grep -cE "(v128|i32x4|f32x4)" || echo "0"); \
			if [ "$$scalar_simd_count" -ne 0 ]; then \
				echo "    Warning: Scalar version contains SIMD instructions"; \
			else \
				echo "    ✓ Scalar version contains no SIMD instructions"; \
			fi; \
			\
			echo "  Testing execution..."; \
			cd $(EXAMPLES_DIR) && simd_output=$$(go run wasmer-runner.go $$example-simd.wasm 2>/dev/null || echo "ERROR"); \
			cd $(EXAMPLES_DIR) && scalar_output=$$(go run wasmer-runner.go $$example-scalar.wasm 2>/dev/null || echo "ERROR"); \
			\
			if [ "$$simd_output" = "$$scalar_output" ] && [ "$$simd_output" != "ERROR" ]; then \
				echo "    ✓ Both versions produce identical output"; \
			else \
				echo "    ERROR: SIMD and scalar outputs differ or failed"; \
				echo "    SIMD: $$simd_output"; \
				echo "    Scalar: $$scalar_output"; \
				exit 1; \
			fi; \
		fi; \
	done
	@echo "Dual-mode tests complete!"

# Run performance benchmarks
benchmark: dual-test
	@echo "Running performance benchmarks..."
	@echo "Benchmarking key examples (simple-sum, ipv4-parser, base64-decoder)..."
	@for example in simple-sum ipv4-parser base64-decoder; do \
		if [ -f "$(EXAMPLES_DIR)/$$example-simd.wasm" ] && [ -f "$(EXAMPLES_DIR)/$$example-scalar.wasm" ]; then \
			echo "Benchmarking $$example..."; \
			echo "  SIMD version:"; \
			cd $(EXAMPLES_DIR) && time go run wasmer-runner.go $$example-simd.wasm 2>&1 | grep real || echo "    Timing failed"; \
			echo "  Scalar version:"; \
			cd $(EXAMPLES_DIR) && time go run wasmer-runner.go $$example-scalar.wasm 2>&1 | grep real || echo "    Timing failed"; \
		fi; \
	done
	@echo "Benchmarks complete!"

# Generate browser SIMD detection helper
browser-support:
	@echo "Generating browser SIMD detection helper..."
	@cat > $(EXAMPLES_DIR)/simd-loader.js << 'EOF'
// Browser-side SIMD detection and WASM loading
async function loadOptimalWasm(baseName) {
    const supportsSimd = WebAssembly.validate(new Uint8Array([
        0x00, 0x61, 0x73, 0x6d, // WASM magic
        0x01, 0x00, 0x00, 0x00, // Version
        0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b, // Type section (v128)
    ]));
    
    const wasmFile = supportsSimd ? `$${baseName}-simd.wasm` : `$${baseName}-scalar.wasm`;
    console.log(`Loading $${wasmFile} (SIMD support: $${supportsSimd})`);
    
    return await WebAssembly.instantiateStreaming(fetch(wasmFile));
}
EOF
	@echo "Browser helper generated: $(EXAMPLES_DIR)/simd-loader.js"

# Validate experiment gating (ensure graceful fallback when disabled)
validate-gating:
	@echo "Validating GOEXPERIMENT gating..."
	@echo "Testing compilation without SPMD experiment (should fail gracefully)..."
	@for example in simple-sum odd-even; do \
		if [ -d "$(EXAMPLES_DIR)/$$example" ]; then \
			echo "Testing $$example without GOEXPERIMENT=spmd..."; \
			./tinygo/tinygo build -target=$(TINYGO_TARGET) $(EXAMPLES_DIR)/$$example/main.go 2>&1 && \
				echo "ERROR: Should have failed without GOEXPERIMENT=spmd" && exit 1 || \
				echo "✓ Correctly failed without experiment flag"; \
		fi; \
	done
	@echo "Experiment gating validation complete!"

# Run all tests including validation
test-all: test dual-test validate-gating
	@echo "All tests passed!"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(EXAMPLES_DIR)/*.wasm
	rm -f $(EXAMPLES_DIR)/simd-loader.js
	cd tinygo && go clean
	cd go && ./src/clean.bash
	@echo "Clean complete!"

# Development workflow targets
dev-setup: install build
	@echo "Development environment setup complete!"

dev-test: examples wasmer-test
	@echo "Development testing complete!"

# Show current status
status:
	@echo "SPMD Project Status:"
	@echo "  Go submodule: $$(cd go && git rev-parse --short HEAD)"
	@echo "  TinyGo submodule: $$(cd tinygo && git rev-parse --short HEAD)"
	@echo "  ISPC submodule: $$(cd ispc && git rev-parse --short HEAD)"
	@echo "  Examples built: $$(ls $(EXAMPLES_DIR)/*.wasm 2>/dev/null | wc -l)"
	@echo "  GOEXPERIMENT: $(GOEXPERIMENT)"