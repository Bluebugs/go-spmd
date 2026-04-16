# Design Spec: SPMD-for-Go Blog Series

**Date:** 2026-04-15
**Author:** Cedric Bail
**Status:** Draft

---

## Overview

A series of 9 standalone blog articles presenting the results of the SPMD-for-Go proof of concept. Each article can be read independently but links to the others. The series covers the pitch (with live WASM demos), a developer how-to, compiler internals, technique deep-dives, and a negative result.

**Voice:** Conversational, first-person, confident but kind. "Here's what we built and what we learned" — not speculative ("what if?") like the original blog series, and not boastful. Canadian.

**Platform:** Hugo site at `bluebugs.github.io/`, using the existing Ananke theme with custom shortcodes. New shortcodes needed for live WASM demos.

---

## Article 1 — "SPMD for Go: What If Your Loops Were 9x Faster?"

**Audience:** Go developers broadly, Go compiler team secondarily.
**Goal:** Get shared. Make the case that SPMD belongs in Go.
**Length:** ~1500 words + 2 live demos.

### Structure

1. **Hook** (~100 words). "We wrote a base64 decoder in 40 lines of Go. It runs at ~17 GB/s on AVX2 — ~9x faster than `encoding/base64` and within 77% of the best C++ SIMD library. Here's the live demo." Link to Article 2 for how to write this code, Article 3 for how the compiler does it.

2. **Live WASM demo: Mandelbrot** (~200 words + shortcode). Side-by-side HTML canvas — scalar Go on the left rendering row by row, SPMD Go on the right rendering visibly faster. Both compiled from the same `examples/mandelbrot/main.go` source, one with `-simd=true`, one with `-simd=false`. The visual difference is the argument. Show the Go source beneath the canvases — readers can see it's normal-looking Go with `go for` and `lanes.Varying[float32]`.

3. **Live WASM demo: Base64** (~200 words + shortcode). Text area with sample base64 input (default: the "quick brown fox" test string from the PoC). Three buttons: "Go stdlib", "SPMD WASM". Each runs the decode, displays throughput in MB/s. The reader watches the SPMD number be higher. Option to paste larger input. Show the `decodeAndPack` kernel source alongside — 40 lines, no intrinsics.

4. **The 30-second explanation** (~200 words). Three concepts: `go for` (SPMD loop), `lanes.Varying[T]` (vector value), `reduce.Add` (collapse to scalar). One minimal code example (the sum-a-slice function). "You write a loop; the compiler vectorizes it. The mask handles the tail. The type system tracks what's varying."

5. **Benchmark table** (~200 words). The real numbers from `spmd-benchmark-x86.sh` and `spmd-benchmark.sh`. Mandelbrot AVX2 6.07x, lo-min 7.27x, hex-encode WASM 8.9x, base64 AVX2 ~17 GB/s (~77% of simdutf, ~9x stdlib). Honest about IPv4 inner-SPMD at 0.58x — not everything speeds up, and we explain why (input too small for the shape).

6. **Why this belongs in the compiler** (~300 words). The core argument:
   - SPMD is not a library. It is a compiler feature that lives in the SSA — predication, loop peeling, and pattern detection are SSA transforms.
   - The mask-stack lesson: we tried bolting it on as a backend analysis. It failed. SPMD has to be at the heart of the SSA form.
   - Comparison with `simd/archsimd` (Go 1.26): complementary, not competing. `archsimd` is instruction-level ("SIMD as `syscall`"); SPMD is loop-level ("SIMD as `go for`"). `archsimd` is right for `crypto` internals where you want a specific instruction; SPMD is right for application code where you want the compiler to pick the best instructions from idiomatic Go. The Go team's planned portable high-level API on top of `archsimd` is a third point in the design space. All three can coexist.

7. **Where SPMD would help in the stdlib** (~200 words). Two categories:
   - Image processing: `image/draw`, `image/color` conversions, JPEG/PNG decode pipelines. Per-pixel arithmetic is the golden SPMD case.
   - Byte parsing: HTTP header scanning, JSON structural character detection, `go/scanner` tokenization, encoding/hex, encoding/base64. The PoC's hex-encode and base64 examples are proofs of concept for this whole category.

8. **Closing** (~100 words). "The PoC is open source at [repo]. We'd welcome feedback from the Go community — whether you're a developer who'd use this, or a compiler engineer who sees how to do it better." Link to all other articles in the series.

### Technical requirements

- Two new Hugo shortcodes: `spmd-demo-mandelbrot` and `spmd-demo-base64`.
- Each shortcode loads two pre-compiled WASM binaries (scalar + SPMD) and runs them via a JavaScript harness.
- Mandelbrot: uses `<canvas>` for rendering, `requestAnimationFrame` for the scalar version to show progressive rendering, SPMD version renders all at once.
- Base64: uses `performance.now()` for timing, runs multiple iterations, displays MB/s.
- WASM binaries compiled from the existing `examples/mandelbrot/main.go` and `examples/base64-decoder/main.go` with appropriate build flags.
- Fallback: if the browser doesn't support WASM SIMD, show a message and static benchmark numbers instead.

---

## Article 2 — "Writing SPMD Go: A Practical Guide"

**Audience:** Go developers who want to write SPMD code.
**Goal:** Teach the mental model and idioms.
**Length:** ~2500 words.

### Structure

1. **The mental model** (~400 words). Uniform vs. varying. Implicit broadcast. The assignment rule (varying to uniform forbidden except via reduction). Where varying values come from: `go for` iteration variable, slice loads, `lanes.Index()`.

2. **Your first `go for`** (~300 words). Sum-a-slice example with `reduce.Add`. Line-by-line walkthrough. What the compiler does (main body + tail, vectorized add, horizontal reduction).

3. **The golden pattern** (~200 words). `out[i] = transform(in[i])` — contiguous load, contiguous store, all-ones mask in the peeled main body. "Almost every loop that hits 5x or better in the PoC has this shape."

4. **Reductions and the `lanes.Index()` anti-pattern** (~300 words). Why `reduce.From` is a code smell. Dual-mode diff as the current detection method. A static analyzer should catch this in the future — the properties are syntactic and local. The `iota` suggestion: `lanes.Index()` is morally just `iota` promoted to varying form.

5. **Control flow** (~300 words). Allowed: varying if/switch/&&/||, continue, inner scalar for. Forbidden: return/break under varying, panic, nested `go for`, `go for` in SPMD functions. Public vs. private SPMD functions.

6. **Performance patterns** (~400 words). Cascading `go for` for widening multiply-add (the base64 pattern). Chunk sizing with `lanes.Count[T]()`. Byte-lane vs int-lane iteration (AVX2: 32 vs 8 lanes). Outer-SPMD batching when inner-SPMD is the wrong shape.

7. **Debugging** (~200 words). `fmt.Printf("%v", v)` shows `[5 _ 15 _]`. Dual-mode build + diff. Reading `wasm2wat` / `llvm-objdump` output. Common error messages.

8. **Worked examples** (~400 words). Hex-encode (both dst-centric and src-centric variants, why performance differs across targets). Mandelbrot (divergent iteration, per-lane break masks, SPMD function calls).

---

## Article 3 — "How SPMD Lives in the Compiler: Lessons from Building It"

**Audience:** Compiler engineers, Go contributors.
**Goal:** Explain the architecture and the hard-won lessons. Make clear that SPMD is an SSA-level feature.
**Length:** ~2500 words.

### Structure

1. **The mask-stack detour** (~500 words). The story: two forks already (Go + TinyGo), didn't want a third. Tried to reconstruct masks in the TinyGo backend without touching `go/ssa`. Built a per-compiler mask stack — push on varying scope entry, pop on exit, consult at every memory op. It worked for simple cases. Then varying switch, &&/|| chains, break under varying, inner scalar loops — each needed new push/pop sites. The walker was doing double duty (LLVM block order vs. Go control-flow semantics). Every bug was "the mask was wrong on this path." Deleted ~330 lines. Accepted the third fork.

   The lesson: SPMD is a compiler feature that has to live at the heart of the SSA form. You cannot bolt it on as a backend analysis. The bugs are proportional to the gap between what the SSA knows and what the backend needs.

2. **Predicated SSA** (~400 words). Three SPMD-aware SSA instructions: `SPMDLoad`, `SPMDStore`, `SPMDSelect`. Four metadata structures: `SPMDLoopInfo`, `If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`. The transform: varying `if` → execute both sides under masks, merge via select. The block-pointer invalidation trap after `optimizeBlocks()`.

3. **Where it goes for upstream Go** (~300 words). Prototyped in `golang.org/x/tools/go/ssa` because that's what TinyGo consumes. For upstream Go, the same patterns go into `cmd/compile/internal/ssa`. The 42 Phase-1 opcodes were the wrong shape (a flat list of vector ops) but the right location. The structured approach (SPMDLoopInfo, explicit masks, predication transforms) is what should replace them.

4. **The `lanes.Varying[T]` type magic** (~300 words). Package-based over keyword — why. The `*_ext_spmd.go` extension-file pattern. types2/go/types duplication pain. The type lattice surprises: `&Varying[T]` → `Varying[*T]`, `Varying[[N]T][i]` → `Varying[T]`, `*Varying[Struct].Field` → `Varying[FieldType]`. Design these in from day one.

5. **Scalar fallback as correctness oracle** (~200 words). `-simd=false` → laneCount=1. Dual-mode E2E Level 8. The five crash categories it uncovered. "Do not ship without this."

6. **Optional: step-through visualization** (shortcode). A JavaScript-based visualization (existing shortcode style) showing how a varying `if` inside a `go for` gets predicated — lanes, masks, and SPMDSelect at each step. Applied to the base64 nibble-LUT decode (loop 1 of `decodeAndPack`): the `if ch == byte('+')` branch shows some lanes active, others masked.

7. **What we'd do differently** (~200 words). Unified types2/go/types. First-class mask width in the type system. Real calling convention for the mask parameter. `iota` for `lanes.Index()`. Smaller `compiler/spmd.go` (file-per-concern from day one).

---

## Article 4 — "Pattern Matching Beats Hand-Written SIMD"

**Audience:** Compiler engineers, SIMD library authors, anyone designing a SIMD API.
**Goal:** Prove that compiler pattern detection on idiomatic Go outperforms explicit cross-lane builtins.
**Length:** ~2000 words.

### Structure

1. **The base64 story** (~400 words). Two versions of the same decoder. v1: explicit cross-lane ops (CompactStore, Rotate, SwizzleWithin). Peaked at ~2x scalar. v2: four idiomatic `go for` loops with plain Go arithmetic. Hit ~77% of simdutf C++. Same algorithm, different expression, dramatically different performance.

2. **The `vpmaddubsw`/`vpmaddwd` detector** (~400 words). What Go idiom it recognizes: `int16(a[i*2])*C + int16(a[i*2+1])`. How it fires. Constant decomposition for weights >127 (signed-byte limit). WASM fallback: deinterleave + widen + mul + add. Result: 0.44 instructions/byte vs 14.3.

3. **Byte-decomposition store** (~300 words). The stride-S store pattern (`out[i*3+0] = byte(tmp); out[i*3+1] = byte(tmp>>8); out[i*3+2] = byte(tmp>>16)`). Detection → bitcast + pshufb + masked store. Replaced ~1500 lines of CompactStore/SPMDMux/SPMDInterleaveStore machinery.

4. **Why simpler code wins** (~400 words). The compiler sees more than the developer. Pattern detection compounds (four detectors firing simultaneously in base64 v2). Cross-lane ops have hidden costs (variable-index swizzle = per-lane extract/insert).

5. **The `DotProductI8x16Add` cautionary tale** (~200 words). Added a builtin for IPv4 decimal conversion. Deleted when the pmadd detector subsumed it. ~163 lines gone. "Pattern detectors generalize; builtins don't."

6. **Closing** (~100 words). "If you're designing a SIMD API, invest in pattern detectors first, builtins second. Ship fewer primitives, not more."

---

## Article 5 — "Byte Iteration at 32 Lanes: The Decomposed Index Path"

**Audience:** Compiler engineers working on SIMD codegen.
**Goal:** Document a technique that transfers to any SIMD compiler for a managed-memory language.
**Length:** ~1200 words.

### Structure

1. **The problem** (~200 words). AVX2 byte-width = 32 lanes. Naive index vector `<32 x i64>` = 256 bytes of register state. LLVM GEP scalarization. SExt corruption.

2. **The technique** (~300 words). Scalar base pointer (GPR, incremented by laneCount per iter) + constant `<N x i8>` offset (loaded once). Combine at GEP. i64→i32 truncation on x86-64.

3. **Why it enables "as many lanes as bytes"** (~200 words). Offset is i8 → matches register width. Base is scalar → no vector register cost.

4. **The bugs it fixed** (~200 words). Hex-encode Bugs 2 (SExt) and 3 (AVX2 vpshufb halving). Alignment SIGSEGVs.

5. **Power-of-2 modulo interaction** (~100 words). `offset & (N-1)` on the `<N x i8>` vector.

6. **Closing** (~100 words). "Build this from day one if you want byte-granular iteration on wide SIMD."

---

## Article 6 — "16 Bytes That Saved a Thousand Branches"

**Audience:** WASM runtime developers, anyone doing SIMD on WASM.
**Goal:** Document the cheapest optimization in the PoC.
**Length:** ~800 words.

### Structure

1. **The problem** (~150 words). WASM `v128.load` traps if it reads past the end of linear memory. Tail loads read up to 15 bytes past valid data. Conventional solutions are expensive (bounce buffer, scalar loads, load_lane).

2. **The trick** (~200 words). Reserve 16 bytes at the top of WASM linear memory. `heapEnd = memory.size * 64KB - 16`. Cost: 16 bytes out of minimum 64KB. Now every `v128.load` is safe.

3. **The overread+mask sequence** (~200 words). `v128.load` → lane-index constant → `icmp ult` → `sext` → `and`. Four instructions, no branches. Code snippet from `createSPMDVectorFromMemoryMasked`.

4. **x86 variant** (~100 words). Page-boundary check (`ptr & 0xFFF > 0xFF0`). ~99.6% fast path: raw `vmovdqu`, no masking.

5. **Closing** (~100 words). "Any WASM runtime with SIMD should do this. Scale the guard zone to match the register width."

---

## Article 7 — "How the Compiler Knows Your Load Is Contiguous"

**Audience:** Compiler engineers, performance-minded Go developers.
**Goal:** Explain the most important backend optimization and why it's non-trivial.
**Length:** ~1200 words.

### Structure

1. **Why it matters** (~200 words). Contiguous = one vector load. Not contiguous = gather/scatter (4-8x slower). This one question determines most of the benchmark delta.

2. **Why it's non-trivial** (~300 words). Real Go SSA has `ChangeType` (int32↔int64 for range-over-int), `BinOp ADD` chains (commutativity), constant folding hiding the iter phi.

3. **The recognizer** (~300 words). `spmdAnalyzeContiguousIndex`: recursive unwrap of BinOp ADD + ChangeType. `spmdUnwrapScalar`: peels ChangeType chains. Walk both sides of every add, trace through phis within the loop.

4. **The 38% improvement** (~200 words). Adding one unwrap case (ChangeType peel) gave 38% on contiguous stores. One recognizer extension, 38% speedup.

5. **Closing** (~100 words). "Invest disproportionately in contiguous-access recognition. Every percentage point of coverage is worth more than any other compiler work."

---

## Article 8 — "Loop Peeling: Where Most of the Speed Comes From"

**Audience:** Compiler engineers, anyone curious about how SPMD vectorization actually works.
**Goal:** Explain the single highest-leverage optimization.
**Length:** ~1200 words.

### Structure

1. **The structural split** (~200 words). Every `go for` → main body (all-ones mask, floor(N/laneCount) iterations) + tail check + tail body (runtime mask, at most once) + trampoline (phi routing).

2. **Why at SSA, not LLVM** (~200 words). LLVM's loop unroller doesn't know mask semantics. LLVM's vectorizer can't see across Go slice headers. Runtime tail masks are hard for LLVM to materialize correctly.

3. **The all-ones fast path** (~300 words). The reason peeling matters: main body's mask is statically known → `SPMDStore` becomes direct vector store (one memory op), not load-blend-store (three memory ops). This is where ~2x of the benchmark wins come from.

4. **Accumulator phi trampolining** (~200 words). Loop-carried varying values need phis that survive main → tail → done. The done-block phi trampoline routes the final value correctly.

5. **Scalar fallback** (~100 words). When `LaneCount <= 1`, skip peeling entirely — scalar build has zero overhead from this transform.

6. **Closing** (~100 words). "If you implement one optimization, implement peeling. Everything else is built on top of it."

---

## Article 9 — "We Built Cross-Lane SIMD Primitives. None of Them Helped."

**Audience:** Anyone designing a SIMD API. ISPC/Mojo engineers. Go team.
**Goal:** Document the most important negative result. Save others from building features nobody needs.
**Length:** ~1500 words.

### Structure

1. **What we built** (~200 words). Rotate (full-width const offset), Swizzle (runtime-indexed), RotateWithin, ShiftLeftWithin, ShiftRightWithin, SwizzleWithin. All const-only shufflevector lowering. All correct. All passing tests.

2. **What we measured** (~300 words). Benchmarked every example at multiple development points. At the end: every example uses zero cross-lane ops (or only Broadcast/Count/Index which are free). Base64 v1 (cross-lane, 2x) → v2 (zero cross-lane, ~77% of simdutf). Hex-encode, mandelbrot, lo-*, IPv4: none.

3. **Why the wins came from elsewhere** (~300 words). Cross-lane = register rearrangement. The real wins: memory layout (contiguous access analysis), instruction reduction (pattern detection), mask elimination (peeling + all-ones fast path), index-register pressure reduction (decomposed path). Register rearrangement is cheap; the other four are not.

4. **The market mismatch** (~200 words). ISPC/Mojo ship rich cross-lane vocabularies because graphics/physics have small-shuffle kernels (butterfly, neighborhood). Go's likely SPMD market — encoding, parsing, numerics, image processing — is contiguous-memory work where cross-lane moves are dead weight.

5. **What to ship in v1** (~200 words). `Broadcast` (free — splat), `Count[T]()` (compile-time constant), `Index()` (constant vector, or better yet `iota`). That's it. Maybe compile-time-const `Rotate` if a benchmark demands it. No `*Within`. No runtime-indexed `Swizzle`. Add more only when a real benchmark demands them.

6. **Closing** (~200 words). "Unused builtins are a tax on every future compiler change. Ship fewer, not more. The evidence says: pattern detection on idiomatic Go delivers more performance from simpler code than any number of cross-lane primitives."

---

## Cross-cutting concerns

### Links between articles

Each article ends with a "Further reading" section that links to related articles:
- Article 1 (marketing) links to all others.
- Articles 4-9 (deep-dives) link back to Article 1 (for the pitch) and Article 3 (for the compiler context).
- Article 2 (developer guide) links to Article 1 (motivation) and Articles 4, 9 (patterns and anti-patterns).

### Code snippets

All code snippets come from the actual PoC source files (`examples/`, `tinygo/compiler/spmd.go`, `x-tools-spmd/go/ssa/`). No invented examples. Each snippet includes a `file:line` attribution.

### Benchmark numbers

All numbers from fresh runs of `test/e2e/spmd-benchmark-x86.sh` and `test/e2e/spmd-benchmark.sh` (re-benchmarked 2026-04-15). Key numbers:
- Base64 AVX2: ~17 GB/s (~77% of simdutf ~22 GB/s, ~9x Go stdlib ~1.9 GB/s)
- Mandelbrot AVX2: 6.07x
- lo-min AVX2: 7.27x
- Hex-encode WASM: 8.9x
- IPv4 inner-SPMD: 0.58x (honestly reported as a negative result)

### WASM demo infrastructure

Two new Hugo shortcodes needed:
- `spmd-demo-mandelbrot`: loads scalar.wasm + spmd.wasm, renders to dual canvases, shows timing.
- `spmd-demo-base64`: loads scalar.wasm + spmd.wasm, runs decode on user-provided input, shows MB/s.

Both need:
- A JavaScript WASI shim (or polyfill) for running TinyGo WASM in the browser.
- Feature detection for WASM SIMD (`WebAssembly.validate` with a simd128 test module).
- Graceful fallback to static numbers if SIMD not supported.
- Pre-compiled WASM binaries checked into `static/wasm/` or built by a Hugo build hook.

### Hugo integration

New articles go in `content/blogs/` following the existing front matter pattern. Each article has:
- TOML front matter with date, title, description, featured_image.
- `<!--more-->` tag after the hook paragraph for Hugo's summary truncation.
- Custom shortcodes for demos where needed.

### Writing order

Recommended implementation order:
1. **WASM demo infrastructure** (shortcodes + JS harness + WASM binaries) — blocks Article 1 and Article 3.
2. **Article 1** (marketing) — the one that gets shared first.
3. **Article 2** (developer guide) — what readers click to after Article 1.
4. **Article 3** (compiler internals) — for the Go team audience.
5. **Articles 4-8** (technique deep-dives) — in any order, independent.
6. **Article 9** (negative result) — natural closing piece.
