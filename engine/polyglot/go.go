package polyglot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Go compiles declaration-only Go fences with the checkout's selected Go
// command. The generated files exist in the module only through -overlay.
type Go struct {
	Command     string
	Environment *EnvironmentPlan
	// AssetSourceDir is the authenticated parent of the block that declared
	// go:embed. Prepare sets it from block origins when they are unambiguous.
	AssetSourceDir string
	Cwd            func() string
	Env            func() []string
}

func (g Go) executable() string {
	if g.Environment != nil && g.Environment.Executable != "" {
		return g.Environment.Executable
	}
	if g.Command != "" {
		return g.Command
	}
	return "go"
}
func (Go) arguments(Plan) []string         { return nil }
func (Go) loadRequest(Plan) map[string]any { return nil }
func (Go) name() string                    { return "Go" }
func (g Go) configure(cmd *exec.Cmd) {
	if g.Environment != nil {
		cmd.Dir = g.Environment.Dir
		cmd.Env = append([]string(nil), g.Environment.Env...)
	}
}

// GoPackageName reports the package clause of unchanged Go source.
func GoPackageName(source string) (string, bool) {
	file, err := parser.ParseFile(token.NewFileSet(), "fence.go", source, parser.PackageClauseOnly)
	if err != nil {
		return "", false
	}
	return file.Name.Name, true
}

// RunProgram compiles and runs an unchanged package-main fence. The build has
// no stdin; the resulting program receives the caller's arguments and streams.
func (g Go) RunProgram(ctx context.Context, source string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if name, ok := GoPackageName(source); !ok || name != "main" {
		return 2, errors.New("Go program fence requires package main")
	}
	dir, err := os.MkdirTemp("", "bashpp-go-program-")
	if err != nil {
		return 2, err
	}
	defer os.RemoveAll(dir)
	backing := filepath.Join(dir, "main.go")
	overlayFile := filepath.Join(dir, "overlay.json")
	output := filepath.Join(dir, "program")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	root := ""
	if g.Environment != nil {
		root = g.Environment.Root
	}
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return 2, err
		}
	}
	root = filepath.Clean(root)
	virtual := filepath.Join(root, ".bashpp-overlay-program", "main.go")
	if err := os.WriteFile(backing, []byte(source), 0o600); err != nil {
		return 2, err
	}
	replace := map[string]string{virtual: backing}
	buildArgs := []string{"build", "-overlay=" + overlayFile, "-o", output, virtual}
	if g.Environment != nil && g.Environment.ModuleFile != "" {
		modFile := g.Environment.ModuleFile
		sumFile := strings.TrimSuffix(modFile, ".mod") + ".sum"
		if _, err := os.Stat(sumFile); err != nil {
			if err := os.WriteFile(sumFile, nil, 0o644); err != nil {
				return 2, err
			}
		}
		replace[filepath.Join(root, "go.mod")] = modFile
		replace[filepath.Join(root, "go.sum")] = sumFile
		buildArgs = []string{"build", "-overlay=" + overlayFile, "-modfile=" + modFile, "-o", output, virtual}
	}
	overlay, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		return 2, err
	}
	if err := os.WriteFile(overlayFile, overlay, 0o600); err != nil {
		return 2, err
	}
	if g.Environment != nil && g.Environment.Manager == "bashy" {
		buildArgs = append([]string{"go"}, buildArgs...)
	}
	buildArgs = append(leadingArgs(g.Environment), buildArgs...)
	build := exec.CommandContext(ctx, g.executable(), buildArgs...)
	g.configure(build)
	build.Stderr = stderr
	if err := build.Run(); err != nil {
		return goProcessStatus(err)
	}
	run := exec.CommandContext(ctx, output, args...)
	if g.Cwd != nil {
		run.Dir = g.Cwd()
	}
	if g.Env != nil {
		run.Env = g.Env()
	}
	run.Stdin, run.Stdout, run.Stderr = stdin, stdout, stderr
	if err := run.Run(); err != nil {
		return goProcessStatus(err)
	}
	return 0, nil
}

func goProcessStatus(err error) (int, error) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 2, err
}

type goExport struct {
	Export
	params      []string
	result      string
	resultError bool
}

func (g Go) Analyze(ctx context.Context, source string) ([]Export, error) {
	exports, _, err := g.AnalyzeArtifact(ctx, source)
	return exports, err
}

func (g Go) AnalyzeArtifact(ctx context.Context, source string) ([]Export, string, error) {
	moduleSource, parsed, err := analyzeGoModule(source)
	if err != nil {
		return nil, "", err
	}
	workerSource := goWorkerSource(parsed)
	dir, err := os.MkdirTemp("", "bashpp-go-build-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	moduleBacking := filepath.Join(dir, "module.go")
	workerBacking := filepath.Join(dir, "worker.go")
	overlayFile := filepath.Join(dir, "overlay.json")
	output := filepath.Join(dir, "module")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	root := ""
	if g.Environment != nil {
		root = g.Environment.Root
	}
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return nil, "", err
		}
	}
	// The go command walks the working directory as $PWD spells it, and an
	// overlay key must match that spelling byte for byte: one cleaned
	// spelling serves as the root, the process directory and its PWD.
	root = filepath.Clean(root)
	hash := strconv.FormatInt(int64(len(source)), 10)
	// Use a virtual subdirectory so a module whose root is a library package
	// does not collide with the generated package main. Keeping it beneath the
	// module still permits imports of that module's internal packages.
	virtualDir := filepath.Join(root, ".bashpp-overlay-"+hash)
	moduleVirtual := filepath.Join(virtualDir, "fence.go")
	workerVirtual := filepath.Join(virtualDir, "worker.go")
	for name, data := range map[string]string{moduleBacking: moduleSource, workerBacking: workerSource} {
		if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
			return nil, "", err
		}
	}
	replace := map[string]string{
		moduleVirtual: moduleBacking,
		workerVirtual: workerBacking,
	}
	if g.Environment != nil {
		sourceDir := g.AssetSourceDir
		if sourceDir == "" {
			sourceDir = g.Environment.SourceDir
		}
		assets, err := stageGoEmbedAssets(moduleSource, sourceDir, virtualDir, dir)
		if err != nil {
			return nil, "", err
		}
		for virtual, backing := range assets {
			replace[virtual] = backing
		}
	}
	args := []string{"build", "-overlay=" + overlayFile, "-o", output, moduleVirtual, workerVirtual}
	if g.Environment != nil && g.Environment.ModuleFile != "" {
		// A gomod fence is the module: present it (and its go.sum) at the
		// root and read/write the real files beside the fence.
		modFile := g.Environment.ModuleFile
		sumFile := strings.TrimSuffix(modFile, ".mod") + ".sum"
		if _, err := os.Stat(sumFile); err != nil {
			if err := os.WriteFile(sumFile, nil, 0o644); err != nil {
				return nil, "", err
			}
		}
		replace[filepath.Join(root, "go.mod")] = modFile
		replace[filepath.Join(root, "go.sum")] = sumFile
		args = append([]string{"build", "-overlay=" + overlayFile, "-modfile=" + modFile, "-o", output, moduleVirtual, workerVirtual}, args[len(args):]...)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(overlayFile, overlay, 0o600); err != nil {
		return nil, "", err
	}
	if g.Environment != nil && g.Environment.Manager == "bashy" {
		args = append([]string{"go"}, args...)
	}
	args = append(leadingArgs(g.Environment), args...)
	cmd := exec.CommandContext(ctx, g.executable(), args...)
	g.configure(cmd)
	if g.Environment != nil && g.Environment.ModuleFile != "" {
		cmd.Dir = root
		cmd.Env = append(cmd.Env, "PWD="+root)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("Go toolchain unavailable: %w", err)
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, "", errors.New(message)
	}
	binary, err := os.ReadFile(output)
	if err != nil {
		return nil, "", err
	}
	exports := make([]Export, len(parsed))
	for i := range parsed {
		exports[i] = parsed[i].Export
	}
	return exports, string(binary), nil
}

func analyzeGoExports(source string) ([]goExport, error) {
	_, exports, err := analyzeGoModule(source)
	return exports, err
}

func analyzeGoModule(source string) (string, []goExport, error) {
	fset := token.NewFileSet()
	moduleSource := "package main\n" + source
	// An unchanged library file may enter through an inline fence or embed.
	// Compile it into the worker's package main while retaining its declarations
	// and source-line shape; package main remains reserved for program entry.
	if original, err := parser.ParseFile(fset, "fence.go", source, parser.PackageClauseOnly); err == nil && original.Name.Name != "main" {
		start := fset.Position(original.Name.Pos()).Offset
		end := fset.Position(original.Name.End()).Offset
		moduleSource = source[:start] + "main" + source[end:]
	}
	fset = token.NewFileSet()
	file, err := parser.ParseFile(fset, "fence.go", moduleSource, parser.AllErrors)
	if err != nil {
		return "", nil, err
	}
	var out []goExport
	seen := map[string]bool{}
decls:
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			// Imports, types, variables and constants remain in the module.
			// Only top-level functions contribute Bash# callable exports.
			continue
		case *ast.FuncDecl:
			if decl.Recv != nil {
				continue
			}
			if decl.Name.Name == "main" || strings.HasPrefix(decl.Name.Name, "__bpp") {
				return "", nil, fmt.Errorf("Go function name %s is reserved by the fence worker", decl.Name.Name)
			}
			if !ast.IsExported(decl.Name.Name) {
				continue
			}
			if seen[decl.Name.Name] {
				return "", nil, fmt.Errorf("Go function %s is exported more than once", decl.Name.Name)
			}
			seen[decl.Name.Name] = true
			export := goExport{Export: Export{Name: decl.Name.Name}}
			reject := func(reason string) {
				out = append(out, goExport{Export: Export{Name: decl.Name.Name, Unavailable: reason}})
			}
			if decl.Type.TypeParams != nil && len(decl.Type.TypeParams.List) > 0 {
				reject("generic type parameters")
				continue decls
			}
			for _, field := range decl.Type.Params.List {
				if _, ok := field.Type.(*ast.Ellipsis); ok {
					reject("variadic")
					continue decls
				}
				typ, bridge, ok := goBridgeType(field.Type)
				if !ok {
					reject("unsupported parameter type")
					continue decls
				}
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for range count {
					export.params = append(export.params, typ)
					export.Signature.Params = append(export.Signature.Params, bridge)
				}
			}
			var results []ast.Expr
			if decl.Type.Results != nil {
				for _, field := range decl.Type.Results.List {
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					for range count {
						results = append(results, field.Type)
					}
				}
			}
			if len(results) > 2 {
				reject("at most one value plus error")
				continue decls
			}
			if len(results) > 0 && isGoIdent(results[len(results)-1], "error") {
				export.resultError = true
				results = results[:len(results)-1]
			}
			if len(results) > 1 {
				reject("at most one value plus error")
				continue decls
			}
			if len(results) == 1 {
				typ, bridge, ok := goBridgeType(results[0])
				if !ok || bridge == "nil" {
					reject("unsupported result type")
					continue decls
				}
				export.result = typ
				export.Signature.Results = []string{bridge}
			}
			out = append(out, export)
		}
	}
	return moduleSource, out, nil
}

func goBridgeType(expr ast.Expr) (string, string, bool) {
	if id, ok := expr.(*ast.Ident); ok {
		switch id.Name {
		case "bool", "string":
			return id.Name, id.Name, true
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
			return id.Name, "int", true
		case "float32", "float64":
			return id.Name, "float64", true
		}
	}
	if array, ok := expr.(*ast.ArrayType); ok && array.Len == nil && isGoIdent(array.Elt, "byte") {
		return "[]byte", "bytes", true
	}
	return "", "", false
}

func isGoIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

func goWorkerSource(exports []goExport) string {
	var out strings.Builder
	out.WriteString(`package main

import (
	__bpp_json "encoding/json"
	__bpp_fmt "fmt"
	__bpp_os "os"
)

type __bpp_request struct { Name string ` + "`json:\"name\"`" + `; Args []__bpp_json.RawMessage ` + "`json:\"args\"`" + ` }
type __bpp_response struct { OK bool ` + "`json:\"ok\"`" + `; Kind string ` + "`json:\"kind,omitempty\"`" + `; Result any ` + "`json:\"result,omitempty\"`" + `; Error string ` + "`json:\"error,omitempty\"`" + ` }
var __bpp_resultPath string
func __bpp_write(response __bpp_response) { data,_:=__bpp_json.Marshal(response); _=__bpp_os.WriteFile(__bpp_resultPath,data,0600) }
func __bpp_fail(err error) { __bpp_write(__bpp_response{Error:err.Error()}) }
func main() {
	if name, ok := __bpp_os.LookupEnv("__BPP_GO_COMMAND"); ok {
		__bpp_os.Unsetenv("__BPP_GO_COMMAND")
		switch name {
`)
	for _, export := range exports {
		if export.Unavailable != "" || len(export.params) != 0 {
			continue
		}
		fmt.Fprintf(&out, "case %q:\n", export.Name)
		call := export.Name + "()"
		switch {
		case export.result != "" && export.resultError:
			fmt.Fprintf(&out, "_,err:=%s\nif err!=nil { __bpp_fmt.Fprintln(__bpp_os.Stderr,err); __bpp_os.Exit(1) }\n", call)
		case export.resultError:
			fmt.Fprintf(&out, "if err:=%s; err!=nil { __bpp_fmt.Fprintln(__bpp_os.Stderr,err); __bpp_os.Exit(1) }\n", call)
		case export.result != "":
			fmt.Fprintf(&out, "_=%s\n", call)
		default:
			fmt.Fprintf(&out, "%s\n", call)
		}
		out.WriteString("return\n")
	}
	out.WriteString(`default: __bpp_fmt.Fprintln(__bpp_os.Stderr,"unknown Go command export"); __bpp_os.Exit(127)
		}
	}
	__bpp_resultPath = __bpp_os.Getenv("__BPP_GO_RESULT")
	__bpp_os.Unsetenv("__BPP_GO_RESULT")
	defer func(){ if value:=recover(); value!=nil { __bpp_fail(__bpp_fmt.Errorf("Go function panicked: %v",value)) } }()
	var request __bpp_request
	if err:=__bpp_json.NewDecoder(__bpp_os.Stdin).Decode(&request); err!=nil { __bpp_fail(err); return }
	switch request.Name {
`)
	for _, export := range exports {
		fmt.Fprintf(&out, "case %q:\n", export.Name)
		if export.Unavailable != "" {
			fmt.Fprintf(&out, "__bpp_fail(__bpp_fmt.Errorf(%q)); return\n", export.UnavailableDiagnostic())
			continue
		}
		fmt.Fprintf(&out, "if len(request.Args)!=%d { __bpp_fail(__bpp_fmt.Errorf(\"Go function %s expects %d arguments, got %%d\",len(request.Args))); return }\n", len(export.params), export.Name, len(export.params))
		var args []string
		for i, typ := range export.params {
			name := fmt.Sprintf("__bpp_arg%d", i)
			fmt.Fprintf(&out, "var %s %s\nif err:=__bpp_json.Unmarshal(request.Args[%d],&%s); err!=nil { __bpp_fail(err); return }\n", name, typ, i, name)
			args = append(args, name)
		}
		call := export.Name + "(" + strings.Join(args, ",") + ")"
		switch {
		case export.result != "" && export.resultError:
			fmt.Fprintf(&out, "value,err:=%s\nif err!=nil { __bpp_fail(err); return }\n__bpp_write(__bpp_response{OK:true,Kind:%q,Result:value})\n", call, export.Signature.Results[0])
		case export.resultError:
			fmt.Fprintf(&out, "if err:=%s; err!=nil { __bpp_fail(err); return }\n__bpp_write(__bpp_response{OK:true,Kind:\"nil\"})\n", call)
		case export.result != "":
			fmt.Fprintf(&out, "value:=%s\n__bpp_write(__bpp_response{OK:true,Kind:%q,Result:value})\n", call, export.Signature.Results[0])
		default:
			fmt.Fprintf(&out, "%s\n__bpp_write(__bpp_response{OK:true,Kind:\"nil\"})\n", call)
		}
	}
	out.WriteString("default: __bpp_fail(__bpp_fmt.Errorf(\"unknown Go function %s\",request.Name))\n}\n}\n")
	return out.String()
}

func (m *Module) callGo(ctx context.Context, name string, args []any, kwargs map[string]any) (CallResult, error) {
	if len(kwargs) != 0 {
		return CallResult{}, errors.New("Go functions do not accept named arguments")
	}
	path, err := m.artifactPath("bashpp-go-run-")
	if err != nil {
		return CallResult{}, err
	}
	request, err := json.Marshal(map[string]any{"name": name, "args": args})
	if err != nil {
		return CallResult{}, err
	}
	controlDir, err := os.MkdirTemp("", "bashpp-go-control-")
	if err != nil {
		return CallResult{}, err
	}
	defer os.RemoveAll(controlDir)
	resultPath := filepath.Join(controlDir, "result.json")
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(goGuestEnv(os.Environ()), "__BPP_GO_RESULT="+resultPath)
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return CallResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
	frame, readErr := os.ReadFile(resultPath)
	if errors.Is(readErr, os.ErrNotExist) {
		if runErr != nil {
			var exitErr *exec.ExitError
			if ctx.Err() == nil && errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
				runErr = &WorkerExit{Runtime: "Go", Code: exitErr.ExitCode(), Err: runErr}
			}
			return CallResult{Stdout: stdout.String(), Stderr: stderr.String()}, fmt.Errorf("Go worker failed: %w", runErr)
		}
		return CallResult{Stdout: stdout.String(), Stderr: stderr.String()}, errors.New("Go worker returned no result frame")
	}
	result := CallResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if readErr != nil {
		return result, fmt.Errorf("read Go worker result frame: %w", readErr)
	}
	if runErr != nil {
		return result, fmt.Errorf("Go worker exited after result frame: %w", runErr)
	}
	var response struct {
		OK     bool            `json:"ok"`
		Kind   string          `json:"kind"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return result, fmt.Errorf("invalid Go worker result frame: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return result, errors.New("invalid Go worker result frame: trailing data")
	}
	if !response.OK {
		if response.Error == "" {
			return result, errors.New("invalid Go worker result frame: missing error")
		}
		return result, errors.New(response.Error)
	}
	switch response.Kind {
	case "nil":
	case "string":
		err = json.Unmarshal(response.Result, &result.Value)
	case "bytes":
		var encoded string
		if err = json.Unmarshal(response.Result, &encoded); err == nil {
			result.Value, err = base64.StdEncoding.DecodeString(encoded)
		}
	case "bool":
		var value bool
		err = json.Unmarshal(response.Result, &value)
		result.Value = value
	case "int":
		var value json.Number
		err = json.Unmarshal(response.Result, &value)
		if err == nil {
			result.Value, err = strconv.ParseInt(value.String(), 10, 64)
		}
	case "float64":
		var value float64
		err = json.Unmarshal(response.Result, &value)
		result.Value = value
	default:
		err = fmt.Errorf("unknown Go result type %q", response.Kind)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// commandGo invokes an exported zero-argument function as a shell command.
// Its process streams and exit code belong to the guest, including os.Exit and
// an unrecovered panic. The dispatch key never occupies guest stdin or argv.
func (m *Module) commandGo(ctx context.Context, name string, argv []string, stdin io.Reader) (CommandResult, error) {
	var stdout, stderr bytes.Buffer
	status, err := m.commandGoTo(ctx, name, argv, stdin, &stdout, &stderr)
	return CommandResult{Status: status, Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func (m *Module) commandGoTo(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	var export *Export
	for i := range m.plan.Exports {
		if m.plan.Exports[i].Name == name {
			export = &m.plan.Exports[i]
			break
		}
	}
	if export == nil {
		return 0, fmt.Errorf("%w: %s", ErrCommandNotFound, name)
	}
	if export.Unavailable != "" || len(export.Signature.Params) != 0 {
		return 0, fmt.Errorf("%w: Go command %s requires an exported zero-argument function", ErrCommandNotCallable, name)
	}
	path, err := m.artifactPath("bashpp-go-run-")
	if err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, path, argv...)
	// A shell command gets the invoking runner's snapshot. Lowered shell
	// regions share modules, so storing this state on Go would mix sessions.
	if caller, ok := ctx.Value(goCommandCallerKey{}).(goCommandCaller); ok {
		cmd.Dir = caller.dir
		cmd.Env = caller.env
	} else if runtime, ok := m.runtime.(Go); ok {
		if runtime.Cwd != nil {
			cmd.Dir = runtime.Cwd()
		}
		if runtime.Env != nil {
			cmd.Env = runtime.Env()
		}
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(goGuestEnv(cmd.Env), "__BPP_GO_COMMAND="+name)
	cmd.Stdin = stdin
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			if death := naturalWorkerDeath(exit.ProcessState); death.signal > 0 {
				return 128 + death.signal, nil
			}
			return exit.ExitCode(), nil
		}
		return 0, runErr
	}
	return 0, nil
}

type goCommandCallerKey struct{}
type goCommandCaller struct {
	dir string
	env []string
}

// WithGoCommandCaller binds the caller's current directory and exported
// environment to one Go command invocation. It has no effect on typed calls.
func WithGoCommandCaller(ctx context.Context, dir string, env []string) context.Context {
	return context.WithValue(ctx, goCommandCallerKey{}, goCommandCaller{dir: dir, env: append([]string{}, env...)})
}

func goGuestEnv(environ []string) []string {
	clean := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "__BPP_GO_COMMAND") && !strings.EqualFold(key, "__BPP_GO_RESULT") {
			clean = append(clean, entry)
		}
	}
	return clean
}

func (m *Module) artifactPath(prefix string) (string, error) {
	if m.tempDir == "" {
		dir, err := os.MkdirTemp("", prefix)
		if err != nil {
			return "", err
		}
		path := filepath.Join(dir, "module")
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		if err := os.WriteFile(path, []byte(m.plan.Artifact), 0o700); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		m.tempDir = dir
	}
	path := filepath.Join(m.tempDir, "module")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	return path, nil
}
