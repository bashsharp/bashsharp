package interp

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"strings"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// GoSourceTestProgram is the positioned, checked package view required by the
// testing runner. gosource.Program implements it without coupling this runtime
// package to the optional Go-source frontend.
// Implementations must retain original source identities and initializer order.
type GoSourceTestProgram interface {
	GoSourceAST() *syntax.File
	GoSourcePackage() string
	GoSourceInitializers() []string
	SourceAt(syntax.Pos) (string, uint, bool)
}

// GoSourceTestingT is the scheduler capability passed to an interpreted test.
// A real *testing.T implements it. The interpreter deliberately does not import
// testing: the dedicated host harness owns testing.Main and the test goroutine.
type GoSourceTestingT interface {
	Errorf(string, ...any)
	Logf(string, ...any)
	Fail()
	FailNow()
	SkipNow()
}

// GoSourceTestingSession owns one loaded, unchanged Go test package. Its
// functions execute through the ordinary interpreter; native dependency calls
// retain their separate bridge. One session and its Runner are serial resources.
// Close it before using the Runner for another program. Reset invalidates it.
type GoSourceTestingSession struct {
	runner  *Runner
	context context.Context
	program GoSourceTestProgram
	loading bool
	active  *goSourceTestingHandle
	closed  bool
}

type goSourceTestingHandle struct {
	session *GoSourceTestingSession
	target  GoSourceTestingT
	// capability is the imported testing type this handle stands for, "T" for
	// a test and "B" for a benchmark. Nested callbacks inherit it, because a
	// subtest of a test receives *testing.T and a sub-benchmark receives
	// *testing.B.
	capability string
	active     bool
}

type goSourceTestingExit struct {
	handle *goSourceTestingHandle
	skip   bool
}

// LoadGoSourceTests initializes a loaded package without rewriting its source
// or adding a main function. The caller must supply every applicable original
// package companion to gosource.Load with RunMain false. Only its original init
// functions are invoked here; test registration is an explicit later operation.
// A package named main is accepted, as `go test` accepts one: nothing here
// invokes a main function, and a test package is free to be the command's own.
func (r *Runner) LoadGoSourceTests(ctx context.Context, program GoSourceTestProgram) (*GoSourceTestingSession, error) {
	if program == nil || program.GoSourceAST() == nil || !program.GoSourceAST().GoSource {
		return nil, fmt.Errorf("gosource: testing requires a loaded Go package")
	}
	if r.Dialect() != syntax.LangBashPP {
		return nil, fmt.Errorf("gosource: testing requires a Bash++ runner")
	}
	if r.goSourceTesting != nil {
		return nil, fmt.Errorf("gosource: runner already owns a testing session")
	}
	r.Reset()
	session := &GoSourceTestingSession{runner: r, context: ctx, program: program, loading: true}
	r.goSourceTesting = session
	file := *program.GoSourceAST()
	file.Stmts = append([]*syntax.Stmt(nil), program.GoSourceAST().Stmts...)
	for _, name := range program.GoSourceInitializers() {
		pos := file.Pos()
		file.Stmts = append(file.Stmts, &syntax.Stmt{Cmd: &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: name, ValuePos: pos, ValueEnd: pos}}, Lparen: pos, Rparen: pos}})
	}
	if err := r.Run(ctx, &file); err != nil {
		session.Close()
		return nil, err
	}
	session.loading = false
	return session, nil
}

// Close releases this package's dependency process. It is idempotent and does
// not close another session that may have replaced this one after Reset.
func (s *GoSourceTestingSession) Close() {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	if s.runner.goSourceTesting == s {
		s.runner.closeGoSourceBridge()
		s.runner.goSourceTesting = nil
	}
}

// Run invokes one original func(*testing.T) body in the caller's testing
// goroutine. Generated testing.InternalTest callbacks can call this method
// directly; no original test body is compiled into their native harness.
// FailNow and SkipNow unwind interpreted defers before reaching the scheduler.
func (s *GoSourceTestingSession) Run(ctx context.Context, name string, target GoSourceTestingT) error {
	if s == nil || s.closed || s.runner.goSourceTesting != s {
		return fmt.Errorf("gosource: testing session is closed or reset")
	}
	return s.runFunction(ctx, name, s.runner.bashPPFuncs[name], target, "T", false, nil)
}
func (s *GoSourceTestingSession) runFunction(ctx context.Context, name string, fn *bashPPFunc, target GoSourceTestingT, capability string, nested bool, cleanup *goSourceTestingHandle) (err error) {
	if s == nil || s.closed || s.runner.goSourceTesting != s {
		return fmt.Errorf("gosource: testing session is closed or reset")
	}
	if target == nil {
		return fmt.Errorf("gosource: nil testing scheduler capability")
	}
	if s.active != nil && !nested {
		return fmt.Errorf("gosource: overlapping test callbacks are not supported")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := s.runner
	if !nested {
		var finishSignalRun func()
		ctx, finishSignalRun = r.beginAsyncSignalRun(ctx)
		defer finishSignalRun()
	}
	previousTaskPolicy := r.bashPPHostedTask
	r.bashPPHostedTask = taskPolicy(ctx)
	defer func() { r.bashPPHostedTask = previousTaskPolicy }()
	defer r.withDeclarations(ctx)()
	if fn == nil {
		return fmt.Errorf("gosource: test function %q is not loaded", name)
	}
	params := bashppParams(fn.params())
	expectedParams := 1
	if cleanup != nil {
		expectedParams = 0
	}
	if len(params) != expectedParams || len(fn.results()) != 0 {
		return fmt.Errorf("gosource: invalid testing callback signature for %s", name)
	}
	if cleanup == nil {
		declared := params[0].declared
		alias, typ, ok := strings.Cut(strings.TrimPrefix(declared, "*"), ".")
		if !strings.HasPrefix(declared, "*") || !ok || typ != capability || r.bashPPImports[alias] != "testing" {
			return fmt.Errorf("gosource: %s must receive the imported *testing.%s, got %s", name, capability, declared)
		}
	}
	previousHandle := s.active
	handle := cleanup
	if handle == nil {
		handle = &goSourceTestingHandle{session: s, target: target, capability: capability}
	}
	previousActive := handle.active
	handle.active = true
	s.active = handle
	previousExit, previousExpandExit := r.exit, r.expandRunExit
	previousGo, previousFile, previousContext := r.bashPPGoSource, r.bashPPGoSourceFile, r.ectx
	r.bashPPGoSource, r.bashPPGoSourceFile = true, s.program.GoSourceAST()
	r.fillExpandConfig(ctx)
	r.exit = exitStatus{}
	r.expandRunExit = exitStatus{}
	defer func() {
		handle.active = previousActive
		s.active = previousHandle
		r.exit, r.expandRunExit = previousExit, previousExpandExit
		r.bashPPGoSource, r.bashPPGoSourceFile = previousGo, previousFile
		r.fillExpandConfig(previousContext)
		if v := recover(); v != nil {
			control, ok := v.(goSourceTestingExit)
			if !ok || control.handle != handle {
				panic(v)
			}
			if control.skip {
				target.SkipNow()
			} else {
				target.FailNow()
			}
		}
	}()
	var args []string
	if cleanup == nil {
		cell := &bashPPCell{vr: expand.NewObject(handle), declType: params[0].typ}
		var ok bool
		args, ok = r.bashPPBindCall(fn, []string{"testing." + capability + "@host"}, nil, []*bashPPCell{cell}, nil, 1)
		if !ok {
			return fmt.Errorf("gosource: cannot bind test callback %s", name)
		}
	}
	recoverMark := r.bashPPRecoverSeq
	r.bashPPInvoke(ctx, fn, args)
	if r.exit.err != nil {
		return r.exit.err
	}
	if code := r.testingCallbackStatus(recoverMark); code != 0 {
		return ExitStatus(code)
	}
	return ctx.Err()
}

// testingCallbackStatus reduces the runner state left by one returned test
// callback to the status that actually failed it. recoverMark is
// [Runner.bashPPRecoverSeq] sampled immediately before that one callback ran.
//
// A Go `func(*testing.T)` returns no value and has no exit status, so the
// residual truthiness of whatever statement happened to run last must not
// decide the test outcome. `recover()` is the case the corpus hits: with
// nothing to recover it deliberately reports status 1 (see
// [Runner.bashPPPredeclared]), so an original body ending in
// `defer func() { recover() }()` returned status 1 and failed a test that
// Go passes.
//
// The reduction is deliberately NOT "status 1 is not a failure". A status 1
// is discarded only when all of the following hold, so that a native bridge
// or runtime failure which happens to report 1 still fails its test:
//
//   - the status carries recover's own provenance stamp, and that stamp is the
//     current one — recover reported it and nothing has reported since;
//   - the stamp was issued while THIS callback ran, so a stamp left by an
//     earlier callback cannot excuse this one;
//   - no terminating condition is pending: an explicit exit, a fatal exit, or
//     a panic still unwinding out of the body.
//
// A fatal interpreter error is already returned by the caller through exit.err
// before this runs, and every other status — including [bashPPPanicStatus] —
// is returned untouched.
func (r *Runner) testingCallbackStatus(recoverMark uint64) uint8 {
	recovered := r.exit.recoverSeq != 0 &&
		r.exit.recoverSeq == r.bashPPRecoverSeq &&
		r.exit.recoverSeq > recoverMark
	if r.exit.code == 1 && recovered &&
		!r.exit.exiting && !r.exit.fatalExit && !r.bashPPPanicking() {
		return 0
	}
	return r.exit.code
}

// bashPPTestingCall handles only the host testing capability. All ordinary
// Bash calls and imported dependency calls continue through their usual paths.
func (r *Runner) bashPPTestingCall(call *syntax.BashPPCall) bool {
	invoke, handled := r.bashPPTestingCapture(call)
	if invoke != nil {
		invoke()
	}
	return handled
}

func (r *Runner) bashPPTestingCapture(call *syntax.BashPPCall) (func(), bool) {
	handle, args, handled, err := r.bashPPTestingArguments(call)
	if err != nil {
		r.exit.fatal(err)
		return nil, true
	}
	if !handled {
		return nil, false
	}
	return func() { r.bashPPTestingInvoke(handle, call.Fun[1].Value, args) }, true
}

func (r *Runner) bashPPTestingArguments(call *syntax.BashPPCall) (*goSourceTestingHandle, []any, bool, error) {
	if !r.bashPPGoSource || r.goSourceTesting == nil || len(call.Fun) != 2 || r.bashPPScope == nil {
		return nil, nil, false, nil
	}
	cell := r.bashPPScope.lookup(call.Fun[0].Value).view()
	if cell == nil {
		return nil, nil, false, nil
	}
	handle, ok := cell.vr.Obj.(*goSourceTestingHandle)
	if !ok {
		return nil, nil, false, nil
	}
	fail := func(err error) (*goSourceTestingHandle, []any, bool, error) { return nil, nil, true, err }
	if !handle.active || handle.session != r.goSourceTesting || handle.session.active != handle {
		return fail(errors.New("gosource: stale testing capability"))
	}
	var args []any
	for _, expr := range call.ArgExprs {
		if literal, ok := expr.(*syntax.BashPPFuncLit); ok {
			fn, _ := r.bashPPMakeClosure(literal)
			args = append(args, fn)
			continue
		}
		value, err := r.bashPPEvalScalarExpr(expr)
		if err != nil {
			return fail(err)
		}
		switch value.value.Kind() {
		case constant.String:
			args = append(args, constant.StringVal(value.value))
		case constant.Bool:
			args = append(args, constant.BoolVal(value.value))
		case constant.Int:
			n, exact := constant.Int64Val(value.value)
			if !exact {
				return fail(errors.New("gosource: testing argument is outside int64"))
			}
			args = append(args, int(n))
		case constant.Float:
			n, _ := constant.Float64Val(value.value)
			args = append(args, n)
		default:
			return fail(errors.New("gosource: testing method requires scalar arguments in this driver slice"))
		}
	}
	return handle, args, true, nil
}

func (r *Runner) bashPPTestingInvoke(handle *goSourceTestingHandle, method string, args []any) {
	fail := func(err error) { r.exit.fatal(err) }
	if !handle.active || handle.session != r.goSourceTesting || handle.session.active != handle {
		fail(errors.New("gosource: stale testing capability"))
		return
	}
	message := func() (string, error) {
		if len(args) == 0 {
			return "", errors.New("gosource: formatted testing call requires a format")
		}
		format, ok := args[0].(string)
		if !ok {
			return "", errors.New("gosource: testing format is not a string")
		}
		return fmt.Sprintf(format, args[1:]...), nil
	}
	switch method {
	case "Run", "Cleanup":
		if _, err := r.bashPPTestingCallback(handle, method, args); err != nil {
			fail(err)
		}
		return
	case "Helper":
		if len(args) != 0 {
			fail(errors.New("gosource: testing.Helper takes no arguments"))
		}
		// A scheduler helper mark cannot describe an interpreted stack. Retain
		// source positions in interpreter errors; no native helper frame is claimed.
		return
	case "Errorf", "Logf", "Fatalf", "Skipf":
		text, err := message()
		if err != nil {
			fail(err)
			return
		}
		if method == "Errorf" || method == "Fatalf" {
			handle.target.Errorf("%s", text)
		} else {
			handle.target.Logf("%s", text)
		}
		if method == "Fatalf" || method == "Skipf" {
			panic(goSourceTestingExit{handle: handle, skip: method == "Skipf"})
		}
	case "Error", "Log", "Fatal", "Skip":
		text := fmt.Sprintln(args...)
		if method == "Error" || method == "Fatal" {
			handle.target.Errorf("%s", text)
		} else {
			handle.target.Logf("%s", text)
		}
		if method == "Fatal" || method == "Skip" {
			panic(goSourceTestingExit{handle: handle, skip: method == "Skip"})
		}
	case "Fail", "FailNow", "SkipNow":
		if len(args) != 0 {
			fail(errors.New("gosource: testing control method takes no arguments"))
			return
		}
		if method != "SkipNow" {
			handle.target.Fail()
		}
		if method != "Fail" {
			panic(goSourceTestingExit{handle: handle, skip: method == "SkipNow"})
		}
	default:
		fail(fmt.Errorf("gosource: testing.%s.%s is not implemented by this driver slice", handle.capabilityName(), method))
		return
	}
}

// A private control panic crosses every interpreted frame without converting
// FailNow into shell exit (which would discard Go defers). It is consumed only
// by the owning scheduler callback after each frame's interpreted defers run.
func (r *Runner) bashPPTestingUnwind(ctx context.Context, mark int) {
	if v := recover(); v != nil {
		if _, ok := v.(goSourceTestingExit); ok {
			r.bashPPRunDefers(ctx, mark)
		}
		panic(v)
	}
}

// Catch only scheduler control while running one cleanup, allowing the rest of
// the frame's defers to run even if this cleanup itself calls FailNow/SkipNow.
func bashPPTestingCatch(call func()) (control *goSourceTestingExit) {
	defer func() {
		if v := recover(); v != nil {
			if exit, ok := v.(goSourceTestingExit); ok {
				control = &exit
			} else {
				panic(v)
			}
		}
	}()
	call()
	return nil
}

// goSourceTestingCancelGrace bounds how long a cancelled callback's abandoned
// defers may run. It is a shutdown grace window, not a test deadline: the
// defers this discharges are cleanup, and cleanup that has not finished in
// this long is wedged rather than slow.
const goSourceTestingCancelGrace = 5 * time.Second

// bashPPTestingCancelUnwind discharges an abandoned callback frame's
// interpreted defers when the session context is cancelled while an original
// body is still running.
//
// Cancellation abandons the frame the way a hard shell `exit` does, and that
// path deliberately drops the deferred-call stack rather than running it. For
// a hosted test callback that would silently strip obligations the driver
// contract requires to be finished or explicitly failed: the body's own
// defers, and — through them — anything it registered with the scheduler.
// A Go `defer` is not conditional on the reason the frame is leaving.
//
// The defers run detached from the cancelled context, because every call they
// make would otherwise abort on the same cancellation they exist to clean up
// after, and with a cleared exit status so the cancellation is not re-reported
// by each deferred call in turn. The caller's status is restored afterwards:
// this discharges obligations, it does not turn a cancelled callback into a
// passing one. [GoSourceTestingSession.runFunction] still returns ctx.Err().
//
// It reports whether it ran the frame's defers, so the abandoning caller drops
// the stack itself when it did not.
func (r *Runner) bashPPTestingCancelUnwind(ctx context.Context, mark int) bool {
	if r.goSourceTesting == nil || r.goSourceTesting.active == nil {
		return false
	}
	if ctx.Err() == nil || len(r.bashPPDeferStack) <= mark {
		return false
	}
	previous := r.exit
	// Restored on a control unwind too: a defer may call FailNow, which leaves
	// through a control panic, and the abandoning caller's status must survive it.
	defer func() {
		r.bashPPDeferStack = r.bashPPDeferStack[:mark]
		r.exit = previous
	}()
	r.exit = exitStatus{}
	// Detached from the cancellation, but not unbounded. A defer is ordinary
	// interpreted code and may block forever -- a receive with no partner is
	// the shape the corpus already writes -- and cancellation is a request to
	// shut down, so a wedged defer must not pin the harness in place of the
	// obligations it was meant to discharge. Whatever has not finished within
	// the grace window is abandoned exactly as it was before this ran.
	detached, done := context.WithTimeout(context.WithoutCancel(ctx), goSourceTestingCancelGrace)
	defer done()
	r.bashPPRunDefers(detached, mark)
	return true
}
