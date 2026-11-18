package shellrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// VarKind distinguishes the shapes a shell variable can hold. Array values are
// carried across this boundary rather than flattened to their first element.
type VarKind uint8

const (
	Scalar VarKind = iota
	Indexed
	Associative
)

// Var is one shell variable as the typed side observes it.
type Var struct {
	Kind     VarKind
	Str      string            // Kind == Scalar
	List     []string          // Kind == Indexed
	Map      map[string]string // Kind == Associative
	Exported bool
	ReadOnly bool
}

// String is the scalar view a typed binding reads. For an indexed array it is
// element zero and for an associative array it is the empty string, matching
// how bash expands a bare array name.
func (v Var) String() string {
	switch v.Kind {
	case Indexed:
		if len(v.List) > 0 {
			return v.List[0]
		}
		return ""
	case Associative:
		return ""
	default:
		return v.Str
	}
}

func (v Var) clone() Var {
	v.List = slices.Clone(v.List)
	v.Map = maps.Clone(v.Map)
	return v
}

// Equal reports whether two variables hold the same value and attributes. The
// shell backend uses it to tell a typed-side write apart from state it already
// published, so that it only re-applies what actually changed.
func (v Var) Equal(o Var) bool {
	if v.Kind != o.Kind || v.Exported != o.Exported || v.ReadOnly != o.ReadOnly {
		return false
	}
	switch v.Kind {
	case Indexed:
		return slices.Equal(v.List, o.List)
	case Associative:
		return maps.Equal(v.Map, o.Map)
	default:
		return v.Str == o.Str
	}
}

// State is the typed side's projection of persistent shell state: working
// directory, variables, shell options and the last status.
//
// State is a projection, not the whole shell. Functions, traps, aliases and
// every other richer part of the shell live in the [ShellRunner] backing the
// session and persist there across regions; they are never serialized through
// State and never reconstructed from it.
type State struct {
	Dir     string
	Vars    map[string]Var
	Options map[string]bool
	Status  int

	// Params are the positional parameters the shell sees as $1, $2, ... and
	// counts with $#. They are a list of opaque argument strings, never a
	// joined command line: an element may be empty, may hold spaces, tabs,
	// newlines or arbitrary UTF-8, and byte boundaries are preserved exactly
	// as the caller supplied them.
	//
	// Like the rest of State this is a projection: `set --` and `shift` inside
	// a shell region are read back here, and a typed-side write is pushed into
	// the shell before the next region runs.
	Params []string

	// Exited reports that the shell asked to end the program during the last
	// region: an `exit` builtin, or a failure under errexit. Status carries
	// the code. The runtime does not act on it -- a session stays usable --
	// because whether to stop is the generated program's decision.
	Exited bool
}

// Clone returns a deep copy, so a child task and its parent share no map.
func (s State) Clone() State {
	c := State{Dir: s.Dir, Status: s.Status, Exited: s.Exited, Vars: map[string]Var{}, Options: maps.Clone(s.Options), Params: slices.Clone(s.Params)}
	for name, v := range s.Vars {
		c.Vars[name] = v.clone()
	}
	if c.Options == nil {
		c.Options = map[string]bool{}
	}
	return c
}

// Environ returns the exported variables as sorted NAME=value pairs, which is
// what a launched process would see. Array variables are not exported by bash
// and are omitted here for the same reason.
func (s State) Environ() []string {
	pairs := make([]string, 0, len(s.Vars))
	for name, v := range s.Vars {
		if v.Exported && v.Kind == Scalar {
			pairs = append(pairs, name+"="+v.Str)
		}
	}
	slices.Sort(pairs)
	return pairs
}

// Stdio is the session's standard I/O triple. It is shared with child tasks by
// reference: a snapshot copies variables and the directory, never the streams.
//
// In should be an *os.File whenever dynamic shell regions run concurrently or
// back to back. The interpreter copies any other reader into a pipe from a
// background goroutine whose lifetime is not bounded by a single run, so a
// shared plain reader would be read from more than one goroutine.
type Stdio struct {
	In       io.Reader
	Out, Err io.Writer
}

// knownOptions are the shell options this foundation projects into [State].
// An unknown name is an error rather than a silently ignored no-op. Options
// set inside a shell region persist in the backend whether or not they appear
// here; this list is what the typed side can read and write.
var knownOptions = []string{"errexit", "noglob", "nounset", "pipefail", "xtrace"}

// KnownOptions returns the shell option names the typed side can read and set.
func KnownOptions() []string { return slices.Clone(knownOptions) }

// ShellRunner is the persistent dynamic-shell backend of one session. It owns
// the shell state that [State] does not model — functions, traps, aliases,
// array attributes — and keeps it alive across regions.
//
// It is the only route from a generated program to a shell interpreter. The
// compiler hands it regions the syntax tree identified as shell; a typed
// program is never passed through it as a whole.
//
// An implementation may assume the session serializes every call against one
// instance: RunShell, Clone and Close never overlap. That matters because a
// shell backend is a shared mutable resource — interp.Runner.Subshell, for
// one, is documented as unsafe to use concurrently with the runner it copies,
// and mutates the runner it is called on.
type ShellRunner interface {
	// RunShell applies the typed side's changes to st onto the persistent
	// shell, runs one region, and writes the resulting projection back into
	// st. A non-zero command status is reported through st.Status; the error
	// result is reserved for parse and runtime faults.
	RunShell(ctx context.Context, st *State, io Stdio, src string) error

	// Clone returns an independent backend for a child task, holding a copy
	// of the current shell state. The clone and its parent must share no
	// mutable state: writes on either side are invisible to the other.
	Clone(io Stdio) (ShellRunner, error)

	// Close terminates the shell: it runs the shell's EXIT trap exactly once
	// and releases the backend's resources. It must be safe to call more than
	// once. The session passes a shutdown context derived with
	// [context.WithoutCancel], so a cancelled program still runs its EXIT trap.
	Close(ctx context.Context) error
}

// BlockingShellRunner optionally identifies actual suspension points inside
// shell execution and EXIT-trap cleanup. The callback releases the owning task
// launcher and must run before blocking. Immediate operations do not call it.
// A backend without this interface retains the conservative legacy contract:
// entering RunShell or Close itself arms the task.
type BlockingShellRunner interface {
	ShellRunner
	RunShellWithBlocking(ctx context.Context, st *State, io Stdio, src string, beforeBlock func()) error
	CloseWithBlocking(ctx context.Context, beforeBlock func()) error
}

// ShellFactory builds a session's backend once the session's initial state and
// streams are fully configured. It is how a backend is seeded with the
// directory, environment and options the session options established.
type ShellFactory func(State, Stdio) (ShellRunner, error)

// Session is the persistent shell state boundary plus ownership of the tasks
// launched from it. The zero value is not usable; call [NewSession].
type Session struct {
	nativeContinuation *NativeContinuation
	mu                 sync.Mutex
	state              State
	io                 Stdio

	// shellMu serializes every use of the backend, including cloning it for a
	// child. It is a separate lock from mu because a region runs for as long
	// as the shell takes, while the projection must stay readable.
	shellMu sync.Mutex
	shell   ShellRunner
	factory ShellFactory
	base    context.Context

	group  *taskGroup
	closed bool

	// armReady is nil for a root session; a child session carries the launch
	// handshake its owner is blocked on. See [Session.Arm].
	armReady chan struct{}
	armOnce  sync.Once
}

// SessionOption configures a [Session] at construction.
type SessionOption func(*Session) error

// WithDir sets the initial working directory. It must name an existing
// directory; an empty path keeps the process working directory.
func WithDir(path string) SessionOption {
	return func(s *Session) error {
		if path == "" {
			return nil
		}
		abs, err := resolveDir("", path)
		if err != nil {
			return err
		}
		s.state.Dir = abs
		return nil
	}
}

// WithEnviron seeds exported scalar variables from NAME=value pairs.
func WithEnviron(pairs ...string) SessionOption {
	return func(s *Session) error {
		for _, pair := range pairs {
			name, value, ok := strings.Cut(pair, "=")
			if !ok || name == "" {
				return fmt.Errorf("shellrt: invalid environment entry %q", pair)
			}
			s.state.Vars[name] = Var{Str: value, Exported: true}
		}
		return nil
	}
}

// WithVars seeds variables, including non-exported ones and arrays.
func WithVars(vars map[string]Var) SessionOption {
	return func(s *Session) error {
		for name, v := range vars {
			s.state.Vars[name] = v.clone()
		}
		return nil
	}
}

// WithStdio sets the standard I/O triple. Nil members keep the defaults.
func WithStdio(in io.Reader, out, err io.Writer) SessionOption {
	return func(s *Session) error {
		s.io = mergeStdio(s.io, in, out, err)
		return nil
	}
}

// WithOption enables or disables a shell option by its `set -o` name.
func WithOption(name string, on bool) SessionOption {
	return func(s *Session) error { return s.setOptionLocked(name, on) }
}

// WithShell installs an already-built persistent dynamic-shell backend.
// Without it, and without [WithShellFactory], a session has no shell:
// [Session.Shell] reports [ErrNoShell], which is what a typed-only artifact
// wants — it links no interpreter at all.
func WithShell(sh ShellRunner) SessionOption {
	return func(s *Session) error {
		s.shell = sh
		s.factory = nil
		return nil
	}
}

// WithShellFactory installs the backend built from the session's final initial
// state and streams, after every other option has been applied. This is the
// normal way to attach a backend:
//
//	shellrt.NewSession(
//		shellrt.WithDir(dir),
//		shellrt.WithShellFactory(shellexec.New(shellexec.BashPP())),
//	)
func WithShellFactory(f ShellFactory) SessionOption {
	return func(s *Session) error {
		s.factory = f
		return nil
	}
}

// WithContext sets the base context every task group and dynamic shell region
// derives from. It is also the seam later agentic callback scopes attach to.
func WithContext(ctx context.Context) SessionOption {
	return func(s *Session) error {
		if ctx == nil {
			return errors.New("shellrt: nil context")
		}
		s.base = ctx
		return nil
	}
}

func mergeStdio(cur Stdio, in io.Reader, out, err io.Writer) Stdio {
	if in != nil {
		cur.In = in
	}
	if out != nil {
		cur.Out = out
	}
	if err != nil {
		cur.Err = err
	}
	return cur
}

// NewSession builds a session with the process environment, the process
// working directory and os stdio. It installs no shell backend; pass
// [WithShell] for a program that has dynamic shell regions.
func NewSession(opts ...SessionOption) (*Session, error) {
	s := &Session{
		state: State{Vars: map[string]Var{}, Options: map[string]bool{}},
		io:    Stdio{In: os.Stdin, Out: Stdout, Err: Stderr},
		base:  context.Background(),
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	s.state.Dir = dir
	for _, pair := range os.Environ() {
		if name, value, ok := strings.Cut(pair, "="); ok && name != "" {
			s.state.Vars[name] = Var{Str: value, Exported: true}
		}
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.shell == nil && s.factory != nil {
		shell, err := s.factory(s.state.Clone(), s.io)
		if err != nil {
			return nil, err
		}
		s.shell = shell
	}
	s.group = newTaskGroup(s.base)
	return s, nil
}

// newChild builds the session a task body runs against: an independent state
// projection, an independent shell backend cloned from this one, the parent's
// streams, and its own task group rooted at the task's context.
func (s *Session) newChild(ctx context.Context, st State) (*Session, error) {
	s.mu.Lock()
	shell, streams := s.shell, s.io
	s.mu.Unlock()
	var childShell ShellRunner
	if shell != nil {
		s.shellMu.Lock()
		var err error
		childShell, err = shell.Clone(streams)
		s.shellMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("shellrt: task snapshot: %w", err)
		}
	}
	return &Session{
		state: st,
		io:    streams,
		shell: childShell,
		base:  ctx,
		group: newTaskGroup(ctx),
	}, nil
}

// Snapshot returns an independent copy of the projected state.
func (s *Session) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Clone()
}

// Dir reports the working directory.
func (s *Session) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Dir
}

// Chdir changes the working directory, resolving relative paths against the
// current one. It fails if the target is not an existing directory.
func (s *Session) Chdir(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	abs, err := resolveDir(s.state.Dir, path)
	if err != nil {
		return err
	}
	s.state.Dir = abs
	return nil
}

func resolveDir(base, path string) (string, error) {
	if path == "" {
		return "", errors.New("shellrt: empty directory")
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("shellrt: not a directory: %s", abs)
	}
	return abs, nil
}

// Get reports a variable's current value.
func (s *Session) Get(name string) (Var, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.state.Vars[name]
	return v.clone(), ok
}

// Set writes a variable, replacing both value and attributes.
func (s *Session) Set(name string, v Var) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Vars[name] = v.clone()
}

// SetString writes a scalar variable, preserving its export attribute.
func (s *Session) SetString(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state.Vars[name]
	s.state.Vars[name] = Var{Str: value, Exported: v.Exported, ReadOnly: v.ReadOnly}
}

// Unset removes a variable.
func (s *Session) Unset(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.Vars, name)
}

// Export marks a variable exported, declaring it unset-but-known if it does
// not exist yet, matching `export name`.
func (s *Session) Export(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.state.Vars[name]
	v.Exported = true
	s.state.Vars[name] = v
}

// Environ returns the exported scalar variables as sorted NAME=value pairs.
func (s *Session) Environ() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Environ()
}

// Option reports whether a shell option is on.
func (s *Session) Option(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Options[name]
}

// SetOption turns a `set -o` option on or off. Unknown names are rejected.
func (s *Session) SetOption(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setOptionLocked(name, on)
}

func (s *Session) setOptionLocked(name string, on bool) error {
	if !slices.Contains(knownOptions, name) {
		return fmt.Errorf("shellrt: unknown shell option %q", name)
	}
	s.state.Options[name] = on
	return nil
}

// Status reports the last command status, the value `$?` expands to.
func (s *Session) Status() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Status
}

// SetStatus records a command status.
func (s *Session) SetStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = code
}

// Exited reports whether the last shell region ended because the shell asked
// to end the program -- `exit`, or a failure under errexit -- rather than
// because it ran out of statements. [Session.Status] carries the code.
func (s *Session) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Exited
}

// Stdio returns the session's streams. They are shared, not copied.
func (s *Session) Stdio() Stdio {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.io
}

// SetStdio replaces the session's streams. Nil members are left unchanged. It
// affects regions started after the call, not one already running.
func (s *Session) SetStdio(in io.Reader, out, err io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.io = mergeStdio(s.io, in, out, err)
}

// Context returns the base context of this session: the process context for a
// root session, and the owning task's context for a child.
func (s *Session) Context() context.Context { return s.base }

// ErrNoShell is returned by [Session.Shell] when the session has no dynamic
// shell backend.
var ErrNoShell = errors.New("shellrt: no dynamic shell backend configured")

// Shell runs one explicitly identified dynamic shell region against this
// session's persistent shell, then refreshes the typed projection from it.
// Functions, traps, aliases, arrays and options defined by a region stay
// alive in the backend and are visible to every later region of the session.
//
// A non-zero command status is reported through [Session.Status], not as an
// error. The returned error is reserved for parse and runtime faults.
//
// A BlockingShellRunner arms at its first actual blocking operation; a legacy
// backend arms before RunShell. Thus immediate builtins retain their launch
// until their status is settled, while blocking providers release the owner.
func (s *Session) Shell(ctx context.Context, src string) error {
	s.mu.Lock()
	shell, st, streams := s.shell, s.state.Clone(), s.io
	s.mu.Unlock()
	if shell == nil {
		return ErrNoShell
	}
	if ctx == nil {
		ctx = s.base
	}
	s.shellMu.Lock()
	var err error
	if blocking, ok := shell.(BlockingShellRunner); ok {
		err = blocking.RunShellWithBlocking(ctx, &st, streams, src, s.Arm)
	} else {
		s.Arm()
		err = shell.RunShell(ctx, &st, streams, src)
	}
	s.shellMu.Unlock()
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	return err
}
