// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Identity of function-local named types.
//
// A type declared inside a function body is a distinct type each time the
// declaration is spelled: two functions that each declare `type T struct{}`
// declare two types, and a package-level `T` is a third. Go's runtime keeps
// them apart in an assertion, a type switch and an interface comparison and
// reports the collision as `interface conversion: interface {} is main.T,
// not main.T (types from different scopes)`.
//
// The evaluator's type registry is keyed by name and shadowed per frame
// (see bashPPShadowLocalType), which is right for resolving a spelling to
// its declaration while the frame runs but says nothing about a value that
// left the frame inside an interface. The identity a dynamic type needs is
// lexical: a named-type reference resolves to the innermost declaration of
// that name in scope at the reference — the same resolution go/types
// performed when it checked the program. Every type expression the
// evaluator records as a dynamic type is an original AST node with its
// source position, so the resolution can be redone from the original file
// whenever two named types are compared.

// goSourceLocalTypeDecl is one type declaration inside a function body. holder
// is the innermost statement containing the declaration, which bounds the
// block the declaration's scope ends with.
type goSourceLocalTypeDecl struct {
	name        string
	pos         syntax.Pos
	holderStart syntax.Pos
	holderEnd   syntax.Pos
}

// goSourceLocalTypeIndex is the immutable per-file index of function-local
// type declarations. It is built with the shared declaration scan; runners
// keep only a fast pointer to it.
type goSourceLocalTypeIndex struct {
	file  *syntax.File
	decls map[string][]goSourceLocalTypeDecl
}

func (r *Runner) goSourceLocalTypes() *goSourceLocalTypeIndex {
	file := r.bashPPGoSourceFile
	if file == nil {
		return nil
	}
	if r.bashPPLocalTypes != nil && r.bashPPLocalTypes.file == file {
		return r.bashPPLocalTypes
	}
	// The immutable declaration index is shared weakly by file and includes
	// this scope index. Retain the parent cache through the Runner so a scope-
	// only consumer cannot let it be collected before a later consumer.
	r.bashPPLocalTypes = r.bashPPLocalTypeDeclarationIndex().localTypes
	return r.bashPPLocalTypes
}

func goSourceBuildLocalTypeIndex(file *syntax.File, stmtsByTop map[*syntax.Stmt][]*syntax.Stmt) *goSourceLocalTypeIndex {
	index := &goSourceLocalTypeIndex{file: file, decls: make(map[string][]goSourceLocalTypeDecl)}
	for _, top := range file.Stmts {
		// Every statement range below the top-level statement was collected by
		// the declaration scan, so a declaration's holder can be found without
		// another syntax walk.
		stmts := stmtsByTop[top]
		for _, s := range stmts {
			d, ok := s.Cmd.(*syntax.BashPPDecl)
			if !ok || d.Site != syntax.StartTypeDecl || d.Name == nil || s == top {
				continue
			}
			var holder *syntax.Stmt
			for _, candidate := range stmts {
				if candidate == s || !goSourceRangeContains(candidate, s.Pos()) || !goSourceRangeContains(candidate, s.End()) {
					continue
				}
				if holder == nil || goSourceRangeContains(holder, candidate.Pos()) && goSourceRangeContains(holder, candidate.End()) {
					holder = candidate
				}
			}
			if holder == nil {
				continue
			}
			index.decls[d.Name.Value] = append(index.decls[d.Name.Value], goSourceLocalTypeDecl{
				name: d.Name.Value, pos: d.Pos(), holderStart: holder.Pos(), holderEnd: holder.End(),
			})
		}
	}
	return index
}

func goSourceRangeContains(s *syntax.Stmt, pos syntax.Pos) bool {
	return !s.Pos().After(pos) && !pos.After(s.End())
}

// goSourceLocalTypeScope resolves a named-type reference to the declaration
// it names. scope is "" for a package-level or predeclared type and the
// declaration's offset for a function-local one; known reports whether the
// reference carries a source position at all — a synthesized type expression
// cannot be resolved and must not be mistaken for a package-level one.
func (r *Runner) goSourceLocalTypeScope(typ syntax.BashPPTypeExpr) (scope string, known bool) {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || !r.bashPPGoSource || named.Name == nil {
		return "", false
	}
	pos := named.Name.Pos()
	if !pos.IsValid() {
		return "", false
	}
	index := r.goSourceLocalTypes()
	if index == nil {
		return "", false
	}
	decls := index.decls[named.Name.Value]
	if len(decls) == 0 {
		return "", true
	}
	var best *goSourceLocalTypeDecl
	for i := range decls {
		d := &decls[i]
		if pos.After(d.holderStart) && d.holderEnd.After(pos) && pos.After(d.pos) && (best == nil || d.pos.After(best.pos)) {
			best = d
		}
	}
	if best == nil {
		return "", true
	}
	return strconv.FormatUint(uint64(best.pos.Offset()), 10), true
}

// goSourceSameTypeScope reports whether two named-type references that spell
// the same name resolve to the same declaration. Either side without a
// resolvable position leaves the answer to the spelling comparison the
// caller already made.
func (r *Runner) goSourceSameTypeScope(a, b syntax.BashPPTypeExpr) bool {
	scopeA, knownA := r.goSourceLocalTypeScope(a)
	scopeB, knownB := r.goSourceLocalTypeScope(b)
	if !knownA || !knownB {
		return true
	}
	return scopeA == scopeB
}
