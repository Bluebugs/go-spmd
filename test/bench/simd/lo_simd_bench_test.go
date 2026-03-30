//go:build go1.26 && goexperiment.simd && amd64

// Package simd-bench benchmarks samber/lo exp/simd (Go 1.26 GOEXPERIMENT=simd)
// against samber/lo generic functions for operations matching our SPMD examples.
//
// Input sizes match the SPMD lo-* integration tests:
//   - sum / mean / min / max / contains: 1024 x int32
//   - clamp: 8192 x int32
//
// Data construction is identical to test/bench/lo_comparison_test.go so that
// results are directly comparable across three baselines:
//   1. lo generic (standard gc, no SIMD)
//   2. lo exp/simd (gc + AVX/AVX2/AVX-512 intrinsics via GOEXPERIMENT=simd)
//   3. SPMD SIMD (TinyGo + SSE/AVX2 + WASM SIMD128) — numbers from memory
//
// Run:
//
//	GOEXPERIMENT=simd go test -bench=. -benchtime=5s -count=3 -cpu=1
package simd_bench

import (
	"runtime"
	"testing"

	lo "github.com/samber/lo"
	losimd "github.com/samber/lo/exp/simd"
)

// ---- shared test data (allocated once, identical to lo_comparison_test.go) --

const (
	smallSize = 1024
	largeSize = 8192
)

var (
	// sum/mean: 1..1024, same as SPMD lo-sum/lo-mean
	smallData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32(i + 1)
		}
		return d
	}()

	// min: pseudo-random pattern + planted minimum, same as SPMD lo-min
	minData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32((i*7 + 13) % 10000)
		}
		d[777] = -999
		return d
	}()

	// max: pseudo-random pattern + planted maximum, same as SPMD lo-max
	maxData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32((i*7 + 13) % 10000)
		}
		d[333] = 99999
		return d
	}()

	// contains: 0..1023, worst-case target = last element
	containsData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32(i)
		}
		return d
	}()
	containsTarget = int32(smallSize - 1)

	// clamp: -4096..4095, same as SPMD lo-clamp
	clampData = func() []int32 {
		d := make([]int32, largeSize)
		for i := range d {
			d[i] = int32(i - largeSize/2)
		}
		return d
	}()
	clampLo = int32(-100)
	clampHi = int32(100)

	// pre-allocated output buffer for clamp (no allocation noise)
	clampOut = make([]int32, largeSize)

	// sinks
	sinkInt32 int32
	sinkBool  bool
)

// ---- BenchmarkSum -----------------------------------------------------------
// Compares lo.Sum (generic, scalar) vs lo/simd.SumInt32 (AVX/AVX2/AVX-512).

func BenchmarkSum(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = lo.Sum(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("lo-simd", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = losimd.SumInt32(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// ---- BenchmarkMean ----------------------------------------------------------

func BenchmarkMean(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = lo.Mean(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("lo-simd", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = losimd.MeanInt32(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// ---- BenchmarkMin -----------------------------------------------------------

func BenchmarkMin(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = lo.Min(minData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("lo-simd", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = losimd.MinInt32(minData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// ---- BenchmarkMax -----------------------------------------------------------

func BenchmarkMax(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = lo.Max(maxData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("lo-simd", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = losimd.MaxInt32(maxData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// ---- BenchmarkContains ------------------------------------------------------
// lo/exp/simd contains functions live in intersect_avx512.go.
// ContainsInt32x4 uses 128-bit SSE (same width as our SPMD SSE baseline).
// ContainsInt32x8 uses 256-bit AVX2 (2x wider).
// The top-level ContainsInt32 dispatcher does not exist yet; call the specific
// widths directly.

func BenchmarkContains(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBool = lo.Contains(containsData, containsTarget)
		}
		runtime.KeepAlive(sinkBool)
	})
	b.Run("lo-simd-x4-SSE", func(b *testing.B) {
		// ContainsInt32x4: 4-lane SSE (128-bit) — same width as SPMD SSE target
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBool = losimd.ContainsInt32x4(containsData, containsTarget)
		}
		runtime.KeepAlive(sinkBool)
	})
	b.Run("lo-simd-x8-AVX2", func(b *testing.B) {
		// ContainsInt32x8: 8-lane AVX2 (256-bit) — 2x wider than SPMD
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBool = losimd.ContainsInt32x8(containsData, containsTarget)
		}
		runtime.KeepAlive(sinkBool)
	})
}

// ---- BenchmarkClamp ---------------------------------------------------------
// lo/simd.ClampInt32 returns a new slice; we pre-allocate and pass the same
// output slice to avoid measuring allocation.  The SPMD example also uses a
// pre-allocated output buffer.

func BenchmarkClamp(b *testing.B) {
	b.Run("lo-generic", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// lo.Clamp is a scalar element function; loop over slice manually
			// (same as lo_comparison_test.go loClamp helper)
			for j, v := range clampData {
				clampOut[j] = lo.Clamp(v, clampLo, clampHi)
			}
		}
		runtime.KeepAlive(clampOut)
	})
	b.Run("lo-simd", func(b *testing.B) {
		// ClampInt32 returns a new []int32 each call; we can't avoid this
		// allocation since the API has no output-buffer variant.
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out := losimd.ClampInt32(clampData, clampLo, clampHi)
			runtime.KeepAlive(out)
		}
	})
}
