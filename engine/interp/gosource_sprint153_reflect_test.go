//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Type-only reflect transport. reflect.TypeOf never invokes its argument, so
// an original function crosses as a bare type descriptor and a method-bearing
// original value crosses as read-only structure; neither wires a callback and
// the returned descriptor is not callback-bearing. Reproducers live under
// testdata/sprint153/reflect-type-only/; differSprint153 is in
// gosource_sprint153_bridge_test.go.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceBridgeReflectTypeOnly covers reflect.TypeOf over original
// functions and method-bearing original types, with a plain local struct as
// the already-supported positive control.
func TestGoSourceBridgeReflectTypeOnly(t *testing.T) {
	differSprint153(t, "reflect-type-only")
}

// TestGoSourceBridgeReflectRefusals proves the classes that stay outside the
// type-only transport are refused, not hung: reflect.ValueOf over a
// method-bearing value whose copy shares a map with the caller, and
// testing.AllocsPerRun, which would measure the callback trampoline's own
// child allocations as the program's observable. (A plain-data value of a
// method-bearing type is admitted since Sprints 219/248; see
// TestS248ReflectValueOfTemporary.)
func TestGoSourceBridgeReflectRefusals(t *testing.T) {
	cases := map[string]string{
		"valueof_reference_refused.go.txt": "dependency mutation of interpreter-owned references is unsupported",
		"allocsperrun_refused.go.txt":      "asynchronous or retained original function callbacks are unsupported",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			refuseSprint153(t, "reflect-type-only", name, want)
		})
	}
}

// refuseSprint153 runs one reproducer expected to fail fast with a specific
// fail-closed diagnostic rather than hang or silently misbehave.
func refuseSprint153(t *testing.T, mechanism, name, want string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "sprint153", mechanism, name))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), name, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	if ctx.Err() != nil {
		t.Fatalf("%s timed out instead of failing fast", name)
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want refusal %q, got err=%v stdout=%q stderr=%q", want, err, stdout.String(), stderr.String())
	}
}
