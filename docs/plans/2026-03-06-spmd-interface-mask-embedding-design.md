# SPMDType Interface Mask Embedding Design

> **Goal:** When `lanes.Varying[T]` is boxed into `interface{}` inside a masked context (varying if, SPMD function body, loop tail), embed the per-block condition mask alongside the value so unboxing can restore which lanes are valid and thread the mask into subsequent operations.

**Tech Stack:** x-tools-spmd (go/ssa), Go compiler (go/types), TinyGo compiler (interface.go, spmd.go, compiler.go)

**Depends on:** SPMDType interface boxing/unboxing (completed 2026-03-05, commits 62d6265, f573ae0, 901b13f, a6395c5, b6a49c7)

---

## Motivation

After SSA-level predication linearizes a varying-if, the then-block becomes straight-line code. `MakeInterface` in the then-block executes for ALL lanes, but only some lanes have valid data. Without the mask, inactive lanes contain garbage that can cause silent corruption when the unboxed value is used as an array index, pointer offset, or division operand.

Zeroing inactive lanes on unbox is insufficient — a zero used as an index still accesses element 0 (wrong data, not a crash). The mask must propagate as the base mask for all subsequent operations.

## Approach: Optional SPMDMask field on MakeInterface + SPMDExtractMask instruction

Two existing patterns for SPMD annotations in go/ssa:

| Pattern | Used by | When |
|---------|---------|------|
| New instruction type | SPMDStore, SPMDLoad, SPMDSelect, SPMDIndex | Semantics fundamentally change |
| Optional field on existing instruction | CallCommon.SPMDMask, IndexAddr.SPMDMask, Index.SPMDMask | Same semantics, mask is metadata |

`MakeInterface` semantics don't change — it still boxes a value into an interface. The mask is metadata for the backend. This matches the optional-field pattern.

For unboxing, a new `SPMDExtractMask` instruction is needed because no existing instruction extracts a mask from an interface — this is new semantics.

---

## Section 1: go/ssa — Boxing (MakeInterface)

Add two fields to `MakeInterface` in `ssa.go`:

```go
type MakeInterface struct {
    register
    X         Value
    SPMDMask  Value // nil = no masking; set for varying values boxed in masked context
    SPMDLanes int   // lane count; 0 when SPMDMask is nil
}
```

Update `Operands()` to include mask when non-nil:

```go
func (v *MakeInterface) Operands(rands []*Value) []*Value {
    rands = append(rands, &v.X)
    if v.SPMDMask != nil {
        rands = append(rands, &v.SPMDMask)
    }
    return rands
}
```

### Where masks are set

In `spmd_predicate.go`, add `case *MakeInterface` to:

1. **`spmdMaskMemOps`** — per-block mask for then/else blocks inside varying-if:
   ```go
   case *MakeInterface:
       if _, ok := instr.X.Type().(*types.SPMDType); ok {
           instr.SPMDMask = mask
           instr.SPMDLanes = lanes
           spmdAddReferrer(mask, instr)
       }
   ```

2. **`spmdConvertScopedMemOps`** — straight-line code within SPMD scope (uses scope-level active mask).

3. **`spmdMaskScopedCallOps`** (or a new sibling function) — default mask for MakeInterface in straight-line scope, analogous to how calls get a default mask.

---

## Section 2: go/ssa — Unboxing (SPMDExtractMask)

New SSA instruction that extracts the embedded mask from an interface holding a `Varying[T]`:

```go
type SPMDExtractMask struct {
    register
    X     Value // the interface value
    Lanes int   // expected lane count
}
```

Returns `Varying[mask]` — the mask that was embedded at boxing time.

### Placement and guard

`SPMDExtractMask` must only execute on the TypeAssert success path. It is placed after the TypeAssert succeeds (in the `typeassert.ok` equivalent block or after the CommaOk check). If the TypeAssert fails, the mask is irrelevant.

### Mask threading

After the predication pass processes a scope, a new sub-pass scans for TypeAssert on SPMDType and inserts mask narrowing:

```
t1 = typeassert x.(Varying[int])     // regular TypeAssert
t2 = spmd_extract_mask<4> x          // extract mask from interface
t3 = t2 AND activeMask               // narrow scope mask
// subsequent SPMDStore/SPMDLoad/Call.SPMDMask in scope use t3
```

This mirrors how varying-if produces `thenMask = activeMask & condition` — TypeAssert produces `narrowedMask = activeMask & interfaceMask`.

### Boilerplate

Required additions in `ssa.go`:
- Type definition with `register`, `X`, `Lanes` fields
- `Operands()` returning `[]*Value{&v.X}`
- `Pos()` returning `token.NoPos` (synthetic)
- `String()` method

In `print.go`: format as `spmd_extract_mask<N> t0`

In `sanity.go`: empty case (no special validation needed).

---

## Section 3: Boxed representation

Current: `Varying[T]` boxes as `[N]T` array.
New: `Varying[T]` boxes as `struct{ Value [N]T; Mask [N]int32 }`.

This is always the format — masked or not. In unmasked contexts (`SPMDMask == nil`), TinyGo fills mask with all-ones. Single type code for `Varying[T]` regardless of boxing context.

### Type code mapping

In `getTypeCode` (`interface.go`), change SPMDType mapping:

```go
if spmdType, ok := typ.(*types.SPMDType); ok && spmdType.IsVarying() {
    elemLLVM := c.getLLVMType(spmdType.Elem())
    laneCount := c.spmdEffectiveLaneCount(spmdType, elemLLVM)
    arrayType := types.NewArray(spmdType.Elem(), int64(laneCount))
    maskArrayType := types.NewArray(types.Typ[types.Int32], int64(laneCount))
    structType := types.NewStruct([]*types.Var{
        types.NewVar(token.NoPos, nil, "Value", arrayType),
        types.NewVar(token.NoPos, nil, "Mask", maskArrayType),
    }, nil)
    return c.getTypeCode(structType)
}
```

---

## Section 4: TinyGo — Boxing (MakeInterface case)

In `compiler.go`, the `*ssa.MakeInterface` case:

```go
case *ssa.MakeInterface:
    val := b.getValue(expr.X, getPos(expr))
    if spmdType, ok := expr.X.Type().(*types.SPMDType); ok && spmdType.IsVarying() {
        valArr := b.vectorToArray(val)
        var maskArr llvm.Value
        if expr.SPMDMask != nil {
            maskVec := b.getValue(expr.SPMDMask, getPos(expr))
            maskArr = b.vectorToArray(maskVec)
        } else {
            // All-ones mask: all lanes valid
            maskArr = allOnesMaskArray(laneCount)
        }
        // Pack struct{[N]T, [N]int32}
        packed := llvm.Undef(structType)
        packed = b.CreateInsertValue(packed, valArr, 0, "")
        packed = b.CreateInsertValue(packed, maskArr, 1, "")
        return b.createMakeInterface(packed, expr.X.Type(), expr.Pos()), nil
    }
    return b.createMakeInterface(val, expr.X.Type(), expr.Pos()), nil
```

---

## Section 5: TinyGo — Unboxing (createTypeAssertSPMD)

Update `createTypeAssertSPMD` to extract the `struct{[N]T, [N]int32}`:

1. Compare type code against the struct type (not array)
2. On success: extract `struct{[N]T, [N]int32}` from interface
3. Split into value array (field 0) and mask array (field 1)
4. Convert value array to vector via `arrayToVector`
5. Convert mask array to mask vector via `arrayToVector`
6. Return the value vector (mask is handled separately by SPMDExtractMask)

---

## Section 6: TinyGo — SPMDExtractMask lowering

New case in `compiler.go` for `*ssa.SPMDExtractMask`:

1. Get the interface value
2. Extract the packed struct from the interface (`extractValueFromInterface`)
3. Extract field 1 (mask array) from the struct
4. Convert mask array to mask vector via `arrayToVector`
5. Return the mask vector

---

## Section 7: Non-masked contexts

When `MakeInterface` has `SPMDMask == nil` (boxing outside any varying-if), TinyGo embeds all-ones mask. On extract, `AND(all-ones, activeMask) == activeMask` — no narrowing, correct behavior.

---

## Section 8: Test strategy

1. **x-tools-spmd unit test**: Verify `MakeInterface.SPMDMask` is set by `spmdMaskMemOps` for SPMDType operands in varying-if blocks
2. **x-tools-spmd unit test**: Verify `SPMDExtractMask` is inserted after TypeAssert on SPMDType and mask is AND'd with active mask
3. **TinyGo LLVM test**: Verify boxing produces `struct{[N]T, [N]int32}` with correct mask
4. **TinyGo LLVM test**: Verify unboxing extracts both value and mask
5. **E2E test**: `type-switch-varying` test exercises full box/unbox round-trip in masked context
