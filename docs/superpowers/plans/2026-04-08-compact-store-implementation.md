# `lanes.CompactStore` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `lanes.CompactStore[T](dst []T, v Varying[T], mask Varying[bool]) int` — a SIMD compress-store builtin that writes active lanes contiguously and returns the count.

**Architecture:** Three-repo change following existing builtin patterns. Go fork: declaration + type checking. x-tools-spmd: `SPMDCompactStore` SSA instruction + predication + sanity. TinyGo: interception + LLVM lowering (constant-mask path first, runtime-mask deferred).

**Tech Stack:** Go (go/types, go/ssa), LLVM IR (via TinyGo), WASM SIMD128, x86 SSE/AVX2

**Spec:** `docs/superpowers/specs/2026-04-08-compact-store-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### Go fork (`go/`)
- **Modify:** `src/lanes/lanes.go` — add `CompactStore` declaration
- **Modify:** `src/go/types/call_ext_spmd.go` — add type-checking for `CompactStore`
- **Create:** `src/go/types/testdata/spmd/compact_store.go` — type checker test cases

### x-tools-spmd (`x-tools-spmd/`)
- **Modify:** `go/ssa/ssa.go` — add `SPMDCompactStore` struct
- **Modify:** `go/ssa/print.go` — add `String()` method
- **Modify:** `go/ssa/emit.go` — add `emitSPMDCompactStore` helper
- **Modify:** `go/ssa/sanity.go` — add sanity checks
- **Modify:** `go/ssa/spmd_predicate.go` — handle in `spmdMaskMemOps` and `spmdConvertScopedMemOps`
- **Create:** `go/ssa/spmd_compact_store_test.go` — SSA construction + predication tests

### TinyGo (`tinygo/`)
- **Modify:** `compiler/compiler.go` — add `*ssa.SPMDCompactStore` case in instruction dispatch
- **Modify:** `compiler/spmd.go` — add `createSPMDCompactStore` lowering function
- **Create:** `compiler/spmd_compact_store_test.go` — LLVM IR output tests

### Integration test
- **Create:** `test/integration/spmd/compact-store/main.go` — E2E correctness test

---

## Task 1: Go fork — `lanes.CompactStore` declaration

**Files:**
- Modify: `go/src/lanes/lanes.go:200` (after `DotProductI8x16Add`)

- [ ] **Step 1: Add CompactStore declaration to lanes package**

In `go/src/lanes/lanes.go`, add after the `DotProductI8x16Add` function (before the `integer` type constraint):

```go
// CompactStore writes the active lanes of v contiguously to dst.
// Active means both the explicit mask lane is true AND the current
// execution mask lane is active. Returns the number of elements written.
// COMPILER BUILTIN: replaced with SIMD compress-store instructions.
//
//go:noinline
func CompactStore[T any](dst []T, v Varying[T], mask Varying[bool]) int {
	panic("lanes.CompactStore is a compiler builtin and should be replaced during compilation")
}
```

- [ ] **Step 2: Verify Go fork builds**

Run:
```bash
cd /home/cedric/work/SPMD && make build-go
```
Expected: Build succeeds.

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/lanes/lanes.go
git commit -m "feat: add lanes.CompactStore builtin declaration"
```

---

## Task 2: Go fork — type checking for `CompactStore`

**Files:**
- Modify: `go/src/go/types/call_ext_spmd.go`
- Create: `go/src/go/types/testdata/spmd/compact_store.go`

- [ ] **Step 1: Write type checker test cases**

Create `go/src/go/types/testdata/spmd/compact_store.go`:

```go
// Test type-checking for lanes.CompactStore.
package spmd_test

import "lanes"

// Valid: CompactStore in SPMD loop with correct types
func testCompactStoreValid(dst []byte, src []byte) {
	go for i, ch := range src {
		_ = i
		mask := ch > 0
		n := lanes.CompactStore(dst, ch, mask)
		_ = n
	}
}

// Valid: CompactStore in SPMD function body
func testCompactStoreInFunc(dst []int32, v lanes.Varying[int32], mask lanes.Varying[bool]) {
	n := lanes.CompactStore(dst, v, mask)
	_ = n
}

// ERROR: CompactStore with wrong slice element type
func testCompactStoreTypeMismatch(dst []int32, src []byte) {
	go for _, ch := range src {
		mask := ch > 0
		_ = lanes.CompactStore(dst, ch, mask) // ERROR "cannot use"
	}
}
```

- [ ] **Step 2: Add CompactStore validation to type checker**

In `go/src/go/types/call_ext_spmd.go`, add a detection function and wire it into `validateSPMDFunctionCall`:

```go
// isLanesCompactStoreCall checks if this is a call to lanes.CompactStore
func (check *Checker) isLanesCompactStoreCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	if sel.Sel.Name != "CompactStore" {
		return false
	}

	if name, ok := sel.X.(*ast.Ident); ok {
		if obj := check.lookup(name.Name); obj != nil {
			if pkg, ok := obj.(*PkgName); ok {
				return pkg.imported.name == "lanes"
			}
		}
	}

	return false
}
```

Then add to `validateSPMDFunctionCall`:

```go
if check.isLanesCompactStoreCall(call) {
	// CompactStore requires varying arguments — no additional context restriction
	// beyond what the type system already enforces (Varying[T] params require
	// SPMD context). Type matching (dst []T vs v Varying[T]) handled by
	// standard generic instantiation.
}
```

Note: `CompactStore` has no special context restriction (unlike `lanes.Index` which requires SPMD context, or `*Within` which requires groupSize validation). The standard generic type checker already validates that `dst []T` matches `v Varying[T]` and that `mask` is `Varying[bool]`. The only SPMD-specific concern is that `Varying[bool]` parameters force SPMD context, which the existing type system handles.

- [ ] **Step 3: Run type checker tests**

Run:
```bash
cd /home/cedric/work/SPMD/go && GOEXPERIMENT=spmd go test ./src/go/types/... -run TestSPMD -v
```
Expected: Tests pass including new compact_store test cases.

- [ ] **Step 4: Verify Go fork builds**

Run:
```bash
cd /home/cedric/work/SPMD && make build-go
```
Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/call_ext_spmd.go src/go/types/testdata/spmd/compact_store.go
git commit -m "feat: add type checking for lanes.CompactStore"
```

---

## Task 3: x-tools-spmd — `SPMDCompactStore` SSA instruction

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go:1493` (after SPMDStore)
- Modify: `x-tools-spmd/go/ssa/print.go:431` (after SPMDStore.String)
- Modify: `x-tools-spmd/go/ssa/emit.go:703` (after emitSPMDStore)
- Modify: `x-tools-spmd/go/ssa/sanity.go:234` (after SPMDStore case)

- [ ] **Step 1: Add SPMDCompactStore struct to ssa.go**

In `x-tools-spmd/go/ssa/ssa.go`, after the `SPMDStore` type definition (line 1493), add:

```go
// SPMDCompactStore stores active lanes of Val contiguously into Addr.
// Effective mask = ExplicitMask AND enclosing execution mask.
// Produces a uniform int: the number of elements written (popcount of effective mask).
// ExplicitMask uses the existing mask representation (Varying[bool] at Go level,
// lowered to <N x i1> on WASM or <N x i32> on x86).
//
// Example printed form:
//
//	t5 = spmd_compact_store<16> t1 t2 mask t3 len t4
type SPMDCompactStore struct {
	register
	Addr         Value     // *T (pointer extracted from slice)
	Val          Value     // Varying[T]
	ExplicitMask Value     // Varying[bool] (user-provided mask)
	Lanes        int       // SIMD width
	Source       Value     // original slice (for bounds check)
	SourceLen    Value     // len(slice) (for bounds check)
	pos          token.Pos // optional source position
}
```

- [ ] **Step 2: Add Operands method**

In `x-tools-spmd/go/ssa/ssa.go`, after the `SPMDStore.Operands` method (around line 2171), add:

```go
func (s *SPMDCompactStore) Operands(rands []*Value) []*Value {
	rands = append(rands, &s.Addr, &s.Val, &s.ExplicitMask)
	if s.Source != nil {
		rands = append(rands, &s.Source)
	}
	if s.SourceLen != nil {
		rands = append(rands, &s.SourceLen)
	}
	return rands
}
```

- [ ] **Step 3: Add Pos method**

In `x-tools-spmd/go/ssa/ssa.go`, after the `SPMDStore.Pos` method (around line 2191), add:

```go
func (s *SPMDCompactStore) Pos() token.Pos { return s.pos }
```

- [ ] **Step 4: Add String method to print.go**

In `x-tools-spmd/go/ssa/print.go`, after `SPMDStore.String()` (around line 431), add:

```go
func (s *SPMDCompactStore) String() string {
	return fmt.Sprintf("spmd_compact_store<%d> %s %s mask %s len %s",
		s.Lanes, spmdRelName(s.Addr, s), spmdRelName(s.Val, s),
		spmdRelName(s.ExplicitMask, s), spmdRelName(s.SourceLen, s))
}
```

- [ ] **Step 5: Add emitSPMDCompactStore to emit.go**

In `x-tools-spmd/go/ssa/emit.go`, after `emitSPMDStore` (around line 703), add:

```go
// emitSPMDCompactStore emits an SPMDCompactStore instruction.
// addr is a pointer to the destination element type.
// val is the Varying[T] value to compact.
// mask is the explicit Varying[bool] mask.
// source is the original slice value (for bounds checking).
// sourceLen is the len of the slice (for bounds checking).
// The result type is int (uniform).
func emitSPMDCompactStore(f *Function, addr, val, mask, source, sourceLen Value, lanes int, pos token.Pos) *SPMDCompactStore {
	s := &SPMDCompactStore{
		Addr:         addr,
		Val:          val,
		ExplicitMask: mask,
		Lanes:        lanes,
		Source:       source,
		SourceLen:    sourceLen,
		pos:          pos,
	}
	s.setType(types.Typ[types.Int]) // returns uniform int
	f.emit(s)
	return s
}
```

- [ ] **Step 6: Add sanity checks to sanity.go**

In `x-tools-spmd/go/ssa/sanity.go`, after the `*SPMDStore` case (around line 252), add:

```go
case *SPMDCompactStore:
	if instr.Lanes <= 0 {
		s.errorf("SPMDCompactStore: Lanes must be > 0, got %d", instr.Lanes)
	}
	if !spmd.IsVaryingMask(instr.ExplicitMask.Type()) {
		s.errorf("SPMDCompactStore: ExplicitMask must be Varying[mask], got %s", instr.ExplicitMask.Type())
	}
	if _, ok := instr.Addr.Type().Underlying().(*types.Pointer); !ok {
		s.errorf("SPMDCompactStore: Addr must be a pointer type, got %s", instr.Addr.Type())
	}
	if !types.Identical(instr.Type(), types.Typ[types.Int]) {
		s.errorf("SPMDCompactStore: result type must be int, got %s", instr.Type())
	}
```

- [ ] **Step 7: Run x-tools-spmd tests**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -v -count=1 2>&1 | tail -20
```
Expected: All existing tests pass. (New instruction not yet emitted so no functional change.)

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/print.go go/ssa/emit.go go/ssa/sanity.go
git commit -m "feat: add SPMDCompactStore SSA instruction"
```

---

## Task 4: x-tools-spmd — predication support for `SPMDCompactStore`

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` — handle in `spmdMaskMemOps`, `spmdConvertScopedMemOps`, `spmdConvertAllMemOps`

`SPMDCompactStore` is emitted at SSA construction time (from `lanes.CompactStore` calls), not created by the predication pass from plain Store instructions. However, the predication pass needs to AND the execution mask into the `ExplicitMask` when a CompactStore appears inside a varying branch.

- [ ] **Step 1: Add SPMDCompactStore handling to spmdMaskMemOps**

In `x-tools-spmd/go/ssa/spmd_predicate.go`, in function `spmdMaskMemOps` (line 3623), add a new case in the `switch instr := instr.(type)` block, after the `*Store` case:

```go
case *SPMDCompactStore:
	// CompactStore already has an explicit mask from the user.
	// AND it with the branch mask to get the effective mask.
	combined := spmdInsertMaskAndAt(b, i, mask, instr.ExplicitMask)
	instr.ExplicitMask = combined
	spmdAddReferrer(mask, instr)
```

Note: `spmdInsertMaskAndAt` may not exist — check if `spmdInsertMaskAnd` inserts before terminator or at a specific position. If `spmdMaskMemOps` operates on instructions in-place (which it does — it modifies `b.Instrs[i]`), we need to insert the AND before the instruction. Look at how the mask is threaded — in `spmdMaskMemOps`, the mask is passed in as a parameter and applied wholesale. For `SPMDCompactStore`, we AND the passed-in mask with the existing `ExplicitMask`.

The simplest approach: create the AND BinOp and insert it before position `i` in the block:

```go
case *SPMDCompactStore:
	// CompactStore already carries an explicit user mask.
	// AND with the branch/scope mask to get effective mask.
	andOp := &BinOp{Op: token.AND, X: mask, Y: instr.ExplicitMask}
	andOp.setType(spmdpkg.NewVaryingMask())
	andOp.setBlock(b)
	// Insert AND before this instruction.
	b.Instrs = append(b.Instrs[:i+1], b.Instrs[i:]...)
	b.Instrs[i] = andOp
	spmdAddReferrer(mask, andOp)
	spmdAddReferrer(instr.ExplicitMask, andOp)
	instr.ExplicitMask = andOp
	spmdAddReferrer(andOp, instr)
	i++ // skip past the inserted AND
```

Actually, check the existing pattern more carefully. `spmdMaskMemOps` replaces Store→SPMDStore with the mask baked in. For `SPMDCompactStore`, the instruction is already an `SPMDCompactStore` (not a plain Store), so we just update its ExplicitMask. The cleanest approach:

```go
case *SPMDCompactStore:
	// SPMDCompactStore already has a user-provided ExplicitMask.
	// AND it with the scope mask so varying control flow is respected.
	andOp := &BinOp{Op: token.AND, X: mask, Y: instr.ExplicitMask}
	andOp.setType(spmdpkg.NewVaryingMask())
	andOp.setBlock(b)
	spmdAddReferrer(mask, andOp)
	spmdAddReferrer(instr.ExplicitMask, andOp)
	// Insert the AND before this instruction.
	newInstrs := make([]Instruction, 0, len(b.Instrs)+1)
	newInstrs = append(newInstrs, b.Instrs[:i]...)
	newInstrs = append(newInstrs, andOp)
	newInstrs = append(newInstrs, b.Instrs[i:]...)
	b.Instrs = newInstrs
	instr.ExplicitMask = andOp
	spmdAddReferrer(andOp, instr)
	i++ // skip past inserted AND on next iteration
```

- [ ] **Step 2: Add SPMDCompactStore handling to spmdConvertScopedMemOps**

In `spmdConvertScopedMemOps` (line 1990), add similar handling. This function iterates blocks and converts plain Store/UnOp to SPMDStore/SPMDLoad. For `SPMDCompactStore`, it should AND the scope mask:

```go
case *SPMDCompactStore:
	// Already an SPMDCompactStore — AND scope mask into ExplicitMask.
	andOp := &BinOp{Op: token.AND, X: mask, Y: instr.ExplicitMask}
	andOp.setType(spmdpkg.NewVaryingMask())
	andOp.setBlock(block)
	spmdAddReferrer(mask, andOp)
	spmdAddReferrer(instr.ExplicitMask, andOp)
	newInstrs := make([]Instruction, 0, len(block.Instrs)+1)
	newInstrs = append(newInstrs, block.Instrs[:i]...)
	newInstrs = append(newInstrs, andOp)
	newInstrs = append(newInstrs, block.Instrs[i:]...)
	block.Instrs = newInstrs
	instr.ExplicitMask = andOp
	spmdAddReferrer(andOp, instr)
	i++
```

- [ ] **Step 3: Add SPMDCompactStore handling to spmdConvertAllMemOps**

In `spmdConvertAllMemOps` (line 3432), add the same pattern — this handles func body scope.

- [ ] **Step 4: Run x-tools-spmd tests**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -v -count=1 2>&1 | tail -20
```
Expected: All existing tests pass.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go
git commit -m "feat: handle SPMDCompactStore in predication passes"
```

---

## Task 5: x-tools-spmd — SSA emission of `SPMDCompactStore` from `lanes.CompactStore` calls

**Files:**
- Modify: `x-tools-spmd/go/ssa/builder.go` or wherever `lanes.From` / `lanes.Broadcast` calls are intercepted during SSA construction

The key question is where `lanes.CompactStore` calls get intercepted. Unlike `lanes.From` (which is intercepted in TinyGo), `CompactStore` needs to be an SSA instruction so that predication can AND the execution mask. So we need to intercept it during SSA construction in x-tools-spmd.

- [ ] **Step 1: Find the call interception point**

`lanes.CompactStore` must be intercepted during SSA construction (not in TinyGo) because the predication pass needs to see the `SPMDCompactStore` instruction to AND the execution mask. Search for where other `lanes.*` builtins that become SSA instructions are intercepted:

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && grep -rn '"lanes"' go/ssa/*.go | grep -v test | head -30
cd /home/cedric/work/SPMD/x-tools-spmd && grep -rn 'SPMDIndex\|SPMDVectorFromMemory\|emitSPMD' go/ssa/builder.go go/ssa/spmd_promote.go | head -30
```

The pattern: `SPMDIndex` is emitted from `lanes.Index()` calls, `SPMDVectorFromMemory` from `lanes.From()` calls. Find the call dispatch that recognizes these and add `CompactStore` alongside. The interception must extract the slice argument, split it into pointer + length, and call `emitSPMDCompactStore`.

- [ ] **Step 2: Add CompactStore interception**

At the identified interception point, add logic to recognize `lanes.CompactStore` calls and emit `SPMDCompactStore`:

```go
// Detect lanes.CompactStore[T](dst []T, v Varying[T], mask Varying[bool]) int
if callee != nil && callee.Pkg() != nil && callee.Pkg().Name() == "lanes" &&
	strings.HasPrefix(callee.Name(), "CompactStore[") {
	// Extract arguments: dst (slice), v (Varying[T]), mask (Varying[bool])
	dst := args[0]   // []T
	val := args[1]   // Varying[T]
	mask := args[2]  // Varying[bool]

	// Extract pointer and length from the slice.
	addr := emitExtract(fn, dst, 0) // pointer
	srcLen := emitExtract(fn, dst, 1) // length

	lanes := fn.SPMDLoops[0].Lanes // or derive from context
	return emitSPMDCompactStore(fn, addr, val, mask, dst, srcLen, lanes, pos)
}
```

The exact code depends on how the interception site works — adapt to the pattern used by other lanes builtins that produce SSA instructions. The lane count should be derived from the enclosing SPMD loop or function context (same as SPMDStore).

- [ ] **Step 3: Run x-tools-spmd tests**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -v -count=1 2>&1 | tail -20
```
Expected: All existing tests pass.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/builder.go  # or whichever file was modified
git commit -m "feat: emit SPMDCompactStore from lanes.CompactStore calls"
```

---

## Task 6: x-tools-spmd — unit tests for `SPMDCompactStore`

**Files:**
- Create: `x-tools-spmd/go/ssa/spmd_compact_store_test.go`

- [ ] **Step 1: Write SSA construction test**

Create `x-tools-spmd/go/ssa/spmd_compact_store_test.go`:

```go
package ssa_test

import (
	"strings"
	"testing"
)

func TestSPMDCompactStoreEmission(t *testing.T) {
	src := `
package main

import "lanes"

func main() {
	dst := make([]byte, 32)
	src := []byte("SGVsbG8=")
	go for i, ch := range src {
		_ = i
		mask := ch != byte('=')
		lanes.CompactStore(dst, ch, mask)
	}
}
`
	fn := buildSPMDFunction(t, src, "main")
	if fn == nil {
		t.Fatal("failed to build SPMD function")
	}

	// Check that SPMDCompactStore instruction was emitted.
	found := false
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			s := instr.String()
			if strings.Contains(s, "spmd_compact_store") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected SPMDCompactStore instruction, found none")
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				t.Logf("  %s", instr)
			}
		}
	}
}
```

Note: Adapt `buildSPMDFunction` to match the test helper pattern used in existing `spmd_*_test.go` files (e.g., `spmd_loop_test.go`, `spmd_varying_test.go`).

- [ ] **Step 2: Write predication mask-AND test**

Add a test that verifies the execution mask gets AND'd into `ExplicitMask` when CompactStore is inside a varying if:

```go
func TestSPMDCompactStoreMaskAND(t *testing.T) {
	src := `
package main

import "lanes"

func main() {
	dst := make([]byte, 32)
	src := []byte("Hello World")
	go for i, ch := range src {
		_ = i
		if ch > byte('Z') {
			mask := ch != byte(' ')
			lanes.CompactStore(dst, ch, mask)
		}
	}
}
`
	fn := buildSPMDFunction(t, src, "main")
	if fn == nil {
		t.Fatal("failed to build SPMD function")
	}

	// The CompactStore should have its mask AND'd with the if-condition mask.
	// Look for a BinOp{AND} feeding into the SPMDCompactStore's ExplicitMask.
	found := false
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			s := instr.String()
			if strings.Contains(s, "spmd_compact_store") {
				found = true
				// The mask operand should reference an AND operation
				// (exact check depends on SSA print format)
			}
		}
	}
	if !found {
		t.Error("expected SPMDCompactStore instruction, found none")
	}
}
```

- [ ] **Step 3: Run tests**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -run TestSPMDCompactStore -v
```
Expected: Both tests pass.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_compact_store_test.go
git commit -m "test: add SPMDCompactStore SSA emission and predication tests"
```

---

## Task 7: TinyGo — intercept `SPMDCompactStore` in instruction dispatch

**Files:**
- Modify: `tinygo/compiler/compiler.go:1937` (after `*ssa.SPMDStore` case)

- [ ] **Step 1: Add SPMDCompactStore case to instruction dispatch**

In `tinygo/compiler/compiler.go`, in the main instruction switch (around line 1937 after `case *ssa.SPMDStore:`), add:

```go
case *ssa.SPMDCompactStore:
	return b.createSPMDCompactStore(instr)
```

Note: `SPMDStore` is handled as a statement (no return value) at line 1937. `SPMDCompactStore` produces a value (the popcount), so it should be handled in the value-producing expression switch (around line 3803, near `*ssa.SPMDLoad`):

```go
case *ssa.SPMDCompactStore:
	return b.createSPMDCompactStore(expr), nil
```

Check which switch handles value-producing SPMD instructions (SPMDLoad, SPMDSelect, SPMDIndex are around line 3803-3807) and add the case there.

- [ ] **Step 2: Verify TinyGo builds**

Run:
```bash
cd /home/cedric/work/SPMD && make build-tinygo
```
Expected: Build succeeds (even though `createSPMDCompactStore` doesn't exist yet — this step will fail; proceed to Task 8).

---

## Task 8: TinyGo — `createSPMDCompactStore` LLVM lowering (constant-mask path)

**Files:**
- Modify: `tinygo/compiler/spmd.go` — add `createSPMDCompactStore` function

This task implements the constant-mask fast path. The runtime-mask path is deferred to a follow-up task.

- [ ] **Step 1: Add createSPMDCompactStore stub**

In `tinygo/compiler/spmd.go`, add the main function:

```go
// createSPMDCompactStore handles SPMDCompactStore lowering.
// Returns the number of elements written (uniform int).
func (b *builder) createSPMDCompactStore(instr *ssa.SPMDCompactStore) llvm.Value {
	// Scalar fallback: when laneCount=1, compact store is just a conditional store.
	if !b.simdEnabled {
		return b.createSPMDCompactStoreScalar(instr)
	}

	addr := b.getValue(instr.Addr, instr.Pos())
	val := b.getValue(instr.Val, instr.Pos())
	mask := b.getValue(instr.ExplicitMask, instr.Pos())
	sourceLen := b.getValue(instr.SourceLen, instr.Pos())
	laneCount := mask.Type().VectorSize()

	// Try constant-mask fast path.
	if constMask, ok := b.spmdExtractConstantMask(mask, laneCount); ok {
		return b.createSPMDCompactStoreConst(addr, val, constMask, sourceLen, laneCount)
	}

	// Runtime mask path.
	return b.createSPMDCompactStoreRuntime(addr, val, mask, sourceLen, laneCount)
}
```

- [ ] **Step 2: Add scalar fallback**

```go
// createSPMDCompactStoreScalar handles laneCount=1: conditional store + return 0 or 1.
func (b *builder) createSPMDCompactStoreScalar(instr *ssa.SPMDCompactStore) llvm.Value {
	addr := b.getValue(instr.Addr, instr.Pos())
	val := b.getValue(instr.Val, instr.Pos())
	mask := b.getValue(instr.ExplicitMask, instr.Pos())

	// mask is a scalar i1 in scalar mode.
	thenBlock := b.insertBasicBlock("compact.then")
	doneBlock := b.insertBasicBlock("compact.done")
	b.CreateCondBr(mask, thenBlock, doneBlock)

	b.SetInsertPointAtEnd(thenBlock)
	b.CreateStore(val, addr)
	b.CreateBr(doneBlock)

	b.SetInsertPointAtEnd(doneBlock)
	// Return 1 if stored, 0 if not.
	phi := b.CreatePHI(b.intType, "compact.n")
	phi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(b.intType, 1, false), llvm.ConstInt(b.intType, 0, false)},
		[]llvm.BasicBlock{thenBlock, b.GetInsertBlock()}, // adjust predecessor tracking
	)
	return phi
}
```

Note: The phi predecessor handling needs care — check how `spmdConditionalStore` handles this pattern in existing code and follow the same approach.

- [ ] **Step 3: Add constant mask extraction**

```go
// spmdExtractConstantMask checks if a mask LLVM value is a compile-time constant.
// Returns a boolean slice of which lanes are active and true if constant.
func (b *builder) spmdExtractConstantMask(mask llvm.Value, laneCount int) ([]bool, bool) {
	if mask.IsConstant().IsNil() {
		return nil, false
	}
	result := make([]bool, laneCount)
	for i := 0; i < laneCount; i++ {
		elem := llvm.ConstExtractElement(mask, llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false))
		if elem.IsNil() {
			return nil, false
		}
		// Check if the element is a constant integer.
		val := elem.ZExtValue()
		result[i] = val != 0
	}
	return result, true
}
```

- [ ] **Step 4: Add constant-mask compact store**

```go
// createSPMDCompactStoreConst emits a shuffle-based compact store for a constant mask.
func (b *builder) createSPMDCompactStoreConst(addr, val llvm.Value, mask []bool, sourceLen llvm.Value, laneCount int) llvm.Value {
	// Count active lanes.
	activeCount := 0
	for _, m := range mask {
		if m {
			activeCount++
		}
	}

	if activeCount == 0 {
		return llvm.ConstInt(b.intType, 0, false)
	}

	// Build compaction shuffle indices: active lanes packed to front.
	indices := make([]llvm.Value, laneCount)
	elemType := val.Type().ElementType()
	outIdx := 0
	for i := 0; i < laneCount; i++ {
		if mask[i] {
			indices[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
			outIdx++
		} else {
			// Inactive lanes get undef (don't care).
			indices[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(laneCount), false) // undef via out-of-range
		}
	}

	// Build the compaction shuffle: active lanes move to positions 0..activeCount-1.
	compactIndices := make([]llvm.Value, laneCount)
	outIdx = 0
	for i := 0; i < laneCount; i++ {
		if mask[i] {
			compactIndices[outIdx] = llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
			outIdx++
		}
	}
	// Fill remaining with undef.
	for i := outIdx; i < laneCount; i++ {
		compactIndices[i] = llvm.Undef(b.ctx.Int32Type())
	}
	shuffleMask := llvm.ConstVector(compactIndices, false)
	compacted := b.CreateShuffleVector(val, llvm.Undef(val.Type()), shuffleMask, "compact.shuffle")

	// Bounds check: verify activeCount <= sourceLen.
	activeConst := llvm.ConstInt(b.intType, uint64(activeCount), false)
	// TODO: emit bounds check (compare activeConst <= sourceLen, panic if not)

	// Store the compacted vector. Only the first activeCount elements matter.
	// For byte-width: use a truncated store.
	elemSize := b.targetData.TypeAllocSize(elemType)
	storeBytes := uint64(activeCount) * elemSize

	if storeBytes == uint64(laneCount)*elemSize {
		// All lanes active — full vector store.
		st := b.CreateStore(compacted, addr)
		st.SetAlignment(int(elemSize))
	} else {
		// Partial store: bitcast to byte array and store activeCount*elemSize bytes.
		// Use a series of scalar stores or a narrowed vector store.
		b.spmdCompactStorePartial(compacted, addr, activeCount, elemType, laneCount)
	}

	return activeConst
}
```

- [ ] **Step 5: Add partial store helper**

```go
// spmdCompactStorePartial stores the first n elements from a vector to addr.
func (b *builder) spmdCompactStorePartial(vec, addr llvm.Value, n int, elemType llvm.Type, laneCount int) {
	// Strategy: extract and store individual elements.
	// For small n this is fine. For larger n, could use a narrowed vector store.
	for i := 0; i < n; i++ {
		elem := b.CreateExtractElement(vec, llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false), "compact.elem")
		gep := b.CreateInBoundsGEP(elemType, addr, []llvm.Value{
			llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false),
		}, "compact.gep")
		b.CreateStore(elem, gep)
	}
}
```

Note: This is a correct-but-naive implementation. For 12-from-16 (base64), it generates 12 scalar stores. A future optimization can use `i8x16.swizzle` + narrowed vector store. The important thing is correctness first.

- [ ] **Step 6: Add runtime mask stub (returns error for now)**

```go
// createSPMDCompactStoreRuntime handles runtime (non-constant) masks.
// TODO: implement prefix-sum index computation + shuffle.
func (b *builder) createSPMDCompactStoreRuntime(addr, val, mask, sourceLen llvm.Value, laneCount int) llvm.Value {
	// Fallback: extract per-lane and store conditionally.
	elemType := val.Type().ElementType()
	outIdx := llvm.ConstInt(b.intType, 0, false)

	for i := 0; i < laneCount; i++ {
		lane := llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false)
		elem := b.CreateExtractElement(val, lane, "compact.rt.elem")
		active := b.CreateExtractElement(mask, lane, "compact.rt.mask")

		// Convert mask element to i1 if needed (x86 uses i32 masks).
		if active.Type() != b.ctx.Int1Type() {
			active = b.CreateICmp(llvm.IntNE, active, llvm.ConstInt(active.Type(), 0, false), "")
		}

		thenBlock := b.insertBasicBlock("compact.rt.then")
		doneBlock := b.insertBasicBlock("compact.rt.done")
		b.CreateCondBr(active, thenBlock, doneBlock)

		b.SetInsertPointAtEnd(thenBlock)
		gep := b.CreateInBoundsGEP(elemType, addr, []llvm.Value{outIdx}, "compact.rt.gep")
		b.CreateStore(elem, gep)
		nextIdx := b.CreateAdd(outIdx, llvm.ConstInt(b.intType, 1, false), "compact.rt.next")
		b.CreateBr(doneBlock)

		b.SetInsertPointAtEnd(doneBlock)
		phi := b.CreatePHI(b.intType, "compact.rt.idx")
		phi.AddIncoming([]llvm.Value{nextIdx, outIdx}, []llvm.BasicBlock{thenBlock, doneBlock})
		outIdx = phi
	}

	return outIdx
}
```

This is a correct scalar-per-lane fallback. It handles any runtime mask correctly. The prefix-sum SIMD optimization can replace this later.

- [ ] **Step 7: Verify TinyGo builds**

Run:
```bash
cd /home/cedric/work/SPMD && make build-tinygo
```
Expected: Build succeeds.

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/spmd.go
git commit -m "feat: add createSPMDCompactStore LLVM lowering"
```

---

## Task 9: TinyGo — LLVM IR output tests

**Files:**
- Create: `tinygo/compiler/spmd_compact_store_test.go`

- [ ] **Step 1: Write LLVM IR test for constant mask**

Create `tinygo/compiler/spmd_compact_store_test.go`. Follow the pattern in `tinygo/compiler/spmd_llvm_test.go` — use the test helper that compiles Go source to LLVM IR and checks for expected instructions.

```go
package compiler

import (
	"testing"
)

func TestSPMDCompactStoreConstMask(t *testing.T) {
	src := `
package main

import "lanes"

func main() {
	dst := make([]byte, 16)
	src := []byte("AAAA")
	go for i, ch := range src {
		pos := i % 4
		mask := pos != 3
		lanes.CompactStore(dst, ch, mask)
	}
}
`
	// Compile to LLVM IR and verify shuffle + store instructions appear.
	ir := compileSPMDToIR(t, src, true /* simd=true */)
	assertContains(t, ir, "shufflevector") // compaction shuffle
	assertNotContains(t, ir, "scatter")    // should NOT use scatter
}
```

Adapt `compileSPMDToIR` and assertion helpers to match the existing test infrastructure in `spmd_llvm_test.go`.

- [ ] **Step 2: Write LLVM IR test for scalar fallback**

```go
func TestSPMDCompactStoreScalar(t *testing.T) {
	src := `
package main

import "lanes"

func main() {
	dst := make([]byte, 16)
	src := []byte("AAAA")
	go for i, ch := range src {
		pos := i % 4
		mask := pos != 3
		lanes.CompactStore(dst, ch, mask)
	}
}
`
	// Compile with simd=false — should use conditional scalar stores.
	ir := compileSPMDToIR(t, src, false /* simd=false */)
	assertContains(t, ir, "store")
	assertNotContains(t, ir, "shufflevector")
}
```

- [ ] **Step 3: Run tests**

Run:
```bash
cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd go test ./compiler/... -run TestSPMDCompactStore -v
```
Expected: Both tests pass.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_compact_store_test.go
git commit -m "test: add LLVM IR tests for SPMDCompactStore lowering"
```

---

## Task 10: Integration test — E2E correctness

**Files:**
- Create: `test/integration/spmd/compact-store/main.go`

- [ ] **Step 1: Write E2E integration test**

Create `test/integration/spmd/compact-store/main.go`:

```go
// run -goexperiment spmd

// E2E test for lanes.CompactStore.
// Tests constant-mask compaction (4→3 pattern) and runtime-mask compaction.
package main

import (
	"fmt"
	"lanes"
)

// scalarCompact is the reference implementation.
func scalarCompact(dst, src []byte, mask []bool) int {
	n := 0
	for i := 0; i < len(src) && i < len(mask); i++ {
		if mask[i] {
			dst[n] = src[i]
			n++
		}
	}
	return n
}

func main() {
	allPass := true

	// Test 1: Constant mask — every 4th lane inactive (base64 pattern).
	{
		src := []byte("ABCxDEFxGHIxJKLx")
		dst := make([]byte, 16)
		offset := 0
		go for i, ch := range src {
			pos := i % 4
			n := lanes.CompactStore(dst[offset:], ch, pos != 3)
			offset += n
		}

		expected := "ABCDEFGHIJKL"
		got := string(dst[:offset])
		if got != expected {
			fmt.Printf("FAIL test1: got %q, want %q\n", got, expected)
			allPass = false
		}
	}

	// Test 2: All lanes active.
	{
		src := []byte("Hello")
		dst := make([]byte, 8)
		offset := 0
		go for i, ch := range src {
			_ = i
			n := lanes.CompactStore(dst[offset:], ch, true)
			offset += n
		}

		if string(dst[:offset]) != "Hello" {
			fmt.Printf("FAIL test2: got %q, want %q\n", string(dst[:offset]), "Hello")
			allPass = false
		}
	}

	// Test 3: Filter — only keep ASCII lowercase.
	{
		src := []byte("HeLLo WoRLd")
		dst := make([]byte, 16)
		offset := 0
		go for i, ch := range src {
			_ = i
			isLower := ch >= byte('a') && ch <= byte('z')
			n := lanes.CompactStore(dst[offset:], ch, isLower)
			offset += n
		}

		expected := "eood"
		got := string(dst[:offset])
		if got != expected {
			fmt.Printf("FAIL test3: got %q, want %q\n", got, expected)
			allPass = false
		}
	}

	if allPass {
		fmt.Println("PASS")
	} else {
		fmt.Println("FAIL")
	}
}
```

- [ ] **Step 2: Compile and run on WASM**

Run:
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=true -o /tmp/compact-store-simd.wasm test/integration/spmd/compact-store/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/compact-store-simd.wasm
```
Expected: `PASS`

- [ ] **Step 3: Compile and run scalar mode**

Run:
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=false -o /tmp/compact-store-scalar.wasm test/integration/spmd/compact-store/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/compact-store-scalar.wasm
```
Expected: `PASS` (same output in scalar mode)

- [ ] **Step 4: Add to E2E test script**

Add `compact-store` to the test list in `test/e2e/spmd-e2e-test.sh` at the appropriate level.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/compact-store/main.go test/e2e/spmd-e2e-test.sh
git commit -m "test: add lanes.CompactStore E2E integration test"
```

---

## Task 11: Update base64-mula-lemire example to use CompactStore

**Files:**
- Modify: `test/integration/spmd/base64-mula-lemire/main.go`

- [ ] **Step 1: Rewrite packing loop to use CompactStore**

Replace the scalar packing loop in `spmdDecode` with a single `go for` that does both lookup AND packing:

```go
func decodeHotSPMD(dst, src []byte) int {
	offset := 0
	go for i, ch := range src {
		// Step 1: ASCII → 6-bit sextet (nibble LUT)
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}

		// Step 2: 4→3 packing via CompactStore
		next := lanes.Rotate(s, -1)
		pos := i % 4

		var out byte
		if pos == 0 {
			out = (s << 2) | (next >> 4)
		} else if pos == 1 {
			out = (s << 4) | (next >> 2)
		} else if pos == 2 {
			out = (s << 6) | next
		}

		n := lanes.CompactStore(dst[offset:], out, pos != 3)
		offset += n
	}
	return offset
}
```

Update `spmdDecode` to call the new combined function and handle the padding quartet separately.

- [ ] **Step 2: Run correctness tests**

Compile and run the base64-mula-lemire test to verify all test cases still pass:

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -simd=true -o /tmp/base64-compact.wasm test/integration/spmd/base64-mula-lemire/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/base64-compact.wasm
```
Expected: `Correctness: PASS`

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/base64-mula-lemire/main.go
git commit -m "feat: rewrite base64 packing to use lanes.CompactStore"
```

---

## Deferred Items

These should be added to PLAN.md "Deferred Items Collection":

1. **SIMD-optimized partial store**: Replace `spmdCompactStorePartial` (scalar per-element stores) with `i8x16.swizzle`/`vpshufb`-based vector shuffle + narrowed vector store for the constant-mask path. Priority: Medium. Depends on: Task 8 completion.

2. **Prefix-sum runtime mask path**: Replace `createSPMDCompactStoreRuntime` (scalar per-lane fallback) with SIMD prefix-sum index computation + shuffle. Priority: Medium. Depends on: Task 8 completion.

3. **AVX-512 / RVV native compress**: Use `vpcompressb`/`vcompress.vm` when available. Priority: Low. Depends on: AVX-512/RVV target support.

4. **Bounds check emission**: Add runtime bounds check (`popcount <= len(dst)`) before the store. Priority: High. Depends on: Task 8 completion.

5. **`pmaddubsw`/`pmaddwd` pattern detection**: Recognize the specific shift constants in the base64 varying-if pattern and emit multiply-add on AVX2. Priority: Low (peephole optimization). Depends on: Task 11 completion.
