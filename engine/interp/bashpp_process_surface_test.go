// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// Acceptance tests for the dialect SURFACE over the bounded process line
// substrate — the literal requested spellings:
//
//	r, err := run(...);   for line := range r.Lines() { … }      (B13)
//	p, err := start(...); for line := range p.Lines() { … }      (B14)
//	status, err := p.Wait();  p.Close()
//
// PROVENANCE — the lifecycle assertions are the SAME faithful ports recorded
// in bashpp_process_test.go and plan-story575-process-line-substrate.md, now
// exercised through the shell spellings rather than the Go-level unit:
//
//   - Project:  The Go programming language, standard library `os/exec`.
//     Pinned:   go1.27.1 (GOROOT sdk go1.27.1), src/os/exec/exec_test.go.
//     License:  BSD-3-Clause ($GOROOT/LICENSE, "Copyright (c) 2009 The Go
//               Authors").
//     Ported:   TestExitStatus / TestExitCode (exit-code passthrough) ->
//               TestBashPPStartWaitExitStatus; TestContext / TestContextCancel
//               (cancelling the context kills the process, Wait then reports
//               an error, the process does not outlive the context) ->
//               TestBashPPStartLinesCancel; the double-Wait / reaping
//               invariant -> TestBashPPStartWaitExactOnce. Adaptation: the
//               upstream helperCommand binary is replaced by the host `sh`
//               with the same observable behaviours; the assertions are kept.
//
//   - Project:  GNU Bash (Bash 5.3), Bash Reference Manual, "Exit Status":
//               a fatal signal N yields 128+N; `wait` returns the awaited
//               process's status.
//     Ported:   TestBashPPStartWaitSignalStatus (a SIGTERM'd child reports
//               143 through p.Wait()) and exit-code passthrough. Adaptation:
//               the status is read from the bound `status` variable rather
//               than `$?`.

func processSurfaceRun(t *testing.T, src string) (out, stderr string, err error) {
	t.Helper()
	return processSurfaceRunCtx(t, context.Background(), src)
}

func processSurfaceRunCtx(t *testing.T, ctx context.Context, src string) (out, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf strings.Builder
	r := captureRunner(t, &errBuf, StdIO(nil, &outBuf, &errBuf))
	err = r.Run(ctx, parseBashPPInternal(t, src))
	return outBuf.String(), errBuf.String(), err
}

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
}

// TestBashPPRunLinesSemantics pins B13: r.Lines() over a completed run result
// yields one value per line — an empty middle line is a line, a final
// unterminated line is a line, a trailing newline adds nothing, and an empty
// capture yields nothing. These are the substrate scanner's boundaries, so
// B13 and B14 agree.
func TestBashPPRunLinesSemantics(t *testing.T) {
	requireSh(t)
	for _, tc := range []struct{ name, printf, want string }{
		{"terminated", `a\nb\n`, "[a][b]"},
		{"final unterminated", `a\nb`, "[a][b]"},
		{"empty middle", `a\n\nb\n`, "[a][][b]"},
		{"only newline", `\n`, "[]"},
		{"empty", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "func main() {\n\tr, err := run(\"printf\", '" + tc.printf + "')\n" +
				"\tif err != \"\" { echo \"err=$err\"; return }\n" +
				"\tfor line := range r.Lines() { printf '[%s]' \"$line\" }\n}\nmain()\n"
			out, stderr, err := processSurfaceRun(t, src)
			qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

// TestBashPPRunLinesNonzeroStatus: run completes with a non-zero status, the
// bytes it wrote are still real data, and Lines() iterates them — status is a
// field, never an error, exactly as run's three-channel contract says.
func TestBashPPRunLinesNonzeroStatus(t *testing.T) {
	requireSh(t)
	src := `func main() {
	r, err := run("sh", "-c", "echo one; echo two; exit 7")
	for line := range r.Lines() { printf '[%s]' "$line" }
	printf ' status=%s err=[%s]\n' r.Status "$err"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "[one][two] status=7 err=[]\n"))
}

// TestBashPPRunLinesBreakAndTypes: break leaves the loop; a run result is not
// re-run by Lines(); and Lines() on a plain string is a typed diagnostic.
func TestBashPPRunLinesBreakAndTypes(t *testing.T) {
	requireSh(t)
	src := `func main() {
	r, err := run("printf", 'a\nb\nc\n')
	for line := range r.Lines() {
		if line == "b" { break }
		printf '[%s]' "$line"
	}
	for line := range r.Lines() { printf '<%s>' "$line" }
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "[a]<a><b><c>"))

	_, stderr, err = processSurfaceRun(t, "func main() {\n\ts := \"x\"\n\tfor line := range s.Lines() { echo \"$line\" }\n}\nmain()\n")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "BASHPP-ERANGE-TYPE: s.Lines() requires a run result or a live start handle"))
}

// TestBashPPRunLinesLarge: a large multi-line stdout AND stderr both round
// trip through run, and Lines() sees every line.
func TestBashPPRunLinesLarge(t *testing.T) {
	requireSh(t)
	src := `func main() {
	r, err := run("sh", "-c", 'i=0; while [ $i -lt 20000 ]; do echo "out line $i"; echo "err line $i" >&2; i=$((i+1)); done')
	n := 0
	for line := range r.Lines() { n++ }
	printf 'n=%s status=%s err=[%s]\n' "$n" r.Status "$err"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "n=20000 status=0 err=[]\n"))
	qt.Assert(t, qt.Equals(stderr, ""), qt.Commentf("run captures stderr as data"))
}

// TestBashPPStartLinesLive pins B14's happy path: start returns a handle
// immediately, range p.Lines() streams stdout including a final unterminated
// line and empty middle lines, stderr passes through, and Wait reports the
// status separately from err.
func TestBashPPStartLinesLive(t *testing.T) {
	requireSh(t)
	src := `func main() {
	p, err := start("sh", "-c", 'echo a; echo; echo warn >&2; printf b')
	if err != "" { echo "start err=$err"; return }
	for line := range p.Lines() { printf '[%s]' "$line" }
	status, err := p.Wait()
	echo " status=$status err=[$err]"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "[a][][b] status=0 err=[]\n"))
	qt.Assert(t, qt.Equals(stderr, "warn\n"))
}

// TestBashPPStartWaitExitStatus is the os/exec TestExitStatus / TestExitCode
// port through the shell spelling: `exit N` surfaces as status N from
// p.Wait(), with an EMPTY err — a non-zero exit is a status, not an error.
func TestBashPPStartWaitExitStatus(t *testing.T) {
	requireSh(t)
	for _, code := range []int{0, 1, 3, 42} {
		src := "func main() {\n\tp, err := start(\"sh\", \"-c\", \"exit " + itoa(code) + "\")\n" +
			"\tfor line := range p.Lines() { echo \"unexpected $line\" }\n" +
			"\tstatus, err := p.Wait()\n\techo \"status=$status err=[$err]\"\n}\nmain()\n"
		out, stderr, err := processSurfaceRun(t, src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(out, "status="+itoa(code)+" err=[]\n"))
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestBashPPStartWaitSignalStatus is the Bash "Exit Status" rule: a child
// killed by SIGTERM (15) reports 128+15 = 143 through p.Wait(), which is
// what bash's `wait` returns for it.
func TestBashPPStartWaitSignalStatus(t *testing.T) {
	requireSh(t)
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("128+signal is the unix convention")
	}
	src := `func main() {
	p, err := start("sh", "-c", 'kill -TERM $$')
	for line := range p.Lines() { echo "unexpected $line" }
	status, err := p.Wait()
	echo "status=$status err=[$err]"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "status=143 err=[]\n"))
}

// TestBashPPStartWaitExactOnce: a second p.Wait() returns the identical
// (status, err) without reaping again; p.Close() after Wait is a no-op; and
// Wait/Close in command position leave the status in $?.
func TestBashPPStartWaitExactOnce(t *testing.T) {
	requireSh(t)
	src := `func main() {
	p, err := start("sh", "-c", "echo once; exit 5")
	for line := range p.Lines() { echo "$line" }
	s1, e1 := p.Wait()
	s2, e2 := p.Wait()
	p.Close()
	echo "closed=$?"
	p.Wait()
	echo "again=$? $s1/$s2 [$e1][$e2]"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "once\nclosed=0\nagain=5 5/5 [][]\n"))
}

// TestBashPPStartEarlyCloseReaps: leaving the loop early does not deadlock
// the producer on the bounded channel — Close kills, drains and reaps a
// child that would otherwise run for a long time, promptly.
func TestBashPPStartEarlyCloseReaps(t *testing.T) {
	requireSh(t)
	src := `func main() {
	p, err := start("sh", "-c", 'i=0; while [ $i -lt 100000 ]; do echo "line $i"; i=$((i+1)); done; exec sleep 30')
	for line := range p.Lines() { echo "$line"; break }
	p.Close()
	echo "closed=$?"
	status, err := p.Wait()
	echo "status=$status canceled=$([ -n "$err" ] && echo yes || echo no)"
}
main()
`
	startAt := time.Now()
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "line 0\nclosed=0\nstatus=1 canceled=yes\n"))
	qt.Assert(t, qt.IsTrue(time.Since(startAt) < 20*time.Second), qt.Commentf("Close did not kill the sleeping child"))
}

// TestBashPPStartLinesCancel is the os/exec TestContext / TestContextCancel
// port: cancelling the runner's context while ranging a live process kills
// it, the range ends without a data line, and Wait reports the cancellation
// as err rather than pretending the output was complete. The child execs
// its sleep because the engine's exec handler signals its direct child only
// (no WaitDelay), so a grandchild holding the pipe is outside this story.
func TestBashPPStartLinesCancel(t *testing.T) {
	requireSh(t)
	ctx, cancel := context.WithCancel(context.Background())
	var outBuf lockedBuffer
	var errBuf strings.Builder
	r := captureRunner(t, &errBuf, StdIO(nil, &outBuf, &errBuf))
	src := `func main() {
	p, err := start("sh", "-c", 'echo first; exec sleep 30')
	for line := range p.Lines() { echo "got $line" }
	status, err := p.Wait()
	echo "status=$status err=[$err]"
}
main()
`
	go func() {
		for i := 0; i < 500 && !strings.Contains(outBuf.String(), "got first"); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	startAt := time.Now()
	_ = r.Run(ctx, parseBashPPInternal(t, src))
	qt.Assert(t, qt.IsTrue(time.Since(startAt) < 20*time.Second), qt.Commentf("cancellation did not kill the child"))
	qt.Assert(t, qt.StringContains(outBuf.String(), "got first"))
	qt.Assert(t, qt.Not(qt.StringContains(outBuf.String(), "never")))
	// The handle is reaped and the cancellation is the one Wait reports.
	proc := r.bashPPProcessHandle("p")
	if proc != nil {
		_, werr := proc.Wait()
		qt.Assert(t, qt.ErrorIs(werr, context.Canceled))
	}
}

// TestBashPPStartLargeStdoutStderr: a live child writing heavily to both
// streams never blocks on either — stdout streams through the bounded channel
// under backpressure, stderr passes straight through — and every line is
// seen.
func TestBashPPStartLargeStdoutStderr(t *testing.T) {
	requireSh(t)
	src := `func main() {
	p, err := start("sh", "-c", 'i=0; while [ $i -lt 20000 ]; do echo "out $i"; echo "err $i" >&2; i=$((i+1)); done')
	n := 0
	for line := range p.Lines() { n++ }
	status, err := p.Wait()
	echo "n=$n status=$status err=[$err]"
}
main()
`
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr[:min(len(stderr), 200)]))
	qt.Assert(t, qt.Equals(out, "n=20000 status=0 err=[]\n"))
	qt.Assert(t, qt.Equals(strings.Count(stderr, "\n"), 20000))
}

// TestBashPPStartNoLeak: repeated start/range/Wait and start/Close cycles
// leave no goroutines behind — every process is drained and reaped.
func TestBashPPStartNoLeak(t *testing.T) {
	requireSh(t)
	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(2 * time.Millisecond)
		}
		return n
	}
	src := `func main() {
	i := 0
	for i < 10 {
		p, err := start("sh", "-c", "echo a; echo b")
		for line := range p.Lines() { : }
		status, err := p.Wait()
		q, err := start("sh", "-c", "echo x; exec sleep 30")
		for line := range q.Lines() { break }
		q.Close()
		i++
	}
	echo done
}
main()
`
	before := settle()
	out, stderr, err := processSurfaceRun(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "done\n"))
	// The engine's exec handler escalates a cancelled child to SIGKILL after
	// its 2s kill timeout on a goroutine of its own; allow that to elapse.
	after := settle()
	for deadline := time.Now().Add(5 * time.Second); after > before+3 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		after = settle()
	}
	if after > before+3 {
		buf := make([]byte, 1<<20)
		t.Logf("%s", buf[:runtime.Stack(buf, true)])
	}
	qt.Assert(t, qt.IsTrue(after <= before+3), qt.Commentf("goroutines grew from %d to %d", before, after))
}

// TestBashPPStartCommandPositionAndArity pins the diagnostics: start needs a
// command, binds exactly two results, and Lines() takes one iteration name.
func TestBashPPStartCommandPositionAndArity(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"func main() {\n\tp, err := start()\n}\nmain()\n", "start: requires a command"},
		{"func main() {\n\tp := start(\"sh\")\n}\nmain()\n", "assignment mismatch: 1 variable(s) but 2 value(s)"},
		{"func main() {\n\tr, err := run(\"printf\", \"a\")\n\tfor i, line := range r.Lines() { : }\n}\nmain()\n", "BASHPP-ERANGE-ARITY: Lines() yields one value"},
	} {
		_, stderr, err := processSurfaceRun(t, tc.src)
		qt.Assert(t, qt.IsNotNil(err), qt.Commentf("src: %s", tc.src))
		qt.Assert(t, qt.StringContains(stderr, tc.want))
	}
}

func TestBashPPStartFileCleanup(t *testing.T) {
	requireSh(t)
	var out, diagnostic strings.Builder
	r := captureRunner(t, &diagnostic, StdIO(nil, &out, &diagnostic))
	started := time.Now()
	err := r.Run(context.Background(), parseBashPPInternal(t, `func main() {
	p, err := start("sh", "-c", 'echo ready; exec sleep 30')
	for line := range p.Lines() { echo "$line"; break }
}
main()
`))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out.String(), "ready\n"))
	qt.Assert(t, qt.IsTrue(time.Since(started) < 10*time.Second))
	qt.Assert(t, qt.Equals(len(r.bashPPProcs.procs), 0))
}

// TestBashPPRunLinesParsesAndPrints: the spellings round-trip through the
// parser and printer byte for byte, and the range carries the chained call.
func TestBashPPRunLinesParsesAndPrints(t *testing.T) {
	src := "func main() {\n\tr, err := run(\"printf\", 'a\\nb')\n\tfor line := range r.Lines() {\n\t\techo \"$line\"\n\t}\n\tp, err := start(\"sh\")\n\tstatus, err := p.Wait()\n\tp.Close()\n}\nmain()\n"
	f := parseBashPPInternal(t, src)
	var sb strings.Builder
	qt.Assert(t, qt.IsNil(syntax.NewPrinter().Print(&sb, f)))
	qt.Assert(t, qt.Equals(sb.String(), src))
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		if rng, ok := n.(*syntax.BashPPRange); ok {
			found = true
			qt.Assert(t, qt.IsNotNil(rng.Call))
			qt.Assert(t, qt.Equals(rng.Call.Fun[1].Value, "Lines"))
		}
		return true
	})
	qt.Assert(t, qt.IsTrue(found))
}
