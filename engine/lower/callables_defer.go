package lower

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// sharpDefer captures supplied arguments followed by defaults, then defers the
// actual function directly. An adapter closure would change recover semantics.
func (e *emitter) sharpDefer(c *syntax.BashPPCall, f *syntax.BashPPFuncDecl) (string, error) {
	plan, err := planSharpCall(c, f)
	if err != nil {
		return "", err
	}
	params, err := sharpParameters(f)
	if err != nil {
		return "", err
	}
	byEvaluation := make([]int, len(plan.Words))
	for parameter, evaluation := range plan.ParameterOrder {
		byEvaluation[evaluation] = parameter
	}
	var out strings.Builder
	out.WriteString("{\n")
	values := make([]string, len(plan.Words))
	for i, word := range plan.Words {
		value, err := e.plannedCallArgument(c, word)
		if err != nil {
			return "", err
		}
		field := params[byEvaluation[i]].field
		typ := "string"
		if field.FieldType != nil || field.FieldTypeExpr != nil {
			typ, err = e.fieldType(field)
			if err != nil {
				return "", err
			}
		}
		values[i] = fmt.Sprintf("%sdeferArg%d", e.prefix, i)
		fmt.Fprintf(&out, "var %s %s = %s\n", values[i], typ, value)
	}
	var args []string
	if e.execution {
		args = append(args, e.program(), e.callSite(c, f.Name.Value))
	}
	for _, i := range plan.ParameterOrder {
		args = append(args, values[i])
	}
	if c.Ellipsis.IsValid() && len(args) > 0 {
		args[len(args)-1] += "..."
	}
	typeArgs, err := e.typeArgs(c.TypeArgs)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&out, "defer %s%s(%s)\n}", e.goName(f.Name.Value), typeArgs, strings.Join(args, ","))
	return out.String(), nil
}
