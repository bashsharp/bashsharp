package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// TestGoSourceConstIota compiles and executes an outside-corpus fixture so a
// fixture without a driving test cannot masquerade as evidence. The first
// group is the supported control. The remaining groups cover a declaration
// named iota, omitted expressions after that shadow, and an omitted expression
// which continues to use a package constant. Exact stdout, successful status,
// and the deadline reject wrong values, stray output, wrong status, and hangs.
func TestGoSourceConstIota(t *testing.T) {
	const name = "shadow.go"
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "lower-1", "const-iota", name))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), name, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: name, Package: program.Package})
	if err != nil {
		t.Fatalf("compile: %#v", err)
	}

	dir := t.TempDir()
	generated := filepath.Join(dir, "main.go")
	if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", generated)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("generated program failed: %v\n%s\n--- generated\n%s", err, output.Bytes(), result.Source)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("generated program exceeded deadline\n--- generated\n%s", result.Source)
	}
	if got, want := strings.TrimSpace(output.String()), "0 1 0 1 1 4 4 1"; got != want {
		t.Fatalf("generated output = %q, want %q\n--- generated\n%s", got, want, result.Source)
	}
}
