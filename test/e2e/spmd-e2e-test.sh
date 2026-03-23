#!/bin/bash
# SPMD End-to-End Test Script
# Tests progressive levels of SPMD compilation and execution
set -uo pipefail

SPMD_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TINYGO="$SPMD_ROOT/tinygo/build/tinygo"
GOROOT_SPMD="$SPMD_ROOT/go"
RUNNER="$SPMD_ROOT/test/e2e/run-wasm.mjs"
WASMOPT="${WASMOPT:-/tmp/wasm-opt}"
# If wasm-opt binary doesn't exist, disable it so TinyGo doesn't fail
if [ ! -x "$WASMOPT" ]; then
    WASMOPT=""
fi
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
        local match_mode="exact"
        local match_pattern="$expected"
        if [[ "$expected" == contains:* ]]; then
            match_mode="contains"
            match_pattern="${expected#contains:}"
        fi

        local passed=false
        if [ "$match_mode" = "exact" ]; then
            [ "$output" = "$match_pattern" ] && passed=true
        else
            # contains: check each required substring (separated by |||)
            passed=true
            local IFS_OLD="$IFS"
            while IFS= read -r needle; do
                if ! echo "$output" | grep -qF "$needle"; then
                    passed=false
                    break
                fi
            done <<< "${match_pattern//|||/$'\n'}"
            IFS="$IFS_OLD"
        fi

        if $passed; then
            RUN_PASS=$((RUN_PASS + 1))
            printf "${GREEN}PASS${NC}         %-40s %s\n" "$name" "(output verified)"
        else
            RUN_FAIL=$((RUN_FAIL + 1))
            printf "${RED}WRONG OUTPUT${NC} %-40s expected='%s' got='%s'\n" "$name" "$expected" "$output"
            return 1
        fi
    else
        RUN_PASS=$((RUN_PASS + 1))
        printf "${GREEN}PASS (no output check)${NC} %-40s %s\n" "$name" "${output:+(output: ${output:0:60})}"
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
test_compile_and_run "integ_map-restrictions" "$INTEG/map-restrictions/main.go" \
    "contains:Map restrictions demonstration completed" "" "-scheduler=none"

# Goroutine/channel tests require -scheduler=asyncify (cooperative scheduling via Binaryen).
# Named SPMD functions can't be goroutines (asyncify doesn't handle $gowrapper),
# but anonymous closures capturing varying values work fine.
test_compile_and_run "integ_goroutine-varying" "$INTEG/goroutine-varying/main.go" \
    "contains:Total: 204|||Sum from SPMD goroutine: 100|||All goroutine varying tests completed successfully" "" "-scheduler=asyncify"
test_compile_and_run "integ_select-with-varying-channels" "$INTEG/select-with-varying-channels/main.go" \
    "contains:Pipeline processing completed|||Done signal received|||All select with varying channels tests completed successfully" "" "-scheduler=asyncify"
test_compile_and_run "integ_spmd-call-contexts" "$INTEG/spmd-call-contexts/main.go" \
    "contains:All SPMD call context operations completed successfully" "" "-scheduler=asyncify"

# array-counting uses go for over [][]int (Varying[[]int], laneCount=1 serial).
# The inner for loop runs serially (N=1), summing each lane's slice independently.
test_compile_and_run "integ_array-counting" "$INTEG/array-counting/main.go" \
    "Array sums: [3 3 4 18]" "" "-scheduler=none"
test_compile_and_run "integ_type-casting-varying" "$INTEG/type-casting-varying/main.go" \
    "contains:All type casting tests completed successfully!" "" "-scheduler=none"
test_compile_and_run "integ_varying-array-iteration" "$INTEG/varying-array-iteration/main.go" \
    "contains:All varying array iteration examples completed successfully" "" "-scheduler=none"
test_compile_and_run "integ_printf-verbs" "$INTEG/printf-verbs/main.go" \
    "contains:Found first '%' at position 6|||No '%' found in: No verbs here|||Found first '%' at position 9" "" "-scheduler=none"
test_compile_and_run "integ_non-spmd-varying-return" "$INTEG/non-spmd-varying-return/main.go" \
    "contains:Constant varying: [42 42 42 42]|||Array varying: [10 20 30 40]|||All non-SPMD varying return operations completed successfully" "" "-scheduler=none"

# ========== LEVEL 5d: Integration examples (compile + run) ==========
printf "\n${BLUE}--- Level 5d: Integration examples (compile + run) ---${NC}\n"

test_compile_and_run "integ_simple-sum"    "$INTEG/simple-sum/main.go"    "Sum: 136"            "" "-scheduler=none"
test_compile_and_run "integ_odd-even"      "$INTEG/odd-even/main.go"      "Result: Odd=4, Even=4" "" "-scheduler=none"
test_compile_and_run "integ_hex-encode"    "$INTEG/hex-encode/main.go"    "contains:Correctness: SPMD and Scalar results match." "" "-scheduler=none"
test_compile_and_run "integ_debug-varying" "$INTEG/debug-varying/main.go" \
    "contains:Doubled: [20 40 60 80]|||Big values: [_ _ 30 40]|||Total for this iteration: 200|||Total for this iteration: 520" \
    "" "-scheduler=none"
test_compile_and_run "integ_lanes-index-restrictions" "$INTEG/lanes-index-restrictions/main.go" \
    "contains:Lane [0 1 2 3] result: [0 11 22 33]|||Result from SPMD function: [50 51 52 53]|||All lanes.Index() restrictions demonstrated successfully" \
    "" "-scheduler=none"
test_compile_and_run "integ_to-upper"      "$INTEG/to-upper/main.go" \
    "contains:'hello world' -> 'HELLO WORLD'|||'Hello World' -> 'HELLO WORLD'|||'hello123WORLD' -> 'HELLO123WORLD'" \
    "" "-scheduler=none"
test_compile_and_run "integ_mandelbrot"    "$INTEG/mandelbrot/main.go" \
    "contains:Verification: 0 differences out of 65536 pixels|||Results match between serial and SPMD versions" \
    "" "-scheduler=none"
test_compile_and_run "integ_store-coalescing" "$INTEG/store-coalescing/main.go" \
    "contains:Result: [170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187 170 187]|||PASS" \
    "" "-scheduler=none"
test_compile_and_run "integ_ipv4-parser"      "$INTEG/ipv4-parser/main.go" \
    "contains:'192.168.1.1' -> 192.168.1.1|||'127.0.0.1' -> 127.0.0.1|||'192.168.1.a' -> ERROR: parse 192.168.1.a at position 10: unexpected character|||'256.1.1.1' -> ERROR: parse 256.1.1.1 at position 0: IPv4 field has value >255|||'192.168.01.1' -> ERROR: parse 192.168.01.1 at position 0: IPv4 field has octet with leading zero" \
    "" "-scheduler=none"
test_compile_and_run "integ_type-switch-varying" "$INTEG/type-switch-varying/main.go" \
    "contains:Varying int: sum=336|||Assert ok: sum=208|||All type switch varying tests completed" \
    "" "-scheduler=none"
test_compile_and_run "integ_defer-varying"       "$INTEG/defer-varying/main.go" \
    "contains:Processed value: [6 14 4 18]|||Allocated for values > 5: [_ 700 _ 900]|||Third defer (first execution): [300 700 200 900]|||All defer varying tests completed successfully" \
    "" "-scheduler=none"
test_compile_and_run "integ_panic-recover-varying" "$INTEG/panic-recover-varying/main.go" \
    "contains:Processed: [5 10 15 25] -> [10 20 30 50]|||OK: [1 2 3 4]|||Done" \
    "" "-scheduler=none"
test_compile_and_run "integ_bit-counting" "$INTEG/bit-counting/main.go" "Bit counts: 28" "" "-scheduler=none"
test_compile_and_run "integ_lo-sum"      "$INTEG/lo-sum/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-mean"     "$INTEG/lo-mean/main.go"     "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-min"      "$INTEG/lo-min/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-max"      "$INTEG/lo-max/main.go"      "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-contains" "$INTEG/lo-contains/main.go" "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_lo-clamp"    "$INTEG/lo-clamp/main.go"    "contains:Correctness: PASS" "" "-scheduler=none"
test_compile_and_run "integ_pointer-varying" "$INTEG/pointer-varying/main.go" "contains:Correctness: PASS" "" "-scheduler=none"

# ========== LEVEL 6: SPMD functions with mask ==========
printf "\n${BLUE}--- Level 6: Complex patterns (compile only) ---${NC}\n"

for dir in base64-decoder; do
    if [ -f "$INTEG/$dir/main.go" ]; then
        test_compile "integ_$dir" "$INTEG/$dir/main.go"
    fi
done

test_compile_and_run "integ_union-type-generics" "$INTEG/union-type-generics/main.go" \
    "contains:All union type generic operations completed successfully" "" "-scheduler=none"

# ========== LEVEL 7: Illegal examples (should fail) ==========
printf "\n${BLUE}--- Level 7: Illegal examples (should be rejected) ---${NC}\n"

ILLEGAL="$INTEG/illegal-spmd"
for f in "$ILLEGAL"/*.go; do
    [ -f "$f" ] || continue
    name=$(basename "$f" .go)
    test_compile_fail "illegal_$name" "$f"
done

# ========== LEVEL 8: Dual-mode testing (SIMD vs scalar) ==========
printf "\n${BLUE}--- Level 8: Dual-mode (SIMD vs scalar) ---${NC}\n"

test_dual_mode() {
    local name="$1" src="$2" extra="${3:--scheduler=none}"
    local simd_out="$OUTDIR/${name}-simd.wasm"
    local scalar_out="$OUTDIR/${name}-scalar.wasm"
    TOTAL=$((TOTAL + 1))

    # Compile SIMD version
    if ! compile "$src" "$simd_out" "$extra" >/dev/null 2>&1; then
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "SIMD compile failed"
        return 1
    fi
    # Compile scalar version
    if ! compile "$src" "$scalar_out" "$extra -simd=false" >/dev/null 2>&1; then
        COMPILE_FAIL=$((COMPILE_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "Scalar compile failed"
        return 1
    fi
    COMPILE_PASS=$((COMPILE_PASS + 1))

    # Run both and compare output.
    # Filter node warnings and benchmark timing lines (Scalar:/SPMD:/Speedup:) which
    # are non-deterministic and differ between SIMD and scalar modes by design.
    local simd_output scalar_output
    simd_output=$(run_wasm "$simd_out" "" 2>&1 | grep -v "ExperimentalWarning\|trace-warnings\|^Scalar:\|^SPMD:\|^Speedup:")
    scalar_output=$(run_wasm "$scalar_out" "" 2>&1 | grep -v "ExperimentalWarning\|trace-warnings\|^Scalar:\|^SPMD:\|^Speedup:")

    if [ "$simd_output" = "$scalar_output" ]; then
        RUN_PASS=$((RUN_PASS + 1))
        printf "${GREEN}DUAL PASS${NC}    %-40s\n" "$name"
    else
        RUN_FAIL=$((RUN_FAIL + 1))
        printf "${RED}DUAL FAIL${NC}    %-40s %s\n" "$name" "outputs differ"
        diff <(echo "$simd_output") <(echo "$scalar_output") | head -5
    fi
}

# Tests that produce scalar-only output (reduce results, not varying %v format).
# Excluded from dual-mode testing:
#   hex-encode, mandelbrot: print benchmark timing lines (Scalar/SPMD dst/Speedup) which
#     differ between modes by design; mandelbrot also prints lane-count-dependent varying display.
#   bit-counting: scalar fallback crashes (SIGSEGV in splatScalar, pre-existing bug).
# Timing lines (^Scalar:/^SPMD:/^Speedup:) are already stripped, but hex-encode uses
# different timing prefixes (e.g. "SPMD dst:", "Speedup dst") so it is excluded entirely.
test_dual_mode "dual_simple-sum"       "$INTEG/simple-sum/main.go"
test_dual_mode "dual_odd-even"         "$INTEG/odd-even/main.go"
test_dual_mode "dual_lo-sum"           "$INTEG/lo-sum/main.go"
test_dual_mode "dual_lo-mean"          "$INTEG/lo-mean/main.go"
test_dual_mode "dual_lo-min"           "$INTEG/lo-min/main.go"
test_dual_mode "dual_lo-max"           "$INTEG/lo-max/main.go"
test_dual_mode "dual_lo-contains"      "$INTEG/lo-contains/main.go"
test_dual_mode "dual_lo-clamp"         "$INTEG/lo-clamp/main.go"

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
