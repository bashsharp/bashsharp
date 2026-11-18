package lower

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) callResultTypes(c *syntax.BashPPCall) []string {
	return e.callResultTypesForArity(c, 0)
}

func (e *emitter) callResultTypesForArity(c *syntax.BashPPCall, arity int) []string {
	if c == nil {
		return nil
	}
	if len(c.Fun) > 0 {
		if foreign, ok := e.foreignFunctions[strings.Join(names(c.Fun), ".")]; ok {
			if foreign.export.Signature.Dynamic {
				return []string{"any", "error"}
			}
			result := make([]string, len(foreign.export.Signature.Results))
			for i, typ := range foreign.export.Signature.Results {
				result[i] = foreignGoType(typ)
			}
			// One binding more than the export's results is the explicit
			// error opt-in, a single error binding for a zero-result export.
			if arity == len(result)+1 {
				result = append(result, "error")
			}
			return result
		}
	}
	if kind := e.pythonCallKind(c); kind != "" {
		if arity == 2 && kind != "module" {
			return []string{"any", "error"}
		}
		return []string{"any"}
	}
	if len(c.Fun) != 1 {
		return nil
	}
	if f := e.functionDecls[c.Fun[0].Value]; f != nil {
		if len(f.TypeParams) > 0 {
			return nil
		}
		results := e.publicResults(f)
		result := e.returnTypes(results)
		i := 0
		for _, field := range results {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			if callableResultField(field) {
				typ, ok := e.callableResultABI(field)
				if !ok {
					return nil
				}
				for j := 0; j < count; j++ {
					result[i+j] = typ
				}
			}
			i += count
		}
		for _, typ := range result {
			if typ == "" {
				return nil
			}
		}
		return result
	}
	p, ok := e.projections.projectionLookup(c.Fun[0].Value)
	if !ok {
		return nil
	}
	expr, err := parser.ParseExpr(p.sourceType)
	if err != nil {
		return nil
	}
	f, ok := expr.(*ast.FuncType)
	if !ok || f.Results == nil {
		return nil
	}
	var result []string
	for _, field := range f.Results.List {
		var out bytes.Buffer
		_ = format.Node(&out, token.NewFileSet(), field.Type)
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			result = append(result, out.String())
		}
	}
	return result
}
func (e *emitter) invocationFrame(name string, arity int) string {
	p := e.program()
	rt := e.prefix + "rt."
	return name + " := " + rt + "MustValue(" + rt + "NewResultFrame(&" + rt + "ResultOwner{Channels:" + p + ".Channels,Session:" + p + ".Session}," + fmt.Sprint(arity) + "))\n"
}
func (e *emitter) shortResultCall(n *syntax.BashPPShortDecl) (string, bool, error) {
	if !e.execution || n.Call == nil {
		return "", false, nil
	}
	types := e.callResultTypesForArity(n.Call, len(n.Lhs))
	if len(types) == 0 || len(types) != len(n.Lhs) {
		return "", false, nil
	}
	// Existing local rebinding needs transactional storage, handled by the
	// assignment path rather than declaring an error beside an old binding.
	for _, name := range n.Lhs {
		if e.scopes[len(e.scopes)-1][name.Value] {
			return "", false, nil
		}
	}
	ns := names(n.Lhs)
	frame := fmt.Sprintf("%sinvocationResults%d", e.prefix, n.Pos().Offset())
	failure := fmt.Sprintf("%sresultFailure%d", e.prefix, n.Pos().Offset())
	mark := frame + "Mark"
	saved := e.resultCallFrame
	savedWant := e.resultCallWant
	e.resultCallFrame = frame
	e.resultCallWant = len(types)
	call, err := e.call(n.Call)
	e.resultCallFrame = saved
	e.resultCallWant = savedWant
	if err != nil {
		return "", true, err
	}
	temps := make([]string, len(types))
	zeros := make([]string, len(types))
	present := make([]string, len(types))
	for i, typ := range types {
		temps[i] = fmt.Sprintf("%svalue%d", frame, i)
		zeros[i] = "*new(" + typ + ")"
		present[i] = fmt.Sprintf("%s.Present(%d)", frame, i)
	}
	body := e.invocationFrame(frame, len(types)) + mark + " := " + e.program() + ".ShortFailureMark()\n" + strings.Join(temps, ",") + " := " + call + "\nif !(" + strings.Join(present, " && ") + ") { return " + strings.Join(zeros, ",") + "," + e.program() + ".MissingResults(" + mark + "," + frame + "," + fmt.Sprint(len(types)) + ") }\nreturn " + strings.Join(temps, ",") + ",nil\n"
	text := strings.Join(ns, ",") + ", " + failure + " := func()(" + strings.Join(types, ",") + ",error){" + strings.ReplaceAll(body, "\n", ";") + "}()\n"
	for i, name := range ns {
		p := e.callProjection(n.Call, i)
		p.sourceType = types[i]
		p.present = failure + " == nil"
		e.bind(name)
		e.projections.projectionBind(name, p)
	}
	if !e.inFunc {
		if e.globalChecked == nil {
			e.globalChecked = map[*syntax.BashPPShortDecl]string{}
		}
		e.globalChecked[n] = failure
	}
	return text + e.unused(append(ns, failure)), true, nil
}
