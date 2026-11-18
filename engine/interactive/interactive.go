// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !plan9 && !js

// Package interactive runs a readline-backed interactive shell loop on top
// of an [*interp.Runner].
//
// Two callers share this implementation:
//
//   - cmd/bashy, the bash-compatible CLI shell, where stdin is the terminal
//     the user is typing into.
//   - Embedders driving a PTY-wrapped runner from another process (e.g. the
//     outpost matrix-shell endpoint that bridges WebSocket bytes ↔ the
//     in-process bash). PTY slaves are TTYs, so the same arrow-keys-and-
//     history experience works there too — provided the slave fd is the one
//     used for termios changes, not the daemon's actual stdin. That wiring
//     is automatic when [Options.Stdin] is an *os.File on a TTY.
//
// The fallback path (when [Options.Stdin] is not a TTY *os.File) runs the
// plain newline-buffered loop from [syntax.Parser.InteractiveSeq], without
// line-editing. Useful for tests piping stdin and for callers who want one
// entry point for both interactive and scripted use.
package interactive

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/ergochat/readline"
	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/pathconv"
)

// Options configures [Run].
type Options struct {
	// Runner is the in-process shell interpreter to drive. Required.
	Runner *interp.Runner

	// Lang selects the parser variant (Bash, POSIX, mksh, …). Zero
	// defaults to [syntax.LangBash].
	Lang syntax.LangVariant
	// LangFunc, when non-nil, selects the parser variant before each statement.
	// It supports shells whose dialect can change at runtime. Lang remains the
	// construction-time fallback and preserves compatibility for all callers.
	LangFunc func(*interp.Runner) syntax.LangVariant

	// PosixMode applies POSIX behavioral parse rules on top of Lang
	// (see [syntax.PosixMode]) without switching to the stricter
	// LangPOSIX grammar — for a `bash --posix` style shell that keeps
	// bash syntax. Off by default; opt-in so other consumers are
	// unaffected.
	PosixMode bool

	// Stdin is the input source. When it is an *os.File on a TTY, the
	// package auto-wires raw-mode handling against that fd — required for
	// arrow keys, history navigation, and Ctrl-C. Otherwise Run falls
	// back to plain newline-buffered input via [syntax.Parser.InteractiveSeq],
	// unless AssumeTTY is set.
	Stdin io.Reader

	// AssumeTTY makes Run treat Stdin as an interactive terminal that is
	// already in raw mode, even when it is not an *os.File on a TTY. For
	// embedders bridging a remote terminal whose raw mode is managed at
	// the far end — an SSH client that did a pty-req, or a WebSocket-
	// attached xterm.js — over a plain pipe, on platforms (Windows) where
	// no kernel PTY pair can sit in between. Raw-mode enter/exit become
	// no-ops; echo and line editing are readline's own, written to Stdout.
	// Ignored when Stdin is a real TTY *os.File (the real binding wins).
	AssumeTTY bool
	// GetSize reports the current terminal size (cols, rows) when
	// AssumeTTY is in effect. Nil, or a non-positive return, falls back
	// to 80x24.
	GetSize func() (cols, rows int)
	// Stdout receives line-edit echo and command stdout. If nil, [os.Stdout]
	// is used.
	Stdout io.Writer
	// Stderr receives runner diagnostics. If nil, [os.Stderr] is used.
	Stderr io.Writer

	// PS1 / PS2 return the prompt to display before each new line of input.
	// The returned bytes are written verbatim — any \u/\h/\w expansion is
	// the caller's responsibility (see cmd/bashy's expandPrompt). Nil
	// defaults: PS1 = "$ ", PS2 = "> ".
	PS1 func() string
	PS2 func() string

	// VimMode, if non-nil, reports whether vi line-editing should be active
	// (emacs otherwise). It is consulted before each prompt so a runtime
	// `set -o vi` / `set +o vi` toggle takes effect. Nil = emacs (the default).
	VimMode func() bool

	// PreCommand, if non-nil, is invoked before each PS1 prompt is shown.
	// Bash's $PROMPT_COMMAND is the canonical use case.
	PreCommand func(context.Context, *interp.Runner)

	// Route, if non-nil, sees each fresh input line (never a continuation
	// line) before the shell parses it and returns the line to run instead.
	// Returning the line unchanged keeps plain shell semantics, continuation
	// prompts included; returning a different line runs that line as typed
	// input (no continuation); returning "" skips it. Agent front ends use it
	// to route input that is not a shell command (bashy's action ladder).
	Route func(ctx context.Context, r *interp.Runner, line string) string

	// OnRunError is called when a parsed statement returns a non-nil error
	// that is not just a non-zero command exit code. Default behaviour:
	// write "<err>\n" to Stderr. Set to a no-op func to suppress.
	OnRunError func(error)

	// Greeting is written verbatim to Stdout once before the first prompt.
	// Empty = no greeting.
	Greeting string

	// HistoryFile is the path to persist input lines to. Empty = in-memory
	// only. Parent directories are not created; the caller should ensure
	// they exist.
	HistoryFile string
	// HistoryLimit caps the number of remembered lines. Zero defaults to
	// 1000. Negative disables history entirely.
	HistoryLimit int
	// HistorySearchFold makes Ctrl-R reverse search case-insensitive.
	HistorySearchFold bool
	// InterruptPrompt is what readline prints on Ctrl-C while a line is
	// being edited. Defaults to "^C".
	InterruptPrompt string
	// EOFPrompt is what readline prints on Ctrl-D on an empty line.
	// Defaults to "exit".
	EOFPrompt string

	// ConfigureReadline, when non-nil, may adjust the line editor immediately
	// before it is constructed. It lets CLI consumers select optional readline
	// behavior without changing the defaults shared by other embedders.
	ConfigureReadline func(*readline.Config)

	// PlainTerminal uses the existing cooked-terminal loop instead of entering
	// readline raw mode. Terminal echo supplies the input transcript, while the
	// shell writes prompts without cursor-control sequences. The default keeps
	// readline editing enabled.
	PlainTerminal bool

	// OnEOF, if non-nil, is consulted when stdin signals EOF (Ctrl-D
	// on an empty line, or the input source running out). Returning
	// true keeps the loop alive — the caller is responsible for any
	// user-facing diagnostic, like bash's "Use 'exit' to leave the
	// shell." nudge driven by IGNOREEOF. Returning false (the
	// default) exits cleanly.
	OnEOF func() bool
}

func currentLang(opts Options, r *interp.Runner, fallback syntax.LangVariant) syntax.LangVariant {
	if opts.LangFunc != nil {
		if lang := opts.LangFunc(r); lang != 0 {
			return lang
		}
	}
	return fallback
}

// route applies opts.Route to a fresh input line. rerouted reports that the
// router replaced the line, which then runs as typed with no continuation.
func route(ctx context.Context, opts Options, r *interp.Runner, line string) (routed string, rerouted bool) {
	if opts.Route == nil {
		return line, false
	}
	routed = opts.Route(ctx, r, line)
	return routed, routed != line
}

func runInput(ctx context.Context, opts Options, r *interp.Runner, input string, fallback syntax.LangVariant, stderr io.Writer, onRunError func(error)) (error, bool) {
	cursor := 0
	for cursor < len(input) {
		lang := currentLang(opts, r, fallback)
		parser := syntax.NewParser(syntax.Variant(lang), syntax.PosixMode(opts.PosixMode))
		restart := false
		for stmt, perr := range parser.StmtsSeq(strings.NewReader(input[cursor:])) {
			if perr != nil {
				_, _ = io.WriteString(stderr, perr.Error()+"\n")
				return perr, false
			}
			if stmt == nil {
				continue
			}
			end := int(stmt.End().Offset())
			if stmt.Semicolon.IsValid() {
				end = int(stmt.Semicolon.Offset()) + 1
			}
			cursor = min(cursor+max(end, 1), len(input))
			runErr := r.Run(ctx, stmt)
			if runErr != nil && !isExitStatus(runErr) {
				onRunError(runErr)
			}
			if r.Exited() {
				return runErr, true
			}
			if currentLang(opts, r, fallback) != lang {
				restart = true
				break
			}
		}
		if !restart {
			return nil, false
		}
	}
	return nil, false
}

// Run starts the interactive read-edit-execute loop. It blocks until the
// user exits (the `exit` builtin or Ctrl-D on an empty line), the parser
// hits EOF, or ctx is canceled.
//
// The returned error is nil on a clean exit and the underlying runner
// error otherwise. A non-zero exit code from a final `exit N` propagates
// as an [interp.ExitStatus] error; callers wanting bash-style "the
// session's exit code is the last command's" should use [errors.As].
func Run(ctx context.Context, opts Options) error {
	if opts.Runner == nil {
		return errors.New("interactive: Runner is required")
	}
	r := opts.Runner

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	lang := opts.Lang
	if lang == 0 {
		lang = syntax.LangBash
	}
	ps1 := opts.PS1
	if ps1 == nil {
		ps1 = func() string { return "$ " }
	}
	ps2 := opts.PS2
	if ps2 == nil {
		ps2 = func() string { return "> " }
	}
	histLimit := opts.HistoryLimit
	if histLimit == 0 {
		histLimit = 1000
	}
	histFile := historyFilePath(r.Dir, opts.HistoryFile, runtime.GOOS == "windows")
	interruptPrompt := opts.InterruptPrompt
	if interruptPrompt == "" {
		interruptPrompt = "^C"
	}
	eofPrompt := opts.EOFPrompt
	if eofPrompt == "" {
		eofPrompt = "exit"
	}
	onRunError := opts.OnRunError
	if onRunError == nil {
		onRunError = func(err error) { _, _ = io.WriteString(stderr, err.Error()+"\n") }
	}

	if opts.Greeting != "" {
		_, _ = io.WriteString(stdout, opts.Greeting)
	}
	r.EnableInteractiveHistory()
	if opts.PlainTerminal {
		return runFallback(ctx, r, stdin, stdout, stderr, lang, opts.PosixMode, ps1, ps2, onRunError, opts.PreCommand, opts.LangFunc, newFileHistory(histFile, histLimit))
	}

	cfg := &readline.Config{
		Prompt:            ps1(),
		HistoryFile:       histFile,
		HistoryLimit:      histLimit,
		HistorySearchFold: opts.HistorySearchFold,
		InterruptPrompt:   interruptPrompt,
		EOFPrompt:         eofPrompt,
		Stdin:             stdin,
		Stdout:            stdout,
		Stderr:            stderr,
		VimMode:           opts.VimMode != nil && opts.VimMode(),
	}
	if opts.ConfigureReadline != nil {
		opts.ConfigureReadline(cfg)
	}
	if !bindTTY(cfg, stdin) && opts.AssumeTTY {
		// Virtual TTY: no kernel terminal behind the stream; the far end
		// (an SSH client that did a pty-req, or a WebSocket-attached
		// xterm.js) already manages raw mode. Drive it with x/term's line
		// editor instead of readline. readline cannot serve this case on
		// Windows: its terminal init unconditionally calls ansi.EnableANSI
		// (terminal.go newTerminal, gated on isInteractive=FuncIsTerminal),
		// which probes the *process* console handles — absent on a daemon
		// — and returns a fatal error, so NewFromConfig fails and Run would
		// silently drop to the echo-less runFallback loop. x/term assumes
		// already-raw I/O over a plain io.ReadWriter and does no console
		// probing, so echo + line editing work over the pipe pair on every
		// platform. (Ctrl-R reverse search is the one readline feature not
		// carried over.)
		return runAssumedTTY(ctx, opts, histFile, r, stdin, stdout, stderr, lang, ps1, ps2, onRunError)
	}

	rl, err := readline.NewFromConfig(cfg)
	if err != nil {
		return runFallback(ctx, opts.Runner, stdin, stdout, stderr, lang, opts.PosixMode, ps1, ps2, onRunError, opts.PreCommand, opts.LangFunc, newFileHistory(histFile, histLimit))
	}
	defer rl.Close()

	// Close the readline instance when ctx is canceled so the in-flight
	// Readline() call returns instead of blocking forever (server shutdown,
	// PTY closed by remote peer, etc.).
	//
	// We intentionally do NOT also check ctx.Done() at the top of the
	// read-eval loop. Some HTTP servers cancel the request context as
	// soon as the response headers are written — for a hijacked WS this
	// happens immediately after the 101 upgrade response goes out, while
	// the underlying conn is still alive. The original parser.Interactive
	// path was unaffected because it only watched ctx after running a
	// statement, and a freshly-spawned shell has no statements to run.
	// Mirror that semantic here: cancellation tears us down via the
	// in-flight Readline closing, not via a pre-emptive return on a ctx
	// the caller may have prematurely tied to the wrong lifetime.
	if ctx.Done() != nil && ctx.Err() == nil {
		stop := context.AfterFunc(ctx, func() { _ = rl.Close() })
		defer stop()
	}

	for {
		if opts.PreCommand != nil {
			opts.PreCommand(ctx, r)
		}

		rl.SetPrompt(ps1())
		if opts.VimMode != nil {
			rl.SetVimMode(opts.VimMode()) // honor a runtime `set -o vi` toggle
		}
		line, err := rl.Readline()
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				continue
			}
			if opts.OnEOF != nil && opts.OnEOF() {
				continue
			}
			return nil // EOF (Ctrl-D) — clean exit
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		routed, rerouted := route(ctx, opts, r, line)
		if routed == "" {
			continue
		}

		// Multi-line continuation: keep reading until the parser is
		// satisfied or hits a real syntax error. parser.Parse leaves the
		// instance in an indeterminate state after returning, so each
		// probe uses a fresh parser — mirrors the cmd/bashy pattern.
		input := routed
		for !rerouted {
			pp := syntax.NewParser(syntax.Variant(currentLang(opts, r, lang)), syntax.PosixMode(opts.PosixMode))
			_, perr := pp.Parse(strings.NewReader(input), "")
			if perr == nil {
				break
			}
			if !pp.Incomplete() {
				break
			}
			rl.SetPrompt(ps2())
			cont, cerr := rl.Readline()
			if cerr != nil {
				// EOF or Ctrl-C mid-continuation — abandon and reprompt.
				input = ""
				break
			}
			input += "\n" + cont
		}
		if input == "" {
			continue
		}
		r.RecordInteractiveHistory(input)

		// Each input chunk is parsed by a fresh parser (line numbers
		// restart at 1), so advance the runner's alias-timing base past
		// the previous chunk. This keeps an alias defined on an earlier
		// prompt expanding on a later one, while a definition and use
		// typed on the same line still do not expand (bash semantics).
		r.AdvanceAliasInput(strings.Count(input, "\n") + 1)
		if runErr, exited := runInput(ctx, opts, r, input, lang, stderr, onRunError); exited {
			return runErr
		}
	}
}

// bindTTY wires custom raw-mode / size / is-terminal callbacks on cfg when
// stdin is an *os.File backed by a TTY whose fd is NOT the daemon's actual
// fd 0. Without this readline's defaults call term.MakeRaw on syscall.Stdin
// — wrong fd, raw mode never reaches the PTY slave the caller asked us to
// drive. The hardcoded fd 0 default works for cmd/bashy (a CLI tool) but
// breaks for embedders driving a separate PTY pair.
//
// Returns whether the binding was installed (stdin really is a TTY file).
func bindTTY(cfg *readline.Config, stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return false
	}
	var saved *term.State
	cfg.FuncIsTerminal = func() bool { return true }
	cfg.FuncMakeRaw = func() error {
		state, err := term.MakeRaw(fd)
		if err == nil {
			saved = state
		}
		return err
	}
	cfg.FuncExitRaw = func() error {
		if saved == nil {
			return nil
		}
		err := term.Restore(fd, saved)
		if err == nil {
			saved = nil
		}
		return err
	}
	cfg.FuncGetSize = func() (int, int) {
		w, h, err := term.GetSize(fd)
		// Some PTY drivers (notably Expect's default spawn PTY) report a
		// successful ioctl with a zero-sized window. Readline treats width 0
		// as a line edge after every rune and renders each typed character as
		// "c<space><backspace>", so automation can no longer observe the
		// terminal's contiguous command echo. A real terminal cannot usefully
		// have zero rows or columns; use the same conservative fallback as an
		// ioctl failure.
		if err != nil || w <= 0 || h <= 0 {
			return 80, 24
		}
		return w, h
	}
	return true
}

// runAssumedTTY is the interactive loop for a virtual terminal — a stream
// whose far end is a real terminal already in raw mode, with no kernel TTY
// (hence no console) on this side. It is driven by [golang.org/x/term]'s
// line editor rather than readline: x/term reads keystrokes and writes echo
// over a single io.ReadWriter, assumes the far end is already raw, and makes
// no platform console calls — so it works on a console-less Windows daemon
// where readline's ANSI-enable step fatally fails. Echo, cursor editing, and
// up/down history navigation all come from x/term; Ctrl-R reverse search is
// the one readline feature this path drops.
//
// The structure mirrors the readline loop above (multi-line continuation,
// per-statement child contexts, OnEOF), so both paths behave identically
// apart from the editor. Command stdout/stderr go straight to the runner's
// own writers (the slave fd), not through x/term — exactly as the readline
// path leaves interp writing to the PTY slave directly.
func runAssumedTTY(ctx context.Context, opts Options, histFile string, r *interp.Runner, stdin io.Reader, stdout, stderr io.Writer, lang syntax.LangVariant, ps1, ps2 func() string, onRunError func(error)) error {
	// The far end is raw, so Ctrl-C arrives as a 0x03 byte; x/term maps that
	// to the same io.EOF it returns for Ctrl-D, which would exit the shell.
	// Strip it from the line-editing input so Ctrl-C is a no-op while
	// editing — during a running command interp reads the raw fd directly,
	// so the command still sees its own Ctrl-C.
	t := term.NewTerminal(rwAdapter{r: ctrlCFilter{r: stdin}, w: stdout}, ps1())
	if h := newFileHistory(histFile, opts.HistoryLimit); h != nil {
		t.History = h
	}
	applySize := func() {
		if opts.GetSize == nil {
			return
		}
		if w, hgt := opts.GetSize(); w > 0 && hgt > 0 {
			_ = t.SetSize(w, hgt)
		}
	}

	// Unblock the in-flight ReadLine when ctx is canceled (server shutdown,
	// remote peer closed the PTY) by closing the underlying stream. Mirrors
	// the readline path's rl.Close-on-cancel; the vpty Close is idempotent,
	// so a later Session.Close is harmless.
	if ctx.Done() != nil && ctx.Err() == nil {
		stop := context.AfterFunc(ctx, func() {
			if c, ok := stdin.(io.Closer); ok {
				_ = c.Close()
			}
		})
		defer stop()
	}

	for {
		if opts.PreCommand != nil {
			opts.PreCommand(ctx, r)
		}
		applySize()
		t.SetPrompt(ps1())
		line, err := t.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if opts.OnEOF != nil && opts.OnEOF() {
					continue
				}
				return nil
			}
			return nil // stream closed (peer hung up / shutdown) — clean exit
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		routed, rerouted := route(ctx, opts, r, line)
		if routed == "" {
			continue
		}

		// Multi-line continuation: keep reading until the parser is
		// satisfied or hits a real syntax error. Same shape as the
		// readline loop, using a fresh parser per probe.
		input := routed
		for !rerouted {
			pp := syntax.NewParser(syntax.Variant(currentLang(opts, r, lang)), syntax.PosixMode(opts.PosixMode))
			_, perr := pp.Parse(strings.NewReader(input), "")
			if perr != nil && !pp.Incomplete() && opts.LangFunc != nil {
				r.RecordInteractiveHistory(input)
				r.AdvanceAliasInput(strings.Count(input, "\n") + 1)
				if runErr, exited := runInput(ctx, opts, r, input, lang, stderr, onRunError); exited {
					return runErr
				}
				input = ""
				break
			}
			if perr == nil || !pp.Incomplete() {
				break
			}
			t.SetPrompt(ps2())
			cont, cerr := t.ReadLine()
			if cerr != nil {
				input = ""
				break
			}
			input += "\n" + cont
		}
		if input == "" {
			continue
		}
		r.RecordInteractiveHistory(input)
		var prog *syntax.File
		if opts.LangFunc == nil {
			// Keep the existing whole-line parse contract when the caller
			// has not opted into statement-by-statement dialect changes.
			parser := syntax.NewParser(syntax.Variant(lang), syntax.PosixMode(opts.PosixMode))
			var perr error
			prog, perr = parser.Parse(strings.NewReader(input), "")
			if perr != nil {
				_, _ = io.WriteString(stderr, perr.Error()+"\n")
				continue
			}
		}

		// Each input chunk is parsed by a fresh parser (line numbers
		// restart at 1), so advance the runner's alias-timing base past
		// the previous chunk. This keeps an alias defined on an earlier
		// prompt expanding on a later one, while a definition and use
		// typed on the same line still do not expand (bash semantics).
		r.AdvanceAliasInput(strings.Count(input, "\n") + 1)
		if opts.LangFunc == nil {
			for _, stmt := range prog.Stmts {
				runErr := r.Run(ctx, stmt)
				if runErr != nil && !isExitStatus(runErr) {
					onRunError(runErr)
				}
				if r.Exited() {
					return runErr
				}
			}
			continue
		}
		if runErr, exited := runInput(ctx, opts, r, input, lang, stderr, onRunError); exited {
			return runErr
		}
	}
}

// rwAdapter joins a separate reader and writer into the single io.ReadWriter
// [term.Terminal] expects.
type rwAdapter struct {
	r io.Reader
	w io.Writer
}

func (a rwAdapter) Read(p []byte) (int, error)  { return a.r.Read(p) }
func (a rwAdapter) Write(p []byte) (int, error) { return a.w.Write(p) }

// ctrlCFilter drops 0x03 (Ctrl-C) bytes from the line-editing input so
// x/term does not surface it as io.EOF and exit the shell. It never returns
// (0, nil): when a read was entirely Ctrl-C it reads again rather than
// signalling a spurious empty read to the line editor.
type ctrlCFilter struct{ r io.Reader }

func (f ctrlCFilter) Read(p []byte) (int, error) {
	for {
		n, err := f.r.Read(p)
		if n > 0 {
			w := 0
			for i := range n {
				if p[i] != 0x03 {
					p[w] = p[i]
					w++
				}
			}
			if w > 0 {
				return w, err
			}
			if err != nil {
				return 0, err
			}
			continue // whole chunk was Ctrl-C; wait for more input
		}
		return n, err
	}
}

// historyFilePath is the history file as the line editor opens it. The
// option arrives in the shell's spelling — bashy hands over $HISTFILE, which
// a script sets to `$TMPDIR/newhistory-$$` and the shell's own `history -w`
// resolves through its path conversion (/tmp is the host temp directory on
// Windows). readline and [newFileHistory] open the file with plain os calls,
// for which `/tmp/x` is the drive-relative C:\tmp\x: the saved lines never
// loaded, so Ctrl-R found nothing (history.tests, history4.sub). Convert
// once, here, the way the interpreter does for its own opens; every other
// host returns the path unchanged.
func historyFilePath(dir, path string, windows bool) string {
	if path == "" {
		return ""
	}
	return pathconv.ToOSMode(dir, path, windows)
}

// fileHistory is an x/term History that mirrors readline's persistent
// history: it loads HistoryFile on construction and appends each new entry,
// keeping the most-recent `limit` lines in memory (index 0 = most recent,
// per the History contract).
type fileHistory struct {
	mu      sync.Mutex
	entries []string
	limit   int
	path    string
}

// newFileHistory returns nil when history is disabled (negative limit), in
// which case the caller keeps x/term's default in-memory ring. A zero limit
// defaults to 1000, matching the readline path.
func newFileHistory(path string, limit int) *fileHistory {
	if limit < 0 {
		return nil
	}
	if limit == 0 {
		limit = 1000
	}
	h := &fileHistory{limit: limit, path: path}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			for ln := range strings.SplitSeq(string(data), "\n") {
				if strings.TrimSpace(ln) != "" {
					h.prepend(ln)
				}
			}
		}
	}
	return h
}

// prepend inserts e as the most-recent entry (index 0), trimming to limit.
func (h *fileHistory) prepend(e string) {
	h.entries = append(h.entries, "")
	copy(h.entries[1:], h.entries)
	h.entries[0] = e
	if len(h.entries) > h.limit {
		h.entries = h.entries[:h.limit]
	}
}

func (h *fileHistory) Add(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepend(entry)
	if h.path != "" {
		if f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.WriteString(entry + "\n")
			_ = f.Close()
		}
	}
}

func (h *fileHistory) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

func (h *fileHistory) At(idx int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entries[idx]
}

// runFallback runs the plain newline-buffered loop when readline cannot
// initialise (typically because Stdin is not a TTY — a pipe, test fixture,
// or genuinely non-interactive context). It is structurally the same loop
// that pre-existed in cmd/bashy/runInteractiveBasic and outpost's
// shell.Session.Run prior to this package — kept here so Run() has a
// single entry point regardless of TTY status.
func runFallback(ctx context.Context, r *interp.Runner, stdin io.Reader, stdout, stderr io.Writer, lang syntax.LangVariant, posixMode bool, ps1, ps2 func() string, onRunError func(error), preCommand func(context.Context, *interp.Runner), langFunc func(*interp.Runner) syntax.LangVariant, history *fileHistory) error {
	r.EnableInteractiveHistory()
	if preCommand != nil {
		preCommand(ctx, r)
	}
	_, _ = io.WriteString(stdout, ps1())
	for {
		var input string
		var prog *syntax.File
		dynamicParse := false
		for {
			line, err := readInteractiveLine(stdin)
			if err != nil && line == "" {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			input += line

			parser := syntax.NewParser(syntax.Variant(currentLang(Options{LangFunc: langFunc}, r, lang)), syntax.PosixMode(posixMode))
			prog, err = parser.Parse(strings.NewReader(input), "")
			if err == nil {
				break
			}
			if parser.Incomplete() {
				_, _ = io.WriteString(stdout, ps2())
				continue
			}
			if langFunc != nil {
				dynamicParse = true
				break
			}
			_, _ = io.WriteString(stderr, err.Error()+"\n")
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
		entry := strings.TrimSuffix(input, "\n")
		r.RecordInteractiveHistory(entry)
		if history != nil {
			history.Add(entry)
		}
		lineCount := strings.Count(input, "\n")
		if !strings.HasSuffix(input, "\n") {
			lineCount++
		}
		r.AdvanceAliasInput(max(lineCount, 1))
		if dynamicParse || langFunc != nil {
			if runErr, exited := runInput(ctx, Options{LangFunc: langFunc, PosixMode: posixMode}, r, input, lang, stderr, onRunError); exited {
				return runErr
			}
		} else {
			for _, stmt := range prog.Stmts {
				runErr := r.Run(ctx, stmt)
				if runErr != nil && !isExitStatus(runErr) {
					onRunError(runErr)
				}
				if r.Exited() {
					return runErr
				}
			}
		}
		if preCommand != nil {
			preCommand(ctx, r)
		}
		_, _ = io.WriteString(stdout, ps1())
	}
}

// readInteractiveLine reads exactly through one newline. In particular, it
// must not use a buffered reader: any following bytes can belong to a command
// that takes ownership of stdin before the next shell prompt, such as the
// editor launched by fc.
func readInteractiveLine(r io.Reader) (string, error) {
	var line strings.Builder
	var one [1]byte
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			line.WriteByte(one[0])
			if one[0] == '\n' {
				return line.String(), nil
			}
		}
		if err != nil {
			if line.Len() > 0 {
				return line.String(), nil
			}
			return "", err
		}
	}
}

// isExitStatus reports whether err is just a non-zero command exit code
// (interp.ExitStatus) rather than a fatal handler error.
func isExitStatus(err error) bool {
	var ec interp.ExitStatus
	return errors.As(err, &ec)
}
