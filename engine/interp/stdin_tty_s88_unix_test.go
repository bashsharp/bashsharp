//go:build unix

package interp

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// TestS88InteractiveCommandPreservesTTYStdin is a public reducer for a login
// session running a foreground provider such as mesg. Interactive source text
// may come from fd 0, but a child command must still inherit the session's tty
// as fd 0; the noninteractive stdin-script tail is not a substitute terminal.
func TestS88InteractiveCommandPreservesTTYStdin(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		t.Run(map[bool]string{true: "interactive", false: "noninteractive-login-reader"}[interactive], func(t *testing.T) {
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer ptmx.Close()
			defer tty.Close()

			const src = "public-tty-probe\n"
			file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var childStdin io.Reader
			capture := func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					childStdin = HandlerCtx(ctx).Stdin
					return nil
				}
			}
			r, err := New(
				Interactive(interactive),
				StdIO(tty, io.Discard, io.Discard),
				WithBashSource([]byte(src)),
				ExecHandlers(capture),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			f, ok := childStdin.(interface{ Fd() uintptr })
			if !ok || !term.IsTerminal(int(f.Fd())) {
				t.Fatalf("foreground child stdin = %T, want terminal fd", childStdin)
			}
		})
	}
}

// TestFcWriteS88PreservesExplicitStdinForChildCommands is a suite-free public
// reducer for the fc:22 and write:22 boundary: an explicit runner stdin stream
// (such as a pipe carrying editor commands or message lines) must be preserved
// for child processes rather than being overwritten by scriptStdinReader.
func TestFcWriteS88PreservesExplicitStdinForChildCommands(t *testing.T) {
	dataPipeReader, dataPipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer dataPipeReader.Close()
	defer dataPipeWriter.Close()

	go func() {
		_, _ = dataPipeWriter.WriteString("payload line\n")
		_ = dataPipeWriter.Close()
	}()

	const src = "public-child-probe\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}

	var childStdin io.Reader
	capture := func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			childStdin = HandlerCtx(ctx).Stdin
			return nil
		}
	}

	r, err := New(
		StdIO(dataPipeReader, io.Discard, io.Discard),
		WithBashSource([]byte(src)),
		ExecHandlers(capture),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}

	if _, ok := childStdin.(*scriptStdinReader); ok {
		t.Fatalf("child stdin was overwritten by scriptStdinReader, want explicit data pipe")
	}
	if childStdin != dataPipeReader {
		t.Fatalf("child stdin = %T %v, want explicit data pipe reader %v", childStdin, childStdin, dataPipeReader)
	}
}
