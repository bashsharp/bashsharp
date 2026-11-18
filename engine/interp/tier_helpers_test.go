// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

// Test helpers shared by the quick tier (default build) and the full tier
// (`-tags full`): the files that define the full-tier tests are excluded from
// the push gate, so anything an untagged test file also needs lives here.

package interp

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// From bashpp_import_resolution_test.go.

func writeImportFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	name = filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativeResolveRequest(dir string, env ...string) bashPPEvalRequest {
	base := os.Environ()
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		base = setEnvString(base, name, value)
	}
	return bashPPEvalRequest{
		Go: filepath.Join(runtime.GOROOT(), "bin", "go"), Dir: dir, Env: base,
		Stdout: io.Discard, Stderr: io.Discard,
	}
}

// From bashpp_import_internal_test.go.

type recordingBashPPEval struct {
	mu       sync.Mutex
	resolved map[string]string
	calls    []bashPPEvalRequest
	values   []any
	err      error
}

func (e *recordingBashPPEval) Resolve(ctx context.Context, req bashPPEvalRequest, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resolved[path], e.err
}

func (e *recordingBashPPEval) Call(ctx context.Context, req bashPPEvalRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	req.Imports = maps.Clone(req.Imports)
	e.calls = append(e.calls, req)
	return e.err
}

func (e *recordingBashPPEval) Values(ctx context.Context, req bashPPEvalRequest) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.values == nil {
		return nil, errors.New("bash++: selected evaluator cannot return object values")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	req.Imports = maps.Clone(req.Imports)
	e.calls = append(e.calls, req)
	return e.values, e.err
}

func parseBashPPInternal(t *testing.T, src string) *syntax.File {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "test.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newInjectedBashPPRunner(t *testing.T, eval bashPPEvaluator) *Runner {
	t.Helper()
	r, err := New(StdIO(nil, io.Discard, io.Discard), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.bashPPTools = bashPPToolchain{goBinary: "/reviewed/go", eval: eval}
	return r
}

// From bashpp_native_functions_internal_test.go.

type callbackProbeWriter func([]byte) (int, error)

func (w callbackProbeWriter) Write(p []byte) (int, error) { return w(p) }
