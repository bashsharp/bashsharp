package interp

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5
import (
	"context"
	"math/rand/v2"
	"reflect"
	"time"
)

// bashPPMixedChoice is the single committed outcome of a select whose arms
// mix dependency-owned and interpreter-owned channels. Exactly one of native
// and local is non-negative, or both are -1 for the language default.
type bashPPMixedChoice struct {
	native int
	local  int
	cell   *bashPPCell
	v      reflect.Value
	open   bool
}

const (
	bashPPMixedSelectMinPoll = 50 * time.Microsecond
	bashPPMixedSelectMaxPoll = 10 * time.Millisecond
)

// bashPPMixedSelect arbitrates one select across two channel owners that
// cannot share a runtime select. Neither side is ever offered an operation it
// might commit concurrently with the other: the interpreter-owned arms are
// tried with a non-blocking select, the dependency-owned arms with the
// dependency's non-blocking probe, each of which commits at most one
// communication and reports whether it did. Only when both report nothing
// ready (and there is no default) does the select block — on the
// interpreter-owned arms alone, with a bounded wait after which the
// dependency is probed again. A local arm that becomes ready wakes the select
// at once; a dependency arm is observed at the next probe. The side probed
// first alternates randomly, so when both are ready either may win, as the
// language's uniform choice among ready cases requires.
//
// cases holds the interpreter-owned select cases, in which dependency arms are
// already disabled nil entries. It reports false after recording the
// cancellation or dependency error in the runner's exit status.
func (r *Runner) bashPPMixedSelect(ctx context.Context, cases []reflect.SelectCase, native []bashPPBridgeValue, hasDefault bool) (bashPPMixedChoice, bool) {
	taskCtx := r.bashPPTaskContext(ctx)
	n := len(cases)
	probe := append(cases[:n:n],
		reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(taskCtx.Done())},
		reflect.SelectCase{Dir: reflect.SelectDefault})
	wait := append(cases[:n:n],
		reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(taskCtx.Done())},
		reflect.SelectCase{Dir: reflect.SelectRecv})
	canceled := func() (bashPPMixedChoice, bool) {
		r.bashPPTaskCanceled = true
		r.exit.code = 1
		return bashPPMixedChoice{}, false
	}
	local := func(i int, v reflect.Value, open bool) (bashPPMixedChoice, bool) {
		return bashPPMixedChoice{native: -1, local: i, v: v, open: open}, true
	}
	armed := false
	delay := bashPPMixedSelectMinPoll
	for {
		localFirst := rand.IntN(2) == 0
		for pass := range 2 {
			if (pass == 0) == localFirst {
				i, v, open := reflect.Select(probe)
				switch {
				case i == n:
					return canceled()
				case i < n:
					return local(i, v, open)
				}
				continue
			}
			// With hasDefault set, the dependency answers its probe
			// without blocking: index -1 means nothing was ready.
			i, cell, open, err := r.goSourceNativeChoose(ctx, native, true)
			if err != nil {
				r.goSourceNativeChannelError(err)
				return bashPPMixedChoice{}, false
			}
			if i >= 0 {
				return bashPPMixedChoice{native: i, local: -1, cell: cell, open: open}, true
			}
		}
		if hasDefault {
			return bashPPMixedChoice{native: -1, local: -1}, true
		}
		if !armed {
			if !r.bashPPArmBeforeBlock(ctx) {
				return bashPPMixedChoice{}, false
			}
			armed = true
		}
		timer := time.NewTimer(delay)
		wait[n+1].Chan = reflect.ValueOf(timer.C)
		i, v, open := reflect.Select(wait)
		timer.Stop()
		switch {
		case i == n:
			return canceled()
		case i < n:
			return local(i, v, open)
		}
		delay = min(2*delay, bashPPMixedSelectMaxPoll)
	}
}
