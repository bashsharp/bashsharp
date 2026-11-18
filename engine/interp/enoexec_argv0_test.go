//go:build !windows

package interp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestENOEXECFallbackPreservesShellInvocation(t *testing.T) {
	const childEnv = "BASHY_TEST_ENOEXEC_ARGV0"
	if os.Getenv(childEnv) == "" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, "-test.run=^TestENOEXECFallbackPreservesShellInvocation$")
		cmd.Args[0] = "sh"
		cmd.Env = append(os.Environ(), "GOSH_PROG=", "GOSH_CMD=", childEnv+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shell-invoked test process: %v\n%s", err, out)
		}
		return
	}
	if os.Args[0] != "sh" {
		t.Fatalf("test process argv0=%q, want sh", os.Args[0])
	}
	dir := t.TempDir()
	program := filepath.Join(dir, "plain-program")
	if err := os.WriteFile(program, []byte("exit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var calls []*exec.Cmd
	stop := errors.New("stop after observing fallback")
	start := func(cmd *exec.Cmd) error {
		clone := *cmd
		clone.Args = append([]string(nil), cmd.Args...)
		calls = append(calls, &clone)
		if len(calls) == 1 {
			return syscall.ENOEXEC
		}
		return stop
	}
	ctx := context.WithValue(context.Background(), execStartOverrideCtxKey{}, start)
	r, err := New(Dir(dir), Env(expand.ListEnviron("PATH="+dir)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser().Parse(strings.NewReader("plain-program"), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Run(ctx, f)
	if len(calls) != 2 {
		t.Fatalf("got %d starts, want 2", len(calls))
	}
	got := calls[1]
	if got.Path != self || len(got.Args) < 2 || got.Args[0] != os.Args[0] || got.Args[1] != program {
		t.Fatalf("fallback Path=%q Args=%q, want executable %q with argv0 %q and script %q", got.Path, got.Args, self, os.Args[0], program)
	}
}
