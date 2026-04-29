# SPMD Width-Typed Varying v5 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Move the canonical SPMD lane count from side-channel annotations into the type system itself. Add `lanes int` to `*types.SPMDType`; make the SSA predication pass mutate in-loop Varying values' types to width-fixed; let TinyGo's existing `getLLVMType` derivation handle width naturally via a 5-line change. Unblocks `n-body` / `n-body-nosqrt` AND avoids the v1-v4 cascade pattern (20-23 broken tests at the GATE) by removing the need for ANY downstream consumer-side patching.

**Architecture:** Three layers, total of ~5 lines of TinyGo change.
1. **Stdlib type system** (`go/types` and `cmd/compile/internal/types2`) — extend `*SPMDType` with `lanes int`, add `NewVaryingWithLanes`/`Lanes()`, update `Identical` and `String` to include lanes.
2. **SSA layer** (`x-tools-spmd/go/ssa`) — restore lift guard so varying allocas survive `lift()`; add a small package-private `setSPMDValueType` bridge that calls existing `register.setType`; predication pass walks each loop's scope and mutates each Varying value's type to `Varying[T, loop.LaneCount]`; forward-propagation pass annotates entry-block alloca types from their consumers' widths.
3. **TinyGo backend** — single 5-line change in `getLLVMType` for `*types.SPMDType`: read `typ.Lanes()` when set, fall back to existing element-natural derivation otherwise. ALL ~21 audit sites become AUTOMATICALLY correct because they all flow through `getLLVMType(value.Type())` and value types now carry the right width.

**Tech Stack:**
- Forked Go (`/home/cedric/work/SPMD/go/`, branch `spmd`) — `go/types` and `types2` SPMD type extensions
- Forked `x/tools` (`/home/cedric/work/SPMD/x-tools-spmd/`, branch `spmd`) — `go/ssa` predication, propagation, lift
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`) — `compiler/compiler.go` `getLLVMType` for SPMDType
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`) — n-body / n-body-nosqrt unblock

**Spec:** `docs/superpowers/specs/2026-04-28-spmd-width-typed-design.md`

**Reverted prior attempts (informational only):**
- v1: lift guard alone (commits reverted; latent scatter bug)
- v2: lift guard + alloca routing (alloca routing didn't help slice writes)
- v3: per-alloca side channel (23 broken tests at GATE)
- v4: per-block side channel + per-call specialization (20 broken tests at GATE; specialization deferred to v5.1)

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `go/src/go/types/spmd.go` (SPMDType struct ~line 16) | MODIFY | Add `lanes int` field, `NewVaryingWithLanes`, `Lanes()` accessor |
| `go/src/cmd/compile/internal/types2/spmd.go` | MODIFY | Mirror of go/types changes |
| `go/src/go/types/predicates_ext_spmd.go` | MODIFY | `Identical` compares `lanes` |
| `go/src/cmd/compile/internal/types2/predicates_ext_spmd.go` | MODIFY | Same |
| `go/src/go/types/typestring_ext_spmd.go` | MODIFY | Print `_N` suffix when lanes>0 |
| `go/src/cmd/compile/internal/types2/typestring_ext_spmd.go` | MODIFY | Same |
| `go/src/go/types/spmd_test.go` | NEW or extend | Three tests: Lanes accessor, Identical with lanes, String with lanes |
| `x-tools-spmd/go/ssa/ssa.go` | MODIFY | Add package-private `setSPMDValueType` bridge |
| `x-tools-spmd/go/ssa/lift.go` | MODIFY | Restore lift guard + `isLanesVaryingType` helper |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | MODIFY | Predication pass mutates each in-loop Varying value's type |
| `x-tools-spmd/go/ssa/spmd_propagate.go` | NEW | Forward-propagation: annotate entry-block alloca types from consumer widths |
| `x-tools-spmd/go/ssa/func.go` | MODIFY | Wire `spmdPropagateAllocaWidth` after `spmdConvertLoopOps` |
| `x-tools-spmd/go/ssa/export_spmd_test.go` | NEW | Test re-export of `isLanesVaryingType` |
| `x-tools-spmd/go/ssa/spmd_lift_test.go` | NEW | `TestSPMDV5VaryingAllocaNotLifted` |
| `x-tools-spmd/go/ssa/spmd_v5_typed_test.go` | NEW | `TestSPMDV5TypeRewriting` + `TestSPMDV5ForwardPropagationEntryBlock` |
| `tinygo/compiler/compiler.go` (`getLLVMType` SPMDType case ~line 548) | MODIFY | 5-line change reading `typ.Lanes()` |
| `tinygo/compiler/compiler_test.go` (`testCompilePackage` ~line 235) | MODIFY | `MaxStackAlloc` propagation (carryover from v3/v4) |
| `tinygo/compiler/spmd_test.go` | MODIFY | Three IR tests: alloca, IndexAddr, reduce.Any |
| `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (cond.) | — |
| `tinybench/BLOCKERS.md` | MODIFY/DELETE | Remove entries; delete file if empty |

Parent SPMD repo: submodule pointer bumps for `go`, `x-tools-spmd`, `tinygo`, `tinybench`.

---

## Task 0: Pre-flight + baselines

**Files:** None (verification + cleanup).

- [ ] **Step 1: Confirm submodule state**

```bash
cd /home/cedric/work/SPMD
for sub in go x-tools-spmd tinygo tinybench; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. `x-tools-spmd` tip: `9f914044e` (v4 specialization deferral) or its revert. `tinygo` tip: `6e40105b` (revert of v4 commit).

- [ ] **Step 2: Discard any leftover staged content**

```bash
cd /home/cedric/work/SPMD/go && git status --short
cd /home/cedric/work/SPMD/x-tools-spmd && git status --short
cd /home/cedric/work/SPMD/tinygo && git status --short
cd /home/cedric/work/SPMD/tinybench && git status --short
```

If any has staged changes: `git restore --staged . && git checkout -- .`. All four submodules should report empty status.

- [ ] **Step 3: Capture E2E baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-baseline-v5.txt 2>&1
tail -10 /tmp/e2e-baseline-v5.txt
```

Expected: `Compile pass: 94, Compile fail: 0, Run pass: 93, Run fail: 0, Reject pass: 11`. Save for the §11 GATE.

- [ ] **Step 4: Capture benchmark baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-baseline-v5.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-baseline-v5.txt | head
```

Expected: benchmark runs cleanly; ratios captured.

- [ ] **Step 5: Confirm n-body-nosqrt NaN baseline**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -3
```

If the build succeeds (or fails with the BLOCKER.md placeholder), check the binary if present:

```bash
[ -x /tmp/nbns-spmd-bin ] && timeout 5 /tmp/nbns-spmd-bin 50000 || echo "(no binary or timed out — NaN baseline confirmed)"
```

Expected: starts with `NaN` OR build fails per BLOCKER. Either confirms the bug exists in baseline.

- [ ] **Step 6: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nb-spmd-bin
```

---

## Task 1: Stdlib type-system — `lanes` field + `NewVaryingWithLanes` + `Lanes()` accessor

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/go/types/spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/spmd.go`

This task adds the storage and constructors. No predicates or string updates yet — those are Tasks 2 and 3.

- [ ] **Step 1: Read the current SPMDType definition**

```bash
cd /home/cedric/work/SPMD
cat go/src/go/types/spmd.go
```

Confirm the struct shape matches:

```go
type SPMDType struct {
    qualifier SPMDQualifier
    elem      Type
}
```

- [ ] **Step 2: Modify go/types/spmd.go**

In `/home/cedric/work/SPMD/go/src/go/types/spmd.go`, replace the SPMDType struct + NewVarying section with:

```go
// SPMDType represents a varying qualified type (lanes.Varying[T]).
//
// The optional `lanes` field is set by the SSA predication pass to a
// non-zero canonical lane count when the value is materialized inside
// an SPMD loop scope (e.g., a Varying[int] inside a `go for` over
// []float64 carries lanes=2 on WASM SIMD128 to match the loop's
// iteration width). The type-checker only ever produces width-free
// instances (lanes=0); width-fixed instances exist only post
// type-checking, mutated in place via the SSA predication pass.
//
// TinyGo's getLLVMType reads Lanes() during Varying[T] materialization
// to emit the LLVM vector at the canonical width. When lanes=0
// (abstract), TinyGo falls back to its existing element-natural /
// function-min derivation.
type SPMDType struct {
    qualifier SPMDQualifier
    elem      Type
    lanes     int // 0 = abstract; > 0 = width-fixed (SSA predication pass)
}

// NewVarying returns a new abstract varying type for the given element
// type. The type-checker uses this constructor; its Lanes() returns 0.
func NewVarying(elem Type) *SPMDType {
    return &SPMDType{qualifier: VaryingQualifier, elem: elem}
}

// NewVaryingWithLanes returns a new width-fixed varying type. Used by
// the SSA predication pass to mutate in-loop Varying values' types to
// carry the surrounding loop's canonical lane count. TinyGo reads
// Lanes() during getLLVMType to materialize the LLVM vector at the
// right width. Type-checker callers should not use this constructor —
// they produce abstract types via NewVarying.
func NewVaryingWithLanes(elem Type, lanes int) *SPMDType {
    return &SPMDType{qualifier: VaryingQualifier, elem: elem, lanes: lanes}
}
```

Then add the `Lanes()` accessor immediately after the `Elem()` method (around line 36):

```go
// Lanes returns the canonical lane count of a width-fixed SPMD type.
// Returns 0 for abstract (type-checker-produced) instances. Set by the
// SSA predication pass via NewVaryingWithLanes; read by TinyGo's
// getLLVMType to materialize the LLVM vector at the right width.
func (s *SPMDType) Lanes() int { return s.lanes }
```

- [ ] **Step 3: Mirror to types2**

Make the SAME changes in `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/spmd.go`. The struct shape and helper signatures are identical between go/types and types2 — copy the same content verbatim.

- [ ] **Step 4: Build the Go toolchain**

```bash
cd /home/cedric/work/SPMD
make build-go 2>&1 | tail -10
```

Expected: clean build. If the build fails because `Lanes()` collides with anything (it shouldn't), inspect and adjust.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/spmd.go src/cmd/compile/internal/types2/spmd.go
```

Do NOT commit — clean-commit batches Tasks 1+2+3+4 (stdlib changes) into one Go submodule commit.

---

## Task 2: Stdlib `Identical` — compare `lanes`

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/go/types/predicates_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/predicates_ext_spmd.go`

- [ ] **Step 1: Read the current handleSPMDTypeIdentical**

```bash
cat /home/cedric/work/SPMD/go/src/go/types/predicates_ext_spmd.go
```

Confirm the function shape (return: `(handled, identical bool)`).

- [ ] **Step 2: Modify go/types/predicates_ext_spmd.go**

Replace the `if spmdY, ok := y.(*SPMDType); ok {` block to also compare lanes:

```go
func (c *comparer) handleSPMDTypeIdentical(x, y Type, p *ifacePair) (handled, identical bool) {
    if !buildcfg.Experiment.SPMD {
        return false, false
    }

    if spmdX, ok := x.(*SPMDType); ok {
        if spmdY, ok := y.(*SPMDType); ok {
            return true, (spmdX.qualifier == spmdY.qualifier &&
                spmdX.lanes == spmdY.lanes &&
                c.identical(spmdX.elem, spmdY.elem, p))
        }
        return true, false
    }
    if _, ok := y.(*SPMDType); ok {
        return true, false
    }
    return false, false
}
```

The only change is the new `spmdX.lanes == spmdY.lanes &&` line.

- [ ] **Step 3: Mirror to types2**

Apply the same edit to `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/predicates_ext_spmd.go`.

- [ ] **Step 4: Build**

```bash
cd /home/cedric/work/SPMD
make build-go 2>&1 | tail -5
```

Expected: clean build.

- [ ] **Step 5: Stage**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/predicates_ext_spmd.go src/cmd/compile/internal/types2/predicates_ext_spmd.go
```

---

## Task 3: Stdlib `String` — print `_N` suffix

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/go/types/typestring_ext_spmd.go`
- Modify: `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/typestring_ext_spmd.go`

- [ ] **Step 1: Modify go/types/typestring_ext_spmd.go**

Replace the function body's varying case to append `_N` when `t.lanes > 0`:

```go
func (w *typeWriter) handleSPMDTypeString(typ Type) bool {
    if !buildcfg.Experiment.SPMD {
        return false
    }

    if t, ok := typ.(*SPMDType); ok {
        switch t.qualifier {
        case UniformQualifier:
            w.typ(t.elem)
        case VaryingQualifier:
            w.string("lanes.Varying[")
            w.typ(t.elem)
            w.byte(']')
            if t.lanes > 0 {
                w.byte('_')
                w.string(strconv.Itoa(t.lanes))
            }
        }
        return true
    }
    return false
}
```

The only changes are the four `if t.lanes > 0` lines.

You'll need to add `"strconv"` to the imports if it's not present:

```go
import (
    "internal/buildcfg"
    "strconv"
)
```

- [ ] **Step 2: Mirror to types2**

Apply the same edits to `/home/cedric/work/SPMD/go/src/cmd/compile/internal/types2/typestring_ext_spmd.go`.

- [ ] **Step 3: Build**

```bash
cd /home/cedric/work/SPMD
make build-go 2>&1 | tail -5
```

Expected: clean build.

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/typestring_ext_spmd.go src/cmd/compile/internal/types2/typestring_ext_spmd.go
```

---

## Task 4: Stdlib type-system tests

**Files:**
- Create or extend: `/home/cedric/work/SPMD/go/src/go/types/spmd_test.go`

Three tests verifying the new field works correctly.

- [ ] **Step 1: Check whether spmd_test.go exists**

```bash
ls /home/cedric/work/SPMD/go/src/go/types/spmd_test.go 2>&1
```

If it exists, append the new tests; otherwise create it.

- [ ] **Step 2: Write the test file (or append)**

If creating:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types_test

import (
    "go/types"
    "testing"
)

// TestSPMDTypeLanesField verifies the new Lanes() accessor returns the
// width-fixed value when set, 0 for abstract types.
func TestSPMDTypeLanesField(t *testing.T) {
    abstract := types.NewVarying(types.Typ[types.Int])
    if abstract.Lanes() != 0 {
        t.Fatalf("abstract Varying[int]: Lanes() = %d, want 0", abstract.Lanes())
    }
    fixed := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    if fixed.Lanes() != 4 {
        t.Fatalf("fixed Varying[int]_4: Lanes() = %d, want 4", fixed.Lanes())
    }
}

// TestSPMDTypeIdenticalLanes verifies type identity respects the lane count.
// Two width-fixed types with the same lane count are identical; with
// different lane counts (or one abstract / one fixed) they are not.
func TestSPMDTypeIdenticalLanes(t *testing.T) {
    a := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    b := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    c := types.NewVaryingWithLanes(types.Typ[types.Int], 8)
    abstract := types.NewVarying(types.Typ[types.Int])

    if !types.Identical(a, b) {
        t.Error("Identical(Varying[int]_4, Varying[int]_4) = false; want true")
    }
    if types.Identical(a, c) {
        t.Error("Identical(Varying[int]_4, Varying[int]_8) = true; want false")
    }
    if types.Identical(a, abstract) {
        t.Error("Identical(Varying[int]_4, Varying[int]_0) = true; want false (strict)")
    }
}

// TestSPMDTypeStringLanes verifies the printed format includes the width.
func TestSPMDTypeStringLanes(t *testing.T) {
    abstract := types.NewVarying(types.Typ[types.Int])
    if got := abstract.String(); got != "lanes.Varying[int]" {
        t.Errorf("abstract String() = %q; want %q", got, "lanes.Varying[int]")
    }
    fixed := types.NewVaryingWithLanes(types.Typ[types.Int], 4)
    if got := fixed.String(); got != "lanes.Varying[int]_4" {
        t.Errorf("fixed String() = %q; want %q", got, "lanes.Varying[int]_4")
    }
}
```

If extending an existing file, append the three test functions and any missing imports.

- [ ] **Step 3: Run the tests**

```bash
cd /home/cedric/work/SPMD/go
GOEXPERIMENT=spmd ./bin/go test ./src/go/types -run 'TestSPMDTypeLanesField|TestSPMDTypeIdenticalLanes|TestSPMDTypeStringLanes' -v -count=1 2>&1 | tail -15
```

Expected: 3 PASSes.

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/go
git add src/go/types/spmd_test.go
```

- [ ] **Step 5: Commit Tasks 1-4 (clean-commit pipeline)**

This is the Go-stdlib batch. Suggested message:

```
feat: add lane-count field to *types.SPMDType

Adds an optional `lanes int` field to *types.SPMDType (in both go/types
and cmd/compile/internal/types2). The type-checker continues to produce
only abstract instances (lanes=0); width-fixed instances are produced
post-type-checking by the SSA predication pass via NewVaryingWithLanes.
TinyGo's getLLVMType reads Lanes() during Varying[T] materialization to
emit the LLVM vector at the canonical width.

Updates Identical to compare lanes (strict; Varying[int]_4 and
Varying[int]_8 are NOT identical). Updates String to print the suffix
`_N` when lanes > 0 (helpful for SSA dumps and debug output). All
gating via buildcfg.Experiment.SPMD; stock Go builds unaffected.

Tests: TestSPMDTypeLanesField, TestSPMDTypeIdenticalLanes,
TestSPMDTypeStringLanes.
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

---

## Task 5: SSA — `setSPMDValueType` package-private bridge + lift guard restoration

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/export_spmd_test.go`

This task does TDD red-then-green for the lift guard, plus adds the bridge for the predication pass (Task 6 uses it).

- [ ] **Step 1: Add the setSPMDValueType bridge**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/ssa.go`, locate the `setType` method on `*register` (around line 1890):

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
grep -n "func (v \*register) setType" go/ssa/ssa.go
```

Add this helper IMMEDIATELY after the existing `setType` method:

```go
// setSPMDValueType mutates v's underlying type via the register mix-in's
// setType. Used ONLY by the SPMD predication pass to rewrite a Varying
// value's type from the type-checker's abstract Varying[T] (lanes=0) to
// a width-fixed Varying[T]_N (lanes=N) where N is the surrounding SPMD
// loop's canonical lane count.
//
// TinyGo reads the new type via getLLVMType to materialize the LLVM
// vector at the correct width. This avoids the need for side-channel
// annotations and downstream "consult the annotation" patches at every
// TinyGo materialization site.
//
// Returns true if v embedded *register and the type was set; false
// otherwise. Not part of the public SSA API.
func setSPMDValueType(v Value, t types.Type) bool {
    type setter interface {
        setType(types.Type)
    }
    if s, ok := v.(setter); ok {
        s.setType(t)
        return true
    }
    return false
}
```

This works because `setType` is the same package-private method on `*register`, and every value-producing instruction embeds `*register`.

- [ ] **Step 2: Create the failing lift test (TDD RED)**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_lift_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa_test

import (
    "go/types"
    "testing"

    "golang.org/x/tools/go/ssa"
)

// accumulateSrc is the shared SPMD-loop fixture: a function with no
// varying parameters that contains an SPMD loop using a Varying[int]
// accumulator. buildSSAWithSPMD (defined in spmd_loop_test.go) marks
// the first RangeStmt as IsSpmd=true so fn.SPMDLoops is non-empty and
// spmdConvertLoopOps runs.
const accumulateSrc = `package main

import "lanes"

func accumulate(data []int) int {
    var acc lanes.Varying[int]
    for i := range len(data) {
        _ = i
        _ = acc
    }
    return 0
}

func main() {}
`

// TestSPMDV5VaryingAllocaNotLifted verifies that a Varying[T] alloca
// survives the SSA lift() pass — its Alloc instruction remains in the
// function body so the SPMD predication pass can mutate the alloca's
// pointee type to a width-fixed *types.SPMDType.
func TestSPMDV5VaryingAllocaNotLifted(t *testing.T) {
    pkg := buildSSAWithSPMD(t, accumulateSrc)
    fn := pkg.Func("accumulate")
    if fn == nil {
        t.Fatal("accumulate function not found in SSA")
    }

    var gotAlloc bool
    for _, bb := range fn.Blocks {
        for _, instr := range bb.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if ptr, ok := alloc.Type().(*types.Pointer); ok {
                    if ssa.IsLanesVaryingType(ptr.Elem()) {
                        gotAlloc = true
                    }
                }
            }
        }
    }
    if !gotAlloc {
        t.Fatal("varying alloca was lifted; expected memory-backed *ssa.Alloc with Varying[T] element in function body")
    }
}
```

- [ ] **Step 3: Create export_spmd_test.go**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/export_spmd_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

// Test-only re-exports.

// IsLanesVaryingType exposes the file-private isLanesVaryingType helper
// to the external ssa_test package. Production callers use the
// lowercase symbol within package ssa.
var IsLanesVaryingType = isLanesVaryingType
```

- [ ] **Step 4: Run test — expect FAIL (compile error)**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5VaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: compile error mentioning `isLanesVaryingType undefined` (the helper isn't in `lift.go` yet). RED state.

- [ ] **Step 5: Add the helper + lift guard (GREEN)**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/lift.go`, add `"go/types"` to the import block (alphabetical, between `"go/token"` and `"math/big"`):

```go
import (
    "fmt"
    "go/token"
    "go/types"
    "math/big"
    // ... rest unchanged ...
)
```

Add `isLanesVaryingType` helper just above `liftAlloc` (around line 395):

```go
// isLanesVaryingType reports whether t is a lanes.Varying[T] type. Matches
// both *types.SPMDType (the production representation when GOEXPERIMENT=spmd
// is active and the forked type-checker intercepts lanes.Varying[T]) and the
// raw *types.Named instantiation produced when the standard importer reads
// lanes.Varying[T] without GOEXPERIMENT.
func isLanesVaryingType(typ types.Type) bool {
    if _, ok := typ.(*types.SPMDType); ok {
        return true
    }
    if named, ok := typ.(*types.Named); ok {
        obj := named.Obj()
        if obj.Name() == "Varying" && obj.Pkg() != nil && obj.Pkg().Path() == "lanes" {
            return true
        }
    }
    return false
}
```

Add the guard at the very top of `liftAlloc`'s body (BEFORE the existing `Recover` check):

```go
func liftAlloc(df domFrontier, alloc *Alloc, newPhis newPhiMap, fresh *int) bool {
    // SPMD: keep varying allocas memory-backed so the SPMD predication
    // pass can mutate the alloca's pointee type to a width-fixed
    // *types.SPMDType. Without this, the alloca becomes a phi and the
    // phi's type would be mutated instead — semantically fine but breaks
    // the alloca-based forward-propagation pass for entry-block allocas
    // in non-SPMD functions.
    if ptr, ok := alloc.Type().(*types.Pointer); ok {
        if isLanesVaryingType(ptr.Elem()) {
            return false
        }
    }

    // Don't lift result values in functions that defer
    // calls that may recover from panic.
    if fn := alloc.Parent(); fn.Recover != nil {
        // ... existing body unchanged ...
```

- [ ] **Step 6: Run test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5VaryingAllocaNotLifted -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDV5VaryingAllocaNotLifted`.

- [ ] **Step 7: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Pre-existing failures are acceptable (the SSA suite has pre-existing crashes from `TestSPMDCloneBlock_TranslateValue`, `TestPeelSPMDLoopSimple`, and assertion failures from `TestSPMDPointerVaryingFieldAccess`, etc.). NO new `--- FAIL` lines vs. running the suite without these changes.

- [ ] **Step 8: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/lift.go go/ssa/spmd_lift_test.go go/ssa/export_spmd_test.go
```

Do NOT commit (clean-commit batches Tasks 5+6+7 into one x-tools-spmd commit).

---

## Task 6: SSA predication pass — type rewriting

**Files:**
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_v5_typed_test.go`

The core v5 change: predication walks each loop's scope and mutates each Varying value's type to width-fixed.

- [ ] **Step 1: Create the failing test (TDD RED)**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_v5_typed_test.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa_test

import (
    "go/types"
    "testing"

    "golang.org/x/tools/go/ssa"
)

// TestSPMDV5TypeRewriting verifies that the SPMD predication pass
// rewrites every Varying value's type in loop scope to width-fixed at
// the loop's canonical lane count. After the pass, a Varying[int]
// value's *types.SPMDType has Lanes() == loop.LaneCount.
func TestSPMDV5TypeRewriting(t *testing.T) {
    pkg := buildSSAWithSPMD(t, accumulateSrc)
    fn := pkg.Func("accumulate")
    if fn == nil {
        t.Fatal("accumulate function not found in SSA")
    }
    if len(fn.SPMDLoops) == 0 {
        t.Fatal("accumulate has no SPMD loops; buildSSAWithSPMD should mark one")
    }
    loop := fn.SPMDLoops[0]
    if loop.LaneCount <= 0 {
        t.Fatalf("loop.LaneCount = %d; expected > 0", loop.LaneCount)
    }

    // Walk every value in the function. For each *Alloc whose pointer
    // points to a Varying[T], its pointee type should now be width-fixed
    // (Lanes() == loop.LaneCount).
    var gotFixed int
    for _, bb := range fn.Blocks {
        for _, instr := range bb.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if ptr, ok := alloc.Type().(*types.Pointer); ok {
                    if elem, ok := ptr.Elem().(*types.SPMDType); ok {
                        if elem.Lanes() == loop.LaneCount {
                            gotFixed++
                        }
                    }
                }
            }
        }
    }
    if gotFixed == 0 {
        t.Fatalf("no width-fixed Varying allocas found; predication pass did not mutate types")
    }
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5TypeRewriting -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `--- FAIL: TestSPMDV5TypeRewriting` with `no width-fixed Varying allocas found; predication pass did not mutate types`.

- [ ] **Step 3: Add the type-rewriting walk in spmdConvertLoopOps**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_predicate.go`, locate `spmdConvertLoopOps` (around line 294). Inside the per-loop body, after the `liveScopeBlocks` is computed and the empty-check guard `if len(liveScopeBlocks) == 0 { continue }` (around line 352-354), and BEFORE the `if loop.IsPeeled {` branch (around line 356), insert:

```go
        // SPMD v5: rewrite every Varying value's type in this loop's scope
        // to be width-fixed at loop.LaneCount. TinyGo reads typ.Lanes()
        // during getLLVMType to materialize the LLVM vector at the
        // canonical width — every existing TinyGo materialization site
        // becomes automatically correct because they all flow through
        // getLLVMType(value.Type()) for some value's type.
        //
        // The mutation is in-place via setSPMDValueType on the register
        // mix-in. The `Lanes() == 0` guard preserves an outer loop's
        // annotation when an inner loop's predication revisits the same
        // value (outer-loop-wins semantics).
        for b := range liveScopeBlocks {
            for _, instr := range b.Instrs {
                // Allocas: rewrite the pointer's element type. *Alloc
                // embeds *register; setSPMDValueType works.
                if alloc, ok := instr.(*Alloc); ok {
                    if ptr, ok := alloc.Type().(*types.Pointer); ok {
                        if elem, ok := ptr.Elem().(*types.SPMDType); ok && elem.Lanes() == 0 {
                            fixed := types.NewVaryingWithLanes(elem.Elem(), loop.LaneCount)
                            setSPMDValueType(alloc, types.NewPointer(fixed))
                        }
                    }
                    continue
                }
                // Value-producing instructions: rewrite the result type.
                v, ok := instr.(Value)
                if !ok {
                    continue
                }
                spmdType, ok := v.Type().(*types.SPMDType)
                if !ok || spmdType.Lanes() != 0 {
                    continue
                }
                fixed := types.NewVaryingWithLanes(spmdType.Elem(), loop.LaneCount)
                setSPMDValueType(v, fixed)
            }
        }
```

Indentation must match the surrounding scope (likely two tabs since it's inside `for _, loop := range fn.SPMDLoops`).

- [ ] **Step 4: Run test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5TypeRewriting -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDV5TypeRewriting`.

- [ ] **Step 5: Run all v5 SSA tests so far**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDV5VaryingAllocaNotLifted|TestSPMDV5TypeRewriting' -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: 2 PASSes.

- [ ] **Step 6: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Same pre-existing failures only. NO new failures.

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go go/ssa/spmd_v5_typed_test.go
```

---

## Task 7: SSA forward-propagation pass

**Files:**
- Create: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go`
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_v5_typed_test.go` (append a test)
- Modify: `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go` (wire the call)

For an alloca declared in an entry block (non-SPMD scope) but consumed by SPMD ops in loop scopes, the alloca's pointee type should be width-fixed too. Without this, accumulator-pattern code in `main()` would size the alloca wrong.

- [ ] **Step 1: Append the failing test**

Append to `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_v5_typed_test.go`:

```go
// TestSPMDV5ForwardPropagationEntryBlock verifies that an entry-block
// alloca consumed by SPMD ops in a loop scope has its pointee type
// mutated to width-fixed (matching the consumer's lane count).
//
// The fixture: main() declares `var acc lanes.Varying[int]` in its
// entry block, then uses it inside a SPMD loop. Forward propagation
// should walk the alloca's referrers, find the in-loop value's
// width-fixed type, and propagate that width back to the alloca's
// pointee type.
func TestSPMDV5ForwardPropagationEntryBlock(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []int{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
    var acc lanes.Varying[int]
    for i := range len(data) {
        _ = i
        acc = acc
    }
    _ = reduce.Add(acc)
}
`
    pkg := buildSSAWithSPMD(t, src)
    fn := pkg.Func("main")
    if fn == nil {
        t.Fatal("main function not found in SSA")
    }
    if len(fn.Blocks) == 0 {
        t.Fatal("main has no blocks")
    }

    // Find the acc alloca and check its pointee type carries Lanes().
    var found bool
    var lanes int
    for _, bb := range fn.Blocks {
        for _, instr := range bb.Instrs {
            if alloc, ok := instr.(*ssa.Alloc); ok {
                if ptr, ok := alloc.Type().(*types.Pointer); ok {
                    if elem, ok := ptr.Elem().(*types.SPMDType); ok {
                        found = true
                        lanes = elem.Lanes()
                    }
                }
            }
        }
    }
    if !found {
        t.Fatal("varying alloca not found in main()")
    }
    if lanes == 0 {
        t.Fatal("alloca pointee type has Lanes() == 0; expected forward-propagation to set it from the SPMD consumer")
    }
}
```

- [ ] **Step 2: Run test — expect FAIL**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5ForwardPropagationEntryBlock -v -count=1 -timeout=60s 2>&1 | tail -10
```

Expected: `--- FAIL: TestSPMDV5ForwardPropagationEntryBlock` with `alloca pointee type has Lanes() == 0; ...`.

- [ ] **Step 3: Create spmd_propagate.go**

Create `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/spmd_propagate.go`:

```go
// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
    "go/token"
    "go/types"
)

// spmdPropagateAllocaWidth annotates entry-block (and any other
// non-loop-scope) varying allocas whose consumers are width-fixed values
// inside loop scopes. The consumer's width tells us what width the
// alloca's storage should be.
//
// This handles the common pattern of an accumulator declared in a
// non-SPMD function's entry block but used inside a `go for` loop:
//
//   var acc lanes.Varying[float64]   // entry block, alloca pointee Lanes()=0
//   go for i, x := range data { ... acc += x }   // loop body uses acc
//
// Without this pass, TinyGo would size `acc`'s alloca via the
// element-natural derivation. Forward propagation sets the alloca's
// pointee type to width-fixed so TinyGo materializes it correctly.
//
// Limitation: an entry block with two allocas of different consumer
// lane counts gets first-consumer-wins per alloca. Documented as a
// known limitation.
//
// Runs AFTER spmdConvertLoopOps (so consumer values have width-fixed
// types).
func spmdPropagateAllocaWidth(fn *Function) {
    for _, b := range fn.Blocks {
        for _, instr := range b.Instrs {
            alloc, ok := instr.(*Alloc)
            if !ok {
                continue
            }
            ptr, ok := alloc.Type().(*types.Pointer)
            if !ok {
                continue
            }
            elem, ok := ptr.Elem().(*types.SPMDType)
            if !ok || elem.Lanes() != 0 {
                continue // not a varying alloca, or already width-fixed
            }
            lc := spmdAllocaConsumerWidth(alloc)
            if lc > 0 {
                fixed := types.NewVaryingWithLanes(elem.Elem(), lc)
                setSPMDValueType(alloc, types.NewPointer(fixed))
            }
        }
    }
}

// spmdAllocaConsumerWidth walks the alloca's referrer chain and returns
// the lane count of the first SPMD consumer found whose value type is
// width-fixed.
//
// Production consumers (SPMDLoad/Store/Index/Select) are produced by
// spmdConvertLoopOps; their result/value types inherit width from the
// predication pass. Test-environment consumers (plain Store/UnOp{MUL})
// in annotated blocks are also accepted because the loop's value-side
// arithmetic produces width-fixed operands.
//
// Returns 0 if no width-fixed consumer is found.
func spmdAllocaConsumerWidth(alloc *Alloc) int {
    refs := alloc.Referrers()
    if refs == nil {
        return 0
    }
    for _, ref := range *refs {
        switch op := ref.(type) {
        case *SPMDLoad:
            if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *SPMDStore:
            if t, ok := op.Val.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *SPMDIndex:
            if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *SPMDSelect:
            if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *Store:
            if t, ok := op.Val.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                return t.Lanes()
            }
        case *UnOp:
            if op.Op == token.MUL && op.X == alloc {
                if t, ok := op.Type().(*types.SPMDType); ok && t.Lanes() > 0 {
                    return t.Lanes()
                }
            }
        }
    }
    return 0
}
```

- [ ] **Step 4: Wire spmdPropagateAllocaWidth into func.go**

In `/home/cedric/work/SPMD/x-tools-spmd/go/ssa/func.go`, locate the `spmdConvertLoopOps(f)` call in `finishBody` (around line 431):

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
grep -n "spmdConvertLoopOps(f)" go/ssa/func.go
```

Add the propagation call IMMEDIATELY after:

```go
        spmdConvertLoopOps(f)
        // SPMD v5: propagate loop-scope value widths back to entry-block
        // varying allocas whose consumers are inside loop scopes. Runs
        // AFTER spmdConvertLoopOps (so consumer values have width-fixed
        // types from the predication pass).
        spmdPropagateAllocaWidth(f)
```

Indentation matches surrounding (two tabs inside the if-block).

- [ ] **Step 5: Run test — expect PASS**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run TestSPMDV5ForwardPropagationEntryBlock -v -count=1 -timeout=60s 2>&1 | tail -5
```

Expected: `--- PASS: TestSPMDV5ForwardPropagationEntryBlock`.

- [ ] **Step 6: Run all v5 SSA tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -run 'TestSPMDV5' -v -count=1 -timeout=60s 2>&1 | tail -15
```

Expected: 3 PASSes.

- [ ] **Step 7: Run full SSA suite — expect no NEW failures**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
../go/bin/go test ./go/ssa -count=1 -timeout=300s 2>&1 | grep -E '^--- FAIL' | sort
```

Same pre-existing failures only.

- [ ] **Step 8: Stage**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_propagate.go go/ssa/spmd_v5_typed_test.go go/ssa/func.go
```

- [ ] **Step 9: Verify the full x-tools-spmd batch is staged**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git status --short
```

Expected (Tasks 5-7 batch):
- `M  go/ssa/ssa.go` (Task 5)
- `M  go/ssa/lift.go` (Task 5)
- `A  go/ssa/spmd_lift_test.go` (Task 5)
- `A  go/ssa/export_spmd_test.go` (Task 5)
- `M  go/ssa/spmd_predicate.go` (Task 6)
- `A  go/ssa/spmd_v5_typed_test.go` (Tasks 6+7)
- `A  go/ssa/spmd_propagate.go` (Task 7)
- `M  go/ssa/func.go` (Task 7)

If any are missing or unstaged: `git add -u` to re-stage.

- [ ] **Step 10: Commit Tasks 5-7 (clean-commit pipeline)**

This is the SSA-side batch. Suggested message:

```
feat: SPMD v5 — encode lane count in *types.SPMDType, predicate types

Adds the SSA-side machinery for the v5 width-typed Varying design:

- setSPMDValueType bridge in ssa.go: package-private helper that calls
  the existing register.setType via interface assertion, used only by
  the SPMD predication and propagation passes.

- Lift guard in lift.go: restored from v3/v4 — varying allocas survive
  lift() so the predication pass can mutate their pointee types.
  Adds isLanesVaryingType helper (handles both *types.SPMDType
  production representation and *types.Named test-mode representation)
  and IsLanesVaryingType test re-export.

- Predication pass type rewriting in spmd_predicate.go: walks each
  loop's live scope blocks and mutates every Varying value's type
  in-place to *types.SPMDType{lanes: loop.LaneCount}. TinyGo's
  getLLVMType reads the new type's Lanes() to materialize the LLVM
  vector at the right width — no side-channel annotations needed.

- Forward-propagation pass in spmd_propagate.go (NEW): walks
  unannotated blocks for varying allocas whose consumers are
  width-fixed values; propagates the consumer's width back onto the
  alloca's pointee type. Handles the common entry-block accumulator
  pattern (var acc lanes.Varying[float64] in main() above a go for).

Tests: TestSPMDV5VaryingAllocaNotLifted, TestSPMDV5TypeRewriting,
TestSPMDV5ForwardPropagationEntryBlock.
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

---

## Task 8: TinyGo `getLLVMType` for SPMDType — read `Lanes()`

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler.go` (`getLLVMType` SPMDType case ~line 548)
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/compiler_test.go` (`testCompilePackage` ~line 235)

The single TinyGo change for v5. ALL ~21 audit sites become automatically correct because they all flow through `getLLVMType`.

- [ ] **Step 1: Rebuild TinyGo with the new x-tools-spmd**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
```

Expected: clean build. Confirms x-tools-spmd Tasks 5-7 are in place and TinyGo can compile against the new `Lanes()` accessor.

- [ ] **Step 2: Find the SPMDType case in getLLVMType**

```bash
cd /home/cedric/work/SPMD/tinygo
grep -n "case \*types.SPMDType:" compiler/compiler.go
```

Locate the case. Read 20 lines around it to confirm the current structure.

- [ ] **Step 3: Modify the case**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler.go`, locate the `*types.SPMDType` case in `getLLVMType`. Replace the current code:

```go
case *types.SPMDType:
    elemType := c.getLLVMType(typ.Elem())
    laneCount := c.spmdEffectiveLaneCount(typ, elemType)
    if laneCount <= 1 {
        return elemType
    }
    return llvm.VectorType(elemType, laneCount)
```

With:

```go
case *types.SPMDType:
    elemType := c.getLLVMType(typ.Elem())
    // SPMD v5: prefer the type-encoded lane count when set by the SSA
    // predication or propagation pass. This is the canonical source for
    // values inside SPMD loop scope. Falls back to spmdEffectiveLaneCount
    // for width-free types (function signatures, global vars, abstract
    // types from compilerContext-level queries).
    var laneCount int
    if typ.Lanes() > 0 {
        laneCount = typ.Lanes()
    } else {
        laneCount = c.spmdEffectiveLaneCount(typ, elemType)
    }
    if laneCount <= 1 {
        return elemType // scalar fallback
    }
    return llvm.VectorType(elemType, laneCount)
```

- [ ] **Step 4: Add MaxStackAlloc to test config**

In `/home/cedric/work/SPMD/tinygo/compiler/compiler_test.go`, locate `testCompilePackage` (around line 235). Find the `compilerConfig` literal block where `AutomaticStackSize`, `DefaultStackSize`, `NeedsStackObjects` are set. Add `MaxStackAlloc` after `NeedsStackObjects`:

```go
            AutomaticStackSize: config.AutomaticStackSize(),
            DefaultStackSize:   config.StackSize(),
            NeedsStackObjects:  config.NeedsStackObjects(),
            // MaxStackAlloc is propagated so that alloca vs heap
            // decisions match the real compiler. Without it (0 default),
            // every non-zero alloca goes to the heap, masking alloca
            // instructions from IR pattern checks (e.g.,
            // TestSPMDV5VaryingAllocaLLVMType checks for
            // `alloca <2 x i32>`).
            MaxStackAlloc: config.MaxStackAlloc(),
```

If `MaxStackAlloc` is already in the literal (perhaps from a prior session), skip this step.

- [ ] **Step 5: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
make build-tinygo 2>&1 | tail -3
```

Expected: clean build.

- [ ] **Step 6: Run existing SPMD tests — verify no regressions**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-run 'TestSPMD' -count=1 -timeout=10m" GOTESTPKGS="./compiler" 2>&1 | grep -E '^--- (PASS|FAIL)' | sort | uniq -c | head
```

Expected: existing SPMD tests should still PASS. The new `getLLVMType` branch only fires when `typ.Lanes() > 0`, which only happens for values mutated by Tasks 5-7 — pre-existing TinyGo paths that work with abstract types continue working.

- [ ] **Step 7: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/compiler_test.go
```

Do NOT commit (clean-commit batches Tasks 8+9 into one tinygo commit).

---

## Task 9: TinyGo IR tests + commit Tasks 8+9 batch

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go`

Three IR tests asserting v5's contract: alloca + load width consistency, IndexAddr matching value vector, reduce.Any at correct width.

- [ ] **Step 1: Append three tests**

Append to `/home/cedric/work/SPMD/tinygo/compiler/spmd_test.go` (anywhere after other test functions, before the helper definitions starting around line 737):

```go
// TestSPMDV5VaryingAllocaLLVMType verifies that a Varying[int] alloca
// inside a go-for over []float64 is materialized at the loop's
// iteration width (2 on WASM SIMD128), and ALL load/reduce ops on the
// alloca are also at that width. v5 expects this without any
// TinyGo-side audit because the type itself carries the width.
func TestSPMDV5VaryingAllocaLLVMType(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []float64{1, 2, 3, 4, 5, 6, 7, 8}

func main() {
    var acc lanes.Varying[int]
    go for i, _ := range data {
        acc += i
    }
    _ = reduce.Add(acc)
}
`
    ir := compileSPMDSource(t, src)
    mustContain(t, ir, "alloca <2 x i32>")
    mustContain(t, ir, "load <2 x i32>, ptr %acc")
    mustContain(t, ir, "reduce.add.v2i32")
    mustNotContain(t, ir, "load <4 x i32>, ptr %acc")
    mustNotContain(t, ir, "reduce.add.v4i32")
}

// TestSPMDV5VaryingByteIndexAddr verifies that b[i] = c in a []byte
// loop produces matching <N x ptr> address and <N x i8> value vectors,
// where N is the loop's lane count (16 on WASM128). This is the
// to-upper bug — v3/v4 broke this because the IndexAddr address vector
// was sized at int's natural width, not the loop width.
func TestSPMDV5VaryingByteIndexAddr(t *testing.T) {
    src := `package main

import "lanes"

var src = []byte("hello world")

func main() {
    b := make([]byte, len(src))
    go for i, c := range src {
        if 'a' <= c && c <= 'z' {
            b[i] = c - 32
        } else {
            b[i] = c
        }
    }
    _ = b
}
`
    ir := compileSPMDSource(t, src)
    // On WASM128, byte iter = 16 lanes. Address and value vectors must
    // agree at <16 x ptr> / <16 x i8>. The loose mustContainAny lets
    // the test work whether the codegen emits scatter, shuffle-store,
    // or contiguous masked store — what matters is the absence of an
    // int-natural-width leak.
    mustNotContain(t, ir, "<4 x ptr>")
    mustNotContain(t, ir, "scatter.v16i8.v4p0")
}

// TestSPMDV5LoContainsReduceAny verifies that reduce.Any on a varying
// comparison result produces the right-width reduce intrinsic.
// lo-contains failed in v3/v4 because reduce.Any read element-natural
// width.
func TestSPMDV5LoContainsReduceAny(t *testing.T) {
    src := `package main

import (
    "lanes"
    "reduce"
)

var data = []int32{1, 2, 3, 4, 5, 6, 7, 8}

func find(target int32) bool {
    var found lanes.Varying[bool]
    go for _, x := range data {
        if x == target {
            found = true
        }
    }
    return reduce.Any(found)
}

func main() {
    _ = find(5)
}
`
    ir := compileSPMDSource(t, src)
    // On WASM128, int32 iter = 4 lanes. reduce.Any over <4 x i1> mask
    // should produce v4 intrinsics (LLVM reduce.or.v4i1 or WASM
    // v128.any_true).
    mustContainAny(t, ir, "reduce.or.v4i1", "v128.any_true")
}
```

- [ ] **Step 2: Run the three new tests**

```bash
cd /home/cedric/work/SPMD/tinygo
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd make test GOTESTFLAGS="-run 'TestSPMDV5VaryingAllocaLLVMType|TestSPMDV5VaryingByteIndexAddr|TestSPMDV5LoContainsReduceAny' -v -count=1" GOTESTPKGS="./compiler" 2>&1 | tail -25
```

Expected: 3 PASSes.

If any FAIL with assertion mismatches:
- Inspect the IR: temporarily add a debug test that does `t.Log(ir)` for the failing fixture and run it to see the actual IR fragment for the asserted pattern.
- Adjust the assertion's pattern to match what the IR actually emits (e.g., variant naming, scatter vs shuffle path), but keep the v5 contract: NO `<4 x ptr>` or `<4 x i32>` for in-loop values that should be at the loop's lane count.
- Remove the debug test before staging.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd_test.go
```

- [ ] **Step 4: Verify the tinygo batch state**

```bash
cd /home/cedric/work/SPMD/tinygo
git status --short
```

Expected (Tasks 8+9 batch):
- `M  compiler/compiler.go` (Task 8)
- `M  compiler/compiler_test.go` (Task 8)
- `M  compiler/spmd_test.go` (Task 9)

If anything is missing or unstaged: `git add -u`.

- [ ] **Step 5: Commit Tasks 8+9 (clean-commit pipeline)**

Suggested message:

```
feat: SPMD v5 — read lane count from *types.SPMDType in getLLVMType

Single 5-line change to compiler/compiler.go's *types.SPMDType case in
getLLVMType: prefer typ.Lanes() when set by the SSA predication /
propagation pass; fall back to existing element-natural derivation
otherwise.

ALL prior-audit-required sites become automatically correct because
they all flow through getLLVMType(value.Type()) for some value, and
value types now carry the right width. No side-channel consultation,
no per-site overrides, no v3/v4-style cascade risk.

Test infrastructure: propagates MaxStackAlloc from real config to
test compile config so SPMD allocas stay on stack and are observable
by IR pattern checks.

Tests:
- TestSPMDV5VaryingAllocaLLVMType (alloca + load + reduce all at
  <2 x i32> for Varying[int] in []float64 loop)
- TestSPMDV5VaryingByteIndexAddr (no <4 x ptr> leak in []byte loop —
  the to-upper failure mode that broke v3/v4)
- TestSPMDV5LoContainsReduceAny (reduce.Any at the loop's width, not
  element-natural)
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

---

## Task 10: Regression sweep — GATE

**Files:** None (verification only).

The explicit go/no-go gate. v1, v2, v3, v4 all regressed tests at this point. v5's design specifically removes the side-channel cascade pattern; this gate confirms.

- [ ] **Step 1: Rebuild full toolchain**

```bash
cd /home/cedric/work/SPMD
make build 2>&1 | tail -5
```

Expected: clean build.

- [ ] **Step 2: Run E2E sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after-v5.txt 2>&1
tail -10 /tmp/e2e-after-v5.txt
```

- [ ] **Step 3: Compare against baseline**

```bash
diff <(grep -E "Compile pass|Compile fail|Run pass|Run fail|Reject pass" /tmp/e2e-baseline-v5.txt) \
     <(grep -E "Compile pass|Compile fail|Run pass|Run fail|Reject pass" /tmp/e2e-after-v5.txt)
```

Expected: identical totals (105/94/93/11). Any new fail → STOP.

```bash
diff <(grep -E "(COMPILE FAIL|RUN FAIL|DUAL FAIL)" /tmp/e2e-baseline-v5.txt | sort) \
     <(grep -E "(COMPILE FAIL|RUN FAIL|DUAL FAIL)" /tmp/e2e-after-v5.txt | sort)
```

Expected: empty diff (no new failures).

**GATE:**
- If empty diff → proceed to Task 11.
- If ANY new FAIL → STOP. Do NOT proceed. Investigate before continuing. The hypothesis is wrong; revert the v5 batch and iterate on the design.

- [ ] **Step 4: Run benchmark sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after-v5.txt 2>&1
grep -E "lo-(sum|mean|min|max|clamp|contains)\s+[0-9]" /tmp/bench-after-v5.txt | head
```

Compare against `/tmp/bench-baseline-v5.txt`. Acceptance: each ratio within ±10% of baseline.

If a benchmark regresses by >10%: note it; don't revert (correctness > performance for this fix). Document for follow-up.

---

## Task 11: Re-enable n-body-nosqrt and n-body

**Files:**
- Modify (or revert): `tinybench/n-body-nosqrt/go-spmd/main.go`, `tinybench/n-body/go-spmd/main.go` if their bodies were disabled
- Delete: `tinybench/n-body-nosqrt/go-spmd/BLOCKER.md`, `tinybench/n-body/go-spmd/BLOCKER.md`
- Modify: `tinybench/BLOCKERS.md`

- [ ] **Step 1: Restore n-body-nosqrt source**

```bash
cd /home/cedric/work/SPMD/tinybench
ls n-body-nosqrt/go-spmd/
cat n-body-nosqrt/go-spmd/BLOCKER.md 2>/dev/null | head -5
```

If `main.go` is disabled (panic placeholder, empty body, etc.), restore from git history:

```bash
git log --oneline -- n-body-nosqrt/go-spmd/main.go | head -10
# Find the last revision with the working SPMD body, then:
# git checkout <revision> -- n-body-nosqrt/go-spmd/main.go
```

- [ ] **Step 2: Build + diff vs scalar**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd-bin n-body-nosqrt/go-spmd/main.go 2>&1 | tail -3
go build -o /tmp/nbns-go-bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go-bin 50000) <(/tmp/nbns-spmd-bin 50000)
```

Expected: empty diff. Reference output `-0.169075164` then `-0.169078071`.

- [ ] **Step 3: Restore n-body source**

Repeat Step 1 for `n-body/go-spmd/main.go`.

- [ ] **Step 4: Build + diff n-body**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go 2>&1 | tail -3
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff.

- [ ] **Step 5: Delete BLOCKER files + update BLOCKERS.md**

```bash
cd /home/cedric/work/SPMD/tinybench
rm -f n-body-nosqrt/go-spmd/BLOCKER.md n-body/go-spmd/BLOCKER.md
```

Edit `tinybench/BLOCKERS.md` to remove both `n-body` and `n-body-nosqrt` entries. If the file becomes empty, replace with:

```
# Tinybench SPMD ports — current blockers

None as of 2026-04-28.
```

OR `git rm` the file if project convention is to omit when empty.

- [ ] **Step 6: Stage + commit (clean-commit pipeline)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body-nosqrt/go-spmd/main.go n-body/go-spmd/main.go BLOCKERS.md
git rm --quiet n-body-nosqrt/go-spmd/BLOCKER.md n-body/go-spmd/BLOCKER.md 2>/dev/null || true
```

Suggested commit:

```
feat: re-enable n-body and n-body-nosqrt SPMD ports (v5)

Both ports were blocked on the varying-local NaN-from-unmasked-writeback
bug. v5's width-typed Varying fix (lane count encoded in
*types.SPMDType, predication pass mutates value types in loop scope,
TinyGo getLLVMType reads typ.Lanes()) makes the Varying[float64]
accumulator alloca size correctly and the cascade of in-loop value
materializations align with the loop's lane count. No more inactive-lane
NaN leak.

Output now byte-identical to the scalar Go reference:
  n-body-nosqrt/50000: -0.169075164 / -0.169078071
  n-body/50000:        (matching scalar reference)

Closes the four-attempt cascade (v1, v2, v3, v4 all reverted at the
GATE) by removing the side-channel-annotation pattern that required
21+ TinyGo-side patches.
```

Pipeline: `golang-pro` → `code-reviewer` → `clean-commit`.

- [ ] **Step 7: Cleanup**

```bash
rm -f /tmp/nbns-spmd-bin /tmp/nbns-go-bin /tmp/nb-spmd-bin /tmp/nb-go-bin
```

---

## Task 12: Parent SPMD submodule pointer bumps + final verification

**Files:**
- Modify: parent SPMD repo's submodule pointers for `go`, `x-tools-spmd`, `tinygo`, `tinybench`

- [ ] **Step 1: Verify each submodule has the expected commits**

```bash
cd /home/cedric/work/SPMD
for sub in go x-tools-spmd tinygo tinybench; do
    echo "=== $sub ==="
    (cd $sub && git log --oneline -3)
done
```

Expected: each submodule's tip is the commit from Tasks 4 / 7 / 9 / 11.

- [ ] **Step 2: Stage submodule pointer updates**

```bash
cd /home/cedric/work/SPMD
git add go x-tools-spmd tinygo tinybench
git status --short
```

Expected: 4 modified submodule pointers.

- [ ] **Step 3: Final E2E verification at parent repo**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-final-v5.txt 2>&1
tail -10 /tmp/e2e-final-v5.txt
```

Expected: same totals as `/tmp/e2e-after-v5.txt` (no regression after submodule pointer bump). n-body / n-body-nosqrt no longer in any failure category.

- [ ] **Step 4: Commit (clean-commit pipeline)**

Suggested commit:

```
deps: land SPMD width-typed Varying v5 across toolchain

go: add lanes int field to *types.SPMDType (in both go/types and
cmd/compile/internal/types2); update Identical and String to include
lanes.

x-tools-spmd: introduce setSPMDValueType bridge; restore lift guard;
SPMD predication pass mutates each in-loop Varying value's type to
width-fixed; new spmd_propagate.go forward-propagates entry-block
alloca pointee types from their consumer widths.

tinygo: single 5-line change to *types.SPMDType case in getLLVMType
to read typ.Lanes() when set. ALL ~21 audit sites become
automatically correct because they all flow through
getLLVMType(value.Type()).

tinybench: re-enable n-body and n-body-nosqrt SPMD ports.

Spec: docs/superpowers/specs/2026-04-28-spmd-width-typed-design.md
Plan: docs/superpowers/plans/2026-04-28-spmd-width-typed-v5.md

Supersedes the v1, v2, v3, v4 attempts (all reverted at the regression
GATE due to side-channel-annotation cascade pattern).
```

- [ ] **Step 5: Cleanup**

```bash
rm -f /tmp/e2e-baseline-v5.txt /tmp/e2e-after-v5.txt /tmp/e2e-final-v5.txt \
      /tmp/bench-baseline-v5.txt /tmp/bench-after-v5.txt
```

---

## Done

All success criteria from spec §5.4 are met when:

- Stdlib type tests pass (`TestSPMDTypeLanesField`, `TestSPMDTypeIdenticalLanes`, `TestSPMDTypeStringLanes`).
- SSA tests pass (`TestSPMDV5VaryingAllocaNotLifted`, `TestSPMDV5TypeRewriting`, `TestSPMDV5ForwardPropagationEntryBlock`).
- TinyGo IR tests pass (`TestSPMDV5VaryingAllocaLLVMType`, `TestSPMDV5VaryingByteIndexAddr`, `TestSPMDV5LoContainsReduceAny`).
- `test/e2e/spmd-e2e-test.sh`: zero new failures vs baseline (105/94/93/11).
- `test/e2e/spmd-benchmark-x86.sh`: all ratios within ±10% of baseline.
- `tinybench/n-body-nosqrt/go-spmd/main.go` and `tinybench/n-body/go-spmd/main.go`: byte-identical output to scalar reference.
- All BLOCKER.md files deleted; BLOCKERS.md updated/deleted.
- Parent SPMD submodule pointers bumped to new revisions.

Update `MEMORY.md` per CLAUDE.md auto-memory: record the v5 fix (lane count encoded in `*types.SPMDType` + SSA predication mutates types + TinyGo's single `getLLVMType` read).
