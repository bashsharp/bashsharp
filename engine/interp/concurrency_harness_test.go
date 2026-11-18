// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

// Concurrency gate harness (Sprint #115, sh todo #2, items 4 & 5).
//
// This file is the *machinery* the race gate calls. It deliberately holds no
// test cases of its own — the adversarial ordering table lives in
// concurrency_adversarial_test.go and drives everything here. The design goal
// is that the future Bash++ concurrency story (channels / go / select, tracked
// as 93de714f8c22) EXTENDS this same harness by appending rows to that table,
// rather than standing up a second, divergent harness.
//
// What lives here:
//
//   - cgAwait / cgDeadline: bounded blocking waits. No wait in the harness may
//     hang. On expiry we dump every goroutine stack and fail WITH it, because
//     tripping `go test -timeout` kills the binary and destroys the diagnosis
//     (deliverable 4).
//
//   - cgLeakDetector: goroutine + file-descriptor leak detection that reports a
//     DIAGNOSIS — what leaked and where it was created (the "created by" frame)
//     — not a bare count. The runtime-owned allowlist is a NAMED LIST WITH
//     REASONS, never a numeric threshold, so the next real leak cannot be
//     silently absorbed (deliverable 3).
//
//   - cgQuarantine: the quarantine registry. A quarantined case records owner,
//     rationale and expiry, and is REPORTED as quarantined. A race is never
//     converted into a pass here (deliverable 5).
//
//   - the GOMAXPROCS / repetition matrix lives in concurrency_adversarial_test.go
//     but its bookkeeping type (cgMatrix) is defined here (deliverable 2).

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// cgStackDump returns a full snapshot of every goroutine stack, growing the
// buffer until runtime.Stack stops truncating. Used by every path that must
// fail with a diagnosis instead of a bare timeout.
func cgStackDump() string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// cgAwait runs fn and enforces a hard deadline. fn is expected to be a blocking
// operation the harness is exercising (a Wait, a cancel-propagation, a signal
// delivery). On success it returns normally. On expiry it dumps ALL goroutine
// stacks and fails the test with them — never returning control to a caller
// that would then block again. This is the antidote to `go test -timeout`:
// hitting the outer timeout kills the process and loses every stack, so the
// harness owns its own, shorter, diagnostic deadline.
func cgAwait(t *testing.T, what string, timeout time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return
	case <-time.After(timeout):
		t.Fatalf("cgAwait: %q did not complete within %s — this is a HANG, not a\n"+
			"flake; the operation is wedged. Goroutine dump follows so the wedge is\n"+
			"actionable instead of a lost `go test -timeout` kill:\n\n%s",
			what, timeout, cgStackDump())
	}
}

// cgDeadline is the value-returning sibling of cgAwait: it runs fn (which
// produces a T) under a bounded deadline, returning fn's result on success and
// failing with a full stack dump on expiry. Adversarial rows that must read a
// result out of a blocking op use this so a wedge is reported as a wedge.
func cgDeadline[T any](t *testing.T, what string, timeout time.Duration, fn func() T) T {
	t.Helper()
	type box struct{ v T }
	ch := make(chan box, 1)
	go func() { ch <- box{fn()} }()
	select {
	case r := <-ch:
		return r.v
	case <-time.After(timeout):
		t.Fatalf("cgDeadline: %q did not complete within %s — wedged. Stacks:\n\n%s",
			what, timeout, cgStackDump())
		panic("unreachable")
	}
}

// ---------------------------------------------------------------------------
// Leak detection with a diagnosis, not a number (deliverable 3).
// ---------------------------------------------------------------------------

// cgAllowRule names one class of goroutine that is allowed to be live at the
// end of a focused run, with the REASON it is allowed. This is intentionally a
// named list rather than a count threshold: a threshold ("<=3 goroutines is
// fine") silently swallows the next genuine leak, whereas a rule that stops
// matching makes the leak surface immediately.
type cgAllowRule struct {
	name    string // short label shown in the report
	reason  string // why this goroutine is legitimately long-lived
	matches func(stack string) bool
}

// cgGoroutineAllowlist enumerates the runtime- and test-framework-owned
// goroutines that legitimately outlive an individual focused run. Every entry
// carries its rationale. Adding a match here is a deliberate act with a paper
// trail; raising a numeric budget is not, which is exactly why no numeric
// budget exists.
var cgGoroutineAllowlist = []cgAllowRule{ // bashpp-racegate:safe-private
	{
		name:   "runtime-internal",
		reason: "GC workers, the sysmon monitor, finalizer and bgsweep goroutines are owned by the Go runtime and are never ours to reap.",
		matches: func(s string) bool {
			return strings.Contains(s, "runtime.gcBgMarkWorker") ||
				strings.Contains(s, "runtime.bgsweep") ||
				strings.Contains(s, "runtime.bgscavenge") ||
				strings.Contains(s, "runtime.runfinq") ||
				strings.Contains(s, "runtime.forcegchelper") ||
				strings.Contains(s, "runtime.ensureSigM") ||
				strings.Contains(s, "runtime.main")
		},
	},
	{
		name:   "signal-recv",
		reason: "os/signal keeps one signal_recv goroutine alive for the whole process once any package (interp installs traps) calls signal.Notify; it is a singleton, not a per-run leak.",
		matches: func(s string) bool {
			return strings.Contains(s, "os/signal.signal_recv") ||
				strings.Contains(s, "os/signal.loop")
		},
	},
	{
		name:   "testing-framework",
		reason: "the test binary's own driver goroutines (testing.tRunner for still-running parallel siblings, testing.runTests, the main goroutine) are not owned by the case under measurement.",
		matches: func(s string) bool {
			return strings.Contains(s, "testing.(*T).Run") ||
				strings.Contains(s, "testing.tRunner") ||
				strings.Contains(s, "testing.runTests") ||
				strings.Contains(s, "testing.(*M).Run") ||
				strings.Contains(s, "testing.(*T).Parallel")
		},
	},
	{
		name:   "harness-self",
		reason: "cgAwait/cgDeadline spawn a bounded helper goroutine; if it is still settling when we snapshot it is harness scaffolding, not product state.",
		matches: func(s string) bool {
			return strings.Contains(s, "interp_test.cgAwait") ||
				strings.Contains(s, "interp_test.cgDeadline") ||
				strings.Contains(s, "interp_test.(*cgLeakDetector)")
		},
	},
}

// cgGoroutine is one parsed goroutine block from a runtime.Stack(all) dump.
type cgGoroutine struct {
	header    string // e.g. "goroutine 42 [chan receive]:"
	createdBy string // the "created by ..." frame, the birthplace of the leak
	topFrame  string // the deepest user-visible frame (what it is doing)
	full      string // the entire block, for the report
}

// cgParseGoroutines splits a runtime.Stack(all) dump into individual
// goroutines, extracting the creation site so a leak can be pinned to where it
// was spawned rather than merely counted.
func cgParseGoroutines(dump string) []cgGoroutine {
	var out []cgGoroutine
	for _, block := range strings.Split(dump, "\n\n") {
		block = strings.TrimRight(block, "\n")
		if !strings.HasPrefix(block, "goroutine ") {
			continue
		}
		lines := strings.Split(block, "\n")
		g := cgGoroutine{header: lines[0], full: block}
		for i, ln := range lines {
			if strings.HasPrefix(ln, "created by ") {
				g.createdBy = strings.TrimPrefix(ln, "created by ")
			}
			// The first frame after the header is the function the
			// goroutine is currently executing.
			if i == 1 {
				g.topFrame = strings.TrimSpace(ln)
			}
		}
		out = append(out, g)
	}
	return out
}

// cgSuspectGoroutines returns the goroutines that are NOT covered by the named
// runtime allowlist — the candidates for a leak. Callers snapshot the count at
// the start of a case and drain back to it before returning, so a case that
// legitimately spawns transient background work (an orphaned `&` job that keeps
// running until its loop ends) leaves no residue for the shared leak detector
// to misread as a leak.
func cgSuspectGoroutines() []cgGoroutine {
	var out []cgGoroutine
	for _, g := range cgParseGoroutines(cgStackDump()) {
		if _, ok := cgAllowed(g); ok {
			continue
		}
		out = append(out, g)
	}
	return out
}

// cgDrainToBaseline blocks until the number of non-allowlisted goroutines falls
// back to base, or fails WITH stacks if it never does within the deadline. This
// is how a case that orphans background work stays self-contained: the orphans
// finish on their own (bounded), we wait for them, and a genuine permanent leak
// becomes a diagnosed failure instead of silent residue.
func cgDrainToBaseline(t *testing.T, base int, what string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for {
		susp := cgSuspectGoroutines()
		if len(susp) <= base {
			return
		}
		if time.Now().After(end) {
			var b strings.Builder // bashpp-racegate:safe-private
			fmt.Fprintf(&b, "cgDrainToBaseline: %q left %d suspect goroutine(s) "+
				"(baseline %d) that did not drain within %s — this is a real leak, "+
				"reported WITH creation sites:\n\n", what, len(susp), base, deadline)
			for i, g := range susp {
				fmt.Fprintf(&b, "  #%d %s\n    created by: %s\n", i+1, g.header, g.createdBy)
			}
			t.Fatalf("%s", b.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cgAllowed reports whether g is covered by a named allowlist rule, and which.
func cgAllowed(g cgGoroutine) (cgAllowRule, bool) {
	for _, r := range cgGoroutineAllowlist {
		if r.matches(g.full) {
			return r, true
		}
	}
	return cgAllowRule{}, false
}

// cgLeakDetector captures a baseline at construction and, at Check time, polls
// for goroutines and file descriptors to drain, then reports whatever remains
// that is neither in the baseline nor on the named allowlist.
type cgLeakDetector struct {
	baseline map[string]bool // goroutine header set at start
	fdBefore int
	fdErr    error
}

// cgNewLeakDetector snapshots the starting goroutine and fd state. Call at the
// top of a focused run and defer (*cgLeakDetector).Check.
func cgNewLeakDetector(t *testing.T) *cgLeakDetector {
	t.Helper()
	cgGCSettle()
	d := &cgLeakDetector{baseline: map[string]bool{}}
	for _, g := range cgParseGoroutines(cgStackDump()) {
		d.baseline[g.header] = true
	}
	d.fdBefore, d.fdErr = cgCountFDs()
	return d
}

// cgGCSettle forces GC twice and lets finalizers run, so an *os.File that was
// dropped without an explicit Close (reclaimed by the runtime's finalizer, not
// leaked in any actionable sense) is closed before we count fds. Without this,
// the fd check false-positives on finalizer timing rather than real leaks. A
// genuine leak — an fd held by a still-reachable structure or a live goroutine
// — survives GC and is still caught.
func cgGCSettle() {
	runtime.GC()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
}

// Check drains and diagnoses. It polls up to the deadline letting well-behaved
// goroutines finish, then fails with a NAMED, LOCATED report of everything that
// leaked. It never returns a bare count.
func (d *cgLeakDetector) Check(t *testing.T) {
	t.Helper()

	// Goroutines: poll so that a goroutine merely mid-teardown is given a
	// fair chance to exit before we accuse it of leaking.
	deadline := time.Now().Add(2 * time.Second)
	var leaked []cgGoroutine
	for {
		leaked = leaked[:0]
		for _, g := range cgParseGoroutines(cgStackDump()) {
			if d.baseline[g.header] {
				continue
			}
			if _, ok := cgAllowed(g); ok {
				continue
			}
			leaked = append(leaked, g)
		}
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(leaked) > 0 {
		var b strings.Builder // bashpp-racegate:safe-private
		fmt.Fprintf(&b, "goroutine leak: %d goroutine(s) survived the focused run and\n"+
			"are not on the named runtime allowlist. Each is reported WITH its\n"+
			"creation site so it is actionable:\n\n", len(leaked))
		for i, g := range leaked {
			fmt.Fprintf(&b, "  leak #%d: %s\n", i+1, g.header)
			if g.createdBy != "" {
				fmt.Fprintf(&b, "    created by: %s\n", g.createdBy)
			} else {
				fmt.Fprintf(&b, "    created by: <unknown — top frame: %s>\n", g.topFrame)
			}
			fmt.Fprintf(&b, "    full stack:\n%s\n\n", cgIndent(g.full, "      "))
		}
		b.WriteString(cgAllowlistLegend())
		t.Errorf("%s", b.String())
	}

	// File descriptors. A leaked pipe/fd is as much a leak as a goroutine;
	// the interpreter's simulated pipelines dup fds (see runner.go), so a
	// missed Close shows up here. We poll for the count to settle first, for
	// the same reason as goroutines: an fd that a just-finished teardown is
	// still closing is not a leak, whereas one that never drains IS — and
	// polling distinguishes the two without hiding a real, steady leak.
	if d.fdErr == nil {
		fdDeadline := time.Now().Add(2 * time.Second)
		var fdAfter int
		var err error
		for {
			cgGCSettle()
			fdAfter, err = cgCountFDs()
			if err != nil || fdAfter <= d.fdBefore || time.Now().After(fdDeadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err == nil && fdAfter > d.fdBefore {
			names, _ := cgListFDs()
			t.Errorf("file-descriptor leak: %d fd(s) open at start, %d at end "+
				"(+%d) and did not drain. Open fds now:\n%s\nA positive delta that "+
				"does not settle means a pipe, FIFO or redirect target was not "+
				"closed — trace it to the redirect/pipe that opened it.",
				d.fdBefore, fdAfter, fdAfter-d.fdBefore,
				cgIndent(strings.Join(names, "\n"), "  "))
		}
	}
	// Child processes and timers: the pure-Go interpreter runs subshells as
	// goroutines, not fork()ed processes, so there is no general child-PID
	// table to scan; real children exist only behind an ExecHandler or a
	// JobCarrier and are tracked by those (see testCarrier.pids). Likewise Go
	// exposes no timer registry. Both classes therefore surface HERE, as the
	// goroutine that is parked in time.Sleep / (*Cmd).Wait / runtime timer
	// service and shows up in the leak report above with its creation site.
}

// cgIndent prefixes every line of s with prefix, for nesting a stack inside a
// report.
func cgIndent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// cgAllowlistLegend prints the named allowlist and each entry's reason, so a
// leak report is self-documenting about what WAS excused and why.
func cgAllowlistLegend() string {
	var b strings.Builder // bashpp-racegate:safe-private
	b.WriteString("runtime allowlist in effect (named, with reasons — NOT a count threshold):\n")
	for _, r := range cgGoroutineAllowlist {
		fmt.Fprintf(&b, "  - %s: %s\n", r.name, r.reason)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Repetition / GOMAXPROCS matrix bookkeeping (deliverable 2).
// ---------------------------------------------------------------------------

// cgMatrix records which (GOMAXPROCS, repetition) combinations were actually
// executed. Recording the matrix is a deliverable in itself: a green run at
// GOMAXPROCS=1 proves almost nothing because many races simply do not schedule
// on a single P, so the gate must be able to SEE that a >1 setting ran.
type cgMatrix struct {
	mu   sync.Mutex
	rows []cgMatrixRow
}

type cgMatrixRow struct {
	gomaxprocs int
	numCPU     int
	repeat     int
	cases      int
}

func (m *cgMatrix) record(gomaxprocs, numCPU, repeat, cases int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, cgMatrixRow{gomaxprocs, numCPU, repeat, cases})
}

// maxProcsExercised returns the largest GOMAXPROCS value the matrix actually
// ran at. The gate uses this to reject a run that never left single-P.
func (m *cgMatrix) maxProcsExercised() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := 0
	for _, r := range m.rows {
		if r.gomaxprocs > max {
			max = r.gomaxprocs
		}
	}
	return max
}

// report renders the executed matrix for the test log.
func (m *cgMatrix) report() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	sort.Slice(m.rows, func(i, j int) bool {
		if m.rows[i].gomaxprocs != m.rows[j].gomaxprocs {
			return m.rows[i].gomaxprocs < m.rows[j].gomaxprocs
		}
		return m.rows[i].repeat < m.rows[j].repeat
	})
	var b strings.Builder // bashpp-racegate:safe-private
	b.WriteString("schedule matrix actually executed (GOMAXPROCS x repetitions):\n")
	for _, r := range m.rows {
		fmt.Fprintf(&b, "  GOMAXPROCS=%d (NumCPU=%d)  repeat=%d  cases=%d\n",
			r.gomaxprocs, r.numCPU, r.repeat, r.cases)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Quarantine discipline (deliverable 5).
// ---------------------------------------------------------------------------

// cgQuarantineEntry documents a case that is temporarily excluded from the
// adversarial run. It is NOT a way to convert a race into a pass: the entry is
// reported loudly every run, and TestConcurrencyQuarantine fails the moment an
// entry's expiry passes, so a quarantine cannot rot into a silent skip.
type cgQuarantineEntry struct {
	caseName  string
	owner     string
	rationale string
	expiry    time.Time // yyyy-mm-dd; a past expiry FAILS the quarantine test
	tracking  string    // issue / story reference
}

// cgQuarantine is the live quarantine list. Empty is the correct, healthy
// state: nothing is quarantined today. An entry here is a debt with a due date.
var cgQuarantine = []cgQuarantineEntry{} // bashpp-racegate:safe-private

// cgIsQuarantined reports whether an adversarial case name is currently
// quarantined, so the runner can skip-with-report rather than run it.
func cgIsQuarantined(name string) (cgQuarantineEntry, bool) {
	for _, e := range cgQuarantine {
		if e.caseName == name {
			return e, true
		}
	}
	return cgQuarantineEntry{}, false
}
