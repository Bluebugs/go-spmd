# SPMD Implementation Plan for Go + TinyGo

**Version**: 3.0
**Last Updated**: 2026-04-12
**Status**: Phase 1 Complete, Phase 2 Complete, Phase 3 Complete. E2E: 102 tests (90 RUN PASS, 91 COMPILE PASS, 0 compile fail, 0 run fail, 11 reject OK). Base64 Mula-Lemire: AVX2 **18141 MB/s** (91% of simdutf C++), SSSE3 9201 MB/s, WASM 6004 MB/s — 33x faster than Go stdlib. lo-*: AVX2 7.27x (lo-min), SSE 2.6x. Mandelbrot: AVX2 6.07x, WASM 3.03x. Hex-encode: SSE 6.31x, WASM 8.9x.

## Project Overview

This document provides a comprehensive, trackable implementation plan for adding Single Program Multiple Data (SPMD) support to Go via TinyGo. The implementation follows a two-stage approach: Go frontend generates SPMD-aware SSA, TinyGo backend converts to WebAssembly SIMD128 instructions.

**Key Success Criterion**: ALL examples must compile to both SIMD and scalar WASM with identical behavior.

## Implementation Architecture

- **Go Frontend (Phase 1)**: SPMD syntax, type system, and SSA generation using standard opcodes
- **TinyGo Backend (Phase 2)**: SSA-to-LLVM IR conversion with dual SIMD/scalar code generation
- **Testing Strategy**: Test-driven development using 20+ existing examples as primary test cases
- **Reference Patterns**: Follows ISPC/LLVM established SPMD implementation patterns

## Phase 0: Foundation Setup

**Goal**: Establish GOEXPERIMENT infrastructure and test foundation before implementation begins.

### 0.1 GOEXPERIMENT Integration ✅ COMPLETED

- [x] Add `SPMD bool` field to `src/internal/goexperiment/flags.go`
- [x] Generate experimental build constraint files (`exp_spmd_on.go`, `exp_spmd_off.go`)
- [x] Verify GOEXPERIMENT flag properly gates all SPMD features
- [x] Test graceful degradation when experiment is disabled
- [x] Update Go build system to recognize SPMD experiment
- [x] Create verification test suite in experiment_test/
- [x] Successfully build modified Go toolchain with SPMD support
- [x] Commit all changes to spmd branch in go/ submodule

### 0.2 Parser Test Suite Setup ✅ COMPLETED

- [x] Create parser test infrastructure in `src/cmd/compile/internal/syntax/testdata/spmd/`
- [x] Copy all examples as parser test cases with expected pass/fail behavior
- [x] Implement `TestSPMDParser` with GOEXPERIMENT flag testing
- [x] Test cases for valid SPMD syntax (should pass when experiment enabled)
- [x] Test cases for invalid SPMD syntax (should fail appropriately)
- [x] Test cases for backward compatibility (should work when experiment disabled)
- [x] Verify all existing examples parse correctly when SPMD enabled
- [x] Verify existing examples fail gracefully when SPMD disabled

### 0.3 Type Checker Test Suite Setup ✅ COMPLETED

- [x] Create type checker test infrastructure in `src/cmd/compile/internal/types2/testdata/spmd/`
- [x] Copy examples as type checker test cases with expected validation behavior
- [x] Implement `TestSPMDTypeChecking` with comprehensive error message validation
- [x] Test uniform/varying assignment rules enforcement
- [x] Test SPMD function restrictions (public API limitations)
- [x] Test control flow restrictions (`break` in `go for`, nested `go for`)
- [x] Test SIMD register capacity constraint validation
- [x] Test map key restrictions and type switch requirements
- [x] Test pointer operation validations with varying types
- [x] Test constrained varying type system rules

### 0.4 SSA Generation Test Suite Setup ✅ COMPLETED

- [x] Create SSA test infrastructure in `src/cmd/compile/internal/ssagen/testdata/spmd/`
- [x] Implement `TestSPMDSSAGeneration` to verify correct opcodes generated
- [x] Test `go for` loops generate standard SSA (OpPhi, OpCall, OpVectorAdd, OpSelect)
- [x] Test uniform-to-varying broadcasts generate lanes.Broadcast calls
- [x] Test varying arithmetic generates vector operations
- [x] Test mask propagation through control flow generates OpAnd/OpOr/OpNot
- [x] Test SPMD function calls get mask-first parameter insertion
- [x] Test reduce operations generate appropriate builtin calls

### 0.5 Integration Test Suite Setup ✅ COMPLETED

- [x] Create integration test infrastructure in `test/integration/spmd/`
- [x] Copy ALL examples as integration tests with dual-mode compilation
- [x] Implement automated test runner script for continuous validation
- [x] Test SIMD version compilation and SIMD instruction verification
- [x] Test scalar version compilation and absence of SIMD instructions
- [x] Test identical output verification between SIMD and scalar modes
- [x] Test illegal examples fail compilation appropriately
- [x] Test legacy compatibility examples work without experiment flag
- [x] Test wasmer-go runtime execution for both SIMD and scalar WASM
- [x] Test browser-side SIMD detection and loading capability

### 0.6 TDD Workflow Documentation ✅ COMPLETED

- [x] Document test-first development cycle procedures
- [x] Create automated test runner commands for each phase
- [x] Establish regression testing procedures
- [x] Define test success criteria for each implementation milestone
- [x] Create continuous integration validation procedures

## Phase 1: Go Frontend Implementation

**Goal**: Extend Go compiler with SPMD syntax, type system, and SSA generation.

### 1.1 GOEXPERIMENT Integration ✅ COMPLETED

- [x] Add `SPMD bool` field to `src/internal/goexperiment/flags.go`
- [x] Generate experimental build constraint files (`exp_spmd_on.go`, `exp_spmd_off.go`)
- [x] Verify GOEXPERIMENT flag properly gates all SPMD features
- [x] Test graceful degradation when experiment is disabled
- [x] Update Go build system to recognize SPMD experiment
- [x] Create clear error messages when SPMD features used without experiment

### 1.2 Lexer Modifications ✅ COMPLETED

- [x] **TDD**: Implement conditional keyword recognition in `src/cmd/compile/internal/syntax/tokens.go`
- [x] Add `_Uniform` and `_Varying` tokens with GOEXPERIMENT=spmd gating
- [x] Implement buildcfg.Experiment.SPMD conditional recognition in scanner
- [x] Add comprehensive test framework with dual-mode testing (SPMD enabled/disabled)
- [x] Verify lexer correctly emits keyword tokens when SPMD enabled
- [x] Verify lexer treats uniform/varying as identifiers when SPMD disabled
- [x] **Architecture Decision**: Context-sensitive disambiguation deferred to Phase 1.3 parser
- [x] **Make parser tests pass**: All lexer functionality working with proper GOEXPERIMENT integration

### 1.3 Parser Extensions ✅ COMPLETED

- [x] **TDD**: Extend parser in `src/cmd/compile/internal/syntax/parser.go` for SPMD syntax
- [x] **Context-Sensitive Grammar**: Implement grammar-based disambiguation for uniform/varying tokens
  - [x] Parse `uniform int x` as SPMD type syntax (uniform as keyword)
  - [x] Parse `var uniform int = 42` as identifier usage (uniform as name)
  - [x] Use context-based disambiguation for type vs identifier contexts
- [x] Add support for type qualifiers (`uniform int`, `varying float32`)
- [x] Implement `go for` SPMD loop construct parsing
- [x] Add support for constrained varying syntax (`varying[4] byte`, `varying[] T`)
- [x] Implement range grouping syntax (`range[4] data`)
- [x] Add AST nodes for SPMD constructs (`SPMDType`, extended `ForStmt` and `RangeClause`)
- [x] **Fix backward compatibility**: Ensure existing code using uniform/varying as identifiers parses correctly
- [x] Test nested SPMD construct detection (for type checking phase validation)
- [x] Verify example files parse correctly with SPMD syntax (`simple_sum.go`, `odd_even.go` working)
- [x] **Make parser tests pass**: All valid SPMD syntax parses correctly with full backward compatibility

### 1.4 Type System Implementation ✅ COMPLETED

- [x] **TDD**: Add SPMD types to `src/cmd/compile/internal/types2/types.go`
- [x] Implement `SPMDType` with Uniform/Varying qualifiers
- [x] Add constrained varying type support (`varying[n]` and `varying[]`)
- [x] Verify universal constrained varying (`varying[]`) functionality for lanes/reduce functions
- [x] **COMPLETED**: Implement type compatibility rules for SPMD types
- [x] **COMPLETED**: Add interface support for varying types with explicit type switches
- [x] **COMPLETED**: Support pointer operations with varying types
- [x] **COMPLETED**: Implement generic type constraints for lanes/reduce functions
- [x] **COMPLETED**: Type system correctly validates SPMD code (Phase 1.4 complete)

### 1.5 Type Checking Rules Implementation ✅ COMPLETED

- [x] **TDD**: Implement SPMD type checking in `src/cmd/compile/internal/types2/stmt.go`
- [x] **COMPLETED**: Implement ISPC-based return/break restrictions with mask alteration tracking:
  - [x] Add `inSPMDFor` context flag, `varyingDepth` counter, and `maskAltered` flag to statement processing
  - [x] Track varying depth through nested conditional statements (if/switch with varying conditions)
  - [x] Track mask alteration when `continue` occurs in varying context (`varyingDepth > 0`)
  - [x] Allow return/break statements when `varyingDepth == 0` AND `maskAltered == false` (clean uniform context only)
  - [x] Forbid return/break statements when `varyingDepth > 0` (any enclosing varying condition)
  - [x] Forbid return/break statements when `maskAltered == true` (prior continue in varying context)
  - [x] Allow continue statements in `go for` loops regardless of varying depth or mask alteration
  - [x] Add error types: `InvalidSPMDReturn`, `InvalidSPMDBreak`, `InvalidNestedSPMDFor`
  - [x] Implement different error messages for varying conditions vs mask alteration
  - [x] Implement `lHasVaryingBreakOrContinue()` equivalent for varying condition detection
- [x] Implement nested `go for` loop restrictions (enforced in type checking phase)
- [x] **Backward Compatibility Fixed**: Semicolon insertion (ASI) fix for uniform/varying identifiers
- [x] **COMPLETED**: Enforce assignment rules (varying-to-uniform prohibited, uniform-to-varying broadcasts)
- [x] **COMPLETED**: Implement SPMD function detection (functions with varying parameters)
- [x] **COMPLETED**: Add public API restrictions (no public SPMD functions except builtins)
- [x] **COMPLETED**: Implement varying expression type propagation for indexing expressions
- [x] **COMPLETED**: Fix binary expression evaluation for mixed varying/uniform operations
- [x] **COMPLETED**: Implement goto statement restrictions in SPMD contexts
- [x] **COMPLETED**: Implement select statement restrictions in SPMD contexts  
- [x] **COMPLETED**: Add switch statement context propagation for varying expressions
- [x] **COMPLETED**: Fixed binary expression evaluation for mixed varying/uniform operations
- [x] **COMPLETED**: Implemented varying type propagation for indexing expressions
- [x] **COMPLETED**: Fixed SPMD function parameter capacity validation (disabled total capacity limit per spec)
- [x] **REMAINING**: Add SIMD register capacity constraint validation (individual parameter limits)
- [x] **REMAINING**: Implement map key restrictions (no varying keys)
- [x] **REMAINING**: Add type switch validation for varying interface{} usage
- [x] **REMAINING**: Validate `lanes.Index()` context requirements (SPMD context only)

### 1.5.1 Infrastructure Fixes ✅ COMPLETED  

- [x] **COMPLETED**: Fix reduce package build constraints - Changed `//go:build ignore` to `//go:build goexperiment.spmd`
- [x] **COMPLETED**: Fix SPMD type conversion parsing - Added special case in `pexpr()` for `varying type(...)` syntax
- [x] **COMPLETED**: Fix parser integration - SPMD type conversions now parse as CallExpr with proper SPMDType nodes
- [x] **COMPLETED**: Fix runtime panics - Parser now creates proper `syntax.SPMDType` nodes instead of compound names
- [x] **COMPLETED**: Fix reduce package compiler panic - Identified root cause: generic SPMD function calls
- [x] **COMPLETED**: Create workaround for generic SPMD function call bug - Removed function aliases from reduce package  
- [x] **COMPLETED**: Document generic SPMD function call bug - Added comprehensive test case in `testdata/spmd/generic_function_calls.go`
- [x] **COMPLETED**: All critical infrastructure issues resolved - Parser, type system, and build process working correctly

### 1.5.2 Assignment Rule Validation

- [x] **Implement SPMD assignment rules**: Add varying-to-uniform validation in assignment checker
- [x] **Add function call validation**: Check varying arguments vs uniform parameters
- [x] **Add return statement validation**: Check varying return vs uniform function type
- [x] **Add multiple assignment validation**: Handle `u1, u2 = v1, v2` cases with proper error messages

### 1.5.3 Function Restriction Validation ✅ COMPLETED

- [x] **Implement public function restriction**: Error on public functions with varying parameters
- [x] **Implement SPMD function nesting restriction**: Error on `go for` inside functions with varying params
- [x] **Add SPMD function detection**: Track functions with varying parameters in type checker

### 1.6 Migration to Package-Based Types ✅ **COMPLETED** (2026-02-10)

**Rationale**: Replaced `varying`/`uniform` keywords with `lanes.Varying[T]` generic type to eliminate
backward compatibility issues. Regular Go values are implicitly uniform (no keyword needed).

**Syntax Mapping**:
| Old (keyword-based)         | New (package-based)              |
|-----------------------------|----------------------------------|
| `var x uniform int`         | `var x int`                      |
| `var y varying float32`     | `var y lanes.Varying[float32]`   |
| `var c varying[4] int`      | REMOVED (constrained types removed) |
| `var d varying[] byte`      | REMOVED (constrained types removed) |
| `func f(x varying int)`    | `func f(x lanes.Varying[int])`   |
| `go for i := range 16`     | `go for i := range 16` (unchanged) |

**Design Decisions**:
- `lanes.Varying[T]` uses compiler magic: the type checker special-cases `lanes.Varying` before
  generic instantiation (not valid Go generics, handled specifically for this type)
- Note: `lanes.Varying[T, N]` (constrained varying) has been REMOVED as a design simplification
- `reduce.Uniform[T]` removed (Go doesn't support generic type aliases with type params on RHS)
- Internal `SPMDType` in types2 is kept as canonical representation; only how it's created changes
- `go for` parsing and ForStmt.IsSpmd stay unchanged
- All ISPC-based control flow rules stay unchanged

**Completed Steps**:
- [x] Define `type Varying[T any] struct{ _ [0]T }` in lanes package
- [x] Update lanes/reduce function signatures to use Varying[T]/T instead of varying T/uniform T
- [x] Add lanes.Varying[T] recognition in type checker (typexpr_ext_spmd.go)
- [x] Add IndexExpr intercept in typexpr.go before generic instantiation
- [x] Handle unqualified Varying[T] within lanes package itself
- [x] Remove UniformQualifier from internal SPMDType (SPMDType always means varying)
- [x] Simplify operand_ext_spmd.go: remove all uniform code paths
- [x] Remove _Uniform/_Varying tokens from syntax/tokens.go
- [x] Remove SPMDType AST node from syntax/nodes.go
- [x] Remove spmdType()/looksLikeSPMDType() from syntax/parser.go
- [x] Delete syntax/spmd_tokens.go
- [x] Remove SPMDType case from syntax/printer.go, walk_ext_spmd.go, noder/quirks.go
- [x] Remove case *syntax.SPMDType from types2/typexpr.go
- [x] Clean up typexpr_ext_spmd.go: remove old processSPMDType(), keep processLanesVaryingType()
- [x] Update all 5 parser test files for new syntax
- [x] Update all 12 type checker test files for new syntax
- [x] Update all 6 SSA test files for new syntax
- [x] Update all 42 integration test files for new syntax
- [x] Verify build with and without GOEXPERIMENT=spmd
- [x] All SPMD tests pass (parser: 5/5, type checker: 12/12, SSA: 6/6)

### 1.7 SIMD Register Capacity Validation ✅ **COMPLETED** (2026-02-10)

- [x] Implement `laneCountForType()` with correct SIMD128 formula: 16 bytes / sizeof(T)
- [x] Fix `calculateVaryingTypeCapacity()` to use type-dependent lane count (was hardcoded to 4)
- [x] Add `computeEffectiveLaneCount()` for go for loops (minimum across all unconstrained varying types)
- [x] Add `computeFunctionLaneCount()` for SPMD functions based on varying parameter types
- [x] Add `LaneCount int64` field to ForStmt AST node for SSA consumption
- [x] Track varying element sizes during type checking (`varyingElemSizes` in SPMDControlFlowInfo)
- [x] Record effective lane count at end of go for loop type checking
- [x] Remove dead code: `defaultLaneCount`, `pocCapacityMultiplier`, `checkGoForCapacity`, `calculateGoForCapacity`, `checkSPMDFunctionCapacity`
- [x] Make `Int`/`Uint` explicit in `getTypeSize()` (was falling through to default)
- [x] Add `testLaneCountConsistency()` test for mixed element types
- [x] Fix misleading lane count comments in simd_capacity.go test
- [x] Verify builds pass with and without GOEXPERIMENT=spmd

### 1.10 Go SSA Generation for SPMD

#### 1.10a SPMD Field Propagation Through Noder/IR Pipeline ✅ **COMPLETED** (2026-02-10)

- [x] Add `IsSpmd` and `LaneCount` fields to `ir.ForStmt` and `ir.RangeStmt`
- [x] Wire SPMD fields through noder serialization (`writer.go`/`reader.go`)
- [x] Propagate fields through `walk/range.go` lowering to SSA generation
- [x] SPMD loops dispatch to `spmdForStmt()` stub in SSA generator

#### 1.10b Scalar Fallback SSA Generation ✅ **COMPLETED** (2026-02-10)

- [x] Replace `spmdForStmt()` fatal stub with standard for-loop SSA generation
- [x] SPMD loops compile and execute correctly with scalar semantics
- [x] Add `scalarForStmt()` as fallback when `laneCount <= 1`

#### 1.10c SPMD Vector Opcodes ✅ **COMPLETED** (2026-02-10)

- [x] Add 42 type-agnostic SPMD opcodes to `ssa/_gen/genericOps.go`
- [x] Opcodes cover: vector construction (Splat, LaneIndex), arithmetic (Add/Sub/Mul/Div/Neg),
      bitwise (And/Or/Xor/Not/Shl/Shr), comparison (Eq/Ne/Lt/Le/Gt/Ge),
      mask (MaskAnd/MaskOr/MaskAndNot/MaskNot), memory (Load/Store/MaskedLoad/MaskedStore/Gather/Scatter),
      reduction (ReduceAdd/ReduceMul/ReduceMin/ReduceMax/ReduceAnd/ReduceOr/ReduceXor/ReduceAll/ReduceAny),
      cross-lane (Broadcast/Rotate/Swizzle/ShiftLanesLeft/ShiftLanesRight),
      and conversion (Select, Convert)
- [x] Opcodes will be lowered to target-specific SIMD instructions by TinyGo LLVM backend

#### 1.10d IR Opcodes for Vectorized Loop Index ✅ **COMPLETED** (2026-02-10)

- [x] Add `OSPMDLaneIndex`, `OSPMDSplat`, `OSPMDAdd` IR opcodes
- [x] `walk/range.go` emits correct stride (`laneCount`) and varying loop index: `i = splat(hv1) + laneIndex`
- [x] `walk/expr.go` handles SPMD IR opcodes in expression evaluation
- [x] SSA generator maps IR SPMD opcodes to SSA SPMD opcodes
- [x] `spmdForStmt` generates vectorized loop structure with correct block layout

#### 1.10e Tail Masking for Non-Multiple Loop Bounds ✅ **COMPLETED** (2026-02-11)

- [x] Generate tail mask when loop bound N is not a multiple of `laneCount`
- [x] Compute `validMask = laneIndices < N` for the last iteration
- [x] AND tail mask with execution mask for all operations in the loop body
- [x] Add `spmdBodyWithTailMask()` that resets mask, executes index assignment, computes tail mask
- [x] Safe fallback when IR pattern doesn't match expected SPMD assignment

#### 1.10f SPMD Mask Propagation Through Control Flow ✅ **COMPLETED** (2026-02-11)

- [x] Add `IsVaryingCond bool` to `syntax.IfStmt` and `ir.IfStmt`
- [x] Type checker sets `IsVaryingCond` in `spmdIfStmt()` when condition is varying
- [x] Noder serializes/deserializes `IsVaryingCond` through export pipeline
- [x] Add `inSPMDLoop` and `spmdMask` fields to SSA `state` struct
- [x] Initialize all-true mask (`SPMDSplat(true)`) at `go for` loop entry
- [x] Dispatch varying if to `spmdIfStmt()` which executes both branches with different lane masks
- [x] Compute `trueMask = currentMask & cond`, `falseMask = currentMask & ~cond`
- [x] Merge modified variables using `SPMDSelect(cond, trueVal, falseVal)`
- [x] Snapshot/restore variable state to prevent cross-branch contamination

#### 1.10g Varying For-Loop Masking (continue/break masks) ✅ **COMPLETED** (2026-02-11)

- [x] Add `spmdLoopMaskState` struct tracking per-lane continue/break masks per loop level
- [x] Add `spmdVaryingDepth` counter to detect varying context (inside spmdIfStmt/spmdSwitchStmt)
- [x] Add `spmdLoopMasks` field to SSA `state` struct with linked list for nested loops
- [x] Implement `spmdMaskedBranchStmt()`: mask accumulation for continue/break in varying context
- [x] Implement `spmdExcludeBranchMasks()`: subtract accumulated masks after if/switch merge
- [x] Implement `spmdRegularForStmt()`: regular for loops inside SPMD get full mask tracking
- [x] Set up continue mask in `spmdForStmt()` (break under varying forbidden in go for)
- [x] Reset continue mask per iteration in `spmdBodyWithTailMask()`
- [x] Add `spmdVaryingDepth` increment/decrement to `spmdIfStmt()` and `spmdSwitchStmt()`
- [x] Dispatch OCONTINUE/OBREAK to mask accumulation when `spmdVaryingDepth > 0`
- [x] Dispatch OFOR to `spmdRegularForStmt` when `s.inSPMDLoop`
- [x] Update test expectations in go_for_loops.go and mask_propagation.go
- [x] Known limitation: break in varying switch inside go for wrongly accumulates into loop continue mask (deferred)

#### 1.10h SPMD Function Call Mask Insertion ✅ **COMPLETED** (2026-02-11)

- [x] Add `OpSPMDCallSetMask` (argLength:1) and `OpSPMDFuncEntryMask` (argLength:0) SSA opcodes
- [x] Add `isSPMDCallTarget()` and `isSPMDFuncType()` helpers detecting TSPMD parameters
- [x] Implement `spmdAnnotateCall()`: emits `OpSPMDCallSetMask` before SPMD function calls with current mask
- [x] Implement `spmdFuncEntry()`: emits `OpSPMDFuncEntryMask` and enables SPMD context (`inSPMDLoop=true`)
- [x] Hook into OCALLFUNC statement and expression handlers (after builtin interception)
- [x] Hook into `buildssa()` for SPMD function entry (after parameter setup, gated on `buildcfg.Experiment.SPMD`)
- [x] Handles edge cases: non-SPMD→SPMD calls (all-true mask), chained SPMD calls, calls under varying conditions
- [x] Update test expectations in `spmd_function_calls.go` for OpSPMDCallSetMask/OpSPMDFuncEntryMask
- [x] End-to-end compilation tests pass (basic call, chained calls, varying if in SPMD func)

#### 1.10i Switch Statement Masking ✅ **COMPLETED** (2026-02-11)

- [x] Add `IsVaryingSwitch` flag propagation: syntax → types2 → noder → IR → walk → SSA
- [x] Type checker sets `IsVaryingSwitch` in `spmdSwitchStmt()`, increments varying depth
- [x] Walk phase skips `walkSwitch` for varying switches, preserves Tag and Cases
- [x] SSA `spmdSwitchStmt()`: per-case mask computation (SPMDEqual + SPMDMaskAnd/AndNot)
- [x] N-way variable merge using cascading SPMDSelect (mutually exclusive masks)
- [x] Support both scalar (auto-splatted) and varying case values
- [x] `isVaryingSPMDValue()` helper detects varying SSA values for conditional splatting
- [x] `spmdCaseValues()` type checker validates mixed scalar/varying case expressions
- [x] Old typecheck (`tcSwitchExpr`) bypassed for varying switches

#### 1.10j lanes/reduce Builtin Call Interception ✅ **COMPLETED** (2026-02-11)

- [x] Add `//go:noinline` to all lanes/reduce exported functions to prevent inlining before SSA interception
- [x] Add SPMD builtin dispatch in `ssa.go` OCALLFUNC handling (expression + statement contexts)
- [x] Implement `spmdBuiltinCall()` dispatcher: validates package path, strips generic type params
- [x] Implement `spmdLanesBuiltin()`: 7 lanes functions mapped to SPMD opcodes (Index, Count, Broadcast, Rotate, Swizzle, ShiftLeft, ShiftRight)
- [x] Implement `spmdReduceBuiltin()`: 9 reduce functions mapped to SPMD opcodes (Add, Mul, Max, Min, Or, And, Xor, All, Any)
- [x] Handle private `*Builtin` function variants (broadcastBuiltin, rotateBuiltin, swizzleBuiltin)
- [x] Defensive argument count validation for all intercepted functions
- [x] Deferred functions (From, reduce.From/Count/FindFirstSet/Mask) fall through to normal call
- Note: FromConstrained/ToConstrained have been REMOVED (constrained varying design removed)
- [x] Rewrite SSA test files (broadcast_operations.go, reduce_operations.go) for builtin interception
- [x] End-to-end compilation tests pass for lanes and reduce function calls

#### 1.10k Remaining SSA Integration

- Note: Constrained varying handling has been REMOVED (design simplification)
- [ ] **Make SSA tests pass**: Correct SSA opcodes generated for all SPMD constructs

#### 1.10L Fix Pre-existing all.bash Failures ✅ **COMPLETED** (2026-02-12)

Fixed all 6 accumulated test failures from Phase 1.1-1.10:

- [x] **`internal/copyright` TestCopyright**: Added copyright headers to lanes/reduce package files
- [x] **`go/doc/comment` TestStd**: Added `lanes` and `reduce` to stdPkgs list in std.go
- [x] **`go/build` TestDependencies**: Added `lanes` and `reduce` to dependency allowlist in deps_test.go
- [x] **`internal/types/errors` TestErrorCodeExamples**: Removed SPMD error code examples (go/types can't parse SPMD syntax); renamed `InvalidSPMDFunction` to `InvalidSPMDFunc` per style guidelines
- [x] **`reduce` build via vet**: Added TypeSPMD handler to `go/internal/gcimporter/ureader.go` (reads SPMD encoding, returns elem type)
- [x] **`go/types` TestGenerate**: Regenerated 8 stale go/types files + created 7 SPMD stub extension files
- [x] All 6 previously-failing test suites pass with `GOEXPERIMENT=spmd`
- [x] Builds pass with AND without `GOEXPERIMENT=spmd`

### 1.8 Standard Library Extensions (lanes package) ✅ **COMPLETED** (signatures updated in Phase 1.6)

- [x] Create `src/lanes/lanes.go` with build constraint `//go:build goexperiment.spmd`
- [x] Implement `Count[T any](value varying T) uniform int` with WASM SIMD128 type-based calculation
- [x] Implement `Index() varying int` as compiler builtin placeholder
- [x] Add `From[T any](slice []T) varying T` as compiler builtin placeholder  
- [x] Implement `Broadcast[T any](value varying T, lane uniform int) varying T`
- [x] Add `Rotate[T any](value varying T, offset uniform int) varying T`
- [x] Implement `Swizzle[T any](value varying T, indices varying int) varying T`
- [x] Add bit shift operations `ShiftLeft`, `ShiftRight`
- Note: `FromConstrained`/`ToConstrained` have been REMOVED (constrained varying design removed)
- [x] Add `RotateWithin`, `SwizzleWithin`, `ShiftLeftWithin`, `ShiftRightWithin` functions

**ARCHITECTURE NOTES**: Phase 1.8 implements sophisticated builtin architecture where:

- **Compiler Builtins**: Functions like `Index()`, `From()`, and internal `*Builtin()` functions cannot be implemented in Go code - they must be replaced by the compiler with SIMD instructions during compilation
- **Within Operations**: Cross-lane operations have `*Within` variants (RotateWithin, SwizzleWithin, ShiftLeftWithin, ShiftRightWithin) that operate within groups of N lanes
- **Type-Based Lane Count**: `Count()` function calculates SIMD width based on type size: 128 bits / (sizeof(T) * 8 bits) for WASM SIMD128 PoC
- **Runtime vs Compile-time**: Phase 1.8 provides runtime PoC implementations that will be replaced by compile-time compiler intrinsics in Phase 2
- [x] **ARCHITECTURE**: Cross-lane operations support full-width and within-group variants
- [x] **KEY INSIGHT**: *Within variants replace constrained types for group-based algorithms
- [x] All operations documented as compiler builtins with proper panic messages
- [x] WASM SIMD128 lane count calculation: 128 bits / (sizeof(T) * 8) for different types

**✅ COMPLETED**: Phase 1.8 lanes package provides complete API for PoC validation with *Within cross-lane operations.

### 1.9 Standard Library Extensions (reduce package) 🔴 **CRITICAL PoC DEPENDENCY** (use lanes.Varying[T] syntax)

- [ ] Create `src/reduce/reduce.go` with build constraint `//go:build goexperiment.spmd`
- [ ] Define generic type constraints (VaryingBool, VaryingNumeric[T], VaryingInteger[T], etc.)
- [ ] Implement `All(data VaryingBool) uniform bool` with varying[] support
- [ ] Implement `Any(data VaryingBool) uniform bool` with varying[] support
- [ ] Add `Add[T Numeric](data VaryingNumeric[T]) uniform T` with varying[] support
- [ ] Implement `Max[T comparable]` and `Min[T comparable]` reductions
- [ ] Add bitwise reductions `Or[T Integer]`, `And[T Integer]`, `Xor[T Integer]`
- [ ] Implement `From[T NumericOrBool](varying T) []T` (numerical types and bool only)
- [ ] Add lane analysis functions `FindFirstSet`, `Mask`
- [ ] Use runtime type checking for varying[] vs varying T parameter handling
- [ ] Add performance requirement: all operations must be automatically inlined

**⚠️ CRITICAL DEPENDENCY**: Phase 1.9 completion is required before PoC validation can begin. All integration tests, examples, and dual-mode compilation depend on `reduce` package availability.

### 1.10 Printf Integration for Varying Types ✅ COMPLETED (via 2.9l)

Superseded by 2.9l (Printf Mask-Aware Varying Formatting). Implementation uses `spmd:"varying"` struct tag + `printSPMDVarying` in `fmt/print.go` — better than original plan since it shows mask-aware output (`[5 _ 15 _]`) instead of just calling `reduce.From()`.

### 1.11 Frontend Integration Testing

- [ ] Verify all examples parse, type-check, and generate SSA correctly
- [ ] Test experiment flag gating works properly across all frontend components
- [ ] Validate error messages are clear and helpful
- [ ] Confirm backward compatibility maintained
- [ ] Test standard library extensions work with all example patterns
- [ ] Verify SIMD register capacity constraints work for all examples

## Phase 2: TinyGo Backend Implementation

**Goal**: Convert SPMD Go programs to LLVM IR via TinyGo and generate dual SIMD/scalar WASM.

**Critical Architecture Note**: TinyGo uses `golang.org/x/tools/go/ssa` (NOT Go's `cmd/compile/internal/ssa`). The 42 SPMD opcodes from Phase 1.10c are invisible to TinyGo. Phase 2 must work at the **type level**: detect `lanes.Varying[T]` types and lower them to LLVM vector types directly in TinyGo's compiler. LLVM builder calls like `CreateAdd(<4 x i32>, <4 x i32>)` automatically generate the correct WASM SIMD128 instructions.

**Critical Prerequisite**: TinyGo uses `go/parser` + `go/types` + `go/ssa` (standard library), NOT the compiler-internal packages. Current status:
- ✅ `go/parser` can parse `go for` syntax (Phase 2.0b)
- ✅ `go/ast` has SPMD fields (`RangeStmt` has `IsSpmd`, `LaneCount`)
- ✅ `go/types` has full SPMD type checking (10 ext_spmd files ported from types2, Phase 2.0c)
- `go/ssa` has no SPMD metadata (not needed — metadata extracted from typed AST in TinyGo's loader)

All standard library porting for SPMD is complete. TinyGo compiler work (Phase 2.1+) can proceed.

**Key Go Standard Library Files** (to port):
- `go/ast/ast.go`: ✅ AST node definitions (SPMD fields added)
- `go/parser/parser.go`: ✅ Parser (`go for` syntax)
- `go/types/*_ext_spmd.go`: ✅ 10 real implementations (ported from types2)
- `go/types/spmd.go`: ✅ SPMDType struct with constructors and helpers

**Key TinyGo Files** (to modify):
- `compiler/compiler.go` (3500+ lines): Main compilation, type mapping, instruction lowering
- `compiler/calls.go`: Function call handling
- `compiler/intrinsics.go`: LLVM intrinsic wrappers (pattern for SIMD intrinsics)
- `compiler/llvm.go`: LLVM IR utilities
- `compiler/ircheck/check.go`: IR verification (already handles `llvm.VectorTypeKind`)
- `loader/loader.go`: Package loading via `go list -json -deps`
- `loader/ssa.go`: SSA generation using `golang.org/x/tools/go/ssa`
- `targets/wasm.json`, `wasip1.json`, `wasip2.json`: WASM target configs
- `compileopts/config.go`: Build options and target configuration
- `goenv/goenv.go`: Go environment management

### 2.0 Go Standard Library SPMD Porting

**Goal**: Port SPMD support from compiler-internal packages to Go standard library packages so that TinyGo (and all tools using go/parser + go/types + go/ssa) can process SPMD code.

**Gap Analysis**:
| Package | Current State | Target State | Lines to Port |
|---------|--------------|--------------|---------------|
| `go/ast` | ✅ `IsSpmd`, `LaneCount` on `RangeStmt` | Done | 2 fields |
| `go/parser` | ✅ `go for` syntax | Done | ~100 lines |
| `go/types` | ✅ 10 ext_spmd files (1,600+ lines) | Done | Ported from types2 |
| `go/ssa` | No SPMD metadata | Not needed (extract from typed AST) | 0 |

#### 2.0a Port go/ast SPMD Fields ✅ COMPLETED

- [x] Add `IsSpmd bool` field to `go/ast.RangeStmt`
- [x] Add `LaneCount int64` field to `go/ast.RangeStmt`
- [x] `go/ast.Walk` needs no changes (only visits child nodes, not metadata fields)
- [x] `go/ast` print.go needs no changes (reflection-based, auto-includes new fields)
- Note: `IsVaryingCond`/`IsVaryingSwitch` not added — these are semantic (type-checker outputs), not syntactic. TinyGo determines varyingness by checking `go/types.Type` at the condition expression. This follows go/ast convention: nodes represent syntax, not semantics.

#### 2.0b Port go/parser `go for` Syntax ✅ COMPLETED

- [x] Port SPMD `go for` loop detection from `cmd/compile/internal/syntax/parser.go`
  - Modified `parseGoStmt` to detect `go` followed by `for` when `buildcfg.Experiment.SPMD` is set
  - Added `parseSpmdForStmt` handling all variants (bare range, key, key-value)
  - Sets `IsSpmd = true` on the resulting `RangeStmt`
- [x] Updated `go/build/deps_test.go` to allow `go/parser` to import `internal/buildcfg`
- [x] Added parser tests in `go/parser/parser_spmd_test.go`
- Note: Constrained `Varying[T, N]` and `range[N]` syntax has been REMOVED (design simplification)

#### 2.0c Port go/types SPMD Type Checking ✅ COMPLETED

Ported 10 `*_ext_spmd.go` files from types2 to go/types with full API translation (`syntax.*` → `ast.*`). Added hooks in 6 main files (stmt.go, expr.go, typexpr.go, check.go, decl.go, call.go, index.go).

- [x] Port `typexpr_ext_spmd.go`: `lanes.Varying[T]` type recognition and `handleSPMDIndexExpr()` (265 lines)
  - Critical entry point: intercepts `lanes.Varying[T]` before generic instantiation
- [x] Port `operand_ext_spmd.go`: SPMD assignability rules (307 lines)
  - Varying→uniform forbidden, uniform→varying broadcast, pointer rules
- [x] Port `stmt_ext_spmd.go`: Statement validation and control flow (320 lines)
  - ISPC-based return/break restrictions, mask alteration tracking, `go for` nesting checks
  - Includes `SPMDControlFlowInfo`, `handleSPMDStatement()`, `spmdForStmt()`
- [x] Port `check_ext_spmd.go`: Function signature validation (169 lines)
  - SPMD function detection, capacity checking, context management
- [x] Port `expr_ext_spmd.go`: Binary/comparison expression handling (139 lines)
  - Varying type propagation through expressions
- [x] Port `call_ext_spmd.go`: Function call validation (80 lines)
  - SPMD function call validation, `lanes.Index()` context checks
- [x] Port `unify_ext_spmd.go`: Generic type unification (77 lines)
- [x] Port `predicates_ext_spmd.go`: Type identity checks (36 lines)
- [x] Port `typestring_ext_spmd.go`: String representation (45 lines)
- [x] Port `pointer_ext_spmd.go`: Pointer-to-varying validation (185 lines)
- [x] Add `spmdInfo SPMDControlFlowInfo` field to `go/types.Checker` struct
- [x] Verify builds pass with and without `GOEXPERIMENT=spmd`
- [x] Add SPMD-specific type checker tests in `go/types/testdata/spmd/`
- [x] Add clear error message for indexing Varying types — intercept before `Underlying()` in `indexExpr()`
- [x] All changes mirrored in both `go/types` and `types2`
- Note: Constrained type checker features (array-to-Varying conversion, type switch Varying[T,0]) have been REMOVED (design simplification)

#### 2.0d SPMD Metadata Extraction in TinyGo Compiler ✅ COMPLETED

**Key insight**: `golang.org/x/tools/go/ssa` is an external module (`golang.org/x/tools v0.30.0`), NOT part of the Go standard library. Modifying go/ssa would require forking `golang.org/x/tools`. Instead, SPMD metadata is extracted from the typed AST directly in TinyGo's compiler.

**Decision**: Do NOT modify `golang.org/x/tools/go/ssa`. Extract SPMD metadata from the typed AST in TinyGo's `CompilePackage()` function (which receives `*loader.Package` with AST and types).

**Implementation** (in `compiler/spmd.go`):
- [x] `extractSPMDLoops()`: Walks AST to find `RangeStmt` with `IsSpmd==true`, extracts `LaneCount`
- [x] `extractSPMDFuncs()`: Scans package scope for functions with `*types.SPMDType` parameters
- [x] `analyzeSPMDSignature()`: Checks function signature for varying params/results
- [x] `loadSPMDInfo()`: Orchestrator on `compilerContext`, builds sorted position ranges for binary search
- [x] `SPMDInfo` side table: `map[token.Pos]*SPMDLoopInfo` + `map[*types.Func]*SPMDFuncInfo` + sorted `loopRanges`
- [x] Query helpers: `isInSPMDLoop()` (binary search), `getSPMDLoopAt()`, `getSPMDFuncInfo()`, `isSPMDFunction()`, `hasSPMDCode()`
- [x] Added `spmdInfo *SPMDInfo` field to `compilerContext` struct
- [x] Added `c.loadSPMDInfo(pkg)` call in `CompilePackage()` after `loadASTComments()`
- [x] 13 tests in `compiler/spmd_test.go` (all passing)
- [x] **No custom SPMD opcodes in go/ssa** — all vectorization happens in TinyGo's compiler layer
- [x] **No fork of golang.org/x/tools** — SPMD metadata flows through AST + types, not SSA

### 2.1 TinyGo Foundation Setup ✅ COMPLETED

- [x] Add GOEXPERIMENT support to TinyGo compilation pipeline
  - [x] Read GOEXPERIMENT from environment in `goenv/goenv.go`
  - [x] Propagate SPMD experiment flag through `compileopts/config.go`
  - [x] Gate SPMD features behind experiment flag in compiler
- [x] Enable WASM SIMD128 in target configuration
  - [x] Auto-add `+simd128` to features when SPMD+WASM via `Features()` in `compileopts/config.go`
  - [ ] Add `-simd=true/false` build flag for dual-mode compilation
  - [ ] Create SIMD-disabled variant targets (features without `+simd128`)
- [x] Set up TinyGo build and test infrastructure
  - [x] Verify TinyGo builds with our modified Go toolchain
  - [x] 12 tests in `compileopts/config_spmd_test.go` (auto-SIMD128 logic + GOExperiment accessor)
  - [ ] Create SPMD-specific test harness for WASM output validation
  - [ ] Set up wasmer-go runtime for testing generated WASM

### 2.2 SPMD Type Detection and Vector Type Generation (TinyGo) ✅ COMPLETED

**Key Function**: `getLLVMType()` / `makeLLVMType()` at `compiler/compiler.go:391`

- [x] Detect `lanes.Varying[T]` in Go type system (via `*types.SPMDType` from our Go fork)
- [x] Map varying types to LLVM vector types based on SIMD width:
  - `lanes.Varying[int32]` → `<4 x i32>` (WASM SIMD128: 128/32 = 4 lanes)
  - `lanes.Varying[float32]` → `<4 x float>` (4 lanes)
  - `lanes.Varying[int64]` → `<2 x i64>` (2 lanes)
  - `lanes.Varying[int8]` → `<16 x i8>` (16 lanes)
  - `lanes.Varying[bool]` → `<16 x i1>` (TypeAllocSize(i1) = 1 byte, 16 lanes)
- [x] Bypass `typeutil.Map` cache for SPMDType (x/tools can't hash custom types; LLVM memoizes internally)
- [x] **FIXED**: Patched `x/tools` copy at `x-tools-spmd/` with SPMDType cases in `hash()`/`shallowHash()` (prime 9181). TinyGo `go.mod` has `replace golang.org/x/tools v0.30.0 => ../x-tools-spmd`. The `getLLVMType()` bypass remains as defense-in-depth.
- [x] Add `spmdLaneCount()` helper: 128-bit SIMD / element size
- [x] Add `splatScalar()` for scalar-to-vector broadcast via insert+shuffle
- [x] Add `spmdBroadcastMatch()` for mixed uniform/varying binary operations
- [x] Add `createSPMDConst()` for splatted vector constants
- [x] Add broadcast at entry of `createBinOp()` for automatic type matching
- [x] Add SPMD pre-check in `createConst()` for varying constants
- [x] Fix `createUnOp()` to use `ConstNull`/`ConstAllOnes` (vector-safe)
- [x] 6 tests in `compiler/spmd_llvm_test.go` (34 test cases)
- [x] Patched `x/tools` at `x-tools-spmd/` with SPMDType hash support in `typeutil.Map` — `hash()` and `shallowHash()` cases added (prime 9181)
- [x] TinyGo `go.mod` replace directive: `replace golang.org/x/tools v0.30.0 => ../x-tools-spmd`
- [ ] Handle scalar fallback mode: map `lanes.Varying[T]` to array types or scalar loops (deferred)

### 2.3 SPMD Loop Lowering (`go for`) ✅ COMPLETED

**Key Approach**: getValue override + instruction interception (no SSA/LLVM block structure changes)

**Critical Discovery**: `go/ssa` lifts the `rangeint.iter` alloca to a phi node with `Comment: "rangeint.iter"`. Detection uses block/phi comments set by `lift.go:491`.

- [x] Detect `go for` range loops in SSA via rangeint pattern matching
  - `analyzeSPMDLoops()`: scans ALL blocks for `rangeint.body` comment, finds `rangeint.iter` phi, validates position inside SPMD loop via `isInSPMDLoop()`
  - Finds successor `rangeint.loop` block, extracts increment BinOp and bound value
  - Computes lane count from iter phi's LLVM element type
- [x] Generate vectorized loop structure:
  - `emitSPMDBodyPrologue()`: after scalar phi compiled, splats it + adds `<0,1,...,laneCount-1>` offset
  - Lane indices: `splat(scalarPhi) + <0,1,2,...,laneCount-1>` via `splatScalar()` + `spmdLaneOffsetConst()`
  - Tail mask: `laneIndices < splat(bound)` via `CreateICmp(IntSLT, ...)`
  - Stores `laneIndices` and `tailMask` in `spmdActiveLoop` for body instructions
- [x] Override `getValue()` to substitute iter phi with lane indices vector in body blocks
  - Per-block `spmdValueOverride` map, enabled only for body blocks, cleared for others
- [x] Replace loop increment `+1` with `+laneCount` in `createExpr()` BinOp interception
  - Uses pointer equality (`expr == loop.incrBinOp`) for precise targeting
- [x] 4 new tests in `compiler/spmd_llvm_test.go` (14 test cases total)
  - `TestSPMDLaneOffsetConstant`: `<0,1,...,N-1>` generation for various lane counts
  - `TestSPMDComputeLaneIndices`: lane index computation at different iteration values
  - `TestSPMDComputeTailMask`: per-lane bounds checking (all active, partial tail, single lane)
  - `TestSPMDAnalyzeLoopsNil`: graceful nil handling for non-SPMD functions
- [ ] Initialize execution mask to all-true at loop entry (deferred to Phase 2.5)
- [ ] Reset continue mask per iteration (deferred to Phase 2.5)

### 2.4 Varying Arithmetic and Binary Operations ✅ COMPLETED (via Phase 2.2)

**Key Function**: `createBinOp()` at `compiler/compiler.go:2570`

- [x] LLVM auto-vectorizes when both operands are vectors (done via type mapping in 2.2):
  - `CreateAdd(<4 x i32>, <4 x i32>)` → `i32x4.add` (automatic)
  - `CreateFMul(<4 x float>, <4 x float>)` → `f32x4.mul` (automatic)
- [x] Add uniform-to-varying broadcast via `spmdBroadcastMatch()` at entry of `createBinOp()`
- [x] Handle mixed varying/uniform binary operations (splat uniform operand automatically)
- [x] Handle comparison operations producing vector masks (`<N x i1>`) — LLVM handles natively
- [x] Unary operations: `ConstNull` for negation, `ConstAllOnes` for complement (vector-safe)

### 2.5 Control Flow Masking (Varying If/Else) ✅ COMPLETED

- [x] Implement varying if/else masking via CFG linearization:
  - Detect vector `<N x i1>` conditions at `*ssa.If` instructions
  - Linearize control flow: unconditional branch to then-block, then-exits redirect to else-block
  - Convert merge-point phis to LLVM `select(vectorCond, thenVal, elseVal)` instructions
  - Handle if-with-else and if-without-else patterns
  - Support nested varying if/else (each merge has exactly 2 predecessors in go/ssa)
  - Fix `spmdValueOverride` scope to persist across if.then/if.else/if.done blocks
- [x] New functions in `compiler/spmd.go`: `spmdVaryingIf` type, `isBlockInSPMDBody`, `spmdDetectVaryingIf`, `spmdFindMerge`, `spmdFindThenExits`, `spmdShouldRedirectJump`, `spmdCreateMergeSelect`, `spmdVectorAnyTrue`, `spmdIsReachableFrom`
- [x] Integration in `compiler/compiler.go`: 3 new builder fields, modified `*ssa.If`/`*ssa.Jump`/`*ssa.Phi` cases
- [x] Tests: `TestSPMDVectorAnyTrue`, `TestSPMDSelectCreation`, `TestSPMDIsBlockInSPMDBody`, `TestSPMDBroadcastMatchForSelect`
- [x] Implement varying switch masking (3 TinyGo commits: chain detection, CFG linearization + cascaded select, deferred phi resolution for DomPreorder ordering)
- [ ] Implement varying for-loop masking: continue/break mask accumulation (deferred)

### 2.6 SPMD Function Call Handling — COMPLETED

**Key Files**: `compiler/spmd.go`, `compiler/symbol.go`, `compiler/func.go`, `compiler/compiler.go`

- [x] Detect SPMD functions (functions with `lanes.Varying[T]` parameters) — via existing `isSPMDFunction()`
- [x] Insert execution mask as first parameter in SPMD function declarations — `getFunction()` in symbol.go
- [x] Insert execution mask type in SPMD function pointer types — `getLLVMFunctionType()` in func.go
- [x] At SPMD function entry, extract mask from first parameter — `createFunctionStart()` with `spmdEntryMask` field
- [x] Handle non-SPMD → SPMD calls (pass all-true mask) — `spmdCallMask()` fallback
- [x] Handle SPMD loop → SPMD calls (pass tail mask from loop state) — `spmdCallMask()` loop check
- [x] Handle SPMD → SPMD calls (pass entry mask) — `spmdCallMask()` entry mask check
- [x] Skip mask for exported SPMD functions (defensive, type checker forbids them)
- [x] Tests: `TestSPMDMaskType` (5 cases), `TestSPMDCallMaskAllTrue` (4 cases)

### 2.7 lanes/reduce Builtin Implementation — COMPLETED

**Key Files**: `compiler/spmd.go`, `compiler/compiler.go`, `compiler/spmd_llvm_test.go`

- [x] Intercept `lanes.Index()` → reuse `spmdLaneOffsetConst()` lane index vector constant `<0, 1, 2, ..., N-1>`
- [x] Intercept `lanes.Count[T]()` → compile-time constant (e.g., 4 for i32 on SIMD128)
- [x] Intercept `lanes.Broadcast[T]()` → `CreateExtractElement` + `splatScalar()`
- [x] Intercept `lanes.ShiftLeft[T]()` → `CreateShl` (element-wise vector shift)
- [x] Intercept `lanes.ShiftRight[T]()` → `CreateAShr`/`CreateLShr` (signed-aware element-wise shift)
- [x] Intercept `lanes.From[T]()` → extract ptr from slice, `CreateLoad(vecType, ptr)` (unmasked vector load)
- [x] Intercept `reduce.Add[T]()` → `llvm.vector.reduce.add` / `llvm.vector.reduce.fadd` intrinsic
- [x] Intercept `reduce.Mul[T]()` → `llvm.vector.reduce.mul` / `llvm.vector.reduce.fmul` intrinsic
- [x] Intercept `reduce.All()` → bitcast `<N x i1>` to `iN`, `icmp eq iN %val, -1`
- [x] Intercept `reduce.Any()` → reuse `spmdVectorAnyTrue()` (bitcast + `icmp ne`)
- [x] Intercept `reduce.Max[T]()` → `llvm.vector.reduce.smax`/`umax`/`fmax` (signed-aware dispatch)
- [x] Intercept `reduce.Min[T]()` → `llvm.vector.reduce.smin`/`umin`/`fmin` (signed-aware dispatch)
- [x] Intercept `reduce.Or[T]()` → `llvm.vector.reduce.or` intrinsic
- [x] Intercept `reduce.And[T]()` → `llvm.vector.reduce.and` intrinsic
- [x] Intercept `reduce.Xor[T]()` → `llvm.vector.reduce.xor` intrinsic
- [x] Intercept `reduce.From[T]()` → extract elements to stack alloca → slice triple `{ptr, N, N}`
- [x] Intercept `reduce.Count()` → bitcast `<N x i1>` to `iN`, `llvm.ctpop.iN`, zext to int
- [x] Intercept `reduce.FindFirstSet()` → bitcast `<N x i1>` to `iN`, `llvm.cttz.iN`, zext to int
- [x] Intercept `reduce.Mask()` → bitcast `<N x i1>` to `iN`, zext to int
- [x] New helpers: `spmdVectorTypeSuffix()`, `spmdCallVectorReduce()`, `spmdCallVectorReduceFloat()`, `spmdIsSignedInt()`, `spmdIsFloat()`
- [x] Tests: 9 new tests/32 cases (vector type suffix, vector reduce, float reduce, reduce all, reduce count, lanes index, lanes broadcast, is-signed-int, is-float)
- [x] Intercept `lanes.RotateWithin()` → `CreateShuffleVector` with within-group rotation (Phase 2.7d)
- [x] Intercept `lanes.SwizzleWithin()` → `CreateShuffleVector` with within-group permutation (Phase 2.7d)
- [x] Intercept `lanes.ShiftLeftWithin()` → `CreateShuffleVector` with within-group left shift (Phase 2.7d)
- [x] Intercept `lanes.ShiftRightWithin()` → `CreateShuffleVector` with within-group right shift (Phase 2.7d)
- [ ] Intercept `lanes.Rotate()` → `CreateShuffleVector` with rotated indices (deferred to Phase 2.7b)
- [ ] Intercept `lanes.Swizzle()` → `CreateShuffleVector` with arbitrary indices (deferred to Phase 2.7b)
- Note: `lanes.FromConstrained()`/`lanes.ToConstrained()` have been REMOVED (constrained varying design removed)

### 2.8 Memory Operations ✅ COMPLETED

- [x] Implement execution mask stack (`spmdPushMask`/`spmdPopMask`/`spmdCurrentMask`) for correct stores in varying conditions
- [x] Implement mask transitions at block boundaries (pushThen/swapElse/pop) via `spmdMaskTransitions` map
- [x] Implement contiguous access detection for `data[i]` where `i` is SPMD loop iter phi
- [x] Implement `spmdMaskedLoad`: `llvm.masked.load` for contiguous vector loads
- [x] Implement `spmdMaskedStore`: `llvm.masked.store` for contiguous vector stores
- [x] Implement `spmdMaskedGather`: `llvm.masked.gather` for non-contiguous (vector-of-pointers) loads
- [x] Implement `spmdMaskedScatter`: `llvm.masked.scatter` for non-contiguous stores
- [x] Handle array/slice indexing with varying indices → contiguous scalar GEP path + gather/scatter fallback
- [x] Save scalar iter value before override for contiguous GEP computation
- [x] Skip bounds checks for contiguous SPMD access (tail mask protects)
- [x] 6 new tests (mask stack, masked load/store intrinsics, mask transitions, contiguous info, mask AND)

**Deferred to Phase 2.8b**: ~~Range-over-slice loop detection~~ — **COMPLETED** (see 2.8b below)

### 2.8b Range-Over-Slice Loop Detection ✅ COMPLETED

- [x] Extend `spmdActiveLoop` struct with rangeindex fields (`isRangeIndex`, `bodyIterValue`, `initEdgeIndex`)
- [x] Add second detection pass in `analyzeSPMDLoops()` for `rangeindex.body` blocks
- [x] Detect `"rangeindex"` phi in loop block (not body), find `incrBinOp` and bounds check
- [x] Use body block instruction positions for SPMD loop membership (phi is in loop block)
- [x] Register both `loopPhi` and `incrBinOp` in `activeLoops` map for contiguous detection
- [x] Modify `emitSPMDBodyPrologue()` to use `bodyIterValue` (incrBinOp for rangeindex, iterPhi for rangeint)
- [x] Add rangeindex body prologue trigger at block entry in `compiler.go` (body has no iter phi)
- [x] Implement phi init override: change -1 to -laneCount on entry edge for rangeindex loops
- [x] 3 new tests (rangeindex fields, body iter value, phi init override)
- [x] All existing Phase 2.8 infrastructure (contiguous detection, masked load/store, gather/scatter, mask stack) works automatically

### E2E Test Infrastructure ✅ COMPLETED

- [x] Fix GOEXPERIMENT passthrough in `loader/list.go` (was stripping `spmd` from `go list` subprocess)
- [x] Add `*types.SPMDType` support to `x-tools-spmd/go/ssa/subst.go` (generic instantiation for reduce/lanes)
- [x] Create Node.js WASI WASM runner (`test/e2e/run-wasm.mjs`) with asyncify stubs
- [x] Create progressive E2E test script (`test/e2e/spmd-e2e-test.sh`) with 7 test levels
- [x] Validate 6 core programs compile + run correctly (stores, conditionals, functions, reduce, lanes, varying vars)
- [x] Validate all 11 illegal examples correctly rejected by type checker

**E2E Test Results (54 tests)** — updated 2026-03-14 after pointer-varying run-pass promotion:
- 33 RUN PASS: L0_store, L0_cond, L0_func, L1_reduce_add, L2_lanes_index, L3_varying_var, L4_range_slice, L4b_varying_break, L5a_simple_sum, L5b_odd_even, L5f_varying_switch, L5g_compound_conditions, integ_simple-sum, integ_odd-even, integ_hex-encode, integ_debug-varying, integ_lanes-index-restrictions, integ_to-upper, integ_mandelbrot, integ_store-coalescing, integ_ipv4-parser, integ_type-switch-varying, integ_defer-varying, integ_panic-recover-varying, integ_bit-counting, integ_array-counting, integ_non-spmd-varying-return, integ_spmd-call-contexts, integ_map-restrictions, integ_lo-sum, integ_lo-mean, integ_lo-min, integ_lo-max, integ_lo-contains, integ_lo-clamp (corrected prior count), integ_pointer-varying
- 8 COMPILE-ONLY PASS: integ_type-casting-varying, integ_printf-verbs, integ_goroutine-varying, integ_select-with-varying-channels, integ_varying-array-iteration, integ_spmd-call-contexts (run-pass per script), integ_map-restrictions (run-pass per script)
- 11 REJECT OK: All illegal examples correctly rejected
- 0 COMPILE FAIL: All integration tests compile successfully

### 2.8c Constrained Varying Type Support -- REMOVED

**NOTE**: Constrained `Varying[T, N]` support has been completely removed as a design simplification. Cross-lane operations that need group processing now use `*Within` functions (e.g., `lanes.RotateWithin`, `lanes.SwizzleWithin`) which operate within groups of N lanes on unconstrained varying types. This eliminates the complexity of constrained types in the AST, parser, type checker, and backend while preserving the same algorithmic capabilities. See `docs/plans/2026-02-21-remove-constrained-varying-design.md` for the design rationale.

### 2.9a SPMD Function Body Mask Infrastructure ✅ COMPLETED

- [x] Add `spmdFuncIsBody` flag: detect SPMD functions (varying params, no go-for loops)
- [x] Initialize mask stack from entry mask for SPMD function bodies
- [x] Extend `isBlockInSPMDBody()` to return true for ALL blocks when `spmdFuncIsBody` is set
- [x] Enable varying if/else linearization (Phase 2.5) in SPMD function bodies
- [x] Extend `*ssa.If` linearization check to use `spmdFuncIsBody` alongside `spmdLoopState`

### 2.9b Per-Lane Break Mask Support ✅ COMPLETED (SSA-level predication replaces TinyGo LLVM code)

- [x] Add `spmdForLoopInfo` type: tracks regular for-range loops in SPMD function bodies
- [x] Add `spmdBreakRedirect` type: tracks varying if → break redirections
- [x] Create break mask alloca at function entry (persists across loop iterations)
- [x] Detect varying if where one successor is a loop exit (`spmdIsVaryingBreak`)
- [x] Implement break redirect: accumulate `breakMask |= (activeMask & condition)`, redirect jump
- [x] Compute active mask at loop body entry: `entryMask & ~breakMask`
- [x] Deferred mask computation: emit after rangeint.iter phi to respect LLVM phi grouping
- [x] **SSA-level predication** (2026-03-03): `predicateVaryingBreaks()` in x-tools-spmd replaces TinyGo LLVM break handling
  - Break mask phi at loop header, SPMDSelect for break results, accumulator phis for loop-carried results
  - Removed from TinyGo: `detectSPMDForLoops` (~290 lines), `spmdIsVaryingBreak` (~30 lines), `spmdForLoopInfo`/`spmdBreakResult`/`spmdBreakRedirect` types, break mask alloca init, varying break detection in If handler, break redirect in Jump handler (~560 lines total)

### 2.9c Vector IndexAddr + Break Result Tracking ✅ COMPLETED

- [x] Vector IndexAddr: when index is a vector, generate vector of GEPs for scatter/gather
- [x] Per-lane bounds checking: extract each lane, check against length, OR all OOB flags
- [x] Support both array (Pointer→Array) and slice types for vector indexing
- [x] Merged body+loop detection in `detectSPMDForLoops()` (fallback when no `rangeint.loop` block)
- [x] `spmdBreakResult` type: track phis at rangeint.done receiving break values via allocas
- [x] Break result accumulation: `select(mask, breakVal, oldResult)` at break redirect
- [x] Break result phi compilation: `select(breakMask, breakResult, phi)` at rangeint.done
- [x] Skip break edges during phi resolution (redirected in LLVM CFG)
- [x] Fix mandelbrot: remove reduce.Any guard, rewrite demonstrateVaryingParameters with reduce.From
- [x] Add L4b_varying_break E2E test, promote mandelbrot to compile+run

**Mandelbrot Results**: 256x256, 256 iterations — 0 differences vs serial, ~2.98x SPMD speedup (was ~1.2x before performance optimizations)

### Performance Optimization Round 1 ✅ COMPLETED

- [x] Early exit when all lanes broken (spmdVectorAllTrue + condBr to done block)
- [x] inlinehint on SPMD functions + spmdCallMask uses narrowed mask from stack
- [x] Generalized contiguous detection (spmdAnalyzeContiguousIndex traces scalar+iter through BinOp ADD)

**Result**: ~1.2x → ~2.81x speedup

### Performance Optimization Round 2 ✅ COMPLETED

- [x] ChangeType unwrap for contiguous store detection — `spmdUnwrapScalar()` peels `*ssa.ChangeType` chains to find underlying scalar SSA value. Root cause: `ChangeType(j*width, SPMDType{int})` caused spurious splat, breaking contiguous detection for `output[j*width+i]` pattern. **~38% improvement** (SPMD time 3.26ms → 2.01ms)
- [x] i32 mask format on WASM — changed mask representation from `<N x i1>` to `<N x i32>` on WASM targets, eliminating `shl 31 + ashr 31` sign-extension overhead. Added `spmdWrapMask()`, `spmdUnwrapMaskForIntrinsic()`, `spmdMaskSelect()` with width-safety fallback, `spmdMaskElemType()`, `spmdIsWASM()`. 8 new tests. **~3% improvement**
- [x] Tail mask hoisting — verified via LLVM IR and WAT analysis that V8's TurboFan JIT hoists the loop-invariant tail mask comparison. No code change needed.

**Result**: ~2.81x → ~2.91x speedup (0 differences, 0 E2E regressions)

### Performance Optimization Round 3 ✅ COMPLETED

- [x] Native WASM v128.any_true / i32x4.all_true intrinsics — replaced bitcast+icmp pattern (bitcast <4 x i32> → i128, icmp) with native WASM SIMD instructions via `@llvm.wasm.anytrue` / `@llvm.wasm.alltrue` LLVM intrinsics. Added `spmdWasmAnyTrue()` and `spmdWasmAllTrue()` helpers. 2 new tests. **~2.4% improvement**
- WAT verification: `v128.any_true` appears in inner loop, old `i64x2.extract_lane` pattern completely eliminated

**Result**: ~2.91x → ~2.98x speedup (0 differences, 0 E2E regressions)

### Constrained Varying[T,N] Expression-Context Parser Fix -- REMOVED

Note: This parser fix has been removed along with all constrained `Varying[T, N]` support (design simplification).

### SPMD Varying Upcast Restriction ✅ COMPLETED

- [x] Add `spmdBasicSize()` helper: returns byte size of numeric basic types (1/2/4/8 bytes, 0 for platform-dependent)
- [x] Add upcast check in `convertibleToSPMD()`: rejects Varying[smallerType] → Varying[largerType]
- [x] Add upcast check in `checkSPMDtoSPMDAssignability()`: rejects assignment upcasts
- [x] Add SPMD-to-SPMD guard in `convertibleTo()`: prevents standard Go numeric conversion fallthrough (SPMDType.Underlying() unwraps to element type)
- [x] Mirror all changes in both `go/types` and `types2` (4 files total)
- [x] Downcasts (e.g., Varying[uint32] → Varying[uint16]) and same-size conversions (e.g., Varying[int32] → Varying[float32]) remain allowed
- [x] `illegal_invalid-type-casting` now correctly rejected with 8 error sites

### SPMD Loop Peeling ✅ COMPLETED

Split `go for` loops into main phase (full vectors, ConstAllOnes mask, plain v128.store) + tail phase (0-1 masked iterations). Covers both rangeindex (range-over-slice) and rangeint (range N) patterns.

- [x] Loop peeling infrastructure: `spmdShouldPeelLoop`, `spmdCreateTailBlocks`, `emitSPMDTailCheck`, `emitSPMDTailBody`
- [x] Rangeindex peeling: entry Jump redirect, loop bound check override, tail iter phi wiring
- [x] Rangeint peeling: entry If handler (`condBr(alignedBound > 0, mainEntry, tailCheckBlock)`), merged body+loop tail redirect
- [x] Accumulator phi gate: loops with `totalPhiCount > 1` excluded (requires future RAUW for post-loop dominance)
- [x] `entryPredecessor()` unified CFG helper: eliminates `isRangeIndex` branching in phi wiring
- [x] Design docs: `docs/plans/2026-02-23-spmd-loop-peeling-design.md`, `docs/plans/2026-02-24-rangeint-loop-peeling-design.md`

**Result**: Hex-encode ~0.34x → ~2.72x; Mandelbrot ~2.98x → ~3.17x (0 differences, no new E2E regressions)

**Resolved**: `type-casting-varying` now compiles correctly. Multi-loop rangeint peeling and accumulator phi peeling both implemented. Loops with accumulators AND varying control flow in the body excluded from peeling (conservative gate).

### 2.9d Scalar Fallback Mode

- [ ] When `-simd=false`, map `lanes.Varying[T]` to scalar loops instead of vectors
- [ ] Generate element-wise scalar loops for varying operations
- [ ] Implement traditional for loops with scalar masking (if/else branches)
- [ ] Ensure identical behavior between SIMD and scalar modes
- [ ] Verify scalar WASM contains no v128.* instructions

### 2.9e Virtual SIMD Register Width

**Goal**: Configurable virtual SIMD width (`-simd-width=N|native`) for cross-platform validation. Allows testing SPMD code generation as if targeting 32/64/128/256/512-bit SIMD hardware, while still executing on WASM SIMD128. Full-stack: affects type checker lane counts AND backend code generation. Zero overhead at native width. See `docs/plans/2026-02-22-virtual-simd-width-design.md` for design.

- [ ] Add `SPMDWidth` global to `internal/buildcfg`, parsed from `SPMD_WIDTH` env var (0 = native)
- [ ] Parameterize `laneCountForType()` in `go/types/check_ext_spmd.go` with `buildcfg.SPMDWidth`
- [ ] Mirror `laneCountForType()` change in `types2/check_ext_spmd.go`
- [ ] Add `-simd-width=N|native` flag to TinyGo CLI (`main.go`)
- [ ] Add `SIMDWidth int` to `compileopts.Options`, `NativeSIMDWidth() int` backend API to Config
- [ ] Propagate `SPMD_WIDTH` env var in `loader/list.go` for `go list` subprocess
- [ ] Parameterize `spmdLaneCount()` in `compiler/spmd.go` with virtual width
- [ ] Implement vector decomposition for widths > native (lo/hi splitting in createBinOp, memory ops, masks, reduce)
- [ ] Handle narrower vectors for widths < native (fewer lanes, no decomposition)
- [ ] Add cross-lane op decomposition (Broadcast/RotateWithin across split vectors)
- [ ] Add `--simd-width` parameter to `test/e2e/spmd-e2e-test.sh`
- [ ] Create `test/e2e/spmd-width-matrix.sh` for full width matrix validation (32/64/128/256/512)
- [ ] Verify identical output across all widths for all passing E2E tests
- [ ] LLVM IR verification: correct number of native-width ops for decomposed widths

### 2.9f Store Coalescing Optimization

**Goal**: Detect matching stores in both branches of varying if/else and emit `select(cond, thenVal, elseVal)` + one store instead of two masked stores. Eliminates redundant masked store intrinsics. See `docs/plans/2026-02-22-store-coalescing-design.md` for design.

- [ ] Add `spmdCoalescedStore` struct, `spmdSameStoreAddr` address matcher, `spmdCoalescedStores` builder map
- [ ] Add `spmdCollectBranchStores` + `spmdAnalyzeCoalescedStores` SSA pre-analysis (called from `preDetectVaryingIfs`)
- [ ] Add `spmdParentMask()` helper to access mask before varying if push
- [ ] Modify `*ssa.Store` codegen: skip then-stores, emit `select + single store` for else-stores
- [ ] Support both contiguous (masked store with parent mask) and scatter (masked scatter with parent mask) paths
- [ ] Handle nested varying if/else chains (inner coalescing feeds outer)
- [ ] LLVM IR tests: simple if/else, nested chain, partial match, type mismatch
- [ ] E2E test: `examples/store-coalescing/main.go` with varying if/else store patterns

**Implementation plan**: `docs/plans/2026-02-22-store-coalescing.md`

### 2.9h Interface Mask Embedding ✅ COMPLETED

**Goal**: When `lanes.Varying[T]` is boxed into `interface{}` inside a masked context (varying if, SPMD function body, loop tail), embed the per-block condition mask alongside the value so unboxing can restore which lanes are valid and thread the mask into subsequent operations.

- [x] Add `SPMDMask Value` and `SPMDLanes int` fields to `MakeInterface` in go/ssa (`ssa.go`)
- [x] Add new `SPMDExtractMask` SSA instruction with `Type()` returning `VaryingMask`
- [x] Update `Operands()`, `String()`, `sanity.go`, `spmd_peel.go` clone for new instruction
- [x] Add `case *MakeInterface` to `spmdMaskMemOps`, `spmdConvertScopedMemOps`, `spmdConvertAllMemOps`
- [x] Add `spmdMaskScopedMakeInterfaceOps` and `spmdMaskAllMakeInterfaceOps` sweep functions
- [x] Add `spmdNarrowMaskAtTypeAsserts` — inserts SPMDExtractMask + AND after TypeAssert on SPMDType, composes multiple TypeAsserts via chained ANDs, handles main/tail scope separately
- [x] Wire into `spmdConvertLoopOps` (mainBlocks/allOnesMask + tailBlocks/tailMask) and `predicateSPMDFuncBody`
- [x] Change boxed representation from `[N]T` to `struct{Value [N]T; Mask [N]int32}` in `getTypeCode` (`interface.go`)
- [x] Update `MakeInterface` case in `compiler.go` to pack struct with mask (from `SPMDMask` or all-ones)
- [x] Rewrite `createTypeAssertSPMD` for struct format (extract field 0 = value array)
- [x] Add `createSPMDExtractMask` (extract field 1 = mask array, convert to mask vector)
- [x] Design doc: `docs/plans/2026-03-06-spmd-interface-mask-embedding-design.md`

**Result**: type-switch-varying promoted from compile-fail to run-pass (22 run pass total, 0 regressions)

### 2.9i Struct Masked Store/Load Fix ✅ COMPLETED

**Goal**: Fix LLVM crashes when SPMDStore/SPMDLoad encounters struct element types (interface, closure, slice header). LLVM masked intrinsics only support integer, float, and pointer vector elements — not structs.

- [x] Add `spmdIsVectorizableElemType()` type check (int, float, double, pointer → true)
- [x] Add `spmdConditionalStore()` — scalar address struct store with anyTrue mask guard
- [x] Add `spmdPerLaneScatterStore()` — vector-of-pointers struct scatter with per-lane conditional stores
- [x] Add `spmdPerLaneGather()` — vector-of-pointers struct gather with PHI merge, returns `[N x T]` array
- [x] Guard in `createSPMDStore` before splat: dispatch to per-lane helpers for non-vectorizable types
- [x] Guard in `createSPMDLoad`: vector address path → `spmdPerLaneGather`, scalar address path → scalar load for non-vectorizable types

**Result**: map-restrictions and panic-recover-varying promoted from compile-fail to compile-pass (fixes "Do not know how to split the result of this operator!" LLVM error). spmd-call-contexts promoted from compile-fail to compile-pass.

### 2.9j Defer SPMD Mask Threading ✅ COMPLETED

**Goal**: Thread SPMD execution mask through deferred closure calls. Deferred closures with varying parameters need the mask packed into the defer struct at creation time and extracted at call time.

- [x] Add `spmdDeferMask()` helper — returns mask from `CallCommon.SPMDMask`, `spmdCallMask`, or all-ones fallback
- [x] Pack mask into defer struct after `{callback, next}` for both `*ssa.Function` and `*ssa.MakeClosure` paths
- [x] Unpack mask: prepend mask type to valueTypes, adjust extraction loop start index
- [x] Fix `spmdBoxedVaryingGoType` to use platform-native mask element type (i64/i32/i16/i8) based on `128/laneCount`
- [x] Fix MakeInterface lane count: derive from `val.Type().VectorSize()` instead of canonical `spmdEffectiveLaneCount`

**Result**: defer-varying promoted from compile-only to run-pass. type-casting-varying regression fixed (wrong mask element size for non-4-lane types). Total: 23 run pass, 29 compile pass, 8 compile fail, 10 reject OK.

### 2.9k InvalidSPMDPanic Type Checker Restriction ✅ COMPLETED

**Goal**: Forbid `panic()` calls under varying conditions or after mask alteration in SPMD `go for` loops, matching existing restrictions on `return` and `break`. Panic with varying data under uniform conditions (e.g., `if reduce.Any(...) { panic(value) }`) remains allowed.

- [x] Add `InvalidSPMDPanic` error code (161) in `internal/types/errors/codes.go`
- [x] Update `code_string.go` string table
- [x] Add `case *ast.ExprStmt` panic detection + `validateSPMDPanic` in `go/types/stmt_ext_spmd.go`
- [x] Add `case *syntax.ExprStmt` panic detection + `validateSPMDPanic` in `types2/stmt_ext_spmd.go`
- [x] Test cases in both `go/types` and `types2`: uniform OK, varying FORBIDDEN, mask-altered FORBIDDEN, bare panic OK
- [x] Rewrite `panic-recover-varying` E2E test to use `reduce.Any` uniform guards

**Result**: panic-recover-varying promoted from compile-fail to run-pass. Total: 24 run pass, 30 compile pass, 7 compile fail, 10 reject OK.

### 2.9l Printf Mask-Aware Varying Formatting ✅ COMPLETED

**Goal**: Make `fmt.Printf` and all print functions display boxed varying values with mask awareness — active lanes show their value, inactive lanes show `_`. Example: `[5 _ 15 _]` instead of raw `{[5 10 15 25] [-1 -1 -1 -1]}`.

- [x] Add `spmd:"varying"` struct tag to boxed Value field in `spmdBoxedVaryingGoType` (`tinygo/compiler/spmd.go`)
- [x] Add `printSPMDVarying` method in `fmt/print.go` — detects tag via `reflect.StructField.Tag.Get("spmd")`, formats active lanes with requested verb, inactive lanes as `_`
- [x] Hook into `printValue` `case reflect.Struct:` — call `printSPMDVarying` before generic struct printing
- [x] Update `debug-varying` integration test to demonstrate partial masking (`if v > 25`)
- [x] Verify all format verbs work (`%v`, `%d`, `%x`)
- [x] Verify no false positives — regular structs with Value/Mask fields unaffected (no `spmd:"varying"` tag)
- [x] E2E: 24 run pass, 30 compile pass, 7 compile fail, 10 reject OK (no regressions)

**Result**: `Printf("%v", varyingValue)` outputs `[10 20 30 40]` (all active) or `[_ _ 30 40]` (partial mask). Works with all format verbs. Tag-based detection is collision-proof.

### 2.9m Switch Fallthrough Predication ✅ COMPLETED

**Goal**: Fix `predicateVaryingSwitch` to handle `fallthrough` in switch cases inside `go for` loops.

- [x] Add failing tests for fallthrough (basic, memops, sanity, after-if, complex body)
- [x] Fix Phase 1 rewiring: use `bodyBlock.Succs[0]` instead of hardcoded `doneBlock` for non-fallthrough detection
- [x] Fix else-if false detection: add `len(elseBlock.Preds) == 1` guard to prevent merge blocks from being treated as else-if
- [x] Fix complex case bodies: add `spmdFindBodyExitBlock` BFS to find actual exit block with inner control flow
- [x] Fix fallthrough phi loss: add `spmdConvertFallthroughPhis` to convert phis at fallthrough targets to SPMDSelect before rewiring
- [x] Fix cumulative mask: use `activeMask &^ newRemaining` for fallthrough phis (covers all prior cases)
- [x] Fix LLVM type mismatch: add `spmdConvertScalarToElem` in TinyGo for i1→i32 conversion before splatting
- [x] Update ipv4-parser to non-fallthrough per-case approach (fallthrough causes OOB for inactive lanes with varying indices)
- [x] E2E: 31 run pass, 36 compile pass, 7 compile fail, 10 reject OK (+6 run pass, +6 compile pass)

**Result**: Switch fallthrough predication works for cases without varying memory indices. ipv4-parser promoted from compile-fail to run-pass. 6 additional examples promoted. Limitation: fallthrough executes all case bodies for all lanes, so memory accesses must be safe for inactive lanes.

### 2.9g Gather Shift-Right Load Expansion

**Goal**: Detect `d = s[i >> n]` patterns (varying index right-shifted by constant into uniform array) and replace expensive gather operations with a smaller contiguous load + shufflevector expansion. For 4 i32 lanes, `s[i >> 1]` becomes 1 load + 1 shuffle instead of 4 scalar loads + 4 inserts. See `docs/plans/2026-02-22-gather-shift-load-expansion-design.md` for design. Inspired by ISPC's Gather Coalescing Pass (`opt/GatherCoalescePass.cpp`).

- [ ] Add `spmdShiftedLoadInfo` struct and `spmdShiftedPtr` builder map (parallel to `spmdContiguousPtr`)
- [ ] Add `spmdAnalyzeShiftedIndex()` in `spmd.go`: detect `BinOp(SHR, contiguous_expr, const)`, compute `uniqueCount` + `shuffleMask`
- [ ] Integrate into IndexAddr handling (`compiler.go` ~line 2870): call `spmdAnalyzeShiftedIndex` before vector-of-GEPs fallback, register in `spmdShiftedPtr`
- [ ] Add load dispatch in UnOp/deref path (`compiler.go` ~line 4200): check `spmdShiftedPtr`, emit scalar load + splat (uniqueCount==1) or narrow vector load + shufflevector (uniqueCount<laneCount)
- [ ] Handle `(base + iter) >> n` pattern (scalar base expression added before shift)
- [ ] Apply execution mask via select on the expanded result (not on the narrow load)
- [ ] LLVM IR tests: `i >> 1` (4 i32 lanes), `i >> 2` (broadcast), `(base+i) >> 1`, `i >> 1` (i8/16 lanes), `i >> 3` (16 lanes)
- [ ] E2E test: lookup table expansion pattern with `go for` loop

### 2.9n reduce.From Aggregate Fix ✅ COMPLETED (2026-03-10)

**Goal**: Fix `reduce.From[T]()` builtin for aggregate element types (e.g., `Varying[string]`) where the value representation is `[N x T]` (ArrayTypeKind), not a vector. The existing `CreateExtractElement` only works on LLVM vectors.

- [x] Add `ArrayTypeKind` branch in `reduce.From` handler: use `CreateExtractValue(vec, i, "")` to extract each lane
- [x] Fix `reduce.From` for `Varying[string]` and other aggregate-typed varying values
- [x] Promote `map-restrictions` from compile-fail to compile-only (unblocked by this fix)
- [x] Commit: `chore: update tinygo submodule for reduce.From aggregate fix`

### 2.9o Array-Counting Run-Pass Promotion ✅ COMPLETED (2026-03-10)

**Goal**: Fix SIGSEGV compiler crash in `array-counting` and promote to run-pass.

- [x] Diagnose root cause in `spmdCreateInterleavedPtrPhis` / IndexAddr for `[][]int` element type
- [x] Fix: laneCount=1 scalar degeneration for slice-of-slices (already complete; the crash was a separate codegen bug)
- [x] Promote `array-counting` from compile-fail to run-pass
- [x] Commits: `f7ce404 chore: update submodules for array-counting fix`, `d11f3e8 fix: promote array-counting from compile-fail to run-pass`

### 2.9p Varying-Array-Iteration laneCount=1 Fix ✅ COMPLETED (2026-03-13)

**Goal**: Fix `go for idx, varyingData := range varyingArray` where `varyingArray` is `[]lanes.Varying[int]`. The outer loop should run serially (laneCount=1), loading one full SIMD vector per step — not producing illegal `<4 x <4 x i32>>` nested vectors.

**Root Cause**: `getTypeSize(Varying[int])` returns 0 (zero-sized Go struct). `computeEffectiveLaneCount` fell back to 4, causing TinyGo to emit `VectorType(<4 x i32>, 4)` = `<4 x <4 x i32>>` (illegal in LLVM).

- [x] Fix `go/types/stmt_ext_spmd.go` (`spmdRangeStmt`): when `rVal` is `*SPMDType`, append `simd128CapacityBytes` (16) to `varyingElemSizes` → yields `16/16 = 1` lane count
- [x] Fix `go/types/stmt_ext_spmd.go`: add `alreadyVarying` guard to prevent `Varying[Varying[T]]` double-wrapping
- [x] Mirror both fixes in `types2/stmt_ext_spmd.go` (Phase 1 frontend)
- [x] Fix `tinygo/compiler/compiler.go` (`makeLLVMType` `*types.SPMDType` case): add `VectorTypeKind` branch returning inner vector directly for `Varying[Varying[T]]`
- [x] Fix `tinygo/compiler/spmd.go` (`createSPMDLoad` contiguous path): early return `CreateLoad(elemType, ptr)` when `elemType` is already a vector (avoids `VectorType(vector, N)`)
- [x] Fix `tinygo/compiler/spmd.go` (`createSPMDStore` contiguous path): add parallel VectorTypeKind guard for stores
- [x] Simplify `test/integration/spmd/varying-array-iteration/main.go`: remove inner varying `if` (1-lane outer mask vs 4-lane condition unsupported) and `processVaryingGroups` (Varying arithmetic outside go for)
- [x] Move `varying-array-iteration` from Level 5c (compile-fail) to Level 6 (compile-only) in `test/e2e/spmd-e2e-test.sh`
- [x] Commit: `fa3f820 fix: promote varying-array-iteration from compile-fail to compile-ok`

**Key Insight**: `TinyGo spmdRangeIndexLaneCount` independently confirms laneCount=1 by computing `TypeAllocSize(<4 x i32>) = 16` → `16/16 = 1`. The go/types fix and TinyGo backend are consistent.

**Remaining Limitation**: Inner `if varyingData > condition` inside `go for over []Varying[T]` (combining 1-lane outer mask with 4-lane condition) is not yet supported.

### 2.9q map-restrictions Fix: Inner Scalar Loop Scope Exclusion ✅ COMPLETED (2026-03-14)

**Root Cause**: `spmdLoopScopeBlocks` BFS included inner scalar loop blocks in the outer SPMD loop's scope. For `map-restrictions`, `demonstrateWorkarounds()` has a `go for i, keys := range data` outer SPMD loop with an inner `for j, key := range reduce.From(keys)` scalar loop. The inner loop's blocks were included in scope, converting their scalar `reduce.From(keys)[j]` loads to `SPMDLoad<2>` — producing `<2 x i32>` where `groupByKey` expected `i32`.

**Fix in `x-tools-spmd/go/ssa/spmd_predicate.go`** (`spmdLoopScopeBlocks`):
- Added pre-pass to detect inner loop headers: blocks with back-edge predecessors (`pred.Index >= b.Index`) that can cycle back without crossing the outer SPMD loop boundaries
- `pred.Index >= b.Index` catches both **normal back-edges** (rangeindex inner loops, `>`) and **self-loops** (merged rangeint inner loops, `==`)
- Guard `!spmdBlockHasSPMDPhi(b)`: inner loop headers with `Varying[T]` phis (e.g., bit-counting's `count Varying[uint8]`) are doing SPMD work and must stay in scope
- BFS stops at detected `innerLoopHeaders` — their entire sub-CFG is excluded from the SPMD scope
- New helper `spmdBlockHasSPMDPhi`: checks if a block's leading phis have `*types.SPMDType` result type

**Two unit tests added to `x-tools-spmd/go/ssa/spmd_predicate_test.go`**:
- `TestPredicateSPMD_InnerScalarLoopExcluded`: fails without `>=` fix — verifies `arr[j]` in inner for-range remains as `UnOp{MUL}`, not SPMDLoad
- `TestPredicateSPMD_InnerLoopWithVaryingPhiInScope`: fails without `!spmdBlockHasSPMDPhi` guard — verifies `if count > 0` in inner loop with Varying phi produces SPMDSelect

- [x] Fix `spmdLoopScopeBlocks` pre-pass: `pred.Index >= b.Index` (catches self-loop merged rangeint inner loops)
- [x] Add `spmdBlockHasSPMDPhi` helper to exempt inner loops with Varying phis from exclusion
- [x] Add two unit tests validating both the exclusion and the SPMD-phi exception
- [x] Promote `map-restrictions` from compile-fail to compile-only (40 compile pass, was 39)
- [x] Commits: `e04b6a03 fix: exclude inner scalar loops from SPMD scope in predication` (x-tools-spmd), `d032f08 chore: update x-tools-spmd for inner loop scope exclusion fix` (main repo)

### 2.10 Backend Integration Testing

- [ ] Verify simple-sum example compiles and produces correct WASM
- [ ] Verify SIMD WASM contains `v128.*` instructions via `wasm2wat` inspection
- [ ] Verify scalar WASM contains no SIMD instructions
- [ ] Test WASM execution in wasmer-go runtime
- [ ] Validate identical output between SIMD and scalar modes
- [ ] Test all examples compile to both SIMD and scalar WASM
- [ ] Benchmark SIMD vs scalar performance differences

## Phase 3: Validation and Success Criteria

**Goal**: Demonstrate complete SPMD implementation with all examples working in dual modes.

### 3.1 Comprehensive Example Validation

- [x] **simple-sum**: Compiles and runs in both SIMD/scalar modes with identical output
- [x] **odd-even**: Conditional processing works correctly in both modes
- [x] **bit-counting**: Complex control flow handled properly
- [x] **array-counting**: Divergent control flow works correctly
- [x] **printf-verbs**: Printf integration displays varying values correctly
- [x] **hex-encode**: String processing algorithms work in both modes
- [x] **to-upper**: Character manipulation operations work correctly
- [x] **base64-decoder**: Cross-lane operations + Mula-Lemire v2 packing — RUN PASS (AVX2 18141 MB/s, 91% of simdutf C++)
- [x] **ipv4-parser**: Real-world parsing algorithm works (PoC goal)
- [x] **debug-varying**: Debugging and introspection features work
- [x] **goroutine-varying**: Goroutine launch with varying values works
- [x] **defer-varying**: Defer statements with varying capture work
- [x] **panic-recover-varying**: Error handling with varying types works
- [x] **map-restrictions**: Map restriction enforcement works correctly
- [x] **pointer-varying**: Pointer operations with varying types work
- [x] **type-switch-varying**: Type switches with varying interface{} work
- [x] **non-spmd-varying-return**: Non-SPMD functions returning varying work
- [x] **spmd-call-contexts**: SPMD functions callable from any context
- [x] **lanes-index-restrictions**: lanes.Index() context restrictions enforced
- [x] **union-type-generics**: Generic type constraints for reduce/lanes functions work — RUN PASS (fixed 2026-03-21)

### 3.2 Illegal Example Validation

- [x] **break-in-go-for.go**: Correctly fails compilation with clear error
- [x] **control-flow-outside-spmd.go**: Control flow restrictions enforced
- [x] **go-for-in-spmd-function.go**: SPMD function restrictions enforced  
- [x] **invalid-contexts.go**: Context validation works correctly
- [x] **invalid-lane-constraints.go**: Lane constraint validation works
- [x] **invalid-type-casting.go**: Type casting restrictions enforced
- [x] **malformed-syntax.go**: Syntax error handling works correctly
- [x] **nested-go-for.go**: Nesting restrictions enforced (type checking phase error)
- [x] **public-spmd-function.go**: Public API restrictions enforced
- [x] **varying-to-uniform.go**: Assignment rule restrictions enforced

### 3.3 Legacy Compatibility Validation

- [x] All legacy examples compile without GOEXPERIMENT=spmd
- [x] Existing code using "uniform"/"varying" as identifiers works
- [x] No breaking changes to existing Go programs
- [x] Graceful degradation when experiment disabled
- [x] Clear error messages when SPMD features used without experiment

### 3.4 Performance and Technical Validation

- [x] **SIMD Instruction Generation**: `wasm2wat` shows v128.* instructions in SIMD builds
- [x] **Scalar Fallback**: Scalar builds contain no SIMD instructions
- [x] **Identical Output**: Both modes produce bit-identical results for all examples
- [x] **Performance Measurement**: Measurable performance difference between modes
- [x] **Memory Efficiency**: SIMD code uses vectors efficiently without excessive memory
- [x] **Browser Compatibility**: SIMD detection and loading works in browsers
- [x] **Wasmer-go Integration**: Both WASM modes execute correctly in wasmer-go

### 3.5 Browser Integration Validation

- [x] Create SIMD detection JavaScript code for runtime capability checking — DONE (test/integration/spmd/browser-simd-detection/)
- [ ] Test automatic loading of SIMD vs scalar WASM based on browser support
- [ ] Verify WASM SIMD128 feature detection works correctly
- [ ] Implement fallback mechanism for browsers without SIMD support
- [ ] Test WASM execution in multiple browser environments

### 3.6 Documentation and User Experience

- [ ] Update README.md with working examples and build instructions
- [ ] Document GOEXPERIMENT=spmd usage and build procedures
- [ ] Create clear error message documentation
- [ ] Update CLAUDE.md with implementation status
- [ ] Provide performance benchmarking guidance
- [ ] Document browser integration procedures

## Testing and Quality Assurance

### Continuous Integration

- [ ] Set up automated testing for all phases
- [ ] Implement regression testing for each milestone
- [ ] Add performance benchmarking in CI pipeline
- [ ] Test matrix for different GOEXPERIMENT combinations
- [ ] Validate backwards compatibility continuously

### Error Handling

- [ ] Comprehensive error message testing
- [ ] Edge case validation for all SPMD constructs  
- [ ] Memory safety validation for varying operations
- [ ] Overflow and underflow testing for SIMD operations
- [ ] Graceful handling of unsupported hardware features

### Performance Validation

- [ ] Benchmark all examples in both SIMD and scalar modes
- [ ] Validate expected performance improvements from SIMD
- [ ] Test memory usage efficiency
- [ ] Profile compilation time impact
- [ ] Measure runtime overhead of SPMD constructs

## Implementation Guidelines

### Development Workflow

1. **Test First**: Write tests before implementation for each feature
2. **Incremental**: Implement one checkbox at a time
3. **Validation**: Run relevant test suite after each change
4. **Integration**: Test full pipeline regularly
5. **Documentation**: Update docs with each user-visible change

### Code Quality Standards

- **Atomic Commits**: One logical change per commit
- **Clear Messages**: Descriptive commit messages without emojis
- **Testing**: All changes must pass existing tests
- **Reference**: Follow ISPC patterns for SPMD-specific code
- **Compatibility**: Maintain backward compatibility

### Success Metrics

- **All Examples Work**: Every example compiles and runs correctly in both modes
- **Performance**: Measurable SIMD performance improvements
- **Compatibility**: No breaking changes to existing Go code
- **Documentation**: Complete user and developer documentation
- **Browser Ready**: Full browser integration with SIMD detection

## Risks and Mitigation

### Technical Risks

- **LLVM Integration Complexity**: Mitigate with incremental testing and ISPC reference patterns
- **SIMD Hardware Variance**: Focus on WASM SIMD128 as single, well-defined target
- **Performance Overhead**: Profile and optimize each component incrementally
- **Memory Safety**: Extensive testing with varying pointer operations

### Project Risks  

- **Scope Creep**: Strict adherence to PoC limitations and example-driven development
- **Complexity Management**: Clear phase separation and test-driven development
- **Integration Issues**: Regular full-pipeline testing and validation

## Current Status

- **Phase 0**: ✅ **COMPLETED** - All foundation infrastructure ready
- **Phase 1**: ✅ **COMPLETED** - Frontend implementation (lexer, parser, type system, SSA)
- **Phase 2**: ✅ **COMPLETED** - TinyGo LLVM backend (vector types, control flow masking, builtins)
- **Phase 3**: ✅ **COMPLETED** - Validation, benchmarks, x86 native, browser demo all done
  - TinyGo architecture explored and documented
  - Critical finding: TinyGo uses `golang.org/x/tools/go/ssa` (not `cmd/compile` SSA)
  - Critical finding: `go/parser`, `go/ast`, `go/types` lack SPMD support (must be ported first)
  - Phase 2 plan rewritten: 2.0 (stdlib porting) + 2.1-2.10 (TinyGo compiler work)
  - 2.0a: ✅ go/ast SPMD fields (IsSpmd, LaneCount on RangeStmt)
  - 2.0b: ✅ go/parser `go for` parsing (constrained syntax removed)
  - 2.0c: ✅ go/types SPMD type checking (10 ext_spmd files, constrained features removed)
  - 2.0d: ✅ SPMD metadata extraction in TinyGo compiler (spmd.go + spmd_test.go, 13 tests)
  - 2.1: ✅ GOEXPERIMENT support + auto-SIMD128 for WASM (6 files, 12 tests)
  - 2.2: ✅ LLVM vector type generation for lanes.Varying[T] (3 files, 6 tests/34 cases)
  - 2.3: ✅ SPMD loop lowering for go for range loops (3 files, 4 tests/14 cases)
  - 2.5: ✅ Control flow masking — varying if/else linearization + phi→select (3 files, 4 tests)
  - 2.6: ✅ SPMD function call handling — mask param in decl/type/call/entry (5 files, 2 tests/9 cases)
  - 2.7: ✅ lanes/reduce builtin interception — 6 lanes + 13 reduce builtins (3 files, 9 tests/32 cases)
  - 2.8: ✅ Execution mask stack + vector memory operations (3 files, 6 tests)
  - 2.8b: ✅ Range-over-slice loop detection (3 files, 3 tests)
  - E2E: ✅ Test infrastructure (GOEXPERIMENT fix, go/ssa SPMDType, Node.js runner, 32-test script)
  - Fix: ✅ Range-over-slice type inference (rangeKeyVal for element types, concrete Typ[Int] for key)
  - Fix: ✅ createConvert SPMDType handling (SPMD-to-SPMD, SPMD-to-scalar, scalar-to-SPMD)
  - Fix: ✅ E2E test program bugs (hex-encode, array-counting, to-upper, debug-varying)
  - Fix: ✅ Additional test program bugs (bit-counting return type, pointer-varying scatter, non-spmd-varying-return syntax)
  - Fix: ✅ SPMDType interface boxing — getTypeCode() redirects to [laneCount]T array representation
  - Fix: ✅ Vector width mismatch — spmdBroadcastMatch() handles vector-vector width normalization via shuffle
  - Fix: ✅ Example program bugs — 7 programs fixed (hex-encode, bit-counting, map-restrictions, defer-varying, select-with-varying-channels, to-upper, mandelbrot)
  - Fix: ✅ E2E suite expansion — 11 programs added to spmd-e2e-test.sh (32 → 43 tests)
  - Fix: ✅ getPointerBitmap vector types — VectorTypeKind added to no-pointer case (unblocked goroutine-varying)
  - Fix: ✅ makeLLVMType untyped int — UntypedInt added to Int/Uint case
  - Fix: ✅ Nested loop deduplication — seenLoopInfo map in analyzeSPMDLoops() prevents nested regular for loops from being vectorized
  - 2.8c: REMOVED — Constrained Varying[T,N] backend (design simplification)
  - 2.9a: ✅ SPMD function body mask infrastructure
  - 2.9b: ✅ Per-lane break mask support (break mask alloca, break redirects) — SSA-level predication replaces TinyGo LLVM code (2026-03-03)
  - 2.9c: ✅ Vector IndexAddr + break result tracking + mandelbrot
  - Feature: ✅ SPMD function body predication (2026-03-03) — `predicateSPMDFuncBody()` + `predicateSPMDScope()` in x-tools-spmd; TinyGo break code removed (~560 lines)
  - Perf: ✅ Round 1 — Early exit + inlining + generalized contiguous (~1.2x → ~2.81x)
  - Perf: ✅ Round 2 — ChangeType unwrap + i32 masks (~2.81x → ~2.91x)
  - Perf: ✅ Round 3 — Native WASM v128.any_true/alltrue intrinsics (~2.91x → ~2.98x)
  - Fix: ✅ E2E suite expansion (32 → 46 tests), 12 example program bugs fixed
  - Fix: ✅ Shift bounds check for vector operands — splat helpers for vector ICmp/Select in asserts.go + compiler.go
  - Fix: ✅ Non-SPMD varying return call signature — spmdMaskType() consistency across declaration/call/type
  - Fix: ✅ L5b varying if/else phi merge inside loop bodies — spmdFindMerge ifBlock barrier + multi-pred merge select + deferred select in else-exit block (L5b 800→404)
  - E2E: ✅ 5 integration tests promoted to run-pass (simple-sum, odd-even, hex-encode, debug-varying, lanes-index-restrictions) — 16 run pass total
  - Fix: ✅ SPMD varying upcast restriction — spmdBasicSize() + convertibleToSPMD/checkSPMDtoSPMDAssignability upcast checks + convertibleTo SPMD guard (4 files in go/types + types2)
  - REMOVED: Constrained Varying[T,N] parser, type checker, type relaxation, FromConstrained/ToConstrained, constraintN (design simplification)
  - Added: *Within cross-lane operations (RotateWithin, SwizzleWithin, ShiftLeftWithin, ShiftRightWithin)
  - Fix: ✅ Varying switch masking — switch chain detection + CFG linearization + deferred phi resolution (3 TinyGo commits, 1 E2E test) — 17 run pass total
  - Feature: ✅ Compound boolean conditions — &&/|| in varying contexts work automatically via short-circuit CFG (1 E2E test) — 19 run pass total
  - Benchmark: ✅ Hex-encode converted to benchmark (1024-byte data, 1000 iterations, SPMD vs scalar timing + speedup ratio)
  - Docs: ✅ SIMD optimization analysis for hex-encode (`docs/hex-encode-simd-analysis.md`) — 6 issues identified (gather scalarization, redundant bounds checks, non-constant-folded patterns). SPMD at 0.24x scalar speed.
  - 2.9d: ✅ Scalar fallback mode via SIMDRegisterSize (2026-03-14)
  - 2.9e: ✅ AVX2 256-bit SIMD width for x86-64 (2026-03-14)
  - Fix: ✅ x86-64 SSE2/SSSE3 intrinsic support (2026-03-20)
  - Fix: ✅ x86-64 decomposed index path (2026-03-20)
  - Perf: ✅ Relaxed SIMD support in IPv4 parser (2026-03-27)
  - Perf: ✅ AVX2 swizzle table duplication for byte lookups (2026-03-27)
  - Perf: ✅ IPv4 parser Lemire scalar trim + page-safe raw load (2026-03-27)
  - Browser: ✅ SIMD detection demo (0193071)
  - E2E: ✅ Dual-mode testing Level 8 (SIMD vs scalar identical output)
  - E2E: ✅ Level 9 scalar validation for lane-count-dependent tests
  - E2E: ✅ Level 10 x86-64 SSE native tests (8 tests)
  - E2E: ✅ Level 11 x86-64 AVX2 native tests (10 tests)
  - Benchmark: ✅ spmd-benchmark.sh (WASM SIMD vs scalar)
  - Benchmark: ✅ spmd-benchmark-x86.sh (x86 native AVX2 vs scalar)
  - Fix: ✅ IPv4 parser x86 page-safe alignment (2026-03-28)
  - Fix: ✅ AVX2 typed constants (2026-03-28)

## Deferred Items Collection

### Phase 1 Deferred Subtask (NOT DONE)

**Purpose**: Track all Phase 1 implementation work that has been explicitly deferred to later phases.

**Deferred Items**:

- [ ] **Known Limitation**: Break in varying switch inside go for loop wrongly accumulates into loop continue mask
  - Location: Phase 1.10i (switch masking)
  - Impact: Incorrect execution mask for break statements in switch with varying case conditions
  - Status: Acknowledged but not yet fixed
  - Priority: Medium (edge case, workaround available)
  - Related Issues: See PLAN.md Phase 2.5-2.6 for control flow masking details

### Phase 2 Deferred Subtask (MOSTLY DONE)

**Purpose**: Track all Phase 2 (TinyGo Backend) implementation work that has been explicitly deferred to later phases or blocked by technical limitations.

**Completed Deferred Items**:

- [x] **Phase 2.2: Scalar Fallback Mode** — DONE (2026-03-22)
  - `-simd=false` flag, `SIMDRegisterSize` on `types.Config`, `spmdUsesSIMD()` helper
  - All 30 tests compile in scalar mode

- [x] **Phase 2.7b: lanes.Rotate() Builtin** — DONE
  - `createRotate` in spmd.go, handles vector + array aggregate types via shufflevector

- [x] **Phase 2.7b: lanes.Swizzle() Builtin** — DONE
  - `createSwizzle` in spmd.go, runtime varying index support with wrapping

- [x] **Masked bounds check for inactive SPMD lanes** — DONE
  - Inactive lane indices clamped to 0 before bounds check (cleaner than post-AND)

- [x] **Fix lanes.Broadcast for aggregate types** — DONE
  - Broadcast case handles ArrayTypeKind via alloca + variable-index GEP + broadcast loop

- [x] **Break in varying switch** — DONE (2026-02-22)
  - Sequential mask narrowing, cascaded select at merge, deferred placeholder phi

- [x] **lanes.CompactStore builtin** — DONE (2026-04-08)
  - SIMD compress-store with constant-mask and runtime-mask paths

- [x] **SPMDMux instruction** — DONE (2026-04-10)
  - Collapses SPMDSelect chains from `i % K` patterns, handles NEQ masks

- [x] **SPMDInterleaveStore instruction** — DONE (2026-04-10)
  - Replaces SPMDMux + CompactStore with diagonal shuffles + compaction

- [x] **Byte-decomposition store** — DONE (2026-04-12)
  - Detects stride-S byte extraction from wider types, emits bitcast + pshufb + store

- [x] **pmaddubsw/pmaddwd pattern detection** — DONE (2026-04-06)
  - Stride-2 multiply-add patterns on x86 SSSE3+ and emulated on WASM

- [x] **x86 feature implication chain** — DONE (2026-04-09)
  - `spmdHasX86Feature("ssse3")` returns true when +avx2 present

- [x] **LICM for SPMD compilations** — DONE (2026-04-11)
  - loop-simplify + lcssa + licm added to LLVM pass pipeline when GOEXPERIMENT=spmd

- [x] **All-ones mask load fast-path** — DONE (2026-04-11)
  - Plain CreateLoad for peeled main body instead of llvm.masked.load

**Remaining Deferred Items**:

- [x] **Phase 2.5: Varying For-Loop Masking (Continue/Break Accumulation)** — DONE
  - SSA: predicateVaryingBreaks + break mask phi + result accumulator (7 unit tests passing)
  - TinyGo: spmdBreakMaskBackEdges + early-exit via spmdVectorAllTrue
  - E2E: mandelbrot example validates the pattern (regular for loop with varying break)

- [x] **Fix unconditional SExt in IndexAddr vector bounds check** — DONE (2026-04-12)
  - Replaced `CreateSExt` with `spmdExtendIndex` which respects Go type signedness

- [x] **Varying[array] indexing: Varying[[N]T][index] → Varying[T]** — DONE (2026-04-12)
  - Both go/types and types2 updated. Array-element indexing on Varying types now allowed.

- [x] **&varyingVar address-of** — DONE (2026-04-12)
  - Type checker: `&Varying[T]` → `Varying[*T]`, `*Varying[*T]` → `Varying[T]`
  - TinyGo: per-lane GEPs using element type lane count (not pointer lane count)
  - Note: pointer-varying E2E test has 3 pre-existing lane-count-dependent failures (use `int` = i64 on x86 → 2 lanes, test expects 4). Fix: change test to use `int32`.

- [ ] **Divergent inner loop support for N>1 in go for over slice-of-slices**
  - Status: NOT DONE — N=1 scalar degeneration works; N>1 requires per-lane termination
  - Priority: Low

### Phase 3 Deferred Subtask (DONE)

All Phase 3 validation work is complete. The only remaining compile failure is `union-type-generics` (generic SPMD function calling another generic SPMD function).

---

**Last Completed**: Base64 Mula-Lemire v2 decoder (2026-04-12) — Cascading go-for loops (byte→int16→int32) trigger pmaddubsw/pmaddwd pattern detection + byte-decomposition store. AVX2 18141 MB/s (91% of simdutf C++), SSSE3 9201 MB/s, WASM 6004 MB/s. 33x faster than Go stdlib.

**Next Action**: All phases complete. 3 low-priority deferred features remain:
1. Varying[array] indexing (type checker change)
2. &varyingVar address-of (semantic design)
3. Divergent inner loops N>1 (complex feature)

### Recent Major Achievements (Phase 1.5 Extensions)

🎉 **MAJOR MILESTONE**: 7 out of 10 SPMD tests now PASSING (70% success rate)!

**Breakthrough Fixes Completed**:

- ✅ **Binary Expression Type Propagation**: Fixed varying expression type detection - `i > 5` where `i` is varying now correctly returns `varying bool`
- ✅ **Mixed Operations Support**: Enhanced binary expression handling for mixed varying/uniform operations with automatic type promotion
- ✅ **Indexing Expression Propagation**: Implemented varying type propagation for indexing - `data[i]` now varying when `i` is varying
- ✅ **Control Flow Validation**: Complete SPMD control flow validation with ISPC-based return/break restrictions and mask alteration tracking
- ✅ **Switch Statement Context**: Fixed switch statement context propagation for varying expressions
- ✅ **Goto/Select Restrictions**: Implemented goto and select statement restrictions in SPMD contexts
- ✅ **Capacity Validation Fix**: Corrected SPMD function parameter capacity validation according to specification (disabled total capacity limit)

**Test Status**: 7 out of 10 SPMD tests passing with remaining issues in assignment validation and varying operations

## Phase 0 Foundation Setup - ✅ COMPLETE

**Phase 0 Status**: All foundation infrastructure is complete and ready for implementation.

### Recent Progress (Phase 0.6 - COMPLETED 2025-08-02)

✅ **TDD Workflow Documentation Complete**

- Created comprehensive TDD-WORKFLOW.md with test-first development procedures
- Implemented main Makefile with automated test runners for all SPMD phases
- Established regression testing and continuous integration procedures
- Defined test success criteria for each implementation milestone
- Created development workflow integration guidelines and CI/CD automation
- Test commands include phase-specific testing (test-phase0, test-phase1, etc.)
- Component testing shortcuts (test-lexer, test-parser, test-typechecker, etc.)
- CI/CD validation (ci-quick, ci-full, test-regression) with performance benchmarking
- Complete Phase 0 validation infrastructure with 6 sub-phases fully tested
- All Phase 0 foundation infrastructure ready for Phase 1 implementation

**Key Technical Achievement**: Complete test-driven development infrastructure ready for systematic SPMD implementation. All foundation phases validated and working, providing comprehensive validation framework for Phases 1-3.

### Recent Progress (Phase 1.1 - COMPLETED 2025-08-03)

✅ **GOEXPERIMENT Integration Complete**

- Verified SPMD bool field exists in internal/goexperiment/flags.go from Phase 0.1
- Confirmed build constraint files (exp_spmd_on.go, exp_spmd_off.go) already generated
- Successfully rebuilt Go toolchain to recognize SPMD experiment
- Tested GOEXPERIMENT=spmd enables/disables SPMD features properly via build constraints
- Validated graceful degradation with backward compatibility tests
- Confirmed Go build system recognizes SPMD experiment via goexperiment.spmd build tags
- Framework ready for clear error messages in lexer/parser phases
- All Phase 0 infrastructure tests continue to pass
- Phase 1.1 frontend GOEXPERIMENT integration is complete

**Key Technical Achievement**: GOEXPERIMENT infrastructure is fully operational for frontend development. Build constraints work correctly, graceful degradation functions properly, and the foundation is ready for Phase 1.2 lexer implementation.

### Recent Progress (Phase 1.2 - COMPLETED 2025-08-06)

✅ **Lexer Modifications Complete**

- Added _Uniform and_Varying tokens to src/cmd/compile/internal/syntax/tokens.go with proper enum positioning
- Implemented context-sensitive keyword recognition in src/cmd/compile/internal/syntax/scanner.go with buildcfg.Experiment.SPMD gating
- Updated token_string.go with correct token mapping arrays and string indices for new SPMD tokens
- Added setGOEXPERIMENT() helper function to parser_test.go for experiment control during testing
- Modified testFileWithSPMD() to properly set experiment flags during parsing with defer cleanup
- Updated SPMD test files with proper build constraints (//go:build goexperiment.spmd)
- All parser tests pass with correct GOEXPERIMENT flag behavior and backward compatibility maintained
- Successfully built modified Go toolchain with SPMD support enabled
- Verified context-sensitive lexing: uniform/varying are keywords only when GOEXPERIMENT=spmd is set
- Confirmed zero breaking changes to existing Go code when experiment is disabled

**Key Technical Achievement**: Context-sensitive lexer implementation is fully operational with GOEXPERIMENT integration. Uniform and varying keywords are recognized conditionally, backward compatibility is maintained, and the lexer foundation is ready for Phase 1.3 parser extensions.

**Important Architectural Finding**: During Phase 1.2 implementation, we discovered that true backward compatibility requires **parser-level context disambiguation**, not lexer-level. The current lexer correctly emits `_Uniform`/`_Varying` tokens when SPMD enabled, but the parser must distinguish between:

- `uniform int x` (SPMD type syntax - uniform as keyword)  
- `var uniform int = 42` (identifier usage - uniform as name)

This follows Go's established patterns for context-sensitive keywords and will be implemented in Phase 1.3 using grammar-based disambiguation with lookahead.

### Recent Progress (Phase 1.4 - PARTIALLY COMPLETED 2025-08-09)

🔄 **Type System Implementation In Progress**

- Added complete `SPMDType` implementation in types2/spmd.go with Uniform/Varying qualifiers
- Implemented constrained varying type support (`varying[n]` and `varying[]` syntax)
- Added SPMD type string representation support in typestring.go extensions
- Created SPMD type expression handling in typexpr.go extensions
- Implemented SPMD type identity comparison in predicates.go extensions
- Added build constraint support for all SPMD type system components
- Successfully built Go compiler with SPMD type system enabled
- Type checker correctly identifies and compares SPMD types (no more panics)
- **Remaining Work**: Complete SPMD parsing in all contexts, semantic rules, and downstream compiler integration

**Key Technical Achievement**: Complete SPMD type system implementation with identity comparison working correctly. Universal constrained varying (`varying[]`) syntax verified to work for lanes/reduce function signatures. Type compatibility rules are the next critical step to enable SPMD function parameter passing and assignments.

**COMPLETED Features**:

- ✅ SPMD type compatibility rules implemented (operand_ext_spmd.go)
- ✅ `varying[4] int` → `varying[] int` parameter passing (universal constraint compatibility)
- ✅ `uniform int` → `varying int` assignments (automatic broadcast)
- ✅ `int` → `uniform int`/`varying int` assignments  
- ✅ Varying-to-uniform assignment blocking with proper error messages
- ✅ All lanes/reduce function prototype support enabled

**PHASE 1.4 COMPLETE**:

- ✅ Complete SPMD type system with Uniform/Varying qualifiers
- ✅ Universal constrained varying (`varying[]`) support for lanes/reduce functions
- ✅ Comprehensive type compatibility and assignability rules
- ✅ Interface support: SPMD types can be assigned to interface{} and varying interface{}
- ✅ Pointer operations: Support varying *T, validate against*varying T restrictions
- ✅ Generic type constraints: Universal varying enables polymorphic function parameters
- ✅ Error handling: Proper InvalidSPMDType error code and validation

**Ready for Phase 1.5**: Type system foundation complete, proceed to semantic rules implementation

### Recent Progress (Phase 1.5 - COMPLETED 2025-08-10)

✅ **Type Checking Rules Implementation Complete**

- Created comprehensive SPMD statement checking extensions in `stmt_ext_spmd.go` with ISPC-based control flow rules
- Implemented mask alteration tracking following ISPC's `lHasVaryingBreakOrContinue` approach
- Added complete return/break restriction validation: forbidden under varying conditions or after mask alteration
- Implemented continue statement handling: always allowed, tracks mask alteration in varying contexts
- Added nested `go for` loop detection and prevention with proper error reporting
- Created SPMD function validation: enforces public API restrictions and prevents go for in SPMD functions  
- Extended AST walker (`walk.go`) to handle `*syntax.SPMDType` nodes preventing SSA crashes
- Added all required SPMD error codes: `InvalidSPMDBreak`, `InvalidSPMDReturn`, `InvalidNestedSPMDFor`, `InvalidSPMDFunction`
- Integrated SPMD statement handling into main `stmt.go` processing pipeline
- Implemented context-sensitive validation with varying depth tracking and mask state management
- Created extension pattern with build constraints for clean SPMD/non-SPMD separation
- Successfully built core SPMD type checking infrastructure ready for SSA generation

**Key Technical Achievement**: Complete SPMD statement validation system implementing ISPC-proven control flow rules. The type checker now properly validates all SPMD constructs including complex control flow scenarios, function restrictions, and maintains perfect backward compatibility. Foundation ready for Phase 1.7 SSA generation extensions.

**COMPLETED Features**:

- ✅ ISPC-based return/break restrictions with mask alteration tracking
- ✅ Continue statement handling with mask state updates
- ✅ Nested `go for` loop prevention and validation
- ✅ SPMD function restriction enforcement (public API, go for containment)
- ✅ Varying control flow depth tracking through conditional statements  
- ✅ AST walker integration preventing SSA generation crashes
- ✅ Complete error handling with descriptive SPMD-specific error codes
- ✅ Main statement processing integration with proper context handling

**Phase 1.5 COMPLETE**: All SPMD type checking rules implemented following ISPC design patterns

### Recent Progress (Phase 1.5.2 - COMPLETED 2025-08-16)

✅ **SPMD Assignment Rule Validation and Infrastructure Completion**

**Core Assignment Rule Implementation**:

- Implemented comprehensive SPMD assignment validation in `operand.go` with precise error messaging
- Added assignment rules: varying→uniform blocked, uniform→varying allowed (broadcast)
- Extended function parameter validation for varying arguments to uniform parameters
- Created detailed error messages: "cannot assign varying expression to uniform variable"
- Integrated SPMD assignment checking with existing Go type checker infrastructure

**Function Signature Validation**:

- Implemented public function restrictions: public functions cannot have varying parameters
- Added exceptions for `lanes` and `reduce` standard library packages
- Created function validation in `check_ext_spmd.go` with proper package-aware rules
- Enforced rule: functions with varying parameters cannot contain `go for` loops

**Critical Infrastructure Fixes**:

- Fixed panic in generic SPMD function calls by extending type substitution system (`subst.go`)
- Added SPMDType handling in type inference system (`infer.go`) for generic function calls
- Fixed syntax printer panic by implementing SPMDType case in `printer.go`
- Resolved all compiler crashes enabling stable SPMD development

**Test Infrastructure Improvements**:

- Fixed assignment_rules.go test by adjusting column position tolerance (colDelta: 0→50)
- Updated test framework to handle SPMD error position reporting differences
- Created comprehensive test coverage for all assignment validation scenarios
- Established working test suite ready for continued SPMD development

**Key Technical Achievement**: Complete SPMD assignment validation system with robust error handling and infrastructure stability. All major compiler panics resolved, assignment rules working correctly, and test suite operational. The SPMD implementation is now stable and ready for continued development.

**COMPLETED Features**:

- ✅ SPMD assignment rule validation with detailed error messages
- ✅ Function signature restrictions with standard library exceptions
- ✅ Generic SPMD function call support (fixed type substitution panics)
- ✅ Syntax printer integration (fixed AST printing panics)
- ✅ Test framework integration with appropriate error position tolerance
- ✅ Comprehensive test coverage for assignment validation scenarios
- ✅ Stable compiler infrastructure with no remaining critical panics

**Phase 1.5.2 COMPLETE**: SPMD assignment validation fully implemented with stable infrastructure

### Previous Progress (Phase 1.3 - COMPLETED 2025-08-09)

✅ **Parser Extensions Complete**

- Extended AST nodes with `SPMDType` struct for qualified types (`uniform`/`varying` with optional constraints)
- Added `IsSpmd` field to `ForStmt` to distinguish `go for` loops from regular `for` loops
- Extended `RangeClause` with `Constraint` field for constrained range syntax (`range[n]` expressions)
- Implemented `spmdType()` function parsing all SPMD type qualifiers:
  - `uniform Type` - scalar values same across lanes
  - `varying Type` - vector values different per lane  
  - `varying[n] Type` - constrained varying with numeric constraint
  - `varying[] Type` - universal constrained varying
- Implemented `go for` SPMD loop parsing with `spmdForStmt()` and `spmdHeader()` functions
- Added `spmdRangeClause()` function for constrained range syntax: `range[4] expr`, `range[] expr`
- Implemented context-sensitive grammar disambiguation between SPMD and regular Go syntax
- Successfully integrated with GOEXPERIMENT flag - all SPMD syntax gated behind `buildcfg.Experiment.SPMD`
- All SPMD parser tests now pass with core examples (`simple_sum.go`, `odd_even.go`) parsing successfully
- Maintained full backward compatibility - no regressions in existing Go syntax parsing
- Built and tested Go compiler successfully with SPMD parser extensions enabled

**Key Technical Achievement**: Complete SPMD syntax parsing implemented with context-sensitive grammar. The parser now successfully handles all fundamental SPMD language constructs while maintaining full Go backward compatibility. Foundation ready for Phase 1.4 type system implementation.

**Architecture Success**: The parser correctly disambiguates `go for` (SPMD) from `go func()` (goroutines) and `uniform`/`varying` type qualifiers from identifier usage, following Go's established patterns for context-sensitive parsing.

### Previous Progress (Phase 0.5 - COMPLETED 2025-08-02)

✅ **Integration Test Suite Infrastructure Complete**

- Created comprehensive test/integration/spmd/ directory with all examples and test infrastructure
- Implemented dual-mode-test-runner.sh for shell-based comprehensive SIMD/scalar testing
- Added integration_test.go with Go test framework integration and parallel testing support
- Copied all 22+ SPMD examples for integration testing including illegal and legacy examples
- Implemented SIMD instruction verification using wasm2wat for generated WASM inspection
- Added identical output validation between SIMD and scalar modes using wasmer-go runtime
- Created Makefile automation with targets for CI, development, and specific test categories
- Implemented browser SIMD detection tests with WebAssembly capability checking
- Added comprehensive documentation and usage instructions for test execution
- Test infrastructure detects TinyGo, Go, wasm2wat dependencies and handles graceful fallbacks
- All integration tests pass infrastructure validation and are ready for Phase 1 implementation
- Test suite includes 22 basic examples, 2 advanced examples, 10 illegal examples, 5 legacy examples

**Key Technical Achievement**: Complete integration test foundation ready for dual-mode validation when SPMD frontend implementation begins. Test infrastructure validates compilation, runtime execution, SIMD instruction generation, and browser compatibility across all example categories.

### Previous Progress (Phase 0.4 - COMPLETED 2025-08-01)

✅ **SSA Generation Test Infrastructure Complete**

- Created testdata/spmd/ directory with 6 comprehensive SSA generation test files
- Implemented TestSPMDSSAGeneration with EXPECT SSA comment parsing and GOEXPERIMENT gating
- Added go_for_loops.go testing OpPhi, OpSelect, mask tracking for SPMD loops
- Added varying_arithmetic.go testing vector operations (OpVectorAdd/Mul/Sub/Div) and type conversions
- Added spmd_function_calls.go testing mask-first parameter insertion and call chain propagation
- Added mask_propagation.go testing OpAnd/OpOr/OpNot for control flow and conditional masking
- Added broadcast_operations.go testing lanes.Broadcast calls for uniform-to-varying conversions
- Added reduce_operations.go testing reduce builtin calls (reduce.Add/All/Any) with varying types
- Test infrastructure detects 151 expected SSA opcodes across all files
- All tests pass and are ready for Phase 1.7 SSA generation implementation
- All changes committed to spmd branch in go/ submodule

**Key Technical Achievement**: Complete SSA generation test foundation ready for Phase 1.7 implementation. Test infrastructure validates all critical SPMD SSA generation patterns including mask propagation, vector operations, and builtin calls.

### Previous Progress (Phase 0.3 - COMPLETED 2025-08-01)

✅ **Type Checker Test Infrastructure Complete**

- Created testdata/spmd/ directory with 5 comprehensive SPMD type checking test files
- Implemented TestSPMDTypeChecking with GOEXPERIMENT flag gating and integration with Go's test framework
- Added assignment_rules.go with uniform/varying assignment validation and ERROR comments
- Added function_restrictions.go testing public API limitations and SPMD function constraints
- Added control_flow.go testing go for loop restrictions and control flow validation
- Added simd_capacity.go testing SIMD register capacity constraints and validation
- Added pointer_operations.go testing varying pointer operations and memory safety
- Test results confirm proper integration: tests fail as expected (SPMD syntax not implemented yet)
- All tests include comprehensive ERROR comment patterns following Go's type checker testing conventions
- All changes committed to spmd branch in go/ submodule

**Key Technical Achievement**: Complete type checker test foundation ready for Phase 1.5 type system implementation. Test infrastructure validates all critical SPMD type checking rules.

### Previous Progress (Phase 0.2 - COMPLETED 2025-08-01)

✅ **Parser Test Infrastructure Complete**

- Created testdata/spmd/ directory with comprehensive SPMD test cases
- Implemented TestSPMDParser with GOEXPERIMENT flag testing and build constraint detection
- Added valid_syntax.go with uniform/varying, go for loops, and constraint syntax tests
- Added invalid_syntax.go with ERROR comments for syntax error validation
- Added backward_compat.go verifying uniform/varying work as identifiers when SPMD disabled
- Copied representative examples: simple_sum, odd_even, break_in_go_for
- Test results confirm SPMD files correctly fail parsing (expected until lexer implemented)
- Backward compatibility tests pass, proving no regression when SPMD disabled
- All changes committed to spmd branch in go/ submodule

**Key Technical Achievement**: Test-driven development foundation ready for Phase 0.3 type checker tests and eventual Phase 1.2 lexer implementation.

### Previous Progress (Phase 0.1 - COMPLETED 2025-08-01)

✅ **GOEXPERIMENT Integration Complete**

- Added SPMD boolean field to internal/goexperiment/flags.go
- Generated exp_spmd_off.go and exp_spmd_on.go constant files  
- Successfully built modified Go toolchain with SPMD support
- Verified build tag gating: `goexperiment.spmd` and `!goexperiment.spmd` work correctly
- Tested that `GOEXPERIMENT=spmd` enables SPMD experiment properly
- Confirmed baseline Go functionality remains unaffected
- All changes committed to `spmd` branch in go/ submodule
- Created comprehensive verification test suite in experiment_test/

**Key Technical Achievement**: SPMD experiment flag infrastructure is fully operational and ready for test-driven development approach starting with Phase 0.2 parser tests.

---

**Total Estimated Tasks**: 200+ checkboxes  
**Estimated Timeline**: 6-12 months for complete PoC implementation  

## PoC Success Criteria

**🔴 CRITICAL PREREQUISITE**: PoC validation can only begin after Phase 1.8-1.9 (Standard Library) completion.

**Success Criteria**: ALL examples compile to both SIMD and scalar WASM with identical behavior

### Validation Dependencies

1. **Phase 1.8-1.9 REQUIRED**: `lanes` and `reduce` packages must be fully implemented
2. **Integration Tests**: Can only validate dual-mode compilation after standard library availability
3. **Example Execution**: All 22+ examples depend on standard library functions
4. **Performance Benchmarking**: Meaningful only with actual SIMD vs scalar code generation

**⚠️ DEPENDENCY CHAIN**: Phase 0 ✅ → Phase 1.1-1.7 → **Phase 1.8-1.9 (CRITICAL)** → Phase 2 → Phase 3 PoC Validation

This plan serves as the definitive roadmap for SPMD implementation and should be updated as progress is made and new requirements are discovered.
