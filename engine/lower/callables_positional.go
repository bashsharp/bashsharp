package lower

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strconv"
	"strings"
)

func positionalAll(w *syntax.Word) bool {
	if w == nil || len(w.Parts) != 1 {
		return false
	}
	quote, ok := w.Parts[0].(*syntax.DblQuoted)
	if !ok || len(quote.Parts) != 1 {
		return false
	}
	p, ok := quote.Parts[0].(*syntax.ParamExp)
	return ok && p.Param != nil && p.Param.Value == "@" && p.Exp == nil && p.Index == nil && !p.Length && !p.Excl
}
func (e *emitter) positionalParameter(p *syntax.ParamExp) (string, bool, error) {
	if p.Param == nil || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Excl || p.Length || p.Width {
		return "", false, nil
	}
	name := p.Param.Value
	index, err := strconv.Atoi(name)
	if err != nil && name != "@" && name != "*" && name != "#" {
		return "", false, nil
	}
	if index == 0 && err == nil {
		return "", false, nil
	}
	params := e.program() + ".Session.Params()"
	if p.Exp != nil {
		if err != nil || p.Exp.Op != syntax.DefaultUnset {
			return "", false, nil
		}
		fallback, problem := e.stringParts(p.Exp.Word.Parts)
		if problem != nil {
			return "", true, problem
		}
		return e.prefix + "rt.PositionalDefault(" + params + "," + strconv.Itoa(index) + "," + fallback + ")", true, nil
	}
	switch name {
	case "#":
		return "len(" + params + ")", true, nil
	case "@", "*":
		return e.prefix + "rt.JoinPositionals(" + params + ")", true, nil
	}
	return e.prefix + "rt.Positional(" + params + "," + strconv.Itoa(index) + ")", true, nil
}
func (e *emitter) positionalArguments(words []*syntax.Word) (string, error) {
	expression := "[]any{}"
	for _, word := range words {
		if positionalAll(word) {
			expression = "append(" + expression + "," + e.prefix + "rt.PositionalArguments(" + e.program() + ".Session.Params())...)"
			continue
		}
		value, err := e.shellWord(word)
		if err != nil {
			return "", err
		}
		expression = "append(" + expression + "," + value + ")"
	}
	return expression, nil
}
func (e *emitter) positionalShell(c *syntax.CallExpr) (string, bool, error) {
	found := false
	for _, word := range c.Args[1:] {
		found = found || positionalAll(word)
	}
	if !found {
		return "", false, nil
	}
	name := c.Args[0].Lit()
	if name != "echo" && name != "printf" {
		return "", false, nil
	}
	words := c.Args[1:]
	leading := ""
	method := "Echo"
	if name == "printf" {
		if len(words) == 0 {
			return "", true, e.fail(c, CodeBridge, "printf requires a format")
		}
		if err := e.printfFormat(words[0]); err != nil {
			return "", true, err
		}
		format, err := e.shellWord(words[0])
		if err != nil {
			return "", true, err
		}
		leading = format + ","
		words = words[1:]
		method = "Printf"
	}
	args, err := e.positionalArguments(words)
	if err != nil {
		return "", true, err
	}
	failure := e.prefix + "positionalError"
	return "if " + failure + " := " + e.program() + "." + method + "(" + leading + args + "...); " + failure + " != nil && " + e.program() + ".Status()!=0 {" + e.program() + ".Fail(" + failure + ")}", true, nil
}

func hasPositionalParameter(node syntax.Node) bool {
	found := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if p, ok := n.(*syntax.ParamExp); ok && p.Param != nil {
			name := p.Param.Value
			_, err := strconv.Atoi(name)
			found = found || err == nil || strings.Contains("@*#", name)
		}
		return !found
	})
	return found
}
