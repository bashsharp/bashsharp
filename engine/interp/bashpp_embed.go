// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPEmbedEdge struct {
	name    string
	pointer bool
}

type bashPPSelection struct {
	edges         []bashPPEmbedEdge
	fieldType     syntax.BashPPTypeExpr
	method        *bashPPFunc
	receiverType  syntax.BashPPTypeExpr
	interfaceSpec *syntax.BashPPMethodSpec
	ambiguous     bool
}

type bashPPSelectionNode struct {
	typ       syntax.BashPPTypeExpr
	edges     []bashPPEmbedEdge
	indirect  bool
	ancestors map[string]bool
}

func bashPPEmbeddedFieldName(field *syntax.BashPPField) (string, bool) {
	if field == nil || !field.Embedded || field.FieldTypeExpr == nil {
		return "", false
	}
	typ := field.FieldTypeExpr
	if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = pointer.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return "", false
	}
	// Go spec, Struct types: "The unqualified type name acts as the field
	// name." An embedded `sync.Mutex` is therefore selected as `.Mutex`, not
	// as `.sync.Mutex`, which is not even a single selector.
	name := named.Name.Value
	if _, unqualified, ok := strings.Cut(name, "."); ok {
		name = unqualified
	}
	return name, true
}

// bashPPDeclaredFieldNames lists every selector name one field contributes. An
// embedded field contributes the implicit name of its type; a named field
// contributes each name in its declaration group, so Go's grouped
// `X, Y float64` form is selectable under both names rather than under neither.
func bashPPDeclaredFieldNames(field *syntax.BashPPField) []string {
	if name, ok := bashPPEmbeddedFieldName(field); ok {
		return []string{name}
	}
	if field == nil || len(field.Names) == 0 {
		return nil
	}
	names := make([]string, 0, len(field.Names))
	for _, n := range field.Names {
		names = append(names, n.Value)
	}
	return names
}

// bashPPEmbeddedTarget resolves only aliases while retaining defined type
// identity. An alias may denote a pointer and is then an indirect embedded
// edge; a defined pointer type is not a legal embedded field in Go.
func (r *Runner) bashPPEmbeddedTarget(typ syntax.BashPPTypeExpr) (target syntax.BashPPTypeExpr, indirect, definedPointer, iface bool) {
	seen := make(map[string]bool)
	for {
		if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
			if indirect {
				return nil, false, true, false
			}
			indirect = true
			typ = pointer.Element
			continue
		}
		if _, ok := typ.(*syntax.BashPPInterfaceType); ok {
			return typ, indirect, false, true
		}
		named, ok := typ.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil {
			return typ, indirect, false, false
		}
		if seen[named.Name.Value] {
			return nil, false, true, false
		}
		seen[named.Name.Value] = true
		decl, found := r.bashPPTypes[named.Name.Value]
		if !found {
			return typ, indirect, false, false
		}
		if !decl.alias {
			switch r.bashPPInstantiateNamedType(named).(type) {
			case *syntax.BashPPPointerType:
				return nil, false, true, false
			case *syntax.BashPPInterfaceType:
				return typ, indirect, false, true
			default:
				return typ, indirect, false, false
			}
		}
		typ = r.bashPPInstantiateNamedType(named)
	}
}

func bashPPNamedOwner(typ syntax.BashPPTypeExpr) (string, syntax.BashPPTypeExpr, bool) {
	if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
		typ = pointer.Element
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return "", nil, false
	}
	return named.Name.Value, typ, true
}

func (r *Runner) bashPPMethodOwner(typ syntax.BashPPTypeExpr) (string, syntax.BashPPTypeExpr, bool) {
	seen := make(map[string]bool)
	for {
		// An alias may name a pointer type (`type P = *T`), in which case
		// the method owner is the pointer's element, as it is for `*T`.
		if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
			typ = pointer.Element
		}
		named, ok := typ.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil {
			return "", nil, false
		}
		if seen[named.Name.Value] {
			return "", nil, false
		}
		seen[named.Name.Value] = true
		decl, found := r.bashPPTypes[named.Name.Value]
		if !found || !decl.alias {
			return named.Name.Value, typ, true
		}
		typ = r.bashPPInstantiateNamedType(named)
	}
}

// bashPPAliasPointerElement reports the element type when a named type is
// an alias — possibly through further aliases — for a pointer type.
func (r *Runner) bashPPAliasPointerElement(typ syntax.BashPPTypeExpr) (syntax.BashPPTypeExpr, bool) {
	seen := make(map[string]bool)
	for {
		named, ok := typ.(*syntax.BashPPNamedType)
		if !ok || named.Name == nil || seen[named.Name.Value] {
			return nil, false
		}
		seen[named.Name.Value] = true
		decl, found := r.bashPPTypes[named.Name.Value]
		if !found || !decl.alias {
			return nil, false
		}
		typ = r.bashPPInstantiateNamedType(named)
		if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
			return pointer.Element, true
		}
	}
}

// bashPPResolveSelection implements breadth-first Go selector lookup, as
// written in the package of the statement being run.
func (r *Runner) bashPPResolveSelection(root syntax.BashPPTypeExpr, name string, methods, addressable bool) bashPPSelection {
	pkg := ""
	if r.bashPPGoSource && methods && goSourceUnexportedName(name) {
		pkg = r.goSourcePackageAt(r.curStmtPos)
	}
	return r.bashPPResolveSelectionIn(root, name, methods, addressable, pkg)
}

// bashPPSelectionCacheKey identifies one memoized selector resolution. The
// root is the canonical identity of the type selected on
// ([Runner.bashPPSelectionRootKey]) rather than the AST node the evaluator
// happened to hand in: a root reached through a value — a pointer cell's
// `*T`, an interface box's dynamic type — is SYNTHESIZED per evaluation, so
// keying on the node re-keyed one static type under a fresh pointer on every
// access and the map grew without bound for the life of the runner. Keyed by
// identity the map is bounded by the program's distinct (type, selector)
// pairs.
type bashPPSelectionCacheKey struct {
	root        string
	name        string
	pkg         string
	methods     bool
	addressable bool
}

// bashPPSelectionRootKey spells the identity of a selector root: a string
// equal for two type expressions exactly when they denote the same type and
// independent of which node spells it. A named type is its name plus, for a
// function-local declaration, the lexical scope of the declaration a
// reference resolves to — the same `name@scope` identity
// bashpp_sprint162_type_scope.go and bashPPUnderlyingTypeKey already use, so
// two functions each declaring `type s struct{…}` stay distinct types.
//
// ok is false for a root whose identity a spelling cannot carry, and such a
// root is resolved without the cache:
//
//   - an anonymous struct, interface or func literal, whose members are the
//     type and which two distinct literals spell alike;
//   - a type parameter, or a named type whose name the executing frame binds
//     to a type argument: its meaning is the frame's, not the program's;
//   - anything else (a collection, a channel, an inferred placeholder), none
//     of which has members for a selector to reach.
func (r *Runner) bashPPSelectionRootKey(typ syntax.BashPPTypeExpr) (string, bool) {
	switch x := typ.(type) {
	case *syntax.BashPPPointerType:
		element, ok := r.bashPPSelectionRootKey(x.Element)
		if !ok {
			return "", false
		}
		return "*" + element, true
	case *syntax.BashPPNamedType:
		if x.Name == nil || r.bashPPTypeParamArgs[x.Name.Value] != nil {
			return "", false
		}
		key := x.Name.Value
		if len(x.TypeArgs) > 0 {
			var args strings.Builder
			for i, arg := range x.TypeArgs {
				if i > 0 {
					args.WriteString(",")
				}
				argKey, ok := r.bashPPSelectionRootKey(arg.ArgType)
				if !ok {
					return "", false
				}
				args.WriteString(argKey)
			}
			key += "[" + args.String() + "]"
		}
		if scope, known := r.goSourceLocalTypeScope(x); known && scope != "" {
			key += "@" + scope
		}
		return key, true
	}
	return "", false
}

// bashPPResolveSelectionIn memoizes bashPPResolveSelectionUncached. Resolution
// reallocates edge slices, ancestor maps and struct-field views on every call
// and its result depends only on the static type/method tables and these
// inputs, so it is cached per Runner. A root without a canonical identity —
// including a nil one, whose type is unknown — is resolved directly. The
// cached bashPPSelection is treated read-only by every caller (edge slices are
// copied before any prepend and only sub-ranged for reads), so sharing the
// value is safe.
func (r *Runner) bashPPResolveSelectionIn(root syntax.BashPPTypeExpr, name string, methods, addressable bool, pkg string) bashPPSelection {
	rootKey, keyed := r.bashPPSelectionRootKey(root)
	if !keyed {
		return r.bashPPResolveSelectionUncached(root, name, methods, addressable, pkg)
	}
	key := bashPPSelectionCacheKey{root: rootKey, name: name, pkg: pkg, methods: methods, addressable: addressable}
	if sel, ok := r.bashPPSelectionCache[key]; ok {
		return sel
	}
	sel := r.bashPPResolveSelectionUncached(root, name, methods, addressable, pkg)
	if r.bashPPSelectionCache == nil {
		r.bashPPSelectionCache = make(map[bashPPSelectionCacheKey]bashPPSelection)
	}
	r.bashPPSelectionCache[key] = sel
	return sel
}

// bashPPInvalidateSelectionCache drops every memoized selector resolution.
// Keyed by type identity, an entry outlives the node a selector spelled it
// with, so it must not outlive the declaration it resolved against: every
// mutation of the runner's named-type or method tables — a Go-form body
// declaring a local type, a frame restoring the entry it shadowed, a method
// declaration — calls this.
func (r *Runner) bashPPInvalidateSelectionCache() {
	r.bashPPSelectionCache = nil
}

// bashPPResolveSelectionUncached is bashPPResolveSelection for a selector
// spelled in a given linked package ("" is the program's own). An unexported
// method name is an identifier of its package: when the shallowest depth holds
// that name from several packages — `struct{ localT; lib.T }` where both
// declare `m()` — only the selecting package's method is the one named,
// and the others do not make the selector ambiguous. If that depth only has
// other packages' private methods, lookup continues: those are different
// identifiers and cannot shadow the requested package's deeper method.
func (r *Runner) bashPPResolveSelectionUncached(root syntax.BashPPTypeExpr, name string, methods, addressable bool, pkg string) bashPPSelection {
	rootPointer := false
	if pointer, ok := root.(*syntax.BashPPPointerType); ok {
		rootPointer, root = true, pointer.Element
	} else if element, ok := r.bashPPAliasPointerElement(root); ok {
		// `type P = *T` is the very type *T: a P root selects through
		// the same implicit indirection, so a pointer-receiver method is
		// in its method set whether or not the P is addressable.
		rootPointer, root = true, element
	}
	// The root's ancestor set ({root}) is only consulted when lookup descends
	// into an embedded field, so it is materialized there rather than on
	// every selector evaluation: a nil ancestors map stands for {root}.
	rootKey, rootKeyed := "", false
	level := []bashPPSelectionNode{{typ: root, indirect: rootPointer}}
	for len(level) > 0 {
		var matches []bashPPSelection
		var next []bashPPSelectionNode
		for _, node := range level {
			if methods {
				if iface, ok := r.bashPPInterfaceType(node.typ); ok {
					set, err := r.bashPPInterfaceMethodSet(bashPPTypeText(node.typ), iface, make(map[string]bool))
					if err == nil {
						if candidate, found := set.byName[bashPPInterfaceMethodKey(name, pkg)]; found {
							matches = append(matches, bashPPSelection{edges: append([]bashPPEmbedEdge(nil), node.edges...), interfaceSpec: candidate.spec})
						}
					}
				}
			}
			fields, _, isStruct := r.bashPPStructFields(node.typ)
			if isStruct {
				for _, field := range fields {
					for _, fieldName := range bashPPDeclaredFieldNames(field) {
						if fieldName != name {
							continue
						}
						edges := append([]bashPPEmbedEdge(nil), node.edges...)
						// A pointer-typed field records its indirection on
						// the edge. Within this selection the flag is inert
						// (every consumer derefs mid-path only), but a caller
						// concatenating per-component selections into one
						// path — s.Subp.SubpSub.test6() — relies on it for
						// the implicit dereference between components.
						_, fieldPointer := field.FieldTypeExpr.(*syntax.BashPPPointerType)
						edges = append(edges, bashPPEmbedEdge{name: fieldName, pointer: fieldPointer})
						matches = append(matches, bashPPSelection{edges: edges, fieldType: field.FieldTypeExpr})
					}
				}
			}
			if methods {
				if owner, receiverType, ok := r.bashPPMethodOwner(node.typ); ok {
					fn := r.bashPPMethods[owner][name]
					if fn != nil && (!fn.decl.Receiver.Pointer || addressable || node.indirect) {
						matches = append(matches, bashPPSelection{edges: append([]bashPPEmbedEdge(nil), node.edges...), method: fn, receiverType: receiverType})
					}
				}
			}
			if !isStruct {
				continue
			}
			for _, field := range fields {
				fieldName, ok := bashPPEmbeddedFieldName(field)
				if !ok {
					continue
				}
				child, pointer, invalid, _ := r.bashPPEmbeddedTarget(field.FieldTypeExpr)
				if invalid || child == nil {
					continue
				}
				childKey := bashPPTypeText(child)
				if node.ancestors == nil && !rootKeyed {
					rootKey, rootKeyed = bashPPTypeText(root), true
				}
				if node.ancestors[childKey] || (node.ancestors == nil && childKey == rootKey) {
					continue
				}
				ancestors := make(map[string]bool, len(node.ancestors)+2)
				if node.ancestors == nil {
					ancestors[rootKey] = true
				}
				for ancestor := range node.ancestors {
					ancestors[ancestor] = true
				}
				ancestors[childKey] = true
				edges := append(append([]bashPPEmbedEdge(nil), node.edges...), bashPPEmbedEdge{name: fieldName, pointer: pointer})
				next = append(next, bashPPSelectionNode{typ: child, edges: edges, indirect: node.indirect || pointer, ancestors: ancestors})
			}
		}
		if len(matches) > 0 && methods && r.bashPPGoSource && goSourceUnexportedName(name) {
			var own []bashPPSelection
			for _, match := range matches {
				if match.method == nil && match.interfaceSpec == nil || r.goSourceMethodPackage(match) == pkg {
					own = append(own, match)
				}
			}
			matches = own
		}
		if len(matches) > 0 {
			if len(matches) != 1 {
				return bashPPSelection{ambiguous: true}
			}
			return matches[0]
		}
		level = next
	}
	return bashPPSelection{}
}

func (r *Runner) bashPPResolveField(typ syntax.BashPPTypeExpr, name string) bashPPSelection {
	return r.bashPPResolveSelection(typ, name, false, false)
}

func bashPPSelectionError(typ syntax.BashPPTypeExpr, name string, sel bashPPSelection) error {
	if sel.ambiguous {
		return fmt.Errorf("BASHPP-ESELECTOR-AMBIGUOUS: ambiguous selector %s.%s", bashPPTypeText(typ), name)
	}
	return fmt.Errorf("BASHPP-ESELECTOR-UNKNOWN: %s has no field or method %q", bashPPTypeText(typ), name)
}

func bashPPDerefEmbedded(value any, meta *bashPPCollectionMeta) (any, *bashPPCollectionMeta, error) {
	if value == nil && meta != nil && meta.kind == "pointer" {
		return nil, nil, errBashPPNilEmbeddedDereference
	}
	pointer, ok := value.(*bashPPPointer)
	if !ok {
		return value, meta, nil
	}
	if pointer == nil {
		return nil, nil, errBashPPNilEmbeddedDereference
	}
	value, meta, _, err := pointer.read()
	return value, meta, err
}

func bashPPReadSelection(value any, meta *bashPPCollectionMeta, edges []bashPPEmbedEdge) (any, *bashPPCollectionMeta, error) {
	for i, edge := range edges {
		var err error
		value, meta, err = bashPPDerefEmbedded(value, meta)
		if err != nil {
			return nil, nil, err
		}
		mapping, ok := value.(map[string]any)
		if !ok || meta == nil || meta.kind != "struct" {
			return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-TYPE: promoted path no longer names struct storage")
		}
		var found bool
		value, found = bashPPStorageGet(mapping, edge.name)
		if !found {
			return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-UNKNOWN: embedded field %q is missing", edge.name)
		}
		meta = bashPPLayoutGet(meta.mapping, edge.name)
		if edge.pointer && i+1 < len(edges) {
			value, meta, err = bashPPDerefEmbedded(value, meta)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return value, meta, nil
}

func bashPPEmbeddedAddress(rootCell *bashPPCell, edges []bashPPEmbedEdge, elem syntax.BashPPTypeExpr) (*bashPPPointer, error) {
	ptr := &bashPPPointer{target: rootCell, elem: elem}
	if rootCell.pointer {
		if rootCell.pointerValue == nil {
			return nil, errBashPPNilDereference
		}
		base := rootCell.pointerValue
		ptr.target = base.target
		ptr.path = append([]bashPPPointerStep(nil), base.path...)
	}
	for i, edge := range edges {
		ptr.path = append(ptr.path, bashPPPointerStep{field: edge.name})
		if edge.pointer && i+1 < len(edges) {
			ptr.path = append(ptr.path, bashPPPointerStep{deref: true})
		}
	}
	return ptr, nil
}

func (r *Runner) bashPPEmbeddedReceiver(rootCell *bashPPCell, sel bashPPSelection) (*bashPPCell, error) {
	if len(sel.edges) == 0 {
		return rootCell, nil
	}
	var value any
	meta := bashPPCellMeta(rootCell)
	if rootCell.pointer {
		if rootCell.pointerValue == nil {
			return nil, errBashPPNilDereference
		}
		var err error
		value, meta, _, err = rootCell.pointerValue.read()
		if err != nil {
			return nil, err
		}
	} else if rootCell.vr.Kind == expand.Object {
		value = rootCell.vr.Obj
	} else {
		value = bashPPScalarValue(rootCell.vr.String())
	}
	value, meta, err := bashPPReadSelection(value, meta, sel.edges)
	if err != nil {
		return nil, err
	}
	cell := &bashPPCell{declType: sel.receiverType, object: rootCell.object}
	if owner, _, ok := bashPPNamedOwner(sel.receiverType); ok {
		cell.typeName = owner
	}
	bashPPStoreCellValue(cell, value, meta)
	if _, pointerType := cell.declType.(*syntax.BashPPPointerType); cell.pointer && !pointerType && cell.declType != nil {
		// The path ended at a pointer field (x.p.M() with p *T): the receiver
		// is that pointer, so it has to carry the pointer type — the method
		// body may store or return it where a *T is required.
		cell.declType = &syntax.BashPPPointerType{Element: cell.declType}
	}
	if cell.vr.Kind == expand.Object && cell.object == nil {
		cell.object = rootCell.object
	}
	return cell, nil
}

func (r *Runner) bashPPBindPromotedMethod(rootCell *bashPPCell, method string, sel bashPPSelection, addressable bool) (*bashPPFunc, bool) {
	if sel.interfaceSpec != nil {
		var value any
		meta := bashPPCellMeta(rootCell)
		var err error
		if rootCell.pointer {
			if rootCell.pointerValue == nil {
				r.bashPPReportNilDereference()
				return nil, false
			}
			value, meta, _, err = rootCell.pointerValue.read()
		} else {
			value = rootCell.vrValue()
		}
		if err != nil {
			r.bashPPReportFault(err)
			return nil, false
		}
		value, meta, err = bashPPReadSelection(value, meta, sel.edges)
		_ = value
		if err != nil {
			r.bashPPReportFault(err)
			return nil, false
		}
		if meta == nil || meta.interfaceValue == nil {
			r.errf("BASHPP-EINTERFACE-VALUE: promoted interface method %s has no interface storage\n", method)
			r.exit.code = 2
			return nil, false
		}
		return r.bashPPBindInterfaceMethod(meta.interfaceValue, method)
	}
	receiver, err := r.bashPPEmbeddedReceiver(rootCell, sel)
	if err != nil {
		r.bashPPReportFault(err)
		return nil, false
	}
	methodOwner := sel.method.decl.Receiver.RecvType.Value
	// The owner test, the copy it makes and the pointer test that shapes the
	// copy's type all answer from the same snapshot.
	receiverView := receiver.view()
	if receiverView.typeName != methodOwner {
		copyCell := *receiverView
		copyCell.typeName = methodOwner
		copyCell.declType = sel.receiverType
		if receiverView.pointer {
			copyCell.declType = &syntax.BashPPPointerType{Element: sel.receiverType}
		}
		receiver = &copyCell
	}
	if sel.method.decl.Receiver.Pointer && !receiver.pointer && (r.bashPPGoSource || len(sel.edges) > 0) {
		ptr, err := bashPPEmbeddedAddress(rootCell, sel.edges, sel.receiverType)
		if err != nil {
			r.bashPPReportFault(err)
			return nil, false
		}
		owner, _, _ := bashPPNamedOwner(sel.receiverType)
		receiver = &bashPPCell{
			vr:           expand.Variable{Set: true, Kind: expand.String},
			declType:     &syntax.BashPPPointerType{Element: sel.receiverType},
			typeName:     owner,
			pointer:      true,
			pointerValue: ptr,
		}
	}
	return r.bashPPBindMethod(receiver, method, addressable)
}
