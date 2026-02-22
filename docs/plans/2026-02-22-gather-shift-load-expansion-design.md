# Gather Shift-Right Load Expansion Optimization

**Date**: 2026-02-22
**Status**: DESIGN
**Phase**: 2.9 (TinyGo Backend Optimization)

## Problem Statement

When SPMD code indexes a uniform array with a right-shifted varying index:

```go
d = s[i >> n]
```

where `i` is varying (lane indices), `s` is a uniform array, and `n` is a
compile-time constant, the resulting access pattern produces **duplicated
elements** that can be satisfied by a smaller contiguous load plus a shuffle
expansion, rather than an expensive gather operation.

### Example (4 lanes, `i = [0, 1, 2, 3]`)

| Shift `n` | Effective indices | Result | Unique loads needed |
|-----------|------------------|--------|-------------------|
| 0 | `[0, 1, 2, 3]` | `[s[0], s[1], s[2], s[3]]` | 4 (already contiguous) |
| 1 | `[0, 0, 1, 1]` | `[s[0], s[0], s[1], s[1]]` | 2 |
| 2 | `[0, 0, 0, 0]` | `[s[0], s[0], s[0], s[0]]` | 1 (broadcast) |

### Why This Matters

- **Gather operations are expensive**: On WASM SIMD128, `llvm.masked.gather`
  generates per-lane scalar loads (extract index, GEP, load, insert). For 4
  lanes, that is 4 separate load instructions with pointer arithmetic.
- **Contiguous loads are cheap**: A single `v128.load` or `v128.load32_zero`
  instruction loads aligned data in one operation.
- **Shuffles are free or nearly free**: `i8x16.shuffle` is a single WASM
  instruction.

The net effect: replacing 4 scalar loads + 4 inserts with 1 vector load + 1
shuffle is a significant improvement, especially in tight loops like the
base64 decoder or IPv4 parser lookup tables.

## ISPC Reference

ISPC implements this as part of its **Gather Coalescing Pass**
(`opt/GatherCoalescePass.cpp`) with pattern detection in `llvmutil.cpp`.

### ISPC's Detection Strategy

ISPC operates at the LLVM IR level (post-lowering), detecting the pattern in
already-generated gather intrinsic calls:

1. **`lVectorShiftRightAllEqual()`** (`llvmutil.cpp:1021`): Detects when a
   vector shift-right produces all-equal elements by:
   - Verifying the shift amount is a uniform constant across all lanes
   - Calling `lAllDivBaseEqual()` to check if dividing by `2^shift` yields
     identical quotients

2. **`lAllDivBaseEqual()`** (`llvmutil.cpp:891`): Recursively analyzes SSA
   def-use chains to determine if all vector elements produce the same result
   when integer-divided by `baseValue`. Handles:
   - Constant vectors (direct check)
   - PHI nodes (recursive through all incoming values)
   - `add(smear, <0,1,2,3...>)` patterns where the smear is a multiple of
     `baseValue` and the constant offsets all have the same quotient

3. **`LLVMVectorValuesAllEqual()`**: Top-level entry point used by the gather
   coalescing pass. When variable offsets to a gather are all-equal (possibly
   after shift analysis), the gather can be coalesced.

### ISPC's Transformation

Once detected, ISPC:
1. Computes a shared scalar base pointer
2. Extracts constant byte offsets from each gather lane
3. Plans efficient loads via `lSelectLoads()` (prefers wider vector loads)
4. Emits the loads via `lEmitLoads()` (scalar, 2-wide, 4-wide, or 8-wide)
5. Assembles the final result via `lAssembleResultVectors()` using
   `shufflevector` to place loaded values into the correct lanes

### Key Difference from Our Approach

ISPC's gather coalescing is a **general post-lowering LLVM pass** that handles
multiple gathers with the same base. Our optimization is **simpler and more
targeted**: we detect the shift-right pattern at the Go SSA level (before LLVM
IR generation) and emit an optimized load+shuffle directly, completely
bypassing gather generation.

## Design

### Pattern Recognition

At the Go SSA level, the pattern `s[i >> n]` produces:

```
t1 = BinOp(i, token.SHR, n)    // i >> n  (varying >> uniform const)
t2 = IndexAddr(s, t1)           // s[t1]
t3 = UnOp(t2, token.MUL)       // *t2 (load)
```

Where:
- `i` is a known SPMD loop iterator (in `spmdValueOverride` /
  `spmdLoopState.activeLoops`)
- `n` is a compile-time constant integer
- `s` is a uniform array/slice

We can also handle the more general pattern `s[(base + i) >> n]` where `base`
is a scalar expression added to the iterator before shifting.

### Integration Point

The detection hooks into the existing `spmdAnalyzeContiguousIndex()` function
in `compiler/spmd.go`. Currently this function recognizes:

1. Direct iterator phi match: `index == iter`
2. Scalar + iterator: `index == scalar + iter`

We add a third pattern:

3. **Shift-right of contiguous**: `index == (expr) >> const` where `expr` is
   contiguous (pattern 1 or 2)

When pattern 3 is detected, instead of marking as contiguous (which would
generate a wrong masked.load), we mark it with a new category that triggers
the load+shuffle path.

### New Data Structures

```go
// spmdShiftedLoadInfo describes a gather that can be optimized to
// a smaller contiguous load + shuffle expansion.
//
// For pattern: s[(base + iter) >> shift]
//   - The effective indices are: (base + [0,1,...,N-1]) >> shift
//   - The unique values loaded: base>>shift to (base+N-1)>>shift
//   - uniqueCount = number of unique indices = ceil(laneCount / (1<<shift))
//   - shuffleMask maps each lane to its position in the loaded vector
type spmdShiftedLoadInfo struct {
    scalarBasePtr llvm.Value      // GEP to s[base >> shift]
    uniqueCount   int             // number of unique elements to load
    shuffleMask   []int           // lane i -> index in loaded vector
    loop          *spmdActiveLoop // owning SPMD loop
}
```

### Detection Algorithm

```
func spmdAnalyzeShiftedIndex(index ssa.Value) -> (shiftInfo, bool):
    // Check if index is BinOp with SHR
    binop, ok := index.(*ssa.BinOp)
    if !ok || binop.Op != token.SHR:
        return nil, false

    // Verify shift amount is a compile-time constant
    shiftConst, ok := binop.Y.(*ssa.Const)
    if !ok:
        return nil, false
    shiftAmount := shiftConst.Int64()

    // Verify the shifted operand is contiguous (iter or scalar+iter)
    loop, scalarBase, ok := spmdAnalyzeContiguousIndex(binop.X)
    if !ok:
        return nil, false

    // Compute properties
    laneCount := loop.laneCount
    groupSize := 1 << shiftAmount  // lanes per unique value

    // Build shuffle mask: lane i maps to i >> shiftAmount
    shuffleMask := make([]int, laneCount)
    for i := 0; i < laneCount; i++ {
        shuffleMask[i] = i >> shiftAmount
    }
    uniqueCount := shuffleMask[laneCount-1] + 1

    // Compute scalar base: scalarBase >> shiftAmount
    shiftedBase := CreateLShr(scalarBase, shiftAmount)
    basePtr := GEP(array, shiftedBase)

    return &spmdShiftedLoadInfo{
        scalarBasePtr: basePtr,
        uniqueCount:   uniqueCount,
        shuffleMask:   shuffleMask,
        loop:          loop,
    }, true
```

### Load + Shuffle Generation

When a load (`*ssa.UnOp` with `token.MUL`) finds a `spmdShiftedLoadInfo`
instead of a `spmdContiguousInfo`, it generates:

```
func spmdShiftedLoad(info *spmdShiftedLoadInfo, elemType llvm.Type) -> llvm.Value:
    laneCount := info.loop.laneCount

    if info.uniqueCount == 1:
        // Pure broadcast case (shift >= log2(laneCount))
        // Load single scalar, splat to all lanes
        scalar := CreateLoad(elemType, info.scalarBasePtr)
        return spmdSplatScalar(scalar, laneCount)

    // General case: load uniqueCount elements, shuffle to expand
    //
    // Step 1: Load a small contiguous vector
    loadVecType := VectorType(elemType, info.uniqueCount)
    // If uniqueCount is not a power of 2 or < laneCount, we may need
    // to load into a wider type and use only part of it.
    // For WASM SIMD128 with 4 i32 lanes, uniqueCount is 1 or 2.
    loaded := CreateLoad(loadVecType, info.scalarBasePtr)

    // Step 2: Shuffle to expand
    // If uniqueCount < laneCount, we need a two-source shufflevector
    // where both sources are the loaded vector.
    // shuffleMask already has the indices we need.
    if info.uniqueCount == laneCount:
        return loaded  // No expansion needed (shift == 0)

    // Pad loaded to laneCount width if needed, then shuffle
    // Use shufflevector(loaded, undef, mask) where mask duplicates elements
    result := CreateShuffleVector(loaded, undef, info.shuffleMask)
    return result
```

### Masking

The execution mask must be applied to the loaded result, not to the load
itself (since we're loading a smaller vector). Two approaches:

**Approach A (simpler)**: Load unconditionally (the base pointer is known
valid since it comes from a uniform array), then apply mask via select on the
final expanded result. This works because uniform arrays have all elements
accessible.

**Approach B (conservative)**: Use a reduced mask for the smaller load.
For `n=1` with mask `[1,1,0,0]`, the reduced mask is `[1,0]` (OR adjacent
pairs). This is more complex and only needed if the array could be
out-of-bounds, which cannot happen for uniform arrays indexed within loop
bounds.

**Recommendation**: Use Approach A. The load is from a uniform array that
exists in full; we're just loading fewer elements than lanes. The mask only
matters for selecting which lanes receive values, applied after expansion.

### Tail Mask Interaction

In the last iteration of a `go for` loop, the tail mask disables out-of-range
lanes. The shifted index `(base + i) >> n` with the tail mask means some
lanes are inactive. Since we load a smaller contiguous range that is always
within bounds of the uniform source array, the tail mask only needs to be
applied to the final result (selecting between the shuffled load result and a
passthrough value).

### Edge Cases

1. **Shift amount >= log2(laneCount)**: All lanes read the same element.
   Optimize to a scalar load + broadcast (splat). For 4 lanes, this means
   `n >= 2`.

2. **Shift amount == 0**: No duplication, this is a standard contiguous
   access. The existing contiguous path already handles this, so
   `spmdAnalyzeShiftedIndex` should not match (it requires `shiftAmount > 0`).

3. **Non-power-of-2 unique counts**: For unusual lane counts, uniqueCount may
   not be a power of 2. Use the next power of 2 for the load width and
   shuffle with some dead lanes.

4. **Element types**: Works for all element types (i32, f32, i64, f64, i8,
   i16). The shufflevector operates on the element type's vector.

5. **Loop base offset**: When the pattern is `s[(j*stride + i) >> n]`, the
   scalar base changes each outer iteration. The GEP for scalarBasePtr must
   use the shifted scalar base, recomputed per iteration.

6. **Arithmetic right shift (SAR) vs logical (SHR)**: For non-negative
   indices (which array indices always are), both produce the same result. We
   should accept both `token.SHR` and verify the pattern works regardless.

## Concrete WASM SIMD128 Examples

### Example 1: `d = s[i >> 1]` (4 lanes, i32 elements)

```
; Before (gather):
; Build 4 GEPs, load each scalar, insert into vector
%idx = <4 x i32> [0, 0, 1, 1]  ; from i >> 1
%gep0 = getelementptr i32, ptr %s, i32 0
%gep1 = getelementptr i32, ptr %s, i32 0
%gep2 = getelementptr i32, ptr %s, i32 1
%gep3 = getelementptr i32, ptr %s, i32 1
%v0 = load i32, ptr %gep0
%v1 = load i32, ptr %gep1
%v2 = load i32, ptr %gep2
%v3 = load i32, ptr %gep3
%r = insertelement x4 ...  ; 4 loads + 4 inserts

; After (load + shuffle):
%base = getelementptr i32, ptr %s, i32 %shifted_iter
%loaded = load <2 x i32>, ptr %base         ; 1 load (64 bits)
%result = shufflevector <2 x i32> %loaded, <2 x i32> undef,
          <4 x i32> <i32 0, i32 0, i32 1, i32 1>  ; 1 shuffle
```

**WASM output**: `v128.load64_zero` + `i8x16.shuffle`

### Example 2: `d = s[i >> 2]` (4 lanes, i32 elements)

```
; After (scalar load + broadcast):
%base = getelementptr i32, ptr %s, i32 %shifted_iter
%scalar = load i32, ptr %base               ; 1 scalar load
%result = splat <4 x i32> %scalar           ; broadcast
```

**WASM output**: `i32.load` + `i32x4.splat`

### Example 3: `d = s[i >> 1]` (4 lanes, i8 elements, laneCount=16)

With 16 lanes of i8 and shift=1, uniqueCount = 8:

```
%loaded = load <8 x i8>, ptr %base
%result = shufflevector <8 x i8> %loaded, <8 x i8> undef,
          <16 x i32> <0,0,1,1,2,2,3,3,4,4,5,5,6,6,7,7>
```

## Implementation Plan

### Step 1: Pattern Detection

**File**: `compiler/spmd.go`

Add `spmdAnalyzeShiftedIndex()` function that:
- Checks for `BinOp` with `token.SHR` (or `token.AND_NOT` if used)
- Verifies constant shift amount via `*ssa.Const`
- Recursively calls `spmdAnalyzeContiguousIndex()` on the shifted operand
- Computes `uniqueCount` and `shuffleMask`
- Returns `*spmdShiftedLoadInfo` or nil

Add `spmdShiftedLoadInfo` struct and `spmdShiftedPtr` map (parallel to
`spmdContiguousPtr`).

### Step 2: IndexAddr Integration

**File**: `compiler/compiler.go` (IndexAddr handling, ~line 2870)

Before falling through to the vector-of-GEPs path, call
`spmdAnalyzeShiftedIndex()`. If matched:
- Compute the shifted scalar base pointer via GEP
- Register in `spmdShiftedPtr` map
- Return the scalar pointer (it won't be used directly; loads dispatch
  through the map)

### Step 3: Load Dispatch

**File**: `compiler/compiler.go` (UnOp/deref handling, ~line 4200)

After checking `spmdContiguousPtr`, check `spmdShiftedPtr`. If found:
- For `uniqueCount == 1`: emit scalar load + splat
- For `uniqueCount < laneCount`: emit narrow vector load + shufflevector
- Apply execution mask via select on the result

### Step 4: Store Path (Optional, Lower Priority)

The pattern `s[i >> n] = d` (scatter with duplicating indices) is unusual
and would cause multiple lanes to write to the same location. This is likely
a programming error, so store optimization is **not planned** for this phase.

### Step 5: Tests

**File**: `compiler/spmd_llvm_test.go`

Add test cases:
1. `TestSPMDShiftedLoad_SHR1_4Lanes` - basic `i >> 1` with 4 i32 lanes
2. `TestSPMDShiftedLoad_SHR2_4Lanes` - broadcast case `i >> 2`
3. `TestSPMDShiftedLoad_SHR1_WithBase` - `(base + i) >> 1`
4. `TestSPMDShiftedLoad_SHR1_i8_16Lanes` - i8 elements, 16 lanes
5. `TestSPMDShiftedLoad_SHR3_16Lanes` - i8, shift 3, uniqueCount=2

**File**: `test/e2e/` (if applicable)

E2E test with a lookup table pattern:
```go
func lookupExpand(table [4]int32, indices []int32) {
    go for i := range len(indices) {
        indices[i] = table[indices[i] >> 1]
    }
}
```

### Step 6: Performance Validation

Compare gather vs load+shuffle for the base64 decoder lookup table pattern
to measure real-world impact.

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| Shift amount could be varying | Check that `binop.Y` is `*ssa.Const`; skip if not |
| Out-of-bounds on narrow load | Uniform array bounds are checked at compile time; runtime bounds come from loop range |
| Interaction with tail mask | Apply mask after shuffle, not on the narrow load |
| `uniqueCount` not power of 2 | Round up load width; shufflevector handles dead lanes |
| Performance regression from load width mismatch | Benchmark; WASM has `v128.load64_zero` for partial loads |

## Summary

This optimization transforms expensive gather operations (N scalar loads +
N inserts) into a single narrow contiguous load + a single shufflevector when
the gather indices follow a right-shift pattern. For WASM SIMD128 with 4 i32
lanes, the common case `i >> 1` becomes 1 load + 1 shuffle instead of 4
loads + 4 inserts -- a clear win for lookup table access patterns common in
parsers and encoders.
