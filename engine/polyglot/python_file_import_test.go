package polyglot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fixture provenance (Sprint 221, story 574 / 84d746b3db12, B9):
//
//   python/cpython tag v3.14.4 (commit 23116f998f6789d8c2fbe5ed5b8146854c8c2a4f),
//   PSF License Version 2 (LICENSE at that tag).
//   - Lib/test/test_import/__init__.py: ImportTests.test_import
//     (a/b module contents after import; Windows accepts .PY/.Py/.pY),
//     test_file_to_source (__file__ ends with .py), test_import_by_filename
//     (a filename is not a module name), test_case_sensitivity.
//   - Lib/test/test_importlib/source/test_file_loader.py: SimpleTest.test_module
//     (__name__, __file__, __package__ after loading), test_module_reuse
//     (the loaded module object is reused), test_bad_syntax (a file that fails
//     to load is not left in sys.modules), test_state_after_failure.
//   - Lib/test/test_importlib/test_spec.py: FactoryTests
//     test_spec_from_file_location_default (spec.origin is the path),
//     test_spec_from_file_location_default_bad_suffix (spam.eggs → no spec).
//
// Intentional adaptations: the loader is CPython's own importlib recipe run
// inside the worker, so the assertions are made through the Bash# surface
// (module attributes over the protocol) rather than on loader objects; the
// module is registered under its file stem as SourceFileLoader would name it;
// one worker per import means "reuse" is asserted as shared state and a
// stable __file__, not id(); bytecode (.pyc) legs are out of scope.

// projectPythonEnviron declares the one project environment in dir through a
// bashpp.json overlay and returns an environ WITHOUT a usable PATH, so a test
// that plans through it proves declaration → provisioned runtime, never PATH.
func projectPythonEnviron(t *testing.T, dir string) []string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	// pyproject.toml marks dir as the project root; bashpp.json declares its
	// one Python environment.
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"fixture\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bashpp.json"), []byte(`{"language":"python","runtime":`+jsonQuote(python)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	environ := []string{"PATH=" + filepath.Join(dir, "no-such-path")}
	if runtime.GOOS == "windows" {
		environ = append(environ, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "PATHEXT="+os.Getenv("PATHEXT"))
	}
	return environ
}

func jsonQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func writePython(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileImport(t *testing.T, dir, module, alias string) *Module {
	t.Helper()
	plan, err := PlanImport(ImportRequest{Source: filepath.Join(dir, "source.bpp"), Language: "python", Module: module, Alias: alias, Environ: projectPythonEnviron(t, dir)})
	if err != nil {
		t.Fatal(err)
	}
	module_ := StartImport(plan)
	t.Cleanup(func() { module_.Close() })
	return module_
}

func attrString(t *testing.T, m *Module, name string) string {
	t.Helper()
	result, err := m.Attr(context.Background(), name)
	if err != nil {
		t.Fatalf("attr %s: %v", name, err)
	}
	switch v := result.Value.(type) {
	case string:
		return v
	case nil:
		return "<nil>"
	default:
		t.Fatalf("attr %s = %#v", name, result.Value)
		return ""
	}
}

func TestPythonFileImportIsFileNotModule(t *testing.T) {
	for module, want := range map[string]bool{
		"pkg.mod": false, "os": false, "fixturemod": false,
		"./tools/x.py": true, "x.py": true, "/abs/x.py": true, `C:\tools\x.py`: true, "C:/tools/x.py": true,
		"tools/x": true, "X.PY": true, "./x.pY": true,
	} {
		if got := PythonFileImport(module); got != want {
			t.Errorf("PythonFileImport(%q) = %t, want %t", module, got, want)
		}
	}
}

// CPython FactoryTests.test_spec_from_file_location_default: the spec origin
// is the path handed in; test_spec_from_file_location_default_bad_suffix: a
// location without a source suffix yields no spec. Relative operands resolve
// against the importing source, and every Windows drive spelling a script may
// hand the shell resolves to the same native path.
func TestPythonFileImportPathResolution(t *testing.T) {
	cases := []struct {
		module, sourceDir, want string
		windows                 bool
	}{
		{"./tools/x.py", "/proj", "/proj/tools/x.py", false},
		{"tools/x.py", "/proj/sub", "/proj/sub/tools/x.py", false},
		{"../x.py", "/proj/sub", "/proj/x.py", false},
		{"/abs/x.py", "/proj", "/abs/x.py", false},
		{"x.py", "/proj", "/proj/x.py", false},
		{`C:\tools\x.py`, `C:\proj`, `C:\tools\x.py`, true},
		{"C:/tools/x.py", `C:\proj`, `C:\tools\x.py`, true},
		{"/c/tools/x.py", `C:\proj`, `C:\tools\x.py`, true},
		{"/mnt/c/tools/x.py", `C:\proj`, `C:\tools\x.py`, true},
		{"./tools/x.py", `C:\proj`, `C:\proj\tools\x.py`, true},
		{"../x.PY", `C:\proj\sub`, `C:\proj\x.PY`, true},
	}
	for _, c := range cases {
		if c.windows != (runtime.GOOS == "windows") && !c.windows {
			// POSIX expectations are only meaningful on a POSIX host.
			continue
		}
		got, err := PythonFileImportPath(c.sourceDir, c.module, c.windows)
		if err != nil {
			t.Errorf("%q in %q: %v", c.module, c.sourceDir, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q in %q (windows=%t) = %q, want %q", c.module, c.sourceDir, c.windows, got, c.want)
		}
	}
	// A backslash in a relative operand is a filename character (Unix
	// convention). NTFS cannot store it, so on Windows the import is not
	// found, and the error names the resolved path as the script spelled it.
	if _, err := PythonFileImportPath(`C:\proj`, `tools\x.py`, true); err == nil ||
		!strings.Contains(err.Error(), "No such file or directory") ||
		!strings.Contains(err.Error(), `C:\proj\tools\x.py`) || strings.ContainsAny(err.Error(), "|\uf05c") {
		t.Errorf(`tools\x.py on Windows: error = %v, want not found naming C:\proj\tools\x.py`, err)
	}
	for _, bad := range []string{"./spam.eggs", "./tools/x", "tools/x.txt"} {
		if _, err := PythonFileImportPath("/proj", bad, false); err == nil || !strings.Contains(err.Error(), ".py source") {
			t.Errorf("%q: error = %v, want .py source refusal", bad, err)
		}
	}
	if _, err := PythonFileImportPath("/proj", "pkg.mod", false); err == nil {
		t.Error("a dotted module resolved as a file")
	}
}

// PlanImport records the resolved absolute path, resolves relative operands
// against the source (never the working directory), keeps the environment
// the project's, and gives two aliases of one file one identity.
func TestPlanImportFilePathIdentity(t *testing.T) {
	dir := t.TempDir()
	environ := projectPythonEnviron(t, dir)
	source := filepath.Join(dir, "scripts", "source.bpp")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := PlanImport(ImportRequest{Source: source, Language: "python", Module: "../tools/x.py", Alias: "a", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "tools", "x.py")
	if relative.Path != want || relative.Module != "../tools/x.py" {
		t.Fatalf("relative plan = %#v, want path %q", relative, want)
	}
	absolute, err := PlanImport(ImportRequest{Source: source, Language: "python", Module: want, Alias: "b", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if absolute.Path != want {
		t.Fatalf("absolute plan path = %q", absolute.Path)
	}
	if relative.Identity() != absolute.Identity() {
		t.Fatalf("identities differ:\n%q\n%q", relative.Identity(), absolute.Identity())
	}
	if relative.ID == absolute.ID {
		t.Fatal("plan IDs ignore the alias")
	}
	module, err := PlanImport(ImportRequest{Source: source, Language: "python", Module: "tools.x", Alias: "a", Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	if module.Path != "" || module.Identity() == relative.Identity() {
		t.Fatalf("module plan = %#v", module)
	}
	if !strings.Contains(strings.Join(relative.Environment.Explanation, " "), "overlay") {
		t.Fatalf("environment was not the project-declared one: %v", relative.Environment.Explanation)
	}
}

// CPython ImportTests.test_import: the module's contents are valid after
// import (a, b). SimpleTest.test_module: __name__ is the module name, __file__
// the path, __package__ ”. test_file_to_source: __file__ ends with .py.
func TestPythonFileImportContentsAndIdentity(t *testing.T) {
	dir := t.TempDir()
	// The values are fixed where CPython draws them at random: the assertion
	// is the same, "module loaded but contents invalid" otherwise.
	writePython(t, filepath.Join(dir, "tools", "fixture.py"), "# This tests Python's ability to import a .py file.\na = 617\nb = 42\ndef total(): return a+b\n")
	m := fileImport(t, dir, "./tools/fixture.py", "fx")
	if got := attrString(t, m, "__name__"); got != "fixture" {
		t.Fatalf("__name__ = %q", got)
	}
	if got := attrString(t, m, "__file__"); got != filepath.Join(dir, "tools", "fixture.py") || !strings.HasSuffix(got, ".py") {
		t.Fatalf("__file__ = %q", got)
	}
	if got := attrString(t, m, "__package__"); got != "" {
		t.Fatalf("__package__ = %q", got)
	}
	for name, want := range map[string]int64{"a": 617, "b": 42} {
		result, err := m.Attr(context.Background(), name)
		if err != nil || result.Value != want {
			t.Fatalf("module loaded but contents invalid: %s = %#v, %v", name, result.Value, err)
		}
	}
	result, err := m.Call(context.Background(), "total")
	if err != nil || result.Value != int64(659) {
		t.Fatalf("total() = %#v, %v", result.Value, err)
	}
	if _, err := m.Attr(context.Background(), "_private"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private attribute error = %v", err)
	}
}

// An absolute path imports the same file; on Windows the .PY spelling is
// accepted as CPython's finder accepts it (ImportTests.test_import).
func TestPythonFileImportAbsoluteAndCase(t *testing.T) {
	dir := t.TempDir()
	writePython(t, filepath.Join(dir, "abs_fixture.py"), "value = 'absolute'\n")
	m := fileImport(t, dir, filepath.Join(dir, "abs_fixture.py"), "fx")
	if got := attrString(t, m, "value"); got != "absolute" {
		t.Fatalf("value = %q", got)
	}
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".PY", ".Py", ".pY"} {
			upper := fileImport(t, dir, filepath.Join(dir, "abs_fixture"+ext), "fx")
			if got := attrString(t, upper, "value"); got != "absolute" {
				t.Fatalf("%s: value = %q", ext, got)
			}
		}
	}
}

// Module state persists across calls (one worker, one module object), and a
// loaded module is cached: rewriting the file does not change what is loaded
// (sys.modules semantics; CPython deletes the entry and invalidates caches
// before it expects a fresh import).
func TestPythonFileImportStateAndCache(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "counter.py")
	writePython(t, file, "count = 0\ndef bump():\n    global count\n    count += 1\n    return count\n")
	m := fileImport(t, dir, "./counter.py", "counter")
	for want := int64(1); want <= 3; want++ {
		result, err := m.Call(context.Background(), "bump")
		if err != nil || result.Value != want {
			t.Fatalf("bump = %#v, %v (want %d)", result.Value, err, want)
		}
	}
	writePython(t, file, "count = 100\ndef bump(): return 'rewritten'\n")
	result, err := m.Call(context.Background(), "bump")
	if err != nil || result.Value != int64(4) {
		t.Fatalf("after rewrite bump = %#v, %v; the loaded module must be cached", result.Value, err)
	}
	// A fresh import of the rewritten file sees the new source: the cache is
	// per module object, not a stale copy pinned to the path forever.
	fresh := fileImport(t, dir, "./counter.py", "again")
	result, err = fresh.Call(context.Background(), "bump")
	if err != nil || result.Value != "rewritten" {
		t.Fatalf("fresh import bump = %#v, %v", result.Value, err)
	}
}

// Failure legs. ImportTests.test_import_by_filename: a path is not a module
// name — here the inverse guard, a file that does not exist fails with the
// loader's own error naming the path. SimpleTest.test_bad_syntax: a file
// that fails to load raises SyntaxError and is not registered, so fixing the
// file and importing again succeeds (test_state_after_failure: the failure
// leaves no half-loaded state behind).
func TestPythonFileImportFailures(t *testing.T) {
	dir := t.TempDir()
	missing := fileImport(t, dir, "./missing.py", "missing")
	_, err := missing.Call(context.Background(), "anything")
	detail, ok := ForeignErrorDetail(err)
	if !ok || detail.Code != "FileNotFoundError" || !strings.Contains(detail.Message, "missing.py") {
		t.Fatalf("missing file error = %v (%#v)", err, detail)
	}
	bad := filepath.Join(dir, "bad.py")
	writePython(t, bad, "=\n")
	m := fileImport(t, dir, "./bad.py", "bad")
	_, err = m.Attr(context.Background(), "value")
	detail, ok = ForeignErrorDetail(err)
	if !ok || detail.Code != "SyntaxError" {
		t.Fatalf("bad syntax error = %v (%#v)", err, detail)
	}
	writePython(t, bad, "value = 'fixed'\n")
	if got := attrString(t, m, "value"); got != "fixed" {
		t.Fatalf("after fixing the file value = %q", got)
	}
	// A module whose body raises at import time reports that exception, not
	// a loader failure, and the traceback names the file.
	writePython(t, filepath.Join(dir, "raises.py"), "raise RuntimeError('import-time boom')\n")
	raises := fileImport(t, dir, "./raises.py", "raises")
	_, err = raises.Attr(context.Background(), "value")
	detail, ok = ForeignErrorDetail(err)
	if !ok || detail.Code != "RuntimeError" || detail.Message != "import-time boom" || !strings.Contains(detail.Help, "raises.py") {
		t.Fatalf("import-time error = %v (%#v)", err, detail)
	}
	// A module name spelled like a file but planned without a .py suffix is
	// refused at planning time, before any worker starts.
	if _, err := PlanImport(ImportRequest{Source: filepath.Join(dir, "source.bpp"), Language: "python", Module: "./tools/x", Alias: "x", Environ: projectPythonEnviron(t, dir)}); err == nil || !strings.Contains(err.Error(), ".py source") {
		t.Fatalf("bad suffix plan error = %v", err)
	}
}

// The file's own directory is not added to sys.path: sibling modules resolve
// through the project environment's PYTHONPATH only, so a file outside the
// project does not pull its neighbours in by accident.
func TestPythonFileImportDoesNotExtendSysPath(t *testing.T) {
	dir := t.TempDir()
	writePython(t, filepath.Join(dir, "elsewhere", "helper.py"), "value = 'helper'\n")
	writePython(t, filepath.Join(dir, "elsewhere", "user.py"), "import helper\nvalue = helper.value\n")
	m := fileImport(t, dir, "./elsewhere/user.py", "user")
	_, err := m.Attr(context.Background(), "value")
	detail, ok := ForeignErrorDetail(err)
	if !ok || detail.Code != "ModuleNotFoundError" {
		t.Fatalf("sibling import error = %v (%#v); the file directory must not be on sys.path", err, detail)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("unexpected cancellation")
	}
}
