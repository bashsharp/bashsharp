// Package gosource loads unmodified Go source into the positioned Bash++ AST.
// It uses Go's lexer, parser and type checker; shell parsing is never a fallback.
package gosource

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"go/version"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const Version = "gosource-v1"

// ErrorList retains every diagnostic emitted by the Go lexer/parser/checker.
// Error does not collapse scanner errors into an "and N more errors" summary.
type ErrorList []error

func (e ErrorList) Error() string {
	lines := make([]string, len(e))
	for i, err := range e {
		if typeErr, ok := err.(types.Error); ok && strings.HasPrefix(typeErr.Msg, "\t") {
			lines[i] = "\t" + typeErr.Fset.Position(typeErr.Pos).String() + ": " + strings.TrimPrefix(typeErr.Msg, "\t")
		} else {
			lines[i] = err.Error()
		}
	}
	return strings.Join(lines, "\n")
}
func appendDiagnostics(out ErrorList, err error) ErrorList {
	if list, ok := err.(scanner.ErrorList); ok {
		for _, item := range list {
			out = append(out, item)
		}
	} else if err != nil {
		out = append(out, err)
	}
	return out
}

type Source struct {
	Name string
	Data []byte
}
type SourceInfo = syntax.SourceFile

type Options struct {
	// RunMain appends init and main entry calls. Loading itself never executes code.
	RunMain bool
	// PreserveNativeInit leaves init declarations named init for a native
	// library build. It is incompatible with RunMain, which needs uniquely
	// named callable init functions.
	//
	// With Packages it selects native units: every mapped package is
	// type-checked and consulted for the program's imports exactly as
	// before, but nothing of it is linked into File. File is then one unit
	// of the program package alone — its package clause, its package-level
	// names unchanged, its imports of mapped packages kept as imports for
	// the compiler's own -D/-importcfg resolution, and its init declarations
	// left to the toolchain — so a caller lowers a package map one package
	// per unit (lower.Options.Library), as cmd/go compiles it. Without it
	// the map is flattened into File for the interpreter.
	PreserveNativeInit bool
	// Importer may resolve module dependencies. Nil uses the Go export importer.
	Importer types.Importer
	// GoVersion is passed unchanged to types.Config.GoVersion. Empty preserves
	// the checker default; a nonempty value selects Go language semantics, not
	// an SDK executable. Invalid or newer versions are rejected by the checker.
	GoVersion string
	// FakeImportC permits import "C" while type checking, matching
	// types.Config.FakeImportC. It does not provide cgo declarations or make
	// cgo source executable by the Bash++ runtime.
	FakeImportC bool
	// TestBuiltins mirrors types.DefPredeclaredTestFuncs, enabling the assert and
	// trace test builtins of the go/types testing environment. It is off by
	// default; this is a checker-environment option, not a language feature.
	TestBuiltins bool
	// CheckerBranchErrors leaves label, goto, break, and continue diagnostics
	// to go/types instead of reporting them during the gc syntax verdict.
	CheckerBranchErrors bool
	// CheckAfterSyntaxErrors continues with go/parser recovery and go/types
	// after a non-empty gc syntax verdict, retaining only gc's syntax errors
	// before the checker's diagnostics.
	CheckAfterSyntaxErrors bool
	// GoTypesParserDiagnostics reports parser diagnostics at go/parser's
	// positions. It is a runner fact for the upstream go/types check_test
	// harness: that runner collects its parser's errors before it calls the
	// checker, whereas the types2 runner collects gc's parser errors. It is
	// deliberately opt-in so ordinary checks retain the gc syntax verdict.
	GoTypesParserDiagnostics bool
	// Packages are explicitly supplied dependency packages, type-checked in
	// the given order before the program and registered under their Path.
	// They are the policy-free half of Go's import model — an in-memory
	// `-importcfg` — and are consulted before Importer for every import.
	Packages []PackageSpec
	// ImportBase is the compiler's `-D`: a relative import "./x" in any file
	// means ImportBase/x. Empty refuses relative imports. It is never
	// resolved against the filesystem.
	ImportBase string
	// ImportPath is the program package's own import path, used to attribute
	// its resolutions. Empty uses the package name, as before.
	//
	// It is also the program's DECLARED identity for internal-package
	// visibility (the compiler's -p): with it set, every import of the
	// program is admitted or refused by cmd/go's rule on identities
	// (syntax.BashPPInternalImportVisible) — a mapped package always has one,
	// its Path — before the map or Importer is consulted. Without it the
	// program's imports go through Importer's own rule, unchanged.
	ImportPath string
	// TestMain asserts that the program is cmd/go's generated test main,
	// which cmd/go alone may import testing/internal/…. It is a fact the
	// caller passes in from the site that knows it is running the testmain;
	// it is never inferred from a ".test" suffix, and it requires ImportPath.
	TestMain bool
}
type Program struct {
	File          *syntax.File
	Package       string
	Sources       []SourceInfo
	InitFunctions []string
	Main          string
	// Resolutions records every import the type checker resolved, for the
	// explicit packages and the program alike, in resolution order.
	Resolutions []Resolution
	// Importer is the importer the program was checked with: the explicit
	// package map in front of Options.Importer. Lowering the same program
	// should type-check against it (lower.Options.Importer) so both halves
	// see one map.
	Importer types.Importer
	// Packages lists the explicit packages linked into File, in map order.
	// Each was lowered from its exact sources ahead of the program, so the
	// interpreter runs it without any on-disk lookup; its InitFunctions are
	// included in InitFunctions ahead of the program's own.
	Packages []LinkedPackage
}

// LinkedPackage is one explicit package lowered into Program.File.
type LinkedPackage struct {
	Path           string
	Name           string
	Files          []string
	SourceDir      string
	CompanionFiles []string
}

// SourceAt maps an AST offset to the original source identity and byte offset.
func (p *Program) SourceAt(pos syntax.Pos) (string, uint, bool) {
	for _, s := range p.Sources {
		if pos.Offset() >= s.Base && pos.Offset() <= s.Base+s.Size {
			return s.Name, pos.Offset() - s.Base, true
		}
	}
	return "", 0, false
}
func Parse(r io.Reader, name string, options Options) (*Program, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Load([]Source{{name, data}}, options)
}

// Load processes a single package in lexical filename order, matching the Go
// toolchain. Source bytes are neither modified nor executed by the native toolchain.
func Load(sources []Source, options Options) (*Program, error) {
	if options.PreserveNativeInit && options.RunMain {
		return nil, fmt.Errorf("gosource: native init preservation requires a non-executing package")
	}
	if options.TestMain && options.ImportPath == "" {
		return nil, fmt.Errorf("gosource: TestMain asserts the identity of the program and requires ImportPath")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("gosource: no source files")
	}
	sources = append([]Source(nil), sources...)
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	prepared, err := openPreparedProgramCache(sources, options)
	if err != nil {
		return nil, err
	}
	if prepared != nil {
		defer prepared.close()
		if program := prepared.program(); program != nil {
			return program, nil
		}
	}
	// gc's own parser is the syntax verdict. A "syntax error" is the complete
	// result, as it is for gc; after any other parser or scanner diagnostic
	// gc still type-checks, and so does Load, on go/parser's recovered AST
	// while retaining gc's diagnostics instead of go/parser's. Checker-test
	// policy may continue the same way after a syntax error too.
	checker, err := checkerOptionsFor(sources, options)
	if err != nil {
		return nil, err
	}
	syntaxErrors, gcFiles := syntaxVerdict(sources, checker)
	if options.GoTypesParserDiagnostics {
		syntaxErrors = goParserSyntaxVerdict(sources)
	}
	if len(syntaxErrors) > 0 && !options.CheckAfterSyntaxErrors && !checksAfterSyntaxVerdict(syntaxErrors) {
		return nil, syntaxErrors
	}
	importsC := sourcesImportC(sources)
	for _, spec := range options.Packages {
		importsC = importsC || sourcesImportC(spec.Sources)
	}
	if importsC && !checker.fakeImportC {
		if !automaticCgoEnabled() {
			return nil, fmt.Errorf("gosource: cgo disabled: import %q requires CGO_ENABLED=1 (go env CGO_ENABLED)", "C")
		}
		checker.fakeImportC = true
	}
	c := &converter{fset: token.NewFileSet(), info: newTypeInfo(), renames: map[types.Object]string{}}
	p := &Program{File: &syntax.File{Name: sources[0].Name, GoSource: true}}
	parseErrors := append(ErrorList(nil), syntaxErrors...)
	var rangeClauses []rangeArity
	for i, s := range sources {
		if i > 0 && s.Name == sources[i-1].Name {
			return nil, fmt.Errorf("gosource: duplicate file %q", s.Name)
		}
		data := s.Data
		if len(syntaxErrors) == 0 && !options.GoTypesParserDiagnostics && i < len(gcFiles) {
			// gc accepts a range clause with extra iteration variables and
			// its checker diagnoses them (range_arity.go).
			var clauses []rangeArity
			data, clauses = rangeArityImage(s.Name, s.Data, gcFiles[i])
			rangeClauses = append(rangeClauses, clauses...)
		}
		f, err := parseGoFile(c.fset, s.Name, data, parser.ParseComments|parser.AllErrors)
		if err != nil && len(syntaxErrors) == 0 {
			parseErrors = appendDiagnostics(parseErrors, err)
		}
		// A recovered file still contains declarations and bodies for the Go
		// checker. Keep it for diagnostics only; no errored AST is converted.
		if f == nil {
			continue
		}
		// The gc policy checks gc's recovered tree. The go/types test
		// runner checks go/parser's tree, including its recovery nodes.
		if len(syntaxErrors) > 0 && !options.GoTypesParserDiagnostics {
			mirrorGCTree(c.fset, f, gcFiles[i])
		}
		if p.Package == "" {
			p.Package = f.Name.Name
		}
		tf := c.fset.File(f.FileStart)
		p.Sources = append(p.Sources, SourceInfo{Name: s.Name, SHA256: fmt.Sprintf("%x", sha256.Sum256(s.Data)), Base: uint(tf.Base() - 1), Size: uint(len(s.Data)), Package: f.Name.Name, LineDirectives: lineDirectives(tf, f)})
		c.files = append(c.files, f)
		c.sources = append(c.sources, s)
	}
	if len(c.files) == 0 {
		return nil, parseErrors
	}
	c.checkerNames = prepareCgoFiles(c.fset, c.files)
	if len(syntaxErrors) > 0 && !options.GoTypesParserDiagnostics {
		parseErrors = appendStructuralCheckerDiagnostics(parseErrors, gcFiles)
	}
	if len(syntaxErrors) == 0 {
		parseErrors = append(parseErrors, validateCompilerDirectives(c.fset, c.files, checker)...)
	}
	fallback := options.Importer
	if fallback == nil {
		fallback = defaultSourceImporter{Importer: importer.Default()}
	}
	imp := newMapImporter(options.ImportBase, fallback)
	imp.fset, imp.checker = c.fset, checker
	imp.link = !options.PreserveNativeInit
	// Explicit packages are checked first, in order, so a later package sees
	// every earlier one; a failing package stops here with its diagnostics
	// and the program is never checked against a partial map.
	for _, spec := range options.Packages {
		if diagnostics := imp.checkDependency(c.fset, spec, checker); len(diagnostics) > 0 {
			return nil, append(parseErrors, diagnostics...)
		}
	}
	programPath := options.ImportPath
	if programPath == "" {
		programPath = p.Package
	}
	imp.from = programPath
	imp.identity = importerIdentity{declared: options.ImportPath != "", testMain: options.TestMain}
	// An external test package (`package x_test`) declared under the tested
	// package's identity is checked as cmd/go checks pxtest: its own path
	// is <pkg>_test, so a type of the tested package it imports stays
	// qualified (`types.Sym`, not a same-package `Sym`), while its import
	// visibility is still decided under the tested package's identity, as
	// cmd/go decides it with the tested package as the importer.
	checkPath := programPath
	if options.ImportPath != "" && !options.TestMain && strings.HasSuffix(p.Package, "_test") && !strings.HasSuffix(options.ImportPath, "_test") {
		checkPath = options.ImportPath + "_test"
	}
	typeErrors := newCheckerDiagnostics(c.fset, c.files, c.info, checker)
	config := checker.config(imp, typeErrors.report)
	pkg, err := config.Check(checkPath, c.fset, c.files, c.info)
	// gc's stderr is sorted by position; the checker-test flow instead lists
	// parser diagnostics first, followed by semantic diagnostics from every
	// recoverable file.
	diagnostics := append(parseErrors, dropRecoveredCallUnused(typeErrors.result(err), c.fset, c.info, gcFiles)...)
	diagnostics = append(diagnostics, rangeArityDiagnostics(c.fset, c.files, c.info, rangeClauses)...)
	if len(diagnostics) > 0 {
		if checker.gcStderr() {
			diagnostics = sortGCStderr(c.fset, sources, diagnostics)
			diagnostics = limitGCStderr(c.fset, diagnostics, checker.errorLimit)
		}
		return nil, diagnostics
	}
	p.Resolutions = imp.resolutions
	p.Importer = imp
	c.shadowedBuiltins = shadowedBuiltinTypes(pkg)
	if main, ok := pkg.Scope().Lookup("main").(*types.Func); ok && p.Package == "main" {
		p.Main = main.Name()
	}
	if options.RunMain && p.Main == "" {
		return nil, fmt.Errorf("%s: Go execution requires package main with func main()", sources[0].Name)
	}
	// The link set is every explicit package, in map order, then the
	// program. All are lowered into one flat file, so the hygiene prefix
	// must be free in every file of the set; a mapped package's
	// package-level names are then renamed under it, which keeps the flat
	// namespace collision-free without touching the program's own names.
	//
	// A native unit (PreserveNativeInit) links nothing: the map served the
	// checker, and the program's imports of it stay imports, so the link
	// set is the program alone and no name is renamed.
	var linked []*converter
	mapped := map[string]int{}
	var mappedPkgs []*types.Package
	var linkedPaths []string
	if !options.PreserveNativeInit {
		linkedPaths = imp.order
	}
	for i, path := range linkedPaths {
		checked := imp.checked[path]
		mapped[path] = i
		mappedPkgs = append(mappedPkgs, checked.pkg)
		linked = append(linked, &converter{packagePath: path, fset: c.fset, files: checked.files, sources: checked.sources, info: checked.info, renames: c.renames, checkerNames: checked.checkerNames, shadowedBuiltins: shadowedBuiltinTypes(checked.pkg)})
	}
	// The converter's own package is the one the checker built: for an
	// external test package that is <pkg>_test, so the tested package's
	// types it imports keep their qualifier (types.Sym) and its own stay
	// bare.
	c.packagePath = checkPath
	linked = append(linked, c)
	c.prefix = "__gosource_"
	for {
		collision := false
		for _, lc := range linked {
			for _, f := range lc.files {
				ast.Inspect(f, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && strings.HasPrefix(id.Name, c.prefix) {
						collision = true
					}
					return true
				})
			}
		}
		if !collision {
			break
		}
		c.prefix += "_"
	}
	for _, lc := range linked {
		lc.prefix = c.prefix
		lc.mapped = mapped
		lc.mappedPkgs = mappedPkgs
	}
	mangleLinkedNames(linked, mappedPkgs)
	// Import aliases are shared: every package's imports are hoisted into
	// the one file, so an alias minted by any package names that path for all.
	importAliases := map[string]string{}
	syntheticImports := map[string]syntheticImport{}
	c.dotImports = map[*ast.File]map[string]bool{}
	liveImportPaths := liveImports(linked, mapped)
	var lowered []*loweredPackage
	for pi, lc := range linked {
		lc.importAliases = importAliases
		lc.syntheticImports = syntheticImports
		lc.dotImports = map[*ast.File]map[string]bool{}
		lc.resolveImport = imp.resolve
		lc.perFileImports = options.PreserveNativeInit
		if lc != c {
			if err := refuseEmbedDirectives(lc); err != nil {
				return nil, err
			}
		}
		for id, obj := range lc.info.Defs {
			if obj != nil && (id.Name == "nil" || id.Name == "true" || id.Name == "false") {
				lc.renames[obj] = fmt.Sprintf("%sbinding_%d", c.prefix, id.Pos())
			}
		}
		// Only live package bindings need an alias. Blank imports and package
		// bindings with no selector use must not create unused alias imports
		// in the lowered file. The program's own imports keep the file's
		// spelling (C8): the checker already forbids a package-level name in
		// a file block, so in the flat file the only collision left is a
		// name the program's files bind to two different paths, and only the
		// later binding takes an alias. A linked package's imports are
		// hoisted into the same file and always take one.
		bound := map[string]string{}
		for fi, f := range lc.files {
			for ii, spec := range f.Imports {
				var obj types.Object
				if spec.Name != nil {
					obj = lc.info.Defs[spec.Name]
				} else {
					obj = lc.info.Implicits[spec]
				}
				if obj != nil && obj.Name() == "." {
					// Native units retain dot imports unless their exports collide
					// across files: lower still validates a combined package tree
					// before emitting separate files. Flattened execution also
					// qualifies linked packages and checked native type identities.
					if pkgname, ok := obj.(*types.PkgName); ok {
						path := pkgname.Imported().Path()
						if liveImportPaths[path] &&
							(dotImportCollides(lc, f, spec, pkgname, liveImportPaths, mapped) ||
								!options.PreserveNativeInit && (lc != c || dotImportNeedsQualifiedTypes(lc, path))) {
							alias := fmt.Sprintf("%simport_%d_%d", c.prefix, fi, ii)
							if lc != c {
								alias = fmt.Sprintf("%simport_%d_%d_%d", c.prefix, pi, fi, ii)
							}
							lc.renames[obj] = alias
							importAliases[path] = alias
							for _, name := range pkgname.Imported().Scope().Names() {
								if member := pkgname.Imported().Scope().Lookup(name); member != nil {
									lc.renames[member] = alias + "." + name
								}
							}
						} else {
							if lc.dotImports[f] == nil {
								lc.dotImports[f] = map[string]bool{}
							}
							lc.dotImports[f][path] = true
						}
					}
				}
				if obj != nil && obj.Name() != "_" && obj.Name() != "." {
					if pkgname, ok := obj.(*types.PkgName); ok {
						path := pkgname.Imported().Path()
						if liveImportPaths[path] {
							if lc != c {
								lc.renames[obj] = fmt.Sprintf("%simport_%d_%d_%d", c.prefix, pi, fi, ii)
							} else if prev, ok := bound[obj.Name()]; ok && prev != path {
								lc.renames[obj] = fmt.Sprintf("%simport_%d_%d", c.prefix, fi, ii)
							} else {
								bound[obj.Name()] = path
							}
						}
						if _, linked := mapped[path]; !linked {
							alias := lc.renames[obj]
							if alias == "" {
								alias = obj.Name()
							}
							importAliases[path] = alias
						}
					}
				}
			}
		}
		// A plain non-executing package is a native library input: keep its
		// init declarations for cmd/go to schedule. Flattened execution and
		// explicit package maps still need uniquely named callable init funcs.
		lp, err := lc.lowerPackage(len(p.InitFunctions), options.PreserveNativeInit)
		if err != nil {
			return nil, err
		}
		p.InitFunctions = append(p.InitFunctions, lp.initFunctions...)
		lowered = append(lowered, lp)
	}
	p.File.CgoPackages = cgoPackages(linked)
	// Imports first: the interpreter starts the native dependency bridge once,
	// at the first statement that is not an import.
	if len(syntheticImports) > 0 {
		paths := make([]string, 0, len(syntheticImports))
		for path := range syntheticImports {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			imp := syntheticImports[path]
			positions := make([]token.Pos, 0, len(imp.sources))
			for pos := range imp.sources {
				positions = append(positions, pos)
			}
			sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
			for _, pos := range positions {
				p.File.Stmts = append(p.File.Stmts, c.stmt(&syntax.BashPPImport{
					Site: syntax.StartImport, Class: syntax.ClassR, Kw: c.lit(pos, "import"),
					Alias: c.lit(pos, imp.alias),
					Path:  &syntax.DblQuoted{Left: c.pos(pos), Right: c.pos(pos), Parts: []syntax.WordPart{c.lit(pos, path)}},
				}))
			}
		}
	}
	for _, lp := range lowered {
		p.File.Stmts = append(p.File.Stmts, lp.imports...)
	}
	// Then each package in link order, a dependency completely before its
	// importer: function definitions precede all initializers to support
	// forward references, and a mapped package's init calls run before the
	// next package's initializers, as Go orders them.
	for i, lp := range lowered {
		p.File.Stmts = append(p.File.Stmts, lp.decls...)
		p.File.Stmts = append(p.File.Stmts, lp.funcs...)
		p.File.Stmts = append(p.File.Stmts, lp.inits...)
		if !options.RunMain {
			continue
		}
		calls := lp.initFunctions
		if i == len(lowered)-1 {
			calls = append(append([]string(nil), calls...), p.Main)
		}
		for _, name := range calls {
			// These init and main entry calls are synthetic glue with no line
			// in any source file. Borrowing the first declaration's position
			// (p.File.Pos()) stamped a //line on them and mis-attributed -m
			// diagnostics to that unrelated line; leave them unpositioned so
			// the emitter never points a diagnostic back at borrowed source.
			lit := &syntax.Lit{Value: name}
			p.File.Stmts = append(p.File.Stmts, c.stmt(&syntax.BashPPCall{Fun: []*syntax.Lit{lit}}))
		}
	}
	if err := checkLoweredNames(p.File, options.PreserveNativeInit); err != nil {
		return nil, err
	}
	for _, path := range linkedPaths {
		checked := imp.checked[path]
		p.Packages = append(p.Packages, LinkedPackage{Path: path, Name: checked.pkg.Name(), Files: imp.files[path], SourceDir: checked.spec.SourceDir, CompanionFiles: append([]string(nil), checked.spec.CompanionFiles...)})
		// A checked package parsed every source, so files and sources align.
		for i, src := range checked.sources {
			tf := c.fset.File(checked.files[i].FileStart)
			p.Sources = append(p.Sources, SourceInfo{Name: src.Name, SHA256: fmt.Sprintf("%x", sha256.Sum256(src.Data)), Base: uint(tf.Base() - 1), Size: uint(len(src.Data)), Package: checked.pkg.Name(), PackagePath: path, LineDirectives: lineDirectives(tf, checked.files[i])})
		}
	}
	for i, path := range linkedPaths {
		checked := imp.checked[path]
		if len(checked.spec.CompanionFiles) == 0 {
			continue
		}
		companion := syntax.GoPackageCompanion{
			Path: path, Name: checked.pkg.Name(), SourceDir: checked.spec.SourceDir,
			SourceFiles: append([]string(nil), imp.files[path]...),
			Files:       append([]string(nil), checked.spec.CompanionFiles...),
		}
		companion.Hook = "BashPPGoSourceCompanionValues"
		for checked.pkg.Scope().Lookup(companion.Hook) != nil {
			companion.Hook += "_"
		}
		for _, file := range checked.files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body != nil || fn.Name == nil {
					continue
				}
				obj := checked.info.Defs[fn.Name]
				runtimeName := c.renames[obj]
				if runtimeName == "" {
					runtimeName = c.mangledName(i, fn.Name.Name)
				}
				companion.Symbols = append(companion.Symbols, syntax.GoPackageCompanionSymbol{Name: fn.Name.Name, RuntimeName: runtimeName})
			}
		}
		sort.Slice(companion.Symbols, func(i, j int) bool { return companion.Symbols[i].Name < companion.Symbols[j].Name })
		p.File.GoPackageCompanions = append(p.File.GoPackageCompanions, companion)
	}
	c.attachEmbedDirectives(p.File, linked)
	p.File.Sources = append([]syntax.SourceFile(nil), p.Sources...)
	c.attachFloatingDirectives(p.File)
	if prepared != nil {
		if err := prepared.store(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// attachFloatingDirectives keeps the file-level compiler directives that
// address no declaration. A //go:linkname or //go:cgo_* line is not bound
// to the declaration below it: gc reads it wherever it stands in the file,
// after the last declaration or above a type or const, and checks every one
// (an undeclared target, a duplicate, an instantiation) whether or not a
// declaration follows. attachEmbedDirectives carries only the directives
// documenting a func or var, so the rest are attached here to the last
// named declaration of the same source file — each with its own position,
// which the emitter spells as its own line directive — so the generated
// unit still carries them for gc to read and refuse.
func (c *converter) attachFloatingDirectives(file *syntax.File) {
	attached := map[uint]bool{}
	last := map[string]*syntax.Stmt{}
	for _, stmt := range file.Stmts {
		for _, comment := range stmt.Comments {
			attached[comment.Hash.Offset()] = true
		}
		switch stmt.Cmd.(type) {
		case *syntax.BashPPDecl, *syntax.BashPPFuncDecl:
			if source, ok := file.SourceAt(stmt.Pos()); ok {
				last[source.Name] = stmt
			}
		}
	}
	for _, f := range c.files {
		for _, group := range f.Comments {
			for _, comment := range group.List {
				if !isFileDirective(comment.Text) {
					continue
				}
				pos := c.pos(comment.Slash)
				if attached[pos.Offset()] {
					continue
				}
				source, ok := file.SourceAt(pos)
				if !ok {
					continue
				}
				if stmt := last[source.Name]; stmt != nil {
					stmt.Comments = append(stmt.Comments, syntax.Comment{Hash: pos, Text: strings.TrimPrefix(comment.Text, "//")})
				}
			}
		}
	}
}

// isFileDirective reports whether a comment line is a compiler directive gc
// applies to the file rather than to the declaration it documents.
func isFileDirective(text string) bool {
	if !strings.HasPrefix(text, "//go:") {
		return false
	}
	directive, _, _ := strings.Cut(strings.TrimPrefix(text, "//go:"), " ")
	directive, _, _ = strings.Cut(directive, "\t")
	return directive == "linkname" || strings.HasPrefix(directive, "cgo_")
}

func liveImports(linked []*converter, mapped map[string]int) map[string]bool {
	live := map[*types.PkgName]bool{}
	for _, lc := range linked {
		for _, obj := range lc.info.Uses {
			if pkgname, ok := obj.(*types.PkgName); ok {
				live[pkgname] = true
			}
		}
	}
	paths := map[string]bool{}
	for _, lc := range linked {
		for _, f := range lc.files {
			for _, spec := range f.Imports {
				var obj types.Object
				if spec.Name != nil {
					obj = lc.info.Defs[spec.Name]
				} else {
					obj = lc.info.Implicits[spec]
				}
				pkgname, ok := obj.(*types.PkgName)
				if !ok {
					continue
				}
				if !live[pkgname] && obj.Name() == "." {
					for _, used := range lc.info.Uses {
						if used != nil && used.Pkg() != nil && used.Pkg().Path() == pkgname.Imported().Path() {
							live[pkgname] = true
							break
						}
					}
				}
				if !live[pkgname] {
					continue
				}
				path := pkgname.Imported().Path()
				if _, linked := mapped[path]; linked {
					continue
				}
				paths[path] = true
			}
		}
	}
	return paths
}

// A bare imported named type loses its origin in the interpreter's flat type
// namespace. Keep an explicit package binding whenever a checked expression's
// type mentions this package, including inferred results and named constants.
// Imports used only for predeclared-typed functions need no such rewrite.
func dotImportNeedsQualifiedTypes(c *converter, path string) bool {
	for _, tv := range c.info.Types {
		found := false
		types.TypeString(tv.Type, func(pkg *types.Package) string {
			if pkg.Path() == path {
				found = true
			}
			return pkg.Name()
		})
		if found {
			return true
		}
	}
	return false
}

func dotImportCollides(lc *converter, file *ast.File, spec *ast.ImportSpec, pkgname *types.PkgName, live map[string]bool, mapped map[string]int) bool {
	path := pkgname.Imported().Path()
	scope := pkgname.Imported().Scope()
	if scope == nil {
		return false
	}
	for _, otherFile := range lc.files {
		for _, otherSpec := range otherFile.Imports {
			if otherFile == file && otherSpec == spec {
				continue
			}
			var obj types.Object
			if otherSpec.Name != nil {
				obj = lc.info.Defs[otherSpec.Name]
			} else {
				obj = lc.info.Implicits[otherSpec]
			}
			otherPkg, ok := obj.(*types.PkgName)
			if !ok || obj.Name() != "." {
				continue
			}
			otherPath := otherPkg.Imported().Path()
			if otherPath == path || !live[otherPath] {
				continue
			}
			if _, linked := mapped[otherPath]; linked {
				continue
			}
			otherScope := otherPkg.Imported().Scope()
			if otherScope == nil {
				continue
			}
			for _, name := range scope.Names() {
				if otherScope.Lookup(name) != nil {
					return true
				}
			}
		}
	}
	return false
}

// shadowedBuiltinTypes reports the predeclared type names a package redeclares
// at package scope as something other than a type — `const int = 15`,
// `var error = …`, `func string() {}`. Within such a package the name no
// longer spells the universe type, so the converter must not synthesize a
// `<name>(x)` conversion around a materialized constant.
func shadowedBuiltinTypes(pkg *types.Package) map[string]bool {
	if pkg == nil {
		return nil
	}
	var out map[string]bool
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if _, ok := types.Universe.Lookup(name).(*types.TypeName); !ok {
			continue
		}
		if _, ok := scope.Lookup(name).(*types.TypeName); ok {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[name] = true
	}
	return out
}

// checkerOptions is the complete policy passed to every types.Config in one
// Load. Keeping it as a value applies the same settings to the program and
// explicit packages. Test builtins are the exception to per-Load isolation:
// the go/types API installs them process-wide, irreversibly.
type checkerOptions struct {
	goVersion                string
	fakeImportC              bool
	testBuiltins             bool
	checkerBranchErrors      bool
	checkAfterSyntaxErrors   bool
	goTypesParserDiagnostics bool
	errorLimit               int
}

// gcStderr reports whether the diagnostics are gc's stderr for the sources:
// sorted by position and passed through gc's per-line filter. The
// checker-test policy (CheckAfterSyntaxErrors) instead expects the parser's
// rows first, every one of them, then go/types' complete output. Both
// policies check gc's tree (mirrorGCTree).
func (o checkerOptions) gcStderr() bool {
	return !o.checkAfterSyntaxErrors
}

var definePredeclaredTestFuncs sync.Once

func checkerOptionsFor(sources []Source, options Options) (checkerOptions, error) {
	out := checkerOptions{
		goVersion:                options.GoVersion,
		fakeImportC:              options.FakeImportC,
		testBuiltins:             options.TestBuiltins,
		checkerBranchErrors:      options.CheckerBranchErrors,
		checkAfterSyntaxErrors:   options.CheckAfterSyntaxErrors,
		goTypesParserDiagnostics: options.GoTypesParserDiagnostics,
	}
	// The Go checker corpus places flag-compatible configuration on the first
	// source line (for example "// -lang=go1.13"). Testdir errorcheck recipes
	// use the same flag later on that line. Honor only checker flags, only from
	// the first source, and let explicit API options win.
	line, _, _ := strings.Cut(string(sources[0].Data), "\n")
	if strings.HasPrefix(strings.TrimSpace(line), "//") {
		for _, field := range strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))) {
			if out.goVersion == "" && strings.HasPrefix(field, "-lang=") {
				out.goVersion = strings.TrimPrefix(field, "-lang=")
			}
			if field == "-fakeImportC" && !options.FakeImportC {
				out.fakeImportC = true
			}
			if out.errorLimit == 0 && field == "-e=0" {
				out.errorLimit = 10
			}
		}
	}
	if out.goVersion != "" && !version.IsValid(out.goVersion) {
		return checkerOptions{}, fmt.Errorf("gosource: invalid Go version %q", out.goVersion)
	}
	return out, nil
}

func (o checkerOptions) config(imp types.Importer, report func(error)) types.Config {
	if o.testBuiltins {
		definePredeclaredTestFuncs.Do(types.DefPredeclaredTestFuncs)
	}
	return types.Config{
		Importer:    imp,
		GoVersion:   o.goVersion,
		FakeImportC: o.fakeImportC,
		Error:       report,
	}
}

// validateCompilerDirectives covers source checks that the gc compiler runs
// around go/types. The public checker deliberately ignores pragmas, but a
// semantic-only Go source check must not accept a directive that compilation
// will reject.
func validateCompilerDirectives(fset *token.FileSet, files []*ast.File, checker checkerOptions) ErrorList {
	var diagnostics ErrorList
	for _, file := range files {
		haveEmbed := false
		for _, spec := range file.Imports {
			if path, err := strconv.Unquote(spec.Path.Value); err == nil && path == "embed" {
				haveEmbed = true
			}
		}

		var funcs []*ast.FuncDecl
		embedDecl := map[*ast.Comment]*ast.ValueSpec{}
		insideFunc := map[*ast.Comment]bool{}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				funcs = append(funcs, fn)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			gd, ok := node.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, raw := range gd.Specs {
				value := raw.(*ast.ValueSpec)
				doc := value.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				if doc == nil {
					continue
				}
				for _, comment := range doc.List {
					embedDecl[comment] = value
					for _, fn := range funcs {
						if fn.Body != nil && fn.Body.Pos() < comment.Slash && comment.End() < fn.Body.End() {
							insideFunc[comment] = true
						}
					}
				}
			}
			return true
		})

		for _, group := range file.Comments {
			for _, comment := range group.List {
				text := strings.TrimPrefix(comment.Text, "//")
				switch {
				case strings.HasPrefix(text, "go:build"):
					if comment.Slash > file.Package {
						diagnostics = append(diagnostics, fmt.Errorf("%s: misplaced compiler directive", fset.Position(comment.Slash)))
					}
				case strings.HasPrefix(text, "go:noinline"):
					allowed := false
					for _, fn := range funcs {
						if fn.Body != nil && fn.Body.Pos() < comment.Slash && comment.End() < fn.Body.End() {
							allowed = false
							break
						}
					}
					if comment.Slash > file.Package && fset.Position(comment.Slash).Column == 1 {
						for _, decl := range file.Decls {
							if comment.Slash < decl.Pos() {
								_, allowed = decl.(*ast.FuncDecl)
								break
							}
						}
					}
					if !allowed {
						diagnostics = append(diagnostics, fmt.Errorf("%s: misplaced compiler directive", fset.Position(comment.Slash)))
					}
				case text == "go:embed" || strings.HasPrefix(text, "go:embed ") || strings.HasPrefix(text, "go:embed\t"):
					value := embedDecl[comment]
					msg := ""
					switch {
					case value == nil:
						msg = "misplaced go:embed directive"
					case !haveEmbed:
						msg = `go:embed only allowed in Go files that import "embed"`
					case len(value.Names) != 1:
						msg = "go:embed cannot apply to multiple vars"
					case len(value.Values) != 0:
						msg = "go:embed cannot apply to var with initializer"
					case value.Type == nil:
						msg = "go:embed cannot apply to var without type"
					case insideFunc[comment]:
						msg = "go:embed cannot apply to var inside func"
					case checker.goVersion != "" && version.Compare(checker.goVersion, "go1.16") < 0:
						msg = fmt.Sprintf("go:embed requires go1.16 or later (-lang was set to %s; check go.mod)", checker.goVersion)
					}
					if msg != "" {
						diagnostics = append(diagnostics, fmt.Errorf("%s: %s", fset.Position(comment.Slash), msg))
					}
				}
			}
		}
	}
	return diagnostics
}

// loweredPackage is one package's lowered top-level statements, grouped so
// Load can hoist every package's imports ahead of the first declaration and
// emit the rest per package in link order.
type loweredPackage struct {
	imports, decls, funcs []*syntax.Stmt
	// inits holds the zero-initialized globals followed by the checker's
	// InitOrder initializers.
	inits         []*syntax.Stmt
	initFunctions []string
}

// lowerPackage converts the converter's files into grouped statements. The
// init functions are numbered from initBase so names stay unique across the
// link set.
func (c *converter) lowerPackage(initBase int, preserveNativeInit bool) (*loweredPackage, error) {
	out := &loweredPackage{}
	vars := map[*types.Var]*syntax.BashPPDecl{}
	tupleSpecs := map[*types.Var]*ast.ValueSpec{}
	tupleDecls := map[*ast.ValueSpec]*ast.GenDecl{}
	for _, f := range c.files {
		c.currentFile = f
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				fn := c.function(fd)
				if fd.Name.Name == "init" && fd.Recv == nil && !preserveNativeInit {
					fn.Name.Value = fmt.Sprintf("%sinit_%d", c.prefix, initBase+len(out.initFunctions))
					out.initFunctions = append(out.initFunctions, fn.Name.Value)
				}
				out.funcs = append(out.funcs, c.stmt(fn))
				continue
			}
			gd := d.(*ast.GenDecl)
			// A parenthesised const group keeps its grouping through the
			// group carrier, and a const whose initializer selects through
			// an imported package (unsafe.Sizeof) keeps its source form so
			// the generated Go keeps the import. A single const stays on the
			// per-spec path, whose folded value the interpreter's exact
			// evaluator accepts (a constant call such as complex is not a
			// scalar expression to it).
			if gd.Tok == token.CONST && (gd.Lparen.IsValid() || c.constGroupUsesImports(gd)) {
				out.decls = append(out.decls, c.stmt(c.constGroup(gd)))
				continue
			}
			for _, s := range gd.Specs {
				switch v := s.(type) {
				case *ast.ImportSpec:
					// An import satisfied by the explicit package map is
					// linked, not imported: nothing reaches the runtime.
					if imp := c.importSpec(gd, v); imp != nil {
						out.imports = append(out.imports, c.stmt(imp))
					}
				case *ast.TypeSpec:
					out.decls = append(out.decls, c.stmt(c.typeDecl(gd, v)))
				case *ast.ValueSpec:
					valueSpec := v
					if gd.Tok == token.VAR && len(v.Values) == 1 && len(v.Names) > 1 {
						zero := *v
						zero.Values = nil
						valueSpec = &zero
						tupleDecls[v] = gd
						for _, n := range v.Names {
							tupleSpecs[c.info.Defs[n].(*types.Var)] = v
						}
					}
					for i, n := range v.Names {
						node := c.valueDecl(gd, valueSpec, n, i)
						if gd.Tok == token.VAR {
							vars[c.info.Defs[n].(*types.Var)] = node
						} else {
							out.decls = append(out.decls, c.stmt(node))
						}
					}
				}
			}
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	initialized := map[*types.Var]bool{}
	// Zero-initialized globals are available to initializer functions.
	for _, f := range c.files {
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, s := range gd.Specs {
					v := s.(*ast.ValueSpec)
					if len(v.Values) == 0 {
						for _, n := range v.Names {
							obj := c.info.Defs[n].(*types.Var)
							out.inits = append(out.inits, c.stmt(vars[obj]))
							initialized[obj] = true
						}
					}
				}
			}
		}
	}
	for _, init := range c.info.InitOrder {
		if len(init.Lhs) > 1 {
			spec := tupleSpecs[init.Lhs[0]]
			if spec == nil {
				return nil, fmt.Errorf("%s: gosource: missing tuple initializer", c.fset.Position(init.Rhs.Pos()))
			}
			out.inits = append(out.inits, c.tupleValueDecls(tupleDecls[spec], spec)...)
			for _, variable := range init.Lhs {
				initialized[variable] = true
			}
			continue
		}
		v := init.Lhs[0]
		out.inits = append(out.inits, c.stmt(vars[v]))
		initialized[v] = true
	}
	for v := range vars {
		if !initialized[v] {
			return nil, fmt.Errorf("gosource: missing initialization of %s", v.Name())
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return out, nil
}

// mangleLinkedNames renames every package-level object of every mapped
// package — types, functions, variables and constants, exported or not — to
// its flat-file spelling, so two linked packages may declare one name and
// the program keeps its own names untouched. Methods are not renamed: they
// dispatch by receiver type, and the receiver's type name is. An embedded
// field is selected by its type's unqualified name, which the runtime
// derives from the field's type spelling, so an embedded field of a renamed
// type takes the same rename; the converter spells the field's uses
// through the field object, and the two agree.
func mangleLinkedNames(linked []*converter, mappedPkgs []*types.Package) {
	if len(mappedPkgs) == 0 {
		return
	}
	// renames is one map shared by every converter of the link set.
	c := linked[0]
	for i, pkg := range mappedPkgs {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			c.renames[scope.Lookup(name)] = c.mangledName(i, name)
		}
	}
	for _, lc := range linked {
		for _, obj := range lc.info.Defs {
			field, ok := obj.(*types.Var)
			if !ok || !field.Embedded() {
				continue
			}
			typ := field.Type()
			if pointer, ok := typ.(*types.Pointer); ok {
				typ = pointer.Elem()
			}
			var declaration *types.TypeName
			switch typ := typ.(type) {
			case *types.Named:
				declaration = typ.Obj()
			case *types.Alias:
				// An embedded alias retains its own field name even though its
				// type is identical to its target. Match the alias declaration,
				// not the unaliased target or an unrelated same-spelled name.
				declaration = typ.Obj()
			}
			if rename := c.renames[declaration]; rename != "" {
				c.renames[field] = rename
			}
		}
	}
}

// checkLoweredNames is the guard behind mangleLinkedNames: the lowered file
// is one flat namespace, so every top-level declaration must have a distinct
// name (a method is keyed by receiver type as well). The checker already
// rejects a package redeclaring a name across its own files and the renames
// keep packages apart, so a duplicate here is a converter defect, reported
// rather than left to alias silently at runtime.
func checkLoweredNames(file *syntax.File, allowNativeInit bool) error {
	declared := map[string]bool{}
	for _, stmt := range file.Stmts {
		var name string
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPFuncDecl:
			name = d.Name.Value
			if d.Receiver != nil && d.Receiver.RecvType != nil {
				name = d.Receiver.RecvType.Value + "." + name
			}
		case *syntax.BashPPDecl:
			name = d.Name.Value
		default:
			continue
		}
		// A blank function or method is not declared (spec: Blank
		// identifier; Method declarations) and cannot be referenced, so
		// several may be lowered side by side, like several `func _()`.
		if name == "_" || strings.HasSuffix(name, "._") {
			continue
		}
		if allowNativeInit && name == "init" {
			continue
		}
		if declared[name] {
			return fmt.Errorf("gosource: package-level name %s declared twice in the lowered file", name)
		}
		declared[name] = true
	}
	return nil
}

// refuseEmbedDirectives rejects go:embed inside a mapped package: embed
// directives are attached for the program's files only and the runtime's
// embed request assumes the program's single source root.
func refuseEmbedDirectives(c *converter) error {
	for _, f := range c.files {
		for _, group := range f.Comments {
			for _, comment := range group.List {
				if strings.HasPrefix(comment.Text, "//go:embed ") || strings.HasPrefix(comment.Text, "//go:embed\t") || comment.Text == "//go:embed" {
					return fmt.Errorf("%s: gosource: go:embed in mapped package %q is not supported by the explicit package map", c.fset.Position(comment.Slash), c.packagePath)
				}
			}
		}
	}
	return nil
}
