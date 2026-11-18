package interp

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/constant"
	"strconv"
	"strings"
)

// Go-source bridge calls transfer evaluated values. Program expressions remain
// on the interpreter path instead of being forwarded as native Go source.
func (r *Runner) bashPPGoSourceArguments(c *syntax.BashPPCall) ([]string, error) {
	if len(c.ArgExprs) != len(c.Args) {
		return nil, fmt.Errorf("gosource: missing positioned bridge arguments")
	}
	args := make([]string, len(c.ArgExprs))
	for i, e := range c.ArgExprs {
		value, err := r.bashPPEvalScalarExpr(e)
		if err != nil {
			return nil, fmt.Errorf("%s%w", r.bashErrPrefix(e.Pos()), err)
		}
		args[i] = value.value.ExactString()
		if value.value.Kind() == constant.Float {
			f, _ := constant.Float64Val(value.value)
			args[i] = strconv.FormatFloat(f, 'g', -1, 64)
			if !strings.ContainsAny(args[i], ".eE") {
				args[i] += ".0"
			}
		}
		if value.typ != "" {
			args[i] = value.typ + "(" + args[i] + ")"
		}
	}
	return args, nil
}

// goSourceError adds the first original source position without replacing error identity.
type goSourceError struct {
	prefix string
	err    error
}

func (e *goSourceError) Error() string { return e.prefix + e.err.Error() }
func (e *goSourceError) Unwrap() error { return e.err }
