//go:build full

package interp_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPStdlibImportSelectorCalls(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron(os.Environ()...)))
	run := func(src string) {
		t.Helper()
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "source.bpp")
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(src, "Println(") {
			if _, ok := f.Stmts[0].Cmd.(*syntax.BashPPCall); !ok {
				t.Fatalf("dot call parsed as %T", f.Stmts[0].Cmd)
			}
		}
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	run("import \"fmt\"\n")
	run("fmt.Println(\"incremental\")\n")
	r.Reset()
	run("import (\n\tf \"fmt\"\n\t_ \"embed\"\n)\n")
	run("f.Println(\"alias\")\n")
	r.Reset()
	run("import . \"fmt\"\n")
	run("Println(\"dot\")\n")
	if got := out.String(); got != "incremental\nalias\ndot\n" {
		t.Fatalf("output %q", got)
	}
}

func TestBashPPLocalMultipartSelectorWinsOverImport(t *testing.T) {
	const src = `import "fmt"
fmt.Println("package")
type Speaker interface { Speak() }
type Voice int
func (v Voice) Speak() { printf local }
type Holder struct { Speaker }
func main() {
 var voice Voice = 0
 var speaker Speaker = voice
 fmt := Holder{Speaker: speaker}
 fmt.Speaker.Speak()
}
main()
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "selector-import.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron(os.Environ()...)))
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "package\nlocal"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestBashPPForcedShellImportEndToEnd(t *testing.T) {
	for _, src := range []string{`command import "fmt"`, `"import" "fmt"`} {
		t.Run(src, func(t *testing.T) {
			var argv []string
			handleImport := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] == "import" {
						argv = append([]string(nil), args...)
						fmt.Fprintln(interp.HandlerCtx(ctx).Stdout, "shell-import")
						return nil
					}
					return next(ctx, args)
				}
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "forced.bpp")
			if err != nil {
				t.Fatal(err)
			}
			if _, claimed := f.Stmts[0].Cmd.(*syntax.BashPPImport); claimed {
				t.Fatal("forced shell form was claimed as a Bash++ import")
			}
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.ExecHandlers(handleImport))
			if err := r.Run(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if strings.Join(argv, "|") != "import|fmt" || out.String() != "shell-import\n" {
				t.Fatalf("argv=%q output=%q", argv, out.String())
			}
		})
	}
}

func TestBashPPImportCancellation(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.Env(expand.ListEnviron(os.Environ()...)))
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("import \"fmt\"\nfmt.Println(\"no\")\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Run(ctx, f); err == nil {
		t.Fatal("expected cancellation")
	}
}
