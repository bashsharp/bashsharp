// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Instantiation closure of the original program's generic declarations.
//
// The dependency helper materialises an instantiated local generic type under
// a generated name and registers it under the instantiation's spelling, so a
// value of that type resolves when it crosses the bridge. The materialised
// set was read from the instantiations the program SPELLS — `Box[int]` in a
// declaration or a literal — which misses every instantiation the program
// only REACHES: `Make[int]()` returning `Box[T]`, a generic method body
// building `Option[T]{}`, `FromIterator[R](IteratorFunc[R](nil))` inside
// `Foo[int, int]`. Those values carry the instantiated identity at run time
// (`Box[int]`), and the helper has nothing registered under it.
//
// The set of instantiations a program can reach is the closure of the ones
// it spells through the generic declarations it instantiates: binding a
// generic function's or type's parameters to concrete arguments and reading
// its signature, body and method bodies with those bindings yields the next
// instantiations. That is what the type checker computed when it checked the
// program; it is recomputed here from the loaded declarations so the helper's
// fixed type namespace covers every identity a value can present. Go's own
// rule that instantiation cannot be infinitely nested (`List[List[T]]` in
// its own body is rejected by the compiler) is what makes the closure finite;
// the bound below is a guard against a program the checker did not reject,
// never a knob.

// bashPPInstantiationBound caps the closure so a pathological declaration
// set cannot make session start-up unbounded. A program that reaches more
// distinct instantiations than this is registered up to the bound; the
// remainder fail closed with the unregistered-type refusal as before.
const bashPPInstantiationBound = 1024

// bashPPInstantiationIndex is the per-file closure, computed once per runner
// toolchain on first use and shared by the runners that copy the toolchain.
type bashPPInstantiationIndex struct {
	file  *syntax.File
	named map[string]*syntax.BashPPNamedType
	// imported holds the concrete instantiations of imported generic
	// functions the program reaches, keyed by bashPPImportedInstanceKey;
	// see bashpp_sprint171_imported_instances.go.
	imported map[string]bashPPImportedInstantiation
}

// bashPPImportedInstantiation is one reached binding of an imported generic
// function — `maps.Clone` with `map[string]int` — as the program spells it.
type bashPPImportedInstantiation struct {
	alias, name string
	args        []syntax.BashPPTypeExpr
}

// bashPPInstantiationItem is one binding of a generic declaration to
// concrete type arguments: a type instantiation `T[args]` or a function
// instantiation `F[args]`.
type bashPPInstantiationItem struct {
	function bool
	name     string
	args     []syntax.BashPPTypeExpr
}

func (r *Runner) bashPPReachedInstantiations() map[string]*syntax.BashPPNamedType {
	return r.bashPPInstantiationIndexFor().named
}

// bashPPReachedImportedInstantiations is the closure's imported generic
// function instantiations, keyed by bashPPImportedInstanceKey.
func (r *Runner) bashPPReachedImportedInstantiations() map[string]bashPPImportedInstantiation {
	return r.bashPPInstantiationIndexFor().imported
}

func (r *Runner) bashPPInstantiationIndexFor() *bashPPInstantiationIndex {
	file := r.bashPPGoSourceFile
	if file == nil {
		return &bashPPInstantiationIndex{}
	}
	if cache := r.bashPPTools.instantiations; cache != nil && cache.file == file {
		return cache
	}
	index := &bashPPInstantiationIndex{file: file}
	index.named, index.imported = bashPPInstantiationClosure(file)
	r.bashPPTools.instantiations = index
	return index
}

// bashPPInstantiationClosure computes every concrete named-type instantiation
// the program reaches, keyed by its spelling, and every concrete
// instantiation of an imported generic function it reaches (a call of
// `alias.Name` whose type arguments — spelled or inferred by the checker —
// are concrete after substitution), keyed by bashPPImportedInstanceKey. A
// qualified callee with type arguments can only be a package's generic
// function (a method has no type parameters), so the import table is not
// consulted here; the helper registers an instantiation only for a package
// it imports, and the call names one only through an import alias.
func bashPPInstantiationClosure(file *syntax.File) (map[string]*syntax.BashPPNamedType, map[string]bashPPImportedInstantiation) {
	generics := map[string]*syntax.BashPPDecl{}
	funcs := map[string]*syntax.BashPPFuncDecl{}
	methods := map[string][]*syntax.BashPPFuncDecl{}
	var concrete []*syntax.Stmt
	for _, stmt := range file.Stmts {
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			if d.Site == syntax.StartTypeDecl && len(d.TypeParams) > 0 && d.DeclTypeExpr != nil && d.Name != nil {
				generics[d.Name.Value] = d
				continue
			}
		case *syntax.BashPPFuncDecl:
			if d.Receiver != nil && d.Receiver.RecvType != nil && len(d.Receiver.TypeParams) > 0 {
				methods[d.Receiver.RecvType.Value] = append(methods[d.Receiver.RecvType.Value], d)
				continue
			}
			if d.Receiver == nil && len(d.TypeParams) > 0 && d.Name != nil {
				funcs[d.Name.Value] = d
				continue
			}
		}
		concrete = append(concrete, stmt)
	}
	out := map[string]*syntax.BashPPNamedType{}
	imported := map[string]bashPPImportedInstantiation{}
	seen := map[string]bool{}
	var queue []bashPPInstantiationItem
	// enqueue records one binding; a spelling seen before is not re-walked.
	enqueue := func(item bashPPInstantiationItem) {
		if len(seen) >= bashPPInstantiationBound {
			return
		}
		key := item.name + "[" + bashPPInstantiationArgsText(item.args) + "]"
		if item.function {
			key = "func " + key
		}
		if seen[key] {
			return
		}
		seen[key] = true
		queue = append(queue, item)
	}
	// collect walks one node under a binding, substituting the bound
	// parameters into every instantiation spelled inside it and enqueueing
	// the concrete results. params is the set of names that must all be
	// bound for a spelling to be concrete.
	collect := func(node syntax.Node, binding map[string]syntax.BashPPTypeExpr, params map[string]bool) {
		if node == nil {
			return
		}
		syntax.Walk(node, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.BashPPNamedType:
				if x.Name == nil || len(x.TypeArgs) == 0 || generics[x.Name.Value] == nil {
					return true
				}
				args := make([]syntax.BashPPTypeExpr, len(x.TypeArgs))
				for i, arg := range x.TypeArgs {
					args[i] = bashPPSubstituteType(arg.ArgType, binding)
				}
				if bashPPInstantiationConcrete(args, params) {
					enqueue(bashPPInstantiationItem{name: x.Name.Value, args: args})
				}
			case *syntax.BashPPCall:
				if len(x.TypeArgs) == 0 {
					return true
				}
				args := make([]syntax.BashPPTypeExpr, len(x.TypeArgs))
				for i, arg := range x.TypeArgs {
					args[i] = bashPPSubstituteType(arg.ArgType, binding)
				}
				if !bashPPInstantiationConcrete(args, params) {
					return true
				}
				if len(x.Fun) == 2 {
					item := bashPPImportedInstantiation{alias: x.Fun[0].Value, name: x.Fun[1].Value, args: args}
					imported[bashPPImportedInstanceKey(item.alias+"."+item.name, args)] = item
					return true
				}
				if len(x.Fun) != 1 || funcs[x.Fun[0].Value] == nil {
					return true
				}
				enqueue(bashPPInstantiationItem{function: true, name: x.Fun[0].Value, args: args})
			}
			return true
		})
	}
	for _, stmt := range concrete {
		collect(stmt, nil, nil)
	}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.function {
			decl := funcs[item.name]
			names := bashPPTypeParamNames(decl.TypeParams)
			binding, params := bashPPInstantiationBinding(names, item.args)
			if binding == nil {
				continue
			}
			collect(decl, binding, params)
			continue
		}
		decl := generics[item.name]
		names := bashPPTypeParamNames(decl.TypeParams)
		binding, params := bashPPInstantiationBinding(names, item.args)
		if binding == nil {
			continue
		}
		named := &syntax.BashPPNamedType{Name: decl.Name, TypeArgs: make([]*syntax.BashPPTypeArg, len(item.args))}
		for i, arg := range item.args {
			named.TypeArgs[i] = &syntax.BashPPTypeArg{ArgType: arg}
		}
		out[bashPPTypeText(named)] = named
		collect(decl.DeclTypeExpr, binding, params)
		for _, method := range methods[item.name] {
			if len(method.TypeParams) > 0 {
				// A method with its own type parameters is bound per call,
				// which the closure does not model; its body is not walked.
				continue
			}
			receiverNames := make([]string, len(method.Receiver.TypeParams))
			for i, name := range method.Receiver.TypeParams {
				receiverNames[i] = name.Value
			}
			receiverBinding, receiverParams := bashPPInstantiationBinding(receiverNames, item.args)
			if receiverBinding == nil {
				continue
			}
			collect(method, receiverBinding, receiverParams)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	if len(imported) == 0 {
		imported = nil
	}
	return out, imported
}

func bashPPTypeParamNames(groups []*syntax.BashPPTypeParam) []string {
	var names []string
	for _, group := range groups {
		for _, name := range group.Names {
			names = append(names, name.Value)
		}
	}
	return names
}

// bashPPInstantiationBinding pairs a declaration's parameter names with the
// instantiation's arguments. A blank parameter binds nothing and constrains
// nothing; an arity mismatch is not a binding.
func bashPPInstantiationBinding(names []string, args []syntax.BashPPTypeExpr) (map[string]syntax.BashPPTypeExpr, map[string]bool) {
	if len(names) != len(args) {
		return nil, nil
	}
	binding := map[string]syntax.BashPPTypeExpr{}
	params := map[string]bool{}
	for i, name := range names {
		if name == "_" {
			continue
		}
		binding[name] = args[i]
		params[name] = true
	}
	return binding, params
}

// bashPPInstantiationConcrete reports whether no argument still names one of
// the enclosing declaration's type parameters after substitution.
func bashPPInstantiationConcrete(args []syntax.BashPPTypeExpr, params map[string]bool) bool {
	if len(params) == 0 {
		return true
	}
	for _, arg := range args {
		unbound := false
		syntax.Walk(arg, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.BashPPTypeParamType:
				if x.Name != nil && params[x.Name.Value] {
					unbound = true
				}
			case *syntax.BashPPNamedType:
				if x.Name != nil && len(x.TypeArgs) == 0 && params[x.Name.Value] {
					unbound = true
				}
			}
			return !unbound
		})
		if unbound {
			return false
		}
	}
	return true
}

func bashPPInstantiationArgsText(args []syntax.BashPPTypeExpr) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = bashPPTypeText(arg)
	}
	return strings.Join(parts, ", ")
}

// bashPPBridgeInstantiatedScalar restores the instantiated spelling on a
// scalar that crossed with only its generic base name. A scalar cell records
// its defined type as the bare declaration name (`T`), which is the identity
// the scalar evaluator keys on; the value's type is the instantiation the
// cell was declared with (`T[int]`), and that is the identity the helper
// registered. A cell whose declared type is not an instantiation of that
// name is left alone.
func bashPPBridgeInstantiatedScalar(value bashPPBridgeValue, typ syntax.BashPPTypeExpr) bashPPBridgeValue {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || len(named.TypeArgs) == 0 || value.Type != named.Name.Value {
		return value
	}
	value.Type = bashPPTypeText(named)
	return value
}
