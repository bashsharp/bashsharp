package cli

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qiangli/bash/expand"
	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/polyglot"
	"github.com/qiangli/bash/syntax"
)

func TestBashPPPlainScriptNeverDiscoversPython(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "python-must-not-be-probed")
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.Env(expand.ListEnviron("BASHPP_PYTHON="+missing)),
		interp.StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		t.Fatal(err)
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(
		strings.NewReader("echo no-python-ok\n"), filepath.Join(t.TempDir(), "plain.bpp"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), program); err != nil {
		t.Fatalf("plain Bash++ script probed invalid BASHPP_PYTHON: %v; stderr=%q", err, stderr.String())
	}
	if stdout.String() != "no-python-ok\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestBashPPPythonImportPlanIdentifiesSelectedEnvironment(t *testing.T) {
	// Planning must identify the selected environment without importing a module.
	executable, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := polyglot.PlanImport(polyglot.ImportRequest{
		Source:   filepath.Join(t.TempDir(), "source.bpp"),
		Language: "python",
		Module:   "does_not_exist.sprint183",
		Alias:    "missing",
		Environ:  []string{"PATH=" + filepath.Dir(executable), "BASHPP_PYTHON=" + executable},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Environment.Executable != executable {
		t.Fatalf("selected executable=%q, want %q", plan.Environment.Executable, executable)
	}
	if plan.Environment.Language != "python" || plan.Environment.Root == "" || plan.Environment.Fingerprint == "" {
		t.Fatalf("environment diagnostics incomplete: %#v", plan.Environment)
	}
	if !slices.Contains(plan.Environment.Explanation, "runtime executable overridden") {
		t.Fatalf("selection explanation=%q", plan.Environment.Explanation)
	}
	if plan.Module != "does_not_exist.sprint183" {
		t.Fatalf("planning imported or rewrote module: %#v", plan)
	}
}
