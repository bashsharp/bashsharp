package interp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 221, story 574 (84d746b3db12): B9 `import python "<path>.py"` and
// B8 `alias.fn args` through the interpreter. Provenance for the ported
// fixtures (CPython v3.14.4 import-by-path tests; xonsh e2b76f7f callable
// alias cases) is recorded once, in polyglot/python_file_import_test.go and
// polyglot/python_command_test.go, next to the protocol-level ports; these
// tests assert the same facts at the shell surface.

// pythonProject lays out a project directory whose one Python environment is
// declared in bashpp.json (pyproject.toml marks the root) and returns an
// environment for the runner whose PATH holds no Python at all: the worker
// must come from the declaration, never from PATH discovery.
func pythonProject(t *testing.T) (dir string, environ []string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"fixture\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(python) + `"`
	if err := os.WriteFile(filepath.Join(dir, "bashpp.json"), []byte(`{"language":"python","runtime":`+quoted+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A private PATH with only the external tools the scripts pipe into.
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"tr", "wc"} {
		if path, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(path, filepath.Join(bin, tool))
		}
	}
	environ = []string{"PATH=" + bin, "HOME=" + dir}
	if runtime.GOOS == "windows" {
		environ = append(environ, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "PATHEXT="+os.Getenv("PATHEXT"))
	}
	return dir, environ
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runBashPP parses source as the named file and runs it from cwd with the
// given environment, returning stdout, stderr and the runner's exit status.
// A .sh name runs as classic bash; anything else as Bash#.
func runBashPP(t *testing.T, name, cwd string, environ []string, source string) (string, string, int) {
	t.Helper()
	lang := syntax.LangBashPP
	if strings.HasSuffix(name, ".sh") {
		lang = syntax.LangBash
	}
	file, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(lang), interp.Dir(cwd), interp.StdIO(nil, &stdout, &stderr), interp.Env(expand.ListEnviron(environ...)))
	if err != nil {
		t.Fatal(err)
	}
	status := 0
	if err := runner.Run(context.Background(), file); err != nil {
		var exit interp.ExitStatus
		if !errorsAs(err, &exit) {
			t.Fatalf("run: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		status = int(exit)
	}
	return stdout.String(), stderr.String(), status
}

func errorsAs(err error, target *interp.ExitStatus) bool {
	if exit, ok := err.(interp.ExitStatus); ok {
		*target = exit
		return true
	}
	return false
}

const fileFixture = `# This tests Python's ability to import a .py file.
a = 617
b = 42
count = 0
def bump():
    global count
    count += 1
    return count
def total(): return a+b
`

// CPython ImportTests.test_import / SimpleTest.test_module at the shell
// surface: a relative path resolves against the importing file, the module's
// contents are valid, its identity dunders name the file, and the default
// alias is the file stem.
func TestBashPPPythonFileImportRelative(t *testing.T) {
	dir, environ := pythonProject(t)
	writeFile(t, filepath.Join(dir, "tools", "fixture.py"), fileFixture)
	source := `import python "./tools/fixture.py"
name := fixture.__name__
file := fixture.__file__
sum := fixture.total()
a := fixture.a
echo "$name:$a:$sum"
echo "$file"
`
	// The script lives in dir/scripts and runs from an unrelated cwd: the
	// path is source-relative, not cwd-relative.
	writeFile(t, filepath.Join(dir, "scripts", "source.bpp"), source)
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "scripts", "source.bpp"), t.TempDir(), environ, strings.Replace(source, `"./tools/fixture.py"`, `"../tools/fixture.py"`, 1))
	if status != 0 || stderr != "" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	if stdout != "fixture:617:659\n"+filepath.Join(dir, "tools", "fixture.py")+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

// The absolute form imports the same file; on Windows the forward-slash
// drive spelling (C:/...) is accepted alongside the native one.
func TestBashPPPythonFileImportAbsolute(t *testing.T) {
	dir, environ := pythonProject(t)
	writeFile(t, filepath.Join(dir, "tools", "fixture.py"), fileFixture)
	spellings := []string{filepath.Join(dir, "tools", "fixture.py")}
	if runtime.GOOS == "windows" {
		spellings = append(spellings, filepath.ToSlash(spellings[0]))
	}
	for _, spelling := range spellings {
		// A Go import string: backslashes must be doubled.
		quoted := strings.ReplaceAll(spelling, `\`, `\\`)
		source := "import python \"" + quoted + "\" as fx\nvalue := fx.total()\necho \"$value\"\n"
		stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, source)
		if status != 0 || stderr != "" || stdout != "659\n" {
			t.Fatalf("%q: status=%d stderr=%q stdout=%q", spelling, status, stderr, stdout)
		}
	}
}

// Module identity: two aliases of one file share one module (and worker),
// as `import x as a; import x as b` do in CPython, so state written through
// one alias is read through the other; state persists across calls.
func TestBashPPPythonFileImportIdentityAndState(t *testing.T) {
	dir, environ := pythonProject(t)
	writeFile(t, filepath.Join(dir, "counter.py"), fileFixture)
	source := `import python "./counter.py" as first
import python "` + strings.ReplaceAll(filepath.Join(dir, "counter.py"), `\`, `\\`) + `" as second
first.bump()
first.bump()
third := second.bump()
viaSecond := second.count
viaFirst := first.count
echo "$third:$viaSecond:$viaFirst"
`
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, source)
	if status != 0 || stderr != "" || stdout != "3:3:3\n" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
}

// Failure legs: a missing file fails with the loader's own error naming the
// path (the call reports failure, the script keeps its shell semantics), and
// a file that does not parse reports SyntaxError.
func TestBashPPPythonFileImportFailures(t *testing.T) {
	dir, environ := pythonProject(t)
	writeFile(t, filepath.Join(dir, "bad.py"), "=\n")
	source := `import python "./missing.py"
import python "./bad.py"
missing.anything()
echo "missing=$?"
bad.anything()
echo "bad=$?"
`
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), dir, environ, source)
	if status != 0 || stdout != "missing=1\nbad=1\n" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	if !strings.Contains(stderr, "FileNotFoundError") || !strings.Contains(stderr, "missing.py") || !strings.Contains(stderr, "SyntaxError") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// The file's environment is the project's declared one even when the file
// lives outside the project: the interpreter is the declared runtime and the
// worker's working directory is the project root.
func TestBashPPPythonFileImportUsesProjectEnvironment(t *testing.T) {
	dir, environ := pythonProject(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "where.py"), "import os, sys\ndef cwd(): return os.getcwd()\ndef exe(): return sys.executable\n")
	quoted := strings.ReplaceAll(filepath.Join(outside, "where.py"), `\`, `\\`)
	source := "import python \"" + quoted + "\" as where\ncwd := where.cwd()\nexe := where.exe()\necho \"$cwd\"\necho \"$exe\"\n"
	stdout, stderr, status := runBashPP(t, filepath.Join(dir, "source.bpp"), outside, environ, source)
	if status != 0 || stderr != "" {
		t.Fatalf("status=%d stderr=%q stdout=%q", status, stderr, stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	root, _ := filepath.EvalSymlinks(dir)
	if len(lines) != 2 || lines[0] != root {
		t.Fatalf("worker cwd = %q, want project root %q", stdout, root)
	}
	if strings.Contains(lines[1], filepath.Join(dir, "bin")) || lines[1] == "" {
		t.Fatalf("interpreter = %q", lines[1])
	}
}
