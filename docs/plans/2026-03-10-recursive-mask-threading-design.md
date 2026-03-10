# Recursive Mask Threading Design

## Problem

The current SPMD mask assignment uses a two-phase architecture:

1. **Phase 1** (`predicateSPMDScope`): Linearizes varying control flow (if/else/switch/boolean chains). Masks memory ops *inside* varying if-bodies with narrowed masks via `spmdMaskMemOps`.
2. **Phase 2** (`spmdConvertLoopOps`): Stamps a flat mask (all-ones or TailMask) on all *remaining* memory ops not already converted in Phase 1.

These phases don't compose. A `continue` inside a varying `if` should deactivate lanes for subsequent instructions in the same iteration, but Phase 2's flat mask doesn't capture this:

```go
go for i, v := range data {
    if v == 0 {       // varying
        continue      // deactivates lanes where v==0
    }
    result[i] = v * 2 // BUG: uses flat tailMask, should use tailMask & ~(v==0)
}
```

Additionally, the two-phase split requires workarounds:
- `spmd.tail.mask` Parameter with getValue fallback in TinyGo
- ssaLoopInfo lookup heuristics for non-peeled rangeindex
- Separate main/tail block partitioning logic

## Design

### Core Model: Mask-as-Return-Value

`predicateSPMDScope` becomes the **sole** mask assignment pass. It threads `activeMask` through blocks sequentially and returns the (potentially modified) mask:

```
predicateSPMDScope(blocks, activeMask) → maskOut:
    for each block in program order:
        if varying_if:
            thenMask = activeMask & cond
            elseMask = activeMask & ~cond
            thenMaskOut = predicateScope(thenBlocks, thenMask)
            elseMaskOut = predicateScope(elseBlocks, elseMask)
            activeMask = SPMDSelect(cond, thenMaskOut, elseMaskOut)
        else:
            mask all mem/call/index ops in block with activeMask
    return activeMask
```

### Continue Semantics

`continue` inside a varying `if` does NOT generate a jump. It narrows the mask to zero for those lanes:

- **Detection**: then-block's original successor is the loop block (before linearization)
- **Effect**: `thenMaskOut = zero_mask` instead of `thenMask`
- **Merge**: `SPMDSelect(cond, zero, elseMask) = activeMask & ~cond`

### Nested Composition

The recursive model naturally composes for nested varying ifs:

```go
if v > 0 {              // thenMask1 = active & (v>0)
    if v > 10 {          // thenMask2 = thenMask1 & (v>10)
        continue         // thenMaskOut2 = zero
    }                    // maskAfterInner = select(v>10, zero, thenMask1 & ~(v>10))
                         //                = thenMask1 & ~(v>10)
    result[i] = v        // masked with: active & (v>0) & ~(v>10) ✓
}                        // thenMaskOut1 = active & (v>0) & ~(v>10)
                         // maskAfterOuter = select(v>0, thenMaskOut1, elseMask)
output[i] = v * 2       // masked with: active & ~(lanes that continued) ✓
```

### Inner For Loops

Regular `for` loops inside `go for` don't affect the outer mask. The mask entering the `for` is the mask exiting the `for`. Break/continue inside inner `for` affect only inner loop control flow, not the SPMD mask.

### Entry Mask per Loop Type

| Loop type | Main phase mask | Tail phase mask |
|-----------|----------------|-----------------|
| Peeled rangeint | all-ones | TailMask |
| Peeled rangeindex | all-ones | TailMask |
| Non-peeled rangeindex | TailMask | n/a |
| Non-peeled rangeint | all-ones | n/a |

The entry mask feeds into `predicateSPMDScope` as `activeMask`. The recursive threading handles everything from there.

### What Gets Removed

- `spmdConvertScopedMemOps` — replaced by inline masking during predication
- `spmdMaskScopedCallOps` — same
- `spmdMaskScopedIndexOps` — same
- `spmdMaskScopedMakeInterfaceOps` — same
- `spmdConvertAllMemOps`, `spmdMaskAllIndexOps`, `spmdMaskCallOps`, `spmdMaskAllMakeInterfaceOps` — same
- Main/tail block partitioning in `spmdConvertLoopOps`
- TailMask Parameter workaround and getValue fallback in TinyGo
- ssaLoopInfo lookup workaround for non-peeled rangeindex

### What Stays Unchanged

- `predicateVaryingBreaks` — break mask accumulation in SPMD function bodies
- `predicateVaryingSwitch`, `predicateBooleanChain` — called within the scope pass
- `spmdNarrowMaskAtTypeAsserts` — post-pass narrowing
- `spmd_peel.go` — peeling is orthogonal

### What `spmdConvertLoopOps` Becomes

Simplified orchestrator: determines entry mask per loop type, calls `predicateSPMDScope` with that mask. For peeled loops, calls it twice (main blocks + tail blocks). Then calls `spmdNarrowMaskAtTypeAsserts`.

## Files Changed

1. **`x-tools-spmd/go/ssa/spmd_predicate.go`**: Main refactor — `predicateSPMDScope` returns mask, inline mem op masking, continue detection, remove flat-stamp functions
2. **`tinygo/compiler/compiler.go`**: Remove `spmd.tail.mask` Parameter fallback
3. **`tinygo/compiler/spmd.go`**: Remove ssaLoopInfo lookup workaround

## Testing

- Existing E2E tests (36 compile, 31 run) must not regress
- New test: `continue` inside varying `if` with subsequent memory access
- New test: nested varying `if` with `continue` in inner if
- New test: multiple `continue` paths narrowing mask progressively

## Expected Impact

~300-400 lines removed, ~50-100 lines added. Single-pass architecture eliminates class of mask composition bugs.
