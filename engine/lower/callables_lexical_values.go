package lower

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

type lexicalValueBinding struct {
	bindings   ast.Expr
	id, name   string
	object     types.Object
	registered token.Pos
	present    ast.Expr
}
type lexicalValuePass struct {
	e             *emitter
	fs            *token.FileSet
	info          *types.Info
	pkg           *types.Package
	locals        map[types.Object]lexicalValueBinding
	rt            string
	mappings      []Mapping
	changed       map[ast.Node]bool
	parents       map[ast.Node]ast.Node
	initial       map[types.Object]constant.Value
	initialExpr   map[types.Object]ast.Expr
	scalarResult  map[types.Object]string
	scalarError   map[types.Object]string
	assignedValue map[ast.Expr]constant.Value
}

// lexicalValues runs after storage registration. It uses checked Go object
// identity to distinguish a binding from a same-spelled field or shadowed name.
func (e *emitter) lexicalValues(source []byte) ([]byte, error) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "generated.go", source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if e.moduleImporter == nil {
		e.moduleImporter = newModuleImporter(e.options.Dir)
	}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: bridgeImporter{fallback: e.moduleImporter, path: e.options.Runtime, cache: map[string]*types.Package{}, foreign: e.hasForeign()}}
	pkg, err := conf.Check(file.Name.Name, fs, []*ast.File{file}, info)
	if err != nil {
		return nil, fmt.Errorf("lexical values: %w", err)
	}
	p := &lexicalValuePass{e: e, fs: fs, info: info, pkg: pkg, locals: map[types.Object]lexicalValueBinding{}, rt: e.prefix + "rt.", changed: map[ast.Node]bool{}, parents: map[ast.Node]ast.Node{}, initial: map[types.Object]constant.Value{}, initialExpr: map[types.Object]ast.Expr{}, scalarResult: map[types.Object]string{}, scalarError: map[types.Object]string{}, assignedValue: map[ast.Expr]constant.Value{}}
	if len(e.marks) > 0 {
		p.mappings = e.sourceMappings(source)
	}
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			p.parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch declaration := n.(type) {
		case *ast.ValueSpec:
			if len(declaration.Values) == len(declaration.Names) {
				for i, id := range declaration.Names {
					p.initial[info.Defs[id]] = info.Types[declaration.Values[i]].Value
					p.initialExpr[info.Defs[id]] = lexicalUnparen(declaration.Values[i])
				}
			}
		case *ast.AssignStmt:
			if len(declaration.Rhs) == len(declaration.Lhs) {
				for i, lhs := range declaration.Lhs {
					p.assignedValue[lhs] = info.Types[declaration.Rhs[i]].Value
				}
			}
			if declaration.Tok == token.DEFINE && len(declaration.Rhs) == len(declaration.Lhs) {
				for i, lhs := range declaration.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						p.initial[info.Defs[id]] = info.Types[declaration.Rhs[i]].Value
						p.initialExpr[info.Defs[id]] = lexicalUnparen(declaration.Rhs[i])
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !p.runtimeCall(call, "Register") || len(call.Args) != 6 {
			return true
		}
		address, ok := call.Args[3].(*ast.UnaryExpr)
		if !ok || address.Op != token.AND {
			return true
		}
		id, ok := address.X.(*ast.Ident)
		if !ok {
			return true
		}
		key, kok := p.stringValue(call.Args[1])
		name, nok := p.stringValue(call.Args[2])
		if !kok || !nok {
			return true
		}
		object := info.Uses[id]
		p.locals[object] = lexicalValueBinding{bindings: call.Args[0], id: key, name: name, object: object, registered: call.Pos(), present: call.Args[4].(*ast.UnaryExpr).X}
		return true
	})
	// Read rewriting is performed bottom-up, except address/lvalue roots, which
	// retain native identity. Replacements are not revisited by this pass.
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		n := c.Node()
		if n == nil {
			return true
		}
		if assignment, ok := n.(*ast.AssignStmt); ok && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
			if id, ok := assignment.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
				return false
			}
		}
		if selector, ok := n.(*ast.SelectorExpr); ok {
			if selection := p.info.Selections[selector]; selection != nil && selection.Kind() == types.MethodVal {
				if signature, ok := selection.Obj().Type().(*types.Signature); ok {
					_, pointerReceiver := signature.Recv().Type().(*types.Pointer)
					_, scalarReceiver := p.info.TypeOf(selector.X).Underlying().(*types.Basic)
					if pointerReceiver && scalarReceiver && p.info.Types[selector.X].Addressable() {
						if bindings := p.contextBindings(selector); bindings != "" {
							selector.X = p.parse("(*" + p.rt + "MustValue(" + p.rt + "LexicalAddress(" + bindings + ",&(" + p.text(selector.X) + ")," + p.site(selector, "") + ")))")
							p.markChanged(selector)
							return false
						}
					}
				}
			}
		}
		if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.AND {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && (p.runtimeCall(call, "NumericUpdate") || p.runtimeCall(call, "AssignTuple") || p.runtimeCall(call, "TransferResults")) {
			if bindings := p.contextBindings(call); bindings != "" {
				originalFunction := call.Fun
				name := "LexicalNumericUpdate"
				if p.runtimeCall(call, "TransferResults") {
					name = "LexicalTransferResults"
					call.Args = append(call.Args, originalFunction)
				}
				if p.runtimeCall(call, "AssignTuple") {
					name = "LexicalAssignTuple"
				}
				call.Fun = p.parse(p.rt + name)
				call.Args = append([]ast.Expr{p.parse(bindings)}, call.Args...)
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && (p.runtimeCall(call, "Register") || p.runtimeCall(call, "Cell")) {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && (p.runtimeCall(call, "TypedFloatProjection") || p.runtimeCall(call, "ForeignBytesProjection")) && len(call.Args) == 1 && p.printArgument(call) {
			if binding, known := p.binding(call.Args[0]); known {
				c.Replace(p.parse(p.rt + "LexicalPrint(" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + strconv.Quote(binding.name) + "," + p.site(call, binding.name) + ")"))
				return false
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && p.runtimeCall(call, "BindingValue") && len(call.Args) == 3 {
			if binding, known := p.binding(call.Args[1]); known {
				observed := p.rt + "LexicalPrint(" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + p.text(call.Args[2]) + "," + p.site(call, binding.name) + ")"
				parent := p.parents[call]
				for {
					paren, ok := parent.(*ast.ParenExpr)
					if !ok {
						break
					}
					parent = p.parents[paren]
				}
				if outer, ok := parent.(*ast.CallExpr); ok && p.runtimeCall(outer, "Word") {
					observed = p.program(binding) + ".ShellString(" + strconv.Quote(binding.name) + ")"
				}
				c.Replace(p.parse("func() any {if !(" + p.text(call.Args[0]) + "){return " + p.text(call.Args[2]) + "};return " + observed + "}()"))
				return false
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && p.runtimeCall(call, "Word") && len(call.Args) == 1 {
			argument := call.Args[0]
			for {
				paren, ok := argument.(*ast.ParenExpr)
				if !ok {
					break
				}
				argument = paren.X
			}
			if binding, ok := p.binding(argument); ok {
				c.Replace(p.parse(p.program(binding) + ".ShellString(" + strconv.Quote(binding.name) + ")"))
				return false
			}
		}
		if p.writeRoot(n) {
			return false
		}
		if binary, ok := n.(*ast.BinaryExpr); ok && binary.Op != token.LAND && binary.Op != token.LOR && p.scalarTree(binary) {
			replacement := p.scalarBinary(binary)
			c.Replace(p.parse(replacement))
			p.markChanged(binary)
			return false
		}
		return true
	}, func(c *astutil.Cursor) bool {
		expr, ok := c.Node().(ast.Expr)
		if !ok {
			return true
		}
		if deref, ok := expr.(*ast.StarExpr); ok && p.info.Types[expr].IsValue() && p.scalarType(expr) {
			if bindings := p.contextBindings(expr); bindings != "" {
				c.Replace(p.parse(p.rt + "MustValue(" + p.rt + "LoadAddress(" + bindings + "," + p.text(deref.X) + "," + p.site(expr, "") + "))"))
				p.markChanged(expr)
				return true
			}
		}
		if binding, ok := p.binding(expr); ok {
			if _, ok := p.info.TypeOf(expr).Underlying().(*types.Basic); !ok {
				return true
			}
			if p.printArgument(expr) {
				c.Replace(p.parse(p.rt + "LexicalPrint(" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + strconv.Quote(binding.name) + "," + p.site(expr, binding.name) + ")"))
			} else {
				typ := p.typeText(p.info.TypeOf(expr))
				c.Replace(p.parse(p.rt + "MustValue(" + p.rt + "Load[" + typ + "](" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + p.site(expr, binding.name) + "))"))
			}
			p.markChanged(expr)
		}
		return true
	})
	p.transactions(file)
	ast.Inspect(file, func(n ast.Node) bool {
		var signature *ast.FuncType
		var body *ast.BlockStmt
		switch function := n.(type) {
		case *ast.FuncDecl:
			signature, body = function.Type, function.Body
		case *ast.FuncLit:
			signature, body = function.Type, function.Body
		}
		if signature == nil || body == nil || !p.sourceCallable(body) {
			return true
		}
		if program := p.signatureProgram(signature); program != "" {
			mark := p.e.prefix + "rawFailureMark" + strconv.Itoa(int(n.Pos()))
			body.List = append(p.statements(mark+" := "+program+".ShortFailureMark();defer "+program+".SettleShortFailures("+mark+")"), body.List...)
		}
		return true
	})
	var output bytes.Buffer
	if err := format.Node(&output, fs, file); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
func (p *lexicalValuePass) runtimeCall(call *ast.CallExpr, name string) bool {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	object := p.info.Uses[sel.Sel]
	return object != nil && object.Name() == name && object.Pkg() != nil && object.Pkg().Path() == p.e.options.Runtime
}
func (p *lexicalValuePass) stringValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
func (p *lexicalValuePass) binding(expr ast.Expr) (lexicalValueBinding, bool) {
	if id, ok := expr.(*ast.Ident); ok {
		b, ok := p.locals[p.info.Uses[id]]
		return b, ok && expr.Pos() > b.registered
	}
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Value" {
		return lexicalValueBinding{}, false
	}
	call, ok := selector.X.(*ast.CallExpr)
	if !ok || !p.runtimeCall(call, "Cell") || len(call.Args) != 4 {
		return lexicalValueBinding{}, false
	}
	id, iok := p.stringValue(call.Args[1])
	name, nok := p.stringValue(call.Args[2])
	return lexicalValueBinding{bindings: call.Args[0], id: id, name: name}, iok && nok
}
func (p *lexicalValuePass) writeRoot(n ast.Node) bool {
	parent := p.parents[n]
	switch x := parent.(type) {
	case *ast.AssignStmt:
		for _, lhs := range x.Lhs {
			if lhs == n {
				return true
			}
		}
	case *ast.IncDecStmt:
		return x.X == n
	case *ast.RangeStmt:
		return x.Key == n || x.Value == n
	case *ast.SelectorExpr:
		return x.Sel == n
	case *ast.Field:
		return true
	}
	return false
}
func (p *lexicalValuePass) printArgument(n ast.Node) bool {
	call, ok := p.parents[n].(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	selection := p.info.Selections[selector]
	if selection == nil {
		return false
	}
	object := selection.Obj()
	return object.Pkg() != nil && object.Pkg().Path() == p.e.options.Runtime && (object.Name() == "Print" || object.Name() == "Println")
}
func (p *lexicalValuePass) text(n ast.Node) string {
	var b bytes.Buffer
	_ = format.Node(&b, p.fs, n)
	return b.String()
}
func (p *lexicalValuePass) parse(s string) ast.Expr {
	x, err := parser.ParseExpr(s)
	if err != nil {
		panic("lexical values generated invalid expression: " + s + ": " + err.Error())
	}
	return x
}
func (p *lexicalValuePass) typeText(t types.Type) string {
	return types.TypeString(t, func(pkg *types.Package) string {
		if pkg == p.pkg {
			return ""
		}
		if pkg.Path() == p.e.options.Runtime {
			return strings.TrimSuffix(p.rt, ".")
		}
		for alias, path := range p.e.imports {
			if path == pkg.Path() {
				return alias
			}
		}
		return pkg.Name()
	})
}
func (p *lexicalValuePass) site(n ast.Node, name string) string {
	site := fmt.Sprintf("%sValueSite{File:%q,Name:%q", p.rt, p.e.options.Origin, name)
	line := p.fs.Position(n.Pos()).Line
	for i := len(p.mappings) - 1; i >= 0; i-- {
		m := p.mappings[i]
		if m.GoLine <= line {
			site += fmt.Sprintf(",Line:%d,Column:%d,Offset:%d", m.Pos.Line(), m.Pos.Col(), m.Pos.Offset())
			break
		}
	}
	return site + "}"
}

func (p *lexicalValuePass) markChanged(node ast.Node) {
	for parent := p.parents[node]; parent != nil; parent = p.parents[parent] {
		p.changed[parent] = true
	}
}
func (p *lexicalValuePass) scalarTree(expr ast.Expr) bool {
	typ := p.info.TypeOf(expr)
	if typ == nil {
		return false
	}
	basic, ok := typ.Underlying().(*types.Basic)
	if !ok || basic.Info()&(types.IsBoolean|types.IsString|types.IsInteger|types.IsFloat) == 0 {
		return false
	}
	if binary, ok := expr.(*ast.BinaryExpr); ok {
		if !p.scalarType(binary.X) || !p.scalarType(binary.Y) {
			return false
		}
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		x, ok := n.(ast.Expr)
		if ok {
			if _, ok := x.(*ast.StarExpr); ok && p.info.Types[x].IsValue() && p.scalarType(x) {
				found = true
				return false
			}
			if _, ok := p.binding(x); ok && p.scalarType(x) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
func (p *lexicalValuePass) scalarBinary(binary *ast.BinaryExpr) string {
	return p.rt + "LexicalBinary[" + p.typeText(types.Default(p.info.TypeOf(binary))) + "](" + p.scalarOperand(binary.X) + "," + p.scalarOperand(binary.Y) + "," + strconv.Itoa(int(binary.Op)) + "," + p.site(binary, "") + ")"
}
func (p *lexicalValuePass) scalarOperand(expr ast.Expr) string {
	if paren, ok := expr.(*ast.ParenExpr); ok {
		return p.scalarOperand(paren.X)
	}
	if deref, ok := expr.(*ast.StarExpr); ok {
		if bindings := p.contextBindings(expr); bindings != "" {
			return p.rt + "LexicalOperandAddress(" + bindings + "," + p.text(deref.X) + "," + p.site(expr, "") + ")"
		}
	}

	if binding, ok := p.binding(expr); ok {
		return p.rt + "LexicalOperand[" + p.typeText(p.info.TypeOf(expr)) + "](" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + p.site(expr, binding.name) + ")"
	}
	if binary, ok := expr.(*ast.BinaryExpr); ok && binary.Op != token.LAND && binary.Op != token.LOR && p.scalarTree(binary) {
		if basic, ok := p.info.TypeOf(binary).Underlying().(*types.Basic); ok && basic.Info()&types.IsFloat != 0 {
			return strings.Replace(p.scalarBinary(binary), "LexicalBinary[", "LexicalEvaluate[", 1) + ".Scalar"
		}
		return p.rt + "LexicalNative(" + p.scalarBinary(binary) + ")"
	}
	if value := p.info.Types[expr].Value; value != nil {
		switch value.Kind().String() {
		case "Int":
			return p.rt + "LexicalLiteral(" + strconv.Quote(value.ExactString()) + ",5)"
		case "Float":
			return p.rt + "LexicalLiteral(" + strconv.Quote(value.String()) + ",6)"
		}
	}
	return p.rt + "LexicalNative(" + p.readExpression(expr) + ")"
}
func (p *lexicalValuePass) statements(source string) []ast.Stmt {
	file, err := parser.ParseFile(token.NewFileSet(), "fragment.go", "package fragment;func fragment(){"+source+"}", 0)
	if err != nil {
		panic("lexical statement: " + source + ": " + err.Error())
	}
	return file.Decls[0].(*ast.FuncDecl).Body.List
}
func (p *lexicalValuePass) transactions(file *ast.File) {
	presence := map[types.Object]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		block, ok := node.(*ast.BlockStmt)
		if !ok {
			return true
		}
		var result []ast.Stmt
		for _, statement := range block.List {
			assignment, ok := statement.(*ast.AssignStmt)
			if ok && assignment.Tok != token.ASSIGN && assignment.Tok != token.DEFINE && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
				if bindings := p.contextBindings(assignment); bindings != "" {
					name := p.e.prefix + "rawUpdate" + strconv.Itoa(int(assignment.Pos()))
					result = append(result, p.statements("if "+name+" := "+p.rt+"LexicalNumericUpdate("+bindings+", &("+p.text(assignment.Lhs[0])+"), "+p.text(assignment.Rhs[0])+","+strconv.Quote(assignment.Tok.String())+","+p.site(assignment, "")+"); "+name+" != nil {"+strings.TrimSuffix(bindings, ".Bindings")+".Fail("+name+")}")...)
					continue
				}
			}
			if ok && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
				if id, ok := assignment.Lhs[0].(*ast.Ident); ok {
					if errName := presence[p.info.Defs[id]]; errName != "" {
						assignment.Rhs[0] = p.parse(errName + " == nil")
					}
				}
				if p.changed[statement] && assignment.Tok == token.DEFINE {
					id, ok := assignment.Lhs[0].(*ast.Ident)
					if ok {
						binding, known := p.locals[p.info.Defs[id]]
						if known {
							errName := p.e.prefix + "rawError" + strconv.Itoa(int(statement.Pos()))
							typ := p.typeText(binding.object.Type())
							initializer := p.text(assignment.Rhs[0])
							capture := ""
							if basic, ok := binding.object.Type().Underlying().(*types.Basic); ok && basic.Info()&types.IsFloat != 0 {
								if binary, ok := p.initialExpr[binding.object].(*ast.BinaryExpr); ok && p.scalarTree(binary) {
									name := p.e.prefix + "rawScalar" + strconv.Itoa(int(statement.Pos()))
									capture = "var " + name + " " + p.rt + "LexicalEvaluated[" + typ + "];"
									initializer = "func() " + typ + " {" + name + " = " + strings.Replace(p.scalarBinary(binary), "LexicalBinary[", "LexicalEvaluate[", 1) + ";return " + name + ".Value}()"
									p.scalarResult[binding.object] = name + ".Scalar"
									p.scalarError[binding.object] = errName
								}
							}
							text := capture + id.Name + ", " + errName + " := " + p.rt + "TryValue(func() " + typ + " {return " + initializer + "});if " + errName + " != nil {" + p.program(binding) + ".Fail(" + errName + ");" + p.program(binding) + ".ShortFailure()}"
							result = append(result, p.statements(text)...)
							if flag, ok := binding.present.(*ast.Ident); ok {
								presence[p.info.Uses[flag]] = errName
							}
							continue
						}
					}
				}
			}
			if ok && assignment.Tok == token.ASSIGN && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
				target := assignment.Lhs[0]
				for {
					paren, ok := target.(*ast.ParenExpr)
					if !ok {
						break
					}
					target = paren.X
				}
				if pointer, ok := target.(*ast.StarExpr); ok {
					if bindings := p.contextBindings(assignment); bindings != "" {
						temp := p.e.prefix + "rawTarget" + strconv.Itoa(int(assignment.Pos()))
						result = append(result, p.statements("{"+temp+" := "+p.text(pointer.X)+";*"+temp+" = "+p.text(assignment.Rhs[0])+";"+p.rt+"MustReadonly("+bindings+".NativeWrittenAt("+temp+"))}")...)
						continue
					}
				}
			}
			result = append(result, statement)
			if _, ok := statement.(*ast.ExprStmt); ok {
				ast.Inspect(statement, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || !p.runtimeCall(call, "Register") {
						return true
					}
					id := call.Args[3].(*ast.UnaryExpr).X.(*ast.Ident)
					binding := p.locals[p.info.Uses[id]]
					if scalar := p.scalarResult[binding.object]; scalar != "" {
						result = append(result, p.statements("if "+p.scalarError[binding.object]+" == nil {"+p.rt+"MustReadonly("+p.text(binding.bindings)+".NativeScalarWritten("+strconv.Quote(binding.id)+","+scalar+"))}")...)
					}
					result = append(result, p.statements(p.rt+"MustReadonly("+p.rt+"LexicalSourceType("+p.text(binding.bindings)+","+strconv.Quote(binding.id)+","+strconv.Quote(p.typeText(binding.object.Type().Underlying()))+"))")...)
					if basic, ok := binding.object.Type().Underlying().(*types.Basic); ok && basic.Info()&types.IsFloat != 0 {
						if value := p.initial[binding.object]; value != nil {
							result = append(result, p.statements(p.rt+"MustReadonly("+p.text(binding.bindings)+".NativeScalarWritten("+strconv.Quote(binding.id)+","+p.rt+"LexicalExact("+strconv.Quote(value.ExactString())+")))")...)
						}
					}
					return false
				})
			}
			if ok && assignment.Tok == token.ASSIGN {
				for _, lhs := range assignment.Lhs {
					if binding, known := p.binding(lhs); known {
						notification := p.text(binding.bindings) + ".NativeWritten(" + strconv.Quote(binding.id) + ")"
						if p.scalarType(lhs) {
							result = append(result, p.statements(p.rt+"MustReadonly("+p.rt+"LexicalSourceType("+p.text(binding.bindings)+","+strconv.Quote(binding.id)+","+strconv.Quote(p.typeText(p.info.TypeOf(lhs).Underlying()))+"))")...)
							if basic := p.info.TypeOf(lhs).Underlying().(*types.Basic); basic.Info()&types.IsFloat != 0 {
								if value := p.assignedValue[lhs]; value != nil {
									notification = p.text(binding.bindings) + ".NativeScalarWritten(" + strconv.Quote(binding.id) + "," + p.rt + "LexicalExact(" + strconv.Quote(value.ExactString()) + "))"
								}
							}
						}
						result = append(result, p.statements(p.rt+"MustReadonly("+notification+")")...)
					}
				}
			}
		}
		block.List = result
		return true
	})
}
func (p *lexicalValuePass) program(binding lexicalValueBinding) string {
	if selector, ok := binding.bindings.(*ast.SelectorExpr); ok {
		return p.text(selector.X)
	}
	panic("compiler lexical binding has no explicit Program")
}

func (p *lexicalValuePass) contextBindings(node ast.Node) string {
	for parent := p.parents[node]; parent != nil; parent = p.parents[parent] {
		var signature *ast.FuncType
		switch function := parent.(type) {
		case *ast.FuncDecl:
			signature = function.Type
		case *ast.FuncLit:
			signature = function.Type
		}
		if signature == nil {
			continue
		}
		if program := p.signatureProgram(signature); program != "" {
			return program + ".Bindings"
		}
	}
	return ""
}
func (p *lexicalValuePass) signatureProgram(signature *ast.FuncType) string {
	for _, field := range signature.Params.List {
		ptr, ok := p.info.TypeOf(field.Type).(*types.Pointer)
		if !ok {
			continue
		}
		named, ok := ptr.Elem().(*types.Named)
		if !ok {
			continue
		}
		object := named.Obj()
		if object.Name() == "Program" && object.Pkg() != nil && object.Pkg().Path() == p.e.options.Runtime && len(field.Names) == 1 {
			return field.Names[0].Name
		}
	}
	return ""
}

func (p *lexicalValuePass) sourceCallable(body *ast.BlockStmt) bool {
	found, settled := false, false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		selection := p.info.Selections[sel]
		if selection == nil {
			return true
		}
		object := selection.Obj()
		if object.Name() == "SettleShortFailures" && object.Pkg() != nil && object.Pkg().Path() == p.e.options.Runtime {
			settled = true
		}
		if object.Name() == "Enter" && object.Pkg() != nil && object.Pkg().Path() == p.e.options.Runtime {
			found = true
		}
		return true
	})
	return found && !settled
}

func (p *lexicalValuePass) scalarType(expr ast.Expr) bool {
	typ := p.info.TypeOf(expr)
	if typ == nil {
		return false
	}
	basic, ok := typ.Underlying().(*types.Basic)
	return ok && basic.Info()&(types.IsBoolean|types.IsString|types.IsInteger|types.IsFloat) != 0
}

func (p *lexicalValuePass) readExpression(expr ast.Expr) string {
	if binding, ok := p.binding(expr); ok && p.scalarType(expr) {
		return p.rt + "MustValue(" + p.rt + "Load[" + p.typeText(p.info.TypeOf(expr)) + "](" + p.text(binding.bindings) + "," + strconv.Quote(binding.id) + "," + p.site(expr, binding.name) + "))"
	}
	switch x := expr.(type) {
	case *ast.ParenExpr:
		return "(" + p.readExpression(x.X) + ")"
	case *ast.CallExpr:
		args := make([]string, len(x.Args))
		for i, arg := range x.Args {
			args[i] = p.readExpression(arg)
		}
		if x.Ellipsis.IsValid() && len(args) > 0 {
			args[len(args)-1] += "..."
		}
		return p.text(x.Fun) + "(" + strings.Join(args, ",") + ")"
	case *ast.UnaryExpr:
		if x.Op != token.AND {
			return "(" + x.Op.String() + p.readExpression(x.X) + ")"
		}
	case *ast.BinaryExpr:
		if x.Op != token.LAND && x.Op != token.LOR && p.scalarTree(x) {
			return p.scalarBinary(x)
		}
		return "(" + p.readExpression(x.X) + x.Op.String() + p.readExpression(x.Y) + ")"
	case *ast.StarExpr:
		if p.scalarType(x) {
			if bindings := p.contextBindings(x); bindings != "" {
				return p.rt + "MustValue(" + p.rt + "LoadAddress(" + bindings + "," + p.readExpression(x.X) + "," + p.site(x, "") + "))"
			}
		}
	case *ast.IndexExpr:
		return p.readExpression(x.X) + "[" + p.readExpression(x.Index) + "]"
	}
	return p.text(expr)
}

func lexicalUnparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}
