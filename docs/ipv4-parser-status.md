# IPv4 Parser SPMD Compilation Status Report

**Date**: 2026-02-21
**Source**: `test/integration/spmd/ipv4-parser/main.go`
**Based on**: Wojciech Muła's SIMD IPv4 parsing research

## Current Status: LLVM Verification Errors (Previously: Compiler Panic)

The ipv4-parser is a key PoC goal — a real-world parsing algorithm using SPMD Go.
It previously crashed during compilation with panics. After two fixes in this session,
it now reaches LLVM IR generation and fails at LLVM verification with type mismatches.

### Progress Timeline

| Stage | Status | Notes |
|-------|--------|-------|
| Parsing | PASS | `go for`, `range input`, `range starts` all parse correctly |
| Type Checking | PASS | All SPMD rules enforced correctly |
| SSA Generation | PASS | x-tools SSA builder succeeds (composite literal panic fixed) |
| LLVM IR Generation | PASS | IR is generated (merge select panic fixed) |
| LLVM Verification | **FAIL** | 25 verification errors across 5 categories |
| WASM Codegen | BLOCKED | Blocked by verification errors |

### SPMD Features Used by ipv4-parser

The parser exercises a wide range of SPMD features, making it an excellent stress test:

1. **Multiple `go for` loops** with different array types and lane counts:
   - `go for i, c := range input` — iterates `[16]byte`, 16 lanes for byte
   - `go for _, isDot := range dotMask` — iterates `[16]bool`, 16 lanes for bool
   - `go for i, start := range starts` — iterates `[4]int`, 4 lanes for int
   - `go for field, start := range starts` — same 4-lane int iteration

2. **Varying switch statement** (`switch fieldLen`) — N-way branch on varying value

3. **Uniform early returns** inside `go for` (via `reduce.All`/`reduce.Any` guards)

4. **Varying array indexing** (`dotMask[i]`, `ends[i]`, `s[start]`)

5. **Reduce operations**: `reduce.All`, `reduce.Any`, `reduce.Add`, `reduce.Mask`, `reduce.FindFirstSet`

6. **Cross-type operations**: `lanes.Count(c)` for loop stride tracking

7. **Mixed scalar/vector arithmetic**: bit manipulation (`bits.TrailingZeros16`), type conversions (`uint8(value)`)

### LLVM Verification Errors (25 errors, 5 categories)

#### Category 1: `sext` vector-to-scalar mismatch (8 errors)

```
sext <4 x i32> %spmd.lane.idx to i32
```

**Root cause**: The lane index (`spmd.lane.idx`) is a `<4 x i32>` vector, but it's being
sign-extended to scalar `i32`. This happens when the lane index is used in a context
expecting a scalar — likely in `reduce.Mask`, `reduce.FindFirstSet`, or `lanes.Count`
calls that should extract a scalar result but receive the raw vector.

**Affected lines**: Lines 78, 80, 94, 95 (first loop), and lines in subsequent loops.

**Fix needed**: When a varying lane index is used in a reduce operation or scalar context,
the backend needs to either: (a) extract the appropriate lane, or (b) generate the
reduce operation correctly from the vector input.

#### Category 2: `and` type mismatch — mask format collision (3 errors)

```
and <4 x i32> %32, <4 x i1> %spmd.load
and <4 x i32> %32, <16 x i32> %43
and <16 x i32> %46, <16 x i1> zeroinitializer
```

**Root cause**: Three distinct sub-issues:
- `<4 x i32>` mask AND'd with `<4 x i1>` — i32 mask format (WASM) vs i1 mask format (LLVM intrinsics)
- `<4 x i32>` AND'd with `<16 x i32>` — different lane counts (4 for int vs 16 for byte/bool)
- `<16 x i32>` AND'd with `<16 x i1>` — same lane count but different mask representations

**Fix needed**: Consistent mask format conversion. When masks from different-width
SPMD loops interact, they need explicit conversion. The `spmdWrapMask`/`spmdUnwrapMask`
functions may need to handle cross-loop-width mask operations.

#### Category 3: PHI type mismatches (4 errors)

```
phi <16 x i1> [...], [ %81, %binop.rhs7 ]        -- <16 x i1> phi with non-i1 operand
phi i32 [ %276, %switch.body29 ], ...              -- scalar phi with vector operands
phi i1 [ %278, %switch.body29 ], ...               -- scalar phi with vector operands
```

**Root cause**: Two sub-issues:
- `Varying[bool]` uses `<16 x i1>` (128/1 = 16 lanes), but mask operations produce
  `<4 x i32>` (WASM i32 mask format). These collide at phi merge points.
- The varying switch statement (`switch fieldLen`) produces phis at the switch merge
  that mix scalar and vector types because switch masking is not yet implemented.

**Fix needed**:
- `Varying[bool]` mask consistency: the backend currently doesn't distinguish between
  "bool as data" (`<16 x i1>`) and "bool as mask" (`<4 x i32>` on WASM).
- Varying switch masking (Phase 2.5 deferred item) would handle the switch phi issues.

#### Category 4: Invalid select operands (3 errors)

```
select <4 x i1> %193, <4 x i1> <i1 true, ...>, <4 x i32> %192
select <4 x i1> %275, <16 x i32> %222, <16 x i32> %295
select <4 x i1> %277, <16 x i1> zeroinitializer, <16 x i32> %297
```

**Root cause**: Select instructions require condition and operand types to match in width.
- `<4 x i1>` condition with `<4 x i1>` true-val but `<4 x i32>` false-val — type mismatch
- `<4 x i1>` condition with `<16 x i32>` operands — lane count mismatch (4 vs 16)

**Fix needed**: Same underlying issues as Categories 2 and 3. Mask format consistency
and lane count consistency across loops.

#### Category 5: PHI predecessor count mismatch (2 errors)

```
PHINode should have one entry for each predecessor of its parent basic block!
```

**Root cause**: After varying if/else CFG linearization, some blocks have their
predecessors redirected, but the phis aren't updated to match. This likely occurs in
the switch statement lowering path which doesn't have SPMD-aware phi handling.

**Fix needed**: Varying switch masking implementation would address this.

### Underlying Root Causes (Summary)

The 25 errors stem from 4 underlying issues:

1. **Multiple lane counts in one function** (HIGH priority)
   - The parser has loops over `[16]byte` (16 lanes), `[16]bool` (16 lanes),
     and `[4]int` (4 lanes) in the same function.
   - The SPMD backend currently assumes a single lane count per SPMD region.
   - Masks, selects, and phis from different loops collide when values flow between them.

2. **`Varying[bool]` type representation** (HIGH priority)
   - `Varying[bool]` → `<16 x i1>` (128/1=16 lanes) but platform masks are `<4 x i32>`.
   - Data booleans and mask booleans have different representations and lane counts.
   - Need to distinguish "bool as varying data" from "bool as execution mask".

3. **Varying switch masking** (MEDIUM priority, deferred)
   - `switch fieldLen` with varying `fieldLen` requires N-way CFG linearization.
   - Currently listed as a deferred Phase 2.5 item.
   - Affects the field processing loop (lines 137-151).

4. **Lane index in scalar contexts** (MEDIUM priority)
   - `sext <4 x i32> to i32` — lane index vector used where scalar expected.
   - Affects `reduce.Mask`, `reduce.FindFirstSet`, `lanes.Count` calls.
   - Backend needs to extract/reduce appropriately when vector meets scalar context.

### What Works

- Parsing and type checking are fully correct
- SSA generation succeeds (composite literal return type-checking fixed)
- LLVM IR generation completes without panics (merge select deferred path fixed)
- Simple `go for` loops with single lane count compile fine (simple-sum, mandelbrot, etc.)
- Reduce operations work for single-lane-count programs
- Uniform early returns work correctly

### Recommended Fix Order

1. **Lane count consistency** — Ensure each `go for` loop properly scopes its lane count;
   values crossing loop boundaries need explicit conversion
2. **`Varying[bool]` representation** — Decide on consistent representation:
   either always `<N x i1>` with lane-count-appropriate N, or always platform mask format
3. **Varying switch masking** — Implement N-way linearization for switch statements
4. **Scalar extraction** — Handle lane index in scalar contexts via reduce/extract
