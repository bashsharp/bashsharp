// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #53; Story-ID: 99bd1de0093b

import "github.com/bashsharp/bashsharp/engine/syntax"

// Go resolves package-level identifiers over the whole package rather than over
// the text preceding them, so a named type may be used before the line that
// declares it. The interpreter runs declarations as statements in source order,
// which on its own can only look backwards: the Tour's web crawler declares
// `type fakeFetcher map[string]*fakeResult` above `type fakeResult struct{...}`
// and was rejected as `undefined type: fakeResult`.
//
// Registration is therefore split in two. Pass one installs every package-level
// type declaration under its name; pass two is the ordinary statement walk,
// which validates each declaration's representation and binds it where it
// stands. Only the file's own top-level declarations are pre-registered —
// declarations inside function bodies keep their sequential, block-scoped
// treatment, so Go's scoping and shadowing rules are unchanged — and only the
// registry is populated early, so variable initializers still evaluate in
// package initialization order.
func (r *Runner) bashPPGoSourceRegisterTypes(file *syntax.File) {
	r.bashPPGoSourcePending = nil
	if file == nil || !file.GoSource {
		return
	}
	declared := map[string]int{}
	for _, stmt := range file.Stmts {
		if d, ok := stmt.Cmd.(*syntax.BashPPDecl); ok && d.Site == syntax.StartTypeDecl {
			declared[d.Name.Value]++
		}
	}
	for _, stmt := range file.Stmts {
		d, ok := stmt.Cmd.(*syntax.BashPPDecl)
		if !ok || d.Site != syntax.StartTypeDecl || d.DeclType == nil {
			continue
		}
		name := d.Name.Value
		// A name this file declares twice, or one already bound by an earlier
		// Run on the same runner, is a redeclaration. Leave the registry as it
		// stands so pass two reports it at the line that wrote it.
		if declared[name] != 1 || !syntax.BashPPValidIdent(name) {
			continue
		}
		if _, exists := r.bashPPTypes[name]; exists {
			continue
		}
		r.bashPPUnshareTypes()
		if r.bashPPTypes == nil {
			r.bashPPTypes = make(map[string]bashPPType)
		}
		if r.bashPPGoSourcePending == nil {
			r.bashPPGoSourcePending = map[string]bool{}
		}
		if r.bashPPGoSourceDecls == nil {
			r.bashPPGoSourceDecls = map[string]bool{}
		}
		// Enum members are deliberately left out: pass two owns them, and a
		// forward reference can only name the type, never one of its members.
		r.bashPPTypes[name] = bashPPType{
			underlying: d.DeclType.Value,
			alias:      d.Alias,
			typeParams: d.TypeParams,
			typeExpr:   d.DeclTypeExpr,
			fields:     d.StructFields,
		}
		r.bashPPInvalidateSelectionCache()
		r.bashPPGoSourcePending[name] = true
		// Reset purges whatever a gosource tree installed; a pre-registered
		// type is no different from one installed by its own statement.
		r.bashPPGoSourceDecls[name] = true
	}
}

// bashPPGoSourceClaimType reports whether name was placed in the registry by
// pass one and has not yet been claimed by its own declaration statement. The
// claim is consumed, so a later redeclaration is diagnosed normally.
func (r *Runner) bashPPGoSourceClaimType(name string) bool {
	if !r.bashPPGoSourcePending[name] {
		return false
	}
	delete(r.bashPPGoSourcePending, name)
	return true
}
