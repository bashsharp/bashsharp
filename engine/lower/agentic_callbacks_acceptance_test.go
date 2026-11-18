//go:build full

package lower_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These cases carry the executable claims of interp's agentic callback tests
// (TestBashPPAgenticTrapCallbacks, TestBashPPAgenticSignalCallback,
// TestBashPPAgenticMapfileCallback and TestBashPPAgenticUnwindAndReuse) onto
// actually compiled artifacts.
//
// Every case runs its source twice with the same host hooks — interp's
// agenticRunner ExecHandler, which reports each command with the agentic scope
// it observed — first through the real interpreter and then through a
// lower.Compile artifact whose Bash++ and generated Go are both deleted before
// execution. The interpreter run is the oracle: stdout, stderr and the outcome
// it produces are what the artifact must reproduce. No expected output is
// written by hand, so no parity claim is inferred from an adapted expectation.
//
// The sources are the interp tests' own, assembled the same way those tests
// assemble them. The one exception is the signal group, which is marked adapted
// and explained at agenticSignalCases.

// callbackOutcome is what one execution produced. The artifact reports it as
// JSON from inside the host binary so the two sides are compared as data.
type callbackOutcome struct {
	Stdout, Stderr string
	Code           int
	Err            string
	Canceled       bool
	ScopeAtCancel  bool
}

// entryOutcome projects a source-runner result onto the callbackOutcome shape.
// It is derived from the contract the existing suite already fixes, never from
// what the artifact happens to do:
//
//   - no error is status 0;
//   - a status error keeps its status and reports no error;
//   - a cancellation is status 1 carrying the context error, as
//     TestCompiledProviderEntryAcceptance requires of a cancelled provider;
//   - any other error is status 2, the value-error status
//     TestProgramEntryHelperTypedOnlyAndDefaultMain requires of the generated
//     program, carrying the error.
//
// The status a failure carries is invariant across the two boundaries, and this
// mapping fixes it. Where a diagnostic *lands* is not invariant, and this
// mapping deliberately does not claim it is: the interpreter's runner writes a
// panic to its own stderr and returns only a status, while the compiled Entry
// returns the failure and prints nothing — lower/shellrt/program.go's Program.Run
// ("returns the failure to report and never prints or exits") and the entry
// lower/callables_entry.go emits ("Returned errors are not printed by the
// entry"). assertCallbackSession compares the two boundaries as boundaries, and
// TestAgenticCallbackPanicMainParity proves the emitted main renders the
// returned error into exactly the interpreter's stderr.
func entryOutcome(stdout, stderr string, err error) callbackOutcome {
	out := callbackOutcome{Stdout: stdout, Stderr: stderr}
	switch {
	case err == nil:
	case isExitStatusErr(err):
		status, _ := interp.IsExitStatus(err)
		out.Code = int(status)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		out.Code, out.Err, out.Canceled = 1, err.Error(), true
	default:
		out.Code, out.Err = 2, err.Error()
	}
	return out
}

func isExitStatusErr(err error) bool {
	_, ok := interp.IsExitStatus(err)
	return ok
}

type callbackCase struct {
	name string
	// source is the Bash++ program, assembled exactly as the interp test does.
	source string
	// adapted documents why this source is not the interp test's own. Empty
	// means the source is the interp test's, unchanged.
	adapted string
	// cancel installs the cancelling handler of TestBashPPAgenticUnwindAndReuse.
	cancel bool
	// reuse is a second, separate Entry executed in the same process after the
	// first unwinds. This measures that no agentic scope leaks between Entry
	// sessions in one process. It is NOT interp's Runner reuse: a compiled
	// artifact has no Runner to reset, so the same-Runner half of
	// TestBashPPAgenticUnwindAndReuse has no artifact analogue and is not
	// claimed here.
	reuse string
	// reuseClaim is the interp test's assertion for that later session.
	reuseClaim func(t *testing.T, o callbackOutcome, sourceErr error)
	// claim is the interp test's own assertion, applied to the source oracle so
	// the oracle is verified to still encode the contract before parity is used.
	// sourceErr is the interpreter's raw error, so a claim can distinguish a
	// status failure from a non-status one without consulting the mapping.
	claim func(t *testing.T, o callbackOutcome, sourceErr error)
}

func agenticTrapCases() []callbackCase {
	var cases []callbackCase
	for _, name := range []string{"ERR", "DEBUG", "RETURN", "EXIT"} {
		for _, explicit := range []bool{false, true} {
			callback := "scope callback"
			if explicit {
				callback = "agentic { scope callback; }"
			}
			want := fmt.Sprintf("scope/callback:%t", explicit)
			cases = append(cases, callbackCase{
				name:   fmt.Sprintf("trap/%s/explicit=%t", name, explicit),
				source: "set -T\nplain() { :; }\ntrap '" + callback + "' " + name + "\nagentic { plain; scope inside; false; }\n:",
				claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
					if o.Code != 0 || o.Err != "" {
						t.Fatalf("code=%d err=%s stderr=%q", o.Code, o.Err, o.Stderr)
					}
					found := false
					for _, line := range strings.Split(strings.TrimSpace(o.Stdout), "\n") {
						if strings.HasPrefix(line, "scope/callback:") {
							found = true
							if line != want {
								t.Fatalf("callback inherited scope: %s", o.Stdout)
							}
						}
					}
					if !found || !strings.Contains(o.Stdout, "scope/inside:true") {
						t.Fatalf("callback/caller did not execute: %s", o.Stdout)
					}
				},
			})
		}
	}
	return append(cases, callbackCase{
		name:   "trap/refuses-marked-function",
		source: "agentic function marked() { scope forbidden; }\ntrap marked ERR\nagentic { false; }",
		claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
			if o.Code == 0 {
				t.Fatalf("expected the original false status, got 0: %q", o.Stdout)
			}
			if strings.Contains(o.Stdout, "scope/forbidden") || !strings.Contains(o.Stderr, "requires an explicit agentic") {
				t.Fatalf("stdout=%q stderr=%q", o.Stdout, o.Stderr)
			}
		},
	})
}

func agenticMapfileCases() []callbackCase {
	var cases []callbackCase
	for _, command := range []string{"mapfile", "readarray"} {
		for _, tc := range []struct{ callback, want string }{
			{"scope", "scope/0/line:false\n"},
			{"agentic { scope own; }; :", "scope/own:true\n"},
		} {
			want := tc.want + "scope/caller:true\nscope/end:false\n"
			cases = append(cases, callbackCase{
				name:   "mapfile/" + command + "/" + tc.callback,
				source: "agentic { " + command + " -t -C '" + tc.callback + "' -c 1 <<< line; scope caller; }\nscope end",
				claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
					if o.Code != 0 || o.Err != "" {
						t.Fatalf("code=%d err=%s stderr=%q", o.Code, o.Err, o.Stderr)
					}
					if o.Stdout != want {
						t.Fatalf("got %q want %q", o.Stdout, want)
					}
				},
			})
		}
		cases = append(cases, callbackCase{
			// interp discards this case's status; only the refusal and the
			// unexecuted body are claimed, so neither is claimed here either.
			name:   "mapfile/" + command + "/refuses-marked-function",
			source: "agentic function marked() { scope forbidden; }\nagentic { " + command + " -C marked -c 1 <<< line; }",
			claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
				if strings.Contains(o.Stdout, "scope/forbidden") || !strings.Contains(o.Stderr, "requires an explicit agentic") {
					t.Fatalf("stdout=%q stderr=%q", o.Stdout, o.Stderr)
				}
			},
		})
	}
	return cases
}

// agenticSignalCases is the one adapted group. TestBashPPAgenticSignalCallback
// reaches the unexported Runner.runSignalTrap directly, which a compiled
// artifact cannot do and which has no public source form. The adapter installs
// the same handler with `trap` and delivers a real SIGUSR1 from inside an
// agentic block, so the handler is entered exactly where that test enters it:
// with the interrupted scope active. The equivalence is therefore over what the
// test claims — the handler's own scope, the interrupted block's scope after
// the handler returns, and the handler's control flow reaching the program —
// and not over the private entry point. The adapted source is nonetheless held
// to the same standard as every other case: it is measured through the real
// interpreter with the same host hooks and the artifact must reproduce that
// exactly, so no expectation is written to fit either side.
func agenticSignalCases() []callbackCase {
	var cases []callbackCase
	for _, callback := range []string{"scope callback", "agentic { scope callback; }", "agentic { exit 7; }", "agentic {"} {
		want := ""
		switch callback {
		case "scope callback":
			want = "scope/callback:false"
		case "agentic { scope callback; }":
			want = "scope/callback:true"
		}
		exiting := callback == "agentic { exit 7; }"
		cases = append(cases, callbackCase{
			name:    "signal/" + callback,
			source:  "trap '" + callback + "' USR1\nagentic { kill -USR1 $$; scope after; }\nscope end",
			adapted: "delivers a real SIGUSR1 because Runner.runSignalTrap is unexported",
			claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
				if exiting {
					if o.Code != 7 {
						t.Fatalf("exiting handler control flow: code=%d out=%q", o.Code, o.Stdout)
					}
					if strings.Contains(o.Stdout, "scope/end") {
						t.Fatalf("exiting handler did not unwind: %q", o.Stdout)
					}
					return
				}
				if want != "" && !strings.Contains(o.Stdout, want) {
					t.Fatalf("handler scope: out=%q want %q", o.Stdout, want)
				}
				// The malformed handler has no scope of its own to claim; what
				// both it and the well-formed handlers must show is that the
				// interrupted scope survives and still ends with the block.
				if !strings.Contains(o.Stdout, "scope/after:true") {
					t.Fatalf("interrupted scope not restored: %q", o.Stdout)
				}
				if !strings.Contains(o.Stdout, "scope/end:false") {
					t.Fatalf("scope leaked past the block: %q", o.Stdout)
				}
			},
		})
	}
	return cases
}

// callbackPanicSource is TestBashPPAgenticUnwindAndReuse's panic source. It is
// shared by the Entry corpus case and by the process-main parity control so the
// two cannot drift apart.
const callbackPanicSource = "agentic func fail() { panic(boom); }\nagentic { fail(); }"

func agenticUnwindCases() []callbackCase {
	noLeak := func(t *testing.T, o callbackOutcome, sourceErr error) {
		t.Helper()
		if o.Stdout != "scope/reused:false\n" || o.Code != 0 || o.Err != "" {
			t.Fatalf("later session saw a leaked scope: out=%q code=%d err=%s", o.Stdout, o.Code, o.Err)
		}
	}
	cases := []callbackCase{{
		name:   "unwind/exit",
		source: "agentic { exit 7; }",
		reuse:  "scope reused",
		claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
			if o.Code != 7 || o.Err != "" {
				t.Fatalf("exit status: code=%d err=%s", o.Code, o.Err)
			}
		},
	}, {
		name:   "unwind/panic",
		source: callbackPanicSource,
		reuse:  "scope reused",
		claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
			if sourceErr == nil {
				t.Fatal("expected failure")
			}
			// This claim is about the source oracle only: the interpreter
			// surfaces the panic as status 2 and writes the diagnostic to its
			// own stderr, returning no error of its own. The status is the
			// invariant the artifact must reproduce; where the diagnostic lands
			// is the boundary difference assertCallbackSession resolves and
			// TestAgenticCallbackPanicMainParity proves lossless.
			status, ok := interp.IsExitStatus(sourceErr)
			if !ok || status != 2 {
				t.Fatalf("source panic status: %v", sourceErr)
			}
			if o.Code != 2 || o.Err != "" {
				t.Fatalf("status identity: code=%d err=%q source=%v", o.Code, o.Err, sourceErr)
			}
			if !strings.Contains(o.Stderr, "panic: boom") {
				t.Fatalf("source did not write the panic diagnostic to stderr: %q", o.Stderr)
			}
		},
	}, {
		name:   "unwind/cancel",
		source: "agentic { cancel-now; }",
		cancel: true,
		reuse:  ":",
		claim: func(t *testing.T, o callbackOutcome, sourceErr error) {
			if !o.ScopeAtCancel {
				t.Error("scope not visible at cancellation")
			}
			if !errors.Is(sourceErr, context.Canceled) {
				t.Fatalf("source did not report cancellation: %v", sourceErr)
			}
			if !o.Canceled || o.Code != 1 || o.Err != sourceErr.Error() {
				t.Fatalf("cancellation identity: code=%d err=%q canceled=%t source=%q", o.Code, o.Err, o.Canceled, sourceErr)
			}
		},
	}}
	for i := range cases {
		if cases[i].reuse == "scope reused" {
			cases[i].reuseClaim = noLeak
		}
	}
	return cases
}

// callbackReporter is interp's agenticRunner ExecHandler: every command is
// reported with the agentic scope it observed. Both sides of every case run
// with it, so the oracle and the artifact are measured through the same hook.
func callbackReporter(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	_, err := fmt.Fprintf(hc.Stdout, "%s:%t\n", strings.Join(args, "/"), hc.Agentic)
	return err
}

// runCallbackSource measures a source through the real interpreter and maps the
// result onto the Entry contract. It returns the raw interpreter error too, so
// a claim can assert the non-status identity rather than trust the mapping.
func runCallbackSource(t *testing.T, ctx context.Context, source string, cancelMode bool) (callbackOutcome, error) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	scopeAtCancel := false
	handler := callbackReporter
	if cancelMode {
		handler = func(ctx context.Context, args []string) error {
			if args[0] == "cancel-now" {
				scopeAtCancel = interp.HandlerCtx(ctx).Agentic
				cancel()
				return ctx.Err()
			}
			return callbackReporter(ctx, args)
		}
	}
	var out, diag bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.Dir(t.TempDir()),
		interp.StdIO(strings.NewReader(""), &out, &diag),
		interp.Env(expand.ListEnviron("PATH=/bin:/usr/bin", "BASHY_HINTS=off")),
		interp.ExecHandler(handler),
	)
	if err != nil {
		t.Fatal(err)
	}
	runErr := runner.Run(runCtx, parse(t, source, "callbacks.bpp"))
	outcome := entryOutcome(out.String(), diag.String(), runErr)
	outcome.ScopeAtCancel = scopeAtCancel
	return outcome, runErr
}

// TestAgenticCallbacksAcceptance measures each callback source through the
// interpreter and then through its own compiled, source-removed artifact,
// requiring the two to agree exactly.
func TestAgenticCallbacksAcceptance(t *testing.T) {
	goBinary := callbackGoBinary(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var cases []callbackCase
	cases = append(cases, agenticTrapCases()...)
	cases = append(cases, agenticSignalCases()...)
	cases = append(cases, agenticMapfileCases()...)
	cases = append(cases, agenticUnwindCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.adapted != "" {
				t.Logf("adapted source: %s", tc.adapted)
			}
			oracle, sourceErr := runCallbackSource(t, ctx, tc.source, tc.cancel)
			tc.claim(t, oracle, sourceErr)
			want := []callbackOutcome{oracle}
			if tc.reuse != "" {
				reuseOracle, reuseErr := runCallbackSource(t, ctx, tc.reuse, false)
				if tc.reuseClaim != nil {
					tc.reuseClaim(t, reuseOracle, reuseErr)
				}
				want = append(want, reuseOracle)
			}
			got := runCallbackArtifact(t, ctx, goBinary, root, tc)
			if len(got) != len(want) {
				t.Fatalf("artifact reported %d sessions, want %d", len(got), len(want))
			}
			for i := range want {
				assertCallbackSession(t, i, got[i], want[i])
			}
		})
	}
}

// callerDiagnostic is what a caller following the emitted process main prints:
// whatever the program wrote to stderr, then the returned error rendered once
// with Fprintln. lower/callables_entry.go emits exactly that adapter —
// `if err != nil { fmt.Fprintln(os.Stderr, err) }` — so this is the quantity
// that must be invariant across the Entry and the interpreter, whichever of the
// two carried the diagnostic.
func callerDiagnostic(o callbackOutcome) string {
	if o.Err == "" {
		return o.Stderr
	}
	return o.Stderr + fmt.Sprintln(o.Err)
}

// assertNoDoubleReport fails if one side both wrote a diagnostic to stderr and
// returned it. Rendering it at the caller would then print it twice, so this is
// what keeps callerDiagnostic honest rather than merely permissive.
func assertNoDoubleReport(t *testing.T, session int, side string, o callbackOutcome) {
	t.Helper()
	if o.Err != "" && strings.Contains(o.Stderr, o.Err) {
		t.Fatalf("session %d: the %s both wrote and returned %q; a caller renders it twice (stderr=%q)", session, side, o.Err, o.Stderr)
	}
}

// assertCallbackSession holds the artifact to the source oracle at the two
// boundaries the product actually defines, instead of at the artifact's current
// bytes.
//
// Status, stdout, cancellation and the scope trace are invariant and compared
// exactly. Raw stderr and the raw returned error are preserved as separate
// fields and asserted separately: neither side may report a diagnostic twice,
// and the caller-visible union must be identical. Raw placement is then required
// to agree except for the one split the product documents — the interpreter's
// runner writes a failure to stderr and returns a status, while the compiled
// Entry returns the failure and prints nothing (lower/shellrt/program.go's
// Program.Run and the entry emitted by lower/callables_entry.go). That split is
// only admissible because the emitted main renders the returned error into
// exactly the interpreter's stderr, which TestAgenticCallbackPanicMainParity
// measures on a real process.
func assertCallbackSession(t *testing.T, session int, got, want callbackOutcome) {
	t.Helper()
	if got.Stdout != want.Stdout || got.Code != want.Code || got.Canceled != want.Canceled || got.ScopeAtCancel != want.ScopeAtCancel {
		t.Fatalf("session %d diverges from the source oracle:\n artifact %+v\n   source %+v", session, got, want)
	}
	assertNoDoubleReport(t, session, "artifact", got)
	assertNoDoubleReport(t, session, "source", want)
	if callerDiagnostic(got) != callerDiagnostic(want) {
		t.Fatalf("session %d: the caller-visible diagnostic diverges: artifact stderr=%q err=%q; source stderr=%q err=%q",
			session, got.Stderr, got.Err, want.Stderr, want.Err)
	}
	switch {
	case got.Err == want.Err:
		// Same placement on both sides, so raw stderr must agree byte for byte.
		if got.Stderr != want.Stderr {
			t.Fatalf("session %d: raw stderr diverges: artifact %q; source %q", session, got.Stderr, want.Stderr)
		}
	case want.Err == "" && got.Stderr == "":
		// The documented split: the source runner printed and returned a
		// status; the Entry returned and printed nothing. The union already
		// matched above, so nothing is lost or repeated.
		t.Logf("entry boundary: the Entry returned %q and wrote no stderr while the source runner wrote it and returned status %d; the emitted main renders it identically (TestAgenticCallbackPanicMainParity)", got.Err, want.Code)
	default:
		t.Fatalf("session %d: returned-error identity diverges: artifact err=%q stderr=%q; source err=%q stderr=%q",
			session, got.Err, got.Stderr, want.Err, want.Stderr)
	}
}

// callbackGoBinary is the toolchain that built this test. A missing toolchain
// is a failure, not a skip: this suite's whole claim is that real artifacts are
// built and run, and a silent green would assert nothing.
func callbackGoBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("no go toolchain at %s: %v", binary, err)
	}
	return binary
}

// TestAgenticCallbackPanicMainParity is the control that pays for the boundary
// split assertCallbackSession admits. The Entry corpus measures a panic at the
// Entry boundary, where the failure is returned and nothing is printed. Here the
// same source is compiled with no Entry option, so lower emits the process main
// of lower/callables_entry.go — the caller that renders the returned error with
// a single Fprintln and exits with the status. That binary is built, stripped of
// both its Bash++ and its generated Go, and run as a real process; its stdout,
// stderr and exit status must equal the interpreter's byte for byte.
//
// Equality here is what proves the rendering is lossless in both directions: a
// dropped diagnostic would leave the process stderr short, and a diagnostic
// reported by both the runtime and the caller would leave it doubled.
func TestAgenticCallbackPanicMainParity(t *testing.T) {
	goBinary := callbackGoBinary(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Source oracle, measured exactly as the Entry corpus measures it.
	oracle, sourceErr := runCallbackSource(t, ctx, callbackPanicSource, false)
	status, ok := interp.IsExitStatus(sourceErr)
	if !ok || status != 2 || oracle.Err != "" || oracle.Stderr == "" {
		t.Fatalf("source oracle: err=%v stderr=%q returned=%q", sourceErr, oracle.Stderr, oracle.Err)
	}

	// The default program: no Entry name, so the emitted unit is package main
	// with the main adapter that renders the entry's returned error.
	compiled, err := lower.Compile(parse(t, callbackPanicSource, "panic.bpp"), lower.Options{Origin: "panic.bpp"})
	if err != nil {
		t.Fatalf("lower.Compile of the default program: %v", err)
	}
	if !strings.Contains(string(compiled.Source), "func main()") {
		t.Fatalf("default program emitted no process main:\n%s", compiled.Source)
	}

	module := t.TempDir()
	writeAgenticModule(t, module, root)
	generated := filepath.Join(module, "main.go")
	writeAgenticFile(t, generated, string(compiled.Source))
	binary := filepath.Join(module, "program")
	build := exec.CommandContext(ctx, goBinary, "build", "-mod=mod", "-o", binary, ".")
	build.Dir = module
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the default program: %v\n%s\n--- generated ---\n%s", err, output, compiled.Source)
	}
	if err := os.Remove(generated); err != nil {
		t.Fatal(err)
	}
	execDir := t.TempDir()
	assertNoSource(t, execDir)

	var out, diagnostic bytes.Buffer
	runCtx, runCancel := context.WithTimeout(ctx, 60*time.Second)
	defer runCancel()
	command := exec.CommandContext(runCtx, binary)
	command.Dir = execDir
	command.Env = []string{"PATH=/bin:/usr/bin", "BASHY_HINTS=off"}
	command.Stdout, command.Stderr = &out, &diagnostic
	processStatus := 0
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		processStatus = exit.ExitCode()
	}
	if runCtx.Err() != nil {
		t.Fatalf("the default program exceeded its deadline: %v", runCtx.Err())
	}
	if out.String() != oracle.Stdout || diagnostic.String() != oracle.Stderr || processStatus != oracle.Code {
		t.Fatalf("the process main diverges from the source oracle:\n process stdout=%q stderr=%q status=%d\n  source stdout=%q stderr=%q status=%d",
			out.String(), diagnostic.String(), processStatus, oracle.Stdout, oracle.Stderr, oracle.Code)
	}
	t.Logf("process main renders the entry's returned error as stderr=%q status=%d, identical to the source runner", diagnostic.String(), processStatus)
}

// runCallbackArtifact compiles the case, builds the host under the race
// detector so the artifact itself is instrumented, deletes every source file,
// and returns what the binary measured.
func runCallbackArtifact(t *testing.T, ctx context.Context, goBinary, root string, tc callbackCase) []callbackOutcome {
	t.Helper()
	module := t.TempDir()
	writeAgenticModule(t, module, root)
	sources := []string{}
	compileInto := func(pkg, source, origin string) {
		t.Helper()
		compiled, err := lower.Compile(parse(t, source, origin), lower.Options{Package: pkg, Entry: "Execute", Origin: origin})
		if err != nil {
			t.Fatalf("GAP lower.Compile(%s): %v", origin, err)
		}
		path := filepath.Join(module, pkg, "program.go")
		writeAgenticFile(t, path, string(compiled.Source))
		sources = append(sources, path)
	}
	compileInto("generated", tc.source, "callbacks.bpp")
	imports, body := "", callbackSingleBody
	if tc.reuse != "" {
		compileInto("reuse", tc.reuse, "reuse.bpp")
		imports, body = `;"agenticacceptance/reuse"`, callbackReuseBody
	}
	host := filepath.Join(module, "host", "artifact_test.go")
	writeAgenticFile(t, host, fmt.Sprintf(callbackHostHarness, imports, body))
	sources = append(sources, host)

	binary := filepath.Join(module, "artifact.test")
	build := exec.CommandContext(ctx, goBinary, "test", "-mod=mod", "-race", "-c", "-o", binary, "./host")
	build.Dir = module
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("GAP building the %s artifact: %v\n%s", tc.name, err, output)
	}
	// Source removal: no original or generated source survives the build.
	for _, path := range sources {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	execDir := t.TempDir()
	assertNoSource(t, execDir)
	result := filepath.Join(t.TempDir(), "outcome.json")
	cancelFlag := "0"
	if tc.cancel {
		cancelFlag = "1"
	}
	command := exec.CommandContext(ctx, binary, "-test.v", "-test.timeout=120s")
	command.Dir = execDir
	command.Env = []string{"PATH=/bin:/usr/bin", "BASHY_HINTS=off", "GORACE=halt_on_error=1", "RESULT=" + result, "CANCEL=" + cancelFlag}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("artifact %s: %v\n%s", tc.name, err, output)
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var measured []callbackOutcome
	if err := json.Unmarshal(data, &measured); err != nil {
		t.Fatal(err)
	}
	return measured
}

// callbackHostHarness keeps the hooks the contracts need — the same reporting
// ExecHandler and the shell factory carrying it — and reports what it measured
// as data, so the outer test compares against the interpreter rather than
// against an expectation written into the harness.
const callbackHostHarness = `package host_test
import("bytes";"context";"encoding/json";"errors";"fmt";"os";"strings";"testing";"agenticacceptance/generated"%s;"github.com/bashsharp/bashsharp/engine/interp";rt "github.com/bashsharp/bashsharp/engine/lower/shellrt";"github.com/bashsharp/bashsharp/engine/lower/shellrt/shellexec")
type outcome struct{Stdout,Stderr string;Code int;Err string;Canceled,ScopeAtCancel bool}
func report(ctx context.Context,args []string)error{
 hc:=interp.HandlerCtx(ctx)
 _,err:=fmt.Fprintf(hc.Stdout,"%%s:%%t\n",strings.Join(args,"/"),hc.Agentic)
 return err
}
func measure(execute func(...rt.SessionOption)(int,error),cancelMode bool)outcome{
 ctx,cancel:=context.WithCancel(context.Background())
 defer cancel()
 result:=outcome{}
 handler:=report
 if cancelMode {
  handler=func(ctx context.Context,args []string)error{
   if args[0]=="cancel-now" {
    result.ScopeAtCancel=interp.HandlerCtx(ctx).Agentic&&rt.Agentic(ctx)
    cancel()
    return ctx.Err()
   }
   return report(ctx,args)
  }
 }
 var out,diagnostic bytes.Buffer
 code,err:=execute(rt.WithContext(ctx),rt.WithDir("."),rt.WithEnviron("PATH=/bin:/usr/bin","BASHY_HINTS=off"),rt.WithStdio(strings.NewReader(""),&out,&diagnostic),rt.WithShellFactory(shellexec.New(shellexec.BashPP(),shellexec.RunnerOptions(interp.ExecHandler(handler)))))
 result.Stdout,result.Stderr,result.Code=out.String(),diagnostic.String(),code
 if err!=nil {result.Err=err.Error();result.Canceled=errors.Is(err,context.Canceled)||errors.Is(err,context.DeadlineExceeded)}
 return result
}
func record(t *testing.T,results []outcome){
 t.Helper()
 data,err:=json.Marshal(results)
 if err!=nil {t.Fatal(err)}
 if err:=os.WriteFile(os.Getenv("RESULT"),data,0600);err!=nil {t.Fatal(err)}
}
%s`

const callbackSingleBody = `func TestArtifact(t *testing.T){
 record(t,[]outcome{measure(generated.Execute,os.Getenv("CANCEL")=="1")})
}
`

// The later session is a separate Entry in the same process: it measures that
// no agentic scope leaks across sessions, which is the compiled boundary. It is
// not interp's Runner reuse, which a compiled artifact has no analogue for.
const callbackReuseBody = `func TestArtifact(t *testing.T){
 first:=measure(generated.Execute,os.Getenv("CANCEL")=="1")
 second:=measure(reuse.Execute,false)
 record(t,[]outcome{first,second})
}
`
