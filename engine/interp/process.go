package interp

import (
	"context"
	"fmt"
	"os/exec"
)

// LineProcess is the live process boundary shared with compiled Bash# programs.
// Lines has one consumer; Wait drains unread lines and reaps exactly once.
type LineProcess interface {
	Lines() <-chan string
	Wait() (int, error)
	Close() error
	Stderr() string
}

// StartLineCommand starts an already-configured worker command on the shared
// bounded channel substrate. The caller must consume Lines then Wait, or Close.
func StartLineCommand(ctx context.Context, cmd *exec.Cmd, buffer int) (LineProcess, error) {
	return bashPPStartCmd(ctx, cmd, buffer)
}

// ProcessResult keeps completed stdout, stderr, and status separate.
type ProcessResult struct {
	Stdout string
	Stderr string
	Status int
}

// Lines splits completed output without starting a goroutine or a process.
func (r ProcessResult) Lines() []string { return bashPPSplitLines(r.Stdout) }

// RunProcess executes argv through this runner's existing command dispatch.
// Like Run, it must not overlap another operation on the same Runner.
func (r *Runner) RunProcess(ctx context.Context, argv ...string) (ProcessResult, error) {
	if len(argv) == 0 {
		return ProcessResult{}, fmt.Errorf("run: requires a command")
	}
	if !r.didReset {
		r.Reset()
	}
	out, diagnostic, status, problem := r.bashPPCaptureExec(ctx, "run", argv, nil)
	if problem != "" {
		return ProcessResult{}, fmt.Errorf("%s", problem)
	}
	return ProcessResult{Stdout: out, Stderr: diagnostic, Status: status}, nil
}

// StartProcess snapshots this runner's command environment and starts argv on
// the same bounded process substrate used by interpreted start(). The owner
// must call Wait or Close; cancellation terminates the child.
func (r *Runner) StartProcess(ctx context.Context, argv ...string) (LineProcess, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("start: requires a command")
	}
	if !r.didReset {
		r.Reset()
	}
	return r.bashPPStartSubshell(ctx, argv), nil
}
