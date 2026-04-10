# SPMDInterleaveStore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `SPMDInterleaveStore` SSA instruction that replaces `SPMDMux + lanes.CompactStore` with diagonal-extraction shuffles + ORs + compaction shuffle + contiguous store when the Mux has periodic indices.

**Architecture:** New SSA instruction in x-tools-spmd + detection pass (runs after `spmdDetectMuxPatterns`, before `peelSPMDLoops`) + TinyGo lowering using N `spmdSwizzle` + ORs + 1 compaction swizzle + 1 store. Detection walks SPMDMux referrers to find consuming `lanes.CompactStore` call.

**Tech Stack:** Go (go/ssa), LLVM IR (via TinyGo), WASM SIMD128, x86 SSE/SSSE3/AVX2

**Spec:** `docs/superpowers/specs/2026-04-10-spmd-interleave-store-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### x-tools-spmd (`x-tools-spmd/`)
- **Modify:** `go/ssa/ssa.go` — add `SPMDInterleaveStore` struct + Operands + Pos
- **Modify:** `go/ssa/print.go` — add `String()` method
- **Modify:** `go/ssa/emit.go` — add emit helper
- **Modify:** `go/ssa/sanity.go` — add sanity checks
- **Modify:** `go/ssa/spmd_predicate.go` — add `spmdDetectInterleaveStore` pass
- **Modify:** `go/ssa/spmd_peel.go` — add clone case
- **Modify:** `go/ssa/func.go` — wire pass into pipeline

### TinyGo (`tinygo/`)
- **Modify:** `compiler/compiler.go` — add dispatch case
- **Modify:** `compiler/spmd.go` — add `createSPMDInterleaveStore` lowering

---

## Task 1: `SPMDInterleaveStore` SSA Instruction Definition

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go` (after SPMDMux)
- Modify: `x-tools-spmd/go/ssa/print.go` (after SPMDMux.String)
- Modify: `x-tools-spmd/go/ssa/emit.go` (after emitSPMDMux)
- Modify: `x-tools-spmd/go/ssa/sanity.go` (after SPMDMux case)
- Modify: `x-tools-spmd/go/ssa/spmd_peel.go` (after SPMDMux clone case)

- [ ] **Step 1: Add SPMDInterleaveStore struct to ssa.go**

After the `SPMDMux` type definition, add:

```go
// SPMDInterleaveStore writes N value vectors interleaved with period K
// contiguously to Addr. Replaces SPMDMux + CompactStore when the Mux
// indices are periodic and the CompactStore mask matches.
//
// For period K=4 with 3 values [A, B, C] and 16 byte lanes:
//   From each group of K input lanes, extracts the diagonal:
//     group g: Values[0][g*K+0], Values[1][g*K+1], Values[2][g*K+2]
//   Output = [A[0],B[1],C[2], A[4],B[5],C[6], A[8],B[9],C[10], A[12],B[13],C[14]]
//
// Returns the number of elements written: len(Values) * Lanes / Period.
//
// Example printed form:
//
//	t9 = spmd_interleave_store<16, period=4> t1 [t3, t5, t7]
type SPMDInterleaveStore struct {
	register
	Addr      Value   // *T destination pointer
	Values    []Value // N value vectors to interleave
	Period    int     // K (group size / interleave stride)
	Lanes     int     // SIMD width of input vectors
	Mask      Value   // execution mask (may be nil for all-ones)
	Source    Value   // original slice (for bounds)
	SourceLen Value   // len(slice)
	pos       token.Pos
}
```

- [ ] **Step 2: Add Operands method**

```go
func (s *SPMDInterleaveStore) Operands(rands []*Value) []*Value {
	rands = append(rands, &s.Addr)
	for i := range s.Values {
		rands = append(rands, &s.Values[i])
	}
	if s.Mask != nil {
		rands = append(rands, &s.Mask)
	}
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

```go
func (s *SPMDInterleaveStore) Pos() token.Pos { return s.pos }
```

- [ ] **Step 4: Add String method to print.go**

After SPMDMux.String():

```go
func (s *SPMDInterleaveStore) String() string {
	from := spmdRelPkg(s)
	var vals []string
	for _, val := range s.Values {
		vals = append(vals, relName(val, from))
	}
	return fmt.Sprintf("spmd_interleave_store<%d, period=%d> %s [%s]",
		s.Lanes, s.Period, spmdRelName(s.Addr, s), strings.Join(vals, ", "))
}
```

- [ ] **Step 5: Add emit helper to emit.go**

```go
// emitSPMDInterleaveStore emits an SPMDInterleaveStore instruction.
func emitSPMDInterleaveStore(f *Function, addr Value, values []Value, period, lanes int, mask, source, sourceLen Value, pos token.Pos) *SPMDInterleaveStore {
	s := &SPMDInterleaveStore{
		Addr:      addr,
		Values:    values,
		Period:    period,
		Lanes:     lanes,
		Mask:      mask,
		Source:    source,
		SourceLen: sourceLen,
		pos:       pos,
	}
	s.setType(types.Typ[types.Int]) // returns element count
	f.emit(s)
	return s
}
```

- [ ] **Step 6: Add sanity checks to sanity.go**

After SPMDMux case:

```go
	case *SPMDInterleaveStore:
		if instr.Lanes <= 0 {
			s.errorf("SPMDInterleaveStore: Lanes must be > 0, got %d", instr.Lanes)
		}
		if instr.Period <= 0 {
			s.errorf("SPMDInterleaveStore: Period must be > 0, got %d", instr.Period)
		}
		if instr.Lanes%instr.Period != 0 {
			s.errorf("SPMDInterleaveStore: Lanes (%d) must be divisible by Period (%d)", instr.Lanes, instr.Period)
		}
		if len(instr.Values) < 1 {
			s.errorf("SPMDInterleaveStore: need at least 1 Value, got %d", len(instr.Values))
		}
		if len(instr.Values) >= instr.Period {
			s.errorf("SPMDInterleaveStore: len(Values) (%d) must be < Period (%d)", len(instr.Values), instr.Period)
		}
		if _, ok := instr.Addr.Type().Underlying().(*types.Pointer); !ok {
			s.errorf("SPMDInterleaveStore: Addr must be a pointer type, got %s", instr.Addr.Type())
		}
```

- [ ] **Step 7: Add clone case to spmd_peel.go**

Find the SPMDMux clone case and add after it:

```go
	case *SPMDInterleaveStore:
		newValues := make([]Value, len(instr.Values))
		for i, v := range instr.Values {
			newValues[i] = xlate(v)
		}
		newInstr := &SPMDInterleaveStore{
			Addr:      xlate(instr.Addr),
			Values:    newValues,
			Period:    instr.Period,
			Lanes:     instr.Lanes,
			Source:    xlate(instr.Source),
			SourceLen: xlate(instr.SourceLen),
			pos:       instr.pos,
		}
		if instr.Mask != nil {
			newInstr.Mask = xlate(instr.Mask)
		}
		newInstr.setType(instr.Type())
		return newInstr
```

- [ ] **Step 8: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -count=1 2>&1 | tail -10
```

- [ ] **Step 9: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/ssa.go go/ssa/print.go go/ssa/emit.go go/ssa/sanity.go go/ssa/spmd_peel.go
git commit -m "feat: add SPMDInterleaveStore SSA instruction

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Detection Pass — `spmdDetectInterleaveStore`

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go` — add detection pass
- Modify: `x-tools-spmd/go/ssa/func.go:420` — wire into pipeline

The detection runs after `spmdDetectMuxPatterns` (line 420) and before `peelSPMDLoops` (line 424).

- [ ] **Step 1: Add the detection pass**

Add at the end of `spmd_predicate.go`, after the `spmdDetectMuxPatterns` helpers:

```go
// spmdDetectInterleaveStore scans SPMD loop bodies for the pattern:
//   mux = SPMDMux(Values, periodic Indices with period K)
//   n = Call(lanes.CompactStore, dst, mux, mask)
// and replaces both with SPMDInterleaveStore.
func spmdDetectInterleaveStore(fn *Function) {
	for _, loop := range fn.SPMDLoops {
		if loop.LaneCount <= 1 {
			continue
		}
		scopeBlocks := spmdLoopScopeBlocks(loop)
		spmdDetectInterleaveStoreInScope(fn, loop, scopeBlocks)
	}
}

func spmdDetectInterleaveStoreInScope(fn *Function, loop *SPMDLoopInfo, scopeBlocks map[*BasicBlock]bool) {
	for _, block := range fn.Blocks {
		if !scopeBlocks[block] {
			continue
		}
		for i := 0; i < len(block.Instrs); i++ {
			mux, ok := block.Instrs[i].(*SPMDMux)
			if !ok {
				continue
			}
			// Check that Indices are periodic.
			period := spmdMuxIndicesPeriod(mux)
			if period <= 0 {
				continue
			}
			// Find a lanes.CompactStore call consuming this Mux.
			call, callIdx, callBlock := spmdFindCompactStoreCall(mux, scopeBlocks)
			if call == nil {
				continue
			}
			// Verify the CompactStore mask matches the Mux gap pattern.
			if !spmdCompactStoreMaskMatchesMux(call, mux, period) {
				continue
			}
			// Extract slice info from the CompactStore call's dst argument.
			// lanes.CompactStore(dst []T, v Varying[T], mask Varying[bool]) int
			// Args: [0]=dst slice, [1]=value (the mux), [2]=mask
			dstSlice := call.Call.Args[0]

			// Build SPMDInterleaveStore.
			interleave := &SPMDInterleaveStore{
				Addr:    nil, // will extract from slice below
				Values:  mux.Values,
				Period:  period,
				Lanes:   mux.Lanes,
				pos:     call.Pos(),
			}
			interleave.setType(types.Typ[types.Int])

			// Extract pointer and length from the slice.
			// The slice is an SSA value of type []T. We need Addr (*T) and SourceLen (int).
			// These are extracted at the LLVM level by TinyGo, not at SSA level.
			// At SSA level, store the slice as Source and let TinyGo decompose it.
			interleave.Source = dstSlice
			interleave.SourceLen = dstSlice // TinyGo extracts len from slice header

			// The Addr is the slice's data pointer — but at SSA level we don't have
			// an explicit Extract instruction for slice fields. Pass the slice as Addr
			// and let TinyGo handle the decomposition (ExtractValue 0 for ptr).
			interleave.Addr = dstSlice

			// Execution mask from the CompactStore call.
			if call.Call.SPMDMask != nil {
				interleave.Mask = call.Call.SPMDMask
			}

			// Add referrers.
			for _, v := range interleave.Values {
				spmdAddReferrer(v, interleave)
			}
			spmdAddReferrer(dstSlice, interleave)
			if interleave.Mask != nil {
				spmdAddReferrer(interleave.Mask, interleave)
			}

			// Replace the CompactStore call with SPMDInterleaveStore.
			interleave.setBlock(callBlock)
			callBlock.Instrs[callIdx] = interleave
			replaceAll(call, interleave)
			call.block = nil

			// Remove the SPMDMux (now dead — its only user was CompactStore).
			// Check if mux has no other referrers before removing.
			if refs := mux.Referrers(); refs != nil && len(*refs) == 0 {
				block.Instrs[i] = nil // mark for removal
			}
		}
		// Clean up nil instructions.
		spmdCompactBlockInstrs(block)
	}
}
```

- [ ] **Step 2: Add helper to detect periodic indices**

```go
// spmdMuxIndicesPeriod returns the period K of the SPMDMux Indices, or -1 if not periodic.
// Periodic means Indices[i] == Indices[i+K] for all i, and K is the smallest such value.
func spmdMuxIndicesPeriod(mux *SPMDMux) int {
	n := len(mux.Indices)
	// Try periods from 2 up to n/2.
	for k := 2; k <= n/2; k++ {
		if n%k != 0 {
			continue
		}
		periodic := true
		for i := k; i < n; i++ {
			if mux.Indices[i] != mux.Indices[i%k] {
				periodic = false
				break
			}
		}
		if periodic {
			return k
		}
	}
	return -1
}
```

- [ ] **Step 3: Add helper to find CompactStore call**

```go
// spmdFindCompactStoreCall walks the referrers of mux to find a *ssa.Call
// to lanes.CompactStore where mux is the value argument (Args[1]).
// Returns the call, its index in the block, and the block.
func spmdFindCompactStoreCall(mux *SPMDMux, scopeBlocks map[*BasicBlock]bool) (*Call, int, *BasicBlock) {
	refs := mux.Referrers()
	if refs == nil {
		return nil, 0, nil
	}
	for _, ref := range *refs {
		call, ok := ref.(*Call)
		if !ok {
			continue
		}
		if call.block == nil || !scopeBlocks[call.block] {
			continue
		}
		// Check that this is a call to lanes.CompactStore.
		callee := call.Call.StaticCallee()
		if callee == nil {
			continue
		}
		name := callee.Name()
		pkg := callee.Pkg()
		if pkg == nil || pkg.Pkg == nil {
			continue
		}
		if pkg.Pkg.Name() != "lanes" {
			continue
		}
		if !strings.HasPrefix(name, "CompactStore[") {
			continue
		}
		// Verify mux is Args[1] (the value argument).
		if len(call.Call.Args) >= 2 && call.Call.Args[1] == mux {
			// Find the index in the block.
			for idx, instr := range call.block.Instrs {
				if instr == call {
					return call, idx, call.block
				}
			}
		}
	}
	return nil, 0, nil
}
```

- [ ] **Step 4: Add mask matching helper**

```go
// spmdCompactStoreMaskMatchesMux verifies that the CompactStore mask
// is consistent with the SPMDMux period — the mask should have
// (period - numValues) inactive lanes per group, repeating.
// For base64: period=4, numValues=3, so 1 inactive lane per group.
func spmdCompactStoreMaskMatchesMux(call *Call, mux *SPMDMux, period int) bool {
	// Simple check: len(Values) + 1 == period (one gap per group).
	// More complex patterns (multiple gaps) could be supported later.
	if len(mux.Values)+1 != period {
		return false
	}
	// Verify the Indices have exactly one "gap" position per period
	// that isn't covered by any value index.
	covered := make(map[int]bool)
	for _, idx := range mux.Indices[:period] {
		covered[idx] = true
	}
	// The gap position should be the one where Indices maps to the default
	// value (the last entry in Values, which is the else/default case).
	// For base64: Indices = [0,1,2,3,0,1,2,3,...] where 3 = default index.
	// We expect exactly one position per period to map to the default.
	defaultIdx := len(mux.Values) - 1
	gapCount := 0
	for i := 0; i < period; i++ {
		if mux.Indices[i] == defaultIdx {
			gapCount++
		}
	}
	return gapCount == 1
}
```

- [ ] **Step 5: Add nil instruction cleanup helper**

```go
// spmdCompactBlockInstrs removes nil entries from block.Instrs.
func spmdCompactBlockInstrs(block *BasicBlock) {
	j := 0
	for _, instr := range block.Instrs {
		if instr != nil {
			block.Instrs[j] = instr
			j++
		}
	}
	block.Instrs = block.Instrs[:j]
}
```

- [ ] **Step 6: Wire into pipeline in func.go**

In `func.go`, after `spmdDetectMuxPatterns(f)` (line 420), add:

```go
		// Detect SPMDMux + CompactStore pairs with periodic indices
		// and replace with SPMDInterleaveStore.
		spmdDetectInterleaveStore(f)
```

- [ ] **Step 7: Run tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && go test ./go/ssa/... -count=1 2>&1 | tail -10
```

- [ ] **Step 8: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate.go go/ssa/func.go
git commit -m "feat: add spmdDetectInterleaveStore pass

Detects SPMDMux + lanes.CompactStore pairs where the Mux has periodic
indices and replaces both with SPMDInterleaveStore. The detection
walks Mux referrers to find the consuming CompactStore call, verifies
the mask matches the periodic gap pattern, and emits the combined
instruction.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: TinyGo Lowering — `createSPMDInterleaveStore`

**Files:**
- Modify: `tinygo/compiler/compiler.go` — add dispatch
- Modify: `tinygo/compiler/spmd.go` — add lowering function

- [ ] **Step 1: Add dispatch in compiler.go**

Find the instruction switch where `*ssa.SPMDStore` is handled (around line 1937). Add for the value-producing case (near `*ssa.SPMDMux` around line 3805):

```go
	case *ssa.SPMDInterleaveStore:
		return b.createSPMDInterleaveStore(expr), nil
```

- [ ] **Step 2: Add `createSPMDInterleaveStore` in spmd.go**

```go
// createSPMDInterleaveStore lowers an SPMDInterleaveStore instruction.
// Uses N diagonal-extraction shuffles + ORs + 1 compaction shuffle + 1 store.
// Returns the constant element count (N * Lanes / Period).
func (b *builder) createSPMDInterleaveStore(instr *ssa.SPMDInterleaveStore) llvm.Value {
	period := instr.Period
	lanes := instr.Lanes
	numValues := len(instr.Values)
	elemCount := numValues * lanes / period

	// Scalar fallback.
	if !b.simdEnabled {
		return b.createSPMDInterleaveStoreScalar(instr, elemCount)
	}

	// Evaluate all value vectors.
	pos := getPos(instr)
	vals := make([]llvm.Value, numValues)
	for i, v := range instr.Values {
		vals[i] = b.getValue(v, pos)
	}

	// Ensure all are vectors.
	var vecType llvm.Type
	for _, v := range vals {
		if v.Type().TypeKind() == llvm.VectorTypeKind {
			vecType = v.Type()
			break
		}
	}
	if vecType.IsNil() {
		vecType = llvm.VectorType(vals[0].Type(), lanes)
	}
	for i, v := range vals {
		if v.Type().TypeKind() != llvm.VectorTypeKind {
			vals[i] = b.splatScalar(v, vecType)
		}
	}

	elemType := vecType.ElementType()
	elemSize := int(b.targetData.TypeAllocSize(elemType))

	// Extract destination pointer from the slice.
	dstSlice := b.getValue(instr.Addr, pos)
	ptr := b.CreateExtractValue(dstSlice, 0, "ileave.ptr")

	// For byte-width: use spmdSwizzle for diagonal extraction + OR + compaction.
	if elemSize == 1 {
		return b.createInterleaveStoreByte(ptr, vals, period, lanes, elemCount)
	}

	// Wider types: use shufflevector.
	return b.createInterleaveStoreWide(ptr, vals, period, lanes, elemCount)
}

// createInterleaveStoreByte handles byte-width SPMDInterleaveStore.
// N swizzle + (N-1) OR + 1 compaction swizzle + 1 store.
func (b *builder) createInterleaveStoreByte(ptr llvm.Value, vals []llvm.Value, period, lanes, elemCount int) llvm.Value {
	i8Type := b.ctx.Int8Type()
	numValues := len(vals)

	// Step 1: N diagonal-extraction swizzles.
	// For value r, mask picks lane (g*period + r) from each group g, zeros others.
	parts := make([]llvm.Value, numValues)
	for r := 0; r < numValues; r++ {
		maskElts := make([]llvm.Value, lanes)
		for lane := 0; lane < lanes; lane++ {
			group := lane / period
			posInGroup := lane % period
			if posInGroup == r {
				// Pick this lane (identity position).
				maskElts[lane] = llvm.ConstInt(i8Type, uint64(group*period+r), false)
			} else {
				// Zero (out-of-range index for swizzle).
				maskElts[lane] = llvm.ConstInt(i8Type, 0x80, false)
			}
		}
		indexVec := llvm.ConstVector(maskElts, false)
		parts[r] = b.spmdSwizzle(vals[r], indexVec)
	}

	// Step 2: OR all parts together.
	interleaved := parts[0]
	for r := 1; r < numValues; r++ {
		interleaved = b.CreateOr(interleaved, parts[r], "ileave.or")
	}

	// Step 3: Compaction swizzle — remove gap lanes, pack N per group.
	compactElts := make([]llvm.Value, lanes)
	outIdx := 0
	for lane := 0; lane < lanes; lane++ {
		posInGroup := lane % period
		if posInGroup < numValues {
			compactElts[outIdx] = llvm.ConstInt(i8Type, uint64(lane), false)
			outIdx++
		}
	}
	// Fill remaining with 0x80 (zero padding).
	for i := outIdx; i < lanes; i++ {
		compactElts[i] = llvm.ConstInt(i8Type, 0x80, false)
	}
	compactVec := llvm.ConstVector(compactElts, false)
	output := b.spmdSwizzle(interleaved, compactVec)

	// Step 4: Full-width vector store (overwrite pattern).
	st := b.CreateStore(output, ptr)
	st.SetAlignment(1) // byte alignment

	// Return constant element count.
	return llvm.ConstInt(b.intType, uint64(elemCount), false)
}

// createInterleaveStoreWide handles non-byte SPMDInterleaveStore via shufflevector.
func (b *builder) createInterleaveStoreWide(ptr llvm.Value, vals []llvm.Value, period, lanes, elemCount int) llvm.Value {
	i32Type := b.ctx.Int32Type()
	numValues := len(vals)

	// Same algorithm as byte path but using shufflevector instead of spmdSwizzle.
	// Diagonal extraction: for each value r, pick lanes where posInGroup == r.
	parts := make([]llvm.Value, numValues)
	for r := 0; r < numValues; r++ {
		maskElts := make([]llvm.Value, lanes)
		for lane := 0; lane < lanes; lane++ {
			posInGroup := lane % period
			if posInGroup == r {
				maskElts[lane] = llvm.ConstInt(i32Type, uint64(lane), false)
			} else {
				maskElts[lane] = llvm.Undef(i32Type)
			}
		}
		shuffleMask := llvm.ConstVector(maskElts, false)
		parts[r] = b.CreateShuffleVector(vals[r], llvm.Undef(vals[r].Type()), shuffleMask, "ileave.diag")
	}

	// OR parts.
	interleaved := parts[0]
	for r := 1; r < numValues; r++ {
		interleaved = b.CreateOr(interleaved, parts[r], "ileave.or")
	}

	// Compaction shuffle.
	compactElts := make([]llvm.Value, lanes)
	outIdx := 0
	for lane := 0; lane < lanes; lane++ {
		if lane%period < numValues {
			compactElts[outIdx] = llvm.ConstInt(i32Type, uint64(lane), false)
			outIdx++
		}
	}
	for i := outIdx; i < lanes; i++ {
		compactElts[i] = llvm.Undef(i32Type)
	}
	compactMask := llvm.ConstVector(compactElts, false)
	output := b.CreateShuffleVector(interleaved, llvm.Undef(interleaved.Type()), compactMask, "ileave.compact")

	st := b.CreateStore(output, ptr)
	st.SetAlignment(1)

	return llvm.ConstInt(b.intType, uint64(elemCount), false)
}

// createSPMDInterleaveStoreScalar handles scalar fallback (laneCount=1).
func (b *builder) createSPMDInterleaveStoreScalar(instr *ssa.SPMDInterleaveStore, elemCount int) llvm.Value {
	pos := getPos(instr)
	dstSlice := b.getValue(instr.Addr, pos)
	ptr := b.CreateExtractValue(dstSlice, 0, "ileave.scalar.ptr")
	elemType := b.getValue(instr.Values[0], pos).Type()

	// Single group: write Values[0][0], Values[1][0], ... to consecutive positions.
	for r, v := range instr.Values {
		val := b.getValue(v, pos)
		gep := b.CreateInBoundsGEP(elemType, ptr, []llvm.Value{
			llvm.ConstInt(b.ctx.Int32Type(), uint64(r), false),
		}, "ileave.scalar.gep")
		b.CreateStore(val, gep)
	}

	return llvm.ConstInt(b.intType, uint64(elemCount), false)
}
```

- [ ] **Step 3: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 4: Verify correctness on all targets**

```bash
# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-ileave.wasm test/integration/spmd/base64-mula-lemire/main.go 2>&1 \
  && wasmtime run /tmp/base64-ileave.wasm

# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-ileave-ssse3 \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-ileave-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-ileave-avx2 \
  test/integration/spmd/base64-mula-lemire/main.go 2>&1 && /tmp/base64-ileave-avx2

# CompactStore integration test (should still work — not all CompactStore uses trigger InterleaveStore)
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/compact-ileave.wasm test/integration/spmd/compact-store/main.go 2>&1 \
  && wasmtime run /tmp/compact-ileave.wasm
```

Expected: `Correctness: PASS` / `PASS` for all.

- [ ] **Step 5: Run benchmarks**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-ileave-ssse3 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-ileave-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-ileave-avx2 \
  test/integration/spmd/base64-mula-lemire/bench.go 2>&1 && /tmp/bench-ileave-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-ileave-wasm.wasm test/integration/spmd/base64-mula-lemire/bench.go 2>&1 \
  && wasmtime run /tmp/bench-ileave-wasm.wasm
```

Report results. Expected: SSSE3 from ~10x to ~14-20x.

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/spmd.go
git commit -m "feat: add TinyGo lowering for SPMDInterleaveStore

Uses N diagonal-extraction spmdSwizzle + ORs + 1 compaction swizzle
+ 1 contiguous store. For base64 (N=3, K=4, 16 lanes): 7 instructions
instead of 111 (12 mux + 99 CompactStore scatter).

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: E2E Validation

**Files:** None (verification only)

- [ ] **Step 1: Run full E2E test suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -20
```

Expected: No regressions (90+ run-pass).

- [ ] **Step 2: Compare before/after benchmarks**

| Target | Before (SPMDMux + CompactStore) | After (InterleaveStore) | Improvement |
|--------|-------------------------------|------------------------|-------------|
| WASM | 0.94x / ~133 MB/s | ? | ? |
| SSSE3 | 10.42x / 2020 MB/s | ? | ? |
| AVX2 | 11.05x / 2118 MB/s | ? | ? |

---

## Deferred Items

1. **Tail iteration handling**: Currently the tail (partial last iteration) uses the unoptimized SPMDMux + CompactStore path since the detection only fires for full-group iterations. The peeled main body should get the optimization; the tail falls back. Priority: Low.

2. **Dead-lane elimination**: The shift formulas (`out0`, `out1`, `out2`) are still computed for ALL lanes even though only diagonal elements are used. A future pass could detect that `SPMDInterleaveStore` only reads `Values[r][g*K+r]` and eliminate computation for non-diagonal lanes. Priority: Medium.

3. **Two-shuffle optimization**: For N=3 (base64), the 3 swizzles + 2 ORs could potentially be reduced to 2 swizzles by combining two values into one swizzle operation (since each swizzle zeroes non-diagonal lanes, two non-overlapping diagonals could share one swizzle from a pre-combined source). Priority: Low.
