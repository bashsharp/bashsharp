// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestExecStartErrorWindowsRule pins the Windows half of the `exec NAME`
// pre-check. Windows has no execve: the shell starts NAME and exits in its
// place, and that exit runs the EXIT trap and discards the trap table. A
// refusal must therefore be discovered by execStartError, before the shell
// commits to the replacement — and it must refuse exactly what the default
// handler's lookup refuses (recorded ACL mode, else the Cygwin name rule),
// so the diagnostics stay `Permission denied` (126) and ENOENT stays 127.
func TestExecStartErrorWindowsRule(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()
	mkfile := func(name string, perm os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tdir, name), []byte("echo bar\n"), perm); err != nil {
			t.Fatal(err)
		}
	}
	mkfile("x.sh", 0o755) // runnable on a Unix host; the Windows name rule refuses it
	mkfile("prog", 0o755) // extensionless: Windows attempts it (ENOEXEC fallback territory)
	mkfile("tool.exe", 0o755)
	if err := os.Mkdir(filepath.Join(tdir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := New(Dir(tdir), Env(expand.ListEnviron("PATH=/usr/bin:/bin")))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	ctx := context.Background()

	tests := []struct {
		name     string
		wantTail string
		wantCode uint8
		refused  bool
	}{
		{"./x.sh", "./x.sh: Permission denied", 126, true},
		{"./prog", "", 0, false},
		{"./tool.exe", "", 0, false},
		{"./subdir", "./subdir: Is a directory", 126, true},
		{"./notthere", "./notthere: No such file or directory", 127, true},
	}
	for _, tc := range tests {
		tail, code, refused := r.execStartErrorMode(ctx, tc.name, true)
		if refused != tc.refused || tail != tc.wantTail || code != tc.wantCode {
			t.Errorf("execStartErrorMode(%q, windows) = (%q, %d, %v), want (%q, %d, %v)",
				tc.name, tail, code, refused, tc.wantTail, tc.wantCode, tc.refused)
		}
	}

	if runtime.GOOS != "windows" {
		// The Unix rule is untouched by the split: the execute bit decides.
		if tail, code, refused := r.execStartErrorMode(ctx, "./x.sh", false); refused {
			t.Errorf("unix mode refused 0755 ./x.sh: (%q, %d)", tail, code)
		}
		mkfile("plain.sh", 0o644)
		if tail, code, refused := r.execStartErrorMode(ctx, "./plain.sh", false); !refused ||
			tail != "./plain.sh: Permission denied" || code != 126 {
			t.Errorf("unix mode on 0644 file = (%q, %d, %v), want Permission denied 126",
				tail, code, refused)
		}
	}
}

// TestExecRefusedKeepsTraps is exec3.sub distilled: with `shopt -s
// execfail`, a refused exec must leave the shell's signal state intact —
// the EXIT trap, the USR1 trap, and the ignored TERM (trap "" TERM) must all
// survive, and the refusal must still report `Permission denied` with
// status 126. On Unix the execute bit refuses x.sh; on Windows the name
// rule does; either way the refusal happens before the shell commits to
// being replaced.
func TestExecRefusedKeepsTraps(t *testing.T) {
	// Not parallel: the ignored TERM (trap "" TERM) and the USR1 trap install process-wide
	// dispositions (signal.Ignore/Notify) that the carrier tests would
	// observe. The script resets them with `trap - TERM USR1` after the
	// listing, but an embedded Runner (no SignalResetter) leaves the OS
	// SIG_IGN in place on reset, which later tests would read as a
	// startup-ignored TERM. Restore the real default when the test ends.
	t.Cleanup(func() { OSSignalResetter{}.ResetDefault(0, "TERM") })

	tdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tdir, "x.sh"), []byte("echo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `shopt -s execfail
trap 'echo EXIT' EXIT
trap '' TERM
trap 'echo USR1' USR1
exec ./x.sh
echo "code=$?"
trap
trap - TERM USR1
exit 0
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "exec-refused")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(
		Dir(tdir),
		Env(expand.ListEnviron("PATH=/usr/bin:/bin")),
		StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "Permission denied") {
		t.Errorf("stderr = %q, want a Permission denied diagnostic", stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"code=126\n",
		"trap -- 'echo EXIT' EXIT\n",
		"trap -- '' SIGTERM\n",
		"trap -- 'echo USR1' SIGUSR1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nstdout=%q", want, out)
		}
	}
	if !strings.HasSuffix(out, "EXIT\n") {
		t.Errorf("stdout = %q, want the EXIT trap to run last", out)
	}
}
