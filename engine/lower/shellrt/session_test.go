package shellrt_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec"
)

// clearEnviron drops the inherited process environment from a session under
// construction, so tests observe only the variables they set. It relies on
// session options being applied in order, before the shell factory runs.
func clearEnviron() shellrt.SessionOption {
	return func(s *shellrt.Session) error {
		for _, pair := range s.Environ() {
			name, _, _ := strings.Cut(pair, "=")
			s.Unset(name)
		}
		return nil
	}
}

// newSession builds a session with a persistent interp backend, buffered
// output and no inherited process environment, so assertions see only what the
// test put there.
func newSession(t *testing.T, opts ...shellrt.SessionOption) (*shellrt.Session, *lockedBuffer) {
	t.Helper()
	out := &lockedBuffer{}
	// A real file for stdin: the interpreter copies any other reader through a
	// background goroutine per run, which is not safe to share across regions.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devNull.Close() })

	// A session's own environment is dropped so assertions see only what the
	// test put there; clearing it before the backend is built keeps the two
	// in step, which is exactly what WithShellFactory is for.
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

// TestShellStatePersistsAcrossRegions is the core of the boundary: one region
// defines a function, an array, an option and a status, and the next region
// still sees all four.
func TestShellStatePersistsAcrossRegions(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()

	if err := s.Shell(ctx, "f() { echo yes; }; arr=(a b); set -u; false"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("Status() = %d, want 1", got)
	}
	if !s.Option("nounset") {
		t.Fatal("nounset was not projected after set -u")
	}
	v, ok := s.Get("arr")
	if !ok || v.Kind != shellrt.Indexed {
		t.Fatalf("arr = %+v, ok=%v; want an indexed array", v, ok)
	}
	if got, want := v.List, []string{"a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("arr = %q, want %q", got, want)
	}

	// The later region observes the function, the array, the option and the
	// previous status.
	if err := s.Shell(ctx, `echo "prev=$?"; f; echo "${arr[1]}"; echo "${#arr[@]}"`); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "prev=1\nyes\nb\n2\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	// nounset is still live, so an unset expansion still fails.
	if err := s.Shell(ctx, "echo ${missing}"); err != nil {
		t.Fatal(err)
	}
	if s.Status() == 0 {
		t.Fatal("nounset did not survive into the third region")
	}
}

func TestArrayValuesAreNotDropped(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()

	// Round trip an array through the typed projection untouched.
	if err := s.Shell(ctx, "arr=(one 'two three' four)"); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Get("arr")
	if got, want := v.List, []string{"one", "two three", "four"}; !slices.Equal(got, want) {
		t.Fatalf("arr = %q, want %q", got, want)
	}
	if got, want := v.String(), "one"; got != want {
		t.Fatalf("scalar view = %q, want %q", got, want)
	}
	if err := s.Shell(ctx, `printf '%s|' "${arr[@]}"; echo`); err != nil {
		t.Fatal(err)
	}

	// A typed-side array write reaches the shell whole.
	s.Set("built", shellrt.Var{Kind: shellrt.Indexed, List: []string{"x y", "z'q"}})
	if err := s.Shell(ctx, `printf '%s|' "${built[@]}"; echo`); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "one|two three|four|\nx y|z'q|\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}

	// Associative arrays survive the same round trip.
	if err := s.Shell(ctx, `declare -A m=([k]=v [k2]='v 2')`); err != nil {
		t.Fatal(err)
	}
	m, ok := s.Get("m")
	if !ok || m.Kind != shellrt.Associative {
		t.Fatalf("m = %+v, ok=%v", m, ok)
	}
	if want := map[string]string{"k": "v", "k2": "v 2"}; !maps.Equal(m.Map, want) {
		t.Fatalf("m = %v, want %v", m.Map, want)
	}
}

func TestTypedWritesReachTheShellWithoutClobberingIt(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()

	if err := s.Shell(ctx, "g() { echo from-g; }; keep=untouched; declare -i n=2"); err != nil {
		t.Fatal(err)
	}
	// Only `typed` changed on the typed side; `keep`, `n` and `g` must not be
	// re-applied and must keep their shell attributes.
	s.SetString("typed", "hello")
	if err := s.Shell(ctx, `echo "$typed $keep"; n=3+4; echo "$n"; g`); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "hello untouched\n7\nfrom-g\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

func TestExportProjection(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	ctx := t.Context()
	if err := s.Shell(ctx, "plain=1; shipped=2; export shipped"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Environ(), []string{"shipped=2"}; !slices.Equal(got, want) {
		t.Fatalf("Environ() = %q, want %q", got, want)
	}
	if v, _ := s.Get("plain"); v.Exported {
		t.Fatal("a plain assignment must not be exported")
	}
}

func TestTrapsPersistAndFollowShellInheritance(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	ctx := t.Context()

	// A trap set in one region is still installed in the next: running a
	// region must not imply an exit that fires and clears EXIT traps.
	if err := s.Shell(ctx, "trap 'echo caught' USR1; trap 'echo bye' EXIT"); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(ctx, "trap -p USR1"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "USR1") || strings.Contains(got, "bye") {
		t.Fatalf("output %q: the trap did not persist, or EXIT fired per region", got)
	}
}

func TestChildTaskShellIsIsolated(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)

	if err := s.Shell(t.Context(), "shared=parent; base() { echo base; }"); err != nil {
		t.Fatal(err)
	}
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		// The clone inherited the parent's functions and variables.
		if err := child.Shell(ctx, `base; echo "$shared"`); err != nil {
			return err
		}
		// Mutations here must not reach the parent's shell.
		return child.Shell(ctx, "shared=child; base() { echo overridden; }; only=here")
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Join(); err != nil {
		t.Fatal(err)
	}
	if v, _ := task.Session().Get("shared"); v.Str != "child" {
		t.Fatalf("child shared = %q, want child", v.Str)
	}
	if err := s.Shell(t.Context(), `base; echo "$shared"; echo "only=${only-unset}"`); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "base\nparent\nbase\nparent\nonly=unset\n"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if _, ok := s.Get("only"); ok {
		t.Fatal("a child's variable reached the parent projection")
	}
}

func TestChildTaskTrapInheritanceAndRestoration(t *testing.T) {
	t.Parallel()
	s, out := newSession(t)
	// Without errtrace, a subshell does not inherit ERR; the DEBUG trap is
	// likewise gated on functrace. A child must observe the shell's own
	// inheritance rules rather than a blanket copy.
	if err := s.Shell(t.Context(), "trap 'echo err' ERR; trap 'echo term' TERM"); err != nil {
		t.Fatal(err)
	}
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		return child.Shell(ctx, "trap -p ERR; trap -p TERM; trap - TERM")
	})
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "ERR") {
		t.Fatalf("output %q: ERR must not be inherited without errtrace", got)
	}
	if !strings.Contains(got, "TERM") {
		t.Fatalf("output %q: TERM should be inherited", got)
	}
	// The child cleared TERM; the parent's is untouched.
	out.mu.Lock()
	out.buf.Reset()
	out.mu.Unlock()
	if err := s.Shell(t.Context(), "trap -p TERM"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "TERM") {
		t.Fatalf("output %q: the child cleared the parent's trap", out.String())
	}
}

func TestSessionCwdPersists(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	start := s.Dir()
	if err := os.Mkdir(filepath.Join(start, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(t.Context(), "cd sub"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Dir(), filepath.Join(start, "sub"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
	// A typed-side Chdir reaches the shell too.
	if err := s.Chdir(start); err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(t.Context(), "true"); err != nil {
		t.Fatal(err)
	}
	if got := s.Dir(); got != start {
		t.Fatalf("Dir() = %q, want %q", got, start)
	}
}

func TestSessionStatusIsNotAnError(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	if err := s.Shell(t.Context(), "false"); err != nil {
		t.Fatalf("non-zero status must not be an error: %v", err)
	}
	if got := s.Status(); got != 1 {
		t.Fatalf("Status() = %d, want 1", got)
	}
	if err := s.Shell(t.Context(), "exit 3"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got != 3 {
		t.Fatalf("Status() = %d, want 3", got)
	}
	if err := s.Shell(t.Context(), "true"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got != 0 {
		t.Fatalf("Status() = %d, want 0", got)
	}
	// A typed-side status write is what the next region's $? reads.
	s.SetStatus(42)
	if err := s.Shell(t.Context(), `echo "$?"`); err != nil {
		t.Fatal(err)
	}
}

func TestSessionShellParseErrorIsReported(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	if err := s.Shell(t.Context(), "if"); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestSessionSnapshotIsIndependent(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	s.Set("a", shellrt.Var{Kind: shellrt.Indexed, List: []string{"1"}, Exported: true})
	if err := s.SetOption("xtrace", true); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()

	s.Set("a", shellrt.Var{Str: "2"})
	s.Set("b", shellrt.Var{Str: "3"})
	if err := s.SetOption("xtrace", false); err != nil {
		t.Fatal(err)
	}

	if got := snap.Vars["a"].List; !slices.Equal(got, []string{"1"}) {
		t.Fatalf("snapshot a = %q, want [1]", got)
	}
	if _, ok := snap.Vars["b"]; ok {
		t.Fatal("snapshot must not see later writes")
	}
	if !snap.Options["xtrace"] {
		t.Fatal("snapshot options must not track later writes")
	}
	// Mutating the snapshot's array must not reach the session.
	snap.Vars["a"].List[0] = "mutated"
	if v, _ := s.Get("a"); v.Str != "2" {
		t.Fatalf("session a = %+v", v)
	}
}

func TestSessionChdirValidates(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	start := s.Dir()
	if err := s.Chdir("nope"); err == nil {
		t.Fatal("want an error for a missing directory")
	}
	if got := s.Dir(); got != start {
		t.Fatalf("Dir() = %q after a failed Chdir, want %q", got, start)
	}
	if err := os.Mkdir(filepath.Join(start, "kid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Chdir("kid"); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Dir(), filepath.Join(start, "kid"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestSessionWithoutShellBackend(t *testing.T) {
	t.Parallel()
	s, err := shellrt.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Shell(t.Context(), "echo hi"); !errors.Is(err, shellrt.ErrNoShell) {
		t.Fatalf("err = %v, want ErrNoShell", err)
	}
}

func TestSessionUnknownOptionRejected(t *testing.T) {
	t.Parallel()
	s, _ := newSession(t)
	if err := s.SetOption("nosuchoption", true); err == nil {
		t.Fatal("want an error for an unknown option")
	}
}

// recordingShell proves the dynamic-shell seam is the only route to a shell,
// and that it receives exactly the region the caller passed.
type recordingShell struct {
	mu     sync.Mutex
	src    []string
	clones int
	closed int
}

func (r *recordingShell) RunShell(ctx context.Context, st *shellrt.State, io shellrt.Stdio, src string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.src = append(r.src, src)
	st.Vars["seen"] = shellrt.Var{Str: src}
	st.Status = 7
	return nil
}

func (r *recordingShell) Clone(io shellrt.Stdio) (shellrt.ShellRunner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clones++
	return &recordingShell{}, nil
}

func (r *recordingShell) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
	return nil
}

func TestSessionShellSeamIsReplaceable(t *testing.T) {
	t.Parallel()
	rec := &recordingShell{}
	s, err := shellrt.NewSession(shellrt.WithShell(rec))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Shell(t.Context(), "anything at all"); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.src, []string{"anything at all"}; !slices.Equal(got, want) {
		t.Fatalf("regions = %q, want %q", got, want)
	}
	if v, _ := s.Get("seen"); v.Str != "anything at all" {
		t.Fatalf("seen = %q", v.Str)
	}
	if got := s.Status(); got != 7 {
		t.Fatalf("Status() = %d, want 7", got)
	}
	// Every child gets its own backend, and Close releases the session's.
	s.Go(func(ctx context.Context, child *shellrt.Session) error { return nil })
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.clones != 1 {
		t.Fatalf("clones = %d, want 1", rec.clones)
	}
	if rec.closed != 1 {
		t.Fatalf("closed = %d, want 1", rec.closed)
	}
}

// failingCloneShell makes the child snapshot fail, which must surface as that
// task's failure rather than as a stuck launcher.
type failingCloneShell struct{ recordingShell }

func (f *failingCloneShell) Clone(io shellrt.Stdio) (shellrt.ShellRunner, error) {
	return nil, errors.New("no snapshot for you")
}

func TestTaskCloneFailureIsTheTaskFailure(t *testing.T) {
	t.Parallel()
	s, err := shellrt.NewSession(shellrt.WithShell(&failingCloneShell{}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task := s.Go(func(ctx context.Context, child *shellrt.Session) error {
		t.Error("the body must not run when the snapshot failed")
		return nil
	})
	if err := task.Wait(); err == nil || !strings.Contains(err.Error(), "task snapshot") {
		t.Fatalf("err = %v, want a task snapshot failure", err)
	}
	if task.Session() != nil {
		t.Fatal("a task that never started must have no session")
	}
	var terr *shellrt.TaskError
	if err := s.Join(); !errors.As(err, &terr) || terr.Ordinal != 0 {
		t.Fatalf("Join() = %v, want task 0's failure", err)
	}
}
