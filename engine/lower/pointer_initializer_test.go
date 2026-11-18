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

// TestGoSourcePointerInitializer drives a returned pointer through lowering
// and the pinned compiler. The direct address initializer is the supported
// control. Exact output and status plus the deadline reject wrong values,
// stray output, wrong status, and hangs.
func TestGoSourcePointerInitializer(t *testing.T) {
	name := filepath.Join("testdata", "sprint165", "lower-1", "pointer-initializer", "main.go")
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
	if string(output) != "7 9\n" {
		t.Fatalf("generated output = %q, want exactly %q\n--- generated\n%s", output, "7 9\n", result.Source)
	}
}
