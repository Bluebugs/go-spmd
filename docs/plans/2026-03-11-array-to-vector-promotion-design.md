# SSA-Level Array-to-Vector Promotion

**Date**: 2026-03-11
**Status**: Design
**Location**: `x-tools-spmd/go/ssa/spmd_promote.go`

## Problem

Small arrays used inside `go for` loops are lowered to stack allocations with `memory.fill` (zeroinit) and `memory.copy` (init from external data). For a `[16]byte` array, this means:

1. `runtime.alloc(16)` or stack alloca
2. `store [16 x i8] zeroinitializer` (memset)
3. `runtime.sliceCopy` (memcpy from source string/slice)
4. Per-iteration `IndexAddr` + `Store`/`Load` through memory

All of this is unnecessary: a `[16]byte` accessed by a 16-lane byte `go for` loop IS semantically a `Varying[byte]`. The array fits in a single v128 register and every element maps 1:1 to a SIMD lane.

## Solution

A new SSA pass `promoteSPMDArrays` that runs early (after `lift`, before predication/peeling) to promote eligible array allocations to `Varying[T]` SSA values, eliminating stack traffic entirely.

## Eligibility Criteria

An `*ssa.Alloc` is promoted when ALL conditions hold:

1. **Fixed-size array type**: `alloc.Type().Underlying()` is `*types.Array`
2. **Fits in v128**: `arrayLen * elemSize <= 16` bytes (e.g., `[16]byte`, `[4]int32`, `[2]float64`)
3. **All use sites inside SPMD loops**: Every referrer is inside a `go for` loop body
4. **All use sites are varying operations**: Stores use varying index or varying value; loads feed varying operations. No uniform access patterns.
5. **Lane count matches array length**: The SPMD loop's lane count equals the array length
6. **No escaping referrers**: Only `IndexAddr`, `Store`, `UnOp{MUL}`, and `DebugRef` referrers are allowed (transitively). Any `Call`, `Phi`, `ChangeInterface`, or other referrer makes the alloc ineligible.

Ineligible arrays are left unchanged on the stack — no transformation is applied.

Note: `lift` does not promote these allocs (they have `IndexAddr` referrers which fail `liftAlloc`'s referrer check), so `promoteSPMDArrays` handles a category that `lift` explicitly skips.

## Relationship to Gather Coalescing

Gather coalescing (`spmdDetectGatherGroups` in `spmd_predicate.go`) operates on arrays accessed with varying indices inside SPMD loops — the same arrays this pass targets. The two passes are complementary:

- **Array promotion** runs first and replaces the `Alloc` + memory ops with `Varying[T]` SSA values
- **Gather coalescing** runs later (during predication) and merges multiple byte-level gathers from the same source into single swizzle operations

After promotion, gather coalescing sees `Varying[T]` values instead of `IndexAddr` chains into allocas. The coalescing pass may need minor updates to recognize promoted values, but the core swizzle-merging logic is unchanged.

**Ordering**: `promoteSPMDArrays` → `predicateSPMDLoop` (which calls `spmdDetectGatherGroups`) → `peelSPMDLoop`

## Pass Ordering

```
lift -> promoteSPMDArrays -> predicateSPMDLoop -> predicateSPMDFuncBody -> peelSPMDLoop
```

**Insertion point**: In `finishBody()` (`x-tools-spmd/go/ssa/func.go`), after the `lift(f)` call and before `predicateSPMDLoop`. The pass is gated on `len(f.SPMDLoopInfos) > 0`.

Early placement ensures promoted arrays affect stack size calculations and all downstream passes see `Varying[T]` values.

## Promotion Mechanics

### A. Create varying value

Replace the `*ssa.Alloc` with a new SSA value of type `*types.SPMDType{Elem: arrayElemType}`. Initial value is varying zero (`SPMDConst(zero)`).

### B. Replace stores

Each `Store` to `IndexAddr(alloc, idx)` with value `v`:

- If `idx` is the loop induction variable: the store assigns lane-by-lane, so the entire vector IS `v`. Replace the store with assignment of the varying value `v`.
- If `idx` is uniform: ineligible (criterion 4 rejects this alloc).

### C. Replace loads

Each `UnOp{MUL}` from `IndexAddr(alloc, idx)`:

- If `idx` is the loop induction variable: the load reads lane-by-lane, so the result IS the varying value. Replace all uses with the varying value.
- If `idx` is uniform: ineligible (criterion 4 rejects this alloc).

### D. Loop body simplification

When the entire loop body is `array[i] = f(input[i])` where `i` is the induction variable, the loop body reduces to a single varying operation `varyingResult = f(varyingInput)`.

**Important**: The `go for` loop CFG structure is NOT removed by this pass. The loop blocks remain (preserving `SPMDLoopInfo`, `IterPhi`, and peeling invariants). Instead, the loop body becomes trivial — downstream passes (predication, peeling) handle or skip it naturally. TinyGo may further optimize empty loop bodies during codegen.

## External Data Initialization

### Pattern Recognition

The pass recognizes two initialization patterns for promoted arrays:

1. **`runtime.sliceCopy` pattern**: An `*ssa.Call` to `runtime.sliceCopy` where the destination is a slice of the promoted alloc. Identified by matching the call's first argument (dst pointer) against the alloc.

2. **`go for` copy loop pattern**: A `go for i, c := range src { arr[i] = c }` loop where `arr` is the promoted alloc and the loop body contains only the indexed store. Identified when the loop's only side effect is stores to the promoted alloc with the induction variable as index.

Both patterns are replaced with `SPMDVectorFromMemory`.

### SPMDVectorFromMemory instruction

```
%v = SPMDVectorFromMemory %ptr %len   // type: Varying[T]
```

Loads N elements from a pointer into a `Varying[T]` value. When `len < arrayLen`, remaining lanes get zero.

- **Operands**: `ptr` (pointer to element type), `len` (int, number of valid elements)
- **Result type**: `*types.SPMDType{Elem: elemType}`
- **Semantics**: Load `min(len, laneCount)` elements from `ptr` into a varying value; remaining lanes are zero
- **Operands() impl**: Returns `[]*Value{&i.Ptr, &i.Len}` for correct referrer graph registration
- **TinyGo lowering**: Masked vector load — WASM: `v128.load` from ptr, then `v128.bitselect` with tail mask and zero vector for lanes beyond `len`
- **Tail masking mechanism**: Generate a constant `<N x i1>` mask where lanes `0..len-1` are true, rest false. Use `select(mask, loaded_vec, zero_vec)`. This reuses the existing tail mask infrastructure from `predicateSPMDLoop`.
- **Peel clone support**: Must be added to `spmdCloneBlock`'s instruction type switch in `spmd_peel.go`. In practice, `SPMDVectorFromMemory` is loop-invariant and placed before the loop, so it won't be inside peeled blocks. The clone case should be added defensively with a simple operand-remap clone.
- **Scalar fallback**: In scalar mode (`-simd=false`), `SPMDVectorFromMemory` degenerates to a single scalar load `ptr[0]`. The pass itself is NOT gated off in scalar mode — it still promotes arrays — but TinyGo's scalar lowering path handles the instruction as a scalar load.

## Worked Example: IPv4 `digits` Array

### Before (current SSA)

```
%input = Alloc [16]byte                           // stack alloc
runtime.sliceCopy(%input, %s)                      // memcpy from string
%digits = Alloc [16]byte                           // stack alloc
store [16]byte zeroinitializer, %digits            // memset

// go for i, c := range input:
//   %ptr = IndexAddr %input %i
//   %c = UnOp{MUL} %ptr
//   %d = BinOp{SUB} %c, '0'
//   %dptr = IndexAddr %digits %i
//   Store %dptr %d
```

### After promotion

```
%input = SPMDVectorFromMemory %s.ptr %s.len        // Varying[byte], one masked load
%digits = BinOp{SUB} %input, broadcast('0')         // Varying[byte], one vector sub
// go for loop body now trivial (stores removed, loads replaced)
```

No stack allocation, no memset, no memcpy. The loop body becomes empty — stores to `digits` are replaced by the single `BinOp{SUB}` on varying values.

## New SSA Instructions

### SPMDVectorFromMemory

- **Struct fields**: `Ptr Value`, `Len Value`, `ElemType types.Type`
- **Operands()**: `[]*Value{&i.Ptr, &i.Len}`
- **Result type**: `*types.SPMDType{Elem: i.ElemType}`
- **Semantics**: Load `min(len, laneCount)` elements from `ptr`; remaining lanes are zero
- **String()**: `"SPMDVectorFromMemory %ptr %len"`

### Future extensions (not in this pass)

Uniform-index access (`SPMDInsertLane`, `SPMDExtractLane`) would enable partial promotion of arrays with mixed access patterns. This is explicitly out of scope — criterion 4 rejects allocs with any uniform access.

## Impact on Downstream Passes

- **Predication**: Already handles `Varying[T]` values. No changes needed.
- **Gather coalescing**: May need to recognize promoted varying values as gather sources (minor update to `spmdDetectGatherGroups`).
- **Peeling**: Fewer instructions in loop body. `SPMDVectorFromMemory` added to `spmdCloneBlock` defensively.
- **TinyGo codegen**: `SPMDVectorFromMemory` needs a new lowering case in `compiler.go`. All other promoted values use existing `Varying[T]` → LLVM vector codegen.
- **Stack size**: Promoted arrays no longer consume stack space.

## Scope Boundaries

### In scope

- Arrays with all-varying use sites inside a single SPMD loop
- `SPMDVectorFromMemory` for external data initialization (sliceCopy and copy-loop patterns)
- Loop body simplification (stores/loads replaced, body becomes trivial)
- Primitive element types (byte, int16, int32, int64, float32, float64)

### Out of scope (future work)

- Arrays accessed from multiple SPMD loops
- Partial promotion (mixed uniform/varying access)
- Arrays larger than 16 bytes (multi-register)
- Struct element types
- Loop CFG removal (loop structure preserved for downstream passes)

## Scalar Fallback Note

In scalar mode, the pass still promotes arrays (reducing stack traffic), but `SPMDVectorFromMemory` lowers to a single scalar load. The pre-existing `digits[0xFF]` out-of-bounds issue in the IPv4 parser is unrelated and tracked in PLAN.md deferred items.

## Testing Strategy

1. **SSA unit tests** (`x-tools-spmd/go/ssa/spmd_promote_test.go`): Verify eligibility detection, promotion mechanics, pattern recognition for sliceCopy/copy-loop
2. **TinyGo LLVM tests** (`tinygo/compiler/spmd_llvm_test.go`): Verify `SPMDVectorFromMemory` lowering to masked vector load
3. **E2E tests**: IPv4 parser before/after promotion — same output, fewer WASM instructions
4. **Benchmark**: Measure stack size reduction and performance improvement on IPv4 parser
