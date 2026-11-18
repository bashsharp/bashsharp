package lower

import (
	"bytes"
	"go/ast"
	"go/parser"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) guardedAssignment(n *syntax.BashPPAssign) (string, bool, error) {
	c := ReadonlyContext{State: e.program() + ".Readonly"}
	if n.TargetExpr == nil && n.Target != nil && n.ValueExpr == nil && n.Value != nil && syntax.BashPPValidIdent(n.Target.Lit()) {
		var text bytes.Buffer
		if err := syntax.NewPrinter().Print(&text, n.Value); err != nil {
			return "", true, err
		}
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("__value := "+text.String()+"\n"), "")
		if err != nil {
			return "", true, e.fail(n.Value, CodeExpr, "invalid committed assignment value")
		}
		if len(file.Stmts) == 1 {
			if declaration, ok := file.Stmts[0].Cmd.(*syntax.BashPPShortDecl); ok && declaration.Expr != nil {
				if _, ok := declaration.Expr.(*syntax.BashPPCompositeLit); ok {
					value, err := e.expr(declaration.Expr)
					if err != nil {
						return "", true, err
					}
					result, err := e.readonlyRebind(n, c, n.Target.Lit(), value)
					return result, true, err
				}
			}
		}
	}
	if n.TargetExpr == nil && n.Target != nil && n.ValueExpr != nil && syntax.BashPPValidIdent(n.Target.Lit()) {
		value, err := e.expr(n.ValueExpr)
		if err != nil {
			return "", true, err
		}
		text, err := e.readonlyRebind(n, c, n.Target.Lit(), value)
		return text, true, err
	}
	if n.TargetExpr != nil && n.ValueExpr != nil {
		value, err := e.expr(n.ValueExpr)
		if err != nil {
			return "", true, err
		}
		switch target := n.TargetExpr.(type) {
		case *syntax.BashPPDerefExpr:
			pointer, err := e.expr(target.X)
			if err != nil {
				return "", true, err
			}
			rt := e.prefix + "rt."
			return rt + "AssignReadonlyPointer(" + c.State + ", " + rt + "MustValue(" + rt + "CheckedPointer(" + pointer + ", " + e.checkedValueSite(target, "") + ")), " + value + ")", true, nil
		case *syntax.BashPPIdent:
			text, err := e.readonlyRebind(n, c, target.Name.Value, value)
			return text, true, err
		default:
			kind := "field"
			if index, ok := target.(*syntax.BashPPIndexExpr); ok {
				kind = "slice"
				if strings.HasPrefix(e.projectionExpr(index.X).sourceType, "map[") {
					kind = "map"
				}
			}
			text, err := e.readonlyMutation(target, c, ReadonlyMutation{Kind: kind, Value: value})
			return text, true, err
		}
	}
	if len(n.Names) > 0 {
		var guard strings.Builder
		for _, name := range n.Names {
			guard.WriteString(e.readonlyGuard(c.State + ".CheckAssign(&" + e.goName(name.Value) + ")"))
		}
		previous := e.readonly
		e.readonly = false
		text, err := e.command(n)
		e.readonly = previous
		return guard.String() + text, true, err
	}
	return "", false, nil
}

func (e *emitter) guardedBuiltin(c *syntax.BashPPCall) (string, bool, error) {
	if len(c.Fun) != 1 || e.funcs[c.Fun[0].Value] || len(c.Args) == 0 {
		return "", false, nil
	}
	name := c.Fun[0].Value
	switch name {
	case "append", "clear", "copy", "delete":
	default:
		return "", false, nil
	}
	var args []string
	for i := range c.Args {
		value, err := e.callArgument(c, i)
		if err != nil {
			return "", true, err
		}
		args = append(args, value)
	}
	expr, err := parser.ParseExpr(args[0])
	if err != nil {
		return "", true, e.fail(c, CodeUnsupported, "readonly builtin target is not a committed native path")
	}
	for {
		switch n := expr.(type) {
		case *ast.Ident:
			root := n.Name
			setup, value, err := e.readonlyBuiltin(c, ReadonlyContext{State: e.program() + ".Readonly"}, ReadonlyBuiltin{Name: name, Root: root, Target: args[0], Args: args[1:], Spread: c.Ellipsis.IsValid()})
			if err != nil {
				return "", true, err
			}
			result, ret := "", ""
			if name == "copy" {
				result = " int"
				ret = "return "
			}
			if name == "append" {
				projection, _ := e.projections.projectionLookup(root)
				if projection.sourceType == "" {
					return "", true, e.fail(c, CodeUnsupported, "append result needs the native collection type")
				}
				result = " " + projection.sourceType
				ret = "return "
			}
			return "func()" + result + "{\n" + setup + ret + value + "\n}()", true, nil
		case *ast.ParenExpr:
			expr = n.X
		case *ast.SelectorExpr:
			expr = n.X
		case *ast.IndexExpr:
			expr = n.X
		default:
			return "", true, e.fail(c, CodeUnsupported, "readonly builtin target requires a rooted native binding")
		}
	}
}
