# Browser SIMD Detection and WebAssembly Loading

This example demonstrates automatic SIMD detection in browsers and optimal WebAssembly loading for SPMD Go applications.

## Features

- **Automatic SIMD Detection**: Detects browser WebAssembly SIMD support
- **Optimal Loading**: Automatically loads SIMD or scalar WASM based on capability
- **Performance Benchmarking**: Compare SIMD vs scalar performance
- **Real-World Examples**: IPv4 parser and base64 decoder benchmarks

## Usage

### 1. Compile Both Versions

```bash
# Compile SIMD versions
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o simple-sum-simd.wasm ../simple-sum/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o ipv4-parser-simd.wasm ../ipv4-parser/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=true -o base64-decoder-simd.wasm ../base64-decoder/main.go

# Compile scalar fallback versions  
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o simple-sum-scalar.wasm ../simple-sum/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o ipv4-parser-scalar.wasm ../ipv4-parser/main.go
GOEXPERIMENT=spmd tinygo build -target=wasi -simd=false -o base64-decoder-scalar.wasm ../base64-decoder/main.go
```

### 2. Serve Files

```bash
# Simple HTTP server
python3 -m http.server 8000

# Or use any web server
npx serve .
```

### 3. Open Browser

Navigate to `http://localhost:8000` and the page will:

1. Automatically detect SIMD support
2. Load the optimal WebAssembly version
3. Provide benchmarking tools
4. Compare performance between modes

## SIMD Detection

The detection uses WebAssembly validation with v128 type:

```javascript
const simdSupported = WebAssembly.validate(new Uint8Array([
    0x00, 0x61, 0x73, 0x6d, // WASM magic
    0x01, 0x00, 0x00, 0x00, // Version  
    0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7b, // Type section (v128)
]));
```

## Browser Compatibility

### SIMD Support Status:
- ✅ **Chrome 91+**: Full WebAssembly SIMD support
- ✅ **Firefox 89+**: Full WebAssembly SIMD support  
- ✅ **Edge 91+**: Full WebAssembly SIMD support
- ❌ **Safari**: Limited/experimental support
- ❌ **Mobile browsers**: Varies by device and browser

### Fallback Strategy:
When SIMD is not supported, the scalar version provides:
- ✅ **Identical functionality**: Same algorithm, different implementation
- ✅ **Broad compatibility**: Works on all WebAssembly-capable browsers
- ⚠️ **Reduced performance**: No SIMD acceleration

## Performance Expectations

For SIMD-capable browsers, expect significant speedups:

| Algorithm | Expected SIMD Speedup |
|-----------|----------------------|
| Simple Sum | 2-4x faster |
| IPv4 Parser | 3-6x faster |
| Base64 Decoder | 4-8x faster |

## Implementation Notes

### Dual Build Strategy

The PoC generates two WebAssembly files per example:
- `example-simd.wasm`: Uses WebAssembly SIMD128 instructions
- `example-scalar.wasm`: Uses scalar loops and operations

### Runtime Loading

```javascript
async function loadOptimalWasm(baseName) {
    const simdSupported = await detectSIMDSupport();
    const wasmFile = simdSupported ? `${baseName}-simd.wasm` : `${baseName}-scalar.wasm`;
    
    return await WebAssembly.instantiateStreaming(fetch(wasmFile));
}
```

### Benchmarking

The demo provides:
- **Correctness verification**: Both versions produce identical results
- **Performance measurement**: Timing comparison between modes
- **Real-world examples**: Complex algorithms showing SIMD benefits

## Development Integration

This approach enables:

1. **Progressive Enhancement**: Start with scalar, add SIMD benefits where available
2. **Broad Deployment**: Single codebase works across all browsers
3. **Performance Optimization**: Automatic best-performance selection
4. **Future-Proofing**: Ready for expanding SIMD browser support

## Testing

```bash
# Verify both versions work
go run ../wasmer-runner.go simple-sum-simd.wasm
go run ../wasmer-runner.go simple-sum-scalar.wasm

# Verify identical output
diff <(go run ../wasmer-runner.go simple-sum-simd.wasm) \
     <(go run ../wasmer-runner.go simple-sum-scalar.wasm)
```

This demonstrates the complete SPMD Go WebAssembly deployment strategy with automatic optimization based on browser capabilities.