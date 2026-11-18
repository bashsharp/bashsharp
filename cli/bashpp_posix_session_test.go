package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/qiangli/bash/syntax"
)

func TestBashPPPOSIXSessionRuntime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   []string
		on    bool
		posix bool
	}{
		{"activation-control", nil, true, false},
		{"explicit-disable-control", []string{"BASHY_BASHPP=0"}, false, false},
		{"posix-disables-default", []string{"POSIXLY_CORRECT="}, false, true},
		{"posix-disables-explicit-env", []string{"POSIXLY_CORRECT=1", "BASHY_BASHPP=1"}, false, true},
		{"posix-retains-env-off", []string{"POSIXLY_CORRECT=1", "BASHY_BASHPP=0"}, false, true},
		{"shellopts-disables-extensions", []string{"SHELLOPTS=posix", "BASHY_BASHPP=1"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withStrictPosixEnv(t, "bashy", false)
			var stdout, stderr bytes.Buffer
			request := SessionIO{
				Command: "eval 'agentic func f(n int) int { return n }'",
				Env:     append([]string{"PATH="}, tc.env...), Stdout: &stdout, Stderr: &stderr,
			}
			r, err := NewSessionRunnerWithConfig(request, SessionConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Dialect() == syntax.LangBashPP; got != tc.on {
				t.Fatalf("runtime Bash++=%v, want %v", got, tc.on)
			}
			if got := r.LangVariant() == syntax.LangPOSIX; got != tc.posix {
				t.Fatalf("runtime POSIX=%v, want %v", got, tc.posix)
			}
			status := RunSessionCommandWithConfig(context.Background(), request, SessionConfig{})
			// eval reports reparsing errors with status 1, even when the
			// enclosing command's startup parser would reject with status 2.
			wantStatus := 1
			if tc.on {
				wantStatus = 0
			}
			if status != wantStatus || stdout.Len() != 0 || (stderr.Len() == 0) != tc.on {
				t.Fatalf("status=%d stdout=%q stderr=%q, want status=%d", status, stdout.String(), stderr.String(), wantStatus)
			}
			if !tc.on && !strings.Contains(stderr.String(), "a command can only contain words and redirects") {
				t.Fatalf("disabled grammar did not reject eval text while parsing: %q", stderr.String())
			}
			if strings.Contains(stderr.String(), "extensions disabled") {
				t.Fatalf("disabled grammar reached Bash++ evaluation: %q", stderr.String())
			}
		})
	}
}
