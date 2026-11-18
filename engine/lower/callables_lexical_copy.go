package lower

import "github.com/bashsharp/bashsharp/engine/syntax"

func (e *emitter) lexicalCopyShellAssignment(s *syntax.Stmt) (string, bool, error) {
	if !e.execution || s == nil || s.Negated || s.Background || len(s.Redirs) > 0 {
		return "", false, nil
	}
	c, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(c.Args) != 0 || len(c.Assigns) != 1 {
		return "", false, nil
	}
	a := c.Assigns[0]
	if a.Name == nil || a.Value == nil || a.Append || a.Index != nil || a.Array != nil {
		return "", false, nil
	}
	source, target := a.Value.Lit(), a.Name.Value
	from, ok := e.projections.projectionLookup(source)
	if !ok {
		return "", false, nil
	}
	to, ok := e.projections.projectionLookup(target)
	if !ok {
		return "", false, nil
	}
	if from.constant || to.constant || from.sourceType == "" || from.sourceType != to.sourceType || !scalarType(from.sourceType) {
		return "", false, nil
	}
	return e.mark(c) + e.prefix + "rt.MustReadonly(" + e.prefix + "rt.CopyLexicalScalar(" + e.program() + ",&" + target + ",&" + source + "))\n", true, nil
}
