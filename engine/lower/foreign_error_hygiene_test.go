//go:build full

package lower_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestForeignErrorExportKeepsPredeclaredErrorType(t *testing.T) {
	if os.Getenv("BASHPP_TYPESCRIPT_MODULE") == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to the official TypeScript compiler module")
	}
	base := filepath.Join("testdata", "s562", "handbook-v1-Basic-Types-L319")
	sourceBytes, err := os.ReadFile(base + ".bsh")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(base + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(want), "# rc=0\n") {
		t.Fatalf("unexpected corpus status metadata: %q", want)
	}
	want = []byte(strings.TrimPrefix(string(want), "# rc=0\n"))
	source := string(sourceBytes)
	_, fence, ok := strings.Cut(source, "~~~typescript\n")
	if !ok {
		t.Fatal("missing TypeScript fence")
	}
	fence, _, ok = strings.Cut(fence, "~~~")
	if !ok {
		t.Fatal("unterminated TypeScript fence")
	}
	// The official compiler must accept the unchanged handbook sample itself.
	exports, _, err := (polyglot.TypeScript{CompilerModule: os.Getenv("BASHPP_TYPESCRIPT_MODULE")}).AnalyzeArtifact(context.Background(), fence)
	if err != nil {
		t.Fatalf("native TypeScript: %v", err)
	}
	for _, name := range []string{"error", "fail", "infiniteLoop"} {
		found := false
		for _, export := range exports {
			found = found || export.Name == name
		}
		if !found {
			t.Fatalf("native TypeScript omitted %q", name)
		}
	}
	for _, mode := range []struct{ name, source string }{{"static", source}, {"execution", source + "\n(true)\n"}} {
		t.Run(mode.name, func(t *testing.T) {
			file := parse(t, mode.source, base+".bsh")
			result, err := lower.Compile(file, lower.Options{})
			if err != nil {
				t.Fatal(err)
			}
			generated := string(result.Source)
			if !strings.Contains(generated, "func error(") || !strings.Contains(generated, "__bpp0_rt.Error") {
				t.Fatalf("public callable or predeclared error alias missing:\n%s", generated)
			}
			if got := testPythonFenceInterpretedNativeParityAt(t, mode.source, base+".bsh"); got != string(want) {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
	t.Run("qualified export with user error type", func(t *testing.T) {
		qualified := strings.Replace(source, "~~~typescript\n", "~~~typescript as ts\n", 1)
		qualified += "\ntype error struct { Message string }\nfunc keep(e error) error { return e }\n"
		result, err := lower.Compile(parse(t, qualified, base+".bsh"), lower.Options{})
		if err != nil {
			t.Fatal(err)
		}
		generated := string(result.Source)
		if !strings.Contains(generated, "__bpp0_rt.Error") || !strings.Contains(generated, "type error struct") || !strings.Contains(generated, "func keep(e error) error") {
			t.Fatalf("generated type identities changed:\n%s", generated)
		}
		if got := testPythonFenceInterpretedNativeParityAt(t, qualified, base+".bsh"); got != string(want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
	t.Run("unaliased error with Python handle and two results", func(t *testing.T) {
		mixed := source + `
~~~python as py
import io
def make(): return io.StringIO("chain")
~~~
h, callErr := py.make()
value, callErr := h.read()
echo "$value:$callErr"
`
		if got := testPythonFenceInterpretedNativeParityAt(t, mixed, base+".bsh"); got != string(want)+"chain:\n" {
			t.Fatalf("output = %q", got)
		}
	})
	t.Run("unaliased error with runner callback", func(t *testing.T) {
		mixed := source + `
func r(verb string, file string, args ...string) string {
  if verb == "methods" { return "{\"name\":\"read\"}" }
  return "runner:" + verb
}
~~~notes as n !r
hello
~~~
value := n.read()
echo "$value"
`
		if got := testPythonFenceInterpretedNativeParityAt(t, mixed, base+".bsh"); got != string(want)+"runner:read\n" {
			t.Fatalf("output = %q", got)
		}
	})
}

func TestForeignErrorTypeShadowControls(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"user function shadows error", "func error() {}\nvar x error\n", "error (function) is not a type"},
		{"user type error", "type error struct { Message string }\nvar x error\n", ""},
		{"ordinary error signature", "func f(err error) error { return err }\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := lower.Compile(parse(t, test.source, "input.bpp"), lower.Options{})
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("diagnostic = %v, want %q", err, test.want)
			}
		})
	}
}

func TestForeignTypeScriptUnusedGeneratedFmt(t *testing.T) {
	module := os.Getenv("BASHPP_TYPESCRIPT_MODULE")
	if module == "" {
		t.Skip("set BASHPP_TYPESCRIPT_MODULE to the official TypeScript compiler module")
	}
	base := filepath.Join("testdata", "s562", "handbook-v2-More-on-Functions-L154")
	sourceBytes, err := os.ReadFile(base + ".bsh")
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := os.ReadFile(base + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	_, fence, ok := strings.Cut(source, "~~~typescript\n")
	if !ok {
		t.Fatal("missing TypeScript fence")
	}
	fence, _, ok = strings.Cut(fence, "~~~")
	if !ok {
		t.Fatal("unterminated TypeScript fence")
	}
	if _, _, err := (polyglot.TypeScript{CompilerModule: module}).AnalyzeArtifact(context.Background(), fence); err != nil {
		t.Fatalf("native TypeScript: %v", err)
	}
	want := strings.TrimPrefix(string(wantBytes), "# rc=0\n")
	for _, mode := range []struct{ name, source string }{{"static", source}, {"execution", source + "\n(true)\n"}} {
		t.Run(mode.name, func(t *testing.T) {
			result, err := lower.Compile(parse(t, mode.source, base+".bsh"), lower.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if mode.name == "static" && strings.Contains(string(result.Source), "import __bpp0_fmt \"fmt\"") {
				t.Fatalf("unused generated fmt import retained:\n%s", result.Source)
			}
			if mode.name == "execution" && !strings.Contains(string(result.Source), "import __bpp0_fmt \"fmt\"") {
				t.Fatal("used generated fmt import removed")
			}
			if got := testPythonFenceInterpretedNativeParityAt(t, mode.source, base+".bsh"); got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}
