# Design Spec: `&varyingVar` Address-of for Varying Variables

**Date**: 2026-04-12
**Status**: Draft
**Motivation**: Allow `&varyingVar` where `varyingVar` is `Varying[T]`, producing `Varying[*T]` — a per-lane pointer vector. Currently rejected with "cannot take address of varying variable".

## 1. Scope

Three changes across two repos (go fork + tinygo):

1. **Type checker** (go/types + types2): Allow `&Varying[T]`, set result type to `Varying[*T]`
2. **TinyGo**: When lowering a pointer to `Varying[T]` storage, emit per-lane GEPs producing `<N x ptr>` instead of a scalar pointer

No go/ssa changes — go/ssa naturally creates `Alloc` + `*Alloc` for address-taken variables. The `Alloc` holds `Varying[T]` in memory. TinyGo reinterprets `*Varying[T]` as `Varying[*T]` at lowering time.

## 2. Type Checker Change

### Files
- `go/src/go/types/pointer_ext_spmd.go`
- `go/src/cmd/compile/internal/types2/pointer_ext_spmd.go`

### Current behavior (lines 34-46)

```go
func (check *Checker) validateSPMDAddressOperation(x *operand, operandExpr ast.Expr) (handled, valid bool, errorMsg string) {
    if spmdType, ok := x.typ().(*SPMDType); ok && spmdType.qualifier == VaryingQualifier {
        if ident, ok := operandExpr.(*ast.Ident); ok && ident != nil {
            return true, false, "cannot take address of varying variable"
        }
    }
    return false, false, ""
}
```

### New behavior

```go
func (check *Checker) validateSPMDAddressOperation(x *operand, operandExpr ast.Expr) (handled, valid bool, errorMsg string) {
    if spmdType, ok := x.typ().(*SPMDType); ok && spmdType.qualifier == VaryingQualifier {
        if _, ok := operandExpr.(*ast.Ident); ok {
            // Allow: &Varying[T] produces Varying[*T].
            // Each lane gets a pointer to its own element in the spilled storage.
            // The go/ssa Alloc holds Varying[T]; TinyGo emits per-lane GEPs.
            return true, true, ""
        }
    }
    return false, false, ""
}
```

The result type transformation (`Varying[T]` → `Varying[*T]`) must happen at the call site where the operand type is set. In `go/types/expr.go` (or equivalent), after validating the address operation succeeds, set:

```go
// When &Varying[T] is allowed, result type = Varying[*T]
if spmdType, ok := x.typ.(*SPMDType); ok && spmdType.IsVarying() {
    x.typ = NewVarying(types.NewPointer(spmdType.Elem()))
}
```

### Same change in types2

The identical logic exists in `cmd/compile/internal/types2/pointer_ext_spmd.go` (lines 33-45) and `expr.go` (lines 148-160). Apply the same changes.

## 3. TinyGo Change

### File
- `tinygo/compiler/spmd.go` or `compiler.go`

### The lowering

When TinyGo encounters a pointer to `Varying[T]` storage (from `&Alloc` where Alloc holds `Varying[T]`):

**Current**: Produces a scalar `ptr` to the alloca base (type `*<N x T>` or `*[N x T]`)

**New**: Detect that the target type should be `Varying[*T]` (vector of pointers). Emit per-lane GEPs:

```go
// alloca holds <N x T> or [N x T]
// Produce <N x ptr>: each lane gets ptr to its own element
elemType := ... // T
ptrVec := llvm.Undef(llvm.VectorType(ptrType, laneCount))
for lane := 0; lane < laneCount; lane++ {
    gep := b.CreateInBoundsGEP(elemType, allocaPtr, []llvm.Value{
        llvm.ConstInt(i32Type, 0, false),
        llvm.ConstInt(i32Type, uint64(lane), false),
    }, "varyingaddr.gep")
    ptrVec = b.CreateInsertElement(ptrVec, gep, 
        llvm.ConstInt(i32Type, uint64(lane), false), "")
}
```

### Detection

The key: when is `*Varying[T]` used as `Varying[*T]`? 

In go/ssa, `&varyingVar` produces `*Varying[T]` (pointer to the Alloc). But the TYPE CHECKER set the Go-level type to `Varying[*T]`. TinyGo sees the go/ssa type (`*Varying[T]`) and the Go type (`Varying[*T]`). It should check: if the go/ssa instruction produces `*SPMDType` but the Go-level result type is `SPMDType{elem: *T}`, emit per-lane GEPs instead of a scalar pointer.

Alternatively: whenever TinyGo encounters `Alloc` of `Varying[T]` whose address is taken, check if any use of the `*Alloc` expects `Varying[*T]` and emit per-lane pointers.

The simplest approach: in `getValue` or `createExpr`, when processing `*ssa.Alloc` whose type is `*Varying[T]`, and the downstream use expects `Varying[*T]`, bitcast the scalar pointer to element-type pointer and create per-lane GEPs.

## 4. Test

Add test cases to `go/src/go/types/testdata/spmd/pointer_varying.go`:

```go
func testAddressOfVarying() {
    var v lanes.Varying[int]
    ptr := &v  // Should succeed, type = Varying[*int]
    _ = ptr
}
```

Add E2E test to `test/integration/spmd/pointer-varying/main.go`:

```go
go for i := range 4 {
    var local lanes.Varying[int] = lanes.Index()
    ptr := &local
    *ptr = *ptr + 1  // Per-lane modification via pointer
    result[i] = *ptr
}
```

## 5. Expected Impact

Low — this is a niche feature. Most SPMD patterns use `&data[i]` (address of slice element via varying index), which already works. `&varyingVar` enables:
- Passing varying values by pointer to non-SPMD functions
- Building data structures with pointers to per-lane storage
- More natural Go patterns (taking address of any variable)

## 6. Files Modified

| Repository | File | Change |
|-----------|------|--------|
| go | `src/go/types/pointer_ext_spmd.go` | Allow `&Varying[T]`, return valid=true |
| go | `src/go/types/expr.go` (or call site) | Set result type to `Varying[*T]` |
| go | `src/cmd/compile/internal/types2/pointer_ext_spmd.go` | Same as go/types |
| go | `src/cmd/compile/internal/types2/expr.go` | Same type transform |
| go | `src/go/types/testdata/spmd/pointer_varying.go` | Test cases |
| tinygo | `compiler/spmd.go` or `compiler.go` | Per-lane GEP for `*Varying[T]` → `Varying[*T]` |
| main | `test/integration/spmd/pointer-varying/main.go` | E2E test |
