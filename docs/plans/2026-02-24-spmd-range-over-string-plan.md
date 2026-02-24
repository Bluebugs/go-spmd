# SPMD Range-Over-String Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `go for i, c := range str` produce `Varying[rune]` via an inline ASCII fast path + runtime multi-byte fallback in TinyGo's SPMD backend.

**Architecture:** Hybrid two-path strategy. Each iteration loads 4 bytes and checks if all are ASCII (<0x80). Fast path: zero-extend to `<4 x i32>` runes inline. Slow path: call `runtime.stringNextVarying4` which decodes up to 4 runes via `decodeUTF8`. Both paths produce `Varying[rune]` + `Varying[int]` + tail mask, merged via phi nodes before the loop body executes.

**Tech Stack:** TinyGo compiler (Go + LLVM IR), TinyGo runtime (Go), go/ssa (SSA representation), WASM SIMD128.

**Design doc:** `docs/plans/2026-02-24-spmd-range-over-string-design.md`

---

### Task 1: Add `stringNextVarying4` Runtime Helper

**Files:**
- Modify: `tinygo/src/runtime/string.go:153` (after `stringNext`)

**Step 1: Write the runtime function**

Add after `stringNext` (line 153) in `tinygo/src/runtime/string.go`:

```go
// stringNextVarying4 decodes up to 4 runes from s starting at byteOffset.
// Used by the SPMD compiler for vectorized range-over-string loops.
// Returns runes and byte indices as fixed arrays, plus total bytes consumed
// and number of valid runes (for tail masking).
func stringNextVarying4(s string, byteOffset int) (
	runes [4]rune, indices [4]int, byteCount int, runeCount int,
) {
	off := uintptr(byteOffset)
	for lane := 0; lane < 4; lane++ {
		if off >= uintptr(len(s)) {
			break
		}
		indices[lane] = int(off)
		r, length := decodeUTF8(s, off)
		runes[lane] = r
		off += length
		runeCount++
	}
	byteCount = int(off) - byteOffset
	return
}
```

**Step 2: Build TinyGo to verify no syntax errors**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds.

**Step 3: Commit**

```
feat: add stringNextVarying4 runtime helper for SPMD string iteration
```

---

### Task 2: Add `isStringRange` Fields to `spmdActiveLoop`

**Files:**
- Modify: `tinygo/compiler/spmd.go:437-460` (`spmdActiveLoop` struct)

**Step 1: Add new fields after the `isDecomposed` field block (line 454)**

```go
	// Range-over-string specific fields (isStringRange == true):
	// String iteration has variable-length byte strides (1-4 bytes per UTF-8
	// rune), so offset is managed via a stack alloca instead of an SSA phi.
	// The compiler generates an ASCII fast path (inline IR) + multi-byte
	// fallback (runtime.stringNextVarying4) in the body prologue.
	isStringRange bool          // true for rangeiter pattern (range-over-string)
	stringValue   ssa.Value     // the string being ranged over (from *ssa.Range.X)
	nextInstr     *ssa.Next     // the *ssa.Next{IsString:true} in the loop block
	extractKey    *ssa.Extract  // extract #1 from Next result (byte index)
	extractValue  *ssa.Extract  // extract #2 from Next result (rune)
	loopBlock     *ssa.BasicBlock // the rangeiter.loop block (for IR insertion)
	doneBlock     *ssa.BasicBlock // the rangeiter.done block (break/loop-exit target)
```

**Step 2: Build TinyGo**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds (new fields unused but valid).

**Step 3: Commit**

```
feat: add string range fields to spmdActiveLoop struct
```

---

### Task 3: Detect `rangeiter.body` in `analyzeSPMDLoops`

**Files:**
- Modify: `tinygo/compiler/spmd.go:924-931` (before `return state`)
- Test: `tinygo/compiler/spmd_llvm_test.go` (new test)

**Step 1: Write a failing test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDAnalyzeStringRange(t *testing.T) {
	// Test that analyzeSPMDLoops detects rangeiter.body blocks for string ranges.
	// This is a structural test: we verify the detection logic recognizes
	// the *ssa.Next{IsString:true} + Extract pattern.
	//
	// We cannot easily construct a full go/ssa function with rangeiter blocks
	// in a unit test, so this test verifies the new fields are populated
	// correctly via an E2E compile test (Task 7).
	//
	// For now, verify the struct fields exist and the zero values are correct.
	loop := &spmdActiveLoop{}
	if loop.isStringRange {
		t.Error("zero-value isStringRange should be false")
	}
	if loop.stringValue != nil {
		t.Error("zero-value stringValue should be nil")
	}
	if loop.nextInstr != nil {
		t.Error("zero-value nextInstr should be nil")
	}
	if loop.extractKey != nil {
		t.Error("zero-value extractKey should be nil")
	}
	if loop.extractValue != nil {
		t.Error("zero-value extractValue should be nil")
	}
}
```

**Step 2: Run test to verify it passes (this is a structural test)**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMDAnalyzeStringRange -v 2>&1 | tail -10`
Expected: PASS

**Step 3: Write Pass 3 detection logic**

Add before line 926 (`if len(state.activeLoops) == 0`) in `analyzeSPMDLoops`:

```go
	// Third pass: detect range-over-string (rangeiter) patterns.
	//
	// The SSA pattern for "go for i, c := range str" (string):
	//   rangeiter.loop: next = Next(Range(str), IsString=true) → (bool, int, rune)
	//                   ok = extract next #0
	//                   if ok: goto body else done
	//   rangeiter.body: key = extract next #1 (byte index)
	//                   value = extract next #2 (rune)
	//                   ...
	//
	// Unlike rangeint/rangeindex, there is no phi-based iteration variable.
	// The byte offset is managed via a stack alloca in the body prologue.

	for _, block := range b.fn.Blocks {
		if block.Comment != "rangeiter.body" {
			continue
		}

		// The loop block is a predecessor of the body with comment "rangeiter.loop".
		var loopBlock *ssa.BasicBlock
		for _, pred := range block.Preds {
			if pred.Comment == "rangeiter.loop" {
				loopBlock = pred
				break
			}
		}
		if loopBlock == nil {
			continue
		}

		// Check SPMD membership using body block instruction positions.
		var loopInfo *SPMDLoopInfo
		for _, instr := range block.Instrs {
			if pos := instr.Pos(); pos.IsValid() {
				if info := b.isInSPMDLoop(pos); info != nil {
					loopInfo = info
					break
				}
			}
		}
		if loopInfo == nil {
			continue
		}

		// Deduplicate: skip if already claimed by an earlier pass.
		if seenLoopInfo[loopInfo] {
			continue
		}

		// Find the *ssa.Next{IsString:true} instruction in the loop block.
		var nextInstr *ssa.Next
		for _, instr := range loopBlock.Instrs {
			if n, ok := instr.(*ssa.Next); ok && n.IsString {
				nextInstr = n
				break
			}
		}
		if nextInstr == nil {
			continue
		}

		// Extract the *ssa.Range and string value.
		rangeInstr, ok := nextInstr.Iter.(*ssa.Range)
		if !ok {
			continue
		}
		stringValue := rangeInstr.X

		// Find *ssa.Extract #1 (key=byte index) and #2 (value=rune) referrers.
		var extractKey, extractValue *ssa.Extract
		refs := nextInstr.Referrers()
		if refs != nil {
			for _, ref := range *refs {
				if ext, ok := ref.(*ssa.Extract); ok {
					switch ext.Index {
					case 1:
						extractKey = ext
					case 2:
						extractValue = ext
					}
				}
			}
		}
		// At least value (rune) must exist for the loop to be useful.
		if extractValue == nil {
			continue
		}

		seenLoopInfo[loopInfo] = true

		// Find the done block (break target) from the If instruction in loop block.
		var doneBlock *ssa.BasicBlock
		for _, succ := range loopBlock.Succs {
			if succ != block { // successor that isn't the body is the done block
				doneBlock = succ
				break
			}
		}

		loop := &spmdActiveLoop{
			info:          loopInfo,
			laneCount:     4, // rune = int32, 128/32 = 4 lanes
			isStringRange: true,
			stringValue:   stringValue,
			nextInstr:     nextInstr,
			extractKey:    extractKey,
			extractValue:  extractValue,
			loopBlock:     loopBlock,
			doneBlock:     doneBlock,
		}

		// Register with Next instruction as key (no iter phi for string ranges).
		state.activeLoops[nextInstr] = loop
		state.bodyBlocks[block.Index] = loop
		state.loopBlocks[loopBlock.Index] = loop
	}
```

**Step 4: Build and run all existing SPMD tests**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20`
Expected: All existing tests pass, new test passes.

**Step 5: Commit**

```
feat: detect range-over-string pattern in analyzeSPMDLoops
```

---

### Task 4: Suppress Scalar `*ssa.Range` and `*ssa.Next` for String Ranges

**Files:**
- Modify: `tinygo/compiler/compiler.go:3624-3636` (`*ssa.Range` case)
- Modify: `tinygo/compiler/compiler.go:3560-3568` (`*ssa.Next` case)
- Modify: `tinygo/compiler/spmd.go` (new helper `findStringRangeByRange` and `findStringRangeByNext`)

**Step 1: Add helper functions to `spmd.go`**

Add near the `spmdActiveLoop` struct (after line 460):

```go
// findStringRangeByRange checks if a *ssa.Range instruction belongs to an
// SPMD string range loop. Returns the loop if found, nil otherwise.
func (s *spmdLoopState) findStringRangeByRange(rangeInstr *ssa.Range) *spmdActiveLoop {
	if s == nil {
		return nil
	}
	for _, loop := range s.activeLoops {
		if loop.isStringRange && loop.nextInstr != nil {
			if r, ok := loop.nextInstr.Iter.(*ssa.Range); ok && r == rangeInstr {
				return loop
			}
		}
	}
	return nil
}

// findStringRangeByNext checks if a *ssa.Next instruction belongs to an
// SPMD string range loop. Returns the loop if found, nil otherwise.
func (s *spmdLoopState) findStringRangeByNext(nextInstr *ssa.Next) *spmdActiveLoop {
	if s == nil {
		return nil
	}
	if loop, ok := s.activeLoops[nextInstr]; ok && loop.isStringRange {
		return loop
	}
	return nil
}
```

**Step 2: Suppress `*ssa.Range` for SPMD string loops**

In `compiler.go`, modify the `*ssa.Range` case (line 3624). Add before `var iteratorType llvm.Type`:

```go
	case *ssa.Range:
		// SPMD: string range loops manage offset via prologue, skip iterator alloca.
		if b.spmdLoopState.findStringRangeByRange(expr) != nil {
			// Return a dummy value; the real offset is managed by emitSPMDStringPrologue.
			return llvm.Undef(b.ctx.Int8Type()), nil
		}
		var iteratorType llvm.Type
```

**Step 3: Suppress `*ssa.Next` for SPMD string loops**

In `compiler.go`, modify the `*ssa.Next` case (line 3560). Add before `rangeVal := expr.Iter.(*ssa.Range).X`:

```go
	case *ssa.Next:
		// SPMD: string range loops use prologue-generated vectors, skip scalar stringNext.
		if b.spmdLoopState.findStringRangeByNext(expr) != nil {
			// Return a dummy value; extractKey/extractValue come from spmdValueOverride.
			return llvm.Undef(b.getLLVMType(expr.Type())), nil
		}
		rangeVal := expr.Iter.(*ssa.Range).X
```

**Step 4: Build TinyGo**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds.

**Step 5: Run all existing SPMD tests**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20`
Expected: All existing tests still pass (no existing test uses range-over-string).

**Step 6: Commit**

```
feat: suppress scalar Range/Next codegen for SPMD string loops
```

---

### Task 5: Implement `emitSPMDStringPrologue`

**Files:**
- Modify: `tinygo/compiler/spmd.go` (new function after `emitSPMDBodyPrologue`)
- Modify: `tinygo/compiler/compiler.go:1536-1552` (call prologue for string range)

**Step 1: Write `emitSPMDStringPrologue` in `spmd.go`**

Add after `emitSPMDBodyPrologue` (after line 1077):

```go
// emitSPMDStringPrologue generates the SPMD body prologue for range-over-string
// loops. It creates a hybrid two-path structure:
//   - ASCII fast path: load 4 bytes, check all < 0x80, zero-extend to <4 x i32>
//   - Multi-byte path: call runtime.stringNextVarying4 for UTF-8 decoding
//
// Both paths produce Varying[rune] (runes), Varying[int] (byte indices), and a
// tail mask, which are merged via phi nodes. The body executes once per chunk
// with the merged vectors as value overrides.
//
// The byte offset is managed via a stack alloca (not an SSA phi) because string
// iteration advances by variable amounts (1-4 bytes per rune on multi-byte path,
// always 4 on ASCII path).
func (b *builder) emitSPMDStringPrologue(loop *spmdActiveLoop) {
	i32Type := b.ctx.Int32Type()
	i8Type := b.ctx.Int8Type()
	i1Type := b.ctx.Int1Type()
	vec4i32 := llvm.VectorType(i32Type, 4)
	vec4i8 := llvm.VectorType(i8Type, 4)
	vec4i1 := llvm.VectorType(i1Type, 4)

	// Get the LLVM string value (struct {ptr, len}).
	strVal := b.getValue(loop.stringValue, token.NoPos)
	strPtr := b.CreateExtractValue(strVal, 0, "str.ptr")
	strLen := b.CreateExtractValue(strVal, 1, "str.len")

	// The offset alloca was created in the loop preheader (see Task 6).
	// Load the current byte offset.
	offsetVal := b.CreateLoad(i32Type, loop.offsetAlloca, "str.offset")

	// ---- Block: check remaining ----
	remaining := b.CreateSub(strLen, offsetVal, "str.remaining")
	four := llvm.ConstInt(i32Type, 4, false)
	zero := llvm.ConstInt(i32Type, 0, false)

	// Get or create the LLVM basic blocks for the two-path structure.
	currentFunc := b.currentBlock.Parent()
	checkAsciiBlock := llvm.AddBasicBlock(currentFunc, "spmd.str.check")
	asciiBlock := llvm.AddBasicBlock(currentFunc, "spmd.str.ascii")
	multibyteBlock := llvm.AddBasicBlock(currentFunc, "spmd.str.multibyte")
	mergeBlock := llvm.AddBasicBlock(currentFunc, "spmd.str.merge")

	// Branch: remaining < 4 → multibyte, else check ASCII.
	remainingLt4 := b.CreateICmp(llvm.IntSLT, remaining, four, "str.lt4")
	b.CreateCondBr(remainingLt4, multibyteBlock, checkAsciiBlock)

	// ---- Block: check ASCII ----
	b.SetInsertPointAtEnd(checkAsciiBlock)
	bytePtr := b.CreateInBoundsGEP(i8Type, strPtr, []llvm.Value{offsetVal}, "str.chunk.ptr")
	// Load 4 bytes as <4 x i8>.
	chunkPtr := b.CreateBitCast(bytePtr, llvm.PointerType(vec4i8, 0), "str.chunk.vec.ptr")
	chunk := b.CreateLoad(vec4i8, chunkPtr, "str.chunk")
	// Check if all bytes < 0x80: AND with 0x80 splat, then check all zero.
	highBitMask := b.splatScalar(llvm.ConstInt(i8Type, 0x80, false), vec4i8)
	highBits := b.CreateAnd(chunk, highBitMask, "str.highbits")
	highBitsZero := b.CreateICmp(llvm.IntEQ, highBits, llvm.ConstNull(vec4i8), "str.ascii.check")
	// Reduce: all lanes must be zero (all bytes are ASCII).
	// Use bitcast to i32 + icmp for WASM-friendly all_true check.
	highBitsI32 := b.CreateBitCast(highBits, i32Type, "str.highbits.i32")
	allAscii := b.CreateICmp(llvm.IntEQ, highBitsI32, llvm.ConstInt(i32Type, 0, false), "str.all.ascii")
	b.CreateCondBr(allAscii, asciiBlock, multibyteBlock)

	// ---- Block: ASCII fast path ----
	b.SetInsertPointAtEnd(asciiBlock)
	// Zero-extend <4 x i8> to <4 x i32> for Varying[rune].
	asciiRunes := b.CreateZExt(chunk, vec4i32, "str.ascii.runes")
	// Build index vector: <offset, offset+1, offset+2, offset+3>.
	offsetSplat := b.splatScalar(offsetVal, vec4i32)
	laneOffset := b.spmdLaneOffsetConst(4, i32Type)
	asciiIndices := b.CreateAdd(offsetSplat, laneOffset, "str.ascii.indices")
	// Tail mask: indices < strLen (handles last chunk of string).
	strLenSplat := b.splatScalar(strLen, vec4i32)
	asciiTailI1 := b.CreateICmp(llvm.IntSLT, asciiIndices, strLenSplat, "str.ascii.tail")
	asciiTail := b.spmdWrapMask(asciiTailI1, 4)
	// Next offset: offset + 4.
	asciiNextOffset := b.CreateAdd(offsetVal, four, "str.ascii.next.off")
	b.CreateBr(mergeBlock)

	// ---- Block: multi-byte fallback ----
	b.SetInsertPointAtEnd(multibyteBlock)
	// Call runtime.stringNextVarying4(str, offset) → {[4]rune, [4]int, int, int}
	result := b.createRuntimeCall("stringNextVarying4", []llvm.Value{strVal, offsetVal}, "str.mb")
	// Unpack: runes = result[0] ([4]rune = [4]i32), indices = result[1] ([4]int = [4]i32)
	mbRunesArr := b.CreateExtractValue(result, 0, "str.mb.runes.arr")
	mbIndicesArr := b.CreateExtractValue(result, 1, "str.mb.indices.arr")
	mbByteCount := b.CreateExtractValue(result, 2, "str.mb.bytecount")
	mbRuneCount := b.CreateExtractValue(result, 3, "str.mb.runecount")
	// Convert [4]i32 arrays to <4 x i32> vectors via insert/extract.
	mbRunes := b.arrayToVector(mbRunesArr, 4, i32Type, "str.mb.runes")
	mbIndices := b.arrayToVector(mbIndicesArr, 4, i32Type, "str.mb.indices")
	// Tail mask: lane index < runeCount.
	mbRuneCountSplat := b.splatScalar(mbRuneCount, vec4i32)
	mbTailI1 := b.CreateICmp(llvm.IntSLT, laneOffset, mbRuneCountSplat, "str.mb.tail")
	mbTail := b.spmdWrapMask(mbTailI1, 4)
	// Next offset: offset + byteCount.
	mbNextOffset := b.CreateAdd(offsetVal, mbByteCount, "str.mb.next.off")
	b.CreateBr(mergeBlock)

	// ---- Block: merge ----
	b.SetInsertPointAtEnd(mergeBlock)
	// Phi nodes to merge ASCII and multi-byte paths.
	runesPhi := b.CreatePHI(vec4i32, "str.runes")
	runesPhi.AddIncoming([]llvm.Value{asciiRunes, mbRunes},
		[]llvm.BasicBlock{asciiBlock, multibyteBlock})
	indicesPhi := b.CreatePHI(vec4i32, "str.indices")
	indicesPhi.AddIncoming([]llvm.Value{asciiIndices, mbIndices},
		[]llvm.BasicBlock{asciiBlock, multibyteBlock})
	maskPhi := b.CreatePHI(asciiTail.Type(), "str.mask")
	maskPhi.AddIncoming([]llvm.Value{asciiTail, mbTail},
		[]llvm.BasicBlock{asciiBlock, multibyteBlock})
	nextOffsetPhi := b.CreatePHI(i32Type, "str.next.off")
	nextOffsetPhi.AddIncoming([]llvm.Value{asciiNextOffset, mbNextOffset},
		[]llvm.BasicBlock{asciiBlock, multibyteBlock})

	// Store the next offset for the next iteration.
	b.CreateStore(nextOffsetPhi, loop.offsetAlloca)

	// Set loop state for body execution.
	loop.laneIndices = indicesPhi
	loop.tailMask = maskPhi

	// Set value overrides so Extract #1 and #2 resolve to our vectors.
	if loop.extractKey != nil {
		b.spmdValueOverride[loop.extractKey] = indicesPhi
	}
	b.spmdValueOverride[loop.extractValue] = runesPhi
	// Also override the Next instruction itself so Extract on it works.
	b.spmdValueOverride[loop.nextInstr] = llvm.Undef(b.getLLVMType(loop.nextInstr.Type()))
}

// arrayToVector converts an LLVM [N]T aggregate to a <N x T> vector using
// extractvalue + insertelement.
func (b *builder) arrayToVector(arr llvm.Value, n int, elemType llvm.Type, name string) llvm.Value {
	vecType := llvm.VectorType(elemType, n)
	vec := llvm.Undef(vecType)
	for i := 0; i < n; i++ {
		elem := b.CreateExtractValue(arr, i, name+"."+strconv.Itoa(i))
		vec = b.CreateInsertElement(vec, elem, llvm.ConstInt(b.ctx.Int32Type(), uint64(i), false), "")
	}
	return vec
}
```

**Step 2: Add `offsetAlloca` field to `spmdActiveLoop`**

In the struct definition (after `doneBlock` added in Task 2), add:

```go
	offsetAlloca llvm.Value    // stack alloca for byte offset (i32), created in loop preheader
```

**Step 3: Wire up the prologue call in `compiler.go`**

Modify `compiler.go` at line 1543 (inside the body block entry). Change:

```go
			if loop.isRangeIndex {
```

To:

```go
			if loop.isStringRange {
				b.emitSPMDStringPrologue(loop)
				b.spmdMaskStack = []llvm.Value{loop.tailMask}
			} else if loop.isRangeIndex {
```

**Step 4: Build TinyGo**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds.

**Step 5: Run all existing SPMD tests**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20`
Expected: All existing tests still pass.

**Step 6: Commit**

```
feat: implement emitSPMDStringPrologue with ASCII fast path
```

---

### Task 6: Create Offset Alloca in Loop Preheader

**Files:**
- Modify: `tinygo/compiler/compiler.go:1536-1552` (body block entry, before prologue call)
- Modify: `tinygo/compiler/compiler.go:2087-2183` (`*ssa.If` case — suppress ok-check)

**Step 1: Create offset alloca when first visiting the loop block**

The offset alloca must exist before the body prologue runs. The cleanest place is when visiting the loop block in DomPreorder. Add to `compiler.go` in the block loop (around line 1537), after the `if b.spmdLoopState != nil` check:

In the DomPreorder block loop (line 1528), add handling for loop blocks. After line 1534 (`b.SetInsertPointAtEnd(b.currentBlockInfo.entry)`), before the existing SPMD body block check:

```go
		// SPMD: create offset alloca for string range loops when visiting the loop block.
		if b.spmdLoopState != nil {
			if loop, isLoop := b.spmdLoopState.loopBlocks[block.Index]; isLoop && loop.isStringRange && loop.offsetAlloca.IsNil() {
				// Insert alloca at function entry (not in loop block) for LLVM's mem2reg.
				entryBlock := b.fn.DomPreorder()[0]
				savedBlock := b.GetInsertBlock()
				b.SetInsertPointBefore(b.blockInfo[entryBlock.Index].entry.FirstInstruction())
				loop.offsetAlloca = b.CreateAlloca(b.ctx.Int32Type(), "str.offset.alloca")
				b.CreateStore(llvm.ConstInt(b.ctx.Int32Type(), 0, false), loop.offsetAlloca)
				b.SetInsertPointAtEnd(savedBlock)
			}
		}
```

**Step 2: Suppress the `*ssa.If` for the `extract #0` (ok check)**

In the `*ssa.If` case in `compiler.go` (line 2087), add early in the case, before the existing checks. The ok-check `If` uses `extract #0` from the `*ssa.Next` as its condition. When the Next belongs to a string range loop, the prologue handles loop termination via `remaining <= 0`, so the If should just fall through to the body:

```go
	case *ssa.If:
		// SPMD: suppress the ok-check If for string range loops.
		// The prologue handles loop termination via the remaining <= 0 check.
		if b.spmdLoopState != nil {
			if ext, ok := instr.Cond.(*ssa.Extract); ok && ext.Index == 0 {
				if n, ok := ext.Tuple.(*ssa.Next); ok && n.IsString {
					if b.spmdLoopState.findStringRangeByNext(n) != nil {
						// The prologue already branched to done or body.
						// Emit an unconditional branch to the body block (Succs[0]).
						b.CreateBr(b.blockInfo[block.Succs[0].Index].entry)
						continue
					}
				}
			}
		}
```

**Step 3: Build TinyGo**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds.

**Step 4: Run all existing SPMD tests**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20`
Expected: All existing tests still pass.

**Step 5: Commit**

```
feat: create offset alloca and suppress ok-check for SPMD string range
```

---

### Task 7: Handle Loop Termination — Redirect Loop Block to Done

**Files:**
- Modify: `tinygo/compiler/compiler.go` (DomPreorder loop block handling)

The original SSA has this control flow in the loop block:
```
rangeiter.loop → (ok check) → body / done
```

But our prologue replaces this: the body prologue itself branches to `done` when `remaining <= 0`. So when visiting the loop block in DomPreorder, we need to:
1. Load the offset
2. Check `remaining <= 0`
3. Branch to done or fall through to the body prologue

**Step 1: Add loop block IR generation for string ranges**

This should happen in the DomPreorder block loop, when visiting a loop block that belongs to a string range. The offset alloca was created in Task 6. Now the loop block needs to emit the termination check.

In `compiler.go`, in the block where we create the offset alloca (Task 6), add after alloca creation, inside the `isLoop && loop.isStringRange` check:

Actually, the loop block check happens every iteration (not just the first time). Restructure: the alloca creation uses `loop.offsetAlloca.IsNil()` as a guard. The termination check should happen on every visit to the loop block.

Add to the DomPreorder block loop, right after the offset alloca creation code from Task 6:

```go
		// SPMD: emit loop termination check for string range loops.
		if b.spmdLoopState != nil {
			if loop, isLoop := b.spmdLoopState.loopBlocks[block.Index]; isLoop && loop.isStringRange {
				// Load current offset and check if we've consumed the entire string.
				i32Type := b.ctx.Int32Type()
				strVal := b.getValue(loop.stringValue, token.NoPos)
				strLen := b.CreateExtractValue(strVal, 1, "str.len")
				offset := b.CreateLoad(i32Type, loop.offsetAlloca, "str.off.check")
				exhausted := b.CreateICmp(llvm.IntSGE, offset, strLen, "str.exhausted")
				// Branch to done block if string is exhausted.
				doneEntry := b.blockInfo[loop.doneBlock.Index].entry
				bodyEntry := b.blockInfo[b.spmdLoopState.bodyBlockForLoop(loop).Index].entry
				b.CreateCondBr(exhausted, doneEntry, bodyEntry)
				// Mark that we've handled this block's terminator — skip normal instruction compilation.
				loop.loopBlockHandled = true
			}
		}
```

This requires:
- A new `loopBlockHandled bool` field on `spmdActiveLoop`
- A `bodyBlockForLoop` helper on `spmdLoopState`
- Skip normal instruction compilation for the loop block when `loopBlockHandled` is true

**Step 2: Add helper and field**

In `spmd.go`, add to `spmdActiveLoop`:
```go
	loopBlockHandled bool          // true after loop block IR is emitted (skip normal compilation)
```

Add to `spmdLoopState`:
```go
// bodyBlockForLoop returns the body block SSA block for a given loop.
func (s *spmdLoopState) bodyBlockForLoop(loop *spmdActiveLoop) *ssa.BasicBlock {
	for idx, l := range s.bodyBlocks {
		if l == loop {
			// Find the SSA block with this index.
			for _, b := range s.bodyBlocks {
				_ = b
			}
			// We stored the block index, need the actual SSA block.
			// This is available from the loop's fields.
			break
		}
		_ = idx
	}
	return nil
}
```

Actually, this is getting complex. Simpler approach: store `bodyBlock *ssa.BasicBlock` on `spmdActiveLoop` during detection (Task 3), alongside `loopBlock` and `doneBlock`. Then the redirect is simply `b.blockInfo[loop.bodyBlock.Index].entry`.

Revise: In Task 3, also add:
```go
	bodyBlock *ssa.BasicBlock // the rangeiter.body block
```
And set it during detection:
```go
	loop := &spmdActiveLoop{
		...
		bodyBlock: block, // the rangeiter.body block we found
	}
```

Then the termination redirect becomes:
```go
	bodyEntry := b.blockInfo[loop.bodyBlock.Index].entry
	b.CreateCondBr(exhausted, doneEntry, bodyEntry)
```

**Step 3: Skip normal instruction compilation for handled loop blocks**

In the DomPreorder instruction loop (around line 1580 in compiler.go), add a check to skip all instructions in the loop block when it's been handled:

```go
		// SPMD: skip instruction compilation for string range loop blocks
		// (termination check was emitted directly in the block preamble).
		if b.spmdLoopState != nil {
			if loop, isLoop := b.spmdLoopState.loopBlocks[block.Index]; isLoop && loop.isStringRange {
				continue // skip to next block
			}
		}
```

This skips compiling the `*ssa.Phi`, `*ssa.Next`, `*ssa.Extract`, and `*ssa.If` that normally live in the loop block — all of which are handled by the prologue and suppression code.

**Step 4: Build and test**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20`
Expected: All existing tests still pass.

**Step 5: Commit**

```
feat: handle loop termination and block skipping for SPMD string range
```

---

### Task 8: Handle `*ssa.Extract` Override Propagation

**Files:**
- Modify: `tinygo/compiler/compiler.go:3182-3187` (`*ssa.Extract` case)

The existing `*ssa.Extract` code at line 3161-3163 already propagates `spmdValueOverride` for `*ssa.Index`. But for string ranges, the Extract is on a `*ssa.Next` tuple, not an `*ssa.Index`. The override is set directly on `extractKey` and `extractValue` (not on the Next instruction), so `getValue()` should already resolve them via `spmdValueOverride`.

However, there's a subtlety: if user code uses `extractKey` or `extractValue` in a context that doesn't go through `getValue()` first (e.g., as a BinOp operand that's compiled before the override is checked), we need to ensure the Extract case itself checks for overrides.

**Step 1: Verify existing override propagation works**

The `*ssa.Extract` case (line 3182) calls `b.getValue(expr.Tuple, ...)` which for a suppressed `*ssa.Next` returns `Undef`. Then `CreateExtractValue(undef, index, "")` would return an undef element. But our overrides are set directly on the Extract SSA values, so `getValue(extractKey)` should return the override before reaching `createExpr`.

Check: `getValue()` (line 2893) checks `spmdValueOverride` first. If `extractKey` is in the override map, it returns the override vector directly and never calls `createExpr`. This is correct.

**Step 2: Handle the case where Extract #0 (ok) is used elsewhere**

The `extract #0` (ok bool) is used by the `*ssa.If` which we suppress in Task 6. But if any other instruction references it (unlikely but possible), we should override it too. Add in `emitSPMDStringPrologue` (Task 5):

```go
	// Override extract #0 (ok) with a constant true — the prologue ensures
	// we only enter the body when there are runes to process.
	if refs := loop.nextInstr.Referrers(); refs != nil {
		for _, ref := range *refs {
			if ext, ok := ref.(*ssa.Extract); ok && ext.Index == 0 {
				b.spmdValueOverride[ext] = llvm.ConstInt(i1Type, 1, false)
			}
		}
	}
```

**Step 3: Build and test**

Run: `cd /home/cedric/work/SPMD/tinygo && make GO=/home/cedric/work/SPMD/go/bin/go 2>&1 | tail -5`
Expected: Build succeeds, all tests pass.

**Step 4: Commit**

```
feat: override extract #0 (ok) for SPMD string range loops
```

---

### Task 9: E2E Test — ASCII String

**Files:**
- Create: `examples/string-range/main.go`

**Step 1: Write the E2E test example**

```go
// Range-over-string using SPMD Go (ASCII fast path test)
package main

import "fmt"

func main() {
	// Test 1: Pure ASCII string
	s := "Hello, World!"
	fmt.Printf("Input: %q (len=%d)\n", s, len(s))
	countRunes(s)

	// Test 2: Empty string
	fmt.Printf("Input: %q (len=%d)\n", "", 0)
	countRunes("")

	// Test 3: Exactly 4 chars (one full chunk)
	s3 := "abcd"
	fmt.Printf("Input: %q (len=%d)\n", s3, len(s3))
	countRunes(s3)

	// Test 4: 5 chars (one full chunk + 1 tail)
	s4 := "abcde"
	fmt.Printf("Input: %q (len=%d)\n", s4, len(s4))
	countRunes(s4)
}

func countRunes(s string) {
	count := 0
	go for _, c := range s {
		_ = c
		count++
	}
	fmt.Printf("  Runes: %d\n", count)
}
```

Wait — `count++` inside a `go for` loop involves a uniform variable being modified per-lane, which is a reduction. This is tricky. Let's use a simpler pattern that outputs per-rune data:

```go
// Range-over-string using SPMD Go (ASCII fast path test)
package main

import "fmt"

func main() {
	s := "Hello!"
	toUpperStr(s)
}

func toUpperStr(s string) {
	result := make([]byte, len(s))
	go for i, c := range s {
		if 'a' <= c && c <= 'z' {
			result[i] = byte(c - ('a' - 'A'))
		} else {
			result[i] = byte(c)
		}
	}
	fmt.Printf("'%s' -> '%s'\n", s, string(result))
}
```

Actually, this has a problem too: `result[i]` where `i` is `Varying[int]` (byte index) and `result` is `[]byte`. This is a scatter store (varying index into a byte slice), which should work with the existing gather/scatter infrastructure.

Let's keep it simple for the first test:

```go
// Range-over-string using SPMD Go
package main

import "fmt"

func main() {
	// ASCII string: each char is 1 byte = 1 rune
	s := "Hello, World!"
	result := make([]byte, len(s))
	go for i, c := range s {
		if 'a' <= c && c <= 'z' {
			result[i] = byte(c - ('a' - 'A'))
		} else {
			result[i] = byte(c)
		}
	}
	fmt.Printf("%s -> %s\n", s, string(result))
}
```

Expected output: `Hello, World! -> HELLO, WORLD!`

**Step 2: Compile the example**

Run: `WASMOPT=/tmp/wasm-opt GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go /home/cedric/work/SPMD/tinygo/build/tinygo build -target=wasi -o /tmp/string-range.wasm /home/cedric/work/SPMD/examples/string-range/main.go 2>&1`
Expected: Compiles successfully.

**Step 3: Run the example**

Run: `wasmtime run /tmp/string-range.wasm 2>&1` (or Node.js runner)
Expected: `Hello, World! -> HELLO, WORLD!`

**Step 4: Add to E2E test script**

Add a test entry in `test/e2e/spmd-e2e-test.sh` under the appropriate level:
```bash
test_compile_and_run "string-range" "$SPMD_ROOT/examples/string-range/main.go" \
    "Hello, World! -> HELLO, WORLD!"
```

**Step 5: Commit**

```
feat: add string-range E2E test for SPMD range-over-string
```

---

### Task 10: E2E Test — Multi-byte UTF-8 String

**Files:**
- Modify: `examples/string-range/main.go` (add multi-byte test case)

**Step 1: Add multi-byte test**

Add to `main()`:

```go
	// Multi-byte UTF-8: mix of ASCII and multi-byte characters
	s2 := "café"  // 'é' is 2 bytes (0xC3 0xA9)
	result2 := make([]byte, 0, len(s2))
	go for _, c := range s2 {
		if c < 128 {
			result2 = append(result2, byte(c))
		} else {
			result2 = append(result2, '?')
		}
	}
	fmt.Printf("%s -> %s\n", s2, string(result2))
```

Wait — `append` inside `go for` is problematic (varying length mutation). Let's use a fixed-size output and write by index:

```go
	// Multi-byte UTF-8: "café" has 4 runes but 5 bytes
	s2 := "café"
	count := 0
	go for _, c := range s2 {
		_ = c
		count++
	}
	// count will be wrong in SPMD (reduction issue). Use print per-lane instead.
```

Reductions in `go for` are not supported without `reduce.*`. Let's use a different approach — print each rune's info to verify correctness:

Actually, the simplest correct test is to have the loop body write to a pre-allocated output buffer at each byte index:

```go
	// Multi-byte: "café" = 4 runes, 5 bytes (é = 2 bytes)
	s2 := "café"
	ascii2 := make([]byte, len(s2)) // len(s2)=5
	go for i, c := range s2 {
		if c < 128 {
			ascii2[i] = byte(c)
		} else {
			ascii2[i] = '?'
		}
	}
	// Note: byte indices are 0,1,2,3 (c,a,f,é) — index 3 for é, index 4 unused
	fmt.Printf("%s -> [%d]%q\n", s2, len(ascii2), string(ascii2))
```

This is getting complex with the byte index vs rune index. Let me think about what makes a clean test...

The cleanest test: write runes to a `[]rune` slice using an auxiliary counter. But we don't have `Varying[rune]` slice support.

Simplest approach: just verify it compiles and doesn't crash. Use a body that performs a simple varying operation on each rune:

```go
func main() {
	test("Hello!", "HELLO!")
	test("abc", "ABC")
	test("café", "CAFé")  // only ASCII chars uppercased
}

func test(input, expected string) {
	result := make([]byte, len(input))
	go for i, c := range input {
		if 'a' <= c && c <= 'z' {
			result[i] = byte(c - ('a' - 'A'))
		} else {
			result[i] = byte(c)
		}
	}
	fmt.Printf("%s -> %s (expected: %s)\n", input, string(result), expected)
}
```

For "café": byte indices are [0,1,2,3,4], runes are ['c','a','f','é']. The loop writes `result[0]='C'`, `result[1]='A'`, `result[2]='F'`, `result[3]=0xC3` (first byte of é), but wait — `byte(c)` where c='é' (0xE9) truncates to 0xE9. And `result[3]` gets 0xE9 while `result[4]` is untouched (0). This doesn't match the simple expected output.

Let me just use ASCII-only strings for the first test and defer multi-byte to a separate, more careful test. The ASCII fast path is the main feature; multi-byte correctness can be verified once the basic infrastructure works.

Final E2E test for Task 9:

```go
package main

import "fmt"

func main() {
	test("Hello, World!", "HELLO, WORLD!")
	test("abcd", "ABCD")
	test("abcde", "ABCDE")
	test("Go!", "GO!")
}

func test(input, expected string) {
	result := make([]byte, len(input))
	go for i, c := range input {
		if 'a' <= c && c <= 'z' {
			result[i] = byte(c - ('a' - 'A'))
		} else {
			result[i] = byte(c)
		}
	}
	actual := string(result)
	if actual == expected {
		fmt.Printf("OK: %s -> %s\n", input, actual)
	} else {
		fmt.Printf("FAIL: %s -> %s (expected %s)\n", input, actual, expected)
	}
}
```

Expected output:
```
OK: Hello, World! -> HELLO, WORLD!
OK: abcd -> ABCD
OK: abcde -> ABCDE
OK: Go! -> GO!
```

**Step 2: Compile and run, verify output matches**

**Step 3: Commit**

```
feat: add string-range E2E example for SPMD range-over-string
```

---

### Task 11: Run Full E2E Suite — Regression Check

**Files:** None (verification only)

**Step 1: Run the full E2E test suite**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -30`
Expected: All previously passing tests still pass. `string-range` test passes (if added to script).

**Step 2: Run TinyGo unit tests**

Run: `cd /home/cedric/work/SPMD/tinygo && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -30`
Expected: All 107+ tests pass.

**Step 3: Commit (if any fixes needed)**

---

### Summary

| Task | Component | Files |
|------|-----------|-------|
| 1 | Runtime helper | `tinygo/src/runtime/string.go` |
| 2 | Struct fields | `tinygo/compiler/spmd.go` |
| 3 | SSA detection (Pass 3) | `tinygo/compiler/spmd.go` |
| 4 | Suppress Range/Next | `tinygo/compiler/compiler.go`, `tinygo/compiler/spmd.go` |
| 5 | String prologue | `tinygo/compiler/spmd.go`, `tinygo/compiler/compiler.go` |
| 6 | Offset alloca + ok-check | `tinygo/compiler/compiler.go` |
| 7 | Loop termination | `tinygo/compiler/compiler.go`, `tinygo/compiler/spmd.go` |
| 8 | Extract override | `tinygo/compiler/spmd.go` |
| 9 | E2E test (ASCII) | `examples/string-range/main.go` |
| 10 | Multi-byte E2E (deferred) | Future work |
| 11 | Regression check | Verification only |

### Dependencies

```
Task 1 (runtime) ─────────────────────────────────┐
Task 2 (struct fields) ──→ Task 3 (detection) ──→ Task 4 (suppress) ──→ Task 5 (prologue) ──→ Task 6 (alloca + ok) ──→ Task 7 (loop term) ──→ Task 8 (extract) ──→ Task 9 (E2E) ──→ Task 11 (regression)
```

Task 1 is independent and can be done in parallel with Tasks 2-3.
