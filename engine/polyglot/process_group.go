package polyglot

import (
	"context"
	"fmt"
)

// JoinProcessGroup reserves the persistent worker for one pipeline job. The
// worker changes its own group (the parent cannot move a child after exec).
// Passing zero establishes a group whose leader is the worker. The caller
// must pair a successful join with LeaveProcessGroup, including error exits.
func (m *Module) JoinProcessGroup(ctx context.Context, group int) (int, error) {
	m.jobOnce.Do(func() { m.jobSem = make(chan struct{}, 1) })
	select {
	case m.jobSem <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	result, err := m.processGroup(ctx, "job_join", group)
	if err != nil {
		<-m.jobSem
		return 0, err
	}
	id, ok := result.Value.(int64)
	if !ok {
		<-m.jobSem
		return 0, fmt.Errorf("invalid worker process group %T", result.Value)
	}
	return int(id), nil
}
func (m *Module) LeaveProcessGroup(ctx context.Context) error {
	if m.jobSem == nil {
		return nil
	}
	defer func() {
		select {
		case <-m.jobSem:
		default:
		}
	}()
	_, err := m.processGroup(ctx, "job_leave", 0)
	return err
}
func (m *Module) processGroup(ctx context.Context, op string, group int) (CallResult, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return CallResult{}, err
	}
	defer unlock()
	if op == "job_join" {
		if err = m.ensure(ctx); err != nil {
			return CallResult{}, err
		}
	} else if m.in == nil {
		return CallResult{}, nil
	}
	m.nextID++
	return m.request(ctx, map[string]any{"id": m.nextID, "op": op, "pgid": group}, nil)
}
