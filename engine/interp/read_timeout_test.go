// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestReadTimeoutRequiresFailedRead(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "complete line beats expired context", err: nil, want: false},
		{name: "failed read observes expired context", err: io.EOF, want: true},
		{name: "deadline error", err: os.ErrDeadlineExceeded, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readErrorIsTimeout(ctx, tc.err, time.Millisecond); got != tc.want {
				t.Fatalf("readErrorIsTimeout() = %v, want %v", got, tc.want)
			}
		})
	}

	if readErrorIsTimeout(ctx, errors.New("read failed"), 0) {
		t.Fatal("a read without -t must not be classified as a timeout")
	}
}

func TestReadLineFromLetsTimeoutReaderProbeExpiredContext(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "timed-read")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("abcde\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	input := timeoutReader(ctx, f, time.Now().Add(-time.Second))
	if input == nil {
		t.Skip("platform has no poll-based timeout reader")
	}
	line, err := (&Runner{}).readLineFrom(ctx, input, false, '\n')
	if err != nil {
		t.Fatalf("ready input should beat the expired timeout: %v", err)
	}
	if got := string(line); got != "abcde" {
		t.Fatalf("readLineFrom() = %q, want %q", got, "abcde")
	}
}
