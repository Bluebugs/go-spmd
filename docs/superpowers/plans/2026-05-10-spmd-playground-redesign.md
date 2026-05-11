# SPMD Playground Redesign

**Status**: Draft
**Date**: 2026-05-10
**Owner**: cedric

## Goal

Repurpose `playground/` from the TinyGo device simulator into an SPMD experiment showcase:

1. Edit Go SPMD source in the browser.
2. **Run** it as WASI in a worker (existing capability, retained).
3. Inspect **WASM SIMD128** disassembly (`.wat`).
4. Inspect **x86-64 AVX2** disassembly (`objdump -M intel`).
5. Toggle `simd=on|off` to A/B against scalar fallback.
6. Pick from curated SPMD examples (lo-contains, hex-encode, base64-mula-lemire, mandelbrot, cross-lane rotate, table lookup, saxpy, simple-sum, lo-sum).

Deployed at `spmd.ddlm.me`. Backend container ships the forked Go + forked TinyGo + `wabt` (`wasm2wat`) + `binutils` (`objdump`).

## Non-goals

- Device simulation, SVG drag-and-drop, boards, parts — delete entirely.
- Multi-file projects — single-file `main.go` only.
- LLVM IR view — pre-opt IR is misleading; full opt IR is huge and impractical. Skip.
- Persistence / shareable URLs — defer to a follow-up.

## Pipeline summary (validated by probe 2026-05-10)

```
Source ──┬─▶ tinygo build -target=wasi -simd=$S -o tmp.wasm        ──▶ wasm in worker  [Run]
         ├─▶ (same artifact) wasm2wat tmp.wasm                     ──▶ .wat text      [WAT]
         └─▶ tinygo build -llvm-features=+ssse3,+sse4.2,+avx2 \
                         -simd=$S -o tmp.elf
             objdump -d -M intel --no-show-raw-insn \
                     --disassemble=<symbol> tmp.elf                ──▶ .s text        [AVX2]
```

Probe confirmed: `linux-amd64-spmd.json` target fails to link (musl/boehm). Default native target + `-llvm-features` is the supported path (matches `test/e2e/spmd-benchmark-x86.sh`). Symbols emerge as `main.<FuncName>`; `--disassemble=` accepts the Go symbol form.

## Backend changes — `playground/`

### `compiler.go`

Extend `compilerJob`:

```go
type compilerJob struct {
    // ... existing fields ...
    SIMD     bool     // new: -simd=true|false
    Symbols  []string // new: function symbols to extract for asm-avx2; empty = whole .text
}
```

Add cases to `Run()`'s `switch job.Format`:

- `"wasi"` (existing): unchanged except thread `-simd=<bool>`.
- `"wat"`: invoke tinygo to produce `.wasm`, then `wasm2wat tmp.wasm > tmp.wat`. Cache the `.wat`.
- `"asm-avx2"`: invoke tinygo with `-llvm-features="+ssse3,+sse4.2,+avx2"` to produce `.elf`, then for each `Symbols[i]` run `objdump -d -M intel --no-show-raw-insn --disassemble=<sym> tmp.elf` and concatenate with a `// ===== <sym> =====` separator. Cache the result.

Cache key (file basename) becomes `sha256(source)-<compiler>-<target>-<format>-simd<bool>.<ext>`. The existing `SourceHash` only covers source; expand the cache filename construction.

### `main.go`

New HTTP routes:

- `GET /api/run?simd=<bool>` — existing flow, parameterised by SIMD.
- `GET /api/wat?simd=<bool>` — returns `text/plain` `.wat`.
- `GET /api/asm?simd=<bool>` — returns `text/plain` x86-64 AVX2 disasm. Symbols selected by example metadata (sent as POST body or query).
- `GET /api/examples` — returns curated example list (JSON).

All routes accept `POST` source body (consistent with current `/api/compile`).

### `tinygo-template/`

Replace heavy device-driver `go.mod` with minimal SPMD module:

```
module playground
go 1.22.0
```

No `require` block — SPMD examples only depend on `lanes` and `reduce` (compiler-provided), plus stdlib.

### `Dockerfile`

Add to the base image:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends \
    wabt binutils && rm -rf /var/lib/apt/lists/*
```

Image must include the forked Go + forked TinyGo built into `/usr/local/go-spmd` and `/usr/local/tinygo-spmd`. Update `PATH` accordingly. Verify `wasm2wat --version` and `objdump --version` in the container entrypoint.

## Frontend changes — `playground/`

### Delete

`simulator.js`, `simulator.css`, `simulator-bootstrap.css`, `simulator-vscode.css`, `boards.js`, `parts/`, `stats/`, `worker/simulator-*`, `dashboard.css` device parts. Keep `worker/runner.js` (WASI shim) and the Monaco editor under `editor/`.

### Replace `index.html`

```
┌─────────────────────────────────────────────────────────────┐
│ SPMD Playground  [Example ▾ hex-encode] [SIMD ☑on] [▶ Run]  │
├──────────────────────────────┬──────────────────────────────┤
│                              │ [Run] [WAT] [AVX2]           │
│        Monaco editor         │ ┌──────────────────────────┐ │
│        (Go syntax)           │ │ output / .wat / asm      │ │
│                              │ │ (highlighted)            │ │
│                              │ │                          │ │
│                              │ └──────────────────────────┘ │
│                              │ Hint: watch for `vpshufb`    │
└──────────────────────────────┴──────────────────────────────┘
```

### `dashboard.js`

State: `{ source, simd, activeTab, exampleKey }`. On tab switch / Run press / SIMD toggle, debounced fetch to corresponding endpoint. Tab content cached per `(sourceHash, simd, tab)` client-side too (cheap server-load reduction).

### Highlighter

Plain `<pre><code>` with regex-driven span injection on the returned text:

```js
const TIER1_WAT  = /\b(v128|i8x16|i16x8|i32x4|i64x2|f32x4|f64x2)\.\w+/g;
const TIER2_WAT  = /\b(i8x16\.swizzle|i8x16\.shuffle)\b/g;
const TIER1_X86  = /\bv(mov|p|broadcast|perm|insert|extract|fm)\w*/g;
const TIER2_X86  = /\b(vpshufb|vpermd|vpermps|vpmaddubsw|vpmaddwd|vpblendvb|vmaskmovd|vmaskmovq)\b/g;
```

Tier-2 always wins (apply last). CSS: tier-1 = subtle accent, tier-2 = bright accent + bold. ~80 LOC total.

## Curated examples

Each lives at `playground/examples/spmd/<key>/main.go`. Metadata in `playground/examples/manifest.json`:

```json
[
  { "key": "simple-sum",          "label": "Simple sum",
    "symbols": ["main.main"],
    "hint": "Reduction across lanes. Look for v128.* in WAT, vpaddd in AVX2." },
  { "key": "hex-encode",          "label": "Hex encode (table lookup)",
    "symbols": ["main.Encode"],
    "hint": "Single vpshufb / i8x16.swizzle does the entire nibble→hex map." },
  { "key": "lo-contains",         "label": "lo.Contains (uniform exit)",
    "symbols": ["main.Contains"],
    "hint": "reduce.Any → vtestps + early-exit. ~7x scalar." },
  { "key": "lo-sum",              "label": "lo.Sum (reduction)",
    "symbols": ["main.Sum"],
    "hint": "Classic reduction baseline." },
  { "key": "mandelbrot",          "label": "Mandelbrot (per-lane break)",
    "symbols": ["main.mandelbrot"],
    "hint": "Varying control flow + early-exit when all lanes broken." },
  { "key": "base64-mula-lemire",  "label": "Base64 decode (Mula-Lemire)",
    "symbols": ["main.decodeChunk"],
    "hint": "Cascading byte→i16→i32 → vpmaddubsw, vpmaddwd, vpshufb, vpermd." },
  { "key": "cross-lane-rotate",   "label": "Cross-lane rotate",
    "symbols": ["main.main"],
    "hint": "lanes.Rotate compiles to a single shufflevector." },
  { "key": "table-lookup",        "label": "Vectorized table lookup",
    "symbols": ["main.main"],
    "hint": "[16]byte{…}[varying] → one vpshufb / i8x16.swizzle." },
  { "key": "saxpy",               "label": "SAXPY (practical-vector)",
    "symbols": ["main.saxpy"],
    "hint": "a*x + y, fused multiply-add via vfm*." }
]
```

Source for the first 6 comes from `test/integration/spmd/<key>/main.go` (trimmed to a few dozen lines each — remove benchmarking scaffolding). Last 3 come from the blog posts:

- `cross-lane-rotate` — extracted from `blogs/cross-lane-communication.md`.
- `table-lookup` — extracted from `blogs/writing-spmd-go.md` ("Vectorized table lookup" section).
- `saxpy` — extracted from `blogs/practical-vector.md`.

The actual symbol names depend on each example's top-level function. Determined during phase 3 by compiling each and listing symbols (`objdump -t <elf> | grep main\.`).

## Build phases

### Phase 1 — Backend: new compile formats

**Commits** (`golang-pro` → `code-reviewer` → `clean-commit`):

1. `compiler.go`: add `SIMD`, `Symbols` to `compilerJob`; thread `-simd` flag into existing wasi case.
2. `compiler.go`: add `wat` format (tinygo wasi build + `wasm2wat`).
3. `compiler.go`: add `asm-avx2` format (tinygo native build with `-llvm-features` + `objdump --disassemble`).
4. `main.go`: add `/api/wat`, `/api/asm`, `/api/examples` routes.
5. `examples/manifest.json` + 9 example sources under `examples/spmd/`.
6. `tinygo-template/go.mod`: strip to SPMD-only deps.

**Verification per commit**: `curl -X POST --data-binary @example.go http://localhost:8080/api/wat?simd=true` returns valid wat; `curl … /api/asm?simd=true` returns text containing expected SIMD mnemonics. Specific assertions in `playground/test_endpoints.sh` (new):

```bash
# Smoke each format for hex-encode, with and without simd
for fmt in run wat asm; do
  for simd in true false; do
    curl -fsS -X POST --data-binary @examples/spmd/hex-encode/main.go \
      "http://localhost:8080/api/$fmt?simd=$simd" >/tmp/out.$fmt.$simd
    [ -s /tmp/out.$fmt.$simd ] || { echo "FAIL: empty $fmt simd=$simd"; exit 1; }
  done
done
# Assert SIMD-on AVX2 contains vpshufb; SIMD-off does not
grep -q vpshufb /tmp/out.asm.true || { echo "FAIL: no vpshufb in SIMD-on AVX2"; exit 1; }
! grep -q vpshufb /tmp/out.asm.false || { echo "FAIL: unexpected vpshufb in scalar mode"; exit 1; }
```

### Phase 2 — Frontend rewrite

1. Delete simulator files (single commit, leave runner.js).
2. New `index.html` + `dashboard.js` + `dashboard.css`: editor + tabs + example dropdown + SIMD toggle.
3. Highlighter module (`highlight-wat.js`, `highlight-x86.js`, shared CSS).
4. Wire `/api/examples` → dropdown population.

**Verification per commit**: Manual browser smoke — see "End-to-end validation" below.

### Phase 3 — Docker + deployment

1. Update `Dockerfile`: install `wabt` + `binutils`, **pin binaryen ≥109** (`wasm-opt`), copy forked Go and TinyGo binaries from a builder stage.
2. Update `Makefile` `build` target.
3. Update `netlify.toml` for the new domain (`spmd.ddlm.me`); confirm SPA routing and `/api/*` reverse-proxy headers.
4. `DEPLOYMENT.md` updated with the new compiler container's resource needs.

#### Binaryen version pin

The SPMD TinyGo fork auto-adds `+relaxed-simd` to LLVM features for all WASM builds (`tinygo/compileopts/config.go:92–94`), and the swizzle codegen emits `llvm.wasm.relaxed.swizzle` (`tinygo/compiler/spmd.go:2648–2683`) which lowers to opcode `0xFD 0x100` = `i8x16.relaxed_swizzle` from the Relaxed SIMD proposal.

Final relaxed-simd opcode numbering landed in binaryen **v109** (PR #5712). Debian/Ubuntu jammy ships v105 which knows the `--enable-relaxed-simd` flag but implements an earlier draft of the opcode numbers — wasm-opt v105 chokes with `[parse exception: invalid code after SIMD prefix: 256]` on any example whose SIMD codegen materializes a swizzle (base64-mula-lemire, table-lookup, eventually any pshufb-style table-lookup workload).

Dockerfile must pull binaryen ≥109 from a reliable source (GitHub releases preferred — `wget https://github.com/WebAssembly/binaryen/releases/download/version_119/binaryen-version_119-x86_64-linux.tar.gz`, extract, place on PATH ahead of the apt-installed v105). Same upgrade desirable for `wabt` (≥1.0.36) so the `wasm2wat` path supports the same opcodes — bundle from GitHub releases too.

**Local-dev impact**: developers on jammy/Pop!_OS still hit the v105 limitation when running `test_local.sh`. The smoke harness accepts non-empty error responses, so the failure is cosmetic (Run and WAT tabs show a wasm-opt error message for two examples in SIMD mode). The AVX2 tab works for all 9 examples regardless. Local devs who want full WAT coverage can either (a) install a newer binaryen manually from GitHub releases, or (b) develop against the Docker image (`test_docker.sh`).

**Why not patch TinyGo to default `-relaxed-simd=false`?** That was option A in the wasm-opt investigation and would be a smaller, more portable fix. We deliberately chose B (binaryen pin) because (1) it keeps SPMD's relaxed-simd codegen path exercised in CI, (2) it doesn't touch the TinyGo fork, (3) the playground is the only deployment surface that needs to work for end-users, and end-users hit it via the deployed Docker image, not local builds. If the local-dev friction becomes annoying, A is a quick follow-up.

## End-to-end validation strategy

Two layers — local-native (fast iteration) and local-Docker (production-fidelity). Both runnable from a single script.

### Local-native

`playground/test_local.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# 1. Build forked toolchain (skip if already built).
if [ ! -x ../tinygo/build/tinygo ]; then ( cd .. && make build ); fi

# 2. Start playground server in background.
PATH="$(cd .. && pwd)/go/bin:$(cd .. && pwd)/tinygo/build:$PATH" \
  GOEXPERIMENT=spmd \
  go run . &
PID=$!
trap "kill $PID 2>/dev/null || true" EXIT
sleep 2

# 3. Endpoint smoke — every example × every format × simd on/off.
fail=0
for ex in $(jq -r '.[].key' examples/manifest.json); do
  for fmt in run wat asm; do
    for simd in true false; do
      out=$(mktemp)
      if curl -fsS -X POST --data-binary @examples/spmd/$ex/main.go \
              "http://localhost:8080/api/$fmt?simd=$simd&example=$ex" >"$out" 2>&1; then
        if [ ! -s "$out" ]; then
          echo "FAIL empty: $ex $fmt simd=$simd"; fail=$((fail+1))
        fi
      else
        echo "FAIL request: $ex $fmt simd=$simd"; fail=$((fail+1))
      fi
      rm -f "$out"
    done
  done
done

# 4. Targeted assertions on hex-encode (canonical pshufb showcase).
asm_simd=$(curl -fsS -X POST --data-binary @examples/spmd/hex-encode/main.go \
                "http://localhost:8080/api/asm?simd=true&example=hex-encode")
asm_scalar=$(curl -fsS -X POST --data-binary @examples/spmd/hex-encode/main.go \
                  "http://localhost:8080/api/asm?simd=false&example=hex-encode")
echo "$asm_simd"   | grep -q vpshufb || { echo "FAIL: hex-encode AVX2 lacks vpshufb"; fail=$((fail+1)); }
echo "$asm_scalar" | grep -qv vpshufb || { echo "FAIL: hex-encode scalar has vpshufb"; fail=$((fail+1)); }

# 5. Targeted assertions on base64-mula-lemire.
asm_b64=$(curl -fsS -X POST --data-binary @examples/spmd/base64-mula-lemire/main.go \
               "http://localhost:8080/api/asm?simd=true&example=base64-mula-lemire")
for mnem in vpmaddubsw vpmaddwd vpshufb vpermd; do
  echo "$asm_b64" | grep -q "$mnem" || { echo "FAIL: base64 AVX2 lacks $mnem"; fail=$((fail+1)); }
done

[ $fail -eq 0 ] && echo "OK: all endpoint checks passed"
exit $fail
```

### Local-Docker

`playground/test_docker.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# 1. Build image.
docker build -t spmd-playground:test .

# 2. Run container.
CID=$(docker run -d --rm -p 8080:8080 spmd-playground:test)
trap "docker stop $CID >/dev/null" EXIT

# 3. Wait for readiness (poll /api/examples).
for i in $(seq 1 30); do
  curl -fsS http://localhost:8080/api/examples >/dev/null 2>&1 && break
  sleep 1
done

# 4. Run the same endpoint smoke as test_local.sh (factored to a shared func).
./test_endpoints.sh http://localhost:8080

# 5. Toolchain version assertions inside the container.
docker exec $CID wasm2wat --version
docker exec $CID objdump --version | head -1
docker exec $CID tinygo version | grep -q spmd || { echo "FAIL: wrong tinygo"; exit 1; }

echo "OK: docker validation passed"
```

### UI validation (manual, scripted prompts)

`playground/UI_CHECKLIST.md` — list of manual browser checks. Each step has expected visual outcome:

1. Open `http://localhost:8080`, editor loads with `simple-sum` source.
2. Pick `hex-encode` from dropdown → editor source replaced.
3. Press **Run** → Run tab shows hex-encoded "hello world" stdout.
4. Click **WAT** tab → text appears within ~2s, `i8x16.swizzle` highlighted bright.
5. Click **AVX2** tab → text appears, `vpshufb` highlighted bright, `vmov*`/`vp*` subtle.
6. Toggle **SIMD off**, click **AVX2** again → no `vpshufb`, plain `mov`/`shr` arithmetic visible.
7. Pick `base64-mula-lemire`, switch to **AVX2** → `vpmaddubsw` + `vpmaddwd` + `vpshufb` + `vpermd` all present and highlighted.
8. Pick `mandelbrot`, **Run** → produces ASCII art / iteration count.
9. Pick `cross-lane-rotate`, **WAT** → `i8x16.shuffle` highlighted.
10. Force a compile error (delete a brace), switch any tab → error text appears in the tab pane with no crash.

To partially automate, capture screenshots via Playwright in a follow-up. For now, manual checklist gates each phase-2 commit.

### CI integration

Add a GitHub Actions workflow `.github/workflows/playground.yml`:

- Build the playground Docker image.
- Run `playground/test_docker.sh`.
- On main branch, push image to registry.

This gates merges and catches Docker-only regressions (missing apt packages, PATH issues, file copy mistakes).

## Risk register

| Risk | Mitigation |
|---|---|
| `objdump --disassemble=<sym>` semantics differ across binutils versions | Pin `binutils` version in Dockerfile; `test_docker.sh` asserts `objdump --version`. |
| TinyGo native build flags drift (`-llvm-features`) | Reference `test/e2e/spmd-benchmark-x86.sh` as source of truth; share a constant in `compiler.go` comments. |
| Symbol names change between examples (inlining, name mangling) | Per-example `symbols` metadata; if symbol missing, fall back to full `.text` disasm with a warning banner. |
| Example sources drift from `test/integration/spmd/` over time | Examples live in `playground/examples/spmd/` only; integration tests are the canonical source — copies are intentional snapshots. Document in `playground/examples/README.md`. |
| Docker image bloats with full LLVM tools | Only `wabt` + `binutils` (already small); confirmed no LLVM tools needed. |
| `wasm2wat` output for production-size WASM is huge | Filter to `(func $main.<symbol>` blocks server-side, same as objdump symbol scoping. |

## Deferred items (not in this plan)

- Shareable URLs / source persistence.
- Multi-example side-by-side comparison view.
- Inline performance numbers (would need benchmark execution, scope creep).
- Mobile layout.
- Playwright screenshot automation.
- AVX-512 toggle (target file exists but not wired here).
- ARM NEON disasm tab (post-launch).
- **TinyGo SPMD fork: optional `-relaxed-simd=false` flag**. Currently `+relaxed-simd` is unconditionally added on WASM SPMD builds. Making it opt-in (default off) would remove the binaryen-v109 dependency entirely and unblock local dev on stock distros. See wasm-opt investigation report. Low-cost fix in `tinygo/compileopts/config.go`, `compileopts/options.go`, `main.go`, `compiler/spmd.go:2648`. Tracked as v9+ follow-up.
- **Bundle `wasm-tools` ≥1.0 in playground Docker image** as a future-proof replacement for wabt's `wasm2wat` — wasm-tools tracks new SIMD opcodes (FP16, JS-string-builtins) faster than wabt.
- **Document binaryen version matrix in `docs/poc-testing-workflow.md`** — v105 (jammy) lacks final relaxed-simd opcodes; v109+ (sid, Homebrew, vcpkg) has them.
- **Investigate `+bulk-memory-opt` / `+call-indirect-overlong` "ignoring feature" warnings** from wasm-ld on v105. LLVM-17/18 features wasm-ld asks for unconditionally — confirm no codegen divergence.

## Open questions

1. Should the AVX2 build pin to `-llvm-features="+ssse3,+sse4.2,+avx2"` (matches benchmark) or add `+avx`/`+avx512f`? — Default to benchmark feature set for consistency with documented perf numbers.
2. Should compile errors from `tinygo build` route to a 4xx status code or 200 with body text? — 200 + text; the frontend renders whatever comes back. Matches current playground behavior.
3. Should `/api/examples` serve manifest + source bundles, or just metadata + lazy-load source? — Lazy-load source on dropdown selection. Smaller initial page weight.
