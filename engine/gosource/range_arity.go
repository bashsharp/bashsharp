package gosource

import (
	"go/ast"
	"go/token"
	"go/types"

	gcsyntax "github.com/bashsharp/bashsharp/engine/gosource/internal/gcsyntax"
)

// A range clause with more than two iteration variables is valid syntax for
// gc: its parser keeps the whole list, and types2's rangeStmt diagnoses it
// (cmd/compile/internal/types2/stmt.go):
//
//	range over s (variable of type chan int) permits only one iteration variable
//	range clause permits at most two iteration variables
//
// go/parser instead reports "expected at most 2 expressions" and replaces the
// whole for statement with a BadStmt, so go/types never sees the range
// expression or the body. rangeArityImage gives go/parser the clause as the
// checker sees it — the first two variables, the extra ones blanked in place
// (same length, every position unchanged) — and records each
// clause so rangeArityDiagnostics can report what types2 reports for the
// extra variables once the range expression is checked.

// rangeArity is one gc range clause with extra iteration variables.
type rangeArity struct {
	file  string
	rng   int // offset of the range keyword
	extra gcsyntax.Pos
}

// rangeArityImage returns src with the extra iteration variables of every gc
// range clause blanked, and those clauses. It changes nothing (and records
// nothing) for a clause whose separators are not plain whitespace between
// the variables or whose extra variables span a line break, so go/parser's
// own diagnostic is kept there.
func rangeArityImage(name string, src []byte, file *gcsyntax.File) ([]byte, []rangeArity) {
	if file == nil {
		return src, nil
	}
	lines := []int{0}
	for i, b := range src {
		if b == '\n' {
			lines = append(lines, i+1)
		}
	}
	offset := func(pos gcsyntax.Pos) int {
		if !pos.IsKnown() || pos.Col() == 0 || int(pos.Line()) > len(lines) {
			return -1
		}
		off := lines[pos.Line()-1] + int(pos.Col()) - 1
		if off > len(src) {
			return -1
		}
		return off
	}
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
	var image []byte
	var clauses []rangeArity
	gcsyntax.Inspect(file, func(node gcsyntax.Node) bool {
		clause, ok := node.(*gcsyntax.RangeClause)
		if !ok || clause == nil {
			return true
		}
		list, ok := clause.Lhs.(*gcsyntax.ListExpr)
		if !ok || len(list.ElemList) <= 2 {
			return true
		}
		// The comma after the second variable starts the blanked span.
		third := offset(gcsyntax.StartPos(list.ElemList[2]))
		comma := third - 1
		for comma >= 0 && space(src[comma]) {
			comma--
		}
		// The assignment operator before the range keyword ends it.
		rng := offset(clause.Pos())
		end := rng - 1
		for end >= 0 && space(src[end]) {
			end--
		}
		if end >= 1 && src[end] == '=' && src[end-1] == ':' {
			end--
		} else if end < 0 || src[end] != '=' {
			return true
		}
		for end > 0 && space(src[end-1]) {
			end--
		}
		if third < 0 || rng < 0 || comma < 0 || src[comma] != ',' || end <= comma {
			return true
		}
		// A line break inside the span would end the statement early once
		// the variables around it are blank (automatic semicolon), and it
		// cannot be removed without moving every later position.
		for i := comma; i < end; i++ {
			if src[i] == '\n' || src[i] == '\r' {
				return true
			}
		}
		if image == nil {
			image = append([]byte(nil), src...)
		}
		for i := comma; i < end; i++ {
			image[i] = ' '
		}
		clauses = append(clauses, rangeArity{file: name, rng: rng, extra: gcsyntax.StartPos(list.ElemList[2])})
		return true
	})
	if image == nil {
		return src, nil
	}
	return image, clauses
}

// rangeArityDiagnostics reports types2's diagnostic for the extra iteration
// variables of each recorded clause. types2 reports only the first applicable
// one of: the range expression is invalid (already reported), it cannot be
// ranged over, it yields no value while a second variable is given (both
// reported by go/types for the two-variable clause it checked), and last, the
// extra variables themselves — reported here.
func rangeArityDiagnostics(fset *token.FileSet, files []*ast.File, info *types.Info, clauses []rangeArity) ErrorList {
	if len(clauses) == 0 {
		return nil
	}
	want := map[string]map[int]gcsyntax.Pos{}
	for _, c := range clauses {
		if want[c.file] == nil {
			want[c.file] = map[int]gcsyntax.Pos{}
		}
		want[c.file][c.rng] = c.extra
	}
	var out ErrorList
	for _, f := range files {
		tf := fset.File(f.FileStart)
		if tf == nil || want[tf.Name()] == nil {
			continue
		}
		byOffset := want[tf.Name()]
		ast.Inspect(f, func(node ast.Node) bool {
			rs, ok := node.(*ast.RangeStmt)
			if !ok || !rs.Range.IsValid() {
				return true
			}
			extra, ok := byOffset[tf.Offset(rs.Range)]
			if !ok || rs.Value == nil {
				return true
			}
			tv, ok := info.Types[rs.X]
			if !ok || tv.Type == nil || tv.Type == types.Typ[types.Invalid] {
				return true
			}
			if rangeYieldsValue(tv.Type) {
				out = append(out, gcError{pos: extra, msg: "range clause permits at most two iteration variables"})
			}
			return true
		})
	}
	return out
}

// rangeYieldsValue reports whether types2's rangeKeyVal accepts typ with a
// non-nil value type: a string, an array or pointer to one, a slice, a map,
// or a range-over-func iterator whose yield takes two values. A type
// parameter qualifies when its type set has one common underlying type
// (commonUnder) that does.
func rangeYieldsValue(typ types.Type) bool {
	if tp, ok := types.Unalias(typ).(*types.TypeParam); ok {
		iface, _ := tp.Constraint().Underlying().(*types.Interface)
		if iface == nil {
			return false
		}
		var common types.Type
		for i := 0; i < iface.NumEmbeddeds(); i++ {
			terms := []types.Type{iface.EmbeddedType(i)}
			if u, ok := iface.EmbeddedType(i).(*types.Union); ok {
				terms = terms[:0]
				for j := 0; j < u.Len(); j++ {
					terms = append(terms, u.Term(j).Type())
				}
			}
			for _, term := range terms {
				under := term.Underlying()
				if _, ok := under.(*types.Interface); ok {
					return false
				}
				if common == nil {
					common = under
				} else if !types.Identical(common, under) {
					return false
				}
			}
		}
		return common != nil && rangeYieldsValue(common)
	}
	switch u := typ.Underlying().(type) {
	case *types.Basic:
		return u.Info()&types.IsString != 0
	case *types.Array, *types.Slice, *types.Map:
		return true
	case *types.Pointer:
		_, ok := u.Elem().Underlying().(*types.Array)
		return ok
	case *types.Signature:
		if u.Params().Len() != 1 || u.Results().Len() != 0 {
			return false
		}
		yield, ok := u.Params().At(0).Type().Underlying().(*types.Signature)
		if !ok || yield.Params().Len() != 2 || yield.Results().Len() != 1 {
			return false
		}
		result, ok := yield.Results().At(0).Type().Underlying().(*types.Basic)
		return ok && result.Kind() == types.Bool
	}
	return false
}
