package lower

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) goName(name string) string {
	if e.execution && e.funcs[name] {
		return e.prefix + "call_" + name
	}
	if name == "main" && e.funcs[name] && !e.nativeMain() {
		return e.prefix + "sourceMain"
	}
	return name
}

// goDirectives spells the compiler directives the converter attached to a
// Go-source declaration (Stmt.Comments, the //go:embed path) so gc sees them
// on the declaration they came from: //go:embed on a var, //go:noinline,
// //go:norace, //go:nosplit and the rest on a func. Each directive gets its
// own source position: a declaration's later //line directive cannot govern a
// diagnostic which gc reports on the preceding pragma comment itself.
func (e *emitter) goDirectives(s *syntax.Stmt) string {
	var out strings.Builder
	var previous syntax.Pos
	for i := range s.Comments {
		comment := &s.Comments[i]
		if strings.HasPrefix(comment.Text, "go:") {
			pos := comment.Pos()
			if !e.consecutiveSourceLines(previous, pos) {
				out.WriteString(e.goSourceLineDirective(pos))
			}
			out.WriteString("//" + comment.Text + "\n")
			previous = pos
		}
	}
	return out.String()
}

func (e *emitter) consecutiveSourceLines(previous, current syntax.Pos) bool {
	if e.sourceFile == nil || !previous.IsValid() || !current.IsValid() || current.Line() != previous.Line()+1 || current.Col() != previous.Col() {
		return false
	}
	before, beforeOK := e.sourceFile.SourceAt(previous)
	after, afterOK := e.sourceFile.SourceAt(current)
	if !beforeOK || !afterOK || before.Name != after.Name {
		return false
	}
	beforeName, beforeAdjusted := adjustedSourceName(e.sourceFile.Sources, previous.Offset())
	afterName, afterAdjusted := adjustedSourceName(e.sourceFile.Sources, current.Offset())
	return beforeAdjusted == afterAdjusted && beforeName == afterName
}

func (e *emitter) goSourceLineDirective(pos syntax.Pos) string {
	if e.sourceFile == nil || !pos.IsValid() {
		return ""
	}
	source, ok := e.sourceFile.SourceAt(pos)
	if !ok {
		return ""
	}
	name := source.Name
	adjustedName := false
	if adjusted, ok := adjustedSourceName(e.sourceFile.Sources, pos.Offset()); ok {
		name = adjusted
		adjustedName = true
	}
	if (name == "" && !adjustedName) || strings.ContainsAny(name, "\r\n") || pos.Line() == 0 {
		return ""
	}
	if pos.Col() > 0 {
		return fmt.Sprintf("//line %s:%d:%d\n", name, pos.Line(), pos.Col())
	}
	return fmt.Sprintf("//line %s:%d\n", name, pos.Line())
}

// inlineSourceLineDirective is goSourceLineDirective in its inline
// /*line file:L:C*/ form, which gc applies to the character right after the
// comment. It needs a column; without one (or with a filename that would end
// the comment) no directive is emitted.
func (e *emitter) inlineSourceLineDirective(pos syntax.Pos) string {
	line := strings.TrimSuffix(e.goSourceLineDirective(pos), "\n")
	if line == "" || pos.Col() == 0 || strings.Contains(line, "*/") {
		return ""
	}
	return "/*" + strings.TrimPrefix(line, "//") + "*/"
}

// nativeMain reports whether the source's own func main is the program entry:
// Go source that declares main and needs no generated boundary around it. The
// converter's synthetic entry calls are then folded into a Go init function
// (init calls) or dropped (the main call), since Go runs both itself.
func (e *emitter) nativeMain() bool {
	return e.goSource && e.funcs["main"] && !e.guarded && !e.execution
}

// syntheticEntryCall recognises the converter's top-level entry call. Go has no
// top-level statements, so under goSource any bare call at file level is one.
func (e *emitter) syntheticEntryCall(s *syntax.Stmt) (string, bool) {
	if !e.goSource {
		return "", false
	}
	c, ok := s.Cmd.(*syntax.BashPPCall)
	if !ok || len(c.Fun) != 1 || len(c.Args) > 0 || c.CalleeExpr != nil || c.FuncLit != nil {
		return "", false
	}
	return c.Fun[0].Value, true
}

func (e *emitter) literal(f *syntax.BashPPFuncLit) (string, error) {
	previousProgram := e.programExpr
	e.programExpr = ""
	defer func() { e.programExpr = previousProgram }()
	saved := e.inFunc
	e.inFunc = true
	defer func() { e.inFunc = saved }()
	// Labels are function-scoped: a loop inside the literal must not take a
	// label pending for the statement that contains the literal.
	savedLabel := e.pendingLabel
	e.pendingLabel = ""
	defer func() { e.pendingLabel = savedLabel }()
	e.push()
	defer e.pop()
	signature, err := e.signature(f.Params, f.Results, f.Body)
	if err != nil {
		return "", err
	}
	savedNames := e.resultNames
	e.resultNames = resultFieldNames(f.Results)
	defer func() { e.resultNames = savedNames }()
	savedResults := e.resultTypes
	e.resultTypes = e.returnTypes(f.Results)
	defer func() { e.resultTypes = savedResults }()
	parts, err := e.blockParts(f.Body)
	if err != nil {
		return "", err
	}
	body := strings.Join(parts, "")
	if e.execution {
		entry, err := e.programEntry("func", false, f.Results, false)
		if err != nil {
			return "", err
		}
		body = entry + body
		signature = e.privateSignature(signature)
	}
	if e.execution {
		parent := previousProgram
		if parent == "" {
			parent = e.prefix + "program"
		}
		captured := e.prefix + "closureBindings"
		p := e.prefix + "program"
		body = e.prefix + "closureProgram := *" + p + "\n" + p + " = &" + e.prefix + "closureProgram\n" + p + ".Bindings = " + captured + "\n" + p + " = " + p + ".LexicalScope(nil)\n" + body
		return "func() func" + signature + " { " + captured + " := " + parent + ".LexicalScope(nil).Bindings\nreturn func" + signature + " {\n" + body + "}\n}()", nil
	}
	return "func" + signature + e.bodyText(f.Body, parts), nil
}
func (e *emitter) signature(params, results []*syntax.BashPPField, body *syntax.Block) (string, error) {
	p, err := e.fields(params)
	if err != nil {
		return "", err
	}
	var r []string
	for _, field := range results {
		if callableResultField(field) {
			literal := returnedFuncLit(body)
			if literal == nil {
				return "", e.fail(field, CodeUnsupported, "func result needs an inferable returned literal")
			}
			// Signature inference must not bind the nested callable's parameters in
			// its factory. The result remains an ordinary typed Go function value.
			e.push()
			sig, err := e.signature(literal.Params, literal.Results, literal.Body)
			e.pop()
			if err != nil {
				return "", err
			}
			// Record the public spelling once: the wrapper's declared result and
			// its return adapter both read it back. What this signature declares
			// is the current ABI's spelling, which under a runtime is the private
			// closure the body's literal actually lowers to.
			e.recordCallableResult(field, "func"+sig)
			typ, ok := e.callableResultABI(field)
			if !ok {
				return "", e.fail(field, CodeUnsupported, "func result needs a recorded native signature")
			}
			names := names(field.Names)
			for _, name := range names {
				e.bind(name)
			}
			prefix := ""
			if len(names) > 0 {
				prefix = strings.Join(names, ",") + " "
			}
			r = append(r, prefix+typ)
		} else {
			x, err := e.fields([]*syntax.BashPPField{field})
			if err != nil {
				return "", err
			}
			r = append(r, x)
		}
	}
	result := ""
	if len(r) > 0 {
		result = " (" + strings.Join(r, ",") + ")"
	}
	return "(" + p + ")" + result, nil
}

// globalStatement separates storage from source-ordered initialization. The
// first static pass infers native types; the second emits only zero storage
// globally and performs each initialization at its original entry position.
func (e *emitter) globalStatement(s *syntax.Stmt) (string, error) {
	if err := e.statementFlags(s); err != nil {
		return "", err
	}
	switch n := s.Cmd.(type) {
	case *syntax.BashPPDecl:
		e.visibleGlobals[n.Name.Value] = true
	case *syntax.BashPPShortDecl:
		for _, name := range names(n.Lhs) {
			e.visibleGlobals[name] = true
		}
	}
	text, err := e.command(s.Cmd)
	if err != nil {
		return "", err
	}
	if n, ok := s.Cmd.(*syntax.BashPPShortDecl); ok && e.globalChecked[n] != "" {
		ns, failure := names(n.Lhs), e.globalChecked[n]
		for _, name := range ns {
			if name == "_" {
				continue
			}
			if e.declaredGlobals[name] {
				return "", e.fail(n, CodeType, "no new variables on left side of :=")
			}
			e.declaredGlobals[name] = true
		}
		if e.globalTypes == nil {
			first := strings.SplitN(text, "\n", 2)[0]
			e.globalDecls.WriteString(e.mark(n) + "var " + strings.Replace(first, " := ", " = ", 1) + "\n")
			return e.mark(n) + e.unused(ns) + "\n", nil
		}
		presence := ""
		for _, name := range ns {
			if name == "_" {
				continue
			}
			fmt.Fprintf(&e.globalDecls, "%svar %s %s\n", e.mark(n), name, e.globalTypes[name])
			if e.execution {
				presence += e.lexicalCell(name, e.program()) + ".Present = " + failure + " == nil\n"
			}
		}
		fmt.Fprintf(&e.globalDecls, "var %s error\n", failure)
		return e.mark(n) + strings.Replace(text, " := ", " = ", 1) + "\n" + presence, nil
	}
	line := text
	var ns []string
	constant := false
	switch n := s.Cmd.(type) {
	case *syntax.BashPPDecl:
		ns = []string{n.Name.Value}
		constant = n.Kw.Value == "const"
	case *syntax.BashPPShortDecl:
		ns = names(n.Lhs)
		line = "var " + strings.Replace(line, " := ", " = ", 1)
	default:
		return "", e.fail(s, CodeUnsupported, "global declaration")
	}
	line = strings.TrimSuffix(line, e.unused(ns))
	newNames := map[string]bool{}
	for _, name := range ns {
		if name != "_" && !e.declaredGlobals[name] {
			newNames[name] = true
		}
	}
	if _, short := s.Cmd.(*syntax.BashPPShortDecl); short && len(newNames) == 0 {
		return "", e.fail(s, CodeType, "no new variables on left side of :=")
	}
	for _, name := range ns {
		if name != "_" {
			e.declaredGlobals[name] = true
		}
	}
	if e.goSource {
		e.globalDecls.WriteString(e.mark(s.Cmd) + e.goDirectives(s) + line + "\n")
		return "", nil
	}
	if constant {
		e.globalDecls.WriteString(e.mark(s.Cmd) + line + "\n")
		if e.goSource {
			return "", nil
		}
		return e.mark(s.Cmd) + e.unused(ns) + "\n" + e.lexicalGlobalConstant(ns[0]), nil
	}
	if e.globalTypes == nil {
		if len(newNames) == len(ns) {
			e.globalDecls.WriteString(e.mark(s.Cmd) + line + "\n")
		} else {
			eq := strings.Index(line, " = ")
			if eq < 0 {
				return "", e.fail(s, CodeType, "redeclaration needs an initializer")
			}
			var temp []string
			for i := range ns {
				temp = append(temp, fmt.Sprintf("%stuple%d_%d", e.prefix, len(e.marks), i))
			}
			fmt.Fprintf(&e.globalDecls, "%svar %s = %s\n", e.mark(s.Cmd), strings.Join(temp, ","), line[eq+3:])
			for i, name := range ns {
				if newNames[name] {
					fmt.Fprintf(&e.globalDecls, "var %s = %s\n", name, temp[i])
				}
			}
		}
		return e.mark(s.Cmd) + e.unused(ns) + "\n", nil
	}
	for _, name := range ns {
		if !newNames[name] {
			continue
		}
		typ, ok := e.globalTypes[name]
		if !ok {
			return "", e.fail(s, CodeType, "missing inferred type for "+name)
		}
		fmt.Fprintf(&e.globalDecls, "%svar %s %s\n", e.mark(s.Cmd), name, typ)
	}

	eq := strings.Index(line, " = ")
	if eq < 0 {
		return e.mark(s.Cmd) + e.unused(ns) + "\n" + e.lexicalPresence(ns), nil
	}
	return e.mark(s.Cmd) + "/*" + e.prefix + "reset*/" + strings.Join(ns, ", ") + " = " + line[eq+3:] + "\n" + e.lexicalPresence(ns), nil
}

func (e *emitter) switchStmt(n *syntax.BashPPSwitch) (string, error) {
	if n.TypeSwitch {
		return e.typeSwitchStmt(n)
	}
	e.push()
	defer e.pop()
	init := ""
	var err error
	if n.Init != nil {
		init, err = e.command(n.Init)
		if err != nil {
			return "", err
		}
	}
	tag := ""
	if n.Tag != nil {
		tag, err = e.expr(n.Tag)
		if err != nil {
			return "", err
		}
	}
	var out strings.Builder
	target := e.pushBranchTarget(false)
	defer e.popBranchTarget()
	header, headerInit := e.goSourceHeaderInit(init)
	if headerInit {
		out.WriteString("switch " + header + "; " + tag + " {\n")
	} else {
		out.WriteString("switch " + tag + " {\n")
	}
	for _, arm := range n.Arms {
		e.push()
		out.WriteString(e.caseClauseDirective(arm))
		if len(arm.Exprs) == 0 {
			out.WriteString("default:\n")
		} else {
			var cases []string
			for _, expr := range arm.Exprs {
				x, err := e.withExprLine(arm.Case, func() (string, error) { return e.expr(expr) })
				if err != nil {
					e.pop()
					return "", err
				}
				cases = append(cases, x)
			}
			out.WriteString("case " + strings.Join(cases, ",") + ":\n")
		}
		parts, err := e.statementList(arm.Stmts)
		if err != nil {
			e.pop()
			return "", err
		}
		out.WriteString(strings.Join(parts, ""))
		e.pop()
	}
	if e.completeEnumSwitch(n) {
		out.WriteString("default: panic(\"invalid enum value\")\n")
	}
	out.WriteString("}")
	text := out.String()
	if target.label != "" {
		text = target.label + ":\n" + text
	}
	if init != "" && !headerInit {
		return "{\n" + init + "\n" + text + "\n}", nil
	}
	return text, nil
}

// caseClauseDirective positions a Go-source case clause: the clause's own
// comparisons are compiled where its case keyword was written, not on the
// line after the previous clause's last statement.
func (e *emitter) caseClauseDirective(arm *syntax.BashPPSwitchArm) string {
	if !e.goSource || arm == nil || !arm.Case.IsValid() {
		return ""
	}
	return e.goSourceLineDirective(arm.Case)
}

func (e *emitter) isRecover(c *syntax.BashPPCall) bool {
	return !e.goSource && c != nil && len(c.Fun) == 1 && c.Fun[0].Value == "recover" && !e.funcs["recover"]
}
func (e *emitter) recovered(name string) string {
	if e.execution {
		return "\n" + name + " = " + e.program() + ".Recovered(" + name + ")"
	}
	return "\nif " + name + " == nil { " + name + " = \"\"; /*" + e.prefix + "status1*/ } else { " + e.prefix + "popPanic(); /*" + e.prefix + "status0*/ }"
}
func (e *emitter) panicHelpers() string {
	return strings.ReplaceAll(`
var PREFIXpanicChain []string
func PREFIXpushPanic(v any) string {s:=PREFIXfmt.Sprint(v);PREFIXpanicChain=append(PREFIXpanicChain,s);return s}
func PREFIXpopPanic(){if len(PREFIXpanicChain)>0{PREFIXpanicChain=PREFIXpanicChain[:len(PREFIXpanicChain)-1]}}
`, "PREFIX", e.prefix)
}
func (e *emitter) panicBoundary() string {
	text := strings.ReplaceAll(`defer func(){if v:=recover();v!=nil{if len(PREFIXpanicChain)==0{PREFIXpanicChain=append(PREFIXpanicChain,PREFIXfmt.Sprint(v))};for i,message:=range PREFIXpanicChain{if i>0{PREFIXfmt.Fprint(PREFIXos.Stderr,"\t")};PREFIXfmt.Fprintln(PREFIXos.Stderr,"panic:",message)};PREFIXos.Exit(2)}}()
`, "PREFIX", e.prefix)
	if e.guarded {
		text = strings.Replace(text, "if v:=recover();v!=nil{", "if v:=recover();v!=nil{"+e.prefix+"rt.PreserveAbort(v);", 1)
	}
	return text
}

func (e *emitter) importDecl(n *syntax.BashPPImport) error {
	if n.Language != nil {
		// Direct foreign imports are planned before emission and loaded lazily
		// by the runtime path; they are not Go package imports.
		return nil
	}
	if e.moduleImporter == nil {
		e.moduleImporter = newModuleImporter(e.options.Dir)
	}
	specs := n.Specs
	if n.Path != nil {
		specs = []*syntax.BashPPImportSpec{{Alias: n.Alias, Path: n.Path}}
	}
	for _, spec := range specs {
		var path strings.Builder
		for _, part := range spec.Path.Parts {
			lit, ok := part.(*syntax.Lit)
			if !ok {
				return e.fail(spec, CodeType, "nonliteral import path")
			}
			path.WriteString(lit.Value)
		}
		p := path.String()
		if p == "C" {
			alias := "C"
			if spec.Alias != nil {
				alias = spec.Alias.Value
			}
			matched := false
			if e.sourceFile != nil {
				for _, pkg := range e.sourceFile.CgoPackages {
					if pkg.Alias == alias {
						matched = true
						break
					}
				}
			}
			if !matched {
				return e.fail(spec, CodeType, "import C has no package-scoped cgo metadata")
			}
			e.imports[alias] = p
			e.cgoAliases[alias] = true
			if e.sourceFile != nil {
				if source, ok := e.sourceFile.SourceAt(spec.Pos()); ok {
					e.fileImports[source.Name] = append(e.fileImports[source.Name], sourceImport{alias: alias, path: p, aliased: spec.Alias != nil})
				}
			}
			e.bind(alias)
			continue
		}
		pkg, err := e.moduleImporter.Import(p)
		if err != nil {
			return e.fail(spec, CodeType, err.Error())
		}
		alias := pkg.Name()
		if spec.Alias != nil {
			alias = spec.Alias.Value
		}
		if alias == "." {
			for _, name := range pkg.Scope().Names() {
				if pkg.Scope().Lookup(name).Exported() {
					e.dotNames[name] = true
				}
			}
		}
		// A dot or blank import binds no identifier, so several may name
		// distinct paths; key those by path so none replaces another. No
		// identifier lookup can match a key holding a space.
		key := alias
		if alias == "." || alias == "_" {
			key = alias + " " + p
		}
		e.imports[key] = p
		if spec.Alias != nil {
			e.importAliased[key] = true
		}
		if e.sourceFile != nil {
			if source, ok := e.sourceFile.SourceAt(spec.Pos()); ok {
				e.fileImports[source.Name] = append(e.fileImports[source.Name], sourceImport{alias: alias, path: p, aliased: spec.Alias != nil})
			}
		}
		e.bind(alias)
	}
	return nil
}

func (e *emitter) constGroup(n *syntax.BashPPConstGroup) (string, error) {
	shadowed := e.goSource && e.bound("iota")
	e.push()
	if !e.goSource {
		e.bind("iota")
	}
	defer e.pop()
	saved := e.iotaValue
	defer func() { e.iotaValue = saved }()
	var lines []string
	var lastRunExprs []syntax.BashPPExpr
	var lastRunWords [][]*syntax.Word
	var lastType string
	// Specs sharing an Iota value came from one multi-name ConstSpec
	// (`abit, amask = 1<<iota, 1<<iota-1`); they must be emitted on one
	// line, or gc's own iota would advance per name instead of per spec.
	for start := 0; start < len(n.Specs); {
		end := start + 1
		for end < len(n.Specs) && n.Specs[end].Iota == n.Specs[start].Iota {
			end++
		}
		run := n.Specs[start:end]
		explicit := !run[0].ImplicitInit && (run[0].InitExpr != nil || len(run[0].Init) > 0)
		names := make([]string, 0, len(run))
		values := make([]string, 0, len(run))
		runType := ""
		for k, spec := range run {
			current := int(spec.Iota)
			if shadowed {
				e.iotaValue = nil
			} else {
				e.iotaValue = &current
			}
			typ := ""
			var err error
			if e.goSource && inferredSpecType(spec) {
				// Go infers the constant's type from its written initializer.
			} else if spec.DeclTypeExpr != nil {
				typ, err = e.typeExpr(spec.DeclTypeExpr)
			} else if spec.DeclType != nil {
				typ, err = e.typeSpelling(spec.DeclType, spec.DeclType.Value)
			}
			if err != nil {
				return "", err
			}
			expr, words := spec.InitExpr, spec.Init
			if !explicit {
				if k < len(lastRunExprs) {
					expr = lastRunExprs[k]
				}
				if k < len(lastRunWords) {
					words = lastRunWords[k]
				}
				typ = lastType
			}
			value := ""
			if explicit || len(run) == 1 {
				if e.goSource && len(words) > 0 {
					// The Go-source converter retains the written initializer in Init
					// even when its typed expression is materialized or contextualized
					// for interpretation. Let gc evaluate that original expression in
					// compiled output. This preserves import and lexical-name uses, and
					// lets iota take the current spec's value naturally.
					value, err = e.wordSequence(words)
				} else if expr != nil {
					value, err = e.expr(expr)
				} else if len(words) > 0 {
					value, err = e.wordSequence(words)
				}
				if err != nil {
					return "", err
				}
			}
			// A multi-name spec with no written initializer repeats the
			// previous spec's expression list implicitly; emitting the bare
			// names keeps that repetition — and its per-line iota — in gc's
			// hands.
			names = append(names, spec.Name.Value)
			if value != "" {
				values = append(values, value)
			}
			if k == 0 && (explicit || len(run) == 1) {
				runType = typ
			}
			e.bind(spec.Name.Value)
			if expr != nil {
				e.projections.projectionBind(spec.Name.Value, e.projectionExpr(expr))
			} else if len(words) == 1 {
				e.projections.projectionBind(spec.Name.Value, e.projectionWord(words[0]))
			}
			projection, _ := e.projections.projectionLookup(spec.Name.Value)
			projection.constant = true
			e.projections.projectionBind(spec.Name.Value, projection)
			if spec.Name.Value == "iota" {
				shadowed = true
			}
		}
		if explicit {
			lastRunExprs = lastRunExprs[:0]
			lastRunWords = lastRunWords[:0]
			for _, spec := range run {
				lastRunExprs = append(lastRunExprs, spec.InitExpr)
				lastRunWords = append(lastRunWords, spec.Init)
			}
			lastType = runType
		}
		line := strings.Join(names, ", ")
		if runType != "" {
			line += " " + runType
		}
		if len(values) > 0 {
			line += " = " + strings.Join(values, ", ")
		}
		lines = append(lines, line)
		start = end
	}
	for _, spec := range n.Specs {
		if spec.Name.Value != "_" {
			e.scopes[len(e.scopes)-2][spec.Name.Value] = true
			if p, ok := e.projections.projectionLookup(spec.Name.Value); ok {
				e.projections.scopes[len(e.projections.scopes)-2][spec.Name.Value] = p
			}
		}
	}
	return "const (\n" + strings.Join(lines, "\n") + "\n)", nil
}

func (e *emitter) rangeStmt(n *syntax.BashPPRange) (string, error) {
	if n.Call != nil {
		return e.processRange(n)
	}
	if n.Chan != nil && e.foreignIteratorValues[n.Chan.Lit()] != "" {
		return e.foreignIteratorRange(n)
	}
	if n.Chan != nil && e.pythonRangeValue(n.Chan.Lit()) {
		return e.pythonHandleIteratorRange(n)
	}
	if e.execution && n.Chan != nil {
		return e.runtimeChannelRange(n, e.runtimeScope(), e.runtimeStatements)
	}
	e.push()
	defer e.pop()
	var rhs string
	var err error
	if n.Expr != nil {
		rhs, err = e.expr(n.Expr)
	} else {
		rhs, err = e.valueWord(n.Chan)
	}
	if err != nil {
		return "", err
	}
	ns := names(n.Names)
	op := " = "
	if n.Define.IsValid() {
		op = " := "
		for _, name := range ns {
			e.bind(name)
		}
	}
	prefix := ""
	if len(ns) > 0 {
		prefix = strings.Join(ns, ",") + op
	}
	target := e.pushBranchTarget(true)
	body, err := e.block(n.Body)
	e.popBranchTarget()
	out := "for " + prefix + "range " + rhs + " {\n" + body + "}"
	if target.label != "" {
		out = target.label + ":\n" + out
	}
	return out, err
}
func (e *emitter) importLines() string {
	aliases := make([]string, 0, len(e.imports))
	for alias := range e.imports {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var out strings.Builder
	for _, key := range aliases {
		if e.cgoAliases[key] {
			if e.sourceFile != nil && len(e.sourceFile.CgoPackages) == 1 {
				out.WriteString(e.sourceFile.CgoPackages[0].Preamble)
			}
			out.WriteString("import \"C\"\n")
			continue
		}
		if e.goSource && !e.importAliased[key] {
			// The input bound the package under its own name (C8).
			fmt.Fprintf(&out, "import %s\n", strconv.Quote(e.imports[key]))
			continue
		}
		alias, _, _ := strings.Cut(key, " ")
		fmt.Fprintf(&out, "import %s %s\n", alias, strconv.Quote(e.imports[key]))
	}
	return out.String()
}

// Bare func parameters are resolved from the concrete callable values supplied
// by this source unit. The output still uses native function types/calls. A
// genuinely dynamic or incompatible signature remains a positioned diagnostic.
func (e *emitter) discoverCallableParams(file *syntax.File) error {
	shapes := map[string]string{}
	declarations := map[string]*syntax.BashPPFuncDecl{}
	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.BashPPFuncDecl:
			if x.Receiver == nil {
				declarations[x.Name.Value] = x
				if shape, err := e.callbackShape(x.Params, x.Results); err == nil {
					shapes[x.Name.Value] = shape
				}
			}
		case *syntax.BashPPShortDecl:
			if x.FuncLit != nil && len(x.Lhs) == 1 {
				if shape, err := e.callbackShape(x.FuncLit.Params, x.FuncLit.Results); err == nil {
					shapes[x.Lhs[0].Value] = shape
				}
			}
		}
		return true
	})
	var problem error
	syntax.Walk(file, func(n syntax.Node) bool {
		if problem != nil {
			return false
		}
		call, ok := n.(*syntax.BashPPCall)
		if !ok || len(call.Fun) != 1 {
			return true
		}
		decl := declarations[call.Fun[0].Value]
		if decl == nil {
			return true
		}
		index := 0
		for _, field := range decl.Params {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for j := 0; j < count; j++ {
				if field.FieldType != nil && field.FieldType.Value == "func" && index < len(call.Args) {
					name := callableWordName(call.Args[index])
					shape := shapes[name]
					if shape != "" {
						if previous := e.callableParams[field]; previous != "" && previous != shape {
							problem = e.fail(field, CodeType, "func parameter receives incompatible native signatures")
							return false
						}
						e.callableParams[field] = shape
					}
				}
				index++
			}
		}
		return true
	})
	return problem
}
func callableWordName(w *syntax.Word) string {
	if w == nil || len(w.Parts) != 1 {
		return ""
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return p.Value
	case *syntax.ParamExp:
		if p.Param != nil {
			return p.Param.Value
		}
	}
	return ""
}
func (e *emitter) callbackShape(params, results []*syntax.BashPPField) (string, error) {
	parts := func(fields []*syntax.BashPPField) (string, error) {
		var out []string
		for _, f := range fields {
			typ := "any"
			var err error
			if f.FieldType != nil || f.FieldTypeExpr != nil {
				typ, err = e.fieldType(f)
			}
			if err != nil {
				return "", err
			}
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				out = append(out, typ)
			}
		}
		return strings.Join(out, ","), nil
	}
	p, err := parts(params)
	if err != nil {
		return "", err
	}
	r, err := parts(results)
	if err != nil {
		return "", err
	}
	if r != "" {
		r = " (" + r + ")"
	}
	return "func(" + p + ")" + r, nil
}

func (e *emitter) shellFor(n *syntax.ForClause) (string, error) {
	loop, ok := n.Loop.(*syntax.WordIter)
	if !ok || n.Select || len(loop.Items) != 1 {
		return "", e.fail(n, CodeUnsupported, "shell loop requires runtime lowering")
	}
	w := loop.Items[0]
	if len(w.Parts) != 1 {
		return "", e.fail(w, CodeBridge, "shell loop word expansion")
	}
	q, ok := w.Parts[0].(*syntax.DblQuoted)
	if !ok || len(q.Parts) != 1 {
		return "", e.fail(w, CodeBridge, "shell loop requires a quoted typed-slice spread")
	}
	p, ok := q.Parts[0].(*syntax.ParamExp)
	if !ok || p.Param == nil || p.Index == nil {
		return "", e.fail(w, CodeBridge, "shell loop requires a typed-slice spread")
	}
	index, ok := p.Index.(*syntax.Word)
	if !ok || index.Lit() != "@" || !e.known(p.Param.Value) {
		return "", e.fail(w, CodeBridge, "shell loop requires a typed-slice spread")
	}
	e.push()
	defer e.pop()
	e.bind(loop.Name.Value)
	var body strings.Builder
	for _, stmt := range n.Do {
		text, err := e.statement(stmt)
		if err != nil {
			return "", err
		}
		body.WriteString(text)
	}
	return "for _, " + loop.Name.Value + " := range " + p.Param.Value + " {\n" + body.String() + "}", nil
}

func (e *emitter) returnTypes(fields []*syntax.BashPPField) []string {
	var out []string
	for _, field := range fields {
		typ := ""
		if (field.FieldType != nil || field.FieldTypeExpr != nil) && (field.FieldType == nil || field.FieldType.Value != "func") {
			typ, _ = e.fieldType(field)
		}
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, typ)
		}
	}
	return out
}

func (e *emitter) typeSwitchStmt(n *syntax.BashPPSwitch) (string, error) {
	init, ok := n.Init.(*syntax.BashPPShortDecl)
	if !ok || len(init.Lhs) > 1 {
		return "", e.fail(n, CodeUnsupported, "type switch guard")
	}
	assert, ok := init.Expr.(*syntax.BashPPTypeAssertExpr)
	if !ok {
		return "", e.fail(n, CodeType, "missing type switch assertion")
	}
	root, err := e.expr(assert.X)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	target := e.pushBranchTarget(false)
	defer e.popBranchTarget()
	name := ""
	if len(init.Lhs) == 1 {
		name = init.Lhs[0].Value
		out.WriteString("switch " + name + " := ")
	} else {
		out.WriteString("switch ")
	}
	out.WriteString(root + ".(type) {\n")
	for _, arm := range n.Arms {
		e.push()
		if name != "" {
			e.bind(name)
			e.projections.projectionBind(name, interfaceProjection())
		}
		out.WriteString(e.caseClauseDirective(arm))
		if len(arm.Exprs) == 0 && len(arm.Types) == 0 {
			out.WriteString("default:\n")
		} else {
			var values []string
			for _, typ := range arm.Types {
				v, err := e.typeExpr(typ)
				if err != nil {
					e.pop()
					return "", err
				}
				values = append(values, v)
			}
			for _, x := range arm.Exprs {
				v, err := e.withExprLine(arm.Case, func() (string, error) { return e.expr(x) })
				if err != nil {
					e.pop()
					return "", err
				}
				values = append(values, v)
			}
			if name != "" {
				if len(values) == 1 && values[0] != "nil" {
					e.projections.projectionBind(name, e.projectionType(values[0], nil))
				} else {
					e.projections.projectionBind(name, interfaceProjection())
				}
			}
			out.WriteString("case " + strings.Join(values, ",") + ":\n")
		}
		if name != "" && !e.goSource {
			out.WriteString("_ = " + name + "\n")
		}
		parts, err := e.statementList(arm.Stmts)
		if err != nil {
			e.pop()
			return "", err
		}
		out.WriteString(strings.Join(parts, ""))
		e.pop()
	}
	out.WriteString("}")
	if target.label != "" {
		return target.label + ":\n" + out.String(), nil
	}
	return out.String(), nil
}
