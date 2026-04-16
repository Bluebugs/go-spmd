# SPMD for Go: Novel and Adapted Patterns

*Research-artifact writeup of the techniques invented or non-trivially adapted during the SPMD-for-Go proof of concept. For compiler researchers, engineers who might cite this work, and anyone doing similar work in other language toolchains.*

---

# §1 Intro and scope

## §1.1 What "novel" means in this document

A technique qualifies as novel (or novel-adapted) here if at least one of the following holds:

- **We did not find it in ISPC, LLVM, Cranelift, or Mojo**, despite looking.
- **It is an adaptation of a known technique** specifically for the Go type system and the TinyGo + `go/ssa` toolchain, where the adaptation required non-trivial engineering that wouldn't transfer mechanically from the originating system.
- **It is a negative result** — a technique we expected to work, built out, measured, and deleted — and the deletion itself was informative.

This is not a claim to theoretical novelty in the academic sense. Predicated SSA, masked vector load/store, loop peeling, and vector reductions are all well-known. What is novel is the *integration*: how these techniques plug into Go's type system and `golang.org/x/tools/go/ssa`, how they compose with one another, and how idiomatic Go source code triggers them.

## §1.2 What this document covers

- §2: Decomposed index path for byte iteration on wide SIMD (the most important practical contribution).
- §3: Predicated SSA at the `go/ssa` layer, with first-class varying metadata.
- §4: SSA-level loop peeling, and why it has to be at the SSA level.
- §5: Deferred mask type resolution.
- §6: Contiguous access analysis through `ChangeType` and `BinOp`.
- §7: Pattern detection for `vpmaddubsw` / `vpmaddwd` via cascading `go for`.
- §8: Byte-decomposition store for interleaved outputs.
- §9: AVX2 `vpshufb` swizzle table duplication rule.
- §10: WASM guard zone: overread + post-mask for safe vector loads.
- §11: SSA-level store merging with `(X, Index)` normalization.
- §12: Inner-scalar-loop exclusion heuristic for predication scope.
- §13: **Negative result**: cross-lane primitives that delivered no measurable win.
- §14: Techniques we considered novel but aren't.
- §15: Research follow-ups.
- §16: Evidence appendix.

## §1.3 What it doesn't cover

Implementation mechanics and file-level details live in `implementer-notes.md`. User-facing patterns live in `developer-guide.md`. This document focuses on "what is the technique, why does it work, what is the prior art, and what is the evidence that it pays off."

## §1.4 Ground truth pointers

Every technique section has at least one file:line anchor pointing at the PoC implementation. The PoC repository is at `github.com/Bluebugs/...` with submodules `go/`, `tinygo/`, and `x-tools-spmd/` (a patched copy of `golang.org/x/tools@v0.30.0`). All anchors are relative to the repository root.

---

# §2 Decomposed index for byte iteration on wide SIMD

This is the PoC's most important practical contribution and the one most likely to transfer to other language toolchains. It is how you make `for i, b := range byteSlice` fast on AVX2 and AVX-512 without drowning in index-vector register pressure.

## §2.1 The problem

On AVX2, a 256-bit register holds **32 bytes** — 32 lanes, if you iterate at byte granularity. On AVX-512 that becomes **64 bytes**. Byte-granular iteration is what you want for encoders, decoders, and byte-level parsers: 4× more parallelism than `int32` lanes at the same width.

But byte-granular iteration over a `[]byte` slice requires computing 32 (or 64) memory addresses per iteration. The naive compilation produces a vector of 32 `i64` indices — **256 bytes of register state just for the index**. That's one full AVX2 register consumed by indices alone, before you've loaded any actual data.

Worse, when those indices flow into a scatter or gather, LLVM's scalarization and GEP lowering paths struggle. In particular, LLVM's `getelementptr vector` path does sign-extension on narrow index vectors (e.g., `<32 x i8>` → `<32 x i64>`) and the sign-extend has subtle correctness issues with certain offset layouts. We named this "hex-encode Bug 2" and fixed it on 2026-03-31 by switching to the technique below.

## §2.2 The technique: scalar base + `<N x i8>` lane offset

Instead of materializing a full vector of indices, maintain the iteration position as a **pair of values**:

1. A **scalar base pointer**. It points to the first byte of the current lane-group. It is incremented by `laneCount * elemSize` (= `laneCount` bytes for a `[]byte`) at the bottom of each main-body iteration. It lives in a general-purpose register, not a vector register.

2. A **constant `<N x i8>` lane offset vector**. Its value is `[0, 1, 2, ..., N-1]` at compile time. It never changes. It lives in a vector register but is loaded once.

Every memory operation combines the two:

```
// Conceptually, at every gather/scatter GEP site:
//
//   address_vector = base + sign_extend(lane_offset)
//
// In practice the combine is done by the target-specific GEP lowering:
//
//   x86-64: lea / vpaddq with i32 base → vector of i64 addresses
//   WASM:   i32 add fold into load offset
```

The base is i64 on x86-64 and i32 on WASM/wasi. Since GEPs on x86-64 often need an `i32` addend (for displacement addressing modes), we **truncate the base to i32 before combining** when the addressing mode allows, then let the load/store reassemble the full pointer.

This gives you byte-granular iteration with **no vector index register pressure**. The offset vector is 32 bytes on AVX2 — it fits in one register, is loaded once, and stays there for the life of the loop.

## §2.3 The gating condition and flag

In the PoC, the decomposed path is gated by:

```go
// tinygo/compiler/spmd.go:1091 (approximate)
isDecomposed := ssaLoop.IsRangeIndex && laneCount > 4
```

The flag is stored on `spmdActiveLoop` at around `spmd.go:902–907`:

```go
type spmdActiveLoop struct {
    // ...
    isDecomposed bool       // true = scalar base + <N x i8> lane offset
    iter         llvm.Value // scalar base pointer in decomposed mode
    laneIndices  llvm.Value // const <N x i8> offset in decomposed mode
    // ...
}
```

The `IsRangeIndex` field distinguishes `for i, x := range slice` (where we can decompose) from `for i := range N` (where the "index" is just a varying integer and decomposition would not save anything).

The `laneCount > 4` threshold is a minimum width. At 4 lanes or fewer, a `<4 x i32>` index vector costs 16 bytes — trivially cheap — and the GEP scalarization issue doesn't arise. Above 4, the decomposed path is strictly better.

**Originally**, the gate was `isDecomposed := spmdIsWASM() && ssaLoop.IsRangeIndex && laneCount > 4` — decomposed only on WASM, because WASM GEPs are i32 and the whole i64-truncation question didn't arise. We removed the `spmdIsWASM()` restriction on 2026-03-31 and the technique now applies uniformly to WASM (16 lanes for byte), SSE (16 lanes), AVX2 (32 lanes), and AVX-512 (64 lanes). It is strictly beneficial on every wide target.

## §2.4 Interaction with power-of-2 modulo

A minor but pleasing optimization: when the lane offset is a `<N x i8>` vector, modulo by a power of 2 becomes a cheap bitmask on the offset — no division, no conditional branches.

For example, `i % 16` where `i` is the lane offset compiles to `lane_offset & 0x0F`, which is a single `vpand` with a splatted constant. The same operation on a full `<N x i64>` index vector would also work but operate on larger registers and chew more throughput.

This isn't unique to SPMD — any compiler knows `x % N` is `x & (N-1)` for power-of-2 N — but it's particularly clean on the decomposed path because the `i8` offset is exactly the right width for the common modulo constants (`% 2`, `% 4`, `% 8`, `% 16`).

## §2.5 Bugs it fixed

Introducing the decomposed path fixed a chain of bugs that had been blocking wide-SIMD correctness:

- **Hex-encode Bug 2 (SExt corruption in scatter GEP).** The pre-decomposed path narrowed indices to `<32 x i8>` for storage efficiency, then SExt-ed them back at GEP time. LLVM's SExt-then-GEP pipeline produced wrong addresses for certain offsets. Switching to the decomposed path avoided the SExt entirely.
- **Hex-encode Bug 3 (AVX2 `vpshufb` halving).** Related to §9 — `vpshufb` shuffles each 128-bit half independently on 256-bit registers. The decomposed path made it obvious where the swizzle inputs came from, which let us apply the table-duplication fix in §9 cleanly.
- **Simple-sum / odd-even alignment SIGSEGVs.** The pre-decomposed path generated vector loads with vector-size alignment (16 or 32 bytes), which faulted on stack-allocated slices. The decomposed path combined with an element-size alignment fix (implementer notes §5.5) solved both.

## §2.6 Novelty claim

This technique is **novel for Go + TinyGo + wide SIMD + byte-granular iteration**. The combination is what makes it necessary:

- **ISPC** has a fixed `programCount` that developers set at compile time. In practice, ISPC programs set programCount to 4, 8, or 16 — never to 32 or 64 — because ISPC's use cases (ray tracing, physics) don't benefit from byte-granular parallelism. Without byte-granular iteration, the SExt-corruption problem doesn't arise, and ISPC's codegen doesn't need this trick.
- **LLVM's auto-vectorizer** can't see across Go slice headers. Even in plain Go, the vectorizer gives up on anything non-trivial, and it particularly can't rewrite iteration variable representations. The trick has to be in the frontend, not the vectorizer.
- **C/C++ vectorized code** that iterates bytes at 32-lane width typically uses hand-written intrinsics that store the base pointer as a plain `char*` and the offset as a `__m256i` constant. The trick is the same in spirit, but C programmers write it manually; we have the compiler do it automatically.
- **Cranelift** as of the time of this PoC does not do any SPMD-style vectorization; the issue doesn't arise for it.

What is novel here: the recognition that **Go's range-over-slice SSA form admits a clean decomposition into a scalar base and a constant offset**, and that this decomposition is strictly better than a naive vector index for any lane count above 4. The implementation is maybe 300 lines of TinyGo code plus small changes in the `go/ssa` patching layer; the conceptual move is the important part.

## §2.7 Recommendation to future implementers

If you are building an SPMD or auto-vectorizing frontend for a systems language and you want byte-granular iteration to be fast, **build the decomposed index path from day one.** It is not an optimization you add later; it is the only way to make byte iteration tractable at wide SIMD widths.

The specific shape to reach for:

- A scalar base pointer that lives in a GPR and is incremented by `laneCount * elemSize` per main-body iteration.
- A compile-time constant `<N x i8>` offset vector loaded once and kept live.
- A GEP lowering site that combines them lazily and locally — one call per memory op, not one at the top of the loop.

---

# §3 Predicated SSA at the `go/ssa` layer

## §3.1 The key insight

Predicated Static Single Assignment — using masks to linearize control flow for SIMD execution — is a classical technique (Carter, Ferrante, and Hall, "Predicated Static Single Assignment," PACT 1999). ISPC uses it. LLVM's SVE/RISC-V vector lowering uses it internally. We didn't invent the concept.

What is novel in our use is **the level of abstraction**. We do predication at the SSA level — as structured transforms (predicated scopes, loop peeling, store merging) rather than as a bag of vector opcodes. In the PoC, we prototyped this in `golang.org/x/tools/go/ssa` because that's the SSA layer TinyGo consumes. This has two consequences:

1. **The backend becomes mechanical.** TinyGo consumes the predicated SSA and emits LLVM IR per node, with no need to understand higher-level control flow. The mask stack that earlier PoC versions maintained in the backend (~330 lines, see implementer notes §6) was deleted.
2. **The patterns transfer.** The structured transforms — `SPMDLoopInfo`, explicit-mask `SPMDLoad`/`SPMDStore`/`SPMDSelect`, `If.IsVarying` metadata, predicated scopes, loop peeling — are not specific to `golang.org/x/tools/go/ssa`. For an upstream Go implementation, they would be re-expressed in `cmd/compile/internal/ssa`, taking inspiration from what worked in this experiment. Any SSA framework that can represent masked memory operations, conditional metadata, and structured loop transforms can host these patterns.

The patched SSA layer lives in `x-tools-spmd/go/ssa/`. It is about 2000 lines of additions on top of the stock `go/ssa`, most of them in a handful of new files: `spmd_loop.go`, `spmd_varying.go`, `spmd_peel.go`, `spmd_predicate.go`. These serve as the reference implementation; the production target is `cmd/compile/internal/ssa`.

## §3.2 Two entry points

Predication has two natural scopes:

- **`predicateSPMDScope`** at `x-tools-spmd/go/ssa/spmd_predicate.go:419`. Runs on the scope of a single `go for` loop body. Most common case.
- **`predicateSPMDFuncBody`** at `x-tools-spmd/go/ssa/spmd_predicate.go:78`. Runs on the entire body of a function whose signature has a varying parameter. Less common, used for SPMD helper functions called from inside a `go for`.

They share helpers (`spmdRelocateToBlock`, boolean chain resolution, accumulator handling) and produce the same output shape. The only difference is scope: what blocks count as "inside the predicated region."

## §3.3 What the transform does

Starting from Go control flow with varying conditions:

```go
if v { A } else { B }
```

where `v` is `lanes.Varying[bool]` and the enclosing mask is `m`, predication produces:

```go
// A executes under mask m & v.
// B executes under mask m & ~v.
// Merge phi at the end becomes:
merged := SPMDSelect(v, a_val, b_val)
```

Memory operations inside A and B become `SPMDLoad` / `SPMDStore` instructions with explicit mask operands. Branches are removed from the CFG; control-flow-linearization makes the block sequence straight-line.

For a switch:

```go
switch v { case 1: A; case 2: B; default: C }
```

The SSA builder has already lowered this to a chain of `If(v == 1) → A; else If(v == 2) → B; else C`. Predication groups this chain as an `SPMDSwitchChain` (§3.4) and lowers it:

- `A` runs under `m & (v == 1)`.
- `B` runs under `m & (v == 2)`.
- `C` runs under `m & ~(v == 1) & ~(v == 2)`.
- Done-block phis become `SPMDSelect` chains.

For `&&` and `||`:

```go
a && b
```

where `a` is varying. Short-circuit semantics say `b` is only evaluated on lanes where `a` is true. This becomes a mask composition: `b` is evaluated under `m & a`, and the result is `m & a & b`. The dual `a || b` gives `m & (a | b)` with `b` evaluated under `m & ~a`.

## §3.4 Why it's not "just" predicated SSA

Classical predicated SSA (Carter-Ferrante-Hall) assumes **scalar SSA that you mechanically vectorize**. Every operation becomes a masked vector operation; every branch becomes a mask composition.

Our input is different: it is **mixed uniform/varying SSA**, where some values are scalars (uniform) and some are vectors (varying), and the control flow can be either uniform or varying on a per-branch basis. A uniform branch should stay as a real branch — we don't want to predicate it, because that would force scalar work into vector registers unnecessarily.

To distinguish, we add **first-class metadata** to the SSA nodes:

- **`If.IsVarying`** is a boolean flag on every `*ssa.If` instruction. It is set during SSA construction (in `builder.go` at lines 217, 253, 276 approximately) using the AST-level helper `exprHasSPMDType` at `x-tools-spmd/go/ssa/spmd_varying.go:17`. The helper walks the AST of the condition expression and returns true if any subexpression has a varying type.
- **`SPMDSwitchChain`** at `x-tools-spmd/go/ssa/ssa.go:455-460` groups the `If`s produced by switch lowering:

  ```go
  type SPMDSwitchChain struct {
      TagValue     Value        // the switch tag, varying
      Cases        []*If        // one per case clause, in order
      DefaultBlock *BasicBlock  // reached when no case matches
      DoneBlock    *BasicBlock  // merge point after the switch
  }
  ```

  Populated during SSA construction at `builder.go:1551, 1605`.

- **`SPMDBooleanChain`** at `x-tools-spmd/go/ssa/ssa.go:466-471` captures the block structure of a varying `&&` or `||`:

  ```go
  type SPMDBooleanChain struct {
      Op         token.Token   // LAND or LOR
      Blocks     []*BasicBlock // short-circuit blocks
      ThenBlock  *BasicBlock
      ElseBlock  *BasicBlock
      IsVarying  bool
  }
  ```

  Populated in `cond()` via a stack-based accumulator at `builder.go:212-219` (for `&&`) and `builder.go:248-255` (for `||`), with shared-target matching so that a chain like `a && b && c` collapses into a single three-operand structure instead of two nested two-operand chains.

With this metadata, the predication pass:

1. Walks uniform branches untouched — they stay as real control flow.
2. Linearizes varying branches into masked-select form.
3. Handles switches and boolean chains as single units, rather than re-deriving their structure from the lowered If chain.

## §3.5 Block pointer re-resolution — the trap

**All of this metadata holds pointers into the CFG.** `SPMDLoopInfo` stores block pointers. `SPMDSwitchChain` stores block pointers. `SPMDBooleanChain` stores block pointers.

Then `ssa.optimizeBlocks()` runs. It deletes empty blocks. It merges straight-line sequences. It renumbers everything. **Our pointers go stale.**

The fix:

```go
// x-tools-spmd/go/ssa/spmd_varying.go around lines 115-140
resolveSPMDSwitchChains(fn)
resolveSPMDBooleanChains(fn)
```

Each resolver re-walks the CFG and re-discovers which blocks correspond to the logical roles in the chain — "the done block is the unique post-dominator of all case blocks," for example. These invariants survive block merging even though pointer identities don't.

**The practical lesson** is generic to any optimization-pass-aware metadata: **if you store block pointers, you must store them in a form that survives block mutation.** Either (a) never mutate blocks after the metadata is set, (b) re-resolve after every mutating pass, or (c) reference blocks by an invariant (like "the unique successor of X with property P") rather than by identity.

We chose (b) — re-resolution — because we wanted the metadata to be populated as early as possible (during SSA construction) for correctness of downstream AST-related checks. If we were starting over, (c) would be cleaner: store queries rather than pointers.

---

# §4 SSA-level loop peeling

## §4.1 The structural split

Every `go for` loop is split into four blocks by `peelSPMDLoops` at `x-tools-spmd/go/ssa/spmd_peel.go:237`:

- **MainBodyBlock.** Executes `floor(N / laneCount)` iterations. The mask is **statically all-ones**. The compiler knows this at every store site.
- **TailCheckBlock.** Branches to TailBodyBlock iff there's a partial iteration (i.e., `N % laneCount != 0`).
- **TailBodyBlock.** Executes at most once. The mask is a runtime-computed bitmap that selects the remaining lanes.
- **TrampolineBlock.** Routes phi values (accumulators, loop-carried varying values) from main to tail to done.

The peeling is guarded by `loop.LaneCount > 1` (spmd_peel.go:254, approximately). Scalar-fallback mode (laneCount = 1) skips peeling entirely, so a scalar build has zero overhead from this transform.

## §4.2 Why SSA-level peeling and not LLVM-level

LLVM has a loop-unroll pass and a loop-vectorizer, both of which do peeling in some form. Neither is suitable for SPMD:

- **LLVM's loop unroller** operates on scalar IR. It doesn't know about masks. If we unroll a masked loop N times, each unrolled iteration still has the mask it started with; there's no way to collapse "iterations 1 through N-1 have all-ones mask, iteration N has a computed tail mask" from within the unroller.

- **LLVM's loop vectorizer** could in principle handle this, but it operates on unannotated scalar loops — loops that don't already know they're going to be vectorized. By the time we hand the IR to LLVM, the loop is already in explicit vector form with masked intrinsics. The vectorizer sees it as "a loop with opaque intrinsic calls" and leaves it alone.

- **Runtime tail masks are hard to materialize correctly in LLVM's passes.** We always got better generated code by building the tail mask structurally at the SSA level than by trying to coax LLVM into computing the right tail mask shape.

At the SSA level, peeling is a straightforward block-creation transform: create four new blocks, rewire phis through them, replace the original loop header with a branch to MainBodyBlock. The backend (TinyGo) consumes the peeled SSA without needing to know it was peeled; it just compiles each block in isolation.

## §4.3 The all-ones fast path enabled by peeling

The reason peeling matters more than any other single optimization in the PoC is this: **the main body's mask is statically known to be all-ones, so memory stores don't need to do load-blend-store.**

Without peeling, an `SPMDStore` must:

1. Load the existing vector at the store address.
2. Blend in the new values at mask-active positions.
3. Store the blended vector back.

Three memory operations per logical store. On a tight loop, this dominates.

With peeling, in the main body, the backend knows the mask is `ConstAllOnes` and can emit a single direct store. One memory operation per logical store. **That's where most of the benchmark win comes from.**

The fast path is implemented in `spmdFullStoreWithBlend` at `tinygo/compiler/spmd.go:4626-4727`. It checks whether the mask is statically all-ones; if yes, emits a direct vector store; if no, falls back to the blend path.

In the peeled tail body, the mask is runtime-computed but executes at most once, so the cost is amortized. The tail is the correct-but-slow path; the main body is the fast-and-common path. Peeling gives us both.

## §4.4 Accumulator phi trampolining

A loop with a varying accumulator — say, a running sum — has a phi at the loop header that combines the initial zero value (from outside the loop) with the updated value from the loop back-edge. After peeling, this phi needs to work across three phases:

1. **Entry → main body.** Initial zero goes to main's phi.
2. **Main body → tail body.** Main's final value becomes tail's initial value.
3. **Tail body → done block.** Tail's final value is the loop result.

The done block therefore needs a phi that selects between "main's final value, if there was no tail" and "tail's final value, if there was a tail." We call this the **done-block phi trampoline**. It was fixed on 2026-03-14 as part of the pointer-varying work (see implementer notes §2.6) and is the kind of detail that is easy to get wrong in any peeling implementation.

The trampoline is built in `peelSPMDLoop` and referenced by the predication pass via `loop.MainIterPhi` and `loop.TailIterPhi` on `SPMDLoopInfo` (see implementer notes §3.2 for the full struct).

---

# §5 Deferred mask type resolution

## §5.1 The problem

A Go function can contain multiple `go for` loops. Consider:

```go
func encode(src []byte, out []int32) {
    go for i, b := range src {       // byte lanes, 32-wide on AVX2
        // ...
    }
    go for j := range len(out) {     // int32 lanes, 8-wide on AVX2
        // ...
    }
}
```

The first loop has lane count 32 (byte-width lanes in a 256-bit register). The second has lane count 8 (int32-width lanes in the same 256-bit register). **Their masks are therefore different LLVM types** — the first is `<32 x i1>` or `<32 x i8>` (depending on representation); the second is `<8 x i1>` or `<8 x i32>`. Same function, same register set, incompatible mask vectors.

There is no canonical answer to "what LLVM type should `MaskType{}` resolve to in this function?" because the right type is context-dependent.

## §5.2 The approach we took

**Resolve mask types at materialization points only.** A materialization point is anywhere a mask becomes a concrete LLVM value:

- **Constant masks.** When we emit a constant all-ones or all-zeros mask, we know the surrounding `SPMDLoopInfo` or the enclosing SPMD function's entry mask. The lane count follows from that. See `tinygo/compiler/spmd.go:2625, 2637`.
- **Phi nodes.** When we create a phi for a mask value, the other incoming values determine the type.
- **Select inputs and outputs.** When we emit an `SPMDSelect`, its condition has a known lane count and we narrow any over-wide mask to match.

Everywhere else — in intermediate `ssa.Value` references, in analysis passes, in transformations — masks are carried abstractly by SSA reference and only converted to LLVM types at the point of actual emission.

As a safety net, `createSPMDSelect` at `tinygo/compiler/spmd.go:8030+` narrows any mask wider than the operands it's selecting between. This catches corner cases where an earlier materialization made a mask too wide.

## §5.3 The alternative we rejected

The cleaner approach would be to thread an explicit "active lane count" context through every LLVM codegen function. Every function that builds an LLVM instruction would take an additional parameter representing the current SPMD scope's lane count, and use it to construct correctly typed masks.

We tried this. It is invasive — every codegen function in `compiler/spmd.go` and several in `compiler/compiler.go` would need to be updated — and it is churny because the codegen path evolves. Every new codegen site has to remember to thread the context.

We judged deferred resolution to be the pragmatic choice for a PoC. For a production implementation, the explicit-context approach is probably worth the investment. Or, even better:

## §5.4 A third option: first-class mask width in the type system

If `MaskType` carried its lane count as part of the type — `Mask[N]` — then `getLLVMType` could return the correct LLVM type without any context. The downside is that you introduce another type-level parameter and have to propagate it through function signatures, phi nodes, and casts.

This would be the cleanest design. We didn't do it because it implies reworking the `types2` / `go/types` extension files again, which mid-PoC was too expensive. For an upstream implementation starting from scratch, consider it.

## §5.5 Soundness argument

The correctness of deferred resolution rests on one property: **every mask is consumed by exactly one materialization point** (a store, a load, a select, or a phi), and each materialization point has enough local context to determine the correct width.

In practice this is true because the SSA predication pass produces a DAG of mask values, and every leaf of that DAG is a memory-op instruction with a known operand type (and therefore a known lane count). The safety net in `createSPMDSelect` catches the rare cases where an earlier materialization produced a mask wider than needed — usually a constant all-ones mask materialized at a conservative width.

The narrowing is always a truncation (`<32 x i8>` → `<8 x i8>`, for example), never an extension. Extensions would be unsound because we don't know which of the wider mask's extra bits to zero out.

---

# §6 Contiguous access analysis

## §6.1 The pattern to recognize

Memory accesses inside a `go for` are **contiguous** (and therefore should use vector load/store instructions) when the address has the form:

```
addr[scalar_base + iter_phi * elemSize]
```

where `scalar_base` is uniform and `iter_phi` is the SPMD loop's iteration variable. The vector load at offset `scalar_base` reads `laneCount` consecutive elements in one instruction.

Everything else — a varying index not derived from the loop iter, a random per-lane offset, an index through a permutation — is **non-contiguous** and must fall back to gather/scatter, which is typically 4-8× slower.

The contiguous/non-contiguous distinction is the single highest-leverage question the backend asks.

## §6.2 Why it's non-trivial

Real Go SSA rarely writes the contiguous pattern in exactly that form. Several intermediate operations obscure it:

- **`ssa.ChangeType`** nodes appear when the iter phi is narrowed (e.g., `int32` to `int`, or `int64` to `int` for range-over-int). The SSA has something like `ChangeType(i32→i64)(phi) + scalar`, and we need to see through the `ChangeType`.
- **`ssa.BinOp ADD` chains** appear when the index is computed as `base + iter + constant` or as `constant + iter + base` — associativity and commutativity give multiple legal shapes for the same logical address.
- **Constant folding** can move the `iter_phi` deeper into the expression tree than a simple pattern matcher would expect.
- **Loop-invariant-motion hoisting** can lift parts of the expression out of the loop, leaving the phi in an unexpected position.

A contiguous-access recognizer has to handle all of these cases to catch the full set of contiguous patterns in real code.

## §6.3 `spmdAnalyzeContiguousIndex` and `spmdUnwrapScalar`

The main recognizer is `spmdAnalyzeContiguousIndex` at `tinygo/compiler/spmd.go:4828-4863` approximately. It recursively unwraps:

- `BinOp ADD` nodes, recursing into both operands to find the iter phi.
- `ChangeType` nodes, continuing through the widen/narrow.
- Phis within the loop body, tracing to their definitions.

Returns a tuple of `(loop, scalar_base, ok)`. If `ok`, the access is contiguous and the scalar base is the uniform offset into the slice.

The dedicated helper `spmdUnwrapScalar` at `tinygo/compiler/spmd.go:4870+` handles the specific case of `ChangeType` chains on the scalar side of the expression. It exists because `range-over-int` produces index expressions like `ChangeType(i32 → i64)(BinOp ADD(iter_phi, zero))` and we need to peel the `ChangeType` without losing the base.

## §6.4 The 38% improvement

The `spmdUnwrapScalar` extension — adding one additional unwrap case on top of already-working recognition — gave a **38% speedup** on the contiguous-store path, documented in the optimization log. That means 38% of the contiguous stores in some benchmarks weren't being recognized as contiguous until we added the `ChangeType` peel, and each missed recognition fell back to scatter.

**The lesson is immediate:** every additional recognizer case is worth more than any other compiler work when it turns "fall back to scatter" into "take the vector-store fast path." Invest disproportionately here.

## §6.5 Generalizing to non-PoC compilers

If you're building a similar recognizer for a different SSA form, the core principles are:

1. **Walk both sides of every add.** Either side could be the scalar base or the iter phi.
2. **Peel all view-only nodes.** `ChangeType`, `Convert`, pointer bitcasts, anything that doesn't change the value.
3. **Trace through phis within the loop body.** The phi may refer to a value that was computed in a previous iteration — follow the chain.
4. **Stop at the first thing that isn't the iter phi or a uniform value.** Don't try to handle "semi-contiguous" cases; either it's fully contiguous or it falls back to gather/scatter.

---

# §7 Pattern detection for `vpmaddubsw` / `vpmaddwd`

## §7.1 The Go idiom

The highest-impact pattern detector in the PoC. It recognizes three cascading `go for` loops of decreasing SIMD width, each doing a constant-coefficient multiply-add:

```go
// Loop 1: byte lanes, per-byte transform.
go for i, ch := range src {
    sextets[i] = transform(ch)
}

// Loop 2: int16 lanes, pair two adjacent sextets.
// a*64 + b pattern → pmaddubsw.
go for g := range merged {
    merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
}

// Loop 3: int32 lanes, pair two adjacent int16s.
// a*4096 + b pattern → pmaddwd.
go for g := range packed {
    packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
}
```

The compiler recognizes the `int16(src[i*2]) * C + int16(src[i*2+1])` shape in loop 2 and emits `vpmaddubsw` — a single SSE4/SSSE3 instruction that does a double-width multiply-add-widen on byte inputs. Same shape in loop 3 on `int16` inputs emits `vpmaddwd`.

For base64 decoding, loop 2 computes `sextet1*64 + sextet2` (concatenating two 6-bit sextets into a 12-bit value), and loop 3 computes `pair1*4096 + pair2` (concatenating two 12-bit pairs into a 24-bit triple). Three cascading loops produce a full 24-bit decoded triple from four ASCII characters, with `vpmaddubsw` and `vpmaddwd` doing all the work in two instructions.

## §7.2 The detector

`spmdTryEmitPmadd` at `tinygo/compiler/spmd.go:8974+` is the entry point. It:

1. Recognizes the `A*C0 + B*C1` shape where A and B are adjacent loads from the same source array at offsets `i*2` and `i*2+1`.
2. Checks that the multipliers are constants.
3. Handles the constant-decomposition case: `vpmaddubsw` takes signed-byte multipliers, so weights `> 127` don't fit. The decomposer splits them into partial products (e.g., `C = 256*hi + lo` where `lo < 128`) and emits two partial `pmaddubsw`es combined with adds.
4. Emits the target instruction via `spmdX86Pmaddubsw` at `tinygo/compiler/spmd_x86.go:57+` or `spmdX86Pmaddwd` at `spmd_x86.go:87+`.

On WASM, there's no single instruction for this; `spmdTryEmitPmadd` falls back to a deinterleave + widen + mul + add sequence, which is still much faster than scalar code.

## §7.3 Why a detector and not a builtin

We originally had a builtin — `lanes.DotProductI8x16Add` — that took the multiply-add-widen shape as an explicit operation. It worked for the IPv4 parser's decimal conversion use case.

The detector replaces it entirely. The detector handles:

- The IPv4 decimal conversion case (specific multipliers).
- The base64 decoder case (different multipliers, larger weights requiring decomposition).
- The hypothetical BGR-to-YUV conversion case.
- Any other pattern that future developers write without knowing about the builtin.

The builtin was deleted in commit 1df19e8, removing ~163 lines of compiler code. The detector's generality is the point.

## §7.4 The result

Base64 Mula-Lemire decoder hot loop: from **14.3 instructions per byte** (scatter-gather version) to **0.44 instructions per byte** with the detector — a **32× instruction reduction**. End-to-end, that's what gets AVX2 to **77% of simdutf C++**.

The significance is not in the instruction count itself — any good SIMD library hits similar numbers — but in the **source the programmer wrote**. The base64 decoder in the PoC is plain Go. No builtins beyond `lanes.Count[byte]`. No intrinsics. The compiler does all the heavy lifting from an idiomatic three-loop kernel.

## §7.5 The moral

**Pattern detectors generalize; per-case builtins don't.**

Every builtin is a tax on future compiler changes: it has to be type-checked, lowered on every target, and shipped in a stable API. A detector is pure compiler code and can be improved, generalized, or deleted without affecting user source.

Ship the detectors. Delete the builtins they subsume.

---

# §8 Byte-decomposition store

## §8.1 The idiom to recognize

Stride-S stores where each slot extracts a byte from a wider source type:

```go
go for i := range n {
    tmp := compute(...)                // Varying[int32]
    out[i*3+0] = byte(tmp)            // low byte
    out[i*3+1] = byte(tmp >> 8)       // mid byte
    out[i*3+2] = byte(tmp >> 16)      // high byte
}
```

Common in packed encoding:

- **RGB** (stride 3, three byte channels).
- **RGBA** (stride 4, four byte channels).
- **Base64 output** (stride 3, three bytes from each decoded 24-bit triple).
- **IPv4 dotted-decimal** (stride variable, four octets from a 32-bit address).

## §8.2 The naive codegen is a disaster

Three separate byte stores per iteration. On a vector target, this is three *masked* byte stores at strided positions. The compiler must either:

- **Scalarize** — one `mov byte ptr [rdi+i*3], al` per lane per position. 3N stores per iteration.
- **Scatter** — a vector scatter at three different offset patterns. Not only expensive but poorly optimized in current hardware.

Either way, the output pack dominates the kernel's runtime. In the base64 v1 decoder, this was the bottleneck; the total performance was gated by output bandwidth.

## §8.3 The technique

Recognize the stride-S pattern and emit:

1. A **bitcast** of the wider source vector to `<N*stride x i8>` — treating the vector as a flat byte array.
2. A **`vpshufb`** (or `v128.swizzle` on WASM) with a constant permutation table that places the extracted bytes at the correct stride-S positions in a destination-shaped byte vector.
3. A **single contiguous masked store** of the destination vector.

Three operations: bitcast, shuffle, store. Independent of N. The shuffle table is a compile-time constant computed from the stride, the shift amounts, and the destination layout.

The detector walks consecutive store instructions, recognizes the stride + shift pattern, synthesizes the shuffle table, and emits the sequence via `spmdEmitInterleavedStoreMasked` at `tinygo/compiler/spmd.go:7670+`. Works uniformly for SSE (`pshufb`), AVX2 (`vpshufb` with the table duplication from §9), and WASM (`v128.swizzle`).

## §8.4 Novelty and evidence

The technique works across all three targets with shared detection code. The detection pass is ~300 lines; the emission is a few dozen per target.

The immediate historical evidence: it replaced ~1500 lines of `lanes.CompactStore` + `SPMDMux` + `SPMDInterleaveStore` infrastructure that had been built for the base64 v1 decoder (added 2026-04-08 through 2026-04-10, removed 2026-04-12). Base64 v1 used explicit cross-lane machinery to compact and store. Base64 v2 uses byte-decomposition detection plus cascading `go for` (§7) and has no cross-lane operations at all, yet hits **77% of simdutf C++** vs. v1's ~20% of simdutf.

**The moral is identical to §7.5:** recognizers generalize, per-case builtins don't. When a compiler has a byte-decomposition store detector, the source code becomes simple and the performance becomes great; when it has compact-store and interleave-store builtins, the source code becomes complex and the performance becomes fine.

## §8.5 Generalizability

Any compiler that ingests Go-shaped SSA and targets a SIMD backend with a byte-granular shuffle instruction can apply this technique. The key requirements are:

1. A **target shuffle instruction** of sufficient width — `pshufb` (16 bytes, SSSE3+), `vpshufb` (16 or 32 bytes, AVX/AVX2), `v128.swizzle` (16 bytes, WASM simd128), Neon `vtbl` (ARM).
2. A **store sequence recognizer** that identifies the stride + shift pattern in N consecutive stores to a contiguous range.
3. A **shuffle table synthesizer** that computes the permutation from the recognized pattern.

The PoC implementation can be lifted almost verbatim, with target-specific stubs for shuffle emission.

---

# §9 AVX2 `vpshufb` swizzle table duplication

## §9.1 The hardware fact

On 256-bit AVX2 registers, `vpshufb` **does not shuffle across the two 128-bit halves**. It treats the operand as two independent 128-bit lanes and applies the shuffle mask to each half separately.

This is an Intel choice — `vpshufb` was originally an SSSE3 instruction on 128-bit registers, and AVX2 extended it to 256-bit by duplicating the semantics per half rather than adding cross-lane logic. The cross-lane instruction is `vpermq` + `vpshufb`, which is slower and requires an extra register.

The practical consequence: a 16-entry byte shuffle table that is correct at 128-bit width is **wrong** at 256-bit width. The high half of the register sees random (or stale) values in the table's top half, which gives garbage outputs.

## §9.2 The rule

**If `laneCount > 16`, duplicate the constant shuffle table to `[table, table]`** — 32 bytes total, with the low and high halves identical.

Now `vpshufb`'s low half sees the real table, and its high half also sees the real table (a copy). The per-half shuffle semantics give correct outputs on both halves.

Implementation: `spmdWasmSwizzle` at `tinygo/compiler/spmd.go:6547+` performs the duplication automatically when the loop's lane count exceeds 16. The x86-specific dispatcher `spmdX86Pshufb` at `tinygo/compiler/spmd_x86.go:9+` routes to `llvm.x86.avx2.pshuf.b` (256-bit) when the operand is `<32 x i8>` and `llvm.x86.ssse3.pshuf.b` (128-bit) when it's `<16 x i8>`.

(The function name `spmdWasmSwizzle` is historical — it was written first for WASM and later extended to handle AVX2. A cleanup would rename it.)

## §9.3 Where it bites you

Any 8-bit lookup table used inside a `go for` at byte granularity on AVX2:

- **Hex encoding** — 16-entry hex character table.
- **Base64 alphabet** — 64-entry table, but the nibble-LUT variant uses 16 entries.
- **URL-encode** — 64-entry percent-escape table.
- **ASCII classification** — 256-entry digit/alpha/hex LUT.

Without duplication, the high 128 bits of every output register hold garbage. **Silent wrong data, no crash.** Testing catches it only if you check the second half of the output.

We discovered it as "hex-encode Bug 3" during AVX2 bringup and fixed it by adding the auto-duplication logic. The fix is a three-line change in `spmdWasmSwizzle`, but you only find the need for it by running on real AVX2 hardware with byte-granular SPMD loops — which is a specific combination few existing tools exercise.

## §9.4 Proposal: first-class shuffle-table types

A cleaner language design would let users declare:

```go
//go:simd shuffle-table
var hexTable = [16]byte{'0','1','2','3','4','5','6','7',
                        '8','9','a','b','c','d','e','f'}
```

and the compiler automatically duplicates the table for the target register width. The PoC does this implicitly by recognizing small constant lookup tables; a production implementation should probably make it explicit so that users can write larger tables and be sure of the layout.

---

# §10 WASM guard zone: overread + post-mask for safe vector loads

## §10.1 The problem

WASM bounds-checks every memory access. A `v128.load` that reads even one byte past the end of linear memory traps — a hard fault, not undefined behavior. The tail of every `go for` loop over a `[]byte` can produce a load that reads up to 15 bytes past the valid data.

Conventional solutions are expensive: bounce buffers (memcpy + extra load), per-element scalar loads (defeats SIMD), or `v128.load_lane` (not universally available, slower than full loads where it is).

## §10.2 The technique

Reserve **16 bytes at the top of WASM linear memory** as an unused guard zone. The heap allocator sees `memory.size * 64KB - 16` as the available heap. Those 16 bytes are never allocated, always valid for reads.

From `tinygo/src/runtime/arch_tinygowasm.go:64`:

```go
heapEnd = uintptr(wasm_memory_size(wasmMemoryIndex)*wasmPageSize) - 16
```

Cost: 16 bytes out of a minimum 64KB memory. Now every `v128.load` from any heap-allocated pointer is safe, even if it overreads by up to 15 bytes.

The load sequence (`createSPMDVectorFromMemoryMasked` at `tinygo/compiler/spmd.go:8708`) is:

```
raw     = v128.load(dataPtr)                  // safe: guard zone prevents trap
indices = const [0, 1, 2, ..., 15]            // lane index vector
mask    = icmp ult indices, splat(length)      // active lanes = 0xFF, inactive = 0x00
result  = and(raw, sext(mask))                // zero the overread garbage
```

Four instructions. No branches, no bounce buffer, no scalar fallback. The `sext(mask)` produces `0xFF` for valid bytes and `0x00` for garbage, so the `and` cleanly zeros everything past the valid length.

## §10.3 x86 variant

On x86-64, the technique adapts: check whether the pointer is within 16 bytes of a page boundary (`ptr & 0xFFF > 0xFF0`). If not (~99.6% of cases), a raw `vmovdqu` suffices — the garbage bytes are acceptable because callers trim via the execution mask, not via vector content. If near a page boundary, fall through to the same overread + mask sequence.

## §10.4 Why it's worth documenting

This is not a novel idea in isolation — overread-then-mask is a common SIMD pattern in C/C++ (simdjson uses it, for example). What is worth documenting is its **specific adaptation to WASM's memory model**, where the guard zone must be integrated into the runtime's heap allocator rather than relying on OS virtual memory guard pages. The 16-byte reservation in `arch_tinygowasm.go` is a one-line change that eliminates an entire class of codegen complexity.

For the IPv4 parser and the base64 decoder's remainder handling, this was the difference between a fast single-load path and a slow bounce-buffer fallback on every input that isn't a multiple of 16 bytes. See `implementer-notes.md` §5.11 for the full details.

---

# §11 SSA-level store merging with (X, Index) normalization

## §13.1 The problem

Post-predication, it is common to see multiple `SPMDStore` instructions targeting the same logical address under different masks. For example:

```go
go for i := range n {
    if cond1 {
        out[i] = A
    } else if cond2 {
        out[i] = B
    } else {
        out[i] = C
    }
}
```

After predication, this produces three `SPMDStore` instructions to the same `out[i]` address, under masks `cond1`, `cond2 & ~cond1`, and `~cond1 & ~cond2`. Three masked stores per iteration. Two of them are redundant in the sense that we could combine them into a single store of `SPMDSelect(cond1, A, SPMDSelect(cond2, B, C))` under the combined mask.

## §13.2 The technique

`spmdMergeRedundantStores` at `x-tools-spmd/go/ssa/spmd_predicate.go:3795+` walks the CFG, groups consecutive stores by **normalized `(X, Index)`** address pairs, merges their masks via OR, chains their values through `SPMDSelect`, and emits a single merged store.

Sub-functions handle the different scopes:

- `spmdMergeStoresInBlock` at `spmd_predicate.go:3902+` merges within one basic block.
- `spmdMergeStoreGroup` at `spmd_predicate.go:4001+` handles the group reduction.
- `spmdMergeStoreGroupCrossBlock` at `spmd_predicate.go:4072+` extends the transform across block boundaries when one block dominates another.

Cross-block merging is delicate because store ordering matters and we have to preserve the observed write order. The pass is conservative: it only merges when it can prove the writes are to the same address and there's no intervening read that could observe the intermediate state.

## §13.3 Why SSA and not LLVM

LLVM's store-forwarding and dead-store-elimination passes do not understand masked vector stores. From LLVM's perspective, a masked store is an opaque intrinsic call with arbitrary side effects — it cannot be forwarded, combined, or eliminated by the existing passes.

At the `go/ssa` level, we know the semantics: we know that `SPMDStore(addr, v1, m1)` followed by `SPMDStore(addr, v2, m2)` (with disjoint masks) is equivalent to `SPMDStore(addr, SPMDSelect(m1, v1, v2), m1 | m2)`. We also know there's no intervening read if we've verified the between-stores region doesn't load from `addr`.

The merge is done at SSA and the output is a single masked intrinsic call, which LLVM then optimizes with its normal passes. Everyone is happy.

## §13.4 Address normalization

The `(X, Index)` comparison uses pointer equality on X (since all SSA values are hash-consed by `go/ssa` — two syntactically equal expressions produce the same SSA node) and structural equality on Index (which may be a small expression tree).

A subtlety: `SPMDLoad` sources need canonicalization so that `load(base) + k` and `k + load(base)` normalize to the same form. This is done at `spmd_predicate.go:7322+` approximately. Without it, the merge pass would miss cases where two stores compute the same address via commutatively-equivalent expressions.

---

# §12 Inner-scalar-loop exclusion heuristic

## §13.1 The problem

A `go for` body can contain nested scalar `for` loops. Inside such a nested loop, every operation is still scalar Go — it just happens to be running inside a vectorized outer context. The per-lane mask is the same at the bottom of the inner loop as at the top.

But how does the predication pass tell a scalar inner loop apart from a vectorized inner structure? Naive predication would vectorize every block inside the `go for` scope, including inner scalar loops, which produces wrong code (the inner loop's counter becomes a vector, its comparison becomes varying, its branch becomes masked — none of which is correct for a per-lane scalar loop).

## §13.2 The heuristic

**A block `b` is part of the SPMD loop scope iff some predecessor has `pred.Index >= b.Index`.**

Blocks in `go/ssa` are numbered in control-flow order. A self-loop (back-edge to the same block) satisfies `pred.Index == b.Index`. A normal loop back-edge (to an earlier block) satisfies `pred.Index > b.Index`. Both of these are part of the SPMD scope.

A block entered only from a parent block (no back-edge from anything with a higher index) is not part of the SPMD scope. This is how inner scalar loops enter their body — the outer loop's block enters the inner loop's header, and the inner loop back-edges are "local" to the inner loop (their index is less than the inner loop's own body index, which is less than the outer loop's back-edge target).

**Exception:** if the inner loop carries a varying phi, it *does* belong to the outer SPMD scope. A varying value flowing through the inner loop implies the inner loop is effectively part of the vectorized computation even if its structure is a nominally scalar `for`.

## §13.3 Where it lives

The heuristic is implemented in `spmdLoopScopeBlocks` in the x-tools-spmd predication pass. Tests:

- `InnerScalarLoopExcluded` in `x-tools-spmd/go/ssa/spmd_predicate_test.go` — verifies a plain inner scalar loop is not predicated.
- `InnerLoopWithVaryingPhiInScope` in the same file — verifies an inner loop carrying a varying phi is predicated.

The motivating bug was the `map-restrictions` integration test on 2026-03-14. Without the heuristic, the test's inner scalar `for` was being predicated as though it were varying, and codegen produced wrong output.

## §13.4 Generalizability

This heuristic is specific to SSA forms with indexed blocks, where back-edges can be identified by block-index comparisons. If your SSA uses a different representation — e.g., dominator-based loop identification — the equivalent rule is: "a block is in the SPMD scope iff there is a path from the SPMD loop's header to the block that does not leave the SPMD loop's dominance region."

Either formulation works. The PoC uses the index-based form because `go/ssa` blocks are already indexed and the check is constant-time.

---

# §13 Negative result: cross-lane primitives delivered no benchmark wins

This is the most important negative result in the PoC and the one most worth documenting, because it contradicts the conventional wisdom about SIMD library design.

## §13.1 The ops we built

The `lanes` package in the PoC shipped a full set of cross-lane operations:

- **`lanes.Rotate(v, k)`** — full-width rotation by compile-time-constant offset.
- **`lanes.Swizzle(v, idx)`** — full-width permutation by a runtime-indexed per-lane vector.
- **`lanes.RotateWithin(v, k, n)`** — rotate within each group of `n` lanes.
- **`lanes.ShiftLeftWithin(v, k, n)`** — shift left within groups.
- **`lanes.ShiftRightWithin(v, k, n)`** — shift right within groups.
- **`lanes.SwizzleWithin(v, idx, n)`** — const-indexed permutation within groups.

All of these work. All of them lower to sensible LLVM IR (`shufflevector` for the const-indexed ones, per-lane extract/insert for the runtime-indexed `Swizzle`). All of them pass correctness tests in both SIMD and scalar fallback modes.

## §13.2 What we measured

Every example in the PoC was benchmarked at multiple points during development. At the end of the project, **every example had been rewritten at least once to use zero cross-lane primitives** (or, at most, `Broadcast`, `Count`, and `Index` — which are essentially free).

- **Base64 Mula-Lemire decoder.** v1 used `lanes.CompactStore` and `lanes.Rotate` tricks for output packing. Peaked at ~2× scalar. v2 was rewritten to use cascading `go for` (§7) and byte-decomposition store (§8), with zero cross-lane operations. Hit **77% of simdutf C++** — a roughly 10× improvement over v1.
- **Hex-encode.** No cross-lane operations in the final version. Hits 8.9× on WASM, 6.3× on SSE.
- **Mandelbrot.** No cross-lane operations. Hits 6.07× on AVX2.
- **lo-min, lo-max, lo-sum, lo-mean, lo-clamp.** All reductions. Zero cross-lane operations. Up to 7.27× on AVX2.
- **IPv4 parser.** Initially used `lanes.DotProductI8x16Add` as a builtin. When `vpmaddubsw` pattern detection (§7) landed, we removed the builtin entirely and the parser still compiled and ran correctly. Performance was unchanged.
- **simple-sum, odd-even, to-upper, array-counting, map-restrictions, union-type-generics.** None of these use cross-lane primitives.

## §13.3 Theory: why the wins come from elsewhere

Cross-lane primitives move values *within* a SIMD register. They do not change how much memory is loaded or how many arithmetic operations are performed.

In data-parallel Go kernels, the measurable wins come from:

1. **Reducing memory traffic.** Contiguous load/store replacing gather/scatter. §5 of the implementer notes, §6 of this document. This is typically 4-8× per recognized access.
2. **Reducing instruction count.** Pattern detection replacing multi-instruction emulation. `vpmaddubsw` (§7) replacing 8+ instructions with 1. `pshufb`-based byte decomposition (§8) replacing scalar stores.
3. **Eliminating mask-blending overhead.** All-ones fast path in peeled main bodies (§4).
4. **Avoiding index-vector register pressure.** Decomposed index path (§2).

Cross-lane primitives contribute to none of these. They are local rearrangements of register state, and register rearrangement is cheap on modern hardware — not expensive enough to matter when the surrounding code is already well-optimized.

ISPC and Mojo ship rich cross-lane vocabularies because their target markets — ray tracing, physics simulation, shader compilation — have lots of small kernels that need within-register rearrangement (butterfly operations, per-lane shuffles for neighborhood operations, etc.). Go's likely SPMD market — encoders, decoders, parsers, numerical reductions over slices — has almost none of that.

**This isn't an indictment of the primitives.** They work, they're correct, they're cheap to execute. It is an indictment of our prior belief that shipping them was essential for a competitive SPMD implementation. The evidence says: **it isn't.**

## §13.4 Recommendation for a real Go SPMD implementation

**Do not ship the `*Within` family in v1.** Zero measured benchmark benefit, non-trivial type-checker and codegen complexity (including the const-only enforcement, the AVX2 table duplication interaction, and scalar fallback paths).

**Do ship `lanes.Broadcast`, `lanes.Count[T]()`, and `lanes.Index()`.** They compile to splats and constants, cost nothing, and are occasionally useful for chunk sizing and lane-independent structure.

**Maybe ship full-width `lanes.Rotate` with a compile-time-constant offset** if you have a specific benchmark that demonstrates a win. Cache-line rotation tricks for circular buffers are a candidate. But gate even this on concrete evidence.

**Do not ship runtime-indexed `lanes.Swizzle` in v1.** It compiles to per-lane extract/insert, which is slow, and the cases where it's needed are almost always better handled by pattern detection (byte-decomposition store, `vpshufb`-recognizable lookups).

You can add more cross-lane primitives later when a real benchmark demands them. This is the opposite of the "future-proof API surface" instinct, and it is the right instinct here: unused builtins are a tax on every future compiler change, and the evidence says you don't need them to win on realistic benchmarks.

---

# §14 Techniques we considered novel but aren't

For honesty, let's enumerate the techniques that *feel* novel when you're working on them but turn out to be well-known in prior art:

- **Masked vector load and store.** LLVM has had `llvm.masked.load` / `llvm.masked.store` intrinsics since the early 3.x series. ARM SVE, Intel AVX-512, and RISC-V V all ship hardware support. This is infrastructure, not research.
- **Predicated SSA itself.** Carter, Ferrante, and Hall, "Predicated Static Single Assignment," PACT 1999. The core transform — linearize control flow, track masks, merge phis as selects — is 25 years old.
- **Loop peeling.** A classical compiler transform. The LLVM pass `LoopUnroll` has been doing it since the early versions.
- **Vector reductions.** Hardware primitive on every SIMD-capable ISA back to SSE. LLVM exposes them as `llvm.vector.reduce.*` intrinsics.
- **Gather and scatter.** Intel AVX2 gather, Intel AVX-512 scatter, ARM SVE gather/scatter, RISC-V V indexed load/store. Hardware infrastructure.
- **The ISPC-style control-flow restrictions** (no return/break under varying, continue always OK). Taken almost verbatim from ISPC.

**What is novel in this PoC** is none of these things individually. It is the integration:

- **Predicated SSA × Go's type system.** Making varying-ness a first-class metadata attribute on SSA nodes (`If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`) so that predication can skip uniform control flow efficiently, rather than predicating everything.
- **Peeling × all-ones fast path.** Recognizing that the main body's mask can be statically known, and exploiting that in the backend to skip load-blend-store dance.
- **Pattern detection replacing builtins.** The `vpmaddubsw`/`vpmaddwd` detector (§7) and the byte-decomposition store detector (§8) replacing entire families of per-case builtins.
- **Decomposed index for byte iteration.** The scalar-base + `<N x i8>` offset combination for wide-SIMD byte-granular iteration (§2).

These are engineering contributions, not algorithmic ones. But they are what make the PoC's numbers possible, and they are reproducible in any future implementation that takes them on board.

---

# §15 Research follow-ups

Things we didn't do, would do next, or consider worthwhile areas for further work:

## §16.1 Outer-SPMD batching as a language feature

The IPv4 parser demonstrated that inner-SPMD hits a ceiling for algorithms whose per-instance work is too small. The architectural fix is to SPMD across instances instead of within them:

```go
// Speculative syntax.
go for i, addr := range ipStrings {
    result[i] = parseAddress(addr)  // parseAddress runs in a single lane;
                                    // many addresses process concurrently.
}
```

This requires the type system to handle "a slice of strings, viewed as a varying string." That in turn requires a notion of varying string or varying slice-header. We did not implement this in the PoC; it was scoped out as future work. A production implementation should probably tackle it, because it unlocks a large class of parser/validator workloads.

## §15.2 Auto-detection of byte-parallel algorithms

The decomposed index path (§2) is enabled heuristically for range-over-slice loops at lane count > 4. A more sophisticated version could analyze the loop body and decide based on whether the operations benefit (e.g., whether the body accesses the slice at byte granularity and would profit from byte-wide lanes).

Currently, the heuristic is right for every example we tested. A production implementation might hit edge cases where it's wrong; investing in better analysis is a useful hardening step.

## §15.3 Declarative pattern-detector rule table

Our pattern detectors for `vpmaddubsw`/`vpmaddwd` and byte-decomposition store are written as imperative Go code. They work but are hard to extend and hard to test in isolation.

A declarative rule table — something like LLVM's TableGen for instruction patterns — would make it much easier to add new patterns, to verify they don't conflict, and to document what each pattern matches. This is an infrastructure investment that pays off as the number of detectors grows.

## §15.4 Runtime SIMD dispatch

The PoC compiles to a single target per build. A real deployment might want to compile a single binary that detects SIMD capability at runtime and dispatches to the fastest available implementation. ISPC supports this via its `--target=avx2-i32x8,sse4-i32x4` syntax, which produces multi-version binaries.

We prototyped a "browser SIMD detection demo" that would pick the right WASM module at load time based on WebAssembly feature detection; it was deferred in favor of bringing up x86 AVX2 first. Future work.

## §15.5 Closing the ~23% gap to simdutf

Base64 AVX2 at ~77% of simdutf — what's in the remaining ~23%? We believe it's a combination of:

- **Function inlining**, which we didn't aggressively pursue. simdutf is a C++ header library and inlines everything.
- **Decode-phase tuning.** The PoC does the decode phase (ASCII → sextets) with a LUT-and-correction approach. simdutf uses a different lookup strategy that may be slightly faster on modern microarchitectures.
- **Loop unroll factors.** simdutf unrolls the outer loop aggressively; the PoC's cascading kernel structure unrolls only within `decodeAndPack`.

None of these require novel techniques. They are productization details that a serious implementation would pursue.

---

# §16 Evidence appendix

## §16.1 Benchmark headline numbers

All numbers are from the PoC's benchmark scripts (`test/e2e/spmd-benchmark.sh` for WASM, `test/e2e/spmd-benchmark-x86.sh` for x86-64 native). Speedups are vs. scalar Go compiled from the same source with `-simd=false`, unless noted otherwise.

### §16.1.1 Base64 Mula-Lemire decoder (the flagship)

| Target | Throughput (MB/s) | Notes |
|---|---|---|
| x86 AVX2 | **~17000** | ~77% of simdutf C++ (~22000 MB/s); ~9× Go stdlib `encoding/base64` (~1900 MB/s) |
| x86 SSSE3 | **~8500** | |
| WASM simd128 (wasmtime) | **6004** | |

Hot-loop instructions per byte: **0.44**, vs. 14.3 in the scatter-gather v1 decoder.

### §16.1.2 AVX2 8-wide i32 lanes

| Workload | Speedup |
|---|---|
| lo-min | **7.27×** |
| lo-max | **7.18×** |
| mandelbrot (int32) | **6.07×** |
| lo-sum | **5.09×** |
| lo-clamp | **4.82×** |
| lo-mean | **3.66×** |

Theoretical peak for 8 lanes is 8×; lo-min and lo-max are at ~91% of peak, which is excellent.

### §16.1.3 SSE 4-wide i32 lanes

| Workload | Speedup |
|---|---|
| mandelbrot | **3.71×** |
| hex-encode Dst | **6.31×** |
| lo-min | **2.63×** |
| lo-max | **2.59×** |
| lo-sum | **2.61×** |
| lo-clamp | **2.39×** |

Hex-encode on SSE exceeds the 4-wide theoretical limit because the recognizer hits 16 lanes at byte granularity.

### §16.1.4 WASM simd128

| Workload | Speedup |
|---|---|
| hex-encode Dst | **~8.9×** |
| mandelbrot (int32) | **~3.03×** |
| lo-sum / lo-mean / lo-min / lo-max | **~2.3–2.4×** |
| lo-clamp | **~2.82×** |

## §15.2 Commit SHAs per technique

Selected commits that document the major techniques in this document. All from the SPMD workspace at `/home/cedric/work/SPMD`.

| Technique | Commit / date |
|---|---|
| Decomposed index path on all targets | 2026-03-31 (TinyGo `spmdIsWASM()` gate removal) |
| `vpmaddubsw` / `vpmaddwd` pattern detection | 2026-04-06 |
| `lanes.DotProductI8x16Add` removal | 1df19e8 |
| Byte-decomposition store | 2026-04-12 (commits around base64 v2) |
| CompactStore / SPMDMux / SPMDInterleaveStore removal | cc52618 / e41cdc4 (2026-04-12) |
| AVX2 `vpshufb` table duplication | 2026-03-31 |
| Mask stack removal | 2026-03-05 |
| Deferred mask type resolution | 2026-03-30 |
| `predicateSPMDFuncBody` enabled | 2026-03-03 |
| SSA-level store merging | 2026-03-23 |

## §15.3 Source file index

Primary files for each technique, with `file:line` anchors.

| Technique | Location |
|---|---|
| Predicated SSA scope | `x-tools-spmd/go/ssa/spmd_predicate.go:419` |
| Predicated SSA func body | `x-tools-spmd/go/ssa/spmd_predicate.go:78` |
| Loop peeling | `x-tools-spmd/go/ssa/spmd_peel.go:237` |
| `SPMDLoopInfo` struct | `x-tools-spmd/go/ssa/ssa.go:411` |
| `If.IsVarying` + helpers | `x-tools-spmd/go/ssa/spmd_varying.go:17, 115, 140` |
| SSA store merging | `x-tools-spmd/go/ssa/spmd_predicate.go:3795` |
| Decomposed index path | `tinygo/compiler/spmd.go:902-907, 1091` |
| `SIMDRegisterSize` | `tinygo/compileopts/config.go:119` |
| Deferred mask resolution | `tinygo/compiler/spmd.go:2625, 2637, 8030` |
| Contiguous analysis | `tinygo/compiler/spmd.go:4828-4863, 4870` |
| All-ones fast path | `tinygo/compiler/spmd.go:4626-4727` |
| AVX2 swizzle table duplication | `tinygo/compiler/spmd.go:6537-6547` |
| Byte-decomposition store | `tinygo/compiler/spmd.go:7670-7679` |
| `vpmaddubsw`/`vpmaddwd` detector | `tinygo/compiler/spmd.go:8974-8988` |
| `spmdX86Pmaddubsw` / `spmdX86Pmaddwd` | `tinygo/compiler/spmd_x86.go:57, 87` |

## §15.4 Selected prior art references

- Carter, L., Ferrante, J., and Hall, M. "Predicated Static Single Assignment." PACT 1999.
  The foundational reference for mask-based control-flow linearization in SSA. Our use is mixed-uniform/varying rather than fully-scalar input; the core transform is theirs.
- **Intel ISPC** (https://ispc.github.io/). Our control-flow restrictions (return/break/continue semantics under varying conditions) are taken directly from ISPC. Our approach to `foreach` and programCount is structurally similar. Our novelty is in the Go-specific integration.
- **simdutf** (https://github.com/simdutf/simdutf). Our base64 decoder benchmark target. The library is hand-written C++ SIMD by Daniel Lemire and collaborators; our PoC reaches ~77% of simdutf's throughput on AVX2 by using idiomatic Go plus pattern recognition.
- **Mula and Lemire**, "Base64 encoding and decoding at almost the speed of a memory copy." Software: Practice and Experience, 2018. The algorithmic basis of our base64 v2 decoder.
- **LLVM masked memory intrinsics** — `llvm.masked.load`, `llvm.masked.store`, `llvm.masked.gather`, `llvm.masked.scatter`. Infrastructure, not research, but essential for the backend.

---

*For hands-on implementation details and the narrative of what we tried and rejected, see `implementer-notes.md`. For user-facing guidance on writing SPMD Go, see `developer-guide.md`.*
