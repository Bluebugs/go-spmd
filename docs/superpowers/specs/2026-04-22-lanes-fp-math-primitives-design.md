# `lanes` Floating-Point Math Primitives — Design

**Date**: 2026-04-22
**Scope**: Add a family of element-wise floating-point math builtins to the `lanes` package in the SPMD fork. Unblocks `tinybench/n-body` (needs `lanes.Sqrt`), removes a workaround in `n-body-nosqrt` (`delta*delta <= tol*tol` instead of `lanes.Abs(delta) <= tol`), and rounds out the FP primitives that vectorized numerical code typically wants.

---

## 1. Overview, Scope, Naming

### Goal

Add 9 exported functions to the `lanes` package, each lowered to an LLVM vector intrinsic (`@llvm.<op>.vN{f32,f64}`):

| Builtin | Args | LLVM intrinsic | Use case |
|---|---|---|---|
| `lanes.Sqrt[T](Varying[T]) Varying[T]` | 1 | `@llvm.sqrt.vN{f32,f64}` | n-body |
| `lanes.Abs[T](Varying[T]) Varying[T]` | 1 | `@llvm.fabs.vN{f32,f64}` | n-body-nosqrt |
| `lanes.Floor[T](Varying[T]) Varying[T]` | 1 | `@llvm.floor.vN{f32,f64}` | future |
| `lanes.Ceil[T](Varying[T]) Varying[T]` | 1 | `@llvm.ceil.vN{f32,f64}` | future |
| `lanes.Round[T](Varying[T]) Varying[T]` | 1 | `@llvm.round.vN{f32,f64}` | future (round-half-away-from-zero, matches `math.Round`) |
| `lanes.Trunc[T](Varying[T]) Varying[T]` | 1 | `@llvm.trunc.vN{f32,f64}` | future |
| `lanes.Min[T](Varying[T], Varying[T]) Varying[T]` | 2 | `@llvm.minnum.vN{f32,f64}` | clamping (NaN-aware, matches Go's `min()`) |
| `lanes.Max[T](Varying[T], Varying[T]) Varying[T]` | 2 | `@llvm.maxnum.vN{f32,f64}` | clamping |
| `lanes.FMA[T](Varying[T], Varying[T], Varying[T]) Varying[T]` | 3 | `@llvm.fma.vN{f32,f64}` | hot loops; `a*b + c` fused with single rounding |

All gated on `floatingPoint` = `~float32 | ~float64` constraint.

### Naming notes

- `lanes.Min` / `lanes.Max` differ from `reduce.Min` / `reduce.Max`. The `lanes` versions are **pairwise per-lane** (Varying × Varying → Varying). The `reduce` versions collapse a Varying into a scalar. Doc comments call this out explicitly.
- Names mirror Go's `math` package (`math.Sqrt`, `math.Abs`, etc.). `math.Round` and LLVM `@llvm.round` both round half-away-from-zero.
- `FMA` matches Go 1.14+'s `math.FMA`. Single-rounding semantics.

### Non-goals

- Integer `Abs` / `Min` / `Max` via `@llvm.{s,u}{min,max,abs}`. Different type constraint, different dispatch. Future "integer math primitives" spec.
- Reciprocal sqrt, `exp`, `log`, trig functions — no LLVM vector intrinsic; require polynomial approximations or library calls.
- `lanes.Copysign`, `lanes.Nextafter`, `lanes.Remainder` — less common, defer.
- The `lanes.Abs`-replacement cleanup in `n-body-nosqrt`'s `sqrtNewtonVarying` (replace the `delta*delta <= tolSq` workaround). Optional; belongs to whoever unblocks n-body-nosqrt (separate spec, still pending varying-local mask threading fix).
- Scalar-fallback specialization beyond what the existing scalar-mode machinery provides.

### Invariants preserved

- Single 5-touchpoint pattern (lanes.go + type checker + TinyGo dispatcher + TinyGo emit + integration test), but the type checker touchpoint is **empty for this feature** — Go generics on the stubs suffice.
- All builtins gated on `buildcfg.Experiment.SPMD` via the existing dispatch.
- Stock-Go behavior: the panic stubs in `lanes.go` ensure non-SPMD builds never execute these (existing convention).

---

## 2. Lanes Package Surface

**File**: `/home/cedric/work/SPMD/go/src/lanes/lanes.go`

### 2.1 Type constraint

```go
// floatingPoint constrains lanes.* math builtins to IEEE 754 binary float types.
// Mirrors Go's stdlib math package, which is float64-only at the API level but
// internally handles float32 via float-bits routines.
type floatingPoint interface {
    ~float32 | ~float64
}
```

Placed near other constraint defs in `lanes.go`, or at the top of the file if none exist.

### 2.2 Single-arg builtins (Sqrt, Abs, Floor, Ceil, Round, Trunc)

Six functions follow this exact shape:

```go
// Sqrt returns the per-lane square root of value. NaN for negative inputs
// per IEEE 754. Lowered to @llvm.sqrt.vN{f32,f64}.
//
//go:noinline
func Sqrt[T floatingPoint](value Varying[T]) Varying[T] {
    return sqrtBuiltin(value)
}

//go:noinline
func sqrtBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
    panic("lanes.sqrtBuiltin is a compiler builtin and should be replaced during compilation")
}
```

Each function pair is 8 lines (doc comment + public + private + panic body).

Abs / Floor / Ceil / Round / Trunc have the same shape with the name substituted and a one-line doc comment describing the specific semantics.

### 2.3 Two-arg builtins (Min, Max)

```go
// Min returns the per-lane minimum of a and b. IEEE 754 minNum semantics:
// when one operand is NaN, returns the other; matches Go's built-in min().
// Note: this is the *pairwise* per-lane min. To collapse a Varying into a
// scalar minimum, use reduce.Min.
// Lowered to @llvm.minnum.vN{f32,f64}.
//
//go:noinline
func Min[T floatingPoint](a, b Varying[T]) Varying[T] {
    return minBuiltin(a, b)
}

//go:noinline
func minBuiltin[T floatingPoint](a, b Varying[T]) Varying[T] {
    panic("lanes.minBuiltin is a compiler builtin and should be replaced during compilation")
}
```

`Max` is symmetric; its doc comment mentions the contrast with `reduce.Max`.

### 2.4 Three-arg builtin (FMA)

```go
// FMA returns the per-lane fused multiply-add: a*b + c, with a single
// rounding step (no intermediate rounding of the product). Matches the
// semantics of Go's math.FMA (added in Go 1.14).
// Lowered to @llvm.fma.vN{f32,f64}.
//
//go:noinline
func FMA[T floatingPoint](a, b, c Varying[T]) Varying[T] {
    return fmaBuiltin(a, b, c)
}

//go:noinline
func fmaBuiltin[T floatingPoint](a, b, c Varying[T]) Varying[T] {
    panic("lanes.fmaBuiltin is a compiler builtin and should be replaced during compilation")
}
```

### 2.5 File organization

All 9 functions go into `lanes/lanes.go` under a contiguous `// FP math primitives` section comment. Matches the existing convention — all SPMD builtins (Broadcast, Rotate, Swizzle, From, RotateWithin, etc.) already live there.

---

## 3. Type Checker — No Changes

Element-wise builtins like `ShiftLeft` / `ShiftRight` in the existing code need no special type-checker validation. Go generics + the panic-stub pattern delegate type checking to standard constraint solving.

For the FP math bundle:

- **No context restrictions** (unlike `lanes.Index()` which must be inside an SPMD context). These ops are valid anywhere a Varying value exists.
- **No group-size validation** (unlike `lanes.RotateWithin`).
- **No cross-lane semantics** — per-lane unary/binary/ternary FP ops.
- **Type signature does the work** — `[T floatingPoint]` constraint rejects `Varying[int]` etc. via standard Go generics.

Net change: **zero** to `go/src/go/types/call_ext_spmd.go` and `go/src/cmd/compile/internal/types2/call_ext_spmd.go`.

---

## 4. TinyGo Backend (Dispatcher + Shared Emit Helper)

Two changes in `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`.

### 4.1 Shared helper: `createSpmdMathIntrinsic`

```go
// createSpmdMathIntrinsic emits a call to an LLVM vector math intrinsic
// (@llvm.<name>.vNf{32,64}) with the given arguments. All args must have
// matching vector types with FP element kind; the result type matches the
// args. Used by Sqrt, Abs, Floor, Ceil, Round, Trunc, Min, Max, FMA.
func (b *builder) createSpmdMathIntrinsic(name string, args []llvm.Value, pos token.Pos) (llvm.Value, error) {
    if len(args) == 0 {
        return llvm.Value{}, b.makeError(pos, "lanes."+name+": no arguments")
    }
    vecType := args[0].Type()
    elemType := vecType
    if vecType.TypeKind() == llvm.VectorTypeKind {
        elemType = vecType.ElementType()
    }

    // Determine intrinsic suffix from element type.
    var bits int
    switch elemType.TypeKind() {
    case llvm.FloatTypeKind:
        bits = 32
    case llvm.DoubleTypeKind:
        bits = 64
    default:
        return llvm.Value{}, b.makeError(pos, "lanes."+name+": unsupported element type")
    }

    var intrinsicName string
    if vecType.TypeKind() == llvm.VectorTypeKind {
        intrinsicName = fmt.Sprintf("llvm.%s.v%df%d", name, vecType.VectorSize(), bits)
    } else {
        // Scalar fallback (-simd=false): operate on scalar value.
        intrinsicName = fmt.Sprintf("llvm.%s.f%d", name, bits)
    }

    paramTypes := make([]llvm.Type, len(args))
    for i := range args {
        paramTypes[i] = vecType
    }
    fnType := llvm.FunctionType(vecType, paramTypes, false)

    fn := b.mod.NamedFunction(intrinsicName)
    if fn.IsNil() {
        fn = llvm.AddFunction(b.mod, intrinsicName, fnType)
    }
    return b.createCall(fnType, fn, args, "lanes."+name), nil
}
```

Notes:
- `intrinsicName` uses the LLVM intrinsic stem (`sqrt`, `fabs`, `floor`, `ceil`, `round`, `trunc`, `minnum`, `maxnum`, `fma`), not the Go name. `Abs` → `fabs`; `Min` → `minnum`; `Max` → `maxnum`.
- Scalar-fallback branch handles `-simd=false` mode, reusing TinyGo's existing scalar `math.Sqrt` lowering target (`@llvm.sqrt.f64` etc.).
- Function lookup-or-create pattern matches the convention in `tinygo/compiler/intrinsics.go`.

### 4.2 Dispatcher cases

Inside `createLanesBuiltin`'s `switch` (around line 3267 of `spmd.go`), add 9 cases. Each is one `strings.HasPrefix` check plus a helper call:

```go
case strings.HasPrefix(name, "lanes.Sqrt["):
    return b.createSpmdMathIntrinsic("sqrt",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Abs["):
    return b.createSpmdMathIntrinsic("fabs",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Floor["):
    return b.createSpmdMathIntrinsic("floor",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Ceil["):
    return b.createSpmdMathIntrinsic("ceil",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Round["):
    return b.createSpmdMathIntrinsic("round",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Trunc["):
    return b.createSpmdMathIntrinsic("trunc",
        []llvm.Value{b.getValue(instr.Args[0], getPos(instr))}, getPos(instr))

case strings.HasPrefix(name, "lanes.Min["):
    return b.createSpmdMathIntrinsic("minnum",
        []llvm.Value{
            b.getValue(instr.Args[0], getPos(instr)),
            b.getValue(instr.Args[1], getPos(instr)),
        }, getPos(instr))

case strings.HasPrefix(name, "lanes.Max["):
    return b.createSpmdMathIntrinsic("maxnum",
        []llvm.Value{
            b.getValue(instr.Args[0], getPos(instr)),
            b.getValue(instr.Args[1], getPos(instr)),
        }, getPos(instr))

case strings.HasPrefix(name, "lanes.FMA["):
    return b.createSpmdMathIntrinsic("fma",
        []llvm.Value{
            b.getValue(instr.Args[0], getPos(instr)),
            b.getValue(instr.Args[1], getPos(instr)),
            b.getValue(instr.Args[2], getPos(instr)),
        }, getPos(instr))
```

### 4.3 Scalar fallback (`-simd=false`)

The helper handles scalar mode automatically: when `vecType` isn't a vector, it emits `@llvm.<op>.f{32,64}` (the scalar form). LLVM's intrinsic mechanism handles both signatures — same code path as TinyGo's existing scalar `math.Sqrt` lowering.

No additional changes to `spmd.go`'s scalar-fallback section.

### 4.4 No SSA-layer changes

SSA treats `lanes.*` calls as regular `*ssa.Call` nodes. TinyGo's dispatcher recognizes them at codegen. `x-tools-spmd/go/ssa/` needs zero changes.

---

## 5. Testing

Existing `lanes.*` builtins are tested integration-only (E2E via TinyGo compilation + runtime output matching). We follow the same pattern.

### 5.1 Integration test — float64

**File**: `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math/main.go`

One `main.go` exercising all 9 builtins with known inputs and verifying output. Each `testXxx` function populates a `Varying[float64]` with known per-lane values, calls `lanes.Xxx`, and `fmt.Printf`s the result. Expected outputs are stable (IEEE 754 determinism).

The `testIntegration` function composes multiple builtins: `lanes.Sqrt(lanes.Abs(v))`, `lanes.Min(lanes.Sqrt(a), lanes.Sqrt(b))`, `lanes.FMA(a, b, c)`. Catches type-propagation and operand-ordering issues.

The exact populate-Varying idiom (use `lanes.From`, `go for ... range` accumulator, or `lanes.Varying[T](literal)` broadcast-and-mutate) is decided during implementation by reading 2-3 sibling tests (e.g., `test/integration/spmd/lanes-broadcast/`).

### 5.2 Integration test — float32

**File**: `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math-float32/main.go`

Mirrors §5.1 but with `Varying[float32]`. Catches element-type dispatch bugs (wrong intrinsic name for the element width). Separate directory so the E2E harness treats it as an independent case; easier to diagnose if one size works and the other doesn't.

### 5.3 Scalar-fallback test

Both tests above run through the existing dual-mode E2E harness convention: once with SIMD enabled (default), once with `-simd=false`. Identical output in both modes per the Level 8/Level 9 pattern in `test/e2e/spmd-e2e-test.sh`. No new harness-level changes; if the default level covers both, nothing extra. Implementation plan confirms.

### 5.4 End-to-end regression — n-body unblock

After this feature lands, update `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/main.go`:

```go
// Before:
distance := math.Sqrt(distanceSquared)
// After:
distance := lanes.Sqrt(distanceSquared)
```

Two call sites in `advance()` and `energy()`.

Verify output parity against the scalar reference:

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff (scalar reference `-0.169075164` / `-0.169078071`).

If clean: delete `tinybench/n-body/go-spmd/BLOCKER.md` and remove the n-body entry from `tinybench/BLOCKERS.md`. If float reassociation in `reduce.Add` produces last-digit mismatch, update BLOCKER.md with the new cause — the feature is complete but the benchmark has a separate, different blocker.

**n-body-nosqrt** is NOT unblocked by this spec. Its blocker is the varying-local mask threading bug (see `docs/superpowers/specs/2026-04-21-varying-local-mask-threading-design.md`), which remains pending.

### 5.5 Broader regression

```bash
bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -10
bash test/e2e/spmd-benchmark-x86.sh 2>&1 | tee /tmp/bench-after.txt
```

Expected: no regression against baselines (current: 92 compile pass / 91 run pass / 0 fail; benchmark ratios within ±10%). The feature adds 2 new E2E tests (§5.1 + §5.2) so compile/run totals tick up slightly.

### 5.6 Intrinsic-name probe

LLVM intrinsic names are exact (`@llvm.fabs` not `abs`, `@llvm.minnum` not `min`, etc.). The implementation plan includes an early task: compile a one-liner using each builtin, inspect the generated LLVM IR for the exact intrinsic name, before declaring the helper complete. Catches typos before the full test run.

---

## 6. Rollout, File Changes, Risks

### 6.1 Rollout order

1. **Lanes package** — declare constraint + 9 function pairs. Commit.
2. **TinyGo helper** — add `createSpmdMathIntrinsic` to `spmd.go`. Commit (helper is dead until dispatcher cases land).
3. **TinyGo dispatcher cases** — add 9 cases in `createLanesBuiltin`. Commit.
4. **Intrinsic-name probe** — §5.6 verification. One probe compile per builtin. No commit (diagnostic).
5. **Integration test — float64** — §5.1 fixture + E2E gate. Commit.
6. **Integration test — float32** — §5.2 fixture. Commit.
7. **Full E2E + benchmark sweep** — gate before n-body unblock.
8. **n-body unblock** — update `tinybench/n-body/go-spmd/main.go`, verify parity, delete/update BLOCKER. Commit.
9. **Submodule pointer bumps** — parent SPMD picks up `go`, `tinygo`, `tinybench` commits.

### 6.2 File-by-file changes

| File | Status | What |
|---|---|---|
| `go/src/lanes/lanes.go` | MODIFY | Add `floatingPoint` constraint + 9 function pairs. |
| `tinygo/compiler/spmd.go` | MODIFY | Add `createSpmdMathIntrinsic` helper + 9 dispatcher cases. |
| `test/integration/spmd/lanes-fp-math/main.go` | NEW | Integration test (float64). |
| `test/integration/spmd/lanes-fp-math-float32/main.go` | NEW | Integration test (float32). |
| `tinybench/n-body/go-spmd/main.go` | MODIFY | `math.Sqrt` → `lanes.Sqrt` (2 call sites). |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (if parity passes) | — |
| `tinybench/BLOCKERS.md` | MODIFY | Remove n-body entry; update summary. |

Parent SPMD repo: submodule pointer bumps for `go`, `tinygo`, `tinybench`.

**Unchanged**: `go/src/go/types/`, `cmd/compile/internal/types2/`, `x-tools-spmd/` (per §3, §4.4).

### 6.3 Risks

1. **LLVM intrinsic name typos** — `@llvm.fabs` (not `abs`), `@llvm.minnum` (not `min`), etc. §5.6 probe catches. Low risk.

2. **float32 dispatch bug** — helper's `bits = 32/64` dispatch could silently route float32 to a float64 intrinsic. §5.2 float32 test catches.

3. **Scalar-mode fallback** — when `vecType` is scalar, the helper emits `@llvm.sqrt.f64` etc., which TinyGo's `math.Sqrt` lowering also uses. LLVM deduplicates identical function declarations — no actual conflict. Benign; noted.

4. **FMA precision** — `@llvm.fma` requires single rounding. Without hardware FMA3 support (older x86, WASM SIMD128), falls back to a software FMA library call — slower than naive `a*b + c`. Only relevant for users of `lanes.FMA`; acceptable.

5. **n-body float reassociation** — `reduce.Add` tree-reduces while scalar n-body accumulates sequentially. If bit-different output, the feature is still complete; BLOCKER.md gets a new cause per §5.4.

6. **Integration test populate-Varying idiom** — exact idiom to build a `Varying[float64]` with per-lane literal values varies by fixture. Implementation plan reads 2-3 sibling tests to settle.

### 6.4 Out of scope / deferred

- Integer `Abs` / `Min` / `Max`.
- `exp`, `log`, trig, reciprocal sqrt.
- `Copysign`, `Nextafter`, `Remainder`, etc.
- `lanes.Abs`-replacement cleanup in `n-body-nosqrt`'s `sqrtNewtonVarying` — optional; belongs to whoever unblocks that port (separate spec).

### 6.5 Success criteria

All must hold:

- `test/e2e/spmd-e2e-test.sh` passes with 2 new test entries (≥ 94 compile pass / 93 run pass / 0 fail).
- `test/e2e/spmd-benchmark-x86.sh` within ±10% of baselines.
- `tinybench/n-body/go-spmd/main.go` compiles after `math.Sqrt` → `lanes.Sqrt`.
- n-body output matches scalar reference (or BLOCKER.md updated with new cause).
