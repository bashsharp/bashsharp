package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	coreutilsshell "github.com/qiangli/coreutils/shell"
	_ "github.com/qiangli/yoke/cmds/all"

	"github.com/qiangli/bash/expand"
	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/syntax"
)

func TestStartupInheritedFdsDiscoversOpenFD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("startup fd discovery is Unix fd-table behavior; Windows handles are not enumerable the same way")
	}
	t.Setenv(interp.BashyInheritedFdsEnv, "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	fd := int(r.Fd())
	if fd < 3 {
		t.Fatalf("test pipe fd = %d, want >= 3", fd)
	}
	if !slices.Contains(startupInheritedFds(), fd) {
		t.Fatalf("startupInheritedFds() did not include open fd %d", fd)
	}
}

func TestNewRunnerInheritsOLDPWD(t *testing.T) {
	// bash (and every POSIX shell) carries an inherited $OLDPWD through so
	// `cd -` works in a fresh shell. Stripping it was a non-conformance
	// (oils builtin-cd differential vs all 5 oracle shells).
	t.Setenv("OLDPWD", "/tmp")
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	got := r.Env.Get("OLDPWD")
	if !got.IsSet() || got.String() != "/tmp" {
		t.Fatalf("OLDPWD not inherited into runner: set=%v val=%q", got.IsSet(), got.String())
	}
}

func TestNewRunnerKeepsBashVersionVarsUnexported(t *testing.T) {
	for _, argv0 := range []string{"sh", "bash"} {
		t.Run(argv0, func(t *testing.T) {
			withStrictPosixEnv(t, argv0, false)
			unsetTestEnv(t, "BASH")
			unsetTestEnv(t, "BASH_VERSION")
			r, err := newRunner()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"BASH", "BASH_VERSION"} {
				vr := r.Env.Get(name)
				if !vr.IsSet() || vr.String() == "" {
					t.Fatalf("%s is not visible as a shell variable: %#v", name, vr)
				}
				if vr.Exported {
					t.Fatalf("%s is exported on clean startup: %#v", name, vr)
				}
			}
		})
	}
}

func TestPromptTransformBangPosixMode(t *testing.T) {
	// GNU Bash 5.3 preserves a bare ! in ${v@P} by default, while POSIX
	// mode expands it as a history number. The explicitly escaped \! prompt
	// form expands in both modes.
	tests := []struct {
		name  string
		posix bool
		value string
		want  string
	}{
		{name: "default bare bang", value: "!", want: "<!>\n"},
		{name: "default escaped bang", value: `\!`, want: "<1>\n"},
		{name: "posix bare bang", posix: true, value: "!", want: "<1>\n"},
		{name: "posix escaped bang", posix: true, value: `\!`, want: "<1>\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var r *interp.Runner
			r, err := interp.New(
				interp.Env(expand.ListEnviron("v="+tc.value)),
				interp.StdIO(nil, &stdout, io.Discard),
				interp.WithPosixMode(tc.posix),
				interp.PromptExpand(func(s string) string {
					return expandPrompt(s, func(name string) string {
						return r.Env.Get(name).String()
					}, 0, 0, tc.posix)
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			prog, err := syntax.NewParser(
				syntax.Variant(syntax.LangBash),
				syntax.PosixMode(tc.posix),
			).Parse(strings.NewReader(`printf '<%s>\n' "${v@P}"`), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), prog); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShouldRunInteractiveForDashSWithArgs(t *testing.T) {
	oldCommand, oldForceI, oldPlusI, oldReadStdin := *command, *forceI, *plusI, *readStdin
	oldFlags := flag.CommandLine
	t.Cleanup(func() {
		*command, *forceI, *plusI, *readStdin = oldCommand, oldForceI, oldPlusI, oldReadStdin
		flag.CommandLine = oldFlags
	})
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	*command, *forceI, *plusI, *readStdin = "", false, false, true
	if err := flag.CommandLine.Parse([]string{"one", "two", "three"}); err != nil {
		t.Fatal(err)
	}
	if !shouldRunInteractive(true) {
		t.Fatal("bash -s arg... with terminal stdin should be interactive")
	}
	if shouldRunInteractive(false) {
		t.Fatal("bash -s arg... with non-terminal stdin should not be interactive without -i")
	}
}

func TestCommandSubstOpenBefore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		pos  syntax.Pos
		want bool
	}{
		{
			name: "inside command substitution",
			src:  "echo $( if x; then echo foo )\n",
			pos:  syntax.NewPos(0, 1, 9),
			want: true,
		},
		{
			name: "after command substitution",
			src:  "echo $(echo ok) if x; then echo foo\n",
			pos:  syntax.NewPos(0, 1, 17),
			want: false,
		},
		{
			name: "plain compound command",
			src:  "if x; then echo foo )\n",
			pos:  syntax.NewPos(0, 1, 1),
			want: false,
		},
		{
			name: "arithmetic expansion",
			src:  "echo $((1 + ))\n",
			pos:  syntax.NewPos(0, 1, 11),
			want: false,
		},
		{
			name: "multiline command substitution",
			src:  "echo $(\nif x; then echo foo\n)\n",
			pos:  syntax.NewPos(0, 2, 1),
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := commandSubstOpenBefore([]byte(test.src), test.pos); got != test.want {
				t.Fatalf("commandSubstOpenBefore() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPrintBashParseErrorCompoundEOF(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		text string
		want string
	}{
		{
			name: "if non-empty eof line",
			src:  "if\n\ttrue; true\nthen\n\techo foo bar",
			text: "`if` statement must end with `fi`",
			want: "bash: -c: line 5: syntax error: unexpected end of file from `if' command on line 1\n",
		},
		{
			name: "until whitespace eof line",
			src:  "until false\ndo\n\techo false\n\t",
			text: "`until` statement must end with `done`",
			want: "bash: -c: line 4: syntax error: unexpected end of file from `until' command on line 1\n",
		},
		{
			name: "case whitespace eof line",
			src:  "case foo in\nbar)\tif false\n\tthen\n\t\ttrue\n\tfi\n\t;;\n\t",
			text: "`case` statement must end with `esac`",
			want: "bash: -c: line 7: syntax error: unexpected end of file from `case' command on line 1\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printBashParseError(&buf, []byte(test.src), "bash: -c", syntax.ParseError{
				Pos:  syntax.NewPos(0, 1, 1),
				Text: test.text,
			})
			if got := buf.String(); got != test.want {
				t.Fatalf("printBashParseError mismatch\nwant:\n%q\ngot:\n%q", test.want, got)
			}
		})
	}
}

func TestPrintBashParseErrorArrayCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		src        string
		pos        syntax.Pos
		text       string
		incomplete bool
		want       string
	}{
		{
			name: "array assignment after command word",
			src:  `printf "%s\n" -a a=(a 'b  c')`,
			pos:  syntax.NewPos(0, 1, 20),
			text: "a command can only contain words and redirects; encountered `(`",
			want: "bash: -c: line 1: syntax error near unexpected token `('\n" +
				"bash: -c: line 1: `printf \"%s\\n\" -a a=(a 'b  c')'\n",
		},
		{
			name: "empty declare name",
			src:  `declare -r []=asdf`,
			pos:  syntax.NewPos(0, 1, 13),
			text: "invalid var name",
			want: "bash: -c: line 1: declare: `[]=asdf': not a valid identifier\n",
		},
		{
			name: "zsh process substitution in declare",
			src:  `declare -a ''=(a 'b c')`,
			pos:  syntax.NewPos(0, 1, 14),
			text: "`=(` process substitutions are a zsh feature",
			want: "bash: -c: line 1: syntax error near unexpected token `('\n" +
				"bash: -c: line 1: `declare -a ''=(a 'b c')'\n",
		},
		{
			name: "array metacharacter pair",
			src:  `metas=( <> < > ! )`,
			pos:  syntax.NewPos(0, 1, 9),
			text: "array element values must be words",
			want: "bash: -c: line 1: syntax error near unexpected token `<>'\n" +
				"bash: -c: line 1: `metas=( <> < > ! )'\n",
		},
		{
			name: "arithmetic bare tilde",
			src:  `echo $(( ~ ))`,
			pos:  syntax.NewPos(0, 1, 10),
			text: "`~` must be followed by an expression",
			want: "bash: -c: line 1: ~ : arithmetic syntax error: operand expected (error token is \"~ \")\n",
		},
		{
			// `a[` — an array subscript running off the end of the input.
			// bash reports the matching-`]' EOF wording and omits the
			// source-line echo (array-assign__006).
			name:       "unterminated array subscript",
			src:        `a[`,
			pos:        syntax.NewPos(1, 1, 2),
			text:       "`[` must be followed by an expression",
			incomplete: true,
			want:       "bash: -c: line 1: unexpected EOF while looking for matching `]'\n",
		},
		{
			// A stray `)` where bash expects a command: bash names it as a
			// generic unexpected token, with the source-line echo (array__005).
			name: "stray close paren",
			src:  `)`,
			pos:  syntax.NewPos(0, 1, 1),
			text: "`)` can only be used to close a subshell",
			want: "bash: -c: line 1: syntax error near unexpected token `)'\n" +
				"bash: -c: line 1: `)'\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			printBashParseError(&buf, []byte(test.src), "bash: -c", syntax.ParseError{
				Pos:        test.pos,
				Text:       test.text,
				Incomplete: test.incomplete,
			})
			if got := buf.String(); got != test.want {
				t.Fatalf("printBashParseError mismatch\nwant:\n%q\ngot:\n%q", test.want, got)
			}
		})
	}
}

func TestDefaultCommandArgv0(t *testing.T) {
	t.Parallel()
	tests := []struct {
		arg0 string
		want string
	}{
		{"/tmp/bin/bash", "/tmp/bin/bash"},
		{"specialname", "specialname"},
		{"-specialname", "-specialname"},
	}
	for _, test := range tests {
		if got := defaultCommandArgv0(test.arg0); got != test.want {
			t.Fatalf("defaultCommandArgv0(%q) = %q, want %q", test.arg0, got, test.want)
		}
	}
}

func TestSplitCombinedShortFlagsLoginCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "single login",
			args: []string{"bashy", "-l", "-c", "echo ok"},
			want: []string{"bashy", "-login", "-c", "echo ok"},
		},
		{
			name: "combined login command",
			args: []string{"bashy", "-lc", "echo ok"},
			want: []string{"bashy", "-login", "-c", "echo ok"},
		},
		{
			name: "combined login errexit command",
			args: []string{"bashy", "-lec", "echo ok"},
			want: []string{"bashy", "-login", "-o", "errexit", "-c", "echo ok"},
		},
		{
			name: "short h is hashall not help",
			args: []string{"bashy", "-h"},
			want: []string{"bashy", "-o", "hashall"},
		},
		{
			name: "combined short h is hashall not help",
			args: []string{"bashy", "-ehc", "echo ok"},
			want: []string{"bashy", "-o", "errexit", "-o", "hashall", "-c", "echo ok"},
		},
		{
			name: "combined trailing o consumes option name",
			args: []string{"bashy", "-Cefuvxo", "allexport", "echo ok"},
			want: []string{"bashy", "-o", "noclobber", "-o", "errexit", "-o", "noglob", "-o", "nounset", "-o", "verbose", "-o", "xtrace", "-o", "allexport", "echo ok"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := splitCombinedShortFlags(test.args); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("splitCombinedShortFlags(%q) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

func TestStaticAliasExpand(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"shopt -s expand_aliases",
		"alias switch=case",
		"switch foo in foo) echo ok;; esac",
		"alias echo='echo ordinary'",
		"echo stays-runtime",
		"echo $( switch foo in foo) echo ok;; esac )",
		"alias comsub0='echo $(echo $DATE'",
		"comsub0)",
		"alias math1='echo $( date )'",
		"math1)",
		"alias number='echo 123'",
		"(( $(number) ))",
		"alias DONE='}'",
		"echo ok; DONE)",
		"alias let='let --'",
		"let '1 == 1'",
		"alias al=' '",
		"shopt -s expand_aliases 2>/dev/null",
		"al for x in y",
		"do echo $x",
		"done",
		"${THIS_SH} -c '",
		"shopt -s expand_aliases 2>/dev/null",
		"alias al=\" \"",
		"alias foo=bar",
		"alias for=echo",
		"al for foo in v",
		"do echo foo=$foo bar=$bar",
		"done' bash",
		"${THIS_SH} -o posix -c '",
		"shopt -s expand_aliases 2>/dev/null",
		"alias al=\" \"",
		"alias foo=bar",
		"al for foo in v",
		"do echo foo=$foo bar=$bar",
		"done' bash",
		`alias raw="echo 'Error:"`,
		`raw bar'`,
		"alias comment=#",
		"comment text after",
		`alias pipe='printf "%s\n" \'`,
		"pipe|cat",
		"alias semi=';'",
		"echo a semi echo b",
		"alias in='<'",
		"cat in file",
		"alias 'headplus=cat <<EOF",
		"hello'",
		"headplus",
		"world",
		"EOF",
		"alias head='cat <<END' body='head",
		"here-document",
		"END'",
		"body",
		"unalias -a",
		`raw again'`,
		"",
	}, "\n")
	want := strings.Join([]string{
		"shopt -s expand_aliases",
		"alias switch=case",
		"case foo in foo) echo ok;; esac",
		"alias echo='echo ordinary'",
		"echo stays-runtime",
		"echo $( case foo in foo) echo ok;; esac )",
		"alias comsub0='echo $(echo $DATE'",
		"echo $(echo $DATE)",
		"alias math1='echo $( date )'",
		"math1)",
		"alias number='echo 123'",
		"(( 123 ))",
		"alias DONE='}'",
		"echo ok; })",
		"alias let='let --'",
		"let '1 == 1'",
		"alias al=' '",
		"shopt -s expand_aliases 2>/dev/null",
		"  for x in y",
		"do echo $x",
		"done",
		"${THIS_SH} -c '",
		"shopt -s expand_aliases 2>/dev/null",
		"alias al=\" \"",
		"alias foo=bar",
		"alias for=echo",
		"al for foo in v",
		"do echo foo=$foo bar=$bar",
		"done' bash",
		"${THIS_SH} -o posix -c '",
		"shopt -s expand_aliases 2>/dev/null",
		"alias al=\" \"",
		"alias foo=bar",
		"  for foo in v",
		"do echo foo=$foo bar=$bar",
		"done' bash",
		`alias raw="echo 'Error:"`,
		`echo 'Error: bar'`,
		"alias comment=#",
		"# text after",
		`alias pipe='printf "%s\n" \'`,
		`printf "%s\n" \|cat`,
		"alias semi=';'",
		"echo a ; echo b",
		"alias in='<'",
		"cat < file",
		"alias 'headplus=cat <<EOF",
		"hello'",
		"cat <<EOF",
		"hello",
		"world",
		"EOF",
		"alias head='cat <<END' body='head",
		"here-document",
		"END'",
		"cat <<END",
		"here-document",
		"END",
		"unalias -a",
		`raw again'`,
		"",
	}, "\n")
	if got := string(staticAliasExpand([]byte(src), false)); got != want {
		t.Fatalf("staticAliasExpand mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestStaticAliasExpandPosixCompoundFollowers(t *testing.T) {
	t.Parallel()

	src := strings.Join([]string{
		"alias forx='for x in 1; ' fory='for y in 2; do echo $y;' for='",
		" do' dn='",
		" done'",
		"forx for echo $x; dn",
		"fory dn",
		"alias c='case a ' case='",
		" in a) :' in=",
		"c case X; echo A; esac",
		"c in a) echo B; esac",
		"",
	}, "\n")
	want := strings.Join([]string{
		"alias forx='for x in 1; ' fory='for y in 2; do echo $y;' for='",
		" do' dn='",
		" done'",
		"for x in 1;  ",
		" do echo $x; ",
		" done",
		"for y in 2; do echo $y; ",
		" done",
		"alias c='case a ' case='",
		" in a) :' in=",
		"case a  ",
		" in a) : X; echo A; esac",
		"case a  in a) echo B; esac",
		"",
	}, "\n")
	if got := string(staticAliasExpand([]byte(src), true)); got != want {
		t.Fatalf("staticAliasExpand posix compound mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestStaticAliasExpandIgnoresQuotedCommandSubstitution(t *testing.T) {
	t.Parallel()

	src := "alias echo=')'\nf() { bracket \"$(echo x)\"; }\n"
	if got := string(staticAliasExpand([]byte(src), true)); got != src {
		t.Fatalf("staticAliasExpand changed quoted command substitution\ngot:  %q\nwant: %q", got, src)
	}
}

func TestQuoteParamReplBackquotes(t *testing.T) {
	t.Parallel()
	src := []byte("printf '%s\\n' ${qpath//`printf '%s' \"\\\\\\\\\"`/}\n")
	want := "printf '%s\\n' /tmp/foo/bar\n"
	if got := string(quoteParamReplBackquotes(src)); got != want {
		t.Fatalf("quoteParamReplBackquotes mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestRunRetriesPosixAfterParsedPrefix(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"set -o posix",
		`echo 1 ${IFS+'}'z}`,
		`echo 2 "${IFS+'}'z}"`,
		`echo 3 "foo ${IFS+'bar} baz"`,
		`printf '%s\n' "4 foo ${IFS+"b   c"} baz"`,
		"",
	}, "\n")
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron("PATH="+defaultPathValue)),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(r, strings.NewReader(src), "posixexp2.sub"); err != nil {
		t.Fatal(err)
	}
	want := "1 }z\n2 ''z}\n3 foo 'bar baz\n4 foo b   c baz\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s", want, got, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestRunKeepsBashConditionalAfterRuntimePosix(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"set -o posix",
		"empty=",
		"if [[ $empty -gt 0 ]]; then echo bad; fi",
		"if [[ 1 -gt 0 ]]; then echo ok; fi",
		"",
	}, "\n")
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron("PATH="+defaultPathValue)),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(r, strings.NewReader(src), "new-exp2.sub"); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "ok\n"; got != want {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s", want, got, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestRunQuotedEmptyHeredocEOFKeepsBody(t *testing.T) {
	t.Parallel()
	src := "cat <<''\nhi\nthere\n''\n"
	var stdout, stderr bytes.Buffer
	parser := syntax.NewParser(
		syntax.Variant(syntax.LangBash),
		syntax.HeredocEOFWarning(func(startLine, eofLine int, stop string) {
			stderr.WriteString("heredoc.tests: line 4: warning: here-document at line 1 delimited by end-of-file (wanted `')\n")
		}),
	)
	file, err := parser.Parse(strings.NewReader(src), "heredoc.tests")
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron("PATH=/no-host-tools")),
		interp.ExecHandlers(coreutilsshell.Handler()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	runErr := r.Run(context.Background(), file)
	if got, want := stdout.String(), "hi\nthere\n''\n"; got != want {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s\nrunErr:%v", want, got, stderr.String(), runErr)
	}
	wantErr := "heredoc.tests: line 4: warning: here-document at line 1 delimited by end-of-file (wanted `')\n"
	if got := stderr.String(); got != wantErr {
		t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q\nrunErr:%v", wantErr, got, runErr)
	}
	if runErr != nil {
		t.Fatalf("run error: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout.String(), stderr.String())
	}
}

func TestInProcessCoreutilsTouchHonorsVirtualUmask(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "baseline")
	f, err := os.OpenFile(baseline, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	baseInfo, err := os.Stat(baseline)
	if err != nil {
		t.Fatal(err)
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(
		"touch default; umask 077; touch restricted; umask 006; touch tp5"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	runner, err := interp.New(
		interp.Dir(dir),
		interp.StdIO(nil, io.Discard, &stderr),
		interp.ExecHandlers(coreutilsshell.Handler()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v, stderr=%q", err, stderr.String())
	}
	for name, want := range map[string]os.FileMode{
		"default":    baseInfo.Mode().Perm(),
		"restricted": 0o600,
		"tp5":        0o660,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode=%#o, want %#o", name, got, want)
		}
	}
}

func TestRunBadSubstDollarParamRecovery(t *testing.T) {
	src := "set -e\ntrap 'echo $?' EXIT\necho ${$NO_SUCH_VAR}\n"
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	defer func() {
		os.Stderr = oldStderr
		readStderr.Close()
	}()
	err = run(r, strings.NewReader(src), "./errors2.sub")
	writeStderr.Close()
	globalStderr, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil {
		t.Fatal("expected recovered parse error")
	}
	if want := "1\n"; stdout.String() != want {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s", want, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected runner stderr: %s", stderr.String())
	}
	wantErr := "./errors2.sub: line 3: ${$NO_SUCH_VAR}: bad substitution\n"
	if string(globalStderr) != wantErr {
		t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q", wantErr, string(globalStderr))
	}
}

func TestRunArithmeticParseErrorRecoveryContinues(t *testing.T) {
	src := "echo $((a b))\n(( x=9 y=41 ))\necho after\n"
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	defer func() {
		os.Stderr = oldStderr
		readStderr.Close()
	}()
	err = run(r, strings.NewReader(src), "./arith.tests")
	writeStderr.Close()
	globalStderr, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil {
		t.Fatal("expected recovered parse error")
	}
	if got, want := stdout.String(), "after\n"; got != want {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s", want, got, stderr.String())
	}
	wantErr := "./arith.tests: line 1: a b: arithmetic syntax error in expression (error token is \"b\")\n" +
		"./arith.tests: line 2: ((: x=9 y=41 : arithmetic syntax error in expression (error token is \"y=41 \")\n"
	if string(globalStderr) != wantErr {
		t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q", wantErr, string(globalStderr))
	}
}

func TestRunNestedBadSubstRecoveryContinues(t *testing.T) {
	src := "c=\"\"\necho ${c//${$(($#-1))}/x/}\nset -- a b\nprintf '<%s>\\n' \"$@\"\n"
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	defer func() {
		os.Stderr = oldStderr
		readStderr.Close()
	}()
	err = run(r, strings.NewReader(src), "./new-exp.tests")
	writeStderr.Close()
	globalStderr, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	// A nested bad substitution (`${$(($#-1))}`) is a RUNTIME expansion error in
	// bash, not a parse error: bash prints "bad substitution" to stderr, the
	// offending command is abandoned, the script CONTINUES, and the final exit
	// status is the last command's (0 here). Verified identical to bash 5.3
	// (stdout "<a>\n<b>\n", exit 0, the bad-subst diagnostic on stderr). Earlier
	// the engine modeled this as a recovered *parse* error (non-nil run() return
	// + diagnostic on os.Stderr); the engine was made more bash-faithful (runtime
	// expansion error on the runner's own stderr, run() returns nil / exit 0).
	if err != nil {
		t.Fatalf("want nil (exit 0, bash-correct), got err: %v", err)
	}
	wantOut := "<a>\n<b>\n"
	if stdout.String() != wantOut {
		t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q\nstderr:\n%s", wantOut, stdout.String(), stderr.String())
	}
	wantErr := "./new-exp.tests: line 2: ${$(($#-1))}: bad substitution\n"
	if stderr.String() != wantErr {
		t.Fatalf("runner stderr mismatch\nwant:\n%q\ngot:\n%q", wantErr, stderr.String())
	}
	if len(globalStderr) != 0 {
		t.Fatalf("unexpected os.Stderr output: %q", string(globalStderr))
	}
}

// TestRunUnexpectedTokenAbort pins bash 5.3's fatal "syntax error near
// unexpected token `X'" abort for a malformed case (no subject word) and a
// loop reserved word used outside a loop: bash runs nothing on the offending
// line and exits 2, where our generic recovery used to run the line's prefix
// and continue. The guard subtest confirms a complete statement on an earlier
// line still runs first (bash reads-parses-executes one command at a time).
func TestRunUnexpectedTokenAbort(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		wantOut    string
		wantErr    string
		wantStatus interp.ExitStatus
	}{
		{
			name:       "case-missing-subject",
			src:        "case\nin esac\n",
			wantStatus: 2,
			wantErr: "./s: line 1: syntax error near unexpected token `newline'\n" +
				"./s: line 1: `case'\n",
		},
		{
			name:       "keyword-out-of-loop-via-cmdsub",
			src:        "$(echo f)$(echo or) i in a b c; do echo $i; done\necho status=$?\n",
			wantStatus: 2,
			wantErr: "./s: line 1: syntax error near unexpected token `do'\n" +
				"./s: line 1: `$(echo f)$(echo or) i in a b c; do echo $i; done'\n",
		},
		{
			name:       "close-paren-after-command",
			src:        "echo )(\necho should-not-run\n",
			wantStatus: 2,
			wantErr: "./s: line 1: syntax error near unexpected token `)'\n" +
				"./s: line 1: `echo )('\n",
		},
		{
			name:    "prior-line-statement-runs-first",
			src:     "echo hi\ncase\nin esac\n",
			wantOut: "hi\n",
			// Earlier-line `echo hi` runs, then the up-front abort is
			// declined and the case error falls to the recovery path.
			// Bash prints ONE diagnostic here -- "line 2: syntax error
			// near unexpected token `newline'" -- and stops; we still
			// report line 3 as well, and name `case' rather than the
			// newline. The exit status does match: `in` is a reserved
			// word, so recovery reports a syntax error (2) instead of
			// running it as a command, as bash never would.
			wantStatus: 2,
			wantErr: "./s: line 2: syntax error near unexpected token `case'\n" +
				"./s: line 2: `case'\n" +
				"./s: line 3: syntax error near unexpected token `in'\n" +
				"./s: line 3: `in esac'\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			r, err := interp.New(
				interp.StdIO(nil, &stdout, &stderr),
				interp.Env(expand.ListEnviron()),
				interp.WithBashCompatErrors(true),
			)
			if err != nil {
				t.Fatal(err)
			}
			oldStderr := os.Stderr
			readStderr, writeStderr, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = writeStderr
			defer func() {
				os.Stderr = oldStderr
				readStderr.Close()
			}()
			runErr := run(r, strings.NewReader(tc.src), "./s")
			writeStderr.Close()
			globalStderr, readErr := io.ReadAll(readStderr)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var es interp.ExitStatus
			if !errors.As(runErr, &es) || es != tc.wantStatus {
				t.Fatalf("want ExitStatus(%d), got %v", tc.wantStatus, runErr)
			}
			if stdout.String() != tc.wantOut {
				t.Fatalf("stdout mismatch\nwant:\n%q\ngot:\n%q", tc.wantOut, stdout.String())
			}
			if string(globalStderr) != tc.wantErr {
				t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q", tc.wantErr, string(globalStderr))
			}
		})
	}
}

// TestRunStrayCloseParenAborts pins bash 5.3's handling of array__005:
//
//	a=(
//	1
//	&
//	'2 3'
//	)
//	argv.py "${a[@]}"
//
// bash recovers from the mid-array `&` (a control operator) — reporting it and
// running the following `'2 3'` as a command (status 127) — but the stray `)`
// is a FATAL parse error: bash names it as a generic unexpected token and
// aborts the whole input (status 2), so the trailing `argv.py` line never runs.
// Our generic statement recovery used to rewrite the `)` to "`)` can only be
// used to close a subshell" and run the trailing line; this guards both the
// bash wording and the abort. Verified byte-for-byte against bash 5.3.
func TestRunStrayCloseParenAborts(t *testing.T) {
	src := "a=(\n1\n&\n'2 3'\n)\nargv.py \"${a[@]}\"\n"
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	defer func() {
		os.Stderr = oldStderr
		readStderr.Close()
	}()
	runErr := run(r, strings.NewReader(src), "./s")
	writeStderr.Close()
	globalStderr, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var es interp.ExitStatus
	if !errors.As(runErr, &es) || es != 2 {
		t.Fatalf("want ExitStatus(2) (fatal abort at `)`), got %v", runErr)
	}
	// argv.py never runs (aborted) — so stdout is empty and the runner's
	// stderr holds only the `'2 3'` command-not-found from the recovered line.
	if stdout.String() != "" {
		t.Fatalf("stdout should be empty (trailing line aborted), got %q", stdout.String())
	}
	wantRunnerErr := "./s: line 4: 2 3: command not found\n"
	if stderr.String() != wantRunnerErr {
		t.Fatalf("runner stderr mismatch\nwant:\n%q\ngot:\n%q", wantRunnerErr, stderr.String())
	}
	wantParseErr := "./s: line 3: syntax error near unexpected token `&'\n" +
		"./s: line 3: `&'\n" +
		"./s: line 5: syntax error near unexpected token `)'\n" +
		"./s: line 5: `)'\n"
	if string(globalStderr) != wantParseErr {
		t.Fatalf("parse-error stderr mismatch\nwant:\n%q\ngot:\n%q", wantParseErr, string(globalStderr))
	}
}

// TestRunBraceGroupEOFAbort pins bash 5.3's fatal "unclosed `{` command group
// at end of file" abort. A here-document delimited by EOF swallows the closing
// `}`, leaving the brace group opened on line 9 unterminated; bash warns about
// the here-document, then reports the unterminated group and exits 2 WITHOUT
// running the swallowed body. Our statement recovery used to re-parse and run
// that body instead, so this guards the up-front fatal classification.
func TestRunBraceGroupEOFAbort(t *testing.T) {
	src := "# c1\n# c2\n# c3\n# c4\n# c5\n# c6\n# c7\n# c8\nfun() {\n  cat << \"$@\"\nhi\n1 2\n}\nfun 1 2\n"
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(nil, &stdout, &stderr),
		interp.Env(expand.ListEnviron()),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	readStderr, writeStderr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writeStderr
	defer func() {
		os.Stderr = oldStderr
		readStderr.Close()
	}()
	runErr := run(r, strings.NewReader(src), "./s")
	writeStderr.Close()
	globalStderr, readErr := io.ReadAll(readStderr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var es interp.ExitStatus
	if !errors.As(runErr, &es) || es != 2 {
		t.Fatalf("want ExitStatus(2), got %v", runErr)
	}
	if stdout.String() != "" {
		t.Fatalf("want no stdout (body must not run), got %q", stdout.String())
	}
	wantErr := "./s: line 14: warning: here-document at line 10 delimited by end-of-file (wanted `$@')\n" +
		"./s: line 15: syntax error: unexpected end of file from `{' command on line 9\n"
	if string(globalStderr) != wantErr {
		t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q", wantErr, string(globalStderr))
	}
}

// TestRunBacktickComsubUnclosedQuote pins bash 5.3's quirk for a backtick
// command substitution with an unterminated quote in its body: bash reports a
// runtime "command substitution" error, the substitution expands to nothing,
// the enclosing `echo` still runs (one empty line), and the shell exits 0 — not
// the status-2 parse abort our eager parser would otherwise emit.
func TestRunBacktickComsubUnclosedQuote(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
	}{
		{"plain", "echo `echo \"`"},
		{"backslashes", "echo `echo \\\\\\\\\"`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origCommand := *command
			*command = tc.cmd
			t.Cleanup(func() { *command = origCommand })

			var stdout bytes.Buffer
			r, err := interp.New(
				interp.StdIO(nil, &stdout, io.Discard),
				interp.Env(expand.ListEnviron()),
				interp.CommandString(true),
				interp.WithBashCompatErrors(true),
			)
			if err != nil {
				t.Fatal(err)
			}
			oldStderr := os.Stderr
			readStderr, writeStderr, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = writeStderr
			defer func() {
				os.Stderr = oldStderr
				readStderr.Close()
			}()
			runErr := run(r, strings.NewReader(*command), "bash")
			writeStderr.Close()
			globalStderr, readErr := io.ReadAll(readStderr)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if runErr != nil {
				t.Fatalf("want nil (exit 0, bash-correct), got %v", runErr)
			}
			if stdout.String() != "\n" {
				t.Fatalf("want one empty line on stdout, got %q", stdout.String())
			}
			wantErr := "bash: command substitution: line 1: unexpected EOF while looking for matching `\"'\n"
			if string(globalStderr) != wantErr {
				t.Fatalf("stderr mismatch\nwant:\n%q\ngot:\n%q", wantErr, string(globalStderr))
			}
		})
	}
}

// runForcedInteractiveInput drives runForcedInteractiveExec with raw
// input bytes (including readline control keys) on a pipe replacing
// os.Stdin, returning the runner's stdout.
func runForcedInteractiveInput(t *testing.T, input string) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		pw.WriteString(input)
		pw.Close()
	}()
	oldStdin := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() { os.Stdin = oldStdin })

	var out bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &out, io.Discard), interp.Interactive(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := runForcedInteractiveExec(r); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestForcedInteractiveOperateAndGetNext(t *testing.T) {
	// Mirrors the last sections of bash's history4.sub: type a few
	// commands (one multi-line, one HISTIGNOREd), then C-p back three
	// entries and C-o twice. The recalled multi-line entry must rerun
	// whole, and each C-o must execute and fetch the next entry even
	// while HISTSIZE stifling shifts the list.
	t.Setenv("HISTSIZE", "6")
	t.Setenv("HISTFILE", "")
	t.Setenv("HISTIGNORE", "&:history*:fc*")
	out := runForcedInteractiveInput(t,
		"echo 0\necho 1\necho 2\necho \"(left\nmid\nright)\"\necho A\necho B\nhistory -w\n\x10\x10\x10\x0f\x0f\n")
	want := "0\n1\n2\n(left\nmid\nright)\nA\nB\n(left\nmid\nright)\nA\nB\n"
	if out != want {
		t.Errorf("operate-and-get-next:\n got: %q\nwant: %q", out, want)
	}
}

func TestForcedInteractiveReverseSearch(t *testing.T) {
	// Mirrors the first sections of history4.sub: load a HISTFILE whose
	// multi-line entry comes back as separate line entries, C-r search
	// for it, then C-o through the continuation lines and the next
	// entries. Clearing HISTFILE first must prevent the exit-time save
	// from clobbering the file.
	dir := t.TempDir()
	hf := filepath.Join(dir, "histfile")
	content := "echo 0\necho 1\necho 2\necho \"(left\nmid\nright)\"\necho A\necho B\nhistory -w\n"
	if err := os.WriteFile(hf, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HISTSIZE", "8")
	t.Setenv("HISTFILE", hf)
	t.Setenv("HISTIGNORE", "&:history*:fc*")
	out := runForcedInteractiveInput(t, "HISTFILE=\n\x12left\x0f\x0f\x0f\x0f\n")
	want := "(left\nmid\nright)\nA\nB\n"
	if out != want {
		t.Errorf("reverse search:\n got: %q\nwant: %q", out, want)
	}
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("history file clobbered at exit:\n got: %q\nwant: %q", string(data), content)
	}
}

func TestForcedInteractiveFcEditorOwnsFollowingPipeInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires ed and Unix pipe inheritance")
	}
	if _, err := exec.LookPath("ed"); err != nil {
		t.Skipf("ed not found: %v", err)
	}
	t.Setenv("HISTFILE", "")

	stdin, input, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	go func() {
		_, _ = input.WriteString("echo hello world\nfc -e ed\ns/world/goodbye/\nw\nq\n")
		_ = input.Close()
	}()

	oldStdin, oldStderr := os.Stdin, os.Stderr
	os.Stdin = stdin
	promptRead, promptWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = promptWrite
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, promptRead)
		close(drained)
	}()
	t.Cleanup(func() {
		os.Stdin = oldStdin
		os.Stderr = oldStderr
		_ = promptWrite.Close()
		<-drained
		_ = promptRead.Close()
	})

	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.StdIO(stdin, &stdout, &stderr),
		interp.Env(expand.ListEnviron("HOME="+t.TempDir(), "HISTFILE=/dev/null", "PATH=/bin:/usr/bin")),
		interp.Interactive(true),
		interp.WithPosixMode(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runForcedInteractiveExec(r); err != nil {
		t.Fatalf("forced interactive fc: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "hello goodbye\n") {
		t.Fatalf("stdout = %q, want edited command output", got)
	}
	if got := stderr.String(); got != "echo hello goodbye\n" {
		t.Fatalf("stderr = %q, want only fc's edited-command trace", got)
	}
}

func TestBashExecutionStringNotExported(t *testing.T) {
	if val, ok := os.LookupEnv("BASH_EXECUTION_STRING"); ok {
		t.Cleanup(func() {
			os.Setenv("BASH_EXECUTION_STRING", val)
		})
		os.Unsetenv("BASH_EXECUTION_STRING")
	}

	origCommand := *command
	*command = `echo "v=[$BASH_EXECUTION_STRING]"`
	t.Cleanup(func() {
		*command = origCommand
	})

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = pw
	t.Cleanup(func() {
		os.Stdout = oldStdout
	})

	r, err := newRunner()
	if err != nil {
		pw.Close()
		t.Fatal(err)
	}

	err = run(r, strings.NewReader(*command), "bash")
	pw.Close()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, pr); err != nil {
		t.Fatal(err)
	}

	got := strings.TrimSpace(buf.String())
	want := `v=[echo "v=[$BASH_EXECUTION_STRING]"]`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	var childEnv []string
	r2, err := interp.New(
		interp.StdIO(nil, io.Discard, io.Discard),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.CommandString(true),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if len(args) > 0 && args[0] == "capture-env" {
					interp.HandlerCtx(ctx).Env.Each(func(name string, vr expand.Variable) bool {
						if vr.Exported {
							childEnv = append(childEnv, name+"="+vr.String())
						}
						return true
					})
					return nil
				}
				return next(ctx, args)
			}
		}),
		interp.WithBashCompatErrors(true),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := run(r2, strings.NewReader("capture-env"), "bash"); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, line := range childEnv {
		if strings.HasPrefix(line, "BASH_EXECUTION_STRING=") {
			found = true
			break
		}
	}
	if found {
		t.Errorf("BASH_EXECUTION_STRING was exported to child env:\n%s", strings.Join(childEnv, "\n"))
	}
}

func TestRelocatePendingCommandFlag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain -c untouched", []string{"bash", "-c", "echo hi"}, []string{"bash", "-c", "echo hi"}},
		{"claude shape: -c -l", []string{"bash", "-c", "-l", "echo hi"}, []string{"bash", "-l", "-c", "echo hi"}},
		{"options after -c with value", []string{"bash", "-c", "-o", "pipefail", "echo hi"}, []string{"bash", "-o", "pipefail", "-c", "echo hi"}},
		{"cluster with c then option", []string{"bash", "-lc", "-e", "echo hi"}, []string{"bash", "-l", "-e", "-c", "echo hi"}},
		{"cluster reduces to nothing", []string{"bash", "-c", "-c", "echo hi"}, []string{"bash", "-c", "echo hi"}},
		{"no operand: -c lands last", []string{"bash", "-c", "-l"}, []string{"bash", "-l", "-c"}},
		{"double dash ends options", []string{"bash", "-c", "-l", "--", "-weird"}, []string{"bash", "-l", "-c", "--", "-weird"}},
		{"value cluster keeps value", []string{"bash", "-c", "-eo", "pipefail", "echo hi"}, []string{"bash", "-eo", "pipefail", "-c", "echo hi"}},
		{"no -c untouched", []string{"bash", "-l", "script.sh", "-c"}, []string{"bash", "-l", "script.sh", "-c"}},
		{"script operand stops scan", []string{"bash", "script.sh", "-c", "arg"}, []string{"bash", "script.sh", "-c", "arg"}},
		{"args after command string", []string{"bash", "-c", "-l", "echo $0", "name", "a1"}, []string{"bash", "-l", "-c", "echo $0", "name", "a1"}},
	}
	for _, tc := range cases {
		got := relocatePendingCommandFlag(append([]string{}, tc.in...))
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
				break
			}
		}
	}
}

// --- strict POSIX mode is engaged by argv[0]=="sh" only ---------------------

// withStrictPosixEnv isolates a newRunner() call from the ambient shell: it
// swaps os.Args[0], resets the flag globals newRunner() consults, and clears
// the env vars that would otherwise inject set-options of their own.
func withStrictPosixEnv(t *testing.T, argv0 string, posixFlag bool) {
	t.Helper()
	oldArgs := os.Args
	oldPosix := *posix
	oldFlags := flag.CommandLine
	t.Cleanup(func() {
		os.Args = oldArgs
		*posix = oldPosix
		flag.CommandLine = oldFlags
	})
	flag.CommandLine = flag.NewFlagSet(argv0, flag.ExitOnError)
	os.Args = []string{argv0}
	*posix = posixFlag
	t.Setenv("SHELLOPTS", "")
	t.Setenv("BASHOPTS", "")
	unsetTestEnv(t, "POSIXLY_CORRECT")
	unsetTestEnv(t, "POSIX_PEDANTIC")
}

func unsetTestEnv(t *testing.T, name string) {
	t.Helper()
	value, present := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestEffectiveStartupPosix(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		envName  string
		envValue string
		want     bool
	}{
		{name: "plain bash", argv: []string{"bash"}},
		{name: "long option", argv: []string{"bash", "--posix"}, want: true},
		{name: "set option", argv: []string{"bash", "-o", "posix"}, want: true},
		{name: "on then off", argv: []string{"bash", "-o", "posix", "-bashy-plus-o", "posix"}},
		{name: "off then on", argv: []string{"bash", "-bashy-plus-o", "posix", "-o", "posix"}, want: true},
		{name: "shellopts", argv: []string{"bash", "-bashy-plus-o", "posix"}, envName: "SHELLOPTS", envValue: "braceexpand:posix", want: true},
		{name: "correct empty", argv: []string{"bash", "-bashy-plus-o", "posix"}, envName: "POSIXLY_CORRECT", want: true},
		{name: "correct value", argv: []string{"bash"}, envName: "POSIXLY_CORRECT", envValue: "no", want: true},
		{name: "pedantic empty", argv: []string{"bash"}, envName: "POSIX_PEDANTIC", want: true},
		{name: "pedantic value", argv: []string{"bash"}, envName: "POSIX_PEDANTIC", envValue: "no", want: true},
		{name: "invoked sh", argv: []string{"/bin/sh", "-bashy-plus-o", "posix"}, want: true},
		{name: "invoked bashy", argv: []string{"bashy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldArgs, oldPosix := os.Args, *posix
			t.Cleanup(func() { os.Args, *posix = oldArgs, oldPosix })
			os.Args, *posix = tc.argv, false
			unsetTestEnv(t, "POSIXLY_CORRECT")
			unsetTestEnv(t, "POSIX_PEDANTIC")
			t.Setenv("SHELLOPTS", "")
			if tc.envName != "" {
				t.Setenv(tc.envName, tc.envValue)
			}
			if got := effectiveStartupPosix(); got != tc.want {
				t.Fatalf("effectiveStartupPosix()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestPosixPedanticCreatesCorrectVariable(t *testing.T) {
	withStrictPosixEnv(t, "bash", false)
	t.Setenv("POSIX_PEDANTIC", "")
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Env.Get("POSIXLY_CORRECT").String(); got != "y" {
		t.Fatalf("POSIXLY_CORRECT=%q, want y", got)
	}
	if got := r.LangVariant(); got != syntax.LangPOSIX {
		t.Fatalf("LangVariant=%v, want POSIX", got)
	}
}

func TestEmptyPosixlyCorrectIsPreservedAndEnablesMode(t *testing.T) {
	withStrictPosixEnv(t, "bash", false)
	t.Setenv("POSIXLY_CORRECT", "")
	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Env.Get("POSIXLY_CORRECT").String(); got != "" {
		t.Fatalf("POSIXLY_CORRECT=%q, want inherited empty value", got)
	}
	if got := r.LangVariant(); got != syntax.LangPOSIX {
		t.Fatalf("LangVariant=%v, want POSIX", got)
	}
}

// runStrictProbe runs a script whose only non-conformance is an assignment
// error in a command-word prefix (`a=b true` where `a` is readonly). POSIX
// makes that fatal for a non-interactive shell; bash 5.3 does not. It needs no
// external command and no PATH, so it is a hermetic strict-mode probe.
func runStrictProbe(t *testing.T) (exited bool, err error) {
	t.Helper()
	const script = "readonly a=a\na=b true\n:\n"
	f, perr := syntax.NewParser().Parse(strings.NewReader(script), "")
	if perr != nil {
		t.Fatal(perr)
	}
	r, nerr := newRunner()
	if nerr != nil {
		t.Fatal(nerr)
	}
	if oerr := interp.StdIO(nil, io.Discard, io.Discard)(r); oerr != nil {
		t.Fatal(oerr)
	}
	err = r.Run(context.Background(), f)
	return r.Exited(), err
}

func TestStrictPosixEngagedByArgv0Sh(t *testing.T) {
	// Every spelling of "invoked as sh" bash honors: plain, login (leading
	// '-'), and an absolute path.
	argv0s := []string{"sh", "-sh", "/bin/sh"}
	if runtime.GOOS == "windows" {
		argv0s = append(argv0s, "sh.exe", "-sh.exe", `C:\bin\sh.exe`)
	}
	for _, argv0 := range argv0s {
		t.Run(argv0, func(t *testing.T) {
			withStrictPosixEnv(t, argv0, true)
			exited, err := runStrictProbe(t)
			if err == nil {
				t.Fatalf("argv0=%q: assignment error on a readonly var was not fatal; strict POSIX mode did not engage", argv0)
			}
			var status interp.ExitStatus
			if errors.As(err, &status) && status == 0 {
				t.Fatalf("argv0=%q: runner exited 0, want nonzero", argv0)
			}
			if !exited {
				t.Fatalf("argv0=%q: runner did not report Exited() after a fatal assignment error", argv0)
			}
		})
	}
}

func TestBashDropinShModeKeepsEchoWithEmptyPath(t *testing.T) {
	withStrictPosixEnv(t, "sh", true)
	old := BashDropinShMode
	BashDropinShMode = true
	t.Cleanup(func() { BashDropinShMode = old })
	if _, err := runStrictProbe(t); err != nil {
		t.Fatalf("pure Bash drop-in sh unexpectedly used strict POSIX: %v", err)
	}
	for _, source := range []string{"PATH=; echo this-is-sh", "unset PATH; echo this-is-sh"} {
		file, err := syntax.NewParser().Parse(strings.NewReader(source), "")
		if err != nil {
			t.Fatal(err)
		}
		r, err := newRunner()
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := interp.StdIO(nil, &output, io.Discard)(r); err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil || output.String() != "this-is-sh\n" {
			t.Fatalf("%q: output=%q status=%v", source, output.String(), err)
		}
	}
}

func TestStrictPosixNotEngagedByPosixFlag(t *testing.T) {
	// `bash --posix` / `-o posix` / SHELLOPTS=posix / POSIXLY_CORRECT must stay
	// byte-identical to bash 5.3, which survives the assignment error.
	for _, argv0 := range []string{"bash", "rbash", "bashy"} {
		t.Run(argv0, func(t *testing.T) {
			withStrictPosixEnv(t, argv0, true)
			if _, err := runStrictProbe(t); err != nil {
				t.Fatalf("argv0=%q with posix mode: want completion exit 0, got %v (strict mode leaked past argv0==sh)", argv0, err)
			}
		})
	}
}

func TestPosixReadonlyPrefixAssignmentStopsPhysicalLine(t *testing.T) {
	const script = "readonly a=a\na=b true; printf 'same-line\\n'\nprintf 'next-line\\n'\n"
	for _, posixMode := range []bool{false, true} {
		for _, argv0 := range []string{"bash", "rbash", "bashy"} {
			t.Run(fmt.Sprintf("%s/posix=%t", argv0, posixMode), func(t *testing.T) {
				withStrictPosixEnv(t, argv0, posixMode)
				file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
				if err != nil {
					t.Fatal(err)
				}
				r, err := newRunner()
				if err != nil {
					t.Fatal(err)
				}
				var stdout bytes.Buffer
				if err := interp.StdIO(nil, &stdout, io.Discard)(r); err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), file); err != nil {
					t.Fatalf("Run error = %v, want next physical line to complete", err)
				}
				want := "same-line\nnext-line\n"
				if posixMode {
					want = "next-line\n"
				}
				if got := stdout.String(); got != want {
					t.Fatalf("stdout = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestStrictPosixNotEngagedWithoutPosix(t *testing.T) {
	for _, argv0 := range []string{"bash", "rbash"} {
		t.Run(argv0, func(t *testing.T) {
			withStrictPosixEnv(t, argv0, false)
			if _, err := runStrictProbe(t); err != nil {
				t.Fatalf("argv0=%q plain: want completion exit 0, got %v", argv0, err)
			}
		})
	}
}

// --- Claude Code Bash-tool `-c -l` invocation guard -------------------------

// TestClaudeCodeWrapperExecutes guards the FULL Claude Code Bash-tool
// invocation end-to-end, not just the argv rewrite.
//
// Claude Code (CLAUDE_CODE_SHELL) runs every command as
//
//	bash -c -l '<wrapper>'
//
// where <wrapper> is a fixed preamble that wraps the real command:
//
//	shopt -u extglob 2>/dev/null || true \
//	  && { \builtin unalias -- '<x>'; \builtin unset -f -- '<x>'; } >/dev/null 2>&1 || true \
//	  && eval '<command>' < /dev/null \
//	  && pwd -P >| <cwd-tracking-file>
//
// TestRelocatePendingCommandFlag proves `-c` binds to the wrapper string (not
// to `-l`). That is necessary but NOT sufficient: it never runs the wrapper,
// so it cannot catch a regression where the wrapper parses/binds fine but then
// fails to execute — the wrapped command never runs, or its stdout is lost, or
// the `>|` cwd redirect fails. Both failure modes have been observed live:
//
//	(1) `-c` bound to `-l`  -> `-l: command not found`, exit 127 (argv bug)
//	(2) wrapper ran but the wrapped command's stdout was swallowed / a
//	    redirect inside the wrapper failed (exec/IO bug)
//
// This test fails on mode (2); TestRelocatePendingCommandFlag fails on mode
// (1). Together they pin the whole `-c -l` contract. Hermetic — no external
// process, mirrors runStrictProbe's parse+newRunner+StdIO pattern.
func TestClaudeCodeWrapperExecutes(t *testing.T) {
	// Isolate from any flag state a sibling test in this package left behind
	// (this package parses into process-global flag vars).
	oldCommand, oldLogin, oldPosix, oldForceI := *command, *login, *posix, *forceI
	*command, *login, *posix, *forceI = "", true, false, false // login=true mirrors `-l`
	t.Cleanup(func() {
		*command, *login, *posix, *forceI = oldCommand, oldLogin, oldPosix, oldForceI
	})

	const marker = "CLAUDE_CODE_WRAPPER_OK"
	cwdFile := filepath.Join(t.TempDir(), "cwd")

	// The exact shape Claude Code's Bash tool emits, with the wrapped command
	// replaced by a marker echo and the cwd-tracking redirect pointed at a
	// temp file. Keep this string faithful to the real preamble.
	wrapper := "shopt -u extglob 2>/dev/null || true && " +
		"{ \\builtin unalias -- 'x'; \\builtin unset -f -- 'x'; } >/dev/null 2>&1 || true && " +
		"eval 'echo " + marker + "' < /dev/null && " +
		"pwd -P >| " + cwdFile

	f, err := syntax.NewParser().Parse(strings.NewReader(wrapper), "")
	if err != nil {
		t.Fatalf("Claude Code wrapper failed to PARSE: %v", err)
	}

	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := interp.StdIO(nil, &out, &out)(r); err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("Claude Code wrapper did not run to a clean exit: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), marker) {
		t.Fatalf("wrapped command stdout was lost: want %q in output, got:\n%s", marker, out.String())
	}
	if _, err := os.Stat(cwdFile); err != nil {
		t.Fatalf("`pwd -P >| <file>` cwd redirect did not write %s: %v", cwdFile, err)
	}
}

func TestStrictPosixPolicyRejectsBuiltinExtensions(t *testing.T) {
	for _, src := range []string{
		"[[ -n x ]]\n",
		"local value=x\n",
		"source ./library.sh\n",
		"set -o pipefail\n",
	} {
		file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(src), "policy.sh")
		if err != nil {
			t.Fatalf("test input should reach policy layer: %q: %v", src, err)
		}
		if err := strictPosixPolicyError(file, "policy.sh"); err == nil {
			t.Fatalf("strict POSIX policy accepted %q", src)
		}
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader("printf '%s\\n' ok\n"), "portable.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictPosixPolicyError(file, "portable.sh"); err != nil {
		t.Fatalf("strict POSIX policy rejected portable input: %v", err)
	}
}
