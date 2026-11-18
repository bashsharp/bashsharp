package lower

import "github.com/bashsharp/bashsharp/engine/syntax"

// goSourceInstructionPos identifies the instruction whose runtime fault a Go
// stack frame reports. Bounds checks belong to the '[' token, while an
// interface method lookup belongs to the selected method identifier. Keeping
// this position on the generated statement lets the Go backend reproduce the
// compiler's PC position without rewriting tracebacks.
func goSourceInstructionPos(command syntax.Command) syntax.Pos {
	if command == nil {
		return syntax.Pos{}
	}
	// Same-line instructions already inherit the correct generated column
	// from the statement anchor. Moving that anchor to a selector or bracket
	// adds its source-column offset twice. Only a distinct fault line needs
	// a new directive; retain the original anchor for ordinary calls.
	faultLine := func(pos syntax.Pos) syntax.Pos {
		if pos.Line() == command.Pos().Line() {
			return command.Pos()
		}
		return pos
	}
	exprPos := func(expr syntax.BashPPExpr) syntax.Pos {
		switch x := expr.(type) {
		case *syntax.BashPPIndexExpr:
			return x.Lbrack
		case *syntax.BashPPSliceExpr:
			return x.Lbrack
		case *syntax.BashPPSelectorExpr:
			return x.Sel.Pos()
		}
		return syntax.Pos{}
	}
	switch x := command.(type) {
	case *syntax.BashPPCall:
		if pos := exprPos(x.CalleeExpr); pos.IsValid() {
			return faultLine(pos)
		}
		if len(x.Fun) > 1 {
			return faultLine(x.Fun[len(x.Fun)-1].Pos())
		}
	}
	// An assignment's or declaration's index, slice or selector operand is
	// emitted by expr, which leads its '[' or selector with the token's own
	// inline directive when it is on another line (faultTokenDirective);
	// the statement keeps its own line for its other operands.
	return command.Pos()
}
