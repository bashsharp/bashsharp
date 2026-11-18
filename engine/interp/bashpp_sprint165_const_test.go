//go:build full

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

const sprint165ConstRoot = "testdata/sprint165/interp-const/unsafe_const"

func sprint165ConstGoRun(t *testing.T, name string) (string, error) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(sprint165ConstRoot, name+".go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		return "", err
	}
	var output strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(context.Background(), program.File)
	return output.String(), err
}

func sprint165ConstExpected(t *testing.T, name string) string {
	t.Helper()
	want, err := os.ReadFile(filepath.Join(sprint165ConstRoot, name+".expected"))
	if err != nil {
		t.Fatal(err)
	}
	return string(want)
}

func TestGoSourceSprint165ConstLayout(t *testing.T) {
	for _, name := range []string{"const_layout", "supported"} {
		got, err := sprint165ConstGoRun(t, name)
		if err != nil {
			t.Fatalf("%s: run: %v; output=%q", name, err, got)
		}
		if want := sprint165ConstExpected(t, name); got != want {
			t.Fatalf("%s: output %q, want %q", name, got, want)
		}
	}
}

func TestGoSourceSprint165ConstDiagnostics(t *testing.T) {
	got, err := sprint165ConstGoRun(t, "nonconstant")
	if err == nil {
		t.Fatalf("non-constant initializer unexpectedly succeeded; output=%q", got)
	}
	const wording = "nonconstant.go:5:16: runtimeValue (variable of type int) is not constant"
	if got != "" || !strings.Contains(err.Error(), wording) || strings.Count(err.Error(), wording) != 1 {
		t.Fatalf("diagnostic output=%q error=%q, want exactly one %q", got, err, wording)
	}
}

func TestGoSourceSprint165ConstClassicParity(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(sprint165ConstRoot, "classic_parity.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(string(source)), "classic_parity.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &output, &output))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; output=%q", err, output.String())
	}
	if want := sprint165ConstExpected(t, "classic_parity"); output.String() != want {
		t.Fatalf("classic output %q, want byte-identical %q", output.String(), want)
	}
}
