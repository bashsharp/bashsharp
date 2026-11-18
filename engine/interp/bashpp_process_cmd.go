// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"io"
	"os/exec"
	"sync"
)

// bashPPCmdSource adapts a real os/exec process to the bashPPProcessSource the
// bounded line substrate consumes. It is the bridge that lets the same
// producer/drain/reap logic serve an actual child process — the shape the
// ported os/exec lifecycle tests exercise — using the platform-correct
// process-group creation and group-kill helpers already in this package.
type bashPPCmdSource struct {
	cmd    *exec.Cmd
	stdout io.Reader
	stderr io.Reader

	mu     sync.Mutex
	reaped bool
}

func (s *bashPPCmdSource) Stdout() io.Reader { return s.stdout }
func (s *bashPPCmdSource) Stderr() io.Reader { return s.stderr }

func (s *bashPPCmdSource) Wait() error {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.reaped = true
	s.mu.Unlock()
	return err
}

// Kill group-kills the child, but only while it has not yet been reaped: the
// mutex serialises Kill against Wait's reaped transition, and a pid is not
// reused before it is reaped, so the group signal can never land on an
// unrelated process. After reaping, Kill is a no-op.
func (s *bashPPCmdSource) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reaped {
		return
	}
	bashPPNativeKill(s.cmd)
}

// bashPPStartCmd starts cmd with its stdout and stderr piped, places it in its
// own process group so cancellation can group-kill it, and returns a live line
// process streaming its stdout. buffer is the bounded channel capacity.
//
// The caller must not have set cmd.Stdout or cmd.Stderr. As with
// exec.Cmd.StdoutPipe, the pipes are drained to EOF by the substrate before it
// reaps the process, so callers never close them.
func bashPPStartCmd(ctx context.Context, cmd *exec.Cmd, buffer int) (*bashPPLineProcess, error) {
	if err := execBudgetRefusalCmd(cmd); err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	bashPPNativeProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	src := &bashPPCmdSource{cmd: cmd, stdout: stdout, stderr: stderr}
	return bashPPStartLineProcess(ctx, src, buffer), nil
}
