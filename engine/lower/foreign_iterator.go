package lower

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) pythonRangeValue(name string) bool {
	if !e.pythonValues[name] {
		return false
	}
	if p, ok := e.projections.projectionLookup(name); ok && p.sourceType != "" && p.sourceType != "any" && p.sourceType != "interface{}" {
		return false
	}
	return true
}

func (e *emitter) pythonHandleIteratorRange(n *syntax.BashPPRange) (string, error) {
	e.handleIterator = true
	if len(n.Names) > 2 {
		return "", e.fail(n, CodeType, "Python handle range permits at most two variables")
	}
	if len(n.Names) > 0 && !n.Define.IsValid() {
		return "", e.fail(n, CodeType, "Python handle range requires a declaration")
	}
	value, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	e.push()
	defer e.pop()
	ns := names(n.Names)
	for _, name := range ns {
		e.bind(name)
	}
	setup := ""
	if len(ns) > 0 && ns[0] != "_" {
		setup += ns[0] + " := " + foreignResultExpr(e.prefix+"iteratorValue", "any") + "\n"
	}
	if len(ns) == 2 && ns[1] != "_" {
		setup += ns[1] + " := " + e.prefix + "iteratorErr\n"
	}
	setup += e.unused(ns) + "\n"
	target := e.pushBranchTarget(true)
	body, bodyErr := e.block(n.Body)
	e.popBranchTarget()
	ctx := e.prefix + "context.Background()"
	if e.execution {
		ctx = e.runtimeScope().Context
	}
	sequence := "IteratorSequence"
	header := e.prefix + "iteratorValue := "
	if len(ns) == 2 {
		sequence = "IteratorErrorSequence"
		header = e.prefix + "iteratorValue, " + e.prefix + "iteratorErr := "
	}
	iterator := fmt.Sprintf("%sshellexec.NewPythonHandleIterator(%spythonHandle(%s))", e.prefix, e.prefix, value)
	out := fmt.Sprintf("for %srange %srt.%s(%s,%s) {\n%s\n}", header, e.prefix, sequence, ctx, iterator, setup+body)
	if target.label != "" {
		out = target.label + ":\n" + out
	}
	return out, bodyErr
}

func (e *emitter) foreignIteratorRange(n *syntax.BashPPRange) (string, error) {
	if len(n.Names) > 1 {
		return "", e.fail(n, CodeType, "iterator range permits one variable")
	}
	value, err := e.valueWord(n.Chan)
	if err != nil {
		return "", err
	}
	element := e.foreignIteratorValues[n.Chan.Lit()]
	e.push()
	defer e.pop()
	ns := names(n.Names)
	prefix := ""
	setup := ""
	if len(ns) > 0 {
		for _, name := range ns {
			e.bind(name)
		}
		if ns[0] != "_" {
			prefix = e.prefix + "iteratorValue := "
			setup = ns[0] + " := " + foreignResultExpr(e.prefix+"iteratorValue", element) + "\n" + e.unused(ns) + "\n"
		}
	}
	target := e.pushBranchTarget(true)
	body, err := e.block(n.Body)
	e.popBranchTarget()
	ctx := e.prefix + "context.Background()"
	if e.execution {
		ctx = e.runtimeScope().Context
	}
	out := fmt.Sprintf("for %srange %srt.IteratorSequence(%s,%s) {\n%s\n}", prefix, e.prefix, ctx, value, setup+body)
	if target.label != "" {
		out = target.label + ":\n" + out
	}
	return out, err
}
