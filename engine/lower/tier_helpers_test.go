// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

// Test helpers shared by the quick tier (default build) and the full tier
// (`-tags full`): the files that define the full-tier tests are excluded from
// the push gate, so anything an untagged test file also needs lives here.

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/bashsharp/bashsharp/engine/expand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// From compile_test.go.

func parse(t *testing.T, source, name string) *syntax.File {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type compiledCase struct {
	*lower.Result
	input string
}

func compile(t *testing.T, source string) compiledCase {
	t.Helper()
	r, err := lower.Compile(parse(t, source, "input.bpp"), lower.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return compiledCase{r, source}
}

func execute(t *testing.T, r compiledCase) (string, string) { return executeBuild(t, r) }

// From generic_method_acceptance_test.go.

func genericMethodOracle(t *testing.T, source string) (string, string, int) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic), interp.Dir(t.TempDir()), interp.Env(expand.ListEnviron("PATH=/no-tools")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status := 0
	if err = runner.Run(ctx, parse(t, source, "input.bpp")); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = int(exit)
	}
	return out.String(), diagnostic.String(), status
}

func executeBuild(t *testing.T, r compiledCase, flags ...string) (string, string) {
	t.Helper()
	return executeBuildNormalized(t, r, nil, flags...)
}

// executeBuildNormalized is executeBuild with a hook for legitimately
// nondeterministic output: normalize (when non-nil) rewrites each engine's
// stdout and stderr before the byte-for-byte parity diff, and the normalized
// compiled streams are what the caller receives. The @timed attestation line
// is the one consumer: its measured duration differs per run by design, so
// parity is proven on the line with the duration canonicalized.
func executeBuildNormalized(t *testing.T, r compiledCase, normalize func(string) string, flags ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(p, r.Source, 0600); err != nil {
		t.Fatal(err)
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	module := "module lowerfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n"
	if strings.Contains(string(r.Source), "lower/shellrt") {
		module += "require github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "program")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	args := append([]string{"build", "-mod=mod"}, flags...)
	args = append(args, "-o", binary, "generated.go")
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, out, r.Source)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, binary)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/no-tools"}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	compiledStatus := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		compiledStatus = exit.ExitCode()
	}
	var interpOut, interpErr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &interpOut, &interpErr), interp.Dir(cmd.Dir), interp.Env(expand.ListEnviron("PATH=/no-tools")))
	if err != nil {
		t.Fatal(err)
	}
	interpretedStatus := 0
	if err := runner.Run(ctx, parse(t, r.input, "input.bpp")); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		interpretedStatus = int(exit)
	}
	compiledOut, compiledErr := out.String(), stderr.String()
	interpretedOut, interpretedErr := interpOut.String(), interpErr.String()
	if normalize != nil {
		compiledOut, compiledErr = normalize(compiledOut), normalize(compiledErr)
		interpretedOut, interpretedErr = normalize(interpretedOut), normalize(interpretedErr)
	}
	if compiledStatus != interpretedStatus {
		t.Fatalf("status compiled=%d interpreted=%d; compiled=(%q,%q), interpreted=(%q,%q)", compiledStatus, interpretedStatus, compiledOut, compiledErr, interpretedOut, interpretedErr)
	}
	if compiledOut != interpretedOut || compiledErr != interpretedErr {
		t.Fatalf("parity mismatch: compiled=(%q,%q), interpreted=(%q,%q)\n%s", compiledOut, compiledErr, interpretedOut, interpretedErr, r.Source)
	}
	return compiledOut, compiledErr
}
