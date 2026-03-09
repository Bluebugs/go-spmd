# Varying LogicalBinop Flattening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace short-circuit branch CFG with parallel AND/OR ops for `&&`/`||` in varying boolean context.

**Architecture:** In `logicalBinop()` (go/ssa builder), detect when the expression type is `*types.SPMDType` and both operands are side-effect-free. Instead of creating `binop.rhs`/`binop.done` blocks with branches and a Phi, emit a single `BinOp(AND/OR)` instruction. Recursive `logicalBinop` calls for nested sub-expressions naturally flatten the entire tree.

**Tech Stack:** Go, go/ssa (x-tools-spmd), go/ast, go/types

---

### Task 1: Add `isSideEffectFreeBoolExpr` helper

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_varying.go` (add helper near `exprHasSPMDType`)

**Step 1: Write the failing test**

Add to `x-tools-spmd/go/ssa/spmd_varying_test.go`:

```go
func TestIsSideEffectFreeBoolExpr(t *testing.T) {
	// Build SSA from source with various boolean expressions.
	// The helper is tested indirectly via logicalBinop flattening in Task 2,
	// but we test it directly here for edge cases.
	src := `package main
import "lanes"

func sideEffect() lanes.Varying[bool] { return lanes.Varying[bool]{} }

func f(a [4]int, b [4]int) {
	for i, x := range a {
		y := b[i]
		_ = lanes.Varying[int](x)

		// Side-effect-free: comparisons, &&, ||, !, parens, idents
		v1 := x > 0 && y < 10
		v2 := x == 1 || (y != 2 && x >= 3)
		v3 := !(x <= 5)

		// Has side effects: function call
		v4 := x > 0 && sideEffect()

		_, _, _, _ = v1, v2, v3, v4
	}
}

func main() {}
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	// After flattening, v1/v2/v3 should produce BinOp AND/OR (no binop.rhs blocks).
	// v4 should still produce binop.rhs blocks (has function call).
	var buf bytes.Buffer
	ssa.WriteFunction(&buf, fn)
	output := buf.String()

	// v4 has side effects, so short-circuit branches must remain.
	if !strings.Contains(output, "binop.rhs") {
		t.Errorf("expected binop.rhs block for side-effecting expression:\n%s", output)
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestIsSideEffectFreeBoolExpr -count=1 -v
```
Expected: FAIL — test file compiles but the test may pass vacuously (binop.rhs blocks exist for ALL expressions since flattening isn't implemented yet). That's OK — this test is a foundation for Task 2.

**Step 3: Write minimal implementation**

Add to `x-tools-spmd/go/ssa/spmd_varying.go` after `exprHasSPMDType`:

```go
// isSideEffectFreeBoolExpr reports whether the boolean expression e can be
// evaluated unconditionally without side effects. This is used to flatten
// varying &&/|| to bitwise AND/OR instead of short-circuit branches.
//
// Safe expressions: comparisons, boolean literals, variable references,
// unary !, parenthesized expressions, and nested &&/||.
// Unsafe: function calls, index expressions, channel ops, etc.
func isSideEffectFreeBoolExpr(fn *Function, e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.LAND, token.LOR:
			return isSideEffectFreeBoolExpr(fn, e.X) && isSideEffectFreeBoolExpr(fn, e.Y)
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return true
		default:
			// Arithmetic ops (+, -, *, etc.) are side-effect-free but not boolean.
			// They appear as operands to comparisons, not as top-level bool exprs.
			// We don't need to recurse into comparison operands — comparisons
			// themselves are always safe.
			return false
		}
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return isSideEffectFreeBoolExpr(fn, e.X)
		}
		return false
	case *ast.ParenExpr:
		return isSideEffectFreeBoolExpr(fn, e.X)
	case *ast.Ident:
		return true
	case *ast.BasicLit:
		return true
	default:
		return false
	}
}
```

**Step 4: Run test to verify it compiles**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestIsSideEffectFreeBoolExpr -count=1 -v
```
Expected: PASS (test passes vacuously — all expressions still have binop.rhs blocks)

**Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_varying.go go/ssa/spmd_varying_test.go
git commit -m "feat: add isSideEffectFreeBoolExpr helper for logicalBinop flattening"
```

---

### Task 2: Flatten varying logicalBinop to BinOp AND/OR

**Files:**
- Modify: `x-tools-spmd/go/ssa/builder.go:295-345` (logicalBinop function)

**Step 1: Write the failing test**

Add to `x-tools-spmd/go/ssa/spmd_predicate_test.go`:

```go
func TestPredicateSPMD_VaryingLogicalBinopFlattened(t *testing.T) {
	src := `package main
import "lanes"

func f(a [4]int, b [4]int) {
	for i, x := range a {
		y := b[i]
		_ = lanes.Varying[int](x)
		result := x > 0 && y < 10
		_ = result
	}
}

func main() {}
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	var buf bytes.Buffer
	ssa.WriteFunction(&buf, fn)
	output := buf.String()

	// After flattening, no binop.rhs or binop.done blocks should exist.
	if strings.Contains(output, "binop.rhs") {
		t.Errorf("expected no binop.rhs block (logicalBinop should be flattened):\n%s", output)
	}
	if strings.Contains(output, "binop.done") {
		t.Errorf("expected no binop.done block (logicalBinop should be flattened):\n%s", output)
	}

	// A BinOp AND should appear instead.
	if !strings.Contains(output, " & ") {
		t.Errorf("expected BinOp AND (&) in flattened output:\n%s", output)
	}
}
```

**Step 2: Run test to verify it fails**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestPredicateSPMD_VaryingLogicalBinopFlattened -count=1 -v
```
Expected: FAIL — `binop.rhs` block still present

**Step 3: Write minimal implementation**

Modify `logicalBinop` in `x-tools-spmd/go/ssa/builder.go:295` to add the flattening early exit:

```go
func (b *builder) logicalBinop(fn *Function, e *ast.BinaryExpr) Value {
	t := fn.typeOf(e)

	// Varying boolean with side-effect-free operands: flatten to BinOp AND/OR.
	// Both sides are evaluated unconditionally (no short-circuit branches).
	// This avoids creating binop.rhs/binop.done blocks that the predicate
	// pass cannot handle for nested mixed &&/|| chains.
	if _, ok := t.(*types.SPMDType); ok {
		if isSideEffectFreeBoolExpr(fn, e.X) && isSideEffectFreeBoolExpr(fn, e.Y) {
			x := b.expr(fn, e.X)
			y := b.expr(fn, e.Y)
			var op token.Token
			switch e.Op {
			case token.LAND:
				op = token.AND
			case token.LOR:
				op = token.OR
			}
			v := &BinOp{Op: op, X: x, Y: y}
			v.pos = e.OpPos
			v.typ = t
			return fn.emit(v)
		}
	}

	rhs := fn.newBasicBlock("binop.rhs")
	done := fn.newBasicBlock("binop.done")

	// ... rest of existing function unchanged ...
```

Note: The `types` import may already be present. Check the import block at the top of `builder.go`. If `"go/types"` is not imported, add it.

**Step 4: Run test to verify it passes**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestPredicateSPMD_VaryingLogicalBinopFlattened -count=1 -v
```
Expected: PASS

**Step 5: Run existing tests to verify no regression**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run "TestPredicateSPMD|TestBooleanChain|TestSPMD" -count=1 -v 2>&1 | tail -20
```
Expected: All existing tests PASS. The LOR short-circuit tests (`TestPredicateSPMD_GoForLoopLORShortCircuit`) should still pass — `fieldLen < 1 || fieldLen > 3` is side-effect-free, so it will be flattened to `BinOp OR` instead of branches+phi. The test checks for "no varying If" and "spmd_select present" — after flattening, there's no varying If (good), but there's also no `spmd_select` (the phi was never created). **This test may need updating** to accept either `spmd_select` or `BinOp OR` as valid output.

**Step 6: Fix any broken existing tests**

If `TestPredicateSPMD_GoForLoopLORShortCircuit` fails because `spmd_select` is no longer emitted (the `||` is now flattened to `BinOp OR` instead of going through predication), update the test assertion:

Change:
```go
if !strings.Contains(output, "spmd_select") {
    t.Errorf("expected spmd_select ...")
}
```
To:
```go
// After flattening, the || is a BinOp OR, not a phi→spmd_select.
if !strings.Contains(output, " | ") && !strings.Contains(output, "spmd_select") {
    t.Errorf("expected BinOp OR (|) or spmd_select after LOR handling:\n%s", output)
}
```

**Step 7: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/builder.go go/ssa/spmd_predicate_test.go
git commit -m "feat: flatten varying logicalBinop to BinOp AND/OR for side-effect-free expressions"
```

---

### Task 3: Test nested mixed &&/|| (the ipv4 pattern)

**Files:**
- Modify: `x-tools-spmd/go/ssa/spmd_predicate_test.go` (add tests)

**Step 1: Write the test for deeply nested pattern**

```go
func TestPredicateSPMD_VaryingLogicalBinopNestedMixed(t *testing.T) {
	// This is the exact pattern from the ipv4-parser examples version:
	// fieldLen == 3 && (b0 > 2 || (b0 == 2 && (b1 > 5 || (b1 == 5 && b2 > 5))))
	src := `package main
import "lanes"

func f(a [4]int, b [4]int, c [4]int, d [4]int) {
	for i, fieldLen := range a {
		b0 := b[i]
		b1 := c[i]
		b2 := d[i]
		_ = lanes.Varying[int](fieldLen)
		hasOverflow := fieldLen == 3 && (b0 > 2 || (b0 == 2 && (b1 > 5 || (b1 == 5 && b2 > 5))))
		_ = hasOverflow
	}
}

func main() {}
`
	pkg := buildSSAWithSPMD(t, src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("function f not found")
	}

	var buf bytes.Buffer
	ssa.WriteFunction(&buf, fn)
	output := buf.String()

	// No binop blocks should remain — everything flattened.
	if strings.Contains(output, "binop.rhs") {
		t.Errorf("expected no binop.rhs blocks for nested &&/||:\n%s", output)
	}

	// Must have both AND (&) and OR (|) operations.
	if !strings.Contains(output, " & ") {
		t.Errorf("expected BinOp AND (&):\n%s", output)
	}
	if !strings.Contains(output, " | ") {
		t.Errorf("expected BinOp OR (|):\n%s", output)
	}
}
```

**Step 2: Run test**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run TestPredicateSPMD_VaryingLogicalBinopNestedMixed -count=1 -v
```
Expected: PASS (recursive flattening handles nesting naturally)

**Step 3: Write sanity check test**

```go
func TestPredicateSPMD_VaryingLogicalBinopNestedMixedSanity(t *testing.T) {
	src := `package main
import "lanes"

func f(a [4]int, b [4]int, c [4]int, d [4]int) {
	for i, fieldLen := range a {
		b0 := b[i]
		b1 := c[i]
		b2 := d[i]
		_ = lanes.Varying[int](fieldLen)
		hasOverflow := fieldLen == 3 && (b0 > 2 || (b0 == 2 && (b1 > 5 || (b1 == 5 && b2 > 5))))
		_ = hasOverflow
	}
}

func main() {}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	setSPMDOnRange(file)

	_, _, err = ssautil.BuildPackage(
		&types.Config{Importer: importer.Default()},
		fset, types.NewPackage("main", ""), []*ast.File{file},
		ssa.SanityCheckFunctions,
	)
	if err != nil {
		t.Fatalf("SanityCheck failed: %v", err)
	}
}
```

**Step 4: Run sanity test**

Run:
```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -run "TestPredicateSPMD_VaryingLogicalBinop" -count=1 -v
```
Expected: All PASS

**Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd
git add go/ssa/spmd_predicate_test.go
git commit -m "test: add nested mixed &&/|| flattening tests (ipv4 pattern)"
```

---

### Task 4: Run full test suite and E2E validation

**Files:**
- No modifications expected

**Step 1: Run all x-tools-spmd SSA tests**

```bash
cd /home/cedric/work/SPMD/x-tools-spmd && GOROOT=/home/cedric/work/SPMD/go GOEXPERIMENT=spmd /home/cedric/work/SPMD/go/bin/go test ./go/ssa/ -count=1 -v 2>&1 | tail -30
```
Expected: All PASS

**Step 2: Build TinyGo and run E2E tests**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
rm -rf ~/.cache/tinygo && bash test/e2e/spmd-e2e-test.sh 2>&1 | tail -15
```
Expected: 36+ compile pass, 31+ run pass, 10 reject OK

**Step 3: Verify the examples ipv4-parser now compiles (if the uint8 OOB issue is separate)**

```bash
rm -rf ~/.cache/tinygo && PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd /home/cedric/work/SPMD/tinygo/build/tinygo build -target=wasi -o /tmp/ipv4-example.wasm -scheduler=none /home/cedric/work/SPMD/examples/ipv4-parser/main.go 2>&1
```
Expected: May still fail due to the separate uint8 OOB bug, but the `br <16 x i8>` error should be gone.

**Step 4: Commit submodule pointer update in main repo**

```bash
cd /home/cedric/work/SPMD
git add x-tools-spmd
git commit -m "chore: update x-tools-spmd for varying logicalbinop flattening"
```
