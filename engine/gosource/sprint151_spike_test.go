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
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestSprint151Spike pins the converter's CURRENT verdict on one small
// out-of-corpus Go program per rejection mechanism catalogued in
// testdata/sprint151/FINDINGS-M2.md. Every program is valid Go (go vet clean,
// deterministic output), so it doubles as a regression test once a mechanism
// is implemented: the row's want must then flip to "" and the program should
// graduate into a run-and-compare table such as TestUnchangedGo.
func TestSprint151Spike(t *testing.T) {
	for _, tc := range []struct {
		mechanism, file, want string
	}{
		{"labeled-branch/for-break", "labeled_break_for.go", ""},
		{"labeled-branch/for-continue", "labeled_continue_for.go", ""},
		{"labeled-branch/switch-break", "labeled_break_switch.go", ""},
		{"labeled-branch/select-break", "labeled_break_select.go", ""},
		{"goto/backward", "goto_backward.go", ""},
		{"goto/forward", "goto_forward.go", ""},
		{"expression-statement/paren-call", "exprstmt_paren_call.go", ""},
		{"expression-statement/bare-type-switch", "exprstmt_bare_typeswitch.go", ""},
		{"expression-kind/IndexListExpr", "expr_indexlist_funcvalue.go", ""},
		{"expression-kind/type-switch-composite-case", "expr_typeswitch_composite_case.go", ""},
		{"type-kind/indexed-call", "type_indexed_call.go", ""},
		{"type-kind/indexed-call-ident", "type_indexed_call_ident.go", ""},
		{"range-target/index", "range_target_index.go", ""},
		{"range-target/field", "range_target_field.go", ""},
		{"range-target/deref", "range_target_deref.go", ""},
		{"compound-simple-statement/for-init", "forinit_compound.go", "forinit_compound.go:11:6: gosource: unsupported compound simple statement"},
		{"function-value-type/generic-selector", "funcvalue_generic_selector.go", ""},
		{"function-value-type/named-func-type", "funcvalue_named_type.go", ""},
	} {
		t.Run(tc.mechanism, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "sprint151", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			_, err = gosource.Load([]gosource.Source{{Name: tc.file, Data: data}}, gosource.Options{RunMain: true})
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("%s: converter verdict changed\n got: %q\nwant: %q\nupdate FINDINGS-M2.md and, if the mechanism is now implemented, promote the program to a run-and-compare test", tc.file, got, tc.want)
			}
		})
	}
}

func TestSprint151Implemented(t *testing.T) {
	for _, file := range []string{
		"exprstmt_bare_typeswitch.go",
		"exprstmt_paren_call.go",
		"type_indexed_call.go",
		"type_indexed_call_ident.go",
		"range_target_index.go",
		"range_target_field.go",
		"range_target_deref.go",
		"labeled_break_for.go",
		"labeled_continue_for.go",
		"labeled_break_switch.go",
		"labeled_break_select.go",
		"expr_typeswitch_composite_case.go",
		"goto_forward.go",
		"goto_backward.go",
		"goto/goto_backward_loop.go",
		"goto/goto_out_of_nested.go",
		"goto/goto_over_switch.go",
		"goto/goto_label_at_end.go",
		"goto/goto_labeled_loop.go",
		"expr_indexlist_funcvalue.go",
	} {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("testdata", "sprint151", filepath.FromSlash(file))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Base(file)
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
			result, err := lower.Compile(program.File, lower.Options{Origin: file})
			if err != nil {
				t.Fatalf("lower: %v", err)
			}
			generated := filepath.Join(t.TempDir(), "generated.go")
			if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
				t.Fatal(err)
			}
			loweredOut, err := exec.Command("go", "run", generated).Output()
			if err != nil {
				t.Fatalf("go run lowered program: %v\n%s", err, result.Source)
			}
			if !bytes.Equal(loweredOut, goOut) {
				t.Fatalf("lowered stdout differs from go run\nlowered: %q\ngo run:  %q", loweredOut, goOut)
			}
		})
	}
}
