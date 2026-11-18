// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import "github.com/bashsharp/bashsharp/engine/syntax"

// The lexical scope walker behind GoSource task capture.
//
// It answers one question about a launched body: which names does this body
// USE that it does not itself BIND? That is the free-variable set Go captures
// by reference, and gosource_task_capture.go turns it into the set of cells a
// task shares with its parent.
//
// Three properties matter, and the walker is structured around them:
//
//  1. USES ONLY. An identifier counts when it appears where a value is read or
//     written. The text of a string literal never counts, a selector's field
//     name never counts, a struct literal's field key never counts, and a type
//     never counts. This is why the walker descends the positioned
//     [syntax.BashPPExpr] tree by case rather than collecting every
//     [syntax.Lit] the body contains.
//
//  2. BINDINGS SHADOW. Every construct that introduces a name pushes it into
//     the innermost frame at the point Go would: after its own initializer, so
//     `x := x` reads the outer x and binds a new one. A use resolved by any
//     enclosing frame is not free, so a body's own local never nominates the
//     outer cell it shadows.
//
//  3. EXACT OR NOTHING. The walker reports whether it modelled everything it
//     saw. A construct it does not model sets exact=false; the caller then
//     shares nothing at all rather than guessing. Adding a construct is
//     therefore additive and safe: until it is added, the affected program gets
//     the old deep-copy answer, never a wrong alias.
//
// Identifiers use [syntax.BashPPValidIdent], which is the dialect's own
// identifier predicate: it accepts the full Unicode letter set Go accepts, so
// `变量` and `naïve` are variables here exactly as they are in Go, and rejects
// Go's reserved words.

type bashPPGoSourceScope struct {
	frames []map[string]bool
	free   map[string]bool
	exact  bool
}

// bashPPGoSourceFreeNames reports the free variables of a launched body and
// whether the analysis modelled every construct it encountered.
//
// The answer depends only on the immutable parsed body and the bound names,
// so it is computed once per (body, bound) pair and shared: every launch of a
// goroutine re-derives the capture set of every carried function, and without
// the memo that re-walks the whole program's function bodies per go
// statement. The returned map is shared and must not be modified.
func bashPPGoSourceFreeNames(body *syntax.Block, bound map[string]bool) (map[string]bool, bool) {
	if body == nil {
		return bashPPGoSourceFreeNamesWalk(body, bound)
	}
	return bashPPGoSourceFreeNamesMemo(body, bound)
}

func bashPPGoSourceFreeNamesWalk(body *syntax.Block, bound map[string]bool) (map[string]bool, bool) {
	s := &bashPPGoSourceScope{free: make(map[string]bool), exact: true}
	frame := make(map[string]bool, len(bound))
	for name := range bound {
		frame[name] = true
	}
	s.frames = append(s.frames, frame)
	s.block(body)
	return s.free, s.exact
}

func (s *bashPPGoSourceScope) push() { s.frames = append(s.frames, make(map[string]bool)) }
func (s *bashPPGoSourceScope) pop()  { s.frames = s.frames[:len(s.frames)-1] }

func (s *bashPPGoSourceScope) bind(name string) {
	if name == "" || name == "_" {
		return
	}
	s.frames[len(s.frames)-1][name] = true
}

func (s *bashPPGoSourceScope) bindLits(names []*syntax.Lit) {
	for _, name := range names {
		if name != nil {
			s.bind(name.Value)
		}
	}
}

// use records a variable read or write. A name already bound by an enclosing
// frame is the body's own and is not free.
func (s *bashPPGoSourceScope) use(name string) {
	if name == "" || name == "_" || !syntax.BashPPValidIdent(name) {
		return
	}
	for i := len(s.frames) - 1; i >= 0; i-- {
		if s.frames[i][name] {
			return
		}
	}
	s.free[name] = true
}

func (s *bashPPGoSourceScope) useLit(lit *syntax.Lit) {
	if lit != nil {
		s.use(lit.Value)
	}
}

// unknown marks the analysis inexact. The capture set is discarded wholesale
// when this fires and the launch is then REFUSED with a diagnostic, so an
// unmodelled construct costs a reportable gap, never soundness. (It does not
// fall back to the classic deep-copy snapshot: for an original Go program that
// would silently be a different program.)
func (s *bashPPGoSourceScope) unknown() { s.exact = false }

func (s *bashPPGoSourceScope) block(b *syntax.Block) {
	if b == nil {
		return
	}
	s.push()
	defer s.pop()
	s.stmts(b.Stmts)
}

func (s *bashPPGoSourceScope) stmts(stmts []*syntax.Stmt) {
	for _, stmt := range stmts {
		s.stmt(stmt)
	}
}

func (s *bashPPGoSourceScope) stmt(stmt *syntax.Stmt) {
	if stmt == nil {
		return
	}
	if len(stmt.Redirs) > 0 {
		// A redirection is shell I/O, not a Go construct; its words are not
		// modelled here.
		s.unknown()
		return
	}
	s.command(stmt.Cmd)
}

func (s *bashPPGoSourceScope) command(cmd syntax.Command) {
	switch cmd := cmd.(type) {
	case nil:
	case *syntax.Block:
		s.block(cmd)
	case *syntax.BashPPShortDecl:
		s.shortDecl(cmd)
	case *syntax.BashPPDecl:
		s.decl(cmd)
	case *syntax.BashPPConstGroup:
		for _, spec := range cmd.Specs {
			if spec == nil {
				continue
			}
			if spec.InitExpr != nil {
				s.expr(spec.InitExpr)
			} else {
				s.words(spec.Init)
			}
			if spec.Name != nil {
				s.bind(spec.Name.Value)
			}
		}
	case *syntax.BashPPAssign:
		// An assignment to an outer name is a use of that variable: the task
		// writes the cell the parent reads.
		if cmd.TargetExpr != nil {
			s.expr(cmd.TargetExpr)
		} else {
			s.word(cmd.Target)
		}
		for _, name := range cmd.Names {
			s.useLit(name)
		}
		switch {
		case cmd.Call != nil:
			s.call(cmd.Call)
		case cmd.ValueExpr != nil || len(cmd.ValueExprs) > 0:
			s.expr(cmd.ValueExpr)
			for _, value := range cmd.ValueExprs {
				s.expr(value)
			}
		default:
			s.words(cmd.Values)
			s.word(cmd.Value)
		}
	case *syntax.BashPPForAssign:
		s.useLit(cmd.Name)
		s.expr(cmd.Expr)
	case *syntax.BashPPIncDec:
		s.useLit(cmd.Name)
		if cmd.Target != nil {
			s.expr(cmd.Target)
		} else {
			s.word(cmd.TargetWord)
		}
	case *syntax.BashPPUpdate:
		if cmd.Target != nil {
			s.expr(cmd.Target)
		} else {
			s.word(cmd.TargetWord)
		}
		if cmd.Value != nil {
			s.expr(cmd.Value)
		} else {
			s.word(cmd.ValueWord)
		}
	case *syntax.BashPPIf:
		// The init statement's bindings scope over the condition and both
		// branches, exactly as in Go.
		s.push()
		defer s.pop()
		if cmd.Init != nil {
			s.shortDecl(cmd.Init)
		}
		s.expr(cmd.Cond)
		s.block(cmd.Then)
		s.command(cmd.Else)
	case *syntax.BashPPFor:
		s.push()
		defer s.pop()
		s.command(cmd.Init)
		s.expr(cmd.Cond)
		s.command(cmd.Post)
		s.block(cmd.Body)
	case *syntax.BashPPRange:
		s.push()
		defer s.pop()
		s.expr(cmd.Expr)
		s.word(cmd.Chan)
		if cmd.Define.IsValid() {
			s.bindLits(cmd.Names)
		} else {
			for _, name := range cmd.Names {
				s.useLit(name)
			}
		}
		s.block(cmd.Body)
	case *syntax.BashPPSwitch:
		s.push()
		defer s.pop()
		s.command(cmd.Init)
		s.expr(cmd.Tag)
		for _, arm := range cmd.Arms {
			if arm == nil {
				continue
			}
			s.push()
			for _, expr := range arm.Exprs {
				s.expr(expr)
			}
			s.stmts(arm.Stmts)
			s.pop()
		}
	case *syntax.BashPPSelect:
		for _, c := range cmd.Cases {
			if c == nil {
				continue
			}
			s.push()
			s.command(c.Comm)
			s.stmts(c.Stmts)
			s.pop()
		}
	case *syntax.BashPPSend:
		// GoSource's typed operands preserve selector roots and calls that
		// the legacy display words cannot identify as lexical variables.
		if cmd.ChanExpr != nil {
			s.expr(cmd.ChanExpr)
		} else {
			s.word(cmd.Chan)
		}
		if cmd.ValueExpr != nil {
			s.expr(cmd.ValueExpr)
		} else {
			s.word(cmd.Value)
		}
	case *syntax.BashPPReceive:
		if cmd.ChanExpr != nil {
			s.expr(cmd.ChanExpr)
		} else {
			s.word(cmd.Chan)
		}
	case *syntax.BashPPClose:
		s.word(cmd.Chan)
	case *syntax.BashPPCommandCall:
		// A shell command line inside a Go region is not a Go construct.
		s.unknown()
	case *syntax.BashPPReturn:
		if len(cmd.ResultExprs) > 0 {
			for _, result := range cmd.ResultExprs {
				s.expr(result)
			}
		} else if cmd.Call != nil {
			s.call(cmd.Call)
		} else if cmd.Expr != nil {
			s.expr(cmd.Expr)
		} else if cmd.FuncLit != nil {
			s.funcLit(cmd.FuncLit)
		} else {
			s.words(cmd.Results)
		}
	case *syntax.BashPPBranch:
	case *syntax.BashPPGoto:
	case *syntax.BashPPLabeled:
		s.stmt(cmd.Stmt)
	case *syntax.BashPPGo:
		s.call(cmd.Call)
	case *syntax.BashPPDefer:
		s.call(cmd.Call)
	case *syntax.BashPPCall:
		s.call(cmd)
	default:
		s.unknown()
	}
}

func (s *bashPPGoSourceScope) shortDecl(d *syntax.BashPPShortDecl) {
	if d == nil {
		return
	}
	// Go evaluates the right-hand side before the new names exist, so `x := x`
	// reads the outer x. Order matters here for exactly that reason.
	// When a positioned form is present it is the source of record: the words
	// beside it are the SOURCE SPELLING of the same expression, and reading a
	// name out of that text would be guessing at exactly the level this walker
	// exists to stop guessing at. Only a declaration with no positioned form
	// falls back to its words.
	switch {
	case len(d.RhsExprs) > 0:
		for _, rhs := range d.RhsExprs {
			s.expr(rhs)
		}
	case d.Expr != nil:
		s.expr(d.Expr)
	case d.Call != nil:
		s.call(d.Call)
	case d.FuncLit != nil:
		s.funcLit(d.FuncLit)
	case d.Recv != nil:
		// Receive declarations (including select cases) use the same typed
		// operand traversal as standalone receives, before binding the LHS.
		s.command(d.Recv)
	case d.MakeChan != nil:
		s.word(d.MakeChan.Capacity)
	case len(d.MethodValue) > 0:
		// `f := v.M` reads v; M is a method name.
		s.useLit(d.MethodValue[0])
	default:
		s.words(d.Rhs)
	}
	s.bindLits(d.Lhs)
}

func (s *bashPPGoSourceScope) decl(d *syntax.BashPPDecl) {
	if d == nil {
		return
	}
	if d.Site == syntax.StartTypeDecl {
		// A type declaration binds a type name, not a variable, and its body
		// contains no value expressions.
		return
	}
	if d.InitExpr != nil {
		s.expr(d.InitExpr)
	} else {
		s.words(d.Init)
	}
	if d.Name != nil {
		s.bind(d.Name.Value)
	}
}

func (s *bashPPGoSourceScope) call(c *syntax.BashPPCall) {
	if c == nil {
		return
	}
	switch {
	case c.CalleeExpr != nil:
		s.expr(c.CalleeExpr)
	case c.FuncLit != nil:
		s.funcLit(c.FuncLit)
	case len(c.Fun) > 0:
		// Only the ROOT of a selector chain can name a variable: `x` in
		// `x.y.z()`. The trailing literals are field and method names.
		s.useLit(c.Fun[0])
	}
	if len(c.ArgExprs) > 0 {
		for _, arg := range c.ArgExprs {
			s.expr(arg)
		}
		return
	}
	s.words(c.Args)
}

func (s *bashPPGoSourceScope) funcLit(lit *syntax.BashPPFuncLit) {
	if lit == nil {
		return
	}
	// A nested closure's own free variables are free in the enclosing body too:
	// the outer body is what must capture them so the inner one can. Its
	// parameters and named results shadow, as any binding does.
	s.push()
	defer s.pop()
	bashPPGoSourceAddFieldNames(s.frames[len(s.frames)-1], lit.Params)
	bashPPGoSourceAddFieldNames(s.frames[len(s.frames)-1], lit.Results)
	s.block(lit.Body)
}

func (s *bashPPGoSourceScope) expr(expr syntax.BashPPExpr) {
	switch expr := expr.(type) {
	case nil:
	case *syntax.BashPPBasicLit:
		// A literal's text is never a variable use. This is the counterexample
		// the old collector failed: `fmt.Println("counter")` must not nominate
		// an outer `counter`.
	case *syntax.BashPPIdent:
		s.useLit(expr.Name)
	case *syntax.BashPPParenExpr:
		s.expr(expr.X)
	case *syntax.BashPPUnaryExpr:
		s.expr(expr.X)
	case *syntax.BashPPBinaryExpr:
		s.expr(expr.X)
		s.expr(expr.Y)
	case *syntax.BashPPAddressExpr:
		s.expr(expr.X)
	case *syntax.BashPPDerefExpr:
		s.expr(expr.X)
	case *syntax.BashPPNewExpr:
		// new(T) names a type only.
	case *syntax.BashPPConvertExpr:
		s.expr(expr.X)
	case *syntax.BashPPIndexExpr:
		s.expr(expr.X)
		s.expr(expr.Index)
	case *syntax.BashPPSliceExpr:
		s.expr(expr.X)
		s.expr(expr.Low)
		s.expr(expr.High)
		s.expr(expr.Max)
	case *syntax.BashPPSelectorExpr:
		// Sel is a field or method name, never a variable.
		s.expr(expr.X)
	case *syntax.BashPPTypeAssertExpr:
		s.expr(expr.X)
	case *syntax.BashPPCompositeLit:
		s.compositeLit(expr)
	case *syntax.BashPPCall:
		s.call(expr)
	case *syntax.BashPPFuncLit:
		s.funcLit(expr)
	default:
		s.unknown()
	}
}

// compositeLit distinguishes a keyed collection element, whose key is an
// expression, from a struct field key, which is a field NAME and no more a
// variable use than a selector's tail is.
func (s *bashPPGoSourceScope) compositeLit(lit *syntax.BashPPCompositeLit) {
	keyed := false
	switch lit.LitType.(type) {
	case *syntax.BashPPCollectionType:
		keyed = true
	}
	for _, elem := range lit.Elems {
		if elem == nil {
			continue
		}
		if elem.Key != nil {
			if keyed {
				s.expr(elem.Key)
			} else if _, ident := elem.Key.(*syntax.BashPPIdent); !ident {
				// A non-identifier key on a non-collection literal is not a
				// shape this walker models.
				s.unknown()
			}
		}
		s.expr(elem.Value)
	}
}

func (s *bashPPGoSourceScope) words(words []*syntax.Word) {
	for _, w := range words {
		s.word(w)
	}
}

// word classifies the legacy word surface a GoSource tree can still carry
// beside its positioned expressions.
//
// Exactly two shapes are modelled, and both follow the established Bash++ call
// convention: a word that is one bare identifier is that binding's value, and a
// word that is pure literal text — including any quoted string — is not a
// variable use at all. Anything richer (an expansion, a substitution, a mixed
// word) is not classified here; it marks the analysis inexact rather than
// guessing whether its text names a variable.
func (s *bashPPGoSourceScope) word(w *syntax.Word) {
	if w == nil {
		return
	}
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok {
			if syntax.BashPPValidIdent(lit.Value) {
				s.use(lit.Value)
			}
			// A non-identifier bare word is an operator, path or number.
			return
		}
	}
	for _, part := range w.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			// Single-quoted text is data, never a name.
		case *syntax.DblQuoted:
			// Double-quoted text is data too, but it may embed expansions.
			for _, inner := range part.Parts {
				if _, lit := inner.(*syntax.Lit); !lit {
					s.unknown()
					return
				}
			}
		default:
			_ = part
			s.unknown()
			return
		}
	}
}
