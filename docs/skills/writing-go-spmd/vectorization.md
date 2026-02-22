# Vectorization Guide: From SIMD Assembly to Go SPMD

This reference bridges raw SIMD/ISPC knowledge to Go SPMD's `go for` programming model. Use it when designing new vectorized algorithms or porting existing SIMD code.

## ISPC to Go SPMD Translation

| ISPC | Go SPMD | Notes |
|------|---------|-------|
| `foreach (i = 0 ... N)` | `go for i := range N` | Same semantics: i is varying |
| `uniform int x` | `var x int` | Go defaults to uniform |
| `varying float y` | `var y lanes.Varying[float32]` | Package-based type |
| `programIndex` | `lanes.Index()` | Current lane index |
| `programCount` | `lanes.Count[T]()` | Type-parameterized |
| `reduce_add(v)` | `reduce.Add(v)` | Horizontal sum |
| `any(mask)` / `all(mask)` | `reduce.Any(m)` / `reduce.All(m)` | Mask reduction |
| `extract(v, lane)` | `reduce.From(v)[lane]` | Extract one lane |
| `broadcast(v, lane)` | `lanes.Broadcast(v, lane)` | Replicate one lane |
| `shuffle(v, idx)` | `lanes.SwizzleWithin(v, idx, N)` | Permute within groups |
| `rotate(v, offset)` | `lanes.RotateWithin(v, offset, N)` | Rotate within groups |
| `cif` (coherent if) | regular `if` with uniform condition | Compiler auto-detects |
| `unmasked {}` | N/A | Implicit outside `go for` |
| `launch` / `sync` | goroutines (`go func()`) | Different parallelism model |
| `soa<N>` qualifier | Manual (struct of `Varying[T]`) | No automatic AOS/SOA |
| `#pragma ignore warning` | N/A | Go has no equivalent |

### Key Difference: ISPC's `foreach` vs Go's `go for`

ISPC's `foreach` and Go's `go for` have identical SPMD semantics: the loop body executes once per SIMD chunk, with the iteration variable being varying (different value per lane). Both handle tail masking automatically when the range isn't a multiple of lane count.

```
// ISPC                              // Go SPMD
foreach (i = 0 ... width) {         go for i := range width {
    float x = data[i];                  x := data[i]
    result[i] = x * x;                 result[i] = x * x
}                                    }
```

### Key Difference: ISPC allows return/break in varying context

ISPC allows `return` and `break` under varying conditions (using per-lane return/break masks). Go SPMD follows a **stricter** approach: return/break are forbidden under varying conditions in `go for` loops. Use `reduce.Any()`/`reduce.All()` to convert to uniform conditions, or use `continue` (always allowed).

## Assembly-to-SPMD Mapping

How raw SIMD instructions translate to readable Go SPMD constructs:

| SIMD Instruction | What It Does | Go SPMD Equivalent |
|------------------|-------------|---------------------|
| `vpcmpeqb` (SSE/AVX) | Compare 16-32 bytes simultaneously | `ch == target` inside `go for` |
| `vpshufb` (SSSE3/AVX2) | Table lookup via byte shuffle | `lanes.SwizzleWithin(table, idx, N)` |
| `vpalignr` (SSSE3) | Byte rotation across registers | `lanes.RotateWithin(v, offset, N)` |
| `vpmovmskb` (SSE2) | Extract bitmask from byte comparisons | `reduce.Mask(boolVarying)` |
| `vpmaddubsw` (SSSE3) | Multiply-add bytes to words | Arithmetic in `go for` body |
| `vpblendvb` (SSE4.1) | Conditional byte blend | `if varyingCond { a } else { b }` |
| `vpgatherdd` (AVX2) | Gather load from scattered addresses | `data[varyingIndex]` (auto-detected) |
| `vpscatterdd` (AVX-512) | Scatter store to scattered addresses | `result[varyingIndex] = v` (auto-detected) |
| `popcnt` / `tzcnt` | Count bits / find first set | `reduce.Count()` / `reduce.FindFirstSet()` |
| `vptest` / `vtestps` | Test if all/any bits set | `reduce.Any()` / `reduce.All()` |
| `v128.any_true` (WASM) | Test if any lane true | `reduce.Any()` (native on WASM) |
| `v128.all_true` (WASM) | Test if all lanes true | `reduce.All()` (native on WASM) |
| `pclmulqdq` (CLMUL) | Carry-less multiply (prefix XOR) | No direct equivalent -- see Limitations |

**Key insight:** SPMD replaces manual shuffle/blend/permute orchestration with readable `if`/`else` and array indexing. The compiler generates the same instructions.

## Algorithm Suitability Matrix

| Algorithm | SPMD Fit | Speedup | Key SPMD Operations | Key Challenge |
|-----------|----------|---------|---------------------|---------------|
| **Character transform** (toupper, tolower) | Excellent | 3-5x | masked arithmetic | None |
| **String scanning** (memchr, find pattern) | Excellent | 4-16x | `reduce.Any` + `FindFirstSet` | First-match is cross-lane reduction |
| **Base64 encode/decode** | Excellent | 2-8x | `SwizzleWithin`, `RotateWithin` | 4:3 byte grouping |
| **Hex encode/decode** | Excellent | 3-6x | table lookup, shift | None |
| **Texture compression** (BC1-7, ASTC, ETC1) | Excellent | 3-50x | block-independent, dot product | None -- embarrassingly parallel |
| **Mandelbrot / fractals** | Excellent | 3-4x | break mask, `reduce.All` | Varying escape counts |
| **Stencil / convolution** | Good | 2-3x | contiguous loads, uniform kernel | Boundary handling |
| **Ray tracing** (packet tracing) | Good | 2-6x | varying mask, BVH traversal | Coherence-dependent |
| **IPv4/packet parsing** | Good | 2-3x | `reduce.Mask`, `FindFirstSet` | Variable-length fields |
| **JSON parsing** (value extraction) | Good | 2-5x | per-value independent parsing | Stage 1 needs bitmask ops |
| **Checksum computation** (batch) | Good | 2-4x | accumulate + fold | Single packet is just reduction |
| **Packet classification** | Good | 1.5-3x | varying break mask | Varying rule match depth |
| **Histogram** | Medium | 1.5-2x | privatization + reduce merge | Write conflicts between lanes |
| **UTF-8 validation** | Medium | 2-3x | cross-lane shift, lookup | Sequential byte dependencies |
| **Prefix sum / scan** | Medium | 1.5-2x | `ShiftWithin` chains | Serial dependency chain |
| **Sorting networks** | Poor | 1-1.5x | fundamentally cross-lane | Data movement between lanes |
| **Tree traversal** | Poor | <1.5x | pointer chasing | Not data-parallel |

## Algorithm Design Cookbook

When vectorizing a new algorithm, follow these 6 steps:

### Step 1: Identify the Data-Parallel Dimension

What iterates independently? Pixels, packets, characters, blocks, array elements?

```
Good: "process each pixel independently" -> go for pixel := range width*height
Good: "process each 4x4 block independently" -> go for block := range numBlocks
Bad:  "each step depends on the previous step" -> not SPMD-friendly
```

### Step 2: Classify Variables as Uniform or Varying

- **Uniform:** Loop bounds, thresholds, constants, configuration, shared state
- **Varying:** Per-element data, indices, intermediate results, conditions on per-element data

```go
func process(data []int, threshold int) {  // threshold is uniform
    go for i, v := range data {             // i and v are varying
        if v > threshold {                  // varying condition (v) vs uniform (threshold)
            result[i] = v * 2              // varying assignment
        }
    }
}
```

### Step 3: Map Control Flow

Which branches depend on varying data?

- **Uniform conditions** (`if threshold < 0`): All lanes agree. `return`/`break` allowed.
- **Varying conditions** (`if data[i] > 0`): Lanes diverge. Only `continue` allowed. Both branches execute with masks.
- **Reduction conditions** (`if reduce.Any(cond)`): Converts varying to uniform. `return`/`break` allowed.

### Step 4: Identify Cross-Lane Needs

Does the algorithm need data from other lanes?

| Need | Solution | Example |
|------|----------|---------|
| None (independent) | Direct `go for` | Mandelbrot, texture compression |
| Table lookup (small) | `SwizzleWithin` | Base64 character validation |
| Neighbor access | `RotateWithin` | Base64 bit packing |
| Bit extraction | `ShiftLeftWithin`/`ShiftRightWithin` | Base64 perfect hash |
| First match position | `reduce.FindFirstSet` | String search |
| All/any test | `reduce.All`/`reduce.Any` | Early termination |
| Full reduction | `reduce.Add`/`Max`/`Min` | Sum, max element |
| Prefix operation | Not directly supported | Use block decomposition |

### Step 5: Check Memory Access Patterns

- **Contiguous** (`data[i]` where i = [0,1,2,3]): Best case. Single vector load/store.
- **Strided** (`data[i*4]`): Moderate. May use gather/scatter.
- **Random** (`data[indices[i]]`): Worst case. Gather/scatter required.
- **AOS** (Array of Structs): Deinterleave needed. Consider SOA layout.

### Step 6: Research Existing ISPC/SIMD Implementations

Search for: `"ISPC" + algorithm name` or `"SIMD" + algorithm name` or `"vectorized" + algorithm name`.

## Research Starting Points

### ISPC Examples (in workspace at `ispc/examples/cpu/`)

| Directory | Algorithm | SPMD Pattern |
|-----------|-----------|-------------|
| `mandelbrot/` | Fractal computation | Per-pixel divergent iteration with break mask |
| `aobench/` | Ambient occlusion ray tracing | Packet tracing with nested varying conditions |
| `stencil/` | 3D stencil computation | Uniform inner loops, contiguous access |
| `volume_rendering/` | Volume ray casting | Per-ray varying traversal |
| `sort/` | Bitonic sort | Cross-lane compare-and-swap (poor SPMD fit) |
| `noise/` | Perlin noise | Table lookup + interpolation |
| `options/` | Black-Scholes pricing | Embarrassingly parallel math |

### External SIMD Libraries and Papers

**Text/String Processing:**
- [simdjson](https://github.com/simdjson/simdjson) -- SIMD JSON parser. Stage 1 (structural detection) uses `vpshufb` classification + `pclmulqdq` prefix-XOR. Stage 2 (value parsing) maps well to SPMD.
- [simdutf](https://github.com/simdutf/simdutf) -- SIMD UTF-8/16/32 validation and transcoding
- [memchr](https://github.com/BurntSushi/memchr) -- Optimized string search using SIMD comparisons

**Data Encoding:**
- [SIMD base64](http://0x80.pl/notesen/2016-01-17-sse-base64-decoding.html) -- Wojciech Mula's base64 SIMD techniques. Uses `vpshufb` for lookup, `vpmaddubsw`/`vpmaddwd` for bit packing.
- [SIMD base64 design walkthrough](https://mcyoung.xyz/2023/11/27/simd-base64/) -- Miguel Young de la Sota's design, basis for Go SPMD's base64 decoder example

**Network/Parsing:**
- [SIMD IPv4 parsing](http://0x80.pl/notesen/2023-04-09-faster-parse-ipv4.html) -- Wojciech Mula's approach, basis for Go SPMD's IPv4 parser example
- [Validating UTF-8 in less than one instruction per byte](https://arxiv.org/pdf/2010.03090) -- Range-based parallel validation

**Image/Graphics:**
- [ISPCTextureCompressor](https://github.com/GameTechDev/ISPCTextureCompressor) -- BC6H, BC7, ASTC, ETC1 texture compression. Each 4x4 block is one SPMD lane. Achieves 654 Mpix/s for DXTC.
- [ISPC Performance Guide](https://ispc.github.io/perfguide.html) -- Detailed optimization strategies for SPMD code

**Academic:**
- [ispc: A SPMD Compiler for High-Performance CPU Programming](https://pharr.org/matt/assets/ispc.pdf) -- Matt Pharr & William Mark's original paper
- [Predicated SSA](https://cseweb.ucsd.edu/~calder/papers/PACT-99-PSSA.pdf) -- Foundation for mask-based execution

**Data Structures:**
- [Rethinking SIMD Vectorization for In-Memory Databases](http://www.cs.columbia.edu/~orestis/sigmod15.pdf) -- SIMD scan, hash join, sort
- [SIMD prefix sum](https://en.algorithmica.org/hpc/algorithms/prefix/) -- Block-based decomposition for scan operations

## Worked Example: JSON Structural Scanner

Shows what maps cleanly to SPMD and what doesn't.

### Stage 1a: Character Classification (SPMD-friendly)

```go
// Each lane classifies one byte -- compiler generates vpcmpeqb
func classifyChars(input []byte) (structural, quotes []bool) {
    structural = make([]bool, len(input))
    quotes = make([]bool, len(input))

    go for i, ch := range input {
        structural[i] = ch == '{' || ch == '}' || ch == '[' ||
                        ch == ']' || ch == ':' || ch == ','
        quotes[i] = ch == '"'
    }
    return
}
```

This maps perfectly: independent per-byte classification, contiguous memory access.

### Stage 1b: Quote Range Detection (NOT SPMD-friendly)

```go
// Prefix-XOR: determines which bytes are "inside" a string
// In assembly: pclmulqdq(quote_bitmap, all_ones) -- O(1) per 64 bytes
// In SPMD: no direct equivalent for prefix operations
//
// Workaround: block-based decomposition
// 1. Process each 64-byte block independently (go for over blocks)
// 2. Each block computes local prefix-XOR using ShiftWithin chains
// 3. Propagate block boundaries sequentially (uniform loop)
```

**Lesson:** Prefix/scan operations are inherently sequential. SPMD handles them via block decomposition (parallel within blocks, sequential between blocks), but it's less efficient than dedicated `pclmulqdq` instructions.

### Stage 2: Value Parsing (SPMD-friendly)

```go
// Parse JSON values at known structural positions
func parseValues(input []byte, positions []int) []Value {
    results := make([]Value, len(positions))

    go for i, pos := range positions {
        // Each lane parses a different JSON value at a different position
        results[i] = parseValueAt(input, pos)
    }

    return results
}
```

Independent value parsing across structural positions is embarrassingly parallel.

## Worked Example: Texture Compression

Block-based compression is the canonical SPMD use case.

```go
// Each lane compresses one 4x4 pixel block
func compressBC1(image []RGBA, width, height int) []BC1Block {
    blocksX := width / 4
    blocksY := height / 4
    numBlocks := blocksX * blocksY
    output := make([]BC1Block, numBlocks)

    go for blockIdx := range numBlocks {
        // Load 16 pixels for this block (each lane loads different block)
        bx := blockIdx % blocksX
        by := blockIdx / blocksX
        pixels := loadBlock4x4(image, bx*4, by*4, width)

        // Find min/max colors (PCA-based endpoint selection)
        minColor, maxColor := findEndpoints(pixels)

        // Assign each pixel to nearest interpolated color
        indices := assignIndices(pixels, minColor, maxColor)

        // Pack into BC1 format
        output[blockIdx] = packBC1(minColor, maxColor, indices)
    }

    return output
}
```

**Why this is ideal for SPMD:**
- Each block is completely independent (no cross-lane communication)
- All blocks follow the same algorithm (uniform control flow)
- `findEndpoints` and `assignIndices` are compute-heavy with no branches
- Memory access is non-contiguous (blocks aren't adjacent in memory), but gather handles it

## SPMD Limitations

### Algorithms That Don't Fit Well

| Algorithm | Why It Doesn't Fit | Alternative Approach |
|-----------|-------------------|---------------------|
| **Sorting** | Fundamentally about moving data between positions (cross-lane) | Use `SwizzleWithin` for small in-register sorts; traditional sort for large arrays |
| **Tree traversal** | Pointer chasing, lanes diverge wildly at each node | Flatten tree to array, use SPMD for leaf processing |
| **Prefix sum** | Serial dependency: each output depends on all previous inputs | Block decomposition: parallel within blocks, sequential propagation |
| **Hash tables** | Random access + write conflicts between lanes | Per-lane private tables + merge, or lock-free approaches |
| **Graph algorithms** | Irregular memory access, unpredictable control flow | Process independent subgraphs in parallel |
| **Carry-chain arithmetic** | Sequential bit propagation across full width | Use `pclmulqdq`-style dedicated instructions when available |

### When to Use Hybrid Approaches

Many real algorithms combine SPMD-friendly and SPMD-unfriendly parts:

1. **simdjson**: Stage 1a (classification) = SPMD. Stage 1b (prefix-XOR) = dedicated. Stage 2 (parsing) = SPMD.
2. **Parallel sort**: In-register sort = `SwizzleWithin`. Merge phase = sequential.
3. **Histogram**: Accumulation = per-lane private copies (SPMD). Merge = sequential reduction.
4. **UTF-8 validation**: Byte classification = SPMD. Sequence validation = cross-lane shift chains.

**Rule of thumb:** If more than 60% of the algorithm's work is data-parallel, SPMD is worth using. Wrap the sequential parts in uniform code outside `go for`.
