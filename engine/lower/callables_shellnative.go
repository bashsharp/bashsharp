package lower

import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strconv"
	"strings"
)

func (e *emitter) findNativeShells(file *syntax.File) {
	e.nativeShellNames = map[string]bool{}
	typedNames := map[string]bool{}
	syntax.Walk(file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BashPPDecl:
			if n.Name != nil {
				typedNames[n.Name.Value] = true
			}
		case *syntax.BashPPShortDecl:
			for _, name := range n.Lhs {
				typedNames[name.Value] = true
			}
		}
		return true
	})
	syntax.Walk(file, func(node syntax.Node) bool {
		f, ok := node.(*syntax.FuncDecl)
		if !ok || f.Name == nil {
			return true
		}
		syntax.Walk(f.Body, func(n syntax.Node) bool {
			if p, ok := n.(*syntax.ParamExp); ok && p.Param != nil && typedNames[p.Param.Value] {
				e.nativeShellNames[f.Name.Value] = true
			}
			_, scope := n.(*syntax.BashPPAgenticBlock)
			if n != nil && !scope && strings.HasPrefix(nodeName(n), "BashPP") {
				e.nativeShellNames[f.Name.Value] = true
			}
			return true
		})
		return true
	})
}
func (e *emitter) nativeShellStatement(s *syntax.Stmt) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	switch n := s.Cmd.(type) {
	case *syntax.FuncDecl:
		if n.Name == nil {
			return "", false, nil
		}
		if !e.nativeShellNames[n.Name.Value] {
			captured := false
			for name := range e.visibleGlobals {
				if !e.typeNames[name] && !e.funcs[name] {
					captured = true
				}
			}
			if !captured {
				return "", false, nil
			}
			e.nativeShellNames[n.Name.Value] = true
		}
		if err := e.statementFlags(s); err != nil {
			return "", true, err
		}
		block, ok := n.Body.Cmd.(*syntax.Block)
		if !ok {
			return "", true, e.fail(n, CodeUnsupported, "native shell callable needs a block body")
		}
		priorProgram, priorFunc, priorResults := e.programExpr, e.inFunc, e.resultTypes
		priorGlobals, priorShell := e.functionGlobals, e.nativeShellBody
		e.functionGlobals = map[string]bool{}
		for name := range e.visibleGlobals {
			e.functionGlobals[name] = true
		}
		e.nativeShellBody = true
		e.programExpr = e.prefix + "program"
		e.inFunc = true
		e.resultTypes = nil
		e.push()
		body, err := e.block(block)
		e.pop()
		e.programExpr, e.inFunc, e.resultTypes = priorProgram, priorFunc, priorResults
		e.functionGlobals, e.nativeShellBody = priorGlobals, priorShell
		if err != nil {
			return "", true, err
		}
		capture := e.prefix + "shellCapture"
		view := "nil"
		return e.mark(n) + "{\n" + capture + " := " + e.program() + ".LexicalScope(" + view + ").Bindings\n" + e.program() + ".DefineNativeShell(" + strconv.Quote(n.Name.Value) + "," + strconv.FormatBool(n.Agentic != nil) + ",func(" + e.prefix + "program *" + e.prefix + "rt.Program){\n" + e.prefix + "shellProgram := *" + e.program() + "\n" + e.program() + " = &" + e.prefix + "shellProgram\n" + e.program() + ".Bindings = " + capture + "\n" + e.program() + " = " + e.program() + ".LexicalScope(nil)\n" + body + "\n})\n}\n", true, nil
	case *syntax.CallExpr:
		if e.nativeShellBody && len(n.Args) > 0 && n.Args[0].Lit() == "return" {
			value := "0"
			if len(n.Args) > 1 {
				if _, err := strconv.Atoi(n.Args[1].Lit()); err != nil {
					return "", true, e.fail(n, CodeUnsupported, "native shell return needs integer status")
				}
				value = n.Args[1].Lit()
			}
			return e.program() + ".SetStatus(" + value + "); return\n", true, nil
		}
		if len(n.Args) == 0 {
			return "", false, nil
		}
		name := n.Args[0].Lit()
		if !e.nativeShellNames[name] && (name == "" || name == "echo" || name == "printf" || !e.execution || len(n.Assigns) > 0 || len(s.Redirs) > 0 || s.Background || s.Negated) {
			return "", false, nil
		}
		if err := e.statementFlags(s); err != nil {
			return "", true, err
		}
		if len(n.Assigns) > 0 {
			return "", true, e.fail(n, CodeUnsupported, "native shell callable positional arguments need the shell parameter bridge")
		}
		arguments, err := e.positionalArguments(n.Args[1:])
		if err != nil {
			if !e.nativeShellNames[name] {
				return "", false, nil
			}
			return "", true, err
		}
		var raw bytes.Buffer
		if err := syntax.NewPrinter().Print(&raw, s); err != nil {
			return "", true, err
		}
		return e.mark(n) + "if !" + e.program() + ".CallNativeShell(" + e.callSite(n, n.Args[0].Lit()) + "," + e.prefix + "rt.StringArguments(" + arguments + ")...){" + e.program() + ".ShellRegion(" + strconv.Quote(raw.String()) + ")}\n", true, nil
	}
	return "", false, nil
}

func hasBadSubstitution(node syntax.Node) bool {
	found := false
	syntax.Walk(node, func(n syntax.Node) bool {
		if p, ok := n.(*syntax.ParamExp); ok && p.BadSubst != nil {
			found = true
		}
		return !found
	})
	return found
}
