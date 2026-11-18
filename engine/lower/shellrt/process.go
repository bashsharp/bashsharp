package shellrt

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Process is supplied by the shell backend: there is no second producer or
// subprocess implementation in the lowering runtime.
type Process interface {
	Lines() <-chan string
	Wait() (int, error)
	Close() error
}

type ProcessResult struct {
	Stdout string
	Stderr string
	Status int
}

func (r ProcessResult) Lines() []string {
	if r.Stdout == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// ProcessShellRunner is an optional capability at the existing shell seam.
type ProcessShellRunner interface {
	RunProcess(context.Context, *State, Stdio, []string) (ProcessResult, error)
	StartProcess(context.Context, *State, Stdio, []string) (Process, error)
}

type LiveProcess struct{ process Process }

func (p *LiveProcess) Lines() <-chan string { return p.process.Lines() }
func (p *LiveProcess) Wait() (int, string) {
	status, err := p.process.Wait()
	if err != nil {
		return status, err.Error()
	}
	return status, ""
}
func (p *LiveProcess) Close() error {
	err := p.process.Close()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (p *Program) RunProcess(argv ...string) (ProcessResult, string) {
	s := p.Session
	s.shellMu.Lock()
	defer s.shellMu.Unlock()
	s.mu.Lock()
	backend, st, streams := s.shell, s.state.Clone(), s.io
	s.mu.Unlock()
	processes, ok := backend.(ProcessShellRunner)
	if !ok {
		return ProcessResult{}, "run: shell backend has no process capability"
	}
	s.Arm()
	result, err := processes.RunProcess(p.Context, &st, streams, argv)
	if err != nil {
		return ProcessResult{}, err.Error()
	}
	return result, ""
}

func (p *Program) StartProcess(argv ...string) (*LiveProcess, string) {
	s := p.Session
	s.shellMu.Lock()
	defer s.shellMu.Unlock()
	s.mu.Lock()
	backend, st, streams := s.shell, s.state.Clone(), s.io
	s.mu.Unlock()
	processes, ok := backend.(ProcessShellRunner)
	if !ok {
		return nil, "start: shell backend has no process capability"
	}
	process, err := processes.StartProcess(p.Context, &st, streams, argv)
	if err != nil {
		return nil, err.Error()
	}
	return &LiveProcess{process: process}, ""
}

func (p *Program) WaitProcess(process *LiveProcess) {
	status, diagnostic := process.Wait()
	if diagnostic != "" {
		fmt.Fprintln(p.Session.Stdio().Err, diagnostic)
		status = 1
	}
	p.Session.SetStatus(status)
}

func (p *Program) CloseProcess(process *LiveProcess) {
	status := 0
	if err := process.Close(); err != nil {
		fmt.Fprintln(p.Session.Stdio().Err, err)
		status = 1
	}
	p.Session.SetStatus(status)
}
