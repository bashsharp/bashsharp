package interp

import (
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceImportTypeIdentity spells typ with every dependency-qualified name
// written by import path instead of by the per-file alias the Go-source
// front end gave that import. Each file of a package map binds its own alias
// (__gosource_import_<pkg>_<file>_<import>) for the same package, so
// `map[string]*A.Package` in one file and `map[string]*B.Package` in another
// are one Go type whenever A and B import the same path.
func (r *Runner) goSourceImportTypeIdentity(typ syntax.BashPPTypeExpr) string {
	return bashPPBridgeTypeTextIn(typ, func(named *syntax.BashPPNamedType) (string, bool) {
		if name, ok := r.bashPPScopedLocalTypeName(named); ok {
			return name, true
		}
		alias, member, ok := strings.Cut(named.Name.Value, ".")
		if !ok {
			return "", false
		}
		if path := r.bashPPImports[alias]; path != "" {
			return strconv.Quote(path) + "." + member, true
		}
		return "", false
	})
}

// goSourceImportTypesIdentical reports whether actual and expected name the
// same type once import aliases are spelled by path. Only a Go-source run
// binds per-file aliases.
func (r *Runner) goSourceImportTypesIdentical(actual, expected syntax.BashPPTypeExpr) bool {
	if !r.bashPPGoSource || actual == nil || expected == nil || len(r.bashPPImports) == 0 {
		return false
	}
	return r.goSourceImportTypeIdentity(actual) == r.goSourceImportTypeIdentity(expected)
}
