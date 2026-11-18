package interp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPForeignFunc struct {
	module    *polyglot.Module
	export    polyglot.Export
	qualified string
	direct    bool
	receiver  *polyglot.Handle
	argNames  []string
	argCells  []*bashPPCell
	call      *syntax.BashPPCall
}

func (r *Runner) bashPPPrepareSourceBlocks(ctx context.Context, file *syntax.File) (func(), error) {
	oldFuncs, oldModules, oldImports := r.bashPPForeignFuncs, r.bashPPForeignModules, r.bashPPForeignImports
	r.bashPPForeignFuncs = nil
	r.bashPPForeignModules = nil
	r.bashPPForeignImports = nil
	restore := func() {
		// Close only modules owned by this source unit. A nested `source` call
		// temporarily replaces the namespace but must leave its caller's
		// persistent workers alive when the nested file returns.
		for _, module := range r.bashPPForeignModules {
			_ = module.Close()
		}
		r.bashPPForeignFuncs, r.bashPPForeignModules, r.bashPPForeignImports = oldFuncs, oldModules, oldImports
	}
	if r.Dialect() != syntax.LangBashPP {
		return restore, nil
	}
	var blocks []polyglot.Block
	var imports []*syntax.BashPPImport
	runnerBlocks := map[string]*syntax.SourceBlock{}
	artifactSources := map[string]string{}
	for _, stmt := range file.Stmts {
		switch node := stmt.Cmd.(type) {
		case *syntax.SourceBlock:
			artifact := file.Name
			if node.Src != nil {
				artifact = filepath.Join(filepath.Dir(file.Name), node.Src.Value)
			}
			artifactSources[polyglot.CanonicalLanguage(node.Language.Value)] = artifact
			alias, runner := "", ""
			if node.Alias != nil {
				alias = node.Alias.Value
			}
			if node.Runner != nil {
				runner = node.Runner.Value
				runnerBlocks[polyglot.CanonicalLanguage(node.Language.Value)] = node
			}
			if artifact != "" && !filepath.IsAbs(artifact) {
				artifact = filepath.Join(r.Dir, artifact)
			}
			blocks = append(blocks, polyglot.Block{Language: node.Language.Value, Alias: alias, Runner: runner, Source: node.Body, Origin: artifact, Filename: file.Name, Line: int(node.BodyPos.Line())})
		case *syntax.BashPPImport:
			if node.Language != nil {
				imports = append(imports, node)
			}
		}
	}
	if len(blocks) == 0 && len(imports) == 0 {
		return restore, nil
	}
	source := file.Name
	if source == "" {
		source = filepath.Join(r.Dir, ".bashpp-stdin")
	} else if !filepath.IsAbs(source) {
		source = filepath.Join(r.Dir, source)
	}
	r.bashPPForeignImports = make(map[string]*polyglot.Module, len(imports))
	// One module identity — the same module or file in the same environment
	// — is one worker, however many aliases name it: CPython gives
	// `import x as a; import x as b` one module object, and a file's state
	// must not fork by alias.
	identities := map[string]*polyglot.Module{}
	for _, imp := range imports {
		modulePath, err := strconv.Unquote(`"` + imp.Path.Parts[0].(*syntax.Lit).Value + `"`)
		if err != nil {
			return restore, err
		}
		alias, _ := syntax.BashPPDerivedImportAlias(modulePath)
		if imp.Alias != nil {
			alias = imp.Alias.Value
		}
		environment := ""
		if imp.Environment != nil {
			environment = imp.Environment.Value
		}
		plan, err := polyglot.PlanImport(polyglot.ImportRequest{Source: source, Language: imp.Language.Value, Environment: environment, Module: modulePath, Alias: alias, Environ: nativeExecEnv(execEnv(r.writeEnv))})
		if err != nil {
			return restore, fmt.Errorf("%s: %w", file.Name, err)
		}
		module := identities[plan.Identity()]
		if module == nil {
			module = polyglot.StartImport(plan)
			identities[plan.Identity()] = module
			r.bashPPForeignModules = append(r.bashPPForeignModules, module)
		}
		r.bashPPForeignImports[alias] = module
	}
	if len(blocks) == 0 {
		r.bashPPForeignFuncs = map[string]*bashPPFunc{}
		return restore, nil
	}
	// One runtime per language per source unit, constructed from its row;
	// a language without a row reaches Prepare, which names it in its
	// refusal.
	runtimes := map[string]polyglot.LanguageRuntime{}
	analyzers := map[string]polyglot.Analyzer{}
	manifests, err := polyglot.ManifestFiles(blocks)
	if err != nil {
		return restore, fmt.Errorf("%s: %w", file.Name, err)
	}
	var agentBlocks []polyglot.Block
	var groupedBlocks []polyglot.Block
	for _, block := range blocks {
		language := polyglot.CanonicalLanguage(block.Language)
		if language == "go" {
			if packageName, ok := polyglot.GoPackageName(block.Source); ok && packageName == "main" {
				if block.Alias != "" {
					return restore, fmt.Errorf("%s: package main Go fence runs as a program and cannot have alias %s", file.Name, block.Alias)
				}
				environment, err := polyglot.DiscoverEnvironment(polyglot.EnvironmentRequest{
					Source: source, Language: language, Environ: nativeExecEnv(execEnv(r.writeEnv)), ModuleFile: manifests[language],
				})
				if err != nil {
					return restore, fmt.Errorf("%s: %w", file.Name, err)
				}
				program := polyglot.Go{Environment: &environment, Cwd: func() string { return r.Dir }, Env: func() []string { return execEnv(r.writeEnv) }}
				status, err := program.RunProgram(ctx, block.Source, r.Params, r.stdin, r.stdout, r.stderr)
				if err != nil {
					return restore, fmt.Errorf("%s: Go program fence: %w", file.Name, err)
				}
				if status != 0 {
					return restore, ExitStatus(status)
				}
				continue
			}
		}
		if language == "agent" && block.Runner == "" {
			agentBlocks = append(agentBlocks, block)
			continue
		}
		groupedBlocks = append(groupedBlocks, block)
		if _, done := runtimes[language]; done {
			continue
		}
		// A runner override processes the body whatever the type; a text
		// fence needs an alias since its methods are only known once the
		// runner has answered, after the parser has been.
		if node := runnerBlocks[language]; node != nil {
			if block.Alias == "" {
				return restore, fmt.Errorf("%s: runner fence %s needs an alias (as NAME)", file.Name, block.Language)
			}
			runtime := r.bashPPRunnerFence(ctx, file, node, language)
			runtimes[language] = runtime
			analyzers[language] = runtime
			continue
		}
		row, ok := polyglot.LookupLanguage(language)
		if !ok {
			continue
		}
		artifact := artifactSources[language]
		if artifact == "" {
			artifact = source
		} else if !filepath.IsAbs(artifact) {
			artifact = filepath.Join(r.Dir, artifact)
		}
		config := polyglot.RuntimeConfig{Source: artifact, Dir: r.Dir, Cwd: func() string { return r.Dir },
			Environ: execEnv(r.writeEnv), Env: func() []string { return execEnv(r.writeEnv) }}
		if row.NeedsEnvironment {
			environment, err := polyglot.DiscoverEnvironment(polyglot.EnvironmentRequest{
				Source: source, Language: language, Environ: nativeExecEnv(execEnv(r.writeEnv)), ModuleFile: manifests[language],
			})
			if err != nil {
				return restore, fmt.Errorf("%s: %w", file.Name, err)
			}
			config.Environment = &environment
		}
		if row.Text && block.Alias == "" {
			return restore, fmt.Errorf("%s: text fence %s needs an alias (as NAME)", file.Name, block.Language)
		}
		runtime := row.NewRuntime(config)
		runtimes[language] = runtime
		analyzers[language] = runtime
	}
	plans, err := polyglot.Prepare(ctx, groupedBlocks, analyzers)
	if err != nil {
		return restore, fmt.Errorf("%s: %w", file.Name, err)
	}
	// Agent definitions are independent sessions. Prepare each against its
	// own origin so imports and policy roots cannot bleed across embeds.
	agentRuntimes := map[string]polyglot.LanguageRuntime{}
	for _, block := range agentBlocks {
		if block.Alias == "" {
			return restore, fmt.Errorf("%s: text fence agent needs an alias (as NAME)", file.Name)
		}
		row, ok := polyglot.LookupLanguage("agent")
		if !ok {
			return restore, fmt.Errorf("%s: unsupported language agent", file.Name)
		}
		origin := block.Origin
		if origin == "" {
			origin = source
		}
		runtime := row.NewRuntime(polyglot.RuntimeConfig{Source: origin, Dir: r.Dir, Cwd: func() string { return r.Dir }, Environ: execEnv(r.writeEnv), Env: func() []string { return execEnv(r.writeEnv) }})
		individual, err := polyglot.Prepare(ctx, []polyglot.Block{block}, map[string]polyglot.Analyzer{"agent": runtime})
		if err != nil {
			return restore, fmt.Errorf("%s: %w", file.Name, err)
		}
		plans = append(plans, individual...)
		agentRuntimes[individual[0].ID] = runtime
	}

	reserved := map[string]string{}
	for _, stmt := range file.Stmts {
		switch n := stmt.Cmd.(type) {
		case *syntax.FuncDecl:
			reserved[n.Name.Value] = "shell function"
		case *syntax.BashPPFuncDecl:
			if n.Receiver == nil {
				reserved[n.Name.Value] = "function"
			}
		case *syntax.BashPPDecl:
			if n.Name != nil {
				reserved[n.Name.Value] = "declaration"
			}
		case *syntax.BashPPImport:
			if n.Alias != nil {
				reserved[n.Alias.Value] = "import"
			}
		}
	}
	foreign := map[string]*bashPPFunc{}
	for _, plan := range plans {
		runtime := runtimes[plan.Language]
		if agentRuntime := agentRuntimes[plan.ID]; agentRuntime != nil {
			runtime = agentRuntime
		}
		module := polyglot.Start(plan, runtime)
		if row, _ := polyglot.LookupLanguage(plan.Language); row.Callbacks {
			module.SetCallbacks(r.bashPPForeignCallbacks())
		}
		r.bashPPForeignModules = append(r.bashPPForeignModules, module)
		if plan.Alias != "" {
			if kind := reserved[plan.Alias]; kind != "" {
				restore()
				return func() {}, fmt.Errorf("polyglot alias %s collides with %s", plan.Alias, kind)
			}
			reserved[plan.Alias] = "foreign module"
		}
		for _, export := range append(append([]polyglot.Export(nil), plan.Exports...), plan.UnavailableExports...) {
			name := export.Name
			if plan.Alias != "" {
				name = plan.Alias + "." + name
			}
			if kind := reserved[name]; kind != "" {
				restore()
				return func() {}, fmt.Errorf("polyglot callable %s collides with %s", name, kind)
			}
			if foreign[name] != nil {
				restore()
				return func() {}, fmt.Errorf("polyglot callable %s is exported more than once", name)
			}
			decl := foreignDecl(name, export.Signature)
			foreign[name] = &bashPPFunc{decl: decl, foreign: &bashPPForeignFunc{module: module, export: export, qualified: name}}
			reserved[name] = "foreign callable"
		}
	}
	r.bashPPForeignFuncs = foreign
	return restore, nil
}

func foreignDecl(name string, sig polyglot.Signature) *syntax.BashPPFuncDecl {
	base := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		base = name[i+1:]
	}
	decl := &syntax.BashPPFuncDecl{Name: &syntax.Lit{Value: base}}
	if sig.Dynamic {
		decl.Params = []*syntax.BashPPField{{Names: []*syntax.Lit{{Value: "args"}}, FieldType: &syntax.Lit{Value: "any"}, Ellipsis: syntax.NewPos(0, 1, 1)}}
		decl.Results = []*syntax.BashPPField{{FieldType: &syntax.Lit{Value: "any"}}, {FieldType: &syntax.Lit{Value: "error"}}}
		return decl
	}
	for i, typ := range sig.Params {
		field := &syntax.BashPPField{Names: []*syntax.Lit{{Value: fmt.Sprintf("arg%d", i)}}, FieldType: &syntax.Lit{Value: foreignShellType(typ)}}
		if sig.Variadic && i == len(sig.Params)-1 {
			field.Ellipsis = syntax.NewPos(0, 1, 1)
		}
		decl.Params = append(decl.Params, field)
	}
	for _, typ := range sig.Results {
		decl.Results = append(decl.Results, &syntax.BashPPField{FieldType: &syntax.Lit{Value: foreignShellType(typ)}})
	}
	return decl
}

func foreignShellType(typ string) string {
	switch typ {
	case "object", "handle", "callback":
		return "any"
	}
	return typ
}

// bashPPForeignExchange is the worker round trip every foreign invocation
// shape shares: argument conversion, the call itself and the island's captured
// stdout/stderr. ok is false when an argument could not be converted; the
// diagnostic and status are already recorded by then.
// foreignArgumentError is a caller input failure, not a worker or transport failure.
type foreignArgumentError struct{ error }

func (r *Runner) bashPPForeignExchange(ctx context.Context, fn *bashPPForeignFunc, args []string) (result polyglot.CallResult, err error, ok bool) {
	if fn.direct {
		values := make([]any, len(args))
		for i, arg := range args {
			values[i] = arg
			if i < len(fn.argCells) && fn.argCells[i] != nil && fn.argCells[i].vr.Kind == expand.Object {
				values[i] = fn.argCells[i].vr.Obj
			} else if fn.call != nil && i < len(fn.call.ArgExprs) {
				switch expr := fn.call.ArgExprs[i].(type) {
				case *syntax.BashPPBasicLit:
					switch expr.Kind {
					case "INT":
						values[i], _ = strconv.ParseInt(arg, 0, 64)
					case "FLOAT":
						values[i], _ = strconv.ParseFloat(arg, 64)
					case "STRING":
						values[i] = strings.Trim(arg, `"'`)
					}
				case *syntax.BashPPIdent:
					if expr.Name.Value == "true" {
						values[i] = true
					}
					if expr.Name.Value == "false" {
						values[i] = false
					}
				}
			} else if fn.call != nil && i < len(fn.call.Args) {
				if lit := fn.call.Args[i].Lit(); lit != "" {
					if integer, err := strconv.ParseInt(lit, 0, 64); err == nil {
						values[i] = integer
					} else if number, err := strconv.ParseFloat(lit, 64); err == nil {
						values[i] = number
					} else if lit == "true" || lit == "false" {
						values[i] = lit == "true"
					}
				}
			}
		}
		positional := len(values) - len(fn.argNames)
		kwargs := make(map[string]any, len(fn.argNames))
		for i, name := range fn.argNames {
			kwargs[name] = values[positional+i]
		}
		values = values[:positional]
		if fn.receiver != nil {
			if fn.export.Name == "" {
				result, err = fn.receiver.Call(ctx, values, kwargs)
			} else {
				result, err = fn.receiver.CallAttr(ctx, fn.export.Name, values, kwargs)
			}
		} else {
			result, err = fn.module.CallKeywords(ctx, fn.export.Name, values, kwargs)
		}
	} else {
		values := make([]any, len(args))
		for i, arg := range args {
			typ := "any"
			if !fn.export.Signature.Dynamic && len(fn.export.Signature.Params) > 0 {
				if i < len(fn.export.Signature.Params) {
					typ = fn.export.Signature.Params[i]
				} else if fn.export.Signature.Variadic {
					typ = fn.export.Signature.Params[len(fn.export.Signature.Params)-1]
				}
			}
			if typ == "handle" {
				if i < len(fn.argCells) && fn.argCells[i] != nil && fn.argCells[i].vr.Kind == expand.Object {
					if handle, ok := fn.argCells[i].vr.Obj.(*polyglot.Handle); ok {
						values[i] = handle
						continue
					}
				}
				r.errf("bash++: %s argument %d: want a Rust handle, got %q\n", fn.qualified, i+1, arg)
				r.exit.code = 2
				return polyglot.CallResult{}, nil, false
			}
			value, convErr := foreignArgument(arg, typ)
			if convErr != nil {
				return polyglot.CallResult{}, &foreignArgumentError{fmt.Errorf("%s argument %d: %w", fn.qualified, i+1, convErr)}, true
			}
			values[i] = value
		}
		if signature, streaming := fn.module.StreamSignature(fn.export.Name); streaming {
			if signature.Filter {
				return polyglot.CallResult{}, fmt.Errorf("%s requires a byte-pipeline invocation", fn.qualified), true
			}
			result.Value = &bashPPForeignIterator{module: fn.module, name: fn.export.Name, args: values, element: signature.Iterator}
		} else {
			result, err = fn.module.Call(ctx, fn.export.Name, values...)
		}
	}
	if result.Stdout != "" {
		fmt.Fprint(r.stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(r.stderr, result.Stderr)
	}
	return result, err, true
}

func (r *Runner) bashPPInvokeForeign(ctx context.Context, fn *bashPPForeignFunc, args []string) []string {
	if !r.bashPPForeignEffectsAllowed(ctx, fn) {
		return foreignZeroResults(fn)
	}
	result, err, ok := r.bashPPForeignExchange(ctx, fn, args)
	if !ok {
		return nil
	}
	// A host-governed method explicitly declaring (string, error) returns
	// every non-cancellation failure through its error result. This includes
	// bridge failures and nonzero governed processor status; callers opted in
	// to handling both. The separate Go call-site opt-in below retains its
	// worker-domain versus bridge-failure distinction.
	if results := fn.export.Signature.Results; len(results) == 2 && results[0] == "string" && results[1] == "error" && !fn.export.Signature.Dynamic {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			r.exit.fatal(err)
			return nil
		}
		value, failure := foreignResult(result.Value), ""
		if err != nil {
			failure = err.Error()
		}
		r.bashPPResultCells = []*bashPPCell{
			{vr: expand.Variable{Set: true, Kind: expand.String, Str: value}},
			bashPPForeignErrorCell(err),
		}
		r.exit = exitStatus{}
		return []string{value, failure}
	}
	if err != nil {
		if fn.export.Signature.Dynamic {
			r.exit = exitStatus{}
			return []string{"", err.Error()}
		}
		r.errf("bash++: foreign call %s failed: %v\n", fn.qualified, err)
		r.exit.code = 1
		var death *polyglot.WorkerExit
		if errors.As(err, &death) {
			r.exit.code = uint8(death.ExitStatus())
		}
		return nil
	}
	r.exit = exitStatus{}
	if iterator, ok := result.Value.(*bashPPForeignIterator); ok {
		r.bashPPResultCells = []*bashPPCell{{vr: expand.NewObject(iterator)}}
		return []string{""}
	}
	if fn.direct || fn.export.Signature.Dynamic {
		if handle, ok := result.Value.(*polyglot.Handle); ok {
			r.bashPPResultCells = []*bashPPCell{bashPPForeignHandleCell(handle)}
			if !fn.direct {
				r.bashPPResultCells = append(r.bashPPResultCells, bashPPForeignErrorCell(nil))
				return []string{"", ""}
			}
			return []string{""}
		}
	}
	value := foreignResult(result.Value)
	if len(fn.export.Signature.Results) == 1 && foreignObjectResult(fn.export.Signature.Results[0]) {
		r.bashPPResultCells = []*bashPPCell{{vr: expand.NewObject(result.Value)}}
	}
	if fn.direct {
		return []string{value}
	}
	if fn.export.Signature.Dynamic {
		return []string{value, ""}
	}
	if len(fn.export.Signature.Results) == 0 {
		return nil
	}
	return []string{value}
}

// bashPPInvokeForeignErr is the explicit error opt-in: the call site named one
// binding more than the export declares results, so the worker's own failure
// becomes a typed trailing error result with status 0 instead of a diagnostic.
// A zero-result export (Python -> None, Rust Result<(), E>) opts in with a
// single error binding.
//
// Argument-conversion failures also populate the opt-in error result.
// Only a failure carrying polyglot.ForeignErrorDetail is the worker's own.
// A transport failure — EOF from a dead worker, cancellation, a response ID
// mismatch, a decode or annotation violation, a launch failure — stays an
// infrastructure failure reported exactly as the one-value form reports it:
// the diagnostic, the failure status, zero results and a nil error, so a
// script can never mistake a broken bridge for a domain error.
func (r *Runner) bashPPInvokeForeignErr(ctx context.Context, fn *bashPPForeignFunc, args []string) []string {
	if !r.bashPPForeignEffectsAllowed(ctx, fn) {
		return append(foreignZeroResults(fn), "")
	}
	result, err, ok := r.bashPPForeignExchange(ctx, fn, args)
	if !ok {
		return nil
	}
	resultTypes := fn.export.Signature.Results
	values := make([]string, len(resultTypes)+1)
	cells := make([]*bashPPCell, len(resultTypes)+1)
	for i, typ := range resultTypes {
		values[i] = foreignZero(typ)
		cells[i] = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: values[i]}}
	}
	last := len(resultTypes)
	var argumentError *foreignArgumentError
	switch {
	case err == nil:
		r.exit = exitStatus{}
		if len(resultTypes) > 0 {
			values[0] = foreignResult(result.Value)
			cells[0].vr.Str = values[0]
			_, handle := result.Value.(*polyglot.Handle)
			if handle {
				cells[0] = bashPPForeignHandleCell(result.Value.(*polyglot.Handle))
			} else if len(resultTypes) == 1 && foreignObjectResult(resultTypes[0]) {
				cells[0] = &bashPPCell{vr: expand.NewObject(result.Value)}
			}
		}
		cells[last] = bashPPForeignErrorCell(nil)
	case bashPPForeignDomainError(err), errors.As(err, &argumentError):
		r.exit = exitStatus{}
		values[last] = err.Error()
		cells[last] = bashPPForeignErrorCell(err)
	default:
		r.errf("bash++: foreign call %s failed: %v\n", fn.qualified, err)
		r.exit.code = 1
		var death *polyglot.WorkerExit
		if errors.As(err, &death) {
			r.exit.code = uint8(death.ExitStatus())
		}
		cells[last] = bashPPForeignErrorCell(nil)
	}
	r.bashPPResultCells = cells
	return values
}

// bashPPForeignDomainError reports whether err is the foreign function's own
// failure, the one kind the explicit error opt-in turns into a result.
func bashPPForeignDomainError(err error) bool {
	_, ok := polyglot.ForeignErrorDetail(err)
	return ok
}

// The carrier is runtime authority; any is only its host assignment type.
func bashPPForeignHandleCell(handle *polyglot.Handle) *bashPPCell {
	return &bashPPCell{vr: expand.NewObject(handle), declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}}
}

func bashPPForeignErrorCell(err error) *bashPPCell {
	errType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}
	if err == nil {
		return &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String}, declType: errType, interfaceValue: &bashPPInterfaceValue{nilIface: true}}
	}
	concrete := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "polyglot.error"}}
	payload := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: err.Error()}, declType: concrete}
	return &bashPPCell{
		vr:             expand.Variable{Set: true, Kind: expand.String, Str: err.Error()},
		declType:       errType,
		interfaceValue: &bashPPInterfaceValue{cell: payload, dynamic: concrete},
	}
}

// foreignObjectResult reports a result kind whose value is the Object itself
// rather than its text: a structured value or an opaque handle.
func foreignObjectResult(typ string) bool {
	return typ == "object" || typ == "handle"
}

func foreignArgument(text, typ string) (any, error) {
	switch typ {
	case "int":
		return strconv.ParseInt(text, 0, 64)
	case "float64":
		return strconv.ParseFloat(text, 64)
	case "bool":
		return strconv.ParseBool(text)
	case "bytes":
		return []byte(text), nil
	case "string", "any":
		return text, nil
	case "object":
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("want JSON object: %w", err)
		}
		return normalizeForeignJSON(value)
	case "nil":
		if text == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("want nil")
	case "handle":
		return nil, fmt.Errorf("want a Rust handle")
	default:
		return text, nil
	}
}

func foreignZero(typ string) string {
	switch typ {
	case "bool":
		return "false"
	case "float64", "int":
		return "0"
	}
	return ""
}

func normalizeForeignJSON(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		return value.Float64()
	case []any:
		for i := range value {
			var err error
			value[i], err = normalizeForeignJSON(value[i])
			if err != nil {
				return nil, err
			}
		}
	case map[string]any:
		for key := range value {
			var err error
			value[key], err = normalizeForeignJSON(value[key])
			if err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

func foreignResult(value any) string {
	switch x := value.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		data, _ := json.Marshal(x)
		return string(data)
	}
}

// bashPPImportModule resolves a direct-import alias: the running file's own
// imports first, then any the embedder seeded through [ForeignImports].
func (r *Runner) bashPPImportModule(alias string) *polyglot.Module {
	if module := r.bashPPForeignImports[alias]; module != nil {
		return module
	}
	return r.bashPPSeededImports[alias]
}

// bashPPForeignCommand resolves a command word of the form `alias.name` to
// the island function it names — a direct import's module attribute or a
// source block's exported function — when the word is not a shell function.
// This is the command-alias adapter (B8): `alias greet=py.greet` then `greet
// hi` dispatches here after alias expansion, as does `py.greet hi` itself and
// a shell function wrapper `greet() { py.greet "$@"; }`. `command py.greet`
// bypasses it, exactly as it bypasses a shell function.
func (r *Runner) bashPPForeignCommand(word string) (*polyglot.Module, string, bool) {
	if r.Dialect() != syntax.LangBashPP {
		return nil, "", false
	}
	dot := strings.IndexByte(word, '.')
	if dot <= 0 || dot == len(word)-1 || strings.ContainsAny(word, "/") {
		return nil, "", false
	}
	alias, name := word[:dot], word[dot+1:]
	if strings.ContainsRune(name, '.') {
		return nil, "", false
	}
	if module := r.bashPPImportModule(alias); module != nil {
		return module, name, true
	}
	if fn := r.bashPPForeignFuncs[word]; fn != nil && fn.foreign != nil && fn.foreign.module != nil && fn.foreign.receiver == nil {
		if lang := fn.foreign.module.Plan().Language; lang == "python" || lang == "powershell" || lang == "go" {
			return fn.foreign.module, fn.foreign.export.Name, true
		}
	}
	return nil, "", false
}

// bashPPRunForeignCommand runs an island function as a shell command. The
// argv words are the command's expanded arguments; what the function writes
// goes to the command's stdout and stderr (so redirections and pipes apply);
// its return value is the status by the xonsh callable-alias convention; a
// lookup failure is the shell's own 127/126; a worker killed by a signal is a
// foreground death, 128+signal; cancellation stops the shell as it would any
// command. See [polyglot.Module.Command].
func (r *Runner) bashPPRunForeignCommand(ctx context.Context, pos syntax.Pos, module *polyglot.Module, word, name string, argv []string) {
	for _, export := range module.Plan().Exports {
		if export.Name == name && !r.bashPPForeignEffectsAllowed(ctx, &bashPPForeignFunc{module: module, export: export, qualified: word}) {
			return
		}
	}
	// Foreign output shares the same sink locks as shell diagnostics, including
	// iterator open/close output delivered by the background process substrate.
	stdout, stderr := r.bashPPWriter(r.stdout), r.bashPPWriter(r.stderr)
	if signature, streaming := module.StreamSignature(name); streaming {
		leave, joinErr := r.bashPPJoinStreamJob(ctx, module)
		if joinErr != nil {
			r.errf("%s: %v\n", word, joinErr)
			r.exit.code = 1
			return
		}
		defer leave()
		values := make([]any, len(argv))
		for i, arg := range argv {
			parameter := i
			if signature.Filter {
				parameter++
			}
			typ := "any"
			if parameter < len(signature.Params) {
				typ = signature.Params[parameter]
			}
			value, err := foreignArgument(arg, typ)
			if err != nil {
				r.errf("%s: %v\n", word, err)
				r.exit.code = 2
				return
			}
			values[i] = value
		}
		var upstream io.Reader = strings.NewReader("")
		if r.stdin != nil {
			upstream = r.stdin
		}
		if signature.Filter {
			values = append([]any{polyglot.FilterInput(upstream)}, values...)
		}
		process, err := StartForeignIterator(ctx, module, name, values, stdout, stderr)
		if err != nil {
			r.errf("%s: %v\n", word, err)
			r.exit.code = 1
			return
		}
		defer process.Close()
		for line := range process.Lines() {
			frame, decodeErr := module.DecodeIteratorFrame(ctx, line, signature.Iterator)
			if decodeErr != nil {
				err = decodeErr
				break
			}
			fmt.Fprint(stdout, frame.Stdout)
			fmt.Fprint(stderr, frame.Stderr)
			if signature.Filter {
				value, ok := frame.Value.(string)
				if !ok {
					err = fmt.Errorf("TextIO filter must yield string")
					break
				}
				_, err = io.WriteString(stdout, value)
			} else {
				encoded, encodeErr := json.Marshal(frame.Value)
				if encodeErr != nil {
					err = encodeErr
					break
				}
				_, err = fmt.Fprintln(stdout, string(encoded))
			}
			if err != nil {
				process.Close()
				break
			}
		}
		if err != nil {
			process.Close()
		}
		status, waitErr := process.Wait()
		if err == nil {
			err = waitErr
		}
		r.exit.code = uint8(status)
		if ctx.Err() != nil {
			r.exit.fatal(ctx.Err())
		} else if err != nil && status == 0 {
			r.errf("%s: %v\n", word, err)
			r.exit.code = 1
		}
		return
	}
	// A function the analyzer marked as reading pipeline input runs as a filter
	// over the command's stdin; every other function leaves stdin untouched so
	// it never blocks on a terminal it does not read.
	var stdin io.Reader
	if module.Plan().Language == "go" {
		stdin = r.stdin
	}
	for _, export := range module.Plan().Exports {
		if export.Name == name && export.Signature.PipeInput {
			if r.stdin != nil {
				stdin = r.stdin
			} else {
				stdin = strings.NewReader("")
			}
			break
		}
	}
	if module.Plan().Language == "go" {
		ctx = polyglot.WithGoCommandCaller(ctx, r.Dir, r.bashPPGoCommandEnvironment())
	}
	status, err := module.CommandStreams(ctx, name, argv, stdin, stdout, stderr)
	switch {
	case err == nil:
		r.exit = exitStatus{code: uint8(status)}
	case ctx.Err() != nil:
		r.exit.fatal(ctx.Err())
	case errors.Is(err, polyglot.ErrCommandNotFound):
		r.errf("%s%s: command not found\n", r.bashErrPrefix(pos), word)
		r.exit.code = 127
	case errors.Is(err, polyglot.ErrCommandNotCallable):
		r.errf("%s%s: cannot execute: not a callable island function\n", r.bashErrPrefix(pos), word)
		r.exit.code = 126
	default:
		var death *polyglot.WorkerExit
		if errors.As(err, &death) && death.Signal > 0 {
			r.exit.code = uint8(128 + death.Signal)
			return
		}
		r.errf("%s%s: %v\n", r.bashErrPrefix(pos), word, err)
		r.exit.code = 1
	}
}

// bashPPForeignErrorMethod exposes the Go error returned by a host runner
// without pretending its dynamic type is itself an interface.
func bashPPForeignErrorMethod(iv *bashPPInterfaceValue, method string) (*bashPPFunc, bool) {
	if iv == nil || iv.cell == nil || method != "Error" || bashPPTypeText(iv.dynamic) != "polyglot.error" {
		return nil, false
	}
	text := iv.cell.vr.String()
	lit := &syntax.BashPPFuncLit{Kw: &syntax.Lit{Value: "func"}, Results: []*syntax.BashPPField{{FieldType: &syntax.Lit{Value: "string"}}}}
	return &bashPPFunc{lit: lit, runtimeError: &bashPPRuntimeErrorCall{text: text, method: "Error"}}, true
}
