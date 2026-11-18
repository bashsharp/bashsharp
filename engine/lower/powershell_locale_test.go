//go:build full

package lower_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compare raw bytes with a real pwsh process, including date formatting. The
// environment comes from the caller; no golden normalization is permitted.
func TestPowerShellLocaleNativeInterpretedCompiled(t *testing.T) {
	requirePowerShell(t)
	command := os.Getenv("BASHPP_PWSH")
	if command == "" {
		command = "pwsh"
	}
	for _, locale := range []string{"en_US.UTF-8", "de_DE.UTF-8"} {
		t.Run(locale, func(t *testing.T) {
			t.Setenv("LANG", locale)
			t.Setenv("LC_ALL", locale)
			t.Setenv("LC_CTYPE", locale)
			t.Setenv("TERM", "dumb")
			t.Setenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "0")
			const body = `function Report {
    [OutputType([string])]
    param()
    $date = [datetime]::new(2020, 7, 14, 13, 24, 35)
    "$env:LANG|$env:LC_ALL|$env:LC_CTYPE|$env:TERM|$env:DOTNET_SYSTEM_GLOBALIZATION_INVARIANT|$($date.ToString())"
}
`
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			native, err := exec.CommandContext(ctx, command, "-NoProfile", "-NonInteractive", "-Command", body+"[Console]::Write((Report) + [char]10)").CombinedOutput()
			if err != nil {
				t.Fatalf("native pwsh: %v: %s", err, native)
			}
			source := "~~~powershell as ps\n" + body + "~~~\nvalue := ps.Report()\necho \"$value\"\n"
			got, gotErr := testForeignParityArtifactEnvironment(t, source, filepath.Join(t.TempDir(), "locale.bsh"), "", map[string]string{"LANG": "C", "LC_ALL": "C", "LC_CTYPE": "C", "TERM": "xterm", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1"}, 0)
			t.Logf("native bytes=%q", native)
			want := string(native)
			if got != want || gotErr != "" {
				t.Fatalf("native=%q interpreted/compiled=%q", want, got)
			}
		})
	}
}

func TestPowerShellLocaleRecipeCorpus(t *testing.T) {
	requirePowerShell(t)
	command := os.Getenv("BASHPP_PWSH")
	if command == "" {
		command = "pwsh"
	}
	command, err := exec.LookPath(command)
	if err != nil {
		t.Fatal(err)
	}
	command, err = filepath.Abs(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(command)+string(os.PathListSeparator)+os.Getenv("PATH"))
	data, err := os.ReadFile("testdata/powershell-locale/recipes.json")
	if err != nil {
		t.Fatal(err)
	}
	var recipes map[string]struct {
		Binding struct {
			SourceFile  string            `json:"source_file"`
			FenceSHA    string            `json:"fence_sha256"`
			SourceSHA   string            `json:"source_sha256"`
			Environment map[string]string `json:"environment"`
			Exit        int               `json:"exit"`
		} `json:"binding"`
		Expected string `json:"expected"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal(data, &recipes); err != nil {
		t.Fatal(err)
	}
	if len(recipes) != 16 {
		t.Fatalf("recipe denominator %d, want 16", len(recipes))
	}
	for id, recipe := range recipes {
		t.Run(id, func(t *testing.T) {
			for key, value := range recipe.Binding.Environment {
				t.Setenv(key, value)
			}
			// These are provider controls, not output normalization.
			t.Setenv("POWERSHELL_TELEMETRY_OPTOUT", "1")
			t.Setenv("POWERSHELL_UPDATECHECK", "Off")
			data, err := os.ReadFile(filepath.Join("testdata/powershell-locale", recipe.Binding.SourceFile))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != recipe.Binding.FenceSHA {
				t.Fatal("fixture source digest changed")
			}
			source := string(data)
			start := strings.Index(source, "~~~powershell as ps\n")
			if start < 0 {
				t.Fatal("missing source fence")
			}
			body, _, ok := strings.Cut(source[start+len("~~~powershell as ps\n"):], "\n~~~")
			if !ok {
				t.Fatal("unterminated source fence")
			}
			body += "\n"
			sum = sha256.Sum256([]byte(body))
			if hex.EncodeToString(sum[:]) != recipe.Binding.SourceSHA {
				t.Fatal("native source digest changed")
			}
			dir := t.TempDir()
			script := filepath.Join(dir, "native.ps1")
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			oracle, err := filepath.Abs("testdata/powershell-locale/oracle.ps1")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, command, "-NoProfile", "-NonInteractive", "-File", oracle, "-Path", script)
			cmd.Dir = dir
			var native, stderr bytes.Buffer
			cmd.Stdout = &native
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			status := 0
			if runErr != nil {
				if exit, ok := runErr.(*exec.ExitError); ok {
					status = exit.ExitCode()
				} else {
					t.Fatal(runErr)
				}
			}
			if status != recipe.Binding.Exit {
				t.Fatalf("native status=%d want=%d stderr=%q", status, recipe.Binding.Exit, stderr.String())
			}
			expected, err := base64.StdEncoding.DecodeString(recipe.Expected)
			if err != nil {
				t.Fatal(err)
			}
			expectedErr, err := base64.StdEncoding.DecodeString(recipe.Stderr)
			if err != nil {
				t.Fatal(err)
			}
			actual := fmt.Sprintf("# rc=%d\n", status) + native.String()
			if actual != string(expected) || stderr.String() != string(expectedErr) {
				t.Fatalf("native differs from immutable receipt: stdout=%q stderr=%q", actual, stderr.String())
			}
			golden, err := os.ReadFile(filepath.Join("testdata/powershell-locale", strings.TrimSuffix(recipe.Binding.SourceFile, ".bsh")+".expected"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(golden, expected) {
				t.Fatal("golden differs from native receipt")
			}
			t.Logf("native exit=%d stderr=%q", status, stderr.String())
			got, gotErr := testForeignParityArtifactEnvironment(t, source, filepath.Join(dir, "input.bsh"), "", map[string]string{"LANG": "C", "LC_ALL": "C", "LC_CTYPE": "C", "TERM": "xterm", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1"}, recipe.Binding.Exit)
			t.Logf("native bytes=%q; interpreted/compiled bytes=%q", native.String(), got)
			if got != native.String() || gotErr != stderr.String() {
				t.Fatalf("native=(%q,%q) interpreted/compiled=(%q,%q)", native.String(), stderr.String(), got, gotErr)
			}
		})
	}
}
