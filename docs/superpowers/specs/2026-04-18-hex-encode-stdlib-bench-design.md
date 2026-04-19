# Hex-Encode stdlib benchmark integration — Design

**Date**: 2026-04-18
**Status**: Draft

## Goal

Extend the two benchmark scripts (`test/e2e/spmd-benchmark.sh` for WASM,
`test/e2e/spmd-benchmark-x86.sh` for x86-64 native) with hex-encode throughput
comparisons that include the Go stdlib (`encoding/hex`) as a baseline, covering
all relevant targets: scalar, SSE, SSSE3, AVX2 (x86) and SIMD128/scalar (WASM).

## Motivation

The existing `hex-encode/main.go` integration test benchmarks three paths:
SPMD dst-centric, SPMD src-centric, and an in-module scalar encoder. What's
missing is a comparison against the canonical Go reference — `encoding/hex` —
and per-CPU-feature SPMD builds on native x86. Without that, the hex-encode
numbers aren't directly comparable to the base64 numbers (which do include
both stdlib and SSSE3/AVX2 builds).

## Non-goals

- Multi-size sweep (1KB / 10KB / 100KB / 1MB). The existing `main.go` uses a
  single 1024-byte fixed payload; we keep it that way for simplicity. If
  multi-size hex benches become interesting later, that is a separate spec.
- Changing the SPMD hex-encode algorithm or `main.go` structure.
- Refactoring `main.go` to emit machine-readable multi-size blocks like
  `bench.go` does in base64.

## New file

### `test/integration/spmd/hex-encode/bench-stdlib.go`

Standalone Go file (not part of the SPMD integration test). Builds under both
native gc and TinyGo WASI. No `GOEXPERIMENT=spmd` required.

Contents:

- Same deterministic payload as `main.go`: `byte((i*31 + 17) & 0xFF)`, size 1024 bytes.
- Same iteration shape: `WARMUP_RUNS = 3`, `BENCH_RUNS = 7`, `iterations = 1000` per run.
- Uses `encoding/hex.Encode(dst, src)` as the encode call.
- Reports exactly one line:

  ```
  Stdlib: min=XXXus avg=XXXus max=XXXus
  ```

  Format string matches `fmtDur` from `main.go` (`Nns`, `N.Nus`, `N.NNNms`)
  so the existing `extract_us` helper in the benchmark scripts picks it up
  unchanged.

The file uses `package main` with a `main()` entry point. Build tag: none
(keep it invocable via `go run` natively and buildable via
`tinygo build -target=wasi`).

## `spmd-benchmark.sh` (WASM) changes

Benchmark 1 section ("Hex-Encode"). Add alongside the existing SIMD/scalar compiles:

```bash
# New: stdlib compile (no GOEXPERIMENT, no -simd flag)
WASMOPT="$WASMOPT" GOROOT="$GOROOT_SPMD" \
    "$TINYGO" build -target=wasi -scheduler=none \
    -o "$OUTDIR/hex-stdlib.wasm" \
    "$INTEG/hex-encode/bench-stdlib.go"
stdlib_out=$(run_wasm "$OUTDIR/hex-stdlib.wasm")
```

After the existing "SIMD mode" / "Scalar mode" text dumps, emit a summary
table, using min-time (the most stable of the three stats):

```
Variant       Time (min)   vs stdlib
────────────  ──────────   ──────────
Go stdlib     NNN us       1.00x
SPMD scalar   NNN us       X.XXx
SPMD dst      NNN us       X.XXx
SPMD src      NNN us       X.XXx
```

Time extraction: reuse existing `extract_us` helper. For:

- "SPMD dst" / "SPMD src": grep the SIMD-mode output's `SPMD dst:` / `SPMD src:` lines, pull `min=` value.
- "SPMD scalar": grep the scalar-mode output's `SPMD dst:` line (scalar-mode dst/src are both scalar fallbacks; pick dst for consistency).
- "Go stdlib": grep the stdlib output's `Stdlib:` line.

Ratio column: `stdlib_us / variant_us`, formatted `%.2fx`. Higher = faster.

Binary-size section already iterates over named tests; add `hex-stdlib`
alongside `hex-encode` there for completeness.

## `spmd-benchmark-x86.sh` changes

Add a new "Hex-Encode Comparison" section after the base64 decode section,
mirroring its layout.

### Builds

Four SPMD binaries of `hex-encode/main.go`:

| Label        | Build flags                                        |
|--------------|-----------------------------------------------------|
| scalar       | `-simd=false`                                       |
| SSE          | (no `-llvm-features`, default SSE2)                 |
| SSSE3        | `-llvm-features="+ssse3,+sse4.2"`                   |
| AVX2         | `-llvm-features="+ssse3,+sse4.2,+avx2"`             |

All use `PATH="$GOROOT_SPMD/bin:$PATH" GOEXPERIMENT=spmd`. Each build failure
is reported with `✗` but does not abort the script (same pattern as base64).

One stdlib binary from `bench-stdlib.go`, run via `go run` (not TinyGo),
using the forked Go toolchain for consistency with the rest of the script.

### Output

Two tables (one for dst, one for src), or one combined table with a "Variant"
column. Chose combined to save vertical space:

```
Variant       Go stdlib    SPMD scalar   SPMD SSE    SPMD SSSE3   SPMD AVX2   AVX2/stdlib
────────────  ──────────   ──────────    ──────────  ──────────   ──────────  ──────────
SPMD dst      NNN us       NNN us        NNN us      NNN us       NNN us      X.XXx
SPMD src      NNN us       NNN us        NNN us      NNN us       NNN us      X.XXx
```

Same "min" value used. Color SPMD cells green when faster than stdlib (same
logic as base64 table). `AVX2/stdlib` speedup per-row.

### Notes block

Append after the table (matching base64's style):

```
Notes:
  Go stdlib:   encoding/hex (gc compiler, stdlib, native amd64)
  SPMD scalar: TinyGo + LLVM, -simd=false
  SPMD SSE:    TinyGo + LLVM, default SSE2 (4-wide i32)
  SPMD SSSE3:  TinyGo + LLVM, +ssse3,+sse4.2 (4-wide, pshufb available)
  SPMD AVX2:   TinyGo + LLVM, +avx2 (8-wide i32)
  Throughput measured on 1024-byte fixed payload, min-time of 7 runs × 1000 iters
```

## Risk / edge cases

- `SSE` build without SSSE3 may take the decomposed scatter path; we expect
  it to be the slowest SPMD variant. That's fine — reporting it is the point.
- `hex-stdlib.wasm` compile under TinyGo uses `encoding/hex`, which is small
  and is already TinyGo-compatible (verified in TinyGo's standard smoke tests).
  If it somehow fails, degrade to "skip stdlib row" the same way base64 does.
- `bench-stdlib.go` native run uses the forked Go toolchain. The fork should
  compile `encoding/hex` identically to upstream (no stdlib fork changes
  there); if not, flag and investigate rather than silently falling back.

## Testing

- Run `bash test/e2e/spmd-benchmark.sh` after changes. Expect Benchmark 1
  to show a 4-row summary table with non-empty values.
- Run `bash test/e2e/spmd-benchmark-x86.sh`. Expect a new hex-encode section
  with 2 rows × 5 data columns filled in (unless a specific build fails).
- Sanity-check that `Go stdlib` ≈ `SPMD scalar` order-of-magnitude — both
  are scalar-ish. SPMD scalar may be slower due to TinyGo overhead; that
  is not a regression.
