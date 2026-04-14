# Design Spec: Divergent Inner Loops N>1

**Date**: 2026-04-12
**Status**: Draft
**Motivation**: `go for i, v := range sliceOfSlices` where elements are `[]T` currently forces N=1 (scalar degeneration) because the slice header size (24 bytes) exceeds the SIMD register. The inner element type `T` IS vectorizable — use its size for lane count instead.

## 1. Scope

Enable N>1 SIMD lanes for outer loops iterating over slice-of-slices. Three changes across three repos.

**Test case**: `array-counting` — `go for i, secondLevel := range arrays` where `arrays` is `[][]int`.

**Success criteria**: `array-counting` produces correct results with N>1 on WASM and x86.

## 2. Lane Count Determination

**Current**: `sizeof([]int32) = 24` → N=0 → fallback N=1.

**Fix**: When the range element type is a slice `[]T`, peel one level and use `sizeof(T)` for lane count.

### Type checker (go/types + types2)

In `getTypeSize` or the lane count computation, detect `*types.Slice` element type and return the element type's size instead of the slice header size (24 bytes).

### TinyGo

In `spmdRangeIndexLaneCount`, when the element type is a slice, use the slice's element type size for lane count computation. `[]int32` → `int32` → 4 bytes → N=4 (SSE 128-bit).

Only applies to slice element types. Other aggregates (structs, interfaces) remain N=1.

## 3. SSA Predication — Include Varying-Bound Inner Loops

Currently `spmdLoopScopeBlocks` excludes inner loop blocks from SPMD scope (line ~1227 in spmd_predicate.go): "Inner loops operate on scalar values."

**Fix**: When an inner loop's bound condition involves a varying value (`j < len(v)` where `len(v)` is `Varying[int]`), include the inner loop's blocks in the SPMD scope.

Detection: check if the loop header's If condition compares against a value with `*types.SPMDType`. If varying, this is a divergent inner loop — don't exclude it.

The existing varying If predication handles the rest:
- `IsVarying = true` on the inner loop's header If
- Active mask narrows as lanes finish their slices
- `predicateVaryingBreaks` handles per-lane early exit
- Loop exits when all lanes have `j >= len(v)` (mask = all-zeros)

Non-varying inner loops (uniform bounds) remain excluded.

## 4. TinyGo — Varying Slice Representation

With N=4 lanes, `v` is `Varying[[]int32]` = `[4 x {ptr, len, cap}]` (array of 4 slice headers).

### len(v) extraction

Extract the `len` field from each lane's slice header → build `Varying[int]` = `<4 x i32/i64>`:
```
for lane := 0..3:
    header := extractvalue([4 x sliceStruct], lane)
    len_lane := extractvalue(header, 1)  // field 1 = len
```

### v[j] element access (inner loop gather)

Extract each lane's data pointer, GEP at uniform offset `j`, gather:
```
for lane := 0..3:
    header := extractvalue([4 x sliceStruct], lane)
    ptr_lane := extractvalue(header, 0)  // field 0 = data ptr
    gep := GEP(ptr_lane, j)             // uniform index j
ptrVec = <4 x ptr> from gep results
elem = llvm.masked.gather(ptrVec, mask)  // → <4 x int32>
```

### Inner loop structure

The uniform counter `j` increments by 1 each iteration (not by lane count). Each iteration processes one element per lane from their respective slices. The varying comparison `j < len(v)` produces a narrowing mask.

## 5. Files Modified

| Repository | File | Change |
|-----------|------|--------|
| go | `src/cmd/compile/internal/types2/check_ext_spmd.go` | Peel slice for getTypeSize |
| go | `src/cmd/compile/internal/types2/stmt_ext_spmd.go` | Use inner elem size for lane count |
| go | `src/go/types/` equivalents | Same changes |
| x-tools-spmd | `go/ssa/spmd_predicate.go` | Don't exclude varying-bound inner loops |
| tinygo | `compiler/spmd.go` | Peel slice in spmdRangeIndexLaneCount |
| tinygo | `compiler/spmd.go` or `compiler.go` | Per-lane len extraction + gather for inner loop access |
