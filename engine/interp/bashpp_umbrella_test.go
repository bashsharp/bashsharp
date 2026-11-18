// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The Sprint 113 umbrella closure (#126, Story-ID 34489edcbe2e) for the
// interpreter: one script drives every accepted Day-1 dispatch form — var,
// const, type, := (scalar, tuple and call-binding) and the Go-form call —
// through a LangBashPP runner end to end, and the same Class E bytes through
// a classic runner to prove they still run as the ordinary commands bash runs
// today. The per-form tests own the edge cases; these own the story-level
// contract.

func TestBashPPUmbrellaEndToEnd(t *testing.T) {
	t.Parallel()

	src := `var x = 1
const K = 2
type Count int
n := 39
a, b := 7, 8
func add(a, b int) int {
 return $((a + b))
}
z := add($n, 3)
func show(v) {
 echo "v:$v"
}
show($z)
echo "$x $K $n $a $b $z ${Count+set}"
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "v:42\n1 2 39 7 8 42 set\n"))
}

// TestBashPPUmbrellaClassicRuntimeIsolation runs the Class E umbrella subset
// under plain LangBash and LangPOSIX runners and records what actually
// executes: each line must reach the exec handler as the ordinary command it
// is today, argv intact, with no variable coming into existence. `type` is
// excluded here only because classic bash resolves it as a builtin whose
// output depends on the host PATH; its parse-level isolation is covered in
// the syntax package.
func TestBashPPUmbrellaClassicRuntimeIsolation(t *testing.T) {
	t.Parallel()

	src := "var x = 1\nconst K = 2\nn := 39\necho \"vars:${x-unset}:${K-unset}:${n-unset}\"\n"
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		t.Run(lang.String(), func(t *testing.T) {
			t.Parallel()
			f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(src), "")
			qt.Assert(t, qt.IsNil(err))

			var argvs [][]string
			recorder := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					argvs = append(argvs, args)
					return nil
				}
			}
			var out strings.Builder
			r, err := interp.New(interp.Lang(lang),
				interp.StdIO(nil, &out, &out), interp.ExecHandlers(recorder))
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.IsNil(r.Run(context.Background(), f)))

			qt.Assert(t, qt.DeepEquals(argvs, [][]string{
				{"var", "x", "=", "1"},
				{"const", "K", "=", "2"},
				{"n", ":=", "39"},
			}))
			qt.Assert(t, qt.Equals(out.String(), "vars:unset:unset:unset\n"))
		})
	}
}

// A malformed hand-built node gets its typed-expression diagnostic rather than
// falling through to the generic "unhandled command node" path.
func TestBashPPUmbrellaBraceIfMalformedTreeDiagnostic(t *testing.T) {
	t.Parallel()

	f := &syntax.File{Stmts: []*syntax.Stmt{{Cmd: &syntax.BashPPIf{}}}}
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	err := r.Run(context.Background(), f)
	var es interp.ExitStatus
	qt.Assert(t, qt.IsTrue(errors.As(err, &es)))
	qt.Assert(t, qt.Equals(uint8(es), 2))
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EEXPR-FORM"))
}
