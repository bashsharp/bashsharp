//go:build full

package interp_test

// Sprint: #153; Story: S153.4; Story-ID: e58cccba74f8
//
// Dependency-bridge value shapes. Every case is an outside-corpus reproducer
// under testdata/sprint153/<mechanism>/: the unchanged original source runs
// through the interpreter and is compared against a real Go build of the same
// file on exact stdout, stderr and exit status. differGoSource lives in
// bashpp_native_structured_test.go.

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

// differSprint153 runs every .go program in one mechanism directory through
// the native-Go differ.
func differSprint153(t *testing.T, mechanism string) {
	t.Helper()
	dir := filepath.Join("testdata", "sprint153", mechanism)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		ran++
		source, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(entry.Name(), func(t *testing.T) { differGoSource(t, string(source), nil, "") })
	}
	if ran == 0 {
		t.Fatalf("no reproducers in %s", dir)
	}
}

// TestGoSourceBridgeLocalInterfaceDecl covers locally declared interfaces with
// method specifications: the helper materialises them as real declarations, so
// a struct field of that interface type no longer breaks the worker build.
func TestGoSourceBridgeLocalInterfaceDecl(t *testing.T) {
	differSprint153(t, "local-interface-decl")
}

// TestGoSourceBridgeLocalArrayLength covers local array types whose length is
// a constant name or expression: the helper refuses to materialise them (it
// never sees the original constant declarations) instead of emitting an
// uncompilable declaration; a literal length keeps materialising.
func TestGoSourceBridgeLocalArrayLength(t *testing.T) {
	differSprint153(t, "local-array-length")
}

// TestGoSourceBridgeLocalTypeClosure covers the dependency closure of the
// materialised set: a declaration naming an unmaterialisable local type — and
// anything that in turn names it — stays out of the helper.
func TestGoSourceBridgeLocalTypeClosure(t *testing.T) {
	differSprint153(t, "local-type-closure")
}

// TestGoSourceBridgeArrayValueTransport covers values of array types the
// helper does not materialise: they cross under their realised structural
// spelling ([3]int) instead of an unregistered name.
func TestGoSourceBridgeArrayValueTransport(t *testing.T) {
	differSprint153(t, "array-value-transport")
}

// TestGoSourceBridgeSliceReconcile covers the reconciled-buffer mechanism:
// direct original slice arguments cross as registered buffers and the
// observed call behaviour picks the class — read-only consumers leave the
// storage untouched, in-place mutators are written back by visible length.
func TestGoSourceBridgeSliceReconcile(t *testing.T) {
	differSprint153(t, "slice-reconcile")
}

// TestGoSourceBridgeSliceRetainedRefusal proves the remaining class is
// refused, not hung: slice storage the buffer writeback cannot reach — here
// nested inside another slice — fails fast with the retention message.
func TestGoSourceBridgeSliceRetainedRefusal(t *testing.T) {
	path := filepath.Join("testdata", "sprint153", "slice-reconcile", "retained_refusal.go.txt")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
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
		t.Fatal("retained-slice refusal timed out instead of failing fast")
	}
	if err == nil || !strings.Contains(err.Error(), "native slice retention or mutation is unsupported") {
		t.Fatalf("want retention refusal, got err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

// TestGoSourceBridgeGenericInstantiation covers instantiated local generic
// types: the helper materialises each instantiation under a generated name
// (the generic body with type arguments substituted), registers it under the
// instantiation spelling so transported values resolve, and its mirrored
// method stubs call back through the base generic name.
func TestGoSourceBridgeGenericInstantiation(t *testing.T) {
	differSprint153(t, "generic-instantiation")
}
