package gosource

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func sourcesImportC(sources []Source) bool {
	for _, source := range sources {
		file, _ := parser.ParseFile(token.NewFileSet(), source.Name, source.Data, parser.ImportsOnly)
		if file == nil {
			continue
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, `"`) == "C" {
				return true
			}
		}
	}
	return false
}

func automaticCgoEnabled() bool { return build.Default.CgoEnabled }

// prepareCgoFiles gives a package-scope Go declaration named C a private
// spelling for go/types. cmd/cgo does not introduce C into the Go file block:
// it consumes C.name selectors and removes import "C" before the ordinary
// compiler sees the file. types.Config.FakeImportC is close, but incorrectly
// declares C as a package name and collides with the user's declaration.
//
// The returned map is private spelling -> source spelling. Conversion applies
// it to checked objects and type strings, so the rewrite is never observable.
func prepareCgoFiles(fset *token.FileSet, files []*ast.File) map[string]string {
	hasCgo := false
	used := map[string]bool{}
	for _, file := range files {
		for _, spec := range file.Imports {
			hasCgo = hasCgo || strings.Trim(spec.Path.Value, `"`) == "C"
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok {
				used[id.Name] = true
			}
			return true
		})
	}
	if !hasCgo {
		return nil
	}
	fileMap := make(map[string]*ast.File, len(files))
	for _, file := range files {
		fileMap[fset.Position(file.Pos()).Filename] = file
	}
	pkg, _ := ast.NewPackage(fset, fileMap, func(imports map[string]*ast.Object, path string) (*ast.Object, error) {
		if found := imports[path]; found != nil {
			return found, nil
		}
		obj := ast.NewObj(ast.Pkg, path)
		obj.Data = ast.NewScope(nil)
		imports[path] = obj
		return obj, nil
	}, nil)
	if pkg == nil {
		return nil
	}
	object := pkg.Scope.Lookup("C")
	if object == nil || object.Kind == ast.Pkg {
		return nil
	}
	alias := ""
	for index := 0; alias == "" || used[alias]; index++ {
		alias = "__gosource_go_C_" + tokenName(index)
	}
	selectorBases := map[*ast.Ident]bool{}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok {
				if id, ok := ast.Unparen(sel.X).(*ast.Ident); ok && id.Name == "C" {
					selectorBases[id] = true
				}
			}
			return true
		})
	}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			id, ok := node.(*ast.Ident)
			if ok && id.Obj == object && !selectorBases[id] {
				id.Name = alias
			}
			return true
		})
	}
	object.Name = alias
	return map[string]string{alias: "C"}
}

func tokenName(index int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if index < len(digits) {
		return digits[index : index+1]
	}
	return tokenName(index/len(digits)-1) + tokenName(index%len(digits))
}

func cgoPackages(linked []*converter) []syntax.CgoPackage {
	var result []syntax.CgoPackage
	for _, c := range linked {
		var preamble, alias string
		for _, file := range c.files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.IMPORT {
					continue
				}
				for _, item := range gen.Specs {
					spec := item.(*ast.ImportSpec)
					if strings.Trim(spec.Path.Value, `"`) != "C" {
						continue
					}
					var object types.Object
					if spec.Name != nil {
						object = c.info.Defs[spec.Name]
					} else {
						object = c.info.Implicits[spec]
					}
					if name := c.renames[object]; name != "" {
						alias = name
					} else if object != nil {
						alias = object.Name()
					} else {
						alias = "C"
					}
					doc := spec.Doc
					if doc == nil {
						doc = gen.Doc
					}
					if doc != nil {
						var b strings.Builder
						for _, comment := range doc.List {
							b.WriteString(comment.Text)
							b.WriteByte('\n')
						}
						preamble = b.String()
					}
				}
			}
		}
		if alias == "" {
			continue
		}
		type use struct {
			kind       string
			args       []string
			resultUsed bool
		}
		uses := map[string]use{}
		for _, file := range c.files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
				if !ok || !cgoSelector(c, sel) {
					return true
				}
				args := make([]string, len(call.Args))
				for i, arg := range call.Args {
					args[i] = cgoProbeArg(c.info.Types[arg].Type)
				}
				uses[sel.Sel.Name] = use{kind: "func", args: args, resultUsed: cgoResultUsed(file, call)}
				return true
			})
			ast.Inspect(file, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if ok && cgoSelector(c, sel) {
					if _, found := uses[sel.Sel.Name]; !found {
						uses[sel.Sel.Name] = use{kind: "unsupported"}
					}
				}
				return true
			})
		}
		names := make([]string, 0, len(uses))
		for name := range uses {
			names = append(names, name)
		}
		sort.Strings(names)
		pkg := syntax.CgoPackage{Path: c.packagePath, Alias: alias, Preamble: preamble}
		for _, name := range names {
			u := uses[name]
			pkg.Symbols = append(pkg.Symbols, syntax.CgoSymbol{Name: name, Kind: u.kind, ProbeArgs: u.args, ResultUsed: u.resultUsed})
		}
		result = append(result, pkg)
	}
	return result
}

func cgoSelector(c *converter, sel *ast.SelectorExpr) bool {
	id, ok := ast.Unparen(sel.X).(*ast.Ident)
	if !ok {
		return false
	}
	pkg, ok := c.info.ObjectOf(id).(*types.PkgName)
	return ok && pkg.Imported().Path() == "C"
}

func cgoProbeArg(typ types.Type) string {
	if typ == nil {
		return "nil"
	}
	switch t := typ.Underlying().(type) {
	case *types.Basic:
		if t.Info()&types.IsBoolean != 0 {
			return "false"
		}
		if t.Info()&(types.IsInteger|types.IsFloat|types.IsComplex) != 0 {
			return "0"
		}
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return "nil"
	}
	return "nil"
}

func cgoResultUsed(file *ast.File, target *ast.CallExpr) bool {
	used := true
	ast.Inspect(file, func(node ast.Node) bool {
		expr, ok := node.(*ast.ExprStmt)
		if ok && ast.Unparen(expr.X) == target {
			used = false
		}
		return true
	})
	return used
}
