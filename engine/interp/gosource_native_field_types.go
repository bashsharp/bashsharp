// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Static types for the fields of a dependency-owned struct. The interpreter's
// own field tables describe only the types this program declares, so a static
// walk that reaches an imported struct has had no answer to give. That gap was
// invisible while the chain was a plain selector path off a name, because the
// runtime walker reads the handle instead; a chain that crosses a map index or
// a call has no such walker to fall back on.

import (
	"go/types"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceNativeFieldType resolves one field of a dependency-owned struct type
// from the go/types metadata installed with its import.
//
// This is the static counterpart of [Runner.bashPPNativeLocalField]: that
// walker reads the field's VALUE, and can only do so for a selector path
// rooted at a name it can look up. `check.objMap[tname].tdecl.Assign` crosses
// interpreter storage on the way, so the declared field type is the only
// authority left on who owns the value — and without it the receiver of
// `.IsValid()` was classified as interpreter-owned and resolved to no callable
// at all.
func (r *Runner) goSourceNativeFieldType(parent syntax.BashPPTypeExpr, name string) (syntax.BashPPTypeExpr, bool) {
	if !r.bashPPGoSource {
		return nil, false
	}
	// Go's selector dereferences a pointer base implicitly, exactly as the
	// interpreter's own field resolution does.
	if pointer, ok := parent.(*syntax.BashPPPointerType); ok {
		parent = pointer.Element
	}
	owner := r.bashPPEmbeddedNativeType(parent)
	if owner == nil {
		return nil, false
	}
	if pointer, ok := types.Unalias(owner).Underlying().(*types.Pointer); ok {
		owner = pointer.Elem()
	}
	structure, ok := types.Unalias(owner).Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	for i := range structure.NumFields() {
		field := structure.Field(i)
		if field.Name() != name {
			continue
		}
		typ := syntax.BashPPTypeExprFromText(types.TypeString(field.Type(), r.goSourceNativeQualifier))
		return typ, typ != nil
	}
	return nil, false
}

// goSourceNativeQualifier spells a dependency package the way this program's
// own imports do. Every interpreter path that asks whether a type is the
// dependency's keys on the import alias, so a type recovered from export
// metadata has to come back in that spelling rather than in its package path.
func (r *Runner) goSourceNativeQualifier(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}
	for alias, path := range r.bashPPImports {
		if path == pkg.Path() && !strings.ContainsAny(alias, ".:") {
			return alias
		}
	}
	return pkg.Name()
}
