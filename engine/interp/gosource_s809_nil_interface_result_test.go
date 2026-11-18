//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Boxing an interface that holds nothing must yield the nil interface. A call,
// index or dereference whose result is a nil interface reaches the interface
// store through bashPPBoxConcreteValue, which named the STATIC interface type
// as the stored dynamic type -- so the cell was non-nil and `x.Field != nil`
// answered true for a field that Go leaves nil. The named-ident and selector
// forms already tested for this, which is why only the call form diverged.
//
// cmd/compile/internal/syntax.(*parser).paramList counts `par.Type != nil` over
// the fields it just built from p.typeOrNil(); with a nil interface reading as
// non-nil its named/typed tallies both gain one, it takes the right-to-left
// distribution branch it must not take, and it reports a syntax error on source
// that is valid Go -- parser.go:709:39 "missing parameter name" for the result
// list of `func extractName(x Expr, force bool) (*Name, Expr)`.

import "testing"

// TestS809NilInterfaceResultStorage stores a nil interface returned by a call
// into every composite cell that can hold one. Only the plain-variable forms
// (1 and 2) passed before the repair; 3 through 6 read back non-nil.
func TestS809NilInterfaceResultStorage(t *testing.T) {
	const source = `package main

import "fmt"

type Expr interface{ isExpr() }

type Name struct{ Value string }

func (n *Name) isExpr() {}

type Field struct {
	Name *Name
	Type Expr
}

func nilExpr() Expr { return nil }

func nilPtr() *Name { return nil }

func main() {
	v := nilExpr()
	fmt.Println("1 short var", v == nil)

	var w Expr
	w = nilExpr()
	fmt.Println("2 assign var", w == nil)

	p := new(Field)
	p.Type = nilExpr()
	fmt.Println("3 ptr field", p.Type == nil)

	var s Field
	s.Type = nilExpr()
	fmt.Println("4 value field", s.Type == nil)

	c := Field{Type: nilExpr()}
	fmt.Println("5 literal field", c.Type == nil)

	sl := make([]Expr, 1)
	sl[0] = nilExpr()
	fmt.Println("6 slice elem", sl[0] == nil)

	m := map[string]Expr{}
	m["k"] = nilExpr()
	got, ok := m["k"]
	fmt.Println("7 map value", got == nil, ok, len(m))

	arr := [2]Expr{}
	arr[1] = nilExpr()
	fmt.Println("8 array elem", arr[0] == nil, arr[1] == nil)

	// A typed nil pointer is NOT a nil interface: the repair must not widen
	// to every nil-valued source.
	p.Type = nilPtr()
	fmt.Println("9 typed nil ptr", p.Type == nil)

	// The literal and the zero value were already correct; keep them pinned.
	p.Type = nil
	var zero Field
	fmt.Println("10 literal nil", p.Type == nil, "zero", zero.Type == nil, zero.Name == nil)

	// A nil interface boxed on through a second interface stays nil, and an
	// interface holding a real value still reports its dynamic type.
	s.Type = nilExpr()
	var anyv any = s.Type
	s.Type = &Name{Value: "n"}
	fmt.Printf("11 rebox %v %T %v\n", anyv == nil, s.Type, s.Type == nil)
}
`
	differGoSource(t, source, nil, "")
}

// TestS809ParamListTypeDistribution is the shape of the diverging function
// itself: cmd/compile/internal/syntax.(*parser).paramList's named/typed tally
// and its right-to-left type sweep, over the two results of parser.go:709.
// Before the repair this printed "named 1 typed 2" and reported
// "39: syntax error: missing parameter name" on valid Go.
func TestS809ParamListTypeDistribution(t *testing.T) {
	const source = `package main

import "fmt"

type Expr interface{ aExpr() }

type Pos struct{ col uint }

func (p Pos) IsKnown() bool { return p.col > 0 }

type Name struct {
	pos   Pos
	Value string
}

func (n *Name) aExpr()     {}
func (n *Name) Pos() Pos   { return n.pos }
func NewName(p Pos, v string) *Name { return &Name{pos: p, Value: v} }

type Operation struct {
	pos Pos
	X   Expr
}

func (o *Operation) aExpr() {}

type BadExpr struct{ pos Pos }

func (b *BadExpr) aExpr() {}

func StartPos(x Expr) Pos {
	switch t := x.(type) {
	case *Name:
		return t.pos
	case *Operation:
		return t.pos
	case *BadExpr:
		return t.pos
	}
	return Pos{}
}

type Field struct {
	pos  Pos
	Name *Name
	Type Expr
}

type pstate struct{ errors []string }

func (p *pstate) syntaxErrorAt(pos Pos, msg string) {
	p.errors = append(p.errors, fmt.Sprintf("%d: syntax error: %s", pos.col, msg))
}

func (p *pstate) badExpr() *BadExpr { return new(BadExpr) }

// typeOrNil answers a nil interface at the end of the list, exactly as the
// pstate's own typeOrNil does when the next token closes the parameter list.
func (p *pstate) typeOrNil(kind string, col uint) Expr {
	switch kind {
	case "star":
		return &Operation{pos: Pos{col: col}, X: &Name{pos: Pos{col: col + 1}, Value: "Name"}}
	case "name":
		return &Name{pos: Pos{col: col}, Value: "Expr"}
	}
	return nil
}

// paramDeclOrNil is the decision the real one makes for each of the two
// results in ` + "`" + `func extractName(x Expr, force bool) (*Name, Expr)` + "`" + `: a leading
// "*" is a type with no name, a bare identifier is a name whose type is only
// resolved by the distribution pass below.
func (p *pstate) paramDeclOrNil(kind string, col uint) *Field {
	f := new(Field)
	f.pos = Pos{col: col}
	if kind == "name" {
		f.Name = &Name{pos: Pos{col: col}, Value: "Expr"}
		f.Type = p.typeOrNil("", col)
		if f.Name != nil || f.Type != nil {
			return f
		}
		return nil
	}
	f.Type = p.typeOrNil("star", col)
	return f
}

// paramList is the verbatim shape of the distribute-parameter-types block.
func (p *pstate) paramList(decls []string, cols []uint, requireNames bool) (list []*Field) {
	var named int
	var typed int
	end := Pos{col: 50}
	for i, kind := range decls {
		par := p.paramDeclOrNil(kind, cols[i])
		if par != nil {
			if par.Name != nil && par.Type != nil {
				named++
			}
			if par.Type != nil {
				typed++
			}
			list = append(list, par)
		}
	}
	if len(list) == 0 {
		return
	}
	fmt.Println("named", named, "typed", typed, "len", len(list))

	if named == 0 && !requireNames {
		for _, par := range list {
			if typ := par.Name; typ != nil {
				par.Type = typ
				par.Name = nil
			}
		}
	} else if named != len(list) {
		var errPos Pos
		var typ Expr
		for i := len(list) - 1; i >= 0; i-- {
			par := list[i]
			if par.Type != nil {
				typ = par.Type
				if par.Name == nil {
					errPos = StartPos(typ)
					par.Name = NewName(errPos, "_")
				}
			} else if typ != nil {
				par.Type = typ
			} else {
				errPos = par.Name.Pos()
				t := p.badExpr()
				t.pos = errPos
				par.Type = t
			}
		}
		if errPos.IsKnown() {
			var msg string
			if named == typed {
				errPos = end
				if requireNames {
					msg = "missing type constraint"
				} else {
					msg = "missing parameter type"
				}
			} else {
				if requireNames {
					msg = "missing type parameter name"
				} else {
					msg = "missing parameter name"
				}
			}
			p.syntaxErrorAt(errPos, msg)
		}
	}
	return
}

func main() {
	// The result list of pstate.go:709,
	//   func extractName(x Expr, force bool) (*Name, Expr) {
	// whose "*" is at column 39 and whose "Expr" is at column 46.
	p := new(pstate)
	list := p.paramList([]string{"star", "name"}, []uint{39, 46}, false)
	for _, f := range list {
		name := "<nil>"
		if f.Name != nil {
			name = f.Name.Value
		}
		fmt.Printf("field name=%s type=%T typeIsNil=%v\n", name, f.Type, f.Type == nil)
	}
	fmt.Println("errors", len(p.errors), p.errors)
}
`
	differGoSource(t, source, nil, "")
}
