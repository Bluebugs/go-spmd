# Design Spec: `lanes.CompactStore` — SIMD Compress-Store Builtin

**Date**: 2026-04-08
**Status**: Draft
**Motivation**: Enable vectorized 4→3 byte packing (Mula-Lemire base64 decoding) and general-purpose SIMD stream compaction in Go SPMD.

## 1. API Surface

```go
// Package lanes

// CompactStore writes the active lanes of v contiguously to dst,
// where active means both the explicit mask lane is true AND the
// current execution mask lane is active. Returns the number of
// elements written.
func CompactStore[T any](dst []T, v Varying[T], mask Varying[bool]) int
```

### Type Checking Rules

- `dst`: slice of T, same element type as the Varying
- `v`: `Varying[T]`
- `mask`: `Varying[bool]`
- Return type: uniform `int`
- Valid in any context where varying values exist (go for body, SPMD function body, or any scope holding varying values) — no SPMD loop context requirement
- T constrained to the same numeric types as other lanes builtins: int8/16/32/64, uint8/16/32/64, float32/64, byte

### Semantics

- Effective mask = `mask AND execution_mask`
- Active lanes are stored left-packed into `dst[0], dst[1], ...dst[n-1]`
- Lane ordering is preserved (lane 0 before lane 3 if both active)
- Returns `n = popcount(effective_mask)`
- If no lanes are active, writes nothing, returns 0
- Panics if `len(dst) < n` (same as slice out-of-bounds)

## 2. SSA Representation

New instruction in x-tools-spmd `go/ssa/ssa.go`, alongside `SPMDStore`:

```go
// SPMDCompactStore stores active lanes of Val contiguously into Addr.
// Effective mask = ExplicitMask AND enclosing execution mask.
// Produces a uniform int: the number of elements written (popcount of effective mask).
type SPMDCompactStore struct {
    register
    Addr         Value    // *T (pointer extracted from slice)
    Val          Value    // Varying[T]
    ExplicitMask Value    // Varying[bool] (user-provided mask)
    Lanes        int      // SIMD width
    Source       Value    // original slice (for bounds check)
    SourceLen    Value    // len(slice) (for bounds check)
}
```

### Differences from SPMDStore

- **Produces a value** (`register` embedding, not effect-only) — the popcount result
- **Has `SourceLen`** — needed for bounds checking since the number of elements written depends on the mask
- **No `Contiguous` flag** — always a compaction pattern
- **`ExplicitMask`** naming — clarifies this is the user-provided mask, distinct from the execution mask which gets AND'd in during lowering

### Mask Representation

The `ExplicitMask` uses the existing mask representation: `Varying[bool]` at the Go level, lowered to `<N x i1>` (WASM) or `<N x i32>` (x86) following the same conventions as SPMDStore/SPMDLoad masks. No new mask type is introduced.

### SSA Predication

The predication pass (`predicateSPMDScope`) handles `SPMDCompactStore` like `SPMDStore`: the execution mask is AND'd into `ExplicitMask` when the instruction appears under varying control flow.

## 3. TinyGo LLVM Lowering

Two codegen paths based on mask constness.

### Constant Mask Path

When the compiler can prove the mask is statically known (e.g., `[true,true,true,false]` repeating):

1. **Shuffle**: `vpshufb`/`i8x16.swizzle`/`tbl` with hardcoded compaction indices — removes gap lanes, packs active lanes contiguously
2. **Store**: narrowed contiguous store of exactly `n` bytes (e.g., 12 bytes from 16)
3. **Return**: `n` as an immediate constant

No popcount, no runtime mask analysis. Shuffle indices and output size are baked into the IR.

### Runtime Mask Path

When the mask is not statically known:

1. **Effective mask**: `AND(explicit_mask, execution_mask)`
2. **Compute shuffle indices**: prefix-sum of popcount per lane — each active lane gets its output position
3. **Shuffle**: permute the value vector using the computed indices
4. **Store**: write `popcount(mask)` elements via target-specific strategy (see below)
5. **Return**: `popcount(effective_mask)`

### Target-Specific Codegen

| ISA | Constant Mask | Runtime Mask |
|-----|--------------|--------------|
| AVX-512 | `vpcompressb`/`vpcompressd` (native) | `vpcompressb`/`vpcompressd` (native) + `popcnt` |
| RVV | `vcompress.vm` (native) | `vcompress.vm` (native) + `vcpop.m` |
| AVX2 | constant `vpshufb` + `vpermd` + narrowed store | prefix-sum → `vpshufb`/`vpermd` + store + `popcnt` |
| SSE | constant `pshufb` + narrowed store | prefix-sum → `pshufb` + store + `popcnt` |
| WASM SIMD128 | constant `i8x16.swizzle` + narrowed store | prefix-sum → `i8x16.swizzle` + store + `popcnt` via `i8x16.bitmask` |
| ARM NEON | constant `tbl` + narrowed store | prefix-sum → `tbl` + store + `popcnt` via `cnt` + horizontal add |

### Prefix-Sum Index Computation (Runtime Mask)

For targets without native compress (AVX2, SSE, WASM, NEON), compute output positions via exclusive prefix-sum of the mask:

```
mask:     [1, 0, 1, 1, 0, 1, 0, 0, 1, 1, 0, 1, 0, 0, 1, 1]
prefix:   [0, 1, 1, 2, 3, 3, 4, 4, 4, 5, 6, 6, 7, 7, 7, 8]
```

Active lane `i` writes to `dst[prefix[i]]`. Computed in ~6 SIMD instructions using parallel prefix-sum (shift-and-add at doubling distances: 1, 2, 4, 8).

For wider types (e.g., `Varying[int32]` on AVX2 = 8 lanes), the prefix-sum is cheaper — fewer lanes and wider shuffle granularity (`vpermd` instead of `vpshufb`).

### Popcount Per Target

| ISA | Popcount Instruction | Cost |
|-----|---------------------|------|
| AVX-512 | `kmov` + `popcnt` | 2 instructions |
| x86 (SSE/AVX2) | mask extract + `popcnt` | 2 instructions |
| WASM | `i8x16.bitmask` + host `popcnt` | 2 instructions |
| ARM NEON | `cnt` + horizontal add | 2-3 instructions |
| RVV | `vcpop.m` | 1 instruction |

When the return value is unused and CompactStore is inlined (always — it's a compiler intrinsic), DCE eliminates the popcount entirely.

## 4. Bounds Checking

CompactStore writes a variable number of elements depending on the mask.

- **Constant mask**: count known at compile time. Bounds check simplifies to `len(dst) >= N` where N is constant. Hoistable out of loops by standard LLVM optimizations.
- **Runtime mask**: emit `popcount(effective_mask)`, compare against `len(dst)`, branch to panic if insufficient. One compare + one conditional branch per CompactStore.
- **Elision**: when the compiler can prove the slice is large enough (e.g., caller pre-allocated sufficient space and the loop processes a known count), LLVM's standard redundant bounds check elimination removes the check. No special elision logic needed.

## 4b. Overwrite Semantics

The underlying store writes a full SIMD-width vector to memory, even though
only `n` elements contain valid data. Trailing bytes beyond position `n` are
garbage that will be overwritten by the next CompactStore call. This matches
the standard SIMD string-processing pattern (Mula-Lemire, simdutf).

The caller must ensure `dst`'s backing memory has at least `lanes.Count[T]()`
elements of accessible space beyond the current write offset. In practice:

```go
// Allocate with SIMD-width slack
dst := make([]byte, outputLen + lanes.Count[byte](v))
```

The slice bounds check validates `len(dst) >= popcount(mask)`, but the physical
store writes up to `lanes.Count[T]()` bytes starting at `&dst[0]`.

## 5. Example: Base64 4→3 Packing

The motivating use case — Mula-Lemire style base64 decoding with vectorized lookup AND packing:

```go
func decodeBase64SPMD(dst, src []byte) int {
    offset := 0
    go for i, ch := range src {
        // Step 1: ASCII → 6-bit sextet (nibble LUT via swizzle)
        s := ch + decodeLUT[ch>>4]
        if ch == byte('+') {
            s += 3
        }

        // Step 2: Pack 4→3 using cross-lane access + CompactStore
        next := lanes.Rotate(s, -1)
        pos := i % 4

        var out byte
        if pos == 0 {
            out = (s << 2) | (next >> 4)
        } else if pos == 1 {
            out = (s << 4) | (next >> 2)
        } else if pos == 2 {
            out = (s << 6) | next
        }

        n := lanes.CompactStore(dst[offset:], out, pos != 3)
        offset += n
    }
    return offset
}
```

### What the Compiler Sees (16-lane WASM)

- `pos` = `[0,1,2,3,0,1,2,3,0,1,2,3,0,1,2,3]` — derived from `i % 4` on composite index
- Three varying ifs produce `out` via SPMDSelect chains
- `pos != 3` = `[T,T,T,F,T,T,T,F,T,T,T,F,T,T,T,F]` — compile-time constant mask
- CompactStore emits constant `i8x16.swizzle` with indices `[0,1,2,4,5,6,8,9,10,12,13,14,_,_,_,_]` + 12-byte store
- `n` = 12 (constant), `offset += 12` per iteration

### Edge Case: Tail Iteration

The `go for` tail iteration has an execution mask masking off inactive lanes. The effective mask = `(pos != 3) AND execution_mask`. CompactStore automatically writes fewer bytes for the last chunk. `n` reflects the actual count.

## 6. Impact on Existing Infrastructure

### Files Modified

| Repository | File | Change |
|-----------|------|--------|
| go | `src/lanes/lanes.go` | Add `CompactStore` function declaration (panic body) |
| go | `src/go/types/call_ext_spmd.go` | Type-checking: validate args, set return type to uniform `int` |
| x-tools-spmd | `go/ssa/ssa.go` | Add `SPMDCompactStore` instruction type with `register` embedding |
| x-tools-spmd | `go/ssa/spmd_predicate.go` | Handle `SPMDCompactStore` in predication — AND execution mask into ExplicitMask |
| x-tools-spmd | `go/ssa/emit.go` (or equivalent) | Emit `SPMDCompactStore` when encountering `lanes.CompactStore` call |
| tinygo | `compiler/spmd.go` | Add `createSPMDCompactStore` — constant-mask and runtime-mask lowering |
| tinygo | `compiler/symbol.go` | Register `lanes.CompactStore` in builtin interception table |

### No Changes To

SPMDStore, SPMDLoad, SPMDStore merge, interleaved store analysis, loop peeling, existing cross-lane ops. CompactStore is purely additive.

## 7. Background: Mula-Lemire Base64 Decoding

**Paper**: Muła & Lemire, "Faster Base64 Encoding and Decoding Using AVX2 Instructions", ACM TWEB 2018. [arXiv:1704.00605](https://arxiv.org/abs/1704.00605).

**Deployed in**: simdutf library → Node.js, Bun, Chromium, WebKit, Cloudflare workerd, Ladybird, GraalVM.

**The 4→3 packing problem**: base64 decoding takes 4 encoded bytes (each containing 6 useful bits) and packs them into 3 decoded bytes. On x86 AVX2, Mula-Lemire use `vpmaddubsw` + `vpmaddwd` (2 multiply-add instructions) to merge the bits, then `vpshufb` to compact and reorder. On other ISAs (WASM, NEON, SSE), the packing uses shift/OR chains (~6-9 instructions) since the multiply-add instructions are x86-specific.

**CompactStore enables**: expressing the full Mula-Lemire decoder in portable Go SPMD — a single source that compiles to efficient SIMD on all targets, using `lanes.Rotate` for neighbor access, varying ifs for per-lane shift formulas, and `lanes.CompactStore` for the 4→3 compaction.
