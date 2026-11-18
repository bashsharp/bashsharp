// Package polyglot plans and runs declaration-only foreign source modules.
package polyglot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Block struct {
	Language string
	Alias    string
	// Runner is the fence's runner override (`!name`), "" for the
	// language's own adapter.
	Runner string
	Source string
	// Origin identifies the source artifact for this block, including when
	// another block has identical bytes in a different directory.
	Origin   string
	Filename string
	Line     int
}

type Signature struct {
	Params   []string `json:"params"`
	Results  []string `json:"results"`
	Dynamic  bool     `json:"dynamic"`
	Variadic bool     `json:"variadic,omitempty"`
	Iterator string   `json:"iterator,omitempty"`
	Filter   bool     `json:"filter,omitempty"`
	// PipeInput says the function reads the command's stdin when it runs as a
	// shell command: a PowerShell function with a process block, a $input
	// reference, or a ValueFromPipeline parameter. The command adapter feeds
	// the upstream byte pipe to such a function and leaves the others alone, so
	// a non-filter command never blocks reading a terminal.
	PipeInput bool `json:"pipe_input,omitempty"`
}

type Export struct {
	// Unavailable records a declaration that is module-internal, not callable.
	// Prepare separates these diagnostic records from the callable exports.
	Unavailable string    `json:"unavailable,omitempty"`
	Name        string    `json:"name"`
	Signature   Signature `json:"signature"`
	// Effects are the effect atoms a world-changing text verb carries; nil
	// for a source function or a read-only verb.
	Effects []string `json:"effects,omitempty"`
	// Agentic requires an explicitly agentic caller before dispatch.
	Agentic bool `json:"agentic,omitempty"`
}

func (e Export) UnavailableDiagnostic() string {
	return fmt.Sprintf("Go function %s is not callable from Bash#: %s", e.Name, e.Unavailable)
}

type Plan struct {
	CSharpAssemblies   string
	RustTarget         string
	UnavailableExports []Export
	ID                 string
	Language           string
	Alias              string
	Runner             string
	Source             string
	Origin             string
	Artifact           string
	Exports            []Export
}

type Analyzer interface {
	Analyze(context.Context, string) ([]Export, error)
}

type artifactAnalyzer interface {
	AnalyzeArtifact(context.Context, string) ([]Export, string, error)
}

// Prepare aggregates same-language blocks into one immutable module. Alias
// policy is per source unit: all blocks for a language must use the same alias.
func Prepare(ctx context.Context, blocks []Block, analyzers map[string]Analyzer) ([]Plan, error) {
	type aggregate struct {
		alias  string
		runner string
		blocks []Block
	}
	groups := map[string]*aggregate{}
	for _, block := range blocks {
		lang := canonicalLanguage(block.Language)
		if lang == "" {
			return nil, errors.New("polyglot: empty language")
		}
		group := groups[lang]
		if group == nil {
			group = &aggregate{alias: block.Alias, runner: block.Runner}
			groups[lang] = group
		}
		if group.alias != block.Alias {
			return nil, fmt.Errorf("polyglot: inconsistent aliases for %s", lang)
		}
		if group.runner != block.Runner {
			return nil, fmt.Errorf("polyglot: inconsistent runners for %s", lang)
		}
		group.blocks = append(group.blocks, block)
	}
	langs := make([]string, 0, len(groups))
	for lang := range groups {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	plans := make([]Plan, 0, len(langs))
	for _, lang := range langs {
		analyzer := analyzers[lang]
		if analyzer == nil {
			return nil, fmt.Errorf("polyglot: unsupported language %q", lang)
		}
		group := groups[lang]
		source := aggregateSource(lang, group.blocks)
		if lang == "go" {
			if goAnalyzer, ok := analyzer.(Go); ok {
				assetDir, originErr := goEmbedSourceDir(group.blocks)
				if originErr != nil {
					return nil, fmt.Errorf("polyglot go: %w", originErr)
				}
				goAnalyzer.AssetSourceDir = assetDir
				analyzer = goAnalyzer
			}
		}
		var exports []Export
		var artifact string
		var err error
		if aa, ok := analyzer.(artifactAnalyzer); ok {
			exports, artifact, err = aa.AnalyzeArtifact(ctx, source)
		} else {
			exports, err = analyzer.Analyze(ctx, source)
		}
		if err != nil {
			return nil, fmt.Errorf("polyglot %s: %w", lang, err)
		}
		var callable, unavailable []Export
		for _, export := range exports {
			if export.Unavailable != "" {
				unavailable = append(unavailable, export)
			} else {
				callable = append(callable, export)
			}
		}
		graph := ""
		rustTarget := ""
		if c, ok := analyzer.(CSharp); ok && c.Environment != nil {
			graph = c.Environment.CSharpAssemblies
		}
		if r, ok := analyzer.(Rust); ok {
			rustTarget, err = r.target()
			if err != nil {
				return nil, fmt.Errorf("polyglot rust: %w", err)
			}
		}
		origin := group.blocks[0].Origin
		identityTarget := rustTarget
		if identityTarget == "binary" {
			identityTarget = ""
		}
		plans = append(plans, Plan{CSharpAssemblies: graph, RustTarget: rustTarget, ID: planID(lang, group.alias, source, artifact+graph+identityTarget, group.runner, origin), Language: lang, Alias: group.alias, Runner: group.runner, Source: source, Origin: origin, Artifact: artifact, Exports: callable, UnavailableExports: unavailable})
	}
	return plans, nil
}

func planID(language, alias, source, artifact, runner, origin string) string {
	hash := sha256.Sum256([]byte(language + "\x00" + alias + "\x00" + source + "\x00" + artifact + "\x00" + runner + "\x00" + origin))
	return hex.EncodeToString(hash[:])
}

func aggregateSource(language string, blocks []Block) string {
	if language == "go" {
		return aggregateGoSource(blocks)
	}
	if !languageFlag(language, func(l Language) bool { return l.LineDirectives }) {
		sources := make([]string, len(blocks))
		for i := range blocks {
			sources[i] = blocks[i].Source
		}
		return strings.Join(sources, "\n")
	}
	var out strings.Builder
	for _, block := range blocks {
		name := block.Filename
		if name == "" {
			name = "<bash++ " + language + ">"
		}
		line := block.Line
		if line < 1 {
			line = 1
		}
		fmt.Fprintf(&out, "#line %d %s\n%s", line, strconv.Quote(name), block.Source)
		if !strings.HasSuffix(block.Source, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

type Python struct {
	Command     string
	Environment *EnvironmentPlan
	// Cwd, when set, answers the caller's directory at call time. The worker
	// starts in the environment root (where the project resolves); each call
	// then runs in the caller's directory, so relative paths inside a fence
	// mean what they mean to the shell line that called it.
	Cwd func() string
}

func (p Python) executable() string {
	if p.Environment != nil && p.Environment.Executable != "" {
		return p.Environment.Executable
	}
	if p.Command != "" {
		return p.Command
	}
	return "python3"
}

func (p Python) Analyze(ctx context.Context, source string) ([]Export, error) {
	name, argv := workerExecArgs(p.executable(), p.pythonArguments(pythonAnalyze, false))
	cmd := exec.CommandContext(ctx, name, argv...)
	p.configure(cmd)
	cmd.Stdin = strings.NewReader(source)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Python runtime unavailable: %w", err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	var exports []Export
	if err := json.Unmarshal(stdout.Bytes(), &exports); err != nil {
		return nil, fmt.Errorf("invalid analyzer response: %w", err)
	}
	return exports, nil
}

type CallResult struct {
	Value          any
	Stdout, Stderr string
}

// ErrorDetail is the neutral wire/detail shape for structured foreign worker
// failures. It is deliberately not an error type and it is not tied to any
// command-envelope package: callers that need a domain-specific envelope
// should map this DTO at their own boundary. Workers may still send the legacy
// plain string; decoding normalizes that form into Message while leaving Code
// and Help empty.
type ErrorDetail struct {
	Code    string       `json:"code,omitempty"`
	Message string       `json:"message,omitempty"`
	Help    string       `json:"help,omitempty"`
	Cause   *ErrorDetail `json:"cause,omitempty"`
}

// ForeignErrorDetail returns the structured detail carried by an error
// returned from a foreign worker. This is the stable mapper seam for embedders:
// a separate coreutils/weave run can convert the returned detail to its own
// weavecli.EnvelopeError without making sh import coreutils or weave packages.
func ForeignErrorDetail(err error) (*ErrorDetail, bool) {
	var foreign *foreignError
	if !errors.As(err, &foreign) {
		return nil, false
	}
	detail := foreign.detail.clone()
	return &detail, true
}

type foreignError struct {
	detail ErrorDetail
	cause  error
}

func (e *foreignError) Error() string {
	if e == nil {
		return ""
	}
	message := e.detail.Message
	if message == "" {
		message = e.detail.Code
	}
	if e.detail.Code != "" && e.detail.Message != "" {
		message = e.detail.Code + ": " + e.detail.Message
	}
	if e.cause != nil {
		cause := e.cause.Error()
		if cause != "" {
			message += ": " + cause
		}
	}
	if message == "" {
		return "foreign worker error"
	}
	return message
}

func (e *foreignError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (d *ErrorDetail) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*d = ErrorDetail{}
		return nil
	}
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		*d = ErrorDetail{Message: legacy}
		return nil
	}
	type errorDetail ErrorDetail
	var structured errorDetail
	if err := json.Unmarshal(data, &structured); err != nil {
		return err
	}
	*d = ErrorDetail(structured)
	return nil
}

func (d ErrorDetail) clone() ErrorDetail {
	out := ErrorDetail{Code: d.Code, Message: d.Message, Help: d.Help}
	if d.Cause != nil {
		cause := d.Cause.clone()
		out.Cause = &cause
	}
	return out
}

func (d ErrorDetail) err() error {
	cause := error(nil)
	if d.Cause != nil {
		cause = d.Cause.err()
	}
	return &foreignError{detail: d.clone(), cause: cause}
}

// StringsToAny adapts a typed variadic shell boundary to Module.Call.
func StringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

// leadingArgs are the arguments a resolved tool carries before any of its
// own (zig cc → [cc]); every launch places them right after the executable.
func leadingArgs(environment *EnvironmentPlan) []string {
	if environment == nil {
		return nil
	}
	return append([]string(nil), environment.ExecutableArgs...)
}

type Runtime interface {
	executable() string
	arguments(Plan) []string
	loadRequest(Plan) map[string]any
	name() string
}

type configuredRuntime interface{ configure(*exec.Cmd) }

// artifactWorker is a Runtime whose persistent worker is the compiled binary
// carried in Plan.Artifact rather than an interpreter reading a script. ensure
// writes the artifact to a private temp binary (under artifactPrefix) and
// launches it directly; a restart after kill rewrites it from the immutable
// plan.
type artifactWorker interface {
	Runtime
	artifactPrefix() string
}

// Embedded adapts an in-process runtime without making polyglot depend on its
// implementation package. Dialect islands use this path and never start an
// external worker.
type Embedded struct {
	RuntimeName string
	AnalyzeFunc func(context.Context, string) ([]Export, error)
	CallFunc    func(context.Context, Plan, string, []any, map[string]any) (CallResult, error)
	// CloseFunc releases resources owned by this module, including on cancellation.
	// It must be idempotent; Close may be called more than once.
	CloseFunc func(Plan) error
}

func (e Embedded) Analyze(ctx context.Context, source string) ([]Export, error) {
	return e.AnalyzeFunc(ctx, source)
}
func (e Embedded) executable() string              { return "" }
func (e Embedded) arguments(Plan) []string         { return nil }
func (e Embedded) loadRequest(Plan) map[string]any { return nil }
func (e Embedded) name() string                    { return e.RuntimeName }

func (p Python) arguments(Plan) []string { return p.pythonArguments(pythonWorker, true) }
func (p Python) pythonArguments(script string, unbuffered bool) []string {
	args := append(leadingArgs(p.Environment), pythonLauncherArgs(p.executable())...)
	args = append(args, "-I")
	if unbuffered {
		args = append(args, "-u")
	}
	args = append(args, "-c", script)
	if p.Environment != nil {
		args = append(args, p.Environment.PythonPath...)
	}
	return args
}

// pythonLauncherArgs pins CPython 3 when the resolved runtime is the Windows
// launcher: `py` is not itself an interpreter, and without -3 it starts
// whatever default the host registers.
func pythonLauncherArgs(executable string) []string {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(executable)), ".exe")
	if base == "py" {
		return []string{"-3"}
	}
	return nil
}

// workerExecArgs rewrites a worker launch whose executable is a Windows App
// Execution Alias — a reparse point os/exec refuses with "not supported by
// windows" — to run through cmd.exe, which starts aliases fine. Everything
// else launches unchanged.
func workerExecArgs(path string, args []string) (string, []string) {
	if !execNeedsCmdShim(lstatFileMode(path), runtime.GOOS) {
		return path, args
	}
	return "cmd.exe", append([]string{"/d", "/c", path}, args...)
}

func lstatFileMode(path string) os.FileMode {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	return info.Mode()
}

func execNeedsCmdShim(mode os.FileMode, goos string) bool {
	return goos == "windows" && mode&os.ModeIrregular != 0
}
func (p Python) loadRequest(plan Plan) map[string]any {
	return map[string]any{"id": 0, "op": "load", "source": plan.Source}
}
func (p Python) name() string { return "Python" }

func (p Python) configure(cmd *exec.Cmd) {
	if p.Environment == nil {
		return
	}
	cmd.Dir = p.Environment.Dir
	cmd.Env = append([]string(nil), p.Environment.Env...)
}

type Module struct {
	jobOnce    sync.Once
	jobSem     chan struct{}
	plan       Plan
	importPlan *ImportPlan
	runtime    Runtime
	// sem serializes callers and honours their context, so a caller that
	// would wait forever — a nested call from a callback that did not pass
	// the callback's context — fails when its context ends instead. A shell
	// callback re-enters through lock, which recognizes the callback's
	// context and does not take sem again.
	semOnce sync.Once
	sem     chan struct{}
	// procMu guards the worker process fields, which a cancellation may
	// tear down while a callback's nested exchange still runs.
	procMu     sync.Mutex
	cmd        *exec.Cmd
	in         io.WriteCloser
	out        *bufio.Reader
	outFile    io.ReadCloser
	nextID     uint64
	generation uint64
	pendingOut string
	pendingErr string
	tempDir    string

	callbacks Callbacks
	// callbackTable holds the callbacks the in-flight calls passed, by wire
	// id; pendingCallbacks are the ids the request being encoded registered,
	// which that request removes when it completes.
	callbackTable    map[uint64]Callback
	pendingCallbacks []uint64
	nextCallback     uint64
	callbackDepth    int
	// lastExit describes how the previous worker process ended, recorded by
	// kill so a request that failed because the worker died on its own can
	// report the signal or status instead of a bare transport error.
	lastExit workerExit
}

// workerExit is kill's record of a worker that had already ended when kill
// reached it. died is false when kill itself ended the process.
type workerExit struct {
	died   bool
	signal int
	code   int
}

func Start(plan Plan, runtime Runtime) *Module { return &Module{plan: plan, runtime: runtime} }

// MaxCallbackDepth bounds shell callback re-entry: a callback may call back
// into the worker, whose function may call the shell again, until this many
// callbacks are active on one module. The next callback is refused with an
// error the worker's function receives, so a mutual recursion between a
// fence and a shell function always terminates.
const MaxCallbackDepth = 8

// Callback is a host function a foreign worker may invoke while the call
// that passed it is in flight. It crosses the boundary as the value adapter's
// `{"$callback": id}` shape and expires when that call completes: a worker
// that stores it and calls it later is refused.
type Callback struct {
	Name   string
	Invoke func(ctx context.Context, args []any) (any, error)
}

// CallbackFailure marks a shell callback error which has a shell status.
// Workers expose Status on their language-specific callback exception while
// retaining Message as the ordinary error text.
type CallbackFailure struct {
	Message string
	Status  int
}

func (e *CallbackFailure) Error() string       { return e.Message }
func (e *CallbackFailure) CallbackStatus() int { return e.Status }

// Callbacks configures how a module serves shell callbacks.
type Callbacks struct {
	// Resolve maps the name a call passed as a plain string for a callback
	// parameter to the function it names. Nil refuses named callbacks.
	Resolve func(name string) (Callback, bool)
	// Output receives island stdout/stderr the worker produced before a
	// callback runs, so it is shown before the callback's own output. Nil
	// accumulates it into the call's result instead.
	Output func(stdout, stderr string)
	// Enter, when set, runs before every callback with the callback's
	// context — the one a nested call must carry to re-enter the module —
	// and the func it returns runs after. A host whose call sites cannot
	// receive a context installs it there.
	Enter func(ctx context.Context) func()
}

// SetCallbacks installs the module's shell callback configuration.
func (m *Module) SetCallbacks(callbacks Callbacks) {
	unlock, _ := m.lock(context.Background())
	defer unlock()
	m.callbacks = callbacks
}

// FuncCallback adapts a Go function to a Callback. Arguments are converted
// from the boundary's JSON values to the parameter types; the results are
// the single result, nil for none, or a list.
func FuncCallback(name string, fn any) Callback {
	value := reflect.ValueOf(fn)
	if value.Kind() != reflect.Func {
		return Callback{Name: name, Invoke: func(context.Context, []any) (any, error) {
			return nil, fmt.Errorf("polyglot: callback %s is not a function", name)
		}}
	}
	return Callback{Name: name, Invoke: func(ctx context.Context, args []any) (result any, err error) {
		typ := value.Type()
		if typ.IsVariadic() || len(args) != typ.NumIn() {
			return nil, fmt.Errorf("polyglot: callback %s expects %d arguments, got %d", name, typ.NumIn(), len(args))
		}
		in := make([]reflect.Value, len(args))
		for i, arg := range args {
			converted, err := callbackArgument(arg, typ.In(i))
			if err != nil {
				return nil, fmt.Errorf("polyglot: callback %s argument %d: %w", name, i+1, err)
			}
			in[i] = converted
		}
		defer func() {
			if failure := recover(); failure != nil {
				result, err = nil, fmt.Errorf("polyglot: callback %s panicked: %v", name, failure)
			}
		}()
		out := value.Call(in)
		if len(out) > 0 && typ.Out(len(out)-1) == reflect.TypeFor[error]() {
			if failure, _ := out[len(out)-1].Interface().(error); failure != nil {
				return nil, failure
			}
			out = out[:len(out)-1]
		}
		switch len(out) {
		case 0:
			return nil, nil
		case 1:
			return out[0].Interface(), nil
		}
		values := make([]any, len(out))
		for i := range out {
			values[i] = out[i].Interface()
		}
		return values, nil
	}}
}

func callbackArgument(arg any, want reflect.Type) (reflect.Value, error) {
	if arg == nil {
		return reflect.Zero(want), nil
	}
	value := reflect.ValueOf(arg)
	if value.Type().AssignableTo(want) {
		return value, nil
	}
	if value.Type().ConvertibleTo(want) && (want.Kind() != reflect.String || value.Kind() == reflect.String) {
		return value.Convert(want), nil
	}
	if want.Kind() == reflect.String {
		return reflect.ValueOf(fmt.Sprint(arg)), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %T as %s", arg, want)
}

type reentrantKey struct{}

// lock serializes callers, except a shell callback re-entering the module
// whose call is in flight: its context carries the module, and its nested
// request rides the same protocol stream last-in-first-out. Waiting ends
// with the context.
func (m *Module) lock(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner, _ := ctx.Value(reentrantKey{}).(*Module); owner == m {
		return func() {}, nil
	}
	m.semOnce.Do(func() { m.sem = make(chan struct{}, 1) })
	select {
	case m.sem <- struct{}{}:
		return func() { <-m.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// StartImport creates a lazy direct Python import. The process and module are
// not loaded until the first operation.
func StartImport(plan ImportPlan) *Module {
	copy := plan.Clone()
	return &Module{
		plan:       Plan{ID: plan.ID, Language: plan.Language, Alias: plan.Alias},
		importPlan: &copy, runtime: Python{Environment: &copy.Environment},
	}
}

func (m *Module) Plan() Plan { return m.plan }

func (m *Module) alive() bool {
	m.procMu.Lock()
	defer m.procMu.Unlock()
	return m.cmd != nil
}

func (m *Module) ensure(ctx context.Context) error {
	if m.alive() {
		return nil
	}
	var name string
	var argv []string
	if worker, ok := m.runtime.(artifactWorker); ok {
		path, err := m.artifactPath(worker.artifactPrefix())
		if err != nil {
			return err
		}
		name, argv = workerExecArgs(path, nil)
	} else {
		name, argv = workerExecArgs(m.runtime.executable(), m.runtime.arguments(m.plan))
	}
	cmd := exec.Command(name, argv...)
	if configured, ok := m.runtime.(configuredRuntime); ok {
		configured.configure(cmd)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var protocolRead io.ReadCloser
	var protocolWrite *os.File
	_, typeScript := m.runtime.(TypeScript)
	// Windows refuses ExtraFiles outright (syscall.StartProcess: more than
	// three files → EWINDOWS, "not supported by windows"), so there the
	// protocol rides the worker's stdout: every worker falls back to it when
	// fd 3 is absent, and island output is captured per call, never on
	// stdout between calls.
	if runtime.GOOS == "windows" {
		protocolRead, err = cmd.StdoutPipe()
		if err != nil {
			in.Close()
			return err
		}
		cmd.Stderr = io.Discard
	} else {
		protocolRead, protocolWrite, err = os.Pipe()
		if err != nil {
			in.Close()
			return err
		}
		cmd.ExtraFiles = []*os.File{protocolWrite}
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	}
	if err := cmd.Start(); err != nil {
		_ = protocolRead.Close()
		if protocolWrite != nil {
			_ = protocolWrite.Close()
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s runtime unavailable: %w", m.runtime.name(), err)
		}
		return err
	}
	if protocolWrite != nil {
		_ = protocolWrite.Close()
	}
	m.procMu.Lock()
	m.generation++
	m.cmd, m.in, m.out, m.outFile = cmd, in, bufio.NewReader(protocolRead), protocolRead
	m.procMu.Unlock()
	var load map[string]any
	if loader, ok := m.runtime.(interface {
		loadRequestContext(context.Context, Plan) map[string]any
	}); ok {
		load = loader.loadRequestContext(ctx, m.plan)
	} else {
		load = m.runtime.loadRequest(m.plan)
	}
	if err := ctx.Err(); err != nil {
		m.kill()
		return err
	}
	if typeScript {
		m.tempDir, err = os.MkdirTemp("", "bashpp-typescript-")
		if err != nil {
			m.kill()
			return err
		}
		load["module_dir"] = m.tempDir
	}
	if m.importPlan != nil {
		load = map[string]any{"id": 0, "op": "import", "module": m.importPlan.Module}
		if m.importPlan.Path != "" {
			load["path"] = m.importPlan.Path
		}
	}
	var response workerResponse
	if err := m.exchangeContext(ctx, load, &response); err != nil {
		m.kill()
		return fmt.Errorf("load %s module: %w", m.runtime.name(), err)
	}
	if !response.OK {
		m.kill()
		return response.Error.err()
	}
	if response.ID != 0 {
		m.kill()
		return fmt.Errorf("load %s module: response ID %d does not match request ID 0", m.runtime.name(), response.ID)
	}
	m.pendingOut, m.pendingErr = response.Stdout, response.Stderr
	return nil
}

func (m *Module) Call(ctx context.Context, name string, args ...any) (CallResult, error) {
	return m.CallKeywords(ctx, name, args, nil)
}

// CallKeywords invokes a source export or direct-module attribute.
func (m *Module) CallKeywords(ctx context.Context, name string, args []any, kwargs map[string]any) (CallResult, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return CallResult{}, err
	}
	defer unlock()
	if embedded, ok := m.runtime.(Embedded); ok {
		return embedded.CallFunc(ctx, m.plan, name, args, kwargs)
	}
	if text, ok := m.runtime.(Text); ok {
		return m.callText(ctx, text, name, args, kwargs)
	}
	if fence, ok := m.runtime.(RunnerFence); ok {
		return m.callRunner(ctx, fence, name, args, kwargs)
	}
	if _, ok := m.runtime.(Go); ok {
		return m.callGo(ctx, name, args, kwargs)
	}
	if _, ok := m.runtime.(Rust); ok {
		if m.plan.RustTarget == "proc-macro" {
			return CallResult{}, errors.New("Rust proc-macro target has no callable exports")
		}
		if len(kwargs) != 0 {
			return CallResult{}, errors.New("Rust functions do not accept named arguments")
		}
		if name == RustReleaseExport && rustReleaseExport(m.plan) {
			return m.releaseCall(ctx, args)
		}
	}
	if languageFlag(m.plan.Language, func(language Language) bool { return language.Callbacks }) {
		if args, err = m.resolveCallbacks(name, args); err != nil {
			return CallResult{}, err
		}
	}
	if _, ok := m.runtime.(C); ok {
		return m.callNativeArtifact(ctx, "C", name, args, kwargs)
	}
	if _, ok := m.runtime.(CPP); ok {
		return m.callNativeArtifact(ctx, "C++", name, args, kwargs)
	}
	if err := m.ensure(ctx); err != nil {
		return CallResult{}, err
	}
	m.nextID++
	encodedArgs, err := m.encodeValue(args)
	if err != nil {
		m.dropPendingCallbacks()()
		return CallResult{}, err
	}
	encodedKwargs, err := m.encodeValue(kwargs)
	if err != nil {
		m.dropPendingCallbacks()()
		return CallResult{}, err
	}
	defer m.dropPendingCallbacks()()
	request := map[string]any{"id": m.nextID, "op": "call", "name": name, "args": encodedArgs, "kwargs": encodedKwargs}
	return m.request(ctx, request, name)
}

// resolveCallbacks turns the plain string passed for each callback-typed
// parameter of the export into the shell function it names, through the
// module's Resolve hook; a Callback or Go function passes unchanged.
func (m *Module) resolveCallbacks(name string, args []any) ([]any, error) {
	var export *Export
	for i := range m.plan.Exports {
		if m.plan.Exports[i].Name == name {
			export = &m.plan.Exports[i]
			break
		}
	}
	if export == nil {
		return args, nil
	}
	resolved := args
	for i, typ := range export.Signature.Params {
		if typ != "callback" || i >= len(args) {
			continue
		}
		text, ok := args[i].(string)
		if !ok {
			continue
		}
		if m.callbacks.Resolve == nil {
			return nil, fmt.Errorf("polyglot: %s function %s argument %d: no shell callback resolver for %q", m.runtime.name(), name, i+1, text)
		}
		callback, ok := m.callbacks.Resolve(text)
		if !ok {
			return nil, fmt.Errorf("polyglot: %s function %s argument %d: unknown shell callback %q", m.runtime.name(), name, i+1, text)
		}
		if &resolved[0] == &args[0] {
			resolved = append([]any(nil), args...)
		}
		resolved[i] = callback
	}
	return resolved, nil
}

// releaseCall is the synthetic Rust release export: it frees the one handle
// argument on the worker.
func (m *Module) releaseCall(ctx context.Context, args []any) (CallResult, error) {
	if len(args) != 1 {
		return CallResult{}, fmt.Errorf("Rust function %s expects 1 arguments, got %d", RustReleaseExport, len(args))
	}
	handle, ok := args[0].(*Handle)
	if !ok {
		return CallResult{}, fmt.Errorf("Rust function %s argument 1: expected a Rust handle, got %T", RustReleaseExport, args[0])
	}
	if handle == nil || handle.module != m {
		return CallResult{}, errors.New("polyglot: stale or foreign Rust handle")
	}
	if !m.alive() || handle.generation != m.generation {
		return CallResult{}, errors.New("polyglot: stale Rust handle: its worker is gone")
	}
	m.nextID++
	return m.request(ctx, map[string]any{"id": m.nextID, "op": "release", "handle": handle.ID}, nil)
}

// dropPendingCallbacks scopes the callbacks the request being encoded
// registered to that request: the returned func removes them.
func (m *Module) dropPendingCallbacks() func() {
	ids := m.pendingCallbacks
	m.pendingCallbacks = nil
	return func() {
		for _, id := range ids {
			delete(m.callbackTable, id)
		}
	}
}

func (m *Module) registerCallback(callback Callback) uint64 {
	if m.callbackTable == nil {
		m.callbackTable = map[uint64]Callback{}
	}
	m.nextCallback++
	m.callbackTable[m.nextCallback] = callback
	m.pendingCallbacks = append(m.pendingCallbacks, m.nextCallback)
	return m.nextCallback
}

// Handle is an opaque value owned by one worker generation: a Python object
// or a Rust bashpp::Handle<T>. It is valid until it is released or its worker
// exits; a restarted worker refuses the handles of the generation before it.
//
// Its JSON form is its shell text (`{"id":1,"type":"Res"}`): the exported
// fields are what expand.ObjectString shows for an Object variable holding a
// handle. It deliberately has no String or MarshalJSON method, which the
// Object model refuses.
type Handle struct {
	module     *Module
	generation uint64
	ID         uint64 `json:"id"`
	Type       string `json:"type,omitempty"`
	Repr       string `json:"repr,omitempty"`
	Callable   bool   `json:"callable,omitempty"`
}

func (h *Handle) stale() error {
	if h == nil || h.module == nil {
		return errors.New("polyglot: stale or foreign handle")
	}
	return fmt.Errorf("polyglot: stale or foreign %s handle", h.module.runtime.name())
}

func (h *Handle) GetAttr(ctx context.Context, name string) (CallResult, error) {
	if h == nil || h.module == nil {
		return CallResult{}, h.stale()
	}
	return h.module.GetAttr(ctx, h, name)
}

func (h *Handle) Call(ctx context.Context, args []any, kwargs map[string]any) (CallResult, error) {
	if h == nil || h.module == nil {
		return CallResult{}, h.stale()
	}
	return h.module.CallHandle(ctx, h, args, kwargs)
}

func (h *Handle) CallAttr(ctx context.Context, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if h == nil || h.module == nil {
		return CallResult{}, h.stale()
	}
	return h.module.CallAttr(ctx, h, name, args, kwargs)
}

// Release frees the worker-owned value exactly once; a second release is
// refused as stale.
func (h *Handle) Release(ctx context.Context) error {
	if h == nil || h.module == nil {
		return h.stale()
	}
	return h.module.Release(ctx, h)
}

func (m *Module) GetAttr(ctx context.Context, handle *Handle, name string) (CallResult, error) {
	if !publicPythonAttribute(name) {
		return CallResult{}, fmt.Errorf("Python attribute %q is private", name)
	}
	return m.handleRequest(ctx, handle, map[string]any{"op": "getattr", "name": name})
}

func (m *Module) CallHandle(ctx context.Context, handle *Handle, args []any, kwargs map[string]any) (CallResult, error) {
	return m.callHandle(ctx, handle, "", args, kwargs)
}

func (m *Module) CallAttr(ctx context.Context, handle *Handle, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if !publicPythonAttribute(name) {
		return CallResult{}, fmt.Errorf("Python attribute %q is private", name)
	}
	return m.callHandle(ctx, handle, name, args, kwargs)
}

func (m *Module) callHandle(ctx context.Context, handle *Handle, name string, args []any, kwargs map[string]any) (CallResult, error) {
	encodedArgs, err := m.encodeValue(args)
	if err != nil {
		return CallResult{}, err
	}
	encodedKwargs, err := m.encodeValue(kwargs)
	if err != nil {
		return CallResult{}, err
	}
	return m.handleRequest(ctx, handle, map[string]any{"op": "call", "name": name, "args": encodedArgs, "kwargs": encodedKwargs})
}

func (m *Module) Release(ctx context.Context, handle *Handle) error {
	if _, ok := m.runtime.(Rust); ok {
		unlock, err := m.lock(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		_, err = m.releaseCall(ctx, []any{handle})
		return err
	}
	_, err := m.handleRequest(ctx, handle, map[string]any{"op": "release"})
	return err
}

func publicPythonAttribute(name string) bool {
	switch name {
	case "__name__", "__file__", "__package__":
		return true
	}
	return name != "" && name[0] != '_'
}

// Attr reads a module-level attribute of a direct import (or an island
// namespace entry of a source block). Private names are refused as they are
// on handles; the module identity dunders __name__, __file__ and __package__
// are readable so a file import can be checked for what it loaded.
func (m *Module) Attr(ctx context.Context, name string) (CallResult, error) {
	if !publicPythonAttribute(name) {
		return CallResult{}, fmt.Errorf("Python attribute %q is private", name)
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return CallResult{}, err
	}
	defer unlock()
	if err := m.ensure(ctx); err != nil {
		return CallResult{}, err
	}
	m.nextID++
	return m.request(ctx, map[string]any{"id": m.nextID, "op": "getattr", "name": name}, nil)
}

// CommandResult is the outcome of an island function run as a shell command
// through [Module.Command].
type CommandResult struct {
	Status         int
	Stdout, Stderr string
}

// CommandStreams sends a Go command's output directly to the caller's streams.
// Other command runtimes retain their existing captured response behavior.
func (m *Module) CommandStreams(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if _, ok := m.runtime.(Go); ok {
		unlock, err := m.lock(ctx)
		if err != nil {
			return 0, err
		}
		defer unlock()
		return m.commandGoTo(ctx, name, argv, stdin, stdout, stderr)
	}
	result, err := m.Command(ctx, name, argv, stdin)
	if result.Stdout != "" {
		if _, writeErr := io.WriteString(stdout, result.Stdout); writeErr != nil && err == nil {
			err = writeErr
		}
	}
	if result.Stderr != "" {
		if _, writeErr := io.WriteString(stderr, result.Stderr); writeErr != nil && err == nil {
			err = writeErr
		}
	}
	return result.Status, err
}

// ErrCommandNotFound reports that the named island function does not exist;
// ErrCommandNotCallable that the name exists but is not callable. Callers map
// them to the shell's 127 and 126 statuses.
var (
	ErrCommandNotFound    = errors.New("polyglot: no such island function")
	ErrCommandNotCallable = errors.New("polyglot: island target is not callable")
)

// Command runs the named island function as a command. For Go, the exported
// zero-argument function receives argv through os.Args, guest stdin and the
// caller's process context; its process exit status is the command status.
// Other runtimes pass argv words as positional strings and interpret their
// return value as the exit status.
//
// Python follows the xonsh callable-alias convention — an int is the status, a
// str is written to stdout, a (stdout, stderr, status) sequence is split, None
// reports 0; SystemExit ends the command with its code, KeyboardInterrupt with
// 130, and any other exception prints its traceback to stderr and reports 1.
//
// PowerShell runs the function as a filter: its success-stream output is
// rendered to stdout (a string as text, a byte array as raw bytes, any other
// object as compact JSON — the only boundary bytes bashy's pipes carry), its
// non-terminating error and warning streams go to stderr, and a terminating
// error ends the command with its message on stderr and status 1 without
// discarding earlier output (the command form of the same error that binds to
// err in a typed call). An exit statement ends only that invocation with its
// requested status; native exit codes propagate through $LASTEXITCODE; a clean
// run reports 0. When the analyzed function reads pipeline input and stdin is
// non-nil, the upstream byte pipe is fed to it a chunk at a time through the
// existing shell-callback channel.
//
// Output line endings are normalised to LF on every OS so the same script
// yields the same bytes. None of the per-language error conventions above are
// errors to the caller; the error result is reserved for lookup failures
// ([ErrCommandNotFound], [ErrCommandNotCallable]), cancellation, and worker
// death ([WorkerExit]). A native PowerShell exit is the exception: the pwsh
// process is the invocation boundary, so its code is the command status and the
// worker is restarted lazily for the next request.
func (m *Module) Command(ctx context.Context, name string, argv []string, stdin io.Reader) (CommandResult, error) {
	if _, ok := m.runtime.(Go); ok {
		unlock, err := m.lock(ctx)
		if err != nil {
			return CommandResult{}, err
		}
		defer unlock()
		return m.commandGo(ctx, name, argv, stdin)
	}
	if !publicPythonAttribute(name) {
		return CommandResult{}, fmt.Errorf("%w: %s is private", ErrCommandNotFound, name)
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer unlock()
	if err := m.ensure(ctx); err != nil {
		return CommandResult{}, err
	}
	m.nextID++
	request := map[string]any{"id": m.nextID, "op": "command", "name": name, "args": StringsToAny(argv)}
	_, powershell := m.runtime.(PowerShell)
	var capture *powerShellCommandCapture
	if powershell {
		capture, err = newPowerShellCommandCapture()
		if err != nil {
			return CommandResult{}, fmt.Errorf("create PowerShell command capture: %w", err)
		}
		defer capture.remove()
		capture.addRequest(request)
	}
	if powershell && stdin != nil {
		// The filter's stdin is a shell callback, never the worker's own
		// protocol stdin; it is scoped to this one request.
		request["stdin"] = m.registerCallback(FilterInput(stdin).Callback)
		defer m.dropPendingCallbacks()()
	}
	result, err := m.request(ctx, request, nil)
	out := CommandResult{Stdout: result.Stdout, Stderr: result.Stderr}
	if err != nil {
		var exit *WorkerExit
		if powershell && errors.As(err, &exit) && exit.Signal == 0 {
			captured, readErr := capture.result()
			if readErr != nil {
				return out, fmt.Errorf("read PowerShell command capture: %w", readErr)
			}
			captured.Status = exit.Code
			return captured, nil
		}
		if detail, ok := ForeignErrorDetail(err); ok {
			switch detail.Code {
			case "LookupError", "CommandNotFound":
				return out, fmt.Errorf("%w: %s", ErrCommandNotFound, name)
			case "TypeError":
				return out, fmt.Errorf("%w: %s", ErrCommandNotCallable, name)
			case "KeyboardInterrupt":
				// Interrupted between the adapter's own guards; the same
				// outcome the adapter reports from inside the call.
				out.Status = 130
				return out, nil
			}
		}
		return out, err
	}
	status := result.Value
	if reply, ok := status.(map[string]any); ok {
		// PowerShell returns {status, stdout}: stdout crosses as base64 so a
		// byte array reaches the pipe byte for byte.
		raw, ok := reply["stdout"].([]byte)
		if !ok && reply["stdout"] != nil {
			return out, fmt.Errorf("decode %s command stdout: %T is not bytes", m.runtime.name(), reply["stdout"])
		}
		out.Stdout += string(raw)
		status = reply["status"]
	}
	switch status := status.(type) {
	case int64:
		out.Status = int(status)
	case float64:
		out.Status = int(status)
	default:
		return out, fmt.Errorf("decode %s command status: %T is not a status", m.runtime.name(), status)
	}
	return out, nil
}

// WorkerExit reports that a foreign worker process ended on its own while a
// request was in flight: Signal is the number of the signal that killed it
// (0 when it exited) and Code its exit status (-1 when signaled). The shell
// reports such a command as a foreground process death, 128+Signal.
type WorkerExit struct {
	Runtime string
	Signal  int
	Code    int
	Err     error
}

func (e *WorkerExit) Error() string {
	if e.Signal != 0 {
		return fmt.Sprintf("%s worker killed by signal %d", e.Runtime, e.Signal)
	}
	return fmt.Sprintf("%s worker exited with status %d", e.Runtime, e.Code)
}

func (e *WorkerExit) Unwrap() error { return e.Err }

// ExitStatus preserves explicit process exits from called Go fence functions.
// Losing any other runtime's worker is an infrastructure failure, not the
// foreign function's result. Lowered programs use this same status contract.
func (e *WorkerExit) ExitStatus() int {
	if e.Runtime == "Go" && e.Signal == 0 && e.Code > 0 {
		return e.Code
	}
	return 1
}

func (m *Module) handleRequest(ctx context.Context, handle *Handle, request map[string]any) (CallResult, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return CallResult{}, err
	}
	defer unlock()
	if err := m.ensure(ctx); err != nil {
		return CallResult{}, err
	}
	if handle == nil || handle.module != m || handle.generation != m.generation {
		return CallResult{}, fmt.Errorf("polyglot: stale or foreign %s handle", m.runtime.name())
	}
	m.nextID++
	request["id"], request["handle"] = m.nextID, handle.ID
	return m.request(ctx, request, request["name"])
}

func (m *Module) request(ctx context.Context, request map[string]any, annotation any) (CallResult, error) {
	id, _ := request["id"].(uint64)
	var cwd func() string
	if py, ok := m.runtime.(Python); ok {
		cwd = py.Cwd
	} else if ps, ok := m.runtime.(PowerShell); ok {
		cwd = ps.Cwd
	} else if cs, ok := m.runtime.(CSharp); ok {
		cwd = cs.Cwd
	}
	if dir, ok := ctx.Value(callerDirKey{}).(string); ok && dir != "" {
		cwd = func() string { return dir }
	}
	if cwd != nil {
		if op, _ := request["op"].(string); op != "job_join" && op != "job_leave" {
			if dir := cwd(); dir != "" {
				request["cwd"] = dir
			}
		}
	}
	var response workerResponse
	if err := m.exchangeContext(ctx, request, &response); err != nil {
		// A PowerShell exit statement closes the worker's protocol pipe. Reap
		// that process without signalling it so Command can distinguish the
		// native status (including status 1 on Windows) from our own cleanup.
		// Other runtimes and PowerShell transport failures keep the ordinary
		// kill-and-report lifecycle below.
		op, _ := request["op"].(string)
		_, powershell := m.runtime.(PowerShell)
		if powershell && op == "command" && errors.Is(err, io.EOF) {
			exit, reapErr := m.reapPowerShellWorker(ctx)
			if reapErr != nil {
				return CallResult{}, reapErr
			}
			if exit.died {
				return CallResult{}, &WorkerExit{Runtime: m.runtime.name(), Signal: exit.signal, Code: exit.code, Err: err}
			}
		}
		m.kill()
		m.procMu.Lock()
		exit := m.lastExit
		m.procMu.Unlock()
		if ctx.Err() == nil && exit.died {
			return CallResult{}, &WorkerExit{Runtime: m.runtime.name(), Signal: exit.signal, Code: exit.code, Err: err}
		}
		return CallResult{}, err
	}
	if response.ID != id {
		m.kill()
		return CallResult{}, fmt.Errorf("%s worker response ID %d does not match request ID %d", m.runtime.name(), response.ID, id)
	}
	result := CallResult{Stdout: m.pendingOut + response.Stdout, Stderr: m.pendingErr + response.Stderr}
	m.pendingOut, m.pendingErr = "", ""
	if !response.OK {
		return result, response.Error.err()
	}
	decoded, decodeErr := m.decodeValue(response.Result)
	if decodeErr != nil {
		return result, fmt.Errorf("decode %s result: %w", m.runtime.name(), decodeErr)
	}
	result.Value = decoded
	name, _ := annotation.(string)
	for _, export := range m.plan.Exports {
		if export.Name != name || export.Signature.Dynamic {
			continue
		}
		want := "nil"
		if len(export.Signature.Results) > 0 {
			want = export.Signature.Results[0]
		}
		coerced, coerceErr := coerceResult(result.Value, want)
		if coerceErr != nil {
			return result, fmt.Errorf("%s function %s violated its %s result annotation: %w", m.runtime.name(), name, want, coerceErr)
		}
		result.Value = coerced
		break
	}
	return result, nil
}

// reapPowerShellWorker waits only after the command protocol pipe reached EOF,
// which is the supported pwsh process boundary for PowerShell's exit keyword.
// The process is detached under procMu before waiting so a worker that closes
// the protocol stream without exiting cannot pin the process mutex. If the
// request is cancelled while it is still alive, the reap kills and joins it;
// otherwise no signal is sent and its native status is the command status.
func (m *Module) reapPowerShellWorker(ctx context.Context) (workerExit, error) {
	m.procMu.Lock()
	if m.cmd == nil {
		m.procMu.Unlock()
		return workerExit{}, nil
	}
	cmd, in, outFile := m.cmd, m.in, m.outFile
	m.cmd, m.in, m.out, m.outFile = nil, nil, nil, nil
	m.procMu.Unlock()

	_ = in.Close()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	cancelled := false
	select {
	case <-done:
	case <-ctx.Done():
		cancelled = true
		_ = cmd.Process.Kill()
		<-done
	}
	if outFile != nil {
		_ = outFile.Close()
	}
	if cancelled {
		m.procMu.Lock()
		m.lastExit = workerExit{}
		m.procMu.Unlock()
		return workerExit{}, ctx.Err()
	}
	exit := workerExit{}
	if state := cmd.ProcessState; state != nil {
		exit = naturalWorkerDeath(state)
	}
	m.procMu.Lock()
	m.lastExit = exit
	m.procMu.Unlock()
	return exit, nil
}

func (m *Module) exchangeContext(ctx context.Context, request any, response *workerResponse) error {
	done := make(chan error, 1)
	m.procMu.Lock()
	in, out := m.in, m.out
	m.procMu.Unlock()
	if in == nil || out == nil {
		return errors.New("foreign worker is not running")
	}
	// On Windows the TypeScript, Rust, PowerShell and C# workers share stdout with
	// island output (there is no fd 3 on Windows), so only marker-framed lines
	// are protocol.
	_, typeScript := m.runtime.(TypeScript)
	_, rust := m.runtime.(Rust)
	_, powershell := m.runtime.(PowerShell)
	_, csharp := m.runtime.(CSharp)
	requireMarker := runtime.GOOS == "windows" && (typeScript || rust || powershell || csharp)
	// The exchange goroutine also serves the shell callbacks the worker
	// nests inside this request; a callback that calls the module again
	// re-enters on this goroutine while the caller stays parked here, so
	// the protocol stream is used strictly last-in-first-out.
	go func() {
		done <- exchange(in, out, request, response, requireMarker, func(callback callbackRequest) map[string]any {
			return m.serveCallback(ctx, callback)
		})
	}()
	select {
	case <-ctx.Done():
		// The worker dies now; a callback still running sees ctx done and
		// its reply fails against the closed pipe, which ends the exchange.
		_ = m.killWorker()
		<-done
		m.pendingOut, m.pendingErr = "", ""
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// invokeCallback runs the callback, turning a panic into its error: the
// exchange goroutine must survive to answer the worker.
func invokeCallback(ctx context.Context, callback Callback, args []any) (result any, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			result, err = nil, fmt.Errorf("polyglot: shell callback %s panicked: %v", callback.Name, failure)
		}
	}()
	return callback.Invoke(ctx, args)
}

// callbackRequest is the worker's target-invocation request in reverse:
// `{"call":"shell","op":"call","callback":id,"args":[…]}`, with the island
// output produced before it.
type callbackRequest struct {
	ID             uint64 `json:"id"`
	Call           string `json:"call"`
	Callback       uint64 `json:"callback"`
	Args           []any  `json:"args"`
	Stdout, Stderr string
}

// serveCallback runs one shell callback and builds its reply. Island output
// preceding it is delivered first so the shell sees it in order.
func (m *Module) serveCallback(ctx context.Context, req callbackRequest) map[string]any {
	callbackName := fmt.Sprintf("%d", req.Callback)
	reply := func(err error) map[string]any {
		detail := map[string]any{"code": "SHELL-ECALLBACK", "callback": callbackName, "message": err.Error()}
		var failure interface{ CallbackStatus() int }
		if errors.As(err, &failure) && failure.CallbackStatus() != 0 {
			detail["status"] = failure.CallbackStatus()
		}
		return map[string]any{"id": req.ID, "ok": false, "error": detail}
	}
	if m.callbacks.Output != nil {
		if out, errText := m.pendingOut+req.Stdout, m.pendingErr+req.Stderr; out != "" || errText != "" {
			m.callbacks.Output(out, errText)
		}
		m.pendingOut, m.pendingErr = "", ""
	} else {
		m.pendingOut += req.Stdout
		m.pendingErr += req.Stderr
	}
	callback, ok := m.callbackTable[req.Callback]
	if !ok {
		return reply(fmt.Errorf("polyglot: shell callback %d expired: the call that passed it has completed", req.Callback))
	}
	callbackName = callback.Name
	if m.callbackDepth >= MaxCallbackDepth {
		return reply(fmt.Errorf("polyglot: shell callback %s re-entry exceeds the bound of %d", callback.Name, MaxCallbackDepth))
	}
	args := make([]any, len(req.Args))
	for i, arg := range req.Args {
		decoded, err := m.decodeValue(arg)
		if err != nil {
			return reply(fmt.Errorf("polyglot: shell callback %s argument %d: %w", callback.Name, i+1, err))
		}
		args[i] = decoded
	}
	if callback.Invoke == nil {
		return reply(fmt.Errorf("polyglot: shell callback %s has no implementation", callback.Name))
	}
	var result any
	var err error
	m.callbackDepth++
	nested := context.WithValue(ctx, reentrantKey{}, m)
	if m.callbacks.Enter != nil {
		exit := m.callbacks.Enter(nested)
		result, err = invokeCallback(nested, callback, args)
		exit()
	} else {
		result, err = invokeCallback(nested, callback, args)
	}
	m.callbackDepth--
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return reply(err)
	}
	encoded, err := m.encodeValue(result)
	if err != nil {
		return reply(err)
	}
	return map[string]any{"id": req.ID, "ok": true, "result": encoded}
}

func coerceResult(value any, want string) (any, error) {
	switch want {
	case "any":
		return value, nil
	case "object":
		return jsonObjectValue(value)
	case "nil":
		if value == nil {
			return nil, nil
		}
	case "int":
		if _, ok := value.(int64); ok {
			return value, nil
		}
	case "float64":
		switch value := value.(type) {
		case float64:
			return value, nil
		case int64:
			return float64(value), nil
		}
	case "string":
		if _, ok := value.(string); ok {
			return value, nil
		}
	case "bool":
		if _, ok := value.(bool); ok {
			return value, nil
		}
	case "bytes":
		if _, ok := value.([]byte); ok {
			return value, nil
		}
	case "handle":
		if _, ok := value.(*Handle); ok {
			return value, nil
		}
	}
	return nil, fmt.Errorf("got %T", value)
}

func jsonObjectValue(value any) (any, error) {
	switch value := value.(type) {
	case nil, bool, string, int64, float64, *Handle:
		return value, nil
	case []any:
		out := make([]any, len(value))
		for i := range value {
			var err error
			out[i], err = jsonObjectValue(value[i])
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			var err error
			out[key], err = jsonObjectValue(item)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("got non-JSON %T", value)
	}
}

func (m *Module) Close() error {
	unlock, _ := m.lock(context.Background())
	defer unlock()
	err := m.kill()
	if embedded, ok := m.runtime.(Embedded); ok && embedded.CloseFunc != nil {
		err = errors.Join(err, embedded.CloseFunc(m.plan))
	}
	return err
}

func (m *Module) kill() error {
	err := m.killWorker()
	m.pendingOut, m.pendingErr = "", ""
	return err
}

// killWorker may run alongside a cancelled exchange. It touches only process
// state protected by procMu, not the callback/output state owned by that
// exchange. Its caller must join the exchange before clearing pending output.
func (m *Module) killWorker() error {
	m.procMu.Lock()
	defer m.procMu.Unlock()
	if m.cmd == nil {
		if m.tempDir != "" {
			_ = os.RemoveAll(m.tempDir)
			m.tempDir = ""
		}
		return nil
	}
	_ = m.in.Close()
	err := m.cmd.Process.Kill()
	_ = m.cmd.Wait()
	m.lastExit = workerExit{}
	if state := m.cmd.ProcessState; state != nil {
		// A worker that ended before kill reached it — killed by another
		// signal or exited — is its own death, not ours.
		m.lastExit = workerDeath(state)
	}
	if m.outFile != nil {
		_ = m.outFile.Close()
	}
	m.cmd, m.in, m.out, m.outFile = nil, nil, nil, nil
	if m.tempDir != "" {
		_ = os.RemoveAll(m.tempDir)
		m.tempDir = ""
	}
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

type workerResponse struct {
	ID             uint64    `json:"id"`
	OK             bool      `json:"ok"`
	Result         any       `json:"result"`
	Error          workerErr `json:"error"`
	Stdout, Stderr string
}

type workerErr struct {
	ErrorDetail
	set bool
}

func (e *workerErr) UnmarshalJSON(data []byte) error {
	e.set = !bytes.Equal(bytes.TrimSpace(data), []byte("null"))
	return e.ErrorDetail.UnmarshalJSON(data)
}

func (e workerErr) err() error {
	if !e.set {
		return errors.New("foreign worker error")
	}
	return e.ErrorDetail.err()
}

func exchange(in io.Writer, out *bufio.Reader, request any, response *workerResponse, requireMarker bool, serve func(callbackRequest) map[string]any) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := in.Write(append(data, '\n')); err != nil {
		return err
	}
	var got workerResponse
	for {
		line, err := out.ReadBytes('\n')
		if err != nil {
			return err
		}
		if marker := bytes.Index(line, []byte("\x1eBASHPP")); marker >= 0 {
			line = line[marker+len("\x1eBASHPP"):]
		} else if requireMarker || len(bytes.TrimSpace(line)) == 0 || bytes.TrimSpace(line)[0] != '{' {
			continue
		}
		var probe struct {
			Call string `json:"call"`
		}
		if json.Unmarshal(line, &probe) == nil && probe.Call == "shell" {
			// The worker asks for a shell function while this request is in
			// flight; the reply goes back on stdin and the wait continues.
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.UseNumber()
			var callback callbackRequest
			if err := dec.Decode(&callback); err != nil {
				return fmt.Errorf("invalid worker callback request: %w", err)
			}
			var reply map[string]any
			if serve == nil {
				reply = map[string]any{"id": callback.ID, "ok": false, "error": map[string]any{"code": "SHELL-ECALLBACK", "message": "polyglot: shell callbacks are not served for this module"}}
			} else {
				reply = serve(callback)
			}
			data, err := json.Marshal(reply)
			if err != nil {
				return err
			}
			if _, err := in.Write(append(data, '\n')); err != nil {
				return err
			}
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&got); err != nil {
			return fmt.Errorf("invalid worker response: %w", err)
		}
		break
	}
	if response != nil {
		*response = got
	}
	return nil
}

func (m *Module) encodeValue(v any) (any, error) {
	switch x := v.(type) {
	case *Handle:
		if x == nil || x.module != m {
			return nil, fmt.Errorf("polyglot: stale or foreign %s handle", m.runtime.name())
		}
		if x.generation != m.generation {
			return nil, fmt.Errorf("polyglot: stale %s handle: its worker is gone", m.runtime.name())
		}
		return map[string]any{"$handle": x.ID}, nil
	case Callback:
		return map[string]any{"$callback": m.registerCallback(x)}, nil
	case *Callback:
		if x == nil {
			return nil, nil
		}
		return map[string]any{"$callback": m.registerCallback(*x)}, nil
	case PipelineInput:
		return map[string]any{"$pipeline_input": m.registerCallback(x.Callback)}, nil
	case *PipelineInput:
		if x == nil {
			return nil, nil
		}
		return map[string]any{"$pipeline_input": m.registerCallback(x.Callback)}, nil
	case []byte:
		return map[string]any{"$bytes": base64.StdEncoding.EncodeToString(x)}, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			var err error
			out[i], err = m.encodeValue(x[i])
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			var err error
			out[k], err = m.encodeValue(v)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		if v != nil && reflect.TypeOf(v).Kind() == reflect.Func {
			return map[string]any{"$callback": m.registerCallback(FuncCallback(reflect.TypeOf(v).String(), v))}, nil
		}
		return v, nil
	}
}

func (m *Module) decodeValue(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if raw, ok := x["$handle"].(map[string]any); ok {
			id, err := numericID(raw["id"])
			if err != nil {
				return nil, err
			}
			h := &Handle{module: m, ID: id, generation: m.generation}
			h.Type, _ = raw["type"].(string)
			h.Repr, _ = raw["repr"].(string)
			h.Callable, _ = raw["callable"].(bool)
			return h, nil
		}
		if raw, ok := x["$handle"]; ok && len(x) == 1 {
			// The host's own encoding, echoed back untyped: a handle a
			// callback returned through a worker value.
			id, err := numericID(raw)
			if err != nil {
				return nil, err
			}
			return &Handle{module: m, ID: id, generation: m.generation}, nil
		}
		if raw, ok := x["$bytes"].(string); ok {
			return base64.StdEncoding.DecodeString(raw)
		}
		out := map[string]any{}
		for k, v := range x {
			var err error
			out[k], err = m.decodeValue(v)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			var err error
			out[i], err = m.decodeValue(x[i])
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		return x.Float64()
	default:
		return v, nil
	}
}

func numericID(v any) (uint64, error) {
	switch n := v.(type) {
	case json.Number:
		return strconv.ParseUint(string(n), 10, 64)
	case float64:
		return uint64(n), nil
	default:
		return 0, fmt.Errorf("invalid handle id %v", v)
	}
}

const pythonPathBootstrap = `
import sys
_pythonpath=sys.argv[1:]
del sys.argv[1:]
sys.path[:0]=_pythonpath
`

const pythonAnalyze = pythonPathBootstrap + `
import ast, json
src=sys.stdin.read()
tree=ast.parse(src)
nodes=tree.body
if nodes and isinstance(nodes[0], ast.Expr) and isinstance(nodes[0].value, ast.Constant) and isinstance(nodes[0].value.value, str): nodes=nodes[1:]
types={'int':'int','float':'float64','str':'string','bool':'bool','bytes':'bytes','None':'nil','Any':'any','TextIO':'textio'}
callable_names={'Callable'}
retained_names={'RetainedCallback'}
for imported in nodes:
    if isinstance(imported,ast.ImportFrom) and imported.module in ('typing','collections.abc'):
        for name in imported.names:
            if name.name=='Callable': callable_names.add(name.asname or name.name)
            if name.name=='RetainedCallback': retained_names.add(name.asname or name.name)
def annotation_name(node):
    if isinstance(node,ast.Name): return node.id
    if isinstance(node,ast.Attribute):
        base=annotation_name(node.value)
        return (base+'.' if base else '')+node.attr
    return ''
def annotation_type(node):
    name=annotation_name(node)
    if name in retained_names or name.endswith('.RetainedCallback'): raise SyntaxError('retained callbacks are not supported')
    if isinstance(node,ast.Name): return types.get(node.id)
    if isinstance(node,ast.Constant) and node.value is None: return 'nil'
    if not isinstance(node,ast.Subscript): return None
    origin=annotation_name(node.value)
    if origin in retained_names or origin.endswith('.RetainedCallback'): raise SyntaxError('retained callbacks are not supported')
    if origin in callable_names or origin in ('typing.Callable','collections.abc.Callable'): return 'callback'
    origin=origin.rsplit('.',1)[-1]
    if origin in ('Iterator','Iterable','Generator'):
        item=node.slice.elts[0] if isinstance(node.slice,ast.Tuple) else node.slice
        typ=annotation_type(item)
        return 'iterator:'+typ if typ is not None else None
    if origin in ('list','List'):
        return 'object' if annotation_type(node.slice) is not None else None
    if origin in ('dict','Dict'):
        parts=node.slice.elts if isinstance(node.slice,ast.Tuple) else []
        if len(parts)!=2 or annotation_type(parts[0])!='string' or annotation_type(parts[1]) is None: return None
        return 'object'
    return None
out=[]
def literal_constant(node):
    # Module-level data a function can rely on: a plain name (optionally
    # annotated) bound to a literal. Anything computed would run code at load.
    if isinstance(node,ast.Assign):
        targets,value=node.targets,node.value
    elif isinstance(node,ast.AnnAssign) and node.value is not None:
        targets,value=[node.target],node.value
    else: return False
    if not all(isinstance(t,ast.Name) for t in targets): return False
    try: ast.literal_eval(value)
    except Exception: raise SyntaxError('module-level name '+', '.join(t.id for t in targets)+' must be bound to a literal constant')
    return True
for node in nodes:
    if isinstance(node,(ast.Import,ast.ImportFrom)): continue
    if literal_constant(node): continue
    if not isinstance(node, ast.FunctionDef): raise SyntaxError('only module docstrings, imports, literal constants and synchronous function declarations are allowed')
    if node.decorator_list: raise SyntaxError('decorated functions are not allowed')
    if node.args.posonlyargs or node.args.kwonlyargs or node.args.vararg or node.args.kwarg: dynamic=True
    else: dynamic=False
    for default in list(node.args.defaults)+list(node.args.kw_defaults):
        if default is not None:
            try: ast.literal_eval(default)
            except Exception: raise SyntaxError('function defaults must be literals')
    params=[]
    for arg in list(node.args.args):
        typ=annotation_type(arg.annotation)
        if typ is None: dynamic=True
        params.append(typ or 'any')
    ret=annotation_type(node.returns)
    if ret is None: dynamic=True
    iterator=ret.removeprefix('iterator:') if ret and ret.startswith('iterator:') else ''
    if not node.name.startswith('_'): out.append({'name':node.name,'signature':{'params':params,'results':[] if ret=='nil' else ['any' if iterator else ret or 'any'],'dynamic':dynamic,'iterator':iterator,'filter':bool(iterator and params and params[0]=='textio')}})
print(json.dumps(out,separators=(',',':')))
`

const pythonWorker = pythonPathBootstrap + `
import __future__, ast, base64, codecs, importlib, importlib.util, importlib.machinery, io, json, os, signal, sys, tempfile, threading, traceback, types
try:
    protocol=os.fdopen(3,'w',buffering=1,newline='\n')
except OSError:
    # No fd 3 (Windows): retain the original stdout pipe independently of fd 1.
    # capture() redirects fd 1, including while TextIO sends callback frames.
    protocol=os.fdopen(os.dup(1),'w',buffering=1,newline='\n')
# Shell text output uses LF on every host. Reconfigure the text wrappers,
# never normalize captured bytes: explicit os.write/buffer writes stay exact.
sys.stdout.reconfigure(newline='\n')
sys.stderr.reconfigure(newline='\n')
ns={'__name__':'__bashpp__'}
module=None
handles={}
iterators={}
next_iterator=0
next_handle=0
busy=False
worker_thread=threading.get_ident()
next_callback=0
callback_depth=0
capture_stack=[]
class BashCallbackError(RuntimeError):
    def __init__(self,name,message,status=None):
        super().__init__(message)
        self.callback=name
        self.status=status
bashpp=types.ModuleType('bashpp')
bashpp.BashCallbackError=BashCallbackError
sys.modules['bashpp']=bashpp
ns['BashCallbackError']=BashCallbackError
def on_sigint(signum, frame):
    # A terminal Ctrl-C reaches the worker as a member of the foreground
    # process group. While a call runs it is the call that is interrupted
    # (KeyboardInterrupt, reported as status 130 by the command adapter);
    # an idle worker is a child waiting on its parent and must not die with
    # its module state, so the signal is dropped between requests.
    if busy: raise KeyboardInterrupt
signal.signal(signal.SIGINT, on_sigint)
file_modules={}
def import_file(path):
    # CPython's documented recipe for importing a source file directly:
    # spec_from_file_location + module_from_spec, registered in sys.modules
    # before exec_module so dataclasses/pickle inside the file find it, and
    # unregistered again when execution fails (importlib does the same for a
    # module that fails to import). The module's __name__ is the file stem
    # and __file__ the absolute path, exactly as SourceFileLoader reports.
    path=os.path.abspath(path)
    key=os.path.normcase(path)
    if key in file_modules: return file_modules[key]
    name=os.path.splitext(os.path.basename(path))[0]
    # The path resolver accepts case-insensitive .py on Windows, while
    # importlib's suffix inference is case-sensitive. Select the source loader
    # explicitly so the already-selected file keeps its original spelling.
    loader=importlib.machinery.SourceFileLoader(name,path) if path.lower().endswith('.py') else None
    spec=importlib.util.spec_from_file_location(name, path, loader=loader)
    if spec is None or spec.loader is None: raise ImportError('cannot load Python source '+path, path=path)
    mod=importlib.util.module_from_spec(spec)
    previous=sys.modules.get(name)
    sys.modules[name]=mod
    try: spec.loader.exec_module(mod)
    except BaseException:
        if previous is None: sys.modules.pop(name, None)
        else: sys.modules[name]=previous
        raise
    file_modules[key]=mod
    return mod
def dec(v):
    if isinstance(v,dict) and set(v)=={'$bytes'}: return base64.b64decode(v['$bytes'])
    if isinstance(v,dict) and set(v)=={'$callback'}: return CallableProxy(v['$callback'])
    if isinstance(v,dict) and set(v)=={'$pipeline_input'}: return PipelineInput(v['$pipeline_input'])
    if isinstance(v,dict) and set(v)=={'$handle'}:
        key=int(v['$handle'])
        if key not in handles: raise ValueError('stale Python handle')
        return handles[key]
    if isinstance(v,list): return [dec(x) for x in v]
    if isinstance(v,dict): return {k:dec(x) for k,x in v.items()}
    return v
def protocol_write(value):
    protocol.write(json.dumps(value,separators=(',',':'))+'\n')
    protocol.flush()
def flush_capture():
    sys.stdout.flush(); sys.stderr.flush()
    if not capture_stack: return '', ''
    state=capture_stack[-1]
    state['out'].seek(state['out_pos']); out=state['out'].read(); state['out_pos']+=len(out); state['out'].seek(0,2)
    state['err'].seek(state['err_pos']); err=state['err'].read(); state['err_pos']+=len(err); state['err'].seek(0,2)
    return out.decode(errors='replace'),err.decode(errors='replace')
class CallableProxy:
    def __init__(self,callback): self.callback=int(callback); self.thread=threading.get_ident()
    def __call__(self,*args):
        global next_callback,callback_depth,busy
        if threading.get_ident()!=self.thread or threading.get_ident()!=worker_thread:
            raise BashCallbackError(str(self.callback),'shell callback must run on its calling thread')
        if callback_depth>=8:
            raise BashCallbackError(str(self.callback),'shell callback re-entry exceeds the bound of 8')
        callback_depth+=1
        try:
            next_callback+=1; rid=next_callback
            out,err=flush_capture()
            protocol_write({'id':rid,'call':'shell','op':'call','callback':self.callback,'args':enc(list(args)),'stdout':out,'stderr':err})
            while True:
                line=sys.stdin.readline()
                if not line: raise BashCallbackError(str(self.callback),'host closed the callback protocol')
                reply=json.loads(line)
                if 'op' in reply:
                    protocol_write(dispatch(reply))
                    busy=True
                    continue
                if int(reply.get('id',-1))!=rid:
                    raise BashCallbackError(str(self.callback),'shell callback reply id does not match request id')
                if reply.get('ok'): return dec(reply.get('result'))
                detail=reply.get('error') or {}
                if isinstance(detail,str): detail={'message':detail}
                raise BashCallbackError(detail.get('callback',str(self.callback)),detail.get('message','shell callback failed'),detail.get('status'))
        finally: callback_depth-=1
class PipelineInput(io.TextIOBase):
    def __init__(self, callback):
        self.callback=callback
        self.buffer=''
        self.eof=False
        self.decoder=codecs.getincrementaldecoder('utf-8')()
    def readable(self): return True
    def _fill(self):
        protocol_write({'id':0,'call':'shell','callback':self.callback,'args':[65536]})
        reply=json.loads(sys.stdin.readline())
        if not reply.get('ok'): raise OSError(reply.get('error',{}).get('message','pipeline read failed'))
        data=dec(reply['result'])
        if not data: self.eof=True
        self.buffer+=self.decoder.decode(data,final=self.eof)
    def read(self,size=-1):
        while not self.eof and (size<0 or len(self.buffer)<size): self._fill()
        if size<0: size=len(self.buffer)
        result,self.buffer=self.buffer[:size],self.buffer[size:]
        return result
    def readline(self,size=-1):
        while '\n' not in self.buffer and not self.eof and (size<0 or len(self.buffer)<size): self._fill()
        end=self.buffer.find('\n')+1
        if end==0: end=len(self.buffer)
        if size>=0: end=min(end,size)
        result,self.buffer=self.buffer[:end],self.buffer[end:]
        return result
def enc(v):
    global next_handle
    if isinstance(v,bytes): return {'$bytes':base64.b64encode(v).decode('ascii')}
    if isinstance(v,(list,tuple)): return [enc(x) for x in v]
    if isinstance(v,dict) and all(isinstance(k,str) for k in v): return {k:enc(x) for k,x in v.items()}
    if v is None or isinstance(v,(bool,int,float,str)): return v
    next_handle+=1; handles[next_handle]=v
    try: shown=repr(v)
    except Exception: shown='<unrepresentable>'
    return {'$handle':{'id':next_handle,'type':type(v).__name__,'repr':shown,'callable':callable(v)}}
def attr(target,name):
    if name.startswith('_') and name not in ('__name__','__file__','__package__'): raise AttributeError('private Python attribute: '+name)
    return getattr(target,name)
def lookup(target,name):
    # A module attribute or an island-namespace entry, by the same rule.
    if target is ns: return target[name]
    return attr(target,name)
def endswith_newline(text):
    return text if text.endswith('\n') else text+'\n'
def command_status(r):
    # xonsh parse_proxy_return: str -> stdout, int -> status, sequence ->
    # (stdout, stderr, status), None -> nothing, anything else -> str(r).
    status=0
    if isinstance(r,str):
        sys.stdout.write(r)
    elif isinstance(r,int):
        status=int(r)
    elif isinstance(r,(list,tuple)):
        if len(r)>0 and r[0] is not None: sys.stdout.write(str(r[0]))
        if len(r)>1 and r[1] is not None: sys.stderr.write(endswith_newline(str(r[1])))
        if len(r)>2 and isinstance(r[2],int): status=int(r[2])
    elif r is not None:
        sys.stdout.write(str(r))
    sys.stdout.flush(); sys.stderr.flush()
    return status
def run_command(fn,argv):
    try:
        return command_status(fn(*argv))
    except SystemExit as exc:
        # As CPython's interpreter exit: an int is the status, None is 0,
        # anything else is printed to stderr and exits 1.
        if exc.code is None: return 0
        if isinstance(exc.code,int): return exc.code
        sys.stderr.write(endswith_newline(str(exc.code))); sys.stderr.flush()
        return 1
    except KeyboardInterrupt:
        return 130
    except Exception:
        traceback.print_exc(); sys.stderr.flush()
        return 1
def envelope_error(exc, trace=None, seen=None, depth=0):
    if trace is None: trace=''.join(traceback.format_exception(type(exc),exc,exc.__traceback__))
    if seen is None: seen=set()
    if id(exc) in seen:
        return {'code':type(exc).__name__,'message':'cycle in Python exception cause chain'}
    if depth>=16:
        return {'code':type(exc).__name__,'message':'Python exception cause chain truncated'}
    seen.add(id(exc))
    message=str(exc)
    cause=getattr(exc,'__cause__',None) or (getattr(exc,'__context__',None) if not getattr(exc,'__suppress_context__',False) else None)
    out={'code':type(exc).__name__,'message':message or type(exc).__name__,'help':trace}
    if cause is not None: out['cause']=envelope_error(cause, None, seen, depth+1)
    seen.remove(id(exc))
    return out
class CaptureFailure(Exception):
    def __init__(self,error,out,err): self.error,self.out,self.err=error,out,err
def capture(operation):
    sys.stdout.flush(); sys.stderr.flush()
    oldout,olderr=os.dup(1),os.dup(2)
    with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
        state={'out':out,'err':err,'out_pos':0,'err_pos':0}
        capture_stack.append(state)
        failure=None
        try:
            os.dup2(out.fileno(),1); os.dup2(err.fileno(),2)
            value=operation()
        except BaseException as exc:
            failure=envelope_error(exc)
        finally:
            sys.stdout.flush(); sys.stderr.flush()
            os.dup2(oldout,1); os.dup2(olderr,2); os.close(oldout); os.close(olderr)
            capture_stack.pop()
        out.seek(state['out_pos']); err.seek(state['err_pos'])
        captured_out,captured_err=out.read().decode(errors='replace'),err.read().decode(errors='replace')
        if failure is not None: raise CaptureFailure(failure,captured_out,captured_err)
        return value,captured_out,captured_err
def dispatch(req):
    global busy,module,next_iterator
    rid=req.get('id',0); captured_out=captured_err=''
    busy=True
    try:
        op=req['op']
        if req.get('cwd'): os.chdir(req['cwd'])
        if op=='job_join' or op=='job_leave':
            if hasattr(os,'setpgid'):
                os.setpgid(0,int(req.get('pgid',0)))
                value=os.getpgrp()
            else: value=os.getpid()
        elif op=='load':
            def action():
                exec(compile(req['source'],'<bash++ python>','exec',flags=__future__.annotations.compiler_flag,dont_inherit=True),ns,ns)
            value,captured_out,captured_err=capture(action)
        elif op=='import':
            def action():
                global module
                module=import_file(req['path']) if req.get('path') else importlib.import_module(req['module'])
            value,captured_out,captured_err=capture(action)
        elif op=='getattr':
            if 'handle' in req: value,captured_out,captured_err=capture(lambda:attr(handles[int(req['handle'])],req['name']))
            else: value,captured_out,captured_err=capture(lambda:lookup(module if module is not None else ns,req['name']))
        elif op=='command':
            # The command adapter: argv words are positional strings, the
            # result is a status, output is whatever the function wrote.
            # Lookup failures are reported before the call so the shell can
            # spell them as command-not-found rather than a Python failure.
            target=module if module is not None else ns
            try: fn=lookup(target,req['name'])
            except (AttributeError,KeyError): raise LookupError('no Python function '+req['name'])
            if not callable(fn): raise TypeError('Python target is not callable: '+req['name'])
            value,captured_out,captured_err=capture(lambda:run_command(fn,[str(a) for a in req.get('args',[])]))
        elif op=='call':
            def action():
                target=handles[int(req['handle'])] if 'handle' in req else module if module is not None else ns
                if req.get('name'): target=attr(target,req['name']) if module is not None or 'handle' in req else target[req['name']]
                if not callable(target): raise TypeError('Python target is not callable')
                return target(*dec(req.get('args',[])),**dec(req.get('kwargs',{})))
            value,captured_out,captured_err=capture(action)
        elif op=='iter_open':
            def action():
                global next_iterator
                if 'handle' in req:
                    key=int(req['handle'])
                    if key not in handles: raise ValueError('stale Python handle')
                    iterator=iter(handles[key])
                else:
                    target=module if module is not None else ns
                    iterator=iter(lookup(target,req['name'])(*dec(req.get('args',[]))))
                next_iterator+=1
                iterators[next_iterator]=iterator
                return next_iterator
            value,captured_out,captured_err=capture(action)
        elif op=='iter_next':
            def action():
                key=int(req['iterator'])
                if key not in iterators: raise ValueError('stale Python iterator')
                try: return {'done':False,'value':next(iterators[key])}
                except StopIteration:
                    iterators.pop(key)
                    return {'done':True}
            value,captured_out,captured_err=capture(action)
        elif op=='iter_close':
            def action():
                iterator=iterators.pop(int(req['iterator']),None)
                close=getattr(iterator,'close',None)
                if close is not None: close()
            value,captured_out,captured_err=capture(action)
        elif op=='release':
            handles.pop(int(req['handle']),None); value=None
        else: raise ValueError('unknown operation')
        res={'id':rid,'ok':True,'result':enc(value),'stdout':captured_out,'stderr':captured_err}
    except CaptureFailure as failure:
        res={'id':rid,'ok':False,'error':failure.error,'stdout':failure.out,'stderr':failure.err}
    except Exception as exc:
        res={'id':rid,'ok':False,'error':envelope_error(exc),'stdout':captured_out,'stderr':captured_err}
    finally:
        busy=False
    return res
for line in sys.stdin:
    protocol_write(dispatch(json.loads(line)))
`
