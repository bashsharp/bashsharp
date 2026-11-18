// Package shellexec is the production dynamic-shell backend for
// [github.com/bashsharp/bashsharp/engine/lower/shellrt]. It implements [shellrt.ShellRunner] on top
// of the public github.com/bashsharp/bashsharp/engine/interp API.
//
// It lives in its own package so that shellrt itself stays free of the
// interpreter's module graph: a generated program with no dynamic shell region
// links neither this package nor interp. A program that does have one imports
// this package explicitly, and only regions the syntax tree identified as
// shell reach it — a typed program is never passed through it as a whole.
//
// One [interp.Runner] backs one session for its whole life, which is what
// makes shell state persistent: functions, traps, aliases, arrays and options
// set by a region are still in place for the next one.
package shellexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Option configures the backend.
type Option func(*config)

type config struct {
	lang     syntax.LangVariant
	runner   []interp.RunnerOption
	noTraps  bool
	shutdown time.Duration
}

// DefaultShutdownTimeout bounds Close. A shell's EXIT trap is arbitrary user
// code and the interpreter also waits there for background work, so shutdown
// needs a deadline of its own: the session's context is already cancelled by
// then and cannot supply one.
const DefaultShutdownTimeout = 10 * time.Second

// Dialect selects the language variant used to parse regions and to run them.
// The default is [syntax.LangBash].
func Dialect(lang syntax.LangVariant) Option {
	return func(c *config) { c.lang = lang }
}

// BashPP is Dialect([syntax.LangBashPP]), the dialect a lowered Bash++ program
// keeps for its dynamic regions.
func BashPP() Option { return Dialect(syntax.LangBashPP) }

// RunnerOptions passes interpreter options straight through, for embedders
// that need their own handlers. Options which the backend owns — Env, Dir,
// StdIO, Lang and the shell option flags — are applied first and can be
// overridden here, at the caller's risk.
func RunnerOptions(opts ...interp.RunnerOption) Option {
	return func(c *config) { c.runner = append(c.runner, opts...) }
}

// WithoutExitTraps stops [shellrt.Session.Close] from running the shell's EXIT
// trap. The default is to run it exactly once per session, which is what a
// shell does when it terminates.
//
// Shutdown still terminates the shell and waits for the interpreter's own
// background work; only the user callbacks are dropped. Dropping them costs
// one `trap - EXIT DEBUG ERR` statement, so a DEBUG trap may observe that one
// statement before it is cleared. There is no public API to clear a trap
// without running a command.
func WithoutExitTraps() Option {
	return func(c *config) { c.noTraps = true }
}

// ShutdownTimeout bounds how long Close waits for the EXIT trap and for the
// interpreter's background work. Zero or negative selects
// [DefaultShutdownTimeout]; use a very large value to wait indefinitely.
func ShutdownTimeout(d time.Duration) Option {
	return func(c *config) { c.shutdown = d }
}

// New returns a factory for [shellrt.WithShellFactory], so that the backend is
// built from the session's fully configured initial state and streams:
//
//	s, err := shellrt.NewSession(
//		shellrt.WithDir(dir),
//		shellrt.WithShellFactory(shellexec.New(shellexec.BashPP())),
//	)
func New(opts ...Option) shellrt.ShellFactory {
	return func(st shellrt.State, streams shellrt.Stdio) (shellrt.ShellRunner, error) {
		return NewRunner(st, streams, opts...)
	}
}

// NewRunner builds a backend directly, for callers that manage session
// construction themselves.
func NewRunner(st shellrt.State, streams shellrt.Stdio, opts ...Option) (shellrt.ShellRunner, error) {
	cfg := config{lang: syntax.LangBash, shutdown: DefaultShutdownTimeout}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.shutdown <= 0 {
		cfg.shutdown = DefaultShutdownTimeout
	}
	base := []interp.RunnerOption{
		interp.Lang(cfg.lang),
		interp.Env(stateEnviron{vars: st.Vars}),
		interp.StdIO(streams.In, streams.Out, streams.Err),
		interp.Params(initialParams(st)...),
	}
	if st.Dir != "" {
		base = append(base, interp.Dir(st.Dir))
	}
	runner, err := interp.New(append(base, cfg.runner...)...)
	if err != nil {
		return nil, err
	}
	runner.Reset()
	runner.SetLastExitStatus(clampStatus(st.Status))
	return &shell{cfg: cfg, runner: runner, last: st.Clone(), io: streams}, nil
}

// shell is one session's persistent interpreter plus the last projection it
// published, which is how it tells a typed-side write apart from shell state
// it already reported.
type shell struct {
	cfg    config
	runner *interp.Runner
	last   shellrt.State
	io     shellrt.Stdio

	closeOnce sync.Once
	closeErr  error
	processes []interp.LineProcess
}

// initialParams seeds both the shell options and the positional parameters in
// the single [interp.Params] call the runner accepts. The "--" terminator is
// always emitted so that a first parameter spelled like a flag is a parameter,
// and so that an empty parameter list is seeded as empty rather than left at
// the interpreter's default.
func initialParams(st shellrt.State) []string {
	args := optionParams(st.Options)
	args = append(args, "--")
	return append(args, st.Params...)
}

func optionParams(options map[string]bool) []string {
	var params []string
	for _, name := range slices.Sorted(maps.Keys(options)) {
		if options[name] {
			params = append(params, "-o", name)
		}
	}
	return params
}

func clampStatus(code int) uint8 {
	if code < 0 || code > 255 {
		return 1
	}
	return uint8(code)
}

// Clone gives a child task its own shell. [interp.Runner.Subshell] is the
// public snapshot primitive: the copy inherits variables, functions and traps
// under the shell's own inheritance rules — ERR only under errtrace,
// DEBUG/RETURN only under functrace — and neither side writes through to the
// other. The session serializes this against the parent's other backend calls,
// which Subshell requires.
func (sh *shell) Clone(streams shellrt.Stdio) (shellrt.ShellRunner, error) {
	child := sh.runner.Subshell()
	if err := interp.StdIO(streams.In, streams.Out, streams.Err)(child); err != nil {
		return nil, err
	}
	return &shell{cfg: sh.cfg, runner: child, last: sh.last.Clone(), io: streams}, nil
}

// Close terminates the shell: it runs the EXIT trap exactly once and waits for
// the interpreter's own background work. Running an empty [syntax.File] is the
// interpreter's documented way to drive an EXIT trap without executing
// anything else.
//
// ctx is the shutdown context, which the session derives with
// [context.WithoutCancel] so that a cancelled program still runs its EXIT trap
// and still releases the interpreter's resources.
func (sh *shell) Close(ctx context.Context) error {
	ctx = sh.taskPolicyContext(ctx)
	sh.closeOnce.Do(func() {
		defer func() {
			for _, process := range sh.processes {
				_ = process.Close()
			}
			sh.processes = nil
		}()
		ctx, cancel := context.WithTimeout(ctx, sh.cfg.shutdown)
		defer cancel()
		if sh.cfg.noTraps {
			// Drop the user callbacks but still terminate the shell below, so
			// the interpreter's own background work is still waited for.
			if err := sh.runStatements(ctx, "trap - EXIT DEBUG ERR", "shellrt-shutdown"); err != nil {
				sh.closeErr = err
				return
			}
		}
		// An empty File is the interpreter's own way to drive an EXIT trap and
		// its background-work wait without executing anything else.
		if err := sh.runner.Run(ctx, &syntax.File{}); err != nil {
			if _, convErr := exitStatus(err); convErr != nil {
				sh.closeErr = convErr
			}
		}
		if err := ctx.Err(); err != nil && sh.closeErr == nil {
			sh.closeErr = fmt.Errorf("shellrt: shutdown did not finish within %s: %w", sh.cfg.shutdown, err)
		}
	})
	return sh.closeErr
}

// runStatements runs bookkeeping source without letting it become part of the
// program's observable status.
func (sh *shell) runStatements(ctx context.Context, src, name string) error {
	file, err := sh.parse(src, name)
	if err != nil {
		return err
	}
	for _, stmt := range file.Stmts {
		if err := sh.runner.Run(ctx, stmt); err != nil {
			if _, convErr := exitStatus(err); convErr != nil {
				return convErr
			}
		}
	}
	return nil
}

// RunShell applies the typed side's changes, runs one region, and refreshes
// the typed-visible projection from the live shell.
func (sh *shell) RunShell(ctx context.Context, st *shellrt.State, streams shellrt.Stdio, src string) error {
	ctx = sh.taskPolicyContext(ctx)
	if streams != sh.io {
		if err := sh.restoreStdio(streams); err != nil {
			return err
		}
	}
	if err := sh.applyTypedWrites(ctx, st); err != nil {
		// The typed write did not take. Publish what the shell actually holds
		// rather than the state the caller asked for, then report.
		if projectErr := sh.project(st, sh.lastStatus()); projectErr != nil {
			return errors.Join(err, projectErr)
		}
		return err
	}
	file, err := sh.parse(src, "shellrt")
	if err != nil {
		return err
	}
	// Statement by statement, not as a whole File: running a File implies an
	// exit, which would fire the EXIT trap at the end of every region. Close
	// is what ends the shell.
	//
	// A non-zero status is not a reason to stop. `false; echo ok` runs both
	// commands and ends at 0, exactly as a shell without errexit does. The
	// shell decides when a region stops: Runner.Exited reports both `exit N`
	// and an errexit-tripped failure, and is only valid immediately after the
	// Run that set it.
	ctx = sh.declarationContext(ctx)
	status, exited := 0, false
	for _, stmt := range file.Stmts {
		runErr := sh.runner.Run(ctx, stmt)
		exited = sh.runner.Exited()
		code, fatal := exitStatus(runErr)
		if fatal != nil {
			return fatal
		}
		status = code
		if exited || shellrt.DeclarationsFromContext(ctx).AssignmentRefused() {
			break
		}
	}
	if err := sh.project(st, status); err != nil {
		return err
	}
	st.Exited = exited
	sh.last.Exited = exited
	return nil
}

// lastStatus reads $? without running anything.
func (sh *shell) lastStatus() int {
	v := sh.runner.LiveVar("?")
	code, err := strconv.Atoi(v.Str)
	if err != nil {
		return 1
	}
	return code
}

func (sh *shell) parse(src, name string) (*syntax.File, error) {
	return syntax.NewParser(syntax.Variant(sh.cfg.lang)).Parse(strings.NewReader(src), name)
}

func (sh *shell) restoreStdio(streams shellrt.Stdio) error {
	sh.io = streams
	return interp.StdIO(streams.In, streams.Out, streams.Err)(sh.runner)
}

func exitStatus(runErr error) (int, error) {
	var status interp.ExitStatus
	switch {
	case runErr == nil:
		return 0, nil
	case errors.As(runErr, &status):
		return int(status), nil
	default:
		return 1, runErr
	}
}

// applyTypedWrites pushes only what the typed side changed since the last
// projection. Re-applying the whole projection would flatten shell-only
// attributes — declare -i, namerefs — on variables the typed program never
// touched.
func (sh *shell) applyTypedWrites(ctx context.Context, st *shellrt.State) error {
	// One statement per element, so a failure names the write that failed.
	var stmts []string
	emit := func(format string, args ...any) {
		stmts = append(stmts, fmt.Sprintf(format, args...))
	}
	if st.Dir != sh.last.Dir {
		emit("cd -- %s", quote(st.Dir))
	}
	for _, name := range shellrt.KnownOptions() {
		if st.Options[name] != sh.last.Options[name] {
			flag := "+o"
			if st.Options[name] {
				flag = "-o"
			}
			emit("set %s %s", flag, name)
		}
	}
	if !slices.Equal(st.Params, sh.last.Params) {
		// One `set --` rewrites the whole list, which is the only way the
		// shell offers: positional parameters have no per-element write.
		// Every element is quoted, so spaces, newlines and leading dashes
		// survive as one parameter each.
		quoted := make([]string, 0, len(st.Params))
		for _, param := range st.Params {
			quoted = append(quoted, quote(param))
		}
		emit("set -- %s", strings.Join(quoted, " "))
	}
	for _, name := range slices.Sorted(maps.Keys(sh.last.Vars)) {
		if _, ok := st.Vars[name]; !ok {
			emit("unset %s", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(st.Vars)) {
		v := st.Vars[name]
		if old, ok := sh.last.Vars[name]; ok && old.Equal(v) {
			continue
		}
		for _, stmt := range assignment(name, v) {
			stmts = append(stmts, stmt)
		}
	}
	if len(stmts) > 0 {
		file, err := sh.parse(strings.Join(stmts, "\n"), "shellrt-apply")
		if err != nil {
			return err
		}
		if len(file.Stmts) != len(stmts) {
			return fmt.Errorf("shellrt: applying typed state: %d statements parsed as %d", len(stmts), len(file.Stmts))
		}
		for i, stmt := range file.Stmts {
			runErr := sh.runner.Run(ctx, stmt)
			code, fatal := exitStatus(runErr)
			if fatal != nil {
				return &ApplyError{Source: stmts[i], Err: fatal}
			}
			// A refused write -- readonly variable, missing directory -- must
			// not be swallowed and then projected as if it had worked.
			if code != 0 {
				return &ApplyError{Source: stmts[i], Status: code}
			}
		}
	}
	// The applied statements are bookkeeping, not program commands: restore
	// the status the region is meant to start from, whether it came from the
	// previous region or from a typed-side write.
	sh.runner.SetLastExitStatus(clampStatus(st.Status))
	return nil
}

// ApplyError reports a typed-side write the shell refused: a readonly
// variable, a directory that does not exist, and so on. The session's
// projection is refreshed from the live shell before it is returned, so the
// caller sees what the shell actually holds.
type ApplyError struct {
	Source string // the bookkeeping statement that failed
	Status int    // the shell status it produced, if it was not fatal
	Err    error  // set when the failure was fatal rather than a status
}

func (e *ApplyError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("shellrt: applying %q: %v", e.Source, e.Err)
	}
	return fmt.Sprintf("shellrt: applying %q: exit status %d", e.Source, e.Status)
}

func (e *ApplyError) Unwrap() error { return e.Err }

func assignment(name string, v shellrt.Var) []string {
	var value strings.Builder
	switch v.Kind {
	case shellrt.Indexed:
		fmt.Fprintf(&value, "%s=(", name)
		for i, elem := range v.List {
			if i > 0 {
				value.WriteByte(' ')
			}
			value.WriteString(quote(elem))
		}
		value.WriteString(")")
	case shellrt.Associative:
		fmt.Fprintf(&value, "declare -A %s=(", name)
		for _, key := range slices.Sorted(maps.Keys(v.Map)) {
			fmt.Fprintf(&value, "[%s]=%s ", quote(key), quote(v.Map[key]))
		}
		value.WriteString(")")
	default:
		fmt.Fprintf(&value, "%s=%s", name, quote(v.Str))
	}
	stmts := []string{value.String()}
	if v.Exported {
		stmts = append(stmts, "export "+name)
	}
	if v.ReadOnly {
		stmts = append(stmts, "readonly "+name)
	}
	return stmts
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// project refreshes the typed-visible view from the live shell.
func (sh *shell) project(st *shellrt.State, status int) error {
	st.Dir = sh.runner.Dir
	st.Status = status
	st.Exited = false

	managed := managedNames(sh.cfg.lang)
	names := map[string]bool{}
	for name := range sh.last.Vars {
		names[name] = true
	}
	for name := range sh.runner.Vars {
		if !managed[name] {
			names[name] = true
		}
	}
	vars := make(map[string]shellrt.Var, len(names))
	for name := range names {
		if v, ok := projectVar(sh.runner.LiveVar(name)); ok {
			vars[name] = v
		}
	}
	st.Vars = vars

	// `set --` and `shift` inside the region move the live parameters; the
	// copy keeps the projection from aliasing the runner's own slice.
	st.Params = slices.Clone(sh.runner.Params)
	if st.Params == nil {
		st.Params = []string{}
	}

	st.Options = sh.projectOptions()
	// The option probe is bookkeeping too; $? must still report the region.
	sh.runner.SetLastExitStatus(clampStatus(status))
	sh.last = st.Clone()
	return nil
}

func projectVar(v expand.Variable) (shellrt.Var, bool) {
	if !v.IsSet() {
		return shellrt.Var{}, false
	}
	out := shellrt.Var{Exported: v.Exported, ReadOnly: v.ReadOnly}
	switch v.Kind {
	case expand.String:
		out.Str = v.Str
	case expand.Indexed:
		out.Kind = shellrt.Indexed
		out.List = v.IndexedValues()
	case expand.Associative:
		out.Kind = shellrt.Associative
		out.Map = maps.Clone(v.Map)
	default:
		// Namerefs and live Go objects have no projection yet; they stay in
		// the shell rather than being flattened into a wrong scalar.
		return shellrt.Var{}, false
	}
	return out, true
}

// projectOptions reads the live option state out of SHELLOPTS. That is a pure
// variable read: unlike running `set +o`, it executes no command, so it fires
// no DEBUG or ERR trap, cannot trip errexit, cannot perturb $?, and cannot
// capture a trap's output as if it were option state. Region bookkeeping must
// stay invisible to the program being run.
func (sh *shell) projectOptions() map[string]bool {
	enabled := map[string]bool{}
	for _, name := range strings.Split(sh.runner.LiveVar("SHELLOPTS").Str, ":") {
		enabled[name] = true
	}
	options := map[string]bool{}
	for _, name := range shellrt.KnownOptions() {
		options[name] = enabled[name]
	}
	return options
}

// managedNames is the set of variables a fresh runner defines by itself. It is
// computed from the interpreter so it stays correct as the engine's defaults
// change, instead of freezing a hand-written list here.
var (
	managedMu    sync.Mutex
	managedCache = map[syntax.LangVariant]map[string]bool{}
)

func managedNames(lang syntax.LangVariant) map[string]bool {
	managedMu.Lock()
	defer managedMu.Unlock()
	if names, ok := managedCache[lang]; ok {
		return names
	}
	names := map[string]bool{}
	runner, err := interp.New(
		interp.Lang(lang),
		interp.Env(expand.ListEnviron()),
		interp.StdIO(nil, io.Discard, io.Discard),
	)
	if err == nil && runner.Run(context.Background(), &syntax.File{}) == nil {
		for name := range runner.Vars {
			names[name] = true
		}
	}
	managedCache[lang] = names
	return names
}

// stateEnviron seeds a runner from a projection, preserving each variable's
// kind and export attribute so that arrays arrive as arrays and only exported
// names reach launched processes.
type stateEnviron struct{ vars map[string]shellrt.Var }

func (e stateEnviron) Get(name string) expand.Variable {
	v, ok := e.vars[name]
	if !ok {
		return expand.Variable{}
	}
	vr := expand.Variable{Set: true, Exported: v.Exported, ReadOnly: v.ReadOnly}
	switch v.Kind {
	case shellrt.Indexed:
		vr.Kind, vr.List = expand.Indexed, slices.Clone(v.List)
	case shellrt.Associative:
		vr.Kind, vr.Map = expand.Associative, maps.Clone(v.Map)
	default:
		vr.Kind, vr.Str = expand.String, v.Str
	}
	return vr
}

func (e stateEnviron) Each(fn func(name string, vr expand.Variable) bool) {
	for _, name := range slices.Sorted(maps.Keys(e.vars)) {
		if !fn(name, e.Get(name)) {
			return
		}
	}
}
