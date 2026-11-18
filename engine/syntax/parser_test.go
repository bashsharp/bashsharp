// Copyright (c) 2016, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package syntax

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/go-quicktest/qt"
	"github.com/google/go-cmp/cmp"
)

func TestParseFiles(t *testing.T) {
	t.Parallel()
	for lang := range langResolvedVariants.bits() {
		t.Run(lang.String(), func(t *testing.T) {
			p := NewParser(Variant(lang))
			for i, c := range append(fileTests, fileTestsNoPrint...) {
				want := c.byLangIndex[lang.index()]
				switch want := want.(type) {
				case nil:
					continue
				case *File:
					for j, in := range c.inputs {
						t.Run(fmt.Sprintf("OK/%03d-%d", i, j), singleParse(p, in, want))
					}
				case string:
					want = strings.Replace(want, "LANG", p.lang.String(), 1)
					for j, in := range c.inputs {
						t.Run(fmt.Sprintf("Err/%03d-%d", i, j), func(t *testing.T) {
							t.Logf("input: %s", in)
							_, err := p.Parse(newStrictReader(in), "")
							if err == nil {
								t.Fatalf("Expected error: %v", want)
							}
							if got := err.Error(); got != want {
								t.Fatalf("Error mismatch\nwant: %s\ngot:  %s",
									want, got)
							}
						})
					}
				}
			}
		})
	}
}

func TestParseDblParenCommandSubst(t *testing.T) {
	t.Parallel()
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		p := NewParser(Variant(lang))
		for _, src := range []string{
			`echo $((echo a);(echo b))`,
			`echo $(( echo ab cde ) )`,
		} {
			f, err := p.Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			call := f.Stmts[0].Cmd.(*CallExpr)
			cs, ok := call.Args[1].Parts[0].(*CmdSubst)
			if !ok {
				t.Fatalf("%s %q: wanted command substitution, got %T", lang, src, call.Args[1].Parts[0])
			}
			if len(cs.Stmts) == 0 {
				t.Fatalf("%s %q: wanted statements in command substitution", lang, src)
			}
		}
	}
}

func TestParseErr(t *testing.T) {
	t.Parallel()
	for lang := range langResolvedVariants.bits() {
		t.Run(lang.String(), func(t *testing.T) {
			p := NewParser(Variant(lang), KeepComments(true))
			for _, c := range errorCases {
				want := c.byLangIndex[lang.index()]
				if want == "" {
					continue
				}
				t.Run("", func(t *testing.T) { // number them #001, #002, ...
					want = strings.Replace(want, "LANG", p.lang.String(), 1)
					t.Logf("input: %s", c.in)
					_, err := p.Parse(newStrictReader(c.in), "")
					if err == nil {
						t.Fatalf("Expected error: %v", want)
					}
					if got := err.Error(); got != want {
						t.Fatalf("Error mismatch\nwant: %s\ngot:  %s",
							want, got)
					}
				})
			}
		})
	}
}

// TestParseByteTransparentBytes locks in bash 5.3's byte transparency: an
// invalid or incomplete multibyte byte is never a parse error, it is advanced
// as a single opaque byte (lib/sh/shmbchar.c: MB_INVALIDCH -> clen=1). These
// inputs used to be strict "invalid UTF-8 encoding" errors in errorCases.
func TestParseByteTransparentBytes(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"echo \x80",
		"\necho \x80",
		"echo foo\x80bar",
		"echo foo\xc3",
		"#foo\xc3",
		"echo a\x80",
	}
	for lang := range langResolvedVariants.bits() {
		p := NewParser(Variant(lang), KeepComments(true))
		for _, in := range inputs {
			// Use the strict reader to also exercise the byte-at-a-time
			// fill path through the lexer.
			f, err := p.Parse(newStrictReader(in), "")
			qt.Assert(t, qt.IsNil(err), qt.Commentf("lang=%s in=%q", lang, in))
			// The stray byte must survive verbatim in the printed output
			// (modulo the printer dropping leading/trailing blank lines).
			var sb strings.Builder
			qt.Assert(t, qt.IsNil(NewPrinter().Print(&sb, f)), qt.Commentf("in=%q", in))
			qt.Assert(t, qt.Equals(strings.Trim(sb.String(), "\n"), strings.Trim(in, "\n")),
				qt.Commentf("lang=%s in=%q", lang, in))
		}
	}
}

// An empty here-document delimiter is a syntax error, not a warning.
// Bash 5.3 reports "syntax error near unexpected token `newline'" and
// exits 2 for `cat <<` regardless of what follows; the EOF warning is
// only for a real delimiter whose body runs off the end of the input.
func TestParseHeredocEOFWarningEmptyDelimiter(t *testing.T) {
	var warnings []string
	p := NewParser(HeredocEOFWarning(func(startLine, eofLine int, stop string) {
		warnings = append(warnings, fmt.Sprintf("%d:%d:%q", startLine, eofLine, stop))
	}))
	_, err := p.Parse(strings.NewReader("cat <<\nhi\nthere"), "")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(err.(ParseError).Text, "`<<` must be followed by a word"))
	qt.Assert(t, qt.HasLen(warnings, 0))
}

// Covers the warning numbers bash 5.3 prints for here-documents
// delimited by end-of-file: the second number ("here-document at line
// N") is the line whose newline triggered gathering the body — not
// necessarily the `<<` opener's line — and the first ("line N:
// warning") is the last line with body content before EOF.
func TestParseHeredocEOFWarningLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  string
		want []string
	}{
		// Quoted empty delimiter; body runs to EOF after a trailing
		// newline: the warning points at the last content line.
		{"cat <<''\nhi\nthere\n''\n", []string{`181:184:""`}},
		// An unquoted delimiter heredoc whose gathering is delayed by
		// a command substitution spanning several lines: both numbers
		// use the line where gathering finally happened (bash numbers
		// the comsub's inner lines one behind, hence the close paren
		// ends "line 184" here).
		{"cat <<EOF && grep $(\n foobar\nEOF\necho nothere) *.c\n", []string{`184:184:"EOF"`}},
	}
	for _, tc := range tests {
		var warnings []string
		p := NewParser(HeredocEOFWarning(func(startLine, eofLine int, stop string) {
			warnings = append(warnings, fmt.Sprintf("%d:%d:%q", startLine, eofLine, stop))
		}))
		// Pad the snippet so it starts at line 181, mirroring the
		// bash 5.3 heredoc.tests / heredoc7.sub fixtures these were
		// distilled from.
		src := strings.Repeat("\n", 180) + tc.src
		_, err := p.Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err), qt.Commentf("src=%q", tc.src))
		qt.Assert(t, qt.DeepEquals(warnings, tc.want), qt.Commentf("src=%q", tc.src))
	}
}

// A here-document inside a plain `( ... )` subshell that runs to
// end-of-file is a hard parse error in bash 5.3 (unlike inside a
// command substitution, where a body line like `EOF)` stands in for
// the missing terminator): the body swallows the input, so the
// subshell can never be closed. The error carries bash's wording and
// is anchored at the EOF line, after the usual heredoc warning.
func TestParseHeredocSubshellEOF(t *testing.T) {
	t.Parallel()
	var warnings []string
	p := NewParser(HeredocEOFWarning(func(startLine, eofLine int, stop string) {
		warnings = append(warnings, fmt.Sprintf("%d:%d:%q", startLine, eofLine, stop))
	}))
	_, err := p.Parse(strings.NewReader("(cat <<EOF\nbody text\nEOF)\n"), "")
	qt.Assert(t, qt.DeepEquals(warnings, []string{`1:3:"EOF"`}))
	perr, ok := err.(ParseError)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(perr.Text, "syntax error: unexpected end of file from `(' command on line 1"))
	qt.Assert(t, qt.Equals(perr.Pos.Line(), 4))

	// Inside a command substitution the close-paren recovery still
	// applies and parsing succeeds with only the warning.
	warnings = nil
	f, err := p.Parse(strings.NewReader("x=$(cat <<EOF\nbody text\nEOF)\n"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(warnings, []string{`1:3:"EOF"`}))
	qt.Assert(t, qt.Equals(len(f.Stmts), 1))
}

// A backquote in an unquoted here-document delimiter is not quoting:
// the delimiter is the raw word text and the body still expands, like
// bash 5.3 (heredoc4.sub).
func TestParseHeredocBackquoteDelim(t *testing.T) {
	t.Parallel()
	f, err := NewParser().Parse(strings.NewReader("cat <<EO`true`F\nheredoc1\nEO`false`F\nEO`true`F\necho Ok:$?\n"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(len(f.Stmts), 2))
	hdoc := f.Stmts[0].Redirs[0].Hdoc
	// The body must contain an expandable command substitution for
	// `false`, not the literal text "EO`false`F".
	hasComsub := false
	for _, part := range hdoc.Parts {
		if _, ok := part.(*CmdSubst); ok {
			hasComsub = true
		}
	}
	qt.Assert(t, qt.IsTrue(hasComsub), qt.Commentf("hdoc parts: %#v", hdoc.Parts))
}

func TestParseHeredocQuotedDollarAtDelimiter(t *testing.T) {
	t.Parallel()

	var gotStart, gotEOF int
	var gotStop string
	p := NewParser(HeredocEOFWarning(func(startLine, eofLine int, stop string) {
		gotStart, gotEOF, gotStop = startLine, eofLine, stop
	}))
	src := "fun() {\n  cat << \"$@\"\nhi\n1\n}\nfun\n"
	_, err := p.Parse(strings.NewReader(src), "./s")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(gotStart, 2))
	qt.Assert(t, qt.Equals(gotEOF, 6))
	qt.Assert(t, qt.Equals(gotStop, "$@"))
}

// Closing a `$(...)` while here-documents opened inside it are still
// pending triggers bash 5.3's "command substitution: N unterminated
// here-document" warning; their bodies are then read from the lines
// following the substitution (heredoc7.sub).
func TestParseHeredocComsubWarning(t *testing.T) {
	t.Parallel()
	var warnings []string
	p := NewParser(
		HeredocEOFWarning(func(startLine, eofLine int, stop string) {}),
		HeredocComsubWarning(func(line, count int) {
			warnings = append(warnings, fmt.Sprintf("%d:%d", line, count))
		}),
	)
	f, err := p.Parse(strings.NewReader("echo $(cat << EOF)\nfoo\nbar\nEOF\nafter\n"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(warnings, []string{"1:1"}))
	qt.Assert(t, qt.Equals(len(f.Stmts), 2))
	cs := f.Stmts[0].Cmd.(*CallExpr).Args[1].Parts[0].(*CmdSubst)
	qt.Assert(t, qt.Equals(cs.Stmts[0].Redirs[0].Hdoc.Lit(), "foo\nbar\n"))
}

// When a here-document is pending at a `$(`, bash 5.3 numbers the
// substitution's inner lines one behind the input (heredoc7.sub:
// `foobar` sits physically on line 2 but bash reports line 1). The
// shift only applies in HeredocEOFWarning mode.
func TestParseHeredocComsubLineLag(t *testing.T) {
	t.Parallel()
	src := "cat <<EOF && grep $(\n foobar\necho nothere) *.c\nbody\nEOF\n"
	parse := func(p *Parser) (foobarLine, echoLine uint) {
		f, err := p.Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err))
		call := f.Stmts[0].Cmd.(*BinaryCmd).Y.Cmd.(*CallExpr)
		cs := call.Args[1].Parts[0].(*CmdSubst)
		return cs.Stmts[0].Pos().Line(), cs.Stmts[1].Pos().Line()
	}
	foobar, echo := parse(NewParser(HeredocEOFWarning(func(int, int, string) {})))
	qt.Assert(t, qt.Equals(foobar, 1))
	qt.Assert(t, qt.Equals(echo, 2))
	// Without the bash-compat option, positions stay physical.
	foobar, echo = parse(NewParser())
	qt.Assert(t, qt.Equals(foobar, 2))
	qt.Assert(t, qt.Equals(echo, 3))
}

func TestParseConfirm(t *testing.T) {
	if testing.Short() {
		t.Skip("calling external shells is slow")
	}
	for lang := range langResolvedVariants.bits() {
		t.Run(lang.String(), func(t *testing.T) {
			external, ok := externalShells[lang]
			if !ok {
				t.Skip("no external shell to check against")
			}
			if external.require != nil {
				external.require(t)
			}
			for i, c := range append(fileTests, fileTestsNoPrint...) {
				want := c.byLangIndex[lang.index()]
				switch want.(type) {
				case nil:
					continue
				case *File:
					for j, in := range c.inputs {
						wantErr := lang.in(c.flipConfirmSet)
						t.Run(fmt.Sprintf("OK/%03d-%d", i, j), confirmParse(in, external.cmd, wantErr))
					}
				case string:
					for j, in := range c.inputs {
						wantErr := !lang.in(c.flipConfirmSet)
						t.Run(fmt.Sprintf("Err/%03d-%d", i, j), confirmParse(in, external.cmd, wantErr))
					}
				}
			}
			if lang == LangZsh {
				return // TODO: we don't confirm errors with zsh yet
			}
			for i, c := range errorCases {
				want := c.byLangIndex[lang.index()]
				if want == "" {
					continue
				}
				wantErr := !lang.in(c.flipConfirmSet)
				t.Run(fmt.Sprintf("ErrOld/%03d", i), confirmParse(c.in, external.cmd, wantErr))
			}
		})
	}
}

func TestParseBashKeepComments(t *testing.T) {
	t.Parallel()
	p := NewParser(KeepComments(true))
	for i, c := range fileTestsKeepComments {
		want, _ := c.byLangIndex[LangBash.index()].(*File)
		if want == nil {
			continue
		}
		for j, in := range c.inputs {
			t.Run(fmt.Sprintf("%03d-%d", i, j), singleParse(p, in, want))
		}
	}
}

func TestParsePosOverflow(t *testing.T) {
	t.Parallel()

	// Consider using a custom reader to save memory.
	tests := []struct {
		name, in, want string
	}{
		{
			"LineOverflowIsValid",
			strings.Repeat("\n", lineMax) + "foo; bar",
			"<nil>",
		},
		{
			"LineOverflowPosString",
			strings.Repeat("\n", lineMax) + ")",
			"?:1: `)` can only be used to close a subshell",
		},
		{
			"LineOverflowExtraPosString",
			strings.Repeat("\n", lineMax+5) + ")",
			"?:1: `)` can only be used to close a subshell",
		},
		{
			"ColOverflowPosString",
			strings.Repeat(" ", colMax) + ")",
			"1:?: `)` can only be used to close a subshell",
		},
		{
			"ColOverflowExtraPosString",
			strings.Repeat(" ", colMax) + ")",
			"1:?: `)` can only be used to close a subshell",
		},
		{
			"ColOverflowSkippedPosString",
			strings.Repeat(" ", colMax+5) + "\n)",
			"2:1: `)` can only be used to close a subshell",
		},
		{
			"LargestLineNumber",
			strings.Repeat("\n", lineMax-1) + ")",
			fmt.Sprintf("%d:1: `)` can only be used to close a subshell", lineMax),
		},
		{
			"LargestColNumber",
			strings.Repeat(" ", colMax-1) + ")",
			"1:16383: `)` can only be used to close a subshell",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			p := NewParser()
			_, err := p.Parse(strings.NewReader(test.in), "")
			got := fmt.Sprint(err)
			if got != test.want {
				t.Fatalf("want error %q, got %q", test.want, got)
			}
		})
	}
}

func TestMain(m *testing.M) {
	internal.TestMainSetup()
	m.Run()
}

var (
	// onceGNUBash is the first bash on PATH that is GNU bash 5.3. bashy
	// installs itself as bash, and confirming our parser against it would
	// only compare the parser with itself.
	onceGNUBash = sync.OnceValue(func() string {
		for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
			for _, name := range []string{"bash", "bash.exe"} {
				path := filepath.Join(dir, name)
				if cmdContains("version 5.3", path, "--version") && !cmdContains("bashy", path, "--version") {
					return path
				}
			}
		}
		return ""
	})
	onceHasBash53 = sync.OnceValue(func() bool { return onceGNUBash() != "" })

	onceHasDash059 = sync.OnceValue(func() bool {
		// dash provides no way to check its version, so we have to
		// check if it's new enough as to not have the bug that breaks
		// our integration tests.
		// This also means our check does not require a specific version.
		//
		// We get odd failures on Windows on CI, and it's hard to debug
		// or even understand what version of dash it's using; skip on those.
		return cmdContains("Bad subst", "dash", "-c", "echo ${#<}") &&
			runtime.GOOS != "windows"
	})

	onceHasMksh59 = sync.OnceValue(func() bool {
		return cmdContains(" R59 ", "mksh", "-c", "echo $KSH_VERSION")
	})

	onceHasZsh59 = sync.OnceValue(func() bool {
		return cmdContains("zsh 5.9", "zsh", "--version")
	})
)

type externalShell struct {
	cmd     string
	require func(testing.TB)
}

// requireShells can be set to make sure that no external shell tests
// are being skipped due to a misalignment in installed versions.
var requireShells = os.Getenv("REQUIRE_SHELLS") == "1"

func skipExternal(tb testing.TB, message string) {
	if requireShells {
		tb.Fatal(message)
	} else {
		tb.Skip(message)
	}
}

// Note that externalShells is a map, and not an array,
// because [LangVariant.index] is not a constant expression.
// This seems fine; this table is only for the sake of testing.
var externalShells = map[LangVariant]externalShell{
	LangBash: {"bash", func(tb testing.TB) {
		if !onceHasBash53() {
			skipExternal(tb, "bash 5.2 required to run")
		}
	}},
	LangPOSIX: {"dash", func(tb testing.TB) {
		if !onceHasDash059() {
			skipExternal(tb, "dash 0.5.9+ required to run")
		}
	}},
	LangMirBSDKorn: {"mksh", func(tb testing.TB) {
		if !onceHasMksh59() {
			skipExternal(tb, "mksh 59 required to run")
		}
	}},
	LangZsh: {"zsh", func(tb testing.TB) {
		if !onceHasZsh59() {
			skipExternal(tb, "zsh 5.9 required to run")
		}
	}},
}

func cmdContains(substr, cmd string, args ...string) bool {
	out, err := exec.Command(cmd, args...).CombinedOutput()
	got := string(out)
	if err != nil {
		got += "\n" + err.Error()
	}
	return strings.Contains(got, substr)
}

var extGlobRe = regexp.MustCompile(`[@?*+!]\(`)

func confirmParse(in, cmd string, wantErr bool) func(*testing.T) {
	return func(t *testing.T) {
		t.Helper()
		t.Parallel()
		t.Logf("input: %s", in)
		var opts []string
		if strings.Contains(in, "\\\r\n") {
			t.Skip("shells do not generally support CRLF line endings")
		}
		if cmd == "bash" && extGlobRe.MatchString(in) {
			// otherwise bash refuses to parse these
			// properly. Also avoid -n since that too makes
			// bash bail.
			in = "shopt -s extglob\n" + in
		} else if !wantErr {
			// -n makes bash accept invalid inputs like
			// "let" or "`{`", so only use it in
			// non-erroring tests. Should be safe to not use
			// -n anyway since these are supposed to just fail.
			// also, -n will break if we are using extglob
			// as extglob is not actually applied.
			opts = append(opts, "-n")
		}

		// All the bits of shell we test should either finish or fail very quickly,
		// given that they are very small. If we make a mistake with an endless loop,
		// or we somehow trigger a bug that makes a shell hang, kill it.
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		if cmd == "bash" {
			cmd = onceGNUBash()
		}
		cmd := exec.CommandContext(ctx, cmd, opts...)
		killCommandOnTestExit(cmd)
		cmd.Dir = t.TempDir() // to be safe
		cmd.Stdin = strings.NewReader(in)
		var stderrBuf strings.Builder
		cmd.Stderr = &stderrBuf
		err := cmd.Run()

		if cmd.ProcessState.ExitCode() == -1 {
			t.Fatalf("shell terminated by signal: %v", err)
		}

		// bash sometimes likes to error on an input via stderr
		// while forgetting to set the exit code to non-zero. Fun.
		// Note that we do not treat warnings as errors.
		stderrLines := strings.Split(stderrBuf.String(), "\n")
		for i, line := range stderrLines {
			stderrLines[i] = strings.TrimSpace(line)
		}
		stderrLines = slices.DeleteFunc(stderrLines, func(line string) bool {
			return line == "" || strings.Contains(line, "warning:")
		})
		if stderr := strings.Join(stderrLines, "\n"); stderr != "" {
			if err == nil {
				err = fmt.Errorf("non-fatal error: %s", stderr)
			} else {
				err = fmt.Errorf("%v: %s", err, stderr)
			}
		}

		if wantErr && err == nil {
			t.Fatalf("Expected error in %q", strings.Join(cmd.Args, " "))
		} else if !wantErr && err != nil {
			t.Fatalf("Unexpected error in %q: %v", strings.Join(cmd.Args, " "), err)
		}
	}
}

var cmpOpt = cmp.FilterValues(func(p1, p2 Pos) bool { return true }, cmp.Ignore())

func singleParse(p *Parser, in string, want *File) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		t.Logf("input: %s", in)
		got, err := p.Parse(newStrictReader(in), "")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		Walk(got, sanityChecker{tb: t, src: in}.visit)
		qt.Assert(t, qt.CmpEquals(got, want, cmpOpt))
	}
}

type errorCase struct {
	in string

	byLangIndex [langResolvedVariantsCount]string

	// The real shells where testing the input succeeds rather than failing as expected.
	flipConfirmSet LangVariant
}

func errCase(in string, opts ...func(*errorCase)) errorCase {
	c := errorCase{in: in}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func langErr(want string, langSets ...LangVariant) func(*errorCase) {
	return func(c *errorCase) {
		// The parameter is a slice to allow omitting the argument.
		switch len(langSets) {
		case 0:
			for i := range c.byLangIndex {
				c.byLangIndex[i] = want
			}
			return
		case 1:
			// continue below
		default:
			panic("use a LangVariant bitset")
		}
		for lang := range langSets[0].bits() {
			c.byLangIndex[lang.index()] = want
		}
	}
}

func flipConfirm(langSet LangVariant) func(*errorCase) {
	return func(c *errorCase) { c.flipConfirmSet = langSet }
}

var flipConfirmAll = flipConfirm(langResolvedVariants)

// The real shells which allow unclosed heredocs.
// TODO: allow ending a heredoc at EOF in these language variant modes.
var flipConfirmUnclosedHeredoc = flipConfirm(LangBash | LangPOSIX | LangBats | LangZsh)

func init() {
	seenInputs := make(map[string]bool)
	for i, c := range errorCases {
		if seenInputs[c.in] {
			panic(fmt.Sprintf("duplicate at %d: %q", i, c.in))
		}
		seenInputs[c.in] = true
	}
}

var errorCases = []errorCase{
	// NOTE: bash 5.3 never errors on an invalid/incomplete multibyte byte
	// — it advances one opaque byte (lib/sh/shmbchar.c: MB_INVALIDCH ->
	// clen=1). The fork follows that, so inputs whose ONLY problem was a
	// stray byte now parse cleanly; see TestParseByteTransparentBytes.
	// The cases below kept an error because they are ALSO unterminated;
	// the expected error is now that of the surrounding construct, with
	// the stray byte absorbed into a literal.
	errCase(
		// Heredoc whose delimiter holds a stray byte; the body never
		// closes it, so the error is now the unclosed-heredoc one (real
		// shells accept an unclosed heredoc at EOF, hence the flip).
		"<<$\xc8\n$\xc8",
		langErr("1:1: unclosed here-document \"$\\xc8\""),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"echo $((foo\x80bar",
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		// The stray \x91 is now absorbed into the array literal, which
		// is left unterminated. Only LangBash had an expectation here
		// (POSIX fails earlier with its no-arrays error, unchanged).
		"z=($\\\n#\\\n\\\n$#\x91\\\n",
		langErr("1:3: unexpected EOF while looking for matching `)'", LangBash),
	),
	errCase(
		`${ `,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		`${ foo;`,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		`${ foo }`,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		`${|`,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		`${|foo;`,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		`${|foo }`,
		langErr("1:1: unexpected EOF while looking for matching `}'", LangMirBSDKorn),
	),
	errCase(
		// The stray byte is absorbed into the arithmetic body, leaving
		// the construct unterminated; POSIX parses `((` as nested `(`.
		"((foo\x80bar",
		langErr("1:1: unexpected EOF while looking for matching `))'", LangBash|LangMirBSDKorn|LangBats|LangZsh),
		langErr("1:2: unexpected EOF while looking for matching `)'", LangPOSIX),
	),
	errCase(
		";\x80",
		langErr("1:1: `;` can only immediately follow a statement"),
	),
	errCase(
		// \x80 becomes an opaque byte (U+FFFD for classification): in
		// bash mode the brace expansion runs to EOF unmatched; the other
		// variants reject it as an invalid parameter-expansion operator.
		"${a\x80",
		langErr("1:1: unexpected EOF while looking for matching `}'", LangBash),
		langErr("1:4: not a valid parameter expansion operator: `�`", LangPOSIX|LangMirBSDKorn|LangBats|LangZsh),
	),
	errCase(
		"${a#\x80",
		langErr("1:1: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"${a-'\x80",
		langErr("1:5: unexpected EOF while looking for matching `''"),
	),
	errCase(
		"echo $((a |\x80",
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		"!",
		langErr("1:1: `!` cannot form a statement alone", LangPOSIX|LangMirBSDKorn|LangZsh|LangBats),
	),
	errCase(
		"! !",
		langErr("1:1: cannot negate a command multiple times", LangPOSIX|LangMirBSDKorn|LangZsh|LangBats),
	),
	errCase(
		"! ! foo",
		langErr("1:1: cannot negate a command multiple times", LangPOSIX|LangZsh|LangBats),
	),
	errCase(
		"}",
		langErr("1:1: `}` can only be used to close a block"),
	),
	errCase(
		"foo | }",
		langErr("1:7: `}` can only be used to close a block"),
	),
	errCase(
		"foo }",
		langErr("1:5: `}` can only be used to close a block", LangZsh),
	),
	errCase(
		"then",
		langErr("1:1: `then` can only be used in an `if`"),
	),
	errCase(
		"elif",
		langErr("1:1: `elif` can only be used in an `if`"),
	),
	errCase(
		"fi",
		langErr("1:1: `fi` can only be used to end an `if`"),
	),
	errCase(
		"do",
		langErr("1:1: `do` can only be used in a loop"),
	),
	errCase(
		"done",
		langErr("1:1: `done` can only be used to end a loop"),
	),
	errCase(
		"esac",
		langErr("1:1: `esac` can only be used to end a `case`"),
	),
	errCase(
		"a=b { foo; }",
		langErr("1:12: `}` can only be used to close a block"),
	),
	errCase(
		"a=b foo() { bar; }",
		langErr("1:8: a command can only contain words and redirects; encountered `(`"),
	),
	errCase(
		"a=b if foo; then bar; fi",
		langErr("1:13: `then` can only be used in an `if`"),
	),
	errCase(
		">f { foo; }",
		langErr("1:1: redirects before compound commands are a zsh feature; tried parsing as LANG"),
		langErr("", LangZsh),
	),
	errCase(
		">f foo() { bar; }",
		langErr("1:1: redirects before compound commands are a zsh feature; tried parsing as LANG"),
		langErr("", LangZsh),
	),
	errCase(
		">f if foo; then bar; fi",
		langErr("1:1: redirects before compound commands are a zsh feature; tried parsing as LANG"),
		langErr("", LangZsh),
	),
	errCase(
		"if done; then b; fi",
		langErr("1:4: `done` can only be used to end a loop"),
	),
	errCase(
		"'",
		langErr("1:1: unexpected EOF while looking for matching `''"),
	),
	errCase(
		`"`,
		langErr("1:1: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		`'\''`,
		langErr("1:4: unexpected EOF while looking for matching `''"),
	),
	errCase(
		";",
		langErr("1:1: `;` can only immediately follow a statement"),
	),
	errCase(
		"{ ; }",
		langErr("1:1: `{` must be followed by a statement list"),
		langErr("", LangZsh|LangMirBSDKorn),
	),
	errCase(
		`"foo"(){ :; }`,
		// Bash 5.3 defers non-identifier func-name errors to runtime,
		// so the parser accepts here. Other langs still reject.
		langErr("1:1: invalid func name", LangPOSIX|LangBats|LangZsh),
		flipConfirm(LangMirBSDKorn), // TODO: support non-literal func names
	),
	errCase(
		`foo$bar(){ :; }`,
		// Bash 5.3 defers to runtime.
		langErr("1:1: invalid func name", LangPOSIX|LangBats|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"{",
		langErr("1:1: `{` must be followed by a statement list"),
		langErr("1:1: unexpected EOF while looking for matching `}'", LangZsh|LangMirBSDKorn),
	),
	errCase(
		"{ foo;",
		langErr("1:1: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"{ foo; #}",
		langErr("1:1: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"(x",
		langErr("1:1: unexpected EOF while looking for matching `)'"),
	),
	errCase(
		")",
		langErr("1:1: `)` can only be used to close a subshell"),
	),
	errCase(
		"`",
		langErr("1:1: unexpected EOF while looking for matching ``'"),
	),
	errCase(
		";;",
		langErr("1:1: `;;` can only be used in a case clause"),
	),
	errCase(
		"( foo;",
		langErr("1:1: unexpected EOF while looking for matching `)'"),
	),
	errCase(
		"&",
		langErr("1:1: `&` can only immediately follow a statement"),
	),
	errCase(
		"|",
		langErr("1:1: `|` can only immediately follow a statement"),
	),
	errCase(
		"&&",
		langErr("1:1: `&&` can only immediately follow a statement"),
	),
	errCase(
		"||",
		langErr("1:1: `||` can only immediately follow a statement"),
	),
	errCase(
		"foo; || bar",
		langErr("1:6: `||` can only immediately follow a statement"),
	),
	errCase(
		"echo & || bar",
		langErr("1:8: `||` can only immediately follow a statement"),
	),
	errCase(
		"echo & ; bar",
		langErr("1:8: `;` can only immediately follow a statement"),
	),
	errCase(
		"foo;;",
		langErr("1:4: `;;` can only be used in a case clause"),
	),
	errCase(
		"foo(",
		langErr("1:1: `foo(` must be followed by `)`", LangPOSIX|LangBash|LangMirBSDKorn|LangBats),
		langErr("1:4: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"foo(bar",
		langErr("1:1: `foo(` must be followed by `)`", LangPOSIX|LangBash|LangMirBSDKorn|LangBats),
		langErr("1:4: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"à(",
		langErr("1:1: `foo(` must be followed by `)`", LangPOSIX|LangBash|LangMirBSDKorn|LangBats),
		langErr("1:3: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"foo'",
		langErr("1:4: unexpected EOF while looking for matching `''"),
	),
	errCase(
		`foo"`,
		langErr("1:4: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		`"foo`,
		langErr("1:1: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		`"foobar\`,
		langErr("1:1: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		`"foo\a`,
		langErr("1:1: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		"foo()",
		langErr("1:1: `foo()` must be followed by a statement"),
		flipConfirm(LangMirBSDKorn), // TODO: some variants allow a missing body
	),
	errCase(
		// bash requires a compound command as the function body; a simple
		// command is a syntax error. dash/POSIX and mksh accept it as an
		// extension, so they parse cleanly (no error expected for them).
		"f() echo bar",
		langErr("1:5: a function body must be a compound command", LangBash|LangBats),
	),
	errCase(
		// Reserved words that introduce non-compound commands (`time`,
		// `declare`, `let`, `coproc`, ...) are likewise rejected as bodies.
		"f() time echo bar",
		langErr("1:5: a function body must be a compound command", LangBash|LangBats),
	),
	errCase(
		"foo() {",
		langErr("1:7: `{` must be followed by a statement list"),
		langErr("1:7: unexpected EOF while looking for matching `}'", LangZsh|LangMirBSDKorn),
	),
	errCase(
		"foo() { bar;",
		langErr("1:7: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"foo-bar() { x; }",
		langErr("1:1: invalid func name", LangPOSIX),
	),
	errCase(
		"foò() { x; }",
		langErr("1:1: invalid func name", LangPOSIX),
	),
	errCase(
		"echo foo(",
		langErr("1:9: a command can only contain words and redirects; encountered `(`", LangPOSIX|LangBash|LangMirBSDKorn|LangBats),
		langErr("1:9: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"echo &&",
		langErr("1:6: `&&` must be followed by a statement"),
	),
	errCase(
		"echo |",
		langErr("1:6: `|` must be followed by a statement"),
	),
	errCase(
		"echo ||",
		langErr("1:6: `||` must be followed by a statement"),
	),
	errCase(
		"echo | #bar",
		langErr("1:6: `|` must be followed by a statement"),
	),
	errCase(
		"echo && #bar",
		langErr("1:6: `&&` must be followed by a statement"),
	),
	errCase(
		"`echo &&`",
		langErr("1:7: `&&` must be followed by a statement"),
	),
	errCase(
		"`echo |`",
		langErr("1:7: `|` must be followed by a statement"),
	),
	errCase(
		"echo | ! true",
		langErr("1:8: `!` can only be used in full statements", LangPOSIX|LangMirBSDKorn|LangZsh|LangBats),
	),
	errCase(
		"echo >",
		langErr("1:6: `>` must be followed by a word"),
	),
	errCase(
		"echo >>",
		langErr("1:6: `>>` must be followed by a word"),
	),
	errCase(
		"echo <",
		langErr("1:6: `<` must be followed by a word"),
	),
	errCase(
		"echo 2>",
		langErr("1:7: `>` must be followed by a word"),
	),
	errCase(
		"echo <\nbar",
		langErr("1:6: `<` must be followed by a word"),
	),
	errCase(
		"echo | < #bar",
		langErr("1:8: `<` must be followed by a word"),
	),
	errCase(
		"echo && > #",
		langErr("1:9: `>` must be followed by a word"),
	),
	errCase(
		"<<",
		langErr("1:1: `<<` must be followed by a word"),
	),
	errCase(
		"<<EOF",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<EOF\n\\",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<EOF\n\\\n",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<EOF\n\\\nEOF",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmAll, // why does mksh allow this?
	),
	errCase(
		"<<EOF\nfoo\\\nEOF",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<'EOF'\n\\\n",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<EOF <`\n#\n`\n``",
		langErr("1:1: unclosed here-document `EOF`"),
	),
	errCase(
		"<<'EOF'",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<\\EOF",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<\\\\EOF",
		langErr("1:1: unclosed here-document `\\EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<-EOF",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<-EOF\n\t",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<-'EOF'\n\t",
		langErr("1:1: unclosed here-document `EOF`"),
		flipConfirmUnclosedHeredoc,
	),
	errCase(
		"<<\nEOF\nbar\nEOF",
		langErr("1:1: `<<` must be followed by a word"),
	),
	errCase(
		"$(<<EOF\nNOTEOF)",
		langErr("1:3: unclosed here-document `EOF`", LangBash|LangMirBSDKorn),
		// Note that this fails on external shells as they treat ")" as part of the heredoc.
	),
	errCase(
		"`<<EOF\nNOTEOF`",
		langErr("1:2: unclosed here-document `EOF`", LangBash|LangMirBSDKorn),
		flipConfirmAll,
		// Note that this works on external shells as they treat "`" as outside the heredoc.
	),
	errCase(
		"if",
		langErr("1:1: `if` must be followed by a statement list"),
		langErr("1:1: `if <cond>` must be followed by `then`", LangZsh|LangMirBSDKorn),
	),
	errCase(
		"if true;",
		langErr("1:1: `if <cond>` must be followed by `then`"),
	),
	errCase(
		"if true then",
		langErr("1:1: `if <cond>` must be followed by `then`"),
	),
	errCase(
		"if true; then bar;",
		langErr("1:1: `if` statement must end with `fi`"),
	),
	errCase(
		"if true; then bar; fi#etc",
		langErr("1:1: `if` statement must end with `fi`"),
	),
	errCase(
		"if a; then b; elif c;",
		langErr("1:15: `elif <cond>` must be followed by `then`"),
	),
	errCase(
		"'foo' '",
		langErr("1:7: unexpected EOF while looking for matching `''"),
	),
	errCase(
		"'foo\n' '",
		langErr("2:3: unexpected EOF while looking for matching `''"),
	),
	errCase(
		"while",
		langErr("1:1: `while` must be followed by a statement list"),
		langErr("1:1: `while <cond>` must be followed by `do`", LangZsh|LangMirBSDKorn),
	),
	errCase(
		"while true;",
		langErr("1:1: `while <cond>` must be followed by `do`"),
	),
	errCase(
		"while true; do bar",
		langErr("1:1: `while` statement must end with `done`"),
	),
	errCase(
		"while true; do bar;",
		langErr("1:1: `while` statement must end with `done`"),
	),
	errCase(
		"until",
		langErr("1:1: `until` must be followed by a statement list"),
		langErr("1:1: `until <cond>` must be followed by `do`", LangZsh|LangMirBSDKorn),
	),
	errCase(
		"until true;",
		langErr("1:1: `until <cond>` must be followed by `do`"),
	),
	errCase(
		"until true; do bar",
		langErr("1:1: `until` statement must end with `done`"),
	),
	errCase(
		"until true; do bar;",
		langErr("1:1: `until` statement must end with `done`"),
	),
	errCase(
		"for",
		langErr("1:1: `for` must be followed by a literal"),
	),
	errCase(
		"for i",
		langErr("1:1: `for foo` must be followed by `in`, `do`, `;`, or a newline"),
	),
	errCase(
		"for i in;",
		langErr("1:1: `for foo [in words]` must be followed by `do`"),
	),
	errCase(
		"for i in 1 2 3;",
		langErr("1:1: `for foo [in words]` must be followed by `do`"),
	),
	errCase(
		"for i in 1 2 &",
		langErr("1:1: `for foo [in words]` must be followed by `do`"),
	),
	errCase(
		"for i in 1 2 (",
		langErr("1:14: word list can only contain words"),
		langErr("1:14: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"for i in 1 2 3; do echo $i;",
		langErr("1:1: `for` statement must end with `done`"),
	),
	errCase(
		"for i in 1 2 3; echo $i;",
		langErr("1:1: `for foo [in words]` must be followed by `do`"),
	),
	errCase(
		"for in 1 2 3; do echo $i; done",
		langErr("1:1: `for foo` must be followed by `in`, `do`, `;`, or a newline"),
	),
	errCase(
		"select",
		langErr("1:1: `select` must be followed by a literal", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select i",
		langErr("1:1: `select foo` must be followed by `in`, `do`, `;`, or a newline", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select i in;",
		langErr("1:1: `select foo [in words]` must be followed by `do`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select i in 1 2 3;",
		langErr("1:1: `select foo [in words]` must be followed by `do`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select i in 1 2 3; do echo $i;",
		langErr("1:1: `select` statement must end with `done`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select i in 1 2 3; echo $i;",
		langErr("1:1: `select foo [in words]` must be followed by `do`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"select in 1 2 3; do echo $i; done",
		langErr("1:1: `select foo` must be followed by `in`, `do`, `;`, or a newline", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo foo &\n;",
		langErr("2:1: `;` can only immediately follow a statement"),
	),
	errCase(
		"echo $(foo",
		langErr("1:6: unexpected EOF while looking for matching `)'"),
	),
	errCase(
		"echo $((foo",
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		`echo $((\`,
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		`echo $((o\`,
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		`echo $((foo\a`,
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		`echo $(($(a"`,
		langErr("1:12: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		"echo $((`echo 0`",
		langErr("1:6: unexpected EOF while looking for matching `))'"),
	),
	errCase(
		`echo $((& $(`,
		langErr("1:9: `&` must follow an expression"),
	),
	errCase(
		`echo $((a'`,
		langErr("1:10: unexpected EOF while looking for matching `''"),
	),
	errCase(
		`echo $((a b"`,
		langErr("1:11: not a valid arithmetic operator: `b`"),
	),
	errCase(
		"echo $(((3))",
		langErr("1:6: reached `)` without matching `$((` with `))`"),
	),
	errCase(
		"echo $((+))",
		langErr("1:9: `+` must be followed by an expression"),
	),
	errCase(
		"echo $((a b c))",
		langErr("1:11: not a valid arithmetic operator: `b`"),
	),
	errCase(
		"echo $((a ; c))",
		langErr("1:11: not a valid arithmetic operator: `;`"),
	),
	// `echo $((foo) )` — bash 5.3 accepts whitespace between the two
	// closing arithmetic parens, so the matching streaming parse now
	// succeeds (see peekArithmEnd in parser_arithm.go).
	errCase(
		"echo $((a *))",
		langErr("1:11: `*` must be followed by an expression"),
	),
	// Bash accepts a bare prefix ++/-- at parse time and reports it as
	// an arithmetic command/expansion error at runtime.
	// Bash accepts a missing ternary ':' at parse time and reports it
	// as an arithmetic expansion error at runtime.
	errCase(
		"echo $((a : b))",
		langErr("1:11: ternary operator missing `?` before `:`"),
	),
	errCase(
		"echo $((/",
		langErr("1:9: `/` must follow an expression"),
	),
	errCase(
		"echo $((:",
		langErr("1:9: ternary operator missing `?` before `:`"),
	),
	// Bash 5.3 accepts assignment to non-name lvalue at parse time and
	// errors at runtime; the parse-time check was removed so for-loop
	// expressions like `for (( 7=4; ; ))` reach the runtime path
	// instead of aborting the surrounding parse. See expand/arith.go
	// for the "attempted assignment to non-variable" error.
	// errCase(
	// 	"echo $((1'2`))",
	// 	// TODO: Take a look at this again, since this no longer fails
	// 	// after fixing https://github.com/mvdan/sh/issues/587.
	// 	// Note that Bash seems to treat code inside $(()) as if it were
	// 	// within double quotes, yet still requires single quotes to be
	// 	// matched.
	// 	//  `1:10: not a valid arithmetic operator: ``,
	// ),
	errCase(
		"<<EOF\n$(()a",
		langErr("2:1: `$((` must be followed by an expression"),
	),
	errCase(
		"<<EOF\n`))",
		langErr("2:2: `)` can only be used to close a subshell"),
	),
	errCase(
		"echo ${foo",
		langErr("1:6: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"echo ${à}",
		langErr("1:8: invalid parameter name"),
		langErr("1:8: not a valid parameter expansion operator: `à`", LangZsh), // not great, but seems like an edge case
	),
	errCase(
		"echo ${1a}",
		langErr("1:8: invalid parameter name"),
	),
	errCase(
		"echo ${foo-bar",
		langErr("1:6: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"#foo\n{ bar;",
		langErr("2:1: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		`echo "foo${bar"`,
		// Bash scans bad substitutions to the matching `}`, past the
		// closing quote, and only hits EOF.
		langErr("1:10: unexpected EOF while looking for matching `}'", LangBash),
		langErr("1:15: not a valid parameter expansion operator: `\"`", LangPOSIX|LangMirBSDKorn|LangBats|LangZsh),
	),
	errCase(
		"echo ${%",
		langErr("1:6: `${%foo}` is a mksh feature; tried parsing as LANG"),
		langErr("1:9: invalid parameter name", LangMirBSDKorn),
	),
	errCase(
		"echo ${+",
		langErr("1:6: `${+foo}` is a zsh feature; tried parsing as LANG"),
		langErr("1:6: unexpected EOF while looking for matching `}'", LangZsh),
	),
	errCase(
		// Bash does not support zsh nested parameter expansions; it
		// parses `${...}` at face value and only rejects the nested
		// form as a bad substitution at expansion time, so the parse
		// error here is the unclosed outer expansion at EOF.
		"echo ${#${",
		langErr("1:9: nested parameter expansions are a zsh feature; tried parsing as LANG"),
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash),
		langErr("1:9: unexpected EOF while looking for matching `}'", LangZsh),
	),
	errCase(
		"echo ${#$(",
		langErr("1:9: nested parameter expansions are a zsh feature; tried parsing as LANG"),
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash),
		langErr("1:9: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"echo ${(",
		langErr("1:6: parameter expansion flags are a zsh feature; tried parsing as LANG"),
		langErr("1:8: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"echo $$(foo)",
		langErr("1:8: a command can only contain words and redirects; encountered `(`", LangPOSIX|LangBash|LangMirBSDKorn|LangBats),
	),
	errCase(
		"echo ${##",
		langErr("1:6: unexpected EOF while looking for matching `}'"),
	),
	errCase(
		"echo ${#<}",
		// Bash defers the bad substitution to expansion time.
		langErr("1:9: not a valid parameter expansion operator: `<`", LangPOSIX|LangMirBSDKorn|LangBats|LangZsh),
	),
	errCase(
		"echo ${%<}",
		langErr("1:8: invalid parameter name", LangMirBSDKorn),
	),
	errCase(
		"echo ${!<}",
		langErr("1:9: not a valid parameter expansion operator: `<`", LangMirBSDKorn),
	),
	errCase(
		"echo ${@foo}",
		langErr("1:9: `@` cannot be followed by a word"),
	),
	errCase(
		"echo ${$needbraces}",
		langErr("1:9: `$` cannot be followed by a word"),
	),
	errCase(
		"echo ${?foo}",
		langErr("1:9: `?` cannot be followed by a word"),
	),
	errCase(
		"echo ${-foo}",
		langErr("1:9: `-` cannot be followed by a word"),
	),
	errCase(
		`echo ${"bad"}`,
		langErr("1:6: invalid nested parameter expansion", LangZsh),
	),
	errCase(
		`echo ${"$needbraces"}`,
		langErr("1:10: `$` cannot be followed by a word", LangZsh),
	),
	errCase(
		`echo ${"${foo}}`,
		langErr("1:8: reached `}` without closing quote `\"`", LangZsh),
	),
	errCase(
		`echo ${"${foo}bad"}`,
		langErr("1:6: invalid nested parameter expansion", LangZsh),
	),
	errCase(
		"echo ${${nested}foo}",
		langErr("1:17: nested parameter expansion cannot be followed by a word", LangZsh),
	),
	errCase(
		"echo ${@[@]} ${@[*]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${${nested}[@]",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangZsh),
	),
	errCase(
		"echo ${*[@]} ${*[*]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${#[x]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${$[0]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${?[@]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${2[@]}",
		langErr("1:9: cannot index a special parameter name", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo;}",
		langErr("1:11: not a valid parameter expansion operator: `;`", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo!}",
		langErr("1:11: not a valid parameter expansion operator: `!`", LangMirBSDKorn),
	),
	errCase(
		"echo ${#foo:-bar}",
		langErr("1:12: cannot combine multiple parameter expansion operators", LangMirBSDKorn),
	),
	errCase(
		"echo ${%foo:1:3}",
		langErr("1:12: cannot combine multiple parameter expansion operators", LangMirBSDKorn),
	),
	errCase(
		"echo ${#foo%x}",
		langErr("1:12: cannot combine multiple parameter expansion operators", LangMirBSDKorn),
	),
	errCase(
		"echo foo\n;",
		langErr("2:1: `;` can only immediately follow a statement"),
	),
	errCase(
		"<<$ <<0\n$(<<$<<",
		langErr("2:6: `<<` must be followed by a word", LangBash|LangMirBSDKorn),
	),
	errCase(
		"(foo) bar",
		langErr("1:7: statements must be separated by &, ; or a newline"),
	),
	errCase(
		"{ foo; } bar",
		langErr("1:10: statements must be separated by &, ; or a newline"),
	),
	errCase(
		"if foo; then bar; fi bar",
		langErr("1:22: statements must be separated by &, ; or a newline"),
	),
	errCase(
		"case",
		langErr("1:1: `case` must be followed by a word"),
	),
	errCase(
		"case i",
		langErr("1:1: `case x` must be followed by `in`"),
	),
	errCase(
		"case i in 3) foo;",
		langErr("1:1: `case` statement must end with `esac`"),
	),
	errCase(
		"case i in 3) foo; 4) bar; esac",
		langErr("1:20: a command can only contain words and redirects; encountered `)`"),
	),
	errCase(
		"case i in 3&) foo;",
		langErr("1:12: case patterns must be separated with `|`"),
	),
	errCase(
		"case $i in &) foo;",
		langErr("1:12: case patterns must consist of words"),
	),
	errCase(
		"case i {",
		langErr("1:1: `case i {` is a mksh feature; tried parsing as LANG"),
		langErr("1:1: `case` statement must end with `}`", LangMirBSDKorn),
	),
	errCase(
		"case i { x) y ;;",
		langErr("1:1: `case` statement must end with `}`", LangMirBSDKorn),
	),
	errCase(
		"\"`\"",
		langErr("1:3: unexpected EOF while looking for matching `\"'"),
	),
	errCase(
		"`\"`",
		langErr("1:2: reached \"`\" without closing quote `\"`"),
	),
	errCase(
		"`\\```",
		langErr("1:3: unexpected EOF while looking for matching ``'"),
	),
	errCase(
		"`{\nfoo`",
		langErr("1:2: reached \"`\" without matching `{` with `}`"),
	),
	errCase(
		"echo \"`)`\"",
		langErr("1:8: `)` can only be used to close a subshell"),
		flipConfirm(LangPOSIX), // dash bug?
	),
	errCase(
		"<<$bar\n$bar",
		langErr("1:3: expansions not allowed in heredoc words"),
		flipConfirmAll, // we are stricter
	),
	errCase(
		"<<${bar}\n${bar}",
		langErr("1:3: expansions not allowed in heredoc words"),
		flipConfirmAll, // we are stricter
	),
	errCase(
		"<<$(bar)\n$(bar)",
		langErr("1:3: expansions not allowed in heredoc words", LangBash),
		flipConfirmAll, // we are stricter
	),

	errCase(
		"<<$-\n$-",
		langErr("1:3: expansions not allowed in heredoc words"),
		flipConfirmAll, // we are stricter
	),
	errCase(
		"<<`bar`\n`bar`",
		langErr("1:3: expansions not allowed in heredoc words"),
		flipConfirmAll, // we are stricter
	),
	errCase(
		"<<\"$bar\"\n$bar",
		langErr("1:4: expansions not allowed in heredoc words"),
		flipConfirmAll, // we are stricter
	),
	errCase(
		"<<a <<0\n$(<<$<<",
		langErr("2:6: `<<` must be followed by a word"),
	),
	errCase(
		`""()`,
		// Bash 5.3 parses this and emits "not a valid identifier" at
		// runtime (the empty name fails ValidName); the bashy parser
		// then errors with "foo() must be followed by a statement"
		// because no body follows. Match that.
		langErr("1:1: `foo()` must be followed by a statement", LangBash),
		langErr("1:1: invalid func name", LangPOSIX|LangBats|LangZsh),
		flipConfirm(LangMirBSDKorn), // TODO: support non-literal func names, even empty ones?
	),
	errCase(
		"]] )",
		langErr("1:1: `]]` can only be used to close a test"),
		langErr("1:4: a command can only contain words and redirects; encountered `)`", LangPOSIX),
	),
	errCase(
		"((foo",
		langErr("1:1: unexpected EOF while looking for matching `))'", LangBash|LangMirBSDKorn|LangZsh),
		langErr("1:2: unexpected EOF while looking for matching `)'", LangPOSIX),
	),
	errCase(
		"echo ((foo",
		langErr("1:6: `((` can only be used to open an arithmetic cmd", LangBash|LangMirBSDKorn|LangZsh),
		langErr("1:1: `foo(` must be followed by `)`", LangPOSIX),
	),
	errCase(
		"echo |&",
		langErr("1:6: `|&` must be followed by a statement", LangBash|LangZsh),
		langErr("1:6: `|` must be followed by a statement", LangPOSIX),
	),
	errCase(
		"|& a",
		langErr("1:1: `|&` is not a valid start for a statement", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"foo |& bar",
		langErr("1:5: `|` must be followed by a statement", LangPOSIX),
	),
	errCase(
		"let a+ b",
		langErr("1:6: `+` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"let + a",
		langErr("1:5: `+` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	// Bash accepts `let a ++` at parse time and reports the trailing
	// `++` as a runtime arithmetic error.
	// Bash 5.3 accepts `(a)++`, `1++`, `$0++` at parse time and emits
	// the "attempted assignment to non-variable" error at runtime (so
	// surrounding constructs like for-loops can terminate cleanly).
	// The previous parse-time `++ must follow a name` errors blocked
	// that runtime path — see expand/arith.go for the runtime check.
	errCase(
		"let --(a)",
		langErr("1:5: `--` must be followed by a literal", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"let --$a",
		langErr("1:5: `--` must be followed by a literal", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"let a+\n",
		langErr("1:6: `+` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"`let !`",
		langErr("1:6: `!` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"let a:b",
		langErr("1:6: ternary operator missing `?` before `:`", LangBash|LangMirBSDKorn|LangZsh),
	),
	// `let a+b=c` — runtime error now (see comment above).
	errCase(
		"[[",
		langErr("1:1: `[[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ ]]",
		langErr("1:1: `[[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a",
		langErr("1:1: unexpected EOF while looking for matching `]]'", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a ||",
		langErr("1:6: `||` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a && &&",
		langErr("1:6: `&&` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a && ]]",
		langErr("1:6: `&&` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a ==",
		langErr("1:6: `==` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a =~",
		langErr("1:6: `=~` must be followed by a word", LangBash|LangZsh),
		langErr("1:6: regex tests are a bash/zsh feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"[[ -f a",
		langErr("1:1: unexpected EOF while looking for matching `]]'", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ -n\na ]]",
		langErr("1:4: `-n` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a -ef\nb ]]",
		langErr("1:6: `-ef` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a ==\nb ]]",
		langErr("1:6: `==` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a -nt b",
		langErr("1:1: unexpected EOF while looking for matching `]]'", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a =~ b",
		langErr("1:1: unexpected EOF while looking for matching `]]'", LangBash|LangZsh),
	),
	errCase(
		"[[ a b c ]]",
		langErr("1:6: not a valid test operator: `b`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		// A unary operator must not swallow the `]]` terminator as its
		// operand; report the operator as missing its argument instead.
		"[[ -z ]]",
		langErr("1:4: `-z` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		// A redirection-shaped token (`3<`) where a binary operator was
		// expected reports the literal fd, not the internal token name.
		"[[ a 3< b ]]",
		langErr("1:6: not a valid test operator: `3`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a b$x c ]]",
		langErr("1:6: test operator words must consist of a single literal", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a & b ]]",
		langErr("1:6: not a valid test operator: `&`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ true && () ]]",
		langErr("1:12: `(` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ true && (&& ]]",
		langErr("1:12: `(` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a == ! b ]]",
		langErr("1:11: not a valid test operator: `b`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ (! ) ]]",
		langErr("1:5: `!` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ ! && ]]",
		langErr("1:4: `!` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ (-e ) ]]",
		langErr("1:5: `-e` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ (a) == b ]]",
		langErr("1:8: expected `&&`, `||` or `]]` after complex expr", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"[[ a =~ ; ]]",
		langErr("1:6: `=~` must be followed by a word", LangBash|LangZsh),
	),
	errCase(
		"[[ a =~ )",
		langErr("1:6: `=~` must be followed by a word", LangBash|LangZsh),
	),
	errCase(
		"[[ a =~ ())",
		langErr("1:1: reached `)` without matching `[[` with `]]`", LangBash|LangZsh),
	),
	errCase(
		"[[ >",
		langErr("1:1: `[[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"local (",
		langErr("1:7: `local` must be followed by names or assignments", LangBash),
		langErr("1:7: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"declare 0=${o}",
		langErr("1:9: invalid var name", LangBash|LangZsh),
	),
	errCase(
		"declare ab=${o})",
		langErr("1:16: statements must be separated by &, ; or a newline", LangBash|LangZsh),
	),
	errCase(
		"export ab=$0(",
		langErr("1:13: `export` must be followed by names or assignments", LangBash),
		langErr("1:13: unexpected EOF while looking for matching `)'", LangZsh),
	),
	errCase(
		"declare {x,y}=(1 2)",
		langErr("1:15: `declare` must be followed by names or assignments", LangBash),
	),
	errCase(
		"a=(<)",
		langErr("1:4: array element values must be words", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"a=([)",
		langErr("1:4: `[` must be followed by an expression", LangBash|LangZsh),
	),
	errCase(
		"a=([i)",
		langErr("1:4: reached `)` without matching `[` with `]`", LangBash|LangZsh),
	),
	errCase(
		"a=([i])",
		langErr("1:4: `[x]` must be followed by `=`", LangBash|LangZsh),
		flipConfirmAll, // TODO: why is this valid?
	),
	errCase(
		"a=([i]=(y))",
		langErr("1:7: arrays cannot be nested", LangBash|LangZsh),
	),
	errCase(
		"o=([0]=#",
		langErr("1:8: array element values must be words", LangBash|LangZsh),
	),
	errCase(
		"a[b] ==[",
		flipConfirmAll, // stringifies
	),
	errCase(
		"a[b] +=c",
		flipConfirmAll, // stringifies
	),
	errCase(
		"function",
		langErr("1:1: `function` must be followed by a name", LangBash|LangMirBSDKorn),
		langErr("1:1: `foo()` must be followed by a statement", LangZsh),
	),
	errCase(
		"function foo(",
		langErr("1:1: `function foo(` must be followed by `)`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"function `function",
		// Bash 5.3 reads the backquote-prefixed token as part of the
		// name (deferred to runtime) and then errors on the unclosed
		// command substitution; the bashy parser surfaces the same
		// "must be followed by a name" but the position now points
		// inside the backquote rather than at the leading `function`.
		langErr("1:11: `function` must be followed by a name", LangBash),
		langErr("1:1: `function` must be followed by a name", LangMirBSDKorn),
		langErr("1:11: `foo()` must be followed by a statement", LangZsh),
	),
	errCase(
		`function "foo"(){}`,
		// Bash 5.3 defers the non-identifier name check to runtime.
		langErr("1:1: `function` must be followed by a name", LangMirBSDKorn),
		langErr("1:10: invalid func name", LangZsh),
	),
	errCase(
		"function foo()",
		langErr("1:1: `foo()` must be followed by a statement", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"@test",
		langErr("1:1: `@test` must be followed by a description word", LangBats),
	),
	errCase(
		"@test 'desc'",
		langErr("1:1: `@test \"desc\"` must be followed by a statement", LangBats),
	),
	errCase(
		"echo <<<",
		langErr("1:6: `<<<` must be followed by a word", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"a[",
		langErr("1:2: `[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"a[b",
		langErr("1:2: unexpected EOF while looking for matching `]'", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"a[]",
		langErr("1:2: `[` must be followed by an expression", LangMirBSDKorn|LangZsh),
		flipConfirmAll, // is cmd
	),
	errCase(
		"a[[",
		langErr("1:3: `[` must follow a name like a[i]", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo $((a[))",
		langErr("1:10: `[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo $((a[b))",
		langErr("1:10: reached `)` without matching `[` with `]`", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo $((a[]))",
		langErr("1:10: `[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
		flipConfirm(LangMirBSDKorn), // wrong?
	),
	errCase(
		"echo $((x$t[",
		langErr("1:12: `[` must follow a name like a[i]", LangBash|LangMirBSDKorn),
		langErr("1:12: `[` must be followed by an expression", LangZsh),
	),
	errCase(
		"a[1]",
		flipConfirmAll, // is cmd
	),
	errCase(
		"a[i]+",
		flipConfirmAll, // is cmd
	),
	errCase(
		"a[1]#",
		flipConfirmAll, // is cmd
	),
	errCase(
		"echo $[foo",
		langErr("1:6: unexpected EOF while looking for matching `]'", LangBash),
	),
	errCase(
		"echo $'",
		langErr("1:6: unexpected EOF while looking for matching `''", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		`echo $"`,
		langErr("1:6: unexpected EOF while looking for matching `\"'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo @(",
		langErr("1:6: unexpected EOF while looking for matching `)'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo @(a",
		langErr("1:6: unexpected EOF while looking for matching `)'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo @([abc)])",
		langErr("1:14: a command can only contain words and redirects; encountered `)`", LangBash|LangMirBSDKorn),
	),
	errCase(
		"((@(",
		langErr("1:4: not a valid arithmetic operator: `(`", LangBash|LangMirBSDKorn),
	),
	errCase(
		"time { foo;",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"time ! foo",
		langErr("1:6: `!` can only be used in full statements", LangMirBSDKorn|LangZsh),
	),
	errCase(
		"coproc",
		langErr("1:1: coproc clause requires a command", LangBash),
	),
	errCase(
		"coproc\n$",
		langErr("1:1: coproc clause requires a command", LangBash),
	),
	errCase(
		"coproc declare (",
		langErr("1:16: `declare` must be followed by names or assignments", LangBash),
	),
	errCase(
		"echo ${foo[}",
		langErr("1:11: `[` must be followed by an expression", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo ${foo]}",
		langErr("1:11: not a valid parameter expansion operator: `]`", LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo ${foo[]}",
		langErr("1:11: `[` must be followed by an expression", LangMirBSDKorn|LangZsh),
		flipConfirm(LangMirBSDKorn), // TODO: why is this valid?
	),
	errCase(
		"echo ${a/\n",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${a/''",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${a-\n",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo:",
		langErr("1:11: `:` must be followed by an expression", LangBash|LangMirBSDKorn),
	),
	errCase(
		"foo=force_expansion; echo ${foo:1 2}",
		langErr("1:35: not a valid arithmetic operator: `2`", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo:1",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo:1:",
		langErr("1:13: `:` must be followed by an expression", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo:1:2",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo:h",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangZsh),
	),
	errCase(
		"echo ${foo,",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash),
	),
	errCase(
		"echo ${foo@",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash),
	),
	errCase(
		"echo ${foo@Q",
		langErr("1:6: unexpected EOF while looking for matching `}'", LangBash),
	),
	errCase(
		"for ((;;",
		langErr("1:5: unexpected EOF while looking for matching `))'", LangBash),
	),
	errCase(
		"for ((;;0000000",
		langErr("1:5: unexpected EOF while looking for matching `))'", LangBash),
	),
	errCase(
		"echo <(",
		langErr("1:6: `<` must be followed by a word", LangPOSIX|LangMirBSDKorn),
	),
	errCase(
		"echo >(",
		langErr("1:6: `>` must be followed by a word", LangPOSIX|LangMirBSDKorn),
	),
	errCase(
		"echo {var}>foo",
		langErr("1:6: `{varname}` redirects are a bash feature; tried parsing as LANG", LangPOSIX|LangMirBSDKorn),
		// shells treat {var} as an argument, but we are a bit stricter
		// so that users won't think this will work like they expect in POSIX shell.
		flipConfirmAll,
	),
	errCase(
		"echo ;&",
		langErr("1:7: `&` can only immediately follow a statement", LangPOSIX),
		langErr("1:6: `;&` can only be used in a case clause", LangBash|LangMirBSDKorn|LangZsh),
	),
	errCase(
		"echo ;;&",
		langErr("1:6: `;;` can only be used in a case clause", LangPOSIX|LangMirBSDKorn),
	),
	errCase(
		"echo ;|",
		langErr("1:7: `|` can only immediately follow a statement", LangPOSIX|LangBash),
		langErr("1:6: `;|` can only be used in a case clause", LangMirBSDKorn|LangZsh),
	),
	errCase(
		"for i in 1 2 3; { echo; }",
		langErr("1:17: for loops with braces are a bash/mksh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"echo !(a)",
		langErr("1:6: extended globs are a bash/mksh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"echo $a@(b)",
		langErr("1:8: extended globs are a bash/mksh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"foo=(1 2)",
		langErr("1:5: arrays are a bash/mksh/zsh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"a=$c\n'",
		langErr("2:1: unexpected EOF while looking for matching `''"),
	),
	errCase(
		"echo ${!foo[@]}",
		langErr("1:12: arrays are a bash/mksh/zsh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"foo << < bar",
		langErr("1:5: `<<` must be followed by a word", LangPOSIX),
	),
	errCase(
		"echo ${foo,bar}",
		langErr("1:11: this expansion operator is a bash feature; tried parsing as LANG", LangPOSIX|LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@Q}",
		langErr("1:11: this expansion operator is a bash/mksh feature; tried parsing as LANG", LangPOSIX),
	),
	errCase(
		"echo ${foo@a}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@u}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@A}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@E}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@K}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@k}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@L}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@P}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"echo ${foo@U}",
		langErr("1:12: this expansion operator is a bash feature; tried parsing as LANG", LangMirBSDKorn),
	),
	errCase(
		"foo=force_expansion; echo ${foo@#}",
		langErr("1:33: this expansion operator is a mksh feature; tried parsing as LANG", LangBash),
	),
	errCase(
		"`\"`\\",
		langErr("1:2: reached \"`\" without closing quote `\"`"),
	),
}

func TestInputName(t *testing.T) {
	t.Parallel()
	in := "("
	want := "some-file.sh:1:2: syntax error: unexpected end of file from `(' command on line 1"
	p := NewParser()
	_, err := p.Parse(strings.NewReader(in), "some-file.sh")
	if err == nil {
		t.Fatalf("Expected error in %q: %v", in, want)
	}
	got := err.Error()
	if got != want {
		t.Fatalf("Error mismatch in %q\nwant: %s\ngot:  %s",
			in, want, got)
	}
}

var errBadReader = fmt.Errorf("write: expected error")

type badReader struct{}

func (b badReader) Read(p []byte) (int, error) { return 0, errBadReader }

func TestReadErr(t *testing.T) {
	t.Parallel()
	p := NewParser()
	_, err := p.Parse(badReader{}, "")
	if err == nil {
		t.Fatalf("Expected error with bad reader")
	}
	if err != errBadReader {
		t.Fatalf("Error mismatch with bad reader:\nwant: %v\ngot:  %v",
			errBadReader, err)
	}
}

type strictStringReader struct {
	*strings.Reader
	gaveEOF bool
}

func newStrictReader(s string) *strictStringReader {
	return &strictStringReader{Reader: strings.NewReader(s)}
}

func (r *strictStringReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		if r.gaveEOF {
			return n, fmt.Errorf("duplicate EOF read")
		}
		r.gaveEOF = true
	}
	return n, err
}

func TestParseStmtsSeq(t *testing.T) {
	t.Parallel()
	p := NewParser()
	inReader, inWriter := io.Pipe()
	recv := make(chan bool, 10)
	errc := make(chan error, 1)
	go func() {
		var firstErr error
		for _, err := range p.StmtsSeq(inReader) {
			recv <- true
			if firstErr == nil && err != nil {
				firstErr = err
			}
		}
		errc <- firstErr
	}()
	io.WriteString(inWriter, "foo\n")
	<-recv
	io.WriteString(inWriter, "bar; baz")
	inWriter.Close()
	<-recv
	<-recv
	if err := <-errc; err != nil {
		t.Fatalf("Expected no error: %v", err)
	}
}

func TestParseStmtsSeqStopEarly(t *testing.T) {
	t.Parallel()
	p := NewParser()
	inReader, inWriter := io.Pipe()
	defer inWriter.Close()
	recv := make(chan bool, 10)
	errc := make(chan error, 1)
	go func() {
		var firstErr error
		for stmt, err := range p.StmtsSeq(inReader) {
			recv <- true
			if firstErr == nil && err != nil {
				firstErr = err
			}
			if stmt.Background {
				break
			}
		}
		errc <- firstErr
	}()
	io.WriteString(inWriter, "a\n")
	<-recv
	io.WriteString(inWriter, "b &\n") // stop here
	<-recv
	if err := <-errc; err != nil {
		t.Fatalf("Expected no error: %v", err)
	}
}

func TestParseStmtsSeqError(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"foo; )",
		"bar; <<EOF",
	} {
		t.Run("", func(t *testing.T) {
			p := NewParser()
			recv := make(chan bool, 10)
			errc := make(chan error, 1)
			go func() {
				var firstErr error
				for _, err := range p.StmtsSeq(strings.NewReader(in)) {
					recv <- true
					if firstErr == nil && err != nil {
						firstErr = err
					}
				}
				errc <- firstErr
			}()
			<-recv
			if err := <-errc; err == nil {
				t.Fatalf("Expected an error in %q, but got nil", in)
			}
		})
	}
}

func TestParseWords(t *testing.T) {
	t.Parallel()
	p := NewParser()
	inReader, inWriter := io.Pipe()
	recv := make(chan bool, 10)
	errc := make(chan error, 1)
	go func() {
		errc <- p.Words(inReader, func(w *Word) bool {
			recv <- true
			return true
		})
	}()
	// TODO: Allow a single space to end parsing a word. At the moment, the
	// parser must read the next non-space token (the next literal or
	// newline, in this case) to finish parsing a word.
	io.WriteString(inWriter, "foo ")
	io.WriteString(inWriter, "bar\n")
	<-recv
	io.WriteString(inWriter, "baz etc")
	inWriter.Close()
	<-recv
	<-recv
	<-recv
	if err := <-errc; err != nil {
		t.Fatalf("Expected no error: %v", err)
	}
}

func TestParseWordsStopEarly(t *testing.T) {
	t.Parallel()
	p := NewParser()
	r := strings.NewReader("a\nb\nc\n")
	parsed := 0
	err := p.Words(r, func(w *Word) bool {
		parsed++
		return w.Lit() != "b"
	})
	if err != nil {
		t.Fatalf("Expected no error: %v", err)
	}
	if want := 2; parsed != want {
		t.Fatalf("wanted %d words parsed, got %d", want, parsed)
	}
}

func TestParseWordsError(t *testing.T) {
	t.Parallel()
	in := "foo )"
	p := NewParser()
	recv := make(chan bool, 10)
	errc := make(chan error, 1)
	go func() {
		errc <- p.Words(strings.NewReader(in), func(w *Word) bool {
			recv <- true
			return true
		})
	}()
	<-recv
	want := "1:5: `)` is not a valid word"
	got := fmt.Sprintf("%v", <-errc)
	if got != want {
		t.Fatalf("Expected %q as an error, but got %q", want, got)
	}
}

var documentTests = []struct {
	in   string
	want []WordPart
}{
	{
		"foo",
		[]WordPart{lit("foo")},
	},
	{
		" foo  $bar",
		[]WordPart{
			lit(" foo  "),
			litParamExp("bar"),
		},
	},
	{
		"$bar\n\n",
		[]WordPart{
			litParamExp("bar"),
			lit("\n\n"),
		},
	},
}

func TestParseDocument(t *testing.T) {
	t.Parallel()
	p := NewParser()

	for _, tc := range documentTests {
		t.Run("", func(t *testing.T) {
			got, err := p.Document(strings.NewReader(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			Walk(got, sanityChecker{tb: t, src: tc.in}.visit)
			want := &Word{Parts: tc.want}
			qt.Assert(t, qt.CmpEquals(got, want, cmpOpt))
		})
	}
}

func TestParseDocumentError(t *testing.T) {
	t.Parallel()
	in := "foo $("
	p := NewParser()
	_, err := p.Document(strings.NewReader(in))
	want := "1:5: unexpected EOF while looking for matching `)'"
	got := fmt.Sprintf("%v", err)
	if got != want {
		t.Fatalf("Expected %q as an error, but got %q", want, got)
	}
}

var arithmeticTests = []struct {
	in   string
	want ArithmExpr
}{
	{
		"foo",
		litWord("foo"),
	},
	{
		"3 + 4",
		&BinaryArithm{
			Op: Add,
			X:  litWord("3"),
			Y:  litWord("4"),
		},
	},
	{
		"3 + 4 + 5",
		&BinaryArithm{
			Op: Add,
			X: &BinaryArithm{
				Op: Add,
				X:  litWord("3"),
				Y:  litWord("4"),
			},
			Y: litWord("5"),
		},
	},
	{
		"1 ? 0 : 2",
		&BinaryArithm{
			Op: TernQuest,
			X:  litWord("1"),
			Y: &BinaryArithm{
				Op: TernColon,
				X:  litWord("0"),
				Y:  litWord("2"),
			},
		},
	},
	{
		"1 ? 20 : x+=2",
		&BinaryArithm{
			Op: AddAssgn,
			X: &BinaryArithm{
				Op: TernQuest,
				X:  litWord("1"),
				Y: &BinaryArithm{
					Op: TernColon,
					X:  litWord("20"),
					Y:  litWord("x"),
				},
			},
			Y: litWord("2"),
		},
	},
	{
		"a = 3, ++a, a--",
		&BinaryArithm{
			Op: Comma,
			X: &BinaryArithm{
				Op: Comma,
				X: &BinaryArithm{
					Op: Assgn,
					X:  litWord("a"),
					Y:  litWord("3"),
				},
				Y: &UnaryArithm{
					Op: Inc,
					X:  litWord("a"),
				},
			},
			Y: &UnaryArithm{
				Op:   Dec,
				Post: true,
				X:    litWord("a"),
			},
		},
	},
}

func TestParseArithmetic(t *testing.T) {
	t.Parallel()
	p := NewParser()

	for _, tc := range arithmeticTests {
		t.Run("", func(t *testing.T) {
			got, err := p.Arithmetic(strings.NewReader(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			Walk(got, sanityChecker{tb: t, src: tc.in}.visit)
			qt.Assert(t, qt.CmpEquals(got, tc.want, cmpOpt))
		})
	}
}

func TestParseArithmeticError(t *testing.T) {
	t.Parallel()
	in := "3 +"
	p := NewParser()
	_, err := p.Arithmetic(strings.NewReader(in))
	want := "1:3: `+` must be followed by an expression"
	got := fmt.Sprintf("%v", err)
	if got != want {
		t.Fatalf("Expected %q as an error, but got %q", want, got)
	}
}

var stopAtTests = []struct {
	in   string
	stop string
	want any
}{
	{
		"foo bar", "$$",
		litCall("foo", "bar"),
	},
	{
		"$foo $", "$$",
		call(word(litParamExp("foo")), litWord("$")),
	},
	{
		"echo foo $$", "$$",
		litCall("echo", "foo"),
	},
	{
		"$$", "$$",
		&File{},
	},
	{
		"echo foo\n$$\n", "$$",
		litCall("echo", "foo"),
	},
	{
		"echo foo; $$", "$$",
		litCall("echo", "foo"),
	},
	{
		"echo foo; $$", "$$",
		litCall("echo", "foo"),
	},
	{
		"echo foo;$$", "$$",
		litCall("echo", "foo"),
	},
	{
		"echo '$$'", "$$",
		call(litWord("echo"), word(sglQuoted("$$"))),
	},
}

func TestParseStopAt(t *testing.T) {
	t.Parallel()
	for _, c := range stopAtTests {
		p := NewParser(StopAt(c.stop))
		want := fullProg(c.want)
		t.Run("", singleParse(p, c.in, want))
	}
}

func TestValidName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"Empty", "", false},
		{"Simple", "foo", true},
		{"MixedCase", "Foo", true},
		{"Underscore", "_foo", true},
		{"NumberPrefix", "3foo", false},
		{"NumberSuffix", "foo3", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidName(tc.in)
			if got != tc.want {
				t.Fatalf("ValidName(%q) got %t, wanted %t",
					tc.in, got, tc.want)
			}
		})
	}
}

func TestIsIncomplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       string
		notWords bool
		want     bool
	}{
		{in: "foo\n", want: false},
		{in: "foo;", want: false},
		{in: "\n", want: false},
		{in: "badsyntax)", want: false},
		{in: "foo 'incomp", want: true},
		{in: `foo "incomp`, want: true},
		{in: "foo ${incomp", want: true},

		{in: "foo; 'incomp", notWords: true, want: true},
		{in: `foo; "incomp`, notWords: true, want: true},
		{in: " (incomp", notWords: true, want: true},
	}
	p := NewParser()
	for i, tc := range tests {
		t.Run(fmt.Sprintf("Parse%02d", i), func(t *testing.T) {
			r := strings.NewReader(tc.in)
			_, err := p.Parse(r, "")
			if got := IsIncomplete(err); got != tc.want {
				t.Fatalf("%q got %t, wanted %t", tc.in, got, tc.want)
			}
		})
		t.Run(fmt.Sprintf("Interactive%02d", i), func(t *testing.T) {
			r := strings.NewReader(tc.in)
			var firstErr error
			for _, err := range p.InteractiveSeq(r) {
				if firstErr == nil && err != nil {
					firstErr = err
				}
			}
			if got := IsIncomplete(firstErr); got != tc.want {
				t.Fatalf("%q got %t, wanted %t", tc.in, got, tc.want)
			}
		})
		if !tc.notWords {
			t.Run(fmt.Sprintf("WordsSeq%02d", i), func(t *testing.T) {
				r := strings.NewReader(tc.in)
				var firstErr error
				for _, err := range p.WordsSeq(r) {
					if firstErr == nil && err != nil {
						firstErr = err
					}
				}
				if got := IsIncomplete(firstErr); got != tc.want {
					t.Fatalf("%q got %t, wanted %t", tc.in, got, tc.want)
				}
			})
		}
	}
}

func TestPosEdgeCases(t *testing.T) {
	in := "`\\\\foo`\n" + // one escaped backslash and 3 bytes
		"\x00foo\x00bar\n" // 8 bytes and newline
	p := NewParser()
	f, err := p.Parse(strings.NewReader(in), "")
	qt.Assert(t, qt.IsNil(err))
	cmdSubst := f.Stmts[0].Cmd.(*CallExpr).Args[0].Parts[0].(*CmdSubst)
	lit := cmdSubst.Stmts[0].Cmd.(*CallExpr).Args[0].Parts[0].(*Lit)

	qt.Check(t, qt.Equals(lit.Value, lit.Value))
	// Note that positions of literals with escape sequences inside backquote command substitutions
	// are weird, since we effectively skip over the double escaping in the literal value and positions.
	// Even though the input source has '\\foo' between columns 2 and 7 (length 5)
	// we end up keeping '\foo' between columns 3 and 7 (length 4).
	qt.Check(t, qt.Equals(lit.ValuePos.String(), "1:3"))
	qt.Check(t, qt.Equals(lit.ValueEnd.String(), "1:7"))

	// Check that we skip over null bytes when counting columns.
	qt.Check(t, qt.Equals(f.Stmts[1].Pos().String(), "2:2"))
	qt.Check(t, qt.Equals(f.Stmts[1].End().String(), "2:9"))
}

func TestParseRecoverErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src string

		wantErr     bool
		wantMissing int
	}{
		{src: "foo;"},
		{src: "foo"},
		{
			src:         "'incomp",
			wantMissing: 1,
		},
		{
			src:         "foo; 'incomp",
			wantMissing: 1,
		},
		{
			src:         "{ incomp",
			wantMissing: 1,
		},
		{
			src:         "(incomp",
			wantMissing: 1,
		},
		{
			src:         "(incomp; foo",
			wantMissing: 1,
		},
		{
			src:         "$(incomp",
			wantMissing: 1,
		},
		{
			src:         "((incomp",
			wantMissing: 1,
		},
		{
			src:         "$((incomp",
			wantMissing: 1,
		},
		{
			src:         "if foo",
			wantMissing: 3,
		},
		{
			src:         "if foo; then bar",
			wantMissing: 1,
		},
		{
			src:         "for i in 1 2 3; echo $i; done",
			wantMissing: 1,
		},
		{
			src:         `"incomp`,
			wantMissing: 1,
		},
		{
			src:         "`incomp",
			wantMissing: 1,
		},
		{
			src:         "incomp >",
			wantMissing: 1,
		},
		{
			src:         "${incomp",
			wantMissing: 1,
		},
		{
			src:         "incomp | ",
			wantMissing: 1,
		},
		{
			src:         "incomp || ",
			wantMissing: 1,
		},
		{
			src:         "incomp && ",
			wantMissing: 1,
		},
		{
			src:         `(one | { two >`,
			wantMissing: 3,
		},
		{
			src:         `(one > ; two | ); { three`,
			wantMissing: 3,
		},
		{
			src:     "badsyntax)",
			wantErr: true,
		},
	}
	parser := NewParser(RecoverErrors(3))
	printer := NewPrinter()
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			t.Logf("input: %s", tc.src)
			r := strings.NewReader(tc.src)
			f, err := parser.Parse(r, "")
			if tc.wantErr {
				qt.Assert(t, qt.Not(qt.IsNil(err)))
			} else {
				qt.Assert(t, qt.IsNil(err))
				switch len(f.Stmts) {
				case 0:
					t.Fatalf("result has no statements")
				case 1:
					if f.Stmts[0].Pos().IsRecovered() {
						t.Fatalf("result is only a recovered statement")
					}
				}
			}
			qt.Assert(t, qt.Equals(countRecoveredPositions(reflect.ValueOf(f)), tc.wantMissing))

			// Check that walking or printing the syntax tree still appears to work
			// even when the input source was incomplete.
			Walk(f, func(node Node) bool {
				if node == nil {
					return true
				}
				// Each position should either be valid, pointing to an offset within the input,
				// or invalid, which could be due to the position being recovered.
				for _, pos := range []Pos{node.Pos(), node.End()} {
					qt.Assert(t, qt.IsFalse(pos.IsValid() && pos.IsRecovered()), qt.Commentf("positions cannot be valid and recovered"))
					if !pos.IsValid() {
						qt.Assert(t, qt.Equals(pos.Offset(), 0), qt.Commentf("invalid positions have no offset"))
						qt.Assert(t, qt.Equals(pos.Line(), 0), qt.Commentf("invalid positions have no line"))
						qt.Assert(t, qt.Equals(pos.Col(), 0), qt.Commentf("invalid positions have no column"))
					}
				}
				return true
			})
			// Note that we don't particularly care about good formatting here.
			printer.Print(io.Discard, f)
		})
	}
}

func countRecoveredPositions(x reflect.Value) int {
	switch x.Kind() {
	case reflect.Interface:
		return countRecoveredPositions(x.Elem())
	case reflect.Pointer:
		if !x.IsNil() {
			return countRecoveredPositions(x.Elem())
		}
	case reflect.Slice:
		n := 0
		for i := range x.Len() {
			n += countRecoveredPositions(x.Index(i))
		}
		return n
	case reflect.Struct:
		if pos, ok := x.Interface().(Pos); ok {
			if pos.IsRecovered() {
				return 1
			}
			return 0
		}
		n := 0
		for i := range x.NumField() {
			n += countRecoveredPositions(x.Field(i))
		}
		return n
	}
	return 0
}
