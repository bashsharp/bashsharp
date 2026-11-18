//go:build full

package lower_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These files are byte-for-byte copies of the pinned csharp-by-example cases.
// Their free Run methods are corpus harness code, and both modes must preserve
// the authored declarations above them.
var cSharpUnitCases = []struct{ name, path, digest string }{
	{"file-L18", "file/L18.bsh", "4cc82ec71b9246e3de433b3902c9020afb98182f491643a2dab482cb6e36f8a1"},
	{"using-directive-L133", "using-directive/L133.bsh", "b9a668a4e752715a7fb7ea89718c00fe57750be4d11f6df8dc5867b83ce5ec46"},
}

func cSharpPinnedUnitFixture(t *testing.T, path, digest string) (string, []byte) {
	t.Helper()
	fixture := filepath.Join("..", "internal", "testdata", "csharp-unit", filepath.FromSlash(path))
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != digest {
		t.Fatalf("pinned corpus fixture changed: %x", hash)
	}
	expected, err := os.ReadFile(strings.TrimSuffix(fixture, ".bsh") + ".expected")
	if err != nil || string(expected) != "# rc=0\n" {
		t.Fatalf("pinned corpus expectation changed: %v %q", err, expected)
	}
	return fixture, body
}

func TestCSharpCorpusCompilationUnitFixturePins(t *testing.T) {
	for _, tc := range cSharpUnitCases {
		t.Run(tc.name, func(t *testing.T) { cSharpPinnedUnitFixture(t, tc.path, tc.digest) })
	}
}

func TestCSharpCorpusCompilationUnitScopeBothModes(t *testing.T) {
	requirePowerShell(t)
	pwsh := os.Getenv("BASHPP_PWSH")
	if pwsh == "" {
		pwsh = "pwsh"
	}
	for _, tc := range cSharpUnitCases {
		t.Run(tc.name, func(t *testing.T) {
			path, body := cSharpPinnedUnitFixture(t, tc.path, tc.digest)
			control := filepath.Join("..", "docs", "s562", "verify-csharp-unit-scope.ps1")
			cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", control, path, tc.name)
			if output, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "native "+tc.name+" PASS" {
				t.Fatalf("independent native control: %v %q", err, output)
			}
			if got := testPythonFenceInterpretedNativeParityAt(t, string(body), path); got != "" {
				t.Fatalf("output = %q, want empty", got)
			}
		})
	}
}

// An ordinary type belongs to the generated Fence when it is private or
// depends on free fence state. Neither an external using static directive nor
// an ordinary class may force the compilation-unit partition.
func TestCSharpOrdinaryNestedTypeAndStateBothModes(t *testing.T) {
	requirePowerShell(t)
	source := `~~~csharp as cs
using static System.Math;
static int calls;
private class Counter { public static int Next() => ++calls; }
public static int Run() => Counter.Next() + (int)Sqrt(9);
~~~
a := cs.Run()
b := cs.Run()
echo "$a $b"
`
	if got := testPythonFenceInterpretedNativeParityAt(t, source, "ordinary-nested.bsh"); got != "4 5\n" {
		t.Fatalf("output = %q, want %q", got, "4 5\n")
	}
}
