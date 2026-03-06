#!/bin/bash
# SPMD End-to-End Test Script
# Tests progressive levels of SPMD compilation and execution
set -uo pipefail

SPMD_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TINYGO="$SPMD_ROOT/tinygo/build/tinygo"
GOROOT_SPMD="$SPMD_ROOT/go"
RUNNER="$SPMD_ROOT/test/e2e/run-wasm.mjs"
WASMOPT="${WASMOPT:-/tmp/wasm-opt}"
OUTDIR="/tmp/spmd-e2e"

# Detect WASM runtime: prefer wasmtime over Node.js for better performance and stability.
if command -v wasmtime &>/dev/null; then
    WASM_RUNTIME="wasmtime"
else
    WASM_RUNTIME="node"
fi

mkdir -p "$OUTDIR"

# Counters
TOTAL=0; COMPILE_PASS=0; COMPILE_FAIL=0; RUN_PASS=0; RUN_FAIL=0; REJECT_PASS=0; REJECT_FAIL=0

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; BLUE='\033[0;34m'; NC='\033[0m'

compile() {
    local src="$1" out="$2" extra="${3:-}"
    WASMOPT="$WASMOPT" GOEXPERIMENT=spmd GOROOT="$GOROOT_SPMD" \
        "$TINYGO" build -target=wasi $extra -o "$out" "$src" 2>&1
}

run_wasm() {
    local wasm="$1" export="${2:-}"
    if [ "$WASM_RUNTIME" = "wasmtime" ]; then
        if [ -n "$export" ]; then
            wasmtime run --invoke "$export" "$wasm" 2>&1 | grep -v '^warning: using `--invoke`'
        else
            wasmtime run "$wasm" 2>&1
        fi
    else
        if [ -n "$export" ]; then
            node --experimental-wasi-unstable-preview1 "$RUNNER" "$wasm" --export "$export" 2>&1
        else
            node --experimental-wasi-unstable-preview1 "$RUNNER" "$wasm" 2>&1
        fi
    fi
}

test_compile() {
    local name="$1" src="$2" extra="${3:-}"
    local out="$OUTDIR/${name}.wasm"
    TOTAL=$((TOTAL + 1))
    local result
    if result=$(compile "$src" "$out" "$extra" 2>&1); then
        COMPILE_PASS=$((COMPILE_PASS + 1))
        printf "${GREEN}COMPILE OK${NC}  %-40s %s\n" "$name" ""
        return 0
    else
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        local err=$(echo "$result" | grep -v "^'+\|^$" | head -3)
        printf "${RED}COMPILE FAIL${NC} %-40s %s\n" "$name" "$err"
        return 1
    fi
}

test_compile_and_run() {
    local name="$1" src="$2" expected="${3:-}" export="${4:-}" extra="${5:-}"
    local out="$OUTDIR/${name}.wasm"
    TOTAL=$((TOTAL + 1))
    local result
    if ! result=$(compile "$src" "$out" "$extra" 2>&1); then
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        local err=$(echo "$result" | grep -v "^'+\|^$" | head -3)
        printf "${RED}COMPILE FAIL${NC} %-40s %s\n" "$name" "$err"
        return 1
    fi
    COMPILE_PASS=$((COMPILE_PASS + 1))

    local output
    if ! output=$(run_wasm "$out" "$export" 2>&1); then
        RUN_FAIL=$((RUN_FAIL + 1))
        local err=$(echo "$output" | grep -v "ExperimentalWarning\|trace-warnings" | head -3)
        printf "${YELLOW}RUN FAIL${NC}     %-40s %s\n" "$name" "$err"
        return 1
    fi
    # Filter node warnings from output
    output=$(echo "$output" | grep -v "ExperimentalWarning\|trace-warnings")

    if [ -n "$expected" ]; then
        if [ "$output" = "$expected" ]; then
            RUN_PASS=$((RUN_PASS + 1))
            printf "${GREEN}PASS${NC}         %-40s output=%s\n" "$name" "$output"
        else
            RUN_FAIL=$((RUN_FAIL + 1))
            printf "${RED}WRONG OUTPUT${NC} %-40s expected='%s' got='%s'\n" "$name" "$expected" "$output"
            return 1
        fi
    else
        RUN_PASS=$((RUN_PASS + 1))
        printf "${GREEN}PASS${NC}         %-40s %s\n" "$name" "${output:+(output: ${output:0:60})}"
    fi
}

test_compile_fail() {
    local name="$1" src="$2"
    TOTAL=$((TOTAL + 1))
    local out="$OUTDIR/${name}.wasm"
    local result
    if result=$(compile "$src" "$out" 2>&1); then
        REJECT_FAIL=$((REJECT_FAIL + 1))
        printf "${RED}SHOULD FAIL${NC}  %-40s (compiled unexpectedly)\n" "$name"
        return 1
    else
        REJECT_PASS=$((REJECT_PASS + 1))
        printf "${GREEN}REJECT OK${NC}    %-40s\n" "$name"
    fi
}

# ========== HEADER ==========
echo ""
printf "${BLUE}=== SPMD End-to-End Test Suite ===${NC}\n"
printf "TinyGo: %s\n" "$TINYGO"
printf "GOROOT: %s\n" "$GOROOT_SPMD"
printf "Runtime: %s\n" "$WASM_RUNTIME"
printf "Output: %s\n\n" "$OUTDIR"

# ========== LEVEL 0: Minimal SPMD (no imports) ==========
printf "${BLUE}--- Level 0: Minimal SPMD (no lane/reduce imports) ---${NC}\n"

# Create inline test programs
cat > "$OUTDIR/L0_store.go" << 'EOF'
package main

var result [8]int32

//go:export testStore
func testStore() int32 {
    go for i := range 8 {
        result[i] = int32(i) * 2
    }
    return result[0] + result[1] + result[2] + result[3]
}

func main() {}
EOF

cat > "$OUTDIR/L0_cond.go" << 'EOF'
package main

var result [8]int32

//go:export testCond
func testCond() int32 {
    go for i := range 8 {
        if i < 4 {
            result[i] = 1
        } else {
            result[i] = 0
        }
    }
    return result[0] + result[1] + result[2] + result[3] + result[4] + result[5]
}

func main() {}
EOF

cat > "$OUTDIR/L0_func.go" << 'EOF'
package main

import "lanes"

var result [8]int32

//go:noinline
func double(x lanes.Varying[int32]) lanes.Varying[int32] {
    return x * 2
}

//go:export testFunc
func testFunc() int32 {
    go for i := range 8 {
        result[i] = double(int32(i))
    }
    return result[0] + result[1] + result[2] + result[3]
}

func main() {}
EOF

test_compile_and_run "L0_store" "$OUTDIR/L0_store.go" "12" "testStore" "-scheduler=none"
test_compile_and_run "L0_cond"  "$OUTDIR/L0_cond.go"  "4"  "testCond"  "-scheduler=none"
test_compile_and_run "L0_func"  "$OUTDIR/L0_func.go"  "12" "testFunc"  "-scheduler=none"

# ========== LEVEL 1: reduce builtins ==========
printf "\n${BLUE}--- Level 1: reduce builtins ---${NC}\n"

cat > "$OUTDIR/L1_reduce_add.go" << 'EOF'
package main

import "reduce"

//go:export testReduceAdd
func testReduceAdd() int32 {
    var data [8]int32
    data[0] = 10; data[1] = 20; data[2] = 30; data[3] = 40
    data[4] = 50; data[5] = 60; data[6] = 70; data[7] = 80
    var total int32
    go for i := range 8 {
        total += reduce.Add(data[i])
    }
    return total
}

func main() {}
EOF

test_compile_and_run "L1_reduce_add" "$OUTDIR/L1_reduce_add.go" "360" "testReduceAdd" "-scheduler=none"

# ========== LEVEL 2: lanes builtins ==========
printf "\n${BLUE}--- Level 2: lanes builtins ---${NC}\n"

cat > "$OUTDIR/L2_lanes_index.go" << 'EOF'
package main

import "lanes"

var result [8]int32

//go:export testLanesIndex
func testLanesIndex() int32 {
    go for i := range 8 {
        idx := lanes.Index()
        result[i] = int32(idx)
    }
    return result[0] + result[1] + result[2] + result[3]
}

func main() {}
EOF

test_compile_and_run "L2_lanes_index" "$OUTDIR/L2_lanes_index.go" "6" "testLanesIndex" "-scheduler=none"

# ========== LEVEL 3: Varying variables + reduce ==========
printf "\n${BLUE}--- Level 3: Explicit varying variables ---${NC}\n"

cat > "$OUTDIR/L3_varying_var.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:export testVaryingVar
func testVaryingVar() int32 {
    var total lanes.Varying[int32]
    go for i := range 8 {
        total += int32(i)
    }
    return reduce.Add(total)
}

func main() {}
EOF

test_compile_and_run "L3_varying_var" "$OUTDIR/L3_varying_var.go" "28" "testVaryingVar" "-scheduler=none"

# ========== LEVEL 4: Range-over-slice ==========
printf "\n${BLUE}--- Level 4: Range-over-slice ---${NC}\n"

cat > "$OUTDIR/L4_range_slice.go" << 'EOF'
package main

var result [8]int32

//go:export testRangeSlice
func testRangeSlice() int32 {
    data := [8]int32{10, 20, 30, 40, 50, 60, 70, 80}
    go for i, v := range data[:] {
        result[i] = v * 2
    }
    return result[0] + result[1] + result[2] + result[3]
}

func main() {}
EOF

test_compile_and_run "L4_range_slice" "$OUTDIR/L4_range_slice.go" "200" "testRangeSlice" "-scheduler=none"

# ========== LEVEL 4b: SPMD function body (varying break) ==========
printf "\n${BLUE}--- Level 4b: SPMD function body with varying break ---${NC}\n"

cat > "$OUTDIR/L4b_varying_break.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:noinline
func breakTest(x lanes.Varying[int]) lanes.Varying[int] {
    var result lanes.Varying[int] = 10
    for i := range 10 {
        if x < i {
            result = i
            break
        }
    }
    return result
}

//go:export testVaryingBreak
func testVaryingBreak() int32 {
    data := lanes.From([]int{1, 3, 5, 8})
    r := reduce.From(breakTest(data))
    // x=1 breaks at i=2, x=3 breaks at i=4, x=5 breaks at i=6, x=8 breaks at i=9
    // Expected: [2, 4, 6, 9] → sum = 21
    return int32(r[0] + r[1] + r[2] + r[3])
}

func main() {}
EOF

test_compile_and_run "L4b_varying_break" "$OUTDIR/L4b_varying_break.go" "21" "testVaryingBreak" "-scheduler=none"

# ========== LEVEL 5a: Simple sum (range-over-slice + reduce) ==========
printf "\n${BLUE}--- Level 5a: Simple sum (range-over-slice + reduce) ---${NC}\n"

cat > "$OUTDIR/L5a_simple_sum.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:export testSimpleSum
func testSimpleSum() int32 {
    data := [16]int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
    var total lanes.Varying[int32] = 0
    go for _, value := range data[:] {
        total += value
    }
    return reduce.Add(total)
}

func main() {}
EOF

test_compile_and_run "L5a_simple_sum" "$OUTDIR/L5a_simple_sum.go" "136" "testSimpleSum" "-scheduler=none"

# ========== LEVEL 5b: Odd/even count (range-over-slice + varying if/else + reduce) ==========
printf "\n${BLUE}--- Level 5b: Odd/even count ---${NC}\n"

cat > "$OUTDIR/L5b_odd_even.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:export testOddEven
func testOddEven() int32 {
    data := [8]int32{1, 2, 3, 4, 5, 6, 7, 8}
    var odd lanes.Varying[int32]
    var even lanes.Varying[int32]
    go for _, value := range data[:] {
        if value&1 == 1 {
            odd++
        } else {
            even++
        }
    }
    // odd=4, even=4 → return 4*100+4 = 404
    return reduce.Add(odd)*100 + reduce.Add(even)
}

func main() {}
EOF

test_compile_and_run "L5b_odd_even" "$OUTDIR/L5b_odd_even.go" "404" "testOddEven" "-scheduler=none"

# ========== LEVEL 5f: Varying switch ==========
printf "\n${BLUE}--- Level 5f: Varying switch ---${NC}\n"

cat > "$OUTDIR/L5f_varying_switch.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:export testVaryingSwitch
func testVaryingSwitch() int32 {
    data := [4]int32{1, 2, 3, 1}
    var result lanes.Varying[int32]
    go for i := range 4 {
        switch data[i] {
        case 1:
            result = 10
        case 2:
            result = 20
        case 3:
            result = 30
        default:
            result = 0
        }
    }
    // data: [1,2,3,1] -> result: [10,20,30,10]
    // reduce.Add = 10+20+30+10 = 70
    return reduce.Add(result)
}

func main() {}
EOF

test_compile_and_run "L5f_varying_switch" "$OUTDIR/L5f_varying_switch.go" "70" "testVaryingSwitch" "-scheduler=none"

# ========== LEVEL 5g: Compound boolean conditions ==========
printf "\n${BLUE}--- Level 5g: Compound boolean conditions (&&/||) ---${NC}\n"

cat > "$OUTDIR/L5g_compound_conditions.go" << 'EOF'
package main

import (
    "lanes"
    "reduce"
)

//go:export testCompound
func testCompound() int32 {
    s := [4]int32{10, 25, 40, 55}

    // Test &&: s[i]>=20 && s[i]<=50 -> [F,T,T,F] -> 2
    var r1 lanes.Varying[int32]
    go for i := range 4 {
        if s[i] >= 20 && s[i] <= 50 {
            r1 = 1
        }
    }

    // Test ||: s[i]<20 || s[i]>50 -> [T,F,F,T] -> 2
    var r2 lanes.Varying[int32]
    go for i := range 4 {
        if s[i] < 20 || s[i] > 50 {
            r2 = 1
        }
    }

    // Test triple &&: s[i]>=15 && s[i]<=50 && s[i]!=40 -> [F,T,F,F] -> 1
    var r3 lanes.Varying[int32]
    go for i := range 4 {
        if s[i] >= 15 && s[i] <= 50 && s[i] != 40 {
            r3 = 1
        }
    }

    // 2 + 2 + 1 = 5
    return reduce.Add(r1) + reduce.Add(r2) + reduce.Add(r3)
}

func main() {}
EOF

test_compile_and_run "L5g_compound_conditions" "$OUTDIR/L5g_compound_conditions.go" "5" "testCompound" "-scheduler=none"

# ========== LEVEL 5c: Integration test examples ==========
printf "\n${BLUE}--- Level 5c: Integration examples (compile only) ---${NC}\n"

INTEG="$SPMD_ROOT/test/integration/spmd"
for dir in bit-counting array-counting \
           type-casting-varying varying-array-iteration \
           map-restrictions defer-varying printf-verbs goroutine-varying \
           panic-recover-varying select-with-varying-channels; do
    if [ -f "$INTEG/$dir/main.go" ]; then
        test_compile "integ_$dir" "$INTEG/$dir/main.go"
    fi
done

# ========== LEVEL 5d: Integration examples (compile + run) ==========
printf "\n${BLUE}--- Level 5d: Integration examples (compile + run) ---${NC}\n"

test_compile_and_run "integ_simple-sum"    "$INTEG/simple-sum/main.go"    "Sum: 136"            "" "-scheduler=none"
test_compile_and_run "integ_odd-even"      "$INTEG/odd-even/main.go"      "Result: Odd=4, Even=4" "" "-scheduler=none"
test_compile_and_run "integ_hex-encode"    "$INTEG/hex-encode/main.go"    ""                    "" "-scheduler=none"
test_compile_and_run "integ_debug-varying" "$INTEG/debug-varying/main.go" ""                    "" "-scheduler=none"
test_compile_and_run "integ_lanes-index-restrictions" "$INTEG/lanes-index-restrictions/main.go" "" "" "-scheduler=none"
test_compile_and_run "integ_to-upper"      "$INTEG/to-upper/main.go"      ""                    "" "-scheduler=none"
test_compile_and_run "integ_mandelbrot"    "$INTEG/mandelbrot/main.go"    ""                    "" "-scheduler=none"
test_compile_and_run "integ_store-coalescing" "$INTEG/store-coalescing/main.go" "" "" "-scheduler=none"
test_compile_and_run "integ_ipv4-parser"      "$INTEG/ipv4-parser/main.go"      "" "" "-scheduler=none"
test_compile_and_run "integ_type-switch-varying" "$INTEG/type-switch-varying/main.go" "" "" "-scheduler=none"

# ========== LEVEL 6: SPMD functions with mask ==========
printf "\n${BLUE}--- Level 6: Complex patterns (compile only) ---${NC}\n"

for dir in pointer-varying non-spmd-varying-return \
           spmd-call-contexts \
           base64-decoder \
           union-type-generics; do
    if [ -f "$INTEG/$dir/main.go" ]; then
        test_compile "integ_$dir" "$INTEG/$dir/main.go"
    fi
done

# ========== LEVEL 7: Illegal examples (should fail) ==========
printf "\n${BLUE}--- Level 7: Illegal examples (should be rejected) ---${NC}\n"

ILLEGAL="$INTEG/illegal-spmd"
for f in "$ILLEGAL"/*.go; do
    [ -f "$f" ] || continue
    name=$(basename "$f" .go)
    test_compile_fail "illegal_$name" "$f"
done

# ========== SUMMARY ==========
echo ""
printf "${BLUE}=== Summary ===${NC}\n"
printf "Total tests:     %d\n" "$TOTAL"
printf "${GREEN}Compile pass:    %d${NC}\n" "$COMPILE_PASS"
printf "${RED}Compile fail:    %d${NC}\n" "$COMPILE_FAIL"
printf "${GREEN}Run pass:        %d${NC}\n" "$RUN_PASS"
printf "${RED}Run fail:        %d${NC}\n" "$RUN_FAIL"
printf "${GREEN}Reject pass:     %d${NC}\n" "$REJECT_PASS"
printf "${RED}Reject fail:     %d${NC}\n" "$REJECT_FAIL"
echo ""

if [ "$COMPILE_FAIL" -gt 0 ] || [ "$RUN_FAIL" -gt 0 ] || [ "$REJECT_FAIL" -gt 0 ]; then
    printf "${YELLOW}Some tests failed. See above for details.${NC}\n"
    exit 1
else
    printf "${GREEN}All tests passed!${NC}\n"
fi
