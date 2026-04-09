# Remove lanes.DotProductI8x16Add Builtin

## Goal

Remove the `lanes.DotProductI8x16Add` builtin and replace its only usage (ipv4-parser) with natural Go stride-2 patterns that the compiler auto-detects as `pmaddubsw` + `pmaddwd`.

## Rationale

The pmaddubsw/pmaddwd pattern detection now works on both x86 and WASM. Natural Go code `int16(src[i*2])*C + int16(src[i*2+1])` is automatically optimized. The explicit builtin is redundant.

## Changes

### 1. Replace DotProductI8x16Add in ipv4-parser

**File**: `test/integration/spmd/ipv4-parser/main.go`

Before (lines 401-404):
```go
weights1 := [16]byte{50, 10, 1, 0, 50, 10, 1, 0, 50, 10, 1, 0, 50, 10, 1, 0}
weights2 := [16]byte{50, 0, 0, 0, 50, 0, 0, 0, 50, 0, 0, 0, 50, 0, 0, 0}
partial := lanes.DotProductI8x16Add(shuffled, weights1, [4]int{})
intValues := lanes.DotProductI8x16Add(shuffled, weights2, partial)
```

After:
```go
// Stage 1: pair bytes → int16 (auto-detected as pmaddubsw)
// shuffled = [h0,t0,o0,0, h1,t1,o1,0, ...] (4 fields × 4 bytes)
// weights  = [100,10, 1,0, ...] → pmaddubsw gives [100h+10t, o+0, ...]
packed := make([]int16, 8)
go for i, _ := range packed {
    packed[i] = int16(shuffled[i*2])*100 + int16(shuffled[i*2+1])*10
}
// packed = [100h+10t, o, 100h+10t, o, ...] for each of 4 fields

// Stage 2: pair int16s → int32 (auto-detected as pmaddwd)
// [1,1,...] weights → simple pair add: (100h+10t) + o = 100h+10t+o
var intValues [4]int32
go for i, _ := range intValues {
    intValues[i] = int32(packed[i*2]) + int32(packed[i*2+1])
}
```

Weight 100 is safe: 100 ≤ 127 (signed i8), pair sum 100+10=110 ≤ 128 (no saturation). No decomposition needed.

The `import "lanes"` stays (used for `lanes.Count` and swizzle elsewhere). Remove `DotProductI8x16Add` from the import usage.

### 2. Remove DotProductI8x16Add from lanes package

**File**: `go/src/lanes/lanes.go`

Remove the `DotProductI8x16Add` function declaration and its doc comment.

### 3. Remove compiler interception

**File**: `tinygo/compiler/spmd.go`

Remove:
- `case name == "lanes.DotProductI8x16Add":` in `createLanesBuiltin` (SIMD path)
- `case name == "lanes.DotProductI8x16Add":` in the scalar fallback path
- Any helper functions only used by DotProductI8x16Add (if they become dead code)

Keep `spmdX86Pmaddubsw`, `spmdX86Pmaddwd`, and WASM emission functions — they're now used by the pattern detection.

### 4. Remove compiler tests

**File**: `tinygo/compiler/spmd_llvm_test.go`

Remove any `TestSPMD*DotProduct*` tests that test the builtin interception.

## Success Criteria

- ipv4-parser produces identical output for all 10 test cases
- ipv4-parser E2E passes on WASM, SSE, and AVX2
- No references to `DotProductI8x16Add` in `go/src/lanes/` or `tinygo/compiler/`
- `pmaddubsw` and `pmaddwd` appear in ipv4-parser AVX2 disassembly
- All E2E tests pass
