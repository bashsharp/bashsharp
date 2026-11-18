//go:build unix

package interp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestDryRunOpenHandlerInactiveFIFORendezvous(t *testing.T) {
	c := newBashPPConcurrent(context.Background())
	defer c.cancel()
	defer c.closeFIFOs(nil)
	reader, writer := newFIFOTestTask(t, c), newFIFOTestTask(t, c)
	var calls atomic.Int32
	hook := DryRunOpenHandler(func(context.Context, string, int, os.FileMode) (io.ReadWriteCloser, error) {
		calls.Add(1)
		return nil, errors.New("inactive override called")
	})
	for _, r := range []*Runner{reader, writer} {
		if err := hook(r); err != nil {
			t.Fatal(err)
		}
	}
	path := fifoTestPath(t)
	first := startFIFOTestOpen(reader, path, os.O_RDONLY)
	<-reader.bashPPTaskState.ready
	second := startFIFOTestOpen(writer, path, os.O_WRONLY)
	a, b := awaitFIFOTestOpen(t, first), awaitFIFOTestOpen(t, second)
	if a.err != nil || b.err != nil {
		t.Fatalf("reader=%v writer=%v", a.err, b.err)
	}
	defer reader.bashPPFIFOCloser(a.file).Close()
	if _, err := io.WriteString(b.file, "native\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.bashPPFIFOCloser(b.file).Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(a.file)
	if err != nil || string(data) != "native\n" || calls.Load() != 0 {
		t.Fatalf("data=%q err=%v calls=%d", data, err, calls.Load())
	}
}

func TestDryRunOpenHandlerTaskBoundary(t *testing.T) {
	for _, tc := range []struct {
		name               string
		initial            bool
		prefix, taskPrefix string
		custom, refused    bool
		clearOverride      bool
	}{
		{name: "normal"},
		{name: "initial-dryrun", initial: true, refused: true},
		{name: "runtime-dryrun", prefix: "set -o dryrun;", refused: true},
		{name: "task-local-dryrun", taskPrefix: "set -o dryrun;", refused: true},
		{name: "runtime-normal", initial: true, prefix: "set +o dryrun;"},
		{name: "task-local-normal", initial: true, taskPrefix: "set +o dryrun;"},
		{name: "task-local-normal-keeps-custom", initial: true, taskPrefix: "set +o dryrun;", custom: true, refused: true},
		{name: "ordinary-custom-still-refused", custom: true, refused: true},
		{name: "nil-override-keeps-custom", custom: true, clearOverride: true, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			hook := func(context.Context, string, int, os.FileMode) (io.ReadWriteCloser, error) {
				calls.Add(1)
				return nil, errors.New("unexpected custom open")
			}
			var diagnostic strings.Builder
			opts := []RunnerOption{Lang(syntax.LangBashPP), Dir(dir), StdIO(nil, io.Discard, &diagnostic), EnableDryRunOption(tc.initial), DryRunOpenHandler(hook)}
			if tc.custom {
				opts = append(opts, OpenHandler(hook))
			}
			if tc.clearOverride {
				opts = append(opts, DryRunOpenHandler(nil))
			}
			r, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			src := tc.prefix + `func write(done) { ` + tc.taskPrefix + `printf native > target || return $?; done <- true; }
 done := make(chan bool)
 go write(done)
 completed := <-done
 `
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), f)
			if tc.refused {
				if err == nil || !strings.Contains(diagnostic.String(), "custom open handlers are unavailable inside a Bash++ task") {
					t.Fatalf("err=%v diagnostic=%q", err, diagnostic.String())
				}
			} else if err != nil {
				t.Fatalf("err=%v diagnostic=%q", err, diagnostic.String())
			}
			want := "native"
			if tc.refused {
				want = "original"
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != want || calls.Load() != 0 {
				t.Fatalf("file=%q err=%v calls=%d; want %q/zero", got, readErr, calls.Load(), want)
			}
		})
	}
}
