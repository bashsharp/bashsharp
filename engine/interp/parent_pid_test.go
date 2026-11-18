package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestParentPIDBridgeValidation(t *testing.T) {
	physical := os.Getppid()
	for _, tc := range []struct {
		name, value string
		standalone  bool
		want        int
	}{
		{"valid", fmt.Sprintf("%d:12345", physical), true, 12345},
		{"embedded", fmt.Sprintf("%d:12345", physical), false, physical},
		{"stale", fmt.Sprintf("%d:12345", physical+1), true, physical},
		{"missing", "", true, physical},
		{"malformed", "bad:12345", true, physical},
		{"extra_field", fmt.Sprintf("%d:12345:6", physical), true, physical},
		{"zero", fmt.Sprintf("%d:0", physical), true, physical},
		{"negative", fmt.Sprintf("%d:-1", physical), true, physical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer // bashpp-racegate:safe-private
			r, err := New(Env(expand.ListEnviron(bashyParentPIDEnv+"="+tc.value)), StdIO(nil, &output, nil), func(r *Runner) error {
				if tc.standalone {
					// Exercise startup consumption without changing this test
					// process's actual signal dispositions.
					r.sigReset = OSSignalResetter{}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			file, err := syntax.NewParser().Parse(strings.NewReader(`printf '%s:%s\n' "$PPID" "${BASHY_PARENT_PID-unset}"; (printf '%s\n' "$PPID")`), "")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				output.Reset()
				if err := r.Run(context.Background(), file); err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("%d:unset\n%d\n", tc.want, tc.want)
				if output.String() != want {
					t.Fatalf("run %d: got %q, want %q", i, output.String(), want)
				}
				for _, entry := range r.execEnvWithFuncs() {
					if strings.HasPrefix(entry, bashyParentPIDEnv+"=") {
						t.Fatal("consumed bridge leaked to a descendant environment")
					}
				}
				r.Reset()
			}
		})
	}
}

type parentPIDCarrier int

func (p parentPIDCarrier) Pid() int   { return int(p) }
func (p parentPIDCarrier) Wait() int  { return 0 }
func (p parentPIDCarrier) Terminate() {}

func TestParentPIDBridgeRequiresOwnExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "bash")
	if err := os.WriteFile(launcher, []byte("unrelated executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{sigReset: OSSignalResetter{}}
	ctx := context.WithValue(context.Background(), bgProcCtxKey{}, &bgProc{carrier: parentPIDCarrier(12345)})
	if got := r.childParentPIDBridge(ctx, launcher, "sh"); got != "" {
		t.Fatalf("unpaired executable received bridge %q", got)
	}
	if err := os.Symlink(self, launcher+".real"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	link := filepath.Join(dir, "sh")
	if err := os.Symlink(launcher, link); err != nil {
		t.Fatal(err)
	}
	if got := r.childParentPIDBridge(ctx, launcher, "sh"); got != "" {
		t.Fatalf("unrelated executable with adjacent payload received bridge %q", got)
	}
	for _, path := range []string{launcher, link} {
		if !sameShellExecutableAs(path, launcher+".real") {
			t.Fatalf("installed launcher identity rejected for %s", path)
		}
	}
	if got, want := r.childParentPIDBridge(ctx, self, "sh"), strconv.Itoa(os.Getpid())+":12345"; got != want {
		t.Fatalf("own payload bridge %q, want %q", got, want)
	}
	// The same inode is also the installed env/coreutils applet. It must
	// never expose the shell's private PPID handoff to an env snapshot.
	for _, name := range []string{"env", "/usr/bin/env", "batch", "at", "coreutils"} {
		if got := r.childParentPIDBridge(ctx, self, name); got != "" {
			t.Fatalf("utility route %q received bridge %q", name, got)
		}
	}
	for _, name := range []string{"bash", "bashy", "/usr/bin/sh", "-bash"} {
		if got, want := r.childParentPIDBridge(ctx, self, name), strconv.Itoa(os.Getpid())+":12345"; got != want {
			t.Fatalf("shell route %q bridge %q, want %q", name, got, want)
		}
	}
	if got := r.childParentPIDBridge(context.Background(), self, "sh"); got != "" {
		t.Fatalf("foreground child received bridge %q", got)
	}
	primary := context.WithValue(context.Background(), bgProcCtxKey{}, &bgProc{carrier: parentPIDCarrier(12345), publishPidToBang: true})
	if got := r.childParentPIDBridge(primary, self, "sh"); got != "" {
		t.Fatalf("primary external job received bridge %q", got)
	}
	replacing := context.WithValue(ctx, execReplacingCtxKey{}, true)
	if got := r.childParentPIDBridge(replacing, self, "sh"); got != "" {
		t.Fatalf("exec replacement received bridge %q", got)
	}
	r.sigReset = nil
	if got := r.childParentPIDBridge(ctx, self, "sh"); got != "" {
		t.Fatalf("embedded runner exported bridge %q", got)
	}
}
