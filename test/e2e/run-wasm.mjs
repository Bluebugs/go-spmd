// SPMD E2E Test Runner — executes WASI WASM files via Node.js
// Usage: node run-wasm.mjs <file.wasm> [--export <funcname>]
import { readFileSync } from 'fs';
import { WASI } from 'wasi';
import { argv, exit } from 'process';

const args = argv.slice(2);
if (args.length === 0) {
  console.error('Usage: node run-wasm.mjs <file.wasm> [--export <funcname>]');
  exit(1);
}

const wasmPath = args[0];
let exportName = null;
if (args[1] === '--export' && args[2]) {
  exportName = args[2];
}

const wasi = new WASI({
  version: 'preview1',
  args: [wasmPath],
  env: { SPMD_RUNTIME: 'node-wasi' },
});

const wasmBytes = readFileSync(wasmPath);
const module = await WebAssembly.compile(wasmBytes);
const importObject = wasi.getImportObject();
// TinyGo uses asyncify for goroutine support — provide no-op stubs
importObject.asyncify = {
  start_unwind: () => {},
  stop_unwind: () => {},
  start_rewind: () => {},
  stop_rewind: () => {},
};
const instance = await WebAssembly.instantiate(module, importObject);

if (exportName) {
  // Call a specific exported function and print its return value
  const fn = instance.exports[exportName];
  if (!fn) {
    console.error(`Export '${exportName}' not found`);
    exit(1);
  }
  const result = fn();
  console.log(result);
} else {
  // Run WASI _start
  try {
    wasi.start(instance);
  } catch (e) {
    if (e.constructor.name === 'ExitStatus' || e.message?.includes('exit')) {
      // Normal WASI exit
    } else {
      throw e;
    }
  }
}
