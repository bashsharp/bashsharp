package lower

import (
	"fmt"
	"go/constant"
	"go/token"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// This file implements the structural source-profile check: the statically
// decidable rejections of the public Go profile, reported with the SOURCE
// diagnostic identities rather than the lowering codes in api.go.
//
// It exists because the lowering pass reports go/types' own wording under
// LOWER-ETYPE, which is a different sentence, a different code, and a different
// position from what the source surface prints. Rewriting go/types messages
// into source messages would be a string substitution that happens to pass the
// corpus; every rule below is instead a structural predicate over the
// positioned tree — scope, type and method registries plus exact constant
// evaluation — so a renamed variable, a renamed constant or a reordered
// declaration reaches the same verdict.
//
// Nothing here executes, expands, imports or compiles anything.

// Source diagnostic codes. These are the identities the interpreter prints, and
// are deliberately separate from the LOWER-E* lowering codes: a consumer must
// be able to tell a source rejection from a lowering limitation.
const (
	CodeProfileAssertImpossible  = "BASHPP-EASSERT-IMPOSSIBLE"
	CodeProfileAssignType        = "BASHPP-EASSIGN-TYPE"
	CodeProfileBuiltinType       = "BASHPP-EBUILTIN-TYPE"
	CodeProfileExprConvert       = "BASHPP-EEXPR-CONVERT"
	CodeProfileForCond           = "BASHPP-EFOR-COND"
	CodeProfileGenericArity      = "BASHPP-EGENERIC-ARITY"
	CodeProfileInterfaceGeneric  = "BASHPP-EINTERFACE-GENERIC"
	CodeProfileGenericParam      = "BASHPP-EGENERIC-PARAM"
	CodeProfileGenericConstraint = "BASHPP-EGENERIC-CONSTRAINT"
	CodeProfileGenericInfer      = "BASHPP-EGENERIC-INFER"
	CodeProfileIfCond            = "BASHPP-EIF-COND"
	CodeProfileInterfaceMissing  = "BASHPP-EINTERFACE-MISSING"
	CodeProfileInterfaceTypeSet  = "BASHPP-EINTERFACE-TYPESET"
	CodeProfileRangeArity        = "BASHPP-ERANGE-ARITY"
	CodeProfileShortNoNew        = "BASHPP-ESHORT-NONEW"
	CodeProfileStructKey         = "BASHPP-ESTRUCT-KEY"
	CodeProfileStructMixed       = "BASHPP-ESTRUCT-MIXED"
	CodeProfileSwitchType        = "BASHPP-ESWITCH-TYPE"
)

// ProfileMethod is one declared method of a session type. Pointer records a
// pointer receiver, which keeps the method out of the value method set.
type ProfileMethod struct {
	Name    string
	Pointer bool
}

// ProfileFacts supplies declarations established earlier in the same session,
// which a single positioned file cannot carry on its own. A nil *ProfileFacts
// means the file being checked is the whole session.
//
// Facts are merged UNDER the file: a declaration in the file wins, so a caller
// cannot make the checker reject a program by supplying stale facts.
type ProfileFacts struct {
	// Types maps each declared type name to its underlying type expression.
	// A nil value records that only the name is known, which is enough to keep
	// the checker quiet about it.
	Types map[string]syntax.BashPPTypeExpr
	// Aliases names the subset of Types declared with the `=` alias form.
	Aliases map[string]bool
	// Methods maps a receiver type name to its declared methods, in order.
	Methods map[string][]ProfileMethod
}

// CheckProfile reports the statically decidable source rejections in file.
//
// origin is the source name used for the `<origin>: line N: ` prefix that some
// source diagnostics carry; an empty origin renders "bash", matching the
// interpreter's own fallback. The result is nil when the file is not
// statically rejectable, and is otherwise ordered as the interpreter emits —
// NOT by position, because a secondary declaration diagnostic can sit at a
// smaller column than the operand diagnostic that precedes it.
func CheckProfile(file *syntax.File, origin string) ErrorList {
	return CheckProfileWithFacts(file, origin, nil)
}

// CheckProfileWithFacts is CheckProfile with session declarations supplied
// explicitly. See ProfileFacts.
func CheckProfileWithFacts(file *syntax.File, origin string, facts *ProfileFacts) ErrorList {
	// Ordinary Go is checked by go/types during ingestion and after emission.
	// Bash++ profile restrictions (including receiver registries) do not apply.
	if file == nil || file.GoSource {
		return nil
	}
	c := &profileChecker{
		origin:      origin,
		types:       map[string]*profileType{},
		methods:     map[string][]ProfileMethod{},
		methodDecls: map[string]map[string]*syntax.BashPPFuncDecl{},
		funcs:       map[string]*syntax.BashPPFuncDecl{},
		volatile:    map[string]bool{},
	}
	c.collectFacts(facts)
	c.collectFile(file)
	c.collectVolatile(file)
	c.push()
	c.stmts(file.Stmts)
	c.pop()
	if len(c.out) == 0 {
		return nil
	}
	return c.out
}

type profileType struct {
	name       string
	alias      bool
	underlying syntax.BashPPTypeExpr
}

// profileBinding is what the checker knows about one name in scope. Every
// field is optional: an entry with nothing known still records that the name
// is declared, which is what the short-declaration rule needs.
type profileBinding struct {
	typeExpr syntax.BashPPTypeExpr // the declared type, when written or derivable
	value    constant.Value        // the static constant value, when decidable
	typ      string                // named type of a typed constant, else ""
}

// profileScalar mirrors the interpreter's scalar: a constant value plus the
// named type it was declared with, empty for an untyped constant.
type profileScalar struct {
	value constant.Value
	typ   string
}

type profileChecker struct {
	origin      string
	types       map[string]*profileType
	methods     map[string][]ProfileMethod
	methodDecls map[string]map[string]*syntax.BashPPFuncDecl
	funcs       map[string]*syntax.BashPPFuncDecl
	scopes      []map[string]*profileBinding
	// volatile names every identifier the file ever assigns, updates or takes
	// the address of. See collectVolatile.
	volatile map[string]bool
	out      ErrorList
}

// done reports whether a diagnostic has already been produced. The interpreter
// exits with status 2 at its first source rejection, so everything after it is
// unreached and must not be reported.
func (c *profileChecker) done() bool { return len(c.out) > 0 }

func (c *profileChecker) emit(code, node string, pos syntax.Pos, prefixed bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	text := msg
	if code != "" {
		text = code + ": " + msg
	}
	if prefixed {
		name := c.origin
		if name == "" {
			name = "bash"
		}
		text = fmt.Sprintf("%s: line %d: %s", name, pos.Line(), text)
	}
	c.out = append(c.out, Diagnostic{Code: code, Msg: msg, Node: node, Pos: pos, Text: text})
}

// --- registries -------------------------------------------------------------

func (c *profileChecker) collectFacts(facts *ProfileFacts) {
	if facts == nil {
		return
	}
	for name, underlying := range facts.Types {
		c.types[name] = &profileType{name: name, alias: facts.Aliases[name], underlying: underlying}
	}
	for recv, list := range facts.Methods {
		c.methods[recv] = append(c.methods[recv], list...)
	}
}

// collectFile registers every type, method and function in the file before the
// statement walk. Declarations are hoisted deliberately: a function body may
// legally mention a type declared textually after it, because the body does not
// run until the call, and rejecting that would be a false positive.
func (c *profileChecker) collectFile(file *syntax.File) {
	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.BashPPDecl:
			if x.Kw != nil && x.Kw.Value == "type" && x.Name != nil {
				underlying := x.DeclTypeExpr
				if underlying == nil && len(x.StructFields) > 0 {
					underlying = &syntax.BashPPStructType{Fields: x.StructFields}
				}
				c.types[x.Name.Value] = &profileType{name: x.Name.Value, alias: x.Alias, underlying: underlying}
			}
		case *syntax.BashPPFuncDecl:
			if x.Name == nil {
				break
			}
			if x.Receiver == nil {
				c.funcs[x.Name.Value] = x
				break
			}
			if x.Receiver.RecvType != nil {
				recv := x.Receiver.RecvType.Value
				if c.methodDecls[recv] == nil {
					c.methodDecls[recv] = map[string]*syntax.BashPPFuncDecl{}
				}
				c.methodDecls[recv][x.Name.Value] = x
				c.methods[recv] = append(c.methods[recv], ProfileMethod{Name: x.Name.Value, Pointer: x.Receiver.Pointer})
			}
		}
		return true
	})
}

// collectVolatile records every identifier the file mutates or exposes to
// mutation, so that no constant fact is ever derived from one.
//
// An initializer is not a constant: `n := 128; n = 1` leaves n holding 1, and
// `p := &n; *p = 1` does the same through an alias the assignment statement
// never names. Deciding which of those writes reaches which read is flow and
// alias analysis, which this checker does not do — so the sound answer is to
// treat a name as a constant only when the file never writes to it and never
// takes its address at all. The scan is name-based and file-wide on purpose:
// it over-approximates, which costs diagnostics rather than working programs.
func (c *profileChecker) collectVolatile(file *syntax.File) {
	mark := func(name string) {
		if name != "" && name != "_" {
			c.volatile[name] = true
		}
	}
	markExpr := func(e syntax.BashPPExpr) { mark(rootIdent(e)) }
	markWord := func(w *syntax.Word) { mark(rootWordName(w)) }
	syntax.Walk(file, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.BashPPAssign:
			for _, name := range x.Names {
				mark(name.Value)
			}
			markExpr(x.TargetExpr)
			markWord(x.Target)
		case *syntax.BashPPForAssign:
			mark(x.Name.Value)
		case *syntax.BashPPUpdate:
			markExpr(x.Target)
			markWord(x.TargetWord)
		case *syntax.BashPPIncDec:
			if x.Name != nil {
				mark(x.Name.Value)
			}
			markExpr(x.Target)
			markWord(x.TargetWord)
		case *syntax.BashPPAddressExpr:
			// Taking the address admits a write this scan cannot see.
			markExpr(x.X)
		case *syntax.BashPPRange:
			for _, name := range x.Names {
				mark(name.Value)
			}
		case *syntax.Assign:
			// An ordinary shell assignment writes the same cell.
			if !x.Naked && x.Name != nil {
				mark(x.Name.Value)
			}
		case *syntax.WordIter:
			if x.Name != nil {
				mark(x.Name.Value)
			}
		}
		return true
	})
}

// rootIdent unwraps a path expression to the name it is rooted at.
func rootIdent(e syntax.BashPPExpr) string {
	for {
		switch x := e.(type) {
		case *syntax.BashPPIdent:
			return x.Name.Value
		case *syntax.BashPPParenExpr:
			e = x.X
		case *syntax.BashPPDerefExpr:
			e = x.X
		case *syntax.BashPPAddressExpr:
			e = x.X
		case *syntax.BashPPIndexExpr:
			e = x.X
		case *syntax.BashPPSelectorExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// rootWordName recovers the name a target word is rooted at, for the older
// assignment surface that keeps its target as source text.
func rootWordName(w *syntax.Word) string {
	if w == nil || len(w.Parts) == 0 {
		return ""
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok {
		return ""
	}
	text := strings.TrimLeft(lit.Value, "*&")
	if i := strings.IndexAny(text, ".[("); i >= 0 {
		text = text[:i]
	}
	if !syntax.BashPPValidIdent(text) {
		return ""
	}
	return text
}

// --- scopes -----------------------------------------------------------------

func (c *profileChecker) push() { c.scopes = append(c.scopes, map[string]*profileBinding{}) }

func (c *profileChecker) pop() {
	if len(c.scopes) > 0 {
		c.scopes = c.scopes[:len(c.scopes)-1]
	}
}

func (c *profileChecker) declare(name string, b *profileBinding) {
	if name == "" || name == "_" || len(c.scopes) == 0 {
		return
	}
	if b == nil {
		b = &profileBinding{}
	}
	if b.value != nil && c.volatile[name] {
		// The declared type survives — assignment cannot change it — but the
		// initializer's value does not.
		b = &profileBinding{typeExpr: b.typeExpr}
	}
	c.scopes[len(c.scopes)-1][name] = b
}

// declaredHere reports whether name is bound in the innermost scope. Go's
// short-declaration rule is about THIS block, not the enclosing chain: an inner
// block redeclaring an outer name is ordinary shadowing.
func (c *profileChecker) declaredHere(name string) bool {
	if len(c.scopes) == 0 {
		return false
	}
	_, ok := c.scopes[len(c.scopes)-1][name]
	return ok
}

func (c *profileChecker) lookup(name string) *profileBinding {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if b, ok := c.scopes[i][name]; ok {
			return b
		}
	}
	return nil
}

// --- statement walk ---------------------------------------------------------

func (c *profileChecker) stmts(list []*syntax.Stmt) {
	for _, s := range list {
		if c.done() {
			return
		}
		if s != nil {
			c.cmd(s.Cmd)
		}
	}
}

func (c *profileChecker) block(b *syntax.Block) {
	if b == nil {
		return
	}
	c.push()
	c.stmts(b.Stmts)
	c.pop()
}

func (c *profileChecker) cmd(cmd syntax.Command) {
	if cmd == nil || c.done() {
		return
	}
	switch x := cmd.(type) {
	case *syntax.Block:
		c.block(x)
	case *syntax.Subshell:
		c.push()
		c.stmts(x.Stmts)
		c.pop()
	case *syntax.BashPPFuncDecl:
		c.funcDecl(x)
	case *syntax.BashPPShortDecl:
		c.shortDecl(x)
	case *syntax.BashPPDecl:
		c.decl(x)
	case *syntax.BashPPConstGroup:
		c.constGroup(x)
	case *syntax.BashPPIf:
		c.ifStmt(x)
	case *syntax.BashPPFor:
		c.forStmt(x)
	case *syntax.BashPPRange:
		c.rangeStmt(x)
	case *syntax.BashPPSwitch:
		c.switchStmt(x)
	case *syntax.BashPPCall:
		c.call(x)
	case *syntax.BashPPCommandCall:
		c.call(x.Call)
	case *syntax.BashPPAssign:
		c.expr(x.ValueExpr)
		for _, v := range x.ValueExprs {
			c.expr(v)
		}
	case *syntax.BashPPUpdate:
		c.expr(x.Value)
	case *syntax.BashPPReturn:
		c.expr(x.Expr)
		if x.Call != nil {
			c.call(x.Call)
		}
	case *syntax.BashPPDefer:
		if x.Call != nil {
			c.call(x.Call)
		}
	case *syntax.BashPPGo:
		if x.Call != nil {
			c.call(x.Call)
		}
	case *syntax.BashPPSend:
	case *syntax.IfClause:
		// Else is a typed *IfClause, so it must be nil-checked before it is
		// widened into the Command interface.
		for at := x; at != nil; at = at.Else {
			c.stmts(at.Cond)
			c.push()
			c.stmts(at.Then)
			c.pop()
		}
	case *syntax.WhileClause:
		c.stmts(x.Cond)
		c.push()
		c.stmts(x.Do)
		c.pop()
	case *syntax.ForClause:
		c.push()
		c.stmts(x.Do)
		c.pop()
	case *syntax.CaseClause:
		for _, item := range x.Items {
			c.push()
			c.stmts(item.Stmts)
			c.pop()
		}
	case *syntax.BinaryCmd:
		if x.X != nil {
			c.cmd(x.X.Cmd)
		}
		if x.Y != nil {
			c.cmd(x.Y.Cmd)
		}
	case *syntax.FuncDecl:
		if x.Body != nil {
			c.cmd(x.Body.Cmd)
		}
	}
}

func (c *profileChecker) funcDecl(d *syntax.BashPPFuncDecl) {
	if c.checkTypeParams(d.TypeParams) {
		return
	}
	// A method's receiver type must be declared. This is the one source
	// diagnostic in the certified slice that carries no BASHPP code, so it is
	// reported with an empty Code and its exact legacy text.
	if recv := d.Receiver; recv != nil && recv.RecvType != nil {
		if _, ok := c.types[recv.RecvType.Value]; !ok {
			c.emit("", "BashPPReceiver", recv.RecvType.Pos(), false,
				"invalid receiver type %s (type is not declared in this session)", recv.RecvType.Value)
			return
		}
	}
	c.push()
	if d.Receiver != nil && d.Receiver.Name != nil && d.Receiver.RecvType != nil {
		typ := syntax.BashPPTypeExpr(&syntax.BashPPNamedType{Name: d.Receiver.RecvType})
		if d.Receiver.Pointer {
			typ = &syntax.BashPPPointerType{Element: typ}
		}
		c.declare(d.Receiver.Name.Value, &profileBinding{typeExpr: typ})
	}
	c.bindFields(d.Params)
	c.bindFields(d.Results)
	if d.Body != nil {
		c.stmts(d.Body.Stmts)
	}
	c.pop()
}

func (c *profileChecker) bindFields(fields []*syntax.BashPPField) {
	for _, f := range fields {
		if f == nil {
			continue
		}
		for _, name := range f.Names {
			c.declare(name.Value, &profileBinding{typeExpr: f.FieldTypeExpr})
		}
	}
}

// --- short declarations -----------------------------------------------------

func (c *profileChecker) shortDecl(d *syntax.BashPPShortDecl) {
	// The interpreter evaluates the right-hand side first, then settles the
	// declaration. Both can report, and in that order: `_ := cap(1)` prints the
	// operand diagnostic and then the blank-binding one.
	failed := c.shortDeclValue(d)

	expected := 0
	newName := false
	for _, lhs := range d.Lhs {
		if lhs == nil || lhs.Value == "_" {
			continue
		}
		expected++
		if !c.declaredHere(lhs.Value) {
			newName = true
		}
	}
	// A right-hand side that failed leaves the names unbound, and an incomplete
	// binding suppresses the declaration-form report — unless there was nothing
	// to bind, which is exactly the blank-only spelling.
	if failed && expected > 0 {
		return
	}
	if !newName && len(d.Lhs) > 0 {
		c.emit(CodeProfileShortNoNew, "BashPPShortDecl", d.Pos(), true,
			"no new variables on left side of :=")
	}
	if c.done() {
		return
	}
	c.bindShortDecl(d)
}

// shortDeclValue checks the right-hand side and reports whether it was
// rejected.
func (c *profileChecker) shortDeclValue(d *syntax.BashPPShortDecl) bool {
	if c.checkGenericMethodValue(d.Expr) {
		return true
	}
	before := len(c.out)
	if d.Call != nil {
		c.call(d.Call)
	}
	c.expr(d.Expr)
	if d.FuncLit != nil && d.FuncLit.Body != nil {
		c.push()
		c.bindFields(d.FuncLit.Params)
		c.bindFields(d.FuncLit.Results)
		c.stmts(d.FuncLit.Body.Stmts)
		c.pop()
	}
	return len(c.out) > before
}

func (c *profileChecker) bindShortDecl(d *syntax.BashPPShortDecl) {
	// A single name bound to a single understood value keeps that value; every
	// other spelling declares the names with nothing known about them, which is
	// what silences the rules that need a decided type.
	if len(d.Lhs) == 1 && d.Call == nil && d.FuncLit == nil && d.MakeChan == nil && d.Recv == nil {
		if b, ok := c.bindingForShortValue(d); ok {
			c.declare(d.Lhs[0].Value, b)
			return
		}
	}
	for _, lhs := range d.Lhs {
		c.declare(lhs.Value, &profileBinding{})
	}
}

func (c *profileChecker) bindingForShortValue(d *syntax.BashPPShortDecl) (*profileBinding, bool) {
	if d.Expr != nil {
		if addr, ok := d.Expr.(*syntax.BashPPAddressExpr); ok {
			if id, ok := addr.X.(*syntax.BashPPIdent); ok {
				if b := c.lookup(id.Name.Value); b != nil && b.typeExpr != nil {
					return &profileBinding{typeExpr: &syntax.BashPPPointerType{Element: b.typeExpr}}, true
				}
			}
			return nil, false
		}
		if s, ok := c.eval(d.Expr); ok {
			return &profileBinding{value: s.value, typ: s.typ, typeExpr: namedTypeExpr(s.typ)}, true
		}
		return nil, false
	}
	if len(d.Rhs) == 1 {
		if s, ok := c.wordScalar(d.Rhs[0]); ok {
			return &profileBinding{value: s.value, typ: s.typ, typeExpr: namedTypeExpr(s.typ)}, true
		}
	}
	return nil, false
}

func namedTypeExpr(name string) syntax.BashPPTypeExpr {
	if name == "" {
		return nil
	}
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
}

// --- var / const declarations -----------------------------------------------

func (c *profileChecker) decl(d *syntax.BashPPDecl) {
	if d.Kw == nil {
		return
	}
	if d.Kw.Value == "type" {
		c.typeDecl(d)
		return
	}
	if d.Site == syntax.StartVar && d.DeclTypeExpr != nil && c.checkValueType(d.DeclTypeExpr, d.Pos()) {
		return
	}
	c.expr(d.InitExpr)
	if c.done() {
		return
	}
	if d.DeclTypeExpr != nil && d.InitExpr != nil {
		if c.checkInterfaceAssign(d.DeclTypeExpr, d.InitExpr) {
			return
		}
		if c.checkScalarDeclValue(d.DeclTypeExpr, d.InitExpr, d.InitExpr.Pos()) {
			return
		}
	}
	if d.Name != nil {
		b := &profileBinding{typeExpr: d.DeclTypeExpr}
		if d.InitExpr != nil {
			if s, ok := c.eval(d.InitExpr); ok {
				b.value, b.typ = s.value, s.typ
			}
		}
		if base := namedBase(d.DeclTypeExpr); base != "" && b.value != nil {
			b.typ = base
		}
		c.declare(d.Name.Value, b)
	}
}

// checkScalarDeclValue applies Go's assignability and representability rules to
// a declaration with a written scalar type and a decidable constant
// initializer. It reports whether it produced a diagnostic.
func (c *profileChecker) checkScalarDeclValue(declType syntax.BashPPTypeExpr, init syntax.BashPPExpr, pos syntax.Pos) bool {
	base := namedBase(declType)
	if !profileScalarBase(base) {
		return false
	}
	value, ok := c.eval(init)
	if !ok || value.typ != "" {
		// A typed operand is an assignability question between two named
		// types, which is not part of this certified slice.
		return false
	}
	if !profileUntypedAssignable(base, value.value) {
		c.emit(CodeProfileAssignType, "BashPPDecl", pos, true,
			"cannot use %s constant as %s in declaration", value.value.Kind(), profileTypeText(declType))
		return true
	}
	if msg, ok := profileConvertScalar(base, value.value); !ok {
		c.emit(CodeProfileExprConvert, "BashPPDecl", pos, true, "%s", msg)
		return true
	}
	return false
}

// checkTypeParams rejects a type-parameter list that declares a name twice. It
// reports whether it produced a diagnostic.
func (c *profileChecker) checkTypeParams(params []*syntax.BashPPTypeParam) bool {
	seen := map[string]bool{}
	for _, group := range params {
		for _, name := range group.Names {
			if name.Value == "_" {
				continue
			}
			if seen[name.Value] {
				c.emit(CodeProfileGenericParam, "BashPPTypeParam", name.Pos(), false,
					"type parameter %s redeclared", name.Value)
				return true
			}
			seen[name.Value] = true
		}
	}
	return false
}

// typeDecl applies the rules a type declaration alone can decide.
func (c *profileChecker) typeDecl(d *syntax.BashPPDecl) {
	if c.checkTypeParams(d.TypeParams) {
		return
	}
	// Interface and enum declarations have their own validations, which are
	// not part of this slice; neither can form a representation cycle.
	if d.DeclType != nil && (d.DeclType.Value == "interface" || d.DeclType.Value == "enum") {
		return
	}
	if d.Name == nil || d.DeclTypeExpr == nil {
		return
	}
	// A named type whose own representation contains itself has no finite size.
	// Slices, maps, pointers, channels and functions are indirections, so they
	// break the cycle; an array element and a struct field do not.
	active, direct := map[string]bool{d.Name.Value: true}, map[string]bool{d.Name.Value: true}
	if c.representationCycle(d.DeclTypeExpr, active, direct) == profileCycleFound {
		c.emit("", "BashPPDecl", d.Pos(), true, "cyclic type declaration: %s", d.Name.Value)
	}
}

type profileCycleResult int

const (
	profileCycleNone profileCycleResult = iota
	profileCycleFound
	// profileCycleUnknown is returned as soon as the walk meets a type it
	// cannot resolve. Absence of a cycle is never concluded from a partial
	// walk, but neither is its presence.
	profileCycleUnknown
)

func (c *profileChecker) representationCycle(t syntax.BashPPTypeExpr, active, direct map[string]bool) profileCycleResult {
	switch x := t.(type) {
	case nil:
		return profileCycleUnknown
	case *syntax.BashPPTypeParamType, *syntax.BashPPInterfaceType,
		*syntax.BashPPUnionType, *syntax.BashPPApproxType, *syntax.BashPPFuncType:
		return profileCycleNone
	case *syntax.BashPPPointerType:
		// An indirection: the referent is a separate allocation, so the chain
		// of direct containment restarts here.
		return c.representationCycle(x.Element, active, map[string]bool{})
	case *syntax.BashPPCollectionType:
		if x.Kind == "array" {
			return c.representationCycle(x.Element, active, direct)
		}
		return c.representationCycle(x.Element, active, map[string]bool{})
	case *syntax.BashPPStructType:
		worst := profileCycleNone
		for _, f := range x.Fields {
			switch c.representationCycle(f.FieldTypeExpr, active, direct) {
			case profileCycleFound:
				return profileCycleFound
			case profileCycleUnknown:
				worst = profileCycleUnknown
			}
		}
		return worst
	case *syntax.BashPPNamedType:
		name := x.Name.Value
		if profileBuiltinTypeName(name) {
			return profileCycleNone
		}
		if active[name] {
			if direct[name] {
				return profileCycleFound
			}
			return profileCycleNone
		}
		info, ok := c.types[name]
		if !ok || info.underlying == nil {
			return profileCycleUnknown
		}
		active[name] = true
		defer delete(active, name)
		next := map[string]bool{name: true}
		for k := range direct {
			next[k] = true
		}
		return c.representationCycle(info.underlying, active, next)
	}
	return profileCycleUnknown
}

// checkValueType rejects a declared value type that is, or contains, a
// constraint interface — one whose elements include type-set terms rather than
// only methods. Such an interface has no value representation.
func (c *profileChecker) checkValueType(t syntax.BashPPTypeExpr, pos syntax.Pos) bool {
	if !c.constraintValueType(t, map[string]bool{}) {
		return false
	}
	c.emit(CodeProfileInterfaceTypeSet, "BashPPDecl", pos, true,
		"constraint interface cannot be used as a value type")
	return true
}

func (c *profileChecker) constraintValueType(t syntax.BashPPTypeExpr, seen map[string]bool) bool {
	switch x := t.(type) {
	case *syntax.BashPPInterfaceType:
		return c.interfaceHasTypeTerms(x, map[*syntax.BashPPInterfaceType]bool{})
	case *syntax.BashPPPointerType:
		return c.constraintValueType(x.Element, seen)
	case *syntax.BashPPCollectionType:
		return c.constraintValueType(x.Key, seen) || c.constraintValueType(x.Element, seen)
	case *syntax.BashPPStructType:
		for _, f := range x.Fields {
			if f.FieldTypeExpr != nil && c.constraintValueType(f.FieldTypeExpr, seen) {
				return true
			}
		}
	case *syntax.BashPPNamedType:
		if seen[x.Name.Value] {
			return false
		}
		seen[x.Name.Value] = true
		info, ok := c.types[x.Name.Value]
		if !ok || info.underlying == nil {
			return false
		}
		return c.constraintValueType(info.underlying, seen)
	}
	return false
}

// interfaceHasTypeTerms reports whether an interface constrains a type set, as
// opposed to requiring methods. A union or approximation term does so directly;
// an embedded interface does so when it in turn has one.
func (c *profileChecker) interfaceHasTypeTerms(iface *syntax.BashPPInterfaceType, seen map[*syntax.BashPPInterfaceType]bool) bool {
	if iface == nil || seen[iface] {
		return false
	}
	seen[iface] = true
	elems := iface.Elems
	if len(elems) == 0 {
		for _, spec := range iface.Methods {
			elems = append(elems, &syntax.BashPPInterfaceElem{Method: spec})
		}
	}
	for _, elem := range elems {
		if elem.Method != nil || elem.Embedded == nil {
			continue
		}
		switch elem.Embedded.(type) {
		case *syntax.BashPPUnionType, *syntax.BashPPApproxType:
			return true
		}
		if embedded, ok := c.interfaceOf(elem.Embedded); ok {
			if c.interfaceHasTypeTerms(embedded, seen) {
				return true
			}
			continue
		}
		// Only a known concrete type proves a type-set term. A name whose
		// declaration is unavailable may instead denote an ordinary interface.
		if c.concreteTypeTerm(elem.Embedded, map[string]bool{}) {
			return true
		}
	}
	return false
}

func (c *profileChecker) concreteTypeTerm(t syntax.BashPPTypeExpr, seen map[string]bool) bool {
	switch x := t.(type) {
	case *syntax.BashPPNamedType:
		if x.Name == nil || seen[x.Name.Value] {
			return false
		}
		name := x.Name.Value
		seen[name] = true
		if info := c.types[name]; info != nil {
			return c.concreteTypeTerm(info.underlying, seen)
		}
		return name != "error" && name != "any" && profileBuiltinTypeName(name)
	case *syntax.BashPPPointerType, *syntax.BashPPCollectionType, *syntax.BashPPStructType, *syntax.BashPPFuncType:
		return true
	}
	return false
}

func (c *profileChecker) constGroup(g *syntax.BashPPConstGroup) {
	// A spec with no initializer repeats the preceding non-empty type and
	// expression; only its own iota index changes. That repetition is what
	// turns a legal first spec into an overflowing second one.
	var previous *syntax.BashPPConstSpec
	for _, spec := range g.Specs {
		if c.done() {
			return
		}
		effective := spec
		if spec.InitExpr == nil {
			if previous == nil {
				return
			}
			copied := *spec
			copied.DeclType, copied.DeclTypeExpr = previous.DeclType, previous.DeclTypeExpr
			copied.Init, copied.InitExpr = previous.Init, previous.InitExpr
			effective = &copied
		} else {
			previous = spec
		}
		if effective.InitExpr == nil {
			continue
		}
		// iota is predeclared; an ordinary declaration of the same name shadows
		// it, in which case the substitution must not happen.
		expr := effective.InitExpr
		if c.lookup("iota") == nil && c.funcs["iota"] == nil {
			expr = profileSubstituteIota(expr, int(spec.Iota))
		}
		if effective.DeclTypeExpr != nil {
			if c.checkScalarDeclValue(effective.DeclTypeExpr, expr, spec.Name.Pos()) {
				return
			}
		}
		if spec.Name == nil || spec.Name.Value == "_" {
			continue
		}
		b := &profileBinding{typeExpr: effective.DeclTypeExpr}
		if s, ok := c.eval(expr); ok {
			b.value, b.typ = s.value, s.typ
		}
		if base := namedBase(effective.DeclTypeExpr); base != "" && b.value != nil {
			b.typ = base
		}
		c.declare(spec.Name.Value, b)
	}
}

// profileSubstituteIota rewrites the predeclared iota to its ConstSpec index.
// It copies every node it rewrites so the caller's tree keeps its positions.
func profileSubstituteIota(expr syntax.BashPPExpr, value int) syntax.BashPPExpr {
	switch x := expr.(type) {
	case *syntax.BashPPIdent:
		if x.Name != nil && x.Name.Value == "iota" {
			lit := *x.Name
			lit.Value = fmt.Sprint(value)
			return &syntax.BashPPBasicLit{Value: &lit, Kind: "INT"}
		}
	case *syntax.BashPPParenExpr:
		y := *x
		y.X = profileSubstituteIota(x.X, value)
		return &y
	case *syntax.BashPPUnaryExpr:
		y := *x
		y.X = profileSubstituteIota(x.X, value)
		return &y
	case *syntax.BashPPBinaryExpr:
		y := *x
		y.X, y.Y = profileSubstituteIota(x.X, value), profileSubstituteIota(x.Y, value)
		return &y
	case *syntax.BashPPConvertExpr:
		y := *x
		y.X = profileSubstituteIota(x.X, value)
		return &y
	}
	return expr
}

// --- control flow -----------------------------------------------------------

func (c *profileChecker) ifStmt(x *syntax.BashPPIf) {
	c.push()
	defer c.pop()
	if x.InitStmt != nil {
		c.cmd(x.InitStmt)
		if c.done() {
			return
		}
	} else if x.Init != nil {
		c.shortDecl(x.Init)
		if c.done() {
			return
		}
	}
	if c.checkCondition(x.Cond, CodeProfileIfCond, "BashPPIf", "if") {
		return
	}
	c.block(x.Then)
	c.cmd(x.Else)
}

func (c *profileChecker) forStmt(x *syntax.BashPPFor) {
	c.push()
	defer c.pop()
	c.cmd(x.Init)
	if c.done() {
		return
	}
	if c.checkCondition(x.Cond, CodeProfileForCond, "BashPPFor", "for") {
		return
	}
	c.cmd(x.Post)
	c.block(x.Body)
}

// checkCondition rejects a header condition whose static kind is decidable and
// is not boolean.
func (c *profileChecker) checkCondition(cond syntax.BashPPExpr, code, node, keyword string) bool {
	if cond == nil {
		return false
	}
	c.expr(cond)
	if c.done() {
		return true
	}
	value, ok := c.eval(cond)
	if !ok || value.value.Kind() == constant.Bool {
		return false
	}
	c.emit(code, node, cond.Pos(), false, "%s condition must be boolean, got %s", keyword, value.value.Kind())
	return true
}

func (c *profileChecker) rangeStmt(x *syntax.BashPPRange) {
	c.push()
	defer c.pop()
	if x.Expr != nil {
		c.expr(x.Expr)
		if c.done() {
			return
		}
		// An integer range yields one index and nothing else, so a second
		// iteration variable has no value to receive.
		if value, ok := c.eval(x.Expr); ok && value.value.Kind() == constant.Int && len(x.Names) > 1 {
			c.emit(CodeProfileRangeArity, "BashPPRange", x.Expr.Pos(), true,
				"integer range permits at most one iteration variable")
			return
		}
	}
	for _, name := range x.Names {
		c.declare(name.Value, &profileBinding{})
	}
	c.block(x.Body)
}

func (c *profileChecker) switchStmt(x *syntax.BashPPSwitch) {
	c.push()
	defer c.pop()
	if x.Init != nil && !x.TypeSwitch {
		c.cmd(x.Init)
		if c.done() {
			return
		}
	}
	if x.TypeSwitch {
		// A type switch compares types, not constants; its own diagnostics are
		// outside this slice.
		return
	}
	var tag profileScalar
	tagged := false
	if x.Tag != nil {
		c.expr(x.Tag)
		if c.done() {
			return
		}
		tag, tagged = c.eval(x.Tag)
		// A tag with a named type admits enum members and named-type
		// comparison, which this slice does not decide.
		if tagged && tag.typ != "" {
			tagged = false
		}
	}
	for _, arm := range x.Arms {
		if tagged {
			for _, e := range arm.Exprs {
				candidate, ok := c.eval(e)
				if !ok || candidate.typ != "" {
					continue
				}
				if profileComparableKinds(tag.value.Kind(), candidate.value.Kind()) {
					continue
				}
				c.emit(CodeProfileSwitchType, "BashPPSwitchArm", e.Pos(), false,
					"case expression type %s does not match switch tag type %s",
					candidate.value.Kind(), tag.value.Kind())
				return
			}
		}
		c.push()
		c.stmts(arm.Stmts)
		c.pop()
		if c.done() {
			return
		}
	}
}

// profileComparableKinds mirrors the source rule: kinds must match, except that
// the two numeric kinds are mutually comparable as untyped constants.
func profileComparableKinds(tag, candidate constant.Kind) bool {
	if tag == candidate {
		return true
	}
	numeric := func(k constant.Kind) bool { return k == constant.Int || k == constant.Float }
	return numeric(tag) && numeric(candidate)
}

// --- expressions ------------------------------------------------------------

// expr visits an expression for the rules that fire on a sub-expression rather
// than on a statement shape.
func (c *profileChecker) expr(e syntax.BashPPExpr) {
	if e == nil || c.done() {
		return
	}
	switch x := e.(type) {
	case *syntax.BashPPParenExpr:
		c.expr(x.X)
	case *syntax.BashPPUnaryExpr:
		c.expr(x.X)
	case *syntax.BashPPBinaryExpr:
		c.expr(x.X)
		c.expr(x.Y)
	case *syntax.BashPPConvertExpr:
		c.expr(x.X)
	case *syntax.BashPPAddressExpr:
		c.expr(x.X)
	case *syntax.BashPPDerefExpr:
		c.expr(x.X)
	case *syntax.BashPPIndexExpr:
		c.expr(x.X)
		c.expr(x.Index)
	case *syntax.BashPPSelectorExpr:
		c.expr(x.X)
	case *syntax.BashPPCall:
		c.call(x)
	case *syntax.BashPPTypeAssertExpr:
		c.expr(x.X)
		if c.done() {
			return
		}
		c.checkTypeAssert(x)
	case *syntax.BashPPCompositeLit:
		c.checkCompositeLit(x)
		if c.done() {
			return
		}
		for _, elem := range x.Elems {
			c.expr(elem.Value)
			if c.done() {
				return
			}
		}
	}
}

// checkCompositeLit rejects a struct literal that mixes keyed and positional
// elements. Slice and map literals may legally mix them, so the rule is gated
// on the literal's type resolving to a struct.
func (c *profileChecker) checkCompositeLit(lit *syntax.BashPPCompositeLit) {
	if lit.LitType == nil {
		return
	}
	name, ok := c.structTypeName(lit.LitType)
	if !ok {
		return
	}
	keyed, positional := false, false
	for _, elem := range lit.Elems {
		if elem.Key != nil {
			keyed = true
		} else {
			positional = true
		}
	}
	if keyed && positional {
		c.emit(CodeProfileStructMixed, "BashPPCompositeLit", lit.Pos(), false,
			"%s literal cannot mix keyed and positional fields", name)
		return
	}
	// A field key names one field of this struct; a selector names a path
	// through another value, which is not a key.
	for _, elem := range lit.Elems {
		if _, selector := elem.Key.(*syntax.BashPPSelectorExpr); selector {
			c.emit(CodeProfileStructKey, "BashPPCompositeElem", elem.Key.Pos(), true,
				"%s literal field key must be an identifier, not a selector expression", name)
			return
		}
	}
}

// structTypeName resolves a literal type to the name the source diagnostic
// uses: the written name for a named struct, "struct" for an anonymous one.
func (c *profileChecker) structTypeName(t syntax.BashPPTypeExpr) (string, bool) {
	switch x := t.(type) {
	case *syntax.BashPPStructType:
		return "struct", true
	case *syntax.BashPPNamedType:
		owner := x.Name.Value
		current := x
		for seen := map[string]bool{}; current != nil && !seen[current.Name.Value]; {
			seen[current.Name.Value] = true
			info, ok := c.types[current.Name.Value]
			if !ok || info.underlying == nil {
				return "", false
			}
			if _, ok := info.underlying.(*syntax.BashPPStructType); ok {
				return owner, true
			}
			current, _ = info.underlying.(*syntax.BashPPNamedType)
		}
	}
	return "", false
}

// --- calls ------------------------------------------------------------------

func (c *profileChecker) call(x *syntax.BashPPCall) {
	if x == nil || c.done() {
		return
	}
	if c.checkLengthBuiltin(x) {
		return
	}
	if c.checkGenericCall(x) {
		return
	}
	if x.FuncLit != nil && x.FuncLit.Body != nil {
		c.push()
		c.bindFields(x.FuncLit.Params)
		c.bindFields(x.FuncLit.Results)
		c.stmts(x.FuncLit.Body.Stmts)
		c.pop()
	}
}

// checkLengthBuiltin rejects len/cap applied to an operand that is provably
// scalar. It stays quiet whenever the operand's shape is not decided, which is
// what keeps channels, slices, maps and call results out of it.
func (c *profileChecker) checkLengthBuiltin(x *syntax.BashPPCall) bool {
	if len(x.Fun) != 1 || len(x.Args) != 1 || x.Ellipsis.IsValid() {
		return false
	}
	name := x.Fun[0].Value
	if name != "len" && name != "cap" {
		return false
	}
	// A user declaration of the same name is not the builtin.
	if c.funcs[name] != nil || c.lookup(name) != nil {
		return false
	}
	kind, ok := c.wordScalarKind(x.Args[0])
	if !ok {
		return false
	}
	if name == "cap" {
		// cap has no meaning for a string, so every scalar kind is rejected.
		c.emit(CodeProfileBuiltinType, "BashPPCall", x.Pos(), false, "cap argument must be an array or slice")
		return true
	}
	if kind == constant.String {
		return false
	}
	c.emit(CodeProfileBuiltinType, "BashPPCall", x.Pos(), false,
		"len argument must be a string, array, slice, or map")
	return true
}

// wordScalarKind decides the constant kind of a call argument, and reports
// false unless the argument is provably a scalar.
func (c *profileChecker) wordScalarKind(w *syntax.Word) (constant.Kind, bool) {
	s, ok := c.wordScalar(w)
	if !ok {
		return constant.Unknown, false
	}
	return s.value.Kind(), true
}

// directMethod resolves only declarations on the actual named receiver (or
// its alias). Promoted and dynamic interface methods are deliberately undecided.
func (c *profileChecker) directMethod(t syntax.BashPPTypeExpr, name string) *syntax.BashPPFuncDecl {
	if p, ok := t.(*syntax.BashPPPointerType); ok {
		t = p.Element
	}
	named, ok := t.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return nil
	}
	recv, ok := c.resolveAlias(named.Name.Value)
	if !ok {
		return nil
	}
	return c.methodDecls[recv][name]
}

func (c *profileChecker) methodReceiver(name string) (syntax.BashPPTypeExpr, bool) {
	if b := c.lookup(name); b != nil {
		return b.typeExpr, false
	}
	if c.types[name] != nil {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}, true
	}
	return nil, false
}

func (c *profileChecker) checkGenericCall(x *syntax.BashPPCall) bool {
	var fn *syntax.BashPPFuncDecl
	var name string
	if len(x.Fun) == 1 {
		name = x.Fun[0].Value
		if c.lookup(name) != nil {
			return false
		}
		fn = c.funcs[name]
	} else if len(x.Fun) == 2 {
		name = x.Fun[1].Value
		receiver, expression := c.methodReceiver(x.Fun[0].Value)
		fn = c.directMethod(receiver, name)
		// A method expression supplies the receiver as its first argument; it
		// does not participate in inference of independent method parameters.
		if expression && len(x.Args) > 0 {
			copy := *x
			copy.Args = x.Args[1:]
			x = &copy
		}
	}
	if fn == nil {
		return false
	}
	count := 0
	for _, group := range fn.TypeParams {
		count += len(group.Names)
	}
	if len(x.TypeArgs) > 0 && len(x.TypeArgs) != count {
		if count == 0 {
			c.emit(CodeProfileGenericArity, "BashPPCall", x.Pos(), false, "%s is not generic; got %d type argument(s)", name, len(x.TypeArgs))
		} else {
			c.emit(CodeProfileGenericArity, "BashPPCall", x.Pos(), false, "%s expects %d type argument(s); got %d", name, count, len(x.TypeArgs))
		}
		return true
	}
	if count == 0 {
		return false
	}
	if len(x.TypeArgs) == 0 {
		used := map[string]bool{}
		for _, f := range fn.Params {
			collectTypeParamUses(f.FieldTypeExpr, used)
		}
		for _, group := range fn.TypeParams {
			for _, param := range group.Names {
				if !used[param.Value] {
					c.emit(CodeProfileGenericInfer, "BashPPCall", x.Pos(), false, "cannot infer type arguments for %s", name)
					return true
				}
			}
		}
	}
	return c.checkGenericConstraints(x, fn, name)
}

// An uninstantiated method value has no call arguments from which to infer
// independent parameters. An indexed/instantiated expression is not this case.
func (c *profileChecker) checkGenericMethodValue(expr syntax.BashPPExpr) bool {
	for {
		p, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = p.X
	}
	sel, ok := expr.(*syntax.BashPPSelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	id, ok := sel.X.(*syntax.BashPPIdent)
	if !ok || id.Name == nil {
		return false
	}
	receiver, _ := c.methodReceiver(id.Name.Value)
	fn := c.directMethod(receiver, sel.Sel.Value)
	if fn == nil || len(fn.TypeParams) == 0 {
		return false
	}
	c.emit(CodeProfileGenericInfer, "BashPPSelectorExpr", sel.Pos(), false, "cannot infer type arguments for %s", sel.Sel.Value)
	return true
}

// checkGenericConstraints rejects an instantiation whose type argument provably
// fails its constraint. Only `comparable` is decided here; every other
// constraint is left to the interpreter.
func (c *profileChecker) checkGenericConstraints(x *syntax.BashPPCall, fn *syntax.BashPPFuncDecl, name string) bool {
	bindings := c.inferTypeArgs(x, fn)
	for _, group := range fn.TypeParams {
		named, ok := group.Constraint.(*syntax.BashPPNamedType)
		if !ok || named.Name.Value != "comparable" {
			continue
		}
		for _, param := range group.Names {
			arg := bindings[param.Value]
			if arg == nil || c.comparableType(arg, map[string]bool{}) {
				continue
			}
			c.emit(CodeProfileGenericConstraint, "BashPPCall", x.Pos(), false,
				"%s does not satisfy constraint for %s in %s", profileTypeText(arg), param.Value, name)
			return true
		}
	}
	return false
}

// inferTypeArgs binds type parameters that a call names explicitly, or that
// appear as a bare parameter type whose argument has a decided type.
func (c *profileChecker) inferTypeArgs(x *syntax.BashPPCall, fn *syntax.BashPPFuncDecl) map[string]syntax.BashPPTypeExpr {
	bindings := map[string]syntax.BashPPTypeExpr{}
	var names []*syntax.Lit
	for _, group := range fn.TypeParams {
		names = append(names, group.Names...)
	}
	if len(x.TypeArgs) > 0 {
		if len(x.TypeArgs) != len(names) {
			return bindings
		}
		for i, name := range names {
			bindings[name.Value] = x.TypeArgs[i].ArgType
		}
		return bindings
	}
	var params []*syntax.BashPPField
	for _, f := range fn.Params {
		if len(f.Names) == 0 {
			params = append(params, f)
			continue
		}
		for range f.Names {
			params = append(params, f)
		}
	}
	for i, arg := range x.Args {
		if i >= len(params) {
			break
		}
		param, ok := params[i].FieldTypeExpr.(*syntax.BashPPTypeParamType)
		if !ok {
			continue
		}
		actual := c.wordTypeExpr(arg)
		if actual == nil {
			continue
		}
		if existing, seen := bindings[param.Name.Value]; seen && profileTypeText(existing) != profileTypeText(actual) {
			delete(bindings, param.Name.Value)
			continue
		}
		bindings[param.Name.Value] = actual
	}
	return bindings
}

func (c *profileChecker) wordTypeExpr(w *syntax.Word) syntax.BashPPTypeExpr {
	if w == nil || len(w.Parts) != 1 {
		return nil
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok || !syntax.BashPPValidIdent(lit.Value) {
		return nil
	}
	if b := c.lookup(lit.Value); b != nil {
		return b.typeExpr
	}
	return nil
}

func collectTypeParamUses(t syntax.BashPPTypeExpr, out map[string]bool) {
	switch x := t.(type) {
	case *syntax.BashPPTypeParamType:
		out[x.Name.Value] = true
	case *syntax.BashPPNamedType:
		for _, arg := range x.TypeArgs {
			collectTypeParamUses(arg.ArgType, out)
		}
	case *syntax.BashPPPointerType:
		collectTypeParamUses(x.Element, out)
	case *syntax.BashPPCollectionType:
		collectTypeParamUses(x.Key, out)
		collectTypeParamUses(x.Element, out)
	case *syntax.BashPPStructType:
		for _, f := range x.Fields {
			collectTypeParamUses(f.FieldTypeExpr, out)
		}
	case *syntax.BashPPFuncType:
		for _, f := range x.Params {
			collectTypeParamUses(f.FieldTypeExpr, out)
		}
		for _, f := range x.Results {
			collectTypeParamUses(f.FieldTypeExpr, out)
		}
	}
}

// comparableType mirrors Go's comparability: slices, maps and functions are not
// comparable, an array is comparable when its element is, and a struct is
// comparable when every field is.
func (c *profileChecker) comparableType(t syntax.BashPPTypeExpr, seen map[string]bool) bool {
	switch x := t.(type) {
	case *syntax.BashPPNamedType:
		info, ok := c.types[x.Name.Value]
		if !ok {
			return profileBuiltinTypeName(x.Name.Value)
		}
		if seen[x.Name.Value] || info.underlying == nil {
			return true
		}
		seen[x.Name.Value] = true
		defer delete(seen, x.Name.Value)
		return c.comparableType(info.underlying, seen)
	case *syntax.BashPPPointerType, *syntax.BashPPInterfaceType, *syntax.BashPPTypeParamType:
		return true
	case *syntax.BashPPCollectionType:
		return x.Kind == "array" && c.comparableType(x.Element, seen)
	case *syntax.BashPPStructType:
		for _, f := range x.Fields {
			if f.FieldTypeExpr == nil || !c.comparableType(f.FieldTypeExpr, seen) {
				return false
			}
		}
		return true
	case *syntax.BashPPFuncType:
		return false
	}
	// An undecided shape is never reported as a violation.
	return true
}

// --- interfaces -------------------------------------------------------------

// checkInterfaceAssign rejects `var i I = v` when v's type provably lacks a
// method I requires. It reports whether it produced a diagnostic.
func (c *profileChecker) checkInterfaceAssign(declType syntax.BashPPTypeExpr, init syntax.BashPPExpr) bool {
	iface, ok := c.interfaceOf(declType)
	if !ok {
		return false
	}
	id, ok := init.(*syntax.BashPPIdent)
	if !ok {
		return false
	}
	b := c.lookup(id.Name.Value)
	if b == nil || b.typeExpr == nil {
		return false
	}
	required, decided := c.interfaceMethodNames(iface, map[*syntax.BashPPInterfaceType]bool{})
	if decided {
		for _, name := range required {
			fn := c.directMethod(b.typeExpr, name)
			if fn != nil && len(fn.TypeParams) > 0 {
				// A pointer-only method is not a member of a non-pointer value's
				// interface method set; preserve the missing-method rule in that case.
				_, pointer := b.typeExpr.(*syntax.BashPPPointerType)
				if fn.Receiver.Pointer && !pointer {
					continue
				}
				c.emit(CodeProfileInterfaceGeneric, "BashPPDecl", init.Pos(), false, "%s method %s declares type parameters and cannot implement an interface method", profileTypeText(b.typeExpr), name)
				return true
			}
		}
	}
	missing, ok := c.missingMethod(b.typeExpr, iface)
	if !ok || missing == "" {
		return false
	}
	c.emit(CodeProfileInterfaceMissing, "BashPPDecl", init.Pos(), false,
		"%s does not implement interface (missing method %s)", profileTypeText(b.typeExpr), missing)
	return true
}

// checkTypeAssert rejects an assertion to a concrete type that cannot possibly
// hold the interface's method set. The comma-ok form is rejected too: it is the
// assertion itself that is impossible, not merely its outcome.
func (c *profileChecker) checkTypeAssert(x *syntax.BashPPTypeAssertExpr) {
	id, ok := x.X.(*syntax.BashPPIdent)
	if !ok || x.Assert == nil {
		return
	}
	b := c.lookup(id.Name.Value)
	if b == nil || b.typeExpr == nil {
		return
	}
	iface, ok := c.interfaceOf(b.typeExpr)
	if !ok {
		return
	}
	// Asserting one interface from another is a dynamic question.
	if _, isIface := c.interfaceOf(x.Assert); isIface {
		return
	}
	missing, ok := c.missingMethod(x.Assert, iface)
	if !ok || missing == "" {
		return
	}
	c.emit(CodeProfileAssertImpossible, "BashPPTypeAssertExpr", x.Pos(), true,
		"%s cannot be asserted from %s", profileTypeText(x.Assert), profileTypeText(b.typeExpr))
}

// missingMethod returns the first method of iface that t's method set lacks.
// The second result is false when the method set is not fully decidable, in
// which case nothing is reported.
func (c *profileChecker) missingMethod(t syntax.BashPPTypeExpr, iface *syntax.BashPPInterfaceType) (string, bool) {
	set, ok := c.methodSet(t)
	if !ok {
		return "", false
	}
	required, ok := c.interfaceMethodNames(iface, map[*syntax.BashPPInterfaceType]bool{})
	if !ok {
		return "", false
	}
	for _, name := range required {
		if !set[name] {
			return name, true
		}
	}
	return "", true
}

// methodSet returns the method names reachable on t, following embedded fields.
// The second result is false whenever any part of that graph is not modeled: a
// partial method set can only ever produce a wrong "missing method", so absence
// is never concluded from an incomplete walk.
func (c *profileChecker) methodSet(t syntax.BashPPTypeExpr) (map[string]bool, bool) {
	set := map[string]bool{}
	if !c.collectMethodSet(t, set, map[string]bool{}) {
		return nil, false
	}
	return set, true
}

func (c *profileChecker) collectMethodSet(t syntax.BashPPTypeExpr, set, seen map[string]bool) bool {
	pointer := false
	if p, ok := t.(*syntax.BashPPPointerType); ok {
		pointer, t = true, p.Element
	}
	if iface, ok := t.(*syntax.BashPPInterfaceType); ok {
		return c.addInterfaceMethods(iface, set)
	}
	named, ok := t.(*syntax.BashPPNamedType)
	if !ok {
		return false
	}
	name, ok := c.resolveAlias(named.Name.Value)
	if !ok {
		return false
	}
	key := name
	if pointer {
		key = "*" + name
	}
	if seen[key] {
		return true
	}
	seen[key] = true
	info := c.types[name]
	if info == nil || info.underlying == nil {
		return false
	}
	for _, m := range c.methods[name] {
		if pointer || !m.Pointer {
			set[m.Name] = true
		}
	}
	switch underlying := info.underlying.(type) {
	case *syntax.BashPPInterfaceType:
		return c.addInterfaceMethods(underlying, set)
	case *syntax.BashPPStructType:
		for _, f := range underlying.Fields {
			if !f.Embedded && len(f.Names) > 0 {
				continue
			}
			if f.FieldTypeExpr == nil {
				return false
			}
			// Whether an embedded value also promotes ITS pointer methods
			// depends on the outer value's addressability, which is not a
			// property of the type. Including them over-approximates the set,
			// which loses a diagnostic rather than rejecting a valid program.
			embedded := f.FieldTypeExpr
			if _, isPointer := embedded.(*syntax.BashPPPointerType); !isPointer {
				embedded = &syntax.BashPPPointerType{Element: embedded}
			}
			if !c.collectMethodSet(embedded, set, seen) {
				return false
			}
		}
	}
	return true
}

func (c *profileChecker) addInterfaceMethods(iface *syntax.BashPPInterfaceType, set map[string]bool) bool {
	names, ok := c.interfaceMethodNames(iface, map[*syntax.BashPPInterfaceType]bool{})
	if !ok {
		return false
	}
	for _, name := range names {
		set[name] = true
	}
	return true
}

// resolveAlias follows an alias chain to the type that actually carries the
// methods. An alias denotes the same type, so it carries the same method set.
func (c *profileChecker) resolveAlias(name string) (string, bool) {
	for seen := map[string]bool{}; ; {
		info, ok := c.types[name]
		if !ok {
			return "", false
		}
		if !info.alias || seen[name] {
			return name, true
		}
		seen[name] = true
		target, ok := info.underlying.(*syntax.BashPPNamedType)
		if !ok {
			return name, true
		}
		name = target.Name.Value
	}
}

// interfaceOf resolves a type expression to the interface it denotes.
func (c *profileChecker) interfaceOf(t syntax.BashPPTypeExpr) (*syntax.BashPPInterfaceType, bool) {
	for seen := map[string]bool{}; t != nil; {
		switch x := t.(type) {
		case *syntax.BashPPInterfaceType:
			return x, true
		case *syntax.BashPPNamedType:
			if seen[x.Name.Value] {
				return nil, false
			}
			seen[x.Name.Value] = true
			info, ok := c.types[x.Name.Value]
			if !ok {
				switch x.Name.Value {
				case "error":
					return &syntax.BashPPInterfaceType{Methods: []*syntax.BashPPMethodSpec{{Name: &syntax.Lit{Value: "Error"}}}}, true
				}
			}
			if !ok || info.underlying == nil {
				return nil, false
			}
			t = info.underlying
		default:
			return nil, false
		}
	}
	return nil, false
}

// interfaceMethodNames lists the methods an interface requires, in declaration
// order, expanding embedded interfaces where they appear. It reports false when
// an embedded element cannot be resolved, so a partial set is never used.
func (c *profileChecker) interfaceMethodNames(iface *syntax.BashPPInterfaceType, seen map[*syntax.BashPPInterfaceType]bool) ([]string, bool) {
	if seen[iface] {
		return nil, true
	}
	seen[iface] = true
	elems := iface.Elems
	if len(elems) == 0 {
		for _, spec := range iface.Methods {
			elems = append(elems, &syntax.BashPPInterfaceElem{Method: spec})
		}
	}
	var out []string
	for _, elem := range elems {
		if elem.Method != nil {
			out = append(out, elem.Method.Name.Value)
			continue
		}
		if elem.Embedded == nil {
			continue
		}
		embedded, ok := c.interfaceOf(elem.Embedded)
		if !ok {
			// A type-set term, not a method requirement.
			if _, named := elem.Embedded.(*syntax.BashPPNamedType); named {
				return nil, false
			}
			continue
		}
		names, ok := c.interfaceMethodNames(embedded, seen)
		if !ok {
			return nil, false
		}
		out = append(out, names...)
	}
	return out, true
}

// --- constant evaluation ----------------------------------------------------

// eval decides an expression's constant value, or reports false. It is
// deliberately total and conservative: any shape it does not model, any operand
// it cannot decide, and any operation it cannot perform exactly yields false,
// which silences every rule that depends on it.
func (c *profileChecker) eval(expr syntax.BashPPExpr) (profileScalar, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPBasicLit:
		tok, ok := map[string]token.Token{
			"INT": token.INT, "FLOAT": token.FLOAT, "CHAR": token.CHAR, "STRING": token.STRING,
		}[x.Kind]
		if !ok {
			return profileScalar{}, false
		}
		v := constant.MakeFromLiteral(x.Value.Value, tok, 0)
		if v.Kind() == constant.Unknown {
			return profileScalar{}, false
		}
		return profileScalar{value: v}, true
	case *syntax.BashPPIdent:
		switch x.Name.Value {
		case "true":
			return profileScalar{value: constant.MakeBool(true)}, true
		case "false":
			return profileScalar{value: constant.MakeBool(false)}, true
		}
		b := c.lookup(x.Name.Value)
		if b == nil || b.value == nil {
			return profileScalar{}, false
		}
		return profileScalar{value: b.value, typ: b.typ}, true
	case *syntax.BashPPParenExpr:
		return c.eval(x.X)
	case *syntax.BashPPUnaryExpr:
		return c.evalUnary(x)
	case *syntax.BashPPBinaryExpr:
		return c.evalBinary(x)
	}
	return profileScalar{}, false
}

func (c *profileChecker) evalUnary(x *syntax.BashPPUnaryExpr) (profileScalar, bool) {
	v, ok := c.eval(x.X)
	if !ok {
		return profileScalar{}, false
	}
	switch x.Op.Value {
	case "+":
		if v.value.Kind() == constant.Int || v.value.Kind() == constant.Float {
			return v, true
		}
	case "-":
		if v.value.Kind() == constant.Int || v.value.Kind() == constant.Float {
			return profileScalar{value: constant.UnaryOp(token.SUB, v.value, 0), typ: v.typ}, true
		}
	case "!":
		if v.value.Kind() == constant.Bool {
			return profileScalar{value: constant.UnaryOp(token.NOT, v.value, 0), typ: v.typ}, true
		}
	}
	return profileScalar{}, false
}

func (c *profileChecker) evalBinary(x *syntax.BashPPBinaryExpr) (profileScalar, bool) {
	op, ok := profileBinaryOps[x.Op.Value]
	if !ok {
		return profileScalar{}, false
	}
	left, ok := c.eval(x.X)
	if !ok {
		return profileScalar{}, false
	}
	right, ok := c.eval(x.Y)
	if !ok {
		return profileScalar{}, false
	}
	typ := left.typ
	if typ == "" {
		typ = right.typ
	}
	if left.typ != "" && right.typ != "" && left.typ != right.typ {
		return profileScalar{}, false
	}
	switch op {
	case token.LAND, token.LOR:
		if left.value.Kind() != constant.Bool || right.value.Kind() != constant.Bool {
			return profileScalar{}, false
		}
		l, r := constant.BoolVal(left.value), constant.BoolVal(right.value)
		result := l && r
		if op == token.LOR {
			result = l || r
		}
		return profileScalar{value: constant.MakeBool(result)}, true
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		l, r, ok := profileUnify(left.value, right.value)
		if !ok {
			return profileScalar{}, false
		}
		return profileScalar{value: constant.MakeBool(constant.Compare(l, op, r))}, true
	case token.SHL, token.SHR:
		if left.value.Kind() != constant.Int || right.value.Kind() != constant.Int {
			return profileScalar{}, false
		}
		count, exact := constant.Uint64Val(right.value)
		if !exact || count > 512 {
			return profileScalar{}, false
		}
		return profileScalar{value: constant.Shift(left.value, op, uint(count)), typ: typ}, true
	default:
		l, r, ok := profileUnify(left.value, right.value)
		if !ok {
			return profileScalar{}, false
		}
		if op == token.QUO || op == token.REM {
			if constant.Sign(r) == 0 {
				return profileScalar{}, false
			}
		}
		if op == token.REM && l.Kind() != constant.Int {
			return profileScalar{}, false
		}
		switch op {
		case token.AND, token.OR, token.XOR, token.AND_NOT:
			if l.Kind() != constant.Int || r.Kind() != constant.Int {
				return profileScalar{}, false
			}
		case token.ADD:
			// Concatenation is the one non-numeric arithmetic operation.
			if l.Kind() != constant.String && !profileNumericKind(l.Kind()) {
				return profileScalar{}, false
			}
		default:
			// go/constant panics rather than erroring on an operator its
			// operands do not support, so the kinds are checked first.
			if !profileNumericKind(l.Kind()) {
				return profileScalar{}, false
			}
		}
		return profileScalar{value: constant.BinaryOp(l, op, r), typ: typ}, true
	}
}

func profileNumericKind(k constant.Kind) bool {
	return k == constant.Int || k == constant.Float
}

var profileBinaryOps = map[string]token.Token{
	"+": token.ADD, "-": token.SUB, "*": token.MUL, "/": token.QUO, "%": token.REM,
	"&": token.AND, "|": token.OR, "^": token.XOR, "&^": token.AND_NOT,
	"<<": token.SHL, ">>": token.SHR,
	"&&": token.LAND, "||": token.LOR,
	"==": token.EQL, "!=": token.NEQ,
	"<": token.LSS, "<=": token.LEQ, ">": token.GTR, ">=": token.GEQ,
}

// profileUnify brings two constants to a common kind, promoting an integer to a
// float when the other side is one. Mixed non-numeric kinds are not unified,
// which is what leaves a string-versus-integer comparison undecided rather than
// wrongly true or false.
func profileUnify(a, b constant.Value) (constant.Value, constant.Value, bool) {
	ka, kb := a.Kind(), b.Kind()
	if ka == constant.Unknown || kb == constant.Unknown {
		return nil, nil, false
	}
	if ka == kb {
		return a, b, true
	}
	numeric := func(k constant.Kind) bool { return k == constant.Int || k == constant.Float }
	if numeric(ka) && numeric(kb) {
		return constant.ToFloat(a), constant.ToFloat(b), true
	}
	return nil, nil, false
}

// wordScalar decides the constant value of an unevaluated word. Only the
// spellings whose value is fixed by the source are decided.
func (c *profileChecker) wordScalar(w *syntax.Word) (profileScalar, bool) {
	if w == nil || len(w.Parts) != 1 {
		return profileScalar{}, false
	}
	switch part := w.Parts[0].(type) {
	case *syntax.SglQuoted:
		return profileScalar{value: constant.MakeString(part.Value)}, true
	case *syntax.DblQuoted:
		if len(part.Parts) != 1 {
			return profileScalar{}, false
		}
		lit, ok := part.Parts[0].(*syntax.Lit)
		if !ok {
			return profileScalar{}, false
		}
		return profileScalar{value: constant.MakeString(lit.Value)}, true
	case *syntax.Lit:
		return c.literalScalar(part.Value)
	}
	return profileScalar{}, false
}

func (c *profileChecker) literalScalar(text string) (profileScalar, bool) {
	switch text {
	case "true":
		return profileScalar{value: constant.MakeBool(true)}, true
	case "false":
		return profileScalar{value: constant.MakeBool(false)}, true
	}
	if syntax.BashPPValidIdent(text) {
		b := c.lookup(text)
		if b == nil || b.value == nil {
			return profileScalar{}, false
		}
		return profileScalar{value: b.value, typ: b.typ}, true
	}
	if strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"") && len(text) >= 2 {
		v := constant.MakeFromLiteral(text, token.STRING, 0)
		if v.Kind() != constant.Unknown {
			return profileScalar{value: v}, true
		}
		return profileScalar{}, false
	}
	for _, tok := range []token.Token{token.INT, token.FLOAT} {
		if v := constant.MakeFromLiteral(text, tok, 0); v.Kind() != constant.Unknown {
			return profileScalar{value: v}, true
		}
	}
	return profileScalar{}, false
}

// --- scalar types -----------------------------------------------------------

var profileIntegerBounds = map[string][2]int64{
	"int8":  {-1 << 7, 1<<7 - 1},
	"int16": {-1 << 15, 1<<15 - 1},
	"int32": {-1 << 31, 1<<31 - 1},
	"rune":  {-1 << 31, 1<<31 - 1},
	"int64": {-1 << 63, 1<<63 - 1},
	"int":   {-1 << 63, 1<<63 - 1},
}

var profileUnsignedBits = map[string]uint{
	"uint8": 8, "byte": 8, "uint16": 16, "uint32": 32, "uint64": 64, "uint": 64, "uintptr": 64,
}

func profileIntegerType(name string) bool {
	_, signed := profileIntegerBounds[name]
	_, unsigned := profileUnsignedBits[name]
	return signed || unsigned
}

func profileScalarBase(name string) bool {
	switch name {
	case "string", "bool", "float32", "float64":
		return true
	}
	return profileIntegerType(name)
}

func profileBuiltinTypeName(name string) bool {
	switch name {
	case "any", "comparable", "error":
		return true
	}
	return profileScalarBase(name)
}

// profileUntypedAssignable mirrors the source rule for an untyped constant
// assigned to a written scalar type.
func profileUntypedAssignable(base string, value constant.Value) bool {
	switch base {
	case "string":
		return value.Kind() == constant.String
	case "bool":
		return value.Kind() == constant.Bool
	case "float32", "float64":
		return value.Kind() == constant.Int || value.Kind() == constant.Float
	default:
		return profileIntegerType(base) && constant.ToInt(value).Kind() == constant.Int
	}
}

// profileConvertScalar applies representability. It returns the source message
// for a value the type cannot hold.
func profileConvertScalar(base string, value constant.Value) (string, bool) {
	if !profileIntegerType(base) {
		return "", true
	}
	integer := constant.ToInt(value)
	if integer.Kind() != constant.Int {
		return "", true
	}
	if profileIntegerRepresentable(base, integer) {
		return "", true
	}
	return fmt.Sprintf("constant %s overflows %s", value, base), false
}

func profileIntegerRepresentable(base string, v constant.Value) bool {
	if bits, ok := profileUnsignedBits[base]; ok {
		if constant.Sign(v) < 0 {
			return false
		}
		n, exact := constant.Uint64Val(v)
		if !exact {
			return false
		}
		if bits >= 64 {
			return true
		}
		return n>>bits == 0
	}
	bounds, ok := profileIntegerBounds[base]
	if !ok {
		return false
	}
	n, exact := constant.Int64Val(v)
	if !exact {
		return false
	}
	return n >= bounds[0] && n <= bounds[1]
}

// --- type spelling ----------------------------------------------------------

func namedBase(t syntax.BashPPTypeExpr) string {
	switch x := t.(type) {
	case *syntax.BashPPNamedType:
		return x.Name.Value
	case *syntax.BashPPPointerType:
		return namedBase(x.Element)
	}
	return ""
}

// profileTypeText spells a type the way the source diagnostics do.
func profileTypeText(t syntax.BashPPTypeExpr) string {
	switch x := t.(type) {
	case *syntax.BashPPNamedType:
		if len(x.TypeArgs) == 0 {
			return x.Name.Value
		}
		args := make([]string, len(x.TypeArgs))
		for i, arg := range x.TypeArgs {
			args[i] = profileTypeText(arg.ArgType)
		}
		return x.Name.Value + "[" + strings.Join(args, ", ") + "]"
	case *syntax.BashPPTypeParamType:
		return x.Name.Value
	case *syntax.BashPPUnionType:
		terms := make([]string, len(x.Terms))
		for i, term := range x.Terms {
			terms[i] = profileTypeText(term)
		}
		return strings.Join(terms, " | ")
	case *syntax.BashPPApproxType:
		return "~" + profileTypeText(x.Term)
	case *syntax.BashPPCollectionType:
		if x.Kind == "map" {
			return "map[" + profileTypeText(x.Key) + "]" + profileTypeText(x.Element)
		}
		length := ""
		if x.Length != nil {
			length = x.Length.Value
		}
		return "[" + length + "]" + profileTypeText(x.Element)
	case *syntax.BashPPStructType:
		return "struct"
	case *syntax.BashPPPointerType:
		return "*" + profileTypeText(x.Element)
	case *syntax.BashPPInterfaceType:
		return "interface"
	}
	return "<inferred>"
}
