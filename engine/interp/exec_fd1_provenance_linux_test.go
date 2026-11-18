//go:build linux

package interp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const execFD1ProvenanceTestChild = "BASHY_FD1_PROVENANCE_TEST_CHILD"

func TestExecFD1Provenance(t *testing.T) {
	if dir := os.Getenv(execFD1ProvenanceTestChild); dir != "" {
		testExecFD1ProvenanceChild(t, dir)
		return
	}
	if testing.Short() {
		t.Skip("process-level executable identity test")
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, self, filepath.Join(dir, "shelltest"))
	copyExecutable(t, "/bin/sh", filepath.Join(dir, "coreutils"))
	if err := os.Symlink("coreutils", filepath.Join(dir, "nohup")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, "shelltest"), "-test.run=^TestExecFD1Provenance$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOSH_PROG=") && !strings.HasPrefix(entry, "GOSH_CMD=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, execFD1ProvenanceTestChild+"="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func testExecFD1ProvenanceChild(t *testing.T, dir string) {
	const child = `-c 'printf "%s\n" "${BASHY_EXEC_FD1_CLOSED-unset}" >&2'`
	for _, tc := range []struct{ name, script, want string }{
		{"owned-closed", "nohup " + child + " 1>&-", "1\n"},
		{"owned-devnull", "nohup " + child + " > /dev/null", "unset\n"},
		{"unrelated-closed", "/bin/sh " + child + " 1>&-", "unset\n"},
		{"spoofed-open", "BASHY_EXEC_FD1_CLOSED=1 nohup " + child + " > /dev/null", "unset\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			r, err := New(
				Dir(dir),
				Env(expand.ListEnviron("PATH="+dir+":/usr/bin:/bin", "HOME="+dir)),
				StdIO(nil, io.Discard, &errOut),
				WithDisabledBuiltins("nohup"),
			)
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			r.Reset()
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := errOut.String(); got != tc.want {
				t.Fatal(fmt.Sprintf("stderr=%q, want %q", got, tc.want))
			}
		})
	}
}
