# SIMD algorithms for blazing-fast IP address parsing

**A single IPv4 address can be parsed in ~50 machine instructions using SIMD — roughly 6× fewer than glibc's `inet_pton`.** The best-known approach, developed by Wojciech Muła and refined by Daniel Lemire, achieves **2.7 GB/s** throughput (200 million addresses per second) on Intel Ice Lake by exploiting a key structural insight: only 81 valid dot-position patterns exist in IPv4 strings, and a perfect hash over the dot bitmask selects a precomputed PSHUFB shuffle mask that rearranges variable-length digit groups into fixed positions for SIMD multiply-accumulate conversion. IPv6 remains largely unsolved in pure SIMD due to `::` abbreviation complexity, though hex-digit conversion reuses the same multiply-add chain. This report covers the core algorithms, architecture-specific implementations across x86-64, ARM64, and WebAssembly SIMD, error detection strategies, batch parsing approaches, and cross-platform abstraction options.

## The Muła-Lemire SIMD IPv4 algorithm in five stages

The canonical SIMD IPv4 parser fits a 7–15 character string (e.g., `"192.168.1.1"`) into a single 128-bit register and processes it through five pipelined stages with no branches in the hot path.

**Stage 1 — Load and classify.** Load 16 bytes into an SSE register via `_mm_loadu_si128`. Compare every byte against `'.'` using `_mm_cmpeq_epi8`, then extract the comparison result to a scalar bitmask with `_mm_movemask_epi8`. This produces a **dot bitmask** — e.g., for `"192.168.1.1"` the bitmask is `0b00000010010001000`. Verify exactly 3 dots via `__builtin_popcount(dotmask) == 3`.

**Stage 2 — Validate characters.** All non-dot bytes must be ASCII digits. The trick uses signed-domain range checking: subtract a biased constant `(-128 + '0')` from every byte, then use `_mm_cmplt_epi8` against `(-128 + 10)`. This converts the unsigned range check `'0' ≤ c ≤ '9'` into a signed comparison in one step. The resulting bitmask, OR'd with the dot mask, must equal the string-length mask — otherwise an invalid character is present.

**Stage 3 — Hash the dot pattern to select a shuffle mask.** The dot bitmask determines the digit-group lengths (1, 2, or 3 digits per octet). Only **81 valid patterns** exist. Lemire's improved hash maps the bitmask to a table index using ~255 bytes of storage:

```c
// Lemire's compact hash — maps ~32K possible bitmasks to 81 valid entries
uint16_t hash = (dotmask >> 5) ^ (dotmask & 0x03ff);
// Lookup precomputed shuffle mask + conversion procedure ID
const entry_t *e = &table[hash];
if (e->expected_dotmask != dotmask) return ERROR;
```

Each table entry contains a **16-byte PSHUFB pattern** and a procedure selector (whether the maximum octet width is 1, 2, or 3 digits). Total storage is ~1.5 KB for the hash table and shuffle masks.

**Stage 4 — Rearrange digits via PSHUFB.** A single `_mm_shuffle_epi8` instruction permutes the input bytes into fixed positions suitable for the multiply-add chain. For a 3-digit-max case, digits are arranged as `[ones₀, tens₀, hundreds₀, 0, ones₁, tens₁, hundreds₁, 0, ...]` across the register — all four octets simultaneously.

**Stage 5 — SIMD multiply-add conversion and range check.** The heart of the algorithm:

```c
// Subtract ASCII '0' with unsigned saturation (zeros stay zero)
__m128i digits = _mm_subs_epu8(shuffled, _mm_set1_epi8('0'));

// Multiply-add: 10*tens + ones (lower half), 100*hundreds (upper half)
__m128i weights = _mm_setr_epi8(
    10, 1, 10, 1, 10, 1, 10, 1,      // PMADDUBSW: pairs → 16-bit
    100, 0, 100, 0, 100, 0, 100, 0);
__m128i partial = _mm_maddubs_epi16(digits, weights);  // SSSE3

// Combine partial results: shift upper 8 bytes down and add
__m128i shifted = _mm_alignr_epi8(partial, partial, 8);
__m128i octets = _mm_add_epi16(partial, shifted);

// Validate: all octets must be ≤ 255
__m128i overflow = _mm_cmpgt_epi16(octets, _mm_set1_epi16(255));
if (_mm_movemask_epi8(overflow) & 0xFF) return ERROR;

// Pack 16-bit values to bytes → final 4-byte IPv4 address
uint32_t ipv4 = _mm_cvtsi128_si32(_mm_packus_epi16(octets, octets));
```

This entire pipeline executes in roughly **52 instructions** on x86-64 with SSSE3/SSE4.1, versus ~300 instructions for glibc's `inet_pton`.

## The multiply-add chain is the universal SIMD parsing primitive

The logarithmic-depth decimal-to-integer conversion chain pioneered by Muła and used throughout simdjson is the foundation for all SIMD number parsing. It converts digit strings to integers in O(log n) depth:

```
Input bytes:    '1'  '9'  '2'  '1'  '6'  '8'  '0'  '1'
After -'0':      1    9    2    1    6    8    0    1
PMADDUBSW ×[10,1]:  19       21       68       01    (8→16-bit)
PMADDWD ×[100,1]:     1921           6801            (16→32-bit)
Final step ×10000+1:        19216801                 (32→64-bit)
```

**Three SIMD instructions** convert 8 digits to a 32-bit integer. `_mm_maddubs_epi16` (SSSE3) is the star instruction — it multiplies unsigned bytes by signed bytes and horizontally adds adjacent pairs into 16-bit results. `_mm_madd_epi16` (SSE2) does the same at 16→32-bit width. For IPv4, only the first two stages are needed since octets are at most 3 digits.

The equivalent **SWAR (SIMD-Within-A-Register)** version works with standard 64-bit arithmetic for portable code:

```c
uint64_t val;
memcpy(&val, chars, 8);
val -= 0x3030303030303030;  // subtract '0' from all bytes
val = ((val * (1 + (0xa << 8))) >> 8) & 0x00FF00FF00FF00FF;   // ×10 + combine pairs
val = ((val * (1 + (0x64 << 16))) >> 16) & 0x0000FFFF0000FFFF; // ×100 + combine quads
val *= (1 + (10000ULL << 32));
return val >> 32;
```

## x86-64 instruction toolkit for IP parsing

The x86-64 SIMD IP parser relies on a tight set of instructions spanning SSE2 through SSE4.1. **PSHUFB** (`_mm_shuffle_epi8`, SSSE3) is the single most important — it performs a 16-entry byte-wise table lookup that serves as both digit rearrangement and nibble-based character classification. **PMADDUBSW** (`_mm_maddubs_epi16`, SSSE3) provides the multiply-add chain. **PMOVMSKB** (`_mm_movemask_epi8`, SSE2) extracts comparison results to scalar bitmasks for structural validation.

For hex digit conversion (IPv6), the nibble-decomposition technique from simdjson uses two PSHUFB lookups:

```c
__m128i lo_nibble = _mm_and_si128(input, _mm_set1_epi8(0x0F));
__m128i hi_nibble = _mm_and_si128(_mm_srli_epi16(input, 4), _mm_set1_epi8(0x0F));
__m128i classification = _mm_and_si128(
    _mm_shuffle_epi8(table_lo, lo_nibble),
    _mm_shuffle_epi8(table_hi, hi_nibble));
```

The tables are designed so `table_lo[lo(b)] & table_hi[hi(b)]` yields the correct classification for byte `b`, covering all 256 input values with just 32 bytes of lookup data and 3 instructions.

**AVX2** doubles register width to 256 bits but operates as two independent 128-bit lanes for VPSHUFB — meaning each IPv4 address still occupies one lane. The practical AVX2 benefit is processing **two addresses simultaneously** in a single register, or scanning 32 bytes at once during structural indexing of log files. The lane-crossing limitation means cross-lane shuffles require additional `VPERM` instructions.

## ARM64 NEON requires different strategies for three key operations

Porting the x86 algorithm to ARM64 NEON encounters three significant gaps that require architectural workarounds.

**The PSHUFB equivalent works well.** NEON's `TBL` instruction (`vqtbl1q_u8`) is actually more powerful than PSHUFB — it supports table sizes of 16, 32, 48, or 64 bytes across 1–4 registers, and zeroes out-of-range indices cleanly. The digit rearrangement stage maps directly:

```c
uint8x16_t rearranged = vqtbl1q_u8(input, shuffle_pattern);
```

**No PMADDUBSW equivalent exists.** NEON lacks horizontal byte-pair multiply-add. The workaround uses widening multiply-accumulate in multiple steps:

```c
// Step 1: Pre-shuffle to place even-indexed digits in high half, odd in low
uint8x16_t shuffled = vqtbl1q_u8(digits, interleave_mask);

// Step 2: Widen low half to 16-bit, multiply-accumulate high half by 10
uint16x8_t low = vmovl_u8(vget_low_u8(shuffled));
uint16x8_t combined = vmlal_high_n_u8(low, shuffled, 10);  // high*10 + low

// Step 3: Same pattern at 16→32-bit for ×100
uint32x4_t low32 = vmovl_u16(vget_low_u16(combined));
uint32x4_t result = vmlal_high_n_u16(low32, combined, 100);
```

This requires **~3× more instructions** than the x86 path for digit conversion, though each instruction is fast on modern ARM cores (Apple M-series, Cortex-A76+).

**No PMOVMSKB equivalent exists.** Extracting a per-byte bitmask from comparison results — trivial on x86 — requires multi-instruction workarounds on NEON. The most efficient approach uses narrowing shifts:

```c
uint8x16_t cmp = vceqq_u8(input, vdupq_n_u8('.'));
uint16x8_t as_u16 = vreinterpretq_u16_u8(cmp);
uint8x8_t narrowed = vshrn_n_u16(as_u16, 4);   // shift right 4, narrow to 8-bit
uint64_t mask = vget_lane_u64(vreinterpret_u64_u8(narrowed), 0);
```

An alternative from simdjson's NEON backend multiplies by power-of-two weights and uses horizontal add (`vaddvq_u8`, AArch64-only) to pack comparison bits. **This bitmask extraction gap is the largest x86/ARM performance disparity** for parsing workloads — ~6 instructions on ARM versus 1 on x86.

Despite these gaps, NEON offers compensating advantages: `vqtbl4q_u8` accesses a **64-byte lookup table** in one instruction (useful for IPv6 hex classification), interleaved loads (`vld2q_u8`) can deinterleave structured data on load, and `vbslq_u8` provides three-operand bitwise select more flexible than x86's `PBLENDVB`.

## WebAssembly SIMD covers most operations but has a critical gap

WASM SIMD (finalized in WebAssembly 2.0, universally supported in browsers since Safari 16.4 in March 2023) provides 128-bit vectors with over 200 instructions. The mapping to the IPv4 parsing algorithm is mostly complete but has one significant hole.

**Available and efficient:**
- `i8x16.swizzle` maps to PSHUFB for digit rearrangement (with slight overhead on x86 due to differing out-of-range semantics — `relaxed_i8x16.swizzle` eliminates this)
- `i8x16.eq` + `i8x16.bitmask` maps directly to PCMPEQB + PMOVMSKB for dot detection and bitmask extraction
- `i32x4.dot_i16x8_s` maps to PMADDWD for the 16→32-bit multiply-add stage
- `v128.bitselect` for conditional operations, `i8x16.sub` for ASCII conversion

**The critical gap: no `_mm_maddubs_epi16` equivalent.** The PMADDUBSW instruction that multiplies adjacent byte pairs and horizontally adds them into 16-bit results has **no single WASM SIMD instruction**. The workaround requires emulation:

```
// x86: 1 instruction
__m128i result = _mm_maddubs_epi16(digits, weights);

// WASM SIMD: 3+ instructions  
v128_t lo = i16x8_extmul_low_i8x16_u(digits, weights);   // even pairs
v128_t hi = i16x8_extmul_high_i8x16_u(digits, weights);  // odd pairs  
v128_t result = i16x8_add(lo, hi);                        // combine
```

**Relaxed SIMD** (a newer extension) adds `i16x8.relaxed_dot_i8x16_i7x16_s` which closely matches PMADDUBSW semantics, but with implementation-defined behavior for some edge cases. This extension trades determinism for performance.

The other major WASM limitation is **fixed 128-bit width** — no AVX2 (256-bit) or AVX-512 (512-bit) equivalent exists. Highway's `HWY_WASM_EMU256` target mitigates this by unrolling 128-bit operations 2×, but this adds instruction overhead rather than true wider execution. Benchmarks show WASM SIMD typically achieves **60–95% of native 128-bit SIMD performance**, with the gap coming from JIT compilation overhead and semantic-emulation instructions.

## Branchless error detection without sacrificing throughput

SIMD IP parsers integrate validation directly into the parsing pipeline using three complementary techniques, with **a single branch at the end** checking an accumulated error register.

**Character validation** runs in parallel with digit conversion. The signed-domain range trick (`_mm_sub_epi8(input, _mm_set1_epi8(-128 + '0'))` followed by `_mm_cmplt_epi8`) classifies all 16 bytes as valid/invalid digits simultaneously. The resulting bitmask is OR'd with the dot bitmask and compared against the expected length mask — if they differ, a non-digit, non-dot character exists.

**Structural validation** operates on the scalar dot bitmask: `popcount == 3` confirms the dot count; `(dotmask & (dotmask >> 1)) == 0` catches adjacent dots (empty octets); the hash-table lookup itself rejects invalid dot patterns since only 81 of ~32K possible bitmask values are valid. Leading-zero detection checks whether multi-digit fields have most-significant digits of '0' after PSHUFB rearrangement, using `_mm_cmpeq_epi8` on the hundreds/tens positions.

**Range validation** catches octets exceeding 255 after the multiply-add conversion produces 16-bit values: `_mm_cmpgt_epi16(octets, _mm_set1_epi16(255))` flags overflow, and `_mm_movemask_epi8` extracts the result to a scalar bit check.

The key design principle is **error accumulation**: OR all error indicators into a single register, branch once at the end. If an error is detected and a detailed error message is needed, re-parse with scalar code — errors are rare in well-formed input, so this "fast path/slow path" split has near-zero amortized cost.

## IPv6 remains largely unsolved by pure SIMD

No fully SIMD-optimized IPv6 parser exists in the published literature. IPv6 parsing is fundamentally harder because of **variable-length hex groups** (1–4 hex digits per group), the **`::` zero-compression abbreviation** (which can appear once and represents an arbitrary number of zero groups), and **mixed notation** (`::ffff:192.168.1.1`). These features create data-dependent control flow that resists branchless SIMD pipelines.

The practical approach to partial SIMD acceleration of IPv6 uses SIMD for the parallelizable substeps: character classification (distinguishing hex digits, colons, dots) via nibble-decomposition PSHUFB lookups; hex-to-binary conversion of individual 4-character groups using the multiply-add chain with weight `[16, 1, 16, 1, ...]`; and colon-position detection via bitmask extraction. The `::` expansion and group counting remain scalar. The `hunyadi/simdparse` library and `ada-url` project both fall back to `inet_pton` for IPv6, confirming the difficulty.

## Batch parsing benefits from structural indexing, not parallel IP conversion

Despite extensive search, **no published implementation processes multiple IP addresses simultaneously in a single SIMD register**. The fundamental obstacle is variable-length encoding — IPv4 strings range from 7 to 15 bytes, making alignment for parallel processing impractical without preprocessing.

The proven production pattern, demonstrated by **simdzone** (NLnet Labs' DNS zone parser used in NSD 4.10.0), follows the simdjson architecture: a SIMD **structural indexing phase** scans the entire input buffer at 32–64 bytes per cycle to locate record boundaries and field delimiters, building an index of IP address positions. A subsequent **parsing phase** processes each IP individually using the fast single-IP SIMD parser. This approach achieves **3–4× speedup** over traditional parsers and **>0.8 GB/s** throughput on zone files containing millions of records.

For log file processing specifically, the highest-impact SIMD application is typically the structural scanning — finding newlines and field separators — rather than the IP parsing itself, since field extraction dominates total work when records contain many fields. The single-IP SIMD parser's **~1.5 KB lookup table** stays warm in L1 cache throughout batch processing, amortizing setup cost to effectively zero.

## Cross-platform abstraction: Highway leads, with Rust and Zig as alternatives

**Google Highway** (C++17, Apache-2.0) is the most mature cross-platform SIMD library, targeting **27 instruction sets** including SSE4, AVX2, AVX-512 variants, NEON, SVE/SVE2, RISC-V RVV, and WebAssembly SIMD. It uses tag-based dispatch with zero-sized `Simd<T, N, kPow2>` types that naturally handle scalable vector architectures (SVE, RVV) where register width is unknown at compile time. Its `HWY_DYNAMIC_DISPATCH` mechanism generates per-target function tables and selects the optimal implementation at runtime. For parsing, Highway provides `TableLookupBytes` (PSHUFB/TBL/swizzle), `MoveMask`/`BitsFromMask`, `WidenMulPairwiseAdd`, `FindFirstTrue`, and `CountTrue` — covering every primitive needed for the IPv4 algorithm. Highway powers JPEG XL, V8, gemma.cpp, and is under consideration for NumPy's SIMD backend.

**Rust's `std::simd`** (portable SIMD) remains nightly-only as of early 2026, with significant stabilization blockers around the `LaneCount` trait bound and swizzle API design. For production Rust, the **`wide`** and **`pulp`** crates provide stable SIMD abstractions with multiversioning support. Rust's `std::arch::wasm32` module (stable since Rust 1.61) provides direct access to WASM SIMD intrinsics including `i8x16_swizzle` and `i8x16_bitmask`.

**Zig's `@Vector(N, T)`** is built into the language as a first-class type with automatic SIMD lowering via LLVM. All standard operators work element-wise, and `@reduce(.Add, vec)` provides horizontal reductions. The main limitation for parsing is that **`@shuffle` requires compile-time-known indices** — runtime-dependent byte rearrangement (the PSHUFB step) requires dropping to architecture-specific builtins or inline assembly.

**C++ `std::experimental::simd`** (P1928, heading into C++26) ships with GCC ≥11 and provides a `simd<T, Abi>` template with `native_simd<T>` aliases. It lacks WASM targeting and is primarily tested on x86, making it less suitable for the three-architecture target specified here.

| Abstraction | WASM support | Runtime dispatch | Runtime swizzle | Maturity |
|---|---|---|---|---|
| Google Highway | ✅ `HWY_WASM` | ✅ Built-in | ✅ `TableLookupBytes` | Stable (1.0+) |
| Rust `std::simd` | ✅ via wasm32 | ❌ Manual | ✅ `.swizzle()` | Nightly only |
| Zig `@Vector` | ✅ via LLVM | ❌ Compile-time | ❌ Compile-time only | Stable |
| C++ P1928 | ❌ | ❌ | Limited | GCC only (TS) |

## Measured performance across approaches

Lemire's benchmarks on Intel Ice Lake (GCC 11 / LLVM 16) establish the definitive SIMD performance ceiling:

| Parser | Instructions/addr | Throughput | Addresses/sec |
|---|---|---|---|
| `inet_pton` (glibc) | ~300 | 0.38 GB/s | 35M |
| `fast_float`-based scalar | ~178 | 0.81 GB/s | 51M |
| Optimized manual scalar | ~94 | 1.03 GB/s | 65M |
| **`sse_inet_aton` (SIMD)** | **~52** | **2.7 GB/s** | **200M** |

On Apple M1, Lemire's portable non-SIMD parser reaches **1.03 GB/s** (65M addresses/sec) — demonstrating that carefully optimized scalar code captures much of the theoretical benefit on ARM. Graham's analysis reveals that **branch mispredictions** cause 2× slowdowns on large datasets with random IP patterns: a branchy parser running at 5.4 ns/addr on 15K addresses degrades to 11.9 ns/addr on 1.5M addresses, while the branchless SWAR version maintains consistent ~5.0 ns/addr throughput regardless of dataset size.

## Conclusion

The Muła-Lemire SIMD IPv4 parser represents a mature, proven technique achieving **~7× speedup** over standard library parsers through three key insights: dot-bitmask hashing to select from 81 precomputed shuffle patterns, PSHUFB-based digit rearrangement, and the PMADDUBSW/PMADDWD multiply-add chain for parallel decimal conversion. The algorithm ports to ARM64 NEON with ~3× more instructions for the digit conversion stage (due to missing PMADDUBSW) and to WebAssembly SIMD with similar overhead plus the PMADDUBSW emulation cost — though Relaxed SIMD's `i16x8.relaxed_dot_i8x16_i7x16_s` closes this gap. Google Highway provides the strongest cross-platform abstraction covering all three target architectures with runtime dispatch.

Two significant open problems remain. IPv6 SIMD parsing lacks a published solution due to `::` abbreviation complexity — a hybrid approach using SIMD for hex classification and digit conversion with scalar `::` expansion logic is the practical path. True batch-parallel IP parsing (multiple addresses in one register) remains unexplored; the production-proven pattern from simdzone uses SIMD structural indexing to locate addresses at high throughput, then applies the fast single-IP parser sequentially. For implementers targeting all three architectures, starting with Highway's `TableLookupBytes` and `WidenMulPairwiseAdd` abstractions, with architecture-specific fast paths for the PMADDUBSW step, offers the best balance of performance and portability.