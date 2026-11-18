package lower

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) selectorProjection(n *syntax.BashPPSelectorExpr) projection {
	if n.Sel == nil {
		return scalarProjection()
	}
	base := e.projectionExpr(n.X)
	return e.fieldProjection(strings.TrimPrefix(base.sourceType, "*"), n.Sel.Value, map[string]bool{})
}
func (e *emitter) fieldProjection(typ, name string, seen map[string]bool) projection {
	if seen[typ] {
		return scalarProjection()
	}
	seen[typ] = true
	declaration := e.declaredTypes[typ]
	if declaration == nil {
		return scalarProjection()
	}
	fields := declaration.StructFields
	if structure, ok := declaration.DeclTypeExpr.(*syntax.BashPPStructType); ok {
		fields = structure.Fields
	}
	for _, field := range fields {
		for _, fieldName := range field.Names {
			if fieldName.Value == name {
				if text, err := e.fieldType(field); err == nil {
					return e.projectionType(text, nil)
				}
			}
		}
	}
	for _, field := range fields {
		if field.Embedded {
			if text, err := e.fieldType(field); err == nil {
				if nested := e.fieldProjection(strings.TrimPrefix(text, "*"), name, seen); nested.sourceType != "" {
					return nested
				}
			}
		}
	}
	if len(fields) == 0 {
		if declaration.DeclTypeExpr != nil {
			if text, err := e.typeExpr(declaration.DeclTypeExpr); err == nil && text != typ {
				return e.fieldProjection(text, name, seen)
			}
		}
		if declaration.DeclType != nil && declaration.DeclType.Value != typ {
			return e.fieldProjection(declaration.DeclType.Value, name, seen)
		}
	}
	return scalarProjection()
}

// importedQualifier distinguishes an import from storage shadowing its name.
// Imports are registered in the outer script scope; function scopes instead
// start fresh and contain only their own parameters and local declarations.
func (e *emitter) importedQualifier(name string) bool {
	if e.imports[name] == "" {
		return false
	}
	if e.inFunc {
		return !e.bound(name)
	}
	for i := 1; i < len(e.scopes); i++ {
		if e.scopes[i][name] {
			return false
		}
	}
	return !e.globals[name] && !e.typeNames[name] && !e.funcs[name]
}

func (e *emitter) checkedSelector(n *syntax.BashPPSelectorExpr) (string, error) {
	if n.Sel != nil {
		if id, ok := n.X.(*syntax.BashPPIdent); ok && e.importedQualifier(id.Name.Value) {
			return id.Name.Value + "." + n.Sel.Value, nil
		}
	}
	base, err := e.expr(n.X)
	if err != nil {
		return "", err
	}
	if n.Sel == nil {
		return "", e.fail(n, CodeExpr, "missing selector name")
	}
	if e.execution && e.selectorProjection(n).sourceType == "" {
		typ := e.projectionExpr(n.X).sourceType
		if e.methodDeclaration(typ, n.Sel.Value) != nil {
			return e.runtimeMethodHandle(n, base, typ, n.Sel.Value)
		}
		if iface, ok := e.interfaceDecl(typ); ok {
			_, _, found, err := e.interfaceMethodTypes(iface, n.Sel.Value, map[*syntax.BashPPInterfaceType]bool{})
			if err != nil {
				return "", err
			}
			if found {
				return e.runtimeMethodHandle(n, base, typ, n.Sel.Value)
			}
		}
		// A method the receiver reaches only through an embedded interface is
		// neither declared on it nor named by it, so neither branch above sees
		// it, yet it is still a handle carrying the private ABI rather than the
		// public method value this selector would otherwise fall through to.
		promoted, err := e.promotedInterfaceSource(n, typ, n.Sel.Value)
		if err != nil {
			return "", err
		}
		if promoted != nil {
			return e.runtimeMethodHandle(n, base, typ, n.Sel.Value)
		}
	}
	if e.goSource {
		// Go selects through a pointer itself; the explicit dereference is
		// the runtime path's nil check and the parentheses its spelling.
		return base + "." + e.faultTokenDirective(n.X, n.Sel.Pos()) + n.Sel.Value, nil
	}
	if info := e.projectionExpr(n.X); info.kind == projectPointer {
		base = e.checkedDeref(n, base, "")
	}
	return "(" + base + ")." + n.Sel.Value, nil
}

func (e *emitter) checkedShortDeclaration(n *syntax.BashPPShortDecl, rhs string) (string, bool, error) {
	// The TryValue binding turns a nil dereference into the runtime's value
	// diagnostic; Go source keeps Go's own nil-pointer panic.
	if e.goSource || len(n.Lhs) != 1 || n.Expr == nil || n.Lhs[0].Value == "_" || e.scopes[len(e.scopes)-1][n.Lhs[0].Value] {
		return "", false, nil
	}
	risky := false
	syntax.Walk(n.Expr, func(node syntax.Node) bool {
		switch x := node.(type) {
		case *syntax.BashPPDerefExpr:
			risky = true
		case *syntax.BashPPSelectorExpr:
			risky = risky || e.projectionExpr(x.X).kind == projectPointer
		}
		return true
	})
	if !risky {
		return "", false, nil
	}
	info := e.projectionExpr(n.Expr)
	if info.sourceType == "" {
		return "", false, nil
	}
	name := n.Lhs[0].Value
	failure := e.prefix + "bindingError" + strings.ReplaceAll(n.Pos().String(), ":", "_")
	info.present = failure + " == nil"
	if !e.inFunc {
		if e.globalChecked == nil {
			e.globalChecked = map[*syntax.BashPPShortDecl]string{}
		}
		e.globalChecked[n] = failure
	}
	e.bind(name)
	e.projections.projectionBind(name, info)
	text := name + ", " + failure + " := " + e.prefix + "rt.TryValue(func() " + info.sourceType + " { return " + rhs + " })\nif " + failure + " != nil {" + e.operationFailure(failure) + "}" + e.unused([]string{name})
	return text, true, nil
}
