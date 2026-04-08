# Fix spmdSwizzleWithTable for AVX2 32-wide byte lanes

## Problem

`spmdSwizzleWithTable` hardcodes swizzle width to 16 lanes (line 6755):
```go
idxVec := b.spmdSwizzlePrepareIndex(index, laneCount, 16)  // always 16
```

On AVX2 with `Varying[byte]` (32 lanes), the table is only 16 bytes in a 128-bit register. Lanes 16-31 index into uninitialized memory, producing wrong results. This blocks `[16]byte` nibble-LUT indexing (base64, hex-encode, IPv4) from working correctly on AVX2.

The sibling function `spmdWasmSwizzle` already handles this correctly by computing dynamic `swizzleWidth` and duplicating the table for AVX2 vpshufb (which shuffles each 128-bit half independently).

## Fix

Apply the same AVX2-aware logic from `spmdWasmSwizzle` to `spmdSwizzleWithTable` and its callers (`spmdSwizzleArrayBytes`, `spmdSwizzleFromPtr`):

1. Compute `swizzleWidth`: 16 on WASM, `laneCount` on x86 when `laneCount > 16`
2. Duplicate the 16-byte table to fill both halves of the 256-bit register
3. Use `spmdSwizzlePrepareIndex(index, laneCount, swizzleWidth)` with correct width
4. Return full-width result without per-lane extraction when `laneCount == swizzleWidth`

## Affected functions

- `spmdSwizzleWithTable` (`tinygo/compiler/spmd.go:6750`)
- `spmdSwizzleArrayBytes` (`tinygo/compiler/spmd.go:6512`) — constructs `v16i8` table
- `spmdSwizzleFromPtr` (`tinygo/compiler/spmd.go:6533`) — loads table as `v16i8`

## Follow-up

After the compiler fix, revert `decodeLUT` in `test/integration/spmd/base64-mula-lemire/main.go` from `[]byte` slice back to `[16]byte` array, and fix the padding modulus from hardcoded 16 to lane-count-aware.

## Success criteria

- `[16]byte` array indexed by `Varying[byte]` on AVX2 32-wide generates `vpshufb` (not per-lane gather)
- All existing E2E tests pass (hex-encode uses the same fast path)
- Base64 nibble-LUT test passes with `[16]byte` decodeLUT on WASM, SSE, and AVX2
