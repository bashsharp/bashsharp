//go:build unix

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalCommandEnvironmentUnderscore(t *testing.T) {
	bash := builtBashBin(t)
	envPath := "/usr/bin/env"
	if _, err := os.Stat(envPath); err != nil {
		t.Skip("env command unavailable")
	}
	grepPath := "/usr/bin/grep"
	if _, err := os.Stat(grepPath); err != nil {
		t.Skip("grep command unavailable")
	}
	path := filepath.Dir(envPath) + string(os.PathListSeparator) + filepath.Dir(grepPath)
	tests := []struct {
		name      string
		inherited bool
		script    string
	}{
		{name: "sole command inherited", inherited: true, script: envPath},
		{name: "sole command absent", script: envPath},
		{
			name:      "pipeline inherited",
			inherited: true,
			script:    envPath + " | " + grepPath + " '^_='",
		},
		{name: "exec inherited", inherited: true, script: "exec " + envPath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(bash, "-c", test.script)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + path}
			if test.inherited {
				cmd.Env = append(cmd.Env, "_=parent")
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var values []string
			for line := range strings.SplitSeq(string(out), "\n") {
				if value, ok := strings.CutPrefix(line, "_="); ok {
					values = append(values, value)
				}
			}
			if len(values) != 1 || values[0] != envPath {
				t.Fatalf("child _ values = %q, want [%q]", values, envPath)
			}
		})
	}
}
