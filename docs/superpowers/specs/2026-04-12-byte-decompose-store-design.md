# Design Spec: Byte-Decomposition Store Optimization

**Date**: 2026-04-12
**Status**: Draft
**Motivation**: Loop 4 of the base64 v2 decoder extracts bytes from `Varying[int32]` via constant shifts and stores them at stride-3 offsets. This generates 18-24 instructions (shift/unpack/shuffle tree). With byte-decomposition detection, it becomes a bitcast + one pshufb + one store = 2-3 instructions.

## 1. Pattern

```go
go for g := range packed {   // packed is []int32, 4 lanes SSE / 8 lanes AVX2
    dst[g*3+0] = byte(packed[g] >> 16)
    dst[g*3+1] = byte(packed[g] >> 8)
    dst[g*3+2] = byte(packed[g])
}
```

After predication, this becomes S=3 interleaved `SPMDStore` instructions with stride 3:
```
SPMDStore(IndexAddr(dst, g*3+0), Trunc(Lshr(packed[g], 16), byte), mask)
SPMDStore(IndexAddr(dst, g*3+1), Trunc(Lshr(packed[g], 8), byte), mask)
SPMDStore(IndexAddr(dst, g*3+2), Trunc(packed[g], byte), mask)
```

All three stores extract bytes from the same `Varying[int32]` source via constant right-shifts that are multiples of 8.

## 2. Detection

Extend TinyGo's existing interleaved store analysis. When a stride-S store group is detected and ALL S stored values match:

```
value_r = Trunc(Lshr(source, shift_r), byte)   // shift_r is multiple of 8
   OR
value_r = Trunc(source, byte)                    // shift_r = 0
```

Where `source` is the same `Varying[int32/int16/int64]` for all S stores, recognize this as a **byte-decomposition** pattern.

### Conditions
- All S values extract from the same source vector
- All shifts are constant multiples of 8
- The shifts correspond to valid byte positions within the source element (0 to W*8-8 where W = element byte width)
- S <= W (can't extract more bytes than the element contains)

## 3. Lowering

Instead of S separate stores:

1. **Bitcast** source from `<N x iW>` to `<N*W x i8>` — LLVM bitcast, zero instructions at machine level

2. **Build constant shuffle mask**: For each source element `g` (0 to N-1), the output needs bytes at positions `[shift_0/8, shift_1/8, ..., shift_{S-1}/8]` from the element's W bytes at offsets `[g*W, g*W+1, ..., g*W+W-1]`. The shuffle index for output position `g*S+r` is `g*W + shift_r/8`.

   For base64 (N=4, W=4, S=3, shifts=[16,8,0]):
   ```
   mask = [2,1,0, 6,5,4, 10,9,8, 14,13,12, 0x80,0x80,0x80,0x80]
   ```

3. **Apply shuffle** via `spmdSwizzle(bitcasted, mask)` — handles pshufb (SSE), vpshufb (AVX2 with lane-crossing fix), i8x16.swizzle (WASM)

4. **Contiguous store** (overwrite pattern) — single `vmovdqu` / `v128.store`

5. **Return count** = N * S (compile-time constant for the interleaved store group)

### AVX2 lane-crossing

For AVX2 (`<8 x i32>` → `<32 x i8>`), the shuffle produces 24 valid bytes but they're split across two 128-bit lanes (12 in each half). The existing InterleaveStore AVX2 fix (per-half shuffle + `shufflevector` merge) applies here. Alternatively, use `vpshufb` + `vpermd` like simdutf.

## 4. Where in the Code

**File**: `tinygo/compiler/spmd.go`

The existing `spmdAnalyzeInterleavedStores` (around line 2038) detects stride-S store groups. Extend it (or add a companion) to check for the byte-decomposition pattern on the stored values.

The lowering happens in `spmdEmitInterleavedStoreMasked` (or a new variant) which currently emits S interleaved stores. Add a fast path: when byte-decomposition is detected, emit bitcast + shuffle + store instead.

## 5. Generality

This optimization applies to any code that decomposes wider SIMD values to bytes:
- Base64 packing (int32 → 3 bytes)
- UTF-8 encoding (int32 → 1-4 bytes)
- Compression codecs (int16/int32 → variable bytes)
- Network protocol packing

No new `lanes.*` functions needed — pure compiler pattern detection.

## 6. Expected Impact

| Phase | Before | After |
|-------|--------|-------|
| Loop 4 (SSSE3) | 18 instrs | 2 instrs (pshufb + movdqu) |
| Loop 4 (AVX2) | 24 instrs | 3 instrs (vpshufb + vpermd + vmovdqu) |
| Loop 4 (WASM) | ~15 instrs | 2 instrs (swizzle + store) |

Total per-chunk: SSSE3 34→18, AVX2 44→23, approaching simdutf's 15.
Expected throughput: AVX2 from 6509 to ~12000+ MB/s.
