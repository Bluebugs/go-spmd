# Go SPMD Patterns

10 battle-tested patterns extracted from real working examples in the SPMD project.

---

## Pattern 1: Parallel Accumulation + Reduce

**Source:** `examples/simple-sum/main.go`

Initialize a varying accumulator, add in parallel across lanes, reduce at the end.

```go
func sum(data []int) int {
    var total lanes.Varying[int] = 0  // broadcast 0 to all lanes

    go for _, value := range data {
        total += value  // each lane adds its own element
    }

    return reduce.Add(total)  // sum all lanes
}
```

**Don't:** Assign `total` to a regular `int` inside the loop. Use `reduce.Add` after the loop.

---

## Pattern 2: Conditional Per-Lane Execution

**Source:** `examples/odd-even/main.go`

Use if/else inside `go for` for per-lane conditional logic. The compiler generates masked execution.

```go
func oddEvenCount(data []int) (int, int) {
    var odd, even lanes.Varying[int]

    go for _, value := range data {
        if value&1 == 1 {   // varying condition -- per-lane mask
            odd++
        } else {
            even++
        }
    }

    return reduce.Add(odd), reduce.Add(even)
}
```

**Key:** Both branches execute (with masks). The compiler generates `select(mask, trueVal, falseVal)`.

---

## Pattern 3: Early Exit with reduce.Any

**Source:** `examples/printf-verbs/main.go`

Convert a varying condition to uniform via `reduce.Any()` to enable safe `return`/`break`.

```go
func findFirstVerb(s string) int {
    i := 0  // uniform position tracker

    go for _, c := range []byte(s) {
        check := c == '%'   // varying bool

        if reduce.Any(check) {   // uniform bool -- safe to return
            return i + reduce.FindFirstSet(check)
        }

        i += lanes.Count(c)
    }

    return len(s)
}
```

**Key:** `reduce.Any()` returns uniform `bool`. The `return` is under a uniform condition, so it's allowed.

**Don't:** Write `if check { return }` -- that's a varying condition; return is forbidden.

---

## Pattern 4: Position Tracking with lanes.Count

**Source:** `examples/printf-verbs/main.go`, `examples/ipv4-parser/main.go`

Track absolute position in the input by incrementing a uniform counter by `lanes.Count()` each iteration.

```go
i := 0                          // uniform position
go for _, c := range data {
    // i is the base position of this SIMD chunk
    // actual position of a match = i + reduce.FindFirstSet(cond)
    i += lanes.Count(c)         // advance by number of lanes processed
}
```

**Key:** `lanes.Count()` returns the number of elements processed per `go for` iteration (e.g., 16 for uint8, 4 for int32 on WASM128).

---

## Pattern 5: Error Extraction from Varying

**Source:** `examples/ipv4-parser/main.go`

When varying lanes can independently produce errors, extract the first one using `reduce.From` + `reduce.FindFirstSet`.

```go
go for field, value := range inputs {
    hasError := validate(value)  // varying bool

    if reduce.Any(hasError) {    // uniform -- safe to return
        errors := reduce.From(hasError)   // varying -> []bool
        return errors[reduce.FindFirstSet(hasError)]
    }
}
```

**Key:** `reduce.From()` converts varying to a uniform array. `FindFirstSet()` gives the lane index of the first error.

---

## Pattern 6: Bitmask Creation with reduce.Mask

**Source:** `examples/ipv4-parser/main.go`

Extract per-lane boolean results as a bitmask for post-processing outside the SPMD loop.

```go
var mask uint16
loop := 0

go for _, isDot := range dotMask {
    mask |= uint16(reduce.Mask(isDot)) << loop   // bit N = lane N
    loop += lanes.Count(isDot)
}

// mask now has one bit per input position where isDot was true
dotCount := bits.OnesCount16(mask)
```

**Key:** `reduce.Mask()` produces an `int` where bit N corresponds to lane N's boolean value.

---

## Pattern 7: Byte/String Transform

**Source:** `examples/to-upper/main.go`

Process byte slices element-wise with conditional transformation.

```go
func toUpper(s []byte) []byte {
    b := make([]byte, len(s))
    go for i, c := range s {     // i is varying, c is varying
        if 'a' <= c && c <= 'z' {
            b[i] = c - ('a' - 'A')
        } else {
            b[i] = c
        }
    }
    return b
}
```

**Key:** `go for i, c := range []byte` -- both `i` (index) and `c` (value) are varying. Memory access `b[i]` is contiguous (efficient vector store).

---

## Pattern 8: Cross-Lane Table Lookup (SwizzleWithin)

**Source:** `examples/base64-decoder/main.go`

Use `SwizzleWithin` for parallel table lookups where each lane reads a different position from a small table.

```go
// Load table as varying (each lane holds one table entry)
offsetTable := []byte{255, 16, 19, 4, 191, 191, 185, 185}
tableVec := lanes.From(offsetTable)

// Each lane uses its own hash to index into the table
hashes := lanes.ShiftRightWithin(ascii, 4, 4)  // compute per-lane index
offsets := lanes.SwizzleWithin(tableVec, hashes, 4)  // parallel lookup

// Apply offsets
sextets := ascii + offsets
```

**Key:** `SwizzleWithin(table, indices, groupSize)` -- each lane reads `table[indices[lane]]` within its group. `groupSize` must be compile-time constant.

---

## Pattern 9: Neighbor Data Exchange (RotateWithin)

**Source:** `examples/base64-decoder/main.go`

Use `RotateWithin` to access adjacent lane data for bit packing and data alignment.

```go
// Shift data within 4-element groups
shiftPattern := lanes.From([]uint16{2, 4, 6, 8})
shifted := lanes.ShiftLeftWithin(sextets, shiftPattern, 4)

// Split into high and low bytes
shiftedLo := lanes.Varying[byte](shifted)
shiftedHi := lanes.Varying[byte](lanes.ShiftRightWithin(shifted, 8, 4))

// Rotate high bytes by 1 position within each group
// Lane 0 gets lane 3's data, Lane 1 gets lane 0's data, etc.
decodedChunks := shiftedLo | lanes.RotateWithin(shiftedHi, 1, 4)
```

**Key:** `RotateWithin(v, offset, groupSize)` enables each lane to access a neighbor's data. Combined with `ShiftLeftWithin`/`ShiftRightWithin` for bit-level coordination between lanes.

**Don't:** Use full-width `Rotate`/`Swizzle` (deferred). Use `*Within` variants for portable algorithms.

---

## Pattern 10: SPMD Function with Per-Lane Break

**Source:** `examples/mandelbrot/main.go`

Define a function with varying parameters (SPMD function) that uses regular `for` loops with per-lane break tracking.

```go
// SPMD function: receives implicit mask as first parameter in SSA
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int) lanes.Varying[int] {
    var zRe, zIm lanes.Varying[float32] = cRe, cIm
    var iterations lanes.Varying[int] = maxIter

    for iter := range maxIter {     // regular for loop (uniform iteration)
        magSquared := zRe*zRe + zIm*zIm
        diverged := magSquared > 4.0

        if diverged {               // varying condition
            iterations = iter       // per-lane assignment via mask
            break                   // per-lane break: sets break mask
        }

        newRe := zRe*zRe - zIm*zIm
        newIm := 2.0 * zRe * zIm
        zRe = cRe + newRe
        zIm = cIm + newIm
    }

    return iterations
}

// Called from go for loop
func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int, output []int) {
    dx := (x1 - x0) / float32(width)
    dy := (y1 - y0) / float32(height)

    for j := 0; j < height; j++ {          // uniform outer loop
        y := y0 + float32(j)*dy

        go for i := range width {           // SPMD inner loop
            x := x0 + lanes.Varying[float32](i)*dx
            iterations := mandelSPMD(x, y, maxIter)
            output[j*width+i] = iterations
        }
    }
}
```

**Key points:**
- SPMD functions (varying params) CANNOT contain `go for` -- use regular `for` inside
- `break` inside varying `if` in a regular `for` (inside SPMD func) uses per-lane break mask
- The compiler tracks which lanes have broken; loop exits when `reduce.All(breakMask)` is true
- Call SPMD functions from `go for` loops; mask is passed implicitly

---

## Pattern Summary

| # | When to Use | Key Operations |
|---|-------------|----------------|
| 1 | Sum/accumulate array | `go for` + `reduce.Add` |
| 2 | Per-element conditional | `if/else` in `go for` (auto-masked) |
| 3 | Find first match | `reduce.Any` + `reduce.FindFirstSet` |
| 4 | Track absolute position | Uniform `i += lanes.Count()` |
| 5 | First error from parallel validation | `reduce.From` + `FindFirstSet` |
| 6 | Extract lane-level bitmask | `reduce.Mask` |
| 7 | Transform byte arrays | `go for i, c := range []byte` |
| 8 | Parallel table lookup | `SwizzleWithin` |
| 9 | Adjacent lane data | `RotateWithin` + `ShiftLeftWithin` |
| 10 | Iterative per-lane computation | SPMD func with regular `for` + break mask |
