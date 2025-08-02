# SPMD Integration Test Suite Makefile
# Part of Phase 0.5 - Integration Test Suite Setup

# Configuration
TINYGO ?= tinygo
GO ?= go
WASM2WAT ?= wasm2wat

# Test targets
.PHONY: all test test-go test-shell clean help

# Default target
all: test

# Run all integration tests
test: test-go test-shell

# Run Go integration tests
test-go:
	@echo "Running Go integration tests..."
	$(GO) test -v ./... -timeout=10m

# Run shell-based dual-mode tests
test-shell:
	@echo "Running shell-based dual-mode tests..."
	./dual-mode-test-runner.sh

# Test basic examples only (faster)
test-basic:
	@echo "Running basic SPMD examples..."
	$(GO) test -v -run "TestSPMDBasicExamples" -timeout=5m

# Test illegal examples
test-illegal:
	@echo "Testing illegal examples..."
	$(GO) test -v -run "TestSPMDIllegalExamples" -timeout=2m

# Test legacy compatibility
test-legacy:
	@echo "Testing legacy compatibility..."
	$(GO) test -v -run "TestSPMDLegacyCompatibility" -timeout=2m

# Test browser integration
test-browser:
	@echo "Testing browser SIMD detection..."
	$(GO) test -v -run "TestSPMDBrowserSIMDDetection"

# Run benchmarks
bench:
	@echo "Running SPMD compilation benchmarks..."
	$(GO) test -bench=. -benchmem

# Check dependencies
check-deps:
	@echo "Checking dependencies..."
	@which $(TINYGO) >/dev/null || (echo "Error: TinyGo not found" && exit 1)
	@which $(GO) >/dev/null || (echo "Error: Go not found" && exit 1)
	@which $(WASM2WAT) >/dev/null || echo "Warning: wasm2wat not found (SIMD verification disabled)"
	@echo "Dependencies OK"

# Clean up generated files
clean:
	@echo "Cleaning up test artifacts..."
	rm -f *.wasm
	rm -f *_output.txt
	rm -f build_*.log
	rm -f bench-*.wasm
	rm -f illegal-*.wasm
	rm -f legacy-*.wasm

# Development helpers

# Quick test of specific example
test-example:
	@test -n "$(EXAMPLE)" || (echo "Usage: make test-example EXAMPLE=simple-sum" && exit 1)
	@echo "Testing example: $(EXAMPLE)"
	GOEXPERIMENT=spmd $(TINYGO) build -target=wasi -o $(EXAMPLE)-test.wasm $(EXAMPLE)/main.go
	$(GO) run wasmer-runner.go $(EXAMPLE)-test.wasm
	@rm -f $(EXAMPLE)-test.wasm

# Verify SIMD instructions in specific example
verify-simd:
	@test -n "$(EXAMPLE)" || (echo "Usage: make verify-simd EXAMPLE=simple-sum" && exit 1)
	@echo "Verifying SIMD instructions in: $(EXAMPLE)"
	GOEXPERIMENT=spmd $(TINYGO) build -target=wasi -o $(EXAMPLE)-verify.wasm $(EXAMPLE)/main.go
	$(WASM2WAT) $(EXAMPLE)-verify.wasm | grep -E "(v128|i32x4|f32x4)" || echo "No SIMD instructions found"
	@rm -f $(EXAMPLE)-verify.wasm

# Continuous integration target
ci: check-deps clean test

# Help target
help:
	@echo "SPMD Integration Test Suite"
	@echo ""
	@echo "Available targets:"
	@echo "  all          - Run all tests (default)"
	@echo "  test         - Run all integration tests"
	@echo "  test-go      - Run Go integration tests only"
	@echo "  test-shell   - Run shell-based dual-mode tests only"
	@echo "  test-basic   - Test basic examples only (faster)"
	@echo "  test-illegal - Test illegal examples only"
	@echo "  test-legacy  - Test legacy compatibility only"
	@echo "  test-browser - Test browser integration only"
	@echo "  bench        - Run compilation benchmarks"
	@echo "  check-deps   - Check required dependencies"
	@echo "  clean        - Clean up generated files"
	@echo ""
	@echo "Development targets:"
	@echo "  test-example EXAMPLE=name  - Test specific example"
	@echo "  verify-simd EXAMPLE=name   - Verify SIMD instructions in example"
	@echo "  ci                         - Full CI test suite"
	@echo ""
	@echo "Requirements:"
	@echo "  - TinyGo with SPMD support"
	@echo "  - Go compiler"
	@echo "  - wasm2wat (optional, for SIMD verification)"
	@echo "  - wasmer-go dependencies"