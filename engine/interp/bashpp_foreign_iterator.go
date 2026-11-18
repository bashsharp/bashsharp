package interp

import (
	"context"
	"errors"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"sync"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A foreign iterator is lazy: merely assigning it cannot leak a child. Its
// first range owns the B14 line process and closes/reaps it on every exit.
type bashPPForeignIterator struct {
	mu      sync.Mutex
	module  *polyglot.Module
	name    string
	args    []any
	element string
	used    bool
}

func (r *Runner) bashPPRangeForeignIterator(ctx context.Context, rng *syntax.BashPPRange) bool {
	if rng.Chan == nil || r.bashPPScope == nil {
		return false
	}
	vr := r.bashPPScope.lookup(r.literal(rng.Chan)).viewVar()
	if vr.Kind != expand.Object {
		return false
	}
	if handle, ok := vr.Obj.(*polyglot.Handle); ok {
		return r.bashPPRangePythonHandle(ctx, rng, handle)
	}
	iterator, ok := vr.Obj.(*bashPPForeignIterator)
	if !ok {
		return false
	}
	iterator.mu.Lock()
	used := iterator.used
	iterator.used = true
	iterator.mu.Unlock()
	if used {
		return true
	}
	if len(rng.Names) > 1 {
		r.errf("BASHPP-ERANGE-TYPE: iterator range permits one variable\n")
		r.exit.code = 2
		return true
	}
	if len(rng.Names) > 0 && !rng.Define.IsValid() {
		r.errf("BASHPP-ERANGE-TYPE: iterator range requires a declaration\n")
		r.exit.code = 2
		return true
	}
	stdout, stderr := r.bashPPWriter(r.stdout), r.bashPPWriter(r.stderr)
	process, err := StartForeignIterator(ctx, iterator.module, iterator.name, iterator.args, stdout, stderr)
	if err != nil {
		r.errf("foreign iterator: %v\n", err)
		r.exit.code = 1
		return true
	}
	defer func() {
		if err := process.Close(); err != nil && !errors.Is(err, context.Canceled) {
			r.errf("foreign iterator: %v\n", err)
			r.exit.code = 1
		}
		fmt.Fprint(stderr, process.Stderr())
	}()
	for line := range process.Lines() {
		frame, err := iterator.module.DecodeIteratorFrame(ctx, line, iterator.element)
		if err != nil {
			r.errf("foreign iterator: %v\n", err)
			r.exit.code = 1
			return true
		}
		fmt.Fprint(stdout, frame.Stdout)
		fmt.Fprint(stderr, frame.Stderr)
		value := frame.Value
		if !r.bashPPForeignRangeValue(ctx, rng, value, iterator.element) {
			return true
		}
	}
	status, err := process.Wait()
	if err != nil {
		r.errf("foreign iterator: %v\n", err)
		r.exit.code = 1
	} else if status != 0 {
		r.exit.code = uint8(status)
	}
	return true
}

func (r *Runner) bashPPRangePythonHandle(ctx context.Context, rng *syntax.BashPPRange, handle *polyglot.Handle) bool {
	if len(rng.Names) > 2 {
		return false
	}
	if len(rng.Names) > 0 && !rng.Define.IsValid() {
		r.errf("BASHPP-ERANGE-TYPE: Python handle range requires a declaration\n")
		r.exit.code = 2
		return true
	}
	stdout, stderr := r.bashPPWriter(r.stdout), r.bashPPWriter(r.stderr)
	worker, initial, err := handle.OpenIterator(ctx)
	fmt.Fprint(stdout, initial.Stdout)
	fmt.Fprint(stderr, initial.Stderr)
	if err != nil {
		if len(rng.Names) == 2 && bashPPForeignDomainError(err) {
			r.bashPPPythonHandleRangeValue(ctx, rng, nil, err)
		} else {
			r.errf("foreign iterator: %v\n", err)
			r.exit.code, r.exit.exiting = 1, true
		}
		return true
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		closed, closeErr := worker.Close(closeCtx)
		fmt.Fprint(stdout, closed.Stdout)
		fmt.Fprint(stderr, closed.Stderr)
		if closeErr != nil && !errors.Is(closeErr, context.Canceled) {
			r.errf("foreign iterator: %v\n", closeErr)
			r.exit.code, r.exit.exiting = 1, true
		}
	}()
	for {
		result, more, nextErr := worker.Next(ctx)
		fmt.Fprint(stdout, result.Stdout)
		fmt.Fprint(stderr, result.Stderr)
		if nextErr != nil {
			if len(rng.Names) == 2 && bashPPForeignDomainError(nextErr) {
				r.bashPPPythonHandleRangeValue(ctx, rng, nil, nextErr)
			} else {
				r.errf("foreign iterator: %v\n", nextErr)
				r.exit.code, r.exit.exiting = 1, true
			}
			return true
		}
		if !more || !r.bashPPPythonHandleRangeValue(ctx, rng, result.Value, nil) {
			return true
		}
	}
}

func (r *Runner) bashPPPythonHandleRangeValue(ctx context.Context, rng *syntax.BashPPRange, value any, iterErr error) bool {
	leave := bashPPNoScopeLeave
	if bashPPRangeBindsNames(rng) {
		leave = r.bashPPPushScope()
	}
	if len(rng.Names) > 0 && rng.Names[0].Value != "_" {
		name := rng.Names[0].Value
		switch value.(type) {
		case nil:
			r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String})
			r.bashPPScope.lookup(name).declType = bashPPRangeNamedType("any")
		case []byte, []any, map[string]any, *polyglot.Handle:
			r.bashPPDeclareName(name, expand.NewObject(value))
			r.bashPPScope.lookup(name).declType = bashPPRangeNamedType("any")
		default:
			r.bashPPDeclareRangeValue(name, value, bashPPRangeNamedType("any"), nil)
		}
	}
	if len(rng.Names) == 2 && rng.Names[1].Value != "_" {
		name := rng.Names[1].Value
		errorCell := bashPPForeignErrorCell(iterErr)
		r.bashPPDeclareName(name, errorCell.vr)
		cell := r.bashPPScope.lookup(name)
		cell.declType = errorCell.declType
		cell.interfaceValue = errorCell.interfaceValue
	}
	r.cmd(r.bashPPTaskContext(ctx), rng.Body)
	leave()
	return r.bashPPRangeControl()
}

// Foreign aggregate values retain their Object cell through the range binding;
// fmt.Sprint would discard byte/handle identity and make fields inaccessible.
func (r *Runner) bashPPForeignRangeValue(ctx context.Context, rng *syntax.BashPPRange, value any, element string) bool {
	switch value.(type) {
	case nil, []byte, []any, map[string]any, *polyglot.Handle:
		leave := r.bashPPPushScope()
		if len(rng.Names) > 0 && rng.Names[0].Value != "_" {
			name := rng.Names[0].Value
			r.bashPPDeclareName(name, expand.NewObject(value))
			cell := r.bashPPScope.lookup(name)
			cell.declType = bashPPRangeNamedType(foreignShellType(element))
		}
		r.cmd(r.bashPPTaskContext(ctx), rng.Body)
		leave()
		return r.bashPPRangeControl()
	default:
		return r.bashPPRangeIteration(ctx, rng, value, bashPPRangeNamedType(element), nil, nil, nil)
	}
}
