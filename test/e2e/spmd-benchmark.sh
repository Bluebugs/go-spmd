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

# Go stdlib baseline (no GOEXPERIMENT, no -simd flag) — runs under the same
# wasmtime runtime for apples-to-apples comparison.
stdlib_hex_ok=true
WASMOPT="$WASMOPT" GOROOT="$GOROOT_SPMD" \
    "$TINYGO" build -target=wasi -scheduler=none \
    -o "$OUTDIR/hex-stdlib.wasm" \
    "$INTEG/hex-encode/bench-stdlib.go" >/dev/null 2>&1 || stdlib_hex_ok=false

simd_out=$(run_wasm "$OUTDIR/hex-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/hex-scalar.wasm")
stdlib_hex_out=""
if $stdlib_hex_ok; then
    stdlib_hex_out=$(run_wasm "$OUTDIR/hex-stdlib.wasm")
fi

printf "  SIMD mode:\n"
echo "$simd_out" | grep -E "^Scalar:|^SPMD" | sed 's/^/    /'
printf "  Scalar mode:\n"
echo "$scalar_out" | grep -E "^Scalar:|^SPMD" | sed 's/^/    /'
if [ -n "$stdlib_hex_out" ]; then
    printf "  Go stdlib:\n"
    echo "$stdlib_hex_out" | grep -E "^Stdlib:" | sed 's/^/    /'
else
    printf "  ${YELLOW}Go stdlib compile failed — skipping stdlib comparison${NC}\n"
fi
printf "  Correctness: %s\n" "$(echo "$simd_out" | grep "Correctness")"
echo ""

# Summary table: min-time across all variants.
# extract_min_us pulls the min=... value from a "Label: min=... avg=... max=..." line
# and normalizes to microseconds.
extract_min_us() {
    local line="$1"
    local tok
    tok=$(echo "$line" | grep -oP 'min=\K[^ ]+' | head -1)
    [ -z "$tok" ] && return
    if echo "$tok" | grep -qP '[0-9.]+us$'; then
        echo "$tok" | grep -oP '[0-9.]+'
    elif echo "$tok" | grep -qP '[0-9.]+ms$'; then
        local ms=$(echo "$tok" | grep -oP '[0-9.]+')
        echo "$ms * 1000" | bc
    elif echo "$tok" | grep -qP '[0-9.]+ns$'; then
        local ns=$(echo "$tok" | grep -oP '[0-9.]+')
        echo "scale=3; $ns / 1000" | bc
    fi
}

stdlib_line=$(echo "$stdlib_hex_out" | grep "^Stdlib:" | head -1)
scalar_dst_line=$(echo "$scalar_out" | grep "SPMD dst:" | head -1)
simd_dst_line=$(echo "$simd_out" | grep "SPMD dst:" | head -1)
simd_src_line=$(echo "$simd_out" | grep "SPMD src:" | head -1)

stdlib_us=$(extract_min_us "$stdlib_line")
scalar_us=$(extract_min_us "$scalar_dst_line")
simd_dst_us=$(extract_min_us "$simd_dst_line")
simd_src_us=$(extract_min_us "$simd_src_line")

hex_speedup() {
    local base="$1" target="$2"
    if [ -n "$base" ] && [ -n "$target" ] && [ "$target" != "0" ]; then
        printf "%.2fx" "$(echo "scale=3; $base / $target" | bc)"
    else
        echo "—"
    fi
}

printf "  ${BOLD}Summary (min of 7 runs, lower is better):${NC}\n"
printf "  %-14s %12s %12s\n" "Variant" "Time" "vs stdlib"
printf "  %-14s %12s %12s\n" "────────────" "──────────" "──────────"
printf "  %-14s %12s %12s\n" "Go stdlib"   "${stdlib_us:+${stdlib_us}us}"   "1.00x"
printf "  %-14s %12s %12s\n" "SPMD scalar" "${scalar_us:+${scalar_us}us}"   "$(hex_speedup "${stdlib_us:-}" "${scalar_us:-}")"
printf "  %-14s %12s %12s\n" "SPMD dst"    "${simd_dst_us:+${simd_dst_us}us}" "$(hex_speedup "${stdlib_us:-}" "${simd_dst_us:-}")"
printf "  %-14s %12s %12s\n" "SPMD src"    "${simd_src_us:+${simd_src_us}us}" "$(hex_speedup "${stdlib_us:-}" "${simd_src_us:-}")"
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

# ========== Benchmark 6: Base64 Mula-Lemire decode ==========
printf "${BOLD}--- Base64 Mula-Lemire Decode (throughput) ---${NC}\n"
compile "$INTEG/base64-mula-lemire/bench.go" "$OUTDIR/b64-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/base64-mula-lemire/bench.go" "$OUTDIR/b64-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1

# Compile Go stdlib reference under the same TinyGo/WASM runtime for apples-to-apples timing.
# bench-stdlib.go uses only encoding/base64, so no GOEXPERIMENT=spmd is needed.
stdlib_compile_ok=true
WASMOPT="$WASMOPT" GOROOT="$GOROOT_SPMD" \
    "$TINYGO" build -target=wasi -scheduler=none -o "$OUTDIR/b64-stdlib.wasm" \
    "$INTEG/base64-mula-lemire/bench-stdlib.go" >/dev/null 2>&1 || stdlib_compile_ok=false

simd_out=$(run_wasm "$OUTDIR/b64-simd.wasm")
scalar_out=$(run_wasm "$OUTDIR/b64-scalar.wasm")
stdlib_out=""
if $stdlib_compile_ok; then
    stdlib_out=$(run_wasm "$OUTDIR/b64-stdlib.wasm")
fi

printf "  SIMD mode (SPMD):\n"
echo "$simd_out" | sed 's/^/    /'
printf "  Scalar mode (SPMD -simd=false):\n"
echo "$scalar_out" | sed 's/^/    /'
if [ -n "$stdlib_out" ]; then
    printf "  Go stdlib (TinyGo, same WASM runtime):\n"
    echo "$stdlib_out" | sed 's/^/    /'
else
    printf "  ${YELLOW}Go stdlib compile failed — skipping stdlib comparison${NC}\n"
fi
echo ""

# Summary table: stdlib vs SPMD scalar vs SPMD SIMD on the same runtime.
# bench.go emits "spmd:   NNN MB/s" and "scalar: NNN MB/s" (internal reference).
# bench-stdlib.go emits "stdlib: NNN MB/s".
# Extract per-size MB/s from each source and build a unified table.
extract_b64_mbps() {
    local output="$1" size_tag="$2" label="$3"
    echo "$output" | awk -v tag="$size_tag" -v lbl="$label" '
        index($0, "[" tag "]") { found=1; next }
        found && $1 == lbl":" { print $2; exit }
    '
}

printf "  ${BOLD}Throughput summary (MB/s on encoded input, higher is better):${NC}\n"
printf "  %-8s %12s %12s %12s %12s %12s\n" \
    "Size" "Go stdlib" "SPMD scalar" "SPMD SIMD" "scalar/std" "SIMD/std"
printf "  %-8s %12s %12s %12s %12s %12s\n" \
    "────────" "──────────" "──────────" "──────────" "──────────" "──────────"
for size_tag in "1KB  " "10KB " "100KB" "1MB  "; do
    stdlib_mb=""
    if [ -n "$stdlib_out" ]; then
        stdlib_mb=$(extract_b64_mbps "$stdlib_out" "$size_tag" "stdlib")
    fi
    scalar_mb=$(extract_b64_mbps "$scalar_out" "$size_tag" "spmd")
    simd_mb=$(extract_b64_mbps "$simd_out" "$size_tag" "spmd")

    scalar_ratio=""
    simd_ratio=""
    if [ -n "$stdlib_mb" ] && [ -n "$scalar_mb" ] && [ "$stdlib_mb" != "0" ]; then
        scalar_ratio=$(printf "%.2fx" "$(echo "scale=2; $scalar_mb / $stdlib_mb" | bc)")
    fi
    if [ -n "$stdlib_mb" ] && [ -n "$simd_mb" ] && [ "$stdlib_mb" != "0" ]; then
        simd_ratio=$(printf "%.2fx" "$(echo "scale=2; $simd_mb / $stdlib_mb" | bc)")
    fi

    printf "  %-8s %12s %12s %12s %12s %12s\n" \
        "$size_tag" \
        "${stdlib_mb:+${stdlib_mb} MB/s}" \
        "${scalar_mb:+${scalar_mb} MB/s}" \
        "${simd_mb:+${simd_mb} MB/s}" \
        "${scalar_ratio:-—}" \
        "${simd_ratio:-—}"
done
printf "  ${BOLD}Note:${NC} Mula-Lemire cascade (byte→i16→i32) is SIMD-tuned.\n"
printf "  In -simd=false mode it degenerates to multi-pass scalar with shadow-stack\n"
printf "  intermediates, so scalar/std is expected to be <1.0x. See docs/notes.\n"
echo ""

# Also verify correctness parity against base64-mula-lemire/main.go (both modes must match).
compile "$INTEG/base64-mula-lemire/main.go" "$OUTDIR/b64-main-simd.wasm" "-scheduler=none" >/dev/null 2>&1
compile "$INTEG/base64-mula-lemire/main.go" "$OUTDIR/b64-main-scalar.wasm" "-scheduler=none -simd=false" >/dev/null 2>&1
simd_ok=$(run_wasm "$OUTDIR/b64-main-simd.wasm")
scalar_ok=$(run_wasm "$OUTDIR/b64-main-scalar.wasm")
if [ "$simd_ok" = "$scalar_ok" ]; then
    printf "  ${GREEN}✓ SIMD and scalar correctness match${NC}\n"
else
    printf "  ${RED}✗ Outputs differ${NC}\n"
fi
echo ""

# ========== Binary size comparison ==========
printf "${BOLD}--- Binary Size Comparison ---${NC}\n"
printf "  %-25s %10s %10s %10s\n" "Test" "SIMD" "Scalar" "Ratio"
printf "  %-25s %10s %10s %10s\n" "----" "----" "------" "-----"
for name in hex-encode hex-stdlib mandelbrot simple-sum store-coalescing base64-mula-lemire; do
    simd_file="$OUTDIR/${name%%-*}-simd.wasm"
    scalar_file="$OUTDIR/${name%%-*}-scalar.wasm"
    # Use the actual filenames
    case $name in
        hex-encode) simd_file="$OUTDIR/hex-simd.wasm"; scalar_file="$OUTDIR/hex-scalar.wasm" ;;
        hex-stdlib) simd_file="$OUTDIR/hex-stdlib.wasm"; scalar_file="$OUTDIR/hex-stdlib.wasm" ;;
        mandelbrot) simd_file="$OUTDIR/mandel-simd.wasm"; scalar_file="$OUTDIR/mandel-scalar.wasm" ;;
        simple-sum) simd_file="$OUTDIR/sum-simd.wasm"; scalar_file="$OUTDIR/sum-scalar.wasm" ;;
        store-coalescing) simd_file="$OUTDIR/store-simd.wasm"; scalar_file="$OUTDIR/store-scalar.wasm" ;;
        base64-mula-lemire) simd_file="$OUTDIR/b64-simd.wasm"; scalar_file="$OUTDIR/b64-scalar.wasm" ;;
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
