// Package bench compares samber/lo generic functions against hand-written Go
// scalar loops on the same operations that the SPMD lo-* examples exercise.
//
// The SPMD examples use TinyGo + WebAssembly, so this benchmark runs under the
// standard gc compiler (go test) on native x86-64. The intent is to establish
// what a standard-library-quality Go implementation looks like so that SPMD
// WASM numbers have a meaningful reference point.
//
// Input sizes match the SPMD examples:
//   - sum / mean / min / max / contains: 1024 x int32
//   - clamp: 8192 x int32
//
// Data construction matches the SPMD examples exactly so the workloads are
// identical.
package bench

import (
	"math"
	"runtime"
	"testing"

	lo "github.com/samber/lo"
)

// ---- shared test data (package-level, allocated once) ----------------------

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

	// min/max: pseudo-random pattern + planted extreme, same as SPMD lo-min/lo-max
	minData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32((i*7 + 13) % 10000)
		}
		d[777] = -999 // planted minimum
		return d
	}()

	maxData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32((i*7 + 13) % 10000)
		}
		d[333] = 99999 // planted maximum
		return d
	}()

	// contains: 0..1023, worst-case target = last element (same as SPMD lo-contains)
	containsData = func() []int32 {
		d := make([]int32, smallSize)
		for i := range d {
			d[i] = int32(i)
		}
		return d
	}()
	containsTarget = int32(smallSize - 1)

	// clamp: -4096..4095 clamped to [-100, 100], same as SPMD lo-clamp
	clampData = func() []int32 {
		d := make([]int32, largeSize)
		for i := range d {
			d[i] = int32(i - largeSize/2)
		}
		return d
	}()
	clampLo = int32(-100)
	clampHi = int32(100)

	// pre-allocated output buffer for clamp (avoids measuring allocation)
	clampOut = make([]int32, largeSize)

	// global sinks to prevent dead-code elimination
	sinkInt32 int32
	sinkBool  bool
)

// ---- scalar helpers (match the SPMD examples' scalar baselines) ------------

func sumScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total
}

func meanScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total / int32(len(data))
}

func minScalar(data []int32) int32 {
	result := int32(math.MaxInt32)
	for _, v := range data {
		if v < result {
			result = v
		}
	}
	return result
}

func maxScalar(data []int32) int32 {
	result := int32(math.MinInt32)
	for _, v := range data {
		if v > result {
			result = v
		}
	}
	return result
}

func containsScalar(data []int32, target int32) bool {
	for _, v := range data {
		if v == target {
			return true
		}
	}
	return false
}

func clampScalar(data, result []int32, lo, hi int32) {
	for i, v := range data {
		if v < lo {
			result[i] = lo
		} else if v > hi {
			result[i] = hi
		} else {
			result[i] = v
		}
	}
}

// ---- lo wrappers that take []int32 -----------------------------------------
//
// samber/lo uses Go generics with type parameters, so these thin wrappers keep
// the benchmark call sites clean.

func loSum(data []int32) int32         { return lo.Sum(data) }
func loMean(data []int32) int32        { return lo.Mean(data) }
func loMin(data []int32) int32         { return lo.Min(data) }
func loMax(data []int32) int32         { return lo.Max(data) }
func loContains(data []int32, t int32) bool { return lo.Contains(data, t) }

func loClamp(data, result []int32, loV, hiV int32) {
	for i, v := range data {
		result[i] = lo.Clamp(v, loV, hiV)
	}
}

// ---- benchmarks ------------------------------------------------------------

// BenchmarkSum compares lo.Sum vs a hand-written scalar loop.
func BenchmarkSum(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = loSum(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = sumScalar(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// BenchmarkMean compares lo.Mean vs a hand-written scalar loop.
func BenchmarkMean(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = loMean(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = meanScalar(smallData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// BenchmarkMin compares lo.Min vs a hand-written scalar loop.
func BenchmarkMin(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = loMin(minData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = minScalar(minData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// BenchmarkMax compares lo.Max vs a hand-written scalar loop.
func BenchmarkMax(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = loMax(maxData)
		}
		runtime.KeepAlive(sinkInt32)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkInt32 = maxScalar(maxData)
		}
		runtime.KeepAlive(sinkInt32)
	})
}

// BenchmarkContains measures worst-case search (target is the last element).
func BenchmarkContains(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBool = loContains(containsData, containsTarget)
		}
		runtime.KeepAlive(sinkBool)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sinkBool = containsScalar(containsData, containsTarget)
		}
		runtime.KeepAlive(sinkBool)
	})
}

// BenchmarkClamp measures element-wise clamp over 8192-element slice.
// The output buffer is pre-allocated to match the SPMD example's methodology.
func BenchmarkClamp(b *testing.B) {
	b.Run("lo", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			loClamp(clampData, clampOut, clampLo, clampHi)
		}
		runtime.KeepAlive(clampOut)
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			clampScalar(clampData, clampOut, clampLo, clampHi)
		}
		runtime.KeepAlive(clampOut)
	})
}
