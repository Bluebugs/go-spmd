# FromConstrained Mask Issue: `[]Varying[bool]` on WASM

## Status
**Unresolved** — masks returned by `lanes.FromConstrained` cannot be used on WASM targets.
Value decomposition works correctly. This document exists so we can revisit the problem later.

## Problem Summary

`lanes.FromConstrained` returns `([]Varying[T], []Varying[bool])`. The value slice works
correctly, but accessing the mask slice causes LLVM errors on WASM because `Varying[bool]`
maps to `<16 x i1>` which WASM cannot load/store from memory.

## Detailed Analysis

### The type system view

On a 128-bit SIMD platform:
- `Varying[int32]` = `<4 x i32>` (128 / 32 = 4 lanes)
- `Varying[bool]` = `<16 x i1>` (128 / 1 = 16 lanes)

This is correct from the developer's perspective: a `Varying[bool]` should have 16
independently varying boolean values on a 128-bit platform.

### The internal mask representation

Internally, SPMD execution masks use `<4 x i32>` on WASM (via `spmdMaskElemType()`).
This is an i32-per-lane format where `-1` = active and `0` = inactive. This format:
- Avoids expensive sign-extension (`shl 31` + `ashr 31`) on every mask operation
- Maps directly to WASM `v128.any_true` / `i32x4.all_true` intrinsics
- Is the standard approach (ISPC uses similar i32 masks internally)

### The mismatch

`createFromConstrained` builds mask arrays using `<4 x i32>` (the internal format),
but `[]Varying[bool]` in the Go type system maps to a slice of `<16 x i1>`. When the
caller loads `masks[0]`, the LLVM type system expects `<16 x i1>` but the memory contains
`<4 x i32>` — causing "Do not know how to promote this operator's operand!" at LLVM
legalization.

### Why not just change `Varying[bool]`?

An attempted fix changed `makeLLVMType` to map `Varying[bool]` to `<4 x i32>` on WASM.
This was rejected because it leaks platform internals into the type system:

- `Varying[bool]` should consistently have `128 / sizeof(bool)` = 16 lanes
- The number of lanes in a type should depend only on the element size and SIMD width
- Internal mask format is an optimization detail, not a semantic property

### WASM `<N x i1>` limitation

WASM SIMD128 has no native support for sub-byte vector element types. Vectors of `i1`
(`<4 x i1>`, `<16 x i1>`, etc.) cannot be:
- Stored to memory (`llvm.masked.store` of `<N x i1>`)
- Loaded from memory (`llvm.masked.load` of `<N x i1>`)
- Used as array elements

LLVM's WASM backend does not implement legalization for these types in memory operations.

## Bugs Found and Fixed

### 1. constraintN type erasure (FIXED)

When constrained `Varying[T, N]` passes through unconstrained `Varying[T]` parameters
(enabled by Commit 1's type relaxation), the Go type loses its constraint info
(constraint becomes -1). But the LLVM vector retains its original width.

**Fix**: Derive `constraintN` from `max(spmdEffectiveLaneCount, vec.Type().VectorSize())`.

### 2. `[]Varying[bool]` mask slice on WASM (UNRESOLVED)

See detailed analysis above. The E2E test works around this by ignoring the mask return:
```go
values, masks := lanes.FromConstrained(data)
_ = masks
```

## Potential Future Approaches

### Approach A: Bitcast at load/store boundaries

When loading a `Varying[bool]` from memory, insert a conversion sequence:
1. Load as `<4 x i32>` (the stored format)
2. Bitcast/convert to `<16 x i1>` (the Go type format)

And the reverse for stores. This keeps the type system clean but adds conversion overhead
at every mask access.

### Approach B: Opaque mask type

Introduce a separate `Varying[mask]` or `lanes.Mask` type that:
- Has the same lane count as the associated data type (4 for int32 context)
- Maps to `<4 x i32>` internally
- Is not `Varying[bool]` (avoids the 16-lane expectation)

This requires API changes to `FromConstrained`'s return type.

### Approach C: Platform-aware mask packing

Store masks in a packed format that WASM can handle (e.g., a single `i32` bitmask),
and unpack at access time. This is efficient but complex.

### Approach D: Change `FromConstrained` return type

Instead of `[]Varying[bool]`, return `[]Varying[uint32]` or `[]int` (bitmasks).
This avoids the `<N x i1>` problem entirely but changes the API.

## Current Workaround

For now, `FromConstrained` value decomposition works. Users who need masks can:
1. Use `reduce.Any(masks[i])` — **blocked** (same `<N x i1>` issue)
2. Compute masks manually based on group index and total element count
3. Avoid `FromConstrained` entirely and use direct SPMD loops with `go for`

The E2E test validates value decomposition only, with `_ = masks`.
