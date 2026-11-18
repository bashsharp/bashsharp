//go:build full

package lower_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// TestGoSourceTypedWideUint drives a typed uint wider than int64 through the
// pinned compiler. A small typed uint is the positive control. Exact output,
// status, and deadline reject wrong values, stray output, wrong status, and
// hangs.
func TestGoSourceTypedWideUint(t *testing.T) {
	name := filepath.Join("testdata", "sprint165", "lower-1", "typed-wide-uint", "main.go")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), name, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name, Package: program.Package})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	generated := filepath.Join(dir, "main.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", generated)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.27.1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("generated program exceeded its deadline")
	}
	if err != nil {
		t.Fatalf("generated program failed: %v\n%s\n--- generated\n%s", err, output, result.Source)
	}
	if string(output) != "true\n" {
		t.Fatalf("generated output = %q, want exactly %q\n--- generated\n%s", output, "true\n", result.Source)
	}
}

// TestClassicWideIntegerParity is the Classic-isolation gate for the scalar
// rendering change: Classic still uses arbitrary-precision integer storage.
func TestClassicWideIntegerParity(t *testing.T) {
	out, stderr := execute(t, compile(t, "n := 18446744073709551615\nprintf '%s\\n' \"$n\"\n"))
	if out != "18446744073709551615\n" || stderr != "" {
		t.Fatalf("Classic output = %q, stderr = %q", out, stderr)
	}
}
