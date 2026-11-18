// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPRunner builds a runner in the given dialect, capturing its output.
func bashPPRunner(tb testing.TB, out *strings.Builder, opts ...interp.RunnerOption) *interp.Runner {
	tb.Helper()
	environ := []string{"PATH=/usr/bin:/bin"}
	if runtime.GOOS == "windows" {
		// Native Go subprocesses need the Windows SDK/cache and temp environment.
		for _, key := range []string{"PATH", "SystemRoot", "USERPROFILE", "LOCALAPPDATA", "TEMP", "TMP"} {
			if value, ok := os.LookupEnv(key); ok {
				environ = append(environ, key+"="+value)
			}
		}
	}
	all := append([]interp.RunnerOption{
		interp.StdIO(nil, out, out),
		// A real PATH, so the OS-boundary test can find `env`.
		interp.Env(expand.ListEnviron(environ...)),
	}, opts...)
	r, err := interp.New(all...)
	qt.Assert(tb, qt.IsNil(err))
	return r
}

func bashPPRun(tb testing.TB, r *interp.Runner, src string) {
	tb.Helper()
	bashPPRunReader(tb, r, src, false)
}

// bashPPRunReader parses src either in one piece or one byte at a time, which
// is what proves a speculative parse restored input it had already read.
func bashPPRunReader(tb testing.TB, r *interp.Runner, src string, bytewise bool) {
	tb.Helper()
	var rd io.Reader = strings.NewReader(src)
	if bytewise {
		rd = iotest.OneByteReader(rd)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(rd, "")
	qt.Assert(tb, qt.IsNil(err))
	// A non-zero exit is the script's business; these tests assert on output.
	_ = r.Run(context.Background(), f)
}

func TestBashPPParsedShortDeclarationEvaluates(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, "x := 42; y, z := 1, 2; printf '%s:%s:%s' \"$x\" \"$y\" \"$z\"")
	qt.Assert(t, qt.Equals(out.String(), "42:1:2"))
}

func TestBashPPStory204DeclarationSemantics(t *testing.T) {
	t.Run("typed scalar zero values and typed const", func(t *testing.T) {
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, "var n int\nvar ok bool\nvar text string\nconst K int8 = 7\nprintf '<%s>:<%s>:<%s>:%s' \"$n\" \"$ok\" \"$text\" \"$K\"\n")
		qt.Assert(t, qt.Equals(out.String(), "<0>:<false>:<>:7"))
	})

	t.Run("named typed const retains method identity", func(t *testing.T) {
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, "type Count int\nfunc (c Count) Value() int { return c }\nconst K Count = 7\nn := K.Value()\nprintf '%s' \"$n\"\n")
		qt.Assert(t, qt.Equals(out.String(), "7"))
	})

	t.Run("ordinary tuple may redeclare when another name is new", func(t *testing.T) {
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, "x := 1\nx, y := 2, 3\nprintf '%s:%s' \"$x\" \"$y\"\n")
		qt.Assert(t, qt.Equals(out.String(), "2:3"))
	})

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"no new short declaration name", "x := 1\nx := 2\n", "BASHPP-ESHORT-NONEW"},
		{"duplicate short declaration name", "x, x := 1, 2\n", "x repeated on left side of :="},
		{"constant overflow", "var n int8 = 128\n", "BASHPP-EEXPR-CONVERT: constant 128 overflows int8"},
		{"float32 overflow", "func main() {\n n := 999999999999999999999999999999999999999.0\n var f float32 = n\n}\nmain()\n", "BASHPP-EEXPR-CONVERT:"},
		{"constant kind is not assignable", "var text string = 1\n", "BASHPP-EASSIGN-TYPE"},
		{"complex carrier policy", "var z complex64 = 0\n", "BASHPP-ECOMPLEX-UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.StringContains(out.String(), tc.want))
		})
	}
}

func TestBashPPStory204ShortDeclAtomicAcrossProducers(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			"function results reuse existing and add new",
			"func pair() (int, int) {\n inner := 3\n return 2, inner\n}\nx := 1\nx, y := pair()\nprintf '%s:%s' \"$x\" \"$y\"\n",
			"2:3",
		},
		{
			"function result rollback on readonly target",
			"func pair() (int, int) { return 2, 3 }\nconst x = 1\ny, x := pair()\nprintf '|%s|%s|' \"${y-unset}\" \"$x\"\n",
			"x: cannot assign to constant\n|unset|1|",
		},
		{
			"status-restoring producer cannot erase binding failure",
			"func main() {\n const x = 1\n x := recover()\n printf 'status=%s x=%s' \"$?\" \"$x\"\n}\nmain()\n",
			"x: cannot assign to constant\nstatus=2 x=1",
		},
		{
			"typed reuse retains identity",
			"type Count int\nfunc (c Count) Value() int { return c }\nfunc pair() (Count, int) { return 2, 3 }\nvar x Count = 1\nx, y := pair()\nn := x.Value()\nprintf '%s:%s' \"$n\" \"$y\"\n",
			"2:3",
		},
		{
			"typed reuse mismatch rolls back tuple",
			"type Count int\nfunc pair() (string, int) { return bad, 3 }\nvar x Count = 1\nx, y := pair()\nprintf '|%s|%s|' \"$x\" \"${y-unset}\"\n",
			"BASHPP-EASSIGN-TYPE: cannot assign string to Count\n|1|unset|",
		},
		{
			"builtin no-new leaves old value",
			"x := 9\nx := len(abc)\nprintf '|%s|' \"$x\"\n",
			"BASHPP-ESHORT-NONEW: no new variables on left side of :=\n|9|",
		},
		{
			"composite no-new leaves old value",
			"x := 9\nx := []int{1, 2}\nprintf '|%s|' \"$x\"\n",
			"BASHPP-ESHORT-NONEW: no new variables on left side of :=\n|9|",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}

func TestBashPPStory204TypedConstRequiresConstantExpression(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			"constant may depend on constant",
			"const A int = 2\nconst B int = A\nprintf '%s' \"$B\"\n",
			"2",
		},
		{
			"mutable identifier",
			"var n int = 2\nconst K int = n\n",
			"BASHPP-ECONST-EXPR:",
		},
		{
			"function-derived identifier",
			"func value() int { return 2 }\nn := value()\nconst K int = n\n",
			"BASHPP-ECONST-EXPR:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.StringContains(out.String(), tc.want))
		})
	}
}

func TestBashPPStory204ReusedScalarKindProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			"quoted numeric remains string",
			"var value string = old\nvalue, fresh := \"2\", 1\nprintf '%s:%s' \"$value\" \"$fresh\"\n",
			"2:1",
		},
		{
			"quoted bool remains string",
			"var value string = old\nvalue, fresh := \"true\", 1\nprintf '%s:%s' \"$value\" \"$fresh\"\n",
			"true:1",
		},
		{
			"quoted numeric rejected by integer target",
			"var value int = 7\nvalue, fresh := \"2\", 1\nprintf '|%s|%s|' \"$value\" \"${fresh-unset}\"\n",
			"BASHPP-EASSIGN-TYPE: cannot assign String to int\n|7|unset|",
		},
		{
			"quoted bool rejected by bool target",
			"var value bool = true\nvalue, fresh := \"true\", 1\nprintf '|%s|%s|' \"$value\" \"${fresh-unset}\"\n",
			"BASHPP-EASSIGN-TYPE: cannot assign String to bool\n|true|unset|",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}

func TestBashPPStory204MismatchDiagnosticsFollowLHSOrder(t *testing.T) {
	const src = "func values() (string, string, int) { return bad, alsoBad, 1 }\nvar first int = 7\nvar second bool = true\nfirst, second, fresh := values()\n"
	for range 20 {
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, src)
		qt.Assert(t, qt.Equals(out.String(), "BASHPP-EASSIGN-TYPE: cannot assign string to int\n"))
	}
}

func TestBashPPStory204NestedCalleeFailureRollsBackCaller(t *testing.T) {
	const src = "func broken() (int, int) {\n inner := 1\n inner := 2\n return 8, 9\n}\nx := 7\nx, fresh := broken()\nprintf 'status=%s x=%s fresh=%s' \"$?\" \"$x\" \"${fresh-unset}\"\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "BASHPP-ESHORT-NONEW: no new variables on left side of :=\nstatus=2 x=7 fresh=unset"))
}

func TestBashPPStory204NestedProducerFailuresSurviveResultSettlement(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			"function result arity",
			"func one() int { return 1 }\nfunc broken() (int, int) {\n a, b := one()\n return 8, 9\n}\n",
			"assignment mismatch: 2 variable(s) but 1 value(s)\n",
		},
		{
			"builtin arity",
			"func broken() (int, int) {\n n := len(one, two)\n return 8, 9\n}\n",
			"BASHPP-EBUILTIN-ARITY: len expects exactly 1 argument; got 2 argument(s)\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const caller = "x := 7\nx, fresh := broken()\nprintf 'status=%s x=%s fresh=%s' \"$?\" \"$x\" \"${fresh-unset}\"\n"
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.body+caller)
			qt.Assert(t, qt.Equals(out.String(), tc.want+"status=2 x=7 fresh=unset"))
		})
	}
}

func TestBashPPStory204DiagnosticHasSourcePosition(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(
		strings.NewReader("var good int8 = 1\nvar bad int8 = 128\n"), "decl.bpp")
	qt.Assert(t, qt.IsNil(err))
	_ = r.Run(context.Background(), f)
	qt.Assert(t, qt.StringContains(out.String(), "decl.bpp: line 2: BASHPP-EEXPR-CONVERT"))
}

func TestBashPPParsedScalarExpressionsEvaluate(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, `func main() {
	base := 40
	copy := base
	n := copy + 2
	ok := n == 42
	s := string(65)
	printf '%s:%s:%s' "$n" "$ok" "$s"
}
main()
`)
	qt.Assert(t, qt.Equals(out.String(), "42:true:A"))
}

// TestBashPPScalarShellOperatorExpressionsEvaluate proves each Go binary
// operator spelled with shell metacharacters end to end: from the source bytes,
// through the parser's carrier and typed tree, to the value the interpreter
// binds. The script is fed both in one piece and one byte at a time, because
// the carrier reads ahead transactionally and a rollback must restore unread
// input either way.
func TestBashPPScalarShellOperatorExpressionsEvaluate(t *testing.T) {
	for _, tc := range []struct{ expr, want string }{
		{"true || false", "true"},
		{"true && false", "false"},
		{"1 < 2", "true"},
		{"1 <= 2", "true"},
		{"1 > 2", "false"},
		{"1 >= 2", "false"},
		{"6 | 3", "7"},
		{"6 & 3", "2"},
		{"6 &^ 3", "4"},
		{"1 << 2", "4"},
		{"8 >> 2", "2"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			src := "func main() {\n\tn := " + tc.expr + "\n\tprintf '%s' \"$n\"\n}\nmain()\n"
			for _, bytewise := range []bool{false, true} {
				var out strings.Builder
				r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
				bashPPRunReader(t, r, src, bytewise)
				qt.Assert(t, qt.Equals(out.String(), tc.want))
			}
		})
	}
}

// TestBashPPScalarShellOperatorDiagnostics proves the evaluator's diagnostics
// survive the new reach: an operator that only the carrier can deliver still
// reports the owner's error code rather than a generic failure.
func TestBashPPScalarShellOperatorDiagnostics(t *testing.T) {
	for _, tc := range []struct{ expr, code string }{
		{"true || 1", "BASHPP-EEXPR-OPERAND"},
		{"missing < 1", "BASHPP-EEXPR-UNDEFINED"},
		{`"a" | 1`, "BASHPP-EEXPR-OPERAND"},
		{`1 << "a"`, "BASHPP-EEXPR-SHIFT"},
		{"1 << -1", "BASHPP-EEXPR-SHIFT"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, "func main() {\n\tn := "+tc.expr+"\n}\nmain()\n")
			qt.Assert(t, qt.StringContains(out.String(), tc.code))
		})
	}
}

// Explicit := starts own the typed scalar RHS at mixed top level as well as
// inside Go functions. The command escape still selects shell redirection.
func TestBashPPScalarExplicitStartsAndShellEscape(t *testing.T) {
	const tail = "z := x < y\nprintf '[%s]' \"$z\"\n"
	for _, tc := range []struct {
		name, src, want string
	}{
		{"top-level", "x := 1\ny := 2\n" + tail, "[true]"},
		{"function", "func main() {\n\tx := 1\n\ty := 2\n\t" +
			strings.ReplaceAll(tail, "\n", "\n\t") + "}\nmain()\n", "[true]"},
		{"shell-escape", "x := 1\ny := 2\ncommand " + tail, "open y: no such file or directory\n[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, bytewise := range []bool{false, true} {
				var out strings.Builder
				r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()))
				bashPPRunReader(t, r, tc.src, bytewise)
				qt.Assert(t, qt.Equals(out.String(), tc.want))
			}
		})
	}
}

// A reserved := start parses Go literals; ordinary shell assignments and
// escaped commands retain shell quoting and literal argument semantics.
func TestBashPPTopLevelRuneBindingAndShellQuotes(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"typed-rune", `x := 'a'; printf '%s' "$x"`, "97"},
		{"shell-assignment", `x='a'; printf '%s' "$x"`, "a"},
		{"shell-escape", `command printf '%s|%s' := 'a'`, ":=|a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, bytewise := range []bool{false, true} {
				var out strings.Builder
				r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
				bashPPRunReader(t, r, tc.src, bytewise)
				qt.Assert(t, qt.Equals(out.String(), tc.want))
			}
		})
	}
}

// The RHS identifier at an explicit := start reads its typed value. It is
// still literal text in a shell assignment or an escaped command argument.
func TestBashPPTopLevelIdentifierBindingAndShellLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"typed-identifier", `base := 40; copy := base; printf '%s' "$copy"`, "40"},
		{"shell-assignment", `base := 40; copy=base; printf '%s' "$copy"`, "base"},
		{"shell-escape", `base := 40; command printf '%s|%s' := base`, ":=|base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, bytewise := range []bool{false, true} {
				var out strings.Builder
				r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
				bashPPRunReader(t, r, tc.src, bytewise)
				qt.Assert(t, qt.Equals(out.String(), tc.want))
			}
		})
	}
}

func TestBashPPTypedScalarTreeIsDialectGatedAtRuntime(t *testing.T) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("x := string(65)\n"), "")
	qt.Assert(t, qt.IsNil(err))
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		t.Run(lang.String(), func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(lang))
			err := r.Run(context.Background(), f)
			qt.Check(t, qt.ErrorMatches(err, "exit status 2"))
			qt.Check(t, qt.Equals(out.String(), "bash++ short declaration evaluated with extensions disabled\n"))
		})
	}
}

func TestBashPPParsedScalarConstantsUseGoConstantPrecision(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, `func main() {
	n := 9223372036854775808 + 1
	printf '%s' "$n"
}
main()
`)
	qt.Assert(t, qt.Equals(out.String(), "9223372036854775809"))
}

func TestBashPPParsedScalarExpressionDiagnostics(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, `func main() {
	n := missing + 1
	printf '%s' "$n"
}
main()
`)
	qt.Assert(t, qt.Equals(out.String(), "BASHPP-EEXPR-UNDEFINED: undefined: missing\n"))
}

func TestBashPPParsedScalarInvalidOperationsNeverPanic(t *testing.T) {
	for _, tc := range []struct {
		name string
		expr string
		code string
	}{
		{"string subtraction", `"a" - "b"`, "BASHPP-EEXPR-OPERAND"},
		{"narrow conversion overflow", `int8(300)`, "BASHPP-EEXPR-CONVERT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, "func main() {\n\tn := "+tc.expr+"\n}\nmain()\n")
			qt.Assert(t, qt.StringContains(out.String(), tc.code))
		})
	}
}

// TestBashPPDialectGate proves that LangBashPP gates the feature: the very same
// call succeeds under bash++ and is refused under every other dialect, and under
// POSIX mode.
func TestBashPPDialectGate(t *testing.T) {
	t.Parallel()

	t.Run("open under bashpp", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBashPP))
		qt.Assert(t, qt.IsNil(r.SetObject("OBJ", map[string]any{"a": 1})))
	})

	t.Run("closed under other dialects", func(t *testing.T) {
		t.Parallel()
		for _, lang := range []syntax.LangVariant{
			syntax.LangBash, syntax.LangPOSIX, syntax.LangMirBSDKorn,
			syntax.LangBats, syntax.LangZsh,
		} {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(lang))
			err := r.SetObject("OBJ", map[string]any{"a": 1})
			qt.Check(t, qt.ErrorIs(err, interp.ErrObjectsUnsupported), qt.Commentf("lang %v", lang))
		}
	})

	t.Run("closed by default", func(t *testing.T) {
		t.Parallel()
		// A runner which never mentions a dialect — i.e. every consumer that
		// exists today — is plain bash, and objects are unavailable.
		var out strings.Builder
		r := bashPPRunner(t, &out)
		qt.Assert(t, qt.Equals(r.Dialect(), syntax.LangBash))
		qt.Assert(t, qt.ErrorIs(r.SetObject("OBJ", 1), interp.ErrObjectsUnsupported))
	})

	t.Run("closed under posix mode", func(t *testing.T) {
		t.Parallel()
		// --posix off: POSIX mode is the shell promising to be nothing but a
		// POSIX shell, so the bash++ extensions are withdrawn even in the
		// bash++ dialect.
		var out strings.Builder
		r := bashPPRunner(t, &out,
			interp.Lang(syntax.LangBashPP), interp.WithPosixMode(true))
		err := r.SetObject("OBJ", map[string]any{"a": 1})
		qt.Assert(t, qt.ErrorIs(err, interp.ErrObjectsUnsupported))
	})

	t.Run("rejects unusable values", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
		qt.Assert(t, qt.IsNotNil(r.SetObject("OBJ", make(chan int))))
		qt.Assert(t, qt.IsNotNil(r.SetObject("not a name", 1)))
	})

	t.Run("unknown variant is an error", func(t *testing.T) {
		t.Parallel()
		_, err := interp.New(interp.Lang(syntax.LangAuto))
		qt.Assert(t, qt.IsNotNil(err))
	})
}

// TestBashPPObjectRoundTrip proves an object survives a trip through the shell:
// the same Go value comes back out, while the shell itself only ever sees a string.
func TestBashPPObjectRoundTrip(t *testing.T) {
	t.Parallel()

	type config struct {
		Name string   `json:"name"`
		Port int      `json:"port"`
		Tags []string `json:"tags"`
	}
	orig := config{Name: "web", Port: 8080, Tags: []string{"a", "b"}}

	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("CFG", orig)))

	// The Go value comes back as itself, not as a reparsed string.
	got, ok := r.Object("CFG")
	qt.Assert(t, qt.IsTrue(ok))
	gotCfg, ok := got.(config)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.DeepEquals(gotCfg, orig))

	// It survives a Run, including one which reads the variable as a string.
	bashPPRun(t, r, `echo "$CFG"`)
	qt.Assert(t, qt.Equals(out.String(), `{"name":"web","port":8080,"tags":["a","b"]}`+"\n"))

	got, ok = r.Object("CFG")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.DeepEquals(got.(config), orig))

	// A plain string variable, and an unset name, are not objects.
	bashPPRun(t, r, `PLAIN=hello`)
	_, ok = r.Object("PLAIN")
	qt.Assert(t, qt.IsFalse(ok))
	_, ok = r.Object("NOPE")
	qt.Assert(t, qt.IsFalse(ok))
}

func TestBashPPSetObjectMutationFailsClosedAtCoercion(t *testing.T) {
	t.Parallel()

	type holder struct {
		Value any `json:"value"`
	}
	orig := &holder{Value: []int{1, 2}}

	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", orig)))

	got, ok := r.Object("OBJ")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(got, any(orig)))

	orig.Value = orig
	bashPPRun(t, r, `echo "$OBJ"`)
	qt.Assert(t, qt.Equals(out.String(), expand.ObjectString(make(chan int))+"\n"))
}

// TestBashPPObjectCoercion covers the "coerce to string for bash commands" half:
// wherever the shell needs a string, the object is one.
func TestBashPPObjectCoercion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{"interpolation", `echo "$OBJ"`, `{"a":1,"b":"two"}` + "\n"},
		{"unquoted", `echo $OBJ`, `{"a":1,"b":"two"}` + "\n"},
		{"builtin arg", `printf '%s\n' "$OBJ"`, `{"a":1,"b":"two"}` + "\n"},
		// The length of the coerced string, i.e. of `{"a":1,"b":"two"}`.
		{"length", `echo ${#OBJ}`, "17\n"},
		{"comparison", `[[ $OBJ == \{* ]] && echo yes`, "yes\n"},
		{"declare -p", `declare -p OBJ`, `declare -- OBJ="{\"a\":1,\"b\":\"two\"}"` + "\n"},
		{"in a function", `f() { echo "$OBJ"; }; f`, `{"a":1,"b":"two"}` + "\n"},
		{"in a subshell", `( echo "$OBJ" )`, `{"a":1,"b":"two"}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			// A fixed key set, so the JSON encoding is deterministic.
			qt.Assert(t, qt.IsNil(r.SetObject("OBJ", map[string]any{"a": 1, "b": "two"})))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}

// TestBashPPObjectCrossesOSBoundary is the auto-JSON gate: an object handed to
// an external binary — a separate process, which cannot be given a Go value —
// arrives as JSON.
func TestBashPPObjectCrossesOSBoundary(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("env"); err != nil {
		t.Skipf("need the env binary: %v", err)
	}

	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", map[string]any{"a": 1, "b": []int{2, 3}})))

	// `env` is a real external process: whatever it prints, the kernel handed it.
	bashPPRun(t, r, `export OBJ; env`)
	qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), `OBJ={"a":1,"b":[2,3]}`)),
		qt.Commentf("env output:\n%s", out.String()))

	// An object which is not exported does not cross, exactly like any other
	// unexported shell variable.
	var out2 strings.Builder
	r2 := bashPPRunner(t, &out2, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r2.SetObject("SECRET", map[string]any{"k": "v"})))
	bashPPRun(t, r2, `env`)
	qt.Assert(t, qt.IsFalse(strings.Contains(out2.String(), "SECRET=")))
}

func TestBashPPLargeObjectCrossesOSBoundaryIntact(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("wc"); err != nil {
		t.Skipf("need the wc binary: %v", err)
	}

	payload := strings.Repeat("x", 200_000)
	wantBytes := len(expand.ObjectString(payload))
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(r.SetObject("OBJ", payload)))

	// printf is the shell builtin, while wc is a real external process. Its
	// count proves the complete JSON string crossed the process boundary.
	bashPPRun(t, r, `printf %s "$OBJ" | wc -c`)
	qt.Assert(t, qt.Equals(strings.TrimSpace(out.String()), fmt.Sprint(wantBytes)))
}

// TestBashPPLangBashUnaffected is the no-regression gate: for any script which
// uses no objects, bash++ and bash behave identically, so opting in costs the
// existing semantics nothing.
func TestBashPPLangBashUnaffected(t *testing.T) {
	t.Parallel()

	scripts := []string{
		`x=1; echo $x`,
		`a=(one two); echo ${a[1]} ${#a[@]}`,
		`declare -A m=([k]=v); echo ${m[k]}`,
		`f() { echo "$1"; }; f hi`,
		`echo ${UNSET:-fallback}`,
		`x=abc; echo ${x^^} ${x:1:2}`,
		`declare -i n=2+3; echo $n`,
		`export E=1; declare -p E`,
	}
	for _, src := range scripts {
		var bashOut, ppOut strings.Builder

		rBash := bashPPRunner(t, &bashOut, interp.Lang(syntax.LangBash))
		bashPPRun(t, rBash, src)

		rPP := bashPPRunner(t, &ppOut, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, rPP, src)

		qt.Check(t, qt.Equals(ppOut.String(), bashOut.String()),
			qt.Commentf("script %q diverged between bash and bashpp", src))
	}
}

// TestBashPPErrObjectsUnsupported keeps the sentinel error usable by callers.
func TestBashPPErrObjectsUnsupported(t *testing.T) {
	t.Parallel()
	qt.Assert(t, qt.IsTrue(errors.Is(interp.ErrObjectsUnsupported, interp.ErrObjectsUnsupported)))
}
