package gosource

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
)

// defaultSourceImporter adds the source-listing half needed to rebuild an
// export-data dependency against an explicitly supplied package variant. In
// particular, go/importer's API mentions go/types objects; when a caller maps
// go/types from source, go/importer must be checked against that same package
// object rather than the distinct object embedded in its export data.
type defaultSourceImporter struct {
	types.Importer
}

func (defaultSourceImporter) SourcePackageFiles(path, srcDir string) (string, []string, error) {
	ctx := build.Default
	// go/build resolves a module package by running `go list` in ctx.Dir, or
	// in the process working directory when that is empty. The importing
	// directory is the context that decides the answer, never wherever the
	// embedding process happens to be running.
	if filepath.IsAbs(srcDir) {
		ctx.Dir = srcDir
	}
	pkg, err := ctx.Import(path, srcDir, 0)
	if err != nil {
		return "", nil, err
	}
	if len(pkg.CgoFiles) > 0 {
		return "", nil, fmt.Errorf("package %q has cgo files", path)
	}
	files := append([]string(nil), pkg.GoFiles...)
	return pkg.Dir, files, nil
}

func (d defaultSourceImporter) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	if from, ok := d.Importer.(types.ImporterFrom); ok {
		return from.ImportFrom(path, srcDir, mode)
	}
	return d.Import(path)
}

// SourcePackageLister is what an importer may offer beyond export data: the
// on-disk Go source files of a package (build-constrained, no cgo, no test
// files), so the map importer can re-check a dependency against the explicit
// package map instead of reading export data that was compiled against
// something else.
type SourcePackageLister interface {
	SourcePackageFiles(path, srcDir string) (dir string, files []string, err error)
}

// variantImport is cmd/go's test-variant rule, applied to export data: a
// dependency read through the fallback whose (transitive) imports name a
// mapped path was compiled against the ON-DISK package of that path, so its
// types would be a second, unrelated object — `types.Importer` from
// go/importer's export data is not the `types.Importer` of the ptest
// go/types the external test package is checked against. cmd/go rebuilds
// every such dependent against the test variant; here it is re-checked from
// its own source with this map importer, so every reference binds to the
// mapped object. A dependency that reaches no mapped path keeps its export
// data, and one without listable source (cgo, no lister) is left as read.
//
// Re-checking unifies the TYPES. When the program is linked for execution
// the mapped package is also the only copy that runs interpreted, while a
// dependent left on its compiled form still links the on-disk copy: two
// packages at run time. That is harmless until a value of a type the mapped
// package declares crosses between them, so a dependent whose exported API
// mentions such a type is promoted (promoteVariant): checked with its
// bodies and linked exactly as an explicit package is, leaving one copy. The
// returned note is non-empty when a dependent needed promotion and could not
// have it.
func (m *mapImporter) variantImport(path string, pkg *types.Package, srcDir string) (*types.Package, string, error) {
	if len(m.packages) == 0 || m.variants == nil {
		return pkg, "", nil
	}
	if v, ok := m.variants[path]; ok && v != nil {
		return v, m.variantNotes[path], nil
	}
	reaches, err := m.reachesMapped(pkg, srcDir, map[*types.Package]bool{})
	if err != nil {
		return nil, "", err
	}
	if !reaches {
		return pkg, "", nil
	}
	lister, ok := m.fallback.(SourcePackageLister)
	if !ok {
		return pkg, m.unpromotedNote(path, pkg, "the importer cannot list its source"), nil
	}
	dir, names, err := lister.SourcePackageFiles(path, srcDir)
	if err != nil || len(names) == 0 {
		reason := "it has no listable Go source"
		if err != nil {
			reason = err.Error()
		}
		return pkg, m.unpromotedNote(path, pkg, reason), nil
	}
	if m.variants[path] == nil && m.variantBusy[path] {
		return nil, "", fmt.Errorf("import cycle through the explicit package map at %q", path)
	}
	m.variantBusy[path] = true
	defer delete(m.variantBusy, path)
	sort.Strings(names)
	sources := make([]Source, 0, len(names))
	var files []*ast.File
	embeds := false
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, "", err
		}
		f, err := parser.ParseFile(m.fset, filepath.Join(dir, name), data, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, "", err
		}
		for _, spec := range f.Imports {
			embeds = embeds || spec.Path.Value == `"embed"`
		}
		sources = append(sources, Source{Name: filepath.Join(dir, name), Data: data})
		files = append(files, f)
	}
	// The variant's own imports are decided with the variant as the importer
	// (go/importer may import go/internal/gcimporter), never the program.
	savedFrom, savedIdentity := m.from, m.identity
	m.from, m.identity = path, importerIdentity{declared: true}
	defer func() { m.from, m.identity = savedFrom, savedIdentity }()
	var errs []error
	config := m.checker.config(m, func(err error) { errs = append(errs, err) })
	config.IgnoreFuncBodies = true
	checked, err := config.Check(path, m.fset, files, nil)
	if err != nil || len(errs) > 0 {
		if err == nil {
			err = errs[0]
		}
		return nil, "", fmt.Errorf("gosource: re-checking %q against the explicit package map: %v", path, err)
	}
	note := ""
	if exposed := m.exposesMapped(checked); exposed != "" {
		reason := ""
		switch {
		case !m.link:
		case embeds:
			reason = "it embeds files"
		case hasNonGoInputs(dir):
			reason = "it has non-Go inputs"
		default:
			promoted, err := m.promoteVariant(PackageSpec{Path: path, SourceDir: dir, Sources: sources})
			if err != nil {
				return nil, "", err
			}
			return promoted, "", nil
		}
		if reason != "" {
			note = unpromotedText(path, exposed, reason)
		}
	}
	m.variants[path] = checked
	if note != "" {
		m.variantNotes[path] = note
	}
	return checked, note, nil
}

// promoteVariant checks a dependent with its bodies and registers it in the
// explicit map, in dependency order: every dependent it imports was promoted
// (or kept) while it was being checked, and it is added before the package
// whose import asked for it finishes.
func (m *mapImporter) promoteVariant(spec PackageSpec) (*types.Package, error) {
	savedFrom, savedIdentity := m.from, m.identity
	defer func() { m.from, m.identity = savedFrom, savedIdentity }()
	if diagnostics := m.checkDependency(m.fset, spec, m.checker); len(diagnostics) > 0 {
		return nil, fmt.Errorf("gosource: linking %q against the explicit package map: %v", spec.Path, diagnostics[0])
	}
	m.promoted = append(m.promoted, spec.Path)
	return m.packages[spec.Path], nil
}

// unpromotedNote is the note for a dependent that reaches the map but whose
// source could not even be re-checked: its export data is all there is, so
// exposure is judged on that.
func (m *mapImporter) unpromotedNote(path string, pkg *types.Package, reason string) string {
	if !m.link {
		return ""
	}
	exposed := m.exposesMappedByPath(pkg)
	if exposed == "" {
		return ""
	}
	return unpromotedText(path, exposed, reason)
}

func unpromotedText(path, exposed, reason string) string {
	return fmt.Sprintf("package %q stays compiled against the on-disk copy of an interpreted package (%s); its API exposes %s, and values of that type cannot cross between the two copies", path, reason, exposed)
}

// hasNonGoInputs reports whether a package directory holds assembly, C or
// object inputs: such a package cannot be linked from its Go source alone.
func hasNonGoInputs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		switch filepath.Ext(entry.Name()) {
		case ".s", ".S", ".c", ".cc", ".cpp", ".cxx", ".m", ".h", ".hh", ".hpp", ".syso", ".f", ".F", ".f90", ".swig", ".swigcxx":
			return true
		}
	}
	return false
}

// exposesMapped names the first type declared by a mapped package that pkg's
// exported API mentions, or "" when there is none. pkg was checked against
// the map, so a mapped type is the map's own object.
func (m *mapImporter) exposesMapped(pkg *types.Package) string {
	return exposedType(pkg, func(owner *types.Package) bool {
		return owner != nil && m.packages[owner.Path()] == owner
	})
}

// exposesMappedByPath is exposesMapped for export data, whose packages are
// distinct objects from the map's: a mapped package is recognized by path.
func (m *mapImporter) exposesMappedByPath(pkg *types.Package) string {
	return exposedType(pkg, func(owner *types.Package) bool {
		if owner == nil {
			return false
		}
		_, ok := m.packages[owner.Path()]
		return ok
	})
}

// exposedType walks the exported API of pkg — the types of its exported
// package-level objects, through exported fields and exported methods — and
// names the first defined type owned by a package mapped accepts. A mapped
// interface made only of exported methods is behaviour, not representation:
// any value satisfying it may cross, so only its method signatures are
// walked. Unexported fields and methods are not API.
func exposedType(pkg *types.Package, mapped func(*types.Package) bool) string {
	seen := map[types.Type]bool{}
	var walk func(t types.Type) string
	walkTuple := func(tuple *types.Tuple) string {
		for i := range tuple.Len() {
			if hit := walk(tuple.At(i).Type()); hit != "" {
				return hit
			}
		}
		return ""
	}
	walk = func(t types.Type) string {
		if t == nil || seen[t] {
			return ""
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Alias:
			return walk(types.Unalias(t))
		case *types.Named:
			obj := t.Obj()
			iface, isInterface := t.Underlying().(*types.Interface)
			if mapped(obj.Pkg()) {
				behaviour := isInterface
				if isInterface {
					for i := range iface.NumMethods() {
						behaviour = behaviour && iface.Method(i).Exported()
					}
				}
				if !behaviour {
					return obj.Pkg().Path() + "." + obj.Name()
				}
			}
			if args := t.TypeArgs(); args != nil {
				for i := range args.Len() {
					if hit := walk(args.At(i)); hit != "" {
						return hit
					}
				}
			}
			for i := range t.NumMethods() {
				if method := t.Method(i); method.Exported() {
					if hit := walk(method.Type()); hit != "" {
						return hit
					}
				}
			}
			return walk(t.Underlying())
		case *types.Pointer:
			return walk(t.Elem())
		case *types.Slice:
			return walk(t.Elem())
		case *types.Array:
			return walk(t.Elem())
		case *types.Chan:
			return walk(t.Elem())
		case *types.Map:
			if hit := walk(t.Key()); hit != "" {
				return hit
			}
			return walk(t.Elem())
		case *types.Signature:
			if hit := walkTuple(t.Params()); hit != "" {
				return hit
			}
			return walkTuple(t.Results())
		case *types.Struct:
			for i := range t.NumFields() {
				if field := t.Field(i); field.Exported() {
					if hit := walk(field.Type()); hit != "" {
						return hit
					}
				}
			}
		case *types.Interface:
			for i := range t.NumMethods() {
				if method := t.Method(i); method.Exported() {
					if hit := walk(method.Type()); hit != "" {
						return hit
					}
				}
			}
		}
		return ""
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil || !obj.Exported() {
			continue
		}
		if hit := walk(obj.Type()); hit != "" {
			return hit
		}
	}
	return ""
}

// reachesMapped reports whether pkg or any package it imports, transitively,
// is in the explicit map.
func (m *mapImporter) reachesMapped(pkg *types.Package, srcDir string, seen map[*types.Package]bool) (bool, error) {
	if pkg == nil || seen[pkg] {
		return false, nil
	}
	seen[pkg] = true
	// Indexed export data can contain incomplete transitive package objects.
	// Their empty Imports list is not evidence that they cannot reach a mapped
	// package. Complete them through the SAME importer before inspecting their
	// dependency graph; keep package identity owned by that importer.
	if !pkg.Complete() {
		complete, err := m.completeImport(pkg.Path(), srcDir)
		if err != nil {
			return false, err
		}
		pkg = complete
	}
	for _, imp := range pkg.Imports() {
		if _, ok := m.packages[imp.Path()]; ok {
			return true, nil
		}
		if reaches, err := m.reachesMapped(imp, srcDir, seen); err != nil || reaches {
			return reaches, err
		}
	}
	return false, nil
}

// completeImport re-reads an already imported, incomplete transitive package
// so its dependency graph can be walked. It is not an import by the package
// being checked — the package was reached through a legal chain (fmt reaches
// internal/poll) — so the importer's visibility rule must not be applied to
// it: prefer the policy-free IdentityImporter, asking under the package's own
// identity, and fall back to the ordinary path otherwise.
func (m *mapImporter) completeImport(path, srcDir string) (*types.Package, error) {
	if by, ok := m.fallback.(IdentityImporter); ok {
		return by.ImportFromPackage(path, path, srcDir)
	}
	return m.fallbackImport(path, srcDir, 0)
}

var _ = token.NoPos
