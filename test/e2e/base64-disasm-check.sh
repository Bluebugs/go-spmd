#!/usr/bin/env bash
# Asserts that the AVX2 codegen for the base64 Mula-Lemire kernel emits
# vpmaddubsw and vpmaddwd instructions (the stride-2 widen-multiply-add
# pattern detected by spmdTryEmitPmadd / spmdExtractPmaddSide).
#
# Regression guard for the ~20x base64 performance regression introduced when
# x-tools-spmd commit f3afc3fb ("propagate Varying[T] through addressable
# IndexExpr load") wrapped SPMDLoad results in a *ssa.ChangeType node.
# spmdExtractPmaddSide expected cvt.X to be *ssa.SPMDLoad directly; after
# f3afc3fb it became *ssa.ChangeType{X: *ssa.SPMDLoad}, causing the assertion
# to fail and returning nil → no pmadd emitted → scalar vpinsrb/vpextrb
# scatter (~0.8 GB/s vs ~17 GB/s).
#
# The fix: peel ChangeType via spmdUnwrapChangeType before the SPMDLoad
# assertion (same class as Bug 2.c fixed in 03f76d91 for contiguous index).
#
# This script FAILs (vpmaddubsw=0 and/or vpmaddwd=0) on the un-fixed code
# and PASSes after the fix.
#
# Note on pipefail + awk exit: the disasm extraction writes to a temp file
# rather than using a subshell pipeline so that awk's early `exit` does not
# send SIGPIPE to objdump, which bash's pipefail would interpret as failure.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TINYGO="$ROOT/tinygo/build/tinygo"
GOROOT_SPMD="$ROOT/go"
SRC="$ROOT/test/integration/spmd/base64-mula-lemire/main.go"
TMPDIR_WORK="$(mktemp -d)"
BIN="$TMPDIR_WORK/b64-avx2"
DISASM_FILE="$TMPDIR_WORK/disasm.txt"

trap 'rm -rf "$TMPDIR_WORK"' EXIT

PATH="$GOROOT_SPMD/bin:$PATH" GOEXPERIMENT=spmd \
    "$TINYGO" build -llvm-features="+ssse3,+sse4.2,+avx2" -o "$BIN" "$SRC"

# Write full disasm to a file to avoid SIGPIPE from awk's early exit under pipefail.
objdump -d --no-show-raw-insn "$BIN" > "$DISASM_FILE"

# Slice the main.decodeAndPack function body.  After LLVM inlining the function
# may appear under that symbol directly; if absent fall back to main.spmdDecode
# and finally to the whole binary.  The guard must see both vpmaddubsw and
# vpmaddwd to confirm the pmadd detection fired.
DIS="$(awk '/<[^>]*decodeAndPack[^>]*>:/{f=1} f{print} f&&/^$/{exit}' "$DISASM_FILE")"
if [ -z "$DIS" ]; then
    DIS="$(awk '/<[^>]*spmdDecode[^>]*>:/{f=1} f{print} f&&/^$/{exit}' "$DISASM_FILE")"
fi
if [ -z "$DIS" ]; then
    # Neither symbol present (fully inlined); use the whole binary disasm.
    DIS="$(cat "$DISASM_FILE")"
fi

fail=0

# Rule: vpmaddubsw must be present (byte→int16 loop).
ubsw_count="$(printf '%s\n' "$DIS" | grep -ci 'vpmaddubsw' || true)"
if [ "$ubsw_count" -eq 0 ]; then
    echo "FAIL: vpmaddubsw not found (spmdExtractPmaddSide ChangeType regression: byte→int16 pmadd not emitted)"
    fail=1
fi

# Rule: vpmaddwd must be present (int16→int32 loop).
wd_count="$(printf '%s\n' "$DIS" | grep -ci 'vpmaddwd' || true)"
if [ "$wd_count" -eq 0 ]; then
    echo "FAIL: vpmaddwd not found (spmdExtractPmaddSide ChangeType regression: int16→int32 pmadd not emitted)"
    fail=1
fi

if [ "$fail" -ne 0 ]; then
    echo "--- disassembly excerpt (decodeAndPack / spmdDecode or whole binary) ---"
    printf '%s\n' "$DIS" | head -80
    exit 1
fi

echo "PASS: base64 AVX2 disasm clean (vpmaddubsw=${ubsw_count} vpmaddwd=${wd_count})"
