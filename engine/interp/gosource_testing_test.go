//go:build full

package interp_test

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceTestingOriginalErrors(t *testing.T) {
	pins := map[string]string{
		"errors_test.go":  "7827d7e3ab7cc317a254ccf76158069b80c5b55b34ca45a3e6d799ad185f70ea",
		"example_test.go": "db75531e0575928a48a16d2c9769da59e719da091a31a4f03c3d5aeeca6be648",
		"join_test.go":    "09f9cc24acc7749d8f7197c6b90dc43223708c8f6412825e29ea3984bc6afdbc",
		"wrap_test.go":    "a098c0f752095df4614abf02925e52d2d35228e6ac272925dd4d9fd59f7e0cf6",
	}
	var sources []gosource.Source
	dir := filepath.Join(runtime.GOROOT(), "src", "errors")
	for name, digest := range pins {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
			t.Fatalf("requires pinned original Go1.27 errors source: %s", path)
		}
		sources = append(sources, gosource.Source{Name: path, Data: data})
	}
	program, err := gosource.Load(sources, gosource.Options{Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if program.Package != "errors_test" || len(program.Sources) != 4 {
		t.Fatalf("wrong original package: %#v", program.Sources)
	}
	var out, errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load original package: %v; %s", err, errs.String())
	}
	defer session.Close()
	// Registration still identifies all ten original roots through the narrow
	// program view, with source paths and positions checked independently.
	registered, err := session.Tests()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]token.Position{}
	fset := token.NewFileSet()
	for _, source := range sources {
		file, err := parser.ParseFile(fset, source.Name, source.Data, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				expected[fn.Name.Name] = fset.Position(fn.Pos())
			}
		}
	}
	if len(registered) != len(expected) || len(expected) != 10 {
		t.Fatalf("registration count %d, expected %d", len(registered), len(expected))
	}
	for _, test := range registered {
		pos, ok := expected[test.Name]
		if !ok || test.Source != pos.Filename || test.Line != uint(pos.Line) || test.Column != uint(pos.Column) {
			t.Fatalf("registration lost source identity: %+v vs %+v", test, pos)
		}
		delete(expected, test.Name)
	}
	for _, name := range []string{"TestNewEqual", "TestErrorMethod"} {
		t.Run(name, func(t *testing.T) {
			if err := session.Run(ctx, name, t); err != nil {
				t.Fatalf("original body: %v; %s", err, errs.String())
			}
		})
	}
	for _, source := range sources {
		after, err := os.ReadFile(source.Name)
		if err != nil || !bytes.Equal(after, source.Data) {
			t.Fatalf("original source changed: %s", source.Name)
		}
	}
}

type testingRecorder struct {
	log                      []string
	failed, failNow, skipped bool
}

func (t *testingRecorder) Errorf(format string, args ...any) {
	t.failed = true
	t.log = append(t.log, fmt.Sprintf(format, args...))
}
func (t *testingRecorder) Logf(format string, args ...any) {
	t.log = append(t.log, fmt.Sprintf(format, args...))
}
func (t *testingRecorder) Fail()    { t.failed = true }
func (t *testingRecorder) FailNow() { t.failed = true; t.failNow = true }
func (t *testingRecorder) SkipNow() { t.skipped = true }

func TestGoSourceTestingControlAndLifecycle(t *testing.T) {
	// Authored driver fixtures exercise scheduler behavior; upstream bodies above
	// are loaded from the SDK, never rewritten to test this adapter.
	source := `package specimen
import "testing"
var initialized int
func init(){initialized=7}
func nested(t *testing.T){defer t.Log("nested defer");t.FailNow();t.Log("unreachable nested")}
func TestFatal(t *testing.T){defer t.Log("outer defer");nested(t);t.Log("unreachable outer")}
func TestSkip(t *testing.T){defer t.Log("skip defer");t.SkipNow();t.Log("unreachable skip")}
func TestInit(t *testing.T){if initialized!=7{t.Errorf("bad init: %d",initialized)}}
func TestDeferControl(t *testing.T){defer t.Log("last");defer t.FailNow();defer t.Log("first")}
func TestDeferCapture(t *testing.T){x:="before";defer t.Log(x);x="after"}
var cleanupOrder string
func TestNested(t *testing.T){
 t.Cleanup(func(){cleanupOrder += "outer;"})
 if !t.Run("child",func(t *testing.T){t.Cleanup(func(){cleanupOrder += "child;"});t.SkipNow();cleanupOrder += "unreachable;"}) {t.Error("skipped child Run returned false")}
 if cleanupOrder != "child;" {t.Errorf("child cleanup order: %s",cleanupOrder)}
}
func TestNestedFatal(t *testing.T){
 t.Cleanup(func(){t.Log("outer cleanup")})
 if t.Run("child",func(t *testing.T){defer t.Log("child defer");t.Cleanup(func(){t.Log("child cleanup")});t.Fatal("nested deliberate")}) {t.Error("failed child Run returned true")} else {t.Log("false result propagated")}
 t.Log("after child")
}
func TestAfterCleanup(t *testing.T){if cleanupOrder != "child;outer;"{t.Errorf("final cleanup order: %s",cleanupOrder)}}
func TestUnsupported(t *testing.T){t.Parallel()}
func TestFailure(t *testing.T){t.Errorf("deliberate failure %s","kept")}
`
	program, err := gosource.Parse(strings.NewReader(source), "driver_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()

	if os.Getenv("BASHPP_TESTING_SCHEDULER_CHILD") == "1" {
		t.Run("FatalScheduler", func(t *testing.T) {
			if err := session.Run(ctx, "TestFatal", t); err != nil {
				t.Fatal(err)
			}
			t.Fatal("unreachable after FailNow")
		})
		t.Run("NestedFatalScheduler", func(t *testing.T) {
			if err := session.Run(ctx, "TestNestedFatal", t); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("Reentry", func(t *testing.T) {
			if err := session.Run(ctx, "TestInit", t); err != nil {
				t.Fatal(err)
			}
		})
		return
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGoSourceTestingControlAndLifecycle$", "-test.v")
	command.Env = append(os.Environ(), "BASHPP_TESTING_SCHEDULER_CHILD=1", "GOSH_PROG=")
	output, childErr := command.CombinedOutput()
	if childErr == nil || !bytes.Contains(output, []byte("--- FAIL: TestGoSourceTestingControlAndLifecycle/FatalScheduler")) || !bytes.Contains(output, []byte("--- PASS: TestGoSourceTestingControlAndLifecycle/Reentry")) || bytes.Contains(output, []byte("unreachable after FailNow")) {
		t.Fatalf("real FailNow scheduler/reentry: %v\n%s", childErr, output)
	}
	position := -1
	for _, marker := range []string{"nested deliberate", "child defer", "child cleanup", "false result propagated", "after child", "outer cleanup"} {
		found := bytes.Index(output, []byte(marker))
		if found <= position {
			t.Fatalf("nested fatal/cleanup ordering at %s: %s", marker, output)
		}
		position = found
	}
	t.Run("NestedScheduler", func(t *testing.T) {
		if err := session.Run(ctx, "TestNested", t); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("AfterCleanup", func(t *testing.T) {
		if err := session.Run(ctx, "TestAfterCleanup", t); err != nil {
			t.Fatal(err)
		}
	})
	afterSkip := false
	t.Run("RealSkip", func(t *testing.T) {
		if err := session.Run(ctx, "TestSkip", t); err != nil {
			t.Fatal(err)
		}
		afterSkip = true
	})
	if afterSkip {
		t.Fatal("SkipNow returned to test body")
	}
	for _, check := range []struct {
		name, log string
		fatal     bool
	}{
		{"TestDeferControl", "first\n|last\n", true},
		{"TestDeferCapture", "before\n", false},
	} {
		recorded := &testingRecorder{}
		if err := session.Run(ctx, check.name, recorded); err != nil || strings.Join(recorded.log, "|") != check.log || recorded.failNow != check.fatal {
			t.Fatalf("%s: %v %#v", check.name, err, recorded)
		}
	}
	fatal := &testingRecorder{}
	if err := session.Run(ctx, "TestFatal", fatal); err != nil {
		t.Fatalf("invoke fatal: %v; stderr=%s", err, errs.String())
	}
	if !fatal.failNow || strings.Join(fatal.log, "|") != "nested defer\n|outer defer\n" {
		t.Fatalf("fatal control: %#v", fatal)
	}
	skipped := &testingRecorder{}
	if err := session.Run(ctx, "TestSkip", skipped); err != nil {
		t.Fatal(err)
	}
	if !skipped.skipped || strings.Join(skipped.log, "|") != "skip defer\n" {
		t.Fatalf("skip control: %#v", skipped)
	}
	initialized := &testingRecorder{}
	if err := session.Run(ctx, "TestInit", initialized); err != nil || initialized.failed {
		t.Fatalf("init: %v %#v", err, initialized)
	}
	failed := &testingRecorder{}
	if err := session.Run(ctx, "TestFailure", failed); err != nil || !failed.failed {
		t.Fatalf("failure swallowed: %v %#v", err, failed)
	}
	if err := session.Run(ctx, "TestUnsupported", &testingRecorder{}); err == nil || !strings.Contains(err.Error(), "Parallel is not implemented") {
		t.Fatalf("unsupported testing method was accepted: %v", err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if err := session.Run(cancelled, "TestInit", &testingRecorder{}); err != context.Canceled {
		t.Fatalf("cancelled callback: %v", err)
	}
	if err := runner.Run(ctx, program.File); err == nil {
		t.Fatal("ordinary Run stole active test session")
	}
	runner.Reset()
	if err := session.Run(ctx, "TestInit", &testingRecorder{}); err == nil {
		t.Fatal("Reset did not invalidate testing capability")
	}
}

func TestGoSourceTestingDiscoveryRejectsInvalid(t *testing.T) {
	for _, source := range []string{
		"package specimen; func TestBad() {}",
		"package specimen; import \"testing\"; func TestBad(t *testing.T) bool {return true}",
		"package specimen; import \"testing\"; func TestBad[T any](t *testing.T) {}",
		"package specimen; import \"testing\"; func TestBad(t *testing.B) {}",
	} {
		program, err := gosource.Parse(strings.NewReader(source), "invalid_registration.go", gosource.Options{})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		session, err := runner.LoadGoSourceTests(context.Background(), program)
		if err != nil {
			t.Fatal(err)
		}
		_, err = session.Tests()
		session.Close()
		if err == nil {
			t.Fatalf("invalid Test registration accepted: %s", source)
		}
	}
}

// TestGoSourceTestingRecoverGuardedCallback pins the shapes the pinned corpus
// writes around its scheduler callbacks, using separately authored fixtures.
//
// The guarded shape is the one that measured a defect: an original body whose
// last act is `defer func() { recover() }()`, as wrap_test.go's TestAsValidation
// subtests write it. `recover` reports "nothing to recover" through the exit
// status, Go source form aborts a statement that reports failure, and the
// residual status then reached the callback outcome — so a test Go passes
// failed twice over, at the root and inside t.Run.
func TestGoSourceTestingRecoverGuardedCallback(t *testing.T) {
	source := `package specimen
import "testing"
func helperRun(t *testing.T, name string){
 t.Helper()
 t.Run(name, func(t *testing.T){ t.Log("helper subtest") })
}
func TestRootRecover(t *testing.T){ defer func(){ recover() }(); t.Log("root body") }
func TestUnrecoveredPanic(t *testing.T){ panic("deliberate") }
func TestSubRecover(t *testing.T){
 t.Run("guarded", func(t *testing.T){ defer func(){ recover() }(); t.Log("sub body") })
}
func TestEmptyNames(t *testing.T){
 for i := 0; i < 3; i++ { t.Run("", func(t *testing.T){ t.Log("sub") }) }
}
func TestHelperCapability(t *testing.T){ helperRun(t, "alpha"); helperRun(t, "beta") }
func TestStillFailsOnDiagnostic(t *testing.T){ var ch chan int; ch <- 1 }
`
	program, err := gosource.Parse(strings.NewReader(source), "recover_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()
	// Every guarded shape reaches the real scheduler and passes, including the
	// nested callback and the capability handed to a helper function.
	for _, name := range []string{
		"TestRootRecover", "TestSubRecover",
		"TestEmptyNames", "TestHelperCapability",
	} {
		errs.Reset()
		t.Run(name, func(t *testing.T) {
			if err := session.Run(ctx, name, t); err != nil {
				t.Fatalf("original body: %v; %s", err, errs.String())
			}
		})
	}
	// The exemption must not become a blanket one. An unrecovered panic and an
	// interpreter diagnostic each still fail their callback rather than being
	// discarded along with the status.
	for _, name := range []string{"TestUnrecoveredPanic", "TestStillFailsOnDiagnostic"} {
		errs.Reset()
		recorded := &testingRecorder{}
		if err := session.Run(ctx, name, recorded); err == nil {
			t.Fatalf("%s was discarded: %#v %s", name, recorded, errs.String())
		}
	}
}

// TestGoSourceTestingRecoverProvenance pins that the recover reduction is a
// narrow one, keyed to recover's own provenance stamp, rather than a rule that
// status 1 cannot fail a hosted callback.
//
// Only the first body is recover's own answer; the rest reach a failure after
// a recover has already stamped the runner, and must still fail. Their
// failures travel by exit.err, by the panic status, or by the capability, so
// this test pins the rule end to end rather than discriminating the narrowed
// condition from a blanket one -- that discrimination is
// [TestTestingCallbackStatusProvenance], which holds the status at 1 and
// varies only the provenance.
func TestGoSourceTestingRecoverProvenance(t *testing.T) {
	source := `package specimen
import "testing"
// Recover's own answer, and the only body here that may pass.
func TestGuardOnly(t *testing.T){ defer func(){ recover() }(); t.Log("body") }
// The guard runs, then the body deliberately panics anyway. The panic is
// unrecovered -- the deferred recover already ran and consumed nothing -- so
// the stamp is stale and the callback must fail.
func TestGuardThenDeliberatePanic(t *testing.T){
 recover()
 t.Log("guard ran")
 panic("deliberate")
}
// A recover stamps the runner, then the original body reports failure the way
// a test does. t.Fatal must remain terminating.
func TestGuardThenFatal(t *testing.T){
 recover()
 t.Fatal("original fatal")
}
// A recover stamps the runner, then a real native bridge failure occurs. The
// bridge diagnostic must reach the callback outcome, not be excused.
func TestGuardThenBridgeFailure(t *testing.T){
 recover()
 var ch chan int
 ch <- 1
}
`
	program, err := gosource.Parse(strings.NewReader(source), "provenance_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()

	errs.Reset()
	t.Run("TestGuardOnly", func(t *testing.T) {
		if err := session.Run(ctx, "TestGuardOnly", t); err != nil {
			t.Fatalf("guarded body: %v; %s", err, errs.String())
		}
	})

	// The reduction must not reach any of these. Each is run through a
	// recorder so a body that fails via the capability is caught even when it
	// reports no error, and via the returned error for the rest.
	//
	// A nil-channel send has no runnable partner, so it is bounded by its own
	// deadline rather than the whole test's; the point is that it does not
	// return a pass.
	for _, name := range []string{
		"TestGuardThenDeliberatePanic",
		"TestGuardThenFatal",
		"TestGuardThenBridgeFailure",
	} {
		errs.Reset()
		recorded := &testingRecorder{}
		caseCtx, caseCancel := context.WithTimeout(ctx, 5*time.Second)
		err := session.Run(caseCtx, name, recorded)
		caseCancel()
		if err == nil && !recorded.failed {
			t.Errorf("%s was discarded: err=nil recorder=%#v stderr=%s",
				name, recorded, errs.String())
			continue
		}
		t.Logf("%s failed as required: err=%v failed=%v stderr=%q",
			name, err, recorded.failed, errs.String())
	}
}

// cancellingRecorder is a scheduler capability that owns nested callbacks and
// cleanups the way testing.T does, and cancels the session context the moment
// the interpreted body reaches a chosen marker. It exists so that cancellation
// can be observed WHILE a nested callback body is running, which the
// root-entry cancellation check in [interp.GoSourceTestingSession.Run] cannot
// reach.
type cancellingRecorder struct {
	log      []string
	failed   bool
	failNow  bool
	skipped  bool
	trigger  string
	cancel   context.CancelFunc
	cleanups []func()
	children []*cancellingRecorder
}

func (t *cancellingRecorder) record(text string) {
	t.log = append(t.log, text)
	if t.trigger != "" && strings.Contains(text, t.trigger) && t.cancel != nil {
		t.cancel()
	}
}

func (t *cancellingRecorder) Errorf(format string, args ...any) {
	t.failed = true
	t.record(fmt.Sprintf(format, args...))
}
func (t *cancellingRecorder) Logf(format string, args ...any) { t.record(fmt.Sprintf(format, args...)) }
func (t *cancellingRecorder) Fail()                           { t.failed = true }
func (t *cancellingRecorder) FailNow()                        { t.failed = true; t.failNow = true }
func (t *cancellingRecorder) SkipNow()                        { t.skipped = true }
func (t *cancellingRecorder) Cleanup(fn func())               { t.cleanups = append(t.cleanups, fn) }

// Run mirrors the scheduler's parent/child structure: the child owns its own
// capability and its cleanups run before Run returns, in reverse order.
func (t *cancellingRecorder) Run(name string, fn func(*cancellingRecorder)) bool {
	child := &cancellingRecorder{trigger: t.trigger, cancel: t.cancel}
	t.children = append(t.children, child)
	fn(child)
	for i := len(child.cleanups) - 1; i >= 0; i-- {
		child.cleanups[i]()
	}
	t.log = append(t.log, child.log...)
	if child.failed {
		t.failed = true
	}
	return !child.failed
}

func (t *cancellingRecorder) runCleanups() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}

// TestGoSourceTestingNestedCallbackCancellation pins that cancelling the
// session context while a NESTED callback body is in flight finishes every
// outstanding obligation rather than silently reporting a pass: the child
// callback fails, its interpreted defers and cleanups still run, the parent
// observes the false Run result, and the root callback reports the
// cancellation to its caller.
//
// The existing lifecycle control only cancels before the root callback starts,
// where the entry check answers immediately; nothing pinned what happens once
// a body is already running inside a child.
func TestGoSourceTestingNestedCallbackCancellation(t *testing.T) {
	source := `package specimen
import "testing"
func TestNestedCancel(t *testing.T){
 defer t.Log("outer defer")
 t.Cleanup(func(){ t.Log("outer cleanup") })
 if t.Run("child", func(t *testing.T){
  defer t.Log("child defer")
  t.Cleanup(func(){ t.Log("child cleanup") })
  t.Log("child cancels here")
  t.Log("child continued")
 }) { t.Log("child returned true") } else { t.Log("child returned false") }
 t.Log("outer after child")
}
`
	program, err := gosource.Parse(strings.NewReader(source), "cancel_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()

	bodyCtx, stop := context.WithCancel(ctx)
	defer stop()
	recorded := &cancellingRecorder{trigger: "child cancels here", cancel: stop}
	err = session.Run(bodyCtx, "TestNestedCancel", recorded)
	recorded.runCleanups()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled nested callback reported %v; stderr=%s", err, errs.String())
	}
	joined := strings.Join(recorded.log, "|")
	// Every outstanding obligation is discharged rather than dropped: the
	// child's own interpreted defer and its scheduler cleanup run, and the
	// abandoned parent's interpreted defer and cleanup run too. The parent's
	// remaining STATEMENTS are not resumed -- cancellation abandons the body,
	// it does not let it keep running -- so no marker after the child's Run is
	// expected.
	for _, want := range []string{
		"child defer", "child cleanup", "outer defer", "outer cleanup",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("cancellation dropped %q; log=%q stderr=%s", want, joined, errs.String())
		}
	}
	if !recorded.failed {
		t.Errorf("cancellation reported a pass; log=%q", joined)
	}
}

// TestGoSourceTestingCancelledDeferIsBounded pins that discharging an
// abandoned callback's defers cannot pin the harness. The defers run detached
// from the cancellation -- otherwise every call would abort on the very
// cancellation they clean up after -- so a defer that blocks forever, which a
// receive with no partner does, would hang were the detached context not
// itself bounded.
func TestGoSourceTestingCancelledDeferIsBounded(t *testing.T) {
	source := `package specimen
import "testing"
func TestWedgedDefer(t *testing.T){
 defer func(){ var ch chan int; <-ch }()
 t.Log("body cancels here")
 t.Log("unreachable")
}
`
	program, err := gosource.Parse(strings.NewReader(source), "wedged_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()

	bodyCtx, stop := context.WithCancel(ctx)
	defer stop()
	recorded := &cancellingRecorder{trigger: "body cancels here", cancel: stop}
	done := make(chan error, 1)
	go func() { done <- session.Run(bodyCtx, "TestWedgedDefer", recorded) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled callback reported %v; stderr=%s", err, errs.String())
		}
	case <-time.After(90 * time.Second):
		t.Fatalf("wedged defer pinned the cancelled callback; stderr=%s", errs.String())
	}
}

// TestGoSourceTestingRecoverBindsValue pins the shape recorded as an open,
// unattributed gap while the guarded-callback repair was made: a deferred
// guard that recovers an ACTUAL panic, binds the recovered value and reports
// it through the capability. The measured symptom was exit status 2 with no
// diagnostic and no log. The guard must instead swallow the panic, deliver the
// recovered value, and let the callback pass.
func TestGoSourceTestingRecoverBindsValue(t *testing.T) {
	source := `package specimen
import "testing"
func TestBindRecovered(t *testing.T){
 defer func(){ v := recover(); t.Log(v) }()
 panic("deliberate")
}
func TestBindRecoveredHelper(t *testing.T){
 defer func(){ v := recover(); t.Log(v) }()
 helper()
}
func helper(){ panic("from helper") }
`
	program, err := gosource.Parse(strings.NewReader(source), "recover_value_fixture.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, nil, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load: %v %s", err, errs.String())
	}
	defer session.Close()
	for name, want := range map[string]string{
		"TestBindRecovered":       "deliberate",
		"TestBindRecoveredHelper": "from helper",
	} {
		errs.Reset()
		recorded := &testingRecorder{}
		if err := session.Run(ctx, name, recorded); err != nil {
			t.Fatalf("%s: %v; stderr=%s", name, err, errs.String())
		}
		if recorded.failed || !strings.Contains(strings.Join(recorded.log, "|"), want) {
			t.Errorf("%s: guard lost the recovered value: %#v stderr=%s", name, recorded, errs.String())
		}
	}
}
