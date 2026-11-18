package lower

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
)

func (e *emitter) declarationType(n *syntax.BashPPDecl) (string, error) {
	if len(n.EnumMembers) == 0 {
		return e.typeDecl(n)
	}
	e.enumMembers[n.Name.Value] = n.EnumMembers
	e.bind(n.Name.Value)
	for _, m := range n.EnumMembers {
		e.bind(m.Value)
		e.visibleGlobals[m.Value] = true
		e.globals[m.Value] = true
	}
	return e.enumDecl(n)
}

// The outer call evaluates and captures arguments at the call/defer boundary.
// The returned ordinary native closure performs the actual invocation later.
func (e *emitter) sharpCall(c *syntax.BashPPCall, f *syntax.BashPPFuncDecl) (string, error) {
	plan, err := planSharpCall(c, f)
	if err != nil {
		return "", err
	}
	params, err := sharpParameters(f)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	var resultTypes []string
	for _, field := range f.Results {
		typ, err := e.fieldType(field)
		if err != nil {
			return "", err
		}
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			resultTypes = append(resultTypes, typ)
		}
	}
	result := ""
	if len(resultTypes) > 0 {
		result = " (" + strings.Join(resultTypes, ",") + ")"
	}
	closure := "func()" + result
	fmt.Fprintf(&out, "func() %s {\n", closure)
	byEvaluation := make([]int, len(plan.Words))
	for parameter, evaluation := range plan.ParameterOrder {
		byEvaluation[evaluation] = parameter
	}
	var values []string
	for i, w := range plan.Words {
		value, err := e.plannedCallArgument(c, w)
		if err != nil {
			return "", err
		}
		field := params[byEvaluation[i]].field
		typ := e.inferredParams[field]
		if typ == "" {
			typ = "string"
		}
		if field.FieldType != nil || field.FieldTypeExpr != nil {
			typ, err = e.fieldType(field)
			if err != nil {
				return "", err
			}
		}
		name := fmt.Sprintf("%sarg%d", e.prefix, i)
		fmt.Fprintf(&out, "var %s %s = %s\n", name, typ, value)
		values = append(values, name)
	}
	var arguments []string
	for _, i := range plan.ParameterOrder {
		arguments = append(arguments, values[i])
	}
	if e.execution {
		caller := e.prefix + "capturedCaller"
		fmt.Fprintf(&out, "%s := %s\n", caller, e.program())
		arguments = append([]string{caller, e.callSite(c, f.Name.Value)}, arguments...)
	}
	ret := ""
	if len(resultTypes) > 0 {
		ret = "return "
	}
	fmt.Fprintf(&out, "return %s { %s%s(%s) }\n}()()", closure, ret, e.goName(f.Name.Value), strings.Join(arguments, ","))
	return out.String(), nil
}

// Static Bash# validation guarantees every member is represented. Go requires
// an explicit terminating default because named integer types are otherwise
// open. Reaching it indicates an invalid value at an unchecked native boundary.
func (e *emitter) completeEnumSwitch(n *syntax.BashPPSwitch) bool {
	seen := map[string]bool{}
	for _, arm := range n.Arms {
		if len(arm.Exprs) == 0 {
			return false
		}
		for _, expr := range arm.Exprs {
			id, ok := expr.(*syntax.BashPPIdent)
			if !ok {
				return false
			}
			seen[id.Name.Value] = true
		}
	}
	for _, members := range e.enumMembers {
		if len(seen) != len(members) {
			continue
		}
		match := true
		for _, member := range members {
			match = match && seen[member.Value]
		}
		if match {
			return true
		}
	}
	return false
}
