// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pattern"
)

const (
	// shellReplyPS3Var, or PS3, is a special variable in Bash used by the select command,
	// while the shell is awaiting for input. the default value is [shellDefaultPS3]
	shellReplyPS3Var = "PS3"
	// shellDefaultPS3, or #?, is PS3's default value
	shellDefaultPS3 = "#? "
	// shellReplyVar, or REPLY, is a special variable in Bash that is used to store the result of
	// the select command or of the read command, when no variable name is specified
	shellReplyVar = "REPLY"

	// `sh-np` matches bash's own naming convention for procsub
	// named pipes; bash's test fixtures (procsub.tests) `case
	// "$1" in *sh-np*) ...` look for this substring to fall into a
	// hard-coded "file has been consumed" branch.
	fifoNamePrefix = "sh-np-"
)

func (r *Runner) fillExpandConfig(ctx context.Context) {
	r.ectx = ctx
	r.ecfg = &expand.Config{
		Env:  expandEnv{r},
		Lang: r.Dialect(),
		OnNameRefCircular: func(name string) {
			r.errf("%swarning: %s: circular name reference\n",
				r.bashErrPrefix(r.curStmtPos), name)
		},
		OnBadArraySubscript: func(ref string) {
			r.errf("%s%s: bad array subscript\n",
				r.bashErrPrefix(r.curStmtPos), ref)
		},
		OnFormatWarning: func(msg string) {
			// Bash 5.3 printf: numeric-conversion failures
			// (e.g. `printf %d xyz`) print the warning and set
			// exit status 1 but keep formatting (the value falls
			// back to 0). The exit-code wire-up lives in the
			// printf builtin which checks r.lastExpandExit.
			r.errf("%s%s\n", r.bashErrPrefix(r.curStmtPos), msg)
			if !strings.Contains(msg, "missing hex digit") {
				r.lastExpandExit = exitStatus{code: 1}
			}
		},
		CmdSubst: func(w io.Writer, cs *syntax.CmdSubst) error {
			r.yieldAsyncLaunch()
			r.lastExpandCmdSubst = true
			switch len(cs.Stmts) {
			case 0: // nothing to do
				return nil
			case 1: // $(<file)
				word := catShortcutArg(cs.Stmts[0])
				if word == nil {
					break
				}
				path, displayPath, ok := r.catShortcutPath(word)
				if !ok {
					r.lastExpandExit = exitStatus{code: 1}
					if sb, ok := w.(*strings.Builder); ok {
						sb.Reset()
					}
					return nil
				}
				if sb, ok := w.(*strings.Builder); ok {
					sb.Reset()
				}
				f, err := r.open(ctx, path, os.O_RDONLY, 0, false)
				if err != nil {
					r.errf("%s%s: %s\n", r.bashErrPrefix(r.curStmtPos), displayPath, bashOSError(err))
					r.lastExpandExit = exitStatus{code: 1}
					return nil
				}
				_, err = io.Copy(w, f)
				f.Close()
				return err
			}
			// Bash 5.3 funsub `${ cmd; }` (cs.TempFile) and valsub
			// `${|cmd;}` (cs.ReplyVar) run the body in the *caller's*
			// scope — variable assignments inside the substitution
			// persist. Regular `$(...)` uses a subshell.
			//
			// Important: capture into an independent buffer rather
			// than writing straight to w. The w supplied by
			// expand.cmdSubst is the shared bufferAlloc; r.fields()
			// invocations inside the body would otherwise interleave
			// their workspace usage with our capture and double up.
			if cs.TempFile || cs.ReplyVar {
				var captureBuf bytes.Buffer
				oldStdout := r.stdout
				if cs.TempFile {
					r.stdout = &captureBuf
				}
				// The body runs in a function-like variable scope
				// (return / local are legal), but in the same process
				// and caller scope for ordinary assignments (no
				// subshell — unlike $(...) which forks).
				oldInFunc := r.inFunc
				r.inFunc = true
				origEnv := r.writeEnv
				funEnv := newFuncScopeEnviron(r.writeEnv, true)
				if cs.ReplyVar {
					reply := r.lookupVar(shellReplyVar)
					reply.Local = true
					funEnv.values = map[string]namedVariable{
						shellReplyVar: {Name: shellReplyVar, Variable: reply},
					}
				}
				r.writeEnv = funEnv
				oldErrExit := r.opts[optErrExit]
				inheritErrexit := r.opts[optPosix]
				if opt, _ := r.bashOptByName("inherit_errexit"); opt != nil && *opt {
					inheritErrexit = true
				}
				if !inheritErrexit {
					r.opts[optErrExit] = false
				}
				oldFunsubLineOffset := r.funsubLineOffset
				if cs.TempFile && cs.Left.Line() != cs.Right.Line() {
					r.funsubLineOffset = 1
				}
				shellHist.cmdSubst.Add(1)
				r.stmts(ctx, cs.Stmts)
				shellHist.cmdSubst.Add(-1)
				r.funsubLineOffset = oldFunsubLineOffset
				reply := ""
				if cs.ReplyVar {
					reply = r.lookupVar(shellReplyVar).Str
				}
				r.opts[optErrExit] = oldErrExit
				funEnv.release()
				r.writeEnv = origEnv
				r.inFunc = oldInFunc
				r.stdout = oldStdout
				// `return` is local to the funsub body (same as a
				// function); `exit` propagates out (kills the shell)
				// — bash 5.3 treats funsub like a function body for
				// scoping but does NOT swallow `exit`.
				r.exit.returning = false
				// Stash the funsub's exit status the same way `$(…)`
				// does so the assignment path's "if exit.ok() restore
				// lastExpandExit" recovery doesn't wipe our exiting
				// flag for `exit 0` inside the body.
				r.lastExpandExit = r.exit
				// w is expand.cmdSubst's shared bufferAlloc; reset it
				// to discard any residue that r.fields() left there
				// during the body run, then deposit our captured output.
				if sb, ok := w.(*strings.Builder); ok {
					sb.Reset()
				}
				if cs.ReplyVar {
					w.Write([]byte(reply))
				} else {
					w.Write(captureBuf.Bytes())
				}
				return nil
			}
			r2 := r.subshell(false)
			defer r2.closeDirFile()
			r2.bgProcs = r.bgProcs
			r2.jobsReadOnly = true
			// Capture through ONE real pipe, not the Go buffer: bash's
			// $(...) is a fork whose stdout is a single fd, so `2>&1`
			// inside it dups stderr onto that same fd and an external
			// command's out/err arrive in write order. Handing the
			// buffer to r2.stdout instead made `2>&1` alias two
			// distinct os/exec pipes, and `x=$(cmd 2>&1)` came back
			// with stderr after (or before) all of stdout.
			pr, pw, perr := os.Pipe()
			if perr != nil {
				r2.stdout = w
			} else {
				r2.stdout = pw
				drained := make(chan struct{})
				go func() {
					io.Copy(w, pr)
					pr.Close()
					close(drained)
				}()
				defer func() {
					pw.Close()
					<-drained
				}()
			}
			if !r.functraceEnabled() {
				delete(r2.trapCallbacks, "DEBUG")
			}
			// inherit_errexit: bash command substitutions do NOT
			// inherit `set -e` by default — `$(false; echo ok)`
			// must echo `ok` even when the caller is under -e.
			// When inherit_errexit is enabled (or implicitly via
			// POSIX mode), copy -e through to the subshell.
			inheritErrexit := false
			if opt, _ := r.bashOptByName("inherit_errexit"); opt != nil && *opt {
				inheritErrexit = true
			}
			if r.opts[optPosix] {
				inheritErrexit = true
			}
			if inheritErrexit {
				r2.opts[optErrExit] = r.opts[optErrExit]
			} else {
				r2.opts[optErrExit] = false
			}
			shellHist.cmdSubst.Add(1)
			r2.stmts(ctx, cs.Stmts)
			shellHist.cmdSubst.Add(-1)
			r2.exit.exiting = false // subshells don't exit the parent shell
			r2.exit.discarding = false
			r2.exit.returning = false // and they swallow `return` locally
			r.lastExpandExit = r2.exit
			if r2.exit.fatalExit {
				return r2.exit.err // surface fatal errors immediately
			}
			return nil
		},
		ProcSubst: func(ps *syntax.ProcSubst) (string, error) {
			if len(ps.Stmts) == 0 { // nothing to do
				return os.DevNull, nil
			}
			r.yieldAsyncLaunch()

			// The rendezvous is platform-specific: a temp-dir FIFO on Unix,
			// a \\.\pipe\ named pipe on Windows. See procSubstPipe.
			pipe, err := r.newProcSubstPipe(ps.Op == syntax.CmdIn)
			if err != nil {
				return "", err
			}
			path := pipe.path()

			r2 := r.subshell(true)
			stdout := r.origStdout
			// TODO: note that `man bash` mentions that `wait` only waits for the last
			// process substitution as long as it is $!; the logic here would mean we wait for all of them.
			bg := &bgProc{
				done:     make(chan struct{}),
				exit:     new(exitStatus),
				pidReady: make(chan struct{}),
				cmd:      "process substitution",
			}
			r.bgProcs = append(r.bgProcs, bg)
			r.lastBang = bg
			go func() {
				defer r2.closeDirFile()
				defer func() {
					*bg.exit = r2.exit
					close(bg.done)
					select {
					case <-bg.pidReady:
					default:
						close(bg.pidReady)
					}
				}()
				// The substitution's end of the pipe is opened as Bash opens
				// a FIFO: blocking, paired by the kernel with whichever peer
				// opens the other end — an external command or the shell's
				// own redirection alike. Under Bash++ the descriptor is then
				// published to the File's task group so a redirection that
				// waits for a registered peer is released by it; Classic has
				// no group and takes exactly the open it always took.
				var endpoint *bashPPFIFOEntry
				closeEndpoint := func(f *os.File) error {
					if endpoint != nil {
						return endpoint.Close()
					}
					return f.Close()
				}
				switch ps.Op {
				case syntax.CmdIn:
					f, err := pipe.openWriter()
					if err != nil {
						r.errf("cannot open fifo for stdout: %v\n", err)
						pipe.cleanup()
						return
					}
					if c := r2.bashPPConcurrent; c != nil {
						endpoint = r2.bashPPFIFOPublish(c, f, path, os.O_WRONLY)
					}
					r2.stdout = f
					defer func() {
						if err := closeEndpoint(f); err != nil {
							r.errf("closing stdout fifo: %v\n", err)
						}
						pipe.cleanup()
					}()
				case syntax.CmdOut:
					f, err := pipe.openReader()
					if err != nil {
						r.errf("cannot open fifo for stdin: %v\n", err)
						pipe.cleanup()
						return
					}
					if c := r2.bashPPConcurrent; c != nil {
						endpoint = r2.bashPPFIFOPublish(c, f, path, os.O_RDONLY)
					}
					r2.stdin = f
					r2.stdout = stdout

					defer func() {
						closeEndpoint(f)
						pipe.cleanup()
					}()
				default:
					// Should only happen if we forgot a case above.
					panic(fmt.Sprintf("unexpected process substitution operator: %q", ps.Op))
				}
				r2.stmts(ctx, ps.Stmts)
				r2.exit.exiting = false // subshells don't exit the parent shell
				r2.exit.discarding = false
			}()
			return path, nil
		},
	}
	r.ecfg.PromptExpand = r.promptExpand
	r.ecfg.StartTime = r.startTime
	r.updateExpandOpts()
}

// catShortcutArg checks if a statement is of the form "$(<file)". The redirect
// word is returned if there's a match, and nil otherwise.
func catShortcutArg(stmt *syntax.Stmt) *syntax.Word {
	if stmt.Cmd != nil || stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown {
		return nil
	}
	if len(stmt.Redirs) != 1 {
		return nil
	}
	redir := stmt.Redirs[0]
	if redir.Op != syntax.RdrIn {
		return nil
	}
	return redir.Word
}

func (r *Runner) catShortcutPath(word *syntax.Word) (path, displayPath string, ok bool) {
	if r.opts[optPosix] {
		path := r.literal(word)
		return path, path, true
	}
	fields := r.fields(word)
	if len(fields) != 1 {
		r.ambiguousRedirect(word.Pos(), word)
		return "", "", false
	}
	return fields[0], fields[0], true
}

func (r *Runner) ambiguousRedirect(pos syntax.Pos, word *syntax.Word) {
	var b bytes.Buffer
	syntax.NewPrinter().Print(&b, word)
	r.errf("%s%s: ambiguous redirect\n", r.bashErrPrefix(pos), b.String())
}

func (r *Runner) updateExpandOpts() {
	if r.opts[optNoGlob] {
		r.ecfg.ReadDir2 = nil
	} else {
		r.ecfg.ReadDir2 = func(s string) ([]fs.DirEntry, error) {
			s = shellPathJoinAbs(r.Dir, s)
			entries, err := r.readDirHandler(r.handlerCtx(r.ectx, handlerKindReadDir, todoPos), s)
			// Names NTFS could not store verbatim (a:b) come back in
			// their Cygwin encoding; globbing must see the script's
			// spelling.
			return decodeDirEntries(entries, runtime.GOOS == "windows"), err
		}
		r.ecfg.IsSearchable = func(s string) bool {
			s = shellPathJoinAbs(r.Dir, s)
			return r.access(r.ectx, s, access_X_OK) == nil
		}
		if runtime.GOOS == "windows" {
			// os.ReadDir can enumerate a directory after chmod records a
			// POSIX mode without read permission in its ACL. Only glob
			// enumeration needs the read check; literal path components
			// can still be traversed when the directory is searchable.
			r.ecfg.IsReadable = func(s string) bool {
				s = shellPathJoinAbs(r.Dir, s)
				return r.access(r.ectx, s, access_R_OK) == nil
			}
		}
		// `**` must stop at a symlinked directory. The globber only has
		// the shell's spelling of the path, which os.Lstat cannot open on
		// Windows, so the stat goes through the runner's handler like
		// every other one.
		r.ecfg.Lstat = func(s string) (fs.FileInfo, error) {
			return r.lstat(r.ectx, s)
		}
		r.ecfg.Stat = func(s string) (fs.FileInfo, error) {
			return r.stat(r.ectx, s)
		}
	}
	r.ecfg.GlobStar = r.opts[optGlobStar]
	if opt, _ := r.bashOptByName("globskipdots"); opt != nil {
		r.ecfg.GlobSkipDots = *opt
	}
	r.ecfg.DotGlob = r.opts[optDotGlob]
	r.ecfg.NoCaseGlob = r.opts[optNoCaseGlob]
	if opt, _ := r.bashOptByName("nocasematch"); opt != nil {
		r.ecfg.NoCaseMatch = *opt
	}
	r.ecfg.NullGlob = r.opts[optNullGlob]
	if opt, _ := r.bashOptByName("failglob"); opt != nil {
		r.ecfg.FailGlob = *opt
	}
	r.ecfg.NoUnset = r.opts[optNoUnset]
	r.ecfg.ExtGlob = r.opts[optExtGlob]
	if opt, _ := r.bashOptByName("patsub_replacement"); opt != nil {
		r.ecfg.PatSubReplacement = *opt
	}
	r.ecfg.Posix = r.opts[optPosix]
}

// standaloneArithParseError reports whether err is a parse/syntax error from a
// top-level `$(( ))` expansion (ArithmError.Standalone) — as opposed to an array
// subscript, a malformed length substitution, or arithmetic recursion. Bash 5.3
// makes such a parse error fatal to the command (status 1).
func standaloneArithParseError(err error) bool {
	if err == nil {
		return false
	}
	var ae *expand.ArithmError
	if errors.As(err, &ae) && ae.Standalone {
		return strings.Contains(ae.Error(), "arithmetic syntax error")
	}
	return false
}

// discardExpansionLine unwinds the current command and skips the remaining
// statements on its physical input line. Run resumes at the next line.
func (r *Runner) discardExpansionLine() {
	r.exit.discarding = true
	r.discardRestOfLine = r.curStmtPos.Line()
}

func (r *Runner) expandErr(err error) {
	if err == nil {
		return
	}
	origErr := err
	var unsetParam expand.UnsetParameterError
	if r.bashCompatErrors {
		if strings.Contains(err.Error(), "readonly variable") {
			// Keep readonly assignment diagnostics plain, even when
			// they originated inside arithmetic evaluation.
		} else if arithErr, expr := innermostArithmError(err); arithErr != nil && !errors.As(err, &unsetParam) {
			err = r.bashArithmError(expr, arithErr.Err, false, arithErr.Text)
		}
	}
	errMsg := err.Error()
	var badSubst expand.BadSubstitutionError
	var indirErr expand.IndirectExpansionError
	if r.bashCompatErrors && errors.As(err, &badSubst) && badSubst.Node != nil {
		if src := r.sourceTextRange(badSubst.Node.Pos(), badSubst.Node.End(), false); src != "" {
			errMsg = src + ": bad substitution"
		}
	}
	if r.bashCompatErrors && strings.Contains(errMsg, "substring expression < 0") {
		errMsg = r.rewriteSubstringLengthError(errMsg)
	}
	// Bash 5.3 prefixes expansion errors (`$(( ))`, `${!x}`,
	// `let`, etc.) with the standard `<file>: line N:` framing.
	// The wrapper isn't applied when the error already carries
	// it (arithm()'s own path) or when the runner doesn't
	// request bash-compatible wording.
	if r.bashCompatErrors && !strings.HasPrefix(errMsg, r.bashErrPrefix(r.curStmtPos)) {
		if strings.HasPrefix(errMsg, "command substitution: ") {
			prefix := r.filename
			if prefix == "" {
				prefix = "bash"
			}
			errMsg = prefix + ": " + errMsg
		} else if errors.As(err, &unsetParam) || looksLikeExpandError(errMsg) {
			errMsg = r.bashErrPrefix(r.curStmtPos) + errMsg
		}
	}
	fmt.Fprintln(r.bashPPWriter(r.stderr), errMsg)
	r.reportError("expand", r.curStmtPos, "", errMsg, 1)
	if strings.Contains(errMsg, "arithmetic syntax error: invalid arithmetic operator") {
		r.lastExpandExit = exitStatus{code: 1}
	}
	if strings.Contains(errMsg, "exponent less than 0") {
		r.lastExpandExit = exitStatus{code: 1}
	}
	if looksLikeArithExpandError(errMsg) {
		r.lastExpandExit = exitStatus{code: 1}
	}
	if strings.Contains(errMsg, `operand expected (error token is """ ")`) {
		r.exit.code = 1
		r.exit.exiting = true
		if !r.opts[optPosix] {
			r.discardExpansionLine()
		}
	}
	switch {
	case errors.As(err, &expand.UnsetParameterError{}):
		if r.interactiveShell {
			// Bash: an unset-parameter expansion error (`${a?}`,
			// `${a:?}`) is fatal for a non-interactive shell but
			// merely prints the diagnostic, sets $? = 1, and aborts
			// the current command in an interactive shell — the shell
			// keeps reading input.
			r.exit.code = 1
			r.lastExpandExit = exitStatus{code: 1}
			r.exit.exiting = true
			r.exit.discarding = true
			return
		}
	case strings.Contains(errMsg, "readonly variable"):
		r.exit.code = 1
		// Bash: a variable-assignment error during word expansion
		// (`${v:=val}` with v readonly) is fatal in POSIX mode and
		// DISCARDs the current top-level command otherwise.
		// Arithmetic readonly failures (`$((v=1))`) only fail the
		// expansion — the next command still runs.
		if arithErr, _ := innermostArithmError(err); arithErr == nil {
			r.exit.exiting = true
			if !r.opts[optPosix] {
				r.exit.discarding = true
			}
		} else if r.opts[optPosix] && !r.interactiveShell {
			// POSIX mode: a readonly-assignment error inside arithmetic
			// (`$((v=1))`) is a fatal variable-assignment error for a
			// non-interactive shell, even though the default runner only
			// fails the expansion and keeps going.
			r.exit.exiting = true
		}
		return
	case errors.As(err, &badSubst):
		// Bash discards the rest of the input line, then reads the next one.
		r.exit.code = 1
		r.lastExpandExit = exitStatus{code: 1}
		if r.opts[optPosix] || (r.bashCompatErrors && !r.interactiveShell) {
			r.exit.exiting = true
			if !r.opts[optPosix] {
				r.discardExpansionLine()
			}
		}
		return
	case strings.Contains(errMsg, "bad substitution"):
		r.exit.code = 1
		r.lastExpandExit = exitStatus{code: 1}
		if r.bashCompatErrors && !r.interactiveShell {
			r.exit.exiting = true
			if !r.opts[optPosix] {
				r.discardExpansionLine()
			}
		}
		return
	case strings.Contains(errMsg, "cannot assign in this way"):
		// Bash: assigning to a positional or special parameter via
		// `${1=x}`, `${@=x}`, or `${!=x}` is an error that sets $? = 1 and
		// discards the rest of the current physical line before resuming at
		// the next line (the same in POSIX and default modes). The pure-Go
		// default runner is more lenient and keeps running the rest of the
		// line, so only abort it under bash-compatible error reporting.
		r.exit.code = 1
		r.lastExpandExit = exitStatus{code: 1}
		if r.bashCompatErrors {
			r.exit.exiting = true
			r.exit.discarding = true
			r.discardRestOfLine = r.curStmtPos.Line()
		}
		return
	case strings.Contains(errMsg, "invalid variable name"),
		strings.Contains(errMsg, "not a valid identifier"):
		// An invalid indirection target in a non-interactive Bash script
		// terminates the shell after the diagnostic. The library's legacy
		// error mode retains its previous continuation behavior.
		r.exit.code = 1
		r.lastExpandExit = exitStatus{code: 1}
		// A `((...))` command only fails with $? = 1 and the line goes on
		// (nameref11.sub: `declare -n r; ((r=0)) ; unset -n r`).
		// So does $((...)) (arith10.sub: `: $(( a[""]=25 )); declare -p a`);
		// only a parameter expansion (`${r=}` on an empty nameref) discards.
		var identArith *expand.ArithmError
		if r.bashCompatErrors && !r.interactiveShell && !strings.Contains(errMsg, "((: ") &&
			!errors.As(origErr, &identArith) {
			r.exit.exiting = true
			if !r.opts[optPosix] {
				r.discardExpansionLine()
			}
		}
		return
	case strings.Contains(errMsg, "no match: "):
		r.exit.code = 1
		r.lastExpandExit = exitStatus{code: 1}
		return
	case strings.Contains(errMsg, "substring expression < 0"):
		// Bash: a substring/offset expansion error (`${a[@]:1:-3}`)
		// skips the current simple command without changing $?
		// (the error is a non-fatal diagnostic; $? stays 0).
		// Subsequent commands on the same physical line still run.
		r.expandRunExit = exitStatus{code: 1}
		r.exit.exiting = true
		r.exit.discarding = true
		return
	case errors.As(err, &indirErr):
		if indirErr.NonFatal {
			// `${!var:-def}` with var unset fails the expansion. A
			// non-interactive Bash script exits after this error.
			r.exit.code = 1
			r.lastExpandExit = exitStatus{code: 1}
			if r.bashCompatErrors && !r.interactiveShell {
				r.exit.exiting = true
				if !r.opts[optPosix] {
					r.discardExpansionLine()
				}
			}
			return
		}
		// Bare `${!var}` with var unset is fatal in bash.
	default:
		return // other cases do not exit
	}
	r.exit.code = 1
	r.exit.exiting = true
}

func (r *Runner) rewriteSubstringLengthError(errMsg string) string {
	expr, ok := strings.CutSuffix(errMsg, ": substring expression < 0")
	if !ok || expr == "" || len(r.bashSource) == 0 || !r.curStmtPos.IsValid() {
		return errMsg
	}
	lineStart := 0
	for line := uint(1); line < r.curStmtPos.Line() && lineStart < len(r.bashSource); lineStart++ {
		if r.bashSource[lineStart] == '\n' {
			line++
		}
	}
	if lineStart > len(r.bashSource) {
		return errMsg
	}
	lineEnd := lineStart
	for lineEnd < len(r.bashSource) && r.bashSource[lineEnd] != '\n' {
		lineEnd++
	}
	line := string(r.bashSource[lineStart:lineEnd])
	needle := ":" + expr + "}"
	if strings.Contains(line, needle) {
		return errMsg
	}
	for i := 0; i < len(line); i++ {
		if line[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		if !strings.HasPrefix(line[j:], expr+"}") {
			continue
		}
		return line[i+1:j] + expr + ": substring expression < 0"
	}
	return errMsg
}

// looksLikeExpandError covers the wordings produced by [expand.Arithm]
// and [expand.paramExp] so we can add the bash 5.3 `<file>: line N:`
// prefix uniformly. Arithmetic-evaluation, indirect-expansion, and
// other parameter-expansion runtime errors all qualify.
func looksLikeExpandError(msg string) bool {
	switch {
	case strings.Contains(msg, "error token is"),
		strings.Contains(msg, "division by"),
		strings.Contains(msg, "attempted assignment to non-variable"),
		strings.Contains(msg, "arithmetic syntax error"),
		strings.Contains(msg, "invalid arithmetic"),
		strings.Contains(msg, "invalid integer constant"),
		strings.Contains(msg, "value too great for base"),
		strings.Contains(msg, "exponent less than 0"),
		strings.Contains(msg, "expression expected"),
		strings.Contains(msg, "invalid indirect expansion"),
		strings.Contains(msg, "bad substitution"),
		strings.HasPrefix(msg, "command substitution: "),
		strings.Contains(msg, "unbound variable"),
		strings.Contains(msg, "readonly variable"),
		strings.Contains(msg, "bad array subscript"),
		strings.Contains(msg, "no match: "),
		strings.Contains(msg, "substring expression < 0"),
		strings.Contains(msg, "cannot assign in this way"),
		strings.Contains(msg, "parameter not set"),
		strings.Contains(msg, "parameter null or not set"),
		strings.Contains(msg, "not a valid identifier"),
		strings.Contains(msg, "invalid variable name"):
		return true
	}
	return false
}

func looksLikeArithExpandError(msg string) bool {
	switch {
	case strings.Contains(msg, "invalid integer constant"),
		strings.Contains(msg, "value too great for base"),
		strings.Contains(msg, "invalid number"),
		strings.Contains(msg, "arithmetic syntax error in expression"):
		return true
	}
	const quotedTokenPrefix = "error token is \"'"
	if idx := strings.Index(msg, quotedTokenPrefix); idx >= 0 {
		idx += len(quotedTokenPrefix)
		return idx < len(msg) && msg[idx] >= '0' && msg[idx] <= '9'
	}
	return false
}

func (r *Runner) arithm(expr syntax.ArithmExpr) int {
	n, err := expand.Arithm(r.ecfg, expr)
	var unsetParam expand.UnsetParameterError
	if err != nil && r.bashCompatErrors {
		if strings.Contains(err.Error(), "readonly variable") {
			// Bash reports arithmetic assignments to readonly vars
			// as a plain variable diagnostic, not as an arithmetic
			// expression error with an error token.
		} else if errors.As(err, &unsetParam) {
			// nounset failures in ((...)) are plain parameter
			// diagnostics, and abort before later arithmetic commands.
		} else if arithErr, arithExpr := innermostArithmError(err); arithErr != nil && !errors.As(err, &unsetParam) {
			if arithExpr == nil {
				arithExpr = expr
			}
			err = r.bashArithmError(arithExpr, arithErr.Err, true, arithErr.Text)
		} else {
			err = r.bashArithmError(expr, err, true, "")
		}
	}
	r.lastArithErr = err
	r.expandErr(err)
	return n
}

func (r *Runner) letArithm(expr syntax.ArithmExpr) int {
	if exprText, ok := r.letEmptyQuotedBinary(expr); ok {
		r.errf("%slet: %s: arithmetic syntax error: operand expected (error token is \"\"\"\")\n",
			r.bashErrPrefix(expr.Pos()), exprText)
		r.exit.code = 1
		r.lastArithErr = fmt.Errorf("let: %s: arithmetic syntax error: operand expected (error token is \"\"\"\")", exprText)
		return 0
	}
	if text, ok := r.assocExpandOnceLetQuotedIndex(expr); ok {
		quoted := `"` + text + `"`
		err := fmt.Errorf("%s: arithmetic syntax error: operand expected (error token is \"%s\")",
			quoted, quoted)
		r.lastArithErr = err
		r.expandErr(err)
		r.exit.code = 1
		r.exit.exiting = true
		if !r.opts[optPosix] {
			r.exit.discarding = true
		}
		return 0
	}
	if exprText, token, ok := r.malformedLetAssocSubscript(expr); ok {
		prefix := r.filename
		if prefix == "" {
			prefix = "bash"
		}
		err := fmt.Errorf("%s: line %d: let: %s: bad array subscript (error token is %q)",
			prefix, expr.Pos().Line(), exprText, token)
		r.lastArithErr = err
		r.expandErr(err)
		return 0
	}
	prevLetArithmetic := r.ecfg.LetArithmetic
	r.ecfg.LetArithmetic = true
	prevAssocExpandOnce := r.ecfg.AssocExpandOnce
	if opt, _ := r.bashOptByName("assoc_expand_once"); opt != nil {
		r.ecfg.AssocExpandOnce = *opt
	}
	n, err := expand.Arithm(r.ecfg, expr)
	r.ecfg.LetArithmetic = prevLetArithmetic
	r.ecfg.AssocExpandOnce = prevAssocExpandOnce
	exprTextOverride := ""
	if arithErr, arithExpr := innermostArithmError(err); arithErr != nil {
		if arithExpr != nil {
			expr = arithExpr
		}
		exprTextOverride = arithErr.Text
		err = arithErr.Err
	}
	// A re-parsed $( … )/`…` operand reports as "<operand>: <msg>" with
	// no `let:` framing, the same as arithmetic expansion and commands.
	if err != nil && r.bashCompatErrors && exprTextOverride != "" {
		if msg := err.Error(); strings.Contains(msg, "arithmetic syntax error: operand expected") &&
			(strings.Contains(msg, "error token is \"$") || strings.Contains(msg, "error token is \"`")) {
			err = r.bashArithmError(expr, err, false, exprTextOverride)
			r.lastArithErr = err
			r.expandErr(err)
			return n
		}
	}
	if err != nil && r.bashCompatErrors {
		exprText := r.arithmSourceText(expr, false)
		if exprTextOverride != "" {
			exprText = exprTextOverride
		}
		if exprText == "" {
			exprText = printArithmExpr(expr)
		}
		if w, ok := expr.(*syntax.Word); ok && exprTextOverride == "" {
			exprText = r.literal(w)
		}
		if idx := strings.Index(exprText, ": let: "); idx >= 0 {
			exprText = exprText[idx+len(": let: "):]
		}
		if inner := err.Error(); strings.Contains(inner, ": let: ") {
			if idx := strings.Index(inner, ": let: "); idx >= 0 {
				rest := inner[idx+len(": let: "):]
				if strings.HasPrefix(rest, exprText+": ") {
					err = fmt.Errorf("%s", strings.TrimPrefix(rest, exprText+": "))
				} else if j := strings.Index(rest, ": "); j >= 0 {
					err = fmt.Errorf("%s", rest[j+len(": "):])
				}
			}
		}
		prefix := r.filename
		if prefix == "" {
			prefix = "bash"
		}
		if msg := err.Error(); strings.HasPrefix(msg, `"`) &&
			strings.Contains(msg, ": arithmetic syntax error") {
			err = fmt.Errorf("%s: line %d: %s", prefix, expr.Pos().Line(), msg)
		} else {
			err = fmt.Errorf("%s: line %d: let: %s: %s",
				prefix, expr.Pos().Line(), exprText, err)
		}
	}
	r.lastArithErr = err
	r.expandErr(err)
	return n
}

func (r *Runner) letEmptyQuotedBinary(expr syntax.ArithmExpr) (string, bool) {
	exprText := ""
	switch expr := expr.(type) {
	case *syntax.Word:
		exprText = r.literal(expr)
	case *syntax.BinaryArithm:
		if expr.Op == syntax.Sub && arithEmptyDoubleQuotedWord(expr.Y) {
			exprText = r.arithmSourceText(expr, false)
			if exprText == "" {
				exprText = printArithmExpr(expr)
			}
		}
	}
	if strings.Contains(exprText, `- ""`) {
		return exprText, true
	}
	return "", false
}

func (r *Runner) assocExpandOnceLetQuotedIndex(expr syntax.ArithmExpr) (string, bool) {
	opt, _ := r.bashOptByName("assoc_expand_once")
	if opt == nil || !*opt {
		return "", false
	}
	lvalue := ""
	switch expr := expr.(type) {
	case *syntax.BinaryArithm:
		switch expr.Op {
		case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn,
			syntax.MulAssgn, syntax.QuoAssgn, syntax.RemAssgn,
			syntax.AndAssgn, syntax.OrAssgn, syntax.XorAssgn,
			syntax.ShlAssgn, syntax.ShrAssgn:
			if w, ok := expr.X.(*syntax.Word); ok {
				lvalue = r.literal(w)
			}
		}
	case *syntax.Word:
		lvalue = r.literal(expr)
		if eq := strings.IndexByte(lvalue, '='); eq >= 0 {
			lvalue = lvalue[:eq]
			open := strings.IndexByte(lvalue, '[')
			close := strings.LastIndexByte(lvalue, ']')
			if open <= 0 || close <= open+1 || close != len(lvalue)-1 {
				return "", false
			}
			sub := lvalue[open+1 : close]
			if sub == `""` {
				return "", true
			}
			return "", false
		}
	}
	if lvalue == "" {
		return "", false
	}
	open := strings.IndexByte(lvalue, '[')
	close := strings.LastIndexByte(lvalue, ']')
	if open <= 0 || close <= open+1 || close != len(lvalue)-1 {
		return "", false
	}
	sub := lvalue[open+1 : close]
	if len(sub) < 2 || sub[0] != '"' || sub[len(sub)-1] != '"' {
		return "", false
	}
	text := sub[1 : len(sub)-1]
	if text == "" || strings.TrimSpace(text) == "" {
		return text, true
	}
	return "", false
}

func arithEmptyDoubleQuotedWord(expr syntax.ArithmExpr) bool {
	word, ok := expr.(*syntax.Word)
	if !ok || len(word.Parts) != 1 {
		return false
	}
	dq, ok := word.Parts[0].(*syntax.DblQuoted)
	return ok && len(dq.Parts) == 0
}

func (r *Runner) malformedLetAssocSubscript(expr syntax.ArithmExpr) (exprText, token string, ok bool) {
	un, ok := expr.(*syntax.UnaryArithm)
	if !ok || (un.Op != syntax.Inc && un.Op != syntax.Dec) {
		return "", "", false
	}
	lval, ok := arithLetWordLvalue(un.X)
	if !ok {
		return "", "", false
	}
	vr := r.lookupVar(lval.name)
	if vr.Kind != expand.Associative {
		return "", "", false
	}
	key := r.assocAssignKey(lval.index)
	idx := strings.Index(key, "],")
	if idx < 0 {
		return "", "", false
	}
	token = key[idx+2:]
	if sp := strings.IndexAny(token, " \t\n"); sp >= 0 {
		token = token[:sp]
	}
	return lval.name + "[" + key[:idx+2] + token, token, true
}

type arithLetLvalue struct {
	name  string
	index *syntax.Word
}

func arithLetWordLvalue(expr syntax.ArithmExpr) (arithLetLvalue, bool) {
	w, ok := expr.(*syntax.Word)
	if !ok || len(w.Parts) != 1 {
		return arithLetLvalue{}, false
	}
	pe, ok := w.Parts[0].(*syntax.ParamExp)
	if !ok || pe.Param == nil || pe.Index == nil {
		return arithLetLvalue{}, false
	}
	index, ok := pe.Index.(*syntax.Word)
	if !ok {
		return arithLetLvalue{}, false
	}
	return arithLetLvalue{name: pe.Param.Value, index: index}, true
}

func printArithmExpr(expr syntax.ArithmExpr) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, &syntax.Stmt{
		Cmd: &syntax.ArithmCmd{X: expr},
	})
	s := strings.TrimSpace(b.String())
	s = strings.TrimPrefix(s, "((")
	s = strings.TrimSuffix(s, "))")
	return strings.TrimSpace(s)
}

func innermostArithmError(err error) (*expand.ArithmError, syntax.ArithmExpr) {
	var found *expand.ArithmError
	var expr syntax.ArithmExpr
	for err != nil {
		var arithErr *expand.ArithmError
		if !errors.As(err, &arithErr) {
			break
		}
		found = arithErr
		if arithErr.Expr != nil {
			expr = arithErr.Expr
		}
		err = arithErr.Err
	}
	return found, expr
}

// bashArithmError reformats an arithmetic-evaluation error so it
// matches bash 5.3's diagnostic shape:
//
//	<file>: line N: ((: <expr> : <message> (error token is "<token> ")
//
// The offending token is approximated as the right-hand side of a
// division/remainder when the error is "division by zero"; otherwise
// it's the whole expression. Bash includes a trailing space inside the
// quoted token — preserved here so tests diff cleanly.
func (r *Runner) bashArithmError(expr syntax.ArithmExpr, err error, command bool, exprTextOverride string) error {
	msg := err.Error()
	bashMsg := msg
	switch {
	case strings.Contains(msg, "division by zero"), strings.Contains(msg, "division by 0"):
		bashMsg = "division by 0"
	default:
		// Other errors keep their wording; still wrap with the file
		// prefix and ((: ...) frame so they're parseable.
	}
	if strings.Contains(bashMsg, "expression recursion level exceeded") {
		return fmt.Errorf("%s%s", r.bashErrPrefix(r.curStmtPos), bashMsg)
	}
	// In $((...)) Bash's error token runs to the end of the source text,
	// so it ends in exactly the whitespace the source had before `))`:
	// $((2**-1)) -> "1", $(( 2**-1 )) -> "1 " (arith.tests).
	srcTrail := ""
	if !command {
		if exprTextOverride != "" {
			// Text re-parsed from an expansion arrives trimmed and has no
			// source positions; read the whitespace before the `))` of the
			// statement's $((...)) instead (arith.tests: `$(( 4 ? : $A ))`).
			srcTrail = r.stmtArithmExpTrail()
		} else if src := r.arithmSourceText(expr, true); src != "" {
			srcTrail = src[len(strings.TrimRight(src, " \t")):]
		}
	}
	if !command && strings.Contains(bashMsg, "assignment requires lvalue") {
		bashMsg = strings.ReplaceAll(bashMsg, `error token is "++ "`, `error token is "++`+srcTrail+`"`)
	}
	if strings.Contains(bashMsg, "not a valid identifier") {
		if command {
			return fmt.Errorf("%s((: %s", r.bashErrPrefix(r.curStmtPos), bashMsg)
		}
		return fmt.Errorf("%s%s", r.bashErrPrefix(r.curStmtPos), bashMsg)
	}
	// Printer.Print doesn't accept bare ArithmExpr nodes; wrap in an
	// ArithmCmd via a Stmt so the printer's command path handles it,
	// then strip the surrounding "(( ... ))" and any escaped-newline
	// continuations the printer inserts for multi-line layout.
	printArithm := func(e syntax.ArithmExpr) string {
		if e == nil {
			return ""
		}
		extendExpr := !command && !arithmInvalidLiteralMsg(bashMsg)
		if s := r.arithmSourceText(e, extendExpr); s != "" {
			if command {
				s = strings.TrimRight(s, " \t")
			}
			return s
		}
		var b strings.Builder
		syntax.NewPrinter().Print(&b, &syntax.Stmt{
			Cmd: &syntax.ArithmCmd{X: e},
		})
		s := b.String()
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "((")
		s = strings.TrimSuffix(s, "))")
		// Collapse "\\\n" (printer's line-continuation) and any
		// surrounding whitespace into a single space.
		s = strings.ReplaceAll(s, "\\\n", " ")
		s = strings.Join(strings.Fields(s), " ")
		return compactArithAssign(s)
	}
	exprText := printArithm(expr)
	if exprTextOverride != "" {
		exprText = exprTextOverride
		if command {
			exprText = strings.TrimRight(exprText, " \t")
		}
	}
	if strings.Contains(bashMsg, "not a valid arithmetic operator") {
		token := strings.TrimSpace(exprText)
		if fields := strings.Fields(token); len(fields) > 0 {
			token = fields[len(fields)-1]
		}
		bashMsg = fmt.Sprintf("arithmetic syntax error in expression (error token is %q)", token)
	}
	// Bash's error token is the unparsed remainder of the expression from
	// the point parsing failed. When a single-quoted operand leads the
	// expression (`'1' + 2`), that remainder is the whole text, so widen
	// the token to exprText. But when the bad operand sits mid-expression
	// — e.g. the RHS of `A['y'] = 'y'`, where parsing succeeds through the
	// `=` — bash reports just that operand (`'y' `), which the inner
	// ArithmError already carries; leave it intact.
	if strings.Contains(bashMsg, "arithmetic syntax error: operand expected") &&
		strings.Contains(bashMsg, "error token is \"'") && strings.Contains(exprText, "'") &&
		strings.HasPrefix(strings.TrimLeft(exprText, " \t"), "'") {
		if start := strings.Index(bashMsg, "error token is \""); start >= 0 {
			tokenStart := start + len("error token is \"")
			if tokenEnd := strings.IndexByte(bashMsg[tokenStart:], '"'); tokenEnd >= 0 {
				newToken := exprText
				// In `(( ... ))` command mode bash appends a trailing
				// space to the echoed error token (`'assoc[…]++' `); the
				// `$(( ... ))` expansion form does not (`'1' + 2`).
				if command && !strings.HasSuffix(newToken, " ") {
					newToken += " "
				}
				bashMsg = bashMsg[:tokenStart] + newToken + bashMsg[tokenStart+tokenEnd:]
			}
		}
	}
	if !command && strings.Contains(bashMsg, "error token is \"$") {
		exprText = strings.ReplaceAll(exprText, `\$`, "$")
		if start := strings.Index(bashMsg, "error token is \""); start >= 0 {
			tokenStart := start + len("error token is \"")
			if tokenEnd := strings.IndexByte(bashMsg[tokenStart:], '"'); tokenEnd >= 0 {
				token := bashMsg[tokenStart : tokenStart+tokenEnd]
				if strings.Contains(exprText, token+" ") && !strings.HasSuffix(token, " ") {
					bashMsg = bashMsg[:tokenStart] + token + " " + bashMsg[tokenStart+tokenEnd:]
				}
			}
		}
	}

	tokenText := "0"
	exactToken := false
	commandSep := " : "
	if b, ok := expr.(*syntax.BinaryArithm); ok && b.Op == syntax.TernQuest {
		branchText := func(e syntax.ArithmExpr) string {
			if w, ok := e.(*syntax.Word); ok {
				if s, err := expand.Literal(r.ecfg, w); err == nil {
					return s
				}
			}
			if s := r.arithmSourceText(e, false); s != "" {
				return strings.TrimSpace(s)
			}
			return printArithm(e)
		}
		if b2, ok := b.Y.(*syntax.BinaryArithm); ok && b2.Op == syntax.TernColon {
			if word, _ := b2.X.(*syntax.Word); arithWordEmpty(word) {
				left := branchText(b.X)
				right := branchText(b2.Y)
				if exprTextOverride != "" {
					if _, after, ok := strings.Cut(exprTextOverride, "?"); ok {
						if _, afterColon, ok := strings.Cut(after, ":"); ok {
							right = strings.TrimSpace(afterColon)
						}
					}
				}
				exprText = strings.TrimSpace(left+" ? : "+right) + " "
				bashMsg = "expression expected"
				tokenText = ": " + right + " "
				exactToken = true
				if !command {
					exprText = strings.TrimRight(exprText, " \t") + srcTrail
					tokenText = strings.TrimRight(tokenText, " \t") + srcTrail
				}
			} else if word, _ := b2.Y.(*syntax.Word); arithWordEmpty(word) {
				if b2.OpPos == b.OpPos {
					tokenText = branchText(b2.X)
					if command {
						tokenText += " "
					} else {
						tokenText += srcTrail
					}
					bashMsg = "`:' expected for conditional expression"
				} else {
					tokenText = ":"
					if command {
						tokenText += " "
					} else {
						tokenText += srcTrail
					}
					bashMsg = "expression expected"
				}
				exactToken = true
			}
		}
	}
	if b, ok := expr.(*syntax.BinaryArithm); ok {
		switch b.Op {
		case syntax.Quo, syntax.Rem, syntax.QuoAssgn, syntax.RemAssgn:
			tokenText = r.arithmSourceText(b.Y, false)
			if tokenText == "" {
				tokenText = printArithm(b.Y)
			}
			if command {
				exactToken = true
				commandSep = ": "
			}
		case syntax.Pow:
			if strings.Contains(bashMsg, "exponent less than 0") {
				tokenText = strings.TrimPrefix(strings.TrimSpace(r.arithmSourceText(b.Y, false)), "-")
				if tokenText == "" {
					tokenText = strings.TrimPrefix(strings.TrimSpace(printArithm(b.Y)), "-")
				}
				if !command {
					tokenText += srcTrail
					exactToken = true
				}
			}
		case syntax.Mul, syntax.Add,
			syntax.Shl, syntax.Shr, syntax.Lss, syntax.Gtr, syntax.Leq, syntax.Geq,
			syntax.Eql, syntax.Neq, syntax.And, syntax.Xor, syntax.Or,
			syntax.AndArit, syntax.OrArit, syntax.XorBool:
			if strings.Contains(bashMsg, "exponent less than 0") {
				tokenText = r.sourceTextRange(b.OpPos, b.Y.End(), true)
				if tokenText == "" {
					tokenText = b.Op.String() + " " + printArithm(b.Y)
				}
				exactToken = true
			}
		case syntax.Sub:
			if command && strings.Contains(bashMsg, "arithmetic syntax error: operand expected") &&
				arithEmptyDoubleQuotedWord(b.Y) {
				left := r.arithmSourceText(b.X, false)
				if left == "" {
					left = printArithm(b.X)
				}
				exprText = strings.TrimSpace(left) + " - "
				bashMsg = "arithmetic syntax error: operand expected (error token is \"-  \")"
			}
		case syntax.Assgn, syntax.AddAssgn, syntax.SubAssgn,
			syntax.MulAssgn, syntax.AndAssgn, syntax.OrAssgn,
			syntax.XorAssgn, syntax.ShlAssgn, syntax.ShrAssgn,
			syntax.PowAssgn:
			if strings.Contains(bashMsg, "attempted assignment to non-variable") {
				tokenText = r.sourceTextRange(b.OpPos, b.Y.End(), true)
				if tokenText == "" {
					tokenText = b.Op.String() + compactArithAssign(printArithm(b.Y))
				}
				exactToken = !strings.ContainsAny(tokenText, " \t")
			}
		}
	}
	if command && strings.Contains(bashMsg, "division by 0") {
		if strings.Contains(exprText, " / ") {
			exactToken = false
			commandSep = " : "
		} else {
			exactToken = true
			commandSep = ": "
		}
	}
	prefix := r.filename
	if prefix == "" {
		prefix = "bash"
	}
	line := 0
	if expr != nil {
		line = int(expr.Pos().Line())
	}
	if !command && r.curStmtPos.IsValid() {
		if expr != nil && expr.Pos().IsValid() && expr.Pos().Line() > r.curStmtPos.Line() &&
			!strings.HasPrefix(exprText, "\n") {
			exprText = "\n" + exprText
		}
		line = int(r.curStmtPos.Line())
	}
	// A multi-line `(( ))` command reports against its closing `))`
	// line. The ArithmCmd dispatch parks curStmtPos at the command end
	// for exactly this purpose, so prefer it when it lands on a later
	// line than the expression itself (single-line forms are unchanged).
	if command && r.curStmtPos.IsValid() && int(r.curStmtPos.Line()) > line {
		line = int(r.curStmtPos.Line())
	}
	if exprTextOverride != "" && r.curStmtPos.IsValid() {
		line = int(r.curStmtPos.Line())
	}
	compactErrSep := false
	if command && (strings.HasSuffix(exprText, "=") || strings.HasSuffix(exprText, "= ")) &&
		strings.Contains(bashMsg, "arithmetic syntax error: operand expected") &&
		strings.Contains(bashMsg, "error token is") {
		exprText = strings.TrimRight(exprText, " \t")
		compactErrSep = true
	}
	if command &&
		strings.Contains(bashMsg, "arithmetic syntax error in expression") &&
		strings.Contains(bashMsg, "error token is") {
		compactErrSep = true
	}
	// `((++))` and `(( ++))` report "++" / "+"; with whitespace before
	// `))` Bash keeps it in both (arith1.sub: `(( ++ ))`).
	if src := r.arithmSourceText(expr, true); command && strings.TrimSpace(exprText) == "++" &&
		!strings.HasSuffix(src, " ") && !strings.HasSuffix(src, "\t") &&
		strings.Contains(bashMsg, "arithmetic syntax error: operand expected") {
		exprText = "++"
		bashMsg = strings.ReplaceAll(bashMsg, `error token is "+ "`, `error token is "+"`)
		compactErrSep = true
	}
	// If the inner message already carries its own "(error token is ...)"
	// suffix (e.g. the "attempted assignment to non-variable" path for
	// `7++` / `7=4`), skip appending our outer copy — bash emits one
	// instance, not two.
	if strings.Contains(bashMsg, "error token is") {
		// Expanded-subscript overrides ($expr/expr indirection over a
		// malformed assoc key): bash reports the expanded token text
		// directly, without the `((: ... :` command wrapper. Verified
		// patch handed across the scope wall in QUOTEARRAY-BLOCKERS.md.
		// The override is the echoed expansion of a subscript whose value
		// was a malformed array reference: either it still carries the
		// `$(…)`/`…` it was expanded from, or — when that value held no
		// `$`/backtick — its `[`/`]` were backslash-escaped by
		// arithmQuoteMalformedIndexText (e.g. `0\],b\[1` from `$index`).
		// A literal-source chain like `1[2] = 3` keeps the `((: … :`
		// wrapper, so distinguish on those signatures rather than the
		// bare presence of brackets.
		if command && exprTextOverride != "" &&
			strings.Contains(bashMsg, "arithmetic syntax error: invalid arithmetic operator") &&
			(strings.ContainsAny(exprTextOverride, "$`") ||
				strings.Contains(exprTextOverride, `\[`) ||
				strings.Contains(exprTextOverride, `\]`)) {
			return fmt.Errorf("%s: line %d: %s: %s",
				prefix, line, exprText, bashMsg)
		}
		// An operand-expected error whose token is a re-parsed
		// $(…)/${…}/`…` expansion is reported by bash directly as
		// "<expanded operand>: <msg>" with no ((: …) command wrapper,
		// even inside a (( )) command or builtin subscript.
		if exprTextOverride != "" &&
			strings.Contains(bashMsg, "arithmetic syntax error: operand expected") &&
			(strings.Contains(bashMsg, "error token is \"$") ||
				strings.Contains(bashMsg, "error token is \"`")) {
			// Bash includes any source whitespace that trailed the
			// operand (e.g. `[ $index ]` / `(( $index ))`) in both the
			// reported expression and the error token.
			if src := r.arithmSourceText(expr, true); strings.HasSuffix(src, " ") || strings.HasSuffix(src, "\t") {
				exprText += " "
				if q := strings.LastIndexByte(bashMsg, '"'); q >= 0 {
					bashMsg = bashMsg[:q] + " " + bashMsg[q:]
				}
			}
			return fmt.Errorf("%s: line %d: %s: %s",
				prefix, line, exprText, bashMsg)
		}
		if command {
			if strings.HasSuffix(exprText, "\n") {
				return fmt.Errorf("%s: line %d: ((: %s: %s",
					prefix, line, exprText, bashMsg)
			}
			if compactErrSep {
				return fmt.Errorf("%s: line %d: ((: %s: %s",
					prefix, line, exprText, bashMsg)
			}
			return fmt.Errorf("%s: line %d: ((: %s : %s",
				prefix, line, exprText, bashMsg)
		}
		return fmt.Errorf("%s: line %d: %s: %s",
			prefix, line, exprText, bashMsg)
	}
	quotedToken := tokenText
	if !exactToken && !strings.HasSuffix(tokenText, " ") {
		quotedToken += " "
	}
	if exactToken {
		quotedToken = tokenText
	}
	if command {
		return fmt.Errorf("%s: line %d: ((: %s%s%s (error token is \"%s\")",
			prefix, line, exprText, commandSep, bashMsg, quotedToken)
	}
	return fmt.Errorf("%s: line %d: %s: %s (error token is \"%s\")",
		prefix, line, exprText, bashMsg, quotedToken)
}

func arithmInvalidLiteralMsg(msg string) bool {
	return strings.Contains(msg, "invalid arithmetic base") ||
		strings.Contains(msg, "invalid integer constant") ||
		strings.Contains(msg, "value too great for base") ||
		strings.Contains(msg, "invalid number")
}

func arithWordEmpty(word *syntax.Word) bool {
	if word == nil || len(word.Parts) != 1 {
		return false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	return ok && lit.Value == "" && lit.Pos() == lit.End()
}

func (r *Runner) arithmSourceText(expr syntax.ArithmExpr, extendTrailingSpace bool) string {
	if expr == nil {
		return ""
	}
	return r.sourceTextRange(expr.Pos(), expr.End(), extendTrailingSpace)
}

func (r *Runner) sourceTextRange(start, end syntax.Pos, extendTrailingSpace bool) string {
	if len(r.bashSource) == 0 || !start.IsValid() || !end.IsValid() {
		return ""
	}
	i, ok := r.sourceOffset(start)
	if !ok {
		return ""
	}
	j, ok := r.sourceOffset(end)
	if !ok {
		return ""
	}
	if i < 0 || j < i || j > len(r.bashSource) {
		return ""
	}
	if extendTrailingSpace {
		for j < len(r.bashSource) && (r.bashSource[j] == ' ' || r.bashSource[j] == '\t') {
			j++
		}
	}
	return string(r.bashSource[i:j])
}

func (r *Runner) sourceOffset(pos syntax.Pos) (int, bool) {
	line, col := int(pos.Line()), int(pos.Col())
	if line <= 0 || col <= 0 {
		return 0, false
	}
	curLine := 1
	i := 0
	for ; i < len(r.bashSource) && curLine < line; i++ {
		if r.bashSource[i] == '\n' {
			curLine++
		}
	}
	if curLine != line {
		return 0, false
	}
	i += col - 1
	if i < 0 || i > len(r.bashSource) {
		return 0, false
	}
	return i, true
}

// stmtArithmExpTrail returns the whitespace just before the closing `))` of
// the first $((...)) in the current statement's source line, or "".
func (r *Runner) stmtArithmExpTrail() string {
	start, ok := r.sourceOffset(r.curStmtPos)
	if !ok || start < 0 || start > len(r.bashSource) {
		return ""
	}
	end := r.sourceLineEndOffset(r.curStmtPos.Line())
	if end < start || end > len(r.bashSource) {
		return ""
	}
	line := string(r.bashSource[start:end])
	open := strings.Index(line, "$((")
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open + 1; i < len(line); i++ {
		switch line[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// line[i-1] is the first `)` of the closing `))`.
				inner := line[:i-1]
				return inner[len(strings.TrimRight(inner, " \t")):]
			}
		}
	}
	return ""
}

func (r *Runner) sourceLineEndOffset(line uint) int {
	if len(r.bashSource) == 0 || line == 0 {
		return r.stdinSourceOffset
	}
	curLine := uint(1)
	for i, b := range r.bashSource {
		if curLine == line && b == '\n' {
			return i + 1
		}
		if b == '\n' {
			curLine++
		}
	}
	return len(r.bashSource)
}

func (r *Runner) stdinSourceStartOffset() int {
	start := r.stdinSourceBaseOffset
	if start == 0 && r.curStmtEnd.IsValid() {
		start = r.sourceLineEndOffset(r.curStmtEnd.Line())
	}
	if start < r.stdinSourceOffset {
		start = r.stdinSourceOffset
	}
	if start > len(r.bashSource) {
		start = len(r.bashSource)
	}
	return start
}

// scriptStdinReader returns a reader over the unconsumed tail of the script
// source for a command reading fd 0 (bash's stdin-script quirk), or nil when
// the feature is inactive. It exposes everything remaining; a command reading
// to EOF (`cat`) drains it all while one reading nothing consumes none — the
// exec handler backs it with a seekable fd and advances the consumed offset by
// the child's actual read position.
func (r *Runner) scriptStdinReader() io.Reader {
	if !r.stdinSourceActive || r.stdinRedirected || len(r.bashSource) == 0 {
		return nil
	}
	off := r.stdinSourceStartOffset()
	return &scriptStdinReader{r: r, off: off, limit: len(r.bashSource)}
}

type scriptStdinReader struct {
	r     *Runner
	off   int
	limit int
}

func (s *scriptStdinReader) Read(p []byte) (int, error) {
	if s.off >= s.limit {
		s.r.stdinSourceOffset = max(s.r.stdinSourceOffset, s.off)
		return 0, io.EOF
	}
	src := s.r.bashSource[s.off:s.limit]
	n := copy(p, src)
	s.off += n
	s.r.stdinSourceOffset = max(s.r.stdinSourceOffset, s.off)
	return n, nil
}

// seekableFile materializes the script-stdin slice as a seekable temp file so an
// EXTERNAL command can be given a real fd. os/exec eagerly drains a non-File
// stdin reader, which would consume the slice even for a command that never
// reads stdin (e.g. `echo`) — silently swallowing the next script line. With a
// real fd the child reads only what it wants; the caller then advances
// stdinSourceOffset by the child's actual read position. Returns the file plus
// the absolute source offset its start corresponds to.
func (s *scriptStdinReader) seekableFile() (*os.File, int) {
	f, err := os.CreateTemp("", "bashy-stdin-src-")
	if err != nil {
		return nil, 0
	}
	if _, err := f.Write(s.r.bashSource[s.off:s.limit]); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, 0
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, 0
	}
	return f, s.off
}

func (r *Runner) fields(words ...*syntax.Word) []string {
	strs, err := expand.Fields(r.ecfg, words...)
	r.expandErr(err)
	return strs
}

func commandDeclarationPrefix(words []*syntax.Word) int {
	i := 0
	for i < len(words) && words[i].Lit() == "command" {
		i++
	}
	if i == 0 || i == len(words) {
		return -1
	}
	switch words[i].Lit() {
	case "export", "readonly":
		return i
	}
	return -1
}

func assignmentWord(word *syntax.Word) bool {
	if len(word.Parts) == 0 {
		return false
	}
	lit, ok := word.Parts[0].(*syntax.Lit)
	if !ok {
		return false
	}
	name, _, found := strings.Cut(lit.Value, "=")
	return found && syntax.ValidName(strings.TrimSuffix(name, "+"))
}

func (r *Runner) literal(word *syntax.Word) string {
	str, err := expand.Literal(r.ecfg, word)
	r.expandErr(err)
	return str
}

func (r *Runner) literalWithQuoteRemoval(word *syntax.Word) string {
	str, err := expand.LiteralWithQuoteRemoval(r.ecfg, word)
	r.expandErr(err)
	return str
}

// unsetQuotedSubscriptSource recovers the raw source of an `unset` operand
// that is an *associative* array reference `name[subscript]` whose subscript
// carries a quote (`a['$key']`, `a["$key"]`, `a["foo"]`). Such a quote makes
// bash treat the whole token as a valid array reference: it suppresses word
// splitting and pins the key to a single expansion (a single-quoted subscript
// stays literal). Expanding the word as a field instead would strip the quotes
// and leave a bare `$key` that the subscript handler then re-expands, so the
// raw source must reach unset intact.
//
// This applies only to associative arrays: an *indexed* array subscript is
// arithmetic, so `unset a["$subscript"]` must feed the (single) expansion of
// the subscript to the arithmetic evaluator — which reports "operand expected"
// without re-running an embedded `$(…)` — rather than be recovered as a literal
// key. Operands with an unquoted subscript still go through field expansion
// (word splitting), and a whole-token quote (`'a[$key]'`) is left to the
// quoted-operand path.
func (r *Runner) unsetQuotedSubscriptSource(word *syntax.Word) (string, bool) {
	src := r.sourceTextRange(word.Pos(), word.End(), false)
	if src == "" {
		return "", false
	}
	name, idx, ok := splitArrayRef(src)
	if !ok || !syntax.ValidName(name) {
		return "", false
	}
	if !strings.ContainsAny(idx, `'"`) {
		return "", false
	}
	if r.lookupVar(name).Kind != expand.Associative {
		return "", false
	}
	return src, true
}

func unsetArrayOperandLiteral(word *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		b.WriteString(lit.Value)
	}
	s := unsetSubscriptUnescape(b.String())
	name, _, ok := splitArrayRef(s)
	return s, ok && syntax.ValidName(name)
}

// unsetSubscriptUnescape applies backslash quote-removal inside the
// `[...]` subscript of a literal unset operand, leaving the bracket
// structure itself intact. `unset A[\]]` addresses the literal key `]`
// (the backslash quoted the bracket at assignment time), so it must here
// too; the brackets are kept literal so the subscript is never glob- or
// arithmetic-expanded.
func unsetSubscriptUnescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if depth > 0 && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			b.WriteByte(c)
		case '[':
			depth++
			b.WriteByte(c)
		case ']':
			if depth > 0 {
				depth--
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (r *Runner) literalForAssign(word *syntax.Word) string {
	str, err := expand.LiteralForAssign(r.ecfg, word)
	r.expandErr(err)
	return str
}

func (r *Runner) arrayAssignEnvValue(as *syntax.Assign) string {
	var b strings.Builder
	b.WriteByte('(')
	for i, elem := range as.Array.Elems {
		if i > 0 {
			b.WriteByte(' ')
		}
		if elem.Index != nil {
			b.WriteByte('[')
			if w, ok := elem.Index.(*syntax.Word); ok {
				b.WriteString(r.literal(w))
			} else {
				b.WriteString(r.arithmSourceText(elem.Index, false))
			}
			b.WriteByte(']')
			if elem.Append {
				b.WriteByte('+')
			}
			b.WriteByte('=')
		}
		b.WriteString(r.literalForAssign(elem.Value))
	}
	b.WriteByte(')')
	return b.String()
}

func (r *Runner) integerArrayAssignWithArithSuffix(cm *syntax.CallExpr, args []*syntax.Word) bool {
	if len(cm.Assigns) != 1 || len(args) != 1 {
		return false
	}
	as := cm.Assigns[0]
	if as.Array == nil || as.Index != nil || as.Name == nil || len(as.Array.Elems) != 1 {
		return false
	}
	elem := as.Array.Elems[0]
	if elem.Index != nil || elem.Append || elem.Value == nil {
		return false
	}
	prev := r.lookupVar(as.Name.Value)
	if !prev.Integer {
		return false
	}
	suffix := r.literalForAssign(args[0])
	if suffix == "" || !strings.ContainsAny(suffix[:1], "+-*/%") {
		return false
	}
	exprText := "(" + r.literalForAssign(elem.Value) + ")" + suffix
	expr, err := syntax.NewParser().Arithmetic(strings.NewReader(exprText))
	if err != nil || expr == nil {
		return false
	}
	n, err := expand.Arithm(r.ecfg, expr)
	if err != nil {
		r.expandErr(err)
		r.exit.code = 1
		return true
	}
	prev.Set = true
	prev.Kind = expand.String
	prev.Str = strconv.Itoa(n)
	r.setVar(as.Name.Value, prev)
	return true
}

func (r *Runner) document(word *syntax.Word) string {
	str, err := expand.Document(r.ecfg, word)
	r.expandErr(err)
	return str
}

func bashDiagnosticWord(s string) string {
	needsQuote := !utf8.ValidString(s)
	if !needsQuote {
		for _, r := range s {
			if !unicode.IsPrint(r) {
				needsQuote = true
				break
			}
		}
	}
	if !needsQuote {
		return s
	}
	q, err := syntax.Quote(s, syntax.LangBash)
	if err != nil {
		return s
	}
	return q
}

func (r *Runner) pattern(word *syntax.Word) string {
	str, err := expand.Pattern(r.ecfg, word)
	r.expandErr(err)
	return str
}

func (r *Runner) restoreInlineVar(name string, vr expand.Variable, wasGoCommandTouched bool) {
	if o, ok := r.writeEnv.(*overlayEnviron); ok {
		defer o.restoreGoCommandTouch(name, wasGoCommandTouched)
		if o.restoreTemp(name, vr) {
			return
		}
		if o.funcScope && o.holdsLocally(name) {
			if wenv, ok := o.parent.(expand.WriteEnviron); ok {
				saved := r.writeEnv
				r.writeEnv = wenv
				r.setVar(name, vr)
				r.writeEnv = saved
				return
			}
		}
	}
	if !vr.Declared() && r.lookupVar(name).ReadOnly {
		return
	}
	r.setVar(name, vr)
}

type inlineRestoreFrame struct {
	restores map[string]bool
	leaked   map[string]bool
}

// markEnclosingInlineLeak marks the nearest active outer command-prefix
// restore for name. The current frame belongs to the special builtin which
// caused the leak, so it is intentionally excluded from the search.
func (r *Runner) markEnclosingInlineLeak(name string) {
	for i := len(r.inlineRestoreFrames) - 2; i >= 0; i-- {
		frame := r.inlineRestoreFrames[i]
		if !frame.restores[name] {
			continue
		}
		if frame.leaked == nil {
			frame.leaked = make(map[string]bool)
		}
		frame.leaked[name] = true
		return
	}
}

// expandEnviron exposes [Runner]'s variables to the expand package.
type expandEnv struct {
	r *Runner
}

var _ expand.WriteEnviron = expandEnv{}

func (e expandEnv) Get(name string) expand.Variable {
	return e.r.lookupVar(name)
}

// ResolveNameRef follows one nameref hop during expansion. A
// self-referencing local nameref (`typeset -n v=v` inside a function)
// would otherwise loop forever; bash warns "circular name reference" and
// resolves the name in the enclosing scope without following namerefs.
func (e expandEnv) ResolveNameRef(name string) expand.Variable {
	r := e.r
	vr := r.lookupVar(name)
	if vr.Kind == expand.NameRef && vr.Str == name {
		r.errf("%swarning: %s: circular name reference\n",
			r.bashErrPrefix(r.curStmtPos), name)
		if o, ok := r.writeEnv.(*overlayEnviron); ok && o.parent != nil {
			return o.parent.Get(name)
		}
		return expand.Variable{}
	}
	return vr
}

func (e expandEnv) Set(name string, vr expand.Variable) error {
	if e.r.lookupVar(name).ReadOnly {
		return fmt.Errorf("%s: readonly variable", name)
	}
	e.r.setVar(name, vr)
	return nil // TODO: return any other errors
}

func (e expandEnv) Each(fn func(name string, vr expand.Variable) bool) {
	e.r.bashPPEnv().Each(fn)
}

var todoPos syntax.Pos // for handlerCtx callers where we don't yet have a position

func (r *Runner) handlerCtx(ctx context.Context, kind handlerKind, pos syntax.Pos) context.Context {
	hc := HandlerContext{
		Agentic: r.bashPPAgentic,
		runner:  r,
		kind:    kind,
		// Layered over the typed bindings rather than over writeEnv alone, so
		// a handler reading HandlerCtx.Env sees the same value for a name
		// that the script's own `$name` does.
		Env:    &overlayEnviron{parent: r.bashPPEnv()},
		Dir:    r.Dir,
		Pos:    pos,
		Stdout: r.bashPPWriter(r.stdout),
		Stderr: r.bashPPWriter(r.stderr),
	}
	if r.stdin != nil { // do not leave hc.Stdin as a typed nil
		hc.Stdin = r.stdin
	} else if r.stdinClosed {
		// fd 0 was explicitly closed (`<&-`): hand the command a closed
		// descriptor so its read fails with EBADF rather than reading an
		// empty /dev/null.
		hc.Stdin = badFdReader{}
	}
	// The script-stdin reader OVERRIDES r.stdin: when the script came from
	// fd 0 (see [WithStdinScript]), r.stdin is that same descriptor, already
	// drained by the parser — the unread tail lives in bashSource. It is gated
	// on stdinScript, so an embedder's explicit stdin stream is never replaced.
	if stdin := r.scriptStdinReader(); stdin != nil {
		// An external command reading fd 0 consumes the rest of the script
		// source (bash's stdin-script quirk). It is backed by a seekable
		// temp file in the exec handler, so a command that reads to EOF
		// (`cat`) drains every remaining line while one that reads nothing
		// (`echo`) consumes none — the consumed offset is advanced by the
		// child's actual read position, not capped to a single line.
		hc.Stdin = stdin
	}
	return context.WithValue(ctx, handlerCtxKey{}, hc)
}

func (r *Runner) out(s string) {
	if _, err := io.WriteString(r.bashPPWriter(r.stdout), s); err != nil {
		r.outErr = err
	}
}

func (r *Runner) outf(format string, a ...any) {
	if _, err := fmt.Fprintf(r.bashPPWriter(r.stdout), format, a...); err != nil {
		r.outErr = err
	}
}

func (r *Runner) errf(format string, a ...any) {
	fmt.Fprintf(r.bashPPWriter(r.stderr), format, a...)
}

func (r *Runner) reportError(kind string, pos syntax.Pos, command, message string, code uint8) {
	if r.structuredErrorHandler == nil {
		return
	}
	message = strings.TrimSuffix(message, "\n")
	ev := ErrorEvent{
		Kind:       kind,
		Severity:   "error",
		Message:    message,
		Pos:        pos,
		Filename:   r.filename,
		Command:    command,
		ExitStatus: code,
	}
	if n := len(r.callStack); n > 0 {
		ev.Function = r.callStack[n-1].funcName
	}
	r.bashPPObserve(func() { r.structuredErrorHandler(ev) })
}

// pureLiteral reports whether all parts of word are literal /
// quoted-literal tokens (no parameter / command / arithmetic /
// process substitution). Used by xtrace formatting to decide
// between "render the source" (`$@`, `$(foo)`) and "expand to a
// value and re-quote" (`$' '`, `\|`).
func pureLiteral(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	for _, p := range word.Parts {
		switch p.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			// always literal
		case *syntax.DblQuoted:
			dq := p.(*syntax.DblQuoted)
			for _, ip := range dq.Parts {
				switch ip.(type) {
				case *syntax.Lit:
				default:
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// traceArrayLiteral renders an array assignment for `set -x` in
// bash 5.3's format: each element re-quoted independently via
// `xtraceQuote`, preserving bash-style minimality (single quotes
// where possible; backslash-escapes for single metacharacters).
func traceArrayLiteral(t *tracer, name, op string, elems []*syntax.ArrayElem, r *Runner) {
	t.stringf("%s%s(", name, op)
	for i, el := range elems {
		if i > 0 {
			t.string(" ")
		}
		if el.Index != nil {
			var buf bytes.Buffer
			syntax.NewPrinter(syntax.SingleLine(true)).Print(&buf, &syntax.Word{Parts: []syntax.WordPart{
				&syntax.Lit{Value: "[" + r.literal(el.Index.(*syntax.Word)) + "]"},
			}})
			t.string(buf.String() + "=")
		}
		// bash xtrace re-quotes purely-literal elements but
		// keeps parameter expansions / command subs / arithmetic
		// in their original source form (`$@`, `$(foo)`, …).
		// Bash keeps ordinary source quotes in compound assignments.
		// ANSI-C quotes are normalized after escape processing.
		sourceQuoted := false
		for _, part := range el.Value.Parts {
			switch part := part.(type) {
			case *syntax.DblQuoted:
				sourceQuoted = true
			case *syntax.SglQuoted:
				sourceQuoted = !part.Dollar
			}
		}
		if pureLiteral(el.Value) && !sourceQuoted {
			val, _ := expand.LiteralWithQuoteRemoval(r.ecfg, el.Value)
			t.string(xtraceQuote(val))
		} else {
			var buf bytes.Buffer
			syntax.NewPrinter(syntax.SingleLine(true)).Print(&buf, el.Value)
			t.string(buf.String())
		}
	}
	t.string(")")
}

// xtraceQuote renders s the way bash 5.3 prints command/argument
// values in xtrace output: bare if no shell-meta chars are
// present, backslash-escape for a single metacharacter,
// single-quote (with literal tab/newline inside) for anything
// else with no embedded `'`, otherwise fall back to the default
// `syntax.Quote`.
func xtraceQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Single metacharacter → backslash-escape: `\|`, `\&`, `\;`,
	// `\(`, `\)`, `\<`, `\>`. Bash xtrace picks this form for
	// single-byte shell metas.
	if len(s) == 1 {
		switch s[0] {
		case '|', '&', ';', '(', ')', '<', '>':
			return `\` + s
		}
	}
	// Bare if no chars need quoting.
	needsQuote := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '"', '\'', '\\', '`',
			'$', '|', '&', ';', '(', ')', '<', '>',
			'*', '?', '[', '{', '!', '#', '~', '=':
			needsQuote = true
		}
		if !unicodeIsPrint(r) && r != '\t' && r != '\n' {
			needsQuote = true
		}
		if needsQuote {
			break
		}
	}
	if !needsQuote {
		return s
	}
	// Otherwise wrap in single quotes if the value has no embedded
	// `'` and no truly unprintable runes; bash uses literal
	// control chars inside single quotes (`'<TAB>'`, `'<NL>'`).
	if !strings.ContainsRune(s, '\'') {
		hasUnprintable := false
		for _, r := range s {
			if r != '\t' && r != '\n' && !unicodeIsPrint(r) {
				hasUnprintable = true
				break
			}
		}
		if !hasUnprintable {
			return "'" + s + "'"
		}
	}
	q, err := syntax.Quote(s, syntax.LangBash)
	if err != nil {
		return s
	}
	return q
}

func unicodeIsPrint(r rune) bool {
	return unicode.IsPrint(r)
}

// compactArithm strips bash-`set -x`-style padding from a printer-
// rendered arithmetic expression: drops spaces around `=`, `+=`,
// `-=`, etc., and around comparison/logical operators so the
// traced form reads `i=0`, `i<5`, `i++` rather than the shfmt
// `i = 0`, `i < 5`, `i ++` rendering. Conservative — leaves
// other whitespace alone.
func compactArithm(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' && i+1 < len(s) {
			// drop space before `=`, `+`, `-`, `*`, `/`, `%`,
			// `<`, `>`, `!`, `&`, `|`, `^`, `?`, `:`, `,` —
			// the operators bash's xtrace renders unspaced.
			n := s[i+1]
			switch n {
			case '=', '+', '-', '*', '/', '%', '<', '>', '!', '&', '|', '^', '?', ':', ',':
				continue
			}
		}
		if c == ' ' && len(out) > 0 {
			// drop space after operator chars (the matching
			// side of the rule above).
			switch out[len(out)-1] {
			case '=', '+', '-', '*', '/', '%', '<', '>', '!', '&', '|', '^', '?', ':', ',':
				continue
			}
		}
		out = append(out, c)
	}
	return string(out)
}

// applyCaseAttr folds the variable's value in place when its
// case-modification attributes (`declare -u/-l/-c`) are set.
// Operates on String, Indexed and Associative kinds; the
// scalar / per-element value gets folded.
func applyCaseAttr(vr *expand.Variable) {
	if !(vr.Upper || vr.Lower || vr.Capitalize) {
		return
	}
	fold := func(s string) string {
		switch {
		case vr.Upper:
			return strings.ToUpper(s)
		case vr.Lower:
			return strings.ToLower(s)
		case vr.Capitalize:
			if s == "" {
				return s
			}
			rs := []rune(s)
			rs[0] = unicode.ToUpper(rs[0])
			for i := 1; i < len(rs); i++ {
				rs[i] = unicode.ToLower(rs[i])
			}
			return string(rs)
		}
		return s
	}
	switch vr.Kind {
	case expand.String:
		vr.Str = fold(vr.Str)
	case expand.Indexed:
		for i, v := range vr.List {
			vr.List[i] = fold(v)
		}
		for i, v := range vr.ListMap {
			vr.ListMap[i] = fold(v)
		}
	case expand.Associative:
		for k, v := range vr.Map {
			vr.Map[k] = fold(v)
		}
	}
}

// validExportedFuncName reports whether name is acceptable as the
// payload of `export -f <name>`. Bash 5.3 refuses to export
// functions whose names contain `=` or `/` (or are otherwise
// incompatible with the BASH_FUNC_<name>%%= envvar round-trip).
func validExportedFuncName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '=', '/':
			return false
		}
	}
	return true
}

func declareFuncInvalidOption(valType string, modes []string) string {
	switch valType {
	case "-a", "-A", "-i", "-n":
		return valType
	}
	for _, mode := range modes {
		switch mode {
		case "-a", "-A", "-i", "-n":
			return mode
		}
	}
	return ""
}

// validBashFuncName reports whether s is a function name bash 5.3
// would accept. Bash is much more lenient than the identifier syntax
// (e.g. `+`, `@`, `foo-bar`, `2nd` are all valid function names), but
// it still rejects names containing shell metacharacters or whitespace
// that would require quoting in a normal command. The intent is to
// catch the names that bash itself diagnoses as "not a valid
// identifier" at runtime (parameter expansions, process subs, quoted
// strings, etc.) while still admitting bash's broad nominal alphabet.
func validBashFuncName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r',
			'$', '\'', '"', '`', '\\',
			'(', ')', '{', '}', '[', ']',
			'<', '>', '|', '&', ';', '#':
			return false
		}
	}
	return true
}

// printFuncDecl prints a function definition in bash 5.3's
// `declare -f` shape: `name () \n{ \n    stmt;\n    stmt2\n}` —
// 4-space indent at every nesting level, trailing `;` on every
// simple statement (and inner statements of compound commands),
// no `;` on the last top-level statement or on compound block
// closers (`fi`, `done`, `esac`, `}`).
func (r *Runner) printFuncDecl(name string, body *syntax.Stmt) {
	// bash 5.3 prefixes the `function` keyword only when the
	// `NAME ()` form would fail to reparse as a function decl —
	// i.e. when the name contains `=` (otherwise an assignment),
	// `(`, `)`, or other characters that break the standard
	// declaration syntax. Pure-digit / dash names render as plain
	// `NAME ()` without `function`.
	if entry := r.bashPPDecoratedShellFunc(name); entry != nil {
		r.bashPPPrintDecorators(entry.decorators)
	}
	if r.bashPPAgenticFunc(name) {
		r.outf("agentic function %s () \n", name)
	} else if funcDeclNeedsKeyword(name) {
		r.outf("function %s () \n", name)
	} else {
		r.outf("%s () \n", name)
	}
	// Body is a syntax.Stmt whose Cmd is a syntax.Block (the `{ }`)
	// in the usual case. Unwrap the block so we can render each
	// inner stmt with the bash-specific trailing-semicolon rule.
	block, ok := body.Cmd.(*syntax.Block)
	if !ok {
		// Subshell bodies (`f() ( ... )`) are wrapped in `{ }` by
		// bash 5.3 declare -f and rendered inline as one stmt.
		// Body redirections are attached to the subshell line.
		if sub, ok := body.Cmd.(*syntax.Subshell); ok {
			r.out("{ \n")
			var buf bytes.Buffer
			printer := syntax.NewPrinter(syntax.SingleLine(true), syntax.SpaceRedirects(true))
			printer.Print(&buf, &syntax.Stmt{Cmd: sub})
			inner := strings.TrimRight(buf.String(), "\n")
			inner = bashSubshellSpace(inner)
			for _, rd := range body.Redirs {
				text := formatRedirect(rd)
				if strings.HasPrefix(text, ">&") {
					text = "1" + text
				} else if strings.HasPrefix(text, "<&") {
					text = "0" + text
				}
				inner += " " + text
			}
			r.outf("    %s\n", inner)
			r.out("}\n")
			return
		}
		// Other Non-Block bodies (rare). Fall back to the printer.
		var buf bytes.Buffer
		syntax.NewPrinter(syntax.Indent(4), syntax.SpaceRedirects(true), syntax.BashCompatArith(true)).Print(&buf, body)
		r.out(buf.String())
		r.out("\n")
		return
	}
	r.out("{ \n")
	printer := syntax.NewPrinter(syntax.Indent(4), syntax.SpaceRedirects(true), syntax.BashCompatArith(true), syntax.JoinEscapedNewlines(true))
	// bash 5.3 declare -f groups a `cmd &` with the following
	// simple stmt onto one line. Skip ahead when we emit a
	// background stmt and merge the buffer of the next.
	skipNext := false
	for i, st := range block.Stmts {
		if skipNext {
			skipNext = false
			continue
		}
		var buf bytes.Buffer
		// Nested function declaration: render with bash 5.3's
		// `function NAME () { ... }` form rather than the
		// printer's `NAME() { ... }`. Pass redirs from BOTH the
		// surrounding stmt (Stmt.Redirs) and the body block
		// (FuncDecl.Body.Redirs) — bash attaches the body-level
		// ones to the closing brace.
		if fd, ok := st.Cmd.(*syntax.FuncDecl); ok {
			redirs := append([]*syntax.Redirect(nil), st.Redirs...)
			redirs = append(redirs, fd.Body.Redirs...)
			renderNestedFuncDecl(&buf, printer, fd, redirs)
		} else {
			printer.Print(&buf, st)
		}
		body := strings.TrimRight(buf.String(), "\n")
		if tc, ok := st.Cmd.(*syntax.TimeClause); ok && tc.Stmt == nil {
			body += " "
		}
		// bash 5.3 groups `cmd & nextStmt` onto one line when
		// the next stmt is a simple/subshell stmt (not a
		// compound). The merged line gets a trailing `;` only
		// when it's not the last stmt of the function body.
		if st.Background && i+1 < len(block.Stmts) {
			nxt := block.Stmts[i+1]
			if isSimpleForAmpJoin(nxt) {
				var nbuf bytes.Buffer
				printer.Print(&nbuf, nxt)
				nbody := strings.TrimRight(nbuf.String(), "\n")
				// Pre-pad the subshell in nbody so the merged
				// line gets `( EXPR )` instead of `(EXPR)`.
				nbody = bashSubshellSpace(nbody)
				body = body + " " + nbody
				skipNext = true
			}
		}
		isLast := (i == len(block.Stmts)-1) ||
			(skipNext && i+1 == len(block.Stmts)-1)
		if skipNext && !isLast && !strings.HasSuffix(body, ";") {
			body += ";"
		}
		rendered := bashDeclareFmt(body, isLast)
		rendered = restoreFuncArithIndent(rendered, r.sourceTextRange(st.Pos(), st.End(), false))
		r.out(rendered)
		r.out("\n")
		// bash 5.3 inserts a blank line between two top-level
		// stmts in a function body when the prior stmt ended
		// with a heredoc terminator. The blank also appears
		// before the closing `}` of the function when the last
		// stmt ends with a terminator.
		if endsWithHeredocTerminator(body) {
			r.out("\n")
		}
	}
	// bash 5.3 attaches function-level redirections (`f() { ... } >file`)
	// to the closing brace. Render via the printer for each Redir and
	// rewrite implicit fd1/fd0 to explicit `1>&N` / `0<&N` to match
	// bash's normalisation.
	if len(body.Redirs) > 0 {
		r.out("}")
		for _, rd := range body.Redirs {
			text := formatRedirect(rd)
			// bash 5.3 normalises only the `&N`-duplicate form
			// (file redirects `> file` / `< file` stay as-is).
			if strings.HasPrefix(text, ">&") {
				text = "1" + text
			} else if strings.HasPrefix(text, "<&") {
				text = "0" + text
			}
			r.outf(" %s", text)
		}
		r.out("\n")
	} else {
		r.out("}\n")
	}
}

// Bash's function printer preserves the source indentation inside a multiline
// arithmetic expansion, even while reindenting the surrounding function.
func restoreFuncArithIndent(rendered, source string) string {
	if !strings.Contains(source, "$((\n") {
		return rendered
	}
	lines := strings.Split(rendered, "\n")
	original := strings.Split(source, "\n")
	startSource, startRendered := -1, -1
	for i, line := range original {
		if strings.HasSuffix(strings.TrimSpace(line), "$((") {
			startSource = i
		}
	}
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimSpace(line), "$((") {
			startRendered = i
		}
	}
	if startSource < 0 || startRendered < 0 {
		return rendered
	}
	for i, j := startSource+1, startRendered+1; i < len(original) && j < len(lines); i, j = i+1, j+1 {
		indent := original[i][:len(original[i])-len(strings.TrimLeft(original[i], " \t"))]
		lines[j] = indent + strings.TrimLeft(lines[j], " \t")
		if strings.TrimSpace(original[i]) == "))" {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// formatRedirect renders a single redirect node as text. Walks the
// node's fields directly instead of going through the printer (the
// printer needs a wrapping stmt that can panic on empty CallExpr).
func formatRedirect(r *syntax.Redirect) string {
	var b strings.Builder
	if r.N != nil {
		b.WriteString(r.N.Value)
	}
	b.WriteString(r.Op.String())
	if r.Hdoc != nil {
		// Heredoc — rare in function redir; just render the word.
	}
	if r.Word != nil {
		var wb bytes.Buffer
		syntax.NewPrinter().Print(&wb, &syntax.Stmt{Cmd: &syntax.CallExpr{Args: []*syntax.Word{r.Word}}})
		s := strings.TrimSpace(wb.String())
		b.WriteString(s)
	}
	return b.String()
}

// endsWithHeredocTerminator returns true if the printer-output body
// for a single stmt ends with a heredoc terminator line (i.e. last
// stmt was something like `cat <<EOF\\n...\\nEOF`). Used by
// printFuncDecl to insert a blank line between such a stmt and the
// next stmt, matching bash 5.3 declare -f formatting.
func endsWithHeredocTerminator(body string) bool {
	lines := strings.Split(body, "\n")
	// Walk forward, track heredoc terminator tag, and check if
	// the LAST non-empty line is a terminator.
	inH := ""
	lastIsTerm := false
	for _, raw := range lines {
		trim := strings.TrimSpace(raw)
		if trim == "" {
			continue
		}
		if inH != "" {
			if trim == inH {
				inH = ""
				lastIsTerm = true
			}
			continue
		}
		lastIsTerm = false
		if idx := findHeredocOp(trim); idx >= 0 {
			afterOp := trim[idx+2:]
			rest := strings.TrimLeft(afterOp, "-")
			rest = strings.TrimLeft(rest, " \t")
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			tag := strings.Trim(rest[:end], "'\"")
			if tag != "" {
				inH = tag
			}
		}
	}
	return lastIsTerm
}

// fieldsAllAssignments reports whether every element of fields is
// shaped like `name=value` with name being a valid identifier. Used
// by the cmd dispatch to promote a leftover expansion to plain
// assignments when the command word expanded to nothing.
func fieldsAllAssignments(fields []string) bool {
	for _, f := range fields {
		eq := strings.IndexByte(f, '=')
		if eq <= 0 {
			return false
		}
		if !syntax.ValidName(f[:eq]) {
			return false
		}
	}
	return true
}

func fieldsPromotableAssignments(fields []string, args []*syntax.Word) bool {
	if !fieldsAllAssignments(fields) {
		return false
	}
	if len(fields) == len(args) {
		for _, arg := range args {
			if !wordLooksLikeAssign(arg) {
				return false
			}
		}
	}
	return true
}

func splitAssignmentField(field string) (name, value string, ok bool) {
	eq := strings.IndexByte(field, '=')
	if eq <= 0 || !syntax.ValidName(field[:eq]) {
		return "", "", false
	}
	return field[:eq], field[eq+1:], true
}

func declareOperandNames(fields []string) map[string]bool {
	names := make(map[string]bool)
	optionsEnded := false
	for _, field := range fields {
		if !optionsEnded && field == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && (strings.HasPrefix(field, "-") || strings.HasPrefix(field, "+")) {
			continue
		}
		name := field
		if n, _, ok := splitAssignmentField(field); ok {
			name = strings.TrimSuffix(n, "+")
		}
		if base, _, ok := splitArrayRef(name); ok {
			name = base
		}
		if syntax.ValidName(name) {
			names[name] = true
		}
	}
	return names
}

func (r *Runner) inlineArrayAssignName(as *syntax.Assign) string {
	name := as.Name.Value
	if as.Index != nil {
		if w, ok := as.Index.(*syntax.Word); ok {
			return name + "[" + r.literal(w) + "]"
		}
		return name + "[]"
	}
	return name
}

// renderNestedFuncDecl writes a nested function decl into buf in
// bash 5.3's declare -f shape:
//
//	function NAME ()
//	{
//	    body
//	} <redirs>
//
// funcDeclNeedsKeyword reports whether the bash 5.3 declare -f
// renderer must prefix the `function` keyword for a function with
// this name. The plain `NAME ()` form would fail to reparse when
// the name contains characters that break the declaration syntax —
// `=` (parsed as assignment), `(`, `)`, whitespace, etc. Names that
// are pure-identifier or pure-digit (`11111`) don't need it.
func funcDeclNeedsKeyword(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch c {
		case '=', '(', ')', '<', '>', '"', '\'', '`', '$', '\\',
			' ', '\t', '\n', ';', '&', '|', '*', '?', '[', ']', '{', '}':
			return true
		}
	}
	return false
}

func renderNestedFuncDecl(buf *bytes.Buffer, printer *syntax.Printer, fd *syntax.FuncDecl, redirs []*syntax.Redirect) {
	buf.WriteString("function ")
	buf.WriteString(fd.Name.Value)
	buf.WriteString(" () \n{\n")
	// Print the function body (which is a Stmt — usually a Block).
	var bodyBuf bytes.Buffer
	if block, ok := fd.Body.Cmd.(*syntax.Block); ok {
		for _, st := range block.Stmts {
			var sb bytes.Buffer
			printer.Print(&sb, st)
			line := strings.TrimRight(sb.String(), "\n")
			// Each body stmt indented by 4 spaces in declare -f.
			bodyBuf.WriteString("    ")
			bodyBuf.WriteString(line)
			bodyBuf.WriteString("\n")
		}
	} else {
		// Non-Block body (rare) — pass through printer.
		printer.Print(&bodyBuf, fd.Body)
	}
	buf.WriteString(bodyBuf.String())
	buf.WriteString("}")
	// Function-level redirections from the surrounding Stmt.
	for _, rd := range redirs {
		text := formatRedirect(rd)
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, ">&") {
			text = "1" + text
		} else if strings.HasPrefix(text, "<&") {
			text = "0" + text
		}
		buf.WriteString(" ")
		buf.WriteString(text)
	}
}

// isSimpleForAmpJoin reports whether stmt can be joined onto the
// preceding `&` stmt's line in declare -f rendering — true for plain
// CallExpr or subshell, false for compound openers / declarations.
func isSimpleForAmpJoin(s *syntax.Stmt) bool {
	switch s.Cmd.(type) {
	case *syntax.CallExpr, *syntax.Subshell:
		return true
	}
	return false
}

// bashSplitCompound expands single-line `for/while/until/if/case`
// compound commands to multi-line layout matching bash 5.3's
// `declare -f` rendering. The mvdan/sh printer compresses them onto
// one line; bash always splits across lines, indenting the body.
//
// Pattern (single-line for/while/until):
//
//	while EXPR; do BODY; done    →    while EXPR; do
//	                                      BODY
//	                                  done
//
// Pattern (if):
//
//	if X; then A; elif Y; then B; else C; fi
//	  → if X; then
//	         A
//	     elif Y; then
//	         B
//	     else
//	         C
//	     fi
//
// Pattern (case):
//
//	case X in PAT) BODY ;; PAT2) BODY2 ;; esac  → multi-line.
//
// We operate on already-arith-padded text. The splitting walks each
// line looking for openers (`do `, `then `, etc. as substrings) and
// closers (`done`, `fi`, `esac`); the body between them gets one
// statement per line at +4 indent relative to the opener.
func bashSplitCompound(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines)*2)
	for _, line := range lines {
		out = append(out, splitCompoundLine(line)...)
	}
	return strings.Join(out, "\n")
}

// splitCompoundLine splits a single printer-output line on compound
// boundaries (`do `, `then `, `else `, `elif `, `fi`, `done`, `;;`),
// returning the new multi-line layout as a slice of lines.
func splitCompoundLine(line string) []string {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	trim := line[indent:]
	// Detect a compound-keyword opener (for/while/until/if/case at
	// the start of the trimmed line). Only split if the line ALSO
	// contains the matching closer (otherwise it's already
	// multi-line from the printer).
	switch {
	case strings.HasPrefix(trim, "for ") ||
		strings.HasPrefix(trim, "while ") ||
		strings.HasPrefix(trim, "until "):
		// A for-in header that ends with `; do` (no body on this
		// line — body is already on subsequent lines from the
		// printer) needs `do` split to its own line. bash 5.3
		// renders for-in as `for X in Y;\n    do`. while/until
		// keep `do` glued so leave them alone.
		if strings.HasSuffix(trim, "; do") {
			ind := strings.Repeat(" ", indent)
			isFor := strings.HasPrefix(trim, "for ")
			isArithFor := strings.HasPrefix(trim, "for ((")
			if isFor {
				header := strings.TrimSuffix(trim, "; do")
				if isArithFor {
					return []string{ind + header, ind + "do"}
				}
				return []string{ind + header + ";", ind + "do"}
			}
			return []string{line}
		}
		if !strings.Contains(trim, "; do ") {
			return []string{line}
		}
		// Match `; done` either at end of line or followed by
		// trailing redirections (`done > /dev/null`); capture
		// trailing so it lands on the closing `done` line.
		doneIdx := -1
		for i := 0; i+len("; done") <= len(trim); i++ {
			if trim[i:i+len("; done")] == "; done" &&
				(i+len("; done") == len(trim) ||
					trim[i+len("; done")] == ' ' ||
					trim[i+len("; done")] == ';') {
				doneIdx = i
			}
		}
		if doneIdx < 0 {
			return []string{line}
		}
		trailing := strings.TrimSpace(trim[doneIdx+len("; done"):])
		trim = trim[:doneIdx+len("; done")]
		// Split into header + (do on its own line for `for`, same
		// line for `while`/`until` — that's bash 5.3's
		// declare -f convention) + body + done. We always emit
		// `do` on a NEW line for `for` (whether `for X in Y` or
		// arith-for `for ((;;))`); for while/until we keep `do`
		// on the same line as the condition.
		doIdx := strings.Index(trim, "; do ")
		var opener string
		isFor := strings.HasPrefix(trim, "for ")
		// bash 5.3 arith-for `for ((...))` drops the trailing `;`
		// before the `do` line; the other forms keep it.
		isArithFor := strings.HasPrefix(trim, "for ((")
		if isFor {
			opener = trim[:doIdx]
			if isArithFor {
				opener = strings.TrimSuffix(opener, ";")
				opener = strings.TrimRight(opener, " ")
			}
		} else {
			opener = trim[:doIdx] + "; do"
		}
		body := trim[doIdx+len("; do ") : len(trim)-len("; done")]
		bodyLines := splitTopLevel(body, ";")
		ind := strings.Repeat(" ", indent)
		inner := strings.Repeat(" ", indent+4)
		out := []string{ind + opener}
		if isFor {
			out = append(out, ind+"do")
		}
		for _, b := range bodyLines {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			for _, sub := range splitCompoundLine(inner + b) {
				out = append(out, sub)
			}
		}
		closer := ind + "done"
		if trailing != "" {
			closer += " " + trailing
		}
		out = append(out, closer)
		return out
	case strings.HasPrefix(trim, "if "):
		if !strings.Contains(trim, "; then ") {
			return []string{line}
		}
		// Match `; fi` either at end of line or followed by a
		// trailing redirection (`fi > /dev/null`). Capture the
		// trailing portion so it can be appended to the closing
		// `fi` line.
		fiIdx := -1
		for i := 0; i+len("; fi") <= len(trim); i++ {
			if trim[i:i+len("; fi")] == "; fi" &&
				(i+len("; fi") == len(trim) ||
					trim[i+len("; fi")] == ' ' ||
					trim[i+len("; fi")] == ';') {
				fiIdx = i
			}
		}
		if fiIdx < 0 {
			return []string{line}
		}
		ifChain := trim[:fiIdx+len("; fi")]
		trailing := strings.TrimSpace(trim[fiIdx+len("; fi"):])
		out := splitIfLine(line, indent, ifChain)
		if trailing != "" && len(out) > 0 {
			// Append trailing redirection to the closing `fi`.
			out[len(out)-1] += " " + trailing
		}
		return out
	case strings.HasPrefix(trim, "case "):
		if !strings.HasSuffix(trim, " esac") {
			// `case X in` (no body on this line — printer left
			// the patterns and esac for following lines). bash
			// 5.3 emits this with a trailing space: `case X in `.
			if strings.HasSuffix(trim, " in") {
				return []string{line + " "}
			}
			return []string{line}
		}
		return splitCaseLine(line, indent, trim)
	}
	// Nested function declaration: `NAME() { BODY; } [trailing]` →
	//   function NAME ()
	//   {
	//       BODY
	//   } trailing
	// bash 5.3 declare -f renders nested function declarations
	// using the `function NAME ()` form. Only fires when the line
	// matches the pattern `<ident>() { ... }`.
	if idx := strings.Index(trim, "() { "); idx > 0 {
		name := trim[:idx]
		if syntax.ValidName(name) {
			rest := trim[idx+len("() { "):]
			// Find matching closing `}` at top level.
			closer := -1
			depthC := 1
			depthP := 0
			inSgl, inDbl := false, false
			for k := 0; k < len(rest); k++ {
				c := rest[k]
				if c == '\\' && k+1 < len(rest) {
					k++
					continue
				}
				switch {
				case inSgl:
					if c == '\'' {
						inSgl = false
					}
				case inDbl:
					if c == '"' {
						inDbl = false
					}
				case c == '\'':
					inSgl = true
				case c == '"':
					inDbl = true
				case c == '(':
					depthP++
				case c == ')':
					if depthP > 0 {
						depthP--
					}
				case c == '{':
					depthC++
				case c == '}':
					depthC--
					if depthC == 0 {
						closer = k
					}
				}
				if closer >= 0 {
					break
				}
			}
			if closer >= 0 {
				body := strings.TrimSpace(rest[:closer])
				body = strings.TrimSuffix(body, ";")
				body = strings.TrimSpace(body)
				trailing := strings.TrimSpace(rest[closer+1:])
				ind := strings.Repeat(" ", indent)
				inner := strings.Repeat(" ", indent+4)
				out := []string{
					ind + "function " + name + " () ",
					ind + "{ ",
				}
				for _, b := range splitTopLevel(body, ";") {
					b = strings.TrimSpace(b)
					if b == "" {
						continue
					}
					for _, sub := range splitCompoundLine(inner + b) {
						out = append(out, sub)
					}
				}
				closeBrace := ind + "}"
				if trailing != "" {
					closeBrace += " " + trailing
				}
				out = append(out, closeBrace)
				return out
			}
		}
	}
	// Subshell wrapping a brace group: `( { X; } ) [trailing]` →
	//   ( {
	//       X
	//   } ) trailing
	// Detect the `( { ` prefix and `} )` suffix (with possible
	// trailing ops after the `)`).
	if strings.HasPrefix(trim, "( { ") {
		// Find `} )` at top level (with whatever trailing).
		braceEnd := -1
		for k := 0; k+2 < len(trim); k++ {
			if trim[k] == '}' && trim[k+1] == ' ' && trim[k+2] == ')' {
				braceEnd = k
				break
			}
		}
		if braceEnd > 4 {
			body := strings.TrimSpace(trim[4:braceEnd])
			body = strings.TrimSuffix(body, ";")
			body = strings.TrimSpace(body)
			trailing := strings.TrimSpace(trim[braceEnd+3:])
			ind := strings.Repeat(" ", indent)
			inner := strings.Repeat(" ", indent+4)
			out := []string{ind + "( { "}
			for _, b := range splitTopLevel(body, ";") {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				for _, sub := range splitCompoundLine(inner + b) {
					out = append(out, sub)
				}
			}
			closer := ind + "} )"
			if trailing != "" {
				closer += " " + trailing
			}
			out = append(out, closer)
			return out
		}
	}
	// Single-line brace-group `{ X; Y; }` → multi-line bash form:
	//
	//	{
	//	    X
	//	    Y
	//	}
	//
	// Only triggers when the line STARTS with `{ ` and contains
	// the matching `}` at the top level. The closing brace may
	// be followed by trailing ops (`}; foo` or `} > file`).
	if strings.HasPrefix(trim, "{ ") {
		// Find matching `}` at top level.
		closer := indexUnnestedBraceClose(trim)
		if closer > 0 {
			body := strings.TrimSpace(trim[2:closer])
			trailing := strings.TrimSpace(trim[closer+1:])
			body = strings.TrimSuffix(body, ";")
			body = strings.TrimSpace(body)
			ind := strings.Repeat(" ", indent)
			inner := strings.Repeat(" ", indent+4)
			out := []string{ind + "{ "}
			for _, b := range splitTopLevel(body, ";") {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				for _, sub := range splitCompoundLine(inner + b) {
					out = append(out, sub)
				}
			}
			closeBrace := ind + "}"
			if trailing != "" {
				closeBrace += trailing
			}
			out = append(out, closeBrace)
			return out
		}
	}
	// Per-line case item: `PAT) BODY ;;` — the printer emits each
	// case item on its own line when the case is multi-line. Split
	// into pattern / body / ;; lines, indented one level deeper
	// than the surrounding `case ... in` (bash 5.3 convention).
	if strings.HasSuffix(trim, " ;;") || strings.HasSuffix(trim, ";;") {
		patEnd := indexUnnestedRune(trim, ')')
		if patEnd > 0 && patEnd < len(trim)-1 {
			pat := trim[:patEnd]
			body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(trim[patEnd+1:], ";;"), " ;"))
			// Indent patterns +4 relative to the printer's
			// position so they sit inside the surrounding
			// `case ... in` after bashDeclareFmt's outer +4
			// prepend.
			patInd := strings.Repeat(" ", indent+4)
			bodyInd := strings.Repeat(" ", indent+8)
			out := []string{patInd + pat + ")"}
			for _, b := range splitTopLevel(body, ";") {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				for _, sub := range splitCompoundLine(bodyInd + b) {
					out = append(out, sub)
				}
			}
			out = append(out, patInd+";;")
			return out
		}
	}
	return []string{line}
}

// splitCaseLine handles `case X in PAT) BODY ;; PAT2) BODY2 ;; esac` →
// multi-line bash 5.3 declare -f format:
//
//	case X in
//	    a)
//	        BODY
//	    ;;
//	    b)
//	        BODY2
//	    ;;
//	esac
func splitCaseLine(orig string, indent int, trim string) []string {
	ind := strings.Repeat(" ", indent)
	inner := strings.Repeat(" ", indent+4)
	innerBody := strings.Repeat(" ", indent+8)
	// trim = `case X in PATS esac`. Strip prefix `case ` and suffix ` esac`.
	rest := strings.TrimSuffix(strings.TrimPrefix(trim, "case "), " esac")
	// rest = `X in PATS`. Find ` in ` to separate subject.
	inIdx := strings.Index(rest, " in ")
	if inIdx < 0 {
		return []string{orig}
	}
	subject := rest[:inIdx]
	pats := rest[inIdx+len(" in "):]
	// Trim leading/trailing space and a trailing `;` from the
	// patterns block (sometimes the printer leaves one).
	pats = strings.TrimSpace(pats)
	// Bash 5.3 includes a trailing space after `in `: "case x in ".
	out := []string{ind + "case " + subject + " in "}
	// Walk pats looking for each PAT) BODY ;; (or `;&`, `;;&`, etc.)
	// We rely on splitTopLevel to break at `;;` separators.
	items := splitCaseItems(pats)
	for _, it := range items {
		patEnd := indexUnnestedRune(it, ')')
		if patEnd < 0 {
			return []string{orig}
		}
		pat := strings.TrimSpace(it[:patEnd])
		body := strings.TrimSpace(it[patEnd+1:])
		// Body may end with `;;` — splitCaseItems already stripped
		// the separator, so trim defensively.
		body = strings.TrimSuffix(body, ";;")
		body = strings.TrimSpace(body)
		out = append(out, inner+pat+")")
		// Body may have multiple stmts.
		for _, b := range splitTopLevel(body, ";") {
			b = strings.TrimSpace(b)
			if b == "" {
				continue
			}
			for _, sub := range splitCompoundLine(innerBody + b) {
				out = append(out, sub)
			}
		}
		out = append(out, inner+";;")
	}
	out = append(out, ind+"esac")
	return out
}

// splitCaseItems splits a case-pattern body string at top-level `;;`
// boundaries (respecting nested parens/quotes). Returns items WITHOUT
// the trailing `;;`.
func splitCaseItems(s string) []string {
	var out []string
	depthP := 0
	inSgl, inDbl := false, false
	last := 0
	for i := 0; i+1 < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		switch {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
		case inDbl:
			if c == '"' {
				inDbl = false
			}
		case c == '\'':
			inSgl = true
		case c == '"':
			inDbl = true
		case c == '(':
			depthP++
		case c == ')':
			if depthP > 0 {
				depthP--
			}
		case depthP == 0 && c == ';' && s[i+1] == ';':
			out = append(out, s[last:i])
			last = i + 2
			// Skip a possible third char for `;;&`.
			if last < len(s) && s[last] == '&' {
				last++
			}
			i = last - 1
		}
	}
	if last < len(s) {
		tail := strings.TrimSpace(s[last:])
		if tail != "" {
			out = append(out, tail)
		}
	}
	return out
}

// indexUnnestedBraceClose returns the index of the matching `}` for a
// brace group that starts at s[0] (assumed to be `{`). Tracks nested
// braces, parens, and quoted regions. -1 if not found.
func indexUnnestedBraceClose(s string) int {
	depthC := 0
	depthP := 0
	inSgl, inDbl := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		switch {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
			continue
		case inDbl:
			if c == '"' {
				inDbl = false
			}
			continue
		case c == '\'':
			inSgl = true
			continue
		case c == '"':
			inDbl = true
			continue
		case c == '(':
			depthP++
			continue
		case c == ')':
			if depthP > 0 {
				depthP--
			}
			continue
		case c == '{':
			depthC++
			continue
		case c == '}':
			depthC--
			if depthC == 0 && depthP == 0 {
				return i
			}
		}
	}
	return -1
}

// indexUnnestedRune returns the index of the first occurrence of r in
// s that is not inside parens/brackets/quotes. -1 if not found.
func indexUnnestedRune(s string, r byte) int {
	depthP, depthB := 0, 0
	inSgl, inDbl := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			continue
		}
		switch {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
			continue
		case inDbl:
			if c == '"' {
				inDbl = false
			}
			continue
		case c == '\'':
			inSgl = true
			continue
		case c == '"':
			inDbl = true
			continue
		case c == '(':
			depthP++
			continue
		case c == '[':
			depthB++
			continue
		case c == ']':
			if depthB > 0 {
				depthB--
			}
			continue
		}
		if c == r && depthP == 0 && depthB == 0 {
			return i
		}
		if c == ')' && depthP > 0 {
			depthP--
		}
	}
	return -1
}

// splitIfLine handles `if X; then A; elif Y; then B; else C; fi` →
// bash 5.3's multi-line declare -f form, where elif chains are
// rendered as NESTED `else; if; fi` (each elif level adds an
// indented `if/fi` pair inside the prior `else`):
//
//	if X; then
//	    A
//	else
//	    if Y; then
//	        B
//	    else
//	        C
//	    fi
//	fi
//
// ifClause is one (condition, body) pair from an if/elif chain.
// Used by splitIfLine + renderNestedIf to convert flat elif chains
// to the bash 5.3 nested-if rendering.
type ifClause struct{ cond, body string }

func splitIfLine(orig string, indent int, trim string) []string {
	rest := strings.TrimSuffix(trim[len("if "):], "; fi")
	var clauses []ifClause
	var elseBody string
	for {
		thenIdx := strings.Index(rest, "; then ")
		if thenIdx < 0 {
			return []string{orig}
		}
		cond := rest[:thenIdx]
		afterThen := rest[thenIdx+len("; then "):]
		elifIdx := strings.Index(afterThen, "; elif ")
		elseIdx := strings.Index(afterThen, "; else ")
		switch {
		case elifIdx >= 0 && (elseIdx < 0 || elifIdx < elseIdx):
			clauses = append(clauses, ifClause{cond, afterThen[:elifIdx]})
			rest = afterThen[elifIdx+len("; elif "):]
		case elseIdx >= 0:
			clauses = append(clauses, ifClause{cond, afterThen[:elseIdx]})
			elseBody = afterThen[elseIdx+len("; else "):]
			goto done
		default:
			clauses = append(clauses, ifClause{cond, afterThen})
			goto done
		}
	}
done:
	return renderNestedIf(indent, clauses, elseBody)
}

// renderNestedIf emits the bash 5.3 nested-if form: each clause after
// the first becomes an `else { if ... fi }` block nested one
// indentation level deeper. lastIndent positions the surrounding
// `if`/`fi`; the recursion handles deeper levels.
func renderNestedIf(indent int, clauses []ifClause, elseBody string) []string {
	if len(clauses) == 0 {
		return nil
	}
	ind := strings.Repeat(" ", indent)
	inner := strings.Repeat(" ", indent+4)
	first := clauses[0]
	out := []string{ind + "if " + first.cond + "; then"}
	for _, b := range splitTopLevel(first.body, ";") {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		for _, sub := range splitCompoundLine(inner + b) {
			out = append(out, sub)
		}
	}
	rest := clauses[1:]
	if len(rest) > 0 || elseBody != "" {
		out = append(out, ind+"else")
		if len(rest) > 0 {
			for _, sub := range renderNestedIf(indent+4, rest, elseBody) {
				out = append(out, sub)
			}
		} else {
			for _, b := range splitTopLevel(elseBody, ";") {
				b = strings.TrimSpace(b)
				if b == "" {
					continue
				}
				for _, sub := range splitCompoundLine(inner + b) {
					out = append(out, sub)
				}
			}
		}
	}
	out = append(out, ind+"fi")
	return out
}

// splitTopLevel splits s on sep that is NOT inside nested parens,
// brackets, braces, or quoted strings.
func splitTopLevel(s, sep string) []string {
	var out []string
	var b strings.Builder
	depthP, depthB, depthC := 0, 0, 0
	inSgl, inDbl := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		switch {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
		case inDbl:
			if c == '"' {
				inDbl = false
			}
		case c == '\'':
			inSgl = true
		case c == '"':
			inDbl = true
		case c == '(':
			depthP++
		case c == ')':
			depthP--
		case c == '[':
			depthB++
		case c == ']':
			depthB--
		case c == '{':
			depthC++
		case c == '}':
			depthC--
		case depthP == 0 && depthB == 0 && depthC == 0 && c == sep[0]:
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// bashNestElifChain converts multi-line if-elif-else-fi chains in
// declare -f output to bash 5.3's nested else/if/fi rendering. The
// printer emits the source-level form (with the `elif` keyword); bash
// rewrites it to nested ifs.
//
// Example transformation:
//
//	if X; then       →    if X; then
//	    A                     A
//	elif Y; then          else
//	    B                     if Y; then
//	else                          B
//	    C                     else
//	fi                            C
//	                          fi
//	                      fi
//
// The closing `fi` line keeps any trailing redirection.
// bashCollapseSubshellHdoc collapses the multi-line subshell-with-
// heredoc layout to bash 5.3's declare -f form. The input has
// already been through the +4 indent pass:
//
//	(                       →    ( cat <<EOF
//	    cat <<EOF                body
//	body                         EOF
//	EOF                         );
//	)
//
// Only fires when the subshell contains exactly one statement and
// that statement opens a heredoc. The closing `)` becomes ` );`
// at column 1 (one leading space), matching bash 5.3 quirks.
func bashCollapseSubshellHdoc(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		trim := strings.TrimSpace(l)
		if trim != "(" || i+1 >= len(lines) {
			out = append(out, l)
			continue
		}
		// Find a heredoc opener on the next non-empty line.
		stmt := strings.TrimRight(lines[i+1], "\n")
		stmtTrim := strings.TrimSpace(stmt)
		// Strip a single trailing `;` that the +4/`;` pass may
		// have appended.
		stmtTrim = strings.TrimSuffix(stmtTrim, ";")
		hdIdx := findHeredocOp(stmtTrim)
		if hdIdx < 0 {
			out = append(out, l)
			continue
		}
		// Extract the heredoc tag.
		after := stmtTrim[hdIdx+2:]
		after = strings.TrimLeft(after, "-")
		after = strings.TrimLeft(after, " \t")
		tag := after
		tag = strings.TrimRight(tag, " \t")
		tag = strings.Trim(tag, "'\"")
		if tag == "" {
			out = append(out, l)
			continue
		}
		// Find the terminator line and the closing `)` after it.
		termIdx := -1
		for k := i + 2; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == tag {
				termIdx = k
				break
			}
		}
		if termIdx < 0 {
			out = append(out, l)
			continue
		}
		// Find the next non-empty line after the terminator —
		// it must be a lone `)` (possibly with trailing ops).
		closeIdx := -1
		for k := termIdx + 1; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == "" {
				continue
			}
			closeIdx = k
			break
		}
		if closeIdx < 0 {
			out = append(out, l)
			continue
		}
		closeTrim := strings.TrimSpace(lines[closeIdx])
		if !strings.HasPrefix(closeTrim, ")") {
			out = append(out, l)
			continue
		}
		trailing := strings.TrimSuffix(closeTrim[1:], ";")
		indent := l[:len(l)-len(trim)]
		// Emit `<indent>( STMT` line, the heredoc body, terminator,
		// then ` );` plus any trailing ops at column 1 (bash 5.3
		// quirk: the close lands one space in regardless of the
		// surrounding indentation).
		out = append(out, indent+"( "+stmtTrim)
		for k := i + 2; k <= termIdx; k++ {
			out = append(out, lines[k])
		}
		out = append(out, " )"+trailing+";")
		i = closeIdx
	}
	return strings.Join(out, "\n")
}

func bashCollapseCoprocSubshellHdoc(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		trim := strings.TrimSpace(l)
		if trim != "coproc (;" || i+1 >= len(lines) {
			out = append(out, l)
			continue
		}
		stmtTrim := strings.TrimSuffix(strings.TrimSpace(lines[i+1]), ";")
		hdIdx := findHeredocOp(stmtTrim)
		if hdIdx < 0 {
			out = append(out, l)
			continue
		}
		after := strings.TrimLeft(stmtTrim[hdIdx+2:], "-")
		after = strings.TrimLeft(after, " \t")
		tag := strings.Trim(strings.TrimRight(after, " \t"), "'\"")
		if tag == "" {
			out = append(out, l)
			continue
		}
		termIdx := -1
		for k := i + 2; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == tag {
				termIdx = k
				break
			}
		}
		if termIdx < 0 {
			out = append(out, l)
			continue
		}
		closeIdx := -1
		for k := termIdx + 1; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == "" {
				continue
			}
			closeIdx = k
			break
		}
		if closeIdx < 0 || strings.TrimSpace(lines[closeIdx]) != ")" {
			out = append(out, l)
			continue
		}
		indent := l[:len(l)-len(trim)]
		out = append(out, indent+"coproc COPROC ( "+stmtTrim)
		for k := i + 2; k <= termIdx; k++ {
			out = append(out, lines[k])
		}
		out = append(out, " );")
		i = closeIdx
	}
	return strings.Join(out, "\n")
}

// bashSubshellSpace pads subshell `(EXPR)` openers/closers with one
// space inside the parens to match bash 5.3 declare -f rendering:
// `(exit 1)` → `( exit 1 )`. Per-line: if the trimmed line starts
// with `(` (and not `((` arith) and contains the matching `)` at
// top level, pad. Trailing ops after the closer are preserved.
func bashSubshellSpace(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		ind := leadingSpaces(l)
		trim := l[ind:]
		// Only fire when the line begins with `(` but NOT `((`
		// (which is arith, handled separately).
		if !strings.HasPrefix(trim, "(") || strings.HasPrefix(trim, "((") {
			continue
		}
		// Find matching `)` at top level.
		closer := -1
		depth := 0
		inSgl, inDbl := false, false
		for j := 0; j < len(trim); j++ {
			c := trim[j]
			if c == '\\' && j+1 < len(trim) {
				j++
				continue
			}
			switch {
			case inSgl:
				if c == '\'' {
					inSgl = false
				}
			case inDbl:
				if c == '"' {
					inDbl = false
				}
			case c == '\'':
				inSgl = true
			case c == '"':
				inDbl = true
			case c == '(':
				depth++
			case c == ')':
				depth--
				if depth == 0 {
					closer = j
				}
			}
			if closer >= 0 {
				break
			}
		}
		if closer < 0 {
			continue
		}
		inner := strings.TrimSpace(trim[1:closer])
		trailing := trim[closer+1:]
		if inner == "" {
			continue
		}
		lines[i] = strings.Repeat(" ", ind) + "( " + inner + " )" + trailing
	}
	return strings.Join(lines, "\n")
}

// bashCaretCtrlChars rewrites ANSI-C control-char escapes inside
// `$'...'` strings to bash 5.3's caret-notation single-quoted form:
//
//	$'\001'  →  '^A'
//	$'\037'  →  '^_'
//	$'\177'  →  '^?'
//
// Only fires when the $'...' content is purely control-char escapes
// (and no other text); mixed-content $'X\001Y' stays as-is.
func bashCaretCtrlChars(body string) string {
	// Pattern: $' [\ + 3 octal digits | \ + 1-2 octal digits] $'
	// → caret form. We do a simple linear scan.
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		if i+1 < len(body) && body[i] == '$' && body[i+1] == '\'' {
			// Look for matching `'` and check if body is all
			// `\NNN` octal escapes that map to control chars.
			end := -1
			for j := i + 2; j < len(body); j++ {
				if body[j] == '\\' && j+1 < len(body) {
					j++
					continue
				}
				if body[j] == '\'' {
					end = j
					break
				}
			}
			if end < 0 {
				b.WriteByte(body[i])
				continue
			}
			content := body[i+2 : end]
			if caret, ok := allOctalControlChars(content); ok {
				b.WriteByte('\'')
				b.WriteString(caret)
				b.WriteByte('\'')
				i = end
				continue
			}
		}
		b.WriteByte(body[i])
	}
	return b.String()
}

// allOctalControlChars parses a string composed entirely of `\NNN`
// octal escapes mapping to control characters (0x01–0x1F, 0x7F).
// Returns the caret-notation string and true if so.
func allOctalControlChars(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\\' {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			return "", false
		}
		// Read 1-3 octal digits.
		end := i + 1
		for end < len(s) && end < i+4 && s[end] >= '0' && s[end] <= '7' {
			end++
		}
		if end == i+1 {
			return "", false
		}
		var v int
		for k := i + 1; k < end; k++ {
			v = v*8 + int(s[k]-'0')
		}
		switch {
		case v >= 0x01 && v <= 0x1F:
			b.WriteByte('^')
			b.WriteByte(byte('A' - 1 + v))
		case v == 0x7F:
			b.WriteString("^?")
		default:
			return "", false
		}
		i = end
	}
	return b.String(), true
}

// bashFdExplicit walks each line and rewrites implicit redirections
// (no leading fd digit) to bash 5.3's explicit form. Only fires when
// the redirection is at the boundary of words (preceded by a space or
// at line start), to avoid mangling things inside strings.
func bashFdExplicit(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = fdExplicitLine(l)
	}
	return strings.Join(lines, "\n")
}

// fdExplicitLine rewrites ` >&N` → ` 1>&N` and ` <&N` → ` 0<&N` in
// a single line. Only fires on the `&`-duplicate forms (bash 5.3
// does NOT normalise plain file `> file` / `< file` to explicit fd).
// Skips already-explicit (`1>&N`), arith-comparison contexts (`<`
// inside `(( ))`), heredocs (`<<`), `<>`, `>>`, `>|`, and strings.
func fdExplicitLine(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	inSgl, inDbl := false, false
	arithDepth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		switch {
		case inSgl:
			b.WriteByte(c)
			if c == '\'' {
				inSgl = false
			}
			continue
		case inDbl:
			b.WriteByte(c)
			if c == '"' {
				inDbl = false
			}
			continue
		case c == '\'':
			inSgl = true
			b.WriteByte(c)
			continue
		case c == '"':
			inDbl = true
			b.WriteByte(c)
			continue
		}
		// Track `((` / `))` so we don't mangle arith comparison
		// operators (`i < 3`, `7 > 40`).
		if c == '(' && i+1 < len(s) && s[i+1] == '(' {
			arithDepth++
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if arithDepth > 0 && c == ')' && i+1 < len(s) && s[i+1] == ')' {
			arithDepth--
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if arithDepth > 0 {
			b.WriteByte(c)
			continue
		}
		// Word-boundary check: only rewrite when not part of a
		// preceding word (so `2>&3` isn't touched).
		boundary := i == 0 || s[i-1] == ' ' || s[i-1] == '\t' ||
			s[i-1] == ';' || s[i-1] == '&' || s[i-1] == '|' ||
			s[i-1] == '\n'
		if !boundary {
			b.WriteByte(c)
			continue
		}
		// `>&` (no leading fd) → `1>&`.
		if c == '>' && i+1 < len(s) && s[i+1] == '&' {
			b.WriteString("1>")
			continue
		}
		// `<&` → `0<&`.
		if c == '<' && i+1 < len(s) && s[i+1] == '&' {
			b.WriteString("0<")
			continue
		}
		b.WriteByte(c)
	}
	return normalizeCloseRedir(b.String())
}

// normalizeCloseRedir rewrites the `<&-` close-read form to `>&-`
// (close-write) to match bash 5.3's declare -f normalisation: both
// directions are equivalent for closing a file descriptor, and bash
// renders the close-write form regardless of source.
func normalizeCloseRedir(s string) string {
	if !strings.Contains(s, "<&-") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	inSgl, inDbl := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		switch {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
		case inDbl:
			if c == '"' {
				inDbl = false
			}
		case c == '\'':
			inSgl = true
		case c == '"':
			inDbl = true
		case c == '<' && i+2 < len(s) && s[i+1] == '&' && s[i+2] == '-':
			b.WriteString(">&-")
			i += 2
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// bashBraceTrailSpace adds the bash 5.3 trailing space after any line
// that ends with a `{` opener in declare -f output. Covers both:
//   - bare standalone `{`
//   - prefixed forms like `coproc a {`, `function f () {`
//
// Idempotent — skips lines already ending in `{ ` (trailing space).
func bashBraceTrailSpace(body string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasSuffix(l, "{") {
			lines[i] = l + " "
		}
	}
	return strings.Join(lines, "\n")
}

func bashNestElifChain(body string) string {
	lines := strings.Split(body, "\n")
	// Find if-chain bounds: groups of lines starting with `if ` and
	// ending with `fi[ <redir>]` at the same indent that contain
	// at least one `elif `. Convert each found chain.
	for i := 0; i < len(lines); i++ {
		ind := leadingSpaces(lines[i])
		trim := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trim, "if ") || !strings.HasSuffix(trim, "; then") {
			continue
		}
		// Walk forward to find the matching `fi` at the same indent.
		fiIdx := -1
		hasElif := false
		for j := i + 1; j < len(lines); j++ {
			jInd := leadingSpaces(lines[j])
			if jInd != ind {
				continue
			}
			jTrim := strings.TrimSpace(lines[j])
			if jTrim == "elif" || strings.HasPrefix(jTrim, "elif ") {
				hasElif = true
				continue
			}
			if jTrim == "fi" || strings.HasPrefix(jTrim, "fi ") || strings.HasPrefix(jTrim, "fi;") {
				fiIdx = j
				break
			}
			if strings.HasPrefix(jTrim, "if ") {
				// nested same-indent if — give up; let recursion
				// handle inner chains separately.
				break
			}
		}
		if fiIdx < 0 || !hasElif {
			continue
		}
		// Convert lines [i..fiIdx] in place.
		converted := convertElifBlock(lines[i:fiIdx+1], ind)
		newLines := make([]string, 0, len(lines)-(fiIdx-i+1)+len(converted))
		newLines = append(newLines, lines[:i]...)
		newLines = append(newLines, converted...)
		newLines = append(newLines, lines[fiIdx+1:]...)
		lines = newLines
		// Re-process from the same i to handle remaining elifs in
		// chains converted into nested form (recursive nesting).
		// The outer i still points at the converted `if X; then`
		// line; the inner elif chain (now nested) needs another
		// pass. Decrement i so the loop re-examines this line.
		i = -1
	}
	return strings.Join(lines, "\n")
}

// convertElifBlock takes the lines of an if-elif-...-fi block (at
// indent `ind`) and returns the bash 5.3 nested form. The original
// block's first line is `if COND; then` at indent `ind`, and the
// last is `fi[ <redir>]` at indent `ind`.
func convertElifBlock(block []string, ind int) []string {
	// Locate the FIRST `elif` line at indent ind; nest it.
	for j := 1; j < len(block); j++ {
		jInd := leadingSpaces(block[j])
		if jInd != ind {
			continue
		}
		jTrim := strings.TrimSpace(block[j])
		if !strings.HasPrefix(jTrim, "elif ") {
			continue
		}
		// Replace `elif COND; then` with `else` + nested
		// `    if COND; then`. Then indent the rest of the
		// block (until the trailing `fi`) by +4, and add an
		// extra `fi` at indent+4 before the original `fi`.
		out := make([]string, 0, len(block)+3)
		out = append(out, block[:j]...) // up to (not including) the elif
		out = append(out, strings.Repeat(" ", ind)+"else")
		// Convert `elif COND; then` → `if COND; then` at ind+4.
		condThen := strings.TrimPrefix(jTrim, "elif ")
		out = append(out, strings.Repeat(" ", ind+4)+"if "+condThen)
		// Re-indent the remainder (lines j+1 .. end-1) by +4.
		for k := j + 1; k < len(block)-1; k++ {
			out = append(out, "    "+block[k])
		}
		// Add nested `fi` at indent+4, then the original `fi`
		// (with any trailing redir).
		out = append(out, strings.Repeat(" ", ind+4)+"fi")
		out = append(out, block[len(block)-1])
		return out
	}
	return block
}

// leadingSpaces counts the number of leading space chars in s.
func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// bashArithSpace pads the contents of `((...))` and `$((...))` runs
// with single spaces inside the parens to match bash 5.3's
// `declare -f` rendering: `((i<3))` → `(( i<3 ))`. Skips already-
// padded constructs to avoid `(( (( x )) ))` blowing up to
// `(((  ( ( x )  ) ))`. Handles nested parens by counting depth.
func bashArithSpace(body string) string {
	var b strings.Builder
	b.Grow(len(body) + 32)
	i := 0
	for i < len(body) {
		// Detect `$((` or `((` at top level. Skip `for ((...))` —
		// bash 5.3 declare -f preserves the source's arith-for
		// header spacing (sometimes spaced, sometimes not).
		start, prefix := -1, ""
		switch {
		case i+2 < len(body) && body[i] == '$' && body[i+1] == '(' && body[i+2] == '(':
			start, prefix = i, "$(("
		case i+1 < len(body) && body[i] == '(' && body[i+1] == '(':
			// Skip arith-for: look back for "for " before the `((`.
			//   - skip optional whitespace
			//   - check for trailing "for"
			j := i - 1
			for j >= 0 && (body[j] == ' ' || body[j] == '\t') {
				j--
			}
			if j >= 2 && body[j] == 'r' && body[j-1] == 'o' && body[j-2] == 'f' &&
				(j == 2 || body[j-3] == ' ' || body[j-3] == '\t' || body[j-3] == '\n') {
				// `for ((`: leave the contents alone.
				b.WriteByte(body[i])
				i++
				continue
			}
			start, prefix = i, "(("
		}
		if start < 0 {
			b.WriteByte(body[i])
			i++
			continue
		}
		// Walk forward to find the position of the FIRST `)` of
		// the closing `))`. Track depth (open `((` counts as 2).
		// Stop when depth would drop to 0 — that means the current
		// `)` is the inner of the pair and the next `)` closes the
		// outer.
		j := start + len(prefix)
		depth := 2
		found := false
		for j < len(body) {
			switch body[j] {
			case '(':
				depth++
			case ')':
				if depth == 2 && j+1 < len(body) && body[j+1] == ')' {
					// `))` closing the outer arith.
					found = true
				} else {
					depth--
				}
			}
			if found {
				break
			}
			j++
		}
		if !found {
			// Couldn't find a matching `))` — emit as-is, skip just
			// the opening to avoid infinite loop.
			b.WriteString(prefix)
			i = start + len(prefix)
			continue
		}
		// content is between start+len(prefix) and j (exclusive) and j+1
		// is the second `)` of the closing `))`.
		content := body[start+len(prefix) : j]
		if strings.Contains(content, "\n") {
			b.WriteString(body[start : j+2])
			i = j + 2
			continue
		}
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			// `(())` — leave it as-is.
			b.WriteString(body[start : j+2])
		} else {
			b.WriteString(prefix)
			b.WriteByte(' ')
			b.WriteString(trimmed)
			b.WriteString(" ))")
		}
		i = j + 2
	}
	return b.String()
}

// compactArithAssign removes single spaces around assignment-family
// operators (`=`, `+=`, `-=`, `*=`, `/=`, `%=`, `&=`, `|=`, `^=`)
// so `i = 0` becomes `i=0` to match bash 5.3's declare -f arith-for
// header. Comparison operators (`==`, `!=`, `<`, `<=`, `>`, `>=`,
// `&&`, `||`) and other non-assign ops are preserved verbatim —
// bash echoes the source spacing for those.
func compactArithAssign(s string) string {
	// Single-pass regex-like rewrite: look for spaces flanking an
	// `=` token where the left-side identifier ends in a valid
	// assignment context. We scan for occurrences of ` = ` or
	// ` += `, ` -= ` … and collapse them. Comparison `==`/`!=`/
	// `<=`/`>=` stay spaced.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Look for `<space>OP<space>` where OP is assignment.
		if c == ' ' && i+2 < len(s) && s[i+2] == ' ' {
			// Single-char op: `=`
			op := s[i+1]
			if op == '=' {
				// Reject comparison `==` (would be ` == `, but
				// then s[i+2] would be `=` not ` `, so we'd
				// never reach here).
				out = trimTrailingSpace(out)
				out = append(out, '=')
				i += 2
				continue
			}
			// Two-char op: `+=`, `-=`, `*=`, `/=`, `%=`, `&=`,
			// `|=`, `^=`, `<<=`, `>>=`. We're scanning a single
			// `<space>OP<space>` pattern (OP being s[i+1] then
			// `=`). Look for that shape: s[i+1] is op char,
			// s[i+2] is `=`, s[i+3] is space.
		}
		if c == ' ' && i+3 < len(s) && s[i+3] == ' ' && s[i+2] == '=' {
			op := s[i+1]
			switch op {
			case '+', '-', '*', '/', '%', '&', '|', '^':
				// Reject `<=`, `>=`, `!=` (comparisons)
				// — handled separately, none reach here
				// because the leading char wouldn't match.
				out = trimTrailingSpace(out)
				out = append(out, op, '=')
				i += 3
				continue
			}
		}
		out = append(out, c)
	}
	return string(out)
}

// trimTrailingSpace strips a single trailing space from buf and
// returns the truncated slice. Helper for compactArithAssign so we
// drop the space we already emitted on the left side of an `=`.
func trimTrailingSpace(buf []byte) []byte {
	if n := len(buf); n > 0 && buf[n-1] == ' ' {
		return buf[:n-1]
	}
	return buf
}

// bashArithForNorm normalises the `for ((init; cond; post))` header in
// declare -f output to match bash 5.3:
//   - empty init/cond/post slots become `1` (a no-op true expression);
//   - a single space is inserted before the closing `))`;
//   - the rest of the contents is left untouched.
//
// `for ((;;))` becomes `for ((1; 1; 1))`. `for (( ; i<3; i++))` becomes
// `for ((1; i<3; i++ ))`.
func bashArithForNorm(body string) string {
	var b strings.Builder
	b.Grow(len(body) + 16)
	i := 0
	for i < len(body) {
		// Look for `for ((` at the start of the trimmed line.
		if !(i+6 <= len(body) && body[i] == 'f' && body[i+1] == 'o' && body[i+2] == 'r' &&
			(body[i+3] == ' ' || body[i+3] == '\t')) {
			b.WriteByte(body[i])
			i++
			continue
		}
		// Need previous char (if any) to be a line start or whitespace.
		if i > 0 {
			prev := body[i-1]
			if prev != '\n' && prev != ' ' && prev != '\t' && prev != ';' {
				b.WriteByte(body[i])
				i++
				continue
			}
		}
		// Find `((` after "for ".
		k := i + 4
		for k < len(body) && (body[k] == ' ' || body[k] == '\t') {
			k++
		}
		if !(k+1 < len(body) && body[k] == '(' && body[k+1] == '(') {
			b.WriteByte(body[i])
			i++
			continue
		}
		// Find matching `))`, tracking paren depth.
		start := k + 2
		j := start
		depth := 2
		found := false
		for j < len(body) {
			switch body[j] {
			case '(':
				depth++
			case ')':
				if depth == 2 && j+1 < len(body) && body[j+1] == ')' {
					found = true
				} else {
					depth--
				}
			}
			if found {
				break
			}
			j++
		}
		if !found {
			b.WriteByte(body[i])
			i++
			continue
		}
		header := body[start:j]
		// Split on `;` at top-level paren depth.
		parts := []string{}
		level := 0
		last := 0
		for x := 0; x < len(header); x++ {
			switch header[x] {
			case '(':
				level++
			case ')':
				level--
			case ';':
				if level == 0 {
					parts = append(parts, header[last:x])
					last = x + 1
				}
			}
		}
		parts = append(parts, header[last:])
		if len(parts) != 3 {
			b.WriteString(body[i : j+2])
			i = j + 2
			continue
		}
		postEmpty := false
		for idx, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				if idx == 2 {
					postEmpty = true
				}
				p = "1"
			} else {
				// bash 5.3 declare -f keeps assignment-family
				// operators (`=`, `+=`, …) compact inside arith-
				// for headers while preserving spaces around
				// comparison ops (`<`, `>`, `&&`, `||`). Source
				// `i=0` stays `i=0` after a round trip.
				p = compactArithAssign(p)
			}
			parts[idx] = p
		}
		b.WriteString(body[i:k])
		b.WriteString("((")
		b.WriteString(parts[0])
		b.WriteString("; ")
		b.WriteString(parts[1])
		b.WriteString("; ")
		b.WriteString(parts[2])
		if postEmpty {
			b.WriteString("))")
		} else {
			b.WriteString(" ))")
		}
		i = j + 2
	}
	return b.String()
}

// findHeredocOp returns the index of the first `<<` in line that is
// NOT part of `<<<` (here-string), or -1 if none. Used by
// bashDeclareFmt to distinguish heredoc openers from here-string ops.
func findHeredocOp(line string) int {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != '<' || line[i+1] != '<' {
			continue
		}
		// `<<<` (here-string) — skip past all the `<`s.
		if i+2 < len(line) && line[i+2] == '<' {
			for i+1 < len(line) && line[i+1] == '<' {
				i++
			}
			continue
		}
		return i
	}
	return -1
}

// bashDeclareFmt reformats a printer-produced statement body to match
// bash 5.3's `declare -f` rendering. Per-stmt the printer emits the
// opener line at column 0, nested-block bodies at column 4 (via
// Indent(4)), nested-block closers (fi/done/esac/}) at column 0, and
// any heredoc body+terminator at column 0 (heredocs always anchor
// left).
//
// We need to:
//   - prepend 4 spaces to every printer line *except* heredoc body
//     and heredoc terminator lines (those stay at column 0 to match
//     bash);
//   - append `;` to each simple statement except heredoc openers
//     (`cat <<TAG`), heredoc body / terminator, block openers
//     (ending in then/do/in/{/else/operators), block closers
//     (fi/done/esac/}), and the last top-level statement of the
//     function body.
//
// Heredoc context is tracked explicitly via inHdoc: when we see a
// line ending with `<<TAG` we capture TAG; subsequent lines up to and
// including the TAG-only line are heredoc content.
func bashDeclareFmt(body string, lastTop bool) string {
	body = bashMultilineArithExpansion(body)
	// Bash 5.3 renders `((expr))` as `(( expr ))` (space-padded
	// inside the double-parens) in `declare -f` output. Same for
	// arith expansion `$((expr))` → `$(( expr ))`. The printer
	// emits the compact form; pad with regex.
	body = bashArithSpace(body)
	// bash 5.3 pads subshell parens: `(expr)` → `( expr )`. Run
	// BEFORE the compound splitter so subshell-of-block patterns
	// like `( { X; } )` are detectable.
	body = bashSubshellSpace(body)
	body = bashTightenMultilineArithParens(body)
	// bash 5.3 normalises `for ((;;))` headers: empty parts become
	// `1` and a space is added before `))`.
	body = bashArithForNorm(body)
	// Bash 5.3 always renders for/while/until/if as multi-line in
	// `declare -f` even when the source was single-line; the
	// printer collapses to single-line. Split here.
	body = bashSplitCompound(body)
	// bash 5.3 converts multi-line elif chains to nested else/if/fi.
	body = bashNestElifChain(body)
	// bash 5.3 emits standalone `{` openers with a trailing space.
	body = bashBraceTrailSpace(body)
	// bash 5.3 declare/type output expands the pipe-all shorthand.
	body = strings.ReplaceAll(body, " |& ", " 2>&1 | ")
	// bash 5.3 normalises implicit fd1/fd0 to explicit form in
	// declare -f output: `>&2` → `1>&2`, `<&3` → `0<&3`,
	// `> file` → `1> file`, `< file` → `0< file`.
	body = bashFdExplicit(body)
	// bash 5.3 declare -f renders control chars (`$'\001'`) using
	// caret notation (`'^A'`) instead of ANSI-C.
	body = bashCaretCtrlChars(body)
	// Pre-pass: track heredoc terminators so we can insert a blank
	// line after each one (bash 5.3 declare -f convention) when we
	// emit the final joined string.
	preLines := strings.Split(body, "\n")
	hdocTermAt := make(map[int]bool)
	{
		inH := ""
		for i, raw := range preLines {
			trim := strings.TrimSpace(raw)
			if inH != "" {
				if trim == inH {
					inH = ""
					hdocTermAt[i] = true
				}
				continue
			}
			if idx := findHeredocOp(trim); idx >= 0 {
				afterOp := trim[idx+2:]
				rest := strings.TrimLeft(afterOp, "-")
				rest = strings.TrimLeft(rest, " \t")
				end := strings.IndexAny(rest, " \t")
				if end < 0 {
					end = len(rest)
				}
				tag := strings.Trim(rest[:end], "'\"")
				if tag != "" {
					inH = tag
				}
			}
		}
	}

	lines := strings.Split(body, "\n")
	inHdoc := ""
	hdocStripTabs := false
	for i, raw := range lines {
		trim := strings.TrimSpace(raw)
		// Inside a heredoc: leave body / terminator untouched
		// (no re-indent, no `;`). If the opener was `<<-`, strip
		// leading tabs from body and terminator (bash 5.3
		// declare -f matches the runtime tab-stripping).
		if inHdoc != "" {
			if hdocStripTabs {
				lines[i] = strings.TrimLeft(raw, "\t")
			}
			if trim == inHdoc {
				inHdoc = ""
				hdocStripTabs = false
			}
			continue
		}
		// Outside a heredoc: every line gets the 4-space lift.
		lines[i] = "    " + raw
		if trim == "" {
			continue
		}
		// Heredoc opener: line contains `<<TAG`, `<<-TAG`,
		// `<<'TAG'`, or `<<"TAG"`. NOT to be confused with `<<<`
		// (here-strings) which are simple-statement-like and get
		// a trailing `;`. Capture the terminator and skip the `;`
		// so the next line parses as the heredoc body. We scan for
		// the FIRST `<<` that isn't part of `<<<`.
		if idx := findHeredocOp(trim); idx >= 0 {
			// After `<<` (optionally `-` for `<<-`), the next word
			// is the tag. The line may continue with redirections
			// (`<<TAG > file`) or a following compound — strip
			// quotes around the tag and stop at first whitespace.
			afterOp := trim[idx+2:]
			stripTabs := strings.HasPrefix(afterOp, "-")
			rest := strings.TrimLeft(afterOp, "-")
			rest = strings.TrimLeft(rest, " \t")
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			tag := strings.Trim(rest[:end], "'\"")
			if tag != "" {
				inHdoc = tag
				hdocStripTabs = stripTabs
				continue
			}
		}
		// Block openers / mid-clauses: never add `;`.
		switch trim {
		case "{", "do", "then", "else":
			continue
		}
		if strings.HasSuffix(trim, "$((") {
			continue
		}
		// Block closers (fi / done / esac / `}`) DO get `;` when
		// followed by more statements in the body — only the last
		// line of the function (lastTop && last i) is bare. Also
		// bare for `done` / `fi` when the PRECEDING non-empty
		// line was a heredoc terminator (bash 5.3 omits `;` on
		// for/while/until/if closers in that case but KEEPS it
		// on `}` and `esac`).
		switch trim {
		case "fi", "done", "esac", "}":
			lastSkip := false
			if trim == "done" || trim == "fi" {
				for k := i - 1; k >= 0; k-- {
					ptrim := strings.TrimSpace(lines[k])
					if ptrim == "" {
						continue
					}
					if hdocTermAt[k] {
						lastSkip = true
					}
					break
				}
			}
			if !(lastTop && i == len(lines)-1) && !lastSkip {
				lines[i] += ";"
			}
			continue
		}
		// Bare-paren subshell lines (`(` opener, `)` closer) get
		// no `;`; check those before the suffix loop since `)`
		// would otherwise match `))` (arith-exp closing) which
		// IS a simple statement and DOES need `;`.
		if trim == "(" || trim == ")" {
			continue
		}
		skip := false
		for _, suffix := range []string{
			" then", " do", " in", " {", " else",
			";", "&", "|", ";;", ";&", "&&", "||",
		} {
			if strings.HasSuffix(trim, suffix) {
				skip = true
				break
			}
		}
		// Case-pattern line: trimmed text ends with bare `)`
		// (single right paren NOT being `))` from arith). Skip
		// when this looks like a case pattern: doesn't begin with
		// `(` (a subshell stmt like `( foo )` should still get a
		// trailing `;` inside a function body).
		if !skip && strings.HasSuffix(trim, ")") && !strings.HasSuffix(trim, "))") && !strings.HasPrefix(trim, "(") {
			skip = true
		}
		// Skip the trailing `;` when the next non-empty line is
		// `;;` (case-pattern separator) — bash 5.3 emits bare
		// body lines before `;;` in declare -f output.
		//
		// Also skip when next line is `do` AND the current line
		// ends with `))` — that's the bash 5.3 arith-for header
		// (`for ((init; cond; post))` with no trailing `;`).
		// `for X in Y; do` keeps the `;` after `Y` (matches bash).
		//
		// Also skip when next line is `}` (or `};` etc.) — body
		// lines inside `{ ... }` blocks omit `;` on the last
		// stmt, matching the function-body rule one level deeper.
		if !skip {
			for k := i + 1; k < len(lines); k++ {
				nxt := strings.TrimSpace(lines[k])
				if nxt == "" {
					continue
				}
				switch {
				case nxt == ";;" || strings.HasPrefix(nxt, ";;"):
					skip = true
				case nxt == "do" && strings.HasSuffix(trim, "))"):
					skip = true
				case nxt == "}" || strings.HasPrefix(nxt, "} ") ||
					strings.HasPrefix(nxt, "};"):
					skip = true
				}
				break
			}
		}
		if skip {
			continue
		}
		if !(lastTop && i == len(lines)-1) {
			lines[i] += ";"
		}
	}
	// Insert a blank line after each heredoc terminator (bash 5.3
	// declare -f puts a blank line between the EOF closer and the
	// next stmt or the function close). Skip when the next line
	// is the collapsed ` );` subshell-close (bash 5.3 keeps the
	// terminator and the close adjacent in that case).
	if len(hdocTermAt) > 0 {
		out := make([]string, 0, len(lines)+len(hdocTermAt))
		for i, l := range lines {
			out = append(out, l)
			if hdocTermAt[i] && i+1 < len(lines) {
				nxtTrim := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(nxtTrim, ")") {
					continue
				}
				// bash 5.3 keeps the terminator adjacent to a
				// following `then`/`do` (heredoc fed the if/while
				// condition); the blank only separates statements.
				if nxtTrim == "then" || nxtTrim == "do" {
					continue
				}
				out = append(out, "")
			}
		}
		lines = out
	}
	result := strings.Join(lines, "\n")
	// bash 5.3 collapses `(\n    CMD <<TAG\n...\nTAG\n    )` to
	// `( CMD <<TAG\n...\nTAG\n );` when the subshell body is a
	// single heredoc-bearing statement.
	result = bashCollapseSubshellHdoc(result)
	result = bashCollapseCoprocSubshellHdoc(result)
	return bashFinalizeMultilineArith(result)
}

func bashMultilineArithExpansion(body string) string {
	if !strings.Contains(body, "$(((\\\n") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		idx := strings.Index(line, "$(((\\")
		if idx < 0 {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		exprIndent := indent + strings.Repeat(" ", 16)
		finalIndent := indent + strings.Repeat(" ", 20)
		closeIndent := indent + strings.Repeat(" ", 13)
		lines[i] = line[:idx] + "$((" + line[idx+len("$(((\\"):]
		for j := i + 1; j < len(lines); j++ {
			trim := strings.TrimSpace(lines[j])
			switch {
			case strings.HasSuffix(trim, "| (\\"):
				expr := strings.TrimSuffix(trim, "| (\\")
				lines[j] = exprIndent + "(" + strings.TrimRight(expr, " \t") + " |"
			case strings.HasSuffix(trim, "))"):
				expr := strings.TrimSuffix(trim, "))")
				lines[j] = finalIndent + "(" + strings.TrimRight(expr, " \t")
				lines = append(lines, "")
				copy(lines[j+2:], lines[j+1:])
				lines[j+1] = closeIndent + "))"
				i = j + 1
				goto nextLine
			}
		}
	nextLine:
	}
	return strings.Join(lines, "\n")
}

func bashTightenMultilineArithParens(body string) string {
	if !strings.Contains(body, "$((\n") {
		return body
	}
	lines := strings.Split(body, "\n")
	inArith := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasSuffix(trim, "$((") {
			inArith = true
			continue
		}
		if !inArith {
			continue
		}
		line = strings.ReplaceAll(line, "( ", "(")
		line = strings.ReplaceAll(line, " )", ")")
		line = strings.ReplaceAll(line, ")|", ") |")
		lines[i] = line
		if trim == "))" || strings.HasSuffix(trim, "))") {
			inArith = false
		}
	}
	return strings.Join(lines, "\n")
}

func bashFinalizeMultilineArith(body string) string {
	if !strings.Contains(body, "$((\n") {
		return body
	}
	lines := strings.Split(body, "\n")
	inArith := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasSuffix(trim, "$((") {
			inArith = true
			continue
		}
		if inArith && trim == "))" {
			lines[i] = line + ";"
			inArith = false
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if next == "" {
					continue
				}
				if next == "done" && !strings.HasPrefix(lines[j], " ") {
					lines[j] = "    " + next
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// isPosixSpecialBuiltin reports whether name is a POSIX "special
// builtin" (POSIX 1003.1 § 2.14). In bash's POSIX mode, an assignment
// preceding a special-builtin invocation persists after the command
// returns rather than being reverted.
func isPosixSpecialBuiltin(name string) bool {
	switch name {
	case "break", ":", "continue", ".", "eval", "exec", "exit",
		"export", "readonly", "return", "set", "shift",
		"source", "times", "trap", "unset":
		return true
	}
	return false
}

// invokesSpecialBuiltin reports whether [Runner.call] will dispatch name as a
// POSIX special builtin, rather than as a shell function shadowing that name.
func (r *Runner) invokesSpecialBuiltin(name string) bool {
	if !isPosixSpecialBuiltin(name) || !IsBuiltin(name) {
		return false
	}
	if r.opts[optPosix] {
		return true
	}
	return r.Funcs[name] == nil
}

// posixSpecialBuiltinFatal upgrades certain special-builtin failures
// to a shell exit in POSIX mode (POSIX 1003.1 § 2.8.1). Bash limits
// this to "hard" errors: any unset failure (readonly variable, bad
// identifier), return outside a function, and non-numeric arguments
// to exit/return/shift/break/continue. "Too many arguments" (a valid
// leading number followed by extras) and semantic failures like
// `trap` with a bad signal name stay non-fatal.
func (r *Runner) posixSpecialBuiltinFatal(name string, args []string) {
	if r.exit.code == 0 || r.exit.exiting {
		return
	}
	// POSIX 2.8.1: an error in a special built-in causes a *non-interactive*
	// shell to exit; an interactive shell reports the error and carries on
	// (e.g. `unset` of a readonly variable typed at the prompt must not kill
	// the session). bash matches this.
	if r.interactiveShell {
		return
	}
	badNumericArg := func() bool {
		if len(args) == 0 {
			return false
		}
		arg := args[0]
		if arg == "--" && len(args) > 1 {
			arg = args[1]
		}
		_, err := strconv.ParseInt(arg, 10, 64)
		return err != nil
	}
	switch name {
	case "unset", "set", "export", "readonly":
		// These reach this point only with a non-zero status (the
		// `r.exit.code == 0` guard above filters success), and their
		// sole non-zero exits are usage errors (an unknown `set -o`
		// option, a bad flag) or assignment errors (`export 1bad=2`,
		// `readonly 1bad=2`). Both are "hard" errors that abort a
		// non-interactive POSIX shell. Listing forms (`set`, `export
		// -p`) succeed with status 0 and never get here.
		r.exit.exiting = true
	case "return":
		// "return 42 abcde" fails with "too many arguments", which
		// stays non-fatal even in POSIX mode; "return" outside a
		// function and "return abcde" are fatal.
		if len(args) > 1 && !badNumericArg() {
			return
		}
		if !r.exit.returning || (r.exit.code == 2 && badNumericArg()) {
			r.exit.exiting = true
		}
	case "exit", "shift", "break", "continue":
		if r.exit.code == 2 && badNumericArg() {
			r.exit.exiting = true
		}
	}
}

// bashErrPrefix returns the bash-style `<filename>: line <N>: ` prefix
// when [WithBashCompatErrors] is on; the empty string otherwise. The
// filename comes from the parsed script (set when running a File) or
// falls back to "bashy" for `-c` / stdin / interactive invocations.
func (r *Runner) bashErrPrefix(pos syntax.Pos) string {
	if r.bashPPGoSource && r.bashPPGoSourceFile != nil {
		if source, ok := r.bashPPGoSourceFile.SourceAt(pos); ok {
			return fmt.Sprintf("%s:%d:%d: ", source.Name, pos.Line(), pos.Col())
		}
	}
	return r.bashErrPrefixLine(int(pos.Line()))
}

// bashErrPrefixLine is bashErrPrefix for callers that need to override
// the reported line number rather than take it from an AST position.
func (r *Runner) bashErrPrefixLine(line int) string {
	if !r.bashCompatErrors {
		return ""
	}
	name := r.filename
	if name == "" {
		name = "bash"
	}
	name = diagnosticShellName(name)
	// When executing a multi-stmt alias body the AST positions are
	// from the alias-body parse (line N within the body), not from
	// the call site in the script. r.aliasLineOverride is set by the
	// alias-expansion code in cmd() to make runtime diagnostics
	// (`command not found`, etc.) report the invocation line.
	if r.aliasLineOverride > 0 {
		line = r.aliasLineOverride
	}
	line += r.funsubLineOffset
	line += r.evalLineOffset
	return fmt.Sprintf("%s: line %d: ", name, line)
}

// bashOSError formats an os.PathError (or any error) the way bash 5.3
// formats file-open failures: the syscall reason with its first letter
// capitalised (`No such file or directory`, `Permission denied`, …),
// stripped of Go's `open <path>: ` prefix.
func bashOSError(err error) string {
	msg := pathErrReason(err)
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

func pathErrReason(err error) string {
	if msg, ok := posixErrorText(err); ok {
		return msg
	}
	var pe *os.PathError
	msg := err.Error()
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	return msg
}

func (r *Runner) stop(ctx context.Context) bool {
	// `returning` is a function-scoped flag (set by the `return`
	// builtin); honour it even inside a trap so a function called
	// from a DEBUG/ERR trap can exit early. `exiting` is the
	// script-level exit flag — some traps trigger on exit so we
	// only honour that one outside trap handlers.
	if r.exit.returning {
		return true
	}
	// A Bash++ panic halts everything until a deferred call recovers it or it
	// terminates the shell. It is checked here, rather than left to the
	// `returning` flag alone, because a panic must keep unwinding across the
	// boundaries that consume a `return` — a shell function, a funsub — and
	// the one place every statement passes through is the only place that can
	// promise it. See interp/bashpp_panic.go.
	if r.bashPPPanicHalts() {
		return true
	}
	if !r.handlingTrap && r.exit.exiting {
		return true
	}
	if err := ctx.Err(); err != nil {
		if r.bashPPGoTask {
			r.bashPPTaskCanceled = true
			if r.bashPPConcurrent != nil {
				r.bashPPConcurrent.arm(r.bashPPTaskState)
			}
		}
		r.exit.fatal(err)
		return true
	}
	// noexec is checked before anything is dispatched, so once it is in
	// effect nothing else runs — not even a POSIX special builtin. That
	// makes it one-way: the `set +n` that would turn it back off is
	// itself never executed. Bash distinguishes the two ways in:
	//
	//	                interactive        non-interactive
	//	  cmdline -n    does not execute   does not execute
	//	  set -n        ignored            one-way stop
	//
	// so an interactive shell ignores `set -n` (the option still shows
	// up in `$-` and `set -o`, it just has no effect) but still honours
	// a `-n` given on the command line.
	if r.cmdlineNoExec {
		return true
	}
	if r.opts[optNoExec] && !r.interactiveShell {
		return true
	}
	return false
}

func (r *Runner) canReturn() bool {
	return r.inFunc || r.inSource || (r.inSignalTrap && r.signalTrapDepth > 0)
}

func backgroundJobText(st *syntax.Stmt) string {
	// Bash stores the job's command without the trailing `&`; the jobs
	// list re-adds it only for still-running jobs (see list_one_job /
	// print_pipeline in jobs.c). Render the statement with Background and
	// Disown cleared so the text matches.
	st2 := *st
	st2.Background = false
	st2.Disown = false
	var buf strings.Builder
	if err := syntax.NewPrinter(syntax.SingleLine(true)).Print(&buf, &st2); err != nil {
		return ""
	}
	s := buf.String()
	// Bash deparses a subshell with spaces inside the parens
	// (`( cmd )`), whereas our single-line printer emits `(cmd)`. Match
	// bash for the common top-level subshell job; group commands
	// (`{ cmd; }`) already render with the spaces.
	if _, ok := st2.Cmd.(*syntax.Subshell); ok &&
		strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		s = "( " + inner + " )"
	}
	return s
}

func plainPipelineSubshell(st *syntax.Stmt) (*syntax.Subshell, bool) {
	if st == nil || st.Negated || st.Background || st.Coprocess || st.Disown || len(st.Redirs) > 0 {
		return nil, false
	}
	subshell, ok := st.Cmd.(*syntax.Subshell)
	return subshell, ok
}

func (r *Runner) stmt(ctx context.Context, st *syntax.Stmt) {
	// Run any trap handlers for OS signals that arrived since the last
	// command. Doing this between statements lets an async signal (e.g.
	// from a background `kill -USR1 $$`) interrupt even a tight loop.
	if r.hasPendingSig.Load() {
		r.deliverPendingSignals(ctx)
	}
	if r.stop(ctx) {
		return
	}
	r.expandRunExit = exitStatus{}
	r.exit = exitStatus{}
	if st.Background || st.Disown {
		r2 := r.subshell(true)
		r2.asyncList = true
		r2.inheritedBang = r.lastBangProc()
		r2.ignoreAsyncListSignals()
		var asyncStdin *os.File
		// POSIX 2.11: an asynchronous list's stdin defaults to /dev/null
		// whenever job control is not enabled (bash does this in default
		// mode too, not only in POSIX mode). An explicit redirection on the
		// command still overrides this default below.
		if !r.monitorActive() && !r.asyncStdinExplicit {
			f, err := os.Open(os.DevNull)
			if err == nil {
				asyncStdin = f
				r2.stdin = f
			}
		}
		st2 := *st
		st2.Background = false
		st2.Disown = false
		waitForExternalLaunch := r.jobCarrier != nil && r.isLiteralExternalCallStmt(&st2)
		externalJob := (r.bareExecHandler || r.jobCarrier != nil) && r.isExternalProcessCallStmt(&st2)
		bg := &bgProc{
			done:                       make(chan struct{}),
			exit:                       new(exitStatus),
			pidReady:                   make(chan struct{}),
			detachOnExit:               externalJob,
			finishBeforeFileReturn:     r.isFastOutputBuiltinStmt(&st2),
			publishPidToBang:           isSimpleCallStmt(&st2) || isPipelineStmt(&st2),
			pidCallback:                r.bgPidCallback, // see WithBgPidCallback
			cmd:                        backgroundJobText(st),
			jobControl:                 r.monitorActive(),
			jobID:                      r.nextJobID(),
			carrierPipelineSignalOwner: isPipelineStmt(&st2),
		}
		r2.asyncProc = bg
		// The job runner's signal dispositions represent the job even
		// without a carrier: the kill builtin consults them to honor the
		// SIG_IGN a fork-based shell's async children inherit (POSIX 2.11).
		// attachCarrier stores the same runner; a background pipeline later
		// replaces it with its last component via publishBgSignalRunner.
		bg.carrierRootSubshell, _ = st2.Cmd.(*syntax.Subshell)
		bg.carrierSignalRunner.Store(r2)
		bgCtx, cancel := r.backgroundContext(ctx)
		bg.cancel = cancel
		// With a JobCarrier configured, give the job a real kernel PID
		// as its identity before it starts running; see WithJobCarrier.
		// The seam fails closed: a host that opted in must never see a
		// synthetic g<N> identity, so a carrier that cannot be started
		// means the job is not created at all — the statement fails like
		// a shell whose fork failed, and `$!` keeps its previous value.
		if err := r.attachCarrier(bgCtx, r2, bg); err != nil {
			cancel()
			if asyncStdin != nil {
				_ = asyncStdin.Close()
			}
			r2.closeDirFile()
			r.errf("%s%v\n", r.bashErrPrefix(st.Pos()), err)
			r.exit = exitStatus{code: 1}
		} else {
			r.bgProcs = append(r.bgProcs, bg)
			r.lastBang = bg
			if externalJob {
				bg.launchYield = make(chan struct{})
			}
			// Stash a pointer to the freshly-appended bgProc on the
			// goroutine's ctx so the exec handlers (DefaultExecHandler,
			// runDetachedExec) can publish the real OS PID into it via
			// publishBgPid. `$!` reads that PID back via bgProc.pidReady.
			bgCtx = context.WithValue(bgCtx, bgProcCtxKey{}, bg)
			if !bg.publishPidToBang {
				select {
				case <-bg.pidReady:
				default:
					close(bg.pidReady)
				}
			}
			// launched marks the point where the background goroutine has
			// been scheduled and begun running — the Go analogue of fork()
			// returning in the parent. It closes unconditionally before any
			// work that could block (redirection setup can open a FIFO and
			// wait for a peer, e.g. `cat <fifo &`). waitForExternalLaunch
			// below must rendezvous on this, not on bg.pidReady: pidReady
			// only closes once the real exec has happened, which can be
			// arbitrarily delayed by that same blocking redirection setup,
			// and gating the parent on it can deadlock when the parent's own
			// next statement is what would unblock the redirection (e.g. the
			// writer side of that FIFO).
			launched := make(chan struct{})
			go func() {
				close(launched)
				defer r2.closeDirFile()
				if asyncStdin != nil {
					defer asyncStdin.Close()
				}
				cleaned := false
				cleanup := func() {
					if cleaned {
						return
					}
					cleaned = true
					// A background runner is a terminal subshell: unlike a
					// caller-owned Runner, it cannot be Reset. Join its trap
					// forwarders before publishing completion through bg.done.
					r2.stopSignalSubscriptions()
					cancel()
					// Ensure pidReady is closed even if no real exec ever
					// happened (e.g. `(true) &`). The reader of `$!` waits
					// on this channel — leaving it open would hang forever.
					select {
					case <-bg.pidReady:
					default:
						close(bg.pidReady)
					}
				}
				// Preserve cleanup if execution or an EXIT trap panics before the
				// normal completion path reaches bg.done.
				defer cleanup()
				r2.Run(bgCtx, &st2)
				if cb := r2.trapCallbacks["EXIT"]; cb != "" && !r2.inheritedExitTrap {
					// In POSIX mode Bash runs an asynchronous trap
					// command's EXIT handler before its stdout redirect
					// is restored. The runner's Run boundary has already
					// restored it, so retain a /dev/null redirect here.
					if r2.opts[optPosix] {
						if call, ok := st2.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 &&
							call.Args[0].Lit() == "trap" {
							for _, rd := range st2.Redirs {
								if rd.Op == syntax.RdrOut && rd.Word != nil &&
									rd.Word.Lit() == os.DevNull &&
									(rd.N == nil || rd.N.Value == "1") {
									r2.stdout = io.Discard
								}
							}
						}
					}
					r2.trapCallback(bgCtx, cb, "exit")
				}
				r2.exit.exiting = false // subshells don't exit the parent shell
				r2.exit.discarding = false
				// Reap the job's carrier process (if any) before sealing the
				// exit status below, so a kill of the carrier racing with
				// natural completion still lands as 128+signal.
				bg.reapCarrier()
				if result := bg.execResult.Load(); result > 0 {
					r2.exit = exitStatus{code: uint8(result - 1)}
				} else if n := bg.killedSignal.Load(); n > 0 {
					r2.exit = exitStatus{code: uint8(128 + n)}
				}
				*bg.exit = r2.exit
				// One reaped child -> one SIGCHLD trap run (bash waitchld).
				// Queue before signalling done so a `wait` that unblocks here
				// sees the pending CHLD at its next statement boundary.
				if !bg.detached.Load() {
					r.notifyChildReaped()
				}
				cleanup()
				close(bg.done)
			}()
			// A real shell forks an asynchronous simple command before it
			// resumes the parent list. The Go runner must preserve that launch
			// boundary: otherwise the parent can run its next statements before
			// the background goroutine has even started. Rendezvous on
			// `launched`, not bg.pidReady — see its comment above for why.
			// Keep this narrow to literal external calls under the CLI's job
			// carrier; builtins, functions, and long-running jobs remain async.
			//
			// The boundary ends here. It orders the fork, never the child's
			// work: bash makes no part of an asynchronous command's effect
			// visible to the next statement, and a sleep that tried to buy the
			// child a head start would be both a conformance deviation and a
			// race. `$!` still names the exec'd child because it waits on
			// bg.pidReady, which is a real rendezvous rather than a delay.
			if waitForExternalLaunch {
				select {
				case <-launched:
				case <-ctx.Done():
				}
			}
			// An external command is more than scheduled when bash resumes
			// the parent: fork has returned, so the child exists and outlives
			// a shell that exits at once (`bash -c 'cmd &'`). A goroutine that
			// has merely begun dies with the process, before it ever starts
			// the command. Wait for the start itself — bg.pidReady, closed by
			// the exec handler's publishBgPid or by the job ending without an
			// exec. This is a rendezvous on the start, never on the command's
			// work. The FIFO deadlock described at `launched` cannot arise:
			// the job closes launchYield before any pre-exec step that has no
			// time bound (see yieldAsyncLaunch), handing the parent back.
			if bg.launchYield != nil {
				// Exec middleware may serve a command itself and never
				// publish a PID; the default handler always ends the wait
				// with an event, so only a middleware chain needs the bound.
				var bound <-chan time.Time
				var timer *time.Timer
				if !r.bareExecHandler {
					timer = time.NewTimer(asyncLaunchBound)
					bound = timer.C
				}
				select {
				case <-bg.pidReady:
				case <-bg.launchYield:
				case <-bound:
				case <-ctx.Done():
				}
				if timer != nil {
					timer.Stop()
				}
			}
		}
	} else {
		r.stmtSync(ctx, st)
		r.applyPipelineWriteFailure()
		// A synchronous self-directed `kill -SIG $$` marks its signal
		// pending while this command runs; deliver it now, before the
		// enclosing function returns, so a `return` in the handler unwinds
		// the right frame (bash interp 1602).
		if r.hasPendingSig.Load() {
			r.deliverPendingSignals(ctx)
		}
	}
	r.lastExit = r.exit
	if stmtUpdatesPipeStatus(st) {
		r.pipeStatus = []string{strconv.Itoa(int(r.exit.code))}
	}
}

func (r *Runner) lastBangProc() *bgProc {
	if r.lastBang != nil {
		return r.lastBang
	}
	// Keep manually assembled runners and older internal fixtures working;
	// normal execution assigns lastBang when it appends the job.
	if n := len(r.bgProcs); n > 0 {
		return r.bgProcs[n-1]
	}
	return r.inheritedBang
}

func (r *Runner) hasLiveBackgroundJobs() bool {
	for _, bg := range r.bgProcs {
		if bg == nil {
			continue
		}
		select {
		case <-bg.done:
			continue
		default:
			return true
		}
	}
	return false
}

// finishBackgroundOutputBuiltins prevents a standalone shell process from
// exiting before a fast, builtin-only background write has reached its sink.
// This is deliberately narrow: external jobs and potentially blocking
// builtins retain asynchronous return behavior.
func (r *Runner) finishBackgroundOutputBuiltins(ctx context.Context) {
	for _, bg := range r.bgProcs {
		if bg == nil || !bg.finishBeforeFileReturn {
			continue
		}
		select {
		case <-bg.done:
		case <-ctx.Done():
			return
		}
	}
}

func (r *Runner) isFastOutputBuiltinStmt(st *syntax.Stmt) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Args[0].Parts) != 1 {
		return false
	}
	lit, ok := call.Args[0].Parts[0].(*syntax.Lit)
	if !ok {
		return false
	}
	if r.disabledBuiltins[lit.Value] || r.Funcs[lit.Value] != nil {
		return false
	}
	return lit.Value == "echo" || lit.Value == "printf"
}

func (r *Runner) isLiteralExternalCallStmt(st *syntax.Stmt) bool {
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Args[0].Parts) != 1 {
		return false
	}
	lit, ok := call.Args[0].Parts[0].(*syntax.Lit)
	if !ok || r.Funcs[lit.Value] != nil {
		return false
	}
	return !IsBuiltin(lit.Value) || r.disabledBuiltins[lit.Value]
}

// isExternalProcessCallStmt narrows isLiteralExternalCallStmt to commands the
// exec handlers start as an operating-system process. A name the embedder
// serves in process, or resolves through its own command rung, runs inside
// this shell: it has no launch to wait for and cannot outlive the shell.
func (r *Runner) isExternalProcessCallStmt(st *syntax.Stmt) bool {
	if !r.isLiteralExternalCallStmt(st) {
		return false
	}
	name := st.Cmd.(*syntax.CallExpr).Args[0].Lit()
	if strings.ContainsRune(name, '/') {
		return true
	}
	if r.servedInProcess != nil && r.servedInProcess(name) {
		return false
	}
	if r.commandResolver != nil {
		if _, ok := r.commandResolver(name); ok {
			return false
		}
	}
	return true
}

// asyncLaunchBound caps the `&` launch rendezvous under exec middleware that
// runs a command without publishing its PID. It is not a head start: a started
// process ends the wait at once, however long the command then runs.
const asyncLaunchBound = time.Second

// yieldAsyncLaunch hands control back to a parent waiting at `&` for this
// asynchronous command to start. The job calls it before pre-exec work that
// has no time bound — a command or process substitution, an open that can
// wait for a peer — which a forked child would do on its own time, and which
// the parent's next statement may be what completes (`cat <fifo & echo >fifo`).
func (r *Runner) yieldAsyncLaunch() {
	if bg := r.asyncProc; bg != nil && bg.launchYield != nil {
		bg.launchYieldOnce.Do(func() { close(bg.launchYield) })
	}
}

// openMayBlock reports whether opening path can wait indefinitely: a FIFO or
// device waiting for its peer, or a /dev/tcp connection. A regular file, a
// directory and a path that does not exist yet all open or fail promptly.
func (r *Runner) openMayBlock(ctx context.Context, path string) bool {
	if path == os.DevNull {
		return false
	}
	if _, _, network := devNetworkRedirect(path); network {
		return true
	}
	info, err := r.stat(ctx, path)
	if err != nil {
		return false
	}
	return !info.Mode().IsRegular() && !info.IsDir()
}

func isSimpleCallStmt(st *syntax.Stmt) bool {
	if st == nil {
		return false
	}
	_, ok := st.Cmd.(*syntax.CallExpr)
	return ok
}

func isPipelineStmt(st *syntax.Stmt) bool {
	if st == nil {
		return false
	}
	bin, ok := st.Cmd.(*syntax.BinaryCmd)
	return ok && (bin.Op == syntax.Pipe || bin.Op == syntax.PipeAll)
}

// stmtUpdatesPipeStatus reports whether finishing st should overwrite
// PIPESTATUS with the single status of st. Bash sets PIPESTATUS for simple
// commands, subshells, and "simple-like" compounds ([[ ]], (( )), declare,
// …), but leaves it untouched for command-list compounds (if/for/while/
// until/case/{ }) and for &&/|| lists: those delegate to whichever inner
// command actually ran last (so `if false; then …; fi` reports PIPESTATUS
// of the failed condition, and a no-match `case` leaves it empty). Real
// pipelines set the multi-element array in cmd(), so they're excluded too.
func stmtUpdatesPipeStatus(st *syntax.Stmt) bool {
	if st == nil {
		return false
	}
	switch st.Cmd.(type) {
	case *syntax.BinaryCmd, // pipelines set it in cmd(); &&/|| delegate
		*syntax.Block, *syntax.IfClause, *syntax.ForClause,
		*syntax.WhileClause, *syntax.CaseClause:
		return false
	}
	return true
}

// stmtSync runs one statement. A Go-form statement that carries no
// redirection takes [Runner.stmtSyncGoForm], whose host frame holds none of
// the redirection bookkeeping: a recursive Go-form call re-enters stmtSync
// once per interpreted level.
func (r *Runner) stmtSync(ctx context.Context, st *syntax.Stmt) {
	if len(st.Redirs) == 0 && st.Cmd != nil && bashPPGoFormCommand(st.Cmd) {
		r.stmtSyncGoForm(ctx, st)
		return
	}
	r.stmtSyncGeneral(ctx, st)
}

// stmtSyncGoForm is [Runner.stmtSyncGeneral] for a statement with a command
// and no redirections: the same redirection scope, saved standard streams and
// status handling, without the descriptor-table snapshots only a redirection
// can need.
func (r *Runner) stmtSyncGoForm(ctx context.Context, st *syntax.Stmt) {
	r.exit.noNegate = false

	scopeIndex := len(r.redirScopes)
	r.redirScopes = append(r.redirScopes, redirScope{closeAt: -1})
	r.curStmtPos = st.Pos()
	r.curStmtEnd = st.End()
	r.coprocReapedFds = nil

	saved := stmtStreams{
		stdin: r.stdin, stdinTTYFallback: r.stdinTTYFallback, stdinDevTTY: r.stdinDevTTY,
		stdout: r.stdout, stderr: r.stderr,
		stdinRedirected: r.stdinRedirected, stdinClosed: r.stdinClosed,
	}
	defer r.stmtSyncGoFormLeave(scopeIndex, r.asyncStdinExplicit, r.redirMoveCloseFds)
	if r.bashPPHasFIFOs() {
		// Only a statement that can see a FIFO retains its streams past its
		// own frame, so only that one pays for a heap copy of them.
		retained := saved
		r.redirScopes[scopeIndex].fifoRestoreRefs = retained.fifoRefs
	}
	r.redirMoveCloseFds = nil

	r.stmtRunCmd(ctx, st)
	if !r.redirScopes[scopeIndex].persist {
		r.stdin, r.stdout, r.stderr = saved.stdin, saved.stdout, saved.stderr
		r.stdinTTYFallback = saved.stdinTTYFallback
		r.stdinDevTTY = saved.stdinDevTTY
		r.stdinRedirected = saved.stdinRedirected
		r.stdinClosed = saved.stdinClosed
	}
}

// stmtStreams is the standard-stream state a statement restores when it ends.
type stmtStreams struct {
	stdin            *os.File
	stdinTTYFallback bool
	stdinDevTTY      bool
	stdout, stderr   io.Writer
	stdinRedirected  bool
	stdinClosed      bool
}

// fifoRefs retains the FIFO endpoints among the streams the statement will
// restore; see [redirScope].
func (s *stmtStreams) fifoRefs(refs map[*os.File]bool) {
	bashPPFIFORef(refs, s.stdin)
	bashPPFIFORef(refs, s.stdout)
	bashPPFIFORef(refs, s.stderr)
}

// stmtSyncGoFormLeave undoes what [Runner.stmtSyncGoForm] set up, in the
// order [Runner.stmtSyncGeneral]'s deferred calls run.
func (r *Runner) stmtSyncGoFormLeave(scopeIndex int, asyncStdinExplicit bool, redirMoveCloseFds map[int]bool) {
	r.redirMoveCloseFds = redirMoveCloseFds
	r.asyncStdinExplicit = asyncStdinExplicit
	for _, closer := range r.redirScopes[scopeIndex].boundaryClosers {
		closer.Close()
	}
	r.redirScopes = r.redirScopes[:scopeIndex]
	if r.bashPPHasFIFOs() {
		r.bashPPReconcileFIFOs()
	}
}

// stmtRunCmd runs a statement's command and settles its status: negation,
// the ERR trap and errexit.
func (r *Runner) stmtRunCmd(ctx context.Context, st *syntax.Stmt) {
	if r.exit.ok() && st.Cmd != nil {
		// A negated stmt suppresses `set -e`-driven exit for the
		// command it wraps — bash treats `! cmd` like `cmd || true`
		// for errexit purposes.
		if st.Negated {
			oldNoErrExit := r.noErrExit
			r.noErrExit = true
			r.cmd(ctx, st.Cmd)
			r.noErrExit = oldNoErrExit
			r.exit.noNegate = r.exit.exiting || r.exit.returning || r.exit.fatalExit
			// Clear any pending exit propagated by inner stmts
			// under errexit; the outer `!` will set the final
			// success/failure below.
			r.exit.exiting = false
			r.exit.discarding = false
		} else {
			r.cmd(ctx, st.Cmd)
		}
	}
	if st.Negated && !r.exit.exiting && !r.exit.returning && !r.exit.fatalExit && !r.exit.noNegate {
		if r.exit.ok() {
			r.exit.code = 1
		} else {
			r.exit.clear()
		}
	} else if errExitExemptByAndOr(st.Cmd) || r.exit.errexitExempt {
	} else if !r.exit.ok() && !r.noErrExit {
		if !r.inFunc {
			prevLineno := r.ecfg.OverrideLineno
			r.ecfg.OverrideLineno = int(st.Pos().Line())
			r.trapCallback(ctx, r.trapCallbacks["ERR"], "error")
			r.ecfg.OverrideLineno = prevLineno
		}
		// If the "errexit" option is set and a command failed, exit the shell. Exceptions:
		//
		//   conditions (if <cond>, while <cond>, etc)
		//   part of && or || lists; excluded via "else" above
		//   preceded by !; excluded via "else" above
		if r.opts[optErrExit] {
			r.exit.exiting = true
		}
	}
}

func (r *Runner) stmtSyncGeneral(ctx context.Context, st *syntax.Stmt) {
	r.exit.noNegate = false

	scopeIndex := len(r.redirScopes)
	r.redirScopes = append(r.redirScopes, redirScope{
		hasRedirs: len(st.Redirs) > 0,
		closeAt:   -1,
	})
	// Registered first so it fires LAST (LIFO) — file-close defers below
	// still see whether exec made this scope persistent.
	defer func() {
		r.redirScopes = r.redirScopes[:scopeIndex]
		if r.bashPPHasFIFOs() {
			r.bashPPReconcileFIFOs()
		}
	}()
	defer func() {
		for _, closer := range r.redirScopes[scopeIndex].boundaryClosers {
			closer.Close()
		}
	}()
	persistNamedRedirs := false
	keepRedirs := func() bool { return r.redirScopes[scopeIndex].persist }
	closeOrTransfer := func(closer io.Closer) {
		if persistNamedRedirs {
			return
		}
		if !keepRedirs() {
			closer.Close()
			return
		}
		if boundary := r.redirScopes[scopeIndex].closeAt; boundary >= 0 {
			r.redirScopes[boundary].boundaryClosers = append(
				r.redirScopes[boundary].boundaryClosers, closer)
		}
	}

	r.curStmtPos = st.Pos()
	r.curStmtEnd = st.End()

	// Track coproc fds reaped within this statement so the fdTable restore
	// below doesn't resurrect a closed coprocess's descriptors.
	r.coprocReapedFds = nil

	oldIn, oldStdinTTYFallback, oldStdinDevTTY, oldOut, oldErr := r.stdin, r.stdinTTYFallback, r.stdinDevTTY, r.stdout, r.stderr
	oldStdinRedirected := r.stdinRedirected
	oldAsyncStdinExplicit := r.asyncStdinExplicit
	defer func() { r.asyncStdinExplicit = oldAsyncStdinExplicit }()
	oldStdinClosed := r.stdinClosed
	// Snapshot fdTable only when this statement has redirects that
	// might mutate it. A coproc statement registers fds in fdTable from
	// inside cmd() itself, not via redir(), and those changes must
	// persist past stmtSync; restoring unconditionally would wipe them.
	var oldFdTable map[int]*os.File
	var oldFdReadTable map[int]bool
	var oldFdWriteTable map[int]io.Writer
	var oldFdClosedTable map[int]bool
	var modifiedFds []int
	redirsPrepared, lateRedir := false, false
	if len(st.Redirs) > 0 {
		oldFdTable = maps.Clone(r.fdTable)
		oldFdReadTable = maps.Clone(r.fdReadTable)
		oldFdWriteTable = maps.Clone(r.fdWriteTable)
		oldFdClosedTable = maps.Clone(r.fdClosedTable)
	}
	if len(st.Redirs) > 0 {
		varredirClose := false
		if opt, _ := r.bashOptByName("varredir_close"); opt != nil {
			varredirClose = *opt
		}
		if !varredirClose {
			for _, rd := range st.Redirs {
				if isNamedFdRedir(rd) {
					persistNamedRedirs = true
					break
				}
			}
		}
	}
	oldRedirMoveCloseFds := r.redirMoveCloseFds
	// A FIFO registered later is a fresh descriptor, never one of the
	// files this scope restores, so a scope that begins with none
	// registered has nothing to retain.
	if r.bashPPHasFIFOs() {
		// Retain exactly what this scope restores. An unrelated outer
		// redirect must not delay an inner exec's persistent close.
		r.redirScopes[scopeIndex].fifoRestoreRefs = func(refs map[*os.File]bool) {
			bashPPFIFORef(refs, oldIn)
			bashPPFIFORef(refs, oldOut)
			bashPPFIFORef(refs, oldErr)
			if persistNamedRedirs {
				return
			}
			if !redirsPrepared || lateRedir {
				for _, f := range oldFdTable {
					bashPPFIFORef(refs, f)
				}
				for _, w := range oldFdWriteTable {
					bashPPFIFORef(refs, w)
				}
			} else {
				for _, fd := range modifiedFds {
					bashPPFIFORef(refs, oldFdTable[fd])
					bashPPFIFORef(refs, oldFdWriteTable[fd])
				}
			}
		}
	}
	r.redirMoveCloseFds = nil
	defer func() { r.redirMoveCloseFds = oldRedirMoveCloseFds }()
	// bash 5.3 caps the number of here-documents per simple command
	// (the historical compile-time limit, 16). Beyond that bash
	// rejects the entire command with "maximum here-document count
	// exceeded" before executing it.
	const maxHeredocs = 16
	hdocCount := 0
	for _, rd := range st.Redirs {
		if rd.Op == syntax.Hdoc || rd.Op == syntax.DashHdoc {
			hdocCount++
		}
	}
	if hdocCount > maxHeredocs {
		r.errf("%smaximum here-document count exceeded\n", r.bashErrPrefix(st.Pos()))
		r.exit.code = 1
		// bash treats the limit as a fatal parser error and
		// stops executing the rest of the script.
		r.exit.exiting = true
		return
	}
	if st.Cmd == nil && len(st.Redirs) > 0 {
		r2 := r.subshell(false)
		defer r2.closeDirFile()
		if r2.bashPPConcurrent != nil {
			defer r2.bashPPConcurrent.closeFIFOs(r2)
		}
		r2.lastExpandExit = exitStatus{}
		var closers []io.Closer
		for _, rd := range st.Redirs {
			cls, err := r2.redir(ctx, rd)
			if err != nil {
				r2.exit.code = 1
				break
			}
			if cls != nil {
				closers = append(closers, cls)
			}
		}
		for _, c := range closers {
			c.Close()
		}
		r.exit = r2.exit
		// POSIX 2.9.1: a command with only redirections and no command
		// word completes with the exit status of the last command
		// substitution performed while expanding the redirect operands
		// (`>/dev/null$(exit 17)` exits 17). If the redirects themselves
		// succeeded and ran no command substitution, lastExpandExit is
		// zero, leaving the success status untouched.
		if r.exit.ok() {
			r.exit = r2.lastExpandExit
		}
		// A redirection error on a command with no command word still
		// fails the command; honor the ERR trap and errexit the same way
		// a normal command failure would, unless we're in an
		// errexit-exempt context (condition, left of &&/||, negated).
		if !r.exit.ok() && !r.noErrExit && !st.Negated {
			if !r.inFunc {
				prevLineno := r.ecfg.OverrideLineno
				r.ecfg.OverrideLineno = int(st.Pos().Line())
				r.trapCallback(ctx, r.trapCallbacks["ERR"], "error")
				r.ecfg.OverrideLineno = prevLineno
			}
			if r.opts[optErrExit] {
				r.exit.exiting = true
			}
		}
		return
	}
	oldCurStmtPos := r.curStmtPos
	// bash quirk, encoded empirically: a redirection failure on a
	// while/for compound INSIDE a subshell is reported one line past
	// the statement's start (see redir12.sub:43 → bash says line 44).
	// Removing this block flips the redir fixture from PASS to FAIL
	// while buying nothing elsewhere — it was removed once during
	// errors-fixture work and had to be restored. Don't.
	if r.subshellLevel > 0 {
		switch st.Cmd.(type) {
		case *syntax.WhileClause, *syntax.ForClause:
			pos := st.Pos()
			r.curStmtPos = syntax.NewPos(pos.Offset(), pos.Line()+1, pos.Col())
		}
	}
	// POSIX order is: expand the command words, THEN perform redirections.
	// For a simple command whose arguments contain a command substitution,
	// defer the redirections until cmd() has expanded the fields — otherwise
	// `echo $(cat f) > f` would truncate f before $(cat f) reads it. Only the
	// command types that expand words/assignment RHS before redirecting and
	// then call applyLateRedirs (CallExpr, DeclClause) opt in; every other
	// command applies redirs up-front. This matters for commands whose own
	// body runs a command substitution under the redirect, e.g.
	// `(( a = $(cmd) )) 2>err`: bash captures cmd's stderr into err, so the
	// redirect must be active before the arithmetic evaluates — deferring it
	// to a late path that ArithmCmd never drains would silently drop it.
	if len(st.Redirs) > 0 && commandHasCmdSubst(st.Cmd) {
		switch st.Cmd.(type) {
		case *syntax.CallExpr, *syntax.DeclClause:
			lateRedir = true
		}
	}
	if lateRedir {
		r.lateRedirs = st.Redirs
		r.lateRedirClosers = nil
		defer func() {
			for _, c := range r.lateRedirClosers {
				closeOrTransfer(c)
			}
			r.lateRedirClosers = nil
			r.lateRedirs = nil
		}()
	} else {
		oldStmtTraceOutput := r.stmtTraceOutput
		if _, ok := st.Cmd.(*syntax.CallExpr); ok && len(st.Redirs) > 0 {
			r.stmtTraceOutput = r.xtraceOutput()
			defer func() { r.stmtTraceOutput = oldStmtTraceOutput }()
		}
		for _, rd := range st.Redirs {
			cls, err := r.redir(ctx, rd)
			if err != nil {
				r.exit.code = 1
				// POSIX mode: a redirection error on a special builtin
				// (`exec 9<nosuchfile`, …) exits a non-interactive shell,
				// even on the left side of || or &&.
				if r.opts[optPosix] && !r.interactiveShell {
					// A redirection error on a special built-in is fatal to a
					// non-interactive POSIX shell (interactive is spared).
					// readonly/export/… parse as DeclClause, not CallExpr, so
					// check both shapes for the command name.
					var cmdName string
					switch c := st.Cmd.(type) {
					case *syntax.CallExpr:
						if len(c.Args) > 0 {
							cmdName = c.Args[0].Lit()
						}
					case *syntax.DeclClause:
						if c.Variant != nil {
							cmdName = c.Variant.Value
						}
					}
					if isPosixSpecialBuiltin(cmdName) {
						r.exit.exiting = true
					}
				}
				break
			}
			if cls != nil {
				// Skip the close when exec persisted this redirection scope. The opened
				// file is now owned by fdTable / stdio and must outlive
				// this stmtSync call. Read the scope at defer time, not
				// here, because exec sets it during cmd execution.
				defer func(c io.Closer) {
					closeOrTransfer(c)
				}(cls)
			}
		}
		if len(st.Redirs) > 0 && !lateRedir {
			// Find modified fds in fdTable
			for fd, file := range r.fdTable {
				oldFile, existed := oldFdTable[fd]
				if !existed || oldFile != file {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			for fd := range oldFdTable {
				if _, exists := r.fdTable[fd]; !exists {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			// Find modified fds in fdReadTable
			for fd, read := range r.fdReadTable {
				oldRead, existed := oldFdReadTable[fd]
				if !existed || oldRead != read {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			for fd := range oldFdReadTable {
				if _, exists := r.fdReadTable[fd]; !exists {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			// Find modified fds in fdWriteTable
			for fd, write := range r.fdWriteTable {
				oldWrite, existed := oldFdWriteTable[fd]
				if !existed || oldWrite != write {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			for fd := range oldFdWriteTable {
				if _, exists := r.fdWriteTable[fd]; !exists {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			// Find modified fds in fdClosedTable
			for fd, closed := range r.fdClosedTable {
				oldClosed, existed := oldFdClosedTable[fd]
				if !existed || oldClosed != closed {
					modifiedFds = append(modifiedFds, fd)
				}
			}
			for fd := range oldFdClosedTable {
				if _, exists := r.fdClosedTable[fd]; !exists {
					modifiedFds = append(modifiedFds, fd)
				}
			}

			if len(modifiedFds) > 0 {
				uniqueFds := make(map[int]bool)
				var dedup []int
				for _, fd := range modifiedFds {
					if !uniqueFds[fd] {
						uniqueFds[fd] = true
						dedup = append(dedup, fd)
					}
				}
				modifiedFds = dedup
			}
		}
	}
	redirsPrepared = true
	r.curStmtPos = oldCurStmtPos
	r.stmtRunCmd(ctx, st)
	if !keepRedirs() {
		r.stdin, r.stdout, r.stderr = oldIn, oldOut, oldErr
		r.stdinTTYFallback = oldStdinTTYFallback
		r.stdinDevTTY = oldStdinDevTTY
		r.stdinRedirected = oldStdinRedirected
		r.stdinClosed = oldStdinClosed
		if len(st.Redirs) > 0 && !persistNamedRedirs {
			if lateRedir {
				r.fdTable = oldFdTable
				r.fdReadTable = oldFdReadTable
				r.fdWriteTable = oldFdWriteTable
				r.fdClosedTable = oldFdClosedTable
			} else {
				for _, fd := range modifiedFds {
					// Restore fdTable
					if oldFile, existed := oldFdTable[fd]; existed {
						if r.fdTable == nil {
							r.fdTable = make(map[int]*os.File)
						}
						r.fdTable[fd] = oldFile
					} else if r.fdTable != nil {
						delete(r.fdTable, fd)
					}

					// Restore fdReadTable
					if oldRead, existed := oldFdReadTable[fd]; existed {
						if r.fdReadTable == nil {
							r.fdReadTable = make(map[int]bool)
						}
						r.fdReadTable[fd] = oldRead
					} else if r.fdReadTable != nil {
						delete(r.fdReadTable, fd)
					}

					// Restore fdWriteTable
					if oldWrite, existed := oldFdWriteTable[fd]; existed {
						if r.fdWriteTable == nil {
							r.fdWriteTable = make(map[int]io.Writer)
						}
						r.fdWriteTable[fd] = oldWrite
					} else if r.fdWriteTable != nil {
						delete(r.fdWriteTable, fd)
					}

					// Restore fdClosedTable
					if oldClosed, existed := oldFdClosedTable[fd]; existed {
						if r.fdClosedTable == nil {
							r.fdClosedTable = make(map[int]bool)
						}
						r.fdClosedTable[fd] = oldClosed
					} else if r.fdClosedTable != nil {
						delete(r.fdClosedTable, fd)
					}
				}
			}
			// A coprocess reaped during this statement (e.g.
			// `wait $COPROC_PID >file`) had its pipe fds closed; keep them
			// gone so a later coproc reuses the freed high fd numbers.
			for fd := range r.coprocReapedFds {
				delete(r.fdTable, fd)
				delete(r.fdReadTable, fd)
				delete(r.fdWriteTable, fd)
				delete(r.fdClosedTable, fd)
			}
			for fd := range r.redirMoveCloseFds {
				delete(r.fdTable, fd)
				delete(r.fdReadTable, fd)
				delete(r.fdWriteTable, fd)
				if r.inheritedFds[fd] {
					if r.fdClosedTable == nil {
						r.fdClosedTable = make(map[int]bool)
					}
					r.fdClosedTable[fd] = true
				} else {
					delete(r.fdClosedTable, fd)
				}
			}
		}
	}
}

type redirScope struct {
	hasRedirs       bool
	persist         bool
	closeAt         int
	boundaryClosers []io.Closer
	fifoRestoreRefs func(map[*os.File]bool)
}

// persistCurrentRedirs marks the exec statement itself and propagates that
// persistence through enclosing statements which have no redirects of their
// own. The first enclosing redirected command is a boundary: bash restores
// that command's pre-redirection descriptors even when its function body runs
// exec with another redirect.
func (r *Runner) persistCurrentRedirs() {
	last := len(r.redirScopes) - 1
	if last < 0 {
		return
	}
	r.redirScopes[last].persist = true
	for i := last - 1; i >= 0; i-- {
		if r.redirScopes[i].hasRedirs {
			r.redirScopes[last].closeAt = i
			break
		}
		r.redirScopes[i].persist = true
	}
	if r.bashPPHasFIFOs() {
		r.bashPPReconcileFIFOs()
	}
}

func (r *Runner) checkFuncDeclRedirs(ctx context.Context, body *syntax.Stmt) bool {
	if body == nil || len(body.Redirs) == 0 {
		return true
	}
	oldIn, oldOut, oldErr := r.stdin, r.stdout, r.stderr
	oldStdinTTYFallback := r.stdinTTYFallback
	oldStdinDevTTY := r.stdinDevTTY
	oldStdinRedirected := r.stdinRedirected
	oldStdinClosed := r.stdinClosed
	oldFdTable := maps.Clone(r.fdTable)
	oldFdReadTable := maps.Clone(r.fdReadTable)
	oldFdWriteTable := maps.Clone(r.fdWriteTable)
	oldFdClosedTable := maps.Clone(r.fdClosedTable)
	var closers []io.Closer
	defer func() {
		for _, cls := range closers {
			cls.Close()
		}
		r.stdin, r.stdout, r.stderr = oldIn, oldOut, oldErr
		r.stdinTTYFallback = oldStdinTTYFallback
		r.stdinDevTTY = oldStdinDevTTY
		r.stdinRedirected = oldStdinRedirected
		r.stdinClosed = oldStdinClosed
		r.fdTable = oldFdTable
		r.fdReadTable = oldFdReadTable
		r.fdWriteTable = oldFdWriteTable
		r.fdClosedTable = oldFdClosedTable
	}()
	for _, rd := range body.Redirs {
		cls, err := r.redir(ctx, rd)
		if err != nil {
			r.exit.code = 1
			return false
		}
		if cls != nil {
			closers = append(closers, cls)
		}
	}
	return true
}

// cmd runs one command. Go-form statements and brace groups take a small
// host frame of their own instead of sharing [Runner.cmdGeneral]'s: a recursive Go-form call
// re-enters cmd once per interpreted level, and the shell command cases'
// locals would otherwise be reserved on the host stack at every level. An
// xtrace run keeps the general path so its prefix is expanded exactly as
// before.
func (r *Runner) cmd(ctx context.Context, cm syntax.Command) {
	block, isBlock := cm.(*syntax.Block)
	if r.opts[optXTrace] || !(isBlock || bashPPGoFormCommand(cm)) {
		r.cmdGeneral(ctx, cm)
		return
	}
	if r.stop(ctx) {
		return
	}
	if r.bashPPGoTask {
		defer r.cmdGoTaskSettled()
	}
	if isBlock {
		r.cmdBlock(ctx, block)
		return
	}
	r.cmdGoForm(ctx, cm)
}

// cmdBlock runs a brace group.
func (r *Runner) cmdBlock(ctx context.Context, cm *syntax.Block) {
	// `{ ...; }` is a Go block in the bash++ dialect: a `var` declared
	// inside it is gone at the closing brace. Shell assignments in the
	// same braces keep bash's lifetime, which is why only the typed
	// scope is pushed here. An empty block declares nothing, so it needs
	// no scope of its own.
	if r.bashPPScope != nil && len(cm.Stmts) > 0 {
		defer r.bashPPPushScope()()
	}
	r.stmts(ctx, cm.Stmts)
}

// cmdGoTaskSettled records a Go task's command outcome once the command has
// run; see the launch-handshake notes inside.
func (r *Runner) cmdGoTaskSettled() {
	canceled := r.bashPPTaskCanceled || errors.Is(r.exit.err, context.Canceled) || errors.Is(r.exit.err, context.DeadlineExceeded)
	if !canceled {
		r.bashPPTaskFailed = r.exit.code != 0
		r.bashPPTaskFailCode = r.exit.code
	}
	// A failing command is not a safe launch-handshake point: if it is
	// the function's terminal command, task completion must record and
	// propagate cancellation before waking the owner. If later shell code
	// recovers the status, that later successful command can wake it.
	// A Bash++ return is also unsettled here: `return 7` records a value
	// with status zero before the result-less function converts it to
	// status 7. Keep the handshake closed while that return unwinds,
	// including through enclosing block commands.
	if r.bashPPConcurrent != nil && !r.bashPPTaskFailed && !r.exit.returning {
		r.bashPPConcurrent.arm(r.bashPPTaskState)
	}
}

// bashPPGoFormCommand reports whether cm is one of the statements
// [Runner.cmdGoForm] runs.
func bashPPGoFormCommand(cm syntax.Command) bool {
	switch cm.(type) {
	case *syntax.BashPPDecl, *syntax.BashPPConstGroup, *syntax.BashPPShortDecl,
		*syntax.BashPPGo, *syntax.BashPPSend, *syntax.BashPPReceive,
		*syntax.BashPPClose, *syntax.BashPPSelect, *syntax.BashPPRange,
		*syntax.BashPPAssign, *syntax.BashPPCall, *syntax.BashPPCommandCall,
		*syntax.BashPPFuncDecl, *syntax.BashPPReturn, *syntax.BashPPDefer,
		*syntax.BashPPSwitch, *syntax.BashPPImport, *syntax.BashPPIf,
		*syntax.BashPPFor, *syntax.BashPPForAssign, *syntax.BashPPIncDec,
		*syntax.BashPPUpdate, *syntax.BashPPBranch, *syntax.BashPPLabeled,
		*syntax.BashPPGoto:
		return true
	}
	return false
}

// cmdGoForm runs a Go-form statement and reports whether cm was one. It must
// stay in step with [bashPPGoFormCommand].
func (r *Runner) cmdGoForm(ctx context.Context, cm syntax.Command) bool {
	switch cm := cm.(type) {
	// Statements that can name a type are rebound against the enclosing
	// generic frame's type arguments before they run, on a clone: the AST is
	// shared by every instantiation, so `var zero T` must not be rewritten in
	// place by the first call. See bashpp_generic_body.go.
	case *syntax.BashPPDecl:
		r.bashPPDeclare(ctx, r.bashPPBindDecl(cm))
	case *syntax.BashPPConstGroup:
		r.bashPPConstGroup(ctx, r.bashPPBindConstGroup(cm))
	case *syntax.BashPPShortDecl:
		r.bashPPShortDecl(ctx, r.bashPPBindShortDecl(cm))
	case *syntax.BashPPGo:
		r.bashPPGo(ctx, cm)
	case *syntax.BashPPSend:
		r.bashPPSend(ctx, cm)
	case *syntax.BashPPReceive:
		r.bashPPReceive(ctx, cm, nil)
	case *syntax.BashPPClose:
		r.bashPPClose(cm)
	case *syntax.BashPPSelect:
		r.bashPPSelect(ctx, cm)
	case *syntax.BashPPRange:
		r.bashPPRange(ctx, r.bashPPBindRange(cm))
	case *syntax.BashPPAssign:
		r.bashPPAssign(ctx, r.bashPPBindAssign(cm))
	case *syntax.BashPPCall:
		r.bashPPCall(ctx, r.bashPPBindCallNode(cm))
	case *syntax.BashPPCommandCall:
		r.bashPPCommandCall(ctx, cm)
	case *syntax.BashPPFuncDecl:
		r.bashPPFuncDecl(cm)
	case *syntax.BashPPReturn:
		r.bashPPReturnStmt(ctx, r.bashPPBindReturn(cm))
	case *syntax.BashPPDefer:
		r.bashPPDeferStmt(ctx, cm)
	case *syntax.BashPPSwitch:
		r.bashPPSwitch(ctx, r.bashPPBindSwitch(cm))
	case *syntax.BashPPImport:
		r.bashPPImport(ctx, cm)
	case *syntax.BashPPIf:
		r.bashPPIf(ctx, cm)
	case *syntax.BashPPFor:
		r.bashPPFor(ctx, cm)
	case *syntax.BashPPForAssign:
		r.bashPPForAssign(cm)
	case *syntax.BashPPIncDec:
		r.bashPPIncDec(r.bashPPBindIncDec(cm))
	case *syntax.BashPPUpdate:
		r.bashPPUpdate(r.bashPPBindUpdate(cm))
	case *syntax.BashPPBranch:
		r.bashPPBranchStmt(cm)
	case *syntax.BashPPLabeled:
		// goto resolves the label in [Runner.stmts], which owns the statement
		// list the label lives in; here the block only remembers what it had
		// declared so far, for a backward goto to re-enter it at this point.
		if r.bashPPScope != nil {
			r.bashPPScope.markLabel(cm.Label.Value)
		}
		if cm.Stmt != nil {
			r.stmt(ctx, cm.Stmt)
		}
	case *syntax.BashPPGoto:
		r.bashPPGotoStmt(cm)
	default:
		return false
	}
	return true
}

func (r *Runner) cmdGeneral(ctx context.Context, cm syntax.Command) {
	if r.stop(ctx) {
		return
	}
	if r.bashPPGoTask {
		defer r.cmdGoTaskSettled()
	}

	tracingEnabled := r.opts[optXTrace]
	trace := r.tracer(cm.Pos())

	switch cm := cm.(type) {
	case *syntax.SourceBlock:
		// The complete source unit is prepared before its first statement.
		// A declaration block has no execution side effect at this point.
		return
	case *syntax.BashPPAgenticBlock:
		saved := r.bashPPAgentic
		r.bashPPAgentic = true
		defer func() { r.bashPPAgentic = saved }()
		r.stmts(ctx, cm.Body.Stmts)
	case *syntax.Block:
		r.cmdBlock(ctx, cm)
	case *syntax.Subshell:
		r2 := r.subshell(false)
		if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil && bg.carrierRootSubshell == cm {
			bg.carrierSignalRunner.Store(r2)
		}
		defer r2.closeDirFile()
		// This internal shell cannot be reused or Reset by the caller. Join
		// its own subscriptions after EXIT cleanup, without canceling any
		// asynchronous descendants that outlive the parenthesized scope.
		defer r2.stopSignalSubscriptions()
		r2.enclosingSubshellEnd = cm.Rparen
		r2.stmts(ctx, cm.Stmts)
		if cb := r2.trapCallbacks["EXIT"]; cb != "" && !r2.inheritedExitTrap {
			r2.trapCallback(ctx, cb, "exit")
		}
		r2.exit.noNegate = r2.exit.exiting || r2.exit.returning || r2.exit.fatalExit
		// Subshells don't exit or return from the surrounding
		// function: `(return 5)` makes the subshell exit with
		// status 5, but the outer function/script keeps running.
		r2.exit.exiting = false
		r2.exit.discarding = false
		r2.exit.returning = false
		r.exit = r2.exit
	case *syntax.CallExpr:
		// Bash sets $BASH_COMMAND to the command's source text BEFORE
		// expansion, so a command can reference its own line via
		// $BASH_COMMAND. The later setVarString in r.call()
		// overwrites this with the post-expansion command while the
		// command itself runs.
		if !r.handlingTrap {
			var cmdBuf strings.Builder
			syntax.NewPrinter().Print(&cmdBuf, cm)
			r.setVarString("BASH_COMMAND", strings.TrimRight(cmdBuf.String(), "\n"))
		}
		// Bash fires DEBUG before each simple command, including
		// assignment-only forms (`x=2`). With `shopt -s extdebug`,
		// a trap that returns 2 skips the next command. Fire before
		// word expansion so command substitutions in arguments do
		// not run ahead of the trap.
		if r.shouldFireDebugTrap() {
			debugPos := cm.Pos()
			if len(cm.Args) == 0 && len(cm.Assigns) > 0 {
				debugPos = cm.Assigns[0].Pos()
			}
			prevLineno := r.ecfg.OverrideLineno
			r.ecfg.OverrideLineno = r.debugTrapLine(debugPos)
			debugCode := r.trapCallback(ctx, r.trapCallbacks["DEBUG"], "debug")
			r.ecfg.OverrideLineno = prevLineno
			if opt, _ := r.bashOptByName("extdebug"); opt != nil && *opt && debugCode == 2 {
				return
			}
		}
		// Use a new slice, to not modify the slice in the alias map.
		args := cm.Args
		origCallPos := cm.Pos()
		// Bash expands an alias only on input read after the line that
		// defined it; a definition and use sharing a parse unit (same
		// line) are not affected. Reconstruct that timing from effective
		// line numbers (see aliasBase / aliasDefOverride on Runner).
		aliasUseLine := r.aliasUseLine(int(origCallPos.Line()))
		if r.opts[optExpandAliases] && r.aliasReparseDepth == 0 &&
			len(cm.Assigns) == 0 && len(args) > 0 && r.aliasStartsCompound(args[0].Lit(), aliasUseLine) {
			if file, ok := r.aliasReparsePhysicalLine(origCallPos, aliasUseLine); ok {
				r.runAliasReparseFile(ctx, cm.Pos(), aliasUseLine, file)
				return
			}
		}
		if r.opts[optExpandAliases] && r.aliasReparseDepth == 0 &&
			!cm.End().IsValid() && r.sourcePhysicalLineContains(origCallPos, "$(") {
			if file, ok := r.aliasReparsePhysicalLine(origCallPos, aliasUseLine); ok {
				r.runAliasReparseFile(ctx, cm.Pos(), aliasUseLine, file)
				return
			}
		}
		seenAliases := make(map[string]bool)
		aliasExpanded := false
		// Variable-assignment prefixes (`a=A b`) apply to the
		// command that results from alias expansion. The reparse
		// paths below run a freshly parsed file and `return`, so the
		// surrounding cm.Assigns would otherwise be dropped; prepend
		// their source text so they attach to the first command, as
		// bash does (`a=A b` with `alias b='b=B cmd'` -> `a=A b=B cmd`).
		var assignSrc string
		if len(cm.Assigns) > 0 {
			var ab strings.Builder
			syntax.NewPrinter().Print(&ab, &syntax.CallExpr{Assigns: cm.Assigns})
			assignSrc = ab.String()
		}
		for i := 0; i < len(args); {
			if !r.opts[optExpandAliases] {
				break
			}
			name := args[i].Lit()
			if seenAliases[name] {
				break
			}
			als, ok := r.alias[name]
			if !ok {
				break
			}
			if als.defLine > 0 && aliasUseLine > 0 && aliasUseLine <= als.defLine {
				break
			}
			seenAliases[name] = true
			aliasExpanded = true
			if als.raw != "" && i == 0 {
				var src strings.Builder
				if assignSrc != "" {
					src.WriteString(assignSrc)
					src.WriteByte(' ')
				}
				src.WriteString(als.raw)
				if als.blank {
					src.WriteByte(' ')
				}
				if i+1 < len(args) {
					if src.Len() > 0 && !strings.HasSuffix(src.String(), " ") && !strings.HasSuffix(src.String(), "\t") {
						src.WriteByte(' ')
					}
					syntax.NewPrinter().Print(&src, &syntax.CallExpr{Args: args[i+1:]})
				}
				p := syntax.NewParser()
				file, err := p.Parse(strings.NewReader(src.String()), "")
				if r.aliasStartsCompound(name, aliasUseLine) {
					if retry, ok := r.aliasReparsePhysicalLine(origCallPos, aliasUseLine); ok {
						file = retry
						err = nil
					}
				}
				if err != nil {
					break
				}
				r.runAliasReparseFile(ctx, cm.Pos(), aliasUseLine, file)
				return
			}
			if als.raw != "" {
				break
			}
			// Keyword or multi-stmt alias (`alias e=export`,
			// `alias foo=$'echo a\necho b'`): execute the parsed file
			// in place of the surrounding call. Only kicks in when the
			// alias word is at i==0 (alias position); otherwise treat it
			// as plain text. If the alias has a suffix, reparse the alias
			// text plus that suffix so keyword aliases like
			// `alias e=export; e x=$y` keep their declaration operands.
			if als.file != nil && i == 0 {
				file := als.file
				if i+1 < len(args) || assignSrc != "" {
					var src strings.Builder
					if assignSrc != "" {
						src.WriteString(assignSrc)
						src.WriteByte(' ')
					}
					src.WriteString(aliasValue(als))
					if i+1 < len(args) {
						if src.Len() > 0 && !strings.HasSuffix(src.String(), " ") && !strings.HasSuffix(src.String(), "\t") {
							src.WriteByte(' ')
						}
						syntax.NewPrinter().Print(&src, &syntax.CallExpr{Args: args[i+1:]})
					}
					p := syntax.NewParser()
					var err error
					file, err = p.Parse(strings.NewReader(src.String()), "")
					if err != nil {
						break
					}
				}
				// A blank/empty alias whose body parses to nothing and
				// that has no suffix (`alias b=' '; b`, `alias a=; a`)
				// leaves no command at all — like a blank line it must
				// preserve $? rather than reset it to 0 (POSIX 2.9.1).
				// r.exit was reset at statement entry, so restore the
				// prior status from lastExit.
				if len(file.Stmts) == 0 {
					r.exit = r.lastExit
					return
				}
				// Override the runtime line so diagnostics
				// (`command not found`, etc.) report bash's invocation
				// line rather than the line within the alias body.
				prevOverride := r.aliasLineOverride
				r.aliasLineOverride = int(cm.Pos().Line())
				r.withAliasReparse(aliasUseLine, func() {
					r.stmts(ctx, file.Stmts)
				})
				r.aliasLineOverride = prevOverride
				return
			}
			args = slices.Replace(args, i, i+1, als.args...)
			if !als.blank {
				continue
			}
			i += len(als.args)
			seenAliases = make(map[string]bool)
		}
		if aliasExpanded {
			prevOverride := r.aliasLineOverride
			r.aliasLineOverride = int(origCallPos.Line())
			defer func() { r.aliasLineOverride = prevOverride }()
		}
		// A blank/empty alias (`alias b=' '`, `alias a=`) that consumes
		// the entire command leaves no command name and no assignments.
		// Like a blank line it is not a command at all, so it must
		// preserve $? rather than run an empty command (which would reset
		// $? to 0). POSIX 2.9.1: only a command with a word or an
		// assignment completes with its own status. r.exit was reset to 0
		// at statement entry, so restore the prior status from lastExit.
		// (Any redirects on the statement have already been applied by
		// stmtSync.)
		if aliasExpanded && len(args) == 0 && len(cm.Assigns) == 0 {
			r.exit = r.lastExit
			return
		}
		if r.integerArrayAssignWithArithSuffix(cm, args) {
			return
		}
		r.lastExpandExit = exitStatus{}
		r.lastExpandCmdSubst = false
		args = r.bashPPRewriteCommandArgs(args)
		var fields []string
		var expandErr error
		if prefix := commandDeclarationPrefix(args); r.opts[optPosix] && prefix >= 0 {
			// A declaration utility remains a declaration when reached through
			// one or more command builtins. Its assignment operands must use
			// assignment expansion, without field splitting or pathname expansion.
			// Re-expand from the syntax words so an operand stays one argument.
			for i, arg := range args {
				if i > prefix && assignmentWord(arg) {
					field, err := expand.LiteralForAssign(r.ecfg, arg)
					if expandErr == nil {
						expandErr = err
					}
					fields = append(fields, field)
				} else {
					part, err := expand.Fields(r.ecfg, arg)
					if expandErr == nil {
						expandErr = err
					}
					fields = append(fields, part...)
				}
			}
		} else {
			fields, expandErr = expand.Fields(r.ecfg, args...)
		}
		r.expandErr(expandErr)
		if expandErr != nil && r.bashCompatErrors && !r.interactiveShell {
			var arithErr *expand.ArithmError
			if errors.As(expandErr, &arithErr) &&
				!strings.Contains(expandErr.Error(), "not a valid identifier") &&
				(arithErr.Standalone || strings.Contains(expandErr.Error(), "arithmetic syntax error")) {
				// An arithmetic expansion error discards this input line;
				// POSIX mode exits the shell instead.
				r.exit.code = 1
				r.exit.exiting = true
				if !r.opts[optPosix] {
					r.discardExpansionLine()
				}
			}
		}
		if r.exit.exiting {
			return
		}
		if r.bashPPChanBoundary {
			for _, field := range fields {
				if r.bashPPHasRuntimeHandle(field) {
					r.errf("bash++: channel handles cannot cross a shell-copy boundary\n")
					r.exit.code = 2
					return
				}
			}
		}
		if standaloneArithParseError(expandErr) {
			// A standalone `$(( ))` PARSE error (e.g. `echo $((--))`) is fatal to
			// the command in bash 5.3: status 1, the command does not run. Array
			// subscripts, malformed length substitutions, and arithmetic recursion
			// stay non-fatal (they have their own bash-faithful handling).
			r.exit.code = 1
			return
		}
		if expandErr != nil && strings.Contains(expandErr.Error(), "bad array subscript") &&
			!strings.HasPrefix(expandErr.Error(), "[") {
			fields = fields[:0]
			for _, arg := range args {
				argFields, err := expand.Fields(r.ecfg, arg)
				if err != nil {
					continue
				}
				fields = append(fields, argFields...)
			}
		}
		if len(args) > 1 && args[0].Lit() == "unset" {
			fields = []string{"unset"}
			for _, arg := range args[1:] {
				if lit, ok := unsetArrayOperandLiteral(arg); ok {
					fields = append(fields, lit)
				} else if src, ok := r.unsetQuotedSubscriptSource(arg); ok {
					fields = append(fields, src)
				} else {
					fields = append(fields, r.fields(arg)...)
				}
			}
		}
		// Apply redirections that stmtSync deferred until after command
		// substitutions in the simple command were expanded (POSIX order).
		// On failure, skip the command; closers opened so far are owned by
		// stmtSync's defer.
		assignmentOnlyWithLateAssigns := len(fields) == 0 && len(cm.Assigns) > 0 && r.lateRedirs != nil
		if !assignmentOnlyWithLateAssigns {
			if !r.applyLateRedirs(ctx, fields) {
				return
			}
		}
		// POSIX mode parses with the POSIX language variant, where
		// `time` is not the timing reserved word but an ordinary
		// command word. Bash, however, keeps `time` reserved even in
		// POSIX mode, so bare `time` reports the shell's cumulative
		// user/system times (bashref "Bash POSIX Mode" #6) rather than
		// running an external `time`. A `time` with arguments stays an
		// ordinary command, matching bashy's POSIX-variant parse.
		if r.opts[optPosix] && len(fields) == 1 && fields[0] == "time" && len(cm.Assigns) == 0 {
			r.posixTimesReport()
			break
		}
		if !r.exit.ok() && r.opts[optPosix] {
			special := len(fields) > 0 && isPosixSpecialBuiltin(fields[0])
			if !special && len(args) > 0 {
				special = isPosixSpecialBuiltin(args[0].Lit())
			}
			if special {
				r.exit.exiting = true
				break
			}
		}
		// bash 5.3: when a CallExpr already has assignment prefixes
		// (`a=5 b=6 $CMD ...`) AND the remaining words after
		// expansion are ALL assignment-shaped (typically because
		// the command word expanded to nothing), promote them to
		// additional plain assignments. e.g.
		//   a=5 b=6 $UNSET c=7 d=8
		// becomes assignments a=5, b=6, c=7, d=8.
		// Also promote when `set -k` (keyword mode) is on: bash
		// treats any `name=value` word anywhere on the line as
		// part of the env, and when the command word disappears
		// the assignments become plain global assignments.
		// Without -k, only fires when cm.Assigns is non-empty —
		// keeps the brace-expanded `{a,b}=value` case (no leading
		// assigns) on the cmd path so it errors as bash 3.x did.
		setK := r.opts[optKeyword]
		if (len(cm.Assigns) > 0 || setK) && len(fields) > 0 && fieldsPromotableAssignments(fields, args) {
			assignPlain := func(name string, vr expand.Variable) bool {
				r.setVar(name, vr)
				if !r.exit.ok() && !r.exit.exiting && !r.exit.returning && !r.exit.fatalExit {
					// Assignment-statement error: fatal in POSIX
					// mode, DISCARD otherwise (see below).
					r.exit.exiting = true
					if !r.opts[optPosix] {
						r.exit.discarding = true
					}
					return false
				}
				return true
			}
			if setK {
				for _, as := range cm.Assigns {
					name := as.Name.Value
					prev := r.lookupVar(name)
					prev.Local = false
					name, vr := r.assignVal(name, prev, as, "")
					if !assignPlain(name, vr) {
						return
					}
				}
				for _, arg := range args {
					field := r.literalForAssign(arg)
					if field == "" {
						continue
					}
					name, val, ok := splitAssignmentField(field)
					if !ok {
						continue
					}
					if !assignPlain(name, expand.Variable{Set: true, Kind: expand.String, Str: val}) {
						return
					}
				}
			} else {
				for _, f := range fields {
					name, val, _ := splitAssignmentField(f)
					if !assignPlain(name, expand.Variable{Set: true, Kind: expand.String, Str: val}) {
						return
					}
				}
			}
			fields = nil
		}
		if len(fields) == 0 {
			// execute_cmd.c:execute_simple_command calls bind_lastarg(NULL)
			// for an assignment/redirection-only simple command.
			defer r.setVarString("_", "")
			for _, as := range cm.Assigns {
				var copiedChannel *bashPPChannel
				var copiedOwner *bashPPConcurrent
				if r.bashPPFuncActive > 0 {
					copiedChannel, copiedOwner = r.bashPPDirectChannel(as.Value)
				}
				as = r.bashPPRewriteAssign(as)
				name := as.Name.Value

				prev := r.lookupVar(name)
				// Here we have a naked "foo=bar", so if we inherited a local var from a parent
				// function we want to signal that we are modifying the parent var rather than
				// creating a new local var via "local foo=bar".
				// TODO: there is likely a better way to do this.
				prev.Local = false

				if as.Index != nil && as.Array != nil {
					r.errf("%s%s: cannot assign list to array member\n", r.bashErrPrefix(r.curStmtPos), r.inlineArrayAssignName(as))
					r.exit.code = 1
					break
				}
				prevForIndex := prev
				if as.Index != nil {
					if prev.Kind == expand.NameRef {
						if _, _, ok := splitArrayRef(prev.Str); ok {
							r.errf("%s`%s': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos), prev.Str)
							r.exit.code = 1
							continue
						}
					}
					if n, resolved := prev.Resolve(r.writeEnv); n != "" {
						prevForIndex = resolved
					}
				}
				if as.Array != nil && prev.Kind == expand.NameRef {
					if _, _, ok := splitArrayRef(prev.Str); ok {
						r.errf("%s`%s': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos), prev.Str)
						r.exit.code = 1
						continue
					}
				}
				roTarget := prev
				if n, resolved := prev.Resolve(r.writeEnv); n != "" {
					roTarget = resolved
				}
				wasReadOnly := roTarget.ReadOnly
				// Bash reports a readonly array append/element store made
				// through a nameref under the nameref name as written
				// (`r+=(4)` -> `r: readonly variable`), unlike a scalar
				// assignment which reports the resolved target.
				if prev.Kind == expand.NameRef && (as.Array != nil || as.Index != nil) {
					r.assignNamerefName = as.Name.Value
				}
				name, vr := r.assignVal(name, prev, as, "")
				r.setVarWithIndex(prevForIndex, name, as.Index, vr, as.Append)
				if as.Index == nil && copiedChannel != nil && copiedOwner == r.bashPPConcurrent {
					if cell := r.bashPPScope.lookup(name); cell != nil {
						cell.channel, cell.channelOwner = copiedChannel, copiedOwner
					}
				}
				r.assignNamerefName = ""
				if !r.exit.ok() && !r.exit.exiting && !r.exit.returning && !r.exit.fatalExit {
					// Bash: an assignment-statement error (readonly
					// variable, bad subscript, …) exits a POSIX-mode
					// shell and DISCARDs the current top-level
					// command otherwise — aborting any enclosing
					// loop, but not the whole script. A real exit
					// propagating out of an expansion (`v=${ exit 2; }`)
					// already carries its own flags; leave it alone.
					//
					// Under `set -e` the failed assignment is fatal and
					// exits the shell unconditionally (bash applies this
					// even inside an if/while condition, an `&&`/`||`
					// list, or after `!` — the usual errexit exemptions
					// do not apply to readonly-assignment errors), so
					// don't downgrade it to a rest-of-line discard.
					r.exit.exiting = true
					if (!r.opts[optPosix] || r.interactiveShell) && !r.opts[optErrExit] {
						// An assignment error spares an interactive shell
						// even in POSIX mode: the current command is
						// discarded (DISCARD longjmp) but the shell keeps
						// reading input rather than exiting.
						r.exit.discarding = true
						// A standalone assignment to a readonly variable
						// also aborts the remaining statements on the same
						// physical line (`RO=z; declare -p RO; echo $RO`).
						if wasReadOnly && as.Index == nil {
							r.discardRestOfLine = r.curStmtPos.Line()
						}
					}
					break
				}

				if !tracingEnabled {
					continue
				}

				// Strangely enough, it seems like Bash prints original
				// source for arrays, but the expanded value otherwise.
				// TODO: add test cases for x[i]=y.
				op := "="
				if as.Append {
					op = "+="
				}
				if as.Array != nil {
					// bash xtrace re-quotes each array element
					// rather than rendering the original source
					// literal — `$' '` becomes `' '`, `\|` stays
					// as `\|` (since the printer keeps backslash-
					// escapes), and tab/newline use single-quote
					// literals.
					traceArrayLiteral(trace, name, op, as.Array.Elems, r)
				} else if as.Value != nil {
					// Bash 5.3 traces the *raw* RHS for `+=` (so
					// the trace shows the appended chunk, not the
					// pre-append concatenated value). For `=` we
					// keep the expanded form.
					val := vr.String()
					if as.Append {
						val, _ = expand.Literal(r.ecfg, as.Value)
					}
					quoted, err := syntax.Quote(val, syntax.LangBash)
					if err != nil { // should never happen
						panic(err)
					}
					trace.stringf("%s%s%s", name, op, quoted)
				} else {
					// An empty assignment (`null=`) traces as
					// `+ null=`, as GNU Bash prints it.
					trace.stringf("%s%s", name, op)
				}
				trace.newLineFlush()
			}
			// If interpreting the last expansion like $(foo) failed,
			// and the expansion and assignments otherwise succeeded,
			// we need to surface that last exit code.
			if r.exit.ok() {
				r.exit = r.lastExpandExit
			}
			if assignmentOnlyWithLateAssigns {
				r.applyLateRedirs(ctx, fields)
			}
			releaseBgPid(ctx)
			break
		}
		if r.lastExpandCmdSubst {
			r.lastExit = r.lastExpandExit
		}

		type restoreVar struct {
			name                string
			vr                  expand.Variable
			wasTemp             bool
			wasGoCommandTouched bool
		}
		var restores []restoreVar
		type callAssign struct {
			name string
			vr   expand.Variable
		}
		var keywordAssigns []callAssign

		if setK {
			cleanFields := fields[:1]
			for _, field := range fields[1:] {
				name, val, ok := splitAssignmentField(field)
				if !ok {
					cleanFields = append(cleanFields, field)
					continue
				}
				keywordAssigns = append(keywordAssigns, callAssign{
					name: name,
					vr:   expand.Variable{Set: true, Kind: expand.String, Str: val, Exported: true},
				})
			}
			fields = cleanFields
		}

		assignFailed := false
		readonlyAssignFailed := false
		for _, as := range cm.Assigns {
			as = r.bashPPRewriteAssign(as)
			if as.Index != nil {
				r.errf("%s`%s': not a valid identifier\n",
					r.bashErrPrefix(r.curStmtPos), r.inlineArrayAssignName(as))
				assignFailed = true
				continue
			}
			name := as.Name.Value
			prev := r.lookupVar(name)
			wasTemp := false
			wasGoCommandTouched := false
			if o, ok := r.writeEnv.(*overlayEnviron); ok {
				wasTemp = o.isTemp(name)
			}
			// Resolve any nameref so we can restore the original final value later on.
			if n, v := prev.Resolve(r.writeEnv); n != "" {
				name, prev = n, v
			}
			if o, ok := r.writeEnv.(*overlayEnviron); ok {
				wasGoCommandTouched = o.goCommandWasTouched(name)
			}
			readonlyTemp := false
			if fields[0] == "readonly" && prev.ReadOnly {
				if o, ok := r.writeEnv.(*overlayEnviron); ok && o.isTemp(name) {
					for _, field := range fields[1:] {
						if field == name {
							prev.ReadOnly = false
							readonlyTemp = true
							break
						}
					}
				}
			}

			var vr expand.Variable
			if as.Array != nil {
				vr = expand.Variable{Set: true, Kind: expand.String, Str: r.arrayAssignEnvValue(as)}
			} else {
				// A command-prefix assignment to an empty-target nameref
				// whose value is not a valid identifier (`r=/ cmd` with
				// `declare -n r` in scope) is stored literally in the temp
				// environment by bash rather than retargeting the nameref
				// and failing. `$r` then expands to the raw value; a later
				// `declare -n r` re-validates and reports the bad target.
				if prev.Kind == expand.NameRef && prev.Str == "" && as.Value != nil &&
					!validNameRefTarget(r.literalForAssign(as.Value)) {
					vr = expand.Variable{Set: true, Kind: expand.String, Str: r.literalForAssign(as.Value)}
				} else {
					name, vr = r.assignVal(name, prev, as, "")
				}
			}
			// Inline command vars are always exported.
			vr.Exported = true

			if readonlyTemp {
				if o, ok := r.writeEnv.(*overlayEnviron); ok {
					o.setTempVar(name, vr)
				}
			} else {
				r.setVar(name, vr)
			}
			if !r.exit.ok() {
				assignFailed = true
				readonlyAssignFailed = prev.ReadOnly && !readonlyTemp
				break
			}
			alreadyRestoring := false
			for _, restore := range restores {
				if restore.name == name {
					alreadyRestoring = true
					break
				}
			}
			if !alreadyRestoring {
				if o, ok := r.writeEnv.(*overlayEnviron); ok {
					o.markTemp(name, prev)
				}
				restores = append(restores, restoreVar{name: name, vr: prev, wasTemp: wasTemp, wasGoCommandTouched: wasGoCommandTouched})
			}
			if tracingEnabled && as.Value != nil {
				op := "="
				if as.Append {
					op = "+="
				}
				val := vr.String()
				if as.Append {
					val, _ = expand.Literal(r.ecfg, as.Value)
				}
				quoted, err := syntax.Quote(val, syntax.LangBash)
				if err != nil {
					panic(err)
				}
				trace.stringf("%s%s%s", name, op, quoted)
				trace.newLineFlush()
			}
		}
		for _, as := range keywordAssigns {
			prev := r.lookupVar(as.name)
			wasTemp := false
			wasGoCommandTouched := false
			if o, ok := r.writeEnv.(*overlayEnviron); ok {
				wasTemp = o.isTemp(as.name)
				wasGoCommandTouched = o.goCommandWasTouched(as.name)
			}
			r.setVar(as.name, as.vr)
			if !r.exit.ok() {
				assignFailed = true
				readonlyAssignFailed = prev.ReadOnly
				break
			}
			if o, ok := r.writeEnv.(*overlayEnviron); ok {
				o.markTemp(as.name, prev)
			}
			restores = append(restores, restoreVar{name: as.name, vr: prev, wasTemp: wasTemp, wasGoCommandTouched: wasGoCommandTouched})
		}
		if assignFailed {
			if r.strictPosix && !r.interactiveShell {
				r.exit.exiting = true
				break
			}
			if r.opts[optPosix] {
				if r.subshellLevel > 0 && !r.interactiveShell {
					r.exit.exiting = true
					break
				}
				if isPosixSpecialBuiltin(fields[0]) && !r.interactiveShell {
					r.exit.exiting = true
					break
				}
				if readonlyAssignFailed && !r.interactiveShell {
					// Bash discards the rest of this physical line after a
					// readonly prefix assignment, but resumes on the next
					// line for a non-special command.
					r.exit.exiting = true
					r.exit.discarding = true
					r.discardRestOfLine = r.curStmtPos.Line()
					break
				}
				// Bash 5.3's default build is not STRICT_POSIX: other temporary
				// assignment errors before a non-special command discard the
				// command and continue with status 1.
				for _, restore := range restores {
					r.restoreInlineVar(restore.name, restore.vr, restore.wasGoCommandTouched)
				}
				r.exit.code = 1
				break
			}
			// Default mode: the error is reported but the command
			// still runs (`VAR=7 echo ok` prints ok).
		}

		if strings.HasPrefix(fields[0], "%") {
			fields = append([]string{"fg"}, fields...)
		}

		trace.call(fields[0], fields[1:]...)
		trace.newLineFlush()

		callPos := cm.Args[0].Pos()
		if aliasExpanded {
			callPos = origCallPos
		}
		savedTempEnv := r.tempEnv
		if len(cm.Assigns) > 0 || len(keywordAssigns) > 0 {
			m := maps.Clone(savedTempEnv)
			if m == nil {
				m = make(map[string]bool, len(cm.Assigns)+len(keywordAssigns))
			}
			for _, as := range cm.Assigns {
				m[as.Name.Value] = true
			}
			for _, as := range keywordAssigns {
				m[as.name] = true
			}
			r.tempEnv = m
		}
		restoreFrame := &inlineRestoreFrame{restores: make(map[string]bool, len(restores))}
		for _, restore := range restores {
			restoreFrame.restores[restore.name] = true
		}
		r.inlineRestoreFrames = append(r.inlineRestoreFrames, restoreFrame)
		r.call(ctx, callPos, fields)
		r.tempEnv = savedTempEnv
		// Bash POSIX mode (or inside a function): assignments
		// preceding a special builtin (`return`, `export`, `eval`,
		// `readonly`, `set`, …) persist after the command returns.
		// Plain functions and regular commands restore their temporary
		// assignments even in POSIX mode.
		special := r.invokesSpecialBuiltin(fields[0])
		inFuncScope := r.inFunc || len(r.callStack) > 0
		persistInline := special && (r.opts[optPosix] || inFuncScope)
		switch fields[0] {
		case "export", "readonly":
			persistInline = true
		case "declare", "typeset":
			persistInline = false
			if !inFuncScope {
				for _, arg := range fields[1:] {
					if arg == "--" {
						continue
					}
					if strings.HasPrefix(arg, "-") && (strings.Contains(arg, "x") || strings.Contains(arg, "r")) {
						persistInline = true
					}
				}
			}
		}
		if persistInline && (fields[0] == "export" || fields[0] == "readonly") &&
			!r.opts[optPosix] && !inFuncScope {
			operands := declareOperandNames(fields[1:])
			kept := restores[:0]
			for _, restore := range restores {
				if operands[restore.name] {
					kept = append(kept, restore)
					continue
				}
				r.restoreInlineVar(restore.name, restore.vr, restore.wasGoCommandTouched)
			}
			restores = kept
		}
		if !persistInline {
			for _, restore := range restores {
				if restoreFrame.leaked[restore.name] {
					continue
				}
				r.restoreInlineVar(restore.name, restore.vr, restore.wasGoCommandTouched)
			}
		} else if r.inFunc && fields[0] == "return" {
			// `var=N return …` inside a function: bash 5.3 leaks
			// the inline assignment to the caller's scope so it
			// outlives the function's overlay being popped.
			// Promote the function-local value to global and
			// flag the caller's restore loop to skip the name.
			for _, restore := range restores {
				vr := r.lookupVar(restore.name)
				if vr.IsSet() {
					r.setGlobalVarString(restore.name, vr.String())
					r.markEnclosingInlineLeak(restore.name)
				}
			}
		} else if r.inFunc && r.opts[optPosix] && (fields[0] == "export" || fields[0] == "readonly") {
			// POSIX export/readonly keep preceding assignments after
			// the function returns. If the function was called through
			// `name=value f`, the caller's restore must not clobber
			// this value.
			for _, restore := range restores {
				if o, ok := r.writeEnv.(*overlayEnviron); ok {
					if o.holdsLocally(restore.name) {
						continue
					}
					o.clearTemp(restore.name)
				}
				if !restore.wasTemp {
					continue
				}
				r.markEnclosingInlineLeak(restore.name)
			}
		}
		r.inlineRestoreFrames = r.inlineRestoreFrames[:len(r.inlineRestoreFrames)-1]
	case *syntax.BinaryCmd:
		switch cm.Op {
		case syntax.AndStmt, syntax.OrStmt:
			oldNoErrExit := r.noErrExit
			r.noErrExit = true
			r.stmt(ctx, cm.X)
			r.noErrExit = oldNoErrExit
			if r.loopControlPending() {
				return
			}
			if r.exit.ok() == (cm.Op == syntax.AndStmt) {
				r.stmt(ctx, cm.Y)
			}
		case syntax.Pipe, syntax.PipeAll:
			streamCtx, finishStreamJob, streamErr := r.bashPPPrepareStreamPipeline(ctx, cm)
			if streamErr != nil {
				r.errf("%v\n", streamErr)
				r.exit.code = 1
				return
			}
			defer finishStreamJob()
			ctx = streamCtx
			pr, pw, err := os.Pipe()
			if err != nil {
				r.exit.fatal(err) // not being able to create a pipe is rare but critical
				return
			}
			// Duplicate pipe fds for use by the goroutines, then close
			// the originals when the duplicates are distinct. On Unix this
			// ensures the parent process does not hold extra references to the pipe, so that:
			//   - EOF propagates when the writer closes its end
			//   - SIGPIPE is delivered when the reader closes its end
			// Builtins and external commands in the goroutines use the
			// duplicated fds, which remain valid until explicitly closed.
			// Non-Unix dupPipeFd may return the same *os.File; keep those
			// originals open so in-process pipeline commands don't inherit
			// already-closed pipe endpoints. Hold for human Windows verify:
			// cat | tr | head should not report "file already closed".
			// See https://github.com/mvdan/sh/issues/1142
			pwDup, pwDistinct, err := dupPipeFd(pw)
			if err != nil {
				pw.Close()
				pr.Close()
				r.exit.fatal(err)
				return
			}
			prDup, prDistinct, err := dupPipeFd(pr)
			if err != nil {
				pw.Close()
				pr.Close()
				pwDup.Close()
				r.exit.fatal(err)
				return
			}
			if pwDistinct {
				pw.Close()
			}
			if prDistinct {
				pr.Close()
			}

			r2 := r.subshell(true)
			r2.pipelineOutput = true
			pipeOut := &pipelineWriter{w: pwDup, runner: r2}
			r2.stdout = pipeOut
			if cm.Op == syntax.PipeAll {
				r2.stderr = pipeOut
			} else {
				r2.stderr = r.stderr
			}
			// POSIX: the first command of a pipeline keeps the shell's
			// standard input. When the script itself is being read from
			// stdin (bash's stdin-script quirk), that means the first
			// command consumes subsequent script lines (`cat | tail` reads
			// the lines after the pipeline). subshell() does not propagate
			// the stdin-source state, so hand it to the first element's
			// runner explicitly and fold its consumed offset back into the
			// parent afterwards so the parent skips the lines the command
			// ate. Only X (the left side / first command) gets this; Y and
			// any inner Y read from the pipe.
			r2.bashSource = r.bashSource
			r2.stdinScript = r.stdinScript
			r2.stdinSourceActive = r.stdinSourceActive
			r2.stdinSourceOffset = r.stdinSourceOffset
			r2.stdinSourceBaseOffset = r.stdinSourceBaseOffset
			r2.curStmtEnd = r.curStmtEnd
			// bash 5.3: the last command in a pipeline runs in a
			// subshell unless `shopt -s lastpipe` is enabled (and
			// job control is off). Without lastpipe, assignments
			// in `... | { IFS=:; read line; }` MUST NOT leak to
			// the parent shell. Decide subshell-vs-current here.
			lastpipe := false
			if opt, _ := r.bashOptByName("lastpipe"); opt != nil && *opt {
				lastpipe = true
			}
			oldStdin := r.stdin
			oldStdinRedirected := r.stdinRedirected
			r.stdin = prDup
			r.stdinRedirected = true
			r.asyncStdinExplicit = true
			var wg sync.WaitGroup
			wg.Go(func() {
				defer r2.closeDirFile()
				xctx := context.WithValue(ctx, bgNonPrimaryPidCtxKey{}, true)
				xctx = context.WithValue(xctx, pipelineExecCtxKey{}, true)
				if subshell, ok := plainPipelineSubshell(cm.X); ok {
					r2.enclosingSubshellEnd = subshell.Rparen
					r2.stmts(xctx, subshell.Stmts)
				} else {
					r2.stmt(xctx, cm.X)
				}
				if cb := r2.trapCallbacks["EXIT"]; cb != "" && !r2.inheritedExitTrap {
					r2.trapCallback(xctx, cb, "exit")
				}
				r2.exit.exiting = false // subshells don't exit the parent shell
				r2.exit.discarding = false
				pwDup.Close()
			})
			var r3 *Runner
			if lastpipe {
				publishBgSignalRunner(ctx, r)
				r.stmt(context.WithValue(ctx, pipelineExecCtxKey{}, true), cm.Y)
			} else {
				// background=false: r3 lazily walks up to r.writeEnv,
				// matching `(...)` subshell semantics so callers that
				// supply a sparse parent env (e.g. expand.FuncEnviron
				// with no Each enumeration) still resolve variables.
				r3 = r.subshell(false)
				defer r3.closeDirFile()
				r3.stdin = prDup
				r3.stdinRedirected = true
				r3.asyncStdinExplicit = true
				r3.stdout = r.stdout
				r3.stderr = r.stderr
				publishBgSignalRunner(ctx, r3)
				if subshell, ok := plainPipelineSubshell(cm.Y); ok {
					r3.enclosingSubshellEnd = subshell.Rparen
					r3.stmts(context.WithValue(ctx, pipelineExecCtxKey{}, true), subshell.Stmts)
				} else {
					r3.stmt(context.WithValue(ctx, pipelineExecCtxKey{}, true), cm.Y)
				}
				if cb := r3.trapCallbacks["EXIT"]; cb != "" && !r3.inheritedExitTrap {
					r3.trapCallback(ctx, cb, "exit")
				}
				r3.exit.exiting = false
				r3.exit.discarding = false
				// Like an explicit `( ... )` subshell, a pipeline component
				// cannot return from a function in its parent shell. Keep the
				// numeric status produced by `return N`, but consume the control
				// transfer at this subshell boundary.
				r3.exit.returning = false
				r.exit = r3.exit
			}
			prDup.Close()
			wg.Wait()
			r.stdin = oldStdin
			r.stdinRedirected = oldStdinRedirected
			// Fold back any script-source input the first command consumed so
			// the parent's Run loop skips those now-eaten lines.
			r.stdinSourceOffset = max(r.stdinSourceOffset, r2.stdinSourceOffset)
			// A process substitution spawned while expanding a pipeline
			// element's words (e.g. `echo x | tee >(cmd)`) registers its
			// bgProc on that element's sub-runner (r2/r3), which is otherwise
			// discarded here. Propagate them to the parent so a later `wait`
			// — and the parent's lifecycle — actually cover them; otherwise
			// `... | tee >(cmd); wait` returns before cmd's output is flushed,
			// racily losing it. subshell() starts with empty bgProcs, so these
			// are only the pipeline's own process substitutions.
			r.bgProcs = append(r.bgProcs, r2.bgProcs...)
			if n := len(r2.bgProcs); n > 0 {
				r.lastBang = r2.bgProcs[n-1]
			}
			if r3 != nil {
				r.bgProcs = append(r.bgProcs, r3.bgProcs...)
				if n := len(r3.bgProcs); n > 0 {
					r.lastBang = r3.bgProcs[n-1]
				}
			}
			// Track PIPESTATUS. mvdan/sh parses pipes left-associative,
			// so `a | b | c` is (a | b) | c — X is the nested pipeline
			// and runs in r2. If r2 itself ran a pipeline, its segment
			// statuses are in r2.pipeStatus; we extend that with Y's
			// status to form the full chain. Otherwise it's a simple
			// X | Y pair.
			yCode := strconv.Itoa(int(r.exit.code))
			if len(r2.pipeStatus) > 0 {
				r.pipeStatus = append(append([]string(nil), r2.pipeStatus...), yCode)
			} else {
				r.pipeStatus = []string{strconv.Itoa(int(r2.exit.code)), yCode}
			}
			if r.opts[optPipeFail] && !r2.exit.ok() && r.exit.ok() {
				r.exit = r2.exit
			}
			if r2.exit.fatalExit {
				r.exit.fatal(r2.exit.err) // surface fatal errors immediately
			}
		}
	case *syntax.IfClause:
		oldNoErrExit := r.noErrExit
		r.noErrExit = true
		r.stmts(ctx, cm.Cond)
		r.noErrExit = oldNoErrExit
		if r.loopControlPending() || r.bashPPBranch != bashPPBranchNone {
			return
		}

		if r.exit.ok() {
			// The branch body is a block. `else` reaches here too: the
			// parser models it as an IfClause with an empty Cond whose Then
			// holds the else body, so one push gives then and else the
			// distinct scopes Go gives them.
			if r.bashPPScope != nil {
				defer r.bashPPPushScope()()
			}
			r.stmts(ctx, cm.Then)
			if !r.exit.ok() && stmtsEndErrexitExempt(cm.Then) {
				r.exit.errexitExempt = true
			}
			return
		}
		r.exit.clear()
		if cm.Else != nil {
			r.cmd(ctx, cm.Else)
		}
	case *syntax.WhileClause:
		var lastBodyExit exitStatus
		bodyRan := false
		for !r.stop(ctx) {
			oldNoErrExit := r.noErrExit
			r.noErrExit = true
			// The condition runs "in the loop": break/continue used in a
			// while/until condition act on this loop, matching bash — e.g.
			// `while break; do …; done` breaks (0 iterations) rather than
			// erroring "only meaningful in a loop" and looping forever.
			oldInLoop := r.inLoop
			r.inLoop = true
			r.stmts(ctx, cm.Cond)
			r.inLoop = oldInLoop
			r.noErrExit = oldNoErrExit

			// A signal trap can interrupt the condition itself. Preserve
			// return/exit/fatal control flow before normal while/until status
			// handling clears the condition status for the next iteration.
			if r.exit.returning || r.exit.exiting || r.exit.fatalExit {
				return
			}

			// Honor a break/continue signaled from the condition (same
			// enclosing-count semantics as loopStmtsBroken does for the body).
			if r.contnEnclosing > 0 {
				r.contnEnclosing--
				if r.contnEnclosing > 0 {
					break // continue targets an outer loop
				}
				r.exit.clear()
				continue // re-evaluate the condition
			}
			if r.breakEnclosing > 0 {
				r.breakEnclosing--
				break
			}

			if r.exit.ok() == cm.Until {
				if bodyRan && !r.exit.exiting && !r.exit.returning && !r.exit.fatalExit {
					r.exit = lastBodyExit
				} else {
					r.exit.clear()
				}
				break
			}
			r.exit.clear()
			broken := r.loopStmtsBroken(ctx, cm.Do)
			bodyRan = true
			lastBodyExit = r.exit
			if broken {
				break
			}
		}
		// If the loop's final body command was errexit-exempt (the left
		// operand of an `&&`/`||`), that exemption propagates to the
		// loop's own non-zero status so `set -e` does not exit — matching
		// bash's IfClause handling above.
		if !r.exit.ok() && bodyRan && stmtsEndErrexitExempt(cm.Do) {
			r.exit.errexitExempt = true
		}
	case *syntax.ForClause:
		switch y := cm.Loop.(type) {
		case *syntax.WordIter:
			name := y.Name.Value
			// Bash 5.3 rejects invalid identifier names at the
			// `for`/`select` loop step before any iteration runs.
			// `for invalid-name in a b c; do …` emits a
			// "not a valid identifier" diagnostic and aborts the
			// loop.
			if !syntax.ValidName(name) {
				// Bash's parser stamps a for/select command inside a
				// function with the body's opening line rather than
				// the keyword's own, and this diagnostic reports that
				// stamped line.
				line := int(y.Pos().Line())
				if n := len(r.callStack); n > 0 && r.callStack[n-1].bodyLine > 0 {
					line = int(r.callStack[n-1].bodyLine)
				}
				r.errf("%s`%s': not a valid identifier\n",
					r.bashErrPrefixLine(line), name)
				if r.opts[optPosix] {
					r.exit.code = 2
					r.exit.err = ExitStatus(2)
					r.exit.exiting = true
				} else {
					r.exit.code = 1
				}
				return
			}
			items := r.Params // for i; do ...

			inToken := y.InPos.IsValid()
			// Bash: on the loop's first iteration, `$?` reflects the last
			// command substitution in the `in` word list (`for i in $(…)`)
			// until the body runs a command. Capture it here.
			forHadCmdSubst := false
			var forExitCode uint8
			if inToken {
				r.lastExpandExit = exitStatus{}
				r.lastExpandCmdSubst = false
				items = r.fields(y.Items...) // for i in ...; do ...
				forHadCmdSubst = r.lastExpandCmdSubst
				forExitCode = r.lastExpandExit.code
			}

			if cm.Select {
				r.selectLoop(ctx, name, items, cm.Do)
				break
			}

			firstIter := true
			for _, field := range items {
				if r.exit.exiting || r.exit.returning || r.exit.fatalExit {
					break
				}
				if r.fireDebugTrap(ctx, cm) {
					break
				}
				// Check if the iteration variable is readonly before
				// attempting to assign. Bash reports an error and stops
				// the loop in this case; in POSIX mode the whole
				// non-interactive shell exits.
				if vr := r.lookupVar(name); vr.Kind == expand.NameRef {
					// Assigning the loop value through an as-yet
					// untargeted nameref retargets it; bash rejects
					// a value that isn't a valid identifier with the
					// bare "not a valid identifier" diagnostic.
					if vr.Str == "" && !validNameRefTarget(field) {
						r.errf("%s`%s': not a valid identifier\n",
							r.bashErrPrefix(r.curStmtPos), field)
						r.exit.code = 1
						continue
					}
					vr.Str = field
					r.setVar(name, vr)
				} else if r.lookupVar(name).ReadOnly {
					r.errf("%s%s: readonly variable\n",
						r.bashErrPrefix(r.curStmtPos), name)
					r.exit.code = 1
					if r.opts[optPosix] && !r.interactiveShell {
						r.exit.exiting = true
					}
					break
				} else {
					r.setVarString(name, field)
				}
				trace.stringf("for %s in", y.Name.Value)
				if inToken {
					for _, item := range y.Items {
						trace.string(" ")
						trace.expr(item)
					}
				} else {
					trace.string(` "$@"`)
				}
				trace.newLineFlush()
				if firstIter && forHadCmdSubst {
					r.exit.code = forExitCode
					r.lastExit.code = forExitCode
				}
				firstIter = false
				if r.loopStmtsBroken(ctx, cm.Do) {
					break
				}
			}
		case *syntax.CStyleLoop:
			// bash runs the DEBUG trap before evaluating the
			// arithmetic for-loop's expressions.
			if r.fireDebugTrap(ctx, cm) {
				break
			}
			// bash `set -x` traces each of the C-style for-loop
			// expressions as a separate `+ (( ... ))` line.
			traceArith := func(expr syntax.ArithmExpr) {
				if !tracingEnabled || expr == nil {
					return
				}
				var inner bytes.Buffer
				syntax.NewPrinter(syntax.SingleLine(true)).Print(&inner, &syntax.ArithmCmd{X: expr})
				rendered := inner.String()
				rendered = strings.TrimPrefix(rendered, "((")
				rendered = strings.TrimSuffix(rendered, "))")
				rendered = strings.TrimSpace(rendered)
				rendered = compactArithm(rendered)
				trace.string("(( ")
				trace.string(rendered)
				// bash 5.3 quirk: trailing `++` / `--` get a
				// double space before `))` in the xtrace line.
				if strings.HasSuffix(rendered, "++") || strings.HasSuffix(rendered, "--") {
					trace.string(" ")
				}
				trace.string(" ))")
				trace.newLineFlush()
			}
			// bash 5.3: a runtime arith error in init/cond/post
			// terminates the for-loop (and sets exit status 1).
			// Clear lastArithErr at each call site we care about so
			// we can detect *this* invocation's failure.
			r.lastArithErr = nil
			if y.Init != nil {
				traceArith(y.Init)
				r.arithm(y.Init)
				if r.lastArithErr != nil {
					r.exit.code = 1
					break
				}
			}
			for {
				r.lastArithErr = nil
				if y.Cond != nil {
					if r.fireDebugTrap(ctx, cm) {
						break
					}
					traceArith(y.Cond)
					if r.arithm(y.Cond) == 0 {
						break
					}
					if r.lastArithErr != nil {
						r.exit.code = 1
						break
					}
				}
				if r.exit.exiting || r.exit.returning || r.exit.fatalExit {
					break
				}
				if r.loopStmtsBroken(ctx, cm.Do) {
					break
				}
				if y.Post != nil {
					r.lastArithErr = nil
					if r.fireDebugTrap(ctx, cm) {
						break
					}
					traceArith(y.Post)
					r.arithm(y.Post)
					if r.lastArithErr != nil {
						r.exit.code = 1
						break
					}
				}
				if y.Cond == nil {
					// infinite loop; need an explicit break
					// path — already handled above.
				}
			}
		}
		// An errexit-exempt final body command (left of `&&`/`||`)
		// propagates its exemption to the loop's own non-zero status.
		if !r.exit.ok() && stmtsEndErrexitExempt(cm.Do) {
			r.exit.errexitExempt = true
		}
	case *syntax.FuncDecl:
		name := cm.Name.Value
		// Bash 5.3 defers non-identifier function-name checks from
		// parse time to runtime. `function sys$read` parses cleanly
		// (name = literal text "sys$read") and then errors here with
		// "<name>: not a valid identifier". Bash allows many chars
		// in function names (e.g. `+`, `@`, `foo-bar`), so only
		// reject names containing shell-special chars that bash
		// itself rejects (whitespace, $, ', ", (, ), etc.).
		if !validBashFuncName(name) {
			// Bash reports the failure at the end of the function
			// declaration (line of the closing brace), not at its
			// start, so use End().
			r.errf("%s`%s': not a valid identifier\n",
				r.bashErrPrefix(cm.End()), name)
			r.exit.code = 1
			return
		}
		// POSIX mode: defining a function whose name is a POSIX special
		// builtin (`break`, `return`, `export`, …) is a fatal error —
		// POSIX interpretation 383, which bash 5.3 enforces only when
		// posixly_correct (execute_cmd.c execute_intern_function sets the
		// special-builtin bit `if (posixly_correct)`, general.c
		// valid_function_word then errors). bashy targets POSIX
		// certification, so it MUST conform when posix is indicated; in
		// the default (non-posix) mode bash allows the definition, so this
		// is gated on optPosix. Reject before stashing in the func table.
		// Strict POSIX mode (yash -p builtins-p) instead requires the
		// definition to be accepted silently — no diagnostic, exit status
		// untouched — while invoking the name still runs the special
		// builtin (see the optPosix fall-through in Runner.call). Skip
		// the rejection and drop the definition on the floor.
		if r.opts[optPosix] && r.strictPosix && isPosixSpecialBuiltin(name) {
			return
		}
		if r.opts[optPosix] && isPosixSpecialBuiltin(name) {
			errPos := cm.End()
			if r.enclosingSubshellEnd.IsValid() {
				errPos = r.enclosingSubshellEnd
			}
			r.errf("%s`%s': is a special builtin\n",
				r.bashErrPrefix(errPos), name)
			r.exit.code = 1
			r.exit.exiting = true
			return
		}
		// Bash rejects redefinition of a readonly function with
		// `<file>: line N: NAME: readonly function`, reported at the
		// end of the rejected definition (closing brace), not its
		// start.
		if r.readonlyFuncs[name] {
			r.errf("%s%s: readonly function\n",
				r.bashErrPrefix(cm.End()), name)
			r.exit.code = 1
			return
		}
		var advised []DecoratorSpec
		if r.bashPPEnabled() {
			var ok bool
			if advised, ok = r.bashPPRegisterDecorators(name, cm.Pos(), cm.Decorators, cm.Agentic != nil); !ok {
				return
			}
		}
		r.setFunc(name, cm.Body)
		if cm.Agentic != nil {
			if r.bashPPAgenticFuncs == nil {
				r.bashPPAgenticFuncs = make(map[string]*syntax.Stmt)
			}
			r.bashPPAgenticFuncs[name] = cm.Body
			delete(r.exportedFuncs, name)
		}
		if len(cm.Decorators) > 0 || len(advised) > 0 {
			if r.bashPPDecoratedFuncs == nil {
				r.bashPPDecoratedFuncs = make(map[string]*bashPPDecorated)
			}
			r.bashPPDecoratedFuncs[name] = &bashPPDecorated{body: cm.Body, decorators: cm.Decorators, advised: advised}
			delete(r.exportedFuncs, name)
		}
	case *syntax.ArithmCmd:
		if r.fireDebugTrap(ctx, cm) {
			return
		}
		if tracingEnabled {
			// bash `set -x` traces `((expr))` as
			// `+ (( <printed-expr> ))` with spaces inside the
			// double-parens and the inner expression in
			// compact, no-space-around-operator form.
			var inner bytes.Buffer
			syntax.NewPrinter(syntax.SingleLine(true)).Print(&inner, cm)
			rendered := inner.String()
			rendered = strings.TrimPrefix(rendered, "((")
			rendered = strings.TrimSuffix(rendered, "))")
			rendered = strings.TrimSpace(rendered)
			if w, ok := cm.X.(*syntax.Word); ok && len(w.Parts) == 1 {
				if pe, ok := w.Parts[0].(*syntax.ParamExp); ok && pe.Dollar.IsValid() {
					rendered = " " + r.literal(w) + " "
				}
			}
			trace.string("(( ")
			trace.string(compactArithm(rendered))
			trace.string(" ))")
			trace.newLineFlush()
		}
		// A `(( ))` command that spans several physical lines reports
		// an arithmetic error against its *closing* `))` line, not the
		// opening `((` (arith.test.sh: `(( a = 3 + 4  # comment\n))`
		// errors on the `))` line). Point curStmtPos at the command's
		// end for the duration of the evaluation; single-line forms are
		// unaffected since start and end share a line.
		oldArithStmtPos := r.curStmtPos
		r.curStmtPos = cm.End()
		exitZero := r.arithm(cm.X) == 0
		r.curStmtPos = oldArithStmtPos
		r.exit.oneIf(exitZero)
	case *syntax.LetClause:
		var val int
		exprs := cm.Exprs
		if len(exprs) > 0 {
			if w, ok := exprs[0].(*syntax.Word); ok && r.literal(w) == "--" {
				exprs = exprs[1:]
			}
		}
		if len(exprs) == 0 {
			r.errf("%slet: expression expected\n", r.bashErrPrefix(cm.Pos()))
			r.exit.code = 1
			break
		}
		for _, expr := range exprs {
			val = r.letArithm(expr)

			if !tracingEnabled {
				continue
			}

			switch expr := expr.(type) {
			case *syntax.Word:
				qs, err := syntax.Quote(r.literal(expr), syntax.LangBash)
				if err != nil {
					return
				}
				trace.stringf("let %v", qs)
			case *syntax.BinaryArithm, *syntax.UnaryArithm:
				trace.expr(cm)
			case *syntax.ParenArithm:
				// TODO
			}
		}

		trace.newLineFlush()
		r.exit.oneIf(val == 0)
	case *syntax.CaseClause:
		if r.fireDebugTrap(ctx, cm) {
			return
		}
		trace.string("case ")
		trace.expr(cm.Word)
		trace.string(" in")
		trace.newLineFlush()
		// Case subject undergoes full quote-removal (per POSIX): an
		// unquoted `\X` collapses to `X` so it can be compared against
		// patterns that have their own backslash semantics.
		// Bash: inside the matched body, `$?` reflects the last command
		// substitution in the case subject word until a command in the
		// body actually runs — pattern matching does not change it. A
		// command substitution reports its status via lastExpandExit
		// (r.exit.code stays whatever it was before the case), so capture
		// it here before pattern evaluation overwrites the shared state.
		r.lastExpandExit = exitStatus{}
		r.lastExpandCmdSubst = false
		subj, err := expand.LiteralWithQuoteRemoval(r.ecfg, cm.Word)
		r.expandErr(err)
		subjectHadCmdSubst := r.lastExpandCmdSubst
		subjectExitCode := r.lastExpandExit.code
		str := subj
		noCaseMatch := false
		if opt, _ := r.bashOptByName("nocasematch"); opt != nil && *opt {
			noCaseMatch = true
		}
		// fallthrough is set when the previous item ended with `;&`,
		// meaning we run this item's stmts unconditionally.
		fallthroughActive := false
		for i, ci := range cm.Items {
			matched := fallthroughActive
			if !matched {
				for _, word := range ci.Patterns {
					prevCode := r.exit.code
					pat := r.pattern(word)
					// Bash: if pattern evaluation (e.g. an inner
					// `$((expr))` arithmetic) failed, bail the case
					// entirely with the resulting exit code rather
					// than running any branch.
					if r.exit.code != prevCode && r.exit.code != 0 {
						return
					}
					matchStr := str
					if noCaseMatch {
						pat = strings.ToLower(pat)
						matchStr = strings.ToLower(matchStr)
					}
					if r.match(pat, matchStr) {
						matched = true
						break
					}
				}
			}
			if !matched {
				continue
			}
			// Restore the case subject's `$?` (from a command substitution
			// in the subject word) for the body's first command, unless a
			// fallthrough already ran a prior body which leaves its own
			// status. `$?` reads r.lastExit, so update that. Matches bash.
			if subjectHadCmdSubst && !fallthroughActive {
				r.exit.code = subjectExitCode
				r.lastExit.code = subjectExitCode
			}
			// Each clause is its own block, and a `;&` fallthrough runs the
			// next one in a block of its own too — so the scope is pushed
			// inside the item loop and popped before the next item rather
			// than deferred to the end of the case.
			if r.bashPPScope != nil {
				leave := r.bashPPPushScope()
				r.stmts(ctx, ci.Stmts)
				leave()
			} else {
				r.stmts(ctx, ci.Stmts)
			}
			if !r.exit.ok() && stmtsEndErrexitExempt(ci.Stmts) {
				r.exit.errexitExempt = true
			}
			if r.loopControlPending() {
				return
			}
			switch ci.Op {
			case syntax.Fallthrough:
				// `;&` — fall into the next item's body unless we're
				// already at the last one.
				if i+1 < len(cm.Items) {
					fallthroughActive = true
					continue
				}
				return
			case syntax.Resume:
				// `;;&` — keep evaluating remaining patterns.
				fallthroughActive = false
				continue
			default:
				// `;;` (Break) or trailing-item-without-op: done.
				return
			}
		}
	case *syntax.TestClause:
		{
			var cmdBuf strings.Builder
			syntax.NewPrinter(syntax.SingleLine(true)).Print(&cmdBuf, cm)
			r.setVarString("BASH_COMMAND", strings.TrimRight(cmdBuf.String(), "\n"))
		}
		if r.fireDebugTrap(ctx, cm) {
			return
		}
		r.runTestClause(ctx, cm.Left, cm.X, true)
	case *syntax.DeclClause:
		if r.fireDebugTrap(ctx, cm) {
			return
		}
		if r.opts[optKeyword] && cm.Variant.Value == "export" && len(cm.Args) > 0 {
			allAssignments := true
			for _, as := range cm.Args {
				if as.Value == nil || as.Index != nil || as.Array != nil {
					allAssignments = false
					break
				}
			}
			if allAssignments {
				// In keyword mode these operands are temporary command
				// environment assignments, leaving bare `export`. Bash
				// prints the existing exported variables and leaves their
				// values unchanged. Expand each RHS for side effects.
				for _, as := range cm.Args {
					r.literalForAssign(as.Value)
				}
				r.applyLateRedirs(ctx, []string{"export"})
				r.printDeclareVars([]string{"-x"})
				return
			}
		}
		local, global := false, false
		var modes []string
		valType := ""
		declQuery := "" // "-f" or "-p" for query mode
		jsonMode := false
		switch cm.Variant.Value {
		case "declare", "typeset":
			// When used in a function, "declare"/"typeset" act as
			// "local" unless the "-g" option is used.
			local = r.inFunc
		case "local":
			if !r.inFunc {
				r.errf("%slocal: can only be used in a function\n", r.bashErrPrefix(r.curStmtPos))
				r.exit.code = 1
				return
			}
			local = true
		case "export":
			modes = append(modes, "-x")
		case "readonly":
			modes = append(modes, "-r")
		case "nameref":
			valType = "-n"
		}
		declHadNames := false
		declFuncInvalidOptReported := false
		optionsEnded := false
		oldDeclCtx := r.declAssignContext
		r.declAssignContext = true
		defer func() { r.declAssignContext = oldDeclCtx }()
		if r.lateRedirs != nil {
			// A declaration clause expands assignment RHS words as it
			// processes each operand. Bash applies redirects after those
			// expansions, so leave the redirect pending until the first
			// operand has had a chance to run command substitutions.
			defer r.applyLateRedirs(ctx, []string{cm.Variant.Value})
		}
	assignLoop:
		for as, fromString := range r.flattenAssigns(cm.Args) {
			// Bash attributes assignment failures from a declare-
			// family builtin's string-parsed arg path
			// (`readonly 'a=v'`) to the builtin rather than the
			// enclosing function. Set the runner-level flag so the
			// setVar error printer picks it up; an inner loop
			// guarantees we clear it once this Assign is done.
			r.setVarFromBuiltin = ""
			r.setVarStringParsed = false
			r.setVarArrayLiteral = false
			// Bash 5.3 attribution for declare-family failures:
			//   no -a/-A flag       → no extra prefix
			//   -a/-A + array literal → function name
			//   -a/-A + scalar (or string-form) → builtin name
			hasArrayFlag := slices.Contains(modes, "-a") || slices.Contains(modes, "-A") || valType == "-a" || valType == "-A"
			switch {
			case !hasArrayFlag:
				switch {
				case as.Array != nil && !fromString && cm.Variant.Value != "local" &&
					cm.Variant.Value != "readonly" && cm.Variant.Value != "export":
					// `declare foo+=(a b)` / `typeset foo=(a b)` without an
					// explicit -a/-A flag is an array literal: bash attributes
					// the failure to the function name, so at top level it
					// carries no builtin prefix. `local`, `readonly`, and
					// `export` have their own attribution rules.
					r.setVarArrayLiteral = true
				case cm.Variant.Value == "readonly" || cm.Variant.Value == "export":
					// For readonly and export, plain assignments get no prefix.
					r.setVarStringParsed = true // suppress any prefix
				default:
					// For declare/local/typeset, plain assignments get the
					// builtin prefix (these builtins are specifically for
					// variable manipulation, so errors are attributed to them).
					r.setVarFromBuiltin = cm.Variant.Value
				}
			case as.Array != nil && !fromString:
				r.setVarArrayLiteral = true // function-name attribution
			default:
				r.setVarFromBuiltin = cm.Variant.Value
			}
			if as.Name.Value == "--" && !optionsEnded {
				optionsEnded = true
				continue assignLoop
			}
			isFlag := !optionsEnded && (strings.HasPrefix(as.Name.Value, "-") || strings.HasPrefix(as.Name.Value, "+"))
			if isFlag {
				if !r.opts[optPosix] && as.Name.Value == "--json" {
					jsonMode = true
					continue assignLoop
				}
				if cm.Variant.Value == "local" && as.Name.Value == "-" {
					r.markLocalOpts()
					declHadNames = true
					continue assignLoop
				}
				fp := flagParser{remaining: []string{as.Name.Value}}
				for fp.more() {
					switch flag := fp.flag(); flag {
					case "-x", "+x":
						if cm.Variant.Value == "readonly" {
							r.errf("%sreadonly: %s: invalid option\n", r.bashErrPrefix(r.curStmtPos), flag)
							r.errf("readonly: usage: %s\n", bashUsage["readonly"])
							r.exit.code = 2
							return
						}
						modes = append(modes, flag)
					case "-r", "+r":
						modes = append(modes, flag)
					case "-a", "-A", "-n":
						// Bash accepts `readonly -n NAME` without
						// treating `-n` as nameref; it leaves the
						// existing readonly status alone.
						if cm.Variant.Value == "readonly" && flag == "-n" {
							break
						}
						// `export -n NAME` removes the export
						// attribute; it is not a nameref flag.
						if cm.Variant.Value == "export" && flag == "-n" {
							modes = append(modes, "+x")
							break
						}
						valType = flag
					case "-I":
						modes = append(modes, flag)
					case "-i":
						// `-i` is an attribute that can coexist
						// with `-a`/`-A` (indexed/assoc array).
						// Track via modes; the post-loop also
						// sets it as valType when no other type
						// flag took precedence so the integer-
						// aware assign path still fires for
						// `typeset -i x=4+5`.
						modes = append(modes, "-i")
						if valType == "" {
							valType = "-i"
						}
					case "+i", "+a", "+A":
						// `+X` removes attribute X. Tracked via modes
						// so setVar can clear the flag on the
						// destination variable.
						modes = append(modes, flag)
					case "+n":
						// `+n` strips the nameref attribute. Also
						// flagged via valType so assignVal targets
						// the nameref var itself, not the resolved
						// reference target.
						valType = "+n"
						modes = append(modes, flag)
					case "-u", "-l", "-c":
						// Case-conversion attributes (`declare -u/-l/-c`).
						// Tracked as additional modes; applied at assign
						// time via `setVar` and surfaced in `declare -p`
						// output via `expand.Variable.Flags`.
						modes = append(modes, flag)
					case "+u", "+l", "+c":
						modes = append(modes, flag)
					case "-t", "+t":
						// The trace attribute. For functions (`declare -ft`)
						// it is honoured: a traced function inherits the
						// DEBUG/RETURN traps. Tracked via modes so the -f
						// path sets it instead of printing the definition.
						modes = append(modes, flag)
					case "-g":
						global = true
					case "-f", "-F":
						declQuery = flag
						modes = append(modes, flag)
					case "-p":
						declQuery = flag
						modes = append(modes, flag)
					default:
						builtinName := cm.Variant.Value
						r.errf("%s%s: %s: invalid option\n", r.bashErrPrefix(r.curStmtPos), builtinName, flag)
						if usage, ok := bashUsage[builtinName]; ok && (builtinName == "declare" || builtinName == "typeset" || builtinName == "readonly") {
							r.errf("%s: usage: %s\n", builtinName, usage)
						}
						r.exit.code = 2
						return
					}
				}
				continue assignLoop
			}
			declHadNames = true
			name := as.Name.Value
			// `declare -f <name>` / `export -f <name>` operate on
			// function names; bash allows arbitrary function names
			// (e.g. `foo-a`) so skip the identifier check there.
			if declQuery == "-p" && as.Index == nil && !syntax.ValidName(name) {
				if base, _, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
					r.errf(r.bashErrPrefix(r.curStmtPos)+"%s: %s: not found\n", cm.Variant.Value, name)
					r.exit.code = 1
					continue
				}
			}
			if declQuery != "-f" && declQuery != "-F" && !syntax.ValidName(name) {
				builtinName := cm.Variant.Value
				if r.bashCompatErrors {
					r.errf("%s%s: `%s': not a valid identifier\n",
						r.bashErrPrefix(r.curStmtPos), builtinName, name)
				} else {
					r.errf("%s: invalid name %q\n", builtinName, name)
				}
				r.exit.code = 1
				// POSIX mode: readonly/export are special builtins, so
				// a bad identifier is fatal — bash stops at the first
				// offending name and exits.
				if r.opts[optPosix] && (builtinName == "readonly" || builtinName == "export") {
					r.exit.exiting = true
					return
				}
				continue
			}
			if cm.Variant.Value == "export" && as.Value == nil && (name == "BASHOPTS" || name == "SHELLOPTS") {
				vr := r.lookupVar(name)
				vr.Exported = true
				if overlay, ok := r.writeEnv.(*overlayEnviron); ok {
					overlay.touch()
					overlay.values[overlay.normalize(name)] = namedVariable{Name: name, Variable: vr}
				} else if r.writeEnv.Set(name, vr) != nil {
					r.exit.code = 1
				}
				continue
			}
			if declQuery == "-F" {
				// declare -F name: print just the function name.
				if body := r.Funcs[name]; body != nil {
					if jsonMode {
						r.exit = r.jsonOut(r.functionJSON(name))
					} else {
						if opt, _ := r.bashOptByName("extdebug"); opt != nil && *opt {
							filename := r.filename
							if filename == "" {
								filename = "bash"
							}
							r.outf("%s %d %s\n", name, body.Pos().Line(), filename)
						} else {
							r.outf("%s\n", name)
						}
					}
				} else {
					r.exit.code = 1
				}
				continue
			}
			if declQuery == "-f" {
				if bad := declareFuncInvalidOption(valType, modes); bad != "" {
					if !declFuncInvalidOptReported {
						r.errf("%sdeclare: %s: invalid option\n", r.bashErrPrefix(r.curStmtPos), bad)
						declFuncInvalidOptReported = true
					}
					r.exit.code = 2
					continue
				}
				if as.Value != nil || as.Array != nil || as.Index != nil {
					r.errf("%sdeclare: cannot use `-f' to make functions\n", r.bashErrPrefix(r.curStmtPos))
					r.exit.code = 1
					continue
				}
				// `export -f <name>` marks the function for export
				// to child processes via BASH_FUNC_<name>%%=…
				// envvar. Other `declare -f name` / `typeset -f
				// name` forms print the function definition. Bash
				// silently returns exit 1 for missing functions.
				if cm.Variant.Value == "export" {
					// Bash refuses to export functions whose name
					// can't survive the env-var round-trip — names
					// containing `=` or `/`, etc. — even when the
					// function itself does exist. The diagnostic
					// is `export: <name>: cannot export` and the
					// builtin keeps going (exit 1).
					if !validExportedFuncName(name) || r.bashPPAgenticFunc(name) || r.bashPPDecoratedShellFunc(name) != nil {
						r.errf("%sexport: %s: cannot export\n",
							r.bashErrPrefix(r.curStmtPos), name)
						r.exit.code = 1
						continue
					}
					if _, ok := r.Funcs[name]; !ok {
						r.errf("%sexport: %s: not a function\n",
							r.bashErrPrefix(r.curStmtPos), name)
						r.exit.code = 1
						continue
					}
					if r.exportedFuncs == nil {
						r.exportedFuncs = make(map[string]bool)
					}
					r.exportedFuncs[name] = true
					continue
				}
				if body := r.Funcs[name]; body != nil {
					if slices.Contains(modes, "+r") && r.readonlyFuncs[name] {
						r.errf("%sdeclare: %s: readonly function\n",
							r.bashErrPrefix(r.curStmtPos), name)
						r.exit.code = 1
						continue
					}
					if slices.Contains(modes, "-r") {
						if r.readonlyFuncs == nil {
							r.readonlyFuncs = make(map[string]bool)
						}
						r.readonlyFuncs[name] = true
						continue
					}
					// `readonly -f NAME` marks the function as
					// read-only (it can't be unset or redefined).
					if cm.Variant.Value == "readonly" {
						if r.readonlyFuncs == nil {
							r.readonlyFuncs = make(map[string]bool)
						}
						r.readonlyFuncs[name] = true
						continue
					}
					// `declare -ft NAME` sets (and `+t` clears) the trace
					// attribute: a traced function inherits the DEBUG and
					// RETURN traps. With an attribute flag present, declare
					// sets the attribute rather than printing the body.
					if slices.Contains(modes, "-t") || slices.Contains(modes, "+t") {
						if slices.Contains(modes, "+t") {
							delete(r.funcTrace, name)
						} else {
							if r.funcTrace == nil {
								r.funcTrace = make(map[string]bool)
							}
							r.funcTrace[name] = true
						}
						continue
					}
					r.printFuncDecl(name, body)
				} else {
					r.exit.code = 1
				}
				continue
			}
			if declQuery == "-p" {
				queryName := name
				if as.Value != nil {
					queryName += "=" + r.literalForAssign(as.Value)
				}
				if as.Index != nil {
					ref := r.inlineArrayAssignName(as)
					r.errf(r.bashErrPrefix(r.curStmtPos)+"%s: %s: not found\n", cm.Variant.Value, ref)
					r.exit.code = 1
					continue
				}
				if slices.Contains(modes, "-f") || slices.Contains(modes, "-F") {
					if body := r.Funcs[name]; body != nil {
						if slices.Contains(modes, "-F") {
							r.outf("%s\n", name)
						} else {
							r.printFuncDecl(name, body)
						}
						continue
					}
				}
				// `local -p NAME` only prints variables local to
				// the current function scope.
				var currentLocal expand.Variable
				if cm.Variant.Value == "local" {
					ol, ok := r.writeEnv.(*overlayEnviron)
					if !ok {
						r.errf(r.bashErrPrefix(r.curStmtPos)+"local: %s: not found\n", name)
						r.exit.code = 1
						continue
					}
					var okLocal bool
					currentLocal, okLocal = ol.currentLocalVar(name)
					if !okLocal {
						r.errf(r.bashErrPrefix(r.curStmtPos)+"local: %s: not found\n", name)
						r.exit.code = 1
						continue
					}
				}
				if cm.Variant.Value == "local" {
					r.outf("%s\n", formatDeclareVar(name, currentLocal, false))
					continue
				}
				// declare -p name: print variable with attributes. With
				// explicit names bash 5.3 ignores the -n/-r/-x/-a/-A/-i
				// attribute flags for selection — those only filter the
				// no-name listing — so every named variable that exists
				// is printed regardless of its own attributes.
				vr := r.lookupVar(name)
				if !vr.Declared() {
					if cm.Variant.Value != "readonly" && cm.Variant.Value != "export" {
						r.errf(r.bashErrPrefix(r.curStmtPos)+"%s: %s: not found\n", cm.Variant.Value, queryName)
						r.exit.code = 1
					}
					continue
				}
				// `readonly -p NAME` / `export -p NAME` print nothing when
				// explicit names are supplied; only their no-name form lists
				// matching variables. They still apply their attribute to
				// existing operands, so `readonly -p local; export -p local;
				// local -p local` shows the local as `declare -rx`.
				if cm.Variant.Value == "readonly" || cm.Variant.Value == "export" {
					if o, ok := r.writeEnv.(*overlayEnviron); ok {
						o.setAttrsBypassReadonly(name, func(vr *expand.Variable) {
							if cm.Variant.Value == "readonly" {
								vr.ReadOnly = true
							} else {
								vr.Exported = true
							}
						})
					} else {
						if cm.Variant.Value == "readonly" {
							vr.ReadOnly = true
						} else {
							vr.Exported = true
						}
						r.setVar(name, vr)
					}
					continue
				}
				// An integer nameref whose value assignment was rejected
				// (`declare -n foo; declare -i foo; foo=7*6`, where the
				// "target" 7*6 is not a valid identifier) stays invisible
				// in bash: the integer-assignment path leaves it set but
				// without a usable target, and `declare -p` skips it
				// silently — no value, no "not found". A plain (non-integer)
				// rejected nameref like `declare -n r; r=^` still prints
				// `declare -n r`, and an unassigned integer nameref
				// (`declare -xi foo6`, Set=false) still prints its flags.
				if vr.Kind == expand.NameRef && vr.Integer && vr.Set && vr.Str == "" {
					continue
				}
				if jsonMode {
					r.exit = r.jsonOut(variableJSON(name, vr))
				} else {
					r.outf("%s\n", formatDeclareVar(name, vr, false))
				}
				continue
			}
			vr := r.lookupVar(name)
			if global {
				vr = r.lookupGlobalVar(name)
			}
			if local && !global && !slices.Contains(modes, "-I") && !r.tempEnv[name] &&
				!r.bashShoptEnabled("localvar_inherit") {
				if ol, ok := r.writeEnv.(*overlayEnviron); ok && ol.funcScope && !ol.holdsLocally(name) {
					vr = expand.Variable{}
				}
			}
			arrayElemDeclareOnNameref := as.Index != nil && vr.Kind == expand.NameRef &&
				(valType == "-a" || valType == "-A" || slices.Contains(modes, "-a") || slices.Contains(modes, "-A"))
			if valType != "-n" && (valType != "+n" || !as.Naked) {
				resolveForAttr := valType == "-i" || valType == "-a" || valType == "-A"
				for _, mode := range modes {
					switch mode {
					case "-i", "+i", "-r", "+r", "-x", "+x", "-u", "+u", "-l", "+l", "-c", "+c", "-a", "-A":
						resolveForAttr = true
					}
				}
				if resolveForAttr && !arrayElemDeclareOnNameref {
					if n, resolved := vr.Resolve(r.writeEnv); n != "" {
						name, vr = n, resolved
					}
				}
			}
			if (cm.Variant.Value == "readonly" || cm.Variant.Value == "export") && as.Index == nil {
				if base, _, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
					r.errf("%s%s: `%s': not a valid identifier\n",
						r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
					r.exit.code = 1
					continue
				}
			}
			// Set the Integer attribute *before* assignVal so the
			// initial assignment can evaluate the RHS as arithmetic.
			if valType == "-i" || slices.Contains(modes, "-i") {
				vr.Integer = true
			}
			if slices.Contains(modes, "+i") {
				vr.Integer = false
			}
			if as.Naked && (slices.Contains(modes, "+a") || slices.Contains(modes, "+A")) &&
				(vr.Kind == expand.Indexed || vr.Kind == expand.Associative) {
				if vr.ReadOnly {
					r.errf("%sdeclare: %s: readonly variable\n",
						r.bashErrPrefix(r.curStmtPos), name)
					r.exit.code = 1
					continue
				}
				r.errf("%sdeclare: %s: cannot destroy array variables in this way\n",
					r.bashErrPrefix(r.curStmtPos), name)
				r.exit.code = 1
				continue
			}
			if as.Naked {
				if as.Index != nil {
					if valType == "-n" {
						r.errf("%s%s: %s: reference variable cannot be an array\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, r.inlineArrayAssignName(as))
						r.exit.code = 1
						continue
					}
					if cm.Variant.Value == "readonly" || cm.Variant.Value == "export" {
						ref := name
						if w, ok := as.Index.(*syntax.Word); ok {
							ref += "[" + r.literal(w) + "]"
						}
						builtinName := cm.Variant.Value
						r.errf("%s%s: `%s': not a valid identifier\n",
							r.bashErrPrefix(r.curStmtPos), builtinName, ref)
						r.exit.code = 1
						if r.opts[optPosix] {
							r.exit.exiting = true
							return
						}
						continue
					}
					if vr.Kind == expand.String && vr.Set {
						vr.List = []string{vr.Str}
						vr.ListSet = nil
						vr.Str = ""
					}
					vr.Kind = expand.Indexed
				}
				// A plain `declare NAME` (no value, no attribute flags) on
				// an existing nameref follows the reference and declares
				// its target instead — e.g. inside a function this makes
				// the target a local variable, so a later write through
				// the nameref stays local (nameref20.sub line 54).
				plainNameRefDecl := valType == "" && len(modes) == 0 &&
					vr.Kind == expand.NameRef && as.Value == nil
				if valType != "-n" && valType != "+n" &&
					(valType == "-a" || valType == "-A" || slices.Contains(modes, "-a") || slices.Contains(modes, "-A") || plainNameRefDecl) {
					if n, resolved := vr.Resolve(r.writeEnv); n != "" {
						name, vr = n, resolved
					}
				}
				switch valType {
				case "-A":
					if cm.Variant.Value == "readonly" && !vr.Declared() &&
						as.Value == nil && as.Array == nil && as.Index == nil {
						vr.Kind = expand.String
					} else {
						// `declare -A NAME` (no value) declares an
						// empty associative array; the variable is
						// "declared" but `[[ -v NAME ]]` is still
						// false until an element is assigned.
						vr.Kind = expand.Associative
					}
				case "-a":
					if cm.Variant.Value == "readonly" && !vr.Declared() &&
						as.Value == nil && as.Array == nil && as.Index == nil {
						vr.Kind = expand.String
					} else {
						// `declare -a NAME` (no value) declares an
						// empty indexed array even when NAME was
						// previously unset. Like -A, this is a
						// declaration without setting the value.
						vr.Kind = expand.Indexed
						if vr.Set && vr.List == nil {
							vr.List = []string{vr.Str}
							vr.ListSet = nil
							vr.Str = ""
						}
					}
				case "-n":
					if vr.ReadOnly {
						// Adding the nameref attribute to an existing
						// readonly variable is rejected like any other
						// attempt to modify it.
						r.errf("%s%s: %s: readonly variable\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
						r.exit.code = 1
						continue
					}
					if vr.Kind == expand.Indexed || vr.Kind == expand.Associative {
						r.errf("%s%s: %s: reference variable cannot be an array\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
						r.exit.code = 1
						continue
					}
					if vr.Kind == expand.String && vr.Str == name {
						if local && !global {
							prefix := r.bashErrPrefix(r.curStmtPos)
							r.errf("%s%s: warning: %s: circular name reference\n",
								prefix, cm.Variant.Value, name)
							r.errf("%swarning: %s: circular name reference\n",
								prefix, name)
						} else {
							r.errf("%s%s: %s: nameref variable self references not allowed\n",
								r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
							r.exit.code = 1
							continue
						}
					}
					if vr.Kind == expand.String && (vr.Str != "" || vr.Set) && !validNameRefTarget(vr.Str) {
						r.errf("%s%s: `%s': invalid variable name for name reference\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, vr.Str)
						r.exit.code = 1
						continue
					}
					// `typeset -n NAME` (no value) on an
					// existing var converts it to a nameref
					// pointing at whatever its current value
					// names (bash 5.3). Preserve the scalar
					// value as the reference target.
					vr.Kind = expand.NameRef
				case "+n":
					if vr.Kind == expand.NameRef {
						vr.Kind = expand.String
					}
				default:
					if as.Index != nil {
						vr.Kind = expand.Indexed
					} else if slices.Contains(modes, "-I") {
						// Keep the value and attributes found in an
						// outer scope for `local -I NAME`.
					} else if !vr.Declared() {
						vr.Kind = expand.String
					} else {
						vr.Kind = expand.KeepValue
					}
				}
			} else {
				prevForIndex := vr
				if arrayElemDeclareOnNameref {
					r.errf("%swarning: %s: removing nameref attribute\n",
						r.bashErrPrefix(r.curStmtPos), name)
					if valType == "-A" || slices.Contains(modes, "-A") {
						vr = expand.Variable{Kind: expand.Associative}
					} else {
						vr = expand.Variable{Kind: expand.Indexed}
					}
					prevForIndex = vr
				}
				if valType == "-n" {
					if as.Index != nil || as.Array != nil {
						ref := name
						if as.Index != nil {
							ref = r.inlineArrayAssignName(as)
						}
						r.errf("%s%s: %s: reference variable cannot be an array\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, ref)
						r.exit.code = 1
						if as.Array != nil {
							_, arr := r.assignVal(name, vr, as, "-a")
							r.setVar(name, arr)
						}
						continue
					}
					if as.Value == nil {
						r.errf("%s%s: `%s': not a valid identifier\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, "")
						r.exit.code = 1
						continue
					} else {
						target := r.literalForAssign(as.Value)
						if as.Append {
							target = vr.Str + target
						}
						if !validNameRefTarget(target) {
							// An empty nameref value (`declare -n r=""`)
							// is reported as a plain invalid identifier;
							// a non-empty but malformed one keeps the
							// "invalid variable name for name reference"
							// wording.
							if target == "" {
								r.errf("%s%s: `': not a valid identifier\n",
									r.bashErrPrefix(r.curStmtPos), cm.Variant.Value)
							} else {
								r.errf("%s%s: `%s': invalid variable name for name reference\n",
									r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, target)
							}
							r.exit.code = 1
							continue
						}
						if target == name {
							if local && !global {
								prefix := r.bashErrPrefix(r.curStmtPos)
								r.errf("%s%s: warning: %s: circular name reference\n",
									prefix, cm.Variant.Value, name)
								r.errf("%swarning: %s: circular name reference\n",
									prefix, name)
							} else {
								if as.Append {
									r.errf("%s%s: nameref variable self references not allowed\n",
										r.bashErrPrefix(r.curStmtPos), name)
								} else {
									r.errf("%s%s: %s: nameref variable self references not allowed\n",
										r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
								}
								r.exit.code = 1
								continue
							}
						}
						if vr.Kind == expand.Indexed || vr.Kind == expand.Associative {
							r.errf("%s%s: %s: reference variable cannot be an array\n",
								r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
							r.exit.code = 1
							continue
						}
						if slices.Contains(modes, "-i") {
							if base, _, ok := splitArrayRef(target); ok && syntax.ValidName(base) {
								tgt := r.lookupVar(base)
								tgt.Integer = true
								r.setVar(base, tgt)
								continue
							}
						}
					}
				}
				if valType == "" && vr.Kind == expand.NameRef && vr.Str == "" && vr.ReadOnly && as.Value != nil {
					r.errf("%s%s: %s: readonly variable\n",
						r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
					r.exit.code = 1
					continue
				}
				if as.Index != nil && valType == "-A" {
					prevForIndex.Kind = expand.Associative
				}
				if as.Index != nil && cm.Variant.Value == "declare" && valType != "-A" {
					spacedOpen := as.Index.Pos().Offset() > as.Name.End().Offset()+1
					spacedClose := as.Value != nil && as.Value.Pos().Offset() > as.Index.End().Offset()+2
					if spacedOpen || spacedClose {
						// Bash does not treat whitespace-bearing
						// array subscripts as a single declare
						// operand, but the arithmetic subscript is
						// still evaluated and can mutate the array.
						r.arithmCompoundArrayIndex(as.Index)
						r.errf("%s%s: `%s[': not a valid identifier\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
						tail := "]"
						if as.Value != nil {
							tail += "=" + r.literalForAssign(as.Value)
						}
						r.errf("%s%s: `%s': not a valid identifier\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, tail)
						r.exit.code = 1
						continue
					}
				}
				if as.Index != nil && (vr.Kind == expand.Associative || valType == "-A") {
					if w, ok := as.Index.(*syntax.Word); ok {
						key := r.assocAssignKey(w)
						// A subscript that closes its bracket early (`foo]bar`)
						// is always invalid. A dangling open bracket (`foo[bar`)
						// is only rejected without assoc_expand_once: with it
						// set, the once-expanded subscript is taken literally and
						// the stray `[` survives as part of the key.
						expandOnce := false
						if opt, _ := r.bashOptByName("assoc_expand_once"); opt != nil {
							expandOnce = *opt
						}
						closesEarly, unbalancedOpen := assocSubscriptBracketIssue(key)
						if closesEarly || (!expandOnce && unbalancedOpen) {
							assignForm := name + "[" + key + "]"
							if as.Value != nil {
								assignForm += "=" + r.literalForAssign(as.Value)
							}
							r.errf("%s%s: `%s': not a valid identifier\n",
								r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, assignForm)
							r.exit.code = 1
							continue
						}
					}
				}
				if as.Index != nil && valType == "-a" && as.Value != nil {
					if s := r.literalForAssign(as.Value); strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
						if file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("x="+s), ""); err == nil &&
							len(file.Stmts) == 1 {
							if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 1 && call.Assigns[0].Array != nil {
								_, arr := r.assignVal(name, vr, call.Assigns[0], valType)
								r.setVar(name, arr)
								continue
							}
						}
					}
				}
				if as.Index != nil && valType == "" && as.Value != nil && !vr.Declared() {
					if s := r.literalForAssign(as.Value); strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
						r.errf("%swarning: %s[%s]=%s: quoted compound array assignment deprecated\n",
							r.bashErrPrefix(r.curStmtPos), name, r.arithmSourceText(as.Index, false), s)
					}
				}
				name, vr = r.assignVal(name, vr, as, valType)
				if valType == "-n" && !slices.Contains(modes, "-i") {
					vr.Integer = false
				}
				if as.Index != nil {
					if prevForIndex.Kind == expand.NameRef {
						if n, resolved := prevForIndex.Resolve(r.writeEnv); n != "" {
							prevForIndex = resolved
						}
					}
					// Attributes (-i, -u, -l, -c) apply to an existing array
					// variable even when the element assignment fails on a bad
					// subscript; bash persists them first (`declare -i
					// a[$badsub]=v` leaves `a` integer-tagged).
					if (prevForIndex.Integer || prevForIndex.Upper || prevForIndex.Lower || prevForIndex.Capitalize) &&
						(prevForIndex.Kind == expand.Indexed || prevForIndex.Kind == expand.Associative) {
						r.setVar(name, prevForIndex)
					}
					r.setVarWithIndex(prevForIndex, name, as.Index, vr, as.Append)
					continue
				}
			}
			// `typeset +n NAME=value` semantics: the assignment
			// goes to the nameref's resolved target (handled
			// above), then the nameref attribute is stripped
			// from the ORIGINAL name. The original keeps its
			// value (the target name), e.g. `foo→bar` →
			// `foo="bar"` after `+n foo=...`.
			if valType == "+n" {
				origName := as.Name.Value
				if origName != name {
					r.setVar(name, vr) // assign target first
					orig := r.lookupVar(origName)
					if orig.Kind == expand.NameRef {
						orig.Kind = expand.String
						r.setVar(origName, orig)
					}
					continue
				}
			}
			if global {
				vr.Local = false
			} else if local {
				vr.Local = true
				// `typeset OPTIND=N` (or `local OPTIND=N`) inside a
				// function resets bash's getopts internal pointers
				// to track the new value. Without this, the
				// caller's char-position state stays around and
				// reads into the new argv at the old offset, which
				// loops forever in recursive-getopts patterns.
				if name == "OPTIND" {
					r.optState = getopts{}
				}
			}
			// A channel capability is an in-process authority, not an
			// environment value. Refuse export after evaluating the RHS but
			// before applying the export attribute or mutating the environment.
			// The typed cell check covers `export ch`; the exact active-handle
			// scan is defense in depth for `export saved=$ch` and concatenation.
			if cm.Variant.Value == "export" {
				cell := r.bashPPScope.lookup(as.Name.Value).view()
				if (cell != nil && cell.channel != nil) || r.bashPPVariableHasRuntimeHandle(vr) {
					r.errf("%sexport: channel capabilities cannot be exported\n", r.bashErrPrefix(r.curStmtPos))
					r.exit.code = 1
					continue
				}
			}
			for _, mode := range modes {
				switch mode {
				case "-x":
					vr.Exported = true
				case "+x":
					vr.Exported = false
				case "-r":
					vr.ReadOnly = true
				case "+r":
					// Bash refuses to remove the readonly
					// attribute once set; report error if readonly.
					if vr.ReadOnly {
						if cur := r.lookupVar(name); cur.Kind == expand.NameRef && cur.Str == "" {
							continue
						}
						r.errf("%s%s: %s: readonly variable\n",
							r.bashErrPrefix(r.curStmtPos), cm.Variant.Value, name)
						r.exit.code = 1
						continue assignLoop
					}
				case "-u":
					vr.Upper, vr.Lower, vr.Capitalize = true, false, false
				case "+u":
					vr.Upper = false
				case "-l":
					vr.Upper, vr.Lower, vr.Capitalize = false, true, false
				case "+l":
					vr.Lower = false
				case "-c":
					vr.Upper, vr.Lower, vr.Capitalize = false, false, true
				case "+c":
					vr.Capitalize = false
				case "+i":
					vr.Integer = false
				case "+n":
					// `+n` strips the nameref attribute and
					// returns the variable to a plain scalar
					// whose value is whatever the nameref was
					// pointing at.
					if vr.Kind == expand.NameRef {
						vr.Kind = expand.String
					}
				}
			}
			// Apply case-conversion attributes to the current value
			// so `declare -u foo; foo=$TEXT` immediately stores the
			// folded form.
			applyCaseAttr(&vr)
			// `typeset -n NAME=target` on an existing readonly NAME
			// is silently a no-op in bash 5.3 (the nameref conversion
			// is treated as a type change, not an assignment, so the
			// readonly attribute doesn't block it; but since we don't
			// model nameref-with-readonly conversions fully, leave
			// the prior readonly scalar untouched and skip the error).
			if valType == "-n" {
				if prev := r.lookupVar(name); prev.ReadOnly {
					continue
				}
			}
			if as.Naked && cm.Variant.Value == "readonly" && r.lookupVar(name).ReadOnly {
				continue
			}
			if global {
				if r.rejectDeclareConversion(name, r.lookupGlobalVar(name), vr) {
					continue
				}
				r.setGlobalVar(name, vr)
			} else {
				if (cm.Variant.Value == "readonly" || cm.Variant.Value == "export") &&
					vr.Local {
					if ol, ok := r.writeEnv.(*overlayEnviron); ok && ol.funcScope && !ol.holdsLocally(name) {
						if p, ok := ol.parent.(*overlayEnviron); ok {
							if found, err := p.setNearestLocal(name, vr); found {
								if err != nil {
									r.errf("%s%s: %v\n", r.bashErrPrefix(r.curStmtPos), name, err)
									r.exit.code = 1
									if r.opts[optPosix] && !r.interactiveShell && !r.opts[optRestricted] {
										r.exit.exiting = true
									}
								}
								continue
							}
						}
					}
				}
				r.setVar(name, vr)
				if (cm.Variant.Value == "readonly" || cm.Variant.Value == "export") &&
					!r.exit.ok() && r.opts[optPosix] && !r.interactiveShell && !r.opts[optRestricted] {
					r.exit.exiting = true
				}
			}
		}
		// Handle declare -F/-f with no arguments: list all functions.
		// Bash sorts the listing by function name. `readonly -f`
		// (no args) lists only read-only functions, each suffixed
		// with `declare -fr NAME`.
		if !declHadNames && (declQuery == "-F" || declQuery == "-f") {
			readonlyOnly := cm.Variant.Value == "readonly" ||
				slices.Contains(modes, "-r")
			exportedOnly := slices.Contains(modes, "-x")
			if jsonMode {
				r.exit = r.jsonOut(map[string]any{
					"functions": r.functionsJSON(readonlyOnly, exportedOnly),
				})
				return
			}
			names := make([]string, 0, len(r.Funcs))
			for name := range r.Funcs {
				if readonlyOnly && !r.readonlyFuncs[name] {
					continue
				}
				if exportedOnly && !r.exportedFuncs[name] {
					continue
				}
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				if declQuery == "-F" {
					// `declare -F` (no NAMEs) outputs
					// `declare -f NAME` (or `declare -fr` when
					// readonly) per function. Per-name lookup
					// `declare -F NAME` is handled elsewhere.
					if readonlyOnly {
						r.outf("declare -fr %s\n", name)
					} else {
						r.outf("declare -f %s\n", name)
					}
					continue
				}
				r.printFuncDecl(name, r.Funcs[name])
				if readonlyOnly {
					r.outf("declare -fr %s\n", name)
				}
			}
		}
		if !declHadNames && declQuery == "-p" && jsonMode {
			r.exit = r.jsonOut(map[string]any{"variables": r.variablesJSON(false)})
			return
		}
		// `declare -p` with an attribute-selecting flag but no names
		// filters the listing to variables carrying that attribute
		// (bash: `declare -pn` lists only namerefs, `declare -pr` only
		// readonly, etc.). The -n/-i selectors live in valType, so fold
		// them into the mode filter; -a/-A are handled by the dedicated
		// array path below, so skip the generic listing for those.
		if !declHadNames && declQuery == "-p" && !jsonMode && cm.Variant.Value == "declare" && valType != "-a" && valType != "-A" {
			r.printDeclareVars(declareListFilterModes(modes, valType))
		}
		if !declHadNames && declQuery == "-p" && !jsonMode && cm.Variant.Value == "readonly" {
			r.printReadonlyVars(r.opts[optPosix])
		}
		if !declHadNames && declQuery == "-p" && !jsonMode && cm.Variant.Value == "export" && valType != "-a" && valType != "-A" {
			r.printDeclareVars(declareListFilterModes(append(slices.Clone(modes), "-x"), valType))
		}
		if !declHadNames && declQuery == "" && valType == "" && len(modes) == 1 && !jsonMode && cm.Variant.Value == "export" && r.opts[optPosix] {
			r.printExportVars()
		}
		if !declHadNames && declQuery == "" && valType == "" && len(modes) == 1 && !jsonMode && cm.Variant.Value == "readonly" && r.opts[optPosix] {
			r.printReadonlyVars(true)
		}
		// Bash `local` with no args lists every variable local to
		// the current function scope in `name=value` form (arrays
		// rendered as `name=([0]="..." [1]="...")`).
		if !declHadNames && cm.Variant.Value == "local" && r.inFunc {
			r.printLocalVars()
		}
		if !declHadNames && declQuery == "" && valType == "" && len(modes) == 0 && !jsonMode && cm.Variant.Value == "declare" {
			r.printSetVars()
			names := make([]string, 0, len(r.Funcs))
			for name := range r.Funcs {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				r.printFuncDecl(name, r.Funcs[name])
			}
		}
		if !declHadNames && declQuery == "" && valType == "" && len(modes) == 1 && !jsonMode && cm.Variant.Value == "export" && !r.opts[optPosix] {
			r.printDeclareVars(modes)
		}
		if !declHadNames && declQuery == "" && valType == "" && len(modes) == 1 && !jsonMode && cm.Variant.Value == "readonly" && !r.opts[optPosix] {
			r.printDeclareVars(modes)
		}
		// `typeset -n` / `declare -n` with no name lists every
		// nameref variable in `declare -n NAME="target"` form.
		if !declHadNames && valType == "-n" && declQuery == "" {
			r.printNamerefVars()
		}
		// `declare -A` / `declare -a` with no name lists every
		// array variable of that kind, including bash's built-in
		// arrays (BASH_ALIASES, BASH_CMDS, BASH_ARGC, …).
		if !declHadNames && (valType == "-A" || valType == "-a") && (declQuery == "" || declQuery == "-p") {
			readonlyOnly := cm.Variant.Value == "readonly" || slices.Contains(modes, "-r")
			readonlyKeyword := cm.Variant.Value == "readonly" && r.opts[optPosix]
			r.printArrayVars(valType, readonlyOnly, readonlyKeyword)
		}
		// Clear the builtin attribution flags so they don't leak to
		// subsequent commands.
		r.setVarFromBuiltin = ""
		r.setVarStringParsed = false
		r.setVarArrayLiteral = false
	case *syntax.TimeClause:
		// bash POSIX mode (bashref "Bash POSIX Mode" #6): `time` used
		// by itself, with no command or pipeline to time, reports the
		// shell's cumulative user/system times rather than timing an
		// empty command. The `-p` form (PosixFormat) keeps the regular
		// timing report of an empty command, matching bash.
		if cm.Stmt == nil && !cm.PosixFormat && r.opts[optPosix] {
			r.posixTimesReport()
			break
		}
		// bash 5.3 only prints timing output for the outermost
		// `time` keyword in a stack of nested `time` clauses;
		// inner ones are absorbed by the outer measurement.
		priorInTimeClause, priorTiming := r.inTimeClause, r.timing
		outer := !priorInTimeClause
		r.inTimeClause = true
		defer func() {
			r.inTimeClause = priorInTimeClause
			r.timing = priorTiming
		}()
		// The outermost clause owns the CPU accounting. It samples the
		// shell process's own CPU (getrusage RUSAGE_SELF / GetProcessTimes)
		// around the timed clause — which covers builtins and the
		// goroutine-based subshells this pure-Go runner uses — and installs
		// a shared scope into which every external child's CPU is folded as
		// it is reaped (see accumulateChildCPU). Nested clauses reuse the
		// same scope, so no child's CPU is counted twice.
		var (
			scope               *timingScope
			selfUser0, selfSys0 time.Duration
			selfSampled         bool
		)
		if outer {
			scope = &timingScope{}
			r.timing = scope
			selfUser0, selfSys0, selfSampled = processCPUTimes()
		}
		start := time.Now()
		if cm.Stmt != nil {
			r.stmt(ctx, cm.Stmt)
		}
		real := time.Since(start)
		if !outer {
			break
		}
		var user, sys time.Duration
		// Shell-process CPU delta (fail-closed: only counted if both
		// samples succeeded; on unsupported targets this stays zero and we
		// report only the external-child CPU rather than a fabricated
		// figure).
		if selfSampled {
			if selfUser1, selfSys1, ok := processCPUTimes(); ok {
				if d := selfUser1 - selfUser0; d > 0 {
					user += d
				}
				if d := selfSys1 - selfSys0; d > 0 {
					sys += d
				}
			}
		}
		// External-child CPU accumulated during the clause.
		childUser, childSys := scope.total()
		user += childUser
		sys += childSys
		// Bash writes the `time` report to the current standard error
		// (fd 2), so a preceding `exec 2>...` redirect affects it.
		decpoint := r.localeDecimalPoint()
		if cm.PosixFormat {
			r.errf("real %s\n", elapsedString(real, true, decpoint))
			r.errf("user %s\n", elapsedString(user, true, decpoint))
			r.errf("sys %s\n", elapsedString(sys, true, decpoint))
		} else if tf := r.lookupVar("TIMEFORMAT"); tf.Set {
			format := tf.String()
			output := formatTIMEFORMAT(format, real, user, sys, decpoint)
			if output != "" {
				r.errf("%s\n", output)
			}
		} else {
			r.errf("\nreal\t%s\nuser\t%s\nsys\t%s\n",
				elapsedString(real, false, decpoint),
				elapsedString(user, false, decpoint),
				elapsedString(sys, false, decpoint))
		}
	case *syntax.CoprocClause:
		// Coproc runs a command in the background with stdin/stdout connected via pipes.
		// Note: bash coproc exposes the child's pipes as ${NAME[0]} / ${NAME[1]},
		// which are numeric fds usable in `<&N` / `>&N` redirects. This runner's
		// redirect layer only handles fds 0/1/2, so those redirect forms won't
		// work yet, but the basic coproc + read/write via the pipe object works.
		// A named coproc must use a valid identifier; bash rejects e.g.
		// `coproc @ { :; }` before launching anything.
		if cm.Name != nil {
			if nm := r.literal(cm.Name); !syntax.ValidName(nm) {
				r.errf("%s`%s': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos), nm)
				r.exit.code = 1
				break
			}
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			r.exit.fatal(err)
			break
		}
		pr2, pw2, err := os.Pipe()
		if err != nil {
			pr.Close()
			pw.Close()
			r.exit.fatal(err)
			break
		}
		r2 := r.subshell(true)
		r2.stdin = pr2
		r2.stdout = pw

		// Set COPROC array with read and write file descriptor numbers.
		// Also register them in fdTable so `<&"${COPROC[0]}"` and
		// `>&"${COPROC[1]}"` work in scripts that use the array. Without
		// this, the redirect layer would see the numeric arg, fail the
		// fd lookup, and return "bad fd number".
		varName := "COPROC"
		if cm.Name != nil {
			varName = r.literal(cm.Name)
		}
		// Bash moves the coproc pipe ends to high fds via
		// move_to_high_fd(fd, 1, 64) (general.c sh_openpipe), so the
		// `${NAME[0]}` / `${NAME[1]}` the script sees count down from 63
		// rather than the raw pipe fds. Mirror that numbering and register
		// the real *os.File objects under those logical fds; the redirect
		// layer keys off fdTable, not the OS fd, so any number works.
		readFd, writeFd := r.coprocHighFds()
		coprocReadonly := ""
		// pidBase is the effective base name bash builds `<NAME>_PID` from
		// (the nameref target when the coproc name resolves through one);
		// empty when the name was invalid and no variables were bound.
		pidBase := ""
		if prev := r.lookupVar(varName); prev.Kind == expand.NameRef {
			if _, _, ok := splitArrayRef(prev.Str); ok {
				r.errf("%s`%s': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos), prev.Str)
			} else if prev.Str == "" {
				// Empty-target nameref: bash can't use it as a reference,
				// so it drops the nameref attribute and binds the fd array
				// to the variable itself, warning first.
				r.errf("%swarning: %s: removing nameref attribute\n",
					r.bashErrPrefix(r.curStmtPos), varName)
				if r.coprocSetVar(varName, readFd, writeFd) {
					coprocReadonly = varName
				}
				pidBase = varName
			} else {
				// Write the coproc array through the nameref to its
				// target, leaving the nameref attribute intact.
				target := varName
				if n, _ := prev.Resolve(r.writeEnv); n != "" {
					target = n
				}
				if r.coprocSetVar(target, readFd, writeFd) {
					coprocReadonly = target
				}
				pidBase = target
			}
		} else {
			if r.coprocSetVar(varName, readFd, writeFd) {
				coprocReadonly = varName
			}
			pidBase = varName
		}
		// coproc_setvars binds `<NAME>_PID` only after the array binding
		// succeeds; a readonly array makes bash bail before the PID. The
		// assignment follows a nameref to its target (so a nameref pointing
		// at a readonly variable reports that target as readonly), matching
		// bash's bind_variable.
		// Mint a synthetic, runner-unique pid for `<NAME>_PID`. Real bash
		// uses the coprocess subshell's OS PID; we run the coprocess as a
		// goroutine with no PID of its own, so wait/kill resolve this
		// integer back to the bgProc below. Based on the shell's own PID so
		// it reads like a plausible pid; offset per coproc so concurrent
		// coprocs never collide.
		r.coprocSeq++
		coprocPid := int64(os.Getpid()) + r.coprocSeq
		coprocPidVar := ""
		if pidBase != "" {
			pidName := pidBase + "_PID"
			coprocPidVar = pidName
			if coprocReadonly == "" {
				pidTarget := pidName
				if pv := r.lookupVar(pidName); pv.Kind == expand.NameRef {
					if n, _ := pv.Resolve(r.writeEnv); n != "" {
						pidTarget = n
					}
				}
				if r.lookupVar(pidTarget).ReadOnly {
					r.errf("%s%s: readonly variable\n", r.bashErrPrefix(r.curStmtPos), pidTarget)
				} else {
					r.setVar(pidTarget, expand.Variable{
						Set:  true,
						Kind: expand.String,
						Str:  strconv.FormatInt(coprocPid, 10),
					})
				}
			}
		}
		if r.fdTable == nil {
			r.fdTable = make(map[int]*os.File)
		}
		r.fdTable[readFd] = pr
		r.fdTable[writeFd] = pw2
		if r.fdClosedTable != nil {
			delete(r.fdClosedTable, readFd)
			delete(r.fdClosedTable, writeFd)
		}
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[readFd] = true
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[writeFd] = pw2
		// Track which array element each fd backs so closing it (via a
		// `${NAME[i]}-` move/close redirect) rewrites the element to "-1",
		// the way bash marks a closed coproc descriptor. Only meaningful
		// when the fd array was actually bound to a writable variable.
		if coprocReadonly == "" && pidBase != "" {
			if r.coprocFds == nil {
				r.coprocFds = make(map[int]coprocFdRef)
			}
			r.coprocFds[readFd] = coprocFdRef{varName: pidBase, idx: 0}
			r.coprocFds[writeFd] = coprocFdRef{varName: pidBase, idx: 1}
		}

		bg := &bgProc{
			done:              make(chan struct{}),
			exit:              new(exitStatus),
			pidReady:          make(chan struct{}),
			coprocReadonly:    coprocReadonly,
			coprocReadonlyPos: r.curStmtPos,
			coprocPidVar:      coprocPidVar,
			coprocPid:         coprocPid,
			coprocReadFd:      readFd,
			coprocWriteFd:     writeFd,
			coprocReadFile:    pr,
			coprocWriteFile:   pw2,
		}
		r.bgProcs = append(r.bgProcs, bg)
		r.lastBang = bg
		if r.coprocReg == nil {
			r.coprocReg = &coprocReg{}
		}
		r.coprocReg.add(coprocPid, bg)
		// Stash the bgProc on the goroutine's ctx so the exec handler
		// publishes the real OS PID of whatever command the coprocess
		// runs into bg.pid (via publishBgPid). `kill $COPROC_PID` resolves
		// the synthetic pid to that real child so the signal reaches it.
		bgCtx, cancel := r.backgroundContext(ctx)
		// A custom in-process command can be blocked in an ordinary io.Copy
		// which context cancellation cannot interrupt. Coprocs are streams,
		// not terminals: close both parent pipe ends as part of cancellation
		// so a blocked reader sees EOF and a blocked writer sees a broken pipe.
		// This is also the correct mechanism for the noninteractive xcase and
		// cat commands in bash's coproc fixture; no ConPTY is involved.
		bg.cancel = func() {
			pr.Close()
			pw2.Close()
			cancel()
		}
		bgCtx = context.WithValue(bgCtx, bgProcCtxKey{}, bg)
		go func() {
			defer r2.closeDirFile()
			defer func() {
				cancel()
				pw.Close()
				pr2.Close()
				if n := bg.killedSignal.Load(); n > 0 {
					r2.exit = exitStatus{code: uint8(128 + n)}
				}
				*bg.exit = r2.exit
				// Close pidReady even if the coprocess body never exec'd a
				// real process (e.g. a pure-builtin coproc), so a waiter on
				// the real pid doesn't hang forever.
				select {
				case <-bg.pidReady:
				default:
					close(bg.pidReady)
				}
				close(bg.done)
			}()
			r2.Run(bgCtx, cm.Stmt)
			r2.exit.exiting = false
			r2.exit.discarding = false
		}()
	default:
		if r.cmdGoForm(ctx, cm) {
			return
		}
		// Should only happen if we forgot a case above.
		r.errf("unhandled command node: %T\n", cm)
		r.exit.code = 1
	}
}

// coprocSetVar binds the coproc fd array to varName, reporting
// `<var>: readonly variable` when varName is readonly. It returns
// whether the variable was readonly: bash also unsets the coproc
// variable when the coprocess is reaped, and that deferred unset of a
// readonly variable yields a separate `<var>: cannot unset: readonly
// variable` at wait time (see [Runner.reapCoproc]).
func (r *Runner) coprocSetVar(varName string, readFd, writeFd int) bool {
	readonly := r.lookupVar(varName).ReadOnly
	r.setVar(varName, expand.Variable{
		Set:  true,
		Kind: expand.Indexed,
		List: []string{
			strconv.Itoa(readFd),
			strconv.Itoa(writeFd),
		},
	})
	return readonly
}

// coprocHighFds picks the logical read/write fd numbers a coproc exposes
// as `${NAME[0]}` / `${NAME[1]}`. Bash's coproc setup calls sh_openpipe
// twice — once per pipe — and sh_openpipe moves *both* ends of each pipe
// to a high fd via move_to_high_fd(fd, 1, 64). So four fds are claimed
// descending from 63 (read-pipe read+write, then write-pipe read+write);
// the parent then keeps the read-pipe's read end and the write-pipe's
// write end while the two child-facing ends in between are closed. That
// is why a fresh coproc surfaces as `63 60`, not `63 62`. We mirror the
// numbering: readFd is the highest free fd, writeFd the fourth one down.
func (r *Runner) coprocHighFds() (readFd, writeFd int) {
	used := func(n int) bool {
		if _, ok := r.fdTable[n]; ok {
			return true
		}
		if _, ok := r.fdWriteTable[n]; ok {
			return true
		}
		return false
	}
	next := func(below int) int {
		for n := below; n > 3; n-- {
			if !used(n) {
				return n
			}
		}
		return -1
	}
	rpipeRead := next(63) // ${NAME[0]} — parent reads child stdout
	rpipeWrite := next(rpipeRead - 1)
	wpipeRead := next(rpipeWrite - 1)
	wpipeWrite := next(wpipeRead - 1) // ${NAME[1]} — parent writes child stdin
	return rpipeRead, wpipeWrite
}

// reapCoproc emits the diagnostics bash produces when it reaps a
// finished coprocess and unsets its variables. Like bash's
// coproc_unsetvars, it first force-unbinds the `<NAME>_PID` variable —
// without following namerefs and ignoring the readonly attribute, so a
// readonly PID variable simply disappears rather than erroring. A
// readonly coproc *array* still can't be unset, so reaping reports
// `<var>: cannot unset: readonly variable` — ordered after any commands
// between `coproc` and `wait`, which is why it fires here rather than at
// coproc creation.
func (r *Runner) reapCoproc(bg *bgProc) {
	// Release the coproc's pipe fds so a later coproc reuses the same high
	// fd numbers (bash closes c_rfd / c_wfd in coproc_dispose). Closing
	// the parent-side ends also lets the now-idle pipe drain.
	if bg.coprocReadFd != 0 || bg.coprocWriteFd != 0 {
		if bg.coprocReadFile != nil {
			bg.coprocReadFile.Close()
		}
		if bg.coprocWriteFile != nil {
			bg.coprocWriteFile.Close()
		}
		for _, fd := range [...]int{bg.coprocReadFd, bg.coprocWriteFd} {
			delete(r.fdTable, fd)
			delete(r.fdReadTable, fd)
			delete(r.fdWriteTable, fd)
			delete(r.coprocFds, fd)
			if r.coprocReapedFds == nil {
				r.coprocReapedFds = make(map[int]bool)
			}
			r.coprocReapedFds[fd] = true
		}
		bg.coprocReadFd, bg.coprocWriteFd = 0, 0
		bg.coprocReadFile, bg.coprocWriteFile = nil, nil
	}
	if bg.coprocPid != 0 && r.coprocReg != nil {
		r.coprocReg.remove(bg.coprocPid)
	}
	if bg.coprocPidVar != "" {
		r.forceDelVar(bg.coprocPidVar)
		bg.coprocPidVar = ""
	}
	if bg.coprocReadonly == "" {
		return
	}
	name := bg.coprocReadonly
	bg.coprocReadonly = ""
	r.errf("%s%s: cannot unset: readonly variable\n", r.bashErrPrefix(bg.coprocReadonlyPos), name)
}

func (r *Runner) trapCallback(ctx context.Context, callback, name string) uint8 {
	if callback == "" {
		return 0 // nothing to do
	}
	wasHandlingTrap := r.handlingTrap
	if wasHandlingTrap {
		if name != "debug" || r.handlingDebugTrap {
			return 0 // don't recurse, as that could lead to cycles
		}
	} else {
		r.handlingTrap = true
	}
	if name == "debug" {
		r.handlingDebugTrap = true
	}
	// A callback is ordinary source even when agentic work triggered it.
	// Its own explicit block may opt in without lending scope to its caller.
	savedAgentic := r.bashPPAgentic
	r.bashPPAgentic = false
	defer func() {
		r.bashPPAgentic = savedAgentic
		if name == "debug" {
			r.handlingDebugTrap = false
		}
		if !wasHandlingTrap {
			r.handlingTrap = false
		}
	}()

	p := syntax.NewParser()
	// The action is re-parsed on every invocation, so the trap text is
	// re-evaluated against the alias and variable state in effect each time
	// it fires rather than parsed once when `trap` was called.
	if r.Dialect() == syntax.LangBashPP {
		syntax.Variant(syntax.LangBashPP)(p)
	}
	file, err := p.Parse(strings.NewReader(callback), name+" trap")
	if err != nil {
		r.errf(name+"trap: %v\n", err)
		// ignore errors in the callback
		return 0
	}
	oldExit := r.exit
	oldTrapEntryExit := r.trapEntryExit
	r.trapEntryExit = oldExit
	defer func() {
		r.trapEntryExit = oldTrapEntryExit
	}()
	r.exit = exitStatus{} // start fresh so we can capture the trap's exit
	// Commands run from a trap handler trace one xtrace level deeper
	// (bash replicates PS4's first char: `+` -> `++`).
	r.xtraceLevel++
	// Anchor alias timing at trap delivery so aliases already defined expand
	// in the re-parsed body (see runSignalTrap).
	r.withAliasReparse(r.trapAliasReparseLine(), func() {
		r.stmts(ctx, file.Stmts)
	})
	r.xtraceLevel--
	trapCode := r.exit.code
	trapExited := r.exit.exiting
	r.exit = oldExit // traps on EXIT or ERR should not modify the result
	// An EXIT trap that itself runs `exit N` overrides the shell's
	// (or subshell's) final status with N (bash: `exit 0` in the EXIT
	// trap clears a prior failure).
	if name == "exit" && trapExited {
		r.exit.code = trapCode
		r.exit.err = nil
		if trapCode == 0 {
			r.exit.fatalExit = false
		}
	}

	return trapCode
}

func (r *Runner) expandRawAliasSource(src string) (string, bool) {
	i := 0
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n') {
		i++
	}
	start := i
	for i < len(src) && (src[i] == '_' || '0' <= src[i] && src[i] <= '9' ||
		'a' <= src[i] && src[i] <= 'z' || 'A' <= src[i] && src[i] <= 'Z') {
		i++
	}
	if i == start {
		return "", false
	}
	als, ok := r.alias[src[start:i]]
	if !ok || als.raw == "" {
		return "", false
	}
	var b strings.Builder
	b.WriteString(src[:start])
	b.WriteString(als.raw)
	if als.blank {
		b.WriteByte(' ')
	} else if i < len(src) && src[i] != ' ' && src[i] != '\t' && src[i] != '\n' {
		b.WriteByte(' ')
	}
	b.WriteString(src[i:])
	return b.String(), true
}

func (r *Runner) runAliasReparseFile(ctx context.Context, pos syntax.Pos, aliasUseLine int, file *syntax.File) {
	prevOverride := r.aliasLineOverride
	r.aliasLineOverride = int(pos.Line())
	r.aliasReparseDepth++
	r.withAliasReparse(aliasUseLine, func() {
		r.stmts(ctx, file.Stmts)
	})
	r.aliasReparseDepth--
	r.aliasLineOverride = prevOverride
}

func (r *Runner) aliasStartsCompound(name string, aliasUseLine int) bool {
	als, ok := r.alias[name]
	if !ok || als.defLine > 0 && aliasUseLine > 0 && aliasUseLine <= als.defLine {
		return false
	}
	src := strings.TrimLeft(aliasValue(als), " \t\r\n")
	for _, kw := range []string{"for", "case"} {
		if src == kw || strings.HasPrefix(src, kw+" ") || strings.HasPrefix(src, kw+"\t") ||
			strings.HasPrefix(src, kw+"\n") || strings.HasPrefix(src, kw+";") {
			return true
		}
	}
	return false
}

func (r *Runner) aliasReparsePhysicalLine(pos syntax.Pos, aliasUseLine int) (*syntax.File, bool) {
	if len(r.bashSource) == 0 || !pos.IsValid() {
		return nil, false
	}
	start, ok := r.sourceOffset(pos)
	if !ok {
		return nil, false
	}
	end := r.sourceLineEndOffset(pos.Line())
	if end <= start || end > len(r.bashSource) {
		return nil, false
	}
	src, ok := r.expandAliasSourceLine(string(r.bashSource[start:end]), aliasUseLine)
	if !ok {
		return nil, false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		return nil, false
	}
	r.stdinSourceOffset = max(r.stdinSourceOffset, end)
	r.discardRestOfLine = pos.Line()
	return file, true
}

func (r *Runner) sourcePhysicalLineContains(pos syntax.Pos, needle string) bool {
	if len(r.bashSource) == 0 || !pos.IsValid() {
		return false
	}
	start, ok := r.sourceOffset(pos)
	if !ok {
		return false
	}
	end := r.sourceLineEndOffset(pos.Line())
	return start < end && end <= len(r.bashSource) &&
		strings.Contains(string(r.bashSource[start:end]), needle)
}

func (r *Runner) expandAliasSourceLine(src string, aliasUseLine int) (string, bool) {
	var b strings.Builder
	inSgl, inDbl := false, false
	comsubDepth := 0
	comsubFromDbl := false
	changed := false
	candidate := true
	for i := 0; i < len(src); {
		c := src[i]
		if inDbl && comsubDepth == 0 && c == '$' && i+1 < len(src) && src[i+1] == '(' {
			b.WriteString("$(")
			i += 2
			comsubDepth = 1
			comsubFromDbl = true
			candidate = true
			continue
		}
		if inSgl {
			b.WriteByte(c)
			i++
			if c == '\'' {
				inSgl = false
			}
			continue
		}
		if inDbl && comsubDepth == 0 {
			b.WriteByte(c)
			i++
			if c == '\\' && i < len(src) {
				b.WriteByte(src[i])
				i++
			} else if c == '"' {
				inDbl = false
			}
			continue
		}
		switch c {
		case '$':
			b.WriteByte(c)
			i++
			if i < len(src) && src[i] == '(' {
				b.WriteByte(src[i])
				i++
				comsubDepth++
				candidate = true
			} else {
				candidate = false
			}
			continue
		case '\'':
			inSgl = true
			b.WriteByte(c)
			candidate = false
			i++
			continue
		case '"':
			inDbl = true
			b.WriteByte(c)
			candidate = false
			i++
			continue
		case '\\':
			b.WriteByte(c)
			candidate = false
			i++
			if i < len(src) {
				b.WriteByte(src[i])
				i++
			}
			continue
		case ' ', '\t', '\n', '\r':
			b.WriteByte(c)
			i++
			continue
		case ';', '&', '|', '(':
			b.WriteByte(c)
			if c == '(' && comsubDepth > 0 {
				comsubDepth++
			}
			candidate = true
			i++
			continue
		case ')':
			b.WriteByte(c)
			candidate = false
			i++
			if comsubDepth > 0 {
				comsubDepth--
				if comsubDepth == 0 && comsubFromDbl {
					inDbl = true
					comsubFromDbl = false
				}
			}
			continue
		}
		if !aliasNameStart(c) {
			b.WriteByte(c)
			candidate = false
			i++
			continue
		}
		j := i + 1
		for j < len(src) && aliasNameChar(src[j]) {
			j++
		}
		name := src[i:j]
		if !candidate {
			b.WriteString(name)
			i = j
			continue
		}
		als, ok := r.alias[name]
		if !ok || als.defLine > 0 && aliasUseLine > 0 && aliasUseLine <= als.defLine {
			b.WriteString(name)
			candidate = false
			i = j
			continue
		}
		repl := aliasValue(als)
		b.WriteString(repl)
		if als.blank {
			b.WriteByte(' ')
		}
		if comsubDepth > 0 {
			if n := aliasReplacementComsubCloseCount(repl); n > 0 {
				comsubDepth -= min(comsubDepth, n)
				if comsubDepth == 0 && comsubFromDbl {
					inDbl = true
					comsubFromDbl = false
				}
			}
		}
		i = j
		changed = true
		candidate = aliasReplacementLeavesCommandPosition(repl, als.blank)
	}
	return b.String(), changed
}

func aliasReplacementComsubCloseCount(src string) int {
	unmatched := 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '\'', '"':
			quote := src[i]
			for i++; i < len(src) && src[i] != quote; i++ {
				if quote == '"' && src[i] == '\\' {
					i++
				}
			}
		case '(':
			unmatched--
		case ')':
			unmatched++
		}
	}
	if unmatched < 0 {
		return 0
	}
	return unmatched
}

func aliasReplacementLeavesCommandPosition(src string, blank bool) bool {
	if blank {
		return true
	}
	for i := len(src) - 1; i >= 0; i-- {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ';', '&', '|', '(':
			return true
		default:
			return false
		}
	}
	return true
}

func aliasNameStart(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func aliasNameChar(c byte) bool {
	return aliasNameStart(c) || '0' <= c && c <= '9'
}

// flattenAssigns yields each effective syntax.Assign from a declare-
// family clause's args. The second return value, fromString, is true
// when the Assign was synthesized from a string-form arg (`readonly
// 'name=val'`) rather than parsed as a syntax-level assignment
// (`readonly name=val`). Bash 5.3 attributes assignment-failure error
// messages differently for the two paths, so the caller needs to know.
//
// Pre-scans args for `-f` / `-F` so a function-mode invocation keeps
// the full string as the function name (functions can be named
// `foo=bar` or `/bin/echo`) instead of splitting on `=`.
func (r *Runner) flattenAssigns(args []*syntax.Assign) iter.Seq2[*syntax.Assign, bool] {
	funcMode := false
	for _, as := range args {
		if as.Name != nil || as.Value == nil {
			continue
		}
		if lit := as.Value.Lit(); lit == "-f" || lit == "-F" {
			funcMode = true
			break
		}
	}
	return func(yield func(*syntax.Assign, bool) bool) {
		for _, as := range args {
			// Convert "declare $x" into "declare value".
			// Don't use syntax.Parser here, as we only want the basic
			// splitting by '='.
			if as.Name != nil {
				if !yield(as, false) {
					return
				}
				continue
			}
			for _, field := range r.fields(as.Value) {
				as := &syntax.Assign{}
				name, val, ok := strings.Cut(field, "=")
				if funcMode && !strings.HasPrefix(field, "-") {
					// `export -f NAME` / `declare -f NAME` —
					// keep the full field as the function name,
					// even if it contains `=`. Option flags
					// (starting with `-`) still get parsed by
					// the option loop below.
					name = field
					ok = false
				}
				// `+=` append form in a string-parsed arg
				// (`typeset 'sx+=bar'`): strip the trailing `+`
				// from the name and mark the assignment as append,
				// matching the literal `declare sx+=bar` parse.
				if ok && strings.HasSuffix(name, "+") {
					name = name[:len(name)-1]
					as.Append = true
				}
				as.Name = &syntax.Lit{Value: name}
				if !ok {
					as.Naked = true
				} else {
					if base, idx, ok := splitArrayRef(name); ok && syntax.ValidName(base) {
						name = base
						as.Name = &syntax.Lit{Value: name}
						as.Index = &syntax.Word{Parts: []syntax.WordPart{
							&syntax.Lit{Value: idx},
						}}
					} else if strings.Contains(name, "[") {
						// Keep the whole assignment form so the
						// identifier diagnostic matches bash, e.g.
						// `declare "A[$k]=X"` with k=] reports
						// `A[]]=X': not a valid identifier.
						as.Name = &syntax.Lit{Value: field}
						as.Naked = true
					} else {
						as.Value = &syntax.Word{Parts: []syntax.WordPart{
							// val is already fully expanded by r.fields above
							// (params, cmdsub, quote removal). Wrap it single-
							// quoted so the later assignment expansion emits it
							// verbatim and does NOT re-process it — in particular
							// a `~` that came from a parameter expansion
							// (`b='c=~/x'; readonly "$b"`) must NOT tilde-expand.
							&syntax.SglQuoted{Value: val},
						}}
					}
					if !as.Naked && as.Value == nil {
						as.Value = &syntax.Word{Parts: []syntax.WordPart{
							// val is already fully expanded by r.fields above
							// (params, cmdsub, quote removal). Wrap it single-
							// quoted so the later assignment expansion emits it
							// verbatim and does NOT re-process it — in particular
							// a `~` that came from a parameter expansion
							// (`b='c=~/x'; readonly "$b"`) must NOT tilde-expand.
							&syntax.SglQuoted{Value: val},
						}}
					}
				}
				if !yield(as, true) {
					return
				}
			}
		}
	}
}

func assocAssignKeyQuoted(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.ParamExp:
			if part.Exp != nil && assocAssignKeyQuoted(part.Exp.Word.Parts) {
				return true
			}
		}
	}
	return false
}

func (r *Runner) match(pat, name string) bool {
	mode := pattern.EntireString | pattern.ExtendedOperators | pattern.LenientRanges
	localeMode := r.localePatternMode()
	if localeMode {
		mode |= pattern.Locale
	}
	// In a declared non-C, single-byte locale (MB_CUR_MAX==1, e.g.
	// de_DE.ISO-8859-1 — as opposed to a UTF-8 or legacy multi-byte
	// charset like Big5/Shift-JIS) every byte is one character whose
	// codepoint equals its byte value, so a byte that fails UTF-8
	// decoding is ordinary Latin-1 text, not corrupt binary data.
	// Widening it into a rune lets bracket expressions, POSIX classes,
	// and equivalence classes see it as a real character instead of
	// falling back to a matcher that can't see "[...]" at all. Outside
	// such a locale (including the default, unset one, and a UTF-8
	// charset such as "C.UTF-8"), keep the byte-blind fallback so
	// unrelated cases like matching a stray UTF-8 continuation byte
	// against multi-byte text keep working at the byte level.
	if localeMode && internal.SingleByteLocale(r.localeCategory("LC_CTYPE")) {
		pat = internal.WidenLatin1(pat)
		name = internal.WidenLatin1(name)
	} else if !utf8.ValidString(pat) {
		return internal.BytePatternMatch([]byte(pat), []byte(name))
	}
	matcher, err := internal.ExtendedPatternMatcher(pat, mode)
	_ = err // TODO: report these errors
	return matcher != nil && matcher(name)
}

func (r *Runner) localePatternMode() bool {
	collate := r.localeCategory("LC_COLLATE")
	ctype := r.localeCategory("LC_CTYPE")
	return nonCLocale(collate) || nonCLocale(ctype)
}

func (r *Runner) localeCategory(category string) string {
	if v := r.lookupVar("LC_ALL"); v.IsSet() && v.String() != "" {
		return v.String()
	}
	if v := r.lookupVar(category); v.IsSet() && v.String() != "" {
		return v.String()
	}
	if v := r.lookupVar("LANG"); v.IsSet() && v.String() != "" {
		return v.String()
	}
	return ""
}

func nonCLocale(locale string) bool {
	switch strings.ToLower(locale) {
	case "", "c", "posix":
		return false
	}
	return true
}

// formatTIMEFORMAT renders the durations against bash's TIMEFORMAT
// directives: `%[l][p]{R,U,S}` for real/user/sys with optional `l`
// (mins+secs) prefix and a 0-3 precision digit; `%P` for %CPU; `%%`
// for literal `%`; backslash escapes `\n` `\t` `\\` `\?` mapped to
// their C equivalents (with unknown sequences emitted verbatim).
func formatTIMEFORMAT(format string, real, user, sys time.Duration, decpoint string) string {
	var sb strings.Builder
	emit := func(d time.Duration, longForm bool, prec int) {
		if longForm {
			min := int(d.Minutes())
			sec := math.Mod(d.Seconds(), 60.0)
			sb.WriteString(withDecimalPoint(fmt.Sprintf("%dm%.*fs", min, prec, sec), decpoint))
			return
		}
		sb.WriteString(withDecimalPoint(fmt.Sprintf("%.*f", prec, d.Seconds()), decpoint))
	}
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '\\':
			if i+1 >= len(format) {
				sb.WriteByte('\\')
				continue
			}
			i++
			switch format[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '\\':
				sb.WriteByte('\\')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(format[i])
			}
		case '%':
			i++
			longForm := false
			prec := 3
			if i < len(format) && format[i] == 'l' {
				longForm = true
				i++
			}
			if i < len(format) && format[i] >= '0' && format[i] <= '9' {
				prec = int(format[i] - '0')
				i++
				if i < len(format) && format[i] == 'l' {
					longForm = true
					i++
				}
			}
			if i >= len(format) {
				sb.WriteByte('%')
				return sb.String()
			}
			switch format[i] {
			case 'R':
				emit(real, longForm, prec)
			case 'U':
				emit(user, longForm, prec)
			case 'S':
				emit(sys, longForm, prec)
			case 'P':
				// %CPU — bash computes (user+sys)/real*100. We
				// don't track user/sys, so emit "0.00" so the
				// directive expands to something parseable.
				fmt.Fprintf(&sb, "%.*f", prec, 0.0)
			case '%':
				sb.WriteByte('%')
			default:
				sb.WriteByte('%')
				sb.WriteByte(format[i])
			}
		default:
			sb.WriteByte(format[i])
		}
	}
	return sb.String()
}

// posixTimesReport writes the shell's cumulative user and system times to
// stderr in the format bash uses for `time` used by itself in POSIX mode:
//
//	user\t<M>m<S.SS>s
//	sys\t<M>m<S.SS>s
//
// This is the same accounting reported by the `times` builtin
// (getrusage RUSAGE_SELF + RUSAGE_CHILDREN); like that builtin, the
// values are not tracked in this pure-Go runner and report as zero.
func (r *Runner) posixTimesReport() {
	var user, sys time.Duration // not tracked; see the `times` builtin
	r.errf("user\t%s\nsys\t%s\n", posixTimesString(user), posixTimesString(sys))
}

// posixTimesString formats a duration as bash does for the POSIX-mode bare
// `time` report: minutes, then seconds to two decimal places (e.g. 0m0.00s).
func posixTimesString(d time.Duration) string {
	min := int(d.Minutes())
	sec := math.Mod(d.Seconds(), 60.0)
	return fmt.Sprintf("%dm%.2fs", min, sec)
}

func elapsedString(d time.Duration, posix bool, decpoint string) string {
	if posix {
		return withDecimalPoint(fmt.Sprintf("%.2f", d.Seconds()), decpoint)
	}
	min := int(d.Minutes())
	sec := math.Mod(d.Seconds(), 60.0)
	return withDecimalPoint(fmt.Sprintf("%dm%.3fs", min, sec), decpoint)
}

// withDecimalPoint swaps the Go-formatted "." radix for the locale's decimal
// point. Timing reports are the one place POSIX requires the shell itself to
// honor LC_NUMERIC ("the radix character of the current locale"); bash prints
// them through locale_decpoint (timeval.c mkfmt).
func withDecimalPoint(s, decpoint string) string {
	if decpoint == "." {
		return s
	}
	return strings.ReplaceAll(s, ".", decpoint)
}

// carriedLocaleDecimalPoint resolves only locale definitions whose numeric
// data this pure-Go runner carries itself. It deliberately does not infer a
// radix from a language or territory: for example, de_CH is not de_DE, and
// German locales do not all share the same radix. Keep this bounded until the
// runner carries more complete locale data.
func carriedLocaleDecimalPoint(locale string) (string, bool) {
	switch strings.ToLower(locale) {
	case "c", "posix":
		return ".", true
	case "de_de.iso-8859-1", "de_de.iso8859-1", "de_de.iso88591":
		return ",", true
	default:
		return "", false
	}
}

// localeDecimalPoint resolves the radix character for the shell's timing
// reports from the carried locale data, using the POSIX LC_NUMERIC
// precedence implemented by localeCategory. Unsupported or unset locales use
// Go's C-locale formatting rather than guessing.
func (r *Runner) localeDecimalPoint() string {
	if point, ok := carriedLocaleDecimalPoint(r.localeCategory("LC_NUMERIC")); ok {
		return point
	}
	return "."
}

func (r *Runner) stmts(ctx context.Context, stmts []*syntax.Stmt) {
	var live *goSourceLivenessPlan
	scope, frame := r.bashPPScope, r.bashPPGoSource && r.bashPPFuncActive > 0
	if frame && len(stmts) > 0 {
		// A loop body asks for the same list's plan on every iteration; the
		// last answer saves the shared cache's interface-keyed lookup.
		if r.goSourceLivenessFirst == stmts[0] && r.goSourceLivenessLen == len(stmts) {
			live = r.goSourceLivenessPlan
		} else {
			live = goSourceLivenessFor(stmts)
			r.goSourceLivenessFirst, r.goSourceLivenessLen, r.goSourceLivenessPlan = stmts[0], len(stmts), live
		}
	}
	for i := 0; i < len(stmts); i++ {
		r.stmt(ctx, stmts[i])
		if frame {
			r.goSourceReleaseDead(live, scope, i)
		}
		// Go source form has no "the last command failed" status to carry, so a
		// statement that reports failure aborts the program. A status a call
		// uses to REPORT a result is exempt, exactly as it is exempt from
		// errexit; see the recover case in [Runner.bashPPPredeclared]. A panic
		// already unwinding carries its own termination and must not be
		// re-reported as a plain fatal status.
		if r.bashPPGoSource && r.exit.code != 0 && !r.exit.errexitExempt && !r.bashPPPanicking() {
			r.exit.fatal(ExitStatus(r.exit.code))
		}
		// A Go-form goto whose label is in this list resumes the list at the
		// label; otherwise it unwinds like a break until the list that holds
		// the label is reached.
		if r.bashPPBranch == bashPPBranchGoto {
			if at := r.bashPPGotoTarget(stmts); at >= 0 {
				if r.bashPPScope != nil {
					r.bashPPScope.resumeAtLabel(r.bashPPGotoLabel)
				}
				r.bashPPClearBranch()
				i = at - 1
				continue
			}
			return
		}
		// Propagate a pending break/continue out of a compound body (brace
		// group, if/case body) so the rest of the list is skipped. Safe across
		// function bodies: call() resets the loop-control state at the function
		// boundary, so a function is a break/continue boundary as bash requires.
		if r.loopControlPending() || r.bashPPBranch != bashPPBranchNone {
			return
		}
		// Stop the list once `exit`/`return` has fired. Outside a trap this is
		// a no-op (stop() already neutralises later statements), but inside a
		// trap body — where stop() deliberately ignores the script-level
		// `exiting` flag — it ensures an `exit N` in the trap terminates the
		// body instead of letting a trailing command reset the status.
		if r.exit.exiting || r.exit.returning {
			return
		}
	}
}

func (r *Runner) hdocReader(rd *syntax.Redirect) (*os.File, error) {
	// Build the entire here-document body up front, then serve it from a
	// descriptor. On unix that descriptor is an unlinked temp file rather
	// than a pipe fed by a goroutine: a pipe+goroutine races with the
	// consuming process under load (os.File fd lifecycle under GC), which
	// intermittently truncated a command's output to empty. A plain file is
	// deterministic and matches how bash materialises here-docs.
	var body []byte
	if rd.Hdoc != nil {
		quoted := quotedHdocDelimiter(rd.Word)
		if rd.Op != syntax.DashHdoc {
			if quoted {
				body = []byte(rd.Hdoc.Lit())
			} else {
				body = []byte(r.document(rd.Hdoc))
			}
		} else {
			var buf bytes.Buffer
			var cur []syntax.WordPart
			atLineStart := true
			flushLine := func() {
				if buf.Len() > 0 {
					buf.WriteByte('\n')
				}
				word := &syntax.Word{Parts: cur}
				if quoted {
					buf.WriteString(word.Lit())
				} else {
					buf.WriteString(r.document(word))
				}
				cur = cur[:0]
			}
			for _, wp := range rd.Hdoc.Parts {
				lit, ok := wp.(*syntax.Lit)
				if !ok {
					cur = append(cur, wp)
					// Even an expansion which later produces an empty string is
					// syntactically part of the line. Tabs in a following literal
					// are therefore not leading tabs eligible for <<- stripping.
					atLineStart = false
					continue
				}
				first := true
				for part := range strings.SplitSeq(lit.Value, "\n") {
					if !first {
						flushLine()
						cur = cur[:0]
						atLineStart = true
					}
					first = false
					if atLineStart {
						part = strings.TrimLeft(part, "\t")
					}
					if part != "" {
						atLineStart = false
					}
					cur = append(cur, &syntax.Lit{Value: part})
				}
			}
			flushLine()
			body = buf.Bytes()
		}
	}
	return hdocServe(body)
}

func quotedHdocDelimiter(word *syntax.Word) bool {
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.Contains(part.Value, `\`) {
				return true
			}
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		default:
			return true
		}
	}
	return false
}

// allocateFd returns the next unused fd number >= 10, suitable for
// {varname} named-fd allocations. Bash picks fd numbers starting at
// 10 to avoid colliding with the conventional stdio range (0/1/2) and
// with fds a script may have explicitly assigned (3-9).
func (r *Runner) allocateFd() (int, bool) {
	limit := 0
	if r.ulimitOverride != nil {
		if s := r.ulimitOverride["-n"]; s != "" {
			limit, _ = strconv.Atoi(s)
		}
	}
	for n := 10; limit == 0 || n < limit; n++ {
		if _, ok := r.fdTable[n]; !ok {
			if _, ok := r.fdWriteTable[n]; !ok {
				return n, true
			}
		}
	}
	return -1, false
}

func (r *Runner) effectiveNofileLimit() int {
	if r.ulimitOverride != nil {
		if value := r.ulimitOverride["-n"]; value != "" {
			if limit, err := strconv.Atoi(value); err == nil && limit > 0 {
				return limit
			}
		}
	}
	if limit, ok := nofileLimit(); ok && limit <= uint64(^uint(0)>>1) {
		return int(limit)
	}
	// An unlimited or unavailable platform limit must not allow an
	// attacker-controlled redirect number to drive an unbounded allocation.
	return 1 << 20
}

func (r *Runner) execExtraFiles() ([]*os.File, string, func(), error) {
	var extra []*os.File
	var fds []string
	var cleanup []func()
	maxFD := 2
	limit := r.effectiveNofileLimit()
	for fd := range r.fdTable {
		if fd < 0 || fd >= limit {
			return nil, "", func() {}, fmt.Errorf("file descriptor %d exceeds open-files limit %d", fd, limit)
		}
		maxFD = max(maxFD, fd)
	}
	for fd := range r.fdWriteTable {
		if fd < 0 || fd >= limit {
			return nil, "", func() {}, fmt.Errorf("file descriptor %d exceeds open-files limit %d", fd, limit)
		}
		maxFD = max(maxFD, fd)
	}
	var closedSlot *os.File
	for fd := 3; fd <= maxFD; fd++ {
		f, ok := r.fdTable[fd]
		if !ok {
			if w, wok := r.fdWriteTable[fd].(*os.File); wok {
				f, ok = w, true
			} else if w, wok := r.fdWriteTable[fd]; wok {
				pr, pw, err := os.Pipe()
				if err != nil {
					break
				}
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(w, pr)
					_ = pr.Close()
					close(done)
				}()
				f, ok = pw, true
				cleanup = append(cleanup, func() {
					_ = pw.Close()
					<-done
				})
			}
		}
		if !ok {
			// exec.Cmd.ExtraFiles maps slice entry i to child fd 3+i,
			// so a virtual sparse fd such as 8 still needs entries for
			// 3 through 7. A non-nil closed *os.File reports fd -1 to
			// os.StartProcess; Unix forkExec closes that child slot rather
			// than inventing an open descriptor there.
			if closedSlot == nil {
				closedSlot, _ = os.Open(os.DevNull)
				if closedSlot == nil {
					break
				}
				_ = closedSlot.Close()
			}
			extra = append(extra, closedSlot)
			continue
		}
		extra = append(extra, f)
		fds = append(fds, strconv.Itoa(fd))
	}
	return extra, strings.Join(fds, ","), func() {
		for _, fn := range cleanup {
			fn()
		}
	}, nil
}

// closeClosedInheritedFdsOnExec prevents an inherited descriptor explicitly
// closed by the shell from reaching an external command. Registered inherited
// descriptors which have not been referenced by a redirect still belong to the
// shell and must remain open: in particular they can be sparse (for example,
// only fd 8), which cannot be represented by exec.Cmd.ExtraFiles without
// inventing the missing descriptors below it.
func (r *Runner) closeClosedInheritedFdsOnExec() {
	for fd := range r.fdClosedTable {
		if fd >= 3 && r.inheritedFds[fd] {
			closeOnExecFd(fd)
		}
	}
}

// setReadFd binds f as a readable source for the given target fd.
// targetFd == -1 means "use the input default (fd 0 / r.stdin)". For 0
// we set r.stdin; for N >= 3 we store in fdTable. 1/2 are not valid
// input targets in bash and are rejected.
func (r *Runner) setReadFd(targetFd int, f *os.File) error {
	switch targetFd {
	case -1, 0:
		r.stdin = f
		r.stdinClosed = false
		r.stdinTTYFallback = false
		r.stdinDevTTY = false
		// fd 0 bound to an explicit input redirect: suppress the stdin-source
		// reader so a heredoc/here-string/`<file` wins over the
		// stdin-script-line-consumption feature (see stdinRedirected).
		r.stdinRedirected = true
		r.asyncStdinExplicit = true
		if r.fdWriteTable != nil {
			delete(r.fdWriteTable, 0)
		}
	case 1, 2:
		return fmt.Errorf("cannot use fd %d as input target", targetFd)
	default:
		if r.fdTable == nil {
			r.fdTable = make(map[int]*os.File)
		}
		r.fdTable[targetFd] = f
		if r.fdClosedTable != nil {
			delete(r.fdClosedTable, targetFd)
		}
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[targetFd] = true
		if r.fdWriteTable != nil {
			delete(r.fdWriteTable, targetFd)
		}
	}
	return nil
}

// setWriteFd binds w as an output sink for the given target fd.
// targetFd == -1 means "use the output default (fd 1 / r.stdout)".
// For 1 we set r.stdout, for 2 r.stderr (both accept any io.Writer).
// For N >= 3 we store in fdTable, which requires *os.File since a
// numbered fd must back a real OS handle.
func (r *Runner) setWriteFd(targetFd int, w io.Writer) error {
	switch targetFd {
	case -1, 1:
		r.stdout = w
		if r.fdTable != nil {
			delete(r.fdTable, 1)
		}
		if r.fdReadTable != nil {
			delete(r.fdReadTable, 1)
		}
	case 2:
		r.stderr = w
		if r.fdTable != nil {
			delete(r.fdTable, 2)
		}
		if r.fdReadTable != nil {
			delete(r.fdReadTable, 2)
		}
	case 0:
		return fmt.Errorf("cannot use fd 0 as output target")
	default:
		if f, ok := w.(*os.File); ok {
			if r.fdTable == nil {
				r.fdTable = make(map[int]*os.File)
			}
			r.fdTable[targetFd] = f
			if r.fdClosedTable != nil {
				delete(r.fdClosedTable, targetFd)
			}
			if r.fdWriteTable == nil {
				r.fdWriteTable = make(map[int]io.Writer)
			}
			r.fdWriteTable[targetFd] = f
			if r.fdReadTable != nil {
				delete(r.fdReadTable, targetFd)
			}
			return nil
		}
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[targetFd] = w
		if r.fdClosedTable != nil {
			delete(r.fdClosedTable, targetFd)
		}
		if r.fdTable != nil {
			delete(r.fdTable, targetFd)
		}
		if r.fdReadTable != nil {
			delete(r.fdReadTable, targetFd)
		}
	}
	return nil
}

func (r *Runner) bindReadWriteFd(targetFd int, f *os.File) error {
	switch targetFd {
	case -1, 0:
		r.stdin = f
		r.stdinTTYFallback = false
		r.stdinDevTTY = false
		r.stdinRedirected = true
		r.asyncStdinExplicit = true
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[0] = f
	case 1:
		r.stdout = f
		if r.fdTable == nil {
			r.fdTable = make(map[int]*os.File)
		}
		r.fdTable[1] = f
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[1] = true
	case 2:
		r.stderr = f
		if r.fdTable == nil {
			r.fdTable = make(map[int]*os.File)
		}
		r.fdTable[2] = f
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[2] = true
	default:
		if r.fdTable == nil {
			r.fdTable = make(map[int]*os.File)
		}
		r.fdTable[targetFd] = f
		if r.fdClosedTable != nil {
			delete(r.fdClosedTable, targetFd)
		}
		if r.fdReadTable == nil {
			r.fdReadTable = make(map[int]bool)
		}
		r.fdReadTable[targetFd] = true
		if r.fdWriteTable == nil {
			r.fdWriteTable = make(map[int]io.Writer)
		}
		r.fdWriteTable[targetFd] = f
	}
	return nil
}

func (r *Runner) dupFd(targetFd, sourceFd int, rd *syntax.Redirect) error {
	if targetFd == sourceFd {
		return nil
	}
	read, write, ok := r.fdCaps(sourceFd)
	// `>&N` requires the source fd be open for output; a closed/never-opened
	// fd, or one open only for input (e.g. an inherited read-only fd dup'd via
	// `>&N`), is EBADF — exactly as bash.
	if rd.Op == syntax.DplOut {
		ok = ok && write != nil
	}
	if rd.Op == syntax.DplIn && targetFd == 0 {
		ok = ok && read != nil
	}
	if !ok {
		r.errf("%s%s: Bad file descriptor\n", r.bashErrPrefix(rd.Word.Pos()), redirWordText(rd))
		return fmt.Errorf("%s: Bad file descriptor", redirWordText(rd))
	}
	r.bindFdCaps(targetFd, read, write)
	return nil
}

func (r *Runner) fdCaps(fd int) (read *os.File, write io.Writer, ok bool) {
	switch fd {
	case 0:
		write := io.Writer(nil)
		if r.fdWriteTable != nil {
			write = r.fdWriteTable[0]
		}
		if r.stdin != nil {
			// An inherited stdin opened for output (`0>file`, a pipe's
			// write end) is a valid `>&0` source, as in bash; Zed's login
			// shell environment capture writes its JSON that way.
			if write == nil && fileWritable(r.stdin) {
				write = r.stdin
			}
			return r.stdin, write, true
		}
		if write != nil {
			return nil, write, true
		}
		return nil, nil, false
	case 1:
		var read *os.File
		if r.fdTable != nil && r.fdReadTable[1] {
			read = r.fdTable[1]
		}
		// A badFdWriter is the sentinel for a closed output slot (`1>&-`);
		// treat it as not-open so a later `2>&1` dup'ing it reports EBADF,
		// matching bash's order-of-appearance fd handling.
		if r.stdout != nil && !isClosedFdWriter(r.stdout) {
			if read == nil {
				if f := dupReadablePipeFile(r.stdout); f != nil {
					read = f
				}
			}
			return read, r.stdout, true
		}
		if read != nil {
			return read, nil, true
		}
		return nil, nil, false
	case 2:
		var read *os.File
		if r.fdTable != nil && r.fdReadTable[2] {
			read = r.fdTable[2]
		}
		if r.stderr != nil && !isClosedFdWriter(r.stderr) {
			if read == nil {
				if f := dupReadablePipeFile(r.stderr); f != nil {
					read = f
				}
			}
			return read, r.stderr, true
		}
		if read != nil {
			return read, nil, true
		}
		return nil, nil, false
	}
	if r.fdClosedTable != nil && fd >= 3 && r.fdClosedTable[fd] {
		return nil, nil, false
	}
	if r.fdTable != nil {
		if f, exists := r.fdTable[fd]; exists {
			ok = true
			if r.fdReadTable[fd] {
				read = f
			}
		}
	}
	if r.fdWriteTable != nil {
		if w, exists := r.fdWriteTable[fd]; exists {
			ok = true
			write = w
			if read == nil {
				if f := dupReadablePipeFile(w); f != nil {
					read = f
				}
			}
		}
	}
	// Resolve any REGISTERED inherited descriptor (via WithInheritedFds /
	// the binary's startup fd discovery) so a dup like `1>&9` works when the
	// fd was passed in by an `exec … 9>file`. inheritedFd() only succeeds for
	// fds in r.inheritedFds, so an un-registered ambient fd still reports
	// EBADF — matching bash, which exposes exactly the descriptors open at
	// exec time.
	if !ok && fd >= 3 {
		if _, exists := r.inheritedFd(fd); exists {
			return r.fdCaps(fd)
		}
	}
	return read, write, ok
}

func (r *Runner) bindFdCaps(targetFd int, read *os.File, write io.Writer) {
	switch targetFd {
	case 0:
		r.stdin = read
		r.stdinClosed = false
		r.stdinTTYFallback = false
		r.stdinDevTTY = false
		r.stdinRedirected = true
		r.asyncStdinExplicit = true
		if write != nil {
			if r.fdWriteTable == nil {
				r.fdWriteTable = make(map[int]io.Writer)
			}
			r.fdWriteTable[0] = write
		} else if r.fdWriteTable != nil {
			delete(r.fdWriteTable, 0)
		}
	case 1:
		if write != nil {
			r.stdout = write
		} else {
			r.stdout = badFdWriter{}
		}
		if read != nil {
			if r.fdTable == nil {
				r.fdTable = make(map[int]*os.File)
			}
			r.fdTable[1] = read
			if r.fdReadTable == nil {
				r.fdReadTable = make(map[int]bool)
			}
			r.fdReadTable[1] = true
		} else {
			delete(r.fdTable, 1)
			delete(r.fdReadTable, 1)
		}
	case 2:
		if write != nil {
			r.stderr = write
		} else {
			r.stderr = badFdWriter{}
		}
		if read != nil {
			if r.fdTable == nil {
				r.fdTable = make(map[int]*os.File)
			}
			r.fdTable[2] = read
			if r.fdReadTable == nil {
				r.fdReadTable = make(map[int]bool)
			}
			r.fdReadTable[2] = true
		} else {
			delete(r.fdTable, 2)
			delete(r.fdReadTable, 2)
		}
	default:
		if r.fdClosedTable != nil {
			delete(r.fdClosedTable, targetFd)
		}
		if read != nil {
			if r.fdTable == nil {
				r.fdTable = make(map[int]*os.File)
			}
			r.fdTable[targetFd] = read
			if r.fdReadTable == nil {
				r.fdReadTable = make(map[int]bool)
			}
			r.fdReadTable[targetFd] = true
		} else {
			delete(r.fdTable, targetFd)
			delete(r.fdReadTable, targetFd)
		}
		if write != nil {
			if r.fdWriteTable == nil {
				r.fdWriteTable = make(map[int]io.Writer)
			}
			r.fdWriteTable[targetFd] = write
		} else {
			delete(r.fdWriteTable, targetFd)
		}
	}
}

type badFdWriter struct{}

func (badFdWriter) Write([]byte) (int, error) {
	return 0, ExitStatus(1)
}

// badFdReader is the sentinel the runner installs as a command's standard
// input when fd 0 has been explicitly closed (`cmd <&-`). A plain reading
// builtin sees an immediate error; the exec handler recognizes it and hands
// the spawned process a genuinely closed descriptor so its read fails with
// EBADF, exactly as bash leaves the inherited fd closed.
type badFdReader struct{}

func (badFdReader) Read([]byte) (int, error) {
	return 0, ExitStatus(1)
}

// isClosedFdWriteErr reports whether a builtin's standard-output write failed
// because the descriptor is closed — the badFdWriter sentinel installed by
// `N>&-` returns the ExitStatus error. A broken pipe (EPIPE) from a real pipe
// write is deliberately excluded: that is bash's silent SIGPIPE case, not a
// reported write error.
func isClosedFdWriteErr(err error) bool {
	_, ok := err.(ExitStatus)
	return ok
}

func shouldReportWriteErr(name string, err error) bool {
	if err == nil {
		return false
	}
	if writeErrChecked[name] && isClosedFdWriteErr(err) {
		return true
	}
	return pipeWriteErrChecked[name] && (isClosedFdWriteErr(err) || isBrokenPipeWriteErr(err))
}

func isBrokenPipeWriteErr(err error) bool {
	return isPlatformBrokenPipeWriteErr(err)
}

func writeErrDiagnostic(err error) string {
	if isBrokenPipeWriteErr(err) {
		return "Broken pipe"
	}
	return "Bad file descriptor"
}

// isClosedFdWriter reports whether w is the badFdWriter sentinel the runner
// installs into r.stdout/r.stderr to model a closed output fd (`N>&-`). Such a
// slot must behave as EBADF for any later dup of that fd.
func isClosedFdWriter(w io.Writer) bool {
	_, ok := w.(badFdWriter)
	return ok
}

func dupReadablePipeFile(w io.Writer) *os.File {
	w = unwrapPipelineWriter(w)
	f, ok := w.(*os.File)
	if !ok {
		return nil
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil
	}
	return f
}

func (r *Runner) redir(ctx context.Context, rd *syntax.Redirect) (closer io.Closer, redirErr error) {
	defer func() {
		if closer != nil {
			closer = r.bashPPFIFOCloser(closer)
		}
	}()
	// Bash reports a failed-open redirection error at the line of the
	// redirect operator, not the enclosing statement's first line. This
	// only shows up on compound commands whose redirect sits on a later
	// line, e.g. `while ...; do ...; done < missing`, where bash points at
	// the `done <` line rather than the `while` line. r.open() is the sole
	// consumer of curStmtPos reachable from here (every other error in this
	// function already uses rd.Pos()/rd.Word.Pos()), so steering it at the
	// operator position fixes the message without affecting anything else.
	// The subshell branch in stmtSync applies its own empirical line+1
	// quirk for while/for redirects, so leave curStmtPos alone there.
	if r.subshellLevel == 0 && rd.OpPos.IsValid() {
		oldStmtPos := r.curStmtPos
		r.curStmtPos = rd.OpPos
		defer func() { r.curStmtPos = oldStmtPos }()
	}
	// Heredoc operator (`<<TAG`, `<<-TAG`) with an empty body
	// parses to rd.Hdoc == nil. Still route an empty reader to
	// stdin so the consumer (`read`, `cat`, …) sees EOF cleanly
	// instead of treating the tag name as a filename to open.
	if rd.Hdoc != nil || rd.Op == syntax.Hdoc || rd.Op == syntax.DashHdoc {
		pr, err := r.hdocReader(rd)
		if err != nil {
			return nil, err
		}
		// `exec {v}<<EOF` form: route the heredoc reader through a
		// fresh fd and stash that fd in $v (globally — bash 5.3
		// makes named-fd assignments visible outside any enclosing
		// function).
		if rd.N != nil {
			val := rd.N.Value
			if strings.HasPrefix(val, "{") && strings.HasSuffix(val, "}") {
				name := val[1 : len(val)-1]
				fd, ok := r.allocateFd()
				if !ok {
					r.namedFdAllocError(rd, "")
					return nil, fmt.Errorf("cannot duplicate fd")
				}
				if err := r.setReadFd(fd, pr); err != nil {
					return nil, err
				}
				r.setGlobalNamedFdVarString(name, strconv.Itoa(fd))
				return pr, nil
			}
			// `cmd N<<TAG` with a numeric fd routes the heredoc
			// reader through fd N instead of stdin. This lets
			// multi-heredoc commands like `while read l <&3; …;
			// done <<EOF1 3<<EOF2` work — both heredocs become
			// distinct fds.
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				if err := r.setReadFd(n, pr); err != nil {
					return nil, err
				}
				return pr, nil
			}
		}
		r.stdin = pr
		r.stdinClosed = false
		r.stdinRedirected = true // heredoc body wins over the stdin-source reader
		r.asyncStdinExplicit = true
		return pr, nil
	}

	// Bash: when the target word of a non-heredoc redirect expands
	// to zero or more than one field, emit "ambiguous redirect".
	// POSIX mode skips field splitting for redirect words, so the
	// expanded word is always a single filename. Quote removal still
	// applies, so an unquoted backslash escape (`<\i'n'"0"` → `in0`)
	// or trailing `\` is collapsed in the filename operand — without it
	// the literal `\in0` would be opened (and fail).
	// Skip the check for here-string (`<<<`) since the entire word
	// is treated as the body, not a filename.
	var arg string
	if rd.Op != syntax.WordHdoc {
		if r.opts[optPosix] {
			arg = r.literalWithQuoteRemoval(rd.Word)
		} else {
			fields := r.fields(rd.Word)
			if len(fields) != 1 {
				r.ambiguousRedirect(rd.Word.Pos(), rd.Word)
				return nil, fmt.Errorf("ambiguous redirect")
			}
			arg = fields[0]
		}
	} else {
		arg = r.literal(rd.Word)
	}
	if r.opts[optRestricted] {
		switch rd.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob,
			syntax.AppClob, syntax.RdrAll, syntax.RdrAllClob, syntax.AppAll,
			syntax.AppAllClob:
			r.errf("%s%s: restricted: cannot redirect output\n",
				r.bashErrPrefix(rd.Word.Pos()), arg)
			return nil, fmt.Errorf("restricted redirect")
		}
	}
	// targetFd is the fd this redirect operates on. -1 means "use the
	// op's natural default" (fd 0 for input, fd 1 for output). N >= 3
	// goes through fdTable; 1/2 are stdin/stdout/stderr.
	targetFd := -1
	var namedFDVar string // non-empty if this is a {varname} redirect to be written back
	if rd.N != nil {
		val := rd.N.Value
		// Named FD redirection: {varname}> or {varname}<
		if strings.HasPrefix(val, "{") && strings.HasSuffix(val, "}") {
			name := val[1 : len(val)-1]
			// `{var}>&-` and `{var}<&-` are the close form: read the
			// fd already stored in $var, target it for deletion, and
			// don't write back (we keep $var with its stale number,
			// matching bash).
			if (rd.Op == syntax.DplOut || rd.Op == syntax.DplIn) && arg == "-" {
				fdStr := r.namedFdVarString(name)
				n, err := strconv.Atoi(fdStr)
				if err != nil || n < 0 {
					// Bash 5.3: `exec {v}>&-` with $v unset
					// or non-numeric emits `v: ambiguous
					// redirect` (single error message).
					r.errf("%s%s: ambiguous redirect\n", r.bashErrPrefix(rd.Pos()), name)
					return nil, fmt.Errorf("%s: ambiguous redirect", name)
				}
				targetFd = n
			} else {
				// Bash 5.3 refuses `{var}>...` when var is readonly,
				// emitting two diagnostics — `<file>: line N: <var>:
				// readonly variable` and `<file>: line N: <var>:
				// cannot assign fd to variable` — before abandoning
				// the redirect.
				if r.lookupVar(name).ReadOnly {
					r.readonlyNamedFdOpenSideEffect(ctx, rd, arg)
					prefix := r.bashErrPrefix(rd.Pos())
					r.errf("%s%s: readonly variable\n", prefix, name)
					r.errf("%s%s: cannot assign fd to variable\n", prefix, name)
					return nil, fmt.Errorf("%s: cannot assign fd to variable", name)
				}
				// Open form: pick a fresh fd for the script.
				var ok bool
				targetFd, ok = r.allocateFd()
				if !ok {
					r.namedFdAllocError(rd, arg)
					return nil, fmt.Errorf("cannot duplicate fd")
				}
				// Bash 5.3 refuses `{var}>...` when var is a nameref with
				// no target: storing the fd number sets the reference
				// target, but a bare number is not a valid identifier, so
				// it emits `exec: `N': not a valid identifier` and `<var>:
				// cannot assign fd to variable` and abandons the redirect.
				if v := r.lookupVar(name); v.Kind == expand.NameRef && v.Str == "" {
					prefix := r.bashErrPrefix(rd.Pos())
					r.errf("%sexec: `%d': not a valid identifier\n", prefix, targetFd)
					r.errf("%s%s: cannot assign fd to variable\n", prefix, name)
					return nil, fmt.Errorf("%s: cannot assign fd to variable", name)
				}
				namedFDVar = name
			}
		} else {
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("unsupported redirect fd: %v", val)
			}
			targetFd = n
		}
	}
	// dup2-style redirections can only target descriptors below
	// RLIMIT_NOFILE. Reject them before recording a sparse virtual fd;
	// otherwise a later external command would need an attacker-sized
	// ExtraFiles slice to preserve its numeric position. Bash treats closing
	// an out-of-range descriptor as a successful no-op, so retain that case.
	closingFd := (rd.Op == syntax.DplOut || rd.Op == syntax.DplIn) && arg == "-"
	if targetFd >= r.effectiveNofileLimit() && !closingFd {
		label := strconv.Itoa(targetFd)
		if rd.Op == syntax.DplOut || rd.Op == syntax.DplIn {
			label = redirWordText(rd)
		}
		r.errf("%s%s: Bad file descriptor\n", r.bashErrPrefix(rd.Word.Pos()), label)
		return nil, fmt.Errorf("%s: Bad file descriptor", label)
	}
	openFile := func(path string, flags int, mode os.FileMode, print bool) (io.ReadWriteCloser, error) {
		return r.bashPPTaskOpen(ctx, path, flags, mode, print, false)
	}
	switch rd.Op {
	case syntax.WordHdoc:
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		// hdoc routes to the input slot (default fd 0); allow N<<EOF too.
		if err := r.setReadFd(targetFd, pr); err != nil {
			return nil, err
		}
		if namedFDVar != "" {
			r.setGlobalNamedFdVarString(namedFDVar, strconv.Itoa(targetFd))
		}
		input := arg + "\n"
		if len(input) <= 512 {
			if _, err := io.WriteString(pw, input); err != nil {
				pw.Close()
				pr.Close()
				return nil, err
			}
			pw.Close()
			return pr, nil
		}
		// We write larger payloads to the pipe in a new goroutine,
		// as pipe writes may block once the buffer gets full.
		go func() {
			io.WriteString(pw, input)
			pw.Close()
		}()
		return pr, nil
	case syntax.DplOut:
		// >&M — point the target fd at whatever fd M references.
		switch arg {
		case "-":
			if targetFd == -1 {
				targetFd = 1
			}
			r.closeFd(targetFd)
			return nil, nil
		}
		closeSource := false
		if strings.HasSuffix(arg, "-") {
			closeSource = true
			arg = strings.TrimSuffix(arg, "-")
		}
		sourceFd, err := strconv.Atoi(arg)
		if err != nil {
			if targetFd == -1 {
				f, err := openFile(arg, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666, true)
				if err != nil {
					return nil, err
				}
				r.stdout = f
				r.stderr = f
				return f, nil
			}
			f, err := openFile(arg, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666, true)
			if err != nil {
				return nil, err
			}
			if err := r.setWriteFd(targetFd, f); err != nil {
				f.Close()
				return nil, err
			}
			if namedFDVar != "" {
				r.setGlobalNamedFdVarString(namedFDVar, strconv.Itoa(targetFd))
			}
			return f, nil
		}
		if sourceFd < 0 {
			// Bash: negative fd in `>&N` is an "ambiguous
			// redirect" before any open.
			r.errf("%s%s: ambiguous redirect\n", r.bashErrPrefix(rd.Pos()), arg)
			return nil, fmt.Errorf("ambiguous redirect")
		}
		if targetFd == -1 {
			targetFd = 1
		}
		if err := r.dupFd(targetFd, sourceFd, rd); err != nil {
			return nil, err
		}
		if closeSource && sourceFd != targetFd {
			r.closeFd(sourceFd)
			if r.redirMoveCloseFds == nil {
				r.redirMoveCloseFds = make(map[int]bool)
			}
			r.redirMoveCloseFds[sourceFd] = true
		}
		if namedFDVar != "" {
			r.setGlobalNamedFdVarString(namedFDVar, strconv.Itoa(targetFd))
		}
		return nil, nil
	case syntax.DplIn:
		// <&M — point the target input fd at fd M's reader.
		if arg == "-" {
			if targetFd == -1 {
				targetFd = 0
			}
			r.closeFd(targetFd)
			return nil, nil
		}
		closeSource := false
		if strings.HasSuffix(arg, "-") {
			closeSource = true
			arg = strings.TrimSuffix(arg, "-")
		}
		sourceFd, err := strconv.Atoi(arg)
		if err != nil {
			return nil, fmt.Errorf("unhandled %v arg: %q", rd.Op, arg)
		}
		if sourceFd < 0 {
			// Bash: negative fd in `<&N` is an "ambiguous redirect".
			r.errf("%s%s: ambiguous redirect\n", r.bashErrPrefix(rd.Pos()), arg)
			return nil, fmt.Errorf("ambiguous redirect")
		}
		if targetFd == -1 {
			targetFd = 0
		}
		if err := r.dupFd(targetFd, sourceFd, rd); err != nil {
			return nil, err
		}
		if closeSource && sourceFd != targetFd {
			r.closeFd(sourceFd)
			if r.redirMoveCloseFds == nil {
				r.redirMoveCloseFds = make(map[int]bool)
			}
			r.redirMoveCloseFds[sourceFd] = true
		}
		if namedFDVar != "" {
			r.setGlobalNamedFdVarString(namedFDVar, strconv.Itoa(targetFd))
		}
		return nil, nil
	case syntax.RdrIn, syntax.RdrOut, syntax.AppOut,
		syntax.RdrAll, syntax.AppAll,
		syntax.RdrClob, syntax.AppClob,
		syntax.RdrAllClob, syntax.AppAllClob,
		syntax.RdrInOut:
		// File-opening fall through.
		// The "Clob" variants (>|, >>|, &>|, &>>|) bypass the
		// noclobber shell option (set -C).
		// RdrInOut (<>) opens the target file for read+write and binds it
		// to the input fd (default 0); we read from the resulting file as
		// stdin. Writes back through fd 0 are not propagated to the file
		// since stdin is plumbed as io.Reader internally.
	default:
		return nil, fmt.Errorf("unhandled redirect op: %v", rd.Op)
	}
	mode := os.O_RDONLY
	switch rd.Op {
	case syntax.AppOut, syntax.AppAll, syntax.AppClob, syntax.AppAllClob:
		mode = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	case syntax.RdrOut, syntax.RdrAll, syntax.RdrClob, syntax.RdrAllClob:
		mode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	case syntax.RdrInOut:
		mode = os.O_RDWR | os.O_CREATE
	}
	if r.opts[optNoClobber] {
		switch rd.Op {
		case syntax.RdrOut, syntax.RdrAll:
			info, err := r.stat(ctx, arg)
			if err == nil {
				if info.Mode().IsRegular() {
					r.errf("%s%s: cannot overwrite existing file\n", r.bashErrPrefix(rd.Word.Pos()), arg)
					return nil, fmt.Errorf("%s: cannot overwrite existing file", arg)
				}
			} else {
				mode = os.O_WRONLY | os.O_CREATE | os.O_EXCL
			}
		}
	}
	// Bash's /dev/tcp and /dev/udp are shell redirection pseudo-paths,
	// rather than filesystem entries. Mark this call so DefaultOpenHandler can
	// implement them while ordinary opens (such as `source /dev/tcp/...`) keep
	// their regular file semantics. This follows the runner dialect rather than
	// the live POSIX option so `bash --posix` keeps Bash's pseudo-paths while a
	// strict POSIX runner treats the operand as an ordinary file.
	if r.devNetworkRedirectsEnabled() {
		ctx = context.WithValue(ctx, devNetworkRedirectCtxKey{}, true)
	}
	f, err := openFile(arg, mode, 0o666, true)
	if err != nil {
		return nil, err
	}
	if mode&(os.O_WRONLY|os.O_RDWR) != 0 {
		if file, ok := f.(*os.File); ok {
			if info, statErr := file.Stat(); statErr == nil && info.Mode()&os.ModeNamedPipe != 0 {
				f = fifoWriteFile{File: file, path: arg}
			}
		}
	}
	switch rd.Op {
	case syntax.RdrIn, syntax.RdrInOut:
		_, ttyFallback := f.(*ttyFallbackFile)
		stdin, err := stdinFile(f)
		if err != nil {
			return nil, err
		}
		if rd.Op == syntax.RdrInOut {
			writeFd := targetFd
			if writeFd == -1 {
				writeFd = 0
			}
			if err := r.bindReadWriteFd(writeFd, stdin); err != nil {
				return nil, err
			}
		} else if err := r.setReadFd(targetFd, stdin); err != nil {
			return nil, err
		}
		if targetFd == -1 || targetFd == 0 {
			r.stdinTTYFallback = ttyFallback
			r.stdinDevTTY = arg == "/dev/tty"
		}
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.AppClob:
		if err := r.setWriteFd(targetFd, f); err != nil {
			return nil, err
		}
	case syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
		// &> and &>> redirect both stdout and stderr; rd.N is ignored.
		r.stdout = f
		r.stderr = f
	default:
		return nil, fmt.Errorf("unhandled redirect op: %v", rd.Op)
	}
	// For named FD redirections that opened a real fd, write the
	// allocated fd number to the variable. Bash callers then use it
	// via `>&$var` / `<&$var` (which hit the numeric arg branch above).
	if namedFDVar != "" {
		r.setGlobalNamedFdVarString(namedFDVar, strconv.Itoa(targetFd))
	}
	return f, nil
}

// commandHasCmdSubst reports whether a command contains a command
// substitution (`$(...)` or backticks). Only such an expansion can read or
// write observable state before redirections on the same statement are
// applied, so it is the only case that needs redirections deferred until after
// expansion (the POSIX order).
func commandHasCmdSubst(cmd syntax.Command) bool {
	if cmd == nil {
		return false
	}
	found := false
	syntax.Walk(cmd, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.CmdSubst); ok {
			found = true
		}
		return !found
	})
	return found
}

func (r *Runner) applyLateRedirs(ctx context.Context, fields []string) bool {
	if r.lateRedirs == nil {
		return true
	}
	redirs := r.lateRedirs
	r.lateRedirs = nil
	for _, rd := range redirs {
		cls, err := r.redir(ctx, rd)
		if err != nil {
			r.exit.code = 1
			if r.opts[optPosix] && len(fields) > 0 && isPosixSpecialBuiltin(fields[0]) {
				r.exit.exiting = true
			}
			break
		}
		if cls != nil {
			r.lateRedirClosers = append(r.lateRedirClosers, cls)
		}
	}
	return r.exit.ok()
}

func redirWordText(rd *syntax.Redirect) string {
	var b bytes.Buffer
	if rd.Word != nil {
		syntax.NewPrinter().Print(&b, rd.Word)
	}
	return b.String()
}

func isNamedFdRedir(rd *syntax.Redirect) bool {
	if rd == nil || rd.N == nil {
		return false
	}
	val := rd.N.Value
	return strings.HasPrefix(val, "{") && strings.HasSuffix(val, "}")
}

func splitArrayElemName(name string) (base, index string, ok bool) {
	base, rest, ok := strings.Cut(name, "[")
	if !ok || !strings.HasSuffix(rest, "]") || !syntax.ValidName(base) {
		return "", "", false
	}
	index = strings.TrimSuffix(rest, "]")
	if index == "" {
		return "", "", false
	}
	return base, index, true
}

func (r *Runner) namedFdVarString(name string) string {
	if base, index, ok := splitArrayElemName(name); ok {
		vr := r.lookupVar(base)
		if vr.Kind == expand.Indexed {
			i, err := strconv.Atoi(index)
			if err == nil && vr.IndexedSet(i) {
				return vr.IndexedElem(i)
			}
		}
		if vr.Kind == expand.Associative && vr.Map != nil {
			return vr.Map[index]
		}
		return ""
	}
	vr := r.lookupVar(name)
	if _, resolved := vr.Resolve(r.writeEnv); resolved.Declared() {
		vr = resolved
	}
	return vr.String()
}

func (r *Runner) setGlobalNamedFdVarString(name, value string) {
	if base, index, ok := splitArrayElemName(name); ok {
		vr := r.lookupVar(base)
		if vr.Kind != expand.Associative {
			i, err := strconv.Atoi(index)
			if err != nil || i < 0 {
				return
			}
			if vr.Kind != expand.Indexed {
				vr = expand.Variable{Set: true, Kind: expand.Indexed}
			}
			vr.List = slices.Clone(vr.List)
			vr.ListSet = vr.CloneListSet()
			vr.ListMap = vr.CloneListMap()
			vr.SetIndexed(i, value)
			r.setVar(base, vr)
			r.setGlobalVar(base, vr)
			return
		}
		if vr.Map == nil {
			vr.Map = make(map[string]string)
		}
		vr.Map[index] = value
		r.setVar(base, vr)
		r.setGlobalVar(base, vr)
		return
	}
	r.setGlobalVarString(name, value)
}

func (r *Runner) setGlobalVar(name string, vr expand.Variable) {
	env := r.writeEnv
	for {
		ol, ok := env.(*overlayEnviron)
		if !ok || ol.parent == nil {
			break
		}
		nextWE, ok := ol.parent.(*overlayEnviron)
		if !ok {
			break
		}
		env = nextWE
	}
	if wenv, ok := env.(expand.WriteEnviron); ok {
		if err := wenv.Set(name, vr); err == nil {
			return
		}
	}
	r.setVar(name, vr)
}

func (r *Runner) namedFdAllocError(rd *syntax.Redirect, path string) {
	prefix := r.bashErrPrefix(rd.Pos())
	r.errf("%s: redirection error: cannot duplicate fd: Invalid argument\n", r.filename)
	if path != "" {
		r.errf("%s%s: Invalid argument\n", prefix, path)
	}
}

func (r *Runner) closeFd(fd int) {
	switch fd {
	case 0:
		r.stdin = nil
		// Mark fd 0 as genuinely closed (not merely absent) so a spawned
		// command reading stdin fails with EBADF, and suppress the
		// stdin-source script reader from re-supplying input.
		r.stdinClosed = true
		r.stdinRedirected = true
		r.asyncStdinExplicit = true
		if r.fdWriteTable != nil {
			delete(r.fdWriteTable, 0)
		}
	case 1:
		r.stdout = badFdWriter{}
		if r.fdTable != nil {
			delete(r.fdTable, 1)
		}
		if r.fdReadTable != nil {
			delete(r.fdReadTable, 1)
		}
	case 2:
		r.stderr = badFdWriter{}
		if r.fdTable != nil {
			delete(r.fdTable, 2)
		}
		if r.fdReadTable != nil {
			delete(r.fdReadTable, 2)
		}
	default:
		delete(r.fdTable, fd)
		delete(r.fdReadTable, fd)
		delete(r.fdWriteTable, fd)
		// If this fd backed a coproc array element, bash marks the closed
		// descriptor as -1 in `${NAME[@]}` (e.g. after
		// `exec {n}<&${COPROC[0]}-`). Mirror that.
		if ref, ok := r.coprocFds[fd]; ok {
			r.setCoprocFdElem(ref.varName, ref.idx, -1)
			delete(r.coprocFds, fd)
			delete(r.fdClosedTable, fd)
			return
		}
		// An explicit close owns the shell's descriptor view even when the
		// embedding Go process has an unregistered fd at the same number.
		// Record it unconditionally so /dev/fd/N and later duplication cannot
		// fall through to that host descriptor. Every bind/reopen path clears
		// this marker.
		if r.fdClosedTable == nil {
			r.fdClosedTable = make(map[int]bool)
		}
		r.fdClosedTable[fd] = true
	}
}

// setCoprocFdElem rewrites element idx of the coproc fd array varName to
// val, preserving the other element. A no-op if the variable is no longer
// a 2-element indexed array.
func (r *Runner) setCoprocFdElem(varName string, idx, val int) {
	cur := r.lookupVar(varName)
	if cur.Kind != expand.Indexed || len(cur.List) != 2 {
		return
	}
	list := []string{cur.List[0], cur.List[1]}
	list[idx] = strconv.Itoa(val)
	r.setVar(varName, expand.Variable{Set: true, Kind: expand.Indexed, List: list})
}

func (r *Runner) readonlyNamedFdOpenSideEffect(ctx context.Context, rd *syntax.Redirect, arg string) {
	mode := 0
	switch rd.Op {
	case syntax.RdrOut, syntax.RdrAll, syntax.RdrClob, syntax.RdrAllClob:
		mode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	case syntax.AppOut, syntax.AppAll, syntax.AppClob, syntax.AppAllClob:
		mode = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	case syntax.RdrInOut:
		mode = os.O_RDWR | os.O_CREATE
	default:
		return
	}
	f, err := r.open(ctx, arg, mode, 0o666, false)
	if err == nil {
		f.Close()
	}
}

func (r *Runner) functraceEnabled() bool {
	if enabled, ok := r.noOpSetState["functrace"]; ok {
		return enabled
	}
	return noOpSetOptions["functrace"]
}

func (r *Runner) errtraceEnabled() bool {
	if enabled, ok := r.noOpSetState["errtrace"]; ok {
		return enabled
	}
	return noOpSetOptions["errtrace"]
}

func (r *Runner) shouldFireDebugTrap() bool {
	if r.trapCallbacks["DEBUG"] == "" {
		return false
	}
	if n := len(r.callStack); n > 0 {
		// Inside a function the DEBUG trap fires only when the frame is
		// traced: the global functrace option, the function's own trace
		// attribute (`declare -ft`), or a `trap ... DEBUG` set within it.
		return r.callStack[n-1].debugTrace
	}
	return true
}

func (r *Runner) debugTrapLine(pos syntax.Pos) int {
	if r.handlingTrap && r.ecfg.OverrideLineno > 0 {
		return r.ecfg.OverrideLineno
	}
	return int(pos.Line())
}

// selectLoop implements bash's `select var in items; do ...; done`.
// Each iteration prints the numbered menu to stderr, prompts with PS3,
// reads a line into REPLY, sets var to items[N-1] when the reply is a
// valid integer 1..len(items) (otherwise var becomes empty), and runs
// the body. An empty reply re-displays the menu without running the
// body. EOF (Ctrl-D) exits the loop with exit code 1, matching bash.
// fireDebugTrap mirrors the DEBUG-trap firing done for simple commands
// in call(): bash also runs the trap before `[[ ]]`, `(( ))`, and the
// arithmetic parts of a C-style for loop. Reports whether extdebug is
// on and the trap returned 2, in which case the command is skipped.
func (r *Runner) fireDebugTrap(ctx context.Context, node syntax.Node) bool {
	if !r.shouldFireDebugTrap() {
		return false
	}
	prevLineno := r.ecfg.OverrideLineno
	prevStmtPos := r.curStmtPos
	r.ecfg.OverrideLineno = r.debugTrapLine(node.Pos())
	debugCode := r.trapCallback(ctx, r.trapCallbacks["DEBUG"], "debug")
	r.ecfg.OverrideLineno = prevLineno
	r.curStmtPos = prevStmtPos
	opt, _ := r.bashOptByName("extdebug")
	return opt != nil && *opt && debugCode == 2
}

func (r *Runner) selectLoop(ctx context.Context, name string, items []string, do []*syntax.Stmt) {
	// Bash 5.3: a `select` with an empty item list (because the
	// optional `in <list>` was omitted and `$@` is empty, or the
	// list expanded to nothing) exits immediately without
	// prompting.
	if len(items) == 0 {
		return
	}
	ps3 := shellDefaultPS3
	if e := r.envGet(shellReplyPS3Var); e != "" {
		ps3 = e
	}
	for {
		var reply string
		// Re-display menu until the user supplies a non-empty reply
		// (matching bash, which suppresses the body run on empty input).
		for {
			for i, word := range items {
				r.errf("%d) %s\n", i+1, word)
			}
			r.errf("%s", ps3)
			line, err := r.readLine(ctx, true, '\n')
			if err != nil {
				// EOF: exit the loop. Bash exits with status 1.
				r.errf("\n")
				r.exit.code = 1
				return
			}
			reply = string(line)
			r.setVarString(shellReplyVar, reply)
			if reply != "" {
				break
			}
		}
		// Bash binds the select variable after reading the reply; a
		// readonly variable aborts the select (and exits a POSIX-mode
		// shell), with the menu and prompt already printed.
		if r.lookupVar(name).ReadOnly {
			r.errf("%s%s: readonly variable\n",
				r.bashErrPrefix(r.curStmtPos), name)
			r.exit.code = 1
			if r.opts[optPosix] {
				r.exit.exiting = true
			}
			return
		}
		c, _ := strconv.Atoi(reply)
		val := ""
		if c > 0 && c <= len(items) {
			val = items[c-1]
		}
		if vr := r.lookupVar(name); vr.Kind == expand.NameRef {
			// Binding the chosen value through an untargeted nameref
			// retargets it; an invalid identifier is rejected without
			// touching the nameref.
			if vr.Str == "" && val != "" && !validNameRefTarget(val) {
				r.errf("%s`%s': not a valid identifier\n",
					r.bashErrPrefix(r.curStmtPos), val)
				r.exit.code = 1
				return
			} else {
				vr.Str = val
				r.setVar(name, vr)
			}
		} else {
			r.setVarString(name, val)
		}
		if r.loopStmtsBroken(ctx, do) {
			return
		}
	}
}

// errExitExemptByAndOr reports whether a command's non-zero exit is exempt
// from `set -e` because it ends in an AND-OR list. Bash exempts the failing
// left operand of `&&`/`||`, and that exemption propagates THROUGH a brace
// group `{ …; }` (same execution environment) — so `set -e; { false && true; }`
// does not exit. It does NOT propagate through a subshell `( … )` or a function
// call (those are boundaries and do fire), so only BinaryCmd and Block (whose
// last statement is itself exempt) qualify.
func errExitExemptByAndOr(cmd syntax.Command) bool {
	switch c := cmd.(type) {
	case *syntax.BashPPAgenticBlock:
		return errExitExemptByAndOr(c.Body)
	case *syntax.BinaryCmd:
		return c.Op == syntax.AndStmt || c.Op == syntax.OrStmt
	case *syntax.Block:
		if n := len(c.Stmts); n > 0 {
			return errExitExemptByAndOr(c.Stmts[n-1].Cmd)
		}
	}
	return false
}

func stmtsEndErrexitExempt(stmts []*syntax.Stmt) bool {
	if n := len(stmts); n > 0 {
		return errExitExemptByAndOr(stmts[n-1].Cmd)
	}
	return false
}

func (r *Runner) loopStmtsBroken(ctx context.Context, stmts []*syntax.Stmt) bool {
	oldInLoop := r.inLoop
	r.inLoop = true
	defer func() { r.inLoop = oldInLoop }()
	// Every loop body — while, until, for-in, C-style and select — runs one
	// iteration through here, so this is the one place a per-iteration block
	// has to be pushed. Fresh cells each time is what lets a closure defined
	// in the body capture ITS iteration rather than the loop's last one, and
	// what lets the body redeclare the same identifiers on the next pass.
	if r.bashPPScope != nil {
		defer r.bashPPPushScope()()
	}
	for _, stmt := range stmts {
		r.stmt(ctx, stmt)
		if r.contnEnclosing > 0 {
			r.contnEnclosing--
			if !oldInLoop {
				// `continue N` with N beyond the loop nesting continues the
				// outermost loop; no outer loop remains to consume the rest, so
				// clamp instead of leaking a pending continue past the loop.
				r.contnEnclosing = 0
				return false
			}
			return r.contnEnclosing > 0
		}
		if r.breakEnclosing > 0 {
			r.breakEnclosing--
			if !oldInLoop {
				// `break N` with N beyond the nesting breaks all enclosing
				// loops; clamp so no pending break leaks past the outermost one.
				r.breakEnclosing = 0
			}
			return true
		}
	}
	return false
}

func (r *Runner) loopControlPending() bool {
	return r.breakEnclosing > 0 || r.contnEnclosing > 0
}

// isStrictPosixIntrinsic reports whether name is an intrinsic utility —
// the POSIX.1-2024 (Issue 8) category of utilities that, like special
// builtins, may be invoked without a PATH search. The list mirrors yash's
// builtins-p.tst "intrinsic built-in ... can be invoked without $PATH"
// cases. In strict POSIX mode every other builtin is PATH-gated.
func isStrictPosixIntrinsic(name string) bool {
	switch name {
	case "alias", "bg", "cd", "command", "fc", "fg", "getopts", "hash", "jobs", "kill", "read", "type", "ulimit", "umask", "unalias", "wait":
		return true
	}
	return false
}

func (r *Runner) call(ctx context.Context, pos syntax.Pos, args []string) {
	if r.stop(ctx) {
		return
	}
	// Bash binds `_` to the last expanded word of every simple command,
	// after that command finishes. Doing this with a defer is important for
	// functions: commands in the function body may update `_`, but the
	// function invocation's own last argument wins when it returns.
	lastArg := args[len(args)-1]
	defer r.setVarString("_", lastArg)
	// Emulate bash's reader-level history recording and expansion: advance
	// the history cursor up to this command's source line. When the line was
	// history-expanded (the expansion ran in its place), skip the original.
	if r.histSync(ctx, pos) {
		return
	}
	// Set BASH_COMMAND to the expanded command while it executes.
	r.setVarString("BASH_COMMAND", strings.Join(args, " "))
	if r.callHandler != nil {
		var err error
		args, err = r.callHandler(r.handlerCtx(ctx, handlerKindCall, pos), args)
		if err != nil {
			// handler's custom fatal error
			r.exit.fatal(err)
			return
		}
	}
	name := args[0]
	if name == "[[" {
		if expr := parseReparsedTestClause(args[1:]); expr != nil {
			r.runTestClause(ctx, pos, expr, true)
			return
		}
	}
	// Bash POSIX mode: POSIX special builtins outrank shell
	// functions during command lookup. Skip the function dispatch
	// so a function defined before POSIX mode was enabled doesn't
	// shadow the builtin (`break`, `return`, `exit`, …).
	if r.opts[optPosix] && strings.ContainsRune(name, '/') {
		// Bash POSIX mode accepts function definitions whose names
		// contain '/', but never executes them as shell functions:
		// invocations are sent through external command lookup. In
		// default (non-POSIX) mode bash DOES execute a slash-named
		// function (e.g. `function /bin/echo { …; }`), so only skip
		// the function dispatch when POSIX mode is enabled.
	} else if r.opts[optPosix] && isPosixSpecialBuiltin(name) && IsBuiltin(name) {
		// fall through to builtin/exec dispatch below
	} else if body := r.Funcs[name]; body != nil {
		marked := r.bashPPAgenticFunc(name)
		if marked && !r.bashPPAgentic {
			r.bashPPAgenticCallError(pos, name)
			return
		}
		savedAgentic := r.bashPPAgentic
		r.bashPPAgentic = marked
		defer func() { r.bashPPAgentic = savedAgentic }()
		releaseBgPid(ctx)
		// Honor $FUNCNEST: when set to a positive integer, bash aborts
		// once nesting reaches that depth. An unset, empty, zero, or
		// non-numeric value disables the limit.
		if limit := r.bashPPFuncNest(); limit > 0 && len(r.callStack) >= limit {
			r.errf("%s%s: maximum function nesting level exceeded (%d)\n",
				r.bashErrPrefix(pos), name, limit)
			r.exit.code = 1
			return
		}

		// stack them to support nested func calls
		oldParams := r.Params
		r.Params = args[1:]
		oldInFunc := r.inFunc
		r.inFunc = true
		// Bash 5.3: if OPTIND is local in the called function (via
		// `typeset OPTIND=1` or similar), getopts processes the
		// nested args independently of the caller, and on return the
		// caller's getopts state is restored. We model that by
		// snapshotting r.optState and restoring it at return.
		oldOptState := r.optState
		// $LINENO override only applies to the trap text itself,
		// not to functions called from the trap — those should see
		// their own body line numbers.
		oldOverrideLineno := r.ecfg.OverrideLineno
		r.ecfg.OverrideLineno = 0
		// A function called from within `eval` reports diagnostics at
		// its own body line, not the eval call's line, so the eval
		// line offset does not leak into the function body.
		oldEvalLineOffset := r.evalLineOffset
		r.evalLineOffset = 0
		oldFilename := r.filename
		if source := r.funcSources[name]; source != "" {
			r.filename = source
		}

		// Push call stack frame.
		frameDebugTrace := r.functraceEnabled() || r.funcTrace[name]
		r.callStack = append(r.callStack, callFrame{
			line:       pos.Line(),
			source:     oldFilename,
			funcName:   name,
			args:       slices.Clone(args[1:]),
			bodyLine:   body.Pos().Line(),
			debugTrace: frameDebugTrace,
		})
		r.localOptStack = append(r.localOptStack, localOptFrame{
			opts:         r.opts,
			noOpSetState: maps.Clone(r.noOpSetState),
		})

		// Shell functions inherit the RETURN trap if function tracing is on
		// globally or on individually for this function.
		oldReturnTrap := r.trapCallbacks["RETURN"]
		if !frameDebugTrace {
			delete(r.trapCallbacks, "RETURN")
		}

		// Functions run in a nested scope.
		// Note that [Runner.exec] below does something similar.
		origEnv := r.writeEnv
		funEnv := newFuncScopeEnviron(r.writeEnv, false)
		r.writeEnv = funEnv
		// A function body's free identifiers resolve where the function was
		// DEFINED, not where it is called; r.bashPPFuncScopes holds that
		// environment. A function defined outside the dialect, or imported
		// from BASH_FUNC_*, has no capture and falls back to the caller's
		// scope, which is the only environment it could have meant.
		origBashPPScope := r.bashPPScope
		if r.bashPPScope != nil {
			captured := r.bashPPFuncScopes[name]
			if captured == nil {
				captured = origBashPPScope
			}
			r.bashPPScope = newBashPPScope(captured)
		}

		if r.shouldFireDebugTrap() {
			prevLineno := r.ecfg.OverrideLineno
			r.ecfg.OverrideLineno = r.debugTrapLine(body.Pos())
			r.trapCallback(ctx, r.trapCallbacks["DEBUG"], "debug")
			r.ecfg.OverrideLineno = prevLineno
		}
		// A shell function is a break/continue boundary: `break`/`continue`
		// inside the function is scoped to the function's own loops, NOT the
		// caller's (bash: `f(){ break; }; for i in 1 2 3 4 5; do f; done; echo $i`
		// prints 5). Reset the loop-control state across the body so a stray
		// loop-control can't leak out to the caller's loop.
		oldBreakEnc, oldContnEnc := r.breakEnclosing, r.contnEnclosing
		r.breakEnclosing, r.contnEnclosing = 0, 0
		if decorated := r.bashPPDecoratedShellFunc(name); decorated != nil {
			// Decorators wrap the body, never the gates: the chain runs
			// after the agentic and FUNCNEST checks above and inside the
			// DEBUG/RETURN trap bracket, so RETURN fires once.
			r.bashPPCallDecorated(ctx, name, decorated, args[1:], func(ctx context.Context) {
				r.breakEnclosing, r.contnEnclosing = 0, 0
				r.stmt(ctx, body)
			})
		} else {
			r.stmt(ctx, body)
		}
		r.breakEnclosing, r.contnEnclosing = oldBreakEnc, oldContnEnc
		if r.exit.exiting && r.trapCallbacks["EXIT"] != "" {
			r.exitTrapCallStack = slices.Clone(r.callStack)
		}

		funEnv.release()
		r.writeEnv = origEnv
		r.bashPPScope = origBashPPScope

		if r.trapCallbacks["RETURN"] != "" {
			prevLineno := r.ecfg.OverrideLineno
			r.ecfg.OverrideLineno = int(body.Pos().Line())
			r.trapCallback(ctx, r.trapCallbacks["RETURN"], "return")
			r.ecfg.OverrideLineno = prevLineno
		}
		if !frameDebugTrace {
			if r.trapCallbacks["RETURN"] == "" {
				if oldReturnTrap != "" {
					r.trapCallbacks["RETURN"] = oldReturnTrap
				}
			}
		}
		r.callStack = r.callStack[:len(r.callStack)-1]
		if n := len(r.localOptStack); n > 0 {
			frame := r.localOptStack[n-1]
			r.localOptStack = r.localOptStack[:n-1]
			if frame.active {
				r.opts = frame.opts
				r.noOpSetState = frame.noOpSetState
				r.setIgnoreEOFOption(r.noOpSetState["ignoreeof"])
			}
		}
		r.Params = oldParams
		r.inFunc = oldInFunc
		r.optState = oldOptState
		r.ecfg.OverrideLineno = oldOverrideLineno
		r.evalLineOffset = oldEvalLineOffset
		r.filename = oldFilename
		if r.bashPPPanicking() {
			// A panic ABANDONS this frame rather than returning from it, so
			// the rule that a function consumes its own `return` does not
			// apply: the unwind continues into the caller.
			r.bashPPUnwind()
		} else {
			r.exit.returning = false
		}
		return
	}
	if module, fn, ok := r.bashPPForeignCommand(name); ok {
		// An island function as a command word sits where a shell function
		// does in the lookup order; `command NAME` skips it (B8).
		releaseBgPid(ctx)
		r.emitAudit("island", pos, args, true)
		r.bashPPRunForeignCommand(ctx, pos, module, name, fn, args[1:])
		return
	}
	if IsBuiltin(name) && !r.disabledBuiltins[name] {
		// Strict POSIX: non-special, non-intrinsic builtins must be found in PATH.
		// `builtin` is Bash's explicit in-process dispatcher, not a utility
		// resolved through PATH. Bash keeps it available in POSIX mode even
		// when PATH is unset; its operand is then dispatched directly as a
		// builtin as well. Keep this Bash extension distinct from the POSIX
		// intrinsic-utility classification above while preserving that lookup
		// contract.
		if r.strictPosix && name != "builtin" && !isPosixSpecialBuiltin(name) && !isStrictPosixIntrinsic(name) {
			if _, err := LookPathDir(r.Dir, r.writeEnv, name); err != nil {
				releaseBgPid(ctx)
				r.errf("%s%s: command not found\n", r.bashErrPrefix(pos), name)
				r.exit.code = 127
				return
			}
		}

		// `command` is a transparent wrapper when its operand resolves to an
		// external command. Keep pidReady open until that nested dispatch either
		// publishes the OS child PID or reaches a terminal non-exec branch.
		if name != "command" {
			releaseBgPid(ctx)
		}
		r.emitAudit("builtin", pos, args, true)
		r.exit = r.builtin(ctx, pos, name, args[1:])
		if r.opts[optPosix] {
			r.posixSpecialBuiltinFatal(name, args[1:])
		}
		return
	}
	// autocd: if command not found but is a directory, cd to it.
	if opt, _ := r.bashOptByName("autocd"); opt != nil && *opt {
		if info, err := r.stat(ctx, name); err == nil && info.IsDir() {
			releaseBgPid(ctx)
			r.exit = r.builtin(ctx, pos, "cd", []string{name})
			return
		}
	}
	// Bash NOTFOUND_HOOK (execute_cmd.c): a command name with no slash
	// that is not found in $PATH invokes the `command_not_found_handle`
	// function, if defined, with the original words, and uses its exit
	// status. Names containing a slash bypass the hook and go straight to
	// the exec path so they report dir/perm/ENOENT diagnostics instead.
	if body := r.Funcs["command_not_found_handle"]; body != nil &&
		!strings.ContainsRune(name, '/') {
		if _, err := LookPathDir(r.Dir, r.writeEnv, name); err != nil {
			releaseBgPid(ctx)
			r.call(ctx, pos, append([]string{"command_not_found_handle"}, args...))
			return
		}
	}
	r.exec(ctx, pos, args)
}

func (r *Runner) exec(ctx context.Context, pos syntax.Pos, args []string) {
	r.execAs(ctx, pos, "", false, false, args)
}

// execStartError reports whether the `exec NAME` builtin would fail to
// start NAME, returning the bash-style diagnostic tail (the part after the
// `<file>: line N: ` prefix) and exit code. It mirrors the `command == 0`
// branch of exec.def (no-slash names: `exec: NAME: not found` / `exec:
// NAME: cannot execute: Is a directory`) and the shell_execve errno cases
// for slash names (`NAME: No such file or directory` etc.). The bool is
// false when NAME can be started, in which case execve takes over.
//
// This pre-check is what keeps a refused exec from destroying the shell.
// On Unix a failed execve returns into an untouched process, but Windows
// has no execve: `exec CMD` starts CMD and exits the shell in its place,
// and that exit runs the EXIT trap and discards every trap and inherited
// ignore. The shell must therefore learn of a refusal here, before
// committing to the replacement (builtin.go sets exit.exiting ahead of
// execAs), so `shopt -s execfail` keeps the same shell — same traps, same
// the ignored TERM — alive afterwards (exec3.sub).
func (r *Runner) execStartError(ctx context.Context, name string) (string, uint8, bool) {
	return r.execStartErrorMode(ctx, name, runtime.GOOS == "windows")
}

// execStartErrorMode is execStartError with the platform rule under the
// caller's control, so the Windows half is testable from any host.
func (r *Runner) execStartErrorMode(ctx context.Context, name string, windows bool) (string, uint8, bool) {
	if strings.ContainsRune(name, '/') {
		info, err := r.stat(ctx, name)
		if err != nil {
			if os.IsPermission(err) {
				return name + ": Permission denied", 126, true
			}
			return name + ": No such file or directory", 127, true
		}
		if info.IsDir() {
			return name + ": Is a directory", 126, true
		}
		if windows {
			// Windows has no execute bit for the 0o111 test below. Apply
			// the same executable-file rule the default handler's lookup
			// applies (a recorded ACL mode when chmod set one, else the
			// Cygwin name rule — findExecutable/windowsExecutableFile), so
			// this pre-check refuses exactly what the handler would refuse
			// after the shell had already committed to being replaced.
			if _, lerr := lookPathDirMode(r.Dir, r.writeEnv, name, findExecutable, true); lerr != nil {
				return name + ": Permission denied", 126, true
			}
		} else if info.Mode()&0o111 == 0 {
			return name + ": Permission denied", 126, true
		}
		return "", 0, false
	}
	if _, err := LookPathDir(r.Dir, r.writeEnv, name); err != nil {
		if info, serr := r.stat(ctx, name); serr == nil && info.IsDir() {
			return "exec: " + name + ": cannot execute: Is a directory", 126, true
		}
		return "exec: " + name + ": not found", 127, true
	}
	return "", 0, false
}

// execAs is like exec but advertises argv0 to the exec handler via
// [HandlerContext.ExecAs], so handlers can launch the spawned process
// under a different argv[0] (the "exec -a NAME CMD" form in bash).
// An empty argv0 means no override.
func (r *Runner) execAs(ctx context.Context, pos syntax.Pos, argv0 string, clearEnv bool, replace bool, args []string) {
	hashed := false
	// unhashed: a registered command (schema) or a name served in process
	// is not a PATH file, so the hash table never applies to it.
	unhashed := false
	if len(args) > 0 {
		name := args[0]
		if r.commandResolver != nil && !strings.ContainsRune(name, '/') {
			if rc, ok := r.commandResolver(name); ok && rc.Schema != nil {
				bound, err := bindCommandSchema(name, rc.Schema, args)
				if err != nil {
					msg := fmt.Sprintf("%s%s\n", r.bashErrPrefix(pos), err)
					r.errf("%s", msg)
					r.reportError("exec", pos, name, msg, 2)
					r.exit.code = 2
					return
				}
				args = bound.args
				unhashed = true
			}
		}
		newlyHashed := false
		if r.servedInProcess != nil && !strings.ContainsRune(name, '/') && r.servedInProcess(name) {
			// Served by the embedder in process, not from PATH: never hashed.
			unhashed = true
		}
		if entry, ok := r.cmdHashTable[name]; ok && !unhashed {
			// bash 5.3 findcmd.c search_for_command: a remembered path is
			// re-checked when `posixly_correct || check_hashed_filenames`;
			// if the file no longer exists or is not executable, the entry
			// is dropped and $PATH is searched again ("Thank you Posix.2").
			// Outside POSIX mode and without checkhash, the stale path is
			// executed as-is and fails.
			checkHash := r.opts[optPosix]
			if opt, _ := r.bashOptByName("checkhash"); opt != nil && *opt {
				checkHash = true
			}
			info, err := r.stat(ctx, entry.path)
			if err == nil && checkHash && runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				// exists but lost its execute bit: same re-search as a vanished file
				err = os.ErrNotExist
			}
			if err != nil && os.IsNotExist(err) {
				if checkHash {
					delete(r.cmdHashTable, name)
					if path, err := LookPathDir(r.Dir, r.writeEnv, name); err == nil {
						r.cmdHashTable[name] = cmdHashEntry{path: path}
						hashed = true
						args = append([]string{path}, args[1:]...)
						if argv0 == "" {
							argv0 = name
						}
					}
				} else {
					msg := fmt.Sprintf("%s%s: No such file or directory\n",
						r.bashErrPrefix(pos), entry.path)
					r.errf("%s", msg)
					r.reportError("exec", pos, entry.path, msg, 127)
					r.exit.code = 127
					return
				}
			} else if err != nil {
				msg := fmt.Sprintf("%s%s: %s\n",
					r.bashErrPrefix(pos), entry.path, err)
				r.errf("%s", msg)
				r.reportError("exec", pos, entry.path, msg, 126)
				r.exit.code = 126
				return
			}
		}
		// With hashall enabled (the default), bash remembers the resolved
		// location of every PATH-searched external command after its first
		// invocation. Explicit paths are never entered in the table.
		hashall := true
		if enabled, ok := r.noOpSetState["hashall"]; ok {
			hashall = enabled
		}
		if _, ok := r.cmdHashTable[name]; !ok && !unhashed && hashall && !strings.ContainsRune(name, '/') {
			if path, err := LookPathDir(r.Dir, r.writeEnv, name); err == nil {
				if r.cmdHashTable == nil {
					r.cmdHashTable = make(map[string]cmdHashEntry)
				}
				r.cmdHashTable[name] = cmdHashEntry{path: path, hits: 1}
				newlyHashed = true
			}
		}
		if entry, ok := r.cmdHashTable[args[0]]; ok && !unhashed && !hashed && !newlyHashed {
			hashed = true
			entry.hits++
			r.cmdHashTable[args[0]] = entry
			if r.opts[optRestricted] && entry.restricted && strings.Contains(entry.path, "/") {
				r.errf("%s%s: restricted\n", r.bashErrPrefix(pos), entry.path)
				r.exit.code = 1
				return
			}
			args = append([]string{entry.path}, args[1:]...)
			// A hash hit changes where execve finds the program, not the
			// command name supplied as argv[0]. Utilities commonly use argv[0]
			// in diagnostics, so exposing the cached absolute path makes a
			// second invocation observably differ from the first.
			if argv0 == "" {
				argv0 = name
			}
		}
	}
	if r.opts[optRestricted] && !hashed && len(args) > 0 && strings.Contains(args[0], "/") {
		msg := fmt.Sprintf("%s%s: restricted: cannot specify `/' in command names\n",
			r.bashErrPrefix(pos), args[0])
		r.errf("%s", msg)
		r.reportError("exec", pos, args[0], msg, 1)
		r.exit.code = 1
		return
	}
	if r.bashPPConcurrent != nil || r.bashPPIssuedHandles != nil {
		for _, arg := range args {
			if r.bashPPHasRuntimeHandle(arg) {
				r.errf("bash++: channel handles cannot cross an exec boundary\n")
				r.exit.code = 2
				return
			}
		}
		for _, vr := range r.bashPPEnv().Each {
			if vr.Exported && r.bashPPHasRuntimeHandle(vr.String()) {
				r.errf("bash++: channel handles cannot cross an exec boundary\n")
				r.exit.code = 2
				return
			}
		}
	}
	if !r.bashPPArmBeforeBlock(ctx) {
		return
	}
	hctx := r.handlerCtx(ctx, handlerKindExec, pos)
	if argv0 != "" {
		hc := HandlerCtx(hctx)
		hc.ExecAs = argv0
		hctx = context.WithValue(hctx, handlerCtxKey{}, hc)
	}
	if clearEnv {
		hc := HandlerCtx(hctx)
		hc.ExecClearEnv = true
		hctx = context.WithValue(hctx, handlerCtxKey{}, hc)
	}
	if replace && r.subshellLevel == 0 && !r.inBashPPTask() {
		hc := HandlerCtx(hctx)
		hc.ExecReplace = true
		hctx = context.WithValue(hctx, handlerCtxKey{}, hc)
	}
	r.emitAudit("exec", pos, args, false)
	var replacingBg *bgProc
	if replace {
		replacingBg, _ = ctx.Value(bgProcCtxKey{}).(*bgProc)
		if replacingBg != nil {
			replacingBg.execReplacing.Store(true)
			hctx = context.WithValue(hctx, execReplacingCtxKey{}, true)
		}
	}
	handlerErr := r.execHandler(hctx, args)
	if replacingBg != nil {
		// The outermost handler's ordinary status is authoritative: middleware
		// may translate or handle the raw status saved by DefaultExecHandler.
		// Leave that raw status intact only for fatal errors such as carrier
		// cancellation, where it preserves the replacement child's actual wait
		// result after the carrier delivered its signal.
		var builtinExit errBuiltinExitStatus
		var externalExit ExitStatus
		preserveResetSignal := false
		if n := replacingBg.carrierResetSignal.Load(); n > 0 {
			preserveResetSignal = replacingBg.execResult.Load() == 129+n
		}
		if !(preserveResetSignal && errors.Is(handlerErr, context.Canceled)) {
			switch {
			case handlerErr == nil:
				replacingBg.execResult.Store(1)
			case errors.As(handlerErr, &builtinExit):
				replacingBg.execResult.Store(int32(exitStatus(builtinExit).code) + 1)
			case errors.As(handlerErr, &externalExit):
				replacingBg.execResult.Store(int32(externalExit) + 1)
			}
		}
	}
	r.exit.fromHandlerError(handlerErr)
}

func (r *Runner) emitAudit(kind string, pos syntax.Pos, args []string, isBuiltin bool) {
	if len(args) == 0 || (r.auditHandler == nil && r.auditLog == nil) {
		return
	}
	when := time.Now()
	if r.deterministic {
		when = r.startTime
	}
	ev := AuditEvent{
		Kind:          kind,
		Args:          slices.Clone(args),
		Pos:           pos,
		When:          when,
		Filename:      r.filename,
		CallStackHash: r.callStackDigest(),
		EnvDigest:     r.envDigest(),
		IsBuiltin:     isBuiltin,
	}
	if r.auditHandler != nil {
		r.bashPPObserve(func() { r.auditHandler(ev) })
	}
	if r.auditLog != nil {
		if data, err := json.Marshal(ev); err == nil {
			data = append(data, '\n')
			r.bashPPWriter(r.auditLog).Write(data)
		}
	}
}

func parseReparsedTestClause(args []string) syntax.TestExpr {
	if len(args) == 0 || args[len(args)-1] != "]]" {
		return nil
	}
	if len(args) > 1 && testClauseBinaryOperatorArg(args[0]) {
		args = append([]string{""}, args...)
	}
	var b strings.Builder
	b.WriteString("[[")
	for _, arg := range args[:len(args)-1] {
		b.WriteByte(' ')
		if testClauseOperatorArg(arg) {
			b.WriteString(arg)
			continue
		}
		quoted, err := syntax.Quote(arg, syntax.LangBash)
		if err != nil {
			return nil
		}
		b.WriteString(quoted)
	}
	b.WriteString(" ]]")
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(b.String()), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil
	}
	tc, ok := file.Stmts[0].Cmd.(*syntax.TestClause)
	if !ok {
		return nil
	}
	return tc.X
}

func testClauseOperatorArg(arg string) bool {
	switch arg {
	case "!", "(", ")", "&&", "||",
		"==", "=", "!=", "<", ">", "=~":
		return true
	}
	if testClauseBinaryOperatorArg(arg) {
		return true
	}
	if len(arg) == 2 && arg[0] == '-' {
		switch arg[1] {
		case 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'k', 'n', 'p', 'r', 's', 't', 'u', 'w', 'x', 'O', 'G', 'L', 'S', 'v':
			return true
		}
	}
	return false
}

func testClauseBinaryOperatorArg(arg string) bool {
	switch arg {
	case "-eq", "-ne", "-lt", "-le", "-gt", "-ge",
		"-ef", "-nt", "-ot":
		return true
	}
	return false
}

func (r *Runner) runTestClause(ctx context.Context, pos syntax.Pos, expr syntax.TestExpr, trace bool) {
	if trace {
		r.traceTestClause(pos, expr)
	}
	r.testIntErr = ""
	r.testArithErr = ""
	result := r.bashTest(ctx, expr, false)
	if r.testArithErr != "" {
		r.errf("%s[[: %s: %s\n", r.bashErrPrefix(pos), r.testIntErr, r.testArithErr)
		r.testIntErr = ""
		r.testArithErr = ""
		r.exit.oneIf(result == "")
	} else if r.testIntErr != "" {
		r.errf(r.bashErrPrefix(pos)+"[[: %s: integer expected\n", r.testIntErr)
		r.testIntErr = ""
		r.exit.code = 2
	} else if result == "" {
		if r.exit.ok() {
			r.exit.code = 1
		}
		// Otherwise preserve exit status code 2 for regex errors, etc.
	} else {
		r.exit.code = 0
	}
}

func (r *Runner) callStackDigest() string {
	h := sha256.New()
	for _, frame := range r.callStack {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", frame.funcName, frame.source, frame.line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Runner) envDigest() string {
	h := sha256.New()
	var names []string
	r.writeEnv.Each(func(name string, vr expand.Variable) bool {
		names = append(names, name)
		return true
	})
	slices.Sort(names)
	for _, name := range names {
		vr := r.writeEnv.Get(name)
		fmt.Fprintf(h, "%s\x00%d\x00%t\x00%t\x00%t\x00%t\x00%s\x00", name, vr.Kind, vr.Set, vr.Exported, vr.ReadOnly, vr.Integer, vr.Str)
		for i, v := range vr.List {
			fmt.Fprintf(h, "%d\x00%s\x00", i, v)
		}
		if len(vr.ListMap) > 0 {
			idxs := make([]int, 0, len(vr.ListMap))
			for i := range vr.ListMap {
				idxs = append(idxs, i)
			}
			slices.Sort(idxs)
			for _, i := range idxs {
				fmt.Fprintf(h, "%d\x00%s\x00", i, vr.ListMap[i])
			}
		}
		if len(vr.Map) > 0 {
			keys := make([]string, 0, len(vr.Map))
			for k := range vr.Map {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fmt.Fprintf(h, "%s\x00%s\x00", k, vr.Map[k])
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

type ttyFallbackFile struct {
	read  *os.File
	write *os.File
}

func (r *Runner) onTerminal() bool {
	for _, f := range [...]any{r.stdin, r.stdout, r.stderr} {
		if _, ok := f.(*ttyFallbackFile); ok {
			return true
		}
		if f, ok := f.(interface{ Fd() uintptr }); ok && term.IsTerminal(int(f.Fd())) {
			return true
		}
	}
	return false
}

func (f *ttyFallbackFile) Read(p []byte) (int, error) {
	return f.read.Read(p)
}

func (f *ttyFallbackFile) Write(p []byte) (int, error) {
	return 0, fs.ErrInvalid
}

func (f *ttyFallbackFile) Close() error {
	err1 := f.read.Close()
	err2 := f.write.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

type borrowedFile struct {
	*os.File
}

func (f borrowedFile) Close() error { return nil }

type fifoWriteFile struct {
	*os.File
	path string
}

func (f fifoWriteFile) Close() error {
	// POSIX requires writes through a FIFO to update its mtime/ctime.
	// Linux pipe writes leave the FIFO node timestamps untouched, so refresh
	// them when a shell-managed write redirection is closed.
	_ = refreshFileTimesNow(f.File, f.path)
	err := f.File.Close()
	return err
}

// customOpenActive follows the same dynamic selection as open, so an inactive
// dry-run override cannot disable native task opens or FIFO rendezvous.
func (r *Runner) customOpenActive() bool {
	return r.bashPPCustomOpen || r.dryRun && r.dryRunOpenHandler != nil
}

func (r *Runner) open(ctx context.Context, path string, flags int, mode os.FileMode, print bool) (io.ReadWriteCloser, error) {
	if bg := r.asyncProc; bg != nil && bg.launchYield != nil && r.openMayBlock(ctx, path) {
		r.yieldAsyncLaunch()
	}
	// Apply this Runner's virtual umask when creating a file. The
	// process-wide syscall umask is never touched (see Runner.umask),
	// so we have to mask the mode here before passing it down.
	if flags&os.O_CREATE != 0 {
		mode &^= os.FileMode(r.umask)
	}
	if path == "/dev/stdin" && flags&3 == os.O_RDONLY && r.stdin != nil {
		return borrowedFile{r.stdin}, nil
	}
	// If we are opening a FIFO temporary file created by the interpreter itself,
	// don't pass this along to the open handler as it will not work at all
	// unless [os.OpenFile] is used directly with it.
	// Matching by directory and basename prefix isn't perfect, but works.
	//
	// If we want FIFOs to use a handler in the future, they probably
	// need their own separate handler API matching Unix-like semantics.
	dir, name := filepath.Split(path)
	dir = strings.TrimSuffix(dir, "/")
	if dir == r.tempDir && strings.HasPrefix(name, fifoNamePrefix) {
		return os.OpenFile(path, flags, mode)
	}
	// The Windows counterpart is a //./pipe/ named pipe (procsubst_windows.go);
	// os.OpenFile hands that local-device path to CreateFile as is, and this
	// open — the consumer's one connection — must not be diverted either.
	if isProcSubstPipePath(path) {
		return os.OpenFile(path, flags, mode)
	}

	handler := r.openHandler
	if r.dryRun && r.dryRunOpenHandler != nil {
		handler = r.dryRunOpenHandler
	}
	f, err := handler(r.handlerCtx(ctx, handlerKindOpen, todoPos), path, flags, mode)
	// TODO: support wrapped PathError returned from openHandler.
	switch err := err.(type) {
	case nil:
		return f, nil
	case *os.PathError:
		_, _, networkPath := devNetworkRedirect(path)
		if err.Op == "connect" && networkPath && ctx.Value(devNetworkRedirectCtxKey{}) != nil {
			// GNU Bash reports the failed connect separately before naming the
			// /dev/tcp or /dev/udp operand. Keep the ordinary redirection error
			// path below for all filesystem failures and custom OpenHandlers.
			if print {
				reason := devNetworkErrorReason(err)
				r.errf("%sconnect: %s\n", r.bashErrPrefix(r.curStmtPos), reason)
				r.errf("%s%s: %s\n", r.bashErrPrefix(r.curStmtPos), path, reason)
			}
			return nil, err
		}
		// Preserve the embedder fallback only while the runner is actually
		// attached to a terminal. With no terminal, Bash reports the failed
		// /dev/tty redirection instead of fabricating a readable descriptor.
		if path == "/dev/tty" && flags&os.O_WRONLY == 0 && r.onTerminal() {
			read, write, pipeErr := os.Pipe()
			if pipeErr == nil {
				return &ttyFallbackFile{read: read, write: write}, nil
			}
		}
		if print {
			if r.bashCompatErrors {
				r.errf("%s%s: %s\n", r.bashErrPrefix(r.curStmtPos), path, bashOSError(err))
			} else {
				r.errf("%v\n", err)
			}
		}
	default: // handler's custom fatal error
		r.exit.fatal(err)
	}
	return nil, err
}

// statVirtualFd resolves a /dev/fd/N pseudo-path (N>=3) to the *os.File bashy
// holds for that descriptor and returns its fstat result. bashy tracks
// redirected descriptors as Go file objects (r.fdTable for reads,
// r.fdWriteTable for writes) rather than dup'ing them to real numbered OS fds,
// so a plain os.Stat("/dev/fd/6") has no real fd to resolve on Linux (where
// /dev/fd is /proc/self/fd). File-type tests like `test -p /dev/fd/6` must
// therefore inspect bashy's own descriptor. fds 0/1/2 are the process's real
// descriptors and are left to the filesystem. ok=false means "fall through";
// ok=true with os.ErrNotExist means the /dev/fd descriptor is closed.
func (r *Runner) statVirtualFd(name string) (fs.FileInfo, bool, error) {
	const prefix = "/dev/fd/"
	if !strings.HasPrefix(name, prefix) {
		return nil, false, nil
	}
	fd, err := strconv.Atoi(name[len(prefix):])
	if err != nil || fd < 3 {
		return nil, false, nil
	}
	// The shell may close an inherited descriptor without closing the
	// embedding Go process's descriptor. Never fall through to /dev/fd in that
	// case: it would expose the host descriptor after `exec N>&-` and make file
	// tests report a descriptor that is closed in the shell's own fd view.
	if r.fdClosedTable[fd] {
		return nil, true, os.ErrNotExist
	}
	// reads live in fdTable (*os.File); writes live in fdWriteTable, possibly
	// wrapped (e.g. fifoWriteFile, which embeds *os.File and so promotes Stat).
	var statter interface {
		Stat() (fs.FileInfo, error)
	}
	if rf := r.fdTable[fd]; rf != nil {
		statter = rf
	} else if w, ok := r.fdWriteTable[fd]; ok {
		statter, _ = w.(interface {
			Stat() (fs.FileInfo, error)
		})
	}
	if statter == nil {
		// Not a bashy-managed descriptor (closed, or an unregistered real fd):
		// fall through to the filesystem, which correctly reports a closed
		// /dev/fd/N as nonexistent.
		return nil, false, nil
	}
	info, statErr := statter.Stat()
	return info, true, statErr
}

func (r *Runner) stat(ctx context.Context, name string) (fs.FileInfo, error) {
	if info, ok, err := r.statVirtualFd(name); ok {
		return info, err
	}
	// A Windows process-substitution pipe is stat'ed without opening it:
	// a CreateFile on the name would take a connection the consumer wants.
	// Once the shell has released the pipe the answer is "no such file",
	// as it is for the unlinked FIFO on Unix.
	if info, err, ok := procSubstPipeStatErr("stat", name); ok {
		return info, err
	}
	path := absPath(r.Dir, name)
	return r.statHandler(ctx, path, true)
}

func (r *Runner) lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	if info, ok, err := r.statVirtualFd(name); ok {
		return info, err
	}
	if info, err, ok := procSubstPipeStatErr("lstat", name); ok {
		return info, err
	}
	path := absPath(r.Dir, name)
	return r.statHandler(ctx, path, false)
}
