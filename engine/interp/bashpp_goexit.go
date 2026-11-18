package interp

// Sprint: #374; Story-ID: ce0b33ec46f8

// runtime.Goexit across the dependency bridge.
//
// A dependency function may end its calling goroutine with runtime.Goexit:
// testing.T.FailNow, Fatal, SkipNow and Skip all do. An interpreted caller
// reaches such a function through a bridged request, which the dependency
// process serves on a goroutine of its own, so the Goexit ends that serving
// goroutine and not the program goroutine which made the call. The worker
// reports the request as ended by Goexit, and the interpreter then does what
// Go does to the calling goroutine: every active interpreted frame is
// abandoned and runs its deferred calls, and no deferred call can recover it.
//
// The unwind reuses the panic halt (see bashpp_panic.go) with a private
// payload that recover never takes. When the unwind reaches the interpreted
// body of a callback the dependency raised — a test function invoked by the
// native testing goroutine — the callback answers with a "goexit" value and
// the worker calls runtime.Goexit on the goroutine which raised the callback,
// so the dependency observes the Goexit on the goroutine Go would have ended.

// bashPPGoexit is the payload of the unwind a runtime.Goexit starts.
type bashPPGoexit struct{}

const (
	bashPPGoexitKind = "goexit"
	bashPPGoexitText = "runtime.Goexit"
)

// bashPPCallbackGoexit reports a bridged request whose serving goroutine ended
// by runtime.Goexit instead of returning.
type bashPPCallbackGoexit struct{}

func (*bashPPCallbackGoexit) Error() string {
	return "gosource: dependency call ended its goroutine (runtime.Goexit)"
}

// bashPPRaiseGoexit abandons the executing goroutine's interpreted frames.
func (r *Runner) bashPPRaiseGoexit() {
	r.bashPPRaiseValue(bashPPGoexitText, bashPPGoexit{})
}

// bashPPGoexiting reports whether the newest unwind is a Goexit.
func (r *Runner) bashPPGoexiting() bool {
	p := &r.bashPPPanic
	if !p.active || len(p.values) == 0 {
		return false
	}
	_, ok := p.values[len(p.values)-1].(bashPPGoexit)
	return ok
}
