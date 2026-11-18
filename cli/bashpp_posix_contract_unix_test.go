//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIBashPPPOSIXContract(t *testing.T) {
	// The function definition never invokes agent assistance. Its syntax is
	// accepted only by Bash++, so every rejection has an activation control.
	const definition = "agentic func f(n int) int { return n }"
	for _, front := range []struct {
		name  string
		build func(*testing.T) string
	}{
		{"bash", builtBashBin},
		{"bashy", builtBashyBin},
	} {
		t.Run(front.name, func(t *testing.T) {
			binary := front.build(t)
			dir := t.TempDir()
			file := filepath.Join(dir, "extension.bpp")
			if err := os.WriteFile(file, []byte(definition+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			type result struct {
				stdout string
				stderr string
				exit   int
			}
			run := func(args []string, env []string, src string, fileInput bool) result {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				argv := append([]string{"--noprofile", "--norc"}, args...)
				if fileInput {
					argv = append(argv, file)
				} else {
					argv = append(argv, "-c", src)
				}
				cmd := exec.CommandContext(ctx, binary, argv...)
				cmd.Env = append([]string{"PATH=/bin:/usr/bin", "HOME=" + dir, "LC_ALL=C"}, env...)
				cmd.Dir = dir
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				if ctx.Err() != nil {
					t.Fatalf("invocation exceeded its deadline: %q", argv)
				}
				exit := 0
				if err != nil {
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) {
						t.Fatalf("run %q: %v", argv, err)
					}
					exit = exitErr.ExitCode()
				}
				return result{stdout.String(), stderr.String(), exit}
			}
			for _, tc := range []struct {
				name  string
				args  []string
				env   []string
				on    bool
				posix bool
			}{
				{"binary-default", nil, nil, front.name == "bashy", false},
				{"canonical", []string{"--bashpp"}, nil, true, false},
				{"alias", []string{"--bash++"}, nil, true, false},
				{"environment-on", nil, []string{"BASHY_BASHPP=1"}, true, false},
				{"environment-off", nil, []string{"BASHY_BASHPP=0"}, false, false},
				{"explicit-off", []string{"--no-bashpp"}, []string{"BASHY_BASHPP=1"}, false, false},
				{"last-selector-off", []string{"--bash++", "--no-bashpp"}, nil, false, false},
				{"last-selector-on", []string{"--no-bashpp", "--bash++"}, nil, true, false},
				{"posix-default", []string{"--posix"}, nil, false, true},
				{"posix-before-canonical", []string{"--posix", "--bashpp"}, nil, false, front.name == "bashy"},
				{"posix-after-canonical", []string{"--bashpp", "--posix"}, nil, false, front.name == "bashy"},
				{"posix-before-alias", []string{"--posix", "--bash++"}, nil, false, front.name == "bashy"},
				{"posix-after-alias", []string{"--bash++", "--posix"}, nil, false, front.name == "bashy"},
				{"posix-short-option", []string{"--bashpp", "-o", "posix"}, nil, false, front.name == "bashy"},
				{"posix-environment-on", []string{"--posix"}, []string{"BASHY_BASHPP=1"}, false, front.name == "bashy"},
				{"posix-environment-off", []string{"--posix"}, []string{"BASHY_BASHPP=0"}, false, true},
				{"posix-cli-off-beats-env-on", []string{"--posix", "--no-bashpp"}, []string{"BASHY_BASHPP=1"}, false, true},
				{"posix-cli-on-beats-env-off", []string{"--bash++", "--posix"}, []string{"BASHY_BASHPP=0"}, false, front.name == "bashy"},
				{"posix-last-selector-off", []string{"--bash++", "--posix", "--no-bashpp"}, nil, false, true},
				{"posix-last-selector-on", []string{"--no-bashpp", "--posix", "--bash++"}, nil, false, front.name == "bashy"},
				{"posix-env-trigger", []string{"--bashpp"}, []string{"POSIXLY_CORRECT="}, false, front.name == "bashy"},
				{"posix-shellopts-trigger", []string{"--bash++"}, []string{"SHELLOPTS=posix"}, false, front.name == "bashy"},
				{"posix-pedantic-trigger", nil, []string{"POSIX_PEDANTIC="}, false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, input := range []struct {
						name    string
						src     string
						file    bool
						offExit int
					}{
						{"command", definition, false, 2},
						{"file", "", true, 2},
						// eval reparses through the runtime dialect rather than the
						// CLI's startup parser, exposing disagreement between them.
						// These builtins retain their established parse-error status 1.
						{"eval", "eval '" + definition + "'", false, 1},
						{"source", ". ./extension.bpp", false, 1},
					} {
						t.Run(input.name, func(t *testing.T) {
							got := run(tc.args, tc.env, input.src, input.file)
							wantExit := input.offExit
							if tc.on {
								wantExit = 0
							}
							if got.exit != wantExit || got.stdout != "" || (got.stderr == "") != tc.on {
								t.Fatalf("got=%+v, want exit=%d and Bash++ accepted=%v", got, wantExit, tc.on)
							}
							if !tc.on && (!strings.Contains(got.stderr, "syntax error near unexpected token") && !strings.Contains(got.stderr, "a command can only contain words and redirects")) {
								t.Fatalf("disabled grammar did not reject the definition while parsing: %+v", got)
							}
							if strings.Contains(got.stderr, "extensions disabled") {
								t.Fatalf("disabled grammar reached Bash++ evaluation: %+v", got)
							}
						})
					}
					// Check the runtime option directly. Sprint 114's bash combined
					// selector deliberately has both extensions and POSIX off.
					got := run(tc.args, tc.env, "set -o", false)
					if got.exit != 0 || got.stderr != "" {
						t.Fatalf("option enumeration failed: %+v", got)
					}
					foundPosix := false
					for _, line := range strings.Split(got.stdout, "\n") {
						fields := strings.Fields(line)
						if len(fields) == 2 && fields[0] == "bashpp" && !tc.on {
							t.Fatalf("disabled grammar was exposed as a runtime option: %q", line)
						}
						if len(fields) == 2 && fields[0] == "posix" {
							foundPosix = true
							if (fields[1] == "on") != tc.posix {
								t.Fatalf("POSIX runtime profile mismatch: %q, want on=%v", line, tc.posix)
							}
						}
					}
					if !foundPosix {
						t.Fatalf("option enumeration omitted the POSIX runtime profile: %q", got.stdout)
					}
				})
			}
			// Resolving POSIX centrally must not discard unrelated imported
			// options when applying the standalone compatibility profile.
			got := run([]string{"--bashpp"}, []string{"SHELLOPTS=posix:errexit"}, "false; printf should-not-run", false)
			if got.exit != 1 || got.stdout != "" || got.stderr != "" {
				t.Fatalf("unrelated imported option was changed: %+v", got)
			}
		})
	}
}
