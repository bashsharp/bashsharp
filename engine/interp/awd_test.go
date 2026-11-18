// Copyright (c) 2025, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestAwdBuiltin covers the bashy `awd DIR [--] cmd…` builtin: it runs the
// command with the cwd temporarily set to DIR, then fully restores the shell's
// cwd; it errors like cd on a bad dir; and it propagates the command's exit.
func TestAwdBuiltin(t *testing.T) {
	start := t.TempDir()
	elsewhere := t.TempDir()

	run := func(src string) (out, errOut string, code uint8) {
		var so, se bytes.Buffer
		r, err := New(Dir(start), StdIO(nil, &so, &se))
		if err != nil {
			t.Fatal(err)
		}
		f, perr := syntax.NewParser().Parse(strings.NewReader(src), "")
		if perr != nil {
			t.Fatal(perr)
		}
		rerr := r.Run(context.Background(), f)
		var st ExitStatus
		if errors.As(rerr, &st) {
			code = uint8(st)
		}
		return so.String(), se.String(), code
	}

	// Restoration: pwd before and after awd are identical (ephemeral chdir).
	out, _, code := run("pwd; awd " + elsewhere + " true; pwd")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != lines[1] {
		t.Errorf("cwd not restored: %q", out)
	}
	if code != 0 {
		t.Errorf("awd true: exit %d, want 0", code)
	}

	// During: the wrapped command observes DIR as its cwd.
	out, _, _ = run("pwd; awd " + elsewhere + " pwd")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Errorf("awd did not change cwd for the command: %q", out)
	}

	// Exit status propagates from the wrapped command.
	if _, _, code := run("awd " + elsewhere + " false"); code != 1 {
		t.Errorf("awd false: exit %d, want 1", code)
	}

	// A missing directory errors like cd (non-zero, cd-style message).
	_, errOut, code := run("awd /no/such/dir true")
	if code == 0 {
		t.Error("awd on a missing dir should fail")
	}
	if !strings.Contains(errOut, "No such file or directory") {
		t.Errorf("missing-dir error wording: %q", errOut)
	}

	// Usage errors: no args, and a dir with no command.
	if _, _, code := run("awd"); code != 2 {
		t.Errorf("bare awd: exit %d, want 2", code)
	}
	if _, _, code := run("awd " + elsewhere); code != 2 {
		t.Errorf("awd DIR with no command: exit %d, want 2", code)
	}

	// The `--` separator lets the command be a builtin.
	out, _, _ = run("awd " + elsewhere + " -- echo ok")
	if strings.TrimSpace(out) != "ok" {
		t.Errorf("awd -- echo: %q", out)
	}
}

// TestAwdConcurrentShellCopiesOwnCwdState makes the shell-copy boundary do
// real work: the two calls overlap, each changes the directory stack beneath
// awd, and neither may observe or retain the other's cwd state.  Classic
// asynchronous lists and Bash++ go tasks are both goroutine-backed copies of
// a Runner, so this is also a -race regression for the snapshot boundary.
func TestAwdConcurrentShellCopiesOwnCwdState(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	reports := filepath.Join(root, "reports")
	for _, dir := range []string{parent, left, right, reports} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	q := func(s string) string { return fmt.Sprintf("%q", s) }
	observe := fmt.Sprintf(`
observe() {
	printf '%%s|%%s|%%s|%%s\n' "$1" "$PWD" "$OLDPWD" "${DIRSTACK[*]}" > %s/"$1.before"
	# Keep both awd invocations live long enough to exercise their independent
	# Runner snapshots, rather than only their launch order.
	sleep 0.01
}
`, q(reports))

	tests := []struct {
		name   string
		lang   syntax.LangVariant
		launch string
	}{
		{
			name: "classic async lists",
			lang: syntax.LangBash,
			launch: fmt.Sprintf(`
awd %s observe left &
awd %s observe right &
wait
`, q(left), q(right)),
		},
		{
			name: "bashpp go tasks",
			lang: syntax.LangBashPP,
			launch: fmt.Sprintf(`
func leftTask() { awd %s observe left; }
func rightTask() { awd %s observe right; }
go leftTask()
go rightTask()
`, q(left), q(right)),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A separate report directory per mode makes a stale report unable to
			// satisfy the next row if a task were accidentally not joined.
			if err := os.RemoveAll(reports); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(reports, 0o755); err != nil {
				t.Fatal(err)
			}

			var out syncBuffer
			r, err := New(Lang(tc.lang), Dir(root), StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			src := fmt.Sprintf("pushd %s >/dev/null\n", q(parent)) + observe + tc.launch
			f, err := syntax.NewParser(syntax.Variant(tc.lang)).Parse(strings.NewReader(src), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), f); err != nil {
				t.Fatalf("Run: %v; output: %s", err, out.String())
			}

			want := map[string]string{
				"left":  "left|" + left + "|" + parent + "|" + left + " " + root + "\n",
				"right": "right|" + right + "|" + parent + "|" + right + " " + root + "\n",
			}
			for name, wantReport := range want {
				got, err := os.ReadFile(filepath.Join(reports, name+".before"))
				if err != nil {
					t.Fatalf("read %s.before: %v", name, err)
				}
				if string(got) != wantReport {
					t.Errorf("%s.before = %q, want %q", name, got, wantReport)
				}
			}
			if got, want := r.Dir, parent; got != want {
				t.Errorf("parent Dir = %q, want %q", got, want)
			}
			if got, want := r.envGet("PWD"), parent; got != want {
				t.Errorf("parent PWD = %q, want %q", got, want)
			}
			if got, want := r.envGet("OLDPWD"), root; got != want {
				t.Errorf("parent OLDPWD = %q, want %q", got, want)
			}
			if got, want := strings.Join(r.dirStack, " "), root+" "+parent; got != want {
				t.Errorf("parent directory stack = %q, want %q", got, want)
			}
		})
	}
}
