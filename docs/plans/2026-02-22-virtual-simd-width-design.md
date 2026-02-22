# Design: Virtual SIMD Register Width

**Date**: 2026-02-22
**Status**: Approved
**Scope**: Configurable virtual SIMD width for cross-platform validation
**Phase**: 2.9e (TinyGo Backend)

## Motivation

The SPMD PoC currently hardcodes 128-bit SIMD width (WASM SIMD128). To validate that Go SPMD code generation is correct and portable across different hardware SIMD widths (SSE=128, AVX2=256, AVX-512=512), we need the ability to generate code *as if* the hardware had wider registers, while still targeting WASM SIMD128 as the underlying execution platform.

This is a **correctness validation tool**, not a performance feature. It ensures the entire SPMD pipeline — from type checker lane count computation through LLVM IR generation — produces correct results at any SIMD width.

### Key Requirements

1. Full-stack: virtual width affects type checker lane counts AND backend code generation
2. Zero overhead when not used: native path must be identical to current behavior
3. Any power of 2 from 32 to 512 bits
4. Output must be identical regardless of virtual width (correctness invariant)

## Design

### Flag and Propagation

```
-simd-width=native    (default — backend reports its native width, e.g. 128 for WASM)
-simd-width=32        (virtual 32-bit, fewer lanes than native)
-simd-width=64        (virtual 64-bit, fewer lanes than native on WASM)
-simd-width=128       (explicit native on WASM — same as native)
-simd-width=256       (virtual 256-bit, decomposed to 2x native ops)
-simd-width=512       (virtual 512-bit, decomposed to 4x native ops)
```

**Propagation path**:

1. TinyGo CLI parses `-simd-width=N|native` flag
2. Stored in `compileopts.Options.SIMDWidth` (0 = native, N = explicit)
3. TinyGo sets `SPMD_WIDTH` environment variable before invoking Go type checker
4. `internal/buildcfg` reads `SPMD_WIDTH` into `buildcfg.SPMDWidth` global (0 = native = 128 on WASM)
5. Both `go/types` and `types2` read `buildcfg.SPMDWidth` in `laneCountForType()`
6. TinyGo backend reads `compileopts.Config` for decomposition decisions

**Backend API**: The backend exposes a `NativeSIMDWidth() int` method — WASM returns 128. When `native` is specified (or `-simd-width` is omitted), this value is used directly. This abstraction allows future backends (x86, ARM) to report their own native widths.

### Type Checker Changes

The type checker's hardcoded `simd128CapacityBytes = 16` constant becomes configurable:

```go
// Before:
const simd128CapacityBytes = 16

// After:
func spmdCapacityBytes() int64 {
    if buildcfg.SPMDWidth > 0 {
        return int64(buildcfg.SPMDWidth / 8) // bits to bytes
    }
    return 16 // default: 128-bit / 8 = 16 bytes
}

func (check *Checker) laneCountForType(elem Type) int64 {
    elemSize := check.getTypeSize(elem)
    lc := spmdCapacityBytes() / elemSize
    ...
}
```

**Lane count examples** (int32, 4 bytes per element):

| Virtual Width | Capacity Bytes | Lane Count (int32) | Lane Count (int8) |
|--------------|----------------|--------------------|--------------------|
| 32-bit       | 4              | 1                  | 4                  |
| 64-bit       | 8              | 2                  | 8                  |
| 128-bit      | 16             | 4                  | 16                 |
| 256-bit      | 32             | 8                  | 32                 |
| 512-bit      | 64             | 16                 | 64                 |

- `lanes.Count[T]()` returns the virtual lane count
- `LaneCount` on `RangeStmt` reflects virtual width
- All type checking rules (return/break restrictions, varying propagation) work unchanged — they don't depend on lane count value
- Both type checkers (`go/types` and `types2`) read the same `buildcfg.SPMDWidth` global

### Backend Decomposition Strategy

When virtual width > native width, the backend decomposes wider vectors into multiple native-width operations.

**Multiplier calculation**:
```
multiplier = max(1, virtualWidth / nativeWidth)
nativeLaneCount = nativeWidth / (elemSize * 8)
virtualLaneCount = virtualWidth / (elemSize * 8)
numNativeVectors = multiplier  (for widths > native)
                 = 1           (for widths <= native)
```

**For `-simd-width=256` on WASM (native=128)**:

A `Varying[int32]` at 256-bit = 8 lanes, stored as **two** `<4 x i32>` LLVM vectors (lo/hi halves):

- **Arithmetic**: `add(a_256, b_256)` → `add(a_lo, b_lo)` + `add(a_hi, b_hi)`
- **Masks**: 256-bit mask = two 128-bit masks. AND/OR/NOT applied to both halves.
- **Memory ops**: Contiguous load of 8 elements → two 4-element masked loads. Gather/scatter similarly split.
- **Cross-lane ops**: `lanes.Broadcast` across 8 lanes extracts from the correct half, then splats to both. `RotateWithin` works within native-width chunks naturally.
- **Reduce ops**: `reduce.Add` → reduce each half, then add the two scalars.
- **`go for` loop**: Iterates by virtual lane count (8), tail mask spans both halves.

**For widths below native** (32, 64 on WASM128):

No decomposition needed. The backend generates narrower LLVM vectors (e.g., `<1 x i32>` for 32-bit, `<2 x i32>` for 64-bit). LLVM handles these natively within 128-bit WASM registers — just fewer active lanes.

**Zero-overhead guarantee**: When virtual == native, the multiplier is 1. No splitting occurs, all code paths are identical to current behavior. The "split" is a single vector with no overhead.

### Width Validation

**Valid values**: 32, 64, 128, 256, 512 — any power of 2 from 32 to 512 bits.

**Constraints**:
- Must be a power of 2
- Minimum: 32 bits (ensures at least 1 lane for int32)
- Maximum: 512 bits (matches AVX-512; beyond adds complexity without validation value)
- `native` keyword queries backend for its native width

**Degenerate cases**:
- 32-bit with `int32` = 1 lane → scalar-like behavior (useful correctness baseline)
- 512-bit with `int8` = 64 lanes → maximum decomposition (stress test)

### Testing Strategy

**Width matrix testing**: Run every E2E test at each virtual width: 32, 64, 128 (native), 256, 512. All must produce identical output.

**Test script extension**: Extend `test/e2e/spmd-e2e-test.sh` with a `--simd-width=N` parameter. A new `spmd-width-matrix.sh` script runs the full suite across all widths.

**LLVM IR verification**: For width > native, verify generated IR contains expected number of native-width vector operations (e.g., 256-bit produces pairs of 128-bit ops).

**WAT verification**: Inspect WASM output to confirm:
- Width <= native: fewer or equal `v128.*` instructions
- Width > native: more `v128.*` instructions (2x for 256, 4x for 512)

**Regression guard**: CI runs at `native` width only (no speed impact). Width matrix is an explicit validation step run during Phase 3.

## Scope

### In Scope

- `-simd-width=N|native` flag on TinyGo CLI
- `buildcfg.SPMDWidth` global propagated to both type checkers
- Lane count computation parameterized by virtual width
- Backend vector decomposition for widths > native (lo/hi splitting)
- Backend narrower vectors for widths < native
- All existing E2E tests must pass at every supported width
- Cross-width correctness: output must be identical regardless of width

### Non-Goals

- Performance optimization for virtual widths (correctness tool only)
- Actual AVX2/AVX-512 backend targets (still WASM only)
- Runtime width detection or dynamic dispatch between widths
- Streaming/pipeline execution for very wide virtual registers
- Interaction with scalar fallback mode (`-simd=false`, separate feature)
- Production use in a finalized Go compiler (PoC validation only)

## Implementation Files

### Go Frontend (both type checkers)
- `go/src/internal/buildcfg/` — new `SPMDWidth` global, parsed from `SPMD_WIDTH` env var
- `go/src/go/types/check_ext_spmd.go` — parameterize `laneCountForType()` with `buildcfg.SPMDWidth`
- `go/src/cmd/compile/internal/types2/check_ext_spmd.go` — same change mirrored

### TinyGo Backend
- `tinygo/main.go` — add `-simd-width` flag
- `tinygo/compileopts/options.go` — add `SIMDWidth int` to Options
- `tinygo/compileopts/config.go` — add `NativeSIMDWidth() int` backend API, propagate width
- `tinygo/compiler/spmd.go` — parameterize `spmdLaneCount()`, add decomposition logic
- `tinygo/compiler/compiler.go` — vector splitting in createBinOp, createExpr, memory ops
- `tinygo/loader/list.go` — propagate `SPMD_WIDTH` env var to `go list` subprocess

### Test Infrastructure
- `test/e2e/spmd-e2e-test.sh` — add `--simd-width` parameter
- `test/e2e/spmd-width-matrix.sh` — new script for full width matrix validation

## Phase Placement

Phase 2.9e in PLAN.md, after existing deferred items (2.9d scalar fallback) but before Phase 3 validation. Phase 3 uses this to run the full test matrix across all widths as part of comprehensive validation.

## Relationship to Other Features

- **Scalar fallback mode** (Phase 2.9d): Orthogonal. Scalar mode disables SIMD entirely; virtual width configures SIMD lane count. Both can coexist.
- **`*Within` cross-lane ops**: Work within groups of N lanes. At wider virtual widths, the group size parameter stays the same — the ops naturally decompose with the vector splitting.
- **`native` keyword**: Creates a clean abstraction boundary. Future backends (x86, ARM SVE) implement `NativeSIMDWidth()` and virtual width decomposition works automatically.
