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
        "$TINYGO" build -target=wasi -scheduler=none $extra -o "$out" "$src" 2>&1 \
        | grep -v "^'+" || true
}

echo "Building mandelbrot (SIMD)..."
compile "$SPMD_ROOT/test/integration/spmd/mandelbrot/main.go" \
    "$OUTDIR/mandelbrot-simd.wasm"

echo "Building mandelbrot (scalar)..."
compile "$SPMD_ROOT/test/integration/spmd/mandelbrot/main.go" \
    "$OUTDIR/mandelbrot-scalar.wasm" "-simd=false"

echo ""
echo "Done. Files:"
ls -lh "$OUTDIR"/*.wasm
echo ""
echo "To test: cd demo && python3 -m http.server 8080"
echo "Then open http://localhost:8080"
