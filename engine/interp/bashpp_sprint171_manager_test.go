package interp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceSprint171DerefNewTypeParamDoesNotCrash pins request R2 of the
// Sprint 171 w2-imports FINDINGS: `unsafe.Sizeof(*new(T))` under a type
// parameter crashed the interpreter (nil pointer type dereferenced in the
// static type walk). It now either answers or refuses with a diagnostic;
// either way the runner returns instead of panicking.
func TestGoSourceSprint171DerefNewTypeParamDoesNotCrash(t *testing.T) {
	path := filepath.Join("testdata", "sprint171", "manager", "deref_new_typeparam.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("interpreter panicked: %v", r)
		}
	}()
	err = runner.Run(context.Background(), program.File)
	got := output.String()
	if err == nil && strings.TrimSpace(got) != "8" {
		t.Fatalf("unexpected success with output %q", got)
	}
	if err != nil && !strings.Contains(got, "BASHPP-E") {
		t.Fatalf("refusal is not a diagnostic: %v; output=%q", err, got)
	}
}
