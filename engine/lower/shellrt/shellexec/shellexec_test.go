package shellexec_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// newSession builds a session on the production backend, with buffered output
// and no inherited process environment.
func newSession(t *testing.T, opts ...shellrt.SessionOption) (*shellrt.Session, *lockedBuffer) {
	t.Helper()
	out := &lockedBuffer{}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })
	// The default factory comes before opts, so a test that passes its own
	// WithShellFactory overrides it: the factory runs once, after every
	// option has been applied.
	base := []shellrt.SessionOption{
		shellrt.WithStdio(devNull, out, out),
		shellrt.WithDir(t.TempDir()),
		clearEnviron(),
		shellrt.WithShellFactory(shellexec.New()),
	}
	s, err := shellrt.NewSession(append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, out
}

func clearEnviron() shellrt.SessionOption {
	return func(s *shellrt.Session) error {
		for _, pair := range s.Environ() {
			name, _, _ := strings.Cut(pair, "=")
			s.Unset(name)
		}
		return nil
	}
}

func TestExitTrapRunsExactlyOnceOnClose(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "trap 'echo bye' EXIT; echo body"); err != nil {
		t.Fatal(err)
	}
	// A region must not imply an exit, so nothing has fired yet.
	if got, want := out.String(), "body\n"; got != want {
		t.Fatalf("output %q, want %q: the EXIT trap fired per region", got, want)
	}
	if err := s.Shell(t.Context(), "echo second"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "body\nsecond\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "body\nsecond\nbye\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	// Close is idempotent, and so is the EXIT trap.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "body\nsecond\nbye\n"; got != want {
		t.Fatalf("output %q, want %q: the EXIT trap ran twice", got, want)
	}
}

func TestCloseRunsExitTrapAfterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	s, out := newSession(t, shellrt.WithContext(ctx))
	if err := s.Shell(ctx, "trap 'echo cleanup' EXIT"); err != nil {
		t.Fatal(err)
	}
	// A cancelled program still has to release its resources: Close derives a
	// shutdown context that outlives the cancellation.
	cancel()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "cleanup\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestChildTaskRunsItsOwnExitTrap(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "trap 'echo parent-exit' EXIT"); err != nil {
		t.Fatal(err)
	}
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		return child.Shell(ctx, "trap 'echo child-exit' EXIT; echo in-child")
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	// The child's own Close ran when its body returned, before the parent's.
	if got, want := out.String(), "in-child\nchild-exit\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "in-child\nchild-exit\nparent-exit\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

// TestTaskRunsTheInheritedExitTrap pins bash++ task semantics rather than
// plain subshell semantics. Real bash does not run an inherited EXIT trap when
// a `( ... )` subshell exits, but the interpreter's task runtime clears
// inheritedExitTrap for a task snapshot (interp/bashpp_concurrency.go), so a
// task does run it. A task ending is a shell terminating, so Close runs it.
func TestTaskRunsTheInheritedExitTrap(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "trap 'echo bye' EXIT"); err != nil {
		t.Fatal(err)
	}
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		return child.Shell(ctx, "echo in-task")
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "in-task\nbye\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "in-task\nbye\nbye\n"; got != want {
		t.Fatalf("output %q, want %q: the parent's own EXIT trap did not run", got, want)
	}
}

func TestWithoutExitTraps(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithShellFactory(shellexec.New(shellexec.WithoutExitTraps())))
	if err := s.Shell(t.Context(), "trap 'echo bye' EXIT; echo body"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "body\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

// TestTypedExchangeThroughAChildSnapshot walks the whole boundary: functions,
// traps, arrays, options, status and cwd set on the typed side or by a region,
// carried into a child snapshot, mutated there, and left untouched at home.
func TestTypedExchangeThroughAChildSnapshot(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()
	start := s.Dir()
	if err := os.Mkdir(filepath.Join(start, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.Shell(ctx, "greet() { echo greet:$1; }; trap 'echo term' TERM; arr=(a 'b c'); set -u; cd sub; false"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Dir(), filepath.Join(start, "sub"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("Status() = %d, want 1", got)
	}
	if !s.Option("nounset") {
		t.Fatal("nounset was not projected")
	}
	if v, _ := s.Get("arr"); !slices.Equal(v.List, []string{"a", "b c"}) {
		t.Fatalf("arr = %+v", v)
	}

	// A typed-side write joins the exchange in the other direction.
	s.SetString("typed", "from-go")
	s.Set("built", shellrt.Var{Kind: shellrt.Indexed, List: []string{"x", "y z"}})

	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		if child.Dir() != filepath.Join(start, "sub") {
			return errors.New("child did not inherit cwd")
		}
		if child.Status() != 1 {
			return errors.New("child did not inherit status")
		}
		if !child.Option("nounset") {
			return errors.New("child did not inherit options")
		}
		if err := child.Shell(ctx, `greet "$typed"; trap -p TERM >/dev/null && echo has-term; printf '%s|' "${arr[@]}" "${built[@]}"; echo`); err != nil {
			return err
		}
		// Everything the child changes stays in the child.
		return child.Shell(ctx, "greet() { echo overridden; }; arr=(mutated); trap - TERM; set +u; cd ..; typed=child")
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}

	child := task.Session()
	if got, want := child.Dir(), start; got != want {
		t.Fatalf("child Dir() = %q, want %q", got, want)
	}
	if child.Option("nounset") {
		t.Fatal("the child's set +u did not take")
	}
	if v, _ := child.Get("arr"); !slices.Equal(v.List, []string{"mutated"}) {
		t.Fatalf("child arr = %+v", v)
	}

	// The parent is exactly as it was.
	if got, want := s.Dir(), filepath.Join(start, "sub"); got != want {
		t.Fatalf("parent Dir() = %q, want %q", got, want)
	}
	if !s.Option("nounset") {
		t.Fatal("the child cleared the parent's option")
	}
	if v, _ := s.Get("arr"); !slices.Equal(v.List, []string{"a", "b c"}) {
		t.Fatalf("parent arr = %+v", v)
	}
	if err := s.Shell(ctx, `greet "$typed"; trap -p TERM >/dev/null && echo parent-has-term`); err != nil {
		t.Fatal(err)
	}
	want := "greet:from-go\nhas-term\na|b c|x|y z|\ngreet:from-go\nparent-has-term\n"
	if got := out.String(); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestBashPPDialectRegion(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithShellFactory(shellexec.New(shellexec.BashPP())))
	// `:=` is a bash++ short declaration; plain bash parses it as a command.
	if err := s.Shell(t.Context(), "n := 42\necho $n"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "42\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestDialectOptionIsHonoured(t *testing.T) {
	t.Parallel()
	// The same source in the plain bash dialect is not a declaration, so it
	// fails as a command rather than silently meaning something else.
	s, _ := newSession(t, shellrt.WithShellFactory(shellexec.New(shellexec.Dialect(syntax.LangBash))))
	if err := s.Shell(t.Context(), "n := 42"); err == nil && s.Status() == 0 {
		t.Fatal("bash dialect accepted a bash++ declaration")
	}
}

func TestRunnerOptionsPassThrough(t *testing.T) {
	t.Parallel()
	s, out := newSession(t, shellrt.WithShellFactory(shellexec.New(
		shellexec.RunnerOptions(),
	)))
	if err := s.Shell(t.Context(), "echo ok"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "ok\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestConcurrentTasksShareNoBackend(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	if err := s.Shell(t.Context(), "base=parent"); err != nil {
		t.Fatal(err)
	}
	tasks := make([]*shellrt.Task, 4)
	for i := range tasks {
		tasks[i] = s.Go(func(ctx context.Context, child *shellrt.Session) error {
			child.SetString("mine", "task")
			if err := child.Shell(ctx, `[ "$base" = parent ] || exit 9; mine=$mine-$$`); err != nil {
				return err
			}
			if child.Status() != 0 {
				return errors.New("child lost the parent's variable")
			}
			return nil
		})
	}
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("mine"); ok {
		t.Fatal("a task's variable reached the parent")
	}
}

// artifact builds a program in a fresh module that declares its dependency and
// resolves it with `go mod tidy`, then runs the binary with no shell and no Go
// on PATH. This is the production-facing path: a compiled mixed artifact
// importing shellexec, with a real go.mod and go.sum, and no umbrella go.work.
func artifact(t *testing.T, source string) (string, string, int) {
	t.Helper()
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(this)))))
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module shellexecfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n\n"
	if strings.Contains(source, "github.com/bashsharp/bashsharp") {
		module += "require github.com/bashsharp/bashsharp v0.0.0\n\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	env := append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOPROXY=off")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, goBin, args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if strings.Contains(source, "github.com/bashsharp/bashsharp") {
		run("mod", "tidy")
		if _, err := os.Stat(filepath.Join(dir, "go.sum")); err != nil {
			t.Fatalf("no go.sum after tidy: %v", err)
		}
	}
	binary := filepath.Join(dir, "program")
	run("build", "-o", binary, ".")

	// The source is gone and PATH has no tools: whatever the program does, it
	// does from what it linked.
	if err := os.Remove(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/no-tools"}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	status := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("%v\n%s", err, stderr.String())
		}
		status = exit.ExitCode()
	}
	return out.String(), stderr.String(), status
}

func TestCompiledArtifactUsesTheBackend(t *testing.T) {
	t.Parallel()
	out, stderr, status := artifact(t, `package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
)

func main() {
	ctx := context.Background()
	s, err := shellrt.NewSession(
		shellrt.WithContext(ctx),
		shellrt.WithShellFactory(shellexec.New(shellexec.BashPP())),
	)
	check(err)

	// Persistent shell state: the function, array and option outlive the
	// region that defined them.
	check(s.Shell(ctx, "greet() { echo greet:$1; }; arr=(a b); set -u; trap 'echo bye' EXIT"))
	s.SetString("who", "world")
	check(s.Shell(ctx, "greet \"$who\"; echo \"${#arr[@]}\""))

	// A typed value read back out of the shell.
	v, _ := s.Get("arr")
	fmt.Println("typed:", v.List)

	// A task against an independent snapshot of that shell.
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		return child.Shell(ctx, "greet child; arr=(only-mine)")
	})
	check(task.Wait())
	check(s.Join())
	after, _ := s.Get("arr")
	fmt.Println("parent-after:", after.List)

	// Close runs the EXIT trap once.
	check(s.Close())
	os.Exit(0)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`)
	// The child runs the EXIT trap it inherited, then the parent runs its own.
	want := "greet:world\n2\ntyped: [a b]\ngreet:child\nbye\nparent-after: [a b]\nbye\n"
	if out != want || stderr != "" || status != 0 {
		t.Fatalf("out=%q stderr=%q status=%d\nwant out=%q", out, stderr, status, want)
	}
}

// TestTypedOnlyArtifactNeedsNoModuleGraph pins the dependency invariant: a
// program that imports only shellrt builds in a module with a go.mod and no
// go.sum, which is only possible while shellrt stays standard-library-only.
func TestTypedOnlyArtifactNeedsNoModuleGraph(t *testing.T) {
	t.Parallel()
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(this)))))
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")

	dir := t.TempDir()
	source := `package main

import (
	"context"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func main() {
	s, err := shellrt.NewSession()
	if err != nil {
		panic(err)
	}
	defer s.Close()
	s.SetString("x", "1")
	v, _ := s.Get("x")
	fmt.Println(v.Str, s.Shell(context.Background(), "echo nope") == shellrt.ErrNoShell)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module typedonly\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") +
		"\n\nrequire github.com/bashsharp/bashsharp v0.0.0\n\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "build", "-o", filepath.Join(dir, "program"), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shellrt no longer builds without a go.sum, so it gained a module dependency: %v\n%s", err, out)
	}
}

// TestOrdinaryFailureDoesNotStopARegion is the default shell contract: a
// non-zero status is not a reason to stop.
func TestOrdinaryFailureDoesNotStopARegion(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "false\necho ok"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "ok\n"; got != want {
		t.Fatalf("output %q, want %q: the region stopped at the first failure", got, want)
	}
	if got := s.Status(); got != 0 {
		t.Fatalf("Status() = %d, want 0: echo is the last command", got)
	}
	if s.Exited() {
		t.Fatal("Exited() is set for an ordinary failure")
	}
	// Several failures in a row, and a status that survives to the end.
	if err := s.Shell(t.Context(), "false; false; echo mid; (exit 5)"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got != 5 {
		t.Fatalf("Status() = %d, want 5", got)
	}
	if got, want := out.String(), "ok\nmid\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestErrexitStopsARegion(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "set -e\nfalse\necho ok"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("output %q, want none: errexit did not stop the region", got)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("Status() = %d, want 1", got)
	}
	if !s.Exited() {
		t.Fatal("Exited() is not set after errexit tripped")
	}
	// The session stays usable; acting on Exited is the program's decision.
	if err := s.Shell(t.Context(), "set +e; false; echo after"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "after\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if s.Exited() {
		t.Fatal("Exited() stuck on after a region that ran to the end")
	}
}

func TestExitStopsARegionAndKeepsItsCode(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	if err := s.Shell(t.Context(), "echo before\nexit 7\necho after"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "before\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if got := s.Status(); got != 7 {
		t.Fatalf("Status() = %d, want 7", got)
	}
	if !s.Exited() {
		t.Fatal("Exited() is not set after exit")
	}
}

// TestRegionBookkeepingIsInvisible proves the per-region machinery -- applying
// typed writes, projecting variables and options, preserving $? -- adds no
// user-visible callbacks and does not perturb the program's status.
func TestRegionBookkeepingIsInvisible(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()

	if err := s.Shell(ctx, "trap 'echo D:$BASH_COMMAND' DEBUG"); err != nil {
		t.Fatal(err)
	}
	// A typed write and an option read both happen at this region boundary.
	s.SetString("typed", "v")
	if err := s.SetOption("xtrace", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(ctx, "echo one\necho two"); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(ctx, "trap - DEBUG"); err != nil {
		t.Fatal(err)
	}

	// The typed assignment is visible as an assignment, which is honest: the
	// typed side really did assign a variable, and the merged program's DEBUG
	// trap should see it. Everything that reflects no program action --
	// reading the options back, projecting variables, restoring $? -- is
	// invisible. Note there is no `set` callback: options are read out of
	// SHELLOPTS rather than by running `set +o`.
	got := out.String()
	debugs := strings.Count(got, "D:")
	if want := 4; debugs != want {
		t.Fatalf("output %q: %d DEBUG callbacks, want %d (typed=, echo one, echo two, trap -)", got, debugs, want)
	}
	for _, leak := range []string{"D:set ", "SHELLOPTS", "D:cd ", "D:unset"} {
		if strings.Contains(got, leak) {
			t.Fatalf("output %q: bookkeeping %q leaked into the DEBUG trap", got, leak)
		}
	}

	// A region boundary with no typed writes adds no callbacks at all.
	if err := s.Shell(ctx, "trap 'echo D:$BASH_COMMAND' DEBUG"); err != nil {
		t.Fatal(err)
	}
	before := strings.Count(out.String(), "D:")
	if err := s.Shell(ctx, "echo three"); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(ctx, "trap - DEBUG"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Count(out.String(), "D:")-before, 2; got != want {
		t.Fatalf("output %q: %d callbacks across two clean region boundaries, want %d", out.String(), got, want)
	}

	// An ERR trap must not see the bookkeeping either, and $? must survive it.
	if err := s.Shell(ctx, "trap 'echo E' ERR"); err != nil {
		t.Fatal(err)
	}
	s.SetString("another", "w")
	if err := s.Shell(ctx, "false"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("Status() = %d, want 1", got)
	}
	if n := strings.Count(out.String(), "E\n"); n != 1 {
		t.Fatalf("output %q: %d ERR callbacks, want 1", out.String(), n)
	}
	// $? still reports the previous region, not the runtime's own last
	// statement -- so it has to be read by the region's first command.
	if err := s.Shell(ctx, "echo prev=$?; trap - ERR"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "prev=1") {
		t.Fatalf("output %q: $? was perturbed across the region boundary", out.String())
	}
}

func TestBookkeepingCannotTripErrexit(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()
	if err := s.Shell(ctx, "set -e; echo start"); err != nil {
		t.Fatal(err)
	}
	// Under errexit, a bookkeeping statement that the runtime chose must not
	// be able to end the program.
	s.SetString("x", "1")
	s.Set("arr", shellrt.Var{Kind: shellrt.Indexed, List: []string{"a"}})
	if err := s.Shell(ctx, "echo still-here"); err != nil {
		t.Fatal(err)
	}
	if s.Exited() {
		t.Fatal("bookkeeping tripped errexit")
	}
	if got, want := out.String(), "start\nstill-here\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestTypedWriteToAReadonlyVariableIsReported(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()
	if err := s.Shell(ctx, "frozen=original; readonly frozen"); err != nil {
		t.Fatal(err)
	}
	s.SetString("frozen", "overwritten")

	err := s.Shell(ctx, "echo unreachable")
	var applyErr *shellexec.ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("err = %v, want an *ApplyError", err)
	}
	if !strings.Contains(applyErr.Source, "frozen=") {
		t.Fatalf("ApplyError names %q, want the refused assignment", applyErr.Source)
	}
	// The region did not run, and the projection reports what the shell holds
	// rather than the write that was refused.
	if got := out.String(); strings.Contains(got, "unreachable") {
		t.Fatalf("output %q: the region ran after a refused write", got)
	}
	v, ok := s.Get("frozen")
	if !ok || v.Str != "original" {
		t.Fatalf("frozen = %+v, want the shell's original value", v)
	}
	if !v.ReadOnly {
		t.Fatal("the projection lost the readonly attribute")
	}
	// The session recovers once the typed side stops fighting the shell.
	s.Set("frozen", v)
	if err := s.Shell(ctx, "echo recovered"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "recovered") {
		t.Fatalf("output %q", out.String())
	}
}

func TestTypedChdirToAVanishedDirectoryIsReported(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	ctx := t.Context()
	start := s.Dir()
	gone := filepath.Join(start, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	// Chdir validated the directory; it disappears before the shell sees it.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	err := s.Shell(ctx, "echo unreachable")
	var applyErr *shellexec.ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("err = %v, want an *ApplyError", err)
	}
	if !strings.HasPrefix(applyErr.Source, "cd ") {
		t.Fatalf("ApplyError names %q, want the cd", applyErr.Source)
	}
	// The projection reports the shell's real cwd, not the one that failed.
	if got := s.Dir(); got != start {
		t.Fatalf("Dir() = %q, want the shell's actual cwd %q", got, start)
	}
}

// blockUntilCancelled is a command that only returns when its context ends.
func blockUntilCancelled(started chan<- struct{}) interp.ExecHandlerFunc {
	var once sync.Once
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || args[0] != "blockforever" {
			return interp.DefaultExecHandler(0)(ctx, args)
		}
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil
	}
}

func TestCloseIsBoundedWhenTheExitTrapBlocks(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	s, _ := newSession(t,
		shellrt.WithContext(ctx),
		shellrt.WithShellFactory(shellexec.New(
			shellexec.ShutdownTimeout(150*time.Millisecond),
			shellexec.RunnerOptions(interp.ExecHandler(blockUntilCancelled(started))),
		)),
	)
	if err := s.Shell(ctx, "trap 'blockforever' EXIT"); err != nil {
		t.Fatal(err)
	}
	// The session context is already cancelled, so shutdown cannot inherit a
	// deadline from it: the backend has to supply its own.
	cancel()

	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the EXIT trap never ran after cancellation")
	}
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close() = %v, want a bounded shutdown deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a blocking EXIT trap")
	}
	// No leaks: the blocked command was released by the shutdown deadline, and
	// Close stays idempotent afterwards.
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("Active() = %d, want 0", active)
	}
}

func TestWithoutExitTrapsStillTerminatesTheShell(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	s, out := newSession(t, shellrt.WithShellFactory(shellexec.New(
		shellexec.WithoutExitTraps(),
		shellexec.ShutdownTimeout(150*time.Millisecond),
		shellexec.RunnerOptions(interp.ExecHandler(blockUntilCancelled(started))),
	)))
	if err := s.Shell(t.Context(), "trap 'echo bye' EXIT; echo body"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with WithoutExitTraps")
	}
	// The user callback was dropped, but shutdown still ran to completion.
	if got, want := out.String(), "body\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}
