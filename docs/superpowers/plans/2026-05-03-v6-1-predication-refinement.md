# v6.1 Predication-Pass Refinement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore e2e to 105/94/0/93/0/11 baseline by refining the SSA predication pass (only width-fix loop-local Varying allocas) and TinyGo backend (revert over-eager contiguity trace + mask-helper switches), without reverting Phase 2 Part B's lift guard.

**Architecture:** Two-repo split. (1) `x-tools-spmd/go/ssa/spmd_predicate.go` Pass A gains a "loop-local" classifier and only width-fixes allocas whose stored values originate from loop-local computations; external-sourced allocas keep abstract `Lanes()==0`. (2) `tinygo/compiler/spmd.go` reverts the bucket-G `*ssa.UnOp{MUL}` extension in `spmdCanonicalSSAIndex` and the 4 `spmdUnwrapMaskForIntrinsic` switches; the existing `getLLVMType` natural-width fallback (when `typ.Lanes()==0`) handles external allocas correctly.

**Tech Stack:** Go (forked at `/home/cedric/work/SPMD/go`), TinyGo (`/home/cedric/work/SPMD/tinygo`), x-tools-spmd submodule (`/home/cedric/work/SPMD/x-tools-spmd`), LLVM 19.1.2, wasmtime, wasmer, samber/lo benchmarks.

**Spec:** `/home/cedric/work/SPMD/docs/superpowers/specs/2026-05-03-v6-1-predication-refinement-design.md`

---

## Repository layout & build commands

**Root**: `/home/cedric/work/SPMD/`
**Forked Go**: `/home/cedric/work/SPMD/go/bin/go` (must be on PATH for SPMD compilation)
**TinyGo binary**: `/home/cedric/work/SPMD/tinygo/build/tinygo`

**Build TinyGo after editing tinygo/ or x-tools-spmd/**:
```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo
```

**Compile a SPMD test (WASM)**:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi \
  -o /tmp/<name>.wasm test/integration/spmd/<name>/main.go
```

**Run a WASM binary**: `wasmtime /tmp/<name>.wasm`

**Run e2e suite (~5-10 min)**:
```bash
bash test/e2e/spmd-e2e-test.sh
```
The summary line format: `Total tests: 105`, `Compile pass: N`, `Compile fail: N`, `Run pass: N`, `Run fail: N`, `Reject pass: 11`.

**Sentinel n-body (must NOT regress)**:
```bash
cd tinybench && PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected output: `-0.169075164` then `-0.169078071` (NOT NaN).

**SSA unit tests (x-tools-spmd)**:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1
```

**TinyGo compiler unit tests**:
```bash
cd /home/cedric/work/SPMD/tinygo
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./compiler/... -count=1
```

---

## File structure (this plan touches)

| File | Repo | Responsibility |
|---|---|---|
| `x-tools-spmd/go/ssa/spmd_predicate.go` | x-tools-spmd | Pass A — add `spmdAllocaIsLoopLocal` classifier; gate alloca width-fixing on it |
| `x-tools-spmd/go/ssa/spmd_predicate_test.go` | x-tools-spmd | Add unit tests for the classifier |
| `tinygo/compiler/spmd.go` | tinygo | Revert `*ssa.UnOp{MUL}` case from `spmdCanonicalSSAIndex` (~line 5142); revert 3 `spmdUnwrapMaskForIntrinsic` switches (~lines 6031, 6164, 6267); remove reduce-builtin `scalarInput` shortcut (~line 4202) |
| `tinygo/compiler/compiler.go` | tinygo | Revert 1 `spmdUnwrapMaskForIntrinsic` switch (~line 3465) |

**Files NOT touched** (kept from prior phases):
- `tinygo/compiler/spmd.go` `spmdReshapeVector` (Phase 1 Part A — defensive scatter, keep)
- `tinygo/compiler/spmd.go` `*ssa.Convert` case in `spmdCanonicalSSAIndex` (today's swizzle-within fix, keep)
- `go/src/go/types/spmd.go` `Lanes` field (v5 stdlib commit `6523f50aa9`, keep)
- `tinygo/compiler/compiler.go` `getLLVMType` for `*types.SPMDType` (Phase 2 Part B, keep — already correct: reads `typ.Lanes()` when set, falls back to `spmdEffectiveLaneCount`)

---

## Phase 0: Pre-flight verification

### Task 0.1: Confirm starting state and toolchain freshness

**Files:** None modified.

- [ ] **Step 1: Verify clean working tree on relevant branches**

```bash
cd /home/cedric/work/SPMD
git status --short
cd tinygo && git status --short
cd ../x-tools-spmd && git status --short
```
Expected: working tree may have uncommitted changes from earlier Phase 3 cascade fixes. Note them; we'll layer v6.1 on top and squash/refine commits at the end.

- [ ] **Step 2: Verify TinyGo builds and current e2e state**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
bash test/e2e/spmd-e2e-test.sh > /tmp/v6-1-pre-state.log 2>&1
grep -E "Total|Compile pass|Compile fail|Run pass|Run fail|Reject" /tmp/v6-1-pre-state.log | sed 's/\x1b\[[0-9;]*m//g'
```
Expected current state: **94 / 0 / 90 / 3 / 11** (compile pass / compile fail / run pass / run fail / reject pass). The 3 run fails are array-counting, ipv4-parser, pointer-varying.

- [ ] **Step 3: Verify n-body sentinel works**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected output:
```
-0.169075164
-0.169078071
```

If any of these checks fail, STOP — fix the discrepancy before starting v6.1 work.

---

## Phase 1: x-tools-spmd predication-pass refinement (Pass A scoping)

### Task 1.1: Read existing Pass A and understand entry points

**Files:** None modified — investigation only.

- [ ] **Step 1: Read the Pass A code (lines 451-521 of spmd_predicate.go)**

```bash
cd /home/cedric/work/SPMD
sed -n '370,560p' x-tools-spmd/go/ssa/spmd_predicate.go
```
Pay attention to:
- The unconditional alloca walk at lines 462-476 (this is what we're refining)
- `spmdMaybeFixVarying`, `spmdMaybeFixPtrToVarying`, `spmdFixConstVaryingType` helpers (lines 422-449) — reusable
- The pointer worklist propagation (lines 478-521) — keep as-is; it just propagates from already-fixed allocas

- [ ] **Step 2: Locate `spmdLoopInfo` definition**

```bash
grep -n "type SPMDLoopInfo\|IterPhi\|LaneCount" x-tools-spmd/go/ssa/spmd_loop.go x-tools-spmd/go/ssa/ssa.go 2>/dev/null | head -10
```
You'll need to know which fields are available for the classifier (especially `IterPhi`).

- [ ] **Step 3: Confirm test file structure**

```bash
head -30 x-tools-spmd/go/ssa/spmd_predicate_test.go
```
Note the package, imports, and helper functions used (e.g., how a test program is parsed and pass-A-run).

### Task 1.2: Write failing test for `spmdAllocaIsLoopLocal` — loop-local case

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go` (append)

- [ ] **Step 1: Identify existing test pattern for compiling SSA from source**

```bash
grep -nB1 -A 20 "func TestSPMDLanesField\b" x-tools-spmd/go/ssa/spmd_predicate_test.go
```
Note the helper used to build a Function from source code (likely `compileSSA` or similar). Use it for the new tests.

- [ ] **Step 2: Append test that constructs a loop-local Varying alloca**

Append to `x-tools-spmd/go/ssa/spmd_predicate_test.go`. Build a tiny program where `var v lanes.Varying[int]` is initialized from `lanes.Index()` (loop-local), then verify after `predicateSPMD` (or whichever function calls Pass A) that the alloca's pointee `Lanes()` equals the loop's `LaneCount`.

```go
// TestSPMDAllocaIsLoopLocal_LoopLocalIter verifies that an alloca initialized
// from the loop iter (loop-local) is width-fixed by Pass A.
func TestSPMDAllocaIsLoopLocal_LoopLocalIter(t *testing.T) {
	src := `package p

import "lanes"

func F() lanes.Varying[int] {
	var v lanes.Varying[int]
	go for i := range 16 {
		v = lanes.Varying[int](i)
	}
	return v
}
`
	fn := buildSPMDFunction(t, src, "F")
	// Find the alloca for v.
	var vAlloc *Alloc
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if a, ok := instr.(*Alloc); ok {
				if ptr, ok := a.Type().(*types.Pointer); ok {
					if st, ok := ptr.Elem().(*types.SPMDType); ok && st.Elem().String() == "int" {
						vAlloc = a
						break
					}
				}
			}
		}
	}
	if vAlloc == nil {
		t.Fatal("did not find alloca for v")
	}
	ptr := vAlloc.Type().(*types.Pointer)
	st := ptr.Elem().(*types.SPMDType)
	if st.Lanes() == 0 {
		t.Errorf("loop-local alloca should be width-fixed; Lanes()==0")
	}
	if st.Lanes() != 4 {
		t.Errorf("expected Lanes=4 (WASM int loop), got %d", st.Lanes())
	}
}
```

If `buildSPMDFunction` doesn't exist, model after the existing test helper (e.g., `compileSSA`); adapt the call to whatever wrapper actually exists in the test file.

- [ ] **Step 3: Run test to verify it FAILS (or, in this case, currently PASSES because Pass A is unconditional)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDAllocaIsLoopLocal_LoopLocalIter -v
```
Expected: PASS (current Pass A unconditionally width-fixes, so loop-local case happens to work today). This test is a **regression guard** — it must keep passing after the refinement.

### Task 1.3: Write failing test for `spmdAllocaIsLoopLocal` — external (slice element) case

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go` (append)

- [ ] **Step 1: Append test for external alloca (slice-of-Varying load)**

```go
// TestSPMDAllocaIsLoopLocal_ExternalSliceLoad verifies that an alloca whose
// stored value comes from a []Varying[T] slice element load is NOT width-fixed
// (Lanes() stays 0 — TinyGo will use natural width).
func TestSPMDAllocaIsLoopLocal_ExternalSliceLoad(t *testing.T) {
	src := `package p

import "lanes"

func F(s []lanes.Varying[int]) {
	go for _, v := range s {
		_ = v
	}
}
`
	fn := buildSPMDFunction(t, src, "F")
	// Find the alloca for v (the loop variable).
	var vAlloc *Alloc
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if a, ok := instr.(*Alloc); ok {
				if ptr, ok := a.Type().(*types.Pointer); ok {
					if st, ok := ptr.Elem().(*types.SPMDType); ok && st.Elem().String() == "int" {
						vAlloc = a
						break
					}
				}
			}
		}
	}
	if vAlloc == nil {
		t.Fatal("did not find alloca for v")
	}
	ptr := vAlloc.Type().(*types.Pointer)
	st := ptr.Elem().(*types.SPMDType)
	if st.Lanes() != 0 {
		t.Errorf("external alloca (slice-of-Varying load) must keep Lanes()==0, got %d", st.Lanes())
	}
}
```

- [ ] **Step 2: Run to verify it currently FAILS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDAllocaIsLoopLocal_ExternalSliceLoad -v
```
Expected: **FAIL** with message like `external alloca (slice-of-Varying load) must keep Lanes()==0, got 1` — current Pass A unconditionally width-fixes to `loop.LaneCount` (which for a 1-lane outer loop over `[]Varying[int]` is 1).

### Task 1.4: Implement `spmdAllocaIsLoopLocal` classifier

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` (add helper function before `spmdConvertLoopOps`)

- [ ] **Step 1: Add the classifier function**

Insert the following helper near the top of the file (after the other helpers but before `predicateSPMDLoop`):

```go
// spmdAllocaIsLoopLocal reports whether every value stored into alloc is
// derived from the SPMD loop's iter, splatted constants, or other loop-local
// computations — versus loaded from external memory (slice/struct elements,
// function parameters, call results).
//
// Used by Pass A in spmdConvertLoopOps to decide whether to width-fix the
// alloca's pointee Varying[T] type to loop.LaneCount, or leave it abstract
// (Lanes()==0) so TinyGo's getLLVMType uses the natural width path.
//
// The walk is bounded by the visited set to handle phi cycles within the loop
// body (such cycles are loop-local by construction). Unknown / unhandled value
// shapes are conservatively classified as external.
func spmdAllocaIsLoopLocal(alloc *Alloc, loop *spmdLoopInfo) bool {
	visited := make(map[Value]bool)
	stores := []Value{}
	if alloc.Referrers() != nil {
		for _, ref := range *alloc.Referrers() {
			switch r := ref.(type) {
			case *Store:
				if r.Addr == alloc {
					stores = append(stores, r.Val)
				}
			case *SPMDStore:
				if r.Addr == alloc {
					stores = append(stores, r.Val)
				}
			}
		}
	}
	if len(stores) == 0 {
		// No stores: zero-init only — loop-local trivially.
		return true
	}
	for _, sv := range stores {
		if !spmdValueIsLoopLocal(sv, loop, visited) {
			return false
		}
	}
	return true
}

// spmdValueIsLoopLocal classifies a value as loop-local-derived. Returns true
// for iter-derived, splatted-uniform, and arithmetic-of-loop-local values.
// Returns false for parameters, call results, IndexAddr/FieldAddr loads, and
// anything else not enumerated as loop-local.
func spmdValueIsLoopLocal(v Value, loop *spmdLoopInfo, visited map[Value]bool) bool {
	if v == nil {
		return false
	}
	if visited[v] {
		// Cycle (e.g., phi within the loop body) — loop-local by construction.
		return true
	}
	visited[v] = true
	// Direct loop iter: definitely loop-local.
	if loop != nil && loop.IterPhi != nil && Value(loop.IterPhi) == v {
		return true
	}
	switch u := v.(type) {
	case *SPMDIndex:
		// The lane-index vector — loop-local by definition.
		return true
	case *Const:
		// Compile-time constant — uniform, splattable.
		return true
	case *ChangeType:
		return spmdValueIsLoopLocal(u.X, loop, visited)
	case *Convert:
		return spmdValueIsLoopLocal(u.X, loop, visited)
	case *BinOp:
		return spmdValueIsLoopLocal(u.X, loop, visited) && spmdValueIsLoopLocal(u.Y, loop, visited)
	case *UnOp:
		// UnOp{MUL} dereferences external memory — NOT loop-local unless the
		// addr is itself a loop-local alloca.
		if u.Op == token.MUL {
			if a, ok := u.X.(*Alloc); ok {
				return spmdAllocaIsLoopLocal(a, loop)
			}
			return false
		}
		return spmdValueIsLoopLocal(u.X, loop, visited)
	case *Phi:
		for _, edge := range u.Edges {
			if !spmdValueIsLoopLocal(edge, loop, visited) {
				return false
			}
		}
		return true
	case *SPMDSelect:
		return spmdValueIsLoopLocal(u.Cond, loop, visited) &&
			spmdValueIsLoopLocal(u.X, loop, visited) &&
			spmdValueIsLoopLocal(u.Y, loop, visited)
	case *SPMDLoad:
		// Loop-local IFF loading from a loop-local alloca.
		if a, ok := u.Addr.(*Alloc); ok {
			return spmdAllocaIsLoopLocal(a, loop)
		}
		return false
	default:
		// Conservative: parameters, call results, IndexAddr, FieldAddr, etc.
		// are external by default.
		return false
	}
}
```

- [ ] **Step 2: Verify the file still compiles**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go build ./go/ssa/...
```
Expected: clean build.

If build fails: check that `spmdLoopInfo` is the right type name (might be `SPMDLoopInfo` in some places — match the local convention in spmd_predicate.go).

- [ ] **Step 3: Run the loop-local test (still PASS)**

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDAllocaIsLoopLocal_LoopLocalIter -v
```
Expected: PASS — classifier returns true, Pass A still width-fixes (we haven't wired the gate yet).

### Task 1.5: Wire the classifier into Pass A

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` (modify Pass A alloca walk, lines ~462-476)

- [ ] **Step 1: Gate the alloca width-fixing on `spmdAllocaIsLoopLocal`**

Replace the loop at lines 462-476 (the `for _, b := range fn.Blocks { for _, instr := ...` block that scans allocas) with a gated version:

```go
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				alloc, ok := instr.(*Alloc)
				if !ok {
					continue
				}
				fixedPtr := spmdMaybeFixPtrToVarying(alloc.Type())
				if fixedPtr == nil {
					continue
				}
				// v6.1: only width-fix loop-local allocas. External allocas
				// (loaded from []Varying[T] slice elements, parameters, etc.)
				// keep abstract Lanes()==0 so TinyGo uses natural width.
				if !spmdAllocaIsLoopLocal(alloc, loop) {
					continue
				}
				fixed := fixedPtr.Elem().(*types.SPMDType)
				setSPMDValueType(alloc, fixedPtr)
				ptrWorklist = append(ptrWorklist, ptrWorkItem{alloc, fixed})
			}
		}
```

- [ ] **Step 2: Run both classifier tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/ -run TestSPMDAllocaIsLoopLocal -v
```
Expected: BOTH tests PASS now — loop-local case still width-fixes; external case stays abstract.

- [ ] **Step 3: Run full SSA test suite**

```bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -20
```
Expected: all tests pass. If a test from a prior phase fails (e.g., the Phase 2 Part B SSA tests assumed unconditional fixing), update the test's expected output OR re-classify the test program to use loop-local pattern. Do NOT relax the classifier without good reason.

### Task 1.6: Build TinyGo and run sentinel checks after Pass A change

**Files:** None modified — verification only.

- [ ] **Step 1: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
```
Expected: clean build.

- [ ] **Step 2: n-body sentinel (must NOT regress)**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected:
```
-0.169075164
-0.169078071
```

If NaN appears, STOP. The classifier is misclassifying n-body's per-pair accumulators as external. Investigate — the dvx/dvy/dvz/ej allocas should be loop-local (initialized via BinOps of iter-derived values).

- [ ] **Step 3: varying-array-iteration target (should now produce correct values)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/vai.wasm test/integration/spmd/varying-array-iteration/main.go
wasmtime /tmp/vai.wasm 2>&1 | head -10
```
Expected: values like `[10 20 30 40]` (NOT `[10 0 _ _]`). The Varying[int] elements loaded from the slice are now natural-width.

- [ ] **Step 4: Quick e2e snapshot**

```bash
bash test/e2e/spmd-e2e-test.sh > /tmp/v6-1-task-1.6.log 2>&1
grep -E "Total|Compile|Run|Reject" /tmp/v6-1-task-1.6.log | sed 's/\x1b\[[0-9;]*m//g' | head -10
```
Expected: at least 94 / 0 compile, with `varying-array-iteration` moving to PASS (or staying COMPILE OK if it was test-classified that way). `array-counting`, `pointer-varying`, `ipv4-parser` may still fail — they need TinyGo changes (Phase 2).

If compile pass < 94: a regression. STOP and investigate which test regressed.

---

## Phase 2: TinyGo backend reverts

### Task 2.1: Revert `*ssa.UnOp{MUL}` extension in `spmdCanonicalSSAIndex`

**Files:**
- Modify: `tinygo/compiler/spmd.go` (around line 5142-5150)

- [ ] **Step 1: Read current `spmdCanonicalSSAIndex` to confirm structure**

```bash
sed -n '5120,5165p' tinygo/compiler/spmd.go
```
Verify the function has cases for: `*ssa.SPMDLoad`, `*ssa.UnOp` (the one we're removing), `*ssa.ChangeType`, `*ssa.Convert`.

- [ ] **Step 2: Remove the `*ssa.UnOp` case**

Edit `tinygo/compiler/spmd.go`. Remove the entire `case *ssa.UnOp:` block:

```go
		case *ssa.UnOp:
			if u.Op != token.MUL {
				return v
			}
			stored := spmdSSAAllocaStoredValue(u.X)
			if stored == nil {
				return v
			}
			v = stored
```

After removal, the switch should contain only: `*ssa.SPMDLoad`, `*ssa.ChangeType`, `*ssa.Convert`, and the `default` arm.

- [ ] **Step 3: Update the function's leading doc comment**

Remove the now-stale paragraph about `*ssa.UnOp{MUL}` from the doc comment (lines ~5125-5132). Specifically, delete the paragraph starting "Also handles *ssa.UnOp{Op: token.MUL} (plain pointer dereference), which appears when..." through "...enabling contiguous detection to recognise result[i] = t as a vectorisable SPMD access."

Keep the comment about SPMDLoad / ChangeType. Add one short sentence noting the v6.1 design rationale:

```go
// v6.1: removed UnOp{MUL} case — that trace was too eager for per-lane
// gather/scatter patterns (array-counting, pointer-varying broke). The
// underlying need is now resolved upstream by Pass A's loop-local
// classifier in x-tools-spmd's predication pass.
```

- [ ] **Step 4: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
```
Expected: clean build.

### Task 2.2: Verify array-counting and pointer-varying after `UnOp{MUL}` revert

**Files:** None modified — verification + contingency check.

- [ ] **Step 1: array-counting**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go 2>&1 | tail -3
```
Two outcomes:

**(A) Compile succeeds**: continue:
```bash
wasmtime /tmp/ac.wasm
```
Expected: `Array sums: [3 3 4 18]`. If you see this, mark Step 2 ✓ and move to Step 3.

**(B) Compile fails** with the original Part B error: `error: Invalid cast` or `Store operand must be a pointer` (`store <4 x i32> %X, <4 x ptr> %Y`). Apply the **contingency from spec §8.1** — see Task 2.2.B below.

- [ ] **Step 2: pointer-varying**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/pv.wasm test/integration/spmd/pointer-varying/main.go 2>&1 | tail -3
wasmtime /tmp/pv.wasm 2>&1 | tail -10
```
Expected output ending with `Correctness: PASS`.

- [ ] **Step 3: n-body sentinel**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected:
```
-0.169075164
-0.169078071
```

### Task 2.2.B (contingency, only if 2.2 Step 1 hits outcome B): Scope contiguous detection for 1-lane outer loops

**Files:**
- Modify: `tinygo/compiler/spmd.go` `spmdAnalyzeContiguousIndex` (around line 5176)

- [ ] **Step 1: Read current `spmdAnalyzeContiguousIndex`**

```bash
sed -n '5176,5215p' tinygo/compiler/spmd.go
```

- [ ] **Step 2: Add early-return guard for 1-lane loops over slices-of-aggregates**

After the canonicalization step, before the `activeLoops[canonical]` lookup, add:

```go
	// v6.1: skip contiguous detection for 1-lane outer loops iterating over
	// a slice-of-aggregates pattern. Such loops process one outer element per
	// iter; treating them as multi-lane contiguous produces broadcast writes
	// (array-counting, pointer-varying regression).
	if loop, ok := b.spmdLoopState.activeLoops[canonical]; ok && loop.LaneCount == 1 {
		if loop.IsRangeIndex && loop.BoundValue != nil {
			elemType := loop.BoundValue.Type().Underlying()
			if slice, ok := elemType.(*types.Slice); ok {
				switch slice.Elem().Underlying().(type) {
				case *types.Slice, *types.Array, *types.Struct, *types.Pointer:
					return nil, llvm.Value{}, false
				}
			}
		}
	}
```

If the field name `BoundValue` doesn't exist or has a different name, grep for `IsRangeIndex` in `spmd_loop.go` and use the matching field that holds the iteration source.

- [ ] **Step 3: Rebuild and re-test**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ac.wasm test/integration/spmd/array-counting/main.go && wasmtime /tmp/ac.wasm
```
Expected: `Array sums: [3 3 4 18]`.

### Task 2.3: Revert `spmdUnwrapMaskForIntrinsic` switches

**Files:**
- Modify: `tinygo/compiler/spmd.go` (3 sites: ~6031, ~6164, ~6267)
- Modify: `tinygo/compiler/compiler.go` (1 site: ~3465)

- [ ] **Step 1: Restore raw `CreateTrunc` at spmd.go:6031**

Find the line currently containing `maskI1 := b.spmdUnwrapMaskForIntrinsic(mask, laneCount)` followed by an `spmd.idx.clamp` select. There are two such patterns. The first site (around line 6031, was originally `spmd.offset.mask`):

```go
			maskI1 := b.CreateTrunc(mask, llvm.VectorType(b.ctx.Int1Type(), laneCount), "spmd.offset.mask")
```

- [ ] **Step 2: Restore raw `CreateTrunc` at spmd.go:6164 and ~6267**

Both these sites (in `spmdVectorIndexString` and `spmdVectorIndexArray`) had a comment block I added describing the helper switch. Replace each with the original two-line raw form (use `replace_all` on the helper-call pattern if the surrounding contexts are identical, otherwise do them individually):

```go
			maskI1 := b.CreateTrunc(mask, llvm.VectorType(b.ctx.Int1Type(), laneCount), "spmd.idx.mask")
```

Remove the v6 comment block above each call (the one explaining "Use spmdUnwrapMaskForIntrinsic which handles lane-count mismatch...").

- [ ] **Step 3: Restore raw `CreateTrunc` at compiler.go:3465**

Same pattern — the call site for varying IndexAddr clamping. Replace the helper call + comment with the original raw `CreateTrunc(mask, ..., "spmd.idx.mask")` line.

- [ ] **Step 4: Rebuild**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
```
Expected: clean build.

### Task 2.4: Verify ipv4-parser fix

**Files:** None modified — verification.

- [ ] **Step 1: Compile and run ipv4-parser**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/ip.wasm test/integration/spmd/ipv4-parser/main.go 2>&1 | tail -3
wasmtime /tmp/ip.wasm 2>&1 | tail -15
```
Expected: parses correctly — `'192.168.1.1' -> 192.168.1.1`, etc., and a passing summary line.

If compile fails with "Invalid cast" again, the operation width still doesn't match the mask. Investigate: the byte index Varying alloca should be classified loop-local by Pass A and width-fixed to the loop's iter width (4). Use `-internal-printir` and inspect the IR around `spmd.idx.mask`.

- [ ] **Step 2: n-body sentinel**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected:
```
-0.169075164
-0.169078071
```

### Task 2.5: Remove reduce-builtin `scalarInput` shortcut

**Files:**
- Modify: `tinygo/compiler/spmd.go` `createReduceBuiltin` (around line 4202)

- [ ] **Step 1: Locate the scalarInput block**

```bash
grep -n "scalarInput" tinygo/compiler/spmd.go
```
Should find a block I added today: a paragraph comment plus an `if b.simdEnabled { ... scalarInput := ... }` setup, and a modified `if !b.simdEnabled || scalarInput {` branch.

- [ ] **Step 2: Restore original guard**

Replace the v6 multi-line scalarInput setup with the original single check `if !b.simdEnabled {`. Remove the `scalarInput` variable, the speculative `b.getValue` call inside the new block, and the multi-line v6 doc paragraph.

The result should look like:

```go
func (b *builder) createReduceBuiltin(instr *ssa.CallCommon, name string) (llvm.Value, error) {
	// Scalar fallback: with laneCount=1, the input is a scalar (not a vector).
	// Reduction of a single element is identity — return the value directly.
	// reduce.From wraps the scalar in a 1-element stack slice.
	if !b.simdEnabled {
		val := b.getValue(instr.Args[0], getPos(instr))
		// ... existing scalar-handling block unchanged ...
```

- [ ] **Step 3: Rebuild**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd make build-tinygo 2>&1 | tail -3
```
Expected: clean build.

- [ ] **Step 4: Re-verify varying-array-iteration**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -o /tmp/vai.wasm test/integration/spmd/varying-array-iteration/main.go
wasmtime /tmp/vai.wasm 2>&1 | head -15
```
Expected: still produces correct values for `varyingData` (e.g., `[10 20 30 40]`) and reduce.Add returns the right sums. If reduce.Add now SIGSEGVs, Pass A is failing to classify the relevant alloca as external; investigate before continuing.

- [ ] **Step 5: n-body sentinel**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected: `-0.169075164` then `-0.169078071`.

---

## Phase 3: Final gate

### Task 3.1: Full e2e suite

**Files:** None modified.

- [ ] **Step 1: Run full e2e**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/v6-1-final-e2e.log 2>&1
grep -E "Total|Compile|Run|Reject|All tests" /tmp/v6-1-final-e2e.log | sed 's/\x1b\[[0-9;]*m//g'
```
Expected:
```
Total tests:     105
Compile pass:    94
Compile fail:    0
Run pass:        93
Run fail:        0
Reject pass:     11
Reject fail:     0
All tests passed!
```

If any failure remains, list it:
```bash
grep -iE "FAIL|WRONG" /tmp/v6-1-final-e2e.log | sed 's/\x1b\[[0-9;]*m//g' | grep -vE "PASS|pass:|fail:" | sort -u
```

For each failure: investigate via reproducer + IR dump. If it's clearly a v6.1 oversight (a pattern the classifier didn't account for), iterate on the classifier. If it's pre-existing (verify against baseline log at `tinybench/_baseline-2026-04-30/e2e/spmd-e2e-test.log`), document and accept.

### Task 3.2: Unit tests pass in both repos

**Files:** None modified.

- [ ] **Step 1: TinyGo compiler tests**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./compiler/... -count=1 2>&1 | tail -10
```
Expected: all tests PASS. If `TestCompiler` (golden-file IR comparison) fails on tests unrelated to v6.1 changes, those are pre-existing and acceptable; document them.

- [ ] **Step 2: x-tools-spmd SSA tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd go test ./go/ssa/... -count=1 2>&1 | tail -10
```
Expected: all tests PASS, including the two new TestSPMDAllocaIsLoopLocal_* tests added in Phase 1.

### Task 3.3: Bucket-G sentinel re-verification

**Files:** None modified.

- [ ] **Step 1: Verify bucket-G tests still pass**

```bash
grep -E "L0_cond|L4b_varying_break|integ_bit-counting|integ_printf-verbs" /tmp/v6-1-final-e2e.log | sed 's/\x1b\[[0-9;]*m//g'
```
Expected: all four lines start with `PASS`.

### Task 3.4: Submodule pointer bumps

**Files:**
- Modify: `/home/cedric/work/SPMD/.gitmodules` references (via `git submodule update` after submodule commits)

- [ ] **Step 1: Commit changes inside submodules**

In `x-tools-spmd`:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go go/ssa/spmd_predicate_test.go
git status
```
Expected: only those two files modified.

Commit (use whatever the project's commit-message convention is — check recent commits via `git log --oneline -5`):
```bash
git commit -m "$(cat <<'EOF'
feat(spmd): scope Pass A width-fixing to loop-local allocas

v6.1 refinement: only width-fix Varying[T] allocas whose stored values
originate from loop-local computations (iter-derived, splatted constants,
arithmetic). Allocas initialized from external memory (slice-of-Varying
loads, parameters, gather results from external addresses) keep abstract
Lanes()==0 so TinyGo's getLLVMType uses natural width.

Fixes the over-fixing observed in varying-array-iteration where a
Varying[int] loaded from []Varying[int] in a 1-lane outer loop was
incorrectly width-fixed to Lanes=1, losing the inner SIMD vector.

Adds spmdAllocaIsLoopLocal classifier + spmdValueIsLoopLocal helper
walking SSA def-use back from Store/SPMDStore values. Conservative
default: unknown shapes are external.

Two new unit tests cover the loop-local and external-slice-load cases.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

In `tinygo`:
```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/compiler.go
git status
```
Expected: only those two files modified (plus possibly any test golden files that legitimately need updating).

```bash
git commit -m "$(cat <<'EOF'
revert(spmd): drop UnOp{MUL} canonical-trace and mask-helper switches

v6.1 partner change to x-tools-spmd Pass A scoping. With predication
producing correct types (loop-local vs external), TinyGo no longer needs
the bucket-G UnOp{MUL} extension in spmdCanonicalSSAIndex (broke
per-lane scatter/gather: array-counting, pointer-varying) nor the
spmdUnwrapMaskForIntrinsic detours at index-clamp sites (mask widths
now match operation widths naturally).

Also removes the createReduceBuiltin scalarInput shortcut: external
allocas now produce true vector loads, so reduce.Add receives a vector
as designed.

Keeps Phase 1 Part A's spmdReshapeVector defensive scatter and the
swizzle-within Convert trace.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 2: Bump parent submodule pointers**

```bash
cd /home/cedric/work/SPMD
git status
```
Expected: `tinygo` and `x-tools-spmd` show as modified (new commit pointers).

Commit:
```bash
git add tinygo x-tools-spmd
git commit -m "$(cat <<'EOF'
chore: bump tinygo + x-tools-spmd to v6.1 refinement

x-tools-spmd: Pass A loop-local scoping
tinygo: drop bucket-G UnOp{MUL} trace + mask-helper switches

E2E restored to baseline 105/94/0/93/0/11. n-body and n-body-nosqrt
remain unblocked (the original v6 goal). Spec:
docs/superpowers/specs/2026-05-03-v6-1-predication-refinement-design.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 3.5: Final sentinel pass

**Files:** None modified.

- [ ] **Step 1: One final n-body run**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
```
Expected:
```
-0.169075164
-0.169078071
```

- [ ] **Step 2: One final e2e summary**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep -E "Total|Compile|Run|Reject|All tests"
```
Expected: `All tests passed!` with `105/94/0/93/0/11`.

If both pass: v6.1 complete. Update PLAN.md / MEMORY.md as appropriate (separately) and proceed to Phase 4 benchmarks.
