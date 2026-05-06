# SPMD Divergent Inner-Loop Design

**Date**: 2026-05-03
**Status**: Design — pending user review
**Predecessors**: v6.1 final state (e2e 94/0/92/1/11). Only `array-counting` failing — this design closes that gap.
**Working name**: v7 (post-v6.1 follow-up)

## 1. Problem statement

A regular `for _, v := range varyingSlice` written inside the body of a `go for` SPMD loop must iterate each SPMD lane's slice independently. Today, TinyGo's range-over-slice codegen treats the varying slice header as a scalar, generating an inner loop that iterates only lane 0's data and applies that lane's elements to every lane's accumulator.

Concrete failure (test/integration/spmd/array-counting/main.go):

```go
arrays := [][]int{{1,2}, {3}, {4}, {5,6,7}}    // per-lane slices

go for i, secondLevel := range arrays {
    t := 0
    for _, value := range secondLevel {        // divergent inner loop
        t += value
    }
    result[i] = t
}
// expected result: [3, 3, 4, 18]
// actual:          [3, 3, 3, 3]   ← all lanes sum lane-0's slice
```

LLVM IR shows the alloca for `secondLevel` is `[1 x slice_struct]` (sized for 1 lane only) while the SPMD store writes 4 slice headers — undefined behavior. The subsequent load reads only the first slice, and the inner loop's bound is lane 0's length.

This design enables true per-lane iteration: each lane visits its own slice's elements; the inner loop runs `max(lens)` iterations with an active mask narrowing as lanes complete or break.

## 2. Scope

| | Included |
|---|---|
| Container types | `Varying[[]T]` only |
| Iteration semantics | Max-bound iteration with mask narrowing (lockstep, ISPC `foreach_active`-style) |
| Control flow inside inner loop | `continue` and `break` (per-lane); `return` deferred |
| Outer mask interaction | Inner `active_mask = outer_mask AND lane_active AND ~broken_mask` |
| New SSA opcodes | None — pure TinyGo lowering, SSA shape unchanged |

Out of scope (deferred):
- `Varying[string]` / `Varying[map]` / `Varying[chan]` ranging
- `return` from inside a divergent inner loop
- Computed per-lane integer ranges (`for j := varying_start; j < varying_end; j++`)

## 3. Architecture

### Detection

TinyGo's existing range-statement compilation grows a single dispatch check: if the range expression's SSA type is `*types.SPMDType` wrapping `*types.Slice`, AND we're inside an active SPMD loop, dispatch to a new helper `spmdEmitDivergentInnerLoop`. Otherwise the existing scalar-range path runs unchanged.

### Storage of varying slice headers

The alloca for a `Varying[[]T]` value gets sized `[N x slice_struct]` where N is the enclosing SPMD loop's `LaneCount`. This is a focused fix to TinyGo's `*ssa.Alloc` materialization: when the alloca's pointee type is a `*types.SPMDType` wrapping a non-vectorizable type (slice/struct/string), and `Lanes()` is set by Pass A, size the alloca for N entries instead of 1.

The N-lane store of slice headers is already produced by current SSA generation; today it overruns the 1-lane alloca, which is the UB. After the sizing fix, the store fits correctly.

### Per-lane access

To extract per-lane bases and lens, TinyGo emits a small unrolled extraction (existing pattern, mirrored from `spmdPerLaneScatterStore`):

```
for lane := 0; lane < N; lane++ {
    gep   := GEP %headers, lane
    slice := load slice_struct, gep
    ptr   := extractvalue slice, 0
    len   := extractvalue slice, 1
    base_ptrs = insertelement <N x ptr>, ptr, lane
    lens      = insertelement <N x i32>, len, lane
}
```

### Loop emission

`spmdEmitDivergentInnerLoop` produces:

**Pre-header (in outer SPMD body, before the inner loop block):**
- Load `[N x slice_struct]`
- Extract `base_ptrs : <N x ptr>` and `lens : <N x i32>` (per Section 3 above)
- Compute `max_len = reduce.Max(lens)` once
- If body contains `break`: alloca `broken_mask : <N x mask_elem>`, store all-zeros

**Loop header:**
```
inner.loop:
  %j = phi i32 [0, %pre_header], [%j_next, %inner.body.end]
  %j_lt_max = icmp slt i32 %j, %max_len
  br i1 %j_lt_max, label %inner.body, label %inner.done
```

**Body entry:**
```
inner.body:
  %j_splat     = splat <N x i32> %j
  %lane_active = icmp slt <N x i32> %j_splat, %lens
  %brk         = load <N x mask_elem>, ptr %broken_mask    ; if break used
  %active_mask = AND(outer_mask, lane_active, ~%brk)
  %any_active  = reduce.any(%active_mask)                  ; fast-exit
  br i1 %any_active, label %inner.body.real, label %inner.done
```

**Body real (the lowered user `for` body):**
```
inner.body.real:
  %j_offset    = mul i32 %j, sizeof(T)
  %j_off_v     = splat <N x i32> %j_offset
  %addrs       = GEP %base_ptrs, %j_off_v
  %v           = call @llvm.masked.gather(<N x ptr> %addrs, <N x i1> %active_mask)

  ; ... user body lowered with the existing SPMD predication
  ;     infrastructure consuming %active_mask via spmdLoopState ...

  br label %inner.body.end

inner.body.end:
  %j_next = add i32 %j, 1
  br label %inner.loop
```

**Exit:**
```
inner.done:
  ; (broken_mask alloca falls out of scope; t accumulators carry final values)
```

### Active mask propagation into the body

The user body's `SPMDLoad` / `SPMDStore` / `SPMDSelect` ops (produced by Pass A) consume an active mask from `spmdLoopState`. To make these use the inner-loop's `active_mask` instead of the outer's, TinyGo pushes a new `spmdActiveLoop`-like entry onto the loop state stack with `mask = active_mask` before lowering the body, and pops after. This mirrors the existing pattern used for outer-loop body lowering.

### Continue and break

**Continue**: a `continue` inside the body becomes a `goto inner.body.end` for the lanes whose narrowed mask says continue. This is already handled by Pass A's predication when `continue` is under a varying condition — the body-mask is narrowed for the rest of the body, no special divergent-loop work needed.

**Break**: a `break` inside the body sets `broken_mask[lane] = true` for the breaking lanes, then jumps to `inner.body.end`. Implementation: a masked store to `broken_mask` before the jump. The next iter's `active_mask` AND'd with `~broken_mask` excludes them. Reuses the existing `spmdForLoopInfo` break-mask alloca pattern from `go for` infrastructure.

## 4. Edge cases

| Case | Handling |
|---|---|
| Empty slice in some lane (`len=0`) | `lane_active = (j < 0)` is false → lane inactive from j=0; `t` stays at initial value |
| All-empty slices | `max_len = 0` → loop never enters body; outer SPMD body proceeds normally |
| Nil slice (ptr=nil, len=0) | Same as empty (len=0 makes lane inactive); ptr=nil is never dereferenced because the gather is masked |
| Outer-mask narrowing (tail iter) | `active_mask = outer_mask AND lane_active AND ~broken_mask` — outer-off lanes stay off |
| Lane count change between outer and inner | Inner reuses outer's N; no new lane count |
| Multiple varying-slice variables in one outer body | Each gets its own `[N x slice]` alloca; inner loops emit independently |

## 5. Testing strategy

### New integration tests

| Test path | Purpose |
|---|---|
| `test/integration/spmd/array-counting/main.go` (existing) | Primary regression test; expected `[3 3 4 18]` |
| `test/integration/spmd/varying-slice-empty/main.go` (new) | Lane with `len=0` skipped from j=0 |
| `test/integration/spmd/varying-slice-continue/main.go` (new) | `continue` skips body for that lane this iter |
| `test/integration/spmd/varying-slice-break/main.go` (new) | `break` retires the lane permanently |
| `test/integration/spmd/varying-slice-multi/main.go` (new) | Combined break + continue + accumulator |

Add corresponding e2e entries with expected outputs.

### Sentinel checks (every step of implementation)

- **n-body**: `-0.169075164 / -0.169078071` (must NOT regress; n-body doesn't use varying slices but verifies no infrastructure regression)
- **bit-counting**: 32 (uniform inner loop; separate codepath)
- **Bucket-G** (`L0_cond`, `L4b_varying_break`, `integ_printf-verbs`): PASS
- **swizzle-within**: PASS
- **goroutine-varying**: PASS

### IR validation

After any TinyGo change touching alloca sizing or inner-loop emission, validate the produced IR with `llvm-as` round-trip:

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -opt=2 -target=wasi -internal-printir -o /tmp/x.wasm <test>.go 2>&1 > /tmp/x.ll
tinygo/llvm-build/bin/llvm-as /tmp/x.ll -o /tmp/x.bc
```

This catches the original UB silently — `[1 x slice]` alloca with `[N x slice]` store fails `llvm-as` validation.

### Final gate

E2E must reach **94/0/93/0/11** — full baseline parity. Phase 4 benchmarks proceed only after gate.

## 6. Implementation order (informs the plan)

1. **Alloca sizing fix**: TinyGo `*ssa.Alloc` materialization for `*types.SPMDType` wrapping non-vectorizable types respects `Lanes()`. Add a unit test asserting `[N x slice]` IR. Verify `array-counting` IR no longer fails `llvm-as`.
2. **Per-lane extraction helper**: small helper that produces `<N x ptr>` and `<N x i32>` from `[N x slice_struct]`. Unit test in `compiler_test.go`.
3. **`spmdEmitDivergentInnerLoop` (initial — no break)**: detection at range-statement entry, pre-header / header / body / done blocks, active mask threading via spmdLoopState push/pop. `continue` works automatically through Pass A. Verify array-counting passes.
4. **Break support**: add `broken_mask` alloca + masked store + AND in active mask. Verify break test.
5. **Edge cases sweep**: empty slices, all-empty, outer-mask interaction, multiple varying-slice loops in one body. Add corresponding tests.
6. **Type checker rules**: forbid `return` inside a divergent inner loop (until/unless we add support). Match the existing `go for` ISPC enforcement infrastructure.
7. **Regression sweep**: full e2e + benchmarks. Goal: 94/0/93/0/11.

## 7. Risks

| Risk | Mitigation |
|---|---|
| Alloca sizing change touches a path used by other tests | Tests guard via sentinels at each step; rollback policy if regression appears |
| Mask format mismatch between outer and inner | Reuse existing `spmdConvertMaskFormat` |
| Break interaction with body's varying control flow | Isolate broken_mask alloca per inner loop; doesn't interact with outer break-mask infrastructure |
| Predication pass not aware of inner-loop active mask | TinyGo pushes a new `spmdActiveLoop` entry; predication pass needs no change |
| `reduce.Max(lens)` for max_len could be expensive on very small slices | Single reduction once per outer iter; amortized across max_len inner iters; not a hot path |

## 8. Success criteria

- ✅ array-counting produces `[3 3 4 18]`
- ✅ New tests (empty / continue / break / multi) all PASS
- ✅ E2E reaches **94/0/93/0/11** baseline parity
- ✅ All existing sentinels (n-body, bit-counting, bucket-G, swizzle-within, goroutine-varying) still PASS
- ✅ IR generated by `array-counting` validates clean via `llvm-as` round-trip
