//go:build unix

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// An external command that leaves a grandchild behind (a test script that
// starts a real server) must end when the context does: with
// ExecProcessGroups the cancellation reaches the whole group, so the
// grandchild dies, the output pipe closes and Run returns.
func TestExecProcessGroupsCancelKillsGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	src := `/bin/sh -c 'sleep 300 & echo $! > "$1"; echo started; wait' x ` + pidFile
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	// A non-*os.File writer makes os/exec copy through a pipe, as an
	// embedding harness's spool does.
	var out bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &out, &out), interp.ExecProcessGroups(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, file) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(raw))); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		t.Fatal("Run did not return after cancellation: a grandchild kept the output pipe open")
	}
	if !strings.Contains(out.String(), "started") {
		t.Fatalf("output = %q, want the output produced before cancellation", out.String())
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("grandchild pid = %q", raw)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d outlived the cancellation", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
