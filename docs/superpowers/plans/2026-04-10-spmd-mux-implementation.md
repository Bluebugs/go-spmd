# SPMDMux Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `SPMDMux` SSA instruction that replaces chains of `SPMDSelect` when masks derive from `IterPhi % constant` comparisons, enabling a single shuffle instead of N-1 masked selects.

**Architecture:** New SSA instruction in x-tools-spmd + post-predication detection pass + TinyGo lowering via N constant-mask LLVM `select` instructions. The detection pass traces SPMDSelect mask origins to `BinOp{REM}` on the loop iterator.

**Tech Stack:** Go (go/ssa), LLVM IR (via TinyGo), WASM SIMD128, x86 SSE/SSSE3/AVX2

**Spec:** `docs/superpowers/specs/2026-04-10-spmd-mux-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### x-tools-spmd (`x-tools-spmd/`)
- **Modify:** `go/ssa/ssa.go` — add `SPMDMux` struct + Operands + Pos
- **Modify:** `go/ssa/print.go` — add `String()` method
- **Modify:** `go/ssa/emit.go` — add `emitSPMDMux` helper
- **Modify:** `go/ssa/sanity.go` — add sanity checks
- **Modify:** `go/ssa/spmd_predicate.go` — add `spmdDetectMuxPatterns` pass
- **Modify:** `go/ssa/func.go` — wire detection pass into pipeline
- **Create:** `go/ssa/spmd_mux_test.go` — unit tests

### TinyGo (`tinygo/`)
- **Modify:** `compiler/compiler.go` — add dispatch case
- **Modify:** `compiler/spmd.go` — add `createSPMDMux` lowering

---

## Task 1: `SPMDMux` SSA Instruction Definition

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (after SPMDSelect, around line 1453)
- Modify: `x-tools-spmd/go/ssa/print.go` (after SPMDSelect.String)
- Modify: `x-tools-spmd/go/ssa/emit.go` (after emitSPMDCompactStore)
- Modify: `x-tools-spmd/go/ssa/sanity.go` (after SPMDSelect case)

- [ ] **Step 1: Add SPMDMux struct to ssa.go**

After the `SPMDSelect` type definition (around line 1453), add:

```go
// SPMDMux selects per-lane from N value operands based on a compile-time
// constant index vector. Replaces chains of SPMDSelect when the masks
// derive from IterPhi % constant comparisons, enabling a single shuffle
// instead of N-1 masked selects.
//
// All Values must have identical types. Indices[i] must be in [0, len(Values)).
// The result type matches Values[0].Type().
//
// Example printed form:
//
//	t8 = spmd_mux<16> [t3, t5, t7] indices [0,1,2,0,0,1,2,0,...]
type SPMDMux struct {
	register
	Values  []Value // N value operands (one per case)
	Indices []int   // per-lane index into Values (len = Lanes)
	Lanes   int     // SIMD width
}
```

- [ ] **Step 2: Add Operands method**

After the SPMDSelect.Operands method:

```go
func (v *SPMDMux) Operands(rands []*Value) []*Value {
	for i := range v.Values {
		rands = append(rands, &v.Values[i])
	}
	return rands
}
```

- [ ] **Step 3: Add Pos method**

After SPMDSelect.Pos:

```go
func (v *SPMDMux) Pos() token.Pos { return token.NoPos }
```

- [ ] **Step 4: Add String method to print.go**

After SPMDSelect.String():

```go
func (v *SPMDMux) String() string {
	from := spmdRelPkg(v)
	var vals []string
	for _, val := range v.Values {
		vals = append(vals, relName(val, from))
	}
	// Show first 8 indices, truncate with "..." if more.
	idxStr := ""
	for i, idx := range v.Indices {
		if i > 0 {
			idxStr += ","
		}
		if i >= 8 {
			idxStr += "..."
			break
		}
		idxStr += fmt.Sprintf("%d", idx)
	}
	return fmt.Sprintf("spmd_mux<%d> [%s] indices [%s]",
		v.Lanes, strings.Join(vals, ", "), idxStr)
}
```

Note: Check if `strings` is already imported in print.go. If not, add the import.

- [ ] **Step 5: Add emitSPMDMux to emit.go**

After `emitSPMDCompactStore`:

```go
// emitSPMDMux emits an SPMDMux instruction.
// values are the N value operands. indices is the per-lane index vector.
// The result type is values[0].Type().
func emitSPMDMux(f *Function, values []Value, indices []int, lanes int) *SPMDMux {
	v := &SPMDMux{
		Values:  values,
		Indices: indices,
		Lanes:   lanes,
	}
	v.setType(values[0].Type())
	f.emit(v)
	return v
}
```

- [ ] **Step 6: Add sanity checks to sanity.go**

After the SPMDSelect case:

```go
	case *SPMDMux:
		if instr.Lanes <= 0 {
			s.errorf("SPMDMux: Lanes must be > 0, got %d", instr.Lanes)
		}
		if len(instr.Values) < 2 {
			s.errorf("SPMDMux: need at least 2 Values, got %d", len(instr.Values))
		}
		if len(instr.Indices) != instr.Lanes {
			s.errorf("SPMDMux: len(Indices) = %d, want %d (Lanes)", len(instr.Indices), instr.Lanes)
		}
		for i, idx := range instr.Indices {
			if idx < 0 || idx >= len(instr.Values) {
				s.errorf("SPMDMux: Indices[%d] = %d, out of range [0, %d)", i, idx, len(instr.Values))
			}
		}
		baseType := instr.Values[0].Type()
		for i := 1; i < len(instr.Values); i++ {
			if !types.Identical(instr.Values[i].Type(), baseType) {
				s.errorf("SPMDMux: Values[%d] type %s != Values[0] type %s",
					i, instr.Values[i].Type(), baseType)
			}
		}
```

- [ ] **Step 7: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -count=1 2>&1 | tail -10
```

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/print.go go/ssa/emit.go go/ssa/sanity.go
git commit -m "feat: add SPMDMux SSA instruction for constant-index lane selection

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Detection Pass — `spmdDetectMuxPatterns`

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` — add detection pass
- Modify: `x-tools-spmd/go/ssa/func.go:417` — wire into pipeline

This is the most complex task. The detection pass runs after `predicateSPMD` and `spmdMergeRedundantStores`, scanning each SPMD loop's scope blocks for collapsible SPMDSelect chains.

- [ ] **Step 1: Add `spmdDetectMuxPatterns` function**

Add at the end of `spmd_predicate.go` (after `spmdMergeRedundantStores` and its helpers):

```go
// spmdDetectMuxPatterns scans SPMD loop bodies for chains of SPMDSelect
// instructions whose masks derive from IterPhi % constant comparisons.
// Such chains are replaced with a single SPMDMux instruction.
//
// Pattern:
//   pos = BinOp{REM}(iter_derived, Const{k})    // pos = i % k
//   mask_i = BinOp{EQL}(pos, Const{c_i})        // mask for case c_i
//   ...chain of SPMDSelect using these masks...
//
// Must run after predicateSPMD (which creates SPMDSelect chains) and after
// spmdMergeRedundantStores. Runs before peelSPMDLoops.
func spmdDetectMuxPatterns(fn *Function) {
	for _, loop := range fn.SPMDLoops {
		if loop.LaneCount <= 1 {
			continue
		}
		scopeBlocks := spmdLoopScopeBlocks(loop)
		spmdDetectMuxInScope(fn, loop, scopeBlocks)
	}
}

// spmdDetectMuxInScope scans blocks in scopeBlocks for SPMDSelect chain roots
// and attempts to collapse them into SPMDMux instructions.
func spmdDetectMuxInScope(fn *Function, loop *SPMDLoopInfo, scopeBlocks map[*BasicBlock]bool) {
	for _, block := range fn.Blocks {
		if !scopeBlocks[block] {
			continue
		}
		for i := 0; i < len(block.Instrs); i++ {
			sel, ok := block.Instrs[i].(*SPMDSelect)
			if !ok {
				continue
			}
			// Check if this is a chain root (not used as Y of another SPMDSelect in scope).
			if spmdIsSelectChainInner(sel, scopeBlocks) {
				continue
			}
			// Try to collapse this chain.
			if mux := spmdTryCollapseMux(fn, sel, loop); mux != nil {
				// Replace the SPMDSelect with SPMDMux in the block.
				block.Instrs[i] = mux
				mux.setBlock(block)
				replaceAll(sel, mux)
				// Remove referrers from old select.
				sel.block = nil
			}
		}
	}
}
```

- [ ] **Step 2: Add helper to check if a select is an inner node**

```go
// spmdIsSelectChainInner returns true if sel is used as the Y operand of
// another SPMDSelect (i.e., it's not the chain root).
func spmdIsSelectChainInner(sel *SPMDSelect, scopeBlocks map[*BasicBlock]bool) bool {
	if sel.Referrers() == nil {
		return false
	}
	for _, ref := range *sel.Referrers() {
		if parentSel, ok := ref.(*SPMDSelect); ok {
			if parentSel.Y == sel && parentSel.block != nil && scopeBlocks[parentSel.block] {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 3: Add the chain collapse function**

```go
// spmdTryCollapseMux attempts to collapse an SPMDSelect chain rooted at sel
// into an SPMDMux. Returns nil if the pattern doesn't match.
func spmdTryCollapseMux(fn *Function, sel *SPMDSelect, loop *SPMDLoopInfo) *SPMDMux {
	// Step 1: Unwind the chain, collecting (mask, value) pairs.
	type maskValPair struct {
		mask Value
		val  Value
	}
	var pairs []maskValPair
	var defaultVal Value
	cur := sel
	for {
		pairs = append(pairs, maskValPair{mask: cur.Mask, val: cur.X})
		// Follow Y to the next SPMDSelect in the chain.
		inner, ok := cur.Y.(*SPMDSelect)
		if !ok {
			defaultVal = cur.Y
			break
		}
		cur = inner
	}

	if len(pairs) < 2 {
		return nil // need at least 2 selects to be worth collapsing
	}

	// Step 2: Trace each mask to BinOp{EQL}(rem_result, Const{c}).
	// All masks must share the same rem_result.
	var sharedRem *BinOp
	caseMap := make(map[int64]Value) // comparison constant → value

	for _, pair := range pairs {
		rem, constVal, ok := spmdTraceMaskToRemEQL(pair.mask)
		if !ok {
			return nil
		}
		if sharedRem == nil {
			sharedRem = rem
		} else if sharedRem != rem {
			return nil // different REM sources
		}
		caseMap[constVal] = pair.val
	}

	// Step 3: Extract divisor k from the REM instruction.
	remConst, ok := sharedRem.Y.(*Const)
	if !ok {
		return nil
	}
	k, ok := constant.Int64Val(remConst.Value)
	if !ok || k <= 0 {
		return nil
	}

	// Step 4: Verify laneCount % k == 0.
	laneCount := loop.LaneCount
	if int64(laneCount)%k != 0 {
		return nil
	}

	// Step 5: Verify the REM's left operand derives from IterPhi.
	if !spmdTracesToIterPhi(sharedRem.X, loop) {
		return nil
	}

	// Step 6: Build the Values slice and Indices vector.
	// Assign each case constant an index. Default gets the last index.
	values := make([]Value, 0, len(caseMap)+1)
	constToIdx := make(map[int64]int)
	for c, v := range caseMap {
		constToIdx[c] = len(values)
		values = append(values, v)
	}
	defaultIdx := len(values)
	values = append(values, defaultVal)

	indices := make([]int, laneCount)
	for lane := 0; lane < laneCount; lane++ {
		remainder := int64(lane) % k
		if idx, ok := constToIdx[remainder]; ok {
			indices[lane] = idx
		} else {
			indices[lane] = defaultIdx
		}
	}

	// Step 7: Emit SPMDMux.
	mux := &SPMDMux{
		Values:  values,
		Indices: indices,
		Lanes:   laneCount,
	}
	mux.setType(values[0].Type())
	// Add referrers.
	for _, v := range values {
		spmdAddReferrer(v, mux)
	}
	return mux
}
```

- [ ] **Step 4: Add mask tracing helper**

```go
// spmdTraceMaskToRemEQL checks if a mask value traces to:
//   BinOp{EQL}(BinOp{REM}(x, Const{k}), Const{c})
// Returns the REM BinOp, the comparison constant c, and whether the pattern matched.
// Also handles the negated case: the mask from predicateVaryingIf may be
// BinOp{AND}(activeMask, convertedCond) where convertedCond is the EQL result
// after Convert to mask type.
func spmdTraceMaskToRemEQL(mask Value) (*BinOp, int64, bool) {
	// The mask at SPMDSelect is typically:
	//   thenMask = BinOp{AND}(activeMask, Convert(BinOp{EQL}(rem, const)))
	// or for else branches:
	//   elseMask = BinOp{AND}(activeMask, NOT(Convert(BinOp{EQL}(rem, const))))
	// We need to peel through AND and Convert to find the EQL.

	// Try to find a BinOp{EQL} by peeling layers.
	eql := spmdPeelToEQL(mask)
	if eql == nil {
		return nil, 0, false
	}

	// One side must be a Const, the other a BinOp{REM}.
	rem, constVal := spmdMatchRemAndConst(eql)
	if rem == nil {
		return nil, 0, false
	}

	return rem, constVal, true
}

// spmdPeelToEQL peels through BinOp{AND}, Convert, ChangeType instructions
// to find the underlying BinOp{EQL} comparison.
func spmdPeelToEQL(v Value) *BinOp {
	for {
		switch x := v.(type) {
		case *BinOp:
			if x.Op == token.EQL {
				return x
			}
			if x.Op == token.AND {
				// Try both sides of AND — one is the active mask, other is the condition.
				if eql := spmdPeelToEQL(x.X); eql != nil {
					return eql
				}
				return spmdPeelToEQL(x.Y)
			}
			return nil
		case *Convert:
			v = x.X
		case *ChangeType:
			v = x.X
		default:
			return nil
		}
	}
}

// spmdMatchRemAndConst checks if a BinOp{EQL} has one side that is a
// BinOp{REM} and the other side is a *Const. Returns the REM and the
// constant value.
func spmdMatchRemAndConst(eql *BinOp) (*BinOp, int64) {
	// Try X=REM, Y=Const
	if rem, ok := eql.X.(*BinOp); ok && rem.Op == token.REM {
		if c, ok := eql.Y.(*Const); ok {
			if val, ok := constant.Int64Val(c.Value); ok {
				return rem, val
			}
		}
	}
	// Try X=Const, Y=REM
	if rem, ok := eql.Y.(*BinOp); ok && rem.Op == token.REM {
		if c, ok := eql.X.(*Const); ok {
			if val, ok := constant.Int64Val(c.Value); ok {
				return rem, val
			}
		}
	}
	return nil, 0
}
```

- [ ] **Step 5: Add IterPhi tracing helper**

```go
// spmdTracesToIterPhi returns true if v traces back to the loop's IterPhi
// through Convert, BinOp{ADD/SUB} with constants, etc.
func spmdTracesToIterPhi(v Value, loop *SPMDLoopInfo) bool {
	seen := make(map[Value]bool)
	return spmdTracesToIterPhiRec(v, loop, seen)
}

func spmdTracesToIterPhiRec(v Value, loop *SPMDLoopInfo, seen map[Value]bool) bool {
	if seen[v] {
		return false
	}
	seen[v] = true

	if v == loop.IterPhi {
		return true
	}
	switch x := v.(type) {
	case *Convert:
		return spmdTracesToIterPhiRec(x.X, loop, seen)
	case *ChangeType:
		return spmdTracesToIterPhiRec(x.X, loop, seen)
	case *BinOp:
		// Allow ADD/SUB/MUL with a constant on one side.
		if x.Op == token.ADD || x.Op == token.SUB || x.Op == token.MUL {
			_, xConst := x.X.(*Const)
			_, yConst := x.Y.(*Const)
			if xConst {
				return spmdTracesToIterPhiRec(x.Y, loop, seen)
			}
			if yConst {
				return spmdTracesToIterPhiRec(x.X, loop, seen)
			}
		}
		return false
	default:
		return false
	}
}
```

- [ ] **Step 6: Wire into pipeline in func.go**

In `func.go`, after `spmdMergeRedundantStores(f)` (line 417), add:

```go
		// Detect SPMDSelect chains from IterPhi % constant patterns
		// and collapse them into SPMDMux instructions.
		spmdDetectMuxPatterns(f)
```

- [ ] **Step 7: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -count=1 2>&1 | tail -10
```

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go go/ssa/func.go
git commit -m "feat: add spmdDetectMuxPatterns post-predication pass

Detects SPMDSelect chains where masks derive from IterPhi % constant
comparisons and collapses them into SPMDMux instructions. Traces
masks through BinOp{AND}, Convert layers to find the underlying
BinOp{EQL}(BinOp{REM}(iter, k), c) pattern.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Unit Tests for SPMDMux Detection

**Files:**
- Create: `x-tools-spmd/go/ssa/spmd_mux_test.go`

- [ ] **Step 1: Write test for basic mux detection**

Create `x-tools-spmd/go/ssa/spmd_mux_test.go`. Follow the test helper pattern used in existing `spmd_*_test.go` files (e.g., `spmd_varying_test.go`). The test compiles a Go source with a `pos := i % 3` if/else chain and verifies that `SPMDMux` appears in the SSA output.

```go
package ssa_test

import (
	"strings"
	"testing"
)

func TestSPMDMux_BasicDetection(t *testing.T) {
	src := `
package main

import "lanes"

func main() {
	src := []byte("ABCDEF")
	dst := make([]byte, 8)
	go for i, ch := range src {
		pos := i % 3
		out := ch
		if pos == 0 {
			out = ch + 1
		} else if pos == 1 {
			out = ch + 2
		} else {
			out = ch + 3
		}
		dst[i] = out
		_ = lanes.Count[byte](ch) // keep lanes import used
	}
}
`
	fn := buildSPMDFunction(t, src, "main")
	if fn == nil {
		t.Fatal("failed to build function")
	}

	found := false
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			if strings.Contains(instr.String(), "spmd_mux") {
				found = true
				t.Logf("Found SPMDMux: %s", instr.String())
			}
		}
	}
	if !found {
		t.Error("expected SPMDMux instruction, found none")
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				t.Logf("  %s", instr)
			}
		}
	}
}
```

Note: Adapt `buildSPMDFunction` to match the test helper in existing files. The helper compiles Go source with SPMD enabled and returns the function's SSA. If the helper doesn't exist by that name, look at how existing tests build SSA from source.

- [ ] **Step 2: Run test**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -run TestSPMDMux -v 2>&1 | tail -20
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_mux_test.go
git commit -m "test: add SPMDMux detection unit test

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: TinyGo Lowering — `createSPMDMux`

**Files:**
- Modify: `tinygo/compiler/compiler.go` — add dispatch case
- Modify: `tinygo/compiler/spmd.go` — add `createSPMDMux`

- [ ] **Step 1: Add dispatch in compiler.go**

Find the value-producing expression switch (around line 3803-3807 where `*ssa.SPMDLoad`, `*ssa.SPMDSelect`, `*ssa.SPMDIndex` are handled). Add:

```go
	case *ssa.SPMDMux:
		return b.createSPMDMux(expr), nil
```

- [ ] **Step 2: Add `createSPMDMux` in spmd.go**

```go
// createSPMDMux lowers an SPMDMux instruction to LLVM IR.
// Emits N LLVM select instructions with compile-time constant <Lanes x i1> masks,
// one per value operand. LLVM constant-folds these into optimal blend/shuffle
// sequences per target.
func (b *builder) createSPMDMux(instr *ssa.SPMDMux) llvm.Value {
	// Scalar fallback: Indices has one element, just return that value.
	if !b.simdEnabled {
		idx := instr.Indices[0]
		return b.getValue(instr.Values[idx], getPos(instr))
	}

	laneCount := instr.Lanes
	numValues := len(instr.Values)

	// Evaluate all value operands.
	vals := make([]llvm.Value, numValues)
	for i, v := range instr.Values {
		vals[i] = b.getValue(v, getPos(instr))
	}

	// Ensure all values are vectors (some may be scalar constants).
	for i, v := range vals {
		if v.Type().TypeKind() != llvm.VectorTypeKind {
			vals[i] = b.splatScalar(v, vals[0].Type())
		}
	}

	// Build per-value constant <Lanes x i1> masks.
	i1Type := b.ctx.Int1Type()
	trueVal := llvm.ConstInt(i1Type, 1, false)
	falseVal := llvm.ConstInt(i1Type, 0, false)

	// Start with zero-initialized result.
	result := llvm.ConstNull(vals[0].Type())

	for valIdx := 0; valIdx < numValues; valIdx++ {
		// Build mask: true where Indices[lane] == valIdx.
		maskElts := make([]llvm.Value, laneCount)
		anyActive := false
		for lane := 0; lane < laneCount; lane++ {
			if instr.Indices[lane] == valIdx {
				maskElts[lane] = trueVal
				anyActive = true
			} else {
				maskElts[lane] = falseVal
			}
		}
		if !anyActive {
			continue // skip values not referenced by any lane
		}
		mask := llvm.ConstVector(maskElts, false)
		result = b.CreateSelect(mask, vals[valIdx], result, "spmd.mux.sel")
	}

	return result
}
```

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 4: Verify correctness on all targets**

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-mux.wasm test/integration/spmd/base64-mula-lemire/main.go 2>&1 \
  && wasmtime run /tmp/base64-mux.wasm
```

SSSE3:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-mux-ssse3 \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-mux-ssse3
```

AVX2:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-mux-avx2 \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-mux-avx2
```

CompactStore test:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/compact-mux.wasm test/integration/spmd/compact-store/main.go 2>&1 \
  && wasmtime run /tmp/compact-mux.wasm
```

Expected: `Correctness: PASS` / `PASS` for all.

- [ ] **Step 5: Run benchmarks**

WASM:
```bash
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-mux-wasm.wasm test/integration/spmd/base64-mula-lemire/bench.go 2>&1 \
  && wasmtime run /tmp/bench-mux-wasm.wasm
```

SSSE3:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-mux-ssse3 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-mux-ssse3
```

AVX2:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-mux-avx2 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-mux-avx2
```

Report results — target: SSSE3 from ~10x to ~14-16x at 1MB, WASM from 0.94x to >1.5x.

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/spmd.go
git commit -m "feat: add TinyGo lowering for SPMDMux instruction

Emits N LLVM select instructions with constant i1 masks, one per
value operand. LLVM constant-folds these into optimal blend/shuffle
per target. Eliminates mask computation overhead from the base64
decoder's pos-based if/else chain.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: E2E Validation

**Files:** None (verification only)

- [ ] **Step 1: Run full E2E test suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```

Expected: No regressions (90+ run-pass).

- [ ] **Step 2: Compare before/after benchmarks**

| Target | Before (SPMDSelect chain) | After (SPMDMux) | Improvement |
|--------|---------------------------|-----------------|-------------|
| WASM | 0.94x / 133 MB/s | ? | ? |
| SSSE3 | 10.24x / 1944 MB/s | ? | ? |
| AVX2 | 10.85x / 2046 MB/s | ? | ? |

---

## Deferred Items

1. **NEQ mask handling**: `pos != K` as a chain root mask (swap X/Y). Current detection only handles EQL. Priority: Low (the base64 pattern uses EQL for the selects, NEQ only for CompactStore mask).

2. **Cross-block SPMDSelect chains**: Chains spanning multiple linearized blocks. Current detection follows Y operands which may cross blocks. The implementation handles this naturally since Y is a Value reference, not block-local. Priority: Low.

3. **SPMDMux → single pshufb/shufflevector**: If LLVM doesn't collapse the N selects into a shuffle, add a TinyGo-level optimization that builds the shuffle directly from the Indices vector. Priority: Medium (depends on benchmark results).
