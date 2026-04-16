# SPMD Blog Series Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Write and publish a 9-article blog series presenting the SPMD-for-Go PoC results, with live WASM demos for the marketing article.

**Architecture:** Hugo blog at `bluebugs.github.io/` (Ananke theme). New articles in `content/blogs/`. WASM demos via Hugo shortcodes + pre-compiled TinyGo WASM binaries in `static/wasm/`. Each article is standalone markdown drawing from the learnings docs in the main SPMD repo.

**Tech Stack:** Hugo, HTML/CSS/JS shortcodes, TinyGo WASM, WASI, JavaScript WASM instantiation.

**Working directories:**
- Blog repo: `/home/cedric/work/SPMD/bluebugs.github.io/`
- SPMD repo (for reference and WASM compilation): `/home/cedric/work/SPMD/`

**Reference docs (read before writing any article):**
- `/home/cedric/work/SPMD/docs/learnings/implementer-notes.md`
- `/home/cedric/work/SPMD/docs/learnings/developer-guide.md`
- `/home/cedric/work/SPMD/docs/learnings/novel-patterns.md`
- `/home/cedric/work/SPMD/docs/learnings/blog-to-poc-retrospective.md`
- `/home/cedric/work/SPMD/docs/superpowers/specs/2026-04-15-blog-series-design.md`

**Existing infrastructure (already working, do not modify):**
- `layouts/shortcodes/spmd-mandelbrot.html` — live WASM mandelbrot demo (serial + SPMD canvases, timing, speedup display)
- `static/wasm/mandelbrot-serial.wasm`, `static/wasm/mandelbrot-spmd.wasm` — pre-compiled mandelbrot binaries
- `static/wasm/wasm_exec.js` — TinyGo WASI JS shim
- `examples/mandelbrot_serial.go`, `examples/mandelbrot_spmd.go` — WASM-export source for mandelbrot

---

## Task 1: Base64 WASM Demo — Export Source

**Files:**
- Create: `bluebugs.github.io/examples/base64_scalar.go`
- Create: `bluebugs.github.io/examples/base64_spmd.go`

Both files follow the pattern of `examples/mandelbrot_serial.go` and `examples/mandelbrot_spmd.go`: standalone Go files with `//go:export` functions for JavaScript interop, a global buffer for results, and an empty `main()`.

- [ ] **Step 1: Write the scalar base64 WASM export**

Create `bluebugs.github.io/examples/base64_scalar.go`:

```go
// Base64 Decoder - Scalar Version for Browser WASM Demo
// Uses Go's encoding/base64 equivalent (manual implementation since
// TinyGo WASM doesn't have full encoding/base64 stdlib).
// Exports decodeBase64/getInputPtr/getOutputPtr/getOutputLen for JavaScript interop.
package main

import "unsafe"

const maxInput = 65536
const maxOutput = 65536

var inputBuf [maxInput]byte
var outputBuf [maxOutput]byte
var outputLen int32

func decodeSextet(ch byte) (byte, bool) {
	switch {
	case 'A' <= ch && ch <= 'Z':
		return ch - 'A', true
	case 'a' <= ch && ch <= 'z':
		return ch - 'a' + 26, true
	case '0' <= ch && ch <= '9':
		return ch - '0' + 52, true
	case ch == '+':
		return 62, true
	case ch == '/':
		return 63, true
	}
	return 0, false
}

func scalarDecode(src []byte) int {
	if len(src) == 0 || len(src)%4 != 0 {
		return 0
	}

	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	outIdx := 0

	for g := 0; g < groups; g++ {
		var sextets [4]byte
		for j := 0; j < 4; j++ {
			ch := src[g*4+j]
			if ch == '=' {
				sextets[j] = 0
				continue
			}
			s, ok := decodeSextet(ch)
			if !ok {
				return 0
			}
			sextets[j] = s
		}
		outputBuf[outIdx+0] = (sextets[0] << 2) | (sextets[1] >> 4)
		outputBuf[outIdx+1] = (sextets[1] << 4) | (sextets[2] >> 2)
		outputBuf[outIdx+2] = (sextets[2] << 6) | sextets[3]
		outIdx += 3
	}

	return outIdx - padCount
}

//go:export decodeBase64
func decodeBase64(inputLen int32) int32 {
	n := scalarDecode(inputBuf[:inputLen])
	outputLen = int32(n)
	return outputLen
}

//go:export getInputPtr
func getInputPtr() int32 {
	return int32(uintptr(unsafe.Pointer(&inputBuf[0])))
}

//go:export getOutputPtr
func getOutputPtr() int32 {
	return int32(uintptr(unsafe.Pointer(&outputBuf[0])))
}

//go:export getOutputLen
func getOutputLen() int32 { return outputLen }

func main() {}
```

- [ ] **Step 2: Write the SPMD base64 WASM export**

Create `bluebugs.github.io/examples/base64_spmd.go`. This uses the same `decodeAndPack` kernel from the PoC's `examples/base64-decoder/main.go`, adapted for WASM export:

```go
// Base64 Decoder - SPMD Version for Browser WASM Demo
// Uses cascading go-for loops (byte→int16→int32) to trigger
// pmaddubsw/pmaddwd pattern detection. No cross-lane operations.
// Exports decodeBase64/getInputPtr/getOutputPtr/getOutputLen for JavaScript interop.
package main

import (
	"lanes"
	"unsafe"
)

const maxInput = 65536
const maxOutput = 65536

var inputBuf [maxInput]byte
var outputBuf [maxOutput]byte
var outputLen int32

var decodeLUT = [16]byte{
	0, 0, 16, 4, 191, 191, 185, 185,
	0, 0, 0, 0, 0, 0, 0, 0,
}

func decodeAndPack(dst, src []byte) int {
	n := len(src)

	sextets := make([]byte, n)
	go for i, ch := range src {
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
	}

	halfLen := n / 2
	merged := make([]int16, halfLen)
	go for g := range merged {
		merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
	}

	quarterLen := halfLen / 2
	packed := make([]int32, quarterLen)
	go for g := range packed {
		packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
	}

	go for g := range packed {
		dst[g*3+0] = byte(packed[g] >> 16)
		dst[g*3+1] = byte(packed[g] >> 8)
		dst[g*3+2] = byte(packed[g])
	}

	return quarterLen * 3
}

func decodeSextet(ch byte) (byte, bool) {
	switch {
	case 'A' <= ch && ch <= 'Z':
		return ch - 'A', true
	case 'a' <= ch && ch <= 'z':
		return ch - 'a' + 26, true
	case '0' <= ch && ch <= '9':
		return ch - '0' + 52, true
	case ch == '+':
		return 62, true
	case ch == '/':
		return 63, true
	}
	return 0, false
}

func spmdDecode(src []byte) int {
	if len(src) == 0 || len(src)%4 != 0 {
		return 0
	}

	padCount := 0
	if src[len(src)-1] == '=' {
		padCount++
	}
	if len(src) >= 2 && src[len(src)-2] == '=' {
		padCount++
	}

	groups := len(src) / 4
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}
	hotBytes := hotGroups * 4

	var bv lanes.Varying[byte]
	chunkSize := lanes.Count[byte](bv)
	outOffset := 0

	for off := 0; off+chunkSize <= hotBytes; off += chunkSize {
		n := decodeAndPack(outputBuf[outOffset:], src[off:off+chunkSize])
		outOffset += n
	}

	rem := hotBytes % chunkSize
	if rem > 0 && rem%4 == 0 {
		padded := make([]byte, chunkSize)
		copy(padded, src[hotBytes-rem:hotBytes])
		for i := rem; i < chunkSize; i++ {
			padded[i] = 'A'
		}
		tmpDst := make([]byte, chunkSize)
		n := decodeAndPack(tmpDst, padded)
		validOut := rem * 3 / 4
		copy(outputBuf[outOffset:], tmpDst[:validOut])
		outOffset += validOut
		_ = n
	}

	if hotGroups < groups {
		tail := src[hotGroups*4:]
		c0, _ := decodeSextet(tail[0])
		c1, _ := decodeSextet(tail[1])
		var c2, c3 byte
		if tail[2] != '=' {
			c2, _ = decodeSextet(tail[2])
		}
		if tail[3] != '=' {
			c3, _ = decodeSextet(tail[3])
		}
		outputBuf[outOffset+0] = (c0 << 2) | (c1 >> 4)
		outputBuf[outOffset+1] = (c1 << 4) | (c2 >> 2)
		outputBuf[outOffset+2] = (c2 << 6) | c3
		outOffset += 3
	}

	return outOffset - padCount
}

//go:export decodeBase64
func decodeBase64(inputLen int32) int32 {
	n := spmdDecode(inputBuf[:inputLen])
	outputLen = int32(n)
	return outputLen
}

//go:export getInputPtr
func getInputPtr() int32 {
	return int32(uintptr(unsafe.Pointer(&inputBuf[0])))
}

//go:export getOutputPtr
func getOutputPtr() int32 {
	return int32(uintptr(unsafe.Pointer(&outputBuf[0])))
}

//go:export getOutputLen
func getOutputLen() int32 { return outputLen }

func main() {}
```

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add examples/base64_scalar.go examples/base64_spmd.go
git commit -m "feat: add base64 WASM export sources (scalar + SPMD)"
```

---

## Task 2: Base64 WASM Demo — Compile Binaries

**Files:**
- Create: `bluebugs.github.io/static/wasm/base64-scalar.wasm`
- Create: `bluebugs.github.io/static/wasm/base64-spmd.wasm`

- [ ] **Step 1: Compile scalar base64 to WASM**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
PATH=/home/cedric/work/SPMD/go/bin:$PATH \
  /home/cedric/work/SPMD/tinygo/build/tinygo build \
  -target=wasi -o static/wasm/base64-scalar.wasm \
  examples/base64_scalar.go
```

Expected: `static/wasm/base64-scalar.wasm` created, no errors.

- [ ] **Step 2: Compile SPMD base64 to WASM**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
PATH=/home/cedric/work/SPMD/go/bin:$PATH GOEXPERIMENT=spmd \
  /home/cedric/work/SPMD/tinygo/build/tinygo build \
  -target=wasi -simd=true -o static/wasm/base64-spmd.wasm \
  examples/base64_spmd.go
```

Expected: `static/wasm/base64-spmd.wasm` created, no errors.

- [ ] **Step 3: Verify both binaries load**

```bash
wasm2wat static/wasm/base64-scalar.wasm | head -5
wasm2wat static/wasm/base64-spmd.wasm | grep "v128" | head -5
```

Expected: scalar has no `v128` instructions, SPMD has `v128.*` instructions.

- [ ] **Step 4: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add static/wasm/base64-scalar.wasm static/wasm/base64-spmd.wasm
git commit -m "feat: add pre-compiled base64 WASM binaries (scalar + SPMD)"
```

---

## Task 3: Base64 WASM Demo — Hugo Shortcode

**Files:**
- Create: `bluebugs.github.io/layouts/shortcodes/spmd-base64.html`

Follow the pattern of `layouts/shortcodes/spmd-mandelbrot.html`: self-contained HTML with embedded CSS and JS. The shortcode:
1. Detects WASM SIMD support.
2. Loads `base64-scalar.wasm` and `base64-spmd.wasm`.
3. Provides a text area with default base64 input.
4. "Run Benchmark" button runs both decoders N iterations, shows MB/s throughput and speedup.

- [ ] **Step 1: Create the shortcode**

Create `bluebugs.github.io/layouts/shortcodes/spmd-base64.html`.

The file should be ~250-300 lines following the existing `spmd-mandelbrot.html` structure. Key differences from mandelbrot:

- Instead of dual canvases, use a text area (input) + two result panels showing MB/s.
- Load `base64-scalar.wasm` and `base64-spmd.wasm` (same `loadWasm` pattern).
- Copy the input string into the WASM input buffer via `getInputPtr()` and `new Uint8Array(memory.buffer, ptr, len)`.
- Call `decodeBase64(inputLen)` on each instance.
- Time multiple iterations (e.g., 100) to get stable throughput numbers.
- Display: scalar MB/s, SPMD MB/s, speedup, decoded output preview (first 100 chars).
- Default input: `"VGhlIHF1aWNrIGJyb3duIGZveCBqdW1wcyBvdmVyIHRoZSBsYXp5IGRvZw=="` repeated to ~4KB for measurable timing.

CSS styling: match the existing mandelbrot shortcode's card-like container, badge for SIMD detection, timing display, and run button.

SIMD detection: reuse the exact same `WebAssembly.validate(simdTest)` pattern from `spmd-mandelbrot.html`.

WASM loading: reuse the exact `loadWasm()` function pattern (TinyGo WASI `_start` → `proc_exit` catch).

The JavaScript interop pattern for writing input data into WASM memory:
```javascript
const inputPtr = instance.exports.getInputPtr();
const mem = new Uint8Array(instance.exports.memory.buffer, inputPtr, inputData.length);
mem.set(inputData);
const outLen = instance.exports.decodeBase64(inputData.length);
```

- [ ] **Step 2: Test locally with Hugo dev server**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
hugo server --buildDrafts
```

Create a temporary test page or add `{{</* spmd-base64 */>}}` to an existing draft post to verify:
1. SIMD badge shows correctly.
2. "Run Benchmark" loads WASM, decodes, shows MB/s for both.
3. Decoded output matches expected ("The quick brown fox jumps over the lazy dog").
4. Speedup ratio displayed.

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add layouts/shortcodes/spmd-base64.html
git commit -m "feat: add base64 live WASM benchmark shortcode"
```

---

## Task 4: Article 1 — "SPMD for Go: What If Your Loops Were 9x Faster?"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-results.md`

This is the marketing article. It uses both existing shortcodes (`spmd-mandelbrot` and the new `spmd-base64`).

- [ ] **Step 1: Write the article**

Create `bluebugs.github.io/content/blogs/spmd-results.md`.

Front matter:
```toml
+++
date = '2026-04-15T12:00:00-07:00'
draft = true
title = 'SPMD for Go: What If Your Loops Were 9x Faster?'
description = 'A proof of concept for language-level data parallelism in Go, with live WASM demos and real benchmark results'
featured_image = 'images/banff.jpg'
featured_image_class = 'cover bg-center'
+++
```

Content sections per the spec (§Article 1 in the design doc):

1. Hook paragraph (~100 words). Lead with the base64 number. Link to Article 2 and Article 3.
2. `{{</* spmd-mandelbrot */>}}` with ~200 words of context.
3. `{{</* spmd-base64 */>}}` with ~200 words of context.
4. The 30-second explanation: `go for`, `lanes.Varying[T]`, `reduce.Add`. Code example: sum-a-slice.
5. Benchmark table (real numbers from the 2026-04-15 re-benchmark).
6. Why this belongs in the compiler (~300 words). The SSA argument. The mask-stack lesson. The `simd/archsimd` comparison (complementary, not competing).
7. Where SPMD would help in the stdlib (~200 words). Image processing + byte parsing.
8. Closing with invitation for feedback.

Source: draw from `developer-guide.md` §1, `implementer-notes.md` §1, `blog-to-poc-retrospective.md` §3 and §11. All benchmark numbers from the fresh 2026-04-15 run. All code from the actual PoC examples.

Voice: conversational, first-person, confident but kind. "Here's what we built" not "what if."

- [ ] **Step 2: Test with Hugo dev server**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
hugo server --buildDrafts
```

Open `http://localhost:1313/blogs/spmd-results/` in browser. Verify:
1. Both WASM demos load and run.
2. Mandelbrot renders on both canvases with timing.
3. Base64 shows MB/s for both scalar and SPMD.
4. Article reads well, no broken links, no missing shortcodes.

- [ ] **Step 3: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-results.md
git commit -m "feat: add SPMD results article with live WASM demos (draft)"
```

---

## Task 5: Article 2 — "Writing SPMD Go: A Practical Guide"

**Files:**
- Create: `bluebugs.github.io/content/blogs/writing-spmd-go.md`

- [ ] **Step 1: Write the article**

Front matter:
```toml
+++
date = '2026-04-15T12:01:00-07:00'
draft = true
title = 'Writing SPMD Go: A Practical Guide'
description = 'How to think about uniform vs varying, write go for loops, use reductions, and avoid the common pitfalls'
featured_image = 'images/lakelouise.jpg'
featured_image_class = 'cover bg-center'
+++
```

Content per spec §Article 2: mental model, first `go for`, golden pattern, reductions + anti-pattern, control flow, performance patterns (cascading `go for`, chunk sizing, byte-lane vs int-lane, outer-SPMD batching), debugging, worked examples (hex-encode + mandelbrot), `iota` suggestion.

~2500 words. Draw from `developer-guide.md`. All code from actual PoC examples. Link to Article 1 for motivation, Article 9 for the negative result.

- [ ] **Step 2: Test locally, commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
hugo server --buildDrafts
# verify at http://localhost:1313/blogs/writing-spmd-go/
git add content/blogs/writing-spmd-go.md
git commit -m "feat: add SPMD developer guide article (draft)"
```

---

## Task 6: Article 3 — "How SPMD Lives in the Compiler"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-compiler-internals.md`

- [ ] **Step 1: Write the article**

Front matter:
```toml
+++
date = '2026-04-15T12:02:00-07:00'
draft = true
title = 'How SPMD Lives in the Compiler: Lessons from Building It'
description = 'The mask-stack detour, predicated SSA, and why SPMD has to live at the heart of the compiler'
featured_image = 'images/lakelouise.jpg'
featured_image_class = 'cover bg-center'
+++
```

Content per spec §Article 3: mask-stack detour (the three-fork story), predicated SSA, where it goes for upstream Go (`cmd/compile/internal/ssa`), the type-checker magic, scalar fallback, what we'd do differently.

Optional: add the step-through visualization shortcode for the varying `if` predication (reuse/adapt existing `spmd-oddeven.html` shortcode style, or create a new `spmd-predication.html`). If too complex, describe the predication transform with a before/after code diagram instead.

~2500 words. Draw from `implementer-notes.md` §3, §4, §6, `blog-to-poc-retrospective.md` §3, §10. All file:line references from the PoC.

Key message: SPMD is a compiler feature that has to live at the heart of the SSA form. The mask-stack lesson is the proof.

- [ ] **Step 2: Test locally, commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
hugo server --buildDrafts
# verify at http://localhost:1313/blogs/spmd-compiler-internals/
git add content/blogs/spmd-compiler-internals.md
git commit -m "feat: add SPMD compiler internals article (draft)"
```

---

## Task 7: Article 4 — "Pattern Matching Beats Hand-Written SIMD"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-pattern-matching.md`

- [ ] **Step 1: Write the article**

~2000 words. The base64 v1→v2 story. The `vpmaddubsw`/`vpmaddwd` detector. Byte-decomposition store. The `DotProductI8x16Add` cautionary tale. Closing: "pattern detectors generalize; builtins don't."

Draw from `novel-patterns.md` §7, §8, `blog-to-poc-retrospective.md` §3, §6.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-pattern-matching.md
git commit -m "feat: add pattern matching article (draft)"
```

---

## Task 8: Article 5 — "Byte Iteration at 32 Lanes"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-decomposed-index.md`

- [ ] **Step 1: Write the article**

~1200 words. The decomposed index path. Draw from `novel-patterns.md` §2, `implementer-notes.md` §5.1.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-decomposed-index.md
git commit -m "feat: add decomposed index path article (draft)"
```

---

## Task 9: Article 6 — "16 Bytes That Saved a Thousand Branches"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-wasm-guard-zone.md`

- [ ] **Step 1: Write the article**

~800 words. Draw from `novel-patterns.md` §10, `implementer-notes.md` §5.11.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-wasm-guard-zone.md
git commit -m "feat: add WASM guard zone article (draft)"
```

---

## Task 10: Article 7 — "How the Compiler Knows Your Load Is Contiguous"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-contiguous-analysis.md`

- [ ] **Step 1: Write the article**

~1200 words. Draw from `novel-patterns.md` §6, `implementer-notes.md` §5.3.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-contiguous-analysis.md
git commit -m "feat: add contiguous analysis article (draft)"
```

---

## Task 11: Article 8 — "Loop Peeling: Where Most of the Speed Comes From"

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-loop-peeling.md`

- [ ] **Step 1: Write the article**

~1200 words. Draw from `novel-patterns.md` §4, `implementer-notes.md` §3.5.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-loop-peeling.md
git commit -m "feat: add loop peeling article (draft)"
```

---

## Task 12: Article 9 — "We Built Cross-Lane SIMD Primitives. None of Them Helped."

**Files:**
- Create: `bluebugs.github.io/content/blogs/spmd-negative-result.md`

- [ ] **Step 1: Write the article**

~1500 words. Draw from `novel-patterns.md` §13, `blog-to-poc-retrospective.md` §3, `developer-guide.md` §6.2.

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-negative-result.md
git commit -m "feat: add cross-lane negative result article (draft)"
```

---

## Task 13: Cross-Link All Articles

**Files:**
- Modify: all 9 article files in `content/blogs/`

- [ ] **Step 1: Add "Further reading" section to each article**

Each article gets a closing section that links to related articles. Pattern:

- Article 1 (marketing) links to all others.
- Article 2 (developer guide) links to 1 (motivation), 4 (patterns), 9 (negative result).
- Article 3 (compiler internals) links to 1 (motivation), 4-8 (technique details), 9 (negative result).
- Articles 4-8 (deep-dives) link to 1 (pitch) and 3 (compiler context).
- Article 9 (negative result) links to 1 (pitch) and 4 (pattern matching — the positive counterpart).

Also link back to the original blog series ("Previous series: Data Parallelism blog posts").

- [ ] **Step 2: Commit**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-*.md
git commit -m "feat: cross-link all SPMD results articles"
```

---

## Task 14: Final Review and Publish

- [ ] **Step 1: Run Hugo build and check for errors**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
hugo --buildDrafts 2>&1 | grep -i "error\|warn"
```

Expected: no errors. Warnings about missing images are OK.

- [ ] **Step 2: Review each article in browser**

```bash
hugo server --buildDrafts
```

Open each URL, verify:
- Content renders correctly.
- WASM demos work (Article 1).
- Code blocks syntax-highlight properly.
- Cross-links are not broken.
- No stale 91%/18141/33x numbers anywhere.

- [ ] **Step 3: Remove draft flag from all articles**

Change `draft = true` to `draft = false` in all 9 article front matters.

- [ ] **Step 4: Final commit and push**

```bash
cd /home/cedric/work/SPMD/bluebugs.github.io
git add content/blogs/spmd-*.md
git commit -m "feat: publish SPMD results blog series (9 articles)"
```

Confirm with user before pushing to trigger GitHub Pages deployment.
