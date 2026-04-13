# `&varyingVar` Address-of Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `&varyingVar` to produce `Varying[*T]` — a per-lane pointer vector from taking the address of a varying variable.

**Architecture:** Remove the types2 rejection, add type transformation in both type checkers (`Varying[T]` → `Varying[*T]` for `&` result), and add TinyGo lowering to emit per-lane GEPs from the spilled alloca. The go/types side already lacks the rejection (dead code), so only the type transformation is needed there.

**Tech Stack:** Go (go/types, types2), TinyGo compiler

**Spec:** `docs/superpowers/specs/2026-04-12-varying-address-of-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### Go fork (`go/`)
- **Modify:** `src/cmd/compile/internal/types2/expr.go:148-160` — remove rejection, add type transform
- **Modify:** `src/go/types/expr.go:145-146` — add type transform for SPMD
- **Modify:** `src/go/types/pointer_ext_spmd.go:34-46` — update to allow and transform type
- **Modify:** `src/cmd/compile/internal/types2/pointer_ext_spmd.go:33-45` — update to allow
- **Modify:** `src/go/types/testdata/spmd/pointer_varying.go` — add test case

### TinyGo (`tinygo/`)
- **Modify:** `compiler/spmd.go` or `compiler/compiler.go` — per-lane GEP for varying alloca

### Integration test
- **Modify:** `test/integration/spmd/pointer-varying/main.go` — add E2E test

---

## Task 1: Type Checker — Allow `&Varying[T]` and Transform to `Varying[*T]`

**Files:**
- Modify: `go/src/cmd/compile/internal/types2/expr.go:148-160`
- Modify: `go/src/go/types/expr.go:145-146`
- Modify: `go/src/go/types/testdata/spmd/pointer_varying.go`

- [ ] **Step 1: Fix types2/expr.go — remove rejection, add type transform**

In `go/src/cmd/compile/internal/types2/expr.go`, replace lines 148-163 (the SPMD rejection + result type setting):

Current code:
```go
		// SPMD validation: cannot take address of varying variable directly (but allow indexed expressions)
		if buildcfg.Experiment.SPMD {
			if spmdType, ok := x.typ().(*SPMDType); ok && spmdType.IsVarying() {
				// Check if this is a direct variable reference (not an indexed expression)
				if _, ok := e.X.(*syntax.Name); ok {
					// This is taking address of a varying variable directly - forbidden
					check.errorf(x, InvalidSPMDType, "%s", "cannot take address of varying variable")
					x.mode_ = invalid
					return
				}
				// Otherwise, this is likely an indexed expression like data[i] - allow it
			}
		}

		x.mode_ = value
		x.typ_ = &Pointer{base: x.typ()}
		return
```

Replace with:
```go
		// SPMD: &Varying[T] produces Varying[*T] (per-lane pointer vector).
		// The Alloc holds the varying value; TinyGo emits per-lane GEPs.
		if buildcfg.Experiment.SPMD {
			if spmdType, ok := x.typ().(*SPMDType); ok && spmdType.IsVarying() {
				x.mode_ = value
				x.typ_ = NewVarying(&Pointer{base: spmdType.Elem()})
				return
			}
		}

		x.mode_ = value
		x.typ_ = &Pointer{base: x.typ()}
		return
```

This handles ALL `&Varying[T]` cases (direct variable, indexed expression, etc.) and transforms the type to `Varying[*T]`.

- [ ] **Step 2: Fix go/types/expr.go — add type transform for SPMD**

In `go/src/go/types/expr.go`, the `token.AND` case currently has NO SPMD check (the pointer_ext_spmd.go code is dead). Add the type transform before the default pointer type setting.

Find lines 144-147:
```go
		x.mode_ = value
		x.typ_ = &Pointer{base: x.typ()}
		return
```

Replace with:
```go
		// SPMD: &Varying[T] produces Varying[*T] (per-lane pointer vector).
		if buildcfg.Experiment.SPMD {
			if spmdType, ok := x.typ().(*SPMDType); ok && spmdType.IsVarying() {
				x.mode_ = value
				x.typ_ = NewVarying(&Pointer{base: spmdType.Elem()})
				return
			}
		}

		x.mode_ = value
		x.typ_ = &Pointer{base: x.typ()}
		return
```

Ensure `buildcfg` is imported (check the import list in expr.go — it should already be).

- [ ] **Step 3: Add type checker test case**

Add to `go/src/go/types/testdata/spmd/pointer_varying.go` (or create if it doesn't exist at the right path):

```go
// Valid: &Varying[T] produces Varying[*T]
func testAddressOfVarying(v lanes.Varying[int]) {
	ptr := &v
	_ = ptr // type should be Varying[*int]
}
```

If the test file already exists, look for the comment about "deferred: &varyingVar address-of" and replace the error annotation with a valid test.

- [ ] **Step 4: Build Go toolchain**

```bash
cd /home/cedric/work/SPMD && make build-go
```

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/go
git add src/cmd/compile/internal/types2/expr.go src/go/types/expr.go src/go/types/testdata/spmd/pointer_varying.go
git commit -m "feat: allow &Varying[T] producing Varying[*T]

Remove the types2 rejection for &varyingVar. Both type checkers now
transform &Varying[T] to Varying[*T] (per-lane pointer vector).
The go/ssa Alloc holds the varying value; TinyGo emits per-lane GEPs.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: TinyGo — Per-Lane GEP for Varying Alloca

**Files:**
- Modify: `tinygo/compiler/compiler.go` or `tinygo/compiler/spmd.go`

When TinyGo encounters `&Alloc` where the Alloc holds a `Varying[T]` and the result type is `Varying[*T]`, it needs to emit per-lane GEPs instead of a scalar pointer.

- [ ] **Step 1: Find where Alloc addresses are generated**

Search for how TinyGo handles `*ssa.Alloc` and `*ssa.UnOp{Op: token.AND}` (address-of). The go/ssa representation for `ptr := &v` where `v` is a local variable is:
- `v` is stored in an `*ssa.Alloc`
- `ptr` references the `*ssa.Alloc` directly (the Alloc IS the address)

So the question is: when `getValue` on an `*ssa.Alloc` of type `*Varying[T]` is called, and the consumer expects `Varying[*T]`, what should happen?

Look in `compiler.go` for `*ssa.Alloc` handling and in `spmd.go` for any SPMD-specific Alloc logic.

- [ ] **Step 2: Add per-lane GEP generation**

When the go/ssa type is `*SPMDType{elem: T}` (pointer to Varying[T]) but the Go-level type says `Varying[*T]`, emit per-lane GEPs:

```go
// Detect: Alloc of Varying[T] where result is used as Varying[*T]
// The alloca holds <N x T> or [N x T] on stack.
// Produce <N x ptr>: each lane gets pointer to its own element.
func (b *builder) spmdVaryingAddressOf(alloca llvm.Value, elemType llvm.Type, laneCount int) llvm.Value {
    i32Type := b.ctx.Int32Type()
    ptrType := llvm.PointerType(elemType, 0)  // or opaque ptr
    ptrVec := llvm.Undef(llvm.VectorType(alloca.Type(), laneCount))
    for lane := 0; lane < laneCount; lane++ {
        gep := b.CreateInBoundsGEP(elemType, alloca, []llvm.Value{
            llvm.ConstInt(i32Type, 0, false),
            llvm.ConstInt(i32Type, uint64(lane), false),
        }, "varyingaddr.gep")
        ptrVec = b.CreateInsertElement(ptrVec, gep,
            llvm.ConstInt(i32Type, uint64(lane), false), "")
    }
    return ptrVec
}
```

The exact integration point depends on where TinyGo processes the `*ssa.Alloc` address. This may be in `getValue` (for SSA values that ARE allocs), or in a specific Alloc handler.

IMPORTANT: Read the existing code first. TinyGo may already handle varying allocas in a way that just needs a small extension. Look for patterns like `spmdAllocaOrigin` or the Broadcast alloca pattern (line ~3102).

- [ ] **Step 3: Build and test**

```bash
cd /home/cedric/work/SPMD && make build-tinygo

# Create a simple test
cat > /tmp/test-addr.go << 'EOF'
// run -goexperiment spmd
package main

import (
    "fmt"
    "lanes"
)

func main() {
    src := []int32{10, 20, 30, 40}
    go for i, v := range src {
        _ = i
        ptr := &v
        *ptr = *ptr + 1
        fmt.Println(*ptr)
    }
}
EOF

# Test WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/test-addr.wasm /tmp/test-addr.go && wasmtime run /tmp/test-addr.wasm
```

If this test is too complex for the initial implementation, start with a simpler pattern that just takes the address and doesn't dereference it.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/compiler.go compiler/spmd.go
git commit -m "feat: emit per-lane GEPs for &Varying[T] address-of

When an Alloc holds Varying[T] and its address is used as Varying[*T],
emit per-lane GEP to produce <N x ptr> instead of a scalar pointer.
Each lane gets a pointer to its own element in the spilled storage.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: E2E Test + PLAN.md Update

**Files:**
- Modify: `test/integration/spmd/pointer-varying/main.go` — add test case
- Modify: `PLAN.md` — mark item as done

- [ ] **Step 1: Add E2E test**

Read `test/integration/spmd/pointer-varying/main.go` and add a test function that exercises `&varyingVar`:

```go
// Test: &varyingVar produces Varying[*T]
func testAddressOfVarying() {
    src := []int32{10, 20, 30, 40}
    dst := make([]int32, 4)
    go for i, v := range src {
        ptr := &v           // Varying[*int32]
        *ptr = *ptr + 1     // Per-lane modification via pointer
        dst[i] = *ptr
    }
    // Expected: [11, 21, 31, 41]
    fmt.Printf("addressOf: %v\n", dst)
}
```

Call it from `main()`.

- [ ] **Step 2: Test**

```bash
# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/ptr-varying.wasm test/integration/spmd/pointer-varying/main.go \
  && wasmtime run /tmp/ptr-varying.wasm

# x86
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/ptr-varying \
  test/integration/spmd/pointer-varying/main.go && /tmp/ptr-varying
```

- [ ] **Step 3: Update PLAN.md**

Mark `&varyingVar address-of` as DONE.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD
git add test/integration/spmd/pointer-varying/main.go PLAN.md
git commit -m "test: add &varyingVar E2E test, mark as DONE in PLAN.md

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Implementation Notes

1. **The go/types pointer_ext_spmd.go `validateSPMDAddressOperation` is DEAD CODE** — it's never called from expr.go. The types2 version has the check inline. Either wire it in or clean it up as part of the type checker changes.

2. **`NewVarying(&Pointer{base: spmdType.Elem()})` is the type transform**. This unwraps Varying to get T, wraps T in *T, then re-wraps in Varying. Import `NewVarying` if not already available (it's in the `types` package).

3. **TinyGo's alloca representation**: A `Varying[T]` variable stored in an Alloc produces an LLVM alloca of type `<N x T>` (vector) or `[N x T]` (array, for non-vectorizable types). The per-lane GEP indexes into this storage. Check which representation is used.

4. **LLVM opaque pointers**: Modern LLVM uses opaque pointers (`ptr`), not typed pointers (`*i32`). The GEP still needs the element type for offset calculation, but the result is just `ptr`. A vector of opaque pointers is `<N x ptr>`.

5. **Escape analysis**: If `ptr` from `&varyingVar` escapes the function, the alloca must be promoted to heap. go/ssa handles this via `Alloc.Heap` flag. TinyGo respects this flag. No special handling needed.
