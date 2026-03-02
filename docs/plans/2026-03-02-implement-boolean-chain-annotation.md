# Compound Boolean Chain Annotation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Annotate `&&`/`||` block chains in x-tools-spmd's go/ssa so TinyGo reads chain metadata instead of reconstructing chains from CFG topology.

**Architecture:** Add `SPMDBooleanChain` struct to go/ssa, populate via stack-based accumulator in `cond()`, resolve block pointers after `optimizeBlocks()`. Then replace TinyGo's `spmdDetectCondChains()` (~167 lines) with a ~30-line function reading `fn.SPMDBooleanChains`.

**Tech Stack:** Go, x-tools-spmd/go/ssa, TinyGo compiler

**Design doc:** `docs/plans/2026-03-01-ssa-compound-boolean-chain-annotation-design.md`

---

## Part 1: x-tools-spmd (go/ssa)

### Task 1: Add `SPMDBooleanChain` struct and Function field

**Files:**
- Modify: `x-tools-spmd/go/ssa/ssa.go:425-432` (add after SPMDSwitchChain)
- Modify: `x-tools-spmd/go/ssa/ssa.go:364-365` (add field on Function)

**Step 1: Add the struct and builder-internal type after `SPMDSwitchChain` (line 432)**

In `ssa.go`, after the `SPMDSwitchChain` struct, add:

```go
// SPMDBooleanChain represents a short-circuit boolean expression (&&/||)
// that was lowered into a chain of If blocks by cond().
// Populated during SSA construction when a compound boolean condition
// has 2+ terms. Block pointers are resolved after optimizeBlocks.
type SPMDBooleanChain struct {
	Op        token.Token    // token.LAND (&&) or token.LOR (||)
	Blocks    []*BasicBlock  // ordered chain: [first, ..., last] condition blocks
	ThenBlock *BasicBlock    // true-exit target (last block's Succs[0] for LAND)
	ElseBlock *BasicBlock    // false-exit target (last block's Succs[1] for LOR)
	IsVarying bool           // true when any condition in chain involves *types.SPMDType
}

// booleanChainCtx is a temporary accumulator used during cond() recursion
// to collect blocks forming a boolean chain. Not exported.
type booleanChainCtx struct {
	op           token.Token    // LAND or LOR
	sharedTarget *BasicBlock    // f for LAND, t for LOR
	blocks       []*BasicBlock  // leaf If blocks, appended in execution order
	expr         ast.Expr       // top-level expression (for IsVarying via exprHasSPMDType)
}
```

Add `"go/ast"` to the imports in `ssa.go` (it already imports `"go/token"`).

**Step 2: Add fields on Function**

In the Function struct, after line 365 (`SPMDSwitchChains`), add:

```go
	SPMDBooleanChains []*SPMDBooleanChain // compound boolean chain metadata; nil if none
```

In the "cleared after building" section (after line 387, `uniq`), add:

```go
	pendingBoolChains []*booleanChainCtx  // stack for boolean chain accumulation during cond()
```

**Step 3: Verify compilation**

Run: `cd x-tools-spmd && go build ./go/ssa/...`
Expected: compiles cleanly

**Step 4: Commit**

```
Add SPMDBooleanChain struct and Function fields
```

---

### Task 2: Populate chains in `cond()` builder

**Files:**
- Modify: `x-tools-spmd/go/ssa/builder.go:180-228` (the `cond()` function)

**Step 1: Write the failing test**

Add to `x-tools-spmd/go/ssa/spmd_varying_test.go`:

```go
func TestBooleanChain_And(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		if i > 2 && i < 10 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.Op != token.LAND {
		t.Errorf("expected LAND, got %v", chain.Op)
	}
	if len(chain.Blocks) != 2 {
		t.Errorf("expected 2 blocks in chain, got %d", len(chain.Blocks))
	}
	if chain.ThenBlock == nil {
		t.Error("ThenBlock is nil")
	}
	if chain.ElseBlock == nil {
		t.Error("ElseBlock is nil")
	}
	if !chain.IsVarying {
		t.Error("expected IsVarying=true")
	}
	// All chain blocks should share the same false successor (ElseBlock).
	for i, blk := range chain.Blocks {
		if blk.Succs[1] != chain.ElseBlock {
			t.Errorf("block %d (index %d): false successor is block %d, want %d",
				i, blk.Index, blk.Succs[1].Index, chain.ElseBlock.Index)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestBooleanChain_And -v`
Expected: FAIL (SPMDBooleanChains is nil/empty)

**Step 3: Implement chain accumulation in `cond()`**

Modify `cond()` in `builder.go` (lines 180-228). Replace the function body:

```go
func (b *builder) cond(fn *Function, e ast.Expr, t, f *BasicBlock) {
	switch e := e.(type) {
	case *ast.ParenExpr:
		b.cond(fn, e.X, t, f)
		return

	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND:
			// Check if continuing an existing LAND chain (flat a && b && c).
			continuing := false
			if n := len(fn.pendingBoolChains); n > 0 {
				top := fn.pendingBoolChains[n-1]
				if top.op == token.LAND && top.sharedTarget == f {
					continuing = true
				}
			}
			if !continuing {
				fn.pendingBoolChains = append(fn.pendingBoolChains, &booleanChainCtx{
					op: token.LAND, sharedTarget: f, expr: e,
				})
			}

			ltrue := fn.newBasicBlock("cond.true")
			b.cond(fn, e.X, ltrue, f)
			fn.currentBlock = ltrue
			b.cond(fn, e.Y, t, f)

			if !continuing {
				ctx := fn.pendingBoolChains[len(fn.pendingBoolChains)-1]
				fn.pendingBoolChains = fn.pendingBoolChains[:len(fn.pendingBoolChains)-1]
				if len(ctx.blocks) > 1 {
					chain := &SPMDBooleanChain{
						Op:        token.LAND,
						Blocks:    ctx.blocks,
						ElseBlock: ctx.sharedTarget,
						ThenBlock: ctx.blocks[len(ctx.blocks)-1].Succs[0],
						IsVarying: exprHasSPMDType(fn, ctx.expr),
					}
					fn.SPMDBooleanChains = append(fn.SPMDBooleanChains, chain)
				}
			}
			return

		case token.LOR:
			// Check if continuing an existing LOR chain (flat a || b || c).
			continuing := false
			if n := len(fn.pendingBoolChains); n > 0 {
				top := fn.pendingBoolChains[n-1]
				if top.op == token.LOR && top.sharedTarget == t {
					continuing = true
				}
			}
			if !continuing {
				fn.pendingBoolChains = append(fn.pendingBoolChains, &booleanChainCtx{
					op: token.LOR, sharedTarget: t, expr: e,
				})
			}

			lfalse := fn.newBasicBlock("cond.false")
			b.cond(fn, e.X, t, lfalse)
			fn.currentBlock = lfalse
			b.cond(fn, e.Y, t, f)

			if !continuing {
				ctx := fn.pendingBoolChains[len(fn.pendingBoolChains)-1]
				fn.pendingBoolChains = fn.pendingBoolChains[:len(fn.pendingBoolChains)-1]
				if len(ctx.blocks) > 1 {
					chain := &SPMDBooleanChain{
						Op:        token.LOR,
						Blocks:    ctx.blocks,
						ThenBlock: ctx.sharedTarget,
						ElseBlock: ctx.blocks[len(ctx.blocks)-1].Succs[1],
						IsVarying: exprHasSPMDType(fn, ctx.expr),
					}
					fn.SPMDBooleanChains = append(fn.SPMDBooleanChains, chain)
				}
			}
			return
		}

	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			b.cond(fn, e.X, f, t)
			return
		}
	}

	// Base case: emit comparison and If instruction.
	val := b.expr(fn, e)
	block := fn.currentBlock
	emitIf(fn, val, t, f)
	// Tag the If instruction as varying when the condition expression
	// involves an SPMD (Varying[T]) type.
	if exprHasSPMDType(fn, e) {
		if ifInstr, ok := block.Instrs[len(block.Instrs)-1].(*If); ok {
			ifInstr.IsVarying = true
		}
	}
	// Append to active boolean chain if shared target matches.
	if n := len(fn.pendingBoolChains); n > 0 {
		top := fn.pendingBoolChains[n-1]
		if (top.op == token.LAND && f == top.sharedTarget) ||
			(top.op == token.LOR && t == top.sharedTarget) {
			top.blocks = append(top.blocks, block)
		}
	}
}
```

**Step 4: Run test to verify it passes**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestBooleanChain_And -v`
Expected: PASS

**Step 5: Commit**

```
Populate SPMDBooleanChain during cond() lowering
```

---

### Task 3: Add comprehensive tests

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_varying_test.go` (append tests)

**Step 1: Write tests for all chain patterns**

Add to `spmd_varying_test.go`:

```go
func TestBooleanChain_Or(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		if i < 2 || i > 10 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.Op != token.LOR {
		t.Errorf("expected LOR, got %v", chain.Op)
	}
	if len(chain.Blocks) != 2 {
		t.Errorf("expected 2 blocks in chain, got %d", len(chain.Blocks))
	}
	if !chain.IsVarying {
		t.Error("expected IsVarying=true")
	}
	// All chain blocks should share the same true successor (ThenBlock).
	for i, blk := range chain.Blocks {
		if blk.Succs[0] != chain.ThenBlock {
			t.Errorf("block %d: true successor is block %d, want %d",
				i, blk.Succs[0].Index, chain.ThenBlock.Index)
		}
	}
}

func TestBooleanChain_TripleAnd(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		if i > 2 && i < 10 && i != 5 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.Op != token.LAND {
		t.Errorf("expected LAND, got %v", chain.Op)
	}
	if len(chain.Blocks) != 3 {
		t.Errorf("expected 3 blocks in chain, got %d", len(chain.Blocks))
	}
}

func TestBooleanChain_TripleOr(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		if i < 2 || i > 10 || i == 5 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.Op != token.LOR {
		t.Errorf("expected LOR, got %v", chain.Op)
	}
	if len(chain.Blocks) != 3 {
		t.Errorf("expected 3 blocks in chain, got %d", len(chain.Blocks))
	}
}

func TestBooleanChain_MixedAndOr(t *testing.T) {
	// (a && b) || c produces inner LAND chain only.
	// The outer LOR is not a flat chain (LHS is compound).
	src := `package main

func main() {
	for i := range 16 {
		if (i > 2 && i < 10) || i == 0 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	// Should have exactly 1 chain: the inner LAND.
	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain for (a&&b)||c, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.Op != token.LAND {
		t.Errorf("expected inner chain to be LAND, got %v", chain.Op)
	}
	if len(chain.Blocks) != 2 {
		t.Errorf("expected 2 blocks in inner LAND chain, got %d", len(chain.Blocks))
	}
}

func TestBooleanChain_UniformCondition(t *testing.T) {
	// Uniform conditions should still create a chain but with IsVarying=false.
	src := `package main

func main() {
	x := 3
	y := 7
	if x > 2 && y < 10 {
		_ = x
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 SPMDBooleanChain, got %d", len(mainFn.SPMDBooleanChains))
	}

	chain := mainFn.SPMDBooleanChains[0]
	if chain.IsVarying {
		t.Error("expected IsVarying=false for uniform conditions")
	}
}

func TestBooleanChain_NotExclusion(t *testing.T) {
	// !a && b should NOT form a chain (NOT inverts successor pattern).
	src := `package main

func main() {
	for i := range 16 {
		if !(i > 5) && i < 10 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	// No chain because NOT breaks the shared-target invariant.
	if len(mainFn.SPMDBooleanChains) != 0 {
		t.Errorf("expected 0 SPMDBooleanChains for !a && b, got %d", len(mainFn.SPMDBooleanChains))
	}
}
```

**Step 2: Run all tests**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestBooleanChain -v`
Expected: all PASS

**Step 3: Commit**

```
Add comprehensive boolean chain tests
```

---

### Task 4: Add resolution, printing, and sanity checking

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_varying.go` (add resolution function)
- Modify: `x-tools-spmd/go/ssa/func.go:377-379` (call resolution)
- Modify: `x-tools-spmd/go/ssa/func.go:765-776` (add printing)
- Modify: `x-tools-spmd/go/ssa/sanity.go:595-615` (add validation)

**Step 1: Write the failing test for string output**

Add to `spmd_varying_test.go`:

```go
func TestBooleanChain_String(t *testing.T) {
	src := `package main

func main() {
	for i := range 16 {
		if i > 2 && i < 10 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	var buf bytes.Buffer
	mainFn.WriteTo(&buf)
	output := buf.String()
	if !strings.Contains(output, "SPMDBooleanChain") {
		t.Error("expected 'SPMDBooleanChain' in SSA output for && chain")
	}
}

func TestBooleanChain_BlockResolution(t *testing.T) {
	// Verify that chain block pointers survive optimizeBlocks.
	src := `package main

func main() {
	for i := range 16 {
		if i > 2 && i < 10 && i != 5 {
			_ = i
		}
	}
}
`
	pkg := buildSSAWithSPMD(t, src)
	mainFn := pkg.Func("main")

	if len(mainFn.SPMDBooleanChains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(mainFn.SPMDBooleanChains))
	}
	chain := mainFn.SPMDBooleanChains[0]

	// ThenBlock and ElseBlock must be valid blocks in the function.
	for _, target := range []*ssa.BasicBlock{chain.ThenBlock, chain.ElseBlock} {
		found := false
		for _, blk := range mainFn.Blocks {
			if blk == target {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("block %d (comment %q) not found in function blocks",
				target.Index, target.Comment)
		}
	}

	// All chain blocks must also be in the function.
	for i, blk := range chain.Blocks {
		found := false
		for _, fblk := range mainFn.Blocks {
			if fblk == blk {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("chain block %d (index %d) not found in function blocks", i, blk.Index)
		}
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run "TestBooleanChain_String|TestBooleanChain_BlockResolution" -v`
Expected: FAIL (no "SPMDBooleanChain" in output)

**Step 3: Add resolution function to `spmd_varying.go`**

After `resolveSPMDSwitchChains`, add:

```go
// resolveSPMDBooleanChains updates SPMDBooleanChain block pointers after
// optimizeBlocks has potentially fused or eliminated blocks.
func resolveSPMDBooleanChains(fn *Function) {
	for _, chain := range fn.SPMDBooleanChains {
		chain.ThenBlock = resolveBlock(fn, chain.ThenBlock)
		chain.ElseBlock = resolveBlock(fn, chain.ElseBlock)
	}
}
```

**Step 4: Call resolution from `func.go`**

In `func.go`, after the `resolveSPMDSwitchChains` call (line 379), add:

```go
	if len(f.SPMDBooleanChains) > 0 {
		resolveSPMDBooleanChains(f)
	}
```

**Step 5: Add printing to `WriteFunction` in `func.go`**

After the SPMDSwitchChain printing block (line 776), add:

```go
	// Print SPMD boolean chain info.
	for i, chain := range f.SPMDBooleanChains {
		fmt.Fprintf(buf, "# SPMDBooleanChain %d:", i)
		fmt.Fprintf(buf, " op=%s", chain.Op)
		fmt.Fprintf(buf, " blocks=[")
		for j, blk := range chain.Blocks {
			if j > 0 {
				fmt.Fprint(buf, ",")
			}
			fmt.Fprintf(buf, "%d", blk.Index)
		}
		fmt.Fprint(buf, "]")
		fmt.Fprintf(buf, " then=%d else=%d", chain.ThenBlock.Index, chain.ElseBlock.Index)
		if chain.IsVarying {
			fmt.Fprint(buf, " varying")
		}
		fmt.Fprintln(buf)
	}
```

**Step 6: Add sanity checking in `sanity.go`**

After the SPMDSwitchChain validation block (line 615), add:

```go
	// Validate SPMD boolean chain info.
	for i, chain := range fn.SPMDBooleanChains {
		if chain.Op != token.LAND && chain.Op != token.LOR {
			s.errorf("SPMDBooleanChain %d: invalid Op %v", i, chain.Op)
		}
		if len(chain.Blocks) < 2 {
			s.errorf("SPMDBooleanChain %d: expected at least 2 blocks, got %d", i, len(chain.Blocks))
		}
		for j, blk := range chain.Blocks {
			if blk == nil {
				s.errorf("SPMDBooleanChain %d: Blocks[%d] is nil", i, j)
			} else if blk.parent != fn {
				s.errorf("SPMDBooleanChain %d: Blocks[%d] belongs to different function", i, j)
			}
		}
		if chain.ThenBlock == nil {
			s.errorf("SPMDBooleanChain %d: ThenBlock is nil", i)
		} else if chain.ThenBlock.parent != fn {
			s.errorf("SPMDBooleanChain %d: ThenBlock belongs to different function", i)
		}
		if chain.ElseBlock == nil {
			s.errorf("SPMDBooleanChain %d: ElseBlock is nil", i)
		} else if chain.ElseBlock.parent != fn {
			s.errorf("SPMDBooleanChain %d: ElseBlock belongs to different function", i)
		}
	}
```

**Step 7: Run all tests**

Run: `cd x-tools-spmd && go test ./go/ssa/ -run TestBooleanChain -v`
Expected: all PASS

**Step 8: Run full ssa test suite to check for regressions**

Run: `cd x-tools-spmd && go test ./go/ssa/ -count=1`
Expected: all PASS (sanity check on all existing tests passes with new validation)

**Step 9: Commit**

```
Add boolean chain resolution, printing, and sanity checks
```

---

## Part 2: TinyGo Compiler

### Task 5: Replace `spmdDetectCondChains` with metadata reader

**Files:**
- Modify: `tinygo/compiler/spmd.go:1882-2043` (replace `spmdDetectCondChains`)

**Step 1: Add `spmdPopulateCondChains()` function**

Replace `spmdDetectCondChains()` (lines 1882-2043) with:

```go
// spmdPopulateCondChains reads SPMDBooleanChain metadata from go/ssa and
// populates the spmdCondChains/spmdCondChainInner maps. Replaces the former
// spmdDetectCondChains which reconstructed chains from CFG topology.
func (b *builder) spmdPopulateCondChains() {
	b.spmdCondChains = make(map[int]*spmdCondChain)
	b.spmdCondChainInner = make(map[int]*spmdCondChain)

	for _, chain := range b.fn.SPMDBooleanChains {
		if !chain.IsVarying {
			continue
		}
		if len(chain.Blocks) < 2 {
			continue
		}
		// Verify the outer block is in an SPMD body.
		if b.isBlockInSPMDBody(chain.Blocks[0]) == nil {
			continue
		}

		cc := &spmdCondChain{
			outerIfBlock: chain.Blocks[0].Index,
			op:           chain.Op,
			thenTarget:   chain.ThenBlock.Index,
			elseTarget:   chain.ElseBlock.Index,
		}
		for _, blk := range chain.Blocks[1:] {
			cc.innerBlocks = append(cc.innerBlocks, blk.Index)
		}
		b.spmdCondChains[cc.outerIfBlock] = cc
		for _, idx := range cc.innerBlocks {
			b.spmdCondChainInner[idx] = cc
		}
	}
}
```

**Step 2: Update the call site in `preDetectVaryingIfs`**

In `preDetectVaryingIfs()` (around line 2045-2099), find the call to `b.spmdDetectCondChains()` and replace it with `b.spmdPopulateCondChains()`.

**Step 3: Verify TinyGo builds**

Run: `cd tinygo && make GO=/home/cedric/work/SPMD/go/bin/go`
Expected: builds cleanly

**Step 4: Run existing SPMD tests**

Run: `cd tinygo && GOEXPERIMENT=spmd go test ./compiler/ -run SPMD -v`
Expected: all existing tests pass

**Step 5: Commit**

```
Replace spmdDetectCondChains with metadata-based spmdPopulateCondChains
```

---

### Task 6: E2E validation

**Step 1: Run the full E2E test suite**

Run: `cd /home/cedric/work/SPMD && bash test/e2e/spmd-e2e-test.sh`
Expected: same results as before (20 run pass, 5 compile-only pass, etc.)

Focus on tests that use compound booleans:
- `ipv4-parser` (uses && and ||)
- `hex-encode` (uses boolean conditions)
- `mandelbrot` (uses compound boolean exit conditions)

**Step 2: Verify specific WASM outputs match**

For any test that was previously passing with compound booleans, compile both before and after and diff the WAT output to confirm identical codegen:

```bash
# Pick a test that uses && (e.g., mandelbrot or hex-encode)
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd WASMOPT=/tmp/wasm-opt \
  ./tinygo/build/tinygo build -target=wasi -simd=true -scheduler=none \
  -o /tmp/test-after.wasm examples/mandelbrot/main.go
wasm2wat /tmp/test-after.wasm > /tmp/test-after.wat
# Compare with pre-change output
```

**Step 3: Commit if any adjustments needed**

```
Validate boolean chain annotation E2E
```

---

## Summary

| Task | Where | What | Lines Changed |
|------|-------|------|---------------|
| 1 | x-tools-spmd/go/ssa/ssa.go | Add struct + fields | +20 |
| 2 | x-tools-spmd/go/ssa/builder.go | Populate in cond() | +60 (replaces 12) |
| 3 | x-tools-spmd/go/ssa/spmd_varying_test.go | Tests | +180 |
| 4 | x-tools-spmd func.go, spmd_varying.go, sanity.go | Resolution, print, sanity | +40 |
| 5 | tinygo/compiler/spmd.go | Replace detection | +30 (replaces ~167) |
| 6 | E2E | Validation | 0 |

Net effect: ~+330 new, ~-167 removed = ~+163 lines, replacing fragile CFG reconstruction with structured metadata.
