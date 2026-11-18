// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/pattern"
	"github.com/qiangli/bash/syntax"
)

// rlEmu is a minimal readline emulation backing `bash -i` with a
// non-tty stdin. It reproduces the slice of readline+bash history
// behavior that bash's own test suite drives through a pipe: plain
// line input, C-r reverse incremental search, C-p previous-history,
// C-o operate-and-get-next, multi-line commands (PS2 continuation)
// recorded cmdhist-style, HISTSIZE stifling, HISTCONTROL/HISTIGNORE
// filtering, and HISTFILE loading at startup.
type rlEmu struct {
	hist []string
	base int // logical history number of hist[0] (bash history_base)
	max  int // maximum entries; -1 means unstifled

	// dir is the shell's cwd, which resolves $HISTFILE into the host's
	// spelling; see shellFilePath.
	dir string

	ignoreSpace, ignoreDups bool
	ignorePats              []string

	added int // entries recorded this session (for the exit-time save)
}

func newRlEmu(dir string) *rlEmu {
	e := &rlEmu{base: 1, max: -1, dir: dir}
	// Unset HISTSIZE defaults to 500; an empty or non-numeric value
	// leaves the history unstifled, like bash's sv_histsize.
	if v, ok := os.LookupEnv("HISTSIZE"); !ok {
		e.max = 500
	} else if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		e.max = n
	}
	for _, w := range strings.Split(os.Getenv("HISTCONTROL"), ":") {
		switch w {
		case "ignorespace":
			e.ignoreSpace = true
		case "ignoredups":
			e.ignoreDups = true
		case "ignoreboth":
			e.ignoreSpace, e.ignoreDups = true, true
		}
	}
	if hi := os.Getenv("HISTIGNORE"); hi != "" {
		e.ignorePats = strings.Split(hi, ":")
	}
	if path := os.Getenv("HISTFILE"); path != "" {
		e.loadFile(path)
	}
	return e
}

// loadFile reads a history file the way an interactive bash does at
// startup: without `#<epoch>` timestamp markers every line is its own
// entry (multi-line entries written by `history -w` come back split).
func (e *rlEmu) loadFile(path string) {
	data, err := os.ReadFile(shellFilePath(e.dir, path))
	if err != nil {
		return
	}
	for _, ln := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if ln == "" || (len(ln) > 1 && ln[0] == '#' && ln[1] >= '0' && ln[1] <= '9') {
			continue
		}
		e.hist = append(e.hist, ln)
		e.stifle()
	}
}

func (e *rlEmu) stifle() {
	if e.max < 0 {
		return
	}
	for len(e.hist) > e.max {
		e.hist = e.hist[1:]
		e.base++
	}
}

func (e *rlEmu) ignored(line string) bool {
	for _, pat := range e.ignorePats {
		if pat == "" {
			continue
		}
		if pat == "&" {
			if len(e.hist) > 0 && e.hist[len(e.hist)-1] == line {
				return true
			}
			continue
		}
		expr, err := pattern.Regexp(pat, pattern.EntireString)
		if err != nil {
			continue
		}
		if rx, err := regexp.Compile(expr); err == nil && rx.MatchString(line) {
			return true
		}
	}
	return false
}

// add records the first line of a command, applying HISTCONTROL and
// HISTIGNORE. It reports whether the line was saved, which decides
// whether continuation lines of the same command are appended.
func (e *rlEmu) add(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	if e.ignoreSpace && (line[0] == ' ' || line[0] == '\t') {
		return false
	}
	if e.ignoreDups && len(e.hist) > 0 && e.hist[len(e.hist)-1] == line {
		return false
	}
	if e.ignored(line) {
		return false
	}
	e.hist = append(e.hist, line)
	e.added++
	e.stifle()
	return true
}

// appendLast merges a continuation line into the last entry, joining
// with a literal newline when the line break was inside quotes (bash's
// history_delimiting_chars) and `; ` otherwise.
func (e *rlEmu) appendLast(line, pending string) {
	if len(e.hist) == 0 {
		return
	}
	sep := "; "
	if inSgl, inDbl := openQuoteState(pending); inSgl || inDbl {
		sep = "\n"
	}
	e.hist[len(e.hist)-1] += sep + line
}

// openQuoteState reports whether s ends inside an unterminated single
// or double quote, skipping backslash escapes outside single quotes.
func openQuoteState(s string) (inSgl, inDbl bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inSgl:
			if c == '\'' {
				inSgl = false
			}
		case inDbl:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDbl = false
			}
		default:
			switch c {
			case '\\':
				i++
			case '\'':
				inSgl = true
			case '"':
				inDbl = true
			}
		}
	}
	return inSgl, inDbl
}

// runnerExpand evaluates `"${expr}"` against the live runner state in a
// throwaway subshell, so in-session assignments (e.g. `HISTFILE=`) are
// honored where the process environment would be stale.
func runnerExpand(r *interp.Runner, expr string) string {
	sub := r.Subshell()
	var buf bytes.Buffer
	if err := interp.StdIO(nil, &buf, io.Discard)(sub); err != nil {
		return ""
	}
	src := `printf '%s' "${` + expr + `}"`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		return ""
	}
	_ = sub.Run(context.Background(), f)
	return buf.String()
}

// Control bytes handled by the emulated readline loop.
const (
	ctrlO = 0x0f // operate-and-get-next
	ctrlP = 0x10 // previous-history
	ctrlR = 0x12 // reverse-search-history
)

// runForcedInteractiveExec emulates `bash -i` (without -n) when stdin
// is not a terminal: input bytes are interpreted as readline keys, each
// accepted line is echoed after the prompt to stderr, recorded into the
// session history, and executed, accumulating across lines until the
// parser has a complete command. At EOF the shell prints `exit` and
// saves the history file.
func runForcedInteractiveExec(r *interp.Runner) error {
	r.EnableInteractiveHistory()
	ps1 := os.Getenv("PS1")
	if ps1 == "" {
		ps1 = "$ "
	}
	ps2 := os.Getenv("PS2")
	if ps2 == "" {
		ps2 = "> "
	}

	e := newRlEmu(r.Dir)
	var (
		buffer       string // current edit line (recalled entries may be multi-line)
		hpos         int    // history offset buffer came from; len(hist) when fresh
		savedLogical = -1   // armed by C-o: logical number of the entry to preload
		pending      string // accumulated lines of an incomplete command
		firstSaved   bool   // first line of the pending command was recorded
		isearch      bool
		isearchStr   string
		exited       bool
		lastErr      error // exit status of the most recently run command
	)

	saveHistory := func() {
		path := runnerExpand(r, "HISTFILE-")
		if path == "" || e.added == 0 {
			return
		}
		n := min(e.added, len(e.hist))
		writeSessionHistory(r.Dir, path, e.hist[len(e.hist)-n:],
			runnerExpand(r, "HISTTIMEFORMAT+set") != "",
			runnerExpand(r, "HISTFILESIZE-__unset__"))
	}

	startLine := func() {
		buffer, hpos = "", len(e.hist)
		if savedLogical >= 0 {
			if idx := savedLogical - e.base; idx >= 0 && idx < len(e.hist) {
				buffer, hpos = e.hist[idx], idx
			}
			savedLogical = -1
		}
		isearch, isearchStr = false, ""
	}

	// accept consumes the current line like readline's accept-line: echo
	// it after the prompt, record it, and run it once the accumulated
	// command parses as complete.
	accept := func(operate bool) {
		if operate {
			// bash 5.3's rl_operate_and_get_next: remember the *logical*
			// number of the next entry, immune to stifle-induced shifts.
			savedLogical = hpos + e.base + 1
		} else {
			savedLogical = -1
		}
		prompt := ps1
		if pending != "" {
			prompt = ps2
		}
		fmt.Fprintf(os.Stderr, "%s%s\n", prompt, buffer)
		if pending == "" {
			if strings.TrimSpace(buffer) == "" {
				return
			}
			firstSaved = e.add(buffer)
		} else if firstSaved {
			e.appendLast(buffer, pending)
		}
		pending += buffer + "\n"
		parser := syntax.NewParser(bashyParseOpts(r.LangVariant(), syntax.KeepComments(true))...)
		_, err := parser.Parse(strings.NewReader(pending), "bashy")
		if err != nil {
			if !syntax.IsIncomplete(err) {
				pending = ""
			}
			return
		}
		r.RecordInteractiveHistory(strings.TrimSuffix(pending, "\n"))
		input := pending
		pending = ""
		lastErr = runStatementStream(context.Background(), r, []byte(input), r.LangVariant(), "bashy")
		if r.Exited() {
			exited = true
		}
	}

	// Do not put a buffered reader in front of the process stdin. Once an
	// accepted command starts, it owns all following bytes until it returns;
	// fc's editor, read, and cat must see them before this loop does.
	var inputByte [1]byte
	startLine()
	for !exited {
		n, err := os.Stdin.Read(inputByte[:])
		if n == 0 {
			if err != nil {
				break
			}
			continue
		}
		c := inputByte[0]
		switch c {
		case '\n':
			if isearch {
				isearch = false
			}
			accept(false)
			startLine()
		case ctrlO:
			if isearch {
				isearch = false
			}
			accept(true)
			startLine()
		case ctrlP:
			if isearch {
				isearch = false
			}
			if len(e.hist) > 0 {
				if hpos >= len(e.hist) {
					hpos = len(e.hist) - 1
				} else if hpos > 0 {
					hpos--
				}
				buffer = e.hist[hpos]
			}
		case ctrlR:
			isearch, isearchStr = true, ""
		case '\r':
			// ignore
		default:
			if c < 0x20 && c != '\t' {
				continue // unhandled control key
			}
			if isearch {
				isearchStr += string(c)
				for j := min(hpos, len(e.hist)-1); j >= 0; j-- {
					if strings.Contains(e.hist[j], isearchStr) {
						buffer, hpos = e.hist[j], j
						break
					}
				}
			} else {
				buffer += string(c)
			}
		}
		if err != nil {
			break
		}
	}
	if !exited {
		fmt.Fprintf(os.Stderr, "%sexit\n", ps1)
	}
	saveHistory()
	// An interactive shell exits with the status of the last command it ran
	// (bash). r.Run returns that status as an interp.ExitStatus error, or nil
	// when the last command succeeded.
	return lastErr
}
