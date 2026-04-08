# vpmaddubsw/vpmaddwd Pattern Detection Design

## Goal

Detect the multiply-add-horizontal pattern in SPMD Go code and emit `vpmaddubsw` (SSSE3/AVX2) / `vpmaddwd` (SSE2/AVX2) instructions automatically, without adding new `lanes` builtins.

## The Pattern

Natural Go code for combining adjacent elements with multiply-add:

```go
// Stage 1: pair bytes → int16 (vpmaddubsw)
packed := make([]int16, len(sextets)/2)
go for i, _ := range packed {
    packed[i] = int16(sextets[i*2])*64 + int16(sextets[i*2+1])
}

// Stage 2: pair int16s → int32 (vpmaddwd)
output := make([]int32, len(packed)/2)
go for i, _ := range output {
    output[i] = int32(packed[i*2])*4096 + int32(packed[i*2+1])
}
```

Each stage:
- Range-over-slice determines lane count and element width
- Two loads at stride-2 from a narrower-typed slice (`[i*2]` and `[i*2+1]`)
- Widen narrow type → loop element type (byte→int16, int16→int32)
- Multiply each by a compile-time constant
- Add the two products
- Store to the iteration slice

## Hardware Mapping

### vpmaddubsw (SSSE3 / AVX2)

**Signature**: `<16 x u8>` × `<16 x i8>` → `<8 x i16>` (SSE), or `<32 x u8>` × `<32 x i8>` → `<16 x i16>` (AVX2)

**Semantics**: `result[i] = a[2i] * b[2i] + a[2i+1] * b[2i+1]` (saturating i16)

**Go pattern**: `int16`-width SPMD loop, two byte loads at stride-2, widen, const-multiply, add.

**Lane counts**: SSE → 8 int16 lanes consuming 16 bytes; AVX2 → 16 int16 lanes consuming 32 bytes.

### vpmaddwd (SSE2 / AVX2)

**Signature**: `<8 x i16>` × `<8 x i16>` → `<4 x i32>` (SSE), or `<16 x i16>` × `<16 x i16>` → `<8 x i32>` (AVX2)

**Semantics**: `result[i] = a[2i] * b[2i] + a[2i+1] * b[2i+1]`

**Go pattern**: `int32`-width SPMD loop, two int16 loads at stride-2, widen, const-multiply, add.

**Lane counts**: SSE → 4 int32 lanes consuming 8 int16s; AVX2 → 8 int32 lanes consuming 16 int16s.

### WASM SIMD128

No direct `vpmaddubsw` equivalent. Workaround: `i16x8.extmul_low_i8x16_u` + `i16x8.extmul_high_i8x16_u` + `i16x8.add` (3 instructions). WASM Relaxed SIMD adds `i32x4.relaxed_dot_i8x16_i7x16_add_s` which is closer but has different semantics (4-way accumulate, not pairwise).

The pattern detection should emit native instructions on x86 and fall back to the shift/multiply/add expansion on WASM.

## Detection Strategy

The detection happens in the TinyGo compiler when processing SSA instructions inside an SPMD loop body. The pattern is:

```
SSA form:
  %idx2   = BinOp MUL %loopIter, const(2)       ; i*2
  %idx2p1 = BinOp ADD %idx2, const(1)            ; i*2+1
  %a      = Index/IndexAddr %srcSlice, %idx2      ; src[i*2]
  %b      = Index/IndexAddr %srcSlice, %idx2p1    ; src[i*2+1]
  %aw     = Convert %a to widerType               ; int16(src[i*2])
  %bw     = Convert %b to widerType               ; int16(src[i*2+1])
  %ma     = BinOp MUL %aw, const(C1)             ; * 64
  %mb     = BinOp MUL %bw, const(C2)             ; * 1
  %sum    = BinOp ADD %ma, %mb                    ; + 
  store %sum to dst[i]
```

The compiler should recognize this SSA pattern and replace the entire sequence with:
1. Load `<2N x narrowType>` from `srcSlice` at offset `i*2` (contiguous load of pairs)
2. Build constant vector `[C1, C2, C1, C2, ...]`
3. Emit `vpmaddubsw` (for byte→int16) or `vpmaddwd` (for int16→int32)
4. Store `<N x wideType>` result

### Detection location

In `tinygo/compiler/spmd.go`, the detection should happen at the `BinOp ADD` instruction when:
- Both operands are `BinOp MUL` with one constant operand
- The non-constant operands of both MULs are widened (`Convert`) from loads
- The loads are at stride-2 from the same source slice
- The source element type is half the width of the loop element type

This is a peephole optimization on the SSA instruction stream during SPMD lowering.

### Alternative: detect at the `*ssa.Store` level

When processing a contiguous store in the SPMD loop body, trace the stored value backwards through ADD → two MULs → two Converts → two stride-2 loads. If the pattern matches, emit the fused instruction.

## Scope

### In scope
- Pattern detection for `vpmaddubsw` (byte → int16 with SSSE3/AVX2)
- Pattern detection for `vpmaddwd` (int16 → int32 with SSE2/AVX2)
- Fallback to normal code generation when pattern doesn't match or target lacks the instruction
- x86-64 targets (SSE and AVX2)

### Out of scope
- WASM Relaxed SIMD mapping (keep existing `DotProductI8x16Add` for explicit use)
- ARM NEON equivalent
- Non-constant multipliers (runtime varying weights)
- More than 2 elements per group (only pairwise horizontal add)

## Base64 Decoder Impact

With pattern detection, the base64 packing becomes three `go for` loops:

```go
// Step 1: SPMD decode (existing, vpshufb)
go for i, ch := range src {
    sextets[i] = ch + decodeLUT[ch>>4]
    if ch == byte('+') { sextets[i] += 3 }
}

// Step 2: Pack pairs byte→int16 (vpmaddubsw)
packed := make([]int16, len(sextets)/2)
go for i, _ := range packed {
    packed[i] = int16(sextets[i*2])*64 + int16(sextets[i*2+1])
}

// Step 3: Pack pairs int16→int32 (vpmaddwd)
quads := make([]int32, len(packed)/2)
go for i, _ := range quads {
    quads[i] = int32(packed[i*2])*4096 + int32(packed[i*2+1])
}

// Step 4: Extract 3 bytes per int32 and compact (vpshufb + vmovdqu)
// ...
```

Steps 2 and 3 replace the current scalar packing loop. Combined with the existing vpshufb decode (step 1), this would bring the SPMD decoder close to Mula-Lemire's instruction efficiency.

## Success Criteria

- Go code with the stride-2 widen-multiply-add pattern generates `vpmaddubsw` / `vpmaddwd` on x86-64
- No new `lanes` package functions needed
- Fallback to shift/multiply/add when pattern doesn't match
- Base64 decoder can use the pattern for packing
- All existing E2E tests pass
