# SPMD-for-Go: Implementer Notes

*Lessons from a proof-of-concept Go-SPMD compiler built across a Go fork, a TinyGo fork, and a patched `golang.org/x/tools`. Written for compiler engineers contemplating a real upstream implementation.*

---

# §1 Executive summary

We set out to answer one question: **can Go express data-parallel loops idiomatically, compile them through an LLVM backend to efficient SIMD code, and compete with hand-written C++ SIMD libraries on realistic workloads?** The answer is yes, with caveats worth writing down.

## §1.1 What we shipped

- **Frontend** on a Go fork (`go/` submodule, branch `spmd`): lexer/parser/type-checker support for `lanes.Varying[T]` as a package-qualified "magic generic," `go for` as an SPMD loop construct, full ISPC-style control-flow rules, and mirrored support in both `cmd/compile/internal/types2` and `go/types`.
- **SSA layer** in a patched `golang.org/x/tools/go/ssa` (`x-tools-spmd/`): first-class metadata for varying conditions (`If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`), `SPMDLoopInfo` with two-phase population, SSA-level loop peeling, SSA-level predication replacing a prior backend mask stack, SSA-level consecutive-store merging.
- **Backend** on a TinyGo fork (`tinygo/` submodule, branch `spmd`): direct LLVM IR generation for three targets — WASM simd128, x86 SSE, x86 AVX2 — with deferred mask type resolution, a decomposed index path for byte iteration on wide SIMD, contiguous access analysis through `ChangeType`/`BinOp` chains, an all-ones mask fast path, and pattern detectors that recognize `vpmaddubsw`/`vpmaddwd` and byte-decomposition stores.
- **Stdlib packages** `lanes` and `reduce`: ~25 builtins total (of which we recommend shipping fewer than half in v1; see §7).
- **Scalar fallback mode**: `-simd=false` compiles the same source to scalar code with laneCount 1, used as a correctness oracle.
- **102 end-to-end tests** across eleven levels in `test/e2e/spmd-e2e-test.sh`, with 90 run-pass, 91 compile-pass, 0 compile-fail, 0 run-fail, 11 reject-OK.

## §1.2 Headline numbers

All speedups are vs. scalar Go compiled from the same source with `-simd=false`, unless noted.

| Workload | Target | Speedup / throughput | Notes |
|---|---|---|---|
| Base64 Mula-Lemire decoder | x86 AVX2 | **~17 GB/s** | ~77% of simdutf C++ (~22 GB/s); ~9× Go stdlib `encoding/base64` (~1.9 GB/s) |
| Base64 Mula-Lemire decoder | x86 SSSE3 | **9201 MB/s** | |
| Base64 Mula-Lemire decoder | WASM simd128 (wasmtime) | **6004 MB/s** | |
| Hex-encode (Dst) | WASM simd128 | **8.9×** | |
| Hex-encode (Dst) | x86 SSE | **6.31×** | |
| Mandelbrot (int32) | x86 AVX2 | **6.07×** | |
| Mandelbrot (int32) | x86 SSE | **3.71×** | |
| Mandelbrot (int32) | WASM simd128 | **3.03×** | |
| lo-min / lo-max | x86 AVX2 8-wide i32 | **7.27× / 7.18×** | near theoretical 8× peak |
| lo-sum / lo-clamp / lo-mean | x86 AVX2 | 5.09× / 4.82× / 3.66× | |

The base64 numbers are the interesting ones. We reach ~77% of Daniel Lemire's hand-tuned C++ SIMD library (simdutf) by writing idiomatic Go and letting the compiler recognize patterns. There is room to close the gap, but the result already demonstrates that SPMD Go can compete in the same ballpark as expert-written C++ SIMD on a realistic workload.

## §1.3 The single biggest lesson

**Vectorization logic belongs at the SSA level — as predication, peeling, and pattern detection — not as a bag of unconnected vector opcodes.** We spent Phase 1 adding 42 vector opcodes to `cmd/compile/internal/ssa/_gen/genericOps.go`. In the PoC, those opcodes were never exercised because TinyGo uses `golang.org/x/tools/go/ssa` (the public SSA package), not `cmd/compile` internals. All the vectorization work that actually produced results — predication, loop peeling, store merging, contiguous access analysis — was developed in our patched copy of `golang.org/x/tools/go/ssa` (`x-tools-spmd/`).

**For a real upstream into the Go compiler**, the work would not go into `golang.org/x/tools/go/ssa` (which is a read-only analysis package in the main Go toolchain). It would go into `cmd/compile/internal/ssa` — the same SSA that the Go compiler already uses for optimization and codegen. The 42 opcodes we added in Phase 1 were the wrong starting point (a flat list of vector ops with no structure), but the right *location*. An upstream implementation should rework `cmd/compile/internal/ssa/_gen/genericOps.go` by taking the patterns that worked in our `x-tools-spmd` experiment — `SPMDLoopInfo`, `SPMDLoad`/`SPMDStore`/`SPMDSelect` with explicit masks, `If.IsVarying` metadata, predicated scope transforms, SSA-level loop peeling — and express them as proper `cmd/compile/internal/ssa` infrastructure. The public `go/ssa` package served as our prototyping ground; `cmd/compile/internal/ssa` is where the production implementation lives.

## §1.4 What to read in this document

- §2 covers the frontend. If you care about the type system and the "`lanes.Varying[T]` is a compiler-magic generic" decision, read this.
- §3 covers the SSA strategy. If you want to understand how predicated SSA plugs into Go's control flow, this is the core contribution.
- §4–§5 cover the TinyGo backend — target-independent lowering and target-specific gotchas. Skip §5 if you're not planning to emit LLVM IR.
- §6 is the mask-stack-abandonment story. Short, opinionated, skip-able.
- §7 is a hall of fame of features we removed. Read if you want to save yourself from building things that don't pay off.
- §8 is engineering process. Read before deciding how to structure the implementation team.
- §9 is a one-pager of what to steal and what to redesign.
- §10 is a source-file atlas for navigation.

---

# §2 Frontend: `lanes.Varying[T]` as compiler-magic

## §2.1 Why package-based beat a keyword

Our first design used a `varying` keyword and a constrained type `Varying[T, N]` (with N the explicit lane count). We abandoned both.

**Why we dropped the keyword.** Any new reserved word breaks old Go code that uses `varying` as an identifier. It also requires lexer changes, which ripple into every downstream tool (gopls, goimports, vet, tree-sitter grammars, syntax highlighters, generator authors). The cost is real and falls on people who don't care about SPMD.

**Why `lanes.Varying[T]`.** It parses as a plain generic index expression — `PkgName.TypeName[TypeArg]`. The frontend type checker special-cases it when resolving the index expression: if the callee is the magic type `lanes.Varying`, dispatch to the SPMD code path and synthesize a `*types2.SPMDType` (or `*types.SPMDType` in the `go/types` mirror). Tooling works unchanged; the grammar is unchanged; `gopls` can see that you're inside a generic invocation and does the right thing.

**Why we dropped constrained `Varying[T, N]`.** The N parameter was lane count. We thought developers would want to write `Varying[float32, 4]` to guarantee 4-wide vectors. In practice, N was unused: backends pick their native width anyway, and forcing a mismatch just triggers scalarization. The constrained form added complexity to every type rule with zero measured benefit. We deleted it mid-project.

**Recommendation:** ship `lanes.Varying[T]` as a compiler-magic single-parameter generic. Do not introduce `varying` as a keyword. Do not ship a constrained variant.

## §2.2 The extension-file pattern

Every SPMD-specific frontend rule lives in a file whose name ends with `_ext_spmd.go`. This makes the SPMD work trivially separable from the rest of the type checker, and it makes merging with upstream Go much easier.

Examples in `go/src/cmd/compile/internal/types2/`:

- `typexpr_ext_spmd.go:17` — `handleSPMDIndexExpr()`, the entry point that catches `lanes.Varying[T]` index expressions and routes them to `processLanesVaryingType()`.
- `check_ext_spmd.go:96` — `simd128CapacityBytes = 16`, the baseline lane-count constant.
- `stmt_ext_spmd.go` — return/break rules (`varyingDepth`, `maskAltered`) and nested `go for` checks.
- `call_ext_spmd.go` — public API restriction (varying parameters only on private functions).
- `pointer_ext_spmd.go`, `expr_ext_spmd.go`, `typestring_ext_spmd.go` — pointer-of-varying handling, varying array indexing, type formatting.

Every one of these files is mirrored in `go/src/go/types/` with identical logic. **This is the single most painful thing about working in Go's type checker today:** `cmd/compile/internal/types2` and `go/types` are two near-duplicate trees. Every SPMD rule we added had to be written twice, reviewed twice, tested twice. If you are planning to upstream this work, **unify types2 and go/types first, or accept that every SPMD contribution is a double-write.**

## §2.3 `go for` syntax

A single flag on `ForStmt` is enough:

```go
// go/src/cmd/compile/internal/syntax/nodes.go, around line 415
type ForStmt struct {
    Init     SimpleStmt // incl. *RangeClause
    Cond     Expr
    Post     SimpleStmt
    Body     *BlockStmt
    IsSpmd   bool  // true for `go for`
    LaneCount int64 // 0 = unset, set by type checker
    simpleStmt
}
```

The parser sets `IsSpmd = true` when it sees the `go` keyword immediately followed by `for` (not `func(`). Disambiguation is a single-token look-ahead: if the token after `go` is `for`, it's an SPMD loop; otherwise it's a goroutine launch.

`LaneCount` is filled in later by the type checker once it knows the loop body's varying element type and hence the target lane count. It is **not** a user-visible parameter — developers cannot write `go for[4] i := range n`. The compiler decides.

The mirror in `go/src/go/ast/ast.go` and `go/src/go/parser/parser.go` follows the same structure.

## §2.4 Type system rules (ISPC-derived)

We took our control-flow rules from Intel ISPC, which defined and validated them in production over many years. The rules are:

1. **Return/break forbidden under varying conditions**, or after mask alteration. Rationale: "which lanes would return?" has no clean answer when lanes diverge. Enforced in `stmt_ext_spmd.go` via a `varyingDepth` counter that tracks enclosing varying `if`/`switch` and a `maskAltered` flag that tracks whether a prior `continue` has pruned the mask.
2. **Nested `go for` forbidden.** Inside a `go for`, all values are either uniform or varying of the outer lane width; a nested `go for` would need to pick a lane width for its own iteration, and the composition is ambiguous. Outer-SPMD batching (§7 of the developer guide) is the correct alternative.
3. **SPMD functions cannot contain `go for`.** A "SPMD function" is one whose signature has a varying parameter. Such a function is already being called under a mask by its caller; starting a new `go for` inside would try to re-broadcast data already in varying form.
4. **Public API restriction.** Functions with varying parameters must be unexported. Rationale: masks and lane counts are implementation details, not API. A library that exports `Frobnicate(v lanes.Varying[int32])` leaks the fact that it uses SPMD; calling it from non-SPMD code makes no sense. Enforced in `call_ext_spmd.go`.
5. **`continue` is always allowed.** It just narrows the mask for the rest of the iteration; no soundness issue.

These rules are **load-bearing for soundness.** Relaxing any of them leads to code that compiles but miscompiles. Do not ship a v1 without them.

## §2.5 Lane count is a compile-time constant

```go
// go/src/cmd/compile/internal/types2/check_ext_spmd.go:96
const simd128CapacityBytes = 16
```

Used at lines 105, 123, 134, 157 to compute `laneCount = simd128CapacityBytes / elemSize`. This is the frontend's **minimum guaranteed lane count** — 16 lanes of i8, 4 lanes of i32, 2 lanes of i64, on any SIMD-capable target.

**Wart:** the constant is hard-coded 128-bit even though the backend targets AVX2 (256-bit) and AVX-512 (512-bit). The frontend promises "at least this many lanes"; the backend widens as appropriate. This works because SPMD semantics are lane-count-agnostic by design — well-written user code produces the same result regardless of how wide the backend chooses to go.

**A cleaner design** would parameterize the constant by target, or better, refuse to commit to any specific lane count in the frontend at all and let the backend decide entirely. We didn't do that because the frontend needs *some* notion of lane count to reject user code that uses `lanes.Count[T]()` in a context where the count is required at type-check time (e.g., compile-time constant array sizes). Think carefully about this before v1.

## §2.6 Type extensions that surprised us

Once `Varying[T]` exists, a whole lattice of derived types follows. We retrofitted each of these painfully:

- **`&Varying[T]` → `Varying[*T]`.** Taking the address of a varying value produces a varying pointer. Dually, dereferencing a varying pointer gives you back a varying value: `*Varying[*T]` → `Varying[T]`. Fixed in `pointer_ext_spmd.go` (both types2 and go/types), then `x-tools-spmd/go/ssa`, then `tinygo/compiler/spmd.go` per-lane GEP expansion. A four-layer fix.
- **`Varying[[N]T][i]` → `Varying[T]`.** Indexing a varying fixed-size array returns a varying element. Fixed in `expr_ext_spmd.go`.
- **`*Varying[Struct].Field` → `Varying[FieldType]`.** Field access through a pointer to a varying struct. Fixed in `pointer_ext_spmd.go` + `expr_ext_spmd.go`.

Each one took a week to propagate through all four layers. **Design these in from day one.** Varying is a functor over types — build the full lattice (pointers, arrays, structs, slices-of) up front, not as retrofits.

## §2.7 What we'd cut from the frontend in v1

- **The 42 vector SSA opcodes** in `cmd/compile/internal/ssa/_gen/genericOps.go` as originally designed. They were a flat list of unstructured vector ops. An upstream implementation should replace them with the structured approach proven in the PoC: `SPMDLoopInfo`, explicit-mask `SPMDLoad`/`SPMDStore`/`SPMDSelect`, predication transforms, and SSA-level loop peeling — all expressed as proper `cmd/compile/internal/ssa` infrastructure.
- **Constrained `Varying[T, N]`.** Already removed. Don't re-introduce.
- **`lanes.DotProductI8x16Add`.** Added as a builtin for the IPv4 parser, removed (commit 1df19e8) once `vpmaddubsw` pattern detection handled the general case. ~163 lines gone. Moral: pattern detectors generalize; per-case builtins don't.
- **`*Within` family** (`RotateWithin`, `ShiftLeftWithin`, `ShiftRightWithin`, `SwizzleWithin`). See §7.4 for why — zero measured benchmark wins.

---

# §3 SSA strategy: predicated SSA (prototyped in `go/ssa`, target `cmd/compile/internal/ssa`)

## §3.1 The key insight

We do not need a zoo of vector opcodes. We need **three** SPMD-aware SSA instructions — `SPMDLoad`, `SPMDStore`, `SPMDSelect` — plus **metadata** to tell the predication pass where varying control flow begins and ends. That metadata is:

- `SPMDLoopInfo` on `Function` — describes each `go for` loop.
- `If.IsVarying` flag — marks a conditional branch as dependent on a varying value.
- `SPMDSwitchChain` — groups the lowered-switch `If` instructions of a varying switch into one logical unit.
- `SPMDBooleanChain` — captures the block structure of a varying `&&`/`||` expression.

With those four pieces, the predication pass can walk any Go function, linearize its varying control flow into masked selects, and hand the backend a CFG where every vector-relevant decision is explicit.

## §3.2 `SPMDLoopInfo`

Lives on `ssa.Function` (defined in `x-tools-spmd/go/ssa/ssa.go` around line 411):

```go
type SPMDLoopInfo struct {
    EntryBlock  *BasicBlock
    BodyBlock   *BasicBlock
    LoopBlock   *BasicBlock
    DoneBlock   *BasicBlock
    IterPhi     Value       // resolved post-lift
    BoundValue  Value
    LaneCount   int
    IsRangeIndex bool       // `for i := range slice` vs `for i := range N`
    Accumulators []*SPMDAccumulator

    // Set by peelSPMDLoops:
    IsPeeled        bool
    MainBodyBlock   *BasicBlock
    TailCheckBlock  *BasicBlock
    TailBodyBlock   *BasicBlock
    TrampolineBlock *BasicBlock
    AlignedBound    Value
    TailMask        Value
    MainIterPhi     Value
    TailIterPhi     Value
}
```

**Two-phase population.** Block pointers are populated during SSA construction, by `spmdLoopConstruction` in `spmd_loop.go`. But `IterPhi` can't be known at construction time because `ssa.lift()` runs afterwards and replaces the loop's alloca-based counter with a proper phi. So `IterPhi` is resolved in a second pass, `resolveSPMDLoopPhis`, called after `lift`. It finds the phi by matching the comment propagated from the original alloca.

**Accumulator tracking.** Loop-carried varying values (the running sum in a reduction, the per-lane min/max) are tracked as `SPMDAccumulator` structs so that peeling and predication can route them through the correct phis in both the main and tail bodies.

## §3.3 Varying conditions as first-class metadata

Consider this Go code:

```go
go for i, x := range xs {
    if x > threshold {
        out[i] = compute(x)
    }
}
```

The condition `x > threshold` is varying (because `x` is varying). We need the SSA to reflect this so predication knows to turn the if into a masked select. The naive approach is to infer varying-ness from operand types during predication; that works for simple cases but breaks on switch lowering (which fans out into chains of ifs) and on `&&`/`||` (which become block chains).

**Our solution:** mark varying conditions explicitly during SSA construction, before any optimization that might rewrite the CFG.

- `If.IsVarying` is set in `builder.go` at lines 217, 253, 276, using the AST-level helper `exprHasSPMDType(fn, expr)` in `x-tools-spmd/go/ssa/spmd_varying.go:17`. The helper walks the AST of the condition expression and returns true if any subexpression has a varying type.
- `SPMDSwitchChain` is populated at `builder.go:1551, 1605` when the tag of a switch statement has a varying type. It stores the tag value, the list of `If`s generated by switch lowering, the default block, and the join ("done") block.
- `SPMDBooleanChain` is populated at `builder.go:212-219` (for `&&`) and `builder.go:248-255` (for `||`), using a stack-based accumulator in `cond()` with shared-target matching. The chain records the operator, the list of short-circuit blocks, the final `then` block, the final `else` block, and whether it's varying.

### §3.3.1 The block-pointer invalidation trap

**Critical bug we discovered late:** all this metadata points into the CFG. Then `optimizeBlocks()` runs — it deletes empty blocks, merges straight-line chains, renumbers everything. Our metadata pointers go stale.

Fix: two resolution passes that run immediately after `optimizeBlocks`:

```go
// x-tools-spmd/go/ssa/spmd_varying.go around lines 115-140
resolveSPMDSwitchChains(fn)
resolveSPMDBooleanChains(fn)
```

Each resolver walks the current CFG and re-discovers which blocks correspond to the logical roles (tag, cases, default, done). The resolvers use invariants that survive block merging (e.g., "the done block is the unique post-dominator of all case blocks").

**Practical lesson for any future `go/ssa` consumer:** if you add metadata that holds block pointers, add a resolver hook that runs after every optimization pass you plug in. Design it up front or you will rediscover this bug the hard way.

## §3.4 Predication

Two entry points, both in `x-tools-spmd/go/ssa/spmd_predicate.go`:

- `predicateSPMDScope` at line 419 — runs on the scope inside a `go for` loop body. This is the common case.
- `predicateSPMDFuncBody` at line 78 — runs on the entire body of an SPMD function (one whose signature has a varying parameter).

They share helpers (`spmdRelocateToBlock`, boolean chain resolution, accumulator handling) and produce identical output shapes. The difference is scope: a `go for` predicates only its loop body; an SPMD function predicates everything.

**Call sequence** in `func.go:417, 443`:

```
optimizeBlocks
  → resolveSPMDSwitchChains
  → resolveSPMDBooleanChains
  → peelSPMDLoops       (§3.5)
  → spmdMergeRedundantStores  (§3.6)
  → predicateSPMDScope / predicateSPMDFuncBody
```

After this pipeline, the SSA passed to TinyGo has:
- All varying `if`s replaced by `SPMDSelect(cond, then_val, else_val)`.
- All varying switches linearized into sequences of masked stores/selects.
- All varying `&&`/`||` collapsed into mask expressions.
- All memory ops tagged with explicit masks (on `SPMDLoad`/`SPMDStore` instructions).

**TinyGo's job shrinks to "consume the SSA mechanically."** That's the whole point of doing predication at the SSA level.

### §3.4.1 What a predication transform does to Go code

`if v { A } else { B }` with varying `v`, starting mask `m`:
1. Execute `A` under `m & v`: both branches' writes become masked stores.
2. Execute `B` under `m & ~v`.
3. Every phi at the merge point becomes `SPMDSelect(v, a_val, b_val)`.

`switch v { case 1: A; case 2: B; default: C }` with varying `v`:
1. Fan out to a chain of `If(v == 1)`, `If(v == 2)`, linked as an `SPMDSwitchChain`.
2. Each case block executes under its case mask.
3. The default block executes under `~(case1 | case2)`.
4. Done block merges via `SPMDSelect` chains.

`a && b` with varying operands follows short-circuit semantics: execute `b` only on lanes where `a` is true, then combine with mask `a & b`. `a || b` is the dual. The block-level structure is captured in `SPMDBooleanChain`.

## §3.5 SSA-level loop peeling

```go
// x-tools-spmd/go/ssa/spmd_peel.go:247
func peelSPMDLoops(fn *Function) {
    for _, loop := range fn.SPMDLoops {
        if loop.LaneCount <= 1 {
            continue // scalar fallback — peeling is a no-op
        }
        peelSPMDLoop(fn, loop)
    }
}
```

Every `go for` loop is split into four blocks:

- **MainBodyBlock.** Executes `floor(N / laneCount)` iterations. The mask is **statically all-ones** — no per-iteration masking.
- **TailCheckBlock.** Branches to TailBodyBlock iff there's a partial iteration left.
- **TailBodyBlock.** Executes at most once. The mask is a runtime-computed bitmap selecting the remaining lanes.
- **TrampolineBlock.** Routes phi values (accumulators, loop-carried varying values) from main to tail to done.

### §3.5.1 Why peeling is the single highest-leverage optimization

Unpeeled, every loop iteration pays a "what's my mask?" cost. For memory ops, that means load-blend-store: load the existing vector, blend in the new values at the active-lane positions, store it back. Three memory ops for one logical store.

Peeled, the main body knows the mask is `ConstAllOnes`. Every `SPMDStore` in the main body becomes a single direct vector store. Every `SPMDLoad` becomes a direct vector load. The peeled tail is executed at most once, so its cost is amortized.

This is where **most of the benchmark win comes from.** Hex-encode, base64, mandelbrot, lo-* all benefit. The tail mask handles correctness; the main body gets the speed.

### §3.5.2 Why at the SSA level and not at LLVM

LLVM has a loop-unroll pass and a vectorizer. Neither can help here:

- LLVM's loop unroll doesn't know the mask semantics. It would unroll masked operations as though they were scalar, losing the mask structure.
- LLVM's auto-vectorizer can't see across a Go slice header. Even on plain Go, the vectorizer gives up on anything non-trivial.
- A runtime-computed tail mask is hard for LLVM to materialize correctly. We always got better code by building it ourselves in SSA.

At the SSA level, peeling is a structural transformation: create four new blocks, rewire phis, done. The backend compiles each block in isolation without having to know it was peeled.

## §3.6 SSA-level store merging

After predication, it's common to have several `SPMDStore` instructions targeting the same logical address, each guarded by a different mask — one per branch of a varying `if`, for example. Three masked stores when one would do.

`spmdMergeRedundantStores` in `spmd_predicate.go:3795` (with helpers at 3902, 4001, 4072) walks the CFG, groups consecutive stores by `(X, Index)` — the base pointer and the index value, with pointer equality on X (all SSA values are hash-consed, so this is safe) and structural equality on Index — and merges each group into a single store whose value is a chain of `SPMDSelect`s and whose mask is the OR of the group's masks.

Cross-block merging (`spmdMergeStoreGroupCrossBlock`, line 4072) extends the transform across block boundaries when one block dominates another.

### §3.6.1 Why at the SSA level and not at LLVM

LLVM's store-forwarding and dead-store-elimination passes do not understand masked stores. From LLVM's point of view, a masked store is an opaque intrinsic call with arbitrary side effects. It cannot forward, combine, or eliminate them.

At the SSA level, we know the semantics and can combine freely. After merging, the IR passed to LLVM contains fewer intrinsic calls and LLVM's normal passes handle the rest.

## §3.7 Accumulator phis and break results

Two subtleties that aren't visible in the above but cost us weeks:

- **Accumulator phis.** Loop-carried varying values (running sums, per-lane min, etc.) need phis that survive peeling. The phi that was at the top of the loop now has three predecessors: the loop header (for the first iteration of the main body), the main body's tail (for the back-edge), and the tail body (for continuing into the tail). The done block gets a phi trampoline that selects between the final main value and the final tail value.
- **Break results.** When an SPMD function contains a varying `break`, the value computed before the break must be preserved per-lane. `predicateVaryingBreaks` at `spmd_predicate.go:3500+` creates a break-mask phi and per-lane result allocas, then relocates the break's "after" code to a done block.

Both are implemented. Both are delicate. Test them exhaustively; they are the two bug dens.

---

# §4 TinyGo backend: target-independent layer

## §4.1 Where integration lives

The SPMD code in TinyGo is concentrated in a handful of files:

- `tinygo/compiler/compiler.go` — main compilation driver, SPMD function entry setup.
- `tinygo/compiler/spmd.go` — **~9000 lines.** Everything SPMD-specific. Too big; see §8.1.
- `tinygo/compiler/symbol.go` — function signature handling (mask parameter).
- `tinygo/compiler/func.go` — function body compilation.
- `tinygo/compiler/interface.go` — varying values as `interface{}`.
- `tinygo/compileopts/config.go` — `SIMDRegisterSize` exposure and `-simd=false` flag.
- `tinygo/loader/list.go` — forces `lanes` and `reduce` stdlib packages into the build.

Everything else is unchanged.

## §4.2 `SIMDRegisterSize` from LLVM features

Defined in `tinygo/compileopts/config.go:119`. Reads the target's LLVM feature string and returns the SIMD register size in bytes:

- `+avx512f` → 64 bytes (512 bits)
- `+avx2` → 32 bytes (256 bits)
- WASM `simd128` → 16 bytes (128 bits)
- scalar fallback → 1 byte (effective laneCount = 1)

Consumed by `spmdRegisterBytes()` in `tinygo/compiler/spmd.go:2681`. Used throughout the backend to parameterize lane counts:

```go
// tinygo/compiler/spmd.go:284
func (c *compilerContext) spmdLaneCount(elemType llvm.Type) int {
    elemSize := c.targetData.TypeAllocSize(elemType)
    if elemSize == 0 {
        return 1
    }
    return int(uint64(c.spmdRegisterBytes()) / elemSize)
}
```

Same principle as the frontend's `simd128CapacityBytes`, but multi-target aware. The `x-tools-spmd/go/ssa` layer also exposes a `SIMDRegisterBits` setting that is plumbed through so that SSA-level decisions (peeling, accumulator width) know the backend's width.

## §4.3 Deferred mask type resolution

**The problem you will absolutely hit.** Two `go for` loops in the same function can have different lane counts — one iterating int32 (4-wide on SSE), another iterating byte (16-wide on SSE). Their masks have different LLVM types. There is no sensible "canonical" mask type for the function.

**The naive fix:** thread a "current mask width" context through every LLVM codegen function. We tried. It is invasive, churny, and error-prone.

**What we did:** resolve mask LLVM types at **materialization points** only. A materialization point is anywhere a mask actually becomes a concrete LLVM value:

- Constant masks (`spmd.go:2625, 2637`). The lane count is determined by the surrounding `go for` or SPMD function body context.
- Phi nodes. The lane count matches the other incoming values.
- Safety narrowing in `createSPMDSelect` (`spmd.go:8030+`). If we're about to build a select from a mask wider than the operands, narrow it.

Everywhere else in the backend, masks are carried as abstract `ssa.Value` references and only converted to LLVM types at the point of actual emission. This is ugly but it works.

**Latent hazard:** `getLLVMType(MaskType{})` on non-WASM targets still returns `i1` (single bit) as a fallback. It's guarded by an early-return in `createConvert`. If that early-return is ever removed or reordered, wrong masks flood the system. Leave a very angry comment.

**Recommendation for upstream:** either thread the mask width explicitly (clean, invasive) or make mask width a first-class part of the SPMD type system so that `Varying[mask[N]]` carries its N. We didn't do either because both imply reworking types2/go/types again, which we couldn't afford mid-PoC.

## §4.4 SPMD function calls as a calling convention

A function with a varying parameter is an "SPMD function." It is compiled as if it had an additional first parameter: the current mask. At every call site, the mask is passed explicitly; at every function entry, it is extracted:

- Mask type computation: `spmdMaskTypeFromSig(sig)` in `compiler.go:2690+`.
- Mask extraction at entry: `compiler.go:1495, 1519, 1541`.
- `spmdFuncIsBody` flag at `compiler.go:196` — true when the entire function body is an SPMD region (the function has at least one varying parameter and contains no `go for`).

**This should be a real calling convention, not a hack.** In the PoC, the mask is a synthetic parameter shoved into the front of the argument list. In a real implementation, tool chains (debuggers, profilers, FFI bridges, reflection) should see it explicitly. We did not go there because the PoC didn't need it.

## §4.5 Scalar fallback mode as correctness oracle

The `-simd=false` flag (in `tinygo/compileopts/config.go`) makes `spmdUsesSIMD()` at `tinygo/compiler/spmd.go:2521-2527` return false. When this is off:

- `SIMDRegisterSize()` reports 1 byte.
- Every lane count becomes 1.
- Every "vector" type becomes a scalar type (`<1 x T>` is just `T`).
- Every mask becomes `i1`.
- Every `SPMDSelect` becomes a scalar `select`; every `SPMDLoad/Store` becomes a scalar load/store.

In scalar fallback, an SPMD program and its non-SPMD equivalent should produce **byte-identical output.** This is the correctness oracle.

**Dual-mode E2E** in `test/e2e/spmd-e2e-test.sh:720` (Level 8) builds every example twice and diffs the output. Level 9 is stricter: tests marked as "scalar-validated" must also match a hand-written scalar reference. Any divergence is a bug.

### §4.5.1 Crashes we had to fix to make scalar mode work

Building scalar mode exposed a tail of assumption bugs. Each of these was a five-minute fix once found, but they were hidden until we ran scalar mode:

- `vectorToArray` and `arrayToVector` had paths that assumed laneCount > 1 and produced malformed LLVM types at laneCount 1.
- `MakeInterface` on a varying value assumed a vector layout; with laneCount 1 the "vector" is a scalar and the layout is different.
- `splatScalar` optimized for laneCount 1 by just returning the scalar, which broke type matching downstream.
- `reduce.Add` (and friends) matched by a naming convention that broke when scalar mode inlined differently.
- `lanes.DotProductI8x16Add` simply didn't have a scalar path; we removed the builtin entirely (see §7.2).

**Do not ship without scalar mode.** It is not optional. It is the only automated correctness check that catches entire classes of bugs that all pass under SIMD and all fail under 1-lane execution.

---

# §5 TinyGo backend: target-specific gotchas

## §5.1 Decomposed index path for byte iteration on wide SIMD

This is the PoC's most important backend contribution, and the most important single technique to carry forward. See also `novel-patterns.md` §2 for the research framing.

**The problem.** Consider `for i, b := range byteSlice` on AVX2, where lane count is 32 (one per byte in a 256-bit register). The iteration variable `i` is a vector of 32 indices. If we materialize it as `<32 x i64>`, that's 256 bytes of register state just for the index. If we use it as input to a gather GEP, LLVM's scalarization pass takes over and emits 32 separate loads. Dead on arrival.

Worse: if we narrow the index to `<32 x i32>` or `<32 x i8>` to save register state, LLVM's GEP vectorization sign-extends it back to `i64` at the scatter point, and the sign-extend is buggy on `<32 x i8>` inputs with certain addend layouts. We named this hex-encode "Bug 2"; fixed 2026-03-31 by introducing the decomposed path.

**The technique.** Keep the iteration index as a *pair*:

- A **scalar base pointer**, incremented by `laneCount * elemSize` per iteration in the main body.
- A **`<N x i8>` lane offset** that is a compile-time constant vector `[0, 1, 2, ..., N-1]`.

Combine them at every GEP site:

```go
// conceptually, for each memory op:
ptr := gep(base, zext_to_i64(lane_offset))
```

The base is a plain scalar pointer — no vector register cost. The offset is 8 bits per lane, so the full lane vector fits in 32 bytes on AVX2 (and in 64 bytes on AVX-512).

On x86-64, the base is i64, but GEPs need an i32 addend on most addressing modes; we truncate before combining. On WASM, pointers are i32 throughout.

**The flag** is `isDecomposed bool` at `tinygo/compiler/spmd.go:902-907`, set at `spmd.go:1091`:

```go
isDecomposed := ssaLoop.IsRangeIndex && laneCount > 4
```

Originally gated behind `spmdIsWASM()`, but we removed the gate on 2026-03-31 — the decomposed path is correct and profitable on every target whenever lane count exceeds 4. WASM (16 lanes for byte), SSE (16 lanes), AVX2 (32 lanes), AVX-512 (64 lanes) — all use it.

**This is what lets SPMD Go iterate `[]byte` with true byte-granular parallelism.** Without it, you either pay the gather/scatter cost or drop to narrower loops.

## §5.2 AVX2 `vpshufb` shuffles each 128-bit half independently

A hardware fact worth knowing: on a 256-bit AVX2 register, `vpshufb` does not permute across the two 128-bit halves. It treats the register as two independent 128-bit shuffles. A 16-byte shuffle table applied to the lower half; the same 16-byte table applied to the upper half.

This breaks naive use of shuffle tables larger than 16 bytes. If you have a 32-byte LUT intended to look up values across the full 256-bit register, `vpshufb` will produce garbage in the upper half.

**The rule:** on AVX2, if `laneCount > 16`, duplicate the 16-byte constant table to a 32-byte `[table, table]`. The lower and upper halves now see the same LUT, and shuffle results are consistent.

Implementation: `spmdWasmSwizzle` at `tinygo/compiler/spmd.go:6547` auto-duplicates when the loop's lane count exceeds 16. Dispatch is in `spmdX86Pshufb` (`tinygo/compiler/spmd_x86.go:9, 39`), which routes to `llvm.x86.avx2.pshuf.b` (256-bit) or `llvm.x86.ssse3.pshuf.b` (128-bit) based on operand width.

**Where it bites:** any byte-granularity lookup. Hex encoding's nibble-to-char table. Base64's alphabet table. URL-encode tables. Without the duplication, the high 128 bits of the result are silently wrong — no crash, just bad data.

## §5.3 Contiguous access analysis — the most important optimization

Most of the benchmark wins come from one compiler question: **"is this memory access contiguous?"** If yes, emit a single vector load/store. If no, fall back to gather/scatter (slow).

Contiguous means: the address is `scalar_base + iter_phi * elemSize` where `iter_phi` is the SPMD loop's iteration variable. In practice, Go code rarely writes this directly — it's hidden behind a sequence of SSA `BinOp ADD`, `ChangeType` (int32↔int64 for range-over-int), and occasional constant folding.

`spmdAnalyzeContiguousIndex` at `spmd.go:4833` recursively unwraps these layers:

```go
func (c *compilerContext) spmdAnalyzeContiguousIndex(index ssa.Value) (
    loop *spmdActiveLoop, scalarBase llvm.Value, ok bool,
) {
    // Peel ChangeType (range-over-int narrowing).
    // Peel BinOp ADD when one side is the iter phi and the other is scalar.
    // Trace back through intermediate phis inside the loop body.
    // Return the loop and the scalar base, or (nil, nil, false).
}
```

A dedicated helper, `spmdUnwrapScalar` at `spmd.go:4870+`, peels ChangeType chains specifically. It exists because the range-over-int case produces SSA like `ChangeType(i64→i32)(add(iter_phi, zero))` and we need to see through it.

**The 38% improvement.** Adding `spmdUnwrapScalar` alone (on top of already-working contiguous detection) gave a 38% speedup on the contiguous-store path (recorded in CLAUDE.md's optimization log). A single additional recognizer case was worth 38% because it meant the difference between "this hot store takes the vector store fast path" and "this hot store falls back to scatter."

**Moral:** invest heavily in the contiguous recognizer. Every percentage point of recognizer coverage is worth more than any other compiler work in this codebase.

## §5.4 The all-ones mask fast path

```go
// tinygo/compiler/spmd.go:4626-4727 (spmdFullStoreWithBlend)
```

This function decides how to emit an `SPMDStore`. The slow path loads the existing vector, blends in the new values at mask positions, stores back. The fast path just does a vector store, no load, no blend.

When can we take the fast path? When the mask is **statically `ConstAllOnes`**. This happens whenever the store is inside a peeled main body (see §3.5), because peeling establishes this invariant.

It also happens when the mask is dynamically all-ones but statically known. We do both.

**Where the benchmark wins come from.** Every hot loop in every example spends ~90% of its time in the peeled main body. Every `SPMDStore` in that main body takes this fast path. That's the difference between "three memory ops per store" and "one memory op per store." For a store-heavy loop (hex-encode, base64), this alone is a ~2× win.

## §5.5 Vector load/store alignment

Small but real gotcha: the alignment attribute on a vector load/store must reflect the **element alignment**, not the vector alignment.

Stack-allocated Go slices can have sub-vector alignment. If you emit a `vmovdqa` (aligned load) on an unaligned stack slice, you get SIGSEGV on x86. On WASM, you get silent corruption because WASM doesn't fault on misaligned accesses but can tear reads across page boundaries.

Fix: `SetAlignment(elemSize)` on every vector load/store. Applied on fullload, fullstore, and shifted-load paths in `spmd.go` around the createSPMDLoad/Store functions. This is what the code does in the base64 and hex-encode hot paths.

**Do not ever emit a vector load/store with vector-size alignment.** Only element-size. LLVM will automatically promote to aligned variants where it can prove it's safe.

## §5.6 Range-over-slice detection and narrowing

Go has two `for` forms that become SPMD loops:

- `go for i := range N` — iterates 0..N. SSA pattern: `rangeint.body` / `rangeint.loop`.
- `go for i, x := range slice` — iterates slice indices. SSA pattern: `rangeindex.body` / `rangeindex.loop`.

They share most of the machinery but the rangeindex form is harder because the phi carries a narrow type (it's the slice index, typically int32 at that point). The body prologue override (`emitSPMDBodyPrologue`) installs `narrowedElemType` and `narrowedPhi` locals so that the body sees the correct vector types when loading from the slice.

A shared helper, `entryPredecessor()`, unifies the two forms. If you're generalizing this to a production compiler, start from `entryPredecessor` — it's the right abstraction.

## §5.7 Byte-decomposition store

```go
// tinygo/compiler/spmd.go:7679 — spmdEmitInterleavedStoreMasked
```

When a `go for` body ends with a series of stride-S stores that together write a wider value to adjacent byte positions:

```go
go for i := range n {
    tmp := compute(...)               // Varying[int32]
    out[i*3+0] = byte(tmp)            // low byte
    out[i*3+1] = byte(tmp >> 8)       // mid byte
    out[i*3+2] = byte(tmp >> 16)      // high byte
}
```

The naive emission is three separate masked stores, each scattered across the output slice with a stride of 3. Horrible performance.

The correct emission is to **bitcast the source vector to a byte vector, run a fixed permutation through `pshufb` to place the bytes in the target layout, and emit a single contiguous masked store.**

The detection pass walks consecutive stores, recognizes the stride + shift pattern, and synthesizes the `pshufb` permutation table. Works for stride 2 (16-bit interleave), stride 3 (RGB, base64 output), stride 4 (RGBA, IPv4 octets).

Implemented uniformly for SSE, AVX2, and WASM. This is the base64 decoder's output packing — one of the reasons we hit 77% of simdutf.

## §5.8 `vpmaddubsw` / `vpmaddwd` pattern detection

```go
// tinygo/compiler/spmd.go:8988 — spmdTryEmitPmadd
```

The Go idiom to write:

```go
// byte lanes
go for i, b := range src {
    // ...
}
// int16 lanes
go for i := range nInt16 {
    v := int16(src[i*2])*C0 + int16(src[i*2+1])*C1
    // ...
}
// int32 lanes
go for i := range nInt32 {
    w := int32(temp[i*2])*D0 + int32(temp[i*2+1])*D1
    // ...
}
```

Three cascading `go for` loops: byte → int16 → int32, each doing a constant-coefficient multiply-add-widen. The exact shape recognized by `vpmaddubsw` (byte*byte → int16 widening) and `vpmaddwd` (int16*int16 → int32 widening).

The detector walks the int16 and int32 loops, sees the `A*C0 + B*C1` shape with adjacent loads, and emits the single SIMD instruction. Constant decomposition handles weights >127 (the signed-byte limit of `vpmaddubsw`): split into two partial products.

- Emission: `spmdX86Pmaddubsw` (`spmd_x86.go:61`), `spmdX86Pmaddwd` (`spmd_x86.go:91`).
- WASM fallback: deinterleave + widen + multiply + add — no single instruction, but still much faster than scalar.

**Result:** base64 Mula-Lemire decoder hot loop went from **14.3 instructions per byte to 0.44 instructions per byte.** A 32× instruction reduction. This single pattern detector is the largest single-source speedup in the PoC.

**Why a detector and not a builtin.** We tried a builtin first (`lanes.DotProductI8x16Add`). It worked for the specific IPv4 use case but didn't generalize. The detector handles the IPv4 case, the base64 case, and the hypothetical BGR-to-YUV conversion case, all from the same idiomatic Go source. We deleted the builtin (§7.2) after the detector landed.

## §5.9 LICM for SPMD compilations

Loop-invariant code motion isn't part of TinyGo's normal LLVM pipeline (TinyGo focuses on small-binary targets where LICM can bloat code). For SPMD compilations, we enable `loop-simplify` + `lcssa` + `licm` in the pass pipeline when `GOEXPERIMENT=spmd`.

Why: without LICM, loop-invariant splats (broadcasts of uniform values into varying registers) get re-emitted on every iteration. The outer scalar loop around a batched `go for` is the primary beneficiary — splats that should hoist out of the outer loop stay inside without LICM.

## §5.10 Inner scalar loop exclusion from the predication scope

If a `go for` contains a nested scalar `for` loop, should the inner loop's blocks be predicated as varying? No — the inner loop is per-lane scalar.

But how does the predication pass tell inner-scalar-for blocks apart from inner SPMD structure? Our heuristic:

> A block `b` is part of the SPMD loop scope iff some predecessor has `pred.Index >= b.Index`.

This catches self-loops and normal back-edges. Inner scalar loops that re-enter from a parent block have `pred.Index < b.Index` and fall outside the scope.

**Exception:** if the inner loop carries a varying phi, it *does* belong to the outer SPMD scope (the varying value flows through it, so the inner loop is effectively part of the vectorized computation).

Tests: `InnerScalarLoopExcluded` and `InnerLoopWithVaryingPhiInScope` in `x-tools-spmd/go/ssa/spmd_predicate_test.go`. The motivating bug was `map-restrictions` (2026-03-14), where inner scalar for loops were being incorrectly vectorized.

## §5.11 WASM guard zone: overread + mask for safe contiguous loads

One of the most practical tricks in the PoC — and one that would transfer directly to any WASM SIMD implementation — is the **16-byte guard zone at the top of linear memory** that allows every vector load to overread safely.

### §5.11.1 The problem

A WASM `v128.load` from a pointer that is within 15 bytes of the end of linear memory **traps**. WASM bounds-checks every memory access, and a 16-byte vector read starting at `memsize - 10` accesses 6 bytes beyond the memory, which is a hard fault.

This matters for SPMD because the tail of every `go for` loop over a `[]byte` can produce a load that reads up to 15 bytes past the valid data. The conventional solutions are all expensive:

- **Bounce buffer.** Copy the tail bytes into a 16-byte-aligned scratch buffer, load from there. Costs a `memcpy` + an extra load on every tail iteration.
- **Per-element scalar loads.** Load one byte at a time for the tail. Defeats the purpose of SIMD.
- **Exact-length masking.** Use `v128.load_lane` to load only the valid bytes. Not available on all targets, and where it is available, it's slower than a full `v128.load`.

### §5.11.2 The trick

Reserve 16 bytes at the top of WASM linear memory as an **unused guard zone**. The heap allocator sees `memory.size * 64KB - 16` as the available heap end. Those 16 bytes are never allocated, never written by user code, and always valid for reads.

From `tinygo/src/runtime/arch_tinygowasm.go:57`:

```go
// heapEnd is the current memory length in bytes, minus the SIMD guard zone.
//
// Reserve 16 bytes at the top of linear memory as a SIMD guard zone.
// This guarantees that v128.load from any heap-allocated pointer will
// not trap, even if it reads up to 15 bytes beyond the allocation.
// Cost: 16 bytes out of minimum 64KB. Used by createSPMDVectorFromMemory
// to do overread+mask instead of memset+memcpy+v128.load bounce buffer.
heapEnd = uintptr(wasm_memory_size(wasmMemoryIndex)*wasmPageSize) - 16
```

Now every vector load in the program — including tail loads — can safely issue a full `v128.load` at any heap-allocated address. The bytes beyond the valid data are garbage, but that's fine: the caller zeroes them with a post-load mask.

### §5.11.3 The overread + mask sequence

`createSPMDVectorFromMemoryMasked` at `tinygo/compiler/spmd.go:8708`:

```
raw = v128.load(dataPtr)                   // always safe thanks to guard zone
indices = [0, 1, 2, ..., 15]              // constant lane index vector
mask = icmp ult indices, splat(length)     // active: 0xFF, inactive: 0x00
result = and(raw, sext(mask))             // zero inactive bytes
```

Four instructions, no branches, no bounce buffer, no scalar fallback. The `sext` + `and` produces 0xFF for active lanes and 0x00 for inactive lanes, cleanly zeroing the overread garbage.

### §5.11.4 x86 variant: page-safe fast path

On x86-64, the guard zone trick doesn't apply (native memory doesn't have the same bounds-check model). Instead, `createSPMDVectorFromMemory` at `spmd.go:8749` checks whether the pointer is within 16 bytes of a 4096-byte page boundary:

```
pageOff = ptr & 0xFFF
nearEnd = pageOff > 0xFF0
```

If `nearEnd` is false (~99.6% of the time), a single `vmovdqu` suffices — garbage bytes beyond the valid data are acceptable because callers trim via the execution mask, not via the vector content. If `nearEnd` is true, the function falls through to the same overread + mask sequence, which is always safe because the operating system maps at least one guard page after each allocation region.

### §5.11.5 Why this matters

The cost of the guard zone is **16 bytes out of a minimum 64KB WASM memory**. The benefit is that every SPMD tail load — and every `SPMDVectorFromMemory` (used for loading variable-length strings and slices into vector registers) — avoids a bounce buffer or per-element scalar path. For the IPv4 parser and the base64 decoder's remainder handling, this was the difference between a working fast path and a slow fallback on every input that isn't a multiple of 16 bytes.

**Recommendation for upstream.** Any Go WASM runtime that supports SIMD should reserve a guard zone. 16 bytes is enough for 128-bit SIMD. If WASM ever gains 256-bit or 512-bit vector extensions, increase the guard zone to match. The cost is negligible; the codegen simplification is substantial.

---

# §6 The mask stack: what we abandoned, and the detour that led to it

## §6.1 Why we tried to avoid touching the SSA

The mask stack wasn't our first choice — it was a workaround, born from a practical constraint we imposed on ourselves.

The PoC already maintained two forked repositories: a Go fork (for the frontend type checker and parser) and a TinyGo fork (for the LLVM backend). Adding a third fork — a patched copy of `golang.org/x/tools/go/ssa` — felt like a maintenance burden we couldn't afford. Three forks means three rebase surfaces, three sets of merge conflicts, three repos to keep in sync every time upstream changes.

So we spent a significant amount of effort trying to do everything in TinyGo's backend without modifying the SSA layer at all. The reasoning was: TinyGo already consumes `go/ssa` as a read-only input; if we can reconstruct varying-ness, control-flow masks, and predication from the SSA structure alone — by analyzing the blocks as we walk them during LLVM codegen — we don't need to fork `go/ssa`.

**That's where the mask stack came from.** The TinyGo backend would walk the SSA blocks, detect varying conditions by inspecting operand types, push/pop masks when entering/leaving varying scopes, and consult the top of the stack at every memory op to decide how to mask the load or store.

## §6.2 Why it didn't work

For the first stretch of Phase 2, the mask stack worked well enough for simple cases: a `go for` with a single varying `if`/`else` inside. But as we added more control flow — varying switch, `&&`/`||` chains, `break` under varying conditions, nested if-inside-for, inner scalar loops that should not be predicated — each one required new push/pop sites sprinkled through the block walker. The walker was doing double duty: traversing LLVM blocks for codegen (which happens in a specific order determined by LLVM's layout) while simultaneously tracking Go-level control-flow semantics (which depends on the Go AST structure, not the LLVM block order). Those two concerns are fundamentally different, and entangling them was a steady source of bugs.

Every bug report came down to "the mask stack was wrong on this specific code path." The mask was too wide, or too narrow, or popped at the wrong time, or never pushed because the varying condition was detected too late.

## §6.3 The fork we couldn't avoid

We eventually accepted that the third fork was necessary. We created `x-tools-spmd/` — a patched copy of `golang.org/x/tools@v0.30.0` — and added the SPMD metadata and transforms described in §3: `SPMDLoopInfo`, `If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`, predication, peeling, store merging. About 2000 lines of additions.

On 2026-03-05 we deleted ~330 lines of mask-stack code from TinyGo. All memory-op masking moved to **explicit SSA-level masks** on `SPMDLoad`/`SPMDStore` instructions, populated by the predication pass. The block walker became trivial: each memory op carries its mask; the walker emits it.

The result was immediate: bugs stopped. New control-flow cases (divergent inner loops, boolean chains, varying switch) landed without mask-stack regressions because the SSA already encoded the correct mask at every point of use.

## §6.4 The lesson

**SPMD is a compiler feature that has to live at the heart of the SSA form.** You cannot bolt it on as a backend analysis. The mask semantics are a property of the program's control flow, and they must be resolved where control flow is represented — in the SSA — not reconstructed during a traversal of a different IR (LLVM blocks, in our case).

We tried hard to avoid this conclusion because forking a third repo was expensive. The detour cost us real time: the mask stack was maintained for months, accumulated workarounds, and was eventually deleted entirely. If we had forked `go/ssa` on day one and built predication there, the total project time would have been shorter.

For an upstream implementation in the Go compiler, this is even more clear: the SPMD transforms belong in `cmd/compile/internal/ssa`, integrated directly, not as an afterthought in the codegen walker. The SSA must know about masks, varying conditions, and loop structure. Trying to defer that knowledge to a later compilation stage is a false economy — it will always be reconstructed imperfectly, and the bugs will be proportional to the gap between what the SSA knows and what the backend needs.

---

# §7 Hall of fame: removals that made the codebase better

Over the PoC we added features, tried them, measured them, and deleted many. Every removal was a win. Documenting them here so a future implementer doesn't pay to discover the same results.

## §7.1 `lanes.CompactStore` + `SPMDMux` + `SPMDInterleaveStore`

Added between 2026-04-08 and 2026-04-10. Removed 2026-04-12 in a single cleanup commit (~1500 lines deleted).

**The story.** The base64 decoder's first working version (v1) used a compress-store builtin (`lanes.CompactStore`) and a custom SSA opcode (`SPMDMux`) to collapse chains of per-lane conditional stores into a single compact store. Later, `SPMDInterleaveStore` was added to handle the specific pattern of deinterleaved output with diagonal extraction.

Performance: 2× scalar base64. Not bad, but a factor of ten off simdutf.

The rewrite (v2) redesigned base64 around cascading `go for` loops (see §5.8, §5.9) and byte-decomposition store (§5.7). It did not need `CompactStore`, `SPMDMux`, or `SPMDInterleaveStore`. Performance: 77% of simdutf.

All three features were deleted. ~1500 lines of compiler code gone. The cleanup also removed the corresponding frontend builtin, the type-checker rules for it, the detection passes, the lowering code, and the integration tests.

**Lesson:** compress-store and explicit deinterleave are tempting builtins that indicate you are working around a missing *optimization*. Fix the optimization; delete the builtin.

## §7.2 `lanes.DotProductI8x16Add`

Added as a builtin for the IPv4 parser (which needed fast decimal conversion via multiply-add). Removed in commit 1df19e8 (~163 lines) once `vpmaddubsw` pattern detection (§5.9) subsumed it.

The builtin took one specific multiply-add shape and emitted one specific instruction sequence. The detector takes any cascading `go for` that matches the general shape and emits the correct instructions for the target (x86 or WASM). The detector is simpler. The detector handles the IPv4 use case, the base64 use case, and every future case we haven't imagined yet.

**Moral (again): pattern detectors generalize; per-case builtins don't.**

## §7.3 Constrained `Varying[T, N]`

Removed entirely. See §2.1 — the lane-count parameter was never useful in practice.

## §7.4 Cross-lane primitives we'd not ship in v1

The biggest negative result in the PoC. We built, tested, optimized, and shipped in the PoC:

- `lanes.Rotate(v, k)` — full-width rotation by a compile-time constant.
- `lanes.Swizzle(v, idx)` — full-width permutation by a runtime index vector.
- `lanes.RotateWithin(v, k, n)` — rotate within each group of `n` lanes.
- `lanes.ShiftLeftWithin(v, k, n)`, `lanes.ShiftRightWithin(v, k, n)` — dual.
- `lanes.SwizzleWithin(v, idx, n)` — const-only permutation within each group.

**In every benchmark in the PoC, none of these primitives delivered a measurable win.** Base64, hex-encode, mandelbrot, lo-*, IPv4 parser, simple-sum, odd-even, to-upper — all optimized to their final speeds without any use of Rotate, Swizzle, or *Within.

The measured wins came from elsewhere:

- Pattern detection for `vpmaddubsw`/`vpmaddwd` (§5.9).
- Byte-decomposition store (§5.7).
- Contiguous access analysis (§5.3).
- SSA-level loop peeling (§3.5).
- The all-ones mask fast path (§5.4).
- The decomposed index path (§5.1).

The cost of shipping the cross-lane primitives was real: frontend builtin interception, type-checker rules, LLVM `shufflevector` lowering, AVX2 table duplication (for `vpshufb`-backed swizzles), const-only enforcement in types2/go/types (because `shufflevector`'s mask operand must be a constant), and scalar fallback implementations for every one. Weeks of engineering and reviewer time.

**Recommendation for a real implementation:** do not ship the `*Within` family in v1. Do not ship full-width `Swizzle` in v1. Consider shipping `Rotate` by compile-time-constant offset (cache-line rotation tricks), but gate even that on a concrete benchmark demonstrating a win. Ship `lanes.Broadcast`, `lanes.Count[T]()`, and `lanes.Index()` because they're free (splats, compile-time constants, iota vectors) and occasionally useful.

Add more cross-lane primitives only when a real benchmark demands them. This is the opposite of the "future-proof API" instinct, and it is the right instinct here: unused builtins are taxes on every future compiler change.

---

# §8 Engineering process lessons

## §8.1 `tinygo/compiler/spmd.go` is too big

~9000 lines in one file, grown organically. Structure:

- Lane count / register size helpers.
- Loop analysis and peeling consumption.
- Control flow (if/else/switch/boolean-chain consumption after predication).
- Memory ops (load/store, contiguous analysis, decomposed index path).
- Cross-lane builtins (lanes.*, reduce.*).
- Pattern detectors (pmadd, byte-decomposition store).
- Target-specific helpers (inline or imported from `spmd_x86.go`).

**What it should have been:** `spmd_loop.go`, `spmd_memory.go`, `spmd_masks.go`, `spmd_patterns_x86.go`, `spmd_patterns_wasm.go`, `spmd_builtins.go`, `spmd_lowering.go`. Seven files of 1200–1500 lines each.

**Why it matters for you.** Large files in a compiler are harder to review, harder to navigate, and harder to search. They also tend to accumulate cross-cutting concerns because "it's already all in one file." If you start over, commit to a file-per-concern structure from day one.

## §8.2 Dual-mode E2E is non-negotiable

The test script at `test/e2e/spmd-e2e-test.sh` has eleven levels. The ones that matter for correctness are:

- **Level 8** — dual-mode (compile with `-simd=true` and `-simd=false`, diff output). 8 tests. Catches any lane-count-dependent bug.
- **Level 9** — scalar-validated (lane-count-dependent tests with a hand-written scalar reference). 16 tests.
- **Level 10** — x86-64 native SSE. 4-wide i32.
- **Level 11** — x86-64 native AVX2. 8-wide i32.

If you ship a Go-SPMD compiler without all four of these, you cannot claim correctness.

## §8.3 The agent workflow that worked

Documented in `CLAUDE.md`. Every implementation task went through:

1. **`golang-pro` agent** — writes the code.
2. **`code-reviewer` agent** — reviews; only proceed if approved.
3. **`clean-commit` agent** — creates the final git commit.

No step skipped. No commit without review. In practice this caught ~15% of changes before they hit main — a high return on a mechanical process.

## §8.4 PLAN.md "Deferred Items Collection"

`/home/cedric/work/SPMD/PLAN.md` has a section at line ~1253 called "Deferred Items Collection." Every time the team deferred a piece of work (because it was blocking another task, because it needed a spec first, because a refactor was imminent), the deferral was captured as:

- Task.
- Location (file:line).
- Status (not-started / in-progress / done).
- Dependencies.
- Priority.
- Related tasks.

By end of Phase 2, that section said "ALL DEFERRED ITEMS RESOLVED." No silent TODOs in the codebase. No forgotten follow-ups.

**Copy this practice.** The alternative — leaving TODOs scattered in source, in commit messages, in Slack — is how long projects accumulate silent debt.

---

# §9 What to steal, what to redesign

## §9.1 Steal verbatim

- **The predicated SSA approach** proven in `x-tools-spmd`. The patterns — `SPMDLoopInfo`, `If.IsVarying`, `SPMDSwitchChain`, `SPMDBooleanChain`, `SPMDLoad`, `SPMDStore`, `SPMDSelect` — should be re-expressed as `cmd/compile/internal/ssa` infrastructure for an upstream Go implementation.
- **SSA-level loop peeling.** The main/tail split with the all-ones invariant.
- **Explicit masks on memory ops.** Never a mask stack in the backend.
- **Decomposed index path** for byte iteration on wide SIMD.
- **Contiguous access analysis** with `ChangeType` and `BinOp` unwrap.
- **Pattern-detection philosophy**: recognize idiomatic Go and emit the right instructions, rather than shipping per-case builtins.
- **Scalar fallback mode** and dual-mode E2E as the correctness oracle.

## §9.2 Redesign from scratch

- **Unified types2 and go/types.** Or at least a shared SPMD logic layer so rules aren't double-written.
- **First-class mask width** in the type system. `Varying[T]` should carry or derive its lane count, not resolve it lazily at materialization.
- **SPMD function calling convention.** A real ABI entry, not a synthetic first parameter.
- **Smaller `compiler/spmd.go`.** File-per-concern from day one.
- **No `*Within` builtins in v1.** No full-width `Swizzle`. Maybe ship compile-time-const `Rotate` but gate on a benchmark.
- **Think through the `Varying[*T]`, `Varying[[N]T]`, `*Varying[Struct]` lattice before shipping**, not as retrofits.
- **`lanes.Index()` could just be `iota`.** Semantically, `lanes.Index()` is "the compile-time constant vector `[0, 1, ..., N-1]`" — which is exactly what `iota` already means in `const` blocks. Letting `iota` carry varying type in an SPMD context reuses a concept every Go developer already understands and avoids adding a package function for what is morally a compile-time constant. The PoC didn't do this (we already had `lanes.Index()` as a builtin), but an upstream implementation should consider it.

---

# §10 Source-file atlas

A navigation cheat sheet for working in the codebase.

## §10.1 Go frontend (`go/src/`)

| File | Lines of interest | What it does |
|---|---|---|
| `cmd/compile/internal/syntax/nodes.go` | ~415 | `ForStmt.IsSpmd`, `ForStmt.LaneCount` |
| `cmd/compile/internal/syntax/parser.go` | 2482, 2879 | `go for` parsing |
| `cmd/compile/internal/types2/typexpr_ext_spmd.go` | 17 | `handleSPMDIndexExpr`, `processLanesVaryingType` |
| `cmd/compile/internal/types2/check_ext_spmd.go` | 96 | `simd128CapacityBytes`, lane count computation |
| `cmd/compile/internal/types2/stmt_ext_spmd.go` | (several) | Return/break rules, `varyingDepth`, `maskAltered` |
| `cmd/compile/internal/types2/call_ext_spmd.go` | (several) | Public API restriction, varying param handling |
| `cmd/compile/internal/types2/pointer_ext_spmd.go` | (several) | `&Varying[T]`, `*Varying[*T]` |
| `cmd/compile/internal/types2/expr_ext_spmd.go` | (several) | `Varying[[N]T][i]`, field access |
| `cmd/compile/internal/types2/typestring_ext_spmd.go` | (several) | Type formatting |
| `go/types/*_ext_spmd.go` | (mirrors) | Same as types2, duplicated for stdlib type checker |
| `go/ast/ast.go` | (ForStmt) | Mirror of syntax.ForStmt for stdlib parser |
| `go/parser/parser.go` | (go for) | Mirror of cmd/compile/internal/syntax parser |

## §10.2 x-tools-spmd (patched `golang.org/x/tools/go/ssa`)

| File | Lines | What it does |
|---|---|---|
| `x-tools-spmd/go/ssa/ssa.go` | 411 | `SPMDLoopInfo` struct, `SPMDAccumulator`, `SPMDSwitchChain`, `SPMDBooleanChain` |
| `x-tools-spmd/go/ssa/spmd_loop.go` | — | `spmdLoopConstruction`, `resolveSPMDLoopPhis` (two-phase population) |
| `x-tools-spmd/go/ssa/spmd_varying.go` | 17, 115, 129, 140 | `exprHasSPMDType`, `resolveSPMDSwitchChains`, `resolveSPMDBooleanChains` |
| `x-tools-spmd/go/ssa/spmd_peel.go` | 247, 302 | `peelSPMDLoops`, `peelSPMDLoop` |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | 78 | `predicateSPMDFuncBody` |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | 419 | `predicateSPMDScope` |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | 3500+ | `predicateVaryingBreaks` |
| `x-tools-spmd/go/ssa/spmd_predicate.go` | 3795, 3902, 4001, 4072 | `spmdMergeRedundantStores` and helpers |
| `x-tools-spmd/go/ssa/builder.go` | 212–219, 248–255 | `SPMDBooleanChain` population in `cond()` |
| `x-tools-spmd/go/ssa/builder.go` | 217, 253, 276 | `If.IsVarying` flag set |
| `x-tools-spmd/go/ssa/builder.go` | 1551, 1605 | `SPMDSwitchChain` population |
| `x-tools-spmd/go/ssa/func.go` | 417, 443 | Predication pipeline invocation |
| `x-tools-spmd/go/ssa/spmd_loop_test.go` | — | SPMDLoopInfo tests |
| `x-tools-spmd/go/ssa/spmd_varying_test.go` | — | Varying metadata tests |
| `x-tools-spmd/go/ssa/spmd_predicate_test.go` | — | Predication tests (inner loop exclusion, boolean chain) |
| `x-tools-spmd/go/ssa/spmd_peel_test.go` | — | Peeling tests |

## §10.3 TinyGo backend (`tinygo/compiler/`)

| File | Lines | What it does |
|---|---|---|
| `compiler/spmd.go` | 273, 284 | `spmdRegisterBytes`, `spmdLaneCount` |
| `compiler/spmd.go` | 902–907, 1091 | `isDecomposed`, decomposed index path |
| `compiler/spmd.go` | 2477, 2521–2527 | `spmdUsesSIMD`, scalar fallback gate |
| `compiler/spmd.go` | 2625, 2637 | Constant mask materialization |
| `compiler/spmd.go` | 2681 | `spmdRegisterBytes()` |
| `compiler/spmd.go` | 2700 | SPMD function signature handling |
| `compiler/spmd.go` | 4626–4727 | `spmdFullStoreWithBlend`, all-ones fast path |
| `compiler/spmd.go` | 4828–4863, 4870 | `spmdAnalyzeContiguousIndex`, `spmdUnwrapScalar` |
| `compiler/spmd.go` | 6537–6547 | `spmdWasmSwizzle` / AVX2 table duplication |
| `compiler/spmd.go` | 7670–7679 | `spmdEmitInterleavedStoreMasked` (byte-decomposition store) |
| `compiler/spmd.go` | 8030+ | `createSPMDSelect` mask narrowing safety net |
| `compiler/spmd.go` | 8974–8988 | `spmdTryEmitPmadd` (pattern detector) |
| `compiler/spmd_x86.go` | 9, 39 | `spmdX86Pshufb` dispatch |
| `compiler/spmd_x86.go` | 57–61, 87–91 | `spmdX86Pmaddubsw`, `spmdX86Pmaddwd` |
| `compiler/compiler.go` | 196 | `spmdFuncIsBody` flag |
| `compiler/compiler.go` | 1495, 1519, 1541 | SPMD function entry, mask extraction |
| `compiler/compiler.go` | 2690+ | `spmdMaskTypeFromSig` |
| `compiler/func.go` | — | Function body compilation with SPMD |
| `compiler/symbol.go` | — | Symbol / mangling with mask parameter |
| `compiler/interface.go` | — | Varying as `interface{}` |
| `compileopts/config.go` | 119 | `SIMDRegisterSize` |
| `loader/list.go` | — | `lanes`/`reduce` package forcing |

## §10.4 Tests and examples

| Path | What it is |
|---|---|
| `test/e2e/spmd-e2e-test.sh` | 11-level E2E runner. Levels 8–11 are the correctness critical path. |
| `test/e2e/spmd-benchmark.sh` | WASM benchmarks (wasmtime) |
| `test/e2e/spmd-benchmark-x86.sh` | x86-64 native benchmarks (SPMD vs samber/lo generic vs lo/exp/simd AVX2) |
| `examples/hex-encode/main.go` | ~2.4 KB — byte-granular encoding |
| `examples/mandelbrot/main.go` | ~7.9 KB — divergent iteration counts, per-lane break masks |
| `examples/base64-decoder/main.go` | ~7.5 KB — cascading `go for`, chunkSize trick, byte-decomposition store. **The flagship example.** |
| `examples/ipv4-parser/main.go` | ~4.4 KB — inner-SPMD ceiling demonstration |
| `PLAN.md` | ~1253 — "Deferred Items Collection" section |

---

# Closing

The PoC proved that SPMD-for-Go is viable: a Go program written with `lanes.Varying[T]` and `go for` can compile through an LLVM backend and reach ~77% of hand-tuned SIMD C++ on a non-trivial benchmark (base64 decoding), with clear paths to close the remaining gap. The vectorization machinery that makes it work was prototyped in `golang.org/x/tools/go/ssa` and is small — a few thousand lines of predication, peeling, and pattern detection, plus a few thousand lines of backend lowering. For an upstream Go implementation, this machinery would be re-expressed in `cmd/compile/internal/ssa`, taking the patterns that worked in the PoC and building them into the production compiler's SSA infrastructure.

The mistakes we made (dead Phase-1 SSA opcodes, a mask stack, cross-lane primitives we couldn't benchmark, a 9000-line file) are all documented above. The things that worked (predicated SSA at go/ssa, peeling + all-ones, pattern detection, decomposed indexing, dual-mode E2E) are all documented too.

If a real upstream implementation starts from this PoC, the most important thing is to treat `go/ssa` as the vectorization substrate and the backend as a mechanical consumer. Everything else follows from that choice.
