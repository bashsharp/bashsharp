// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pathconv"
)

// TODO: given the categories below, perhaps this should be more like:
//
//   func IsBuiltin(lang syntax.LangVariant, name string) bool
//
// or perhaps some API that also lets the user iterate through the builtins?
//
// Also, should we move this to the syntax package too?
// It's not a syntactical property strictly speaking,
// but it's also odd to require importing the interp package for it.

// IsBuiltin returns true if the given word is a POSIX Shell
// or Bash builtin.
func IsBuiltin(name string) bool {
	// While reader-level history expansion is armed (`set -o history`
	// plus `set -H`), `!`-prefixed words are history event designators
	// resolved by the history engine at dispatch, not external commands.
	if histDesignator(name) {
		return true
	}
	switch name {
	case
		// POSIX Shell builtins, from section 1.d obtained in September 2025 from:
		// https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html#tag_18_09_01_01
		"alias",
		"bg",
		"cd",
		"command",
		"false",
		"fc",
		"fg",
		"getopts",
		"hash",
		"jobs",
		"kill",
		"newgrp",
		"pwd",
		"read",
		"true",
		"umask",
		"unalias",
		"wait",

		// POSIX Shell special built-ins, obtained in September 2025 from:
		// https://pubs.opengroup.org/onlinepubs/9699919799/utilities/V3_chap02.html#tag_18_14
		"break",
		":",
		"continue",
		".",
		"eval",
		"exec",
		"exit",
		"export",   // NOTE: our parser treats this as a keyword
		"readonly", // NOTE: our parser treats this as a keyword
		"return",
		"set",
		"shift",
		"times",
		"trap",
		"unset",

		// Bash built-ins which are not present in POSIX, obtained in September 2025 from:
		// https://man.archlinux.org/man/bash.1.en#SHELL_BUILTIN_COMMANDS
		"source",
		"bind",
		"builtin",
		"caller",
		"compgen",
		"complete",
		"compopt",
		"declare", // NOTE: our parser treats this as a keyword
		"typeset", // NOTE: our parser treats this as a keyword
		"dirs",
		"disown",
		"echo", // TODO: surely this is POSIX? but why is it not in the main POSIX spec page?
		"enable",
		"history",
		"help",
		"let", // NOTE: our parser treats this as a keyword
		"local",
		"logout",
		"mapfile",
		"readarray",
		"popd",
		"printf", // TODO: surely this is POSIX? but why is it not in the main POSIX spec page?
		"pushd",
		"shopt",
		"suspend",
		"test",
		"[", // NOTE: an alias for "test", not explicitly listed
		"type",
		"ulimit",

		// Non-bash, non-POSIX — implemented as builtins so they're
		// reliable inside the in-process runner even where the
		// corresponding /usr/bin/* binaries are missing (macOS has
		// no setsid binary) or buggy in the runner's stdio context
		// (BSD nohup hits "Inappropriate ioctl for device" when the
		// parent is a session leader).
		"nohup",
		"setsid",

		// Bash loadable builtin used by its glob-bracket test. We
		// implement it internally since bashy has no dynamic builtin
		// loader.
		"strmatch",

		// Agentic extensions — bashy-specific introspection. Surfaces
		// runner state as JSON so harnesses can observe and assert on
		// what the shell is doing. See docs/agentic-extensions.md.
		"runner-state",

		// Agentic extension — "alter working directory": run one command
		// with the cwd temporarily set to DIR, without changing the shell's
		// own cwd (unlike cd/pushd). Bashy-only; suppressed in the pure bash
		// drop-in. See docs/agentic-tooling-modernization.md.
		"awd":
		return true
	}
	return false
}

func isKeyword(word string) bool {
	switch word {
	case "elif", "{", "}":
		return true
	}
	return syntax.IsKeyword(word)
}

// TODO: atoi is duplicated in the expand package.

// atoi is like [strconv.ParseInt](s, 10, 64), but it ignores errors and trims whitespace.
func atoi(s string) int64 {
	s = strings.TrimSpace(s)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

type errBuiltinExitStatus exitStatus

func (e errBuiltinExitStatus) Error() string {
	return fmt.Sprintf("builtin exit status %d", e.code)
}

// Builtin allows [ExecHandlerFunc] implementations to execute any builtin,
// which can be useful for an exec handler to wrap or combine builtin calls.
//
// Note that a non-nil error may be returned in cases where the builtin
// alters the control flow of the runner, even if the builtin did not fail.
// For example, this is the case with `exit 0` or `return`.
func (hc HandlerContext) Builtin(ctx context.Context, args []string) error {
	if hc.kind != handlerKindExec {
		return fmt.Errorf("HandlerContext.Builtin can only be called via an ExecHandlerFunc")
	}
	exit := hc.runner.builtin(ctx, hc.Pos, args[0], args[1:])
	if exit != (exitStatus{}) {
		return errBuiltinExitStatus(exit)
	}
	return nil
}

func validBuiltinAssignName(name string) bool {
	if syntax.ValidName(name) {
		return true
	}
	base, idx, ok := splitArrayRef(name)
	return ok && syntax.ValidName(base) && !strings.Contains(idx, "]")
}

// subscriptQuotesBalanced reports whether every quote opened in an array
// subscript is also closed. bash's skipsubscript honours quoting, so a
// subscript left inside an unclosed quote (e.g. `80's`) swallows the
// closing `]` and never forms a valid array reference.
func subscriptQuotesBalanced(s string) bool {
	var quote byte // 0, '\'', '"', or '`'
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch quote {
		case '\'':
			if c == '\'' {
				quote = 0
			}
		case '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case '`':
			if c == '`' {
				quote = 0
			}
		default:
			switch c {
			case '\\':
				i++
			case '\'', '"', '`':
				quote = c
			}
		}
	}
	return quote == 0
}

// builtinAssignNameValid validates a read/printf assignment target. Beyond
// the basic identifier/array-reference shape, it mirrors bash's re-parse of
// an already-expanded subscript: without assoc_expand_once the subscript is
// scanned again via skipsubscript, so one left inside an unclosed quote
// (`a[80's]`) is rejected as not a valid identifier.
func (r *Runner) builtinAssignNameValid(name string, quoted bool) bool {
	if opt, _ := r.bashOptByName("assoc_expand_once"); opt != nil && *opt {
		// With assoc_expand_once the subscript was expanded once during
		// word expansion and is taken literally — bash does not re-parse
		// it, so an unquoted target's bracket structure is known from the
		// source word: `A[$rkey]` with rkey=`]` keeps the literal key `]`.
		if syntax.ValidName(name) {
			return true
		}
		base, idx, ok := splitArrayRef(name)
		if !ok || idx == "" || !syntax.ValidName(base) {
			return false
		}
		// A fully quoted target (`"A[$k]"`) is not recognised as an array
		// reference at parse time; bash re-scans the once-expanded string
		// with skipsubscript, so a subscript whose `]` closes before the
		// end (`A[]]`) leaves trailing junk and is rejected.
		if quoted {
			if closesEarly, _ := assocSubscriptBracketIssue(idx); closesEarly {
				return false
			}
		}
		return true
	}
	if !validBuiltinAssignName(name) {
		return false
	}
	if _, idx, ok := splitArrayRef(name); ok && !subscriptQuotesBalanced(idx) {
		return false
	}
	return true
}

// builtinTargetTokenQuoted reports whether the source token on the builtin's
// command line whose first byte is `target[0]` and that contains target's base
// name began with a quote. It is a best-effort source scan (mirroring
// unsetOperandSources) used only to decide whether a once-expanded array
// subscript should be re-scanned strictly. When the source can't be recovered
// it returns false (lenient).
func (r *Runner) builtinTargetQuoted(pos syntax.Pos, base string) bool {
	offs, ok := r.sourceOffset(pos)
	if !ok {
		return false
	}
	end := offs
	for end < len(r.bashSource) && r.bashSource[end] != '\n' {
		end++
	}
	line := string(r.bashSource[offs:end])
	i := 0
	for i < len(line) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		quoted := line[i] == '\'' || line[i] == '"'
		for i < len(line) {
			c := line[i]
			if c == ' ' || c == '\t' {
				break
			}
			switch c {
			case '\\':
				i++
				if i < len(line) {
					i++
				}
			case '\'':
				i++
				for i < len(line) && line[i] != '\'' {
					i++
				}
				if i < len(line) {
					i++
				}
			case '"':
				i++
				for i < len(line) {
					if line[i] == '\\' {
						i++
						if i < len(line) {
							i++
						}
						continue
					}
					if line[i] == '"' {
						i++
						break
					}
					i++
				}
			default:
				i++
			}
		}
		tok := line[start:i]
		// A quoted target reference like "A[$k]" begins with the quote and
		// carries the base name right after it: match `"`+base+`[`.
		if quoted && len(tok) >= len(base)+2 &&
			(tok[0] == '"' || tok[0] == '\'') &&
			strings.HasPrefix(tok[1:], base) &&
			len(tok) > len(base)+1 && tok[len(base)+1] == '[' {
			return true
		}
	}
	return false
}

// unsetIdentLikeStart reports whether c could begin a (possibly malformed)
// variable identifier — a letter, digit, or underscore. Bash's `unset` only
// emits "not a valid identifier" for such names; names led by other
// punctuation fall through to the function namespace instead.
func unsetIdentLikeStart(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

func (r *Runner) unsetBuiltinArrayElem(name, idx string) bool {
	vr := r.lookupVar(name)
	if vr.Kind == expand.NameRef {
		if base, targetIdx, ok := splitArrayRef(vr.Str); ok && syntax.ValidName(base) {
			return r.unsetArrayElem(base, targetIdx)
		}
		if vr.Str != "" && syntax.ValidName(vr.Str) {
			name = vr.Str
			vr = r.lookupVar(name)
		}
	}
	if vr.Kind == expand.String {
		// A declared scalar — set OR declared-but-unset (`declare undef`) — is
		// not an array, so subscript-unsetting it errors in bash. (A truly
		// undeclared name has Kind != String and falls through to a no-op.)
		// Exception: subscript `[0]` aliases the scalar itself, so bash treats
		// `unset name[0]` as unsetting the whole variable (a no-op when unset)
		// and returns success; any non-zero subscript is the error case.
		if n, err := r.arithFromString(idx); err == nil && n == 0 {
			if vr.IsSet() {
				r.delVar(name)
			}
			return true
		}
		r.errf("%sunset: %s: not an array variable\n", r.bashErrPrefix(r.curStmtPos), name)
		return false
	}
	if vr.Kind == expand.Associative {
		if strings.ContainsAny(idx, "'\"\\") {
			if w, ok := r.arrayTargetIndex(idx).(*syntax.Word); ok {
				idx = r.assocAssignKey(w)
			}
		} else if len(idx) > 1 && idx[0] == '$' && syntax.ValidName(idx[1:]) {
			if keyVar := r.lookupVar(idx[1:]); keyVar.IsSet() {
				idx = keyVar.String()
			}
		}
		if _, ok := vr.Map[idx]; ok {
			delete(vr.Map, idx)
			vr.Set = true
			r.setVar(name, vr)
		}
		return true
	}
	return r.unsetArrayElem(name, idx)
}

func (r *Runner) unsetStringArrayElem(name, idx string) bool {
	vr := r.lookupVar(name)
	if vr.Kind == expand.String {
		// A declared scalar — set OR declared-but-unset (`declare undef`) — is
		// not an array, so subscript-unsetting it errors in bash. (A truly
		// undeclared name has Kind != String and falls through to a no-op.)
		// Exception: subscript `[0]` aliases the scalar itself, so bash treats
		// `unset name[0]` as unsetting the whole variable (a no-op when unset)
		// and returns success; any non-zero subscript is the error case. The
		// subscript may carry quotes (`name["key"]`), so strip them first.
		if n, err := r.arithFromString(r.unsetStringSubscript(idx)); err == nil && n == 0 {
			if vr.IsSet() {
				r.delVar(name)
			}
			return true
		}
		r.errf("%sunset: %s: not an array variable\n", r.bashErrPrefix(r.curStmtPos), name)
		return false
	}
	if vr.Kind != expand.Associative {
		return r.unsetArrayElem(name, idx)
	}
	key := r.unsetStringSubscript(idx)
	if _, ok := vr.Map[key]; ok {
		delete(vr.Map, key)
		vr.Set = true
		r.setVar(name, vr)
	}
	return true
}

func (r *Runner) unsetStringSubscript(idx string) string {
	if opt, _ := r.bashOptByName("assoc_expand_once"); opt != nil && *opt {
		return quoteRemoveUnsetSubscript(idx)
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader("x "+idx+"\n"), "")
	if err != nil || len(file.Stmts) != 1 {
		return idx
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 {
		return idx
	}
	return r.literal(call.Args[1])
}

func quoteRemoveUnsetSubscript(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 < len(s) {
				i++
			}
			b.WriteByte(s[i])
		case '\'':
			i++
			for i < len(s) && s[i] != '\'' {
				b.WriteByte(s[i])
				i++
			}
		case '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

type unsetOperandSource struct {
	quoted bool
}

func (r *Runner) unsetOperandSources(pos syntax.Pos) []unsetOperandSource {
	offs, ok := r.sourceOffset(pos)
	if !ok {
		return nil
	}
	end := offs
	for end < len(r.bashSource) && r.bashSource[end] != '\n' {
		end++
	}
	line := string(r.bashSource[offs:end])
	return scanUnsetOperandSources(line)
}

func scanUnsetOperandSources(line string) []unsetOperandSource {
	var operands []unsetOperandSource
	i := 0
	for i < len(line) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		start := i
		quoted := line[i] == '\'' || line[i] == '"'
		for i < len(line) {
			switch line[i] {
			case '\\':
				i += 2
			case '\'':
				i++
				for i < len(line) && line[i] != '\'' {
					i++
				}
				if i < len(line) {
					i++
				}
			case '"':
				i++
				for i < len(line) {
					if line[i] == '\\' {
						i += 2
						continue
					}
					if line[i] == '"' {
						i++
						break
					}
					i++
				}
			case ' ', '\t':
				goto wordDone
			default:
				i++
			}
		}
	wordDone:
		if line[start:i] == "unset" {
			continue
		}
		if strings.HasPrefix(line[start:i], "-") {
			continue
		}
		operands = append(operands, unsetOperandSource{quoted: quoted})
	}
	return operands
}

// writeErrChecked is the set of builtins that, like bash's sh_chkwrite
// callers, report a standard-output write failure to a closed descriptor
// (`echo >&-`) and exit non-zero. Builtins outside this set (e.g. `help`)
// fail their write silently, matching bash. cd only writes when printing
// the new directory (`cd -`, CDPATH): bash still changes the directory and
// reports the write error with a failure status.
var writeErrChecked = map[string]bool{
	"cd":      true,
	"command": true, // bash's command.def reports -v/-V output write failures via sh_chkwrite
	"echo":    true,
	"printf":  true,
	"pwd":     true,
}

// pipeWriteErrChecked is the POSIX special/listing builtin subset whose
// standard-output write errors are observable when SIGPIPE is ignored. In the
// common `builtin | consumer` case bash dies from SIGPIPE silently, so keep
// the broader EPIPE check away from echo/printf and other output builtins.
var pipeWriteErrChecked = map[string]bool{
	"alias":    true,
	"export":   true,
	"hash":     true,
	"readonly": true,
	"set":      true,
	"times":    true,
	"umask":    true,
}

func (r *Runner) builtin(ctx context.Context, pos syntax.Pos, name string, args []string) (exit exitStatus) {
	// These builtins can wait on context-aware I/O or child state. Announce the
	// blocking edge before entering it so the `go` launch handshake cannot
	// deadlock an owner whose next statement supplies the wakeup or failure.
	defer r.bashPPLockLogicalOutput(name)()
	// Bash fails a builtin whose standard-output write fails on a closed or
	// broken descriptor (`echo >&-`, `printf x >&-`): it reports a write
	// error and exits non-zero rather than silently succeeding. Track writes
	// performed during this builtin and surface the failure if it otherwise
	// would have succeeded. The flag is saved/restored so a nested builtin
	// (`builtin echo`, `command echo`) reports independently.
	prevOutErr := r.outErr
	r.outErr = nil
	defer func() {
		// A builtin on the in-process left side of a pipeline is Bash's
		// process-isolated writer in our goroutine-based model. The platform
		// writer suppresses only the kernel SIGPIPE generated by its own write;
		// turn EPIPE back into the child-side 128+SIGPIPE status and stop this
		// simulated subshell. The enclosing shell must neither die nor run its
		// PIPE trap.
		pipeBroken := r.pipelineWriteBroken.Swap(false) || isBrokenPipeWriteErr(r.outErr)
		if r.pipelineOutput && pipeBroken {
			ignored := r.pipelineSIGPIPEIgnored()
			if !ignored {
				exit.code = 128 + 13
				exit.exiting = true
				r.outErr = prevOutErr
				return
			}
			// An ignored PIPE cannot terminate the pipeline child. Bash's
			// sh_chkwrite builtins observe EPIPE as an ordinary write error;
			// surrounding shell code may continue and replace that status.
			if (writeErrChecked[name] || pipeWriteErrChecked[name]) &&
				exit.code == 0 && !exit.exiting && !exit.returning {
				r.errf("%s%s: write error: %s\n", r.bashErrPrefix(pos), name, writeErrDiagnostic(r.outErr))
				exit.code = 1
			}
			r.outErr = prevOutErr
			return
		}
		// Bash checks for output write errors only in the builtins that call
		// sh_chkwrite (echo, printf): a write to a closed descriptor (EBADF:
		// `echo >&-`) is reported and exits non-zero. Other output builtins
		// (e.g. `help >&-`) fail silently, so the check is scoped to that set.
		// A broken pipe (EPIPE: `echo foo | false`) is usually bash's SIGPIPE
		// case — the writer dies silently and the status comes from the
		// pipeline — so the generic check remains limited to closed fds. POSIX
		// also requires a few special/listing builtins to report write errors
		// when SIGPIPE is ignored; cover that narrower class separately.
		if shouldReportWriteErr(name, r.outErr) &&
			exit.code == 0 && !exit.exiting && !exit.returning {
			r.errf("%s%s: write error: %s\n", r.bashErrPrefix(pos), name, writeErrDiagnostic(r.outErr))
			exit.code = 1
		}
		r.outErr = prevOutErr
	}()
	// failf emits a user-fault error and sets the exit code. When
	// [WithBashCompatErrors] is on, the message is prefixed with
	// "<filename>: line <N>: " so bash 5.3's test suite output matches.
	failf := func(code uint8, format string, a ...any) exitStatus {
		msg := fmt.Sprintf(format, a...)
		if prefix := r.bashErrPrefix(pos); prefix != "" {
			msg = prefix + msg
			r.errf("%s", msg)
		} else {
			r.errf("%s", msg)
		}
		r.reportError("builtin", pos, name, msg, code)
		exit.code = code
		return exit
	}
	// invalidOpt is the canonical "<builtin>: <flag>: invalid option"
	// formatter. In bashCompat mode it follows the bash 5.3 wording
	// (arg-first, no quotes) and also emits a usage hint to stderr
	// without the line prefix, matching bash. The legacy form
	// "<builtin>: invalid option \"<flag>\"" is preserved otherwise.
	invalidOpt := func(builtin, flag string) exitStatus {
		if r.bashCompatErrors {
			r.errf(r.bashErrPrefix(pos)+"%s: %s: invalid option\n", builtin, flag)
			if usage := bashUsage[builtin]; usage != "" {
				r.errf("%s: usage: %s\n", builtin, usage)
			}
			exit.code = 2
			return exit
		}
		return failf(2, "%s: invalid option %q\n", builtin, flag)
	}
	invalidIdentifier := func(builtin, ident string) exitStatus {
		if r.bashCompatErrors {
			return failf(1, "%s: `%s': not a valid identifier\n", builtin, ident)
		}
		return failf(1, "%s: invalid name %q\n", builtin, ident)
	}
	// Emulate bash's reader-level history recording: every builtin
	// dispatch advances the history cursor up to its source line. No-op
	// unless a script has turned on `set -o history`.
	r.histSync(ctx, pos)
	if histDesignator(name) {
		// The histSync call above consumed this line's `!` event
		// designator from the reader timeline, echoing the expansion to
		// stderr and executing it. Surface that execution's exit code.
		exit.code = r.exit.code
		return exit
	}
	switch name {
	case ":", "true":
	case "false":
		exit.code = 1
	case "exit":
		if len(args) >= 1 && args[0] == "--" {
			args = args[1:]
		}
		switch len(args) {
		case 0:
			if r.handlingTrap {
				exit = r.trapEntryExit
			} else {
				exit = r.lastExit
			}
		case 1:
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return failf(2, "exit: %s: numeric argument required\n", args[0])
			}
			exit.code = uint8(n)
		default:
			return failf(2, "exit: too many arguments\n")
		}
		exit.exiting = true
	case "set":
		if !r.opts[optPosix] && len(args) == 1 && args[0] == "--json" {
			return r.jsonOut(map[string]any{"variables": r.variablesJSON(true)})
		}
		if len(args) == 2 && args[0] == "-o" && strings.HasPrefix(args[1], "-") {
			for _, name := range r.bashSetOptNames() {
				status := "off"
				if opt := r.posixOptByName(name); opt != nil && *opt {
					status = "on"
				} else if on, ok := r.noOpSetState[name]; ok && on {
					status = "on"
				}
				r.outf("%-15s\t%s\n", name, status)
			}
			break
		}
		if len(args) == 0 {
			// `set` with no args prints all shell variables in
			// `name=value` form, alphabetically sorted, values
			// quoted bash-style. Bash distinguishes scalars,
			// indexed arrays and associative arrays via the same
			// rules as `declare -p`'s output minus the `declare -X`
			// prefix.
			r.printSetVars()
			break
		}
		prevHistOn := r.noOpSetState["history"]
		prevHistExp := r.noOpSetState["histexpand"]
		if err := Params(args...)(r); err != nil {
			if err.Error() == "+r: invalid option" {
				r.errf("%sset: +r: invalid option\n", r.bashErrPrefix(pos))
				r.errf("set: usage: %s\n", bashUsage["set"])
				exit.code = 2
				return exit
			}
			if opt, ok := strings.CutPrefix(err.Error(), "invalid option: "); ok {
				opt = strings.Trim(opt, `"`)
				if strings.HasPrefix(opt, "-") || strings.HasPrefix(opt, "+") {
					r.errf("%sset: %s: invalid option\n", r.bashErrPrefix(pos), opt)
					r.errf("set: usage: %s\n", bashUsage["set"])
				} else {
					r.errf("%sset: %s: invalid option name\n", r.bashErrPrefix(pos), opt)
				}
				exit.code = 2
				return exit
			}
			return failf(2, "set: %v\n", err)
		}
		r.updateExpandOpts()
		if now := r.noOpSetState["history"]; now != prevHistOn {
			r.histSetEnabled(now, pos)
		}
		if now := r.noOpSetState["histexpand"]; now != prevHistExp {
			r.histSetExpand(now)
		}
		// `set -H` / `set +H` aren't modeled in noOpSetState; scan the
		// flag arguments for the histexpand toggle.
		for _, a := range args {
			if a == "--" || a == "-" || len(a) < 2 || (a[0] != '-' && a[0] != '+') {
				break
			}
			if a[1] != 'o' && strings.ContainsRune(a[1:], 'H') {
				r.histSetExpand(a[0] == '-')
			}
		}
	case "shift":
		if len(args) == 1 && args[0] == "--help" {
			r.outf("%s", bashHelpShiftLong())
			break
		}
		// Accept `--` as end-of-options.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		n := 1
		operand := ""
		switch len(args) {
		case 0:
		case 1:
			operand = args[0]
			n2, err := strconv.Atoi(args[0])
			if err != nil {
				return failf(2, "shift: %s: numeric argument required\n", args[0])
			}
			if n2 < 0 {
				return failf(1, "shift: %s: shift count out of range\n", args[0])
			}
			n = n2
		default:
			return failf(2, "shift: too many arguments\n")
		}
		if n > len(r.Params) {
			// Out of range: silent error by default; with
			// `shopt -s shift_verbose` or in POSIX mode, emit
			// a diagnostic.
			if opt, _ := r.bashOptByName("shift_verbose"); (opt != nil && *opt) || r.opts[optPosix] {
				if operand == "" {
					return failf(1, "shift: shift count out of range\n")
				}
				return failf(1, "shift: %s: shift count out of range\n", operand)
			}
			exit.code = 1
			return exit
		}
		if n >= len(r.Params) {
			r.Params = nil
		} else {
			r.Params = r.Params[n:]
		}
	case "unset":
		vars := true
		funcs := true
		explicitVars := false
		// `-n NAME` unsets the nameref itself (not the variable
		// it points to). Without -n, unset of a nameref follows
		// the reference and unsets the target.
		nameref := false
	unsetOpts:
		for i, arg := range args {
			switch arg {
			case "-v":
				explicitVars = true
				funcs = false
			case "-f":
				vars = false
			case "-n":
				nameref = true
				funcs = false
			default:
				if len(arg) > 1 && arg[0] == '-' {
					r.errf("%sunset: %s: invalid option\n", r.bashErrPrefix(pos), arg)
					r.errf("unset: usage: unset [-f] [-v] [-n] [name ...]\n")
					exit.code = 2
					return exit
				}
				args = args[i:]
				break unsetOpts
			}
		}

		if !vars && !funcs {
			return failf(1, "unset: cannot simultaneously unset a function and a variable\n")
		}

		operandSources := r.unsetOperandSources(pos)
		for i := 0; i < len(args); i++ {
			arg := args[i]
			operand := unsetOperandSource{}
			if i < len(operandSources) {
				operand = operandSources[i]
			}
			tryFuncOnly := false
			// Bare unset checks both variables and functions. An invalid
			// variable name can still be a function name, so a miss is
			// silent; explicit -v reports the identifier error.
			// Function names are unrestricted, so `unset -f 1bad` is allowed.
			//
			// Array-element form `name[index]` is valid: unset the
			// specified element instead of the whole variable.
			if vars {
				if name, idx, ok := splitArrayRef(arg); ok {
					if syntax.ValidName(name) {
						unsetElem := r.unsetBuiltinArrayElem
						if operand.quoted {
							unsetElem = r.unsetStringArrayElem
						}
						if !unsetElem(name, idx) {
							exit.code = 1
						}
						continue
					}
				}
				if !syntax.ValidName(arg) {
					if !explicitVars && funcs && !nameref {
						// Try the function namespace below.
						tryFuncOnly = true
					} else if funcs && !nameref && !explicitVars && len(arg) > 0 && !unsetIdentLikeStart(arg[0]) {
						// Bash only reports "not a valid identifier" for names
						// that look like a botched variable name — those that
						// begin with a letter, digit, or underscore (`1bad`,
						// `invalid-name`). A name that can't even begin an
						// identifier (pure punctuation such as `%`) is treated
						// as a non-match: bare unset falls through to the
						// function namespace, so `unset %` silently exits 0.
						tryFuncOnly = true
					} else if !explicitVars && strings.Contains(arg, "$(") {
						for i+1 < len(args) && !syntax.ValidName(args[i+1]) {
							i++
						}
						continue
					} else if r.bashCompatErrors && strings.Contains(arg, "/") {
						continue
					} else {
						msg := fmt.Sprintf("unset: `%s': not a valid identifier\n", arg)
						// Bash reports in-function errors at the function body brace line.
						line := int(pos.Line())
						if n := len(r.callStack); n > 0 && r.callStack[n-1].bodyLine > 0 {
							line = int(r.callStack[n-1].bodyLine)
						}
						if prefix := r.bashErrPrefixLine(line); prefix != "" {
							msg = prefix + msg
						}
						r.errf("%s", msg)
						r.reportError("builtin", syntax.NewPos(pos.Offset(), uint(line), pos.Col()), name, msg, 2)
						exit.code = 2
						continue
					}
				}
			}
			if nameref {
				// Skip the auto-resolve so we delete the nameref
				// variable itself rather than its target.
				vr := r.lookupVar(arg)
				// Bash only acts when NAME actually has the nameref
				// attribute; `unset -n` on a plain variable is a no-op
				// (it does NOT fall back to unsetting the variable).
				if vr.Kind != expand.NameRef {
					continue
				}
				if vr.ReadOnly {
					r.errf("%sunset: %s: cannot unset: readonly variable\n", r.bashErrPrefix(pos), arg)
					exit.code = 1
					continue
				}
				r.delVar(arg)
				continue
			}
			if vars && !tryFuncOnly {
				switch arg {
				case "BASH_LINENO", "BASH_SOURCE":
					r.errf("%sunset: %s: cannot unset\n", r.bashErrPrefix(pos), arg)
					exit.code = 1
					continue
				}
				vr := r.lookupVar(arg)
				if vr.Kind == expand.NameRef {
					// Bash: `unset NAME` on a nameref follows
					// the reference and unsets the *target*.
					// The nameref itself keeps the attribute
					// (now pointing at an unset variable).
					if vr.Str != "" {
						if base, idx, ok := splitArrayRef(vr.Str); ok && syntax.ValidName(base) {
							tgt := r.lookupVar(base)
							if tgt.ReadOnly {
								r.errf("%sunset: %s: cannot unset: readonly variable\n",
									r.bashErrPrefix(pos), base)
								exit.code = 1
								continue
							}
							if !r.unsetArrayElem(base, idx) {
								exit.code = 1
							}
							continue
						}
						tgt := r.lookupVar(vr.Str)
						if tgt.ReadOnly {
							r.errf("%sunset: %s: cannot unset: readonly variable\n",
								r.bashErrPrefix(pos), vr.Str)
							exit.code = 1
							continue
						}
						r.delVar(vr.Str)
					}
					continue
				}
				if vr.ReadOnly {
					r.errf("%sunset: %s: cannot unset: readonly variable\n",
						r.bashErrPrefix(pos), arg)
					exit.code = 1
					continue
				}
				if vr.IsSet() || vr.Integer || vr.Exported || vr.Local ||
					vr.Upper || vr.Lower || vr.Capitalize || vr.Kind != expand.Unset {
					r.delVar(arg)
					continue
				}
			}
			if _, ok := r.Funcs[arg]; ok && funcs {
				if r.readonlyFuncs[arg] {
					r.errf("%sunset: %s: cannot unset: readonly function\n",
						r.bashErrPrefix(pos), arg)
					exit.code = 1
					continue
				}
				delete(r.Funcs, arg)
				delete(r.bashPPAgenticFuncs, arg)
				delete(r.bashPPDecoratedFuncs, arg)
			}
		}
	case "echo":
		xpgOpt, _ := r.bashOptByName("xpg_echo")
		newline, doExpand := true, xpgOpt != nil && *xpgOpt
		// Issue 7 does not define any echo options. Outside XSI, only an
		// initial operand exactly equal to -n enters an
		// implementation-defined region; choose Bash's BSD behavior there.
		// In the opt-in XSI mode -n remains an operand and escapes are
		// interpreted. Keep Bash's broader -n/-e/-E option parser confined
		// to the non-strict path.
		if r.strictPosix {
			if !doExpand && len(args) > 0 && args[0] == "-n" {
				newline = false
				args = args[1:]
			}
		} else {
		echoOpts:
			for len(args) > 0 {
				arg := args[0]
				if len(arg) < 2 || arg[0] != '-' {
					break echoOpts
				}
				// Validate all chars are echo flags (n, e, E).
				valid := true
				for _, c := range arg[1:] {
					if c != 'n' && c != 'e' && c != 'E' {
						valid = false
						break
					}
				}
				if !valid {
					break echoOpts
				}
				for _, c := range arg[1:] {
					switch c {
					case 'n':
						newline = false
					case 'e':
						doExpand = true
					case 'E':
						doExpand = false
					}
				}
				args = args[1:]
			}
		}
		// Keep a complete echo line in one write. Other processes can share
		// this descriptor (including the VSC SigWait helper); separate writes
		// for operands and the newline allow their output to split this line.
		var line strings.Builder
		for i, arg := range args {
			if i > 0 {
				line.WriteByte(' ')
			}
			if doExpand {
				// `echo -e` uses bash's `%b`-style escape table:
				// `\c` terminates output, `\'`/`\"`/`\?` stay
				// literal (no `\` strip), the rest follow the
				// standard ANSI-C escapes.
				out, err := expand.FormatBPercent(r.ecfg, arg)
				line.WriteString(out)
				if err == expand.ErrPrintfStop {
					if line.Len() > 0 {
						r.out(line.String())
					}
					return exit
				}
				continue
			}
			line.WriteString(arg)
		}
		if newline {
			line.WriteByte('\n')
		}
		if line.Len() > 0 {
			r.out(line.String())
		}
	case "printf":
		// printf -v VAR FORMAT [args...] writes output to VAR instead of
		// stdout. Multiple -v flags are illegal; -v requires a name.
		var assignTo string
		fp := flagParser{remaining: args}
		var formatArgs []string
	parsePrintfFlags:
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-v":
				assignTo = fp.value()
				if assignTo == "" {
					return failf(2, "printf: -v: option requires an argument\n")
				}
				targetBase := assignTo
				if b, _, ok := splitArrayRef(assignTo); ok {
					targetBase = b
				}
				if !r.builtinAssignNameValid(assignTo, r.builtinTargetQuoted(pos, targetBase)) {
					if r.bashCompatErrors {
						return failf(2, "printf: `%s': not a valid identifier\n", assignTo)
					}
					return failf(1, "printf: %q: not a valid identifier\n", assignTo)
				}
				if base, idx, ok := splitArrayRef(assignTo); ok && (idx == "@" || idx == "*") {
					if r.lookupVar(base).Kind == expand.Indexed {
						return failf(1, "%s: bad array subscript\n", assignTo)
					}
				}
			default:
				// A leading '+' cannot be a printf option; it is the
				// format operand (and any remaining args are arguments).
				if flag[0] == '+' {
					if fp.current != "" {
						flag += fp.current[1:]
						fp.current = ""
					}
					formatArgs = append([]string{flag}, fp.args()...)
					break parsePrintfFlags
				}
				return invalidOpt("printf", flag)
			}
		}
		if formatArgs == nil {
			args = fp.args()
		} else {
			args = formatArgs
		}
		if len(args) == 0 {
			if r.bashCompatErrors {
				// Bash emits the bare `printf: usage: ...` line for
				// no-args without the `<file>: line N:` prefix (the
				// prefix is reserved for error conditions; usage on
				// "no required arg" is informational).
				r.errf("printf: usage: %s\n", bashUsage["printf"])
				exit.code = 2
				return exit
			}
			return failf(2, "usage: printf [-v var] format [arguments]\n")
		}
		format, args := args[0], args[1:]
		var sb strings.Builder
		// Format may also invoke r.ecfg.OnFormatWarning for soft
		// failures (e.g. `printf %d xyz` → "invalid number"). The
		// callback stashes the bash-compat exit code in
		// r.lastExpandExit; reset it here so we only see warnings
		// from this invocation, then propagate at the end.
		r.lastExpandExit = exitStatus{}
		// Wire up bash printf %n: store the byte count emitted so far
		// into the named variable. Restore the previous callback after
		// this printf invocation so nested users of r.ecfg aren't
		// affected.
		prevOnPercentN := r.ecfg.OnPercentN
		r.ecfg.OnPercentN = func(name string, n int) error {
			r.setVarString(name, strconv.Itoa(n))
			return nil
		}
		defer func() { r.ecfg.OnPercentN = prevOnPercentN }()
		prevPrintfAssign := r.ecfg.PrintfAssign
		r.ecfg.PrintfAssign = assignTo != ""
		defer func() { r.ecfg.PrintfAssign = prevPrintfAssign }()
		for {
			s, n, err := expand.Format(r.ecfg, format, args)
			stop := errors.Is(err, expand.ErrPrintfStop)
			formatErr := err != nil && !stop
			diagnosticBeforeOutput := formatErr && bashPrintfFormatError(err)
			if diagnosticBeforeOutput {
				r.errf("%s%v\n", r.bashErrPrefix(pos), err)
			}
			if assignTo != "" {
				sb.WriteString(s)
			} else {
				r.out(s)
			}
			if formatErr {
				if assignTo != "" {
					r.setVarString(assignTo, sb.String())
				}
				if diagnosticBeforeOutput {
					exit.code = 1
					return exit
				}
				return failf(1, "%v\n", err)
			}
			if stop {
				break
			}
			args = args[n:]
			if n == 0 || len(args) == 0 {
				break
			}
		}
		if assignTo != "" {
			// Writing through an as-yet-untargeted nameref retargets it;
			// reject a value that isn't a valid identifier.
			if vr := r.lookupVar(assignTo); vr.Kind == expand.NameRef && vr.Str == "" &&
				!validNameRefTarget(sb.String()) {
				return failf(1, "printf: `%s': not a valid identifier\n", sb.String())
			}
			r.setVarString(assignTo, sb.String())
		}
		if r.lastExpandExit.code != 0 {
			exit.code = r.lastExpandExit.code
		}
	case "break", "continue":
		if !r.inLoop {
			// Bash 5.3 in POSIX mode silently treats break/continue
			// outside a loop as a no-op (returns 0), instead of
			// emitting the "only meaningful in a loop" diagnostic.
			if r.opts[optPosix] {
				break
			}
			return failf(0, "%s: only meaningful in a `for', `while', or `until' loop\n", name)
		}
		enclosing := &r.breakEnclosing
		if name == "continue" {
			enclosing = &r.contnEnclosing
		}
		// Accept and skip `--` as the end-of-options marker.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		switch len(args) {
		case 0:
			*enclosing = 1
		case 1:
			if n, err := strconv.Atoi(args[0]); err == nil {
				if n < 1 {
					// Bash still breaks out of the current loop
					// (the body after `break N<1` is unreachable),
					// but exits with a diagnostic.
					if name == "continue" {
						r.breakEnclosing = 1
					} else {
						*enclosing = 1
					}
					r.errf("%s%s: %s: loop count out of range\n",
						r.bashErrPrefix(r.curStmtPos), name, args[0])
					exit.code = 1
					return exit
				}
				*enclosing = n
				break
			}
			r.breakEnclosing = 1
			r.errf("%s%s: %s: numeric argument required\n",
				r.bashErrPrefix(r.curStmtPos), name, args[0])
			exit.code = 128
			exit.exiting = true
			return exit
		default:
			// Bash discards the rest of the input line and reads the next
			// one (errors10.sub: "after continue: 2", POSIX mode too). A
			// one-line `bash -c` string therefore looks like an exit.
			exit = failf(2, "%s: too many arguments\n", name)
			if !r.interactiveShell {
				exit.exiting = true
				exit.discarding = true
				r.discardRestOfLine = r.curStmtPos.Line()
			}
			return exit
		}
	case "pwd":
		evalSymlinks := false
		windowsForm := false
		for len(args) > 0 {
			arg := args[0]
			if arg == "--" {
				args = nil
				break
			}
			if len(arg) < 2 || arg[0] != '-' {
				break
			}
			if arg[1] == '-' {
				return failf(2, "pwd: %s: invalid option\n", arg)
			}
			valid := true
			for _, flag := range arg[1:] {
				switch flag {
				case 'L':
					evalSymlinks = false
				case 'P':
					evalSymlinks = true
				case 'W':
					// Git-Bash extension: print the native Windows path with
					// forward slashes. Remains an invalid option elsewhere,
					// matching bash 5.3 on those hosts.
					if runtime.GOOS == "windows" {
						windowsForm = true
					} else {
						valid = false
					}
				default:
					valid = false
				}
				if !valid {
					break
				}
			}
			if !valid {
				return failf(2, "pwd: %s: invalid option\n", arg)
			}
			args = args[1:]
		}
		pwd := r.envGet("PWD")
		osPwd := r.Dir
		if !evalSymlinks && validLogicalPWD(pwd) {
			// PWD is an ordinary shell variable and can be reassigned without
			// changing the shell's logical current-directory state. Keep that
			// state in r.Dir, as cd does, rather than trusting the variable.
			pwd = r.logicalDir()
		} else {
			var err error
			// Physical mode is defined by the invocation's actual working
			// directory, not by the possibly stale or user-modified PWD.
			osPwd, err = evalPhysicalPath(r.Dir)
			if err != nil {
				exit.fatal(err) // perhaps overly dramatic?
				return exit
			}
			pwd = shellPathFromOS(osPwd)
		}
		if windowsForm {
			r.outf("%s\n", pathconv.ToSlash(osPwd, osPwd))
		} else {
			r.outf("%s\n", pwd)
		}
		// POSIX requires pwd -P to update PWD to the physical path. This
		// deliberately leaves OLDPWD alone, as Bash's setpwd does.
		if evalSymlinks && r.opts[optPosix] {
			pwdVar := r.lookupVar("PWD")
			if pwdVar.ReadOnly {
				r.errf("%sPWD: readonly variable\n", r.bashErrPrefix(pos))
				exit.code = 1
				return exit
			}
			r.setVar("PWD", expand.Variable{
				Set: true, Exported: pwdVar.Exported, Kind: expand.String, Str: pwd,
			})
		}
	case "cd":
		if r.opts[optRestricted] {
			r.errf("%scd: restricted\n", r.bashErrPrefix(pos))
			exit.code = 1
			return exit
		}
		// bash's `cd` accepts `-L` (logical, default), `-P`
		// (physical — resolve symlinks so $PWD names the real dir),
		// `-e`, and `-@` (extended attributes; not meaningful here).
		// `-L`/`-P` are last-one-wins; under `-P` we resolve the
		// target's symlinks so $PWD is the physical path (bash:
		// `cd -P symlink` leaves PWD at the link's target).
		physical := false
		exitOnError := false
		for len(args) > 0 {
			a := args[0]
			if a == "--" { // end of options; remaining args are operands
				args = args[1:]
				break
			}
			if len(a) > 1 && a[0] == '-' && a != "-" {
				valid := true
				for _, opt := range a[1:] {
					switch opt {
					case 'P':
						physical = true
					case 'L':
						physical = false
					case 'e':
						exitOnError = true
					case '@':
					default:
						valid = false
					}
					if !valid {
						break
					}
				}
				if valid {
					args = args[1:]
					continue
				}
			}
			break
		}
		var path string
		printPath := false
		switch len(args) {
		case 0:
			path = r.envGet("HOME")
			if path == "" {
				// bash 5.3: an unset HOME is an error, while a HOME set to
				// the empty string makes `cd` a silent no-op (POSIX leaves
				// both cases implementation-defined).
				if r.lookupVar("HOME").IsSet() {
					return exit
				}
				r.errf("%scd: HOME not set\n", r.bashErrPrefix(r.curStmtPos))
				exit.code = 1
				return exit
			}
		case 1:
			path = args[0]

			// replicate the commonly implemented behavior of `cd -`
			// ref: https://www.man7.org/linux/man-pages/man1/cd.1p.html#OPERANDS
			if path == "-" {
				path = r.envGet("OLDPWD")
				if path == "" {
					r.errf("%scd: OLDPWD not set\n", r.bashErrPrefix(r.curStmtPos))
					exit.code = 1
					return exit
				}
				printPath = true
			} else if cdpath, printCdpath, ok := r.cdpath(ctx, path); ok {
				path = cdpath
				printPath = printCdpath
			}
		default:
			// bash returns 2 for a cd usage error (too many operands).
			return failf(2, "cd: too many arguments\n")
		}
		exit.code = r.changeDir(ctx, "cd", path, physical)
		if exitOnError && exit.code != 0 {
			exit.code = 2
		}
		if printPath && exit.code == 0 {
			r.outf("%s\n", path)
		}
	case "awd":
		// awd DIR [--] command [args...] — "alter working directory": run the
		// command with the cwd temporarily set to DIR, then fully restore the
		// shell's cwd (and PWD/OLDPWD/dirStack), so the change is ephemeral
		// (unlike cd/pushd, which persist). Lets an agent run a one-off command
		// elsewhere without the `cd x && cmd` state leak that strands the next
		// command in the wrong directory.
		if len(args) == 0 {
			return failf(2, "awd: usage: awd directory [--] command [args...]\n")
		}
		dir := args[0]
		args = args[1:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return failf(2, "awd: command required\n")
		}
		// Snapshot the live cwd state so it can be restored verbatim.
		savedDir := r.Dir
		r.ensureDirFile(savedDir)
		savedDirFile, _ := dupRunnerDir(r.dirFile)
		savedPWD := r.envGet("PWD")
		savedOLDPWD := r.envGet("OLDPWD")
		savedTop := ""
		if len(r.dirStack) > 0 {
			savedTop = r.dirStack[len(r.dirStack)-1]
		}
		// Reuse changeDir for cd-identical validation + error wording (it sets
		// r.Dir/PWD/OLDPWD/dirStack); the deferred restore is what makes it
		// ephemeral.
		if code := r.changeDir(ctx, "awd", dir); code != 0 {
			exit.code = code
			return exit
		}
		defer func() {
			if r.dirFile != nil {
				_ = r.dirFile.Close()
			}
			r.Dir = savedDir
			r.dirFile = savedDirFile
			if len(r.dirStack) > 0 {
				r.dirStack[len(r.dirStack)-1] = savedTop
			}
			r.setExportedVarString("PWD", savedPWD)
			r.setExportedVarString("OLDPWD", savedOLDPWD)
		}()
		// Run the command word through the normal dispatch path (handles
		// functions, builtins, and external commands uniformly) on the already
		// post-expansion argv. Propagate its full exit (code + exiting/returning
		// so `awd dir exit 3` / `return` behave).
		r.call(ctx, pos, args)
		exit = r.exit
	case "wait":
		fp := flagParser{remaining: args}
		waitNext := false
		var pidVar string
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-n":
				waitNext = true
			case "-p":
				pidVar = fp.value()
			case "-f":
				// Wait for the job to terminate (not merely stop). Our
				// jobs only ever terminate, so this is a no-op accepted
				// for compatibility.
			default:
				return invalidOpt("wait", flag)
			}
		}
		remaining := fp.args()
		// Sanity-check and reset the -p variable up front, matching bash
		// (builtins/wait.def): the name must be a valid identifier (or
		// array reference) and unbindable, else error out before waiting.
		if pidVar != "" {
			// Use the same assignment-target validator as read/printf so an
			// assoc subscript carrying a literal `]` (e.g. `wait -p A[$rkey]`
			// with rkey=`]`) is accepted, matching bash (builtins/wait.def
			// re-parses the target like any assignment LHS).
			targetBase := pidVar
			if b, _, ok := splitArrayRef(pidVar); ok {
				targetBase = b
			}
			if !r.builtinAssignNameValid(pidVar, r.builtinTargetQuoted(pos, targetBase)) {
				return failf(1, "wait: `%s': not a valid identifier\n", pidVar)
			}
			if vr := r.lookupVar(pidVar); vr.ReadOnly {
				return failf(1, "wait: %s: cannot unset: readonly variable\n", pidVar)
			}
			r.delVar(pidVar)
		}
		if waitNext {
			// `wait -n [jobspec...]` waits for the NEXT of the named jobs
			// (or, with no names, any background job) to finish, returning
			// with $? = that job's exit status. With -p VAR it also stores
			// the finishing job's PID into VAR (which may be an array
			// element like A[k], handled by setVarString's array path).
			candidates := r.bgProcs
			if len(remaining) > 0 {
				candidates = candidates[:0:0]
				for _, arg := range remaining {
					// bash's set_waitlist reports an invalid spec but
					// keeps scanning the rest, then waits for whatever
					// valid jobs were named.
					bg := r.resolveJobArg(arg)
					if bg == nil {
						if strings.HasPrefix(arg, "%") {
							r.errf("%swait: %s: no such job\n", r.bashErrPrefix(pos), arg)
						} else {
							r.errf("%swait: `%s': not a pid or valid job spec\n", r.bashErrPrefix(pos), arg)
						}
						continue
					}
					candidates = append(candidates, bg)
				}
			}
			// With no live candidates, bash's wait_for_any_job returns
			// -1, i.e. exit status 127.
			if len(candidates) == 0 {
				exit.code = 127
				break
			}
			// Any candidate already done?
			for _, bg := range candidates {
				select {
				case <-bg.done:
					exit = *bg.exit
					r.reapCoproc(bg)
					r.storeWaitPid(pidVar, bg)
					goto waitDone
				default:
				}
			}
			// None done yet; block until the first of them finishes.
			if !r.bashPPArmBeforeBlock(ctx) {
				return r.exit
			}
			if bg := waitAnyDone(candidates); bg != nil {
				exit = *bg.exit
				r.reapCoproc(bg)
				r.storeWaitPid(pidVar, bg)
			}
		waitDone:
			r.removeFinishedJobs()
			break
		}
		if len(remaining) == 0 {
			// "wait" without arguments returns exit status zero, unless a
			// trapped signal interrupts it (POSIX: return >128, then run
			// the trap at the next statement boundary).
			needArm := false
			for _, bg := range r.bgProcs {
				select {
				case <-bg.done:
				default:
					needArm = true
				}
			}
			if needArm && !r.bashPPArmBeforeBlock(ctx) {
				return r.exit
			}
			for _, bg := range r.bgProcs {
				if r.waitOrSignal(ctx, bg) {
					if err := ctx.Err(); err != nil {
						exit.fatal(err)
						return exit
					}
					_, num := r.peekPendingSignal()
					exit.code = uint8(128 + num)
					return exit
				}
				r.reapCoproc(bg)
			}
			r.removeFinishedJobs()
			break
		}
		for _, arg := range remaining {
			// Accept either the legacy "gN" sentinel ($! used to always
			// return that) or a real numeric OS PID (what $! now
			// returns when the bg statement spawned a real process).
			var bg *bgProc
			if strings.HasPrefix(arg, "%") {
				// Job-control notation is allowed even without monitor
				// mode (SUS requirement). An unknown spec is "no such
				// job" with exit 127.
				bg = r.resolveJobArg(arg)
				if bg == nil {
					return failf(127, "wait: %s: no such job\n", arg)
				}
			} else if rest, ok := strings.CutPrefix(arg, "g"); ok {
				bg = r.resolveJobArg(arg)
				if bg == nil {
					return failf(127, "wait: pid %s is not a child of this shell\n", "g"+rest)
				}
			} else {
				// bash only treats an argument as a PID when it begins
				// with a digit; anything else (a negative number, `+4`,
				// garbage) is "not a pid or valid job spec".
				if len(arg) == 0 || arg[0] < '0' || arg[0] > '9' {
					return failf(1, "wait: `%s': not a pid or valid job spec\n", arg)
				}
				pid, perr := strconv.ParseInt(arg, 10, 64)
				if perr != nil {
					return failf(1, "wait: `%s': not a pid or valid job spec\n", arg)
				}
				for _, candidate := range r.bgProcs {
					// Match a real OS PID (published by the exec handler) or
					// a coproc's synthetic `<NAME>_PID`. Skip coprocs already
					// reaped so a stale entry with a reused synthetic pid
					// doesn't shadow the live one.
					if candidate.matchesPid(pid) ||
						(candidate.coprocPid == pid && candidate.coprocPidVar != "") {
						bg = candidate
						break
					}
				}
				if bg == nil {
					if saved, ok := r.doneBgPids[pid]; ok {
						exit = saved
						delete(r.doneBgPids, pid)
						continue
					}
					return failf(127, "wait: pid %s is not a child of this shell\n", arg)
				}
			}
			select {
			case <-bg.done:
			default:
				if !r.bashPPArmBeforeBlock(ctx) {
					return r.exit
				}
			}
			if r.waitOrSignal(ctx, bg) {
				if err := ctx.Err(); err != nil {
					exit.fatal(err)
					return exit
				}
				_, num := r.peekPendingSignal()
				exit.code = uint8(128 + num)
				return exit
			}
			exit = *bg.exit
			r.reapCoproc(bg)
			r.storeWaitPid(pidVar, bg)
			r.removeJob(bg)
		}
	case "kill":
		if r.inBashPPTask() {
			failed := failf(2, "kill: process signals are unavailable inside a Bash++ task\n")
			failed.exiting = true
			return failed
		}
		// Bash kill accepts: `-l [signum|name…]`, `-s NAME pid…`, `-n NUM
		// pid…`, `-NAME pid…`, `-NUM pid…`, and `pid…` (default SIGTERM).
		// Job specs (`%1`) aren't supported because the in-process runner
		// has no real job table — `$!` returns a "g<N>" sentinel that is
		// not a real PID. The shared flagParser doesn't fit here because
		// `-SIGNAME` is a whole-arg flag, not stacked short flags.
		listOnly := false
		posixSignals := r.opts[optPosix]
		sig := defaultTermSignal
		// sawSignal tracks whether a signal has already been established via
		// -s/-n or a bare -SIGSPEC/-NUM flag. Once true, a later dash-prefixed
		// argument is no longer eligible for the bare-form reinterpretation
		// below — it is left for the target loop, since it may be a negative
		// pid naming a process group (e.g. `kill -s TERM -1234`).
		sawSignal := false
		remaining := args
	killFlags:
		for len(remaining) > 0 {
			arg := remaining[0]
			if !strings.HasPrefix(arg, "-") || arg == "-" {
				break
			}
			// -s<SPEC> / -n<NUM> are the attached-argument forms of `-s SPEC`
			// and `-n NUM` (e.g. `kill -sHUP` == `kill -s HUP`, `kill -n9` ==
			// `kill -n 9`), which bash's getopt-style kill accepts; the
			// exact-match switch below only handles the detached forms. Signal
			// names in the -NAME form are upper-case and the -NUM form is all
			// digits, so a lowercase "-s"/"-n" prefix is unambiguously the
			// option, not a signal spec.
			if len(arg) > 2 && strings.HasPrefix(arg, "-s") {
				spec := arg[2:]
				s, ok := parseSignalSpecPosix(spec, posixSignals)
				if !ok {
					return failf(1, "kill: %s: invalid signal specification\n", spec)
				}
				sig = s
				sawSignal = true
				remaining = remaining[1:]
				continue
			}
			if len(arg) > 2 && strings.HasPrefix(arg, "-n") {
				n, err := strconv.Atoi(arg[2:])
				if err != nil {
					return failf(2, "kill: -n requires a signal number\n")
				}
				s, _, ok := signalByNumber(n)
				if !ok {
					return failf(1, "kill: %d: invalid signal specification\n", n)
				}
				sig = s
				sawSignal = true
				remaining = remaining[1:]
				continue
			}
			switch arg {
			case "--":
				remaining = remaining[1:]
				break killFlags
			case "-l", "-L":
				listOnly = true
				remaining = remaining[1:]
			case "-s":
				if len(remaining) < 2 {
					r.errf("%skill: -s: option requires an argument\n", r.bashErrPrefix(pos))
					exit.code = 2
					return exit
				}
				// bash's `-s` sigspec accepts a name OR a number (incl. 0,
				// the null signal: `kill -s 0 $$` checks process existence),
				// same as the bare `-NAME`/`-NUM` form below — so use the
				// number-or-name parser, not the name-only lookup.
				s, ok := parseSignalSpecPosix(remaining[1], posixSignals)
				if !ok {
					return failf(1, "kill: %s: invalid signal specification\n", remaining[1])
				}
				sig = s
				sawSignal = true
				remaining = remaining[2:]
			case "-n":
				if len(remaining) < 2 {
					return failf(2, "kill: -n requires a signal number\n")
				}
				n, err := strconv.Atoi(remaining[1])
				if err != nil {
					return failf(2, "kill: -n requires a signal number\n")
				}
				s, _, ok := signalByNumber(n)
				if !ok {
					return failf(1, "kill: %d: invalid signal specification\n", n)
				}
				sig = s
				sawSignal = true
				remaining = remaining[2:]
			default:
				// -SIGNAME or -NUMBER (whole flag is the spec). Bash only
				// reinterprets a dash-prefixed argument as a bare signal spec
				// the first time (see kill.def: `saw_signal == 0`) — once a
				// signal is already set, later dash-prefixed arguments are
				// left alone since they may be negative pids naming a
				// process group (`kill -s TERM -1234`).
				if sawSignal {
					break killFlags
				}
				spec := strings.TrimPrefix(arg, "-")
				s, ok := parseSignalSpecPosix(spec, posixSignals)
				if !ok {
					return failf(1, "kill: %s: invalid signal specification\n", spec)
				}
				sig = s
				sawSignal = true
				remaining = remaining[1:]
				break killFlags
			}
		}
		if listOnly {
			if len(remaining) == 0 {
				r.printSignalList(posixSignals)
				break
			}
			for _, a := range remaining {
				if n, err := strconv.Atoi(a); err == nil {
					if n > 128 {
						n -= 128
					}
					// signalByNumber maps 0 to the pseudo-signal EXIT,
					// which bash 5.3 does print for `kill -l 0` (a normal
					// exit status carries no terminating signal). 128
					// itself is not adjusted and stays invalid, as in bash.
					if _, name, ok := signalByNumber(n); ok {
						r.outf("%s\n", name)
						continue
					}
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: %s: invalid signal specification\n", a)
					continue
				}
				if s, ok := signalByNamePosix(a, posixSignals); ok {
					n, _ := signalNumber(s)
					r.outf("%d\n", n)
					continue
				}
				exit.code = 1
				r.errf(r.bashErrPrefix(pos)+"kill: %s: invalid signal specification\n", a)
			}
			break
		}
		if len(remaining) == 0 {
			r.errf("kill: usage: %s\n", bashUsage["kill"])
			exit.code = 2
			return exit
		}
		for _, target := range remaining {
			if strings.HasPrefix(target, "%") {
				// Job-control notation is permitted without monitor mode
				// (SUS). Resolve the spec to its real OS PID and signal it.
				bg := r.resolveJobArg(target)
				if bg == nil {
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: %s: no such job\n", target)
					continue
				}
				if bg.pidReady != nil {
					select {
					case <-bg.pidReady:
					default:
					}
				}
				rp := jobSignalPid(bg)
				if rp == 0 {
					r.killSyntheticBg(bg, sig)
					continue
				}
				if asyncJobInheritedIgnore(bg, sig) {
					// The exec'd child of an async list would hold the
					// inherited SIG_IGN in a fork-based shell; the kernel
					// would discard this delivery. Successful no-op.
					continue
				}
				if err := sendSignal(rp, sig); err != nil {
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", int(bg.pid.Load()), err)
					continue
				}
				r.recordJobSignal(bg, sig)
				continue
			}
			if strings.HasPrefix(target, "g") {
				bg := r.resolveJobArg(target)
				if bg == nil {
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: (%s) - No such process\n", target)
					continue
				}
				if bg.pidReady != nil {
					select {
					case <-bg.pidReady:
					default:
					}
				}
				rp := jobSignalPid(bg)
				if rp == 0 {
					r.killSyntheticBg(bg, sig)
					continue
				}
				if asyncJobInheritedIgnore(bg, sig) {
					// The exec'd child of an async list would hold the
					// inherited SIG_IGN in a fork-based shell; the kernel
					// would discard this delivery. Successful no-op.
					continue
				}
				if err := sendSignal(rp, sig); err != nil {
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: (%s) - %v\n", target, err)
					continue
				}
				continue
			}
			pid, err := strconv.Atoi(target)
			if err != nil {
				exit.code = 1
				r.errf(r.bashErrPrefix(pos)+"kill: `%s': not a pid or valid job spec\n", target)
				continue
			}
			// A compound job's own kernel identity is its live proxy. Deliver
			// builtin self-signals synchronously, as for $$, so its next
			// statement sees the trap/default action without an IPC race.
			// External senders use the same live-disposition event path.
			if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil && bg.carrier != nil && pid == bg.carrier.Pid() {
				if _, live := bg.carrier.(SignalAwareCarrierProcess); live && !signalStopsJob(sig) && !signalContinuesJob(sig) {
					bg.deliverCarrierSignal(sigNum(sig))
					continue
				}
			}
			// A coproc's `<NAME>_PID` is a synthetic integer, not a real OS
			// pid; resolve it to the coprocess's real child so the signal
			// actually reaches the running command (e.g. `kill $COPROC_PID`).
			// The registry is shared with subshells, so this also works for
			// the common `{ sleep 1; kill $COPROC_PID; } &` idiom.
			if r.coprocReg != nil {
				if bg := r.coprocReg.lookup(int64(pid)); bg != nil {
					if rp := bg.publishedSignalPid(); rp != 0 {
						if err := sendSignal(int(rp), sig); err != nil {
							exit.code = 1
							r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", int(rp), err)
						}
					}
					r.killSyntheticBg(bg, sig)
					continue
				}
			}
			// A signal directed at our own $$ for which this runner (or a
			// foreground parent it runs inline within) owns a trap is
			// delivered synchronously into that runner's pending queue,
			// rather than via the OS — relying on signal.Notify here would
			// race with the trap firing before the next statement boundary.
			// A `(kill -SIG $$)` foreground subshell thus reliably fires the
			// parent's trap once the subshell finishes. Background subshells
			// run in their own goroutine (sigParent nil), so a backgrounded
			// `kill -SIG $$` still sends a real OS signal that the parent's
			// Notify catches.
			var replacement *execReplacementAttempt
			replacementPid := 0
			if r.asyncList && r.execReplacement != nil {
				replacement = r.execReplacement.current.Load()
				if replacement != nil {
					<-replacement.ready
					replacementPid = int(replacement.pid.Load())
				}
			}
			if pid == r.shellPid() || replacementPid > 0 && pid == replacementPid {
				// A top-level exec with live in-process background jobs cannot
				// use execve without destroying those jobs. While its external
				// replacement is proxied, asynchronous children still see $$ as
				// that replacement's process identity, just as after a real
				// execve. Route both probes and signals to the replacement child.
				if replacement != nil {
					if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil {
						replacement.observer.Store(bg)
					}
					if replacementPid == 0 {
						exit.code = 1
						continue
					}
					if err := sendSignal(replacementPid, sig); err != nil {
						exit.code = 1
						r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", replacementPid, err)
					}
					continue
				}
				if attempt, queued := r.routeAsyncOwnerSignal(sig); queued {
					continue
				} else if attempt != nil {
					<-attempt.ready
					if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil {
						attempt.observer.Store(bg)
					}
					pid := int(attempt.pid.Load())
					if pid == 0 {
						exit.code = 1
					} else if err := sendSignal(pid, sig); err != nil {
						exit.code = 1
						r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", pid, err)
					}
					continue
				}
				if sname, ok := signalName(sig); ok {
					if r.isStartupIgnored(sname) {
						// POSIX: a signal ignored on entry to a
						// non-interactive shell stays ignored, even
						// after an attempted trap/reset.
						continue
					}
					if owner := r.owningSignalRunner(sname); owner != nil {
						owner.markPendingSignal(sname)
						continue
					}
					if cb, ok := r.trapCallbacks[sname]; ok {
						if cb == "" {
							continue
						}
						// Background subshells inherit the trap table but not
						// the parent's signal.Notify ownership. Fall through to
						// a real OS signal so the foreground parent receives it.
					} else if !sigIsZero(sig) {
						if signalDefaultDoesNotTerminate(sig) {
							// Avoid sending these self-directed defaults to
							// the real Go process. Ignored/continue defaults
							// are successful no-ops. Stop defaults must not
							// become an exit status either: a real shell
							// resumes after a later CONT and keeps running
							// the script, while stopping this Go process
							// would suspend all in-process runners.
							continue
						}
						// A standalone shell owns its process signal disposition.
						// Relay the default action so its parent observes WIFSIGNALED,
						// rather than a normal exit with status 128+signal.
						if _, standalone := r.sigReset.(OSSignalResetter); standalone {
							if err := relayAsyncOwnerSignal(sig); err == nil {
								continue
							}
						}
						exit.code = uint8(128 + sigNum(sig))
						exit.exiting = true
						return exit
					}
				}
			}
			// A numeric PID that belongs to a background job (carrier
			// identity or an exec'd child) must update the job's state
			// machine just like %N and gN targets do. This closes a
			// wait-status lifecycle race for VSC-PCTS TP429: without it
			// the runner can neither set killedSignal nor cancel the
			// job's context when the OS signal arrives at a child PID,
			// relying solely on the carrier watcher or exec-handler
			// reaping to infer the signal. The resolveJobArg table
			// already matches numeric PIDs to bgProcs via matchesPid.
			if bg := r.resolveJobArg(target); bg != nil {
				if bg.pidReady != nil {
					select {
					case <-bg.pidReady:
					default:
					}
				}
				rp := jobSignalPid(bg)
				if rp == 0 {
					r.killSyntheticBg(bg, sig)
					continue
				}
				if asyncJobInheritedIgnore(bg, sig) {
					// The exec'd child of an async list would hold the
					// inherited SIG_IGN in a fork-based shell; the kernel
					// would discard this delivery. Successful no-op.
					continue
				}
				if err := sendSignal(rp, sig); err != nil {
					exit.code = 1
					r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", int(bg.pid.Load()), err)
					continue
				}
				r.recordJobSignal(bg, sig)
				continue
			}
			var groupTrapOwner *Runner
			var groupReceipt <-chan struct{}
			if !sigIsZero(sig) && targetsOwnProcessGroup(pid) {
				if name, ok := signalName(sig); ok {
					groupTrapOwner = r.owningSignalRunner(name)
					if groupTrapOwner != nil {
						groupReceipt = groupTrapOwner.groupSignalReceiptChan(name)
					}
				}
			}
			if err := sendSignal(pid, sig); err != nil {
				exit.code = 1
				r.errf(r.bashErrPrefix(pos)+"kill: (%d) - %v\n", pid, err)
			} else if groupReceipt != nil {
				select {
				case <-groupReceipt:
				case <-ctx.Done():
				}
			}
		}
	case "nohup":
		exit = r.runNohup(ctx, args)
	case "setsid":
		exit = r.runSetsid(ctx, args)
	case "disown":
		// Backgrounded `&` statements are goroutines, not kernel jobs, and
		// the runner never sends SIGHUP, so disown's only observable effect
		// here is removing entries from r.bgProcs — which matters because a
		// later argument-less `wait` blocks on every remaining bgProc. With
		// -h (keep the job but shield it from SIGHUP) there is nothing for
		// us to do, so the job stays waitable.
		all, noHup := false, false
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-a":
				all = true
			case "-h":
				noHup = true
			case "-r":
				// restrict to running jobs; all ours are "running"
			default:
				return invalidOpt("disown", flag)
			}
		}
		specs := fp.args()
		if !r.bashCompatErrors {
			switch {
			case noHup:
			case all:
				r.bgProcs = nil
			case len(specs) == 0:
				if jobs := r.realJobs(); len(jobs) > 0 {
					r.removeJob(jobs[len(jobs)-1])
				}
			default:
				for _, spec := range specs {
					if bg := r.resolveJobArg(spec); bg != nil {
						r.removeJob(bg)
					}
				}
			}
			break
		}
		switch {
		case noHup:
			if len(specs) == 0 {
				// Keep jobs in the table (we never deliver SIGHUP anyway).
				break
			}
			for _, spec := range specs {
				if bg := r.resolveJobArg(spec); bg == nil {
					if _, err := strconv.ParseInt(spec, 10, 64); err != nil && !strings.HasPrefix(spec, "%") {
						r.errf("%sdisown: warning: %s: job specification requires leading `%%'\n", r.bashErrPrefix(pos), spec)
					}
					exit.code = 1
					r.errf("%sdisown: %s: no such job\n", r.bashErrPrefix(pos), spec)
				}
			}
		case all:
			r.bgProcs = nil
		case len(specs) == 0:
			// disown the current (most recent) job.
			if jobs := r.realJobs(); len(jobs) > 0 {
				r.removeJob(jobs[len(jobs)-1])
			} else {
				r.errf("%sdisown: current: no such job\n", r.bashErrPrefix(pos))
				exit.code = 1
			}
		default:
			var remove []*bgProc
			for _, spec := range specs {
				bg := r.resolveJobArg(spec)
				if bg == nil {
					if _, err := strconv.ParseInt(spec, 10, 64); err != nil && !strings.HasPrefix(spec, "%") {
						r.errf("%sdisown: warning: %s: job specification requires leading `%%'\n", r.bashErrPrefix(pos), spec)
					}
					exit.code = 1
					r.errf("%sdisown: %s: no such job\n", r.bashErrPrefix(pos), spec)
					continue
				}
				remove = append(remove, bg)
			}
			for _, bg := range remove {
				r.removeJob(bg)
			}
		}
	case "builtin":
		if len(args) < 1 {
			break
		}
		if strings.HasPrefix(args[0], "-") && args[0] != "-" {
			r.errf("%sbuiltin: %s: invalid option\n", r.bashErrPrefix(pos), args[0])
			r.errf("builtin: usage: %s\n", bashUsage["builtin"])
			exit.code = 2
			return exit
		}
		if !IsBuiltin(args[0]) {
			r.errf("%sbuiltin: %s: not a shell builtin\n", r.bashErrPrefix(pos), args[0])
			exit.code = 1
			return exit
		}
		exit = r.builtin(ctx, pos, args[0], args[1:])
	case "type":
		skipFuncs := false
		showAll := false
		mode := "" // "", "-t", "-p", "-P"
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-a":
				showAll = true
			case "-f":
				skipFuncs = true
			case "-p", "-P", "-t":
				mode = flag
			default:
				return invalidOpt("type", flag)
			}
		}
		args := fp.args()
		anyNotFound := false
		for _, arg := range args {
			// -P always does PATH lookup, ignoring builtin/function/etc.
			if mode == "-P" {
				if path, err := LookPathDir(r.Dir, r.writeEnv, arg); err == nil {
					r.outf("%s\n", path)
				} else {
					anyNotFound = true
				}
				continue
			}
			matches := r.typeMatches(arg, skipFuncs, r.writeEnv, showAll)
			// -p: only print path if no non-file match exists.
			if mode == "-p" {
				var pathMatch string
				hasNonFile, sawFile := false, false
				for _, m := range matches {
					if m.kind != "file" {
						hasNonFile = true
						continue
					}
					// The first file-kind match is the one that would run;
					// an embedder-resolved name with no path prints nothing
					// rather than the PATH file it shadows.
					if !sawFile {
						sawFile, pathMatch = true, m.path
					}
				}
				if !hasNonFile && pathMatch != "" {
					r.outf("%s\n", pathMatch)
				}
				if len(matches) == 0 {
					anyNotFound = true
				}
				continue
			}
			if len(matches) == 0 {
				if mode != "-t" {
					r.errf(r.bashErrPrefix(pos)+"type: %s: not found\n", arg)
				}
				anyNotFound = true
				continue
			}
			toShow := matches
			if !showAll {
				toShow = matches[:1]
			}
			for _, m := range toShow {
				if mode == "-t" {
					r.outf("%s\n", m.kind)
				} else {
					r.outf("%s\n", m.desc)
					// `type funcname` (and -a) also dumps the
					// body in bash's `declare -f` shape.
					if mode == "" && m.kind == "function" {
						if body := r.Funcs[arg]; body != nil {
							r.printFuncDecl(arg, body)
						}
					}
				}
			}
		}
		if anyNotFound {
			exit.code = 1
		}
	case "caller":
		// Bash semantics:
		//  caller          — prints "<line> [<source>]" for the current
		//                    function call or "0 NULL" at top-level.
		//  caller <expr>   — prints "<line> <function> <source>" for the
		//                    frame at depth <expr>; errors if <expr> is
		//                    not an integer or is out of range.
		//  caller -X       — invalid option.
		if len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--" {
			r.errf("%scaller: %s: invalid option\ncaller: usage: caller [expr]\n",
				r.bashErrPrefix(pos), args[0])
			exit.code = 2
			return exit
		}
		if len(args) > 1 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) > 1 {
			r.errf("caller: usage: caller [expr]\n")
			exit.code = 1
			return exit
		}
		if len(args) == 0 {
			// "Implicit" caller: print line + source of the
			// immediate caller, or "0 NULL" if at the top level.
			if len(r.callStack) == 0 {
				if r.commandString || r.filename == "" {
					// `bash -c caller` has no frame at all.
					exit.code = 1
					break
				}
				r.outf("0 NULL\n")
				break
			}
			frame := r.callStack[len(r.callStack)-1]
			r.outf("%d %s\n", frame.line, frame.source)
			break
		}
		level, err := strconv.Atoi(args[0])
		if err != nil || level < 0 {
			r.errf("%scaller: %s: invalid number\ncaller: usage: caller [expr]\n",
				r.bashErrPrefix(pos), args[0])
			exit.code = 1
			return exit
		}
		// The outermost frame (the top-level caller, "main") is reported
		// only when it came from a script file: `bash script` prints
		// "4 main script" (dbg-support.tests), a `bash -c` string has no
		// such frame and fails.
		if idx := len(r.callStack) - 1 - level; level < len(r.callStack) &&
			(idx > 0 || (!r.commandString && r.callStack[idx].source != "")) {
			frame := r.callStack[idx]
			funcName := "main"
			if idx > 0 {
				funcName = r.callStack[idx-1].funcName
			}
			source := frame.source
			if source == "" {
				source = "bash"
			}
			r.outf("%d %s %s\n", frame.line, funcName, source)
		} else {
			exit.code = 1
		}
	case "hash":
		fp := flagParser{remaining: args}
		clearHash := false
		var explicitPath string
		listOnly := false
		printPath := false
		deleteNames := false
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-r":
				clearHash = true
			case "-p":
				// `hash -p PATH NAME`: cache NAME with the
				// given PATH instead of searching $PATH.
				explicitPath = fp.value()
				if explicitPath == "" {
					return failf(2, "hash: -p: option requires an argument\n")
				}
			case "-l":
				// `hash -l`: emit reusable `builtin hash …` form.
				listOnly = true
			case "-t":
				// `hash -t name …`: print just the path for each
				// hashed name. Handled below in name loop.
				printPath = true
			case "-d":
				// `hash -d NAME`: forget specific name.
				deleteNames = true
			default:
				// bash 5.3 treats an unknown hash option as a usage error
				// with EX_USAGE (2), in and out of POSIX mode.
				r.errf("%shash: %s: invalid option\n", r.bashErrPrefix(pos), flag)
				r.errf("hash: usage: %s\n", bashUsage["hash"])
				exit.code = 2
				return exit
			}
		}
		remaining := fp.args()
		if len(remaining) == 0 && (deleteNames || printPath) {
			option := "-t"
			if deleteNames {
				option = "-d"
			}
			return failf(1, "hash: %s: option requires an argument\n", option)
		}
		if len(remaining) == 0 && explicitPath != "" {
			r.errf("%shash: -p: option requires an argument\n", r.bashErrPrefix(pos))
			r.errf("hash: usage: %s\n", bashUsage["hash"])
			exit.code = 2
			return exit
		}
		if enabled, ok := r.noOpSetState["hashall"]; ok && !enabled &&
			(len(remaining) > 0 || explicitPath != "" || printPath || deleteNames) {
			return failf(1, "hash: hashing disabled\n")
		}
		hashListEntry := func(name string, entry cmdHashEntry) {
			r.outf("builtin hash -p %s %s\n", entry.path, name)
		}
		if clearHash {
			clear(r.cmdHashTable)
			if len(remaining) == 0 {
				break
			}
		}
		if deleteNames {
			if len(remaining) == 0 {
				return failf(2, "hash: -d: option requires an argument\n")
			}
			for _, name := range remaining {
				if _, ok := r.cmdHashTable[name]; !ok {
					r.errf(r.bashErrPrefix(pos)+"hash: %s: not found\n", name)
					exit.code = 1
					continue
				}
				delete(r.cmdHashTable, name)
			}
			break
		}
		if printPath {
			for _, name := range remaining {
				entry, ok := r.cmdHashTable[name]
				if !ok {
					r.errf(r.bashErrPrefix(pos)+"hash: %s: not found\n", name)
					exit.code = 1
					continue
				}
				if listOnly {
					hashListEntry(name, entry)
				} else {
					r.outf("%s\n", entry.path)
				}
			}
			break
		}
		if len(remaining) == 0 {
			// List cached commands in bash's format:
			//   hits	command
			//      N	/path
			if len(r.cmdHashTable) == 0 {
				if !r.opts[optPosix] {
					r.outf("hash: hash table empty\n")
				}
				break
			}
			names := make([]string, 0, len(r.cmdHashTable))
			vr := r.lookupVar("BASH_CMDS")
			if vr.Kind == expand.Associative {
				names = vr.AssocKeysForDeclare()
			}
			if listOnly {
				for _, name := range names {
					hashListEntry(name, r.cmdHashTable[name])
				}
			} else {
				r.outf("hits\tcommand\n")
				for _, name := range names {
					entry := r.cmdHashTable[name]
					r.outf("%4d\t%s\n", entry.hits, entry.path)
				}
			}
			break
		}
		// Cache specific commands.
		for _, name := range remaining {
			var path string
			if explicitPath != "" {
				if info, err := os.Stat(explicitPath); err == nil && info.IsDir() {
					r.errf("%shash: %s: Is a directory\n", r.bashErrPrefix(pos), explicitPath)
					exit.code = 1
					continue
				}
				if r.opts[optRestricted] {
					if strings.Contains(explicitPath, "/") {
						r.errf("%shash: %s: restricted\n", r.bashErrPrefix(pos), explicitPath)
						exit.code = 1
						continue
					}
					if _, err := LookPathDir(r.Dir, r.writeEnv, explicitPath); err != nil {
						r.errf("%shash: %s: not found\n", r.bashErrPrefix(pos), explicitPath)
						exit.code = 1
						continue
					}
				}
				path = explicitPath
			} else {
				// bash 5.3 hashes only PATH-searched externals: a name that
				// resolves to a shell function or an enabled builtin, or that
				// contains a slash, is silently skipped with success — POSIX
				// likewise says built-ins shall not be reported by hash.
				// An explicit `hash -p path name` still caches any name.
				if r.Funcs[name] != nil || (IsBuiltin(name) && !r.disabledBuiltins[name]) ||
					strings.ContainsRune(name, '/') {
					continue
				}
				p, err := LookPathDir(r.Dir, r.writeEnv, name)
				if err != nil {
					r.errf(r.bashErrPrefix(pos)+"hash: %s: not found\n", name)
					exit.code = 1
					continue
				}
				path = p
			}
			if r.cmdHashTable == nil {
				r.cmdHashTable = make(map[string]cmdHashEntry)
			}
			r.cmdHashTable[name] = cmdHashEntry{path: path}
		}
	case "help":
		mode := ""
		var patterns []string
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch arg {
			case "--":
				patterns = append(patterns, args[i+1:]...)
				i = len(args)
			case "-d":
				mode = "description"
			case "-m":
				mode = "man"
			case "-s":
				mode = "short"
			default:
				if strings.HasPrefix(arg, "-") {
					r.errf("%shelp: %s: invalid option\n", r.bashErrPrefix(pos), arg)
					r.errf("help: usage: help [-dms] [pattern ...]\n")
					exit.code = 2
					return exit
				}
				patterns = append(patterns, arg)
			}
		}
		if len(patterns) == 0 {
			r.outf("%s", bashHelpOverview())
			break
		}
		for _, pattern := range patterns {
			if !r.helpTopic(pattern, mode) {
				r.errf(r.bashErrPrefix(pos)+"help: no help topics match `%s'.  Try `help help' or `man -k %s' or `info %s'.\n", pattern, pattern, pattern)
				exit.code = 1
			}
		}
	case "enable":
		fp := flagParser{remaining: args}
		disable := false
		deleteDynamic := false
		loadDynamic := false
		listAll := false
		showAll := false
		specialOnly := false
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-n":
				disable = true
			case "-p":
				// `enable -p` lists all builtins in the format
				// `enable [-n] NAME` per builtin.
				listAll = true
			case "-a":
				// `enable -a` lists all builtins regardless of
				// enable/disable state.
				showAll = true
				listAll = true
			case "-s":
				// `-s` filters the listing to POSIX special
				// builtins (1003.1 § 2.14). Bash combines it
				// with `-p`/`-n`/`-a` for that subset.
				specialOnly = true
				listAll = true
			case "-f":
				// `-f` loads dynamic builtins, which are not
				// supported. Accept the flag and consume its
				// filename argument to avoid breaking scripts.
				fp.value()
				loadDynamic = true
			case "-d":
				// `-d` deletes a dynamically-loaded builtin.
				deleteDynamic = true
			default:
				return failf(2, "enable: %s: invalid option\n", flag)
			}
		}
		remaining := fp.args()
		if len(remaining) == 0 {
			if listAll {
				// Print every recognised builtin, marking disabled
				// ones with the `-n` flag. Bash sorts alphabetically.
				names := []string{
					":", ".", "[", "alias", "bg", "bind", "break", "builtin",
					"caller", "cd", "command", "compgen", "complete", "compopt",
					"continue", "declare", "dirs", "disown", "echo", "enable",
					"eval", "exec", "exit", "export", "false", "fc", "fg",
					"getopts", "hash", "help", "history", "jobs", "kill",
					"let", "local", "logout", "mapfile", "popd", "printf",
					"pushd", "pwd", "read", "readarray", "readonly", "return",
					"set", "shift", "shopt", "source", "suspend", "test",
					"times", "trap", "true", "type", "typeset", "ulimit",
					"umask", "unalias", "unset", "wait",
				}
				sort.Strings(names)
				for _, n := range names {
					if specialOnly && !isPosixSpecialBuiltin(n) {
						continue
					}
					if r.disabledBuiltins[n] {
						if disable || showAll || specialOnly {
							r.outf("enable -n %s\n", n)
						}
					} else if !disable {
						r.outf("enable %s\n", n)
					}
				}
				break
			}
			// Default (no args, no -p): list only enabled OR only
			// disabled depending on `-n`.
			if disable {
				for name := range r.disabledBuiltins {
					r.outf("enable -n %s\n", name)
				}
			}
			break
		}
		for _, name := range remaining {
			if !IsBuiltin(name) {
				r.errf(r.bashErrPrefix(pos)+"enable: %s: not a shell builtin\n", name)
				exit.code = 1
				continue
			}
			if deleteDynamic {
				if r.dynamicBuiltins[name] {
					delete(r.dynamicBuiltins, name)
					if r.disabledBuiltins == nil {
						r.disabledBuiltins = make(map[string]bool)
					}
					r.disabledBuiltins[name] = true
					continue
				}
				r.errf(r.bashErrPrefix(pos)+"enable: %s: not dynamically loaded\n", name)
				exit.code = 1
				continue
			}
			if loadDynamic {
				if r.dynamicBuiltins == nil {
					r.dynamicBuiltins = make(map[string]bool)
				}
				r.dynamicBuiltins[name] = true
			}
			if disable {
				if r.disabledBuiltins == nil {
					r.disabledBuiltins = make(map[string]bool)
				}
				r.disabledBuiltins[name] = true
			} else {
				delete(r.disabledBuiltins, name)
			}
		}
	case "eval":
		if len(args) > 0 {
			switch first := args[0]; {
			case first == "--":
				args = args[1:]
			case strings.HasPrefix(first, "-") && first != "-":
				r.errf("%seval: %s: invalid option\n", r.bashErrPrefix(pos), first)
				r.errf("eval: usage: %s\n", bashUsage["eval"])
				exit.code = 2
				return exit
			}
		}
		src := strings.Join(args, " ")
		p := syntax.NewParser()
		if r.LangVariant() == syntax.LangBashPP {
			syntax.Variant(syntax.LangBashPP)(p)
		}
		file, err := p.Parse(strings.NewReader(src), "")
		if err != nil {
			if r.opts[optExpandAliases] {
				if expanded, ok := r.expandRawAliasSource(src); ok {
					if retry, rerr := p.Parse(strings.NewReader(expanded), ""); rerr == nil {
						r.withAliasReparse(r.aliasUseLine(int(pos.Line())), func() {
							r.stmts(ctx, retry.Stmts)
						})
						exit = r.exit
						break
					}
				}
			}
			// POSIX mode: a syntax error in the text given to eval is
			// fatal in a non-interactive shell (POSIX.1 §2.8.1), since
			// eval is a special builtin. Only a parse failure exits;
			// the eval'd command's own non-zero status (e.g. `eval
			// 'false'`) is not a syntax error and keeps running. This
			// is the only parse-error path (the alias retry above
			// breaks out on a successful re-parse).
			if r.opts[optPosix] && !r.interactiveShell {
				exit.exiting = true
			}
			// Bash 5.3 prints eval-time parse errors as
			// `<file>: eval: line N: <text>` — `eval:` lives between
			// the filename and `line N:`, not after it as `failf`
			// would arrange. The line N refers to the outer-script
			// line where `eval` was called.
			var pe syntax.ParseError
			if r.bashCompatErrors && errors.As(err, &pe) {
				name := r.filename
				if name == "" {
					name = "bash"
				}
				text := pe.Text
				// The parser stamps "from `(' command on line N"
				// with the line inside the eval'd string; bash
				// counts from the top of the enclosing script.
				// Re-base before the rewrites below (the `{` case
				// constructs its own absolute line).
				if i := strings.LastIndex(text, " command on line "); i >= 0 {
					if n, aerr := strconv.Atoi(text[i+len(" command on line "):]); aerr == nil {
						text = fmt.Sprintf("%s command on line %d",
							text[:i], n+int(pos.Line())-1)
					}
				}
				// Rewrite our generic "statements must be separated"
				// message to bash's "syntax error near unexpected
				// token `X'" form when we can identify the
				// offending token from the source.
				switch {
				case text == "statements must be separated by &, ; or a newline":
					if tok := offendingToken(src, pe.Pos); tok != "" {
						text = fmt.Sprintf("syntax error near unexpected token `%s'", tok)
					}
				case text == "unexpected EOF while looking for matching `}'" && strings.HasSuffix(src, "\\"):
					if openLine := firstBraceLine(src); openLine > 0 {
						absLine := int(pos.Line()) + openLine - 1
						text = fmt.Sprintf(
							"syntax error: unexpected end of file from `{' command on line %d",
							absLine)
					}
				case strings.HasPrefix(text, "reached EOF without matching"):
					// Bash phrases unclosed brace/paren EOFs as
					// "unexpected EOF while looking for matching `X'".
					// An unclosed bare `{` (block) uses a special
					// shape that points back at the opening line.
					switch {
					case strings.Contains(text, "`${`"):
						text = "unexpected EOF while looking for matching `}'"
					case strings.Contains(text, "`{`"):
						if openLine := firstBraceLine(src); openLine > 0 {
							// Map eval-source-relative line back
							// to the outer-script line by adding
							// the eval call's own line minus 1.
							absLine := int(pos.Line()) + openLine - 1
							text = fmt.Sprintf(
								"syntax error: unexpected end of file from `{' command on line %d",
								absLine)
						} else {
							text = "unexpected EOF while looking for matching `}'"
						}
					case strings.Contains(text, "`$(`") || strings.Contains(text, "`(`"):
						text = "unexpected EOF while looking for matching `)'"
					}
				}
				// An arithmetic operator error inside $((...)) or
				// $[...] is a *runtime* arithmetic error in bash,
				// which defers arithmetic parsing to expansion time:
				// no `eval:` tag, no source echo, and the evaluator's
				// "operand expected" shape.
				if strings.HasSuffix(text, "must follow an expression") {
					if srcLine := evalSourceLine(src, int(pe.Pos.Line())); srcLine != "" {
						if expr, ok := innerArithText(srcLine); ok {
							r.errf("%s: line %d: %s: arithmetic syntax error: operand expected (error token is %q)\n",
								name, pos.Line(), expr, expr)
							exit.code = 1
							return exit
						}
					}
				}
				// bash reports the eval-time EOF on the line right
				// after the eval source ran out, not the eval-call
				// line itself. Add the eval source's line count to
				// approximate; a trailing `\` in the eval payload
				// triggers bash's line-continuation reader, so
				// count one extra line for that case.
				evalLine := int(pos.Line())
				if strings.HasPrefix(text, "unexpected EOF") ||
					strings.HasPrefix(text, "syntax error: unexpected end of file") {
					evalLine += strings.Count(src, "\n") + 1
					if strings.HasSuffix(src, "\\") {
						evalLine++
					}
				}
				r.errf("%s: eval: line %d: %s\n", name, evalLine, text)
				// Bash also echoes the offending source line on a
				// second `<file>: eval: line N: \`<line>'` line —
				// except for "unexpected EOF" / "syntax error:
				// unexpected end of file" diagnostics, which
				// already self-describe the unclosed token.
				if !strings.HasPrefix(text, "unexpected EOF") &&
					!strings.HasPrefix(text, "syntax error: unexpected end of file") {
					if line := evalSourceLine(src, int(pe.Pos.Line())); line != "" {
						r.errf("%s: eval: line %d: `%s'\n", name, pos.Line(), line)
					}
				}
				exit.code = 1
				return exit
			}
			return failf(1, "eval: %v\n", err)
		}
		// Bash anchors eval'd code to the absolute script line where it
		// physically sits: an `eval` on line N reports a runtime error
		// in its (line-1-based) body at N+line-1. Accumulate so nested
		// evals stack correctly.
		prevEvalOffset, prevEvalExec := r.evalLineOffset, r.evalExec
		r.evalLineOffset += int(pos.Line()) - 1
		r.evalExec++
		r.withAliasReparse(r.aliasUseLine(int(pos.Line())), func() {
			r.stmts(ctx, file.Stmts)
		})
		r.evalLineOffset, r.evalExec = prevEvalOffset, prevEvalExec
		exit = r.exit
		// A variable-assignment error inside the eval'd text (e.g.
		// assigning to a readonly variable) discards the eval'd command
		// list, but bash 5.3 contains that DISCARD within eval: eval
		// simply returns a non-zero status and the surrounding
		// function/script keeps running. Convert the discard back to a
		// plain non-zero exit so it does not abort eval's caller. A real
		// `exit` carries `exiting` without `discarding` and still
		// propagates; POSIX mode never sets `discarding` for these
		// errors, so its fatal-exit behaviour is preserved.
		if exit.discarding {
			exit.exiting = false
			exit.discarding = false
			// A standalone readonly assignment also records its physical
			// line so the rest of that line is skipped, but the line
			// belongs to the eval'd payload, not the outer script;
			// clear it so it cannot spuriously skip an outer statement
			// sharing that line number.
			r.discardRestOfLine = 0
			if exit.code == 0 {
				exit.code = 1
			}
		}
	case "source", ".":
		savedAgentic := r.bashPPAgentic
		r.bashPPAgentic = false
		defer func() { r.bashPPAgentic = savedAgentic }()
		// Bash 5.3: accept `-p PATH` to override the search path.
		var pathOverride string
		havePathOverride := false
		var parsedArgs []string
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "--":
				parsedArgs = append(parsedArgs, args[i+1:]...)
				i = len(args)
			case arg == "-p":
				if i+1 >= len(args) {
					return failf(2, "%s: -p: option requires an argument\n", name)
				}
				i++
				pathOverride = args[i]
				havePathOverride = true
			case strings.HasPrefix(arg, "-") && len(arg) > 1:
				return failf(2, "%s: %s: invalid option\n%s: usage: %s\n",
					name, arg, name, bashUsage[name])
			default:
				parsedArgs = append(parsedArgs, args[i:]...)
				i = len(args)
			}
		}
		args = parsedArgs
		if len(args) < 1 {
			r.errf("%s%s: filename argument required\n%s: usage: %s\n",
				r.bashErrPrefix(pos), name, name, bashUsage[name])
			exit.code = 2
			return exit
		}
		if r.opts[optRestricted] && strings.Contains(args[0], "/") {
			r.errf("%s.: %s: restricted\n", r.bashErrPrefix(pos), args[0])
			exit.code = 1
			return exit
		}
		var path string
		var err error
		if havePathOverride {
			// Search the explicit PATH only; layer a one-off overlay on
			// top of the writeEnv so PATH is overridden but other env
			// stays intact for the search.
			overlay := newOverlayEnviron(r.writeEnv, false)
			overlay.Set("PATH", expand.Variable{Kind: expand.String, Str: pathOverride})
			path, err = scriptFromPathDir(r.Dir, overlay, args[0])
		} else {
			path, err = scriptFromPathDir(r.Dir, r.writeEnv, args[0])
		}
		if err != nil {
			// If the script was not found in PATH or there was any error, pass
			// the source path to the open handler so it has a chance to look
			// at files it manages (eg: virtual filesystem), and also allow
			// it to look for the sourced script in the current directory.
			if havePathOverride || (r.opts[optPosix] && !strings.Contains(args[0], "/")) {
				r.errf("%s%s: %s: file not found\n", r.bashErrPrefix(pos), name, args[0])
				if r.opts[optPosix] && !r.interactiveShell {
					exit.exiting = true
				}
				exit.code = 1
				return exit
			}
			path = args[0]
		}
		// In bash-compat mode, let r.open print its own bash-shaped
		// `<file>: line N: <path>: No such file or directory` line and
		// avoid stacking a redundant `source: ` prefix on top. Outside
		// compat mode, keep the legacy "source: <go-error>" wording.
		f, err := r.bashPPTaskOpen(ctx, path, os.O_RDONLY, 0, r.bashCompatErrors, true)
		if err != nil {
			if r.bashCompatErrors {
				if r.opts[optPosix] && !r.interactiveShell {
					exit.exiting = true
				}
				exit.code = 1
				return exit
			}
			return failf(1, "source: %v\n", err)
		}
		if r.inBashPPTask() && r.bashPPTaskContext(ctx).Err() != nil {
			_ = f.Close()
			r.bashPPTaskCanceled = true
			r.exit.fatal(r.bashPPTaskContext(ctx).Err())
			return r.exit
		}
		defer f.Close()
		p := syntax.NewParser()
		if r.LangVariant() == syntax.LangBashPP {
			syntax.Variant(syntax.LangBashPP)(p)
		}
		var file *syntax.File
		if r.bashCompatErrors {
			// Bash evalfile.c: a sourced directory is `<builtin>: <path>:
			// is a directory` (status 1); a binary file (ELF magic, a NUL
			// in the first line, or >256 NULs total) is `<builtin>:
			// <path>: cannot execute binary file` (status 126,
			// EX_BINARY_FILE).
			if info, serr := r.stat(ctx, path); serr == nil && info.IsDir() {
				r.errf("%s%s: %s: is a directory\n", r.bashErrPrefix(pos), name, path)
				exit.code = 1
				return exit
			}
			content, rerr := io.ReadAll(f)
			if rerr != nil {
				r.errf("%s%s: %s: %s\n", r.bashErrPrefix(pos), name, path, bashOSError(rerr))
				exit.code = 1
				return exit
			}
			if isBinarySource(content) {
				r.errf("%s%s: %s: cannot execute binary file\n", r.bashErrPrefix(pos), name, path)
				exit.code = 126
				return exit
			}
			if r.noOpSetState["verbose"] {
				r.errf("%s", content)
			}
			file, err = p.Parse(bytes.NewReader(content), path)
		} else {
			if r.noOpSetState["verbose"] {
				content, rerr := io.ReadAll(f)
				if rerr != nil {
					return failf(1, "source: %v\n", rerr)
				}
				r.errf("%s", content)
				file, err = p.Parse(bytes.NewReader(content), path)
			} else {
				file, err = p.Parse(f, path)
			}
		}
		if err != nil {
			return failf(1, "source: %v\n", err)
		}
		restoreForeign, err := r.bashPPPrepareSourceBlocks(ctx, file)
		if err != nil {
			return failf(1, "source: %v\n", err)
		}
		defer restoreForeign()

		// Keep the current versions of some fields we might modify.
		oldParams := r.Params
		oldSourceSetParams := r.sourceSetParams
		oldInSource := r.inSource
		oldFilename := r.filename
		oldArgv0 := r.argv0
		callerArgv0 := r.shellArgv0()
		oldStdinSourceBaseOffset := r.stdinSourceBaseOffset

		// If we run "source file args...", set said args as parameters.
		// Otherwise, keep the current parameters.
		sourceArgs := len(args[1:]) > 0
		if sourceArgs {
			r.Params = args[1:]
			r.sourceSetParams = false
		}
		// We want to track if the sourced file explicitly sets the
		// parameters.
		r.sourceSetParams = false
		r.inSource = true // know that we're inside a sourced script.
		// A sourced file executes in the current shell environment, so it
		// must retain the caller's $0. Keep filename separate: it identifies
		// the sourced file for diagnostics and BASH_SOURCE.
		r.argv0 = callerArgv0
		r.filename = path
		if r.stdinSourceActive && r.curStmtEnd.IsValid() {
			r.stdinSourceBaseOffset = r.stdinSourceStartOffset()
		}
		r.callStack = append(r.callStack, callFrame{
			line:         pos.Line(),
			source:       path,
			callerSource: oldFilename,
			funcName:     "source",
			// A sourced file runs in the current execution environment, but
			// the DEBUG trap is inherited by its commands only when functrace
			// is on (set -T / -o functrace) or the enclosing function frame
			// is itself traced — sourcing at the top level with functrace off
			// does not fire DEBUG for the sourced commands, matching bash.
			debugTrace: r.functraceEnabled() ||
				(len(r.callStack) > 0 && r.callStack[len(r.callStack)-1].debugTrace),
		})
		r.withAliasReparse(r.aliasUseLine(int(pos.Line())), func() {
			r.stmts(ctx, file.Stmts)
		})
		r.callStack = r.callStack[:len(r.callStack)-1]
		if r.trapCallbacks["RETURN"] != "" {
			prevLineno := r.ecfg.OverrideLineno
			prevDebugTrap := r.trapCallbacks["DEBUG"]
			r.ecfg.OverrideLineno = int(pos.Line())
			if !r.functraceEnabled() && len(r.callStack) == 0 {
				r.removeTrapCallback("DEBUG")
			}
			r.trapCallback(ctx, r.trapCallbacks["RETURN"], "return")
			if prevDebugTrap != "" {
				r.setTrapCallback("DEBUG", prevDebugTrap)
			}
			r.ecfg.OverrideLineno = prevLineno
		}
		r.filename = oldFilename
		// BASH_ARGV0 assignments made by the sourced file persist. Otherwise
		// restore the unset state so later direct Run calls still derive $0
		// from their own script filename.
		if r.argv0 == callerArgv0 {
			r.argv0 = oldArgv0
		}
		r.stdinSourceBaseOffset = oldStdinSourceBaseOffset

		// If we modified the parameters and the sourced file didn't
		// explicitly set them, we restore the old ones.
		if sourceArgs && !r.sourceSetParams {
			r.Params = oldParams
		}
		r.sourceSetParams = oldSourceSetParams
		r.inSource = oldInSource

		exit = r.exit
		exit.returning = false
	case "[":
		if len(args) == 0 || args[len(args)-1] != "]" {
			return failf(2, "[: missing `]'\n")
		}
		args = args[:len(args)-1]
		fallthrough
	case "test":
		// bash arg-count quirks for `test`:
		//
		//   - 2 args: usually `-X arg` (unary). bash picks unary
		//     when neither side looks like the operator it needs:
		//     args[1] isn't a known *binary* op AND args[0] isn't
		//     a known *unary* op (or `!`). In that case the
		//     diagnostic blames args[0] with "unary operator
		//     expected". When args[1] IS a binary op (e.g.
		//     `a -a`), bash instead treats it as a binary form
		//     missing the right operand — let the parser surface
		//     that.
		if len(args) == 2 && testBinaryOp(args[1]) == illegalTok {
			u := testUnaryOp(args[0])
			// `(` is in our unary table as TsParen (grouping),
			// but bash doesn't accept it as a true unary operator
			// in the 2-arg form — treat it like an unknown name.
			if (u == illegalTok || u == syntax.TsParen) && args[0] != "!" {
				r.errf("%s%s: %s: unary operator expected\n",
					r.bashErrPrefix(pos), name, args[0])
				exit.code = 2
				return exit
			}
		}
		if len(args) == 3 {
			if op := testBinaryOp(args[1]); op != illegalTok &&
				op != syntax.AndTest && op != syntax.OrTest {
				word := func(s string) *syntax.Word {
					return &syntax.Word{Parts: []syntax.WordPart{
						&syntax.Lit{Value: s},
					}}
				}
				expr := &syntax.BinaryTest{
					Op: op,
					X:  word(args[0]),
					Y:  word(args[2]),
				}
				r.testIntErr = ""
				exit.oneIf(r.bashTest(ctx, expr, true) == "")
				if r.testIntErr != "" {
					inner := name
					r.errf("%s%s: %s: integer expected\n",
						r.bashErrPrefix(pos), inner, r.testIntErr)
					r.testIntErr = ""
					exit.code = 2
				}
				return exit
			}
			if r.bashCompatErrors && args[0] == "-n" {
				r.errf("%s%s: %s: binary operator expected\n",
					r.bashErrPrefix(pos), name, args[1])
				exit.code = 2
				return exit
			}
			if r.bashCompatErrors && args[0] == "-v" {
				r.errf("%s%s: %s: binary operator expected\n",
					r.bashErrPrefix(pos), name, args[1])
				exit.code = 2
				return exit
			}
		}
		// 3-arg `arg1 OP arg2` where OP isn't a binary operator:
		// bash 5.3 emits `<OP>: binary operator expected` rather
		// than the generic "too many arguments" path used for 4+
		// args. Catch it here so the testParser doesn't take the
		// general recursive route for this case. Skip if args[0]
		// is `!` / `(` (grouping/negation), or any known unary op
		// — those go through the recursive parser unchanged.
		if len(args) == 3 &&
			testBinaryOp(args[1]) == illegalTok &&
			args[0] != "!" && args[0] != "(" &&
			testUnaryOp(args[0]) == illegalTok {
			r.errf("%s%s: %s: binary operator expected\n",
				r.bashErrPrefix(pos), name, args[1])
			exit.code = 2
			return exit
		}
		parseErr := false
		closer := ""
		if name == "[" {
			closer = "]"
		}
		p := testParser{
			rem:    args,
			closer: closer,
			err: func(err error) {
				// bash format: `<file>: line N: <test|[>: <msg>`
				r.errf("%s%s: %v\n",
					r.bashErrPrefix(pos), name, err)
				parseErr = true
			},
		}
		p.next()
		expr := p.classicTest("[", false)
		if parseErr {
			exit.code = 2
			return exit
		}
		r.testIntErr = ""
		exit.oneIf(r.bashTest(ctx, expr, true) == "")
		if r.testIntErr != "" {
			// bash: `<file>: line N: test: <arg>: integer expected` (exit 2).
			// The `[` form uses `[` instead of `test` as the inner name.
			inner := name
			r.errf("%s%s: %s: integer expected\n",
				r.bashErrPrefix(pos), inner, r.testIntErr)
			r.testIntErr = ""
			exit.code = 2
		}
	case "strmatch":
		if len(args) != 2 {
			return failf(2, "strmatch: usage: strmatch string pattern\n")
		}
		if !bashStrmatch(args[1], args[0]) {
			exit.code = 1
		}
	case "exec":
		if r.opts[optRestricted] {
			r.errf("%sexec: restricted\n", r.bashErrPrefix(pos))
			exit.code = 1
			return exit
		}
		// TODO: Consider unix.Exec, i.e. actually replacing
		// the process. It's in theory what a shell should do,
		// but in practice it would kill the entire Go process
		// and it's not available on Windows.
		var argv0 string
		clearEnv := false
		loginShell := false
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-a":
				argv0 = fp.value()
				if argv0 == "" {
					return failf(2, "exec: -a: option requires an argument\n")
				}
			case "-c":
				// bash 5.3 `-c` clears the environment for the
				// exec'd command.
				clearEnv = true
			case "-l":
				// bash 5.3 `-l` makes the exec'd shell act as a
				// login shell by prefixing argv[0] with `-`.
				loginShell = true
			default:
				return invalidOpt("exec", flag)
			}
		}
		args = fp.args()
		if len(args) == 0 {
			r.persistCurrentRedirs()
			break
		}
		if loginShell {
			if argv0 != "" {
				argv0 = "-" + argv0
			} else if len(args) > 0 {
				argv0 = "-" + filepath.Base(args[0])
			}
		}
		// Bash `exec NAME`: if NAME can't be started, print the
		// diagnostic and either exit the shell or, with `shopt -s
		// execfail` (no_exit_on_failed_exec), stay alive with the failure
		// status. The redirections applied for this command are undone on
		// the failure path (no redirection scope is persisted), matching
		// exec17.sub.
		execfail := false
		if opt, _ := r.bashOptByName("execfail"); opt != nil {
			execfail = *opt
		}
		if tail, code, startErr := r.execStartError(ctx, args[0]); startErr {
			r.errf("%s%s\n", r.bashErrPrefix(pos), tail)
			r.reportError("exec", pos, args[0], tail, code)
			exit.code = code
			if !execfail && !r.interactiveShell {
				exit.exiting = true
			}
			return exit
		}
		r.exit.exiting = true
		r.execAs(ctx, pos, argv0, clearEnv, true, args)
		exit = r.exit
	case "command":
		showV := false      // -v: name or path
		showVV := false     // -V: "X is a Y" description
		useStdPath := false // -p: search the standard utility path
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-v":
				showV = true
			case "-V":
				showVV = true
			case "-p":
				if r.opts[optRestricted] {
					releaseBgPid(ctx)
					r.errf("%scommand: -p: restricted\n", r.bashErrPrefix(pos))
					exit.code = 1
					return exit
				}
				// bash 5.3 `-p` looks the command up using a default
				// "standard utilities" path, ignoring the caller's
				// $PATH, while still running it with the caller's
				// environment (so $PATH inside the child is unchanged).
				useStdPath = true
			default:
				releaseBgPid(ctx)
				return invalidOpt("command", flag)
			}
		}
		args := fp.args()
		if len(args) == 0 {
			releaseBgPid(ctx)
			break
		}
		// commandLookupEnv resolves command names for this `command`
		// invocation. With -p it overlays the standard utilities path on
		// top of the runner's environment; otherwise it is the runner's
		// environment as-is.
		lookupEnv := expand.Environ(r.writeEnv)
		if useStdPath {
			overlay := newOverlayEnviron(r.writeEnv, false)
			overlay.Set("PATH", expand.Variable{Kind: expand.String, Str: standardUtilsPath})
			lookupEnv = overlay
		}
		if !showV && !showVV {
			if IsBuiltin(args[0]) {
				// command and the process-spawning builtins may still publish
				// the actual background child PID. All other builtin targets
				// are terminal in-process operations.
				switch args[0] {
				case "command", "exec", "nohup", "setsid":
				default:
					releaseBgPid(ctx)
				}
				exit = r.builtin(ctx, pos, args[0], args[1:])
				// The `command` prefix strips a special builtin's POSIX
				// status, including its "exit the shell on a usage/syntax
				// error" behavior. `.`/`source` flag their own open/option
				// failures via exiting; `eval` flags a parse error of its
				// argument the same way (see the eval case above). Clear it
				// so e.g. `command eval '( '` reports the error yet keeps
				// the shell running.
				if args[0] == "." || args[0] == "source" || args[0] == "eval" {
					exit.exiting = false
				}
				return exit
			}
			if useStdPath && !strings.ContainsRune(args[0], '/') {
				// Resolve via the standard path so the child still runs
				// with the caller's $PATH in its environment. The program
				// is launched under its original argv[0] (bash passes the
				// command word, not the resolved path), so $0 inside it
				// is the name as typed, not the absolute path.
				if path, lerr := LookPathDir(r.Dir, lookupEnv, args[0]); lerr == nil {
					orig := args[0]
					args[0] = path
					r.execAs(ctx, pos, orig, false, false, args)
					exit = r.exit
					return exit
				}
			}
			r.exec(ctx, pos, args)
			exit = r.exit
			return exit
		}
		releaseBgPid(ctx)
		// bash 5.3 exits 0 when any of the -v/-V operands resolved and 1 only
		// when none did (POSIX specifies a single command_name; bash's
		// any-found rule governs multiple names, in and out of POSIX mode).
		foundAny := false
		for _, arg := range args {
			if showVV {
				if r.opts[optPosix] && IsBuiltin(arg) && !isPosixSpecialBuiltin(arg) && !r.disabledBuiltins[arg] {
					if path, err := LookPathDir(r.Dir, lookupEnv, arg); err == nil {
						// POSIX `command -v` reports the PATH resolution of a
						// regular builtin (asserted by yash command-p), and -V
						// output must both contain that pathname (command-p
						// "output of describing non-special built-in (-V)")
						// and identify the name as a built-in — yash test-p's
						// preamble probes `command -V test` and SKIPS the whole
						// suite unless the output matches *built[-]in*. Print
						// both facts; -V wording is unspecified by POSIX.
						r.outf("%s is a regular built-in at %s\n", arg, path)
						foundAny = true
						continue
					}
				}
				ms := r.typeMatches(arg, false, lookupEnv, false)
				if len(ms) == 0 {
					r.errf(r.bashErrPrefix(pos)+"command: %s: not found\n", arg)
					continue
				}
				foundAny = true
				if r.opts[optPosix] && ms[0].kind == "file" && strings.ContainsRune(arg, '/') && !filepath.IsAbs(ms[0].path) {
					ms[0].path = r.absPath(ms[0].path)
					ms[0].desc = fmt.Sprintf("%s is %s", arg, ms[0].path)
				}
				r.outf("%s\n", ms[0].desc)
				// bash's `command -V <fn>` also dumps the body
				// for a function match, like `type <fn>`.
				if ms[0].kind == "function" {
					if body := r.Funcs[arg]; body != nil {
						r.printFuncDecl(arg, body)
					}
				}
				continue
			}
			// -v: minimal form. Functions/builtins/keywords print the
			// name; files print the path.
			if r.opts[optPosix] && IsBuiltin(arg) && !isPosixSpecialBuiltin(arg) && !r.disabledBuiltins[arg] {
				if path, err := LookPathDir(r.Dir, lookupEnv, arg); err == nil {
					r.outf("%s\n", path)
				} else {
					r.outf("%s\n", arg)
				}
				foundAny = true
			} else if isKeyword(arg) || r.Funcs[arg] != nil || (IsBuiltin(arg) && !r.disabledBuiltins[arg]) {
				r.outf("%s\n", arg)
				foundAny = true
			} else if als, ok := r.alias[arg]; ok && r.opts[optExpandAliases] {
				r.outf("alias %s='%s'\n", arg, strings.ReplaceAll(aliasValue(als), "'", `'\''`))
				foundAny = true
			} else if m, ok := r.resolvedCommandMatch(arg); ok {
				// The embedder's rung: a path when it has one, else the
				// bare name — the builtin/function shape, since the name
				// runs when invoked.
				if m.path != "" {
					r.outf("%s\n", m.path)
				} else {
					r.outf("%s\n", arg)
				}
				foundAny = true
			} else if path, err := LookPathDir(r.Dir, lookupEnv, arg); err == nil {
				if r.opts[optPosix] && strings.ContainsRune(arg, '/') && !filepath.IsAbs(path) {
					path = r.absPath(path)
				}
				r.outf("%s\n", path)
				foundAny = true
			}
		}
		if !foundAny {
			exit.code = 1
		}
	case "dirs":
		// `dirs +N` / `dirs -N` selects a single entry. `-v` adds
		// "index<TAB>dir" line-per-entry. `-p` is line-per-entry.
		// `-l` prints absolute paths instead of abbreviating HOME.
		// Without flags or index, print the
		// whole stack top-first on one line.
		vertical := false
		perLine := false
		long := false
		idx := -1
		idxSign := byte(0)
		for _, a := range args {
			switch {
			case a == "--":
				args = nil
			case a == "-v":
				vertical = true
				perLine = true
			case a == "-p":
				perLine = true
			case a == "-c":
				r.dirStack = append(r.dirStack[:0], r.logicalDir())
				return exit
			case a == "-l":
				long = true
			case strings.HasPrefix(a, "+") || strings.HasPrefix(a, "-"):
				n, err := strconv.Atoi(a[1:])
				if err != nil {
					r.errf("%sdirs: %s: invalid number\n",
						r.bashErrPrefix(r.curStmtPos), a)
					r.errf("dirs: usage: dirs [-clpv] [+N] [-N]\n")
					exit.code = 1
					return exit
				}
				idx = n
				idxSign = a[0]
			default:
				r.errf("%sdirs: %s: invalid option\n",
					r.bashErrPrefix(r.curStmtPos), a)
				r.errf("dirs: usage: dirs [-clpv] [+N] [-N]\n")
				exit.code = 1
				return exit
			}
		}
		topFirst := make([]string, len(r.dirStack))
		for i, d := range r.dirStack {
			if !long {
				home := r.envGet("HOME")
				if home != "" && (d == home || strings.HasPrefix(d, home+"/")) {
					d = "~" + strings.TrimPrefix(d, home)
				}
			}
			topFirst[len(r.dirStack)-1-i] = d
		}
		if idx >= 0 {
			n := idx
			if idxSign == '-' {
				n = len(topFirst) - 1 - n
			}
			if n < 0 || n >= len(topFirst) {
				return failf(2, "dirs: %d: directory stack index out of range\n", idx)
			}
			if vertical {
				r.outf("%2d  %s\n", n, topFirst[n])
			} else {
				r.outf("%s\n", topFirst[n])
			}
			break
		}
		if perLine {
			for i, d := range topFirst {
				if vertical {
					r.outf("%2d  %s\n", i, d)
				} else {
					r.outf("%s\n", d)
				}
			}
			break
		}
		for i, dir := range topFirst {
			if i > 0 {
				r.out(" ")
			}
			r.outf("%s", dir)
		}
		r.out("\n")
	case "pushd":
		change := true
		if len(args) > 0 && args[0] == "-n" {
			change = false
			args = args[1:]
		}
		if len(args) > 0 && args[0] == "--" {
			args = nil
		}
		swap := func() string {
			oldtop := r.dirStack[len(r.dirStack)-1]
			top := r.dirStack[len(r.dirStack)-2]
			r.dirStack[len(r.dirStack)-1] = top
			r.dirStack[len(r.dirStack)-2] = oldtop
			return top
		}
		switch len(args) {
		case 0:
			if !change {
				break
			}
			if len(r.dirStack) < 2 {
				return failf(1, "pushd: no other directory\n")
			}
			newtop := swap()
			if code := r.changeDir(ctx, "pushd", newtop); code != 0 {
				exit.code = code
				return exit
			}
			r.builtin(ctx, syntax.Pos{}, "dirs", nil)
		case 1:
			// bash's pushd treats `+N` / `-N` arguments as a
			// stack-index rotation; reject other `-…` / `+…`
			// forms as an "invalid number" usage error before
			// trying to change directories.
			arg := args[0]
			if len(arg) > 1 && (arg[0] == '+' || arg[0] == '-') {
				idx, err := strconv.Atoi(arg[1:])
				if err != nil {
					r.errf("%spushd: %s: invalid number\n",
						r.bashErrPrefix(r.curStmtPos), arg)
					r.errf("pushd: usage: pushd [-n] [+N | -N | dir]\n")
					exit.code = 1
					return exit
				}
				topFirst := dirStackTopFirst(r.dirStack)
				n := dirStackIndex(len(topFirst), arg[0], idx)
				if n < 0 || n >= len(topFirst) {
					return failf(1, "pushd: %s: directory stack index out of range\n", arg)
				}
				oldStack := slices.Clone(r.dirStack)
				rotated := append(append([]string(nil), topFirst[n:]...), topFirst[:n]...)
				setDirStackTopFirst(r, rotated)
				if change {
					if code := r.changeDir(ctx, "pushd", rotated[0]); code != 0 {
						r.dirStack = oldStack
						exit.code = code
						return exit
					}
					r.builtin(ctx, syntax.Pos{}, "dirs", nil)
				}
				return exit
			}
			if change {
				// Push a new top slot first so that changeDir's
				// "keep dirStack top in sync with r.Dir" update
				// targets the new slot rather than overwriting the
				// previous top.
				r.dirStack = append(r.dirStack, "")
				if code := r.changeDir(ctx, "pushd", arg); code != 0 {
					r.dirStack = r.dirStack[:len(r.dirStack)-1]
					exit.code = code
					return exit
				}
			} else {
				r.dirStack = append(r.dirStack, arg)
				swap()
			}
			r.builtin(ctx, syntax.Pos{}, "dirs", nil)
		default:
			return failf(2, "pushd: too many arguments\n")
		}
	case "popd":
		change := true
		if len(args) > 0 && args[0] == "-n" {
			change = false
			args = args[1:]
		}
		if len(args) > 0 && args[0] == "--" {
			args = nil
		}
		switch len(args) {
		case 0:
			if len(r.dirStack) < 2 {
				return failf(1, "popd: directory stack empty\n")
			}
			oldtop := r.dirStack[len(r.dirStack)-1]
			r.dirStack = r.dirStack[:len(r.dirStack)-1]
			if change {
				newtop := r.dirStack[len(r.dirStack)-1]
				if code := r.changeDir(ctx, "popd", newtop); code != 0 {
					exit.code = code
					return exit
				}
			} else {
				r.dirStack[len(r.dirStack)-1] = oldtop
			}
			r.builtin(ctx, syntax.Pos{}, "dirs", nil)
		default:
			// `popd +N` / `popd -N` are stack-index ops in
			// bash; other `-…` / `+…` args are an "invalid
			// number" usage error.
			arg := args[0]
			if len(arg) > 1 && (arg[0] == '+' || arg[0] == '-') {
				idx, err := strconv.Atoi(arg[1:])
				if err != nil {
					r.errf("%spopd: %s: invalid number\n",
						r.bashErrPrefix(r.curStmtPos), arg)
					r.errf("popd: usage: popd [-n] [+N | -N]\n")
					exit.code = 1
					return exit
				}
				topFirst := dirStackTopFirst(r.dirStack)
				n := dirStackIndex(len(topFirst), arg[0], idx)
				if n < 0 || n >= len(topFirst) {
					return failf(1, "popd: %s: directory stack index out of range\n", arg)
				}
				if len(topFirst) < 2 {
					return failf(1, "popd: directory stack empty\n")
				}
				oldStack := slices.Clone(r.dirStack)
				newTopFirst := append(append([]string(nil), topFirst[:n]...), topFirst[n+1:]...)
				setDirStackTopFirst(r, newTopFirst)
				if change && n == 0 {
					if code := r.changeDir(ctx, "popd", newTopFirst[0]); code != 0 {
						r.dirStack = oldStack
						exit.code = code
						return exit
					}
				}
				r.builtin(ctx, syntax.Pos{}, "dirs", nil)
				return exit
			}
			r.errf("%spopd: %s: invalid argument\n", r.bashErrPrefix(r.curStmtPos), arg)
			r.errf("popd: usage: popd [-n] [+N | -N]\n")
			exit.code = 2
			return exit
		}
	case "return":
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) > 1 {
			msg := r.bashErrPrefix(pos) + "return: too many arguments"
			r.errf("%s\n", msg)
			r.reportError("builtin", pos, name, msg, 2)
			exit.code = 2
			if r.inFunc || r.inSource {
				exit.returning = true
			}
			return exit
		}
		if !r.inFunc && !r.inSource {
			return failf(2, "return: can only `return' from a function or sourced script\n")
		}
		switch len(args) {
		case 0:
			// `return` with no argument returns the exit status of the
			// last command executed in the function/sourced script.
			// POSIX interp 1602: in the trap action ITSELF it instead yields
			// the $? in effect when the trap was invoked.
			//
			// The depth test must be `==`, not `>=`: the special status applies
			// only at the trap action's own frame, NOT inside functions the
			// action calls. Verified against bash 5.3 (both default and posix):
			//
			//	fn() { true; return; }
			//	trap 'fn; echo trapped $?' USR1   # (exit 19) beforehand
			//	=> "trapped 0"   -- fn's bare return yields true's 0, not 19
			//
			//	check() { false; return; }
			//	handle() { check && echo B || echo A; }
			//	trap handle USR1                  # bash-5.3 tests/trap9.sub
			//	=> "A"           -- check's bare return yields false's 1
			//
			// Widening this to `>=` makes every function called during a trap
			// action return the trap's saved status, which fails trap9.sub.
			//
			// Under strictPosix (yash return-p:148) the widening IS the
			// specified behavior: a bare `return` anywhere during a trap
			// action — including inside functions the action calls — yields
			// the $? from just before the action began. trapCallback-driven
			// actions (EXIT/ERR/DEBUG/RETURN) record that status in
			// trapEntryExit; signal actions record it in signalTrapExit.
			switch {
			case r.inSignalTrap && (len(r.callStack) == r.signalTrapDepth || r.strictPosix):
				exit.code = r.signalTrapExit
			case r.strictPosix && r.handlingTrap:
				exit.code = r.trapEntryExit.code
			default:
				exit.code = r.lastExit.code
			}
		case 1:
			n, err := strconv.Atoi(args[0])
			if err != nil {
				msg := r.bashErrPrefix(pos) + fmt.Sprintf("return: %s: numeric argument required", args[0])
				r.errf("%s\n", msg)
				r.reportError("builtin", pos, name, msg, 2)
				exit.code = 2
				exit.returning = true
				return exit
			}
			exit.code = uint8(n)
		}
		exit.returning = true
	case "read":
		var prompt string
		raw := false
		silent := false
		readline := false
		readArray := false
		var timeout time.Duration
		timeoutSpecified := false
		var timeoutSpec string
		invalidTimeoutStatus := uint8(0)
		nchars := 0
		ncharsSpecified := false
		// nstrict tracks `-N`: read exactly that many bytes, ignoring
		// the delimiter and skipping the IFS split step (the buffer
		// becomes one verbatim field).
		nstrict := false
		delim := "\n"
		// readFD: -u <N>. -1 means "use r.stdin" (the default).
		readFD := -1
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-s":
				silent = true
			case "-r":
				raw = true
			case "-a":
				readArray = true
			case "-p":
				prompt = fp.value()
				if prompt == "" {
					return failf(2, "read: -p: option requires an argument\n")
				}
			case "-t":
				timeoutSpecified = true
				val := fp.value()
				if val == "" {
					return failf(2, "read: -t: option requires an argument\n")
				}
				secs, err := strconv.ParseFloat(val, 64)
				if err != nil {
					timeoutSpec = val
					invalidTimeoutStatus = 1
					break
				}
				if secs < 0 {
					timeoutSpec = val
					invalidTimeoutStatus = 2
					break
				}
				timeout = time.Duration(secs * float64(time.Second))
				if secs > 0 && timeout == 0 {
					timeout = time.Nanosecond
				}
			case "-n", "-N":
				ncharsSpecified = true
				val := fp.value()
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					return failf(2, "read: %s: invalid number\n", val)
				}
				nchars = n
				nstrict = flag == "-N"
			case "-d":
				d := fp.value()
				if d == "" {
					delim = "\x00"
				} else {
					delim = d[:1]
				}
			case "-e", "-i":
				// -e (readline) and -i (initial text) require readline integration.
				// Accept but ignore for now.
				readline = true
				if flag == "-i" {
					fp.value() // consume the argument
				}
			case "-u":
				val := fp.value()
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					return failf(2, "read: %s: invalid file descriptor specification\n", val)
				}
				readFD = n
			default:
				return invalidOpt("read", flag)
			}
		}

		args := fp.args()
		for _, name := range args {
			targetBase := name
			if b, _, ok := splitArrayRef(name); ok {
				targetBase = b
			}
			if !r.builtinAssignNameValid(name, r.builtinTargetQuoted(pos, targetBase)) {
				return failf(1, "read: `%s': not a valid identifier\n", name)
			}
		}

		// `read -a` requires an indexed array; bash refuses to read into
		// an existing associative array.
		if readArray {
			arrayName := shellReplyVar
			if len(args) > 0 {
				arrayName = args[0]
			}
			if vr := r.lookupVar(arrayName); vr.Kind == expand.NameRef {
				if _, _, ok := splitArrayRef(vr.Str); !ok {
					arrayName = vr.Str
				}
			}
			if vr := r.lookupVar(arrayName); vr.Kind == expand.Associative {
				return failf(2, "read: %s: not an indexed array\n", arrayName)
			}
		}

		// Resolve the reader: `-u N` opens fd N from the runner's
		// fd table; otherwise we keep r.stdin. Swap r.stdin so
		// readLine sees the requested source.
		var savedStdin *os.File
		stdinSwapped := false
		if readFD >= 0 {
			switch {
			case readFD == 1, readFD == 2:
				return failf(2, "read: %d: invalid file descriptor: not open for reading\n", readFD)
			case readFD == 0:
				// `-u 0` is just stdin — no swap needed.
			default:
				f, ok := r.fdTable[readFD]
				if !ok && !r.fdClosedTable[readFD] {
					f, ok = r.inheritedFd(readFD)
				}
				if !ok {
					return failf(2, "read: %d: invalid file descriptor: Bad file descriptor\n", readFD)
				}
				savedStdin = r.stdin
				r.stdin = f
				stdinSwapped = true
			}
		}
		defer func() {
			if stdinSwapped {
				r.stdin = savedStdin
			}
		}()

		readCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			readCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		stdin := r.stdin
		var input io.Reader = stdin
		if !stdinSwapped {
			if src := r.scriptStdinReader(); src != nil {
				input = src
			}
		}
		clearReadVars := func() {
			if readArray {
				arrayName := shellReplyVar
				if len(args) > 0 {
					arrayName = args[0]
				}
				r.setVar(arrayName, expand.Variable{
					Set:  true,
					Kind: expand.Indexed,
				})
				return
			}
			for _, name := range args {
				r.setVarString(name, "")
			}
		}
		if invalidTimeoutStatus != 0 {
			r.errf("%sread: %s: invalid timeout specification\n",
				r.bashErrPrefix(r.curStmtPos), timeoutSpec)
			exit.code = 1
			return exit
		}
		// isTerminalFile, not stdin.Fd(): probing with Fd would detach stdin
		// from the runtime poller and break the SetReadDeadline this same
		// builtin relies on for -t and for context cancellation.
		if prompt != "" && input == stdin && isTerminalFile(stdin) {
			r.out(prompt)
		}
		readInput := func() ([]byte, error) {
			var line []byte
			if nchars > 0 {
				buf := make([]byte, nchars)
				if nstrict {
					// `-N`: read EXACTLY nchars bytes, never honoring the
					// delimiter.
					n, readErr := io.ReadFull(input, buf)
					line = buf[:n]
					if readErr == io.ErrUnexpectedEOF {
						readErr = io.EOF
					}
					return line, readErr
				}

				// `-n`: read up to nchars characters, stopping early at
				// the delimiter. Bash drops the delimiter byte from the
				// result, so read one byte at a time but count complete
				// UTF-8 runes.
				one := make([]byte, 1)
				delimByte := byte('\n')
				if len(delim) > 0 {
					delimByte = delim[0]
				}
				for chars := 0; chars < nchars; {
					n, readErr := input.Read(one)
					if n > 0 {
						if one[0] == delimByte {
							break
						}
						line = append(line, one[0])
						start := len(line) - 1
						for start > 0 && (line[start]&0xc0) == 0x80 {
							start--
						}
						cur := line[start:]
						if utf8.FullRune(cur) {
							chars++
						} else if len(cur) >= utf8.UTFMax {
							// Malformed input should still make progress.
							chars++
						}
					}
					if readErr != nil {
						return line, readErr
					}
				}
				return line, nil
			}
			if silent {
				// Note that on Windows, syscall.Stdin is of type uintptr.
				return term.ReadPassword(int(syscall.Stdin))
			}
			delimByte := byte('\n')
			if len(delim) > 0 {
				delimByte = delim[0]
			}
			return r.readLineFrom(readCtx, input, raw, delimByte)
		}
		isReadTimeout := func(err error) bool {
			return readErrorIsTimeout(readCtx, err, timeout)
		}
		var line []byte
		var err error
		// A trapped signal may arrive while read is blocked. Bash runs the
		// trap immediately and, when the handler completes normally, resumes
		// the same read rather than returning a failed builtin. This matters
		// for callers which arrange input only after delivering the signal
		// (the POSIX conformance shape). Keep the retry local to read: wait(1)
		// and other interruptible builtins have their own status contract.
		readThroughSignals := func() ([]byte, error) {
			var accumulated []byte
			for {
				part, readErr := readInput()
				accumulated = append(accumulated, part...)
				if !errors.Is(readErr, errReadInterrupted) {
					return accumulated, readErr
				}
				r.deliverPendingSignals(ctx)
				if r.exit.returning || r.exit.exiting || r.exit.fatalExit ||
					r.breakEnclosing > 0 || r.contnEnclosing > 0 {
					return accumulated, errReadInterrupted
				}
			}
		}
		zeroCharRead := ncharsSpecified && nchars == 0
		if timeoutSpecified && timeout == 0 {
			ready := false
			switch input := input.(type) {
			case *scriptStdinReader:
				ready = true
			case *os.File:
				if info, statErr := input.Stat(); statErr == nil && info.Mode().IsRegular() {
					ready = true
				} else {
					ready = taskReadReadyNow(input)
				}
			}
			if !ready {
				exit.code = 1
			}
			return exit
		}
		if zeroCharRead {
			// Bash treats -n 0 and -N 0 as successful, non-consuming reads
			// which still perform the normal empty assignment below.
		} else if r.inBashPPTask() {
			if silent {
				return failf(2, "read: silent terminal input is unavailable inside a Bash++ task\n")
			}
			if r.origStdinAsyncCopy && !stdinSwapped && !r.stdinRedirected && input == stdin {
				return failf(2, "read: non-cooperative input is unavailable inside a Bash++ task\n")
			}
			switch inputSource := input.(type) {
			case *scriptStdinReader:
				// The script tail is immutable memory and cannot block.
			case *os.File:
				info, statErr := inputSource.Stat()
				if statErr != nil {
					return failf(2, "read: unable to inspect task input: %v\n", statErr)
				}
				if !info.Mode().IsRegular() {
					pollableMode := info.Mode()&os.ModeNamedPipe != 0 ||
						info.Mode()&os.ModeCharDevice != 0 || info.Mode()&os.ModeSocket != 0
					if !pollableMode {
						return failf(2, "read: non-pollable input is unavailable inside a Bash++ task\n")
					}
					var deadline time.Time
					if timeout > 0 {
						deadline = time.Now().Add(timeout)
					}
					input = taskReadReader(r.bashPPTaskContext(ctx), inputSource, deadline, r.signalWakeChan(), func() bool {
						return r.bashPPArmBeforeBlock(ctx)
					})
					if input == nil {
						return failf(2, "read: blocking input is unavailable inside a Bash++ task\n")
					}
				}
			default:
				return failf(2, "read: non-cooperative input is unavailable inside a Bash++ task\n")
			}
			line, err = readThroughSignals()
			if taskErr := r.bashPPTaskContext(ctx).Err(); taskErr != nil {
				r.bashPPTaskCanceled = true
				r.exit.fatal(taskErr)
				return r.exit
			}
		} else if timeout > 0 {
			if r.stdinTTYFallback || r.stdinDevTTY {
				clearReadVars()
				exit.code = 142
				return exit
			}
			deadline := time.Now().Add(timeout)
			cancelGrace := func() {}
			if input == stdin && stdin != nil && timeout < 10*time.Millisecond && fdReadableNow(stdin) {
				deadline = time.Now().Add(10 * time.Millisecond)
				var cancel context.CancelFunc
				readCtx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				cancelGrace = cancel
			}
			// For an explicit `read -u <fd>` the fd may be a fifo/pipe whose
			// SetReadDeadline silently no-ops on fds not registered with the
			// runtime poller (e.g. `read -u 9 -t` on an `exec 9<>p` fifo would
			// block forever on linux). Use the poll-based reader there. On
			// Windows, fd0 redirects such as here-strings and pipeline input
			// also use anonymous pipe handles outside Go's runtime poller, so
			// route them through the readiness reader too. Its poll-before-read
			// shape mirrors Cygwin: buffered input wins over an expired
			// deadline, and an empty pipe times out cleanly.
			var fdReader io.Reader
			if f, ok := input.(*os.File); ok && f != nil && (stdinSwapped || runtime.GOOS == "windows") {
				fdReader = timeoutReader(readCtx, f, deadline)
			}
			if fdReader != nil {
				input = fdReader
				if !r.bashPPArmBeforeBlock(ctx) {
					return r.exit
				}
				line, err = readThroughSignals()
				cancelGrace()
			} else if input == stdin && stdin != nil && stdin.SetReadDeadline(deadline) == nil {
				if !r.bashPPArmBeforeBlock(ctx) {
					return r.exit
				}
				line, err = readThroughSignals()
				stdin.SetReadDeadline(time.Time{})
				cancelGrace()
			} else {
				if input == stdin && stdin != nil {
					input = &timeoutFileReader{ctx: readCtx, file: stdin, deadline: deadline}
				}
				if !r.bashPPArmBeforeBlock(ctx) {
					return r.exit
				}
				line, err = readThroughSignals()
				cancelGrace()
			}
		} else {
			if input == stdin && stdin != nil {
				if wake := r.signalWakeChan(); wake != nil {
					if fdReader := signalReader(readCtx, stdin, wake); fdReader != nil {
						input = fdReader
					}
				}
			}
			if !r.bashPPArmBeforeBlock(ctx) {
				return r.exit
			}
			line, err = readThroughSignals()
		}
		if errors.Is(err, errReadInterrupted) {
			clearReadVars()
			exit.code = 1
			return exit
		}
		if isReadTimeout(err) {
			clearReadVars()
			exit.code = 142
			return exit
		}
		// readLine already stops at the configured delimiter and
		// discards it; nothing left to trim here.
		_ = delim
		if readArray {
			// read -a arrayname: split line into fields and assign to indexed array.
			arrayName := shellReplyVar
			if len(args) > 0 {
				arrayName = args[0]
			}
			if vr := r.lookupVar(arrayName); vr.Kind == expand.NameRef {
				if _, _, ok := splitArrayRef(vr.Str); ok {
					return failf(1, "read: `%s': not a valid identifier\n", vr.Str)
				}
				arrayName = vr.Str
			}
			// Use -1 as max to get all fields without joining the last ones.
			values := expand.ReadFields(r.ecfg, string(line), -1, raw)
			r.setVar(arrayName, expand.Variable{
				Set:  true,
				Kind: expand.Indexed,
				List: values,
			})
		} else {
			if len(args) == 0 {
				// `read` (no NAMEs) assigns the raw line to
				// REPLY with leading/trailing IFS whitespace
				// preserved — bash's "otherwise unmodified"
				// behaviour. The trimming/splitting only kicks
				// in with explicit variable names.
				r.setVarString(shellReplyVar, string(line))
			} else if nstrict {
				// `-N` assigns the raw buffer to the first variable
				// and clears the rest; no field splitting per bash.
				r.setVarString(args[0], string(line))
				for _, name := range args[1:] {
					r.setVarString(name, "")
				}
			} else {
				values := expand.ReadFields(r.ecfg, string(line), len(args), raw)
				readonlyFailed := false
				for i, name := range args {
					val := ""
					if i < len(values) {
						val = values[i]
					}
					lookupName := name
					if base, _, ok := splitArrayRef(name); ok {
						lookupName = base
					}
					if r.lookupVar(lookupName).ReadOnly {
						// bash: abort the read with exit 2 on
						// readonly-assignment failure. setVarString
						// will surface the diagnostic itself.
						r.setVarString(name, val)
						readonlyFailed = true
						break
					}
					r.setVarString(name, val)
				}
				if readonlyFailed {
					exit.code = 2
					return exit
				}
			}
		}

		// We can get data back from readLine and an error at the same time, so
		// check err after we process the data. For EOF with no data,
		// bash still clears the named variables to empty before
		// returning the non-zero exit status.
		if err != nil {
			if timeout > 0 && errors.Is(readCtx.Err(), context.DeadlineExceeded) {
				exit.code = 142
				return exit
			}
			if readline && timeout > 0 && len(line) == 0 {
				exit.code = 142
				return exit
			}
			if len(line) == 0 && !readArray && len(args) > 0 {
				for _, name := range args {
					r.setVarString(name, "")
				}
			}
			exit.code = 1
			return exit
		}

	case "getopts":
		// Like other shell builtins, getopts accepts -- to end its own
		// option parsing before its optstring. Without it, a normal
		// optstring such as "a" was incorrectly diagnosed as an option.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		// bash rejects any leading `-X` flag with an "invalid option"
		// diagnostic, even though our own optstring may legitimately
		// start with `-` (no, it can't — only `:` is special in bash).
		if len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' && args[0][1] != ':' {
			r.errf("%s%s: %s: invalid option\n",
				r.bashErrPrefix(r.curStmtPos), "getopts", args[0])
			r.errf("getopts: usage: getopts optstring name [arg ...]\n")
			exit.code = 2
			return exit
		}
		if len(args) < 2 {
			// bash 5.3 emits the usage line without the
			// `<file>: line N: ` prefix that other builtin
			// errors carry.
			r.errf("getopts: usage: getopts optstring name [arg ...]\n")
			exit.code = 2
			return exit
		}
		optind, _ := strconv.Atoi(r.envGet("OPTIND"))
		if optind-1 != r.optState.argidx {
			if optind < 1 {
				optind = 1
			}
			r.optState = getopts{argidx: optind - 1}
		}
		optstr := args[0]
		name := args[1]
		invalidName := !syntax.ValidName(name)
		args = args[2:]
		if len(args) == 0 {
			args = r.Params
		}
		// Invalid identifier: bash still advances getopts' internal
		// state (so the caller's subsequent `shift $((OPTIND-1))`
		// works) but refuses the assignment and returns rc=1.
		if invalidName {
			_, optarg, _ := r.optState.next(optstr, args)
			if optarg != "" {
				r.setVarString("OPTARG", optarg)
			}
			r.setVarString("OPTIND", strconv.Itoa(r.optState.argidx+1))
			return failf(1, "getopts: `%s': not a valid identifier\n", name)
		}
		// Diagnostics fire unless the optstring starts with ':' (silent
		// mode) or the caller sets OPTERR=0 — the latter being bash's
		// runtime escape hatch when the optstring is hard-coded.
		diagnostics := !strings.HasPrefix(optstr, ":")
		if opterr, err := strconv.Atoi(r.envGet("OPTERR")); err == nil && opterr == 0 {
			diagnostics = false
		}

		opt, optarg, done := r.optState.next(optstr, args)

		// Storing the option character into an empty-target nameref
		// retargets it; a non-identifier result like `?` is rejected.
		// Bash reports this after the regular `illegal option`
		// diagnostic, so defer the error past the switch below.
		// A missing required argument signals as ':' internally. Only in
		// SILENT mode (optstring starting ':') does bash store ':' (with
		// OPTARG set to the option char); in verbose mode it stores '?'
		// (with OPTARG unset) and prints a diagnostic. opt itself stays ':'
		// below so the right diagnostic is chosen.
		storeOpt := opt
		if opt == ':' && diagnostics {
			storeOpt = '?'
		}
		nameVar := r.lookupVar(name)
		nameRefBadTarget := nameVar.Kind == expand.NameRef && nameVar.Str == "" &&
			!validNameRefTarget(string(storeOpt))
		if !nameRefBadTarget {
			r.setVarString(name, string(storeOpt))
		}
		// bash's getopts only surfaces the OPTARG-readonly diagnostic
		// when it would have written to OPTARG (i.e. the default branch
		// below): the unset that happens at the top of every getopts
		// call is silent on readonly.
		optargRO := r.lookupVar("OPTARG").ReadOnly
		if !optargRO {
			r.delVar("OPTARG")
		}
		// bash prefixes diagnostics with $0 (the script name) and prints
		// the offending character unquoted (e.g. `./script.sh: illegal
		// option -- c`).
		scriptName := r.argv0
		if scriptName == "" {
			scriptName = r.filename
		}
		if scriptName == "" {
			scriptName = "bash"
		}
		switch {
		case opt == '?' && diagnostics && !done:
			r.errf("%s: illegal option -- %s\n", scriptName, optarg)
		case opt == ':' && diagnostics:
			r.errf("%s: option requires an argument -- %s\n", scriptName, optarg)
		default:
			if optarg != "" {
				r.setVarString("OPTARG", optarg)
			}
		}
		r.setVarString("OPTIND", strconv.FormatInt(int64(r.optState.argidx+1), 10))
		// Bash reports a readonly OPTARG assignment but getopts still
		// succeeds when it parsed an option.
		if nameRefBadTarget {
			r.errf("%sgetopts: `%s': not a valid identifier\n", r.bashErrPrefix(r.curStmtPos), string(storeOpt))
		}

		exit.oneIf(done)

	case "shopt":
		if len(args) >= 2 && (args[0] == "--set" || args[0] == "--unset") {
			allStrictArray := true
			for _, arg := range args[1:] {
				if arg != "strict_array" {
					allStrictArray = false
					break
				}
			}
			if allStrictArray {
				break
			}
		}
		bareEndOfOptions := len(args) == 1 && args[0] == "--"
		mode := ""
		sawSet := false
		sawUnset := false
		posixOpts := false
		printReusable := false
		quiet := false
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-s":
				sawSet = true
				mode = flag
			case "-u":
				sawUnset = true
				mode = flag
			case "-o":
				posixOpts = true
			case "-p":
				// bash's `shopt -p` lists every option in
				// `shopt -s NAME` / `shopt -u NAME` form.
				printReusable = true
			case "-q":
				// `shopt -q NAME`: return 0 if set, 1 if not,
				// no output. Inline below in the per-arg loop.
				quiet = true
			default:
				return invalidOpt("shopt", flag)
			}
		}
		if sawSet && sawUnset {
			r.errf("%sshopt: cannot set and unset shell options simultaneously\n", r.bashErrPrefix(pos))
			exit.code = 1
			return exit
		}
		args := fp.args()
		if bareEndOfOptions && len(args) == 0 {
			break
		}
		// Emit a line as either `shopt -s NAME` / `shopt -u NAME`
		// (or `set -o`/`set +o` when -o is in play) for the
		// printReusable form, or `name<TAB>on/off` otherwise.
		emitOpt := func(name string, enabled, supported bool) {
			if printReusable {
				if posixOpts {
					flag := "+o"
					if enabled {
						flag = "-o"
					}
					r.outf("set %s %s\n", flag, name)
				} else {
					flag := "-u"
					if enabled {
						flag = "-s"
					}
					r.outf("shopt %s %s\n", flag, name)
				}
				return
			}
			width := 20 // bash 5.3 pads `shopt` names to 20 before the tab
			if posixOpts && len(args) == 0 {
				width = 15 // ...but `shopt -o` listings pad to 15
			}
			r.printOptLineWidth(name, enabled, supported, width)
		}
		if len(args) == 0 {
			// When combined with `-p`, `shopt -s` lists only set
			// options and `shopt -u` lists only unset ones.
			// Without `-p`, the same form just lists everything
			// in default bash format.
			showSet := mode == "" || mode == "-s"
			showUnset := mode == "" || mode == "-u"
			if posixOpts {
				// Build the combined list: real POSIX options
				// from posixOptsTable, plus all no-op aliases
				// (history, hashall, etc.) so `set -o` matches
				// bash's output. Sort alphabetically.
				type oentry struct {
					name    string
					enabled bool
				}
				var list []oentry
				for i, opt := range &posixOptsTable {
					if opt.name == "restricted" {
						continue
					}
					list = append(list, oentry{opt.name, r.opts[i]})
				}
				for name, on := range noOpSetOptions {
					if v, ok := r.noOpSetState[name]; ok {
						on = v
					}
					list = append(list, oentry{name, on})
				}
				if r.bashPPEnabled() && !r.hideBashPPOption {
					list = append(list, oentry{"bashpp", true})
				}
				sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
				for _, e := range list {
					if (e.enabled && !showSet) || (!e.enabled && !showUnset) {
						continue
					}
					emitOpt(e.name, e.enabled, true)
				}
				break
			}
			{
				names := make([]string, len(bashOptsTable))
				for i, opt := range bashOptsTable {
					names[i] = opt.name
				}
				idx := make(map[string]int, len(names))
				for i, n := range names {
					idx[n] = i
				}
				sort.Strings(names)
				for _, n := range names {
					i := idx[n]
					enabled := r.opts[len(posixOptsTable)+i]
					if (enabled && !showSet) || (!enabled && !showUnset) {
						continue
					}
					emitOpt(n, enabled, bashOptsTable[i].supported)
				}
			}
			break
		}
		for _, arg := range args {
			opt, supported := (*bool)(nil), true
			if posixOpts {
				if arg == "bashpp" || arg == "bashsharp" {
					if mode == "-s" || mode == "-u" {
						r.setBashPPMode(mode == "-s")
						continue
					}
					state := r.bashPPEnabled()
					if quiet {
						if !state {
							exit.code = 1
						}
						continue
					}
					emitOpt(arg, state, true)
					continue
				}
				opt = r.posixOptByName(arg)
				if opt == nil {
					if defaultOn, ok := noOpSetOptions[arg]; ok {
						// `shopt -so NAME` for an accept-and-ignore
						// option (physical, ignoreeof, etc.): record
						// the toggle so subsequent listings echo it.
						if mode == "-s" || mode == "-u" {
							if r.noOpSetState == nil {
								r.noOpSetState = make(map[string]bool)
							}
							prev := r.noOpSetState[arg]
							r.noOpSetState[arg] = mode == "-s"
							if arg == "history" && prev != (mode == "-s") {
								r.histSetEnabled(mode == "-s", pos)
							}
							if arg == "histexpand" && prev != (mode == "-s") {
								r.histSetExpand(mode == "-s")
							}
							if arg == "ignoreeof" {
								r.setIgnoreEOFOption(mode == "-s")
							}
							continue
						}
						// For `shopt -p -o NAME` / `shopt -q -o NAME`
						// emit/check using the tracked state so the
						// listing form matches bash's output.
						state := defaultOn
						if v, ok := r.noOpSetState[arg]; ok {
							state = v
						}
						if quiet {
							if !state {
								exit.code = 1
							}
							continue
						}
						emitOpt(arg, state, true)
						continue
					}
				}
			} else {
				opt, supported = r.bashOptByName(arg)
			}
			if opt == nil {
				// Bash 5.3 distinguishes the two `shopt` namespaces:
				// `shopt NAME` → "invalid shell option name"
				// `shopt -o NAME` → "invalid option name"
				kind := "invalid shell option name"
				if posixOpts {
					kind = "invalid option name"
				}
				r.errf("%sshopt: %s: %s\n", r.bashErrPrefix(pos), arg, kind)
				exit.code = 1
				return exit
			}

			switch mode {
			case "-s", "-u":
				// bash silently accepts `shopt -s NAME` even for
				// options that don't change anything at runtime
				// (cdspell, checkhash, histappend, etc.). Mirror
				// that — store the bit so subsequent `shopt -p`
				// reflects the user's choice; we just don't do
				// anything with it.
				_ = supported
				*opt = mode == "-s"
				// Some shopts have a mirrored runner-level
				// option (`optExpandAliases` etc.); keep them
				// in sync so the alias / extglob / etc.
				// code paths see the same state.
				switch arg {
				case "expand_aliases":
					r.opts[optExpandAliases] = *opt
				}
			default: // ""
				if !quiet {
					emitOpt(arg, *opt, supported)
				}
				// Bash's `shopt name` returns 0 if set, 1 if not —
				// useful for `if shopt -q name`-style probes
				// (and the open form too).
				if !*opt {
					exit.code = 1
				}
			}
		}
		r.updateExpandOpts()

	case "alias":
		posixFormat := r.opts[optPosix]
		show := func(name string, als alias, forcePrefix bool) {
			// Bash displays alias bodies verbatim (single-quoted),
			// preserving the original text exactly.
			prefix := "alias "
			if posixFormat && !forcePrefix {
				prefix = ""
			}
			// A leading-dash alias name must remain reparsable as output.
			// Bash inserts its option terminator whenever it emits the
			// GNU-style "alias " prefix.
			if prefix != "" && strings.HasPrefix(name, "-") {
				prefix += "-- "
			}
			// Bash quotes alias bodies with sh_single_quote(): wrap in
			// single quotes, escaping any embedded `'` as `'\''` so the
			// printed form re-parses (e.g. `eval alias "$(alias a)"`).
			r.outf("%s%s='%s'\n", prefix, name, strings.ReplaceAll(aliasValue(als), "'", `'\''`))
		}

		// showAll lists every alias sorted by name, matching bash 5.3
		// (which keeps its alias table sorted). Go map iteration order is
		// nondeterministic, so without this the listing flaps run-to-run.
		showAll := func(forcePrefix bool) {
			names := make([]string, 0, len(r.alias))
			for name := range r.alias {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				show(name, r.alias[name], forcePrefix)
			}
		}

		// `alias -p` prints all aliases (same as no args). Parse every
		// option before printing: Bash accepts repeated `-p`, but a later
		// invalid option is still an option (not an alias name), even when
		// it contains `=`. A name beginning with `-` requires `--`.
		filtered := args
		printAll := false
		for len(filtered) > 0 {
			arg := filtered[0]
			if arg == "--" {
				filtered = filtered[1:]
				break
			}
			if len(arg) <= 1 || arg[0] != '-' {
				break
			}
			for i := 1; i < len(arg); i++ {
				if arg[i] == 'p' {
					printAll = true
					continue
				}
				r.errf("%salias: -%c: invalid option\n", r.bashErrPrefix(pos), arg[i])
				r.errf("alias: usage: alias [-p] [name[=value] ... ]\n")
				exit.code = 2
				return exit
			}
			filtered = filtered[1:]
		}
		if printAll {
			showAll(true)
			break
		}
		if len(args) == 0 {
			showAll(false)
		}
		for _, arg := range filtered {
			name, src, ok := strings.Cut(arg, "=")
			if !ok {
				als, ok := r.alias[name]
				if !ok {
					r.errf(r.bashErrPrefix(pos)+"alias: %s: not found\n", name)
					exit.code = 1
					continue
				}
				show(name, als, false)
				continue
			}
			if !validAliasName(name) {
				r.errf("%salias: `%s': invalid alias name\n", r.bashErrPrefix(pos), name)
				exit.code = 1
				continue
			}

			// Bash stores alias bodies as TEXT and only re-parses
			// at expansion time, so things like
			// `alias switch=case` (a body that's a bare keyword) or
			// `alias foo="echo 'Error:"` (with unclosed quotes that
			// continue into the next user input) are legal even
			// though they don't parse standalone. parseAliasBody
			// handles the multi-stmt / per-word / raw fallbacks; the
			// same helper backs BASH_ALIASES[...]= assignments.
			if r.alias == nil {
				r.alias = make(map[string]alias)
			}
			als := parseAliasBody(src)
			als.defLine = r.aliasDefLine(int(pos.Line()))
			r.alias[name] = als
		}
	case "unalias":
		all := false
		for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
			if args[0] == "--" {
				args = args[1:]
				break
			}
			switch args[0] {
			case "-a":
				all = true
				args = args[1:]
			default:
				r.errf("%sunalias: %s: invalid option\n", r.bashErrPrefix(pos), args[0])
				r.errf("unalias: usage: unalias [-a] name [name ...]\n")
				exit.code = 2
				return exit
			}
		}
		if all {
			r.alias = nil
			break
		}
		if len(args) == 0 {
			r.errf("unalias: usage: unalias [-a] name [name ...]\n")
			exit.code = 2
			return exit
		}
		for _, name := range args {
			if _, ok := r.alias[name]; !ok {
				r.errf("%sunalias: %s: not found\n", r.bashErrPrefix(pos), name)
				exit.code = 1
				continue
			}
			delete(r.alias, name)
		}

	case "trap":
		fp := flagParser{remaining: args}
		listSignals := false
		printTraps := false
		printActions := false // -P: print only the action string
		dashReset := false    // a bare "-" action (reset to default)
		callback := "-"
	trapFlags:
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-l":
				listSignals = true
			case "-p":
				printTraps = true
			case "-P":
				printActions = true
			case "-":
				// A bare "-" is the reset-to-default *action*, not a
				// flag; stop flag parsing so it isn't mistaken for the
				// callback of `trap - SIG...`.
				dashReset = true
				break trapFlags
			default:
				r.errf("%strap: %s: invalid option\n", r.bashErrPrefix(pos), flag)
				r.errf("trap: usage: %s\n", bashUsage["trap"])
				exit.code = 2
				return exit
			}
		}
		if listSignals {
			r.printSignalList(r.opts[optPosix])
			break
		}
		if printTraps && printActions {
			return failf(2, "trap: cannot specify both -p and -P\n")
		}
		args := fp.args()
		if printActions {
			// `trap -P SIG...` prints only the action for each named
			// signal (no `trap -- ... SIG` wrapper).
			if len(args) == 0 {
				r.errf("%strap: -P requires at least one signal name\n", r.bashErrPrefix(pos))
				r.errf("trap: usage: %s\n", bashUsage["trap"])
				exit.code = 2
				return exit
			}
			for _, a := range args {
				sig := normalizeSignal(a)
				if sig == "" {
					return failf(1, "trap: %s: invalid signal specification\n", a)
				}
				if cb, ok := r.trapCallbacks[sig]; ok && cb != "" {
					r.outf("%s\n", cb)
				}
			}
			break
		}
		if !dashReset && (printTraps || len(args) == 0) {
			// Print traps, optionally filtered by signal names. When
			// signal operands are given, bash prints them in the order
			// they were supplied on the command line (duplicates included),
			// not in its internal numeric order.
			var filterOrder []string
			for _, a := range args {
				sig := normalizeSignal(a)
				if sig == "" {
					return failf(1, "trap: %s: invalid signal specification\n", a)
				}
				filterOrder = append(filterOrder, sig)
			}
			// bash prints `trap -- 'CMD' SIGNAME` with the body in
			// single-quotes (`'`) and the signal name prefixed with
			// `SIG` for all non-EXIT/non-DEBUG/non-ERR/non-RETURN
			// pseudo-signals. Order matches bash: EXIT first
			// (signal 0), then numeric signals in ascending order,
			// then ERR/DEBUG/RETURN at the end.
			sigPrefix := func(name string) string {
				if r.opts[optPosix] {
					return name
				}
				switch name {
				case "EXIT", "DEBUG", "ERR", "RETURN":
					return name
				default:
					return "SIG" + name
				}
			}
			// In POSIX mode, `trap -p` lists every signal (defaults shown as
			// `trap -- - SIG`); a bare `trap` (no -p) lists only the
			// non-default traps, as does default bash mode.
			fullList := r.opts[optPosix] && printTraps
			var sigKeys []string
			if fullList {
				for _, e := range sortedSignalEntries() {
					sigKeys = append(sigKeys, e.Name)
				}
			} else {
				// Build the sort order: EXIT (signal 0) first, then
				// every trapped real signal in ascending numeric order
				// (so CHLD, WINCH, etc. — not just 1..15 — are listed),
				// then ERR, DEBUG, RETURN.
				for sig := range r.trapCallbacks {
					switch sig {
					case "EXIT", "ERR", "DEBUG", "RETURN":
						continue
					}
					sigKeys = append(sigKeys, sig)
				}
				// Signals that were SIG_IGN at shell startup are listed as
				// `trap -- '' SIG` even though no trap could attach to them
				// (bash showtrap: signal_is_hard_ignored -> empty action).
				for sig := range r.startupIgnored {
					if _, ok := r.trapCallbacks[sig]; !ok {
						sigKeys = append(sigKeys, sig)
					}
				}
				sort.Slice(sigKeys, func(i, j int) bool {
					si, _ := signalByName(sigKeys[i])
					sj, _ := signalByName(sigKeys[j])
					ni, _ := signalNumber(si)
					nj, _ := signalNumber(sj)
					return ni < nj
				})
			}
			// With explicit signal operands, iterate them in the order
			// given (bash showtrap loops over the supplied specs); otherwise
			// use EXIT, ascending real signals, then ERR/DEBUG/RETURN.
			iterOrder := append([]string{"EXIT"}, sigKeys...)
			iterOrder = append(iterOrder, "DEBUG", "ERR", "RETURN")
			if len(filterOrder) > 0 {
				iterOrder = filterOrder
			}
			for _, sig := range iterOrder {
				cb, ok := r.trapCallbacks[sig]
				ignored := r.isStartupIgnored(sig)
				defaulted := fullList
				if !ok && !ignored && !defaulted {
					continue
				}
				// A hard-ignored signal always prints an empty action,
				// regardless of any stale callback string.
				quoted := "''"
				if !ok && !ignored {
					quoted = "-"
				} else if !ignored {
					quoted = "'" + strings.ReplaceAll(cb, "'", `'\''`) + "'"
				}
				r.outf("trap -- %s %s\n", quoted, sigPrefix(sig))
			}
			break
		}
		if len(args) == 1 && args[0] == "" && !dashReset {
			// `trap ''` with no signals is a no-op in bash (empty action,
			// nothing to attach it to).
			break
		}
		unsignedDecimal := func(s string) bool {
			if s == "" {
				return false
			}
			for _, c := range s {
				if c < '0' || c > '9' {
					return false
				}
			}
			return true
		}
		reset := false
		switch {
		case dashReset:
			// `trap - SIG...`: the "-" action was already consumed; the
			// remaining args are all signal names to reset.
			reset = true
		case unsignedDecimal(args[0]) && normalizeSignal(args[0]) != "":
			// POSIX: a numeric first operand that names a valid signal means
			// every operand is a condition to reset to default — `trap 2 QUIT`
			// resets signals 2 and QUIT, it does not install "2" as the action
			// for QUIT. bash applies this in both POSIX and default modes. A
			// numeric operand that is *not* a valid signal (`trap 512 QUIT`) is
			// still an action, so it falls through to the default case.
			reset = true
		case len(args) == 1:
			// 1-arg form resets the named signal to default, but only
			// if the operand actually names a signal. A lone operand
			// that isn't a signal is an action with no signal_spec,
			// which bash reports as a usage error (`trap 512`).
			if normalizeSignal(args[0]) == "" {
				r.errf("trap: usage: %s\n", bashUsage["trap"])
				exit.code = 2
				return exit
			}
			reset = true
		default:
			callback = args[0]
			args = args[1:]
		}
		// `-` is the explicit "reset to default" sentinel; an
		// empty string means "ignore the signal" (a distinct state
		// that `trap -p` prints as `trap -- '' SIG`).
		if callback == "-" {
			reset = true
		}
		for _, arg := range args {
			sig := normalizeSignal(arg)
			if sig == "" {
				return failf(2, "trap: %s: invalid signal specification\n", arg)
			}
			// A signal ignored on entry to the shell cannot be trapped or
			// reset; bash silently ignores the request (trap.c set_signal:
			// SIG_HARD_IGNORE), leaving it listed as `trap -- '' SIG`.
			if r.isStartupIgnored(sig) {
				continue
			}
			if reset {
				r.removeTrapCallback(sig)
				if sig == "EXIT" {
					r.inheritedExitTrap = false
				}
				r.disableSignalTrap(sig)
				if sig == "CHLD" {
					r.chldTrapActive.Store(false)
				}
			} else {
				r.setTrapCallback(sig, callback)
				if sig == "EXIT" {
					r.inheritedExitTrap = false
				}
				if sig == "CHLD" {
					// SIGCHLD is reap-driven: a non-empty action fires once
					// per reaped child; an empty action ("ignore") suppresses.
					r.chldTrapActive.Store(callback != "")
				}
				// A DEBUG trap set inside a function takes effect for the
				// remainder of that function's commands, even when the
				// frame was not otherwise traced (bash trap.tests: the
				// `func[29] funcdebug` line).
				if sig == "DEBUG" && callback != "" {
					if n := len(r.callStack); n > 0 {
						r.callStack[n-1].debugTrace = true
					}
				}
				// Adjust the OS disposition so the signal reaches this
				// runner instead of taking its default action (which would
				// kill the process). An empty callback means "ignore": set a
				// real SIG_IGN so an exec'd child inherits it as ignored,
				// matching bash. A non-empty callback is notified so the
				// trap handler can run.
				if callback == "" {
					r.ignoreSignalTrap(sig)
				} else {
					r.enableSignalTrap(sig)
				}
			}
		}

	case "readarray", "mapfile":
		dropDelim := false
		delim := "\n"
		// -O origin: index to start writing at (default 0).
		// -s skip:   discard the first `skip` lines from the input.
		// -n max:    copy at most `max` lines (0 = no limit).
		// -c quant:  invoke -C callback every `quant` lines (default 5000).
		// -C cb:     shell code run as `cb INDEX LINE` every quant lines.
		origin, skip, maxLines, quantum := 0, 0, 0, 5000
		callback := ""
		// readFD lets the caller pick an open FD other than stdin
		// (mapfile -u 3). -1 means "use r.stdin".
		readFD := -1
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-t":
				// Remove the delim from each line read
				dropDelim = true
			case "-d":
				if len(fp.remaining) == 0 {
					return failf(2, "%s: -d: option requires an argument\n", name)
				}
				delim = fp.value()
				if delim == "" {
					// Bash sets the delim to an ASCII NUL if provided with an empty
					// string.
					delim = "\x00"
				}
			case "-O":
				v := fp.value()
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return failf(2, "%s: %s: invalid origin specification\n", name, v)
				}
				origin = n
			case "-s":
				v := fp.value()
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return failf(2, "%s: %s: invalid line count specification\n", name, v)
				}
				skip = n
			case "-n":
				v := fp.value()
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return failf(2, "%s: %s: invalid line count specification\n", name, v)
				}
				maxLines = n
			case "-c":
				v := fp.value()
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return failf(2, "%s: %s: invalid callback quantum\n", name, v)
				}
				quantum = n
			case "-C":
				v := fp.value()
				if v == "" {
					return failf(2, "%s: -C: option requires an argument\n", name)
				}
				callback = v
			case "-u":
				v := fp.value()
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return failf(2, "%s: %s: invalid file descriptor specification\n", name, v)
				}
				readFD = n
			default:
				return invalidOpt(name, flag)
			}
		}

		args := fp.args()
		var arrayName string
		switch len(args) {
		case 0:
			arrayName = "MAPFILE"
		case 1:
			if args[0] == "" {
				return failf(1, "%s: empty array variable name\n", name)
			}
			if !syntax.ValidName(args[0]) {
				return failf(2, "%s: `%s': not a valid identifier\n", name, args[0])
			}
			arrayName = args[0]
			if vr := r.lookupVar(arrayName); vr.Kind == expand.NameRef {
				if _, _, ok := splitArrayRef(vr.Str); ok {
					return failf(1, "%s: `%s': not a valid identifier\n", name, vr.Str)
				}
				if vr.Str == "" {
					// An empty-target nameref can't be followed; bash drops
					// the nameref attribute and turns the variable itself
					// into the array (with a warning) rather than
					// dereferencing to an empty name.
					r.errf("%swarning: %s: removing nameref attribute\n",
						r.bashErrPrefix(r.curStmtPos), arrayName)
				} else {
					arrayName = vr.Str
				}
			}
		default:
			return failf(2, "%s: Only one array name may be specified, %v\n", name, args)
		}

		// Resolve the input source: -u FD selects an entry from the
		// per-runner fdTable; 0 means stdin; anything else is an
		// error if the fd hasn't been opened by a redirect.
		var src io.Reader = r.stdin
		switch {
		case readFD < 0, readFD == 0:
			if stdin := r.scriptStdinReader(); stdin != nil {
				src = stdin
			}
		case readFD == 1, readFD == 2:
			return failf(2, "%s: %d: invalid file descriptor: not open for reading\n", name, readFD)
		default:
			f, ok := r.fdTable[readFD]
			if !ok && !r.fdClosedTable[readFD] {
				f, ok = r.inheritedFd(readFD)
			}
			if !ok {
				return failf(2, "%s: %d: invalid file descriptor: Bad file descriptor\n", name, readFD)
			}
			src = f
		}
		var newLines []string
		if r.inBashPPTask() {
			file, ok := src.(*os.File)
			if !ok {
				return failf(2, "%s: blocking non-file input is unavailable inside a Bash++ task\n", name)
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				return failf(2, "%s: blocking non-regular input is unavailable inside a Bash++ task\n", name)
			}
		}
		scanner := bufio.NewScanner(src)
		scanner.Split(mapfileSplit(delim[0], dropDelim))
		lineNum := 0
		for scanner.Scan() {
			if r.inBashPPTask() && r.bashPPTaskContext(ctx).Err() != nil {
				r.bashPPTaskCanceled = true
				r.exit.fatal(r.bashPPTaskContext(ctx).Err())
				return r.exit
			}
			lineNum++
			if skip > 0 && lineNum <= skip {
				continue
			}
			newLines = append(newLines, scanner.Text())
			// Fire the callback after the line is recorded — order
			// matters when -n caps reads, so do this before the
			// maxLines break check.
			if callback != "" && len(newLines)%quantum == 0 {
				idx := origin + len(newLines) - 1
				quoted, qerr := syntax.Quote(scanner.Text(), syntax.LangBash)
				if qerr == nil {
					cb := fmt.Sprintf("%s %d %s", callback, idx, quoted)
					func() {
						savedAgentic := r.bashPPAgentic
						r.bashPPAgentic = false
						defer func() { r.bashPPAgentic = savedAgentic }()
						p := syntax.NewParser()
						if r.Dialect() == syntax.LangBashPP {
							syntax.Variant(syntax.LangBashPP)(p)
						}
						if prog, perr := p.Parse(strings.NewReader(cb), ""); perr == nil {
							r.stmts(ctx, prog.Stmts)
						}
					}()
				}
			}
			if maxLines > 0 && len(newLines) >= maxLines {
				break
			}
		}
		if err := scanner.Err(); err != nil {
			return failf(2, "%s: unable to read, %v\n", name, err)
		}
		if r.inBashPPTask() && r.bashPPTaskContext(ctx).Err() != nil {
			r.bashPPTaskCanceled = true
			r.exit.fatal(r.bashPPTaskContext(ctx).Err())
			return r.exit
		}

		// Merge into the existing indexed array so that entries below
		// origin and above origin+len(newLines) survive (matches bash).
		// mapfile always assigns the array, so it is set even when the
		// input was empty (`declare -a r=()` rather than `declare -a r`).
		var vr expand.Variable
		vr.Kind = expand.Indexed
		vr.Set = true
		if origin > 0 {
			prev := r.lookupVar(arrayName)
			if prev.Kind == expand.Indexed {
				vr.List = append([]string(nil), prev.List...)
			}
		}
		if len(vr.List) < origin {
			vr.List = append(vr.List, make([]string, origin-len(vr.List))...)
		}
		end := origin + len(newLines)
		if len(vr.List) < end {
			vr.List = append(vr.List, make([]string, end-len(vr.List))...)
		}
		for i, line := range newLines {
			vr.List[origin+i] = line
		}
		r.setVar(arrayName, vr)

	case "jobs":
		// jobs [-lnprs] [jobspec ...]
		var long, pidOnly, runningOnly, stoppedOnly bool
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-l":
				long = true
			case "-p":
				pidOnly = true
			case "-r":
				runningOnly = true
			case "-s":
				stoppedOnly = true
			case "-n":
				// "only changed jobs": we don't track notification
				// state, so this lists nothing extra — accept it.
			default:
				return invalidOpt("jobs", flag)
			}
		}
		jobs := r.realJobs()
		specs := fp.args()
		if len(specs) > 0 {
			for _, spec := range specs {
				bg := r.resolveJobArg(spec)
				if bg == nil || bg.jobID == 0 {
					return failf(1, "jobs: %s: no such job\n", spec)
				}
				r.formatJob(jobs, bg, long, pidOnly)
			}
			break
		}
		for _, bg := range jobs {
			if stoppedOnly && !jobStoppedState(bg) {
				continue
			}
			if runningOnly && !jobRunningState(bg) {
				continue
			}
			r.formatJob(jobs, bg, long, pidOnly)
		}
	case "fg":
		// Argument forms mirror the merged `wait` logic: no args → most
		// recent bgProc; %N → bash job-spec; gN → legacy $! sentinel;
		// bare integer → real OS PID (since `$!` now returns one when the
		// bg statement spawned a real exec). On Unix, an external job has a
		// recorded process group which is given the controlling terminal for
		// the duration of the foreground wait.
		//
		// Job-control gating must match bash even though subshells here
		// are goroutines: refuse entirely when monitor mode is off, and
		// refuse a job that was backgrounded before job control was
		// enabled (`set +o monitor; cmd &; set -m; fg %1`). Without this,
		// fg would block forever on a job it can never foreground.
		fp := flagParser{remaining: args}
		for fp.more() {
			return invalidOpt("fg", fp.flag())
		}
		args = fp.args()
		if len(args) > 1 {
			return failf(1, "fg: too many arguments\n")
		}
		if !r.monitorActive() {
			return failf(1, "fg: no job control\n")
		}
		if r.jobsReadOnly {
			return failf(1, "fg: no current jobs\n")
		}
		var bg *bgProc
		switch {
		case len(args) == 0:
			if jobs := r.realJobs(); len(jobs) == 0 {
				return failf(1, "fg: no current jobs\n")
			} else {
				bg = r.currentJob(jobs)
			}
		case strings.HasPrefix(args[0], "%"):
			arg := strings.TrimPrefix(args[0], "%")
			switch arg {
			case "%", "+", "", "-":
				bg = r.resolveJobArg(args[0])
				if bg == nil {
					return failf(1, "fg: no current jobs\n")
				}
			default:
				bg = r.resolveJobArg(args[0])
				if bg == nil {
					return failf(1, "fg: %%%s: no such job\n", arg)
				}
			}
		default:
			if strings.HasPrefix(args[0], "g") {
				bg = r.resolveJobArg(args[0])
				if bg == nil {
					return failf(1, "fg: %s: no such job\n", args[0])
				}
			} else {
				pid, perr := strconv.ParseInt(args[0], 10, 64)
				if perr != nil {
					return failf(1, "fg: %s: no such job\n", args[0])
				}
				for _, candidate := range r.bgProcs {
					if candidate.matchesPid(pid) {
						bg = candidate
						break
					}
				}
				if bg == nil {
					return failf(1, "fg: pid %s is not a child of this shell\n", args[0])
				}
			}
		}
		if !bg.jobControl {
			return failf(1, "fg: job %d started without job control\n", bg.jobID)
		}
		r.outf("%s\n", bg.cmd)
		// A completed synthetic fixture has no terminal ownership to change;
		// preserve its recorded status. Real jobs wait for PID publication so
		// fg cannot race ahead of exec and block forever behind SIGTTIN.
		select {
		case <-bg.pidReady:
		case <-bg.done:
		case <-ctx.Done():
			return failf(1, "fg: %v\n", ctx.Err())
		}
		if jobDone(bg) {
			exit = *bg.exit
			r.removeJob(bg)
			break
		}
		pid := bg.pid.Load()
		if pid <= 0 {
			// The portable default uses synthetic goroutine jobs. It retains the
			// historical wait/status seam but cannot claim terminal foregrounding.
			<-bg.done
			exit = *bg.exit
			r.removeJob(bg)
			break
		}
		pgrp := int(bg.pgrp.Load())
		tty, err := foregroundExistingJob(r, pgrp)
		if err != nil {
			return failf(1, "fg: %v\n", err)
		}
		sigCont, _ := signalByName("CONT")
		if err := sendSignal(foregroundContinuePid(bg), sigCont); err != nil {
			_ = tty.restore()
			if jobDone(bg) {
				exit = *bg.exit
				r.removeJob(bg)
				break
			}
			return failf(1, "fg: job %d: %v\n", bg.jobID, err)
		}
		bg.ignoreNextContinue.Store(0)
		bg.ignoreNextStop.Store(1)
		bg.setState(jobRunning)
		select {
		case <-bg.done:
		case <-ctx.Done():
			_ = tty.restore()
			return failf(1, "fg: %v\n", ctx.Err())
		}
		if err := tty.restore(); err != nil {
			return failf(1, "fg: restore controlling terminal: %v\n", err)
		}
		exit = *bg.exit
		r.removeJob(bg)
	case "bg":
		fp := flagParser{remaining: args}
		for fp.more() {
			return invalidOpt("bg", fp.flag())
		}
		args = fp.args()
		if !r.monitorActive() {
			return failf(1, "bg: no job control\n")
		}
		if r.jobsReadOnly {
			return failf(1, "bg: no current jobs\n")
		}
		jobs := r.realJobs()
		if len(args) == 0 {
			if len(jobs) == 0 {
				return failf(1, "bg: no current jobs\n")
			} else {
				args = []string{fmt.Sprintf("%%%d", r.currentJob(jobs).jobID)}
			}
		}
		for _, spec := range args {
			bg := r.resolveJobArg(spec)
			if bg == nil {
				exit.code = 1
				r.errf("%sbg: %s: no such job\n", r.bashErrPrefix(pos), spec)
				continue
			}
			if !jobStoppedState(bg) {
				r.errf("%sbg: job %d already in background\n", r.bashErrPrefix(pos), bg.jobID)
				continue
			}
			select {
			case <-bg.pidReady:
				if pid := bg.pid.Load(); pid > 0 {
					sigCont, _ := signalByName("CONT")
					if err := sendSignal(jobSignalPid(bg), sigCont); err != nil {
						exit.code = 1
						r.errf("%sbg: job %d: %v\n", r.bashErrPrefix(pos), bg.jobID, err)
						continue
					}
				}
			default:
			}
			bg.ignoreNextContinue.Store(0)
			bg.ignoreNextStop.Store(1)
			bg.setState(jobRunning)
			r.preferredJobID = bg.jobID
			r.out(r.bgJobLine(jobs, bg))
		}
	case "fc":
		return r.fcBuiltin(ctx, pos, args)
	case "bind":
		// Stub: bind requires readline infrastructure.
	case "history":
		return r.historyBuiltin(pos, args)
	case "suspend":
		fp := flagParser{remaining: args}
		for fp.more() {
			switch flag := fp.flag(); flag {
			case "-f":
			default:
				return invalidOpt("suspend", flag)
			}
		}
		return failf(1, "suspend: cannot suspend: no job control\n")
	case "runner-state":
		// Agentic introspection. Emits a JSON object describing the
		// current runner state to stdout. Subcommand selects which
		// section; with no arg, the full dump is returned.
		section := "all"
		if len(args) > 0 {
			section = args[0]
		}
		obj := map[string]any{}
		emitVars := func() map[string]string {
			m := map[string]string{}
			r.writeEnv.Each(func(name string, vr expand.Variable) bool {
				if vr.IsSet() {
					m[name] = vr.String()
				}
				return true
			})
			return m
		}
		emitOpts := func() map[string]bool {
			m := map[string]bool{}
			for i, opt := range &posixOptsTable {
				m[opt.name] = r.opts[i]
			}
			for i, opt := range bashOptsTable {
				m[opt.name] = r.opts[len(posixOptsTable)+i]
			}
			return m
		}
		emitTraps := func() map[string]string {
			m := map[string]string{}
			for k, v := range r.trapCallbacks {
				m[k] = v
			}
			return m
		}
		emitFds := func() []int {
			fds := []int{}
			for n := range r.fdTable {
				fds = append(fds, n)
			}
			for n := range r.inheritedFds {
				if !slices.Contains(fds, n) {
					fds = append(fds, n)
				}
			}
			slices.Sort(fds)
			return fds
		}
		emitFuncs := func() []string {
			names := []string{}
			for k := range r.Funcs {
				names = append(names, k)
			}
			slices.Sort(names)
			return names
		}
		switch section {
		case "vars":
			obj["vars"] = emitVars()
		case "opts":
			obj["opts"] = emitOpts()
		case "traps":
			obj["traps"] = emitTraps()
		case "fds":
			obj["fds"] = emitFds()
		case "funcs":
			obj["funcs"] = emitFuncs()
		case "callstack":
			frames := []map[string]any{}
			for _, f := range r.callStack {
				frames = append(frames, map[string]any{
					"funcName": f.funcName,
					"source":   f.source,
					"line":     f.line,
				})
			}
			obj["callstack"] = frames
		case "all", "":
			obj["vars"] = emitVars()
			obj["opts"] = emitOpts()
			obj["traps"] = emitTraps()
			obj["fds"] = emitFds()
			obj["funcs"] = emitFuncs()
			obj["subshell_level"] = r.subshellLevel
			obj["umask"] = fmt.Sprintf("%04o", r.umask)
			obj["deterministic"] = r.deterministic
		default:
			return failf(2, "runner-state: unknown section %q (try: vars opts traps fds funcs callstack all)\n", section)
		}
		buf, err := json.Marshal(obj)
		if err != nil {
			return failf(1, "runner-state: %v\n", err)
		}
		r.out(string(buf))
		r.out("\n")
	case "logout":
		// Bash refuses `logout` from a non-login shell. Embedders mark
		// the runner via WithLoginShell.
		if !r.loginShell {
			return failf(1, "logout: not login shell: use `exit'\n")
		}
		switch len(args) {
		case 0:
			exit = r.lastExit
		case 1:
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return failf(2, "logout: invalid exit status code: %q\n", args[0])
			}
			exit.code = uint8(n)
		default:
			return failf(1, "logout: too many arguments\n")
		}
		exit.exiting = true
	case "compgen":
		actionType := ""
		varName := ""
		prefix := ""
		filter := ""
		word := ""
		wordList := ""
		wordListGiven := false
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch arg {
			case "-A":
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				actionType = args[i+1]
				i++
			case "-V":
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				varName = args[i+1]
				if !syntax.ValidName(varName) {
					return failf(1, "compgen: `%s': not a valid identifier\n", varName)
				}
				i++
			case "-P":
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				prefix = args[i+1]
				i++
			case "-X":
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				filter = args[i+1]
				i++
			case "-o":
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				switch args[i+1] {
				case "bashdefault", "default", "dirnames", "filenames", "nospace":
				default:
					return failf(1, "compgen: %s: invalid option name\n", args[i+1])
				}
				i++
			case "-a":
				actionType = "alias"
			case "-b":
				actionType = "builtin"
			case "-c":
				actionType = "command"
			case "-d":
				actionType = "directory"
			case "-e":
				actionType = "export"
			case "-f":
				actionType = "file"
			case "-g":
				actionType = "group"
			case "-j":
				actionType = "job"
			case "-k":
				actionType = "keyword"
			case "-s":
				actionType = "service"
			case "-u":
				actionType = "user"
			case "-v":
				actionType = "variable"
			case "-W":
				// -W <wordlist>: a distinct mode — complete from a literal
				// IFS-split word list (the most common completion idiom), not
				// an -A action type.
				if i+1 >= len(args) {
					return invalidOpt("compgen", arg)
				}
				wordList = args[i+1]
				wordListGiven = true
				i++
			case "-r", "-D":
				return invalidOpt("compgen", arg)
			default:
				if strings.HasPrefix(arg, "-") {
					return invalidOpt("compgen", arg)
				}
				word = arg
			}
		}
		var names []string
		ok := true
		switch {
		case wordListGiven:
			// IFS-split the literal list; the word-prefix filter below
			// narrows it to candidates starting with `word`.
			names = r.compgenWordList(wordList)
		case actionType == "file" || actionType == "directory":
			// Path-aware: candidates already start with `word` (dir prefix +
			// matching entries), so the generic prefix filter still applies.
			names = r.compgenFiles(ctx, pos, word, actionType == "directory")
		case actionType == "command":
			names = r.compgenCommands(ctx, pos)
		case actionType == "user", actionType == "group", actionType == "service":
			names = r.compgenEtc(ctx, actionType)
		case actionType == "job":
			// bash completes active job names here; this runner's bg "jobs"
			// are goroutines without stable names, so there is nothing to
			// complete (correct when no jobs exist).
			names = nil
		default:
			names, ok = r.compgenNames(actionType)
		}
		if !ok {
			// bash exits 2 for an unknown action name (usage error).
			return failf(2, "compgen: %s: invalid action name\n", actionType)
		}
		var out []string
		for _, n := range names {
			if word != "" && !strings.HasPrefix(n, word) {
				continue
			}
			if filter != "" {
				if matched, _ := filepath.Match(filter, n); matched {
					continue
				}
			}
			out = append(out, prefix+n)
		}
		if varName != "" {
			r.setVar(varName, expand.Variable{Set: true, Kind: expand.Indexed, List: out})
		} else {
			for _, n := range out {
				r.outf("%s\n", n)
			}
		}
		// bash: compgen exits 1 when no candidates were generated, 0 otherwise —
		// a contract completion scripts branch on (`compgen … || <no matches>`).
		if len(out) == 0 {
			exit.code = 1
		}
	case "complete", "compopt":
		if name == "compopt" {
			if len(args) >= 2 && args[0] == "-o" {
				return failf(1, "compopt: %s: invalid option name\n", args[1])
			}
			break
		}
		exit = r.completeBuiltin(pos, args)
		return exit
	case "times":
		// Print accumulated user and system times.
		r.outf("0m0.000s 0m0.000s\n0m0.000s 0m0.000s\n")
	case "umask":
		symbolic := false
		printFlag := false
		for len(args) > 0 {
			if args[0] == "--" {
				args = args[1:]
				break
			}
			if args[0] == "-S" || args[0] == "-p" {
				flag := args[0]
				args = args[1:]
				switch flag {
				case "-S":
					symbolic = true
				case "-p":
					printFlag = true
				}
				continue
			}
			if strings.HasPrefix(args[0], "-") && args[0] != "-" {
				r.errf("%sumask: %s: invalid option\n", r.bashErrPrefix(pos), args[0])
				r.errf("umask: usage: %s\n", bashUsage["umask"])
				exit.code = 2
				return exit
			}
			break
		}
		// helper to format the current mask in symbolic form
		formatSymbolic := func(mask int) string {
			// umask bits are inverted: a 1 bit means "deny".
			// Convert each user/group/other triad.
			perm := func(shift int) string {
				bits := (^mask >> shift) & 7
				s := ""
				if bits&4 != 0 {
					s += "r"
				}
				if bits&2 != 0 {
					s += "w"
				}
				if bits&1 != 0 {
					s += "x"
				}
				return s
			}
			return fmt.Sprintf("u=%s,g=%s,o=%s", perm(6), perm(3), perm(0))
		}
		if len(args) == 0 {
			switch {
			case symbolic && printFlag:
				r.outf("umask -S %s\n", formatSymbolic(r.umask))
			case symbolic:
				r.outf("%s\n", formatSymbolic(r.umask))
			case printFlag:
				r.outf("umask %04o\n", r.umask)
			default:
				r.outf("%04o\n", r.umask)
			}
			break
		}
		// Setting umask: accept either an octal mode or a bash-style
		// symbolic form (e.g. `u=rwx,g=rx,o=rx`, `g-w`, `+x`). The
		// symbolic form mutates the current mask; the octal form
		// replaces it. We deliberately do not call syscall.Umask
		// (process-wide) — only the per-Runner virtual mask moves.
		symRes := parseSymbolicUmask(args[0], r.umask)
		if symRes.ok {
			r.umask = symRes.mask
			if r.mirrorUmask {
				setProcessUmask(r.umask)
			}
			break
		}
		if symRes.kind != "" {
			// Symbolic mode that parsed partially before hitting
			// an invalid byte — match bash's diagnostic shape.
			tok := string(symRes.badChar)
			if symRes.badChar == 0 {
				tok = ""
			}
			return failf(1, "umask: `%s': invalid symbolic mode %s\n", tok, symRes.kind)
		}
		mask, err := strconv.ParseUint(args[0], 8, 32)
		if err != nil {
			return failf(1, "umask: %s: octal number out of range\n", args[0])
		}
		r.umask = int(mask)
		if r.mirrorUmask {
			setProcessUmask(r.umask)
		}
	case "export":
		// In POSIX mode the parser treats `export`/`readonly` as plain
		// commands rather than the declare-family keyword, so they reach
		// this simple-builtin path. Parse the leading `-p` option and a
		// `--` end-of-options separator before treating the rest as
		// name[=value] operands.
		printMode := false
		for len(args) > 0 {
			if args[0] == "-p" {
				printMode = true
				args = args[1:]
				continue
			}
			if args[0] == "--" {
				args = args[1:]
			}
			break
		}
		// `export -p`, or a bare `export` in POSIX mode, lists the
		// exported variables in a form re-readable by the shell.
		if printMode || (len(args) == 0 && r.opts[optPosix]) {
			r.printExportVars()
			break
		}
		for _, arg := range args {
			eqIdx := strings.IndexByte(arg, '=')
			if eqIdx >= 0 {
				name := arg[:eqIdx]
				if !syntax.ValidName(name) {
					exit = invalidIdentifier("export", name)
					continue
				}
				if prev := r.lookupVar(name); prev.ReadOnly {
					r.errf("%s%s: readonly variable\n", r.bashErrPrefix(r.curStmtPos), name)
					exit.code = 1
					continue
				}
				val := arg[eqIdx+1:]
				if r.bashPPHasRuntimeHandle(val) {
					r.errf("bash++: channel capabilities cannot be exported\n")
					exit.code = 2
					continue
				}
				r.setVar(name, expand.Variable{Set: true, Kind: expand.String, Str: val, Exported: true})
			} else {
				if !syntax.ValidName(arg) {
					exit = invalidIdentifier("export", arg)
					continue
				}
				if r.bashPPScope != nil {
					if cell := r.bashPPScope.lookup(arg).view(); cell != nil && cell.channel != nil {
						r.errf("bash++: channel capabilities cannot be exported\n")
						exit.code = 2
						continue
					}
				}
				vr := r.lookupVar(arg)
				vr.Exported = true
				r.setVar(arg, vr)
			}
		}
	case "readonly":
		if len(args) == 0 && r.opts[optPosix] {
			r.printReadonlyVars(true)
			break
		}
		if len(args) == 1 && (args[0] == "-a" || args[0] == "-A") {
			r.printArrayVars(args[0], true, r.opts[optPosix])
			break
		}
		// As with `export` above, POSIX mode routes `readonly -p` here
		// instead of through the declare-family keyword. List the
		// read-only variables and exit 0, matching the keyword path.
		if len(args) == 1 && args[0] == "-p" {
			r.printReadonlyVars(r.opts[optPosix])
			break
		}
		for _, arg := range args {
			eqIdx := strings.IndexByte(arg, '=')
			if eqIdx >= 0 {
				name := arg[:eqIdx]
				if !syntax.ValidName(name) {
					exit = invalidIdentifier("readonly", name)
					continue
				}
				if prev := r.lookupVar(name); prev.ReadOnly {
					r.errf("%s%s: readonly variable\n", r.bashErrPrefix(r.curStmtPos), name)
					exit.code = 1
					continue
				}
				val := arg[eqIdx+1:]
				r.setVar(name, expand.Variable{Set: true, Kind: expand.String, Str: val, ReadOnly: true})
			} else {
				if !syntax.ValidName(arg) {
					exit = invalidIdentifier("readonly", arg)
					continue
				}
				vr := r.lookupVar(arg)
				vr.ReadOnly = true
				r.setVar(arg, vr)
			}
		}
	case "local":
		if !r.inFunc {
			return failf(1, "local: can only be used in a function\n")
		}
		for _, arg := range args {
			eqIdx := strings.IndexByte(arg, '=')
			if eqIdx >= 0 {
				name := arg[:eqIdx]
				if !syntax.ValidName(name) {
					exit = invalidIdentifier("local", name)
					continue
				}
				val := arg[eqIdx+1:]
				r.setVar(name, expand.Variable{Set: true, Kind: expand.String, Str: val, Local: true})
			} else {
				if !syntax.ValidName(arg) {
					exit = invalidIdentifier("local", arg)
					continue
				}
				vr := r.lookupVar(arg)
				vr.Local = true
				r.setVar(arg, vr)
			}
		}
	case "declare", "typeset":
		// Simple declare when called as a command (not keyword).
		// Keyword form is handled by DeclClause in runner.go.
		inFuncScope := r.inFunc || len(r.callStack) > 0
		printMode := false
		exportMode := false
		readonlyMode := false
		namerefMode := false
		arrayMode := ""
		var names []string
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
				fp := flagParser{remaining: []string{arg}}
				for fp.more() {
					switch flag := fp.flag(); flag {
					case "-p":
						printMode = true
					case "-x":
						exportMode = true
					case "-r":
						readonlyMode = true
					case "-n":
						namerefMode = true
					case "-a", "-A":
						arrayMode = flag
					default:
						// Other simple-command declare flags are handled by
						// DeclClause in the common path; keep this fallback
						// narrowly scoped to the inline-assignment cases.
					}
				}
				continue
			}
			eqIdx := strings.IndexByte(arg, '=')
			if eqIdx >= 0 {
				name := arg[:eqIdx]
				val := arg[eqIdx+1:]
				vr := expand.Variable{Set: true, Kind: expand.String, Str: val}
				prev := r.lookupVar(name)
				vr.Exported = prev.Exported || exportMode
				if readonlyMode {
					vr.ReadOnly = true
				}
				if inFuncScope {
					vr.Local = true
				}
				r.setVar(name, vr)
				names = append(names, name)
			} else if namerefMode {
				// `VAR=val declare -n NAME` (and other CallExpr-form
				// declares): adding the nameref attribute re-validates
				// NAME's current value as a reference target, matching
				// the keyword DeclClause path (nameref11.sub line 22).
				names = append(names, arg)
				vr := r.lookupVar(arg)
				if vr.Kind == expand.String && (vr.Str != "" || vr.Set) && !validNameRefTarget(vr.Str) {
					r.errf("%s%s: `%s': invalid variable name for name reference\n",
						r.bashErrPrefix(pos), name, vr.Str)
					exit.code = 1
					continue
				}
				vr.Kind = expand.NameRef
				vr.Exported = vr.Exported || exportMode
				vr.ReadOnly = vr.ReadOnly || readonlyMode
				vr.Local = vr.Local || inFuncScope
				r.setVar(arg, vr)
			} else {
				names = append(names, arg)
				if exportMode || readonlyMode || inFuncScope {
					vr := r.lookupVar(arg)
					vr.Exported = vr.Exported || exportMode
					vr.ReadOnly = vr.ReadOnly || readonlyMode
					vr.Local = vr.Local || inFuncScope
					r.setVar(arg, vr)
				}
			}
		}
		if len(names) == 0 && arrayMode != "" {
			r.printArrayVars(arrayMode, readonlyMode, false)
			break
		}
		if printMode {
			for _, name := range names {
				vr := r.lookupVar(name)
				if !vr.Declared() {
					r.errf(r.bashErrPrefix(pos)+"declare: %s: not found\n", name)
					exit.code = 1
					continue
				}
				if vr.Kind == expand.NameRef && vr.Integer && vr.Set && vr.Str == "" {
					// An integer nameref whose value assignment was
					// rejected (`declare -i foo; foo=7*6`) stays invisible
					// in bash: `declare -p` skips it silently. See the
					// matching DeclClause path in runner.go.
					continue
				}
				r.outf("%s\n", formatDeclareVar(name, vr, false))
			}
		}
	case "ulimit":
		// Minimal best-effort ulimit: read-only on the common
		// resource flags via syscall.Getrlimit. Setting is a no-op
		// (bash's tests use `ulimit -n N` defensively and then
		// `ulimit -n` to read it back — we just keep the override
		// in r.ulimits so the read returns it).
		exit = r.ulimitBuiltin(pos, args)
	default:
		if hint, ok := unsupportedHints[name]; ok {
			return failf(2, "%s: not supported in this shell — %s\n", name, hint)
		}
		return failf(2, "%s: not supported in this shell\n", name)
	}
	return exit
}

func (r *Runner) jsonOut(v any) exitStatus {
	buf, err := json.Marshal(v)
	if err != nil {
		r.errf("json: %v\n", err)
		return exitStatus{code: 1}
	}
	r.out(string(buf))
	r.out("\n")
	return exitStatus{}
}

// realJobs returns the background jobs eligible for the `jobs` list and
// `%N` job specs, in creation order. Coproc and process-substitution
// bgProcs (jobID == 0) are excluded — bash tracks those separately.
func (r *Runner) realJobs() []*bgProc {
	var out []*bgProc
	for _, bg := range r.bgProcs {
		if bg.jobID > 0 {
			out = append(out, bg)
		}
	}
	return out
}

// nextJobID returns the lowest positive job number not currently in use,
// mirroring bash reusing the lowest free job-table slot.
func (r *Runner) nextJobID() int {
	for n := 1; ; n++ {
		used := false
		for _, bg := range r.bgProcs {
			if bg.jobID == n {
				used = true
				break
			}
		}
		if !used {
			return n
		}
	}
}

func (r *Runner) currentJob(jobs []*bgProc) *bgProc {
	current, _ := r.currentPreviousJobs(jobs)
	return current
}

func (r *Runner) currentPreviousJobs(jobs []*bgProc) (*bgProc, *bgProc) {
	var stopped, running []*bgProc
	for _, bg := range jobs {
		switch {
		case jobStoppedState(bg):
			stopped = append(stopped, bg)
		case jobRunningState(bg):
			running = append(running, bg)
		}
	}
	if len(stopped) > 0 {
		current := stopped[len(stopped)-1]
		if len(stopped) > 1 {
			return current, stopped[len(stopped)-2]
		}
		if len(running) > 0 {
			return current, running[len(running)-1]
		}
		return current, current
	}
	if r.preferredJobID != 0 {
		for i := len(running) - 1; i >= 0; i-- {
			if running[i].jobID == r.preferredJobID {
				current := running[i]
				for j := len(running) - 1; j >= 0; j-- {
					if running[j] != current {
						return current, running[j]
					}
				}
				return current, current
			}
		}
	}
	if len(running) > 0 {
		current := running[len(running)-1]
		if len(running) > 1 {
			return current, running[len(running)-2]
		}
		return current, current
	}
	if len(jobs) > 0 {
		current := jobs[len(jobs)-1]
		if len(jobs) > 1 {
			return current, jobs[len(jobs)-2]
		}
		return current, current
	}
	return nil, nil
}

// jobMarker returns bash's current/previous markers: '+' for the current job,
// '-' for the previous one, and a space otherwise. Stopped jobs take priority
// over running jobs, matching jobs.c:reset_current.
func (r *Runner) jobMarker(jobs []*bgProc, bg *bgProc) byte {
	current, previous := r.currentPreviousJobs(jobs)
	switch bg {
	case current:
		return '+'
	case previous:
		return '-'
	}
	return ' '
}

// longestSignalDesc is bash's LONGEST_SIGNAL_DESC (jobs.h): the column
// width of the job-state word ("Running"/"Done"/…) in `jobs` output.
const longestSignalDesc = 27

// jobDone reports whether a background job has finished.
func jobDone(bg *bgProc) bool {
	select {
	case <-bg.done:
		bg.setState(jobDead)
		return true
	default:
		return false
	}
}

func jobStoppedState(bg *bgProc) bool {
	return !jobDone(bg) && bg.jobState() == jobStopped
}

func jobRunningState(bg *bgProc) bool {
	return !jobDone(bg) && bg.jobState() != jobStopped
}

func (bg *bgProc) pidList() []int64 {
	bg.pidsMu.Lock()
	defer bg.pidsMu.Unlock()
	return slices.Clone(bg.pids)
}

func (bg *bgProc) matchesPid(pid int64) bool {
	// Zero and negative kill operands select a process group; they are not
	// process identities. A carrier-free asynchronous job has bg.pid == 0,
	// so accepting zero here makes `kill 0` target that one synthetic job
	// instead of the caller's process group.
	if pid <= 0 {
		return false
	}
	if bg.pid.Load() == pid {
		return true
	}
	for _, p := range bg.pidList() {
		if p == pid {
			return true
		}
	}
	return false
}

func (bg *bgProc) publishedSignalPid() int64 {
	if pid := int64(jobSignalPid(bg)); pid != 0 {
		return pid
	}
	pids := bg.pidList()
	if len(pids) == 0 {
		return 0
	}
	return pids[len(pids)-1]
}

// bgJobLine renders the line `bg` prints when it resumes a job in the
// background, e.g. "[1]+ sleep 10 &". #49: POSIX mode omits the
// current/previous (+/-) marker, leaving a single space where it would
// be ("[1] sleep 10 &"), matching bash's bg_builtin / pretty_print_job.
func (r *Runner) bgJobLine(jobs []*bgProc, bg *bgProc) string {
	if r.opts[optPosix] {
		return fmt.Sprintf("[%d] %s &\n", bg.jobID, bg.cmd)
	}
	return fmt.Sprintf("[%d]%c %s &\n", bg.jobID, r.jobMarker(jobs, bg), bg.cmd)
}

// formatJob prints one line of `jobs` output for bg, matching bash's
// list_one_job. jobs is the ordered real-job list, used to compute the
// current/previous markers. With pidOnly only the leader PID is printed;
// with long the PID is shown after the marker.
func (r *Runner) formatJob(jobs []*bgProc, bg *bgProc, long, pidOnly bool) {
	pid := bg.pid.Load()
	// A one-command job has the command PID as both `$!` and the observable
	// `jobs -p` identity in Bash. Our carrier remains the real process-group
	// leader internally, but exposing it here makes those two shell interfaces
	// disagree. Pipelines still publish their stable carrier group.
	if pgrp := bg.pgrp.Load(); pgrp > 0 && len(bg.pidList()) > 2 {
		pid = pgrp
	}
	if pidOnly {
		if pid > 0 {
			r.outf("%d\n", pid)
		} else {
			r.outf("g%d\n", bg.jobID)
		}
		return
	}
	marker := r.jobMarker(jobs, bg)
	cmd := bg.cmd
	if cmd == "" {
		cmd = "running"
	}
	posix := r.opts[optPosix]
	state, suffix := "Running", " &"
	if jobStoppedState(bg) {
		// #24: POSIX mode annotates the stop signal, e.g. "Stopped(SIGTSTP)";
		// default mode prints the bare word "Stopped".
		state, suffix = "Stopped", ""
		if posix {
			if sig := bg.getStopSignal(); sig != "" {
				state = "Stopped(" + sig + ")"
			}
		}
	} else if jobDone(bg) {
		// #23: Done jobs print the command without a trailing `&`. A zero
		// exit is "Done" in both modes; a nonzero exit N is "Exit N" in
		// default mode and "Done(N)" in POSIX mode (bash list_one_job).
		state, suffix = "Done", ""
		var code uint8
		if bg.exit != nil {
			code = bg.exit.code
		}
		if code != 0 {
			if posix {
				state = fmt.Sprintf("Done(%d)", code)
			} else {
				state = fmt.Sprintf("Exit %d", code)
			}
		}
	}
	if long {
		r.outf("[%d]%c %d %-*s%s%s\n", bg.jobID, marker, pid, longestSignalDesc, state, cmd, suffix)
		return
	}
	r.outf("[%d]%c  %-*s%s%s\n", bg.jobID, marker, longestSignalDesc, state, cmd, suffix)
}

// removeFinishedJobs drops completed `&` background jobs from the table.
// This mirrors bash's mark_dead_jobs_as_notified + cleanup_dead_jobs run
// after a `wait`: once a background job has finished and been waited for,
// bash removes it from the job table so it no longer shows up in `jobs`
// and its slot can be reused. Coproc / process-substitution entries
// (jobID == 0) are left to their own reapers.
func (r *Runner) removeFinishedJobs() {
	kept := r.bgProcs[:0]
	for _, bg := range r.bgProcs {
		if bg.jobID > 0 {
			select {
			case <-bg.done:
				r.saveDonePidStatus(bg)
				continue
			default:
			}
		}
		kept = append(kept, bg)
	}
	for i := len(kept); i < len(r.bgProcs); i++ {
		r.bgProcs[i] = nil
	}
	r.bgProcs = kept
}

func (r *Runner) removeJob(target *bgProc) {
	if target == nil {
		return
	}
	for i, bg := range r.bgProcs {
		if bg != target {
			continue
		}
		copy(r.bgProcs[i:], r.bgProcs[i+1:])
		r.bgProcs[len(r.bgProcs)-1] = nil
		r.bgProcs = r.bgProcs[:len(r.bgProcs)-1]
		return
	}
}

// asyncJobInheritedIgnore reports whether sig would have been inherited as
// SIG_IGN by the processes of bg's asynchronous list — POSIX 2.11's implicit
// SIGINT/SIGQUIT ignore when job control is off. A fork-based shell sets
// SIG_IGN in the child before exec, so the kernel discards a later kill of
// those signals; the pure-Go runner cannot pre-set a child's disposition for
// every delivery path, so its own kill builtin must treat the delivery as a
// successful no-op instead. A trap inside the async list that replaces or
// resets the disposition clears asyncDefaultIgnored (see setTrapCallback /
// removeTrapCallback), restoring raw delivery.
func asyncJobInheritedIgnore(bg *bgProc, sig killSig) bool {
	if bg == nil || bg.jobControl {
		return false
	}
	name, ok := signalName(sig)
	if !ok || (name != "INT" && name != "QUIT") {
		return false
	}
	job := bg.carrierSignalRunner.Load()
	if job == nil {
		return false
	}
	job.sigMu.Lock()
	defer job.sigMu.Unlock()
	return job.asyncDefaultIgnored[name]
}

// recordJobSignal moves a job through the stopped/running state machine
// after `kill` has delivered sig to it — whether the job was named by `%N`,
// by the PID of one of its processes, or is a synthetic goroutine job with no
// PID at all. `jobs`, `jobs -r`, `jobs -s`, the `+`/`-` current-job markers,
// `bg` and `fg` all read that state, so all three kill paths must record it
// identically.
//
// On unix the same transition is also observed from the child's wait status
// by the job-control waiter (os_unix.go), and ignoreNextStop/
// ignoreNextContinue keep the two sources from double-counting. On Windows
// there is no wait status for a stop at all: suspendProcess leaves the
// process suspended inside the exec goroutine's Wait, which never returns
// until it really exits. The kill is therefore the ONLY event that can tell
// the job table anything, which is why this must sit on the sending side.
func (r *Runner) recordJobSignal(bg *bgProc, sig killSig) {
	switch {
	case signalStopsJob(sig):
		bg.ignoreNextContinue.Store(1)
		if name, ok := signalName(sig); ok {
			bg.setStopSignal("SIG" + name)
		}
		bg.setState(jobStopped)
	case signalContinuesJob(sig):
		bg.ignoreNextContinue.Store(0)
		bg.ignoreNextStop.Store(1)
		bg.setState(jobRunning)
		r.preferredJobID = bg.jobID
	}
}

func (r *Runner) killSyntheticBg(bg *bgProc, sig killSig) {
	if bg == nil || sigIsZero(sig) {
		return
	}
	if asyncJobInheritedIgnore(bg, sig) {
		return
	}
	if signalStopsJob(sig) || signalContinuesJob(sig) {
		r.recordJobSignal(bg, sig)
		return
	}
	if signalDefaultDoesNotTerminate(sig) {
		return
	}
	if bg.killedSignal.CompareAndSwap(0, int32(sigNum(sig))) && bg.cancel != nil {
		bg.cancel()
	}
}

func (r *Runner) saveDonePidStatus(bg *bgProc) {
	if bg == nil {
		return
	}
	select {
	case <-bg.done:
	default:
		return
	}
	if r.doneBgPids == nil {
		r.doneBgPids = make(map[int64]exitStatus)
	}
	for _, pid := range bg.pidList() {
		r.doneBgPids[pid] = *bg.exit
	}
	if pid := bg.pid.Load(); pid > 0 {
		r.doneBgPids[pid] = *bg.exit
	}
}

// resolveJobArg maps a `wait`/`wait -n` argument to a background job:
// `%N` job-spec, `gN` legacy `$!` sentinel, or a real OS PID (also a
// coproc's synthetic `<NAME>_PID`). Returns nil when nothing matches.
func (r *Runner) resolveJobArg(arg string) *bgProc {
	if rest, ok := strings.CutPrefix(arg, "%"); ok {
		jobs := r.realJobs()
		switch rest {
		case "%", "+", "":
			// current job (most recent)
			if len(jobs) == 0 {
				return nil
			}
			return jobs[len(jobs)-1]
		case "-":
			// previous job
			if len(jobs) < 2 {
				return nil
			}
			return jobs[len(jobs)-2]
		}
		if strings.HasPrefix(rest, "?") {
			needle := strings.TrimPrefix(rest, "?")
			for i := len(jobs) - 1; i >= 0; i-- {
				if strings.Contains(jobs[i].cmd, needle) {
					return jobs[i]
				}
			}
			return nil
		}
		if n64, err := strconv.ParseInt(rest, 10, 0); err == nil {
			n := int(n64)
			for _, bg := range jobs {
				if bg.jobID == n {
					return bg
				}
			}
			return nil
		}
		for i := len(jobs) - 1; i >= 0; i-- {
			if strings.HasPrefix(jobs[i].cmd, rest) {
				return jobs[i]
			}
		}
		return nil
	}
	if rest, ok := strings.CutPrefix(arg, "g"); ok {
		n := int(atoi(rest))
		for _, bg := range r.bgProcs {
			if bg.jobID == n {
				return bg
			}
		}
		return nil
	}
	pid, perr := strconv.ParseInt(arg, 10, 64)
	if perr != nil {
		return nil
	}
	for _, candidate := range r.bgProcs {
		if candidate.matchesPid(pid) ||
			(candidate.coprocPid == pid && candidate.coprocPidVar != "") {
			return candidate
		}
	}
	return nil
}

// storeWaitPid implements `wait -p VAR`: store the finishing job's PID
// into VAR. The value mirrors `$!` — the real OS PID when one was
// published, else the legacy `g<N>` sentinel. VAR may be an array
// element (e.g. A[k]); setVarString routes that through the array path.
func (r *Runner) storeWaitPid(pidVar string, bg *bgProc) {
	if pidVar == "" || bg == nil {
		return
	}
	if bg.pidReady != nil {
		<-bg.pidReady
	}
	var val string
	if pid := bg.pid.Load(); pid > 0 {
		val = strconv.FormatInt(pid, 10)
	} else if bg.jobID > 0 {
		val = "g" + strconv.Itoa(bg.jobID)
	}
	r.setVarString(pidVar, val)
}

// waitAnyDone blocks until the first of the given background jobs
// finishes and returns it (nil if the slice is empty).
func waitAnyDone(procs []*bgProc) *bgProc {
	if len(procs) == 0 {
		return nil
	}
	cases := make([]reflect.SelectCase, len(procs))
	for i, bg := range procs {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(bg.done)}
	}
	chosen, _, _ := reflect.Select(cases)
	return procs[chosen]
}

func bashPrintfFormatError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "invalid format character") ||
		strings.Contains(msg, "missing format character")
}

// offendingToken extracts the token starting at pos within src, used
// to reformat our generic "statements must be separated" parser error
// into bash's "syntax error near unexpected token `X'" shape. Returns
// "" when pos is out of range or no token can be identified.
func offendingToken(src string, pos syntax.Pos) string {
	col := int(pos.Col())
	line := int(pos.Line())
	if line <= 0 || col <= 0 {
		return ""
	}
	// Walk to the start of the requested line.
	curLine := 1
	i := 0
	for ; i < len(src) && curLine < line; i++ {
		if src[i] == '\n' {
			curLine++
		}
	}
	// Advance to the requested column (1-indexed).
	i += col - 1
	if i >= len(src) {
		return ""
	}
	// Single-char operators bash names verbatim.
	switch src[i] {
	case ')', '(', '|', '&', ';', '<', '>', '`':
		return string(src[i])
	}
	// Identifier-ish token: keep reading until whitespace or
	// another operator.
	start := i
	for ; i < len(src); i++ {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '<' || c == '>' || c == '(' || c == ')' || c == '`' {
			break
		}
	}
	return src[start:i]
}

// evalSourceLine returns the 1-indexed nth line of src with the
// trailing newline stripped, or "" when n is out of range. Used by
// the eval builtin to echo the offending line under a parse error.
// firstBraceLine returns the 1-indexed line of the first bare `{`
// token in src (skipping over `${`, `$()` insides, and single/double
// quoted strings) — bash's "from `{' command on line M" diagnostic
// points back at where the unclosed block started, which we can
// only approximate from the source.
func firstBraceLine(src string) int {
	line := 1
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\n':
			line++
		case '\\':
			if i+1 < len(src) {
				i++
			}
		case '\'':
			for i++; i < len(src) && src[i] != '\''; i++ {
				if src[i] == '\n' {
					line++
				}
			}
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				switch src[i] {
				case '\\':
					if i+1 < len(src) {
						i++
					}
				case '\n':
					line++
				}
			}
		case '$':
			if i+1 < len(src) && (src[i+1] == '{' || src[i+1] == '(') {
				i++ // skip the `{` / `(`; we don't deeply scan
			}
		case '{':
			return line
		}
	}
	return 0
}

// innerArithText extracts the body of the first $((...)) or $[...]
// in line, returning ok=false when neither is present.
func innerArithText(line string) (string, bool) {
	if i := strings.Index(line, "$(("); i >= 0 {
		if j := strings.Index(line[i:], "))"); j >= 0 {
			return strings.TrimSpace(line[i+3 : i+j]), true
		}
	}
	if i := strings.Index(line, "$["); i >= 0 {
		if j := strings.IndexByte(line[i:], ']'); j >= 0 {
			return strings.TrimSpace(line[i+2 : i+j]), true
		}
	}
	return "", false
}

func evalSourceLine(src string, n int) string {
	if n <= 0 {
		return ""
	}
	cur := 1
	start := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			if cur == n {
				return src[start:i]
			}
			cur++
			start = i + 1
		}
	}
	if cur == n {
		return src[start:]
	}
	return ""
}

// bashUsage holds the usage line bash 5.3 prints after a builtin
// rejects an invalid flag — verbatim from bash so the test suite
// diffs cleanly. Only the builtins exercised by the bash 5.3 test
// suite are listed; missing entries simply omit the usage line.
var bashUsage = map[string]string{
	"alias":    "alias [-p] [name[=value] ... ]",
	"bg":       "bg [job_spec ...]",
	"break":    "break [n]",
	"builtin":  "builtin [shell-builtin [arg ...]]",
	"cd":       "cd [-L|[-P [-e]] [-@]] [dir]",
	".":        ". [-p path] filename [arguments]",
	"command":  "command [-pVv] command [arg ...]",
	"complete": "complete [-abcdefgjksuv] [-pr] [-DEI] [-o option] [-A action] [-G globpat] [-W wordlist] [-F function] [-C command] [-X filterpat] [-P prefix] [-S suffix] [name ...]",
	"compgen":  "compgen [-V varname] [-abcdefgjksuv] [-o option] [-A action] [-G globpat] [-W wordlist] [-F function] [-C command] [-X filterpat] [-P prefix] [-S suffix] [word]",
	"continue": "continue [n]",
	"declare":  "declare [-aAfFgiIlnrtux] [name[=value] ...] or declare -p [-aAfFilnrtux] [name ...]",
	"disown":   "disown [-h] [-ar] [jobspec ... | pid ...]",
	"enable":   "enable [-a] [-dnps] [-f filename] [name ...]",
	"eval":     "eval [arg ...]",
	"exec":     "exec [-cl] [-a name] [command [argument ...]] [redirection ...]",
	"export":   "export [-fn] [name[=value] ...] or export -p",
	"fc":       "fc [-e ename] [-lnr] [first] [last] or fc -s [pat=rep] [command]",
	"fg":       "fg [job_spec]",
	"getopts":  "getopts optstring name [arg ...]",
	"hash":     "hash [-lr] [-p pathname] [-dt] [name ...]",
	"help":     "help [-dms] [pattern ...]",
	"history":  "history [-c] [-d offset] [n] or history -anrw [filename] or history -ps arg [arg...]",
	"jobs":     "jobs [-lnprs] [jobspec ...] or jobs -x command [args]",
	"kill":     "kill [-s sigspec | -n signum | -sigspec] pid | jobspec ... or kill -l [sigspec]",
	"let":      "let arg [arg ...]",
	"local":    "local [option] name[=value] ...",
	"logout":   "logout [n]",
	"mapfile":  "mapfile [-d delim] [-n count] [-O origin] [-s count] [-t] [-u fd] [-C callback] [-c quantum] [array]",
	"printf":   "printf [-v var] format [arguments]",
	"pwd":      "pwd [-LP]",
	"read":     "read [-Eers] [-a array] [-d delim] [-i text] [-n nchars] [-N nchars] [-p prompt] [-t timeout] [-u fd] [name ...]",
	"readonly": "readonly [-aAf] [name[=value] ...] or readonly -p",
	"return":   "return [n]",
	"set":      "set [-abefhkmnptuvxBCEHPT] [-o option-name] [--] [-] [arg ...]",
	"shift":    "shift [n]",
	"shopt":    "shopt [-pqsu] [-o] [optname ...]",
	"source":   "source [-p path] filename [arguments]",
	"suspend":  "suspend [-f]",
	"trap":     "trap [-Plp] [[action] signal_spec ...]",
	"type":     "type [-afptP] name [name ...]",
	"typeset":  "typeset [-aAfFgiIlnrtux] name[=value] ... or typeset -p [-aAfFilnrtux] [name ...]",
	"umask":    "umask [-p] [-S] [mode]",
	"unalias":  "unalias [-a] name [name ...]",
	"unset":    "unset [-f] [-v] [-n] [name ...]",
	"wait":     "wait [-fn] [-p var] [id ...]",
}

// typeMatch is a single resolution of a name. type / command -V iterate
// them in bash priority order (keyword → alias → function → builtin →
// file). desc holds the "X is a Y" line for the default / -V output;
// path is set only for file matches.
type typeMatch struct {
	kind string // "keyword" | "alias" | "function" | "builtin" | "file"
	desc string
	path string
}

type cmdHashEntry struct {
	path       string
	hits       int
	restricted bool
}

type completionSpec struct {
	action   string
	options  []string
	flags    []string
	funcName string
	command  string
	wordlist string
	filter   string
	prefix   string
	suffix   string
}

func (s completionSpec) String(name string) string {
	var parts []string
	parts = append(parts, "complete")
	for _, opt := range s.options {
		parts = append(parts, "-o", opt)
	}
	if s.action != "" {
		parts = append(parts, "-A", s.action)
	}
	parts = append(parts, s.flags...)
	if s.funcName != "" {
		parts = append(parts, "-F", s.funcName)
	}
	if s.command != "" {
		parts = append(parts, "-C", s.command)
	}
	if s.wordlist != "" {
		parts = append(parts, "-W", bashCompletionQuote(s.wordlist))
	}
	if s.filter != "" {
		parts = append(parts, "-X", bashCompletionQuote(s.filter))
	}
	if s.prefix != "" {
		parts = append(parts, "-P", bashCompletionQuote(s.prefix))
	}
	if s.suffix != "" {
		parts = append(parts, "-S", bashCompletionQuote(s.suffix))
	}
	parts = append(parts, name)
	return strings.Join(parts, " ")
}

func bashCompletionQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func dirStackTopFirst(stack []string) []string {
	topFirst := make([]string, len(stack))
	for i, d := range stack {
		topFirst[len(stack)-1-i] = d
	}
	return topFirst
}

func setDirStackTopFirst(r *Runner, topFirst []string) {
	if cap(r.dirStack) < len(topFirst) {
		r.dirStack = make([]string, len(topFirst))
	} else {
		r.dirStack = r.dirStack[:len(topFirst)]
	}
	for i, d := range topFirst {
		r.dirStack[len(topFirst)-1-i] = d
	}
}

// validLogicalPWD reports whether PWD is eligible for pwd -L. POSIX permits
// the logical value only when it is absolute and contains no dot or dot-dot
// pathname components; otherwise pwd must fall back to physical resolution.
func validLogicalPWD(pwd string) bool {
	return validLogicalPWDMode(pwd, runtime.GOOS == "windows")
}

func validLogicalPWDMode(pwd string, windows bool) bool {
	if !shellPathAbsMode(pwd, windows) {
		return false
	}
	for _, component := range strings.Split(pwd, "/") {
		if component == "." || component == ".." {
			return false
		}
	}
	return true
}

func dirStackIndex(length int, sign byte, idx int) int {
	if sign == '-' {
		return length - 1 - idx
	}
	return idx
}

// BuiltinNames returns the names of all POSIX and Bash builtins this
// interpreter implements — the same set `compgen -b` reports. The slice is
// freshly allocated, so callers may sort or mutate it freely.
func BuiltinNames() []string {
	return bashBuiltinNames()
}

func bashBuiltinNames() []string {
	return []string{
		".", ":", "[", "alias", "bg", "bind", "break", "builtin",
		"caller", "cd", "command", "compgen", "complete", "compopt",
		"continue", "declare", "dirs", "disown", "echo", "enable",
		"eval", "exec", "exit", "export", "false", "fc", "fg",
		"getopts", "hash", "help", "history", "jobs", "kill",
		"let", "local", "logout", "mapfile", "popd", "printf",
		"pushd", "pwd", "read", "readarray", "readonly", "return",
		"set", "shift", "shopt", "source", "suspend", "test",
		"times", "trap", "true", "type", "typeset", "ulimit",
		"umask", "unalias", "unset", "wait",
	}
}

func bashKeywordNames() []string {
	return []string{
		"if", "then", "else", "elif", "fi", "case", "esac", "for",
		"select", "while", "until", "do", "done", "in", "function",
		"time", "{", "}", "!", "[[", "]]", "coproc",
	}
}

func bashHelpTopicNames() []string {
	names := []string{"!", "%", "(( ... ))"}
	names = append(names, bashBuiltinNames()...)
	names = append(names,
		"[[ ... ]]", "case", "coproc", "for", "for ((", "function", "if",
		"select", "time", "until", "variables", "while", "{ ... }")
	slices.Sort(names)
	return names
}

func bashCompletePrintOrder() []string {
	return []string{
		"printenv", "texi2html", "groupmod", "typeset", "nohup", "unalias",
		"groupdel", "telnet", "local", "readonly", "cd", "type", "ln",
		"gunzip", "makeinfo", "jobs", "pushd", "acroread", "unset",
		"ghostview", "rsh", "exec", "kill", "eval", "chown", "gzip",
		"newgrp", "shopt", "ftp", "rlogin", "getopts", "nice", "gdb",
		"fg", "dvips", "texi2dvi", ".", "declare", "export", "xdvi",
		"su", "popd", "trap", "wait", "zmore", "disown", "gs", "gv",
		"source", "make", "bg", "cat", "mkdir", "help", "read", "time",
		"zcat", "uncompress", "rmdir", "more", "gzcat",
	}
}

func bashHelpOverview() string {
	return `GNU bash, version 5.3.0(1)-release (x86_64-apple-darwin24.0.0)
These shell commands are defined internally.  Type ` + "`help'" + ` to see this list.
Type ` + "`help name'" + ` to find out more about the function ` + "`name'" + `.
Use ` + "`info bash'" + ` to find out more about the shell in general.
Use ` + "`man -k'" + ` or ` + "`info'" + ` to find out more about commands not in this list.

A star (*) next to a name means that the command is disabled.

 ! PIPELINE                              history [-c] [-d offset] [n] or hist>
 job_spec [&]                            if COMMANDS; then COMMANDS; [ elif C>
 (( expression ))                        jobs [-lnprs] [jobspec ...] or jobs >
 . [-p path] filename [arguments]        kill [-s sigspec | -n signum | -sigs>
 :                                       let arg [arg ...]
 [ arg... ]                              local [option] name[=value] ...
 [[ expression ]]                        logout [n]
 alias [-p] [name[=value] ... ]          mapfile [-d delim] [-n count] [-O or>
 bg [job_spec ...]                       popd [-n] [+N | -N]
 bind [-lpsvPSVX] [-m keymap] [-f file>  printf [-v var] format [arguments]
 break [n]                               pushd [-n] [+N | -N | dir]
 builtin [shell-builtin [arg ...]]       pwd [-LP]
 caller [expr]                           read [-Eers] [-a array] [-d delim] [>
 case WORD in [PATTERN [| PATTERN]...)>  readarray [-d delim] [-n count] [-O >
 cd [-L|[-P [-e]]] [-@] [dir]            readonly [-aAf] [name[=value] ...] o>
 command [-pVv] command [arg ...]        return [n]
 compgen [-V varname] [-abcdefgjksuv] >  select NAME [in WORDS ... ;] do COMM>
 complete [-abcdefgjksuv] [-pr] [-DEI]>  set [-abefhkmnptuvxBCEHPT] [-o optio>
 compopt [-o|+o option] [-DEI] [name .>  shift [n]
 continue [n]                            shopt [-pqsu] [-o] [optname ...]
 coproc [NAME] command [redirections]    source [-p path] filename [argument>
 declare [-aAfFgiIlnrtux] [name[=value>  suspend [-f]
 dirs [-clpv] [+N] [-N]                  test [expr]
 disown [-h] [-ar] [jobspec ... | pid >  time [-p] pipeline
 echo [-neE] [arg ...]                   times
 enable [-a] [-dnps] [-f filename] [na>  trap [-Plp] [[action] signal_spec ..>
 eval [arg ...]                          true
 exec [-cl] [-a name] [command [argume>  type [-afptP] name [name ...]
 exit [n]                                typeset [-aAfFgiIlnrtux] name[=value>
 export [-fn] [name[=value] ...] or ex>  ulimit [-SHabcdefiklmnpqrstuvxPRT] [>
 false                                   umask [-p] [-S] [mode]
 fc [-e ename] [-lnr] [first] [last] o>  unalias [-a] name [name ...]
 fg [job_spec]                           unset [-f] [-v] [-n] [name ...]
 for NAME [in WORDS ... ] ; do COMMAND>  until COMMANDS; do COMMANDS-2; done
 for (( exp1; exp2; exp3 )); do COMMAN>  variables - Names and meanings of so>
 function name { COMMANDS ; } or name >  wait [-fn] [-p var] [id ...]
 getopts optstring name [arg ...]        while COMMANDS; do COMMANDS-2; done
 hash [-lr] [-p pathname] [-dt] [name >  { COMMANDS ; }
 help [-dms] [pattern ...]
`
}

func bashHelpShiftLong() string {
	return `shift: shift [n]
    Shift positional parameters.
    
    Rename the positional parameters $N+1,$N+2 ... to $1,$2 ...  If N is
    not given, it is assumed to be 1.
    
    Exit Status:
    Returns success unless N is negative or greater than $#.
`
}

func (r *Runner) helpTopic(pattern, mode string) bool {
	switch mode {
	case "description":
		switch pattern {
		case "shift":
			r.outf("shift - Shift positional parameters.\n")
			return true
		}
	case "man":
		switch pattern {
		case ":":
			r.outf(`NAME
    : - Null command.

SYNOPSIS
    :

DESCRIPTION
    Null command.
    
    No effect; the command does nothing.
    
    Exit Status:
    Always succeeds.

SEE ALSO
    bash(1)

IMPLEMENTATION
    Copyright (C) 2025 Free Software Foundation, Inc.

`)
			return true
		}
	case "short":
		switch pattern {
		case "help":
			r.outf("help: help [-dms] [pattern ...]\n")
			return true
		case "builtin":
			r.outf("builtin: builtin [shell-builtin [arg ...]]\n")
			return true
		case "shift":
			r.outf("shift: shift [n]\n")
			return true
		case "read*":
			r.outf("Shell commands matching keyword `read*'\n\n")
			r.outf("%s", bashHelpReadShort())
			return true
		case "rea":
			r.outf("%s", bashHelpReadShort())
			return true
		}
	default:
		switch pattern {
		case ":":
			r.outf(`:: :
    Null command.
    
    No effect; the command does nothing.
    
    Exit Status:
    Always succeeds.
`)
			return true
		}
	}
	if mode == "" && IsBuiltin(pattern) {
		r.outf("%s: %s is a shell builtin\n", pattern, pattern)
		return true
	}
	return false
}

func bashHelpReadShort() string {
	return `read: read [-Eers] [-a array] [-d delim] [-i text] [-n nchars] [-N nchars] [-p prompt] [-t timeout] [-u fd] [name ...]
readarray: readarray [-d delim] [-n count] [-O origin] [-s count] [-t] [-u fd] [-C callback] [-c quantum] [array]
readonly: readonly [-aAf] [name[=value] ...] or readonly -p
`
}

func (r *Runner) bashSetOptNames() []string {
	var names []string
	if r.bashPPEnabled() && !r.hideBashPPOption {
		names = append(names, "bashpp")
	}
	for _, opt := range posixOptsTable {
		if opt.name != "" && opt.name != "restricted" {
			names = append(names, opt.name)
		}
	}
	for n := range noOpSetOptions {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (r *Runner) compgenNames(actionType string) ([]string, bool) {
	switch actionType {
	case "alias":
		var names []string
		for n := range r.alias {
			names = append(names, n)
		}
		slices.Sort(names)
		return names, true
	case "function":
		names := make([]string, 0, len(r.Funcs))
		for n := range r.Funcs {
			names = append(names, n)
		}
		slices.Sort(names)
		return names, true
	case "shopt":
		names := make([]string, 0, len(bashOptsTable))
		for _, opt := range bashOptsTable {
			names = append(names, opt.name)
		}
		slices.Sort(names)
		return names, true
	case "setopt":
		return r.bashSetOptNames(), true
	case "builtin", "enabled":
		return bashBuiltinNames(), true
	case "keyword":
		return bashKeywordNames(), true
	case "helptopic":
		return bashHelpTopicNames(), true
	case "variable":
		var names []string
		r.writeEnv.Each(func(n string, _ expand.Variable) bool {
			names = append(names, n)
			return true
		})
		slices.Sort(names)
		return names, true
	case "export":
		var names []string
		r.writeEnv.Each(func(n string, vr expand.Variable) bool {
			if vr.Exported {
				names = append(names, n)
			}
			return true
		})
		slices.Sort(names)
		return names, true
	}
	return nil, false
}

// compgenWordList implements `compgen -W <list>`: split the literal list on the
// current IFS (default space/tab/newline; an explicit empty IFS yields one
// word, matching bash). The caller's word-prefix filter then narrows it.
func (r *Runner) compgenWordList(s string) []string {
	ifs := " \t\n"
	if v := r.writeEnv.Get("IFS"); v.IsSet() {
		ifs = v.String()
	}
	if ifs == "" {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	return strings.FieldsFunc(s, func(c rune) bool { return strings.ContainsRune(ifs, c) })
}

// compgenFiles implements `compgen -f` / `compgen -d`: path-aware completion of
// filenames (or directories only) under the directory part of `word`. The
// returned candidates already carry `word`'s directory prefix, so they start
// with `word` and pass the caller's generic prefix filter unchanged.
func (r *Runner) compgenFiles(ctx context.Context, pos syntax.Pos, word string, dirsOnly bool) []string {
	dirPart, basePart, outPrefix := ".", word, ""
	if i := strings.LastIndex(word, "/"); i >= 0 {
		dirPart = word[:i]
		if dirPart == "" {
			dirPart = "/"
		}
		basePart = word[i+1:]
		outPrefix = word[:i+1]
	}
	entries, err := r.readDirHandler(r.handlerCtx(ctx, handlerKindReadDir, pos), r.absPath(dirPart))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, basePart) {
			continue
		}
		if dirsOnly && !e.IsDir() {
			continue
		}
		out = append(out, outPrefix+name)
	}
	slices.Sort(out)
	return out
}

// compgenCommands implements `compgen -c`: the names runnable as a command —
// shell keywords, builtins, functions, aliases, and the executables found on
// PATH. (Liberal like bash: every regular file in a PATH dir is listed.)
func (r *Runner) compgenCommands(ctx context.Context, pos syntax.Pos) []string {
	seen := map[string]bool{}
	var out []string
	add := func(names ...string) {
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	add(bashKeywordNames()...)
	add(bashBuiltinNames()...)
	for n := range r.Funcs {
		add(n)
	}
	for n := range r.alias {
		add(n)
	}
	for _, dir := range pathconv.SplitPathList(r.writeEnv.Get("PATH").String(), runtime.GOOS == "windows") {
		if dir == "" {
			dir = "."
		}
		entries, err := r.readDirHandler(r.handlerCtx(ctx, handlerKindReadDir, pos), r.absPath(dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				add(e.Name())
			}
		}
	}
	slices.Sort(out)
	return out
}

// compgenEtc implements `compgen -u` / `-g` / `-s`: user / group / service
// names read from the system databases (/etc/passwd, /etc/group,
// /etc/services). Empty when the file is absent (e.g. on Windows) — the same
// graceful degradation bash shows on a host without those databases.
func (r *Runner) compgenEtc(ctx context.Context, actionType string) []string {
	path := map[string]string{
		"user":    "/etc/passwd",
		"group":   "/etc/group",
		"service": "/etc/services",
	}[actionType]
	f, err := r.open(ctx, path, os.O_RDONLY, 0, false)
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var field string
		if actionType == "service" {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			field = fields[0]
		} else if i := strings.IndexByte(line, ':'); i >= 0 {
			field = line[:i]
		} else {
			field = line
		}
		if field != "" && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	slices.Sort(out)
	return out
}

func (r *Runner) completeBuiltin(pos syntax.Pos, args []string) exitStatus {
	var exit exitStatus
	spec := completionSpec{}
	printSpecs := false
	removeSpecs := false
	var names []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-p":
			printSpecs = true
		case "-r":
			removeSpecs = true
		case "--":
			names = append(names, args[i+1:]...)
			i = len(args)
		case "-V":
			r.errf("%scomplete: -V: invalid option\n", r.bashErrPrefix(pos))
			r.errf("complete: usage: %s\n", bashUsage["complete"])
			exit.code = 2
			return exit
		case "-A":
			if i+1 >= len(args) {
				exit.code = 2
				return exit
			}
			spec.action = args[i+1]
			i++
		case "-F":
			spec.funcName = args[i+1]
			i++
		case "-C":
			spec.command = args[i+1]
			i++
		case "-W":
			spec.wordlist = args[i+1]
			i++
		case "-X":
			spec.filter = args[i+1]
			i++
		case "-P":
			spec.prefix = args[i+1]
			i++
		case "-S":
			spec.suffix = args[i+1]
			i++
		case "-o":
			if i+1 >= len(args) {
				exit.code = 2
				return exit
			}
			switch args[i+1] {
			case "bashdefault", "default", "dirnames", "filenames", "nospace":
				spec.options = append(spec.options, args[i+1])
				slices.Sort(spec.options)
			default:
				r.errf("%scomplete: %s: invalid option name\n", r.bashErrPrefix(pos), args[i+1])
				exit.code = 1
				return exit
			}
			i++
		case "-a":
			spec.flags = append(spec.flags, "-a")
		case "-b":
			if i == len(args)-1 {
				r.errf("complete: usage: %s\n", bashUsage["complete"])
				exit.code = 2
				return exit
			}
			spec.flags = append(spec.flags, "-b")
		case "-c", "-d", "-e", "-f", "-g", "-j", "-k", "-s", "-u", "-v":
			spec.flags = append(spec.flags, arg)
		default:
			if strings.HasPrefix(arg, "-") {
				r.errf("%scomplete: %s: invalid option\n", r.bashErrPrefix(pos), arg)
				r.errf("complete: usage: %s\n", bashUsage["complete"])
				exit.code = 2
				return exit
			}
			names = append(names, arg)
		}
	}
	if r.completionSpecs == nil {
		r.completionSpecs = make(map[string]completionSpec)
	}
	if printSpecs {
		if len(names) > 0 {
			ok := false
			for _, n := range names {
				if spec, exists := r.completionSpecs[n]; exists {
					r.outf("%s\n", spec.String(n))
					ok = true
				}
			}
			if !ok {
				exit.code = 1
			}
			return exit
		}
		seen := make(map[string]bool, len(r.completionSpecs))
		var keys []string
		for _, n := range bashCompletePrintOrder() {
			if _, ok := r.completionSpecs[n]; ok {
				keys = append(keys, n)
				seen[n] = true
			}
		}
		var rest []string
		for n := range r.completionSpecs {
			if !seen[n] {
				rest = append(rest, n)
			}
		}
		slices.Sort(rest)
		keys = append(keys, rest...)
		for _, n := range keys {
			r.outf("%s\n", r.completionSpecs[n].String(n))
		}
		if len(keys) == 0 {
			exit.code = 1
		}
		return exit
	}
	if removeSpecs {
		if len(names) == 0 {
			clear(r.completionSpecs)
			return exit
		}
		for _, n := range names {
			if _, ok := r.completionSpecs[n]; !ok {
				r.errf("%scomplete: %s: no completion specification\n", r.bashErrPrefix(pos), n)
				exit.code = 1
				continue
			}
			delete(r.completionSpecs, n)
		}
		return exit
	}
	for _, n := range names {
		r.completionSpecs[n] = spec
	}
	return exit
}

// typeMatches returns all resolutions of arg, in bash priority order.
// skipFuncs corresponds to `type -f` (suppress function matches);
// alias matches are only included when [optExpandAliases] is set.
func (r *Runner) typeMatches(arg string, skipFuncs bool, env expand.Environ, allPaths bool) []typeMatch {
	var ms []typeMatch
	if isKeyword(arg) {
		ms = append(ms, typeMatch{
			kind: "keyword",
			desc: fmt.Sprintf("%s is a shell keyword", arg),
		})
	}
	if als, ok := r.alias[arg]; ok && r.opts[optExpandAliases] {
		ms = append(ms, typeMatch{
			kind: "alias",
			desc: fmt.Sprintf("%s is aliased to `%s'", arg, aliasValue(als)),
		})
	}
	// Bash POSIX mode: POSIX special builtins (break, :, continue,
	// ., eval, exec, exit, …) take precedence over functions during
	// command lookup. List the builtin entry first in that case.
	posixSpecial := r.opts[optPosix] && isPosixSpecialBuiltin(arg) && IsBuiltin(arg) && !r.disabledBuiltins[arg]
	if posixSpecial {
		ms = append(ms, typeMatch{
			kind: "builtin",
			desc: fmt.Sprintf("%s is a special shell builtin", arg),
		})
	}
	if !skipFuncs {
		if _, ok := r.Funcs[arg]; ok {
			ms = append(ms, typeMatch{
				kind: "function",
				desc: fmt.Sprintf("%s is a function", arg),
			})
		}
	}
	if !posixSpecial && IsBuiltin(arg) && !r.disabledBuiltins[arg] {
		ms = append(ms, typeMatch{
			kind: "builtin",
			desc: fmt.Sprintf("%s is a shell builtin", arg),
		})
	}
	// The embedder's pre-PATH rung (see [CommandResolver]) outranks the hash
	// table and PATH, as it does at dispatch; with -a both are listed.
	if m, ok := r.resolvedCommandMatch(arg); ok {
		ms = append(ms, m)
	}
	// Check the command hash table before doing a PATH lookup.
	// Bash uses the hashed entry directly (`<name> is hashed
	// (<path>)`) even when the file no longer exists.
	if entry, ok := r.cmdHashTable[arg]; ok {
		entry.hits++
		r.cmdHashTable[arg] = entry
		ms = append(ms, typeMatch{
			kind: "file",
			desc: fmt.Sprintf("%s is hashed (%s)", arg, entry.path),
			path: entry.path,
		})
	} else {
		paths := []string{env.Get("PATH").String()}
		if allPaths && !lookPathHasPath(arg, runtime.GOOS == "windows") {
			paths = splitLookPath(paths[0], runtime.GOOS == "windows")
			if len(paths) == 0 {
				paths = []string{""}
			}
		}
		for _, pathList := range paths {
			lookup := expand.FuncEnviron(func(name string) string {
				if name == "PATH" {
					return pathList
				}
				return env.Get(name).String()
			})
			if path, err := LookPathDir(r.Dir, lookup, arg); err == nil {
				ms = append(ms, typeMatch{
					kind: "file",
					desc: fmt.Sprintf("%s is %s", arg, path),
					path: path,
				})
			}
		}
	}
	return ms
}

// resolvedCommandMatch asks the embedder's [CommandResolver] about arg and
// shapes the answer as a typeMatch. Never consulted for a word containing a
// slash: that is a path, and paths are the shell's own business.
func (r *Runner) resolvedCommandMatch(arg string) (typeMatch, bool) {
	if r.commandResolver == nil || strings.ContainsRune(arg, '/') {
		return typeMatch{}, false
	}
	rc, ok := r.commandResolver(arg)
	if !ok {
		return typeMatch{}, false
	}
	m := typeMatch{kind: rc.Kind, desc: rc.Desc, path: rc.Path}
	if m.kind == "" {
		m.kind = "file"
	}
	if m.desc == "" {
		m.desc = fmt.Sprintf("%s is %s", arg, arg)
	}
	return m, true
}

// symbolicUmaskResult conveys how parseSymbolicUmask classified its input.
// `kind` is "" on success or `looks-octal` (caller should try ParseUint),
// otherwise the bash diagnostic kind: "character", "operator", or "".
type symbolicUmaskResult struct {
	mask    int
	ok      bool
	badChar byte
	kind    string // "character" | "operator" | "" (general)
}

// parseSymbolicUmask applies a bash-style symbolic umask string to
// `current` and returns the new mask. The grammar is one or more
// clauses separated by commas; each clause is `[who][op]perms` where
// `who` is any combination of `u`, `g`, `o`, `a` (default `a`),
// `op` is `=`, `+`, or `-`, and `perms` is any combination of
// `r`, `w`, `x`, or permission-copy tokens `u`, `g`, `o`. Additional
// operators may appear in the permissions tail, e.g. `u+w=r+x`; they
// are applied left-to-right. On failure, returns kind="" for "looks
// octal" so caller can try numeric parse, or "character"/"operator"
// with the offending byte for a bash-shaped diagnostic.
func parseSymbolicUmask(s string, current int) symbolicUmaskResult {
	if s == "" {
		return symbolicUmaskResult{}
	}
	// Quick reject: octal-looking input goes through ParseUint instead.
	allDigits := true
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return symbolicUmaskResult{}
	}
	mask := current
	for _, clause := range strings.Split(s, ",") {
		if clause == "" {
			return symbolicUmaskResult{badChar: ',', kind: "character"}
		}
		who := 0 // bitmask of which triads to affect: 4=u, 2=g, 1=o
		i := 0
		for ; i < len(clause); i++ {
			switch clause[i] {
			case 'u':
				who |= 4
			case 'g':
				who |= 2
			case 'o':
				who |= 1
			case 'a':
				who |= 7
			default:
				goto opStart
			}
		}
	opStart:
		if who == 0 {
			who = 7 // default = `a`
		}
		if i >= len(clause) {
			return symbolicUmaskResult{badChar: 0, kind: "operator"}
		}
		op := clause[i]
		if op != '=' && op != '+' && op != '-' {
			return symbolicUmaskResult{badChar: op, kind: "operator"}
		}
		apply := func(op byte, perms int) {
			applyTriad := func(shift int) {
				cur := (^mask >> shift) & 7
				switch op {
				case '=':
					cur = perms
				case '+':
					cur |= perms
				case '-':
					cur &^= perms
				}
				// Rebuild mask: set triad bits to ~cur.
				mask &^= 7 << shift
				mask |= ((^cur) & 7) << shift
			}
			if who&4 != 0 {
				applyTriad(6)
			}
			if who&2 != 0 {
				applyTriad(3)
			}
			if who&1 != 0 {
				applyTriad(0)
			}
		}
		for i < len(clause) {
			i++
			perms := 0
			for ; i < len(clause); i++ {
				switch clause[i] {
				case '=', '+', '-':
					goto applySegment
				case 'r':
					perms |= 4
				case 'w':
					perms |= 2
				case 'x':
					perms |= 1
				case 'u':
					perms |= (^mask >> 6) & 7
				case 'g':
					perms |= (^mask >> 3) & 7
				case 'o':
					perms |= (^mask >> 0) & 7
				case 's', 't', 'X':
					// Ignore permission bits that don't fit in umask.
				default:
					return symbolicUmaskResult{badChar: clause[i], kind: "character"}
				}
			}
		applySegment:
			apply(op, perms)
			if i < len(clause) {
				op = clause[i]
				if op != '=' && op != '+' && op != '-' {
					return symbolicUmaskResult{badChar: op, kind: "operator"}
				}
			}
		}
	}
	return symbolicUmaskResult{mask: mask & 0o777, ok: true}
}

// ulimitBuiltin implements a best-effort `ulimit`. `ulimit -X` reads
// the current limit (RLIMIT_NOFILE for `-n`, "unlimited" otherwise),
// and `ulimit -X N` records the value in r.ulimitOverride so the
// next read returns it. We don't actually call setrlimit because
// changes would affect the whole process and require permissions; the
// override is purely cosmetic but lets scripts that probe-and-loop
// against `ulimit -n` finish.
func (r *Runner) ulimitBuiltin(pos syntax.Pos, args []string) exitStatus {
	var exit exitStatus
	flag := "-f"
	var setVal string
	parsingOpts := true
	for _, a := range args {
		if parsingOpts && a == "--" {
			parsingOpts = false
			continue
		}
		if parsingOpts && len(a) > 1 && a[0] == '-' {
			for _, ch := range a[1:] {
				switch ch {
				case 'S', 'H':
					// Soft/hard selectors affect real setrlimit calls.
					// We keep cosmetic state only, so the resource flag
					// remains unchanged.
				case 'a':
					return exit
				case 'g':
					r.errf("%sulimit: -g: invalid option\n", r.bashErrPrefix(pos))
					r.errf("ulimit: usage: ulimit [-SHabcdefiklmnpqrstuvxPRT] [limit]\n")
					exit.code = 2
					return exit
				default:
					flag = "-" + string(ch)
				}
			}
			continue
		}
		setVal = a
	}
	if setVal != "" {
		if strings.HasPrefix(setVal, "+") {
			r.errf("%sulimit: %s: invalid number\n", r.bashErrPrefix(pos), setVal)
			exit.code = 1
			return exit
		}
		if flag == "-u" {
			r.errf("%sulimit: max user processes: cannot modify limit: Operation not permitted\n", r.bashErrPrefix(pos))
			exit.code = 1
			return exit
		}
		if setVal == "soft" || setVal == "hard" {
			return exit
		}
		if r.ulimitOverride == nil {
			r.ulimitOverride = make(map[string]string)
		}
		r.ulimitOverride[flag] = setVal
		return exit
	}
	if r.ulimitOverride != nil {
		if v, ok := r.ulimitOverride[flag]; ok {
			r.outf("%s\n", v)
			return exit
		}
	}
	switch flag {
	case "-n":
		// nofileLimit is per-platform: getrlimit on unix, "unlimited"
		// elsewhere (Windows has no fd rlimit concept).
		if cur, ok := nofileLimit(); ok {
			r.outf("%d\n", cur)
		} else {
			r.outf("unlimited\n")
		}
	default:
		r.outf("unlimited\n")
	}
	return exit
}

// unsupportedHints carries actionable messages for bash/POSIX builtins that
// IsBuiltin recognizes but this runner does not implement. The hint is
// appended to "<name>: not supported in this shell — " so agentic callers
// see a named alternative or escape hatch rather than a generic refusal.
// Most former entries (fg/bg/jobs/fc/umask/logout/etc.) now have explicit
// implementations in the dispatcher and no longer reach the default arm;
// future design work may swap some back to hint-only for outpost.
var unsupportedHints = map[string]string{
	"newgrp": "group switching is not supported; switch groups in the parent process (e.g. with sudo -g)",
}

// mapfileSplit returns a suitable Split function for a [bufio.Scanner];
// the code is mostly stolen from [bufio.ScanLines].
func mapfileSplit(delim byte, dropDelim bool) bufio.SplitFunc {
	// Bash strings can't hold a NUL byte, so when -d '' selects the
	// NUL delimiter we always drop it from the token regardless of -t.
	if delim == 0 {
		dropDelim = true
	}
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if i := bytes.IndexByte(data, delim); i >= 0 {
			// We have a full newline-terminated line.
			if dropDelim {
				return i + 1, data[0:i], nil
			} else {
				return i + 1, data[0 : i+1], nil
			}
		}
		// If we're at EOF, we have a final, non-terminated line. Return it.
		if atEOF {
			return len(data), data, nil
		}
		// Request more data.
		return 0, nil, nil
	}
}

func (r *Runner) printOptLine(name string, enabled, supported bool) {
	r.printOptLineWidth(name, enabled, supported, 15)
}

// printOptLineWidth emits a `name<spaces><tab>on/off` line padding the
// name to a width-character field. Bash 5.3 pads `shopt` to 20 and
// `set -o`/`shopt -o` to 15 (both encoded in the shopt fixture).
// Names longer than the field get no padding.
func (r *Runner) printOptLineWidth(name string, enabled, supported bool, width int) {
	_ = supported
	pad := width - len(name)
	if pad < 0 {
		pad = 0
	}
	r.outf("%s%s\t%s\n", name, strings.Repeat(" ", pad), r.optStatusText(enabled))
}

func (r *Runner) readLine(ctx context.Context, raw bool, delim byte) ([]byte, error) {
	if r.stdin == nil {
		return nil, errors.New("interp: can't read, there's no stdin")
	}
	return r.readLineFrom(ctx, r.stdin, raw, delim)
}

func (r *Runner) readLineFrom(ctx context.Context, stdin io.Reader, raw bool, delim byte) ([]byte, error) {
	var line []byte
	esc := false

	if file, ok := stdin.(*os.File); ok {
		if cancellable := cancellableReader(ctx, file); cancellable != nil {
			// The handle rejects deadlines (a Windows pipe or console), so
			// the AfterFunc below would silently do nothing and leave the
			// read wedged past its cancellation. Poll for readability
			// instead, which the context can interrupt.
			stdin = cancellable
		} else {
			stopc := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				file.SetReadDeadline(time.Now())
				close(stopc)
			})
			defer func() {
				if !stop() {
					// The AfterFunc was started.
					// Wait for it to complete, and reset the file's deadline.
					<-stopc
					file.SetReadDeadline(time.Time{})
				}
			}()
		}
	} else if _, timeoutAware := stdin.(*timeoutFileReader); !timeoutAware {
		// timeoutFileReader owns the context check and deliberately probes
		// readiness once after a deadline. Checking ctx here would prevent
		// already-buffered input from winning over a tiny read timeout.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	for {
		var buf [1]byte
		n, err := stdin.Read(buf[:])
		if n > 0 {
			b := buf[0]
			switch {
			case !raw && b == '\\':
				line = append(line, b)
				esc = !esc
			case !raw && b == '\n' && esc && delim == '\n':
				// Backslash-newline is a line continuation only
				// when the delimiter is the default newline. With
				// `-d <other>` bash treats newline as ordinary.
				line = line[:len(line)-1]
				esc = false
			case b == delim:
				return line, nil
			default:
				line = append(line, b)
				esc = false
			}
		}
		if err != nil {
			return line, err
		}
	}
}

func (r *Runner) changeDir(ctx context.Context, cmd, path string, physical ...bool) uint8 {
	if path == "" {
		// bash 5.3's wording for an empty directory operand.
		r.errf("%s%s: null directory\n", r.bashErrPrefix(r.curStmtPos), cmd)
		return 1
	}
	phys := false
	if len(physical) > 0 {
		phys = physical[0]
	}
	if runtime.GOOS == "windows" {
		// `cd D:` returns to the drive's recorded current directory, the
		// way cmd.exe tracks a cwd per drive.
		if target, ok := driveOperandTarget(path, r.driveCwd); ok {
			path = target
		}
	}
	apath, err := r.resolveCdPath(ctx, path, phys)
	if err != nil {
		r.errf("%s%s: %s: %s\n",
			r.bashErrPrefix(r.curStmtPos), cmd, bashDiagnosticWord(path), cdStatErrorReason(err))
		return 1
	}
	info, err := r.stat(ctx, apath)
	if err != nil {
		r.errf("%s%s: %s: %s\n",
			r.bashErrPrefix(r.curStmtPos), cmd, bashDiagnosticWord(path), cdStatErrorReason(err))
		return 1
	}
	if !info.IsDir() {
		r.errf("%s%s: %s: Not a directory\n",
			r.bashErrPrefix(r.curStmtPos), cmd, bashDiagnosticWord(path))
		return 1
	}
	if r.access(ctx, apath, access_X_OK) != nil {
		r.errf("%s%s: %s: Permission denied\n",
			r.bashErrPrefix(r.curStmtPos), cmd, bashDiagnosticWord(path))
		return 1
	}
	if r.lookupVar("OLDPWD").ReadOnly {
		r.errf("%sOLDPWD: readonly variable\n", r.bashErrPrefix(r.curStmtPos))
		return 1
	}
	if r.lookupVar("PWD").ReadOnly {
		r.errf("%sPWD: readonly variable\n", r.bashErrPrefix(r.curStmtPos))
		return 1
	}
	// The logical name bash records: the operand's own POSIX spelling for
	// a non-physical cd, the resolved path otherwise. On Unix this is apath.
	logical := r.cdLogicalPWD(path, apath, phys)
	r.replaceDirFile(apath)
	r.Dir = apath
	if runtime.GOOS == "windows" {
		r.driveCwd = recordDriveCwd(r.driveCwd, apath)
	}
	// Keep the top of the directory stack in sync with the current
	// dir so `pushd`/`popd`/`dirs` see the cd's effect. Bash treats
	// the topmost dirStack entry as the live "current dir".
	if len(r.dirStack) > 0 {
		r.dirStack[len(r.dirStack)-1] = logical
	}
	// bash keeps both PWD and OLDPWD exported across every cd (they show
	// up in `env` and in child process environments).
	r.setExportedVarString("OLDPWD", r.envGet("PWD"))
	r.setExportedVarString("PWD", logical)
	return 0
}

// logicalDir is the shell's current directory in the spelling `pwd` and
// `dirs` print. On Unix it is r.Dir. On Windows r.Dir is native, so the
// POSIX spelling comes from $PWD when that still names r.Dir (a cd through
// a mount keeps the operand's spelling: /bin stays /bin although the
// directory is root\usr\bin), and from the mounts otherwise.
func (r *Runner) logicalDir() string {
	if runtime.GOOS != "windows" {
		return r.Dir
	}
	return logicalDirMode(pathconv.CurrentMounts(), r.envGet("PWD"), r.Dir, true)
}

func logicalDirMode(m *pathconv.Mounts, pwd, dir string, windows bool) string {
	if !windows {
		return dir
	}
	if posixSpelled(pwd) && validLogicalPWDMode(pwd, true) &&
		sameNativePath(pathconv.ToOSMountsMode(m, dir, pwd, true), dir) {
		return pwd
	}
	return pathconv.FromOSMountsMode(m, dir, true)
}

// cdLogicalPWD is the PWD a successful cd records for operand, whose
// resolved directory is apath. On Unix it is apath. On Windows a logical
// cd keeps the operand's POSIX spelling, normalized the way bash's
// sh_canonpath does (pure string handling of . and ..): `cd /tmp` records
// /tmp rather than the native temp directory, a relative operand is
// appended to the current logical directory, and a physical cd or a
// natively spelled operand maps apath back through the mounts.
func (r *Runner) cdLogicalPWD(operand, apath string, physical bool) string {
	if runtime.GOOS != "windows" {
		return apath
	}
	return cdLogicalPWDMode(pathconv.CurrentMounts(), r.logicalDir(), operand, apath, physical, true)
}

func cdLogicalPWDMode(m *pathconv.Mounts, cur, operand, apath string, physical, windows bool) string {
	if !windows {
		return apath
	}
	if !physical {
		switch {
		case posixSpelled(operand):
			return canonPosixPath(operand)
		case cur != "" && posixSpelled(cur) && !nativeSpelled(operand):
			return canonPosixPath(cur + "/" + operand)
		}
	}
	return pathconv.FromOSMountsMode(m, apath, true)
}

// posixSpelled reports whether path is an absolute path in the shell's
// POSIX spelling: it starts with / and uses no backslash.
func posixSpelled(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.Contains(path, `\`)
}

// nativeSpelled reports whether path carries a native Windows spelling: a
// drive prefix or a backslash.
func nativeSpelled(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	_, ok := pathconv.DriveOf(path)
	return ok
}

// canonPosixPath normalizes an absolute POSIX path as a string, like bash's
// sh_canonpath: repeated slashes collapse, . is dropped, .. removes the
// previous component (never above /), and no trailing slash remains.
func canonPosixPath(path string) string {
	var parts []string
	for part := range strings.SplitSeq(path, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	return "/" + strings.Join(parts, "/")
}

// sameNativePath reports whether two native paths name the same directory
// as NTFS sees it: case-insensitively, with / and \ alike and a trailing
// separator ignored.
func sameNativePath(a, b string) bool {
	norm := func(p string) string {
		p = strings.ReplaceAll(p, "/", `\`)
		if len(p) > 1 {
			p = strings.TrimRight(p, `\`)
		}
		return p
	}
	return strings.EqualFold(norm(a), norm(b))
}

// resolveCdPath resolves a pathname for cd/pushd/popd by walking each
// component of the operand. Under physical mode, symlinks are resolved at each
// step and the current directory is resolved to its physical path before
// handling relative paths. This matches `bash --posix` behaviour: non-existing
// or non-directory intermediate components cause an immediate error.
func (r *Runner) resolveCdPath(ctx context.Context, path string, physical bool) (string, error) {
	// On Windows, map a drive/MSYS/mounted path (/c/foo, C:\foo, /usr) to an
	// OS path and, when it is absolute, resolve it directly — the POSIX
	// base="/" + split/join below does not understand Windows drive roots
	// (filepath.IsAbs("/c/x") is false).
	if runtime.GOOS == "windows" {
		stat := func(p string) (fs.FileInfo, error) { return r.stat(ctx, p) }
		if osp, ok, err := resolveCdPathWindows(pathconv.CurrentMounts(), r.Dir, path, physical, stat, evalPhysicalPath); ok {
			return osp, err
		}
	}
	// Determine the base directory for relative paths.
	base := r.Dir
	if physical {
		// PWD (and therefore r.Dir) can become stale when another process
		// renames the directory we are in. Physical cd must start from the
		// live directory inode, not its old logical name.
		physicalBase := runnerPhysicalDir(r, base)
		if resolved, err := evalPhysicalPath(physicalBase); err == nil {
			base = resolved
		}
	}
	if filepath.IsAbs(path) {
		base = "/"
	}
	parts := splitPathOperand(path)
	if len(parts) == 0 {
		return base, nil
	}
	current := base
	for _, part := range parts {
		current = joinNoClean(current, part)
		info, err := r.stat(ctx, current)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("Not a directory")
		}
		if physical {
			if resolved, err := evalPhysicalPath(current); err == nil {
				current = resolved
			}
		}
	}
	return filepath.Clean(current), nil
}

// resolveCdPathWindows resolves an absolute cd operand on Windows through
// the mount table: ok is false when the operand is relative (the caller's
// component walk applies). A missing extensionless target is retried with
// .exe so `cd /bin/sh` reports Not a directory, the error bash gives, when
// only sh.exe exists. stat and eval are seams so the logic runs on any
// host.
func resolveCdPathWindows(m *pathconv.Mounts, dir, path string, physical bool,
	stat func(string) (fs.FileInfo, error), eval func(string) (string, error),
) (osp string, ok bool, err error) {
	osp = pathconv.ToOSMountsMode(m, dir, path, true)
	if !pathconv.IsAbsMode(osp, true) {
		return "", false, nil
	}
	info, err := stat(osp)
	if err != nil {
		info, err = statExeFallback(stat, osp, err, true)
		if err != nil {
			return "", true, err
		}
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("Not a directory")
	}
	if physical {
		if resolved, err := eval(osp); err == nil {
			osp = resolved
		}
	}
	return osp, true, nil
}

func evalPhysicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return physicalLongPath(path, err)
	}
	return resolved, nil
}

// joinNoClean joins a directory and a path component without [filepath.Clean]
// normalization. This preserves ".." and "." components so they can be checked
// individually.
func joinNoClean(dir, comp string) string {
	if dir == "" {
		return comp
	}
	if comp == "" {
		return dir
	}
	return dir + string(filepath.Separator) + comp
}

// splitPathOperand splits a path operand into its components without cleaning.
// e.g. "./file/../dev" → [".", "file", "..", "dev"].
func splitPathOperand(path string) []string {
	if path == "" || path == "/" {
		return nil
	}
	abs := filepath.IsAbs(path)
	parts := strings.Split(path, string(filepath.Separator))
	var result []string
	for _, p := range parts {
		if p == "" {
			continue
		}
		result = append(result, p)
	}
	if abs && len(result) == 0 {
		return nil
	}
	return result
}

func cdStatErrorReason(err error) string {
	if msg, ok := posixErrorText(err); ok {
		return msg
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "No such file or directory"
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		err = unwrapped
	}
	msg := err.Error()
	if msg == "" {
		return msg
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

func (r *Runner) cdpath(ctx context.Context, path string) (string, bool, bool) {
	if cdpathSkipMode(path, runtime.GOOS == "windows") {
		return "", false, false
	}
	cdpath := r.envGet("CDPATH")
	if cdpath == "" {
		return "", false, false
	}
	for _, elem := range strings.Split(cdpath, ":") {
		base := elem
		if base == "" {
			base = "."
		}
		candidate := filepath.Clean(joinNoClean(r.absPath(base), path))
		info, err := r.stat(ctx, candidate)
		if err == nil && info.IsDir() && r.access(ctx, candidate, access_X_OK) == nil {
			printPath := elem != ""
			return r.cdpathLogical(base, path, candidate), printPath, true
		}
	}
	return "", false, false
}

// cdpathSkipMode reports whether an operand bypasses the CDPATH search,
// which is bash's absolute_pathname: an absolute path, "." or "..", or one
// beginning with "./" or "../". The test must use the shell's own notion of
// absolute, not [filepath.IsAbs]: on Windows the latter is false for "/",
// so `cd /` under a CDPATH of ".:/tmp" matched the "." element and silently
// became `cd .` — builtins1.sub then saw the wrong $OLDPWD and `cd -`
// printed the tests directory instead of "/".
func cdpathSkipMode(path string, windows bool) bool {
	if path == "" || shellPathAbsMode(path, windows) {
		return true
	}
	rest, ok := strings.CutPrefix(path, "..")
	if !ok {
		rest, ok = strings.CutPrefix(path, ".")
	}
	if !ok {
		return false
	}
	// "." and ".." themselves, and anything below them.
	return rest == "" || rest[0] == '/' || (windows && rest[0] == '\\')
}

// cdpathLogical is the spelling a CDPATH hit hands to cd. The search itself
// runs on native paths (r.absPath resolves a mounted element like /tmp to
// the directory it lives in), but cd must receive the shell's own spelling:
// both the path `cd` echoes for a CDPATH hit and the PWD it records come
// from this operand, and builtins1.sub wants /tmp/bash-dir-a rather than the
// native temp directory the /tmp mount points at. On Unix, where the two
// spellings coincide, the resolved path is returned unchanged.
func (r *Runner) cdpathLogical(base, path, candidate string) string {
	if runtime.GOOS != "windows" {
		return candidate
	}
	return cdpathLogicalMode(pathconv.CurrentMounts(), r.logicalDir(), base, path, candidate, true)
}

func cdpathLogicalMode(m *pathconv.Mounts, cur, base, path, candidate string, windows bool) string {
	if !windows {
		return candidate
	}
	switch {
	case posixSpelled(base):
		// /tmp + bash-dir-a -> /tmp/bash-dir-a.
		return canonPosixPath(base + "/" + path)
	case !nativeSpelled(base) && cur != "" && posixSpelled(cur):
		// A relative element (".", "sub") resolves against the logical dir.
		return canonPosixPath(cur + "/" + base + "/" + path)
	}
	// A natively spelled CDPATH element (C:\tmp) maps back through the mounts.
	return pathconv.FromOSMountsMode(m, candidate, true)
}

func absPath(dir, path string) string {
	if path == "" {
		return ""
	}
	if _, ok := devTTYStat(path); ok {
		// The synthetic Windows /dev/tty (devtty_windows.go) is answered by
		// the stat handler by its POSIX spelling; joined onto the cwd it
		// would become C:\…\dev\tty and `test -c /dev/tty` would say no.
		return path
	}
	path = shellPathJoinAbs(dir, path)
	return filepath.Clean(path) // TODO: this clean is likely unnecessary
}

func (r *Runner) absPath(path string) string {
	return absPath(r.Dir, path)
}

// flagParser is used to parse builtin flags.
//
// It's similar to the getopts implementation, but with some key differences.
// First, the API is designed for Go loops, making it easier to use directly.
// Second, it doesn't require the awkward ":ab" syntax that getopts uses.
// Third, it supports "-a" flags as well as "+a".
type flagParser struct {
	current   string
	remaining []string
}

func (p *flagParser) more() bool {
	if p.current != "" {
		// We're still parsing part of "-ab".
		return true
	}
	if len(p.remaining) == 0 {
		// Nothing left.
		p.remaining = nil
		return false
	}
	arg := p.remaining[0]
	if arg == "--" {
		// We explicitly stop parsing flags.
		p.remaining = p.remaining[1:]
		return false
	}
	if len(arg) == 0 || (arg[0] != '-' && arg[0] != '+') {
		// The next argument is not a flag.
		return false
	}
	// More flags to come.
	return true
}

func (p *flagParser) flag() string {
	arg := p.current
	if arg == "" {
		arg = p.remaining[0]
		p.remaining = p.remaining[1:]
	} else {
		p.current = ""
	}
	if len(arg) > 2 {
		// We have "-ab", so return "-a" and keep "-b".
		p.current = arg[:1] + arg[2:]
		arg = arg[:2]
	}
	return arg
}

func (p *flagParser) value() string {
	// When the previous `flag()` call split a cluster like `-dX`,
	// the rest (`X`, prefixed with `-`) is sitting in `p.current`.
	// For value-taking flags, that's the value — consume it.
	if p.current != "" {
		// p.current is shaped like "-<rest>". Strip the leading "-"
		// to recover the original tail.
		v := p.current[1:]
		p.current = ""
		return v
	}
	if len(p.remaining) == 0 {
		return ""
	}
	arg := p.remaining[0]
	p.remaining = p.remaining[1:]
	return arg
}

func (p *flagParser) args() []string { return p.remaining }

type getopts struct {
	argidx  int
	runeidx int
}

func (g *getopts) next(optstr string, args []string) (opt rune, optarg string, done bool) {
	if len(args) == 0 || g.argidx >= len(args) {
		g.argidx = len(args)
		g.runeidx = 0
		return '?', "", true
	}
	arg := []rune(args[g.argidx])
	if len(arg) < 2 || arg[0] != '-' {
		return '?', "", true
	}
	// `--` is the end-of-options marker; consume it so OPTIND points
	// past it, matching bash.
	if string(arg) == "--" {
		g.argidx++
		g.runeidx = 0
		return '?', "", true
	}
	if arg[1] == '-' {
		return '?', "", true
	}

	opts := arg[1:]
	opt = opts[g.runeidx]
	hasRest := g.runeidx+1 < len(opts)

	i := strings.IndexRune(optstr, opt)
	// A colon in optstr controls diagnostics or marks the preceding option
	// as requiring an argument; it is never an option character itself.
	if opt == ':' || i < 0 {
		// invalid option — advance past this letter so we don't loop forever
		if hasRest {
			g.runeidx++
		} else {
			g.argidx++
			g.runeidx = 0
		}
		return '?', string(opt), false
	}

	if i+1 < len(optstr) && optstr[i+1] == ':' {
		// Option requires an argument. If there's content remaining in
		// the current cluster (e.g. `-bbval` for option `b`), that's
		// the value. Otherwise consume the next arg.
		if hasRest {
			optarg = string(opts[g.runeidx+1:])
			g.argidx++
			g.runeidx = 0
		} else {
			g.argidx++
			g.runeidx = 0
			if g.argidx >= len(args) {
				return ':', string(opt), false
			}
			optarg = args[g.argidx]
			g.argidx++
		}
		return opt, optarg, false
	}

	if hasRest {
		g.runeidx++
	} else {
		g.argidx++
		g.runeidx = 0
	}
	return opt, optarg, false
}

// optStatusText returns a shell option's status text display
func (r *Runner) optStatusText(status bool) string {
	if status {
		return "on"
	}
	return "off"
}

// printSignalList prints `kill -l` / `trap -l` from the platform signal
// table (sortedSignalEntries), so the listing, `kill -l N` and $BASH_TRAPSIG
// all agree on one numbering — Linux, Darwin/BSD and the Cygwin/MSYS-numbered
// Windows table alike.
func (r *Runner) printSignalList(posix bool) {
	sorted := sortedSignalEntries()
	entries := make([]signalListEntry, 0, len(sorted))
	for _, e := range sorted {
		num, ok := signalNumber(e.Sig)
		if !ok {
			continue
		}
		entries = append(entries, signalListEntry{Num: num, Name: e.Name})
	}
	if runtime.GOOS != "windows" && !posix {
		r.outf("%s", formatSignalListTabs(entries))
	} else {
		r.outf("%s", formatSignalList(entries, posix))
	}
}

func signalByNamePosix(name string, posix bool) (killSig, bool) {
	if posix && strings.HasPrefix(strings.ToUpper(name), "SIG") {
		return defaultTermSignal, false
	}
	return signalByName(name)
}

func parseSignalSpecPosix(spec string, posix bool) (killSig, bool) {
	if n, err := strconv.Atoi(spec); err == nil {
		sig, _, ok := signalByNumber(n)
		return sig, ok
	}
	return signalByNamePosix(spec, posix)
}

// normalizeSignal converts a signal specification to a canonical name.
// Accepts: "EXIT", "ERR", "DEBUG", "RETURN", "INT", "SIGINT", "2", etc.
// Returns "" if the signal is not recognized.
func normalizeSignal(s string) string {
	s = strings.ToUpper(s)
	s = strings.TrimPrefix(s, "SIG")
	// Pseudo-signals
	switch s {
	case "EXIT", "ERR", "DEBUG", "RETURN":
		return s
	}
	// Check by name
	if _, ok := signalByName(s); ok {
		return s
	}
	// Check by number — use the full platform signal table (signalByNumber),
	// not the legacy 0–15 signalNames map, so trap accepts every signal kill
	// does (e.g. 28=WINCH, 29=IO, 30=PWR, 31=SYS on Linux).
	if n, err := strconv.Atoi(s); err == nil {
		if _, name, ok := signalByNumber(n); ok {
			return name
		}
	}
	return ""
}
