// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const fifoReadWriteHelperEnv = "BASHY_TEST_FIFO_READWRITE_HELPER"

// TestReadWriteFIFORedirect covers Oils redirect__029: one <> descriptor must
// retain both sides of a FIFO so bytes written through it can be read back.
// Run the exact script in a subprocess so a regression is a bounded test
// failure rather than a goroutine blocked forever in the test process.
func TestReadWriteFIFORedirect(t *testing.T) {
	if fifo := os.Getenv(fifoReadWriteHelperEnv); fifo != "" {
		runReadWriteFIFOHelper(t, fifo)
		return
	}

	fifo := filepath.Join(t.TempDir(), "f.pipe")
	if err := mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, testBin, "-test.run=^TestReadWriteFIFORedirect$")
	// TestMain uses GOSH_PROG to re-exec arbitrary shell snippets. Do not
	// inherit it here: this child must enter the ordinary Go test path.
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
		fifoReadWriteHelperEnv + "=" + fifo,
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("<> FIFO script did not complete: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("<> FIFO helper: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("line1=first line2=second\n")) {
		t.Fatalf("<> FIFO output missing round trip: %q", out)
	}
}

func runReadWriteFIFOHelper(t *testing.T, fifo string) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(`
exec 8<> "$1"
echo first >&8
echo second >&8
read line1 <&8
read line2 <&8
exec 8<&-
echo line1=$line1 line2=$line2
`), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer // bashpp-racegate:safe-private
	runner, err := New(
		Params("--", fifo),
		StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	_, _ = os.Stdout.Write(stdout.Bytes())
}
