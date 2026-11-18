package lower

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) carrierShellAssignment(stmt *syntax.Stmt) (string, bool, error) {
	if !e.execution || stmt == nil {
		return "", false, nil
	}
	c, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(c.Args) != 0 || len(c.Assigns) != 1 || len(stmt.Redirs) > 0 {
		return "", false, nil
	}
	a := c.Assigns[0]
	if a.Name == nil || a.Index != nil || a.Array != nil || a.Append || a.Value == nil {
		return "", false, nil
	}
	source := a.Value.Lit()
	target := a.Name.Value
	from, found := e.projections.projectionLookup(source)
	to, known := e.projections.projectionLookup(target)
	if !found || !known || !richChannelType(from.sourceType) || richChannelType(to.sourceType) || to.sourceType == "" {
		return "", false, nil
	}
	elem := strings.TrimSpace(strings.TrimPrefix(from.sourceType, "chan "))
	to.capabilityElement = elem
	e.projections.projectionBind(target, to)
	return e.mark(c) + e.prefix + "rt.MustChannelOperation(" + e.prefix + "rt.CarryChannel(" + e.program() + ",&" + target + "," + source + "))\n", true, nil
}

func (e *emitter) carrierArgumentCall(c *syntax.BashPPCall, f *syntax.BashPPFuncDecl, invocation string) (string, bool, error) {
	if !e.execution || len(f.TypeParams) > 0 || c.Ellipsis.IsValid() {
		return "", false, nil
	}
	plan, err := planSharpCall(c, f)
	if err != nil {
		return "", false, nil
	}
	params, err := sharpParameters(f)
	if err != nil {
		return "", true, err
	}
	if len(params) != len(plan.Words) {
		return "", false, nil
	}
	expected := make([]string, len(params))
	byEvaluation := make([]int, len(plan.Words))
	carriers := make([]bool, len(plan.Words))
	retained := make([]bool, len(plan.Words))
	handled := false
	for parameter, evaluation := range plan.ParameterOrder {
		byEvaluation[evaluation] = parameter
		field := params[parameter].field
		typ := e.inferredParams[field]
		if field.FieldType != nil || field.FieldTypeExpr != nil {
			typ, err = e.fieldType(field)
			if err != nil {
				return "", true, err
			}
		}
		if typ == "" {
			typ = "string"
		}
		expected[parameter] = typ
		info := e.projectionWord(plan.Words[evaluation])
		carriers[evaluation] = richChannelType(info.sourceType) && !richChannelType(typ)
		retained[evaluation] = info.capabilityElement != "" && !richChannelType(typ)
		handled = handled || carriers[evaluation] || retained[evaluation]
	}
	if !handled {
		return "", false, nil
	}
	frame := fmt.Sprintf("%sargumentFrame%d", e.prefix, c.Pos().Offset())
	resultTypes := e.callResultTypes(c)
	signature := ""
	ret := ""
	if len(resultTypes) > 0 {
		signature = "(" + strings.Join(resultTypes, ",") + ")"
		ret = "return "
	}
	var out strings.Builder
	out.WriteString("func() " + signature + " {\n" + e.invocationFrame(frame, len(params)))
	args := make([]string, len(params))
	rt := e.prefix + "rt."
	for evaluation, word := range plan.Words {
		parameter := byEvaluation[evaluation]
		typ := expected[parameter]
		value, err := e.plannedCallArgument(c, word)
		if err != nil {
			return "", true, err
		}
		name := fmt.Sprintf("%svalue%d", frame, evaluation)
		args[parameter] = name
		if carriers[evaluation] {
			captured := name + "Channel"
			fmt.Fprintf(&out, "%s := %s\nvar %s %s\n", captured, value, name, typ)
			fmt.Fprintf(&out, "%sMustChannelOperation(%sSetResultCapability(%s,%d,%s,%s))\n", rt, rt, frame, parameter, name, captured)
		} else {
			fmt.Fprintf(&out, "var %s %s = %s\n", name, typ, value)
			if retained[evaluation] {
				fmt.Fprintf(&out, "%sMustChannelOperation(%sSetArgumentFromBinding(%s,%d,%s,%s.ResultSidecars,&%s))\n", rt, rt, frame, parameter, name, e.program(), word.Lit())
			} else {
				fmt.Fprintf(&out, "%sMustResult(%sSetResult(%s,%d,%s))\n", rt, rt, frame, parameter, name)
			}
		}
	}
	args = append([]string{invocation + ".WithArguments(" + frame + ")", e.callSite(c, f.Name.Value)}, args...)
	fmt.Fprintf(&out, "%s%s(%s)\n}()", ret, e.goName(f.Name.Value), strings.Join(args, ","))
	return out.String(), true, nil
}

func (e *emitter) bindResultArguments(params []*syntax.BashPPField) string {
	var targets []string
	for _, field := range params {
		for _, name := range field.Names {
			targets = append(targets, "&"+name.Value)
		}
	}
	if len(targets) == 0 {
		return ""
	}
	p := e.program()
	rt := e.prefix + "rt."
	frame := e.prefix + "arguments"
	return "if " + frame + " := " + p + ".Arguments; " + frame + " != nil {" + p + " = " + p + ".WithArguments(nil)\n" + rt + "MustResult(" + rt + "TransferResults(" + frame + "," + p + ".ResultSidecars,[]any{" + strings.Join(targets, ",") + "}," + rt + "ValueSite{Name:" + strconv.Quote("arguments") + "}))}\n"
}
