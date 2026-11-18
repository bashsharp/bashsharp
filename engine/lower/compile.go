package lower

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type emitter struct {
	sourceFile            *syntax.File
	goSource              bool
	options               Options
	moduleImporter        types.Importer
	prefix                string
	marks                 []Mapping
	scopes                []map[string]bool
	funcs                 map[string]bool
	bridge                bool
	output                bool
	inFunc                bool
	globals               map[string]bool
	globalTypes           map[string]string
	typeNames             map[string]bool
	visibleGlobals        map[string]bool
	functionGlobals       map[string]bool
	panicSupport          bool
	imports               map[string]string
	importAliased         map[string]bool // import bindings the input spelled with an alias
	cgoAliases            map[string]bool
	fileImports           map[string][]sourceImport
	callableParams        map[*syntax.BashPPField]string
	resultTypes           []string
	resultNames           []string
	resultCallFrame       string
	resultCallWant        int
	foreignGlobals        map[string]bool
	dotNames              map[string]bool
	declaredGlobals       map[string]bool
	iotaValue             *int
	bigIntegers           bool
	functionDecls         map[string]*syntax.BashPPFuncDecl
	initEntries           map[string]bool
	foreignFunctions      map[string]foreignFunction
	foreignIteratorValues map[string]string
	iteratorScopes        []map[string]string
	foreignPlans          []polyglot.Plan
	foreignImports        []polyglot.ImportPlan
	foreignImportAliases  map[string]int
	pythonValues          map[string]bool
	foreignEnvs           map[string]*polyglot.EnvironmentPlan
	goErrorFuncs          map[*syntax.BashPPFuncDecl]bool
	methodDeclarations    []*syntax.BashPPFuncDecl
	enumMembers           map[string][]*syntax.Lit
	projections           projector
	declaredTypes         map[string]*syntax.BashPPDecl
	writtenNames          map[string]bool
	execution             bool
	guarded               bool
	readonly              bool
	mixedShell            bool
	handleIterator        bool
	programExpr           string
	sourceName            string
	inferredParams        map[*syntax.BashPPField]string
	globalDecls           strings.Builder
	globalChecked         map[*syntax.BashPPShortDecl]string
	nativeShellNames      map[string]bool
	nativeShellBody       bool
	branchTargets         []*lowerBranchTarget
	branchLabelSeq        int
	// pendingLabel is the source label of the labeled loop, switch, or select
	// about to push its branch target; it seeds the target so a deep branch
	// and a goto name the same label instead of a synthesized one.
	pendingLabel string
	// predeclaredCall is set when a Bash++ unit uses the predeclared
	// decorator context without declaring its own Call type; shellFuncs
	// names the unit's shell functions for the decorator diagnostics.
	predeclaredCall bool
	shellFuncs      map[string]bool

	// exprLine is the source line of the Go-source expression or statement
	// being emitted; a sub-expression on another source line is led by an
	// inline /*line*/ directive (see expr).
	exprLine uint
}

type lowerBranchTarget struct {
	loop  bool
	label string
}

func (e *emitter) pushBranchTarget(loop bool) *lowerBranchTarget {
	target := &lowerBranchTarget{loop: loop, label: e.pendingLabel}
	e.pendingLabel = ""
	e.branchTargets = append(e.branchTargets, target)
	return target
}

func (e *emitter) popBranchTarget() {
	e.branchTargets = e.branchTargets[:len(e.branchTargets)-1]
}

func (e *emitter) branchStmt(n *syntax.BashPPBranch) (string, error) {
	if n.Depth == 0 {
		return n.Kw.Value, nil
	}
	depth := int(n.Depth)
	for i := len(e.branchTargets) - 1; i >= 0; i-- {
		target := e.branchTargets[i]
		if n.Kw.Value == "continue" && !target.loop {
			continue
		}
		depth--
		if depth != 0 {
			continue
		}
		// A labeled branch to the innermost target stays labeled when the
		// target carries its source label: Go rejects a label nothing names.
		if target.label == "" {
			if n.Depth == 1 {
				return n.Kw.Value, nil
			}
			target.label = fmt.Sprintf("%sbranch%d", e.prefix, e.branchLabelSeq)
			e.branchLabelSeq++
		}
		return n.Kw.Value + " " + target.label, nil
	}
	return "", e.fail(n, CodeUnsupported, "branch depth exceeds enclosing statements")
}

// labeledStmt emits a Go label verbatim. A labeled loop, switch, or select
// emits the label through its branch target so that target does not
// synthesize a second one; go/types has already rejected unused labels, so
// every emitted label has a branch or goto naming it.
func (e *emitter) labeledStmt(n *syntax.BashPPLabeled) (string, error) {
	if n.Stmt == nil {
		return n.Label.Value + ":", nil
	}
	switch n.Stmt.Cmd.(type) {
	case *syntax.BashPPFor, *syntax.BashPPRange, *syntax.BashPPSwitch, *syntax.BashPPSelect:
		e.pendingLabel = n.Label.Value
		text, err := e.statement(n.Stmt)
		e.pendingLabel = ""
		return text, err
	}
	text, err := e.statement(n.Stmt)
	return n.Label.Value + ":\n" + text, err
}

// Compile returns canonical Go and mappings, or positioned diagnostics with no
// partial output. It never executes the input program.
func Compile(file *syntax.File, options Options) (*Result, error) {
	if options.Library && (file == nil || !file.GoSource) {
		return nil, ErrorList{{Code: CodeUnsupported, Msg: "library emission requires Go source"}}
	}
	if options.Library && options.Entry != "" {
		return nil, ErrorList{{Code: CodeType, Msg: "library emission cannot define a runtime entry", Pos: file.Pos()}}
	}
	if err := CheckProfile(file, options.Origin); err != nil {
		return nil, err
	}
	if err := CheckToolchain(file); err != nil {
		return nil, err
	}
	if !file.GoSource {
		if err := CheckBashSharp(file); err != nil {
			return nil, err
		}
	}
	return compilePass(file, options, nil, &preparedForeign{})
}
func compilePass(file *syntax.File, options Options, globalTypes map[string]string, prepared *preparedForeign) (*Result, error) {
	if file == nil {
		return nil, ErrorList{{Code: CodeUnsupported, Msg: "nil syntax file"}}
	}
	if options.Library && options.Package == "" {
		return nil, ErrorList{{Code: CodeType, Msg: "library emission requires the source package name", Pos: file.Pos()}}
	}
	if options.Package == "" {
		options.Package = "main"
	}
	if len(file.CgoPackages) > 1 {
		return nil, ErrorList{{Code: CodeUnsupported, Msg: "compiled flattened output cannot merge multiple package-scoped cgo preambles", Pos: file.Pos()}}
	}
	if options.Runtime == "" {
		options.Runtime = DefaultRuntime
	}
	if !token.IsIdentifier(options.Package) || token.Lookup(options.Package).IsKeyword() {
		return nil, ErrorList{{Code: CodeType, Msg: "invalid package name", Pos: file.Pos()}}
	}
	e := &emitter{goSource: file.GoSource, sourceFile: file, writtenNames: map[string]bool{}, inferredParams: map[*syntax.BashPPField]string{}, declaredTypes: map[string]*syntax.BashPPDecl{}, functionDecls: map[string]*syntax.BashPPFuncDecl{}, enumMembers: map[string][]*syntax.Lit{}, options: options, funcs: map[string]bool{}, scopes: []map[string]bool{{}}, globals: map[string]bool{}, visibleGlobals: map[string]bool{}, imports: map[string]string{}, importAliased: map[string]bool{}, cgoAliases: map[string]bool{}, fileImports: map[string][]sourceImport{}, callableParams: map[*syntax.BashPPField]string{}, dotNames: map[string]bool{}, declaredGlobals: map[string]bool{}, typeNames: map[string]bool{}, globalTypes: globalTypes, foreignImportAliases: map[string]int{}, pythonValues: map[string]bool{}, goErrorFuncs: map[*syntax.BashPPFuncDecl]bool{}}
	e.foreignFunctions = map[string]foreignFunction{}
	e.moduleImporter = newModuleImporter(options.Dir)
	if options.Importer != nil {
		e.moduleImporter = options.Importer
	}
	e.sourceName = options.Origin
	if e.sourceName == "" {
		e.sourceName = file.Name
	}
	if err := e.prepareForeign(context.Background(), file, prepared); err != nil {
		return nil, err
	}
	e.findPythonValues(file)
	e.projections.projectionPush()
	e.needsExecution(file)
	e.needsShell(file)
	e.findNativeShells(file)
	if options.Entry != "" {
		if !token.IsIdentifier(options.Entry) || !ast.IsExported(options.Entry) {
			return nil, e.fail(file, CodeType, "entry must be an exported Go identifier")
		}
		e.execution = true
		e.bridge = true
	}
	e.findCheckedValues(file)
	e.findProjectionWrites(file)
	// Allocate private names from the tree rather than reserving user identifiers.
	for n := 0; ; n++ {
		e.prefix = fmt.Sprintf("__bpp%d_", n)
		used := false
		syntax.Walk(file, func(n syntax.Node) bool {
			if l, ok := n.(*syntax.Lit); ok && strings.Contains(l.Value, e.prefix) {
				used = true
			}
			return true
		})
		if !used {
			break
		}
	}
	for _, s := range file.Stmts {
		if f, ok := s.Cmd.(*syntax.BashPPFuncDecl); ok {
			if f.Receiver != nil {
				e.methodDeclarations = append(e.methodDeclarations, f)
			}
			if f.Receiver == nil {
				e.funcs[f.Name.Value] = true
				e.functionDecls[f.Name.Value] = f
			}
		}
		switch n := s.Cmd.(type) {
		case *syntax.BashPPConstGroup:
			for _, spec := range n.Specs {
				e.globals[spec.Name.Value] = true
			}
		case *syntax.BashPPDecl:
			if n.Kw.Value == "type" {
				e.typeNames[n.Name.Value] = true
				e.declaredTypes[n.Name.Value] = n
			}
			if n.Kw.Value == "var" || n.Kw.Value == "const" {
				e.globals[n.Name.Value] = true
			}
		case *syntax.BashPPShortDecl:
			for _, name := range names(n.Lhs) {
				if name != "_" {
					e.globals[name] = true
				}
			}
		}
	}
	if file.GoSource {
		for name := range e.globals {
			e.visibleGlobals[name] = true
		}
	}
	if options.Entry != "" && (e.funcs[options.Entry] || e.globals[options.Entry] || e.typeNames[options.Entry]) {
		return nil, e.fail(file, CodeType, "entry conflicts with source declaration: "+options.Entry)
	}
	if err := e.prepareDecorators(file); err != nil {
		return nil, err
	}
	if err := e.discoverCallableParams(file); err != nil {
		return nil, err
	}
	if e.execution {
		if err := e.inferChannelParameters(file); err != nil {
			return nil, err
		}
	}
	hasConstants := false
	syntax.Walk(file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPConstGroup:
			hasConstants = true
		case *syntax.BashPPDecl:
			hasConstants = hasConstants || n.Kw.Value == "const"
		}
		return true
	})
	// The converter renames each Go-source `func init` to a uniquely named
	// callable and appends one synthetic entry call per function. When the
	// source's own main is the entry, gc schedules init functions itself:
	// each renamed function is emitted back as a real `func init` and its
	// dispatcher call is dropped. A synthetic `func init` that only calls
	// them would survive gc's dead-init elimination as one live init func,
	// which GOROOT test/noinit.go observes through the main init task.
	e.initEntries = map[string]bool{}
	if e.nativeMain() {
		for _, s := range file.Stmts {
			name, ok := e.syntheticEntryCall(s)
			if !ok || name == "main" {
				continue
			}
			f := e.functionDecls[name]
			if f == nil || f.Receiver != nil || len(f.Params) > 0 || len(f.Results) > 0 {
				continue
			}
			e.initEntries[name] = true
		}
	}
	var declarations, body strings.Builder
	declarations.WriteString(e.foreignDeclarations())
	declarations.WriteString(e.decoratorCallAlias())
	fileBodies := map[string]*strings.Builder{}
	// Under goSource the declarations are re-emitted in the order the input
	// wrote them (C11): each statement's declaration text is captured here
	// with the statement's source position and written sorted, instead of
	// flushing globalDecls ahead of declarations.
	var sourceOrder []sourceDecl
	for _, s := range file.Stmts {
		globalStart, declStart := e.globalDecls.Len(), declarations.Len()
		if f, ok := s.Cmd.(*syntax.BashPPFuncDecl); ok {
			if err := e.statementFlags(s); err != nil {
				return nil, err
			}
			if e.initEntries[f.Name.Value] {
				renamed := *f
				nameLit := *f.Name
				nameLit.Value = "init"
				renamed.Name = &nameLit
				f = &renamed
			}
			text, err := e.function(f)
			if err != nil {
				return nil, err
			}
			if directives := e.goDirectives(s); directives != "" && e.goSource {
				// Between the marker and the declaration, where gofmt
				// keeps a directive block.
				marker, decl, _ := strings.Cut(text, "\n")
				text = marker + "\n" + directives + decl
			}
			declarations.WriteString(text)
		} else {
			var text string
			var err error
			switch n := s.Cmd.(type) {
			case *syntax.BashPPImport:
				err = e.importDecl(n)
			case *syntax.BashPPConstGroup:
				var constants string
				constants, err = e.constGroup(n)
				declarations.WriteString(e.mark(n) + constants + "\n")
				for _, spec := range n.Specs {
					e.visibleGlobals[spec.Name.Value] = true
					text += e.lexicalGlobalConstant(spec.Name.Value)
				}
			case *syntax.BashPPDecl:
				if n.Kw.Value == "var" || n.Kw.Value == "const" {
					text, err = e.globalStatement(s)
				} else if n.Kw.Value == "type" {
					var typ string
					typ, err = e.declarationType(n)
					declarations.WriteString(e.mark(n) + typ + "\n")
				} else {
					text, err = e.statement(s)
				}
			case *syntax.BashPPShortDecl:
				text, err = e.globalStatement(s)
			default:
				// A synthetic entry call has no source position of its own:
				// emit it bare rather than marking it with a borrowed one.
				if name, ok := e.syntheticEntryCall(s); ok && e.nativeMain() {
					if name != "main" && !e.initEntries[name] {
						text = name + "()\n"
					}
					break
				}
				text, err = e.statement(s)
			}
			if err != nil {
				return nil, err
			}
			if e.execution && hasConstants && text != "" {
				switch s.Cmd.(type) {
				case *syntax.BashPPDefer, *syntax.BashPPReturn:
				default:
					text = e.program() + ".RootStatement(func(){\n" + text + "\n})\n"
				}
			}
			body.WriteString(text)
			if options.Library && text != "" {
				source, ok := file.SourceAt(s.Pos())
				if !ok {
					return nil, e.fail(s, CodeUnsupported, "library emission has source-free package initialization")
				}
				builder := fileBodies[source.Name]
				if builder == nil {
					builder = new(strings.Builder)
					fileBodies[source.Name] = builder
				}
				builder.WriteString(text)
			}
		}
		if e.goSource {
			if chunk := e.globalDecls.String()[globalStart:] + declarations.String()[declStart:]; chunk != "" {
				source, _ := file.SourceAt(s.Pos())
				sourceOrder = append(sourceOrder, sourceDecl{pos: s.Pos(), source: source.Name, text: chunk})
			}
		}
	}
	imports := []string{}
	if e.mixedShell || e.handleIterator {
		imports = append(imports, options.Runtime+"/shellexec")
	}
	if e.bigIntegers {
		imports = append(imports, "math/big")
	}
	if e.execution || e.panicSupport || e.guarded {
		e.output = true
		imports = append(imports, "os")
	}
	if e.output {
		imports = append(imports, "fmt")
	}
	if len(e.foreignPlans) > 0 || len(e.foreignImports) > 0 {
		imports = append(imports, "context", "github.com/bashsharp/bashsharp/engine/polyglot")
	}
	imports = append(imports, e.loweredRuntimeImports()...)
	if e.bridge {
		imports = append(imports, options.Runtime)
	}
	for _, path := range e.imports {
		found := false
		for _, p := range imports {
			if p == path {
				found = true
			}
		}
		if !found {
			imports = append(imports, path)
		}
	}
	sort.Strings(imports)
	if options.Library {
		return e.libraryResult(file, options, globalTypes, prepared, sourceOrder, fileBodies)
	}
	var raw strings.Builder
	fmt.Fprintf(&raw, "// Code generated by github.com/bashsharp/bashsharp/engine/lower. DO NOT EDIT.\npackage %s\n", options.Package)
	if e.mixedShell || e.handleIterator {
		fmt.Fprintf(&raw, "import %sshellexec %s\n", e.prefix, strconv.Quote(options.Runtime+"/shellexec"))
	}
	if e.output {
		fmt.Fprintf(&raw, "import %sfmt \"fmt\"\n", e.prefix)
	}
	if len(e.foreignPlans) > 0 || len(e.foreignImports) > 0 {
		fmt.Fprintf(&raw, "import %scontext \"context\"\n", e.prefix)
		fmt.Fprintf(&raw, "import %spolyglot \"github.com/bashsharp/bashsharp/engine/polyglot\"\n", e.prefix)
	}
	for _, importPath := range e.loweredRuntimeImports() {
		fmt.Fprintf(&raw, "import %s%s %s\n", e.prefix, path.Base(importPath), strconv.Quote(importPath))
	}
	if e.bridge {
		fmt.Fprintf(&raw, "import %srt %s\n", e.prefix, strconv.Quote(options.Runtime))
	}
	if e.execution || e.panicSupport || e.guarded {
		fmt.Fprintf(&raw, "import %sos \"os\"\n", e.prefix)
	}
	if e.bigIntegers {
		fmt.Fprintf(&raw, "import %sbig \"math/big\"\n", e.prefix)
	}
	raw.WriteString(e.importLines())
	if e.panicSupport && !e.execution {
		raw.WriteString(e.panicHelpers())
	}
	if e.execution && e.globalTypes == nil && len(e.globals) > 0 {
		fmt.Fprintf(&raw, "var %sprogram *%srt.Program\n", e.prefix, e.prefix)
	}
	if e.goSource {
		// The converter buckets a package's declarations (vars, then
		// types and consts, then funcs) for the interpreter's
		// forward-reference rule; Go orders package-level declarations
		// itself, so the generated Go keeps the input's order.
		sort.SliceStable(sourceOrder, func(i, j int) bool {
			return sourceOrder[i].before(sourceOrder[j])
		})
		for _, d := range sourceOrder {
			raw.WriteString(d.text)
		}
	} else {
		raw.WriteString(e.globalDecls.String())
		raw.WriteString(declarations.String())
	}
	tail := ""
	if e.bridge && !e.goSource {
		tail = e.prefix + "rt.Exit()\n"
	}
	head := ""
	if e.panicSupport {
		head = e.panicBoundary()
	}
	if e.guarded && !e.execution {
		head = e.guardBoundary() + head
	}
	if e.execution {
		raw.WriteString(e.programMain(body.String()))
	} else if e.nativeMain() || (e.goSource && options.Package != "main") {
		// The source's main is the entry; what remains of the body is the
		// converter's init calls, which Go sequences before main itself.
		// A Go package that is not main has no entry to synthesise either:
		// gc compiles it as it is, and a made-up main would be one more
		// function for its -m notes to report.
		if body.Len() > 0 {
			fmt.Fprintf(&raw, "func init() {\n%s}\n", body.String())
		}
	} else {
		raw.WriteString(e.foundationMain(head, body.String(), tail))
	}
	reset := ""
	if e.goSource {
		// Ordinary Go declarations and assignments have no shell exit status.
		// Goroutines must not write shared command state as a side effect.
	} else if e.execution {
		reset = e.program() + ".SetStatus(0)\n"
	} else if e.bridge {
		reset = e.prefix + "rt.Status = 0\n"
	}
	rawText := strings.ReplaceAll(raw.String(), "/*"+e.prefix+"reset*/", reset)
	status0 := ""
	status1 := ""
	if e.goSource {
		// Go's recover/print semantics do not report shell command statuses.
	} else if e.execution {
		status0 = e.program() + ".SetStatus(0);"
		status1 = e.program() + ".SetStatus(1);"
	} else if e.bridge {
		status0 = e.prefix + "rt.Status = 0;"
		status1 = e.prefix + "rt.Status = 1;"
	}
	rawText = strings.ReplaceAll(rawText, "/*"+e.prefix+"status0*/", status0)
	rawText = strings.ReplaceAll(rawText, "/*"+e.prefix+"status1*/", status1)
	if e.goSource {
		pruned, retained, err := pruneUnusedGoSourceImports(rawText, e.imports, e.cgoAliases)
		if err != nil {
			return nil, e.fail(file, CodeExpr, "generated Go imports are not syntactically valid: "+err.Error())
		}
		rawText = string(pruned)
		fromSource := map[string]bool{}
		for _, path := range e.imports {
			fromSource[path] = true
		}
		filtered := imports[:0]
		for _, path := range imports {
			if fromSource[path] && !retained[path] {
				continue
			}
			filtered = append(filtered, path)
		}
		imports = filtered
	}
	// Execution mode can need output plumbing without emitting a fmt call.
	// Check the generated import's actual selectors before retaining it: Go
	// rejects an unused import even in a compile-only foreign fence unit.
	if e.output {
		generated, parseErr := parser.ParseFile(token.NewFileSet(), "generated.go", rawText, 0)
		if parseErr != nil {
			return nil, e.fail(file, CodeExpr, "generated Go imports are not syntactically valid: "+parseErr.Error())
		}
		fmtUsed := false
		ast.Inspect(generated, func(n ast.Node) bool {
			if selector, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := selector.X.(*ast.Ident); ok && id.Name == e.prefix+"fmt" {
					fmtUsed = true
				}
			}
			return true
		})
		if !fmtUsed {
			rawText = strings.Replace(rawText, "import "+e.prefix+"fmt \"fmt\"\n", "", 1)
			if !e.goSource {
				fromSource := false
				for _, importPath := range e.imports {
					fromSource = fromSource || importPath == "fmt"
				}
				if !fromSource {
					for i, importPath := range imports {
						if importPath == "fmt" {
							imports = append(imports[:i], imports[i+1:]...)
							break
						}
					}
				}
			}
		}
	}
	source, err := format.Source([]byte(rawText))
	if err != nil {
		return nil, e.fail(file, CodeExpr, "generated Go is not syntactically valid: "+err.Error())
	}
	result := &Result{Sources: append([]syntax.SourceFile(nil), file.Sources...), Source: source, Package: options.Package, Imports: imports, Origin: options.Origin}
	for _, plan := range e.foreignImports {
		result.ForeignImports = append(result.ForeignImports, plan.Clone())
	}
	if e.execution {
		result.Entry = e.prefix + "execute"
		if options.Entry != "" {
			result.Entry = options.Entry
		}
	}
	// Markers survive gofmt; map the next nonempty emitted line. Markers carry
	// only ordinal IDs, never source filenames or host paths.
	pending := -1
	for i, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "// lower:") {
			pending, _ = strconv.Atoi(strings.TrimPrefix(trimmed, "// lower:"))
			continue
		}
		if pending >= 0 && trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			m := e.marks[pending]
			m.GoLine = i + 1
			m.GoCol = len(line) - len(strings.TrimLeft(line, "\t ")) + 1
			result.Mappings = append(result.Mappings, m)
			pending = -1
		}
	}
	// Native Go's checker supplies representability, arity and operator checks.
	// A synthetic bridge signature avoids loading module source or running tools.
	fs := token.NewFileSet()
	goFile, err := parser.ParseFile(fs, "generated.go", source, 0)
	if err != nil {
		return nil, e.fail(file, CodeExpr, err.Error())
	}
	prepareCgoCheckerFile(goFile)
	var diagnostics ErrorList
	conf := types.Config{FakeImportC: len(file.CgoPackages) > 0, Importer: bridgeImporter{fallback: e.moduleImporter, path: options.Runtime, cache: map[string]*types.Package{}, foreign: e.hasForeign()}, Error: func(err error) {
		te, ok := err.(types.Error)
		pos := file.Pos()
		node := "File"
		if ok {
			line := fs.Position(te.Pos).Line
			for _, m := range result.Mappings {
				if m.GoLine > line {
					break
				}
				pos, node = m.Pos, m.Node
			}
		}
		msg := err.Error()
		if ok {
			msg = te.Msg
		}
		code := CodeType
		if strings.Contains(msg, "undefined:") {
			code = CodeUndefined
		}
		origin, _ := file.SourceAt(pos)
		diagnostics = append(diagnostics, Diagnostic{Code: code, Msg: msg, Node: node, Pos: pos, Source: origin.Name})
	}}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	checked, _ := conf.Check(options.Package, fs, []*ast.File{goFile}, info)
	if len(diagnostics) > 0 {
		sort.SliceStable(diagnostics, func(i, j int) bool { return diagnostics[i].Pos.Offset() < diagnostics[j].Pos.Offset() })
		return nil, diagnostics
	}
	if globalTypes == nil && len(e.globals) > 0 {
		inferred := map[string]string{}
		for name := range e.globals {
			if obj := checked.Scope().Lookup(name); obj != nil {
				inferred[name] = types.TypeString(obj.Type(), func(p *types.Package) string {
					if p == checked {
						return ""
					}
					if p.Path() == options.Runtime {
						return e.prefix + "rt"
					}
					if p.Path() == "math/big" && e.bigIntegers {
						return e.prefix + "big"
					}
					for alias, path := range e.imports {
						if path == p.Path() {
							return alias
						}
					}
					return p.Name()
				})
			}
		}
		return compilePass(file, options, inferred, prepared)
	}
	if e.execution {
		if e.globalTypes == nil {
			e.globalTypes = map[string]string{}
		}
		source, err = e.lexicalStorage(source, fs, goFile, checked, info)
		if err != nil {
			return nil, e.fail(file, CodeExpr, err.Error())
		}
		source, err = e.lexicalValues(source)
		if err != nil {
			return nil, e.fail(file, CodeExpr, err.Error())
		}
		result.Source = source
		result.Mappings = e.sourceMappings(source)
		fs = token.NewFileSet()
		goFile, err = parser.ParseFile(fs, "generated.go", source, 0)
		if err != nil {
			return nil, e.fail(file, CodeExpr, err.Error())
		}
		_, _ = conf.Check(options.Package, fs, []*ast.File{goFile}, nil)
		if len(diagnostics) > 0 {
			return nil, diagnostics
		}
	}
	if e.goSource {
		if err := goSourcePositions(result, e.sourceName); err != nil {
			return nil, e.fail(file, CodeExpr, err.Error())
		}
	}
	return result, nil
}

// foundationMain emits the standalone adapter for units that use the legacy
// scalar runtime rather than Program. Keep foreign cleanup in a helper: Exit
// uses os.Exit for non-zero status, so a defer in main itself would be skipped.
// Register the cleanup after the panic/guard boundaries in head, because those
// boundaries may also call os.Exit while unwinding; LIFO then reaps workers
// before either boundary observes the panic.
func (e *emitter) foundationMain(head, body, tail string) string {
	cleanup := e.foreignMainCleanup()
	if cleanup == "" {
		return "func main() {\n" + head + body + tail + "}\n"
	}
	entry := e.prefix + "mainEntry"
	return "func " + entry + "() {\n" + head +
		"defer func() {\n" + cleanup + "}()\n" + body + "}\n" +
		"func main() {\n" + entry + "()\n" + tail + "}\n"
}

// prepareCgoCheckerFile models cmd/cgo for the validation check over generated
// source. The emitted bytes intentionally retain import "C", C.name calls,
// and a user's package declaration named C for the native cgo build. go/types'
// FakeImportC incorrectly lets the pseudo-package collide with that declaration,
// so only the checker tree receives a hygienic spelling for the Go object.
func prepareCgoCheckerFile(file *ast.File) {
	if file == nil || file.Scope == nil {
		return
	}
	importsC := false
	for _, spec := range file.Imports {
		if spec.Path != nil && spec.Path.Value == `"C"` {
			importsC = true
			break
		}
	}
	if !importsC {
		return
	}
	object := file.Scope.Lookup("C")
	if object == nil || object.Kind == ast.Pkg {
		return
	}
	alias := "__lower_go_C"
	used := map[string]bool{}
	selectorBases := map[*ast.Ident]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok {
			used[id.Name] = true
		}
		if sel, ok := node.(*ast.SelectorExpr); ok {
			if id, ok := ast.Unparen(sel.X).(*ast.Ident); ok && id.Name == "C" {
				selectorBases[id] = true
			}
		}
		return true
	})
	for used[alias] {
		alias += "_"
	}
	ast.Inspect(file, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if ok && id.Obj == object && !selectorBases[id] {
			id.Name = alias
		}
		return true
	})
	object.Name = alias
}

// sourceDecl is one top-level declaration's generated text keyed by the
// position of the statement it came from, for source-order emission.
type sourceDecl struct {
	pos    syntax.Pos
	source string
	text   string
}

type sourceImport struct {
	alias, path string
	aliased     bool
}

// before orders declarations by source offset; a declaration without a
// position (synthetic) sorts after every positioned one.
func (d sourceDecl) before(o sourceDecl) bool {
	if !d.pos.IsValid() || !o.pos.IsValid() {
		return d.pos.IsValid() && !o.pos.IsValid()
	}
	return d.pos.Offset() < o.pos.Offset()
}

func (e *emitter) sourceMappings(source []byte) []Mapping {
	var mappings []Mapping
	pending := -1
	for i, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "// lower:") {
			pending, _ = strconv.Atoi(strings.TrimPrefix(trimmed, "// lower:"))
			continue
		}
		if pending >= 0 && trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			m := e.marks[pending]
			m.GoLine = i + 1
			m.GoCol = len(line) - len(strings.TrimLeft(line, "\t ")) + 1
			mappings = append(mappings, m)
			pending = -1
		}
	}
	return mappings
}

type bridgeImporter struct {
	fallback types.Importer
	path     string
	cache    map[string]*types.Package
	// foreign reports that the unit carries foreign fences or imports: only
	// then does the shellexec stub declare the interp and polyglot seams, so
	// a unit without them never resolves the compiler's own packages from
	// the caller's module.
	foreign bool
}

func (i bridgeImporter) Import(path string) (*types.Package, error) {
	if pkg := i.cache[path]; pkg != nil {
		return pkg, nil
	}
	var pkg *types.Package
	var err error
	switch path {
	case i.path:
		pkg, err = i.importRuntime(path)
	case i.path + "/shellexec":
		pkg, err = i.importShellExec(path)
	default:
		return i.fallback.Import(path)
	}
	if err == nil && i.cache != nil {
		i.cache[path] = pkg
	}
	return pkg, err
}
func nodeName(n syntax.Node) string {
	if n == nil {
		return "nil"
	}
	return strings.TrimPrefix(reflect.TypeOf(n).String(), "*syntax.")
}
func (e *emitter) fail(n syntax.Node, code, msg string) error {
	var pos syntax.Pos
	if n != nil {
		pos = n.Pos()
	}
	source := syntax.SourceFile{}
	if e.sourceFile != nil {
		source, _ = e.sourceFile.SourceAt(pos)
	}
	return ErrorList{{Code: code, Msg: msg, Node: nodeName(n), Pos: pos, Source: source.Name}}
}
func (e *emitter) mark(n syntax.Node) string {
	return e.markAt(n, n.Pos())
}
func (e *emitter) markAt(n syntax.Node, pos syntax.Pos) string {
	id := len(e.marks)
	if d, ok := n.(*syntax.BashPPDecl); ok && e.goSource && d.Name != nil && d.Name.Pos().Line() > pos.Line() {
		// A spec of a grouped `var (` / `type (` declaration carries the
		// group's keyword position; it is emitted as its own declaration,
		// and gc reports diagnostics and -m notes on the spec's own line.
		pos = d.Name.Pos()
	}
	source := syntax.SourceFile{}
	if e.sourceFile != nil {
		source, _ = e.sourceFile.SourceAt(pos)
	}
	e.marks = append(e.marks, Mapping{Pos: pos, Node: nodeName(n), Source: source.Name, SourceOffset: pos.Offset() - source.Base})
	return fmt.Sprintf("// lower:%d\n", id)
}
func (e *emitter) known(name string) bool {
	global := e.globals[name]
	if e.inFunc {
		global = e.functionGlobals[name]
	}
	if e.funcs[name] || global || e.typeNames[name] || e.imports[name] != "" || e.dotNames[name] {
		return true
	}
	for i := len(e.scopes) - 1; i >= 0; i-- {
		if e.scopes[i][name] {
			return true
		}
	}
	switch name {
	case "true", "false", "nil":
		return true
	}
	return false
}

// bound distinguishes script storage from predeclared literals and callables.
func (e *emitter) bound(name string) bool {
	for i := len(e.scopes) - 1; i >= 0; i-- {
		if e.scopes[i][name] {
			return true
		}
	}
	if e.inFunc {
		return e.functionGlobals[name]
	}
	return e.globals[name]
}
func (e *emitter) bind(name string) {
	if name != "_" {
		e.scopes[len(e.scopes)-1][name] = true
		delete(e.foreignIteratorValues, name)
		e.projections.projectionBind(name, scalarProjection())
	}
}
func (e *emitter) syntheticName() string {
	for i := 0; ; i++ {
		name := fmt.Sprintf("%sunnamed%d", e.prefix, i)
		if !e.known(name) && !e.bound(name) {
			e.bind(name)
			return name
		}
	}
}
func (e *emitter) push() {
	e.iteratorScopes = append(e.iteratorScopes, e.foreignIteratorValues)
	cloned := map[string]string{}
	for name, value := range e.foreignIteratorValues {
		cloned[name] = value
	}
	e.foreignIteratorValues = cloned
	e.scopes = append(e.scopes, map[string]bool{})
	e.projections.projectionPush()
}
func (e *emitter) pop() {
	e.scopes = e.scopes[:len(e.scopes)-1]
	e.projections.projectionPop()
	if len(e.iteratorScopes) > 0 {
		e.foreignIteratorValues = e.iteratorScopes[len(e.iteratorScopes)-1]
		e.iteratorScopes = e.iteratorScopes[:len(e.iteratorScopes)-1]
	}
}
func (e *emitter) statementFlags(s *syntax.Stmt) error {
	if s.Negated || s.Background || s.Coprocess || s.Disown || len(s.Redirs) > 0 {
		return e.fail(s, CodeUnsupported, "statement flags and redirections need the shell runtime slice")
	}
	return nil
}
func (e *emitter) statement(s *syntax.Stmt) (string, error) {
	if text, handled, err := e.lexicalCopyShellAssignment(s); handled || err != nil {
		return text, err
	}
	if text, handled, err := e.carrierShellAssignment(s); handled || err != nil {
		return text, err
	}
	if text, handled, err := e.nativeShellStatement(s); handled || err != nil {
		return text, err
	}
	if e.dynamicShell(s) {
		return e.shellStatement(s)
	}
	if err := e.statementFlags(s); err != nil {
		return "", err
	}
	if s.Cmd == nil {
		return "", nil
	}
	anchor := s.Cmd.Pos()
	if e.goSource {
		anchor = goSourceInstructionPos(s.Cmd)
	}
	text, err := e.withExprLine(anchor, func() (string, error) { return e.command(s.Cmd) })
	if err != nil {
		return "", err
	}
	reset := ""
	switch n := s.Cmd.(type) {
	case *syntax.BashPPDecl, *syntax.BashPPShortDecl, *syntax.BashPPAssign, *syntax.BashPPForAssign, *syntax.BashPPIncDec, *syntax.BashPPUpdate:
		reset = "/*" + e.prefix + "reset*/"
	case *syntax.BashPPReturn:
		if len(n.Results) > 0 {
			reset = "/*" + e.prefix + "reset*/"
		}
	}
	if e.goSource {
		reset = ""
	} else if e.execution && reset != "" {
		reset = e.program() + ".SetStatus(0)\n"
	}
	if _, ok := s.Cmd.(*syntax.CallExpr); ok && hasBadSubstitution(s) {
		e.bridge = true
		failure := e.prefix + "expansionError"
		text = "if " + failure + " := " + e.prefix + "rt.TryShellStatement(func(){\n" + text + "\n}); " + failure + " != nil {" + e.operationFailure(failure) + "}"
	}
	return e.markAt(s.Cmd, anchor) + reset + text + "\n", nil
}
func (e *emitter) block(b *syntax.Block) (string, error) {
	parts, err := e.blockParts(b)
	return strings.Join(parts, ""), err
}

// blockParts emits a block one statement per element, each ending in a
// newline and led by its marker line where it has one.
func (e *emitter) blockParts(b *syntax.Block) ([]string, error) {
	e.push()
	defer e.pop()
	return e.statementList(b.Stmts)
}

// oneLineBody lays a Go-source body whose braces share a source line out on
// that one line, as gofmt keeps it, when every statement is itself one line:
// `{ return x }`. The statements' own markers are dropped — the declaration's
// //line directive already names the line — and an empty body is `{}`.
func (e *emitter) oneLineBody(b *syntax.Block, parts []string) (string, bool) {
	if !e.goSource || b == nil || !b.Lbrace.IsValid() || b.Lbrace.Line() != b.Rbrace.Line() {
		return "", false
	}
	var stmts []string
	for _, part := range parts {
		if strings.HasPrefix(part, "// lower:") {
			_, part, _ = strings.Cut(part, "\n")
		}
		part = strings.TrimSuffix(part, "\n")
		if part == "" || strings.Contains(part, "\n") {
			return "", false
		}
		stmts = append(stmts, part)
	}
	if len(stmts) == 0 {
		return " {}", true
	}
	return " { " + strings.Join(stmts, "; ") + " }", true
}
func names(lits []*syntax.Lit) []string {
	out := make([]string, len(lits))
	for i, l := range lits {
		out[i] = l.Value
	}
	return out
}
func (e *emitter) unused(ns []string) string {
	// Go source is checked by gc itself: an unused variable is the source's
	// own error, and a sink would only hide it.
	if e.goSource {
		return ""
	}
	var out string
	for _, n := range ns {
		if n != "_" {
			out += "\n_ = " + n
		}
	}
	return out
}
func (e *emitter) function(f *syntax.BashPPFuncDecl) (string, error) {
	previousProgram := e.programExpr
	e.programExpr = ""
	defer func() { e.programExpr = previousProgram }()
	if f.Agentic != nil && !e.execution {
		return "", e.fail(f, CodeUnsupported, "marked, generic and receiver functions need callable metadata lowering")
	}
	// Package functions cannot see main locals. Do not accidentally resolve a
	// script-local name while lowering a package-level function.
	savedGlobals := e.functionGlobals
	e.functionGlobals = map[string]bool{}
	for name := range e.visibleGlobals {
		e.functionGlobals[name] = true
	}
	defer func() { e.functionGlobals = savedGlobals }()
	savedFunc := e.inFunc
	e.inFunc = true
	defer func() { e.inFunc = savedFunc }()
	saved := e.scopes
	savedProjections := e.projections
	e.projections = projector{}
	e.projections.projectionPush()
	for name := range e.visibleGlobals {
		if p, ok := savedProjections.projectionLookup(name); ok {
			e.projections.projectionBind(name, p)
		}
	}
	e.projections.projectionPush()
	defer func() { e.projections = savedProjections }()
	e.scopes = []map[string]bool{{}}
	defer func() { e.scopes = saved }()
	if f.Receiver != nil {
		// The runtime path names every receiver and parameter so its
		// projections can refer to them; Go source keeps its own spelling,
		// including an unnamed or blank receiver.
		if !e.goSource {
			if f.Receiver.Name == nil {
				f.Receiver.Name = &syntax.Lit{Value: e.syntheticName()}
			} else if f.Receiver.Name.Value == "_" {
				f.Receiver.Name.Value = e.syntheticName()
			}
		}
		typ := f.Receiver.RecvType.Value
		if f.Receiver.Pointer {
			typ = "*" + typ
		}
		if f.Receiver.Name != nil {
			e.bind(f.Receiver.Name.Value)
			info := e.projectionType(typ, nil)
			info.receiver = f.Receiver.Pointer
			e.projections.projectionBind(f.Receiver.Name.Value, info)
		}
		for _, param := range f.Receiver.TypeParams {
			e.bind(param.Value)
		}
	}
	for _, p := range f.TypeParams {
		for _, n := range p.Names {
			e.bind(n.Value)
		}
	}
	for _, p := range f.Params {
		if e.goSource {
			break
		}
		if len(p.Names) == 0 {
			p.Names = []*syntax.Lit{{Value: e.syntheticName()}}
		} else {
			for _, n := range p.Names {
				if n.Value == "_" {
					n.Value = e.syntheticName()
				}
			}
		}
	}
	signature, err := e.signature(f.Params, e.publicResults(f), f.Body)
	if err != nil {
		return "", err
	}
	savedNames := e.resultNames
	e.resultNames = resultFieldNames(f.Results)
	defer func() { e.resultNames = savedNames }()
	savedResults := e.resultTypes
	e.resultTypes = e.returnTypes(f.Results)
	defer func() { e.resultTypes = savedResults }()
	recv := ""
	if f.Receiver != nil {
		r := f.Receiver
		typ := r.RecvType.Value
		if r.Pointer {
			typ = "*" + typ
		}
		if len(r.TypeParams) > 0 {
			typ += "[" + strings.Join(names(r.TypeParams), ",") + "]"
		}
		recv = "(" + typ + ") "
		if r.Name != nil {
			recv = "(" + r.Name.Value + " " + typ + ") "
		}
	}
	generics, err := e.typeParams(f.TypeParams)
	if err != nil {
		return "", err
	}
	if f.Body == nil {
		// A body-less declaration is a compile-time fact of Go source: the
		// body comes from assembly, a //go:linkname or a //go:wasmimport, and
		// gc decides whether one is present (the generic form is the
		// checker's error before lowering). Go source passes it through as
		// written; the runtime path wraps a body it does not have, and a
		// Bash++ script cannot spell one, so both keep refusing.
		if e.goSource && !e.execution {
			return e.mark(f) + "func " + recv + e.goName(f.Name.Value) + generics + signature + "\n", nil
		}
		return "", e.fail(f, CodeUnsupported, "function declaration without body")
	}
	parts, err := e.blockParts(f.Body)
	if err != nil {
		return "", err
	}
	body := strings.Join(parts, "")
	if e.execution {
		body = e.program() + " = " + e.program() + ".LexicalScope(" + e.lexicalNames(e.functionGlobals) + ")\n" + body
		return e.runtimeFunction(f, signature, body, generics)
	}
	return e.mark(f) + "func " + recv + e.goName(f.Name.Value) + generics + signature + e.bodyText(f.Body, parts) + "\n", nil
}

// bodyText lays out an emitted function body: on one line where the Go
// source had it so (oneLineBody), else braced on its own lines.
func (e *emitter) bodyText(b *syntax.Block, parts []string) string {
	if text, ok := e.oneLineBody(b, parts); ok {
		return text
	}
	return " {\n" + strings.Join(parts, "") + "}"
}
func scalarType(s string) bool {
	switch s {
	case "bool", "string", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64", "byte", "rune", "error", "any":
		return true
	}
	return false
}
func (e *emitter) fields(fs []*syntax.BashPPField) (string, error) {
	var out []string
	for _, f := range fs {
		ns := names(f.Names)
		for _, n := range ns {
			e.bind(n)
		}
		s := strings.Join(ns, ",")
		if s != "" {
			s += " "
		}
		typ := "any"
		if inferred := e.inferredParams[f]; inferred != "" {
			typ = inferred
		}
		if f.FieldType != nil || f.FieldTypeExpr != nil {
			var err error
			if f.FieldType != nil && f.FieldType.Value == "func" {
				typ = e.callableParams[f]
				if typ == "" {
					return "", e.fail(f, CodeUnsupported, "func parameter needs an inferable native call-site signature")
				}
			} else {
				typ, err = e.fieldType(f)
			}
			if err != nil {
				return "", err
			}
		} else if f.Ellipsis.IsValid() {
			typ = "...any"
		}
		if f.Ellipsis.IsValid() && !strings.HasPrefix(typ, "...") {
			typ = "..." + typ
		}
		for _, name := range ns {
			e.projections.projectionBind(name, e.projectionType(typ, nil))
		}
		out = append(out, s+typ)
	}
	return strings.Join(out, ", "), nil
}
func (e *emitter) command(c syntax.Command) (string, error) {
	if e.goSource {
		if text, handled, err := e.goSourceCommand(c); handled {
			return text, err
		}
	}
	switch n := c.(type) {
	case *syntax.SourceBlock:
		return "", nil
	case *syntax.BashPPAgenticBlock:
		return e.agenticBlock(n)
	case *syntax.BashPPSend:
		return e.runtimeSend(n, e.runtimeScope())
	case *syntax.BashPPReceive:
		value, err := e.runtimeReceive(n, e.runtimeScope(), false)
		return "_ = " + value, err
	case *syntax.BashPPClose:
		return e.runtimeClose(n, e.runtimeScope())
	case *syntax.BashPPSelect:
		return e.runtimeSelect(n, e.runtimeScope(), e.runtimeStatements)
	case *syntax.BashPPGo:
		return e.programGo(n)

	case *syntax.Subshell:
		return e.nativeSubshell(n)
	case *syntax.DeclClause:
		return e.readonlyDeclare(n, ReadonlyContext{State: e.program() + ".Readonly"})
	case *syntax.BashPPCommandCall:
		tail, err := e.call(n.Call)
		if err != nil {
			return "", err
		}
		return e.shellWithTail(&syntax.CallExpr{Args: n.Before}, tail)

	case *syntax.ForClause:
		return e.shellFor(n)
	case *syntax.BashPPConstGroup:
		return e.constGroup(n)
	case *syntax.BashPPRange:
		return e.rangeStmt(n)
	case *syntax.BashPPDecl:
		if n.Kw.Value != "var" && n.Kw.Value != "const" {
			return e.declarationType(n)
		}
		if len(n.TypeParams) > 0 || len(n.StructFields) > 0 || len(n.EnumMembers) > 0 {
			return "", e.fail(n, CodeUnsupported, "composite declaration")
		}
		typ := ""
		if e.goSource && inferredDeclType(n) {
			// The converter spells the inferred type of `var x = f()` for
			// the interpreter; Go infers it from the initializer itself.
		} else if n.DeclTypeExpr != nil {
			x, err := e.typeExpr(n.DeclTypeExpr)
			if err != nil {
				return "", err
			}
			typ = " " + x
		} else if n.DeclType != nil {
			x, err := e.typeSpelling(n.DeclType, n.DeclType.Value)
			if err != nil {
				return "", err
			}
			typ = " " + x
		}
		init := ""
		var err error
		_, foldedVarConstant := n.InitExpr.(*syntax.BashPPBasicLit)
		if e.goSource && len(n.Init) > 0 && (n.Kw.Value == "const" || n.Kw.Value == "var" && foldedVarConstant) {
			// Init is the written Go initializer carrier; InitExpr may hold a
			// checker-derived value for interpretation. Compiled native Go must
			// keep the source expression and let gc evaluate it; that distinction
			// also matters for vars initialized by imported named constants.
			init, err = e.wordSequence(n.Init)
		} else if n.InitExpr != nil {
			init, err = e.expr(n.InitExpr)
		} else if len(n.Init) > 0 {
			init, err = e.wordSequence(n.Init)
		}
		if err != nil {
			return "", err
		}
		if !e.goSource && init != "" && init != "nil" && strings.HasPrefix(strings.TrimSpace(typ), "*") {
			source := scalarProjection()
			if n.InitExpr != nil {
				source = e.projectionExpr(n.InitExpr)
			} else if len(n.Init) == 1 {
				source = e.projectionWord(n.Init[0])
			}
			if source.kind != projectPointer {
				element := strings.TrimPrefix(strings.TrimSpace(typ), "*")
				init = "func() *" + element + " { var value " + element + " = " + init + "; return &value }()"
			}
		}
		if init != "" {
			init = " = " + init
		}
		e.bind(n.Name.Value)
		projection := e.declarationProjection(n)
		projection.constant = n.Kw.Value == "const"
		if projection.kind == projectFloat && !projection.hasText && !projection.runtimeFloat {
			return "", e.fail(n, CodeUnsupported, "initialized typed float needs certified scalar conversion semantics")
		}
		e.projections.projectionBind(n.Name.Value, projection)
		return n.Kw.Value + " " + n.Name.Value + typ + init + e.unused([]string{n.Name.Value}), nil
	case *syntax.BashPPShortDecl:
		iterator := ""
		if n.Call != nil {
			f, ok := e.foreignFunctions[strings.Join(names(n.Call.Fun), ".")]
			if ok {
				iterator = f.export.Signature.Iterator
			}
		}
		if len(n.Rhs) == 1 {
			if iterator == "" {
				iterator = e.foreignIteratorValues[n.Rhs[0].Lit()]
			}
		}
		defer func() {
			if len(n.Lhs) == 1 && iterator != "" {
				if e.foreignIteratorValues == nil {
					e.foreignIteratorValues = map[string]string{}
				}
				e.foreignIteratorValues[n.Lhs[0].Value] = iterator
			}
		}()
		if text, handled, err := e.shortResultCall(n); handled || err != nil {
			return text, err
		}
		if n.Recv != nil {
			return e.receiveDeclaration(n)
		}
		var rhs string
		var err error
		switch {
		case n.Call != nil:
			savedWant := e.resultCallWant
			e.resultCallWant = len(n.Lhs)
			rhs, err = e.call(n.Call)
			e.resultCallWant = savedWant
		case n.Expr != nil:
			if assertion, ok := n.Expr.(*syntax.BashPPTypeAssertExpr); ok {
				rhs, err = e.valueAssertion(assertion, len(n.Lhs) == 2)
			} else {
				rhs, err = e.expr(n.Expr)
			}
		case e.goSource && len(n.RhsExprs) > 0:
			parts := make([]string, len(n.RhsExprs))
			for i, x := range n.RhsExprs {
				parts[i], err = e.expr(x)
				if err != nil {
					return "", err
				}
			}
			rhs = strings.Join(parts, ", ")
		case n.FuncLit != nil:
			rhs, err = e.literal(n.FuncLit)
		case len(n.MethodValue) > 0:
			if e.execution {
				rhs, err = e.methodHandle(n)
			} else {
				rhs = strings.Join(names(n.MethodValue), ".")
			}
		case n.MakeChan != nil:
			rhs, err = e.runtimeMakeChannel(n.MakeChan, e.runtimeScope())
		case n.Recv != nil:
			rhs, err = e.runtimeReceive(n.Recv, e.runtimeScope(), len(n.Lhs) == 2)
		default:
			if len(n.Lhs) > 1 && len(n.Rhs) > 1 {
				var parts []string
				for _, w := range n.Rhs {
					x, problem := e.valueWord(w)
					if problem != nil {
						return "", problem
					}
					parts = append(parts, x)
				}
				rhs = strings.Join(parts, ", ")
			} else {
				rhs, err = e.wordSequence(n.Rhs)
			}
		}
		if err != nil {
			return "", err
		}
		if !e.goSource && len(n.Lhs) == 1 && n.Call == nil && n.FuncLit == nil {
			if value, err := types.Eval(token.NewFileSet(), nil, token.NoPos, rhs); err == nil && value.Value != nil && value.Value.Kind() == constant.Int {
				if _, fits := constant.Int64Val(value.Value); !fits {
					e.bigIntegers = true
					rhs = "func() *" + e.prefix + "big.Int { v, _ := new(" + e.prefix + "big.Int).SetString(" + strconv.Quote(value.Value.ExactString()) + ",10); return v }()"
				}
			}
		}
		if text, handled, err := e.checkedShortDeclaration(n, rhs); handled || err != nil {
			return text, err
		}
		ns := names(n.Lhs)
		for i, name := range ns {
			p := scalarProjection()
			switch {
			case n.Call != nil:
				p = e.callProjection(n.Call, i)
			case n.MakeChan != nil:
				elem, _ := e.makeElement(n.MakeChan)
				p.sourceType = "chan " + elem
			case n.Expr != nil:
				if i == 0 {
					p = e.projectionExpr(n.Expr)
				}
			case len(n.Rhs) > i:
				p = e.projectionWord(n.Rhs[i])
			}
			e.bind(name)
			e.projections.projectionBind(name, p)
		}
		post := ""
		if e.isRecover(n.Call) {
			if len(ns) != 1 {
				return "", e.fail(n, CodeResult, "recover yields one value")
			}
			post = e.recovered(ns[0])
		}
		return strings.Join(ns, ", ") + " := " + rhs + post + e.unused(ns), nil
	case *syntax.BashPPCall:
		if e.isRecover(n) {
			if e.execution {
				return e.program() + ".Recovered(" + e.userRecover() + ")", nil
			}
			e.panicSupport = true
			return "if " + e.prefix + "recovered := " + e.userRecover() + "; " + e.prefix + "recovered == nil { /*" + e.prefix + "status1*/ } else { " + e.prefix + "popPanic(); /*" + e.prefix + "status0*/ }", nil
		}
		if e.liveProcessMethod(n) && (n.Fun[1].Value == "Wait" || n.Fun[1].Value == "Close") {
			return e.program() + "." + n.Fun[1].Value + "Process(" + e.goName(n.Fun[0].Value) + ")", nil
		}
		text, err := e.call(n)
		if !e.inFunc && len(n.Fun) == 1 && !e.funcs[n.Fun[0].Value] && (n.Fun[0].Value == "print" || n.Fun[0].Value == "println") {
			text += "\n/*" + e.prefix + "status0*/"
		}
		return text, err
	case *syntax.BashPPReturn:
		return e.returnStatement(n)
	case *syntax.BashPPForAssign:
		e.projections.projectionInvalidate(n.Name.Value)
		x, err := e.expr(n.Expr)
		return n.Name.Value + " = " + x, err
	case *syntax.BashPPAssign:
		if text, handled, err := e.richCallAssignment(n); handled || err != nil {
			return text, err
		}
		if e.readonly {
			if text, handled, err := e.guardedAssignment(n); handled || err != nil {
				return text, err
			}
		}
		for _, name := range n.Names {
			e.projections.projectionInvalidate(name.Value)
		}
		if n.Call != nil {
			savedWant := e.resultCallWant
			e.resultCallWant = len(n.Names)
			rhs, err := e.call(n.Call)
			e.resultCallWant = savedWant
			if err != nil {
				return "", err
			}
			if len(n.Names) == 0 {
				return "", e.fail(n, CodeUnsupported, "structured call assignment")
			}
			if len(n.Names) > 1 {
				return e.tupleCallAssignment(n, rhs)
			}
			return strings.Join(names(n.Names), ", ") + " = " + rhs, nil
		}
		if len(n.Names) > 0 {
			var rhs []string
			for _, x := range n.ValueExprs {
				v, err := e.expr(x)
				if err != nil {
					return "", err
				}
				rhs = append(rhs, v)
			}
			if len(rhs) == 0 {
				for _, w := range n.Values {
					v, err := e.valueWord(w)
					if err != nil {
						return "", err
					}
					rhs = append(rhs, v)
				}
			}
			return strings.Join(names(n.Names), ", ") + " = " + strings.Join(rhs, ", "), nil
		}
		if n.TargetExpr != nil && n.ValueExpr != nil {
			l, err := e.expr(n.TargetExpr)
			if err != nil {
				return "", err
			}
			r, err := e.expr(n.ValueExpr)
			return l + " = " + r, err
		}
		return "", e.fail(n, CodeUnsupported, "assignment variant lacks typed target/value")
	case *syntax.BashPPIncDec:
		target := ""
		var err error
		if n.Target != nil {
			target, err = e.expr(n.Target)
		} else if n.Name != nil {
			target = n.Name.Value
		} else {
			return "", e.fail(n, CodeUnsupported, "update target")
		}
		return target + n.Op.Value, err
	case *syntax.BashPPUpdate:
		if n.Target == nil || n.Value == nil {
			return "", e.fail(n, CodeExpr, "compound assignment lacks typed expressions")
		}
		l, err := e.expr(n.Target)
		if err != nil {
			return "", err
		}
		r, err := e.expr(n.Value)
		if err != nil {
			return "", err
		}
		typ := e.projectionExpr(n.Target).sourceType
		if e.numericUpdateNeedsCheck(n, typ, r) {
			e.bridge = true
			return e.numericUpdate(n, l, r, typ)
		}
		return l + " " + n.Op.Value + " " + r, nil
	case *syntax.BashPPIf:
		return e.ifStmt(n)
	case *syntax.BashPPFor:
		return e.forStmt(n)
	case *syntax.BashPPSwitch:
		return e.switchStmt(n)
	case *syntax.BashPPBranch:
		return e.branchStmt(n)
	case *syntax.BashPPLabeled:
		return e.labeledStmt(n)
	case *syntax.BashPPGoto:
		return "goto " + n.Label.Value, nil
	case *syntax.BashPPDefer:
		if !e.goSource && n.Call != nil && len(n.Call.Fun) == 1 && n.Call.Fun[0].Value == "panic" && !e.funcs["panic"] {
			if len(n.Call.Args) != 1 {
				return "", e.fail(n, CodeResult, "panic takes one argument")
			}
			e.panicSupport = true
			x, err := e.argument(n.Call.Args[0])
			if e.execution {
				return "defer func(p *" + e.prefix + "rt.Program,v any){panic(p.PushPanic(v))}(" + e.program() + "," + x + ")", err
			}
			return "defer func(v any) { panic(" + e.prefix + "pushPanic(v)) }(" + x + ")", err
		}
		if n.Call != nil && len(n.Call.Fun) == 1 {
			if f := e.functionDecls[n.Call.Fun[0].Value]; f != nil && (hasSharpDefaults(f) || len(n.Call.ArgNames) > 0) {
				return e.sharpDefer(n.Call, f)
			}
		}
		x, err := e.call(n.Call)
		return "defer " + x, err
	case *syntax.Block:
		b, err := e.block(n)
		return "{\n" + b + "}", err
	case *syntax.CallExpr:
		return e.shell(n)
	default:
		return "", e.fail(c, CodeUnsupported, "certified node is not implemented by the foundation: "+nodeName(c))
	}
}
func (e *emitter) ifStmt(n *syntax.BashPPIf) (string, error) {
	e.push()
	defer e.pop()
	init := ""
	var initializer syntax.Command = n.InitStmt
	if initializer == nil && n.Init != nil {
		initializer = n.Init
	}
	if initializer != nil {
		x, err := e.command(initializer)
		if err != nil {
			return "", err
		}
		init = x + "\n"
	}
	cond, err := e.expr(n.Cond)
	if err != nil {
		return "", err
	}
	body, err := e.block(n.Then)
	if err != nil {
		return "", err
	}
	out := "if " + cond + " {\n" + body + "}"
	if n.Else != nil {
		x, err := e.command(n.Else)
		if err != nil {
			return "", err
		}
		out += " else " + x
	}
	if init != "" {
		if header, ok := e.goSourceHeaderInit(init); ok {
			// Go source keeps its own `if init; cond {` header: hoisting the
			// initializer into an enclosing block moves it to a line of its
			// own, and gc then reports its operations on the wrong line.
			return "if " + header + "; " + cond + " {\n" + body + "}" + strings.TrimPrefix(out, "if "+cond+" {\n"+body+"}"), nil
		}
		out = "{\n" + init + out + "\n}"
	}
	return out, nil
}

// goSourceHeaderInit reports whether an emitted if/switch initializer is the
// one simple statement a Go statement header can carry, and returns it
// without its trailing newline. Anything else (a checked binding's guard, a
// sink, several statements) keeps the enclosing-block form.
func (e *emitter) goSourceHeaderInit(init string) (string, bool) {
	if !e.goSource {
		return "", false
	}
	init = strings.TrimSuffix(init, "\n")
	if strings.TrimSpace(init) == "" {
		return "", false
	}
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _() {\nif "+init+"; true {}\n}\n", parser.SkipObjectResolution)
	if err != nil || len(f.Decls) != 1 {
		return "", false
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Body == nil || len(fn.Body.List) != 1 {
		return "", false
	}
	stmt, ok := fn.Body.List[0].(*ast.IfStmt)
	if !ok || stmt.Init == nil {
		return "", false
	}
	return init, true
}
func (e *emitter) forStmt(n *syntax.BashPPFor) (string, error) {
	e.push()
	defer e.pop()
	init, post, cond := "", "", ""
	var err error
	if n.Init != nil {
		init, err = e.command(n.Init)
		if err != nil {
			return "", err
		}
	}
	if n.Post != nil {
		post, err = e.command(n.Post)
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(strings.TrimSpace(post), "if ") {
		post = "func(){" + post + "}()"
	}
	if n.Cond != nil {
		cond, err = e.expr(n.Cond)
		if err != nil {
			return "", err
		}
	}
	target := e.pushBranchTarget(true)
	body, err := e.block(n.Body)
	e.popBranchTarget()
	if err != nil {
		return "", err
	}
	header := cond
	if n.FirstSemi.IsValid() {
		if i := strings.Index(init, "\n_ = "); i >= 0 {
			init = init[:i]
		}
		// Checked bindings expand to a declaration plus a failure guard. Keep
		// that compound initializer in the loop's enclosing lexical block.
		if strings.Contains(init, "\n") {
			header = "; " + cond + "; " + post
		} else {
			header = init + "; " + cond + "; " + post
			init = ""
		}
	}
	out := "for " + header + " {\n" + body + "}"
	if target.label != "" {
		out = target.label + ":\n" + out
	}
	if init != "" {
		out = "{\n" + init + "\n" + out + "\n}"
	}
	return out, nil
}

// expr emits one expression. Under goSource an expression written on a
// different source line than the expression or statement containing it is led
// by an inline /*line file:L:C*/ directive: the generated file lays a
// multi-line source expression out on fewer lines, and gc reports each
// operation (bounds and nil checks, inlining, escape and liveness notes, the
// assembly of each operand) on the line the directive in effect names, so
// every operand keeps the source line it was written on.
func (e *emitter) expr(x syntax.BashPPExpr) (string, error) {
	if !e.goSource || x == nil {
		return e.exprText(x)
	}
	if v := reflect.ValueOf(x); v.Kind() == reflect.Pointer && v.IsNil() {
		return e.exprText(x)
	}
	pos := x.Pos()
	if !pos.IsValid() || pos.Line() == 0 {
		return e.exprText(x)
	}
	prefix := ""
	if e.exprLine != 0 && pos.Line() != e.exprLine {
		prefix = e.inlineSourceLineDirective(pos)
	}
	saved := e.exprLine
	e.exprLine = pos.Line()
	text, err := e.exprText(x)
	e.exprLine = saved
	if err != nil || text == "" || prefix == "" {
		return text, err
	}
	// The directive governs the character right after it: keep it after
	// any leading line break of the emitted text.
	rest := strings.TrimLeft(text, " \t\n")
	return text[:len(text)-len(rest)] + prefix + rest, nil
}

// faultTokenDirective positions the token of an index, slice or selector
// operation (its '[' or selector name) that gc reports the operation's bounds
// or nil check on, when it was written on another line than the operand.
func (e *emitter) faultTokenDirective(operand syntax.BashPPExpr, token syntax.Pos) string {
	if !e.goSource || operand == nil || !token.IsValid() {
		return ""
	}
	if v := reflect.ValueOf(operand); v.Kind() == reflect.Pointer && v.IsNil() {
		return ""
	}
	if start := operand.Pos(); start.IsValid() && start.Line() == token.Line() {
		return ""
	}
	return e.inlineSourceLineDirective(token)
}

// withExprLine emits under the source line of pos as the enclosing position
// for expr's inline directives.
func (e *emitter) withExprLine(pos syntax.Pos, emit func() (string, error)) (string, error) {
	saved := e.exprLine
	if e.goSource && pos.IsValid() {
		e.exprLine = pos.Line()
	}
	defer func() { e.exprLine = saved }()
	return emit()
}

func (e *emitter) exprText(x syntax.BashPPExpr) (string, error) {
	switch n := x.(type) {
	case *syntax.BashPPFuncLit:
		if e.goSource {
			if text, ok, err := e.forwardedCallee(n); ok || err != nil {
				return text, err
			}
		}
		return e.literal(n)
	case *syntax.BashPPCall:
		return e.call(n)
	case *syntax.BashPPCompositeLit:
		if e.goSource {
			return e.compositeExpr(n)
		}
		if text, handled, err := e.promotedCompositeExpr(n, e.declaredTypes); handled || err != nil {
			return text, err
		}
		return e.compositeExpr(n)
	case *syntax.BashPPSelectorExpr:
		if e.pythonExpr(n.X) || e.pythonModuleSelector(n) {
			return e.pythonAttr(n)
		}
		return e.checkedSelector(n)
	case *syntax.BashPPTypeAssertExpr:
		return e.valueAssertion(n, false)
	case *syntax.BashPPSliceExpr:
		base, err := e.expr(n.X)
		if err != nil {
			return "", err
		}
		bounds := []string{"", ""}
		for i, x := range []syntax.BashPPExpr{n.Low, n.High} {
			if x != nil {
				bounds[i], err = e.expr(x)
				if err != nil {
					return "", err
				}
			}
		}
		if n.Max != nil {
			x, err := e.expr(n.Max)
			if err != nil {
				return "", err
			}
			bounds = append(bounds, x)
		}
		return base + e.faultTokenDirective(n.X, n.Lbrack) + "[" + strings.Join(bounds, ":") + "]", nil
	case *syntax.BashPPIndexExpr:
		a, err := e.expr(n.X)
		if err != nil {
			return "", err
		}
		b, err := e.expr(n.Index)
		return a + e.faultTokenDirective(n.X, n.Lbrack) + "[" + b + "]", err
	case *syntax.BashPPAddressExpr:
		a, err := e.expr(n.X)
		return "&" + a, err
	case *syntax.BashPPDerefExpr:
		a, err := e.expr(n.X)
		if err != nil {
			return "", err
		}
		return e.checkedDeref(n, a, ""), nil
	case *syntax.BashPPNewExpr:
		if n.Init != nil {
			a, err := e.expr(n.Init)
			return "new(" + a + ")", err
		}
		a, err := e.typeExpr(n.AllocType)
		return "new(" + a + ")", err
	case *syntax.BashPPBasicLit:
		return n.Value.Value, nil
	case *syntax.BashPPIdent:
		if n.Name.Value == "iota" && e.iotaValue != nil {
			return strconv.Itoa(*e.iotaValue), nil
		}
		if !e.known(n.Name.Value) && !(e.goSource && scalarType(n.Name.Value)) {
			return "", e.fail(n, CodeUndefined, "undefined: "+n.Name.Value)
		}
		return e.goName(n.Name.Value), nil
	case *syntax.BashPPParenExpr:
		v, err := e.expr(n.X)
		return "(" + v + ")", err
	case *syntax.BashPPUnaryExpr:
		v, err := e.expr(n.X)
		return e.group(n.Op.Value + v), err
	case *syntax.BashPPBinaryExpr:
		l, err := e.expr(n.X)
		if err != nil {
			return "", err
		}
		r, err := e.expr(n.Y)
		return e.group(l + " " + n.Op.Value + " " + r), err
	case *syntax.BashPPConvertExpr:
		if e.goSource && n.ConvTypeExpr != nil {
			target, err := e.typeExpr(n.ConvTypeExpr)
			if err != nil {
				return "", err
			}
			v, err := e.expr(n.X)
			return conversionType(target) + "(" + v + ")", err
		}
		if !scalarType(n.ConvType.Value) && !(e.goSource && (e.typeNames[n.ConvType.Value] || n.ConvType.Value == "complex64" || n.ConvType.Value == "complex128")) {
			return "", e.fail(n, CodeUnsupported, "non-scalar conversion")
		}
		v, err := e.expr(n.X)
		return n.ConvType.Value + "(" + v + ")", err
	default:
		return "", e.fail(x, CodeUnsupported, "typed expression not implemented: "+nodeName(x))
	}
}

// conversionType spells a conversion's type as its callee. The converter
// resolves the parentheses Go requires around a pointer, channel or function
// type in that position, so they are put back from the type's own spelling.
func conversionType(typ string) string {
	for _, prefix := range []string{"*", "<-", "chan ", "func"} {
		if strings.HasPrefix(typ, prefix) {
			return "(" + typ + ")"
		}
	}
	return typ
}

// group parenthesises an emitted operand. The Bash++ path builds expressions
// the shell parser never grouped, so every operator and callee is wrapped; Go
// source carries its own BashPPParenExpr nodes and gets its precedence back.
func (e *emitter) group(text string) string {
	if e.goSource {
		return text
	}
	return "(" + text + ")"
}
func (e *emitter) call(c *syntax.BashPPCall) (string, error) {
	if text, handled, err := e.processCall(c); handled {
		return text, err
	}
	// The short declaration's binding count is this call's opt-in arity and
	// nobody else's: a call nested in the arguments must not inherit it.
	want := e.resultCallWant
	e.resultCallWant = 0
	defer func() { e.resultCallWant = want }()
	if e.pythonCallKind(c) != "" {
		return e.pythonCall(c, want)
	}
	if c.CalleeExpr != nil {
		callee, err := e.expr(c.CalleeExpr)
		if err != nil {
			return "", err
		}
		typeargs, err := e.typeArgs(c.TypeArgs)
		if err != nil {
			return "", err
		}
		args := make([]string, len(c.Args))
		for i := range c.Args {
			args[i], err = e.callArgument(c, i)
			if err != nil {
				return "", err
			}
		}
		spread := ""
		if c.Ellipsis.IsValid() {
			spread = "..."
		}
		return e.group(callee) + typeargs + e.callArgs(c, args, spread), nil
	}
	frame := e.resultCallFrame
	e.resultCallFrame = ""
	defer func() { e.resultCallFrame = frame }()
	invocation := e.program()
	if frame != "" {
		invocation += ".WithResults(" + frame + ")"
	}
	if len(c.Fun) == 1 {
		if f := e.functionDecls[c.Fun[0].Value]; f != nil {
			if text, handled, err := e.carrierArgumentCall(c, f, invocation); handled || err != nil {
				return text, err
			}
		}
	}
	if e.readonly {
		if text, handled, err := e.guardedBuiltin(c); handled || err != nil {
			return text, err
		}
	}
	if len(c.Fun) == 1 {
		if f := e.functionDecls[c.Fun[0].Value]; f != nil && (hasSharpDefaults(f) || len(c.ArgNames) > 0) {
			return e.sharpCall(c, f)
		}
	}
	if len(c.ArgNames) > 0 {
		return "", e.fail(c, CodeUnsupported, "named arguments require a resolved callable signature")
	}
	if c.FuncLit != nil {
		callee, err := e.literal(c.FuncLit)
		if err != nil {
			return "", err
		}
		var args []string
		for i := range c.Args {
			x, err := e.callArgument(c, i)
			if err != nil {
				return "", err
			}
			args = append(args, x)
		}
		if e.execution {
			args = append([]string{invocation, e.callSite(c, "func")}, args...)
		}
		return e.group(callee) + "(" + strings.Join(args, ",") + ")", nil
	}
	if len(c.Fun) == 0 {
		return "", e.fail(c, CodeExpr, "missing callable")
	}
	qualifiedParts := make([]string, len(c.Fun))
	for i, part := range c.Fun {
		qualifiedParts[i] = part.Value
	}
	if len(qualifiedParts) > 1 && e.cgoAliases[qualifiedParts[0]] {
		qualifiedParts[0] = "C"
	}
	if foreign, ok := e.foreignFunctions[strings.Join(qualifiedParts, ".")]; ok {
		var args []string
		for i := range c.Args {
			x, err := e.callArgument(c, i)
			if err != nil {
				return "", err
			}
			args = append(args, x)
		}
		name := strings.Join(qualifiedParts, ".")
		if foreign.alias == "go" {
			name = fmt.Sprintf("%sforeignAlias%d.%s", e.prefix, foreign.plan, foreign.export.Name)
		}
		if foreignScalarAdapter(foreign.export) {
			name = foreignScalarAdapterName(fmt.Sprintf("%sforeign%d", e.prefix, foreign.plan), foreign.export)
		}
		if e.execution {
			args = append([]string{e.program()}, args...)
		}
		call := name + "(" + strings.Join(args, ",") + ")"
		if want == len(foreign.export.Signature.Results)+1 && !foreign.export.Signature.Dynamic {
			return e.framedForeignErrCall(c, foreign, frame)
		}
		if frame != "" {
			if types := e.callResultTypes(c); len(types) > 0 {
				return e.framedForeignCall(c, call, frame, types), nil
			}
		}
		return call, nil
	}
	if len(c.Fun) > 1 && e.execution && e.imports[c.Fun[0].Value] == "" {
		return e.methodCall(c)
	}
	if len(c.Fun) > 1 {
		var args []string
		for i := range c.Args {
			x, err := e.callArgument(c, i)
			if err != nil {
				return "", err
			}
			args = append(args, x)
		}
		callee := strings.Join(qualifiedParts, ".")
		if c.PointerMethodExpr {
			callee = "(*" + c.Fun[0].Value + ")." + c.Fun[1].Value
		}
		spread := ""
		if c.Ellipsis.IsValid() {
			spread = "..."
		}
		typeargs, err := e.typeArgs(c.TypeArgs)
		if err != nil {
			return "", err
		}
		return callee + typeargs + "(" + strings.Join(args, ",") + spread + ")", nil
	}
	name := c.Fun[0].Value
	if !e.known(name) && !scalarType(name) && !nativeBuiltin(name) && name != "print" && name != "println" {
		return "", e.fail(c, CodeUndefined, "undefined callable: "+name)
	}
	var args []string
	if c.ArgType != nil {
		typ, err := e.typeExpr(c.ArgType)
		if err != nil {
			return "", err
		}
		args = append(args, typ)
	}
	for i, w := range c.Args {
		if i == 0 && c.ArgType != nil {
			continue
		}
		if i == 0 && name == "make" && !e.funcs[name] && c.ArgType == nil {
			var spelling strings.Builder
			_ = syntax.NewPrinter().Print(&spelling, w)
			typ, err := e.typeSpelling(w, spelling.String())
			if err != nil {
				return "", err
			}
			args = append(args, typ)
			continue
		}
		x, err := e.callArgument(c, i)
		if err != nil {
			return "", err
		}
		args = append(args, x)
	}
	if members := e.enumMembers[name]; len(members) > 0 && len(args) == 1 {
		if _, err := strconv.ParseInt(args[0], 0, 64); err != nil {
			return "", e.fail(c, CodeUnsupported, "nonconstant enum conversion requires runtime membership guard")
		}
	}
	if e.goSource && !e.funcs[name] && (name == "panic" || name == "recover") {
		return name + e.callArgs(c, args, ""), nil
	}
	if !e.funcs[name] && (name == "print" || name == "println") {
		if e.goSource {
			return name + e.callArgs(c, args, ""), nil
		}
		for i, w := range c.Args {
			projected, err := e.projectionArgument(w, args[i])
			if err != nil {
				return "", err
			}
			args[i] = projected
		}
		e.output = true
		if e.execution {
			method := "Print"
			if name == "println" {
				method = "Println"
			}
			return e.program() + "." + method + "(" + strings.Join(args, ",") + ")", nil
		}
		if name == "println" {
			return e.prefix + "fmt.Println(" + strings.Join(args, ", ") + ")", nil
		}
		for i, a := range args {
			args[i] = e.prefix + "fmt.Sprint(" + a + ")"
		}
		return e.prefix + "fmt.Print(" + strings.Join(args, ", ") + ")", nil
	}
	spread := ""
	if c.Ellipsis.IsValid() {
		spread = "..."
	}
	if !e.funcs[name] && name == "panic" {
		if len(args) != 1 {
			return "", e.fail(c, CodeResult, "panic takes one argument")
		}
		e.panicSupport = true
		if e.execution {
			return "panic(" + e.program() + ".PushPanic(" + args[0] + "))", nil
		}
		return "panic(" + e.prefix + "pushPanic(" + args[0] + "))", nil
	}
	if !e.funcs[name] && name == "recover" {
		if len(args) != 0 {
			return "", e.fail(c, CodeResult, "recover takes no arguments")
		}
		e.panicSupport = true
		return e.userRecover(), nil
	}
	typeargs, err := e.typeArgs(c.TypeArgs)
	if err != nil {
		return "", err
	}
	if e.execution && (e.funcs[name] || e.bound(name)) {
		args = append([]string{invocation, e.callSite(c, name)}, args...)
	}
	return e.goName(name) + typeargs + e.callArgs(c, args, spread), nil
}

// callArgs spells a call's parenthesised argument list. Go source keeps the
// lines the input put its arguments on (goBracketed): gc reports an escape
// note at the argument's own line. Every other unit, and a call whose
// argument list the emitter extended (a runtime invocation, a spelled
// make type), keeps the one-line spelling.
func (e *emitter) callArgs(c *syntax.BashPPCall, args []string, spread string) string {
	if !e.goSource || len(args) != len(c.Args) || !c.Lparen.IsValid() || !c.Rparen.IsValid() {
		return "(" + strings.Join(args, ", ") + spread + ")"
	}
	items := append([]string(nil), args...)
	if spread != "" && len(items) > 0 {
		items[len(items)-1] += spread
	}
	starts, ends := make([]syntax.Pos, len(c.Args)), make([]syntax.Pos, len(c.Args))
	for i, w := range c.Args {
		starts[i], ends[i] = w.Pos(), w.End()
		// goBracketed puts the argument on its own source line: a leading
		// inline directive naming exactly that position is redundant.
		if directive := e.inlineSourceLineDirective(starts[i]); directive != "" {
			items[i] = strings.TrimPrefix(items[i], directive)
		}
	}
	return goBracketed("(", ")", c.Lparen, c.Rparen, items, starts, ends, ",")
}
