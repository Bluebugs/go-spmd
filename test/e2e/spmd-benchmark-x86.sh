#!/usr/bin/env bash
# SPMD x86-64 Benchmark: SPMD SIMD vs lo generic vs lo/simd AVX2
#
# Compares three implementations on native x86-64:
#   1. lo generic  — samber/lo pure Go generics (gc compiler)
#   2. lo/simd AVX2 — samber/lo experimental SIMD (gc + GOEXPERIMENT=simd)
#   3. SPMD SIMD   — go for compiler (TinyGo + LLVM, SSE/AVX2)
#
# Usage: bash test/e2e/spmd-benchmark-x86.sh
#
# Prerequisites:
#   - Go 1.26+ with GOEXPERIMENT=simd support
#   - TinyGo SPMD fork built (make build)
#   - samber/lo with exp/simd (test/bench/simd/ module)

set -euo pipefail

SPMD_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$SPMD_ROOT"

GOROOT_SPMD="$SPMD_ROOT/go"
TINYGO="$SPMD_ROOT/tinygo/build/tinygo"
OUTDIR="/tmp/spmd-bench-x86"
INTEG="$SPMD_ROOT/test/integration/spmd"
BENCH_DIR="$SPMD_ROOT/test/bench"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
BOLD='\033[1m'
NC='\033[0m'

mkdir -p "$OUTDIR"

printf "\n${BOLD}${BLUE}╔══════════════════════════════════════════════════════════════╗${NC}\n"
printf "${BOLD}${BLUE}║     SPMD x86-64 Benchmark: SPMD vs lo vs lo/simd            ║${NC}\n"
printf "${BOLD}${BLUE}╚══════════════════════════════════════════════════════════════╝${NC}\n\n"

# ========== Step 1: Build SPMD examples for x86-64 ==========
printf "${BOLD}--- Building SPMD examples for x86-64 native ---${NC}\n"

spmd_compile() {
    local src="$1" out="$2"
    PATH="$GOROOT_SPMD/bin:$PATH" GOEXPERIMENT=spmd \
        "$TINYGO" build -llvm-features="+ssse3,+sse4.2,+avx2" -o "$out" "$src" 2>&1
}

declare -A SPMD_BIN
SPMD_FAILS=""
for fn in sum mean min max contains; do
    src="$INTEG/lo-${fn}/main.go"
    out="$OUTDIR/spmd-lo-${fn}"
    if spmd_compile "$src" "$out" >/dev/null 2>&1; then
        SPMD_BIN[$fn]="$out"
        printf "  ${GREEN}✓${NC} lo-${fn}\n"
    else
        SPMD_FAILS="${SPMD_FAILS} lo-${fn}"
        printf "  ${RED}✗${NC} lo-${fn} (compile error)\n"
    fi
done
# clamp: try to compile and run, checking correctness
src="$INTEG/lo-clamp/main.go"
out="$OUTDIR/spmd-lo-clamp"
if spmd_compile "$src" "$out" >/dev/null 2>&1; then
    # Check if it passes correctness
    if "$out" 2>&1 | grep "Correctness: PASS" >/dev/null; then
        SPMD_BIN[clamp]="$out"
        printf "  ${GREEN}✓${NC} lo-clamp\n"
    else
        SPMD_FAILS="${SPMD_FAILS} lo-clamp(wrong)"
        printf "  ${YELLOW}✗${NC} lo-clamp (correctness failure)\n"
    fi
else
    SPMD_FAILS="${SPMD_FAILS} lo-clamp"
    printf "  ${RED}✗${NC} lo-clamp (compile error)\n"
fi
echo ""

# ========== Step 2: Run gc benchmarks ==========
printf "${BOLD}--- Running lo generic benchmarks (gc native) ---${NC}\n"

# Run lo_comparison_test.go (lo generic + scalar)
gc_out=$(cd "$BENCH_DIR" && go test -bench=. -benchtime=3s -count=1 -cpu=1 -timeout=10m 2>&1)
echo "$gc_out" > "$OUTDIR/gc-bench.txt"
printf "  ${GREEN}Done${NC}\n\n"

# ========== Step 3: Run lo/simd benchmarks ==========
printf "${BOLD}--- Running lo/simd AVX2 benchmarks (gc + GOEXPERIMENT=simd) ---${NC}\n"

losimd_out=""
if [ -d "$BENCH_DIR/simd" ]; then
    losimd_out=$(cd "$BENCH_DIR/simd" && GOEXPERIMENT=simd go test -bench=. -benchtime=3s -count=1 -cpu=1 -timeout=10m 2>&1)
    echo "$losimd_out" > "$OUTDIR/losimd-bench.txt"
    printf "  ${GREEN}Done${NC}\n\n"
else
    printf "  ${YELLOW}Skipped — test/bench/simd/ not found${NC}\n\n"
fi

# ========== Step 4: Run SPMD x86-64 benchmarks ==========
printf "${BOLD}--- Running SPMD SIMD benchmarks (TinyGo x86-64 native) ---${NC}\n"

declare -A SPMD_RESULTS
for fn in sum mean min max contains clamp; do
    if [ -n "${SPMD_BIN[$fn]:-}" ]; then
        SPMD_RESULTS[$fn]=$("${SPMD_BIN[$fn]}" 2>&1)
        printf "  ${GREEN}✓${NC} lo-${fn}\n"
    fi
done
echo ""

# ========== Step 5: Extract metrics and build comparison table ==========

# Extract ns/op from Go benchmark output: "BenchmarkSum/lo 12345 273.8 ns/op"
extract_go_bench() {
    local output="$1" pattern="$2"
    echo "$output" | grep "$pattern" | awk '{for(i=1;i<=NF;i++) if($i=="ns/op") print $(i-1)}' | head -1
}

# Extract ns/iter from SPMD output: "SPMD: 171ns/iter"
extract_spmd() {
    local output="$1" label="$2"
    echo "$output" | grep "^${label}:" | grep -oP '[0-9]+(?=ns/iter)' | head -1
}

printf "${BOLD}${BLUE}╔══════════════════════════════════════════════════════════════════════════════╗${NC}\n"
printf "${BOLD}${BLUE}║                    x86-64 Comparison Table (ns/op, lower is better)         ║${NC}\n"
printf "${BOLD}${BLUE}╚══════════════════════════════════════════════════════════════════════════════╝${NC}\n\n"

printf "  ${BOLD}%-12s %12s %12s %12s │ %10s %10s${NC}\n" \
    "Operation" "lo generic" "lo/simd" "SPMD SIMD" "SPMD vs lo" "SPMD vs AVX"
printf "  %-12s %12s %12s %12s │ %10s %10s\n" \
    "────────────" "──────────" "──────────" "──────────" "──────────" "──────────"

compute_speedup() {
    local base="$1" target="$2"
    if [ -n "$base" ] && [ -n "$target" ] && [ "$target" != "0" ] && [ "$target" != "" ]; then
        printf "%.2fx" "$(echo "scale=2; $base / $target" | bc)"
    else
        echo "—"
    fi
}

for fn in sum mean min max contains clamp; do
    # lo generic (from lo_comparison_test.go)
    case $fn in
        sum)      gc_ns=$(extract_go_bench "$gc_out" "BenchmarkSum/lo") ;;
        mean)     gc_ns=$(extract_go_bench "$gc_out" "BenchmarkMean/lo") ;;
        min)      gc_ns=$(extract_go_bench "$gc_out" "BenchmarkMin/lo") ;;
        max)      gc_ns=$(extract_go_bench "$gc_out" "BenchmarkMax/lo") ;;
        contains) gc_ns=$(extract_go_bench "$gc_out" "BenchmarkContains/lo") ;;
        clamp)    gc_ns=$(extract_go_bench "$gc_out" "BenchmarkClamp/lo") ;;
    esac

    # lo/simd AVX2 (from lo_simd_bench_test.go)
    avx_ns=""
    if [ -n "$losimd_out" ]; then
        case $fn in
            sum)      avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkSum/lo-simd") ;;
            mean)     avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkMean/lo-simd") ;;
            min)      avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkMin/lo-simd") ;;
            max)      avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkMax/lo-simd") ;;
            contains) avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkContains/lo-simd-x8") ;;
            clamp)    avx_ns=$(extract_go_bench "$losimd_out" "BenchmarkClamp/lo-simd") ;;
        esac
    fi

    # SPMD SIMD (from TinyGo x86-64 binary)
    spmd_ns=""
    if [ -n "${SPMD_RESULTS[$fn]:-}" ]; then
        spmd_ns=$(extract_spmd "${SPMD_RESULTS[$fn]}" "SPMD")
    fi

    # Format values
    gc_fmt="${gc_ns:+${gc_ns}ns}"
    avx_fmt="${avx_ns:+${avx_ns}ns}"
    spmd_fmt="${spmd_ns:+${spmd_ns}ns}"

    # Speedups
    vs_lo=$(compute_speedup "${gc_ns:-}" "${spmd_ns:-}")
    vs_avx=$(compute_speedup "${avx_ns:-}" "${spmd_ns:-}")

    # Color the SPMD column green if it's the fastest
    spmd_color=""
    if [ -n "$spmd_ns" ] && [ -n "$gc_ns" ]; then
        if [ "$(echo "$spmd_ns < $gc_ns" | bc)" -eq 1 ]; then
            spmd_color="${GREEN}"
        fi
    fi

    printf "  %-12s %12s %12s ${spmd_color}%12s${NC} │ %10s %10s\n" \
        "lo-${fn}" "${gc_fmt:-—}" "${avx_fmt:-—}" "${spmd_fmt:-—}" "$vs_lo" "$vs_avx"
done

echo ""

# ========== Step 6: SPMD detailed output ==========
printf "${BOLD}--- SPMD Detailed Results ---${NC}\n"
for fn in sum mean min max contains clamp; do
    if [ -n "${SPMD_RESULTS[$fn]:-}" ]; then
        printf "  ${BOLD}lo-${fn}:${NC}\n"
        echo "${SPMD_RESULTS[$fn]}" | sed 's/^/    /'
        echo ""
    fi
done

# ========== Summary ==========
printf "${BOLD}${BLUE}=== Notes ===${NC}\n"
printf "  lo generic:  samber/lo pure Go generics, gc compiler (no SIMD)\n"
printf "  lo/simd:     samber/lo exp/simd, gc + GOEXPERIMENT=simd (AVX2 8-wide)\n"
printf "  SPMD SIMD:   go for compiler, TinyGo + LLVM (SSE 4-wide i32)\n"
printf "  Speedup > 1.0x means SPMD is faster.\n"
if [ -n "$SPMD_FAILS" ]; then
    printf "  ${YELLOW}SPMD compile/correctness failures:${SPMD_FAILS}${NC}\n"
fi
echo ""
