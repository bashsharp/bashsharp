package interp

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecExtraFilesIncludesWriteOnlyFds(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	runner := &Runner{fdWriteTable: map[int]io.Writer{3: w}}
	extra, inherited, cleanup, err := runner.execExtraFiles()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if inherited != "3" {
		t.Fatalf("inherited fds = %q, want 3", inherited)
	}
	if len(extra) != 1 || extra[0] != w {
		t.Fatalf("extra files = %#v, want write fd", extra)
	}
}

func TestExecExtraFilesBridgesWriteOnlyNonFileFds(t *testing.T) {
	var out strings.Builder
	runner := &Runner{fdWriteTable: map[int]io.Writer{3: &out}}
	extra, inherited, cleanup, err := runner.execExtraFiles()
	if err != nil {
		t.Fatal(err)
	}
	if inherited != "3" {
		t.Fatalf("inherited fds = %q, want 3", inherited)
	}
	if len(extra) != 1 {
		t.Fatalf("extra files len = %d, want 1", len(extra))
	}
	if _, err := extra[0].WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := out.String(); got != "hello" {
		t.Fatalf("bridged output = %q, want hello", got)
	}
}
