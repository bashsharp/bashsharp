// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #290; Story: #939; Story-ID: c7d8f9e96827
//
// A package-level type whose name is one of the dependency helper's own
// identifiers — `type request struct{...}` is ordinary Go — used to be left
// unregistered (bashPPHelperReserved), so every value of it crossing into an
// imported package (json.Unmarshal(data, &req)) failed as an unregistered
// bridge type. Such a type is now registered under a generated helper
// identity, the Sprint 243 scoped-identity mechanism keyed by the package
// scope "", and references spell that identity. Mirrored method stubs call
// back through the original name (Callback). As for instantiated generics,
// the generated identity is what a native %T prints; that fidelity gap is
// recorded, not hidden.

import (
	"sort"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPReservedPackageTypes returns the top-level, non-generic, non-alias
// type declarations of file whose name the helper reserves. A function-local
// declaration of such a name is not included: it keeps the refusal.
func bashPPReservedPackageTypes(file *syntax.File) (names []string, decls map[string]*syntax.BashPPDecl) {
	decls = map[string]*syntax.BashPPDecl{}
	if file == nil {
		return nil, decls
	}
	for _, stmt := range file.Stmts {
		d, ok := stmt.Cmd.(*syntax.BashPPDecl)
		if !ok || d.Site != syntax.StartTypeDecl || d.Name == nil || !bashPPHelperReserved[d.Name.Value] {
			continue
		}
		if len(d.TypeParams) > 0 || d.Alias || d.DeclTypeExpr == nil {
			continue
		}
		if _, dup := decls[d.Name.Value]; !dup {
			names = append(names, d.Name.Value)
		}
		decls[d.Name.Value] = d
	}
	sort.Strings(names)
	return names, decls
}

// bashPPReservedTypeSpelling spells a scalar's plain type name by the helper
// identity of a reserved package-level type — a defined scalar such as
// `type value int` crosses by name, not by a positioned type expression.
func (r *Runner) bashPPReservedTypeSpelling(name string) string {
	if !bashPPHelperReserved[name] {
		return name
	}
	if scoped, ok := r.bashPPScopedLocalTypeName(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}); ok {
		return scoped
	}
	return name
}
