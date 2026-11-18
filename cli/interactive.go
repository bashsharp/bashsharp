// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ergochat/readline"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interactive"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// ignoreEOFLimit decodes the IGNOREEOF environment variable into the
// number of *additional* EOFs to tolerate before exiting. An unset
// or empty value disables the feature (ok=false). A non-numeric value
// behaves like bash's documented default of 10.
func ignoreEOFLimit(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 0 {
			n = 0
		}
		return n, true
	}
	return 10, true
}

// setHistCmd publishes the interactive command counter as $HISTCMD.
// Bash only sets HISTCMD when history is enabled (interactive mode),
// so we update it here rather than in lookupVar.
func setHistCmd(r *interp.Runner, n int) {
	if r.Vars == nil {
		r.Vars = make(map[string]expand.Variable)
	}
	r.Vars["HISTCMD"] = expand.Variable{
		Set:  true,
		Kind: expand.String,
		Str:  strconv.Itoa(n),
	}
}

// singleQuote wraps s as a single POSIX shell word with no expansion, escaping
// embedded single quotes using the standard POSIX shell escaping pattern.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runInteractive(r *interp.Runner, stdin *os.File, stdout, stderr io.Writer) error {
	// POSIX suppresses Bash++ grammar even when a live toggle has selected a
	// latent Bash++ dialect. Translate the effective POSIX variant to Bash:
	// the drop-in retains arrays and parameter extensions, with POSIX behavioral
	// parse rules supplied separately by PosixMode. After set +o posix, the
	// effective getter can expose the selected Bash++ dialect again.
	langForRunner := func(r *interp.Runner) syntax.LangVariant {
		lang := r.LangVariant()
		if lang == syntax.LangPOSIX {
			return syntax.LangBash
		}
		return lang
	}
	lang := langForRunner(r)
	posixMode := resolvedStartupPosix()

	var cmdNum int
	getPrompt := func(ps string) string {
		defaultPS := `\u@\h:\w\$ `
		if ps == "PS1" && invokedAsSh() {
			defaultPS = `sh-\v\$ `
		}
		if ps == "PS2" {
			defaultPS = "> "
		}
		// Read PS1/PS2 from the LIVE variable scope, not r.Env: r.Env holds
		// only the initial environment, so an in-session `PS1=...` (re)assignment
		// would otherwise never reach the prompt. POSIX behavior #29 (parameter
		// expansion on PS1/PS2 in posix mode) depends on this too.
		vr := r.LiveVar(ps)
		val := vr.String()
		if !vr.IsSet() {
			val = defaultPS
		}
		envGet := func(name string) string { return r.LiveVar(name).String() }
		return expandPrompt(val, envGet, cmdNum, cmdNum, posixMode)
	}

	histFile := r.Env.Get("HISTFILE").String()
	if histFile == "" {
		// Bash assigns a default to $HISTFILE: ~/.sh_history in POSIX mode
		// (behavior #30), ~/.bash_history otherwise. bashy keeps its own
		// ~/.bashy_history name for the non-posix default but matches the
		// required ~/.sh_history under --posix.
		name := ".bashy_history"
		if posixMode {
			name = ".sh_history"
		}
		if home, _ := os.UserHomeDir(); home != "" {
			histFile = filepath.Join(home, name)
		}
		// Expose the default to scripts as a (non-exported) shell variable, as
		// bash does, by running a real assignment through the runner: r.Vars is
		// only an output mirror (cleared at Reset), so the value must land in
		// the live variable scope to be visible to ${HISTFILE}.
		if histFile != "" {
			assign := "HISTFILE=" + singleQuote(histFile)
			p := syntax.NewParser(syntax.Variant(lang), syntax.PosixMode(posixMode))
			if prog, perr := p.Parse(strings.NewReader(assign), "histfile-init"); perr == nil {
				_ = r.Run(context.Background(), prog)
			}
		}
	}

	var eofPresses int
	ps1 := func() string { return getPrompt("PS1") }
	return interactive.Run(context.Background(), interactive.Options{
		Runner:    r,
		Lang:      lang,
		LangFunc:  langForRunner,
		PosixMode: posixMode,
		Stdin:     stdin,
		// Prompts and line-editing echo are terminal UI, which bash writes to
		// stderr. Keep command stdout on the runner's independently configured
		// stream so `sh -s >output` captures only command output.
		Stdout:            stderr,
		Stderr:            stderr,
		PS1:               ps1,
		PS2:               func() string { return getPrompt("PS2") },
		VimMode:           r.VimMode, // `set -o vi` switches to vi editing (re-read each prompt)
		HistoryFile:       histFile,
		HistoryLimit:      1000,
		HistorySearchFold: true,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		PlainTerminal:     invokedAsSh(),
		ConfigureReadline: func(cfg *readline.Config) {
			// GNU Bash invoked as POSIX sh does not place a terminal cursor
			// position query in the interactive transcript. Expect-style
			// automation treats readline's DSR probe and its padding bytes as
			// shell output, so suppress only that optional probe in POSIX mode.
			cfg.DisableCursorPositionQuery = posixMode
		},
		PreCommand: func(ctx context.Context, r *interp.Runner) {
			AgentOSObserveDirectory(r.Dir)
			if pc := r.Env.Get("PROMPT_COMMAND").String(); pc != "" {
				pcp := syntax.NewParser(syntax.Variant(lang), syntax.PosixMode(posixMode))
				if prog, err := pcp.Parse(strings.NewReader(pc), "PROMPT_COMMAND"); err == nil {
					_ = r.Run(ctx, prog)
				}
			}
			eofPresses = 0
			cmdNum++
			setHistCmd(r, cmdNum)
		},
		// IGNOREEOF asks the shell to tolerate N additional Ctrl-D
		// presses before actually exiting on EOF. Returning true here
		// keeps the interactive loop running; false (default) lets it
		// exit on the first EOF as bash does without IGNOREEOF.
		OnEOF: func() bool {
			limit, ok := ignoreEOFLimit(r.Env.Get("IGNOREEOF").String())
			if !ok || eofPresses >= limit {
				return false
			}
			eofPresses++
			_, _ = io.WriteString(stderr, "Use \"exit\" to leave the shell.\n")
			return true
		},
		OnRunError: func(err error) {
			_, _ = io.WriteString(stderr, "bashy: "+err.Error()+"\n")
		},
	})
}
