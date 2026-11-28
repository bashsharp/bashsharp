// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package front

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBashPPImportsOnlyTheEngine pins Sprint 211's D1: the dependency chain
// is sh ← bashpp ← bashy. The language front depends on the sh engine and on
// nothing from bashy, coreutils or yoke — so `bashpp` builds and tests in a
// checkout holding only itself and ../sh, and nothing above it can leak
// back in through a convenience import. Mirrors bashy's
// TestBinaryImportGraphsIsolateGfy (an import-graph guard, not a runtime
// check).
func TestBashPPImportsOnlyTheEngine(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/bashsharp/bashsharp/...").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable (%v): %s", err, out)
	}
	deps := string(out)
	for _, forbidden := range []string{
		"github.com/qiangli/bashy",
		"github.com/qiangli/coreutils",
		"github.com/qiangli/yoke",
	} {
		if strings.Contains(deps, forbidden) {
			t.Errorf("bashpp must not import %s — the language depends on the sh engine only (D1)", forbidden)
		}
	}
	if !strings.Contains(deps, "github.com/qiangli/bash/interp") {
		t.Error("bashpp should import github.com/qiangli/bash/interp — the engine is where the language runs")
	}
}

// TestModuleNamesNoYokeBashyYcode is the Sprint 746 U3 ratchet: Bash# is the
// language front door bashy consumes, never the other way round. The
// standalone module graph (requires and replaces), every package and test
// import, and every Go source string naming a package path stay clear of
// yoke, bashy and ycode, so `GOWORK=off go test ./...` needs none of them.
// Tests of bashy-the-product (building cmd/bashy and its siblings) live in
// bashy's cmd/bashy. There is no allowlist.
func TestModuleNamesNoYokeBashyYcode(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command unavailable: %v", err)
	}
	banned := []string{"github.com/qiangli/yoke", "github.com/qiangli/bashy", "github.com/qiangli/ycode"}
	isBanned := func(path string) bool {
		for _, b := range banned {
			if path == b || strings.HasPrefix(path, b+"/") {
				return true
			}
		}
		return false
	}
	root := ".."
	goList := func(args ...string) []string {
		t.Helper()
		cmd := exec.Command("go", append([]string{"list"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("GOWORK=off go list %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}
	for _, line := range goList("-m", "-f", "{{.Path}}{{with .Replace}} {{.Path}}{{end}}", "all") {
		for _, path := range strings.Fields(line) {
			if isBanned(path) {
				t.Errorf("module graph names %s (%s)", path, line)
			}
		}
	}
	for _, pkg := range goList("-deps", "-test", "./...") {
		if isBanned(pkg) {
			t.Errorf("a package or test of bashsharp imports %s", pkg)
		}
	}
	// A test that builds a sibling binary by package path never imports it.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, b := range banned {
			if strings.Contains(string(src), `"`+b+`/`) {
				t.Errorf("%s names a %s package path", path, b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
