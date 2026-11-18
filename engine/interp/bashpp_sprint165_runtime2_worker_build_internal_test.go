package interp

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// The worker's importcfg build shells out to `go list`, `go tool compile`
// and `go tool link` (bashpp_sprint165_runtime2_worker_build.go). When the
// caller's context expires mid-run, exec.CommandContext kills the child and
// Run returns a bare "signal: killed" — errors.Is(err, ctx.Err()) is false,
// so the real reason (a timeout or cancellation) never reaches the caller;
// only an opaque kill notice does, indistinguishable from the process being
// killed for any other reason (e.g. the OOM killer during a `go tool
// compile` of a large dependency closure). bashPPWorkerImportPaths returns a
// plain wrapped parser error, so it does not carry this class of bug: it
// never runs a subprocess.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeSleepyFakeGo writes a shell script standing in for the "go" binary:
// it sleeps long enough to be killed once its argv[1] is in sleepOn, and
// otherwise exits immediately with no output.
func writeSleepyFakeGo(t *testing.T, sleepOn string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell script standing in for go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fakego")
	script := "#!/bin/sh\nif [ \"$1\" = \"" + sleepOn + "\" ]; then sleep 2; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBashPPWorkerImportcfgContextDeadlineNotLost covers the `go list
// -export -deps` step directly: killed by an expiring context, the caller
// must see the context's error, not the exec package's opaque exit status.
func TestBashPPWorkerImportcfgContextDeadlineNotLost(t *testing.T) {
	goBinary := writeSleepyFakeGo(t, "list")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := bashPPWorkerImportcfg(ctx, goBinary, t.TempDir(), os.Environ(), "", []string{"runtime"})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error lost the context deadline as its cause: %v", err)
	}
}

// TestBashPPBuildWorkerImportcfgContextDeadlineNotLost covers the `go tool
// compile` / `go tool link` step: `go list` returns instantly, so the
// context only expires once the (simulated, killed) compile is under way.
func TestBashPPBuildWorkerImportcfgContextDeadlineNotLost(t *testing.T) {
	goBinary := writeSleepyFakeGo(t, "tool")
	dir := t.TempDir()
	source := filepath.Join(dir, "worker.go")
	if err := os.WriteFile(source, []byte("package main\n\nimport \"runtime\"\n\nfunc main() { _ = runtime.GOOS }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := bashPPBuildWorkerImportcfg(ctx, goBinary, dir, os.Environ(), "", dir, source, filepath.Join(dir, "worker"), nil, "")
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error lost the context deadline as its cause: %v", err)
	}
}
