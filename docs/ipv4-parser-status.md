# IPv4 Parser SPMD Compilation Status Report

**Date**: 2026-03-01
**Source**: `test/integration/spmd/ipv4-parser/main.go`
**Based on**: Wojciech Muła's SIMD IPv4 parsing research

## Current Status: 1 LLVM Verification Error (Previously: 25 errors)

The ipv4-parser is a key PoC goal — a real-world parsing algorithm using SPMD Go.
After significant work across multiple sessions, we are down from 25 LLVM verification
errors to 1. The remaining error is a multi-lane-count select mismatch.

### Progress Timeline

| Stage | Status | Notes |
|-------|--------|-------|
| Parsing | PASS | `go for`, `range input`, `range starts` all parse correctly |
| Type Checking | PASS | All SPMD rules enforced correctly |
| SSA Generation | PASS | x-tools SSA builder succeeds (composite literal panic fixed) |
| LLVM IR Generation | PASS | IR is generated (merge select deferred path fixed) |
| LLVM Verification | **FAIL** | 1 verification error: multi-lane-count select mismatch |
| WASM Codegen | BLOCKED | Blocked by remaining verification error |

### Fixes Applied (2026-02-28 to 2026-03-01)

1. **`anytrue.v4i1` intrinsic type mismatch** — FIXED
   - `Varying[bool]` uses `<4 x i1>` but anytrue.v4i1 was being called with `<4 x i32>`.
   - Fix: `spmdVectorAnyTrue` now calls `spmdWasmAnyTrue` directly without inserting sext,
     avoiding `ReplaceAllUsesWith` corruption of `sext <4 x i1>` → `sext <4 x i32>`.

2. **`Varying[bool]` switch merge type mismatch** — FIXED
   - `spmdCreateSwitchMergeSelect` built cascaded selects mixing `<4 x i1>` and `<4 x i32>`.
   - Fix: Normalize `<4 x i1>` to `<4 x i32>` before `spmdMaskSelect` in the cascaded
     select loop. Also truncate `selectVal` back to `<4 x i1>` before `ReplaceAllUsesWith`
     when the placeholder phi was created with `<4 x i1>` type.

3. **LOR phi tail-phase predecessor mismatch** — FIXED
   - Value-LOR (`a || b`) deferred phi in the tail phase referenced main-phase block
     (`binop.rhs19`) instead of the tail-phase equivalent (`binop.rhs.tail`).
   - Root cause: phi resolution reads `b.blockInfo` after state restoration (main-phase).
   - Fix: Resolve tail-phase merge phi overrides BEFORE restoring `b.blockInfo`, analogous
     to how deferred switch phis are resolved in `emitSPMDTailBody`.

4. **Accumulator phi missing predecessor** — FIXED
   - `rangeindex.loop13` phi had 3 LLVM predecessors but only 2 incoming values.
   - Root cause: Tail-phase `binop.done.tail` (If body block) branched back to the main
     loop block (`rangeindex.loop13`) without wiring the phi value.
   - Fix: In the `*ssa.If` handler, when in tail phase, detect successors that target
     the loop block and redirect them to `tailExitBlock`.

5. **`<4 x i8>` sub-128-bit vector promotion** — FIXED (partial)
   - `spmdVectorIndexArray` produced `<4 x i8>` gather results for byte arrays in 4-lane
     loops. WASM cannot lower sub-128-bit vectors.
   - Fix: After the per-lane gather, zero-extend `<4 x i8>` to `<4 x i32>` on WASM when
     the result is sub-128-bit.

### Remaining Issue (1 error)

```
Invalid operands for select instruction!
  %70 = select <16 x i1> %69, <4 x i8> %spmd.resize, <4 x i32> zeroinitializer
```

**Root cause**: Multi-lane-count mismatch in store coalescing for `dotMask[i] = c == '.'`.

The first `go for i, c := range input` loop uses 4-lane `i` (int=i32) but accesses
`dotMask [16]bool` (naturally 16-element). The comparison `c == '.'` produces `<4 x i1>`
(4-lane), but after the `zext <4 x i8> to <4 x i32>` fix, the code bitcasts the result
to `<16 x i8>` (via a `ChangeType` SSA instruction converting between uint8 and the 16-lane
bool array context). This causes:
- Comparison: `icmp eq <16 x i8> %changetype.vec, <16 x i8> splat('.')` — 16-lane
- Store mask: 16-lane `<16 x i1>` from the dotMask array load
- Store value: 4-lane `<4 x i8>` (from zext result truncated back to 4 elements)

The `select <16 x i1>, <4 x i8>, <4 x i32>` mixes all three types incorrectly.

**Root diagnosis**: The `ChangeType` SSA instruction that converts `c` (uint8) to be used
with the `dotMask` bool context reinterprets `<4 x i32>` as `<16 x i8>` via bitcast.
This is the fundamental multi-lane-count problem: a 4-lane loop body accessing a 16-element
array needs explicit lane-count boundary management.

**Fix needed**: One of:
1. For the zext approach: intercept `ChangeType` to prevent bitcast reinterpretation of
   zext'd byte values back to 16-lane context. Or...
2. Alternative: Instead of zext in `spmdVectorIndexArray`, handle sub-128-bit comparisons
   by promoting only at the comparison site (not the gather result), ensuring the
   4-lane context is maintained throughout.

### SPMD Features Used by ipv4-parser

The parser exercises a wide range of SPMD features:

1. **Multiple `go for` loops** with different array types and lane counts:
   - `go for i, c := range input` — iterates `[16]byte`, 4 lanes (int index)
   - `go for _, isDot := range dotMask` — iterates `[16]bool`, 4 lanes (int index)
   - `go for i, start := range starts` — iterates `[4]int`, 4 lanes for int
   - `go for field, start := range starts` — same 4-lane int iteration

2. **Varying switch statement** (`switch fieldLen`) — N-way branch on varying value

3. **Uniform early returns** inside `go for` (via `reduce.All`/`reduce.Any` guards)

4. **Varying array indexing** (`dotMask[i]`, `ends[i]`, `s[start]`)

5. **Reduce operations**: `reduce.All`, `reduce.Any`, `reduce.Add`, `reduce.Mask`, `reduce.FindFirstSet`

6. **Cross-type operations**: `lanes.Count(c)` for loop stride tracking

7. **Mixed scalar/vector arithmetic**: bit manipulation (`bits.TrailingZeros16`), type conversions (`uint8(value)`)

### What Works

- Parsing and type checking are fully correct
- SSA generation succeeds
- LLVM IR generation completes without panics
- Simple `go for` loops with single lane count compile fine (simple-sum, mandelbrot, etc.)
- Reduce operations work for single-lane-count programs
- Uniform early returns work correctly
- Varying switch statements work (simple-base64, type-casting-varying)
- Value-LOR (`a || b`) expressions work (for single-lane-count programs)

### Recommended Next Fix

The remaining `select <16 x i1> %69, <4 x i8> %spmd.resize, <4 x i32> zeroinitializer`
error can be fixed by intercepting `ChangeType` (convert) SSA instructions for byte values
inside SPMD loops. When a `uint8` value (which after `spmdVectorIndexArray` is `<4 x i32>`)
gets converted via `ChangeType` to another integer type, the conversion should use the
SPMD-zext'd `<4 x i32>` value directly, not bitcast it to `<16 x i8>`.

Alternatively, revert the blanket `spmdVectorIndexArray` zext and instead handle the
WASM promotion at the comparison site in `createBinOp` — only when a `<N x i8>` value
would be used in an icmp against a splat constant.
