// Copyright (c) 2025, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestDryRunOption covers the non-POSIX `set -o dryrun` extension
// ([EnableDryRunOption] + [HandlerContext.DryRun]): recognized only when
// enabled, toggles at runtime, survives the lazy first Reset, restores its
// initial state on an explicit Reset, and is rejected like Bash when off.
func TestDryRunOption(t *testing.T) {
	newRunner := func(seen *[]string, opts ...RunnerOption) *Runner {
		mw := func(next ExecHandlerFunc) ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if HandlerCtx(ctx).DryRun() {
					*seen = append(*seen, "DRY:"+args[0])
				} else {
					*seen = append(*seen, "RUN:"+args[0])
				}
				return nil
			}
		}
		r, err := New(append(opts, ExecHandlers(mw))...)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	exec := func(r *Runner, src string) {
		f, _ := syntax.NewParser().Parse(strings.NewReader(src), "")
		_ = r.Run(context.Background(), f)
	}

	// Enabled: `set -o dryrun` toggles (and the option survives the first
	// lazy Reset triggered by Run — without the Reset fix this would error).
	var a []string
	exec(newRunner(&a, EnableDryRunOption(false)), "set -o dryrun; ext1; set +o dryrun; ext2")
	if got := strings.Join(a, ","); got != "DRY:ext1,RUN:ext2" {
		t.Errorf("toggle: got %q", got)
	}

	// Explicit Reset restores the initial (on) state.
	var b []string
	r := newRunner(&b, EnableDryRunOption(true))
	exec(r, "set +o dryrun; ext1") // turn it off mid-run
	r.Reset()
	exec(r, "ext2") // back to the initial dry-run state
	if got := strings.Join(b, ","); got != "RUN:ext1,DRY:ext2" {
		t.Errorf("reset-restore: got %q", got)
	}

	// Not enabled: `set -o dryrun` is rejected like Bash, so ext1 runs.
	var c []string
	exec(newRunner(&c), "set -o dryrun; ext1")
	if got := strings.Join(c, ","); got != "RUN:ext1" {
		t.Errorf("not-enabled: got %q (dryrun should be unknown)", got)
	}
}
