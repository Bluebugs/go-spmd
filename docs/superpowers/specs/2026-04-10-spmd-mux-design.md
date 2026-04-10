# Design Spec: `SPMDMux` — Constant-Index Lane Selection

**Date**: 2026-04-10
**Status**: Draft
**Motivation**: SPMDSelect chains from `if pos == 0 { A } else if pos == 1 { B } else { C }` where `pos = i % k` generate N-1 masked blends (8+ SIMD instructions). Since `pos` is a constant per-lane index, the entire chain is a static lane selection expressible as a single instruction.

## 1. Scope

New SSA instruction `SPMDMux` + post-predication detection pass + TinyGo lowering. Three repos touched (x-tools-spmd, tinygo, go — no go changes needed).

**Success criteria**: Base64 benchmark SSSE3 from ~10x to ~14-16x. WASM from 0.94x to >1.5x.

## 2. `SPMDMux` SSA Instruction

```go
// SPMDMux selects per-lane from N value operands based on a compile-time
// constant index vector. Replaces chains of SPMDSelect when the masks
// derive from IterPhi % constant comparisons, enabling a single shuffle
// instead of N-1 masked selects.
//
// All Values must have identical types. Indices[i] must be in [0, len(Values)).
// The result type matches Values[0].Type().
//
// Example printed form:
//
//	t8 = spmd_mux<16> [t3, t5, t7] indices [0,1,2,0,0,1,2,0,...]
type SPMDMux struct {
    register
    Values  []Value  // N value operands (one per case)
    Indices []int    // per-lane index into Values (len = Lanes)
    Lanes   int      // SIMD width
}
```

- **Operands**: all Values entries (for referrer tracking)
- **Pos**: `token.NoPos` (synthetic, like SPMDSelect)
- **Type**: `Values[0].Type()` (all Values must match)
- **Sanity checks**: `Lanes > 0`, `len(Indices) == Lanes`, all `Indices[i] in [0, len(Values))`, all Values have identical types

## 3. Detection Pass: `spmdDetectMuxPatterns`

Runs after `predicateSPMDScope` and `spmdMergeRedundantStores`. Scans each SPMD loop's scope blocks.

### Algorithm

1. **Find SPMDSelect chain roots**: Walk instructions. An SPMDSelect is a root if its result is NOT used as the Y operand of another SPMDSelect in the same block.

2. **Unwind the chain**: From root, follow Y operands through nested SPMDSelect:
   ```
   root:   SPMDSelect(mask0, val0, inner)    → pair (mask0, val0)
   inner:  SPMDSelect(mask1, val1, inner2)   → pair (mask1, val1)
   inner2: SPMDSelect(mask2, val2, default)  → pair (mask2, val2), default
   ```

3. **Trace each mask to `IterPhi % k == c`**: Each mask must be a `BinOp{EQL}` comparing a `BinOp{REM}` result against a `*Const`. The REM operand must derive from the loop's IterPhi (possibly through Convert, BinOp{ADD} with constant, etc.). All masks must share the same REM result and have distinct comparison constants.

4. **Extract the divisor `k`** from the REM's constant operand. Extract each comparison constant `c_i` from the EQL.

5. **Compute Indices vector**: For each lane `j` in `[0, laneCount)`:
   - `lane_remainder = j % k`
   - If `lane_remainder` matches comparison constant `c_i` → `Indices[j] = i` (index of the i-th value in the chain)
   - If no match → `Indices[j]` maps to the default value (add it as the last Values entry)

6. **Emit SPMDMux**: Create instruction with the collected Values and computed Indices. Replace all uses of the root SPMDSelect with the SPMDMux result. Remove dead SPMDSelect instructions.

### Pattern tracing detail

The mask trace follows this structure:
```
mask = BinOp{EQL}(rem_val, Const{c})
rem_val = BinOp{REM}(iter_val, Const{k})
iter_val = ... → IterPhi  (through Convert, BinOp{ADD/SUB} with const, etc.)
```

The pass must verify:
- All masks in the chain share the same `rem_val` (same BinOp{REM} instruction)
- All comparison constants are distinct integers in `[0, k)`
- `laneCount % k == 0` (ensures the pattern tiles cleanly)

### Edge cases

- **Partial coverage**: Chain covers cases 0 and 1 of `% 4`, cases 2-3 fall to default → still valid. Default value gets its own index.
- **Chain length 1**: Single SPMDSelect from `pos == c` → valid if mask derives from IterPhi % k.
- **Negated mask**: `pos != 0` at the root is equivalent to `pos == 0` with X/Y swapped. The detection should handle `BinOp{NEQ}` by swapping the root's X and Y.
- **Cross-block chains**: SPMDSelect chain may span multiple linearized blocks (from if/else predication). The chain-walk should follow Y operands across block boundaries within the loop scope.

## 4. TinyGo Lowering

`createSPMDMux` in `tinygo/compiler/spmd.go`:

1. Evaluate all Values to LLVM vectors.
2. Build N constant `<Lanes x i1>` masks, one per value — `mask_i[lane] = (Indices[lane] == i)`.
3. Emit N LLVM `select` instructions:
   ```
   result = select(mask_0, val0, zeroinit)
   result = select(mask_1, val1, result)
   ...
   result = select(mask_{N-1}, val_{N-1}, result)
   ```
4. All masks are compile-time constants, so LLVM constant-folds the selects into optimal blend/shuffle sequences per target.

### Scalar fallback

When `!b.simdEnabled` (laneCount=1): `Indices` has one element, just return `Values[Indices[0]]`.

### Why LLVM `select` instead of manual shuffle

- LLVM's backend knows the best lowering per target (pblendvb on SSE4.1, vpblendvb on AVX2, v128.bitselect on WASM)
- Constant i1 masks fold into the instruction encoding
- For N=3 (base64), LLVM may further optimize three constant selects into a single shufflevector
- No manual shuffle index construction needed

## 5. Expected Impact

### Base64 decoder (16 byte lanes, SSSE3)

**Before**: 2 SPMDSelect with runtime mask computation
- `pcmpeqd` + `packssdw` + `packsswb` per mask (4 instructions × 2 = 8)
- `pblendvb` per select (2 instructions)
- Total: ~10 instructions for the select chain

**After**: 3 LLVM selects with constant i1 masks
- LLVM lowers to 3 constant-mask blends (or fewer if optimized to shuffle)
- No mask computation instructions
- Total: ~3-5 instructions

**Estimated speedup improvement**: 10x → 14-16x at 1MB (by removing ~50% of the hot loop's instruction budget)

### WASM SIMD128

**Before**: 2 `v128.bitselect` with runtime-computed masks
**After**: 3 `v128.bitselect` with constant masks (or LLVM-optimized shuffle)
**Estimated improvement**: 0.94x → >1.5x (by eliminating mask computation)

## 6. Files Modified

| Repository | File | Change |
|-----------|------|--------|
| x-tools-spmd | `go/ssa/ssa.go` | Add `SPMDMux` struct |
| x-tools-spmd | `go/ssa/print.go` | Add `String()` method |
| x-tools-spmd | `go/ssa/emit.go` | Add `emitSPMDMux` helper |
| x-tools-spmd | `go/ssa/sanity.go` | Add sanity checks |
| x-tools-spmd | `go/ssa/spmd_predicate.go` | Add `spmdDetectMuxPatterns` pass, wire into pipeline |
| x-tools-spmd | `go/ssa/spmd_mux_test.go` | Unit tests for detection + emission |
| tinygo | `compiler/spmd.go` | Add `createSPMDMux` lowering |
| tinygo | `compiler/compiler.go` | Add dispatch case for `*ssa.SPMDMux` |
