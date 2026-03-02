# SSA Mask Type Design

## Date: 2026-03-01

## Status: IMPLEMENTED

## Problem

The TinyGo SPMD backend currently guesses mask representation from context using
heuristics in `spmd.go`. Masks are represented as `Varying[bool]` or
architecture-specific vector types (`<4 x i32>` on WASM, `<N x i1>` elsewhere),
but there is no first-class SSA type to distinguish "execution mask" from
"vector of booleans". This leads to:

- Heuristic-based mask detection in SSA-to-LLVM lowering
- Architecture-specific mask format leaking into the SSA layer
- No type-level enforcement of mask semantics (e.g., cannot store to memory)

## Solution

Add a new `MaskType` to the x-tools-spmd package as an internal-only,
architecture-opaque type representing an SPMD execution mask.

## Design Decisions

1. **Internal only**: Users never write `mask` in Go source code. It is
   synthesized by the SSA package during lowering.

2. **SSA-local, not in go/types**: `MaskType` lives in a shared sub-package
   under x-tools-spmd, not as a `BasicKind` in the Go fork. This avoids
   modifying the Go standard library for an internal concern.

3. **Wrapped in SPMDType**: `Varying[mask]` is represented as
   `SPMDType{VaryingQualifier, MaskType{}}`, consistent with all other varying
   types. This lets existing SPMDType infrastructure (hash, string, assignment)
   work with masks.

4. **Shared sub-package**: `MaskType` lives in
   `golang.org/x/tools/go/types/spmd` to avoid circular dependencies between
   `go/ssa` (which uses masks) and `go/types/typeutil` (which hashes types).

## Type Properties

- **Architecture-opaque**: No defined bit width. TinyGo chooses the concrete
  representation per target (e.g., `<4 x i32>` on WASM, `<N x i1>` on native).
- **Cannot be stored in memory**: Only valid in registers and on the mask stack
  (push/pop). This constraint is enforced by the backend, not the type system.
- **Convertible to/from `Varying[bool]`**: Explicit conversion at SSA level.
  `Varying[bool]` is the user-visible boolean vector; `Varying[mask]` is the
  internal execution mask.
- **Singleton**: A single `MaskInstance` value is shared across the package.

## Package Layout

```
x-tools-spmd/go/types/spmd/
  mask.go          -- MaskType definition, MaskInstance, IsMask helper
```

## Type Definition

```go
package spmd

import "go/types"

// MaskType represents an opaque SPMD execution mask.
// It is internal to the SSA/compiler pipeline and never appears in user code.
// The concrete representation (bit width, format) is architecture-dependent.
//
// MaskType implements go/types.Type so it can be used as the element type
// inside SPMDType: SPMDType{VaryingQualifier, MaskType{}}.
//
// Constraints:
//   - Cannot be stored in memory (registers and mask stack only)
//   - Convertible to/from Varying[bool] via explicit SSA operations
type MaskType struct{}

// MaskInstance is the singleton MaskType value.
var MaskInstance = &MaskType{}

// Underlying returns the mask type itself (it has no underlying type).
func (t *MaskType) Underlying() types.Type { return t }

// String returns "mask".
func (t *MaskType) String() string { return "mask" }

// IsMask reports whether t is a MaskType.
func IsMask(t types.Type) bool {
	_, ok := t.(*MaskType)
	return ok
}

// IsVaryingMask reports whether t is SPMDType{Varying, MaskType{}}.
func IsVaryingMask(t types.Type) bool {
	if st, ok := t.(*types.SPMDType); ok && st.IsVarying() {
		return IsMask(st.Elem())
	}
	return false
}

// NewVaryingMask returns SPMDType{VaryingQualifier, MaskType{}}.
func NewVaryingMask() *types.SPMDType {
	return types.NewVarying(MaskInstance)
}
```

## Hash Support

In `x-tools-spmd/go/types/typeutil/map.go`:

```go
import "golang.org/x/tools/go/types/spmd"

// In hash():
case *spmd.MaskType:
    return 9182

// In shallowHash():
case *spmd.MaskType:
    return 9182
```

Prime `9182` is adjacent to the existing SPMDType prime `9181`.

## Detection in TinyGo

```go
import "golang.org/x/tools/go/types/spmd"

// When lowering an SSA value with type SPMDType{Varying, MaskType{}}:
if spmd.IsVaryingMask(val.Type()) {
    // Emit architecture-specific mask (e.g., <4 x i32> on WASM)
}
```

## Future Work (not in this change)

- SSA opcodes for mask operations (MaskPush, MaskPop, MaskAnd, MaskOr, etc.)
- Automatic mask insertion during `go for` lowering
- Masked load/store/gather/scatter using MaskType parameters
- Masked `if` (varying condition) using MaskType
- TinyGo lowering changes to consume MaskType values

## Files Changed

| File | Change |
|------|--------|
| `x-tools-spmd/go/types/spmd/mask.go` | New: MaskType, MaskInstance, helpers |
| `x-tools-spmd/go/types/typeutil/map.go` | Add hash cases for *spmd.MaskType |
