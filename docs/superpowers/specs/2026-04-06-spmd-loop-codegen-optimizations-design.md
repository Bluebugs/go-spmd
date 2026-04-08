# SPMD Loop Codegen Optimizations Design

## Goal

Reduce SPMD loop overhead from 40 to ~18 instructions per 32-byte iteration by eliminating redundant mask computation, constant broadcasts, and read-modify-write stores in the main (all-active) body of peeled loops.

## Problem

The perf analysis of the base64 Mula-Lemire decoder on AVX2 showed 40 instructions per 32-byte iteration vs Mula-Lemire's ~8. The breakdown:

| Overhead | Instructions | % of loop |
|----------|-------------|-----------|
| Mask prologue (B3) | 18 | 45% |
| Unhoisted constant broadcasts (B1) | 4 | 10% |
| Read-modify-write masked store (B2) | 2 | 5% |
| **Total removable** | **24** | **60%** |
| Actual compute + store | 16 | 40% |

After optimization: ~16 instructions / 32 bytes = 0.50 instrs/byte (vs current 1.25).

## Context: SSA-Level Loop Peeling

The x-tools-spmd `peelSPMDLoop` (in `x-tools-spmd/go/ssa/spmd_peel.go`) already splits SPMD loops into:
- **Main body**: all lanes active, iterates while `remaining >= laneCount`
- **Tail body**: masked, handles the final partial iteration

The TinyGo compiler knows about this split via `ssaLoop.MainBodyBlock` and `ssaLoop.TailBodyBlock` (detected at `spmd.go:1539`). But currently both paths go through the same `emitSPMDBodyPrologue()` which computes the full mask every iteration.

## Fix B2: Direct Store When Mask Is All-Ones

**File**: `tinygo/compiler/spmd.go`, function `spmdMaskedStore` (~line 4488)

**Current behavior**: Always does load-blend-store:
```
oldVal = load(ptr)
blended = (newVal & mask) | (oldVal & ~mask)
store(blended, ptr)
```

**Fix**: At the top of `spmdMaskedStore`, check `spmdIsConstAllOnesMask(mask)`. If true, emit a direct `CreateStore(val, ptr)` and return — skip the load-blend-store entirely.

This also applies to `spmdFullStoreWithBlend` (~line 7886) which is the contiguous-store path. When mask is all-ones, call `CreateStore` directly instead of the blend path.

**Impact**: Saves 2 instructions per store (the wasted load + blend). Low risk — the check is a simple constant inspection.

## Fix B1+B3: Simplify Main Body Prologue

**File**: `tinygo/compiler/spmd.go`, function `emitSPMDBodyPrologue` (~line 1533)

B1 and B3 are coupled — both involve what `emitSPMDBodyPrologue` does for the main body of a peeled loop.

**Current main body prologue** (18 instructions in assembly):
1. Compute `active_lane_count = min(remaining, laneCount)` (5 instrs)
2. Broadcast `active_lane_count` to vector (2 instrs)
3. Compare against lane-index vector to produce mask (3 instrs)
4. Branch on whether all lanes active (3 instrs)
5. Lane-index computation: `iterVec = splat(iter) + offsetConst` (3 instrs)
6. Constant loads for offset vector, mask type (2 instrs)

**For the main body, all of steps 1-4 are dead work** — the mask is always all-ones by definition (the main loop only runs when `remaining >= laneCount`).

**Fix**: When `isPeeled && isMainBody`:
1. Set `loop.tailMask = llvm.ConstAllOnes(maskType)` (already done at line 1585)
2. Skip the `min(remaining, laneCount)` computation
3. Skip the `broadcast + compare` mask derivation
4. Skip the all-active branch (there's only one path)
5. Still compute lane indices (`iterVec + offsetConst`) — needed for address computation
6. Still increment the loop counter

The lane-index computation stays (needed for loads/stores), but the mask computation and branching are eliminated.

**Impact**: Saves ~18 instructions per main-body iteration. The tail body is unchanged — it still computes the dynamic mask.

**Risk**: Medium. Must ensure all downstream code that reads `loop.tailMask` or the loop's mask value works correctly with the constant all-ones mask on the main path. The key invariant: in the main body, `loop.tailMask` is `ConstAllOnes`, which `spmdIsConstAllOnesMask` recognizes, enabling the B2 direct-store optimization as well.

## What Does NOT Change

- **Tail body**: Full mask prologue, masked stores, all as before
- **Non-peeled loops**: No change (they don't have main/tail separation)
- **SSA-level peeling**: No changes to x-tools-spmd
- **Lane-index computation in main body**: Still needed for address calculation

## Success Criteria

- Base64 Mula-Lemire decoder hot loop on AVX2: ~16-18 instructions per 32 bytes (down from 40)
- No `vpbroadcastb` for mask-related constants in the main loop body
- No `vmovdqu load + vpblendvb` in the main loop stores (direct `vmovdqu store` only)
- All 101 E2E tests pass
- Hex-encode, mandelbrot, lo-* benchmarks produce identical output
