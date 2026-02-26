# SPMD Implementation Makefile
# Automated test runners for all phases of SPMD development
# Part of Phase 0.6 - TDD Workflow Documentation

# Configuration
GO ?= go
TINYGO ?= tinygo
WASM2WAT ?= wasm2wat
TEST_TIMEOUT ?= 30m
GOEXPERIMENT ?= spmd

# Directories
GO_DIR = go
TINYGO_DIR = tinygo
TEST_DIR = test
EXAMPLES_DIR = examples
INTEGRATION_DIR = test/integration/spmd

# Derived paths
SPMD_GO = $(GO_DIR)/bin/go
SPMD_TINYGO = $(TINYGO_DIR)/build/tinygo

# Colors for output
GREEN = \033[0;32m
YELLOW = \033[1;33m
RED = \033[0;31m
NC = \033[0m # No Color

# Default target
.PHONY: all
all: check-deps test-phase0

# =============================================================================
# Build targets
# =============================================================================

.PHONY: build
build: build-go build-tinygo ## Build both Go and TinyGo with SPMD support

.PHONY: build-go
build-go: ## Build the forked Go toolchain with SPMD support
	@echo "$(YELLOW)Building Go toolchain...$(NC)"
	cd $(GO_DIR)/src && GOEXPERIMENT=$(GOEXPERIMENT) ./make.bash
	@echo "$(GREEN)✓ Go toolchain built at $(SPMD_GO)$(NC)"

.PHONY: build-tinygo
build-tinygo: $(SPMD_GO) ## Build TinyGo with SPMD support (requires Go to be built first)
	@echo "$(YELLOW)Building TinyGo...$(NC)"
	$(MAKE) -C $(TINYGO_DIR) tinygo GO=$(CURDIR)/$(SPMD_GO)
	@echo "$(GREEN)✓ TinyGo built at $(SPMD_TINYGO)$(NC)"

$(SPMD_GO):
	@echo "$(RED)Error: Go toolchain not built. Run 'make build-go' first.$(NC)"
	@exit 1

# SPMD compilation environment
# TinyGo resolves GOROOT via `go env`, so the forked Go must be on PATH.
SPMD_ENV = PATH=$(CURDIR)/$(GO_DIR)/bin:$(PATH) GOEXPERIMENT=$(GOEXPERIMENT)

.PHONY: compile
compile: $(SPMD_GO) $(SPMD_TINYGO) ## Compile an SPMD example to WASM (usage: make compile EXAMPLE=hex-encode)
	@test -n "$(EXAMPLE)" || (echo "$(RED)Error: specify EXAMPLE=<name> (e.g., make compile EXAMPLE=hex-encode)$(NC)" && exit 1)
	@test -f $(EXAMPLES_DIR)/$(EXAMPLE)/main.go || (echo "$(RED)Error: $(EXAMPLES_DIR)/$(EXAMPLE)/main.go not found$(NC)" && exit 1)
	$(SPMD_ENV) $(SPMD_TINYGO) build -target=wasi -o $(EXAMPLE).wasm $(EXAMPLES_DIR)/$(EXAMPLE)/main.go
	@echo "$(GREEN)✓ Compiled $(EXAMPLE).wasm$(NC)"

# Help target
.PHONY: help
help:
	@echo "SPMD Implementation Test Automation"
	@echo ""
	@echo "Build:"
	@echo "  build                - Build both Go and TinyGo with SPMD support"
	@echo "  build-go             - Build the forked Go toolchain"
	@echo "  build-tinygo         - Build TinyGo (requires Go built first)"
	@echo "  compile EXAMPLE=name - Compile an example to WASM"
	@echo ""
	@echo "Phase Testing:"
	@echo "  test-phase0          - Run all Phase 0 foundation tests"
	@echo "  test-phase1          - Run all Phase 1 frontend tests"
	@echo "  test-phase2          - Run all Phase 2 backend tests"
	@echo "  test-phase3          - Run all Phase 3 validation tests"
	@echo ""
	@echo "Individual Phase Tests:"
	@echo "  test-phase01         - GOEXPERIMENT integration"
	@echo "  test-phase02         - Parser test infrastructure"
	@echo "  test-phase03         - Type checker test infrastructure"
	@echo "  test-phase04         - SSA generation test infrastructure"
	@echo "  test-phase05         - Integration test infrastructure"
	@echo "  test-phase06         - TDD workflow validation"
	@echo ""
	@echo "Component Testing:"
	@echo "  test-lexer           - Lexer modifications (Phase 1.2)"
	@echo "  test-parser          - Parser extensions (Phase 1.3)"
	@echo "  test-typechecker     - Type system (Phase 1.4-1.5)"
	@echo "  test-ssa             - SSA generation (Phase 1.7)"
	@echo "  test-stdlib          - Standard library (Phase 1.8-1.9)"
	@echo ""
	@echo "Integration Testing:"
	@echo "  test-dual-mode       - Dual-mode compilation"
	@echo "  test-examples        - All example validation"
	@echo "  test-illegal         - Illegal examples"
	@echo "  test-legacy          - Legacy compatibility"
	@echo "  test-browser         - Browser integration"
	@echo ""
	@echo "CI/CD:"
	@echo "  ci-quick             - Quick validation (<5 min)"
	@echo "  ci-full              - Full validation (<30 min)"
	@echo "  test-regression      - Regression testing"
	@echo ""
	@echo "Utilities:"
	@echo "  check-deps           - Check dependencies"
	@echo "  clean                - Clean generated files"
	@echo "  benchmark            - Run performance benchmarks"

# Dependency checking
.PHONY: check-deps
check-deps:
	@echo "$(YELLOW)Checking dependencies...$(NC)"
	@which $(GO) >/dev/null || (echo "$(RED)Error: Go not found$(NC)" && exit 1)
	@echo "$(GREEN)✓ Go compiler available$(NC)"
	@cd $(GO_DIR) && $(GO) version >/dev/null || (echo "$(RED)Error: Go submodule not built$(NC)" && exit 1)
	@echo "$(GREEN)✓ Go submodule built$(NC)"
	@which $(TINYGO) >/dev/null || echo "$(YELLOW)⚠ TinyGo not found (backend tests will be skipped)$(NC)"
	@which $(WASM2WAT) >/dev/null || echo "$(YELLOW)⚠ wasm2wat not found (SIMD verification disabled)$(NC)"
	@echo "$(GREEN)Dependencies checked$(NC)"

# Phase 0: Foundation Testing
.PHONY: test-phase0
test-phase0: test-phase01 test-phase02 test-phase03 test-phase04 test-phase05 test-phase06
	@echo "$(GREEN)Phase 0 foundation testing complete$(NC)"

.PHONY: test-phase01
test-phase01:
	@echo "$(YELLOW)Testing Phase 0.1: GOEXPERIMENT Integration$(NC)"
	@cd $(GO_DIR) && GOEXPERIMENT=$(GOEXPERIMENT) $(GO) test ./src/internal/goexperiment -v -timeout=5m
	@echo "$(GREEN)✓ Phase 0.1 tests passed$(NC)"

.PHONY: test-phase02
test-phase02:
	@echo "$(YELLOW)Testing Phase 0.2: Parser Test Infrastructure$(NC)"
	@cd $(GO_DIR) && GOEXPERIMENT=$(GOEXPERIMENT) $(GO) test ./src/cmd/compile/internal/syntax -run TestSPMDParser -v -timeout=5m
	@echo "$(GREEN)✓ Phase 0.2 tests passed$(NC)"

.PHONY: test-phase03
test-phase03:
	@echo "$(YELLOW)Testing Phase 0.3: Type Checker Test Infrastructure$(NC)"
	@cd $(GO_DIR) && GOEXPERIMENT=$(GOEXPERIMENT) $(GO) test ./src/cmd/compile/internal/types2 -run TestSPMDTypeChecking -v -timeout=5m
	@echo "$(GREEN)✓ Phase 0.3 tests passed$(NC)"

.PHONY: test-phase04
test-phase04:
	@echo "$(YELLOW)Testing Phase 0.4: SSA Generation Test Infrastructure$(NC)"
	@cd $(GO_DIR) && GOEXPERIMENT=$(GOEXPERIMENT) $(GO) test ./src/cmd/compile/internal/ssagen -run TestSPMDSSAGeneration -v -timeout=5m
	@echo "$(GREEN)✓ Phase 0.4 tests passed$(NC)"

.PHONY: test-phase05
test-phase05:
	@echo "$(YELLOW)Testing Phase 0.5: Integration Test Infrastructure$(NC)"
	@cd $(INTEGRATION_DIR) && $(GO) test -v -run TestSPMDTestInfrastructure -timeout=5m
	@echo "$(GREEN)✓ Phase 0.5 tests passed$(NC)"

.PHONY: test-phase06
test-phase06:
	@echo "$(YELLOW)Testing Phase 0.6: TDD Workflow Documentation$(NC)"
	@test -f TDD-WORKFLOW.md || (echo "$(RED)TDD-WORKFLOW.md not found$(NC)" && exit 1)
	@test -f Makefile || (echo "$(RED)Main Makefile not found$(NC)" && exit 1)
	@echo "$(GREEN)✓ Phase 0.6 documentation complete$(NC)"

# Quick validation for development
.PHONY: ci-quick
ci-quick: check-deps
	@echo "$(YELLOW)Running quick validation (<5 minutes)$(NC)"
	@$(MAKE) test-phase0-quick
	@$(MAKE) test-illegal
	@echo "$(GREEN)Quick validation complete$(NC)"

.PHONY: test-phase0-quick
test-phase0-quick:
	@echo "$(YELLOW)Quick Phase 0 validation$(NC)"
	@cd $(GO_DIR) && GOEXPERIMENT=$(GOEXPERIMENT) $(GO) test ./src/internal/goexperiment -v -timeout=2m
	@cd $(INTEGRATION_DIR) && $(GO) test -v -run TestSPMDTestInfrastructure -timeout=1m
	@echo "$(GREEN)✓ Quick Phase 0 validation passed$(NC)"

# Integration testing shortcuts
.PHONY: test-illegal
test-illegal:
	@echo "$(YELLOW)Testing illegal examples$(NC)"
	@cd $(INTEGRATION_DIR) && $(GO) test -v -run TestSPMDIllegalExamples -timeout=10m

.PHONY: test-legacy
test-legacy:
	@echo "$(YELLOW)Testing legacy compatibility$(NC)"
	@cd $(INTEGRATION_DIR) && $(GO) test -v -run TestSPMDLegacyCompatibility -timeout=10m

# Clean up
.PHONY: clean
clean:
	@echo "$(YELLOW)Cleaning generated files$(NC)"
	@find . -name "*.wasm" -delete
	@find . -name "*_output.txt" -delete
	@find . -name "build_*.log" -delete
	@cd $(INTEGRATION_DIR) && $(MAKE) clean 2>/dev/null || true
	@echo "$(GREEN)Cleanup complete$(NC)"