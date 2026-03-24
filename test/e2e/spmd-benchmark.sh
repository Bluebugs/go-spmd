#!/usr/bin/env bash
# SPMD Benchmark: SIMD vs Scalar comparison
# Compiles key tests in both -simd=true (SIMD128) and -simd=false (scalar) modes,
# runs them with wasmtime (Cranelift) for accurate SIMD performance, and reports
# speedup ratios.
#
# Usage: bash test/e2e/spmd-benchmark.sh

set -euo pipefail

SPMD_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$SPMD_ROOT"

GOROOT_SPMD="$SPMD_ROOT/go"
TINYGO="$SPMD_ROOT/tinygo/build/tinygo"
WASMOPT="${WASMOPT:-/tmp/wasm-opt}"
OUTDIR="/tmp/spmd-bench"
INTEG="$SPMD_ROOT/test/integration/spmd"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

mkdir -p "$OUTDIR"

# Detect runtime
if command -v wasmtime &>/dev/null; then
    RUNTIME="wasmtime"
    run_wasm() { wasmtime run "$1" 2>&1; }
    printf "${GREEN}Using wasmtime (Cranelift) for accurate SIMD benchmarking${NC}\n"
else
    RUNTIME="node"
    run_wasm() { node --experimental-wasi-unstable-preview1 "$SPMD_ROOT/test/e2e/run-wasm.mjs" "$1" 2>&1 | grep -v "ExperimentalWarning\|trace-warnings"; }
    printf "${YELLOW}wasmtime not found, falling back to Node.js (V8) — SIMD speedups may be lower${NC}\n"
fi

compile() {
    local src="$1" out="$2" extra="${3:-}"
    WASMOPT="$WASMOPT" GOEXPERIMENT=spmd GOROOT="$GOROOT_SPMD" \
        "$TINYGO" build -target=wasi $extra -o "$out" "$src" 2>&1
}

# Extract a numeric value from output matching a pattern.
# Usage: extract_metric "output" "pattern_before_number"
extract_metric() {
    echo "$1" | grep -oP "$2\K[0-9]+\.?[0-9]*" | head -1
}

# Extract timing in microseconds from various formats
extract_us() {
    local line="$1"
    # Handle "123.4us" or "123us"
    if echo "$line" | grep -qP '[0-9.]+us'; then
        echo "$line" | grep -oP '[0-9.]+(?=us)' | head -1
    # Handle "1.234ms"
    elif echo "$line" | grep -qP '[0-9.]+ms'; then
        local ms=$(echo "$line" | grep -oP '[0-9.]+(?=ms)' | head -1)
        echo "$ms * 1000" | bc
    # Handle "123ns"
    elif echo "$line" | grep -qP '[0-9.]+ns'; then
        local ns=$(echo "$line" | grep -oP '[0-9.]+(?=ns)' | head -1)
        echo "$ns / 1000" | bc -l
    else
        echo ""
    fi
}

printf "\n${BOLD}${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}\n"
printf "${BOLD}${BLUE}║           SPMD Benchmark: SIMD vs Scalar                    ║${NC}\n"
printf "${BOLD}${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}\n\n"

# ========== Benchmark 1: Hex-Encode ==========
printf "${BOLD}--- Hex-Encode (1024 bytes, 1000 iterations) ---${NC}\n"
compile "$INTEG/hex-encode/main.go" "$OUTDIR/hex-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/hex-encode/main.go" "$OUTDIR/hex-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

simd_out=$(run_wasm "$OUTDIR/hex-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/hex-scalar.wasm")

# Extract SPMD dst and src min times
simd_dst=$(echo "$simd_out" | grep "SPMD (dst" | head -1)
simd_src=$(echo "$simd_out" | grep "SPMD (src" | head -1)
scalar_line=$(echo "$simd_out" | grep "^Scalar:" | head -1)
scalar_scalar_line=$(echo "$scalar_out" | grep "^Scalar:" | head -1)

# For scalar mode, the "SPMD" line IS the scalar fallback
scalar_spmd_dst=$(echo "$scalar_out" | grep "SPMD (dst" | head -1)
scalar_spmd_src=$(echo "$scalar_out" | grep "SPMD (src" | head -1)

printf "  SIMD mode:\n"
echo "$simd_out" | grep -E "^Scalar:|^SPMD" | sed 's/^/    /'
printf "  Scalar mode:\n"
echo "$scalar_out" | grep -E "^Scalar:|^SPMD" | sed 's/^/    /'
printf "  Correctness: %s\n" "$(echo "$simd_out" | grep "Correctness")"
echo ""

# ========== Benchmark 2: Mandelbrot ==========
printf "${BOLD}--- Mandelbrot (256x256, serial vs SPMD) ---${NC}\n"
compile "$INTEG/mandelbrot/main.go" "$OUTDIR/mandel-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/mandelbrot/main.go" "$OUTDIR/mandel-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

simd_out=$(run_wasm "$OUTDIR/mandel-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/mandel-scalar.wasm")

printf "  SIMD mode:\n"
echo "$simd_out" | grep -E "^Serial|^SPMD|^Speedup|^Verification" | sed 's/^/    /'
printf "  Scalar mode:\n"
echo "$scalar_out" | grep -E "^Serial|^SPMD|^Speedup|^Verification" | sed 's/^/    /'
echo ""

# ========== Benchmark 3: lo-* reduction functions ==========
printf "${BOLD}--- Reduction Functions (lo-* suite) ---${NC}\n"
printf "  %-20s %12s %12s %10s\n" "Function" "SIMD" "Scalar" "Speedup"
printf "  %-20s %12s %12s %10s\n" "--------" "----" "------" "-------"

for fn in sum mean min max contains clamp; do
    src="$INTEG/lo-${fn}/main.go"
    [ -f "$src" ] || continue

    compile "$src" "$OUTDIR/lo-${fn}-simd.wasm" "-scheduler=none" >/dev/null 2>&1
    compile "$src" "$OUTDIR/lo-${fn}-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

    simd_out=$(run_wasm "$OUTDIR/lo-${fn}-simd.wasm")
    scalar_out=$(run_wasm "$OUTDIR/lo-${fn}-scalar.wasm")

    # Extract SPMD timing (ns/iter)
    simd_spmd=$(echo "$simd_out" | grep "^SPMD:" | grep -oP '[0-9]+' | head -1)
    scalar_spmd=$(echo "$scalar_out" | grep "^SPMD:" | grep -oP '[0-9]+' | head -1)

    if [ -n "$simd_spmd" ] && [ -n "$scalar_spmd" ] && [ "$simd_spmd" -gt 0 ]; then
        speedup=$(echo "scale=2; $scalar_spmd / $simd_spmd" | bc)
        printf "  %-20s %10sns %10sns %8sx\n" "lo-${fn}" "$simd_spmd" "$scalar_spmd" "$speedup"
    else
        printf "  %-20s %12s %12s %10s\n" "lo-${fn}" "?" "?" "?"
    fi
done
echo ""

# ========== Benchmark 4: Simple-sum (correctness + timing) ==========
printf "${BOLD}--- Simple-Sum (dual-mode correctness) ---${NC}\n"
compile "$INTEG/simple-sum/main.go" "$OUTDIR/sum-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/simple-sum/main.go" "$OUTDIR/sum-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

simd_out=$(run_wasm "$OUTDIR/sum-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/sum-scalar.wasm")
printf "  SIMD:   %s\n" "$simd_out"
printf "  Scalar: %s\n" "$scalar_out"
if [ "$simd_out" = "$scalar_out" ]; then
    printf "  ${GREEN}✓ Outputs match${NC}\n"
else
    printf "  ${RED}✗ Outputs differ${NC}\n"
fi
echo ""

# ========== Benchmark 5: Store-coalescing ==========
printf "${BOLD}--- Store-Coalescing (interleaved stores) ---${NC}\n"
compile "$INTEG/store-coalescing/main.go" "$OUTDIR/store-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/store-coalescing/main.go" "$OUTDIR/store-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

simd_out=$(run_wasm "$OUTDIR/store-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/store-scalar.wasm")
if [ "$simd_out" = "$scalar_out" ]; then
    printf "  ${GREEN}✓ SIMD and scalar outputs match${NC}\n"
else
    printf "  ${RED}✗ Outputs differ${NC}\n"
fi
echo ""

# ========== Binary size comparison ==========
printf "${BOLD}--- Binary Size Comparison ---${NC}\n"
printf "  %-25s %10s %10s %10s\n" "Test" "SIMD" "Scalar" "Ratio"
printf "  %-25s %10s %10s %10s\n" "----" "----" "------" "-----"
for name in hex-encode mandelbrot simple-sum store-coalescing; do
    simd_file="$OUTDIR/${name%%-*}-simd.wasm"
    scalar_file="$OUTDIR/${name%%-*}-scalar.wasm"
    # Use the actual filenames
    case $name in
        hex-encode) simd_file="$OUTDIR/hex-simd.wasm"; scalar_file="$OUTDIR/hex-scalar.wasm" ;;
        mandelbrot) simd_file="$OUTDIR/mandel-simd.wasm"; scalar_file="$OUTDIR/mandel-scalar.wasm" ;;
        simple-sum) simd_file="$OUTDIR/sum-simd.wasm"; scalar_file="$OUTDIR/sum-scalar.wasm" ;;
        store-coalescing) simd_file="$OUTDIR/store-simd.wasm"; scalar_file="$OUTDIR/store-scalar.wasm" ;;
    esac
    if [ -f "$simd_file" ] && [ -f "$scalar_file" ]; then
        simd_size=$(stat -c%s "$simd_file")
        scalar_size=$(stat -c%s "$scalar_file")
        ratio=$(echo "scale=2; $simd_size / $scalar_size" | bc)
        printf "  %-25s %8sKB %8sKB %9sx\n" "$name" "$((simd_size/1024))" "$((scalar_size/1024))" "$ratio"
    fi
done
echo ""

# ========== Summary ==========
printf "${BOLD}${BLUE}=== Summary ===${NC}\n"
printf "Runtime: %s\n" "$RUNTIME"
printf "All benchmarks compiled in both SIMD and scalar modes.\n"
printf "SIMD speedup is measured as scalar_time / simd_time.\n"
printf "Values > 1.0x indicate SIMD is faster.\n"
