# Inline Small Byte Copy Design

## Goal

Replace opaque `runtime.sliceCopy` calls with inline `@llvm.memmove` intrinsic
for small fixed-size byte slice destinations, allowing LLVM optimization passes
(SROA, store-forwarding) to see through the copy.

## Motivation

The SPMD IPv4 parser pattern:
```go
input := [16]byte{}        // alloca + memory.fill
copy(input[:], s)           // runtime.sliceCopy → memory.copy
go for _, c := range input  // v128.load from stack
```

`runtime.sliceCopy` is opaque to LLVM — it can't optimize through the call.
Replacing it with `@llvm.memmove` (a known intrinsic) lets the WASM backend
specialize the copy and opens the door for alloca elimination.

## Detection Criteria

In `compiler.go` `case "copy":` handler, inline when ALL hold:
- `elemSize == 1` (byte elements only)
- `dstLen` is a compile-time constant (from `[N]byte` slice)
- Constant value <= 256 bytes

## Emitted IR

Instead of:
```llvm
%n = call i32 @runtime.sliceCopy(ptr %dst, ptr %src, i32 16, i32 %srcLen, i32 1)
```

Emit:
```llvm
%cmp = icmp ult i32 %srcLen, 16
%n = select i1 %cmp, i32 %srcLen, i32 16
call void @llvm.memmove.p0.p0.i32(ptr %dst, ptr %src, i32 %n, i1 false)
; return %n
```

## Design Decisions

- **`@llvm.memmove` not `@llvm.memcpy`**: `runtime.sliceCopy` uses `memmove`
  for overlap safety. We match that semantics. LLVM's alias analysis can still
  prove non-overlap when dst is a fresh alloca.
- **General optimization**: Applies to all code, not just SPMD. Benefits any
  small fixed-size byte copy.
- **Threshold 256**: Covers all practical SIMD register sizes and small buffers.
  Large copies should still use the runtime call for its optimized bulk path.

## Expected Wins

- Eliminates function call overhead (~10-15 cycles)
- Inlines min(dstLen, srcLen) computation
- LLVM/WASM backend can specialize the copy knowing dst alloca size
- Opens door for SROA/store-forwarding to eliminate alloca

## Non-Goals

- Not changing alloca or zero-initialization (separate optimization)
- Not changing non-byte copies (elemSize > 1)
- Not changing large or runtime-sized destination copies

## Files

- Modify: `tinygo/compiler/compiler.go:1928-1937` (copy builtin handler)
- May need: `tinygo/compiler/intrinsics.go` (memmove intrinsic declaration)
- Test: E2E suite + ipv4-parser benchmark
