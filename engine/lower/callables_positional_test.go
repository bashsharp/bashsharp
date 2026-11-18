//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"testing"
)

func TestCompiledPositionalDispatch(t *testing.T) {
	for name, source := range map[string]string{
		"typed": `func show(value string) { printf 'typed:<%s>\n' "$value" }
show("$1")
printf 'count=%s\n' "$#"
printf 'item:<%s>\n' "$@"
printf 'default:<%s>\n' "${9-missing}"
`,
		"native_shell": `var kept int = 1
show() { printf 'count=%s:%s\n' "$#" "$kept"; printf 'item:<%s>\n' "$@"; }
show "$@"
show "" "two words"
printf 'restored:%s:<%s>\n' "$#" "$1"
`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, args := range [][]string{nil, {"", "two words", "line\nbreak", "-n", "$literal"}} {
				file := parse(t, source, "input.bpp")
				result, err := lower.Compile(file, lower.Options{})
				if err != nil {
					t.Fatal(err)
				}
				out, diagnostic, status := runArtifact(t, result, args...)
				var expected, expectedDiagnostic bytes.Buffer
				params := append([]string{"--"}, args...)
				runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Params(params...), interp.StdIO(nil, &expected, &expectedDiagnostic), interp.Env(expand.ListEnviron("PATH=/no-tools")))
				if err != nil {
					t.Fatal(err)
				}
				expectedStatus := 0
				if err := runner.Run(context.Background(), file); err != nil {
					var code interp.ExitStatus
					if !errors.As(err, &code) {
						t.Fatal(err)
					}
					expectedStatus = int(code)
				}
				if out != expected.String() || diagnostic != expectedDiagnostic.String() || status != expectedStatus {
					t.Fatalf("args=%q compiled=(%q,%q,%d) source=(%q,%q,%d)", args, out, diagnostic, status, expected.String(), expectedDiagnostic.String(), expectedStatus)
				}
			}
		})
	}
}
