package shellrt

import "fmt"

// AbsentResults is a source result-count failure. Its presence is independent
// of a callable's status and of the native zero placeholders used by its ABI.
type AbsentResults struct{ Want, Got int }

func (e *AbsentResults) Error() string {
	return fmt.Sprintf("assignment mismatch: %d variable(s) but %d value(s)", e.Want, e.Got)
}
func (*AbsentResults) ExitStatus() int { return 2 }

// MissingResults reports only a fresh result-count mismatch. A failure already
// raised while evaluating a scalar return is propagated without a second line.
func (p *Program) MissingResults(mark uint64, frame *ResultFrame, want int) error {
	got := 0
	if frame != nil {
		for i := 0; i < frame.Arity(); i++ {
			if frame.Present(i) {
				got++
			}
		}
	}
	err := &AbsentResults{Want: want, Got: got}
	if p.ShortFailureMark() == mark {
		p.Fail(err)
	}
	p.ShortFailure()
	return err
}

// ForwardResults transfers a completed invocation descriptor, including an
// absent slot, to its explicit caller. It does not inspect command status.
func ForwardResults(to, from *ResultFrame) error {
	if to == nil {
		return nil
	}
	if from == nil || len(to.slots) != len(from.slots) {
		return fmt.Errorf("bash++: forwarded result arity mismatch")
	}
	copy(to.slots, from.slots)
	return nil
}

// SetNamedResult observes a native named result after source defers. A retained
// capability describes the same declared result cell and must not be erased by
// its ordinary carrier value.
func SetNamedResult[T any](frame *ResultFrame, index int, value T) error {
	if frame == nil {
		return nil
	}
	if err := frame.slot(index); err != nil {
		return err
	}
	if frame.slots[index].capability != nil {
		return nil
	}
	return SetResult(frame, index, value)
}
