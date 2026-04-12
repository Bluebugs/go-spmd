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
6. **Lane-count-dependent anti-pattern**: Code that uses `lanes.Index()` for per-lane computation or inspects individual lane values via `reduce.From` produces results that depend on the SIMD width. Such code is NOT portable across different lane counts (e.g., SIMD vs scalar mode, or 128-bit vs 256-bit SIMD). Correct SPMD code should use reductions (`reduce.Add`, `reduce.Max`, etc.) to produce lane-independent scalar results. Future: detect with golangci-lint rule.

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
- **2.9-2.10** (REMAINING): Varying for-loop masking
- **Scalar fallback mode** (DONE): `-simd=false` flag, `SIMDRegisterSize` in `types.Config`, `spmdUsesSIMD()` helper
- **SPMDStore merge** (DONE): SSA-level optimization merges consecutive stores to same address into single store + chained SPMDSelect
- **Dual-mode E2E** (DONE): Level 8 (identical output, 8 tests) + Level 9 (scalar-validated, 16 tests + 1 compile-only)
- **SIMD vs scalar benchmark** (DONE): `test/e2e/spmd-benchmark.sh` with wasmtime
- **x86-64 native target** (DONE): SSE + AVX2. Rangeindex narrowing, unified mask wrapping, elseif linearization, fullload alignment, decomposed path on x86, AVX2 vpshufb table duplication.
- **AVX2 256-bit mode** (DONE): `SIMDRegisterSize` detects `+avx2` → 32 bytes. Parameterized `spmdLaneCount`, `spmdMaskElemType`, x-tools-spmd `SIMDRegisterBits`. Deferred mask resolution at materialization points. `lanes.From` caps to context lane count.
- **x86-64 benchmark** (DONE): `test/e2e/spmd-benchmark-x86.sh` — compares SPMD vs samber/lo generic vs lo/exp/simd AVX2
- **x86-64 E2E** (DONE): Level 10 (SSE) + Level 11 (AVX2) in `spmd-e2e-test.sh`
- **Key Metrics** (wasmtime, SIMD vs scalar SPMD): Hex-encode Dst **~8.9x**; Mandelbrot **~3.03x**; lo-sum/mean/min/max **~2.3-2.4x**; lo-clamp **~2.82x**
- **Key Metrics** (x86-64 AVX2 8-wide, SPMD vs scalar): lo-min **7.27x**, lo-max **7.18x**, mandelbrot **6.07x**, lo-sum **5.09x**, lo-clamp **4.82x**, lo-mean **3.66x**
- **Key Metrics** (x86-64 SSE 4-wide, SPMD vs scalar): lo-min **2.63x**, lo-max **2.59x**, lo-sum **2.61x**, mandelbrot **3.71x**, hex-encode dst **6.31x**
- **Key Metrics** (base64 Mula-Lemire hot loop, AVX2): **0.44 instrs/byte** (was 14.3 with scatter-gather) — 32x instruction reduction, 1 vpshufb per 32 bytes
- **lanes.CompactStore** (2026-04-08): New SIMD compress-store builtin. Writes active lanes contiguously, returns count. Constant-mask path uses pshufb/swizzle + vector store.
- **SPMDMux** (2026-04-10): Collapses SPMDSelect chains from `i % K` patterns into single per-lane index selection. Handles NEQ masks via X/Y swap.
- **SPMDInterleaveStore** (2026-04-10): Replaces SPMDMux + CompactStore with diagonal-extraction shuffles + ORs + compaction + contiguous store. Eliminates 200+ instruction scatter chain → ~7 instructions.
- **Key Metrics** (base64 v1, shift/OR + InterleaveStore, 1MB): SSSE3 **24.55x** (4293 MB/s, 0.70 cyc/B), AVX2 **29.42x** (5178 MB/s, 0.58 cyc/B), WASM **13.37x** (1663 MB/s, 1.80 cyc/B)
- **Key Metrics** (base64 v2, pmaddubsw + cascading loops, 1MB): SSSE3 **4208 MB/s**, AVX2 **6509 MB/s**, WASM **2740 MB/s**
- **Key Metrics** (base64 decode, SPMD vs Go stdlib): AVX2 v2 **11.8x** faster than `encoding/base64` (~550 MB/s)
- **Key Metrics** (base64 decode, SPMD vs simdutf C++): simdutf AVX2 19930 MB/s vs SPMD AVX2 v2 6509 MB/s — 3.1x gap (Loop 4 byte-extract compaction 24 instrs vs simdutf's vpshufb+vpermd 3 instrs)
- **Base64 v2 decoder** (2026-04-11): Three cascading `go for` loops (byte→int16→int32) trigger pmaddubsw/pmaddwd. `lanes.Count[byte]()` for chunkSize ensures single-iteration unrolling. Pack phase at parity with simdutf (2 instrs each).
- **Compiler optimizations** (2026-04-06): SwizzleWithin const-only, spmdSwizzleWithTable AVX2 fix, direct store on all-ones mask, vpmaddubsw/vpmaddwd pattern detection (x86+WASM), DotProductI8x16Add removed
- **Compiler optimizations** (2026-04-09/10): x86 feature implication chain (+avx2 implies +ssse3), swizzle fallback lane count fix, constant-mask SPMDSelect fast-path, decomposed REM power-of-2 optimization, AVX2 cross-lane compaction fix
- **Compiler optimizations** (2026-04-11): All-ones mask load fast-path (plain vmovdqu/v128.load), LICM for SPMD compilations (loop-simplify+lcssa+licm), InterleaveStore NEQ mask fix + callee.Pkg nil fix + Indices mapping fix
- **E2E Results**: 90 run pass, 91 compile pass, 0 compile fail, 0 run fail, 11 reject OK (102 total)

### Phase 3: Validation (IN PROGRESS)

Scalar fallback, dual-mode E2E, SIMD-vs-scalar benchmarking, x86-64 native (SSE + AVX2) all operational. Remaining: browser SIMD detection demo. See `docs/poc-testing-workflow.md`.

**E2E Compile Failures** (0 remaining)

**Next Priority**: (1) Loop 4 byte-extract compaction — `lanes.ReinterpretBytes` to enable vpshufb+vpermd instead of 24-instr shuffle tree (closes 3.1x gap to simdutf), (2) Browser SIMD detection demo, (3) Outer-SPMD batching for IPv4 parser

## Debugging Tips

- Add `-d=ssa/all/dump` flag to see SSA generation
- Use `wasm2wat` to verify SIMD instructions in output
- Check LLVM IR for vector types and operations
- Verify mask propagation with control flow tests
- Compare against ISPC's generated code for similar patterns
