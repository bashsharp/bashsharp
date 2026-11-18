//go:build windows

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

func TestStory679WindowsExecSelection(t *testing.T) {
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, contents string
		wantCalls      int
		check          func(*testing.T, []*exec.Cmd)
	}{
		{"no-shebang", "echo ok\n", 2, func(t *testing.T, calls []*exec.Cmd) {
			if calls[1].Path != self || len(calls[1].Args) < 2 {
				t.Fatalf("fallback command: Path=%q Args=%q", calls[1].Path, calls[1].Args)
			}
		}},
		{"shebang", "#! /bin/sh\r\necho ok\n", 1, func(t *testing.T, calls []*exec.Cmd) {
			if calls[0].Path != self || len(calls[0].Args) < 2 {
				t.Fatalf("shebang command: Path=%q Args=%q", calls[0].Path, calls[0].Args)
			}
		}},
		{"pe", "MZnot-a-real-image", 1, func(t *testing.T, calls []*exec.Cmd) {
			if calls[0].Path == self {
				t.Fatalf("PE image unexpectedly re-execed through self: %q", calls[0].Args)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, []byte(tt.contents), 0o755); err != nil {
				t.Fatal(err)
			}
			var calls []*exec.Cmd
			stop := errors.New("stub stop")
			start := func(cmd *exec.Cmd) error {
				clone := *cmd
				clone.Args = append([]string(nil), cmd.Args...)
				calls = append(calls, &clone)
				if tt.name == "no-shebang" && len(calls) == 1 {
					return syscall.Errno(193)
				}
				return stop
			}
			ctx := context.WithValue(context.Background(), execStartOverrideCtxKey{}, start)
			env := expand.ListEnviron("PATH=" + dir)
			r, _ := New(Dir(dir), Env(env), WithBashCompatErrors(true))
			f, err := syntax.NewParser().Parse(strings.NewReader(tt.name), "")
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Run(ctx, f)
			if len(calls) != tt.wantCalls {
				t.Fatalf("got %d starts; want %d", len(calls), tt.wantCalls)
			}
			tt.check(t, calls)
		})
	}
}
