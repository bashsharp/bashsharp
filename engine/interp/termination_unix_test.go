//go:build unix

package interp_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunPreservesSignaledForegroundTermination(t *testing.T) {
	t.Run("signal", func(t *testing.T) {
		file, err := syntax.NewParser().Parse(strings.NewReader("/bin/sh -c 'kill -TERM $$'"), "")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.StdIO(nil, io.Discard, io.Discard))
		if err != nil {
			t.Fatal(err)
		}
		err = runner.Run(context.Background(), file)
		if err.Error() != "exit status 143" {
			t.Fatalf("error text = %q; legacy contract changed", err)
		}
		signaled, ok := runner.LastSignaledStatus()
		if !ok {
			t.Fatalf("error = %T %v, want recorded signal", err, err)
		}
		if signaled.Status != 128+interp.ExitStatus(syscall.SIGTERM) || signaled.Signal != int(syscall.SIGTERM) || signaled.SignalName != "SIGTERM" {
			t.Fatalf("signaled status = %#v", signaled)
		}
		var status interp.ExitStatus
		if !errors.As(err, &status) || status != 143 {
			t.Fatalf("legacy exit status = %d, %v", status, err)
		}
	})

	t.Run("same numeric exit", func(t *testing.T) {
		file, err := syntax.NewParser().Parse(strings.NewReader("/bin/sh -c 'exit 143'"), "")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.StdIO(nil, io.Discard, io.Discard))
		if err != nil {
			t.Fatal(err)
		}
		err = runner.Run(context.Background(), file)
		if signaled, ok := runner.LastSignaledStatus(); ok {
			t.Fatalf("explicit exit was classified as signal: %#v", signaled)
		}
		var status interp.ExitStatus
		if !errors.As(err, &status) || status != 143 {
			t.Fatalf("exit status = %d, %v", status, err)
		}
	})
}
