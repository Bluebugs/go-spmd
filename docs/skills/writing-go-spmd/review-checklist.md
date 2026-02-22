# Go SPMD Code Review Checklist

Review concerns ordered by impact. Focus on what the **compiler can't catch**: performance pitfalls, design decisions, and subtle correctness issues.

Based on ISPC's performance guide, Matt Pharr's retrospective, and Go SPMD project experience (mandelbrot ~2.98x speedup baseline).

---

## Critical: Memory Access Patterns

### Gather/Scatter Detection (10-100x slowdown)

**The single biggest SPMD performance killer.** Varying array indices generate gather/scatter instructions instead of contiguous vector loads/stores.

```go
// BAD: Varying index → gather (each lane loads from different address)
go for i := range len(data) {
    result := table[data[i]]       // data[i] is varying → gather
    output[indices[i]] = result    // indices[i] is varying → scatter
}

// GOOD: Contiguous index → vector load/store
go for i := range len(data) {
    result := data[i]              // i is contiguous [0,1,2,3] → vector load
    output[i] = result * 2         // i is contiguous → vector store
}
```

**Review question:** For every `array[expr]` inside `go for`, is `expr` contiguous (based on loop variable) or varying (data-dependent)?

**Compiler detection:** TinyGo's `spmdAnalyzeContiguousIndex` traces through `BinOp ADD` chains to detect `scalar + iterationVar` patterns. Complex index expressions defeat this analysis.

```go
// Compiler CAN detect as contiguous:
data[i]                    // direct loop variable
data[i + offset]           // scalar + loop variable
data[base + i]             // scalar + loop variable

// Compiler CANNOT detect (forces gather):
data[computeIndex(i)]      // function call hides pattern
data[lookupTable[i]]       // double indirection
data[i * stride]           // multiplication (not addition)
```

### AOS vs SOA Layout (10-20x difference)

Array of Structures (AOS) with varying index causes gathers for every field access. Structure of Arrays (SOA) keeps fields contiguous.

```go
// BAD: AOS layout — accessing .X scatters across Y,Z values in memory
type Particle struct { X, Y, Z float32 }
particles := []Particle{...}

go for i := range len(particles) {
    x := particles[i].X    // gather: memory is [X,Y,Z,X,Y,Z,...]
    y := particles[i].Y    // gather: must skip X,Z to reach each Y
    z := particles[i].Z    // gather: must skip X,Y to reach each Z
}

// GOOD: SOA layout — each field array is contiguous
type ParticlesSOA struct {
    X, Y, Z []float32
}

go for i := range len(p.X) {
    x := p.X[i]            // vector load: memory is [X,X,X,X,...]
    y := p.Y[i]            // vector load: memory is [Y,Y,Y,Y,...]
    z := p.Z[i]            // vector load: memory is [Z,Z,Z,Z,...]
}
```

**Review question:** Are structs accessed with varying indices inside `go for`? Would SOA layout be feasible?

**When AOS is acceptable:**
- Struct accessed with uniform index (broadcast, not gather)
- Struct fields accessed together and struct is small (cache line fits both)
- Data shared with non-SPMD code where AOS is natural

---

## High: Control Flow Efficiency

### Varying Condition Coherence

Both branches of a varying `if` always execute (with masks). Deep nesting compounds the cost -- each level potentially halves active lanes.

```go
// BAD: Deep varying nesting — most lanes idle most of the time
go for i := range len(data) {
    if data[i] > 10 {              // ~50% lanes active
        if data[i] > 100 {         // ~25% lanes active
            if data[i] > 1000 {    // ~12% lanes active
                heavyWork(data[i])  // 88% of compute wasted
            }
        }
    }
}

// BETTER: Flatten conditions
go for i := range len(data) {
    if data[i] > 1000 {
        heavyWork(data[i])          // only one masked branch
    } else if data[i] > 100 {
        mediumWork(data[i])
    } else if data[i] > 10 {
        lightWork(data[i])
    }
}

// BEST: Sort/bucket data first, then process coherent groups
```

**Review question:** How many levels of varying `if` nesting exist? Is the data naturally coherent (adjacent elements have similar values)?

**Coherence improvement techniques:**
- Sort or bucket input data so adjacent elements follow similar paths
- Use tiled iteration (2D blocks) for spatial coherence
- Split into uniform pre-check + varying detail pass

### Reduction Inside Loops (Anti-Pattern)

Cross-lane reductions are expensive. Accumulate per-lane, reduce once after the loop.

```go
// BAD: Reduce every iteration — cross-lane operation per element
var sum int
go for i := range len(data) {
    sum += reduce.Add(data[i])     // N reductions!
}

// GOOD: Accumulate per-lane, single reduce at end
var sum lanes.Varying[int]
go for i := range len(data) {
    sum += data[i]                 // per-lane add (vector instruction)
}
total := reduce.Add(sum)           // 1 reduction

// ACCEPTABLE: reduce.Any for early exit (amortized by loop savings)
go for i := range len(data) {
    if reduce.Any(data[i] == target) {   // OK: enables early termination
        return i + reduce.FindFirstSet(data[i] == target)
    }
}
```

**Review question:** Is any `reduce.*` call inside a `go for` body? Can it be moved outside?

**Exception:** `reduce.Any`/`reduce.All` for early exit is acceptable -- the cost is amortized by avoiding remaining iterations.

### Cross-Lane Operation Frequency

`lanes.Broadcast`, `lanes.RotateWithin`, `lanes.SwizzleWithin`, `lanes.ShiftLeftWithin`, `lanes.ShiftRightWithin` are all cross-lane operations with non-trivial cost (shuffle instructions).

**Review question:** How many cross-lane operations per iteration? Could the algorithm be restructured to reduce them?

**Acceptable:** 1-3 cross-lane ops per iteration (base64 decoder uses 5, but each does substantial work).
**Suspicious:** Cross-lane ops inside nested loops, or cross-lane ops that could be hoisted out.

---

## Medium: Variable Scope and Masking

### Broad Variable Scope Increases Masking Cost

Variables declared at function scope need masked stores across all control flow paths. Variables declared in narrow scope only need masks within that scope.

```go
// BAD: result allocated for entire function, masked in both branches
func process(data []int) []int {
    var temp lanes.Varying[int]    // lives across both branches
    go for i := range len(data) {
        if data[i] > 0 {
            temp = data[i] * 2     // masked store
        } else {
            temp = -data[i]        // masked store
        }
        output[i] = temp
    }
    return output
}

// BETTER: temp only exists where needed (compiler may optimize anyway)
func process(data []int) []int {
    go for i := range len(data) {
        if data[i] > 0 {
            output[i] = data[i] * 2
        } else {
            output[i] = -data[i]
        }
    }
    return output
}
```

**Review question:** Are varying variables declared at broader scope than their actual use?

### Debug Output in Tight Loops

`fmt.Printf` with `%v` on varying types triggers `reduce.From()` (varying->array conversion) every call. Avoid inside performance-critical loops.

```go
// BAD: Printf with varying conversion every iteration
go for i := range len(data) {
    fmt.Printf("Processing: %v\n", data[i])  // reduce.From per iteration
    result[i] = process(data[i])
}

// GOOD: Remove debug output, or gate behind uniform condition
go for i := range len(data) {
    result[i] = process(data[i])
}
// Debug after loop if needed
fmt.Printf("Final results: first batch = %v\n", result[0:4])
```

**Review question:** Are there `fmt.Printf`/`fmt.Println` calls with varying arguments inside `go for`?

---

## Subtle Correctness Issues

### Continue Semantics: Only Active Lanes Execute Past Continue

After `continue` in a varying context, the remaining loop body executes only for lanes that did NOT continue. Variables written after `continue` are masked -- they retain their old values for lanes that continued.

```go
go for i := range len(data) {
    result[i] = 0                    // all lanes write 0

    if data[i] < 0 {
        continue                     // lanes with negative data skip ahead
    }

    result[i] = process(data[i])     // ONLY non-negative lanes execute this
    // Negative lanes still have result[i] = 0 from above
}
```

**Review question:** After a `continue` under varying condition, are there writes that assume all lanes are active?

### Break in go for is Per-Lane

`break` inside a varying condition in `go for` sets the break mask for matching lanes. The loop continues for remaining active lanes until ALL lanes have broken.

```go
go for i := range len(data) {
    if data[i] == sentinel {
        break    // ONLY lanes matching sentinel break
                 // Other lanes keep iterating!
    }
    process(data[i])
}
// Execution continues here when ALL lanes have broken or loop ends
```

This is different from regular Go `break` which exits the entire loop immediately. The `go for` loop only fully exits when `reduce.All(breakMask)` is true (all lanes broken) or the range is exhausted.

**Review question:** Does the developer understand that `break` in `go for` is per-lane? Is the intent to break all lanes (use `reduce.Any` + uniform `break` instead)?

### SPMD Function with All-Zero Mask

If an SPMD function (varying params) is called when no lanes are active, the return value is uninitialized/garbage. This can happen in deeply nested masked control flow.

```go
func compute(x lanes.Varying[float32]) lanes.Varying[float32] {
    // If called with all-zero mask, nothing executes, return is garbage
    return x * x + 1.0
}

go for i := range len(data) {
    if data[i] > 0 {                    // some lanes active
        if data[i] > 1000 {             // maybe zero lanes active
            result[i] = compute(data[i]) // if zero lanes: garbage!
        }
    }
}
```

In practice, the masking system prevents using garbage values (inactive lanes don't write to output). But if the result is used in a reduction outside the masked scope, it could leak.

**Review question:** Could an SPMD function be called with all lanes masked off? Is the result used outside the mask scope?

### Memory Aliasing Between Parameters

When the same array is passed as both input and output to an SPMD function, the compiler may reorder loads and stores incorrectly (it assumes no aliasing).

```go
// DANGEROUS: data is both read and written
func doubleInPlace(data []lanes.Varying[int]) {
    go for i := range len(data) {
        data[i] = data[i] * 2    // read and write same location
    }
}

// SAFE: separate input and output
func double(dst, src []int) {
    go for i := range len(src) {
        dst[i] = src[i] * 2      // different arrays
    }
}
```

**Review question:** Are there in-place SPMD operations where the same memory is read and written? Could aliasing cause issues?

---

## Testing Concerns

### Dual-Mode Verification

Every SPMD example should produce identical output in SIMD and scalar modes. Different lane counts can expose masking bugs (tail handling, break mask accumulation).

```bash
# SIMD mode
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o test-simd.wasm main.go
# Scalar mode
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o test-scalar.wasm main.go
# Compare outputs
diff <(node run-wasm.mjs test-simd.wasm) <(node run-wasm.mjs test-scalar.wasm)
```

**Review question:** Has the code been tested in both SIMD and scalar modes with identical results?

### Mask Edge Case Testing

SPMD code should be tested with inputs that exercise:
- **Empty mask:** Zero-length input (no iterations)
- **Partial mask:** Input length not a multiple of lane count (tail masking)
- **Full mask:** All lanes active throughout (baseline correctness)
- **Single lane:** Input length = 1 (maximum masking)

```go
// Test with these input sizes for 4-lane int32:
testSizes := []int{0, 1, 2, 3, 4, 5, 7, 8, 15, 16, 17, 100}
```

**Review question:** Does the test suite include input sizes that aren't multiples of the lane count?

### Performance Regression

Small SPMD code changes can cause large performance swings (gather introduced, coherence lost, vectorization defeated). Benchmark before and after.

**Review question:** For performance-critical SPMD code, are there before/after benchmarks? (Mandelbrot baseline: ~2.98x SPMD speedup.)

---

## Quick Review Scan

When reviewing Go SPMD code, check these in order:

| # | Check | Severity | How to Spot |
|---|-------|----------|-------------|
| 1 | Varying array index inside `go for`? | Critical | `array[expr]` where `expr` depends on data, not loop var |
| 2 | Struct field access with varying index? | Critical | `structs[i].Field` inside `go for` (AOS gather) |
| 3 | `reduce.*` call inside `go for` body? | High | Any reduction except `reduce.Any`/`All` for early exit |
| 4 | Deep varying `if` nesting (3+ levels)? | High | Count nested `if` blocks with varying conditions |
| 5 | Cross-lane ops inside nested loops? | High | `SwizzleWithin`/`RotateWithin` in inner loop |
| 6 | `fmt.Printf` with `%v` on varying in loop? | Medium | Debug output in hot path |
| 7 | Variables declared broader than needed? | Medium | Varying var at function scope, used in one branch |
| 8 | Writes after `continue` assume all lanes? | Subtle | Code after `continue` under varying condition |
| 9 | `break` in `go for` intended as all-lanes? | Subtle | `break` under varying condition (per-lane, not whole-loop) |
| 10 | In-place read+write of same array? | Subtle | `data[i] = f(data[i])` in SPMD context |
| 11 | Non-multiple-of-lanes test inputs? | Testing | Test sizes: 0, 1, 3, 4, 5, 7, 15, 16, 17 |
| 12 | SIMD vs scalar output match? | Testing | Both modes produce identical results |
