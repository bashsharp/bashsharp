package polyglot

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlanImportIsLazyAndImmutable(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	source := filepath.Join(t.TempDir(), "source.bpp")
	plan, err := PlanImport(ImportRequest{
		Source: source, Language: "python", Module: "does_not_exist.sprint183", Alias: "missing",
		Environ: []string{"PATH=" + filepath.Dir(python), "BASHPP_PYTHON=" + python},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Module != "does_not_exist.sprint183" || plan.Alias != "missing" || plan.ID == "" || plan.Environment.Fingerprint == "" {
		t.Fatalf("plan = %#v", plan)
	}
	clone := plan.Clone()
	if len(clone.Environment.Env) > 0 {
		clone.Environment.Env[0] = "changed"
		if plan.Environment.Env[0] == "changed" {
			t.Fatal("clone aliases environment storage")
		}
	}
}
