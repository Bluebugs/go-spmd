# Gather Coalescing Optimization Design

## Problem

When a `go for` loop indexes a small byte array (`[N]byte`, N ≤ 16) multiple times with related varying indices, the compiler emits separate gather chains per access. Each gather chain costs 5 WASM SIMD ops:

```wat
i8x16.shuffle 0 4 8 12 ...   ;; compact i32x4 indices to i8x16
i8x16.swizzle                 ;; table[index] for 4 lanes
i16x8.extend_low_i8x16_u     ;; widen i8→i16
i32x4.extend_low_i16x8_u     ;; widen i16→i32
;; + v128.bitselect for fieldLen masking
```

In the ipv4-parser, Loop 3 accesses `input[start]`, `input[start+1]`, `input[start+2]` — 3 accesses per field × 2 fieldLen conditions = **6 gather chains = 30+ SIMD ops**. All 6 read from the same `input[16]` register with indices that differ by constant offsets from the same base (`start`).

## Key Insight

All 6 gathers share the same 16-byte source register (`input[16]`). Their indices are `start+0`, `start+1`, `start+2` (offset variants). A single `i8x16.swizzle` can pick all 12 needed bytes at once if we construct a merged 16-byte index vector. The individual digit values are then extracted via compile-time `i8x16.shuffle` (column extraction).

**Before** (6 swizzles):
```
swizzle(input, [s0, s1, s2, s3])       → [d2_0, d2_1, d2_2, d2_3]  as i32x4
swizzle(input, [s0+1, s1+1, s2+1, ...]) → [d1_0, d1_1, d1_2, d1_3]  as i32x4
swizzle(input, [s0+2, s1+2, s2+2, ...]) → [d0_0, d0_1, d0_2, d0_3]  as i32x4
× 2 (conditioned on fieldLen)
= 6 swizzles + 6 extends + 6 shuffles + bitselect merging = ~64 SIMD ops
```

**After** (1 swizzle + column extraction):
```
;; Build merged index: [s0, s0+1, s0+2, _, s1, s1+1, s1+2, _, ...]
;; 1 compile-time shuffle + 1 i8x16.add + 1 bitselect = 3 ops
merged_idx = shuffle(starts_bytes, [0,0,0,16, 4,4,4,16, ...])
           + [0,1,2,0, 0,1,2,0, ...]
           + bitselect(valid_mask, merged_idx, 0xFF)

;; Single swizzle picks all 12 bytes
swizzle(digits, merged_idx) = [d2_0,d1_0,d0_0,0, d2_1,d1_1,d0_1,0, ...]
= 1 SIMD op

;; Column extraction via compile-time shuffles
hundreds = shuffle(swizzled, zeros, [0,_,_,_, 4,_,_,_, 8,_,_,_, 12,_,_,_])
tens     = shuffle(swizzled, zeros, [1,_,_,_, 5,_,_,_, 9,_,_,_, 13,_,_,_])
units    = shuffle(swizzled, zeros, [2,_,_,_, 6,_,_,_, 10,_,_,_, 14,_,_,_])
= 3 SIMD ops (result is i32x4 via little-endian byte placement)

;; Multiply with uniform weights
value = hundreds * 100 + tens * 10 + units
= 2 mul + 2 add = 4 ops

Total: ~13 SIMD ops (vs ~64)
```

## Scope

This is **NOT** a general-purpose optimization pass. It's a targeted enhancement to the existing swizzle path in `spmdVectorIndexArray()`. The optimization triggers when:

1. Target is WASM
2. Source is a byte array ≤ 16 elements (already on the swizzle path)
3. The same source array is swizzled multiple times in the same block/scope
4. The index vectors differ by constant i32x4 offsets from a common base

## Design

### Where It Lives

The optimization operates at LLVM IR emission time in `tinygo/compiler/spmd.go`, extending the existing swizzle machinery. No SSA-level changes needed.

### Detection: Swizzle Coalescing Map

Add a map to track pending swizzle operations from the same source:

```go
// In spmdLoopState or builder:
type spmdSwizzleGroup struct {
    tableVec  llvm.Value     // The <16 x i8> source register (shared)
    tableSSA  ssa.Value      // SSA source (for identity check)
    entries   []spmdSwizzleEntry
}

type spmdSwizzleEntry struct {
    index     llvm.Value     // The <4 x i32> index vector
    ssaIndex  ssa.Value      // SSA index expression (for offset analysis)
    result    *llvm.Value    // Pointer to fill with coalesced result
    laneCount int
}
```

### Trigger Point

In `spmdVectorIndexArray()` at line 4499, after determining we're on the WASM swizzle path, instead of immediately calling `spmdSwizzleFromPtr`/`spmdSwizzleArrayBytes`, register the swizzle in the coalescing map:

```go
if b.spmdIsWASM() && elemType == b.ctx.Int8Type() && xType.Len() <= 16 {
    // ... identity swizzle check (unchanged) ...

    // Check if this source array has pending swizzles to coalesce with
    if group := b.spmdPendingSwizzles[sourceKey]; group != nil {
        // Register this swizzle for deferred coalesced emission
        return b.spmdDeferSwizzle(group, index, laneCount)
    }
    // First swizzle from this source — defer it too
    group := b.spmdCreateSwizzleGroup(tableVec, sourceSSA)
    return b.spmdDeferSwizzle(group, index, laneCount)
}
```

### Problem: Deferred Emission Requires Forward Knowledge

The compiler processes SSA instructions sequentially. When it sees the first `input[start]`, it doesn't know that `input[start+1]` and `input[start+2]` will follow. Deferring emission breaks the sequential codegen model.

**Alternative approach: Post-hoc coalescing at LLVM IR level.**

After all swizzles in a block are emitted, scan for coalescing opportunities in the generated LLVM IR. This avoids changing the sequential emission model.

However, LLVM IR pattern matching is fragile and loses the SSA-level relationship information.

**Better alternative: SSA-level annotation.**

The go/ssa predication pass (`x-tools-spmd`) already has visibility into the full loop body. It can detect related `*ssa.Index` instructions on the same array with offset-related indices and annotate them. TinyGo then reads the annotation.

**Simplest alternative: Opportunistic coalescing at the widening step.**

The current pipeline is: swizzle → extract+widen to i32x4. Instead of widening each swizzle result independently, detect that multiple swizzled results come from the same `<16 x i8>` intermediate and extract columns from it.

### Chosen Approach: Post-Swizzle Column Extraction

The simplest approach that requires no SSA changes and no deferred emission:

1. **Keep the existing swizzle emission unchanged** — each `input[start+k]` still emits its own swizzle call.
2. **Add a post-processing pass** (`spmdCoalesceSwizzles`) that runs after all instructions in a block are compiled.
3. The pass finds groups of `@llvm.wasm.swizzle` calls that share the same table operand.
4. For each group, it constructs a merged index vector, emits ONE swizzle, and replaces each original swizzle's uses with column extractions from the merged result.

Wait — this is essentially an LLVM optimization pass. TinyGo already has a `transform/` package with LLVM-level passes. But writing a custom LLVM pass in Go is complex.

**Even simpler: detect at swizzle-emit time using a 1-instruction lookback.**

When `spmdSwizzleWithTable` is about to emit `@llvm.wasm.swizzle(table, idx)`, check if the PREVIOUS instruction in the current LLVM basic block is also `@llvm.wasm.swizzle` with the same `table` operand. If yes, the two can be merged.

This lookback approach works because the compiler processes SSA instructions in dominator order, and related `Index` instructions on the same array tend to be adjacent (they're in the same `go for` body block).

But this is fragile. Let's go with the SSA annotation approach.

### Final Chosen Approach: SSA-Level Gather Group Annotation

**Phase 1 (x-tools-spmd):** During `predicateSPMDScope`, detect groups of `*ssa.Index` instructions on the same `[N]byte` array (N ≤ 16) with offset-related varying indices. Annotate them with a shared group ID.

**Phase 2 (TinyGo):** When compiling the first Index in a group, emit the merged swizzle and cache the result. For subsequent Indexes in the group, extract the appropriate column from the cached result.

### SSA-Level Detection

In `x-tools-spmd/go/ssa/spmd_predicate.go`, add a new function `spmdDetectGatherGroups` called during `predicateSPMDLoop`. It scans the loop body blocks for Index instructions on the same small byte array.

Two Index instructions `a[i]` and `a[j]` are "offset-related" if:
- They index the same SSA array value
- `j = i + const` where `const` is a compile-time constant (typically 0, 1, or 2)

The group records the base index and each member's constant offset.

### SSA Annotation

Add to `ssa.go`:

```go
// SPMDGatherGroup records a group of Index instructions on the same
// small byte array that can be coalesced into a single swizzle.
type SPMDGatherGroup struct {
    Source   Value              // The array value being indexed
    Base     Value              // The base varying index (e.g., start)
    Members  []*SPMDGatherMember
}

type SPMDGatherMember struct {
    Index    *Index             // The Index instruction
    Offset   int               // Constant offset from Base (0, 1, 2, ...)
    Position int               // Position in the merged 16-byte result (0-15)
}
```

Each `*ssa.Index` instruction gets a field:
```go
type Index struct {
    // ... existing fields ...
    SPMDGatherGroup *SPMDGatherGroup // nil if not part of a coalesced group
    SPMDGatherPos   int              // position in merged swizzle result
}
```

### TinyGo Emission

In `spmdVectorIndexArray()`, after the existing swizzle path entry check:

```go
if expr.SPMDGatherGroup != nil {
    return b.spmdCoalescedSwizzle(expr, collection, index, laneCount)
}
```

`spmdCoalescedSwizzle`:
1. On first member: build merged index vector, emit ONE swizzle, cache result.
2. On subsequent members: extract column from cached result.

```go
func (b *builder) spmdCoalescedSwizzle(expr *ssa.Index, collection, index llvm.Value, laneCount int) (llvm.Value, error) {
    group := expr.SPMDGatherGroup
    cacheKey := group // pointer identity

    if cached, ok := b.spmdGatherCache[cacheKey]; ok {
        // Extract column for this member's position
        return b.spmdExtractSwizzleColumn(cached, expr.SPMDGatherPos, laneCount)
    }

    // First member: build merged index and emit single swizzle
    tableVec := ... // load source array as <16 x i8> (existing code)

    // Build merged <16 x i8> index vector:
    // For each member m at position p, index bytes [p, p+1, ..., p+k-1]
    // get the value base + m.Offset (truncated to i8)
    mergedIdx := b.spmdBuildMergedGatherIndex(group, index, laneCount)

    // Single swizzle
    swizzled := b.spmdWasmSwizzleRaw(tableVec, mergedIdx)

    // Cache for subsequent members
    b.spmdGatherCache[cacheKey] = swizzled

    // Extract column for this member
    return b.spmdExtractSwizzleColumn(swizzled, expr.SPMDGatherPos, laneCount)
}
```

### Merged Index Construction

For a group with base `start` (i32x4) and members at offsets [0, 1, 2]:

```go
func (b *builder) spmdBuildMergedGatherIndex(group *SPMDGatherGroup, baseIdx llvm.Value, laneCount int) llvm.Value {
    // baseIdx is <4 x i32> = [s0, s1, s2, s3]
    // Need: [s0+0, s0+1, s0+2, _, s1+0, s1+1, s1+2, _, s2+0, s2+1, s2+2, _, s3+0, s3+1, s3+2, _]

    // Step 1: Truncate base to i8 (values are 0-15)
    baseI8 := b.CreateTrunc(baseIdx, <4 x i8>)

    // Step 2: Replicate each byte to group.MemberCount positions via shuffle
    // For 3 members + 1 padding per lane: shuffle mask [0,0,0,16, 1,1,1,16, 2,2,2,16, 3,3,3,16]
    replicateMask := ... // compile-time constant
    replicated := b.CreateShuffleVector(baseI8, zeros16, replicateMask)
    // → [s0,s0,s0,0, s1,s1,s1,0, s2,s2,s2,0, s3,s3,s3,0]

    // Step 3: Add per-position offsets
    offsets := v128.const [0,1,2,0, 0,1,2,0, 0,1,2,0, 0,1,2,0]
    indexed := b.CreateAdd(replicated, offsets)
    // → [s0,s0+1,s0+2,0, s1,s1+1,s1+2,0, ...]

    // Step 4: Mask invalid positions to 0xFF (so swizzle returns 0)
    // Position p is valid if member at offset (p % stride) exists in the group
    // For dense groups [0,1,2], all are valid. For sparse groups, mask needed.
    // The 4th byte per lane (padding) gets 0xFF.
    paddingMask := v128.const [0,0,0,0xFF, 0,0,0,0xFF, 0,0,0,0xFF, 0,0,0,0xFF]
    merged := b.CreateOr(indexed, paddingMask) // OR with 0xFF sets padding bytes
    // Wait — this corrupts the index bytes. Use bitselect instead:
    validMask := v128.const [0xFF,0xFF,0xFF,0, 0xFF,0xFF,0xFF,0, ...]
    merged := b.spmdMaskSelect(validMask, indexed, splat(0xFF))

    return merged
}
```

### Column Extraction

Extract the i-th member's values from the merged swizzle result:

```go
func (b *builder) spmdExtractSwizzleColumn(swizzled llvm.Value, memberPos, laneCount int) (llvm.Value, error) {
    // swizzled is <16 x i8>: [d0_0,d1_0,d2_0,0, d0_1,d1_1,d2_1,0, ...]
    // memberPos is the byte offset within each 4-byte group (0, 1, or 2)
    // Extract bytes at positions [memberPos, memberPos+4, memberPos+8, memberPos+12]
    // Place them at i32 lane positions [0, 4, 8, 12] (little-endian low byte)

    stride := 16 / laneCount // bytes per lane group (4 for 4 lanes)
    shuffleMask := make([]int, 16)
    for i := range shuffleMask {
        shuffleMask[i] = 16 // select from zeros operand (= 0)
    }
    for lane := 0; lane < laneCount; lane++ {
        // Source byte: lane * stride + memberPos
        srcByte := lane * stride + memberPos
        // Destination: lane * (16/laneCount) + 0 (low byte of i32 in little-endian)
        dstByte := lane * stride
        shuffleMask[dstByte] = srcByte
    }

    zeros := llvm.ConstNull(v16i8)
    extracted := b.CreateShuffleVector(swizzled, zeros, shuffleMask)
    // Result is <16 x i8> but logically <4 x i32> in little-endian
    // Bitcast to <4 x i32>
    result := b.CreateBitCast(extracted, llvm.VectorType(i32Type, laneCount))
    return result, nil
}
```

### Handling fieldLen-Dependent Access

The ipv4-parser has a `switch fieldLen { case 3: ...; case 2: ...; case 1: ... }` where different cases access different byte offsets. After SSA predication, this becomes:

```
;; case 3: d2 = input[start], d1 = input[start+1], d0 = input[start+2]
;; case 2: d1 = input[start], d0 = input[start+1]
;; case 1: d0 = input[start]
```

With SPMDSelect merging, the SSA has separate Index instructions for each case. The gather group detection needs to handle this: group by base index (after stripping the constant offset) even across different predicated paths.

The `bitselect` merging of case results happens AFTER the column extraction, preserving correctness. Each case's gathers are coalesced independently:

- case 3 group: `[start, start+1, start+2]` → 1 swizzle, 3 column extractions
- case 2 group: `[start, start+1]` → 1 swizzle, 2 column extractions
- case 1 group: `[start]` → 1 swizzle (no coalescing benefit here)

Total: 3 swizzles instead of 6. Plus 5 column extractions (shuffles, no extends needed).

## Performance Estimate

### Instruction Count

| Component | Before | After | Delta |
|-----------|--------|-------|-------|
| Gather chains (6 × 5 ops) | 30 | 0 | -30 |
| Merged swizzles (3 × 1 op) | 0 | 3 | +3 |
| Index construction (3 × 3 ops) | 0 | 9 | +9 |
| Column extraction (5 × 1 shuffle) | 0 | 5 | +5 |
| Widening (6 × 2 extends) | 12 | 0 | -12 |
| Bitselect merging | ~6 | ~6 | 0 |
| **Subtotal gather region** | **~48** | **~23** | **-25** |
| Multiply arithmetic | 14 | 14 | 0 |
| **Loop 3 total** | **~64** | **~39** | **-25** |
| **parseIPv4 total** | **~117 SIMD** | **~92 SIMD** | **-25 (-21%)** |

### Cycle Estimate

The 25 eliminated SIMD ops are mostly `extend` (2 per gather = 12) and `shuffle+swizzle` (2 per gather = 12). The new column extraction shuffles (5 ops) are compile-time constant shuffles which execute in 1 cycle each on Cranelift.

Estimated improvement: **~15-20% reduction in Loop 3 latency**, translating to **0.76x → ~0.82-0.85x** overall.

### Why Not 1.0x

The remaining gap is structural:
- Frame setup (53 ops) — unrelated to gathers
- Serial ctz loop (33 ops) — inherently sequential
- Loop 1 char classify (100 ops) — already optimal, no change
- Loop 2 field validation + replace_lane assembly (42 ops) — unrelated

These ~228 ops of non-gather overhead dominate. The scalar function (261 total ops) wins because it has no SIMD setup cost.

## Implementation Plan

### Files Changed

1. **`x-tools-spmd/go/ssa/ssa.go`** (~15 lines): Add `SPMDGatherGroup` type, `SPMDGatherMember` type, field on `Index`.
2. **`x-tools-spmd/go/ssa/spmd_predicate.go`** (~80 lines): Add `spmdDetectGatherGroups` — scan loop body for coalescing opportunities, annotate Index instructions.
3. **`x-tools-spmd/go/ssa/spmd_predicate_test.go`** (~40 lines): Test gather group detection.
4. **`tinygo/compiler/spmd.go`** (~120 lines): Add `spmdCoalescedSwizzle`, `spmdBuildMergedGatherIndex`, `spmdExtractSwizzleColumn`. Modify `spmdVectorIndexArray` to check annotation.

### Task Breakdown

#### Task 1: SSA Gather Group Types
**Files:** `x-tools-spmd/go/ssa/ssa.go`

Add types and field:
```go
type SPMDGatherGroup struct {
    Source  Value
    Base    Value              // base varying index
    Stride  int               // bytes per lane in merged result (e.g., 4 for 3 members + padding)
    Members []*SPMDGatherMember
}

type SPMDGatherMember struct {
    Instr  *Index
    Offset int  // constant offset from Base
    Pos    int  // byte position within each stride group
}
```

Add to `Index` struct:
```go
SPMDGatherGroup *SPMDGatherGroup
SPMDGatherPos   int
```

#### Task 2: SSA Gather Group Detection
**Files:** `x-tools-spmd/go/ssa/spmd_predicate.go`

Add `spmdDetectGatherGroups(fn *Function, loop *SPMDLoopInfo, scopeBlocks map[*BasicBlock]bool)`.

Algorithm:
1. Scan all instructions in scope blocks for `*Index` on `*types.Array` with elem type `byte` and array len ≤ 16.
2. Group by source value (the array being indexed).
3. Within each group, check if indices are `base + const` for a shared `base` Value.
   - `base + 0`: the Index instruction's `.Index` is directly `base`
   - `base + k`: the Index instruction's `.Index` is a `*BinOp{Op: ADD, X: base, Y: ConstInt(k)}`
4. If a group has ≥ 2 members, annotate each member's `Index.SPMDGatherGroup` and `SPMDGatherPos`.

Call from `predicateSPMDLoop` after `predicateSPMDScope` returns.

#### Task 3: Test Gather Group Detection
**Files:** `x-tools-spmd/go/ssa/spmd_predicate_test.go`

Test cases:
- 3 accesses `a[i]`, `a[i+1]`, `a[i+2]` → group of 3
- 2 accesses `a[i]`, `a[i+1]` → group of 2
- 1 access `a[i]` → no group (need ≥ 2)
- Different source arrays → separate groups
- Non-constant offset → no group

#### Task 4: TinyGo Coalesced Swizzle Emission
**Files:** `tinygo/compiler/spmd.go`

Add cache: `spmdGatherCache map[*ssa.SPMDGatherGroup]llvm.Value` in builder.

Modify `spmdVectorIndexArray` to check `expr.SPMDGatherGroup != nil` before the existing swizzle path.

Implement:
- `spmdCoalescedSwizzle(expr, collection, index, laneCount)` — first-member emits, subsequent extract
- `spmdBuildMergedGatherIndex(group, baseIdx, laneCount)` — replicate + offset + mask
- `spmdExtractSwizzleColumn(swizzled, memberPos, stride, laneCount)` — shuffle + bitcast

#### Task 5: Integration Test
**Files:** test source + expected output

Verify ipv4-parser still produces correct output after the optimization. Compare WASM instruction counts before/after.

## Risks

1. **Predicated paths**: Index instructions from different switch cases may have the same base but be under different masks. The column extraction must respect the existing bitselect merging — it should not try to merge across predicated paths. Each predicated path gets its own gather group.

2. **Index ordering**: The compiler processes instructions in dominator order. The first Index in a group triggers the merged swizzle. Subsequent Indexes must occur later in the same dominator-ordered traversal. This is guaranteed if they're in the same block (which they are after predication linearizes control flow).

3. **Non-contiguous offsets**: If a group has offsets [0, 2] (gap at 1), the stride must account for the gap. The current design uses a fixed stride of `max_offset + 1 + padding`. Gaps get 0xFF indices (swizzle returns 0, harmless).

4. **Register pressure**: The merged swizzle result must be live until all column extractions are done. For a group of 3 in a scope with other SIMD values, this adds 1 v128 register worth of pressure. Acceptable.

## What This Does NOT Replace

- `lanes.Swizzle` as a user-facing API (still needed for manual Muła-style table lookup)
- Cross-lane operations (`Rotate`, `Swizzle`, `Broadcast`)
- General gather/scatter for non-byte types or arrays > 16 bytes

This optimization is specifically about **coalescing multiple byte-level gathers from the same small array** — a common pattern in text/byte processing kernels.
