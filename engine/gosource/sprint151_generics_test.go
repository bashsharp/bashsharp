//go:build full

package gosource_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestSprint151Generics runs one small out-of-corpus Go program per
// generics/interface mechanism catalogued in
// testdata/sprint151/generics/FINDINGS.md through gosource.Load + interp and
// compares stdout with go run. Every program is valid Go with deterministic
// output.
func TestSprint151Generics(t *testing.T) {
	for _, file := range []string{
		"infer_typeargs.go",
		"chan_typeparam.go",
		"generic_funcvalue.go",
		"typeswitch_operand.go",
		"embed_type_term.go",
		"iface_generic_method.go",
		"named_func_conversion.go",
		"typeparam_method_expr.go",
		"map_typeparam_key.go",
		"recursive_constrained.go",
		"term_constraints.go",
		"blank_typeparam.go",
	} {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("testdata", "sprint151", "generics", file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			goOut, err := exec.Command("go", "run", path).Output()
			if err != nil {
				t.Fatalf("go run: %v", err)
			}
			program, err := gosource.Load([]gosource.Source{{Name: file, Data: data}}, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), program.File); err != nil || stderr.Len() > 0 {
				t.Fatalf("interpreter: err=%v stderr=%q", err, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), goOut) {
				t.Fatalf("stdout differs from go run\ninterpreter: %q\ngo run:      %q", stdout.Bytes(), goOut)
			}
		})
	}
}
