package cli

import (
	"bytes"
	"context"
	"os"
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
