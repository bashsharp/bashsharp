package polyglot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows PATH fallback prefers the launcher: bare python3/python on a
// stock runner are often App Execution Aliases os/exec cannot start, while
// `py` is a real executable. The order is py, python, python3.
func TestPythonPATHRuntimeOrder(t *testing.T) {
	if got := strings.Join(pythonPATHRuntimes("windows"), " "); got != "py python python3" {
		t.Fatalf("windows runtime order = %q", got)
	}
	if got := strings.Join(pythonPATHRuntimes("linux"), " "); got != "python3" {
		t.Fatalf("linux runtime order = %q", got)
	}
}

func TestLookupPythonRuntimeWindowsOrdering(t *testing.T) {
	sep := string(os.PathListSeparator)
	writeExe := func(dir, name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// python outranks python3 within one directory.
	dir := t.TempDir()
	want := writeExe(dir, "python.EXE")
	writeExe(dir, "python3.EXE")
	env := map[string]string{"PATH": dir, "PATHEXT": ".EXE"}
	got, err := lookupPythonRuntime(env, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(want) {
		t.Fatalf("resolved %s, want %s", got, want)
	}

	// The launcher wins even from a later PATH directory: the order is per
	// runtime name, not per directory.
	first, second := t.TempDir(), t.TempDir()
	writeExe(first, "python3.EXE")
	launcher := writeExe(second, "py.EXE")
	env = map[string]string{"PATH": first + sep + second, "PATHEXT": ".EXE"}
	got, err = lookupPythonRuntime(env, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != filepath.Base(launcher) {
		t.Fatalf("resolved %s, want the py launcher %s", got, launcher)
	}
}

func TestLookupPythonRuntimeUnix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "py"), []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "python3")
	if err := os.WriteFile(want, []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookupPythonRuntime(map[string]string{"PATH": dir}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "python3" {
		t.Fatalf("resolved %s, want python3 (py is windows-only)", got)
	}
}

func TestExecutableFileModeAppExecutionAlias(t *testing.T) {
	// A reparse-point alias is accepted on Windows (the launch path routes it
	// through cmd.exe) and refused everywhere else.
	if !executableFileMode(`python3.exe`, os.ModeIrregular, "windows", "") {
		t.Fatal("windows alias refused")
	}
	if executableFileMode(`python3.exe`, os.ModeIrregular|0o755, "linux", "") {
		t.Fatal("irregular file accepted on linux")
	}
	if executableFileMode(`python3.txt`, os.ModeIrregular, "windows", "") {
		t.Fatal("alias with a non-executable extension accepted")
	}
}

func TestPythonLauncherArgs(t *testing.T) {
	if got := pythonLauncherArgs("some/dir/py.exe"); len(got) != 1 || got[0] != "-3" {
		t.Fatalf("py.exe launcher args = %v, want [-3]", got)
	}
	if got := pythonLauncherArgs("some/dir/py"); len(got) != 1 || got[0] != "-3" {
		t.Fatalf("py launcher args = %v, want [-3]", got)
	}
	if got := pythonLauncherArgs("some/dir/python3.exe"); got != nil {
		t.Fatalf("python3.exe launcher args = %v, want none", got)
	}
}

func TestPythonArgumentsThroughLauncher(t *testing.T) {
	p := Python{Command: "dir/py.exe"}
	args := p.pythonArguments("SCRIPT", true)
	if strings.Join(args, " ") != "-3 -I -u -c SCRIPT" {
		t.Fatalf("launcher arguments = %v", args)
	}
	p = Python{Command: "python3"}
	args = p.pythonArguments("SCRIPT", false)
	if strings.Join(args, " ") != "-I -c SCRIPT" {
		t.Fatalf("interpreter arguments = %v", args)
	}
}

func TestExecNeedsCmdShim(t *testing.T) {
	if !execNeedsCmdShim(os.ModeIrregular, "windows") {
		t.Fatal("windows reparse point not routed through cmd.exe")
	}
	if execNeedsCmdShim(0, "windows") {
		t.Fatal("regular windows executable routed through cmd.exe")
	}
	if execNeedsCmdShim(os.ModeIrregular, "darwin") {
		t.Fatal("cmd.exe routing applied off windows")
	}
}

func TestWorkerExecArgsRegularFileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(path, []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, argv := workerExecArgs(path, []string{"-I", "-c", "S"})
	if name != path || strings.Join(argv, " ") != "-I -c S" {
		t.Fatalf("regular launch rewritten: %s %v", name, argv)
	}
}
