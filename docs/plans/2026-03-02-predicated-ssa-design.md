# Predicated SSA for SPMD

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Move SPMD control flow linearization from TinyGo's LLVM layer into go/ssa as a post-lift predication pass, producing predicated SSA with explicit select, masked-load, and masked-store instructions.

**Architecture:** Three new instruction types (`SPMDSelect`, `SPMDLoad`, `SPMDStore`) are added to go/ssa, each carrying an explicit `LaneCount` derived from the SPMD loop's element type — not from individual value types. A new `predicateSPMD()` pass runs after `lift()` and consumes the existing metadata (`If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`) to transform varying control flow into linear blocks with mask-gated operations. All masks use the `Varying[mask]` type (platform-native format) with explicit `Convert` instructions at the `Varying[bool]` boundary. Lane count propagates from the loop to all operations, so TinyGo never infers it from value types. TinyGo then does mechanical translation from predicated SSA to LLVM IR, decomposing wider-than-register operations into multiple groups when needed.

**Tech Stack:** Go (x-tools-spmd/go/ssa, x-tools-spmd/go/types/spmd), TinyGo compiler (compiler/spmd.go), LLVM IR

---

## Background & Motivation

### Current Architecture (metadata + TinyGo reconstruction)

```
go/ssa builder                           TinyGo compiler/spmd.go
─────────────                           ──────────────────────────
Emits standard SSA:                      Pattern-matches metadata:
  If{IsVarying: true}                      preDetectVaryingIfs()
  Phi at merge points                      spmdAnalyzeVaryingIf()
  UnOp(MUL) for loads                      spmdCreateMergeSelect()
  Store for stores                         spmdMaskedLoad/Store()
                                           spmdMaskStack push/pop
Provides metadata:                         spmdPopulateSwitchChains()
  SPMDLoopInfo                             spmdPopulateCondChains()
  SPMDSwitchChain                          spmdFindMerge()
  SPMDBooleanChain                         spmdFindThenExits()
  If.IsVarying                             ... (~3000 lines of analysis)
```

**Problem:** TinyGo must reverse-engineer SPMD intent from an SSA form not designed to express it. This reconstruction is:
- **Fragile**: Phi-to-select detection, merge finding, then-exit collection all pattern-match CFG topology
- **Bug-prone**: Most ipv4-parser bugs, boolean chain issues, and switch chain problems stem from reconstruction failures
- **Growing**: Each new SPMD feature requires more metadata in go/ssa AND more reconstruction code in TinyGo
- **Backend-specific**: Another backend would need to reimplement all ~3000 lines

### Proposed Architecture (predicated SSA)

```
go/ssa builder            go/ssa predicateSPMD()          TinyGo compiler
─────────────            ──────────────────────          ──────────────
Emits standard SSA       Transforms varying control:      Mechanical translation:
  + existing metadata  →   If(varying) → linear + masks →   SPMDSelect → llvm select
                           Phi → SPMDSelect                  SPMDLoad → llvm masked.load
                           Load → SPMDLoad(mask)              SPMDStore → llvm masked.store
                           Store → SPMDStore(mask)
```

**Key insight:** The builder already knows during construction which conditions are varying and which blocks form switch/boolean chains. The metadata exists precisely to communicate this. A predication pass in go/ssa can consume this metadata directly — in the same package where it was produced — and output a clean predicated form that any backend can consume mechanically.

### Why Post-Lift (not during construction)

The `lift()` pass promotes `Alloc` instructions to `Phi` nodes. Since `SPMDSelect` replaces `Phi` at varying merge points, the predication pass must run AFTER lift:

```
finishBody() sequence:
  1. optimizeBlocks()           ← block fusion, jump threading
  2. resolveSPMDSwitchChains()  ← fix stale block pointers
  3. resolveSPMDBooleanChains()
  4. buildReferrers()
  5. buildDomTree()
  6. lift()                     ← Alloc → Phi promotion
  7. resolveSPMDLoops()         ← find iter phi, accumulators
  8. predicateSPMD()            ← NEW: varying control flow → predicated form
```

## Mask Type Design: `Varying[mask]`

### Why not `Varying[bool]`?

`Varying[bool]` is the wrong abstraction for execution masks:

| | `Varying[bool]` | Execution mask |
|---|---|---|
| **Lane count** | Fixed: `128/1 = 16` lanes | Matches data: 4 for i32, 8 for i16, 16 for i8 |
| **Platform format** | Always `<N x i1>` conceptually | WASM: `<N x i32>` sign-extended; ARM: `<N x i1>` |
| **Semantics** | Boolean vector (user data) | Active lane tracker (compiler internal) |

This mismatch is why TinyGo currently has ~200 lines of mask format reconciliation (`spmdWrapMask`, `spmdUnwrapMaskForIntrinsic`, `spmdConvertMaskFormat`, `spmdMatchMaskFormat`, `spmdVaryingBoolPhiType`, context-dependent `Varying[bool]` constant conversion).

### `MaskType` (already exists)

`MaskType` is a non-parameterized singleton defined in `x-tools-spmd/go/types/spmd/mask.go`:

```go
type MaskType struct{}
var MaskInstance = &MaskType{}
```

It represents an opaque execution mask — no concrete width, no platform commitment. The backend decides the representation:
- WASM 4-lane: `<4 x i32>` (sign-extended, fills 128-bit v128)
- WASM 8-lane: `<8 x i16>`
- WASM 16-lane: `<16 x i8>`
- ARM 4-lane: `<4 x i1>` (native boolean vector)

`Varying[mask]` = `SPMDType{VaryingQualifier, MaskInstance}` — compiler-internal, never in user code.

### Explicit Conversions at SSA Level

The predication pass inserts `Convert` at the `Varying[bool]` ↔ `Varying[mask]` boundary:

```
// Comparison produces Varying[bool]
cond = BinOp(GT, iter, 5)               // type: Varying[bool]

// Pass inserts explicit conversion to mask
mask_cond = Convert(cond, Varying[mask]) // type: Varying[mask]

// All mask operations in platform-native format
then_mask = BinOp(AND, active_mask, mask_cond)      // type: Varying[mask]
else_mask = BinOp(AND_NOT, active_mask, mask_cond)   // type: Varying[mask]

// Predicated instructions take Varying[mask]
SPMDSelect(then_mask, x, y)
SPMDLoad(addr, then_mask)
SPMDStore(addr, val, then_mask)

// Convert back only when needed (store to Varying[bool] var, reduce.Any(), etc.)
bool_val = Convert(then_mask, Varying[bool])  // type: Varying[bool]
```

### Lane Count from Context

`MaskType` is non-parameterized (singleton). The lane count comes from the SPMD loop context, not the type. This works because all masks within an SPMD scope share the same lane count. The backend determines the LLVM type from the active loop's lane count — no context-dependent type mapping needed at the SSA level.

### TinyGo Mapping for Conversions

| SSA conversion | TinyGo action |
|---|---|
| `Convert(Varying[bool] → Varying[mask])` | `spmdWrapMask(val, laneCount)` — SExt on WASM, no-op on ARM |
| `Convert(Varying[mask] → Varying[bool])` | `spmdUnwrapMaskForIntrinsic(val, laneCount)` — Trunc on WASM, no-op on ARM |

TinyGo no longer decides **where** to insert these conversions — the SSA tells it. This eliminates `spmdConvertMaskFormat`, `spmdMatchMaskFormat`, `spmdVaryingBoolPhiType`, `spmdIsVaryingBoolPhi`, and context-dependent `Varying[bool]` constant conversion (~140 lines).

## Lane Count Propagation

### The Problem: Type-Derived Lane Counts Are Wrong

Currently, TinyGo derives lane count from each value's element type: `Varying[int32]` → `128/32 = 4` lanes, `Varying[byte]` → `128/8 = 16` lanes. This breaks when the loop's element type differs from the value type:

```go
go for i := range data {  // data is []byte → LaneCount = 16
    idx := lanes.Index()  // Returns Varying[int] → TinyGo computes 4 lanes (WRONG!)
    val := data[i]        // Varying[byte] → 16 lanes (correct)
    x := int32(val) * 2   // Varying[int32] → TinyGo computes 4 lanes (WRONG!)
}
```

The loop processes 16 bytes per iteration. ALL operations must be 16-lane. An `int32` operation on 16 lanes requires 4 WASM v128 registers (decomposition), but the SSA should say "16 lanes" and let TinyGo handle the register allocation.

### The Rule: Lane Count Comes From the Loop, Not the Type

The `go for` range's element type determines the lane count for the entire scope:

| Range type | Element | Lane count (WASM128) | All operations |
|---|---|---|---|
| `range []byte` | `byte` | 16 | 16-lane, decompose wider ops |
| `range []int16` | `int16` | 8 | 8-lane, decompose wider ops |
| `range []int32` | `int32` | 4 | 4-lane, natural fit |
| `range []int64` | `int64` | 2 | 2-lane |
| `range N` (int) | `int` | 4 | 4-lane (int = i32 on WASM32) |

This lane count is already computed correctly in `SPMDLoopInfo.LaneCount` (from the type checker). The predication pass propagates it to every predicated instruction.

### Explicit LaneCount on All Predicated Instructions

Every `SPMDSelect`, `SPMDLoad`, `SPMDStore`, and `Convert(bool↔mask)` carries an explicit `LaneCount int` field. TinyGo reads this directly — never infers from value types.

```go
SPMDSelect{Mask: m, X: x, Y: y, Lanes: 16}     // 16 lanes even for int32 values
SPMDLoad{Addr: ptr, Mask: m, Lanes: 16}          // loads 16 elements
SPMDStore{Addr: ptr, Val: v, Mask: m, Lanes: 16} // stores 16 elements
Convert(cond, Varying[mask], Lanes: 16)           // 16-lane mask
```

### Backend Decomposition

When `LaneCount` exceeds what fits in a single SIMD register for the value type, TinyGo decomposes into multiple register groups:

```
SPMDSelect{Mask: m, X: x, Y: y, Lanes: 16}  where X is Varying[int32]
  → 16 int32 values = 4 WASM v128 registers
  → TinyGo emits 4 × i32x4.select operations
  → Mask <16 x i8> decomposed into 4 × <4 x i32> masks (extract + widen)
```

For byte-native operations, no decomposition needed:
```
SPMDSelect{Mask: m, X: x, Y: y, Lanes: 16}  where X is Varying[byte]
  → 16 byte values = 1 WASM v128 register
  → TinyGo emits 1 × i8x16.select operation
  → Mask <16 x i8> used directly
```

### Index Decomposition

In a byte loop (16 lanes), `lanes.Index()` returns `[0,1,2,...,15]`. The natural representation is `Varying[byte]` — all 16 indices fit in a single byte and a single v128 register:

```
SPMDIndex{Lanes: 16, ElemType: byte}  → <16 x i8> [0,1,2,...,15]
```

When used for byte array indexing (`data[i]`), the byte index works directly — contiguous load of 16 bytes. When widened for int32 arithmetic, TinyGo decomposes:

```
idx = SPMDIndex{Lanes: 16, ElemType: byte}     // <16 x i8>
wide = Convert(idx, Varying[int32], Lanes: 16) // 16 int32 values → 4 registers
result = BinOp(MUL, wide, two)                 // 4 × i32x4.mul
narrow = Convert(result, Varying[byte], Lanes: 16) // back to <16 x i8>
```

The SSA makes all of this explicit: lane count on every predicated instruction, explicit Convert for type changes, explicit narrowing/widening. TinyGo's job is purely mechanical translation with register decomposition.

### What This Eliminates

| TinyGo code | Why eliminated |
|---|---|
| `spmdLaneCount(elemType)` for SPMD operations | Lane count on instruction, not inferred from type |
| `spmdEffectiveLaneCount()` | Same |
| `spmdRangeIndexLaneCount()` (heuristic scan) | SSA carries authoritative lane count |
| `lanes.Index()` wrong-lane-count bug | `SPMDIndex` carries loop's lane count and element type |
| Context-dependent lane count guessing | Every instruction self-describes |

## New SSA Instruction Types

### SPMDSelect

Per-lane conditional value selection. Replaces `Phi` at varying merge points.

```go
// The SPMDSelect instruction yields X where Mask is active, Y where inactive,
// evaluated per SIMD lane. It replaces Phi nodes at varying control flow
// merge points after the predicateSPMD pass.
//
// Mask must be Varying[mask] (platform-native execution mask).
// X and Y must have compatible types.
// Lanes is the SPMD lane count from the enclosing loop.
//
// Example printed form:
//
//     t5 = spmd_select<16> t2 t3 t4
type SPMDSelect struct {
    register
    Mask  Value // Varying[mask] — platform-native execution mask
    X     Value // value for active lanes
    Y     Value // value for inactive lanes
    Lanes int   // lane count from enclosing SPMD loop
}
```

**Type rule:** `SPMDSelect.Type() = X.Type()` (X and Y must have compatible types).

**Semantics:** `result[lane] = mask[lane] ? x[lane] : y[lane]` for each of `Lanes` SIMD lanes. TinyGo decomposes into multiple register operations if `Lanes * elemWidth > 128`.

### SPMDLoad

Masked vector load. Replaces `UnOp{Op: token.MUL}` (pointer dereference) when inside a varying conditional path.

```go
// The SPMDLoad instruction loads a value from Addr, but only for lanes
// where Mask is active. Inactive lanes receive the zero value of the type.
//
// Mask must be Varying[mask] (platform-native execution mask).
// Addr is a pointer; the result type is the pointer's element type.
// Lanes is the SPMD lane count from the enclosing loop.
//
// Example printed form:
//
//     t3 = spmd_load<16> t1 mask t2
type SPMDLoad struct {
    register
    Addr  Value // pointer to load from
    Mask  Value // Varying[mask] — which lanes execute the load
    Lanes int   // lane count from enclosing SPMD loop
}
```

**Type rule:** `SPMDLoad.Type() = Addr.Type().(*types.Pointer).Elem()`.

**Semantics:** `result[lane] = mask[lane] ? *addr[lane] : zero` for each of `Lanes` lanes. Prevents faulting on inactive lanes with invalid addresses. TinyGo decomposes into multiple masked loads if `Lanes * elemWidth > 128`.

### SPMDStore

Masked vector store. Replaces `Store` when inside a varying conditional path.

```go
// The SPMDStore instruction stores Val to Addr, but only for lanes
// where Mask is active. Inactive lanes leave memory unchanged.
//
// Mask must be Varying[mask] (platform-native execution mask).
// Lanes is the SPMD lane count from the enclosing loop.
// SPMDStore is effect-only (does not produce a value).
//
// Example printed form:
//
//     spmd_store<16> t1 t2 mask t3
type SPMDStore struct {
    anInstruction
    Addr  Value // pointer to store to
    Val   Value // value to store
    Mask  Value // Varying[mask] — which lanes execute the store
    Lanes int   // lane count from enclosing SPMD loop
}
```

**Semantics:** `if mask[lane]: *addr[lane] = val[lane]` for each of `Lanes` lanes.

### SPMDIndex

Produces per-lane indices for the SPMD loop. Replaces `lanes.Index()` with an instruction that carries the correct lane count and element type.

```go
// The SPMDIndex instruction produces a vector of consecutive lane indices
// [0, 1, 2, ..., Lanes-1] in the loop's natural element type.
//
// For a byte loop (Lanes=16): produces <16 x i8> [0,1,...,15]
// For an int32 loop (Lanes=4): produces <4 x i32> [0,1,2,3]
//
// The element type is chosen to match the loop's range element type,
// minimizing register pressure. When used in wider-type contexts,
// an explicit Convert widens the index.
//
// Example printed form:
//
//     t1 = spmd_index<16, byte>
type SPMDIndex struct {
    register
    Lanes    int        // lane count from enclosing SPMD loop
    ElemType types.Type // loop's natural element type (byte, int16, int32, etc.)
}
```

**Type rule:** `SPMDIndex.Type() = SPMDType{Varying, ElemType}`.

**Semantics:** `result[lane] = ElemType(lane)` for `lane` in `[0, Lanes)`. The index is in the narrowest type that holds the lane values, avoiding unnecessary register decomposition.

### Mask Computation (BinOp on `Varying[mask]`)

Masks are `Varying[mask]` SSA values, computed with existing `BinOp`:

```
then_mask = BinOp{AND,     active_mask, mask_cond}   // Varying[mask] & Varying[mask]
else_mask = BinOp{AND_NOT, active_mask, mask_cond}   // Varying[mask] &^ Varying[mask]
```

Go's `&^` (AND-NOT) operator maps to `token.AND_NOT`, which already exists as a valid `BinOp` operation. Extending `BinOp` to operate on `Varying[mask]` values is a minor semantic extension — these BinOps are generated by the predication pass, not from source code. The sanity checker is extended to allow AND/OR/AND_NOT/XOR on `Varying[mask]`.

The mask lane count matches the loop's lane count. For a 16-lane byte loop on WASM, masks are `<16 x i8>` (each byte is 0x00 or 0xFF). `BinOp(AND, <16 x i8>, <16 x i8>)` compiles to a single `v128.and`. When used with wider data (e.g., int32 select), TinyGo decomposes the mask alongside the data: extract 4-byte groups, widen to `<4 x i32>`, apply per-group.

## Transformation Examples

### Varying If/Else

**Source:**
```go
go for i := range 16 {
    if i > 5 {
        result[i] = a[i] + 1
    } else {
        result[i] = a[i] - 1
    }
}
```

**Before (standard SSA after lift):**
```
rangeint.body:
    iter = phi [entry: 0, loop: iter_next]
    cond = BinOp(GT, iter, 5)                   // Varying[bool]
    if cond goto if.then else if.else            // If{IsVarying: true}

if.then:
    a_val = *a_ptr                               // UnOp{MUL}
    sum = BinOp(ADD, a_val, 1)
    Store(result_ptr, sum)
    jump if.done

if.else:
    a_val2 = *a_ptr                              // UnOp{MUL}
    diff = BinOp(SUB, a_val2, 1)
    Store(result_ptr, diff)
    jump if.done

if.done:
    jump rangeint.loop
```

**After predicateSPMD() (range-over-int, LaneCount=4):**
```
rangeint.body:                                          // Lanes=4
    iter = phi [entry: 0, loop: iter_next]
    cond = BinOp(GT, iter, 5)                           // Varying[bool]
    mask_cond = Convert(cond, Varying[mask], Lanes=4)   // bool → mask
    then_mask = BinOp(AND, active_mask, mask_cond)      // Varying[mask]
    else_mask = BinOp(AND_NOT, active_mask, mask_cond)  // Varying[mask]
    jump if.then                                        // unconditional (was: If)

if.then:                                                // executes on all 4 lanes
    a_val = SPMDLoad(a_ptr, then_mask, Lanes=4)         // masked load
    sum = BinOp(ADD, a_val, 1)                          // all lanes compute
    SPMDStore(result_ptr, sum, then_mask, Lanes=4)      // masked store
    jump if.else                                        // sequential (was: jump if.done)

if.else:                                                // executes on all 4 lanes
    a_val2 = SPMDLoad(a_ptr, else_mask, Lanes=4)        // masked load
    diff = BinOp(SUB, a_val2, 1)                        // all lanes compute
    SPMDStore(result_ptr, diff, else_mask, Lanes=4)     // masked store
    jump if.done

if.done:
    jump rangeint.loop
```

**Key changes:**
1. `If{IsVarying}` → `Jump` (unconditional) + mask computation
2. Explicit `Convert(Varying[bool] → Varying[mask], Lanes=4)` at condition boundary
3. `UnOp{MUL}` → `SPMDLoad` with `Varying[mask]` mask and explicit `Lanes`
4. `Store` → `SPMDStore` with `Varying[mask]` mask
5. `if.then` falls through to `if.else` (sequential, not diamond)
6. All mask operations (AND, AND_NOT) in platform-native format — no wrapping

### Varying If/Else with Value Merge

### Byte Loop with Index Decomposition

**Source:**
```go
data := []byte{...}
go for i := range data {     // LaneCount = 16 (byte)
    if data[i] > 127 {
        result[i] = data[i] - 128
    } else {
        result[i] = data[i]
    }
}
```

**After predicateSPMD() (range-over-byte, LaneCount=16):**
```
rangeindex.body:                                              // Lanes=16
    idx = SPMDIndex(Lanes=16, ElemType=byte)                  // <16 x i8> [0..15]
    data_val = SPMDLoad(&data[base], active_mask, Lanes=16)   // 16 bytes, 1 register
    cond = BinOp(GT, data_val, 127)                           // Varying[bool]
    mask_cond = Convert(cond, Varying[mask], Lanes=16)        // 16-lane mask
    then_mask = BinOp(AND, active_mask, mask_cond)
    else_mask = BinOp(AND_NOT, active_mask, mask_cond)
    // Then path: all 16 lanes compute
    sub_val = BinOp(SUB, data_val, 128)                       // 16 bytes, 1 register
    // Merge
    x = SPMDSelect(then_mask, sub_val, data_val, Lanes=16)    // 16 bytes, 1 register
    SPMDStore(&result[base], x, active_mask, Lanes=16)        // 16 bytes, 1 register
```

Everything fits in single v128 registers — byte data, byte index, byte mask. No decomposition needed. Compare with the int32 case below where the same loop with int32 data would require 4 registers per operation.

### Varying If/Else with Value Merge

**Source:**
```go
go for i := range 16 {
    var x lanes.Varying[int]
    if i > 5 {
        x = a[i] + 1
    } else {
        x = a[i] - 1
    }
    result[i] = x
}
```

**Before (standard SSA after lift):**
```
rangeint.body:
    ...
    if cond goto if.then else if.else

if.then:
    x1 = BinOp(ADD, a_val, 1)
    jump if.done

if.else:
    x2 = BinOp(SUB, a_val, 1)
    jump if.done

if.done:
    x3 = phi [if.then: x1, if.else: x2]          // varying merge
    Store(result_ptr, x3)
    jump rangeint.loop
```

**After predicateSPMD() (LaneCount=4):**
```
rangeint.body:                                        // Lanes=4
    ...
    mask_cond = Convert(cond, Varying[mask], Lanes=4)
    then_mask = BinOp(AND, active_mask, mask_cond)
    else_mask = BinOp(AND_NOT, active_mask, mask_cond)
    jump if.then

if.then:
    x1 = BinOp(ADD, a_val, 1)                        // all 4 lanes compute
    jump if.else

if.else:
    x2 = BinOp(SUB, a_val, 1)                        // all 4 lanes compute
    jump if.done

if.done:
    x3 = SPMDSelect(then_mask, x1, x2, Lanes=4)      // replaces phi
    Store(result_ptr, x3)
    jump rangeint.loop
```

**Key change:** `Phi [if.then: x1, if.else: x2]` → `SPMDSelect(then_mask, x1, x2, Lanes=4)`. Pure computations execute on all lanes (harmless speculation); the select picks the correct result per lane. The mask is `Varying[mask]` with explicit `Lanes=4`, so TinyGo emits `spmdMaskSelect()` directly (bitwise select on WASM) without conversion or lane count guessing.

### Nested Varying Conditions

```go
if a > 0 {           // outer varying
    if b > 0 {       // inner varying
        store(ptr, val)
    }
}
```

**After predicateSPMD():**
```
entry:
    cond_a = BinOp(GT, a, 0)                            // Varying[bool]
    mask_a = Convert(cond_a, Varying[mask])              // bool → mask
    outer_then = BinOp(AND, active_mask, mask_a)         // Varying[mask]
    outer_else = BinOp(AND_NOT, active_mask, mask_a)     // Varying[mask]
    jump if.then.outer

if.then.outer:
    cond_b = BinOp(GT, b, 0)                            // Varying[bool]
    mask_b = Convert(cond_b, Varying[mask])              // bool → mask
    inner_then = BinOp(AND, outer_then, mask_b)          // nested narrowing
    inner_else = BinOp(AND_NOT, outer_then, mask_b)
    jump if.then.inner

if.then.inner:
    SPMDStore(ptr, val, inner_then)                      // doubly-narrowed mask
    jump if.done.inner

if.done.inner:
    jump if.done.outer

if.done.outer:
    ...
```

Mask narrowing composes naturally: `inner_then = active_mask & mask_a & mask_b`. No explicit "mask stack" — each mask is a `Varying[mask]` SSA value computed from its parent. All AND/AND_NOT operations stay in platform-native format (e.g., `<4 x i32>` on WASM) — zero conversion overhead within the mask domain.

### Varying Switch

```go
switch x {
case 1: f1()
case 2: f2()
default: f3()
}
```

**After predicateSPMD():**
```
entry:
    eq1 = BinOp(EQ, x, 1)                                       // Varying[bool]
    meq1 = Convert(eq1, Varying[mask])                           // bool → mask
    mask1 = BinOp(AND, active_mask, meq1)                        // Varying[mask]
    eq2 = BinOp(EQ, x, 2)                                       // Varying[bool]
    meq2 = Convert(eq2, Varying[mask])                           // bool → mask
    mask2 = BinOp(AND, active_mask, meq2)                        // Varying[mask]
    any_case = BinOp(OR, meq1, meq2)                             // Varying[mask]
    default_mask = BinOp(AND_NOT, active_mask, any_case)         // Varying[mask]
    jump case1.body

case1.body:
    ... // calls f1() with mask1
    jump case2.body

case2.body:
    ... // calls f2() with mask2
    jump default.body

default.body:
    ... // calls f3() with default_mask
    jump switch.done

switch.done:
    result = SPMDSelect(mask1, v1, SPMDSelect(mask2, v2, v3))   // chained select
    ...
```

### Boolean Chain (&&)

```go
if a > 0 && b > 0 {
    store(ptr, val)
}
```

**After predicateSPMD():**
```
entry:
    cond_a = BinOp(GT, a, 0)                           // Varying[bool]
    mask_a = Convert(cond_a, Varying[mask])             // bool → mask
    cond_b = BinOp(GT, b, 0)                           // Varying[bool]
    mask_b = Convert(cond_b, Varying[mask])             // bool → mask
    combined = BinOp(AND, mask_a, mask_b)               // && → AND on masks
    then_mask = BinOp(AND, active_mask, combined)       // Varying[mask]
    else_mask = BinOp(AND_NOT, active_mask, combined)   // Varying[mask]
    jump if.then

if.then:
    SPMDStore(ptr, val, then_mask)
    jump if.done

if.done:
    ...
```

The `SPMDBooleanChain` metadata tells the pass that these blocks form a chain. The pass collapses the chain of `If` blocks into a single combined mask, eliminating the intermediate blocks entirely. All operations stay in `Varying[mask]` format — the `Convert` from `Varying[bool]` happens once per condition, then everything composes in native format.

## What Moves vs What Stays

### Moves to go/ssa (predicateSPMD pass)

| Concern | Current TinyGo code | go/ssa replacement |
|---------|---------------------|-------------------|
| Varying if detection | `preDetectVaryingIfs()` ~100 lines | Pass reads `If.IsVarying` directly |
| Phi → select | `spmdCreateMergeSelect()` ~400 lines | Pass emits `SPMDSelect` |
| Mask stack | `spmdPushMask/PopMask/CurrentMask` ~30 lines | SSA values compose naturally |
| Mask transitions | `spmdMaskTransitions` map ~200 lines | Mask computed per-block during pass |
| CFG linearization | `spmdDetectVaryingIf()` + branch removal ~300 lines | Pass rewires edges |
| Switch linearization | `spmdPopulateSwitchChains()` + per-case masks ~200 lines | Pass reads `SPMDSwitchChain` |
| Boolean chain collapse | `spmdPopulateCondChains()` + combined cond ~150 lines | Pass reads `SPMDBooleanChain` |
| Merge finding | `spmdFindMerge()` ~100 lines | Pass uses dominator tree |
| Then-exit detection | `spmdFindThenExits()` ~100 lines | Pass handles during linearization |
| Value LOR detection | `spmdCreateValueLOR()` ~50 lines | Pass detects `thenEntry == merge` |
| Load masking decision | implicit from mask state tracking | Pass wraps loads in varying paths |
| Store masking decision | implicit from mask state tracking | Pass wraps stores in varying paths |
| **Total** | **~1630 lines removed** | |

### Stays in TinyGo

| Concern | Why |
|---------|-----|
| Contiguous access detection | Target-dependent (memory layout, stride analysis) |
| Gather/scatter vs masked load/store | LLVM intrinsic choice |
| `Varying[mask]` → LLVM type mapping | Target-specific: `<4 x i32>` on WASM, `<4 x i1>` on ARM |
| `Convert(bool↔mask)` lowering | Maps to `spmdWrapMask`/`spmdUnwrapMask` (mechanical) |
| Vector type widths | Target-specific lane count mapping |
| Loop peeling | Performance optimization, target-dependent |
| Builtin interception (lanes/reduce) | LLVM intrinsic mapping |
| Break mask tracking | See phased plan — moves in Phase 8 |
| LLVM IR generation | All CreateLoad/CreateStore/CreateInsertElement etc. |
| Early exit optimization | WASM-specific anytrue/alltrue intrinsics |
| Byte widening | WASM-specific sub-128-bit workaround |

### Eliminated from TinyGo (by `Varying[mask]` type)

| TinyGo code | Lines | Why eliminated |
|---|---|---|
| `spmdConvertMaskFormat()` | ~70 | SSA has explicit `Convert` at right points |
| `spmdMatchMaskFormat()` | ~20 | Types match by construction (`Varying[mask]` throughout) |
| `spmdVaryingBoolPhiType()` | ~15 | No `Varying[bool]` phis in mask contexts |
| `spmdIsVaryingBoolPhi()` | ~15 | Same — masks are `Varying[mask]` |
| Varying[bool] constant conversion | ~20 | `Convert` in SSA handles it |
| **Total** | **~140** | |

### Metadata that becomes internal to go/ssa

After the predication pass, downstream consumers no longer need:
- `If.IsVarying` — varying Ifs are removed (replaced with Jumps)
- `SPMDSwitchChain` — switch chains are linearized
- `SPMDBooleanChain` — boolean chains are collapsed

These metadata structures still exist and are populated during construction, but they're consumed by `predicateSPMD()` within go/ssa. TinyGo never reads them.

`SPMDLoopInfo` **remains public** — TinyGo still needs loop structure for peeling, lane index override, contiguous detection, and tail masking.

## predicateSPMD() Pass Algorithm

### Entry Point

```go
// predicateSPMD transforms varying control flow in SPMD functions
// into predicated form with explicit mask-gated operations.
// It runs after lift() and resolveSPMDLoops().
//
// Preconditions:
//   - All Allocs promoted to Phis by lift()
//   - SPMDLoopInfo resolved (IterPhi, Accumulators populated)
//   - SPMDSwitchChain/SPMDBooleanChain block pointers resolved
//   - Dominator tree built
//
// Postconditions:
//   - No If instructions with IsVarying=true remain
//   - All varying merge Phis replaced with SPMDSelect
//   - All loads/stores in varying paths carry masks (SPMDLoad/SPMDStore)
//   - CFG is linearized for varying control flow (sequential blocks)
func predicateSPMD(fn *Function) {
    // Only process functions with SPMD loops or varying params
    if len(fn.SPMDLoops) == 0 && !hasSPMDParams(fn) {
        return
    }
    // ... pass implementation
}
```

### Pass Phases

**Phase 1: Identify SPMD scope.** Determine which blocks are inside SPMD loops or SPMD function bodies. Only these blocks need predication.

**Phase 2: Compute active masks.** Walk blocks in dominator preorder. For each block, compute its "active mask" — the mask value that is live at that point. Initially all-ones for SPMD loop body entry. Narrowed by varying conditions.

**Phase 3: Classify Phis.** For each Phi at a merge point, determine if it merges values from varying control flow. If so, record it for SPMDSelect replacement. Use the dominator tree + metadata to distinguish varying merges from loop-back merges.

**Phase 4: Linearize.** For each varying If:
1. Compute `then_mask = AND(active_mask, cond)` and `else_mask = AND_NOT(active_mask, cond)`
2. Replace `If` terminator with `Jump` to then-block
3. Redirect then-block's exit to else-block (or done-block if no else)
4. At merge: replace each Phi with `SPMDSelect(cond, then_val, else_val)`

For each varying switch chain:
1. Compute per-case masks from tag comparisons
2. Collapse comparison chain into single entry block with all mask computations
3. Chain case bodies sequentially
4. At merge: chain `SPMDSelect` for multi-way merge

For each boolean chain:
1. Collapse intermediate blocks into single block
2. Compute combined condition with `BinOp AND` (for &&) or `BinOp OR` (for ||)
3. Use combined condition for mask narrowing

**Phase 5: Mask memory operations.** Walk blocks in the linearized order. For each `UnOp{MUL}` (load) in a block with a non-trivial active mask, replace with `SPMDLoad{Addr, Mask, Lanes}`. For each `Store` similarly, replace with `SPMDStore{Addr, Val, Mask, Lanes}`. Lane count comes from the enclosing `SPMDLoopInfo.LaneCount`.

**Phase 5b: Replace lanes.Index().** Replace `lanes.Index()` calls within SPMD scope with `SPMDIndex{Lanes, ElemType}` where `ElemType` is the loop's range element type. This produces the index in the narrowest natural type (e.g., `byte` for byte loops), avoiding the current bug where `lanes.Index()` always returns 4-lane `Varying[int]`.

**Phase 6: Clean up.** Remove dead blocks and unreachable edges created by linearization. Renumber blocks. Rebuild referrers.

### Mask Computation Strategy

Masks are `Varying[mask]` SSA values in platform-native format. No stack needed — each mask is derived from its parent via SSA value composition:

```
loop_body_mask = Varying[mask]  (all-ones for non-tail, tail-mask for last iteration)
    ├── cond_a = BinOp(GT, a, 0)                    // Varying[bool]
    │   mask_a = Convert(cond_a, Varying[mask])      // one-time conversion
    │   then_mask_a = AND(loop_body_mask, mask_a)    // Varying[mask], native ops
    │   else_mask_a = AND_NOT(loop_body_mask, mask_a)
    │       ├── cond_b = BinOp(GT, b, 0)            // Varying[bool]
    │       │   mask_b = Convert(cond_b, Varying[mask])
    │       │   then_mask_b = AND(then_mask_a, mask_b)  // nested, still native
    │       │   ...
```

After linearization, each block's active mask is computable from SSA values without runtime state. All mask AND/OR/AND_NOT operations execute in platform-native format (e.g., `<4 x i32>` on WASM → single `v128.and`). The `Convert` from `Varying[bool]` happens exactly once per comparison result. This is strictly more efficient than the current approach where TinyGo's mask stack intermixes format conversions with mask operations.

## Implementation Phases

The plan is structured for incremental migration. Each phase produces a working system. TinyGo handles both old (Phi) and new (SPMDSelect, SPMDLoad, SPMDStore) forms during the transition.

### Phase 1: New instruction types + mask type + lane count (x-tools-spmd)

Add `SPMDSelect`, `SPMDLoad`, `SPMDStore`, `SPMDIndex` to go/ssa. All predicated instructions carry `Lanes int` from the enclosing SPMD loop. All mask operands typed as `Varying[mask]` (`SPMDType{Varying, MaskInstance}`). `SPMDIndex` produces lane indices in the loop's natural element type. Extend `BinOp` sanity checking to allow AND/OR/AND_NOT/XOR on `Varying[mask]`. Extend `Convert` to allow `Varying[bool]` ↔ `Varying[mask]` conversions with `Lanes` annotation. Add emitters, printers, and sanity checks for all four new instructions.

### Phase 2: predicateSPMD() — varying if/else (x-tools-spmd)

Implement the core pass for the most common case: simple varying if/else. Handles if-with-else, if-without-else, nested ifs. At each varying condition: insert `Convert(Varying[bool] → Varying[mask])`, compute masks with `BinOp(AND/AND_NOT)` on `Varying[mask]`, linearize CFG, replace Phi→SPMDSelect, replace Load→SPMDLoad, replace Store→SPMDStore. All mask operands are `Varying[mask]` throughout.

### Phase 3: TinyGo — consume new instructions (tinygo)

Add handlers for SPMDSelect, SPMDLoad, SPMDStore, SPMDIndex. Read `Lanes` field directly from each instruction — never derive from value types. Map `Varying[mask]` type to platform LLVM type using `Lanes` (e.g., `Lanes=16` on WASM → `<16 x i8>` mask). Handle `Convert(Varying[bool] ↔ Varying[mask])` via existing `spmdWrapMask`/`spmdUnwrapMaskForIntrinsic`. SPMDSelect maps to `spmdMaskSelect()` with decomposition when `Lanes * elemWidth > 128`. SPMDLoad/SPMDStore map to existing masked load/store with decomposition. SPMDIndex maps to `spmdLaneOffsetConst(lanes, elemType)`. Remove `spmdConvertMaskFormat`, `spmdMatchMaskFormat`, `spmdVaryingBoolPhiType`, `spmdIsVaryingBoolPhi`, `spmdLaneCount` inference calls, `spmdRangeIndexLaneCount` heuristic. Skip pre-detection for blocks already linearized by the pass.

### Phase 4: predicateSPMD() — varying switch (x-tools-spmd)

Extend the pass to handle `SPMDSwitchChain`. Collapses comparison chain, computes per-case masks, chains bodies sequentially, emits chained SPMDSelect at merge.

### Phase 5: TinyGo — remove switch reconstruction (tinygo)

Remove `spmdPopulateSwitchChains()`, switch-specific merge code, `spmdSwitchChains`/`spmdSwitchIfBlocks`/`spmdSwitchBodyBlocks` maps.

### Phase 6: predicateSPMD() — boolean chains (x-tools-spmd)

Extend the pass to handle `SPMDBooleanChain`. Collapses intermediate condition blocks, computes combined condition, uses it for mask narrowing.

### Phase 7: TinyGo — remove chain reconstruction (tinygo)

Remove `spmdPopulateCondChains()`, `spmdCondChains`/`spmdCondChainInner` maps, condition chain handling in `spmdAnalyzeVaryingIf`.

### Phase 8: predicateSPMD() — break masks (x-tools-spmd)

Add break mask tracking. Break inside varying condition accumulates into a break-mask SSA value (`Varying[mask]`). Active mask = `BinOp(AND_NOT, loop_mask, break_mask)`. All in platform-native format.

### Phase 9: TinyGo — remove old mask management (tinygo)

Remove: mask stack (`spmdPushMask/PopMask/CurrentMask`), mask transitions map, merge finding (`spmdFindMerge`), then-exit detection (`spmdFindThenExits`), phi-to-select detection (`spmdCreateMergeSelect` and variants), varying if analysis (`spmdAnalyzeVaryingIf`, `spmdDetectVaryingIf`), value LOR detection.

### Phase 10: Validation

All 47 E2E tests pass. Benchmark comparison (mandelbrot, hex-encode, ipv4-parser). Code size comparison (net lines removed from TinyGo vs added to go/ssa).

## Testing Strategy

### Unit Tests (x-tools-spmd)

New test file: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

| Test | Input | Assertion |
|------|-------|-----------|
| Simple varying if/else | `if v > 5 { x = 1 } else { x = 2 }` | SPMDSelect at merge, no If.IsVarying in output |
| If without else | `if v > 5 { store }` | SPMDStore with then_mask, SPMDSelect merges with original |
| Nested if | `if a { if b { ... } }` | Inner mask = AND(outer_then, cond_b) |
| Varying switch | `switch v { case 1: ... case 2: ... }` | Per-case masks, chained SPMDSelect |
| Boolean AND chain | `if a && b { ... }` | Combined = AND(a, b), single then_mask |
| Boolean OR chain | `if a \|\| b { ... }` | Combined = OR(a, b), single then_mask |
| Mixed chain | `if a && (b \|\| c) { ... }` | Nested chain handling |
| Load in varying path | `x = *ptr` inside if | SPMDLoad with path mask |
| Store in varying path | `*ptr = x` inside if | SPMDStore with path mask |
| Load outside varying | `x = *ptr` in loop body | Remains UnOp{MUL} (not masked) |
| Uniform if (not predicated) | `if uniformCond { ... }` | If instruction preserved (not linearized) |
| Value LOR | `x = a \|\| b` | No masking, just BinOp OR |
| Break in varying if | `if v { break }` | Break mask accumulation |
| Multi-way merge | switch with fallthrough-like patterns | Chained SPMDSelect |

### Integration Tests (E2E)

All existing 47 E2E tests must pass unchanged. The predicated SSA is an internal representation change — the LLVM IR output should be semantically equivalent.

### Regression Benchmarks

Compare before/after for:
- Mandelbrot (~3.29x baseline)
- Hex-encode Dst (~4.3x) / Src (~14.8x)
- IPv4-parser (~0.5x)

Performance should be neutral or improved (fewer pass-throughs in TinyGo).

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Lift creates Phis that pass must handle | Pass must distinguish loop-back Phis from varying-merge Phis | Use dominator tree: varying-merge Phis have predecessors from both sides of a varying If |
| DomPreorder visits merge before inner blocks | SPMDSelect at merge references values not yet processed | The pass creates SPMDSelect after processing both branches (similar to TinyGo's deferred phi resolution) |
| optimizeBlocks fuses blocks after pass | Block references in SPMDSelect operands could go stale | predicateSPMD runs after optimizeBlocks (no further optimization expected) |
| BinOp AND/OR/AND_NOT on Varying[mask] | Sanity checker might reject it | Extend sanity checker to allow bitwise ops on `Varying[mask]`; cleaner than bool since mask IS a bitwise type |
| Convert(Varying[bool] ↔ Varying[mask]) is new | Convert validation might reject it | Extend Convert validation in sanity checker; conversion is always valid |
| Incremental migration creates mixed model | TinyGo must handle both Phi-to-select (old) and SPMDSelect (new) | TinyGo already has both paths; SPMDSelect handler is a simple addition |
| Performance regression from extra pass | Compilation time increases | The pass is O(blocks) per function; only runs on SPMD functions. Negligible vs LLVM codegen |

## Implementation Details

### File Inventory

**x-tools-spmd (new/modified):**
- `go/ssa/ssa.go` — Add SPMDSelect, SPMDLoad, SPMDStore, SPMDIndex struct definitions (all with `Lanes int`)
- `go/ssa/emit.go` — Add emitSPMDSelect, emitSPMDLoad, emitSPMDStore, emitSPMDIndex functions
- `go/ssa/print.go` — Add String() formatting for new instructions (include `<N>` lane annotation)
- `go/ssa/sanity.go` — Add validation for new instructions; verify `Lanes > 0`; extend BinOp to allow AND/OR/AND_NOT/XOR on `Varying[mask]`; extend Convert to allow `Varying[bool]` ↔ `Varying[mask]`
- `go/ssa/func.go` — Call predicateSPMD() in finishBody() after resolveSPMDLoops()
- `go/ssa/spmd_predicate.go` — NEW: the predication pass (propagates `SPMDLoopInfo.LaneCount` to all emitted instructions)
- `go/ssa/spmd_predicate_test.go` — NEW: tests

**tinygo (modified):**
- `compiler/spmd.go` — Add SPMDSelect/SPMDLoad/SPMDStore/SPMDIndex handlers; read `Lanes` field directly (never infer from type); map `Varying[mask]` to platform LLVM type using `Lanes`; implement register decomposition when `Lanes * elemWidth > 128`; handle `Convert(bool↔mask)` via wrap/unwrap; remove `spmdConvertMaskFormat`, `spmdMatchMaskFormat`, `spmdVaryingBoolPhiType`, `spmdIsVaryingBoolPhi`, `spmdLaneCount` inference, `spmdRangeIndexLaneCount` heuristic; incrementally remove reconstruction code
- `compiler/compiler.go` — Handle new instruction types in instruction dispatch; add `Varying[mask]` to LLVM type mapping (`getLLVMType`)

### SPMDSelect Interface Implementation

```go
func (v *SPMDSelect) String() string              { return printInstr(v) }
func (v *SPMDSelect) Type() types.Type             { return v.X.Type() }
func (v *SPMDSelect) Operands(rands []*Value) []*Value {
    return append(rands, &v.Mask, &v.X, &v.Y)
}
func (v *SPMDSelect) Pos() token.Pos              { return token.NoPos }
func (v *SPMDSelect) Referrers() *[]Instruction    { return &v.referrers }
func (v *SPMDSelect) Parent() *Function            { return v.block.parent }
func (v *SPMDSelect) Block() *BasicBlock           { return v.block }
```

### SPMDLoad Interface Implementation

```go
func (v *SPMDLoad) String() string              { return printInstr(v) }
func (v *SPMDLoad) Type() types.Type {
    return v.Addr.Type().(*types.Pointer).Elem()
}
func (v *SPMDLoad) Operands(rands []*Value) []*Value {
    return append(rands, &v.Addr, &v.Mask)
}
```

### SPMDStore Interface Implementation

```go
func (v *SPMDStore) String() string              { return printInstr(v) }
func (v *SPMDStore) Operands(rands []*Value) []*Value {
    return append(rands, &v.Addr, &v.Val, &v.Mask)
}
func (v *SPMDStore) Pos() token.Pos              { return token.NoPos }
func (v *SPMDStore) Referrers() *[]Instruction    { return nil }
func (v *SPMDStore) Parent() *Function            { return v.block.parent }
func (v *SPMDStore) Block() *BasicBlock           { return v.block }
```

### Phi Classification Heuristic

The pass must distinguish which Phis to replace with SPMDSelect:

```
For each Phi in a block B inside SPMD scope:
  1. If B has exactly 2 predecessors from both sides of a varying If → replace with SPMDSelect
  2. If B has >2 predecessors from a varying switch chain → replace with chained SPMDSelect
  3. If B is a loop header with back-edge → keep as Phi (loop-carried value)
  4. If B merges uniform control flow → keep as Phi
```

The key distinguishing signal: a Phi is a "varying merge" if its block is the merge point (done block) of a varying If or switch chain. The metadata (IsVarying, SPMDSwitchChain) identifies these precisely.

## Appendix A: TinyGo Translation Table

Complete mapping from predicated SSA to TinyGo codegen:

| SSA instruction | Mask type | TinyGo action | WASM output |
|---|---|---|---|
| `Convert(Varying[bool] → Varying[mask])` | — | `spmdWrapMask(val, laneCount)` | `i32x4.shr_s(sext, 31)` or folds with comparison |
| `Convert(Varying[mask] → Varying[bool])` | — | `spmdUnwrapMaskForIntrinsic(val, laneCount)` | `i32x4.trunc_sat` |
| `BinOp(AND, m1, m2)` | `Varying[mask]` | `CreateAnd(m1, m2)` | `v128.and` |
| `BinOp(OR, m1, m2)` | `Varying[mask]` | `CreateOr(m1, m2)` | `v128.or` |
| `BinOp(AND_NOT, m1, m2)` | `Varying[mask]` | `CreateAnd(m1, CreateNot(m2))` | `v128.andnot` |
| `SPMDSelect(mask, x, y)` | `Varying[mask]` | `spmdMaskSelect(mask, x, y)` | bitwise: `v128.or(v128.and(m,x), v128.andnot(m,y))` |
| `SPMDLoad(addr, mask)` | `Varying[mask]` | unwrap → `spmdMaskedLoad(type, ptr, i1mask)` | `v128.load` + select, or gather |
| `SPMDStore(addr, val, mask)` | `Varying[mask]` | unwrap → `spmdMaskedStore(val, ptr, i1mask)` | load + blend + `v128.store`, or scatter |

**Key:** `SPMDSelect` uses the mask in native format directly (bitwise select, no conversion). `SPMDLoad`/`SPMDStore` unwrap to `<N x i1>` for LLVM masked intrinsics. All mask-to-mask operations (AND/OR/AND_NOT) stay native — zero conversion overhead.

## Appendix B: Comparison with ISPC

ISPC's internal representation uses a similar predicated approach:

| Concept | ISPC | This design |
|---------|------|-------------|
| Mask type | `<N x i1>` (LLVM native) | `Varying[mask]` (platform-abstracted) |
| Mask tracking | `FunctionEmitContext::GetFullMask()` | SSA value composition (BinOp AND/AND_NOT on `Varying[mask]`) |
| Conditional select | `LLVMSelect(mask, trueVal, falseVal)` | `SPMDSelect(mask, x, y)` with `Varying[mask]` |
| Masked load | `llvm.masked.load` with mask | `SPMDLoad(addr, mask)` |
| Masked store | `llvm.masked.store` with mask | `SPMDStore(addr, val, mask)` |
| Control flow | Linearized during emission | Linearized in predicateSPMD pass |
| Break mask | `breakLanesAddedMask` accumulator | BinOp OR accumulation on `Varying[mask]` |
| Bool↔mask conversion | Implicit (ISPC uses i1 masks) | Explicit `Convert` instructions |

The key differences:
1. ISPC uses `<N x i1>` masks throughout — fine because ISPC targets native SIMD (SSE/AVX) where i1 is natural. We need `Varying[mask]` because WASM SIMD128 requires `<N x i32>` sign-extended masks for efficient bitwise select.
2. ISPC linearizes during LLVM IR emission (interleaved with codegen). This design separates linearization (go/ssa pass) from codegen (TinyGo), enabling independent testing and backend reuse.
3. Explicit `Convert` instructions make the bool→mask boundary visible and auditable in the SSA, rather than implicit in codegen.
