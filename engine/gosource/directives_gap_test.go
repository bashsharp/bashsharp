package gosource

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestDeclarationDirectivesAcrossBlankLines(t *testing.T) {
	name := filepath.Join("testdata", "sprint162", "directive-gap", "directives.go")
	source, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	program, err := Parse(bytes.NewReader(source), name, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"Adjacent":  {"go:noinline"},
		"Separated": {"go:noescape"},
		"Before":    nil,
		"After":     {"go:nosplit"},
	}
	for _, stmt := range program.File.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok {
			continue
		}
		var got []string
		for _, comment := range stmt.Comments {
			got = append(got, comment.Text)
		}
		if !reflect.DeepEqual(got, want[decl.Name.Value]) {
			t.Errorf("%s directives = %q, want %q", decl.Name.Value, got, want[decl.Name.Value])
		}
	}
}
