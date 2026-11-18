//go:build linux

package interp_test

import (
	"bytes"
	"context"
	"os"
	"runtime/debug"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestRunnerRedirectedFunctionExecDoesNotLeakFDs(t *testing.T) {
	countFDs := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}

	dir := t.TempDir()
	for _, name := range []string{"inner", "boundary"} {
		if err := os.WriteFile(dir+"/"+name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := syntax.NewParser().Parse(bytes.NewBufferString(`
		redirect() { exec >inner; }
		i=0
		while test "$i" -lt 256; do
			redirect >boundary
			i=$((i + 1))
		done
	`), "function-fd-leak")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}

	// Make skipped Close calls observable instead of allowing os.File
	// finalizers to hide them during the repeated function calls.
	oldGCPercent := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGCPercent)
	before := countFDs()
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	after := countFDs()
	if delta := after - before; delta > 4 {
		t.Fatalf("redirected function calls leaked %d descriptors", delta)
	}
}
