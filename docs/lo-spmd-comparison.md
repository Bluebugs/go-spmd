# Lo SIMD vs Go SPMD: A Comparative Analysis

This document compares samber/lo's `exp/simd` package (Go 1.26 `goexperiment.simd` + `archsimd` intrinsics) with Go SPMD (`go for` loops + `lanes`/`reduce` packages). It covers operation mappings, code metrics, and identifies limitation categories where SPMD cannot yet replace hand-written SIMD.

## Overview

Lo's approach uses Go 1.26's new `simd` experiment to access hardware SIMD intrinsics (`archsimd` package) on amd64. Each operation is hand-written per (element-type x SIMD-width) combination, with runtime CPU feature detection dispatching to AVX, AVX2, or AVX-512 variants.

Go SPMD uses `go for` loops where the compiler automatically vectorizes the loop body. The programmer writes scalar-looking code; the compiler handles SIMD width, masking, tail loops, and reductions.

## Operation Mapping

### Implementable Operations

| Operation | Lo Functions | SPMD Code | SPMD Pattern |
|-----------|-------------|-----------|--------------|
| **Sum** | 30 functions (10 types x 3 widths) | 1 function | `go for` accumulator + `reduce.Add` |
| **Min** | 28 functions (2 types lack AVX) | 1 function | `go for` + conditional accumulator + `reduce.Min` |
| **Max** | 28 functions (2 types lack AVX) | 1 function | `go for` + conditional accumulator + `reduce.Max` |
| **Clamp** | 28 functions | 1 function | `go for` + varying if/else (element-wise, no reduction) |
| **Contains** | 30 functions | 1 function | `go for` + `Varying[bool]` accumulator + `reduce.Any` |
| **Mean** | 30 functions | 1 function | Sum then scalar divide |

Each SPMD function is ~5-10 lines of code that replaces ~30 hand-written functions totaling ~200+ lines per operation.

### Code Comparison: Sum

**Lo (one of 30 variants — SumInt32x4):**
```go
func SumInt32x4[T ~int32](collection []T) T {
    length := uint(len(collection))
    if length == 0 { return 0 }
    const lanes = simdLanes4
    base := unsafeSliceInt32(collection, length) // unsafe.Pointer cast
    var acc archsimd.Int32x4
    i := uint(0)
    for ; i+lanes <= length; i += lanes {
        v := archsimd.LoadInt32x4Slice(base[i : i+lanes])
        acc = acc.Add(v)
    }
    var buf [lanes]int32
    acc.Store(&buf)
    var sum T
    for k := uint(0); k < lanes; k++ { sum += T(buf[k]) }
    for ; i < length; i++ { sum += collection[i] } // tail
    return sum
}
```

Plus the dispatcher:
```go
func SumInt32[T ~int32](collection []T) T {
    switch currentSimdFeature {
    case simdFeatureAVX512: return SumInt32x16(collection)
    case simdFeatureAVX2:   return SumInt32x8(collection)
    case simdFeatureAVX:    return SumInt32x4(collection)
    default:                return lo.Sum(collection)
    }
}
```

**SPMD equivalent (replaces all 30 variants + 10 dispatchers):**
```go
func sumSPMD(data []int32) int32 {
    var total lanes.Varying[int32] = 0
    go for _, v := range data {
        total += v
    }
    return reduce.Add(total)
}
```

### Code Comparison: Contains

**Lo (one of 30 variants — ContainsInt32x4):**
```go
func ContainsInt32x4[T ~int32](collection []T, target T) bool {
    length := uint(len(collection))
    if length == 0 { return false }
    const lanes = simdLanes4
    targetVec := archsimd.BroadcastInt32x4(int32(target))
    base := unsafeSliceInt32(collection, length)
    i := uint(0)
    for ; i+lanes <= length; i += lanes {
        v := archsimd.LoadInt32x4Slice(base[i : i+lanes])
        cmp := v.Equal(targetVec)
        if cmp.ToBits() != 0 { return true }
    }
    for ; i < length; i++ {
        if collection[i] == target { return true }
    }
    return false
}
```

**SPMD equivalent:**
```go
func containsSPMD(data []int32, target int32) bool {
    var found lanes.Varying[bool] = false
    go for _, v := range data {
        if v == target {
            found = true
        }
    }
    return reduce.Any(found)
}
```

### Code Comparison: Clamp

**Lo (one of 28 variants — ClampInt32x4, ~25 lines):** Manual load, min/max intrinsics, store, tail loop.

**SPMD equivalent:**
```go
func clampSPMD(data []int32, lo, hi int32) []int32 {
    result := make([]int32, len(data))
    go for i := range len(data) {
        v := data[i]
        if v < lo {
            result[i] = lo
        } else if v > hi {
            result[i] = hi
        } else {
            result[i] = v
        }
    }
    return result
}
```

## Code Metrics

| Metric | Lo `exp/simd` | Go SPMD |
|--------|--------------|---------|
| Source files | 16 | 6 examples |
| Lines of Go | ~2000+ | ~60 |
| Functions per operation | 30-40 (10 types x 3-4 widths + dispatchers) | 1 |
| `unsafe.Pointer` usage | Every function (type casts) | None |
| Architecture support | amd64 only | Any LLVM backend (WASM, ARM, x86) |
| SIMD width handling | Runtime CPU detection + dispatch | Compile-time (128-bit WASM, etc.) |
| Tail loop handling | Manual per function | Automatic (loop peeling) |

## Limitation Categories

### 1. Callback-Based Operations (SumBy, MeanBy)

Lo provides `SumByInt32[T any, R ~int32](collection []T, iteratee func(item T) R)` which applies an arbitrary callback before summing. SPMD cannot vectorize arbitrary function pointers — the callback would need to be inlined into the `go for` body.

**Workaround:** If the callback logic is known at compile time, inline it:
```go
// Instead of SumBy(items, func(x Item) int32 { return x.Price })
var total lanes.Varying[int32] = 0
go for _, item := range items {
    total += item.Price  // inlined field access
}
```

This only works for field access and simple arithmetic, not arbitrary functions.

### 2. Cross-Lane Algorithms

Some SIMD algorithms require each lane to access values from other lanes within the same vector register. Examples:

- **Sorted-set intersection:** Broadcast one element from set A across all lanes, compare against N elements of set B simultaneously. Requires cross-lane broadcast + comparison + popcount on the result mask. This is the pattern behind algorithms like Schlegel et al.'s SIMD set intersection.

- **Prefix sum (scan):** Each output element is the sum of all prior elements. Requires log2(N) rounds of shift-and-add across lanes. Used in stream compaction, radix sort, and histogram building.

- **Lookup tables (base64, hex decode):** Use a varying index to look up values in a constant table. On x86 this maps to `vpshufb`/`vpermb`; on WASM to `i8x16.swizzle`. Our hex-encode example uses `i8x16.swizzle` for this. The general case (tables larger than one vector register) requires multi-register lookup or gather instructions.

Our `*Within` operations (`RotateWithin`, `ShiftLeftWithin`, `ShiftRightWithin`, `SwizzleWithin`) handle fixed shuffle patterns within sub-groups of a vector. Full-width `Rotate` and `Swizzle` (variable indices) are not yet implemented. Prefix-scan patterns are not expressible in the current SPMD model.

### 3. Variable-Length Output (Filter/Compact)

Operations like `lo.Filter` produce a subset of input elements based on a predicate. The output size varies — this requires compress-store semantics: write only active lanes contiguously to the output buffer.

Compress-store needs:
1. Evaluate the predicate (produces a varying bool mask)
2. Popcount the mask (how many elements to write)
3. Prefix-sum on the mask (where each active element goes in the output)
4. Scatter-store active elements to contiguous output positions

Steps 2-4 are cross-lane operations that aren't expressible in the current `go for` model. AVX-512 has dedicated `vpcompressd` instructions for this; WASM SIMD128 does not.

### 4. Runtime SIMD Width Dispatch

Lo detects CPU features at startup (`archsimd.X86.AVX()`, `.AVX2()`, `.AVX512()`) and dispatches to the widest available implementation. A single binary runs optimally on any x86 CPU.

SPMD compiles to a fixed SIMD width determined by the target (128-bit for WASM SIMD128). To support multiple widths would require:
- Compiling multiple binaries (one per width)
- Fat binaries with runtime selection (not supported)
- A future SPMD runtime width parameter

For WASM deployment this is a non-issue (SIMD128 is the only width). For native targets, this is a real gap compared to lo's approach.

## Current Backend Status

All 6 implemented operations compile and run correctly:

| Operation | Status | Speedup (Node.js) | Notes |
|-----------|--------|-------------------|-------|
| Sum | Run pass | 5.90x | Simple accumulator (`+=`) |
| Mean | Run pass | 5.90x | Same as sum + scalar divide |
| Min | Run pass | 5.90x | Varying conditional accumulator (`if v < result { result = v }`) |
| Max | Run pass | 5.90x | Same pattern as min |
| Contains | Run pass | 0.82x | `Varying[bool]` accumulator requires mask widening (16-lane bool in 4-lane int32 loop) |
| Clamp | Run pass | 1.07x | Varying if/else-if/else chain with recursive predication |

Contains and Clamp have lower speedups due to additional overhead: Contains needs shuffle-based mask widening for the `Varying[bool]` type mismatch, and Clamp's three-way branch produces more masked operations. Both are correctness-complete and can be optimized further.

## Future Work

Once the `union-type-generics` bug is fixed (x-tools SPMDType in `typeparams.Free`), the SPMD examples can be made fully generic:

```go
func Sum[T constraints.Integer | constraints.Float](data []T) T {
    var total lanes.Varying[T] = 0
    go for _, v := range data {
        total += v
    }
    return reduce.Add(total)
}
```

This single generic function would replace all 40 of lo's Sum-related functions (10 types x 3 widths + 10 dispatchers) while supporting any numeric type on any architecture.

## Conclusion

For straightforward data-parallel operations (sum, min, max, clamp, contains, mean), SPMD replaces ~2000 lines of platform-specific intrinsics code with ~60 lines of portable Go. The SPMD code requires no `unsafe`, no manual SIMD width management, and no per-type specialization.

The trade-off: SPMD cannot express cross-lane algorithms (set intersection, prefix scan), callback-based operations (SumBy), variable-length output (filter/compact), or runtime width dispatch. These limitations are fundamental to the SPMD execution model and represent genuinely different algorithmic patterns that require explicit vector programming.
