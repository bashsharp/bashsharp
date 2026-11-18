//go:build full

package lower_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// C# is hosted by pwsh: culture must be selected before the CLR starts.
func TestCSharpLocaleNativeInterpretedCompiled(t *testing.T) {
	requirePowerShell(t)
	command := os.Getenv("BASHPP_PWSH")
	if command == "" {
		command = "pwsh"
	}
	for _, locale := range []string{"en_US.UTF-8", "de_DE.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			for key, value := range map[string]string{"LANG": locale, "LC_ALL": locale, "LC_CTYPE": locale, "TERM": "", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "0"} {
				t.Setenv(key, value)
			}
			const body = `public static string Report() {
    var culture = System.Globalization.CultureInfo.GetCultureInfo("fr-FR");
    var date = new System.DateTime(2020, 7, 14, 13, 24, 35);
    return $"{date}|{date.ToString("D", culture)}|" +
        System.Environment.GetEnvironmentVariable("LANG") + "|" +
        System.Environment.GetEnvironmentVariable("LC_ALL") + "|" +
        System.Environment.GetEnvironmentVariable("LC_CTYPE") + "|" +
        System.Environment.GetEnvironmentVariable("TERM") + "|" +
        System.Environment.GetEnvironmentVariable("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT");
}
`
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			native, err := exec.CommandContext(ctx, command, "-NoProfile", "-NonInteractive", "-Command", "Add-Type -TypeDefinition @'\npublic class LocaleFixture {\n"+body+"}\n'@\n[Console]::Write([LocaleFixture]::Report() + [char]10)").CombinedOutput()
			if err != nil {
				t.Fatalf("native C#: %v: %s", err, native)
			}
			source := "~~~csharp as cs\n" + body + "~~~\nvalue := cs.Report()\necho \"$value\"\n"
			got, stderr := testForeignParityArtifactEnvironment(t, source, filepath.Join(t.TempDir(), "culture.bsh"), "", map[string]string{"LANG": "C", "LC_ALL": "C", "LC_CTYPE": "C", "TERM": "xterm", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1"}, 0)
			t.Logf("native bytes=%q; interpreted/compiled bytes=%q", native, got)
			if got != string(native) || stderr != "" {
				t.Fatalf("native=%q interpreted/compiled=(%q,%q)", native, got, stderr)
			}
		})
	}
}

// The failing C#510 fixture is copied byte-for-byte, including its oracle.
func TestCSharpLocaleInterpolationRecipe(t *testing.T) {
	requirePowerShell(t)
	command := os.Getenv("BASHPP_PWSH")
	if command == "" {
		command = "pwsh"
	}
	data, err := os.ReadFile("testdata/csharp-locale/program.bsh")
	if err != nil {
		t.Fatal(err)
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256(data)); sum != "f7e1506bb34c6844fec57c2afaff1a21fdabb47f179f3f1776d3ae3ffa4d8b38" {
		t.Fatal("fixture changed", sum)
	}
	recipe, err := os.ReadFile("testdata/csharp-locale/program.env")
	if err != nil {
		t.Fatal(err)
	}
	if string(recipe) != "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=0\n" {
		t.Fatal("recipe changed")
	}
	t.Setenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "0")
	// Match the corpus host's default C locale, independently of its explicit recipe.
	for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		t.Setenv(key, "C.UTF-8")
	}
	t.Setenv("TERM", "dumb")
	_, body, ok := strings.Cut(string(data), "~~~csharp as cs\n")
	if !ok {
		t.Fatal("missing C# fence")
	}
	body, _, ok = strings.Cut(body, "\npublic static int Run() { Interpolation.Program.Main(); return 0; }\n~~~")
	if !ok {
		t.Fatal("missing generated entrypoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The fence exposes text with LF newlines. Set the native writer contract
	// explicitly; retain captured bytes and the authored source unchanged.
	native, err := exec.CommandContext(ctx, command, "-NoProfile", "-NonInteractive", "-Command", "Add-Type -TypeDefinition @'\n"+body+"\n'@\n[Console]::Out.NewLine = [char]10\n[Interpolation.Program]::Main()").CombinedOutput()
	if err != nil {
		t.Fatalf("native C#: %v: %s", err, native)
	}
	if runtime.GOOS != "windows" {
		golden, err := os.ReadFile("testdata/csharp-locale/program.expected")
		if err != nil {
			t.Fatal(err)
		}
		if "# rc=0\n"+string(native) != string(golden) {
			t.Fatalf("native differs from unchanged oracle: %q", native)
		}
	}
	got, stderr := testForeignParityArtifactEnvironment(t, string(data), filepath.Join(t.TempDir(), "program.bsh"), "", map[string]string{"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1"}, 0)
	t.Logf("native bytes=%q; interpreted/compiled bytes=%q", native, got)
	if got != string(native) || stderr != "" {
		t.Fatalf("native=%q interpreted/compiled=(%q,%q)", native, got, stderr)
	}
}
