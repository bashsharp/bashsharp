//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Dependency-bridge writers. An original pointer to a native handle — &b of a
// bytes.Buffer, new(bytes.Buffer) — aliases the handle's own storage in the
// worker, so fmt.Fprint* writes land in the value later reads observe.
// Reproducers live under testdata/sprint153/writer-proxy/; differSprint153 is
// in gosource_sprint153_bridge_test.go.

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

// TestGoSourceBridgeWriterAlias covers fmt.Fprint/Fprintln/Fprintf into a
// dependency-owned writer reached through an original pointer, alongside the
// already-supported native stream writers as the positive control.
func TestGoSourceBridgeWriterAlias(t *testing.T) {
	differSprint153(t, "writer-proxy")
}

// TestGoSourceBridgeLocalWriter proves an original writer the dependency does
// not own is executed by the interpreter rather than hung or silently written
// through a detached native copy.
func TestGoSourceBridgeLocalWriter(t *testing.T) {
	path := filepath.Join("testdata", "sprint153", "writer-proxy", "local_writer_refused.go.txt")
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
		t.Fatal("original writer timed out")
	}
	if err != nil || stdout.String() != "4\n" || stderr.Len() != 0 {
		t.Fatalf("run err=%v stdout=%q stderr=%q, want interpreted writer result", err, stdout.String(), stderr.String())
	}
}
