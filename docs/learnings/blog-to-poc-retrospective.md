# From Blog to PoC: What Changed and Why

*A retrospective comparing the original SPMD-for-Go blog series (June-July 2025) with what the proof-of-concept actually built (August 2025 - April 2026). Focuses on misconceptions, surprises, and the fundamental lesson that pattern matching on idiomatic Go works better than trying to imagine what the compiler could do.*

---

# §1 The blog series at a glance

Four posts, written before any implementation existed:

1. **"Data Parallelism: simpler solution for Golang?"** (2025-06-19) — introduced `go for`, `lanes.Varying[T]`, uniform/varying, masks, divergent control flow.
2. **"What if? Practical parallel data."** (2025-06-21) — printf `%` scanning, hex encoding, `bytes.ToUpper`. Used `reduce.Any`, `reduce.FindFirstSet`, `lanes.Count`.
3. **"Cross-Lane Communication: When Lanes Need to Talk"** (2025-07-12) — base64 decoding via `SwizzleWithin`, `RotateWithin`, `ShiftLeftWithin`, output-pattern extraction. Built around Miguel Young de la Sota's approach.
4. **"Putting It All Together: Fast IPv4 Parsing"** (2025-07-13) — IPv4 parser combining all techniques. Predicted 2-3x speedup.

The blogs were a design exploration — "what if Go had this?" — written to test whether the syntax and mental model felt right. They were not an implementation plan. But they shaped our assumptions about what the compiler would need, and several of those assumptions turned out to be wrong.

---

# §2 What the blogs got right

Before the list of misconceptions, it's worth noting what survived contact with reality unchanged:

**The syntax.** `go for i := range N`, `lanes.Varying[T]`, uniform/varying distinction, `reduce.Add/Any/All/Max/Min` — all of this shipped exactly as the blogs described. The syntax was the easy part; the blogs nailed it because it was designed for readability, not for compiler convenience.

**Package-based types over keywords.** Blog 1 already argued against a `varying` keyword: "adding keywords to Go is nearly impossible without breaking existing code." The PoC confirmed this — `lanes.Varying[T]` as a compiler-magic generic worked seamlessly with existing tooling (gopls, goimports, vet).

**`reduce.Any` for early exit.** Blog 4's IPv4 parser used `reduce.Any(condition)` to detect errors across all lanes and exit early under a uniform condition. This pattern worked exactly as designed and is one of the most useful idioms in the PoC.

**`reduce.FindFirstSet` for error location.** Blog 4 used `reduce.FindFirstSet(!validChars) + loop` to locate the exact position of the first invalid character. The PoC implemented this and it gives precise error messages from SPMD code — a real advantage over hand-written SIMD assembly.

**The readability argument.** The blogs' core thesis — that SPMD code is more readable than assembly or intrinsics while being faster than scalar Go — held up. The base64 decoder, hex encoder, and mandelbrot examples in the PoC are readable by any Go developer who understands the uniform/varying mental model.

---

# §3 The big misconception: developer-crafted cross-lane ops vs. compiler pattern detection

This is the central lesson of the entire PoC, and it contradicts the premise of blog 3 almost completely.

## §3.1 What the blog imagined

Blog 3 ("Cross-Lane Communication") was built around the idea that **base64 decoding requires explicit cross-lane operations** — `SwizzleWithin`, `RotateWithin`, `ShiftLeftWithin` — and that developers would write code like:

```go
// Blog's imagined base64 approach (from cross-lane-communication.md)
offsetTable := []byte{255, 16, 19, 4, 191, 191, 185, 185}
offsets := lanes.SwizzleWithin(lanes.From(offsetTable), hashes, 8)
sextets := ascii + offsets

shiftPattern := lanes.From([]uint16{2, 4, 6, 8})
shifted := lanes.ShiftLeftWithin(sextets, shiftPattern, 4)
decodedChunks := shiftedLo | lanes.RotateWithin(shiftedHi, 1, 4)

output := lanes.SwizzleWithin(decodedChunks, pattern, 4)
```

The blog assumed that the algorithm the developer writes should be close to what the SIMD hardware executes — explicit shuffles, explicit rotations, explicit extraction patterns. The developer is essentially writing a hardware-aware algorithm in Go syntax.

Blog 3 even asked the right question: "Is the added complexity worth it? Perhaps the real question is whether we need the full suite of cross-lane operations, or if reduction alone would cover the majority of practical use cases." It proposed three options:

1. Full suite (swizzle, rotation, reduction) — maximum capability, maximum complexity.
2. Reduction only — simpler mental model, covers many common patterns.
3. Gradual introduction — start with reduction, add others based on demonstrated need.

## §3.2 What actually worked

The PoC implemented the full suite (option 1). Then we benchmarked. Then we built a second base64 decoder (v2) that used **zero cross-lane operations** and hit ~77% of simdutf C++, vs. v1's ~20% of simdutf.

The v2 decoder looks like this:

```go
// What actually shipped (from examples/base64-decoder/main.go:41)
func decodeAndPack(dst, src []byte) int {
    n := len(src)

    // Loop 1: decode ASCII → 6-bit sextets via nibble LUT.
    sextets := make([]byte, n)
    go for i, ch := range src {
        s := ch + decodeLUT[ch>>4]
        if ch == byte('+') { s += 3 }
        sextets[i] = s
    }

    // Loop 2: merge pairs → pmaddubsw pattern.
    halfLen := n / 2
    merged := make([]int16, halfLen)
    go for g := range merged {
        merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
    }

    // Loop 3: merge pairs → pmaddwd pattern.
    quarterLen := halfLen / 2
    packed := make([]int32, quarterLen)
    go for g := range packed {
        packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
    }

    // Loop 4: extract 3 bytes per int32.
    go for g := range packed {
        dst[g*3+0] = byte(packed[g] >> 16)
        dst[g*3+1] = byte(packed[g] >> 8)
        dst[g*3+2] = byte(packed[g])
    }

    return quarterLen * 3
}
```

No `SwizzleWithin`. No `RotateWithin`. No `ShiftLeftWithin`. No output pattern. No explicit cross-lane anything. Just four `go for` loops with plain Go arithmetic.

The compiler recognized the `int16(a)*64 + int16(b)` shape and emitted `vpmaddubsw`. It recognized the `int32(a)*4096 + int32(b)` shape and emitted `vpmaddwd`. It recognized the stride-3 byte extraction and emitted a `vpshufb`-based byte-decomposition store. All automatically, from idiomatic Go.

## §3.3 Why the simpler code won

The blog's approach was **top-down**: start from the SIMD algorithm (Miguel Young de la Sota's base64 decoder), translate it into Go syntax with explicit cross-lane operations. This is how you'd write an intrinsics library — you know the target instructions and you express them in your source language.

The PoC's winning approach was **bottom-up**: write the algorithm in the most natural Go you can, and let the compiler recognize patterns that map to efficient SIMD. The developer doesn't need to know about `vpmaddubsw`; they just write `int16(a)*C + int16(b)` and the compiler handles the rest.

**Why bottom-up wins:**

1. **The compiler sees more than the developer.** The `vpmaddubsw` pattern detector handles any constant-coefficient widening multiply-add, not just the base64 case. It automatically handles constant decomposition when coefficients exceed 127 (the signed-byte limit). It falls back to a generic sequence on WASM where no pmadd instruction exists. The developer writes one line; the compiler produces target-optimal code on every platform.

2. **The developer writes less and simpler code.** The v2 decoder is ~40 lines of kernel code. The blog's v1 approach (if fully implemented) would be 80+ lines of explicit shuffles and rotations. Simpler code has fewer bugs, is easier to review, and is easier to maintain.

3. **Pattern detection compounds.** The v2 decoder benefits from *four* pattern detectors firing simultaneously: pmadd for loops 2 and 3, byte-decomposition store for loop 4, and contiguous access analysis for every load and store. The blog's explicit-shuffle approach bypasses all of these because the developer has already lowered the algorithm to specific operations.

4. **Cross-lane ops have hidden costs.** `SwizzleWithin` with a variable index compiles to per-lane extract/insert — slow. `RotateWithin` needs a const-only `shufflevector`, which means the compiler must prove the offset is constant. The blog assumed these would be free; they are not.

## §3.4 The answer to blog 3's question

Blog 3 asked: "Full suite, reduction only, or gradual introduction?"

The PoC's answer: **option 3, but more aggressive than imagined.** Start with reductions + `Broadcast` + `Count` + `Index`. Don't ship the `*Within` family or full-width `Swizzle` until a concrete benchmark demands them. Invest the engineering time in pattern detectors instead — they deliver more performance from simpler user code.

---

# §4 The IPv4 parser: when inner-SPMD is the wrong shape

## §4.1 What the blog predicted

Blog 4 predicted "2-3x performance improvements" for an SPMD IPv4 parser that processes all characters of a single IP address in parallel (16 lanes for 16 bytes). The approach was:

1. Pad the string to 16 bytes.
2. Classify all characters in parallel (dots, digits, nulls).
3. Build a dot-position bitmask via `reduce.Mask`.
4. Extract dot positions with `bits.TrailingZeros16`.
5. Parse all four octets in a second `go for` over a 4-element array.

## §4.2 What actually happened

Inner-SPMD (vectorizing within a single IP address) peaked at **~0.58x scalar** — actually slower than scalar code. Not because the compiler was bad, but because the shape was wrong:

- A single IPv4 string has 7-15 characters. Vector setup overhead (loading 16 bytes, splatting constants, computing masks) dominates the actual arithmetic.
- Every input has a different length, so every iteration pays the masked-tail cost.
- The output is a single `[4]byte` — the entire SPMD computation produces just 4 bytes of useful output per invocation.

The 2-3x speedup the blog predicted was based on Wojciech Mula's C/assembly benchmarks, where the setup cost is amortized differently (the C version processes raw buffers with no slice-header overhead, no Go calling convention, and hand-tuned register allocation).

## §4.3 The right shape: outer-SPMD batching

The architectural fix — which the PoC identified but did not fully implement as a language feature — is to vectorize **across** IP addresses instead of within one:

```go
// Process 16 IP addresses simultaneously, one per lane.
go for i, addr := range batch {
    result[i] = parseOneIPv4(addr)
}
```

Each lane holds one complete address and processes it independently. If you have 16 addresses to parse, you get real 16-wide parallelism with no wasted lanes.

This is how ISPC's flagship use cases work (ray tracing across rays, physics across particles) and it's the natural shape for any per-item parser.

## §4.4 What we learned

**Not every algorithm that can be expressed in SPMD should be.** The blogs assumed that any character-processing task benefits from inner-SPMD. In reality, the input size relative to the SIMD width matters enormously. If each "instance" of the work is small (a single IP address, a single short string), the setup overhead dominates and inner-SPMD loses.

The rule that emerged: **if your input fits in one or two SIMD registers, look for outer-SPMD batching instead of inner-SPMD scanning.**

---

# §5 The hex encoder: closer than expected, but the shape still surprised us

## §5.1 What the blog wrote

Blog 2's hex encoder was expressed in a single `go for` loop iterating over the source, with explicit `hextable` indexing:

```go
// Blog's hex encode (from practical-vector.md)
go for i := range src {
    dst[i*2] = hextable[src[i]>>4]
    dst[i*2+1] = hextable[src[i]&0x0f]
}
```

This is close to what shipped. But the blog focused on "expressing operations per-lane" and assumed the compiler would handle memory efficiently.

## §5.2 What the PoC discovered

The PoC shipped **two** versions:

```go
// Dst-centric: iterate over destination
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

// Src-centric: iterate over source (closer to the blog)
func EncodeSrc(dst, src []byte) int {
    go for i := range src {
        dst[i*2] = hextable[src[i]>>4]
        dst[i*2+1] = hextable[src[i]&0x0f]
    }
    return len(src) * 2
}
```

The dst-centric version hit **8.9x on WASM** but only **1.13x on AVX2**. The src-centric version (the blog's approach) triggered the byte-decomposition store pattern and performed better on AVX2.

The blog didn't anticipate:

- **The decomposed index path.** At byte granularity on AVX2 (32 lanes), maintaining a 32-element index vector is expensive. The PoC invented the scalar-base + `<N x i8>` lane-offset decomposition to make byte-lane iteration tractable. The blog had no notion of this.
- **The `vpshufb` table duplication.** The `hextable` is 16 bytes. On AVX2, `vpshufb` shuffles each 128-bit half independently, so the table must be duplicated to `[hextable, hextable]`. The blog assumed table lookups would "just work."
- **The performance reversal across targets.** Dst-centric wins on WASM; src-centric wins on AVX2. The blog assumed one algorithm would be universally best. The PoC taught us: **always benchmark both shapes on every target.**

---

# §6 The base64 algorithm was completely replaced

## §6.1 Blog's algorithm

Blog 3's base64 decoder was based on Miguel Young de la Sota's approach:

1. Perfect hash of ASCII characters → table index.
2. `SwizzleWithin` for parallel table lookup (nibble LUT).
3. `ShiftLeftWithin` to pack 6-bit sextets into bytes.
4. `RotateWithin` to combine bits across lane boundaries.
5. `SwizzleWithin` with an output pattern to extract 3 of every 4 bytes.

This is a direct translation of a SIMD-intrinsics algorithm into Go syntax. It requires the developer to understand the shuffle/rotate/extract pipeline.

## §6.2 PoC's algorithm

The PoC's base64 v2 decoder is based on Mula and Lemire's approach:

1. Nibble-LUT decode via a single `go for` at byte granularity.
2. Two cascading `go for` loops at decreasing widths (byte → int16 → int32) with constant-coefficient multiply-add.
3. A final `go for` for byte extraction via stride-3 stores.

No cross-lane operations. The multiply-add pattern is recognized by the compiler and emitted as `vpmaddubsw` / `vpmaddwd`. The stride-3 stores are recognized and emitted as `vpshufb`-based byte decomposition.

## §6.3 Why the switch

v1 (closer to the blog's approach, using `CompactStore` + `Rotate`) peaked at ~2x scalar. v2 (the cascading `go for` approach) peaked at ~77% of simdutf C++.

The difference is not in the algorithm's mathematical correctness — both produce the same output. The difference is in how well the compiler can optimize each shape:

- v1's explicit shuffles and rotations compile to what the developer asked for — specific shuffle instructions in a specific order. The compiler can't improve on the developer's choices.
- v2's idiomatic Go gives the compiler freedom. It recognizes the multiply-add shape and picks the best instruction for the target (pmadd on x86, deinterleave+widen+mul+add on WASM). It recognizes the stride-3 stores and picks the best pack strategy. It peels the loop and uses the all-ones fast path for the main body.

**The blog's approach treated the compiler as a translator. The PoC's approach treated the compiler as an optimizer.** The optimizer won.

---

# §7 APIs that weren't implementable as written

Several APIs the blogs sketched didn't survive contact with the type system and codegen:

## §7.1 `decodeChunk` returning `([]byte, bool)`

Blog 3's `decodeChunk` had this signature:

```go
func decodeChunk(ascii lanes.Varying[byte], pattern lanes.Varying[uint8]) ([]byte, bool)
```

A function that takes varying parameters and returns a `[]byte`. This can't work: the function is called under a mask (it's an SPMD function), and `[]byte` is a scalar type — which lane's output becomes the slice? The return type would need to be `(lanes.Varying[byte], bool)` at minimum, but even then, the caller needs to compact the varying output into a contiguous buffer.

The PoC's actual API:

```go
func decodeAndPack(dst, src []byte) int
```

Explicit output buffer, scalar return of bytes written. No varying returns.

## §7.2 `append` inside `go for`

Blog 3 used `decoded = append(decoded, decodedChunk...)` inside a `go for` loop. Append is a runtime operation that resizes the backing array — it cannot be vectorized. Each lane would need its own append target, but Go slices are scalar (uniform) data structures.

The PoC replaced this with explicit output buffers and offset tracking via `lanes.Count[byte]()`.

## §7.3 `dotMask` as a `[16]bool` scatter target

Blog 4 wrote `dotMask[i] = c == '.'` inside a `go for`, where `dotMask` is a regular `[16]bool`. This is a scatter store to a fixed-size array indexed by a varying value. The PoC does support this pattern (varying index into a uniform array), but the blog's subsequent use of `dotMask` in a second `go for` to build a bitmask via `reduce.Mask` was more complex than anticipated — the scatter + gather round-trip through a `[16]bool` intermediate is slower than computing the bitmask directly.

---

# §8 `lanes.Count` evolved from position tracking to chunk sizing

## §8.1 Blog's use

Blog 2 used `lanes.Count(c)` to track position in a string during a `go for` loop:

```go
loop += lanes.Count(c)
```

This increments a uniform counter by the number of lanes processed per iteration, so you can compute absolute positions in the input (e.g., for `reduce.FindFirstSet` results).

## §8.2 PoC's killer use

The most impactful use of `lanes.Count` in the PoC turned out to be entirely different: **chunk sizing for the outer scalar loop.**

```go
var bv lanes.Varying[byte]
chunkSize := max(4, lanes.Count[byte](bv))  // 16 on SSE, 32 on AVX2, 4 in scalar mode

for off := 0; off+chunkSize <= hotBytes; off += chunkSize {
    n := decodeAndPack(dst[outOffset:], src[off:off+chunkSize])
    outOffset += n
}
```

This pattern — use `lanes.Count[T]()` to size batches so each inner `go for` runs exactly one register-sized iteration — turned out to be the key insight for high-performance SPMD kernels. It eliminates the peeling overhead (no main+tail split when the iteration count equals the lane count) and lets LLVM fully unroll each inner `go for` to straight-line code.

The blog didn't anticipate this pattern at all. It emerged organically during base64 v2 optimization.

---

# §9 The `[16]byte` padding approach vs. the WASM guard zone

## §9.1 Blog's approach

Blog 4's IPv4 parser padded each input to 16 bytes before processing:

```go
input := [16]byte{}
copy(input[:], s)
```

This guarantees a full-width vector load won't overrun the input. It's correct but allocates a temporary buffer per call.

## §9.2 PoC's approach

The PoC reserves 16 bytes at the top of WASM linear memory as a guard zone:

```go
// tinygo/src/runtime/arch_tinygowasm.go:64
heapEnd = uintptr(wasm_memory_size(wasmMemoryIndex)*wasmPageSize) - 16
```

Now *every* vector load from *any* heap-allocated pointer can safely overread by up to 15 bytes. No per-call padding needed. The overread bytes are garbage, cleaned with a post-load mask.

On x86, a page-boundary check (`ptr & 0xFFF > 0xFF0`) gates a fast path that skips even the masking — raw `vmovdqu`, garbage bytes acceptable because callers use the execution mask.

This is more general, faster (no per-call copy), and applies to every SPMD memory access, not just IPv4 parsing.

---

# §10 Things the blogs didn't imagine at all

Some of the most important techniques in the PoC had no precedent in the blog series:

**SSA-level predication.** The blogs assumed the compiler would "just handle" varying control flow. The PoC discovered that predication (linearizing if/else/switch into masked selects) needed to happen at the SSA level, not in the backend, and required first-class metadata (`If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`). The entire x-tools-spmd fork (~2000 lines) emerged from this realization.

**SSA-level loop peeling.** The blogs had no concept of "main body with all-ones mask + masked tail." This turned out to be the single highest-leverage optimization, responsible for ~2x of the benchmark wins.

**The decomposed index path.** Wide SIMD + byte iteration = need for scalar base + `<N x i8>` offset. Entirely absent from the blogs because the blogs didn't think about what happens at 32 or 64 lanes.

**The mask stack detour.** We spent real time trying to avoid forking `golang.org/x/tools/go/ssa` by reconstructing masks in the TinyGo backend. The blogs had no notion of this constraint; they assumed the compiler would "figure it out." The lesson — SPMD is a compiler feature that has to live at the heart of the SSA form — was hard-won.

**Pattern detection as a philosophy.** The blogs assumed developers would write hardware-aware algorithms. The PoC proved that compiler pattern detectors (for pmadd, byte-decomposition, contiguous access) produce better results from simpler source code. This is the single biggest conceptual shift from the blog series to the final implementation.

---

# §11 Summary: what the blogs' author should have known

If I could send a message back to the blog-writing version of myself, it would be:

1. **Don't design the cross-lane vocabulary up front.** You'll build it, benchmark it, and delete most of it. Instead, invest early in pattern detection — recognizers for multiply-add widen, contiguous access, and byte-decomposition store will deliver more performance from simpler user code than any number of `SwizzleWithin` calls.

2. **The base64 decoder you imagine will be replaced.** The cross-lane approach (blog 3) will peak at 2x. The idiomatic-Go approach (cascading `go for` with plain arithmetic) will hit ~77% of simdutf. Trust the compiler more than your SIMD intuition.

3. **Inner-SPMD is not always the right shape.** Your IPv4 parser will be slower than scalar. The fix is outer-SPMD batching (across inputs), not inner-SPMD scanning (within one input). Think about input size relative to SIMD width before choosing the shape.

4. **You will need to fork `go/ssa`.** Trying to avoid it with a backend mask stack will cost months of debugging. Accept the three-fork maintenance burden early and build predication at the SSA level from day one.

5. **`lanes.Count[T]()` for chunk sizing is the killer pattern.** You'll discover it late and wish you'd known earlier. Size your outer loop to feed one register-width chunk per inner `go for` iteration.

6. **The readable code wins.** The blog series' thesis was that SPMD Go should be readable. The PoC proved something stronger: **the simplest, most readable Go code also produces the fastest SIMD output**, because the compiler's pattern detectors work best on idiomatic patterns. Complexity is not just a readability cost — it's a performance cost.

---

*For the technical details behind each of these lessons, see `implementer-notes.md`, `developer-guide.md`, and `novel-patterns.md` in this directory.*
