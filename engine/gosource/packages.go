package gosource

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"sort"
	"strings"
)

// PackageSpec is one explicitly supplied dependency package: the import path
// it is registered under and the exact source files that make it up. The
// path is an opaque identity, exactly as `go tool compile -p` treats it; it
// is never looked up on disk.
type PackageSpec struct {
	Path    string
	Sources []Source
	// SourceDir is the original package directory used only when
	// CompanionFiles is non-empty. CompanionFiles is the caller's exact
	// host-selected non-Go input set; gosource never discovers files from the
	// directory or sends them through Go parsing.
	SourceDir      string
	CompanionFiles []string
}

// Resolution is one recorded import resolution. Load records every import
// the type checker asked for, in the order it asked, so the sequence is
// deterministic for a given set of inputs and can be listed and audited.
type Resolution struct {
	// From is the import path of the package whose file contained the import.
	From string
	// Import is the path exactly as written in the source.
	Import string
	// Path is the path after relative-import joining; equal to Import for a
	// non-relative import.
	Path string
	// Origin is "package-map" when the explicit map satisfied the import,
	// "linked-dependent" when the import is a dependent of a mapped package
	// that was linked from its own source so that one copy of the mapped
	// package exists (variants.go), and "importer" when the caller's importer
	// (module, GOPATH or export data) did. A rejected import is not
	// recorded; it is reported as a diagnostic.
	Origin string
	// Name is the resolved package's declared name.
	Name string
	// Files lists the explicit map package's source names, sorted; nil for
	// an importer resolution.
	Files []string
	// Note is set on an importer resolution whose package exposes types of
	// an interpreted (mapped) package yet could not be linked from source:
	// it stays compiled against the on-disk copy, and the note says why.
	Note string `json:",omitempty"`
}

// mapImporter implements the policy-free half of Go's import model: an
// explicit import-path→package map consulted before any on-disk policy, plus
// the compiler's `-D` rule for relative imports. It mirrors what
// `go tool compile -importcfg -D` does with `.a` files, in memory.
type mapImporter struct {
	base     string
	packages map[string]*types.Package
	files    map[string][]string
	fallback types.Importer
	from     string
	// identity qualifies from: whether it was declared (the map's path or
	// Options.ImportPath) and whether it is the asserted test main. See
	// import_visibility.go.
	identity    importerIdentity
	resolutions []Resolution
	// checked retains what checkDependency parsed and checked, keyed by
	// path, and order lists the paths as they were checked. Load links
	// each checked package into the lowered file from exactly this data:
	// the ASTs parsed from the caller's bytes, positioned in the shared
	// file set, and the checker's Info over them.
	checked map[string]*checkedPackage
	order   []string
	// variants are dependencies re-checked from source against the map
	// (variantImport); fset and checker are what Load checks with.
	variants    map[string]*types.Package
	variantBusy map[string]bool
	// link is set when the program is linked for execution: a dependent
	// whose API exposes a mapped package's types is then promoted into the
	// map (promoteVariant) and promoted lists those paths. variantNotes
	// keeps the note of a dependent that needed promotion and stayed
	// compiled.
	link         bool
	promoted     []string
	variantNotes map[string]string
	fset         *token.FileSet
	checker      checkerOptions
}

// checkedPackage is one explicit package after checkDependency: its sorted
// sources, their ASTs, the checker's Info over them and the resulting
// package object, which is the same pointer every importer of it receives.
type checkedPackage struct {
	spec         PackageSpec
	sources      []Source
	files        []*ast.File
	info         *types.Info
	pkg          *types.Package
	checkerNames map[string]string
}

func newMapImporter(base string, fallback types.Importer) *mapImporter {
	return &mapImporter{base: base, packages: map[string]*types.Package{}, files: map[string][]string{}, fallback: fallback, checked: map[string]*checkedPackage{}, variants: map[string]*types.Package{}, variantBusy: map[string]bool{}, variantNotes: map[string]string{}}
}

// newTypeInfo allocates the Info map set the converter reads. The program
// and every explicit package are checked into an Info of this shape so the
// same converter can lower any of them. Instances is what lets the converter
// spell every generic call's type arguments explicitly, inferred or not.
func newTypeInfo() *types.Info {
	return &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Implicits: map[ast.Node]types.Object{}, Scopes: map[ast.Node]*types.Scope{}, Selections: map[*ast.SelectorExpr]*types.Selection{}, Instances: map[*ast.Ident]types.Instance{}, FileVersions: map[*ast.File]string{}}
}

func isRelativeImport(p string) bool {
	return p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// resolve applies the compiler's import-path rules (resolveImportPath) and
// then the relative-import rule. Without a base a relative path is refused,
// as the compiler refuses it without -D; with a base it is joined and must
// stay inside the base.
func (m *mapImporter) resolve(p string) (string, error) {
	if err := resolveImportPath(p); err != nil {
		return "", err
	}
	if !isRelativeImport(p) {
		return p, nil
	}
	if m.base == "" {
		return "", fmt.Errorf("import %q: relative import path requires an import base", p)
	}
	joined := path.Join(m.base, p)
	if joined == ".." || strings.HasPrefix(joined, "../") || !strings.HasPrefix(joined, m.base) {
		return "", fmt.Errorf("import %q: relative import path escapes import base %q", p, m.base)
	}
	return joined, nil
}

func (m *mapImporter) Import(p string) (*types.Package, error) {
	return m.ImportFrom(p, "", 0)
}

func (m *mapImporter) ImportFrom(p, srcDir string, mode types.ImportMode) (*types.Package, error) {
	resolved, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	// Visibility is decided on the importer's declared identity before the
	// map is consulted: a mapped internal package is no more visible to a
	// foreign identity than an on-disk one, exactly as cmd/go would never
	// have written it into that importer's importcfg.
	if !m.visible(resolved) {
		return nil, visibilityError(resolved)
	}
	if pkg, ok := m.packages[resolved]; ok {
		m.resolutions = append(m.resolutions, Resolution{From: m.from, Import: p, Path: resolved, Origin: "package-map", Name: pkg.Name(), Files: m.files[resolved]})
		return pkg, nil
	}
	if isRelativeImport(p) {
		return nil, fmt.Errorf("import %q: package %q is not in the explicit package map", p, resolved)
	}
	pkg, err := m.fallbackImport(resolved, srcDir, mode)
	if err != nil {
		return nil, err
	}
	pkg, note, err := m.variantImport(resolved, pkg, srcDir)
	if err != nil {
		return nil, err
	}
	if linked, ok := m.packages[resolved]; ok && linked == pkg {
		m.resolutions = append(m.resolutions, Resolution{From: m.from, Import: p, Path: resolved, Origin: "linked-dependent", Name: pkg.Name(), Files: m.files[resolved]})
		return pkg, nil
	}
	m.resolutions = append(m.resolutions, Resolution{From: m.from, Import: p, Path: resolved, Origin: "importer", Name: pkg.Name(), Note: note})
	return pkg, nil
}

// add registers a type-checked package under its explicit path. The path is
// an identity: registering it twice is a caller error, not a merge.
func (m *mapImporter) add(checked *checkedPackage) error {
	spec := checked.spec
	if _, dup := m.packages[spec.Path]; dup {
		return fmt.Errorf("gosource: duplicate package path %q", spec.Path)
	}
	names := make([]string, 0, len(spec.Sources))
	for _, s := range spec.Sources {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	m.packages[spec.Path] = checked.pkg
	m.files[spec.Path] = names
	m.checked[spec.Path] = checked
	m.order = append(m.order, spec.Path)
	return nil
}

// checkDependency parses and type-checks one explicit package in lexical
// filename order and registers it, retaining the ASTs and the checker's Info
// for the link step. Parse and type diagnostics are returned together,
// attributed by file position exactly as the main package's are; nothing is
// converted or executed here.
func (m *mapImporter) checkDependency(fset *token.FileSet, spec PackageSpec, checker checkerOptions) ErrorList {
	if spec.Path == "" {
		return ErrorList{fmt.Errorf("gosource: explicit package with empty import path")}
	}
	if isRelativeImport(spec.Path) {
		return ErrorList{fmt.Errorf("gosource: explicit package path %q must not be relative", spec.Path)}
	}
	if len(spec.Sources) == 0 {
		return ErrorList{fmt.Errorf("gosource: explicit package %q has no source files", spec.Path)}
	}
	if len(spec.CompanionFiles) > 0 && spec.SourceDir == "" {
		return ErrorList{fmt.Errorf("gosource: explicit package %q companions require the original source directory", spec.Path)}
	}
	sources := append([]Source(nil), spec.Sources...)
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	// Same syntax-verdict policy as Load: checker-test mode may continue on
	// go/parser's recovered AST after retaining gc's diagnostics.
	syntaxErrors, gcFiles := syntaxVerdict(sources, checker)
	if len(syntaxErrors) > 0 && !checker.checkAfterSyntaxErrors && !checksAfterSyntaxVerdict(syntaxErrors) {
		return syntaxErrors
	}
	diagnostics := append(ErrorList(nil), syntaxErrors...)
	var files []*ast.File
	for i, s := range sources {
		if i > 0 && s.Name == sources[i-1].Name {
			return ErrorList{fmt.Errorf("gosource: duplicate file %q in package %q", s.Name, spec.Path)}
		}
		f, err := parseGoFile(fset, s.Name, s.Data, parser.ParseComments|parser.AllErrors)
		if err != nil && len(syntaxErrors) == 0 {
			diagnostics = appendDiagnostics(diagnostics, err)
		}
		if f != nil {
			if len(syntaxErrors) > 0 {
				mirrorGCTree(fset, f, gcFiles[i])
			}
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return diagnostics
	}
	checkerNames := prepareCgoFiles(fset, files)
	if len(syntaxErrors) > 0 {
		diagnostics = appendStructuralCheckerDiagnostics(diagnostics, gcFiles)
	}
	if len(syntaxErrors) == 0 {
		diagnostics = append(diagnostics, validateCompilerDirectives(fset, files, checker)...)
	}
	m.from = spec.Path
	// A mapped package's identity is always declared: it is the path the
	// map registers it under. It is never the test main.
	m.identity = importerIdentity{declared: true}
	info := newTypeInfo()
	typeErrors := newCheckerDiagnostics(fset, files, info, checker)
	config := checker.config(m, typeErrors.report)
	pkg, err := config.Check(spec.Path, fset, files, info)
	diagnostics = append(diagnostics, dropRecoveredCallUnused(typeErrors.result(err), fset, info, gcFiles)...)
	if len(diagnostics) > 0 {
		if checker.gcStderr() {
			diagnostics = sortGCStderr(fset, sources, diagnostics)
			diagnostics = limitGCStderr(fset, diagnostics, checker.errorLimit)
		}
		return diagnostics
	}
	if err := m.add(&checkedPackage{spec: spec, sources: sources, files: files, info: info, pkg: pkg, checkerNames: checkerNames}); err != nil {
		return ErrorList{err}
	}
	return nil
}
