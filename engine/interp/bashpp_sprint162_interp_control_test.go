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

func TestBashPPSprint162RecoverExpression(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-control")
	for _, name := range []string{"recover_expr", "recover_int"} {
		source, err := os.ReadFile(filepath.Join(root, name+".go"))
		if err != nil {
			t.Fatal(err)
		}
		program, err := gosource.Parse(strings.NewReader(string(source)), name+".go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(context.Background(), program.File); err != nil {
			t.Fatalf("%s: run: %v, output=%q", name, err, out.String())
		}
		want, err := os.ReadFile(filepath.Join(root, name+".expected"))
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != string(want) {
			t.Fatalf("%s: output %q, want %q", name, out.String(), string(want))
		}
	}
}

func TestBashPPSprint162RecoverDirectCallNegative(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "interp-control")
	source, err := os.ReadFile(filepath.Join(root, "recover_direct_negative.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(string(source)), "recover_direct_negative.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runner, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program.File); err == nil || !strings.Contains(out.String(), "panic: must escape") {
		t.Fatalf("direct recover unexpectedly recovered: err=%v output=%q", err, out.String())
	}
}
