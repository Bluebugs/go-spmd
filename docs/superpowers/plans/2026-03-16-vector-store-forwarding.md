# Vector Store Forwarding for Partially-Modified Allocas

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a `[N]byte` alloca is initialized with a `<N x i8>` vector store, partially modified by scalar byte stores at constant indices, and then reloaded as `<N x i8>`, emit `insertelement` ops directly in LLVM IR instead of relying on LLVM's store forwarding (which decomposes into `v128.load8_splat + 15× v128.load8_lane` on WASM).

**Architecture:** Track a "shadow vector register" per promoted alloca in TinyGo's builder. When `spmdPromoteByteArrayCopyToVector` stores a `<N x i8>` to an alloca, record the vector value. When scalar stores modify bytes at constant indices, emit `insertelement` alongside the scalar store to maintain the shadow. At merge blocks (switch/if), insert LLVM phis for the shadow. When the identity load fires in `spmdVectorIndexArray`, return the shadow vector directly instead of emitting `v128.load(alloca)`. The alloca still receives all stores for correctness.

**Tech Stack:** TinyGo LLVM codegen (Go + LLVM C API bindings), WASM SIMD128

---

## Background: the LLVM IR that LLVM fails to optimize

After `spmdPromoteByteArrayCopyToVector` and the identity load path, TinyGo emits:

```llvm
dispatch_block:
  %vec = load <16 x i8>, ptr %src_field, align 1      ; promoted vector load
  store <16 x i8> %vec, ptr %shuffleMask               ; vector store to alloca
  switch i32 %l3, label %case1 [i32 3, label %case3  i32 2, label %case2]

case3:
  store i8 %sf3,   ptr getelementptr(%shuffleMask, 0, 12)
  store i8 %sf3p1, ptr getelementptr(%shuffleMask, 0, 13)
  store i8 %sf3p2, ptr getelementptr(%shuffleMask, 0, 14)
  br label %merge

case2:
  store i8 -1,     ptr getelementptr(%shuffleMask, 0, 12)
  store i8 %sf3,   ptr getelementptr(%shuffleMask, 0, 13)
  store i8 %sf3p1, ptr getelementptr(%shuffleMask, 0, 14)
  br label %merge

case1:
  store i8 -1,     ptr getelementptr(%shuffleMask, 0, 12)
  store i8 -1,     ptr getelementptr(%shuffleMask, 0, 13)
  store i8 %sf3,   ptr getelementptr(%shuffleMask, 0, 14)
  br label %merge

merge:
  %result = load <16 x i8>, ptr %shuffleMask           ; identity load
```

LLVM's GVN/DSE cannot forward the vector through cross-block scalar clobbers. It decomposes `%result` into `v128.load8_splat + 15× v128.load8_lane` = **49 WASM instructions**.

**After this optimization**, TinyGo would emit:

```llvm
dispatch_block:
  %vec = load <16 x i8>, ptr %src_field, align 1
  store <16 x i8> %vec, ptr %shuffleMask               ; still store for correctness

case3:
  store i8 %sf3, ptr gep(...)                           ; still store for correctness
  %v3a = insertelement <16 x i8> %vec, i8 %sf3, i32 12
  store i8 %sf3p1, ptr gep(...)
  %v3b = insertelement <16 x i8> %v3a, i8 %sf3p1, i32 13
  store i8 %sf3p2, ptr gep(...)
  %v3c = insertelement <16 x i8> %v3b, i8 %sf3p2, i32 14
  br label %merge

case2:
  ; ... similar with %v2c ...

case1:
  ; ... similar with %v1c ...

merge:
  %shadow = phi <16 x i8> [%v3c, %case3], [%v2c, %case2], [%v1c, %case1]
  ; identity load returns %shadow directly — no v128.load needed
```

This produces: **1 `v128.load` (from source) + 3 `replace_lane` (in chosen case) + 1 phi** = ~5 WASM instructions instead of 49.

---

## Implementation Status: COMPLETED (2026-03-18)

All tasks completed. Additionally discovered and fixed a deeper root cause:

**Root cause discovered:** wasm-ld's `--lto-O2` SROA decomposes a whole-struct store (`store %lemireEntry, ptr %structAlloc`) into 16 individual `store i8`, then converts the `load <16 x i8>` from the struct alloca into an `insertelement` chain → `v128.load8_splat + 15× v128.load8_lane`. The shadow forwarding bypasses the identity-load alloca but the INITIAL source load (`spmd.alloc.vec.copy`) also goes through a struct alloca that gets SROA'd.

**Additional fix (Pattern A alloca-bypass):** When `spmdPromoteByteArrayCopyToVector` Pattern A detects `FieldAddr(structAlloc, fieldIdx)` where `structAlloc` has a `Store(structAlloc, *srcStructPtr)` populator, it bypasses the struct alloca entirely — emitting `GEP(srcStructPtr, 0, fieldIdx)` as the load source. This gives LLVM a direct `load <16 x i8>, ptr %table_entry, align 2` that survives all LLVM passes as a single `v128.load`.

**Also added Pattern B (Field path):** Handles `Store(alloc, Field(structVal, idx))` where structVal = `*structPtr`. Computes field GEP from the struct pointer and loads as vector.

**Final results:**
- `load8_lane` in parseIPv4Inner: **0** (was 15)
- `replace_lane`: **10** (was 29 at session start)
- Total function lines: **277** (was 335)
- `v128.load`: **2** (was 1 — now loads shuffleMask directly from table)
- E2E: **41/41 run-pass**, 0 regressions
- 4+5 new LLVM unit tests (alloca-bypass + Field pattern)

---

## File Map

| File | Change |
|------|--------|
| `tinygo/compiler/spmd.go` | Add shadow vector tracking: `spmdVecShadow` map, insert-at-store logic, phi-at-merge logic, identity-load bypass |
| `tinygo/compiler/compiler.go` | Intercept `Store(IndexAddr(tracked_alloc, const), val)` to emit `insertelement` alongside scalar store |
| `tinygo/compiler/spmd_llvm_test.go` | Test: verify `insertelement` appears instead of `v128.load8_lane` chain |

---

## Chunk 1: Shadow Vector Tracking Infrastructure

### Task 1: Add shadow vector data structures

**Files:**
- Modify: `tinygo/compiler/spmd.go`

- [ ] **Step 1: Add the shadow vector tracking fields**

Add to `spmd.go` near the existing `spmdLoopState` and builder fields:

```go
// spmdVecShadow tracks the "register-based" vector value for allocas that were
// initialized by spmdPromoteByteArrayCopyToVector. When scalar byte stores
// modify individual elements at constant indices, the shadow is updated with
// insertelement ops. At identity-load time (spmdVectorIndexArray), the shadow
// value is returned directly, bypassing the alloca reload that LLVM would
// decompose into v128.load8_splat + N× v128.load8_lane.
//
// Key: SSA *Alloc value. Value: current LLVM <N x i8> vector.
// Reset/phi'd at block boundaries when predecessors have different values.
type spmdVecShadowState struct {
    current  map[*ssa.Alloc]llvm.Value            // current shadow per alloc
    blockOut map[llvm.Value]map[*ssa.Alloc]llvm.Value // outgoing shadow per LLVM block
}
```

- [ ] **Step 2: Initialize in `spmdPromoteByteArrayCopyToVector`**

At the end of `spmdPromoteByteArrayCopyToVector` (after `b.CreateLoad`), before the `return load` line, add:

```go
    // Record the shadow vector for this alloc so that subsequent const-index
    // scalar stores can maintain an up-to-date register-based vector,
    // bypassing the alloca round-trip at identity-load time.
    if alloc, ok := instr.Addr.(*ssa.Alloc); ok {
        if b.spmdVecShadow == nil {
            b.spmdVecShadow = &spmdVecShadowState{
                current:  make(map[*ssa.Alloc]llvm.Value),
                blockOut: make(map[llvm.Value]map[*ssa.Alloc]llvm.Value),
            }
        }
        b.spmdVecShadow.current[alloc] = load
    }
```

- [ ] **Step 3: Save outgoing shadow at block end**

Find where TinyGo transitions between basic blocks (the block processing loop in the main compilation function). At the END of each block's instruction processing, before moving to the next block, save the shadow state:

```go
    // Save shadow vector state at block exit for phi insertion at merge blocks.
    if b.spmdVecShadow != nil && len(b.spmdVecShadow.current) > 0 {
        out := make(map[*ssa.Alloc]llvm.Value, len(b.spmdVecShadow.current))
        for k, v := range b.spmdVecShadow.current {
            out[k] = v
        }
        b.spmdVecShadow.blockOut[llvmBlock] = out
    }
```

- [ ] **Step 4: Restore/phi shadow at block entry**

At the START of each block's processing, before processing instructions, restore the shadow from predecessors:

```go
    if b.spmdVecShadow != nil && len(b.spmdVecShadow.current) > 0 {
        b.spmdVecShadowBlockEntry(llvmBlock, ssaBlock)
    }
```

Where `spmdVecShadowBlockEntry` is:

```go
// spmdVecShadowBlockEntry restores the shadow vector state at block entry.
// For single-predecessor blocks, inherits the predecessor's outgoing state.
// For merge blocks (multiple predecessors), inserts LLVM phi nodes.
func (b *builder) spmdVecShadowBlockEntry(llvmBlock llvm.Value, ssaBlock *ssa.BasicBlock) {
    if b.spmdVecShadow == nil {
        return
    }
    preds := ssaBlock.Preds
    if len(preds) == 0 {
        return
    }

    // Collect all tracked allocs from any predecessor.
    allocs := make(map[*ssa.Alloc]bool)
    for _, pred := range preds {
        predLLVM := b.blockEntries[pred]
        if out, ok := b.spmdVecShadow.blockOut[predLLVM]; ok {
            for alloc := range out {
                allocs[alloc] = true
            }
        }
    }

    for alloc := range allocs {
        // Gather incoming values from each predecessor.
        var vals []llvm.Value
        var blocks []llvm.Value
        allSame := true
        var first llvm.Value
        for _, pred := range preds {
            predLLVM := b.blockEntries[pred]
            var v llvm.Value
            if out, ok := b.spmdVecShadow.blockOut[predLLVM]; ok {
                v = out[alloc]
            }
            if v.IsNil() {
                // Predecessor doesn't have a shadow → alloc was not modified
                // in that path. This shouldn't happen for valid patterns, but
                // if it does, drop tracking for this alloc.
                delete(b.spmdVecShadow.current, alloc)
                goto nextAlloc
            }
            vals = append(vals, v)
            blocks = append(blocks, predLLVM)
            if first.IsNil() {
                first = v
            } else if v.C != first.C {
                allSame = false
            }
        }

        if allSame {
            b.spmdVecShadow.current[alloc] = first
        } else {
            // Insert phi at the start of the merge block.
            savedIP := b.GetInsertBlock()
            // Position at the start of llvmBlock (before any other instructions).
            firstInstr := llvm.FirstInstruction(llvmBlock)
            if firstInstr.IsNil() {
                b.SetInsertPointAtEnd(llvmBlock)
            } else {
                b.SetInsertPointBefore(firstInstr)
            }
            vecType := vals[0].Type()
            phi := b.CreatePHI(vecType, "spmd.shadow.phi")
            phi.AddIncoming(vals, blocks)
            b.spmdVecShadow.current[alloc] = phi
            // Restore insert point.
            b.SetInsertPointAtEnd(savedIP)
        }
    nextAlloc:
    }
}
```

- [ ] **Step 5: Run existing tests — no regressions**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "spmd: add shadow vector tracking for promoted byte array allocas"
```

---

### Task 2: Emit insertelement on const-index scalar stores

**Files:**
- Modify: `tinygo/compiler/compiler.go` (Store handler)

- [ ] **Step 1: Intercept Store(IndexAddr(tracked_alloc, constIdx), val)**

In the `case *ssa.Store:` handler in `compiler.go`, AFTER the existing `spmdPromoteByteArrayCopyToVector` check and AFTER `b.CreateStore(llvmVal, llvmAddr)`, add:

```go
    // SPMD: if this store writes a scalar byte to a const-index position
    // of a shadow-tracked alloca, also update the shadow vector with
    // insertelement. This keeps the register-based vector in sync with
    // the alloca, so the identity-load path can return the shadow directly
    // instead of loading from the alloca (which LLVM would decompose).
    b.spmdVecShadowUpdate(instr)
```

Where `spmdVecShadowUpdate` is in `spmd.go`:

```go
// spmdVecShadowUpdate checks if a Store instruction writes to a const-index
// position of a shadow-tracked alloca and updates the shadow vector with
// insertelement if so.
func (b *builder) spmdVecShadowUpdate(instr *ssa.Store) {
    if b.spmdVecShadow == nil || len(b.spmdVecShadow.current) == 0 {
        return
    }
    // Store.Addr must be IndexAddr(alloc, constIdx).
    ia, ok := instr.Addr.(*ssa.IndexAddr)
    if !ok {
        return
    }
    alloc, ok := ia.X.(*ssa.Alloc)
    if !ok {
        return
    }
    shadow, ok := b.spmdVecShadow.current[alloc]
    if !ok {
        return
    }
    // Index must be a constant.
    constIdx, ok := ia.Index.(*ssa.Const)
    if !ok {
        return
    }
    idxVal, ok := constant.Int64Val(constIdx.Value)
    if !ok || idxVal < 0 {
        return
    }

    // Get the stored value as LLVM scalar.
    val := b.getValue(instr.Val, getPos(instr))

    // Narrow to i8 if needed (the shadow vector is <N x i8>).
    elemType := shadow.Type().ElementType()
    if val.Type() != elemType {
        val = b.CreateTrunc(val, elemType, "spmd.shadow.trunc")
    }

    idx := llvm.ConstInt(b.ctx.Int32Type(), uint64(idxVal), false)
    newShadow := b.CreateInsertElement(shadow, val, idx, "spmd.shadow.insert")
    b.spmdVecShadow.current[alloc] = newShadow
}
```

Note: import `go/constant` if not already imported in `spmd.go` (it likely is).

- [ ] **Step 2: Run existing tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/compiler.go
git commit -m "spmd: emit insertelement alongside const-index stores to shadow-tracked allocas"
```

---

### Task 3: Bypass alloca reload at identity-load time

**Files:**
- Modify: `tinygo/compiler/spmd.go` (in `spmdVectorIndexArray`)

- [ ] **Step 1: Return shadow vector instead of v128.load(alloca)**

In `spmdVectorIndexArray`, at the identity swizzle elimination path (line ~4569), where it currently emits `v128.load(srcPtr)`, add a shadow check FIRST:

```go
    if int64(xType.Len()) == int64(laneCount) && b.spmdIsLoopLaneIndex(index, expr.Index) {
        if unop, ok := expr.X.(*ssa.UnOp); ok && unop.Op == token.MUL {
            // Check if the alloca has a shadow vector (promoted + possibly patched).
            if alloc, ok := unop.X.(*ssa.Alloc); ok && b.spmdVecShadow != nil {
                if shadow, ok := b.spmdVecShadow.current[alloc]; ok {
                    return shadow, nil
                }
            }

            // Existing path: load from alloca.
            srcPtr := b.getValue(unop.X, getPos(expr))
            if laneCount == 16 {
                v16i8 := llvm.VectorType(b.ctx.Int8Type(), 16)
                load := b.CreateLoad(v16i8, srcPtr, "spmd.identity.load")
                load.SetAlignment(1)
                return load, nil
            }
            // ...
```

- [ ] **Step 2: Write test**

Add to `tinygo/compiler/spmd_llvm_test.go`:

```go
func TestSPMDVecShadowStoreForwarding(t *testing.T) {
    // Build SPMD program: struct with [16]byte field, copy to local,
    // patch 3 bytes, range over modified local.
    // Verify: LLVM IR contains "insertelement" for the patches and
    // the identity load uses the shadow (no v128.load8_lane chain).
    // Check: no "v128.load8_lane" in the WASM output for this function.
}
```

The test should compile a minimal SPMD program similar to the IPv4 parser's shuffleMask pattern and verify the generated LLVM IR contains `insertelement` instructions and does NOT contain a chain of per-byte loads.

- [ ] **Step 3: Run tests**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run "TestSPMD" -v 2>&1 | tail -20
```

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "spmd: bypass alloca reload via shadow vector at identity-load time"
```

---

## Chunk 2: End-to-End Verification

### Task 4: Verify IPv4 parser

- [ ] **Step 1: Build and run**

```bash
cd /home/cedric/work/SPMD
make build-tinygo GO=/home/cedric/work/SPMD/go/bin/go
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-shadow.wasm test/integration/spmd/ipv4-parser/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-shadow.wasm 2>&1 | head -15
```

Expected: all 10 test cases correct, "Correctness: SPMD and scalar results match."

- [ ] **Step 2: Count instructions**

```bash
wasm2wat /tmp/ipv4-shadow.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c 'replace_lane'
wasm2wat /tmp/ipv4-shadow.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c 'load8_lane'
wasm2wat /tmp/ipv4-shadow.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c '^\s\+'
```

Targets:
- `load8_lane`: **0** (eliminated by shadow forwarding)
- `replace_lane`: ~8 (3 from l3 patch + 3 from flens vector + 2 from other)
- Total instructions: **~260** (down from 309, saving ~49 from Phase 4c)

- [ ] **Step 3: Run E2E suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -10
```

Expected: 41 compile pass, 41 run pass — no regressions.

- [ ] **Step 4: Commit**

---

## Edge Cases

| Case | Handling |
|------|---------|
| Non-const index store `shuffleMask[i] = v` (varying) | `spmdVecShadowUpdate` skips (requires `*ssa.Const` index) — falls back to alloca path |
| Multiple tracked allocas in same function | Each alloc tracked independently in the map |
| No scalar patches (pure copy + range) | Shadow vector is the initial vec, returned directly. Saves 1 alloca load. |
| Store to non-tracked alloca | `spmdVecShadowUpdate` skips (alloc not in map) |
| Block with no predecessors that modified shadow | Shadow entry removed — falls back to alloca load |
| Alloca read by non-identity path (e.g., `shuffleMask[5]` scalar read) | Scalar reads still go through the alloca (which has correct values). Shadow only used by identity load. |
| Switch with default block fallthrough | Default block is just another predecessor for the phi merge |
