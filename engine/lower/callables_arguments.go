package lower

import (
	"fmt"
	"go/types"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// callArgument reads the authoritative typed edge when the parser committed
// a scalar argument. Legacy words retain their ordinary source conversion.
func (e *emitter) callArgument(c *syntax.BashPPCall, index int) (result string, problem error) {
	defer func() {
		if problem == nil && e.execution && e.foreignParameterKind(c, index) == "callback" {
			foreign := e.foreignFunctions[strings.Join(names(c.Fun), ".")]
			result = fmt.Sprintf("%sresolveCallbacks%d(%s,%s)", e.prefix, foreign.plan, e.program(), result)
		}
	}()
	if c.ArgExprs != nil {
		if index >= len(c.ArgExprs) || c.ArgExprs[index] == nil {
			return "", e.fail(c, CodeExpr, "missing typed call argument")
		}
		text, err := e.expr(c.ArgExprs[index])
		if err == nil && e.pythonExpr(c.ArgExprs[index]) {
			if typ := e.callParameterType(c, index); typ != "" && typ != "any" && scalarType(typ) {
				text = "(" + text + ").(" + typ + ")"
			}
		}
		return text, err
	}
	if name := c.Args[index].Lit(); e.funcs[name] && (e.callParameterType(c, index) == "string" || e.foreignParameterKind(c, index) == "callback") {
		// A function named for a foreign callback parameter crosses by name,
		// resolved by the module's generated resolver, exactly as the
		// interpreter passes it; a closure variable crosses as its value.
		return strconv.Quote(name), nil
	}
	if e.mixedShell {
		if _, foreign := e.foreignFunctions[strings.Join(names(c.Fun), ".")]; foreign {
			if name := c.Args[index].Lit(); syntax.BashPPValidIdent(name) && !e.known(name) {
				// Shell assignments live in the session, not the Go declaration
				// table. Match the interpreter's bare-word lookup at invocation
				// time, preserving empty values and the unset literal fallback.
				return e.program() + ".ShellDefault(" + strconv.Quote(name) + "," + strconv.Quote(name) + ")", nil
			}
		}
	}
	text, err := e.argument(c.Args[index])
	if err == nil {
		name := c.Args[index].Lit()
		if e.pythonValues[name] || e.pythonValues[text] {
			if typ := e.callParameterType(c, index); typ != "" && typ != "any" && scalarType(typ) {
				text = "(" + text + ").(" + typ + ")"
			}
		}
	}
	return text, err
}
func (e *emitter) plannedCallArgument(c *syntax.BashPPCall, w *syntax.Word) (string, error) {
	for i, supplied := range c.Args {
		if supplied == w {
			return e.callArgument(c, i)
		}
	}
	return e.argument(w)
}

func (e *emitter) callParameterType(c *syntax.BashPPCall, index int) string {
	if len(c.Fun) == 2 && e.imports[c.Fun[0].Value] != "" {
		if pkg, err := e.moduleImporter.Import(e.imports[c.Fun[0].Value]); err == nil {
			if fn, ok := pkg.Scope().Lookup(c.Fun[1].Value).(*types.Func); ok {
				if signature, ok := fn.Type().(*types.Signature); ok && signature.Params().Len() > 0 {
					param := index
					if signature.Variadic() && param >= signature.Params().Len()-1 {
						param = signature.Params().Len() - 1
					}
					if param < signature.Params().Len() {
						typ := signature.Params().At(param).Type()
						if signature.Variadic() && param == signature.Params().Len()-1 {
							if slice, ok := typ.(*types.Slice); ok {
								typ = slice.Elem()
							}
						}
						return types.TypeString(typ, func(*types.Package) string { return "" })
					}
				}
			}
		}
	}
	var fields []*syntax.BashPPField
	if c.FuncLit != nil {
		fields = c.FuncLit.Params
	} else if len(c.Fun) == 1 {
		if f := e.functionDecls[c.Fun[0].Value]; f != nil {
			fields = f.Params
		}
	} else if len(c.Fun) > 1 && e.imports[c.Fun[0].Value] == "" {
		_, receiverType := e.methodReceiver(c.Fun[:len(c.Fun)-1])
		method := c.Fun[len(c.Fun)-1].Value
		if e.typeNames[c.Fun[0].Value] || c.PointerMethodExpr {
			index--
		}
		if index < 0 {
			return ""
		}
		if iface, ok := e.interfaceDecl(receiverType); ok {
			params, _, found, err := e.interfaceMethodTypes(iface, method, map[*syntax.BashPPInterfaceType]bool{})
			if err == nil && found && index < len(params) {
				return params[index]
			}
			return ""
		}
		if f := e.methodDeclaration(receiverType, method); f != nil {
			fields = f.Params
		}
	}
	for _, field := range fields {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		if index < count || field.Ellipsis.IsValid() {
			if typ, err := e.fieldType(field); err == nil {
				return typ
			}
			return ""
		}
		index -= count
	}
	return ""
}

// foreignParameterKind is the boundary kind ("callback", "handle", …) the
// foreign export called by c declares for argument index, or "".
func (e *emitter) foreignParameterKind(c *syntax.BashPPCall, index int) string {
	if len(c.Fun) == 0 {
		return ""
	}
	names := make([]string, len(c.Fun))
	for i, part := range c.Fun {
		names[i] = part.Value
	}
	foreign, ok := e.foreignFunctions[strings.Join(names, ".")]
	if !ok || index >= len(foreign.export.Signature.Params) {
		return ""
	}
	return foreign.export.Signature.Params[index]
}
