# Byte-Decomposition Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect stride-S interleaved stores that extract bytes from a wider Varying source via constant shifts, and lower them to a bitcast + constant shuffle + contiguous store instead of S separate scatter stores.

**Architecture:** Single-file change in TinyGo. Extend the existing `spmdEmitInterleavedStoreMasked` with a byte-decomposition fast path. When the stored values are `Trunc(Lshr(source, const*8), byte)` from the same source, emit a bitcast + spmdSwizzle + store.

**Tech Stack:** Go (TinyGo compiler), LLVM IR

**Spec:** `docs/superpowers/specs/2026-04-12-byte-decompose-store-design.md`

**Mandatory Workflow:** Each task uses golang-pro → code-reviewer → clean-commit pipeline.

---

## File Structure

### TinyGo (`tinygo/`)
- **Modify:** `compiler/spmd.go` — add byte-decomposition detection + lowering in the interleaved store path

---

## Task 1: Detect Byte-Decomposition Pattern

**Files:**
- Modify: `tinygo/compiler/spmd.go`

Add a function that checks if an interleaved store group's values are all byte extractions from the same wider source.

- [ ] **Step 1: Add detection function**

Add before `spmdEmitInterleavedStoreMasked` (around line 7281):

```go
// spmdByteDecomposeInfo holds the analysis result for a byte-decomposition
// interleaved store group: all S stored values are byte extractions from the
// same wider source via constant right-shifts.
type spmdByteDecomposeInfo struct {
	source    ssa.Value // the common Varying[int32/int16/int64] source
	byteOffs []int     // per-remainder: byte offset within source element (shift/8)
	elemWidth int      // source element byte width (2 for int16, 4 for int32, 8 for int64)
}

// spmdDetectByteDecompose checks if a stride-S interleaved store group is a
// byte-decomposition pattern: all S stored values are Trunc(Lshr(source, K*8), byte)
// from the same source. Returns the analysis or nil if the pattern doesn't match.
func spmdDetectByteDecompose(group *spmdInterleavedStoreGroup) *spmdByteDecomposeInfo {
	stride := group.stride
	var source ssa.Value
	byteOffs := make([]int, stride)

	for r := 0; r < stride; r++ {
		val := spmdStoreVal(group.stores[r])

		// Peel through Convert (byte truncation).
		var shifted ssa.Value
		var shiftAmt int

		switch v := val.(type) {
		case *ssa.Convert:
			// Convert(wider → byte). Check if the source is Lshr or direct.
			inner := v.X
			if binop, ok := inner.(*ssa.BinOp); ok && binop.Op == token.SHR {
				// Lshr(source, const)
				if c, ok := binop.Y.(*ssa.Const); ok {
					if amt, ok2 := constant.Int64Val(c.Value); ok2 && amt >= 0 && amt%8 == 0 {
						shifted = binop.X
						shiftAmt = int(amt)
					} else {
						return nil
					}
				} else {
					return nil
				}
			} else {
				// No shift — byte offset 0.
				shifted = inner
				shiftAmt = 0
			}
		default:
			return nil
		}

		byteOffs[r] = shiftAmt / 8

		// All remainders must share the same source.
		if source == nil {
			source = shifted
		} else if source != shifted {
			return nil
		}
	}

	// Determine source element width from the SSA type.
	srcType := source.Type()
	if spmdType, ok := srcType.(*types.SPMDType); ok {
		srcType = spmdType.Elem()
	}
	basic, ok := srcType.Underlying().(*types.Basic)
	if !ok {
		return nil
	}
	var elemWidth int
	switch basic.Kind() {
	case types.Int16, types.Uint16:
		elemWidth = 2
	case types.Int32, types.Uint32:
		elemWidth = 4
	case types.Int, types.Uint:
		elemWidth = 4 // TinyGo int is 32-bit
	case types.Int64, types.Uint64:
		elemWidth = 8
	default:
		return nil
	}

	// Validate: all byte offsets must be within element bounds.
	for _, off := range byteOffs {
		if off < 0 || off >= elemWidth {
			return nil
		}
	}

	return &spmdByteDecomposeInfo{
		source:    source,
		byteOffs:  byteOffs,
		elemWidth: elemWidth,
	}
}
```

Note: `spmdStoreVal` is an existing helper that extracts the value from either `*ssa.Store` or `*ssa.SPMDStore`. Check that it exists. Also import `go/constant` and `go/token` if not already available (they should be).

- [ ] **Step 2: Build and verify compilation**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "feat: add spmdDetectByteDecompose for interleaved store analysis

Detects stride-S store groups where all values are byte extractions
from the same wider Varying source via constant right-shifts. Returns
per-remainder byte offsets within the source element.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Byte-Decomposition Lowering

**Files:**
- Modify: `tinygo/compiler/spmd.go` — add fast path in `spmdEmitInterleavedStoreMasked`

- [ ] **Step 1: Add byte-decomposition fast path**

In `spmdEmitInterleavedStoreMasked` (line 7290), add a check at the very beginning (after collecting all S values into `vals`), before the existing shuffle logic:

After line 7305 (`vals[stride-1] = b.getValue(lastVal, token.NoPos)`), add:

```go
	// Fast path: byte-decomposition — all S stored values extract bytes from the
	// same wider source via constant shifts. Emit bitcast + constant shuffle + store.
	if decomp := spmdDetectByteDecompose(group); decomp != nil {
		b.spmdEmitByteDecomposeStore(decomp, group, mask)
		return
	}
```

Then add the lowering function:

```go
// spmdEmitByteDecomposeStore lowers a byte-decomposition interleaved store group
// to bitcast + constant byte shuffle + contiguous store.
func (b *builder) spmdEmitByteDecomposeStore(decomp *spmdByteDecomposeInfo, group *spmdInterleavedStoreGroup, mask llvm.Value) {
	stride := group.stride
	N := group.laneCount
	W := decomp.elemWidth

	// Get the source vector.
	sourceVal := b.getValue(decomp.source, token.NoPos)

	// Clamp N to actual LLVM vector lane count.
	if sourceVal.Type().TypeKind() == llvm.VectorTypeKind {
		if actualN := sourceVal.Type().VectorSize(); actualN < N {
			N = actualN
		}
	}

	// Step 1: Bitcast source from <N x iW> to <N*W x i8>.
	i8Type := b.ctx.Int8Type()
	byteVecType := llvm.VectorType(i8Type, N*W)
	bitcasted := b.CreateBitCast(sourceVal, byteVecType, "bytedecomp.cast")

	// Step 2: Build constant shuffle mask.
	// For each source element g (0..N-1), pick S bytes at the decomposition offsets.
	// Output layout: [g0_byte0, g0_byte1, ..., g0_byteS-1, g1_byte0, g1_byte1, ..., gN-1_byteS-1]
	totalOut := N * stride
	maskElts := make([]llvm.Value, N*W) // full vector width for pshufb
	outIdx := 0
	for g := 0; g < N; g++ {
		for r := 0; r < stride; r++ {
			// Byte position within the source vector.
			srcByteIdx := g*W + decomp.byteOffs[r]
			maskElts[outIdx] = llvm.ConstInt(i8Type, uint64(srcByteIdx), false)
			outIdx++
		}
	}
	// Fill remaining positions with 0x80 (zero in swizzle semantics).
	for i := outIdx; i < N*W; i++ {
		maskElts[i] = llvm.ConstInt(i8Type, 0x80, false)
	}
	shuffleMask := llvm.ConstVector(maskElts, false)

	// Step 3: Apply byte shuffle.
	shuffled := b.spmdSwizzle(bitcasted, shuffleMask)

	// Step 4: Compute base pointer and store.
	addr0 := group.addrs[0]
	baseSliceVal := b.getValue(addr0.X, getPos(addr0))

	var bufptr llvm.Value
	switch ptrTyp := addr0.X.Type().Underlying().(type) {
	case *types.Slice:
		bufptr = b.CreateExtractValue(baseSliceVal, 0, "bytedecomp.ptr")
	case *types.Pointer:
		bufptr = baseSliceVal
		_ = ptrTyp
	default:
		return
	}

	// Get the scalar base index.
	scalarBase := llvm.Value{}
	if b.spmdDecomposed != nil {
		if dec, ok := b.spmdDecomposed[addr0.Index]; ok {
			scalarBase = dec.scalarBase
		}
	}
	if scalarBase.IsNil() {
		idxVec := b.getValue(addr0.Index, getPos(addr0))
		if idxVec.Type().TypeKind() == llvm.VectorTypeKind {
			scalarBase = b.CreateExtractElement(idxVec,
				llvm.ConstInt(b.ctx.Int32Type(), 0, false), "bytedecomp.base")
		} else {
			scalarBase = idxVec
		}
	}
	if scalarBase.IsNil() {
		scalarBase = llvm.ConstInt(b.uintptrType, 0, false)
	}
	scalarBase = b.extendInteger(scalarBase, addr0.Index.Type(), b.uintptrType)

	// GEP to the output position.
	elemType := b.ctx.Int8Type()
	outPtr := b.CreateInBoundsGEP(elemType, bufptr, []llvm.Value{scalarBase}, "bytedecomp.gep")

	// Store the shuffled vector (overwrite pattern — writes N*W bytes, only N*stride are valid).
	st := b.CreateStore(shuffled, outPtr)
	st.SetAlignment(1)

	_ = totalOut
}
```

- [ ] **Step 2: Build TinyGo**

```bash
cd /home/cedric/work/SPMD && make build-tinygo
```

- [ ] **Step 3: Verify correctness on all targets**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-bytedecomp-ssse3 \
  test/integration/spmd/base64-mula-lemire-v2/main.go && /tmp/base64-bytedecomp-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/base64-bytedecomp-avx2 \
  test/integration/spmd/base64-mula-lemire-v2/main.go && /tmp/base64-bytedecomp-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/base64-bytedecomp.wasm test/integration/spmd/base64-mula-lemire-v2/main.go \
  && wasmtime run /tmp/base64-bytedecomp.wasm

# Also test v1 to ensure no regression
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/base64-v1-regress \
  test/integration/spmd/base64-mula-lemire/main.go && /tmp/base64-v1-regress
```

Expected: `Correctness: PASS` for all.

**IMPORTANT**: If the byte-decomposition pattern does NOT match for the v2 decoder (the detection may fail because the SSA structure doesn't match the expected `Convert(BinOp{SHR}(...))` pattern), the fallback to the existing interleaved store path kicks in automatically — correctness is preserved. Debug by adding `println` in `spmdDetectByteDecompose`.

- [ ] **Step 4: Run benchmarks**

```bash
# SSSE3
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2 -o /tmp/bench-bytedecomp-ssse3 \
  test/integration/spmd/base64-mula-lemire-v2/bench.go && /tmp/bench-bytedecomp-ssse3

# AVX2
PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd ./tinygo/build/tinygo build \
  -llvm-features=+ssse3,+sse4.2,+avx2 -o /tmp/bench-bytedecomp-avx2 \
  test/integration/spmd/base64-mula-lemire-v2/bench.go && /tmp/bench-bytedecomp-avx2

# WASM
WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-bytedecomp.wasm test/integration/spmd/base64-mula-lemire-v2/bench.go \
  && wasmtime run /tmp/bench-bytedecomp.wasm
```

Report results. Expected: Loop 4 drops from 18-24 to 2-3 instructions.

- [ ] **Step 5: Commit**

```bash
cd /home/cedric/work/SPMD/tinygo
git add compiler/spmd.go
git commit -m "perf: byte-decomposition fast path for interleaved stores

When a stride-S interleaved store group extracts bytes from the same
wider Varying source via constant shifts, emit bitcast + constant
pshufb/swizzle + contiguous store instead of S separate scatter
stores. Reduces base64 v2 Loop 4 from 18-24 to 2-3 instructions.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Final Benchmarks

**Files:** None (verification only)

- [ ] **Step 1: Run all three targets**

```bash
cd /home/cedric/work/SPMD

echo "=== SSSE3 ===" && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -llvm-features=+ssse3,+sse4.2 \
  -o /tmp/bench-final-ssse3 test/integration/spmd/base64-mula-lemire-v2/bench.go \
  && /tmp/bench-final-ssse3

echo "=== AVX2 ===" && PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -llvm-features=+ssse3,+sse4.2,+avx2 \
  -o /tmp/bench-final-avx2 test/integration/spmd/base64-mula-lemire-v2/bench.go \
  && /tmp/bench-final-avx2

echo "=== WASM ===" && WASMOPT=/tmp/wasm-opt PATH=$(pwd)/go/bin:$PATH GOEXPERIMENT=spmd \
  ./tinygo/build/tinygo build -target=wasi -scheduler=none \
  -o /tmp/bench-final.wasm test/integration/spmd/base64-mula-lemire-v2/bench.go \
  && wasmtime run /tmp/bench-final.wasm
```

- [ ] **Step 2: Compare v2 before/after**

| Target | v2 Before | v2 After | simdutf | Gap |
|--------|-----------|----------|---------|-----|
| SSSE3 | 4208 MB/s | ? | — | — |
| AVX2 | 6509 MB/s | ? | 19930 MB/s | ? |
| WASM | 2740 MB/s | ? | — | — |

---

## Implementation Notes

1. **The `constant` and `token` packages** are already imported in spmd.go (used throughout the file).

2. **`spmdStoreVal`** is an existing helper (search for `func spmdStoreVal`) that extracts the Value from `*ssa.Store` or `*ssa.SPMDStore`.

3. **The SSA representation of `byte(packed[g] >> 16)`**: In go/ssa, this is typically `Convert(BinOp{SHR}(val, Const{16}), byte)`. The `Convert` does the truncation. However, after SSA predication, the value may be wrapped in `SPMDSelect` or other instructions. Check the actual SSA structure if detection fails.

4. **AVX2 lane-crossing**: `spmdSwizzle` on AVX2 uses `vpshufb` which only shuffles within 128-bit lanes. For `<32 x i8>` (8 int32 values → 24 output bytes), the shuffle indices for bytes 12-23 cross the lane boundary. The existing `spmdSwizzle` + AVX2 lane-crossing handling should manage this, but verify. If not, apply the same per-half-shuffle + merge pattern from the InterleaveStore AVX2 fix.

5. **Overwrite pattern**: The store writes `N*W` bytes (full vector width) but only `N*stride` are valid. The caller advances the output pointer by `N*stride`, and the next chunk's store overwrites the garbage. Same pattern as CompactStore/InterleaveStore.

6. **Stride-3 index pattern**: The store addresses are `g*3+0`, `g*3+1`, `g*3+2`. The `spmdAnalyzeStrideIndex` function detects this as stride=3 with remainders 0,1,2. The existing interleaved store analysis groups them. The new detection adds on top of this existing grouping.
