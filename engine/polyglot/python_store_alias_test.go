package polyglot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupPythonRuntimeSkipsStoreAlias(t *testing.T) {
	root := t.TempDir()
	aliases := filepath.Join(root, "AppData", "Local", "Microsoft", "WindowsApps")
	real := filepath.Join(root, "Python")
	for _, dir := range []string{aliases, real} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"python.EXE", "python3.EXE"} {
		if err := os.WriteFile(filepath.Join(aliases, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"PATH": aliases, "PATHEXT": ".EXE"}
	if got, err := lookupPythonRuntime(env, "windows"); err == nil {
		t.Fatalf("selected Store alias %s instead of reporting no usable Python", got)
	}
	want := filepath.Join(real, "python.EXE")
	if err := os.WriteFile(want, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	env["PATH"] += string(os.PathListSeparator) + real
	got, err := lookupPythonRuntime(env, "windows")
	if err != nil || !strings.HasSuffix(got, filepath.Join("Python", "python.EXE")) {
		t.Fatalf("lookup = %q, %v; want real interpreter %s", got, err, want)
	}
}

func TestPythonStoreAlias(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		goos string
		want bool
	}{
		{`C:\Users\test\AppData\Local\Microsoft\WindowsApps\python.exe`, 0, "windows", true},
		{`C:/Users/test/APPDATA/LOCAL/MICROSOFT/WINDOWSAPPS/PYTHON3.EXE`, 0, "windows", true},
		{`C:\Python\python.exe`, 0, "windows", false},
		{`C:\Users\test\AppData\Local\Microsoft\WindowsApps\python.exe`, 128, "windows", false},
		{`C:\Users\test\AppData\Local\Microsoft\WindowsApps\other.exe`, 0, "windows", false},
		{`C:\Users\test\AppData\Local\Microsoft\WindowsApps\nested\python.exe`, 0, "windows", false},
		{`/appdata/local/microsoft/windowsapps/python.exe`, 0, "linux", false},
	} {
		if got := pythonStoreAlias(tc.name, tc.size, tc.goos); got != tc.want {
			t.Errorf("pythonStoreAlias(%q, %d, %q) = %v; want %v", tc.name, tc.size, tc.goos, got, tc.want)
		}
	}
}
