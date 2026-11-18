package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiangli/bash/gosource"
)

// offlineGoEnv forces the installed SDK and the local module cache, making
// the go.dev release-index path unreachable: GOTOOLCHAIN=local never switches
// (and so never consults the network for a newer toolchain for go.mod's
// `toolchain` line), and GOPROXY=off fails a missing-cache lookup instead of
// dialing out. Every `go` invocation below must use it; a cold CI cache or a
// slow index fetch otherwise flakes the build (run 40530939179 timed out on
// macos-latest fetching the index). Entries are appended after os.Environ so
// the pins win over ambient CI values.
func offlineGoEnv() []string {
	return append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off")
}

// runGoCommand runs the installed go SDK offline and fails the test on error.
func runGoCommand(ctx context.Context, t *testing.T, what string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
	cmd.Env = offlineGoEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v %s", what, err, output)
	}
	return string(output)
}

// A real default executable is necessary: a package-only test cannot prove
// startup ordering relative to agentos.init or the cold CLI's shell enrichment.
func TestGoSourceExecutableEnvironment(t *testing.T) {
	dir := t.TempDir()
	bashy := filepath.Join(dir, "bashy")
	bashyPayload := bashy
	signalIgnore := ""
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		bashyPayload += ".real"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	runGoCommand(ctx, t, "default CLI build", "build", "-p", "2", "-o", bashyPayload, "github.com/qiangli/bashy/cmd/bashy")
	if bashyPayload != bashy {
		cc, err := exec.LookPath("cc")
		if err != nil {
			t.Skip("cc is required to exercise the shipped signal launcher")
		}
		cmd := exec.CommandContext(ctx, cc, "-x", "c", "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-o", bashy, "../native/siglaunch.c.in")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("signal launcher build: %v %s", err, output)
		}
		// A shell wrapper cannot carry arbitrary caller values for its own
		// special variables: dash rejects OPTIND=caller-optind before exec.
		// This native wrapper changes only the TERM disposition and execs the
		// target with the original environment and argument order.
		signalIgnore = filepath.Join(dir, "signal-ignore")
		cmd = exec.CommandContext(ctx, cc, "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-o", signalIgnore, "testdata/gosource-environment/signal-ignore.c")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("signal ignore wrapper build: %v %s", err, output)
		}
	}
	source, err := os.ReadFile("testdata/gosource-environment/environment-variables.go")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	oracle := filepath.Join(dir, "oracle")
	runGoCommand(ctx, t, "native build", "build", "-p", "2", "-o", oracle, path)
	cache := runGoCommand(ctx, t, "go env GOCACHE", "env", "GOCACHE")
	moduleCache := runGoCommand(ctx, t, "go env GOMODCACHE", "env", "GOMODCACHE")
	// The source importer needs an available build cache. Configure it equally
	// for both executables; the variables under test remain absent or supplied.
	// GOTOOLCHAIN=local pins the installed SDK and GOPROXY=off keeps the
	// compared runs off the network, matching the builds above.
	baseEnv := []string{"HOME=" + dir, "GOCACHE=" + strings.TrimSpace(cache), "GOMODCACHE=" + strings.TrimSpace(moduleCache), "GOTOOLCHAIN=local", "GOPROXY=off"}
	if cache := os.Getenv("BASHY_BIN_CACHE"); cache != "" {
		baseEnv = append(baseEnv, "BASHY_BIN_CACHE="+cache)
	}
	for name, env := range map[string][]string{
		"absent":        {"LAST=last", "BAR=original-bar", "FIRST=first"},
		"explicit":      {"LAST=last", "BASH=caller-bash", "SHELL=caller-shell", "SHLVL=17", "UID=caller-uid", "EUID=caller-euid", "IFS=caller-ifs", "OPTIND=caller-optind", "BASH_VERSION=caller-version", "BASHY_AGENT_MANIFEST=caller-manifest", "BASHY_HARD_IGNORE=USR1", "FIRST=first"},
		"long explicit": {"LAST=last", "BASHY_HARD_IGNORE=" + strings.Repeat("USR1,", 120) + "USR1", "FIRST=first"},
	} {
		t.Run(name, func(t *testing.T) {
			env = append(append([]string{}, baseEnv...), env...)
			run := func(binary string, args ...string) (string, string) {
				t.Helper()
				runCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				var cmd *exec.Cmd
				if signalIgnore != "" {
					// Force the launcher to add TERM to its private signal
					// sideband. The native oracle inherits the identical SIG_IGN
					// disposition without receiving that environment entry.
					wrapperArgs := append([]string{binary}, args...)
					cmd = exec.CommandContext(runCtx, signalIgnore, wrapperArgs...)
				} else {
					cmd = exec.CommandContext(runCtx, binary, args...)
				}
				cmd.Dir = dir
				cmd.Env = append([]string{}, env...)
				var out, stderr bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("run %s: %v stdout=%q stderr=%q", filepath.Base(binary), err, out.String(), stderr.String())
				}
				return out.String(), stderr.String()
			}
			wantOut, wantErr := run(oracle)
			gotOut, gotErr := run(bashy, "--bashpp", "--source=go", path)
			if gotOut != wantOut || gotErr != wantErr {
				t.Fatalf("CLI stdout=%q stderr=%q; native stdout=%q stderr=%q", gotOut, gotErr, wantOut, wantErr)
			}
		})
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, source) {
		t.Fatal("original source changed")
	}
}

// TestOfflineGoEnvPins keeps the network-free `go` invocations honest: the
// helper must pin the installed SDK and a dead proxy, and the pins must win
// over ambient CI values, so no `go` subprocess can reach the go.dev release
// index even when the ambient environment names a proxy or auto toolchain.
func TestOfflineGoEnvPins(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GOPROXY", "https://proxy.golang.org,direct")
	env := offlineGoEnv()
	pins := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "GOTOOLCHAIN" || name == "GOPROXY" {
			pins[name] = value
		}
	}
	if pins["GOTOOLCHAIN"] != "local" {
		t.Errorf("GOTOOLCHAIN=%q, want local", pins["GOTOOLCHAIN"])
	}
	if pins["GOPROXY"] != "off" {
		t.Errorf("GOPROXY=%q, want off", pins["GOPROXY"])
	}
}

func TestGoSourceWarmSessionEnvironment(t *testing.T) {
	env := []string{"SECOND=two", "BASHY_BASHPP=1", "BASH=caller-bash", "FIRST=one"}
	var out, stderr bytes.Buffer
	runner, err := NewSessionRunnerWithConfig(SessionIO{Env: env, Dir: t.TempDir(), Stdout: &out, Stderr: &stderr}, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader("package main\nimport \"fmt\"\nimport \"os\"\nfunc main(){fmt.Println(os.Environ())}"), "session.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("warm Runner: %v %s", err, stderr.String())
	}
	want := "[" + strings.Join(env, " ") + "]\n"
	if out.String() != want || stderr.Len() != 0 {
		t.Fatalf("warm environment %q %q; want %q", out.String(), stderr.String(), want)
	}
}
