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

### Phase 2: TinyGo LLVM Backend (IN PROGRESS -- 90+ commits)

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

**Phase Summary**: Phase 1 (Go frontend) complete, 53 commits; Phase 2 (TinyGo LLVM backend) in progress, 90+ commits including v6→v6.1→v7→v8 width-typed Varying chain; Phase 3 (validation) ongoing — full e2e at **105/94/0/93/0/11** ("All tests passed!") + perf restored to or beyond pre-v6.1 baseline. See PLAN.md for detailed task breakdown and deferred items.

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
- **Key Metrics** (wasmtime, SIMD vs scalar SPMD): Hex-encode Dst **~6-9x**; Mandelbrot **~2.5-3.6x**; lo-sum/mean/min/max **~2-3x**; lo-clamp **~2-3x**
- **Key Metrics** (x86-64 AVX2 8-wide, SPMD vs scalar): lo-min **7.27x**, lo-max **7.18x**, mandelbrot **6.07x**, lo-sum **5.09x**, lo-clamp **4.82x**, lo-mean **3.66x**
- **Key Metrics** (x86-64 SSE 4-wide, SPMD vs scalar): lo-min **2.63x**, lo-max **2.59x**, lo-sum **2.61x**, mandelbrot **3.71x**, hex-encode dst **6.31x**
- **Key Metrics** (base64 Mula-Lemire hot loop, AVX2): **0.44 instrs/byte** (was 14.3 with scatter-gather) — 32x instruction reduction, 1 vpshufb per 32 bytes
- **lanes.CompactStore** (2026-04-08): New SIMD compress-store builtin. Writes active lanes contiguously, returns count. Constant-mask path uses pshufb/swizzle + vector store.
- **SPMDMux** (2026-04-10): Collapses SPMDSelect chains from `i % K` patterns into single per-lane index selection. Handles NEQ masks via X/Y swap.
- **SPMDInterleaveStore** (2026-04-10): Replaces SPMDMux + CompactStore with diagonal-extraction shuffles + ORs + compaction + contiguous store. Eliminates 200+ instruction scatter chain → ~7 instructions.
- **lanes.CompactStore** (2026-04-08): SIMD compress-store builtin. SPMDMux + SPMDInterleaveStore chain for deinterleave patterns.
- **Base64 Mula-Lemire decoder** (2026-04-12): Three cascading `go for` loops (byte→int16→int32) trigger pmaddubsw/pmaddwd. `lanes.Count[byte]()` for chunkSize ensures single-iteration unrolling. Byte-decomposition store for output compaction.
- **Key Metrics** (base64 decode, SPMD, 100KB): SSSE3 **~8.5 GB/s**, AVX2 **~17 GB/s**, WASM **~6 GB/s** (wasmtime; varies by host)
- **Key Metrics** (base64 decode, SPMD vs Go stdlib): AVX2 **~9x** faster than `encoding/base64` (~1.9 GB/s)
- **Key Metrics** (base64 decode, SPMD vs simdutf C++): simdutf AVX2 ~22 GB/s vs SPMD AVX2 ~17 GB/s — **~77% of simdutf** (~23% gap)
- **Key Metrics** (hex-encode vs Go stdlib `encoding/hex`, 1024 bytes): WASM dst **4.84x**, src **1.68x**; x86 AVX2 dst **13.01x**, src **1.31x**; x86 SSSE3 dst **7.77x**; x86 SSE no-pshufb dst **0.56x** (slower — decomposed path without LUT). Re-benchmarked 2026-04-18.
- **Hex-encode key insight**: pshufb LUT path is the critical optimization; vector width is secondary. SSSE3 (98.9us) vs AVX2 (59us) for dst is only 1.67x ratio despite 2x width. Without pshufb (SSE2 only), dst is slower than scalar stdlib.
- **Compiler optimizations** (2026-04-06): SwizzleWithin const-only, spmdSwizzleWithTable AVX2 fix, direct store on all-ones mask, vpmaddubsw/vpmaddwd pattern detection (x86+WASM), DotProductI8x16Add removed
- **Compiler optimizations** (2026-04-09/10): x86 feature implication chain (+avx2 implies +ssse3), swizzle fallback lane count fix, constant-mask SPMDSelect fast-path, decomposed REM power-of-2 optimization, AVX2 cross-lane compaction fix
- **Compiler optimizations** (2026-04-11/12): All-ones mask load fast-path, LICM for SPMD compilations, InterleaveStore detection fixes (NEQ masks, callee.Pkg nil, Indices mapping), byte-decomposition store (bitcast+pshufb+store for stride-S interleaved stores extracting bytes from wider types, SSE+AVX2+WASM)
- **E2E Results** (post-v8): **105 total**, 93 run-pass, 94 compile-pass, 0 compile-fail, 0 run-fail, 11 reject-pass — "All tests passed!"

### v6→v6.1→v7→v8 chain (2026-04-30 → 2026-05-07): width-typed Varying

- **v6 Phase 1 Part A** (tinygo `d8f29a42`): defensive `createSPMDStore` lane-count reconciliation — emits `spmdReshapeVector` (shufflevector with last-element clamp) when val/addr vector widths differ.
- **v6 Phase 2 Part B** (x-tools-spmd `2dd8d1357` / tinygo `dda72362`): SSA lift guard for `lanes.Varying[T]` allocas + `*types.SPMDType.Lanes()` field carrying width through the type system. Fixes n-body NaN (per-pair accumulators in tail iters were unmasked).
- **v6.1** (x-tools-spmd `0d7838f11` then `b77d54398` "scope-based" classifier): Pass A loop-local discriminator scopes type width-fixing to allocas referenced in the loop's scope blocks. Plus phi-edge `*Const` retyping. Plus compound-boolean `findElseSubgraphPredOfThen` for `(A && B) || (C && D)` chains. Cascade fixes (multi-step): tinygo `7c339f21` (contiguous-via-SPMDLoad + write-time normalizations), `9f43228c` (swizzle-within Convert/SPMDLoad trace), `905a104a` (analyzeSPMDLoops Pass 1/2 structural IsRangeIndex matching). Bit-counting expected fixed (parent `c03105c9` → 32, was wrongly 28).
- **v7 Phase 1** (tinygo `175a82ef`): `getLLVMType` for `*types.SPMDType` Struct/Array branch honors `typ.Lanes()` — fixes `Varying[[]int]` alloca size from `[1 x slice]` to `[N x slice]`. The actual array-counting failure was stack corruption from the undersized alloca, not divergent inner loop semantics. v7's broader divergent-inner-loop spec was unnecessary.
- **v8 Phase 1** (x-tools-spmd `950eaa2b`): narrow lift guard to **non-vectorizable** Varying[T] only (slice/struct/array/interface). Vectorizable types (int, float, byte, pointer) lift back into SSA phi nodes for performance. Adds `spmdElemNonVectorizable` helper.
- **v8 Phase 2** (x-tools-spmd `7920ef1a`): `spmdMaskTailBodyBackEdges` inserts `SPMDSelect(tail_mask, new_value, pre_body_value)` on tail-body loop-header phi back-edges. Inactive lanes preserve previous-iter phi value. Plus `spmdFixBlockPhiTypes` repair helper for trampoline/done block phis. Restores n-body correctness AFTER v8 Phase 1 narrowing.
- **Parent submodule bump** (`d61874a`): incorporates v8 + spec/plan docs.

### Performance — post-v8 vs pre-v6.1 baseline

- **WASM SIMD128 (lo-* hot loops)**: lo-sum 277ns/2.37x (baseline 308ns/2.50x — 10% better), lo-mean 282ns/2.33x (baseline 332ns/2.19x — better), lo-min 277ns/2.38x (better), lo-max 278ns/2.38x (better), lo-clamp 6139ns/1.76x (better), lo-contains 125ns/5.25x (same).
- **x86-64 AVX2 mandelbrot**: ~933µs / 6.5x — within 15% of pre-v6.1 baseline ~940µs.
- **Base64 AVX2 (canonical input, no validation in bench)**: 17-18 GB/s consistently (was 11-18 with high variance pre-v6.1; baseline ratio: 1KB SPMD beats simdutf 1.40x; 100KB-1MB ~67-70% of simdutf).

### Compiler quality validated

Disasm comparison (perf-analyzer dispatched 2026-05-07): TinyGo SPMD AVX2 base64 kernel emits **38 instrs per 32 bytes (1.19 instrs/byte)** for the Mula-Lemire pipeline — same vector ops (vpmaddubsw + vpmaddwd + vpshufb + vpermd) as simdutf hand-tuned C++. Per-byte instruction count is **at parity with hand-tuned intrinsics**. Remaining throughput gap at large sizes is structural (function-call boundary preventing constant hoisting; 4-pass cascade vs simdutf's 2-phase classify+flush pipeline), not codegen quality.

### Vectorized table lookup pattern

A `[16]byte{...}` constant indexed by a varying byte compiles to **one shuffle instruction** — `vpshufb` (x86 SSSE3/AVX2), `i8x16.swizzle` (WASM SIMD128), `tbl` (ARM NEON). Paired LUTs (high nibble × low nibble, AND'd together) match simdutf's per-byte instruction count for byte classification (e.g., base64 char validity). Documented in `bluebugs.github.io/content/blogs/writing-spmd-go.md` "Vectorized table lookup" section. Verified via base64 validation experiment: SPMD inline 23 GB/s with paired-vpshufb validation vs simdutf 13-27 GB/s — beats simdutf at 1KB by 1.75x, near parity at 10KB, ~0.79x at 100KB-1MB.

### Phase 3: Validation (IN PROGRESS)

Scalar fallback, dual-mode E2E, SIMD-vs-scalar benchmarking, x86-64 native (SSE + AVX2) all operational. Remaining: browser SIMD detection demo. See `docs/poc-testing-workflow.md`.

**E2E Compile Failures** (0 remaining); **E2E Run Failures** (0 remaining).

**Next Priority**: (1) Compiler `inlinehint` tuning — function-call boundary on hot SPMD functions blocks constant hoisting; closing this could push base64 to ~33 GB/s with validation, beating simdutf at all sizes; (2) Browser SIMD detection demo; (3) Outer-SPMD batching for IPv4 parser; (4) `lanes.Swizzle`-friendly stdlib examples (already-supported pattern, just needs visibility).

## Debugging Tips

- Add `-d=ssa/all/dump` flag to see SSA generation
- Use `wasm2wat` to verify SIMD instructions in output
- Check LLVM IR for vector types and operations
- Verify mask propagation with control flow tests
- Compare against ISPC's generated code for similar patterns
