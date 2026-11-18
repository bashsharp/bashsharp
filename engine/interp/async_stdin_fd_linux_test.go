// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package interp

import (
	"context"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestAsyncStdinDescriptorClosed(t *testing.T) {
	// An unclosed *os.File has a finalizer; disabling GC makes the
	// descriptor ownership assertion deterministic.
	runtime.GC()
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)
	count := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := count()
	r, err := New(StdIO(nil, io.Discard, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(strings.Repeat(": & wait\n", 200)), "async-fds")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if growth := count() - before; growth > 8 {
		t.Fatalf("asynchronous lists leaked %d file descriptors", growth)
	}
}
