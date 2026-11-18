package lower

import (
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Method callables under the execution runtime. A method is lowered exactly
// like a free callable is: a private capability method that takes the caller's
// program and call site and holds the body, plus a public method that keeps the
// ordinary Go ABI by opening its own program. Only the private method carries
// the marker check, so nothing about a compiled type's exported surface changes
// and a foreign caller reaching the public method gets assistance off.
//
// The private name is the same spelling for every receiver, which is what makes
// a private capability interface — an interface literal naming only the private
// method — resolve a marked method through an interface value without the
// compiler having to know the dynamic type. A receiver that does not satisfy it
// is foreign to this source unit, so the public method is called instead and the
// call runs with assistance off, which is the same answer the engine gives.

// methodPrivateName is the private capability spelling of a method. It mirrors
// goName's execution-mode spelling for free callables so one naming rule covers
// both, and it deliberately does not depend on the receiver: identical spellings
// across receivers are what a private capability interface asserts on.
func (e *emitter) methodPrivateName(name string) string { return e.prefix + "call_" + name }

// receiverBaseType reduces a receiver type spelling to the declared type name a
// method declaration names, dropping the pointer and any instantiation.
func receiverBaseType(typ string) string {
	typ = strings.TrimSpace(typ)
	typ = strings.TrimPrefix(typ, "*")
	if i := strings.IndexByte(typ, '['); i >= 0 {
		typ = typ[:i]
	}
	return strings.TrimSpace(typ)
}

// receiverSpelling is the Go receiver of a method declaration, pointer and
// instantiation included. It reproduces the ordinary declaration's spelling so
// the private and the public method always agree on their receiver.
func receiverSpelling(r *syntax.BashPPReceiver) (name, typ string) {
	name = "_"
	if r.Name != nil {
		name = r.Name.Value
	}
	typ = r.RecvType.Value
	if r.Pointer {
		typ = "*" + typ
	}
	if len(r.TypeParams) > 0 {
		typ += "[" + strings.Join(names(r.TypeParams), ",") + "]"
	}
	return name, typ
}

// methodDeclaration resolves the declaration a call names. The receiver type
// selects it together with the method name: distinct types may declare the same
// method name with different signatures, so resolving by name alone lowers a
// call against another type's signature.
func (e *emitter) methodDeclaration(receiverType, name string) *syntax.BashPPFuncDecl {
	base := receiverBaseType(receiverType)
	if base == "" {
		return nil
	}
	for _, f := range e.methodDeclarations {
		if f == nil || f.Name == nil || f.Receiver == nil || f.Receiver.RecvType == nil {
			continue
		}
		if f.Name.Value == name && f.Receiver.RecvType.Value == base {
			return f
		}
	}
	return nil
}

// methodsNamed reports every declaration of name, on any receiver. It answers
// the promoted and the unresolved-receiver cases, where the declaring type is
// not the one written at the call site; more than one answer is ambiguous and
// must not be guessed at.
func (e *emitter) methodsNamed(name string) []*syntax.BashPPFuncDecl {
	var out []*syntax.BashPPFuncDecl
	for _, f := range e.methodDeclarations {
		if f != nil && f.Name != nil && f.Name.Value == name {
			out = append(out, f)
		}
	}
	return out
}

// methodTypes spells a field list as bare types, repeating a group's type once
// per declared name. A private capability interface must state exactly the
// types the private method declares and no names at all: Go rejects a signature
// that mixes named and unnamed parameters, so a named source parameter beside
// the runtime's program and site parameters does not parse.
func (e *emitter) methodTypes(fields []*syntax.BashPPField) ([]string, error) {
	var out []string
	for i, f := range fields {
		if f == nil {
			return nil, e.fail(nil, CodeType, "missing method field")
		}
		if f.FieldType != nil && strings.HasPrefix(f.FieldType.Value, "func") {
			return nil, e.fail(f, CodeUnsupported, "runtime method callable parameters need native adapters")
		}
		typ := "any"
		if inferred := e.inferredParams[f]; inferred != "" {
			typ = inferred
		}
		if f.FieldType != nil || f.FieldTypeExpr != nil {
			var err error
			if typ, err = e.fieldType(f); err != nil {
				return nil, err
			}
		} else if f.Ellipsis.IsValid() {
			typ = "...any"
		}
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		if f.Ellipsis.IsValid() {
			if i != len(fields)-1 || count != 1 {
				return nil, e.fail(f, CodeType, "variadic parameter must be the final one")
			}
			if !strings.HasPrefix(typ, "...") {
				typ = "..." + typ
			}
		}
		for range count {
			out = append(out, typ)
		}
	}
	return out, nil
}

// methodSignature is the declaration's private parameter and result types.
func (e *emitter) methodSignature(f *syntax.BashPPFuncDecl) (params, results []string, err error) {
	if params, err = e.methodTypes(f.Params); err != nil {
		return nil, nil, err
	}
	if results, err = e.methodTypes(f.Results); err != nil {
		return nil, nil, err
	}
	return params, results, nil
}

// interfaceDecl reports the interface a receiver type names, following named
// declarations of this source unit only. A concrete type answers false, which
// is what routes its calls to the direct private selector.
func (e *emitter) interfaceDecl(typ string) (*syntax.BashPPInterfaceType, bool) {
	decl := e.declaredTypes[receiverBaseType(typ)]
	if decl == nil {
		return nil, false
	}
	iface, ok := decl.DeclTypeExpr.(*syntax.BashPPInterfaceType)
	return iface, ok
}

// interfaceMethodTypes resolves a method of an interface to its bare parameter
// and result types, following embedded interface elements. The interface's own
// specification is the authority here, not any implementation's declaration:
// several types may implement the interface and only the specification names
// the one signature every implementation shares.
func (e *emitter) interfaceMethodTypes(iface *syntax.BashPPInterfaceType, method string, seen map[*syntax.BashPPInterfaceType]bool) (params, results []string, found bool, err error) {
	if iface == nil || seen[iface] {
		return nil, nil, false, nil
	}
	seen[iface] = true
	elems := iface.Elems
	if len(elems) == 0 {
		for _, spec := range iface.Methods {
			elems = append(elems, &syntax.BashPPInterfaceElem{Method: spec})
		}
	}
	for _, elem := range elems {
		if elem == nil {
			continue
		}
		if elem.Method != nil {
			if elem.Method.Name == nil || elem.Method.Name.Value != method {
				continue
			}
			if params, err = e.methodTypes(elem.Method.Params); err != nil {
				return nil, nil, false, err
			}
			if results, err = e.methodTypes(elem.Method.Results); err != nil {
				return nil, nil, false, err
			}
			return params, results, true, nil
		}
		named, ok := elem.Embedded.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil {
			continue
		}
		embedded, ok := e.interfaceDecl(named.Name.Value)
		if !ok {
			continue
		}
		params, results, found, err = e.interfaceMethodTypes(embedded, method, seen)
		if found || err != nil {
			return params, results, found, err
		}
	}
	return nil, nil, false, nil
}

// methodCapability is the private capability interface an implementation must
// satisfy for a call through an interface value to keep its assistance. Only
// the private method appears in it, so satisfaction means the exact runtime ABI
// rather than a name a foreign type could also spell.
func (e *emitter) methodCapability(name string, params, results []string) string {
	return "interface{ " + e.methodPrivateName(name) + e.methodParams(params) + methodResults(results) + " }"
}

// methodHandleType is the type of a captured method handle: the private ABI as
// an ordinary Go function value, so the program and the call site stay
// invocation-time arguments rather than captured state.
func (e *emitter) methodHandleType(params, results []string) string {
	return "func" + e.methodParams(params) + methodResults(results)
}

func (e *emitter) methodParams(params []string) string {
	all := append([]string{"*" + e.prefix + "rt.Program", e.prefix + "rt.Site"}, params...)
	return "(" + strings.Join(all, ", ") + ")"
}

func methodResults(results []string) string {
	if len(results) == 0 {
		return ""
	}
	return " (" + strings.Join(results, ", ") + ")"
}

// runtimeMethodFunction lowers a method declaration to the private capability
// method that holds the body and the public method that keeps the ordinary Go
// ABI. Only the private method performs the marker check, so a Go caller that
// never went through this compiler reaches the public method and runs with
// assistance off.
func (e *emitter) runtimeMethodFunction(f *syntax.BashPPFuncDecl, signature, body, generics string) (string, error) {
	if f == nil || f.Receiver == nil || f.Receiver.RecvType == nil || f.Name == nil {
		return "", e.fail(f, CodeUnsupported, "runtime method lowering needs a named receiver declaration")
	}
	entry, err := e.programEntry(f.Name.Value, f.Agentic != nil, f.Results, len(f.Decorators) > 0)
	if err != nil {
		return "", err
	}
	decorators := ""
	if len(f.Decorators) > 0 {
		if body, decorators, err = e.decoratedBody(f, signature, body); err != nil {
			return "", err
		}
	}
	var args []string
	for _, field := range f.Params {
		if field.FieldType != nil && strings.HasPrefix(field.FieldType.Value, "func") {
			return "", e.fail(field, CodeUnsupported, "runtime public callable parameters need native adapters")
		}
		if len(field.Names) == 0 {
			return "", e.fail(field, CodeUnsupported, "a runtime method parameter needs a name to forward")
		}
		for _, name := range field.Names {
			args = append(args, name.Value)
		}
		if field.Ellipsis.IsValid() {
			args[len(args)-1] += "..."
		}
	}
	storage, values, err := e.resultStorage(f.Results)
	if err != nil {
		return "", err
	}
	receiver, receiverType := receiverSpelling(f.Receiver)
	recv := "(" + receiver + " " + receiverType + ") "
	private := e.methodPrivateName(f.Name.Value)
	p := e.prefix + "program"
	// Go 1.27 lets a method declare type parameters of its own, so both halves
	// of the pair carry the same list. The wrapper instantiates the private
	// half EXPLICITLY with those same names rather than leaving it to
	// inference: a parameter used only in the results is not inferable, and
	// the names are in scope in the wrapper either way.
	instantiation := ""
	if len(f.TypeParams) > 0 {
		var own []string
		for _, param := range f.TypeParams {
			own = append(own, names(param.Names)...)
		}
		instantiation = "[" + strings.Join(own, ", ") + "]"
	}
	invocation := receiver + "." + private + instantiation + "(" + p + ", " + e.prefix + "rt.Site{Name:" + strconv.Quote(f.Name.Value) + "}"
	if len(args) > 0 {
		invocation += ", " + strings.Join(args, ", ")
	}
	invocation += ")"
	if values != "" {
		invocation = values + " = " + invocation
	}
	wrapper := "func " + recv + f.Name.Value + generics + signature + " {\n" +
		p + ", err := " + e.prefix + "rt.NewProgram()\n" +
		"if err != nil { panic(err) }\n" + storage +
		"err = " + p + ".Run(func(" + p + " *" + e.prefix + "rt.Program){\n" + invocation + "\n})\n" +
		"if err != nil { panic(err) }\n" +
		"return " + values + "\n}\n"
	return e.mark(f) + "func " + recv + private + generics + e.privateSignature(signature) + " {\n" + entry + body + "}\n" + wrapper + decorators, nil
}

// promotedInterfaceSource names the embedded interface field a promoted method
// actually comes from, as a selector suffix on the receiver expression.
type promotedInterfaceSource struct {
	selector string
	iface    *syntax.BashPPInterfaceType
}

// promotedInterfaceSource resolves a method that the receiver's own type does
// not declare to the embedded interface field that supplies it.
//
// Go promotes an embedded interface's method set to the outer struct, but only
// the methods the interface itself names — its public ones. The private
// capability method is not part of an interface type unless that interface
// spells it, so a promoted call cannot be lowered as a direct private selector
// on the outer type: the outer type has no such method and the Go checker says
// so. The selector down to the embedded field is an ordinary interface value
// though, so the very same capability dispatch a directly typed interface
// receiver gets applies to it, which is what keeps the caller's program, frame,
// channel and agentic authority on a promoted call.
//
// A nil result means there is nothing to reroute: either the method is not
// promoted at all, or it is promoted from an embedded concrete type, whose
// private method Go promotes exactly as it promotes the public one.
func (e *emitter) promotedInterfaceSource(node syntax.Node, receiverType, method string) (*promotedInterfaceSource, error) {
	base := receiverBaseType(receiverType)
	if base == "" || e.declaredTypes[base] == nil {
		return nil, nil
	}
	// A receiver already typed as an interface is dispatched as one; promotion
	// is about a struct that embeds one.
	if _, ok := e.interfaceDecl(base); ok {
		return nil, nil
	}
	if e.methodDeclaration(base, method) != nil {
		return nil, nil
	}
	type embeddedNode struct {
		selector string
		typ      string
	}
	level := []embeddedNode{{"", base}}
	visited := map[string]bool{base: true}
	// Go resolves a selector at the shallowest embedding depth that has it, and
	// calls a tie at that depth ambiguous rather than picking one. Walking depth
	// by depth reproduces both halves of that rule.
	for len(level) > 0 {
		var next []embeddedNode
		var found []*promotedInterfaceSource
		concrete := 0
		for _, current := range level {
			fields, ok := promotedFields(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: current.typ}}, e.declaredTypes, map[string]bool{})
			if !ok {
				continue
			}
			for _, field := range fields {
				if !field.embedded {
					continue
				}
				selector := current.selector + "." + field.name
				if iface, ok := e.interfaceDecl(field.name); ok {
					_, _, has, err := e.interfaceMethodTypes(iface, method, map[*syntax.BashPPInterfaceType]bool{})
					if err != nil {
						return nil, err
					}
					if has {
						found = append(found, &promotedInterfaceSource{selector: selector, iface: iface})
						continue
					}
				} else if e.methodDeclaration(field.name, method) != nil {
					concrete++
					continue
				}
				if !visited[field.name] {
					visited[field.name] = true
					next = append(next, embeddedNode{selector: selector, typ: field.name})
				}
			}
		}
		if len(found)+concrete > 1 {
			return nil, e.fail(node, CodeType, "method "+method+" is promoted from several embedded fields of "+base)
		}
		if len(found) == 1 {
			return found[0], nil
		}
		if concrete == 1 {
			return nil, nil
		}
		level = next
	}
	return nil, nil
}

// runtimeMethodCall lowers a method call. receiverType is the receiver's static
// type as the caller resolved it; it, and not the method name on its own,
// chooses the declaration, so two receivers declaring one name keep their own
// signatures.
func (e *emitter) runtimeMethodCall(c *syntax.BashPPCall, receiverType string) (string, error) {
	if c == nil || len(c.Fun) != 2 {
		return "", e.fail(c, CodeExpr, "a method call needs a receiver and a method name")
	}
	if len(c.ArgNames) > 0 {
		return "", e.fail(c, CodeUnsupported, "named arguments require a resolved callable signature")
	}
	receiver := c.Fun[0].Value
	method := c.Fun[1].Value
	// Arguments are emitted in source order and passed straight through to the
	// typed parameters, so evaluation stays left to right and an untyped
	// constant still converts to the parameter's type rather than to a default.
	var args []string
	for i := range c.Args {
		value, err := e.callArgument(c, i)
		if err != nil {
			return "", err
		}
		args = append(args, value)
	}
	spread := ""
	if c.Ellipsis.IsValid() {
		spread = "..."
	}
	// Explicit instantiation of a method's own type parameters. Inference
	// covers the rest, so an absent list stays absent in the emitted call.
	typeargs, err := e.typeArgs(c.TypeArgs)
	if err != nil {
		return "", err
	}
	if iface, ok := e.interfaceDecl(receiverType); ok {
		// Go 1.27 gave methods type parameters but not interfaces, so there is
		// nothing on an interface method to instantiate.
		if typeargs != "" {
			return "", e.fail(c, CodeUnsupported, "an interface method takes no type arguments")
		}
		params, results, found, err := e.interfaceMethodTypes(iface, method, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return "", err
		}
		if !found {
			return "", e.fail(c, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+method)
		}
		return e.interfaceMethodCall(receiver, method, params, results, args, spread), nil
	}
	promoted, err := e.promotedInterfaceSource(c, receiverType, method)
	if err != nil {
		return "", err
	}
	if promoted != nil {
		if typeargs != "" {
			return "", e.fail(c, CodeUnsupported, "an interface method takes no type arguments")
		}
		params, results, found, err := e.interfaceMethodTypes(promoted.iface, method, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return "", err
		}
		if !found {
			return "", e.fail(c, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+method)
		}
		return e.interfaceMethodCall(receiver+promoted.selector, method, params, results, args, spread), nil
	}
	decl, err := e.resolveMethod(c, receiverType, method)
	if err != nil {
		return "", err
	}
	if decl == nil {
		// Foreign to this source unit: there is no private method to reach, so
		// the ordinary Go call is the whole lowering and it runs with
		// assistance off.
		return receiver + "." + method + typeargs + "(" + strings.Join(args, ", ") + spread + ")", nil
	}
	// A declaration whose own signature this compiler cannot spell would emit a
	// call the Go type checker rejects at an unmapped position instead.
	if _, _, err := e.methodSignature(decl); err != nil {
		return "", err
	}
	call := receiver + "." + e.methodPrivateName(method) + typeargs + "(" + e.program() + ", " + e.callSite(c, method)
	if len(args) > 0 {
		call += ", " + strings.Join(args, ", ") + spread
	}
	return call + ")", nil
}

// interfaceMethodCall dispatches through the private capability interface, and
// falls back to the public method for an implementation that does not carry the
// private ABI. The fallback returns, which is the whole reason the private
// branch is written as a statement rather than a value: a zero-result private
// call that fell through would run the public method as well and repeat the
// method's effects, or ask a second time for a permission already denied.
func (e *emitter) interfaceMethodCall(receiver, method string, params, results, args []string, spread string) string {
	value := e.prefix + "receiver"
	capability := e.prefix + "capability"
	ok := e.prefix + "ok"
	arguments := ""
	if len(args) > 0 {
		arguments = ", " + strings.Join(args, ", ") + spread
	}
	ret := "return "
	tail := ""
	if len(results) == 0 {
		ret = ""
		tail = "\nreturn"
	}
	return "func()" + methodResults(results) + " {\n" +
		value + " := " + receiver + "\n" +
		"if " + capability + ", " + ok + " := " + value + ".(" + e.methodCapability(method, params, results) + "); " + ok + " {\n" +
		ret + capability + "." + e.methodPrivateName(method) + "(" + e.program() + ", " + e.prefix + "rt.Site{Name:" + strconv.Quote(method) + "}" + arguments + ")" + tail + "\n}\n" +
		ret + value + "." + method + "(" + strings.Join(args, ", ") + spread + ")" + tail + "\n}()"
}

// runtimeMethodHandleType is the Go type of the handle runtimeMethodHandle
// produces. A handle has to be stored — in a variable, a parameter or a result
// — and the storage has to state the private ABI, so resolving the type is part
// of the method API rather than something a caller re-derives.
func (e *emitter) runtimeMethodHandleType(node syntax.Node, receiverType, methodName string) (string, error) {
	params, results, err := e.methodHandleSignature(node, receiverType, methodName)
	if err != nil {
		return "", err
	}
	return e.methodHandleType(params, results), nil
}

// methodHandleSignature resolves the private parameter and result types a
// handle on methodName carries, from the interface's specification when the
// receiver is one and from the resolved declaration otherwise.
func (e *emitter) methodHandleSignature(node syntax.Node, receiverType, methodName string) (params, results []string, err error) {
	if iface, ok := e.interfaceDecl(receiverType); ok {
		params, results, found, err := e.interfaceMethodTypes(iface, methodName, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, e.fail(node, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+methodName)
		}
		return params, results, nil
	}
	promoted, err := e.promotedInterfaceSource(node, receiverType, methodName)
	if err != nil {
		return nil, nil, err
	}
	if promoted != nil {
		params, results, found, err := e.interfaceMethodTypes(promoted.iface, methodName, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, e.fail(node, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+methodName)
		}
		return params, results, nil
	}
	decl, err := e.resolveMethod(node, receiverType, methodName)
	if err != nil {
		return nil, nil, err
	}
	if decl == nil {
		return nil, nil, e.fail(node, CodeUnsupported, "a method handle needs a declaration of "+methodName+" in this source unit")
	}
	return e.methodSignature(decl)
}

// resolveMethod chooses the declaration a concrete receiver reaches. A name
// declared on several receivers with no receiver type to choose between them is
// a diagnostic: guessing one lowers the call against another type's signature.
// A single declaration answers both the exact and the promoted case, because
// Go's own selector resolves promotion on the private method as it does on the
// public one. No declaration at all means the method is foreign to this unit.
func (e *emitter) resolveMethod(node syntax.Node, receiverType, methodName string) (*syntax.BashPPFuncDecl, error) {
	if decl := e.methodDeclaration(receiverType, methodName); decl != nil {
		return decl, nil
	}
	named := e.methodsNamed(methodName)
	switch {
	case len(named) > 1:
		return nil, e.fail(node, CodeType, "method "+methodName+" is declared on several receivers; the call site needs a resolved receiver type")
	case len(named) == 1:
		return named[0], nil
	}
	return nil, nil
}

// runtimeMethodHandle captures a method as a value. The receiver is captured
// once, when the handle is taken, and the program and call site stay ordinary
// parameters, so an invocation is checked against the frame it is made from
// rather than the frame the handle was created in.
func (e *emitter) runtimeMethodHandle(node syntax.Node, receiverExpr, receiverType, methodName string) (string, error) {
	if receiverExpr == "" || methodName == "" {
		return "", e.fail(node, CodeExpr, "a method handle needs a receiver and a method name")
	}
	if iface, ok := e.interfaceDecl(receiverType); ok {
		params, results, found, err := e.interfaceMethodTypes(iface, methodName, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return "", err
		}
		if !found {
			return "", e.fail(node, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+methodName)
		}
		return e.interfaceMethodHandle(receiverExpr, methodName, params, results), nil
	}
	promoted, err := e.promotedInterfaceSource(node, receiverType, methodName)
	if err != nil {
		return "", err
	}
	if promoted != nil {
		params, results, found, err := e.interfaceMethodTypes(promoted.iface, methodName, map[*syntax.BashPPInterfaceType]bool{})
		if err != nil {
			return "", err
		}
		if !found {
			return "", e.fail(node, CodeUndefined, "interface "+receiverBaseType(receiverType)+" has no method "+methodName)
		}
		return e.interfaceMethodHandle(receiverExpr+promoted.selector, methodName, params, results), nil
	}
	decl, err := e.resolveMethod(node, receiverType, methodName)
	if err != nil {
		return "", err
	}
	if decl == nil {
		return "", e.fail(node, CodeUnsupported, "a method handle needs a declaration of "+methodName+" in this source unit")
	}
	if _, _, err := e.methodSignature(decl); err != nil {
		return "", err
	}
	// Go's own method value: it captures the receiver once with the receiver's
	// own rules — a copy for a value receiver, the address of an addressable
	// operand for a pointer one — and its type is already the private ABI.
	return receiverExpr + "." + e.methodPrivateName(methodName), nil
}

// interfaceMethodHandle captures a handle through an interface value. Both
// branches capture at handle-creation time and produce the same private ABI, so
// the fallback for a foreign implementation is a handle too, not a second
// resolution deferred to the invocation.
func (e *emitter) interfaceMethodHandle(receiver, method string, params, results []string) string {
	value := e.prefix + "receiver"
	capability := e.prefix + "capability"
	ok := e.prefix + "ok"
	public := e.prefix + "public"
	var declared, forwarded []string
	for i, typ := range params {
		name := e.prefix + "arg" + strconv.Itoa(i)
		declared = append(declared, name+" "+typ)
		if strings.HasPrefix(typ, "...") {
			name += "..."
		}
		forwarded = append(forwarded, name)
	}
	signature := "(" + strings.Join(append([]string{e.prefix + "program *" + e.prefix + "rt.Program", e.prefix + "site " + e.prefix + "rt.Site"}, declared...), ", ") + ")" + methodResults(results)
	ret := "return "
	if len(results) == 0 {
		ret = ""
	}
	return "func() " + e.methodHandleType(params, results) + " {\n" +
		value + " := " + receiver + "\n" +
		"if " + capability + ", " + ok + " := " + value + ".(" + e.methodCapability(method, params, results) + "); " + ok + " {\n" +
		"return " + capability + "." + e.methodPrivateName(method) + "\n}\n" +
		public + " := " + value + "." + method + "\n" +
		"return func" + signature + " {\n" +
		ret + public + "(" + strings.Join(forwarded, ", ") + ")\n}\n}()"
}
