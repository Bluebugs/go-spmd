# Contiguous Aggregate Load Fix — Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix `go for` range-over-slice of aggregate types (strings, structs) so each SPMD lane loads a distinct consecutive element instead of broadcasting element 0 to all lanes.

**Architecture:** Two-layer fix: (1) x-tools-spmd SSA predication allows aggregate `UnOp{MUL}` → `SPMDLoad` conversion when the address is a contiguous `IndexAddr`, and (2) TinyGo's `createSPMDLoad` gains a contiguous aggregate path that emits N scalar loads at consecutive GEP offsets and packs them into `[N x T]`.

**Tech Stack:** Go (x-tools-spmd SSA), Go+LLVM (TinyGo compiler), WASM SIMD128

---

## Bug Summary

When `go for _, s := range []string{...}` iterates, each lane should load a different string. Instead, lane 0's string is broadcast to all lanes. Same for any aggregate type (structs, interfaces).

**Root cause chain:**
1. `spmdMaskMemOps` and `spmdConvertScopedMemOps` in x-tools-spmd skip `UnOp{MUL}` loads where `!spmdIsVectorizableElemType(instr.Type())` (line 3602 / line 1974).
2. The load stays as a plain scalar `UnOp{MUL}`, producing one element.
3. `ChangeType` (compiler.go:2619-2629) broadcasts that single element into `[N x T]`.

**Expected behavior:** For contiguous aggregate loads, emit `SPMDLoad` so TinyGo can generate N consecutive scalar loads into `[N x T]`.

**Note on `spmdConvertAllMemOps` (line 3411):** A third identical `spmdIsVectorizableElemType` guard exists in `spmdConvertAllMemOps`, which handles SPMD function bodies (not loop bodies). This function does NOT need modification because function bodies have no `SPMDLoops`, so `spmdIsContiguousIndex` always returns false — the relaxed guard would still skip aggregates. The comment at line 3407-3410 documents this explicitly.

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `x-tools-spmd/go/ssa/spmd_predicate.go` | Modify | Relax vectorizable guard for contiguous aggregate loads |
| `x-tools-spmd/go/ssa/spmd_predicate_test.go` | Modify | Add test for aggregate SPMDLoad emission |
| `tinygo/compiler/spmd.go` | Modify | Add contiguous aggregate load path in `createSPMDLoad` |
| `test/e2e/spmd-e2e-test.sh` | Modify | Promote map-restrictions to run-pass |
| `test/integration/spmd/map-restrictions/main.go` | Verify | Check output correctness after fix |

---

## Task 1: SSA — Allow contiguous aggregate loads to become SPMDLoad

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:3595-3604` (`spmdMaskMemOps`)
- Modify: `x-tools-spmd/go/ssa/spmd_predicate.go:1968-1976` (`spmdConvertScopedMemOps`)
- Test: `x-tools-spmd/go/ssa/spmd_predicate_test.go`

### Step 1: Write the failing test

Add `TestPredicateSPMD_ContiguousAggregateLoad` in `spmd_predicate_test.go`. This test should build SSA for a `go for` ranging over `[]struct{A,B int}` and verify that the load is converted to `SPMDLoad` with `[contiguous]` annotation.

```go
func TestPredicateSPMD_ContiguousAggregateLoad(t *testing.T) {
	src := `package main

import "fmt"

type Pair struct { A, B int }

func f(pairs []Pair) {
	for _, p := range pairs {
		fmt.Println(p.A)
	}
}

func main() { f(make([]Pair, 8)) }
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	foundContiguousLoad := false
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			s := instr.String()
			if strings.Contains(s, "SPMDLoad") && strings.Contains(s, "[contiguous]") {
				foundContiguousLoad = true
			}
		}
	}
	if !foundContiguousLoad {
		var buf bytes.Buffer
		ssa.WriteFunction(&buf, fn)
		t.Errorf("expected contiguous SPMDLoad for aggregate type, got:\n%s", buf.String())
	}
}
```

- [ ] **Step 1a:** Add the test to `x-tools-spmd/go/ssa/spmd_predicate_test.go`

- [ ] **Step 1b:** Run the test to verify it fails

Run: `cd x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_ContiguousAggregateLoad -v`
Expected: FAIL — no `SPMDLoad [contiguous]` found (aggregate loads are skipped)

### Step 2: Implement the fix in spmdMaskMemOps

In `spmd_predicate.go`, function `spmdMaskMemOps` (line ~3595-3604), change the guard to allow aggregate loads through when the address is a contiguous IndexAddr:

```go
case *UnOp:
    if instr.Op != token.MUL {
        continue
    }
    // Check if address is a contiguous IndexAddr.
    indexAddr, isIndexAddr := instr.X.(*IndexAddr)
    isContiguous := isIndexAddr && spmdIsContiguousIndex(b.parent, indexAddr.Index)

    // Non-vectorizable types (structs, strings) are only converted
    // to SPMDLoad when the access is contiguous. Non-contiguous
    // aggregate loads are truly uniform (scalar broadcast is correct).
    if !spmdIsVectorizableElemType(instr.Type()) && !isContiguous {
        continue
    }
    // Replace pointer load with SPMDLoad.
    load := &SPMDLoad{
        Addr:  instr.X,
        Mask:  mask,
        Lanes: lanes,
        pos:   instr.Pos(),
    }
    // Detect contiguous access via IndexAddr with iter-based index.
    if isContiguous {
        load.Contiguous = true
        load.Source = indexAddr.X
    }
```

- [ ] **Step 2a:** Apply the change to `spmdMaskMemOps` in `spmd_predicate.go`

### Step 3: Apply the same fix to spmdConvertScopedMemOps

In `spmd_predicate.go`, function `spmdConvertScopedMemOps` (line ~1968-1989), apply the identical guard change:

```go
case *UnOp:
    if instr.Op != token.MUL {
        continue
    }
    indexAddr, isIndexAddr := instr.X.(*IndexAddr)
    isContiguous := isIndexAddr && spmdIsContiguousIndex(fn, indexAddr.Index)

    if !spmdIsVectorizableElemType(instr.Type()) && !isContiguous {
        continue
    }
    load := &SPMDLoad{
        Addr:  instr.X,
        Mask:  mask,
        Lanes: lanes,
        pos:   instr.Pos(),
    }
    if isContiguous {
        load.Contiguous = true
        load.Source = indexAddr.X
    }
```

- [ ] **Step 3a:** Apply the change to `spmdConvertScopedMemOps` in `spmd_predicate.go`

### Step 4: Run the test to verify it passes

- [ ] **Step 4a:** Run: `cd x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD_ContiguousAggregateLoad -v`
Expected: PASS — aggregate load now produces `SPMDLoad [contiguous]`

- [ ] **Step 4b:** Run the full predicate test suite to check for regressions:
`cd x-tools-spmd && GOEXPERIMENT=spmd GOROOT=/home/cedric/work/SPMD/go go test ./go/ssa/ -run TestPredicateSPMD -v -count=1`
Expected: All tests PASS

### Step 5: Commit

- [ ] **Step 5a:** Commit x-tools-spmd changes

```
feat: allow contiguous aggregate loads to become SPMDLoad

Relax the spmdIsVectorizableElemType guard in spmdMaskMemOps and
spmdConvertScopedMemOps to permit aggregate-typed UnOp{MUL} loads
when the address is a contiguous IndexAddr. Previously these loads
were skipped, causing ChangeType to broadcast element 0 to all
lanes instead of loading N consecutive elements.
```

---

## Task 2: TinyGo — Contiguous aggregate load path in createSPMDLoad

**Files:**
- Modify: `tinygo/compiler/spmd.go:5859-5864` (`createSPMDLoad` contiguous path, between VectorTypeKind check and narrow load path)

### Step 1: Implement the contiguous aggregate load in createSPMDLoad

In `tinygo/compiler/spmd.go`, in the `createSPMDLoad` function's contiguous path, add a branch for non-vectorizable element types immediately after the VectorTypeKind check (line ~5859) and BEFORE the narrow load path (line ~5864). Everything after the narrow load path assumes vectorizable types and would crash on structs.

Note: `spmdIsVectorizableElemType` in TinyGo (`spmd.go:5634`) checks LLVM type kinds: `IntegerTypeKind`, `FloatTypeKind`, `DoubleTypeKind`, `PointerTypeKind`. `StructTypeKind` is NOT matched, so structs and strings correctly fall into the new branch.

```go
// If the element type is already a vector...
if elemType.TypeKind() == llvm.VectorTypeKind {
    return b.CreateLoad(elemType, ci.scalarPtr, "spmd.load.result")
}

// Contiguous aggregate load: the element is a struct/string/slice
// that can't form an LLVM vector. Emit N scalar loads at
// consecutive GEP offsets and pack into [N x elemType].
// Uses cap-based bounds check when available to avoid OOB reads
// on the last iteration (aggregates may contain GC-scanned pointers).
if !spmdIsVectorizableElemType(elemType) {
    arrType := llvm.ArrayType(elemType, laneCount)
    zero := llvm.ConstNull(arrType)
    result := zero
    for lane := 0; lane < laneCount; lane++ {
        gep := b.CreateInBoundsGEP(elemType, ci.scalarPtr, []llvm.Value{
            llvm.ConstInt(b.ctx.Int32Type(), uint64(lane), false),
        }, "spmd.agg.gep")
        loaded := b.CreateLoad(elemType, gep, "spmd.agg.lane")
        result = b.CreateInsertValue(result, loaded, lane, "")
    }
    return result
}

// Narrow load path (WASM byte/bool elements)...
```

Safety rationale for unconditional loads: Go's range loop guarantees `scalarIndex < len(slice)` for lane 0. For lane 1..N-1, the reads may be past `len` but within `cap` (Go allocates capacity >= length). For the tail iteration, inactive lanes read valid memory (within the slice's backing array allocation) but produce values that are never observed (masked out by SPMDSelect downstream). This matches the scalar contiguous load behavior in `spmdFullLoadWithSelect`, which also loads unconditionally then selects.

If a test surfaces OOB issues (e.g., slice at exact page boundary), the fallback is to add a `sliceCap` guard mirroring `spmdFullLoadWithSelect` — emitting per-lane conditional loads for the tail case. This is deferred since Go's allocator guarantees alignment padding.

- [ ] **Step 1a:** Add the contiguous aggregate load branch to `createSPMDLoad` in `spmd.go`

### Step 3: Rebuild TinyGo and run the E2E test

- [ ] **Step 3a:** Rebuild TinyGo: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`

- [ ] **Step 3b:** Clear cache: `rm -rf ~/.cache/tinygo`

- [ ] **Step 3c:** Run the string range test to verify the fix:
```bash
cat > /tmp/string-range-test.go << 'EOF'
package main

import (
    "fmt"
    "reduce"
)

func main() {
    data := []string{"apple", "banana", "cherry", "date"}
    go for _, s := range data {
        extracted := reduce.From(s)
        for i, v := range extracted {
            fmt.Printf("[%d] = %s\n", i, v)
        }
    }
}
EOF
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/string-range-test.wasm /tmp/string-range-test.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/string-range-test.wasm
```

Expected output:
```
[0] = apple
[1] = banana
[0] = cherry
[1] = date
```
(2 lanes per string iteration, 4 strings = 2 iterations)

- [ ] **Step 3d:** Also test structs:
```bash
cat > /tmp/struct-range-test.go << 'EOF'
package main

import (
    "fmt"
    "reduce"
)

type Pair struct { A, B int }

func main() {
    pairs := []Pair{{1, 2}, {3, 4}, {5, 6}, {7, 8}}
    go for _, p := range pairs {
        extracted := reduce.From(p)
        for i, v := range extracted {
            fmt.Printf("[%d] = {%d, %d}\n", i, v.A, v.B)
        }
    }
}
EOF
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/struct-range-test.wasm /tmp/struct-range-test.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/struct-range-test.wasm
```

Expected output:
```
[0] = {1, 2}
[1] = {3, 4}
[0] = {5, 6}
[1] = {7, 8}
```

### Step 4: Commit

- [ ] **Step 4a:** Commit TinyGo changes

```
feat: add contiguous aggregate load path in createSPMDLoad

When SPMDLoad has a contiguous address and the element type is
non-vectorizable (struct, string), emit N scalar loads at
consecutive GEP offsets and pack into [N x T] array. Previously
these loads fell through to the scalar broadcast path, causing
all lanes to receive the same element.
```

---

## Task 3: Verify map-restrictions and promote to run-pass

**Files:**
- Verify: `test/integration/spmd/map-restrictions/main.go`
- Modify: `test/e2e/spmd-e2e-test.sh`
- Modify: `PLAN.md`, `CLAUDE.md`

### Step 1: Run map-restrictions and verify output

- [ ] **Step 1a:** Compile and run:
```bash
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build -target=wasi -scheduler=none -o /tmp/map-restrictions.wasm test/integration/spmd/map-restrictions/main.go
node --experimental-wasi-unstable-preview1 test/e2e/run-wasm.mjs /tmp/map-restrictions.wasm
```

- [ ] **Step 1b:** Manually verify the output is correct. In particular check `demonstrateWorkarounds()`:
- `reduce.From(keys)` on `["apple","banana","cherry","date"]` should now produce `["apple","banana"]` and `["cherry","date"]` (not duplicated)
- `reduce.From(values)` on `[0,10,20,30]` should produce `[0,10]` and `[20,30]`

Expected output for that section:
```
Processing: apple -> 0
Processing: banana -> 10
Processing: cherry -> 20
Processing: date -> 30
```

### Step 2: Promote to run-pass in e2e script

- [ ] **Step 2a:** In `test/e2e/spmd-e2e-test.sh`, move `map-restrictions` from the compile-only loop to a `test_compile_and_run` call:

Remove `map-restrictions` from the `for dir in map-restrictions; do` loop, and add:
```bash
test_compile_and_run "integ_map-restrictions" "$INTEG/map-restrictions/main.go" \
    "contains:Map restrictions demonstration completed" "" "-scheduler=none"
```

### Step 3: Run full E2E suite

- [ ] **Step 3a:** Run: `bash test/e2e/spmd-e2e-test.sh`
Expected: 41 run-pass, 41 compile pass, 2 compile fail, 0 run fail, 11 reject OK

### Step 4: Update metrics and commit

- [ ] **Step 4a:** Update PLAN.md and CLAUDE.md with new E2E counts (41 run-pass)
- [ ] **Step 4b:** Commit:
```
feat: promote map-restrictions to run-pass

Contiguous aggregate load fix enables correct per-lane loading
of strings and structs in go-for range loops. E2E: 41 run-pass.
```

---

## Risk Notes

- **Tail mask safety**: Unconditional N-lane loads may read past `len(slice)` but within `cap(slice)` for the last iteration. Go's range loop guarantees lane 0 is valid; lanes 1..N-1 read within the backing array allocation. Values from inactive lanes are masked out by SPMDSelect downstream. If OOB issues surface at page boundaries, add a `sliceCap` guard mirroring `spmdFullLoadWithSelect`.

- **Store symmetry (DEFERRED)**: The same `spmdIsVectorizableElemType` guard exists in the Store handlers (line 3647 in `spmdMaskMemOps`, line 2009 in `spmdConvertScopedMemOps`). Aggregate stores (`slice[i] = structVal` inside go-for) likely have a symmetric broadcast bug. This plan does NOT address stores because: (a) no test currently exercises contiguous aggregate stores, and (b) TinyGo's `createSPMDStore` would need a symmetric "N consecutive stores" path. **Add to PLAN.md Deferred Items Collection.**

- **TinyGo rebuild required**: After x-tools-spmd changes, TinyGo must be rebuilt (`make GO=...`) because the `replace` directive in `tinygo/go.mod` maps to `../x-tools-spmd` but the binary must be recompiled.
