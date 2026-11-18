package lower

import (
	"go/ast"
	"go/parser"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// ReadonlyContext carries the runtime state expression, never a package-global
// variable, a thread local or a goroutine-id lookup. State evaluates to the
// *ReadonlyState of the enclosing program or task — `program.Readonly` for a
// program built by the runtime's Program helper.
//
// There is no failure sink here. A guard failure unwinds with its typed error
// through MustReadonly, so deferred code runs and the program boundary reports
// the diagnostic once and exits with the error's own status.
type ReadonlyContext struct{ State string }

// ReadonlyMutation describes one guarded container update. Kind is the
// interpreter's diagnostic vocabulary for the mutated container: "field" when
// the final path element is a field, "map" when the parent is a map, and
// "slice" for every other indexed parent, arrays included. Value is the
// already-emitted right-hand side; it is emitted exactly once and evaluated by
// the update, after the guard, which is what the interpreter does.
type ReadonlyMutation struct{ Kind, Value string }

// ReadonlyBuiltin describes one guarded collection builtin call. Target is the
// emitted first argument, captured exactly once so the guard checks the same
// container the builtin mutates. Args are the remaining arguments in source
// order; ArgTypes, when supplied, is parallel to Args and gives the type an
// argument must be captured with. Root is the source spelling of the root
// binding and Address its actual binding address, defaulting to the root's own
// address. Spread marks the append(s, more...) form, whose element count is
// only known at runtime.
type ReadonlyBuiltin struct {
	Name, Root, Address, Target string
	Args, ArgTypes              []string
	Spread                      bool
}

func (e *emitter) readonlyContext(n syntax.Node, c ReadonlyContext) error {
	// No default is possible here: a fallback would reintroduce exactly the
	// global state this contract forbids.
	if c.State == "" {
		return e.fail(n, CodeBridge, "readonly lowering requires an explicit runtime state expression")
	}
	e.bridge = true
	return nil
}

// readonlyGuard reports one runtime check through the typed unwind.
func (e *emitter) readonlyGuard(check string) string {
	return e.prefix + "rt.MustReadonly(" + check + ")\n"
}

// readonlyDeclare marks each named binding by address. Marking records runtime
// locations, so the compiler must pass the binding itself and not a copy.
func (e *emitter) readonlyDeclare(n *syntax.DeclClause, c ReadonlyContext) (string, error) {
	if err := e.readonlyContext(n, c); err != nil {
		return "", err
	}
	if n.Variant == nil || n.Variant.Value != "readonly" {
		return "", e.fail(n, CodeUnsupported, "declaration variant is not readonly")
	}
	if len(n.Args) == 0 {
		return "", e.fail(n, CodeUnsupported, "readonly without names reports state rather than marking it")
	}
	var out strings.Builder
	for _, arg := range n.Args {
		if arg == nil || arg.Name == nil {
			return "", e.fail(n, CodeExpr, "readonly name is missing")
		}
		if arg.Value != nil || arg.Array != nil || arg.Index != nil || arg.Append {
			return "", e.fail(arg, CodeUnsupported, "readonly with an initializer needs the compiler's declaration path")
		}
		name := arg.Name.Value
		if !e.known(name) {
			return "", e.fail(arg, CodeUndefined, "undefined: "+name)
		}
		out.WriteString(e.readonlyGuard(c.State + ".Mark(" + strconv.Quote(name) + ", &" + e.goName(name) + ")"))
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// readonlyRebind guards the binding location itself. Rebinding a distinct alias
// is not mutation of the shared object, so the check takes the binding address
// and nothing else, and the value is left in the assignment: the interpreter
// also refuses the rebinding before evaluating its right-hand side.
func (e *emitter) readonlyRebind(n syntax.Node, c ReadonlyContext, name, value string) (string, error) {
	if err := e.readonlyContext(n, c); err != nil {
		return "", err
	}
	if name == "" || value == "" {
		return "", e.fail(n, CodeExpr, "readonly rebinding needs a binding name and an emitted value")
	}
	if !e.known(name) {
		return "", e.fail(n, CodeUndefined, "undefined: "+name)
	}
	return e.readonlyGuard(c.State+".CheckAssign(&"+e.goName(name)+")") + e.goName(name) + " = " + value, nil
}

type readonlyStep struct {
	field string
	index syntax.BashPPExpr
}

// readonlyTarget is a mutation path split into its root and its positioned
// source text. Nothing is hoisted: the guard resolves the root binding's own
// identity, so the update stays one ordinary Go statement whose operands are
// evaluated once, left to right, exactly as Go and the interpreter order them.
type readonlyTarget struct {
	root, source, field string
	indexed             bool
}

func (e *emitter) readonlyPath(x syntax.BashPPExpr) (*readonlyTarget, error) {
	var steps []readonlyStep
	current := x
	for {
		switch n := current.(type) {
		case *syntax.BashPPSelectorExpr:
			if n.Sel == nil {
				return nil, e.fail(n, CodeExpr, "mutation path field is missing")
			}
			steps = append(steps, readonlyStep{field: n.Sel.Value})
			current = n.X
		case *syntax.BashPPIndexExpr:
			if n.Index == nil {
				return nil, e.fail(n, CodeExpr, "mutation path index is missing")
			}
			steps = append(steps, readonlyStep{index: n.Index})
			current = n.X
		case *syntax.BashPPParenExpr:
			current = n.X
		case *syntax.BashPPIdent:
			if n.Name == nil {
				return nil, e.fail(n, CodeExpr, "mutation path root is missing")
			}
			if !e.known(n.Name.Value) {
				return nil, e.fail(n, CodeUndefined, "undefined: "+n.Name.Value)
			}
			target := &readonlyTarget{root: n.Name.Value}
			for i := len(steps) - 1; i >= 0; i-- {
				if step := steps[i]; step.index == nil {
					target.indexed, target.field = false, "."+step.field
					target.source += target.field
				} else {
					target.indexed = true
					target.source += "[" + readonlyExprText(step.index) + "]"
				}
			}
			return target, nil
		default:
			return nil, e.fail(x, CodeUnsupported, "mutation path is not a rooted field/index chain: "+nodeName(x))
		}
	}
}

// readonlyExprText reproduces the interpreter's positioned path spelling for
// the vocabulary a mutation path can contain. It is diagnostic text only.
func readonlyExprText(x syntax.BashPPExpr) string {
	switch n := x.(type) {
	case *syntax.BashPPBasicLit:
		if n.Value == nil {
			return "?"
		}
		return n.Value.Value
	case *syntax.BashPPIdent:
		if n.Name == nil {
			return "?"
		}
		return n.Name.Value
	case *syntax.BashPPSelectorExpr:
		if n.Sel == nil {
			return "?"
		}
		return readonlyExprText(n.X) + "." + n.Sel.Value
	case *syntax.BashPPIndexExpr:
		return readonlyExprText(n.X) + "[" + readonlyExprText(n.Index) + "]"
	case *syntax.BashPPParenExpr:
		return "(" + readonlyExprText(n.X) + ")"
	case *syntax.BashPPUnaryExpr:
		if n.Op == nil {
			return "?"
		}
		return n.Op.Value + readonlyExprText(n.X)
	case *syntax.BashPPBinaryExpr:
		if n.Op == nil {
			return "?"
		}
		return readonlyExprText(n.X) + " " + n.Op.Value + " " + readonlyExprText(n.Y)
	}
	return "?"
}

// readonlyMutation guards a container update. The check resolves the root
// binding's address and its own value, so it needs no part of the path
// evaluated: a readonly slice index out of range, a readonly nil map and a
// readonly array element all report the readonly diagnostic rather than the
// bounds, nil-map or panic they would otherwise raise, which is what the
// interpreter does. Because the root's value carries the identity, a map, slice
// or pointer alias resolves to the marked owner, and an overlapping subslice
// resolves through the recorded region.
func (e *emitter) readonlyMutation(target syntax.BashPPExpr, c ReadonlyContext, m ReadonlyMutation) (string, error) {
	if err := e.readonlyContext(target, c); err != nil {
		return "", err
	}
	switch m.Kind {
	case "field", "map", "slice":
	default:
		return "", e.fail(target, CodeType, "readonly mutation kind must be field, map or slice")
	}
	if m.Value == "" {
		return "", e.fail(target, CodeExpr, "readonly mutation needs an emitted value expression")
	}
	path, err := e.readonlyPath(target)
	if err != nil {
		return "", err
	}
	if path.source == "" {
		return "", e.fail(target, CodeExpr, "readonly mutation target is a whole binding; use the rebinding guard")
	}
	if path.indexed == (m.Kind == "field") {
		return "", e.fail(target, CodeType, "readonly mutation kind does not match the final path element")
	}
	// A field diagnostic names only the final selector, an indexed one the whole
	// path; the interpreter's wording distinguishes them that way.
	label := path.source
	if m.Kind == "field" {
		label = path.field
	}
	update, err := e.expr(target)
	if err != nil {
		return "", err
	}
	root := e.goName(path.root)
	check := c.State + ".CheckMutation(" + strconv.Quote(path.root) + ", &" + root + ", " + root +
		", " + strconv.Quote(label) + ", " + strconv.Quote(m.Kind) + ")"
	return e.readonlyGuard(check) + update + " = " + m.Value, nil
}

// readonlyPure reports whether an emitted operand may stay in the call. Such an
// operand has no side effects and cannot panic, so whether it is evaluated
// before or after the guard is unobservable — and hoisting it would be actively
// wrong when it is an untyped constant, since a bare temp takes its default
// type. Anything else is hoisted, which is safe because it already has a type.
func (e *emitter) readonlyPure(text string) bool {
	x, err := parser.ParseExpr(text)
	if err != nil {
		return false
	}
	var pure func(ast.Expr) bool
	pure = func(a ast.Expr) bool {
		switch q := a.(type) {
		case *ast.BasicLit, *ast.Ident:
			return true
		case *ast.ParenExpr:
			return pure(q.X)
		case *ast.UnaryExpr:
			return q.Op.String() != "<-" && pure(q.X)
		case *ast.BinaryExpr:
			return pure(q.X) && pure(q.Y)
		case *ast.SelectorExpr:
			// A package-qualified name may be an untyped constant, and reading it
			// cannot panic. A field selector can, so it is hoisted instead.
			base, ok := q.X.(*ast.Ident)
			return ok && e.imports[base.Name] != ""
		}
		return false
	}
	return pure(x)
}

// readonlyBuiltin guards append, copy, delete and clear. Every operand is
// captured before the guard, in source order, because the interpreter evaluates
// a builtin's arguments before it checks the target — a readonly delete with an
// ill-typed key reports the readonly failure, not the key. The call is returned
// as an expression so append keeps its value form. append is guarded only when
// the new elements fit in the existing capacity, because only then does it write
// into the readonly backing array; a growing append allocates and mutates
// nothing, and both implementations allow it.
func (e *emitter) readonlyBuiltin(n syntax.Node, c ReadonlyContext, b ReadonlyBuiltin) (string, string, error) {
	if err := e.readonlyContext(n, c); err != nil {
		return "", "", err
	}
	extra := len(b.Args)
	switch b.Name {
	case "clear":
		if extra != 0 {
			return "", "", e.fail(n, CodeResult, "clear takes one collection")
		}
	case "delete":
		if extra != 1 {
			return "", "", e.fail(n, CodeResult, "delete takes a map and one key")
		}
	case "copy":
		if extra != 1 {
			return "", "", e.fail(n, CodeResult, "copy takes a destination and a source")
		}
	case "append":
	default:
		return "", "", e.fail(n, CodeUnsupported, "collection builtin is not guarded: "+b.Name)
	}
	if b.Spread && (b.Name != "append" || extra != 1) {
		return "", "", e.fail(n, CodeResult, "the spread form takes one spread argument after the slice")
	}
	if len(b.ArgTypes) > extra {
		return "", "", e.fail(n, CodeType, "readonly builtin has more argument types than arguments")
	}
	if b.Target == "" || b.Root == "" {
		return "", "", e.fail(n, CodeExpr, "readonly builtin needs an emitted target and its root binding")
	}
	if !e.known(b.Root) {
		return "", "", e.fail(n, CodeUndefined, "undefined: "+b.Root)
	}
	address := b.Address
	if address == "" {
		address = "&" + e.goName(b.Root)
	}
	target := e.prefix + "readonlyTarget"
	var setup strings.Builder
	setup.WriteString(target + " := " + b.Target + "\n")
	arguments := []string{target}
	for i, arg := range b.Args {
		typ := ""
		if i < len(b.ArgTypes) {
			typ = b.ArgTypes[i]
		}
		value := arg
		switch {
		case typ != "":
			// An explicit type is the only way to capture an untyped constant
			// without changing which type it takes.
			value = e.prefix + "readonlyArg" + strconv.Itoa(i)
			setup.WriteString("var " + value + " " + typ + " = " + arg + "\n")
		case !e.readonlyPure(arg):
			value = e.prefix + "readonlyArg" + strconv.Itoa(i)
			setup.WriteString(value + " := " + arg + "\n")
		}
		arguments = append(arguments, value)
	}
	guard := e.readonlyGuard(c.State + ".CheckBuiltin(" + address + ", " + target + ", " + strconv.Quote(b.Name) + ")")
	if b.Name == "append" {
		count := strconv.Itoa(extra)
		if b.Spread {
			count = "len(" + arguments[1] + ")"
			arguments[1] += "..."
		}
		switch {
		case b.Spread:
			guard = "if " + count + " > 0 && len(" + target + ")+" + count + " <= cap(" + target + ") {\n" + guard + "}\n"
		case extra == 0:
			// append(s) adds nothing and writes nothing.
			guard = ""
		default:
			guard = "if len(" + target + ")+" + count + " <= cap(" + target + ") {\n" + guard + "}\n"
		}
	}
	return setup.String() + guard, b.Name + "(" + strings.Join(arguments, ", ") + ")", nil
}
