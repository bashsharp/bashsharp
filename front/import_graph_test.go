// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package front

import (
	"os/exec"
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
	out, err := exec.Command("go", "list", "-deps", "github.com/qiangli/bashsharp/...").CombinedOutput()
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
	if !strings.Contains(deps, "mvdan.cc/sh/v3/interp") {
		t.Error("bashpp should import mvdan.cc/sh/v3/interp — the engine is where the language runs")
	}
}
