package transpile

import (
	"fmt"
	"go/build"
	"path/filepath"

	"github.com/qiangli/bashpp/front"

	"mvdan.cc/sh/v3/gosource"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/lower"
)

// Sprint 118, W1 (Bashy half): the only file in this repository that imports
// the sh Go front end. It adapts mvdan.cc/sh/v3/gosource onto the
// front.GoSourceLoad hook and does nothing else — no Go lexing, parsing or type
// checking happens on the Bashy side.
//
// Keeping the import here rather than in internal/cli is what stops the pure
// bash drop-in from linking the Go SOURCE FRONT END (mvdan.cc/sh/v3/gosource,
// go/build package selection, and this wiring): cmd/bash cannot reach
// internal/agentos. It no longer stops the drop-in from linking go/types —
// sh de4ff069's shared Bash++ native bridge (interp/bashpp_native_bridge.go)
// imports go/types and go/importer directly, so `go list -deps ./cmd/bash`
// names go/types and go/build. That is a property of the interpreter both
// binaries share, and the boundary this file defends is the FRONT END: verified
// by TestGoSourceFrontEndIsNotLinkedIntoClassicBash.
//
// The support is NOT behind a build tag — `--source=go` is a required part of
// the bashy binary, so `make build` links it and an optional front end can
// never be mistaken for a missing one.

func init() {
	front.GoSourceLoad = loadGoSource
	front.GoSourcePackageFiles = goSourcePackageFiles
	// The module context travels with the SOURCE, not with the runner's
	// working directory: the harness runs a program from a fresh runtime
	// directory holding only the declared assets, while the Go source it
	// names lives, with its go.mod, somewhere else. sh de4ff069 landed
	// interp.GoSourceModuleDir for exactly this, and it is the same
	// directory lower.NewModuleImporter type-checks against above, so
	// interpreted dependency resolution and type checking agree by
	// construction. Inert for shell source.
	front.GoSourceModuleDir = interp.GoSourceModuleDir
}

// loadGoSource hands the original bytes to the front end untouched and
// translates its provenance into the CLI's shape. It never modifies Data, and
// never reorders Files: the front end owns file ordering.
func loadGoSource(files []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
	sources := make([]gosource.Source, 0, len(files))
	for _, f := range files {
		sources = append(sources, gosource.Source{Name: f.Name, Data: f.Data})
	}
	// The module-aware importer is the SAME one lowering uses (it resolves
	// through the installed SDK's `go list`, honouring module, workspace,
	// vendor and internal-visibility rules), so a program that imports a
	// helper module type-checks identically in interpreted and compiled mode.
	// Without it the Go export importer resolves the standard library only,
	// and every local or helper-module import is rejected before lowering
	// gets a chance to resolve it.
	packages := make([]gosource.PackageSpec, 0, len(opts.Packages))
	for _, pkg := range opts.Packages {
		spec := gosource.PackageSpec{Path: pkg.Path}
		for _, f := range pkg.Files {
			spec.Sources = append(spec.Sources, gosource.Source{Name: f.Name, Data: f.Data})
		}
		packages = append(packages, spec)
	}
	prog, err := gosource.Load(sources, gosource.Options{
		RunMain:                opts.RunMain,
		PreserveNativeInit:     opts.PreserveNativeInit,
		Importer:               lower.NewModuleImporter(opts.Dir),
		GoVersion:              opts.GoVersion,
		TestBuiltins:           opts.TestBuiltins,
		CheckerBranchErrors:    opts.CheckerBranchErrors,
		CheckAfterSyntaxErrors: opts.CheckAfterSyntaxErrors,
		Packages:               packages,
		ImportBase:             opts.ImportBase,
		ImportPath:             opts.ImportPath,
		TestMain:               opts.TestMain,
	})
	if err != nil {
		return nil, err
	}
	out := &front.GoSourceProgram{
		File:          prog.File,
		Package:       prog.Package,
		Main:          prog.Main,
		InitFunctions: prog.InitFunctions,
		FrontEnd:      gosource.Version,
		Importer:      prog.Importer,
	}
	for _, s := range prog.Sources {
		out.Origins = append(out.Origins, front.GoSourceOrigin{
			Name: s.Name, SHA256: s.SHA256, Base: s.Base, Size: s.Size,
		})
	}
	for _, r := range prog.Resolutions {
		out.Resolutions = append(out.Resolutions, front.GoSourceImportResolution{
			From: r.From, Import: r.Import, Path: r.Path, Origin: r.Origin, Name: r.Name, Files: r.Files,
		})
	}
	return out, nil
}

// goSourcePackageFiles selects the Go files of a directory recipe the way the
// Go toolchain does: through go/build, so `//go:build` lines, `// +build`
// comments and _GOOS/_GOARCH filename suffixes all apply, and the host's
// GOOS/GOARCH/build-tag context is the one `go build ./dir` would use.
//
// Doing this by hand (every *.go that is not a test) is what made a package
// carrying main_darwin.go and main_linux.go — buildable natively on either
// host — collect both files and fail as a duplicate main. Explicit --go-file
// recipes keep their deliberate bypass: a caller that names files has already
// made the selection.
func goSourcePackageFiles(dir string) ([]string, error) {
	ctx := build.Default
	pkg, err := ctx.ImportDir(dir, 0)
	if err != nil {
		if _, ok := err.(*build.NoGoError); ok {
			return nil, fmt.Errorf("no Go source files in %s for %s/%s",
				dir, ctx.GOOS, ctx.GOARCH)
		}
		return nil, err
	}
	if len(pkg.CgoFiles) > 0 {
		return nil, fmt.Errorf("%s: cgo source files are not supported by --source=go (%v)",
			dir, pkg.CgoFiles)
	}
	if len(pkg.GoFiles) == 0 {
		return nil, fmt.Errorf("no Go source files in %s for %s/%s",
			dir, ctx.GOOS, ctx.GOARCH)
	}
	names := make([]string, 0, len(pkg.GoFiles))
	for _, name := range pkg.GoFiles {
		names = append(names, filepath.Join(dir, name))
	}
	return names, nil
}
