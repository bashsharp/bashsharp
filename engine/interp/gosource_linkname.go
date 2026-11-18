package interp

// Sprint: #243; Story: #674; Story-ID: 63073886bfce

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceLinknameDeclaration binds a package-level variable carrying a
// two-argument //go:linkname directive to the target package variable's cell.
// The interpreted path validates the supported directive shape itself;
// go/types does not enforce compiler pragmas. Unsupported initializer or
// storage reinterpretation forms stay explicit errors.
func (r *Runner) goSourceLinknameDeclaration(d *syntax.BashPPDecl) (error, bool) {
	if !r.bashPPGoSource || d.Site != syntax.StartVar || r.bashPPGoSourceFile == nil || !r.goSourceTopLevelDecl(d) {
		return nil, false
	}
	local, target, ok, directiveErr := r.goSourceDeclLinkname(d)
	if directiveErr != nil {
		return directiveErr, true
	}
	if !ok {
		return nil, false
	}
	if local != goSourceDeclaredName(d.Name.Value) {
		return fmt.Errorf("go:linkname local name %q does not name declaration %q", local, goSourceDeclaredName(d.Name.Value)), true
	}
	if !r.goSourceLinknameUnsafeImport(d) {
		return fmt.Errorf("go:linkname requires importing unsafe in its source file"), true
	}
	if d.InitExpr != nil || len(d.Init) != 0 {
		return fmt.Errorf("go:linkname alias initializers are unsupported"), true
	}
	root := r.bashPPScope
	for root.parent != nil {
		root = root.parent
	}
	cell := root.linknames[target]
	if cell == nil {
		cell = r.goSourceGeneratedInitTaskCell(d, target)
		if cell != nil {
			if root.linknames == nil {
				root.linknames = make(map[string]*bashPPCell)
			}
			root.linknames[target] = cell
		}
	}
	if cell == nil {
		return fmt.Errorf("go:linkname target %q is not a linked package variable", target), true
	}
	expected := d.DeclTypeExpr
	if expected == nil && d.DeclType != nil {
		expected = &syntax.BashPPNamedType{Name: d.DeclType}
	}
	actual := cell.declType
	if actual == nil && cell.typeName != "" {
		actual = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: cell.typeName}}
	}
	if expected == nil || actual == nil || r.goSourceDynamicTypeIdentity(expected) != r.goSourceDynamicTypeIdentity(actual) {
		return fmt.Errorf("go:linkname storage reinterpretation is unsupported"), true
	}
	if _, exists := r.bashPPScope.entries[d.Name.Value]; exists && d.Name.Value != "_" {
		return fmt.Errorf("%s redeclared in this block", d.Name.Value), true
	}
	r.bashPPScope.put(d.Name.Value, cell)
	return nil, true
}

// goSourceGeneratedInitTaskCell models the one package-local storage symbol
// synthesized by gc for package initialization. It is not a source variable,
// so it cannot have been published by goSourceRegisterLinknameTarget. Its
// linker spelling is deliberately exact: all ordinary missing targets retain
// the refusal above.
func (r *Runner) goSourceGeneratedInitTaskCell(d *syntax.BashPPDecl, target string) *bashPPCell {
	source, ok := r.bashPPGoSourceFile.SourceAt(d.Pos())
	if !ok {
		return nil
	}
	packagePath := source.PackagePath
	if packagePath == "" && source.Package == "main" {
		// The program package is deliberately stored without an import path;
		// gc nevertheless gives its generated symbols the main linker prefix.
		packagePath = "main"
	}
	if packagePath == "" || target != packagePath+"..inittask" {
		return nil
	}
	typ := d.DeclTypeExpr
	if typ == nil && d.DeclType != nil {
		typ = &syntax.BashPPNamedType{Name: d.DeclType}
	}
	if typ == nil {
		return nil
	}
	value, meta := r.bashPPZeroValue(typ)
	vr, meta := bashPPCollectionVariable(value), meta
	cell := &bashPPCell{vr: vr, declType: typ, valueMeta: meta}
	if meta != nil {
		cell.object = &bashPPObjectIdentity{owner: target, collection: meta}
	}
	return cell
}

// goSourceRegisterLinknameTarget publishes a successfully evaluated mapped
// package variable under its linker spelling. It runs after the declaration's
// initializer, so an alias never creates or initializes a second cell.
func (r *Runner) goSourceRegisterLinknameTarget(d *syntax.BashPPDecl) {
	if !r.bashPPGoSource || d.Site != syntax.StartVar || r.bashPPGoSourceFile == nil || !r.goSourceTopLevelDecl(d) {
		return
	}
	if r.exit.code != 0 || r.exit.exiting || r.exit.fatalExit {
		return
	}
	source, ok := r.bashPPGoSourceFile.SourceAt(d.Pos())
	if !ok || source.PackagePath == "" {
		return
	}
	cell := r.bashPPScope.lookup(d.Name.Value)
	if cell == nil {
		return
	}
	root := r.bashPPScope
	for root.parent != nil {
		root = root.parent
	}
	if root.linknames == nil {
		root.linknames = make(map[string]*bashPPCell)
	}
	key := source.PackagePath + "." + goSourceDeclaredName(d.Name.Value)
	if prior := root.linknames[key]; prior == nil {
		root.linknames[key] = cell
	}
}

func (r *Runner) goSourceTopLevelDecl(d *syntax.BashPPDecl) bool {
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		if stmt.Cmd == d {
			return true
		}
	}
	return false
}

func (r *Runner) goSourceDeclLinkname(d *syntax.BashPPDecl) (local, target string, ok bool, err error) {
	return r.goSourceLinknameIn(d.Pos(), d.Name.Value)
}

// goSourceLinknameIn finds the two-argument //go:linkname directive naming
// the declaration name in the source file that holds pos.
func (r *Runner) goSourceLinknameIn(pos syntax.Pos, name string) (local, target string, ok bool, err error) {
	source, present := r.bashPPGoSourceFile.SourceAt(pos)
	if !present {
		return
	}
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		for _, comment := range stmt.Comments {
			origin, found := r.bashPPGoSourceFile.SourceAt(comment.Hash)
			if !found || origin.Base != source.Base {
				continue
			}
			fields := strings.Fields(comment.Text)
			if len(fields) < 2 || fields[0] != "go:linkname" || fields[1] != goSourceDeclaredName(name) {
				continue
			}
			if ok {
				return "", "", true, fmt.Errorf("duplicate go:linkname for %s", fields[1])
			}
			if len(fields) == 2 {
				continue
			}
			if len(fields) != 3 {
				return "", "", true, fmt.Errorf("unsupported go:linkname directive for %s", fields[1])
			}
			local, target, ok = fields[1], fields[2], true
		}
	}
	return
}

func (r *Runner) goSourceLinknameUnsafeImport(d *syntax.BashPPDecl) bool {
	source, present := r.bashPPGoSourceFile.SourceAt(d.Pos())
	if !present {
		return false
	}
	for _, stmt := range r.bashPPGoSourceFile.Stmts {
		imp, ok := stmt.Cmd.(*syntax.BashPPImport)
		if !ok {
			continue
		}
		origin, found := r.bashPPGoSourceFile.SourceAt(imp.Pos())
		if !found || origin.Base != source.Base {
			continue
		}
		path := func(q *syntax.DblQuoted) string {
			if q == nil {
				return ""
			}
			var b strings.Builder
			for _, part := range q.Parts {
				lit, ok := part.(*syntax.Lit)
				if !ok {
					return ""
				}
				b.WriteString(lit.Value)
			}
			return b.String()
		}
		if path(imp.Path) == "unsafe" {
			return true
		}
		for _, spec := range imp.Specs {
			if path(spec.Path) == "unsafe" {
				return true
			}
		}
	}
	return false
}
