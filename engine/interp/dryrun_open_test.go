package interp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestDryRunOpenHandlerSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	hook := func(ctx context.Context, _ string, _ int, _ os.FileMode) (io.ReadWriteCloser, error) {
		if !HandlerCtx(ctx).DryRun() {
			t.Error("dry-run override called during ordinary execution")
		}
		calls.Add(1)
		return os.OpenFile(os.DevNull, os.O_RDWR, 0)
	}
	r, err := New(Dir(dir), EnableDryRunOption(true), DryRunOpenHandler(hook))
	if err != nil {
		t.Fatal(err)
	}
	run := func(r *Runner, src string) {
		t.Helper()
		f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string, n int) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want || calls.Load() != int32(n) {
			t.Fatalf("file=%q err=%v hook calls=%d; want %q/%d", got, err, calls.Load(), want, n)
		}
	}
	run(r, "printf ignored > target")
	check("original", 1)
	run(r.Subshell(), "printf ignored > target")
	check("original", 2)
	for i, src := range []string{
		"(printf ignored > target)",
		"value=$(printf ignored > target)",
		"printf ignored > target | :",
	} {
		run(r, src)
		check("original", 3+i)
	}
	run(r, "set +o dryrun; printf native > target; set -o dryrun; printf ignored > target")
	check("native", 6)
	run(r, "set +o dryrun")
	r.Reset()
	run(r, "printf ignored > target")
	check("native", 7)
	child := r.Subshell()
	run(child, "set +o dryrun; printf child-native > target")
	check("child-native", 7)
	child.Reset()
	run(child, "printf ignored > target")
	check("child-native", 8)
	run(r, "printf ignored > target")
	check("child-native", 9)
}
