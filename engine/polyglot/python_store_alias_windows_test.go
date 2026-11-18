package polyglot

import (
	"os"
	"path/filepath"
	"testing"
)

// Exercise the real Store reparse points, including their failing Stat path.
func TestPythonStoreAliasWindows(t *testing.T) {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps")
	found := 0
	for _, base := range []string{"python.exe", "python3.exe"} {
		name := filepath.Join(dir, base)
		info, err := os.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			continue
		} // An installed executable is not a placeholder.
		found++
		t.Logf("Store placeholder %s: mode=%s size=%d", base, info.Mode(), info.Size())
		if got, err := canonicalExecutable(name, nil); err == nil {
			t.Errorf("accepted Store placeholder %q", got)
		}
	}
	if found == 0 {
		t.Skip("no Store Python placeholders installed on this Windows host")
	}
	if got, err := lookupPythonRuntime(map[string]string{"PATH": dir, "PATHEXT": ".EXE"}, "windows"); err == nil {
		t.Errorf("alias-only PATH selected %q", got)
	}
}
