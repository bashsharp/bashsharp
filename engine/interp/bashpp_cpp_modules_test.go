//go:build full

package interp_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestBashPPCPPModuleAssets(t *testing.T) {
	zig := os.Getenv("BASHPP_TEST_ZIG")
	if zig == "" {
		var err error
		zig, err = exec.LookPath("zig")
		if err != nil {
			t.Skip("pinned Zig module provider unavailable")
		}
	}
	old := polyglot.ToolResolver
	polyglot.ToolResolver = func(name string) ([]string, string, error) {
		if name == "clang++" || name == "c++" {
			return []string{zig, "c++"}, "pinned module fixture", nil
		}
		if old != nil {
			return old(name)
		}
		executable, err := exec.LookPath(name)
		return []string{executable}, "test PATH tool", err
	}
	t.Cleanup(func() { polyglot.ToolResolver = old })
	t.Setenv("BASHPP_CXX", "")
	root := t.TempDir()
	for name, data := range map[string]string{
		"bashpp.json":  `{"language":"cpp","runtime":"clang++","native_modules":"modules.json"}`,
		"modules.json": `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm","requires":[]}]}`,
		"example.cppm": "export module Example;\nnamespace Example_NS { int internal(){return 42;} export int f(){return internal();} }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := "~~~cpp as native\nimport Example;\nint answer(){return Example_NS::f();}\n~~~\nx := native.answer()\necho \"$x\"\n"
	out, stderr, status := runBashPP(t, filepath.Join(root, "entry.bpp"), root, os.Environ(), source)
	if status != 0 || out != "42\n" || stderr != "" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, out, stderr)
	}
}
