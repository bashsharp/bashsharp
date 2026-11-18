//go:build full

package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestPythonImportPlansWithoutImporting(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	t.Setenv("BASHPP_PYTHON", python)
	dir := t.TempDir()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("import python \"does_not_exist.sprint183\" as missing\necho planned\n"), filepath.Join(dir, "source.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.Getwd()
	result, err := lower.Compile(file, lower.Options{Dir: root, Origin: filepath.Join(dir, "source.bpp")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ForeignImports) != 1 || result.ForeignImports[0].Module != "does_not_exist.sprint183" || result.ForeignImports[0].Alias != "missing" {
		t.Fatalf("foreign plans = %#v", result.ForeignImports)
	}
}

func TestPythonImportAliasCollision(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	t.Setenv("BASHPP_PYTHON", python)
	dir := t.TempDir()
	for _, source := range []string{
		"import python \"one.mod\" as py\nimport python \"two.mod\" as py\n",
		"func py() {}\nimport python \"one.mod\" as py\n",
	} {
		file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), filepath.Join(dir, "source.bpp"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lower.Compile(file, lower.Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("Compile error = %v", err)
		}
	}
}

func TestPythonImportObjectCallsLowerThroughSharedRuntime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	t.Setenv("BASHPP_PYTHON", python)
	dir := t.TempDir()
	t.Setenv("PYTHONPATH", dir)
	source := `import python "fixturemod" as fixture
thing := fixture.make(value: 4)
number := thing.add(amount: 3)
class := fixture.kind()
name := class.__name__
other := class(9)
value := other.value
trimmed := strings.TrimSpace(name)
fmt.Println(number, trimmed, value)
`
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("import \"fmt\"\nimport \"strings\"\n"+source), filepath.Join(dir, "source.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.Getwd()
	result, err := lower.Compile(file, lower.Options{Dir: root, Origin: filepath.Join(dir, "source.bpp")})
	if err != nil {
		t.Fatal(err)
	}
	generated := string(result.Source)
	for _, want := range []string{"polyglot.StartImport", ".CallKeywords(", ".CallAttr(", ".GetAttr(", ".Call("} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated source lacks %q\n%s", want, generated)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "fixturemod.py"), []byte(`
class Thing:
    def __init__(self, value=1): self.value=value
    def add(self, amount=1): self.value += amount; return self.value
def make(value=1): return Thing(value)
def kind(): return Thing
`), 0o644); err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "generated.go"), result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module pythonfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.Dir(filepath.Dir(root)) + "\n"
	if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(buildDir, "program")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, "generated.go")
	cmd.Dir, cmd.Env = buildDir, append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, output, result.Source)
	}
	cmd = exec.Command(binary)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v; stderr=%s", err, stderr.String())
	}
	if stdout.String() != "7 Thing 9\n" || stderr.Len() != 0 {
		t.Fatalf("output=(%q,%q)", stdout.String(), stderr.String())
	}
}
