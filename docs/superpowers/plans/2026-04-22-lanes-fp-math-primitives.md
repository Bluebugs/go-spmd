# `lanes` Floating-Point Math Primitives Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass.

**Goal:** Add 9 element-wise floating-point math builtins to the `lanes` package — `Sqrt`, `Abs`, `Floor`, `Ceil`, `Round`, `Trunc`, `Min`, `Max`, `FMA` — each lowered to the corresponding LLVM vector intrinsic. Unblocks `tinybench/n-body` (needs `lanes.Sqrt`).

**Architecture:** One shared `createSpmdMathIntrinsic` helper in `tinygo/compiler/spmd.go` emits `@llvm.<name>.vN{f32,f64}` calls with 1/2/3 args. Nine dispatcher cases in `createLanesBuiltin` forward to it with the right intrinsic stem. All 9 `lanes` package functions are panic-stub pairs (public + private `xxxBuiltin`) following the existing `Broadcast`/`Rotate`/etc. pattern. No type-checker or SSA-layer changes.

**Tech Stack:**
- Forked Go (`/home/cedric/work/SPMD/go/`, branch `spmd`) — lanes package
- Forked TinyGo (`/home/cedric/work/SPMD/tinygo/`, branch `spmd`) — LLVM codegen
- Tinybench (`/home/cedric/work/SPMD/tinybench/`, branch `spmd`) — n-body port
- LLVM vector intrinsics: `@llvm.{sqrt,fabs,floor,ceil,round,trunc,minnum,maxnum,fma}.vN{f32,f64}`

**Spec:** `/home/cedric/work/SPMD/docs/superpowers/specs/2026-04-22-lanes-fp-math-primitives-design.md`

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `go/src/lanes/lanes.go` | MODIFY | Add `floatingPoint` constraint (`~float32 \| ~float64`) + 9 function pairs (public + `xxxBuiltin` stub). Placement: after `SwizzleWithin` (around line 190), constraint near the existing `integer` constraint. |
| `tinygo/compiler/spmd.go` | MODIFY | Add `createSpmdMathIntrinsic` helper + 9 cases in `createLanesBuiltin` switch (around line 3267). |
| `test/integration/spmd/lanes-fp-math/main.go` | NEW | Integration test (float64 path, all 9 builtins). |
| `test/integration/spmd/lanes-fp-math-float32/main.go` | NEW | Integration test (float32 path). |
| `tinybench/n-body/go-spmd/main.go` | MODIFY | `math.Sqrt` → `lanes.Sqrt` at 2 call sites in `advance()` and `energy()`. |
| `tinybench/n-body/go-spmd/BLOCKER.md` | DELETE (if parity passes) | — |
| `tinybench/BLOCKERS.md` | MODIFY | Remove n-body entry; update summary. |

Parent SPMD repo: submodule pointer bumps for `go`, `tinygo`, `tinybench`.

**Unchanged**: `go/src/go/types/`, `cmd/compile/internal/types2/`, `x-tools-spmd/` (per spec §3, §4.4).

---

## Task 0: Pre-flight

**Files:** None (verification only).

- [ ] **Step 1: Submodule state**

```bash
cd /home/cedric/work/SPMD
for sub in go tinygo tinybench x-tools-spmd; do
    echo "$sub: $(cd $sub && git rev-parse --abbrev-ref HEAD) @ $(cd $sub && git log -1 --oneline)"
done
```

Expected: all on branch `spmd`. `tinygo` tip should be `c4c424c` (the revert of the varying-local mask threading test) or later. `tinybench` tip should be `5482537` or later.

- [ ] **Step 2: Confirm n-body currently fails with math.Sqrt error**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-probe n-body/go-spmd/main.go 2>&1 | head -5
```

Expected output contains:
```
n-body/go-spmd/main.go:XX:YY: cannot use ... (variable of type lanes.Varying[float64]) as float64 value in argument to math.Sqrt: cannot assign varying expression to uniform variable
```

This confirms the blocker baseline (math.Sqrt rejects varying). After Task 7 lands, this error should be gone.

- [ ] **Step 3: Capture E2E baseline**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-baseline.txt 2>&1
tail -10 /tmp/e2e-baseline.txt
```

Expected: `92 compile pass, 91 run pass, 0 fail` (the restored baseline after the varying-local mask threading revert). Save `/tmp/e2e-baseline.txt` for comparison in Task 6.

- [ ] **Step 4: Capture benchmark baseline**

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-baseline.txt 2>&1
grep -E "lo-min|lo-max|lo-sum|lo-mean|lo-clamp|mandelbrot" /tmp/bench-baseline.txt | head -10
```

Expected: benchmark script completes without error; capture the SPMD-vs-scalar ratios for later comparison.

---

## Task 1: Lanes package — declare constraint and builtins

**Files:**
- Modify: `/home/cedric/work/SPMD/go/src/lanes/lanes.go`

- [ ] **Step 1: Add the `floatingPoint` constraint**

Open `/home/cedric/work/SPMD/go/src/lanes/lanes.go`. Locate the existing `integer` constraint (around line 191). Directly before or after it, add:

```go
// floatingPoint constrains lanes.* math builtins to IEEE 754 binary float types.
// Mirrors Go's stdlib math package, which handles both float32 and float64 internally.
type floatingPoint interface {
	~float32 | ~float64
}
```

- [ ] **Step 2: Add the 9 function pairs**

Append at the end of `lanes.go` (after the existing `SwizzleWithin` function and any trailing helpers — typically the file structure is: functions first, then constraints at the bottom). Put the section just BEFORE the `integer` / `floatingPoint` constraint block if the constraints are at the end, otherwise append directly below `SwizzleWithin`.

Add this complete block:

```go
// =====================================================================
// FP math primitives
// Each function is an element-wise floating-point operation lowered to
// the corresponding LLVM vector intrinsic (@llvm.<op>.vN{f32,f64}).
// =====================================================================

// Sqrt returns the per-lane square root of value.
// NaN for negative inputs per IEEE 754. Lowered to @llvm.sqrt.vN{f32,f64}.
//
//go:noinline
func Sqrt[T floatingPoint](value Varying[T]) Varying[T] {
	return sqrtBuiltin(value)
}

//go:noinline
func sqrtBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.sqrtBuiltin is a compiler builtin and should be replaced during compilation")
}

// Abs returns the per-lane absolute value. Lowered to @llvm.fabs.vN{f32,f64}.
//
//go:noinline
func Abs[T floatingPoint](value Varying[T]) Varying[T] {
	return absBuiltin(value)
}

//go:noinline
func absBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.absBuiltin is a compiler builtin and should be replaced during compilation")
}

// Floor returns the per-lane largest integer value <= value (round toward -inf).
// Lowered to @llvm.floor.vN{f32,f64}.
//
//go:noinline
func Floor[T floatingPoint](value Varying[T]) Varying[T] {
	return floorBuiltin(value)
}

//go:noinline
func floorBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.floorBuiltin is a compiler builtin and should be replaced during compilation")
}

// Ceil returns the per-lane smallest integer value >= value (round toward +inf).
// Lowered to @llvm.ceil.vN{f32,f64}.
//
//go:noinline
func Ceil[T floatingPoint](value Varying[T]) Varying[T] {
	return ceilBuiltin(value)
}

//go:noinline
func ceilBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.ceilBuiltin is a compiler builtin and should be replaced during compilation")
}

// Round returns the per-lane value rounded to the nearest integer, with ties
// rounded away from zero (matches Go's math.Round semantics).
// Lowered to @llvm.round.vN{f32,f64}.
//
//go:noinline
func Round[T floatingPoint](value Varying[T]) Varying[T] {
	return roundBuiltin(value)
}

//go:noinline
func roundBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.roundBuiltin is a compiler builtin and should be replaced during compilation")
}

// Trunc returns the per-lane value with its fractional part removed
// (rounded toward zero). Lowered to @llvm.trunc.vN{f32,f64}.
//
//go:noinline
func Trunc[T floatingPoint](value Varying[T]) Varying[T] {
	return truncBuiltin(value)
}

//go:noinline
func truncBuiltin[T floatingPoint](value Varying[T]) Varying[T] {
	panic("lanes.truncBuiltin is a compiler builtin and should be replaced during compilation")
}

// Min returns the per-lane minimum of a and b. IEEE 754 minNum semantics:
// when one operand is NaN, returns the other; matches Go's built-in min().
// Note: this is the PAIRWISE per-lane min. To collapse a Varying into a scalar
// minimum, use reduce.Min. Lowered to @llvm.minnum.vN{f32,f64}.
//
//go:noinline
func Min[T floatingPoint](a, b Varying[T]) Varying[T] {
	return minBuiltin(a, b)
}

//go:noinline
func minBuiltin[T floatingPoint](a, b Varying[T]) Varying[T] {
	panic("lanes.minBuiltin is a compiler builtin and should be replaced during compilation")
}

// Max returns the per-lane maximum of a and b. IEEE 754 maxNum semantics:
// when one operand is NaN, returns the other; matches Go's built-in max().
// Note: this is the PAIRWISE per-lane max. To collapse a Varying into a scalar
// maximum, use reduce.Max. Lowered to @llvm.maxnum.vN{f32,f64}.
//
//go:noinline
func Max[T floatingPoint](a, b Varying[T]) Varying[T] {
	return maxBuiltin(a, b)
}

//go:noinline
func maxBuiltin[T floatingPoint](a, b Varying[T]) Varying[T] {
	panic("lanes.maxBuiltin is a compiler builtin and should be replaced during compilation")
}

// FMA returns the per-lane fused multiply-add: a*b + c, with a single rounding
// step (no intermediate rounding of the product). Matches the semantics of
// Go's math.FMA (added in Go 1.14). Lowered to @llvm.fma.vN{f32,f64}.
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

- [ ] **Step 3: Verify package builds**

```bash
cd /home/cedric/work/SPMD/go/src
../bin/go build ./lanes 2>&1 | head
```

Expected: clean build (no errors). The new functions and constraint type-check against existing `Varying[T]` shape.

- [ ] **Step 4: Stage**

```bash
cd /home/cedric/work/SPMD/go
git add src/lanes/lanes.go
git status --short
```

Expected: `M  src/lanes/lanes.go`. Do NOT `git commit` — clean-commit handles that after review.

---

## Task 2: TinyGo — add shared emit helper

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`

- [ ] **Step 1: Add `createSpmdMathIntrinsic`**

Open `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`. Find `createLanesBuiltin` (around line 3231-3233). Immediately BEFORE that function, add the helper:

```go
// createSpmdMathIntrinsic emits a call to an LLVM vector math intrinsic
// (@llvm.<name>.vNf{32,64}) with the given arguments. All args must have
// matching vector types with floating-point element kind; the result type
// matches the args. In scalar mode (-simd=false) it emits the scalar form
// @llvm.<name>.f{32,64}.
//
// Used by Sqrt, Abs, Floor, Ceil, Round, Trunc, Min, Max, FMA dispatcher
// cases in createLanesBuiltin.
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

	// Build the intrinsic name. Vector: llvm.<name>.vNfBITS. Scalar: llvm.<name>.fBITS.
	var intrinsicName string
	if vecType.TypeKind() == llvm.VectorTypeKind {
		intrinsicName = fmt.Sprintf("llvm.%s.v%df%d", name, vecType.VectorSize(), bits)
	} else {
		intrinsicName = fmt.Sprintf("llvm.%s.f%d", name, bits)
	}

	// Build LLVM function type: (vecType, vecType, ...) -> vecType.
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

Note: `token.Pos` requires `"go/token"` in imports. Check the top of `spmd.go` — if `go/token` isn't already imported, add it. Also `"fmt"` for `Sprintf`. Both are very likely already imported; verify with `head -30 spmd.go`.

- [ ] **Step 2: Verify TinyGo compiler source still builds**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH make build-tinygo 2>&1 | tail -3
```

Expected: clean build. The new helper is dead code until Task 3 adds dispatcher cases — this step verifies syntactic + type correctness of the helper itself.

- [ ] **Step 3: Stage**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
```

Do NOT commit.

---

## Task 3: TinyGo — dispatcher cases

**Files:**
- Modify: `/home/cedric/work/SPMD/tinygo/compiler/spmd.go`

- [ ] **Step 1: Insert 9 cases into `createLanesBuiltin`**

In `tinygo/compiler/spmd.go`, locate the `switch` inside `createLanesBuiltin` (around line 3267). Insert the following 9 cases. They can go anywhere in the switch before the `default` case; for readability group them together (e.g., right after the last existing case). Each case is independent — they don't depend on each other or on case ordering.

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

Note the intrinsic stem substitutions:
- `lanes.Abs` → `fabs` (LLVM's FP absolute value intrinsic; `abs` is integer)
- `lanes.Min` → `minnum` (IEEE 754 minNum: NaN-aware)
- `lanes.Max` → `maxnum`
- Others match their Go name lowercased

- [ ] **Step 2: Also add scalar-fallback cases**

Look for the scalar-fallback section around line 3235-3264 (starts with the `if !b.spmdUsesSIMD() { ... }` block or similar). When SPMD is disabled (`-simd=false`), most `lanes.*` calls become identity. Our math intrinsics should also work in scalar mode — the helper already handles this (scalar vs vector dispatch internally). To route scalar-mode calls through the helper, add this inside the scalar-fallback block's switch:

```go
case strings.HasPrefix(name, "lanes.Sqrt["),
    strings.HasPrefix(name, "lanes.Abs["),
    strings.HasPrefix(name, "lanes.Floor["),
    strings.HasPrefix(name, "lanes.Ceil["),
    strings.HasPrefix(name, "lanes.Round["),
    strings.HasPrefix(name, "lanes.Trunc["),
    strings.HasPrefix(name, "lanes.Min["),
    strings.HasPrefix(name, "lanes.Max["),
    strings.HasPrefix(name, "lanes.FMA["):
    // FP math primitives work in both SIMD and scalar mode via the same
    // helper. Fall through to the main switch below.
```

WAIT — review the actual scalar-fallback code structure in spmd.go first (~line 3235-3264 per the Explore report) before applying this. If the scalar-fallback block is `if !simd { switch ... { return } }` and falls through to the main switch, no change needed — the main-switch cases from Step 1 handle both modes via the helper's internal vector/scalar dispatch. Only if the scalar-fallback block has its own mandatory switch with no fall-through does Step 2 apply.

Sub-agent: read lines 3235-3275 of `spmd.go` to confirm structure. If the main switch (Step 1) is reachable in scalar mode, Step 2 is unnecessary. Report which it is.

- [ ] **Step 3: Rebuild TinyGo**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH make build-tinygo 2>&1 | tail -3
```

Expected: clean build.

- [ ] **Step 4: Smoke-test compilation of a tiny fixture**

```bash
cat > /tmp/lanes-sqrt-probe.go <<'EOF'
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []float64{1.0, 4.0, 9.0, 16.0}
	var v lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		v = x
	}
	result := lanes.Sqrt(v)
	fmt.Printf("sqrt sum = %f\n", reduce.Add(result))
}
EOF
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/lanes-sqrt-probe /tmp/lanes-sqrt-probe.go 2>&1 | head
```

Expected: clean compile, binary produced.

If compile fails:
- Check the error message; likely a minor issue in the helper or dispatcher case.
- A common issue: missing imports in `spmd.go` (e.g., `go/token` for `token.Pos`). Fix and retry.

- [ ] **Step 5: Run probe and verify output**

```bash
/tmp/lanes-sqrt-probe
```

Expected output: `sqrt sum = 10.000000` (= 1+2+3+4).

If output is wrong (e.g., NaN, 0, or Inf):
- The wrong LLVM intrinsic was emitted. Dump IR: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 -print-llvm-ir -o /tmp/probe /tmp/lanes-sqrt-probe.go 2>&1 | grep "llvm\." | head` — verify the intrinsic name is `@llvm.sqrt.v4f64` or similar (v4 for AVX2 4-wide f64).
- Fix the stem string in the dispatcher case.

- [ ] **Step 6: Cleanup and stage**

```bash
rm /tmp/lanes-sqrt-probe /tmp/lanes-sqrt-probe.go
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
```

Do NOT commit.

---

## Task 4: Intrinsic-name probe for all 9 builtins

**Files:** None (diagnostic only).

Verify every builtin's LLVM intrinsic name is correct BEFORE writing the full integration test. Catches typos early.

- [ ] **Step 1: Write probe for all 9 builtins**

```bash
cat > /tmp/lanes-fp-probe.go <<'EOF'
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []float64{1.0, 4.0, 9.0, 16.0}
	var a, b, c lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		a = x
		b = x * 0.5
		c = x + 0.25
	}
	// Force all 9 intrinsics to be emitted.
	r1 := lanes.Sqrt(a)
	r2 := lanes.Abs(a)
	r3 := lanes.Floor(c)
	r4 := lanes.Ceil(c)
	r5 := lanes.Round(c)
	r6 := lanes.Trunc(c)
	r7 := lanes.Min(a, b)
	r8 := lanes.Max(a, b)
	r9 := lanes.FMA(a, b, c)
	// Sum so the compiler can't dead-code-eliminate them.
	total := reduce.Add(r1) + reduce.Add(r2) + reduce.Add(r3) + reduce.Add(r4) +
		reduce.Add(r5) + reduce.Add(r6) + reduce.Add(r7) + reduce.Add(r8) + reduce.Add(r9)
	fmt.Printf("total = %f\n", total)
}
EOF
```

- [ ] **Step 2: Compile with IR dump**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -print-llvm-ir -o /tmp/lanes-fp-probe /tmp/lanes-fp-probe.go > /tmp/lanes-fp-probe.ll 2>&1
```

(If `-print-llvm-ir` isn't a valid TinyGo flag, check `../tinygo/build/tinygo build -help` for the correct flag — might be `-llvm-print=verify` or similar.)

- [ ] **Step 3: Inspect IR for each expected intrinsic**

```bash
grep -o "llvm\.\(sqrt\|fabs\|floor\|ceil\|round\|trunc\|minnum\|maxnum\|fma\)\.v[0-9]*f64" /tmp/lanes-fp-probe.ll | sort -u
```

Expected output (one line per intrinsic, all 9 should appear):
```
llvm.ceil.v4f64
llvm.fabs.v4f64
llvm.floor.v4f64
llvm.fma.v4f64
llvm.maxnum.v4f64
llvm.minnum.v4f64
llvm.round.v4f64
llvm.sqrt.v4f64
llvm.trunc.v4f64
```

(`v4` on 256-bit AVX2 for f64; may be `v2` on 128-bit WASM-SIMD128 or `v8` on AVX-512.)

If an intrinsic is missing or has a wrong name:
- Check the corresponding dispatcher case in `tinygo/compiler/spmd.go` for the correct stem.
- Common errors: `llvm.abs` (wrong — that's integer; should be `llvm.fabs`), `llvm.min`/`llvm.max` (wrong — LLVM uses `minnum`/`maxnum` for IEEE-754 FP min/max).

- [ ] **Step 4: Execute the binary to sanity-check outputs**

```bash
/tmp/lanes-fp-probe
```

Expected: `total = <some finite number>` (not NaN, not Inf). Exact value depends on intrinsic semantics. Should be:
- Sqrt sum for `a = {1,4,9,16}`: 1+2+3+4 = 10
- Abs sum: 1+4+9+16 = 30 (abs of positive = itself)
- Floor, Ceil, Round, Trunc on `c = {1.25, 4.25, 9.25, 16.25}`: various
- Min, Max on `a, b = a*0.5`: b-sum = 15, a-sum = 30, min sum = 15, max sum = 30
- FMA `a*b + c`: exact value is 1*0.5+1.25 + 4*2+4.25 + 9*4.5+9.25 + 16*8+16.25 = 1.75+12.25+49.75+144.25 = 208

Sum of all 9: 10+30+Floor(c)+Ceil(c)+Round(c)+Trunc(c)+15+30+208 where per-lane floor/ceil/round/trunc of {1.25,4.25,9.25,16.25} give {1,4,9,16}/{2,5,10,17}/{1,4,9,16}/{1,4,9,16} summing to 30/34/30/30. Grand total: 10+30+30+34+30+30+15+30+208 = 417.

Approximate expected: `total = 417.000000`. If off by a bit, inspect which intrinsic returned wrong values — that specific stem string needs fixing.

- [ ] **Step 5: Cleanup**

```bash
rm /tmp/lanes-fp-probe /tmp/lanes-fp-probe.go /tmp/lanes-fp-probe.ll
```

No commit — this task is purely diagnostic. Proceed to Task 5 only if all 9 intrinsic names appear in Step 3 AND the runtime output in Step 4 is finite and approximately correct.

---

## Task 5: Integration test — float64 path

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math/main.go`

- [ ] **Step 1: Create the directory and test file**

```bash
mkdir -p /home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math
```

Create `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math/main.go` with this content:

```go
// run -goexperiment spmd -target=wasi

// Integration test for lanes FP math primitives (float64 path).
// Exercises all 9 builtins: Sqrt, Abs, Floor, Ceil, Round, Trunc, Min, Max, FMA.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	// Populate test vectors with known per-lane values.
	// data[i] = 1, 4, 9, 16 — perfect squares for clean sqrt output.
	data := []float64{1.0, 4.0, 9.0, 16.0}
	negData := []float64{-1.0, -4.0, 9.0, -16.0}     // Mixed signs for Abs.
	fracData := []float64{1.25, 4.75, 9.5, 16.499}   // Non-integer values for Floor/Ceil/Round/Trunc.

	var a, an, af, b lanes.Varying[float64]
	go for i, x := range data {
		_ = i
		a = x
	}
	go for i, x := range negData {
		_ = i
		an = x
	}
	go for i, x := range fracData {
		_ = i
		af = x
	}
	go for i, x := range data {
		_ = i
		b = x * 0.5
	}

	fmt.Printf("Sqrt([1,4,9,16])  sum = %.2f (expect 10.00)\n", reduce.Add(lanes.Sqrt(a)))
	fmt.Printf("Abs([-1,-4,9,-16]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Abs(an)))
	fmt.Printf("Floor([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Floor(af)))
	fmt.Printf("Ceil([1.25,4.75,9.5,16.499])  sum = %.2f (expect 34.00)\n", reduce.Add(lanes.Ceil(af)))
	fmt.Printf("Round([1.25,4.75,9.5,16.499]) sum = %.2f (expect 31.00)\n", reduce.Add(lanes.Round(af)))
	fmt.Printf("Trunc([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Trunc(af)))
	fmt.Printf("Min(a, b)  sum = %.2f (expect 15.00)\n", reduce.Add(lanes.Min(a, b)))
	fmt.Printf("Max(a, b)  sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Max(a, b)))
	fmt.Printf("FMA(a,b,a) sum = %.2f (expect 45.00)\n", reduce.Add(lanes.FMA(a, b, a)))

	// Integration: compose multiple builtins.
	fmt.Printf("Sqrt(Abs([-1,-4,9,-16])) sum = %.2f (expect 10.00)\n", reduce.Add(lanes.Sqrt(lanes.Abs(an))))
	fmt.Printf("Min(Sqrt(a), Sqrt(b))    sum = %.2f (expect 7.07)\n", reduce.Add(lanes.Min(lanes.Sqrt(a), lanes.Sqrt(b))))
}
```

Expected values are calculated by hand from the per-lane inputs. `Round(1.25) = 1` (nearest), `Round(4.75) = 5`, `Round(9.5) = 10` (ties away from zero), `Round(16.499) = 16`; sum 32. Wait — let me recalculate: 1 + 5 + 10 + 16 = 32. Adjust comment from `expect 31.00` to `expect 32.00`.

Actually, Round-half-away-from-zero on {1.25, 4.75, 9.5, 16.499}:
- Round(1.25) = 1 (fractional 0.25 < 0.5)
- Round(4.75) = 5 (fractional 0.75 > 0.5)
- Round(9.5) = 10 (tie → away from zero)
- Round(16.499) = 16 (fractional 0.499 < 0.5)
Sum: 1 + 5 + 10 + 16 = 32.

Use `expect 32.00`. Similarly re-verify others:
- Floor: 1 + 4 + 9 + 16 = 30 ✓
- Ceil: 2 + 5 + 10 + 17 = 34 ✓
- Trunc: 1 + 4 + 9 + 16 = 30 ✓

For Min/Max with `a = {1,4,9,16}` and `b = {0.5, 2, 4.5, 8}`:
- Min: 0.5 + 2 + 4.5 + 8 = 15 ✓
- Max: 1 + 4 + 9 + 16 = 30 ✓

For FMA `a*b + a`:
- (1*0.5+1) + (4*2+4) + (9*4.5+9) + (16*8+16) = 1.5 + 12 + 49.5 + 144 = 207. Adjust expected to `207.00`.

Correct the file accordingly before staging. Final printf strings:

```go
	fmt.Printf("Sqrt([1,4,9,16])  sum = %.2f (expect 10.00)\n", reduce.Add(lanes.Sqrt(a)))
	fmt.Printf("Abs([-1,-4,9,-16]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Abs(an)))
	fmt.Printf("Floor([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Floor(af)))
	fmt.Printf("Ceil([1.25,4.75,9.5,16.499])  sum = %.2f (expect 34.00)\n", reduce.Add(lanes.Ceil(af)))
	fmt.Printf("Round([1.25,4.75,9.5,16.499]) sum = %.2f (expect 32.00)\n", reduce.Add(lanes.Round(af)))
	fmt.Printf("Trunc([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Trunc(af)))
	fmt.Printf("Min(a, b)  sum = %.2f (expect 15.00)\n", reduce.Add(lanes.Min(a, b)))
	fmt.Printf("Max(a, b)  sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Max(a, b)))
	fmt.Printf("FMA(a,b,a) sum = %.2f (expect 207.00)\n", reduce.Add(lanes.FMA(a, b, a)))

	// Integration: compose multiple builtins.
	fmt.Printf("Sqrt(Abs([-1,-4,9,-16])) sum = %.2f (expect 10.00)\n", reduce.Add(lanes.Sqrt(lanes.Abs(an))))
	fmt.Printf("Min(Sqrt(a), Sqrt(b))    sum = %.2f (expect 7.07)\n", reduce.Add(lanes.Min(lanes.Sqrt(a), lanes.Sqrt(b))))
```

For `Min(Sqrt(a), Sqrt(b))` with `a = {1,4,9,16}` and `b = {0.5, 2, 4.5, 8}`:
- Sqrt(a) = {1, 2, 3, 4}
- Sqrt(b) = {0.707..., 1.414..., 2.121..., 2.828...}
- Min per lane = {0.707, 1.414, 2.121, 2.828}
- Sum ≈ 7.071 + small rounding → 7.07 at %.2f.

Hand check: 0.7071 + 1.4142 + 2.1213 + 2.8284 = 7.071. `expect 7.07` ✓.

- [ ] **Step 2: Compile and run directly (before the E2E harness)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/lanes-fp-math test/integration/spmd/lanes-fp-math/main.go
/tmp/lanes-fp-math
```

Expected: all 11 lines print, actual values match expected values shown in the printf strings.

If any value is wrong: inspect the IR (`-print-llvm-ir` flag or similar), find which intrinsic produced garbage, fix the dispatcher stem. Re-run.

- [ ] **Step 3: Run through E2E harness (SIMD + scalar modes)**

```bash
cd /home/cedric/work/SPMD
# The test harness runs each integration test; verify new test is picked up:
bash test/e2e/spmd-e2e-test.sh 2>&1 | grep "lanes-fp-math" | head
```

Expected: entries like `PASS lanes-fp-math` for both SIMD-enabled and SIMD-disabled modes (the harness typically runs each test in multiple target configurations).

If the scalar-mode run fails: Task 3 Step 2's scalar-fallback integration may be needed. Inspect the scalar-mode IR; it should contain `llvm.sqrt.f64` etc. (scalar form), not `llvm.sqrt.v4f64`.

- [ ] **Step 4: Cleanup and stage**

```bash
rm -f /tmp/lanes-fp-math
cd /home/cedric/work/SPMD
git add test/integration/spmd/lanes-fp-math/main.go
```

Do NOT commit.

---

## Task 6: Integration test — float32 path

**Files:**
- Create: `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math-float32/main.go`

Mirrors Task 5 but with `float32`. Catches element-type dispatch bugs (wrong intrinsic name for wrong element width).

- [ ] **Step 1: Create the directory and test file**

```bash
mkdir -p /home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math-float32
```

Create `/home/cedric/work/SPMD/test/integration/spmd/lanes-fp-math-float32/main.go`:

```go
// run -goexperiment spmd -target=wasi

// Integration test for lanes FP math primitives (float32 path).
// Parallel to lanes-fp-math but with Varying[float32] to exercise the
// element-width dispatch in createSpmdMathIntrinsic.
package main

import (
	"fmt"
	"lanes"
	"reduce"
)

func main() {
	data := []float32{1.0, 4.0, 9.0, 16.0}
	negData := []float32{-1.0, -4.0, 9.0, -16.0}
	fracData := []float32{1.25, 4.75, 9.5, 16.499}

	var a, an, af, b lanes.Varying[float32]
	go for i, x := range data {
		_ = i
		a = x
	}
	go for i, x := range negData {
		_ = i
		an = x
	}
	go for i, x := range fracData {
		_ = i
		af = x
	}
	go for i, x := range data {
		_ = i
		b = x * 0.5
	}

	fmt.Printf("Sqrt([1,4,9,16])  sum = %.2f (expect 10.00)\n", reduce.Add(lanes.Sqrt(a)))
	fmt.Printf("Abs([-1,-4,9,-16]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Abs(an)))
	fmt.Printf("Floor([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Floor(af)))
	fmt.Printf("Ceil([1.25,4.75,9.5,16.499])  sum = %.2f (expect 34.00)\n", reduce.Add(lanes.Ceil(af)))
	fmt.Printf("Round([1.25,4.75,9.5,16.499]) sum = %.2f (expect 32.00)\n", reduce.Add(lanes.Round(af)))
	fmt.Printf("Trunc([1.25,4.75,9.5,16.499]) sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Trunc(af)))
	fmt.Printf("Min(a, b)  sum = %.2f (expect 15.00)\n", reduce.Add(lanes.Min(a, b)))
	fmt.Printf("Max(a, b)  sum = %.2f (expect 30.00)\n", reduce.Add(lanes.Max(a, b)))
	fmt.Printf("FMA(a,b,a) sum = %.2f (expect 207.00)\n", reduce.Add(lanes.FMA(a, b, a)))
}
```

Note: float32 has lower precision than float64. The FMA expected value 207.00 should still hold at `%.2f` — intermediate values are within float32 range.

- [ ] **Step 2: Compile and run**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/lanes-fp-math-f32 test/integration/spmd/lanes-fp-math-float32/main.go
/tmp/lanes-fp-math-f32
```

Expected: all 9 lines match their expected values.

- [ ] **Step 3: Verify the IR contains `v8f32` (not `v4f64`)**

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -print-llvm-ir -o /tmp/lanes-fp-math-f32 \
  test/integration/spmd/lanes-fp-math-float32/main.go > /tmp/f32.ll 2>&1
grep -o "llvm\.\(sqrt\|fabs\|fma\)\.v[0-9]*f[0-9]*" /tmp/f32.ll | sort -u
```

Expected output includes `llvm.sqrt.v8f32` (not `v4f64`). The element width in the intrinsic name is `f32`, and the lane count is typically 8 on AVX2 for float32 (256-bit / 32-bit = 8).

If you see `f64` — the element-type dispatch in `createSpmdMathIntrinsic` is wrong; the `bits = 32/64` switch needs fixing.

- [ ] **Step 4: Cleanup and stage**

```bash
rm -f /tmp/lanes-fp-math-f32 /tmp/f32.ll
cd /home/cedric/work/SPMD
git add test/integration/spmd/lanes-fp-math-float32/main.go
```

Do NOT commit.

---

## Task 7: Tinybench — update n-body to use lanes.Sqrt

**Files:**
- Modify: `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/main.go`
- Delete (conditional): `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/BLOCKER.md`
- Modify: `/home/cedric/work/SPMD/tinybench/BLOCKERS.md`

- [ ] **Step 1: Replace `math.Sqrt` calls**

Open `/home/cedric/work/SPMD/tinybench/n-body/go-spmd/main.go`. There are exactly 2 call sites (from the port's history):

- In `advance()`: `distance := math.Sqrt(distanceSquared)` → `distance := lanes.Sqrt(distanceSquared)`.
- In `energy()`: `distance := math.Sqrt(dx*dx + dy*dy + dz*dz)` → `distance := lanes.Sqrt(dx*dx + dy*dy + dz*dz)`.

Use sed to make the change precise and reproducible:

```bash
cd /home/cedric/work/SPMD/tinybench/n-body/go-spmd
sed -i 's/math\.Sqrt(/lanes.Sqrt(/g' main.go
grep -n "math\.Sqrt\|lanes\.Sqrt" main.go
```

Expected: 0 occurrences of `math.Sqrt`, 2 occurrences of `lanes.Sqrt`.

Also remove the now-unused `math` import if n-body doesn't use anything else from math (check with `grep "math\." main.go` — if no other uses, remove the import):

```bash
grep "math\." main.go
# If no matches: 
# Open main.go and remove "math" from the import block.
```

- [ ] **Step 2: Compile n-body**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go 2>&1 | head
```

Expected: clean compile. The original `math.Sqrt` error is gone.

- [ ] **Step 3: Output parity diff**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/nb-go-bin ./n-body/go/main.go
diff <(/tmp/nb-go-bin 50000) <(/tmp/nb-spmd-bin 50000)
```

Expected: empty diff (bit-for-bit identical output: `-0.169075164` / `-0.169078071`).

- [ ] **Step 4a: SUCCESS PATH — identical output**

If `diff` is empty:

Delete BLOCKER.md and update top-level BLOCKERS.md:

```bash
cd /home/cedric/work/SPMD/tinybench
rm n-body/go-spmd/BLOCKER.md
```

Open `tinybench/BLOCKERS.md` and remove the entire `## n-body` section. Update the summary line: `Summary: **2 of 5** benchmarks are blocked.` → `Summary: **1 of 5** benchmarks are blocked.`

Stage:
```bash
git add -A
git status --short
```

Expected output:
```
 M BLOCKERS.md
 M n-body/go-spmd/main.go
 D n-body/go-spmd/BLOCKER.md
```

- [ ] **Step 4b: PARTIAL SUCCESS PATH — last-digit float mismatch**

If `diff` shows last-digit `%.9f` differences (e.g., `-0.169075163` vs `-0.169075164`), the compiler feature is complete but float reassociation in `reduce.Add` produces non-bit-identical output vs sequential scalar. Spec §5.4 acknowledges this.

In this case: keep the `main.go` change (the feature IS working), but update `n-body/go-spmd/BLOCKER.md` with the new cause (float reassociation) and keep `BLOCKERS.md`'s n-body entry. Suggested BLOCKER.md content:

```markdown
# n-body go-spmd: BLOCKED

**Status (2026-04-22, post-lanes.Sqrt):** Original blocker
(`math.Sqrt(Varying[float64])` unsupported) is **resolved** by
adding `lanes.Sqrt` in
`docs/superpowers/specs/2026-04-22-lanes-fp-math-primitives-design.md`.
The port now compiles and produces finite output.

A **new blocker** surfaced: the SPMD output differs from the scalar
reference in the last digit of `%.9f`. Cause: `reduce.Add` performs
a tree reduction (e.g., `((a+b)+(c+d))`) while the scalar variant
accumulates sequentially (`((a+b)+c)+d`). IEEE 754 float addition
is not associative; the two orders differ in the last bit.

**Symptom:**
- Scalar: `-0.169075164` / `-0.169078071`
- SPMD:   <fill in actual observed values>

**Reproducer:**
\`\`\`bash
PATH=../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd-bin n-body/go-spmd/main.go
/tmp/nb-spmd-bin 50000
\`\`\`

**Next steps:** Either
(a) accept the last-digit difference and relax the TestCorrectness
    gate to within-1-ULP for this benchmark,
(b) add a `reduce.AddOrdered` variant that preserves sequential
    accumulation order at the cost of vectorization, or
(c) restructure the algorithm to avoid the divergence (infeasible —
    pair-loop reduction is fundamental to the algorithm).

---

## History

- **2026-04-13 to 2026-04-21** — original blocker: type checker rejected field access on `Varying[*Planet]`. Resolved by the Varying[*Struct] field access feature.
- **2026-04-21** — second blocker: `math.Sqrt(Varying[float64])` unsupported. Resolved by the lanes.Sqrt feature (this change).
- **2026-04-22 forward** — third blocker: `%.9f` last-digit mismatch from `reduce.Add` tree-vs-sequential reduction order.
```

Also update `BLOCKERS.md` n-body entry to reflect the new cause.

Stage both:
```bash
git add -A
git status --short
```

- [ ] **Step 4c: UNEXPECTED FAILURE PATH**

If the SPMD output is wildly different (not last-digit, not compile-fail), the feature is incomplete. STOP — do not commit. Report and investigate.

- [ ] **Step 5: Cleanup**

```bash
rm -f /tmp/nb-go-bin /tmp/nb-spmd-bin
```

---

## Task 8: Regression sweep

**Files:** None (verification only).

- [ ] **Step 1: Full E2E correctness sweep**

```bash
cd /home/cedric/work/SPMD
bash test/e2e/spmd-e2e-test.sh > /tmp/e2e-after.txt 2>&1
tail -10 /tmp/e2e-after.txt
diff /tmp/e2e-baseline.txt /tmp/e2e-after.txt | head -30
```

Expected:
- Totals: now `94 compile pass, 93 run pass, 0 fail` (baseline 92/91/0 + 2 new lanes-fp-math tests).
- No net losses — the diff should only show the 2 new PASS entries.

If any previously-passing test now fails, that's a regression caused by this feature — investigate the helper or dispatcher cases.

- [ ] **Step 2: Benchmark sweep**

```bash
bash test/e2e/spmd-benchmark-x86.sh > /tmp/bench-after.txt 2>&1
grep -E "lo-min|lo-max|lo-sum|lo-mean|lo-clamp|mandelbrot" /tmp/bench-after.txt | head -10
diff <(grep -E "lo-min|lo-max|lo-sum|lo-mean|lo-clamp|mandelbrot" /tmp/bench-baseline.txt | head) \
     <(grep -E "lo-min|lo-max|lo-sum|lo-mean|lo-clamp|mandelbrot" /tmp/bench-after.txt | head)
```

Acceptance: each benchmark within **±10%** of baseline. The feature shouldn't affect these benchmarks at all (they don't use FP math primitives), so any drift beyond noise is a real concern.

If drift >10% on any bench: unlikely but worth investigating. The new math intrinsic declarations might be affecting the module's code layout. This is not an expected risk — proceed if clean.

---

## Task 9: Commit all staged work

Up to this point, work across 3 submodules is staged but uncommitted. This task runs clean-commit for each logical unit.

Each sub-step dispatches `clean-commit` with focused context. The subagent-driven workflow (per CLAUDE.md) handles commit messages.

- [ ] **Step 1: Commit lanes package change**

Dispatch `clean-commit` with context: "Add floatingPoint constraint + 9 function pairs (Sqrt/Abs/Floor/Ceil/Round/Trunc/Min/Max/FMA) to lanes/lanes.go. Each function is a generic panic stub that the TinyGo compiler intercepts and lowers to an LLVM vector intrinsic. Follows the existing Broadcast/Rotate/Swizzle panic-stub pattern."

Working dir for commit: `/home/cedric/work/SPMD/go`. Branch: `spmd`. Staged file: `src/lanes/lanes.go`.

- [ ] **Step 2: Commit TinyGo helper + dispatcher**

Dispatch `clean-commit` with context: "Add createSpmdMathIntrinsic helper and 9 dispatcher cases in createLanesBuiltin for the FP math primitives. The helper emits @llvm.<op>.vN{f32,f64} for vector inputs and @llvm.<op>.f{32,64} for scalar (simd=false) mode. Each dispatcher case forwards to the helper with the appropriate intrinsic stem (sqrt, fabs, floor, ceil, round, trunc, minnum, maxnum, fma)."

Working dir: `/home/cedric/work/SPMD/tinygo`. Branch: `spmd`. Staged file: `compiler/spmd.go`.

- [ ] **Step 3: Commit integration tests**

Dispatch `clean-commit` with context: "Add integration tests for lanes FP math primitives — one for float64 path, one for float32 — each exercising all 9 builtins with known inputs and expected outputs (sum via reduce.Add). Verifies correct LLVM intrinsic dispatch by element width (v4f64 vs v8f32 on AVX2)."

Working dir: `/home/cedric/work/SPMD`. Branch: `main`. Staged files: `test/integration/spmd/lanes-fp-math/main.go` and `test/integration/spmd/lanes-fp-math-float32/main.go`.

- [ ] **Step 4: Commit tinybench n-body update**

Dispatch `clean-commit` with context: "Replace math.Sqrt with lanes.Sqrt in n-body/go-spmd. The original math.Sqrt call rejected Varying[float64] arguments; lanes.Sqrt is the SPMD-aware variant lowering to @llvm.sqrt.v4f64 on AVX2. The port now produces [identical | last-digit-different — update BLOCKER] output vs the scalar reference."

Working dir: `/home/cedric/work/SPMD/tinybench`. Branch: `spmd`. Staged files depend on Step 4a vs 4b from Task 7:
- 4a success: `main.go`, deleted `BLOCKER.md`, updated `BLOCKERS.md`.
- 4b partial: `main.go`, updated `BLOCKER.md`, updated `BLOCKERS.md`.

- [ ] **Step 5: Bump submodule pointers in parent SPMD repo**

```bash
cd /home/cedric/work/SPMD
git add go tinygo tinybench test
git diff --cached --stat
```

Expected (exact counts vary):
```
 go              |  2 +-
 test/integration/spmd/lanes-fp-math/main.go          | <N> ++++++++
 test/integration/spmd/lanes-fp-math-float32/main.go  | <N> ++++++++
 tinybench       |  2 +-
 tinygo          |  2 +-
```

Dispatch `clean-commit` with context: "Land the lanes FP math primitives feature across the toolchain. Submodule bumps:
- `go`: adds Sqrt/Abs/Floor/Ceil/Round/Trunc/Min/Max/FMA panic stubs to the lanes package + floatingPoint constraint.
- `tinygo`: adds createSpmdMathIntrinsic + 9 dispatcher cases for @llvm.<op>.vN{f32,f64} emission.
- `tinybench`: n-body port updated to call lanes.Sqrt.
- `test/integration/spmd`: two new integration tests (float64 + float32 paths) covering all 9 builtins.
[If Task 7 Step 4a (success path)]: Unblocks n-body — now produces output identical to scalar reference.
[If Task 7 Step 4b (partial path)]: Original math.Sqrt blocker resolved; new BLOCKER documents float reassociation cause."

Working dir: `/home/cedric/work/SPMD`. Branch: `main`.

---

## Self-Review

### 1. Spec coverage

- Spec §1 (overview/scope/non-goals): Plan header + Task 0 baseline.
- Spec §2.1 (floatingPoint constraint): Task 1 Step 1.
- Spec §2.2–§2.4 (9 function pairs): Task 1 Step 2 (all shown verbatim).
- Spec §2.5 (file organization): Task 1 Step 2 placement note.
- Spec §3 (no type-checker changes): implicit — plan doesn't touch `go/types` or `types2`.
- Spec §4.1 (helper): Task 2 Step 1.
- Spec §4.2 (dispatcher): Task 3 Step 1.
- Spec §4.3 (scalar fallback): Task 3 Step 2 (conditional — read code first to decide if needed).
- Spec §4.4 (no SSA changes): implicit.
- Spec §5.1 (float64 test): Task 5.
- Spec §5.2 (float32 test): Task 6.
- Spec §5.3 (scalar fallback test): Task 5 Step 3 (via E2E harness dual-mode runs).
- Spec §5.4 (n-body unblock): Task 7.
- Spec §5.5 (broader regression): Task 8.
- Spec §5.6 (intrinsic-name probe): Task 4.
- Spec §6.1 (rollout order): Tasks 1-9 follow it.
- Spec §6.2 (file-by-file): plan top's File Structure matches.
- Spec §6.3 (risks): each risk addressed:
  - Risk 1 (intrinsic typos): Task 4 probe.
  - Risk 2 (float32 dispatch): Task 6 Step 3 explicit IR check.
  - Risk 3 (scalar mode): Task 5 Step 3 E2E dual-mode, Task 3 Step 2 fallback-if-needed.
  - Risk 4 (FMA precision): no specific task — acceptable per spec.
  - Risk 5 (n-body float reassoc): Task 7 Step 4b explicit partial-success handling.
  - Risk 6 (populate-Varying idiom): Tasks 5 and 6 use the `go for i, x := range data { v = x }` idiom confirmed from sibling tests.
- Spec §6.4 (out of scope): not implemented.
- Spec §6.5 (success criteria): mapped to Tasks 5 Step 3 (E2E), 7 Step 3 (n-body), 8 Step 1 (regression), 8 Step 2 (benchmarks).

No gaps.

### 2. Placeholder scan

No TBD/TODO/FIXME/XXX. Task 7 Step 4b's BLOCKER.md template uses `<fill in actual observed values>` as an engineer-fill-in during the partial-success path — that's documentation intent, not a plan placeholder.

### 3. Type consistency

- `createSpmdMathIntrinsic(name string, args []llvm.Value, pos token.Pos) (llvm.Value, error)` — Task 2, referenced identically in Task 3 cases.
- `floatingPoint` constraint: `~float32 | ~float64` — Task 1 Step 1, used by all 9 function pairs in Task 1 Step 2.
- Function name pairs (`Sqrt` + `sqrtBuiltin`, `Abs` + `absBuiltin`, etc.) consistent.
- LLVM intrinsic stems:
  - `Sqrt` → `sqrt`
  - `Abs` → `fabs`
  - `Floor` → `floor`
  - `Ceil` → `ceil`
  - `Round` → `round`
  - `Trunc` → `trunc`
  - `Min` → `minnum`
  - `Max` → `maxnum`
  - `FMA` → `fma`

Used consistently in Task 3 Step 1 dispatcher cases and Task 4 Step 3 intrinsic-name grep.

Consistent.
