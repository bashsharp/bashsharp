package lower

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
)

// returnStatement preserves typed results and the existing resultless status
// return contract. Parenthesize conversion types so pointer results remain
// conversions: (*Box)(p), not a dereference of Box(p).
func (e *emitter) returnStatement(n *syntax.BashPPReturn) (string, error) {
	if e.goSource && len(n.ResultExprs) > 0 {
		values := make([]string, len(n.ResultExprs))
		for i, x := range n.ResultExprs {
			var err error
			values[i], err = e.expr(x)
			if err != nil {
				return "", err
			}
		}
		return "return " + strings.Join(values, ", "), nil
	}
	if e.execution && len(e.resultTypes) > 0 {
		return e.recordedReturn(n)
	}
	if n.Expr != nil {
		value, err := e.expr(n.Expr)
		return "return " + value, err
	}
	if n.Call != nil {
		value, err := e.call(n.Call)
		return "return " + value, err
	}
	if len(e.resultTypes) == 0 && len(n.Results) > 0 {
		if e.goSource {
			return "", e.fail(n, CodeResult, "Go resultless function cannot return a shell status")
		}
		if len(n.Results) != 1 {
			return "", e.fail(n, CodeResult, "resultless function return requires one status")
		}
		x, err := e.valueWord(n.Results[0])
		if err != nil {
			return "", err
		}
		e.bridge = true
		if e.execution {
			return e.program() + ".SetStatus(int(" + x + "))\nreturn", nil
		}
		return e.prefix + "rt.Status = int(" + x + ")\nreturn", nil
	}
	if n.FuncLit != nil {
		x, err := e.literal(n.FuncLit)
		return "return " + x, err
	}
	var values []string
	for i, w := range n.Results {
		x, err := e.valueWord(w)
		if err != nil {
			return "", err
		}
		if i < len(e.resultTypes) && e.resultTypes[i] != "" {
			x = "(" + e.resultTypes[i] + ")(" + x + ")"
		}
		values = append(values, x)
	}
	return "return " + strings.Join(values, ", "), nil
}
