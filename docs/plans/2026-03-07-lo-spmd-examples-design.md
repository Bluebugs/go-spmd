# Lo SPMD Examples — Design

**Date:** 2026-03-07
**Status:** Approved

## Goal

Implement SPMD equivalents of samber/lo's `exp/simd` package operations as examples and E2E tests with scalar-vs-SPMD benchmarks. Produce a standalone analysis document (`docs/lo-spmd-comparison.md`) covering operation mappings, code reduction metrics, and SPMD limitation categories.

## Context

Lo's `exp/simd` package provides SIMD-accelerated collection operations (Sum, Min, Max, Clamp, Contains, Mean, SumBy, MeanBy) using Go 1.26's `goexperiment.simd` + `archsimd` intrinsics. Each operation is hand-written per (type x SIMD-width) combination, totaling ~2000+ lines of amd64-only code with `unsafe.Pointer` casts and runtime CPU feature dispatch.

Our SPMD syntax (`go for` loops + `lanes`/`reduce` packages) can express the same operations in ~5 generic functions, architecture-portable, with no unsafe code.

## Operations

| Operation | SPMD Pattern | Backend Support | Type |
|-----------|-------------|-----------------|------|
| Sum | `go for` + accumulator + `reduce.Add` | DONE | int32 |
| Min | `go for` + accumulator + `reduce.Min` | DONE | int32 |
| Max | `go for` + accumulator + `reduce.Max` | DONE | int32 |
| Clamp | `go for` + varying if/else + store | DONE | int32 |
| Contains | `go for` + compare + `reduce.Any` + break | DONE | int32 |
| Mean | Sum / len (trivial wrapper) | DONE | int32 |

## File Structure

```
examples/lo/sum/main.go
examples/lo/min/main.go
examples/lo/max/main.go
examples/lo/clamp/main.go
examples/lo/contains/main.go
examples/lo/mean/main.go

test/integration/spmd/lo-sum/main.go
test/integration/spmd/lo-min/main.go
test/integration/spmd/lo-max/main.go
test/integration/spmd/lo-clamp/main.go
test/integration/spmd/lo-contains/main.go
test/integration/spmd/lo-mean/main.go

docs/lo-spmd-comparison.md
```

### Example pattern (`examples/lo/<op>/main.go`)
- Clean, minimal: SPMD function + correctness check + print result
- `// run -goexperiment spmd` directive
- Uses `int32` (4 lanes on WASM128)

### E2E test pattern (`test/integration/spmd/lo-<op>/main.go`)
- Scalar implementation + SPMD implementation
- Correctness check (scalar == SPMD == expected)
- Timed benchmark: warmup + multiple runs, print speedup ratio
- Data size 1024, same pattern as existing `simple-sum`
- Added as `test_run` entries in `spmd-e2e-test.sh`

## Not Implemented (Limitations)

Documented in `docs/lo-spmd-comparison.md`:

1. **SumBy/MeanBy** — callback `func(T) R` can't be vectorized
2. **Cross-lane algorithms** — sorted-set intersection, prefix sums, base64 lookup tables
3. **Variable-length output** — filter/compact produce different sizes per lane
4. **Runtime width dispatch** — lo picks AVX/AVX2/AVX512 at runtime; SPMD is compile-time

## Future Work

Once the `union-type-generics` bug is fixed (x-tools SPMDType in typeparams.Free), these examples should be converted to generic functions and tested across all base numeric types (int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64). This will demonstrate full type-generic SPMD replacing lo's per-type combinatorial explosion.

## Implementation Plan

1. Create `examples/lo/` directory with 6 sub-examples
2. Create corresponding `test/integration/spmd/lo-*/` E2E tests with benchmarks
3. Add E2E entries to `spmd-e2e-test.sh`
4. Write `docs/lo-spmd-comparison.md` analysis document
5. Run E2E suite to verify all compile + run
