# CLAUDE.md - SPMD Implementation for Go via TinyGo

## Project Overview

This workspace implements Single Program Multiple Data (SPMD) support for Go, similar to Intel's ISPC and Mojo's SIMD capabilities. The implementation extends TinyGo (which uses LLVM) rather than the main Go compiler, targeting WebAssembly SIMD128 as the proof of concept backend.

**Scope**: Full frontend (lexer, parser, type checker), TinyGo LLVM backend (SIMD128 + scalar fallback), `lanes`/`reduce` standard library packages, Node.js WASI E2E tests. Goal: compile ALL examples to both SIMD and scalar WASM.

## Key Concepts

- **SPMD**: All lanes execute the same program but on different data elements
- **Uniform**: Values that are the same across all SIMD lanes (regular Go values)
- **Varying**: Values that differ across SIMD lanes (`lanes.Varying[T]`)
- **Execution Mask**: Tracks which lanes are active during control flow
- **lanes.Count[T]()**: Number of SIMD lanes for type T (e.g., 4 for i32 on WASM128), compile-time known
- **lanes.Index()**: Current lane index (0 to Count-1) in SPMD context
- **lanes**: Standard library module for cross-lane functions
- **reduce**: Standard library module for reduction operations (varying to uniform)
- **Printf Integration**: `fmt.Printf` with `%v` on varying values shows mask-aware output: active lanes print values, inactive lanes show `_` (e.g., `[5 _ 15 _]`)

## Go SPMD Syntax

```go
import "lanes"

// Uniform values are regular Go values (no annotation needed)
var x int              // Scalar value, same across all lanes
var y float32          // Regular Go value = uniform

// Varying values use lanes.Varying[T]
var v lanes.Varying[float32]  // Vector value, different per lane

// SPMD loop construct
go for i := range 16 {
    // Loop body executes in SIMD fashion
    // i is automatically varying: [i, i+1, i+2, i+3]
}

// Builtins
lanes.Count[int](v)          // Returns SIMD width (e.g., 4)
lanes.Index()                // Returns current lane [0,1,2,3]

// Cross-lane operations
lanes.Broadcast(value, lane)         // Broadcast from one lane to all
lanes.Rotate(value, offset)          // Rotate values across lanes
lanes.Swizzle(value, indices)        // Arbitrary permutation
lanes.RotateWithin(value, offset, n) // Rotate within groups of n lanes
lanes.SwizzleWithin(value, idx, n)   // Swizzle within groups of n lanes
lanes.ShiftLeftWithin(value, cnt, n) // Shift left within groups of n lanes
lanes.ShiftRightWithin(val, cnt, n)  // Shift right within groups of n lanes
```

## Implementation Architecture

### GOEXPERIMENT=spmd Gating

- All SPMD functionality behind `GOEXPERIMENT=spmd` runtime flag
- Single compiler binary handles both SPMD and standard Go modes
- All SPMD syntax, type rules, and SSA generation gated behind `buildcfg.Experiment.SPMD`
- Standard Go files work in both modes -- no special build tags required

### Phase 1: Go Frontend (COMPLETED -- 53 commits)

Lexer, parser, type system with `lanes.Varying[T]` (compiler magic, not regular generics). 42 SPMD vector opcodes in `cmd/compile/internal/ssa`. Full type checking with ISPC-based return/break restrictions. All gated behind `GOEXPERIMENT=spmd`.

### Phase 2: TinyGo LLVM Backend (IN PROGRESS -- 82 commits)

**Critical**: TinyGo uses `go/parser` + `go/types` + `golang.org/x/tools/go/ssa` (standard library), NOT `cmd/compile` internals. The 42 Phase 1 opcodes are invisible to TinyGo. Vectorization happens in the TinyGo compiler layer via direct LLVM IR generation.

Key files: `compiler/compiler.go`, `compiler/spmd.go`, `compiler/symbol.go`, `compiler/func.go`, `compiler/interface.go`

### Phase 3: Validation (NOT STARTED)

Dual-mode testing (SIMD vs scalar WASM) and performance benchmarking. See `docs/poc-testing-workflow.md`.

### SSA Generation Strategy

Follows ISPC's approach: direct LLVM operations, no custom opcodes. Vector types (`<4 x i32>`), explicit mask threading (`<N x i1>`), control flow linearization with LLVM select merges, per-lane break masks, contiguous GEPs + masked load/store. See `docs/ssa-generation-strategy.md`.

## Critical Implementation Rules

### Type System Rules

1. **Assignment Rule**: Varying values cannot be assigned to uniform variables
2. **Implicit Broadcast**: Uniform values automatically broadcast when needed, preserved as uniform as long as possible
3. **Control Flow**: All control flow (if/for/switch) can use varying conditions via masking in SPMD context
4. **Select Support**: `select` statements can use channels carrying varying values
5. **Return/Break Rules**: In `go for` loops, return/break allowed under uniform conditions only; forbidden under varying conditions or after mask alteration (continue in varying context). Continue always allowed.
6. **Nesting Restriction**: `go for` loops cannot be nested within other `go for` loops
7. **SPMD Function Restriction**: Functions with varying parameters cannot contain `go for` loops
8. **Public API Restriction**: Only private functions can have varying parameters (except lanes/reduce builtins)

See `docs/spmd-control-flow-masking.md` for transformation examples and detailed masking rules.
See `docs/spmd-type-checker-enforcement.md` for enforcement pseudocode and test coverage matrix.

### Function Semantics

1. Functions with varying parameters are "SPMD functions"
2. SPMD functions receive an implicit mask parameter **as the first parameter** in SSA
3. SPMD functions carry mask around all operations
4. Return behavior: no varying params -> unmasked varying; has varying params -> masked varying
5. Varying can be passed as `interface{}`/`any` (reflect exposes as uniform arrays)

## Reference Materials

- **Blog posts**: `bluebugs.github.io/content/blogs/` -- go-data-parallelism, practical-vector, cross-lane-communication, go-spmd-ipv4-parser
- **ISPC**: `ispc/src/` -- parser.yy, type.cpp, ctx.cpp, stmt.cpp (reference SPMD implementation)
- **Go compiler**: `go/src/cmd/compile/` -- syntax/, types2/, ssagen/
- **TinyGo**: `tinygo/compiler/`, `tinygo/transform/`
- **Academic**: [Predicated SSA](https://cseweb.ucsd.edu/~calder/papers/PACT-99-PSSA.pdf) -- mask-based execution for SIMD

## Development Workflow

### TinyGo SPMD Build Commands

```bash
# Build everything (Go toolchain + TinyGo)
make build

# Build just the Go toolchain
make build-go

# Build just TinyGo (requires Go built first)
make build-tinygo

# Compile an SPMD example to WebAssembly (via Makefile)
make compile EXAMPLE=hex-encode

# Compile SPMD to WebAssembly (manual)
# IMPORTANT: The forked Go must be on PATH so TinyGo's `go env` finds it.
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -o output.wasm examples/simple-sum/main.go

# Execute with wasmer-go
go run wasmer-runner.go output.wasm

# Dual mode testing
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=true -o test-simd.wasm main.go    # SIMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=false -o test-scalar.wasm main.go  # Scalar
wasm2wat test-simd.wasm | grep "v128"  # Inspect SIMD instructions

# Verify experiment gating (should work without SPMD syntax)
tinygo build -target=wasi examples/simple-sum/main.go
```

### Agent Workflow (MANDATORY)

All implementation work MUST follow this 3-step pipeline:

1. **`golang-pro` agent**: Performs all code implementation
2. **`code-reviewer` agent**: Reviews all changes -- only proceed if reviewer approves
3. **`clean-commit` agent**: Creates the final git commit only after review passes

Never skip steps. Never commit without review approval.

### Deferred Items Management (MANDATORY)

All deferred work MUST be documented in the "Deferred Items Collection" section of PLAN.md with: Task, Location, Status, Depends On, Implementation, Priority, Related. Never leave undocumented TODOs or mention deferrals without a PLAN.md entry.

### Git Commit Guidelines

- **Atomic**: One logical change per commit, code must compile at each step
- **Concise messages**: Imperative verb ("Add", "Fix", "Implement"), 50 char max summary, NO EMOJIS
- **Immediate testing**: Add tests with every change (parser, type checker, LLVM IR, WASM execution)
- **Repository consistency**: Same rules for Go fork, TinyGo fork, SPMD workspace, examples
- Maintain `spmd` branch in each fork. Update README.md and PLAN.md with progress.

### Testing Strategy

- **Parser Tests**: Valid/invalid syntax recognition (Go frontend)
- **Type Tests**: Uniform/varying rules, SPMD function marking (Go frontend)
- **LLVM Tests**: Verify vector IR generation and WASM SIMD128 output (TinyGo backend)
- **Runtime Tests**: Execute WASM binaries and verify behavior (Node.js WASI / wasmer-go)
- **SIMD Verification**: Inspect generated WASM for proper SIMD instruction usage

### Common Pitfalls

1. Don't confuse `go for` (SPMD) with `go func()` (goroutine)
2. Remember mask propagation through nested control flow
3. Ensure varying operations generate vector LLVM IR
4. Prevent LLVM from scalarizing vector operations
5. Handle edge cases like varying array indices

## Current Implementation Status

**Phase Summary**: Phase 1 (Go frontend) complete, 53 commits; Phase 2 (TinyGo LLVM backend) in progress, 54 commits; Phase 3 (validation) not started. See PLAN.md for detailed task breakdown and deferred items.

### Phase 1: Go Frontend (COMPLETED)

Lexer, parser, type system with `lanes.Varying[T]`, full SPMD type checking (ISPC semantics), 42 SSA opcodes, mask propagation, lanes/reduce builtin interception, SPMD function signatures. All gated behind `GOEXPERIMENT=spmd`.

### Phase 2: TinyGo LLVM Backend (IN PROGRESS)

- **2.0-2.0d** (DONE): Go stdlib porting (go/ast, go/parser, go/types); SPMD metadata extraction
- **2.1-2.9c** (DONE): GOEXPERIMENT support, LLVM vector types, SPMD loop lowering, control flow masking, function calls, builtin interception, break masks, *Within cross-lane ops, varying switch, compound booleans, vector index, bounds check elision, store coalescing, gather shift-load expansion
- **Predicated SSA** (DONE): go/ssa linearizes varying control flow (if/else, switch, boolean chains) into SPMDSelect/SPMDLoad/SPMDStore/SPMDIndex
- **SSA-level loop peeling** (DONE): go/ssa splits loops into main (all-ones mask) + tail (masked). TinyGo consumes mechanically.
- **Mask stack removed** (DONE): All memory op masking migrated to SSA level (explicit masks on SPMDLoad/SPMDStore). spmdMaskStack/push/pop/current removed. Interleaved store analysis migrated to scan SPMDStore.
- **2.9-2.10** (REMAINING): Varying for-loop masking, lanes.Rotate/Swizzle (full-width), scalar fallback mode
- **Key Metrics**: Mandelbrot ~3.19x SPMD speedup (0 diffs vs serial); hex-encode Dst ~4.5x, Src ~14.1x (wasmtime)
- **E2E Results**: 42 run pass, 42 compile pass, 1 compile fail, 0 run fail, 11 reject OK (54 total)

### Phase 3: Validation (NOT STARTED)

Syntax migration completed (5 commits, ~55 files). Dual-mode testing and benchmarking remain. See `docs/poc-testing-workflow.md`.

**E2E Compile Failures** (1 remaining):

- **Missing features (1)**: base64-decoder (varying indexing `r[varyingIndex]` + type inference for ShiftLeft)

**Next Priority**: (1) Implement scalar fallback mode, (2) Fix base64-decoder varying indexing, (3) Phase 3 validation

## Debugging Tips

- Add `-d=ssa/all/dump` flag to see SSA generation
- Use `wasm2wat` to verify SIMD instructions in output
- Check LLVM IR for vector types and operations
- Verify mask propagation with control flow tests
- Compare against ISPC's generated code for similar patterns
