# Struct-Field Array Promotion to SIMD Vector

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Promote `[N]byte` struct fields accessed via `go for … range` to a single `v128.load` + `replace_lane` overrides, eliminating 60+ scalar `replace_lane` ops per call.

**Architecture:** Two new SSA instructions (`SPMDVectorFromPtr`, `SPMDVectorInsert`) capture the load-from-field and scalar-element-override operations at the x-tools-spmd IR level. A new promotion pass (`checkFieldArrayPromotion` + `doPromoteFieldArray`) detects the pattern `shuffleMask := entry.field; shuffleMask[k] = v; go for i, m := range shuffleMask` and rewrites it. TinyGo lowers the two instructions to `v128.load` and `insert_element` (→ WASM `replace_lane`) respectively. No changes to TinyGo's loop-analysis passes are required.

**Tech Stack:** Go, x-tools-spmd go/ssa IR, TinyGo LLVM codegen, WASM SIMD128

---

## Background: the exact SSA shape being targeted

For code in `parseIPv4Inner`:
```go
entry := lemireTable[hash]        // entry: lemireEntry (value type)
shuffleMask := entry.shuffleMask  // local copy of [16]byte field
shuffleMask[12] = byte(sf3)       // const-index element overrides
shuffleMask[13] = byte(sf3 + 1)
shuffleMask[14] = byte(sf3 + 2)
go for i, m := range shuffleMask { shuffled[i] = digits[m] }
```

go/ssa emits (roughly):
```
t1  = IndexAddr(&lemireTable, hash)   // *lemireEntry
t2  = *t1                             // lemireEntry (UnOp{MUL, t1})
t3  = Field(t2, 5)                    // [16]byte  (Field instruction, field index 5)
t4  = Alloc *[16]byte                 // local alloc for shuffleMask
     Store(t4, t3)                    // init store — t3 comes from Field
t5  = IndexAddr(t4, 12); Store(t5, …) // override stores (constant indices)
t6  = IndexAddr(t4, 13); Store(t6, …)
t7  = IndexAddr(t4, 14); Store(t7, …)
... (possible switch CFG with multiple case blocks each having their own stores)
t8  = *t4                             // whole-array load (UnOp{MUL, t4})
t9  = Index(t8, IterPhi)              // range read  ← produces Varying[byte]
```

After promotion:
```
fa  = FieldAddr(t1, 5)                // *[16]byte  — emitted by promotion pass
v0  = SPMDVectorFromPtr(fa, 16)       // Varying[byte]  ← v128.load
v1  = SPMDVectorInsert(v0, 12, …)    // replace byte 12
v2  = SPMDVectorInsert(v1, 13, …)    // replace byte 13
v3  = SPMDVectorInsert(v2, 14, …)    // replace byte 14
(phi at merge block if overrides span multiple CFG paths)
t9  = Index(v_final, IterPhi)         // unchanged — now reads the vector
```

---

## File Map

| File | Change |
|------|--------|
| `x-tools-spmd/go/ssa/ssa.go` | Add `SPMDVectorFromPtr` and `SPMDVectorInsert` struct + methods |
| `x-tools-spmd/go/ssa/emit.go` | Add `emitSPMDVectorFromPtr`, `emitSPMDVectorInsert` helpers |
| `x-tools-spmd/go/ssa/print.go` | Add `String()` methods for both new instructions |
| `x-tools-spmd/go/ssa/sanity.go` | Add nil-field validation for both new instructions |
| `x-tools-spmd/go/ssa/spmd_promote.go` | Add `fieldArrayInfo`, `checkFieldArrayPromotion`, `doPromoteFieldArray` |
| `x-tools-spmd/go/ssa/spmd_promote_test.go` | Add tests for both new instructions and the new pattern |
| `tinygo/compiler/spmd.go` | Add `createSPMDVectorFromPtr`, `createSPMDVectorInsert` |
| `tinygo/compiler/compiler.go` | Add dispatch cases for both new instructions |

---

## Chunk 1: New SSA Instructions

### Task 1: Add `SPMDVectorFromPtr` SSA instruction

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (after the `SPMDVectorFromMemory` struct ~line 1538)
- Modify: `x-tools-spmd/go/ssa/emit.go` (after `emitSPMDVectorFromMemory` ~line 731)
- Modify: `x-tools-spmd/go/ssa/print.go` (after the `SPMDVectorFromMemory` case in String() ~line 445)
- Modify: `x-tools-spmd/go/ssa/sanity.go` (after the `*SPMDVectorFromMemory` case ~line 281)
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go`

- [ ] **Step 1: Write failing tests for `SPMDVectorFromPtr`**

Add to `x-tools-spmd/go/ssa/spmd_promote_test.go` after the existing `SPMDVectorFromMemory` tests:

```go
// SPMDVectorFromPtr tests

var _ ssa.Value = (*ssa.SPMDVectorFromPtr)(nil)
var _ ssa.Instruction = (*ssa.SPMDVectorFromPtr)(nil)

func TestSPMDVectorFromPtr_Type(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorFromPtr{ElemType: byteType, Lanes: 16}
    got := instr.Type()
    want := types.NewVarying(byteType)
    if !types.Identical(got, want) {
        t.Errorf("Type() = %v, want %v", got, want)
    }
}

func TestSPMDVectorFromPtr_Operands(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorFromPtr{ElemType: byteType, Lanes: 16}
    var rands []*ssa.Value
    got := instr.Operands(rands)
    if len(got) != 1 {
        t.Fatalf("Operands() len = %d, want 1", len(got))
    }
    if got[0] != &instr.Ptr {
        t.Errorf("Operands()[0] not &Ptr")
    }
}

func TestSPMDVectorFromPtr_String(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorFromPtr{ElemType: byteType, Lanes: 16}
    got := instr.String()
    if !strings.Contains(got, "spmd_vector_from_ptr") {
        t.Errorf("String() = %q, want spmd_vector_from_ptr prefix", got)
    }
    if !strings.Contains(got, "16") {
        t.Errorf("String() = %q, missing lane count", got)
    }
}
```

- [ ] **Step 2: Run tests — expect compile errors (types not yet defined)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestSPMDVectorFromPtr 2>&1 | head -20
```

Expected: `undefined: ssa.SPMDVectorFromPtr`

- [ ] **Step 3: Add `SPMDVectorFromPtr` struct and methods to `ssa.go`**

In `x-tools-spmd/go/ssa/ssa.go`, add after the `SPMDVectorFromMemory` struct (after line 1538):

```go
// SPMDVectorFromPtr loads a fixed-size array from a raw pointer directly into
// a SIMD vector. Unlike SPMDVectorFromMemory, the source has exactly Lanes
// valid elements, so no zero-padding is needed. Lowers to a single v128.load.
//
// This arises when a [N]T struct field is copied to a local variable and then
// iterated with go for: the copy alloc is eliminated and the field pointer is
// used directly.
type SPMDVectorFromPtr struct {
    register
    Ptr      Value      // *[N]T — pointer to the source array (e.g., from FieldAddr)
    ElemType types.Type // element type (byte, int32, etc.)
    Lanes    int        // lane count (= N)
}

func (v *SPMDVectorFromPtr) Operands(rands []*Value) []*Value {
    return append(rands, &v.Ptr)
}

func (v *SPMDVectorFromPtr) Pos() token.Pos { return v.pos }

func (v *SPMDVectorFromPtr) Type() types.Type {
    return types.NewVarying(v.ElemType)
}
```

Add the `String()` method to `print.go` after the `SPMDVectorFromMemory` case:

```go
func (v *SPMDVectorFromPtr) String() string {
    from := spmdRelPkg(v)
    return fmt.Sprintf("spmd_vector_from_ptr<%d, %s> %s",
        v.Lanes, relType(v.ElemType, from), spmdRelName(v.Ptr, v))
}
```

Add emit helper to `emit.go` after `emitSPMDVectorFromMemory`:

```go
func emitSPMDVectorFromPtr(f *Function, ptr Value, lanes int, elemType types.Type, pos token.Pos) *SPMDVectorFromPtr {
    v := &SPMDVectorFromPtr{
        Ptr:      ptr,
        ElemType: elemType,
        Lanes:    lanes,
    }
    v.pos = pos
    f.emit(v)
    return v
}
```

Add sanity checks to `sanity.go` after the `*SPMDVectorFromMemory` case:

```go
case *SPMDVectorFromPtr:
    if instr.Lanes <= 0 {
        s.errorf("SPMDVectorFromPtr: Lanes must be positive, got %d", instr.Lanes)
    }
    if instr.ElemType == nil {
        s.errorf("SPMDVectorFromPtr: ElemType is nil")
    }
    if instr.Ptr == nil {
        s.errorf("SPMDVectorFromPtr: Ptr is nil")
    }
```

Also add to the interpreter no-op list in `interp/ops.go` (find the line with `*ssa.SPMDVectorFromMemory`):
```go
case *ssa.SPMDVectorFromPtr, *ssa.SPMDVectorInsert:
    // SPMD instructions not interpreted — handled by TinyGo backend.
    panic("SPMDVectorFromPtr/Insert encountered in interpreter")
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestSPMDVectorFromPtr -v
```

Expected: PASS for all 3 TestSPMDVectorFromPtr tests.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/emit.go go/ssa/print.go go/ssa/sanity.go go/ssa/spmd_promote_test.go go/ssa/interp/ops.go
git commit -m "ssa: add SPMDVectorFromPtr instruction for direct field array loads"
```

---

### Task 2: Add `SPMDVectorInsert` SSA instruction

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (after `SPMDVectorFromPtr`)
- Modify: `x-tools-spmd/go/ssa/emit.go`
- Modify: `x-tools-spmd/go/ssa/print.go`
- Modify: `x-tools-spmd/go/ssa/sanity.go`
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go`

- [ ] **Step 1: Write failing tests**

Add to `spmd_promote_test.go`:

```go
// SPMDVectorInsert tests

var _ ssa.Value = (*ssa.SPMDVectorInsert)(nil)
var _ ssa.Instruction = (*ssa.SPMDVectorInsert)(nil)

func TestSPMDVectorInsert_Type(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorInsert{ElemType: byteType, Idx: 12}
    got := instr.Type()
    want := types.NewVarying(byteType)
    if !types.Identical(got, want) {
        t.Errorf("Type() = %v, want %v", got, want)
    }
}

func TestSPMDVectorInsert_Operands(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorInsert{ElemType: byteType, Idx: 12}
    var rands []*ssa.Value
    got := instr.Operands(rands)
    if len(got) != 2 {
        t.Fatalf("Operands() len = %d, want 2", len(got))
    }
    if got[0] != &instr.Vec {
        t.Errorf("Operands()[0] not &Vec")
    }
    if got[1] != &instr.Val {
        t.Errorf("Operands()[1] not &Val")
    }
}

func TestSPMDVectorInsert_String(t *testing.T) {
    byteType := types.Typ[types.Byte]
    instr := &ssa.SPMDVectorInsert{ElemType: byteType, Idx: 12}
    got := instr.String()
    if !strings.Contains(got, "spmd_vector_insert") {
        t.Errorf("String() = %q, missing spmd_vector_insert", got)
    }
    if !strings.Contains(got, "12") {
        t.Errorf("String() = %q, missing lane index 12", got)
    }
}
```

- [ ] **Step 2: Run tests — expect compile errors**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestSPMDVectorInsert 2>&1 | head -10
```

Expected: `undefined: ssa.SPMDVectorInsert`

- [ ] **Step 3: Implement `SPMDVectorInsert`**

In `ssa.go`, add after `SPMDVectorFromPtr`:

```go
// SPMDVectorInsert inserts a scalar value at a constant lane index into an
// existing SIMD vector, returning a new vector. Lowers to i8x16.replace_lane
// (or equivalent for other element widths). Only constant lane indices are
// supported — use this only when the index is known at compile time.
type SPMDVectorInsert struct {
    register
    Vec      Value      // input Varying[T] vector
    Val      Value      // scalar T value to insert
    Idx      int        // constant lane index (0 ≤ Idx < Lanes)
    ElemType types.Type // element type T
}

func (v *SPMDVectorInsert) Operands(rands []*Value) []*Value {
    return append(rands, &v.Vec, &v.Val)
}

func (v *SPMDVectorInsert) Pos() token.Pos { return v.pos }

func (v *SPMDVectorInsert) Type() types.Type {
    return types.NewVarying(v.ElemType)
}
```

In `print.go`:
```go
func (v *SPMDVectorInsert) String() string {
    from := spmdRelPkg(v)
    return fmt.Sprintf("spmd_vector_insert[%d, %s] %s %s",
        v.Idx, relType(v.ElemType, from), spmdRelName(v.Vec, v), spmdRelName(v.Val, v))
}
```

In `emit.go`:
```go
func emitSPMDVectorInsert(f *Function, vec, val Value, idx int, elemType types.Type, pos token.Pos) *SPMDVectorInsert {
    v := &SPMDVectorInsert{
        Vec:      vec,
        Val:      val,
        Idx:      idx,
        ElemType: elemType,
    }
    v.pos = pos
    f.emit(v)
    return v
}
```

In `sanity.go`:
```go
case *SPMDVectorInsert:
    if instr.ElemType == nil {
        s.errorf("SPMDVectorInsert: ElemType is nil")
    }
    if instr.Vec == nil {
        s.errorf("SPMDVectorInsert: Vec is nil")
    }
    if instr.Val == nil {
        s.errorf("SPMDVectorInsert: Val is nil")
    }
    if instr.Idx < 0 {
        s.errorf("SPMDVectorInsert: Idx must be non-negative, got %d", instr.Idx)
    }
```

- [ ] **Step 4: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run "TestSPMDVector" -v
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Run full x-tools-spmd test suite**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/... 2>&1 | tail -5
```

Expected: `ok  golang.org/x/tools/go/ssa`

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/emit.go go/ssa/print.go go/ssa/sanity.go go/ssa/spmd_promote_test.go
git commit -m "ssa: add SPMDVectorInsert instruction for scalar lane override"
```

---

## Chunk 2: Promotion Pass

### Task 3: Detection — `checkFieldArrayPromotion`

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_promote.go`
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go`

The new pattern has these referrers of `alloc *[N]T`:
1. Exactly ONE `*Store` where `store.Addr == alloc` and `store.Val` is a `*Field` instruction (full-array init from struct field).
2. Zero or more `*IndexAddr` with a `*Const` index, each used only by `*Store` (element overrides). These are NOT inside the SPMD loop body.
3. Exactly ONE `*UnOp{MUL}` (whole-array load, rangeindex pattern).
4. `*DebugRef` ignored.

The `Field` instruction (`store.Val.(*Field)`) gives us:
- `field.X` — struct VALUE (a `*UnOp{MUL}` loading the struct from a pointer)
- `field.Field` — int field index

From `field.X.(*UnOp).X` we get the struct pointer (e.g., `*lemireEntry`). We need this to emit `FieldAddr` for `SPMDVectorFromPtr`.

- [ ] **Step 1: Write failing detection test**

Add to `spmd_promote_test.go`:

```go
func TestPromoteFieldArray_SimpleNoOverrides(t *testing.T) {
    // [N]byte struct field, no element overrides, rangeindex reads.
    src := `
package main

import "lanes"

type Entry struct {
    mask uint16
    data [16]byte
}

var table [100]Entry

func process(idx int) {
    entry := table[idx]
    buf := entry.data  // [16]byte field copy
    _ = lanes.Count[byte](buf[0])
    go for i, v := range buf {
        _ = v
        _ = i
    }
}
`
    pkg := buildSPMDProgram(t, src, 16)
    fn := pkg.Func("process")
    if fn == nil {
        t.Fatal("function 'process' not found")
    }

    // After promotion, no Alloc of *[16]byte should remain.
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if pt, ok := alloc.Type().Underlying().(*types.Pointer); ok {
                    if at, ok := pt.Elem().Underlying().(*types.Array); ok {
                        if at.Len() == 16 {
                            t.Errorf("found unexpected [16]byte Alloc — should have been promoted")
                        }
                    }
                }
            }
        }
    }

    // A SPMDVectorFromPtr instruction must be present.
    var found bool
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            if _, ok := instr.(*ssa.SPMDVectorFromPtr); ok {
                found = true
            }
        }
    }
    if !found {
        t.Error("SPMDVectorFromPtr not found after promotion")
    }
}
```

- [ ] **Step 2: Run — expect FAIL (alloc not removed, SPMDVectorFromPtr not emitted)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestPromoteFieldArray_SimpleNoOverrides -v 2>&1
```

Expected: FAIL.

- [ ] **Step 3: Implement `checkFieldArrayPromotion`**

Add to `spmd_promote.go` after `checkCopyPattern` (~line 446):

```go
// fieldArrayInfo describes the "struct field init + const-index overrides + rangeindex" pattern.
type fieldArrayInfo struct {
    initStore   *Store        // Store(alloc, Field(structVal, fieldIdx)) — full array init
    fieldInstr  *Field        // the Field instruction providing the initial [N]T value
    overrides   []fieldOverride // const-index element stores (may span multiple blocks)
    arrayLoad   *UnOp         // UnOp{MUL, alloc} — whole-array load feeding range
    indexInstrs []*Index      // Index(arrayLoad, IterPhi) in loop body
}

// fieldOverride is one element override: IndexAddr(alloc, constIdx) + Store.
type fieldOverride struct {
    indexAddr *IndexAddr
    store     *Store
    idx       int // constant lane index
}

// checkFieldArrayPromotion checks whether alloc follows the struct-field init
// pattern and returns the loop and pattern info, or nil if ineligible.
//
// Eligible pattern:
//   t3  = Field(structVal, f)           // field extraction from loaded struct
//   t4  = Alloc *[N]T                   // alloc (the candidate)
//        Store(t4, t3)                  // full init from field value
//   t5  = IndexAddr(t4, constIdx)       // element override (may be in switch branches)
//        Store(t5, newVal)
//        ...
//   t8  = *t4                           // whole-array load (rangeindex)
//   t9  = Index(t8, IterPhi)            // range reads (inside go for body)
func checkFieldArrayPromotion(alloc *Alloc, blockToLoop map[*BasicBlock]*SPMDLoopInfo) (*SPMDLoopInfo, *fieldArrayInfo) {
    // Check 1: type is *[N]T for a promotable element type.
    ptrType, ok := alloc.Type().Underlying().(*types.Pointer)
    if !ok {
        return nil, nil
    }
    arrayType, ok := ptrType.Elem().Underlying().(*types.Array)
    if !ok {
        return nil, nil
    }
    elemType := arrayType.Elem()
    elemSize := spmdPromoteElemSize(elemType)
    if elemSize == 0 {
        return nil, nil
    }
    arrayLen := int(arrayType.Len())
    if arrayLen*elemSize > 16 {
        return nil, nil
    }

    // Check 2: categorize all referrers of alloc.
    refs := alloc.Referrers()
    if refs == nil || len(*refs) == 0 {
        return nil, nil
    }

    var initStore *Store       // exactly one
    var arrayLoad *UnOp        // exactly one
    var overrides []fieldOverride

    for _, ref := range *refs {
        switch r := ref.(type) {
        case *Store:
            // Must be Store(alloc, t3) where t3 is a Field instruction.
            if r.Addr != alloc {
                return nil, nil // alloc on wrong side — ineligible
            }
            if _, ok := r.Val.(*Field); !ok {
                return nil, nil // init value not a Field — ineligible
            }
            if initStore != nil {
                return nil, nil // multiple init stores — too complex
            }
            initStore = r
        case *IndexAddr:
            // Element override: must have constant index.
            constIdx, ok := r.Index.(*Const)
            if !ok {
                return nil, nil // non-constant override index — ineligible
            }
            idxInt, ok := constant.Int64Val(constIdx.Value)
            if !ok || idxInt < 0 || int(idxInt) >= arrayLen {
                return nil, nil // out-of-range constant index
            }
            // IndexAddr must be used only by Stores (no reads of these elements).
            iaRefs := r.Referrers()
            if iaRefs == nil || len(*iaRefs) == 0 {
                return nil, nil
            }
            var storeRef *Store
            for _, iaRef := range *iaRefs {
                switch s := iaRef.(type) {
                case *Store:
                    if s.Addr != r {
                        return nil, nil
                    }
                    if storeRef != nil {
                        return nil, nil // multiple stores through same IndexAddr
                    }
                    storeRef = s
                case *DebugRef:
                    // ok
                default:
                    return nil, nil // reads of override element — ineligible
                }
            }
            if storeRef == nil {
                return nil, nil
            }
            overrides = append(overrides, fieldOverride{
                indexAddr: r,
                store:     storeRef,
                idx:       int(idxInt),
            })
        case *UnOp:
            if r.Op != token.MUL {
                return nil, nil
            }
            if arrayLoad != nil {
                return nil, nil // multiple whole-array loads — too complex
            }
            arrayLoad = r
        case *DebugRef:
            // ok
        default:
            return nil, nil // escape
        }
    }

    // Check 3: must have an init store from a Field instruction.
    if initStore == nil {
        return nil, nil
    }
    fieldInstr := initStore.Val.(*Field)

    // Check 4: must have a whole-array load for the rangeindex pattern.
    if arrayLoad == nil {
        return nil, nil
    }

    // Check 5: validate Index instructions on the whole-array load.
    var loop *SPMDLoopInfo
    var indexInstrs []*Index

    alRefs := arrayLoad.Referrers()
    if alRefs == nil || len(*alRefs) == 0 {
        return nil, nil
    }
    for _, alRef := range *alRefs {
        switch r := alRef.(type) {
        case *Index:
            l, ok := blockToLoop[r.Block()]
            if !ok {
                return nil, nil
            }
            if loop == nil {
                loop = l
            } else if loop != l {
                return nil, nil
            }
            // Index must be keyed by IterPhi or IncrBinOp.
            unwrapped := unwrapChangeType(r.Index)
            if unwrapped != loop.IterPhi && (loop.IncrBinOp == nil || unwrapped != Value(loop.IncrBinOp)) {
                return nil, nil
            }
            indexInstrs = append(indexInstrs, r)
        case *DebugRef:
            // ok
        default:
            return nil, nil
        }
    }

    if loop == nil || len(indexInstrs) == 0 {
        return nil, nil
    }

    // Check 6: loop iterates exactly arrayLen times (rangeindex: LaneCount == arrayLen).
    if loop.LaneCount != arrayLen {
        return nil, nil
    }

    return loop, &fieldArrayInfo{
        initStore:   initStore,
        fieldInstr:  fieldInstr,
        overrides:   overrides,
        arrayLoad:   arrayLoad,
        indexInstrs: indexInstrs,
    }
}
```

Wire it into `promoteSPMDArrays` in `spmd_promote.go` by adding a call alongside `checkArrayPromotion`:

```go
// In promoteSPMDArrays, after checkArrayPromotion:
if loop, fi := checkFieldArrayPromotion(alloc, blockToLoop); loop != nil {
    doPromoteFieldArray(f, alloc, loop, fi)
    continue
}
```

For now, add a stub `doPromoteFieldArray` that does nothing:

```go
func doPromoteFieldArray(f *Function, alloc *Alloc, loop *SPMDLoopInfo, fi *fieldArrayInfo) {
    // TODO: implement in Task 4
}
```

- [ ] **Step 4: Run detection test — still FAIL (doPromoteFieldArray is a stub)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestPromoteFieldArray_SimpleNoOverrides -v 2>&1
```

Expected: FAIL (alloc not removed). The detection path compiles. Good: no panics.

- [ ] **Step 5: Run full suite — no regressions**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/... 2>&1 | tail -5
```

Expected: `ok`.

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_promote.go go/ssa/spmd_promote_test.go
git commit -m "ssa: add checkFieldArrayPromotion to detect struct-field-init pattern"
```

---

### Task 4: Emission — `doPromoteFieldArray`

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_promote.go`
- Test: `x-tools-spmd/go/ssa/spmd_promote_test.go`

**Algorithm for `doPromoteFieldArray`:**

The overrides may span multiple CFG blocks (e.g., the three branches of a `switch l3` block). We handle this by:

1. Emit `FieldAddr(structPtr, fieldIdx)` immediately before the init store in the init block.
2. Emit `v0 = SPMDVectorFromPtr(fieldAddr, lanes, elemType)` right after the FieldAddr.
3. Walk the CFG forward from the init block to the block containing the whole-array load, collecting element overrides per block. Use a map `blockVec map[*BasicBlock]Value` = the vector value leaving that block.
4. For each block in dominator-tree order (use block Index as proxy):
   - Compute incoming vector: if single predecessor, use `blockVec[pred]`; if multiple predecessors, insert a new Phi.
   - For each override in this block (in instruction order), emit `SPMDVectorInsert`.
   - Set `blockVec[block] = lastVec`.
5. The block containing `arrayLoad` gets a final incoming vector. Replace every `Index(arrayLoad, i)` instruction: replace `arrayLoad` operand with this final vector (the Index instruction then reads from the promoted vector, which TinyGo will handle via `extract_element`).
6. Remove all element-override `IndexAddr + Store` pairs, the `initStore`, the `arrayLoad`, and the `alloc`.

**Key helper** — `spmdInsertPhiAt(b *BasicBlock, preds []*BasicBlock, vals []Value, typ types.Type) *Phi`:

```go
func spmdInsertPhiAt(b *BasicBlock, preds []*BasicBlock, vals []Value, typ types.Type) *Phi {
    phi := &Phi{}
    phi.setType(typ)
    phi.setBlock(b)
    phi.Edges = make([]Value, len(preds))
    copy(phi.Edges, vals)
    // Prepend phi to block instructions.
    b.Instrs = append([]Instruction{phi}, b.Instrs...)
    return phi
}
```

**Note on `FieldAddr` type:** `FieldAddr(structPtr, fieldIdx)` has type `*[N]T`. Construct it as:

```go
fieldPtrType := types.NewPointer(fi.fieldInstr.Type())
fa := &FieldAddr{
    X:     structPtr,
    Field: fi.fieldInstr.Field,
}
fa.setType(fieldPtrType)
fa.setBlock(initBlock)
spmdInsertBeforeInstr(initBlock, fi.initStore, fa)
```

Where `structPtr` = `fi.fieldInstr.X.(*UnOp).X` (the pointer to the struct loaded by the init store's value).

- [ ] **Step 1: Add test with element overrides**

```go
func TestPromoteFieldArray_WithOverrides(t *testing.T) {
    src := `
package main

import "lanes"

type Entry struct {
    mask uint16
    data [16]byte
}

var table [100]Entry

func process(idx int, v0 byte, v1 byte) {
    entry := table[idx]
    buf := entry.data
    buf[0] = v0   // element override at const index 0
    buf[1] = v1   // element override at const index 1
    _ = lanes.Count[byte](buf[0])
    go for i, v := range buf {
        _ = v
        _ = i
    }
}
`
    pkg := buildSPMDProgram(t, src, 16)
    fn := pkg.Func("process")
    if fn == nil {
        t.Fatal("function 'process' not found")
    }

    // No [16]byte Alloc should remain.
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if pt, ok := alloc.Type().Underlying().(*types.Pointer); ok {
                    if at, ok := pt.Elem().Underlying().(*types.Array); ok {
                        if at.Len() == 16 {
                            t.Errorf("found unexpected [16]byte Alloc after promotion")
                        }
                    }
                }
            }
        }
    }

    // Count SPMDVectorFromPtr and SPMDVectorInsert instructions.
    var nFromPtr, nInsert int
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            switch instr.(type) {
            case *ssa.SPMDVectorFromPtr:
                nFromPtr++
            case *ssa.SPMDVectorInsert:
                nInsert++
            }
        }
    }
    if nFromPtr != 1 {
        t.Errorf("SPMDVectorFromPtr count = %d, want 1", nFromPtr)
    }
    if nInsert != 2 {
        t.Errorf("SPMDVectorInsert count = %d, want 2 (one per override)", nInsert)
    }
}
```

- [ ] **Step 2: Run tests — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run "TestPromoteFieldArray" -v 2>&1 | tail -15
```

Expected: both tests FAIL (stub does nothing).

- [ ] **Step 3: Implement `doPromoteFieldArray`**

Replace the stub in `spmd_promote.go`:

```go
// doPromoteFieldArray promotes an Alloc that follows the struct-field init +
// optional const-index overrides + rangeindex read pattern.
//
// Transformation:
//  1. Emit FieldAddr + SPMDVectorFromPtr in the init block.
//  2. Forward-propagate the vector through the CFG, emitting SPMDVectorInsert
//     for each element override; insert Phi nodes at merge blocks.
//  3. Replace Index(arrayLoad, i) reads with the promoted vector.
//  4. Remove initStore, override IndexAddr+Stores, arrayLoad, alloc.
func doPromoteFieldArray(f *Function, alloc *Alloc, loop *SPMDLoopInfo, fi *fieldArrayInfo) {
    // Step 1: locate init block and struct pointer.
    initBlock := fi.initStore.Block()

    // fieldInstr.X is the struct VALUE; its parent is UnOp{MUL, structPtr}.
    structLoad, ok := fi.fieldInstr.X.(*UnOp)
    if !ok || structLoad.Op != token.MUL {
        return // unexpected shape — bail out
    }
    structPtr := structLoad.X // *StructType pointer (e.g., *lemireEntry)

    // Step 1a: emit FieldAddr(structPtr, fieldIdx) before initStore.
    fieldPtrType := types.NewPointer(fi.fieldInstr.Type())
    fa := &FieldAddr{
        X:     structPtr,
        Field: fi.fieldInstr.Field,
    }
    fa.setType(fieldPtrType)
    fa.setBlock(initBlock)
    spmdInsertBeforeInstr(initBlock, fi.initStore, fa)

    // Step 1b: emit SPMDVectorFromPtr right after FieldAddr (= before initStore).
    // Use spmdInsertBeforeInstr again since fa was just inserted before initStore.
    elemType := fi.fieldInstr.Type().Underlying().(*types.Array).Elem()
    fromPtr := &SPMDVectorFromPtr{
        Ptr:      fa,
        ElemType: elemType,
        Lanes:    loop.LaneCount,
    }
    fromPtr.pos = fi.initStore.Pos()
    fromPtr.setType(types.NewVarying(elemType))
    fromPtr.setBlock(initBlock)
    spmdInsertBeforeInstr(initBlock, fi.initStore, fromPtr)

    // Step 2: build a map from block → set of overrides in that block (ordered).
    blockOverrides := make(map[*BasicBlock][]fieldOverride)
    for _, ov := range fi.overrides {
        b := ov.store.Block()
        blockOverrides[b] = append(blockOverrides[b], ov)
    }

    // Step 3: forward-propagate the vector through the CFG.
    // blockVecOut[b] = the Varying[byte] value leaving block b.
    blockVecOut := make(map[*BasicBlock]Value)
    blockVecOut[initBlock] = fromPtr

    // Collect all blocks between initBlock and the arrayLoad block (inclusive)
    // by BFS following successors, stopping when we leave the relevant region.
    // We process in block.Index order (ascending) to ensure producers come before
    // consumers (go/ssa numbers blocks in dominator-tree preorder).
    arrayLoadBlock := fi.arrayLoad.Block()
    visited := make(map[*BasicBlock]bool)
    queue := []*BasicBlock{initBlock}
    visited[initBlock] = true
    var ordered []*BasicBlock
    for len(queue) > 0 {
        cur := queue[0]
        queue = queue[1:]
        ordered = append(ordered, cur)
        if cur == arrayLoadBlock {
            continue
        }
        for _, s := range cur.Succs {
            if !visited[s] {
                visited[s] = true
                queue = append(queue, s)
            }
        }
    }
    // Sort by block index so we process producers before consumers.
    sort.Slice(ordered, func(a, b int) bool {
        return ordered[a].Index < ordered[b].Index
    })

    for _, b := range ordered {
        if b == initBlock {
            // Already have blockVecOut[initBlock] = fromPtr; apply any overrides.
            cur := Value(fromPtr)
            for _, ov := range blockOverrides[b] {
                ins := emitSPMDVectorInsertAfter(f, b, fi.initStore, cur, ov.store.Val, ov.idx, elemType, ov.store.Pos())
                cur = ins
            }
            blockVecOut[b] = cur
            continue
        }

        // Compute incoming vector.
        var incoming Value
        switch len(b.Preds) {
        case 0:
            continue // unreachable
        case 1:
            v, ok := blockVecOut[b.Preds[0]]
            if !ok {
                continue // predecessor not yet computed
            }
            incoming = v
        default:
            // Multiple predecessors: gather values and insert Phi if they differ.
            var edges []Value
            allSame := true
            var first Value
            for _, pred := range b.Preds {
                v := blockVecOut[pred]
                if v == nil {
                    v = fromPtr // default to base vector for unvisited preds
                }
                edges = append(edges, v)
                if first == nil {
                    first = v
                } else if v != first {
                    allSame = false
                }
            }
            if allSame {
                incoming = first
            } else {
                phi := &Phi{}
                phi.Edges = edges
                phi.setType(types.NewVarying(elemType))
                phi.setBlock(b)
                // Prepend phi at start of block (before any existing instructions).
                b.Instrs = append([]Instruction{phi}, b.Instrs...)
                incoming = phi
            }
        }

        // Apply overrides in this block.
        cur := incoming
        if ovs, ok := blockOverrides[b]; ok {
            for _, ov := range ovs {
                ins := emitSPMDVectorInsertAfterInstruction(f, b, ov.store, cur, ov.store.Val, ov.idx, elemType, ov.store.Pos())
                cur = ins
            }
        }
        blockVecOut[b] = cur
    }

    // Step 4: replace Index(arrayLoad, i) with the final vector.
    // The final vector is blockVecOut[arrayLoadBlock].
    finalVec := blockVecOut[arrayLoadBlock]
    if finalVec == nil {
        finalVec = fromPtr // fallback if no overrides exist
    }
    for _, idx := range fi.indexInstrs {
        // Replace idx.X (which was arrayLoad) with finalVec.
        // Index(vec, laneI) will be handled by TinyGo as extract_element(vec, laneI).
        idx.X = finalVec
    }

    // Step 5: remove override stores, override IndexAddrs, initStore, arrayLoad, alloc.
    affectedBlocks := make(map[*BasicBlock]bool)
    for _, ov := range fi.overrides {
        affectedBlocks[ov.store.Block()] = true
        affectedBlocks[ov.indexAddr.Block()] = true
        spmdNilInstr(ov.store.Block(), ov.store)
        spmdNilInstr(ov.indexAddr.Block(), ov.indexAddr)
    }
    affectedBlocks[fi.initStore.Block()] = true
    spmdNilInstr(fi.initStore.Block(), fi.initStore)
    affectedBlocks[fi.arrayLoad.Block()] = true
    spmdNilInstr(fi.arrayLoad.Block(), fi.arrayLoad)
    affectedBlocks[alloc.Block()] = true
    spmdNilInstr(alloc.Block(), alloc)

    for b := range affectedBlocks {
        spmdCompactInstrs(b)
    }
}

// emitSPMDVectorInsertAfter emits an SPMDVectorInsert instruction immediately
// after 'after' in block 'b'.
func emitSPMDVectorInsertAfter(f *Function, b *BasicBlock, after Instruction, vec, val Value, idx int, elemType types.Type, pos token.Pos) *SPMDVectorInsert {
    ins := &SPMDVectorInsert{Vec: vec, Val: val, Idx: idx, ElemType: elemType}
    ins.pos = pos
    ins.setType(types.NewVarying(elemType))
    ins.setBlock(b)
    // Find 'after' in block and insert immediately following it.
    for i, instr := range b.Instrs {
        if instr == after {
            newInstrs := make([]Instruction, 0, len(b.Instrs)+1)
            newInstrs = append(newInstrs, b.Instrs[:i+1]...)
            newInstrs = append(newInstrs, ins)
            newInstrs = append(newInstrs, b.Instrs[i+1:]...)
            b.Instrs = newInstrs
            return ins
        }
    }
    panic("emitSPMDVectorInsertAfter: target instruction not found in block")
}

// emitSPMDVectorInsertAfterInstruction inserts after a given instruction (for override blocks).
func emitSPMDVectorInsertAfterInstruction(f *Function, b *BasicBlock, after Instruction, vec, val Value, idx int, elemType types.Type, pos token.Pos) *SPMDVectorInsert {
    return emitSPMDVectorInsertAfter(f, b, after, vec, val, idx, elemType, pos)
}
```

Add `"sort"` to the import list in `spmd_promote.go` (or use an existing import if present).

- [ ] **Step 4: Run failing tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run "TestPromoteFieldArray" -v 2>&1
```

Expected: both tests PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/... 2>&1 | tail -5
```

Expected: `ok` — no regressions.

- [ ] **Step 6: Rebuild TinyGo to check compilation only (not exec yet)**

```bash
cd /home/cedric/work/SPMD && make build-tinygo GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```

TinyGo will compile but the new instructions will panic at runtime (not yet lowered). That's expected.

- [ ] **Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_promote.go go/ssa/spmd_promote_test.go
git commit -m "ssa: implement doPromoteFieldArray with SPMDVectorFromPtr + SPMDVectorInsert emission"
```

---

## Chunk 3: TinyGo Lowering

### Task 5: Lower `SPMDVectorFromPtr` in TinyGo

**Files:**
- Modify: `tinygo/compiler/spmd.go`
- Modify: `tinygo/compiler/compiler.go`

`SPMDVectorFromPtr.Ptr` is a `*[N]T` pointer (from `FieldAddr`). TinyGo represents it as an opaque LLVM pointer. We load a vector directly from it with `CreateLoad(vecType, ptr, align=1)`.

- [ ] **Step 1: Add `createSPMDVectorFromPtr` to `spmd.go`**

Add after `createSPMDVectorFromMemory`:

```go
// createSPMDVectorFromPtr lowers SPMDVectorFromPtr to a single v128.load.
// Unlike createSPMDVectorFromMemory, no stack buffer or memcpy is needed:
// the source pointer already points to exactly Lanes valid elements.
func (b *builder) createSPMDVectorFromPtr(expr *ssa.SPMDVectorFromPtr) llvm.Value {
    ptr := b.getValue(expr.Ptr, getPos(expr))
    elemLLVM := b.getLLVMType(expr.ElemType)
    vecType := llvm.VectorType(elemLLVM, expr.Lanes)
    // Load the vector directly from the pointer. WASM v128.load accepts any
    // natural alignment (minimum 1-byte alignment is valid for unaligned loads).
    return b.CreateLoad(vecType, ptr, "spmd.vec_from_ptr")
}
```

- [ ] **Step 2: Dispatch in `compiler.go`**

Find the `case *ssa.SPMDVectorFromMemory:` line (~3495) and add the new case immediately after:

```go
case *ssa.SPMDVectorFromPtr:
    return b.createSPMDVectorFromPtr(expr), nil
```

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```

Expected: successful build.

- [ ] **Step 4: Smoke-test IPv4 parser compile (SPMDVectorInsert will still panic)**

```bash
cd /home/cedric/work/SPMD && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-test.wasm test/integration/spmd/ipv4-parser/main.go 2>&1 | head -10
```

Expected: compile error about unhandled `SPMDVectorInsert` (panic in codegen). Next task fixes this.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/compiler.go
git commit -m "tinygo: lower SPMDVectorFromPtr to v128.load"
```

---

### Task 6: Lower `SPMDVectorInsert` in TinyGo

**Files:**
- Modify: `tinygo/compiler/spmd.go`
- Modify: `tinygo/compiler/compiler.go`

`SPMDVectorInsert` maps to LLVM `insertelement` (→ WASM `i8x16.replace_lane` or equivalent). The `Val` operand is a scalar; it may need narrowing (e.g., `i32` → `i8`) if the element type is narrower than the SSA representation.

- [ ] **Step 1: Add `createSPMDVectorInsert` to `spmd.go`**

```go
// createSPMDVectorInsert lowers SPMDVectorInsert to LLVM insertelement.
// Lowers to i8x16.replace_lane / i32x4.replace_lane etc. in WASM SIMD128.
func (b *builder) createSPMDVectorInsert(expr *ssa.SPMDVectorInsert) llvm.Value {
    vec := b.getValue(expr.Vec, getPos(expr))
    val := b.getValue(expr.Val, getPos(expr))

    // The vector element type in LLVM (may be widened, e.g., i8→i32 for byte vectors).
    elemLLVM := b.getLLVMType(expr.ElemType)
    vecElemType := vec.Type().ElementType()

    // Narrow val to the vector element width if needed (e.g., wasm i32 rep → i8).
    if val.Type() != vecElemType {
        val = b.CreateTrunc(val, vecElemType, "spmd.insert.narrow")
    }

    idxVal := llvm.ConstInt(b.ctx.Int32Type(), uint64(expr.Idx), false)
    return b.CreateInsertElement(vec, val, idxVal, "spmd.vec_insert")
}
```

- [ ] **Step 2: Dispatch in `compiler.go`**

Add after `case *ssa.SPMDVectorFromPtr:`:

```go
case *ssa.SPMDVectorInsert:
    return b.createSPMDVectorInsert(expr), nil
```

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5
```

Expected: successful build.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/compiler.go
git commit -m "tinygo: lower SPMDVectorInsert to LLVM insertelement (WASM replace_lane)"
```

---

## Chunk 4: End-to-End Verification

### Task 7: Verify IPv4 parser correctness and instruction count

**Files:**
- Read: `test/integration/spmd/ipv4-parser/main.go` (no changes expected)

- [ ] **Step 1: Compile and run IPv4 parser**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-promoted.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-promoted.wasm 2>&1 | head -15
```

Expected output includes:
```
'192.168.1.1' -> 192.168.1.1
...
Correctness: SPMD and scalar results match.
```

- [ ] **Step 2: Inspect generated WAT for Phase 4**

```bash
wasm2wat /tmp/ipv4-promoted.wasm | grep -A 200 'parseIPv4Inner' | grep -c 'replace_lane'
```

Goal: ≤ 6 `replace_lane` instructions (3 for l3 patch + at most 3 for the `v128.load` alignment rounding that TinyGo may add).
Previously: ~29 `replace_lane` instructions for the shuffleMask construction.

- [ ] **Step 3: Count total instructions in `parseIPv4Inner`**

```bash
wasm2wat /tmp/ipv4-promoted.wasm | awk '/\$main\.parseIPv4Inner/,/^\s*\)/' | grep -c '^\s\+'
```

Target: ≤ 230 instructions (down from 312 before this optimization).

Phase 4 breakdown target:
- Lemire hash compute: ~17 (unchanged)
- expectedMask check: ~13 (unchanged)
- l0/l1/l2 extraction: ~9 (unchanged)
- `v128.load` from field: ~3 (was 65 — **62 instruction saving**)
- 3× `replace_lane` for l3 patch: ~15 (was ~25 in switch — modest saving too)
- **Phase 4 total target: ~57** (was 137)

- [ ] **Step 4: Run E2E test suite — no regressions**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -10
```

Expected: compile/run numbers at least as good as pre-task (40 compile pass, 32 run pass).

- [ ] **Step 5: Run all TinyGo LLVM tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

Expected: all existing tests pass.

- [ ] **Step 6: Run all x-tools-spmd tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && /home/cedric/work/SPMD/go/bin/go test ./go/ssa/... 2>&1 | tail -5
```

Expected: `ok`.

- [ ] **Step 7: Commit x-tools-spmd**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add -A
git commit -m "ssa: promote struct-field array init+override+range to SPMDVectorFromPtr+Insert"
```

- [ ] **Step 8: Commit TinyGo**

```bash
cd /home/cedric/work/SPMD/tinygo
git add -A
git commit -m "tinygo: lower SPMDVectorFromPtr/Insert — v128.load + replace_lane"
```

---

## Known Edge Cases

| Case | Handling |
|------|---------|
| `structPtr` is not `UnOp{MUL, *}` (e.g., func param) | `checkFieldArrayPromotion` returns nil at step 3a |
| Multiple `Field` init stores | Rejected (one initStore max) |
| Override with non-const index (e.g., `buf[i] = x`) | Rejected at `checkFieldArrayPromotion` (requires `*Const` index) |
| Overrides in unreachable blocks | Skip (block not in ordered CFG traversal) |
| `arrayLen * elemSize < 16` (e.g., `[4]int32`) | Still works — `SPMDVectorFromPtr` carries exact lane count; `v128.load` always loads 16 bytes (safe since struct padding covers the rest) |
| Struct field not 16-byte-aligned in memory | WASM `v128.load` with natural alignment ≥ 1 is always valid; no forced alignment needed |

## Deferred

- **PLAN.md entry required** after this plan is executed: add to "Deferred Items Collection" if any sub-cases are left unimplemented.
- **Generalize to `FieldAddr` init** (struct pointer init, not value init): a different SSA shape (`*FieldAddr` load rather than `*Field` extract); straightforward extension of `checkFieldArrayPromotion` once the value pattern is working.
- **Multi-field promotion** (two separate `[N]T` fields of the same struct): handled independently — each alloc gets its own pass.
