package lower

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) nativeSubshell(n *syntax.Subshell) (string, error) {
	bindings := map[string]bool{}
	for _, scope := range e.projections.scopes {
		for name := range scope {
			projection, _ := e.projections.projectionLookup(name)
			if !projection.constant && !e.funcs[name] && !e.typeNames[name] && e.imports[name] == "" && !e.dotNames[name] && e.known(name) {
				bindings[name] = true
			}
		}
	}
	var names []string
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	parent := e.program()
	child := fmt.Sprintf("%ssubProgram%d", e.prefix, len(e.marks))
	snapshot := child + "Snapshot"
	failure := child + "Error"
	rt := e.prefix + "rt."
	globalCells := map[string]bool{}
	for _, name := range names {
		if e.globalTypes[name] == "" {
			continue
		}
		local := false
		if e.inFunc {
			for _, scope := range e.scopes {
				local = local || scope[name]
			}
		} else {
			for _, scope := range e.scopes[1:] {
				local = local || scope[name]
			}
		}
		globalCells[name] = !local
	}
	projections := map[string]projection{}
	for _, name := range names {
		projections[name], _ = e.projections.projectionLookup(name)
	}
	previous := e.programExpr
	e.programExpr = child
	e.push()
	for _, name := range names {
		e.bind(name)
		e.projections.projectionBind(name, projections[name])
	}
	body, err := e.runtimeStatements(n.Stmts, e.runtimeScope())
	e.pop()
	e.programExpr = previous
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("{\n")
	for i, name := range names {
		fmt.Fprintf(&out, "%soriginal%d := &%s\n", child, i, e.goName(name))
	}
	fmt.Fprintf(&out, "%s,%s := %s.Subshell()\nif %s != nil {%s.Fail(%s)} else {\n%s = %s.Run(func(%s *%sProgram){\n", child, failure, parent, failure, parent, failure, failure, child, child, rt)
	for i, name := range names {
		if !globalCells[name] {
			fmt.Fprintf(&out, "%s := *%soriginal%d\n", name, child, i)
		}
	}
	fmt.Fprintf(&out, "%s := %sNewSnapshot(%s.Readonly,%s.Channels)\n", snapshot, rt, parent, child)
	for i, name := range names {
		fmt.Fprintf(&out, "%sMustReadonly(%sCapture(%s,%soriginal%d,&%s))\n", rt, rt, snapshot, child, i, name)
	}
	fmt.Fprintf(&out, "%sMustReadonly(%s.CloneContext(%s.Context))\n%s.Readonly = %s.Readonly()\n", rt, snapshot, child, child, snapshot)
	for _, name := range names {
		if globalCells[name] {
			fmt.Fprintf(&out, "%s.Present = %s.Present\n", e.lexicalCell(name, child), e.lexicalCell(name, parent))
		}
	}
	fmt.Fprintf(&out, "%sMustReadonly(%s.Bindings.RebindSnapshot(%s))\n", rt, child, snapshot)
	fmt.Fprintf(&out, "%s.ResultSidecars = %sMustValue(%sForkSidecars(%s.ResultSidecars,%s,&%sResultOwner{Channels:%s.Channels,Session:%s.Session}))\n", child, rt, rt, parent, snapshot, rt, child, child)
	out.WriteString(body)
	fmt.Fprintf(&out, "})\nif %s != nil {%s.Fail(%sSourceFailure(%s))} else {%s.SetStatus(%s.Status())}\n}\n}", failure, parent, rt, failure, parent, child)
	return out.String(), nil
}
