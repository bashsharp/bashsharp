// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// The companion build runs the host toolchain over the original package
// directory, because that is the only place the package's assembly files can
// be assembled against the symbols they name. None of the original Go source
// may be compiled there. Those bodies are the program under interpretation,
// and the generated helper already declares every package-level symbol the
// companions link against, so leaving an original file in the build would both
// redeclare those symbols and natively compile the very root being
// interpreted. Every Go file the build would otherwise read is therefore
// replaced, through the same overlay that introduces the helper, by a file
// holding nothing but its package clause.

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// bashPPCompanionRootStubs maps each Go file of the original package directory
// to the package-clause-only text that stands in for it. interpreted lists the
// inputs the runner itself loaded; they are stubbed whatever the host build
// context thinks of them, so an interpreted body can never reach the compiler.
func bashPPCompanionRootStubs(sourceDir string, interpreted []string) (map[string]string, error) {
	paths, err := bashPPCompanionRootGoFiles(sourceDir, interpreted)
	if err != nil {
		return nil, err
	}
	stubs := make(map[string]string, len(paths))
	for _, path := range paths {
		stub, err := bashPPCompanionStub(path)
		if err != nil {
			return nil, err
		}
		stubs[path] = stub
	}
	return stubs, nil
}

// bashPPCompanionRootGoFiles lists, in path order, the Go files of the
// original package directory that the companion build must not compile.
func bashPPCompanionRootGoFiles(sourceDir string, interpreted []string) ([]string, error) {
	if sourceDir == "" {
		return nil, fmt.Errorf("gosource: native companions require the original source directory")
	}
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !bashPPCompanionGoFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(sourceDir, entry.Name())
		// A file the host build context rejects is left alone: it contributes
		// no body to this build, and a stub carries none of its constraints,
		// so replacing it could only add a file the build had excluded.
		if !bashPPCompanionFileBuilt(path) {
			continue
		}
		add(path)
	}
	for _, path := range interpreted {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(sourceDir, path)
		}
		if filepath.Dir(path) != filepath.Clean(sourceDir) {
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			continue
		}
		add(path)
	}
	sort.Strings(out)
	return out, nil
}

// bashPPCompanionGoFileName reports the names `go build .` would consider for
// the package itself: Go source, not ignored by the toolchain's own name
// rules, and not a test file, which this build never compiles.
func bashPPCompanionGoFileName(name string) bool {
	if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
		return false
	}
	return !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
}

// bashPPCompanionStub is the replacement text for one original Go file: that
// file's own package clause and nothing else. Keeping the original clause
// means the stubbed directory still declares exactly the package the original
// program did, rather than being silently renamed into the helper's.
func bashPPCompanionStub(path string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", fmt.Errorf("gosource: assembly companion package file %s: %w", filepath.Base(path), err)
	}
	if file.Name == nil || file.Name.Name == "" {
		return "", fmt.Errorf("gosource: assembly companion package file %s declares no package", filepath.Base(path))
	}
	return "// Body withheld: this file is interpreted, never compiled.\npackage " + file.Name.Name + "\n", nil
}

// bashPPCompanionOverlayComplete states the invariant the companion build
// rests on, over the canonical directory cmd/go will actually read: every Go
// file it would select is answered by an overlay entry, so no original body
// reaches the compiler. It is checked rather than assumed because the paths
// the overlay is keyed by are resolved through symlinks, while the ones the
// stubs were derived from need not be.
func bashPPCompanionOverlayComplete(dir string, replace map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !bashPPCompanionGoFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if !bashPPCompanionFileBuilt(path) {
			continue
		}
		if _, ok := replace[path]; !ok {
			return fmt.Errorf("gosource: assembly companion build would compile original file %s", entry.Name())
		}
	}
	return nil
}

// bashPPCompanionBuildIsolated observes, rather than assumes, the invariant
// the companion build rests on. It asks cmd/go which Go files the build will
// compile under the very overlay the build is given, and requires that list to
// be the generated helper plus nothing but overlaid-away originals. The check
// has to exist because an overlay whose keys do not match the paths cmd/go
// resolves is not reported as an error: it is ignored, and the original
// package -- the program under interpretation -- would be compiled and run.
func bashPPCompanionBuildIsolated(ctx context.Context, goBinary string, file *bashPPImportSource, env []string) error {
	list := exec.CommandContext(ctx, goBinary, "list", "-overlay="+file.overlay, "-f", "{{range .GoFiles}}{{.}}\n{{end}}", ".")
	list.Dir, list.Env = file.sourceDir, env
	var out, diagnostics bytes.Buffer
	list.Stdout, list.Stderr = &out, &diagnostics
	if err := list.Run(); err != nil {
		return fmt.Errorf("gosource: list companion build: %w: %s", err, diagnostics.String())
	}
	worker := filepath.Base(file.buildPath)
	generated := false
	for _, name := range strings.Fields(out.String()) {
		if name == worker {
			generated = true
			continue
		}
		if _, ok := file.replace[filepath.Join(file.sourceDir, name)]; !ok {
			return fmt.Errorf("gosource: assembly companion build would compile original file %s", name)
		}
	}
	if !generated {
		return fmt.Errorf("gosource: assembly companion build does not include the generated helper; its overlay did not apply")
	}
	return nil
}
