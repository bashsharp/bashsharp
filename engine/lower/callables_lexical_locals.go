package lower

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

type lexicalEdit struct {
	start, end int
	text       string
}

func (e *emitter) lexicalLocals(file *ast.File, fs *token.FileSet, info *types.Info) []lexicalEdit {
	var edits []lexicalEdit
	sourceName := func(id *ast.Ident) bool {
		return id != nil && id.Name != "_" && !strings.HasPrefix(id.Name, e.prefix) && info.Defs[id] != nil
	}
	registration := func(ids []*ast.Ident, program, present string) string {
		text := ""
		for _, id := range ids {
			if !sourceName(id) {
				continue
			}
			object, ok := info.Defs[id].(*types.Var)
			if !ok || object.IsField() {
				continue
			}
			key := "local:" + strconv.Itoa(fs.Position(id.Pos()).Offset) + ":" + id.Name
			flag := e.prefix + "present" + strconv.Itoa(fs.Position(id.Pos()).Offset)
			kind := "KindScalar"
			switch object.Type().Underlying().(type) {
			case *types.Pointer:
				kind = "KindPointer"
			case *types.Interface:
				kind = "KindInterface"
			case *types.Struct, *types.Map, *types.Array, *types.Slice:
				kind = "KindObject"
			}
			text += flag + " := " + present + "\n" + e.prefix + "rt.MustReadonly(" + e.prefix + "rt.Register(" + program + ".Bindings," + strconv.Quote(key) + "," + strconv.Quote(id.Name) + ",&" + id.Name + ",&" + flag + "," + e.prefix + "rt." + kind + "))\n"
		}
		return text
	}
	var walk func(ast.Node, string)
	walk = func(node ast.Node, program string) {
		if node == nil {
			return
		}
		switch n := node.(type) {
		case *ast.FuncDecl:
			walk(n.Body, program)
			return
		case *ast.FuncLit:
			ownProgram := false
			for _, field := range n.Type.Params.List {
				if ptr, ok := field.Type.(*ast.StarExpr); ok {
					if sel, ok := ptr.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Program" && len(field.Names) == 1 {
						program = field.Names[0].Name
						ownProgram = true
					}
				}
			}
			if program == "" {
				return
			}
			var ids []*ast.Ident
			for _, field := range n.Type.Params.List {
				ids = append(ids, field.Names...)
			}
			if n.Type.Results != nil {
				for _, field := range n.Type.Results.List {
					ids = append(ids, field.Names...)
				}
			}
			offset := fs.Position(n.Body.Lbrace).Offset + 1
			for _, stmt := range n.Body.List {
				if assignment, ok := stmt.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
					if call, ok := assignment.Rhs[0].(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "LexicalScope" {
							offset = fs.Position(stmt.End()).Offset
						}
					}
				}
			}
			if ownProgram {
				edits = append(edits, lexicalEdit{offset, offset, "\n" + program + " = " + program + ".LexicalScope(nil)\n" + registration(ids, program, "true")})
			}
			for _, stmt := range n.Body.List {
				walk(stmt, program)
			}
			return
		case *ast.BlockStmt:
			hasLocal := false
			for _, stmt := range n.List {
				switch stmt := stmt.(type) {
				case *ast.AssignStmt:
					if stmt.Tok == token.DEFINE {
						for _, lhs := range stmt.Lhs {
							if id, ok := lhs.(*ast.Ident); ok && sourceName(id) {
								hasLocal = true
							}
						}
					}
				case *ast.DeclStmt:
					if gen, ok := stmt.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
						for _, spec := range gen.Specs {
							for _, id := range spec.(*ast.ValueSpec).Names {
								hasLocal = hasLocal || sourceName(id)
							}
						}
					}
				}
			}
			if hasLocal && program != "" {
				offset := fs.Position(n.Lbrace).Offset + 1
				edits = append(edits, lexicalEdit{offset, offset, "\n" + program + " := " + program + ".LexicalScope(nil)\n_ = " + program + "\n"})
			}
			for _, stmt := range n.List {
				walk(stmt, program)
			}
			return
		case *ast.AssignStmt:
			if program != "" && n.Tok == token.DEFINE {
				var ids []*ast.Ident
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						ids = append(ids, id)
					}
				}
				present := "true"
				if len(n.Lhs) > 1 {
					if id, ok := n.Lhs[len(n.Lhs)-1].(*ast.Ident); ok && strings.HasPrefix(id.Name, e.prefix) {
						if obj := info.Defs[id]; obj != nil && obj.Type().String() == "error" {
							present = id.Name + " == nil"
						}
					}
				}
				text := registration(ids, program, present)
				if text != "" {
					offset := fs.Position(n.End()).Offset
					edits = append(edits, lexicalEdit{offset, offset, "\n" + text})
				}
			}
		case *ast.DeclStmt:
			if program != "" {
				if gen, ok := n.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
					var ids []*ast.Ident
					for _, spec := range gen.Specs {
						ids = append(ids, spec.(*ast.ValueSpec).Names...)
					}
					text := registration(ids, program, "true")
					if text != "" {
						offset := fs.Position(n.End()).Offset
						edits = append(edits, lexicalEdit{offset, offset, "\n" + text})
					}
				}
			}
		case *ast.ForStmt:
			var ids []*ast.Ident
			if init, ok := n.Init.(*ast.AssignStmt); ok && init.Tok == token.DEFINE {
				for _, lhs := range init.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						ids = append(ids, id)
					}
				}
			}
			text := registration(ids, program, "true")
			if text != "" {
				offset := fs.Position(n.Body.Lbrace).Offset + 1
				edits = append(edits, lexicalEdit{offset, offset, "\n" + program + " := " + program + ".LexicalScope(nil)\n" + text})
			}
			if text != "" {
				for _, stmt := range n.Body.List {
					walk(stmt, program)
				}
			} else {
				walk(n.Body, program)
			}
			return
		case *ast.RangeStmt:
			var ids []*ast.Ident
			if n.Tok == token.DEFINE {
				for _, expr := range []ast.Expr{n.Key, n.Value} {
					if id, ok := expr.(*ast.Ident); ok {
						ids = append(ids, id)
					}
				}
			}
			text := registration(ids, program, "true")
			if text != "" {
				offset := fs.Position(n.Body.Lbrace).Offset + 1
				edits = append(edits, lexicalEdit{offset, offset, "\n" + program + " := " + program + ".LexicalScope(nil)\n" + text})
			}
			if text != "" {
				for _, stmt := range n.Body.List {
					walk(stmt, program)
				}
			} else {
				walk(n.Body, program)
			}
			return
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == nil {
				return false
			}
			if child == node {
				return true
			}
			walk(child, program)
			return false
		})
	}
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		program := ""
		for _, field := range function.Type.Params.List {
			if ptr, ok := field.Type.(*ast.StarExpr); ok {
				if sel, ok := ptr.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Program" && len(field.Names) == 1 {
					program = field.Names[0].Name
				}
			}
		}
		if program == "" {
			walk(function.Body, "")
			continue
		}
		var ids []*ast.Ident
		for _, field := range function.Type.Params.List {
			ids = append(ids, field.Names...)
		}
		if function.Type.Results != nil {
			for _, field := range function.Type.Results.List {
				ids = append(ids, field.Names...)
			}
		}
		if function.Recv != nil {
			for _, field := range function.Recv.List {
				ids = append(ids, field.Names...)
			}
		}
		offset := fs.Position(function.Body.Lbrace).Offset + 1
		// Named callable bodies may contain an explicit source-name scope reset.
		// Register their native parameters after that reset, not before it.
		for _, stmt := range function.Body.List {
			if assignment, ok := stmt.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
				if call, ok := assignment.Rhs[0].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "LexicalScope" {
						offset = fs.Position(stmt.End()).Offset
					}
				}
			}
		}
		edits = append(edits, lexicalEdit{offset, offset, "\n" + program + " = " + program + ".LexicalScope(nil)\n" + registration(ids, program, "true")})
		for _, stmt := range function.Body.List {
			walk(stmt, program)
		}
	}
	return edits
}
