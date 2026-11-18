package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// This helper forms a three-process tree before the test harness parses flags.
// The grandchild inherits stdout and writes a marker if it survives.
func init() {
	mode := os.Getenv("SH_PROCESS_TREE_HELPER")
	if mode == "" {
		return
	}
	ready, marker := os.Getenv("SH_PROCESS_TREE_READY"), os.Getenv("SH_PROCESS_TREE_MARKER")
	if mode == "grandchild" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			os.Exit(2)
		}
		time.Sleep(6 * time.Second)
		_ = os.WriteFile(marker, []byte("survived"), 0o600)
		os.Exit(0)
	}
	if err := os.Setenv("SH_PROCESS_TREE_HELPER", "grandchild"); err != nil {
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	_ = cmd.Wait()
	os.Exit(0)
}

func TestExecProcessGroupsDeadlineKillsTree(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	marker := filepath.Join(t.TempDir(), "survived")
	// Single quotes preserve Windows path separators in the shell parser.
	quotedExe := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
	file, err := syntax.NewParser().Parse(strings.NewReader(quotedExe), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.StdIO(nil, &out, &out), interp.ExecProcessGroups(true), interp.Env(expand.ListEnviron(
		"PATH="+os.Getenv("PATH"), "SystemRoot="+os.Getenv("SystemRoot"),
		"SH_PROCESS_TREE_HELPER=parent", "SH_PROCESS_TREE_READY="+ready, "SH_PROCESS_TREE_MARKER="+marker)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, file) }()
	startLimit := time.After(2500 * time.Millisecond)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("command exited before grandchild was ready: %v; output: %s", err, out.String())
		case <-startLimit:
			t.Fatal("grandchild did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	<-ctx.Done()
	cancelled := time.Now()
	select {
	case <-done:
	case <-time.After(9 * time.Second):
		t.Fatal("Run did not return promptly after cancellation")
	}
	if elapsed := time.Since(cancelled); elapsed >= 9*time.Second {
		t.Fatalf("Run took %v after cancellation", elapsed)
	}
	// The grandchild would write this six seconds after its ready marker.
	time.Sleep(6100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("grandchild survived cancellation")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
