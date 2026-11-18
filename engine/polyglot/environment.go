package polyglot

// This file deliberately contains no subprocess calls. Environment selection is
// part of planning: callers may safely use it while parsing or type checking.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// EnvironmentRequest describes the source being planned. Source must be a file
// name (it need not exist). Name selects a named bashpp environment. Environ is
// an optional os.Environ-style snapshot; an empty slice means os.Environ().
// Supplying a snapshot makes planning independent of ambient process state.
// ToolResolverFunc is the embedder's answer to "which program serves this
// island tool name" — go, cc, c++, python3, pythonX.Y, node, bun, rustc,
// cargo. It returns the argv to run (a prefix such as [zig cc] is allowed),
// a one-line reason recorded in [EnvironmentPlan.Explanation], or an error
// naming what it could not provide.
//
// It is the island counterpart of the shell's own command lookup, but a
// MATERIALIZING one: the embedder may provision (download, verify, cache)
// the toolchain before answering, because an island needs a program it can
// exec, not a name. It is called only when a fence actually needs the tool.
type ToolResolverFunc func(name string) (argv []string, why string, err error)

// ToolResolver, when set, replaces the PATH rung of every island's tool
// resolution: a fence then never resolves its tool from the process PATH,
// so the same program means the same thing on every host. The project's own
// declaration (a venv, an overlay runtime) and the BASHPP_* overrides still
// win, as they name a program explicitly. nil (the default) keeps the PATH
// lookup, which is what a standalone engine and the certification profile
// run.
var ToolResolver ToolResolverFunc

// resolveTool answers name through [ToolResolver] when one is set, and from
// PATH otherwise. The argv it returns is never empty on success.
func resolveTool(env map[string]string, name string) (argv []string, why string, err error) {
	if ToolResolver != nil {
		argv, why, err = ToolResolver(name)
		if err != nil {
			return nil, "", err
		}
		if len(argv) == 0 || argv[0] == "" {
			return nil, "", fmt.Errorf("tool resolver returned no program for %s", name)
		}
		if why == "" {
			why = "selected provisioned " + name
		}
		return argv, why, nil
	}
	executable, err := lookupPath(env, name)
	if err != nil {
		return nil, "", err
	}
	return []string{executable}, "selected PATH " + name, nil
}

// resolveOverride is a BASHPP_* override: the program the caller named,
// explicitly — an absolute path as given, a bare name on PATH. It never goes
// through [ToolResolver]; that is what makes it the escape.
func resolveOverride(env map[string]string, name string) (string, error) {
	if filepath.IsAbs(name) {
		return canonicalExecutable(name, env)
	}
	return lookupPath(env, name)
}

// applyTool records a resolved argv on the plan: argv[0] is the executable
// (made canonical), the rest are the leading arguments every launch splices
// in after it.
func (p *EnvironmentPlan) applyTool(argv []string, env map[string]string) error {
	executable, err := canonicalExecutable(argv[0], env)
	if err != nil {
		return err
	}
	p.Executable = executable
	p.ExecutableArgs = append([]string(nil), argv[1:]...)
	return nil
}

type EnvironmentRequest struct {
	Source   string
	Language string
	Name     string
	Environ  []string
	// ModuleFile, for Go, is a manifest fence's materialized go.mod: the
	// source's directory becomes the module root without a go.mod in it,
	// the file being presented there through the worker build's overlay.
	ModuleFile string
}

// EnvironmentPlan is the immutable, language-neutral resolution record. Its
// fields are values, rather than handles to discovery state, so it can be
// fingerprinted, cached, and handed from checking to execution unchanged.
// Slice fields are copies owned by the plan and must be treated read-only.
type EnvironmentPlan struct {
	CSharpAssemblies string // immutable resolved C# graph JSON, including asset bytes
	RustTarget       string // selected Rust crate role; empty is the binary worker
	// Preparation-only immutable snapshot; deliberately omitted from lowered launch plans.
	rustContext       *rustDependencySnapshot
	Language          string
	Name              string
	Root              string   // project or VCS boundary
	Dir               string   // fixed child-process working directory
	SourceDir         string   // directory containing the requesting source
	Executable        string   // absolute selected runtime executable
	ExecutableArgs    []string // leading arguments every launch places after Executable (zig cc)
	Manager           string
	RuntimeConstraint string
	Runtime           string             // selected runtime family, such as node or bun
	CompilerModule    string             // TypeScript compiler package or entry point
	NativeModules     []NativeModuleUnit // validated C++ source units in dependency order
	Manifests         []string
	Locks             []string
	PythonPath        []string
	Env               []string // normalized launch environment, sorted by key
	Explanation       []string // deterministic and redacted; no ambient values
	// ResolutionFiles are canonical, source-relative metadata inputs whose
	// contents participate in Fingerprint. They are ordered by discovery.
	ResolutionFiles []string
	Fingerprint     string
	// ModuleFile is the manifest fence's go.mod a Go fence builds against
	// when the tree holds none; "" means the nearest go.mod of the tree.
	ModuleFile string
}

// Clone returns an independently owned copy of p.
func (p EnvironmentPlan) Clone() EnvironmentPlan {
	p.NativeModules = append([]NativeModuleUnit(nil), p.NativeModules...)
	for i := range p.NativeModules {
		p.NativeModules[i].Requires = append([]string(nil), p.NativeModules[i].Requires...)
	}
	p.ExecutableArgs = append([]string(nil), p.ExecutableArgs...)
	p.Manifests = append([]string(nil), p.Manifests...)
	p.Locks = append([]string(nil), p.Locks...)
	p.PythonPath = append([]string(nil), p.PythonPath...)
	p.Env = append([]string(nil), p.Env...)
	p.Explanation = append([]string(nil), p.Explanation...)
	p.ResolutionFiles = append([]string(nil), p.ResolutionFiles...)
	return p
}

// nodeFamily reports the fence languages that run on the Node/Bun worker and
// share one project reader: JavaScript is TypeScript without the type check.
func nodeFamily(language string) bool { return language == "typescript" || language == "javascript" }

// DiscoverEnvironment performs source-relative, read-only environment
// discovery. It never executes a runtime, imports a module, invokes a package
// manager, or writes a file.
func DiscoverEnvironment(request EnvironmentRequest) (EnvironmentPlan, error) {
	lang := strings.ToLower(strings.TrimSpace(request.Language))
	if lang == "" {
		lang = "python"
	}
	lang = canonicalLanguage(lang)
	if !languageFlag(lang, func(l Language) bool { return l.NeedsEnvironment }) {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: no environment metadata reader for %q", lang)
	}
	if request.Source == "" {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: environment source is required")
	}
	source, err := filepath.Abs(request.Source)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	dir := filepath.Dir(source)
	env := envMap(request.Environ)
	root, dirs, err := environmentDirs(dir, lang)
	if err != nil {
		return EnvironmentPlan{}, err
	}

	// A pair of formats at one level is intentionally ambiguous. Silently
	// preferring YAML made adding a JSON override change a build unexpectedly.
	var overlays []environmentOverlay
	for _, d := range dirs {
		yamlFile, jsonFile := filepath.Join(d, "bashpp.yaml"), filepath.Join(d, "bashpp.json")
		yaml, yerr := readOverlay(yamlFile)
		jsonOverlay, jerr := readOverlay(jsonFile)
		if yerr != nil {
			return EnvironmentPlan{}, yerr
		}
		if jerr != nil {
			return EnvironmentPlan{}, jerr
		}
		if yaml != nil && jsonOverlay != nil {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: ambiguous environment overlays in %s", d)
		}
		if yaml != nil {
			for i := range yaml {
				yaml[i].dir = d
			}
			overlays = append(overlays, yaml...)
		}
		if jsonOverlay != nil {
			for i := range jsonOverlay {
				jsonOverlay[i].dir = d
			}
			overlays = append(overlays, jsonOverlay...)
		}
	}
	matching := make([]environmentOverlay, 0, len(overlays))
	for _, o := range overlays {
		if o.Language == "" || o.Language == lang || (nodeFamily(lang) && nodeFamily(o.Language)) {
			matching = append(matching, o)
		}
	}
	var selected *environmentOverlay
	if request.Name != "" {
		for i := range matching {
			if matching[i].Name == request.Name {
				if selected != nil {
					return EnvironmentPlan{}, fmt.Errorf("polyglot: ambiguous environment %q", request.Name)
				}
				selected = &matching[i]
			}
		}
		if selected == nil {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: environment %q not found", request.Name)
		}
	} else if len(matching) == 1 {
		selected = &matching[0]
	} else if len(matching) > 1 {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: ambiguous matching environment overlays")
	}

	if selected != nil && selected.CSharpAssemblies != "" && lang != "csharp" {
		return EnvironmentPlan{}, fmt.Errorf("csharp_assemblies requires C#")
	}
	plan := EnvironmentPlan{Language: lang, Name: request.Name, Root: root, Dir: root, SourceDir: dir}
	if lang == "go" && request.ModuleFile != "" {
		// The manifest fence is the module: its root is the caller's
		// directory, whatever go.mod the tree may hold further up.
		plan.Root, plan.Dir, plan.ModuleFile = dir, dir, request.ModuleFile
	}
	for _, d := range dirs {
		for _, name := range []string{"bashpp.yaml", "bashpp.json"} {
			if file := canonicalExistingFile(filepath.Join(d, name)); file != "" {
				plan.ResolutionFiles = append(plan.ResolutionFiles, file)
			}
		}
	}
	projectMetadata := pythonProjectMetadata
	if nodeFamily(lang) {
		projectMetadata = typeScriptProjectMetadata
	} else if lang == "rust" {
		projectMetadata = rustProjectMetadata
	} else if lang == "c" || lang == "cpp" {
		projectMetadata = nativeProjectMetadata
	} else if lang == "go" {
		projectMetadata = goProjectMetadata
	} else if lang == "powershell" || lang == "csharp" {
		// A PowerShell or C# fence has no project manifest: only the pwsh
		// runtime is resolved, so nothing contributes to ResolutionFiles here.
		projectMetadata = nil
	}
	for _, name := range projectMetadata {
		if file := canonicalExistingFile(filepath.Join(root, name)); file != "" {
			plan.ResolutionFiles = append(plan.ResolutionFiles, file)
		}
	}
	if nodeFamily(lang) {
		return discoverTypeScriptEnvironment(plan, dirs, selected, env)
	}
	if lang == "rust" {
		return discoverRustEnvironment(plan, selected, env)
	}
	if lang == "c" || lang == "cpp" {
		return discoverNativeEnvironment(plan, selected, env)
	}
	if lang == "go" {
		return discoverGoEnvironment(plan, selected, env)
	}
	if lang == "powershell" || lang == "csharp" {
		// C# compiles and runs through PowerShell's Add-Type in the same
		// pinned archive, so both rows resolve the one pwsh runtime.
		return discoverPowerShellEnvironment(plan, selected, env)
	}
	metadata, err := discoverPythonMetadata(root)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	plan.Manager = metadata.manager
	plan.RuntimeConstraint = metadata.runtimeConstraint
	plan.Manifests = metadata.manifests
	plan.Locks = metadata.locks
	var executable string
	if selected != nil {
		plan.Explanation = append(plan.Explanation, "selected bashpp overlay")
		if selected.Name != "" {
			plan.Name = selected.Name
		}
		executable, err = resolveRuntime(selected.dir, selected.Runtime, env)
		if err != nil {
			return EnvironmentPlan{}, err
		}
	} else if projectRuntime, why := projectPythonRuntime(root, env); projectRuntime != "" {
		plan.Explanation = append(plan.Explanation, why)
		executable = projectRuntime
	} else if active := activePythonRuntime(env); active != "" {
		plan.Explanation = append(plan.Explanation, "selected inherited active environment")
		executable = active
	} else if ToolResolver != nil {
		argv, why, rerr := resolveTool(env, "python3")
		if rerr != nil {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: Python runtime unavailable: %w", rerr)
		}
		plan.Explanation = append(plan.Explanation, why)
		executable = argv[0]
		plan.ExecutableArgs = append([]string(nil), argv[1:]...)
	} else {
		plan.Explanation = append(plan.Explanation, "selected PATH runtime")
		executable, err = lookupPythonRuntime(env, runtime.GOOS)
		if err != nil {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: Python runtime unavailable: %w", err)
		}
	}
	// The override changes only the executable, never which environment won.
	if override := env["BASHPP_PYTHON"]; override != "" {
		executable, err = resolveRuntime(root, override, env)
		if err != nil {
			return EnvironmentPlan{}, err
		}
		plan.Explanation = append(plan.Explanation, "runtime executable overridden")
	}
	plan.Executable, err = canonicalExecutable(executable, env)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	if cfg := pyvenvConfig(plan.Executable); cfg != "" {
		plan.ResolutionFiles = append(plan.ResolutionFiles, cfg)
	}
	plan.PythonPath, err = canonicalPythonPath(env["PYTHONPATH"], root)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	plan.Env = launchEnvironment(env, plan.PythonPath)
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

type environmentOverlay struct {
	Name, Language, Runtime, NativeModules, CSharpAssemblies, RustContext string
	dir                                                                   string
}

func environmentDirs(start, language string) (string, []string, error) {
	start, err := nearestExistingDir(start)
	if err != nil {
		return "", nil, err
	}
	var dirs []string
	project := ""
	projectIndex := -1
	for d := start; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if recognizedProject(d, language) {
			if project == "" {
				project = d
				projectIndex = len(dirs) - 1
			}
			if nodeFamily(language) {
				if typeScriptWorkspaceBoundary(d) {
					project = d
					projectIndex = len(dirs) - 1
					break
				}
			} else {
				break
			}
		}
		if exists(filepath.Join(d, ".git")) {
			if project == "" || nodeFamily(language) {
				project = d
				projectIndex = len(dirs) - 1
			}
			break
		}
		if parent := filepath.Dir(d); parent == d {
			if project == "" {
				project = start
				projectIndex = 0
			}
			break
		}
	}
	if projectIndex >= 0 {
		dirs = dirs[:projectIndex+1]
	}
	// closest overlays are considered first, and discovery cannot escape root.
	return project, dirs, nil
}

func typeScriptWorkspaceBoundary(dir string) bool {
	for _, name := range []string{"package-lock.json", "pnpm-lock.yaml", "bun.lock", "bun.lockb"} {
		if exists(filepath.Join(dir, name)) {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	return json.Unmarshal(data, &pkg) == nil && len(pkg.Workspaces) > 0 && string(pkg.Workspaces) != "null"
}

func nearestExistingDir(start string) (string, error) {
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for d := start; ; d = filepath.Dir(d) {
		resolved, err := filepath.EvalSymlinks(d)
		if err == nil {
			info, statErr := os.Stat(resolved)
			if statErr == nil && info.IsDir() {
				return resolved, nil
			}
			if statErr != nil && !os.IsNotExist(statErr) {
				return "", statErr
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if parent := filepath.Dir(d); parent == d {
			return "", fmt.Errorf("polyglot: no existing source ancestor for %s", start)
		}
	}
}

func recognizedProject(dir, language string) bool {
	if language == "go" {
		return exists(filepath.Join(dir, "go.mod"))
	}
	metadata := pythonProjectMetadata
	if nodeFamily(language) {
		metadata = typeScriptProjectMetadata
	} else if language == "rust" {
		metadata = rustProjectMetadata
	} else if language == "c" || language == "cpp" {
		metadata = nativeProjectMetadata
	}
	for _, n := range metadata {
		if exists(filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}

var rustProjectMetadata = []string{"Cargo.toml", "Cargo.lock", "rust-toolchain.toml", "rust-toolchain"}

var nativeProjectMetadata = []string{"CMakeLists.txt", "compile_commands.json", "meson.build", "Makefile", "makefile"}

var goProjectMetadata = []string{"go.mod", "go.sum", "go.work", "go.work.sum"}

func discoverGoEnvironment(plan EnvironmentPlan, selected *environmentOverlay, env map[string]string) (EnvironmentPlan, error) {
	if plan.ModuleFile != "" {
		plan.Manifests = append(plan.Manifests, plan.ModuleFile)
		plan.Explanation = append(plan.Explanation, "module from the gomod fence")
	} else if canonicalExistingFile(filepath.Join(plan.Root, "go.mod")) == "" {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: Go source fence requires a nearest go.mod (or a ~~~gomod fence in the unit)")
	}
	for _, name := range goProjectMetadata {
		if plan.ModuleFile != "" {
			break
		}
		if file := canonicalExistingFile(filepath.Join(plan.Root, name)); file != "" {
			if name == "go.sum" || name == "go.work.sum" {
				plan.Locks = append(plan.Locks, file)
			} else {
				plan.Manifests = append(plan.Manifests, file)
			}
		}
	}
	requested, manager := "go", "go"
	if selected != nil && selected.Runtime != "" {
		requested = selected.Runtime
		plan.Explanation = append(plan.Explanation, "selected bashpp overlay")
	}
	overridden := false
	if override := env["BASHPP_GO"]; override != "" {
		requested = override
		overridden = true
		plan.Explanation = append(plan.Explanation, "Go executable overridden")
	}
	var executable string
	var err error
	if overridden {
		executable, err = resolveOverride(env, requested)
	} else if filepath.IsAbs(requested) {
		executable, err = canonicalExecutable(requested, env)
		if err == nil {
			plan.Explanation = append(plan.Explanation, "selected Go toolchain")
		}
	} else if argv, why, rerr := resolveTool(env, requested); rerr == nil {
		if err = plan.applyTool(argv, env); err == nil {
			executable = plan.Executable
			plan.Explanation = append(plan.Explanation, why)
		}
	} else if requested == "go" && ToolResolver == nil {
		// Standalone engine, no go on PATH: bashy on PATH provisions one.
		executable, err = lookupPath(env, "bashy")
		manager = "bashy"
		if err == nil {
			plan.Explanation = append(plan.Explanation, "selected bashy go provisioner")
		}
	} else {
		err = rerr
	}
	if err != nil {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: Go toolchain unavailable: %w", err)
	}
	plan.Executable, plan.Manager, plan.Runtime = executable, manager, "native"
	plan.Env = goLaunchEnvironment(env)
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

func goLaunchEnvironment(env map[string]string) []string {
	var out []string
	// LOCALAPPDATA/USERPROFILE and friends: the go command's default build
	// cache is %LocalAppData%\go-build, and without it `go build` refuses
	// ("build cache is required, but could not be located").
	for _, key := range []string{"PATH", "HOME", "SystemRoot", "TMPDIR", "TEMP", "TMP", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOMODCACHE", "GOCACHE", "GOPATH",
		"LOCALAPPDATA", "APPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SystemDrive", "PATHEXT", "ComSpec", "XDG_CACHE_HOME"} {
		if value := environmentValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	sort.Strings(out)
	return out
}

func discoverNativeEnvironment(plan EnvironmentPlan, selected *environmentOverlay, env map[string]string) (EnvironmentPlan, error) {
	for _, name := range nativeProjectMetadata {
		if file := canonicalExistingFile(filepath.Join(plan.Root, name)); file != "" {
			if name == "compile_commands.json" {
				plan.Locks = append(plan.Locks, file)
			} else {
				plan.Manifests = append(plan.Manifests, file)
			}
		}
	}
	requested := ""
	if selected != nil && selected.Runtime != "" {
		requested = selected.Runtime
		plan.Explanation = append(plan.Explanation, "selected bashpp overlay")
	}
	override := "BASHPP_CC"
	candidates := []string{"clang", "cc"}
	if plan.Language == "cpp" {
		override = "BASHPP_CXX"
		candidates = []string{"clang++", "c++"}
	}
	overridden := false
	if value := env[override]; value != "" {
		requested = value
		overridden = true
		plan.Explanation = append(plan.Explanation, "compiler executable overridden")
	}
	var executable string
	var err error
	if requested != "" {
		if overridden {
			executable, err = resolveOverride(env, requested)
		} else if filepath.IsAbs(requested) {
			executable, err = canonicalExecutable(requested, env)
		} else if argv, _, rerr := resolveTool(env, requested); rerr == nil {
			err = plan.applyTool(argv, env)
			executable = plan.Executable
		} else {
			err = rerr
		}
	} else {
		var why string
		for _, candidate := range candidates {
			var argv []string
			if argv, why, err = resolveTool(env, candidate); err == nil {
				if err = plan.applyTool(argv, env); err == nil {
					executable = plan.Executable
				}
				break
			}
		}
		if err == nil {
			plan.Explanation = append(plan.Explanation, why)
		}
	}
	if err != nil {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: %s compiler unavailable: %w", nativeLanguageName(plan.Language), err)
	}
	plan.Executable = executable
	if selected != nil && selected.NativeModules != "" {
		if plan.Language != "cpp" {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: native_modules requires C++")
		}
		units, files, err := readNativeModules(plan.Root, selected.dir, selected.NativeModules)
		if err != nil {
			return EnvironmentPlan{}, err
		}
		plan.NativeModules = units
		plan.ResolutionFiles = append(plan.ResolutionFiles, files...)
	}
	plan.Manager, plan.Runtime = "clang", "native"
	plan.Env = nativeLaunchEnvironment(env)
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

func discoverRustEnvironment(plan EnvironmentPlan, selected *environmentOverlay, env map[string]string) (EnvironmentPlan, error) {
	var err error
	for _, name := range rustProjectMetadata {
		if file := canonicalExistingFile(filepath.Join(plan.Root, name)); file != "" {
			switch name {
			case "Cargo.toml":
				plan.Manifests = append(plan.Manifests, file)
			case "Cargo.lock":
				plan.Locks = append(plan.Locks, file)
			}
		}
	}
	requested := "rustc"
	if selected != nil && selected.Runtime != "" {
		requested = selected.Runtime
		plan.Explanation = append(plan.Explanation, "selected bashpp overlay")
	}
	if override := env["BASHPP_RUSTC"]; override != "" {
		requested = override
		plan.Explanation = append(plan.Explanation, "compiler executable overridden")
		plan.Executable, err = resolveOverride(env, requested)
	} else if filepath.IsAbs(requested) {
		plan.Executable, err = canonicalExecutable(requested, env)
	} else if argv, why, rerr := resolveTool(env, requested); rerr == nil {
		if err = plan.applyTool(argv, env); err == nil {
			plan.Explanation = append(plan.Explanation, why)
		}
	} else {
		err = rerr
	}
	if err != nil {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: Rust compiler unavailable: %w", err)
	}
	plan.Manager, plan.Runtime = "cargo", "native"
	plan.Env = rustLaunchEnvironment(env)
	if selected != nil && selected.RustContext != "" && env["BASHPP_RUST_CONTEXT"] == "" {
		contextPath := selected.RustContext
		if !filepath.IsAbs(contextPath) {
			contextPath = filepath.Join(selected.dir, contextPath)
		}
		env["BASHPP_RUST_CONTEXT"] = contextPath
	}
	if err := discoverRustContext(&plan, env); err != nil {
		return EnvironmentPlan{}, err
	}
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

// rustLaunchEnvironment is the cargo child environment: the native toolchain
// set plus the keys that place the registry cache and the rustup toolchain,
// so a fence builds against the same crates and compiler as the user's own
// `cargo build`.
func rustLaunchEnvironment(env map[string]string) []string {
	out := nativeLaunchEnvironment(env)
	for _, key := range []string{"CARGO_HOME", "RUSTUP_HOME", "RUSTUP_TOOLCHAIN"} {
		if value := environmentValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	sort.Strings(out)
	return out
}

// discoverPowerShellEnvironment resolves the pwsh runtime for a PowerShell
// fence. The BASHPP_PWSH override names a program explicitly (an absolute path
// or a bare name on PATH) and never goes through the resolver; otherwise the
// embedder's ToolResolver answers "pwsh" (bashy provisions the pinned
// PowerShell 7), falling back to a PATH lookup for a standalone engine. There
// is no project manifest to inventory — only the runtime is selected.
func discoverPowerShellEnvironment(plan EnvironmentPlan, selected *environmentOverlay, env map[string]string) (EnvironmentPlan, error) {
	requested := "pwsh"
	if selected != nil && selected.Runtime != "" {
		requested = selected.Runtime
		plan.Explanation = append(plan.Explanation, "selected bashpp overlay")
	}
	var err error
	if override := env["BASHPP_PWSH"]; override != "" {
		requested = override
		plan.Explanation = append(plan.Explanation, "runtime executable overridden")
		plan.Executable, err = resolveOverride(env, requested)
	} else if filepath.IsAbs(requested) {
		plan.Executable, err = canonicalExecutable(requested, env)
	} else if argv, why, rerr := resolveTool(env, requested); rerr == nil {
		if err = plan.applyTool(argv, env); err == nil {
			plan.Explanation = append(plan.Explanation, why)
			// The embedder's provisioner can select a private .NET runtime and
			// install child-process controls while resolving pwsh. Discovery
			// copied request.Environ before that call, so carry those controls
			// into the worker's own environment snapshot.
			if ToolResolver != nil {
				for _, key := range []string{"LD_LIBRARY_PATH", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "DOTNET_ROOT"} {
					// An explicit recipe wins over provisioner defaults.
					if key == "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT" {
						if _, present := powerShellEnvironmentEntry(env, key); present {
							continue
						}
					}
					if value, ok := os.LookupEnv(key); ok {
						env[key] = value
					}
				}
			}
		}
	} else {
		err = rerr
	}
	if err != nil {
		if plan.Language == "csharp" {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: C# runtime (pinned PowerShell) unavailable: %w", err)
		}
		return EnvironmentPlan{}, fmt.Errorf("polyglot: PowerShell runtime unavailable: %w", err)
	}
	if selected != nil && selected.CSharpAssemblies != "" {
		if plan.Language != "csharp" {
			return EnvironmentPlan{}, fmt.Errorf("csharp_assemblies requires C#")
		}
		plan.CSharpAssemblies, err = resolveCSharpAssemblies(selected.dir, selected.CSharpAssemblies)
		if err != nil {
			return EnvironmentPlan{}, err
		}
	}
	plan.Manager, plan.Runtime = "pwsh", "pwsh"
	plan.Env = powerShellLaunchEnvironment(env)
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

// powerShellLaunchEnvironment is the pwsh child environment: the portable keys
// plus the Windows ones the .NET host and PowerShell need to locate HOME, the
// temp directory and its module path, with telemetry opted out so a worker
// never phones home.
func powerShellLaunchEnvironment(env map[string]string) []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "SystemRoot", "TMPDIR", "TEMP", "TMP",
		"PATHEXT", "ComSpec", "SystemDrive", "windir",
		"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData",
		"APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"PSModulePath", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
		"LD_LIBRARY_PATH", "DOTNET_ROOT", "SSL_CERT_FILE"} {
		if value := environmentValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	// Only these explicit startup inputs are admitted, including empty values.
	// The resulting immutable plan and fingerprint carry the same exact bytes.
	for _, key := range powerShellExecutionKeys {
		if value, present := powerShellEnvironmentEntry(env, key); present {
			out = append(out, key+"="+value)
		}
	}
	out = append(out, "POWERSHELL_TELEMETRY_OPTOUT=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1")
	sort.Strings(out)
	return out
}

func recognizedPythonProject(dir string) bool {
	return recognizedProject(dir, "python")
}
func exists(name string) bool { _, err := os.Lstat(name); return err == nil }

func canonicalExistingFile(name string) string {
	if !exists(name) {
		return ""
	}
	name, err := filepath.EvalSymlinks(name)
	if err != nil {
		return ""
	}
	info, err := os.Stat(name)
	if err != nil || info.IsDir() {
		return ""
	}
	return name
}

func pyvenvConfig(executable string) string {
	for d := filepath.Dir(executable); ; d = filepath.Dir(d) {
		if cfg := canonicalExistingFile(filepath.Join(d, "pyvenv.cfg")); cfg != "" {
			return cfg
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

// resolveToolExecutable is resolveTool for callers that can only carry a
// single program: an argv with leading arguments is reported as absent.
func resolveToolExecutable(env map[string]string, name string) string {
	argv, _, err := resolveTool(env, name)
	if err != nil || len(argv) != 1 {
		return ""
	}
	return argv[0]
}

func projectPythonRuntime(root string, env map[string]string) (string, string) {
	for _, n := range []string{".venv", "venv", "env", ".env"} {
		d := filepath.Join(root, n)
		if exists(filepath.Join(d, "pyvenv.cfg")) {
			for _, exe := range []string{filepath.Join(d, "bin", "python3"), filepath.Join(d, "bin", "python"), filepath.Join(d, "Scripts", "python.exe")} {
				if exists(exe) {
					return exe, "selected nearest Python project runtime"
				}
			}
		}
	}
	for _, name := range []string{".python-version", "runtime.txt"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		version := strings.Fields(string(data))
		if len(version) == 0 {
			continue
		}
		version[0] = strings.TrimPrefix(version[0], "python-")
		version[0] = strings.TrimPrefix(version[0], "python")
		if executable := resolveToolExecutable(env, "python"+version[0]); executable != "" {
			return executable, "selected nearest Python runtime metadata"
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".tool-versions")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "python" {
				if executable := resolveToolExecutable(env, "python"+fields[1]); executable != "" {
					return executable, "selected nearest Python runtime metadata"
				}
			}
		}
	}
	return "", ""
}
func activePythonRuntime(env map[string]string) string {
	for _, key := range []string{"VIRTUAL_ENV", "CONDA_PREFIX"} {
		if d := env[key]; d != "" {
			for _, n := range []string{"bin/python3", "bin/python", "Scripts/python.exe"} {
				if exists(filepath.Join(d, n)) {
					return filepath.Join(d, n)
				}
			}
		}
	}
	return ""
}
func resolveRuntime(root, value string, env map[string]string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("polyglot: empty Python runtime")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return canonicalExecutable(value, env)
}
func canonicalExecutable(name string, env map[string]string) (string, error) {
	return canonicalExecutableFor(name, runtime.GOOS, environmentValue(env, "PATHEXT"))
}
func canonicalExecutableFor(name, goos, pathExt string) (string, error) {
	name, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	// Preserve the selected executable path. Virtual environments commonly
	// expose bin/python as a symlink; resolving it to the base interpreter
	// discards the venv prefix and therefore its site-packages. os.Stat and the
	// fingerprint still follow the link when validating its target.
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return "", err
	}
	name = filepath.Join(parent, filepath.Base(name))
	info, err := os.Stat(name)
	if err != nil {
		// A Windows App Execution Alias is a reparse point whose target Stat
		// may refuse to resolve; Lstat still classifies it, and the launch
		// path knows to start it through cmd.exe.
		if goos != "windows" {
			return "", err
		}
		linfo, lerr := os.Lstat(name)
		if lerr != nil || linfo.Mode()&os.ModeIrregular == 0 {
			return "", err
		}
		info = linfo
	}
	if pythonStoreAlias(name, info.Size(), goos) {
		return "", fmt.Errorf("polyglot: runtime %s is a Microsoft Store Python execution alias, not an installed interpreter", name)
	}
	if !executableFileMode(name, info.Mode(), goos, pathExt) {
		return "", fmt.Errorf("polyglot: runtime %s is not an executable file", name)
	}
	return name, nil
}

// Store's Python placeholders are zero-byte reparse points, not interpreters.
// Normalize Windows spelling independently of the host so this selection rule
// can be tested on every OS. Other app aliases and installed Python binaries
// retain the existing executable checks.
func pythonStoreAlias(name string, size int64, goos string) bool {
	if goos != "windows" || size != 0 {
		return false
	}
	name = strings.ToLower(strings.ReplaceAll(name, `\`, "/"))
	for _, base := range []string{"python.exe", "python3.exe"} {
		if strings.HasSuffix(name, "/appdata/local/microsoft/windowsapps/"+base) {
			return true
		}
	}
	return false
}

func executableFileMode(name string, mode os.FileMode, goos, pathExt string) bool {
	if goos != "windows" {
		if !mode.IsRegular() {
			return false
		}
		return mode.Perm()&0o111 != 0
	}
	// Runnable App Execution Aliases are reported as irregular and launched
	// through cmd.exe. canonicalExecutableFor rejects Python Store placeholders.
	if !mode.IsRegular() && mode&os.ModeIrregular == 0 {
		return false
	}
	ext := filepath.Ext(name)
	for _, allowed := range windowsExecutableExtensions(pathExt) {
		if strings.EqualFold(ext, allowed) {
			return true
		}
	}
	return false
}

func envMap(entries []string) map[string]string {
	if len(entries) == 0 {
		entries = os.Environ()
	}
	out := map[string]string{}
	for _, e := range entries {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}
func lookupPath(env map[string]string, file string) (string, error) {
	return lookupPathFor(env, file, runtime.GOOS)
}

func lookupPathFor(env map[string]string, file, goos string) (string, error) {
	for _, d := range filepath.SplitList(environmentValue(env, "PATH")) {
		if d == "" {
			continue
		}
		for _, candidate := range executableNames(file, goos, environmentValue(env, "PATHEXT")) {
			n := filepath.Join(d, candidate)
			if executable, err := canonicalExecutableFor(n, goos, environmentValue(env, "PATHEXT")); err == nil {
				return executable, nil
			}
		}
	}
	return "", os.ErrNotExist
}

// pythonPATHRuntimes is the PATH fallback order per platform. On Windows the
// launcher `py` comes first: the bare python3/python names there are commonly
// App Execution Aliases (store reparse points os/exec cannot start) rather
// than real installs, while `py` is a real executable that fronts one.
func pythonPATHRuntimes(goos string) []string {
	if goos == "windows" {
		return []string{"py", "python", "python3"}
	}
	return []string{"python3"}
}

func lookupPythonRuntime(env map[string]string, goos string) (string, error) {
	err := error(os.ErrNotExist)
	for _, name := range pythonPATHRuntimes(goos) {
		var executable string
		if executable, err = lookupPathFor(env, name, goos); err == nil {
			return executable, nil
		}
	}
	return "", err
}

func environmentValue(env map[string]string, key string) string {
	if value := env[key]; value != "" || runtime.GOOS != "windows" {
		return value
	}
	for candidate, value := range env {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return ""
}

func executableNames(file, goos, pathExt string) []string {
	if goos != "windows" || filepath.Ext(file) != "" {
		return []string{file}
	}
	names := make([]string, 0, 4)
	for _, ext := range windowsExecutableExtensions(pathExt) {
		names = append(names, file+ext)
	}
	return names
}

func windowsExecutableExtensions(pathExt string) []string {
	if pathExt == "" {
		pathExt = ".COM;.EXE;.BAT;.CMD"
	}
	var extensions []string
	for _, ext := range strings.Split(pathExt, ";") {
		if ext = strings.TrimSpace(ext); ext != "" {
			extensions = append(extensions, ext)
		}
	}
	return extensions
}
func canonicalPythonPath(value, root string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var out []string
	for _, p := range filepath.SplitList(value) {
		if p == "" {
			return nil, fmt.Errorf("polyglot: empty PYTHONPATH entry is not allowed")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		p, err = filepath.EvalSymlinks(p)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func launchEnvironment(env map[string]string, paths []string) []string {
	keep := map[string]string{"PYTHONNOUSERSITE": "1"}
	if len(paths) > 0 {
		keep["PYTHONPATH"] = strings.Join(paths, string(os.PathListSeparator))
	}
	for _, k := range []string{"PATH", "VIRTUAL_ENV", "CONDA_PREFIX"} {
		if env[k] != "" {
			keep[k] = env[k]
		}
	}
	keys := make([]string, 0, len(keep))
	for k := range keep {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+keep[k])
	}
	return out
}

var typeScriptProjectMetadata = []string{"package.json", "tsconfig.json", "package-lock.json", "pnpm-lock.yaml", "bun.lock", "bun.lockb"}

func discoverTypeScriptEnvironment(plan EnvironmentPlan, dirs []string, _ *environmentOverlay, env map[string]string) (EnvironmentPlan, error) {
	var err error
	for _, dir := range dirs {
		for _, name := range []string{"package.json", "tsconfig.json"} {
			if file := canonicalExistingFile(filepath.Join(dir, name)); file != "" && !containsString(plan.Manifests, file) {
				plan.Manifests = append(plan.Manifests, file)
			}
		}
	}
	locks := []struct{ name, manager string }{
		{"package-lock.json", "npm"}, {"pnpm-lock.yaml", "pnpm"}, {"bun.lock", "bun"}, {"bun.lockb", "bun"},
	}
	for _, dir := range dirs {
		for _, candidate := range locks {
			if file := canonicalExistingFile(filepath.Join(dir, candidate.name)); file != "" {
				plan.Locks = append(plan.Locks, file)
				if plan.Manager != "" && plan.Manager != candidate.manager {
					return EnvironmentPlan{}, fmt.Errorf("polyglot: conflicting TypeScript manager lockfiles in %s", plan.Root)
				}
				plan.Manager = candidate.manager
			}
		}
	}
	for _, file := range append(append([]string(nil), plan.Manifests...), plan.Locks...) {
		if !containsString(plan.ResolutionFiles, file) {
			plan.ResolutionFiles = append(plan.ResolutionFiles, file)
		}
	}
	for _, manifest := range plan.Manifests {
		if filepath.Base(manifest) != "package.json" {
			continue
		}
		var pkg struct {
			PackageManager string            `json:"packageManager"`
			Engines        map[string]string `json:"engines"`
		}
		data, readErr := os.ReadFile(manifest)
		if readErr != nil {
			return EnvironmentPlan{}, readErr
		}
		if jsonErr := json.Unmarshal(data, &pkg); jsonErr != nil {
			return EnvironmentPlan{}, fmt.Errorf("polyglot: parse %s: %w", manifest, jsonErr)
		}
		if plan.Manager == "" {
			manager, _, _ := strings.Cut(pkg.PackageManager, "@")
			switch manager {
			case "npm", "pnpm", "bun":
				plan.Manager = manager
			}
		}
		if plan.RuntimeConstraint == "" {
			plan.RuntimeConstraint = pkg.Engines["node"]
		}
	}
	runtimeName := strings.ToLower(strings.TrimSpace(env["BASHPP_TYPESCRIPT_RUNTIME"]))
	if runtimeName == "" {
		runtimeName = "node"
	}
	if runtimeName != "node" && runtimeName != "bun" {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: unsupported TypeScript runtime %q", runtimeName)
	}
	plan.Runtime = runtimeName
	requested := runtimeName
	overridden := false
	if override := env[map[string]string{"node": "BASHPP_NODE", "bun": "BASHPP_BUN"}[runtimeName]]; override != "" {
		requested = override
		overridden = true
		plan.Explanation = append(plan.Explanation, "runtime executable overridden")
	}
	if overridden {
		plan.Executable, err = resolveOverride(env, requested)
	} else if filepath.IsAbs(requested) {
		plan.Executable, err = canonicalExecutable(requested, env)
	} else if argv, why, rerr := resolveTool(env, requested); rerr == nil {
		if err = plan.applyTool(argv, env); err == nil {
			plan.Explanation = append(plan.Explanation, why)
		}
	} else {
		err = rerr
	}
	if err != nil {
		return EnvironmentPlan{}, fmt.Errorf("polyglot: TypeScript %s runtime unavailable: %w", runtimeName, err)
	}
	if override := env["BASHPP_TYPESCRIPT_MODULE"]; override != "" {
		plan.CompilerModule = override
		plan.Explanation = append(plan.Explanation, "compiler module overridden")
	} else {
		for _, d := range dirs {
			packageFile := filepath.Join(d, "node_modules", "typescript", "package.json")
			if canonicalExistingFile(packageFile) != "" {
				plan.CompilerModule, err = filepath.EvalSymlinks(filepath.Dir(packageFile))
				if err != nil {
					return EnvironmentPlan{}, err
				}
				plan.ResolutionFiles = append(plan.ResolutionFiles, canonicalExistingFile(packageFile))
				plan.Explanation = append(plan.Explanation, "selected project-local official TypeScript compiler")
				break
			}
		}
		if plan.CompilerModule == "" && ToolResolver != nil {
			// No project-local compiler: the same resolver that answered the
			// runtime answers the module — a provisioned, pinned typescript
			// package directory — so the island never depends on a global
			// npm install.
			argv, why, rerr := ToolResolver("typescript")
			if rerr != nil {
				return EnvironmentPlan{}, fmt.Errorf("polyglot: TypeScript compiler module unavailable: %w", rerr)
			}
			if len(argv) != 1 {
				return EnvironmentPlan{}, fmt.Errorf("polyglot: TypeScript compiler module: %q is not a single package directory", argv)
			}
			plan.CompilerModule = argv[0]
			if why == "" {
				why = "selected provisioned TypeScript compiler"
			}
			plan.Explanation = append(plan.Explanation, why)
		}
		if plan.CompilerModule == "" {
			plan.CompilerModule = "typescript"
			plan.Explanation = append(plan.Explanation, "selected default official TypeScript compiler")
		}
	}
	plan.Env = typeScriptLaunchEnvironment(env)
	// Node initializes its default timezone and Intl locale before evaluating
	// the worker. Preserve only those caller-selected startup inputs, including
	// empty versus absent, and fingerprint their exact launch-plan values.
	// Keep this addition local: the shared launch helper also serves native
	// toolchains, and arbitrary environment/Node loader settings stay excluded.
	for _, key := range []string{"TZ", "LANG", "LC_ALL", "LC_CTYPE"} {
		if value, present := env[key]; present {
			plan.Env = append(plan.Env, key+"="+value)
		}
	}
	sort.Strings(plan.Env)
	plan.Fingerprint, err = environmentFingerprint(plan)
	if err != nil {
		return EnvironmentPlan{}, err
	}
	return plan.Clone(), nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func typeScriptLaunchEnvironment(env map[string]string) []string {
	var out []string
	// Beyond the portable set, the Windows entries let a toolchain child run
	// its own discovery: rustc locates MSVC's link.exe through the VS
	// installer under ProgramFiles(x86), and without it falls back to a bare
	// PATH lookup of "link.exe" — which a GNU coreutils link (Git for
	// Windows' usr/bin) then shadows. On other hosts these keys are simply
	// absent. PATHEXT and ComSpec keep name resolution and cmd.exe launches
	// working in the same child. HOME and the XDG/zig cache keys let a
	// provisioned zig cc find its own cache directory (it refuses to run
	// without one: "AppDataDirUnavailable").
	for _, key := range []string{"PATH", "HOME", "SystemRoot", "TMPDIR", "TEMP", "TMP",
		"XDG_CACHE_HOME", "ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR",
		"PATHEXT", "ComSpec", "SystemDrive", "windir",
		"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData",
		"APPDATA", "LOCALAPPDATA", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		if value := environmentValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	sort.Strings(out)
	return out
}

// nativeLaunchEnvironment is the compiler-child environment: the toolchain
// set above plus the MSVC Developer-prompt variables, so a vcvarsall session's
// INCLUDE/LIB reach clang's own MSVC driver and the linker it invokes.
func nativeLaunchEnvironment(env map[string]string) []string {
	out := typeScriptLaunchEnvironment(env)
	for _, key := range []string{"INCLUDE", "LIB", "LIBPATH"} {
		if value := environmentValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	sort.Strings(out)
	return out
}

func environmentFingerprint(p EnvironmentPlan) (string, error) {
	h := sha256.New()
	write := func(s string) { _, _ = io.WriteString(h, s+"\x00") }
	write(p.Language)
	if p.RustTarget != "" {
		write(p.RustTarget)
	}
	if p.CSharpAssemblies != "" {
		write(p.CSharpAssemblies)
	}
	write(p.Root)
	write(p.Dir)
	write(p.SourceDir)
	write(p.Manager)
	write(p.RuntimeConstraint)
	write(p.Runtime)
	write(p.CompilerModule)
	write(runtime.GOOS)
	write(runtime.GOARCH)
	for _, s := range p.ExecutableArgs {
		write(s)
	}
	for _, s := range p.Manifests {
		write(s)
	}
	for _, s := range p.Locks {
		write(s)
	}
	for _, s := range p.PythonPath {
		write(s)
	}
	for _, s := range p.Env {
		write(s)
	}
	if err := fingerprintFile(h, p.Executable); err != nil {
		return "", err
	}
	// Project and overlay contents are resolution inputs too. Recording their
	// bytes makes a cached plan expire when a lock file or declared runtime is
	// edited, without exposing their contents in Explanation.
	for _, file := range p.ResolutionFiles {
		if err := fingerprintFile(h, file); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func fingerprintFile(h io.Writer, name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	_, _ = io.WriteString(h, name+"\x00"+fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())+"\x00")
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}

var pythonProjectMetadata = []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "requirements-dev.txt", "Pipfile", "poetry.lock", "uv.lock", "pdm.lock", "Pipfile.lock", ".python-version", "runtime.txt", ".tool-versions"}

type pythonMetadataPlan struct {
	manager           string
	runtimeConstraint string
	manifests         []string
	locks             []string
}

func discoverPythonMetadata(root string) (pythonMetadataPlan, error) {
	var result pythonMetadataPlan
	for _, name := range []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "requirements-dev.txt", "Pipfile"} {
		if file := canonicalExistingFile(filepath.Join(root, name)); file != "" {
			result.manifests = append(result.manifests, file)
		}
	}
	lockManagers := []struct{ name, manager string }{
		{"uv.lock", "uv"},
		{"poetry.lock", "poetry"},
		{"pdm.lock", "pdm"},
		{"Pipfile.lock", "pipenv"},
	}
	for _, candidate := range lockManagers {
		if file := canonicalExistingFile(filepath.Join(root, candidate.name)); file != "" {
			result.locks = append(result.locks, file)
			if result.manager != "" && result.manager != candidate.manager {
				return pythonMetadataPlan{}, fmt.Errorf("polyglot: conflicting Python manager lockfiles in %s", root)
			}
			result.manager = candidate.manager
		}
	}
	if result.manager == "" {
		result.manager = inferredPythonManager(root)
	}
	result.runtimeConstraint = pythonRuntimeConstraint(root)
	return result, nil
}

func inferredPythonManager(root string) string {
	if exists(filepath.Join(root, "Pipfile")) {
		return "pipenv"
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		text := string(data)
		for _, candidate := range []struct{ marker, manager string }{
			{"[tool.uv", "uv"},
			{"[tool.poetry", "poetry"},
			{"[tool.pdm", "pdm"},
		} {
			if strings.Contains(text, candidate.marker) {
				return candidate.manager
			}
		}
	}
	if exists(filepath.Join(root, "requirements.txt")) || exists(filepath.Join(root, "requirements-dev.txt")) {
		return "pip"
	}
	if exists(filepath.Join(root, "setup.py")) || exists(filepath.Join(root, "setup.cfg")) {
		return "setuptools"
	}
	return ""
}

func pythonRuntimeConstraint(root string) string {
	for _, name := range []string{".python-version", "runtime.txt"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			if fields := strings.Fields(string(data)); len(fields) > 0 {
				return strings.TrimPrefix(strings.TrimPrefix(fields[0], "python-"), "python")
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, ".tool-versions")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "python" {
				return fields[1]
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "requires-python" {
				return strings.Trim(strings.TrimSpace(value), "\"'")
			}
		}
	}
	return ""
}

func readOverlay(name string) ([]environmentOverlay, error) {
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(name, ".json") {
		return parseOverlayJSON(data, name)
	}
	return parseOverlayYAML(data, name)
}
func parseOverlayJSON(data []byte, file string) ([]environmentOverlay, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("polyglot: parse %s: %w", file, err)
	}
	return overlayObjects(raw, file)
}

// parseOverlayYAML accepts the small mapping subset needed for declarative
// environment files. JSON remains available for nested or generated config.
func parseOverlayYAML(data []byte, file string) ([]environmentOverlay, error) {
	lines := strings.Split(string(data), "\n")
	// Named mappings are the usual convenient YAML spelling:
	// environments:\n  dev:\n    language: python\n    runtime: .venv/bin/python
	var named []environmentOverlay
	inEnvironments, currentIndent := false, -1
	var current *environmentOverlay
	for _, raw := range lines {
		withoutComment := yamlWithoutComment(raw)
		indent := len(withoutComment) - len(strings.TrimLeft(withoutComment, " \t"))
		line := strings.TrimSpace(withoutComment)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("polyglot: parse %s: expected key: value", file)
		}
		key, value = strings.TrimSpace(key), strings.Trim(strings.TrimSpace(value), "\"'")
		if key == "environments" && value == "" {
			inEnvironments = true
			current = nil
			continue
		}
		if !inEnvironments {
			continue
		}
		if value == "" && (current == nil || indent <= currentIndent) {
			named = append(named, environmentOverlay{Name: key})
			current = &named[len(named)-1]
			currentIndent = indent
			continue
		}
		if current != nil && indent > currentIndent {
			switch key {
			case "language":
				current.Language = value
			case "runtime", "python", "environment":
				current.Runtime = value
			case "name":
				current.Name = value
			case "csharp_assemblies":
				current.CSharpAssemblies = value
			case "native_modules":
				current.NativeModules = value
			case "rust_context":
				current.RustContext = value
			}
			continue
		}
	}
	if len(named) > 0 {
		for _, o := range named {
			if o.Runtime == "" {
				return nil, fmt.Errorf("polyglot: environment %q in %s has no runtime", o.Name, file)
			}
		}
		return named, nil
	}
	values := map[string]string{}
	section := ""
	for _, line := range lines {
		line = strings.TrimSpace(yamlWithoutComment(line))
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("polyglot: parse %s: expected key: value", file)
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), "\"'")
		if v == "" {
			section = k
			continue
		}
		if section != "" && k != "environment" && k != "runtime" && k != "language" && k != "name" {
			section = ""
		}
		values[k] = v
	}
	if len(values) == 0 {
		return nil, nil
	}
	return []environmentOverlay{{Name: values["name"], Language: values["language"], NativeModules: values["native_modules"], CSharpAssemblies: values["csharp_assemblies"], RustContext: values["rust_context"], Runtime: first(values["runtime"], values["python"], values["environment"])}}, nil
}

func yamlWithoutComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if c == quote {
				if i+1 < len(line) && line[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			return line[:i]
		}
	}
	return line
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func overlayObjects(raw any, file string) ([]environmentOverlay, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("polyglot: %s must be an object", file)
	}
	var objects []any
	if v, ok := m["environments"]; ok {
		switch x := v.(type) {
		case []any:
			objects = x
		case map[string]any:
			for name, val := range x {
				o, ok := val.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("polyglot: invalid environment in %s", file)
				}
				o["name"] = name
				objects = append(objects, o)
			}
		default:
			return nil, fmt.Errorf("polyglot: invalid environments in %s", file)
		}
	} else {
		objects = []any{m}
	}
	var out []environmentOverlay
	for _, raw := range objects {
		o, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("polyglot: invalid environment in %s", file)
		}
		if value, exists := o["csharp_assemblies"]; exists {
			if path, ok := value.(string); !ok || path == "" {
				return nil, fmt.Errorf("polyglot: csharp_assemblies must be a nonempty manifest path")
			}
		}
		str := func(k string) string { v, _ := o[k].(string); return v }
		runtime := first(str("runtime"), str("python"), str("environment"))
		if runtime == "" {
			return nil, fmt.Errorf("polyglot: environment in %s has no runtime", file)
		}
		out = append(out, environmentOverlay{Name: str("name"), Language: str("language"), Runtime: runtime, NativeModules: str("native_modules"), CSharpAssemblies: str("csharp_assemblies"), RustContext: str("rust_context")})
	}
	return out, nil
}
