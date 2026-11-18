package interp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBuildHistTimeline(t *testing.T) {
	src := `echo one
# a comment line
for x in one two three
do
	:
done
cat <<!
body1
body2
!
echo a; echo b
echo "(left
mid
right)"
`
	groups, _, _ := buildHistTimeline([]byte(src))
	want := []string{
		"echo one",
		"# a comment line",
		"for x in one two three; do \t:; done",
		"cat <<!\nbody1\nbody2\n!\n",
		"echo a; echo b",
		"echo \"(left\nmid\nright)\"",
	}
	if len(groups) != len(want) {
		t.Fatalf("got %d groups, want %d: %#v", len(groups), len(want), groups)
	}
	for i, w := range want {
		if groups[i].text != w {
			t.Errorf("group %d:\n got: %q\nwant: %q", i, groups[i].text, w)
		}
	}
}

func TestReadHistFileTimestamps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "histfile")
	content := "#1\necho below zero\n#2\n\n\necho one\n#3\necho two\n\n\n#4\necho three\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := readHistFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"echo below zero",
		"echo one",
		"echo two\n\n",
		"echo three",
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %#v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry %d:\n got: %q\nwant: %q", i, entries[i], w)
		}
	}

	// Without timestamps, every line is one entry.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("ls\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, nlines, err := readHistFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0] != "ls" || entries[1] != "echo hi" || nlines != 2 {
		t.Fatalf("plain file: got %#v (lines=%d)", entries, nlines)
	}
}

// runHistScript writes src to a script file and interprets it the way
// cmd/bashy would, returning combined output.
func runHistScript(t *testing.T, src string) string {
	t.Helper()
	histReset()
	t.Cleanup(histReset)
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(src), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r, err := New(Dir(dir), StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Logf("run error: %v", err)
	}
	return buf.String()
}

func TestHistoryBuiltinRecording(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
set -o history
echo start
echo next
history
`)
	want := "start\nnext\n    1  echo start\n    2  echo next\n    3  history\n"
	if out != want {
		t.Errorf("recording:\n got: %q\nwant: %q", out, want)
	}
}

func TestHistoryBuiltinDeleteAndStifle(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
HISTSIZE=4
set -o history
echo 1
echo 2
echo 3
echo 4
echo 5
history -d -1
history
history -d 99
`)
	// Note: `history -d -1` is itself recorded first, so it deletes its
	// own entry (matching bash), and the later `history` line is
	// recorded before it lists.
	want := "1\n2\n3\n4\n5\n" +
		"    3  echo 3\n    4  echo 4\n    5  echo 5\n" +
		"    6  history\nhistory: 99: history position out of range\n"
	if out != want {
		t.Errorf("delete/stifle:\n got: %q\nwant: %q", out, want)
	}
}

func TestHistoryBuiltinIgnoreAndFc(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
HISTIGNORE='&:history*:fc*'
set -o history
echo aa bb
fc -l
fc -nl -1
fc -s aa=cc
`)
	want := "aa bb\n" +
		"1\t echo aa bb\n" +
		"\t echo aa bb\n" +
		"echo cc bb\ncc bb\n"
	if out != want {
		t.Errorf("histignore/fc:\n got: %q\nwant: %q", out, want)
	}
}

func TestInteractiveHistoryFeedsFc(t *testing.T) {
	histReset()
	t.Cleanup(histReset)

	var out bytes.Buffer
	r, err := New(
		Env(expand.ListEnviron("HISTFILE=/dev/null", "HOME="+t.TempDir())),
		StdIO(nil, &out, &out),
		Interactive(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.EnableInteractiveHistory()
	runLine := func(src string) {
		t.Helper()
		r.RecordInteractiveHistory(src)
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil {
			t.Fatal(err)
		}
	}

	runLine("echo marker")
	runLine("fc -l")
	want := "marker\n1\t echo marker\n"
	if out.String() != want {
		t.Fatalf("interactive fc -l:\n got: %q\nwant: %q", out.String(), want)
	}
}

func newInteractiveHistoryRunner(t *testing.T, posix bool, handler ExecHandlerFunc) (*Runner, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	histReset()
	t.Cleanup(histReset)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	opts := []RunnerOption{
		Dir(dir),
		Env(expand.ListEnviron("HISTFILE=/dev/null", "HOME="+dir)),
		StdIO(nil, &stdout, &stderr),
		Interactive(true),
	}
	if posix {
		opts = append(opts, WithPosixMode(true))
	}
	if handler != nil {
		opts = append(opts, ExecHandler(handler))
	}
	r, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	r.EnableInteractiveHistory()
	return r, &stdout, &stderr, dir
}

func runInteractiveHistoryLine(t *testing.T, r *Runner, posix bool, src string) {
	t.Helper()
	r.RecordInteractiveHistory(src)
	file, err := syntax.NewParser(
		syntax.Variant(syntax.LangBash),
		syntax.PosixMode(posix),
	).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("run %q: %v", src, err)
	}
}

func TestFcIssue7InteractiveListingDefaultsAndRanges(t *testing.T) {
	t.Run("default sixteen without numbers", func(t *testing.T) {
		r, stdout, _, _ := newInteractiveHistoryRunner(t, true, nil)
		for i := 1; i <= 18; i++ {
			runInteractiveHistoryLine(t, r, true, fmt.Sprintf(": command-%02d", i))
		}
		runInteractiveHistoryLine(t, r, true, "fc -ln")

		var want strings.Builder
		for i := 3; i <= 18; i++ {
			fmt.Fprintf(&want, "\t: command-%02d\n", i)
		}
		if got := stdout.String(); got != want.String() {
			t.Fatalf("fc -ln output = %q, want %q", got, want.String())
		}
	})

	for _, tc := range []struct {
		name string
		cmd  string
		want string
	}{
		{"first defaults through newest", "fc -l 2", "2\t: two\n3\t: three\n"},
		{"forward range", "fc -l 1 3", "1\t: one\n2\t: two\n3\t: three\n"},
		{"reverse operands", "fc -l 3 1", "3\t: three\n2\t: two\n1\t: one\n"},
		{"reverse option", "fc -lr 1 3", "3\t: three\n2\t: two\n1\t: one\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, stdout, _, _ := newInteractiveHistoryRunner(t, true, nil)
			for _, command := range []string{": one", ": two", ": three"} {
				runInteractiveHistoryLine(t, r, true, command)
			}
			runInteractiveHistoryLine(t, r, true, tc.cmd)
			if got := stdout.String(); got != tc.want {
				t.Fatalf("%s output = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestFcIssue7InteractiveSubstitutionRedirectsAndHistory(t *testing.T) {
	r, stdout, stderr, dir := newInteractiveHistoryRunner(t, true, nil)
	runInteractiveHistoryLine(t, r, true, "printf 'before\\n'")
	runInteractiveHistoryLine(t, r, true, "fc -s before=after >result 2>trace")
	runInteractiveHistoryLine(t, r, true, "fc -ln 1 -1")

	result, err := os.ReadFile(filepath.Join(dir, "result"))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result), "after\n"; got != want {
		t.Fatalf("executed command output = %q, want %q", got, want)
	}
	if got, want := string(trace), "printf 'after\\n'\n"; got != want {
		t.Fatalf("fc diagnostic output = %q, want %q", got, want)
	}
	if got, want := stderr.String(), ""; got != want {
		t.Fatalf("unredirected stderr = %q, want empty", got)
	}
	wantOut := "before\n" +
		"\tprintf 'before\\n'\n" +
		"\tprintf 'after\\n'\n"
	if got := stdout.String(); got != wantOut {
		t.Fatalf("history output = %q, want %q", got, wantOut)
	}
}

func TestFcIssue7InteractiveSubstitutionSelectorContainingEquals(t *testing.T) {
	r, stdout, stderr, _ := newInteractiveHistoryRunner(t, true, nil)
	runInteractiveHistoryLine(t, r, true, "mode=value printf 'old selected\\n'")
	runInteractiveHistoryLine(t, r, true, "printf 'old latest\\n'")
	runInteractiveHistoryLine(t, r, true, "fc -s old=new mode=value")

	if got, want := stdout.String(), "old selected\nold latest\nnew selected\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "mode=value printf 'new selected\\n'\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestFcBashInteractiveMultipleSubstitutions(t *testing.T) {
	r, stdout, stderr, _ := newInteractiveHistoryRunner(t, false, nil)
	runInteractiveHistoryLine(t, r, false, "printf 'aa cc\\n'")
	runInteractiveHistoryLine(t, r, false, "fc -s aa=bb cc=dd")

	if got, want := stdout.String(), "aa cc\nbb dd\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "printf 'bb dd\\n'\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestFcIssue7InteractiveEditorSelectionRedirectsAndHistory(t *testing.T) {
	var editors []string
	handler := func(ctx context.Context, args []string) error {
		if args[0] != "explicit-editor" && args[0] != "variable-editor" && args[0] != "ed" {
			return NewExitStatus(127)
		}
		editors = append(editors, args[0])
		if _, err := io.WriteString(HandlerCtx(ctx).Stdout, "editor output\n"); err != nil {
			return err
		}
		return os.WriteFile(args[1], []byte("printf 'edited\\n'\n"), 0o600)
	}
	r, stdout, stderr, dir := newInteractiveHistoryRunner(t, true, handler)
	runInteractiveHistoryLine(t, r, true, "FCEDIT=variable-editor")
	runInteractiveHistoryLine(t, r, true, "printf 'original\\n'")
	runInteractiveHistoryLine(t, r, true, "fc -e explicit-editor -1 >result 2>trace")
	runInteractiveHistoryLine(t, r, true, "fc -ln 1 -1")

	if got, want := strings.Join(editors, ","), "explicit-editor"; got != want {
		t.Fatalf("editors = %q, want %q", got, want)
	}
	result, err := os.ReadFile(filepath.Join(dir, "result"))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(filepath.Join(dir, "trace"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result), "editor output\nedited\n"; got != want {
		t.Fatalf("redirected stdout = %q, want %q", got, want)
	}
	if got, want := string(trace), "printf 'edited\\n'\n"; got != want {
		t.Fatalf("redirected stderr = %q, want %q", got, want)
	}
	if got, want := stderr.String(), ""; got != want {
		t.Fatalf("unredirected stderr = %q, want empty", got)
	}
	wantOut := "original\n" +
		"\tFCEDIT=variable-editor\n" +
		"\tprintf 'original\\n'\n" +
		"\tprintf 'edited\\n'\n"
	if got := stdout.String(); got != wantOut {
		t.Fatalf("history output = %q, want %q", got, wantOut)
	}
}

func TestFcIssue7InteractiveEditorSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup string
		fc    string
		want  string
	}{
		{
			name:  "option before variable",
			setup: "FCEDIT=variable-editor",
			fc:    "fc -e explicit-editor",
			want:  "explicit-editor",
		},
		{
			name:  "variable before default",
			setup: "FCEDIT=variable-editor",
			fc:    "fc",
			want:  "variable-editor",
		},
		{
			name: "standard default",
			fc:   "fc",
			want: "ed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var editors []string
			handler := func(_ context.Context, args []string) error {
				if args[0] != "explicit-editor" && args[0] != "variable-editor" && args[0] != "ed" {
					return NewExitStatus(127)
				}
				editors = append(editors, args[0])
				return os.WriteFile(args[1], []byte(": edited\n"), 0o600)
			}
			r, _, _, _ := newInteractiveHistoryRunner(t, true, handler)
			if tc.setup != "" {
				runInteractiveHistoryLine(t, r, true, tc.setup)
			}
			runInteractiveHistoryLine(t, r, true, ": selected")
			runInteractiveHistoryLine(t, r, true, tc.fc)
			if got := strings.Join(editors, ","); got != tc.want {
				t.Fatalf("editors = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFcPosixEditorAndArgs(t *testing.T) {
	run := func(t *testing.T, src string, editors *[]string) string {
		t.Helper()
		histReset()
		t.Cleanup(histReset)
		dir := t.TempDir()
		path := filepath.Join(dir, "script.sh")
		if err := os.WriteFile(path, []byte(src), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), path)
		if err != nil {
			t.Fatal(err)
		}
		editorHandler := func(next ExecHandlerFunc) ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				switch args[0] {
				case "ed", "vi":
					*editors = append(*editors, args[0])
					return nil
				}
				return next(ctx, args)
			}
		}
		var buf bytes.Buffer
		r, err := New(Dir(dir), StdIO(nil, &buf, &buf), ExecHandlers(editorHandler))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil {
			t.Logf("run error: %v", err)
		}
		return buf.String()
	}

	for _, tt := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "non-posix editor fallback",
			src: `HISTFILE=/dev/null
unset FCEDIT EDITOR
set -o history
echo selected
fc
`,
			want: "vi",
		},
		{
			name: "posix editor fallback",
			src: `HISTFILE=/dev/null
unset FCEDIT EDITOR
set -o history
set -o posix
echo selected
fc
`,
			want: "ed",
		},
		{
			name: "posix ignores EDITOR fallback",
			src: `HISTFILE=/dev/null
unset FCEDIT
EDITOR=vi
set -o history
set -o posix
echo selected
fc
`,
			want: "ed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var editors []string
			run(t, tt.src, &editors)
			if len(editors) != 1 || editors[0] != tt.want {
				t.Fatalf("editors = %v, want [%s]", editors, tt.want)
			}
		})
	}

	var editors []string
	out := run(t, `HISTFILE=/dev/null
set -o history
echo base
fc -s echo extra
printf 'status:%s\n' "$?"
`, &editors)
	want := "base\necho base\nbase\nstatus:0\n"
	if out != want {
		t.Errorf("non-posix fc -s extra args:\n got: %q\nwant: %q", out, want)
	}

	editors = nil
	out = run(t, `HISTFILE=/dev/null
set -o history
set -o posix
echo base
fc -s echo extra
printf 'status:%s\n' "$?"
`, &editors)
	want = "base\nfc: too many arguments\nstatus:1\n"
	if out != want {
		t.Errorf("posix fc -s extra args:\n got: %q\nwant: %q", out, want)
	}

	editors = nil
	out = run(t, `HISTFILE=/dev/null
set -o history
set -o posix
echo base
fc -l echo echo extra
printf 'status:%s\n' "$?"
`, &editors)
	if !strings.Contains(out, "fc: too many arguments\n") || !strings.HasSuffix(out, "status:1\n") {
		t.Errorf("posix fc -l extra args:\n got: %q", out)
	}
}

func TestFcIssue7FormValidation(t *testing.T) {
	for _, src := range []string{
		"fc -l one two three",
		"fc -n one",
		"fc -ls one",
		"fc -se ed one",
		"fc -s one two",
		"fc -s a=b c=d one",
		"fc -e ed one two three",
	} {
		t.Run(strings.ReplaceAll(src, " ", "_"), func(t *testing.T) {
			out := runHistScript(t, "HISTFILE=/dev/null\nset -o history\nset -o posix\necho one\n"+src+"\nprintf 'status:%s\\n' \"$?\"\n")
			if !strings.Contains(out, "fc:") || !strings.HasSuffix(out, "status:1\n") {
				t.Fatalf("%s output = %q", src, out)
			}
		})
	}
}

func TestFcIssue7FirstSubstitutionAndMultilineListing(t *testing.T) {
	const src = `HISTFILE=/dev/null
set -o history
set -o posix
echo aa aa
fc -s aa=bb echo
`
	out := runHistScript(t, src)
	if want := "aa aa\necho bb aa\nbb aa\n"; out != want {
		t.Fatalf("first substitution output = %q, want %q", out, want)
	}

	histReset()
	t.Cleanup(histReset)
	shellHist.mu.Lock()
	shellHist.list = []string{"first line\nsecond line"}
	shellHist.base = 1
	shellHist.mu.Unlock()
	var buf bytes.Buffer
	r, err := New(WithPosixMode(true), StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	status := r.fcBuiltin(context.Background(), syntax.Pos{}, []string{"-ln", "1", "1"})
	if status.code != 0 || buf.String() != "\tfirst line\n\tsecond line\n" {
		t.Fatalf("multiline listing: status=%d output=%q", status.code, buf.String())
	}
}

func TestFcReverseEditRange(t *testing.T) {
	histReset()
	t.Cleanup(histReset)
	dir := t.TempDir()
	const src = `HISTFILE=/dev/null
set -o history
: one
: two
: three
fc -e capture -1 -3
`
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte(src), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), path)
	if err != nil {
		t.Fatal(err)
	}
	var edited string
	captureEditor := func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] != "capture" {
				return next(ctx, args)
			}
			if len(args) != 2 {
				t.Fatalf("capture args = %q, want editor and file", args)
			}
			data, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			edited = string(data)
			return nil
		}
	}
	r, err := New(Dir(dir), ExecHandlers(captureEditor))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if want := ": three\n: two\n: one\n"; edited != want {
		t.Fatalf("edited commands = %q, want %q", edited, want)
	}
}

// TestFcIssue7ListRangeSelectsNewestCommand pairs POSIX mode against bash mode
// over the same three-command history. POSIX Issue 7 treats every command
// number that `fc -l` displays as a valid first/last operand, so naming the
// newest one selects it -- and a first newer than last lists in reverse. Bash's
// fc_gethnum instead treats the newest selectable index as out of range and
// silently clamps it to the oldest entry. The bash-mode expectations below were
// captured from GNU Bash 5.3.15 running the same script, so they must not be
// "fixed" to match the POSIX column.
func TestFcIssue7ListRangeSelectsNewestCommand(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cmd       string
		wantPosix string
		// wantBash is real GNU Bash 5.3 output; bash pads with an extra
		// space for the omitted modified-command marker.
		wantBash string
	}{
		{
			name:      "reverse range from newest",
			cmd:       "fc -l 3 1",
			wantPosix: "3\t: three\n2\t: two\n1\t: one\n",
			wantBash:  "1\t : one\n",
		},
		{
			name:      "newest as both endpoints",
			cmd:       "fc -l 3 3",
			wantPosix: "3\t: three\n",
			wantBash:  "1\t : one\n2\t : two\n3\t : three\n",
		},
		{
			name:      "newest as lone operand",
			cmd:       "fc -l 3",
			wantPosix: "3\t: three\n",
			wantBash:  "1\t : one\n2\t : two\n3\t : three\n",
		},
		{
			// Ranges that stay below the newest command agree in both
			// modes; the divergence is confined to the newest number.
			name:      "reverse range below newest",
			cmd:       "fc -l 2 1",
			wantPosix: "2\t: two\n1\t: one\n",
			wantBash:  "2\t : two\n1\t : one\n",
		},
		{
			name:      "forward range",
			cmd:       "fc -l 1 3",
			wantPosix: "1\t: one\n2\t: two\n3\t: three\n",
			wantBash:  "1\t : one\n2\t : two\n3\t : three\n",
		},
		{
			// Out of range past the newest command still clamps in both
			// modes: 4 is not a displayed command number.
			name:      "past newest still clamps",
			cmd:       "fc -l 4 1",
			wantPosix: "1\t: one\n",
			wantBash:  "1\t : one\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := func(posix string) string {
				return "HISTFILE=/dev/null\nHISTIGNORE='fc*'\n" + posix +
					"set -o history\n: one\n: two\n: three\n" + tc.cmd + "\n"
			}
			if got := runHistScript(t, script("set -o posix\n")); got != tc.wantPosix {
				t.Errorf("posix %s:\n got: %q\nwant: %q", tc.cmd, got, tc.wantPosix)
			}
			if got := runHistScript(t, script("")); got != tc.wantBash {
				t.Errorf("bash %s:\n got: %q\nwant: %q", tc.cmd, got, tc.wantBash)
			}
		})
	}
}

func TestFcListPosixOmitsModifiedMarker(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
HISTIGNORE='fc*'
set -o posix
set -o history
: one
fc -l -1 -1
fc -nl -1 -1
`)
	if want := "1\t: one\n\t: one\n"; out != want {
		t.Fatalf("fc listing = %q, want %q", out, want)
	}
}

// TestFcS88PosixNoNumberListingBytes pins Bash's POSIX-mode output: -n
// suppresses the command number but retains the tab-delimited listing field.
// Both explicit and default ranges must therefore begin each command with a
// tab, including continuation lines covered above.
func TestFcS88PosixNoNumberListingBytes(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
HISTIGNORE='fc*'
set -o posix
set -o history
: public-alpha
: public-beta
fc -l -n 1 2
fc -l -n
`)
	if want := "\t: public-alpha\n\t: public-beta\n\t: public-alpha\n\t: public-beta\n"; out != want {
		t.Fatalf("fc -l -n bytes = %q, want %q", out, want)
	}
}

func TestFcIssue7ReexecutionReturnsCommandStatus(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
set -o history
set -o posix
false
fc -s false
printf 'status:%s\n' "$?"
`)
	if want := "false\nstatus:1\n"; out != want {
		t.Fatalf("fc -s status propagation = %q, want %q", out, want)
	}
}

func TestHistoryExpansionDesignators(t *testing.T) {
	out := runHistScript(t, `HISTFILE=/dev/null
set -o history
echo line one
set -H
!!
!e
`)
	// `!!` repeats the previous command (`set -H`); `!e` repeats the
	// last command starting with `e`. Each expansion is echoed to
	// stderr before running, and the designator must not fall through
	// to command lookup (`!!: command not found`).
	want := "line one\nset -H\necho line one\nline one\n"
	if out != want {
		t.Errorf("history expansion:\n got: %q\nwant: %q", out, want)
	}
}

func TestHistorySAndW(t *testing.T) {
	dir := t.TempDir()
	hf := filepath.Join(dir, "histfile")
	out := runHistScript(t, `HISTFILE=/dev/null
set -o history
history -s "echo stored"
history -p '!!'
history -w `+hf+`
`)
	want := "echo stored\n"
	if out != want {
		t.Errorf("history -s/-p:\n got: %q\nwant: %q", out, want)
	}
	data, err := os.ReadFile(hf)
	if err != nil {
		t.Fatal(err)
	}
	// The history/set lines themselves are recorded too; the stored
	// entry must be present.
	if !strings.Contains(string(data), "echo stored\n") {
		t.Errorf("history -w file missing entry: %q", string(data))
	}
}
