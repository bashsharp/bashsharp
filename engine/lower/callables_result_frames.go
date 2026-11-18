package lower

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func resultFieldNames(fields []*syntax.BashPPField) []string {
	var out []string
	for _, f := range fields {
		if len(f.Names) == 0 {
			out = append(out, "")
		} else {
			out = append(out, names(f.Names)...)
		}
	}
	return out
}
func (e *emitter) makeElement(n *syntax.BashPPMakeChan) (string, error) {
	if n.ChanType.Element != nil {
		return e.typeExpr(n.ChanType.Element)
	}
	return e.typeSpelling(n.ChanType.Elem, n.ChanType.Elem.Value)
}
func (e *emitter) recordedReturn(n *syntax.BashPPReturn) (string, error) {
	var expressions, payloads []string
	tuple := false
	forward := ""
	switch {
	case n.FuncLit != nil:
		value, err := e.literal(n.FuncLit)
		if err != nil {
			return "", err
		}
		expressions = append(expressions, value)
	case n.Call != nil:
		if len(e.callResultTypes(n.Call)) > 0 {
			forward = e.prefix + "forwardedResults"
		}
		savedFrame := e.resultCallFrame
		e.resultCallFrame = forward
		value, err := e.call(n.Call)
		e.resultCallFrame = savedFrame
		if err != nil {
			return "", err
		}
		expressions = append(expressions, value)
		tuple = len(e.resultTypes) > 1
	case n.Expr != nil:
		value, err := e.expr(n.Expr)
		if err != nil {
			return "", err
		}
		expressions = append(expressions, value)
		payloads = append(payloads, e.projectionExpr(n.Expr).sourceType)
	case len(n.Results) > 0:
		for _, w := range n.Results {
			value, err := e.valueWord(w)
			if err != nil {
				return "", err
			}
			expressions = append(expressions, value)
			payloads = append(payloads, e.projectionWord(w).sourceType)
		}
	default:
		for _, name := range e.resultNames {
			if name == "" {
				return "return", nil
			}
			expressions = append(expressions, name)
			if p, ok := e.projections.projectionLookup(name); ok {
				payloads = append(payloads, p.sourceType)
			} else {
				payloads = append(payloads, "")
			}
		}
	}
	count := len(expressions)
	if tuple {
		count = len(e.resultTypes)
	}
	if count != len(e.resultTypes) {
		return "return " + strings.Join(expressions, ","), nil
	}
	descriptors := make([]richResultDescriptor, count)
	for i, typ := range e.resultTypes {
		descriptors[i] = richResultDescriptor{Index: i, Declared: typ}
		if i < len(payloads) {
			descriptors[i].Payload = payloads[i]
		}
	}
	plan := e.richResultPlanFor(descriptors)
	var out strings.Builder
	out.WriteString("{\n")
	if forward != "" {
		out.WriteString(e.invocationFrame(forward, count))
	}
	temps := make([]string, count)
	for i := range temps {
		temps[i] = fmt.Sprintf("%sreturned%d", e.prefix, i)
	}
	if tuple {
		fmt.Fprintf(&out, "%s := %s\n", strings.Join(temps, ","), expressions[0])
	} else {
		for i, value := range expressions {
			// Native nil and untyped constants need the declared contextual type.
			if plan.Classes[i] == richDirect && e.resultTypes[i] != "" {
				fmt.Fprintf(&out, "var %s %s = (%s)(%s)\n", temps[i], e.resultTypes[i], e.resultTypes[i], value)
			} else {
				fmt.Fprintf(&out, "%s := %s\n", temps[i], value)
			}
		}
	}
	frame := e.prefix + "results"
	if forward != "" {
		checks := make([]string, count)
		zeros := make([]string, count)
		for i, typ := range e.resultTypes {
			checks[i] = fmt.Sprintf("%s.Present(%d)", forward, i)
			zeros[i] = "*new(" + typ + ")"
		}
		fmt.Fprintf(&out, "if !(%s) {%s.ShortFailure(); return %s}\n", strings.Join(checks, " && "), e.program(), strings.Join(zeros, ","))
		fmt.Fprintf(&out, "%srt.MustResult(%srt.ForwardResults(%s,%s))\nreturn %s\n}", e.prefix, e.prefix, frame, forward, strings.Join(temps, ","))
		return out.String(), nil
	}
	fmt.Fprintf(&out, "if %s != nil {\n", frame)
	for i, temp := range temps {
		record, err := e.richResultRecord(frame, plan, i, temp)
		if err != nil {
			return "", err
		}
		out.WriteString(record + "\n")
	}
	out.WriteString("}\n")
	for i, class := range plan.Classes {
		if class == richCapability {
			temps[i] = "*new(" + e.resultTypes[i] + ")"
		}
	}
	out.WriteString("return " + strings.Join(temps, ",") + "\n}")
	return out.String(), nil
}

// This bounded source-type plan follows committed channel declarations and
// return operands. Native payload authority never comes from scalar text.
func (e *emitter) functionResultPlan(f *syntax.BashPPFuncDecl) richResultPlan {
	declared := e.returnTypes(f.Results)
	names := resultFieldNames(f.Results)
	ds := make([]richResultDescriptor, len(declared))
	for i, typ := range declared {
		ds[i] = richResultDescriptor{Index: i, Declared: typ, Name: names[i]}
	}
	locals := map[string]string{}
	if f.Body == nil {
		return e.richResultPlanFor(ds)
	}
	syntax.Walk(f.Body, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BashPPFuncLit, *syntax.BashPPFuncDecl:
			return false
		case *syntax.BashPPShortDecl:
			if len(n.Lhs) == 1 && n.MakeChan != nil {
				typ, err := e.makeElement(n.MakeChan)
				if err == nil {
					locals[n.Lhs[0].Value] = "chan " + typ
				}
			}
		case *syntax.BashPPReturn:
			for i, w := range n.Results {
				if i < len(ds) && locals[w.Lit()] != "" {
					ds[i].Payload = locals[w.Lit()]
				}
			}
		}
		return true
	})
	return e.richResultPlanFor(ds)
}
func (e *emitter) richCallAssignment(n *syntax.BashPPAssign) (string, bool, error) {
	if !e.execution || n.Call == nil || len(n.Call.Fun) != 1 || len(n.Names) == 0 {
		return "", false, nil
	}
	f := e.functionDecls[n.Call.Fun[0].Value]
	if f == nil {
		return "", false, nil
	}
	plan := e.functionResultPlan(f)
	if !plan.NeedsFrame() {
		return "", false, nil
	}
	frame := fmt.Sprintf("%sresultFrame%d", e.prefix, n.Pos().Offset())
	allocation, err := e.richResultFrame(frame, e.runtimeScope(), plan)
	if err != nil {
		return "", true, err
	}
	saved := e.resultCallFrame
	e.resultCallFrame = frame
	call, err := e.call(n.Call)
	e.resultCallFrame = saved
	if err != nil {
		return "", true, err
	}
	temps := make([]string, len(plan.Results))
	targets := make([]string, len(n.Names))
	for i := range temps {
		temps[i] = fmt.Sprintf("%sresultValue%d", e.prefix, i)
	}
	for i, name := range n.Names {
		targets[i] = "&" + name.Value
		if name.Value == "_" {
			targets[i] = "nil"
		}
		if i < len(plan.Classes) && plan.Classes[i] == richCapability {
			p, _ := e.projections.projectionLookup(name.Value)
			p.capabilityElement = strings.TrimPrefix(plan.Results[i].Payload, "chan ")
			e.projections.projectionBind(name.Value, p)
		}
	}
	transfer, err := e.richResultTransfer(frame, e.program()+".ResultSidecars", targets, e.checkedValueSite(n, ""))
	if err != nil {
		return "", true, err
	}
	return "{\n" + allocation + "\n" + strings.Join(temps, ",") + " := " + call + e.unused(temps) + "\n" + transfer + "\n}", true, nil
}
