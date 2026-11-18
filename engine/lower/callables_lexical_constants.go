// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package lower

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// A source `const` lowers to a real Go `const`. That is the whole point: the
// declaration is not storage, so no native path can write it and no missed
// check can quietly make it writable. Two things still have to happen for the
// shell boundary.
//
// First, `x=42` where x is constant must stop being lowered as a native Go
// assignment. It is not one — Go rejects it, and `Compile` was reporting
// `cannot assign to x (neither addressable nor a map index expression)` for a
// program the engine runs. The statement is an ordinary shell assignment whose
// outcome the declaration policy decides at runtime, so
// `nativeScalarShellAssignment` must decline to claim it. A compile-time
// refusal is not a substitute: the reproduced defect *is* a static rejection.
//
// Second, the constant needs a shadow registration at the boundary so a shell
// region can read `$x` and so the backend can be told which identity is
// constant. That is lexicalConstants, a []lexicalEdit producer shaped exactly
// like lexicalLocals so core installs it with one line.

// lexicalConstantAssigned reports that an ordinary shell assignment targets a
// constant. It reads the emitter's own scoped projection metadata, which
// already models shadowing, scope push/pop and captured function views — a
// second table keyed by name would have to re-derive all of that, and one
// keyed by emitter pointer would leak across concurrent compiles.
func (e *emitter) lexicalConstantAssigned(name string) bool {
	p, ok := e.projections.projectionLookup(name)
	return ok && p.constant
}

// lexicalConstants emits the shadow registration for every source constant
// declared inside a callable body. It is the postpass counterpart of
// lexicalLocals and produces nothing else, so core installs it in
// lexicalStorage with a single line:
//
//	edits = append(edits, e.lexicalConstants(file, fs, info)...)
//
// Identity keys match lexicalLocals exactly — "local:<offset>:<name>" — so a
// constant and a variable can never collide and a constant shadowing an outer
// binding of the same spelling keeps its own identity.
func (e *emitter) lexicalConstants(file *ast.File, fs *token.FileSet, info *types.Info) []lexicalEdit {
	var edits []lexicalEdit
	sourceName := func(id *ast.Ident) bool {
		return id != nil && id.Name != "_" && !strings.HasPrefix(id.Name, e.prefix) && info.Defs[id] != nil
	}
	registration := func(ids []*ast.Ident, program string) string {
		text := ""
		for _, id := range ids {
			if !sourceName(id) {
				continue
			}
			object, ok := info.Defs[id].(*types.Const)
			if !ok {
				continue
			}
			key := "local:" + strconv.Itoa(fs.Position(id.Pos()).Offset) + ":" + id.Name
			// The value is passed, never its address: `&x` does not compile
			// for a constant, and rewriting the declaration into a var to
			// obtain one would be the demotion this file exists to avoid.
			text += e.prefix + "rt.MustReadonly(" + e.prefix + "rt.RegisterConstant(" +
				program + ".Bindings," + strconv.Quote(key) + "," + strconv.Quote(id.Name) + "," +
				strconv.Quote(constantSourceType(object)) + "," + id.Name + "," +
				e.prefix + "rt." + constantKind(object) + "))\n"
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
			walk(n.Body, functionProgram(n.Type, program))
			return
		case *ast.FuncLit:
			// The entry's body is a literal passed to Program.Run, so its
			// program parameter is the only context the root's own constants
			// ever have. Descending into literals with an inherited program is
			// what keeps `Execute -> Run(func(program *rt.Program){...})`
			// constants registered rather than silently dropped.
			walk(n.Body, functionProgram(n.Type, program))
			return
		case *ast.DeclStmt:
			if program != "" {
				if gen, ok := n.Decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
					var ids []*ast.Ident
					for _, spec := range gen.Specs {
						value, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						ids = append(ids, value.Names...)
					}
					if text := registration(ids, program); text != "" {
						offset := fs.Position(n.End()).Offset
						edits = append(edits, lexicalEdit{offset, offset, "\n" + text})
					}
				}
			}
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
		// A declaration without a program parameter is still descended into:
		// the entry and the source main take none, and hold the root body as
		// a literal that does.
		program := functionProgram(function.Type, "")
		for _, stmt := range function.Body.List {
			walk(stmt, program)
		}
	}
	return edits
}

// functionProgram finds the runtime program parameter a body registers
// against, falling back to the one it was reached with. A body with neither
// has no boundary to register against.
func functionProgram(signature *ast.FuncType, inherited string) string {
	if signature == nil || signature.Params == nil {
		return inherited
	}
	for _, field := range signature.Params.List {
		ptr, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := ptr.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Program" || len(field.Names) != 1 {
			continue
		}
		return field.Names[0].Name
	}
	return inherited
}

// constantSourceType reports the source type spelling the boundary should
// report, which is the declared named type rather than Go's rendering of its
// underlying type: `type Amount int` must diagnose as Amount.
func constantSourceType(object *types.Const) string {
	if named, ok := object.Type().(*types.Named); ok {
		return named.Obj().Name()
	}
	return object.Type().String()
}

func constantKind(object *types.Const) string {
	switch object.Type().Underlying().(type) {
	case *types.Interface:
		return "KindInterface"
	}
	return "KindScalar"
}
