# IPv4 Parser SPMD Status & Performance Analysis

**Date**: 2026-03-02
**Source**: `test/integration/spmd/ipv4-parser/main.go`
**Based on**: Wojciech Muła's SIMD IPv4 parsing research

## Current Status: Compiles and Runs Correctly

The ipv4-parser is a key PoC goal — a real-world parsing algorithm using SPMD Go.
As of 2026-03-02, it compiles cleanly and produces **correct results** matching scalar output
for all valid and invalid IP addresses.

**Performance**: 0.61x on Node.js (SPMD slower than scalar). See analysis below.

### Progress Timeline

| Stage | Status | Notes |
|-------|--------|-------|
| Parsing | PASS | `go for`, `range input`, `range starts` all parse correctly |
| Type Checking | PASS | All SPMD rules enforced correctly |
| SSA Generation | PASS | x-tools SSA builder succeeds |
| LLVM IR Generation | PASS | IR generates cleanly |
| LLVM Verification | PASS | All verification errors resolved |
| WASM Codegen | PASS | SIMD128 WASM binary produced |
| Runtime Correctness | **PASS** | Output matches scalar for all test cases |
| Performance | **0.61x** | SPMD slower than scalar (see analysis) |

### Fixes Applied (2026-02-28 to 2026-03-02)

1. **`anytrue.v4i1` intrinsic type mismatch** — FIXED
   - `Varying[bool]` uses `<4 x i1>` but anytrue.v4i1 was being called with `<4 x i32>`.
   - Fix: `spmdVectorAnyTrue` now calls `spmdWasmAnyTrue` directly without inserting sext.

2. **`Varying[bool]` switch merge type mismatch** — FIXED
   - `spmdCreateSwitchMergeSelect` built cascaded selects mixing `<4 x i1>` and `<4 x i32>`.
   - Fix: Normalize `<4 x i1>` to `<4 x i32>` before `spmdMaskSelect`.

3. **LOR phi tail-phase predecessor mismatch** — FIXED
   - Value-LOR deferred phi in tail phase referenced main-phase blocks.
   - Fix: Resolve tail-phase merge phi overrides BEFORE restoring `b.blockInfo`.

4. **Accumulator phi missing predecessor** — FIXED
   - Tail-phase If body block branched to loop block without wiring the phi value.
   - Fix: Detect and redirect tail-phase loop-targeting branches to `tailExitBlock`.

5. **`<4 x i8>` sub-128-bit vector promotion** — FIXED
   - Gather results for byte arrays in 4-lane loops produced illegal `<4 x i8>` on WASM.
   - Fix: Zero-extend sub-128-bit gather results to `<4 x i32>`.

6. **Multi-lane-count select mismatch** — FIXED
   - 16-lane `ChangeType` bitcast produced `<16 x i8>` from `<4 x i32>` in a 4-lane loop context.
   - Fix: `dotMaskTotal` changed from `Varying[uint32]` to `Varying[uint8]` (source fix).

7. **Lane count detection for multi-loop programs** — FIXED (cdc30d7)
   - `spmdFindActiveLoopForBlock` returned first dominating loop, not innermost.
   - Fix: Select loop with highest body block index among dominating loops.
   - Also: ChangeType unwrap in `spmdRangeIndexLaneCount` for byte array element detection.

8. **LOR/LAND mask format normalization** — FIXED (cdc30d7)
   - LOR/LAND in multi-loop programs mixed mask formats (`<16 x i8>` vs `<4 x i32>`).
   - Fix: `spmdMatchMaskFormat` normalization at all 4 LOR/LAND emission sites.

9. **Bool array contiguous load/store bit-packing** — FIXED (4715645)
   - `llvm.masked.load.v16i1` reads 16 bits (bit-packed) but `[16]bool` stores 1 byte per element.
   - Fix: Load as `<N x i8>` then truncate to `<N x i1>`; store by zext `<N x i1>` to `<N x i8>`.

### SPMD Features Exercised

The parser exercises a wide range of SPMD features:

1. **Multiple `go for` loops** with different array types and lane counts:
   - `go for i, c := range input` — iterates `[16]byte`, 16 lanes (i8x16)
   - `go for _, isDot := range dotMask` — iterates `[16]bool`, 16 lanes
   - `go for i, start := range starts` — iterates `[4]int`, 4 lanes (i32x4)
   - `go for field, start := range starts` — iterates `[4]int`, 4 lanes

2. **Varying switch statement** (`switch fieldLen`) — N-way branch on varying value
3. **Uniform early returns** inside `go for` (via `reduce.All`/`reduce.Any` guards)
4. **Varying array indexing** (`dotMask[i]`, `ends[i]`, `s[start]`)
5. **Reduce operations**: `reduce.All`, `reduce.Any`, `reduce.Add`, `reduce.Mask`, `reduce.FindFirstSet`
6. **Cross-type operations**: `lanes.Count(c)` for loop stride tracking
7. **Mixed scalar/vector arithmetic**: bit manipulation (`bits.TrailingZeros16`), type conversions

---

## Performance Analysis

### Architecture Overview

The ipv4-parser has 4 SPMD loops:

| Loop | Range | Lanes | Purpose |
|------|-------|-------|---------|
| 1 | `[16]byte` input | 16 (i8x16) | Find dot positions, classify characters |
| 2 | `[16]bool` dotMask | 16 (i8x16) | Build 16-bit dot position bitmask |
| 3 | `[4]int` starts | 4 (i32x4) | Compute octet start/end boundaries |
| 4 | `[4]int` starts | 4 (i32x4) | Parse digit characters into integers |

### Generated WASM Profile

| Section | SIMD Ops | Scalar Ops | Notes |
|---------|----------|------------|-------|
| Loop 1 (dot/char classification) | 30 | — | i8x16 eq/and/or/bitmask — efficient |
| Loop 2 (bitmask construction) | 247 | — | Per-lane extract + accumulate — avoidable |
| Loop 3 (boundary computation) | 155 | — | i32x4 bit manipulation |
| Loop 4 (digit parsing) | ~120 | — | Gather-heavy, data-dependent |
| Scalar between loops | — | ~80 | `bits.TrailingZeros16`, bounds validation |
| **Total** | **~552** | **~80** | **~632 total** |

Scalar implementation: ~150 instructions for the equivalent work.

### Bottleneck 1 (Priority 1): Loop 2 is Fully Avoidable

**Impact**: ~247 ops (~40% of all SPMD work)
**Type**: Source-level rewrite

Loop 1 already computes `i8x16.eq` + `i8x16.bitmask`, producing a 16-bit integer where each
bit indicates a dot position. This is exactly the information Loop 2 reconstructs lane-by-lane:

```wat
;; Loop 1 already produces:
i8x16.eq       ;; <16 x i1> dot positions
i8x16.bitmask  ;; i32 with 16-bit dot mask (via reduce.Mask)

;; Loop 2 then wastefully rebuilds the same information:
loop 247 instructions:
  i8x16.extract_lane_u  ;; extract each bool
  i32.const 1; i32.shl  ;; shift to bit position
  i32.or                 ;; accumulate into bitmask
  ... × 16 lanes
```

**Fix**: Replace Loop 2 with direct use of the `reduce.Mask` result from Loop 1. The `dotMask`
bitmask is already computed — Loop 2 should be eliminated entirely. This requires a source-level
change to the ipv4-parser algorithm.

### Bottleneck 2 (Priority 2): Input Loading as Gather Instead of v128.load

**Impact**: 15 `v128.load8_lane` instead of 1 `v128.load`
**Type**: Compiler fix

Loop 1 iterates over `[16]byte` with 16 lanes. The input load should be a single contiguous
`v128.load` but instead generates a per-lane gather:

```wat
;; Current (slow):
v128.load8_lane 0   ;; load byte 0
v128.load8_lane 1   ;; load byte 1
... × 15 more       ;; 15 more lane loads

;; Optimal:
v128.load            ;; single 128-bit load
```

**Root cause**: `spmdAnalyzeContiguousIndex` doesn't recognize `range [16]byte` as contiguous
when the range produces a varying index over an array (as opposed to a slice). The contiguous
detection traces the index through `BinOp ADD` to find the loop iterator, but the range-over-array
pattern uses a different SSA shape than range-over-slice.

**Fix**: Extend `spmdAnalyzeContiguousIndex` to recognize array range indices as contiguous
when the array base is uniform and the index is the loop iterator.

### Bottleneck 3 (Priority 3): Serialized Per-Lane Bounds Checks

**Impact**: 21 `extract_lane` + `br_if` sequences
**Type**: Compiler fix

Array indexing with varying indices generates per-lane bounds checks:

```wat
;; Current: 21 serialized bounds checks
i32x4.extract_lane 0; local.get array_len; i32.ge_u; br_if trap
i32x4.extract_lane 1; local.get array_len; i32.ge_u; br_if trap
i32x4.extract_lane 2; local.get array_len; i32.ge_u; br_if trap
i32x4.extract_lane 3; local.get array_len; i32.ge_u; br_if trap
... × more sites
```

**Fix**: Replace with a single vector comparison + bitmask check:

```wat
;; Optimal: single vector bounds check per site
i32x4.splat array_len
i32x4.ge_u indices, splat_len
v128.any_true
br_if trap
```

This requires a new `spmdVectorBoundsCheck` that performs the comparison in vector form
rather than extracting each lane.

### Bottleneck 4 (Unavoidable): Data-Dependent Digit Gather

**Impact**: ~120 ops in Loop 4
**Type**: Fundamental — cannot be optimized away

Loop 4 parses digits from positions computed at runtime (`s[start+0]`, `s[start+1]`, etc.).
Since each lane's `start` value is different, the memory access pattern is inherently
non-contiguous (gather). Each lane loads from a different address:

```wat
;; Lane 0: s[0], s[1], s[2]    (octet "192")
;; Lane 1: s[4], s[5]          (octet "168")
;; Lane 2: s[7]                (octet "1")
;; Lane 3: s[9], s[10]         (octet "42")
```

This is the unavoidable cost of inner-level parallelism on variable-length fields.

### Fundamental Architectural Limitation

The ipv4-parser applies SPMD at the **inner level** — processing 4 octets of a single IP
address in parallel. This limits the parallelism to 4 lanes (4 octets per IP), where scalar
processes the same 4 octets sequentially with simpler code.

| Approach | Parallelism | Lanes | Work per IP |
|----------|-------------|-------|-------------|
| Current (inner) | 4 octets × 1 IP | 4 (i32x4) | ~632 SIMD + ~80 scalar |
| Scalar | 1 octet × 1 IP | 1 | ~150 scalar |
| Outer (ideal) | 1 octet × 16 IPs | 16 (i8x16) | ~150 × 1/16 per IP |

**Outer-level parallelism** — parsing 4 or 16 IPs simultaneously — would yield real speedup
by amortizing the scalar overhead across multiple IPs. However, this requires a fundamentally
different algorithm structure (SOA layout, uniform control flow across IPs of similar length).

### Optimization Roadmap

| Priority | Fix | Type | Expected Impact |
|----------|-----|------|-----------------|
| 1 | Eliminate Loop 2 (reuse bitmask) | Source rewrite | -247 ops (~40% reduction) |
| 2 | Contiguous array load detection | Compiler fix | -14 ops per load site |
| 3 | Vector bounds check | Compiler fix | -21 serialized checks |
| — | Outer-level parallelism | Algorithm redesign | Potential 4-16x improvement |

With Priority 1 alone, the SPMD version would drop from ~632 to ~385 total ops, potentially
reaching ~1.0-1.2x parity with scalar. Adding Priorities 2 and 3 could push to ~1.3-1.5x.
True speedup requires the outer-level redesign.

### Comparison with Other SPMD Benchmarks

| Benchmark | Speedup (wasmtime) | Parallelism Level | Key Factor |
|-----------|-------------------|-------------------|------------|
| hex-encode (src) | **~19-20x** | Outer (16 bytes) | Eliminates hextable loads |
| hex-encode (dst) | **~6.25x** | Outer (8 bytes) | Half throughput of src |
| mandelbrot | **~3.19x** | Outer (4 pixels) | Compute-bound, good fit |
| ipv4-parser | **0.61x** | Inner (4 octets) | Overhead exceeds parallelism |

The pattern is clear: outer-level parallelism (processing independent data items) yields
strong speedups. Inner-level parallelism (parallelizing within a single item) only helps
when the item has enough regular structure to amortize SIMD overhead.

### References

- Wojciech Muła, "SIMD-friendly algorithms for substring searching" and IPv4 parsing work
- Daniel Lemire et al., SIMD parsing techniques
- ISPC documentation on inner vs outer SPMD patterns
