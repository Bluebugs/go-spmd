# SPMD for Go: Developer Guide

*How to think about SPMD in Go, how to write code that wins, and which tricks to avoid. Written for Go application developers who know the language but have never seen SPMD before.*

---

# §1 What is SPMD and why should you care

## §1.1 The one-sentence definition

**SPMD is "one loop body, many lanes, all running in lockstep over different data elements."** You write a loop once. The compiler runs it on N data elements simultaneously, using SIMD hardware. N is typically 4 to 32, depending on the register width of your target and the size of your data type.

This guide covers the SPMD extension to Go developed as a proof of concept in this repository: `lanes.Varying[T]` for varying values, `go for` as an SPMD loop construct, and the `lanes`/`reduce` standard library packages.

## §1.2 How SPMD differs from the alternatives

| Mechanism | Model | What you write | What it does |
|---|---|---|---|
| Goroutines | Concurrent, different programs | `go func() { ... }()` | Schedules a function on another thread |
| Hand-written assembly | Manual per-arch | `.s` files, Go assembler | Whatever you wrote, per-architecture, no preemption |
| `simd/archsimd` (Go 1.26, `GOEXPERIMENT=simd`) | Typed SIMD intrinsics | Methods on `Int32x8`, `Float64x4`, etc. | Compiler emits the matching machine instruction |
| Auto-vectorization | Implicit, fragile | Regular loops | Whatever the vectorizer can prove safe — usually nothing |
| **SPMD** | One program, many lanes | `go for i := range ... { ... }` | Compiler vectorizes the loop body |

SPMD is most useful when the other approaches either don't work or are too painful:

- **vs. goroutines.** SPMD is not concurrent. It runs in one thread, on vector registers. There's no scheduler overhead, no synchronization, no context switch. It is also not a substitute for goroutines — goroutines handle task-level parallelism, SPMD handles data-level parallelism. You often want both.

- **vs. hand-written assembly.** Before Go 1.26, hand-written `.s` files were the standard way to ship SIMD from Go (e.g., the routines behind `crypto/sha256`, `math/big`, the Go protobuf fast paths). The cost is real: you write one version per architecture; assembly functions block asynchronous preemption and defeat inlining; every new instruction set means a new file.

- **vs. `simd/archsimd` (the big one).** Go 1.26 shipped an experimental `simd/archsimd` package, enabled via `GOEXPERIMENT=simd`, which this guide's author considers the most important recent development in the Go SIMD space. It exposes fixed-width vector types as opaque structs — `Int8x16`, `Int32x8`, `Float64x4`, `Int16x32` on AVX-512, and so on — with operations as methods (`Add`, `Mul`, `MulAdd`, `Min`, `Max`, `And`, `ShiftLeft`, `Permute`, `Compress`, `LoadMasked`, `StoreMasked`, `Broadcast*`, `ConvertTo*`, etc.). Most methods compile to a single AMD64 instruction. The Go team has publicly stated that `archsimd` is the **low-level** layer of a planned two-tier design: `archsimd` is "SIMD as `syscall`" — architecture-specific, per-instruction; a future portable high-level API will be "SIMD as `os`," layered on top, with scalable-vector support for ARM64 SVE and RISC-V V.

  `archsimd` is a proper Go solution to SIMD and it is the right comparison point for an SPMD extension. The differences that matter:

  - **Portability of source.** `archsimd` targets AMD64 today; the Go team's roadmap is to add a portable high-level package later. SPMD Go as implemented in this PoC already compiles the *same source* to WASM simd128, x86 SSE, x86 AVX2, and scalar fallback. The decomposed index path (§5 of `implementer-notes.md`), the `vpmaddubsw`/`vpmaddwd` pattern detector (§7.1), and the byte-decomposition store (§7.5) all fire on every target from one kernel.

  - **Level of abstraction.** `archsimd` is at the instruction level. You pick your vector width (`Int32x8` for AVX2, `Int32x16` for AVX-512), you manage your own loop tail, you write your own mask computations, and you generally know which instruction each method emits. SPMD is at the loop level. You write `go for i := range xs`, `lanes.Varying[int32]`, and `reduce.Add`. The compiler picks the width, generates the tail, tracks the mask through control flow, and decides when to decompose indices.

  - **Control flow.** In `archsimd`, conditional computation inside a vector kernel is manual: you compute a mask, blend the two sides of the condition with `BlendVariable` or a mask-select method, and carry on. In SPMD, you write `if x > threshold { ... } else { ... }` and the compiler does the blend for you via predication at the SSA level (`novel-patterns.md` §3).

  - **Nested varying structure.** `archsimd` vector types are fixed-size structs: `Int32x8` is 8 lanes, full stop. SPMD's `lanes.Varying[T]` is type-constructor magic: you get `Varying[byte]` at byte lane count, `Varying[*MyStruct]` as a varying pointer, `Varying[[4]float32]` as a varying array, all without writing width numbers. The type system keeps track of which is which.

  - **What you cede.** With `archsimd` you keep control — if a specific instruction is the right call, you name it. With SPMD you hand that control to the compiler's pattern recognizers. When the recognizer is good (as it is for multiply-add widen, contiguous access, small-table lookup, byte-decomposition store, and reductions), you get competitive output without writing any SIMD yourself. When the recognizer is bad, you file a bug and wait.

  The honest framing is that `archsimd` and an SPMD extension are **complementary**, not competing, approaches:

  - `archsimd` is ideal for library authors who know exactly which instruction they want and who are willing to maintain per-architecture files for the foreseeable future. It is a direct improvement on hand-written assembly with real benefits (no preemption blocking, inlining, type safety, no .s files to maintain). For `crypto`, `math/big`, and similar hot internals, `archsimd` is probably the right choice today.

  - SPMD is ideal for application code — encoders, decoders, parsers, array math, image filters — where the developer wants to write natural Go and get good SIMD output on every target. It's also where the pattern-detection work in this PoC pays off the most, because the wins compound when the compiler can recognize several idioms in the same kernel (see the base64 decoder in §9.3, which hits 77% of simdutf C++ without any manual SIMD).

  A serious Go SIMD story probably wants both: `archsimd` underneath for cases where you need a specific instruction, SPMD on top for loop-level data parallelism. The Go team's planned portable high-level API on top of `archsimd` will be interesting to watch — our hypothesis is that it will look like a typed vector library (similar to Google's Highway in C++), whereas SPMD is a **language construct** (`go for`) that encodes varying-ness in the type system. Those are different points in the design space, and the comparison between them is the most important question any future Go SIMD work has to answer.

- **vs. auto-vectorization.** SPMD is explicit. You mark the loop with `go for`. The compiler has permission — and obligation — to vectorize. Auto-vectorizers have neither.

## §1.3 When SPMD wins

In this PoC, SPMD delivered measurable wins on:

- **Encoders and decoders.** Base64 decode hit **~77% of the simdutf C++ SIMD library's speed** on AVX2 (~17 GB/s vs simdutf's ~22 GB/s, **~9× faster** than Go's stdlib `encoding/base64`). Hex-encode hit **6-9×** on WASM simd128 (varies by host/runtime).
- **Math kernels.** Mandelbrot divergence detection: **6.07×** on AVX2, **3.71×** on SSE, **2.5-3.6×** on WASM (varies by host).
- **Array reductions.** `samber/lo` style min/max/sum/mean/clamp: **7.27× / 7.18× / 5.09× / 4.82× / 3.66×** on AVX2 respectively.
- **Parsers and format converters.** IPv4 parsing, hex encoding, byte transforms.

The common thread: **tight, regular loops over contiguous memory** where the per-element work is predictable.

### §1.3.1 A particularly good fit: the `image` stdlib

One Go stdlib area that is not especially prominent in Go's day-to-day but is a nearly-ideal SPMD target is the **`image` family**: `image`, `image/color`, `image/draw`, `image/jpeg`, `image/png`, `image/gif`. The workloads in those packages are exactly the kind of per-pixel data-parallel math that GPUs and SIMD hardware were built for:

- **`image/color` conversions** — RGB↔YCbCr, RGBA↔NRGBA, sRGB↔linear, alpha premultiplication — are per-pixel fixed arithmetic with constant coefficients. They map directly onto the cascading `go for` + multiply-add pattern (§7.1) that produced the base64 decoder's `vpmaddubsw` / `vpmaddwd` wins. A properly SPMD-ified `RGBA.At()` loop should get single-digit-multiples speedup on every target.
- **`image/draw`** is almost entirely "for each destination pixel, blend a source pixel with an optional mask" — contiguous loads, contiguous stores, varying `if` for the mask branch, per-lane arithmetic. This is the golden case for SPMD (§7.5). The compiler's all-ones fast path (main body in the peeled loop) turns `draw.Draw` into one vector load + one vector store per lane-count pixels.
- **JPEG and PNG pipelines** contain per-block and per-scanline transforms — inverse DCT, zig-zag unpacking, color-space conversion, Paeth filtering — that are classic SIMD territory. simdutf's cousin `simdjpeg`, Intel IPP, and the libjpeg-turbo SIMD paths are all structured around these exact transforms.
- **Filter and kernel passes** — blur, sharpen, edge detect, resize — are sliding-window contiguous loads with multiply-accumulate. The same shape as signal-processing kernels, which SIMD libraries have optimized heavily for decades.

This matters for the argument for SPMD-in-Go because **image processing is exactly the domain where the comparison with `archsimd` tilts most strongly toward SPMD.** Image kernels tend to be numerous, similar in shape, and frequently updated (new color spaces, new filters, new formats). Writing each one with `archsimd`'s instruction-level methods would mean a library of hand-tuned kernels per architecture, maintained forever. Writing them with `go for` + `lanes.Varying[T]` means one file that works on every target and picks up recognizer improvements for free.

If you are looking for a place to apply SPMD to real Go code where the fit is nearly ideal — regular per-pixel arithmetic, contiguous memory, simple control flow, many related kernels — the `image` family is the first place to look.

### §1.3.2 Byte manipulation: HTTP, JSON, and other parsers

The second area where SPMD Go looks especially promising is **byte-level scanning and parsing**. The PoC's hex-encode and base64-decoder examples are byte-manipulation workloads in the small; extending the same techniques to real parsers in the Go stdlib and ecosystem is where the interesting wins would live.

A few categories worth calling out:

- **HTTP header parsing.** `net/http`'s header reader scans bytes looking for `:`, CR/LF, folding whitespace, and forbidden characters, then lowercases keys and trims values. Every one of these operations is a byte-predicate scan or a byte-map transform — exactly the shape of the hex-encode example (§9.1). A `go for i, b := range headerBytes` loop with a varying comparison and a `reduce.Mask` can locate delimiters in one pass. The inner hot loop of header parsing is a well-bounded, well-understood piece of code that already has adversarial-input hardening, which makes it a relatively low-risk place to try SPMD in a critical path.

- **JSON parsing.** The scan phase of a JSON parser — locating structural characters (`{`, `}`, `[`, `]`, `:`, `,`, `"`), finding string terminators, skipping whitespace — is a textbook SIMD workload. This is exactly what `simdjson` does in C++ and it is why `simdjson` is an order of magnitude faster than scalar parsers. The PoC's `vpshufb`-backed small-table lookup pattern (§9.1) and `reduce.Mask` are the building blocks for an equivalent Go-side scan phase. The semantic phase (types, number parsing, UTF-8 validation) is more varied but also benefits — UTF-8 validation in particular is a multi-byte state machine that several C++ SIMD libraries have shown can be vectorized effectively.

- **Go's own source parsing.** `go/scanner` and `go/parser` are byte-driven — identifier scanning, keyword recognition, operator lookahead, comment skipping, string literal reading. `go vet`, `gopls`, `gofmt`, and every Go tool that loads source spends time here. The scanner inner loop in particular is a natural `go for` target: scan a contiguous window of source looking for end-of-token markers, then dispatch scalar on the small set of interesting positions. The same PoC patterns (contiguous byte load, varying comparison, small-table LUT, `reduce.Mask` to find first match) apply directly. A self-hosted benefit loop would be satisfying: SPMD speeds up the Go compiler's own parsing, which speeds up every build.

- **Network protocol parsers more generally.** TLS record parsing, HTTP/2 frame headers, gRPC length-prefixed framing, DNS message parsing, WebSocket frame masking. Most of these are short, byte-granular, and bounded — ideal for SPMD because the chunks fit in one SIMD register and the outer loop is a simple for over the input stream.

**Why this category is a good early target.** Byte-parsing hot paths have three properties that matter for introducing a new compiler technology into production Go:

1. **Well-tested.** These paths have been fuzzed heavily for years. A vectorized rewrite that passes the existing corpus is low-risk.
2. **Self-contained.** A parser inner loop is a small, pure function. You can ship an SPMD version behind a build tag and fall back to scalar, with no API change.
3. **Performance-visible.** HTTP header parsing and JSON decoding show up in every real web service's profile. A measurable speedup there is immediately legible.

The PoC's base64 decoder and hex encoder are proofs of concept for this whole category. Anything that is "scan a `[]byte`, classify each byte, act on the classification" is the same shape, and the same patterns — byte-lane iteration, small-table `vpshufb` lookup, cascading multiply-add where appropriate, `reduce.Mask` to collapse per-lane findings into a scalar index, byte-decomposition store for structured output — apply directly.

If you want a concrete first step beyond the PoC's examples, a vectorized HTTP header delimiter scan (find the next CR/LF and colon positions in a contiguous buffer) is probably the smallest interesting real-world target, and its interface is narrow enough that the benefit is easy to measure and verify.

## §1.4 When SPMD does not help

- **Pointer-chasing code.** Linked lists, trees, maps, tries — the memory layout isn't contiguous, so vector loads can't amortize the pointer dereference.
- **Heavily branchy logic** where most branches are taken by only a few lanes. Every branch runs under a mask; every lane pays the cost of every path even if it's masked off.
- **System calls in the loop.** You don't get to vectorize a syscall. Hoist them out.
- **Small input sizes.** SIMD has a fixed setup cost. For inputs under a few hundred elements, scalar code is often faster.
- **Data sizes that don't match your element type.** Processing `int64` on WASM simd128 gives you 2 lanes. Marginal win at best.

**Rule of thumb:** if your loop body contains only arithmetic, bit ops, and contiguous memory access, SPMD will help a lot. Every step you take away from that makes the win smaller.

---

# §2 The mental model: uniform vs. varying

## §2.1 Two kinds of values

In an SPMD program, every value has one of two "shapes":

- **Uniform.** A regular Go value. Same across all lanes. No annotation.
- **Varying.** A vector of values, one per lane. Typed as `lanes.Varying[T]`.

Uniform values are exactly what you're used to in Go. `int`, `float32`, pointers, slice headers, struct values — all uniform unless you specifically mark them varying. There is no runtime overhead to a uniform value in SPMD code; it's stored in a scalar register, not a vector register.

Varying values are new. They represent "this value has a different per-lane content." In generated code, a `lanes.Varying[int32]` is a vector register: 4 × int32 on WASM, 8 × int32 on AVX2.

## §2.2 Implicit broadcast

When you combine a uniform value with a varying value, the uniform is automatically broadcast to every lane:

```go
var v lanes.Varying[int32]
u := int32(10)        // uniform

result := v + u       // v + broadcast(u), result is Varying[int32]
```

You don't write the broadcast explicitly. The compiler inserts it. Broadcasts are free in code: they compile to `v128.splat` on WASM or `vpbroadcastd` on x86.

**Important:** the compiler keeps values uniform as long as possible. If you write `x := 10 + 20`, that's a uniform constant; it doesn't become varying just because it's used in an SPMD context. Only when a uniform meets a varying does broadcast happen. This matters for register pressure — the less you widen, the more room for genuinely varying values.

## §2.3 The assignment rule

**Varying to uniform is forbidden.** A uniform variable cannot hold a varying value, because it has no place to put the per-lane content.

```go
var v lanes.Varying[int32]
var u int32

u = v  // ERROR: cannot use varying value as uniform
```

The only way to go from varying to uniform is via a **reduction**:

```go
u = reduce.Add(v)   // OK: sums all lanes into a single int32
u = reduce.Max(v)   // OK: extracts the max lane
```

**Uniform to varying is implicit (broadcast).** So `v = u` where `v` is varying and `u` is uniform is fine — the broadcast is automatic.

**Why this rule is load-bearing.** Without it, you'd be able to silently drop information (which lane's value are you assigning?). Intel ISPC enforces the same rule for the same reason. Don't look for workarounds.

## §2.4 Where varying values come from

Three sources:

1. **The iteration variable of `go for i := range N`.** Inside the loop, `i` is varying: `[0, 1, 2, ..., N-1]` at first, then `[N, N+1, ..., 2N-1]`, and so on. One lane per "virtual iteration."
2. **Loading from a slice inside a `go for`.** `x := slice[i]` where `i` is varying produces a varying `x`.
3. **`lanes.Index()`.** Returns the current per-lane index as a varying. Equivalent to `lanes.Varying[int]{0, 1, 2, 3}` on a 4-wide target.

`lanes.Count[T]()` is **uniform**. It's a compile-time constant equal to the lane count for element type T (e.g., 4 for int32 on WASM). Use it for batch sizing, never per-lane computation.

---

# §3 Your first `go for` loop

## §3.1 `go for` is not `go func()`

```go
go for i := range 16 { ... }   // SPMD loop: vectorized iteration
go func() { ... }()            // Goroutine: concurrent function call
```

The parser disambiguates by look-ahead: if the token after `go` is `for`, it's an SPMD loop; otherwise it's a goroutine. There is no ambiguity in practice because the two constructs do completely different things.

An SPMD loop is lowered by the compiler into a vectorized main body that processes `laneCount` elements per iteration, plus a masked tail that handles the leftover at the end. You don't see this — you just write the loop.

## §3.2 A minimal example: sum a slice

```go
package main

import (
    "fmt"
    "lanes"
    "reduce"
)

func sum(xs []int32) int32 {
    var acc lanes.Varying[int32]
    go for _, x := range xs {
        acc += x
    }
    return reduce.Add(acc)
}

func main() {
    xs := []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
    fmt.Println(sum(xs))  // 55
}
```

Walkthrough:

- `var acc lanes.Varying[int32]` declares a zero-initialized varying accumulator. Each lane starts at 0.
- `go for _, x := range xs` iterates `xs` in SPMD. Inside, `x` is varying — on a 4-wide target, it's `[xs[0], xs[1], xs[2], xs[3]]` in the first main iteration, `[xs[4], xs[5], xs[6], xs[7]]` in the second, etc.
- `acc += x` adds the current vector of values into the accumulator. On a 4-wide target this is one `vaddd` per main iteration.
- `return reduce.Add(acc)` collapses the varying accumulator into a single scalar. On x86 this is one `vphaddd` chain.

For a 1024-element slice on AVX2 (8 lanes of int32), the main loop runs 128 iterations, each doing one load + one add. The scalar equivalent does 1024 loads + 1024 adds. Roughly 8× fewer instructions, which translates to ~5-7× wall clock improvement after accounting for cache and dispatch.

## §3.3 Hex-encode (real example)

From `examples/hex-encode/main.go:79`:

```go
func Encode(dst, src []byte) int {
    go for i := range dst {
        v := src[i>>1]
        if i%2 == 0 {
            dst[i] = hextable[v>>4]
        } else {
            dst[i] = hextable[v&0x0f]
        }
    }
    return len(src) * 2
}
```

This loops over the destination (twice the size of the source), picking the high or low nibble of each source byte and looking it up in a 16-byte constant table. On WASM simd128 it hits **6-9×** scalar (varies by host/runtime). On x86 SSE it hits **6.31×**. See CLAUDE.md "Key Metrics" section for the full numbers.

Things worth noticing:

- The iteration variable `i` is varying. So is `src[i>>1]` (a gather-load from src). So is `hextable[...]` (another gather, though the compiler recognizes the small-table pattern and emits `pshufb`/WASM `v128.swizzle`).
- The `if i%2 == 0` is a varying conditional. The compiler emits both branches under masks and blends the results. You don't write the mask — the compiler handles it.
- `dst[i] = ...` is a contiguous store under a full mask (in the peeled main body). One vector store per main iteration.

Also notice the alternative form in the same file (`EncodeSrc`, line 92):

```go
func EncodeSrc(dst, src []byte) int {
    go for i := range src {
        dst[i*2] = hextable[src[i]>>4]
        dst[i*2+1] = hextable[src[i]&0x0f]
    }
    return len(src) * 2
}
```

Same output, different shape. The dst-centric version iterates over the destination (2× source length) with a varying-index gather on source. The src-centric version iterates over the source with two strided stores on destination (§7.5 — the byte-decomposition store pattern). On WASM the dst-centric form wins; on AVX2 the difference is small. **This is a recurring theme in SPMD performance: the same algorithm can be expressed multiple ways, and the best one depends on the target.** Benchmark both.

---

# §4 Reductions are mandatory — the `lanes.Index()` anti-pattern

## §4.1 The trap

This innocent-looking code is wrong in a deep way:

```go
func findFirst(xs []int32, target int32) int {
    result := -1
    go for i, x := range xs {
        if x == target {
            result = int(i)  // !!
        }
    }
    return result
}
```

The problem: `i` is varying; `result` is uniform. The assignment `result = int(i)` should not typecheck (§2.3). But suppose it did — then you'd get "some lane's value of i," and which lane depends on how the compiler happens to iterate. On a 4-wide target you might get the first match's index; on an 8-wide target you'd get... something else. **The result depends on the SIMD width of your target.**

## §4.2 The broader anti-pattern

Any time you use `lanes.Index()` to distinguish lanes, or `reduce.From` to extract a slice of per-lane values and do something per-lane with them, you are producing **lane-count-dependent results**. Your program compiled for WASM simd128 (4-wide int32) will give different output from the same program compiled for AVX2 (8-wide int32). And a different output from scalar fallback (1-wide).

Lane-count-dependence is a real correctness bug, not a performance issue. Your automated tests pass in one mode and fail in another.

## §4.3 How to detect it

**In the current PoC**, the way to detect lane-count-dependent code is to compile your program in dual-mode and diff the output:

```bash
tinygo build -target=wasi -simd=true -o out-simd.wasm main.go
tinygo build -target=wasi -simd=false -o out-scalar.wasm main.go
wasmer run out-simd.wasm > out-simd.txt
wasmer run out-scalar.wasm > out-scalar.txt
diff out-simd.txt out-scalar.txt
```

If the two outputs differ, you have a lane-count-dependent bug. Level 8 of `test/e2e/spmd-e2e-test.sh` automates this for every example in the repo. This works but is a runtime check — you need inputs that exercise the buggy path, and you only find out after compiling and running twice.

**In a real implementation, this should be a static analyzer rule**, not a runtime diff. The properties that make code lane-count-dependent are syntactic and local:

- A varying value is read per-lane (via `reduce.From`, or via `lanes.Index()` used as anything other than lane-independent structure) and the result flows into scalar output.
- A loop's trip count or termination condition depends on a specific lane width.
- A constant like `4` or `16` is compared against `lanes.Count[T]()` or used as if it were the lane count.

All of these are detectable with a `go/analysis`-style pass over the SSA: track which values are varying, track which varying values escape to scalar contexts via non-reduction paths, and flag the escape points. The check belongs alongside `govet` and the nilness checks — you would run it on every SPMD package as part of the normal build, and CI would reject commits that introduced lane-count-dependent output.

Building this static analyzer is an obvious follow-up to the PoC and was captured as deferred work in `PLAN.md`. Until it exists, dual-mode diffing is the practical workaround, and any team shipping SPMD Go should run dual-mode diffs in CI as a safety net — but developers shouldn't have to wait for a failed CI run to learn that they wrote lane-count-dependent code. The analyzer should catch it at save-on-edit time.

## §4.4 The correct discipline

**Produce scalar results via reductions.** The `reduce` package offers:

- `reduce.Add(v)` — sum of all lanes.
- `reduce.Mul(v)` — product.
- `reduce.Min(v)`, `reduce.Max(v)` — min/max across lanes.
- `reduce.Or(v)`, `reduce.And(v)`, `reduce.Xor(v)` — bitwise reductions.
- `reduce.Mask(v)` — bitmask of true lanes (for varying bool).

All of these produce scalar output from varying input, and all are lane-count-independent: they give the same answer regardless of SIMD width.

**`reduce.From(v)` exists.** It extracts all lanes into a Go slice. It is a code smell in hot paths — it's slow (N scalar extractions), it makes your output lane-count-dependent, and it implies you're trying to do per-lane work on the CPU side. Reserve it for tests and debugging.

## §4.5 Rewriting `findFirst` correctly

If you really need "the index of the first match in a slice," don't use SPMD inside. Use SPMD to find *whether* there's a match in each chunk, then fall back to scalar for the exact index:

```go
func findFirst(xs []int32, target int32) int {
    // SPMD-accelerated membership check per chunk.
    for start := 0; start+laneCount <= len(xs); start += laneCount {
        // ... scan chunk for any match
    }
    // ... scalar fallback for the exact first-match index
}
```

Or batch your queries: if you're looking up 16 targets in the same slice, SPMD across targets, not across the slice. This is **outer-SPMD batching** (§7.6). It's a recurring pattern in this guide because it's recurringly useful.

---

# §5 Control flow in SPMD

## §5.1 What's allowed

Most of Go's control flow works inside a `go for`:

- **`if` with varying condition.** The compiler executes both branches under masks and merges via `SPMDSelect`. Per-lane predicate handled for you.
- **`switch` with varying tag.** Each case runs under its mask; the default runs under `not-any-case`.
- **`&&` and `||` with varying operands.** Short-circuit semantics are preserved via mask composition.
- **`continue`** — always fine. It just narrows the mask for the rest of the iteration.
- **Inner scalar `for` loops** — allowed. Each lane executes them scalar-style. If the iteration count is varying, see §5.3.

Varying `if`/`switch`/`&&`/`||` feel identical to normal Go control flow. You don't write the mask. The compiler does.

## §5.2 What's forbidden

- **`return` under a varying condition or after mask alteration.** "Which lanes would return?" has no clean answer. The compiler rejects this at type-check time.
- **`break` under a varying condition.** Same reason. *Note:* `break` under a uniform condition is fine, and the mandelbrot example relies on this for early exit when all lanes have diverged.
- **`panic` inside a `go for`.** Varying panics are nonsensical. If you need per-lane error detection, set a sticky varying bool and check it after the loop with `reduce.Or`.
- **Nested `go for` inside `go for`.** Ambiguous lane count. Use outer batching instead (§7.6).
- **`go for` inside an SPMD function** (a function that takes a varying parameter). Same reason.

The error messages for these cases are specific and actionable — the type checker knows exactly what rule you violated and tells you which.

## §5.3 Divergent inner loops over slice-of-slices

Inside a `go for`, an inner scalar `for` loop can iterate a **per-lane slice** whose length differs across lanes. The canonical shape is `go for` over a `[][]T`, where the outer iteration picks one inner slice per lane and the inner loop walks that slice:

```go
func countPerOuter(data [][]int32) []int {
    out := make([]int, len(data))
    go for i, sub := range data {
        var n lanes.Varying[int]
        for _, x := range sub {   // per-lane inner iteration
            if x > 0 {
                n++
            }
        }
        out[i] = n
    }
    return out
}
```

Here each lane sees its own `sub` (a different inner slice with its own `len`), and the inner `for` walks it to completion. Lane A might do 3 iterations, lane B might do 18, lane C might do 0. The compiler runs the inner loop up to `max(len(sub_a), len(sub_b), ...)` times, and at each step a per-lane gather mask selects which lanes are still active. Lanes that have exhausted their slice continue executing under a cleared mask — their `n++` is still emitted but masked off, so the observable effect is zero. When all lanes are done, the loop exits.

**Status in the PoC:** supported as of 2026-04-12 (PLAN.md line 1335, "Divergent inner loop support for N>1 in go for over slice-of-slices — DONE"). The integration test `array-counting` exercises this exact shape and produces `[3 3 4 18]` correctly on both WASM and x86. The implementation touches three layers: the type checker peels the slice to its inner element type for lane count; the SSA pass includes varying-bound inner loops in the SPMD scope via `spmdInnerLoopHasVaryingBound`; and TinyGo extracts per-lane `len`/`cap`/`IndexAddr` from the `[N x sliceStruct]` and drives the inner loop via `isDivergentInner` detection plus a per-lane gather mask.

**Known limits of this feature today:**

1. **The outer structure must be a slice-of-slices.** The machinery that makes divergent inner iteration work is built around loading a per-lane slice header from a `[][]T` at each outer iteration. An inner loop bounded by an arbitrary varying integer (`for j := 0; j < int(vbound); j++` where `vbound` is an unrelated varying value) is not the shape the current code path recognizes.

2. **Worst-lane scaling.** Every lane pays for the slowest lane. If one lane's inner slice is 1000× longer than the others, the whole `go for` iteration spends ~1000 cycles doing masked-off work in the short lanes for each real cycle of work in the long lane. If the length distribution is very uneven, the inner-SPMD shape is not the right choice and you want to restructure (flatten the slice-of-slices into a single flat slice, process it contiguously, and reconstruct the groupings separately).

3. **Cache pressure from the `[N x sliceStruct]`.** Loading N slice headers per outer iteration is cheap at small N but becomes noticeable when N is 32 (AVX2 byte lanes) and each header is 24 bytes. For the workloads where divergent inner loops naturally arise, N is usually in the 4–8 range and this is not a real concern, but it is worth knowing.

For divergent iteration shapes that *aren't* slice-of-slices, check `PLAN.md` for the current state — the simplest workaround is usually to restructure the algorithm so the varying-bound inner work becomes a per-element operation inside a single-level `go for` (i.e., pre-flatten).

## §5.4 Public vs. private SPMD functions

**Varying parameters are only allowed on unexported functions** (except for builtins in the `lanes` and `reduce` packages, which get an exemption).

```go
// OK: unexported
func doMath(v lanes.Varying[float32]) lanes.Varying[float32] { ... }

// ERROR: exported with varying param
func DoMath(v lanes.Varying[float32]) lanes.Varying[float32] { ... }
```

The rationale: masks and lane counts are implementation details. A library that exports a function with varying parameters leaks that it uses SPMD; calling such a function from non-SPMD code makes no sense (what mask would you pass?).

**The idiomatic workaround** is to wrap your SPMD kernel in a scalar-interface public function:

```go
// Private kernel, SPMD.
func transformKernel(dst, src []float32) {
    go for i, x := range src {
        dst[i] = x*2 + 1
    }
}

// Public scalar-looking entry point.
func Transform(src []float32) []float32 {
    dst := make([]float32, len(src))
    transformKernel(dst, src)
    return dst
}
```

Users of your library see a normal Go function. Inside, you're SPMD.

---

# §6 Cross-lane operations: what actually works

## §6.1 What the PoC shipped

The `lanes` package offers these cross-lane primitives:

| Builtin | Purpose | Lowering |
|---|---|---|
| `lanes.Count[T]()` | Lane count for element type T | Compile-time constant |
| `lanes.Index()` | Per-lane index vector | `[0, 1, ..., N-1]` splat |
| `lanes.Broadcast(v, lane)` | Replicate one lane's value across all | Splat |
| `lanes.Rotate(v, k)` | Full-width rotation by const k | `shufflevector` |
| `lanes.Swizzle(v, idx)` | Runtime-indexed permutation | Per-lane extract/insert |
| `lanes.RotateWithin(v, k, n)` | Rotate within each group of n | `shufflevector` (const) |
| `lanes.ShiftLeftWithin(v, k, n)` | Shift left within groups | `shufflevector` |
| `lanes.ShiftRightWithin(v, k, n)` | Shift right within groups | `shufflevector` |
| `lanes.SwizzleWithin(v, idx, n)` | Permute within groups, const idx | `shufflevector` |

These are the operations you'd expect from a SIMD library. They all compile to sensible code — none of them is broken.

## §6.2 The uncomfortable truth

**In every benchmark in this PoC, none of the `*Within` family or full-width `Rotate`/`Swizzle` delivered a measurable performance win.** The final version of every example — base64 decoder, hex-encode, mandelbrot, lo-min, lo-max, lo-sum, lo-clamp, IPv4 parser, simple-sum, odd-even, to-upper — either used no cross-lane primitives at all, or used only `Broadcast`, `Count`, or `Index`.

Where the wins came from instead:

- **Contiguous vector loads and stores**, enabled by writing idiomatic Go that the compiler recognizes (§7.5).
- **Reductions** — collapsing varying accumulators to scalars at the right moment (§4).
- **Cascading `go for` loops** that trigger compiler pattern detection for `vpmaddubsw`/`vpmaddwd` (§7.1).

This isn't a failure of the `*Within` primitives — they work correctly. It's that the measurable wins in data-parallel Go come from **choosing the right memory layout** and **having the compiler recognize arithmetic patterns**, not from rearranging values within a SIMD register.

### §6.2.1 A concrete negative result

The base64 decoder was implemented twice. Version 1 used `lanes.CompactStore` + `lanes.Rotate` tricks to produce its output. It hit 2× scalar — a real but underwhelming speedup. Version 2 was rewritten to use cascading `go for` loops (byte → int16 → int32) with zero cross-lane operations. It hit **77% of simdutf C++**, a ~10× improvement over v1.

If a compiler engineer or library author tells you "just use `SwizzleWithin` for deinterleave" — measure it first against the idiomatic version. In this PoC, the byte-decomposition store (see §7.5 and the implementer notes §5.7) outperformed every hand-tuned `Swizzle` approach we tried.

## §6.3 When cross-lane ops do help

A few are free or cheap and worth using:

- **`lanes.Broadcast(v, lane)`** compiles to a splat, costs nothing. Use when you need one lane's value across all.
- **`lanes.Count[T]()`** is a compile-time constant. Use it to size chunks and batches (see §7.2 for the key trick).
- **`lanes.Index()`** compiles to a constant vector. Use it for *lane-independent structure* — e.g., computing `[start, start+1, start+2, ..., start+N-1]` as a base for a gather. Never use it for per-lane inspection (§4).

  **Possible language polish.** `lanes.Index()` is really just `iota` — "the sequence `0, 1, 2, ..., N-1`" — promoted to vector form. A natural future refinement is to let `iota` itself carry that meaning when its result type is varying:

  ```go
  var idx lanes.Varying[int] = iota   // hypothetical: [0, 1, 2, ..., N-1]
  ```

  This would reuse a keyword every Go developer already understands (from `const` blocks) instead of introducing a new package function, and it would reinforce that "the index vector is a compile-time constant" — which is already how `iota` feels in Go. The PoC does not implement this; it is offered as a language-design suggestion for a real upstream version.

Everything else: **benchmark before using.** The infrastructure exists, the semantics are correct, the performance argument is not.

## §6.4 Rule of thumb

**Write the loop. Trust the compiler.** If the generated code is bad, file a bug, don't reach for a cross-lane builtin. Most of the time, the fix is in the compiler's pattern recognizer, not in the user's code.

---

# §7 Performance patterns

## §7.1 Cascading `go for` for widening multiply-add

This is the highest-impact idiom in the whole PoC. The compiler recognizes three cascading `go for` loops of decreasing SIMD width (byte → int16 → int32), each doing a constant-coefficient multiply-add, and emits `vpmaddubsw` / `vpmaddwd` on x86 (and equivalent deinterleave + widen + mul + add on WASM).

The canonical demonstration is the base64 decoder at `examples/base64-decoder/main.go:41` (`decodeAndPack`):

```go
func decodeAndPack(dst, src []byte) int {
    n := len(src)

    // Loop 1 (byte-width): decode ASCII → 6-bit sextets via nibble LUT.
    sextets := make([]byte, n)
    go for i, ch := range src {
        s := ch + decodeLUT[ch>>4]
        if ch == byte('+') {
            s += 3
        }
        sextets[i] = s
    }

    // Loop 2 (int16-width): merge adjacent sextet pairs.
    // a*64 + b = (a<<6)|b → pmaddubsw pattern [64, 1, 64, 1, ...].
    halfLen := n / 2
    merged := make([]int16, halfLen)
    go for g := range merged {
        merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
    }

    // Loop 3 (int32-width): merge adjacent int16 pairs.
    // a*4096 + b → pmaddwd pattern [4096, 1, 4096, 1, ...].
    quarterLen := halfLen / 2
    packed := make([]int32, quarterLen)
    go for g := range packed {
        packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
    }

    // Loop 4: extract 3 bytes per packed int32.
    go for g := range packed {
        dst[g*3+0] = byte(packed[g] >> 16)
        dst[g*3+1] = byte(packed[g] >> 8)
        dst[g*3+2] = byte(packed[g])
    }

    return quarterLen * 3
}
```

The compiler recognizes the `int16(src[i*2])*C + int16(src[i*2+1])` pattern in loop 2 and emits `vpmaddubsw`. It recognizes the same shape in loop 3 on int16→int32 and emits `vpmaddwd`. Loop 4 is the byte-decomposition store (§7.5).

**The measured result.** The hot loop went from **14.3 instructions per byte** (scatter-gather version) to **0.44 instructions per byte** after these patterns landed — a 32× instruction reduction. End-to-end, AVX2 throughput is **~17 GB/s, 77% of simdutf.**

**Why this is idiomatic.** The programmer writes plain Go. No builtins, no intrinsics, no annotations. The compiler handles all target-specific details and falls back to generic code on WASM. Same source file compiles efficiently everywhere.

## §7.2 Chunk size from `lanes.Count[T]()`

Still in the base64 decoder, at line 119:

```go
var bv lanes.Varying[byte]
chunkSize := max(4, lanes.Count[byte](bv))
outOffset := 0

// Process full chunks.
for off := 0; off+chunkSize <= hotBytes; off += chunkSize {
    n := decodeAndPack(dst[outOffset:], src[off:off+chunkSize])
    outOffset += n
}
```

`chunkSize` is the byte-lane count of the target: 16 on SSE, 32 on AVX2. The outer scalar loop feeds `decodeAndPack` exactly one register-sized chunk at a time.

**Why this matters.** Inside `decodeAndPack`, every `go for` loop runs for exactly the right number of elements to fill one SIMD register once. There's no unrolling to compute, no partial iterations, no masked tail. Each `go for` compiles to a straight-line sequence of vector instructions — no loop at all after compiler lowering. Register allocation becomes trivial.

Without `lanes.Count[byte]()`-sized chunks, the inner `go for` loops would include a main-body loop plus a masked tail, and the compiler would have a harder time keeping everything in registers.

**Why the `max(4, ...)`.** Scalar mode (`-simd=false`) reports `lanes.Count[byte]() = 1`. A cascading byte → int16 → int32 kernel computes `halfLen = n/2`, `quarterLen = n/4`; with `n=1` both are zero and the kernel produces no output at all. The minimum `n` that exercises every level of the cascade is **`n = 2^(levels-1) × align`**, where `align` is the output granularity of the deepest level. For the base64 decoder (three cascade levels, 4 sextets per output triplet), that bound is 4. Any kernel with this shape needs the same guard. Use `max` of the lane count and the algorithmic minimum; the SIMD build uses the lane count (which is already ≥ 4 for bytes on any real target), the scalar build uses the minimum. Dual-mode testing (§8.2) catches this if you forget.

**The general pattern:** for encoder/decoder/packer kernels, write the outer loop in scalar Go, use `lanes.Count[T]()` to size chunks, and let each `go for` inside the kernel run exactly one iteration.

## §7.3 Byte-lane vs. int-lane iteration

On AVX2:

- Iterating at byte granularity: **32 lanes** per iteration.
- Iterating at int32 granularity: **8 lanes** per iteration.

That's 4× more parallelism if your algorithm can be expressed at byte level. Encoders, decoders, compressors, and hashes often can. Numerical kernels usually cannot — you need the precision of int32 or float32 lanes.

**The cost of byte-lane iteration** is that the compiler must use the decomposed index path (see the implementer notes §5.1) — a scalar base pointer plus a `<N x i8>` lane offset — to avoid generating huge index vectors. The decomposed path adds a tiny per-GEP overhead on x86 but is almost free on WASM.

**Rule of thumb:** if your algorithm is naturally byte-parallel, prefer byte lanes. If you need int32 or float32 precision, use them — but know that your max theoretical speedup is 4× lower per level of width.

## §7.4 Reductions beat `reduce.From`

`reduce.Add(acc)` emits a single vector reduction instruction (`vphaddd` chain on x86, `i32x4.extract_lane` fan-in on WASM). `reduce.From(acc)` extracts all lanes into a Go slice: N scalar extracts, plus slice allocation, plus a heap bookkeeping walk. At least an order of magnitude slower.

**In hot paths, never use `reduce.From`.** It's there for tests and debugging. If you find yourself reaching for it, the question to ask is: "can I rewrite this so the answer is a reduction?"

## §7.5 Slice stores inside `go for` — the golden case

```go
go for i, x := range in {
    out[i] = transform(x)
}
```

This is the happy path of SPMD. `in[i]` is a contiguous vector load. `out[i] = ...` is a contiguous vector store. In the peeled main body, the mask is all-ones, so the store is a single vector store (no load-blend-store dance). The main body is typically 5-10 instructions: load, transform, store, pointer advance, branch back.

**Almost every loop that hits 5× or better speedup in the PoC has this shape.** Hex-encode (src-centric variant), simple-sum, lo-max, lo-clamp, hex-encode-Dst, half the base64 decoder.

When writing new SPMD code, aim for this shape first. Complications come from:

- **Varying-index access** on the input or output (gather/scatter instead of contiguous). Sometimes unavoidable; try to refactor into a separate pass if you can.
- **Strided stores** (`out[i*3+0] = ...; out[i*3+1] = ...`). These trigger the byte-decomposition store pattern (implementer notes §5.7) — write them naturally and let the compiler do its job.
- **Partial stores** under a varying condition. These work, but pay the load-blend-store cost. Try to hoist the condition out if the loop structure allows.

## §7.6 Outer-SPMD batching

The IPv4 parser in `examples/ipv4-parser/main.go` is instructive for a different reason: it does **not** speed up with inner SPMD. Inner-SPMD (vectorizing the parse of a single IP address across lanes) peaks at **0.58× scalar** — it's actually slower than scalar code — because a single IPv4 string has only 4-15 characters and the vector setup overhead eats the gains.

The architectural answer is **outer-SPMD batching**: vectorize across **independent instances** instead of within one instance. If you have 16 IPv4 strings to parse, run 16 parsers in parallel, one per lane:

```go
// Pseudocode — not an implemented feature in the PoC, but the design direction.
func parseBatch(inputs []string) []uint32 {
    out := make([]uint32, len(inputs))
    go for i := range inputs {
        out[i] = parseSingleInBatch(inputs[i])  // runs across lanes
    }
    return out
}
```

**The general rule.** When one instance of your computation is too small, too branchy, or too memory-irregular to vectorize internally, vectorize *across* instances. This is how ISPC's flagship use cases (ray tracers, physics solvers) work: they SPMD across rays or across particles, not within them.

The PoC doesn't implement outer-SPMD batching as a first-class language feature — it would require lifting slice-of-strings into a varying string view, which needs more type system work. But the pattern is clear: **when your inner loop isn't paying off, look outer.**

---

# §8 Debugging SPMD code

## §8.1 `fmt.Printf` with `%v` on varying values

This is the single fastest debugging tool:

```go
var v lanes.Varying[int]
go for i := range 16 {
    v = i * 3
    if i%2 == 0 {
        fmt.Printf("%v\n", v)  // prints: [0 _ 6 _] then [12 _ 18 _]
    }
}
```

`%v` on a varying value prints `[value _ value _ ...]` where active lanes show their values and inactive (masked-off) lanes show `_`. This makes the mask immediately visible. Use it liberally while writing your first SPMD code.

## §8.2 Dual-mode as your correctness check

Compile and run in both `-simd=true` and `-simd=false` modes, diff the output. If they differ, you have a lane-count-dependent bug (§4).

The test script `test/e2e/spmd-e2e-test.sh` Level 8 automates this for every example. For your own code, add a two-line shell function that does the diff.

## §8.3 Reading generated code

When a loop is slower than you expected, read the generated assembly:

- **WASM:** `wasm2wat out.wasm | less`. Look for `v128.load`, `v128.store`, `i32x4.add`, `v128.swizzle`. If you see scalar `i32.load` in the hot loop, vectorization didn't trigger.
- **x86-64 native:** `llvm-objdump -d out.elf | less`. Look for `vmovdqu` (load/store), `vpaddd` (add), `vpshufb` (byte shuffle), `vpmaddubsw` / `vpmaddwd` (the magic).
- If you see `pextrd` / `pinsrd` sequences dominating, the compiler is gathering/scattering when it should be doing contiguous ops. That's a sign the contiguous access analyzer didn't recognize your pattern. File a bug with a minimal reproducer.

## §8.4 Common error messages

- `cannot use varying value as uniform` — you tried to assign `lanes.Varying[T]` to a regular variable. Use a reduction.
- `return not allowed under varying condition` — you put a `return` inside a varying `if`. Hoist the return out of the condition.
- `break not allowed under varying condition` — ditto, but for break. If you need early exit, use a uniform reducer like `reduce.Any(...)` to turn the condition into a uniform one.
- `cannot nest go for loops` — you put a `go for` inside another `go for`. Restructure to outer-SPMD batching or scalar inner loop.
- `go for not allowed in SPMD function` — you put a `go for` inside a function that has a varying parameter. Split the function.

---

# §9 Worked examples

## §9.1 hex-encode

Source: `examples/hex-encode/main.go:79`.

```go
const hextable = "0123456789abcdef"

func Encode(dst, src []byte) int {
    go for i := range dst {
        v := src[i>>1]
        if i%2 == 0 {
            dst[i] = hextable[v>>4]
        } else {
            dst[i] = hextable[v&0x0f]
        }
    }
    return len(src) * 2
}
```

**What makes it fast:**
- `go for i := range dst` iterates at byte granularity. 16 lanes on WASM (byte width on 128-bit), 32 on AVX2.
- `src[i>>1]` — varying index gather. On WASM it compiles to `v128.swizzle` with a constant-offset table; on AVX2 to the decomposed index path + `vpshufb`.
- `hextable[v>>4]` and `hextable[v&0x0f]` — 16-entry table lookups. Compile to `v128.swizzle` / `vpshufb` (with the AVX2 table duplication trick — see implementer notes §5.2).
- `if i%2 == 0` is a varying conditional. Both branches compute; mask selects.
- `dst[i] = ...` — contiguous store. In the peeled main body, single `v128.store` or `vmovdqu`.

**Measured speedup** (CLAUDE.md "Key Metrics" section):
- WASM simd128: **6-9×** (varies by host)
- x86 SSE: **6.31×**
- x86 AVX2: **1.13×** — this looks bad, but it's because AVX2 byte iteration has 32 lanes and the decomposed index path pays a small per-GEP overhead that dominates at this width. See the implementer notes for why.

**Alternative form** (`EncodeSrc`, line 92) iterates over the source with a strided store to the destination:

```go
func EncodeSrc(dst, src []byte) int {
    go for i := range src {
        dst[i*2] = hextable[src[i]>>4]
        dst[i*2+1] = hextable[src[i]&0x0f]
    }
    return len(src) * 2
}
```

This triggers the byte-decomposition store pattern (§7.5): the compiler recognizes that `dst[i*2]` and `dst[i*2+1]` together form a stride-2 interleaved store and emits a single bitcast+`pshufb`+store sequence. On WASM it's slightly faster than `Encode`; on AVX2 it wins.

**The lesson:** when writing SPMD, express the same algorithm both src-centric and dst-centric, then benchmark to see which the compiler handles best on your target.

## §9.2 mandelbrot

Source: `examples/mandelbrot/main.go:46` (kernel) and 97 (driver).

```go
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int) lanes.Varying[int] {
    var zRe lanes.Varying[float32] = cRe
    var zIm lanes.Varying[float32] = cIm
    var iterations lanes.Varying[int] = maxIter  // start at max

    for iter := range maxIter {
        magSquared := zRe*zRe + zIm*zIm
        diverged := magSquared > 4.0

        if diverged {
            iterations = iter
            break
        }

        newRe := zRe*zRe - zIm*zIm
        newIm := 2.0 * zRe * zIm
        zRe = cRe + newRe
        zIm = cIm + newIm
    }

    return iterations
}

func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
    dx := (x1 - x0) / float32(width)
    dy := (y1 - y0) / float32(height)

    for j := 0; j < height; j++ {
        y := y0 + float32(j)*dy
        go for i := range width {
            x := x0 + lanes.Varying[float32](i)*dx
            iterations := mandelSPMD(x, y, maxIter)
            index := j*width + i
            output[index] = iterations
        }
    }
}
```

**What's interesting:**
- The kernel `mandelSPMD` is an **SPMD function**: it takes varying parameters and is called under a mask. It does not contain a `go for`.
- The inner `for iter := range maxIter` is a **uniform** loop (no `go for`). Every lane runs the same number of iterations — up to `maxIter` — but the mask narrows as lanes diverge.
- The `if diverged { iterations = iter; break }` pattern sets the per-lane result and **breaks out of the uniform loop** when all lanes have diverged. Note: the prior version of this code explicitly used `reduce.Any(!diverged)` as a loop condition for early exit; the current compiler handles that automatically via per-lane mask tracking.
- `output[index] = iterations` — the index is varying (`j*width + i` where `i` is varying), so this is a scatter store. It still vectorizes well because the index is `const + contiguous`, which the compiler recognizes as contiguous.

**Measured speedup** (CLAUDE.md):
- x86 AVX2: **6.07×**
- x86 SSE: **3.71×**
- WASM simd128: **2.5-3.6×** (varies by host)

**The lesson:** divergent iteration counts (different lanes diverging at different times) are handled well by SPMD. Write the uniform loop with a varying break condition; the compiler tracks per-lane masks correctly.

## §9.3 base64 Mula-Lemire decoder (the flagship)

Source: `examples/base64-decoder/main.go`.

This is the example that proves SPMD Go is competitive with hand-written C++ SIMD. At AVX2, it achieves **~17 GB/s = 77% of simdutf C++**. The full source is 285 lines and uses almost every pattern in this guide:

1. **`decodeAndPack` (line 41)** is the inner kernel. Four cascading `go for` loops:
   - Byte-lane loop: decode ASCII to 6-bit sextets via a 16-entry nibble LUT with a varying conditional for the `+` special case.
   - Int16-lane loop: merge pairs of sextets with `a*64 + b` — the compiler emits `vpmaddubsw`.
   - Int32-lane loop: merge pairs of int16 with `a*4096 + b` — the compiler emits `vpmaddwd`.
   - Byte output loop: extract three bytes per int32 using stride-3 indexing — the compiler emits byte-decomposition store.

2. **`spmdDecode` (line 91)** is the outer driver. It:
   - Computes `chunkSize := max(4, lanes.Count[byte](bv))` — 16 on SSE, 32 on AVX2, 4 in scalar fallback (the minimum for the cascade to produce output).
   - Loops over the source in `chunkSize`-sized chunks, calling `decodeAndPack` for each.
   - Handles the remainder (less than one chunk) by padding with `'A'` and copying the valid output.
   - Handles base64 padding (`'='`) with a scalar fallback for the last quartet.

**Why it works.**
- Each call to `decodeAndPack` runs each of its four inner `go for` loops exactly once, because `chunkSize` matches the byte lane count. The loops fully unroll at compile time to straight-line vector code.
- The compiler recognizes the multiply-add patterns and emits the tightest available SIMD instructions for each target.
- The stride-3 output store is recognized as a byte-decomposition pattern.
- No cross-lane operations. No builtins beyond `lanes.Count[byte]`. Everything that matters is in the pattern recognizer.

**What you should internalize from this example.** Idiomatic Go can compete with hand-written SIMD libraries when (a) you structure your kernel around what the compiler recognizes and (b) you size your chunks to match the register width. The PoC validates both claims.

## §9.4 IPv4 parser (the cautionary tale)

Source: `examples/ipv4-parser/main.go`. This example parses IPv4 addresses from ASCII to 32-bit integers, using SPMD within a single address parse.

**What happened.** Inner-SPMD (parsing one address with lanes across the 15 characters) was architecturally capped at **~0.58× scalar** — i.e., it was *slower* than scalar code. Not because the compiler was bad, but because:

1. A single IPv4 string has only 4-15 characters. Vector setup overhead (loading 16 bytes with mask, splatting constants, etc.) dominates the actual work.
2. The data has varying length, so every iteration pays the masked-tail cost.
3. The output is a single `uint32`, so reductions are cheap but you only get one per input.

The fix — implemented as a benchmark but not upstreamed as a language feature — is **outer-SPMD batching** (§7.6). Parse 16 addresses at once, one per lane, across the address space.

**The lesson.** SPMD isn't free. When each "instance" of the work is too small or too irregular to amortize vector setup, inner-SPMD loses. Look for opportunities to parallelize across instances instead of within them. This lesson matters for *any* parser, validator, or per-item formatter you might be tempted to vectorize.

---

# §10 Cheat sheet

## §10.1 Allowed in a `go for`

- Varying `if`/`switch`/`&&`/`||`
- `continue` (always)
- Inner scalar `for` loops
- Calls to private SPMD functions (functions with varying params)
- Contiguous slice load and store
- Arithmetic on varying values
- Small-table LUT lookups (compile to `pshufb`/`v128.swizzle`)
- Calls to `lanes.*` and `reduce.*` builtins

## §10.2 Forbidden in a `go for`

- `return` under varying condition
- `break` under varying condition
- `panic` anywhere inside
- Nested `go for`
- Reassigning a varying value to a uniform variable (must go through reduction)

## §10.3 Forbidden on public APIs

- Exported functions with `lanes.Varying[T]` parameters

## §10.4 Idioms that deliver wins

- Size chunks with `chunkSize := max(minAlgorithmic, lanes.Count[byte](bv))`; wrap the SPMD kernel in a scalar outer loop. The `max` ensures scalar fallback still works for cascading kernels.
- Cascading `go for` loops at decreasing widths (byte → int16 → int32) with constant-coefficient multiply-add for `vpmaddubsw`/`vpmaddwd`.
- Contiguous slice load and store inside the `go for` — the "golden case."
- Varying accumulator + `reduce.Add`/`Max`/`Min` outside the loop.
- Strided stores `out[i*N+k] = byte(...)` for byte-decomposition store recognition.
- Outer-SPMD batching when one instance is too small to vectorize.

## §10.5 Anti-patterns that hurt

- Using `lanes.Index()` for per-lane computation and `reduce.From` to inspect results → lane-count-dependent.
- Reaching for `*Within` / `Swizzle` before measuring — they have yet to deliver a measured win.
- Per-lane panic/return — the type checker rejects these, but trying to work around the rejection is a sign you're thinking about SPMD the wrong way.
- Vectorizing a single small string when you could batch across strings.

## §10.6 Debugging checklist

1. Compile with `-simd=false` and compare output. Differences → lane-count-dependent bug.
2. Add `fmt.Printf("%v\n", varyingValue)` anywhere to see per-lane values and masks.
3. Inspect generated code with `wasm2wat` or `llvm-objdump -d`. Look for expected vector instructions.
4. If pattern detection didn't trigger, file a bug with a minimal reproducer.
5. If your loop is slower than expected, check that the contiguous analyzer recognizes your access pattern.

## §10.7 Expected speedups (approximate)

| Category | WASM simd128 | x86 SSE (4-wide) | x86 AVX2 (8-wide) |
|---|---|---|---|
| Sum/min/max reductions | 3-4× | 2-3× | 5-7× |
| Clamp / element-wise math | 2-3× | 2-3× | 4-5× |
| Hex-encode / byte transforms | 6-9× | 4-7× | 1-6× |
| Mandelbrot (divergent) | 3× | 4× | 6× |
| Base64 decode | 2-3× vs stdlib | 10× vs stdlib | ~9× vs stdlib |

Anything more than 8× on an 8-wide target is either a measurement artifact or a microbenchmark hitting cache effects. Anything less than 2× on a reduction-heavy workload is probably a signal that the compiler missed an optimization — file a bug.

---

*For the internal compiler details behind these patterns, see `implementer-notes.md`. For the research framing of techniques that are novel or adapted in this PoC, see `novel-patterns.md`.*
