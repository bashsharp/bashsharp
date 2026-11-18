package gosource

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// lineDirectives records where the input's own Go line directives change the
// reported filename, one boundary per honored directive. Offsets are relative
// to the file's bytes. The parser already applied every directive to the
// fileset, so each candidate boundary is resolved by asking the token file for
// its adjusted position; a misplaced directive the scanner ignored resolves to
// the previous filename and records nothing. Line and column adjustments reach
// node positions through the fileset; the filename is what a position cannot
// carry.
func lineDirectives(tf *token.File, f *ast.File) []syntax.LineDirective {
	current := tf.Name()
	var out []syntax.LineDirective
	add := func(offset int) {
		if offset < 0 || offset > tf.Size() {
			return
		}
		name := tf.PositionFor(tf.Pos(offset), true).Filename
		if name != current {
			out = append(out, syntax.LineDirective{Offset: uint(offset), Filename: name})
			current = name
		}
	}
	for _, group := range f.Comments {
		for _, cm := range group.List {
			switch {
			case strings.HasPrefix(cm.Text, "//line "), strings.HasPrefix(cm.Text, "//line\t"):
				// A //line directive takes effect at the start of the next line.
				line := tf.PositionFor(cm.End(), false).Line
				if line < tf.LineCount() {
					add(tf.Offset(tf.LineStart(line + 1)))
				}
			case strings.HasPrefix(cm.Text, "/*line "), strings.HasPrefix(cm.Text, "/*line\t"):
				// A /*line*/ directive takes effect at the character following it.
				add(tf.Offset(cm.End()))
			}
		}
	}
	return out
}

// attachEmbedDirectives preserves the compiler directives written on the
// input's declarations — //go:embed on a var; //go:noinline, //go:nosplit,
// //go:norace, //go:noescape, //go:registerparams, //go:linkname,
// //go:uintptrescapes and the rest on a func — as Stmt.Comments on the
// positioned declaration, where the emitter (goDirectives) prints them
// verbatim above it so gc applies them as it did to the source: an embed
// consumes the same relative asset paths as the unchanged package, a
// noinline keeps the call the asmcheck row measures. Only the directive
// lines between the preceding declaration and this declaration travel;
// unlike go/ast's Doc field, gc's pragma association does not require the
// comment to be adjacent. //go:build and //go:generate address the file and
// the tooling, not a declaration. The name is historical: the //go:embed path
// was the first to exist.
//
// A linked (flattened) dependency package contributes only its
// //go:nointerface lines: that pragma changes the package's method sets as
// every importer observes them (under GOEXPERIMENT=fieldtrack), while the
// dependency's other directives keep their existing treatment.
func (c *converter) attachEmbedDirectives(file *syntax.File, linked []*converter) {
	byNamePos := map[uint][]syntax.Comment{}
	keep := isDeclDirective
	attach := func(name *ast.Ident, groups []*ast.CommentGroup) {
		for _, group := range groups {
			for _, comment := range group.List {
				if !keep(comment.Text) {
					continue
				}
				pos := c.pos(name.Pos()).Offset()
				byNamePos[pos] = append(byNamePos[pos], syntax.Comment{Hash: c.pos(comment.Slash), Text: strings.TrimPrefix(comment.Text, "//")})
			}
		}
	}
	scan := func(files []*ast.File) {
		for _, f := range files {
			c.attachFileDirectives(f, attach)
		}
	}
	scan(c.files)
	keep = isNointerfaceDirective
	for _, lc := range linked {
		if lc != c {
			scan(lc.files)
		}
	}
	// A body-less function of a linked package names its implementation
	// with //go:linkname; the interpreter needs that target to tell a pull
	// of its own interpreted code (go/types badlinkname_Checker_infer) from
	// a native companion symbol.
	keep = isLinknameDirective
	for _, lc := range linked {
		if lc == c {
			continue
		}
		for _, f := range lc.files {
			c.attachFileDirectives(f, func(name *ast.Ident, groups []*ast.CommentGroup) {
				if bodylessFunc(f, name) {
					attach(name, groups)
				}
			})
		}
	}
	for _, stmt := range file.Stmts {
		var name *syntax.Lit
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			name = d.Name
		case *syntax.BashPPFuncDecl:
			name = d.Name
		}
		if name != nil {
			stmt.Comments = append(stmt.Comments, byNamePos[name.Pos().Offset()]...)
		}
	}
}

// isNointerfaceDirective reports whether a comment line is //go:nointerface.
func isNointerfaceDirective(text string) bool {
	fields := strings.Fields(strings.TrimPrefix(text, "//"))
	return len(fields) > 0 && fields[0] == "go:nointerface" && strings.HasPrefix(text, "//go:")
}

// attachFileDirectives hands each declaration of f the comment groups that
// stand between it and the preceding declaration.
func (c *converter) attachFileDirectives(f *ast.File, attach func(*ast.Ident, []*ast.CommentGroup)) {
	previousEnd := f.Package
	for _, decl := range f.Decls {
		var groups []*ast.CommentGroup
		for _, group := range f.Comments {
			if group.Pos() > previousEnd && group.End() < decl.Pos() {
				groups = append(groups, group)
			}
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			attach(d.Name, groups)
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				previousEnd = decl.End()
				continue
			}
			declGroups := groups
			if len(d.Specs) != 1 && d.Doc != nil {
				declGroups = nil
				for _, group := range groups {
					if group != d.Doc {
						declGroups = append(declGroups, group)
					}
				}
			}
			for _, spec := range d.Specs {
				v := spec.(*ast.ValueSpec)
				if len(v.Names) != 1 {
					continue
				}
				specGroups := declGroups
				if v.Doc != nil && v.Doc != d.Doc {
					specGroups = append(append([]*ast.CommentGroup(nil), declGroups...), v.Doc)
				}
				attach(v.Names[0], specGroups)
			}
		}
		previousEnd = decl.End()
	}
}

// isDeclDirective reports whether a comment line is a compiler directive
// that addresses the declaration it documents.
func isDeclDirective(text string) bool {
	if !strings.HasPrefix(text, "//go:") {
		return false
	}
	directive, _, _ := strings.Cut(strings.TrimPrefix(text, "//go:"), " ")
	directive, _, _ = strings.Cut(directive, "\t")
	return directive != "build" && directive != "generate"
}

// isLinknameDirective reports whether a comment line is //go:linkname.
func isLinknameDirective(text string) bool {
	fields := strings.Fields(strings.TrimPrefix(text, "//"))
	return len(fields) > 0 && fields[0] == "go:linkname" && strings.HasPrefix(text, "//go:")
}

// bodylessFunc reports whether name declares a package function of f
// without a body.
func bodylessFunc(f *ast.File, name *ast.Ident) bool {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name == name {
			return fn.Recv == nil && fn.Body == nil
		}
	}
	return false
}
