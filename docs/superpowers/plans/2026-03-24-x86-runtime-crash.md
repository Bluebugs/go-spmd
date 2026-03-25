# Fix x86-64 Native Runtime Crash in IPv4 Parser

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the "index out of range" crash when running the IPv4 parser compiled for native x86-64 with SPMD SIMD.

**Architecture:** The crash is caused by 32-bit vs 64-bit `int` size differences between WASM (int=4 bytes) and x86-64 (int=8 bytes). Key issues: (1) `flensTable [81][4]int` is 32 bytes per entry on x86-64 vs 16 on WASM — exceeds v128, breaking vector promotion; (2) `DotProductI8x16Add` returns `[4]int` = `[4 x i64]` on x86-64 but the intrinsic produces `<4 x i32>` — sign-extension required; (3) `go for field := range 4` with `int` values creates `<2 x i64>` lanes (128-bit/8-byte = 2 lanes, not 4). Investigate, isolate, and fix each issue.

**Tech Stack:** TinyGo LLVM codegen, Go SPMD, native x86-64

---

## Task 1: Isolate the crash with a minimal reproducer

- [ ] **Step 1: Build minimal test cases to isolate which feature crashes**

Test A: Just the Lemire hash + table lookup (no SPMD loop):
```go
package main
import "math/bits"
func main() {
    x := uint16(0b0000_0100_1000_0010) // dots at positions 1, 7, 10
    println(bits.OnesCount16(x))        // should print 3
    println(bits.Len16(x))              // should print 11
}
```

Test B: The `go for field := range 4` loop with `[4]int`:
```go
package main
import "reduce"
func main() {
    arr := [4]int{10, 20, 30, 40}
    go for i := range 4 {
        v := arr[i]
        if reduce.Any(v > 100) { println("overflow") }
    }
    println("done")
}
```

Test C: `lanes.DotProductI8x16Add` on native:
```go
package main
import "lanes"
func main() {
    a := [16]byte{1, 2, 3, 0, 4, 5, 6, 0, 7, 8, 9, 0, 1, 0, 0, 0}
    b := [16]byte{100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0, 100, 10, 1, 0}
    result := lanes.DotProductI8x16Add(a, b, [4]int{})
    println(result[0], result[1], result[2], result[3])
}
```

Build each with: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/testX /tmp/testX.go`

Report which tests crash and which succeed.

- [ ] **Step 2: Check the LLVM IR for type mismatches**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -llvm-features="+ssse3,+sse4.2,+avx2" -internal-printir -o /tmp/ipv4-debug test/integration/spmd/ipv4-parser/main.go 2>/tmp/ipv4-x86.ll
```

Search for:
- `i64` vs `i32` mismatches in the DotProduct result
- `[4 x i64]` for flensTable (should it be `[4 x i32]`?)
- Bounds check constants (what value is used for the shuffle/flens table index check?)

- [ ] **Step 3: Fix identified issues and verify**

- [ ] **Step 4: Commit**
