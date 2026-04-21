# tinybench `go-spmd` Variant — Design

**Date**: 2026-04-20
**Scope**: Add a 7th compiler row, `go-spmd`, to the `tinybench` submodule
(`https://github.com/Bluebugs/tinybench`, fork of `tinygo-org/tinybench`),
covering all five existing benchmarks and using the SPMD fork of TinyGo on
native x86-64 with AVX2.

---

## 1. Overview & Goals

**Goal**: Produce a `go-spmd` compiler row in the tinybench comparison matrix
(zig / rust / go / tinygo / gcc / clang / **go-spmd**). Each existing benchmark
(`fannkuch-redux`, `fasta`, `n-body`, `n-body-nosqrt`, `spectral-norm`) gets a
new `<benchmark>/go-spmd/main.go` source. Ports use `go for`, `lanes.Varying[T]`,
and the `lanes` / `reduce` packages where the algorithm admits data parallelism;
they stay scalar (but compiled by the SPMD fork) where it doesn't.

**Non-goals**:

- WASM target — `go-spmd-wasm` deferred.
- SSE-only or scalar-fallback rows — AVX2 only, per user direction.
- Plotting / `RESULTS.md` automation — regenerated once, manually, after the
  spec is implemented.
- Maintaining tinybench's "equivalence in function signatures, statements and
  ordering" rule for the `go-spmd/` variants — explicitly relaxed. Idiomatic
  Go-with-SPMD is the standard.

**Invariants**:

- `go-spmd/main.go` output **must be byte-for-byte identical** to `go/main.go`
  for every `args.txt` case. Enforced by a new `TestCorrectness`.
- `go test -bench=.` without the SPMD fork available still works; the `go-spmd`
  row is gracefully skipped with a `b.Logf` line, exactly like an unavailable
  compiler today.
- No changes to existing `go/`, `c/`, `rust/`, `zig/` source files or stock
  compiler flags.

---

## 2. Layout Changes

```
tinybench/
├── bench_test.go              (modified — §3)
├── compilerflags_test.go      (modified — §3 / §4)
├── correctness_test.go        (NEW — §6)
├── fannkuch-redux/
│   └── go-spmd/main.go        (NEW — scalar port; no go for)
├── fasta/
│   └── go-spmd/main.go        (NEW — scalar port; stateful LCG doesn't vectorize)
├── n-body/
│   └── go-spmd/main.go        (NEW — go for over body pairs)
├── n-body-nosqrt/
│   └── go-spmd/main.go        (NEW — same shape as n-body)
├── spectral-norm/
│   └── go-spmd/main.go        (NEW — primary showcase)
└── README.md                  (modified — §7)
```

**Top-level scan**: `setup()` (`bench_test.go:182-206`) lists top-level
directories with no `.` or `_` in the name. The new SPMD ports live *inside*
existing benchmark directories; no top-level dir is added or renamed. The
existing dir-scan is unaffected.

**Per-benchmark dir-exists skip**: `bench_test.go:124-128` already does
`os.Stat(testname + "/" + compiler.Language)` and logs "skipped" when missing.
We use `Language: "go-spmd"`, so this maps cleanly to `<bench>/go-spmd/`.

---

## 3. Harness Changes

### 3.1 `Compiler` struct extension (`bench_test.go:15-23`)

```go
type Compiler struct {
    Language       string
    VersionCommand *exec.Cmd
    Compiler       string          // display name used in subtest labels and logs
    BinaryPath     string          // NEW: actual binary invoked; falls back to Compiler if empty
    OutputBinary   string
    MakeArgs       func(testname string) []string

    // NEW: optional environment for compile invocations (e.g. GOEXPERIMENT=spmd,
    // PATH prepended with the forked Go toolchain).
    Env       []string

    // NEW: optional pre-check; if non-nil and returns false, the compiler is
    // treated as unavailable even if VersionCommand succeeds.
    Available func() bool

    Version [3]int
}
```

Backward compat: existing entries leave `BinaryPath`, `Env`, and `Available`
zero. Invocation site uses `BinaryPath` if set, else `Compiler`.

### 3.2 Invocation site (`bench_test.go:136-146`)

```go
binary := compiler.BinaryPath
if binary == "" {
    binary = compiler.Compiler
}
cmd := exec.Command(binary, compArgs...)
if len(compiler.Env) > 0 {
    cmd.Env = append(os.Environ(), compiler.Env...)
}
out, err := cmd.CombinedOutput()
```

### 3.3 Availability check (`bench_test.go:95-108`)

After the existing `VersionCommand.Output()` branch, before the version-parse
branch:

```go
if c.Available != nil && !c.Available() {
    b.Logf("skipping all benchmarks for compiler %q (toolchain not found)", c.Compiler)
    continue
}
```

This runs *after* `VersionCommand` succeeds, so it's a belt-and-suspenders
check that catches "binary exists but isn't actually executable here" cases
beyond what `VersionCommand` detects.

### 3.4 New compiler entry (`compilerflags_test.go`)

Appended to the `compilers` slice after the stock `tinygo` entry:

```go
{
    Language:       "go-spmd",
    VersionCommand: spmdVersionCmd(),
    Compiler:       "tinygo-spmd",          // display name (subtest label / logs)
    BinaryPath:     spmdCompilerPath(),     // actual binary invoked
    OutputBinary:   "./go-spmd.bin",
    MakeArgs: func(testname string) []string {
        return append(goSpmdBaseFlags, "./"+testname+"/go-spmd/main.go")
    },
    Env:       spmdEnv(),
    Available: spmdAvailable,
},
```

### 3.5 Build flags (`compilerflags_test.go`)

```go
var goSpmdBaseFlags = []string{
    "build",
    "-opt=2",
    "-llvm-features=+avx2",
    "-o=go-spmd.bin",
}
```

`+avx2` implies `+ssse3` and `+sse4.2` via the SPMD fork's feature implication
chain (per the project's CLAUDE.md). No need to list them explicitly.

---

## 4. Toolchain Discovery

Four helper functions in `compilerflags_test.go`. Resolution order: env var
first, submodule-relative fallback second, skip if neither resolves to an
executable.

```go
func tinybenchDir() string {
    // setup() in bench_test.go asserts the working directory is tinybench root.
    wd, _ := os.Getwd()
    return wd
}

func spmdTinyGoPath() string {
    if p := os.Getenv("TINYGO_SPMD"); p != "" {
        return p
    }
    return filepath.Join(tinybenchDir(), "..", "tinygo", "build", "tinygo")
}

func spmdGoRoot() string {
    if p := os.Getenv("GOROOT_SPMD"); p != "" {
        return p
    }
    return filepath.Join(tinybenchDir(), "..", "go")
}

func spmdAvailable() bool {
    if fi, err := os.Stat(spmdTinyGoPath()); err != nil || fi.IsDir() || fi.Mode()&0111 == 0 {
        return false
    }
    if fi, err := os.Stat(filepath.Join(spmdGoRoot(), "bin", "go")); err != nil || fi.IsDir() {
        return false
    }
    return true
}

func spmdEnv() []string {
    goBin := filepath.Join(spmdGoRoot(), "bin")
    return []string{
        "GOEXPERIMENT=spmd",
        "PATH=" + goBin + string(os.PathListSeparator) + os.Getenv("PATH"),
    }
}

func spmdVersionCmd() *exec.Cmd {
    cmd := exec.Command(spmdTinyGoPath(), "version")
    cmd.Env = append(os.Environ(), spmdEnv()...)
    return cmd
}

func spmdCompilerPath() string { return spmdTinyGoPath() }
```

Construction at package-init time is safe even when the binary is absent:
`exec.Command` with a nonexistent path returns a valid `*Cmd` whose `Output()`
will fail at run time, hitting the existing skip path at `bench_test.go:98-99`.

`os.PathListSeparator` handles `:` vs `;` on Windows; `tinygo` shells out to
`go env` internally and honors `PATH`.

---

## 5. SPMD Port Strategy Per Benchmark

Each port is idiomatic Go-with-SPMD. I/O (`main`, argument parsing, `Printf`)
stays scalar. Output must be byte-identical to the scalar `go/main.go`,
enforced by §6.

### 5.1 `spectral-norm/go-spmd/main.go` — primary showcase

Inner-`j` reduction over `u[]` is the natural SPMD shape:

```go
func times(v, u Vec) {
    for i := 0; i < len(v); i++ {
        var acc lanes.Varying[float64]
        go for j, uj := range u {
            acc += uj / float64(evala(i, j))
        }
        v[i] = reduce.Add(acc)
    }
}
```

`evala(i, j)` becomes `Varying[int]` because `j` is varying. Same shape for
`times_trans` with swapped args. The final `vBv` / `vv` reduction loop in
`main` is also ported (trivial, consistent).

Expected speedup on AVX2 (4-wide float64 — 32-byte register ÷ 8-byte f64):
**2-4x** over stock tinygo, gated by memory bandwidth at large `n`.

### 5.2 `n-body/go-spmd/main.go` and `n-body-nosqrt/go-spmd/main.go`

Only 5 bodies, so the inner `j` loop is 4/3/2/1 iterations. Vectorize the
inner loop anyway (honest reporting):

```go
func advance(nbodies int, bodies []Planet, dt float64) {
    for i := 0; i < nbodies; i++ {
        b := &bodies[i]
        var dvx, dvy, dvz lanes.Varying[float64]
        go for j := i + 1; j < nbodies; j++ {
            b2 := &bodies[j]
            dx := b.x - b2.x
            // ...
            // per-lane updates to b2 via varying scatter into bodies[j]
        }
        b.vx -= reduce.Add(dvx)
        // ...
    }
}
```

`sqrt_newton` (in `n-body-nosqrt`) becomes a varying scalar loop — the
`for { ... if math.Abs(delta) <= tol { break } }` converges under a varying
mask, which the SPMD fork supports via break-mask predication.

Expected speedup: **<2x** likely; lane occupancy is poor with N=5.

The varying-scatter into shared `bodies[j]` is the riskiest pattern — if it
trips a fork bug, fall under the §5.4 blocker policy.

### 5.3 `fannkuch-redux/go-spmd/main.go` and `fasta/go-spmd/main.go`

Both are inherently sequential (recursive permutation generation; stateful
LCG + linear search). Ports are **near-copies of `go/main.go`** — same
algorithm, same output, no `lanes.*` imports, no `go for`. They prove the
SPMD fork produces correct native AVX2 binaries for realistic scalar
workloads and give the matrix an apples-to-apples row across all five
benchmarks. Expected speedup vs stock tinygo: **~1x** (any difference is
LLVM optimization noise, not SPMD).

### 5.4 Blocker handling policy

When a port hits a compiler issue:

1. **Preserve the SPMD source** — keep the intended `go for` /
   `lanes.Varying[T]` code in `<bench>/go-spmd/main.go` exactly as written.
   Do not downgrade to scalar.
2. **Disable the entry** — add `<bench>/go-spmd/BLOCKER.md` documenting:
   - the SPMD pattern attempted,
   - the exact compiler error or wrong-output behavior,
   - reproducer command (`PATH=... GOEXPERIMENT=spmd tinygo build ...`),
   - any narrowing already done (which sub-pattern fails, what works).
3. **Harness skip** — extend the per-benchmark loop in `bench_test.go` to
   check for `BLOCKER.md` alongside the existing dir-exists check. If
   present, log `b.Logf("%s: go-spmd BLOCKED — see %s/go-spmd/BLOCKER.md",
   testname, testname)` and continue. No build, no benchmark.
4. **Correctness test** — `TestCorrectness` (§6) treats `BLOCKER.md`-marked
   benchmarks as `t.Skip("blocked: <first line of BLOCKER.md>")` so the
   suite stays green; the skip message names the file.
5. **End-of-session report** — produce `BLOCKERS.md` at the tinybench root
   that aggregates every per-benchmark `BLOCKER.md` (one section per
   blocked benchmark, links to the source). If no blockers exist,
   `BLOCKERS.md` is **not created** — its absence is the all-clear.

The broken SPMD code stays in place as a regression test for future
fork work; re-enabling a blocked benchmark is one `rm BLOCKER.md` away.

---

## 6. Correctness Test Harness

New file `correctness_test.go` with a single `TestCorrectness` that
guarantees `go-spmd/main.go` output matches `go/main.go` byte-for-byte.

### 6.1 Top-level structure

```go
package tinybench

import (
    "bytes"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
)

func TestCorrectness(t *testing.T) {
    benchnames := setup() // reused from bench_test.go

    var ref, spmd *Compiler
    for i := range compilers {
        switch compilers[i].Compiler {
        case "go":
            ref = &compilers[i]
        case "tinygo-spmd":
            spmd = &compilers[i]
        }
    }
    if ref == nil {
        t.Fatal("stock go compiler not found in compilers list")
    }
    if spmd == nil || (spmd.Available != nil && !spmd.Available()) {
        t.Skip("go-spmd toolchain not available; skipping correctness comparison")
    }
    probeVersion(t, ref)
    probeVersion(t, spmd)

    for _, testname := range benchnames {
        t.Run(testname, func(t *testing.T) {
            spmdDir := filepath.Join(testname, "go-spmd")
            if _, err := os.Stat(spmdDir); os.IsNotExist(err) {
                t.Skipf("no go-spmd port for %s", testname)
            }
            blocker := filepath.Join(spmdDir, "BLOCKER.md")
            if fileExists(blocker) {
                b, _ := os.ReadFile(blocker)
                firstLine := strings.SplitN(string(b), "\n", 2)[0]
                t.Skipf("go-spmd blocked: %s (see %s)", firstLine, blocker)
            }

            compileWith(t, ref, testname)
            compileWith(t, spmd, testname)

            cases := readArgs(t, testname)
            for _, argline := range cases {
                args := strings.Split(argline, " ")
                refOut := mustRun(t, ref.OutputBinary, args)
                spmdOut := mustRun(t, spmd.OutputBinary, args)
                if !bytes.Equal(refOut, spmdOut) {
                    t.Fatalf("%s args=%q: output mismatch\n--- go ---\n%s\n--- go-spmd ---\n%s",
                        testname, argline, refOut, spmdOut)
                }
            }
        })
    }
}
```

### 6.2 Helpers

- `compileWith(t, *Compiler, testname)` — runs the compiler with `Env`
  applied, fails on error. Refactor the body of the existing
  `ensureCompile` closure in `BenchmarkAll` into this package-level helper
  so both `BenchmarkAll` and `TestCorrectness` share construction logic.
- `mustRun(t, binary, args)` — `exec.Command(binary, args...).Output()`,
  fails on non-zero exit, returns stdout.
- `readArgs(t, testname)` — reads `<testname>/args.txt`, splits on `\n`,
  trims empties.
- `fileExists(path)` — `os.Stat` helper.
- `probeVersion(t, *Compiler)` — runs `VersionCommand`, populates `Version`
  via the existing `parseNextSemanticVersion`.

### 6.3 Behavior

- Subtests via `t.Run(testname, ...)`: one mismatch fails its own subtest
  but lets others continue.
- Within a benchmark, first mismatch uses `t.Fatalf` to stop further args
  cases — first failure is enough to characterize the bug.
- No output normalization. Float reassociation differences are real
  divergence; if a benchmark genuinely needs tolerance, that's a §5.4
  blocker.

### 6.4 Run order

Benchmarks do not depend on correctness passing — separate `go test`
entry points. CI / human convention: run `TestCorrectness` first; only
trust `BenchmarkAll` numbers once correctness is green. README §7 calls
this out explicitly.

---

## 7. Documentation

### 7.1 `README.md` — new "go-spmd variant" section

Inserted after "Compilers", before "Run Benchmarks". Covers:

- What the row is (SPMD fork of TinyGo, x86-64 AVX2, link to fork).
- Per-benchmark port style (`lanes.Varying[T]`, `go for`, `reduce.*`).
- Toolchain discovery: env vars (`TINYGO_SPMD`, `GOROOT_SPMD`) +
  submodule fallback (`../tinygo/build/tinygo`, `../go`).
- Skip behavior when toolchain absent.
- Output-parity gate via `go test -run TestCorrectness`.
- Disabled ports via `BLOCKER.md` and aggregated `BLOCKERS.md`.

### 7.2 `README.md` — "Add a benchmark" section

Append a paragraph: a new benchmark with a `go-spmd/main.go` is picked up
automatically; same `args.txt` applies; no harness changes.

### 7.3 `BLOCKERS.md` (NEW, top-level)

Generated mechanically at session end by aggregating per-benchmark
`BLOCKER.md` files. **Not created** if no blockers exist (absence = all
clear; do not write a placeholder).

### 7.4 Out of scope

- `RESULTS.md` regeneration — manual one-shot after implementation.
- `benchmark.png` — `plot_/` already accepts any compiler set; manual
  regeneration.

---

## 8. Testing & Verification

Three levels:

1. **Compile parity** — implicit in `TestCorrectness`: every non-blocked
   benchmark must build under both `go` and the SPMD fork.
2. **Output parity** — `TestCorrectness` byte-diffs stdout for every args
   case.
3. **Performance comparison** — `BenchmarkAll` (existing harness, now with
   the 7th `go-spmd` row) measures wall time. No assertions; performance
   is reported, not enforced.

### 8.1 Local verification commands

```bash
# Build the SPMD fork once (from SPMD repo root)
make build

# From tinybench/:
go test -v -run TestCorrectness                 # output parity
go test -v -bench BenchmarkAll/spectral-norm    # one benchmark
go test -v -bench .                             # full matrix
```

### 8.2 Skip-behavior matrix

| Condition | TestCorrectness | BenchmarkAll |
|---|---|---|
| SPMD fork not built | skip whole test | skip `go-spmd` rows, run rest |
| Benchmark has no `go-spmd/` dir | skip subtest | skip that compiler×benchmark cell |
| `BLOCKER.md` present | skip subtest with reason | skip cell with `b.Logf` reason |
| Output mismatch | `t.Fatalf` for that benchmark | (not detected here — gated separately) |

---

## 9. File-by-file change summary

| File | Change |
|---|---|
| `tinybench/bench_test.go` | Extend `Compiler` struct (`BinaryPath`, `Env`, `Available`); use `BinaryPath` in `ensureCompile`; call `Available()` after version probe; refactor `ensureCompile` body into `compileWith` helper. |
| `tinybench/compilerflags_test.go` | Add `goSpmdBaseFlags`; add `spmdTinyGoPath` / `spmdGoRoot` / `spmdAvailable` / `spmdEnv` / `spmdVersionCmd` / `spmdCompilerPath` helpers; add `go-spmd` entry to `compilers`. |
| `tinybench/correctness_test.go` | NEW — `TestCorrectness` + helpers. |
| `tinybench/fannkuch-redux/go-spmd/main.go` | NEW — scalar port. |
| `tinybench/fasta/go-spmd/main.go` | NEW — scalar port. |
| `tinybench/n-body/go-spmd/main.go` | NEW — `go for` over inner pair loop + scatter. |
| `tinybench/n-body-nosqrt/go-spmd/main.go` | NEW — same as `n-body` + varying `sqrt_newton`. |
| `tinybench/spectral-norm/go-spmd/main.go` | NEW — `go for` + `reduce.Add` reductions. |
| `tinybench/README.md` | Add "go-spmd variant" section + addendum to "Add a benchmark". |
| `tinybench/BLOCKERS.md` | NEW only if any per-benchmark `BLOCKER.md` exists at session end. |

`.gitignore` already covers `*bin` (matches `go-spmd.bin`) — no change needed.

---

## 10. Open questions / deferred

- **WASM variant** (`go-spmd-wasm`, wasmtime) — deferred; would add a second
  compiler entry following the same pattern.
- **Scalar-mode comparison row** (`go-spmd-scalar`, `-simd=false`) — out of
  scope; user direction was AVX2 only.
- **`RESULTS.md` / `benchmark.png` regeneration** — manual after
  implementation, not automated by this design.
- **CI integration** — out of scope; `TestCorrectness` is invocable from
  any CI harness but no CI changes are part of this spec.
