package shellexec

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func (sh *shell) prepareProcess(ctx context.Context, st *shellrt.State, streams shellrt.Stdio) error {
	if streams != sh.io {
		if err := sh.restoreStdio(streams); err != nil {
			return err
		}
	}
	return sh.applyTypedWrites(ctx, st)
}

func (sh *shell) RunProcess(ctx context.Context, st *shellrt.State, streams shellrt.Stdio, argv []string) (shellrt.ProcessResult, error) {
	if err := sh.prepareProcess(ctx, st, streams); err != nil {
		return shellrt.ProcessResult{}, err
	}
	r, err := sh.runner.RunProcess(sh.taskPolicyContext(ctx), argv...)
	return shellrt.ProcessResult{Stdout: r.Stdout, Stderr: r.Stderr, Status: r.Status}, err
}

func (sh *shell) StartProcess(ctx context.Context, st *shellrt.State, streams shellrt.Stdio, argv []string) (shellrt.Process, error) {
	if err := sh.prepareProcess(ctx, st, streams); err != nil {
		return nil, err
	}
	p, err := sh.runner.StartProcess(sh.taskPolicyContext(ctx), argv...)
	if err == nil {
		sh.processes = append(sh.processes, p)
	}
	return p, err
}
