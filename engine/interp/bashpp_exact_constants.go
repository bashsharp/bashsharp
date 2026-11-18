// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPPreparePackageConstants evaluates Go package constants in dependency
// order after imports are installed. The returned set tells the source-order
// loop which declarations have already been installed.
func (r *Runner) bashPPPreparePackageConstants(ctx context.Context, file *syntax.File) (map[syntax.Command]bool, error) {
	if file == nil || !file.GoSource {
		return nil, nil
	}
	type constantDecl struct {
		cmd  syntax.Command
		pos  syntax.Pos
		name string
		expr syntax.BashPPExpr
		spec *syntax.BashPPConstSpec
	}
	var decls []*constantDecl
	byName := make(map[string]*constantDecl)
	for _, stmt := range file.Stmts {
		switch cmd := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			if cmd.Site != syntax.StartConst {
				continue
			}
			decl := &constantDecl{cmd: cmd, pos: cmd.Pos(), name: cmd.Name.Value, expr: cmd.InitExpr}
			decls = append(decls, decl)
			if decl.name != "_" {
				byName[decl.name] = decl
			}
		case *syntax.BashPPConstGroup:
			var previous *syntax.BashPPConstSpec
			for _, spec := range cmd.Specs {
				effective := spec
				if spec.InitExpr == nil {
					copySpec := *spec
					copySpec.DeclType, copySpec.DeclTypeExpr = previous.DeclType, previous.DeclTypeExpr
					copySpec.Init, copySpec.InitExpr = previous.Init, previous.InitExpr
					effective = &copySpec
				} else {
					previous = spec
				}
				decl := &constantDecl{cmd: cmd, pos: spec.Pos(), name: spec.Name.Value, expr: effective.InitExpr, spec: effective}
				decls = append(decls, decl)
				if decl.name != "_" {
					byName[decl.name] = decl
				}
			}
		}
	}
	state := make(map[*constantDecl]uint8)
	prepared := make(map[syntax.Command]bool, len(decls))
	var visit func(*constantDecl) error
	visit = func(decl *constantDecl) error {
		switch state[decl] {
		case 1:
			return fmt.Errorf("%sBASHPP-ECONST-CYCLE: constant initialization cycle", r.bashErrPrefix(decl.pos))
		case 2:
			return nil
		}
		state[decl] = 1
		if decl.expr != nil {
			var dependencyErr error
			syntax.Walk(decl.expr, func(node syntax.Node) bool {
				ident, ok := node.(*syntax.BashPPIdent)
				if !ok {
					return true
				}
				dependency := byName[ident.Name.Value]
				if dependency == nil {
					return true
				}
				if err := visit(dependency); err != nil {
					dependencyErr = err
					return false
				}
				return true
			})
			if dependencyErr != nil {
				return dependencyErr
			}
		}
		switch cmd := decl.cmd.(type) {
		case *syntax.BashPPDecl:
			r.bashPPDeclare(ctx, r.bashPPBindDecl(cmd))
		case *syntax.BashPPConstGroup:
			one := *cmd
			one.Specs = []*syntax.BashPPConstSpec{decl.spec}
			r.bashPPConstGroup(ctx, r.bashPPBindConstGroup(&one))
		}
		if r.exit.code != 0 {
			return ExitStatus(r.exit.code)
		}
		state[decl] = 2
		prepared[decl.cmd] = true
		return nil
	}
	for _, decl := range decls {
		if err := visit(decl); err != nil {
			return nil, err
		}
	}
	return prepared, nil
}
