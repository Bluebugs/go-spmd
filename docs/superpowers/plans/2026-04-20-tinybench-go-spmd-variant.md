# tinybench `go-spmd` Variant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Project workflow override:** Per `/home/cedric/work/SPMD/CLAUDE.md`, all implementation work MUST follow the 3-step pipeline: `golang-pro` → `code-reviewer` → `clean-commit`. Each task below is one logical change suitable for one pipeline pass. The "Commit" step at the end of each task corresponds to the `clean-commit` agent.

**Goal:** Add a `go-spmd` compiler row to the `tinybench` submodule that compiles each of the five existing benchmarks with the SPMD fork of TinyGo on native x86-64 AVX2, with byte-identical output gated by a `TestCorrectness` test.

**Architecture:** Extend the existing `Compiler` struct in `tinybench/bench_test.go` with `BinaryPath`, `Env`, and `Available` fields so the harness can launch the forked tinygo binary with `GOEXPERIMENT=spmd` and the forked Go on `PATH`. Add a `go-spmd` entry that auto-discovers the toolchain via env vars (`TINYGO_SPMD`, `GOROOT_SPMD`) or submodule-relative paths. Add per-benchmark `go-spmd/main.go` ports — vectorized where the algorithm admits it (`spectral-norm`, `n-body`, `n-body-nosqrt`), scalar copies where it doesn't (`fannkuch-redux`, `fasta`). Disabled ports preserve their broken SPMD source under a sibling `BLOCKER.md` file.

**Tech Stack:**
- Go 1.23+ (tinybench `go.mod`)
- TinyGo SPMD fork (`/home/cedric/work/SPMD/tinygo/build/tinygo`)
- Forked Go (`/home/cedric/work/SPMD/go/bin`) with `GOEXPERIMENT=spmd`
- LLVM AVX2 target features
- `lanes` and `reduce` SPMD packages (resolved through the forked stdlib)

**Spec:** `docs/superpowers/specs/2026-04-20-tinybench-go-spmd-variant-design.md`

**Working directory for all tinybench changes:** `/home/cedric/work/SPMD/tinybench/`. The tinybench submodule has its own git history (`origin git@github.com:Bluebugs/tinybench.git`); commits live in that repo, not the parent SPMD repo. The parent SPMD repo only tracks the submodule pointer.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `tinybench/bench_test.go` | modified | `Compiler` struct + harness invocation + `compileWith` helper + `BLOCKER.md` skip |
| `tinybench/compilerflags_test.go` | modified | `goSpmdBaseFlags`, toolchain discovery helpers, `go-spmd` entry |
| `tinybench/correctness_test.go` | NEW | `TestCorrectness` + `mustRun` / `readArgs` / `fileExists` / `probeVersion` helpers |
| `tinybench/fannkuch-redux/go-spmd/main.go` | NEW | Scalar port (sequential algorithm) |
| `tinybench/fasta/go-spmd/main.go` | NEW | Scalar port (stateful LCG) |
| `tinybench/spectral-norm/go-spmd/main.go` | NEW | Vectorized inner reduction with `go for` + `reduce.Add` |
| `tinybench/n-body/go-spmd/main.go` | NEW | Vectorized inner pair loop with varying scatter |
| `tinybench/n-body-nosqrt/go-spmd/main.go` | NEW | Same as n-body + varying `sqrt_newton` |
| `tinybench/README.md` | modified | "go-spmd variant" section + "Add a benchmark" addendum |
| `tinybench/<bench>/go-spmd/BLOCKER.md` | conditional | Per-benchmark blocker doc — only created if a port hits a compiler issue |
| `tinybench/BLOCKERS.md` | conditional | Top-level aggregation — only created if any per-benchmark BLOCKER.md exists |

---

## Task 0: Pre-flight

**Files:** None (verification only).

- [ ] **Step 1: Verify SPMD fork is built**

```bash
ls -la /home/cedric/work/SPMD/tinygo/build/tinygo /home/cedric/work/SPMD/go/bin/go
```

Expected: both files present and executable. If missing, run from `/home/cedric/work/SPMD/`:

```bash
make build
```

- [ ] **Step 2: Verify tinybench submodule baseline**

```bash
cd /home/cedric/work/SPMD/tinybench
git status --short
git rev-parse --abbrev-ref HEAD
```

Expected: clean working tree, branch `main`.

- [ ] **Step 3: Smoke-test the existing harness compiles**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -c -o /tmp/tinybench.test .
ls -la /tmp/tinybench.test
rm /tmp/tinybench.test
```

Expected: `tinybench.test` produced (proves existing test files build cleanly before we touch them).

- [ ] **Step 4: Smoke-test forked toolchain version probe**

```bash
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd \
  /home/cedric/work/SPMD/tinygo/build/tinygo version
```

Expected: prints a version string starting with `tinygo version <ver>`. If this fails, fix the fork build before proceeding.

---

## Task 1: Extend `Compiler` struct and invocation site

**Files:**
- Modify: `tinybench/bench_test.go:15-23` (struct), `tinybench/bench_test.go:136-146` (invocation), `tinybench/bench_test.go:95-108` (availability check)

- [ ] **Step 1: Extend the `Compiler` struct**

Replace the current struct definition at `tinybench/bench_test.go:15-27` with:

```go
type Compiler struct {
	Language       string
	VersionCommand *exec.Cmd
	Compiler       string         // display name used in subtest labels and logs
	BinaryPath     string         // optional: actual binary invoked; falls back to Compiler if empty
	OutputBinary   string
	MakeArgs       func(testname string) []string

	// Optional environment for compile invocations (e.g. GOEXPERIMENT=spmd,
	// PATH prepended with the forked Go toolchain).
	Env []string

	// Optional pre-check; if non-nil and returns false, the compiler is
	// treated as unavailable even when VersionCommand succeeds.
	Available func() bool

	Version [3]int // 0:Major, 1:Minor, 2:Patch
}
```

- [ ] **Step 2: Update the compile invocation to use `BinaryPath` and `Env`**

In `tinybench/bench_test.go`, locate the `ensureCompile` closure (currently `bench_test.go:133-147`) and replace its body. Before:

```go
var onceCompile sync.Once
ensureCompile := func(b *testing.B) {
	onceCompile.Do(func() {
		compArgs := compiler.MakeArgs(testname)
		out, err := exec.Command(compiler.Compiler, compArgs...).CombinedOutput()
		if err != nil {
			b.Fatalf("%s: building with %s flags=%v:\n%s", testname, compiler.Compiler, compArgs, out)
		}
		finfo, err := os.Stat(compiler.OutputBinary)
		if err != nil {
			b.Fatalf("%s: os.Stat(%q): %s", testname, compiler.OutputBinary, err.Error())
		}
		b.Logf("name=%q compiler=%q binarysize=%d version=%s\n", testname, compiler.Compiler, finfo.Size(), compiler.VersionString())
	})
}
```

After:

```go
var onceCompile sync.Once
ensureCompile := func(b *testing.B) {
	onceCompile.Do(func() {
		compArgs := compiler.MakeArgs(testname)
		binary := compiler.BinaryPath
		if binary == "" {
			binary = compiler.Compiler
		}
		cmd := exec.Command(binary, compArgs...)
		if len(compiler.Env) > 0 {
			cmd.Env = append(os.Environ(), compiler.Env...)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			b.Fatalf("%s: building with %s flags=%v:\n%s", testname, compiler.Compiler, compArgs, out)
		}
		finfo, err := os.Stat(compiler.OutputBinary)
		if err != nil {
			b.Fatalf("%s: os.Stat(%q): %s", testname, compiler.OutputBinary, err.Error())
		}
		b.Logf("name=%q compiler=%q binarysize=%d version=%s\n", testname, compiler.Compiler, finfo.Size(), compiler.VersionString())
	})
}
```

- [ ] **Step 3: Add the `Available` check after the version probe**

In `tinybench/bench_test.go`, locate the per-compiler version probe loop (currently `bench_test.go:95-108`). After the `if err != nil` block (which already logs "skipping" and `continue`s), and before the `vMajor, vMinor, vPatch, ok := ...` line, insert:

```go
		if c.Available != nil && !c.Available() {
			b.Logf("skipping all benchmarks for compiler %q (toolchain not available)", c.Compiler)
			continue
		}
```

The full updated loop should look like:

```go
for i, c := range compilers {
	version, err := c.VersionCommand.Output()
	if err != nil {
		b.Logf("skipping all benchmarks for compiler %q", c.Compiler)
		continue
	}
	if c.Available != nil && !c.Available() {
		b.Logf("skipping all benchmarks for compiler %q (toolchain not available)", c.Compiler)
		continue
	}
	var ok bool
	vMajor, vMinor, vPatch, ok := parseNextSemanticVersion(string(version))
	if !ok {
		b.Fatalf("unable to parse version for %s compiler from version output:\n%s", c.Compiler, version)
	}
	compilers[i].Version = [3]int{vMajor, vMinor, vPatch}
	b.Logf("found compiler %s %d.%d.%d", c.Compiler, vMajor, vMinor, vPatch)
}
```

- [ ] **Step 4: Smoke-test that existing benchmarks still work**

Run a single, fast existing benchmark (this proves the struct + invocation refactor is backward-compatible):

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -bench='BenchmarkAll/fannkuch-redux:args=6/go/go' -benchtime=1x -run=^$ -timeout=120s
```

Expected: PASS, with one `BenchmarkAll/fannkuch-redux:args=6/go/go-N` line. No "go-spmd" entries yet (we haven't added it).

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinybench
git add bench_test.go
git commit -m "harness: extend Compiler struct with BinaryPath, Env, Available

Adds three optional fields to the Compiler struct so the harness can
launch a binary at an absolute path with a customized environment
(used by the forthcoming go-spmd entry to pass GOEXPERIMENT=spmd and
prepend the forked Go toolchain to PATH). The Available callback lets
a compiler entry signal 'toolchain not present' without failing the
version probe.

Existing entries are unchanged: BinaryPath empty falls back to
Compiler (PATH lookup); Env empty inherits os.Environ as before;
Available nil skips the new pre-check."
```

---

## Task 2: Toolchain discovery helpers, build flags, and `go-spmd` entry

**Files:**
- Modify: `tinybench/compilerflags_test.go`

- [ ] **Step 1: Add `goSpmdBaseFlags`**

Append to `tinybench/compilerflags_test.go`:

```go
var goSpmdBaseFlags = []string{
	"build",
	"-opt=2",
	"-llvm-features=+avx2",
	"-o=go-spmd.bin",
}
```

- [ ] **Step 2: Add toolchain discovery helpers**

Append to `tinybench/compilerflags_test.go` (and add `"os"`, `"os/exec"`, `"path/filepath"` to the imports if not already present — note the file currently has no imports because it's pure data; switch it to `import (...)` when adding helpers):

```go
import (
	"os"
	"os/exec"
	"path/filepath"
)

func tinybenchDir() string {
	// setup() in bench_test.go asserts the working directory is the tinybench
	// module root, so os.Getwd() is the right reference point.
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

- [ ] **Step 3: Add the `go-spmd` compiler entry**

In `tinybench/compilerflags_test.go`, the `compilers` slice lives in `bench_test.go` (lines 33-84). Open `tinybench/bench_test.go` and append a new entry to the slice (after the closing `}` of the `clang` entry, before the `}` that closes the slice):

```go
	{
		Language:       "go-spmd",
		VersionCommand: spmdVersionCmd(),
		Compiler:       "tinygo-spmd",
		BinaryPath:     spmdCompilerPath(),
		OutputBinary:   "./go-spmd.bin",
		MakeArgs: func(testname string) []string {
			return append(goSpmdBaseFlags, "./"+testname+"/go-spmd/main.go")
		},
		Env:       spmdEnv(),
		Available: spmdAvailable,
	},
```

- [ ] **Step 4: Build the test binary to confirm everything compiles**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -c -o /tmp/tinybench.test .
rm /tmp/tinybench.test
```

Expected: clean build. If imports are missing in `compilerflags_test.go`, fix and rerun.

- [ ] **Step 5: Smoke-test the harness recognizes the new entry**

Run the harness with no benchmarks selected, just to print the compiler-found / skipped lines:

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -bench='BenchmarkAll/fannkuch-redux:args=6/go/go' -benchtime=1x -run=^$ -timeout=120s 2>&1 | head -30
```

Expected: among the `bench_test.go:107: found compiler ...` lines, one should read `found compiler tinygo-spmd <ver>`. If the SPMD fork isn't built, expect instead `skipping all benchmarks for compiler "tinygo-spmd"`.

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinybench
git add bench_test.go compilerflags_test.go
git commit -m "harness: add go-spmd compiler entry with AVX2 build flags

Adds a 7th compiler row, go-spmd, that invokes the SPMD fork of
TinyGo (https://github.com/Bluebugs/tinygo, spmd branch) with
GOEXPERIMENT=spmd and the forked Go toolchain on PATH, targeting
native x86-64 with -llvm-features=+avx2.

Toolchain auto-discovery: env vars TINYGO_SPMD and GOROOT_SPMD
take precedence; otherwise the harness falls back to the submodule
layout (../tinygo/build/tinygo and ../go relative to the tinybench
directory). When neither is found, the entry is skipped via the
Available callback and the rest of the matrix runs normally.

No go-spmd/ source dirs exist yet, so per-benchmark cells will log
'skipped' until the ports land."
```

---

## Task 3: Correctness test infrastructure

**Files:**
- Modify: `tinybench/bench_test.go` (introduce `compileWith` helper, refactor `ensureCompile` to use it)
- Create: `tinybench/correctness_test.go`

- [ ] **Step 1: Refactor compile logic into a package-level helper**

Add to `tinybench/bench_test.go` (anywhere after the `Compiler` struct, e.g. after the `compilers` slice):

```go
// compileWith runs the compiler defined by c on the given testname,
// honoring c.BinaryPath (falling back to c.Compiler) and c.Env.
// Returns combined output and error so callers can choose how to report.
func compileWith(c *Compiler, testname string) ([]byte, error) {
	compArgs := c.MakeArgs(testname)
	binary := c.BinaryPath
	if binary == "" {
		binary = c.Compiler
	}
	cmd := exec.Command(binary, compArgs...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd.CombinedOutput()
}
```

Then replace the body of the `ensureCompile` closure to call it. Replace the inline compile block (introduced in Task 1, Step 2) with:

```go
var onceCompile sync.Once
ensureCompile := func(b *testing.B) {
	onceCompile.Do(func() {
		out, err := compileWith(&compiler, testname)
		if err != nil {
			b.Fatalf("%s: building with %s flags=%v:\n%s", testname, compiler.Compiler, compiler.MakeArgs(testname), out)
		}
		finfo, err := os.Stat(compiler.OutputBinary)
		if err != nil {
			b.Fatalf("%s: os.Stat(%q): %s", testname, compiler.OutputBinary, err.Error())
		}
		b.Logf("name=%q compiler=%q binarysize=%d version=%s\n", testname, compiler.Compiler, finfo.Size(), compiler.VersionString())
	})
}
```

- [ ] **Step 2: Create `correctness_test.go`**

Create `tinybench/correctness_test.go` with this complete content:

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

// TestCorrectness compiles each benchmark with both the stock Go compiler
// (reference) and the go-spmd compiler, runs both binaries with every args
// case, and fails if their stdout differs by even one byte.
//
// It is the gate for trusting BenchmarkAll numbers: a benchmark whose
// go-spmd port produces wrong output should not be benchmarked.
func TestCorrectness(t *testing.T) {
	benchnames := setup()

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
	if spmd == nil {
		t.Fatal("tinygo-spmd compiler not found in compilers list")
	}
	if spmd.Available != nil && !spmd.Available() {
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
				data, _ := os.ReadFile(blocker)
				firstLine := strings.SplitN(string(data), "\n", 2)[0]
				t.Skipf("go-spmd blocked: %s (see %s)", firstLine, blocker)
			}

			if out, err := compileWith(ref, testname); err != nil {
				t.Fatalf("compile %s with %s failed: %v\n%s", testname, ref.Compiler, err, out)
			}
			if out, err := compileWith(spmd, testname); err != nil {
				t.Fatalf("compile %s with %s failed: %v\n%s", testname, spmd.Compiler, err, out)
			}

			cases := readArgs(t, testname)
			for _, argline := range cases {
				args := strings.Split(argline, " ")
				refOut := mustRun(t, ref.OutputBinary, args)
				spmdOut := mustRun(t, spmd.OutputBinary, args)
				if !bytes.Equal(refOut, spmdOut) {
					t.Fatalf("%s args=%q: output mismatch\n--- go (%d bytes) ---\n%s\n--- go-spmd (%d bytes) ---\n%s",
						testname, argline, len(refOut), refOut, len(spmdOut), spmdOut)
				}
			}
		})
	}
}

func mustRun(t *testing.T, binary string, args []string) []byte {
	t.Helper()
	out, err := exec.Command(binary, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("running %s %v: %v\nstderr:\n%s", binary, args, err, ee.Stderr)
		}
		t.Fatalf("running %s %v: %v", binary, args, err)
	}
	return out
}

func readArgs(t *testing.T, testname string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testname, "args.txt"))
	if err != nil {
		t.Fatalf("reading %s/args.txt: %v", testname, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func probeVersion(t *testing.T, c *Compiler) {
	t.Helper()
	if c.Version != [3]int{} {
		return // already probed (e.g. by BenchmarkAll in same test process)
	}
	out, err := c.VersionCommand.Output()
	if err != nil {
		t.Fatalf("version probe for %s failed: %v", c.Compiler, err)
	}
	maj, min, patch, ok := parseNextSemanticVersion(string(out))
	if !ok {
		t.Fatalf("could not parse version for %s from output:\n%s", c.Compiler, out)
	}
	c.Version = [3]int{maj, min, patch}
}
```

- [ ] **Step 3: Build the test binary**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -c -o /tmp/tinybench.test .
rm /tmp/tinybench.test
```

Expected: clean build.

- [ ] **Step 4: Run TestCorrectness — expect all subtests to skip**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run TestCorrectness -timeout=120s
```

Expected (no `go-spmd/` ports exist yet): the test prints SKIP for each benchmark with `no go-spmd port for <name>`. Overall result: `--- PASS: TestCorrectness` (subtests skipped, not failed). If the SPMD fork isn't available, the top-level test SKIPs entirely with `go-spmd toolchain not available`.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinybench
git add bench_test.go correctness_test.go
git commit -m "test: add TestCorrectness gate for go-spmd output parity

Adds a TestCorrectness that compiles each benchmark with both stock
go and go-spmd, runs both binaries against every args.txt case, and
fails on any byte-level stdout difference.

Skips at three levels:
  - whole test if go-spmd toolchain isn't available
  - per-benchmark subtest if no <bench>/go-spmd/ directory exists
  - per-benchmark subtest if <bench>/go-spmd/BLOCKER.md exists, with
    the blocker's first line surfaced in the skip message

Refactors the compile call out of the BenchmarkAll closure into a
package-level compileWith helper so both the benchmark harness and
the correctness test share one code path for compilation."
```

---

## Task 4: `BLOCKER.md` skip extension to `BenchmarkAll`

**Files:**
- Modify: `tinybench/bench_test.go` (add BLOCKER.md check next to dir-exists check, around line 124-131)

- [ ] **Step 1: Add the BLOCKER.md check**

In `tinybench/bench_test.go`, locate the per-benchmark per-compiler loop, currently:

```go
for _, compiler := range compilers {
	if !compiler.CanRun() {
		continue
	}
	testDir := testname + "/" + compiler.Language
	_, err := os.Stat(testDir)
	if os.IsNotExist(err) {
		b.Logf("%s skipped for %s", testname, compiler.Compiler)
		continue
	} else if err != nil {
		b.Fatal(err)
	}
	// ... ensureCompile, runBench loop ...
}
```

Insert immediately after the `else if err != nil` branch and before the `var onceCompile sync.Once` declaration:

```go
	if fileExists(filepath.Join(testDir, "BLOCKER.md")) {
		b.Logf("%s: %s BLOCKED — see %s/BLOCKER.md", testname, compiler.Compiler, testDir)
		continue
	}
```

(`fileExists` is the helper added in Task 3 step 2; both `_test.go` files share the package so it's directly callable. Add `"path/filepath"` to the `bench_test.go` imports if it's not already there — it isn't in the current file.)

- [ ] **Step 2: Build to confirm imports compile**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -c -o /tmp/tinybench.test .
rm /tmp/tinybench.test
```

Expected: clean build.

- [ ] **Step 3: Functional test — fake a blocker, run, observe skip log, clean up**

```bash
cd /home/cedric/work/SPMD/tinybench
mkdir -p spectral-norm/go-spmd
echo "Test blocker (delete me)" > spectral-norm/go-spmd/BLOCKER.md
go test -v -bench='BenchmarkAll/spectral-norm:args=1000/go-spmd' -benchtime=1x -run=^$ -timeout=120s 2>&1 | grep -E "(BLOCKED|spectral-norm)"
rm -rf spectral-norm/go-spmd
```

Expected: a log line containing `spectral-norm: tinygo-spmd BLOCKED — see spectral-norm/go-spmd/BLOCKER.md`. No build attempt for that cell.

- [ ] **Step 4: Confirm the directory cleanup is clean**

```bash
cd /home/cedric/work/SPMD/tinybench
git status --short
```

Expected: only `M bench_test.go` (no leftover `spectral-norm/go-spmd/` from Step 3).

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinybench
git add bench_test.go
git commit -m "harness: skip benchmark cells with BLOCKER.md sentinel

When <benchmark>/<lang>/BLOCKER.md exists, BenchmarkAll logs a BLOCKED
line and skips the cell without attempting to build. Lets us preserve
broken go-spmd ports as regression tests for future compiler work
without breaking the main benchmark run."
```

---

## Task 5: `fannkuch-redux/go-spmd/main.go` (scalar port)

**Files:**
- Create: `tinybench/fannkuch-redux/go-spmd/main.go`

This benchmark generates permutations and is inherently sequential (recursive `tk` with rotation). The SPMD port is a scalar copy of `go/main.go` — no `go for`, no `lanes.*` imports — purely to validate the SPMD fork produces a correct AVX2 binary for typical scalar Go.

- [ ] **Step 1: Create the port**

Copy `tinybench/fannkuch-redux/go/main.go` verbatim to `tinybench/fannkuch-redux/go-spmd/main.go`:

```bash
cd /home/cedric/work/SPMD/tinybench
mkdir -p fannkuch-redux/go-spmd
cp fannkuch-redux/go/main.go fannkuch-redux/go-spmd/main.go
```

- [ ] **Step 2: Manual compile test**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/fk-spmd.bin fannkuch-redux/go-spmd/main.go
ls -la /tmp/fk-spmd.bin
```

Expected: binary produced. If it fails, this is a blocker — go to Step 5b.

- [ ] **Step 3: Manual output parity check**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/fk-go.bin ./fannkuch-redux/go/main.go
diff <(/tmp/fk-go.bin 6) <(/tmp/fk-spmd.bin 6)
diff <(/tmp/fk-go.bin 7) <(/tmp/fk-spmd.bin 7)
diff <(/tmp/fk-go.bin 9) <(/tmp/fk-spmd.bin 9)
rm /tmp/fk-go.bin /tmp/fk-spmd.bin
```

Expected: no output from any `diff` (byte-identical). If any diff, this is a blocker — go to Step 5b.

- [ ] **Step 4: TestCorrectness gate**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run 'TestCorrectness/fannkuch-redux' -timeout=180s
```

Expected: `--- PASS: TestCorrectness/fannkuch-redux`.

- [ ] **Step 5a: Commit (success path)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add fannkuch-redux/go-spmd/main.go
git commit -m "fannkuch-redux: add scalar go-spmd port

The fannkuch-redux algorithm (recursive permutation generation) has no
data parallelism worth vectorizing; this port is a verbatim copy of
go/main.go that proves the SPMD fork produces a correct AVX2 binary
for realistic sequential Go code. Output is byte-identical to the
stock go/ build (validated by TestCorrectness)."
```

- [ ] **Step 5b: Blocker path (only if Step 2, 3, or 4 failed)**

Create `tinybench/fannkuch-redux/go-spmd/BLOCKER.md` with this template:

```markdown
# fannkuch-redux go-spmd: BLOCKED

**Symptom:** <one-line summary, e.g. "compile error: ..." or "wrong output for args=9">

**Pattern:** Scalar Go (no `go for`, no `lanes.*`).

**Reproducer:**

\`\`\`bash
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/fk-spmd.bin fannkuch-redux/go-spmd/main.go
\`\`\`

**Compiler error / wrong output:**

\`\`\`
<paste exact error or output diff here>
\`\`\`

**Narrowing done:**

<which sub-pattern fails, what works — fill in during diagnosis>

**Next steps:** <what fix in the SPMD fork would unblock this>
```

Then commit:

```bash
cd /home/cedric/work/SPMD/tinybench
git add fannkuch-redux/go-spmd/main.go fannkuch-redux/go-spmd/BLOCKER.md
git commit -m "fannkuch-redux: add go-spmd port (BLOCKED)

Scalar copy of go/main.go intended to validate the SPMD fork on a
sequential workload. Currently blocked — see BLOCKER.md for the
exact failure and reproducer. The source is preserved as a regression
test for the eventual fix."
```

---

## Task 6: `fasta/go-spmd/main.go` (scalar port)

**Files:**
- Create: `tinybench/fasta/go-spmd/main.go`

The `fasta` benchmark uses a stateful linear congruential RNG and a per-element linear search — both inherently sequential. Like fannkuch-redux, the SPMD port is a scalar copy.

- [ ] **Step 1: Create the port**

```bash
cd /home/cedric/work/SPMD/tinybench
mkdir -p fasta/go-spmd
cp fasta/go/main.go fasta/go-spmd/main.go
```

- [ ] **Step 2: Manual compile test**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/fa-spmd.bin fasta/go-spmd/main.go
ls -la /tmp/fa-spmd.bin
```

Expected: binary produced. Failure → blocker (Step 5b).

- [ ] **Step 3: Manual output parity check (smaller arg for speed)**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/fa-go.bin ./fasta/go/main.go
diff <(/tmp/fa-go.bin 100000) <(/tmp/fa-spmd.bin 100000)
rm /tmp/fa-go.bin /tmp/fa-spmd.bin
```

Expected: no diff output. The full args.txt cases (12500000, 25000000) are large and slow; they're covered by TestCorrectness in Step 4 — but the manual test here uses a small input for fast iteration.

- [ ] **Step 4: TestCorrectness gate (full args.txt cases)**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run 'TestCorrectness/fasta' -timeout=600s
```

Expected: `--- PASS: TestCorrectness/fasta`. Note: args.txt has values 12500000 and 25000000; running both binaries twice each can take several minutes — hence the 600s timeout.

- [ ] **Step 5a: Commit (success path)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add fasta/go-spmd/main.go
git commit -m "fasta: add scalar go-spmd port

The fasta benchmark uses a stateful LCG (sequential by definition) and
a per-element linear search; neither vectorizes. This port is a
verbatim copy of go/main.go that confirms the SPMD fork produces a
correct AVX2 binary for stateful sequential workloads."
```

- [ ] **Step 5b: Blocker path (only if any step above failed)**

Create `tinybench/fasta/go-spmd/BLOCKER.md` using the template from Task 5 Step 5b (substitute `fasta` for `fannkuch-redux` and `fa-spmd.bin` for `fk-spmd.bin`). Commit message:

```bash
cd /home/cedric/work/SPMD/tinybench
git add fasta/go-spmd/main.go fasta/go-spmd/BLOCKER.md
git commit -m "fasta: add go-spmd port (BLOCKED)

Scalar copy of go/main.go for the SPMD fork. Currently blocked — see
BLOCKER.md for the failure detail and reproducer."
```

---

## Task 7: `spectral-norm/go-spmd/main.go` (vectorized — primary showcase)

**Files:**
- Create: `tinybench/spectral-norm/go-spmd/main.go`

`spectral-norm` is the primary SPMD target. The hot kernels `times` and `times_trans` perform `v[i] = Σ_j u[j] / evala(...)` over vectors of length `n` (1000, 2500, 5500). The inner `j` loop is a perfect SPMD reduction.

- [ ] **Step 1: Write the SPMD port**

Create `tinybench/spectral-norm/go-spmd/main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"lanes"
	"math"
	"reduce"
	"strconv"
)

var n = 0

type Vec []float64

func evala(i int, j int) int {
	return (i+j)*(i+j+1)/2 + i + 1
}

// evalaVarying mirrors evala but with j varying across SIMD lanes.
// i is uniform; the result is a per-lane int.
func evalaVarying(i int, j lanes.Varying[int]) lanes.Varying[int] {
	s := i + j
	return s*(s+1)/2 + i + 1
}

func times(v, u Vec) {
	for i := 0; i < len(v); i++ {
		var acc lanes.Varying[float64]
		go for j, uj := range u {
			acc += uj / float64(evalaVarying(i, j))
		}
		v[i] = reduce.Add(acc)
	}
}

func times_trans(v, u Vec) {
	for i := 0; i < len(v); i++ {
		var acc lanes.Varying[float64]
		go for j, uj := range u {
			acc += uj / float64(evalaVarying(j, i))
		}
		v[i] = reduce.Add(acc)
	}
}

func a_times_transp(v, u Vec) {
	x := make(Vec, len(u))
	times(x, u)
	times_trans(v, x)
}

func main() {
	flag.Parse()
	if flag.NArg() > 0 {
		n, _ = strconv.Atoi(flag.Arg(0))
	}

	u := make(Vec, n)
	v := make(Vec, n)
	for i := range u {
		u[i] = 1
		v[i] = 1
	}
	for i := 0; i < 10; i++ {
		a_times_transp(v, u)
		a_times_transp(u, v)
	}
	var vBv, vv float64
	for i, vi := range v {
		vBv += u[i] * vi
		vv += vi * vi
	}
	fmt.Printf("%0.9f\n", math.Sqrt(vBv/vv))
}
```

Notes for the implementer:
- Keep the final `vBv`/`vv` reduction loop scalar. Vectorizing it would change reduction order and might cause a last-digit `%0.9f` mismatch versus the reference. Output parity is non-negotiable; if the spec's "expected 2-4x speedup" is missed by leaving this loop scalar, that's fine — it's a tiny pass.
- `evalaVarying` exists because `evala(i, varying j)` would need varying arithmetic, which the compiler accepts only when the function is "SPMD" (has varying parameters). Per the project CLAUDE.md, "Functions with varying parameters are SPMD functions", so we need the dedicated entry point.
- Imports must include `lanes` and `reduce` — the SPMD fork's stdlib provides them when `GOEXPERIMENT=spmd` is set.

- [ ] **Step 2: Manual compile test**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/sn-spmd.bin spectral-norm/go-spmd/main.go
ls -la /tmp/sn-spmd.bin
```

Expected: binary produced. Compile failure → blocker (Step 5b).

- [ ] **Step 3: Manual output parity check (smallest arg)**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/sn-go.bin ./spectral-norm/go/main.go
diff <(/tmp/sn-go.bin 1000) <(/tmp/sn-spmd.bin 1000)
rm /tmp/sn-go.bin /tmp/sn-spmd.bin
```

Expected: no diff. If a last-digit float mismatch shows up, the cause is reduction reassociation (`reduce.Add` is tree reduction; scalar Go is sequential). Mitigation options:
- Split the inner reduction into pieces matching the reference order — generally not feasible idiomatically.
- Accept that this is a blocker per the spec (no output normalization in TestCorrectness).
Choose blocker (Step 5b) if mismatch can't be eliminated by code shape.

- [ ] **Step 4: TestCorrectness gate (full args.txt: 1000, 2500, 5500)**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run 'TestCorrectness/spectral-norm' -timeout=900s
```

Expected: `--- PASS: TestCorrectness/spectral-norm`. The `5500` case is slow — minutes per run × 2 runs.

- [ ] **Step 5a: Commit (success path)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add spectral-norm/go-spmd/main.go
git commit -m "spectral-norm: add vectorized go-spmd port

The hot kernels times() and times_trans() compute v[i] = sum_j
u[j]/evala(...) over vectors of length n (up to 5500). The inner j
loop becomes a 'go for' with a Varying[float64] accumulator and a
reduce.Add at the end of each i iteration.

evala is split into a uniform helper and an evalaVarying that takes
a Varying[int] j (required because functions with varying params are
SPMD functions). Final vBv/vv reduction stays scalar to preserve
sequential reduction order and match %0.9f output bit-for-bit.

Output verified byte-identical to stock go for n=1000, 2500, 5500
via TestCorrectness."
```

- [ ] **Step 5b: Blocker path (only if any step failed)**

Create `tinybench/spectral-norm/go-spmd/BLOCKER.md` using the template from Task 5 Step 5b. Commit:

```bash
cd /home/cedric/work/SPMD/tinybench
git add spectral-norm/go-spmd/main.go spectral-norm/go-spmd/BLOCKER.md
git commit -m "spectral-norm: add vectorized go-spmd port (BLOCKED)

Inner-j SPMD reduction over u[]. Currently blocked — see BLOCKER.md."
```

---

## Task 8: `n-body/go-spmd/main.go` (vectorized inner pair loop)

**Files:**
- Create: `tinybench/n-body/go-spmd/main.go`

Only 5 bodies, so SIMD lane occupancy is poor (4-of-4 down to 1-of-4 across the inner `j` loop on AVX2 4-wide f64). Vectorize honestly anyway — even sub-2x is useful data.

- [ ] **Step 1: Write the SPMD port**

Create `tinybench/n-body/go-spmd/main.go`:

```go
package main

import (
	"fmt"
	"lanes"
	"math"
	"os"
	"reduce"
	"strconv"
)

const (
	pi          = 3.141592653589793
	solarMass   = 4 * pi * pi
	daysPerYear = 365.24
)

type Planet struct {
	x, y, z    float64
	vx, vy, vz float64
	mass       float64
}

func advance(nbodies int, bodies []Planet, dt float64) {
	for i := 0; i < nbodies; i++ {
		b := &bodies[i]
		var dvx, dvy, dvz lanes.Varying[float64]
		go for j := i + 1; j < nbodies; j++ {
			b2 := &bodies[j]
			dx := b.x - b2.x
			dy := b.y - b2.y
			dz := b.z - b2.z
			distanceSquared := dx*dx + dy*dy + dz*dz
			distance := lanes.Sqrt(distanceSquared)
			mag := dt / (distanceSquared * distance)
			dvx += dx * b2.mass * mag
			dvy += dy * b2.mass * mag
			dvz += dz * b2.mass * mag
			// Symmetric back-write to b2 — varying scatter.
			b2.vx += dx * b.mass * mag
			b2.vy += dy * b.mass * mag
			b2.vz += dz * b.mass * mag
		}
		b.vx -= reduce.Add(dvx)
		b.vy -= reduce.Add(dvy)
		b.vz -= reduce.Add(dvz)
	}
	for i := 0; i < nbodies; i++ {
		b := &bodies[i]
		b.x += dt * b.vx
		b.y += dt * b.vy
		b.z += dt * b.vz
	}
}

func energy(nbodies int, bodies []Planet) float64 {
	e := 0.0
	for i := 0; i < nbodies; i++ {
		b := &bodies[i]
		e += 0.5 * b.mass * (b.vx*b.vx + b.vy*b.vy + b.vz*b.vz)
		var ej lanes.Varying[float64]
		go for j := i + 1; j < nbodies; j++ {
			b2 := &bodies[j]
			dx := b.x - b2.x
			dy := b.y - b2.y
			dz := b.z - b2.z
			distance := lanes.Sqrt(dx*dx + dy*dy + dz*dz)
			ej += (b.mass * b2.mass) / distance
		}
		e -= reduce.Add(ej)
	}
	return e
}

func offsetMomentum(nbodies int, bodies []Planet) {
	px, py, pz := 0.0, 0.0, 0.0
	for i := 0; i < nbodies; i++ {
		px += bodies[i].vx * bodies[i].mass
		py += bodies[i].vy * bodies[i].mass
		pz += bodies[i].vz * bodies[i].mass
	}
	bodies[0].vx = -px / solarMass
	bodies[0].vy = -py / solarMass
	bodies[0].vz = -pz / solarMass
}

const nbodies = 5

var bodies = [nbodies]Planet{
	{ // sun
		0, 0, 0, 0, 0, 0, solarMass,
	},
	{ // jupiter
		4.84143144246472090e+00,
		-1.16032004402742839e+00,
		-1.03622044471123109e-01,
		1.66007664274403694e-03 * daysPerYear,
		7.69901118419740425e-03 * daysPerYear,
		-6.90460016972063023e-05 * daysPerYear,
		9.54791938424326609e-04 * solarMass,
	},
	{ // saturn
		8.34336671824457987e+00,
		4.12479856412430479e+00,
		-4.03523417114321381e-01,
		-2.76742510726862411e-03 * daysPerYear,
		4.99852801234917238e-03 * daysPerYear,
		2.30417297573763929e-05 * daysPerYear,
		2.85885980666130812e-04 * solarMass,
	},
	{ // uranus
		1.28943695621391310e+01,
		-1.51111514016986312e+01,
		-2.23307578892655734e-01,
		2.96460137564761618e-03 * daysPerYear,
		2.37847173959480950e-03 * daysPerYear,
		-2.96589568540237556e-05 * daysPerYear,
		4.36624404335156298e-05 * solarMass,
	},
	{ // neptune
		1.53796971148509165e+01,
		-2.59193146099879641e+01,
		1.79258772950371181e-01,
		2.68067772490389322e-03 * daysPerYear,
		1.62824170038242295e-03 * daysPerYear,
		-9.51592254519715870e-05 * daysPerYear,
		5.15138902046611451e-05 * solarMass,
	},
}

func main() {
	n, _ := strconv.Atoi(os.Args[1])
	offsetMomentum(nbodies, bodies[:])
	fmt.Printf("%.9f\n", energy(nbodies, bodies[:]))
	for i := 1; i <= n; i++ {
		advance(nbodies, bodies[:], 0.01)
	}
	fmt.Printf("%.9f\n", energy(nbodies, bodies[:]))
}
```

Notes for the implementer:
- The varying scatter `b2.vx += dx * b.mass * mag` (where `b2` walks varying `j`) is the riskiest pattern. If the SPMD fork rejects it or produces wrong output, this is a blocker — preserve the source and write `BLOCKER.md`. Do NOT downgrade to scalar.
- `lanes.Sqrt(varying float64)` should exist in the lanes package; if it's missing, use `math.Sqrt` with a per-lane decomposition or treat as a blocker.
- Float reassociation in `reduce.Add` may produce a last-digit difference vs scalar `b.vx -= dx * b2.mass * mag` accumulated sequentially. The reference output is `%.9f` — 9 fractional digits — so even tiny reassociation differences can show. If that happens, this is a blocker (no output normalization per spec §6.3).

- [ ] **Step 2: Manual compile test**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nb-spmd.bin n-body/go-spmd/main.go
ls -la /tmp/nb-spmd.bin
```

Expected: binary produced. Failure → blocker.

- [ ] **Step 3: Manual output parity check (smallest arg, fast)**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/nb-go.bin ./n-body/go/main.go
diff <(/tmp/nb-go.bin 50000) <(/tmp/nb-spmd.bin 50000)
rm /tmp/nb-go.bin /tmp/nb-spmd.bin
```

Expected: no diff. If a `%.9f` last-digit mismatch appears, this is a blocker per spec (no output normalization). Document in BLOCKER.md as "float reassociation in reduce.Add diverges from sequential accumulation".

- [ ] **Step 4: TestCorrectness gate (full args.txt: 50000, 100000, 200000)**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run 'TestCorrectness/n-body$' -timeout=300s
```

The trailing `$` anchors the regex so it doesn't also match `n-body-nosqrt`.

Expected: `--- PASS: TestCorrectness/n-body`.

- [ ] **Step 5a: Commit (success path)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body/go-spmd/main.go
git commit -m "n-body: add vectorized go-spmd port

Inner pair loop in advance() and energy() becomes a 'go for' over
j > i with Varying[float64] accumulators reduced to scalar via
reduce.Add at the i boundary. The symmetric back-write to bodies[j]
inside advance is a varying scatter — the highest-risk pattern in
this port.

With only 5 bodies, AVX2 4-wide f64 lane occupancy ranges from 4/4
down to 1/4 across the inner loop, so speedup is modest by design.
Output verified byte-identical to stock go via TestCorrectness."
```

- [ ] **Step 5b: Blocker path (only if any step failed)**

Create `tinybench/n-body/go-spmd/BLOCKER.md` using the Task 5 Step 5b template (substitute `n-body`). Commit:

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body/go-spmd/main.go n-body/go-spmd/BLOCKER.md
git commit -m "n-body: add vectorized go-spmd port (BLOCKED)

Inner pair loop with varying scatter into bodies[j]. Currently
blocked — see BLOCKER.md."
```

---

## Task 9: `n-body-nosqrt/go-spmd/main.go` (vectorized + varying sqrt_newton)

**Files:**
- Create: `tinybench/n-body-nosqrt/go-spmd/main.go`

Same shape as `n-body`, but the math.Sqrt call is replaced by a Newton-iteration `sqrt_newton`. In the SPMD port that becomes a varying-scalar loop with a varying break condition.

- [ ] **Step 1: Write the SPMD port**

Create `tinybench/n-body-nosqrt/go-spmd/main.go`. Copy the body of Task 8's main.go and:
- Replace `lanes.Sqrt(...)` calls with `sqrtNewtonVarying(..., tol)`.
- Add `tol = 1e-5` to the `const` block.
- Add a varying `sqrtNewtonVarying` function:

```go
const (
	pi          = 3.141592653589793
	solarMass   = 4 * pi * pi
	daysPerYear = 365.24
	tol         = 1e-5
)

// sqrtNewtonVarying is the SPMD analogue of sqrt_newton: each lane
// converges independently under a varying mask. Negative inputs are
// not expected here (distanceSquared is non-negative); we mirror the
// scalar guard for parity.
func sqrtNewtonVarying(a lanes.Varying[float64], tol float64) lanes.Varying[float64] {
	x := a // initial guess
	for {
		delta := (x*x - a) / (2 * x)
		x -= delta
		if reduce.All(lanes.Abs(delta) <= tol) {
			break
		}
	}
	return x
}
```

Then in `advance`:

```go
distance := sqrtNewtonVarying(distanceSquared, tol)
```

And in `energy`:

```go
distance := sqrtNewtonVarying(dx*dx+dy*dy+dz*dz, tol)
```

Use `reduce.All` so the loop exits only when all active lanes have converged — matching the scalar single-lane `if math.Abs(delta) <= tol { break }` semantics, just delayed across lanes. (Per CLAUDE.md the SPMD fork supports this via break-mask predication.)

The full file structure mirrors n-body. Reproduce the rest verbatim from Task 8's main.go (Planet struct, bodies array, offsetMomentum, main, etc.).

- [ ] **Step 2: Manual compile test**

```bash
cd /home/cedric/work/SPMD/tinybench
PATH=$(pwd)/../go/bin:$PATH GOEXPERIMENT=spmd \
  ../tinygo/build/tinygo build -opt=2 -llvm-features=+avx2 \
  -o /tmp/nbns-spmd.bin n-body-nosqrt/go-spmd/main.go
ls -la /tmp/nbns-spmd.bin
```

Expected: binary produced. Failure → blocker.

- [ ] **Step 3: Manual output parity check**

```bash
cd /home/cedric/work/SPMD/tinybench
go build -o /tmp/nbns-go.bin ./n-body-nosqrt/go/main.go
diff <(/tmp/nbns-go.bin 50000) <(/tmp/nbns-spmd.bin 50000)
rm /tmp/nbns-go.bin /tmp/nbns-spmd.bin
```

Expected: no diff. The Newton iteration converges deterministically per-lane to the same value the scalar version produces (same starting guess, same tolerance), so output should match — but if `reduce.Add` reassociation differs, that's the blocker case.

- [ ] **Step 4: TestCorrectness gate**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run 'TestCorrectness/n-body-nosqrt' -timeout=600s
```

Expected: `--- PASS`.

- [ ] **Step 5a: Commit (success path)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body-nosqrt/go-spmd/main.go
git commit -m "n-body-nosqrt: add vectorized go-spmd port

Same vectorization shape as n-body (inner-j 'go for' with reduction
back to scalar at the i boundary), with sqrt_newton replaced by a
varying sqrtNewtonVarying that converges each lane independently
and exits when all active lanes have reached tolerance via reduce.All.

This exercises the SPMD fork's break-mask predication of varying
control flow. Output verified byte-identical to stock go."
```

- [ ] **Step 5b: Blocker path (only if any step failed)**

Create BLOCKER.md per Task 5 Step 5b template. Commit:

```bash
cd /home/cedric/work/SPMD/tinybench
git add n-body-nosqrt/go-spmd/main.go n-body-nosqrt/go-spmd/BLOCKER.md
git commit -m "n-body-nosqrt: add vectorized go-spmd port (BLOCKED)

Same shape as n-body plus varying sqrtNewtonVarying loop. Currently
blocked — see BLOCKER.md."
```

---

## Task 10: README documentation

**Files:**
- Modify: `tinybench/README.md`

- [ ] **Step 1: Add the "go-spmd variant" section**

Open `tinybench/README.md`. After the "Compilers" section (which ends around line 36) and before the "Run Benchmarks" section (around line 39), insert:

```markdown
## go-spmd variant

The `go-spmd` rows compare a 7th compiler: the SPMD fork of TinyGo
(https://github.com/Bluebugs/tinygo, `spmd` branch) targeting native
x86-64 with AVX2. Each benchmark has a `<benchmark>/go-spmd/main.go`
port that uses `lanes.Varying[T]`, `go for`, and the `lanes` and
`reduce` packages from the SPMD experiment to vectorize hot loops.
Where an algorithm has no data parallelism (e.g. `fannkuch-redux`,
`fasta`'s LCG), the port stays scalar and serves as a parity test
for the SPMD fork on sequential code.

### Toolchain discovery

The harness looks for the SPMD toolchain in this order:

1. Env vars: `TINYGO_SPMD` (path to the forked tinygo binary) and
   `GOROOT_SPMD` (path to the forked Go root).
2. Submodule fallback: `../tinygo/build/tinygo` and `../go`,
   relative to this directory. This is the zero-config layout when
   tinybench is checked out as a submodule of
   https://github.com/Bluebugs/SPMD.

If neither is found, `go-spmd` rows are skipped (the rest of the
benchmark proceeds normally).

### Output parity

`go test -v -run TestCorrectness` compiles each benchmark with both
stock `go` and `go-spmd`, runs both with every args case, and fails
on any byte-level output mismatch. Run this before trusting
`go test -v -bench=.` numbers.

### Disabled ports

A `<benchmark>/go-spmd/BLOCKER.md` file marks a port as disabled
because of a known compiler issue. The harness skips disabled ports
without aborting other benchmarks. If any blockers exist, they are
aggregated in the top-level `BLOCKERS.md`. The SPMD source is
preserved in place as a regression test for future fork work — to
re-enable, just delete `BLOCKER.md`.
```

- [ ] **Step 2: Add addendum to "Add a benchmark"**

At the end of the "Add a benchmark" section (after step 3, around line 299 in the current README), append:

```markdown

#### go-spmd variant for a new benchmark

A new benchmark with a `<benchmark>/go-spmd/main.go` is picked up
automatically — same `args.txt` cases apply, no harness changes
required. The port should produce byte-identical output to
`<benchmark>/go/main.go` (gated by `TestCorrectness`).
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinybench
git add README.md
git commit -m "docs: document the go-spmd variant

Adds a 'go-spmd variant' section explaining the new compiler row
(SPMD fork of TinyGo, AVX2-only, native x86-64), how the toolchain
is discovered (env vars or submodule fallback), how output parity
is enforced via TestCorrectness, and how disabled ports work via
BLOCKER.md.

Adds a short addendum to 'Add a benchmark' noting that a new
go-spmd/main.go is recognized automatically."
```

---

## Task 11: Final verification and conditional `BLOCKERS.md`

**Files:**
- Create (conditional): `tinybench/BLOCKERS.md`

- [ ] **Step 1: Run the full TestCorrectness sweep**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -run TestCorrectness -timeout=1800s
```

Expected outcomes per benchmark:
- PASS — port works, output identical
- SKIP with `go-spmd blocked: ...` — port has BLOCKER.md (Task 5b/6b/7b/8b/9b path)

A FAIL at this point indicates the port "passed" earlier per-benchmark validation but now diverges — investigate, downgrade to blocker if necessary.

- [ ] **Step 2: Spot-check a benchmark run**

```bash
cd /home/cedric/work/SPMD/tinybench
go test -v -bench='BenchmarkAll/spectral-norm:args=1000' -benchtime=1x -run=^$ -timeout=600s 2>&1 | tail -20
```

Expected: ns/op lines for every compiler row including `tinygo-spmd` (or "BLOCKED" if spectral-norm has a BLOCKER.md). No runtime errors.

- [ ] **Step 3: Aggregate blockers (only if any BLOCKER.md exists)**

Check whether any blockers exist:

```bash
cd /home/cedric/work/SPMD/tinybench
find . -name BLOCKER.md -not -path './.*'
```

If the find returns nothing → skip Steps 4–5 and go to Step 6 (no blockers to aggregate, no `BLOCKERS.md` to create — its absence is the all-clear per spec §7.3).

- [ ] **Step 4: Generate `BLOCKERS.md` (only if Step 3 found files)**

For each `<bench>/go-spmd/BLOCKER.md` listed by Step 3, create `tinybench/BLOCKERS.md`:

```markdown
# go-spmd Blockers

Benchmarks where the SPMD port is currently disabled because of a
compiler issue in the SPMD fork. Each entry links to the
per-benchmark BLOCKER.md with full details. Re-enable by deleting
the per-benchmark BLOCKER.md.

## <benchmark name>

- **Pattern:** <one-line copied from per-benchmark BLOCKER.md>
- **Symptom:** <one-line copied from per-benchmark BLOCKER.md>
- **Details:** [<benchmark>/go-spmd/BLOCKER.md](<benchmark>/go-spmd/BLOCKER.md)

(... one section per blocked benchmark ...)
```

- [ ] **Step 5: Commit `BLOCKERS.md` (only if Step 4 created it)**

```bash
cd /home/cedric/work/SPMD/tinybench
git add BLOCKERS.md
git commit -m "docs: aggregate go-spmd blockers in top-level BLOCKERS.md

One section per disabled port, linking to the per-benchmark
BLOCKER.md with the full failure detail and reproducer."
```

- [ ] **Step 6: Final report**

Surface back to the parent session:
- How many of the five benchmarks are working go-spmd ports (committed without BLOCKER.md)?
- How many are blocked (committed with BLOCKER.md)?
- If any are blocked, point to `tinybench/BLOCKERS.md` for the aggregated summary.
- The tinybench commits live in the submodule — list them so the parent SPMD repo can bump its submodule pointer in a follow-up commit if desired.

```bash
cd /home/cedric/work/SPMD/tinybench
git log --oneline main..HEAD
```

Expected: 6 to 11 commits depending on path (Task 1, 2, 3, 4 = 4 commits guaranteed; Tasks 5–9 = 5 commits; Task 10 = 1 commit; Task 11 BLOCKERS.md commit conditional).

---

## Self-Review

**1. Spec coverage:**
- Spec §1 (overview, invariants, non-goals): captured in plan header and Task 1's Step 4 smoke test (proves backward-compat invariant).
- Spec §2 (layout): file table at top of plan covers it; Tasks 5–9 create each `<bench>/go-spmd/main.go`.
- Spec §3.1 (struct): Task 1 Step 1.
- Spec §3.2 (invocation): Task 1 Step 2 (then refactored in Task 3 Step 1 into `compileWith`).
- Spec §3.3 (Available check): Task 1 Step 3.
- Spec §3.4 (compiler entry): Task 2 Step 3.
- Spec §3.5 (build flags): Task 2 Step 1.
- Spec §4 (toolchain helpers): Task 2 Step 2.
- Spec §5.1–5.3 (per-benchmark ports): Tasks 5–9.
- Spec §5.4 (blocker policy): Task 4 (BLOCKER.md skip in BenchmarkAll), Tasks 5b/6b/7b/8b/9b (blocker template), Task 11 Steps 3–5 (aggregation).
- Spec §6 (correctness test): Task 3.
- Spec §7 (docs): Task 10.
- Spec §8.2 (skip behavior matrix): exercised in Task 3 Step 4 (no-port skip), Task 4 Step 3 (blocker skip), Task 1 Step 4 (toolchain present), Task 2 Step 5 (toolchain absent → entry-level skip).

No gaps.

**2. Placeholder scan:** no TBD/TODO/FIXME/XXX. Every code block is complete. The blocker BLOCKER.md template uses literal angle brackets (`<paste exact error here>`) as placeholders for the engineer to fill in *during* a blocker investigation; that's the document's purpose, not a plan placeholder.

**3. Type consistency:**
- `Compiler.BinaryPath`, `Env`, `Available` declared in Task 1 Step 1, used in Task 1 Steps 2/3, Task 2 Step 3, Task 3 Steps 1/2.
- `compileWith(c *Compiler, testname string) ([]byte, error)` declared in Task 3 Step 1, used in Task 3 Step 2.
- `mustRun`, `readArgs`, `fileExists`, `probeVersion` declared in Task 3 Step 2; `fileExists` reused in Task 4 Step 1.
- `spmdAvailable`, `spmdEnv`, `spmdVersionCmd`, `spmdCompilerPath` declared in Task 2 Step 2, used in Task 2 Step 3.
- Compiler entry `Compiler: "tinygo-spmd"` matches the lookup in Task 3 Step 2 (`case "tinygo-spmd"`).
- Output binary name `./go-spmd.bin` matches `goSpmdBaseFlags`'s `-o=go-spmd.bin`.

Consistent.
