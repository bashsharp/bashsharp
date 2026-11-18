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

// Sprint 221, story 574 (84d746b3db12): lowered parity for B9 (a .py file by
// path) and B8 (an island function as a command word). The interpreter's
// behaviour is pinned in interp/bashpp_python_{file,command}_test.go; here
// the same script is compiled to Go, built and run, and must print the same.

// lowerPythonProject declares one project environment (bashpp.json under a
// pyproject.toml root) whose runtime is the host python3, and the fixture
// file the script imports.
func lowerPythonProject(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(python) + `"`
	for name, content := range map[string]string{
		"pyproject.toml": "[project]\nname = \"fixture\"\n",
		"bashpp.json":    `{"language":"python","runtime":` + quoted + `}`,
		filepath.Join("tools", "cmd.py"): `import sys
a = 617
count = 0
def bump():
    global count
    count += 1
    return count
def echo(*args): print(' '.join(args))
def stream():
    print('hallo on err', file=sys.stderr)
    print('hallo on out')
    return 1
def status(code): return int(code)
`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const lowerPythonScript = `import python "./tools/cmd.py" as py
import python "./tools/cmd.py" as again
a := py.a
py.bump()
n := again.bump()
echo "$a:$n"
py.echo --option1 --option2
py.stream 2> err.txt
echo "stream=$?"
py.status 7
echo "status=$?"
py.nope
echo "missing=$?"
`

const lowerPythonWant = "617:2\n--option1 --option2\nhallo on out\nstream=1\nstatus=7\nmissing=127\n"

// Compilation alone: the plan carries the resolved path, one identity is one
// worker however many aliases name it, module attributes lower to Attr, and
// the entry seeds the shell backend with the import aliases so a command
// word in a region reaches the program's own worker.
func TestPythonFileImportLowersWithSeededCommands(t *testing.T) {
	dir := lowerPythonProject(t)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(lowerPythonScript), filepath.Join(dir, "source.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.Getwd()
	result, err := lower.Compile(file, lower.Options{Dir: root, Origin: filepath.Join(dir, "source.bpp")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ForeignImports) != 1 || result.ForeignImports[0].Path != filepath.Join(dir, "tools", "cmd.py") || result.ForeignImports[0].Module != "./tools/cmd.py" {
		t.Fatalf("foreign plans = %#v", result.ForeignImports)
	}
	generated := string(result.Source)
	for _, want := range []string{
		"polyglot.StartImport", `Path: "` + strings.ReplaceAll(filepath.Join(dir, "tools", "cmd.py"), `\`, `\\`) + `"`,
		`python0.Attr(`, `interp.ForeignImports(map[string]*`, `"again":`, `"py":`, `ShellRegion("py.echo --option1 --option2")`,
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated source lacks %q\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "python1") {
		t.Fatalf("two aliases of one file started two workers\n%s", generated)
	}
}

// Build and run: the lowered program prints what the interpreter prints.
func TestPythonFileImportAndCommandsRunLowered(t *testing.T) {
	dir := lowerPythonProject(t)
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(lowerPythonScript), filepath.Join(dir, "source.bpp"))
	if err != nil {
		t.Fatal(err)
	}
	root, _ := os.Getwd()
	result, err := lower.Compile(file, lower.Options{Dir: root, Origin: filepath.Join(dir, "source.bpp")})
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "generated.go"), result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module pythonfile\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + filepath.Dir(filepath.Dir(root)) + "\n"
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
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != lowerPythonWant {
		t.Fatalf("stdout=%q, want %q; stderr=%q", stdout.String(), lowerPythonWant, stderr.String())
	}
	if !strings.Contains(stderr.String(), "py.nope: command not found") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	redirected, _ := os.ReadFile(filepath.Join(dir, "err.txt"))
	if string(redirected) != "hallo on err\n" {
		t.Fatalf("redirected stderr = %q", redirected)
	}
}
