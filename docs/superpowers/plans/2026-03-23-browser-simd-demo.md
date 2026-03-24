# Browser SIMD Detection Demo — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a self-contained HTML demo that detects browser SIMD support, loads the appropriate WASM binary (SIMD or scalar), runs it, and displays results with timing comparison.

**Architecture:** Single HTML file with inline JS. No build tools, no framework, no server required — just open in a browser. Uses a minimal WASI shim for `fd_write` (stdout capture), `clock_time_get`, `proc_exit`, and stubs for `args_*`/`random_get`. Pre-compiled WASM binaries for mandelbrot (visual + timing) and hex-encode (best speedup).

**Tech Stack:** HTML, vanilla JS, WebAssembly, WASI preview1 shim

---

## Design

### SIMD Detection

The standard approach (already sketched in `docs/poc-testing-workflow.md`):

```js
const supportsSimd = WebAssembly.validate(new Uint8Array([
    0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
    0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b,
]));
```

This validates a minimal WASM module that declares a `v128` return type. Browsers without SIMD support reject it.

### WASI Shim

Our WASM binaries import 6 WASI functions:
- `fd_write(fd, iovs, iovs_len, nwritten)` → capture stdout to JS string
- `proc_exit(code)` → throw to stop execution
- `clock_time_get(id, precision, time)` → `performance.now()` in nanoseconds
- `args_sizes_get(argc, argv_buf_size)` → return 0 args
- `args_get(argv, argv_buf)` → no-op
- `random_get(buf, len)` → `crypto.getRandomValues`

Plus asyncify stubs (4 no-ops) for TinyGo scheduler compatibility.

### Demo Content

**Mandelbrot** — visually compelling, computes a 256×256 pixel grid. The output includes serial vs SPMD timing and a pixel-by-pixel verification. Shows `3.18x` speedup in SIMD mode vs `1.0x` in scalar.

**Hex-encode** — best speedup numbers (8.9x dst). Output includes correctness check and timing comparison.

### File Structure

```
demo/
  index.html              — single-file demo (inline JS + CSS)
  mandelbrot-simd.wasm    — pre-compiled SIMD binary
  mandelbrot-scalar.wasm  — pre-compiled scalar binary
  build-demo.sh           — script to compile the WASM binaries
```

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `demo/index.html` | Create | Self-contained browser demo |
| `demo/build-demo.sh` | Create | Compiles WASM binaries for the demo |
| `CLAUDE.md` | Modify | Update Phase 3 status |

---

## Task 1: Build script for demo WASM binaries

**Files:**
- Create: `demo/build-demo.sh`

### Step 1: Create the build script

```bash
#!/usr/bin/env bash
# Build WASM binaries for the browser SIMD demo
set -euo pipefail

SPMD_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WASMOPT="${WASMOPT:-/tmp/wasm-opt}"
OUTDIR="$(dirname "$0")"
TINYGO="$SPMD_ROOT/tinygo/build/tinygo"

compile() {
    local src="$1" out="$2" extra="${3:-}"
    WASMOPT="$WASMOPT" GOEXPERIMENT=spmd GOROOT="$SPMD_ROOT/go" \
        PATH="$SPMD_ROOT/go/bin:$PATH" \
        "$TINYGO" build -target=wasi -scheduler=none $extra -o "$out" "$src" 2>&1
}

echo "Building mandelbrot (SIMD)..."
compile "$SPMD_ROOT/test/integration/spmd/mandelbrot/main.go" \
    "$OUTDIR/mandelbrot-simd.wasm"

echo "Building mandelbrot (scalar)..."
compile "$SPMD_ROOT/test/integration/spmd/mandelbrot/main.go" \
    "$OUTDIR/mandelbrot-scalar.wasm" "-simd=false"

echo "Done. Files:"
ls -lh "$OUTDIR"/*.wasm
```

- [ ] **Step 1a:** Create `demo/build-demo.sh`
- [ ] **Step 1b:** Run: `bash demo/build-demo.sh`
- [ ] **Step 1c:** Verify both `.wasm` files are created

### Step 2: Commit

```
feat: add build script for browser SIMD demo WASM binaries
```

---

## Task 2: Create the browser demo

**Files:**
- Create: `demo/index.html`

### Step 1: Create the HTML demo

The demo should be a single self-contained HTML file with:

1. **Header**: "Go SPMD — Browser SIMD Demo"
2. **Detection panel**: Shows whether browser supports WASM SIMD128
3. **Run button**: Loads and runs the appropriate WASM binary
4. **Output panel**: Shows captured stdout (program output)
5. **Timing panel**: Highlights the SPMD speedup number
6. **Comparison**: Option to run both modes and compare

Key implementation details:

**WASI shim** — minimal implementation capturing fd_write stdout:

```js
function createWasiImports(memory, stdout) {
    return {
        wasi_snapshot_preview1: {
            fd_write(fd, iovs, iovs_len, nwritten_ptr) {
                const view = new DataView(memory.buffer);
                let written = 0;
                for (let i = 0; i < iovs_len; i++) {
                    const ptr = view.getUint32(iovs + i * 8, true);
                    const len = view.getUint32(iovs + i * 8 + 4, true);
                    const bytes = new Uint8Array(memory.buffer, ptr, len);
                    stdout.push(new TextDecoder().decode(bytes));
                    written += len;
                }
                view.setUint32(nwritten_ptr, written, true);
                return 0;
            },
            proc_exit(code) { throw new WasiExit(code); },
            clock_time_get(id, precision_lo, precision_hi, time_ptr) {
                const view = new DataView(memory.buffer);
                const ns = BigInt(Math.round(performance.now() * 1e6));
                view.setBigUint64(time_ptr, ns, true);
                return 0;
            },
            args_sizes_get(argc_ptr, argv_buf_size_ptr) {
                const view = new DataView(memory.buffer);
                view.setUint32(argc_ptr, 0, true);
                view.setUint32(argv_buf_size_ptr, 0, true);
                return 0;
            },
            args_get() { return 0; },
            random_get(buf, len) {
                const view = new Uint8Array(memory.buffer, buf, len);
                crypto.getRandomValues(view);
                return 0;
            },
        },
        asyncify: {
            start_unwind() {}, stop_unwind() {},
            start_rewind() {}, stop_rewind() {},
        },
    };
}
```

**SIMD detection**:

```js
function detectSimd() {
    try {
        return WebAssembly.validate(new Uint8Array([
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
            0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b,
        ]));
    } catch { return false; }
}
```

**Run logic**:

```js
async function runDemo(mode) {
    const file = mode === 'auto'
        ? (detectSimd() ? 'mandelbrot-simd.wasm' : 'mandelbrot-scalar.wasm')
        : `mandelbrot-${mode}.wasm`;

    const stdout = [];
    const response = await fetch(file);
    const bytes = await response.arrayBuffer();
    const module = await WebAssembly.compile(bytes);

    const memory = new WebAssembly.Memory({ initial: 16 });
    // TinyGo manages its own memory — find memory from exports
    const imports = createWasiImports(memory, stdout);
    const instance = await WebAssembly.instantiate(module, imports);

    // TinyGo exports its own memory
    const actualMemory = instance.exports.memory;
    // Re-bind the shim to use exported memory
    // (handled by using instance.exports.memory in fd_write)

    try { instance.exports._start(); }
    catch (e) { if (!(e instanceof WasiExit)) throw e; }

    return stdout.join('');
}
```

Note: TinyGo exports `memory` from the WASM module. The WASI imports need to use `instance.exports.memory.buffer` not a pre-created `Memory`. Handle this by creating a lazy reference.

**UI**: Clean, minimal. No frameworks. Show:
- Browser SIMD status (green/red indicator)
- "Run SIMD" / "Run Scalar" / "Run Both" buttons
- Stdout output in a `<pre>` block
- Extracted speedup number highlighted

- [ ] **Step 1a:** Create `demo/index.html`

### Step 2: Test locally

```bash
cd demo && python3 -m http.server 8080
# Open http://localhost:8080 in browser
```

Verify:
- SIMD detection shows correct status
- "Run SIMD" runs mandelbrot-simd.wasm, shows output including "SPMD speedup: ~3.x"
- "Run Scalar" runs mandelbrot-scalar.wasm, shows "SPMD speedup: ~1.0x"
- "Run Both" runs both and shows comparison
- Works in Chrome, Firefox, Safari (all support WASM SIMD)

- [ ] **Step 2a:** Test in browser
- [ ] **Step 2b:** Verify SIMD and scalar modes produce correct output

### Step 3: Commit

```
feat: add browser SIMD detection demo

Self-contained HTML demo that detects WebAssembly SIMD support,
loads appropriate WASM binary (mandelbrot SIMD or scalar), runs
it with a minimal WASI shim, and displays results with timing.
```

---

## Risk Notes

- **Memory binding**: TinyGo WASM exports its own memory. The WASI shim must reference `instance.exports.memory.buffer` (not a pre-allocated Memory). This requires either a lazy proxy or post-instantiation rebinding.

- **clock_time_get precision**: `performance.now()` in browsers may be limited to 1ms resolution (Spectre mitigations). The mandelbrot benchmark runs long enough (~2-6ms) that this is fine, but sub-millisecond timings would be unreliable.

- **CORS**: Fetching `.wasm` files requires serving from an HTTP server (not `file://`). The `python3 -m http.server` approach works for local testing.

- **No .wasm files in git**: The compiled binaries should NOT be committed to git (they're 500KB+ each). The `build-demo.sh` script regenerates them. Add `demo/*.wasm` to `.gitignore`.
