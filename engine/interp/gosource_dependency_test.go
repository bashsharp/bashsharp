package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goListDeps resolves the transitive import list of pkg via the go tool,
// with GOWORK=off so workspace overlays cannot mask the module graph.
func goListDeps(t *testing.T, pkg string) []string {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOWORK=") {
			env = append(env, kv)
		}
	}
	env = append(env, "GOWORK=off")
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency graph for %s: %v: %s", pkg, err, output)
	}
	fields := bytes.Fields(output)
	deps := make([]string, len(fields))
	for i, f := range fields {
		deps[i] = string(f)
	}
	return deps
}

// The optional source frontend may consume the runtime, but the runtime must
// not make classic shell consumers link the frontend through a reverse edge.
func TestGoSourceRuntimeDoesNotDependOnFrontend(t *testing.T) {
	if slices.Contains(goListDeps(t, "github.com/bashsharp/bashsharp/engine/interp"), "github.com/bashsharp/bashsharp/engine/gosource") {
		t.Fatal("runtime links optional Go-source frontend")
	}
}

// Layer ratchet for the classic runtime packages: interp, syntax, and expand
// must never grow import edges into the Go-source frontend stack. The banned
// set is closed: gosource, lower, grammar, and polyglot. The only exception is
// the shrink-only allowlist below; entries may be removed, never added.
var layerBannedImports = []string{
	"github.com/bashsharp/bashsharp/engine/gosource",
	"github.com/bashsharp/bashsharp/engine/lower",
	"github.com/bashsharp/bashsharp/engine/grammar",
	"github.com/bashsharp/bashsharp/engine/polyglot",
}

// layerImportAllowlist holds temporary exceptions that must stay real: each
// entry must still be imported, otherwise the ratchet fails so the stale
// entry is deleted. The list only shrinks.
//
// interp -> polyglot: imported today; removed by S5 68dba07e6977.
var layerImportAllowlist = map[string][]string{
	"github.com/bashsharp/bashsharp/engine/interp": {"github.com/bashsharp/bashsharp/engine/polyglot"},
}

func TestLayerImportRatchet(t *testing.T) {
	for _, pkg := range []string{"interp", "syntax", "expand"} {
		t.Run(pkg, func(t *testing.T) {
			full := "github.com/bashsharp/bashsharp/engine/" + pkg
			deps := goListDeps(t, full)
			seen := make(map[string]bool, len(deps))
			for _, dep := range deps {
				seen[dep] = true
			}
			for _, dep := range deps {
				if !slices.Contains(layerBannedImports, dep) {
					continue
				}
				if slices.Contains(layerImportAllowlist[full], dep) {
					continue
				}
				t.Errorf("%s must not transitively import %s", full, dep)
			}
			for _, dep := range layerImportAllowlist[full] {
				if !seen[dep] {
					t.Errorf("allowlisted %s is no longer imported by %s; shrink the allowlist", dep, full)
				}
			}
		})
	}
}

func TestGoSourceTestingRejectsNilProgramView(t *testing.T) {
	r, err := interp.New(interp.Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	var program *gosource.Program
	for _, view := range []interp.GoSourceTestProgram{nil, program} {
		if _, err := r.LoadGoSourceTests(context.Background(), view); err == nil {
			t.Fatal("nil program accepted")
		}
	}
}
