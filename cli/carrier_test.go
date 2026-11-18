// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// testCleanups run after the whole package's tests, registered by
// platform-specific test files (e.g. removing the built bin/bash).
var testCleanups []func()

// TestMain intercepts job-carrier helper mode first: newRunner wires the
// OS-backed carrier, which re-execs os.Executable() — under `go test` that is
// THIS test binary. Without the interception a carrier re-exec would run the
// entire test suite as its "helper".
func TestMain(m *testing.M) {
	MaybeRunJobCarrierHelper()
	code := m.Run()
	for _, f := range testCleanups {
		f()
	}
	os.Exit(code)
}

// TestUnsupportedJobCarrierFailsClosed pins the no-platform-carrier contract:
// StartCarrier must error (which the runner turns into a failed background
// statement) and never hand back a process.
func TestUnsupportedJobCarrierFailsClosed(t *testing.T) {
	cp, err := unsupportedJobCarrier{}.StartCarrier(context.Background())
	if err == nil {
		t.Fatal("unsupportedJobCarrier.StartCarrier: want error, got nil")
	}
	if cp != nil {
		t.Fatalf("unsupportedJobCarrier.StartCarrier: want nil process, got %#v", cp)
	}
}

// failingTestCarrier models a platform carrier whose startup fails.
type failingTestCarrier struct{}

func (failingTestCarrier) StartCarrier(context.Context) (interp.CarrierProcess, error) {
	return nil, errors.New("carrier down for test")
}

// TestNewRunnerCarrierStartFailureFailsClosed proves the CLI wiring keeps the
// interpreter's fail-closed contract end to end: with the injection seam
// handing newRunner a carrier that cannot start, a background job must not
// run, must not surface a synthetic g<N> in $!, and must fail its statement
// with a diagnostic. (The nonpositive-PID variant of this path is covered by
// the sibling interp unit tests; the seam here is what Bashy contributes.)
func TestNewRunnerCarrierStartFailureFailsClosed(t *testing.T) {
	withStrictPosixEnv(t, "bash", false)
	oldSeam := newCLIJobCarrier
	newCLIJobCarrier = func() interp.JobCarrier { return failingTestCarrier{} }
	t.Cleanup(func() { newCLIJobCarrier = oldSeam })

	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := interp.StdIO(nil, &out, &errOut)(r); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`
{ echo ran; } &
echo "st=$?"
[ -z "$!" ] && echo no-bang
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "ran") {
		t.Fatalf("background job ran despite carrier start failure:\n%s%s", got, errOut.String())
	}
	for _, want := range []string{"st=1", "no-bang"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in output:\n%s%s", want, got, errOut.String())
		}
	}
	if !strings.Contains(errOut.String(), "carrier down for test") {
		t.Fatalf("missing carrier diagnostic on stderr:\n%s", errOut.String())
	}
}

// TestNewRunnerWiresCLIJobCarrier proves newRunner consults the seam at all —
// a recording carrier sees exactly one StartCarrier per background job.
func TestNewRunnerWiresCLIJobCarrier(t *testing.T) {
	withStrictPosixEnv(t, "bash", false)
	calls := 0
	oldSeam := newCLIJobCarrier
	newCLIJobCarrier = func() interp.JobCarrier {
		return carrierFunc(func(ctx context.Context) (interp.CarrierProcess, error) {
			calls++
			return nil, errors.New("counted")
		})
	}
	t.Cleanup(func() { newCLIJobCarrier = oldSeam })

	r, err := newRunner()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := interp.StdIO(nil, &buf, &buf)(r); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader("{ :; } &\n{ :; } &\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	// The script ends on a failed async statement (the counting carrier
	// errors by design), so Run reports that exit status; only a non-status
	// error is unexpected.
	if err := r.Run(context.Background(), file); err != nil {
		var es interp.ExitStatus
		if !errors.As(err, &es) {
			t.Fatalf("Run: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("StartCarrier calls = %d, want 2 (one per background job)", calls)
	}
}

// carrierFunc adapts a func to interp.JobCarrier.
type carrierFunc func(context.Context) (interp.CarrierProcess, error)

func (f carrierFunc) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return f(ctx)
}
