package lower

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) methodReceiver(parts []*syntax.Lit) (string, string) {
	if len(parts) == 0 {
		return "", ""
	}
	name := parts[0].Value
	info, _ := e.projections.projectionLookup(name)
	if e.typeNames[name] {
		info = e.projectionType(name, nil)
	}
	expression := e.goName(name)
	for _, part := range parts[1:] {
		expression += "." + part.Value
		info = e.fieldProjection(strings.TrimPrefix(info.sourceType, "*"), part.Value, map[string]bool{})
	}
	return expression, info.sourceType
}
func (e *emitter) methodHandle(n *syntax.BashPPShortDecl) (string, error) {
	if len(n.MethodValue) < 2 {
		return "", e.fail(n, CodeExpr, "method value needs a receiver")
	}
	receiver, typ := e.methodReceiver(n.MethodValue[:len(n.MethodValue)-1])
	method := n.MethodValue[len(n.MethodValue)-1].Value
	if _, err := e.runtimeMethodHandleType(n, typ, method); err != nil {
		return "", err
	}
	return e.runtimeMethodHandle(n, receiver, typ, method)
}
func (e *emitter) methodCall(c *syntax.BashPPCall) (string, error) {
	receiver, typ := e.methodReceiver(c.Fun[:len(c.Fun)-1])
	method := c.Fun[len(c.Fun)-1]
	if e.typeNames[c.Fun[0].Value] || c.PointerMethodExpr {
		if len(c.Args) == 0 {
			return "", e.fail(c, CodeResult, "method expression requires its receiver argument")
		}
		var args []string
		for i := range c.Args {
			value, err := e.callArgument(c, i)
			if err != nil {
				return "", err
			}
			args = append(args, value)
		}
		if c.PointerMethodExpr {
			receiver = "(*" + receiver + ")"
		}
		arguments := append([]string{args[0], e.program(), e.callSite(c, method.Value)}, args[1:]...)
		if c.Ellipsis.IsValid() {
			arguments[len(arguments)-1] += "..."
		}
		typeargs, err := e.typeArgs(c.TypeArgs)
		if err != nil {
			return "", err
		}
		return receiver + "." + e.methodPrivateName(method.Value) + typeargs + "(" + strings.Join(arguments, ",") + ")", nil
	}
	copy := *c
	copy.Fun = []*syntax.Lit{{Value: receiver}, method}
	return e.runtimeMethodCall(&copy, typ)
}
