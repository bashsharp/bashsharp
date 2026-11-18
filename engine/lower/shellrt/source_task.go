package shellrt

import (
	"context"
	"errors"
	"fmt"
)

// RunSourceTask preserves the language's panic reporting and command status
// while joining descendants before the owning Session closes this task.
func (p *Program) RunSourceTask(body func()) error {
	err := rankFailures(p.runBody(func(*Program) { body() }), p.operationFailure())
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		p.Fail(err)
		err = &ExitError{Status: p.Status(), Err: err}
	}
	if err == nil && p.Status() != 0 {
		err = &ExitError{Status: p.Status()}
	}
	if err != nil {
		p.Session.Cancel()
	}
	return rankFailures(err, p.Session.Join())
}

type sourceTaskFailure struct {
	err    error
	status int
}

func (e sourceTaskFailure) Error() string {
	return fmt.Sprintf("bash++: task failed: exit status %d", e.status)
}
func (e sourceTaskFailure) ExitStatus() int { return e.status }
func (e sourceTaskFailure) Unwrap() error   { return e.err }

// SourceFailure adapts only task failures at the compiled program boundary.
// The Session API keeps its detailed task identity for Go callers.
func SourceFailure(err error) error {
	var task *TaskError
	if errors.As(err, &task) {
		return sourceTaskFailure{err: err, status: failureStatus(err)}
	}
	return err
}
