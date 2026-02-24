# Proof of Concept Testing Workflow

Reference document for PoC success criteria and testing scripts. Extracted from CLAUDE.md for on-demand search by agents.

## Success Criteria

The TinyGo PoC implementation succeeds when ALL examples compile to both SIMD and scalar WASM:

1. **Frontend Validation**: SPMD syntax parses and type-checks correctly for all examples
2. **Dual Code Generation**: All examples generate both SIMD128 and scalar fallback WASM
3. **SIMD Mode**: Varying arithmetic produces `v128.*` WASM instructions when SIMD enabled
4. **Scalar Mode**: Varying arithmetic produces scalar loops when SIMD disabled
5. **Runtime Execution**: Both SIMD and scalar WASM binaries execute correctly in wasmer-go
6. **Performance Benchmarking**: Measurable performance difference between SIMD vs scalar modes
7. **Browser Compatibility**: Ability to detect SIMD support and load appropriate WASM file
8. **Complete Libraries**: All `lanes` and `reduce` functions needed for the examples work in both modes
9. **Complex Examples**: IPv4 parser and base64 decoder compile and execute in both modes
10. **Error Handling**: Clean error messages for invalid SPMD code

## 1. Build Test Runner

```bash
# Create wasmer-go test runner
go mod init spmd-tester
go get github.com/wasmerio/wasmer-go/wasmer
```

## 2. Compile and Test Examples (Dual Mode)

```bash
# Compile ALL examples to both SIMD and scalar WASM
for example in simple-sum odd-even bit-counting array-counting printf-verbs hex-encode to-upper base64-decoder ipv4-parser debug-varying goroutine-varying defer-varying panic-recover-varying map-restrictions pointer-varying type-switch-varying non-spmd-varying-return spmd-call-contexts lanes-index-restrictions union-type-generics infinite-loop-exit uniform-early-return; do
    echo "Testing $example in dual mode..."

    # Compile SIMD version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o $example-simd.wasm examples/$example/main.go

    # Compile scalar fallback version
    GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o $example-scalar.wasm examples/$example/main.go

    # Verify SIMD version contains SIMD instructions
    simd_count=$(wasm2wat $example-simd.wasm | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$simd_count" -eq 0 ]; then
        echo "ERROR: SIMD version contains no SIMD instructions"
        exit 1
    fi

    # Verify scalar version contains no SIMD instructions
    scalar_simd_count=$(wasm2wat $example-scalar.wasm | grep -cE "(v128|i32x4|f32x4)" || true)
    if [ "$scalar_simd_count" -ne 0 ]; then
        echo "ERROR: Scalar version contains SIMD instructions"
        exit 1
    fi

    # Execute both versions and verify identical output
    simd_output=$(go run wasmer-runner.go $example-simd.wasm)
    scalar_output=$(go run wasmer-runner.go $example-scalar.wasm)

    if [ "$simd_output" != "$scalar_output" ]; then
        echo "ERROR: SIMD and scalar outputs differ"
        echo "SIMD: $simd_output"
        echo "Scalar: $scalar_output"
        exit 1
    fi

    echo "$example: SIMD($simd_count instructions) and scalar modes both work"
done
```

## 3. Performance Validation and Browser Integration

```bash
# Benchmark SIMD vs scalar performance
for example in simple-sum ipv4-parser base64-decoder; do
    echo "Benchmarking $example..."

    # Time SIMD version
    simd_time=$(time go run wasmer-runner.go $example-simd.wasm 2>&1 | grep real)

    # Time scalar version
    scalar_time=$(time go run wasmer-runner.go $example-scalar.wasm 2>&1 | grep real)

    echo "$example SIMD: $simd_time"
    echo "$example Scalar: $scalar_time"
done

# Create browser SIMD detection helper
cat > simd-loader.js << 'EOF'
// Browser-side SIMD detection and WASM loading
async function loadOptimalWasm(baseName) {
    const supportsSimd = WebAssembly.validate(new Uint8Array([
        0x00, 0x61, 0x73, 0x6d, // WASM magic
        0x01, 0x00, 0x00, 0x00, // Version
        0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b, // Type section (v128)
    ]));

    const wasmFile = supportsSimd ? `${baseName}-simd.wasm` : `${baseName}-scalar.wasm`;
    console.log(`Loading ${wasmFile} (SIMD support: ${supportsSimd})`);

    return await WebAssembly.instantiateStreaming(fetch(wasmFile));
}
EOF
```
