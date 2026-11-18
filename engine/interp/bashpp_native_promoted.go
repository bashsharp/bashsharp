// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/types"
	"strings"
)

// bashPPResolveNativeEmbedded finds the embedded field path that promotes an
// imported type's method to root. It is the dependency-owned half of
// [Runner.bashPPResolveSelection]: that BFS can only match methods declared in
// this program, so a struct embedding `sync.Mutex` resolves nothing for `Lock`.
//
// Go's depth and ambiguity rules are shared, not re-invented. Levels are walked
// breadth-first; the shallowest level holding any candidate decides, and a
// level is usable only when exactly one candidate sits at it. A local field or
// method of the same name at that level therefore shadows or collides exactly
// as it would in Go, and this helper reports nothing so the interpreted paths
// keep ownership of the diagnostic.
func (r *Runner) bashPPResolveNativeEmbedded(root syntax.BashPPTypeExpr, name string, addressable bool) ([]bashPPEmbedEdge, bool) {
	edges, native, _ := r.bashPPResolveNativeEmbeddedSelection(root, name, addressable)
	return edges, native
}

// bashPPResolveNativeEmbeddedSelection also reports ambiguity for callers
// which must not fall through to the interpreter-only method set after the
// combined selector walk has found multiple candidates at the winning depth.
func (r *Runner) bashPPResolveNativeEmbeddedSelection(root syntax.BashPPTypeExpr, name string, addressable bool) ([]bashPPEmbedEdge, bool, bool) {
	edges, _, ok, ambiguous := r.bashPPResolveNativeMethodCandidate(root, name, addressable)
	return edges, ok, ambiguous
}

// bashPPResolveNativeMethodSpec resolves a method specification from a native type
// or an embedded native field on root.
func (r *Runner) bashPPResolveNativeMethodSpec(root syntax.BashPPTypeExpr, name string, addressable bool) (*syntax.BashPPMethodSpec, bool, bool) {
	_, spec, ok, ambiguous := r.bashPPResolveNativeMethodCandidate(root, name, addressable)
	return spec, ok, ambiguous
}

func (r *Runner) bashPPResolveNativeMethodCandidate(root syntax.BashPPTypeExpr, name string, addressable bool) ([]bashPPEmbedEdge, *syntax.BashPPMethodSpec, bool, bool) {
	if !r.bashPPGoSource || root == nil || name == "" {
		return nil, nil, false, false
	}
	rootPointer := false
	if pointer, ok := root.(*syntax.BashPPPointerType); ok {
		rootPointer, root = true, pointer.Element
	}
	key := bashPPTypeText(root)
	level := []bashPPSelectionNode{{typ: root, indirect: rootPointer, ancestors: map[string]bool{key: true}}}
	type candidate struct {
		edges  []bashPPEmbedEdge
		native bool
		spec   *syntax.BashPPMethodSpec
	}
	pending := map[int][]candidate{}
	for depth := 0; len(level) > 0 || len(pending) > 0; depth++ {
		var next []bashPPSelectionNode
		for _, node := range level {
			if typ := r.bashPPEmbeddedNativeType(node.typ); typ != nil {
				object, index, indirect := types.LookupFieldOrMethod(typ, addressable || node.indirect, nil, name)
				if object != nil || index != nil || indirect {
					offset := len(index) - 1
					if offset < 0 {
						offset = 0
					}
					fn, method := object.(*types.Func)
					var spec *syntax.BashPPMethodSpec
					if method && fn != nil {
						if sig, ok := fn.Type().(*types.Signature); ok {
							spec = bashPPImportedMethodSpec(name, sig)
							if named, ok := node.typ.(*syntax.BashPPNamedType); ok {
								bindings := bashPPImportedInterfaceTypeBindings(typ, named)
								if len(bindings) > 0 {
									spec.Params = bashPPSubstituteFields(spec.Params, bindings)
									spec.Results = bashPPSubstituteFields(spec.Results, bindings)
								}
							}
						}
					}
					pending[depth+offset] = append(pending[depth+offset], candidate{node.edges, method, spec})
				}
				continue
			}
			fields, _, isStruct := r.bashPPStructFields(node.typ)
			if isStruct {
				for _, field := range fields {
					for _, fieldName := range bashPPDeclaredFieldNames(field) {
						if fieldName == name {
							pending[depth] = append(pending[depth], candidate{})
						}
					}
				}
			}
			if owner, _, ok := r.bashPPMethodOwner(node.typ); ok {
				if fn := r.bashPPMethods[owner][name]; fn != nil {
					pending[depth] = append(pending[depth], candidate{})
				}
			}
			if iface, ok := r.bashPPInterfaceType(node.typ); ok {
				set, err := r.bashPPInterfaceMethodSet(bashPPTypeText(node.typ), iface, make(map[string]bool))
				if err == nil {
					found := false
					for _, method := range set.byName {
						if method.name == name {
							found = true
							break
						}
					}
					if found {
						pending[depth] = append(pending[depth], candidate{})
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
				if node.ancestors[childKey] {
					continue
				}
				ancestors := make(map[string]bool, len(node.ancestors)+1)
				for ancestor := range node.ancestors {
					ancestors[ancestor] = true
				}
				ancestors[childKey] = true
				edges := append(append([]bashPPEmbedEdge(nil), node.edges...), bashPPEmbedEdge{name: fieldName, pointer: pointer})
				next = append(next, bashPPSelectionNode{typ: child, edges: edges, indirect: node.indirect || pointer, ancestors: ancestors})
			}
		}
		if matches := pending[depth]; len(matches) > 0 {
			if len(matches) != 1 {
				for _, match := range matches {
					if match.native {
						return nil, nil, false, true
					}
				}
				// An interpreter-only level is resolved by the package-aware
				// selector walk, particularly for unexported promoted methods.
				return nil, nil, false, false
			}
			if !matches[0].native {
				return nil, nil, false, false
			}
			return matches[0].edges, matches[0].spec, true, false
		}
		delete(pending, depth)
		level = next
	}
	return nil, nil, false, false
}

// bashPPPromotedNativeReceiver reads the embedded dependency-owned receiver
// that carries method, for a receiver expression that is itself a local
// identifier or field chain. The embedded field is read out of the root's live
// storage rather than reconstructed, so the returned handle is the same value
// the explicit `x.Mutex.Lock()` spelling would reach: pointer identity, and
// with it lock state, is preserved.
//
// Only identifiers and field selectors are walked, so this evaluates no index,
// call or user expression and cannot repeat an operand's effects — the caller
// may use it as a predicate and then again to bind, as the direct native paths
// already do with [Runner.bashPPNativeLocalField].
func (r *Runner) bashPPPromotedNativeReceiver(expr syntax.BashPPExpr, method string) *bashPPBridgeValue {
	if !r.bashPPGoSource || method == "" {
		return nil
	}
	if paren, ok := expr.(*syntax.BashPPParenExpr); ok {
		return r.bashPPPromotedNativeReceiver(paren.X, method)
	}
	value, meta, ok := r.bashPPNativeLocalBase(expr)
	if !ok || meta == nil || meta.kind != "struct" {
		return nil
	}
	if _, already := value.(*bashPPBridgeValue); already {
		// A wholly dependency-owned receiver is the direct path's business.
		return nil
	}
	edges, ok := r.bashPPResolveNativeEmbedded(meta.typ, method, true)
	if !ok || len(edges) == 0 {
		return nil
	}
	embedded, _, err := bashPPReadSelection(value, meta, edges)
	if err != nil {
		return nil
	}
	// new(imported.Type) may carry an interpreter pointer to the authentic
	// native value cell. Follow that pointer without reconstructing its target.
	if pointer, ok := embedded.(*bashPPPointer); ok {
		if pointer == nil {
			return nil
		}
		embedded, _, _, err = pointer.read()
		if err != nil {
			return nil
		}
	}
	native, _ := embedded.(*bashPPBridgeValue)
	return native
}

// bashPPPromotedNativeCellReceiver is the interface-dispatch counterpart of
// bashPPPromotedNativeReceiver. Interface values no longer have the source
// expression used to find their storage, but their dynamic type still owns
// selector resolution. Resolve that type with the same combined breadth-first
// walk, then read the selected dependency receiver from the saved dynamic
// value. A deeper original method or an ambiguous shallow level therefore
// prevents native dispatch here just as it does for a direct selector.
func (r *Runner) bashPPPromotedNativeCellReceiver(cell *bashPPCell, dynamic syntax.BashPPTypeExpr, method string) (*bashPPBridgeValue, bool) {
	if !r.bashPPGoSource || cell == nil || dynamic == nil || method == "" {
		return nil, false
	}
	edges, ok, ambiguous := r.bashPPResolveNativeEmbeddedSelection(dynamic, method, false)
	if ambiguous {
		return nil, true
	}
	if !ok || len(edges) == 0 {
		return nil, false
	}
	var value any
	meta := bashPPCellMeta(cell)
	var err error
	if cell.pointer {
		if cell.pointerValue == nil {
			return nil, false
		}
		value, meta, _, err = cell.pointerValue.read()
	} else {
		value = cell.vrValue()
	}
	if err != nil {
		return nil, false
	}
	value, _, err = bashPPReadSelection(value, meta, edges)
	if err != nil {
		return nil, false
	}
	if pointer, ok := value.(*bashPPPointer); ok {
		if pointer == nil {
			return nil, false
		}
		value, _, _, err = pointer.read()
		if err != nil {
			return nil, false
		}
	}
	native, _ := value.(*bashPPBridgeValue)
	return native, false
}

// The name is resolved through this Runner's accepted import binding. The type
// object came from the reviewed SDK export importer, not a display spelling or
// user-supplied method-name list. Lookup includes native internal embedding.
func (r *Runner) bashPPEmbeddedNativeType(typ syntax.BashPPTypeExpr) types.Type {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return nil
	}
	alias, name, ok := strings.Cut(named.Name.Value, ".")
	if !ok {
		return nil
	}
	path := r.bashPPImports[alias]
	if path == "" {
		return nil
	}
	return r.bashPPTools.nativeTypes[path+"."+name]
}
