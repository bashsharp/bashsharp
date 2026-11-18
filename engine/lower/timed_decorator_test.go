//go:build full

package lower_test

import (
	"regexp"
	"testing"
)

// B27a (Sprint 221, story c19cb824e6bd): `@timed` is the engine-supplied
// observation decorator — an ordinary rung of the existing chain, resolved
// only after every user-owned slot misses, that times one Next and appends
// one stable attestation line to stderr. Every case is compiled, built,
// executed, and diffed against the interpreter running the same source with
// only the measured `duration=` value canonicalized, so each row is a
// measured interpreted/lowered parity claim on the stable part of the format.
//
// Source-derived fixtures, pinned provenance (full table in
// plan-b27-timed-introspection.md):
//   - python/cpython tag v3.12.0, Lib/timeit.py (PSF-2.0): monotonic timer;
//     the observation wraps the statement without changing its outcome, and
//     the finally-shaped emission times an erroring call like a success.
//   - this repo commit 53a95043 (BSD-3-Clause, the bashy authors):
//     lower/decorator_lowering_test.go TestDecoratedCallableExecution and
//     lower/goerror_decorator_test.go — the ordering, status and settle
//     behavior the observation rung must preserve untouched.

// timedDuration canonicalizes the one legitimately nondeterministic token of
// the attestation line before the parity diff.
var timedDuration = regexp.MustCompile(`(@timed: [^:]+: status=\d+ duration=)\S+`)

func normalizeTimed(s string) string {
	return timedDuration.ReplaceAllString(s, "${1}X")
}

func TestTimedDecorator(t *testing.T) {
	for _, tc := range []struct{ name, src, out, err string }{
		{
			// timeit: the observation wraps the call without changing its
			// outcome — stdout, results and status are the untimed call's.
			"success_timing",
			`@timed()
func greet(name string) {
	echo "hi $name"
}
greet(world)
echo "status=$?"
`,
			"hi world\nstatus=0\n",
			"@timed: greet: status=0 duration=X\n",
		},
		{
			// timeit restores state in finally: an erroring call is timed
			// exactly like a success, and $? stays the body's own status.
			"error_timing",
			`@timed()
func failing() {
	false
}
failing()
echo "after=$?"
`,
			"after=1\n",
			"@timed: failing: status=1 duration=X\n",
		},
		{
			// Results pass through the observation rung unchanged.
			"value_passthrough",
			`@timed()
func double(n int) int {
	return $((n * 2))
}
v := double(21)
echo "v=$v"
`,
			"v=42\n",
			"@timed: double: status=0 duration=X\n",
		},
		{
			// Repo 53a95043 ordering (outermost first): a user rung outside
			// @timed sees exactly what it sees around an untimed call.
			"nested_user_rung",
			`func tag(c *Call) {
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
`,
			"in\nbody\nout status=0\n",
			"@timed: f: status=0 duration=X\n",
		},
		{
			// Stacked @timed measures at both depths: the inner rung settles
			// first, so its line lands first in both engines.
			"stacked_timed",
			`@timed()
@timed()
func f() {
	echo body
}
f()
`,
			"body\n",
			"@timed: f: status=0 duration=X\n@timed: f: status=0 duration=X\n",
		},
		{
			// Composition with the B23 adapter: the observation rung sees the
			// body's settled status, and the (T, error) conversion still runs
			// after the chain settles.
			"goerror_composition",
			`func mark(c *Call) {
	c.Next()
	c.Status = 3
}
@timed()
@mark()
@go.error()
func deploy() int {
	return 7
}
v, err := deploy()
echo "v=$v err=[$err] status=$?"
`,
			"v=7 err=[deploy: exit status 3] status=3\n",
			"@timed: deploy: status=3 duration=X\n",
		},
		{
			// A source-declared decorator named timed shadows the built-in in
			// both engines; no attestation line exists to normalize.
			"shadowed_by_script",
			`func timed(c *Call) {
	echo custom
	c.Next()
}
@timed()
func f() {
	echo body
}
f()
`,
			"custom\nbody\n",
			"",
		},
		{
			// The built-in takes no arguments; the refusal is the existing
			// native-rung diagnostic in both engines.
			"arguments_refused",
			`@timed(1)
func f() { echo leaked }
f()
echo "status=$?"
`,
			"status=1\n",
			"BASHPP-EDECO-NATIVE: @timed: requires no arguments\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr := executeBuildNormalized(t, compile(t, tc.src), normalizeTimed)
			if out != tc.out || stderr != tc.err {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q stderr=%q", out, stderr, tc.out, tc.err)
			}
		})
	}
}
