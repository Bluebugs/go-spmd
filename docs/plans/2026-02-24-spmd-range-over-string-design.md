# SPMD Range-Over-String Design

**Date**: 2026-02-24
**Status**: Approved
**Scope**: TinyGo backend — make `go for i, c := range str` produce Varying[rune] via ASCII fast path + multi-byte runtime fallback

## Problem

The Go type checker already accepts `go for i, c := range str` and assigns `Varying[int]` / `Varying[rune]` to the iteration variables. However, the TinyGo backend has zero awareness of the `rangeiter.body` SSA pattern that `go/ssa` generates for string ranges. The `analyzeSPMDLoops` function only detects `rangeint.body` and `rangeindex.body`, so string iteration silently falls through to scalar execution.

The fundamental challenge is that UTF-8 string iteration has variable-length byte strides (1-4 bytes per rune), making direct SPMD vectorization impossible in the general case.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Primary target | ASCII-dominated strings | Most real-world string processing (logs, CSV, JSON, protocols) is ASCII. Multi-byte is handled correctly but slower. |
| ASCII check granularity | Per-chunk (4 bytes) | Each iteration checks if the current 4 bytes are all ASCII. Non-ASCII chunks fall back to runtime. No upfront string scan. |
| Lane count | 4 lanes (rune-width) | Matches `Varying[rune]` = `Varying[int32]` = `<4 x i32>`. Consistent with type system. Simpler than 16-lane decomposed mode. |
| Multi-byte fallback | Per-lane decode | Decode 4 runes sequentially via `decodeUTF8`, pack into vectors, execute body once. Reuses existing runtime. |
| Architecture | Hybrid: inline ASCII IR + runtime fallback | ASCII fast path is trivial LLVM IR (load + compare + zext). Runtime handles all UTF-8 edge cases in Go. |

## Architecture Overview

```
go for i, c := range str {
    // body uses i: Varying[int], c: Varying[rune]
}
```

Compiled structure:

```
entry:
    offsetAlloca = alloca i32
    store 0, offsetAlloca
    → rangeiter.loop

rangeiter.loop:
    offset = load offsetAlloca
    remaining = len(str) - offset
    cmp remaining <= 0 → done / check_ascii

check_ascii:
    cmp remaining < 4 → multibyte_path / load_and_check

load_and_check:
    strPtr = getelementptr str.data, offset
    load4 = load <4 x i8> from strPtr
    highBits = and <4 x i8> load4, <0x80 x 4>
    allZero = icmp eq <4 x i8> highBits, <0 x 4>
    allAscii = reduce_and allZero              // v128.all_true on WASM
    br allAscii → ascii_path / multibyte_path

ascii_path:
    runes = zext <4 x i8> to <4 x i32>
    indices = <offset, offset+1, offset+2, offset+3>
    tailMask = indices < len(str)
    nextOffset = offset + 4
    → merge

multibyte_path:
    call runtime.stringNextVarying4(str, offset)
    // unpack [4]rune → <4 x i32>, [4]int → <4 x i32>
    tailMask = laneIndex < runeCount
    nextOffset = offset + byteCount
    → merge

merge:
    runes_merged = phi [ascii_runes, mb_runes]
    indices_merged = phi [ascii_indices, mb_indices]
    mask_merged = phi [ascii_tail, mb_tail]
    store nextOffset, offsetAlloca
    // push mask_merged onto mask stack
    // execute body with value overrides
    // pop mask stack
    → rangeiter.loop

done:
    // loop finished
```

When remaining < 4, skip directly to multibyte_path (at most 3 runes — not worth optimizing).

## Component 1: SSA Detection in `analyzeSPMDLoops`

New Pass 3 in `spmd.go:analyzeSPMDLoops` detects the `rangeiter` pattern:

**SSA pattern to match:**
```
rangeiter.loop:
    t0 = phi [range_init, entry], [t0, rangeiter.body]  // opaque iter
    t1 = next t0          // *ssa.Next{IsString: true} → (bool, int, rune)
    t2 = extract t1 #0    // ok: bool
    if t2 → rangeiter.body / rangeiter.done

rangeiter.body:
    t3 = extract t1 #1    // key: int (byte index)
    t4 = extract t1 #2    // value: rune
    ... body ...
    jump → rangeiter.loop
```

**Detection steps:**
1. Find blocks with comment `"rangeiter.body"` inside an SPMD loop (`isInSPMDLoop`)
2. Find predecessor `"rangeiter.loop"` block
3. In loop block, find `*ssa.Next` with `IsString == true`
4. Find `*ssa.Range` via `next.Iter.(*ssa.Range)`
5. Extract string value from `range.X`
6. Find `*ssa.Extract` #1 (key) and #2 (value) used in body

**New fields on `spmdActiveLoop`:**
```go
isStringRange   bool          // true for range-over-string
stringValue     ssa.Value     // the string being ranged over
nextInstr       *ssa.Next     // the *ssa.Next SSA instruction
extractKey      *ssa.Extract  // extract #1 (byte index)
extractValue    *ssa.Extract  // extract #2 (rune)
```

Does NOT reuse `iterPhi` / `incrBinOp` / `boundValue` — string iteration manages offset via stack alloca, not SSA phi.

## Component 2: Runtime Helper

New function in `tinygo/src/runtime/string.go`:

```go
// stringNextVarying4 decodes up to 4 runes from s starting at byteOffset.
func stringNextVarying4(s string, byteOffset int) (
    runes [4]rune, indices [4]int, byteCount int, runeCount int,
) {
    off := uintptr(byteOffset)
    for lane := 0; lane < 4; lane++ {
        if off >= uintptr(len(s)) {
            break
        }
        indices[lane] = int(off)
        r, length := decodeUTF8(s, off)
        runes[lane] = r
        off += length
        runeCount++
    }
    byteCount = int(off) - byteOffset
    return
}
```

Reuses existing `decodeUTF8`. Returns fixed-size arrays (compiler packs into `<4 x i32>`). `runeCount` provides lane count for tail masking.

## Component 3: Body Prologue — `emitSPMDStringPrologue`

New function in `spmd.go`, called from body block entry when `isStringRange == true`.

**Offset management**: Stack alloca (`i32`) created in loop preheader, loaded/stored explicitly. Replaces the opaque `stringIterator`.

**ASCII fast path**: Load `<4 x i8>`, AND with `<0x80 x 4>`, check all zero via `v128.all_true`. If all ASCII: `zext <4 x i8> to <4 x i32>` for runes, build index vector, advance by 4.

**Multi-byte slow path**: Call `runtime.stringNextVarying4`, unpack returned arrays into vectors.

**Tail guard**: When `remaining < 4`, skip directly to multibyte_path (runtime handles tail correctly).

**Merge**: Phi nodes merge both paths, produce `runes_merged`, `indices_merged`, `mask_merged`.

## Component 4: Value Overrides and SSA Suppression

**Value overrides** (in body block after prologue):
```go
b.spmdValueOverride[loop.extractKey]   = indicesMerged   // Varying[int]
b.spmdValueOverride[loop.extractValue] = runesMerged     // Varying[rune]
```

**Suppressed SSA instructions:**
- `*ssa.Range`: Skip `stringIterator` alloca (offset managed by prologue)
- `*ssa.Next{IsString:true}`: Skip `runtime.stringNext` call (handled by prologue)
- `*ssa.If` on `extract #0`: Skip ok-check (prologue handles `remaining <= 0`)

The body enters through the merge block with a valid mask. `getValue()` redirects extract instructions to the varying vectors.

## Component 5: Infrastructure Integration

| Feature | Status | Notes |
|---------|--------|-------|
| Mask stack | Works unchanged | `mask_merged` pushed before body, popped after |
| Break masks | Works unchanged | Accumulate as normal, early exit applies |
| Continue | Works unchanged | Jumps to `rangeiter.loop`, loads next offset |
| Loop peeling | Not applicable | Variable stride prevents aligned bound computation |
| Store coalescing | Works unchanged | Operates on body memory ops, independent of iteration |
| Bounds check elision | Works unchanged | Body ops analyzed independently |

**Explicitly not supported:**
- `Varying[string]` as range expression (string must be uniform)
- Nested `go for` inside string range body (forbidden by type checker)
- Break under varying condition (forbidden by type checker)
