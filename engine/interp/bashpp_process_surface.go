// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The dialect SURFACE over the one bounded process line substrate
// (bashpp_process.go):
//
//   - B13  `r, err := run(...)` then `for line := range r.Lines()` — iteration
//     over the COMPLETED capture's stdout. It reads the buffered result and
//     never streams: run has already returned, so Lines() is a pure split.
//   - B14  `p, err := start(...)` — a LIVE process handle. `range p.Lines()`
//     receives from the substrate's bounded channel and is cancellable (the
//     runner's context cancels the process); `status, err := p.Wait()` is the
//     one blocking call, exact-once; `p.Close()` abandons the process early —
//     kill, drain, reap — and is safe after Wait.
//
// The spellings are the narrowest seams the story asks for: a range may read
// exactly one chained method, `x.Lines()`, and the short-declaration and
// command-position dispatchers answer `x.Wait()` / `x.Close()` only when x is
// a live handle. Nothing here is a general method evaluator, and there is no
// second streaming abstraction — start's subshell is just another
// bashPPProcessSource for bashPPLineProcess.

// bashPPProcessTable is the session-wide registry of live start(...) handles,
// shared by pointer with subshells so a handle bound in the parent is visible
// wherever the variable is.
type bashPPProcessTable struct {
	mu    sync.Mutex
	next  int
	procs map[string]*bashPPLineProcess
}

// bashPPProcessHandleKey is the Object field naming a live handle. The handle
// Object is opaque by design: it interpolates as JSON like any other Object
// but carries no output — output is read through Lines(), status through
// Wait(). Nothing about it blocks.
const bashPPProcessHandleKey = "Handle"

func (r *Runner) bashPPProcessRegistry() *bashPPProcessTable {
	if r.bashPPProcs == nil {
		r.bashPPProcs = &bashPPProcessTable{procs: map[string]*bashPPLineProcess{}}
	}
	return r.bashPPProcs
}

func (t *bashPPProcessTable) add(p *bashPPLineProcess) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.next++
	id := "proc" + strconv.Itoa(t.next)
	t.procs[id] = p
	return id
}

func (t *bashPPProcessTable) get(id string) *bashPPLineProcess {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.procs[id]
}

// cleanupSince releases only handles created by this file execution. A nested
// Run must not close handles owned by the outer execution or interactive caller.
func (t *bashPPProcessTable) cleanupSince(first int) {
	t.mu.Lock()
	var procs []*bashPPLineProcess
	for n := first + 1; n <= t.next; n++ {
		id := "proc" + strconv.Itoa(n)
		if p := t.procs[id]; p != nil {
			procs = append(procs, p)
			delete(t.procs, id)
		}
	}
	t.mu.Unlock()
	for _, p := range procs {
		_ = p.Close()
	}
}

// bashPPProcessHandle resolves a bound name to its live process, or nil when
// the name is not a start(...) handle.
func (r *Runner) bashPPProcessHandle(name string) *bashPPLineProcess {
	if !syntax.BashPPValidIdent(name) || r.bashPPProcs == nil {
		return nil
	}
	obj, ok := r.lookupVar(name).Obj.(map[string]any)
	if !ok {
		return nil
	}
	id, ok := obj[bashPPProcessHandleKey].(string)
	if !ok {
		return nil
	}
	return r.bashPPProcs.get(id)
}

// bashPPRunResultStdout resolves a bound name to a completed run(...)
// result's stdout, reporting false for anything else.
func (r *Runner) bashPPRunResultStdout(name string) (string, bool) {
	if !syntax.BashPPValidIdent(name) {
		return "", false
	}
	obj, ok := r.lookupVar(name).Obj.(map[string]any)
	if !ok {
		return "", false
	}
	stdout, ok := obj["Stdout"].(string)
	if !ok {
		return "", false
	}
	if _, ok := obj["Status"]; !ok {
		return "", false
	}
	return stdout, true
}

// bashPPSplitLines splits completed output exactly as the live substrate's
// scanner does: every "\n" ends a line, a final unterminated line is a line,
// an empty middle line is an empty line, and a trailing newline does not add
// an empty last line. B13 and B14 therefore agree on every boundary.
func bashPPSplitLines(s string) []string {
	if s == "" {
		return nil
	}
	// The capture is already in memory; imposing the live scanner's token
	// limit here would silently truncate an otherwise successful capture.
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

// bashPPRangeLines answers `for line := range x.Lines()`. It reports false
// when the range carries no chained call so the other range shapes run.
func (r *Runner) bashPPRangeLines(ctx context.Context, rng *syntax.BashPPRange) bool {
	if rng.Call == nil {
		return false
	}
	if len(rng.Names) > 1 {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-ARITY: Lines() yields one value")
		return true
	}
	base := rng.Call.Fun[0].Value
	lineType := bashPPRangeNamedType("string")
	if stdout, ok := r.bashPPRunResultStdout(base); ok {
		for _, line := range bashPPSplitLines(stdout) {
			if !r.bashPPRangeIteration(ctx, rng, line, lineType, nil, nil, nil) {
				return true
			}
		}
		return true
	}
	proc := r.bashPPProcessHandle(base)
	if proc == nil {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: %s.Lines() requires a run result or a live start handle", base)
		return true
	}
	taskCtx := r.bashPPTaskContext(ctx)
	for {
		var line string
		var open bool
		select {
		case line, open = <-proc.Lines():
		case <-taskCtx.Done():
			// Cancellation: the process is killed and reaped; the loop ends
			// without a data line, and Wait reports the cancellation.
			_ = proc.Close()
			r.bashPPTaskCanceled = true
			r.exit.code = 1
			return true
		}
		if !open {
			return true
		}
		if !r.bashPPRangeIteration(ctx, rng, line, lineType, nil, nil, nil) {
			// Leaving early (break/return) does not kill: the handle is
			// still live and Wait/Close settle it.
			return true
		}
	}
}

// bashPPStartCall reports whether a call names the predeclared start.
func bashPPStartCall(c *syntax.BashPPCall) bool {
	return c != nil && c.FuncLit == nil && len(c.Fun) == 1 && c.Fun[0].Value == "start"
}

// bashPPSubshellSource runs argv in an in-process subshell as a
// bashPPProcessSource: stdout through a pipe the substrate reads, stderr
// passed straight through to the shell's stderr (a direct write, so nothing
// can block on it), a context whose cancellation is the kill, and a done
// channel the substrate's Wait blocks on.
type bashPPSubshellSource struct {
	stdout *io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
	status int
	fatal  error
}

func (s *bashPPSubshellSource) Stdout() io.Reader { return s.stdout }
func (s *bashPPSubshellSource) Stderr() io.Reader { return nil }

// Kill cancels the subshell — the exec handler signals the child — and
// closes the read side of the stdout pipe. The close is what keeps a kill
// prompt: a grandchild the signal did not reach (bash's own `sh -c 'sleep'`
// shape) can otherwise hold the write end open indefinitely, and the exec
// handler's copy would wait on it. Closing the reader ends that copy at once.
func (s *bashPPSubshellSource) Kill() {
	s.cancel()
	_ = s.stdout.CloseWithError(context.Canceled)
}
func (s *bashPPSubshellSource) Wait() error {
	<-s.done
	if s.fatal != nil {
		return s.fatal
	}
	if s.status != 0 {
		return &bashPPStatusError{code: s.status}
	}
	return nil
}

// bashPPStatusError carries an in-process subshell's non-zero exit status to
// the substrate, which maps it to a STATUS, not an error, exactly like an
// *exec.ExitError from a real child.
type bashPPStatusError struct{ code int }

func (e *bashPPStatusError) Error() string { return "exit status " + strconv.Itoa(e.code) }

// bashPPProcessWriter owns the locks for a private streaming pipe. Sharing
// the foreground sink lock would deadlock when backpressure stalls a Write
// while the consumer prints a line before requesting the next one.
type bashPPProcessWriter struct {
	w         io.Writer
	mu        sync.Mutex
	logicalMu sync.Mutex
}

func (w *bashPPProcessWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// bashPPStartSubshell launches argv concurrently in a subshell of r and
// returns the live line process over its stdout.
func (r *Runner) bashPPStartSubshell(ctx context.Context, argv []string) *bashPPLineProcess {
	// The child shares the established writer locks with foreground commands,
	// including when an embedder starts a process outside a full File run.
	r.bashPPConcurrency(ctx)
	pr, pw := io.Pipe()
	cctx, cancel := context.WithCancel(ctx)
	src := &bashPPSubshellSource{stdout: pr, cancel: cancel, done: make(chan struct{})}
	// A background subshell: it runs concurrently with the caller, so its
	// environment must be the snapshot a `&` job takes, not the live overlay
	// a `$(...)` capture shares.
	r2 := r.subshell(true)
	r2.bgProcs = r.bgProcs
	r2.jobsReadOnly = true
	r2.stdout = &bashPPProcessWriter{w: pw}
	if !r.functraceEnabled() {
		delete(r2.trapCallbacks, "DEBUG")
	}
	r2.opts[optErrExit] = false
	words := make([]*syntax.Word, len(argv))
	for i, arg := range argv {
		words[i] = &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: arg}}}
	}
	go func() {
		defer close(src.done)
		defer pw.Close()
		defer r2.closeDirFile()
		r2.stmts(cctx, []*syntax.Stmt{{Cmd: &syntax.CallExpr{Args: words}}})
		if r2.exit.fatalExit && r2.exit.err != nil && cctx.Err() == nil {
			src.fatal = r2.exit.err
		}
		src.status = int(r2.exit.code)
	}()
	return bashPPStartLineProcess(ctx, src, 0)
}

// bashPPShortDeclStart binds `p, err := start(...)`. Like run/capture the
// statement itself succeeds; a start that could not launch binds an empty
// handle next to a non-empty err.
func (r *Runner) bashPPShortDeclStart(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	if !bashPPStartCall(d.Call) {
		return false
	}
	if len(d.Lhs) != 2 {
		r.errf("assignment mismatch: %d variable(s) but 2 value(s)\n", len(d.Lhs))
		r.exit = exitStatus{code: 2}
		return true
	}
	argv := r.bashPPCallArgValues(d.Call)
	if len(argv) == 0 {
		r.errf("start: requires a command\n")
		r.exit = exitStatus{code: 2}
		return true
	}
	proc := r.bashPPStartSubshell(ctx, argv)
	id := r.bashPPProcessRegistry().add(proc)
	value := expand.NewObject(map[string]any{bashPPProcessHandleKey: id, "Command": argv[0]})
	r.bashPPBindResultPair(d.Lhs, value, true, "")
	return true
}

// bashPPShortDeclProcessWait binds `status, err := p.Wait()` for a live
// handle. The status is bash's: the exit code, or 128+N for a signal. err is
// non-empty only for a cancellation or an internal streaming failure — never
// for an ordinary non-zero exit, which is the status.
func (r *Runner) bashPPShortDeclProcessWait(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	c := d.Call
	if c == nil || len(c.Fun) != 2 || c.Fun[1].Value != "Wait" || len(c.Args) != 0 {
		return false
	}
	proc := r.bashPPProcessHandle(c.Fun[0].Value)
	if proc == nil {
		return false
	}
	if len(d.Lhs) != 2 {
		r.errf("assignment mismatch: %d variable(s) but 2 value(s)\n", len(d.Lhs))
		r.exit = exitStatus{code: 2}
		return true
	}
	status, err := proc.Wait()
	errMsg := ""
	if err != nil {
		errMsg = fmt.Sprintf("%s.Wait: %v", c.Fun[0].Value, err)
	}
	value := expand.Variable{Set: true, Kind: expand.String, Str: strconv.Itoa(status)}
	r.bashPPBindResultPair(d.Lhs, value, false, errMsg)
	if cell := r.bashPPScope.lookup(d.Lhs[0].Value); cell != nil && d.Lhs[0].Value != "_" {
		cell.declType = bashPPRangeNamedType("int")
	}
	return true
}

// bashPPProcessCommandCall answers `p.Wait()` and `p.Close()` in command
// position for a live handle: Close abandons the process (kill, drain, reap);
// Wait in command position reaps and leaves the status in $?. Both are
// idempotent through the substrate's exact-once Wait.
func (r *Runner) bashPPProcessCommandCall(ctx context.Context, c *syntax.BashPPCall) bool {
	if c == nil || len(c.Fun) != 2 || len(c.Args) != 0 {
		return false
	}
	proc := r.bashPPProcessHandle(c.Fun[0].Value)
	if proc == nil {
		return false
	}
	switch c.Fun[1].Value {
	case "Close":
		if err := proc.Close(); err != nil && !errors.Is(err, context.Canceled) {
			r.errf("%s.Close: %v\n", c.Fun[0].Value, err)
			r.exit = exitStatus{code: 1}
			return true
		}
		r.exit = exitStatus{}
		return true
	case "Wait":
		status, err := proc.Wait()
		if err != nil {
			r.errf("%s.Wait: %v\n", c.Fun[0].Value, err)
			r.exit = exitStatus{code: 1}
			return true
		}
		r.exit = exitStatus{code: uint8(status)}
		return true
	}
	return false
}
