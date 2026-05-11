# SPMD vs Go experimental SIMD intrinsics: where the gap comes from

**Date:** 2026-05-10
**Hardware:** AMD Ryzen 7 6800U (Zen3), AVX2 8-wide i32
**Inputs:** `[]int32` of length 1024
**Targets compared:**
- TinyGo + SPMD (this project) — `go for` loops + `lanes`/`reduce`, compiled with `-target=amd64-avx2`
- `samber/lo/exp/simd` — experimental package built on Go's new `simd` intrinsics (compiled with stock `gc`)
- `samber/lo` generic Go — scalar baseline

## Headline numbers

| Kernel        | lo/simd (Go intrinsics) | SPMD (TinyGo) | SPMD speedup over lo/simd |
|---------------|------------------------:|--------------:|--------------------------:|
| Sum           | 329 ns/op               | 181 ns/op     | **1.82x**                 |
| Min           | 337 ns/op               | 160 ns/op     | **2.11x**                 |
| Contains (x8) | 178 ns/op               | 69 ns/op      | **2.57x**                 |

Both lower the loop to AVX2 8-wide i32. Both issue roughly the same number of vector ops per 8 elements in the body. The runtime gap is not vector-ISA-choice; it is **register liveness across iterations** and **what each compiler can see across function boundaries**.

## What the disassembly shows

### Sum — accumulator forced to the stack every iteration

`samber/lo/exp/simd.SumInt32x8` hot loop (~26 instrs / 8 elements):

```asm
vmovdqu  [rsp+0x38], ymm0       ; spill accumulator: ymm0 is the ABI return reg
mov      ebx, 0x8
CALL     LoadInt32x8Slice        ; returns the loaded <8xi32> in ymm0
vmovdqu  ymm1, [rsp+0x38]        ; reload accumulator
vpaddd   ymm0, ymm1, ymm0
mov      rcx, [rsp+0x88]         ; reload 3 loop vars the call may have clobbered
mov      rdx, [rsp+0x60]
mov      rax, [rsp+0x58]
lea/cmp/jb/cmp/jbe                ; loop control + bounds checks
```

SPMD `sum` hot loop (~27 instrs / 8 elements):

```asm
...mask setup for tail (4-6 instrs, runs every iter)...
vpand    ymm3, ymm3, [rbx]       ; mask-and load (memory operand)
vpaddd   ymm0, ymm3, ymm0        ; ymm0 stays live across all iterations
add r11d,-8 / add r10,r8 / add r9,8 / jmp
```

Same vector op count. The difference is the **store-call-reload chain on ymm0**: the Go intrinsic API returns the loaded vector through ymm0, which is also the natural accumulator register, so gc has to spill the accumulator before every call and reload after. That is a ~6-cycle latency chain on top of every iteration.

Horizontal reduction:
- lo/simd: store ymm0 to stack, scalar loop summing 8 lanes (9 instrs).
- SPMD: `vextracti128 + 3×vphaddd + vmovd` (5 instrs, all vector).

### Min — same spill pattern, plus an in-loop boolean branch

`MinInt32x8` carries a `firstInitialized bool` so the first iteration seeds `minVec` instead of comparing. That adds a `movzx + test + je` **inside the hot loop**, executed 128 times for n=1024, mispredicted exactly once. Decode/dispatch overhead, but real.

SPMD has no equivalent: the mask handles the first iteration uniformly, and `vpminsd` replaces `vpaddd`. Horizontal reduce uses `vextracti128 + vpminsd` chains (7 instrs) vs lo/simd's stack-spill + 8×`mov`/`cmovl` (17 scalar instrs).

### Contains — peeled main loop and `vtestps`

lo/simd hot loop (~27 instrs / 8 elements):

```asm
...bounds checks...
CALL     LoadInt32x8Slice
vmovdqu  ymm1, [rsp+0x18]        ; reload broadcast needle
vpcmpeqd ymm0, ymm0, ymm1
vmovmskps edx, ymm0              ; bitmask -> GP reg
test     dl, dl
je       <continue>
```

SPMD tight main loop, 9 instrs / 8 elements:

```asm
add r10, 0x8 / cmp r10,r8 / jge
lea r11,[r9+rcx] / sar r9,0x1e
vpcmpeqd ymm2, ymm1, [rdi+r9]    ; load+compare fused
vtestps  ymm2, ymm2               ; sets ZF directly from vector
mov r9, r11
je       <found>
```

Three structural wins compound here:
1. **Loop peeling.** When `remaining >= 8` no mask is needed, so the compiler emits a stripped main body with no tail-mask overhead. The masked path runs only at the tail.
2. **Memory-operand `vpcmpeqd`.** The load fuses into the compare; one instruction, not two.
3. **`vtestps` instead of `vmovmskps` + `test`.** Vector flag-set in one instruction; no round-trip through a general-purpose register.

lo/simd cannot express any of these through the current intrinsic surface: `cmp.ToBits() != 0` always lowers to `vmovmskps + test`, the load is a separate function call so it cannot fuse into the compare, and there is no peeling pass because the compiler does not own the loop shape.

## Root causes

**1. Accumulator spill driven by the intrinsic's return-value ABI (Sum, Min).**
Vector intrinsics that return a vector use ymm0. Any caller-side vector live across the call must round-trip through the stack. For a reduction the accumulator *is* the value live across every iteration — so it spills every iteration.

**2. Inlining failure across the intrinsic boundary (all kernels).**
The `simd` package exposes per-op intrinsics as bodyless declarations (assembly stubs / compiler-generated symbols). `LoadInt32x8Slice → LoadInt32x8 → vmovdqu` is a three-line wrapper that the gc compiler cannot see through. If it could, the load would fold into the next op exactly the way LLVM does for SPMD, the save/restore would vanish, and the kernel would collapse to roughly 15 instrs / 8 elements — faster than SPMD's current code. This is not a missed gc optimization; it is a consequence of how the API is shaped.

**3. No loop-level rewrites available to the intrinsic user.**
Loop peeling (separating a full-width main body from a masked tail), early-exit reductions using `vtestps`, and choosing horizontal-reduce strategies (vector vs scalar) all require the compiler to own the loop. The intrinsic user writes one iteration at a time; the compiler stitches them together but cannot restructure the loop. SPMD owns the whole loop and applies all three.

## Could the experimental `simd` package close the gap?

| Kernel   | Closable in principle? | What it would take |
|----------|------------------------|--------------------|
| Sum      | Partially              | Restructure API so loads write into a caller-owned accumulator (e.g. `acc.AddFrom(slice)`) instead of returning through ymm0. Or make the intrinsic wrappers inlinable. With either, Sum's loop drops to ~15 instrs / 8 elements. |
| Min      | Partially              | Same as Sum, plus dropping `firstInitialized` in favour of a sentinel or an explicit pre-load of the first chunk. |
| Contains | No                     | Requires a new intrinsic exposing `vtestps`-style direct ZF set, and loop peeling at the gc level — neither is in scope of a library. |

The structural takeaway: **per-operation intrinsics with vector return values cannot keep an accumulator live across iterations without compiler-level inlining of the intrinsic.** SPMD sidesteps this entirely by giving the compiler the whole loop body — LLVM schedules registers freely across iterations and applies loop peeling without the user having to write it.

## Methodology

- SPMD binaries: `PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=amd64-avx2 -o <out> test/integration/spmd/lo-<kernel>/main.go`.
- lo/simd binaries: `go test -c` in `test/bench/simd/`, then `go tool objdump -s "SumInt32x8|MinInt32x8|ContainsInt32x8" <binary>`.
- Driver: `test/e2e/spmd-benchmark-x86.sh` (three-way SPMD vs lo vs lo/simd).
- Disassembly: `objdump -d --disassembler-options=intel` for the TinyGo output, `go tool objdump` for the gc output.

See also: [docs/lo-spmd-comparison.md](lo-spmd-comparison.md) for the broader SPMD-vs-lo numbers (including scalar lo baseline).
