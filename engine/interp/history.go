package interp

// Bash-compatible command history for the `history` and `fc` builtins.
//
// Bash records history at the *reader* level: every line the parser
// consumes while `set -o history` is on becomes a history entry (joined
// with `; `/` `/newline separators for multi-line commands, comments
// included), filtered through HISTCONTROL/HISTIGNORE and stifled to
// HISTSIZE. This interpreter executes a fully-parsed AST instead of
// reading lines, so we emulate the reader: when history is enabled we
// re-parse the script source (keeping comments) into a line-ordered
// "timeline" of would-be history entries, and every builtin dispatch
// advances a cursor through that timeline up to the builtin's source
// line. This matches bash's observable behavior for scripts because all
// lines up to the currently-executing command have been "read".
//
// The state is a process-wide singleton rather than a Runner field so
// that subshells (which are separate Runner copies in this interpreter)
// share the history list the way bash subshells inherit it. It only
// activates once a script turns on `set -o history`, so unrelated
// Runners in the same process are unaffected.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pattern"
)

// histGroup is one would-be history entry from the source timeline.
type histGroup struct {
	startLine int
	endLine   int
	text      string
	lines     []string // raw physical source lines (for per-line expansion)
	hdoc      bool
}

type histState struct {
	mu       sync.Mutex
	active   atomic.Bool  // fast-path gate for histSync
	replay   atomic.Int32 // >0 while re-executing an expanded line
	cmdSubst atomic.Int32 // >0 while evaluating a command/process substitution body

	enabled bool // `set -o history`
	expand  bool // `set -H` / `set -o histexpand`
	posix   bool // `set -o posix` (affects history-expansion quoting rules)

	srcName     string
	timeline    []histGroup
	next        int          // next unconsumed timeline group
	quotedBreak map[int]bool // line L: break between L and L+1 is inside quotes
	noExpand    map[int]bool // physical lines read mid-parse (cmdsubst/heredoc continuation): no history expansion

	// Reader-emulation suppression: the physical line range whose timeline
	// group was history-expanded (or print-only/error). Commands on those
	// lines must not run their original AST — histSync already executed the
	// expansion. Covers extra statements split onto the same line. The lower
	// bound (suppressFromLine) keeps this from catching commands that run at
	// unrelated line numbers, such as the freshly-parsed body of `eval`
	// (which starts at line 1).
	suppressFromLine    int
	suppressThroughLine int

	// History-expansion engine state (bash globals), persistent across
	// expansions: the last `?str?` search and the last `:s/old/new/`.
	searchString string
	searchMatch  string
	substLHS     string
	substRHS     string

	list []string
	base int // history number of list[0] (bash history_base)

	linesThisSession int // entries added this session (for history -a)
	linesInFile      int // file lines already consumed (for history -n)
	loaded           bool

	lastAdded  bool // bash hist_last_line_added
	lastPushed bool // bash hist_last_line_pushed
}

var shellHist = &histState{base: 1}

// histReset restores the singleton to its initial state. Used by tests.
func histReset() {
	h := shellHist
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active.Store(false)
	h.enabled, h.expand = false, false
	h.srcName, h.timeline, h.next = "", nil, 0
	h.quotedBreak = nil
	h.noExpand = nil
	h.suppressFromLine, h.suppressThroughLine = 0, 0
	h.replay.Store(0)
	h.cmdSubst.Store(0)
	h.searchString, h.searchMatch, h.substLHS, h.substRHS = "", "", "", ""
	h.list, h.base = nil, 1
	h.linesThisSession, h.linesInFile, h.loaded = 0, 0, false
	h.lastAdded, h.lastPushed = false, false
}

// histDesignator reports whether a command word should be treated as a
// history event designator (`!!`, `!n`, `!prefix`) rather than a
// command name. Mirrors the reader gate in histSync: the `!` event
// char is only special once `set -o history` and `set -H` are both on,
// and never before a blank, `=` or `(`.
func histDesignator(name string) bool {
	h := shellHist
	if !h.active.Load() {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled || !h.expand {
		return false
	}
	// `!`-led event designators and `^`-led quick substitutions are resolved
	// by the history engine at dispatch, not run as external commands.
	if len(name) >= 2 && name[0] == '!' && !strings.ContainsRune(" \t=(", rune(name[1])) {
		return true
	}
	if len(name) >= 2 && name[0] == '^' {
		return true
	}
	return false
}

// histSync advances the reader-emulation cursor up to the given source
// position, recording every timeline entry bash's reader would have
// consumed by now. Called at the top of every builtin dispatch.
// histSync advances the reader-emulation cursor up to the given source
// position. It records every timeline entry bash's reader would have
// consumed by now, applying history expansion, echoing expansions to
// stderr and executing them. It returns true when the command on this
// physical line was history-expanded (or was a print-only/error/already-
// consumed expansion), meaning the caller must not run the original
// statement. Called from [Runner.call] before every command dispatch.
func (r *Runner) histSync(ctx context.Context, pos syntax.Pos) bool {
	h := shellHist
	if !h.active.Load() {
		return false
	}
	// While re-executing the result of a history expansion, the reader has
	// already consumed these lines: don't record, expand or suppress again.
	if h.replay.Load() > 0 {
		return false
	}
	// Commands inside a command/process substitution are part of the
	// enclosing reader line, never read as separate history lines. Bash
	// history-expands the whole outer line (including the substitution
	// body) as one unit, then parses and runs it once. So while evaluating
	// a substitution body, do nothing here: the outer command's histSync
	// will record and replay the entire line. This avoids triggering a
	// nested whole-line replay from inside the substitution's own output
	// capture.
	if h.cmdSubst.Load() > 0 {
		return false
	}
	line := int(pos.Line())
	if line <= 0 {
		return false
	}
	h.mu.Lock()
	if !h.enabled {
		h.mu.Unlock()
		return false
	}
	h.posix = r.opts[optPosix]
	// A command on a physical line already consumed and executed as part of
	// an expanded/print-only group on the same line must not run again.
	suppress := h.suppressThroughLine > 0 && line >= h.suppressFromLine && line <= h.suppressThroughLine

	type pending struct {
		echoes   []string
		errs     []histLineErr
		execText string
		status   int
	}
	var pends []pending
	for h.next < len(h.timeline) && h.timeline[h.next].startLine <= line {
		g := h.timeline[h.next]
		h.next++
		isCurrent := line >= g.startLine && line <= g.endLine
		echoes, errs, execText, recordText, status := h.expandGroup(g)
		h.addLine(r, recordText)
		if isCurrent && status != histUnchanged {
			suppress = true
			h.suppressFromLine = g.startLine
			if g.endLine > h.suppressThroughLine {
				h.suppressThroughLine = g.endLine
			}
		}
		pends = append(pends, pending{echoes, errs, execText, status})
	}
	h.mu.Unlock()

	// Bash echoes history-expanded lines to stderr as it reads them, and
	// then executes the result; print-only (`:p`) and erroring expansions
	// are echoed but not executed.
	for _, p := range pends {
		for _, e := range p.errs {
			r.errf("%s%s\n", r.bashErrPrefixLine(e.line), e.msg)
		}
		for _, e := range p.echoes {
			r.errf("%s\n", e)
		}
		if p.status == histChanged {
			h.replay.Add(1)
			r.histRunString(ctx, p.execText)
			h.replay.Add(-1)
		}
	}
	return suppress
}

// histSetEnabled flips `set -o history` state. On the off→on transition
// it builds the source timeline and, like bash's set.def, loads HISTFILE
// if no history lines have been added this session.
func (r *Runner) histSetEnabled(on bool, pos syntax.Pos) {
	h := shellHist
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = on
	if !on {
		return
	}
	h.active.Store(true)
	if h.timeline == nil && r.filename != "" {
		if src, err := os.ReadFile(r.absPath(r.filename)); err == nil {
			h.srcName = r.filename
			h.timeline, h.quotedBreak, h.noExpand = buildHistTimeline(src)
			// Skip everything up to and including the enabling line:
			// those lines were read while history was still off.
			for h.next < len(h.timeline) && h.timeline[h.next].startLine <= int(pos.Line()) {
				h.next++
			}
		}
	}
	if !h.loaded && h.linesThisSession == 0 {
		h.loaded = true
		hf := r.envGet("HISTFILE")
		if hf == "" && r.opts[optExpandAliases] {
			// Bash only loads $HISTFILE; it never invents a default path in
			// `set -o history`. The default history file is assigned to
			// HISTFILE during *interactive* shell startup. A non-interactive
			// script that turns on history with HISTFILE unset must not pull
			// in the user's saved history (that pollutes scripts like
			// history2.sub / history3.sub). Only fall back to the bashy
			// default when running interactively (optExpandAliases is the
			// interactive signal set by interp.Interactive).
			if home, err := os.UserHomeDir(); err == nil {
				hf = filepath.Join(home, ".bashy_history")
			}
		}
		if hf != "" {
			if entries, nlines, err := readHistFile(r.absPath(hf)); err == nil {
				for _, e := range entries {
					h.appendEntry(r, e)
				}
				h.linesInFile = nlines
			}
		}
	}
}

func (r *Runner) histSetExpand(on bool) {
	h := shellHist
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expand = on
	if on {
		h.active.Store(true)
	}
}

// EnableInteractiveHistory enables the reader-level history used by the
// history and fc builtins. Interactive front-ends call this once before their
// read-eval loop; non-interactive embedders retain the existing opt-in
// behavior through `set -o history`.
func (r *Runner) EnableInteractiveHistory() {
	// Interactive front-ends need history before their first call to Run.
	// Initialise the runner's writable environment now so HISTFILE,
	// HISTCONTROL, and HISTIGNORE can be resolved safely while recording the
	// first input line. Run would otherwise perform this Reset itself.
	if r.writeEnv == nil {
		r.Reset()
	}
	r.histSetEnabled(true, syntax.Pos{})
}

// RecordInteractiveHistory records one complete input unit before it is
// executed. Bash's reader adds the current line before dispatch, which lets fc
// exclude its own invocation while still seeing every preceding command.
func (r *Runner) RecordInteractiveHistory(input string) {
	h := shellHist
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled {
		return
	}
	h.addLine(r, input)
}

// addLine records one reader line, applying HISTCONTROL and HISTIGNORE,
// and updates hist_last_line_added.
func (h *histState) addLine(r *Runner, text string) {
	h.lastPushed = false
	if strings.TrimSpace(text) == "" {
		return
	}
	ignoreSpace, ignoreDups, eraseDups := false, false, false
	for _, w := range strings.Split(r.envGet("HISTCONTROL"), ":") {
		switch w {
		case "ignorespace":
			ignoreSpace = true
		case "ignoredups":
			ignoreDups = true
		case "ignoreboth":
			ignoreSpace, ignoreDups = true, true
		case "erasedups":
			eraseDups = true
		}
	}
	if ignoreSpace && (text[0] == ' ' || text[0] == '\t') {
		h.lastAdded = false
		return
	}
	if ignoreDups && len(h.list) > 0 && h.list[len(h.list)-1] == text {
		h.lastAdded = false
		return
	}
	if h.histIgnored(r, text) {
		h.lastAdded = false
		return
	}
	if eraseDups {
		kept := h.list[:0]
		for _, e := range h.list {
			if e != text {
				kept = append(kept, e)
			}
		}
		h.list = kept
	}
	h.appendEntry(r, text)
	h.lastAdded = true
	h.linesThisSession++
}

// histIgnored reports whether HISTIGNORE filters out the line. The `&`
// pattern matches the previous history entry.
func (h *histState) histIgnored(r *Runner, text string) bool {
	hi := r.envGet("HISTIGNORE")
	if hi == "" {
		return false
	}
	for _, pat := range strings.Split(hi, ":") {
		if pat == "" {
			continue
		}
		if pat == "&" {
			if len(h.list) > 0 && h.list[len(h.list)-1] == text {
				return true
			}
			continue
		}
		expr, err := pattern.Regexp(pat, pattern.EntireString)
		if err != nil {
			continue
		}
		if rx, err := regexp.Compile(expr); err == nil && rx.MatchString(text) {
			return true
		}
	}
	return false
}

// appendEntry adds an entry and stifles the list to HISTSIZE, advancing
// the base number for entries dropped off the front (bash history_base).
func (h *histState) appendEntry(r *Runner, text string) {
	h.list = append(h.list, text)
	if size, ok := h.histSize(r); ok {
		if size <= 0 {
			h.base += len(h.list)
			h.list = nil
			return
		}
		for len(h.list) > size {
			h.list = h.list[1:]
			h.base++
		}
	}
}

func (h *histState) histSize(r *Runner) (int, bool) {
	vr := r.lookupVar("HISTSIZE")
	if !vr.IsSet() {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(vr.String()))
	if err != nil || n < 0 {
		return 0, false // bash treats invalid/negative as unlimited
	}
	return n, true
}

// expandDesignator implements the small subset of history expansion the
// builtins need: `!!`, `!-n`, `!n` and `!prefix`, optionally followed by
// more text on the line.
func (h *histState) expandDesignator(line string) (string, bool) {
	rest := line[1:]
	var desig, tail string
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		desig, tail = rest[:i], rest[i:]
	} else {
		desig = rest
	}
	var entry string
	switch {
	case desig == "!":
		if len(h.list) == 0 {
			return "", false
		}
		entry = h.list[len(h.list)-1]
	case strings.HasPrefix(desig, "-"):
		n, err := strconv.Atoi(desig)
		if err != nil || -n > len(h.list) || n == 0 {
			return "", false
		}
		entry = h.list[len(h.list)+n]
	default:
		if n, err := strconv.Atoi(desig); err == nil {
			idx := n - h.base
			if idx < 0 || idx >= len(h.list) {
				return "", false
			}
			entry = h.list[idx]
			break
		}
		for j := len(h.list) - 1; j >= 0; j-- {
			if strings.HasPrefix(h.list[j], desig) {
				entry = h.list[j]
				break
			}
		}
		if entry == "" {
			return "", false
		}
	}
	return entry + tail, true
}

// ---- bash-compatible history expansion engine ----
//
// This is a faithful port of GNU readline's history_expand
// (lib/readline/histexpand.c) plus bash's overrides from bashhist.c,
// covering event designators (`!!`, `!n`, `!-n`, `!str`, `!?str?`), the
// quick substitution `^old^new`, word designators (`:0 :n :$ :* :% :^`,
// ranges `x-y`, `x*`, `x-`) and modifiers (`:h :t :r :e :p :q :x`,
// `:s/old/new/`, `:gs`, `:&`, `:g&`).

// history_expand status codes.
const (
	histUnchanged = 0  // no expansion took place
	histChanged   = 1  // expansion occurred; run the result
	histPrintOnly = 2  // a `:p` modifier: print but do not execute
	histError     = -1 // expansion error; output holds the message
)

const (
	histExpandChar  = '!'
	histSubstChar   = '^'
	histCommentChar = '#'
)

// Character classes, matching readline's defaults with bash's overrides
// (bash_history_no_expand_chars, history_search_delimiter_chars, …).
const (
	histNoExpandChars    = " \t\n\r=;&|()<>"
	histWordDelims       = " \t\n;&()|<>"
	histEventDelims      = "^$*%-"
	histSearchDelims     = ";&()|<>"
	histQuoteChars       = "\"'`"
	histSlashifyInQuotes = "\\`\"$"
)

// histLineErr records a per-physical-line expansion error for deferred
// reporting (bash echoes these as internal errors).
type histLineErr struct {
	line int
	msg  string
}

// word-specifier outcomes.
const (
	histWordNone  = 0 // no word specifier: use the whole event
	histWordValid = 1
	histWordError = 2
)

func histMember(c byte, set string) bool { return c != 0 && strings.IndexByte(set, c) >= 0 }
func histWhitespace(c byte) bool         { return c == ' ' || c == '\t' }
func histFieldDelim(c byte) bool         { return histWhitespace(c) || c == '\n' }
func histDigit(c byte) bool              { return c >= '0' && c <= '9' }

func histByteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func histSafeSlice(s string, i int) string {
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		return ""
	}
	return s[i:]
}

// expandGroup history-expands every physical line of one timeline group,
// returning the per-line stderr echoes, deferred errors, the text to
// execute, the text to record, and the overall status.
func (h *histState) expandGroup(g histGroup) (echoes []string, errs []histLineErr, execText, recordText string, status int) {
	expanded := make([]string, len(g.lines))
	overall := histUnchanged
	for i, ln := range g.lines {
		// Lines read by the parser mid-token (here-document bodies and
		// continuation lines inside a command substitution) are not
		// history-expanded by bash — only the line that opens the
		// construct is. Keep them verbatim.
		if h.noExpand[g.startLine+i] {
			expanded[i] = ln
			continue
		}
		res, st := h.historyExpand(ln)
		switch st {
		case histError:
			errs = append(errs, histLineErr{line: g.startLine + i, msg: res})
			expanded[i] = ln
			overall = histError
		case histPrintOnly:
			echoes = append(echoes, res)
			expanded[i] = res
			if overall != histError {
				overall = histPrintOnly
			}
		case histChanged:
			echoes = append(echoes, res)
			expanded[i] = res
			if overall == histUnchanged {
				overall = histChanged
			}
		default:
			expanded[i] = res
		}
	}
	execText = strings.Join(expanded, "\n")
	recordText = joinHistLines(expanded, g.hdoc, g.startLine, h.quotedBreak)
	return echoes, errs, execText, recordText, overall
}

// historyExpand expands one source line, returning the expanded line and a
// status code. The quoting state is assumed to start neutral, which holds
// for the start of every physical line bash's reader feeds it.
func (h *histState) historyExpand(hstring string) (string, int) {
	l := len(hstring)
	var s string

	// The quick substitution `^old^new` is shorthand for `!!:s^old^new`.
	if l > 0 && hstring[0] == histSubstChar {
		s = "!!:s" + hstring
	} else {
		s = hstring
		// First pass: is there a real expansion character at all?
		dquote := false
		found := false
		for i := 0; i < len(s); i++ {
			cc := histByteAt(s, i+1)
			if s[i] == histCommentChar && !dquote &&
				(i == 0 || histMember(s[i-1], histWordDelims)) {
				break
			} else if s[i] == histExpandChar {
				if cc == 0 || histMember(cc, histNoExpandChars) {
					continue
				} else if dquote && cc == '"' {
					continue
				} else if h.inhibitExpansion(s, i) {
					continue
				}
				found = true
				break
			} else if dquote && s[i] == '\\' && cc == '"' {
				i++
			} else if s[i] == '"' {
				dquote = !dquote
			} else if !dquote && s[i] == '\'' {
				flag := i > 0 && s[i-1] == '$'
				i++
				i = histExtractSingleQuoted(s, i, flag)
				if i >= l {
					break
				}
			} else if s[i] == '\\' {
				if cc == '\'' || cc == histExpandChar {
					i++
				}
			}
		}
		if !found {
			return s, histUnchanged
		}
	}

	// Second pass: build the expanded result.
	var result []byte
	dquote := false
	squote := false
	onlyPrinting := 0
	modified := 0
	passc := false
	for i := 0; i < len(s); i++ {
		tchar := s[i]
		if passc {
			passc = false
			result = append(result, tchar)
			continue
		}
		if tchar == histExpandChar {
			cc := histByteAt(s, i+1)
			if cc == 0 || histMember(cc, histNoExpandChars) || (dquote && cc == '"') ||
				h.inhibitExpansion(s, i) {
				result = append(result, tchar)
				continue
			}
			qc := byte(0)
			if squote {
				qc = '\''
			} else if dquote {
				qc = '"'
			}
			temp, eindex, st := h.expandInternal(s, i, qc, string(result))
			if st < 0 {
				return temp, histError
			}
			modified++
			result = append(result, temp...)
			if st == 1 {
				onlyPrinting++
			}
			i = eindex
			continue
		}
		if tchar == histCommentChar {
			if !dquote && (i == 0 || histMember(s[i-1], histWordDelims)) {
				result = append(result, s[i:]...)
				i = len(s)
			} else {
				result = append(result, tchar)
			}
			continue
		}
		switch tchar {
		case '\\':
			passc = true
			result = append(result, tchar)
		case '"':
			dquote = !dquote
			result = append(result, tchar)
		case '\'':
			if squote {
				squote = false
				result = append(result, tchar)
			} else if !dquote {
				flag := i > 0 && s[i-1] == '$'
				quote := i
				i++
				i = histExtractSingleQuoted(s, i, flag)
				end := i + 1
				if end > len(s) {
					end = len(s)
				}
				result = append(result, s[quote:end]...)
			} else {
				result = append(result, tchar)
			}
		default:
			result = append(result, tchar)
		}
	}

	out := string(result)
	if onlyPrinting > 0 {
		return out, histPrintOnly
	}
	if modified > 0 {
		return out, histChanged
	}
	return out, histUnchanged
}

// histExtractSingleQuoted advances past a single-quoted string, returning
// the index of the closing quote (or len(s) if unterminated). When
// allowBackslash is set, backslash escapes the closing quote ($'...').
func histExtractSingleQuoted(s string, i int, allowBackslash bool) int {
	for i < len(s) && s[i] != '\'' {
		if allowBackslash && s[i] == '\\' && i+1 < len(s) {
			i++
		}
		i++
	}
	return i
}

// inhibitExpansion ports bash_history_inhibit_expansion's simple cases: a
// `!` is not an expansion inside a glob `[...!...]`, an indirect `${!name}`,
// the `$!` parameter, or an extended-glob `!(...)`.
func (h *histState) inhibitExpansion(s string, i int) bool {
	if i > 0 && s[i-1] == '[' && strings.IndexByte(histSafeSlice(s, i+1), ']') >= 0 {
		return true
	}
	if i > 1 && s[i-1] == '{' && s[i-2] == '$' && strings.IndexByte(histSafeSlice(s, i+1), '}') >= 0 {
		return true
	}
	if i > 1 && s[i-1] == '$' {
		return true
	}
	if i > 1 && histByteAt(s, i+1) == '(' && strings.IndexByte(histSafeSlice(s, i+2), ')') >= 0 {
		return true
	}
	// Quote- and command/process-substitution awareness, ported from bash's
	// bash_history_inhibit_expansion: a `!` is not a real history expansion
	// when it lies inside a quoted string or a command/process substitution.
	// skip_to_histexp walks the line honoring those contexts and reports the
	// index of the next genuine `!`. If, scanning from the start, the first
	// genuine `!` lands past position i, then the `!` at i was inside a
	// skipped context and must be inhibited.
	t := histSkipToHistexp(s, 0, h.posix)
	for t < i {
		t = histSkipToHistexp(s, t+1, h.posix)
	}
	return t > i
}

// histSkipToHistexp scans s from start and returns the index of the next
// character bash would treat as a genuine history-expansion char (`!`),
// honoring backslashes, single/double quotes, backquotes, and command/
// process substitutions exactly as bash's skip_to_histexp does. It returns
// len(s) when no genuine `!` remains. In posix mode a double-quoted string
// is skipped whole; otherwise `"` merely toggles the quoting state (so a
// `!` directly inside double quotes is still genuine unless followed by `"`).
func histSkipToHistexp(s string, start int, posix bool) int {
	passNext := false
	backq := false
	dquote := false
	comsub := 0
	oldDquote := false
	i := start
	for i < len(s) {
		c := s[i]
		switch {
		case passNext:
			passNext = false
			i++
		case c == '\\':
			passNext = true
			i++
		case backq && c == '`':
			backq = false
			dquote = oldDquote
			i++
		case c == '`':
			backq = true
			oldDquote = dquote
			dquote = false
			i++
		case dquote && c == histExpandChar && histByteAt(s, i+1) == '"':
			i++
		case c == histExpandChar:
			return i
		case dquote && c == '\'':
			i++
		case c == '\'':
			i = histExtractSingleQuoted(s, i+1, false)
			if i < len(s) {
				i++
			}
		case posix && c == '"':
			i = histSkipDoubleQuoted(s, i+1)
		case c == '"':
			dquote = !dquote
			i++
		case (c == '$' || c == '<' || c == '>') && histByteAt(s, i+1) == '(' && histByteAt(s, i+2) != '(':
			if histByteAt(s, i+2) == 0 {
				return i + 2
			}
			i += 2
			comsub++
			oldDquote = dquote
			dquote = false
		case comsub > 0 && c == ')':
			comsub--
			dquote = oldDquote
			i++
		default:
			i++
		}
	}
	return i
}

// histSkipDoubleQuoted skips a double-quoted string starting just after the
// opening quote (s[i-1] == '"'), returning the index past the closing quote
// (or len(s) if unterminated). Backslash escapes and nested command/process
// substitutions and backquotes are honored so their contents are opaque.
func histSkipDoubleQuoted(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '`':
			i++
			for i < len(s) && s[i] != '`' {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		case '$':
			if histByteAt(s, i+1) == '(' {
				i = histSkipParenNest(s, i+2)
				continue
			}
			i++
		case '"':
			return i + 1
		default:
			i++
		}
	}
	return i
}

// histSkipParenNest skips a parenthesized command substitution body starting
// just after `$(`, returning the index past the matching `)`.
func histSkipParenNest(s string, i int) int {
	depth := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return i
}

// historyGet returns the entry with absolute history number `which`.
func (h *histState) historyGet(which int) (string, bool) {
	idx := which - h.base
	if idx < 0 || idx >= len(h.list) {
		return "", false
	}
	return h.list[idx], true
}

// getHistoryEvent resolves an event designator beginning at s[i] (the `!`),
// returning the matched entry line and the index just past the designator.
func (h *histState) getHistoryEvent(s string, i int, qc byte) (string, bool, int) {
	i++ // past `!`
	sign := 1

	if histByteAt(s, i) == histExpandChar { // !!
		i++
		line, ok := h.historyGet(h.base + len(h.list) - 1)
		return line, ok, i
	}
	if histByteAt(s, i) == '-' && histDigit(histByteAt(s, i+1)) {
		sign = -1
		i++
	}
	if histDigit(histByteAt(s, i)) {
		which := 0
		for histDigit(histByteAt(s, i)) {
			which = which*10 + int(s[i]-'0')
			i++
		}
		if sign < 0 {
			which = len(h.list) + h.base - which
		}
		line, ok := h.historyGet(which)
		return line, ok, i
	}

	substringOkay := false
	if histByteAt(s, i) == '?' {
		substringOkay = true
		i++
	}
	localIndex := i
	for i < len(s) {
		c := s[i]
		if !substringOkay &&
			(histWhitespace(c) || c == ':' ||
				(i > localIndex && c == '-') ||
				(c != '-' && histMember(c, histEventDelims)) ||
				histMember(c, histSearchDelims) ||
				(qc != 0 && c == qc)) {
			break
		}
		if c == '\n' {
			break
		}
		if substringOkay && c == '?' {
			break
		}
		i++
	}
	which := s[localIndex:i]
	if substringOkay && histByteAt(s, i) == '?' {
		i++
	}
	newIndex := i

	temp := which
	if temp == "" && substringOkay {
		if h.searchString != "" {
			temp = h.searchString
		} else {
			return "", false, newIndex
		}
	}
	if substringOkay {
		entry, charIdx := h.histSearchSubstring(temp)
		if entry < 0 {
			return "", false, newIndex
		}
		h.searchString = temp
		h.searchMatch = h.historyFindWord(h.list[entry], charIdx)
		return h.list[entry], true, newIndex
	}
	entry := h.histSearchPrefix(temp)
	if entry < 0 {
		return "", false, newIndex
	}
	return h.list[entry], true, newIndex
}

// histSearchSubstring finds the most recent entry containing temp; within
// it, the last occurrence (readline searches lines backwards), returning
// the entry index and the character offset of the match.
func (h *histState) histSearchSubstring(temp string) (int, int) {
	for e := len(h.list) - 1; e >= 0; e-- {
		if idx := strings.LastIndex(h.list[e], temp); idx >= 0 {
			return e, idx
		}
	}
	return -1, -1
}

// histSearchPrefix finds the most recent entry starting with temp.
func (h *histState) histSearchPrefix(temp string) int {
	for e := len(h.list) - 1; e >= 0; e-- {
		if strings.HasPrefix(h.list[e], temp) {
			return e
		}
	}
	return -1
}

// historyFindWord returns the word of `line` containing char index ind.
func (h *histState) historyFindWord(line string, ind int) string {
	words, wind := histTokenizeInternal(line, ind)
	if wind < 0 || wind >= len(words) {
		return ""
	}
	return words[wind]
}

// expandInternal handles one `!` expansion: the event, an optional word
// designator, and any trailing modifiers. It mirrors
// history_expand_internal, returning the expansion, the end index, and a
// status (0 ok, 1 print-only, -1 error with the message in the string).
func (h *histState) expandInternal(s string, start int, qc byte, current string) (string, int, int) {
	i := start
	var event string
	var ok bool

	switch {
	case histMember(histByteAt(s, i+1), ":$*%^"): // !! implied
		i++
		event, ok, _ = h.getHistoryEvent("!!", 0, 0)
	case histByteAt(s, i+1) == '#': // !# — the line so far
		i += 2
		event, ok = current, true
	default:
		event, ok, i = h.getHistoryEvent(s, i, qc)
	}
	if !ok {
		return histErrorMsg(s, start, i, "event not found"), i, -1
	}

	startingIndex := i
	wordResult, wordKind, ni := h.getWordSpecifier(s, event, i)
	i = ni
	if wordKind == histWordError {
		return histErrorMsg(s, startingIndex, i, "bad word specifier"), i, -1
	}
	temp := event
	if wordKind == histWordValid {
		temp = wordResult
	}

	wantQuotes := byte(0)
	printOnly := false
	startingIndex = i
	for histByteAt(s, i) == ':' {
		c := histByteAt(s, i+1)
		globally := 0
		byWords := false
		if c == 'g' || c == 'a' {
			globally = 1
			i++
			c = histByteAt(s, i+1)
		} else if c == 'G' {
			byWords = true
			i++
			c = histByteAt(s, i+1)
		}

		if c == 's' || c == '&' {
			nt, nidx, emsg := h.applySubstitution(s, i, c, temp, globally, byWords)
			if emsg != "" {
				return histErrorMsg(s, startingIndex, nidx, emsg), nidx, -1
			}
			temp = nt
			i = nidx
			continue
		}

		switch c {
		case 'q':
			wantQuotes = 'q'
		case 'x':
			wantQuotes = 'x'
		case 'p':
			printOnly = true
		case 't':
			if idx := strings.LastIndexByte(temp, '/'); idx >= 0 {
				temp = temp[idx+1:]
			}
		case 'h':
			if idx := strings.LastIndexByte(temp, '/'); idx >= 0 {
				temp = temp[:idx]
			}
		case 'r':
			if idx := strings.LastIndexByte(temp, '.'); idx >= 0 {
				temp = temp[:idx]
			}
		case 'e':
			if idx := strings.LastIndexByte(temp, '.'); idx >= 0 {
				temp = temp[idx:]
			}
		default:
			return histErrorMsg(s, i+1, i+2, "unrecognized history modifier"), i, -1
		}
		i += 2
	}
	i--

	switch wantQuotes {
	case 'q':
		temp = histSingleQuote(temp)
	case 'x':
		temp = histQuoteBreaks(temp)
	}
	st := 0
	if printOnly {
		st = 1
	}
	return temp, i, st
}

// getWordSpecifier parses a word designator from spec[idx] against the
// tokenized event `from`. It returns the selected text, an outcome
// (none/valid/error), and the new index.
func (h *histState) getWordSpecifier(spec, from string, idx int) (string, int, int) {
	i := idx
	expectingWordSpec := false
	if histByteAt(spec, i) == ':' {
		i++
		expectingWordSpec = true
	}
	if histByteAt(spec, i) == '%' {
		return h.searchMatch, histWordValid, i + 1
	}
	if histByteAt(spec, i) == '*' {
		r, _ := histArgExtract(1, '$', from)
		return r, histWordValid, i + 1
	}
	if histByteAt(spec, i) == '$' {
		r, found := histArgExtract('$', '$', from)
		if !found {
			return "", histWordError, i + 1
		}
		return r, histWordValid, i + 1
	}

	first := 0
	last := 0
	switch {
	case histByteAt(spec, i) == '-':
		first = 0
	case histByteAt(spec, i) == '^':
		first = 1
		i++
	case histDigit(histByteAt(spec, i)) && expectingWordSpec:
		for histDigit(histByteAt(spec, i)) {
			first = first*10 + int(spec[i]-'0')
			i++
		}
	default:
		// No valid word specifier. Like readline, leave the caller index
		// untouched (at the `:`) so a following modifier still sees it.
		return "", histWordNone, idx
	}

	switch {
	case histByteAt(spec, i) == '^' || histByteAt(spec, i) == '*':
		if spec[i] == '^' {
			last = 1
		} else {
			last = '$'
		}
		i++
	case histByteAt(spec, i) != '-':
		last = first
	default:
		i++
		switch {
		case histDigit(histByteAt(spec, i)):
			for histDigit(histByteAt(spec, i)) {
				last = last*10 + int(spec[i]-'0')
				i++
			}
		case histByteAt(spec, i) == '$':
			i++
			last = '$'
		case histByteAt(spec, i) == '^':
			i++
			last = 1
		default:
			last = -1 // x- omits the last word
		}
	}

	if last >= first || last == '$' || last < 0 {
		r, found := histArgExtract(first, last, from)
		if !found {
			return "", histWordError, i
		}
		return r, histWordValid, i
	}
	return "", histWordError, i
}

// histArgExtract joins words [first..last] of `line`. first/last may be
// '$' (last word) or negative (counting from the end), matching
// history_arg_extract.
func histArgExtract(first, last int, line string) (string, bool) {
	list, _ := histTokenizeInternal(line, -1)
	length := len(list)
	if last < 0 {
		last = length + last - 1
	}
	if first < 0 {
		first = length + first - 1
	}
	if last == '$' {
		last = length - 1
	}
	if first == '$' {
		first = length - 1
	}
	last++
	if first >= length || last > length || first < 0 || last < 0 || first > last {
		return "", false
	}
	return strings.Join(list[first:last], " "), true
}

// applySubstitution performs one `:s/old/new/` or `:&` modifier, mutating
// h.substLHS/substRHS, and returns the new temp, the index past the
// modifier, and an error message (empty on success).
func (h *histState) applySubstitution(s string, i int, c byte, temp string, globally int, byWords bool) (string, int, string) {
	if c == 's' {
		if i+2 >= len(s) {
			return temp, i + 2, "" // no delimiter: treated as a no-op
		}
		delimiter := s[i+2]
		i += 3
		lhs, ni, present := histGetSubstPattern(s, i, delimiter, false)
		i = ni
		if present {
			h.substLHS = lhs
		} else if h.substLHS == "" && h.searchString != "" {
			h.substLHS = h.searchString
		}
		rhs, ni2, _ := histGetSubstPattern(s, i, delimiter, true)
		i = ni2
		h.substRHS = rhs
		if h.substLHS != "" && strings.IndexByte(h.substRHS, '&') >= 0 {
			h.substRHS = histPostprocSubstRHS(h.substRHS, h.substLHS)
		}
	} else {
		i += 2
	}

	if len(h.substLHS) == 0 {
		return temp, i, "no previous substitution"
	}
	lhsLen := len(h.substLHS)
	lTemp := len(temp)
	if lhsLen > lTemp {
		return temp, i, "substitution failed"
	}

	si := 0
	we := 0
	failed := true
	for si+lhsLen <= lTemp {
		if byWords && si > we {
			for si < len(temp) && histFieldDelim(temp[si]) {
				si++
			}
			we = histTokenizeWord(temp, si)
		}
		if si+lhsLen <= len(temp) && temp[si:si+lhsLen] == h.substLHS {
			temp = temp[:si] + h.substRHS + temp[si+lhsLen:]
			failed = false
			if globally != 0 {
				si += len(h.substRHS) - 1
				lTemp = len(temp)
				si++
				continue
			} else if byWords {
				si = we
				lTemp = len(temp)
				si++
				continue
			}
			break
		}
		si++
	}
	if failed {
		return temp, i, "substitution failed"
	}
	return temp, i, ""
}

// histGetSubstPattern reads a substitution pattern up to `delimiter`,
// unescaping `\delimiter`. present is false for an empty lhs (so the
// previous pattern is reused).
func histGetSubstPattern(s string, i int, delimiter byte, isRHS bool) (string, int, bool) {
	si := i
	for si < len(s) && s[si] != delimiter {
		if s[si] == '\\' && histByteAt(s, si+1) == delimiter {
			si++
		}
		si++
	}
	present := si > i || isRHS
	var b []byte
	if present {
		for k := i; k < si; k++ {
			if s[k] == '\\' && histByteAt(s, k+1) == delimiter {
				k++
			}
			b = append(b, s[k])
		}
	}
	if si < len(s) {
		si++ // past delimiter
	}
	return string(b), si, present
}

// histPostprocSubstRHS replaces an unescaped `&` in the rhs with the lhs.
func histPostprocSubstRHS(rhs, lhs string) string {
	var b []byte
	for i := 0; i < len(rhs); i++ {
		if rhs[i] == '&' {
			b = append(b, lhs...)
			continue
		}
		if rhs[i] == '\\' && i+1 < len(rhs) && rhs[i+1] == '&' {
			i++
		}
		b = append(b, rhs[i])
	}
	return string(b)
}

// histSingleQuote wraps s in single quotes (the `:q` modifier).
func histSingleQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString(`'\''`)
		} else {
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// histQuoteBreaks quotes s so each whitespace run becomes a word boundary
// (the `:x` modifier).
func histQuoteBreaks(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			b.WriteString(`'\''`)
		case c == ' ' || c == '\t' || c == '\n':
			b.WriteByte('\'')
			b.WriteByte(c)
			b.WriteByte('\'')
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// histErrorMsg builds readline's "<offending text>: <reason>" message.
func histErrorMsg(s string, start, current int, emsg string) string {
	sub := ""
	if start < len(s) && current > start {
		end := current
		if end > len(s) {
			end = len(s)
		}
		sub = s[start:end]
	}
	return sub + ": " + emsg
}

// histTokenizeWord returns the index just past the token beginning at
// s[ind], splitting words exactly as history_tokenize_word does (so
// command/process substitutions and extended globs stay one word).
func histTokenizeWord(s string, ind int) int {
	i := ind
	var delimiter, delimopen byte
	nestdelim := 0

	if i < len(s) && histMember(s[i], "()\n") {
		return i + 1
	}
	if i < len(s) && histDigit(s[i]) {
		j := i
		for j < len(s) && histDigit(s[j]) {
			j++
		}
		if j >= len(s) {
			return j
		}
		if s[j] == '<' || s[j] == '>' {
			i = j
		} else {
			i = j
			goto getWord
		}
	}

	if i < len(s) && histMember(s[i], "<>;&|") {
		peek := histByteAt(s, i+1)
		if peek == s[i] {
			if peek == '<' && (histByteAt(s, i+2) == '-' || histByteAt(s, i+2) == '<') {
				i++
			}
			i += 2
			return i
		} else if peek == '&' && (s[i] == '>' || s[i] == '<') {
			j := i + 2
			for j < len(s) && histDigit(s[j]) {
				j++
			}
			if histByteAt(s, j) == '-' {
				j++
			}
			return j
		} else if (peek == '>' && s[i] == '&') || (peek == '|' && s[i] == '>') {
			return i + 2
		} else if peek == '(' && (s[i] == '>' || s[i] == '<') {
			i += 2
			delimopen = '('
			delimiter = ')'
			nestdelim = 1
			goto getWord
		}
		return i + 1
	}

getWord:
	if delimiter == 0 && i < len(s) && histMember(s[i], histQuoteChars) {
		delimiter = s[i]
		i++
	}
	for ; i < len(s); i++ {
		if s[i] == '\\' && histByteAt(s, i+1) == '\n' {
			i++
			continue
		}
		if s[i] == '\\' && delimiter != '\'' &&
			(delimiter != '"' || histMember(s[i], histSlashifyInQuotes)) {
			i++
			if i >= len(s) {
				break
			}
			continue
		}
		if nestdelim != 0 && s[i] == delimopen {
			nestdelim++
			continue
		}
		if nestdelim != 0 && s[i] == delimiter {
			nestdelim--
			if nestdelim == 0 {
				delimiter = 0
			}
			continue
		}
		if delimiter != 0 && s[i] == delimiter {
			delimiter = 0
			continue
		}
		if nestdelim == 0 && delimiter == 0 && histMember(s[i], "<>$!@?+*") && histByteAt(s, i+1) == '(' {
			i++
			if i+1 >= len(s) {
				break
			}
			delimopen = '('
			delimiter = ')'
			nestdelim = 1
			continue
		}
		if delimiter == 0 && histMember(s[i], histWordDelims) {
			break
		}
		if delimiter == 0 && histMember(s[i], histQuoteChars) {
			delimiter = s[i]
		}
	}
	return i
}

// histTokenizeInternal splits a line into words. When wind >= 0, the index
// of the word containing char offset wind is returned as the second value.
func histTokenizeInternal(s string, wind int) ([]string, int) {
	var result []string
	indp := -1
	i := 0
	for i < len(s) {
		for i < len(s) && histFieldDelim(s[i]) {
			i++
		}
		if i >= len(s) || s[i] == histCommentChar {
			return result, indp
		}
		start := i
		i = histTokenizeWord(s, start)
		if i == start { // a bare delimiter char becomes its own field
			i++
			for i < len(s) && histMember(s[i], histWordDelims) {
				i++
			}
		}
		if wind != -1 && wind >= start && wind < i {
			indp = len(result)
		}
		result = append(result, s[start:i])
	}
	return result, indp
}

// readHistFile reads a history file the way bash's read_history does:
// `#<digits>` lines are timestamps delimiting (possibly multi-line)
// entries; without timestamps every line is one entry. Returns the
// entries and the number of file lines consumed.
func readHistFile(path string) ([]string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) == 0 {
		return nil, 0, nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, strings.Count(string(data), "\n"), nil
	}
	lines := strings.Split(text, "\n")
	isTimestamp := func(s string) bool {
		return len(s) > 1 && s[0] == '#' && s[1] >= '0' && s[1] <= '9'
	}
	var entries []string
	var cur []string
	timestamped := false
	flush := func() {
		// Leading blank lines are removed; interior and trailing
		// blanks are preserved.
		for len(cur) > 0 && strings.TrimSpace(cur[0]) == "" {
			cur = cur[1:]
		}
		if len(cur) > 0 {
			entries = append(entries, strings.Join(cur, "\n"))
		}
		cur = nil
	}
	for _, ln := range lines {
		if isTimestamp(ln) {
			timestamped = true
			flush()
			continue
		}
		if timestamped {
			cur = append(cur, ln)
		} else if ln != "" {
			entries = append(entries, ln)
		}
	}
	flush()
	return entries, len(lines), nil
}

func writeHistFile(path string, entries []string) error {
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

func appendHistFile(path string, entries []string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		if _, err := fmt.Fprintf(f, "%s\n", e); err != nil {
			return err
		}
	}
	return nil
}

// ---- timeline construction ----

// buildHistTimeline parses the script source (keeping comments) and
// produces the ordered list of history entries bash's reader would
// record, with their starting source lines.
func buildHistTimeline(src []byte) ([]histGroup, map[int]bool, map[int]bool) {
	parser := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBash))
	f, err := parser.Parse(strings.NewReader(string(src)), "")
	if err != nil {
		return nil, nil, nil
	}
	srcLines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")

	type span struct {
		start, end int
		hdoc       bool
	}
	var spans []span
	quotedBreak := map[int]bool{} // line L: break between L and L+1 is inside quotes
	noExpand := map[int]bool{}    // continuation lines read mid-parse: no history expansion

	// Bash does not history-expand lines it reads while still parsing an
	// unfinished construct: here-document bodies and the continuation lines
	// of a command substitution that spans multiple physical lines. Only the
	// line that opens the construct is expanded.
	markNoExpand := func(node syntax.Node) {
		syntax.Walk(node, func(n syntax.Node) bool {
			if n == nil {
				return true
			}
			switch q := n.(type) {
			case *syntax.CmdSubst:
				for l := int(q.Left.Line()) + 1; l <= int(q.Right.Line()); l++ {
					noExpand[l] = true
				}
			case *syntax.Redirect:
				if (q.Op == syntax.Hdoc || q.Op == syntax.DashHdoc) && q.Hdoc != nil {
					p, e := q.Hdoc.Pos(), q.Hdoc.End()
					if p.IsValid() && e.IsValid() {
						for l := int(p.Line()); l <= int(e.Line()); l++ {
							noExpand[l] = true
						}
					}
				}
			}
			return true
		})
	}

	scanQuotes := func(node syntax.Node) {
		syntax.Walk(node, func(n syntax.Node) bool {
			if n == nil {
				return true
			}
			var p, e syntax.Pos
			switch q := n.(type) {
			case *syntax.SglQuoted:
				p, e = q.Pos(), q.End()
			case *syntax.DblQuoted:
				p, e = q.Pos(), q.End()
			default:
				return true
			}
			if p.IsValid() && e.IsValid() {
				for l := int(p.Line()); l < int(e.Line()); l++ {
					quotedBreak[l] = true
				}
			}
			return true
		})
	}

	for _, st := range f.Stmts {
		start, end := int(st.Pos().Line()), int(st.End().Line())
		hdoc := false
		syntax.Walk(st, func(n syntax.Node) bool {
			if n == nil {
				return true
			}
			if e := n.End(); e.IsValid() && int(e.Line()) > end {
				end = int(e.Line())
			}
			if rd, ok := n.(*syntax.Redirect); ok &&
				(rd.Op == syntax.Hdoc || rd.Op == syntax.DashHdoc) {
				hdoc = true
			}
			return true
		})
		scanQuotes(st)
		markNoExpand(st)
		spans = append(spans, span{start, end, hdoc})
		for _, c := range st.Comments {
			spans = append(spans, span{int(c.Pos().Line()), int(c.End().Line()), false})
		}
	}
	for _, c := range f.Last {
		spans = append(spans, span{int(c.Pos().Line()), int(c.End().Line()), false})
	}
	if len(spans) == 0 {
		return nil, quotedBreak, noExpand
	}
	// Sort by start line and merge spans sharing lines: bash's reader is
	// line-oriented, so `echo a; echo b` on one line is one entry.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	var merged []span
	for _, s := range spans {
		if n := len(merged); n > 0 && s.start <= merged[n-1].end {
			if s.end > merged[n-1].end {
				merged[n-1].end = s.end
			}
			merged[n-1].hdoc = merged[n-1].hdoc || s.hdoc
			continue
		}
		merged = append(merged, s)
	}

	var groups []histGroup
	for _, s := range merged {
		if s.start < 1 || s.start > len(srcLines) {
			continue
		}
		end := min(s.end, len(srcLines))
		lines := srcLines[s.start-1 : end]
		text := joinHistLines(lines, s.hdoc, s.start, quotedBreak)
		if strings.TrimSpace(text) == "" {
			continue
		}
		groups = append(groups, histGroup{
			startLine: s.start,
			endLine:   end,
			text:      text,
			lines:     append([]string(nil), lines...),
			hdoc:      s.hdoc,
		})
	}
	return groups, quotedBreak, noExpand
}

// joinHistLines reproduces bash_add_history's joining of the source
// lines of one command into a single history entry.
func joinHistLines(lines []string, hdoc bool, startLine int, quotedBreak map[int]bool) string {
	if len(lines) == 1 {
		return lines[0]
	}
	if hdoc {
		// Here-document lines keep their newlines, including the
		// trailing one after the delimiter.
		return strings.Join(lines, "\n") + "\n"
	}
	acc := lines[0]
	for i, ln := range lines[1:] {
		boundary := startLine + i // line number before this one
		if quotedBreak[boundary] {
			acc += "\n" + ln
			continue
		}
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if t := strings.TrimSuffix(acc, "\\"); t != acc && !strings.HasSuffix(t, "\\") {
			// Escaped newline: the quoted newline disappears.
			acc = t + ln
			continue
		}
		if histNoSemiAfter(acc) {
			acc += " " + ln
		} else {
			acc += "; " + ln
		}
	}
	return acc
}

// histNoSemiAfter mirrors parse.y's no_semi_successors: after these
// trailing tokens bash joins continuation lines with a space, not `; `.
func histNoSemiAfter(acc string) bool {
	t := strings.TrimRight(acc, " \t")
	if t == "" {
		return true
	}
	for _, suf := range []string{"&&", "||", ";;&", ";;", ";&"} {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	switch t[len(t)-1] {
	case ';', '&', '|', '{', '(', ')':
		return true
	}
	fields := strings.Fields(t)
	switch fields[len(fields)-1] {
	case "case", "do", "else", "if", "then", "until", "while", "in":
		return true
	}
	return false
}

// ---- the history builtin ----

func (r *Runner) historyBuiltin(pos syntax.Pos, args []string) (exit exitStatus) {
	failf := func(code uint8, format string, a ...any) exitStatus {
		r.errf("%s"+format, append([]any{r.bashErrPrefix(pos)}, a...)...)
		exit.code = code
		return exit
	}
	usage := func(flag string) exitStatus {
		r.errf("%shistory: %s: invalid option\n", r.bashErrPrefix(pos), flag)
		r.errf("history: usage: %s\n", bashUsage["history"])
		exit.code = 2
		return exit
	}
	var aFlag, cFlag, nFlag, pFlag, rFlag, sFlag, wFlag, dFlag bool
	var deleteArg string
	i := 0
parseOpts:
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'a':
				aFlag = true
			case 'c':
				cFlag = true
			case 'n':
				nFlag = true
			case 'p':
				pFlag = true
			case 'r':
				rFlag = true
			case 's':
				sFlag = true
			case 'w':
				wFlag = true
			case 'd':
				dFlag = true
				if j+1 < len(a) {
					deleteArg = a[j+1:]
				} else if i+1 < len(args) {
					i++
					deleteArg = args[i]
				} else {
					return failf(2, "history: -d: option requires an argument\n")
				}
				continue parseOpts
			default:
				return usage(a[:1] + string(a[j]))
			}
		}
	}
	args = args[i:]

	nrw := 0
	for _, f := range []bool{aFlag, nFlag, rFlag, wFlag} {
		if f {
			nrw++
		}
	}
	if nrw > 1 {
		return failf(1, "history: cannot use more than one of -anrw\n")
	}

	h := shellHist
	h.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			h.mu.Unlock()
		}
	}()

	if cFlag {
		h.list = nil
		h.base = 1
		h.linesThisSession = 0
		h.lastAdded = false
		if len(args) == 0 && !sFlag && !dFlag && nrw == 0 {
			return exit
		}
	}

	switch {
	case sFlag:
		if len(args) > 0 {
			// bash push_history: drop the `history -s` line itself if
			// the reader just added it, then add the argument string.
			if h.lastAdded && !h.lastPushed && len(h.list) > 0 {
				h.list = h.list[:len(h.list)-1]
			}
			h.addLine(r, strings.Join(args, " "))
			h.lastAdded = false
			h.lastPushed = true
		}
		return exit
	case pFlag:
		if h.lastAdded && !h.lastPushed && len(h.list) > 0 {
			h.list = h.list[:len(h.list)-1]
			h.lastAdded = false
		}
		for _, a := range args {
			if strings.HasPrefix(a, "!") && len(a) > 1 {
				exp, ok := h.expandDesignator(a)
				if !ok {
					h.mu.Unlock()
					unlocked = true
					return failf(1, "history: %s: history expansion failed\n", a)
				}
				r.outf("%s\n", exp)
			} else {
				r.outf("%s\n", a)
			}
		}
		return exit
	case dFlag:
		erange := func(tok string) exitStatus {
			h.mu.Unlock()
			unlocked = true
			return failf(1, "history: %s: history position out of range\n", tok)
		}
		rest := deleteArg
		if strings.HasPrefix(rest, "-") {
			rest = rest[1:]
		}
		if dash := strings.Index(rest, "-"); dash >= 0 {
			// Range form: start-end.
			startTok := deleteArg[:len(deleteArg)-len(rest)+dash]
			endTok := rest[dash+1:]
			start, err1 := strconv.Atoi(startTok)
			end, err2 := strconv.Atoi(endTok)
			if err1 != nil || err2 != nil || endTok == "" {
				return erange(deleteArg)
			}
			if strings.HasPrefix(startTok, "-") && start < 0 {
				start += len(h.list)
			} else if start > 0 {
				start -= h.base
			}
			if start < 0 || start >= len(h.list) {
				return erange(startTok)
			}
			if strings.HasPrefix(endTok, "-") && end < 0 {
				end += len(h.list)
			} else if end > 0 {
				end -= h.base
			}
			if end < 0 || end >= len(h.list) {
				return erange(endTok)
			}
			if start <= end {
				h.list = append(h.list[:start], h.list[end+1:]...)
			}
			return exit
		}
		n, err := strconv.Atoi(deleteArg)
		if err != nil {
			h.mu.Unlock()
			unlocked = true
			return failf(1, "history: %s: invalid number\n", deleteArg)
		}
		var idx int
		if strings.HasPrefix(deleteArg, "-") && n < 0 {
			idx = len(h.list) + n
			if idx < 0 {
				return erange(deleteArg)
			}
		} else if n < h.base || n >= h.base+len(h.list) {
			return erange(deleteArg)
		} else {
			idx = n - h.base
		}
		if idx >= len(h.list) {
			return erange(deleteArg)
		}
		h.list = append(h.list[:idx], h.list[idx+1:]...)
		return exit
	case nrw > 0:
		filename := ""
		if len(args) > 0 {
			filename = args[0]
		} else {
			filename = r.envGet("HISTFILE")
		}
		if filename == "" {
			h.mu.Unlock()
			unlocked = true
			if len(args) > 0 {
				return failf(1, "history: empty filename\n")
			}
			return failf(1, "history: HISTFILE: parameter null or not set\n")
		}
		path := r.absPath(filename)
		var err error
		switch {
		case aFlag:
			if h.linesThisSession > 0 {
				n := min(h.linesThisSession, len(h.list))
				err = appendHistFile(path, h.list[len(h.list)-n:])
				if err == nil {
					h.linesInFile += n
					h.linesThisSession = 0
				}
			} else if _, serr := os.Stat(path); os.IsNotExist(serr) {
				err = writeHistFile(path, nil)
			}
		case wFlag:
			err = writeHistFile(path, h.list)
		case rFlag:
			var entries []string
			var nlines int
			entries, nlines, err = readHistFile(path)
			if err == nil {
				for _, e := range entries {
					h.appendEntry(r, e)
				}
				h.linesInFile = nlines
			}
		case nFlag:
			var entries []string
			var nlines int
			entries, nlines, err = readHistFileFrom(path, h.linesInFile)
			if err == nil {
				for _, e := range entries {
					h.appendEntry(r, e)
				}
				h.linesInFile = nlines
				h.linesThisSession += len(entries)
			}
		}
		if err != nil {
			h.mu.Unlock()
			unlocked = true
			return failf(1, "history: %s: %v\n", filename, err)
		}
		return exit
	case cFlag:
		return exit
	}

	// Plain listing, optionally limited to the last N entries.
	limit := len(h.list)
	if len(args) > 1 {
		h.mu.Unlock()
		unlocked = true
		return failf(2, "history: too many arguments\n")
	}
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			h.mu.Unlock()
			unlocked = true
			return failf(2, "history: %s: numeric argument required\n", args[0])
		}
		if n < 0 {
			n = -n
		}
		limit = min(n, len(h.list))
	}
	for idx := len(h.list) - limit; idx < len(h.list); idx++ {
		r.outf("%5d  %s\n", h.base+idx, h.list[idx])
	}
	return exit
}

// readHistFileFrom reads only the file lines past the first `from`
// lines, for `history -n`.
func readHistFileFrom(path string, from int) ([]string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil, 0, nil
	}
	lines := strings.Split(text, "\n")
	if from >= len(lines) {
		return nil, len(lines), nil
	}
	rest := strings.Join(lines[from:], "\n") + "\n"
	tmp := []byte(rest)
	// Reuse readHistFile's parsing on the remaining lines.
	f, err := os.CreateTemp("", "bashy-histn")
	if err != nil {
		return nil, 0, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(tmp); err != nil {
		f.Close()
		return nil, 0, err
	}
	f.Close()
	entries, _, err := readHistFile(f.Name())
	return entries, len(lines), err
}

// ---- the fc builtin ----

const histInvalid = -1 << 30
const histNotFound = histInvalid + 2

func (r *Runner) fcBuiltin(ctx context.Context, pos syntax.Pos, args []string) (exit exitStatus) {
	failf := func(code uint8, format string, a ...any) exitStatus {
		r.errf("%s"+format, append([]any{r.bashErrPrefix(pos)}, a...)...)
		exit.code = code
		return exit
	}
	fcIsNumber := func(s string) bool {
		s = strings.TrimPrefix(s, "-")
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

	numbering, reverse, listing, execute := true, false, false, false
	seenN, seenR, seenL, seenS, seenE := false, false, false, false, false
	ename := ""
	i := 0
parseOpts:
	for ; i < len(args); i++ {
		a := args[i]
		if fcIsNumber(a) {
			break
		}
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'n':
				seenN = true
				numbering = false
			case 'l':
				seenL = true
				listing = true
			case 'r':
				seenR = true
				reverse = true
			case 's':
				seenS = true
				execute = true
			case 'e':
				seenE = true
				if j+1 < len(a) {
					ename = a[j+1:]
				} else if i+1 < len(args) {
					i++
					ename = args[i]
				} else {
					return failf(2, "fc: -e: option requires an argument\n")
				}
				continue parseOpts
			default:
				r.errf("%sfc: -%c: invalid option\n", r.bashErrPrefix(pos), a[j])
				r.errf("fc: usage: %s\n", bashUsage["fc"])
				exit.code = 2
				return exit
			}
		}
	}
	specs := args[i:]

	editorDash := seenE && ename == "-"
	if ename == "-" {
		execute = true
		ename = ""
	}

	// Issue 7 defines three distinct forms. Bash accepts mixtures and surplus
	// operands as extensions, but the sh/POSIX route must reject them rather
	// than silently reinterpret or ignore them.
	if r.opts[optPosix] {
		usage := func() exitStatus { return failf(1, "fc: invalid POSIX usage\n") }
		tooMany := func() exitStatus { return failf(1, "fc: too many arguments\n") }
		switch {
		case seenS || editorDash:
			if seenL || seenN || seenR || (seenS && seenE) {
				return usage()
			}
			if len(specs) > 2 {
				return tooMany()
			}
			// The two-operand execution form is [old=new] [first]. Without
			// an '=' the first operand is already the command selector.
			if len(specs) == 2 && !strings.Contains(specs[0], "=") {
				return tooMany()
			}
		case seenL:
			if seenE || seenS {
				return usage()
			}
			if len(specs) > 2 {
				return tooMany()
			}
		default:
			if seenN || seenS {
				return usage()
			}
			if len(specs) > 2 {
				return tooMany()
			}
		}
	}

	h := shellHist
	h.mu.Lock()

	rh := 0
	if h.enabled {
		rh = 1
	}
	la := 0
	if h.lastAdded {
		la = 1
	}
	// Inside a command/process substitution, bash's reader has already
	// recorded the enclosing line before running the substitution body, so
	// hist_last_line_added refers to that enclosing line and fc excludes it.
	// This interpreter records the enclosing line only after the body runs
	// (at r.call), so h.lastAdded here still points at the genuine previous
	// command, which must be kept. Don't subtract for it.
	if h.cmdSubst.Load() > 0 {
		la = 0
	}

	// gethnum translates one history spec into a 0-based list index,
	// following fc.def's fc_gethnum.
	lastHist := len(h.list) - rh - la
	if lastHist < 0 {
		lastHist = 0
	}
	realLast := len(h.list) - 1
	gethnum := func(spec string, first bool) int {
		idx := lastHist
		if spec == "" {
			return idx
		}
		s := spec
		sign := 1
		if strings.HasPrefix(s, "-") {
			sign = -1
			s = s[1:]
		}
		if s != "" && s[0] >= '0' && s[0] <= '9' {
			end := 0
			for end < len(s) && s[end] >= '0' && s[end] <= '9' {
				end++
			}
			n, _ := strconv.Atoi(s[:end])
			n *= sign
			if n < 0 {
				n += idx + 1
				if n < 0 {
					n = 0
				}
				return n
			}
			if n == 0 {
				if sign == -1 {
					if listing {
						return realLast
					}
					return histInvalid
				}
				return idx
			}
			n -= h.base
			// Bash's fc_gethnum treats the newest selectable index as
			// already out of range, so `fc -l 3 1` over a three-command
			// history clamps 3 down to the oldest entry and lists only
			// command 1. POSIX Issue 7 has no such exclusion: any command
			// number that `fc -l` displays is a valid first/last operand,
			// and a first newer than last must list in reverse. This is a
			// deliberate POSIX-over-Bash divergence, so keep bash's
			// exclusive bound whenever POSIX mode is off.
			outOfRange := n < 0 || n >= idx
			if r.opts[optPosix] {
				outOfRange = n < 0 || n > idx
			}
			if outOfRange {
				if first {
					return 0
				}
				return idx
			}
			return n
		}
		for j := idx; j >= 0 && j < len(h.list); j-- {
			if strings.HasPrefix(h.list[j], spec) {
				return j
			}
		}
		return histNotFound
	}

	if execute {
		// fc -s [pat=rep ...] [command]: re-execute with substitutions.
		var subs [][2]string
		maxSubs := len(specs)
		if r.opts[optPosix] {
			// Issue 7 defines one optional old=new operand followed by an
			// optional first operand. Once that substitution is consumed, a
			// selector containing '=' must remain a selector. Bash accepts
			// multiple substitutions as an extension outside POSIX mode.
			maxSubs = min(maxSubs, 1)
		}
		for len(specs) > 0 && len(subs) < maxSubs {
			eq := strings.Index(specs[0], "=")
			if eq < 0 {
				break
			}
			subs = append(subs, [2]string{specs[0][:eq], specs[0][eq+1:]})
			specs = specs[1:]
		}
		if r.opts[optPosix] && len(specs) > 1 {
			h.mu.Unlock()
			return failf(1, "fc: too many arguments\n")
		}
		spec := ""
		if len(specs) > 0 {
			spec = specs[0]
		}
		idx := gethnum(spec, false)
		if idx < 0 || idx >= len(h.list) {
			h.mu.Unlock()
			return failf(1, "fc: no command found\n")
		}
		command := h.list[idx]
		for _, s := range subs {
			if r.opts[optPosix] {
				// Issue 7 specifies replacement of the first occurrence.
				command = strings.Replace(command, s[0], s[1], 1)
			} else {
				command = strings.ReplaceAll(command, s[0], s[1])
			}
		}
		// Replace the recorded `fc -s` line with the re-executed
		// command, so consecutive substitutions chain.
		if h.lastAdded && !h.lastPushed && len(h.list) > 0 {
			h.list = h.list[:len(h.list)-1]
		}
		h.addLine(r, command)
		h.mu.Unlock()
		r.errf("%s\n", command)
		return r.histRunString(ctx, command)
	}

	if len(h.list) == 0 {
		h.mu.Unlock()
		return exit
	}

	var histbeg, histend int
	if len(specs) > 0 {
		histbeg = gethnum(specs[0], true)
		if len(specs) > 1 {
			histend = gethnum(specs[1], false)
		} else if histbeg == realLast {
			histend = histbeg
			if listing {
				histend = realLast
			}
		} else {
			histend = histbeg
			if listing {
				histend = lastHist
			}
		}
	} else if listing {
		histend = lastHist
		histbeg = max(histend-15, 0)
	} else {
		histbeg, histend = lastHist, lastHist
	}

	if histbeg == histInvalid || histend == histInvalid {
		h.mu.Unlock()
		return failf(1, "fc: history specification out of range\n")
	}
	if histbeg == histNotFound || histend == histNotFound {
		h.mu.Unlock()
		return failf(1, "fc: no command found\n")
	}
	histbeg, histend = max(histbeg, 0), max(histend, 0)

	if !listing && h.lastAdded && !h.lastPushed && len(h.list) > 0 {
		// "When not listing, the fc command that caused the editing
		// shall not be entered into the history list."
		h.list = h.list[:len(h.list)-1]
		if histend >= lastHist {
			histend = lastHist
		} else if histbeg >= lastHist {
			histbeg = lastHist
		}
		h.lastAdded = false
	}

	if histend < histbeg {
		histbeg, histend = histend, histbeg
		reverse = true
	}
	histend = min(histend, len(h.list)-1)
	histbeg = min(histbeg, len(h.list)-1)
	if histbeg < 0 || histend < 0 {
		h.mu.Unlock()
		return exit
	}

	if listing {
		idxs := make([]int, 0, histend-histbeg+1)
		for j := histbeg; j <= histend; j++ {
			idxs = append(idxs, j)
		}
		if reverse {
			for l, rgt := 0, len(idxs)-1; l < rgt; l, rgt = l+1, rgt-1 {
				idxs[l], idxs[rgt] = idxs[rgt], idxs[l]
			}
		}
		base := h.base
		entries := make([]string, len(idxs))
		for k, j := range idxs {
			entries[k] = h.list[j]
		}
		h.mu.Unlock()
		for k, j := range idxs {
			// Bash reserves a one-character column after the tab for the
			// history entry's modified marker. POSIX mode suppresses that
			// indication entirely.
			marker := " "
			if r.opts[optPosix] {
				marker = ""
			}
			lines := strings.Split(entries[k], "\n")
			if numbering {
				r.outf("%d\t%s%s\n", base+j, marker, lines[0])
			} else {
				r.outf("\t%s%s\n", marker, lines[0])
			}
			for _, line := range lines[1:] {
				r.outf("\t%s\n", line)
			}
		}
		return exit
	}

	// Edit-and-rerun: write the range to a temp file, run the editor on
	// it, then echo and execute the (possibly edited) commands.
	selected := make([]string, 0, histend-histbeg+1)
	if reverse {
		for j := histend; j >= histbeg; j-- {
			selected = append(selected, h.list[j])
		}
	} else {
		for j := histbeg; j <= histend; j++ {
			selected = append(selected, h.list[j])
		}
	}
	h.mu.Unlock()

	tmp, err := os.CreateTemp("", "bashy-fc")
	if err != nil {
		return failf(1, "fc: cannot open temp file: %v\n", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	for _, e := range selected {
		fmt.Fprintf(tmp, "%s\n", e)
	}
	tmp.Close()

	editor := ename
	if editor == "" {
		if editor = r.envGet("FCEDIT"); editor == "" {
			if r.opts[optPosix] {
				editor = "ed"
			} else {
				if editor = r.envGet("EDITOR"); editor == "" {
					editor = "vi"
				}
			}
		}
	}
	// The editor command is shell text (bash runs `$editor $tmpfile` through
	// the parser), so the temp file goes in the shell's own spelling
	// (/c/Users/… on Windows, where the native C:\… would lose its
	// backslashes) and quoted, so a temp directory with a space survives.
	editorOperand := shellPathFromOS(tmpName)
	if quoted, err := syntax.Quote(editorOperand, syntax.LangBash); err == nil {
		editorOperand = quoted
	}
	if st := r.histRunString(ctx, editor+" "+editorOperand); st.code != 0 {
		exit.code = 1
		return exit
	}

	edited, err := os.ReadFile(tmpName)
	if err != nil {
		exit.code = 1
		return exit
	}
	content := strings.TrimSuffix(string(edited), "\n")
	if content == "" {
		return exit
	}
	for _, ln := range strings.Split(content, "\n") {
		r.errf("%s\n", ln)
	}
	h.mu.Lock()
	h.addLine(r, content)
	h.mu.Unlock()
	return r.histRunString(ctx, content)
}

// histRunString parses and runs a command string, returning its exit
// status. Used by fc to re-execute history entries and editors.
func (r *Runner) histRunString(ctx context.Context, src string) exitStatus {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		r.errf("fc: parse error: %v\n", err)
		return exitStatus{code: 1}
	}
	r.stmts(ctx, file.Stmts)
	return r.exit
}
