//go:build unix

package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func runOptionVisibilityCLI(t *testing.T, binary, selector, envSelector, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--noprofile", "--norc"}
	if selector != "" {
		args = append(args, selector)
	}
	args = append(args, "-c", script)
	cmd := exec.CommandContext(ctx, binary, args...)
	dir := t.TempDir()
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "LC_ALL=C"}
	if envSelector != "" {
		cmd.Env = append(cmd.Env, "BASHY_BASHPP="+envSelector)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI option visibility command timed out: %v", ctx.Err())
	}
	return string(out), err
}

func TestCLIClassicOptionListingsRemainIdenticalWithExplicitBashPP(t *testing.T) {
	binary := builtBashBin(t)
	for _, script := range []string{"set -o", "set +o", "shopt -o", "shopt -po", "compgen -A setopt"} {
		t.Run(script, func(t *testing.T) {
			want, err := runOptionVisibilityCLI(t, binary, "", "", script)
			if err != nil || strings.Contains(want, "bashpp") {
				t.Fatalf("Classic listing output=%q err=%v", want, err)
			}
			for _, tc := range []struct{ selector, envSelector string }{
				{"", "1"}, {"--bashpp", ""}, {"--bash++", ""}, {"--no-bashpp", "1"},
			} {
				got, err := runOptionVisibilityCLI(t, binary, tc.selector, tc.envSelector, script)
				if err != nil || got != want {
					t.Errorf("selector=%q env=%q changed Classic listing\ngot: %q\nwant: %q\nerr: %v", tc.selector, tc.envSelector, got, want, err)
				}
			}
		})
	}
}

func TestCLIClassicHiddenOptionStillEnablesBashPPGrammar(t *testing.T) {
	binary := builtBashBin(t)
	const script = "set -e\ntype Sprint119OptionProof int\nprintf '%s\\n' typed-mode-active\n"
	for _, tc := range []struct{ selector, envSelector string }{
		{"", "1"}, {"--bashpp", ""}, {"--bash++", ""},
	} {
		got, err := runOptionVisibilityCLI(t, binary, tc.selector, tc.envSelector, script)
		if err != nil || got != "typed-mode-active\n" {
			t.Errorf("hidden selector=%q env=%q did not activate grammar: output=%q err=%v", tc.selector, tc.envSelector, got, err)
		}
	}
	for _, tc := range []struct{ selector, envSelector string }{
		{"", ""}, {"--no-bashpp", "1"},
	} {
		got, err := runOptionVisibilityCLI(t, binary, tc.selector, tc.envSelector, script)
		if err == nil || strings.Contains(got, "typed-mode-active") {
			t.Errorf("disabled selector=%q env=%q activated grammar: output=%q err=%v", tc.selector, tc.envSelector, got, err)
		}
	}
}

func TestCLIBashyExplicitBashPPOptionRemainsDiscoverable(t *testing.T) {
	binary := builtBashyBin(t)
	for _, script := range []string{"set -o", "set +o", "shopt -o", "shopt -po", "compgen -A setopt"} {
		t.Run(script, func(t *testing.T) {
			got, err := runOptionVisibilityCLI(t, binary, "--bashpp", "", script)
			if err != nil || !strings.Contains(got, "bashpp") {
				t.Fatalf("Bashy explicit selector lost discovery: output=%q err=%v", got, err)
			}
			got, err = runOptionVisibilityCLI(t, binary, "--no-bashpp", "1", script)
			if err != nil || strings.Contains(got, "bashpp") {
				t.Fatalf("Bashy disabled selector remained listed: output=%q err=%v", got, err)
			}
		})
	}
	got, err := runOptionVisibilityCLI(t, binary, "--bashpp", "", "type Sprint119BashyProof int\nprintf '%s\\n' typed-mode-active\n")
	if err != nil || got != "typed-mode-active\n" {
		t.Fatalf("Bashy discovery changed grammar activation: output=%q err=%v", got, err)
	}
}
