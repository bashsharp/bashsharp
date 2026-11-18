package interp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPPythonImportObjects(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixturemod.py"), []byte(`
class Thing:
    def __init__(self, value=1): self.value=value
    def add(self, amount=1): self.value += amount; return self.value
def make(value=1): return Thing(value)
def kind(): return Thing
`), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `import python "fixturemod" as fixture
thing := fixture.make(value: 4)
number := thing.add(amount: 3)
class := fixture.kind()
name := class.__name__
other := class(9)
value := other.value
echo "$number:$name:$value"
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), filepath.Join(dir, "source.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &stderr), interp.Env(expand.ListEnviron("PATH="+filepath.Dir(python), "BASHPP_PYTHON="+python, "PYTHONPATH="+dir)))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	if out.String() != "7:Thing:9\n" {
		t.Fatalf("output = %q; stderr=%q", out.String(), stderr.String())
	}
}
