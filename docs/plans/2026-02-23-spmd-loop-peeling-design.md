# SPMD Loop Peeling Design

Split every `go for` SPMD loop into a **main loop** (all lanes active, plain stores) and a **tail** (0-1 masked iterations). Eliminates 73% of loop body instructions caused by per-lane masked store scalarization on WASM SIMD128.

## Motivation

WASM SIMD128 has no native masked store instruction. LLVM lowers `llvm.masked.store.v16i8` into 16 conditional `v128.store8_lane` operations (129 instructions). For hex-encode, this is 73% of the loop body, making SPMD 0.36x scalar speed.

When the mask is `ConstAllOnes` (`<16 x i1> splat(true)`), LLVM optimizes `llvm.masked.store` to a plain `v128.store` (1 instruction). Loop peeling ensures the main loop always has an all-ones mask.

## Control Flow Structure

**Before (current)**:
```
Entry → Body (tail mask every iter) → Loop (iter < bound) → Body or Exit
```

**After (peeled)**:
```
Entry:
  alignedBound = bound & ~(laneCount - 1)
  br i1 (alignedBound > 0), %Main.Body, %Tail.Check

Main.Body:
  mainIter = phi [Entry: 0, Main.Loop: mainNext]
  tailMask = ConstAllOnes                   // no computation
  ... body instructions (plain v128.store) ...

Main.Loop:
  mainNext = mainIter + laneCount
  br i1 (mainNext < alignedBound), %Main.Body, %Tail.Check

Tail.Check:
  tailIter = phi [Entry: 0, Main.Loop: mainNext]
  br i1 (tailIter < bound), %Tail.Body, %Exit

Tail.Body:
  tailMask = computed(tailIter, bound, laneCount)
  ... body instructions (masked stores) ...
  br %Exit

Exit:
  ... blocks after the loop ...
```

Properties:
- Main loop uses `alignedBound` as exit condition → all iterations have full vectors
- Tail runs at most once (remaining 0 to laneCount-1 elements)
- For inputs divisible by laneCount, tail never executes
- `laneCount` is always a power of 2 on WASM, so `& ~(laneCount-1)` is correct

## Implementation Architecture

### New Struct: `spmdPeeledLoop`

```go
type spmdPeeledLoop struct {
    loop           *spmdActiveLoop
    alignedBound   llvm.Value          // bound & ~(laneCount-1)
    tailCheckBlock llvm.BasicBlock     // phi + branch to tail.body or exit
    tailBlockInfo  map[int]blockInfo   // SSA block index → tail LLVM blocks
    tailExitBlock  llvm.BasicBlock     // convergence point
    tailIterPhi    llvm.Value          // phi in tailCheck for the iter value
}
```

### Phase Enum

```go
type spmdLoopPhase int
const (
    spmdLoopPhaseMain spmdLoopPhase = iota
    spmdLoopPhaseTail
)
```

### Block Creation (compiler.go, function setup)

After the existing LLVM block creation loop (line 1314-1325), for each SPMD loop:

1. Create `tail.check` block
2. For each SSA block that is a body block or interior block of this SPMD loop, create a corresponding `blockname.tail` LLVM block
3. Create `tail.exit` block (or reuse the original loop exit block)
4. Store in `spmdPeeledLoop.tailBlockInfo`

### Modified DomPreorder Processing

When visiting an SPMD body block during DomPreorder (line 1508-1679):

**Main phase** (the default DomPreorder pass):
- `emitSPMDBodyPrologue` sets `loop.tailMask = ConstAllOnes`
- `b.spmdMaskStack = [ConstAllOnes]`
- All instructions processed normally into main LLVM blocks
- Stores see all-ones mask → `llvm.masked.store` with `splat(true)` → LLVM optimizes

**After DomPreorder completes**, for each peeled loop:
1. Save builder state (locals, overrides, decomposed, mask stack, contiguous/shifted maps)
2. Emit `alignedBound` computation and main→tail branch in main loop block
3. Emit `Tail.Check` phi + branch
4. Switch `b.blockInfo` to `tailBlockInfo`
5. Re-process body blocks in DomPreorder order with `spmdLoopPhaseTail`:
   - `emitSPMDBodyPrologue` computes real tail mask
   - `b.spmdMaskStack = [computedTailMask]`
   - Interior blocks (if.then/else/done) get tail mask transitions
   - Stores see computed mask → per-lane masked stores
6. Emit tail body exit branch to `Exit`
7. Restore builder state and `b.blockInfo`

### Body Block Re-emission Function

```go
func (b *builder) emitSPMDTailBody(peeled *spmdPeeledLoop) {
    loop := peeled.loop

    // Save state
    savedLocals := b.locals
    savedOverride := b.spmdValueOverride
    savedDecomposed := b.spmdDecomposed
    savedContiguous := b.spmdContiguousPtr
    savedShifted := b.spmdShiftedPtr
    savedBlockInfo := b.blockInfo

    // Clone locals: keep only values defined outside the SPMD body
    b.locals = make(map[ssa.Value]llvm.Value)
    for k, v := range savedLocals {
        if !b.isValueInSPMDBody(k, loop) {
            b.locals[k] = v
        }
    }

    // Swap block info to tail blocks
    for idx, info := range peeled.tailBlockInfo {
        b.blockInfo[idx] = info
    }

    // Fresh SPMD maps for tail phase
    b.spmdValueOverride = make(map[ssa.Value]llvm.Value)
    b.spmdDecomposed = make(map[ssa.Value]*spmdDecomposedIndex)
    b.spmdContiguousPtr = make(map[ssa.Value]*spmdContiguousInfo)
    b.spmdShiftedPtr = make(map[ssa.Value]*spmdShiftedLoadInfo)

    // Process body blocks in DomPreorder order
    for _, block := range b.fn.DomPreorder() {
        if b.isBlockInSPMDBody(block) != loop {
            continue
        }
        b.currentBlock = block
        b.currentBlockInfo = &b.blockInfo[block.Index]
        b.SetInsertPointAtEnd(b.currentBlockInfo.entry)

        // Handle phis, prologue, mask transitions, instructions
        // (mirrors the DomPreorder logic but uses tail mask)
        b.emitSPMDBlockForPhase(block, loop, spmdLoopPhaseTail)
    }

    // Restore state
    b.locals = savedLocals
    b.spmdValueOverride = savedOverride
    b.spmdDecomposed = savedDecomposed
    b.spmdContiguousPtr = savedContiguous
    b.spmdShiftedPtr = savedShifted
    b.blockInfo = savedBlockInfo
}
```

### Phi Handling in Tail

The tail body runs once (not a loop), so the iterator phi has one incoming edge:

- **rangeint**: Body has `iterPhi = phi [entry: 0, loop: next]`. In tail, override to `tailIterPhi` from `Tail.Check` (the value of `mainIter` at main loop exit).
- **rangeindex**: Similar — the loop phi gets the tail iter value.

Implementation: In `emitSPMDBodyPrologue` during tail phase, use `peeled.tailIterPhi` as the scalar base instead of the SSA phi's LLVM value.

### Loop Block Modification

The SSA loop block contains the increment (`iter + 1` → `iter + laneCount`) and bounds check (`incr < bound`).

**Main phase**: Change bounds check to `incr < alignedBound`. On exit, branch to `Tail.Check`.

**Tail phase**: The loop block is NOT emitted (tail doesn't loop). The tail body's terminal instruction branches directly to `Exit`.

### Branch Rewiring

For the main phase, the `*ssa.If` in the loop block normally branches to `body` (true) or `exit` (false). We intercept this:
- True target: main body block (unchanged)
- False target: `Tail.Check` block (instead of original exit)

For the tail phase, the terminal `*ssa.Jump` or `*ssa.If` at the end of the last body block branches to `Exit` instead of the loop block.

## Mask Flow

### Main Phase

```
emitSPMDBodyPrologue:
  loop.tailMask = ConstAllOnes(maskType)
  b.spmdMaskStack = [ConstAllOnes]

Varying if/else inside body:
  thenMask = ConstAllOnes & condition = condition
  elseMask = ConstAllOnes & ~condition = ~condition
  (LLVM optimizes these naturally)

Store coalescing:
  selected = select(cond, thenVal, elseVal)
  spmdMaskedStore(selected, ptr, ConstAllOnes)
  → llvm.masked.store with <16 x i1> splat(true)
  → LLVM optimizes to: store <16 x i8>, ptr

Shifted load:
  result = narrowLoad + shuffle
  select(ConstAllOnes, result, zero) → LLVM folds to result
```

### Tail Phase

Same as current behavior — computed tail mask flows through mask stack, stores use real mask, LLVM generates per-lane conditional stores.

## Edge Cases

### bound < laneCount
`alignedBound = 0`, main loop skipped, tail handles all elements in one masked iteration.

### bound == 0
`alignedBound = 0`, main loop skipped, tail check `0 < 0 = false`, exit immediately.

### bound exactly divisible by laneCount
`alignedBound = bound`, main loop handles everything, tail check `bound < bound = false`, tail skipped.

### Varying if/else inside body
Works in both phases. Main phase: base mask is all-ones, narrowed masks are `condition`/`~condition`. Tail phase: base mask is computed, narrowed masks are `tailMask & condition`/`tailMask & ~condition`.

### Store coalescing
Works in both phases. The coalesced store uses `spmdParentMask()` which returns the base mask (all-ones in main, computed in tail).

## Scope and Exclusions

**In scope**:
- `go for i := range N` loops (rangeint pattern)
- `go for i, v := range slice` loops (rangeindex pattern)
- Decomposed index path (laneCount > 4 on WASM)
- Loops with varying if/else, varying switch, store coalescing

**Out of scope** (no peeling, fall back to current behavior):
- Loops inside SPMD function bodies with break masks
- SPMD function bodies without `go for` loops
- Non-power-of-2 lane counts (not applicable on WASM)

## Expected Impact

### Hex-encode (16 i8 lanes, 1024 bytes → 2048 output)

| Metric | Before | After (main loop) |
|--------|--------|-------------------|
| Instructions/iter | ~177 | ~31 |
| v128.store8_lane | 16 | 0 |
| v128.store | 0 | 1 |
| Tail mask compute | 11/iter | 0 (main), 11 (tail) |
| Branches in store | 17 | 0 |
| Theoretical speedup | 0.36x | ~8x |

### Other examples affected
All `go for` examples benefit: simple-sum, odd-even, mandelbrot-bench (for non-break iterations), debug-varying, lanes-index-restrictions, and any future examples.

## Testing Plan

1. **LLVM IR unit test**: Verify main loop body contains `store <16 x i8>` (not `llvm.masked.store`)
2. **LLVM IR unit test**: Verify tail body contains `llvm.masked.store` with computed mask
3. **LLVM IR unit test**: Verify `alignedBound` = `bound & ~(laneCount-1)` in entry
4. **LLVM IR unit test**: Verify main loop exits to `tail.check`, not original exit
5. **E2E test**: hex-encode correctness (aligned input, 1024 bytes)
6. **E2E test**: hex-encode with non-aligned input (e.g., 1025 bytes → 2050 output)
7. **E2E test**: small input (bound < laneCount, main loop skipped)
8. **E2E test**: empty input (bound == 0)
9. **Benchmark**: hex-encode speedup before/after
10. **Regression**: all existing E2E tests still pass
