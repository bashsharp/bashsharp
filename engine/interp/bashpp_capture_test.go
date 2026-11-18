// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Tests for the inbound half of the typed process boundary: `run`, `capture`
// and the explicit `json.Decode`. These are internal tests on purpose — the
// contract being pinned is about variable KINDS (a captured string must be a
// string, never an inferred object), and only this package can see a
// variable's kind directly.

func captureRunner(t *testing.T, stderr *strings.Builder, opts ...RunnerOption) *Runner { // bashpp-racegate:safe-synchronized
	t.Helper()
	all := append([]RunnerOption{
		StdIO(nil, stderr, stderr),
		Env(expand.ListEnviron("PATH=/usr/bin:/bin")),
		Lang(syntax.LangBashPP),
	}, opts...)
	r, err := New(all...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func captureRun(t *testing.T, r *Runner, src string) error {
	t.Helper()
	return r.Run(context.Background(), parseBashPPInternal(t, src))
}

func stringVar(t *testing.T, r *Runner, name string) string {
	t.Helper()
	vr := r.lookupVar(name)
	if !vr.IsSet() {
		t.Fatalf("%s is not set", name)
	}
	if vr.Kind != expand.String {
		t.Fatalf("%s kind = %v, want expand.String", name, vr.Kind)
	}
	return vr.Str
}

func TestBashPPRunSeparatesThreeChannels(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, "f() { printf out-data; printf err-data >&2; return 3; }\nres, err := run(f)\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "err"); got != "" {
		t.Fatalf("err = %q, want empty: a command that ran to completion is not a failed capture", got)
	}
	vr := r.lookupVar("res")
	if vr.Kind != expand.Object {
		t.Fatalf("res kind = %v, want expand.Object", vr.Kind)
	}
	obj, ok := vr.Obj.(map[string]any)
	if !ok {
		t.Fatalf("res value = %#v, want a map", vr.Obj)
	}
	// Three DISTINCT channels: stdout, stderr and status never mix.
	if got := obj["Stdout"]; got != "out-data" {
		t.Fatalf("Stdout = %#v, want %q", got, "out-data")
	}
	if got := obj["Stderr"]; got != "err-data" {
		t.Fatalf("Stderr = %#v, want %q", got, "err-data")
	}
	if got := obj["Status"]; got != 3 {
		t.Fatalf("Status = %#v, want 3", got)
	}
	// Nothing the child wrote leaked to the shell's own streams.
	if out.Len() != 0 {
		t.Fatalf("shell stderr/stdout got %q, want nothing", out.String())
	}
}

func TestBashPPRunResultInterpolatesAndResolves(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, `f() { printf hi; return 5; }
res, err := run(f)
printf '%s|' res.Stdout res.Status
printf '%s' "$res"
`)
	if err != nil {
		t.Fatal(err)
	}
	// Field access rides the existing object-path machinery; whole-value
	// interpolation is the deterministic JSON coercion (sorted map keys).
	want := `hi|5|{"Status":5,"Stderr":"","Stdout":"hi"}`
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// TestBashPPCaptureNeverInfersJSON is the rule that is the whole point: a
// command whose stdout happens to be valid JSON, captured WITHOUT a decode
// call, is still a plain string. If someone later adds inference — decoding
// output merely because it parses — this test fails on the variable's kind.
func TestBashPPCaptureNeverInfersJSON(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, "f() { printf '{\"a\":1}\\n'; }\nout, err := capture(f)\n")
	if err != nil {
		t.Fatal(err)
	}
	vr := r.lookupVar("out")
	if vr.Kind != expand.String {
		t.Fatalf("captured valid JSON has kind %v, want expand.String: decoding must be EXPLICIT, never inferred", vr.Kind)
	}
	// Byte-exact, including the trailing newline: the typed boundary does not
	// strip newlines the way `$(...)` does, and it does not reformat.
	if vr.Str != "{\"a\":1}\n" {
		t.Fatalf("out = %q, want the exact bytes %q", vr.Str, "{\"a\":1}\n")
	}
	if got := stringVar(t, r, "err"); got != "" {
		t.Fatalf("err = %q, want empty", got)
	}
}

func TestBashPPCaptureFailureIsNotEmptySuccess(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, "f() { printf partial; return 7; }\nout, err := capture(f)\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "out"); got != "partial" {
		t.Fatalf("out = %q, want %q", got, "partial")
	}
	if got := stringVar(t, r, "err"); got != "capture: f: exit status 7" {
		t.Fatalf("err = %q, want the status named", got)
	}

	// Command not found is a failure too, never a successful empty capture.
	if err := captureRun(t, r, "o2, e2 := capture(bashpp_no_such_cmd_xyz)\n"); err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "o2"); got != "" {
		t.Fatalf("o2 = %q, want empty", got)
	}
	if got := stringVar(t, r, "e2"); !strings.Contains(got, "exit status 127") {
		t.Fatalf("e2 = %q, want it to name exit status 127", got)
	}
	// The child's stderr passed through to the shell's stderr — a separate
	// channel, not folded into the captured stdout.
	if !strings.Contains(out.String(), "executable file not found") {
		t.Fatalf("shell stderr = %q, want the not-found report to pass through", out.String())
	}
}

func TestBashPPCaptureUnderscoreAndStatus(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, `set -e
f() { return 9; }
_, err := capture(f)
printf 'after:%s:%s' "$?" "$err"
`)
	if err != nil {
		t.Fatal(err)
	}
	// The := statement succeeds even though the captured command failed: the
	// failure is IN the results, so `set -e` must not abort here.
	if got := out.String(); got != "after:0:capture: f: exit status 9" {
		t.Fatalf("output = %q", got)
	}
}

func TestBashPPCaptureCancellationReportsCancellation(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"run", "capture"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out strings.Builder // bashpp-racegate:safe-synchronized
			// The handler writes real bytes, then cancels mid-capture, then
			// writes more: a lazy implementation would hand back a truncated
			// value that looks complete.
			r := captureRunner(t, &out, ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(hctx context.Context, args []string) error {
					hc := HandlerCtx(hctx)
					fmt.Fprintf(hc.Stdout, "before-cancel")
					cancel()
					fmt.Fprintf(hc.Stdout, "after-cancel")
					return nil
				}
			}))
			src := fmt.Sprintf("out, err := %s(fake_external_cmd)\n", form)
			_ = r.Run(ctx, parseBashPPInternal(t, src))
			vr := r.lookupVar("out")
			if !vr.IsSet() || vr.Kind != expand.String || vr.Str != "" {
				t.Fatalf("out = %#v, want an empty string: a cancelled capture never returns a truncated value", vr)
			}
			errVal := r.lookupVar("err")
			if !strings.Contains(errVal.Str, "context canceled") {
				t.Fatalf("err = %q, want it to report cancellation", errVal.Str)
			}
			if !strings.HasPrefix(errVal.Str, form+":") {
				t.Fatalf("err = %q, want the %s form to name itself", errVal.Str, form)
			}
		})
	}
}

// TestBashPPCaptureLargeIntact pins that a capture larger than a pipe buffer
// (>64KiB) round-trips intact — a short read reported as success is the
// defect class this boundary exists to remove. This variant is generated by
// builtins so it runs on every platform; the sibling test below pushes the
// same volume through a real OS pipe from an external process.
func TestBashPPCaptureLargeIntact(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	chunk := strings.Repeat("0123456789abcdef", 4) // 64 bytes
	err := captureRun(t, r, fmt.Sprintf(`f() {
  i=0
  while [ "$i" -lt 1500 ]; do printf '%%s' %q; i=$((i+1)); done
}
out, err := capture(f)
`, chunk))
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "err"); got != "" {
		t.Fatalf("err = %q, want empty", got)
	}
	want := strings.Repeat(chunk, 1500) // 96000 bytes > 64KiB
	got := stringVar(t, r, "out")
	if len(got) != len(want) {
		t.Fatalf("captured %d bytes, want %d: a large capture must arrive whole", len(got), len(want))
	}
	if got != want {
		t.Fatal("captured bytes differ from what the command wrote")
	}
}

func TestBashPPCaptureLargeExternalPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses head and /dev/zero; the builtin-generated variant covers windows")
	}
	if _, err := exec.LookPath("head"); err != nil {
		t.Skipf("head not found: %v", err)
	}
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	// 100000 NUL bytes from a real external process: exercises the OS pipe
	// between the child and the capture buffer, and binary safety.
	err := captureRun(t, r, `out, err := capture("head", "-c", "100000", "/dev/zero")`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "err"); got != "" {
		t.Fatalf("err = %q, want empty", got)
	}
	got := stringVar(t, r, "out")
	if len(got) != 100000 {
		t.Fatalf("captured %d bytes, want 100000: a capture exceeding the pipe buffer must arrive whole", len(got))
	}
	if got != strings.Repeat("\x00", 100000) {
		t.Fatal("captured bytes are not the 100000 NULs the child wrote")
	}
}

func TestBashPPCaptureArgConventionAndSpread(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	// Arguments are argv, by the established P3 call convention: a bare
	// identifier naming a live binding is that binding's value, `$x`
	// expands, and `xs...` spreads an indexed variable element by element.
	// No word splitting: an argument holding a space stays ONE argv word.
	err := captureRun(t, r, `f() { printf '%s|%s' "$#" "$2"; }
arg2='a b'
out, err := capture(f, one, $arg2)
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "out"); got != "2|a b" {
		t.Fatalf("out = %q, want %q (no word splitting at the typed boundary)", got, "2|a b")
	}
}

func TestBashPPCaptureArityAndCommandPosition(t *testing.T) {
	t.Parallel()
	t.Run("one name", func(t *testing.T) {
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := captureRunner(t, &out)
		err := captureRun(t, r, "x := run(\"ls\")\n")
		if err == nil || r.lookupVar("x").IsSet() {
			t.Fatalf("err = %v, x = %#v: run returns 2 values and must not bind 1", err, r.lookupVar("x"))
		}
		if !strings.Contains(out.String(), "assignment mismatch: 1 variable(s) but 2 value(s)") {
			t.Fatalf("stderr = %q", out.String())
		}
	})
	t.Run("no command", func(t *testing.T) {
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := captureRunner(t, &out)
		if err := captureRun(t, r, "a, b := capture()\n"); err == nil {
			t.Fatal("capture() with no command must fail")
		}
		if !strings.Contains(out.String(), "capture: requires a command") {
			t.Fatalf("stderr = %q", out.String())
		}
	})
	t.Run("command position", func(t *testing.T) {
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := captureRunner(t, &out)
		if err := captureRun(t, r, "run(\"ls\")\n"); err == nil {
			t.Fatal("run(...) in command position must be diagnosed")
		}
		if !strings.Contains(out.String(), "bind them with :=") {
			t.Fatalf("stderr = %q", out.String())
		}
	})
}

// A session's own `func run` shadows the predeclared capture form, exactly as
// a Go declaration shadows a predeclared identifier.
func TestBashPPUserFuncShadowsRun(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := captureRunner(t, &out)
	err := captureRun(t, r, `func run(cmd string) (a, b string) {
 a=shadow
 b=none
 return
}
x, y := run("z")
printf '%s:%s' "$x" "$y"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "shadow:none" {
		t.Fatalf("output = %q, want %q", got, "shadow:none")
	}
}

// The new call names must stay ordinary shell words outside the bash++
// dialect: Classic and POSIX behaviour is untouched, and the call spelling
// remains the syntax error stock bash gives it.
func TestBashPPCaptureInertOutsideBashPP(t *testing.T) {
	t.Parallel()
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangPOSIX} {
		t.Run(lang.String(), func(t *testing.T) {
			t.Parallel()
			f, err := syntax.NewParser(syntax.Variant(lang)).Parse(
				strings.NewReader("run() { echo ran-as-function; }\nrun\necho capture run\n"), "")
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder // bashpp-racegate:safe-synchronized
			r, err := New(StdIO(nil, &out, &out), Lang(lang))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != "ran-as-function\ncapture run\n" {
				t.Fatalf("%v output = %q: run/capture must remain ordinary words", lang, got)
			}
			// The call spelling stays Class R: a syntax error, exactly as in
			// stock bash — never quietly claimed.
			if _, err := syntax.NewParser(syntax.Variant(lang)).Parse(
				strings.NewReader(`r, err := run("ls")`), ""); err == nil {
				t.Fatalf("%v parsed the Class R capture form; it must stay a syntax error", lang)
			}
		})
	}
}

// --- explicit decode ---
//
// Source-derived JSON boundary matrix: independently adapted from CPython's
// Lib/test/test_json/test_decode.py, TestDecode.test_empty_objects and
// TestDecode.test_extra_data at v3.14.0
// (ebf955df7a89ed0c7968f79faec1de49f61ed7cb). CPython is distributed under
// the Python Software Foundation License Version 2.  The fixtures below
// express the same success/error boundaries in Bash#; they do not copy
// CPython test code. The duplicate-key policy is explicit here because neither
// JSON nor that source fixture rejects duplicate member names: as with Go's
// encoding/json, the last member wins.

// TestBashPPDecodeCPythonFixtures faithfully ports the exact JSON inputs and
// success/error assertions from CPython TestDecode.test_empty_objects and
// TestDecode.test_extra_data. The assertions are expressed against Bash#'s
// decoded value model rather than copying CPython's unittest implementation.
func TestBashPPDecodeCPythonFixtures(t *testing.T) {
	t.Parallel()
	emptyCases := []struct {
		input string
		check func(any) bool
	}{
		{`{}`, func(v any) bool { m, ok := v.(map[string]any); return ok && len(m) == 0 }},
		{`[]`, func(v any) bool { s, ok := v.([]any); return ok && len(s) == 0 }},
		{`""`, func(v any) bool { s, ok := v.(string); return ok && s == "" }},
	}
	for _, tc := range emptyCases {
		value, err := bashPPDecodeJSON(tc.input)
		if err != nil {
			t.Fatalf("decode %q: %v", tc.input, err)
		}
		if !tc.check(value) {
			t.Fatalf("decode %q = %#v, want the corresponding empty value", tc.input, value)
		}
	}
	if value, err := bashPPDecodeJSON(`[1, 2, 3]5`); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("decode extra data = (%#v, %v), want a trailing-data error", value, err)
	}
}

func decodeRunner(t *testing.T, out *strings.Builder) *Runner { // bashpp-racegate:safe-synchronized
	t.Helper()
	r := captureRunner(t, out)
	// json.Decode is an imported selector; these tests pre-register the
	// import rather than exercising the Go toolchain resolution, which has
	// its own tests. Recognition is keyed on the import PATH.
	r.Reset()
	r.bashPPImports = map[string]string{"json": "encoding/json"}
	return r
}

func TestBashPPDecodeExplicitTyped(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := decodeRunner(t, &out)
	err := captureRun(t, r, `f() { printf '{"name":"ada","tags":[1,2]}'; }
out, cerr := capture(f)
v, derr := json.Decode(out)
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stringVar(t, r, "derr"); got != "" {
		t.Fatalf("derr = %q, want empty", got)
	}
	vr := r.lookupVar("v")
	if vr.Kind != expand.Object {
		t.Fatalf("v kind = %v, want expand.Object: the EXPLICIT decode produces the typed value", vr.Kind)
	}
	obj, ok := vr.Obj.(map[string]any)
	if !ok || obj["name"] != "ada" {
		t.Fatalf("v = %#v", vr.Obj)
	}
	tags, ok := obj["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != json.Number("1") {
		t.Fatalf("tags = %#v, want json.Number elements preserving the source spelling", obj["tags"])
	}
}

// TestBashPPStructuredCaptureExample is the runnable B15 contract from
// docs/bashpp-structured-capture.md. `run` keeps process output in its
// separate Stdout field; json.Decode is the one explicit output-conversion
// step. In particular, neither run/capture nor a pipe infers an Object from
// bytes that merely happen to be JSON.
func TestBashPPStructuredCaptureExample(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	// This example executes a real Go import. Preserve the native toolchain
	// environment (including Windows cache/profile paths), unlike the other
	// capture fixtures which deliberately use a sparse Unix search path.
	r := captureRunner(t, &out, Env(expand.ListEnviron(os.Environ()...)))
	err := captureRun(t, r, `import "encoding/json"
report() { printf '{"name":"Ada","tags":["go","shell"]}'; }
result, rerr := run(report)
if [ -n "$rerr" ]; then printf 'run:%s' "$rerr"; fi
value, derr := json.Decode(result.Stdout)
if [ -n "$derr" ]; then printf 'decode:%s' "$derr"; fi
printf '%s:%s' value.name value.tags[1]
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := stringVar(t, r, "rerr"); got != "" {
		t.Fatalf("rerr = %q, want empty", got)
	}
	if got := stringVar(t, r, "derr"); got != "" {
		t.Fatalf("derr = %q, want empty", got)
	}
	if got := out.String(); got != "Ada:shell" {
		t.Fatalf("output = %q, want %q", got, "Ada:shell")
	}
}

func TestBashPPDecodeAliasAndPathIdentity(t *testing.T) {
	t.Parallel()
	t.Run("alias", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := captureRunner(t, &out)
		r.Reset()
		r.bashPPImports = map[string]string{"j2": "encoding/json"}
		if err := captureRun(t, r, "v, derr := j2.Decode('[true]')\n"); err != nil {
			t.Fatal(err)
		}
		if got := stringVar(t, r, "derr"); got != "" {
			t.Fatalf("derr = %q, want empty: the decode is recognised under any import alias", got)
		}
		if vr := r.lookupVar("v"); vr.Kind != expand.Object {
			t.Fatalf("v kind = %v, want expand.Object", vr.Kind)
		}
	})
	t.Run("other package named json", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := captureRunner(t, &out)
		r.Reset()
		r.bashPPImports = map[string]string{"json": "example.com/json"}
		r.bashPPTools = bashPPToolchain{goBinary: "/reviewed/go", eval: &recordingBashPPEval{}}
		// A different package that happens to be NAMED json is not claimed:
		// its Decode goes to the ordinary imported-selector path (which this
		// injected evaluator cannot serve, and honestly says so).
		if err := captureRun(t, r, "v, derr := json.Decode('[true]')\n"); err == nil {
			t.Fatal("expected the ordinary imported-selector path, not the built-in decode")
		}
		if !strings.Contains(out.String(), "cannot return object values") {
			t.Fatalf("stderr = %q", out.String())
		}
	})
}

func TestBashPPDecodeScalarsStayStrings(t *testing.T) {
	t.Parallel()
	tests := []struct{ input, want string }{
		{`'"hi"'`, "hi"},
		{`'42'`, "42"},
		{`'true'`, "true"},
		{`'1e+21'`, "1e+21"}, // the source spelling, not a float64 detour
	}
	for _, tc := range tests {
		var out strings.Builder // bashpp-racegate:safe-synchronized
		r := decodeRunner(t, &out)
		if err := captureRun(t, r, fmt.Sprintf("v, derr := json.Decode(%s)\n", tc.input)); err != nil {
			t.Fatal(err)
		}
		if got := stringVar(t, r, "derr"); got != "" {
			t.Fatalf("decode %s: derr = %q", tc.input, got)
		}
		if got := stringVar(t, r, "v"); got != tc.want {
			t.Fatalf("decode %s: v = %q, want %q", tc.input, got, tc.want)
		}
	}

	// null is a structured absence, not a scalar: it must round-trip as the
	// object coercion "null", distinguishable from the empty string.
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := decodeRunner(t, &out)
	if err := captureRun(t, r, "v, derr := json.Decode('null')\nprintf '%s' \"$v\"\n"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "null" {
		t.Fatalf("null interpolates as %q, want %q", got, "null")
	}
}

func TestBashPPDecodeDuplicateKeysLastWins(t *testing.T) {
	t.Parallel()
	decoded, err := bashPPDecodeJSON(`{"language":"first","language":"last"}`)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("decoded = %#v, want object", decoded)
	}
	if got := obj["language"]; got != "last" {
		t.Fatalf("duplicate-key value = %#v, want last member", got)
	}
}

// A decode failure binds a diagnostic next to an EMPTY value; a successful
// decode of the empty JSON string binds an empty value next to an EMPTY
// diagnostic. The two are always distinguishable — that is requirement 2.
func TestBashPPDecodeFailureIsDistinguishable(t *testing.T) {
	t.Parallel()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r := decodeRunner(t, &out)
	err := captureRun(t, r, `a, aerr := json.Decode('""')
b, berr := json.Decode("")
`)
	if err != nil {
		t.Fatal(err)
	}
	if a, aerr := stringVar(t, r, "a"), stringVar(t, r, "aerr"); a != "" || aerr != "" {
		t.Fatalf("decode of a valid empty string: a=%q aerr=%q", a, aerr)
	}
	if b, berr := stringVar(t, r, "b"), stringVar(t, r, "berr"); b != "" || berr == "" {
		t.Fatalf("decode of empty input: b=%q berr=%q, want a diagnostic", b, berr)
	}
}

func TestBashPPDecodeDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
	}{
		{"empty", "", "json.Decode: empty input"},
		{"whitespace only", " \n\t", "json.Decode: empty input"},
		{"truncated object", `{"a":`, "json.Decode: unexpected end of input at offset 5"},
		{"bare key", `{a:1}`, "at offset 2"},
		{"trailing garbage", `[1,2] x`, "trailing data after the JSON value at offset 6"},
		{"second value", `1 2`, "trailing data after the JSON value at offset 2"},
		{"nul byte", "\x00", "at offset 1"},
		{"bad literal", "tru", "json.Decode:"},
		{"deep nesting", strings.Repeat("[", 100000), "json.Decode:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := bashPPDecodeJSON(tc.input)
			if err == nil {
				t.Fatalf("decode %q succeeded as %#v, want a diagnostic", tc.input, v)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("decode %q: diagnostic %q does not contain %q", tc.input, err, tc.want)
			}
		})
	}
}

// Invalid UTF-8 inside a QUOTED string is not a syntax error: encoding/json
// coerces the bad bytes to U+FFFD, the documented Go behaviour. This pins
// that the decode returns that value rather than panicking or failing —
// arbitrary bytes always yield either a value or a diagnostic.
func TestBashPPDecodeInvalidUTF8String(t *testing.T) {
	t.Parallel()
	v, err := bashPPDecodeJSON("\"\xff\xfe\"")
	if err != nil {
		t.Fatalf("decode of a quoted invalid-UTF-8 string: %v", err)
	}
	if v != "��" {
		t.Fatalf("v = %q, want the U+FFFD replacements encoding/json documents", v)
	}
}

// --- properties over arbitrary values and bytes ---

// randomJSONValue builds an arbitrary value from the full supported type set:
// null, bool, number, string (including NULs, non-ASCII and long runs),
// object and array, nested to the given depth.
func randomJSONValue(rng *rand.Rand, depth int) any {
	kind := rng.IntN(7)
	if depth <= 0 && kind >= 5 {
		kind = rng.IntN(5)
	}
	switch kind {
	case 0:
		return nil
	case 1:
		return rng.IntN(2) == 0
	case 2: // integers, negatives, and large magnitudes
		return int64(rng.Uint64()) >> uint(rng.IntN(63))
	case 3: // finite integral floats; exact number TEXT is pinned separately
		return float64(int64(rng.NormFloat64() * 1e6))
	case 4:
		return randomJSONString(rng, rng.IntN(64))
	case 5:
		n := rng.IntN(5)
		arr := make([]any, n)
		for i := range arr {
			arr[i] = randomJSONValue(rng, depth-1)
		}
		return arr
	default:
		n := rng.IntN(5)
		obj := make(map[string]any, n)
		for i := 0; i < n; i++ {
			obj[randomJSONString(rng, 1+rng.IntN(8))] = randomJSONValue(rng, depth-1)
		}
		return obj
	}
}

func randomJSONString(rng *rand.Rand, n int) string {
	var b strings.Builder // bashpp-racegate:safe-private
	for i := 0; i < n; i++ {
		switch rng.IntN(6) {
		case 0:
			b.WriteByte(0) // embedded NUL: encodes as the \\u0000 escape
		case 1:
			b.WriteRune(rune(0x80 + rng.IntN(0x800))) // non-ASCII
		case 2:
			b.WriteByte("\"\\\n\t/<&"[rng.IntN(7)]) // escape-heavy
		default:
			b.WriteByte(byte(' ' + rng.IntN(95)))
		}
	}
	return b.String()
}

// TestBashPPDecodeRoundTripProperty: for every supported type, the outbound
// coercion (expand.ObjectString, the L0 rule) decodes back to the same value.
// Equality is judged by re-encoding, which is exact because numbers decode to
// their source spelling.
func TestBashPPDecodeRoundTripProperty(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(0xba5, 0x115))
	for i := 0; i < 500; i++ {
		original := randomJSONValue(rng, 6)
		encoded := expand.ObjectString(original)
		decoded, err := bashPPDecodeJSON(encoded)
		if err != nil {
			t.Fatalf("case %d: decode(%q) failed: %v", i, encoded, err)
		}
		reencoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("case %d: re-encode failed: %v", i, err)
		}
		if string(reencoded) != encoded {
			t.Fatalf("case %d: round trip drifted:\n out: %q\nback: %q", i, encoded, reencoded)
		}
	}
}

// A value bigger than a pipe buffer round-trips intact through encode+decode.
func TestBashPPDecodeRoundTripLarge(t *testing.T) {
	t.Parallel()
	big := make([]any, 0, 4096)
	for i := 0; i < 4096; i++ {
		big = append(big, strings.Repeat("x", 32)+"é")
	}
	for _, original := range []any{
		strings.Repeat("payload-", 16384), // ~128KiB string
		big,                               // >64KiB array
	} {
		encoded := expand.ObjectString(original)
		if len(encoded) <= 64*1024 {
			t.Fatalf("test value too small (%d bytes) to exceed a pipe buffer", len(encoded))
		}
		decoded, err := bashPPDecodeJSON(encoded)
		if err != nil {
			t.Fatal(err)
		}
		reencoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(reencoded) != encoded {
			t.Fatalf("large value drifted: %d bytes out, %d bytes back", len(encoded), len(reencoded))
		}
	}
}

// FuzzBashPPDecodeJSON: arbitrary bytes always yield either a value or a
// diagnostic — never a panic, a hang, or a silent truncation. On success the
// whole input was consumed as one valid JSON value and the value re-encodes
// and re-decodes stably.
func FuzzBashPPDecodeJSON(f *testing.F) {
	seeds := []string{
		"", " ", "null", "true", "42", "-0.5e3", `"hi"`,
		`{"a":[1,2,{"b":null}]}`,
		`{"a":`, `{a:1}`, `[1,2] x`, `1 2`, `"unterminated`,
		"\x00", "\"\xff\xfe\"", "\xed\xa0\x80", // NULs and invalid UTF-8
		strings.Repeat("[", 4096), strings.Repeat(`{"a":`, 512),
		`"` + strings.Repeat("\\u0000", 64) + `"`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		v, err := bashPPDecodeJSON(input)
		if err != nil {
			if err.Error() == "" {
				t.Fatal("failure without a diagnostic")
			}
			return
		}
		// Success promises the whole input was one JSON value: nothing was
		// silently ignored, so the input must be valid JSON in its entirety.
		if !json.Valid([]byte(input)) {
			t.Fatalf("decode accepted %q, which is not one whole valid JSON value", input)
		}
		reencoded, merr := json.Marshal(v)
		if merr != nil {
			t.Fatalf("decoded value does not re-encode: %v", merr)
		}
		again, derr := bashPPDecodeJSON(string(reencoded))
		if derr != nil {
			t.Fatalf("re-decode of %q failed: %v", reencoded, derr)
		}
		reencoded2, merr := json.Marshal(again)
		if merr != nil || string(reencoded2) != string(reencoded) {
			t.Fatalf("decode is not stable: %q vs %q (%v)", reencoded, reencoded2, merr)
		}
	})
}
