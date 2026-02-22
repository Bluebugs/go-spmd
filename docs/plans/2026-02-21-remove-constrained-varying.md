# Remove Constrained Varying Types — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove all `Varying[T, N]` constrained type support and replace group-based cross-lane operations with `*Within` functions.

**Architecture:** Three-layer removal (Go frontend types → TinyGo backend → examples/docs), plus addition of 4 `*Within` cross-lane operations in the lanes standard library with TinyGo LLVM lowering. Each layer is independently testable.

**Tech Stack:** Go compiler (go/ast, go/parser, go/types, types2), TinyGo LLVM backend, WASM SIMD128

**Design Doc:** `docs/plans/2026-02-21-remove-constrained-varying-design.md`

---

## Task 1: Remove Constraint from Go AST and Parser (go/src/go/)

**Files:**

- Modify: `go/src/go/ast/ast.go` (line ~785 — Constraint field on RangeStmt)
- Modify: `go/src/go/parser/parser.go` (lines ~1349-1362, ~1600-1614, ~2068-2176)
- Modify: `go/src/go/parser/parser_spmd_test.go` (remove ~9 constrained test cases)

**Step 1: Remove Constraint field from RangeStmt**

In `go/src/go/ast/ast.go`, delete the `Constraint` field from `RangeStmt`:

```go
// BEFORE:
IsSpmd     bool        // true for "go for" SPMD loops
LaneCount  int64       // effective SIMD lane count (set by type checker)
Constraint Expr        // SPMD constraint expression from range[N] syntax; nil if unconstrained

// AFTER:
IsSpmd    bool  // true for "go for" SPMD loops
LaneCount int64 // effective SIMD lane count (set by type checker)
```

**Step 2: Remove parseSpmdConstraint function**

In `go/src/go/parser/parser.go`, delete the entire `parseSpmdConstraint()` function (~lines 2162-2176).

**Step 3: Remove constraint parsing from parseSpmdForStmt**

In `parseSpmdForStmt()`, remove:

- `var constraint ast.Expr` declaration
- All calls to `p.parseSpmdConstraint()`
- All assignments of `constraint` to `RangeStmt.Constraint`

**Step 4: Remove constrained type parsing from parseIndexOrSliceOrInstance**

In `parseIndexOrSliceOrInstance()` (~lines 1349-1362), replace the SPMD conditional block:

```go
// BEFORE (simplified):
if len(list) > 0 && buildcfg.Experiment.SPMD {
    if t := p.tryIdentOrType(); t != nil {
        list = append(list, t)
    } else {
        list = append(list, p.parseRhs())
    }
} else {
    list = append(list, p.parseType())
}

// AFTER:
list = append(list, p.parseType())
```

**Step 5: Remove constrained type parsing from parseTypeInstance**

In `parseTypeInstance()` (~lines 1600-1614), same change — remove the SPMD conditional and keep only the standard type parsing path.

**Step 6: Remove constrained parser tests**

In `go/src/go/parser/parser_spmd_test.go`, delete all test cases that use `Varying[T, N]` syntax (tests with two type arguments). Keep tests for `Varying[T]` (single type argument) and `go for` without constraints.

**Step 7: Fix any compilation errors from Constraint field removal**

Search for any other references to `RangeStmt.Constraint` in `go/src/go/` and remove or update them.

**Step 8: Run go/parser tests**

Run: `cd go/src && GOEXPERIMENT=spmd go test ./go/parser/ -run SPMD -v`
Expected: All remaining (non-constrained) parser tests PASS

**Step 9: Build Go toolchain**

Run: `cd go/src && GOEXPERIMENT=spmd ./make.bash`
Expected: Build succeeds

**Step 10: Commit**

```
Remove constrained type parsing from go/parser

Remove Constraint field from RangeStmt AST, delete
parseSpmdConstraint function, and remove Varying[T,N]
parsing branches. Only Varying[T] syntax remains.
```

---

## Task 2: Remove Constraint from Go Type System (go/src/go/types/)

**Files:**

- Modify: `go/src/go/types/spmd.go` (constraint field, methods, constructor)
- Modify: `go/src/go/types/typexpr_ext_spmd.go` (processLanesVaryingType constraint handling)
- Delete: `go/src/go/types/testdata/spmd/constrained_to_unconstrained.go`
- Delete: `go/src/go/types/testdata/spmd/type_switch_constrained.go`
- Modify: `go/src/go/types/testdata/spmd/array_to_varying.go` (remove constrained refs)

**Step 1: Remove constraint from SPMDType struct**

In `go/src/go/types/spmd.go`:

- Delete `constraint int64` field from `SPMDType` struct
- Delete `NewVaryingConstrained()` constructor function
- Delete `Constraint()` method
- Delete `IsConstrained()` method
- Delete `IsUniversalConstrained()` method

**Step 2: Remove constraint handling from processLanesVaryingType**

In `go/src/go/types/typexpr_ext_spmd.go`:

- Remove the constraint parsing block (the `if len(args) == 2` section that extracts and validates constraints)
- Replace with error if 2 args: `check.errorf(..., "lanes.Varying takes exactly one type argument")`
- Change `NewVaryingConstrained(elem, constraint)` call to `NewVarying(elem)`
- Delete `calculateTypeSize()` and `calculateBaseTypeSize()` if only used for constraint capacity validation

**Step 3: Fix compilation errors**

Search for all references to `Constraint()`, `IsConstrained()`, `IsUniversalConstrained()`, `NewVaryingConstrained` in `go/src/go/types/` and remove or update them. Check:

- `conversions.go` — constrained capacity validation
- `typexpr_ext_spmd_validation.go` — constraint interface validation skip
- `expr_ext_spmd.go` — any constraint-aware expression handling
- `stmt_ext_spmd.go` — any constraint-aware statement handling
- `check_ext_spmd.go` — any constraint-aware checking

**Step 4: Delete constrained test files**

Delete:

- `go/src/go/types/testdata/spmd/constrained_to_unconstrained.go`
- `go/src/go/types/testdata/spmd/type_switch_constrained.go`

Review and edit (remove constrained references from):

- `go/src/go/types/testdata/spmd/array_to_varying.go`
- `go/src/go/types/testdata/spmd/varying_index.go`

**Step 5: Run go/types tests**

Run: `cd go/src && GOEXPERIMENT=spmd go test ./go/types/ -run SPMD -v`
Expected: All remaining type checker tests PASS

**Step 6: Build Go toolchain**

Run: `cd go/src && GOEXPERIMENT=spmd ./make.bash`
Expected: Build succeeds

**Step 7: Commit**

```
Remove constrained types from go/types

Delete constraint field from SPMDType, remove
NewVaryingConstrained constructor and Is*Constrained
methods. Only unconstrained Varying[T] remains.
```

---

## Task 3: Remove Constraint from cmd/compile (types2 + syntax)

**Files:**

- Modify: `go/src/cmd/compile/internal/syntax/nodes.go` (Constraint field on ForStmt)
- Modify: `go/src/cmd/compile/internal/syntax/parser.go` (constraint parsing)
- Modify: `go/src/cmd/compile/internal/types2/spmd.go` (mirrors go/types)
- Modify: `go/src/cmd/compile/internal/types2/typexpr_ext_spmd.go` (mirrors go/types)
- Delete: `go/src/cmd/compile/internal/types2/testdata/spmd/constrained_to_unconstrained.go`
- Delete: `go/src/cmd/compile/internal/types2/testdata/spmd/type_switch_constrained.go`
- Delete: `go/src/cmd/compile/internal/types2/testdata/spmd/simd_capacity.go` (if constraint-only)
- Modify: `go/src/cmd/compile/internal/ssagen/ssa_ext_spmd.go` (update comment)

**Step 1: Mirror all go/types changes into types2**

Apply identical changes from Task 2 to `cmd/compile/internal/types2/`:

- Remove constraint from `types2/spmd.go` (same fields, methods, constructors)
- Remove constraint handling from `types2/typexpr_ext_spmd.go` (same processLanesVaryingType changes)
- Delete constrained test files from `types2/testdata/spmd/`

**Step 2: Remove Constraint field from syntax ForStmt**

In `cmd/compile/internal/syntax/nodes.go`, delete:

```go
Constraint Expr // constraint for SPMD "range[n]" - nil means no constraint
```

**Step 3: Remove constraint parsing from syntax parser**

In `cmd/compile/internal/syntax/parser.go`:

- Remove `newSpmdRangeClause()` or equivalent constraint parsing functions
- Remove `spmdRangeClause()` if it handles `range[N]`
- Remove `range[N]` parsing from `spmdForStmt()`
- Remove constrained type parsing branches (similar to go/parser changes)

**Step 4: Update SSA comment**

In `cmd/compile/internal/ssagen/ssa_ext_spmd.go` (~line 609):

```go
// BEFORE:
// Unrecognized (From, FromConstrained, ToConstrained) - fall through to normal call

// AFTER:
// Unrecognized (From) - fall through to normal call
```

**Step 5: Fix compilation errors**

Search for all constraint references in `cmd/compile/internal/` and fix.

**Step 6: Run types2 tests**

Run: `cd go/src && GOEXPERIMENT=spmd go test ./cmd/compile/internal/types2/ -run SPMD -v`
Expected: All remaining type checker tests PASS

**Step 7: Run full syntax tests**

Run: `cd go/src && GOEXPERIMENT=spmd go test ./cmd/compile/internal/syntax/ -v`
Expected: PASS

**Step 8: Build Go toolchain (both modes)**

Run: `cd go/src && GOEXPERIMENT=spmd ./make.bash && ./make.bash`
Expected: Both builds succeed

**Step 9: Commit**

```
Remove constrained types from types2 and syntax

Mirror go/types constraint removal into cmd/compile/internal/types2.
Remove Constraint field from syntax ForStmt and range[N] parsing.
```

---

## Task 4: Remove FromConstrained/ToConstrained from lanes package

**Files:**

- Modify: `go/src/lanes/lanes.go` (delete FromConstrained, ToConstrained, convertConstrainedToVarying)

**Step 1: Delete constrained functions**

In `go/src/lanes/lanes.go`:

- Delete `FromConstrained[T]` function (~lines 117-128)
- Delete `ToConstrained[T]` function (~lines 130-139)
- Delete `convertConstrainedToVarying[T]` helper function (~lines 196-207)
- Update architecture comment at top of file — remove constrained conversion explanation

**Step 2: Build Go toolchain**

Run: `cd go/src && GOEXPERIMENT=spmd ./make.bash`
Expected: Build succeeds

**Step 3: Commit**

```
Remove FromConstrained and ToConstrained from lanes package

Delete builtin functions for constrained varying conversion.
Only unconstrained Varying[T] operations remain.
```

---

## Task 5: Add *Within cross-lane operations to lanes package

**Files:**

- Modify: `go/src/lanes/lanes.go` (add 4 new function stubs)

**Step 1: Add *Within function declarations**

In `go/src/lanes/lanes.go`, add after the existing cross-lane functions:

```go
// RotateWithin rotates values within independent groups of groupSize lanes.
// Groups: lanes [0..groupSize-1], [groupSize..2*groupSize-1], etc.
// groupSize must be a compile-time constant that evenly divides the lane count.
//
//go:noinline
func RotateWithin[T any](v Varying[T], offset int, groupSize int) Varying[T] {
 panic("lanes.RotateWithin is a compiler builtin and should be replaced during compilation")
}

// ShiftLeftWithin shifts values left within independent groups, filling with zero.
// groupSize must be a compile-time constant that evenly divides the lane count.
//
//go:noinline
func ShiftLeftWithin[T any](v Varying[T], amount int, groupSize int) Varying[T] {
 panic("lanes.ShiftLeftWithin is a compiler builtin and should be replaced during compilation")
}

// ShiftRightWithin shifts values right within independent groups, filling with zero.
// groupSize must be a compile-time constant that evenly divides the lane count.
//
//go:noinline
func ShiftRightWithin[T any](v Varying[T], amount int, groupSize int) Varying[T] {
 panic("lanes.ShiftRightWithin is a compiler builtin and should be replaced during compilation")
}

// SwizzleWithin permutes values within independent groups using indices.
// groupSize must be a compile-time constant that evenly divides the lane count.
//
//go:noinline
func SwizzleWithin[T any](v Varying[T], indices Varying[int], groupSize int) Varying[T] {
 panic("lanes.SwizzleWithin is a compiler builtin and should be replaced during compilation")
}
```

**Step 2: Build Go toolchain**

Run: `cd go/src && GOEXPERIMENT=spmd ./make.bash`
Expected: Build succeeds

**Step 3: Commit**

```
Add *Within cross-lane operations to lanes package

Add RotateWithin, ShiftLeftWithin, ShiftRightWithin, and
SwizzleWithin functions for group-based cross-lane operations.
These replace constrained varying for algorithms that need
fixed-size group processing (e.g., base64 4-byte groups).
```

---

## Task 6: Remove constrained support from TinyGo backend

**Files:**

- Modify: `tinygo/compiler/spmd.go` (remove Constraint fields, FromConstrained/ToConstrained, spmdResizeVector)
- Delete constrained tests from: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Remove Constraint fields from SPMD structs**

In `tinygo/compiler/spmd.go`:

- Delete `Constraint int64` from `SPMDLoopInfo` struct (~line 26)
- Delete `Constraint int64` from `SPMDParamInfo` struct (~line 33)

**Step 2: Remove constraint extraction from extractSPMDLoops**

In `extractSPMDLoops()` (~lines 75-85):

- Delete the constraint extraction block that reads `rangeStmt.Constraint`
- Remove `Constraint: constraint,` from `SPMDLoopInfo` initialization

**Step 3: Simplify spmdEffectiveLaneCount**

```go
// BEFORE:
func (c *compilerContext) spmdEffectiveLaneCount(spmdType *types.SPMDType, elemLLVM llvm.Type) int {
 if spmdType.IsConstrained() && spmdType.Constraint() > 0 {
  return int(spmdType.Constraint())
 }
 return c.spmdLaneCount(elemLLVM)
}

// AFTER:
func (c *compilerContext) spmdEffectiveLaneCount(spmdType *types.SPMDType, elemLLVM llvm.Type) int {
 return c.spmdLaneCount(elemLLVM)
}
```

Consider inlining calls to `spmdEffectiveLaneCount` if it's now just a pass-through.

**Step 4: Remove builtin interception for FromConstrained/ToConstrained**

In the builtin interception switch (~lines 1636-1640):

- Delete `case strings.HasPrefix(name, "lanes.FromConstrained["):` and its handler
- Delete `case strings.HasPrefix(name, "lanes.ToConstrained["):` and its handler

**Step 5: Delete createFromConstrained function**

Delete `createFromConstrained()` entirely (~lines 2392-2523, ~130 lines).

**Step 6: Delete createToConstrained function**

Delete `createToConstrained()` entirely (~lines 2525-2582, ~57 lines).

**Step 7: Delete spmdResizeVector function**

Delete `spmdResizeVector()` entirely (~lines 341-360) if only used by constrained code.

**Step 8: Remove constraint references from analyzeSPMDSignature**

Delete `Constraint: spmdType.Constraint(),` from any SPMDParamInfo initialization.

**Step 9: Fix compilation errors**

Search for remaining references to `Constraint`, `IsConstrained`, `createFromConstrained`, `createToConstrained`, `spmdResizeVector` in `tinygo/compiler/` and fix.

**Step 10: Delete constrained test cases from spmd_llvm_test.go**

Delete test functions:

- `TestSPMDConstrainedLaneCount`
- `TestSPMDConstrainedConst`
- `TestFromConstrainedDimensions`
- `TestFromConstrainedShuffleMask`
- `TestFromConstrainedMaskValues`
- `TestToConstrainedReconstruction`
- `TestFromConstrainedSingleGroup`
- `TestFromConstrainedUniversalError`

Also remove any constrained test cases within other test functions (search for "constrained" in test file).

**Step 11: Build TinyGo**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: Build succeeds

**Step 12: Run TinyGo SPMD tests**

Run: `cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run SPMD -v`
Expected: All remaining (non-constrained) tests PASS

**Step 13: Commit**

```
Remove constrained varying support from TinyGo backend

Delete Constraint fields from SPMDLoopInfo/SPMDParamInfo,
remove FromConstrained/ToConstrained LLVM implementations,
simplify spmdEffectiveLaneCount to always use hardware lanes.
```

---

## Task 7: Add *Within builtin interception to TinyGo backend

**Files:**

- Modify: `tinygo/compiler/spmd.go` (add builtin interception + LLVM lowering for 4 *Within functions)
- Add tests to: `tinygo/compiler/spmd_llvm_test.go`

**Step 1: Write failing tests for RotateWithin**

In `tinygo/compiler/spmd_llvm_test.go`, add:

```go
func TestSPMDRotateWithin(t *testing.T) {
 // Test: RotateWithin with groupSize=4 on 16-lane byte vector
 // Input:  [0,1,2,3, 4,5,6,7, 8,9,10,11, 12,13,14,15]
 // Rotate by 1 within groups of 4:
 // Output: [1,2,3,0, 5,6,7,4, 9,10,11,8, 13,14,15,12]
 // Verify the generated shuffle vector mask
}
```

**Step 2: Run test to verify it fails**

Run: `cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMDRotateWithin -v`
Expected: FAIL

**Step 3: Add builtin interception for *Within functions**

In `tinygo/compiler/spmd.go`, in the builtin interception switch, add cases:

```go
case strings.HasPrefix(name, "lanes.RotateWithin["):
 return b.createRotateWithin(instr, name)
case strings.HasPrefix(name, "lanes.ShiftLeftWithin["):
 return b.createShiftLeftWithin(instr, name)
case strings.HasPrefix(name, "lanes.ShiftRightWithin["):
 return b.createShiftRightWithin(instr, name)
case strings.HasPrefix(name, "lanes.SwizzleWithin["):
 return b.createSwizzleWithin(instr, name)
```

**Step 4: Implement createRotateWithin**

```go
func (b *builder) createRotateWithin(instr *ssa.Call, name string) (llvm.Value, error) {
 // Extract arguments: value, offset, groupSize
 args := instr.Call.Args
 value := b.getValue(args[0])
 offset := b.getValue(args[1]) // must be constant
 groupSize := b.getValue(args[2]) // must be constant

 vecType := value.Type()
 totalLanes := vecType.VectorSize()

 // Extract constant groupSize
 groupSizeVal := groupSize.SExtValue() // or appropriate constant extraction

 // Build shuffle mask for rotation within groups
 mask := make([]llvm.Value, totalLanes)
 for i := 0; i < totalLanes; i++ {
  group := i / int(groupSizeVal)
  lane := i % int(groupSizeVal)
  rotated := (lane + int(offset)) % int(groupSizeVal)
  if rotated < 0 {
   rotated += int(groupSizeVal)
  }
  mask[i] = llvm.ConstInt(b.ctx.Int32Type(), uint64(group*int(groupSizeVal)+rotated), false)
 }

 shuffleMask := llvm.ConstVector(mask, false)
 return b.CreateShuffleVector(value, llvm.Undef(vecType), shuffleMask, ""), nil
}
```

**Step 5: Run test to verify it passes**

Run: `cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run TestSPMDRotateWithin -v`
Expected: PASS

**Step 6: Implement ShiftLeftWithin and ShiftRightWithin**

Similar to RotateWithin but shifted elements are replaced with zero (use poison/zero instead of wrapping).

**Step 7: Implement SwizzleWithin**

Takes varying indices — remap each index to stay within its group, then use shufflevector.

**Step 8: Write and run tests for remaining *Within functions**

Add `TestSPMDShiftLeftWithin`, `TestSPMDShiftRightWithin`, `TestSPMDSwizzleWithin`.

**Step 9: Build TinyGo**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: Build succeeds

**Step 10: Run all SPMD tests**

Run: `cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run SPMD -v`
Expected: All PASS

**Step 11: Commit**

```
Add *Within cross-lane builtin interception to TinyGo

Implement RotateWithin, ShiftLeftWithin, ShiftRightWithin,
and SwizzleWithin LLVM lowering using shufflevector with
per-group index masks.
```

---

## Task 8: Update examples — remove constrained, rewrite IPv4/base64

**Files:**

- Delete: `examples/varying-universal-constrained/` (entire directory)
- Delete: `examples/illegal-spmd/invalid-lane-constraints.go`
- Verify: `examples/ipv4-parser/main.go` (review and verify)
- Modify: `examples/base64-decoder/main.go` (rewrite with *Within)
- Modify: `examples/type-casting-varying/main.go` (remove constrained sections)
- Modify: `examples/type-switch-varying/main.go` (remove constrained cases)
- Check/modify: all other examples for any Varying[T, N] references

**Step 1: Delete varying-universal-constrained example**

Delete entire directory `examples/varying-universal-constrained/`.

**Step 2: Delete invalid-lane-constraints illegal example**

Delete `examples/illegal-spmd/invalid-lane-constraints.go`.

**Step 3: Remove constrained sections from type-casting-varying**

In `examples/type-casting-varying/main.go`:

- Delete all `Varying[T, N]` variable declarations
- Delete constrained casting examples
- Keep only unconstrained `Varying[T]` examples

**Step 4: Remove constrained cases from type-switch-varying**

In `examples/type-switch-varying/main.go`:

- Remove `case lanes.Varying[byte, 8]:` and similar constrained type switch cases
- Keep only `case lanes.Varying[T]:` unconstrained cases

**Step 5: Rewrite IPv4 parser with unconstrained varying**

Verify that `examples/ipv4-parser/main.go` use unconstrained `go for` loops with accumulation patterns (see design doc for approach). Key changes:

- Replace `range[16]` with `range` over `[16]byte`
- Replace `Varying[T, 16]` with `Varying[T]`
- Accumulate into varying variables, reduce outside loop
- Remove all constraint-specific code

**Step 6: Rewrite base64 decoder with *Within functions**

Rewrite `examples/base64-decoder/main.go` to use `RotateWithin`, `ShiftLeftWithin` etc. with `groupSize=4`. Key changes:

- Replace `range[4]` with unconstrained `range`
- Replace `Varying[T, 4]` with `Varying[T]`
- Replace `lanes.Rotate(v, offset)` with `lanes.RotateWithin(v, offset, 4)`
- Replace `lanes.ShiftLeft(v, amount)` with `lanes.ShiftLeftWithin(v, amount, 4)`

**Step 7: Check remaining examples**

Search all `examples/*/main.go` for any remaining `Varying[T,` patterns (with comma indicating constraint). Fix any found.

**Step 8: Commit**

```
Remove constrained varying from all examples

Delete varying-universal-constrained and invalid-lane-constraints.
Verified ipv4-parser with unconstrained accumulation pattern.
Rewrite base64-decoder with *Within cross-lane operations.
Remove constrained sections from type-casting and type-switch examples.
```

---

## Task 9: Update integration tests

**Files:**

- Delete: `test/integration/spmd/varying-universal-constrained/` (entire directory)
- Modify: `test/e2e/spmd-e2e-test.sh` (remove constrained test entries)
- Check: all files in `test/integration/spmd/` for constrained references

**Step 1: Delete varying-universal-constrained integration test**

Delete entire directory `test/integration/spmd/varying-universal-constrained/`.

**Step 2: Check for constrained references in other integration tests**

Search `test/integration/spmd/` for `Varying[` with comma patterns. Fix any found.

**Step 3: Update E2E test script**

In `test/e2e/spmd-e2e-test.sh`:

- Remove `varying-universal-constrained` from test lists
- Remove `invalid-lane-constraints` from reject lists
- Update test counts in comments

**Step 4: Run E2E tests**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh`
Expected: No regressions from constrained removal; some failures may now be resolved

**Step 5: Commit**

```
Update integration tests for constrained varying removal

Delete varying-universal-constrained test directory.
Update E2E test script to remove constrained test entries.
```

---

## Task 10: Update documentation

**Files:**

- Delete: `docs/fromconstrained_mask_issue.md`
- Modify: `CLAUDE.md` (remove all Varying[T,N] references)
- Modify: `PLAN.md` (update phase status, remove constrained tasks, update deferred items)
- Modify: `bluebugs.github.io/content/blogs/cross-lane-communication.md` (update base64 example)
- Modify: `bluebugs.github.io/content/blogs/go-spmd-ipv4-parser.md` (update parser example)

**Step 1: Delete fromconstrained_mask_issue.md**

Delete `docs/fromconstrained_mask_issue.md` — the issue no longer exists.

**Step 2: Update CLAUDE.md**

Major updates:

- Remove `Varying[T, N]` from "Key Concepts" section
- Remove `range[N]` from syntax examples
- Remove FromConstrained/ToConstrained from function lists
- Add `*Within` functions to cross-lane operations documentation
- Update "lanes package" API section
- Update implementation status sections
- Remove constrained-related deferred items
- Update E2E test counts

**Step 3: Update PLAN.md**

- Remove Phase 2.8c tasks (Constrained Varying backend)
- Update deferred items — remove constrained-specific items
- Update test counts
- Add notes about constrained removal decision

**Step 4: Update blog posts**

Update `cross-lane-communication.md`:

- Replace base64 example using `range[4]` and `Varying[T,4]` with `*Within` approach

Update `go-spmd-ipv4-parser.md`:

- Replace `range[16]` and `Varying[T,16]` with unconstrained approach

**Step 5: Update memory files**

Update `/home/cedric/.claude/projects/-home-cedric-work-SPMD/memory/MEMORY.md`:

- Remove constrained-specific entries
- Add note about constrained removal
- Update API section

**Step 6: Commit**

```
Update documentation for constrained varying removal

Remove fromconstrained_mask_issue.md (issue no longer exists).
Update CLAUDE.md, PLAN.md, and blog posts to reflect
removal of Varying[T,N] and addition of *Within operations.
```

---

## Task 11: Full test suite verification

**Step 1: Build Go toolchain (both modes)**

```bash
cd go/src && GOEXPERIMENT=spmd ./make.bash
cd go/src && ./make.bash
```

Expected: Both builds succeed

**Step 2: Run all Go SPMD tests**

```bash
cd go/src && GOEXPERIMENT=spmd go test ./go/parser/ -run SPMD -v
cd go/src && GOEXPERIMENT=spmd go test ./go/types/ -run SPMD -v
cd go/src && GOEXPERIMENT=spmd go test ./cmd/compile/internal/types2/ -run SPMD -v
cd go/src && GOEXPERIMENT=spmd go test ./cmd/compile/internal/syntax/ -v
```

Expected: All PASS

**Step 3: Build TinyGo**

```bash
cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go
```

Expected: Build succeeds

**Step 4: Run TinyGo SPMD tests**

```bash
cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run SPMD -v
```

Expected: All PASS

**Step 5: Run E2E tests**

```bash
bash test/e2e/spmd-e2e-test.sh
```

Expected: No regressions; document any improvements from constrained removal

**Step 6: Fix any regressions found**

If any tests fail due to the removal, fix them.

**Step 7: Commit any fixes**

```
Fix test regressions from constrained varying removal
```

---

## Task 12: Update E2E results and final documentation

**Step 1: Record new E2E test counts**

Run E2E tests and document new counts:

- How many run pass / compile-only pass / reject OK / compile fail
- Which previously-failing tests now pass (if any)
- Which tests were removed

**Step 2: Update PLAN.md with final counts**

**Step 3: Update MEMORY.md with final state**

**Step 4: Final commit**

```
Update E2E results after constrained varying removal
```

---

## Execution Order and Dependencies

```
Task 1 (go/ast + go/parser)
    ↓
Task 2 (go/types)
    ↓
Task 3 (types2 + syntax) — depends on Tasks 1-2 for pattern
    ↓
Task 4 (lanes package — remove FromConstrained/ToConstrained)
    ↓
Task 5 (lanes package — add *Within stubs)
    ↓
Task 6 (TinyGo backend — remove constrained) — depends on Tasks 1-4
    ↓
Task 7 (TinyGo backend — add *Within lowering) — depends on Tasks 5-6
    ↓
Task 8 (examples) — depends on Tasks 5, 7
    ↓
Task 9 (integration tests) — depends on Task 8
    ↓
Task 10 (documentation) — depends on Tasks 1-9
    ↓
Task 11 (full verification) — depends on all above
    ↓
Task 12 (final results) — depends on Task 11
```

**Parallelizable pairs:**

- Tasks 1-3 could be combined into a single "Go frontend" commit if preferred
- Tasks 4-5 are sequential but small, could be one commit
- Tasks 8-9 could be combined

**Estimated scope:** 12 tasks, ~1200 lines removed, ~200 lines added across ~25 files.
