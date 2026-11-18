package lower

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Decorated typed callables (dhnt/docs/bashpp-decorators-and-advice.md §5).
//
// A decorator stack lowers to a chain emitted INSIDE the callable's private
// entry, after the agentic gate and the argument plumbing: the original body
// becomes a closure with the callable's own signature, the entry packs its
// parameters into one shellrt.Call, and Program.Decorate runs the rungs
// outermost first with Next bound to the next rung and finally to the closure.
// Because the chain lives in the private entry, the public symbol keeps its
// declared signature and every indirect route — method value, interface
// capability dispatch, function handle — is decorated by construction; there
// is no second representation that could reach an undecorated body.
//
// There is one ordinary Go wrapper per callable: the closure, the per-position
// type assertions that rebind the body's parameters from Call.Args on every
// Next, and the per-result assertions that settle Call.Results back into the
// declared results. No generics over the target's signature and no reflection
// in generated code.

// decoratorCallType is the predeclared decorator context type. A unit that
// names it without declaring its own gets `type Call = shellrt.Call`; a unit
// declaring `type Call` shadows it, exactly as in the interpreter, and its
// functions taking that type are simply not decorators.
const decoratorCallType = "Call"
const goErrorDecoratorName = "go.error"

// decoratorCallProjection is the predeclared Call as the emitter's projection
// tables see it, so `c.Name`, `c.Status`, `c.Results[0]` and the rest lower
// through the ordinary struct field paths.
const decoratorCallProjection = `type Call struct {
	Name string
	Site string
	Caller string
	Args []any
	Results []any
	Status int
	Agentic bool
	Advised string
}
`

// decoratorSignature reports whether f is a decorator: a free function whose
// first parameter is one name of the predeclared *Call type.
func (e *emitter) decoratorSignature(f *syntax.BashPPFuncDecl) bool {
	if f == nil || f.Receiver != nil || len(f.Params) == 0 || !e.predeclaredCall {
		return false
	}
	first := f.Params[0]
	if len(first.Names) != 1 || first.Ellipsis.IsValid() {
		return false
	}
	return predeclaredCallPointer(first)
}

// predeclaredCallPointer reports whether a field's type is spelled *Call.
func predeclaredCallPointer(field *syntax.BashPPField) bool {
	ptr, ok := field.FieldTypeExpr.(*syntax.BashPPPointerType)
	if !ok {
		return field.FieldType != nil && field.FieldType.Value == "*"+decoratorCallType
	}
	named, ok := ptr.Element.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && named.Name.Value == decoratorCallType && len(named.TypeArgs) == 0
}

// usesPredeclaredCall reports whether a Bash++ unit reaches for the
// predeclared decorator context: a decorator stack anywhere, or a function
// whose first parameter is spelled *Call.
func usesPredeclaredCall(file *syntax.File) bool {
	if file.GoSource {
		return false
	}
	used := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BashPPDecorator:
			used = true
		case *syntax.FuncDecl:
			used = used || len(n.Decorators) > 0
		case *syntax.BashPPFuncDecl:
			used = used || len(n.Decorators) > 0 || (n.Receiver == nil && len(n.Params) > 0 && len(n.Params[0].Names) == 1 && predeclaredCallPointer(n.Params[0]))
		}
		return !used
	})
	return used
}

// prepareDecorators settles the predeclared Call for the unit and runs the
// static decorator diagnostics: reserved namespaced names, self-decoration,
// non-decorator rungs, decorator cycles among unit functions, and decorated
// shell functions, which this MVP does not lower.
func (e *emitter) prepareDecorators(file *syntax.File) error {
	if !usesPredeclaredCall(file) {
		return nil
	}
	// A unit declaring its own Call shadows the predeclared one: no alias, no
	// projection, and a function taking that type is not a decorator, which
	// the checks below then report for any stack naming it.
	if !e.typeNames[decoratorCallType] {
		e.predeclaredCall = true
		projection, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(decoratorCallProjection), "")
		if err != nil {
			return e.fail(file, CodeType, "predeclared Call is unavailable: "+err.Error())
		}
		if decl, ok := projection.Stmts[0].Cmd.(*syntax.BashPPDecl); ok {
			e.declaredTypes[decoratorCallType] = decl
		}
	}
	e.shellFuncs = map[string]bool{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if n, ok := node.(*syntax.FuncDecl); ok && n.Name != nil {
			e.shellFuncs[n.Name.Value] = true
		}
		return true
	})
	var problem error
	syntax.Walk(file, func(node syntax.Node) bool {
		if problem != nil {
			return false
		}
		switch n := node.(type) {
		case *syntax.FuncDecl:
			if len(n.Decorators) > 0 {
				problem = e.fail(n.Decorators[0], CodeUnsupported, "decorated shell functions are not lowered")
			}
		case *syntax.BashPPFuncDecl:
			problem = e.checkDecorators(n)
		}
		return problem == nil
	})
	if problem != nil {
		return problem
	}
	return e.checkDecoratorCycles()
}

// checkDecorators validates one declaration's stack statically, as the
// engine validates it at registration and resolves it at call time.
func (e *emitter) checkDecorators(f *syntax.BashPPFuncDecl) error {
	name := f.Name.Value
	goError := false
	for _, d := range f.Decorators {
		if d.Name.Value == goErrorDecoratorName {
			if goError || len(d.Args) != 0 || len(d.ArgNames) != 0 || f.Receiver != nil || len(f.TypeParams) != 0 || trailingErrorResult(f.Results) {
				return e.fail(d, "BASHPP-EDECO-GOERROR", "@go.error requires a receiver-less non-generic function with no arguments and no trailing error result")
			}
			goError = true
			e.goErrorFuncs[f] = true
			continue
		}
		switch {
		case strings.Contains(d.Name.Value, "."):
			return e.fail(d, "BASHPP-EDECO-RESERVED", "@"+d.Name.Value+": namespaced decorators are reserved")
		case d.Name.Value == name && f.Receiver == nil:
			return e.fail(d, "BASHPP-EDECO-SELF", name+" cannot decorate itself")
		}
		if dec := e.functionDecls[d.Name.Value]; dec != nil {
			if !e.decoratorSignature(dec) {
				return e.fail(d, "BASHPP-EDECO-SIG", d.Name.Value+" is not a decorator: its first parameter must be *Call")
			}
			if len(dec.TypeParams) > 0 {
				return e.fail(d, CodeUnsupported, "generic decorators are not lowered")
			}
			continue
		}
		if e.shellFuncs[d.Name.Value] {
			return e.fail(d, "BASHPP-EDECO-SIG", d.Name.Value+" is not a decorator: a shell function has no *Call parameter")
		}
	}
	return nil
}

func trailingErrorResult(results []*syntax.BashPPField) bool {
	if len(results) == 0 {
		return false
	}
	last := results[len(results)-1]
	if last.FieldType != nil && last.FieldType.Value == "error" {
		return true
	}
	named, ok := last.FieldTypeExpr.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && named.Name.Value == "error" && len(named.TypeArgs) == 0
}

func goErrorResultField() *syntax.BashPPField {
	return &syntax.BashPPField{FieldType: &syntax.Lit{Value: "error"}}
}

func (e *emitter) publicResults(f *syntax.BashPPFuncDecl) []*syntax.BashPPField {
	if f == nil || !e.goErrorFuncs[f] {
		return f.Results
	}
	out := append([]*syntax.BashPPField(nil), f.Results...)
	return append(out, goErrorResultField())
}

// checkDecoratorCycles refuses a decorator graph with a cycle among unit
// functions: a decorator decorated, directly or through others, by a function
// its own chain runs would re-enter itself on every call. The engine detects
// the same cycle at call time; lowering resolves every callee statically, so
// it reports it before anything runs.
func (e *emitter) checkDecoratorCycles() error {
	const visiting, done = 1, 2
	state := map[string]int{}
	var visit func(name string, via []*syntax.BashPPDecorator) error
	visit = func(name string, via []*syntax.BashPPDecorator) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return e.fail(via[len(via)-1], "BASHPP-EDECO-CYCLE", "decorator "+name+" is already decorating an active call")
		}
		state[name] = visiting
		if f := e.functionDecls[name]; f != nil {
			for _, d := range f.Decorators {
				if d.Name.Value == goErrorDecoratorName {
					continue
				}
				if e.functionDecls[d.Name.Value] == nil {
					continue
				}
				if err := visit(d.Name.Value, append(via, d)); err != nil {
					return err
				}
			}
		}
		state[name] = done
		return nil
	}
	for _, f := range e.functionList() {
		if err := visit(f.Name.Value, nil); err != nil {
			return err
		}
	}
	return nil
}

// decoratorCallAlias is the unit-level spelling of the predeclared Call.
func (e *emitter) decoratorCallAlias() string {
	if !e.predeclaredCall {
		return ""
	}
	return "type " + decoratorCallType + " = " + e.prefix + "rt." + decoratorCallType + "\n"
}

// decoratedParameter is one bound position of a decorated callable.
type decoratedParameter struct {
	name, typ string
	variadic  bool
}

func (e *emitter) decoratedParameters(f *syntax.BashPPFuncDecl) ([]decoratedParameter, error) {
	var params []decoratedParameter
	for _, field := range f.Params {
		typ := "any"
		if inferred := e.inferredParams[field]; inferred != "" {
			typ = inferred
		}
		if field.FieldType != nil || field.FieldTypeExpr != nil {
			var err error
			if typ, err = e.fieldType(field); err != nil {
				return nil, err
			}
		}
		for _, name := range field.Names {
			params = append(params, decoratedParameter{name: name.Value, typ: typ, variadic: field.Ellipsis.IsValid()})
		}
	}
	return params, nil
}

// decoratorsHelper names the package-level function that builds a
// callable's rungs. It takes only the program, so a rung's argument words
// resolve to the unit's globals — the declaration scope — and never to the
// target's parameters, which Go would otherwise resolve lexically inside the
// private entry.
func (e *emitter) decoratorsHelper(f *syntax.BashPPFuncDecl) string {
	name := e.prefix + "decorators_"
	if f.Receiver != nil && f.Receiver.RecvType != nil {
		name += f.Receiver.RecvType.Value + "_"
	}
	return name + f.Name.Value
}

// decoratedBody wraps a private entry's body in its decorator chain. body is
// the entry's body text as emitted for the undecorated shape; signature is
// the callable's declared Go signature. The first result replaces the body
// inside the private entry, after the agentic gate; the second is the
// package-level rung builder the entry calls.
func (e *emitter) decoratedBody(f *syntax.BashPPFuncDecl, signature, body string) (string, string, error) {
	rt := e.prefix + "rt."
	p := e.prefix + "program"
	call := e.prefix + "call"
	name := f.Name.Value
	if f.Receiver != nil && f.Receiver.RecvType != nil {
		name = f.Receiver.RecvType.Value + "." + name
	}
	quoted := strconv.Quote(name)
	params, err := e.decoratedParameters(f)
	if err != nil {
		return "", "", err
	}
	plan := e.functionResultPlan(f)
	if plan.NeedsFrame() {
		return "", "", e.fail(f, CodeUnsupported, "decorated callable results need direct native carriers")
	}
	goError := e.goErrorFuncs[f]
	resultTypes := e.returnTypes(f.Results)
	// The failure block runs when the decorator infrastructure itself fails —
	// Decorate, DecoratedResults or DecoratedResult reporting false. It returns
	// the declared zero values for the ENTIRE public signature, which for a
	// @go.error callable includes a nil (zero) trailing error. The failure is
	// reported by the chain through Program.Fail and settled through the
	// result frame's absence (the caller reads MissingResults), exactly as the
	// interpreter returns nil,false without minting any result. Synthesizing
	// GoError(name, Status()) here would instead mint an ordinary
	// (zero, "exit status N") pair — masking an infrastructure failure as a
	// normal call outcome — so the trailing error stays its zero value like
	// every other result.
	storage, zeros, err := e.resultStorage(e.publicResults(f))
	if err != nil {
		return "", "", err
	}
	failure := "{\n" + storage + "return " + zeros + "\n}\n"

	var out strings.Builder
	// The context: one entry per bound position, a variadic tail one per
	// element, so a decorator sees and rewrites arguments by position.
	packed := make([]string, 0, len(params))
	fixed := 0
	variadic := false
	for _, param := range params {
		if param.variadic {
			variadic = true
			continue
		}
		fixed++
		packed = append(packed, param.name)
	}
	fmt.Fprintf(&out, "%s := &%sCall{Name: %s, Site: %ssite.CallSite, Caller: %s.Caller(), Agentic: %t, Args: []any{%s}}\n", call, rt, quoted, e.prefix, p, f.Agentic != nil, strings.Join(packed, ", "))
	if variadic {
		tail := params[len(params)-1]
		fmt.Fprintf(&out, "for _, %sv := range %s { %s.Args = append(%s.Args, %sv) }\n", e.prefix, tail.name, call, call, e.prefix)
	}

	// The body, as a closure with the callable's own signature. Its
	// parameters shadow the entry's, so every Next binds fresh values from
	// the context and the body's lexical registrations name the closure's
	// storage; a repeated Next is an independent invocation with its own
	// scope and its own defer stack. The caller's result frame is shadowed
	// by nil: the body's returns must not reach it, because the chain's
	// settled Results are what the caller reads, and a failed chain leaves
	// the frame absent exactly as the engine's failed call yields no values.
	frame := e.prefix + "results"
	closure := "func(" + p + " *" + rt + "Program, " + frame + " *" + rt + "ResultFrame"
	if signature[1] != ')' {
		closure += ", "
	}
	closure += signature[1:]
	fmt.Fprintf(&out, "%sbody := %s {\n%s}\n", e.prefix, closure, body)

	// The rungs, outermost first. Their argument words evaluate in the
	// DECLARATION's scope on every run — a typed target's parameters are not
	// visible to them, as the engine's captured declaration scope has it —
	// so they lower with the callable's own bindings set aside and live in a
	// package-level builder where no parameter is in Go scope either.
	helper := e.decoratorsHelper(f)
	fmt.Fprintf(&out, "%srungs := %s(%s)\n", e.prefix, helper, p)
	var builder strings.Builder
	// The builder sees the unit's globals, as the function body does, and
	// nothing of the caller's locals.
	fmt.Fprintf(&builder, "func %s(%s *%sProgram) []%sDecorator {\n%s = %s.LexicalScope(%s)\nreturn []%sDecorator{\n", helper, p, rt, rt, p, p, e.lexicalNames(e.functionGlobals), rt)
	savedScopes, savedProjections := e.scopes, e.projections
	e.scopes = []map[string]bool{{}}
	e.projections = projector{}
	e.projections.projectionPush()
	if len(savedProjections.scopes) > 0 {
		e.projections.scopes[0] = savedProjections.scopes[0]
	}
	e.projections.projectionPush()
	var rungErr error
	for _, d := range f.Decorators {
		if d.Name.Value == goErrorDecoratorName {
			continue
		}
		rung, err := e.decoratorRung(f, d)
		if err != nil {
			rungErr = err
			break
		}
		builder.WriteString(rung + ",\n")
	}
	e.scopes, e.projections = savedScopes, savedProjections
	if rungErr != nil {
		return "", "", rungErr
	}
	builder.WriteString("}\n}\n")

	// Run the chain. The body binder revalidates what the original call
	// proved — the arity and every position's type — because a decorator's
	// Args rewrite may have broken either.
	fmt.Fprintf(&out, "if !%s.Decorate(%s, %srungs, func(%s *%sProgram) error {\n", p, call, e.prefix, p, rt)
	// Every local here is prefix-spelled: the lexical storage pass treats an
	// unprefixed local as script storage to register.
	errName := e.prefix + "err"
	fmt.Fprintf(&out, "if %s := %sDecoratedArity(%s, %s, %d, %t); %s != nil { return %s }\n", errName, rt, call, quoted, fixed, variadic, errName, errName)
	var bound []string
	for i, param := range params {
		arg := fmt.Sprintf("%sarg%d", e.prefix, i)
		if param.variadic {
			fmt.Fprintf(&out, "%s, %s := %sDecoratedVariadic[%s](%s, %s, %d, %s)\nif %s != nil { return %s }\n", arg, errName, rt, param.typ, call, quoted, i, strconv.Quote(param.name), errName, errName)
			bound = append(bound, arg+"...")
			continue
		}
		fmt.Fprintf(&out, "%s, %s := %sDecoratedArg[%s](%s, %s, %d, %s)\nif %s != nil { return %s }\n", arg, errName, rt, param.typ, call, quoted, i, strconv.Quote(param.name), errName, errName)
		bound = append(bound, arg)
	}
	invocation := e.prefix + "body(" + strings.Join(append([]string{p, "nil"}, bound...), ", ") + ")"
	if len(resultTypes) == 0 {
		fmt.Fprintf(&out, "%s\n%s.Results = nil\nreturn nil\n}) %s", invocation, call, failure)
	} else {
		var produced []string
		for i := range resultTypes {
			produced = append(produced, fmt.Sprintf("%sproduced%d", e.prefix, i))
		}
		fmt.Fprintf(&out, "%s := %s\n%s.Results = []any{%s}\nreturn nil\n}) %s", strings.Join(produced, ", "), invocation, call, strings.Join(produced, ", "), failure)
	}

	// Settle: the context's Results become the declared results, its Status
	// the call's, and the caller's result frame — if it handed one in — is
	// filled so a rewritten or skipped result is what the caller reads.
	fmt.Fprintf(&out, "if !%s.DecoratedResults(%s, %s, %d) %s", p, call, quoted, len(resultTypes), failure)
	if len(resultTypes) == 0 {
		if goError {
			fmt.Fprintf(&out, "if %s != nil { %sMustResult(%sSetResult(%s, 0, %sGoError(%s, %s.Status()))) }\n", frame, rt, rt, frame, rt, quoted, p)
			fmt.Fprintf(&out, "return %sGoError(%s, %s.Status())\n", rt, quoted, p)
		} else {
			out.WriteString("return\n")
		}
		return out.String(), builder.String(), nil
	}
	var finals []string
	for i, typ := range resultTypes {
		final := fmt.Sprintf("%sfinal%d", e.prefix, i)
		finals = append(finals, final)
		fmt.Fprintf(&out, "%s, %sok%d := %sDecoratedResult[%s](%s, %s, %s, %d)\nif !%sok%d %s", final, e.prefix, i, rt, typ, p, call, quoted, i, e.prefix, i, failure)
	}
	fmt.Fprintf(&out, "if %s != nil {\n", frame)
	for i, final := range finals {
		record, err := e.richResultRecord(frame, plan, i, final)
		if err != nil {
			return "", "", err
		}
		out.WriteString(record + "\n")
	}
	if goError {
		fmt.Fprintf(&out, "%sMustResult(%sSetResult(%s, %d, %sGoError(%s, %s.Status())))\n", rt, rt, frame, len(finals), rt, quoted, p)
	}
	out.WriteString("}\n")
	// Named results are the entry's own; the body closure's defers already
	// ran against the closure's, so the settled values are final.
	for i, name := range resultFieldNames(f.Results) {
		if name != "" {
			fmt.Fprintf(&out, "%s = %s\n", name, finals[i])
		}
	}
	if goError {
		finals = append(finals, rt+"GoError("+quoted+", "+p+".Status())")
	}
	out.WriteString("return " + strings.Join(finals, ", ") + "\n")
	return out.String(), builder.String(), nil
}

// decoratorRung emits one rung. A unit function resolves statically to its
// private entry, with the rung's arguments planned by the ordinary Bash#
// binding — positional, keyword and default — and evaluated on every run of
// the rung, in the declaration's scope. Any other name is a native rung
// resolved through shellrt.Decorators when it runs.
func (e *emitter) decoratorRung(target *syntax.BashPPFuncDecl, d *syntax.BashPPDecorator) (string, error) {
	rt := e.prefix + "rt."
	p := e.prefix + "program"
	c := e.prefix + "c"
	lexical := p + " = " + p + ".LexicalScope(" + e.lexicalNames(e.functionGlobals) + "); "
	line := &syntax.BashPPCall{Fun: []*syntax.Lit{d.Name}, Args: d.Args, ArgNames: d.ArgNames, Lparen: d.Lparen, Rparen: d.Rparen}
	dec := e.functionDecls[d.Name.Value]
	if dec == nil {
		var args []string
		positional := len(d.Args) - len(d.ArgNames)
		for i, w := range d.Args {
			value, err := e.plannedCallArgument(line, w)
			if err != nil {
				return "", err
			}
			argName := ""
			if i >= positional {
				argName = d.ArgNames[i-positional].Value
			}
			args = append(args, rt+"DecoratorArg{Name: "+strconv.Quote(argName)+", Value: "+rt+"DecoratorArgText("+value+")}")
		}
		return rt + "Decorator{Name: " + strconv.Quote(d.Name.Value) + ", Args: func(" + p + " *" + rt + "Program) []" + rt + "DecoratorArg { " + lexical + "return []" + rt + "DecoratorArg{" + strings.Join(args, ", ") + "} }}", nil
	}
	// The hidden context parameter is bound by the chain, so the plan sees
	// the decorator's remaining parameters only.
	rest := *dec
	rest.Params = dec.Params[1:]
	plan, err := planSharpCall(line, &rest)
	if err != nil {
		return "", err
	}
	params, err := sharpParameters(&rest)
	if err != nil {
		return "", err
	}
	byEvaluation := make([]int, len(plan.Words))
	for parameter, evaluation := range plan.ParameterOrder {
		byEvaluation[evaluation] = parameter
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%sDecorator{Name: %s, Run: func(%s *%sProgram, %s *%sCall) {\n", rt, strconv.Quote(d.Name.Value), p, rt, c, rt)
	out.WriteString(lexical)
	values := make([]string, len(plan.Words))
	for i, w := range plan.Words {
		value, err := e.plannedCallArgument(line, w)
		if err != nil {
			return "", err
		}
		field := params[byEvaluation[i]].field
		typ := e.inferredParams[field]
		if typ == "" {
			typ = "string"
		}
		if field.FieldType != nil || field.FieldTypeExpr != nil {
			if typ, err = e.fieldType(field); err != nil {
				return "", err
			}
		}
		values[i] = fmt.Sprintf("%sdecoratorArg%d", e.prefix, i)
		fmt.Fprintf(&out, "var %s %s = %s\n", values[i], typ, value)
	}
	arguments := []string{p, rt + "Site{Name: " + strconv.Quote(d.Name.Value) + "}", c}
	for _, i := range plan.ParameterOrder {
		arguments = append(arguments, values[i])
	}
	fmt.Fprintf(&out, "%s(%s)\n}}", e.goName(d.Name.Value), strings.Join(arguments, ", "))
	return out.String(), nil
}
