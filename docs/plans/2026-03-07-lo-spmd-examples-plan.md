# Lo SPMD Examples Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement 6 SPMD equivalents of lo's SIMD operations as examples + E2E tests with scalar-vs-SPMD benchmarks, plus a standalone comparison document.

**Architecture:** Each operation gets two files: a clean example in `examples/lo/<op>/main.go` and a benchmark test in `test/integration/spmd/lo-<op>/main.go` with scalar vs SPMD timing. All use `int32` (4 lanes on WASM128). E2E script entries verify correctness.

**Tech Stack:** Go SPMD (`go for`, `lanes`, `reduce`), TinyGo WASM, E2E test harness (`spmd-e2e-test.sh`)

**Design doc:** `docs/plans/2026-03-07-lo-spmd-examples-design.md`

**Future work:** Once `union-type-generics` bug is fixed (x-tools SPMDType in typeparams.Free), convert to generic functions and test across all base numeric types (int8/16/32/64, uint8/16/32/64, float32/64).

---

### Task 1: Sum example + E2E test

**Files:**
- Create: `examples/lo/sum/main.go`
- Create: `test/integration/spmd/lo-sum/main.go`

**Step 1: Write the example**

Create `examples/lo/sum/main.go`:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's SumInt32 — replaces 30+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := make([]int32, 64)
	for i := range data {
		data[i] = int32(i + 1)
	}

	scalar := sumScalar(data)
	spmd := sumSPMD(data)
	expected := int32(64 * 65 / 2) // 2080

	fmt.Printf("Sum: scalar=%d spmd=%d expected=%d\n", scalar, spmd, expected)
	if scalar != expected || spmd != expected {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func sumScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total
}

func sumSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total)
}
```

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-sum/main.go`:

```go
// run -goexperiment spmd

// Lo Sum SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i + 1)
	}

	// Correctness
	scalar := sumScalar(data)
	spmd := sumSPMD(data)
	expected := int32(dataSize * (dataSize + 1) / 2)
	fmt.Printf("Sum: scalar=%d spmd=%d expected=%d\n", scalar, spmd, expected)
	if scalar != expected || spmd != expected {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	// Benchmark
	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			sumScalar(data)
			sumSPMD(data)
		}
	}

	scalarNs := bench(func() { sumScalar(data) })
	spmdNs := bench(func() { sumSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func sumScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total
}

func sumSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total)
}

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	// Return minimum
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile and test**

```bash
# Compile example
make compile EXAMPLE=lo/sum
# Compile E2E test
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/spmd-e2e/lo-sum.wasm test/integration/spmd/lo-sum/main.go
# Run
wasmtime run /tmp/spmd-e2e/lo-sum.wasm
```

Expected output contains: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/sum/main.go test/integration/spmd/lo-sum/main.go
git commit -m "Add lo Sum SPMD example and E2E benchmark"
```

---

### Task 2: Min example + E2E test

**Files:**
- Create: `examples/lo/min/main.go`
- Create: `test/integration/spmd/lo-min/main.go`

**Step 1: Write the example**

Create `examples/lo/min/main.go`:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's MinInt32 — replaces 20+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"math"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	scalar := minScalar(data)
	spmd := minSPMD(data)

	fmt.Printf("Min: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 3 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
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

func minSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MaxInt32
	go for _, v := range data {
		if v < result {
			result = v
		}
	}
	return reduce.Min(result)
}
```

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-min/main.go` — same bench pattern as Task 1 but with min logic. Data: 1024 random-ish values (seeded via index math), verify scalar == SPMD.

```go
// run -goexperiment spmd

// Lo Min SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"math"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32((i*7 + 13) % 10000) // deterministic pseudo-random
	}
	data[777] = -999 // known minimum

	scalar := minScalar(data)
	spmd := minSPMD(data)
	fmt.Printf("Min: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != -999 {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			minScalar(data)
			minSPMD(data)
		}
	}

	scalarNs := bench(func() { minScalar(data) })
	spmdNs := bench(func() { minSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
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

func minSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MaxInt32
	go for _, v := range data {
		if v < result {
			result = v
		}
	}
	return reduce.Min(result)
}

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile, run, verify**

Same as Task 1 pattern. Expected: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/min/main.go test/integration/spmd/lo-min/main.go
git commit -m "Add lo Min SPMD example and E2E benchmark"
```

---

### Task 3: Max example + E2E test

**Files:**
- Create: `examples/lo/max/main.go`
- Create: `test/integration/spmd/lo-max/main.go`

**Step 1: Write the example**

Create `examples/lo/max/main.go` — mirror of min but with `>` comparison and `reduce.Max`:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's MaxInt32 — replaces 20+ hand-written SIMD functions
package main

import (
	"fmt"
	"lanes"
	"math"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	scalar := maxScalar(data)
	spmd := maxSPMD(data)

	fmt.Printf("Max: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 99 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
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

func maxSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MinInt32
	go for _, v := range data {
		if v > result {
			result = v
		}
	}
	return reduce.Max(result)
}
```

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-max/main.go` — same bench pattern, data[333] = 99999 as known maximum.

```go
// run -goexperiment spmd

// Lo Max SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"math"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32((i*7 + 13) % 10000)
	}
	data[333] = 99999 // known maximum

	scalar := maxScalar(data)
	spmd := maxSPMD(data)
	fmt.Printf("Max: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 99999 {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			maxScalar(data)
			maxSPMD(data)
		}
	}

	scalarNs := bench(func() { maxScalar(data) })
	spmdNs := bench(func() { maxSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
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

func maxSPMD(data []int32) int32 {
	var result lanes.Varying[int32] = math.MinInt32
	go for _, v := range data {
		if v > result {
			result = v
		}
	}
	return reduce.Max(result)
}

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile, run, verify**

Expected: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/max/main.go test/integration/spmd/lo-max/main.go
git commit -m "Add lo Max SPMD example and E2E benchmark"
```

---

### Task 4: Clamp example + E2E test

**Files:**
- Create: `examples/lo/clamp/main.go`
- Create: `test/integration/spmd/lo-clamp/main.go`

**Step 1: Write the example**

Create `examples/lo/clamp/main.go`. Clamp is a pure map (no reduction) — each element independently clamped to [min, max]:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's ClampInt32 — replaces 20+ hand-written SIMD functions
// Pure element-wise operation: no reduction needed
package main

import (
	"fmt"
	"reduce"
)

func main() {
	data := []int32{-5, 0, 3, 10, 15, 20, 25, 30}
	lo := int32(0)
	hi := int32(20)

	scalar := clampScalar(data, lo, hi)
	spmd := clampSPMD(data, lo, hi)
	expected := []int32{0, 0, 3, 10, 15, 20, 20, 20}

	fmt.Printf("Clamp: scalar=%v spmd=%v\n", scalar, spmd)
	pass := true
	for i := range expected {
		if scalar[i] != expected[i] || spmd[i] != expected[i] {
			pass = false
		}
	}
	if pass {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}

func clampScalar(data []int32, lo, hi int32) []int32 {
	result := make([]int32, len(data))
	for i, v := range data {
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

Note: Clamp doesn't use `reduce` — it's element-wise. The `go for` handles SIMD automatically via masking on the if/else branches.

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-clamp/main.go`:

```go
// run -goexperiment spmd

// Lo Clamp SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"os"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i - dataSize/2) // -512 to 511
	}
	lo := int32(-100)
	hi := int32(100)

	scalar := clampScalar(data, lo, hi)
	spmd := clampSPMD(data, lo, hi)

	pass := true
	for i := range data {
		if scalar[i] != spmd[i] {
			fmt.Printf("FAIL at index %d: scalar=%d spmd=%d\n", i, scalar[i], spmd[i])
			pass = false
			break
		}
	}
	if !pass {
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			clampScalar(data, lo, hi)
			clampSPMD(data, lo, hi)
		}
	}

	scalarNs := bench(func() { clampScalar(data, lo, hi) })
	spmdNs := bench(func() { clampSPMD(data, lo, hi) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func clampScalar(data []int32, lo, hi int32) []int32 {
	result := make([]int32, len(data))
	for i, v := range data {
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

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile, run, verify**

Expected: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/clamp/main.go test/integration/spmd/lo-clamp/main.go
git commit -m "Add lo Clamp SPMD example and E2E benchmark"
```

---

### Task 5: Contains example + E2E test

**Files:**
- Create: `examples/lo/contains/main.go`
- Create: `test/integration/spmd/lo-contains/main.go`

**Step 1: Write the example**

Create `examples/lo/contains/main.go`. Contains uses varying comparison + `reduce.Any` to check if any lane found a match. Uses early break for efficiency:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's Contains — replaces 30+ hand-written SIMD functions
// Uses reduce.Any to collapse varying bool to uniform bool
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int32{42, 17, 99, 3, 85, 61, 28, 73, 55, 11, 90, 36, 68, 5, 47, 22}

	// Test found case
	found := containsSPMD(data, 28)
	fmt.Printf("Contains 28: %v\n", found) // true

	// Test not-found case
	notFound := containsSPMD(data, 100)
	fmt.Printf("Contains 100: %v\n", notFound) // false

	scalarFound := containsScalar(data, 28)
	scalarNotFound := containsScalar(data, 100)
	if found == scalarFound && notFound == scalarNotFound && found && !notFound {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}

func containsScalar(data []int32, target int32) bool {
	for _, v := range data {
		if v == target {
			return true
		}
	}
	return false
}

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

Note: We use accumulation (`found = true` under varying condition) rather than `break` + `reduce.Any` inside the loop. The `break` variant would require `reduce.Any` inside the `go for` which converts varying to uniform — that's valid but the accumulation pattern is simpler and already proven.

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-contains/main.go`:

```go
// run -goexperiment spmd

// Lo Contains SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i)
	}

	// Test found (worst case: at end)
	sf := containsScalar(data, int32(dataSize-1))
	pf := containsSPMD(data, int32(dataSize-1))
	// Test not found
	snf := containsScalar(data, -1)
	pnf := containsSPMD(data, -1)

	fmt.Printf("Contains (found): scalar=%v spmd=%v\n", sf, pf)
	fmt.Printf("Contains (not found): scalar=%v spmd=%v\n", snf, pnf)
	if sf != pf || snf != pnf || !sf || snf {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	target := int32(dataSize - 1) // worst case: last element

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			containsScalar(data, target)
			containsSPMD(data, target)
		}
	}

	scalarNs := bench(func() { containsScalar(data, target) })
	spmdNs := bench(func() { containsSPMD(data, target) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func containsScalar(data []int32, target int32) bool {
	for _, v := range data {
		if v == target {
			return true
		}
	}
	return false
}

func containsSPMD(data []int32, target int32) bool {
	var found lanes.Varying[bool] = false
	go for _, v := range data {
		if v == target {
			found = true
		}
	}
	return reduce.Any(found)
}

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile, run, verify**

Expected: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/contains/main.go test/integration/spmd/lo-contains/main.go
git commit -m "Add lo Contains SPMD example and E2E benchmark"
```

---

### Task 6: Mean example + E2E test

**Files:**
- Create: `examples/lo/mean/main.go`
- Create: `test/integration/spmd/lo-mean/main.go`

**Step 1: Write the example**

Create `examples/lo/mean/main.go`. Mean = Sum / len. The sum is SPMD, the division is scalar:

```go
// run -goexperiment spmd

// SPMD equivalent of lo's MeanInt32 — sum is SPMD, division is scalar
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []int32{10, 20, 30, 40, 50, 60, 70, 80}

	scalar := meanScalar(data)
	spmd := meanSPMD(data)

	fmt.Printf("Mean: scalar=%d spmd=%d\n", scalar, spmd)
	if scalar != spmd || scalar != 45 {
		fmt.Println("FAIL")
	} else {
		fmt.Println("PASS")
	}
}

func meanScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total / int32(len(data))
}

func meanSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total) / int32(len(data))
}
```

**Step 2: Write the E2E test**

Create `test/integration/spmd/lo-mean/main.go`:

```go
// run -goexperiment spmd

// Lo Mean SPMD equivalent — E2E test with scalar vs SPMD benchmark
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
	"time"
)

const (
	dataSize   = 1024
	iterations = 10000
	warmup     = 3
	benchRuns  = 7
)

func main() {
	data := make([]int32, dataSize)
	for i := range data {
		data[i] = int32(i + 1)
	}

	scalar := meanScalar(data)
	spmd := meanSPMD(data)
	expected := int32(dataSize*(dataSize+1)/2) / int32(dataSize) // 512

	fmt.Printf("Mean: scalar=%d spmd=%d expected=%d\n", scalar, spmd, expected)
	if scalar != expected || spmd != expected {
		fmt.Println("FAIL: results mismatch")
		os.Exit(1)
	}
	fmt.Println("Correctness: PASS")

	for i := 0; i < warmup; i++ {
		for n := 0; n < iterations; n++ {
			meanScalar(data)
			meanSPMD(data)
		}
	}

	scalarNs := bench(func() { meanScalar(data) })
	spmdNs := bench(func() { meanSPMD(data) })

	fmt.Printf("Scalar: %dns/iter\n", scalarNs)
	fmt.Printf("SPMD:   %dns/iter\n", spmdNs)
	if spmdNs > 0 {
		fmt.Printf("Speedup: %.2fx\n", float64(scalarNs)/float64(spmdNs))
	}
}

func meanScalar(data []int32) int32 {
	var total int32
	for _, v := range data {
		total += v
	}
	return total / int32(len(data))
}

func meanSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total) / int32(len(data))
}

func bench(fn func()) int64 {
	times := make([]int64, benchRuns)
	for i := 0; i < benchRuns; i++ {
		start := time.Now()
		for n := 0; n < iterations; n++ {
			fn()
		}
		times[i] = time.Since(start).Nanoseconds() / int64(iterations)
	}
	min := times[0]
	for _, t := range times[1:] {
		if t < min {
			min = t
		}
	}
	return min
}
```

**Step 3: Compile, run, verify**

Expected: `Correctness: PASS`

**Step 4: Commit**

```bash
git add examples/lo/mean/main.go test/integration/spmd/lo-mean/main.go
git commit -m "Add lo Mean SPMD example and E2E benchmark"
```

---

### Task 7: Add E2E test entries to spmd-e2e-test.sh

**Files:**
- Modify: `test/e2e/spmd-e2e-test.sh` (after existing `integ_bit-counting` entry, around line 545)

**Step 1: Add the 6 new test entries**

Add after the last `test_compile_and_run` line in Level 5d:

```bash
test_compile_and_run "integ_lo-sum"      "$INTEG/lo-sum/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-min"      "$INTEG/lo-min/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-max"      "$INTEG/lo-max/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-clamp"    "$INTEG/lo-clamp/main.go"    "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-contains" "$INTEG/lo-contains/main.go" "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-mean"     "$INTEG/lo-mean/main.go"     "contains:Correctness: PASS" "" "-scheduler=none"
```

**Step 2: Run E2E suite**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Verify all 6 new tests appear as PASS in the summary.

**Step 3: Commit**

```bash
git add test/e2e/spmd-e2e-test.sh
git commit -m "Add lo SPMD examples to E2E test suite"
```

---

### Task 8: Write docs/lo-spmd-comparison.md

**Files:**
- Create: `docs/lo-spmd-comparison.md`

**Step 1: Write the analysis document**

The document should cover:

1. **Introduction** — lo's `exp/simd` approach and what SPMD offers as an alternative
2. **Operation-by-operation mapping** — table showing lo function count vs SPMD lines, with code snippets
3. **Code reduction metrics** — lo: ~2000+ lines across 16 files, SPMD: ~6 functions
4. **Architecture portability** — lo: amd64-only with `goexperiment.simd`; SPMD: any LLVM backend (WASM, ARM, x86)
5. **Limitation categories:**
   - **Callback-based operations** (SumBy, MeanBy): `func(T) R` callbacks cannot be vectorized. The iteratee runs scalar per-element. To vectorize, the callback's logic must be inlined into the `go for` body.
   - **Cross-lane algorithms**: Operations where each element's result depends on values in other lanes. Examples: sorted-set intersection (broadcast one element across all lanes, compare against the other set), prefix sum (each output depends on all prior outputs), base64 lookup (table lookup using varying indices). Our `*Within` ops (RotateWithin, ShiftLeftWithin, etc.) help but are limited to fixed shuffle patterns within sub-groups. Variable-index cross-lane access (full Swizzle) and prefix-scan patterns are not yet supported.
   - **Variable-length output**: Operations like filter/compact where the number of output elements varies per-lane. Requires compress-store (write only active lanes contiguously) which needs popcount + prefix-sum on the execution mask to compute scatter indices. Not currently expressible in SPMD.
   - **Runtime SIMD width dispatch**: Lo detects CPU features (AVX/AVX2/AVX512) at runtime and dispatches to the widest available implementation. SPMD compiles to a fixed SIMD width (e.g., 128-bit for WASM). Multi-width dispatch would require compiling multiple WASM binaries or future runtime width selection.
6. **Future work** — generic type support once union-type-generics bug is fixed
7. **Conclusion** — SPMD trades runtime width flexibility for code simplicity, safety, and portability

**Step 2: Commit**

```bash
git add docs/lo-spmd-comparison.md
git commit -m "Add lo vs SPMD comparison analysis document"
```

---

### Task 9: Final verification

**Step 1: Run full E2E suite**

```bash
bash test/e2e/spmd-e2e-test.sh
```

**Step 2: Verify all 6 lo tests pass in both compile and run phases**

Expected: All `integ_lo-*` tests show green PASS.

**Step 3: Check total counts**

The E2E summary should show increased pass counts (6 more run-pass, 6 more compile-pass).
