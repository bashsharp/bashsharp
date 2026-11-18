package polyglot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qiangli/bash/pathconv"
)

// A text fence carries a declarative artifact — a Dockerfile, an OpenTofu
// module, a manifest, a task file — instead of source. Its alias exposes the
// verbs of the processor that reads that artifact, and those verbs are
// DECLARED, never inferred: a built-in row declares them in its table, a
// runner override declares them by answering `methods`. The body is
// materialized under the cache directory keyed by its content, never into
// the checkout, and is embedded verbatim in lowered output.

// Verb is one method of a text fence row.
type Verb struct {
	// Name is the method (`plan`, `apply`, `build`).
	Name string
	// Args are the processor's arguments for this verb. `{file}` is the
	// materialized artifact, `{dir}` its directory, `{root}` the fence's
	// materialization root and `{cwd}` the caller's directory; the call's
	// own string arguments follow.
	Args []string
	// Tool, when set, is this verb's processor instead of the row's (a local
	// `podman kube play` beside a row whose tool is kubectl).
	Tool string
	// Env are extra `KEY=value` entries for the processor, with the same
	// placeholders as Args.
	Env []string
	// Effects are the effect atoms a world-changing verb carries, read by
	// the contract layer; nil for a read-only verb.
	Effects []string
	// Agentic requires an explicitly agentic caller.
	Agentic bool
	// Result is the export's result type; "" means one string (the
	// processor's stdout).
	Result string
}

// Text is the runtime of a built-in text fence row: the artifact's file
// name, the processor tool and the verb table. The tool resolves through
// [ToolResolver] like every fence tool, never from PATH when a resolver is
// installed.
type Text struct {
	Type     string
	FileName string
	Tool     string
	Verbs    []Verb
	// WorkDir is where the processor runs: "" for the artifact's directory
	// (a self-contained module), "{cwd}" for the caller's directory (a task
	// file whose bodies address the checkout, a chart path beside a values
	// file).
	WorkDir string
	// Shadow names entries of the caller's directory that are linked into
	// the artifact's directory before a verb runs, for a processor that
	// insists the manifest sit beside the sources (cargo: src, tests, …).
	// An entry absent from the caller's directory is skipped; where a link
	// cannot be made the entry is copied. Nothing is written into the
	// caller's directory.
	Shadow []string
	// Overlay names sibling files that the `{overlay}` placeholder maps
	// beside the manifest (go: go.sum): the generated overlay JSON tells a
	// tool that `{cwd}/<FileName>` and each `{cwd}/<sibling>` are the fenced
	// files under the artifact's directory, without a byte in the caller's
	// tree; a sibling absent there is created empty.
	Overlay []string
	Dir     string
	// CwdFunc, when set, answers the caller's directory at call time and
	// wins over Dir; it is a host binding, never part of a lowered literal.
	CwdFunc func() string
	Environ []string
	// EnvFunc, when set, answers the caller's environment at call time and
	// wins over Environ; a host binding like CwdFunc.
	EnvFunc func() []string
	// Discover, when set, is this row's extra verb for Analyze: run once per
	// plan in WorkDir with the same {file}/{dir}/{root}/{cwd} placeholders
	// as any Verb's Args/Env. Non-empty stdout is the [ParseMethods] shape
	// (one exported method per line); empty stdout is not a refusal, it
	// means no additional methods — a skill without tasks.md keeps exactly
	// its declared Verbs, because its processor has nothing more to say.
	Discover *Verb
	// DiscoveredVerb, when set beside Discover, is the template a
	// discovered method's own call runs: its Args' literal `{target}`
	// becomes the method's name. A discovery answer carries a name and
	// effects, not an invocation — the processor needs only the name to
	// run one.
	DiscoveredVerb *Verb
}

func (t Text) environ() []string {
	if t.EnvFunc != nil {
		if env := t.EnvFunc(); env != nil {
			return env
		}
	}
	return t.Environ
}

// callerDir answers the caller's directory this row's processor addresses:
// CwdFunc at call time, else the fixed Dir a non-interactive construction
// carries.
func (t Text) callerDir() string {
	if t.CwdFunc != nil {
		if dir := t.CwdFunc(); dir != "" {
			return dir
		}
	}
	return t.Dir
}

// RunnerFence is the runtime of a fence whose opener named a runner
// (`~~~<type> as <alias> !<runner>`). The host supplies Invoke — the
// interpreter dispatches to a Bash++ function or a registered command, a
// lowered program calls the lowered function — with argv
// `<verb> <file> [args…]`; the runner's stdout is the result. Methods come
// from `runner methods <file>`: one export JSON object per line, `name`
// with an optional `signature` and `effects` (or a comma-joined `effect`).
type RunnerFence struct {
	Type     string
	FileName string
	Runner   string
	Invoke   func(ctx context.Context, argv []string) (string, error)
}

// hostKey carries the calling host through a runner call's context: a
// lowered program's wrapper attaches its Program with WithHost so the
// RunnerFence's Invoke — fixed when the module started, before any Program
// existed — can call the lowered runner function on the Program that is
// making the call, the way a shell callback is bound per call.
type hostKey struct{}

// WithHost attaches the calling host to ctx for the RunnerFence's Invoke.
func WithHost(ctx context.Context, host any) context.Context {
	return context.WithValue(ctx, hostKey{}, host)
}

// HostFrom answers the host WithHost attached, or nil.
func HostFrom(ctx context.Context) any { return ctx.Value(hostKey{}) }

// MethodsVerb is the reserved verb a runner answers with its method list.
const MethodsVerb = "methods"

// TextFileName is the artifact file name a type materializes as when its row
// does not say: the type is the extension.
func TextFileName(typ string) string { return "fence." + typ }

// TextSignature is the default signature of a declared method: string
// arguments, one string result.
func TextSignature() Signature {
	return Signature{Params: []string{"string"}, Variadic: true, Results: []string{"string"}}
}

func (t Text) name() string                    { return "text/" + t.Type }
func (t Text) executable() string              { return "" }
func (t Text) arguments(Plan) []string         { return nil }
func (t Text) loadRequest(Plan) map[string]any { return nil }

// Analyze answers the row's verb table; the body is not read, the
// processor reads it when a verb runs.
func (t Text) Analyze(ctx context.Context, source string) ([]Export, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(t.Verbs) == 0 {
		return nil, fmt.Errorf("text fence %s declares no methods", t.Type)
	}
	exports := make([]Export, 0, len(t.Verbs))
	for _, verb := range t.Verbs {
		if verb.Name == "" {
			return nil, fmt.Errorf("text fence %s: a verb without a name", t.Type)
		}
		sig := TextSignature()
		if verb.Result != "" {
			sig.Results = []string{verb.Result}
		}
		exports = append(exports, Export{Name: verb.Name, Signature: sig, Effects: verb.Effects, Agentic: verb.Agentic})
	}
	if t.Discover != nil {
		discovered, err := t.runDiscover(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("text fence %s: discover: %w", t.Type, err)
		}
		for _, export := range discovered {
			if _, ok := t.verb(export.Name); ok {
				return nil, fmt.Errorf("text fence %s: discovered method %s shadows a declared verb", t.Type, export.Name)
			}
			exports = append(exports, export)
		}
	}
	return exports, nil
}

// runDiscover executes the Discover verb and parses its answer, tolerating
// empty output as "no additional methods" instead of [ParseMethods]'s
// refusal: a row that declares Discover but a processor that simply has
// nothing more to say (an untasked skill) is not a contract violation.
func (t Text) runDiscover(ctx context.Context, source string) ([]Export, error) {
	stdout, stderr, err := t.run(ctx, textKey(t.Type, "discover", source), source, *t.Discover, nil)
	if err != nil {
		if stderr != "" {
			return nil, fmt.Errorf("%w: %s", err, stderr)
		}
		return nil, err
	}
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}
	return ParseMethods(t.Type, stdout)
}

func (t Text) verb(name string) (Verb, bool) {
	for _, verb := range t.Verbs {
		if verb.Name == name {
			return verb, true
		}
	}
	return Verb{}, false
}

// LoweredLiteral is the Go expression a lowered program uses to construct
// this runtime; prefix is the emitter's import prefix.
func (t Text) LoweredLiteral(prefix string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%spolyglot.Text{Type:%q,FileName:%q,Tool:%q,WorkDir:%q,Shadow:%#v,Overlay:%#v,Verbs:[]%spolyglot.Verb{", prefix, t.Type, t.FileName, t.Tool, t.WorkDir, t.Shadow, t.Overlay, prefix)
	for _, verb := range t.Verbs {
		fmt.Fprintf(&out, "{Name:%q,Args:%#v,Tool:%q,Env:%#v,Effects:%#v,Result:%q", verb.Name, verb.Args, verb.Tool, verb.Env, verb.Effects, verb.Result)
		if verb.Agentic {
			out.WriteString(",Agentic:true")
		}
		out.WriteString("},")
	}
	out.WriteString("}}")
	return out.String()
}

func (r RunnerFence) name() string                    { return "runner/" + r.Runner }
func (r RunnerFence) executable() string              { return "" }
func (r RunnerFence) arguments(Plan) []string         { return nil }
func (r RunnerFence) loadRequest(Plan) map[string]any { return nil }

// Analyze asks the runner for its methods. An empty or malformed answer is a
// refusal — there is no fallback to dynamic dispatch.
func (r RunnerFence) Analyze(ctx context.Context, source string) ([]Export, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.Invoke == nil {
		return nil, fmt.Errorf("runner fence %s: no host to run %s", r.Type, r.Runner)
	}
	file, err := materializeText(textKey(r.Type, r.Runner, source), r.fileName(), source)
	if err != nil {
		return nil, err
	}
	answer, err := r.Invoke(ctx, []string{MethodsVerb, file})
	if err != nil {
		return nil, fmt.Errorf("runner %s: %s: %w", r.Runner, MethodsVerb, err)
	}
	return ParseMethods(r.Runner, answer)
}

func (r RunnerFence) fileName() string {
	if r.FileName != "" {
		return r.FileName
	}
	return TextFileName(r.Type)
}

// ParseMethods reads a runner's `methods` answer: one export per non-blank
// line, as JSON. Every method needs a name; a missing signature is
// [TextSignature].
func ParseMethods(runner, answer string) ([]Export, error) {
	var exports []Export
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(answer))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var raw struct {
			Name      string     `json:"name"`
			Signature *Signature `json:"signature"`
			Effect    string     `json:"effect"`
			Effects   []string   `json:"effects"`
			Agentic   bool       `json:"agentic"`
		}
		if err := json.Unmarshal([]byte(text), &raw); err != nil {
			return nil, fmt.Errorf("runner %s: %s line %d is not a method object: %v", runner, MethodsVerb, line, err)
		}
		if raw.Name == "" {
			return nil, fmt.Errorf("runner %s: %s line %d declares a method without a name", runner, MethodsVerb, line)
		}
		if raw.Name == MethodsVerb {
			return nil, fmt.Errorf("runner %s: %q is the reserved method verb", runner, MethodsVerb)
		}
		if seen[raw.Name] {
			return nil, fmt.Errorf("runner %s: method %s declared twice", runner, raw.Name)
		}
		seen[raw.Name] = true
		sig := TextSignature()
		if raw.Signature != nil {
			sig = *raw.Signature
		}
		effects := raw.Effects
		if raw.Effect != "" {
			effects = append(effects, strings.Split(raw.Effect, ",")...)
		}
		for i, effect := range effects {
			effects[i] = strings.TrimSpace(effect)
		}
		exports = append(exports, Export{Name: raw.Name, Signature: sig, Effects: effects, Agentic: raw.Agentic})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("runner %s: %s: %v", runner, MethodsVerb, err)
	}
	if len(exports) == 0 {
		return nil, fmt.Errorf("runner %s declared no methods", runner)
	}
	return exports, nil
}

func textKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// textRoot is where artifacts materialize: the user cache, or the temp
// directory where no user cache exists — never the checkout.
func textRoot() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "bashpp", "text")
	}
	return filepath.Join(os.TempDir(), "bashpp-text")
}

// materializeText writes the body under the cache keyed by its content and
// returns the file path. A body already materialized with the same bytes is
// reused; a changed body is rewritten, never appended to. The file name is
// the row's (a row may nest it: `skill/SKILL.md`), never the script's, and
// stays inside the fence's root.
func materializeText(key, fileName, source string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(fileName))
	if fileName == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") || filepath.IsAbs(fileName) {
		return "", fmt.Errorf("text fence: invalid artifact file name %q", fileName)
	}
	dir := filepath.Join(textRoot(), key, filepath.Dir(filepath.FromSlash(clean)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(textRoot(), key, filepath.FromSlash(clean))
	if _, err := os.Stat(file); err == nil {
		return file, nil
	}
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		return "", err
	}
	return file, nil
}

// run executes one verb of a built-in text row against a body materialized
// under key: resolve the tool, exec `tool <verb args with
// {file}/{dir}/{root}/{cwd}> <call args>` in WorkDir (the artifact's
// directory by default). Stdout and stderr come back raw and trimmed; a
// non-zero exit is reported in err with the exit code in its text — a
// caller adds its own context (the fence/method names, or nothing for a
// discovery run that is not itself a call).
func (t Text) run(ctx context.Context, key, source string, verb Verb, args []any) (stdout, stderr string, err error) {
	fileName := t.FileName
	if fileName == "" {
		fileName = TextFileName(t.Type)
	}
	file, err := materializeText(key, fileName, source)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Dir(file)
	root := filepath.Join(textRoot(), key)
	cwd := t.callerDir()
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// A tool that walks its working directory (go) reads it as $PWD spells
	// it and compares that spelling with overlay keys byte for byte: one
	// cleaned spelling serves as the placeholder, the process directory and
	// its PWD.
	cwd = filepath.Clean(cwd)
	if err := shadowEntries(dir, cwd, t.Shadow); err != nil {
		return "", "", err
	}
	overlay := ""
	var overlayErr error
	expand := func(arg string) string {
		if strings.Contains(arg, "{overlay}") && overlay == "" {
			overlay, overlayErr = writeOverlay(dir, cwd, file, t.Overlay)
		}
		arg = strings.ReplaceAll(arg, "{file}", file)
		arg = strings.ReplaceAll(arg, "{dir}", dir)
		arg = strings.ReplaceAll(arg, "{root}", root)
		arg = strings.ReplaceAll(arg, "{overlay}", overlay)
		return strings.ReplaceAll(arg, "{cwd}", cwd)
	}
	toolName := t.Tool
	if verb.Tool != "" {
		toolName = verb.Tool
	}
	environ := t.environ()
	env := envMap(environ)
	tool, _, err := resolveTool(env, toolName)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", toolName, err)
	}
	argv := append([]string(nil), tool...)
	for _, arg := range verb.Args {
		argv = append(argv, expand(arg))
	}
	if overlayErr != nil {
		return "", "", fmt.Errorf("overlay: %w", overlayErr)
	}
	for _, arg := range args {
		argv = append(argv, foreignText(arg))
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if t.WorkDir != "" {
		cmd.Dir = expand(t.WorkDir)
	}
	cmd.Env = append(environOf(environ), "BASHPP_FENCE_TYPE="+t.Type, "BASHPP_FENCE_FILE="+file, "PWD="+cmd.Dir)
	for _, entry := range verb.Env {
		cmd.Env = append(cmd.Env, expand(entry))
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	runErr := cmd.Run()
	// A text-fence verb returns text, so CRLF from Windows processors is
	// normalized before removing trailing newlines. Preserve lone CR bytes
	// and the raw stderr stream; only the text value has this contract.
	value := strings.ReplaceAll(out.String(), "\r\n", "\n")
	stdout, stderr = strings.TrimRight(value, "\n"), errBuf.String()
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return stdout, stderr, fmt.Errorf("%s exited %d", toolName, exit.ExitCode())
		}
		return stdout, stderr, runErr
	}
	return stdout, stderr, nil
}

// discoveredVerb builds the Verb a discovered method's own call runs:
// DiscoveredVerb's Args template with the literal `{target}` replaced by
// the method's name. There is no need to re-run Discover first to validate
// the name — the processor itself refuses an unknown one when the call
// runs, exactly as it would for a hand-written Verb.
func (t Text) discoveredVerb(name string) (Verb, bool) {
	if t.DiscoveredVerb == nil {
		return Verb{}, false
	}
	verb := *t.DiscoveredVerb
	args := make([]string, len(verb.Args))
	for i, arg := range verb.Args {
		args[i] = strings.ReplaceAll(arg, "{target}", name)
	}
	verb.Name, verb.Args = name, args
	return verb, true
}

// callText runs one verb of a built-in text row (declared or discovered).
// A discovered name must be a member of the plan's own Exports — the same
// membership [callRunner] already enforces — so a target added to the
// artifact after Discover ran (never audited, never gated) cannot be
// reached just because DiscoveredVerb's template can spell its name.
// Stdout is the result; a non-zero status is the call error, with stderr
// passed through.
func (m *Module) callText(ctx context.Context, text Text, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if len(kwargs) != 0 {
		return CallResult{}, fmt.Errorf("text fence %s: %s does not accept named arguments", text.Type, name)
	}
	verb, ok := text.verb(name)
	if !ok {
		export, declared := m.exportNamed(name)
		if !declared {
			return CallResult{}, fmt.Errorf("text fence %s has no method %s", text.Type, name)
		}
		verb, ok = text.discoveredVerb(name)
		if !ok {
			return CallResult{}, fmt.Errorf("text fence %s has no method %s", text.Type, name)
		}
		// The export's effects were audited once, when Discover ran to
		// build the plan; the artifact behind {target} (a tasks.md, say)
		// is a plain mutable file that may have changed since. A caller
		// upstream of here (bashPPForeignEffectsAllowed) already gated the
		// CALL against that stale declaration — recheck it again, right
		// before running, so a target widened between discovery and this
		// call (declared read, now also net) is denied on the declaration
		// in force now, not waved through on the one audited earlier.
		if err := text.recheckDiscovered(ctx, m.plan.Source, export); err != nil {
			return CallResult{}, fmt.Errorf("text fence %s.%s: %w", text.Type, name, err)
		}
	}
	stdout, stderr, err := text.run(ctx, m.plan.ID, m.plan.Source, verb, args)
	result := CallResult{Value: stdout, Stderr: stderr}
	if err != nil {
		return result, fmt.Errorf("text fence %s.%s: %w", text.Type, name, err)
	}
	return result, nil
}

// exportNamed answers the plan's own recorded Export for name — the
// declaration a discovered call must be checked against, never a looser
// guess built from the call site alone.
func (m *Module) exportNamed(name string) (Export, bool) {
	for _, export := range m.plan.Exports {
		if export.Name == name {
			return export, true
		}
	}
	return Export{}, false
}

// recheckDiscovered re-runs Discover and compares the target's current
// effects and agentic requirement against approved — what the plan recorded when
// Discover first ran. An atom present now but absent from approved denies
// the call: approved is what every upstream gate audited, and anything
// beyond it was never audited at all.
func (t Text) recheckDiscovered(ctx context.Context, source string, approved Export) error {
	if t.Discover == nil {
		return fmt.Errorf("%s: no discover verb to recheck against", approved.Name)
	}
	discovered, err := t.runDiscover(ctx, source)
	if err != nil {
		return fmt.Errorf("%s: recheck: %w", approved.Name, err)
	}
	var current *Export
	for i := range discovered {
		if discovered[i].Name == approved.Name {
			current = &discovered[i]
			break
		}
	}
	if current == nil {
		return fmt.Errorf("%s is no longer discoverable (recheck before execution)", approved.Name)
	}
	if current.Agentic && !approved.Agentic {
		return fmt.Errorf("%s now requires agentic authority absent at discovery: refusing a changed declaration", approved.Name)
	}
	allowed := make(map[string]bool, len(approved.Effects))
	for _, effect := range approved.Effects {
		allowed[effect] = true
	}
	for _, effect := range current.Effects {
		if !allowed[effect] {
			return fmt.Errorf("%s now declares effect %q, absent from the %v audited at discovery: refusing a changed declaration", approved.Name, effect, approved.Effects)
		}
	}
	return nil
}

// callRunner runs one declared method through the fence's runner.
func (m *Module) callRunner(ctx context.Context, fence RunnerFence, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if len(kwargs) != 0 {
		return CallResult{}, fmt.Errorf("runner fence %s: %s does not accept named arguments", fence.Type, name)
	}
	if fence.Invoke == nil {
		return CallResult{}, fmt.Errorf("runner fence %s: no host to run %s", fence.Type, fence.Runner)
	}
	declared := false
	for _, export := range m.plan.Exports {
		if export.Name == name {
			declared = true
		}
	}
	if !declared {
		return CallResult{}, fmt.Errorf("runner %s declares no method %s", fence.Runner, name)
	}
	file, err := materializeText(m.plan.ID, fence.fileName(), m.plan.Source)
	if err != nil {
		return CallResult{}, err
	}
	argv := []string{name, file}
	for _, arg := range args {
		argv = append(argv, foreignText(arg))
	}
	out, err := fence.Invoke(ctx, argv)
	if err != nil {
		return CallResult{Value: out}, fmt.Errorf("runner %s: %s: %w", fence.Runner, name, err)
	}
	return CallResult{Value: out}, nil
}

func foreignText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	case []byte:
		return string(v)
	}
	return fmt.Sprint(value)
}

func environOf(environ []string) []string {
	if len(environ) == 0 {
		environ = os.Environ()
	}
	return nativeTextProcessorEnv(environ, runtime.GOOS == "windows")
}

// A text-fence verb is a native processor, even when its caller is a shell
// script. Bashy spells these OS-owned paths as /c/... in that script. Windows
// tools such as Podman require their native drive spelling. Leave user-defined
// values under the shell's usual BASHYENV contract.
func nativeTextProcessorEnv(environ []string, windows bool) []string {
	result := append([]string(nil), environ...)
	if !windows {
		return result
	}
	for i, entry := range result {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !(strings.EqualFold(name, "USERPROFILE") || strings.EqualFold(name, "TEMP") || strings.EqualFold(name, "TMP")) {
			continue
		}
		result[i] = name + "=" + pathconv.NativePath(value)
	}
	return result
}

// shadowEntries links each named entry of the caller's directory into the
// artifact's directory, so a processor that wants the manifest beside the
// sources finds them there. A stale link from an earlier caller is replaced;
// an entry the caller lacks is skipped; where a link cannot be made the
// entry is copied.
func shadowEntries(dir, cwd string, entries []string) error {
	for _, entry := range entries {
		source := filepath.Join(cwd, entry)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		target := filepath.Join(dir, entry)
		if existing, err := os.Readlink(target); err == nil && existing == source {
			continue
		}
		_ = os.RemoveAll(target)
		if err := os.Symlink(source, target); err == nil {
			continue
		}
		if err := copyTree(source, target); err != nil {
			return fmt.Errorf("shadow %s: %w", entry, err)
		}
	}
	return nil
}

func copyTree(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeOverlay writes the go-style overlay JSON that presents the fenced
// manifest and its named siblings at the caller's directory.
func writeOverlay(dir, cwd, file string, siblings []string) (string, error) {
	replace := map[string]string{filepath.Join(cwd, filepath.Base(file)): file}
	for _, sibling := range siblings {
		local := filepath.Join(dir, sibling)
		if _, err := os.Stat(local); err != nil {
			if err := os.WriteFile(local, nil, 0o644); err != nil {
				return "", err
			}
		}
		replace[filepath.Join(cwd, sibling)] = local
	}
	data, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "overlay.json")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return path, nil
	}
	return path, os.WriteFile(path, data, 0o644)
}

// ManifestPath materializes a text row's body the way its first verb would
// and returns the file, for a unit that hands the manifest to another
// fence's environment before any verb has run. The key is the plan id
// [Prepare] will compute for the same block.
func ManifestPath(language, alias, fileName, source string, origin ...string) (string, error) {
	lang := canonicalLanguage(language)
	blockOrigin := ""
	if len(origin) > 0 {
		blockOrigin = origin[0]
	}
	return materializeText(planID(lang, alias, source, "", "", blockOrigin), fileName, source)
}
