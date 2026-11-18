// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

// B27a (Sprint 221, story c19cb824e6bd): @timed is the engine-supplied
// observation decorator. Source-derived fixtures, pinned provenance (full
// table in plan-b27-timed-introspection.md):
//   - python/cpython tag v3.12.0, Lib/timeit.py (PSF-2.0): the timer is
//     monotonic (default_timer = time.perf_counter) and the observation wraps
//     the call without changing its outcome; the finally-shaped emission
//     times an erroring call exactly like a success.
//   - this repo commit 53a95043 (BSD-3-Clause, the bashy authors):
//     lower/decorator_lowering_test.go TestDecoratedCallableExecution and
//     interp/bashpp_decorator_test.go — the rung ordering, status and result
//     behavior the observation rung must preserve untouched.

// bashPPTimedLine matches one stable attestation line and captures the
// measured duration, the only run-to-run-variable part of the format.
var bashPPTimedLine = regexp.MustCompile(`^@timed: ([^:]+): status=(\d+) duration=(\S+)\n$`)

// CPython 0fb18b02c8ad56299d6a2910be0bab8ad601ef24 (v3.12.0),
// Lib/test/test_timeit.py TestTimeit.test_timeit_zero_iters and
// test_timeit_few_iters (PSF-2.0): exact iteration counts 0 and 3 and the
// incrementing callable. Bash# times one invocation per decorator entry,
// so preserve call counts and require one measurement per actual call;
// elapsed time is real, not CPython's injected FakeTimer.
func TestBashPPTimedCPythonIterationCounts(t *testing.T) {
	for _, n := range []int{0, 3} {
		src := fmt.Sprintf(`count := 0
@timed()
func inc() { count = count + 1 }
		%s
echo $count
`, strings.Repeat("inc()\n", n))
		out, stderr, err := runDecorated(t, src)
		if err != nil || out != fmt.Sprintf("%d\n", n) {
			t.Fatalf("n=%d out=%q err=%v", n, out, err)
		}
		if got := strings.Count(stderr, "@timed: inc: status=0 duration="); got != n {
			t.Fatalf("n=%d stderr=%q", n, stderr)
		}
	}
}

func TestBashPPTimedDecorator(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@timed()
func greet(name string) {
	echo "hi $name"
}
greet(world)
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "hi world\n"))
		m := bashPPTimedLine.FindStringSubmatch(stderr)
		if m == nil {
			t.Fatalf("stderr %q is not one @timed attestation line", stderr)
		}
		qt.Assert(t, qt.Equals(m[1], "greet"))
		qt.Assert(t, qt.Equals(m[2], "0"))
		d, parseErr := time.ParseDuration(m[3])
		qt.Assert(t, qt.IsNil(parseErr))
		qt.Assert(t, qt.IsTrue(d >= 0))
	})
	t.Run("error status is timed and preserved", func(t *testing.T) {
		// timeit restores state in finally: an erroring call is timed like a
		// success, and the observation changes nothing — $? stays the body's.
		out, stderr, err := runDecorated(t, `@timed()
func failing() {
	false
}
failing()
echo "after=$?"
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "after=1\n"))
		m := bashPPTimedLine.FindStringSubmatch(stderr)
		if m == nil {
			t.Fatalf("stderr %q is not one @timed attestation line", stderr)
		}
		qt.Assert(t, qt.Equals(m[1], "failing"))
		qt.Assert(t, qt.Equals(m[2], "1"))
	})
	t.Run("results pass through unchanged", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@timed()
func double(n int) int {
	return $((n * 2))
}
v := double(21)
echo "v=$v status=$?"
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "v=42 status=0\n"))
		qt.Assert(t, qt.IsTrue(bashPPTimedLine.MatchString(stderr)))
	})
	t.Run("nested order with a user rung", func(t *testing.T) {
		// Repo 53a95043 ordering: outermost first. The user rung wraps the
		// built-in, which observes only what runs inside it; the built-in
		// rewrites nothing the outer rung reads.
		out, stderr, err := runDecorated(t, `func tag(c *Call) {
	echo "in"
	c.Next()
	st := c.Status
	echo "out status=$st"
}
@tag()
@timed()
func f() {
	echo body
}
f()
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "in\nbody\nout status=0\n"))
		qt.Assert(t, qt.IsTrue(bashPPTimedLine.MatchString(stderr)))
	})
	t.Run("stacked timed measures at both depths", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@timed()
@timed()
func f() {
	echo body
}
f()
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "body\n"))
		lines := strings.SplitAfter(stderr, "\n")
		qt.Assert(t, qt.Equals(len(lines), 3)) // two attestation lines + trailing empty split
		for _, line := range lines[:2] {
			m := bashPPTimedLine.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("line %q is not one @timed attestation line", line)
			}
			qt.Assert(t, qt.Equals(m[1], "f"))
			qt.Assert(t, qt.Equals(m[2], "0"))
		}
	})
}

func TestBashPPTimedDecoratorResolution(t *testing.T) {
	t.Run("script decorator shadows the built-in", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `func timed(c *Call) {
	echo custom
	c.Next()
}
@timed()
func f() {
	echo body
}
f()
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "custom\nbody\n"))
		qt.Assert(t, qt.Equals(stderr, ""))
	})
	t.Run("embedder native overrides the built-in", func(t *testing.T) {
		ran := false
		natives := map[string]interp.DecoratorFunc{
			"timed": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
				ran = true
				c.Next(ctx)
				return nil
			},
		}
		out, stderr, err := runDecorated(t, `@timed()
func f() {
	echo body
}
f()
`, interp.Decorators(natives))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "body\n"))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.IsTrue(ran))
	})
	t.Run("shell function named timed stays EDECO-SIG", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `timed() { :; }
@timed()
func f() { echo leaked }
f()
`)
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EDECO-SIG")))
		qt.Assert(t, qt.IsFalse(strings.Contains(out, "leaked")))
	})
	t.Run("arguments are refused", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@timed(1)
func f() { echo leaked }
f()
echo "status=$?"
`)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "status=1\n"))
		qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EDECO-NATIVE: @timed: requires no arguments")))
		qt.Assert(t, qt.IsFalse(strings.Contains(out, "leaked")))
	})
}
