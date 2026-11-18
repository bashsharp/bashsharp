package lower

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A `func`-spelled result carries no committed type: its native shape comes
// from the literal the declaration returns. Runtime-backed units then have two
// spellings for that one result. The private half of a declaration is invoked
// with the caller's Program, so the closure it hands back is invoked the same
// way and its type carries the private leading parameters. The public wrapper
// is what a native Go caller holds, so its result must be the signature source
// wrote, with no runtime types in it.
//
// This file owns that pair: inference records the public spelling once, the
// declaration and its result storage use whichever spelling the current ABI
// calls for, and the wrapper's return adapter binds the private closure to the
// Program that created it.

// callableResultField reports a result whose native shape is inferred rather
// than committed.
func callableResultField(f *syntax.BashPPField) bool {
	return f != nil && f.FieldType != nil && f.FieldType.Value == "func"
}

// callableResults reports whether a declaration returns any inferred callable,
// i.e. whether its public wrapper needs a return adapter at all.
func callableResults(fields []*syntax.BashPPField) bool {
	for _, f := range fields {
		if callableResultField(f) {
			return true
		}
	}
	return false
}

// returnedFuncLit is the literal a `func` result is inferred from: the first
// one the body returns. Inference and the return adapter must agree on which
// literal that is, so both ask here rather than walking independently.
func returnedFuncLit(body *syntax.Block) *syntax.BashPPFuncLit {
	var found *syntax.BashPPFuncLit
	syntax.Walk(body, func(n syntax.Node) bool {
		if found != nil {
			return false
		}
		if ret, ok := n.(*syntax.BashPPReturn); ok && ret.FuncLit != nil {
			found = ret.FuncLit
			return false
		}
		return true
	})
	return found
}

// recordCallableResult stores the public native spelling inferred for a
// callable result. callableParams already maps callable *parameter* fields to
// their resolved native signature; result fields are distinct nodes, so the
// same map carries both without collision and every reader stays in one place.
func (e *emitter) recordCallableResult(f *syntax.BashPPField, native string) {
	if callableResultField(f) && native != "" {
		e.callableParams[f] = native
	}
}

// callableResultType returns the recorded public spelling. Missing inference
// is reported rather than widened to `any`: source promised a callable, and a
// public wrapper that returns `any` is not the signature it promised.
func (e *emitter) callableResultType(f *syntax.BashPPField) (string, bool) {
	native := e.callableParams[f]
	if !callableResultField(f) || native == "" {
		return "", false
	}
	return native, true
}

// callableResultABI is the spelling a callable result has in the ABI being
// emitted. Without a runtime the two halves coincide. With one, the declared
// type is the private closure type, because that is what the body's literal
// lowers to and what an in-unit call site invokes.
func (e *emitter) callableResultABI(f *syntax.BashPPField) (string, bool) {
	public, ok := e.callableResultType(f)
	if !ok {
		return "", false
	}
	if !e.execution {
		return public, true
	}
	return "func" + e.privateSignature(strings.TrimPrefix(public, "func")), true
}

// methodResultField reports that a result belongs to a receiver declaration.
// Runtime methods are lowered by their own public/private pair, which has no
// return adapter, so a callable result there is refused with its own message
// instead of emitting a wrapper whose halves disagree.
func (e *emitter) methodResultField(f *syntax.BashPPField) bool {
	for _, decl := range e.methodDeclarations {
		for _, result := range decl.Results {
			if result == f {
				return true
			}
		}
	}
	return false
}

// publicResultList spells the wrapper's declared results. It is the source
// signature with every callable result kept public, which is the only place
// the two halves of a runtime declaration differ.
func (e *emitter) publicResultList(fields []*syntax.BashPPField) (string, error) {
	var parts []string
	for _, f := range fields {
		typ, ok := e.callableResultType(f)
		if !ok {
			var err error
			typ = "any"
			if f.FieldType != nil || f.FieldTypeExpr != nil {
				if typ, err = e.fieldType(f); err != nil {
					return "", err
				}
			}
		}
		prefix := ""
		if declared := names(f.Names); len(declared) > 0 {
			prefix = strings.Join(declared, ",") + " "
		}
		parts = append(parts, prefix+typ)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " (" + strings.Join(parts, ",") + ")", nil
}

// publicSignature rebuilds a runtime declaration's public signature. The
// parameters are re-spelled from the same fields the private half used — the
// two halves never differ there — and only the results are taken public.
func (e *emitter) publicSignature(f *syntax.BashPPFuncDecl) (string, error) {
	params, err := e.fields(f.Params)
	if err != nil {
		return "", err
	}
	results, err := e.publicResultList(f.Results)
	if err != nil {
		return "", err
	}
	return "(" + params + ")" + results, nil
}

// capturedProgramName is the wrapper-local binding for the Program that ran
// the private invocation. A callable that outlives that invocation is bound to
// it here, once, so every escaped closure of one call shares one owner.
func (e *emitter) capturedProgramName() string { return e.prefix + "capturedProgram" }

// callableReturnAdapter converts one private closure into the public callable
// the wrapper returns. The adapter supplies the creating Program rather than
// starting a new one: a program that has finished has already revoked its
// channel authority, and reviving it would hand an escaped callable channels
// its owner no longer holds. Its retained non-channel lexical state stays
// exactly as the invocation left it.
func (e *emitter) callableReturnAdapter(f *syntax.BashPPField, literal *syntax.BashPPFuncLit, value, site string) (public, adapter string, err error) {
	public, ok := e.callableResultType(f)
	if !ok {
		return "", "", e.fail(f, CodeUnsupported, "returned callable adapter needs an inferable native signature")
	}
	if literal == nil {
		return "", "", e.fail(f, CodeUnsupported, "returned callable adapter needs the returned literal")
	}
	var args []string
	for _, param := range literal.Params {
		declared := names(param.Names)
		if len(declared) == 0 {
			return "", "", e.fail(f, CodeUnsupported, "returned callable adapter needs named parameters to forward")
		}
		for _, name := range declared {
			if name == "_" {
				return "", "", e.fail(f, CodeUnsupported, "returned callable adapter cannot forward a blank parameter")
			}
			args = append(args, name)
		}
		if param.Ellipsis.IsValid() {
			args[len(args)-1] += "..."
		}
	}
	call := value + "(" + e.capturedProgramName() + "," + site
	if len(args) > 0 {
		call += "," + strings.Join(args, ",")
	}
	call += ")"
	if len(literal.Results) == 0 {
		return public, public + " {\n" + call + "\n}", nil
	}
	return public, public + " {\nreturn " + call + "\n}", nil
}

// publicReturnValues adapts the private invocation's stored results to the
// wrapper's declared ones. Only inferred callables are adapted; every other
// result already has one spelling and is returned untouched.
//
// The adapter is bound through a declared local rather than written inline,
// because a refused entry — a marked callable an unmarked caller reached —
// stores the zero closure and must hand the caller a nil callable, not a live
// wrapper around one that would panic on the first call.
func (e *emitter) publicReturnValues(f *syntax.BashPPFuncDecl, values string) (prologue, returned string, err error) {
	if values == "" {
		return "", "", nil
	}
	stored := strings.Split(values, ",")
	fields := expandedResultFields(f.Results)
	literal := returnedFuncLit(f.Body)
	site := e.prefix + "rt.Site{Name:" + strconv.Quote(f.Name.Value) + "}"
	for i := range stored {
		if i >= len(fields) || !callableResultField(fields[i]) {
			continue
		}
		public, adapter, err := e.callableReturnAdapter(fields[i], literal, stored[i], site)
		if err != nil {
			return "", "", err
		}
		name := fmt.Sprintf("%spublic%d", e.prefix, i)
		prologue += "var " + name + " " + public + "\n" +
			"if " + stored[i] + " != nil {\n" + name + " = " + adapter + "\n}\n"
		stored[i] = name
	}
	return prologue, strings.Join(stored, ","), nil
}

// expandedResultFields mirrors resultStorage's expansion: one field per stored
// result value, so an adapter can be matched to the value it converts.
func expandedResultFields(fields []*syntax.BashPPField) []*syntax.BashPPField {
	var out []*syntax.BashPPField
	for _, f := range fields {
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			out = append(out, f)
		}
	}
	return out
}
