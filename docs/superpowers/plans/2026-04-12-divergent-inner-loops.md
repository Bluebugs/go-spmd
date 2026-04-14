# Divergent Inner Loops N>1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable N>1 SIMD lanes for `go for` over slice-of-slices by using the inner element type for lane count and including varying-bound inner loops in the SPMD scope.

**Architecture:** Three changes: (1) type checkers peel slice wrapper for lane count, (2) SSA predication includes inner loops with varying bounds, (3) TinyGo computes lane count from inner element type. The existing varying-If predication handles per-lane mask narrowing in the inner loop.

**Tech Stack:** Go (go/types, types2), go/ssa (x-tools-spmd), TinyGo compiler

**Spec:** `docs/superpowers/specs/2026-04-12-divergent-inner-loops-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### Go fork (`go/`)
- **Modify:** `src/cmd/compile/internal/types2/check_ext_spmd.go:160-188` — peel slice in getTypeSize
- **Modify:** `src/go/types/` equivalent — same change

### x-tools-spmd (`x-tools-spmd/`)
- **Modify:** `go/ssa/spmd_predicate.go:1300-1304` — don't exclude inner loops with varying bounds

### TinyGo (`tinygo/`)
- **Modify:** `compiler/spmd.go:412-532` — peel slice element type in spmdRangeIndexLaneCount

---

## Task 1: Type Checker — Peel Slice for Lane Count

**Files:**
- Modify: `go/src/cmd/compile/internal/types2/check_ext_spmd.go:160-188`
- Modify: `go/src/go/types/` equivalent (find the corresponding getTypeSize)

The `getTypeSize` function returns 24 for slice types (the header size). For the lane count computation of `go for range [][]T`, we need the INNER element type's size.

- [ ] **Step 1: Modify getTypeSize in types2**

In `go/src/cmd/compile/internal/types2/check_ext_spmd.go`, find the `getTypeSize` function (line 160). The `*Slice` case returns 24. Change it to return the element type's size:

```go
	case *Slice:
		// For lane count computation, use the element type size (not the
		// slice header). This enables N>1 lanes for go-for over slice-of-slices.
		return check.getTypeSize(t.Elem())
```

WAIT — this would break lane count for `go for range []byte` where we WANT `sizeof(byte) = 1` (which is already what strategy 1 in TinyGo's spmdRangeIndexLaneCount returns). The type checker's `getTypeSize` is used for the `varyingElemSizes` array which feeds `computeEffectiveLaneCount`. For `go for range []byte`, the range variable type IS `byte` (from `rVal`), not `[]byte`. So the slice case in getTypeSize is only hit when the element type IS a slice (slice-of-slices).

Actually — let me re-read the stmt_ext_spmd.go logic. At line 332:
```go
globalSPMDInfo.varyingElemSizes = append(..., check.getTypeSize(rVal))
```

`rVal` is the VALUE type from the range. For `go for i, v := range data` where `data` is `[][]int`:
- `rVal` = `[]int` (the element type of the outer slice)
- `getTypeSize([]int)` currently returns 24 → N=0 → fallback

With the fix: `getTypeSize([]int)` returns `getTypeSize(int) = 8` → N=2 on x86-64 (128/8=2), N=4 on WASM (128/4=4 since WASM int=i32).

This is correct! For `go for range []byte`, `rVal` = `byte`, `getTypeSize(byte) = 1` → N=16. No change needed there.

So the fix IS: make `getTypeSize` return the element type size for slices. Apply to both types2 and go/types.

- [ ] **Step 2: Find and modify go/types equivalent**

Search for `getTypeSize` in `go/src/go/types/`:
```bash
grep -rn "getTypeSize\|func.*TypeSize" go/src/go/types/*.go | head -10
```

Apply the same `*Slice` change. If go/types doesn't have a `getTypeSize` (the type checker paths diverge), check how `varyingElemSizes` is computed in the go/types path.

- [ ] **Step 3: Build Go toolchain**

```bash
cd /home/cedric/work/SPMD && make build-go
```

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/cmd/compile/internal/types2/check_ext_spmd.go src/go/types/
git commit -m "feat: peel slice element type for SPMD lane count

getTypeSize now returns the element type size for slices instead of
the header size (24 bytes). This enables N>1 lanes for go-for over
slice-of-slices. []int32 → sizeof(int32) = 4 → N=4 on SSE.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: SSA Predication — Include Varying-Bound Inner Loops

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1300-1304`

Currently `spmdLoopScopeBlocks` excludes inner loop headers (line 1303):
```go
if innerLoopHeaders[succ] {
    continue  // Stop at inner loop headers
}
```

The fix: before excluding, check if the inner loop has a varying bound. If so, include it.

- [ ] **Step 1: Add varying-bound detection for inner loops**

In `spmd_predicate.go`, find the inner loop exclusion at line 1300-1304. Replace:

```go
			// Stop at inner loop headers to avoid including their bodies in
			// the SPMD scope. Inner loops operate on scalar values; converting
			// their memory ops to masked vector ops produces type mismatches.
			if innerLoopHeaders[succ] {
				continue
			}
```

With:

```go
			// Stop at inner loop headers UNLESS the inner loop has a varying
			// bound (divergent inner loop). Varying-bound inner loops are
			// included in the SPMD scope so their condition If gets predicated
			// with a narrowing mask (lanes finish at different iterations).
			if innerLoopHeaders[succ] && !spmdInnerLoopHasVaryingBound(succ) {
				continue
			}
```

Then add the helper function:

```go
// spmdInnerLoopHasVaryingBound checks if an inner loop header's termination
// condition involves a varying value. This detects divergent inner loops
// (e.g., "for j := range v" where len(v) varies per lane).
func spmdInnerLoopHasVaryingBound(header *BasicBlock) bool {
	// The loop header typically ends with an If instruction.
	// Check if the If condition involves an SPMDType (varying value).
	if len(header.Instrs) == 0 {
		return false
	}
	terminator := header.Instrs[len(header.Instrs)-1]
	ifInstr, ok := terminator.(*If)
	if !ok {
		return false
	}
	// Check if the condition or any of its operands has an SPMD type.
	return spmdValueHasSPMDType(ifInstr.Cond)
}

// spmdValueHasSPMDType checks if a value has an SPMD (varying) type,
// tracing through BinOp comparisons to find varying operands.
func spmdValueHasSPMDType(v Value) bool {
	if _, ok := v.Type().(*types.SPMDType); ok {
		return true
	}
	// For BinOp comparisons (j < len(v)), check both operands.
	if binop, ok := v.(*BinOp); ok {
		if _, ok := binop.X.Type().(*types.SPMDType); ok {
			return true
		}
		if _, ok := binop.Y.Type().(*types.SPMDType); ok {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run x-tools-spmd tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/go/bin/go test ./go/ssa/... -count=1 2>&1 | tail -10
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go
git commit -m "feat: include varying-bound inner loops in SPMD scope

Don't exclude inner loop headers when the loop's termination condition
involves a varying value. This allows divergent inner loops (e.g.,
for-range over per-lane slices with different lengths) to be predicated
with a narrowing mask.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: TinyGo — Peel Slice Element Type in Lane Count

**Files:**
- Modify: `tinygo/compiler/spmd.go:412-532` (spmdRangeIndexLaneCount)

The TinyGo lane count function uses strategies 0-3 to determine element type. For slice-of-slices (`[][]int`), when the range variable is a slice, the existing strategies may not find the inner element type.

- [ ] **Step 1: Add slice-peeling to spmdRangeIndexLaneCount**

In `tinygo/compiler/spmd.go`, at the beginning of `spmdRangeIndexLaneCount` (around line 412), before Strategy 0, add a check for when the bound value comes from a `len(sliceOfSlices)`:

Actually, the better approach: Strategy 1 (line 427) already handles `len(slice)`. For `go for range data` where `data` is `[][]int`, the bound is `len(data)`. Strategy 1 extracts the slice's element type: `[]int`. But `getLLVMType([]int)` returns a slice struct (not vectorizable), so `spmdLaneCount` returns 1.

The fix: in Strategy 1, when the extracted element type IS a slice, peel one more level:

```go
	// Strategy 1: if boundValue is a call to builtin len, extract the slice element type.
	if call, ok := boundValue.(*ssa.Call); ok {
		if builtin, ok := call.Call.Value.(*ssa.Builtin); ok && builtin.Name() == "len" {
			if len(call.Call.Args) == 1 {
				arg := call.Call.Args[0]
				if sliceType, ok := arg.Type().Underlying().(*types.Slice); ok {
					elemType := sliceType.Elem()
					// Peel through nested slices: for [][]T, use T's size.
					if innerSlice, ok := elemType.Underlying().(*types.Slice); ok {
						elemType = innerSlice.Elem()
					}
					elemLLVM := b.getLLVMType(elemType)
					return b.spmdLaneCount(elemLLVM)
				}
			}
		}
	}
```

Also check Strategy 2 (IndexAddr scan) — for inner loop access patterns, the IndexAddr would be on the per-lane slice, not the outer slice. This should naturally work if the inner loop is included in the SPMD scope.

- [ ] **Step 2: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 3: Test array-counting**

```bash
# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/array-counting.wasm test/integration/spmd/array-counting/main.go 2>&1 \
  && wasmtime run /tmp/array-counting.wasm

# x86
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/array-counting \
  test/integration/spmd/array-counting/main.go 2>&1 && /tmp/array-counting
```

Expected output: `Array sums: [3 3 4 18]`

If it doesn't compile or produces wrong output, debug. The most likely issues:
1. The inner loop's varying condition isn't recognized → predication fails
2. The `Varying[[]int]` representation (`[N x sliceStruct]`) isn't handled by TinyGo's gather/scatter
3. `len(v)` extraction from per-lane slice headers doesn't produce a varying vector

**IMPORTANT**: This is the most complex task. If it fails, report the exact error and what you've tried. The inner loop gather from per-lane slice headers may need additional TinyGo codegen work.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "feat: peel slice element type for N>1 lanes on slice-of-slices

spmdRangeIndexLaneCount Strategy 1 now peels nested slices:
for [][]T, uses sizeof(T) instead of sizeof([]T) to determine
SIMD lane count. Enables N>1 for divergent inner loop patterns.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: E2E Validation + PLAN.md

**Files:**
- Modify: `PLAN.md`

- [ ] **Step 1: Verify array-counting on all targets**

```bash
# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/array-final.wasm test/integration/spmd/array-counting/main.go 2>&1 \
  && wasmtime run /tmp/array-final.wasm

# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/array-final-ssse3 \
  test/integration/spmd/array-counting/main.go 2>&1 && /tmp/array-final-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/array-final-avx2 \
  test/integration/spmd/array-counting/main.go 2>&1 && /tmp/array-final-avx2

# Also verify base64 no regression
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-noreg \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-noreg
```

- [ ] **Step 2: Update PLAN.md**

Mark divergent inner loops as DONE:
```
- [x] **Divergent inner loop support for N>1** — DONE (2026-04-12)
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD
git add PLAN.md
git commit -m "feat: mark divergent inner loops N>1 as DONE — PLAN.md complete!

All deferred items resolved. Zero remaining features in PLAN.md.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Implementation Notes

1. **The `types` package import** in spmd_predicate.go: `types.SPMDType` requires the `go/types` import (already present as `spmdpkg` or similar alias). Check the import path.

2. **The inner loop's `len(v)` call**: In go/ssa, `len(v)` where `v` is `Varying[[]int]` produces a `Builtin("len")` call. TinyGo's `createBuiltin` handles `len` for varying slices — it should extract lane 0's len. For N>1, it needs to extract ALL lanes' lengths. Check how `createBuiltin` handles `len` on `[N x sliceStruct]`.

3. **The `for range secondLevel` pattern**: In go/ssa, `for _, value := range secondLevel` becomes a rangeindex loop. When `secondLevel` is `Varying[[]int]`, the bound is `len(secondLevel)` which is `Varying[int]` (per-lane length). The loop header's If compares `j < Varying[int]` — this is the varying condition that should be detected by `spmdInnerLoopHasVaryingBound`.

4. **Per-lane gather in inner loop**: `secondLevel[j]` where `secondLevel` is `Varying[[]int]` and `j` is uniform → per-lane gather. Each lane's data pointer + offset `j` → N different addresses → `llvm.masked.gather`. TinyGo may need to handle this in `createSPMDLoad` for `[N x sliceStruct]` element access.

5. **This is the most complex remaining feature**. If Task 3 fails due to TinyGo codegen issues with `[N x sliceStruct]`, it's OK to report BLOCKED. The lane count fix (Tasks 1-2) is still valuable on its own.
