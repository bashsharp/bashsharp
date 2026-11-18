package gosource

import (
	"errors"
	"fmt"
	"go/importer"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 165 S165.2 (D8 = (a)): identity-keyed internal visibility. The
// fixtures under testdata/sprint165/internal-visibility are outside the
// corpus; each case names the corpus row whose shape it reproduces.

const visibilityFixtures = "testdata/sprint165/internal-visibility"

func visibilitySource(t *testing.T, name string) Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(visibilityFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return Source{Name: filepath.Base(name), Data: data}
}

// directoryRuleImporter stands in for a fallback whose own visibility rule is
// keyed on directories (lower's module importer in the harness's scratch
// shape): through types.ImporterFrom it refuses EVERY internal package, as
// the scratch directory is outside every tree; through IdentityImporter it
// resolves policy-free and records the identity it was handed.
type directoryRuleImporter struct {
	std     types.Importer
	byIdent []string // "identity -> path" in call order
	legacy  []string // paths asked through ImportFrom
}

func (d *directoryRuleImporter) Import(path string) (*types.Package, error) {
	return d.ImportFrom(path, "", 0)
}

func (d *directoryRuleImporter) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	d.legacy = append(d.legacy, path)
	if _, internal := internalElement(path); internal {
		return nil, fmt.Errorf("use of internal package %s not allowed", path)
	}
	return d.std.Import(path)
}

func (d *directoryRuleImporter) ImportFromPackage(path, identity, srcDir string) (*types.Package, error) {
	d.byIdent = append(d.byIdent, identity+" -> "+path)
	return d.std.Import(path)
}

func internalElement(path string) (string, bool) {
	switch {
	case strings.HasSuffix(path, "/internal"):
		return path[:len(path)-len("/internal")], true
	case strings.Contains(path, "/internal/"):
		return path[:strings.LastIndex(path, "/internal/")], true
	case path == "internal", strings.HasPrefix(path, "internal/"):
		return "", true
	}
	return "", false
}

func newDirectoryRuleImporter() *directoryRuleImporter {
	return &directoryRuleImporter{std: importer.Default()}
}

// legacyOnly hides ImportFromPackage: a fallback that predates the interface.
type legacyOnly struct{ d *directoryRuleImporter }

func (l legacyOnly) Import(path string) (*types.Package, error) { return l.d.Import(path) }
func (l legacyOnly) ImportFrom(path, srcDir string, mode types.ImportMode) (*types.Package, error) {
	return l.d.ImportFrom(path, srcDir, mode)
}

// wantRefused asserts exactly one diagnostic, at the import's own position,
// with gc's wording for the importer error: "could not import P (use of
// internal package P not allowed)". Anything else — no error, a second
// diagnostic, another line, another wording — fails.
func wantRefused(t *testing.T, err error, file string, line, col int, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: import of %s succeeded, want refusal", file, path)
	}
	var list ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("%s: err = %v (%T), want a diagnostic list", file, err, err)
	}
	if len(list) != 1 {
		t.Fatalf("%s: %d diagnostics, want exactly one:\n%v", file, len(list), err)
	}
	want := fmt.Sprintf("%s:%d:%d: could not import %s (use of internal package %s not allowed)", file, line, col, path, path)
	if got := list[0].Error(); got != want {
		t.Fatalf("diagnostic:\n got %q\nwant %q", got, want)
	}
}

func wantResolved(t *testing.T, prog *Program, err error, from, path, origin string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, r := range prog.Resolutions {
		if r.From == from && r.Path == path {
			if r.Origin != origin {
				t.Fatalf("resolution %s -> %s has origin %q, want %q", from, path, r.Origin, origin)
			}
			return
		}
	}
	t.Fatalf("no resolution %s -> %s recorded in %#v", from, path, prog.Resolutions)
}

// A mapped internal package is visible to a mapped importer inside its parent
// and to no other identity: the map is consulted only after the decision.
func TestInternalVisibilityMappedPackage(t *testing.T) {
	x := PackageSpec{Path: "example.com/m/internal/x", Sources: []Source{visibilitySource(t, "mapped/x.go")}}
	y := visibilitySource(t, "mapped/y.go")
	other := visibilitySource(t, "mapped/other.go")

	t.Run("inside the parent", func(t *testing.T) {
		fb := newDirectoryRuleImporter()
		prog, err := Load([]Source{y}, Options{ImportPath: "example.com/m/y", Packages: []PackageSpec{x}, Importer: fb})
		wantResolved(t, prog, err, "example.com/m/y", "example.com/m/internal/x", "package-map")
		if len(fb.legacy)+len(fb.byIdent) != 0 {
			t.Fatalf("the fallback was consulted for a mapped package: %v %v", fb.legacy, fb.byIdent)
		}
	})
	t.Run("as a mapped importer", func(t *testing.T) {
		yy := PackageSpec{Path: "example.com/m/y", Sources: []Source{y}}
		prog, err := Load([]Source{src("main.go", "package main\n\nimport \"example.com/m/y\"\n\nfunc main() { _ = y.V }\n")},
			Options{ImportPath: "example.com/app", Packages: []PackageSpec{x, yy}, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "example.com/m/y", "example.com/m/internal/x", "package-map")
	})
	t.Run("foreign module refused", func(t *testing.T) {
		_, err := Load([]Source{other}, Options{ImportPath: "example.com/other", Packages: []PackageSpec{x}, Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "other.go", 6, 8, "example.com/m/internal/x")
	})
	t.Run("foreign mapped importer refused", func(t *testing.T) {
		o := PackageSpec{Path: "example.com/other", Sources: []Source{other}}
		_, err := Load([]Source{src("main.go", "package main\n\nimport \"example.com/other\"\n\nfunc main() { _ = other.V }\n")},
			Options{ImportPath: "example.com/m/app", Packages: []PackageSpec{x, o}, Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "other.go", 6, 8, "example.com/m/internal/x")
	})
	t.Run("same file admitted under a qualifying identity", func(t *testing.T) {
		prog, err := Load([]Source{other}, Options{ImportPath: "example.com/m/z", Packages: []PackageSpec{x}, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "example.com/m/z", "example.com/m/internal/x", "package-map")
	})
	t.Run("the program's own identity", func(t *testing.T) {
		// example.com/m itself is the parent of internal.
		prog, err := Load([]Source{other}, Options{ImportPath: "example.com/m", Packages: []PackageSpec{x}, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "example.com/m", "example.com/m/internal/x", "package-map")
	})
}

// A standard identity — cmd/compile/internal/foo, main, p — sees top-level
// internal/… and its own cmd/internal tree through the fallback's policy-free
// path; a dotted identity is refused before the fallback is asked.
func TestInternalVisibilityStandardIdentity(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		identity string
		path     string
		line     int
		col      int
		admit    bool
	}{
		{"15 package rows: cmd/compile identity", "std/buildcfg.go", "cmd/compile/internal/foo", "internal/buildcfg", 6, 8, true},
		{"cmd/go's <pkg>.test identity", "std/buildcfg.go", "cmd/compile/internal/base.test", "internal/buildcfg", 6, 8, true},
		{"the external test package", "std/buildcfg.go", "cmd/compile/internal/base_test", "internal/buildcfg", 6, 8, true},
		{"dotted identity refused", "std/buildcfg.go", "example.com/foo", "internal/buildcfg", 6, 8, false},
		{"intrinsic.go: -p main", "std/sys.go", "main", "internal/runtime/sys", 5, 10, true},
		{"intrinsic.go's shape under a user identity", "std/sys.go", "example.com/intrinsic", "internal/runtime/sys", 5, 10, false},
		{"escape_runtime_atomic.go: -p p", "std/atomic.go", "p", "internal/runtime/atomic", 6, 2, true},
		{"escape_runtime_atomic.go's shape under a user identity", "std/atomic.go", "example.com/escape", "internal/runtime/atomic", 6, 2, false},
		{"cmd/internal from a cmd/compile identity", "std/cmdinternal.go", "cmd/compile/internal/foo", "cmd/internal/src", 5, 8, true},
		{"cmd/internal from a dotted identity", "std/cmdinternal.go", "example.com/foo", "cmd/internal/src", 5, 8, false},
		{"cmd/internal from a standard identity outside cmd", "std/cmdinternal.go", "std/foo", "cmd/internal/src", 5, 8, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := newDirectoryRuleImporter()
			prog, err := Load([]Source{visibilitySource(t, c.file)}, Options{ImportPath: c.identity, Importer: fb})
			if !c.admit {
				wantRefused(t, err, filepath.Base(c.file), c.line, c.col, c.path)
				for _, asked := range append(fb.legacy, fb.byIdent...) {
					if strings.HasSuffix(asked, c.path) {
						t.Fatalf("the fallback was asked for %s after the refusal: %v %v", c.path, fb.legacy, fb.byIdent)
					}
				}
				return
			}
			wantResolved(t, prog, err, c.identity, c.path, "importer")
			want := c.identity + " -> " + c.path
			found := false
			for _, got := range fb.byIdent {
				found = found || got == want
			}
			if !found {
				t.Fatalf("fallback was not asked through IdentityImporter for %q: byIdent=%v legacy=%v", want, fb.byIdent, fb.legacy)
			}
			for _, asked := range fb.legacy {
				if asked == c.path {
					t.Fatalf("the directory rule was consulted for %s under a declared identity", c.path)
				}
			}
		})
	}
}

// The fact, not the suffix: testing/internal/testdeps is admitted for the
// asserted test main and for nothing else; the assertion exempts only
// testing/internal/….
func TestInternalVisibilityTestMain(t *testing.T) {
	testmain := visibilitySource(t, "testmain/testmain.go")
	testenv := visibilitySource(t, "testmain/testenv.go")

	t.Run("asserted test main of a cmd package", func(t *testing.T) {
		prog, err := Load([]Source{testmain}, Options{ImportPath: "cmd/compile/internal/base.test", TestMain: true, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "cmd/compile/internal/base.test", "testing/internal/testdeps", "importer")
	})
	t.Run("asserted test main of a user module", func(t *testing.T) {
		prog, err := Load([]Source{testmain}, Options{ImportPath: "example.com/m.test", TestMain: true, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "example.com/m.test", "testing/internal/testdeps", "importer")
	})
	t.Run("the suffix is not the fact", func(t *testing.T) {
		_, err := Load([]Source{testmain}, Options{ImportPath: "cmd/compile/internal/base.test", Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "testmain.go", 9, 2, "testing/internal/testdeps")
	})
	t.Run("a user identity without the fact", func(t *testing.T) {
		_, err := Load([]Source{testmain}, Options{ImportPath: "example.com/m.test", Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "testmain.go", 9, 2, "testing/internal/testdeps")
	})
	t.Run("the fact exempts testing/internal only", func(t *testing.T) {
		prog, err := Load([]Source{testenv}, Options{ImportPath: "cmd/internal/testdir.test", TestMain: true, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "cmd/internal/testdir.test", "internal/testenv", "importer")
		_, err = Load([]Source{testenv}, Options{ImportPath: "example.com/m.test", TestMain: true, Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "testenv.go", 8, 2, "internal/testenv")
	})
	t.Run("a mapped package is never the test main", func(t *testing.T) {
		tm := PackageSpec{Path: "example.com/m/helper", Sources: []Source{src("helper.go", "package helper\n\nimport \"testing/internal/testdeps\"\n\nvar _ testdeps.TestDeps\n")}}
		_, err := Load([]Source{src("main.go", "package main\n\nimport _ \"example.com/m/helper\"\n\nfunc main() {}\n")},
			Options{ImportPath: "example.com/m.test", TestMain: true, Packages: []PackageSpec{tm}, Importer: newDirectoryRuleImporter()})
		wantRefused(t, err, "helper.go", 3, 8, "testing/internal/testdeps")
	})
	t.Run("the fact requires an identity", func(t *testing.T) {
		_, err := Load([]Source{testmain}, Options{TestMain: true, Importer: newDirectoryRuleImporter()})
		if err == nil || !strings.Contains(err.Error(), "TestMain asserts the identity of the program and requires ImportPath") {
			t.Fatalf("err = %v, want the option refusal", err)
		}
	})
}

// A fallback without IdentityImporter still sees the map's refusal first and
// is otherwise used through ImporterFrom, so the negative set never depends
// on the fallback; the positive set then depends on the fallback's own rule.
func TestInternalVisibilityLegacyFallback(t *testing.T) {
	d := newDirectoryRuleImporter()
	fb := legacyOnly{d}
	if _, ok := types.Importer(fb).(IdentityImporter); ok {
		t.Fatal("legacyOnly must not offer IdentityImporter")
	}
	_, err := Load([]Source{visibilitySource(t, "std/buildcfg.go")}, Options{ImportPath: "example.com/foo", Importer: fb})
	wantRefused(t, err, "buildcfg.go", 6, 8, "internal/buildcfg")
	if len(d.legacy) != 0 {
		t.Fatalf("the fallback was asked after the map's refusal: %v", d.legacy)
	}
	// Admitted by the identity, then refused by the fallback's directory rule
	// — the shape of every corpus row until the fallback offers the
	// interface; the wording is the fallback's own.
	_, err = Load([]Source{visibilitySource(t, "std/buildcfg.go")}, Options{ImportPath: "cmd/compile/internal/foo", Importer: fb})
	wantRefused(t, err, "buildcfg.go", 6, 8, "internal/buildcfg")
	if len(d.legacy) != 1 || d.legacy[0] != "internal/buildcfg" {
		t.Fatalf("legacy asks = %v, want [internal/buildcfg]", d.legacy)
	}
}

// Without a declared identity nothing changes: the map is policy-free (an
// in-memory importcfg) and the fallback applies its own rule, as before.
func TestInternalVisibilityUndeclaredIdentityUnchanged(t *testing.T) {
	t.Run("fallback rule applies", func(t *testing.T) {
		fb := newDirectoryRuleImporter()
		_, err := Load([]Source{visibilitySource(t, "nomap/main.go")}, Options{Importer: fb})
		wantRefused(t, err, "main.go", 8, 8, "internal/abi")
		if len(fb.byIdent) != 0 || len(fb.legacy) != 1 {
			t.Fatalf("asks: byIdent=%v legacy=%v, want the legacy path once", fb.byIdent, fb.legacy)
		}
	})
	t.Run("mapped internal package stays a map hit", func(t *testing.T) {
		x := PackageSpec{Path: "example.com/m/internal/x", Sources: []Source{visibilitySource(t, "mapped/x.go")}}
		prog, err := Load([]Source{visibilitySource(t, "mapped/other.go")}, Options{Packages: []PackageSpec{x}, Importer: newDirectoryRuleImporter()})
		wantResolved(t, prog, err, "other", "example.com/m/internal/x", "package-map")
	})
	t.Run("golden: lower's module importer on a directory outside GOROOT", func(t *testing.T) {
		// The ordinary bashy program: no map, no --go-import-path, the
		// module importer built from the source directory. Today's verdict
		// is the directory rule's, and it is unchanged.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/nomap\n\ngo 1.25\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load([]Source{visibilitySource(t, "nomap/main.go")}, Options{Importer: lower.NewModuleImporter(dir)})
		wantRefused(t, err, "main.go", 8, 8, "internal/abi")
		_, err = Load([]Source{visibilitySource(t, "nomap/main.go")}, Options{ImportPath: "example.com/nomap", Importer: lower.NewModuleImporter(dir)})
		wantRefused(t, err, "main.go", 8, 8, "internal/abi")
	})
}

// The three corpus import shapes admitted by the rule, and nothing wider:
// an unreviewed non-internal path is not this rule's business (it is
// visible by the rule and refused or admitted by whoever holds the
// inventory), and a foreign internal tree stays refused.
func TestInternalVisibilityNothingMore(t *testing.T) {
	fb := newDirectoryRuleImporter()
	_, err := Load([]Source{src("a.go", "package a\n\nimport \"cmd/link/internal/ld\"\n\nvar _ = ld.Main\n")}, Options{ImportPath: "cmd/compile/internal/foo", Importer: fb})
	wantRefused(t, err, "a.go", 3, 8, "cmd/link/internal/ld")
	_, err = Load([]Source{src("a.go", "package a\n\nimport \"example.com/dep/internal/secret\"\n\nvar _ = secret.N\n")}, Options{ImportPath: "cmd/compile/internal/foo", Importer: fb})
	wantRefused(t, err, "a.go", 3, 8, "example.com/dep/internal/secret")
	_, err = Load([]Source{src("a.go", "package a\n\nimport \"example.com/dep/internal/secret\"\n\nvar _ = secret.N\n")}, Options{ImportPath: "main", Importer: fb})
	wantRefused(t, err, "a.go", 3, 8, "example.com/dep/internal/secret")
}
