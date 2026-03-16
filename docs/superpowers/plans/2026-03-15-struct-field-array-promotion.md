# Stack Array to Vector Load Promotion

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a `go for i, v := range arr` loop iterates over a `[N]T` stack alloc that fits in v128 (N*sizeof(T) <= 16), load the alloc as a single `v128.load` instead of N individual byte loads + N `replace_lane` ops.

**Architecture:** Purely a TinyGo-level optimization. No SSA changes, no new SSA instructions. The alloc is on the stack; all scalar stores (init + any overrides) have already executed by the time the loop body runs. A single `v128.load` from the alloc's stack address reads the correct final values directly. This replaces 60-80+ WASM instructions (N loads + N replace_lanes) with 1-2 instructions.

**Tech Stack:** TinyGo LLVM codegen, WASM SIMD128

---

## Why this is so simple

For code like:
```go
shuffleMask := entry.shuffleMask  // Store(alloc, *fieldPtr) → scalar copy to stack
shuffleMask[12] = byte(sf3)       // Store(IndexAddr(alloc, 12), sf3) → byte store to stack
shuffleMask[13] = byte(sf3 + 1)   // same
shuffleMask[14] = byte(sf3 + 2)   // same
go for i, m := range shuffleMask { // rangeindex over [16]byte alloc
    shuffled[i] = digits[m]
}
```

TinyGo processes instructions in DomPreorder. By the time it reaches the `go for` body:
1. The init store has written 16 bytes to the stack alloc (scalar `memory.copy` or `v128.store`)
2. The override stores have written bytes 12-14 to the stack alloc (3x `i32.store8`)
3. The stack alloc now has the correct final 16-byte value

So the loop body just needs `v128.load(alloc_ptr)` to get all 16 bytes as a `<16 x i8>` vector. No tracing of init patterns, no override detection, no CFG analysis. The stack is the "merge point" for all the scalar writes.

Currently TinyGo builds the vector byte-by-byte: `local.get $alloc + i32.load8_u offset=K + i8x16.replace_lane K` repeated 16 times = ~80 instructions. After this change: 2 instructions.

---

## File Map

| File | Change |
|------|--------|
| `tinygo/compiler/spmd.go` | Add `spmdLoadAllocAsVector` helper; call it from rangeindex loop processing |
| `tinygo/compiler/spmd.go` | Modify `Index(arrayLoad, varyingIdx)` handling to use pre-loaded vector |

No x-tools-spmd changes. No Go frontend changes. No new SSA instructions.

---

## Chunk 1: TinyGo Rangeindex Vector Load

### Task 1: Identify the current byte-by-byte vector build path

**Files:**
- Read: `tinygo/compiler/spmd.go` — find where `*ssa.Index` with a varying index inside a SPMD loop body is handled. This is where the N replace_lane ops currently get generated.

- [ ] **Step 1: Locate the Index instruction handler**

Search `tinygo/compiler/spmd.go` and `tinygo/compiler/compiler.go` for how `*ssa.Index` is handled when the index is varying (SPMD context). Look for:
- `case *ssa.Index:` in the instruction dispatch
- Any special SPMD handling for Index with varying index
- How the array value is loaded (`UnOp{MUL, alloc}` → LLVM aggregate load)
- How individual elements are extracted and collected into a vector

The current pattern likely involves:
1. Loading the `[N]T` from the alloc as an LLVM aggregate `[N x T]`
2. For each lane: `extractvalue [N x T], laneIdx` (or GEP + load)
3. Building the vector via `insertelement <N x T>, val, laneIdx`

Report the exact function names, line numbers, and code paths involved.

- [ ] **Step 2: Write a benchmark baseline**

Before any changes, compile the IPv4 parser and count the replace_lane instructions in `parseIPv4Inner`:

```bash
cd /home/cedric/work/SPMD
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-before.wasm test/integration/spmd/ipv4-parser/main.go
wasm2wat /tmp/ipv4-before.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c 'replace_lane'
```

Record the count (expected: ~29 replace_lane instructions from the shuffleMask construction).

- [ ] **Step 3: Commit baseline count in a comment (no code changes yet)**

---

### Task 2: Implement `v128.load` for rangeindex over stack alloc

**Files:**
- Modify: `tinygo/compiler/spmd.go`

**The optimization:**

When TinyGo encounters a `*ssa.Index` instruction inside a SPMD rangeindex loop body, where:
- The array source (`Index.X`) is a `*ssa.UnOp{MUL}` loading from an `*ssa.Alloc`
- The alloc type is `*[N]T` where `N * sizeof(T) <= 16` (fits in v128)
- The index is the loop's IterPhi or IncrBinOp (i.e., lane-sequential access)

Then instead of loading each element individually and building the vector via N `insertelement` ops:

1. Get the LLVM pointer to the alloc (already available — it's a stack alloca)
2. Compute the target vector type: `llvm.VectorType(elemLLVM, N)`
3. Emit `v128.load`: `result = b.CreateLoad(vecType, allocPtr, "spmd.arr2vec")`
4. Return `result` directly as the Varying[T] value — since the loop iterates from 0 to N-1 with one lane per element, the vector IS the result

**Cache the loaded vector:** The same `arrayLoad = *alloc` may be referenced by multiple `Index` instructions in the loop body (if the loop variable `m` is used in multiple expressions). Cache the v128.load result keyed by the `*ssa.UnOp` (arrayLoad), so it's only emitted once.

**Key constraint:** The `v128.load` must be emitted INSIDE the loop body block (not before the loop), because in peeled loops the alloc might be modified between iterations. But for rangeindex (non-peeled), this doesn't apply — a single load before the loop body would also work. Use whatever is simplest.

- [ ] **Step 1: Write a TinyGo LLVM test for the optimization**

Add a test to `tinygo/compiler/spmd_llvm_test.go` that verifies a rangeindex loop over a `[16]byte` alloc generates a `v128.load` instead of 16 `insertelement` ops:

```go
func TestSPMDRangeIndexAllocVectorLoad(t *testing.T) {
    // Source: function with [16]byte alloc, const-index stores, rangeindex loop
    // Expected: v128.load appears in LLVM IR, no series of 16 extractvalue+insertelement
}
```

The test should compile a small SPMD program and inspect the generated LLVM IR for `v128.load` or `load <16 x i8>` patterns.

- [ ] **Step 2: Run test — expect FAIL (optimization not yet implemented)**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMDRangeIndexAllocVectorLoad -v
```

- [ ] **Step 3: Implement the optimization**

In the appropriate handler in `tinygo/compiler/spmd.go`:

```go
// spmdLoadAllocAsVector checks if an array load from a stack alloc can be
// promoted to a single v128.load. Returns the vector value if so, or a
// zero llvm.Value if the pattern doesn't match.
func (b *builder) spmdLoadAllocAsVector(arrayLoad *ssa.UnOp) llvm.Value {
    // Check: arrayLoad is UnOp{MUL} (pointer dereference)
    if arrayLoad.Op != token.MUL {
        return llvm.Value{}
    }
    // Check: source is an Alloc
    alloc, ok := arrayLoad.X.(*ssa.Alloc)
    if !ok {
        return llvm.Value{}
    }
    // Check: alloc type is *[N]T
    ptrType, ok := alloc.Type().Underlying().(*types.Pointer)
    if !ok {
        return llvm.Value{}
    }
    arrayType, ok := ptrType.Elem().Underlying().(*types.Array)
    if !ok {
        return llvm.Value{}
    }
    // Check: fits in v128
    elemLLVM := b.getLLVMType(arrayType.Elem())
    elemSize := b.targetData.TypeAllocSize(elemLLVM)
    arrayLen := int(arrayType.Len())
    if arrayLen * int(elemSize) > 16 {
        return llvm.Value{}
    }

    // Get LLVM pointer to the alloc
    allocPtr := b.getValue(alloc, getPos(arrayLoad))

    // Load as vector type
    vecType := llvm.VectorType(elemLLVM, arrayLen)
    result := b.CreateLoad(vecType, allocPtr, "spmd.arr2vec")
    result.SetAlignment(1) // WASM: any alignment is valid
    return result
}
```

Then in the Index instruction handler, when inside a SPMD loop and the index is the IterPhi/IncrBinOp:

```go
// Before building the vector byte-by-byte, try the v128.load optimization:
if vec := b.spmdLoadAllocAsVector(arrayLoad); !vec.IsNil() {
    return vec // the vector IS the result for lane-sequential access
}
// ... existing byte-by-byte fallback ...
```

- [ ] **Step 4: Run test — expect PASS**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMDRangeIndexAllocVectorLoad -v
```

- [ ] **Step 5: Run full SPMD test suite**

```bash
cd /home/cedric/work/SPMD/tinygo && /home/cedric/work/SPMD/go/bin/go test ./compiler/ -run TestSPMD -v 2>&1 | tail -20
```

Expected: all existing tests pass — no regressions.

- [ ] **Step 6: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go compiler/spmd_llvm_test.go
git commit -m "spmd: promote rangeindex over stack [N]T alloc to v128.load"
```

---

## Chunk 2: End-to-End Verification

### Task 3: Verify IPv4 parser correctness and instruction count

- [ ] **Step 1: Rebuild TinyGo and compile IPv4 parser**

```bash
cd /home/cedric/work/SPMD
make build-tinygo GO=/home/cedric/work/SPMD/go/bin/go
rm -rf ~/.cache/tinygo
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/ipv4-promoted.wasm test/integration/spmd/ipv4-parser/main.go
```

- [ ] **Step 2: Verify correctness**

```bash
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/ipv4-promoted.wasm 2>&1 | head -15
```

Expected: all 10 test cases correct, "Correctness: SPMD and scalar results match."

- [ ] **Step 3: Count replace_lane instructions**

```bash
wasm2wat /tmp/ipv4-promoted.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c 'replace_lane'
```

Target: significant reduction from baseline (~29 → ~6 or fewer). The shuffleMask construction should now be 1 `v128.load` + 3 `replace_lane` (for the l3 overrides) instead of 16 `replace_lane` for the initial load.

- [ ] **Step 4: Count total instructions**

```bash
wasm2wat /tmp/ipv4-promoted.wasm | awk '/$main.parseIPv4Inner/,/^\s*\)/' | grep -c '^\s\+'
```

Target: ~230 instructions (down from 312 before this optimization). Phase 4 target: ~50 instructions (down from 137).

- [ ] **Step 5: Run full E2E suite**

```bash
cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -10
```

Expected: at least 40 compile pass, 32 run pass — no regressions.

- [ ] **Step 6: Commit (if any adjustments were needed)**

---

## Why this works for all cases

| Pattern | Stack state at loop entry | v128.load result |
|---------|--------------------------|------------------|
| `copy(arr[:], s)` (already promoted) | N/A — alloc removed by SSA pass | N/A |
| `arr := entry.field` (struct field copy, no overrides) | 16 bytes from field | Correct |
| `arr := entry.field; arr[k] = v` (field + overrides) | 16 bytes from field, then bytes k patched | Correct |
| `arr := [16]byte{...}` (literal init) | 16 bytes from literal | Correct |
| `for j := range 16 { arr[j] = compute(j) }` (loop init) | 16 computed bytes | Correct |

The stack is the universal "merge point." All scalar stores converge there. The v128.load reads whatever is on the stack — regardless of how it got there.

## Edge Cases

| Case | Handling |
|------|---------|
| Array larger than 16 bytes | Not promoted (size check fails) |
| Array element size > 1 byte (e.g., `[4]int32`) | Works — `VectorType(i32, 4)` loads 16 bytes |
| Non-contiguous stack alloc (heap-escaped) | `*ssa.Alloc` with `Heap: true` — pointer is to heap, v128.load still works since the alloc is materialized in memory |
| Multiple `go for` loops over same alloc | Each loop gets its own v128.load (cached per UnOp) |
| Alloc modified inside the loop body | Not an issue for rangeindex — the loop only READS the array; writes go to `shuffled[i]`, a different alloc |
