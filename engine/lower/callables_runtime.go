package lower

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"sort"
	"strconv"
	"strings"
)

func (e *emitter) needsExecution(file *syntax.File) {
	if file.GoSource {
		return
	}
	if hasPositionalParameter(file) {
		e.execution = true
		e.bridge = true
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.DeclClause:
			if n.Variant != nil && n.Variant.Value == "readonly" {
				e.readonly = true
				e.execution = true
			}
		case *syntax.Subshell, *syntax.BashPPAgenticBlock, *syntax.BashPPGo, *syntax.BashPPMakeChan, *syntax.BashPPSend, *syntax.BashPPReceive, *syntax.BashPPClose, *syntax.BashPPSelect:
			e.execution = true
		case *syntax.BashPPFuncDecl:
			e.execution = e.execution || n.Agentic != nil
		}
		return true
	})
	// A decorated callable lowers as private entry plus public wrapper, the
	// same shape a marked one takes, and a decorator body reaches the
	// runtime's Call; both are execution-mode units.
	e.execution = e.execution || usesPredeclaredCall(file)
	if e.execution {
		e.bridge = true
	}
}
func (e *emitter) program() string {
	if e.programExpr != "" {
		return e.programExpr
	}
	return e.prefix + "program"
}
func (e *emitter) runtimeScope() RuntimeContext {
	p := e.program()
	return RuntimeContext{Context: p + ".Context", Session: p + ".Session", Channels: p + ".Channels"}
}
func (e *emitter) privateSignature(signature string) string {
	if !e.execution {
		return signature
	}
	parameters := e.prefix + "program *" + e.prefix + "rt.Program, " + e.prefix + "site " + e.prefix + "rt.Site"
	if signature[1] != ')' {
		parameters += ", "
	}
	return "(" + parameters + signature[1:]
}
func (e *emitter) callSite(n syntax.Node, name string) string {
	if e.predeclaredCall && n != nil && n.Pos().IsValid() {
		location := fmt.Sprintf("%s:%d", e.sourceName, n.Pos().Line())
		return fmt.Sprintf("%srt.Site{Name:%s, CallSite:%s}", e.prefix, strconv.Quote(name), strconv.Quote(location))
	}
	return fmt.Sprintf("%srt.Site{Name:%s}", e.prefix, strconv.Quote(name))
}
func (e *emitter) resultStorage(fields []*syntax.BashPPField) (declarations, values string, err error) {
	var namesList []string
	for _, f := range fields {
		typ := "any"
		switch {
		case callableResultField(f):
			// `func` is not a committed type; its shape came from the literal
			// the declaration returns. Storage holds what the private half
			// produces, which is the private closure the wrapper adapts.
			if e.methodResultField(f) {
				return "", "", e.fail(f, CodeUnsupported, "runtime returned callable needs a receiver-aware return adapter")
			}
			native, ok := e.callableResultABI(f)
			if !ok {
				return "", "", e.fail(f, CodeUnsupported, "runtime returned callable needs an inferable native signature")
			}
			typ = native
		case f.FieldType != nil || f.FieldTypeExpr != nil:
			typ, err = e.fieldType(f)
			if err != nil {
				return "", "", err
			}
		}
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for j := 0; j < count; j++ {
			name := fmt.Sprintf("%sresult%d", e.prefix, len(namesList))
			declarations += "var " + name + " " + typ + "\n"
			namesList = append(namesList, name)
		}
	}
	return declarations, strings.Join(namesList, ","), nil
}

// programEntry is the private entry's prolog. A decorated entry settles its
// named results itself, from the chain's outcome, so the deferred
// named-result observation is left out for it.
func (e *emitter) programEntry(name string, marked bool, results []*syntax.BashPPField, decorated bool) (string, error) {
	storage, values, err := e.resultStorage(results)
	if err != nil {
		return "", err
	}
	p := e.prefix + "program"
	site := e.prefix + "site"
	failure := e.prefix + "entryError"
	child := e.prefix + "enteredProgram"
	mark := e.prefix + "shortMark"
	settle := mark + " := " + p + ".ShortFailureMark()\ndefer " + p + ".SettleShortFailures(" + mark + ")\n"
	var named []string
	index := 0
	for _, field := range results {
		if len(field.Names) == 0 {
			index++
			continue
		}
		for _, name := range field.Names {
			named = append(named, e.prefix+"rt.MustResult("+e.prefix+"rt.SetNamedResult("+e.prefix+"results,"+strconv.Itoa(index)+","+name.Value+"))")
			index++
		}
	}
	if len(named) > 0 && !decorated {
		settle += "defer func(){if " + p + ".ShortFailureMark()==" + mark + " {" + strings.Join(named, ";") + "}}()\n"
	}
	return e.prefix + "results := " + p + ".Results\n_ = " + e.prefix + "results\n" + p + " = " + p + ".WithResults(nil)\n" + site + ".Name = " + strconv.Quote(name) + "\n" + child + ", " + failure + " := " + p + ".Enter(" + site + ", " + strconv.FormatBool(marked) + ")\nif " + failure + " != nil { " + p + ".Fail(" + failure + ")\n" + storage + "return " + values + "\n}\n" + p + " = " + child + "\n" + settle, nil
}
func (e *emitter) runtimeFunction(f *syntax.BashPPFuncDecl, signature, body, generics string) (string, error) {
	if f.Receiver != nil {
		return e.runtimeMethodFunction(f, signature, body, generics)
	}
	publicResults := e.publicResults(f)
	entry, err := e.programEntry(f.Name.Value, f.Agentic != nil, publicResults, len(f.Decorators) > 0)
	if err != nil {
		return "", err
	}
	entry += e.bindResultArguments(f.Params)
	decorators := ""
	if len(f.Decorators) > 0 {
		bodySignature := signature
		if e.goErrorFuncs[f] {
			bodySignature, err = e.signature(f.Params, f.Results, f.Body)
			if err != nil {
				return "", err
			}
		}
		if body, decorators, err = e.decoratedBody(f, bodySignature, body); err != nil {
			return "", err
		}
	}
	private := e.goName(f.Name.Value)
	var args []string
	for _, field := range f.Params {
		if field.FieldType != nil && strings.HasPrefix(field.FieldType.Value, "func") {
			return "", e.fail(field, CodeUnsupported, "runtime public callable parameters need native adapters")
		}
		for _, name := range field.Names {
			args = append(args, name.Value)
		}
		if field.Ellipsis.IsValid() && len(args) > 0 {
			args[len(args)-1] += "..."
		}
	}
	var typeArgs []string
	for _, p := range f.TypeParams {
		typeArgs = append(typeArgs, names(p.Names)...)
	}
	instance := ""
	if len(typeArgs) > 0 {
		instance = "[" + strings.Join(typeArgs, ",") + "]"
	}
	p := e.prefix + "program"
	invocation := private + instance + "(" + p + ", " + e.prefix + "rt.Site{Name:" + strconv.Quote(f.Name.Value) + "}"
	if len(args) > 0 {
		invocation += ", " + strings.Join(args, ",")
	}
	invocation += ")"
	resultAllocation, resultValidation := "", ""
	plan := e.functionResultPlan(&syntax.BashPPFuncDecl{Results: publicResults})
	if plan.NeedsFrame() {
		frame := e.prefix + "publicResults"
		resultAllocation, err = e.richResultFrame(frame, e.runtimeScope(), plan)
		if err != nil {
			return "", err
		}
		resultAllocation += "\n"
		invocation = strings.Replace(invocation, "("+p+",", "("+p+".WithResults("+frame+"),", 1)
		for i, result := range plan.Results {
			resultValidation += "if _, err := " + e.prefix + "rt.NativeResult[" + result.Declared + "](" + frame + "," + strconv.Itoa(i) + "," + e.checkedValueSite(f, "") + "); err != nil {panic(err)}\n"
		}
	}
	storage, values, err := e.resultStorage(publicResults)
	if err != nil {
		return "", err
	}
	if values != "" {
		invocation = values + " = " + invocation
	}
	public := f.Name.Value
	if public == "main" {
		public = e.prefix + "sourceMain"
	}
	// A declaration that returns a callable has two spellings for that result,
	// so the wrapper declares its own public signature and adapts the private
	// closure on the way out. The Program it binds is the one this invocation
	// ran under, captured inside Run: an escaped callable must keep reaching
	// its creating owner, whose channel authority Run has already revoked, and
	// never a fresh program that would hand it channels the owner gave up.
	publicSignature, captured, adapters, returned := signature, "", "", values
	if callableResults(f.Results) {
		publicSignature, err = e.publicSignature(f)
		if err != nil {
			return "", err
		}
		adapters, returned, err = e.publicReturnValues(f, values)
		if err != nil {
			return "", err
		}
		name := e.capturedProgramName()
		captured = "var " + name + " *" + e.prefix + "rt.Program\n"
		invocation = name + " = " + p + "\n" + invocation
	}
	wrapper := "func " + public + generics + publicSignature + " {\n" + p + ",err := " + e.prefix + "rt.NewProgram()\nif err != nil {panic(err)}\n" + storage + captured + resultAllocation + "err = " + p + ".Run(func(" + p + " *" + e.prefix + "rt.Program){" + invocation + "})\nif err != nil {panic(err)}\n" + resultValidation + adapters + "return " + returned + "\n}\n"
	return e.mark(f) + "func " + private + generics + e.privateSignature(signature) + " {\n" + entry + body + "}\n" + wrapper + decorators, nil
}
func (e *emitter) programMain(body string) string {
	if e.options.Entry != "" {
		return e.programEntrySourceNamed(body, e.mixedShell, e.options.Entry)
	}
	return e.programEntrySource(body, e.mixedShell)
}

func (e *emitter) agenticBlock(n *syntax.BashPPAgenticBlock) (string, error) {
	parent := e.program()
	child := fmt.Sprintf("%sblockProgram%d", e.prefix, len(e.marks))
	previous := e.programExpr
	e.programExpr = child
	defer func() { e.programExpr = previous }()
	text, err := e.runtimeStatements(n.Body.Stmts, e.runtimeScope())
	if err != nil {
		return "", err
	}
	// Agentic changes assistance, not lexical bindings: no Go braces or new scope.
	return child + " := " + parent + ".Block()\n_ = " + child + "\n" + text, nil
}
func (e *emitter) runtimeStatements(stmts []*syntax.Stmt, _ RuntimeContext) (string, error) {
	var out strings.Builder
	for _, stmt := range stmts {
		text, err := e.statement(stmt)
		if err != nil {
			return "", err
		}
		out.WriteString(text)
	}
	return out.String(), nil
}
func (e *emitter) programGo(n *syntax.BashPPGo) (string, error) {
	c := n.Call
	if c == nil {
		return "", e.fail(n, CodeExpr, "missing task callable")
	}
	var callee string
	var err error
	if c.FuncLit != nil {
		callee, err = e.literal(c.FuncLit)
	} else if len(c.Fun) == 1 {
		callee = e.goName(c.Fun[0].Value)
	} else {
		return "", e.fail(c, CodeUnsupported, "task method adapter")
	}
	if err != nil {
		return "", err
	}
	if len(c.ArgNames) > 0 {
		return "", e.fail(c, CodeUnsupported, "task named-argument capture adapter")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "{\n%staskCallee := %s\n", e.prefix, callee)
	var args []string
	for i := range c.Args {
		value, err := e.callArgument(c, i)
		if err != nil {
			return "", err
		}
		name := fmt.Sprintf("%staskArg%d", e.prefix, i)
		typ := ""
		if len(c.Fun) == 1 {
			if f := e.functionDecls[c.Fun[0].Value]; f != nil {
				params, problem := sharpParameters(f)
				if problem != nil {
					return "", problem
				}
				if i < len(params) {
					field := params[i].field
					if field.FieldType != nil || field.FieldTypeExpr != nil {
						typ, err = e.fieldType(field)
						if err != nil {
							return "", err
						}
					}
				}
			}
		}
		if typ == "" {
			fmt.Fprintf(&out, "%s := %s\n", name, value)
		} else {
			fmt.Fprintf(&out, "var %s %s = %s\n", name, typ, value)
		}
		args = append(args, name)
	}
	if c.Ellipsis.IsValid() && len(args) > 0 {
		args[len(args)-1] += "..."
	}
	parent := e.prefix + "taskParent"
	p := e.prefix + "program"
	rt := e.prefix + "rt."
	fmt.Fprintf(&out, "%s := %s\n%s.Session.Go(%sChannelTask(func(ctx %sTaskContext, session *%sSession)error{\n%s := %s.Child(ctx,session)\n", parent, e.program(), parent, rt, rt, rt, p, parent)
	invocation := e.prefix + "taskCallee(" + p + "," + e.callSite(c, "func")
	if len(args) > 0 {
		invocation += "," + strings.Join(args, ",")
	}
	invocation += ")"
	out.WriteString("return " + p + ".RunSourceTask(func(){" + invocation + "})\n}))\n}\n")
	return out.String(), nil
}

// Infer the native channel signature of legacy untyped parameters from the
// parser's committed make-channel nodes and resolved in-unit calls.
func (e *emitter) inferChannelParameters(file *syntax.File) error {
	for round := 0; round < 3; round++ {
		for _, owner := range append([]*syntax.BashPPFuncDecl{nil}, e.functionList()...) {
			local := map[string]string{}
			var root syntax.Node = file
			if owner != nil {
				if owner.Body == nil {
					// A body-less declaration has no calls to infer from;
					// the emitter diagnoses it when it reaches it.
					continue
				}
				root = owner.Body
				for _, p := range owner.Params {
					if typ := e.inferredParams[p]; typ != "" {
						for _, n := range p.Names {
							local[n.Value] = typ
						}
					}
				}
			}
			syntax.Walk(root, func(node syntax.Node) bool {
				if _, ok := node.(*syntax.BashPPFuncDecl); ok {
					return false
				}
				if decl, ok := node.(*syntax.BashPPShortDecl); ok && decl.MakeChan != nil && len(decl.Lhs) == 1 {
					local[decl.Lhs[0].Value] = "chan " + decl.MakeChan.ChanType.Elem.Value
				}
				return true
			})
			var problem error
			syntax.Walk(root, func(node syntax.Node) bool {
				if _, ok := node.(*syntax.BashPPFuncDecl); ok {
					return false
				}
				call, ok := node.(*syntax.BashPPCall)
				if !ok || len(call.Fun) != 1 {
					return true
				}
				target := e.functionDecls[call.Fun[0].Value]
				if target == nil {
					return true
				}
				index := 0
				for _, field := range target.Params {
					for range field.Names {
						if index < len(call.Args) && field.FieldType == nil && field.FieldTypeExpr == nil {
							if typ := local[call.Args[index].Lit()]; typ != "" {
								if prior := e.inferredParams[field]; prior != "" && prior != typ {
									problem = e.fail(call, CodeType, "channel parameter has incompatible call-site types")
								} else {
									e.inferredParams[field] = typ
								}
							}
						}
						index++
					}
				}
				return true
			})
			if problem != nil {
				return problem
			}
		}
	}
	return nil
}
func (e *emitter) functionList() []*syntax.BashPPFuncDecl {
	var result []*syntax.BashPPFuncDecl
	// Source positions make inference deterministic despite map storage.
	for _, f := range e.functionDecls {
		result = append(result, f)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Pos().Offset() < result[j].Pos().Offset() })
	return result
}
