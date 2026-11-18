package interp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// S88 suite-free fc reducer.
//
// The Profile D S82 journal records twenty non-PASS fc identities
// (FAIL 4, 5, 7, 10, 11, 17-19, 21, 22, 26, 28, 29, 31-34 and
// UNRESOLVED 8, 27, 30) whose transcripts drive an interactive `fc`
// editor session. These tests separate the two possible owners without
// touching the licensed suite:
//
//   - Shell-owned contract: when the editor transcript reaches the
//     editor - the shell hands its own stdin to the spawned editor,
//     runs it on the temp file holding the selected history entries,
//     echoes the edited text, executes it, and records it - the POSIX
//     Issue 7 fc requirements hold. TestFcS88EditorDrivenFromShellStdin
//     proves the product side with a real ed(1) driven from the
//     runner's stdin.
//
//   - Misroute signature: when the same transcript lines instead
//     arrive on the shell's command input after the editor has already
//     exited, a fully conformant shell must execute them as commands,
//     fail with 127, and record them in history, which then corrupts
//     every later history-consuming assertion. The interpreter does the
//     right thing after the misroute; the non-TTY interactive front end
//     used to cause it by reading ahead of the command being executed.
//     TestFcS88MisroutedEditorTranscriptSignature reproduces exactly
//     the journal's FAIL-then-UNRESOLVED signature with no fc defect
//     involved.

func newFcReducerRunner(t *testing.T, stdin *os.File) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	histReset()
	t.Cleanup(histReset)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := New(
		Dir(dir),
		Env(expand.ListEnviron("HISTFILE=/dev/null", "HOME="+dir, "PATH=/bin:/usr/bin")),
		StdIO(stdin, &stdout, &stderr),
		Interactive(true),
		WithPosixMode(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.EnableInteractiveHistory()
	return r, &stdout, &stderr
}

// runFcReducerLine records one interactive line in history and runs it,
// returning its exit status like an interactive loop iteration would.
func runFcReducerLine(t *testing.T, r *Runner, src string) uint8 {
	t.Helper()
	r.RecordInteractiveHistory(src)
	file, err := syntax.NewParser(
		syntax.Variant(syntax.LangBash),
		syntax.PosixMode(true),
	).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		if code, ok := IsExitStatus(err); ok {
			return code
		}
		t.Fatalf("run %q: %v", src, err)
	}
	return 0
}

// TestFcS88EditorDrivenFromShellStdin is the shell-owned half of the
// reducer: the ed transcript sits on the shell's own stdin, exactly
// where an interactive certification run leaves it, and must be
// consumed by the editor that fc spawns, not by the shell.
func TestFcS88EditorDrivenFromShellStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires ed(1)")
	}
	if _, err := exec.LookPath("ed"); err != nil {
		t.Skipf("ed not found: %v", err)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	// The classic Profile D transcript shape: a substitution, a write,
	// and a quit, addressed to the editor.
	if _, err := pw.WriteString("s/world/goodbye/\nw\nq\n"); err != nil {
		t.Fatal(err)
	}
	pw.Close()

	r, stdout, stderr := newFcReducerRunner(t, pr)
	if code := runFcReducerLine(t, r, "echo hello world"); code != 0 {
		t.Fatalf("seed command status = %d, want 0", code)
	}
	if code := runFcReducerLine(t, r, "fc -e ed"); code != 0 {
		t.Fatalf("fc -e ed status = %d, want 0, stderr %q", code, stderr.String())
	}

	// ed announces 17 bytes read ("echo hello world\n") and 19 written
	// ("echo hello goodbye\n"); the edited command must then run.
	wantOut := "hello world\n17\n19\nhello goodbye\n"
	if got := stdout.String(); got != wantOut {
		t.Fatalf("stdout = %q, want %q", got, wantOut)
	}
	// POSIX: fc echoes the commands to be re-executed; the diagnostic
	// stream must carry only the edited line, no shell errors from the
	// transcript.
	if got, want := stderr.String(), "echo hello goodbye\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}

	// History: the fc invocation itself is dropped, the edited command
	// is recorded, and the transcript lines never appear.
	stdout.Reset()
	if code := runFcReducerLine(t, r, "fc -ln"); code != 0 {
		t.Fatalf("fc -ln status = %d, want 0", code)
	}
	wantHist := "\techo hello world\n\techo hello goodbye\n"
	if got := stdout.String(); got != wantHist {
		t.Fatalf("history = %q, want %q", got, wantHist)
	}
}

// TestFcS88MisroutedEditorTranscriptSignature is the diagnostic half of
// the reducer: the same transcript delivered as shell command input
// (the editor having exited without consuming it) must yield the S82
// journal signature - 127s for the ed commands, followed by history
// listings that no longer match any expectation formed before the
// misroute. A shell doing anything else here would be broken.
func TestFcS88MisroutedEditorTranscriptSignature(t *testing.T) {
	r, stdout, stderr := newFcReducerRunner(t, nil)
	if code := runFcReducerLine(t, r, "echo hello world"); code != 0 {
		t.Fatalf("seed command status = %d, want 0", code)
	}
	// `true` stands in for an editor that exits without reading the
	// transcript: fc must still run the unedited selection.
	if code := runFcReducerLine(t, r, "fc -e true"); code != 0 {
		t.Fatalf("fc -e true status = %d, want 0", code)
	}
	if got, want := stdout.String(), "hello world\nhello world\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "echo hello world\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}

	// The stranded transcript now arrives as commands: the FAIL half.
	stderr.Reset()
	if code := runFcReducerLine(t, r, "s/world/goodbye/"); code != 127 {
		t.Fatalf("s/world/goodbye/ status = %d, want 127", code)
	}
	if code := runFcReducerLine(t, r, "q"); code != 127 {
		t.Fatalf("q status = %d, want 127", code)
	}
	diags := stderr.String()
	if !strings.Contains(diags, "s/world/goodbye") || !strings.Contains(diags, "q") {
		t.Fatalf("stderr = %q, want diagnostics naming both misrouted lines", diags)
	}

	// The UNRESOLVED half: history is now polluted, so any later
	// fc listing or numeric selection diverges from the fixture's
	// pre-misroute expectations even though every entry is correct.
	stdout.Reset()
	if code := runFcReducerLine(t, r, "fc -ln"); code != 0 {
		t.Fatalf("fc -ln status = %d, want 0", code)
	}
	wantHist := "\techo hello world\n" +
		"\techo hello world\n" +
		"\ts/world/goodbye/\n" +
		"\tq\n"
	if got := stdout.String(); got != wantHist {
		t.Fatalf("history = %q, want %q", got, wantHist)
	}
}
