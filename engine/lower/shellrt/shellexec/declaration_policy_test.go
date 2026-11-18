package shellexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
	"testing"
)

func TestNativeDeclarationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		constant      bool
	}{
		{"var_unset", "unset 'x'; echo \"$x:$?\"", false},
		{"const_write", "x=42; echo after", true},
		{"const_eval_write", "eval 'x=42; echo after'; echo escaped", true},
		{"const_unset", "unset 'x'; echo \"$x:$?\"", true},
		{"const_compound", "if true; then x=42; echo after; fi; echo escaped", true},
		{"const_subshell", "(x=42; echo after); echo \"parent=$x\"", true},
		{"var_write", "x=010; echo \"$x\"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag, wantOut, wantDiag bytes.Buffer
			p, err := shellrt.NewProgram(shellrt.WithStdio(nil, &out, &diag), shellrt.WithShellFactory(New(BashPP())))
			if err != nil {
				t.Fatal(err)
			}
			x := shellrt.Cell[int](p.Bindings, "local:x", "x", shellrt.KindScalar)
			x.Value = 1
			x.Present = true
			if err := p.Bindings.SetInfo("local:x", shellrt.LexicalInfo{SourceType: "int", Constant: tc.constant, Readonly: tc.constant}); err != nil {
				t.Fatal(err)
			}
			err = p.Run(func(p *shellrt.Program) { p.ShellRegion(tc.command); p.ShellRegion("echo tail") })
			if err != nil {
				t.Fatal(err)
			}
			declaration := "var"
			if tc.constant {
				declaration = "const"
			}
			src := "func main() {\n" + declaration + " x int = 1\n" + tc.command + "\necho tail\n}\nmain()\n"
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron()), interp.StdIO(nil, &wantOut, &wantDiag))
			if err != nil {
				t.Fatal(err)
			}
			status := 0
			if err = r.Run(context.Background(), f); err != nil {
				var code interp.ExitStatus
				if !errors.As(err, &code) {
					t.Fatal(err)
				}
				status = int(code)
			}
			if p.Status() != status || out.String() != wantOut.String() || diag.String() != wantDiag.String() {
				t.Fatalf("native %d %q %q; source %d %q %q", p.Status(), out.String(), diag.String(), status, wantOut.String(), wantDiag.String())
			}
		})
	}
}

func TestDeclarationPolicyViewDoesNotPersist(t *testing.T) {
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX, syntax.LangBashPP} {
		t.Run(lang.String(), func(t *testing.T) {
			var out, diag bytes.Buffer
			r, err := interp.New(interp.Lang(lang), interp.Env(expand.ListEnviron("x=1")), interp.StdIO(nil, &out, &diag))
			if err != nil {
				t.Fatal(err)
			}
			refused := false
			ctx := interp.WithDeclarations(context.Background(), []interp.BashPPDeclaration{{ID: "one", Name: "x", Constant: true}}, func() { refused = true })
			f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader("x=2"), "")
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Run(ctx, f.Stmts[0])
			if refused != (lang == syntax.LangBashPP) {
				t.Fatalf("refused=%v", refused)
			}
			f, err = syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader("x=3; echo $x"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Run(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if out.String() != "3\n" {
				t.Fatalf("policy leaked: %q", out.String())
			}
		})
	}
}

func TestDeclarationPolicyConcurrentPrograms(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			var out, diag bytes.Buffer
			p, err := shellrt.NewProgram(shellrt.WithStdio(nil, &out, &diag), shellrt.WithShellFactory(New(BashPP())))
			if err != nil {
				t.Fatal(err)
			}
			x := shellrt.Cell[int](p.Bindings, "x", "x", shellrt.KindScalar)
			x.Value = 1
			x.Present = true
			constant := i%2 == 0
			if err := p.Bindings.SetInfo("x", shellrt.LexicalInfo{Constant: constant}); err != nil {
				t.Fatal(err)
			}
			err = p.Run(func(p *shellrt.Program) { p.ShellRegion("x=2"); p.ShellRegion("echo $x") })
			if err != nil {
				t.Fatal(err)
			}
			if constant {
				if p.Status() != 1 || out.Len() != 0 || diag.String() != "x: cannot assign to const\n" {
					t.Fatalf("constant %d %q %q", p.Status(), out.String(), diag.String())
				}
			} else if p.Status() != 0 || out.String() != "2\n" || diag.Len() != 0 {
				t.Fatalf("variable %d %q %q", p.Status(), out.String(), diag.String())
			}
		})
	}
}
