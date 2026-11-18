// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build linux

package interp_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A process-group TERM reaches the shell and its foreground external command
// together. The command can be reaped before Go's signal forwarder publishes
// the shell's own TERM; each shell still has to run its trap before exiting.
func TestForegroundGroupTermRunsEveryTrap(t *testing.T) {
	const groups = 8
	const script = `
trap 'printf x > "$KILL_DIR/parent"; exit 0' TERM
for i in 1 2 3; do
  KILL_INDEX="$i" "$GOSH_PROG" 'trap '\''printf x > "$KILL_DIR/child-$KILL_INDEX"; exit 0'\'' TERM
printf x > "$KILL_DIR/ready-$KILL_INDEX"
/bin/sleep 300' &
done
for i in 1 2 3; do
  while [ ! -f "$KILL_DIR/ready-$i" ]; do /bin/sleep 0.02; done
done
printf x > "$KILL_DIR/ready-parent"
/bin/sleep 300
`
	type group struct {
		dir string
		cmd *exec.Cmd
	}
	var runs []group
	defer func() {
		for _, run := range runs {
			_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	for i := 0; i < groups; i++ {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0], script)
		cmd.Env = append(os.Environ(), "GOSH_PROG="+os.Args[0], "KILL_DIR="+dir)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, group{dir, cmd})
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, run := range runs {
		for {
			if _, err := os.Stat(filepath.Join(run.dir, "ready-parent")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("group %d did not become ready", run.cmd.Process.Pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Let every member enter its foreground external wait, as in kill:19.
	time.Sleep(2 * time.Second)
	for _, run := range runs {
		if err := syscall.Kill(-run.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range runs {
		if err := run.cmd.Wait(); err != nil {
			t.Logf("group %d shell exit: %v", run.cmd.Process.Pid, err)
		}
	}
	var missing []string
	markerDeadline := time.Now().Add(2 * time.Second)
	for {
		missing = missing[:0]
		for _, run := range runs {
			for _, name := range []string{"parent", "child-1", "child-2", "child-3"} {
				if _, err := os.Stat(filepath.Join(run.dir, name)); err != nil {
					missing = append(missing, fmt.Sprintf("%d/%s", run.cmd.Process.Pid, name))
				}
			}
		}
		if len(missing) == 0 || time.Now().After(markerDeadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(missing) != 0 {
		t.Fatalf("TERM traps absent after process-group signal: %v", missing)
	}
}

// A TERM aimed only at the foreground command must not run the shell's trap.
// The receipt join must also give up promptly because no shell TERM is coming.
func TestForegroundCommandOnlyTermDoesNotTrapShell(t *testing.T) {
	dir := t.TempDir()
	const script = `
trap 'printf x > "$KILL_DIR/trap"' TERM
"$GOSH_PROG" 'printf "%s" "$$" > "$KILL_DIR/child-pid"; /bin/sleep 300'
printf x > "$KILL_DIR/after"
`
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	cmd := exec.Command(os.Args[0], script)
	cmd.Env = append(os.Environ(), "GOSH_PROG="+os.Args[0], "KILL_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "child-pid"))
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground child did not publish PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(childPID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("shell did not continue after child-only TERM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shell did not leave the bounded receipt wait")
	}
	if _, err := os.Stat(filepath.Join(dir, "after")); err != nil {
		t.Fatal("shell did not reach post-child statement:", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trap")); !os.IsNotExist(err) {
		t.Fatalf("child-only TERM ran shell trap: %v", err)
	}
}
