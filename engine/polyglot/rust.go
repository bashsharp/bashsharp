package polyglot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Rust analyzes declaration-only Rust fences and builds them, with cargo and
// the standard serde/serde_json crates, into a persistent worker. The worker
// is carried in Plan.Artifact, so a lowered Bash++ program needs neither
// cargo nor rustc at execution time. One worker serves a fence for the life
// of its Module: static state persists between calls, and cancellation or
// Close terminates it, after which the next call restarts it from the
// immutable plan.
type Rust struct {
	// ContextFile opts standalone callers into a locked dependency context.
	// ContextSHA256, when set, binds its bytes before preparation.
	ContextFile   string
	ContextSHA256 string
	Command       string
	Environment   *EnvironmentPlan
}

func (r Rust) executable() string {
	if r.Environment != nil && r.Environment.Executable != "" {
		return r.Environment.Executable
	}
	if r.Command != "" {
		return r.Command
	}
	return "rustc"
}

// arguments is unused: the worker is the compiled artifact itself, launched
// by Module.ensure through artifactPrefix rather than an interpreter reading
// a script.
func (Rust) arguments(Plan) []string         { return nil }
func (Rust) loadRequest(Plan) map[string]any { return map[string]any{"id": 0, "op": "load"} }
func (Rust) name() string                    { return "Rust" }
func (Rust) artifactPrefix() string          { return "bashpp-rust-run-" }

func (r Rust) configure(cmd *exec.Cmd) {
	if r.Environment != nil {
		cmd.Dir = r.Environment.Dir
		cmd.Env = append([]string(nil), r.Environment.Env...)
	}
}

type rustExport struct {
	Export
	params     []string
	result     string
	resultWrap bool
	// release marks the synthetic release export, which has no dispatcher
	// arm: the worker serves it as its release operation.
	release        bool
	iteratorItem   string
	iteratorResult bool
}

// rustPublicFunction matches top-level `pub fn` declarations only: an
// indented `pub fn` is a method inside an impl block, which has no
// free-function call form and is never exported.
var rustPublicFunction = regexp.MustCompile(`(?m)^pub[\t ]+fn[\t ]+([[:alpha:]_][[:alnum:]_]*)[\t ]*\(([^)]*)\)[\t ]*(?:->[\t ]*([^\{\n]+))?[\t ]*\{`)

func (r Rust) Analyze(ctx context.Context, source string) ([]Export, error) {
	exports, _, err := r.AnalyzeArtifact(ctx, source)
	return exports, err
}

func (r Rust) AnalyzeArtifact(ctx context.Context, source string) ([]Export, string, error) {
	target, err := r.target()
	if err != nil {
		return nil, "", err
	}
	if target == "proc-macro" {
		path, digest := r.dependencyContext()
		_, err := r.buildLocked(ctx, source, "", path, digest)
		return nil, "", err
	}
	parsed, err := analyzeRustExports(source)
	if err != nil {
		return nil, "", err
	}
	if len(parsed) == 0 {
		return nil, "", errors.New("no supported public Rust functions")
	}
	generated, err := rustWorkerSource(source, parsed)
	if err != nil {
		return nil, "", err
	}
	var binary []byte
	if path, digest := r.dependencyContext(); path != "" {
		binary, err = r.buildLocked(ctx, source, generated, path, digest)
	} else {
		binary, err = r.build(ctx, generated)
	}
	if err != nil {
		return nil, "", err
	}
	exports := make([]Export, len(parsed))
	for i := range parsed {
		exports[i] = parsed[i].Export
	}
	return exports, string(binary), nil
}

// RustReleaseExport is the export every Rust fence that uses bashpp::Handle
// gains: `release(handle)` frees the worker-owned value exactly once, and a
// second release — or any later use — is refused as stale. The name is
// reserved in such a fence.
const RustReleaseExport = "release"

func analyzeRustExports(source string) ([]rustExport, error) {
	matches := rustPublicFunction.FindAllStringSubmatch(source, -1)
	out := make([]rustExport, 0, len(matches))
	seen := map[string]bool{}
	handles := false
	for _, match := range matches {
		name := match[1]
		if seen[name] {
			return nil, fmt.Errorf("Rust function %s is exported more than once", name)
		}
		seen[name] = true
		var params, bridgeParams []string
		for _, field := range splitRustTypes(match[2]) {
			if field == "" {
				continue
			}
			_, typ, ok := strings.Cut(field, ":")
			if !ok {
				return nil, fmt.Errorf("Rust function %s has an unsupported parameter %q", name, strings.TrimSpace(field))
			}
			typ = normalizeRustType(typ)
			if why := rustUnsupportedType(typ); why != "" {
				return nil, fmt.Errorf("Rust function %s has unsupported parameter type %s: %s", name, typ, why)
			}
			bridge := rustBridgeType(typ)
			handles = handles || bridge == "handle"
			params, bridgeParams = append(params, typ), append(bridgeParams, bridge)
		}
		result := normalizeRustType(match[3])
		wrapped := false
		if result == "" {
			result = "()"
		}
		if inner, ok := rustResultInner(result); ok {
			result, wrapped = inner, true
		}
		if result == "&str" {
			return nil, fmt.Errorf("Rust function %s has unsupported borrowed result type &str; return String instead", name)
		}
		if strings.HasPrefix(result, "&") {
			return nil, fmt.Errorf("Rust function %s has unsupported borrowed result type %s; return an owned value instead", name, result)
		}
		iteratorItem := ""
		iteratorResult := false
		if strings.HasPrefix(result, "impl Iterator<Item=") && strings.HasSuffix(result, ">") {
			iteratorItem = strings.TrimSuffix(strings.TrimPrefix(result, "impl Iterator<Item="), ">")
		} else if strings.HasPrefix(result, "impl Iterator<Item = ") && strings.HasSuffix(result, ">") {
			iteratorItem = strings.TrimSuffix(strings.TrimPrefix(result, "impl Iterator<Item = "), ">")
		}
		if inner, ok := rustResultInner(iteratorItem); ok {
			iteratorItem, iteratorResult = inner, true
		}
		if why := rustUnsupportedType(result); why != "" && iteratorItem == "" {
			return nil, fmt.Errorf("Rust function %s has unsupported result type %s: %s", name, result, why)
		}
		sig := Signature{Params: bridgeParams}
		if iteratorItem != "" {
			sig.Iterator = rustBridgeType(iteratorItem)
			handles = handles || sig.Iterator == "handle"
		}
		if bridgeResult := rustBridgeType(result); bridgeResult != "nil" {
			if bridgeResult == "callback" {
				return nil, fmt.Errorf("Rust function %s has unsupported result type %s: a callback expires with the call that passed it", name, result)
			}
			handles = handles || bridgeResult == "handle"
			sig.Results = []string{bridgeResult}
			if iteratorItem != "" {
				sig.Results = []string{"any"}
			}
		}
		out = append(out, rustExport{Export: Export{Name: name, Signature: sig}, params: params, result: result, resultWrap: wrapped, iteratorItem: iteratorItem, iteratorResult: iteratorResult})
	}
	if handles {
		if seen[RustReleaseExport] {
			return nil, fmt.Errorf("Rust function %s is reserved: it releases a bashpp::Handle", RustReleaseExport)
		}
		out = append(out, rustExport{Export: Export{Name: RustReleaseExport, Signature: Signature{Params: []string{"handle"}}}, release: true})
	}
	return out, nil
}

// rustReleaseExport reports whether plan is a Rust plan that carries the
// synthetic release export, which Module.CallKeywords routes to the worker's
// release operation instead of a dispatched function.
func rustReleaseExport(plan Plan) bool {
	if plan.Language != "rust" {
		return false
	}
	for _, export := range plan.Exports {
		if export.Name == RustReleaseExport && len(export.Signature.Params) == 1 && export.Signature.Params[0] == "handle" && len(export.Signature.Results) == 0 {
			return true
		}
	}
	return false
}

func splitRustTypes(source string) []string {
	var out []string
	start, depth := 0, 0
	for i, r := range source {
		switch r {
		case '<', '[', '(':
			depth++
		case '>', ']', ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(source[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(source[start:]))
	return out
}

func normalizeRustType(typ string) string {
	typ = strings.TrimSpace(typ)
	typ = strings.Join(strings.Fields(typ), " ")
	return strings.ReplaceAll(strings.ReplaceAll(typ, "< ", "<"), " >", ">")
}

func rustResultInner(typ string) (string, bool) {
	if !strings.HasPrefix(typ, "Result<") || !strings.HasSuffix(typ, ">") {
		return "", false
	}
	parts := splitRustTypes(typ[len("Result<") : len(typ)-1])
	if len(parts) != 2 {
		return "", false
	}
	return normalizeRustType(parts[0]), true
}

// rustUnsupportedType names the reason a type cannot cross the boundary at
// all. Everything else is either a scalar or an "object" that rustc and serde
// check: a type without Serialize/Deserialize fails at preparation with the
// compiler's own diagnostic.
func rustUnsupportedType(typ string) string {
	switch {
	case strings.HasPrefix(typ, "&mut "):
		return "mutable borrows cannot cross the boundary"
	case strings.Contains(typ, "'"):
		return "lifetimes cannot cross the boundary"
	case strings.HasPrefix(typ, "impl "), strings.HasPrefix(typ, "dyn "), strings.Contains(typ, "<impl "), strings.Contains(typ, "<dyn "):
		return "trait objects cannot cross the boundary"
	}
	return ""
}

// rustBridgeType maps a Rust type to the Bash# boundary kind. Scalars, bytes,
// and unit keep their typed wrappers; bashpp::Handle<T> is an opaque handle
// and bashpp::Callback a shell callback, both by value or shared borrow;
// every other type — a user struct, a Vec<Struct>, Option<T>, a map, a tuple
// — is an Object that serde_json converts on the worker side, so users write
// ordinary serde types.
func rustBridgeType(typ string) string {
	if kind := rustBoundaryType(strings.TrimSpace(strings.TrimPrefix(typ, "&"))); kind != "" {
		return kind
	}
	switch typ {
	case "bool":
		return "bool"
	case "i8", "i16", "i32", "i64", "isize", "u8", "u16", "u32", "u64", "usize":
		return "int"
	case "f32", "f64":
		return "float64"
	case "String", "&str":
		return "string"
	case "Vec<u8>":
		return "bytes"
	case "()":
		return "nil"
	}
	return "object"
}

// rustBoundaryType recognizes the two runtime-published boundary types by
// their qualified or `use`d spelling.
func rustBoundaryType(typ string) string {
	for _, prefix := range []string{"bashpp::", "crate::bashpp::", "__bpp::", "crate::__bpp::", ""} {
		rest, ok := strings.CutPrefix(typ, prefix)
		if !ok {
			continue
		}
		if rest == "Callback" {
			return "callback"
		}
		if strings.HasPrefix(rest, "Handle<") && strings.HasSuffix(rest, ">") {
			return "handle"
		}
	}
	return ""
}

// rustOwnedType is the type a borrowed parameter is deserialized into before
// the call borrows it: &str → String, &[T] → Vec<T>, &T → T. Deref coercion
// turns the borrow back into the declared parameter type.
func rustOwnedType(typ string) string {
	if !strings.HasPrefix(typ, "&") {
		return typ
	}
	typ = strings.TrimSpace(typ[1:])
	if typ == "str" {
		return "String"
	}
	if strings.HasPrefix(typ, "[") && strings.HasSuffix(typ, "]") {
		return "Vec<" + typ[1:len(typ)-1] + ">"
	}
	return typ
}

// rustWorkerSource is the fence source followed by the generated dispatcher
// and the constant worker runtime. The dispatcher is the only per-fence code:
// arity check, one serde decode per argument, the call, one serde encode.
func rustWorkerSource(source string, exports []rustExport) (string, error) {
	var out strings.Builder
	out.WriteString(source)
	out.WriteString("\n\nfn __bpp_dispatch(name: &str, args: &[serde_json::Value]) -> Result<serde_json::Value, String> {\n    match name {\n")
	for _, export := range exports {
		if export.release {
			continue
		}
		fmt.Fprintf(&out, "        %q => {\n            __bpp::arity(%q, args, %d)?;\n", export.Name, export.Name, len(export.params))
		callArgs := make([]string, len(export.params))
		for i, typ := range export.params {
			local := fmt.Sprintf("__bpp_arg%d", i)
			if typ == "Vec<u8>" {
				fmt.Fprintf(&out, "            let %s: Vec<u8> = __bpp::bytes_arg(%q, args, %d)?;\n", local, export.Name, i)
			} else {
				fmt.Fprintf(&out, "            let %s: %s = __bpp::arg(%q, args, %d)?;\n", local, rustOwnedType(typ), export.Name, i)
			}
			if strings.HasPrefix(typ, "&") {
				local = "&" + local
			}
			callArgs[i] = local
		}
		call := fmt.Sprintf("%s(%s)", export.Name, strings.Join(callArgs, ", "))
		switch {
		case export.resultWrap:
			fmt.Fprintf(&out, "            let __bpp_value = match %s { Ok(value) => value, Err(error) => return Err(error.to_string()) };\n", call)
		case export.result == "()":
			fmt.Fprintf(&out, "            %s;\n", call)
		default:
			fmt.Fprintf(&out, "            let __bpp_value = %s;\n", call)
		}
		if export.iteratorItem != "" {
			conversion := "serde_json::to_value(value).map_err(|error| error.to_string())"
			if export.iteratorItem == "Vec<u8>" {
				conversion = "Ok(__bpp::bytes_value(&value))"
			}
			if export.iteratorResult {
				fmt.Fprintf(&out, "            Ok(__bpp::iterator(__bpp_value.map(|item| item.map_err(|error| error.to_string()).and_then(|value| %s))))\n", conversion)
			} else {
				fmt.Fprintf(&out, "            Ok(__bpp::iterator(__bpp_value.map(|value| %s)))\n", conversion)
			}
			out.WriteString("        }\n")
			continue
		}
		switch export.result {
		case "()":
			out.WriteString("            Ok(serde_json::Value::Null)\n")
		case "Vec<u8>":
			out.WriteString("            Ok(__bpp::bytes_value(&__bpp_value))\n")
		default:
			fmt.Fprintf(&out, "            __bpp::value(%q, __bpp_value)\n", export.Name)
		}
		out.WriteString("        }\n")
	}
	out.WriteString("        _ => Err(format!(\"unknown Rust function {}\", name)),\n    }\n}\n")
	out.WriteString(rustWorkerRuntime)
	return out.String(), nil
}

// build writes the worker crate to a private temporary directory and drives
// cargo: `cargo fetch` (offline first, so a warm registry cache never touches
// the network) resolves and downloads serde, serde_json, and base64, and
// `cargo build --frozen` compiles against exactly that resolution. The
// target directory is shared across builds so the dependency crates compile
// once per toolchain; the per-source binary name keeps concurrent builds
// from overwriting each other.
func (r Rust) build(ctx context.Context, generated string) ([]byte, error) {
	cargo, err := r.cargo()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bashpp-rust-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sum := sha256.Sum256([]byte(generated))
	name := "m" + hex.EncodeToString(sum[:8])
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(fmt.Sprintf(rustManifest, name)), 0o600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte(generated), 0o600); err != nil {
		return nil, err
	}
	target := rustTargetDir()
	// Whatever the outcome, only the shared dependency artifacts stay in the
	// cache; this build's own binary, deps, and fingerprints are removed.
	defer func() {
		for _, pattern := range []string{
			filepath.Join(target, "debug", name+"*"),
			filepath.Join(target, "debug", "deps", name+"-*"),
			filepath.Join(target, "debug", ".fingerprint", name+"-*"),
			filepath.Join(target, "debug", "incremental", name+"-*"),
		} {
			stale, _ := filepath.Glob(pattern)
			for _, path := range stale {
				_ = os.RemoveAll(path)
			}
		}
	}()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, cargo[0], append(append([]string(nil), cargo[1:]...), args...)...)
		cmd.Dir = dir
		cmd.Env = r.buildEnvironment(target)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		return strings.TrimSpace(stderr.String()), err
	}
	if message, err := run("fetch", "--offline", "--quiet"); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Rust cargo unavailable: %w", err)
		}
		if message, err = run("fetch", "--quiet"); err != nil {
			return nil, fmt.Errorf("Rust dependencies unavailable: %s", rustFailure(message, err))
		}
	}
	if message, err := run("build", "--frozen", "--quiet"); err != nil {
		return nil, errors.New(rustFailure(message, err))
	}
	output := filepath.Join(target, "debug", name)
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	return os.ReadFile(output)
}

func rustFailure(message string, err error) string {
	if message == "" {
		return err.Error()
	}
	return message
}

// cargo names the cargo that drives the build, as argv: the one beside the
// selected rustc (rustup proxies and distribution toolchains ship them
// together, so BASHPP_RUSTC and an overlay keep selecting one toolchain),
// else the embedder's tool resolver, else PATH — the same rungs rustc itself
// resolved through.
func (r Rust) cargo() ([]string, error) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if rustc, err := exec.LookPath(r.executable()); err == nil {
		if resolved, err := filepath.EvalSymlinks(rustc); err == nil {
			rustc = resolved
		}
		beside := filepath.Join(filepath.Dir(rustc), "cargo"+suffix)
		if info, err := os.Stat(beside); err == nil && !info.IsDir() {
			return []string{beside}, nil
		}
	}
	if ToolResolver != nil && !r.overridden() {
		argv, _, err := ToolResolver("cargo")
		if err != nil {
			return nil, fmt.Errorf("Rust cargo unavailable: %w", err)
		}
		if len(argv) == 0 || argv[0] == "" {
			return nil, errors.New("Rust cargo unavailable: tool resolver returned no program for cargo")
		}
		return argv, nil
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		return nil, fmt.Errorf("Rust cargo unavailable: %w", err)
	}
	return []string{cargo}, nil
}

// buildEnvironment is the cargo child environment: the plan's launch
// environment (or the process's, for an unplanned runtime), the selected
// rustc, the shared target directory, and the resolver's linker.
func (r Rust) buildEnvironment(target string) []string {
	var env []string
	if r.Environment != nil {
		env = append(env, r.Environment.Env...)
	} else {
		env = os.Environ()
	}
	if rustc := r.executable(); rustc != "rustc" {
		env = append(env, "RUSTC="+rustc)
	}
	env = append(env, "CARGO_TARGET_DIR="+target, "CARGO_TERM_COLOR=never")
	if args := r.linkerArgs(); len(args) != 0 {
		env = append(env, "RUSTFLAGS="+strings.Join(args, " "))
	}
	return env
}

// rustTargetDir is the cargo target directory shared by every worker build:
// the user cache, or the temp directory where no user cache exists.
func rustTargetDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "bashpp", "rust-target")
	}
	return filepath.Join(os.TempDir(), "bashpp-rust-target")
}

// linkerArgs names rustc's linker when the embedder's tool resolver
// provides one ("cc-linker": a single program that behaves as cc — bashy
// answers with a wrapper over its provisioned zig cc). Without it rustc
// looks for `cc` on PATH, which a host with no toolchain does not have;
// on Windows the provisioned toolchain is the gnu one, linked the same way.
func (r Rust) linkerArgs() []string {
	if ToolResolver == nil || r.overridden() {
		return nil
	}
	argv, _, err := ToolResolver("cc-linker")
	if err != nil || len(argv) != 1 {
		return nil
	}
	return []string{"-C", "linker=" + argv[0]}
}

// overridden reports a rustc the caller named with BASHPP_RUSTC (or an
// overlay): that toolchain keeps its own linker — a host msvc rustc handed
// zig cc's gnu flavour would fail to link.
func (r Rust) overridden() bool {
	if r.Environment == nil {
		return false
	}
	for _, why := range r.Environment.Explanation {
		if why == "compiler executable overridden" || why == "selected bashpp overlay" {
			return true
		}
	}
	return false
}
