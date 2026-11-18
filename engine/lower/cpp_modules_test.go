//go:build full

package lower_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestCPPModuleFenceInterpretedNativeParity(t *testing.T) {
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
		"bashpp.json":        `{"language":"cpp","runtime":"clang++","native_modules":"modules.json"}`,
		"modules.json":       `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm","requires":[]},{"name":"Example","kind":"implementation","source":"implementation.cpp","requires":["Example"]}]}`,
		"example.cppm":       "export module Example;\nexport int answer();\n",
		"implementation.cpp": "module Example;\nint answer(){return 42;}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := "~~~cpp as native\nimport Example;\nint module_answer(){return answer();}\n~~~\nx := native.module_answer()\necho \"$x\"\n"
	got := testPythonFenceInterpretedNativeParityAt(t, source, filepath.Join(root, "entry.bpp"))
	if got != "42\n" {
		t.Fatalf("output=%q", got)
	}
}
