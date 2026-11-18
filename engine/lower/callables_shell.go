package lower

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (e *emitter) needsShell(file *syntax.File) {
	declared := map[string]bool{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if f, ok := node.(*syntax.BashPPFuncDecl); ok && f.Name != nil {
			declared[f.Name.Value] = true
		}
		return true
	})
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BashPPCall:
			if !file.GoSource && len(n.Fun) == 1 && !declared[n.Fun[0].Value] && (n.Fun[0].Value == "run" || n.Fun[0].Value == "start") {
				e.mixedShell = true
			}
		case *syntax.FuncDecl, *syntax.IfClause, *syntax.BinaryCmd:
			e.mixedShell = true
		case *syntax.CallExpr:
			if len(n.Assigns) > 0 {
				e.mixedShell = true
			}
			if len(n.Args) > 0 {
				name := n.Args[0].Lit()
				if name != "echo" && name != "printf" {
					e.mixedShell = true
				}
			}
		}
		return true
	})
	if e.mixedShell {
		e.execution = true
		e.bridge = true
	}
}

// nativeScalarShellAssignment retains the native arithmetic boundary and the
// canonical integer declarations used by shell-shaped local variables. Raw
// spellings and values requiring shell conversion stay in the persistent session.
func (e *emitter) nativeScalarShellAssignment(stmt *syntax.Stmt) bool {
	c, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(c.Assigns) == 0 || len(c.Args) != 0 || stmt.Negated || stmt.Background || len(stmt.Redirs) != 0 || e.readonly {
		return false
	}
	for _, a := range c.Assigns {
		if a.Name == nil || a.Index != nil || a.Array != nil || a.Append || a.Naked || a.Value == nil || len(a.Value.Parts) != 1 {
			return false
		}
		// A constant is not native storage, so this is not a native write. It
		// stays a shell assignment whose refusal the declaration policy owns;
		// claiming it here would emit a Go assignment to a Go constant and
		// reject the whole program at compile time instead.
		if e.lexicalConstantAssigned(a.Name.Value) {
			return false
		}
		if _, arithmetic := a.Value.Parts[0].(*syntax.ArithmExp); arithmetic {
			if !e.known(a.Name.Value) {
				return false
			}
			continue
		}
		lit, ok := a.Value.Parts[0].(*syntax.Lit)
		if !ok {
			return false
		}
		n, err := strconv.ParseInt(lit.Value, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != lit.Value {
			return false
		}
		if p, known := e.projections.projectionLookup(a.Name.Value); known {
			switch p.sourceType {
			case "", "int", "int64":
			case "int8", "int16", "int32":
				bits, _ := strconv.Atoi(strings.TrimPrefix(p.sourceType, "int"))
				if _, err := strconv.ParseInt(lit.Value, 10, bits); err != nil {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func (e *emitter) dynamicShell(stmt *syntax.Stmt) bool {
	if !e.mixedShell || stmt == nil || e.nativeScalarShellAssignment(stmt) {
		return false
	}
	switch n := stmt.Cmd.(type) {
	case *syntax.FuncDecl, *syntax.IfClause, *syntax.BinaryCmd:
		return true
	case *syntax.CallExpr:
		if len(n.Assigns) > 0 || len(n.Args) == 0 || stmt.Negated || stmt.Background || len(stmt.Redirs) > 0 {
			return true
		}
		name := n.Args[0].Lit()
		return name != "echo" && name != "printf"
	}
	return false
}
func (e *emitter) shellStatement(stmt *syntax.Stmt) (string, error) {
	var unsupported syntax.Node
	syntax.Walk(stmt, func(node syntax.Node) bool {
		if node == nil {
			return false
		}
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 && e.nativeShellNames[call.Args[0].Lit()] {
			unsupported = node
			return false
		}
		if _, scope := node.(*syntax.BashPPAgenticBlock); !scope && strings.HasPrefix(nodeName(node), "BashPP") {
			unsupported = node
			return false
		}
		return unsupported == nil
	})
	if unsupported != nil {
		return "", e.fail(unsupported, CodeUnsupported, "dynamic shell region contains a typed node requiring native dispatch")
	}
	var source bytes.Buffer
	if err := syntax.NewPrinter().Print(&source, stmt); err != nil {
		return "", err
	}
	return e.mark(stmt.Cmd) + e.program() + ".ShellRegion(" + strconv.Quote(source.String()) + ")\n", nil
}
