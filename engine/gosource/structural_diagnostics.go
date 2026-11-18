package gosource

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"

	gcsyntax "github.com/bashsharp/bashsharp/engine/gosource/internal/gcsyntax"
)

// dropRecoveredCallUnused removes an unused diagnostic only when the exact
// declaration object is referenced by a non-call go/defer operand retained by
// gc's tree. go/parser discards that operand, so go/types cannot record the
// use. Independent declarations with the same spelling remain errors.
func dropRecoveredCallUnused(out ErrorList, fset *token.FileSet, info *types.Info, files []*gcsyntax.File) ErrorList {
	used := map[types.Object]bool{}
	position := func(pos gcsyntax.Pos) token.Pos {
		var found token.Pos
		base := pos.FileBase()
		if !pos.IsKnown() || base == nil || pos.Col() == 0 {
			return token.NoPos
		}
		fset.Iterate(func(file *token.File) bool {
			if file.Name() != base.Filename() || int(pos.Line()) > file.LineCount() {
				return true
			}
			found = file.LineStart(int(pos.Line())) + token.Pos(pos.Col()-1)
			return false
		})
		return found
	}
	lookup := func(name string, pos token.Pos) types.Object {
		var innermost *types.Scope
		for _, scope := range info.Scopes {
			if scope.Contains(pos) && (innermost == nil || scope.Pos() >= innermost.Pos()) {
				innermost = scope
			}
		}
		if innermost == nil {
			return nil
		}
		_, obj := innermost.LookupParent(name, pos)
		return obj
	}
	for _, file := range files {
		if file == nil {
			continue
		}
		gcsyntax.Inspect(file, func(node gcsyntax.Node) bool {
			stmt, ok := node.(*gcsyntax.CallStmt)
			if !ok || stmt == nil || stmt.Call == nil {
				return true
			}
			if _, ok := stmt.Call.(*gcsyntax.CallExpr); ok {
				return false
			}
			var visit func(gcsyntax.Node) bool
			visit = func(node gcsyntax.Node) bool {
				// Sel names a field or package member, never a lexical use.
				// Inspect only the receiver, including nested selectors.
				if selector, ok := node.(*gcsyntax.SelectorExpr); ok {
					gcsyntax.Inspect(selector.X, visit)
					return false
				}
				if name, ok := node.(*gcsyntax.Name); ok && name != nil {
					if obj := lookup(name.Value, position(name.Pos())); obj != nil {
						used[obj] = true
					}
				}
				return true
			}
			gcsyntax.Inspect(stmt.Call, visit)
			return false
		})
	}
	byPos := map[token.Pos]types.Object{}
	for id, obj := range info.Defs {
		if obj != nil {
			byPos[id.Pos()] = obj
		}
	}
	for _, obj := range info.Implicits {
		if obj != nil {
			byPos[obj.Pos()] = obj
		}
	}
	kept := out[:0]
	for _, diagnostic := range out {
		e, ok := diagnostic.(types.Error)
		if ok && (strings.Contains(e.Msg, "declared and not used") || strings.Contains(e.Msg, "imported and not used")) && used[byPos[e.Pos]] {
			continue
		}
		kept = append(kept, diagnostic)
	}
	return kept
}

// structuralCheckerDiagnostics recovers checker diagnostics which go/types
// cannot produce from go/parser's recovered tree. The gc parser retains the
// operand of a non-call go/defer statement in a CallStmt, while go/parser
// replaces the entire statement with BadStmt after reporting a parser error.
// types2 consequently diagnoses the retained operand and continues checking
// the rest of the function independently.
func structuralCheckerDiagnostics(files []*gcsyntax.File) ErrorList {
	var diagnostics ErrorList
	for _, file := range files {
		if file == nil {
			continue
		}
		gcsyntax.Inspect(file, func(node gcsyntax.Node) bool {
			stmt, ok := node.(*gcsyntax.CallStmt)
			if !ok || stmt == nil || stmt.Call == nil {
				return true
			}
			if _, ok := stmt.Call.(*gcsyntax.CallExpr); ok {
				return true
			}
			keyword := stmt.Tok.String()
			diagnostics = append(diagnostics, gcError{
				pos: gcsyntax.EndPos(stmt.Call),
				msg: fmt.Sprintf("expression in %s must be function call", keyword),
			})
			return true
		})
	}
	return diagnostics
}

// appendStructuralCheckerDiagnostics adds only diagnostics absent because
// go/parser discarded their statement. gcError is comparable, so the exact
// position and wording are used for deduplication; unrelated errors on the
// same line are deliberately preserved.
func appendStructuralCheckerDiagnostics(out ErrorList, files []*gcsyntax.File) ErrorList {
	for _, recovered := range structuralCheckerDiagnostics(files) {
		duplicate := false
		for _, existing := range out {
			if existing == recovered {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, recovered)
		}
	}
	return out
}
