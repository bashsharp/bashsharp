// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// Sprint 216, story 536: Windows first-hour runtime behavior. These only run
// on a real Windows worker; the conversion logic behind them is covered
// platform-neutrally in story536_test.go and pathconv.

// runWindowsScript runs src with the runner's cwd set to dir and returns
// stdout+stderr.
func runWindowsScript(t *testing.T, dir, src string) string {
	t.Helper()
	file := parse(t, nil, src)
	var b bytes.Buffer
	r, err := interp.New(interp.Dir(dir), interp.StdIO(nil, &b, &b))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run error: %v\noutput: %q", err, b.String())
	}
	return b.String()
}

func TestPwdWindowsForm(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	got := runWindowsScript(t, tdir, "pwd -W\n")
	want := strings.ReplaceAll(tdir, `\`, "/") + "\n"
	if !strings.EqualFold(got, want) {
		t.Fatalf("pwd -W:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestCdDriveOperandPerDriveCwd(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	drive := filepath.VolumeName(tdir) // e.g. "C:"
	if len(drive) != 2 {
		t.Skipf("temp dir %q has no drive volume", tdir)
	}
	// cd onto the drive records the cwd; cd / moves to the drive root; a
	// bare drive operand returns to the recorded cwd.
	src := fmt.Sprintf("cd %s\ncd /\ncd %s\npwd -W\n",
		singleQuote(msysPath(t, tdir)), drive)
	got := runWindowsScript(t, tdir, src)
	want := strings.ReplaceAll(tdir, `\`, "/") + "\n"
	if !strings.EqualFold(got, want) {
		t.Fatalf("cd %s:\nwant: %q\ngot:  %q", drive, want, got)
	}
}

func TestTmpAndDevNullOperands(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	name := fmt.Sprintf("sh-story536-%d", os.Getpid())
	osPath := filepath.Join(os.TempDir(), name)
	defer os.Remove(osPath)

	src := fmt.Sprintf(`echo hi >/tmp/%[1]s
read -r line </tmp/%[1]s
echo "$line"
echo discarded >/dev/null
[ -e /dev/null ] && echo nullok
`, name)
	got := runWindowsScript(t, tdir, src)
	if want := "hi\nnullok\n"; got != want {
		t.Fatalf("tmp/dev-null script:\nwant: %q\ngot:  %q", want, got)
	}
	if _, err := os.Stat(osPath); err != nil {
		t.Fatalf("/tmp operand did not land in the OS temp dir: %v", err)
	}
}

func TestPipelineInProcessStages(t *testing.T) {
	t.Parallel()

	// Three in-process stages; before the DuplicateHandle-based dupPipeFd,
	// shared pipe endpoints surfaced "file already closed" (mvdan/sh#1142).
	src := `echo hi | { read -r x; echo "$x there"; } | { read -r y; echo "got $y"; }` + "\n"
	got := runWindowsScript(t, t.TempDir(), src)
	if want := "got hi there\n"; got != want {
		t.Fatalf("pipeline:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestPipelineEarlyExitReader(t *testing.T) {
	t.Parallel()

	// The reader stops after one line; the writer's remaining writes must be
	// classified as a broken pipe (status 141), not surface an error. The
	// loop is bounded so a missing classification fails fast instead of
	// hanging.
	src := `i=0
while [ "$i" -lt 100000 ]; do echo yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy; i=$((i+1)); done | { read -r first; echo "read $first"; }
echo "status ${PIPESTATUS[0]}"
`
	got := runWindowsScript(t, t.TempDir(), src)
	want := "read yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy\nstatus 141\n"
	if got != want {
		t.Fatalf("early-exit reader:\nwant: %q\ngot:  %q", want, got)
	}
}

func TestProcSubstIn(t *testing.T) {
	t.Parallel()

	src := `read -r line < <(echo hello)
echo "sub $line"
`
	got := runWindowsScript(t, t.TempDir(), src)
	if want := "sub hello\n"; got != want {
		t.Fatalf("<(cmd):\nwant: %q\ngot:  %q", want, got)
	}
}

func TestProcSubstOut(t *testing.T) {
	t.Parallel()

	src := `echo hi > >(read -r l; echo "got $l")
wait
`
	got := runWindowsScript(t, t.TempDir(), src)
	if want := "got hi\n"; got != want {
		t.Fatalf(">(cmd):\nwant: %q\ngot:  %q", want, got)
	}
}
