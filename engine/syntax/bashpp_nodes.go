// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import "fmt"

// SourceBlock is a raw, fenced foreign-language declaration unit. It is
// recognized only by the Bash++ grammar at a statement boundary. Body is
// opaque to the shell parser; the selected language adapter owns its text.
// Runner, when set, names the function or registered command that processes
// the body instead of the language's own adapter (`~~~tf as iac !my-tofu`).
// Src, when set, is the file the body was read from: the one-line form
// `embed tf "./main.tf" as iac`, which has no fence and no closing line.
type SourceBlock struct {
	Fence      string
	FencePos   Pos
	Language   *Lit
	Alias      *Lit
	Runner     *Lit
	Src        *Lit
	Body       string
	BodyPos    Pos
	ClosingPos Pos
}

func (b *SourceBlock) Pos() Pos { return b.FencePos }
func (b *SourceBlock) End() Pos {
	if b.ClosingPos.IsValid() {
		return posAddCol(b.ClosingPos, len(b.Fence))
	}
	return b.BodyPos
}

// This file holds the Bash++ P1 ("Day-1") typed nodes. It is deliberately a
// separate file from nodes.go: the nodes can be declared, reviewed and merged
// without touching a single line the certification workstream owns, which
// keeps the shared-file diff down to the dispatch call itself.
//
// The P1 command-position dispatch is wired into parser.go, so every node below
// is constructed from real input. BashPPIf is the one form that cannot use the
// bounded-lookahead recognizer; inside a committed function region it instead
// uses the complete parser transaction described in bashpp_if.go.
//
// Two rules from the design of record govern every node below, and both are
// easy to violate later:
//
//   - Classification is stored ONCE, here in the typed AST. No later phase
//     re-reads source text to decide whether a region is Go or shell. A node's
//     existence IS the decision.
//   - Go semicolon insertion applies only INSIDE a committed Go region. The
//     Site field records which start site opened the region so that rule has
//     something to key on.

// StartSite identifies the committed start site that opened a Bash++ region.
// It is recorded on every Bash++ node so that the region's provenance survives
// into the interpreter and the printer, rather than being re-derived.
type StartSite uint8

const (
	// StartNone means no Bash++ start site fired and the input stays shell.
	// It is the zero value on purpose: a node that never had its site set is
	// indistinguishable from shell, which fails safe.
	StartNone StartSite = iota

	StartVar       // var x = 1 · var x int = 1 · var x int
	StartConst     // const K = 2 · const K int = 2
	StartTypeDecl  // type T struct { … } · type ID = string
	StartShortDecl // x := 42 · x, y := f()
	StartGoCall    // f(1, 2) · x.y.z() · clear(m)
	StartGoIf      // if err != nil { … }
	StartImport    // import "path" · import alias "path"
	StartFunc      // func name(a int) int { … }
	StartDefer     // defer f(x)
	StartReturn    // return · return a, b (only inside a func body)
	StartFuncLit   // func(a int) int { … }(1) — a function literal
	StartGo        // go f() · go func() { … }()
	StartSelect    // select { case …: …; default: … }
	StartAgentic   // agentic { … } or agentic function/func declaration
	StartSource    // ~~~python … ~~~
)

func (s StartSite) String() string {
	switch s {
	case StartVar:
		return "var"
	case StartConst:
		return "const"
	case StartTypeDecl:
		return "type"
	case StartShortDecl:
		return ":="
	case StartGoCall:
		return "call"
	case StartGoIf:
		return "if"
	case StartImport:
		return "import"
	case StartFunc:
		return "func"
	case StartDefer:
		return "defer"
	case StartReturn:
		return "return"
	case StartFuncLit:
		return "funclit"
	case StartGo:
		return "go"
	case StartSelect:
		return "select"
	case StartAgentic:
		return "agentic"
	case StartSource:
		return "source-fence"
	}
	return "none"
}

// UnmarshalText is the inverse of [StartSite.String], and exists because
// sh/syntax/typedjson encodes a node's small enums by their string form so the
// wire format survives new values being added. Without the inverse, a tree
// containing a Bash++ node encodes cleanly and then fails to decode, which is
// the worst place to discover a missing method.
func (s *StartSite) UnmarshalText(b []byte) error {
	switch string(b) {
	case "none":
		*s = StartNone
	case "agentic":
		*s = StartAgentic
	case "var":
		*s = StartVar
	case "const":
		*s = StartConst
	case "type":
		*s = StartTypeDecl
	case ":=":
		*s = StartShortDecl
	case "call":
		*s = StartGoCall
	case "if":
		*s = StartGoIf
	case "import":
		*s = StartImport
	case "func":
		*s = StartFunc
	case "defer":
		*s = StartDefer
	case "return":
		*s = StartReturn
	case "funclit":
		*s = StartFuncLit
	case "go":
		*s = StartGo
	case "select":
		*s = StartSelect
	case "source-fence":
		*s = StartSource
	default:
		return fmt.Errorf("unknown Bash++ start site: %q", b)
	}
	return nil
}

// SiteClass is the compatibility class of a start site, as measured against
// stock bash 5.3 by bashpp-tests/tools/startsites and published in the design
// of record.
//
// The distinction is not cosmetic; it decides what happens to an unsupported
// form. A Class R shape is already a bash syntax error, so Bash++ may answer
// with a diagnostic — nothing legal is being taken away. A Class E shape runs
// today as an ordinary command, so an unsupported Class E form must fall back
// to the shell and must NEVER produce a diagnostic: emitting one would break a
// working script.
type SiteClass uint8

const (
	// ClassR — stock bash 5.3 REJECTS the shape. Purely additive.
	ClassR SiteClass = iota + 1
	// ClassE — stock bash 5.3 ACCEPTS the shape. Meaning-changing, and so it
	// carries a published table row, a near-miss fallback and a shell escape.
	ClassE
)

func (c SiteClass) String() string {
	switch c {
	case ClassR:
		return "R"
	case ClassE:
		return "E"
	}
	return "?"
}

// UnmarshalText is the inverse of [SiteClass.String]; see
// [StartSite.UnmarshalText] for why it exists.
func (c *SiteClass) UnmarshalText(b []byte) error {
	switch string(b) {
	case "R":
		*c = ClassR
	case "E":
		*c = ClassE
	default:
		return fmt.Errorf("unknown Bash++ site class: %q", b)
	}
	return nil
}

// BashPPTypeIdentity preserves a checked Go declaration's identity independently
// of the linker's private name and the native helper's implementation package.
type BashPPTypeIdentity struct {
	Name, PackageName, PackagePath string
}

// BashPPDecl is a Go declaration in command position: var, const or type.
//
// It covers the three keyword-led Day-1 sites, which share a shape (keyword,
// name, optional type, optional value) and differ only in the keyword. They
// are one node rather than three because the interpreter treats them
// identically apart from mutability, which Kw already records.
//
// DeclType is spelled that way rather than the obvious `Type` because
// sh/syntax/typedjson reserves a `Type` key on every tagged node to carry the
// node's own type name, and builds its encoding struct by reflection: a node
// field called Type collides with it and panics reflect.StructOf. That is not
// a hypothetical — it is what wiring the dispatch turned up the first time a
// BashPPDecl could reach an encoder — and it is worth a line here because the
// collision is invisible until a tree containing one is serialized.
type BashPPDecl struct {
	GoTypeIdentity *BashPPTypeIdentity

	Site       StartSite // StartVar, StartConst or StartTypeDecl
	Kw         *Lit      // the literal "var", "const" or "type" as written
	Name       *Lit      // the declared identifier
	TypeParams []*BashPPTypeParam
	DeclType   *Lit // the declared type, or nil when inferred
	// DeclTypeExpr is the lowering-ready form of a declared value type. It is
	// populated for the struct/collection surface while DeclType retains the
	// original compact spelling used by older API consumers.
	DeclTypeExpr BashPPTypeExpr
	Alias        bool    // whether a type declaration uses the `=` alias form
	Init         []*Word // the initializer, or nil for a bare declaration
	// InitExpr is populated whenever Init is a supported typed expression.
	// Keeping Init preserves shell quoting and backwards compatibility; the
	// interpreter consumes only this typed form for structured values.
	InitExpr BashPPExpr
	// StructFields is non-empty for the closed Bash# struct declaration
	// surface, `type T struct { Name string; ... }`. DeclType is the `struct`
	// literal in that form; the braces retain their source positions.
	StructFields []*BashPPField
	// EnumMembers is non-empty for the closed Bash# enum declaration
	// surface, `type Color enum { Red; Green }`. Members retain their source
	// positions so diagnostics and typed JSON never need to split source text.
	EnumMembers []*Lit
	Lbrace      Pos
	Rbrace      Pos

	// End_ is the end of the declaration, which for a multi-line type
	// declaration is the closing brace rather than the end of Init.
	End_ Pos
}

// BashPPConstGroup is a parenthesized Go constant declaration. A spec with a
// nil Init repeats the preceding non-empty type and expression; Iota records
// the zero-based ConstSpec index used when that expression is evaluated.
type BashPPConstGroup struct {
	Kw             *Lit
	Lparen, Rparen Pos
	Comments, Last []Comment
	Specs          []*BashPPConstSpec
}

func (g *BashPPConstGroup) Pos() Pos { return g.Kw.Pos() }
func (g *BashPPConstGroup) End() Pos { return posAddCol(g.Rparen, 1) }

type BashPPConstSpec struct {
	Comments     []Comment
	Name         *Lit
	DeclType     *Lit
	DeclTypeExpr BashPPTypeExpr
	Eq           Pos
	Init         []*Word
	InitExpr     BashPPExpr
	ImplicitInit bool
	Iota         uint32
}

func (s *BashPPConstSpec) Pos() Pos { return s.Name.Pos() }
func (s *BashPPConstSpec) End() Pos {
	if len(s.Init) > 0 {
		return s.Init[len(s.Init)-1].End()
	}
	if s.DeclType != nil {
		return s.DeclType.End()
	}
	return s.Name.End()
}

func (d *BashPPDecl) Pos() Pos { return d.Kw.Pos() }
func (d *BashPPDecl) End() Pos {
	if d.End_.IsValid() {
		return d.End_
	}
	if len(d.Init) > 0 {
		return d.Init[len(d.Init)-1].End()
	}
	if d.DeclType != nil {
		return d.DeclType.End()
	}
	return d.Name.End()
}

// BashPPAssign is a Go-form assignment used by Bash# deep-readonly checks.
// Target and Value remain words so the AST preserves quote structure and byte
// positions; the interpreter parses their deliberately small expression
// grammar only after the node has passed the Bash++/POSIX runtime gates.
type BashPPAssign struct {
	Target *Word
	// Names/Values carry general identifier tuple assignment inside an already
	// committed Go region. Target/Value remain the compatibility surface for
	// the older single structured assignment forms.
	Names  []*Lit
	Values []*Word
	Eq     Pos
	Value  *Word
	// TargetExpr and ValueExpr are populated for the supported collection
	// assignment surface. The words remain for the older struct/readonly
	// surface until selectors are lowered in a later slice.
	TargetExpr BashPPExpr
	ValueExpr  BashPPExpr
	ValueExprs []BashPPExpr
	// Call is set when the right side is a Go-form call. Value retains its
	// exact source span for compatibility.
	Call *BashPPCall
	// Recv is the receive right-hand side of an assignment in a select
	// communication clause. It keeps `=` receive clauses on the ordinary
	// assignment node while preserving the channel operand for the scheduler.
	Recv *BashPPReceive
}

func (a *BashPPAssign) Pos() Pos {
	if len(a.Names) > 0 {
		return a.Names[0].Pos()
	}
	return a.Target.Pos()
}
func (a *BashPPAssign) End() Pos {
	if a.Call != nil {
		return a.Call.End()
	}
	if len(a.Values) > 0 {
		return a.Values[len(a.Values)-1].End()
	}
	if a.Recv != nil {
		return a.Recv.End()
	}
	return a.Value.End()
}

// BashPPShortDecl is a Go short variable declaration: x := 42, x, y := f().
//
// Lhs is a slice because the multi-value form is what makes several of these
// shapes Class R: a parenthesised call after := is already a bash syntax
// error, while the bare scalar form is not. Both spellings land here; Class
// records which one fired, so the compatibility contract is queryable from the
// tree instead of being re-derived from source.
type BashPPShortDecl struct {
	RhsExprs []BashPPExpr // positioned expressions for Go-source parallel RHS evaluation
	Lhs      []*Lit       // one name, or several for the tuple form
	Rhs      []*Word      // the right-hand side, unevaluated
	Class    SiteClass
	OpPos    Pos // position of :=
	// GoRegion is true when the declaration was parsed inside an already
	// committed Go region, such as a Bash++ function body. It lets the
	// interpreter diagnose expression errors there while preserving the legacy
	// Class-E top-level fallback for bare shell words.
	GoRegion bool

	// Call is set when the right-hand side is a Go-form call, e.g.
	// `x := f(1)` or `config, err := readConfig("c")`. It carries the parsed
	// call so the interpreter can invoke a typed function and bind its results
	// to Lhs positionally, rather than re-deriving the call from Rhs's text.
	// It is nil for every scalar/tuple/composite right-hand side.
	Call *BashPPCall

	// FuncLit is set when the right-hand side is a function literal bound to
	// a name, `greet := func(who string) { … }`. Rhs is empty then: a closure
	// has no word spelling to re-expand, and the literal's own body is the
	// value. When the literal is immediately invoked — `n := func() int { …
	// }()` — Call carries the invocation and FuncLit stays nil, because what
	// is bound is the call's result rather than the function.
	FuncLit *BashPPFuncLit

	// MethodValue is set for a selector captured as a value, `f := v.M`.
	// Rhs retains the original word for compatibility; this field records the
	// selector structure so neither the printer nor interpreter must split
	// source text to rediscover it.
	MethodValue []*Lit

	// Recv is set when the right-hand side is a channel receive, `v := <-ch`
	// or the two-value `v, ok := <-ch`. Rhs is empty then: bash parses the
	// arrow as a redirect, so there is no word spelling to keep, and the arrow
	// and channel positions live on the [BashPPReceive] instead. Lhs holds one
	// name for the value form and two for the value-and-open form, which is
	// the only place Go admits a second name on a receive.
	Recv *BashPPReceive

	// MakeChan is set for `ch := make(chan T, n)`. It is separate from Call
	// because `make` is not a call the interpreter can dispatch — it is the Go
	// builtin, and its first argument is a TYPE rather than a value, which no
	// [BashPPCall] argument list can hold.
	MakeChan *BashPPMakeChan

	// Expr is the typed scalar expression for the deliberately small scalar
	// subset admitted inside a committed Go region. It is nil for the Class-E
	// top-level spelling, whose words must keep their ordinary shell meaning.
	// Rhs is empty when Expr is set: the expression tree is then the source of
	// record for both printing and evaluation.
	Expr BashPPExpr
}

func (d *BashPPShortDecl) Pos() Pos {
	if len(d.Lhs) > 0 {
		return d.Lhs[0].Pos()
	}
	return d.OpPos
}

func (d *BashPPShortDecl) End() Pos {
	if len(d.Rhs) > 0 {
		return d.Rhs[len(d.Rhs)-1].End()
	}
	if d.FuncLit != nil {
		return d.FuncLit.End()
	}
	if d.Recv != nil {
		return d.Recv.End()
	}
	if d.MakeChan != nil {
		return d.MakeChan.End()
	}
	if d.Expr != nil {
		return d.Expr.End()
	}
	return posAddCol(d.OpPos, 2)
}

// BashPPExpr is the source-reachable scalar expression subset. These nodes
// deliberately model only forms which the shell lexer can carry into an
// already committed Bash++ region; they are not a claim to support arbitrary
// Go operators in shell command position.
type BashPPExpr interface {
	Node
	bashPPExprNode()
}

func (*BashPPBasicLit) bashPPExprNode()       {}
func (*BashPPIdent) bashPPExprNode()          {}
func (*BashPPParenExpr) bashPPExprNode()      {}
func (*BashPPUnaryExpr) bashPPExprNode()      {}
func (*BashPPBinaryExpr) bashPPExprNode()     {}
func (*BashPPConvertExpr) bashPPExprNode()    {}
func (*BashPPIndexExpr) bashPPExprNode()      {}
func (*BashPPSliceExpr) bashPPExprNode()      {}
func (*BashPPSelectorExpr) bashPPExprNode()   {}
func (*BashPPCompositeLit) bashPPExprNode()   {}
func (*BashPPAddressExpr) bashPPExprNode()    {}
func (*BashPPDerefExpr) bashPPExprNode()      {}
func (*BashPPNewExpr) bashPPExprNode()        {}
func (*BashPPTypeAssertExpr) bashPPExprNode() {}
func (*BashPPCall) bashPPExprNode()           {}
func (*BashPPFuncLit) bashPPExprNode()        {}

// BashPPBasicLit is an exact scalar literal. Kind uses Go token names (INT,
// FLOAT, CHAR, STRING), retained as text so typed JSON remains stable.
type BashPPBasicLit struct {
	Value *Lit
	Kind  string
}

func (x *BashPPBasicLit) Pos() Pos { return x.Value.Pos() }
func (x *BashPPBasicLit) End() Pos { return x.Value.End() }

// BashPPIdent is an identifier or the predeclared true/false spelling.
type BashPPIdent struct{ Name *Lit }

func (x *BashPPIdent) Pos() Pos { return x.Name.Pos() }
func (x *BashPPIdent) End() Pos { return x.Name.End() }

type BashPPTypeAssertExpr struct {
	X         BashPPExpr
	Dot       Pos
	Lparen    Pos
	Assert    BashPPTypeExpr
	TypeToken *Lit // non-nil for the type-switch guard spelling .(type)
	Rparen    Pos
}

func (x *BashPPTypeAssertExpr) Pos() Pos { return x.X.Pos() }
func (x *BashPPTypeAssertExpr) End() Pos { return posAddCol(x.Rparen, 1) }

type BashPPParenExpr struct {
	Lparen Pos
	X      BashPPExpr
	Rparen Pos
}

func (x *BashPPParenExpr) Pos() Pos { return x.Lparen }
func (x *BashPPParenExpr) End() Pos { return posAddCol(x.Rparen, 1) }

type BashPPUnaryExpr struct {
	Op *Lit
	X  BashPPExpr
}

// BashPPAddressExpr and BashPPDerefExpr retain the operator positions for
// lowering. They are distinct from ordinary unary arithmetic because their
// operands are locations and pointer values, respectively.
type BashPPAddressExpr struct {
	Amp Pos
	X   BashPPExpr
}

func (x *BashPPAddressExpr) Pos() Pos { return x.Amp }
func (x *BashPPAddressExpr) End() Pos { return x.X.End() }

type BashPPDerefExpr struct {
	Star Pos
	X    BashPPExpr
}

func (x *BashPPDerefExpr) Pos() Pos { return x.Star }
func (x *BashPPDerefExpr) End() Pos { return x.X.End() }

// BashPPNewExpr is the predeclared allocation expression new(T), or Go 1.27's
// new(v). AllocType always records the allocated element type; Init is non-nil
// only for the value form.
type BashPPNewExpr struct {
	New       *Lit
	Lparen    Pos
	AllocType BashPPTypeExpr
	Init      BashPPExpr
	Rparen    Pos
}

func (x *BashPPNewExpr) Pos() Pos { return x.New.Pos() }
func (x *BashPPNewExpr) End() Pos { return posAddCol(x.Rparen, 1) }

func (x *BashPPUnaryExpr) Pos() Pos { return x.Op.Pos() }
func (x *BashPPUnaryExpr) End() Pos { return x.X.End() }

type BashPPBinaryExpr struct {
	X  BashPPExpr
	Op *Lit
	Y  BashPPExpr
}

func (x *BashPPBinaryExpr) Pos() Pos { return x.X.Pos() }
func (x *BashPPBinaryExpr) End() Pos { return x.Y.End() }

// BashPPConvertExpr is a source-reachable one-argument conversion.
type BashPPConvertExpr struct {
	// GoStringConstant records the original go/types constant-string operand.
	GoStringConstant bool
	// ConvTypeExpr preserves structured Go-source targets; ConvType retains legacy spelling.
	ConvTypeExpr BashPPTypeExpr
	ConvType     *Lit
	Lparen       Pos
	X            BashPPExpr
	Rparen       Pos
}

// BashPPIndexExpr is a single indexed read. Chaining is represented by X
// containing another BashPPIndexExpr, so every bracket retains its position.
type BashPPIndexExpr struct {
	GoString bool // Go-source base has underlying string type
	X        BashPPExpr
	Lbrack   Pos
	Index    BashPPExpr
	Rbrack   Pos
}

func (x *BashPPIndexExpr) Pos() Pos { return x.X.Pos() }
func (x *BashPPIndexExpr) End() Pos { return posAddCol(x.Rbrack, 1) }

// BashPPSliceExpr is a two-index or full three-index slice expression. Low,
// High, and Max are nil when their source slots are empty.
type BashPPSliceExpr struct {
	GoString    bool // Go-source base has underlying string type
	X           BashPPExpr
	Lbrack      Pos
	Low         BashPPExpr
	Colon       Pos
	High        BashPPExpr
	SecondColon Pos
	Max         BashPPExpr
	Rbrack      Pos
}

func (x *BashPPSliceExpr) Pos() Pos { return x.X.Pos() }
func (x *BashPPSliceExpr) End() Pos { return posAddCol(x.Rbrack, 1) }

// BashPPSelectorExpr is a positioned field selection. Chained selections and
// selections through indexed collections are represented recursively in X.
type BashPPSelectorExpr struct {
	MethodValue         bool            // Go-source selector denotes a method value, not a function-valued field
	ReceiverAddressable bool            // Original receiver is addressable according to go/types
	FuncType            *BashPPFuncType // Go-source function value signature, when applicable
	X                   BashPPExpr
	Dot                 Pos
	Sel                 *Lit
}

func (x *BashPPSelectorExpr) Pos() Pos { return x.X.Pos() }
func (x *BashPPSelectorExpr) End() Pos { return x.Sel.End() }

// BashPPTypeExpr is the closed type vocabulary accepted by collection
// literals in this implementation slice.
type BashPPTypeExpr interface {
	Node
	bashPPTypeExprNode()
}

func (*BashPPFuncType) bashPPTypeExprNode() {}
func (*BashPPChanType) bashPPTypeExprNode() {}

func (*BashPPNamedType) bashPPTypeExprNode()      {}
func (*BashPPCollectionType) bashPPTypeExprNode() {}
func (*BashPPStructType) bashPPTypeExprNode()     {}
func (*BashPPPointerType) bashPPTypeExprNode()    {}
func (*BashPPInterfaceType) bashPPTypeExprNode()  {}
func (*BashPPTypeParamType) bashPPTypeExprNode()  {}
func (*BashPPUnionType) bashPPTypeExprNode()      {}
func (*BashPPApproxType) bashPPTypeExprNode()     {}

// BashPPFuncType is a concrete callable signature, distinct from the legacy
// untyped `func` marker. Every parameter/result retains its positioned type.
type BashPPFuncType struct {
	Func                 Pos
	Lparen, Rparen       Pos
	Params, Results      []*BashPPField
	ResLparen, ResRparen Pos
}

func (t *BashPPFuncType) Pos() Pos { return t.Func }
func (t *BashPPFuncType) End() Pos {
	if t.ResRparen.IsValid() {
		return posAddCol(t.ResRparen, 1)
	}
	if len(t.Results) > 0 {
		return t.Results[len(t.Results)-1].End()
	}
	return posAddCol(t.Rparen, 1)
}

type BashPPNamedType struct {
	// LocalSync is a front-end ownership certificate, never inferred at runtime.
	LocalSync string
	Name      *Lit
	TypeArgs  []*BashPPTypeArg
}

func (t *BashPPNamedType) Pos() Pos { return t.Name.Pos() }
func (t *BashPPNamedType) End() Pos {
	if len(t.TypeArgs) > 0 {
		return t.TypeArgs[len(t.TypeArgs)-1].End()
	}
	return t.Name.End()
}

// BashPPTypeParamType is a use of a function type parameter inside a
// parameter/result type. It is distinct from BashPPNamedType so lowering and
// runtime substitution never have to guess whether T names a script type or a
// generic parameter.
type BashPPTypeParamType struct{ Name *Lit }

func (t *BashPPTypeParamType) Pos() Pos { return t.Name.Pos() }
func (t *BashPPTypeParamType) End() Pos { return t.Name.End() }

// BashPPUnionType is the type-set union spelling used inside generic
// constraints, for example `~int | string`.
type BashPPUnionType struct {
	Terms []BashPPTypeExpr
	Bars  []*Lit
}

func (t *BashPPUnionType) Pos() Pos { return t.Terms[0].Pos() }
func (t *BashPPUnionType) End() Pos { return t.Terms[len(t.Terms)-1].End() }

// BashPPApproxType is the `~T` approximation term inside a type-set
// constraint.
type BashPPApproxType struct {
	Tilde Pos
	Term  BashPPTypeExpr
}

func (t *BashPPApproxType) Pos() Pos { return t.Tilde }
func (t *BashPPApproxType) End() Pos { return t.Term.End() }

// BashPPPointerType represents *T. Star is retained independently from the
// element's position for exact diagnostics and source-to-source lowering.
type BashPPPointerType struct {
	Star    Pos
	Element BashPPTypeExpr
}

func (t *BashPPPointerType) Pos() Pos { return t.Star }
func (t *BashPPPointerType) End() Pos { return t.Element.End() }

// BashPPCollectionType represents [N]T, [...]T, []T, or map[K]V. Kind is one
// of "array", "inferred-array", "slice", and "map".
type BashPPCollectionType struct {
	Kind    string
	Start   Pos
	Lbrack  Pos
	Length  *Lit
	Rbrack  Pos
	Key     BashPPTypeExpr
	Element BashPPTypeExpr
}

func (t *BashPPCollectionType) Pos() Pos { return t.Start }
func (t *BashPPCollectionType) End() Pos {
	if t.Element != nil {
		return t.Element.End()
	}
	return posAddCol(t.Rbrack, 1)
}

// BashPPStructType is an anonymous struct type as written in a composite
// literal. Named declarations use the same positioned BashPPField entries.
type BashPPStructType struct {
	Struct *Lit
	Lbrace Pos
	Fields []*BashPPField
	Rbrace Pos
}

func (t *BashPPStructType) Pos() Pos { return t.Struct.Pos() }
func (t *BashPPStructType) End() Pos { return posAddCol(t.Rbrace, 1) }

// BashPPInterfaceType is an anonymous interface type with explicit method
// specifications and embedded interface type elements.
type BashPPInterfaceType struct {
	Interface *Lit
	Lbrace    Pos
	Elems     []*BashPPInterfaceElem
	Methods   []*BashPPMethodSpec
	Rbrace    Pos
}

func (t *BashPPInterfaceType) Pos() Pos { return t.Interface.Pos() }
func (t *BashPPInterfaceType) End() Pos { return posAddCol(t.Rbrace, 1) }

type BashPPInterfaceElem struct {
	Method   *BashPPMethodSpec
	Embedded BashPPTypeExpr
}

func (e *BashPPInterfaceElem) Pos() Pos {
	if e.Method != nil {
		return e.Method.Pos()
	}
	return e.Embedded.Pos()
}
func (e *BashPPInterfaceElem) End() Pos {
	if e.Method != nil {
		return e.Method.End()
	}
	return e.Embedded.End()
}

type BashPPMethodSpec struct {
	Name      *Lit
	Params    []*BashPPField
	Results   []*BashPPField
	Lparen    Pos
	Rparen    Pos
	ResLparen Pos
	ResRparen Pos
}

func (s *BashPPMethodSpec) Pos() Pos { return s.Name.Pos() }
func (s *BashPPMethodSpec) End() Pos {
	if s.ResRparen.IsValid() {
		return posAddCol(s.ResRparen, 1)
	}
	if len(s.Results) > 0 {
		return s.Results[len(s.Results)-1].End()
	}
	return posAddCol(s.Rparen, 1)
}

// BashPPCompositeLit is a lowering-ready collection literal. A nil LitType
// denotes the nested inferred spelling { ... }, whose type comes from its
// containing element. Struct composites deliberately remain on the preserved
// word path for a later story.
type BashPPCompositeLit struct {
	LitType BashPPTypeExpr
	Lbrace  Pos
	Elems   []*BashPPCompositeElem
	Rbrace  Pos
}

func (x *BashPPCompositeLit) Pos() Pos {
	if x.LitType != nil {
		return x.LitType.Pos()
	}
	return x.Lbrace
}
func (x *BashPPCompositeLit) End() Pos { return posAddCol(x.Rbrace, 1) }

type BashPPCompositeElem struct {
	Key   BashPPExpr
	Colon Pos
	Value BashPPExpr
}

func (e *BashPPCompositeElem) Pos() Pos {
	if e.Key != nil {
		return e.Key.Pos()
	}
	return e.Value.Pos()
}
func (e *BashPPCompositeElem) End() Pos { return e.Value.End() }

func (x *BashPPConvertExpr) Pos() Pos { return x.ConvType.Pos() }
func (x *BashPPConvertExpr) End() Pos { return posAddCol(x.Rparen, 1) }

// BashPPCall is a Go call expression in command position: f(1, 2), x.y.z().
//
// Every shape reaching this node is Class R — the parenthesis after a word is
// only legal in bash as a function definition, so `f(1, 2)` is already a
// syntax error and claiming it takes nothing away. This is the free
// disambiguator the whole Day-1 set leans on, and it is why `go build ./...`
// keeps running the Go toolchain while `go worker(a, b)` does not.
type BashPPCall struct {
	// GoRuntime records a checked Go call whose result is not a constant.
	// In particular, unsafe layout calls on type parameters remain runtime
	// values even after instantiation gives the operand a concrete layout.
	GoRuntime bool
	// CalleeExpr preserves computed callees, such as factory().Method.
	// It is mutually exclusive with Fun and FuncLit.
	CalleeExpr BashPPExpr
	Fun        []*Lit  // the selector chain: x.y.z is three literals
	Args       []*Word // the arguments, unevaluated
	// ArgExprs owns arguments of calls nested in a typed scalar expression.
	// When present, it corresponds one-for-one with Args, which retains the
	// original words for legacy consumers. Walk, printing, and evaluation use
	// ArgExprs rather than walking/evaluating both representations.
	ArgExprs []BashPPExpr
	// ExclusiveSliceArgs records arguments whose backing array is proven by
	// the Go front end to have no surviving program alias at this call. The
	// proof is deliberately per call and per argument; consumers must still
	// require their own authority before transferring storage.
	ExclusiveSliceArgs []bool
	// ArgType records the first argument where a predeclared function accepts a
	// type rather than a value. Today only make populates it.
	// Keeping it on the positioned call avoids re-parsing source in interp.
	ArgType BashPPTypeExpr
	// ResultFuncType preserves the concrete underlying signature when the call
	// returns a function value. Range-over-function needs the signature even
	// when the declared result is a named type such as iter.Seq[T].
	ResultFuncType *BashPPFuncType
	// ResultTypes preserves the checked result types of the called signature.
	// Native calls need this when their concrete transport value is assigned by
	// a short declaration: an interface result has a static interface type
	// independent of the dynamic value returned by the dependency.
	ResultTypes []BashPPTypeExpr

	// TypeArgs are the explicit instantiation arguments in f[T, *U](...).
	// They are nil for ordinary inferred calls.
	TypeArgs []*BashPPTypeArg

	// ArgNames names the trailing named arguments in source order. Since the
	// grammar requires positional arguments first, Args[:len(Args)-len(ArgNames)]
	// are positional and each remaining Args entry pairs with an ArgNames entry.
	// This compact form avoids nil placeholders in the public/JSON AST.
	ArgNames []*Lit

	// FuncLit is set when the callee is a function literal rather than a
	// name, which is what an immediately invoked literal — `func(n int) {
	// … }(1)` — is. Fun is empty exactly then, so the two are alternatives
	// and never both set; a reader asks which one is nil rather than
	// re-deriving the shape from the source.
	FuncLit *BashPPFuncLit

	// Ellipsis is the position of the `...` in `f(xs...)`, which passes a
	// slice to a variadic parameter instead of one more argument. Go allows it
	// only on the final argument, so one position on the call is enough.
	Ellipsis Pos

	// PointerMethodExpr records the exact `(*T).M` callee spelling. Fun still
	// contains T,M so selector resolution remains uniform.
	PointerMethodExpr bool
	MethodExprLparen  Pos
	MethodExprRparen  Pos

	Lparen Pos
	Rparen Pos
}

// BashPPCommandCall is a shell command whose final argument is the result of a
// typed Bash# call, as in `printf '%s\n' label(Green)`. Keeping the outer
// command and inner call as nodes avoids inventing a shell expansion spelling
// for a typed return value.
type BashPPCommandCall struct {
	Before []*Word
	Call   *BashPPCall
}

func (c *BashPPCommandCall) Pos() Pos {
	if len(c.Before) > 0 {
		return c.Before[0].Pos()
	}
	return c.Call.Pos()
}
func (c *BashPPCommandCall) End() Pos { return c.Call.End() }

// BashPPImport imports one or more standard-library packages into the
// Runner-local Bash++ namespace.
type BashPPImport struct {
	Site        StartSite
	Class       SiteClass
	Kw          *Lit
	Language    *Lit // non-nil for a direct foreign import
	Lbrack      Pos  // optional named environment, foreign form only
	Environment *Lit
	Rbrack      Pos
	As          *Lit                // non-nil when the foreign alias was explicit
	Alias       *Lit                // nil means the package's declared name (single form)
	Path        *DblQuoted          // non-nil in the single form
	Comments    []Comment           // comments between the keyword and grouped form
	Specs       []*BashPPImportSpec // non-nil in the grouped form
	Last        []Comment           // comments before the closing parenthesis
	Lparen      Pos
	Rparen      Pos
}

func (i *BashPPImport) Pos() Pos { return i.Kw.Pos() }
func (i *BashPPImport) End() Pos {
	if i.Language != nil && i.Alias != nil {
		return i.Alias.End()
	}
	if i.Path != nil {
		return i.Path.End()
	}
	return posAddCol(i.Rparen, 1)
}

// BashPPImportSpec is one exact Go import specification. Alias may be a Go
// identifier, _ or .; nil requests the package's declared name.
type BashPPImportSpec struct {
	Comments []Comment
	Alias    *Lit
	Path     *DblQuoted
}

func (s *BashPPImportSpec) Pos() Pos {
	if s.Alias != nil {
		return s.Alias.Pos()
	}
	return s.Path.Pos()
}
func (s *BashPPImportSpec) End() Pos { return s.Path.End() }

func (c *BashPPCall) Pos() Pos {
	if c.CalleeExpr != nil {
		return c.CalleeExpr.Pos()
	}
	if c.MethodExprLparen.IsValid() {
		return c.MethodExprLparen
	}
	if len(c.Fun) > 0 {
		return c.Fun[0].Pos()
	}
	if c.FuncLit != nil {
		return c.FuncLit.Pos()
	}
	return c.Lparen
}
func (c *BashPPCall) End() Pos { return posAddCol(c.Rparen, 1) }

// BashPPIf is a Go brace-form if: if err != nil { … }.
//
// Init, when non-nil, is the optional short declaration before the header's
// semicolon. InitStmt preserves another Go simple statement initializer; at most
// one of Init and InitStmt is non-nil. Cond is always a typed scalar expression. Else is another
// BashPPIf for `else if`, a Block for `else`, or nil.
type BashPPIf struct {
	Site      StartSite
	If        Pos
	Init      *BashPPShortDecl
	InitStmt  Command
	Semicolon Pos
	Cond      BashPPExpr
	Then      *Block
	ElsePos   Pos
	Else      Command
}

// BashPPFor is a Go brace-form for statement inside a committed Bash++
// function region. A nil Init, Cond, and Post is the infinite form. The two
// semicolon positions distinguish a three-clause loop from a condition-only
// loop without re-reading source text.
type BashPPFor struct {
	For        Pos
	Init       Command
	FirstSemi  Pos
	Cond       BashPPExpr
	SecondSemi Pos
	Post       Command
	Body       *Block
}

func (f *BashPPFor) Pos() Pos { return f.For }
func (f *BashPPFor) End() Pos {
	if f.Body != nil {
		return f.Body.End()
	}
	return f.For
}

// BashPPForAssign is an assignment whose right-hand side is already a
// typed scalar expression. It is deliberately distinct from BashPPAssign,
// which records the older object-mutation surface and retains words for its
// deep-readonly diagnostics.
type BashPPForAssign struct {
	Name *Lit
	Eq   Pos
	Expr BashPPExpr
}

func (a *BashPPForAssign) Pos() Pos { return a.Name.Pos() }
func (a *BashPPForAssign) End() Pos { return a.Expr.End() }

// BashPPIncDec is the scalar ++ or -- statement admitted in a for clause.
type BashPPIncDec struct {
	Name       *Lit // compatibility field for for-clause identifier forms
	TargetWord *Word
	Target     BashPPExpr
	Op         *Lit
}

func (s *BashPPIncDec) Pos() Pos {
	if s.Target != nil {
		return s.Target.Pos()
	}
	if s.TargetWord != nil {
		return s.TargetWord.Pos()
	}
	return s.Name.Pos()
}
func (s *BashPPIncDec) End() Pos { return s.Op.End() }

// BashPPUpdate is a compound assignment statement in a committed Go region.
// TargetWord preserves the exact source while Target and Value are positioned,
// lowering-ready expressions. A nil expression is retained for a deterministic
// runtime diagnostic rather than falling through to shell execution.
type BashPPUpdate struct {
	TargetWord *Word
	Target     BashPPExpr
	Op         *Lit
	ValueWord  *Word
	Value      BashPPExpr
}

func (u *BashPPUpdate) Pos() Pos { return u.TargetWord.Pos() }
func (u *BashPPUpdate) End() Pos { return u.ValueWord.End() }

// BashPPBranch is a Go-form break, continue, or fallthrough. Depth is zero for
// an unlabeled branch and otherwise counts eligible enclosing constructs.
type BashPPBranch struct {
	Kw    *Lit
	Depth uint
}

func (b *BashPPBranch) Pos() Pos { return b.Kw.Pos() }
func (b *BashPPBranch) End() Pos { return b.Kw.End() }

// BashPPLabeled is a Go-form labeled statement: Label names the statement
// that follows the colon so a BashPPGoto can target it. A nil Stmt is Go's
// empty statement (a label at the end of a block). Labeled loops still
// resolve break/continue through BashPPBranch.Depth; the label is carried so
// goto can target the loop and so lowering can emit the source label.
type BashPPLabeled struct {
	Label *Lit
	Colon Pos
	Stmt  *Stmt
}

func (l *BashPPLabeled) Pos() Pos { return l.Label.Pos() }
func (l *BashPPLabeled) End() Pos {
	if l.Stmt != nil {
		return l.Stmt.End()
	}
	return posAddCol(l.Colon, 1)
}

// BashPPGoto is a Go-form goto. Label names a BashPPLabeled statement in the
// same block or an enclosing block of the same function; go/types has already
// rejected jumps into blocks and over variable declarations.
type BashPPGoto struct {
	Kw    *Lit
	Label *Lit
}

func (g *BashPPGoto) Pos() Pos { return g.Kw.Pos() }
func (g *BashPPGoto) End() Pos { return g.Label.End() }

// BashPPSwitch is a Go-form expression switch admitted inside a typed Bash++
// function. A nil Tag is a tagless switch, whose implicit tag is true.
type BashPPSwitch struct {
	Switch     Pos
	Init       Command
	Semicolon  Pos
	Tag        BashPPExpr
	TypeSwitch bool
	Lbrace     Pos
	Arms       []*BashPPSwitchArm
	Rbrace     Pos
}

func (s *BashPPSwitch) Pos() Pos { return s.Switch }
func (s *BashPPSwitch) End() Pos { return posAddCol(s.Rbrace, 1) }

// BashPPSwitchArm is one case clause. Types carries Go type-switch cases;
// Exprs carries expression-switch cases. Both are empty for default.
type BashPPSwitchArm struct {
	Case   Pos // position of case/default
	Exprs  []BashPPExpr
	Types  []BashPPTypeExpr
	Commas []*Lit
	Colon  Pos
	Stmts  []*Stmt
	Last   []Comment
}

func (a *BashPPSwitchArm) Pos() Pos { return a.Case }
func (a *BashPPSwitchArm) End() Pos {
	if len(a.Stmts) > 0 {
		return a.Stmts[len(a.Stmts)-1].End()
	}
	return posAddCol(a.Colon, 1)
}

func (i *BashPPIf) Pos() Pos { return i.If }
func (i *BashPPIf) End() Pos {
	if i.Else != nil {
		return i.Else.End()
	}
	if i.Then != nil {
		return i.Then.End()
	}
	return i.If
}

// BashPPField is one group of a Go-form parameter or result list: `a, b int`,
// a lone untyped parameter name, or a bare result type.
//
// Names carries the identifiers a group declares — several when a type is
// shared, as in `a, b int`. FieldType is the group's declared type, or nil for an
// untyped parameter (a Bash++ extension over Go, `func f(a, b)`). An unnamed
// result — `func f() (int, error)` — is a group with an empty Names and the
// type in FieldType, which is what keeps the result arity queryable from the tree
// without re-reading whether each word was a name or a type.
type BashPPField struct {
	Tag       *Lit   // optional original Go struct tag, including its quotes
	Names     []*Lit // the declared identifiers, empty for an unnamed result type
	FieldType *Lit   // the declared type, or nil for an untyped parameter
	// Embedded distinguishes an ordinary anonymous struct field from an
	// unnamed function result. Its field name is derived from FieldTypeExpr.
	Embedded bool
	// FieldTypeExpr is the lowering-ready type for struct fields. Function
	// signatures retain FieldType alone until their broader type slice lands.
	FieldTypeExpr BashPPTypeExpr
	Default       *Word // the value after `=`, nil for a required parameter
	Equals        Pos   // position of `=` when Default is non-nil

	// Ellipsis is the position of the `...` in a variadic parameter group,
	// `func f(head string, rest ...int)`, and is invalid for every other
	// group. It is a POSITION rather than a bool because the printer must put
	// the dots back where they were written, and because Go's only-final rule
	// is reported against the offending group's own source column.
	//
	// FieldType stays the ELEMENT type (`int` above), as it is in go/ast: the
	// parameter's own type is a slice of it, and spelling the element type is
	// what lets the arity check and the diagnostics talk about the same name
	// the script wrote.
	Ellipsis Pos
}

// BashPPTypeParam is one generic function type-parameter group:
// `T any`, `K comparable`, or `T Reader`. Multiple Names share one
// constraint, mirroring ordinary signature field grouping.
type BashPPTypeParam struct {
	Names      []*Lit
	Constraint BashPPTypeExpr
}

func (p *BashPPTypeParam) Pos() Pos {
	if len(p.Names) > 0 {
		return p.Names[0].Pos()
	}
	return p.Constraint.Pos()
}
func (p *BashPPTypeParam) End() Pos { return p.Constraint.End() }

// BashPPTypeArg is one explicit function instantiation type argument.
type BashPPTypeArg struct {
	ArgType BashPPTypeExpr
}

func (a *BashPPTypeArg) Pos() Pos { return a.ArgType.Pos() }
func (a *BashPPTypeArg) End() Pos { return a.ArgType.End() }

// Variadic reports whether the group is the `...T` form.
func (f *BashPPField) Variadic() bool { return f.Ellipsis.IsValid() }

func (f *BashPPField) Pos() Pos {
	if len(f.Names) > 0 {
		return f.Names[0].Pos()
	}
	if f.Ellipsis.IsValid() {
		return f.Ellipsis
	}
	if f.FieldType != nil {
		return f.FieldType.Pos()
	}
	return Pos{}
}
func (f *BashPPField) End() Pos {
	if f.Tag != nil {
		return f.Tag.End()
	}
	if f.Default != nil {
		return f.Default.End()
	}
	if f.FieldType != nil {
		return f.FieldType.End()
	}
	if len(f.Names) > 0 {
		return f.Names[len(f.Names)-1].End()
	}
	return Pos{}
}

// BashPPFuncDecl is a Go-form typed function declaration:
//
//	func name(a int, b string) (int, error) { … }
//
// Every shape reaching this node is Class R — `func name(…)` is two command
// words before a `(`, which bash rejects, so claiming it takes nothing away
// from a working script. That is why the signature may be parsed forward
// without a transaction: a malformed body is a bash syntax error either way.
type BashPPFuncDecl struct {
	Decorators []*BashPPDecorator // declaration decorators, outermost first
	Agentic    *Lit               // optional bare "agentic" modifier
	Kw         *Lit               // the literal "func"
	Name       *Lit               // the declared function name
	Receiver   *BashPPReceiver    // nil for an ordinary function
	// TypeParams are the type parameters the declaration itself introduces.
	// A METHOD may have them too, independent of its receiver's: Go 1.27
	// accepts `func (r R) M[T any](v T) T`, so the two scopes coexist over one
	// signature and Receiver.TypeParams stays the receiver's alone. Their
	// names may not collide; the parser rejects the collision the way the Go
	// compiler reports `T redeclared in this block`.
	TypeParams []*BashPPTypeParam
	Params     []*BashPPField // the parameter groups, in source order
	Results    []*BashPPField // the result groups, or nil when there are none
	Body       *Block         // the braced body

	Lparen    Pos // ( opening the parameter list
	Rparen    Pos // ) closing the parameter list
	ResLparen Pos // ( opening a parenthesised result list, else invalid
	ResRparen Pos // ) closing a parenthesised result list, else invalid
}

// BashPPReceiver is the single receiver of a method declaration. TypeParams
// are the identifiers introduced by an instantiated generic receiver such as
// (b Box[T]); their constraints come from Box's declaration. A method's OWN
// type parameters are not here — they carry their own constraints and live on
// BashPPFuncDecl.TypeParams.
type BashPPReceiver struct {
	Name       *Lit
	RecvType   *Lit
	TypeParams []*Lit
	Pointer    bool
	Lparen     Pos
	Rparen     Pos
}

func (r *BashPPReceiver) Pos() Pos { return r.Lparen }
func (r *BashPPReceiver) End() Pos { return posAddCol(r.Rparen, 1) }

func (d *BashPPFuncDecl) Pos() Pos {
	if len(d.Decorators) > 0 {
		return d.Decorators[0].Pos()
	}
	if d.Agentic != nil {
		return d.Agentic.Pos()
	}
	return d.Kw.Pos()
}

// BashPPDecorator is one @name(args) line attached to a function declaration.
// Decorators remain in source order, so the first entry is the outermost
// decorator and the entry nearest the declaration is the innermost one.
type BashPPDecorator struct {
	At        Pos
	Name      *Lit
	Args      []*Word
	ArgNames  []*Lit
	Lparen    Pos
	Rparen    Pos
	Semicolon Pos
	Comments  []Comment // comments before the next decorator or declaration
}

func (d *BashPPDecorator) Pos() Pos { return d.At }
func (d *BashPPDecorator) End() Pos { return posAddCol(d.Rparen, 1) }
func (d *BashPPFuncDecl) End() Pos {
	if d.Body != nil {
		return d.Body.End()
	}
	return posAddCol(d.Rparen, 1)
}

// BashPPFuncLit is a Go function literal: an unnamed function written where a
// value is expected.
//
//	greet := func(who string) { echo "hi $who" }
//	func(n int) { echo $n }(1)
//	defer func() { echo done }()
//	return func(extra int) int { return $((base + extra)) }
//
// WHY IT IS NOT A COMMAND. A bare literal is not a statement in Go either; a
// literal always appears as a value — bound by `:=`, invoked immediately,
// deferred, or returned. Each of those sites owns a field pointing here, so
// the tree records WHICH site the literal occupied instead of leaving a
// free-floating node whose meaning a reader would have to infer from context.
//
// THE ONE SHAPE THAT IS NOT CLAIMED. `func() { … }` at a command position is
// the bash function definition of a function NAMED `func`, and it is legal
// shell today. Only the trailing `()` after the matching `}` tells the two
// apart, which is unbounded lookahead of exactly the kind
// bashpp_startsites.go forbids. So a command-position literal must carry a
// parameter — `func(n int) { … }(1)` — and the parameterless invocation is
// spelled `_ := func() { … }()`, where the `:=` prefix already commits the
// region. Every other literal site is unambiguous from its prefix.
type BashPPFuncLit struct {
	Kw      *Lit           // the literal "func"
	Params  []*BashPPField // the parameter groups, in source order
	Results []*BashPPField // the result groups, or nil when there are none
	Body    *Block         // the braced body

	Lparen    Pos // ( opening the parameter list
	Rparen    Pos // ) closing the parameter list
	ResLparen Pos // ( opening a parenthesised result list, else invalid
	ResRparen Pos // ) closing a parenthesised result list, else invalid
}

func (l *BashPPFuncLit) Pos() Pos {
	if l == nil || l.Kw == nil {
		return Pos{}
	}
	return l.Kw.Pos()
}
func (l *BashPPFuncLit) End() Pos {
	if l == nil {
		return Pos{}
	}
	if l.Body != nil {
		return l.Body.End()
	}
	if l.ResRparen.IsValid() {
		return posAddCol(l.ResRparen, 1)
	}
	return posAddCol(l.Rparen, 1)
}

// BashPPReturn is a Go-form return inside a func body: `return`, `return a, b`.
//
// It is only ever constructed inside a Bash++ func body, where the parser
// tracks that it is within a committed Go region; a `return` anywhere else
// stays the ordinary shell builtin, which is Class E. Results is nil for a bare
// return, which yields the function's named results (or its last status when it
// declares none).
type BashPPReturn struct {
	ResultExprs []BashPPExpr // positioned Go-source results, evaluated before binding
	Kw          *Lit         // the literal "return"
	Results     []*Word      // the returned values, or nil for a bare return
	// Call retains the positioned AST for a single returned call. Results keeps
	// its complete legacy Word spelling for consumers that still read values.
	Call *BashPPCall
	// Expr is a single positioned scalar result claimed in a committed typed
	// body. Results retains its complete legacy word; Call/FuncLit are separate
	// alternatives and do not also populate Expr.
	Expr BashPPExpr

	// FuncLit is set when the single returned value is a function literal,
	// `return func(n int) int { … }`. That is how a closure ESCAPES the
	// function that built it — the factory idiom — and it needs its own field
	// because a literal has no word spelling for Results to hold.
	FuncLit *BashPPFuncLit
}

func (r *BashPPReturn) Pos() Pos { return r.Kw.Pos() }
func (r *BashPPReturn) End() Pos {
	if len(r.ResultExprs) > 0 {
		return r.ResultExprs[len(r.ResultExprs)-1].End()
	}
	if r.Expr != nil {
		return r.Expr.End()
	}
	if r.Call != nil {
		return r.Call.End()
	}
	if len(r.Results) > 0 {
		return r.Results[len(r.Results)-1].End()
	}
	if r.FuncLit != nil {
		return r.FuncLit.End()
	}
	return r.Kw.End()
}

// BashPPDefer is a Go-form deferred call: `defer f(x)`.
//
// `defer` is an ordinary command word in bash, so `defer cleanup` runs a
// command today and must keep doing so (Class E). Only the Go-call form
// `defer f(x)` — already a bash syntax error, hence Class R — is claimed, which
// is what lets an existing script that shells out to a program named `defer`
// keep working untouched.
type BashPPDefer struct {
	Kw   *Lit        // the literal "defer"
	Call *BashPPCall // the deferred call
}

// BashPPGo is a Go-style asynchronous call. It is syntax only; execution is
// intentionally owned by the interpreter concurrency lane.
type BashPPGo struct {
	Kw   *Lit
	Call *BashPPCall
}

func (g *BashPPGo) Pos() Pos { return g.Kw.Pos() }
func (g *BashPPGo) End() Pos {
	if g.Call != nil {
		return g.Call.End()
	}
	return g.Kw.End()
}

// BashPPChanType is a positioned channel type. Direction is "" for a
// bidirectional channel, "send" for chan<- T, and "recv" for <-chan T.
// Element is authoritative when present. Elem preserves the make(chan T)
// legacy element spelling; Walk visits only the authoritative representation.
type BashPPChanType struct {
	Chan      Pos
	Arrow     Pos
	Direction string
	// LocalDomain is set by Go-source package planning when every channel
	// type connected by assignment, payload or select is interpreter-owned
	// and none crosses a native package boundary. Inferred types retain it.
	LocalDomain bool
	Element     BashPPTypeExpr
	Elem        *Lit
}

func (t *BashPPChanType) Pos() Pos {
	if t.Direction == "recv" {
		return t.Arrow
	}
	return t.Chan
}
func (t *BashPPChanType) End() Pos {
	if t.Element != nil {
		return t.Element.End()
	}
	if t.Elem != nil {
		return t.Elem.End()
	}
	return posAddCol(t.Chan, 4)
}

// BashPPMakeChan records make(chan T, n), without evaluating its capacity.
//
// ChanType is spelled that way rather than the obvious `Type` for the reason
// [BashPPDecl.DeclType] records: sh/syntax/typedjson reserves a `Type` key on
// every tagged node for the node's own type name and builds its encoding
// struct by reflection, so a node field called Type panics reflect.StructOf.
// The collision is invisible until a tree containing one is serialized, which
// is exactly how it was found here.
type BashPPMakeChan struct {
	Make           *Lit
	ChanType       *BashPPChanType
	Capacity       *Word
	CapacityExpr   BashPPExpr // authoritative GoSource capacity
	Lparen, Rparen Pos
}

func (m *BashPPMakeChan) Pos() Pos { return m.Make.Pos() }
func (m *BashPPMakeChan) End() Pos { return posAddCol(m.Rparen, 1) }

// BashPPSend, BashPPReceive and BashPPClose are channel operations admitted
// only while parsing an already committed Go region.
type BashPPSend struct {
	ChanExpr  BashPPExpr // authoritative GoSource channel operand
	Chan      *Word
	Arrow     Pos
	Value     *Word
	ValueExpr BashPPExpr
}

func (s *BashPPSend) Pos() Pos {
	if s.ChanExpr != nil {
		return s.ChanExpr.Pos()
	}
	return s.Chan.Pos()
}
func (s *BashPPSend) End() Pos {
	if s.ValueExpr != nil {
		return s.ValueExpr.End()
	}
	return s.Value.End()
}

type BashPPReceive struct {
	Arrow    Pos
	ChanExpr BashPPExpr // authoritative GoSource channel operand
	Chan     *Word
}

func (r *BashPPReceive) Pos() Pos { return r.Arrow }
func (r *BashPPReceive) End() Pos {
	if r.ChanExpr != nil {
		return r.ChanExpr.End()
	}
	return r.Chan.End()
}

type BashPPClose struct {
	Kw             *Lit
	Chan           *Word
	Lparen, Rparen Pos
}

func (c *BashPPClose) Pos() Pos { return c.Kw.Pos() }
func (c *BashPPClose) End() Pos { return posAddCol(c.Rparen, 1) }

// BashPPSelect is a Go select statement and its source-order cases.
type BashPPSelect struct {
	Select Pos
	Lbrace Pos
	Cases  []*BashPPSelectCase
	Rbrace Pos
}

func (s *BashPPSelect) Pos() Pos { return s.Select }
func (s *BashPPSelect) End() Pos { return posAddCol(s.Rbrace, 1) }

type BashPPSelectCase struct {
	Case     Pos
	Default  bool
	Comm     Command
	Colon    Pos
	Comments []Comment
	Stmts    []*Stmt
	Last     []Comment
}

func (c *BashPPSelectCase) Pos() Pos { return c.Case }
func (c *BashPPSelectCase) End() Pos {
	if len(c.Stmts) > 0 {
		return c.Stmts[len(c.Stmts)-1].End()
	}
	return posAddCol(c.Colon, 1)
}

// BashPPRange is range-over-channel, parsed only in a committed Go region.
type BashPPRange struct {
	For    Pos
	Names  []*Lit
	Define Pos
	Range  Pos
	Chan   *Word
	Expr   BashPPExpr
	// Call is the one chained method form a range admits: `x.Lines()`, the
	// line iteration of a completed `run(...)` result or a live `start(...)`
	// process. Chan still carries the spelled text so printing and walking
	// need no new case; Expr is nil.
	Call *BashPPCall
	Body *Block
}

func (r *BashPPRange) Pos() Pos { return r.For }
func (r *BashPPRange) End() Pos {
	if r.Body != nil {
		return r.Body.End()
	}
	return r.Chan.End()
}

func (d *BashPPDefer) Pos() Pos { return d.Kw.Pos() }
func (d *BashPPDefer) End() Pos {
	if d.Call != nil {
		return d.Call.End()
	}
	return d.Kw.End()
}

// The Command marker methods. Declaring them here rather than in nodes.go is
// what lets this whole file merge without touching a certification-owned file.
func (*BashPPDecl) commandNode()        {}
func (*BashPPConstGroup) commandNode()  {}
func (*BashPPShortDecl) commandNode()   {}
func (*BashPPAssign) commandNode()      {}
func (*BashPPCall) commandNode()        {}
func (*BashPPCommandCall) commandNode() {}
func (*BashPPIf) commandNode()          {}
func (*BashPPFor) commandNode()         {}
func (*BashPPForAssign) commandNode()   {}
func (*BashPPIncDec) commandNode()      {}
func (*BashPPUpdate) commandNode()      {}
func (*BashPPBranch) commandNode()      {}
func (*BashPPLabeled) commandNode()     {}
func (*BashPPGoto) commandNode()        {}
func (*BashPPSwitch) commandNode()      {}
func (*BashPPImport) commandNode()      {}
func (*BashPPFuncDecl) commandNode()    {}
func (*BashPPReturn) commandNode()      {}
func (*BashPPDefer) commandNode()       {}
func (*BashPPGo) commandNode()          {}
func (*BashPPSend) commandNode()        {}
func (*BashPPReceive) commandNode()     {}
func (*BashPPClose) commandNode()       {}
func (*BashPPSelect) commandNode()      {}
func (*BashPPRange) commandNode()       {}
func (*SourceBlock) commandNode()       {}
