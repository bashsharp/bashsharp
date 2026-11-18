//go:build unix

package interp_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBuiltinSelfSignalTerminatesStandaloneShell(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"KILL", syscall.SIGKILL},
		{"TERM", syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBuiltinSelfSignalChild$")
			cmd.Env = append(os.Environ(), "GOSH_PROG=", "SH_SELF_SIGNAL="+tc.name)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("child exit = %v, output = %q, want signal %v", err, out, tc.sig)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != tc.sig {
				t.Fatalf("child wait status = %v, output = %q, want signal %v", exit.Sys(), out, tc.sig)
			}
		})
	}
}

func TestBuiltinSelfSignalChild(t *testing.T) {
	name := os.Getenv("SH_SELF_SIGNAL")
	if name == "" {
		return
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("kill -"+name+" $$"), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.WithSignalResetter(interp.OSSignalResetter{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("shell returned normally: %v", err)
	}
	t.Fatal("shell returned normally")
}
