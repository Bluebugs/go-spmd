package spmd_integration_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPMDBase64DecoderRegression(t *testing.T) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}

	if _, err := exec.LookPath("wasmtime"); err != nil {
		t.Skipf("wasmtime not available: %v", err)
	}

	want := strings.Join([]string{
		"'SGVsbG8gV29ybGQ=' -> 'Hello World'",
		"'Zm9vYmFy' -> 'foobar'",
		"'YWJjZA==' -> 'abcd'",
	}, "\n") + "\n"

	for _, tc := range []struct {
		name string
		simd string
	}{
		{name: "simd", simd: "true"},
		{name: "scalar", simd: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wasmFile := filepath.Join(root, "test", "integration", "spmd", "base64-decoder", "base64-regression-"+tc.name+".wasm")
			t.Cleanup(func() {
				_ = exec.Command("rm", "-f", wasmFile).Run()
			})

			build := exec.Command("./tinygo/build/tinygo", "build", "-target=wasi", "-simd="+tc.simd, "-o", wasmFile, "test/integration/spmd/base64-decoder/main.go")
			build.Dir = root
			build.Env = append(build.Environ(), "GOROOT="+filepath.Join(root, "go"), "GOEXPERIMENT=spmd")
			buildOutput, err := build.CombinedOutput()
			if err != nil {
				t.Fatalf("build failed: %v\n%s", err, buildOutput)
			}

			run := exec.Command("wasmtime", "run", wasmFile)
			run.Dir = root
			runOutput, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("run failed: %v\n%s", err, runOutput)
			}

			if string(runOutput) != want {
				t.Fatalf("unexpected output:\nwant:\n%s\ngot:\n%s", want, runOutput)
			}
		})
	}
}
