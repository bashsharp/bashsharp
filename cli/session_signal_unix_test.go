//go:build unix

package cli

import (
	"context"
	"io"
	"testing"
)

func TestSessionResultPreservesForegroundSignal(t *testing.T) {
	result := RunSessionCommandResultWithConfig(context.Background(), SessionIO{
		Command: "/bin/sh -c 'kill -TERM $$'",
		Env:     []string{"PATH=/bin:/usr/bin"},
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	}, SessionConfig{})
	if result.ExitCode != 143 || !result.Signaled || result.Signal != "SIGTERM" {
		t.Fatalf("result = %#v", result)
	}
}
