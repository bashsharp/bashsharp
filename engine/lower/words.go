package lower

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Word-backed arguments occur only after the parser has selected a typed call
// or return. This decodes that expression; it never chooses a source dialect.
func (e *emitter) wordSequence(ws []*syntax.Word) (string, error) {
	if len(ws) == 0 {
		return "", e.fail(nil, CodeExpr, "missing value")
	}
	if len(ws) == 1 {
		return e.valueWord(ws[0])
	}
	var texts []string
	for _, w := range ws {
		if len(w.Parts) != 1 {
			return "", e.fail(w, CodeExpr, "multiword typed expression contains expansion")
		}
		l, ok := w.Parts[0].(*syntax.Lit)
		if !ok {
			return "", e.fail(w, CodeExpr, "multiword typed expression contains quoting")
		}
		texts = append(texts, l.Value)
	}
	return e.nativeWordExpr(ws[0], strings.Join(texts, " "))
}
func (e *emitter) valueWord(w *syntax.Word) (string, error) {
	if w == nil {
		return "", e.fail(nil, CodeExpr, "missing value word")
	}
	if len(w.Parts) != 1 {
		return e.shellWord(w)
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return e.nativeWordExpr(w, p.Value)
	case *syntax.SglQuoted:
		if p.Dollar {
			return "", e.fail(w, CodeUnsupported, "ANSI-C quoting")
		}
		return strconv.Quote(p.Value), nil
	case *syntax.DblQuoted:
		return e.quoted(p)
	case *syntax.ArithmExp:
		return e.arithmetic(p.X)
	case *syntax.ParamExp:
		return e.parameter(p)
	default:
		return "", e.fail(w, CodeExpr, "unsupported typed value word")
	}
}
func (e *emitter) nativeWordExpr(n syntax.Node, text string) (string, error) {
	x, err := parser.ParseExpr(text)
	if err != nil {
		return "", e.fail(n, CodeExpr, "invalid committed expression: "+text)
	}
	if e.goSource {
		// This fragment came from a Go-typechecked AST compatibility field.
		// Retain its complete Go expression grammar; generated-code checking
		// below Compile remains authoritative for emitted names and types.
		var stack []ast.Node
		ast.Inspect(x, func(node ast.Node) bool {
			if node == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if id, ok := node.(*ast.Ident); ok && id.Name == "main" {
				selectorField := false
				if len(stack) > 0 {
					if parent, ok := stack[len(stack)-1].(*ast.SelectorExpr); ok && parent.Sel == id {
						selectorField = true
					}
				}
				if !selectorField {
					id.Name = e.goName(id.Name)
				}
			}
			stack = append(stack, node)
			return true
		})
		var out bytes.Buffer
		if err := format.Node(&out, token.NewFileSet(), x); err != nil {
			return "", e.fail(n, CodeExpr, err.Error())
		}
		return out.String(), nil
	}
	var problem error
	ast.Inspect(x, func(a ast.Node) bool {
		if a == nil || problem != nil {
			return false
		}
		switch q := a.(type) {
		case *ast.Ident:
			if !e.known(q.Name) && !scalarType(q.Name) && q.Name != "print" && q.Name != "println" {
				problem = e.fail(n, CodeUndefined, "undefined: "+q.Name)
				return false
			}
		case *ast.BasicLit:
			if q.Kind != token.INT && q.Kind != token.FLOAT && q.Kind != token.STRING && q.Kind != token.CHAR {
				problem = e.fail(n, CodeUnsupported, "literal kind "+q.Kind.String())
			}
		case *ast.ParenExpr, *ast.BinaryExpr, *ast.StarExpr, *ast.IndexExpr:
		case *ast.UnaryExpr:
			if q.Op != token.ADD && q.Op != token.SUB && q.Op != token.NOT && q.Op != token.XOR {
				problem = e.fail(n, CodeUnsupported, "unary word expression operator "+q.Op.String())
			}
		case *ast.CallExpr:
			if id, ok := q.Fun.(*ast.Ident); !ok || (!e.known(id.Name) && !scalarType(id.Name)) {
				problem = e.fail(n, CodeUnsupported, "word call requires resolved native function")
			}
		default:
			problem = e.fail(n, CodeUnsupported, fmt.Sprintf("committed word expression %T", a))
		}
		return problem == nil
	})
	if problem != nil {
		return "", problem
	}
	ast.Inspect(x, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && e.execution {
			if id, ok := call.Fun.(*ast.Ident); ok && (e.funcs[id.Name] || e.bound(id.Name)) {
				program, _ := parser.ParseExpr(e.program())
				site, _ := parser.ParseExpr(e.callSite(n, id.Name))
				call.Args = append([]ast.Expr{program, site}, call.Args...)
			}
		}
		if id, ok := node.(*ast.Ident); ok {
			id.Name = e.goName(id.Name)
		}
		return true
	})
	var out bytes.Buffer
	if err := format.Node(&out, token.NewFileSet(), x); err != nil {
		return "", e.fail(n, CodeExpr, err.Error())
	}
	return out.String(), nil
}

// Shell lookup observes declarations reached by the script, even though native
// declarations are collected early for type checking.
func (e *emitter) shellKnown(name string) bool {
	if !e.inFunc && e.globals[name] && !e.visibleGlobals[name] {
		for _, scope := range e.scopes {
			if scope[name] {
				return true
			}
		}
		return false
	}
	return e.known(name)
}
func (e *emitter) parameter(p *syntax.ParamExp) (string, error) {
	if value, handled, err := e.positionalParameter(p); handled || err != nil {
		return value, err
	}
	if p.BadSubst != nil {
		e.bridge = true
		return e.prefix + "rt.BadSubstitutionValue()", nil
	}
	if p.Param != nil && strings.Contains(p.Param.Value, ".") && p.Index == nil && p.Exp == nil && !p.Excl && !p.Length {
		value, handled, err := e.shellProjection(&syntax.Word{Parts: []syntax.WordPart{p.Param}})
		if handled || err != nil {
			return value, err
		}
	}
	if p.Param != nil && p.Exp != nil && p.Exp.Op == syntax.DefaultUnset && p.Index == nil && !p.Length && !p.Excl {
		if e.shellKnown(p.Param.Value) {
			return p.Param.Value, nil
		}
		fallback, err := e.stringParts(p.Exp.Word.Parts)
		if err != nil {
			return "", err
		}
		if e.mixedShell {
			return e.program() + ".ShellDefault(" + strconv.Quote(p.Param.Value) + "," + fallback + ")", nil
		}
		return fallback, nil
	}
	// ${name:+word} over a known typed binding: the binding's established
	// projection decides null-ness, so a foreign error binding selects its
	// alternate exactly when the interpreter's cell text is non-empty.
	if p.Param != nil && p.Exp != nil && p.Exp.Op == syntax.AlternateUnsetOrNull && p.Index == nil && !p.Length && !p.Excl && e.shellKnown(p.Param.Value) {
		value, err := e.projectBinding(p, p.Param.Value, p.Param.Value)
		if err != nil {
			return "", err
		}
		if e.execution {
			value = e.program() + ".ShellBinding(" + strconv.Quote(p.Param.Value) + ",func() string {return " + e.prefix + "rt.Word(" + value + ")})"
		}
		alternate := `""`
		if p.Exp.Word != nil {
			alternate, err = e.stringParts(p.Exp.Word.Parts)
			if err != nil {
				return "", err
			}
		}
		e.bridge = true
		return e.prefix + "rt.BindingValue(" + e.prefix + "rt.Word(" + value + `) != "",` + alternate + `,"")`, nil
	}
	if p.Param != nil && e.shellKnown(p.Param.Value) && p.Index != nil && p.Exp == nil && !p.Excl {
		if index, ok := p.Index.(*syntax.Word); ok {
			if index.Lit() == "@" && p.Length {
				return "len(" + p.Param.Value + ")", nil
			}
			if !p.Length {
				if _, err := strconv.Atoi(index.Lit()); err == nil {
					return p.Param.Value + "[" + index.Lit() + "]", nil
				}
			}
		}
	}
	if p.Param != nil && p.Param.Value == "?" && p.Index == nil && p.Exp == nil && !p.Length && !p.Excl {
		e.bridge = true
		if e.execution {
			return e.program() + ".Status()", nil
		}
		return e.prefix + "rt.Status", nil
	}
	// Only the simple $name and ${name} bridge is implemented. All modifiers,
	// subscripts, positional and special parameters remain explicit failures.
	if p.Param == nil || !syntax.BashPPValidIdent(p.Param.Value) || p.Excl || p.Length || p.Width || p.IsSet || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Exp != nil || p.NestedParam != nil || p.Flags != nil || p.Split != syntax.OptUnset || p.GlobSubst != syntax.OptUnset || p.RcExpand != syntax.OptUnset || len(p.Modifiers) > 0 || p.Names != 0 || p.BadSubst != nil {
		return "", e.fail(p, CodeBridge, "parameter expansion requires shell-state lowering")
	}
	if !e.shellKnown(p.Param.Value) && e.mixedShell {
		return e.program() + ".ShellString(" + strconv.Quote(p.Param.Value) + ")", nil
	}
	if !e.shellKnown(p.Param.Value) {
		return "", e.fail(p, CodeUndefined, "unknown shell/typed binding: "+p.Param.Value)
	}
	return p.Param.Value, nil
}
func (e *emitter) quoted(q *syntax.DblQuoted) (string, error) {
	if q.Dollar {
		return "", e.fail(q, CodeUnsupported, "locale quoting")
	}
	return e.stringParts(q.Parts)
}
func (e *emitter) stringParts(parts []syntax.WordPart) (string, error) {
	var out []string
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			out = append(out, strconv.Quote(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", e.fail(p, CodeUnsupported, "ANSI-C quoting")
			}
			out = append(out, strconv.Quote(p.Value))
		case *syntax.DblQuoted:
			x, err := e.quoted(p)
			if err != nil {
				return "", err
			}
			out = append(out, x)
		case *syntax.ParamExp:
			x, err := e.parameter(p)
			if err != nil {
				return "", err
			}
			if p.Param != nil && e.shellKnown(p.Param.Value) && p.Index == nil && (p.Exp == nil || p.Exp.Op == syntax.DefaultUnset) && !p.Length {
				x, err = e.projectBinding(p, p.Param.Value, x)
				if err != nil {
					return "", err
				}
				if e.execution {
					x = e.program() + ".ShellBinding(" + strconv.Quote(p.Param.Value) + ",func() string {return " + e.prefix + "rt.Word(" + x + ")})"
				}
			}
			e.bridge = true
			out = append(out, e.prefix+"rt.Word("+x+")")
		case *syntax.ArithmExp:
			x, err := e.arithmetic(p.X)
			if err != nil {
				return "", err
			}
			e.bridge = true
			out = append(out, e.prefix+"rt.Word("+x+")")
		default:
			return "", e.fail(part, CodeBridge, "unsupported shell word expansion")
		}
	}
	if len(out) == 0 {
		return `""`, nil
	}
	return "(" + strings.Join(out, " + ") + ")", nil
}
func (e *emitter) shellWord(w *syntax.Word) (string, error) {
	if len(w.Parts) == 1 {
		if l, ok := w.Parts[0].(*syntax.Lit); ok {
			if parsed, err := parser.ParseExpr(l.Value); err == nil {
				if call, ok := parsed.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && e.funcs[id.Name] {
						return e.nativeWordExpr(w, l.Value)
					}
				}
			}
		}
	}

	if value, ok, err := e.shellProjection(w); ok || err != nil {
		return value, err
	}
	if len(w.Parts) == 1 {
		if l, ok := w.Parts[0].(*syntax.Lit); ok && e.inFunc && e.bound(l.Value) {
			return e.projectBinding(w, l.Value, l.Value)
		}
	}
	return e.stringParts(w.Parts)
}
func (e *emitter) arithmetic(x syntax.ArithmExpr) (string, error) {
	switch n := x.(type) {
	case *syntax.Word:
		return e.wordSequence([]*syntax.Word{n})
	case *syntax.BinaryArithm:
		op := n.Op.String()
		switch op {
		case "+", "-", "*", "/", "%", "&", "|", "^", "<<", ">>":
		default:
			return "", e.fail(n, CodeUnsupported, "shell arithmetic operator "+op)
		}
		l, err := e.arithmetic(n.X)
		if err != nil {
			return "", err
		}
		r, err := e.arithmetic(n.Y)
		return "(" + l + " " + op + " " + r + ")", err
	case *syntax.UnaryArithm:
		op := n.Op.String()
		if op != "+" && op != "-" && op != "~" {
			return "", e.fail(n, CodeUnsupported, "shell arithmetic unary operator "+op)
		}
		if op == "~" {
			op = "^"
		}
		v, err := e.arithmetic(n.X)
		return "(" + op + v + ")", err
	case *syntax.ParenArithm:
		v, err := e.arithmetic(n.X)
		return "(" + v + ")", err
	default:
		return "", e.fail(x, CodeUnsupported, "shell arithmetic form")
	}
}
func (e *emitter) shell(c *syntax.CallExpr) (string, error) { return e.shellWithTail(c, "") }
func (e *emitter) shellWithTail(c *syntax.CallExpr, tail string) (string, error) {
	if tail == "" && len(c.Args) > 0 {
		if text, handled, err := e.positionalShell(c); handled || err != nil {
			return text, err
		}
	}
	if len(c.Assigns) > 0 {
		if len(c.Args) > 0 {
			return "", e.fail(c, CodeBridge, "command-scoped assignments need shell-state runtime")
		}
		var out []string
		for _, a := range c.Assigns {
			if a.Name == nil || a.Index != nil || a.Array != nil || a.Append || a.Naked {
				return "", e.fail(a, CodeBridge, "non-scalar shell assignment")
			}
			name := a.Name.Value
			fresh := !e.known(name)
			v, err := e.valueWord(a.Value)
			if err != nil {
				return "", err
			}
			op := " = "
			if fresh {
				op = " := "
				e.bind(name)
			}
			e.projections.projectionInvalidate(name)
			out = append(out, name+op+v+e.unused([]string{name}))
		}
		return strings.Join(out, "\n"), nil
	}
	if len(c.Args) == 0 {
		return "", nil
	}
	if len(c.Args[0].Parts) != 1 {
		return "", e.fail(c, CodeBridge, "dynamic command name")
	}
	lit, ok := c.Args[0].Parts[0].(*syntax.Lit)
	if !ok {
		return "", e.fail(c, CodeBridge, "quoted command name")
	}
	name := lit.Value
	if name != "printf" && name != "echo" {
		return "", e.fail(c, CodeBridge, "dynamic shell command requires runtime lowering: "+name)
	}
	if name == "printf" && len(c.Args) > 1 {
		if err := e.printfFormat(c.Args[1]); err != nil {
			return "", err
		}
	}
	e.bridge = true
	var args []string
	for _, w := range c.Args[1:] {
		s, err := e.shellWord(w)
		if err != nil {
			return "", err
		}
		args = append(args, s)
	}
	if tail != "" {
		args = append(args, tail)
	}
	if name == "printf" && len(args) == 0 {
		return "", e.fail(c, CodeBridge, "printf requires a format")
	}
	if name == "echo" && len(c.Args) > 1 {
		w := c.Args[1]
		if len(w.Parts) == 1 {
			if l, ok := w.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(l.Value, "-") {
				return "", e.fail(w, CodeBridge, "echo option parsing needs full shell runtime")
			}
		}
	}
	method := "Printf"
	if name == "echo" {
		method = "Echo"
	}
	if e.execution {
		return "if " + e.prefix + "err := " + e.program() + "." + method + "(" + strings.Join(args, ", ") + "); " + e.prefix + "err != nil && " + e.program() + ".Status() != 0 {" + e.program() + ".Fail(" + e.prefix + "err)}", nil
	}
	return "if " + e.prefix + "err := " + e.prefix + "rt." + method + "(" + strings.Join(args, ", ") + "); " + e.prefix + "err != nil { " + e.prefix + "rt.Fail(" + e.prefix + "err) }", nil
}

func (e *emitter) printfFormat(w *syntax.Word) error {
	if len(w.Parts) != 1 {
		return e.fail(w, CodeBridge, "dynamic printf format needs full shell runtime")
	}
	var text string
	switch p := w.Parts[0].(type) {
	case *syntax.SglQuoted:
		if p.Dollar {
			return e.fail(w, CodeBridge, "ANSI-C format")
		}
		text = p.Value
	case *syntax.Lit:
		if e.inFunc && e.known(p.Value) {
			return e.fail(w, CodeBridge, "dynamic printf format")
		}
		text = p.Value
	default:
		return e.fail(w, CodeBridge, "foundation printf format must be a literal or single-quoted word")
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			if i >= len(text) || !strings.ContainsRune("ntr\\abfv", rune(text[i])) {
				return e.fail(w, CodeBridge, "printf escape needs full shell runtime")
			}
			continue
		}
		if text[i] == '%' {
			i++
			if i >= len(text) || !strings.ContainsRune("sd%", rune(text[i])) {
				return e.fail(w, CodeBridge, "printf conversion needs full shell runtime")
			}
		}
	}
	return nil
}

func (e *emitter) argument(w *syntax.Word) (string, error) {
	if value, ok, err := e.shellProjection(w); ok || err != nil {
		return value, err
	}
	if len(w.Parts) == 1 {
		if l, ok := w.Parts[0].(*syntax.Lit); ok && (syntax.BashPPValidIdent(l.Value) || token.Lookup(l.Value).IsKeyword()) && !e.known(l.Value) {
			return strconv.Quote(l.Value), nil
		}
	}
	return e.valueWord(w)
}

// Shell arguments have an explicit structured-value projection convention.
// Decode only selector/index paths rooted in a live binding; ordinary quoted
// strings and unknown command words retain shell literal semantics.
func (e *emitter) shellProjection(w *syntax.Word) (string, bool, error) {
	if len(w.Parts) == 0 {
		return "", false, nil
	}
	if _, ok := w.Parts[0].(*syntax.Lit); !ok {
		return "", false, nil
	}
	var raw strings.Builder
	if err := syntax.NewPrinter().Print(&raw, w); err != nil {
		return "", false, err
	}
	expr, err := parser.ParseExpr(raw.String())
	if err != nil {
		return "", false, nil
	}
	switch expr.(type) {
	case *ast.SelectorExpr, *ast.IndexExpr:
	default:
		return "", false, nil
	}
	var root func(ast.Expr) (string, bool)
	root = func(x ast.Expr) (string, bool) {
		switch n := x.(type) {
		case *ast.Ident:
			return n.Name, e.known(n.Name)
		case *ast.SelectorExpr:
			return root(n.X)
		case *ast.IndexExpr:
			switch n.Index.(type) {
			case *ast.BasicLit, *ast.Ident:
			default:
				return "", false
			}
			return root(n.X)
		}
		return "", false
	}
	if _, ok := root(expr); !ok {
		return "", false, nil
	}
	var output bytes.Buffer
	if err := format.Node(&output, token.NewFileSet(), expr); err != nil {
		return "", false, err
	}
	return output.String(), true, nil
}
