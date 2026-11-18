package lower

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) findCheckedValues(file *syntax.File) {
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BashPPDerefExpr, *syntax.BashPPSelectorExpr:
			// Go source dereferences natively (checkedDeref); only the
			// runtime path checks the pointer and needs the guard.
			e.guarded = e.guarded || !e.goSource
		case *syntax.BashPPTypeAssertExpr:
			// Likewise Go source asserts natively (valueAssertion).
			e.guarded = e.guarded || (n.TypeToken == nil && !e.goSource)
		}
		return true
	})
	if e.guarded {
		e.bridge = true
		e.output = true
	}
}

func (e *emitter) userRecover() string {
	if e.guarded || e.execution {
		return e.prefix + "rt.PreserveAbort(recover())"
	}
	return "recover()"
}

func (e *emitter) guardBoundary() string {
	return strings.ReplaceAll(`defer func(){if v:=recover();v!=nil{if failure:=PREFIXrt.GuardError(v);failure!=nil{PREFIXfmt.Fprintln(PREFIXos.Stderr,failure);PREFIXos.Exit(failure.ExitStatus())};panic(v)}}()
`, "PREFIX", e.prefix)
}

func (e *emitter) valueAssertion(n *syntax.BashPPTypeAssertExpr, commaOK bool) (string, error) {
	// Go source keeps Go's own assertion: `x.(T)` panics or, with two
	// targets, yields the comma-ok pair. The runtime's Assert/AssertOK and
	// their site metadata are the Bash++ path's diagnostic contract.
	if n.TypeToken != nil || e.goSource {
		return e.typeAssertExpr(n)
	}
	value, err := e.expr(n.X)
	if err != nil {
		return "", err
	}
	info := e.projectionExpr(n.X)
	source := info.sourceType
	if source == "" {
		source = "any"
	}
	text, err := e.checkedAssertion(n, value, source, false, commaOK)
	if err != nil {
		return "", err
	}
	target, err := e.typeExpr(n.Assert)
	if err != nil {
		return "", err
	}
	return strings.Replace(text, "Impossible:false", "Impossible:!"+e.prefix+"rt.AssertionPossible["+source+","+target+"]()", 1), nil
}
