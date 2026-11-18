//go:build full

package interp_test

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// The dependency worker is built through the policy-free toolchain path
// (importcfg): an import the checker admitted on the program's declared
// identity is not re-decided by cmd/go's directory rule at execute time.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func mustReadSprint165WorkerBuild(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-2", "worker-importcfg", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runGoSourceIdentity runs one original source under a declared identity and
// returns the outcome together with the Runner's own error, if any.
func runGoSourceIdentity(t *testing.T, source, identity string) (goSourceOutcome, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	options := []interp.RunnerOption{interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &stdout, &stderr)}
	if identity != "" {
		options = append(options, interp.GoSourceIdentity(identity, false))
	}
	runner, err := interp.New(options...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	outcome := goSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		outcome.status = int(status)
		err = nil
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*")); len(leftovers) > 0 {
		t.Fatalf("bridge workspace leaked: %v", leftovers)
	}
	return outcome, err
}

// intrinsic.go's shape: identity `main` (upstream's -p) importing
// internal/runtime/sys. The check admits it (D8); the worker build must not
// refuse it again. The expected values are the facts math/bits computes for
// the same inputs, not a pinned string.
func TestGoSourceSprint165WorkerImportcfgInternalIdentity(t *testing.T) {
	got, err := runGoSourceIdentity(t, mustReadSprint165WorkerBuild(t, "internal_sys.go.txt"), "main")
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	want := goSourceOutcome{stdout: fmt.Sprintln(bits.TrailingZeros64(8), bits.Len64(255), bits.OnesCount64(7))}
	if got != want {
		t.Fatalf("Runner %+v; want %+v", got, want)
	}
}

// Negative: a dotted (user) identity is refused at the CHECK by the reviewed
// inventory — the worker is never built, so the refusal is the resolver's
// wording and not a build diagnostic.
func TestGoSourceSprint165WorkerImportcfgDottedIdentityRefusedAtCheck(t *testing.T) {
	for _, identity := range []string{"example.com/app", ""} {
		got, err := runGoSourceIdentity(t, mustReadSprint165WorkerBuild(t, "internal_sys.go.txt"), identity)
		if err == nil {
			t.Fatalf("identity %q: want a refusal, got %+v", identity, got)
		}
		if !strings.Contains(err.Error(), "reviewed Go standard library") || strings.Contains(err.Error(), "build dependency bridge") {
			t.Fatalf("identity %q: want the inventory's refusal before any build, got %v", identity, err)
		}
	}
}

// Positive control: the supported form of the same program (math/bits) runs
// through the importcfg-built worker exactly as native Go runs it.
func TestGoSourceSprint165WorkerImportcfgSupportedForm(t *testing.T) {
	differGoSource(t, mustReadSprint165WorkerBuild(t, "supported_bits.go.txt"), nil, "")
}

// Runtime negative set for the build route: the worker's exit status, its
// stderr and the program's argv survive the importcfg build — wrong-status,
// stray-output and wrong-value would each show as a difference from native.
func TestGoSourceSprint165WorkerImportcfgStatusAndStreams(t *testing.T) {
	differGoSource(t, mustReadSprint165WorkerBuild(t, "status_streams.go.txt"), []string{"alpha", "beta"}, "")
}

// The dependency closure is complete: a worker importing a std package with
// a deep non-std-only closure (net, crypto/tls) links without cmd/go.
func TestGoSourceSprint165WorkerImportcfgDeepClosure(t *testing.T) {
	differGoSource(t, mustReadSprint165WorkerBuild(t, "deep_closure.go.txt"), nil, "")
}
