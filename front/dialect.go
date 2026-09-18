// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package front

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Sprint 98, Story #125 (B1): the resolved Bash++ dialect/selector model.
//
// Resolution is shared by the cold CLI and warm-session entry paths. Enabled
// describes the effective grammar/runtime after the startup POSIX policy;
// Source retains the winning selector tier even when that policy disables it.
//
// # Parser entry-path audit
//
// User-source entry paths and the internal parses which remain Classic:
//
//   - main.go, run() (lang := syntax.LangBash/LangPOSIX, ~line 2529) plus its
//     parseOpts/bashyParseOpts closure (~2464) and the statement-recovery
//     loop's parseOnce (~2654) and the direct -c parse (~2708). This is the
//     primary script/-c/stdin execution path. It uses the runner's effective
//     dialect and the resolved startup POSIX profile.
//   - main.go, bashyParseOpts (~2464): translates a requested LangPOSIX into
//     LangBash+PosixMode so `--posix` keeps bash grammar. Startup POSIX
//     suppresses Bash++ rather than combining LangBashPP with PosixMode.
//   - interactive.go, runInteractive (~62, 105, 141): the readline-backed
//     interactive REPL. Delegates per-line parsing to mvdan.cc/sh/v3/interactive
//     with the startup dialect and live statement-boundary reselection.
//   - forced_interactive.go, runForcedInteractiveExec (~224): the non-TTY
//     `bash -i` emulation; same session-wide Lang shape as the interactive
//     REPL. runnerExpand (~191) parses a synthetic `${...}` snippet for
//     prompt/HISTFILE bookkeeping, not user source, and is expected to stay
//     plain LangBash regardless of the resolved dialect.
//   - session.go, RunSessionCommand (~98): the live-session socket command
//     path (the agentic surface's warm-session analogue of -c).
//   - main.go, completeStmtBeforeLine (~3994): a diagnostic-only re-parse
//     used solely to shape bash-format parse-error output; intentionally
//     bash-fixed, not a candidate for dialect wiring.
//   - main.go, registerDefaultFuncs (~831) and importBashFuncs (~873): parse
//     bashy-authored preamble source and inherited BASH_FUNC_* environment
//     functions respectively. Both are always plain Bash by construction —
//     not user Bash++ source — and stay out of scope.
//   - main.go, the BASH_EXECUTION_STRING bookkeeping assignment (~2621): an
//     internal synthetic assignment, not user source.
//
// # Precedence
//
// explicit CLI > environment > .bpp extension > binary default
//
// each tier is winner-take-all: the first tier that expresses an opinion
// decides the result and lower tiers are not consulted.

// BashPPBinary identifies which of the two compiled entry points
// ([BashPPBinaryBash], cmd/bash's pure Bash 5.3 drop-in, or
// [BashPPBinaryBashy], cmd/bashy's AgentOS shell) is resolving the dialect.
// It is the "binary default" tier and identifies the pure bash front door
// whose Sprint 114 POSIX+Bash++ combination is an inert compatibility profile.
type BashPPBinary string

const (
	BashPPBinaryBash  BashPPBinary = "bash"
	BashPPBinaryBashy BashPPBinary = "bashy"
)

// bashPPBinaryDefault is the last-resort tier: Bash++ is off on the pure bash
// front door and on by default in bashy (independently overridable, like its
// agentic surface).
func (b BashPPBinary) bashPPDefault() bool {
	return b == BashPPBinaryBashy
}

// BashPPSource names which precedence tier decided a [BashPPResolution].
// (The Go identifiers keep the BashPP prefix from the Bash++ era; the
// language is Bash# — see docs/naming-collision.md.)
type BashPPSource string

const (
	BashPPSourceCLI           BashPPSource = "cli"            // --bashsharp / --no-bashsharp (+ deprecated --bashpp / --bash++ / --no-bashpp)
	BashPPSourceEnv           BashPPSource = "env"            // BASHY_BASHSHARP=1|0 (BASHY_BASHPP deprecated)
	BashPPSourceExtension     BashPPSource = "extension"      // .bsh (.bpp deprecated)
	BashPPSourceBinaryDefault BashPPSource = "binary-default" // bash off, bashy on
)

// Explicit reports whether the source is a deliberate user request (a CLI
// flag or an environment variable) rather than an inferred default (the file
// extension or the binary's product default).
func (s BashPPSource) Explicit() bool {
	return s == BashPPSourceCLI || s == BashPPSourceEnv
}

// BashPPSelector is the input to [ResolveBashPP]: everything the precedence
// chain reads to decide the initial grammar for one parse. It takes an
// argv-shaped slice and an env lookup func, rather than reading os.Args and
// os.Environ directly, so resolution stays a pure, independently testable
// function.
type BashPPSelector struct {
	// Binary is which compiled entry point is asking. Required.
	Binary BashPPBinary
	// Args is an os.Args-shaped slice (Args[0] is the program name, not
	// scanned). May be nil.
	Args []string
	// LookupEnv resolves an environment variable by name, in the shape of
	// os.LookupEnv. May be nil, meaning no environment tier.
	LookupEnv func(name string) (value string, ok bool)
	// Filename is the script path about to be parsed ("" for -c/stdin/
	// interactive input). The .bpp convention is only a source label for the
	// Bash++-default bashy binary; the bash drop-in requires an explicit
	// CLI/environment selector.
	Filename string
	// Posix is the already-resolved startup POSIX mode (effectiveStartupPosix
	// in main.go), not merely the --posix flag's literal presence.
	Posix bool
}

// BashPPResolution is the effective startup dialect and POSIX profile. Source
// identifies the winning selector tier before applying the POSIX policy.
type BashPPResolution struct {
	Enabled bool
	Source  BashPPSource
	Posix   bool
	// Deprecated is the Bash++-era spelling that decided this resolution
	// ("--bashpp", "BASHY_BASHPP", ".bpp"), or "" when a Bash# spelling or a
	// default did. The aliases are kept for one minor release (Sprint 212
	// D3, the rail5/bashpp collision); a front prints [DeprecationNotice]
	// once when it is non-empty.
	Deprecated string
}

// DeprecationNotice is the one-line stderr notice for a deprecated spelling.
func (r BashPPResolution) DeprecationNotice() string {
	switch r.Deprecated {
	case "":
		return ""
	case ".bpp":
		return "warning: the .bpp extension is deprecated; name Bash# scripts .bsh (the alias is removed after one minor release)"
	case "BASHY_BASHPP":
		return "warning: BASHY_BASHPP is deprecated; use BASHY_BASHSHARP (the alias is removed after one minor release)"
	default:
		return "warning: " + r.Deprecated + " is deprecated; use " + strings.Replace(strings.Replace(r.Deprecated, "bash++", "bashsharp", 1), "bashpp", "bashsharp", 1) + " (the alias is removed after one minor release)"
	}
}

// LangVariant returns the concrete construction-time interpreter dialect.
func (r BashPPResolution) LangVariant() syntax.LangVariant {
	if r.Enabled && !r.Posix {
		return syntax.LangBashPP
	}
	return syntax.LangBash
}

// ParserOptions retains Bash grammar and applies the POSIX semantic profile.
// POSIX input never selects Bash++ grammar, including an explicit LangPOSIX
// base or a caller-constructed resolution with both booleans set.
func (r BashPPResolution) ParserOptions(base syntax.LangVariant, extra ...syntax.ParserOption) []syntax.ParserOption {
	posix := r.Posix || base == syntax.LangPOSIX
	if base == syntax.LangPOSIX {
		base = syntax.LangBash
	}
	if posix && base == syntax.LangBashPP {
		base = syntax.LangBash
	}
	if r.Enabled && !posix {
		base = syntax.LangBashPP
	}
	return append([]syntax.ParserOption{syntax.Variant(base), syntax.PosixMode(posix)}, extra...)
}

// ResolveBashPP resolves the initial Bash++ dialect for one selector,
// applying the documented precedence (explicit CLI > environment > .bpp
// extension > binary default). For the pure bash front door, .bpp is only a
// filename and an affirmative Bash++ selector paired with startup POSIX mode
// selects the Sprint 114 inertness profile: both extensions and POSIX-mode
// parser/runtime differences are disabled so its result is byte-identical to
// the selector-off, POSIX-off invocation. Ordinary bash --posix and explicit
// --no-bashpp retain the POSIX profile. The bashy front door always retains
// startup POSIX semantics while disabling Bash++ grammar/runtime, regardless
// of which selector tier won. Source still reports that tier.
func ResolveBashPP(sel BashPPSelector) (BashPPResolution, error) {
	enabled, source, deprecated := resolveTiers(sel)
	if sel.Binary == BashPPBinaryBash && sel.Posix && enabled {
		return BashPPResolution{Source: source, Deprecated: deprecated}, nil
	}
	return BashPPResolution{Enabled: enabled && !sel.Posix, Source: source, Posix: sel.Posix, Deprecated: deprecated}, nil
}

// ResolveBashPPTiers is the precedence chain alone: the effective selector
// and the tier that decided it, before the POSIX policy.
func ResolveBashPPTiers(sel BashPPSelector) (bool, BashPPSource) {
	enabled, source, _ := resolveTiers(sel)
	return enabled, source
}

func resolveTiers(sel BashPPSelector) (bool, BashPPSource, string) {
	if enabled, seen, word := scanCommandLine(sel.Args); seen {
		return enabled, BashPPSourceCLI, deprecatedFlag(word)
	}
	if enabled, seen, name := envBashPP(sel.LookupEnv); seen {
		deprecated := ""
		if name == "BASHY_BASHPP" {
			deprecated = name
		}
		return enabled, BashPPSourceEnv, deprecated
	}
	if sel.Binary == BashPPBinaryBashy {
		if strings.HasSuffix(sel.Filename, ".bsh") {
			return true, BashPPSourceExtension, ""
		}
		if strings.HasSuffix(sel.Filename, ".bpp") {
			return true, BashPPSourceExtension, ".bpp"
		}
	}
	return sel.Binary.bashPPDefault(), BashPPSourceBinaryDefault, ""
}

// deprecatedFlag names a Bash++-era selector flag, or "" for a Bash# one.
func deprecatedFlag(word string) string {
	switch word {
	case "--bashpp", "--bash++", "--no-bashpp":
		return word
	}
	return ""
}

// CommandLineBashPP resolves the last of --bashsharp/--no-bashsharp (and the
// deprecated --bashpp/--bash++/--no-bashpp aliases) on the command line,
// mirroring commandLinePosixMode's shape in main.go: scanning stops at "--",
// at "-c" (whose operand is a command string, not a further flag), or at the
// first operand that does not start with "-" (the script path, after which
// remaining words are script arguments). The spellings are exact aliases;
// none creates a separate mode.
func CommandLineBashPP(args []string) (enabled, seen bool) {
	enabled, seen, _ = scanCommandLine(args)
	return enabled, seen
}

// IsSelectorFlag reports whether arg is one of the dialect selector flags, in
// any spelling — for argv scanners that must consume them before Go's flag
// package sees them.
func IsSelectorFlag(arg string) bool {
	switch arg {
	case "--bashsharp", "--no-bashsharp", "--bashpp", "--bash++", "--no-bashpp":
		return true
	}
	return false
}

func scanCommandLine(args []string) (enabled, seen bool, word string) {
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--":
			return enabled, seen, word
		case "--bashsharp", "--bashpp", "--bash++":
			enabled, seen, word = true, true, args[i]
		case "--no-bashsharp", "--no-bashpp":
			enabled, seen, word = false, true, args[i]
		case "-c":
			return enabled, seen, word
		default:
			// A value-taking invocation option consumes the following token.
			// It is not a script operand, so selectors after it must still be
			// scanned: `--source go --bashpp x.go` selects Bash++.
			if InvocationFlagTakesValue(args[i]) {
				if i+1 < len(args) {
					i++
				}
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				return enabled, seen, word
			}
		}
	}
	return enabled, seen, word
}

// envBashPP resolves BASHY_BASHSHARP=1|0, then the deprecated BASHY_BASHPP.
// Any other value (unset, or set to something other than "1"/"0") is treated
// as this tier having no opinion, falling through to the next precedence
// tier, rather than as an error — this mirrors how the equivalent
// POSIXLY_CORRECT/SHELLOPTS checks in main.go treat presence, not spelling
// validation, as the signal. The third result names the variable that decided.
func envBashPP(lookupEnv func(string) (string, bool)) (enabled, seen bool, name string) {
	if lookupEnv == nil {
		return false, false, ""
	}
	for _, name := range []string{"BASHY_BASHSHARP", "BASHY_BASHPP"} {
		raw, ok := lookupEnv(name)
		if !ok {
			continue
		}
		switch raw {
		case "1":
			return true, true, name
		case "0":
			return false, true, name
		}
	}
	return false, false, ""
}
