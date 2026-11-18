package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func sharpError(n syntax.Node, code, message string) error {
	return ErrorList{{Code: code, Msg: message, Node: nodeName(n), Pos: n.Pos()}}
}

type sharpParameter struct {
	name  string
	field *syntax.BashPPField
}

func sharpParameters(f *syntax.BashPPFuncDecl) ([]sharpParameter, error) {
	var params []sharpParameter
	defaultSeen := false
	for _, field := range f.Params {
		for _, name := range field.Names {
			if field.Default == nil && defaultSeen {
				return nil, sharpError(name, "BASHPP-EDEFAULT-ORDER", fmt.Sprintf("required parameter %q follows a default parameter", name.Value))
			}
			defaultSeen = defaultSeen || field.Default != nil
			params = append(params, sharpParameter{name.Value, field})
		}
	}
	return params, nil
}

// sharpCallPlan separates source evaluation order from parameter binding order.
// Evaluate Words left-to-right in the caller scope, once each; then call the
// unchanged native function with temporary values indexed by ParameterOrder.
// Defaults are appended after every supplied argument in parameter order.
type sharpCallPlan struct {
	Words          []*syntax.Word
	ParameterOrder []int
}

func planSharpCall(call *syntax.BashPPCall, function *syntax.BashPPFuncDecl) (*sharpCallPlan, error) {
	params, err := sharpParameters(function)
	if err != nil {
		return nil, err
	}
	positional := len(call.Args) - len(call.ArgNames)
	if positional < 0 {
		return nil, sharpError(call, CodeExpr, "invalid named argument shape")
	}
	if positional > len(params) {
		return nil, sharpError(call, "BASHPP-EARG-COUNT", fmt.Sprintf("%s accepts at most %d arguments; got %d", function.Name.Value, len(params), len(call.Args)))
	}
	seen := map[string]bool{}
	for _, name := range call.ArgNames {
		if seen[name.Value] {
			return nil, sharpError(name, "BASHPP-EKWARG-DUPLICATE", fmt.Sprintf("argument %q is supplied more than once", name.Value))
		}
		seen[name.Value] = true
	}
	byName := map[string]int{}
	for i, param := range params {
		byName[param.name] = i
	}
	for _, name := range call.ArgNames {
		if _, ok := byName[name.Value]; !ok {
			return nil, sharpError(name, "BASHPP-EKWARG-UNKNOWN", fmt.Sprintf("%s has no parameter named %q", function.Name.Value, name.Value))
		}
	}
	plan := &sharpCallPlan{Words: append([]*syntax.Word(nil), call.Args...), ParameterOrder: make([]int, len(params))}
	for i := range plan.ParameterOrder {
		plan.ParameterOrder[i] = -1
	}
	for i := 0; i < positional; i++ {
		plan.ParameterOrder[i] = i
	}
	for i, name := range call.ArgNames {
		target := byName[name.Value]
		if plan.ParameterOrder[target] >= 0 {
			return nil, sharpError(name, "BASHPP-EARG-DUPLICATE-BINDING", fmt.Sprintf("parameter %q is supplied positionally and by name", name.Value))
		}
		plan.ParameterOrder[target] = positional + i
	}
	for i, param := range params {
		if plan.ParameterOrder[i] >= 0 {
			continue
		}
		if param.field.Default == nil {
			return nil, sharpError(call, "BASHPP-EARG-MISSING", fmt.Sprintf("%s requires argument %q", function.Name.Value, param.name))
		}
		plan.ParameterOrder[i] = len(plan.Words)
		plan.Words = append(plan.Words, param.field.Default)
	}
	return plan, nil
}
func hasSharpDefaults(f *syntax.BashPPFuncDecl) bool {
	for _, field := range f.Params {
		if field.Default != nil {
			return true
		}
	}
	return false
}
func checkEnumDecl(decl *syntax.BashPPDecl) error {
	seen := map[string]bool{}
	for _, member := range decl.EnumMembers {
		if !syntax.BashPPValidIdent(member.Value) {
			return sharpError(member, "BASHPP-EENUM-MEMBER", fmt.Sprintf("enum member %q must be an identifier", member.Value))
		}
		if seen[member.Value] {
			return sharpError(member, "BASHPP-EENUM-DUPLICATE", fmt.Sprintf("enum %s declares member %q more than once", decl.Name.Value, member.Value))
		}
		seen[member.Value] = true
	}
	return nil
}
func (e *emitter) enumDecl(decl *syntax.BashPPDecl) (string, error) {
	if err := checkEnumDecl(decl); err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "type %s int\nconst (\n", decl.Name.Value)
	for i, member := range decl.EnumMembers {
		fmt.Fprintf(&out, "%s %s = %d\n", member.Value, decl.Name.Value, i)
	}
	out.WriteString(")\n")
	return out.String(), nil
}

// CheckBashSharp validates static Bash# obligations on the source AST. It is
// independent of Compile dispatch so mixed shell statements do not disable
// checking. Deep readonly mutation is intentionally a runtime obligation.
func CheckBashSharp(file *syntax.File) error {
	if file == nil {
		return ErrorList{{Code: CodeExpr, Msg: "nil syntax file"}}
	}
	functions := map[string]*syntax.BashPPFuncDecl{}
	enums := map[string]*syntax.BashPPDecl{}
	var diagnostics ErrorList
	add := func(err error) {
		if list, ok := err.(ErrorList); ok {
			diagnostics = append(diagnostics, list...)
		}
	}
	syntax.Walk(file, func(n syntax.Node) bool {
		switch node := n.(type) {
		case *syntax.BashPPDecl:
			if node.DeclType != nil && node.DeclType.Value == "enum" {
				add(checkEnumDecl(node))
				enums[node.Name.Value] = node
			}
		case *syntax.BashPPFuncDecl:
			functions[node.Name.Value] = node
			_, err := sharpParameters(node)
			add(err)
		}
		return true
	})
	syntax.Walk(file, func(n syntax.Node) bool {
		switch node := n.(type) {
		case *syntax.BashPPCall:
			if len(node.Fun) != 1 {
				break
			}
			name := node.Fun[0].Value
			if function := functions[name]; function != nil && (len(node.ArgNames) > 0 || hasSharpDefaults(function)) {
				_, err := planSharpCall(node, function)
				add(err)
			}
			if enum := enums[name]; enum != nil && len(node.Args) == 1 {
				text, ok := sharpWordText(node.Args[0])
				if !ok {
					break
				}
				value, err := strconv.ParseInt(text, 0, 64)
				if err == nil && (value < 0 || value >= int64(len(enum.EnumMembers))) {
					add(sharpError(node, "BASHPP-EENUM-VALUE", fmt.Sprintf("%s is not a member of %s", text, name)))
				}
			}
		case *syntax.BashPPFuncDecl:
			types := map[string]string{}
			for _, field := range node.Params {
				if field.FieldType != nil {
					for _, name := range field.Names {
						types[name.Value] = field.FieldType.Value
					}
				}
			}
			syntax.Walk(node.Body, func(child syntax.Node) bool {
				sw, ok := child.(*syntax.BashPPSwitch)
				if !ok {
					return true
				}
				id, ok := sw.Tag.(*syntax.BashPPIdent)
				if !ok {
					return true
				}
				enum := enums[types[id.Name.Value]]
				if enum == nil {
					return true
				}
				covered, hasDefault := map[string]bool{}, false
				for _, arm := range sw.Arms {
					hasDefault = hasDefault || len(arm.Exprs) == 0
					for _, expr := range arm.Exprs {
						if member, ok := expr.(*syntax.BashPPIdent); ok {
							covered[member.Name.Value] = true
						}
					}
				}
				if !hasDefault {
					for _, member := range enum.EnumMembers {
						if !covered[member.Value] {
							add(sharpError(sw, "BASHPP-EENUM-NONEXHAUSTIVE", fmt.Sprintf("switch on %s is missing member %s or a default arm", enum.Name.Value, member.Value)))
							break
						}
					}
				}
				return true
			})
			checker := newSharpNullChecker()
			checker.parameters(node.Params)
			checker.block(node.Body, checker.initial)
			diagnostics = append(diagnostics, checker.diagnostics...)
		}
		return true
	})
	sort.SliceStable(diagnostics, func(i, j int) bool { return diagnostics[i].Pos.Offset() < diagnostics[j].Pos.Offset() })
	if len(diagnostics) > 0 {
		return diagnostics
	}
	return nil
}

// A compact expression view keeps nullable-flow analysis independent of code
// emission. Positioned typed expressions are consumed directly. Only legacy
// Word-backed typed returns/arguments need the committed-expression adapter.
type sharpNullExpr struct {
	op, name string
	node     syntax.Node
	children []*sharpNullExpr
}

func sharpExpr(x syntax.BashPPExpr) *sharpNullExpr {
	if x == nil {
		return nil
	}
	n := &sharpNullExpr{node: x}
	// Call becomes an expression when the parser exposes nested typed calls.
	// The interface conversion keeps this helper compatible with earlier ASTs.
	if call, ok := any(x).(*syntax.BashPPCall); ok {
		n.op = "call"
		if len(call.Fun) == 1 {
			n.children = append(n.children, &sharpNullExpr{op: "ident", name: call.Fun[0].Value, node: call.Fun[0]})
		}
		for _, word := range call.Args {
			n.children = append(n.children, sharpWordExpr(word))
		}
		return n
	}
	switch v := x.(type) {
	case *syntax.BashPPIdent:
		n.op, n.name = "ident", v.Name.Value
	case *syntax.BashPPParenExpr:
		return sharpExpr(v.X)
	case *syntax.BashPPBinaryExpr:
		n.op = v.Op.Value
		n.children = []*sharpNullExpr{sharpExpr(v.X), sharpExpr(v.Y)}
	case *syntax.BashPPDerefExpr:
		n.op = "deref"
		n.children = []*sharpNullExpr{sharpExpr(v.X)}
	case *syntax.BashPPAddressExpr:
		n.op = "new"
		n.children = []*sharpNullExpr{sharpExpr(v.X)}
	case *syntax.BashPPNewExpr:
		n.op = "new"
		if v.Init != nil {
			n.children = []*sharpNullExpr{sharpExpr(v.Init)}
		}
	case *syntax.BashPPIndexExpr:
		n.op = "index"
		n.children = []*sharpNullExpr{sharpExpr(v.X), sharpExpr(v.Index)}
	case *syntax.BashPPSelectorExpr:
		n.op = "selector"
		n.children = []*sharpNullExpr{sharpExpr(v.X)}
	case *syntax.BashPPUnaryExpr:
		n.op = v.Op.Value
		n.children = []*sharpNullExpr{sharpExpr(v.X)}
	case *syntax.BashPPCompositeLit:
		n.op = "new"
		for _, elem := range v.Elems {
			n.children = append(n.children, sharpExpr(elem.Key), sharpExpr(elem.Value))
		}
	case *syntax.BashPPSliceExpr:
		n.op = "slice"
		n.children = []*sharpNullExpr{sharpExpr(v.X), sharpExpr(v.Low), sharpExpr(v.High), sharpExpr(v.Max)}
	}
	return n
}
func sharpWordText(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var out strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			out.WriteString(p.Value)
		case *syntax.DblQuoted:
			var text strings.Builder
			for _, child := range p.Parts {
				lit, ok := child.(*syntax.Lit)
				if !ok {
					return "", false
				}
				text.WriteString(lit.Value)
			}
			out.WriteString(strconv.Quote(text.String()))
		case *syntax.SglQuoted:
			out.WriteString(strconv.Quote(p.Value))
		default:
			return "", false
		}
	}
	return out.String(), true
}
func sharpWordExpr(word *syntax.Word) *sharpNullExpr {
	text, ok := sharpWordText(word)
	if !ok {
		return nil
	}
	expr, err := parser.ParseExpr(text)
	if err != nil {
		return nil
	} // Shell expansions remain shell AST operations.
	var convert func(ast.Expr) *sharpNullExpr
	convert = func(x ast.Expr) *sharpNullExpr {
		if x == nil {
			return nil
		}
		n := &sharpNullExpr{node: word}
		switch v := x.(type) {
		case *ast.Ident:
			n.op, n.name = "ident", v.Name
		case *ast.ParenExpr:
			return convert(v.X)
		case *ast.BinaryExpr:
			n.op = v.Op.String()
			n.children = []*sharpNullExpr{convert(v.X), convert(v.Y)}
		case *ast.UnaryExpr:
			n.op = v.Op.String()
			if v.Op == token.AND {
				n.op = "new"
			}
			n.children = []*sharpNullExpr{convert(v.X)}
		case *ast.StarExpr:
			n.op = "deref"
			n.children = []*sharpNullExpr{convert(v.X)}
		case *ast.IndexExpr:
			n.op = "index"
			n.children = []*sharpNullExpr{convert(v.X), convert(v.Index)}
		case *ast.SelectorExpr:
			n.op = "selector"
			n.children = []*sharpNullExpr{convert(v.X)}
		case *ast.CallExpr:
			n.op = "call"
			n.children = append(n.children, convert(v.Fun))
			for _, arg := range v.Args {
				n.children = append(n.children, convert(arg))
			}
			if id, ok := v.Fun.(*ast.Ident); ok && (id.Name == "new" || id.Name == "make") {
				n.op = "new"
			}
		case *ast.CompositeLit, *ast.FuncLit:
			n.op = "new"
		}
		return n
	}
	return convert(expr)
}

type sharpNullValue struct {
	kind               string
	nonnil, reassigned bool
}
type sharpNullState map[string]sharpNullValue

func (s sharpNullState) clone() sharpNullState {
	out := sharpNullState{}
	for key, value := range s {
		out[key] = value
	}
	return out
}
func mergeSharpNull(a, b sharpNullState) sharpNullState {
	out := a.clone()
	for key, value := range out {
		other := b[key]
		value.nonnil = value.nonnil && other.nonnil
		value.reassigned = value.reassigned || other.reassigned
		out[key] = value
	}
	return out
}

type sharpNullChecker struct {
	initial     sharpNullState
	diagnostics ErrorList
	seen        map[string]bool
}

func newSharpNullChecker() *sharpNullChecker {
	return &sharpNullChecker{initial: sharpNullState{}, seen: map[string]bool{}}
}
func sharpNullableType(field *syntax.BashPPField) string {
	switch typ := field.FieldTypeExpr.(type) {
	case *syntax.BashPPPointerType:
		return "pointer"
	case *syntax.BashPPCollectionType:
		if typ.Kind == "slice" || typ.Kind == "map" {
			return "indexable"
		}
	}
	if field.FieldType != nil {
		text := field.FieldType.Value
		switch {
		case strings.HasPrefix(text, "*"):
			return "pointer"
		case strings.HasPrefix(text, "[]") || strings.HasPrefix(text, "map["):
			return "indexable"
		case strings.HasPrefix(text, "func("):
			return "callable"
		}
	}
	return ""
}
func (c *sharpNullChecker) parameters(fields []*syntax.BashPPField) {
	for _, field := range fields {
		for _, name := range field.Names {
			c.initial[name.Value] = sharpNullValue{kind: sharpNullableType(field)}
		}
	}
}
func (c *sharpNullChecker) add(n syntax.Node, code, message string) {
	key := fmt.Sprintf("%s:%d:%s", code, n.Pos().Offset(), message)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.diagnostics = append(c.diagnostics, Diagnostic{Code: code, Msg: message, Node: nodeName(n), Pos: n.Pos()})
}
func (c *sharpNullChecker) check(x *sharpNullExpr, state sharpNullState) {
	if x == nil {
		return
	}
	if (x.op == "&&" || x.op == "||") && len(x.children) == 2 {
		c.check(x.children[0], state)
		c.check(x.children[1], refineSharpNull(state, x.children[0], x.op == "&&"))
		return
	}
	if len(x.children) > 0 && x.children[0] != nil && x.children[0].op == "ident" {
		name := x.children[0].name
		value := state[name]
		if !value.nonnil {
			switch {
			case x.op == "deref" && value.kind == "pointer":
				message := name + " may be nil when dereferenced"
				if value.reassigned {
					message = name + " may be nil after reassignment"
				}
				c.add(x.node, "BASHPP-ENULL-DEREF", message)
			case x.op == "index" && value.kind == "indexable":
				c.add(x.node, "BASHPP-ENULL-INDEX", name+" may be nil when indexed")
			case x.op == "call" && value.kind == "callable":
				c.add(x.node, "BASHPP-ENULL-CALL", name+" may be nil when called")
			}
		}
	}
	for _, child := range x.children {
		c.check(child, state)
	}
}
func refineSharpNull(state sharpNullState, x *sharpNullExpr, truth bool) sharpNullState {
	out := state.clone()
	if x == nil {
		return out
	}
	if x.op == "!" && len(x.children) == 1 {
		return refineSharpNull(out, x.children[0], !truth)
	}
	if len(x.children) != 2 {
		return out
	}
	if (x.op == "&&" && truth) || (x.op == "||" && !truth) {
		return refineSharpNull(refineSharpNull(out, x.children[0], truth), x.children[1], truth)
	}
	if x.op != "==" && x.op != "!=" {
		return out
	}
	a, b := x.children[0], x.children[1]
	if a == nil || b == nil || a.op != "ident" || b.op != "ident" {
		return out
	}
	if a.name == "nil" {
		a, b = b, a
	}
	if b.name != "nil" {
		return out
	}
	value := out[a.name]
	value.nonnil = truth == (x.op == "!=")
	out[a.name] = value
	return out
}
func sharpDefinitelyNonNil(x *sharpNullExpr, state sharpNullState) bool {
	if x == nil {
		return false
	}
	if x.op == "new" {
		return true
	}
	return x.op == "ident" && state[x.name].nonnil
}
func (c *sharpNullChecker) block(block *syntax.Block, state sharpNullState) (sharpNullState, bool) {
	if block == nil {
		return state, false
	}
	current := state.clone()
	for _, stmt := range block.Stmts {
		var ends bool
		current, ends = c.command(stmt.Cmd, current)
		if ends {
			return current, true
		}
	}
	return current, false
}
func (c *sharpNullChecker) command(command syntax.Command, state sharpNullState) (sharpNullState, bool) {
	current := state.clone()
	check := func(expr syntax.BashPPExpr) { c.check(sharpExpr(expr), current) }
	word := func(value *syntax.Word) { c.check(sharpWordExpr(value), current) }
	switch n := command.(type) {
	case *syntax.BashPPReturn:
		for _, result := range n.Results {
			word(result)
		}
		return current, true
	case *syntax.BashPPIf:
		if n.InitStmt != nil {
			current, _ = c.command(n.InitStmt, current)
		} else if n.Init != nil {
			current, _ = c.command(n.Init, current)
		}
		condition := sharpExpr(n.Cond)
		c.check(condition, current)
		then, thenEnds := c.block(n.Then, refineSharpNull(current, condition, true))
		otherwise := refineSharpNull(current, condition, false)
		elseEnds := false
		if n.Else != nil {
			otherwise, elseEnds = c.command(n.Else, otherwise)
		}
		if thenEnds {
			return otherwise, elseEnds
		}
		if elseEnds {
			return then, false
		}
		return mergeSharpNull(then, otherwise), false
	case *syntax.Block:
		return c.block(n, current)
	case *syntax.BashPPDecl:
		if n.Kw.Value == "var" || n.Kw.Value == "const" {
			check(n.InitExpr)
			for _, value := range n.Init {
				word(value)
			}
			value := sharpExpr(n.InitExpr)
			if value == nil && len(n.Init) == 1 {
				value = sharpWordExpr(n.Init[0])
			}
			// The static proof obligation follows nullable parameters and aliases.
			// Local zero values retain checked runtime dereference semantics.
			state := sharpNullValue{nonnil: sharpDefinitelyNonNil(value, current)}
			if value != nil && value.op == "ident" {
				state.kind = current[value.name].kind
			}
			current[n.Name.Value] = state
		}
	case *syntax.BashPPShortDecl:
		check(n.Expr)
		for _, value := range n.Rhs {
			word(value)
		}
		value := sharpExpr(n.Expr)
		if value == nil && len(n.Rhs) == 1 {
			value = sharpWordExpr(n.Rhs[0])
		}
		for _, name := range n.Lhs {
			old := current[name.Value]
			old.nonnil = sharpDefinitelyNonNil(value, current)
			if value != nil && value.op == "ident" {
				old.kind = current[value.name].kind
			}
			current[name.Value] = old
		}
		if n.Call != nil {
			c.checkCall(n.Call, current)
		}
	case *syntax.BashPPAssign:
		check(n.TargetExpr)
		check(n.ValueExpr)
		word(n.Target)
		word(n.Value)
		for _, value := range n.ValueExprs {
			check(value)
		}
		for _, value := range n.Values {
			word(value)
		}
		value := sharpExpr(n.ValueExpr)
		if value == nil {
			value = sharpWordExpr(n.Value)
		}
		targets := names(n.Names)
		if len(targets) == 0 {
			if id, ok := n.TargetExpr.(*syntax.BashPPIdent); ok {
				targets = []string{id.Name.Value}
			} else if text, ok := sharpWordText(n.Target); ok && syntax.BashPPValidIdent(text) {
				targets = []string{text}
			}
		}
		for i, name := range targets {
			rhs := value
			if i < len(n.ValueExprs) {
				rhs = sharpExpr(n.ValueExprs[i])
			} else if i < len(n.Values) {
				rhs = sharpWordExpr(n.Values[i])
			}
			old := current[name]
			old.nonnil = sharpDefinitelyNonNil(rhs, state)
			old.reassigned = true
			current[name] = old
		}
	case *syntax.BashPPForAssign:
		check(n.Expr)
		old := current[n.Name.Value]
		old.nonnil = sharpDefinitelyNonNil(sharpExpr(n.Expr), state)
		old.reassigned = true
		current[n.Name.Value] = old
	case *syntax.BashPPCall:
		c.checkCall(n, current)
	case *syntax.BashPPDefer:
		c.checkCall(n.Call, current)
	case *syntax.BashPPFor:
		if n.Init != nil {
			current, _ = c.command(n.Init, current)
		}
		check(n.Cond)
		body, _ := c.block(n.Body, current)
		if n.Post != nil {
			body, _ = c.command(n.Post, body)
		}
		return mergeSharpNull(current, body), false
	case *syntax.BashPPSwitch:
		check(n.Tag)
		for _, arm := range n.Arms {
			for _, expr := range arm.Exprs {
				check(expr)
			}
			armState := current
			for _, stmt := range arm.Stmts {
				armState, _ = c.command(stmt.Cmd, armState)
			}
			current = mergeSharpNull(current, armState)
		}
	}
	return current, false
}
func (c *sharpNullChecker) checkCall(call *syntax.BashPPCall, state sharpNullState) {
	if call == nil {
		return
	}
	if len(call.Fun) == 1 {
		name := call.Fun[0].Value
		if state[name].kind == "callable" && !state[name].nonnil {
			c.add(call, "BASHPP-ENULL-CALL", name+" may be nil when called")
		}
	}
	for _, arg := range call.Args {
		c.check(sharpWordExpr(arg), state)
	}
}
