//go:build unix

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIPosixBashPPSignalIsByteInert(t *testing.T) {
	type result struct {
		stdout string
		stderr string
		exit   int
	}
	run := func(args ...string) result {
		t.Helper()
		cmd := exec.Command(builtBashBin(t), args...)
		cmd.Env = cleanPosixStartupEnv(os.Environ())
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		exit := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run %q: %v", args, err)
			}
			exit = exitErr.ExitCode()
		}
		return result{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
	}

	// These inputs deliberately exercise recovery paths where ordinary POSIX
	// mode produces fewer diagnostics. The Sprint 114 oracle requires the
	// combined selector to match the POSIX-off invocation byte for byte.
	for _, script := range []string{
		"func greet(name string, retries int == 3) string {\n    return name\n}\n",
		"type Color enum { Red; Green }\nswitch color {\ncase Red:\n    return 1\n}\n",
	} {
		want := run("-c", script)
		for _, selector := range []string{"--bashpp", "--bash++"} {
			if got := run("--posix", selector, "-c", script); got != want {
				t.Errorf("%s changed bash result for %q\ngot:  %+v\nwant: %+v", selector, script, got, want)
			}
		}
	}
}

func TestCLIPosixStartupMatchesBash53(t *testing.T) {
	bash := builtBashBin(t)
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	bashy := filepath.Join(dir, "bashy")
	if err := os.Symlink(bash, sh); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bash, bashy); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		path      string
		args      []string
		env       string
		wantOn    bool
		wantPLC   string
		wantAlias bool
	}{
		{name: "plain bash", path: bash, wantPLC: "unset"},
		{name: "plain bashy", path: bashy, wantPLC: "unset"},
		{name: "long option", path: bash, args: []string{"--posix"}, wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "set option", path: bash, args: []string{"-o", "posix"}, wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "shellopts forces after off", path: bash, args: []string{"+o", "posix"}, env: "SHELLOPTS=posix", wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "correct empty forces after off", path: bash, args: []string{"+o", "posix"}, env: "POSIXLY_CORRECT=", wantOn: true, wantPLC: "", wantAlias: true},
		{name: "correct value", path: bash, env: "POSIXLY_CORRECT=no", wantOn: true, wantPLC: "no", wantAlias: true},
		{name: "pedantic empty", path: bash, env: "POSIX_PEDANTIC=", wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "pedantic value", path: bash, env: "POSIX_PEDANTIC=no", wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "command on then off", path: bash, args: []string{"-o", "posix", "+o", "posix"}, wantPLC: "unset"},
		{name: "command off then on", path: bash, args: []string{"+o", "posix", "-o", "posix"}, wantOn: true, wantPLC: "y", wantAlias: true},
		{name: "invoked sh forces after off", path: sh, args: []string{"+o", "posix"}, wantOn: true, wantPLC: "y", wantAlias: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The alias probe exercises CLI parsing, not just the runner option:
			// POSIX mode expands aliases in a non-interactive shell.
			script := "set -o\nprintf 'PLC=<%s>\\n' \"${POSIXLY_CORRECT-unset}\"\nalias hi='printf ALIAS'\nhi\n"
			args := append(append([]string{}, tc.args...), "-c", script)
			cmd := exec.Command(tc.path, args...)
			cmd.Env = cleanPosixStartupEnv(os.Environ())
			if tc.env != "" {
				cmd.Env = append(cmd.Env, tc.env)
			}
			out, err := cmd.CombinedOutput()
			if err != nil && tc.wantAlias {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
			text := string(out)
			isOn := false
			for _, line := range strings.Split(text, "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == "posix" {
					isOn = fields[1] == "on"
				}
			}
			if isOn != tc.wantOn {
				t.Fatalf("posix on=%v, want %v\n%s", isOn, tc.wantOn, text)
			}
			if !strings.Contains(text, "PLC=<"+tc.wantPLC+">") {
				t.Fatalf("wrong POSIXLY_CORRECT, want %q\n%s", tc.wantPLC, text)
			}
			if got := strings.HasSuffix(text, "ALIAS"); got != tc.wantAlias {
				t.Fatalf("alias expanded=%v, want %v\n%s", got, tc.wantAlias, text)
			}
		})
	}
}

func cleanPosixStartupEnv(env []string) []string {
	out := env[:0:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, "POSIXLY_CORRECT=") ||
			strings.HasPrefix(entry, "POSIX_PEDANTIC=") ||
			strings.HasPrefix(entry, "SHELLOPTS=") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
