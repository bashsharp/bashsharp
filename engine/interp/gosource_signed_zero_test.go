package interp_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGoSourceSignedZeroFixedbugsRoots covers the runtime and constant forms
// exercised by issue6899 and issue12577 without depending on corpus files.
func TestGoSourceSignedZeroFixedbugsRoots(t *testing.T) {
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	cases := map[string]string{
		"runtime_negative_zero": `package main
import "math"
func main() { println(math.Copysign(0, -1)) }
`,
		"constant_zero_and_runtime_negation": `package main
import "math"
const z0 = 0.0
const z1 = -0.0
var x0 float32 = z0
var x1 float32 = z1
var y0 float64 = z0
var y1 float64 = z1
func main() {
	if f := -x0; f != 0 || !math.Signbit(float64(f)) { println("bad -float32") }
	if x1 != 0 || math.Signbit(float64(x1)) { println("bad const float32") }
	if f := -y0; f != 0 || !math.Signbit(f) { println("bad -float64") }
	if y1 != 0 || math.Signbit(y1) { println("bad const float64") }
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "main.go")
			if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			oracle := filepath.Join(dir, "oracle")
			if output, err := exec.Command(goBinary, "build", "-o", oracle, sourcePath).CombinedOutput(); err != nil {
				t.Fatalf("oracle build: %v: %s", err, output)
			}
			var wantOut, wantErr bytes.Buffer
			native := exec.Command(oracle)
			native.Stdout, native.Stderr = &wantOut, &wantErr
			if err := native.Run(); err != nil {
				t.Fatalf("oracle run: %v", err)
			}
			gotOut, gotErr, runErr := bashPPRunGoSource(t, dir, sourcePath, source)
			if runErr != nil {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", runErr, gotOut, gotErr)
			}
			if gotOut != wantOut.String() || gotErr != wantErr.String() {
				t.Fatalf("Runner stdout/stderr %q/%q; oracle %q/%q", gotOut, gotErr, wantOut.String(), wantErr.String())
			}
		})
	}
}
