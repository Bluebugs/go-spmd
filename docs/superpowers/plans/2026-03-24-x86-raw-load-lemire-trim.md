# x86 Raw Load + Lemire Scalar Trim

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the 5-instruction P1 masking overhead on x86-64 by combining page-safe raw loads with Lemire-style scalar bitmask trimming, reducing P1 from 5 SIMD instructions to 1 `vmovdqu`.

**Architecture:** Two changes: (1) In `createSPMDVectorFromMemory`, when targeting x86-64, emit a raw `vmovdqu` with a page-boundary check fallback (aligned load + shift for the 0.4% edge case). No zero-padding — garbage bytes are allowed. (2) In the IPv4 parser Go source, replace `|| c == 0` null check with scalar bitmask trimming after extraction: `dotBitmask &= (1 << len(s)) - 1` and `validBitmask &= (1 << len(s)) - 1`. This makes the masking unnecessary — garbage bytes beyond the string produce false positives that are trimmed in scalar.

**Tech Stack:** TinyGo LLVM codegen (x86-64), Go SPMD source

---

## Why this works

Current flow:
```
P1: load 16 bytes → mask to zero bytes beyond len(s) → c == 0 detects padding
P3: extract dotBitmask → all 16 bits are valid (garbage was zeroed)
```

New flow:
```
P1: load 16 bytes RAW (garbage beyond len(s) stays as garbage)
P2: classify as before — garbage bytes may produce false dot/digit matches
P3: extract dotBitmask → trim in scalar: dotBitmask &= (1 << len(s)) - 1
    extract validBitmask → trim: check (validBitmask & lengthMask) == lengthMask
```

The garbage bytes are never seen by the algorithm — they're masked out at the scalar bitmask level (3 scalar instructions) instead of the vector level (5 SIMD instructions). Net: **-5 SIMD + 3 scalar = -2 instructions, but the SIMD instructions were on the critical path** (they gate all subsequent vector ops), so the latency improvement is larger than the instruction count suggests.

## Page-safety strategy (x86-64 only)

- **Fast path (99.6%)**: `ptr % 4096 <= 4080` → raw `vmovdqu [ptr]` (1 instruction)
- **Slow path (0.4%)**: `ptr % 4096 > 4080` → aligned load `vmovdqu [ptr & ~15]` + `pshufb` shift by `ptr & 15` (3-4 instructions, provably safe since aligned 16-byte blocks never cross pages)

On WASM: keep the existing overread+mask approach (already safe with 16-byte guard zone).

---

## File Map

| File | Change |
|------|--------|
| `tinygo/compiler/spmd.go` | x86 path in `createSPMDVectorFromMemory`: page check + raw load |
| `tinygo/compiler/spmd_x86.go` | Add aligned-load-shift helper |
| `test/integration/spmd/ipv4-parser/main.go` | Remove `\|\| c == 0`, add scalar bitmask trimming |

---

## Task 1: IPv4 parser source — Lemire-style scalar trim

**Files:**
- Modify: `test/integration/spmd/ipv4-parser/main.go`

The Go source changes are target-independent — they work on both WASM and x86.

- [ ] **Step 1: Remove `|| c == 0` and add scalar bitmask trimming**

In the classification loop, change:
```go
validChars := isDot || digitMask || c == 0
```
to:
```go
validChars := isDot || digitMask
```

Then after the loop, before the dotCount check, add scalar trimming:
```go
// Trim bitmasks to string length — clear false matches from garbage bytes
// beyond len(s). This is the Lemire approach: instead of zero-padding the
// input vector (5 SIMD instructions), we trim the scalar bitmask (3 scalar
// instructions). Garbage bytes produce false dot/digit matches that are
// harmlessly cleared here.
lengthMask := uint16((1 << len(s)) - 1)
dotBitmask &= lengthMask
```

For the `validChars` check — currently `reduce.All(validChars)` inside the loop. With garbage bytes, this would trigger for invalid garbage. Two approaches:

**Approach A**: Build `validBitmask` alongside `dotBitmask` in the loop, then check in scalar:
```go
var validBitmask uint16
// Inside go for loop:
validBitmask |= uint16(reduce.Mask(validChars)) << loop

// After loop:
// Check all positions within string length have valid chars
if validBitmask & lengthMask != lengthMask {
    // Find first invalid char within string
    invalidMask := ^validBitmask & lengthMask
    return [4]byte{}, 2, bits.TrailingZeros16(invalidMask)
}
```

**Approach B (simpler)**: Keep `|| c == 0` but understand it's now checking for ACTUAL zero bytes in the input (which are invalid for IPv4), not padding zeros. Remove the dependency on zero-padding. The `createSPMDVectorFromMemory` can then skip the mask on x86 without breaking the algorithm.

Wait — Approach B doesn't work because on x86 without masking, bytes beyond `len(s)` are garbage (not zero), so `c == 0` won't match them, and `validChars` will be false for garbage bytes, triggering the error. We need Approach A.

Actually, the simplest approach: **move the validChars check out of the loop and into scalar**, using the bitmask:

```go
// Inside go for loop — build both bitmasks:
go for i, c := range input {
    isDot := c == '.'
    digitMask := (c >= '0' && c <= '9')
    validChars := isDot || digitMask

    digits[i] = c - '0'
    dotBitmask |= uint16(reduce.Mask(isDot)) << loop
    validBitmask |= uint16(reduce.Mask(validChars)) << loop
    loop += lanes.Count(c)
}

// Scalar validation — trim to string length, then check
lengthMask := uint16((1 << len(s)) - 1)
dotBitmask &= lengthMask

// All positions 0..len(s)-1 must be valid (dot or digit)
if validBitmask & lengthMask != lengthMask {
    invalidMask := ^validBitmask & lengthMask
    return [4]byte{}, 2, bits.TrailingZeros16(invalidMask)
}
```

This removes the `reduce.All(validChars)` + `reduce.FindFirstSet(!validChars)` from inside the loop (2 reduce operations that generate `i8x16.all_true` + `i8x16.bitmask + ctz`) and replaces with 3 scalar instructions after the loop.

- [ ] **Step 2: Verify on WASM (must still work with zero-padding)**

```bash
cd /home/cedric/work/SPMD
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-trim-wasm.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-trim-wasm.wasm 2>&1 | head -15
```

- [ ] **Step 3: Verify on x86-64 native**

```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/ipv4-trim-x86 test/integration/spmd/ipv4-parser/main.go
/tmp/ipv4-trim-x86 2>&1 | head -15
```

All 10 test cases must be correct on both targets.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/ipv4-parser/main.go
git commit -m "perf: Lemire-style scalar bitmask trimming replaces vector zero-padding"
```

---

## Task 2: x86 raw load in `createSPMDVectorFromMemory`

**Files:**
- Modify: `tinygo/compiler/spmd.go`
- Modify: `tinygo/compiler/spmd_x86.go`

On x86-64, `createSPMDVectorFromMemory` currently does the same overread+mask as WASM (v128.load + splat + compare + bitselect). Since the IPv4 parser no longer needs zero-padded bytes (scalar bitmask trimming handles garbage), we can emit a raw load.

- [ ] **Step 1: Add page-boundary-safe raw load for x86**

In `createSPMDVectorFromMemory`, add the x86 path BEFORE the existing WASM path:

```go
if b.spmdIsX86() && lanes == 16 {
    // x86-64: raw 16-byte load. The caller handles garbage bytes via
    // scalar bitmask trimming (Lemire approach). No zero-padding needed.
    //
    // Page safety: if ptr is within 16 bytes of a page boundary
    // (ptr & 0xFFF > 0xFF0), use aligned load + shift. Otherwise,
    // direct vmovdqu.
    rawLoad := b.spmdX86PageSafeLoad16(dataPtr)
    return rawLoad
}
```

In `spmd_x86.go`, add:

```go
// spmdX86PageSafeLoad16 loads 16 bytes from ptr, handling page boundaries.
// Fast path (99.6%): vmovdqu [ptr]
// Slow path (0.4%): aligned load [ptr & ~15] + pshufb shift
func (b *builder) spmdX86PageSafeLoad16(ptr llvm.Value) llvm.Value {
    i8Type := b.ctx.Int8Type()
    i32Type := b.ctx.Int32Type()
    v16i8 := llvm.VectorType(i8Type, 16)

    // Check if we're within 16 bytes of page end: (ptr & 0xFFF) > 0xFF0
    ptrInt := b.CreatePtrToInt(ptr, b.uintptrType, "page.ptr")
    pageMask := llvm.ConstInt(b.uintptrType, 0xFFF, false)
    pageOff := b.CreateAnd(ptrInt, pageMask, "page.off")
    threshold := llvm.ConstInt(b.uintptrType, 0xFF0, false)
    nearEnd := b.CreateICmp(llvm.IntUGT, pageOff, threshold, "page.near")

    // Create basic blocks for fast/slow paths + merge
    curBlock := b.GetInsertBlock()
    fastBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.fast")
    slowBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.slow")
    mergeBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.merge")

    b.CreateCondBr(nearEnd, slowBlock, fastBlock)

    // Fast path: direct unaligned load
    b.SetInsertPointAtEnd(fastBlock)
    fastLoad := b.CreateLoad(v16i8, ptr, "rawload.fast")
    fastLoad.SetAlignment(1)
    b.CreateBr(mergeBlock)

    // Slow path: aligned load + pshufb shift
    b.SetInsertPointAtEnd(slowBlock)
    alignMask := llvm.ConstInt(b.uintptrType, ^uint64(15), false)
    alignedInt := b.CreateAnd(ptrInt, alignMask, "aligned.ptr")
    alignedPtr := b.CreateIntToPtr(alignedInt, ptr.Type(), "aligned.ptrv")
    alignedLoad := b.CreateLoad(v16i8, alignedPtr, "aligned.load")
    alignedLoad.SetAlignment(16)

    // Compute shift amount: ptr & 15
    offset := b.CreateAnd(ptrInt, llvm.ConstInt(b.uintptrType, 15, false), "shift.off")
    offsetI8 := b.CreateTrunc(offset, i8Type, "shift.i8")

    // Build shift indices: [off, off+1, off+2, ..., off+15]
    // Using vpaddb with a constant [0,1,2,...,15]
    indices := make([]llvm.Value, 16)
    for i := 0; i < 16; i++ {
        indices[i] = llvm.ConstInt(i8Type, uint64(i), false)
    }
    indexVec := llvm.ConstVector(indices, false)
    offSplat := b.CreateVectorSplat(16, offsetI8, "shift.splat")
    shiftIndices := b.CreateAdd(indexVec, offSplat, "shift.idx")

    // pshufb to shift bytes into position
    slowResult := b.spmdX86Pshufb(alignedLoad, shiftIndices)
    b.CreateBr(mergeBlock)

    // Merge
    b.SetInsertPointAtEnd(mergeBlock)
    phi := b.CreatePHI(v16i8, "rawload.result")
    phi.AddIncoming([]llvm.Value{fastLoad, slowResult},
                    []llvm.BasicBlock{fastBlock, slowBlock})
    return phi
}
```

Note: `pshufb` returns 0 for indices with bit 7 set. When `offset + i >= 16`, `shiftIndices[i]` wraps to `offset+i` which is 16-31 — bit 4 is set but bit 7 is NOT. This means pshufb would return `aligned[offset+i & 15]` for indices 16-31 on x86 (it only checks bit 7, not bounds). We need indices >= 128 for zero. So instead of `vpaddb`, use a different approach:

Actually, `pshufb` on x86 uses only the low 4 bits as the index AND zeros the result when bit 7 is set. So `shiftIndices[i] = offset + i`. For `i` where `offset + i >= 16`, the value is 16-30. Low 4 bits = `(offset+i) & 15` which gives `0..14` — this wraps around and reads from the beginning of the aligned block, which is wrong. We need to zero those bytes.

Better approach for the slow path: use `palignr` (SSSE3) if available, or load two aligned 16-byte blocks and combine:

```go
// Alternative slow path: load two aligned blocks and combine
// aligned_ptr = ptr & ~15
// block0 = load [aligned_ptr]       // 16 bytes at aligned address
// block1 = load [aligned_ptr + 16]  // next 16 bytes (safe — within same or next mapped page... wait, that's not safe either)
```

Hmm, the second block load at `aligned_ptr + 16` could cross a page boundary — that's the whole problem. Let me use `palignr` instead:

Actually, the simplest correct slow path:
```go
// Slow path: byte-by-byte copy to stack buffer, then load as vector
// This path executes < 0.4% of the time, so it can be slow.
```

Or even simpler: on the slow path, just use the existing overread+mask approach (which is what we already have). The point of the optimization is that the fast path (99.6%) is a single `vmovdqu`.

- [ ] **Step 2: Simplify: fast path = raw load, slow path = existing mask**

```go
if b.spmdIsX86() && lanes == 16 {
    // Fast path: if not near page boundary, raw vmovdqu (1 instruction).
    // Slow path: fall through to existing overread+mask (5 instructions).
    // The caller handles garbage bytes via scalar bitmask trimming.
    ptrInt := b.CreatePtrToInt(dataPtr, b.uintptrType, "page.ptr")
    pageOff := b.CreateAnd(ptrInt, llvm.ConstInt(b.uintptrType, 0xFFF, false), "page.off")
    nearEnd := b.CreateICmp(llvm.IntUGT, pageOff, llvm.ConstInt(b.uintptrType, 0xFF0, false), "page.near")

    fastBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.fast")
    slowBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.slow")
    mergeBlock := b.ctx.AddBasicBlock(curBlock.Parent(), "rawload.merge")

    b.CreateCondBr(nearEnd, slowBlock, fastBlock)

    // Fast: raw load
    b.SetInsertPointAtEnd(fastBlock)
    fastLoad := b.CreateLoad(v16i8, dataPtr, "rawload")
    fastLoad.SetAlignment(1)
    b.CreateBr(mergeBlock)

    // Slow: existing overread+mask approach
    b.SetInsertPointAtEnd(slowBlock)
    slowLoad := ... // existing bitselect code
    b.CreateBr(mergeBlock)

    // Merge
    b.SetInsertPointAtEnd(mergeBlock)
    phi := b.CreatePHI(v16i8, "load.result")
    phi.AddIncoming([]llvm.Value{fastLoad, slowLoad}, ...)
    return phi
}
```

This is the cleanest approach — the slow path reuses the existing proven code.

- [ ] **Step 3: Run SPMD tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -count=1 -v 2>&1 | tail -20
```

- [ ] **Step 4: Build and test on both targets**

```bash
# WASM
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-wasm.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-wasm.wasm 2>&1 | head -15

# x86-64
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -llvm-features="+ssse3,+sse4.2,+avx2" -o /tmp/ipv4-x86.bin test/integration/spmd/ipv4-parser/main.go
/tmp/ipv4-x86.bin 2>&1 | head -15
```

- [ ] **Step 5: Verify the x86 assembly has a raw vmovdqu on the fast path**

```bash
objdump -d /tmp/ipv4-x86.bin | awk '/parseIPv4Inner>:/,/^$/' | grep -cE 'vpmaxub|vpbroadcastb.*0x200'
```

Target: 0 (the masking instructions should be gone on the fast path).

- [ ] **Step 6: Benchmark**

```bash
/tmp/ipv4-x86.bin 2>&1 | tail -15
```

- [ ] **Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/spmd_x86.go
git commit -m "spmd: x86 page-safe raw load in createSPMDVectorFromMemory (fast path = 1 vmovdqu)"
```

---

## Expected Results

| Metric | Before | After |
|--------|--------|-------|
| P1 on x86 fast path | 5 SIMD instrs (mask) | **1 instruction** (vmovdqu) |
| P1 on x86 slow path | same | 5 SIMD instrs (mask fallback) |
| P2 `\|\| c == 0` | 2 SIMD instrs | **0** (removed) |
| Scalar trim overhead | 0 | 3 scalar instrs (AND + compare) |
| Net hot-path saving | — | **~4 instructions** |
| x86 total (est.) | 184 | **~180** |

The saving is modest in instruction count but significant in latency — the 5 SIMD masking instructions were on the critical path (they gated all subsequent P2/P3/P4 operations). Removing them from the fast path eliminates ~2-3 cycles of vector dependency chain.
