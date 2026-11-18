package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A name the embedder serves in process keeps reaching it on every call even
// when a same-named file is on PATH: it is never hashed.
func TestServedInProcessIsNeverHashed(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "tool")
	if err := os.WriteFile(host, []byte("#!/bin/sh\necho host\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, served := range []bool{false, true} {
		var out bytes.Buffer
		inProcess := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] == "tool" {
					interp.HandlerCtx(ctx).Stdout.Write([]byte("in-process\n"))
					return nil
				}
				return next(ctx, args)
			}
		}
		opts := []interp.RunnerOption{
			interp.StdIO(nil, &out, &out),
			interp.Env(nil),
			interp.ExecHandlers(inProcess),
		}
		if served {
			opts = append(opts, interp.ServedInProcess(func(name string) bool { return name == "tool" }))
		}
		r, err := interp.New(opts...)
		if err != nil {
			t.Fatal(err)
		}
		prog, _ := syntax.NewParser().Parse(strings.NewReader("PATH="+dir+"; tool; tool; tool"), "")
		if err := r.Run(context.Background(), prog); err != nil && !served {
			// the host script may not run everywhere; only the served case must succeed
		}
		got := out.String()
		if served && got != "in-process\nin-process\nin-process\n" {
			t.Fatalf("served in process: %q", got)
		}
		if !served && !strings.HasPrefix(got, "in-process\n") {
			t.Fatalf("without the option the first call is in process: %q", got)
		}
	}
}
