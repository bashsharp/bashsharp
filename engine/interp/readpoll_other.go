// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import (
	"context"
	"io"
	"os"
	"time"
)

// timeoutReader returns nil on non-unix, signalling the caller to fall back
// to (*os.File).SetReadDeadline, which is the reliable mechanism there
// (e.g. Windows console/pipe handles).
func timeoutReader(context.Context, *os.File, time.Time) io.Reader { return nil }

func signalReader(context.Context, *os.File, <-chan struct{}) io.Reader { return nil }

func taskReadReader(context.Context, *os.File, time.Time, <-chan struct{}, func() bool) io.Reader {
	return nil
}

func fdReadableNow(f *os.File) bool {
	return false
}

func taskReadReadyNow(*os.File) bool { return false }

type timeoutFileReader struct {
	ctx      context.Context
	file     *os.File
	deadline time.Time
}

func (r *timeoutFileReader) Read(p []byte) (int, error) {
	return r.file.Read(p)
}

// cancellableReader has nothing to add here: every descriptor this shell
// reads from is registered with the runtime poller, so the caller's
// SetReadDeadline already calls a blocked read off.
func cancellableReader(context.Context, *os.File) io.Reader { return nil }
