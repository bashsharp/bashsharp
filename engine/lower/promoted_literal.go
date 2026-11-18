package lower

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type promotedField struct {
	name     string
	typ      syntax.BashPPTypeExpr
	embedded bool
}
type promotedEdge struct {
	name    string
	typ     syntax.BashPPTypeExpr
	pointer bool
}
type promotedSelection struct {
	path []promotedEdge
	typ  syntax.BashPPTypeExpr
}

// promotedCompositeExpr is a dispatcher seam for keyed, known struct literals.
// It never mutates source nodes. Typed temporaries preserve source evaluation
// order before fields are regrouped into native nested embedded literals.
func (e *emitter) promotedCompositeExpr(lit *syntax.BashPPCompositeLit, declaredTypes map[string]*syntax.BashPPDecl) (string, bool, error) {
	if lit == nil || lit.LitType == nil {
		return "", false, nil
	}
	if _, ok := promotedFields(lit.LitType, declaredTypes, map[string]bool{}); !ok {
		return "", false, nil
	}
	keyed, positional := false, false
	for _, elem := range lit.Elems {
		if elem == nil || elem.Value == nil {
			return "", true, e.fail(lit, CodeExpr, "missing composite element")
		}
		keyed = keyed || elem.Key != nil
		positional = positional || elem.Key == nil
	}
	if !keyed {
		return "", false, nil
	}
	typ, err := e.typeExpr(lit.LitType)
	if err != nil {
		return "", true, err
	}
	// fail reports a keyed-literal rejection with the public source rendering.
	// The source engine writes every one of these through r.bashErrPrefix (see
	// bashPPStructLiteral in interp/bashpp_struct.go), so their public bytes
	// carry an `<origin>: line N: ` prefix; failMixed is the one rejection
	// there that is deliberately written without it. Recording the rendering
	// per diagnostic in Text keeps that split where the source engine puts it,
	// rather than making it a formatter-wide rule a consumer would have to
	// re-derive. Code, Msg and Pos stay populated as before.
	fail := func(node syntax.Node, code, message string) (string, bool, error) {
		return "", true, e.promotedFail(node, code, message, true)
	}
	failMixed := func(node syntax.Node, code, message string) (string, bool, error) {
		return "", true, e.promotedFail(node, code, message, false)
	}
	if positional {
		return failMixed(lit, "MIXED", typ+" literal cannot mix keyed and positional fields")
	}
	seen := map[string]bool{}
	selections := make([]promotedSelection, len(lit.Elems))
	for i, elem := range lit.Elems {
		key, ok := elem.Key.(*syntax.BashPPIdent)
		if !ok || key.Name == nil {
			return fail(elem.Key, "KEY", typ+" literal field key must be an identifier, not a selector expression")
		}
		name := key.Name.Value
		selection, ambiguous := e.promotedSelect(lit.LitType, name, declaredTypes)
		if ambiguous {
			return fail(key, "AMBIGUOUS", fmt.Sprintf("field selector %s.%s is ambiguous", typ, name))
		}
		if len(selection.path) == 0 {
			return fail(key, "UNKNOWN", fmt.Sprintf("%s has no field selector %q", typ, name))
		}
		if seen[name] {
			return fail(key, "DUPLICATE", fmt.Sprintf("field selector %q is supplied more than once", name))
		}
		seen[name] = true
		for _, edge := range selection.path[:len(selection.path)-1] {
			if edge.pointer {
				return fail(key, "KEY-POINTER", fmt.Sprintf("field selector %s.%s traverses pointer field %s", typ, name, edge.name))
			}
		}
		for _, previous := range selections[:i] {
			if promotedPathConflict(previous.path, selection.path) {
				return fail(key, "KEY-CONFLICT", fmt.Sprintf("field selector %s.%s conflicts with key %s", typ, name, promotedPath(previous.path)))
			}
		}
		selections[i] = selection
	}
	type entry struct {
		name, value string
		typ         syntax.BashPPTypeExpr
		children    []*entry
	}
	root := &entry{typ: lit.LitType}
	var setup strings.Builder
	for i, elem := range lit.Elems {
		selection := selections[i]
		fieldType, err := e.typeExpr(selection.typ)
		if err != nil {
			return "", true, err
		}
		value := ""
		if child, ok := elem.Value.(*syntax.BashPPCompositeLit); ok {
			copy := *child
			if copy.LitType == nil {
				copy.LitType = selection.typ
			}
			var handled bool
			value, handled, err = e.promotedCompositeExpr(&copy, declaredTypes)
			if !handled && err == nil {
				value, err = e.compositeExpr(&copy)
			}
		} else {
			value, err = e.compositeValue(elem.Value)
		}
		if err != nil {
			return "", true, err
		}
		temp := fmt.Sprintf("%spromotedValue%d", e.prefix, i)
		fmt.Fprintf(&setup, "var %s %s = %s\n", temp, fieldType, value)
		current := root
		for j, edge := range selection.path {
			var next *entry
			for _, candidate := range current.children {
				if candidate.name == edge.name {
					next = candidate
					break
				}
			}
			if next == nil {
				next = &entry{name: edge.name, typ: edge.typ}
				current.children = append(current.children, next)
			}
			if j == len(selection.path)-1 {
				next.value = temp
			}
			current = next
		}
	}
	var render func(*entry) (string, error)
	render = func(node *entry) (string, error) {
		if node.value != "" {
			return node.value, nil
		}
		name, err := e.typeExpr(node.typ)
		if err != nil {
			return "", err
		}
		var fields []string
		for _, child := range node.children {
			value, err := render(child)
			if err != nil {
				return "", err
			}
			fields = append(fields, child.name+": "+value)
		}
		return name + "{" + strings.Join(fields, ", ") + "}", nil
	}
	result, err := render(root)
	if err != nil {
		return "", true, err
	}
	return "func() " + typ + " {\n" + setup.String() + "return " + result + "\n}()", true, nil
}

// promotedFail builds the keyed-literal diagnostic. prefixed selects the
// `<origin>: line N: ` rendering the source engine's bashErrPrefix produces,
// which names an unnamed script "bash" exactly as the interpreter does.
func (e *emitter) promotedFail(node syntax.Node, code, message string, prefixed bool) error {
	code = "BASHPP-ESTRUCT-" + code
	err := e.fail(node, code, message)
	list, ok := err.(ErrorList)
	if !prefixed || !ok || len(list) == 0 {
		return err
	}
	origin := e.options.Origin
	if origin == "" {
		origin = "bash"
	}
	list[0].Text = fmt.Sprintf("%s: line %d: %s: %s", origin, list[0].Pos.Line(), code, message)
	return list
}

func promotedPath(path []promotedEdge) string {
	names := make([]string, len(path))
	for i, edge := range path {
		names[i] = edge.name
	}
	return strings.Join(names, ".")
}
func promotedPathConflict(a, b []promotedEdge) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i].name != b[i].name {
			return false
		}
	}
	return true
}

func (e *emitter) promotedSelect(root syntax.BashPPTypeExpr, name string, declared map[string]*syntax.BashPPDecl) (promotedSelection, bool) {
	type node struct {
		typ       syntax.BashPPTypeExpr
		path      []promotedEdge
		ancestors map[string]bool
	}
	level := []node{{typ: root, ancestors: map[string]bool{}}}
	for len(level) > 0 {
		var matches []promotedSelection
		var next []node
		for _, parent := range level {
			fields, ok := promotedFields(parent.typ, declared, map[string]bool{})
			if !ok {
				continue
			}
			for _, field := range fields {
				if field.name == name {
					path := append(append([]promotedEdge(nil), parent.path...), promotedEdge{name: field.name, typ: field.typ})
					matches = append(matches, promotedSelection{path: path, typ: field.typ})
				}
				if !field.embedded {
					continue
				}
				child, pointer := promotedTarget(field.typ, declared, map[string]bool{})
				if child == nil {
					continue
				}
				spelling, err := e.typeExpr(child)
				if err != nil || parent.ancestors[spelling] {
					continue
				}
				ancestors := map[string]bool{}
				for key, value := range parent.ancestors {
					ancestors[key] = value
				}
				ancestors[spelling] = true
				path := append(append([]promotedEdge(nil), parent.path...), promotedEdge{name: field.name, typ: field.typ, pointer: pointer})
				next = append(next, node{typ: child, path: path, ancestors: ancestors})
			}
		}
		if len(matches) > 0 {
			return matches[0], len(matches) > 1
		}
		level = next
	}
	return promotedSelection{}, false
}
func promotedTarget(typ syntax.BashPPTypeExpr, declared map[string]*syntax.BashPPDecl, seen map[string]bool) (syntax.BashPPTypeExpr, bool) {
	if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
		return pointer.Element, true
	}
	if named, ok := typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
		declaration := declared[named.Name.Value]
		if declaration != nil && declaration.Alias && !seen[named.Name.Value] {
			seen[named.Name.Value] = true
			return promotedTarget(promotedInstantiate(declaration, named), declared, seen)
		}
	}
	return typ, false
}
func promotedFields(typ syntax.BashPPTypeExpr, declared map[string]*syntax.BashPPDecl, seen map[string]bool) ([]promotedField, bool) {
	switch typ := typ.(type) {
	case *syntax.BashPPNamedType:
		if typ.Name == nil || seen[typ.Name.Value] {
			return nil, false
		}
		seen[typ.Name.Value] = true
		declaration := declared[typ.Name.Value]
		if declaration == nil {
			return nil, false
		}
		return promotedFields(promotedInstantiate(declaration, typ), declared, seen)
	case *syntax.BashPPStructType:
		var result []promotedField
		for _, field := range typ.Fields {
			if field == nil || field.FieldTypeExpr == nil {
				return nil, false
			}
			if field.Embedded {
				target := field.FieldTypeExpr
				if pointer, ok := target.(*syntax.BashPPPointerType); ok {
					target = pointer.Element
				}
				named, ok := target.(*syntax.BashPPNamedType)
				if !ok || named.Name == nil {
					return nil, false
				}
				result = append(result, promotedField{name: named.Name.Value, typ: field.FieldTypeExpr, embedded: true})
			} else {
				for _, name := range field.Names {
					result = append(result, promotedField{name: name.Value, typ: field.FieldTypeExpr})
				}
			}
		}
		return result, true
	}
	return nil, false
}
func promotedInstantiate(declaration *syntax.BashPPDecl, named *syntax.BashPPNamedType) syntax.BashPPTypeExpr {
	substitutions := map[string]syntax.BashPPTypeExpr{}
	index := 0
	for _, param := range declaration.TypeParams {
		for _, name := range param.Names {
			if index < len(named.TypeArgs) && named.TypeArgs[index] != nil {
				substitutions[name.Value] = named.TypeArgs[index].ArgType
			}
			index++
		}
	}
	typ := declaration.DeclTypeExpr
	if typ == nil && declaration.DeclType != nil && declaration.DeclType.Value == "struct" {
		typ = &syntax.BashPPStructType{Struct: declaration.DeclType, Fields: declaration.StructFields, Lbrace: declaration.Lbrace, Rbrace: declaration.Rbrace}
	}
	return promotedSubstitute(typ, substitutions)
}
func promotedSubstitute(typ syntax.BashPPTypeExpr, substitutions map[string]syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	fields := func(original []*syntax.BashPPField) []*syntax.BashPPField {
		copy := make([]*syntax.BashPPField, len(original))
		for i, field := range original {
			if field == nil {
				continue
			}
			value := *field
			value.FieldTypeExpr = promotedSubstitute(field.FieldTypeExpr, substitutions)
			copy[i] = &value
		}
		return copy
	}
	switch typ := typ.(type) {
	case *syntax.BashPPTypeParamType:
		if value := substitutions[typ.Name.Value]; value != nil {
			return value
		}
	case *syntax.BashPPNamedType:
		if value := substitutions[typ.Name.Value]; value != nil && len(typ.TypeArgs) == 0 {
			return value
		}
		copy := *typ
		copy.TypeArgs = make([]*syntax.BashPPTypeArg, len(typ.TypeArgs))
		for i, arg := range typ.TypeArgs {
			if arg == nil {
				continue
			}
			value := *arg
			value.ArgType = promotedSubstitute(arg.ArgType, substitutions)
			copy.TypeArgs[i] = &value
		}
		return &copy
	case *syntax.BashPPPointerType:
		copy := *typ
		copy.Element = promotedSubstitute(typ.Element, substitutions)
		return &copy
	case *syntax.BashPPCollectionType:
		copy := *typ
		copy.Key = promotedSubstitute(typ.Key, substitutions)
		copy.Element = promotedSubstitute(typ.Element, substitutions)
		return &copy
	case *syntax.BashPPStructType:
		copy := *typ
		copy.Fields = fields(typ.Fields)
		return &copy
	case *syntax.BashPPFuncType:
		copy := *typ
		copy.Params = fields(typ.Params)
		copy.Results = fields(typ.Results)
		return &copy
	}
	return typ
}
