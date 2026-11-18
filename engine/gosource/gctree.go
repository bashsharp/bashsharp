package gosource

import (
	"go/ast"
	"go/token"
	"reflect"

	gcsyntax "github.com/bashsharp/bashsharp/engine/gosource/internal/gcsyntax"
)

// mirrorGCTree makes go/parser's tree for one file agree with gc's own tree
// where gc's parser recovered from a diagnostic that is not a syntax error,
// so go/types checks what types2 checks and reports what types2 reports.
// go/parser keeps the rejected text; gc's tree records the rejection:
//
//   - a literal the scanner rejected is Bad (scanner.go setLit). types2
//     evaluates it as an invalid operand without a message (expr.go: "error
//     reported during parsing"), and skips an import whose path is Bad
//     (resolver.go). The literal is replaced by *ast.BadExpr, which go/types
//     treats the same way; an import path is blanked, which go/types skips
//     ("error reported by parser"); a struct tag is dropped.
//   - a method with no receiver is a plain function and a method with
//     several receivers keeps the first (parser.go funcDeclOrNil). go/parser
//     keeps the whole receiver list, on which go/types would repeat the
//     parser's diagnostic.
//   - a receiver written as ...T is T (parser.go paramList: "use T instead
//     of invalid ...T", after "invalid use of ..."). go/parser parses the
//     receiver list like a parameter list, where a final ...T is legal, so
//     the Ellipsis reaches go/types, which reports the parser's diagnostic
//     again as an invalid syntax tree.
//   - a type parameter list in which no parameter has a constraint
//     ("missing type constraint") declares each name with a Bad constraint
//     (parser.go paramList: a lone name is the parameter's name and its
//     type is a BadExpr at the same position). go/parser keeps each lone
//     name as an unnamed field's constraint type, on which go/types
//     declares nothing and reports the name undefined. Once one parameter
//     has a constraint both parsers name the rest and give them a Bad
//     type, and both checkers ignore the Bad type without a message.
//
// Both policies check the mirrored tree: gc's stderr because types2 checks
// gc's tree, the checker-test policy because its runners' own parsers make
// the same recoveries before their checker runs (the go/types runner's
// go/parser drops the receiver list's error entirely and the syntax
// runner's parser is gc's), so neither ever sees a checker diagnostic
// repeat its parser's.
//
// Nodes are matched by their absolute (not //line-adjusted) position, which
// both parsers assign to the same byte.
func mirrorGCTree(fset *token.FileSet, file *ast.File, gc *gcsyntax.File) {
	if gc == nil {
		return
	}
	type lineCol struct{ line, col uint }
	bad := map[lineCol]bool{}
	funcs := map[lineCol]*gcsyntax.FuncDecl{}
	badTParams := map[lineCol]bool{}
	// After a syntax error gc's tree may hold a typed nil where the parser
	// gave up (an import declaration without a path); the walker visits it.
	gcsyntax.Inspect(gc, func(n gcsyntax.Node) bool {
		switch n := n.(type) {
		case *gcsyntax.BasicLit:
			if n == nil {
				return false
			}
			if n.Bad {
				bad[lineCol{n.Pos().Line(), n.Pos().Col()}] = true
			}
		case *gcsyntax.FuncDecl:
			if n == nil {
				return false
			}
			funcs[lineCol{n.Pos().Line(), n.Pos().Col()}] = n
			for _, f := range n.TParamList {
				if _, isBad := f.Type.(*gcsyntax.BadExpr); isBad {
					badTParams[lineCol{f.Pos().Line(), f.Pos().Col()}] = true
				}
			}
		}
		return true
	})
	if len(bad) == 0 && len(funcs) == 0 {
		return
	}
	at := func(pos token.Pos) lineCol {
		p := fset.PositionFor(pos, false)
		return lineCol{uint(p.Line), uint(p.Column)}
	}
	isBad := func(lit *ast.BasicLit) bool { return lit != nil && bad[at(lit.Pos())] }

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Type.TypeParams != nil && len(badTParams) > 0 {
			for _, field := range fn.Type.TypeParams.List {
				name, ok := field.Type.(*ast.Ident)
				if len(field.Names) == 0 && ok && badTParams[at(name.Pos())] {
					field.Names = []*ast.Ident{name}
					field.Type = &ast.BadExpr{From: name.Pos(), To: name.End()}
				}
			}
		}
		if fn.Recv == nil {
			continue
		}
		// gc positions the declaration at the token after "func": the
		// receiver's "(" here.
		gcFn, ok := funcs[at(fn.Recv.Opening)]
		if !ok {
			continue
		}
		switch {
		case gcFn.Recv == nil:
			fn.Recv = nil
			continue
		case len(fn.Recv.List) == 0:
			continue
		case fn.Recv.NumFields() > 1:
			fn.Recv.List = fn.Recv.List[:1]
			fn.Recv.List[0].Names = fn.Recv.List[0].Names[:min(1, len(fn.Recv.List[0].Names))]
		}
		if _, dots := gcFn.Recv.Type.(*gcsyntax.DotsType); !dots {
			if ell, ok := fn.Recv.List[0].Type.(*ast.Ellipsis); ok && ell.Elt != nil {
				fn.Recv.List[0].Type = ell.Elt
			}
		}
	}
	if len(bad) == 0 {
		return
	}
	badExpr := func(lit *ast.BasicLit) ast.Expr { return &ast.BadExpr{From: lit.Pos(), To: lit.End()} }
	exprType := reflect.TypeFor[ast.Expr]()
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case nil:
			return false
		case *ast.ImportSpec:
			if isBad(n.Path) {
				n.Path.Value = ""
			}
			return false
		case *ast.Field:
			if isBad(n.Tag) {
				n.Tag = nil
			}
		}
		// Every other *ast.BasicLit sits in an ast.Expr slot, directly or in
		// a []ast.Expr; replace it there.
		v := reflect.ValueOf(n).Elem()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			switch {
			case f.Type() == exprType:
				if lit, ok := f.Interface().(*ast.BasicLit); ok && isBad(lit) {
					f.Set(reflect.ValueOf(badExpr(lit)))
				}
			case f.Kind() == reflect.Slice && f.Type().Elem() == exprType:
				for j := 0; j < f.Len(); j++ {
					if lit, ok := f.Index(j).Interface().(*ast.BasicLit); ok && isBad(lit) {
						f.Index(j).Set(reflect.ValueOf(badExpr(lit)))
					}
				}
			}
		}
		return true
	})
}
