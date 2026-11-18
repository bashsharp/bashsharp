package gosource

import (
	"fmt"
	"go/types"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// IdentityImporter is the policy-free half of a fallback importer. The
// explicit package map declares the identity of every package it checks —
// PackageSpec.Path for a mapped package, Options.ImportPath for the program,
// the compiler's -p — and decides internal visibility on that identity
// (syntax.BashPPInternalImportVisible) before consulting the fallback. A
// fallback that implements this interface is then asked to resolve the path
// for that identity WITHOUT applying a directory-derived visibility rule of
// its own: the decision was already taken on the declared identity, and a
// directory rule keyed on where the files happen to sit (a scratch
// directory, a symlink-mirrored GOROOT) would refuse what the identity
// admits. srcDir is the importing file's directory exactly as go/types
// supplies it to ImporterFrom.
//
// A fallback that does not implement it is used through types.ImporterFrom
// as before, with whatever policy it carries; the map's refusal always comes
// first, so the negative decisions do not depend on the fallback.
//
// Programs without a declared identity — no Options.ImportPath — never reach
// this interface: their imports go through the fallback's own rule
// unchanged, so an ordinary program on disk behaves exactly as before.
type IdentityImporter interface {
	ImportFromPackage(path, identity, srcDir string) (*types.Package, error)
}

// visibilityError is the importer-side wording gc prints inside
// "could not import P (…)": cmd/go's ImportErrorf text, verbatim.
func visibilityError(path string) error {
	return fmt.Errorf("use of internal package %s not allowed", path)
}

// importerIdentity is what the map knows about the package it is checking
// right now: its declared identity (m.from), whether that identity was
// declared at all, and whether it is cmd/go's generated test main.
type importerIdentity struct {
	declared bool
	testMain bool
}

// visible applies the identity rule for the current importer. It is a
// no-op — reporting visible — when the importer's identity is not declared,
// so that nothing changes for a program checked without one.
func (m *mapImporter) visible(path string) bool {
	if !m.identity.declared {
		return true
	}
	return syntax.BashPPInternalImportVisible(m.from, path, m.identity.testMain)
}

// fallbackImport resolves a path the map does not hold. With a declared
// identity the fallback is asked through IdentityImporter when it offers
// it; otherwise, and for every undeclared importer, through
// types.ImporterFrom / types.Importer as before.
func (m *mapImporter) fallbackImport(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	if m.identity.declared {
		if by, ok := m.fallback.(IdentityImporter); ok {
			return by.ImportFromPackage(path, m.from, srcDir)
		}
	}
	if from, ok := m.fallback.(types.ImporterFrom); ok {
		return from.ImportFrom(path, srcDir, mode)
	}
	return m.fallback.Import(path)
}
