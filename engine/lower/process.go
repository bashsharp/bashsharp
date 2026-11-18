package lower

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"
)

func (e *emitter) liveProcessMethod(c *syntax.BashPPCall) bool {
	if c == nil || len(c.Fun) != 2 || len(c.Args) != 0 {
		return false
	}
	p, _ := e.projections.projectionLookup(c.Fun[0].Value)
	return p.sourceType == "*"+e.prefix+"rt.LiveProcess"
}

func (e *emitter) processCall(c *syntax.BashPPCall) (string, bool, error) {
	if e.goSource || c == nil || c.FuncLit != nil {
		return "", false, nil
	}
	if len(c.Fun) == 1 && e.functionDecls[c.Fun[0].Value] == nil {
		name := c.Fun[0].Value
		if name != "run" && name != "start" {
			return "", false, nil
		}
		if len(c.Args) == 0 {
			return "", true, e.fail(c, CodeResult, name+": requires a command")
		}
		args := make([]string, len(c.Args))
		for i := range c.Args {
			value, err := e.callArgument(c, i)
			if err != nil {
				return "", true, err
			}
			args[i] = value
		}
		method := "RunProcess"
		if name == "start" {
			method = "StartProcess"
		}
		spread := ""
		if c.Ellipsis.IsValid() {
			spread = "..."
		}
		return e.program() + "." + method + "(" + strings.Join(args, ",") + spread + ")", true, nil
	}
	if e.liveProcessMethod(c) {
		switch c.Fun[1].Value {
		case "Wait", "Close", "Lines":
			return e.goName(c.Fun[0].Value) + "." + c.Fun[1].Value + "()", true, nil
		}
	}
	return "", false, nil
}

func (e *emitter) processRange(n *syntax.BashPPRange) (string, error) {
	if len(n.Names) > 1 {
		return "", e.fail(n, CodeType, "Lines() yields one value")
	}
	base := n.Call.Fun[0].Value
	p, _ := e.projections.projectionLookup(base)
	live := p.sourceType == "*"+e.prefix+"rt.LiveProcess"
	if !live && p.sourceType != e.prefix+"rt.ProcessResult" {
		return "", e.fail(n, CodeType, "Lines() requires a run result or live start handle")
	}
	e.push()
	defer e.pop()
	name := "_"
	if len(n.Names) == 1 {
		name = n.Names[0].Value
	}
	op := " = "
	if n.Define.IsValid() {
		op = " := "
		e.bind(name)
		e.projections.projectionBind(name, e.projectionType("string", nil))
	}
	prefix := name + op
	if !live {
		prefix = "_, " + prefix
	}
	if name == "_" {
		prefix = ""
	}
	target := e.pushBranchTarget(true)
	body, err := e.block(n.Body)
	e.popBranchTarget()
	if err != nil {
		return "", err
	}
	unused := ""
	if name != "_" {
		unused = e.unused([]string{name})
	}
	text := "for " + prefix + "range " + e.goName(base) + ".Lines() {\n" + unused + body + "}"
	if target.label != "" {
		text = target.label + ":\n" + text
	}
	return text, nil
}
