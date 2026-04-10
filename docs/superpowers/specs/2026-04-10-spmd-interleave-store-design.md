# Design Spec: `SPMDInterleaveStore` — Deinterleave Optimization

**Date**: 2026-04-10
**Status**: Draft
**Motivation**: SPMDMux + CompactStore generates N select instructions + per-lane scatter (111 instructions on SSSE3 for base64). When the SPMDMux has periodic indices (from `i % K`) and CompactStore has a matching periodic mask, the entire output path can be replaced with N diagonal-extraction shuffles + ORs + one compaction shuffle + one contiguous store (~7 instructions).

**Depends on**: `docs/superpowers/specs/2026-04-10-spmd-mux-design.md`

## 1. Scope

New SSA instruction `SPMDInterleaveStore` + detection pass + TinyGo lowering. Optimizes the output path only — the redundant computation of all formulas for all lanes is NOT eliminated (future dead-lane elimination).

**Files**: x-tools-spmd (SSA instruction + detection), TinyGo (lowering + dispatch)

**Success criteria**: Base64 SSSE3 from ~10x to ~14-16x. Instruction count for output path drops from ~111 to ~7.

## 2. `SPMDInterleaveStore` SSA Instruction

```go
// SPMDInterleaveStore writes N value vectors interleaved with period K
// contiguously to Addr. Replaces SPMDMux + CompactStore when the Mux
// indices are periodic and the CompactStore mask matches.
//
// For period K=4 with 3 values [A, B, C] and 16 byte lanes:
//   From each group of K input lanes, extracts the diagonal:
//     group g: Values[0][g*K+0], Values[1][g*K+1], Values[2][g*K+2]
//   Output = [A[0],B[1],C[2], A[4],B[5],C[6], A[8],B[9],C[10], A[12],B[13],C[14]]
//
// Returns the number of elements written: N * Lanes/K (compile-time constant).
//
// Example printed form:
//
//	t9 = spmd_interleave_store<16, period=4> t1 [t3, t5, t7]
type SPMDInterleaveStore struct {
    register                    // produces int (element count)
    Addr      Value             // *T destination pointer
    Values    []Value           // N value vectors to interleave
    Period    int               // K (group size / interleave stride)
    Lanes     int               // SIMD width of input vectors
    Mask      Value             // execution mask (from enclosing scope)
    Source    Value              // original slice (for bounds)
    SourceLen Value             // len(slice)
    pos       token.Pos
}
```

### Output layout

For `Values = [A, B, C]`, `Period = 4`, `Lanes = 16`:

```
Input:   A = [a0  a1  a2  a3  a4  a5  a6  a7  a8  a9  a10 a11 a12 a13 a14 a15]
         B = [b0  b1  b2  b3  b4  b5  b6  b7  b8  b9  b10 b11 b12 b13 b14 b15]
         C = [c0  c1  c2  c3  c4  c5  c6  c7  c8  c9  c10 c11 c12 c13 c14 c15]

Groups:  [0..3]  [4..7]  [8..11]  [12..15]

Diagonal extraction per group:
  group 0: A[0], B[1], C[2]
  group 1: A[4], B[5], C[6]
  group 2: A[8], B[9], C[10]
  group 3: A[12], B[13], C[14]

Output:  [a0, b1, c2, a4, b5, c6, a8, b9, c10, a12, b13, c14]  (12 bytes)
```

### Return value

Always `N * Lanes / Period` (compile-time constant). For base64: `3 * 16 / 4 = 12`.

### Tail iteration handling

The peeled loop's main body always has an all-ones execution mask (full groups). The tail iteration uses the existing CompactStore fallback — the detection pass only fires for the main body path where the mask is all-ones.

## 3. Detection Pass

Runs after `spmdDetectMuxPatterns` (which creates SPMDMux). Scans each SPMD loop for:

1. An `SPMDMux` with periodic `Indices` (period K, N distinct active values)
2. The SPMDMux result flows into a `lanes.CompactStore` call (found by walking referrers)
3. The CompactStore's mask argument matches the SPMDMux's gap pattern (every K-th lane inactive)

### Algorithm

```
for each SPMDMux in the loop body:
    K = period of Indices
    N = number of distinct active values
    if Lanes % K != 0: skip

    // Find CompactStore call consuming this Mux
    for ref in mux.Referrers():
        if ref is *ssa.Call to lanes.CompactStore:
            if ref.Args[1] == mux:   // mux is the value argument
                // Verify the mask arg matches: every K-th lane inactive
                // (the mask originates from the same i%K comparison)
                
                // Build SPMDInterleaveStore
                // Extract Addr, Source, SourceLen from the CompactStore call's dst slice arg
                // Values = mux.Values
                // Period = K
                
                // Replace: mux → dead, CompactStore call → SPMDInterleaveStore
```

### Matching the CompactStore mask

The CompactStore mask (e.g., `pos != 3`) must be consistent with the SPMDMux period. Verify by checking that the mask originates from a `BinOp{NEQ}(rem_result, Const{K-1})` or equivalent, where `rem_result` is the same REM used by the SPMDMux.

A simpler check: the SPMDMux has N active values and period K. If `N == K-1` (one gap per group), the CompactStore is removing exactly the gap lanes. Verify `N + 1 == K`.

## 4. TinyGo Lowering

`createSPMDInterleaveStore` uses N diagonal-extraction shuffles + ORs + one compaction shuffle + one store.

### Byte-width algorithm

For `Values = [A, B, C]`, `Period = 4`, `Lanes = 16`:

```
// Step 1: N swizzle operations — each extracts diagonal elements, zeros rest.
// mask_A picks lane r=0 from each group: [0, 0x80, 0x80, 0x80, 4, 0x80, ...]
// mask_B picks lane r=1 from each group: [0x80, 1, 0x80, 0x80, 0x80, 5, ...]
// mask_C picks lane r=2 from each group: [0x80, 0x80, 2, 0x80, 0x80, 0x80, 6, ...]

part_A = spmdSwizzle(A, mask_A)  // [a0, 0, 0, 0, a4, 0, 0, 0, ...]
part_B = spmdSwizzle(B, mask_B)  // [0, b1, 0, 0, 0, b5, 0, 0, ...]
part_C = spmdSwizzle(C, mask_C)  // [0, 0, c2, 0, 0, 0, c6, 0, ...]

// Step 2: OR together.
interleaved = part_A | part_B | part_C  // [a0, b1, c2, 0, a4, b5, c6, 0, ...]

// Step 3: Compaction shuffle — remove gap lanes.
compact_mask = [0, 1, 2, 4, 5, 6, 8, 9, 10, 12, 13, 14, 0x80, 0x80, 0x80, 0x80]
output = spmdSwizzle(interleaved, compact_mask)

// Step 4: Full-width store (overwrite pattern).
store(output, addr)

// Step 5: Return count = N * Lanes / K = 12.
```

**Instruction count**: N swizzles + (N-1) ORs + 1 compaction swizzle + 1 store = 3 + 2 + 1 + 1 = **7 instructions**.

All shuffle masks are compile-time constants built from `Period`, `N`, and `Lanes`.

### Building the shuffle masks

For value index `r` (0 to N-1), the diagonal extraction mask is:

```go
for lane := 0; lane < Lanes; lane++ {
    group := lane / Period
    posInGroup := lane % Period
    if posInGroup == r {
        mask[lane] = byte(group * Period + r)  // identity position for this lane
    } else {
        mask[lane] = 0x80  // zero (out-of-range for swizzle)
    }
}
```

The compaction mask is:

```go
outIdx := 0
for lane := 0; lane < Lanes; lane++ {
    posInGroup := lane % Period
    if posInGroup < N {  // active lane (not gap)
        compactMask[outIdx] = byte(lane)
        outIdx++
    }
}
for i := outIdx; i < Lanes; i++ {
    compactMask[i] = 0x80  // zero padding
}
```

### Wider types

For non-byte types, use LLVM `shufflevector` instead of `spmdSwizzle`/`pshufb`. Same algorithm — diagonal extraction + OR + compaction.

### Scalar fallback

When `!b.simdEnabled` (laneCount=1): single-element store, return 1.

## 5. Expected Impact

### SSSE3 (16 byte lanes, base64 K=4 N=3)

| Phase | Before | After | Saved |
|-------|--------|-------|-------|
| Mux select chain | 12 | 0 | 12 |
| CompactStore scatter | 99 | 0 | 99 |
| Diagonal swizzles + ORs | 0 | 5 | -5 |
| Compaction swizzle + store | 0 | 2 | -2 |
| **Net** | **111** | **7** | **104** |

Total hot loop: 207 - 104 = **~103 instructions** (was 207). Expected speedup: 207/103 ≈ 2× improvement over current → SSSE3 from ~10x to ~20x.

### AVX2 (32 byte lanes)

Similar ratio. CompactStore was 166 instructions → replaced by ~9 instructions (wider shuffles). Expected: AVX2 from ~11x to ~22x.

### WASM SIMD128

CompactStore was ~160 WAT ops → replaced by ~10 ops. Plus the mux overhead (~80 ops) eliminated. Expected: WASM from 0.94x to ~2-3x.

## 6. Files Modified

| Repository | File | Change |
|-----------|------|--------|
| x-tools-spmd | `go/ssa/ssa.go` | Add `SPMDInterleaveStore` struct |
| x-tools-spmd | `go/ssa/print.go` | Add `String()` method |
| x-tools-spmd | `go/ssa/emit.go` | Add emit helper |
| x-tools-spmd | `go/ssa/sanity.go` | Add sanity checks |
| x-tools-spmd | `go/ssa/spmd_predicate.go` | Add detection pass |
| x-tools-spmd | `go/ssa/spmd_peel.go` | Add clone case |
| x-tools-spmd | `go/ssa/func.go` | Wire detection pass |
| tinygo | `compiler/compiler.go` | Add dispatch case |
| tinygo | `compiler/spmd.go` | Add `createSPMDInterleaveStore` |
