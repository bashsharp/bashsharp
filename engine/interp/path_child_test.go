package interp

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNativeExecEnvForChildKeepsShellPath(t *testing.T) {
	m := story678Mounts()
	path := "/usr/local/bin:/usr/GNU/bin:/usr/bin:/bin:."
	env := []string{"PATH=" + path, "HOME=/home/test", "TEMP=/tmp"}
	self := nativeExecEnvForChildMountsMode(m, env, true, true)
	if !slices.Contains(self, "PATH="+path) {
		t.Fatalf("Bashy child PATH = %q, want %q", self, path)
	}
	if !slices.Contains(self, `TEMP=C:\Temp`) {
		t.Fatalf("host TEMP not converted: %q", self)
	}
	native := nativeExecEnvForChildMountsMode(m, env, true, false)
	if slices.Contains(native, "PATH="+path) || !strings.Contains(strings.Join(native, "\n"), ";") {
		t.Fatalf("native child PATH not converted: %q", native)
	}
	if !slices.Equal(env, []string{"PATH=" + path, "HOME=/home/test", "TEMP=/tmp"}) {
		t.Fatalf("parent environment mutated: %q", env)
	}
}

func TestExecutableContentHashDistinguishesTail(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.exe")
	b := filepath.Join(dir, "b.exe")
	content := bytes.Repeat([]byte("Bashy image"), 500)
	if err := os.WriteFile(a, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, append([]byte(nil), content...), 0o755); err != nil {
		t.Fatal(err)
	}
	ha, err := executableContentHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := executableContentHash(b)
	if err != nil || ha != hb {
		t.Fatalf("identical copy hash = %x, %v; want %x", hb, err, ha)
	}
	content[len(content)-1] ^= 1
	if err := os.WriteFile(b, content, 0o755); err != nil {
		t.Fatal(err)
	}
	hb, err = executableContentHash(b)
	if err != nil || ha == hb {
		t.Fatalf("changed tail hash = %x, %v; want != %x", hb, err, ha)
	}
}
