#!/usr/bin/env bash
# Asserts the AVX2 codegen for parseIPv4Inner is free of:
#   - vpcmpeqq (i64-inflated flen/values compare — Bug 2.b)
#   - scatter/gather instructions (per-lane scatter — Bug 2.c)
#   - vpextrb in the loop body (only the return-ABI tail vpextrb is expected)
#
# Why vpextrb is tricky: after the final vpshufb that packs the 4-byte result
# into the low bytes of xmm0, the SysV ABI requires decomposing [4]byte into
# four separate byte-register arguments (r8d/edi/esi/edx). Those vpextrb
# instructions are NOT scatter — they are the return decomposition and appear
# strictly after the last vpshufb in the function. Any vpextrb that appears
# BEFORE the last vpshufb would indicate per-lane scatter inside the loop body
# (Bug 2.c still present). This script applies that "before-last-vpshufb" test.
#
# Note on pipefail + awk exit: the DIS extraction uses a write to a temp file
# rather than a subshell pipeline, because awk's early `exit` sends SIGPIPE to
# objdump, which bash's pipefail interprets as a pipeline failure even though
# the data is correct.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TINYGO="$ROOT/tinygo/build/tinygo"
GOROOT_SPMD="$ROOT/go"
SRC="$ROOT/test/integration/spmd/ipv4-parser/main.go"
TMPDIR_WORK="$(mktemp -d)"
BIN="$TMPDIR_WORK/ipv4-avx2"
DISASM_FILE="$TMPDIR_WORK/disasm.txt"

PATH="$GOROOT_SPMD/bin:$PATH" GOEXPERIMENT=spmd \
    "$TINYGO" build -llvm-features="+ssse3,+sse4.2,+avx2" -o "$BIN" "$SRC"

# Write full disasm to a file to avoid SIGPIPE from awk's early exit under pipefail.
objdump -d --no-show-raw-insn "$BIN" > "$DISASM_FILE"

# Slice the parseIPv4Inner function body: from its symbol label to the next
# symbol label (line starting with a hex address + " <") or blank line.
DIS="$(awk '/<[^>]*parseIPv4Inner[^>]*>:/{f=1} f{print} f&&/^$/{exit}' "$DISASM_FILE")"

if [ -z "$DIS" ]; then
    echo "FAIL: parseIPv4Inner symbol not found in disassembly"
    exit 1
fi

fail=0

# Rule 1: vpcmpeqq must be absent (Bug 2.b — i64-inflated compare for
# flens[field]/values[field] is eliminated by spmdMaskedLoadNarrow i32 cap).
if printf '%s\n' "$DIS" | grep -qiw 'vpcmpeqq'; then
    echo "FAIL: vpcmpeqq present in parseIPv4Inner (Bug 2.b: i64-inflated compare)"
    fail=1
fi

# Rule 2: No scatter/gather instructions (per-lane scatter store/load).
if printf '%s\n' "$DIS" | grep -Eiqw 'vp(scatter|gather)[a-z]*'; then
    echo "FAIL: scatter/gather instruction present in parseIPv4Inner (Bug 2.c: per-lane scatter)"
    fail=1
fi

# Rule 3: vpextrb must not appear before the last vpshufb in the slice.
# The last vpshufb is the result-packing shuffle; any vpextrb after it is the
# SysV ABI decomposition of [4]byte (legitimate). vpextrb before the last
# vpshufb would be per-lane scatter inside the loop body (Bug 2.c still live).
last_vpshufb_line="$(printf '%s\n' "$DIS" | awk 'tolower($0) ~ /vpshufb/{n=NR} END{print n+0}')"
if [ "$last_vpshufb_line" -eq 0 ]; then
    # No vpshufb at all: any vpextrb is suspect.
    if printf '%s\n' "$DIS" | grep -qiw 'vpextrb'; then
        echo "FAIL: vpextrb present in parseIPv4Inner with no vpshufb (loop-body scatter, Bug 2.c)"
        fail=1
    fi
else
    # Check for vpextrb lines that appear before the last vpshufb.
    bad_vpextrb="$(printf '%s\n' "$DIS" | awk -v last="$last_vpshufb_line" \
        'NR < last && tolower($0) ~ /vpextrb/')"
    if [ -n "$bad_vpextrb" ]; then
        echo "FAIL: vpextrb in loop body (before last vpshufb at line $last_vpshufb_line) — Bug 2.c scatter"
        printf '%s\n' "$bad_vpextrb"
        fail=1
    fi
fi

if [ "$fail" -ne 0 ]; then
    echo "--- parseIPv4Inner disassembly ---"
    printf '%s\n' "$DIS"
    exit 1
fi

echo "PASS: parseIPv4Inner AVX2 disasm clean (no vpcmpeqq, no scatter/gather, no loop-body vpextrb)"
