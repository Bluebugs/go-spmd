# Design Spec: CompactStore Codegen Optimizations

**Date**: 2026-04-09
**Status**: Draft
**Motivation**: CompactStore's constant-mask path emits per-element scalar stores (89 instructions on SSE for base64 4→3), and the runtime-mask path uses a serial branch chain. Replace both with SIMD shuffle-based compaction using target-native byte-shuffle instructions. Also fix x86 feature detection and the hardcoded lane count in the swizzle fallback.

**Depends on**: `docs/superpowers/specs/2026-04-08-compact-store-design.md`

## 1. Scope

Four optimizations, all in `tinygo/compiler/spmd.go`:

1. **Constant-mask CompactStore**: shuffle + single vector store (overwrite pattern)
2. **Runtime-mask CompactStore**: binary-tree compaction via log2(N) conditional shuffles
3. **x86 feature implication chain**: `spmdHasX86Feature("ssse3")` recognizes AVX2+
4. **`spmdSwizzleScalarFallback` lane count**: use actual vector width, not hardcoded 16

**Non-goals**: AVX-512 `vpcompressb`, RVV `vcompress.vm` (future). Bounds checking (deferred).

**Success criteria**: Base64 SSSE3 benchmark from ~10x to ~14-16x. AVX2 with just `+avx2` flag works correctly. WASM benchmark improves from 0.94x to >2x.

## 2. Constant-Mask CompactStore — Shuffle + Vector Store

Replace `createCompactStoreConst` (lines 3285-3327). New implementation:

1. Build compaction shuffle indices: active lanes packed to positions 0..activeCount-1 (same as current).
2. Apply shuffle using target's native byte-shuffle instruction:
   - **WASM**: `i8x16.swizzle` (via `spmdWasmSwizzle`)
   - **x86 SSSE3+**: `pshufb`/`vpshufb` (via `spmdX86Pshufb`)
   - **NEON**: `tbl` (future, falls back to `shufflevector`)
   - **Non-byte types or no native shuffle**: LLVM `shufflevector`
3. Store the full vector to `ptr` with a single store instruction (overwrite pattern). The caller advances the output pointer by `activeCount`, and the next iteration's store overwrites the trailing garbage.
4. Return constant `activeCount`.

For byte-width types, use the existing `spmdWasmSwizzle`/`spmdX86Pshufb` dispatch (the same path used by `lanes.Swizzle`). The indices vector is built as a compile-time constant `<N x i8>`.

### Example: base64 4→3 on 16 lanes

```
Input:  [b0,b1,b2,_,b4,b5,b6,_,b8,b9,b10,_,b12,b13,b14,_]
Shuffle mask: [0,1,2,4,5,6,8,9,10,12,13,14,0x80,0x80,0x80,0x80]
Output: [b0,b1,b2,b4,b5,b6,b8,b9,b10,b12,b13,b14,0,0,0,0]
Store:  single v128.store / movdqu (16 bytes, last 4 are garbage overwritten next iteration)
```

Instruction count: 1 shuffle + 1 store = **2 instructions** (was 89).

### Wider types (i16, i32, i64)

For non-byte types, `pshufb`/`i8x16.swizzle` doesn't apply. Use LLVM `shufflevector` with the compaction indices (same approach as current code, but followed by one vector store instead of per-element extraction).

## 3. Runtime-Mask CompactStore — Binary Tree Compaction

Replace `createCompactStoreRuntime` (lines 3329-3375). New implementation uses a binary-tree compaction in log2(N) levels, fully SIMD, no scalar extraction or branches.

### Algorithm

At each level `k` (distance = 2^k), each element conditionally shifts left based on how many inactive lanes exist in its left neighborhood.

```
Input:  [A, _, B, C, _, D, _, _]  mask: [1,0,1,1,0,1,0,0]

Level 0 (stride 1): Compute gaps = prefix-count of inactive lanes at stride 1.
  Each element: if left neighbor is inactive, shift left by 1.
  → [A, B, C, _, D, _, _, _]

Level 1 (stride 2): Compute gaps at stride 2.
  Each element: if 2 positions to the left has a gap, shift left by 2.
  → [A, B, C, D, _, _, _, _]

Level 2 (stride 4): no-op (no gaps of size 4 remain).

Done: [A, B, C, D, _, _, _, _]
```

### Per-level implementation

Each level `k` (for `k` = 0, 1, 2, ..., log2(N)-1):

1. **Count inactive lanes in the stride-2^k window to the left**: this is the prefix-sum of `!mask` at the current stride. Computed as a left-shift of the cumulative gap count by 2^k positions, then add.
2. **Build per-lane shift amount**: `shift[i]` = number of inactive lanes in positions `[i-2^k, i)`.
3. **Build shuffle indices**: `idx[i] = i + shift[i]` — each element pulls from `shift[i]` positions to its right.
4. **Blend with identity**: only apply the shuffle for lanes that are still "in flight" (not already compacted past them). Use `select(should_move, shuffled, current)`.

Actually, the simpler formulation: at each level, compute the cumulative gap count (prefix-sum of `!mask`), then:

```
// For each level k:
gap_k = shift_left_bytes(cumulative_gaps, 2^k)  // gaps from 2^k positions left
moved = cumulative_gaps - gap_k                   // how far this element has moved so far
// Each element at position i should be at position i - cumulative_gaps[i]
```

The final shuffle mask is: `idx[i] = i + cumulative_gaps[i]` — this maps each output position to its source position.

### Concrete instruction sequence (16 lanes, 4 levels)

```
// Input: val = <16 x i8>, mask = <16 x i1>

// Step 1: Convert mask to <16 x i8> gap counts (0 = active, 1 = inactive)
gaps = xor(zext(mask, <16 x i8>), splat(1))  // invert: 1 where inactive

// Step 2: Inclusive prefix-sum of gaps (parallel doubling)
g = gaps
g = g + byte_shift_left(g, 1)    // sums of 2
g = g + byte_shift_left(g, 2)    // sums of 4
g = g + byte_shift_left(g, 4)    // sums of 8
g = g + byte_shift_left(g, 8)    // sums of 16
// g[i] = number of inactive lanes in positions [0, i] (inclusive)

// Step 3: Build source indices — output position j reads from source position j + g[j]
identity = <0, 1, 2, ..., 15>
src_indices = add(identity, g)

// Step 4: Shuffle using the computed indices
compacted = swizzle(val, src_indices)    // pshufb / i8x16.swizzle / tbl

// Step 5: Store full vector (overwrite pattern)
store(compacted, ptr)

// Step 6: Popcount for return value
n = sub(splat(laneCount), extractelement(g, laneCount-1))
// Or: n = popcount(bitmask(mask))
```

Total: ~10 SIMD instructions (4 shift+add for prefix-sum, 1 xor, 1 add for indices, 1 swizzle, 1 store, 1 popcount). Was 89 branch+extract instructions on SSE.

For 32 lanes (AVX2): one more doubling step → ~12 instructions.

### Byte-shift instruction mapping

The "byte_shift_left by N bytes" operation (shifting the entire vector left by N byte positions, filling with zero):

| Target | Instruction |
|--------|-------------|
| WASM SIMD128 | `i8x16.shuffle(v, zero, [N,N+1,...,15,0,0,...])` |
| x86 SSE | `pslldq xmm, N` (shift left by N bytes) |
| x86 AVX2 | `vpslldq ymm, N` (within each 128-bit lane) + `vperm2i128` for cross-lane |
| ARM NEON | `vext.8 q, zero, q, #(16-N)` |

Note: AVX2 `vpslldq` only shifts within 128-bit lanes. For the prefix-sum to work across the full 256-bit register, the stride-16 step needs a cross-lane shift (`vperm2i128` + blend). This adds ~2 extra instructions for the stride-16 level on AVX2.

### Popcount per target

| Target | Approach |
|--------|----------|
| x86 | `pmovmskb` + scalar `popcnt` |
| WASM | `i8x16.bitmask` + scalar popcount (or extract last prefix-sum element) |
| NEON | `cnt` + horizontal add |

### Wider types (i16, i32, i64)

The prefix-sum approach works identically — just use word/dword shifts instead of byte shifts, and fewer doubling levels (e.g., 2 levels for 4-lane i32). The shuffle uses `shufflevector` instead of `pshufb`.

## 4. x86 Feature Implication Chain

Replace `spmdHasSSSE3()` with `spmdHasX86Feature(name string) bool`.

The x86 SIMD feature hierarchy:

```
sse2 → sse3 → ssse3 → sse4.1 → sse4.2 → avx → avx2 → avx512f
```

Implementation: an ordered slice of feature names. For a query feature, check if any feature at or above it in the hierarchy is present in `c.Features`.

```go
var x86FeatureChain = []string{
    "sse2", "sse3", "ssse3", "sse4.1", "sse4.2", "avx", "avx2", "avx512f",
}

func (c *compilerContext) spmdHasX86Feature(name string) bool {
    if !c.spmdIsX86() { return false }
    // Find the position of the requested feature.
    reqIdx := -1
    for i, f := range x86FeatureChain {
        if f == name { reqIdx = i; break }
    }
    if reqIdx < 0 { return false }
    // Any feature at reqIdx or above implies the requested feature.
    for i := reqIdx; i < len(x86FeatureChain); i++ {
        if strings.Contains(c.Features, "+"+x86FeatureChain[i]) { return true }
    }
    return false
}
```

Update callers:
- `spmdHasSSSE3()` → `spmdHasX86Feature("ssse3")`
- Any future x86 feature checks

## 5. `spmdSwizzleScalarFallback` Lane Count Fix

Current code (line 2418) hardcodes `v16i8` and loop bound of 16:

```go
result := llvm.ConstNull(v16i8)
for i := 0; i < 16; i++ { ... }
```

Fix: read lane count from `table.Type().VectorSize()`, use that for result type and loop:

```go
laneCount := table.Type().VectorSize()
resultType := llvm.VectorType(i8Type, laneCount)
result := llvm.ConstNull(resultType)
for i := 0; i < laneCount; i++ { ... }
```

The threshold check `>= 16` for the zero-fill of out-of-range indices should use `>= laneCount`.

## 6. Documentation Update

Update `go/src/lanes/lanes.go` CompactStore doc comment and `docs/superpowers/specs/2026-04-08-compact-store-design.md` to add:

> The underlying store may write up to `lanes.Count[T]()` elements to the destination memory, even though only `n` elements contain valid data. The caller must ensure the destination slice's backing array has at least `lanes.Count[T]()` elements of accessible memory beyond the current offset. In practice, allocate `output_len + lanes.Count[T]()` and advance by the returned `n`. Trailing bytes are overwritten by subsequent calls.

## 7. Files Modified

| File | Change |
|------|--------|
| `tinygo/compiler/spmd.go` | Replace `createCompactStoreConst`, `createCompactStoreRuntime`, add `spmdHasX86Feature`, fix `spmdSwizzleScalarFallback` |
| `go/src/lanes/lanes.go` | Update CompactStore doc comment (overwrite semantics) |
| `docs/superpowers/specs/2026-04-08-compact-store-design.md` | Add overwrite documentation |
