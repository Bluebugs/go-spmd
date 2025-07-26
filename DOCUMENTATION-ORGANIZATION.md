# SPMD Documentation Organization Rules

This document defines clear rules for organizing SPMD-related information across different documentation files to maintain consistency and avoid duplication.

## Document Purposes and Content Rules

### 1. SPECIFICATIONS.md - Language Specification
**Purpose**: Official Go language specification for SPMD extensions

**Contains**:
- **Formal language syntax** (lexical elements, grammar rules)
- **Type system definitions** (uniform/varying types, constraints, compatibility rules)  
- **Semantic rules** (assignment rules, function semantics, execution model)
- **Built-in function specifications** (lanes.*, reduce.* function signatures and semantics)
- **Standard library integration** (fmt.Printf behavior, interface{} handling)
- **Complete working examples** (demonstrating correct usage patterns)
- **Error conditions** (what should cause compile-time errors)
- **Backward compatibility** (how existing Go code remains unchanged)

**Writing Style**: Formal specification language, precise definitions, normative statements

**Authority**: This is the **definitive reference** for language behavior

### 2. GOEXPERIMENT-IMPLEMENTATION.md - Implementation Plan
**Purpose**: Detailed implementation roadmap for Go compiler and TinyGo backend

**Contains**:
- **Compiler phase breakdown** (lexer, parser, type checker, SSA generation, LLVM backend)
- **Code organization** (which files to modify, function signatures, data structures)
- **Implementation strategies** (how to implement language features in code)
- **Testing approaches** (unit tests, integration tests, validation strategies)
- **Build system integration** (GOEXPERIMENT flag, build constraints, feature gating)
- **Development workflow** (test-driven development, commit guidelines)
- **Technical implementation details** (SSA opcodes, LLVM IR generation, WASM targets)

**Writing Style**: Technical implementation details, code snippets, step-by-step procedures

**Authority**: Implementation guidance for **developers building the compiler**

### 3. CLAUDE.md - Project Overview and Context
**Purpose**: High-level project context and development instructions for Claude

**Contains**:
- **Project vision** (goals, scope, proof-of-concept boundaries)
- **Key concepts** (SPMD model, uniform/varying, lanes, execution masks)
- **Architecture overview** (Go frontend + TinyGo backend approach)
- **Development principles** (commit rules, git workflow, TDD approach)
- **Critical implementation rules** (type system constraints, function restrictions)
- **Reference materials** (links to ISPC, blog posts, related work)
- **Success criteria** (what constitutes successful PoC implementation)

**Writing Style**: Explanatory, educational, provides context and rationale

**Authority**: **Project guidance** and **high-level direction**

### 4. Examples Directory - Working Code Demonstrations
**Purpose**: Concrete, executable code examples showing SPMD features

**Contains**:
- **Simple demonstrations** (basic SPMD loops, reduce operations)
- **Complex algorithms** (IPv4 parser, base64 decoder, encryption)
- **Edge cases and restrictions** (illegal SPMD patterns, error conditions)
- **Testing patterns** (how to validate SPMD code works correctly)
- **Performance comparisons** (SIMD vs scalar implementations)
- **Legacy compatibility** (showing existing Go code still works)

**Writing Style**: Executable Go code with thorough comments

**Authority**: **Concrete demonstrations** of how language features work in practice

## Content Allocation Rules

### When to Update Which Document

#### Language Feature Changes
- **Specification change** → Update SPECIFICATIONS.md first (authoritative)
- **Implementation approach** → Update GOEXPERIMENT-IMPLEMENTATION.md 
- **Working examples** → Add/modify examples in examples/
- **Context or rationale** → Update CLAUDE.md if needed

#### Function/API Changes  
1. **SPECIFICATIONS.md**: Update function signature, semantics, constraints
2. **GOEXPERIMENT-IMPLEMENTATION.md**: Update implementation details (intrinsics, SSA generation)
3. **Examples**: Add/update examples showing new functionality
4. **CLAUDE.md**: Update if conceptual changes affect project understanding

#### Type System Changes
1. **SPECIFICATIONS.md**: Update type definitions, constraints, rules
2. **GOEXPERIMENT-IMPLEMENTATION.md**: Update type checker implementation
3. **Examples**: Add examples demonstrating new type behaviors
4. **CLAUDE.md**: Update if fundamental concepts change

#### Error Handling Changes
1. **SPECIFICATIONS.md**: Define what should be errors and when
2. **GOEXPERIMENT-IMPLEMENTATION.md**: Implement error checking logic
3. **Examples**: Add illegal-spmd/ examples showing error cases
4. **CLAUDE.md**: Update if error philosophy changes

### Cross-Reference Rules

#### From SPECIFICATIONS.md
- **MAY reference** CLAUDE.md for rationale or background context
- **SHOULD NOT reference** implementation details from GOEXPERIMENT-IMPLEMENTATION.md
- **MAY reference** examples/ for concrete demonstrations

#### From GOEXPERIMENT-IMPLEMENTATION.md  
- **MUST reference** SPECIFICATIONS.md for authoritative behavior definitions
- **MAY reference** CLAUDE.md for project context and goals
- **SHOULD reference** examples/ for test cases and validation

#### From CLAUDE.md
- **SHOULD reference** SPECIFICATIONS.md as the definitive language reference
- **MAY reference** GOEXPERIMENT-IMPLEMENTATION.md for technical depth
- **SHOULD reference** examples/ for concrete demonstrations

#### From Examples
- **Comments SHOULD reference** SPECIFICATIONS.md section numbers for language features used
- **README files MAY reference** other documents for additional context

## Consistency Maintenance Rules

### 1. Single Source of Truth
- **Language behavior**: SPECIFICATIONS.md is authoritative
- **Implementation approach**: GOEXPERIMENT-IMPLEMENTATION.md is authoritative  
- **Project direction**: CLAUDE.md is authoritative
- **Working code**: examples/ are authoritative

### 2. Update Propagation
When changing authoritative content:
1. **Update the authoritative document first**
2. **Update dependent documents** to maintain consistency
3. **Update examples** to reflect changes
4. **Verify no contradictions** exist across documents

### 3. Avoiding Duplication
- **Don't repeat** detailed specifications in implementation docs
- **Don't repeat** implementation details in specification  
- **Do provide** appropriate cross-references between documents
- **Do maintain** consistent terminology across all documents

### 4. Version Consistency
- All documents must reflect the **same version** of the language design
- When making breaking changes, update **all relevant documents** in the same commit
- Use **git commits** to maintain consistency across document updates

## Review Checklist

When updating any document, verify:

- [ ] **Content belongs** in this document according to purpose rules
- [ ] **No contradictions** with other documents  
- [ ] **Cross-references** are accurate and up-to-date
- [ ] **Examples work** with described language features
- [ ] **Implementation details** match specification requirements
- [ ] **Terminology** is consistent across all documents

## Document Maintenance Workflow

### For Language Feature Addition:
1. **Design phase**: Update CLAUDE.md with concept and rationale
2. **Specification phase**: Add formal definition to SPECIFICATIONS.md
3. **Implementation phase**: Add implementation plan to GOEXPERIMENT-IMPLEMENTATION.md
4. **Validation phase**: Create examples demonstrating the feature
5. **Review phase**: Verify consistency across all documents

### For Bug Fixes:
1. **Identify authoritative source**: Which document defines correct behavior?
2. **Fix authoritative document**: Correct the canonical definition
3. **Update dependent documents**: Propagate fixes to implementation/examples
4. **Validate consistency**: Ensure no contradictions remain

This organization ensures each document serves its specific purpose while maintaining overall project coherence.