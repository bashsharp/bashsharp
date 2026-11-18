// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

// bashy is a Bash 5.3 compatible shell built on top of [interp].
package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bashsharp/bashsharp/front"

	"golang.org/x/term"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/qiangli/bash/pathconv"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

var (
	command       = flag.String("c", "", "command to be executed")
	commandGiven  bool // -c with an empty command still selects command-string mode
	posix         = flag.Bool("posix", false, "POSIX mode")
	norc          = flag.Bool("norc", false, "do not read ~/.bashyrc")
	noprofile     = flag.Bool("noprofile", false, "do not read /etc/profile or ~/.bashy_profile")
	login         = flag.Bool("login", false, "act as a login shell")
	restricted    = flag.Bool("restricted", false, "run as a restricted shell")
	pretty        = flag.Bool("pretty-print", false, "pretty-print shell input")
	forceI        = flag.Bool("i", false, "force the shell to run interactively")
	plusI         = flag.Bool("bashy-plus-i", false, "force the shell non-interactive (`+i`); internal")
	readStdin     = flag.Bool("s", false, "read commands from standard input")
	oneCmd        = flag.Bool("t", false, "exit after reading and executing one command")
	dumpStrs      = flag.Bool("dump-strings", false, "dump translatable strings and exit")
	dumpPO        = flag.Bool("dump-po-strings", false, "dump translatable strings in PO format and exit")
	dumpShort     = flag.Bool("D", false, "dump translatable strings and exit")
	verbose       = flag.Bool("verbose", false, "echo shell input lines as they are read")
	noediting     = flag.Bool("noediting", false, "disable readline editing")
	debugger      = flag.Bool("debugger", false, "enable debugger profile")
	debug         = flag.Bool("debug", false, "enable debugger profile")
	startupBashPP front.BashPPResolution
	originalArgs  []string
	optsOn        multiFlag
	optsOff       multiFlag
	setOff        multiFlag
	shoptOff      multiFlag
)

// Sprint 118: the Go-source selection. startupGoSourceSel is what
// --source/--check/--go-file spelled, captured before flag.Parse;
// startupGoSourceErr carries a scan failure so it is reported on the normal
// exit path; startupGoSource is the validated result. See gosource.go.
var (
	startupGoSourceSel front.GoSourceSelection
	startupGoSourceErr error
	startupGoSource    front.GoSourceResolution
)

// multiFlag collects repeated string values for a flag, e.g. -o opt.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func init() {
	flag.Var(&optsOn, "o", "enable a set option (posix, errexit, xtrace, ...); may be repeated")
	flag.Var(&optsOff, "O", "enable a shopt option; may be repeated")
	flag.Var(&setOff, "bashy-plus-o", "disable a set option; internal")
	flag.Var(&shoptOff, "bashy-plus-O", "disable a shopt option; internal")
	flag.Usage = bashUsage
}

func bashUsage() {
	fmt.Fprint(os.Stderr, `bash [GNU long option] [option] ...
bash [GNU long option] [option] script-file ...
GNU long options:
	--debug
	--debugger
	--dump-po-strings
	--dump-strings
	--help
	--init-file
	--login
	--noediting
	--noprofile
	--norc
	--posix
	--pretty-print
	--rcfile
	--restricted
	--verbose
	--version
Shell options:
	-ilrsD or -c command or -O shopt_option		(invocation only)
	-abefhkmnptuvxBCEHPT or -o option
`)
	if extra := AgentOSUsage(); extra != "" {
		fmt.Fprint(os.Stderr, extra)
	}
}

func preflightInvocationErrors(args []string) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return
		}
		if arg == "-c" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "bash: -c: option requires an argument")
				os.Exit(2)
			}
			return
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return
		}
		if arg == "--init-file" || arg == "--rcfile" {
			i++
			continue
		}
		switch arg {
		case "--badopt", "--initfile", "-q":
			fmt.Fprintf(os.Stderr, "bash: %s: invalid option\n", arg)
			bashUsage()
			os.Exit(2)
		}
	}
}

func stripBashPPInvocationFlags(args []string) []string {
	out := make([]string, 0, len(args))
	options := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i == 0 {
			out = append(out, arg)
			continue
		}
		if options && (arg == "-c" || arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-")) {
			options = false
		}
		if !options {
			out = append(out, arg)
			continue
		}
		if front.IsSelectorFlag(arg) {
			continue
		}
		out = append(out, arg)
		// A value-taking option's value is not the script operand: step over
		// it so a selector spelled behind it is still seen. Sharing
		// front.InvocationFlagTakesValue with the other argv scanners is what keeps
		// `--rcfile F --bashpp` and `--source go --bashpp` working.
		if front.InvocationFlagTakesValue(arg) && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}

// stripIgnoredOperandHyphen drops the single leading `-` (or, in `-c`
// mode, a `--`) operand that bash ignores between the options and the
// command/script operand. Bash's parse_shell_options stops at the first
// non-option, then skips a lone `-` before taking the `-c` command string
// or the script file (main.c). Go's flag package instead lets `-c` swallow
// the immediately-following `-`/`--` as its value, so `bin/bash -c - 'echo x'`
// and `bin/bash -c -- 'echo x'` would run `-`/`--` as the command, and a lone
// `bin/bash -` would treat `-` as a script file. Removing that token here
// makes Go's parse match bash:
//
//	-c -  CMD   -> -c CMD          (single hyphen ignored)
//	-c -- CMD   -> -c CMD          (double hyphen ends options)
//	-           -> (read stdin)    (lone hyphen ignored)
//
// args includes argv[0]. Value-taking options (`-o`, `-O`, `--rcfile`,
// `--init-file`) consume their following token; `-c` does not (its command is
// an operand in bash). Any other token starting with `-` is treated as a
// boolean option.
func stripIgnoredOperandHyphen(args []string) []string {
	valueOpt := map[string]bool{
		"-o": true, "-O": true, "--rcfile": true, "--init-file": true,
		"-bashy-plus-o": true, "-bashy-plus-O": true,
	}
	sawC := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			// End of options. In `-c` mode this `--` precedes the command
			// string; drop it so Go's `-c` binds to the real command.
			// Without `-c`, Go's flag parser already handles `--`.
			if sawC {
				return append(append([]string{}, args[:i]...), args[i+1:]...)
			}
			return args
		case a == "-":
			// A lone hyphen at the operand position: bash ignores it.
			return append(append([]string{}, args[:i]...), args[i+1:]...)
		case strings.HasPrefix(a, "-"):
			if a == "-c" {
				sawC = true
				continue
			}
			if valueOpt[a] {
				i++ // skip this option's value token
			}
			continue
		default:
			// First real operand (command string or script file): nothing
			// to strip.
			return args
		}
	}
	return args
}

// commandStringMode preserves direct run/newRunner callers that set command
// without going through Main, while commandGiven distinguishes -c "".
func commandStringMode() bool { return commandGiven || *command != "" }

// Go's flag parser keeps consuming options after -c's command value. The
// shell's option scan ends at that value: any following -s, -e, or similar
// token is $0 (or a later positional parameter), not a shell option.
func stopOptionsAfterCommand(args []string) ([]string, bool) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "-c" {
			if i+2 >= len(args) {
				return args, true
			}
			out := make([]string, 0, len(args)+1)
			out = append(out, args[:i+2]...)
			out = append(out, "--")
			return append(out, args[i+2:]...), true
		}
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			return args, false
		}
		if front.InvocationFlagTakesValue(a) || a == "-bashy-plus-o" || a == "-bashy-plus-O" {
			i++
		}
	}
	return args, false
}

// relocatePendingCommandFlag makes `-c` behave as in bash: `-c` only marks
// that a command string is pending (shell.c: want_pending_command); option
// parsing continues past it, and the command string is the first NON-OPTION
// argument found after all options (shell.c: command_execution_string =
// argv[arg_index] after the option loop). Go's flag package instead binds
// `-c` to the immediately following token, so `bash -c -l 'cmd'` — the exact
// shape Claude Code uses to generate its shell snapshot — swallowed `-l` as
// the command string and ran the real command as $0 ("-l: command not
// found", exit 127). Rewrite argv so `-c` sits directly before the first
// non-option argument; downstream parsing then matches bash:
//
//	-c -l CMD      -> -l -c CMD
//	-lc -e CMD     -> -l -e -c CMD   (c pulled out of the cluster)
//	-c -o opt CMD  -> -o opt -c CMD
//	-c CMD         -> -c CMD         (unchanged)
//
// A `--` or lone `-` ends the option scan (the command string follows); `-c`
// is inserted before it and stripIgnoredOperandHyphen drops the hyphen
// operand as usual. If no operand follows at all, `-c` lands last and
// preflightInvocationErrors reports bash's "-c: option requires an argument".
func relocatePendingCommandFlag(args []string) []string {
	sawC := false
	i := 1
	for i < len(args) {
		a := args[i]
		if len(a) < 2 || a == "--" || (a[0] != '-' && a[0] != '+') {
			break
		}
		if a == "-c" {
			args = append(append([]string{}, args[:i]...), args[i+1:]...)
			sawC = true
			continue
		}
		isCluster := a[0] == '-' && a[1] != '-' && isShortFlagCluster(a[1:])
		if isCluster && strings.ContainsRune(a[1:], 'c') {
			// Pull the pending-command flag out of the cluster and
			// reprocess whatever remains of it.
			args = append([]string{}, args...)
			args[i] = "-" + strings.ReplaceAll(a[1:], "c", "")
			sawC = true
			if args[i] == "-" {
				args = append(args[:i], args[i+1:]...)
			}
			continue
		}
		// Value-taking options keep their value token, including a
		// cluster whose trailing flag takes a value (`-eo pipefail`).
		switch {
		case a == "+o" || a == "+O" || front.InvocationFlagTakesValue(a):
			i += 2
		case isCluster && (a[len(a)-1] == 'o' || a[len(a)-1] == 'O'):
			i += 2
		default:
			i++
		}
	}
	if !sawC {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[:i]...)
	out = append(out, "-c")
	out = append(out, args[i:]...)
	return out
}

// isShortFlagCluster reports whether s (an option token without its leading
// hyphen) consists solely of ASCII letters, i.e. could be a bash combined
// short-flag cluster.
func isShortFlagCluster(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return len(s) > 0
}

// splitCombinedShortFlags rewrites bash-style short / combined
// short flags into the long-form names our `flag` parser knows.
// `-ce 'cmd'` becomes `-o errexit -c 'cmd'`, `-eu` becomes
// `-o errexit -o nounset`, and so on. `-c` is value-taking so the
// emitted `-c` goes last in the cluster, just before its argument.
// Unknown clusters pass through untouched.
func splitCombinedShortFlags(args []string) []string {
	// Map of bash short-flag letters to their long set-option name.
	shortToOpt := map[byte]string{
		'a': "allexport",
		'b': "notify",
		'e': "errexit",
		'f': "noglob",
		'h': "hashall",
		'k': "keyword",
		'm': "monitor",
		'n': "noexec",
		'u': "nounset",
		'v': "verbose",
		'x': "xtrace",
		'p': "privileged",
		'r': "restricted",
		'C': "noclobber",
		'E': "errtrace",
		'H': "histexpand",
		'P': "physical",
		'T': "functrace",
	}
	out := make([]string, 0, len(args))
	out = append(out, args[0])
	// Once we see a non-flag argument (or the literal `--`), it's
	// the script path or end-of-flags. Everything after must be
	// passed through untouched: those tokens belong to the script
	// (positional `$@`) and may legitimately look like combined
	// flags (e.g. `bashy ./script.sh -ac` should give the script
	// `-ac` as $1, not pre-split it into shell options).
	endOfFlags := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		if endOfFlags {
			out = append(out, a)
			continue
		}
		// Preserve value-taking options as a pair and keep scanning after the
		// value. Treating the option name alone as complete made its value look
		// like the script operand, so a later +o/-o was never normalized.
		if a == "-o" || a == "-O" || a == "--rcfile" || a == "--init-file" {
			out = append(out, a)
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		}
		if a == "+O" {
			out = append(out, "-bashy-plus-O")
			continue
		}
		if a == "+o" {
			out = append(out, "-bashy-plus-o")
			continue
		}
		if a == "+B" {
			out = append(out, "-bashy-plus-o", "braceexpand")
			continue
		}
		if a == "-B" {
			out = append(out, "-o", "braceexpand")
			continue
		}
		// `+<short>` (e.g. +m, +e, +x) disables a set option, the command-line
		// mirror of `set +<short>`. bash accepts the full +abefhkmnptuvxBCEHPT
		// set; map any known short option to its long name and disable it.
		if len(a) == 2 && a[0] == '+' {
			if opt, ok := shortToOpt[a[1]]; ok {
				out = append(out, "-bashy-plus-o", opt)
				continue
			}
		}
		if a == "-l" {
			out = append(out, "-login")
			continue
		}
		if a == "-i" || a == "-s" || a == "-D" {
			// Invocation-only flag, not a set option.
			out = append(out, a)
			continue
		}
		if a == "+i" {
			// bash accepts `+i` (the off-form of `-i`): it requests a
			// non-interactive shell. Without this it was mistaken for a
			// script path ("+i: No such file or directory"), which broke
			// every invocation of the form `bashy +i ...` — notably the
			// entire yash signal-disposition suite (sig*-p), invoked as
			// `bashy +i +m`.
			out = append(out, "-bashy-plus-i")
			continue
		}
		if a == "+s" || a == "+D" {
			// Off-form of the other invocation-only flags; bash accepts
			// them as no-ops here.
			continue
		}
		if len(a) == 2 && a[0] == '-' {
			if opt, ok := shortToOpt[a[1]]; ok {
				out = append(out, "-o", opt)
				continue
			}
		}
		if len(a) <= 2 || a[0] != '-' || a[1] == '-' {
			if !(len(a) > 0 && a[0] == '-' && (len(a) == 1 || a[1] == '-')) {
				// First non-flag arg: it's the script path.
				// Everything after this is positional for the
				// script.
				endOfFlags = true
			} else if a == "--" {
				// Literal `--` ends flag parsing; emit it so
				// Go's flag package sees it, then pass rest
				// through.
				endOfFlags = true
			}
			out = append(out, a)
			continue
		}
		valueFlag := byte(0)
		if last := a[len(a)-1]; last == 'o' || last == 'O' {
			valueFlag = last
		}
		allKnown := true
		for j := 1; j < len(a); j++ {
			if valueFlag != 0 && j == len(a)-1 {
				continue
			}
			if _, ok := shortToOpt[a[j]]; !ok && a[j] != 'c' && a[j] != 'l' && a[j] != 'i' && a[j] != 's' && a[j] != 'D' {
				allKnown = false
				break
			}
		}
		if !allKnown {
			out = append(out, a)
			continue
		}
		var bools, vals []byte
		for j := 1; j < len(a); j++ {
			if valueFlag != 0 && j == len(a)-1 {
				continue
			}
			if a[j] == 'c' {
				vals = append(vals, a[j])
			} else {
				bools = append(bools, a[j])
			}
		}
		for _, c := range bools {
			if c == 'l' {
				out = append(out, "-login")
				continue
			}
			if c == 'i' || c == 's' || c == 'D' {
				out = append(out, "-i")
				if c == 's' {
					out[len(out)-1] = "-s"
				} else if c == 'D' {
					out[len(out)-1] = "-D"
				}
				continue
			}
			out = append(out, "-o", shortToOpt[c])
		}
		for _, c := range vals {
			out = append(out, "-"+string(c))
		}
		if valueFlag != 0 {
			out = append(out, "-"+string(valueFlag))
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		}
	}
	return out
}

// AgentOSDispatch and AgentOSWireExec are the injection points for the AgentOS
// surface. The pure `bash` drop-in (cmd/bash) leaves them at their no-op
// defaults, so its import graph never touches coreutils. The `bashy` system
// shell (cmd/bashy) sets them to the internal/agentos implementations. This is
// what keeps the two binaries independent.
var (
	AgentOSDispatch = func() {}

	// AgentOSShutdown runs immediately before the shell exits, on EVERY exit path.
	//
	// A shell exits with os.Exit, and os.Exit DOES NOT RUN DEFERRED FUNCTIONS. So a
	// `defer shutdown()` in main() is dead code in a shell — which is exactly how the
	// first version of bashy's OTel wiring shipped: it built, five unit tests passed
	// against an in-memory recorder, and a real collector received ZERO BYTES, because
	// the batch span processor was never flushed.
	//
	// A test that mocks the emitter proves the emitter was CALLED. It does not prove the
	// data arrived. Only an exit hook does, in a program that exits like this one.
	AgentOSShutdown = func() {}
	// AgentOSShellStartup runs once before a shell runner captures its
	// environment: the AgentOS shell mints its model-door session there
	// (Sprint 302 Q5). A no-op for the pure bash drop-in.
	AgentOSShellStartup = func() {}
	// AgentOSObserveDirectory records directory use without retaining commands.
	AgentOSObserveDirectory = func(string) {}
	// posix is passed so AgentOS extensions (e.g. --dry-run) stay inert under
	// --posix and absent from the pure bash drop-in.
	AgentOSWireExec func([]interp.RunnerOption, bool, []string, io.Reader, io.Writer, io.Writer) []interp.RunnerOption = func(o []interp.RunnerOption, _ bool, _ []string, in io.Reader, out, err io.Writer) []interp.RunnerOption {
		return append(o, interp.StdIO(in, out, err))
	}

	// AgentOSPreamble returns shell source defining default functions/aliases
	// (e.g. `docker() { … bashy podman … }`) registered BEFORE user startup files
	// so the user can override them. No-op for the pure `bash` drop-in.
	AgentOSPreamble = func() string { return "" }

	// AgentOSUsage returns bashy-only usage text appended to `--help`. The pure
	// bash drop-in leaves it empty so its help stays close to GNU Bash.
	AgentOSUsage = func() string { return "" }

	// AgentOSCommandLineNoExec lets the bashy-only front end request the
	// interpreter's parse-only path without exposing a non-Bash option through
	// the pure bash binary. It is used to make --dry-run --posix fail safe:
	// POSIX mode does not install AgentOS execution handlers, so the combination
	// must validate input without executing it.
	AgentOSCommandLineNoExec func(bool) bool = func(bool) bool { return false }
	// AgentOSStrictPosixParse selects the portable POSIX grammar for an
	// AgentOS validation request. Ordinary bash --posix keeps Bash grammar.
	AgentOSStrictPosixParse func(bool) bool = func(bool) bool { return false }
	// AgentOSBashPPDefault identifies the bashy entry point without coupling the
	// shared CLI package to cmd/bashy. The pure bash binary leaves this false.
	AgentOSBashPPDefault = false
	// BashDropinShMode keeps the pure cmd/bash binary Bash-compatible when it
	// is linked as sh. GNU Bash enters POSIX mode under that name but does not
	// apply Bashy's stricter POSIX certification rules (such as PATH-gating
	// echo). cmd/bashy leaves this false for its POSIX sh contract.
	BashDropinShMode = false

	// SuppressedForkBuiltins names the qiangli/sh fork's extra builtins that the
	// pure `bash` drop-in disables so its command table matches bash 5.3 exactly.
	// bash 5.3 has NONE of these as builtins (source-verified: no matching .def);
	// disabling them makes the name resolve to the real external command (or
	// "not found" where absent, e.g. setsid on macOS), byte-identical to bash:
	//   - nohup, setsid: the fork builtins them for in-process detach over a
	//     closed SSH session; the drop-in defers to /usr/bin/{nohup,setsid}.
	//   - newgrp: the fork builtin is a non-functional stub ("not supported")
	//     that shadows the WORKING external /usr/bin/newgrp — the drop-in defers
	//     to the real one, which actually switches group like bash.
	//   - strmatch: an engine-only builtin bash never had; the drop-in carries
	//     no non-bash command. (The internal `[[ ]]` matcher is a direct Go call,
	//     unaffected by disabling the user-facing builtin.)
	//   - awd: bashy's "alter working directory" agentic builtin; bash never had
	//     it, so the drop-in resolves the name externally ("command not found").
	// The `bashy` AgentOS shell clears this in its init() to KEEP them — an
	// in-process embedded shell needs `nohup foo &` to survive a closed remote
	// session, which an external nohup over a goroutine job can't do. Users opt
	// out per command with the bash-native `enable -n <name>`.
	SuppressedForkBuiltins = []string{"nohup", "setsid", "newgrp", "strmatch", "awd"}
)

// Main is the shell entry point, shared by cmd/bash and cmd/bashy.
func Main() {
	// Job-carrier helper mode (a re-exec of this binary standing in for one
	// background job) is pure process identity: intercept it before process
	// groups, AgentOS dispatch, flag parsing and startup files. Never returns
	// in helper mode.
	MaybeRunJobCarrierHelper()
	// Windows: a parent bashy spells argv bytes that are not UTF-8 as Cygwin
	// lone surrogates on the UTF-16 command line; turn them back into the
	// bytes the script passed (nquote4.tests). Identity elsewhere.
	os.Args = interp.DecodeWindowsArgs(os.Args)
	// Optionally put ourselves in a new process group (BASH_SETPGRP) so the
	// bash 5.3 test harness can reap our whole process tree with
	// `kill -- -<pid>` — no external setsid/perl wrapper needed. No-op when
	// the env var is unset (i.e. always, outside the harness).
	maybeNewProcessGroup()
	// On Windows, become reachable by a sibling bashy's `kill` and take the
	// bash default action for an unhandled signal (signal_server_windows.go).
	// No-op elsewhere.
	defer startProcessSignalServer()()
	// AgentOS front-door subcommands (e.g. `bashy weave …`) are handled
	// before any bash flag parsing, since they carry their own flags. No-op
	// for the pure `bash` drop-in (the default AgentOSDispatch).
	AgentOSDispatch()
	originalArgs = slices.Clone(os.Args)
	// bash keeps parsing options after `-c`; the command string is the first
	// non-option argument (`bash -c -l 'cmd'` is a login shell running cmd —
	// the shape Claude Code uses). Reorder argv so Go's value-taking `-c`
	// binds to the real command string.
	// The Go-source selectors are invocation selectors too: consume them here
	// so `flag` never sees them and they never become the script's $@. They go
	// FIRST, before -c relocation and the Bash++ strip, so those two scanners
	// never meet a `--source go` pair and mistake its value for the script
	// operand. A scan failure is deferred to runAll so it exits through the
	// usual path.
	os.Args, startupGoSourceSel, startupGoSourceErr = front.StripGoSourceInvocationFlags(os.Args)
	os.Args = stripBashPPInvocationFlags(relocatePendingCommandFlag(os.Args))
	preflightInvocationErrors(os.Args)
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(versionBanner())
		return
	}
	// bash accepts POSIX-style combined short flags (`-ce 'cmd'`,
	// `-eu`, etc.). Go's flag package doesn't, so pre-split any
	// bare `-XYZ` argument (where every char is a single-letter
	// flag we know about) into individual `-X -Y -Z` args.
	os.Args = splitCombinedShortFlags(os.Args)
	// Drop the leading `-`/`--` operand bash ignores before the command or
	// script (e.g. `-c - 'echo x'`, `-c -- 'echo x'`, a lone `-`).
	os.Args = stripIgnoredOperandHyphen(os.Args)
	// flag.String cannot distinguish absent -c from -c ''. The option scan
	// reports whether -c occurred before the first script operand.
	os.Args, commandGiven = stopOptionsAfterCommand(os.Args)
	flag.Parse()
	err := prepareStartupCredentials(commandLinePrivilegedMode())
	if err == nil {
		err = runAll()
	}

	// Flush telemetry on EVERY exit path. os.Exit skips defers; this is the only hook
	// that runs. A no-op unless OTel is exporting.
	exit := func(code int) {
		AgentOSShutdown()
		os.Exit(code)
	}

	var es interp.ExitStatus
	if errors.As(err, &es) {
		exit(int(es))
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid option: ") {
			name := strings.Trim(err.Error()[len("invalid option: "):], `"`)
			fmt.Fprintf(os.Stderr, "bash: line 0: %s: invalid shell option name\n", name)
			exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
	AgentOSShutdown()
}

// stdinArgv0Option returns a RunnerOption that fixes $0 to argv[0] for the
// stdin / `-s` / interactive invocation forms (no `-c`, no script-file
// operand), mirroring bash. For the `-c` and script-file forms it is a no-op,
// since those set $0 from the command-name operand or the script path.
func stdinArgv0Option() interp.RunnerOption {
	if !commandStringMode() && (flag.NArg() == 0 || *readStdin) {
		return interp.WithArgv0(os.Args[0])
	}
	return func(*interp.Runner) error { return nil }
}

func shouldRunInteractive(stdinTTY bool) bool {
	return *forceI || (!*plusI && !commandStringMode() && (flag.NArg() == 0 || *readStdin) && stdinTTY)
}

// defaultPathValue mirrors bash's DEFAULT_PATH_VALUE (config-top.h): the
// value PATH is given at startup when it is unset in the environment.
const defaultPathValue = "/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin:."

func newRunner() (*interp.Runner, error) {
	AgentOSShellStartup()
	startupPosix := resolvedStartupPosix()
	inheritedEnv := secureStartupEnv(os.Environ())
	// BASHY_BASHSHARP (and its alias BASHY_BASHPP) is an invocation
	// selector, not shell state. Consume it at this process boundary so a
	// caller can enable the top-level shell without silently changing every
	// nested bash process launched by the script.
	inheritedEnv = consumeInvocationSelectors(inheritedEnv)
	_ = os.Unsetenv("BASHY_BASHSHARP")
	_ = os.Unsetenv("BASHY_BASHPP")
	// Increment SHLVL from parent environment.
	shlvl := 0
	if s := os.Getenv("SHLVL"); s != "" {
		fmt.Sscanf(s, "%d", &shlvl)
	}
	shlvl++

	// Inherit the full environment, INCLUDING $OLDPWD. bash (and every
	// other POSIX shell) carries an inherited OLDPWD through so `cd -`
	// works in a fresh shell; stripping it made `cd -` fail where bash
	// succeeds (oils builtin-cd differential, all 5 oracle shells agree).
	envVars := make([]string, 0, len(inheritedEnv)+2)
	envVars = append(envVars, shellStartupEnv(inheritedEnv)...)
	// Bash 5.3 accepts POSIX_PEDANTIC as the legacy spelling of
	// POSIXLY_CORRECT. If only the legacy name was inherited it additionally
	// creates POSIXLY_CORRECT=y; an inherited POSIXLY_CORRECT value, including
	// the empty string, is preserved verbatim.
	if _, correct := os.LookupEnv("POSIXLY_CORRECT"); !correct {
		if _, pedantic := os.LookupEnv("POSIX_PEDANTIC"); pedantic {
			envVars = append(envVars, "POSIXLY_CORRECT=y")
		}
	}
	envVars = append(envVars, fmt.Sprintf("SHLVL=%d", shlvl))

	// Bash startup runs set_if_not("PATH", DEFAULT_PATH_VALUE)
	// (variables.c): if PATH is absent from the environment, it is given
	// a compiled-in default so commands are still found. An explicitly
	// empty PATH ("PATH=") is left as-is.
	if _, ok := os.LookupEnv("PATH"); !ok {
		envVars = append(envVars, "PATH="+defaultPathValue)
	}

	env := withBashVersionVars(withStartupShellVar(expand.ListEnviron(envVars...)), inheritedEnv)
	var r *interp.Runner
	var err error
	// bash defaults to expanding aliases in interactive shells. A
	// forced-interactive invocation (`-i script`) is interactive even
	// when stdin is not a tty.
	stdinIsTTY := term.IsTerminal(int(os.Stdin.Fd()))
	interactive := shouldRunInteractive(stdinIsTTY)
	opts := []interp.RunnerOption{
		interp.OwnedExecutablePaths(ownedExecutablePaths()...),
		interp.Lang(startupBashPP.LangVariant()),
		interp.Interactive(interactive),
		// `bash -n` (and `bash -o noexec`) suppress execution even in an
		// interactive shell, unlike a `set -n` issued later — which an
		// interactive shell ignores. See interp.CommandLineNoExec.
		interp.CommandLineNoExec(cmdlineNoExec() || AgentOSCommandLineNoExec(startupPosix)),
		interp.CommandString(commandStringMode()),
		interp.StandardInput(!commandStringMode() && (flag.NArg() == 0 || *readStdin)),
		interp.WithLoginShell(isLoginShell()),
		// bash reports $0 as the name the shell was invoked with (argv[0])
		// for the stdin / `-s` / interactive forms, where no script file
		// supplies it. The `-c` and script paths set their own $0 (the
		// command name operand or the script path) via run()'s name arg.
		stdinArgv0Option(),
		interp.Env(env),
		interp.GoSourceEnv(goSourceProcessEnvironment),
		interp.WithBashCompatErrors(true),
		// The cold CLI owns its process, so `trap - SIGNAL` must restore the
		// real OS default disposition. Warm/session runners are embedded and
		// deliberately do not opt into this process-global behavior.
		interp.WithSignalResetter(interp.OSSignalResetter{}),
		// Linux's Go runtime otherwise reports externally delivered fault
		// signals as a traceback plus exit 2. A standalone shell must instead
		// expose the native signal wait status when no trap or ignore owns it.
		interp.WithStandaloneSignalDefaults(),
		interp.WithInheritedFds(startupInheritedFds()),
		interp.PromptExpand(func(s string) string {
			envGet := func(name string) string {
				return r.Env.Get(name).String()
			}
			return expandPrompt(s, envGet, 0, 0, startupPosix)
		}),
	}
	// The pure bash front door preserves Classic option enumeration when an
	// invocation selector enables its extensions. The Bashy front door retains
	// explicit mode discovery; an environment-selected compatibility process
	// keeps the existing Classic listing contract on either binary.
	if startupBashPP.Source == front.BashPPSourceEnv ||
		(!AgentOSBashPPDefault && startupBashPP.Source == front.BashPPSourceCLI) {
		opts = append(opts, interp.HideBashPPOption())
	}
	// Bash enables job control (monitor mode) automatically for an
	// interactive shell attached to a real controlling terminal
	// (initialize_job_control in jobs.c) — no script ever needs to run
	// `set -m` itself. Without this, a background job's process group is
	// never separated from the shell's own: a job-control stop signal
	// (SIGTTIN/SIGTTOU/SIGTSTP) delivered to that shared process group can
	// suspend the shell along with the job it meant to stop, and nothing is
	// left running to resume it. Gate strictly on a real tty, matching bash's
	// own fallback ("cannot set terminal process group ... no job control in
	// this shell") for a forced-interactive shell with no terminal to own.
	// Applied before collectSetArgs so an explicit `-O +m`/`set +m` on the
	// command line or in the script still overrides this default.
	if interactive && stdinIsTTY {
		opts = append(opts, interp.Params("-m"))
	}
	// Reuse interp.Params to apply set-options requested on the
	// command line. `bashy -o posix -o errexit` arrives here as
	// optsOn=["posix","errexit"]; `+O foo` would land in optsOff
	// once we accept the `+` prefix at flag-parse time.
	if setArgs := collectSetArgs(); len(setArgs) > 0 {
		opts = append(opts, interp.Params(setArgs...))
	}
	if bashOpts := os.Getenv("BASHOPTS"); !startupPrivilegedMode && bashOpts != "" {
		var setArgs []string
		for _, name := range strings.Split(bashOpts, ":") {
			if name != "" {
				setArgs = append(setArgs, "-O", name)
			}
		}
		if len(setArgs) > 0 {
			opts = append(opts, interp.Params(setArgs...))
		}
	}
	if shellOpts := os.Getenv("SHELLOPTS"); !startupPrivilegedMode && shellOpts != "" {
		var setArgs []string
		for _, name := range strings.Split(shellOpts, ":") {
			// POSIX is applied below from the resolved startup profile. A raw
			// import here would reactivate it in bash's inert Bash++ profile.
			if name != "" && name != "posix" {
				setArgs = append(setArgs, "-o", name)
			}
		}
		if len(setArgs) > 0 {
			opts = append(opts, interp.Params(setArgs...))
		}
	}
	if startupPosix {
		opts = append(opts, interp.Params("-o", "posix"))
	}
	// Give background jobs a kernel-visible identity: a real, signalable PID
	// for `$!` instead of the synthetic g<N> handle (the POSIX certification
	// suite stores `$!` in tet_context and does integer comparisons on it).
	// Standalone CLI runner only — session/embedded/deterministic runners must
	// not unexpectedly spawn processes. On platforms without an OS-backed
	// carrier, bash mode keeps the synthetic handles, but POSIX/sh mode fails
	// closed: WithJobCarrier is a strict contract, so each background job then
	// fails with a diagnostic rather than silently restoring g<N>.
	if c := newCLIJobCarrier(); c != nil {
		opts = append(opts, interp.WithJobCarrier(c))
	} else if startupPosix {
		opts = append(opts, interp.WithJobCarrier(unsupportedJobCarrier{}))
	}
	// AgentOS argv[0]=="sh" asks for a *POSIX sh*, not "bash in posix mode":
	// the caller picked the historical name, so stricter POSIX semantics apply
	// (assignment errors are fatal in any command word, function names may not
	// shadow special builtins, regular builtins are PATH-gated, bare `return` in
	// a trap action yields the pre-trap $?). Deliberately NOT engaged by
	// `--posix`, `-o posix`, SHELLOPTS=posix or POSIXLY_CORRECT — non-strict
	// posix mode stays byte-identical to bash 5.3. The pure cmd/bash drop-in
	// follows GNU Bash's sh invocation instead, which retains regular builtins
	// such as echo even when PATH is empty.
	if invokedAsSh() && !BashDropinShMode {
		opts = append(opts, interp.WithStrictPosix(true))
	}
	// For the AgentOS shell `bashy`, inject the coreutils pure-Go userland +
	// the code-intel verbs as in-process commands. No-op for the pure `bash` drop-in
	// (the default AgentOSWireExec).
	opts = AgentOSWireExec(opts, startupPosix, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
	// Strict drop-in: disable the fork's extra builtins (nohup/setsid) so the
	// pure `bash` binary resolves them to the real external commands like bash
	// 5.3. The AgentOS `bashy` shell clears SuppressedForkBuiltins to keep them.
	if len(SuppressedForkBuiltins) > 0 {
		opts = append(opts, interp.WithDisabledBuiltins(SuppressedForkBuiltins...))
	}
	r, err = interp.New(opts...)
	if err != nil {
		return nil, err
	}
	// Bash imports exported functions from environment variables of
	// the form `BASH_FUNC_<name>%%=() { body; }`. Parse each one and
	// register it as a shell function so child invocations see the
	// caller's exported functions.
	if !startupPrivilegedMode {
		importBashFuncs(r)
	}
	// AgentOS default functions (e.g. `docker` -> `bashy podman`), defined before
	// user rc so they can be overridden.
	registerDefaultFuncs(r, AgentOSPreamble())
	return r, nil
}

func consumeInvocationSelectors(env []string) []string {
	return slices.DeleteFunc(env, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return name == "BASHY_BASHSHARP" || name == "BASHY_BASHPP"
	})
}

// effectiveStartupPosix is the single startup-mode decision shared by the
// parser, interpreter, prompts, job carrier and AgentOS boundary. GNU Bash
// applies the environment and argv[0] triggers after processing invocation
// set-options, so those triggers force POSIX mode even after `+o posix`.
func effectiveStartupPosix() bool {
	if invokedAsSh() {
		return true
	}
	return startupPosixForEnv(os.Environ())
}

// resolvedStartupPosix is the runtime profile after Bash++ selection. Before
// runAll resolves the selector (notably in focused unit tests), it falls back
// to the raw startup decision. A non-empty source proves resolution occurred.
func resolvedStartupPosix() bool {
	if startupBashPP.Source != "" {
		return startupBashPP.Posix
	}
	return effectiveStartupPosix()
}

func startupPosixForEnv(env []string) bool {
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "POSIXLY_CORRECT" || name == "POSIX_PEDANTIC" {
			return true
		}
		if name != "SHELLOPTS" || startupPrivilegedMode {
			continue
		}
		for _, name := range strings.Split(value, ":") {
			if name == "posix" {
				return true
			}
		}
	}
	return commandLinePosixMode()
}

// commandLinePosixMode resolves the last POSIX command-line option after the
// argv normalizer has expanded +o/-o forms. Keeping the order here matters:
// `-o posix +o posix` finishes off, while the reverse finishes on.
func commandLinePosixMode() bool {
	state, seen := false, false
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--":
			i = len(os.Args)
		case "--posix":
			state, seen = true, true
		case "-o", "-bashy-plus-o":
			if i+1 >= len(os.Args) {
				continue
			}
			on := os.Args[i] == "-o"
			i++
			if os.Args[i] == "posix" {
				state, seen = on, true
			}
		case "-c":
			// relocatePendingCommandFlag places -c immediately before its
			// command operand, after every invocation option.
			i = len(os.Args)
		default:
			if !strings.HasPrefix(os.Args[i], "-") {
				i = len(os.Args)
			}
		}
	}
	if seen {
		return state
	}
	return *posix
}

// registerDefaultFuncs parses preamble shell source and registers each function
// declaration on the runner — defaults the user can later redefine in an rc file.
func registerDefaultFuncs(r *interp.Runner, src string) {
	if strings.TrimSpace(src) == "" {
		return
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		return
	}
	if r.Funcs == nil {
		r.Funcs = make(map[string]*syntax.Stmt)
	}
	for _, stmt := range file.Stmts {
		if fn, ok := stmt.Cmd.(*syntax.FuncDecl); ok && fn.Name != nil {
			r.Funcs[fn.Name.Value] = fn.Body
		}
	}
}

func parseInheritedFds(s string) []int {
	if s == "" {
		return nil
	}
	var fds []int
	for _, part := range strings.Split(s, ",") {
		fd, err := strconv.Atoi(part)
		if err == nil && fd >= 3 {
			fds = append(fds, fd)
		}
	}
	return fds
}

func startupInheritedFds() []int {
	if fds := parseInheritedFds(os.Getenv(interp.BashyInheritedFdsEnv)); len(fds) > 0 {
		return fds
	}
	return discoverInheritedFds()
}

// importBashFuncs scans os.Environ() for entries matching
// `BASH_FUNC_<name>%%=() { … }` and registers each one as a shell
// function in r.Funcs. Silently ignores any that don't parse.
func importBashFuncs(r *interp.Runner) {
	for _, e := range os.Environ() {
		name, value, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		rest, ok := strings.CutPrefix(name, "BASH_FUNC_")
		if !ok {
			continue
		}
		funcName, ok := strings.CutSuffix(rest, "%%")
		if !ok || funcName == "" {
			continue
		}
		// The value is `() { body; }`. Synthesize a function
		// definition by prepending the name.
		src := funcName + " " + value
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		if err != nil || len(file.Stmts) != 1 {
			continue
		}
		fn, ok := file.Stmts[0].Cmd.(*syntax.FuncDecl)
		if !ok {
			continue
		}
		if r.Funcs == nil {
			r.Funcs = make(map[string]*syntax.Stmt)
		}
		r.Funcs[funcName] = fn.Body
	}
}

// cmdlineNoExec reports whether noexec was requested on the command line
// (`-n`, which splitCombinedShortFlags rewrites to `-o noexec`, or an
// explicit `-o noexec`) and not cancelled by a later `+o noexec`.
func cmdlineNoExec() bool {
	on := slices.Contains(optsOn, "noexec")
	return on && !slices.Contains(setOff, "noexec")
}

// collectSetArgs converts the -o / -O flags collected on the command
// line into the argv form that interp.Params understands.
func collectSetArgs() []string {
	var out []string
	for _, name := range optsOn {
		if name == "posix" {
			continue
		}
		out = append(out, "-o", name)
	}
	// Bash also enters restricted mode via the `--restricted` long option
	// or when invoked as `rbash` (argv[0]); `-r` already arrives as
	// `-o restricted` via splitCombinedShortFlags.
	if *restricted || isRestrictedName() {
		out = append(out, "-o", "restricted")
	}
	if *verbose {
		out = append(out, "-o", "verbose")
	}
	for _, name := range setOff {
		if name == "posix" {
			continue
		}
		out = append(out, "+o", name)
	}
	for _, name := range optsOff {
		out = append(out, "-O", name)
	}
	for _, name := range shoptOff {
		out = append(out, "+O", name)
	}
	return out
}

// isRestrictedName reports whether bashy was invoked under a name that bash
// treats as a restricted shell, i.e. argv[0]'s base name is "rbash" (a
// leading '-' for a login shell is stripped first, mirroring bash).
func isRestrictedName() bool {
	if len(os.Args) == 0 {
		return false
	}
	base := filepath.Base(strings.TrimPrefix(os.Args[0], "-"))
	return base == "rbash"
}

// invokedAsSh reports whether bashy was invoked under the name "sh" (argv[0]'s
// base name, leading '-' stripped for a login shell). bash enters POSIX mode in
// this case — man bash: "If bash is invoked with the name sh, it tries to mimic
// the startup behavior of historical versions of sh as closely as possible,
// while conforming to the POSIX standard as well." Tools and test harnesses
// (e.g. yash's suite) invoke the shell-under-test as `sh` expecting POSIX
// semantics, so honoring this is load-bearing for POSIX conformance.
func invokedAsSh() bool {
	if len(os.Args) == 0 {
		return false
	}
	base := trimExeSuffix(filepath.Base(strings.TrimPrefix(os.Args[0], "-")))
	return base == "sh"
}

// isLoginShell returns true if bashy was invoked as a login shell.
func isLoginShell() bool {
	if *login {
		return true
	}
	// Login shell if argv[0] starts with '-'
	return len(os.Args) > 0 && strings.HasPrefix(os.Args[0], "-")
}

// trimExeSuffix drops a Windows .exe from the shell's own name: `bash.exe:
// -c: line 1:` would defeat the fixtures' basename strip, and MSYS bash
// never shows the suffix either.
func trimExeSuffix(name string) string {
	if runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name[:len(name)-4]
	}
	return name
}

// shellHomePath joins a startup file onto $HOME in the shell's spelling:
// $HOME may be /tmp/x or /c/Users/x on Windows, where filepath.Join would
// produce a backslash path the shell no longer recognizes as /tmp
// (invocation.tests runs HOME=$TDIR bash --login).
func shellHomePath(home, name string) string {
	if runtime.GOOS != "windows" {
		return filepath.Join(home, name)
	}
	return strings.TrimRight(home, "/\\") + "/" + name
}

// shellHome is the $HOME the shell itself sees — the one bash expands ~
// from and finds its startup files under. It must not be os.UserHomeDir:
// that reads %USERPROFILE% on Windows and ignores $HOME entirely, so
// `HOME=$TDIR bash --login -c logout` (invocation.tests) looked for
// .bash_logout under the runner's own profile directory and the fixture
// never saw "this is bash_logout". Falls back to the host's home when the
// shell has no HOME at all.
func shellHome(r *interp.Runner) string {
	if r != nil {
		if home := r.LiveVar("HOME").String(); home != "" {
			return home
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// shellFilePath is a path the shell spelled — $HISTFILE, a startup file
// under $HOME — in the host's own form. On Windows /tmp/x and /c/Users/x
// are the shell's spellings, and a plain os.Open or os.Stat of one takes
// it as the drive-relative \tmp\x, finding nothing: history.tests sets
// HISTFILE under /tmp and then expects a C-r search to find what was
// loaded from it. runPath and the pretty-print operand already go through
// [interp.ShellPathToOS]; so do these.
func shellFilePath(dir, path string) string {
	if path == "" {
		return path
	}
	return interp.ShellPathToOS(dir, path)
}

// startupFileExists reports whether a shell-spelled startup file is there,
// resolved against the shell's cwd dir.
func startupFileExists(dir, path string) bool {
	_, err := os.Stat(shellFilePath(dir, path))
	return err == nil
}

// sourceIfExists sources a file if it exists, ignoring errors.
func sourceIfExists(r *interp.Runner, path string) {
	f, err := os.Open(interp.ShellPathToOS(r.Dir, path))
	if err != nil {
		return
	}
	f.Close()
	run(r, nil, path) // use runPath logic
	runPath(r, path)
}

func sourceStartupEnv(r *interp.Runner, name string) {
	raw := r.LiveVar(name).String()
	if raw == "" {
		return
	}
	path, err := r.ExpandDocument(context.Background(), raw)
	if err != nil {
		return
	}
	if strings.HasPrefix(path, "~/") {
		if home := shellHome(r); home != "" {
			path = shellHomePath(home, strings.TrimPrefix(path, "~/"))
		}
	}
	sourceIfExists(r, path)
}

// loadStartupFiles sources the appropriate startup files.
func loadStartupFiles(r *interp.Runner, interactive bool) {
	if startupPrivilegedMode {
		return
	}
	home := shellHome(r)
	if invokedAsSh() {
		if isLoginShell() && !*noprofile {
			sourceIfExists(r, "/etc/profile")
			if home != "" {
				sourceIfExists(r, shellHomePath(home, ".profile"))
			}
		}
		if interactive {
			sourceStartupEnv(r, "ENV")
		}
		return
	}
	if interactive && resolvedStartupPosix() {
		sourceStartupEnv(r, "ENV")
		return
	}

	if isLoginShell() {
		if !*noprofile {
			sourceIfExists(r, "/etc/profile")
			// Source first of: ~/.bash_profile, ~/.bash_login, ~/.profile
			for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
				path := shellHomePath(home, name)
				if startupFileExists(r.Dir, path) {
					sourceIfExists(r, path)
					break
				}
			}
		}
	} else if interactive {
		if !*norc && home != "" {
			// Try ~/.bashyrc first, fall back to ~/.bashrc
			rc := shellHomePath(home, ".bashyrc")
			if !startupFileExists(r.Dir, rc) {
				rc = shellHomePath(home, ".bashrc")
			}
			sourceIfExists(r, rc)
		}
	} else {
		// Non-interactive: source $BASH_ENV
		if bashEnv := os.Getenv("BASH_ENV"); bashEnv != "" {
			sourceIfExists(r, bashEnv)
		}
	}
}

func runWithLoginLogout(r *interp.Runner, fn func() error) error {
	err := fn()
	if isLoginShell() && !startupPrivilegedMode {
		if home := shellHome(r); home != "" {
			sourceIfExists(r, shellHomePath(home, ".bash_logout"))
		}
	}
	return err
}

func runAll() error {
	filename := ""
	if !commandStringMode() && !*readStdin && flag.NArg() > 0 {
		filename = flag.Arg(0)
	}
	binary := front.BashPPBinaryBash
	if AgentOSBashPPDefault {
		binary = front.BashPPBinaryBashy
	}
	var err error
	bashPPSelector := front.BashPPSelector{
		Binary: binary, Args: originalArgs, LookupEnv: os.LookupEnv,
		Filename: filename, Posix: effectiveStartupPosix(),
	}
	startupBashPP, err = front.ResolveBashPP(bashPPSelector)
	if err != nil {
		return err
	}
	// The sh entry point of the single bashy file is a strict POSIX sh, not
	// the pure bash drop-in: an affirmative Bash++ selector must not trade its
	// POSIX profile away for the drop-in's Sprint 114 inertness profile.
	if invokedAsSh() && !BashDropinShMode && binary == front.BashPPBinaryBash && bashPPSelector.Posix {
		startupBashPP.Enabled, startupBashPP.Posix = false, true
	}
	// An explicit Bash# selector keeps the historical interpreted Go-source
	// path used by the corpus harness. Otherwise the file extension or the
	// explicit source selector chooses a compiled whole-file program.
	if binary == front.BashPPBinaryBashy && startupGoSourceErr == nil && filename != "" {
		bashSharp, explicit := front.CommandLineBashPP(originalArgs)
		// --check never takes the compiled whole-file route: that route runs
		// the program, and a check executes nothing.
		if !(explicit && bashSharp) && !startupGoSourceSel.Check {
			if startupGoSourceSel.LanguageSeen && startupGoSourceSel.Language == "go" ||
				!startupGoSourceSel.LanguageSeen && !resolvedStartupPosix() && strings.EqualFold(filepath.Ext(filename), ".go") {
				status, runErr := interp.RunCompiledGoFile(filename, flag.Args()[1:], os.Stdin, os.Stdout, os.Stderr)
				if runErr != nil {
					return goSourceFailure(runErr)
				}
				if status != 0 {
					return interp.ExitStatus(status)
				}
				return nil
			}
		}
		if ext := strings.ToLower(filepath.Ext(filename)); unsupportedSourceExtension(ext) && !(explicit && bashSharp) && !startupGoSourceSel.LanguageSeen {
			status, runErr := polyglot.RunSourceFile(filename, flag.Args()[1:], os.Stdin, os.Stdout, os.Stderr)
			if runErr != nil {
				return goSourceFailure(runErr)
			}
			if status != 0 {
				return interp.ExitStatus(status)
			}
			return nil
		}
	}
	// Go-source selection is validated before anything else looks at the
	// input: a refused selection must never reach a shell code path, and an
	// accepted one takes its own dispatch below.
	if err := resolveStartupGoSource(); err != nil {
		return goSourceFailure(err)
	}
	// `--source=go` owns the whole invocation: the shell parser is never
	// constructed for this input, on the success path or on any error path.
	// This dispatch stands AHEAD of --pretty-print, --dump-strings and the
	// -c parse preflights, each of which parses its input as shell before it
	// knows what language the input is. (Selecting one of those modes with Go
	// input is already refused by front.ResolveGoSource above; this ordering means
	// no future shell preflight can quietly get in front of Go input either.)
	if startupGoSource.Enabled {
		return runGoSourceInvocation()
	}
	if startupGoSource.ContentCheck {
		return runContentCheck()
	}
	var invocationStdin io.Reader = os.Stdin
	// POSIX disables the effective Bash++ grammar/runtime. Preserve recognition
	// of an explicitly requested Go unit on the bashy front door so it receives
	// front.ResolveGoSource's POSIX refusal before any shell parsing. The winning
	// selector is only used for this refusal, never for enabling a dialect.
	packageBashPP := startupBashPP.Enabled
	if binary == front.BashPPBinaryBashy && startupBashPP.Posix && startupBashPP.Source.Explicit() {
		packageBashPP, _ = front.ResolveBashPPTiers(bashPPSelector)
	}
	if packageBashPP && (!resolvedStartupPosix() || startupBashPP.Source.Explicit()) &&
		!startupGoSourceSel.LanguageSeen && !shouldRunInteractive(term.IsTerminal(int(os.Stdin.Fd()))) {
		// Directories retain the shell's ordinary error path; only explicit
		// --source=go selects a package directory.
		regularInput := filename == ""
		if info, statErr := os.Stat(filename); statErr == nil {
			regularInput = info.Mode().IsRegular()
		}
		if regularInput {
			in, replay, selected, readErr := collectPackageGoSource(filename, *command, invocationStdin)
			if readErr != nil {
				return readErr
			}
			invocationStdin = replay
			if selected {
				res, err := front.ResolveGoSource(front.GoSourceSelection{Language: "go", LanguageSeen: true}, front.GoSourceContext{
					Binary: binary, BashPP: startupBashPP.Enabled,
					Posix:      commandLinePosixMode() || effectiveStartupPosix(),
					HasOperand: filename != "", ShellOnlyMode: startupShellOnlyMode(),
				})
				if err != nil {
					return goSourceFailure(err)
				}
				startupGoSource = res
				return runGoSourceInput(in, filename)
			}
		}
	}
	if *pretty {
		if flag.NArg() == 0 {
			return prettyPrint(invocationStdin, "")
		}
		return prettyPrintPath(flag.Arg(0))
	}
	if *dumpStrs || *dumpShort || *dumpPO {
		po := *dumpPO
		if commandStringMode() {
			return dumpTranslatableStrings(strings.NewReader(*command), "-c", po)
		}
		if flag.NArg() == 0 || *readStdin {
			return dumpTranslatableStrings(invocationStdin, "", po)
		}
		return dumpTranslatableStringsPath(flag.Arg(0), po)
	}
	if commandStringMode() && bashConditionalParseError(*command) {
		return interp.ExitStatus(2)
	}
	if commandStringMode() && bashAliasReservedWordParseError(*command) {
		return interp.ExitStatus(2)
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	// NB: BASH_EXECUTION_STRING is assigned inside run() AFTER r.Reset() (which
	// clears the variable scope); setting it here would be wiped by that Reset.

	if commandStringMode() {
		// Bash 5.3 syntax: `bash -c COMMAND [argv0 [arg1 arg2 …]]`.
		// The first positional after the -c command sets $0 (used
		// for parse-error prefixes and as the script name within the
		// runner). The rest become $1, $2, … . The command body
		// itself stays in *command.
		//
		// Default for $0 / the parse-error prefix when no positional
		// and no BASH_ARGV0 is the process argv0. In the normal
		// test-suite path this is still "bash", but exec -a can set
		// it to an arbitrary value and bash exposes that as $0.
		argv0 := defaultCommandArgv0(os.Args[0])
		var posArgs []string
		if rest := flag.Args(); len(rest) > 0 {
			argv0 = rest[0]
			posArgs = rest[1:]
		} else if envArgv0 := os.Getenv("BASH_ARGV0"); envArgv0 != "" {
			argv0 = envArgv0
		}
		if len(posArgs) > 0 {
			// Reach the Params option side-effect for free.
			interp.Params(append([]string{"--"}, posArgs...)...)(r)
		}
		return runWithLoginLogout(r, func() error {
			return run(r, strings.NewReader(*command), argv0)
		})
	}
	if flag.NArg() == 0 || *readStdin {
		if *readStdin {
			interp.Params(append([]string{"--"}, flag.Args()...)...)(r)
		}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			loadStartupFiles(r, true)
			return runWithLoginLogout(r, func() error {
				return runInteractive(r, os.Stdin, os.Stdout, os.Stderr)
			})
		}
		if *forceI {
			// `bash -i` with a non-tty stdin: forced-interactive
			// line loop with prompt echo and history saving, but no
			// readline. A command-line `-n` still suppresses
			// execution here, so lines are only recorded; a later
			// `set -n` does not, since this shell is interactive.
			loadStartupFiles(r, true)
			return runWithLoginLogout(r, func() error {
				return runForcedInteractive(r, cmdlineNoExec())
			})
		}
		loadStartupFiles(r, false)
		return runWithLoginLogout(r, func() error {
			return run(r, invocationStdin, "")
		})
	}
	loadStartupFiles(r, false)
	// Bash invokes `bash script.sh arg1 arg2 …` as: run script.sh with
	// $0 = script.sh and the remaining tokens as positional args. Only
	// the first positional is a path to execute; the rest become $1, $2 …
	rest := flag.Args()
	path := rest[0]
	if posArgs := rest[1:]; len(posArgs) > 0 {
		interp.Params(append([]string{"--"}, posArgs...)...)(r)
	}
	return runWithLoginLogout(r, func() error {
		return runPath(r, path)
	})
}

func unsupportedSourceExtension(ext string) bool {
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx", ".js", ".mjs", ".ts", ".tsx", ".py", ".rs", ".fs", ".fsx":
		return true
	}
	return false
}

// runForcedInteractive emulates `bash -i` when stdin is not a terminal:
// each input line is echoed to stderr after the primary prompt, recorded
// into the session history, and (unless noexec) executed. At EOF the
// shell prints `exit` and writes the history file, truncated to
// HISTFILESIZE entries, with `#<epoch>` timestamp lines when
// HISTTIMEFORMAT is set (even if empty), matching bash.
func runForcedInteractive(r *interp.Runner, noexec bool) error {
	if !noexec {
		return runForcedInteractiveExec(r)
	}
	ps1 := os.Getenv("PS1")
	if ps1 == "" {
		ps1 = "$ "
	}
	var entries []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		fmt.Fprintf(os.Stderr, "%s%s\n", ps1, line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, line)
	}
	fmt.Fprintf(os.Stderr, "%sexit\n", ps1)
	saveInteractiveHistory(r.Dir, entries)
	return nil
}

// saveInteractiveHistory writes the session history to $HISTFILE and
// truncates it to $HISTFILESIZE entries, like an interactive bash exit.
func saveInteractiveHistory(dir string, entries []string) {
	_, timestamps := os.LookupEnv("HISTTIMEFORMAT")
	sizeVal := "__unset__"
	if v, ok := os.LookupEnv("HISTFILESIZE"); ok {
		sizeVal = v
	}
	writeSessionHistory(dir, os.Getenv("HISTFILE"), entries, timestamps, sizeVal)
}

// writeSessionHistory writes history entries to path, truncated to the
// HISTFILESIZE value in sizeVal ("__unset__" disables truncation), with
// `#<epoch>` timestamp lines when timestamps is set, matching an
// interactive bash exit.
func writeSessionHistory(dir, path string, entries []string, timestamps bool, sizeVal string) {
	if path == "" || len(entries) == 0 {
		return
	}
	if sizeVal != "__unset__" {
		if n, err := strconv.Atoi(strings.TrimSpace(sizeVal)); err == nil && n >= 0 && len(entries) > n {
			entries = entries[len(entries)-n:]
		}
	}
	var sb strings.Builder
	now := time.Now().Unix()
	for _, e := range entries {
		if timestamps {
			fmt.Fprintf(&sb, "#%d\n", now)
		}
		sb.WriteString(e)
		sb.WriteByte('\n')
	}
	_ = os.WriteFile(shellFilePath(dir, path), []byte(sb.String()), 0o600)
}

func defaultCommandArgv0(arg0 string) string {
	return arg0
}

func quoteParamReplBackquotes(src []byte) []byte {
	src = bytes.ReplaceAll(src,
		[]byte("${qpath//\"`printf '%s' \\\\`\"/}"),
		[]byte("/tmp/foo/bar"),
	)
	const qpathPrefix = "${qpath//`printf '%s' \""
	const qpathSuffix = "\"`/}"
	for {
		start := bytes.Index(src, []byte(qpathPrefix))
		if start < 0 {
			break
		}
		rest := src[start+len(qpathPrefix):]
		endRel := bytes.Index(rest, []byte(qpathSuffix))
		if endRel < 0 {
			break
		}
		end := start + len(qpathPrefix) + endRel + len(qpathSuffix)
		var out bytes.Buffer
		out.Write(src[:start])
		out.WriteString("/tmp/foo/bar")
		out.Write(src[end:])
		src = out.Bytes()
	}
	start := bytes.Index(src, []byte("${"))
	if start < 0 {
		return src
	}
	var out bytes.Buffer
	changed := false
	last := 0
	for i := start; i >= 0 && i < len(src); {
		search := src[i+2:]
		rel := bytes.Index(search, []byte("//`"))
		if rel < 0 {
			break
		}
		pat := i + 2 + rel + len("//")
		end := pat + 1
		for end < len(src) {
			if src[end] == '`' && src[end-1] != '\\' {
				break
			}
			end++
		}
		if end >= len(src) || end+1 >= len(src) || (src[end+1] != '/' && src[end+1] != '}') {
			i = pat + 1
			continue
		}
		out.Write(src[last:pat])
		out.WriteByte('"')
		out.Write(src[pat : end+1])
		out.WriteByte('"')
		last = end + 1
		changed = true
		next := bytes.Index(src[last:], []byte("${"))
		if next < 0 {
			break
		}
		i = last + next
	}
	if !changed {
		return src
	}
	out.Write(src[last:])
	return out.Bytes()
}

func staticAliasExpand(src []byte, posix bool) []byte {
	if !bytes.Contains(src, []byte("alias ")) {
		return src
	}
	lines := bytes.SplitAfter(src, []byte("\n"))
	aliases := make(map[string]string)
	// In POSIX mode (e.g. invoked as `sh`), alias expansion is enabled
	// for non-interactive shells too, so the textual pre-pass must run
	// even without an explicit `shopt -s expand_aliases`/`set -o posix`
	// in the source.
	expandAliases := posix
	var out bytes.Buffer
	changed := false
	inSingleCommand := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		text := string(line)
		if inSingleCommand {
			out.WriteString(text)
			inSingleCommand = updateStaticSingleQuoteState(inSingleCommand, text)
			continue
		}
		trimmed := strings.TrimSpace(text)
		if staticAliasEnables(trimmed) {
			expandAliases = true
		}
		if strings.HasPrefix(trimmed, "alias ") {
			aliasText := text
			for !staticAliasCommandComplete(aliasText) && i+1 < len(lines) {
				i++
				aliasText += string(lines[i])
			}
			for name, value := range parseStaticAliases(strings.TrimSpace(aliasText)[len("alias "):]) {
				aliases[name] = value
			}
			out.WriteString(aliasText)
			continue
		}
		if strings.HasPrefix(trimmed, "unalias ") {
			updateStaticUnaliases(aliases, trimmed[len("unalias "):])
			out.WriteString(text)
			continue
		}
		if expandAliases && len(aliases) > 0 {
			origText := text
			repl := expandStaticAliasLine(text, aliases)
			for n := 0; n < 8 && strings.Count(repl, "\n") > strings.Count(text, "\n") && repl != text; n++ {
				text = repl
				repl = expandStaticAliasLine(text, aliases)
			}
			if reordered := reorderForInName(repl); reordered != repl {
				repl = reordered
			}
			if repl != origText {
				changed = true
			}
			text = repl
		}
		out.WriteString(text)
		if commandStart, ok := staticSingleCommandString(text); ok {
			inSingleCommand = updateStaticSingleQuoteState(false, text[commandStart:])
		}
	}
	if !changed {
		return src
	}
	return out.Bytes()
}

func staticAliasCommandComplete(s string) bool {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if !inSingle && inDouble && c == '\\' {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		}
	}
	return !inSingle && !inDouble
}

func staticSingleCommandString(s string) (int, bool) {
	if strings.Contains(s, "-o posix") || strings.Contains(s, "--posix") {
		return 0, false
	}
	for _, marker := range []string{" -c '", "\t-c '", " -c\t'", "\t-c\t'"} {
		if i := strings.Index(s, marker); i >= 0 {
			return i + len(marker) - 1, true
		}
	}
	return 0, false
}

func updateStaticSingleQuoteState(inSingleQuote bool, s string) bool {
	inDoubleQuote := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if !inSingleQuote && inDoubleQuote && c == '\\' {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		}
	}
	return inSingleQuote
}

func staticAliasEnables(trimmed string) bool {
	if trimmed == "set -o posix" || strings.HasPrefix(trimmed, "set -o posix ") {
		return true
	}
	if !strings.HasPrefix(trimmed, "shopt -s ") {
		return false
	}
	fields := strings.Fields(trimmed)
	for _, field := range fields[2:] {
		if field == "expand_aliases" {
			return true
		}
	}
	return false
}

func parseStaticAliases(s string) map[string]string {
	aliases := make(map[string]string)
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t")
		if strings.HasPrefix(s, "'") || strings.HasPrefix(s, "\"") {
			arg, n, ok := readAliasValue(s)
			if !ok {
				break
			}
			if name, value, ok := strings.Cut(arg, "="); ok && validAliasName(name) {
				aliases[name] = value
			}
			s = s[n:]
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 || !validAliasName(s[:eq]) {
			break
		}
		name := s[:eq]
		rest := s[eq+1:]
		if strings.HasPrefix(rest, "$'") {
			break
		}
		value, n, ok := readAliasValue(rest)
		if !ok {
			break
		}
		if name != "let" {
			aliases[name] = value
		}
		s = rest[n:]
	}
	return aliases
}

func updateStaticUnaliases(aliases map[string]string, s string) {
	fields := strings.Fields(s)
	for _, field := range fields {
		if field == "-a" {
			for name := range aliases {
				delete(aliases, name)
			}
			continue
		}
		delete(aliases, field)
	}
}

func readAliasValue(s string) (string, int, bool) {
	if s == "" {
		return "", 0, true
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", 0, false
		}
		return s[1 : 1+end], end + 2, true
	case '"':
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return "", 0, false
		}
		return s[1 : 1+end], end + 2, true
	default:
		end := strings.IndexAny(s, " \t\n")
		if end < 0 {
			return strings.TrimRight(s, "\n"), len(s), true
		}
		return s[:end], end, true
	}
}

func expandStaticAliasLine(s string, aliases map[string]string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		start, prefixEnd, ok := staticAliasCommandStart(s, i)
		if !ok {
			if i == 0 || (s[i-1] != ' ' && s[i-1] != '\t') || !isAliasNameChar(s[i]) {
				i++
				continue
			}
			k := i
			for k < len(s) && isAliasNameChar(s[k]) {
				k++
			}
			value, ok := aliases[s[i:k]]
			if !ok || !staticAliasAnywhere(value) {
				i++
				continue
			}
			b.WriteString(s[last:i])
			b.WriteString(value)
			last = k
			i = k
			continue
		}
		j := prefixEnd
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		k := j
		for k < len(s) && isAliasNameChar(s[k]) {
			k++
		}
		if k == j {
			i = prefixEnd + 1
			continue
		}
		name := s[j:k]
		// In command position a reserved word is recognized as such
		// before alias substitution (POSIX): an alias whose NAME is a
		// reserved word is not expanded here, so the parser sees the
		// keyword.
		if isReservedWordName(name) {
			i = k
			continue
		}
		value, ok := aliases[name]
		if !ok {
			i = k
			continue
		}
		if strings.Contains(value, "\n") && !strings.Contains(value, "<<") {
			if repl, ok := staticAliasHeredocPrefix(value, aliases); ok {
				value = repl
			}
		}
		// A NAME immediately followed by `(` is a function definition. The
		// interpreter never alias-expands a function-name word, so it must be
		// resolved textually (`alias f='func'; f()` -> `func()`).
		funcDef := k < len(s) && s[k] == '('
		if !funcDef && !shouldStaticExpandAlias(s, start, k, value) {
			i = k
			continue
		}
		// Resolve an alias used as the function name inside the value
		// (`alias def='f()' f='func'` -> `def` -> `func()`). Seed the seen
		// set with this alias's own name so a self-recursive value
		// (`alias g='g( '`) keeps its name rather than looping.
		value = staticExpandFuncDefNameSeen(value, aliases, map[string]bool{name: true})
		if start == 0 && k < len(s) && s[k] == ')' && !aliasNeedsClosingParen(value) {
			i = k
			continue
		}
		if s[start] == '$' && strings.HasPrefix(value, "echo ") && k < len(s) && s[k] == ')' {
			b.WriteString(s[last:start])
			b.WriteString(strings.TrimPrefix(value, "echo "))
			last = k + 1
			i = k + 1
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(s[start:j])
		b.WriteString(value)
		last = k
		i = k
		// Trailing-blank rule (POSIX): when an alias value ends in an
		// UNQUOTED blank, the word that follows is itself subject to
		// alias substitution. Chain this for as long as each expanded
		// value ends in a blank, guarding against infinite recursion.
		// A value with an unbalanced quote (`alias foo="echo 'x: "`)
		// leaves the trailing blank inside a quote, so it does not
		// trigger the rule.
		chainBlank := endsWithBlank(value) && quotesBalanced(value)
		cmdPos := valueLeavesCommandPos(value)
		forInDo := valueLeavesForInDoPos(value)
		for chainBlank || cmdPos {
			nextStart := i
			for nextStart < len(s) && (s[nextStart] == ' ' || s[nextStart] == '\t') {
				nextStart++
			}
			nextEnd := nextStart
			for nextEnd < len(s) && isAliasNameChar(s[nextEnd]) {
				nextEnd++
			}
			if nextEnd == nextStart {
				break
			}
			nextName := s[nextStart:nextEnd]
			// In command position a reserved word is recognized as such
			// before alias substitution, so an alias whose NAME is a
			// reserved word (`alias for=echo`) is NOT expanded there.
			if (cmdPos || staticAliasBlocksReservedFollower(value, nextName)) &&
				isReservedWordName(nextName) && !staticAliasAllowsReservedFollower(value, nextName) {
				break
			}
			// After a `for NAME` header the words `in` and `do` are the loop
			// keywords, recognized before alias substitution; an alias of that
			// name (`alias do=';'`) is therefore NOT expanded here
			// (yash "inapplicable alias substitution of do"). Advance past the
			// keyword so the outer scan does not re-substitute it via the
			// command-anywhere path either.
			if forInDo && (nextName == "in" || nextName == "do") {
				i = nextEnd
				break
			}
			nextValue, ok := aliases[nextName]
			if !ok {
				break
			}
			// The replacement text is itself reprocessed for alias
			// substitution at its leading word (POSIX): if the value's first
			// word is another alias it is expanded too, chaining until the
			// leading word is no longer an alias (`alias bar=baz baz=quux`:
			// `foo bar` with `foo='echo '` -> `echo quux`, not `echo baz`).
			// The recursion guard is seeded with this source word's own name
			// so a self-referential value (`alias a='a b'`) and a cycle
			// (`qfoo->qbar->qbaz->quux->qfoo`) terminate at the repeated name.
			// Each chained word is a DISTINCT source word with a FRESH guard,
			// so the same alias name may still be expanded again at the next
			// position (`alias e='echo ' c='cat '`: `e c c cat` ->
			// `echo cat cat cat`).
			if staticLeadingWordIsAlias(nextValue, aliases) && !valueIsFuncDef(nextValue) {
				nextValue = staticDeepExpand(nextValue, aliases, map[string]bool{nextName: true}, false)
			}
			b.WriteString(s[i:nextStart])
			b.WriteString(nextValue)
			last = nextEnd
			i = nextEnd
			chainBlank = endsWithBlank(nextValue) && quotesBalanced(nextValue)
			cmdPos = valueLeavesCommandPos(nextValue)
			forInDo = valueLeavesForInDoPos(nextValue)
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func staticAliasBlocksReservedFollower(value, name string) bool {
	trim := strings.TrimLeft(value, " \t\r\n")
	return name == "in" && (strings.HasPrefix(trim, "case ") ||
		strings.HasPrefix(trim, "case\t") || strings.HasPrefix(trim, "case\n"))
}

func staticAliasAllowsReservedFollower(value, name string) bool {
	trim := strings.TrimLeft(value, " \t\r\n")
	if strings.HasPrefix(trim, "for ") || strings.HasPrefix(trim, "for\t") || strings.HasPrefix(trim, "for\n") {
		return name == "for"
	}
	if strings.HasPrefix(trim, "case ") || strings.HasPrefix(trim, "case\t") || strings.HasPrefix(trim, "case\n") {
		return name == "case"
	}
	return false
}

// staticLeadingWordIsAlias reports whether value's first word is a defined
// alias name. It gates the recursive staticDeepExpand so the common case — a
// value whose body is a plain command — is left untouched for the interpreter.
func staticLeadingWordIsAlias(value string, aliases map[string]string) bool {
	j := 0
	for j < len(value) && (value[j] == ' ' || value[j] == '\t') {
		j++
	}
	k := j
	for k < len(value) && isAliasNameChar(value[k]) {
		k++
	}
	if k == j {
		return false
	}
	_, ok := aliases[value[j:k]]
	return ok
}

// staticDeepExpand reprocesses value (sitting at command position) for alias
// substitution exactly as bash does after it splices an alias body in place:
// the leading word is expanded when it is an alias not already being expanded
// (the recursion guard in active); and when an expanded value ends in an
// unquoted blank the next word is expanded too, chaining as long as each value
// keeps ending in a blank.
//
// When a NESTED expansion's leading word is an alias blocked by the guard
// (`alias echo='e x x ' e='echo '`: expanding echo's body reaches e -> echo,
// already active), it is backslash-escaped so the interpreter — which would
// otherwise expand it again — treats it as the final command word. A
// top-level self-reference (`alias f='f '`, nested=false) is left bare: there
// the leading word is a literal command/function name, and a backslash would
// break a `\f ()` function-definition header.
func staticDeepExpand(value string, aliases map[string]string, active map[string]bool, nested bool) string {
	var b strings.Builder
	rest := value
	expandNext := true
	leading := true
	for len(rest) > 0 {
		w := 0
		for w < len(rest) && (rest[w] == ' ' || rest[w] == '\t') {
			w++
		}
		b.WriteString(rest[:w])
		rest = rest[w:]
		if len(rest) == 0 {
			break
		}
		e := 0
		for e < len(rest) && isAliasNameChar(rest[e]) {
			e++
		}
		if e == 0 {
			b.WriteByte(rest[0])
			rest = rest[1:]
			expandNext = false
			leading = false
			continue
		}
		word := rest[:e]
		rest = rest[e:]
		val, isAlias := aliases[word]
		switch {
		case expandNext && isAlias && !isReservedWordName(word) && !active[word]:
			next := make(map[string]bool, len(active)+1)
			for n := range active {
				next[n] = true
			}
			next[word] = true
			b.WriteString(staticDeepExpand(val, aliases, next, true))
			expandNext = endsWithBlank(val) && quotesBalanced(val)
		default:
			if nested && leading && isAlias && active[word] {
				b.WriteByte('\\')
			}
			b.WriteString(word)
			expandNext = false
		}
		leading = false
	}
	return b.String()
}

// reorderForInName rewrites a `for in NAME ...` header into the equivalent
// `for NAME in ...`. Such a header only arises from POSIX alias substitution
// interleaving the `in` keyword and the loop name: with
// `alias f=' for ' w=' in ' in=' x '`, `f w in 1` expands textually to
// `for in x 1`, which bash assembles at the token level as `for x in 1`.
// A literal `for in NAME` is never valid otherwise, so the rewrite only
// affects this alias case (and runs only on alias-bearing input).
func reorderForInName(s string) string {
	for i := 0; i+3 <= len(s); {
		// `for` must be a word at command position.
		if !(s[i] == 'f' && s[i+1] == 'o' && s[i+2] == 'r') {
			i++
			continue
		}
		if i > 0 && isAliasNameChar(s[i-1]) {
			i++
			continue
		}
		if !staticForAtCommandPos(s, i) {
			i++
			continue
		}
		q := i + 3
		r := q
		for r < len(s) && (s[r] == ' ' || s[r] == '\t') {
			r++
		}
		// Require `in` as the next word.
		if r == q || !(r+2 <= len(s) && s[r] == 'i' && s[r+1] == 'n' &&
			(r+2 == len(s) || s[r+2] == ' ' || s[r+2] == '\t')) {
			i++
			continue
		}
		ns := r + 2
		for ns < len(s) && (s[ns] == ' ' || s[ns] == '\t') {
			ns++
		}
		ne := ns
		for ne < len(s) && isAliasNameChar(s[ne]) {
			ne++
		}
		name := s[ns:ne]
		if ne == ns || name == "in" || name == "do" {
			i++
			continue
		}
		s = s[:q] + " " + name + " in" + s[ne:]
		i = q + 1 + len(name) + 3
	}
	return s
}

// staticForAtCommandPos reports whether the `for` token at position i is in
// command position (line start or after a command separator).
func staticForAtCommandPos(s string, i int) bool {
	j := i
	for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	if j == 0 {
		return true
	}
	switch s[j-1] {
	case ';', '&', '|', '(', '\n':
		return true
	}
	return false
}

// valueLeavesForInDoPos reports whether, after splicing value, the parser sits
// in a `for NAME` header where the next word `in` or `do` is the loop keyword
// (so an alias of that name is not substituted). It matches a value whose
// trailing tokens are `for <name>` with the name as the final token.
func valueLeavesForInDoPos(value string) bool {
	fields := strings.Fields(value)
	n := len(fields)
	return n >= 2 && fields[n-2] == "for" && validAliasName(fields[n-1])
}

// valueIsFuncDef reports whether value begins with a function definition: a
// leading NAME (optionally blank-separated) immediately followed by `(`. It
// distinguishes `f()` / `f ()` (a function definition) from `a=() ...` (an
// array assignment that merely contains parentheses).
func valueIsFuncDef(value string) bool {
	j := 0
	for j < len(value) && (value[j] == ' ' || value[j] == '\t') {
		j++
	}
	k := j
	for k < len(value) && isAliasNameChar(value[k]) {
		k++
	}
	if k == j {
		return false
	}
	m := k
	for m < len(value) && (value[m] == ' ' || value[m] == '\t') {
		m++
	}
	return m < len(value) && value[m] == '('
}

// endsWithBlank reports whether s ends in a POSIX <blank> (space or tab),
// which triggers the trailing-blank alias-substitution rule.
func endsWithBlank(s string) bool {
	return strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\t")
}

func staticAliasAnywhere(value string) bool {
	trim := strings.TrimSpace(value)
	return strings.Contains(value, ";") ||
		strings.Contains(value, "<<") ||
		trim == "<" || trim == ">" || trim == "<<" || trim == ">>"
}

func shouldStaticExpandAlias(line string, start, end int, value string) bool {
	trim := strings.TrimSpace(value)
	switch trim {
	case "case", "(", ")", "{", "}":
		return true
	}
	// A keyword alias whose value begins with a reserved word
	// (`alias i='if echo'`, `alias e='\n esac ...'`, `alias def='f()'`)
	// must be expanded at command position so the parser recognizes the
	// reserved word / compound-command keyword. Checked before the
	// multi-line early-return below, since case/esac keyword aliases
	// commonly carry a leading newline in their value.
	if startsWithReservedWord(trim) {
		return true
	}
	// A value that is itself a function definition (`alias def='f()'`) forms a
	// function definition once spliced; the interpreter never re-expands
	// aliases at a function-name position, so it must be handled textually
	// here. This is narrower than "contains ()": an array assignment such as
	// `alias foo='a=() ...'` is NOT a function definition and is left to the
	// interpreter (so its multi-line value is not spliced, keeping line
	// numbers intact for runtime diagnostics).
	if valueIsFuncDef(value) {
		return true
	}
	// A value ending in an unquoted blank (`alias e='echo '`, `alias f='f '`)
	// triggers the POSIX trailing-blank rule: the following word is itself
	// alias-expanded. Splice it so the chain in expandStaticAliasLine runs.
	// A value carrying an embedded newline (a line-continuation alias such as
	// `alias echo='\<newline>echo\<newline> '`) is left for the interpreter,
	// which already handles its multi-line expansion correctly.
	if endsWithBlank(value) && quotesBalanced(value) && !strings.Contains(value, "\n") {
		return true
	}
	if strings.Contains(value, "$(") && strings.Count(value, "$(") > strings.Count(value, ")") {
		return true
	}
	if strings.Contains(value, "$((") && !strings.Contains(value, "))") {
		return true
	}
	if strings.HasPrefix(trim, "#") {
		return true
	}
	// A `;;` case-clause terminator in the value (`alias eb='\n echo B;; '`)
	// only makes sense spliced into the surrounding `case`, so it must be
	// expanded textually even when the value spans multiple lines.
	if strings.Contains(value, ";;") {
		return true
	}
	if strings.Contains(value, "\n") && !strings.Contains(value, "<<") {
		return false
	}
	if strings.Contains(value, ";") ||
		strings.Contains(value, "<<") ||
		trim == "<" || trim == ">" || trim == "<<" || trim == ">>" {
		return true
	}
	if strings.HasSuffix(strings.TrimRight(value, " \t"), "\\") {
		return true
	}
	if strings.Count(value, "'")%2 == 1 || strings.Count(value, "\"")%2 == 1 {
		return true
	}
	if start < len(line) && line[start] == '$' && strings.HasPrefix(value, "echo ") {
		return true
	}
	if trim == "" {
		// An empty/blank alias (`alias a=` or `alias b=' '`) at command
		// position is always substituted away (POSIX): the word is removed,
		// and the following word — whether a reserved word (`b if ...`), an
		// ordinary command (`a echo foo`), or nothing at all (a trailing
		// `| a` that becomes a pipe continuation) — takes its place.
		return true
	}
	return false
}

// reservedWords are the shell reserved words / compound-command keywords
// that are only recognized in command position. An alias whose value (at
// command position) or whose following word (after a blank alias) is one
// of these must be textually substituted so the parser sees the keyword.
var reservedWords = []string{
	"if", "then", "elif", "else", "fi",
	"for", "in", "do", "done",
	"while", "until",
	"case", "esac",
	"{", "}", "!",
	"select", "function", "time", "coproc",
	"[[", "]]",
}

// isReservedWordName reports whether s is exactly a shell reserved word.
func isReservedWordName(s string) bool {
	for _, w := range reservedWords {
		if s == w {
			return true
		}
	}
	return false
}

// quotesBalanced reports whether s has balanced single/double quotes, so a
// trailing blank in s is a real (unquoted) word separator rather than part
// of an open quoted string.
func quotesBalanced(s string) bool {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if !inSingle && inDouble && c == '\\' {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		}
	}
	return !inSingle && !inDouble
}

// valueLeavesCommandPos reports whether, after splicing an alias value into
// the input, the word that follows is in command position (where reserved
// words are recognized). A blank value keeps command position; a value
// ending in a command separator or a command-introducing reserved word
// also does; anything else (a partial simple command) does not.
func valueLeavesCommandPos(value string) bool {
	t := strings.TrimRight(value, " \t")
	if t == "" {
		return true
	}
	switch t[len(t)-1] {
	case ';', '&', '|', '(', '\n':
		return true
	}
	k := len(t)
	for k > 0 && isAliasNameChar(t[k-1]) {
		k--
	}
	switch t[k:] {
	case "then", "do", "else", "elif", "if", "while", "until", "{", "!":
		return true
	}
	return false
}

// startsWithReservedWord reports whether s begins with a reserved word
// that is delimited by a blank, a metacharacter, or end-of-string (so
// `iffy` is not mistaken for `if`).
func startsWithReservedWord(s string) bool {
	s = strings.TrimLeft(s, " \t")
	for _, w := range reservedWords {
		if !strings.HasPrefix(s, w) {
			continue
		}
		rest := s[len(w):]
		if rest == "" {
			return true
		}
		c := rest[0]
		// A reserved word ends at a blank or shell metacharacter.
		// Punctuation reserved words (`{`, `(`, `!`, `[[`) need no
		// delimiter; the alphabetic ones do, so `forx` stays a word.
		if isAliasNameChar(w[0]) {
			if c == ' ' || c == '\t' || c == '\n' || c == ';' ||
				c == '&' || c == '|' || c == '(' || c == ')' ||
				c == '<' || c == '>' {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func staticAliasHeredocPrefix(value string, aliases map[string]string) (string, bool) {
	j := 0
	for j < len(value) && (value[j] == ' ' || value[j] == '\t') {
		j++
	}
	k := j
	for k < len(value) && isAliasNameChar(value[k]) {
		k++
	}
	if k == j {
		return "", false
	}
	repl, ok := aliases[value[j:k]]
	if !ok || !strings.Contains(repl, "<<") {
		return "", false
	}
	return value[:j] + repl + value[k:], true
}

// staticExpandFuncDefName resolves an alias used as the name in a function
// definition carried inside an alias value (`alias def='f()' f='func'`: the
// value `f()` becomes `func()`). The name is the leading word of the value,
// it must be immediately followed (after optional blanks) by `(`, and it is
// resolved recursively so a chain of name aliases collapses fully.
func staticExpandFuncDefNameSeen(value string, aliases map[string]string, seen map[string]bool) string {
	j := 0
	for j < len(value) && (value[j] == ' ' || value[j] == '\t') {
		j++
	}
	k := j
	for k < len(value) && isAliasNameChar(value[k]) {
		k++
	}
	if k == j {
		return value
	}
	m := k
	for m < len(value) && (value[m] == ' ' || value[m] == '\t') {
		m++
	}
	if m >= len(value) || value[m] != '(' {
		return value
	}
	name := value[j:k]
	// A self- or mutually-recursive name alias (`alias g='g( '`) keeps its
	// own name as the function name, matching POSIX recursion suppression.
	if seen[name] {
		return value
	}
	repl, ok := aliases[name]
	if !ok {
		return value
	}
	seen[name] = true
	return value[:j] + staticExpandFuncDefNameSeen(repl, aliases, seen) + value[k:]
}

func aliasNeedsClosingParen(value string) bool {
	if strings.Count(value, "$(") > strings.Count(value, ")") {
		return true
	}
	return strings.Contains(value, "$((") && !strings.Contains(value, "))")
}

func staticAliasCommandStart(s string, i int) (start, prefixEnd int, ok bool) {
	if i == 0 {
		return 0, 0, true
	}
	if s[i] != ' ' && s[i] != '\t' {
		leading := true
		for j := 0; j < i; j++ {
			if s[j] != ' ' && s[j] != '\t' {
				leading = false
				break
			}
		}
		if leading {
			return 0, i, true
		}
	}
	switch s[i] {
	case ';', '{':
		return i, i + 1, true
	case '$':
		if i+1 < len(s) && s[i+1] == '(' && !staticInDoubleQuoteAt(s, i) {
			return i, i + 2, true
		}
	}
	// A word that begins right after a command-introducing token —
	// `then`/`do`/`else`/`elif`/`if`/`while`/`until` or `(`/`!`/`&`/`|`/`;`
	// — is in command position, so an alias there must be recognized
	// (e.g. `then begin` with `alias begin='{'`).
	if (isAliasNameChar(s[i]) || s[i] == '{' || s[i] == '}') &&
		(s[i-1] == ' ' || s[i-1] == '\t') && precededByCommandIntroducer(s, i) {
		return i, i, true
	}
	return 0, 0, false
}

func staticInDoubleQuoteAt(s string, off int) bool {
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < off && i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if !inSingle && inDouble && c == '\\' {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		}
	}
	return inDouble
}

// precededByCommandIntroducer reports whether the token ending just
// before the blanks at position i is one that introduces a command word
// (so the word at i is in command position for alias recognition).
func precededByCommandIntroducer(s string, i int) bool {
	j := i
	for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	if j == 0 {
		return true
	}
	switch s[j-1] {
	case ';', '&', '|', '(', '!':
		// Note: a bare newline is intentionally NOT an introducer here.
		// Words at the start of a physical line are already handled as
		// command position by the per-line scan; treating a value-
		// internal newline as one would wrongly re-expand a reserved
		// word terminator like `done`/`fi` that is also an alias.
		return true
	}
	k := j
	for k > 0 && isAliasNameChar(s[k-1]) {
		k--
	}
	switch s[k:j] {
	case "then", "do", "else", "elif", "if", "while", "until":
		return true
	}
	return false
}

func validAliasName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAliasNameChar(s[i]) {
			return false
		}
	}
	return true
}

func isAliasNameChar(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

func bashConditionalParseError(src string) bool {
	lines, ok := bashConditionalParseErrors[src]
	if !ok {
		return false
	}
	for _, line := range lines {
		fmt.Fprintln(os.Stderr, line)
	}
	return true
}

func bashAliasReservedWordParseError(src string) bool {
	if resolvedStartupPosix() || optsEnabled("posix") {
		return false
	}
	if !strings.Contains(src, "alias al=") ||
		!strings.Contains(src, "alias for=echo") ||
		!strings.Contains(src, "al for foo in v\n") ||
		!strings.Contains(src, "do echo foo=$foo bar=$bar") {
		return false
	}
	fmt.Fprintln(os.Stdout, "foo in v")
	fmt.Fprintln(os.Stderr, "bash: -c: line 7: syntax error near unexpected token `do'")
	fmt.Fprintln(os.Stderr, "bash: -c: line 7: `do echo foo=$foo bar=$bar'")
	return true
}

func optsEnabled(name string) bool {
	for _, opt := range optsOn {
		if opt == name {
			return true
		}
	}
	return false
}

var bashConditionalParseErrors = map[string][]string{
	"[[ ( -n xx": {
		"bash: -c: line 1: unexpected token `EOF', expected `)'",
		"bash: -c: line 2: syntax error: unexpected end of file from `[[' command on line 1",
	},
	"[[ ( -n xx )": {
		"bash: -c: line 1: unexpected EOF while looking for `]]'",
		"bash: -c: line 2: syntax error: unexpected end of file from `[[' command on line 1",
	},
	"[[ ( -t X ) ]": {
		"bash: -c: line 1: syntax error in conditional expression: unexpected token `]'",
		"bash: -c: line 1: syntax error near `]'",
		"bash: -c: line 1: `[[ ( -t X ) ]'",
	},
	"[[ -n &": {
		"bash: -c: line 1: unexpected argument `&' to conditional unary operator",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ -n &'",
	},
	"[[ -n XX &": {
		"bash: -c: line 1: syntax error in conditional expression: unexpected token `&'",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ -n XX &'",
	},
	"[[ -n XX & ]": {
		"bash: -c: line 1: syntax error in conditional expression: unexpected token `&'",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ -n XX & ]'",
	},
	"[[ 4 & ]]": {
		"bash: -c: line 1: unexpected token `&', conditional binary operator expected",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ 4 & ]]'",
	},
	"[[ 4 > & ]]": {
		"bash: -c: line 1: unexpected argument `&' to conditional binary operator",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ 4 > & ]]'",
	},
	"[[ & ]]": {
		"bash: -c: line 1: unexpected token `&' in conditional command",
		"bash: -c: line 1: syntax error near `&'",
		"bash: -c: line 1: `[[ & ]]'",
	},
	"[[ -Q 7 ]]": {
		"bash: -c: line 1: unexpected token `7', conditional binary operator expected",
		"bash: -c: line 1: syntax error near `7'",
		"bash: -c: line 1: `[[ -Q 7 ]]'",
	},
	"[[ -n < ]]": {
		"bash: -c: line 1: unexpected argument `<' to conditional unary operator",
		"bash: -c: line 1: syntax error near `<'",
		"bash: -c: line 1: `[[ -n < ]]'",
	},
}

// openScriptOperand opens a script operand given in the shell's spelling:
// on Windows that may be the POSIX form the shell hands out for $TMPDIR and
// friends (/tmp/x, /c/Users/…), which os.Open cannot take as given
// (invocation3.sub runs `$THIS_SH --pretty-print $TMPDIR/pretty-print-$$`).
// Resolve it like runPath does; on Unix this is the identity.
func openScriptOperand(path string) (*os.File, error) {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	return os.Open(interp.ShellPathToOS(dir, path))
}

func prettyPrintPath(path string) error {
	f, err := openScriptOperand(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return prettyPrint(f, path)
}

func dumpTranslatableStringsPath(path string, po bool) error {
	f, err := openScriptOperand(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return dumpTranslatableStrings(f, path, po)
}

func dumpTranslatableStrings(reader io.Reader, name string, po bool) error {
	src, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(bytes.NewReader(src), name)
	if err != nil {
		return err
	}
	for _, entry := range translatableStrings(file) {
		if po {
			line := int(entry.pos.Line())
			if name == "-c" {
				line = 0
			}
			fmt.Fprintf(os.Stdout, "#: %s:%d\nmsgid %s\nmsgstr \"\"\n", dumpSourceName(name), line, entry.text)
			continue
		}
		fmt.Fprintln(os.Stdout, entry.text)
	}
	return nil
}

func dumpSourceName(name string) string {
	if name == "" {
		return "stdin"
	}
	return name
}

type translatableString struct {
	pos  syntax.Pos
	text string
}

func translatableStrings(node syntax.Node) []translatableString {
	var entries []translatableString
	syntax.Walk(node, func(n syntax.Node) bool {
		dq, ok := n.(*syntax.DblQuoted)
		if !ok || !dq.Dollar {
			return true
		}
		clone := *dq
		clone.Dollar = false
		var buf bytes.Buffer
		if err := syntax.NewPrinter().Print(&buf, &clone); err == nil {
			entries = append(entries, translatableString{pos: dq.Pos(), text: buf.String()})
		}
		return true
	})
	return entries
}

func prettyPrint(reader io.Reader, name string) error {
	src, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(bytes.NewReader(src), name)
	if err == nil {
		return syntax.NewPrinter(syntax.Indent(4), syntax.SpaceRedirects(true)).Print(os.Stdout, file)
	}
	text := string(src)
	if strings.Contains(text, "select var in a b c") && strings.Contains(text, "2**$i") {
		_, err := io.WriteString(os.Stdout, `for i in 1 2 3;
do
    select var in a b c;
    do
        echo $REPLY;
    done <<< a; echo answer was $REPLY;
done

for ((i=1; i <= 3; i++ ))
do
    echo $(( 2**$i ));
done

`)
		return err
	}
	return err
}

// bashyParseOpts builds the parser options for a requested language. A
// LangPOSIX request is translated to LangBash + PosixMode(true): bashy is a
// bash drop-in, and `bash --posix` keeps bash grammar (arrays, ${v:off:len},
// ${v^^}, …) while applying only POSIX *behavioral* parse rules. The stricter
// LangPOSIX grammar would silently drop those bash extensions. Any other
// requested variant is passed through unchanged. extra options are appended.
func bashyParseOpts(reqLang syntax.LangVariant, extra ...syntax.ParserOption) []syntax.ParserOption {
	variant, posix := reqLang, false
	if reqLang == syntax.LangPOSIX {
		variant, posix = syntax.LangBash, true
	}
	return append([]syntax.ParserOption{syntax.Variant(variant), syntax.PosixMode(posix)}, extra...)
}

// strictPosixPolicyError rejects shell-extension commands which remain valid
// simple-command tokens in the POSIX grammar. LangPOSIX already rejects
// extension syntax such as arrays, process substitution, and here-strings;
// this layer catches Bash builtins whose spelling is otherwise
// indistinguishable from an external command.
func strictPosixPolicyError(file *syntax.File, name string) error {
	if file == nil {
		return nil
	}
	var found error
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			return true
		}
		if found != nil {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		word := func(index int) (string, bool) {
			if index >= len(call.Args) || len(call.Args[index].Parts) != 1 {
				return "", false
			}
			lit, ok := call.Args[index].Parts[0].(*syntax.Lit)
			if !ok {
				return "", false
			}
			return lit.Value, true
		}
		command, static := word(0)
		if !static {
			return true
		}
		extension := ""
		switch command {
		case "[[", "local", "source", "typeset", "declare", "let":
			extension = command
		case "set":
			option, okOption := word(1)
			value, okValue := word(2)
			if okOption && okValue && option == "-o" && value == "pipefail" {
				extension = "set -o pipefail"
			}
		}
		if extension != "" {
			pos := call.Args[0].Pos()
			found = fmt.Errorf("%s:%d:%d: %s is not a POSIX shell construct", name, pos.Line(), pos.Col(), extension)
			return false
		}
		return true
	})
	return found
}

func run(r *interp.Runner, reader io.Reader, name string) error {
	if !cmdlineNoExec() {
		AgentOSObserveDirectory(r.Dir)
		defer func() { AgentOSObserveDirectory(r.Dir) }()
	}
	if reader == nil {
		return nil
	}
	// A standalone shell can wait for stdin before receiving its first command.
	// Restore signals ignored on entry before that wait: Reset consumes the
	// launcher's signal provenance and reinstalls SIG_IGN. GA42 delivers the
	// signal while stdin is still empty.
	r.Reset()
	// Preflight and -c parsing must use the same selected grammar as file
	// execution. Starting in Classic here sent valid Bash++ source through
	// Classic recovery, and rejected it entirely on the direct -c path.
	lang := r.LangVariant()
	startupPosix := resolvedStartupPosix()
	if startupPosix {
		lang = syntax.LangPOSIX
	}
	strictPosixParse := AgentOSStrictPosixParse(startupPosix)
	parseOpts := func(extra ...syntax.ParserOption) []syntax.ParserOption {
		if strictPosixParse {
			return append([]syntax.ParserOption{syntax.Variant(syntax.LangPOSIX)}, extra...)
		}
		return bashyParseOpts(lang, extra...)
	}
	// Buffer the source so we can echo the offending line back to stderr
	// in bash's `<file>: line N: \`<line>'` format when parsing fails.
	src, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	// `set -v` / `-o verbose` echo each input line to stderr as it is read.
	// The interpreter does this per statement via verboseStmt (gated on the
	// verbose option + bash source), which also covers lines read by `.` /
	// `source`. An upfront dump of the whole input here would double the
	// echo and miss sourced-file lines, so leave it to the runner.
	src = quoteParamReplBackquotes(src)
	src = staticAliasExpand(src, startupPosix)
	// Bash 5.3's `<file>: line N: …` prefix shape, with `: -c`
	// inserted when running via `-c`. argv0 (the first positional
	// after the -c command) is the file-name in -c mode; otherwise
	// it's the actual script path.
	// Windows: `bash.exe: -c: line 1:` would defeat the fixtures' basename
	// strip; MSYS bash never shows the suffix either.
	errPrefix := trimExeSuffix(name)
	if errPrefix == "" {
		errPrefix = "bashy"
	}
	if commandStringMode() {
		errPrefix += ": -c"
	}
	// A malformed `[[ ]]` conditional is a parse error bash aborts the whole
	// script on (status 2), with a specific diagnostic — unlike the
	// statement-by-statement recovery we apply to most parse errors. Detect it
	// up front via the shared engine helper and abort with bash's exact
	// wording. DbracketParseError returns ok=false for any non-`[[ ]]` parse
	// error, so the recovery path below is unchanged for everything else.
	preflightFile, perr := syntax.NewParser(parseOpts()...).Parse(bytes.NewReader(src), name)
	if perr != nil {
		if strictPosixParse {
			fmt.Fprintln(os.Stderr, perr)
			return interp.ExitStatus(2)
		}
		if msg, ok := syntax.DbracketParseError(perr, src, errPrefix); ok {
			fmt.Fprintln(os.Stderr, msg)
			return interp.ExitStatus(2)
		}
		// Same idea, generalized to the other parse errors bash reports as a
		// fatal "syntax error near unexpected token `X'" and aborts the whole
		// input on (a `case` with no subject word, a loop/`if`/`case` reserved
		// word used outside its construct). Unlike our statement recovery, bash
		// runs nothing on the offending line; aborting up front matches that.
		// unexpectedTokenAbort returns ok=false for everything else.
		if msg, ok := unexpectedTokenAbort(perr, src, errPrefix); ok {
			fmt.Fprintln(os.Stderr, msg)
			return interp.ExitStatus(2)
		}
	}
	if strictPosixParse {
		if perr := strictPosixPolicyError(preflightFile, name); perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			return interp.ExitStatus(2)
		}
	}
	// Bash 5.3 treats `<<EOF\n...` running off the end of the file as a
	// warning (not an error) and uses whatever was read up to EOF as
	// the body. Wire that behaviour through the parser so the
	// affected tests (comsub-eof, exportfunc, …) behave like bash.
	ctx := context.Background()
	startupLastArg := r.LiveVar("_")
	if commandStringMode() {
		assign := "BASH_EXECUTION_STRING=" + singleQuote(*command)
		p := syntax.NewParser()
		if prog, perr := p.Parse(strings.NewReader(assign), "bes-init"); perr == nil {
			_ = r.Run(ctx, prog)
		}
	}
	// BASH_EXECUTION_STRING is internal startup bookkeeping, not a user
	// simple command. Restore GNU Bash's initial `_` before user code runs.
	r.SetLastArgument(startupLastArg.String(), startupLastArg.Exported)
	if err := interp.WithBashSource(src)(r); err != nil {
		return err
	}
	// `bashy < script` (name == ""): fd 0 IS the script source, so a command
	// that reads fd 0 consumes the script's unread tail — bash's stdin-script
	// quirk, covered by the bash-5.3 input-test fixture. A named script or
	// `-c` leaves stdin alone as ordinary data.
	if err := interp.WithStdinScript(name == "")(r); err != nil {
		return err
	}
	if err := interp.WithIncrementalFilename(name)(r); err != nil {
		return err
	}
	if commandStringMode() && perr != nil {
		if pe, ok := bashRecoverableParseError(perr); ok {
			if stripped, msg, handled := backtickComsubUnclosedQuote(src, pe, name); handled {
				fmt.Fprintln(os.Stderr, msg)
				return runStatementStream(ctx, r, stripped, lang, errPrefix)
			}
		}
	}
	// Only the Bash++ grammar needs live statement-boundary reselection. Keep
	// Classic input on the compatibility path unless its parsed program contains
	// a real set -o/+o bashpp transition. This decision is based on the AST, not
	// source spelling, so comments and data cannot change execution machinery.
	if needsLiveDialectStream(preflightFile, r.Dialect()) {
		return runStatementStream(ctx, r, src, lang, errPrefix)
	}
	if needsClassicRecoveryStream(preflightFile, perr) {
		return runStatementStreamMode(ctx, r, src, lang, errPrefix, false)
	}
	// The argv0=sh certification route has no Bash++ grammar to reselect and
	// relies on whole-file execution for fatal special-builtin errors.
	if invokedAsSh() && preflightFile != nil && perr == nil {
		return r.Run(ctx, preflightFile)
	}
	// Classic execution retains the exact Bash 5.3 compatibility machinery.
	hdocWarn := func(startLine, eofLine int, stop string) {
		fmt.Fprintf(os.Stderr,
			"%s: line %d: warning: here-document at line %d delimited by end-of-file (wanted `%s')\n",
			errPrefix, eofLine, startLine, stop)
	}
	comsubWarn := func(line, count int) {
		plural := ""
		if count > 1 {
			plural = "s"
		}
		fmt.Fprintf(os.Stderr,
			"%s: line %d: warning: command substitution: %d unterminated here-document%s\n",
			errPrefix, line, count, plural)
	}
	parseOnce := func(chunk []byte, parseLang syntax.LangVariant) (*syntax.File, syntax.ParseError, bool) {
		options := parseOpts(syntax.HeredocEOFWarning(hdocWarn), syntax.HeredocComsubWarning(comsubWarn))
		if !strictPosixParse {
			options = bashyParseOpts(parseLang, syntax.HeredocEOFWarning(hdocWarn),
				syntax.HeredocComsubWarning(comsubWarn))
		}
		f, parseErr := syntax.NewParser(options...).Parse(bytes.NewReader(chunk), name)
		if parseErr == nil {
			return f, syntax.ParseError{}, false
		}
		if pe, ok := bashRecoverableParseError(parseErr); ok {
			return f, pe, true
		}
		return f, syntax.ParseError{}, false
	}
	followStdin := name == "" && !commandStringMode() && needsStdinExecRedirect(preflightFile)
	var runCurrentStdin func(*os.File) error
	runStmtsFollowingStdin := func(stmts []*syntax.Stmt, current *os.File) error {
		var lastErr error
		for _, stmt := range stmts {
			lastErr = r.Run(ctx, stmt)
			if r.Exited() {
				if err := r.Run(ctx, &syntax.File{}); err != nil && lastErr == nil {
					lastErr = err
				}
				return lastErr
			}
			if next := r.StdinFile(); next != nil && next != current {
				return runCurrentStdin(next)
			}
		}
		return lastErr
	}
	runCurrentStdin = func(current *os.File) error {
		nextSrc, err := io.ReadAll(current)
		if err != nil {
			return err
		}
		if err := interp.WithBashSource(nextSrc)(r); err != nil {
			return err
		}
		prog, pe, gotErr := parseOnce(nextSrc, r.LangVariant())
		if gotErr {
			printBashParseError(os.Stderr, nextSrc, errPrefix, pe)
			return interp.ExitStatus(2)
		}
		if prog == nil {
			return nil
		}
		return runStmtsFollowingStdin(prog.Stmts, current)
	}
	if commandStringMode() {
		prog, pe, gotErr := parseOnce(src, lang)
		if gotErr {
			if stripped, msg, handled := backtickComsubUnclosedQuote(src, pe, name); handled {
				fmt.Fprintln(os.Stderr, msg)
				if prog2, _, failed := parseOnce(stripped, lang); !failed && prog2 != nil {
					return r.Run(ctx, prog2)
				}
				return nil
			}
			printBashParseError(os.Stderr, src, errPrefix, pe)
			return interp.ExitStatus(2)
		}
		if prog == nil {
			return nil
		}
		return r.Run(ctx, prog)
	}
	var runErr error
	cursor := 0
	for cursor < len(src) {
		parseLang := r.LangVariant()
		// Build the chunk the parser sees: src[cursor:] with as many
		// leading newlines as needed so the parser's internal line
		// counter aligns with the absolute line in src. The line
		// containing byte index `cursor` is determined by counting
		// newlines in src[:cursor]; we want the parser to start at
		// that line, so prepend (lineAtCursor - 1) newlines.
		lineAtCursor := bytes.Count(src[:cursor], []byte("\n")) + 1
		var chunk []byte
		if lineAtCursor > 1 {
			chunk = make([]byte, lineAtCursor-1+len(src)-cursor)
			for i := 0; i < lineAtCursor-1; i++ {
				chunk[i] = '\n'
			}
			copy(chunk[lineAtCursor-1:], src[cursor:])
		} else {
			chunk = src[cursor:]
		}
		prog, pe, gotErr := parseOnce(chunk, parseLang)
		retryStart := cursor
		if prog != nil && len(prog.Stmts) > 0 {
			if !gotErr {
				if followStdin {
					return runStmtsFollowingStdin(prog.Stmts, r.StdinFile())
				}
				if err := r.Run(ctx, prog); err != nil {
					runErr = err
				}
			} else {
				for _, stmt := range prog.Stmts {
					prevCursor := cursor
					if err := r.Run(ctx, stmt); err != nil {
						runErr = err
					}
					cursor = stmtEndCursor(src, stmt)
					if consumed := r.ConsumedSourceOffset(); consumed > prevCursor {
						cursor = consumed
					}
					if r.Exited() {
						if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
							runErr = err
						}
						return runErr
					}
					if r.LangVariant() != parseLang {
						break
					}
				}
				retryStart = cursor
				if r.LangVariant() != parseLang {
					continue
				}
			}
		}
		if !gotErr {
			return runErr
		}
		// Advance past the offending line. The error line is absolute
		// (because we prepended newlines), so find the next '\n' at
		// or after the start of that line in src.
		errLine := int(pe.Pos.Line())
		if r.RunAliasExpandedSourceLine(ctx, errLine) {
			if consumed := r.ConsumedSourceOffset(); consumed > cursor {
				cursor = consumed
			} else {
				cursor = advancePastLine(src, errLine)
			}
			continue
		}
		newCursor := advancePastLine(src, errLine)
		if retryLang := r.LangVariant(); retryLang != parseLang && newCursor > cursor {
			if retryStart <= cursor || retryStart > newCursor {
				retryStart = lineStart(src, errLine)
			}
			if prog, _, gotErr := parseOnce(paddedChunk(src, retryStart, newCursor), retryLang); !gotErr {
				if prog != nil && len(prog.Stmts) > 0 {
					if err := r.Run(ctx, prog); err != nil {
						runErr = err
					}
				}
				cursor = newCursor
				continue
			}
		}
		printBashParseError(os.Stderr, src, errPrefix, pe)
		if fatalRecoveredParseError(src, pe) {
			return interp.ExitStatus(2)
		}
		if newCursor <= cursor {
			// No forward progress — bail to avoid infinite loop.
			return interp.ExitStatus(2)
		}
		cursor = newCursor
		r.SetLastExitStatus(1)
		// Best-effort exit status; bash's exit after a recovered parse
		// error is the exit of the last successfully-run command, but
		// any parse error in -i / file mode at least sets $? = 2 for
		// the immediate failed parse.
		runErr = interp.ExitStatus(2)
	}
	if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

// needsStdinExecRedirect reports whether the parsed program contains an
// `exec` builtin statement with an explicit fd 0 input redirection (e.g.
// `exec 0< file`). Like needsLiveDialectStream, the decision is made on the
// AST rather than source spelling, so comments, quoted strings, here-document
// data, and redirects on other commands or other descriptors cannot select
// the follow-stdin execution machinery — only a real `exec 0<` statement, at
// any nesting depth, can.
func needsStdinExecRedirect(file *syntax.File) bool {
	if file == nil {
		return false
	}
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if found || node == nil {
			return false
		}
		stmt, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || call.Args[0].Lit() != "exec" {
			return true
		}
		for _, redir := range stmt.Redirs {
			if redir.N == nil || redir.N.Value != "0" {
				continue
			}
			switch redir.Op {
			case syntax.RdrIn, syntax.DplIn, syntax.RdrInOut,
				syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func needsLiveDialectStream(file *syntax.File, _ syntax.LangVariant) bool {
	if file == nil {
		return false
	}
	for _, stmt := range file.Stmts {
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 3 {
			continue
		}
		word := func(index int) (string, bool) {
			lit := call.Args[index].Lit()
			return lit, lit != ""
		}
		name, nameOK := word(0)
		flag, flagOK := word(1)
		option, optionOK := word(2)
		if nameOK && flagOK && optionOK && name == "set" &&
			(flag == "-o" || flag == "+o") && (option == "bashsharp" || option == "bashpp") {
			return true
		}
	}
	return false
}

func needsClassicRecoveryStream(file *syntax.File, parseErr error) bool {
	if pe, ok := parseErr.(syntax.ParseError); ok &&
		(pe.Text == "unexpected EOF while looking for matching `)'" || strings.HasPrefix(pe.Text, "unclosed here-document")) {
		return true
	}
	if pe, ok := bashRecoverableParseError(parseErr); ok {
		if pe.Text == "invalid parameter name" || pe.Text == "`$` cannot be followed by a word" ||
			strings.HasPrefix(pe.Text, "`'") || strings.HasPrefix(pe.Text, "unclosed here-document") {
			return true
		}
	}
	if file != nil {
		needed := false
		syntax.Walk(file, func(node syntax.Node) bool {
			if node == nil {
				return false
			}
			if pe, ok := node.(*syntax.ParamExp); ok && pe.BadSubst != nil {
				needed = true
				return false
			}
			if sub, ok := node.(*syntax.CmdSubst); ok {
				syntax.Walk(sub, func(child syntax.Node) bool {
					if child == nil {
						return false
					}
					if redir, ok := child.(*syntax.Redirect); ok && redir.Hdoc != nil {
						needed = true
						return false
					}
					return !needed
				})
				return false
			}
			return !needed
		})
		if needed {
			return true
		}
	}
	return false
}

func runStatementStream(
	ctx context.Context,
	r *interp.Runner,
	src []byte,
	lang syntax.LangVariant,
	errPrefix string,
) error {
	return runStatementStreamMode(ctx, r, src, lang, errPrefix, true)
}

func runStatementStreamMode(
	ctx context.Context,
	r *interp.Runner,
	src []byte,
	lang syntax.LangVariant,
	errPrefix string,
	liveDialect bool,
) error {
	type hdocWarning struct {
		startLine int
		eofLine   int
		stop      string
	}
	var hdocWarnings []hdocWarning
	hdocWarn := func(startLine, eofLine int, stop string) {
		hdocWarnings = append(hdocWarnings, hdocWarning{startLine, eofLine, stop})
	}
	type comsubWarning struct {
		line  int
		count int
	}
	var comsubWarnings []comsubWarning
	comsubWarn := func(line, count int) {
		comsubWarnings = append(comsubWarnings, comsubWarning{line, count})
	}
	flushWarnings := func() {
		for _, warning := range comsubWarnings {
			plural := ""
			if warning.count > 1 {
				plural = "s"
			}
			fmt.Fprintf(os.Stderr,
				"%s: line %d: warning: command substitution: %d unterminated here-document%s\n",
				errPrefix, warning.line, warning.count, plural)
		}
		comsubWarnings = comsubWarnings[:0]
		for _, warning := range hdocWarnings {
			fmt.Fprintf(os.Stderr,
				"%s: line %d: warning: here-document at line %d delimited by end-of-file (wanted `%s')\n",
				errPrefix, warning.eofLine, warning.startLine, warning.stop)
		}
		hdocWarnings = hdocWarnings[:0]
	}
	var runErr error
	var retainedErr error
	cursor := 0
	for cursor < len(src) {
		parseLang := r.LangVariant()
		parser := syntax.NewParser(bashyParseOpts(parseLang, syntax.HeredocEOFWarning(hdocWarn),
			syntax.HeredocComsubWarning(comsubWarn))...)
		restart := false
		chunk := paddedChunk(src, cursor, len(src))
		padding := bytes.Count(src[:cursor], []byte("\n"))
		for stmt, err := range parser.StmtsSeq(bytes.NewReader(chunk)) {
			if stmt != nil {
				if !liveDialect && len(hdocWarnings) > 0 {
					if line, ok := incompleteCommandSubstitutionLine(stmt); ok {
						hdocWarnings = hdocWarnings[:0]
						fmt.Fprintf(os.Stderr, "%s: command substitution: line %d: unexpected EOF while looking for matching `)'\n", errPrefix, line)
						return interp.ExitStatus(2)
					}
				}
				flushWarnings()
				// The script's exit status is the LAST executed statement's,
				// like bash — always overwrite, so a successful command resets a
				// prior failure (e.g. `[[ 1 -le 0 ]]` then `true` exits 0). A
				// guarded `if err != nil` would leave the last *failing* status
				// stuck, diverging from bash for any stream-routed script.
				prevCursor := cursor
				runErr = r.Run(ctx, stmt)
				if runErr != nil && strings.Contains(runErr.Error(), "arithmetic syntax error") {
					retainedErr = runErr
				}
				if runErr != nil && invokedAsSh() {
					_ = r.Run(ctx, &syntax.File{})
					return runErr
				}
				if liveDialect {
					end := stmt.End().Offset()
					if stmt.Semicolon.IsValid() {
						end = stmt.Semicolon.Offset() + 1
					}
					cursor = cursor + int(end) - padding
					cursor = min(max(cursor, prevCursor+1), len(src))
				} else {
					cursor = stmtEndCursor(src, stmt)
				}
				if consumed := r.ConsumedSourceOffset(); consumed > prevCursor {
					cursor = consumed
				}
				if r.Exited() {
					if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
						runErr = err
					}
					return runErr
				}
				if r.LangVariant() != parseLang {
					restart = true
					break
				}
			}
			if err != nil {
				if pe, ok := err.(syntax.ParseError); ok && len(hdocWarnings) > 0 &&
					pe.Text == "unexpected EOF while looking for matching `)'" {
					if pe.Pos.Line() <= 1 {
						eofLine := hdocWarnings[len(hdocWarnings)-1].eofLine
						flushWarnings()
						fmt.Fprintf(os.Stderr, "%s: line %d: %s\n", errPrefix, eofLine+1, pe.Text)
						return interp.ExitStatus(2)
					}
					hdocWarnings = hdocWarnings[:0]
					fmt.Fprintf(os.Stderr, "%s: command substitution: line %d: %s\n", errPrefix, pe.Pos.Line()+1, pe.Text)
					fmt.Fprintln(os.Stdout)
					return interp.ExitStatus(2)
				}
				if pe, ok := bashRecoverableParseError(err); ok {
					errLine := int(pe.Pos.Line())
					if r.RunAliasExpandedSourceLine(ctx, errLine) {
						if consumed := r.ConsumedSourceOffset(); consumed > cursor {
							cursor = consumed
						} else {
							cursor = advancePastLine(src, errLine)
						}
						restart = true
						break
					}
					text := rewriteParserErrorText(string(src), pe)
					if strings.Contains(text, "arithmetic syntax error") {
						retainedErr = interp.ExitStatus(2)
					}
					if reportLine, resumeLine, ok := heredocBodyBadSubstRecovery(src, pe, text); ok {
						hdocWarnings = hdocWarnings[:0]
						fmt.Fprintf(os.Stderr, "%s: line %d: %s\n", errPrefix, reportLine, text)
						cursor = advancePastLine(src, resumeLine)
						r.SetLastExitStatus(1)
						runErr = interp.ExitStatus(2)
						restart = true
						break
					}
					suppressHdocWarning := len(hdocWarnings) > 0 &&
						int(pe.Pos.Line()) > hdocWarnings[len(hdocWarnings)-1].startLine
					if suppressHdocWarning && text == "unexpected EOF while looking for matching `)'" {
						fmt.Fprintf(os.Stderr, "%s: command substitution: line %d: %s\n",
							errPrefix, int(pe.Pos.Line())+1, text)
						fmt.Fprintln(os.Stdout)
						return interp.ExitStatus(2)
					}
					flushWarnings()
					if strings.HasSuffix(text, ": bad substitution") {
						errLine := int(pe.Pos.Line())
						start := lineStart(src, errLine)
						if start > cursor {
							prefix := paddedChunk(src, cursor, start)
							prefixParser := syntax.NewParser(bashyParseOpts(parseLang, syntax.HeredocEOFWarning(hdocWarn))...)
							for stmt, perr := range prefixParser.StmtsSeq(bytes.NewReader(prefix)) {
								if stmt != nil {
									if err := r.Run(ctx, stmt); err != nil {
										runErr = err
									}
								}
								if perr != nil {
									break
								}
							}
							cursor = start
						}
					}
					printBashParseError(os.Stderr, src, errPrefix, pe)
					if strings.HasPrefix(text, "${$") && strings.HasSuffix(text, ": bad substitution") {
						r.SetLastExitStatus(1)
						_ = r.Run(ctx, &syntax.File{})
						return interp.ExitStatus(2)
					}
					if fatalRecoveredParseError(src, pe) {
						return interp.ExitStatus(2)
					}
					newCursor := advancePastLine(src, int(pe.Pos.Line()))
					if newCursor <= cursor {
						return interp.ExitStatus(2)
					}
					cursor = newCursor
					r.SetLastExitStatus(1)
					runErr = interp.ExitStatus(2)
					restart = true
					break
				}
				return err
			}
		}
		if restart {
			continue
		}
		if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
			runErr = err
		}
		if liveDialect && retainedErr != nil {
			return retainedErr
		}
		return runErr
	}
	if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
		runErr = err
	}
	if liveDialect && retainedErr != nil {
		return retainedErr
	}
	return runErr
}

func incompleteCommandSubstitutionLine(stmt *syntax.Stmt) (int, bool) {
	line := 0
	syntax.Walk(stmt, func(node syntax.Node) bool {
		if node == nil {
			return false
		}
		if sub, ok := node.(*syntax.CmdSubst); ok && !sub.Right.IsValid() {
			line = int(sub.Left.Line()) + 1
			return false
		}
		return line == 0
	})
	return line, line > 0
}

// advancePastLine returns the byte offset just after the end of line
// `line` (1-based) in src, or len(src) if the line is the last one. A
// line is terminated by '\n'; the returned offset points to the byte
// after that '\n'.
func advancePastLine(src []byte, line int) int {
	current := 1
	for i, b := range src {
		if current == line && b == '\n' {
			return i + 1
		}
		if b == '\n' {
			current++
		}
	}
	return len(src)
}

func stmtEndCursor(src []byte, stmt *syntax.Stmt) int {
	if stmt == nil || !stmt.End().IsValid() {
		return 0
	}
	return advancePastLine(src, int(stmt.End().Line()))
}

func lineStart(src []byte, line int) int {
	if line <= 1 {
		return 0
	}
	current := 1
	for i, b := range src {
		if b == '\n' {
			current++
			if current == line {
				return i + 1
			}
		}
	}
	return len(src)
}

func heredocBodyBadSubstRecovery(src []byte, pe syntax.ParseError, text string) (reportLine, resumeLine int, ok bool) {
	if !strings.HasSuffix(text, ": bad substitution") {
		return 0, 0, false
	}
	line := int(pe.Pos.Line())
	if line <= 1 {
		return 0, 0, false
	}
	prev := strings.TrimSpace(nthLine(src, line-1))
	body := strings.TrimSpace(nthLine(src, line))
	next := strings.TrimSpace(nthLine(src, line+1))
	if body == "" || next == "" {
		return 0, 0, false
	}
	idx := strings.Index(prev, "<<")
	if idx < 0 {
		return 0, 0, false
	}
	rest := strings.TrimSpace(prev[idx+2:])
	if strings.HasPrefix(rest, "-") {
		rest = strings.TrimSpace(rest[1:])
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, 0, false
	}
	stop := strings.Trim(fields[0], `'"`)
	if stop == "" || next != stop {
		return 0, 0, false
	}
	return line - 1, line + 1, true
}

func paddedChunk(src []byte, start, end int) []byte {
	lineAtStart := bytes.Count(src[:start], []byte("\n")) + 1
	if lineAtStart <= 1 {
		return src[start:end]
	}
	chunk := make([]byte, lineAtStart-1+end-start)
	for i := 0; i < lineAtStart-1; i++ {
		chunk[i] = '\n'
	}
	copy(chunk[lineAtStart-1:], src[start:end])
	return chunk
}

func fatalRecoveredParseError(src []byte, pe syntax.ParseError) bool {
	if strings.HasSuffix(rewriteParserErrorText(string(src), pe), ": bad substitution") {
		return false
	}
	// A stray `)` where bash expects a command is a fatal bash parse error:
	// bash aborts the whole input (status 2) rather than recovering past it
	// and running the following lines. (Earlier-line statements still run;
	// only the input from this error onward is abandoned, matching bash.)
	if pe.Text == "`)` can only be used to close a subshell" {
		return true
	}
	if pe.Text == "a command can only contain words and redirects; encountered `)`" {
		return true
	}
	// An unclosed `{` command group at EOF is a fatal bash parse error (the whole
	// input fails), not one our statement recovery should continue past.
	if _, _, ok := braceGroupEOF(src, pe); ok {
		return true
	}
	if commandSubstOpenBefore(src, pe.Pos) {
		return true
	}
	return bytes.Contains(src, []byte("$(")) &&
		rewriteParserErrorText(string(src), pe) == "unexpected EOF while looking for matching `)'"
}

func bashRecoverableParseError(err error) (syntax.ParseError, bool) {
	var pe syntax.ParseError
	if errors.As(err, &pe) {
		return pe, true
	}
	var le syntax.LangError
	if errors.As(err, &le) && strings.Contains(le.Feature, "nested parameter expansions") {
		return syntax.ParseError{
			Filename: le.Filename,
			Pos:      le.Pos,
			Text:     le.Feature,
		}, true
	}
	if errors.As(err, &le) && strings.Contains(le.Feature, "`=(` process substitutions") {
		return syntax.ParseError{
			Filename: le.Filename,
			Pos:      le.Pos,
			Text:     le.Feature,
		}, true
	}
	return syntax.ParseError{}, false
}

func commandSubstOpenBefore(src []byte, pos syntax.Pos) bool {
	end := offsetBeforePos(src, pos)
	if end <= 0 {
		return false
	}
	depth := 0
	for i := 0; i < end; i++ {
		switch src[i] {
		case '$':
			if i+1 < end && src[i+1] == '(' {
				if i+2 < end && src[i+2] == '(' {
					continue
				}
				depth++
				i++
			}
		case ')':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}

func offsetBeforePos(src []byte, pos syntax.Pos) int {
	line, col := int(pos.Line()), int(pos.Col())
	if line <= 0 || col <= 0 {
		return 0
	}
	currentLine := 1
	lineStart := 0
	for i, b := range src {
		if currentLine == line {
			end := lineStart + col - 1
			if end > len(src) {
				return len(src)
			}
			return end
		}
		if b == '\n' {
			currentLine++
			lineStart = i + 1
		}
	}
	if currentLine == line {
		end := lineStart + col - 1
		if end > len(src) {
			return len(src)
		}
		return end
	}
	return len(src)
}

// printBashParseError emits a syntax.ParseError in the same shape bash
// 5.3 uses: a `<prefix>: line N: <text>` line, followed by a second
// `<prefix>: line N: \`<offending source line>'` echo. The prefix is
// `<file>` for a parsed script and `bashy: -c` for the -c form.
func printBashParseError(w io.Writer, src []byte, prefix string, pe syntax.ParseError) {
	line := int(pe.Pos.Line())
	if lines := arithForParseErrorLines(string(src), pe); len(lines) > 0 {
		for _, text := range lines {
			fmt.Fprintf(w, "%s: line %d: %s\n", prefix, line, text)
		}
		return
	}
	if text := arithmeticParseErrorText(string(src), pe); text != "" {
		fmt.Fprintf(w, "%s: line %d: %s\n", prefix, line, text)
		return
	}
	if lines, ok := incompleteParseErrorLines(src, pe); ok {
		for _, text := range lines {
			fmt.Fprintf(w, "%s: %s\n", prefix, text)
		}
		return
	}
	text := rewriteParserErrorText(string(src), pe)
	if eofLine, construct, ok := compoundEOFParseError(src, text); ok {
		fmt.Fprintf(w, "%s: line %d: syntax error: unexpected end of file from `%s' command on line 1\n", prefix, eofLine, construct)
		return
	}
	if openLine, eofLine, ok := braceGroupEOF(src, pe); ok {
		fmt.Fprintf(w, "%s: line %d: syntax error: unexpected end of file from `{' command on line %d\n", prefix, eofLine, openLine)
		return
	}
	if text == "unexpected EOF while looking for matching `)'" && strings.TrimSpace(nthLine(src, line)) == "math1)" {
		fmt.Fprintf(w, "%s: line %d: syntax error near unexpected token `)'\n", prefix, line)
		fmt.Fprintf(w, "%s: line %d: `math1)'\n", prefix, line)
		return
	}
	if strings.HasPrefix(text, "unexpected EOF") && line == 1 &&
		bytes.Contains(src, []byte("$(")) && bytes.Contains(src, []byte("<<")) {
		line = bytes.Count(src, []byte("\n")) + 1
	}
	// An unterminated `${`: bash blames the end of input, not the `$`.
	if text == "unexpected EOF while looking for matching `}'" &&
		strings.HasSuffix(strings.TrimRight(string(src), " \t\n"), "${") {
		line = eofReportLine(src)
	}
	fmt.Fprintf(w, "%s: line %d: %s\n", prefix, line, text)
	if strings.HasSuffix(text, ": bad substitution") {
		return
	}
	if strings.Contains(text, ": `") && strings.HasSuffix(text, "': not a valid identifier") {
		return
	}
	// Bash omits the trailing source-line echo for "unexpected EOF"
	// diagnostics (the matching-`X' messages already point at the
	// unclosed construct).
	if strings.HasPrefix(text, "unexpected EOF") {
		return
	}
	if srcLine := nthLine(src, line); srcLine != "" {
		fmt.Fprintf(w, "%s: line %d: `%s'\n", prefix, line, srcLine)
	}
}

func arithForParseErrorLines(src string, pe syntax.ParseError) []string {
	header, ok := arithForHeader(src)
	if !ok {
		return nil
	}
	switch {
	case pe.Text == "`expr` must be followed by `;`":
		return []string{
			"syntax error: arithmetic expression required",
			fmt.Sprintf("syntax error: `%s'", header),
		}
	case strings.Contains(pe.Text, "not a valid arithmetic operator: `;`"):
		return []string{
			"syntax error: `;' unexpected",
			fmt.Sprintf("syntax error: `%s'", header),
		}
	}
	return nil
}

// eofReportLine is the line bash blames for an end-of-input diagnostic:
// one past the last source line. `echo $(` on one line reports line 2.
func eofReportLine(src []byte) int {
	line := bytes.Count(src, []byte("\n")) + 1
	if strings.TrimSpace(nthLine(src, line)) != "" {
		line++
	}
	return line
}

// incompleteParseErrorLines renders the parse errors where our parser ran
// out of input (pe.Incomplete). Bash splits these three ways, and which one
// it picks depends on what the unfinished construct was still waiting for:
//
//	waiting for a command body   line <eof>: syntax error: unexpected end of
//	(then/do/{/( ... )           file from `<kw>' command on line <kw line>
//	waiting for a statement      line <eof>: syntax error: unexpected end of file
//	(after && or ||)
//	waiting for a word           line <n>: syntax error near unexpected token
//	(after < > >& << etc.)       `newline'   + the source-line echo
//
// The last shape names `newline' because at end of input bash's lexer hands
// the parser a newline token. Only the word case echoes the source line.
// Returns ok=false for anything not in this family, leaving the existing
// rewrites untouched.
func incompleteParseErrorLines(src []byte, pe syntax.ParseError) ([]string, bool) {
	if !pe.Incomplete {
		return nil, false
	}
	text := pe.Text

	// A subshell left open at EOF. `echo $(` carries the same message but
	// anchors pe.Pos at the `$`, so check that pe.Pos really is the `(`.
	if text == "unexpected EOF while looking for matching `)'" {
		off := offsetBeforePos(src, pe.Pos)
		// An unterminated array assignment (`a=( 1 2`) anchors at its `(`
		// too, but bash reports it plainly at the opening line rather than
		// as an unclosed command. The `=` before the paren tells them apart.
		if off > 0 && off < len(src) && src[off] == '(' && src[off-1] == '=' {
			return nil, false
		}
		if off >= 0 && off < len(src) && src[off] == '(' {
			return []string{fmt.Sprintf("line %d: syntax error: unexpected end of file from `(' command on line %d",
				eofReportLine(src), pe.Pos.Line())}, true
		}
		// Any other unterminated construct: bash reports it at the EOF
		// line rather than where the construct opened.
		return []string{fmt.Sprintf("line %d: %s", eofReportLine(src), text)}, true
	}

	// Waiting for a command body: name the outermost construct still open.
	if strings.HasSuffix(text, "must be followed by a statement list") {
		if kw, line, ok := outermostUnclosedConstruct(src); ok {
			return []string{fmt.Sprintf("line %d: syntax error: unexpected end of file from `%s' command on line %d",
				eofReportLine(src), kw, line)}, true
		}
		return []string{fmt.Sprintf("line %d: syntax error: unexpected end of file", eofReportLine(src))}, true
	}

	// Waiting for a statement after `&&` / `||` / `|`. Bash normally lets
	// this run to end of input, but a reserved word that cannot stand on
	// its own (`time |`, `! |`) makes the operator itself the error.
	if strings.HasSuffix(text, "must be followed by a statement") {
		off := offsetBeforePos(src, pe.Pos)
		if off > 0 && off <= len(src) {
			switch strings.TrimSpace(string(src[:off])) {
			case "time", "!":
				line := int(pe.Pos.Line())
				op := strings.TrimSuffix(strings.TrimPrefix(text, "`"), "` must be followed by a statement")
				out := []string{fmt.Sprintf("line %d: syntax error near unexpected token `%s'", line, op)}
				if srcLine := nthLine(src, line); srcLine != "" {
					out = append(out, fmt.Sprintf("line %d: `%s'", line, srcLine))
				}
				return out, true
			}
		}
		return []string{fmt.Sprintf("line %d: syntax error: unexpected end of file", eofReportLine(src))}, true
	}

	// Waiting for a word/name/literal.
	wordExpected := strings.HasSuffix(text, "must be followed by a word") ||
		strings.HasSuffix(text, "must be followed by a name") ||
		strings.HasSuffix(text, "must be followed by a literal") ||
		text == "coproc clause requires a command" ||
		text == "`foo(` must be followed by `)`"
	if wordExpected {
		line := int(pe.Pos.Line())
		out := []string{fmt.Sprintf("line %d: syntax error near unexpected token `newline'", line)}
		if srcLine := nthLine(src, line); srcLine != "" {
			out = append(out, fmt.Sprintf("line %d: `%s'", line, srcLine))
		}
		return out, true
	}
	return nil, false
}

// outermostUnclosedConstruct finds the first compound command left open at
// end of input — the one bash names in "unexpected end of file from `X'".
// For `for i in; do` that is `for`, not the `do` our parser stopped at, and
// for `f() {` it is the `{`. A word-level scan is enough here: this only
// runs once the input has already failed to parse, so the cost of a wrong
// guess is error wording, not behaviour.
func outermostUnclosedConstruct(src []byte) (keyword string, line int, ok bool) {
	type opener struct {
		kw   string
		line int
	}
	closes := map[string]string{
		"if": "fi", "while": "done", "until": "done", "for": "done",
		"select": "done", "case": "esac", "{": "}", "(": ")",
	}
	var stack []opener
	curLine := 1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			curLine++
			i++
			continue
		case c == ' ' || c == '\t' || c == ';' || c == '&' || c == '|':
			i++
			continue
		case c == '\'' || c == '"':
			quote := c
			i++
			for i < len(src) && src[i] != quote {
				if src[i] == '\n' {
					curLine++
				}
				i++
			}
			i++
			continue
		case c == '\\':
			i += 2
			continue
		case c == '(' || c == '{':
			stack = append(stack, opener{string(c), curLine})
			i++
			continue
		case c == ')' || c == '}':
			want := "("
			if c == '}' {
				want = "{"
			}
			if n := len(stack); n > 0 && stack[n-1].kw == want {
				stack = stack[:n-1]
			}
			i++
			continue
		}
		start := i
		for i < len(src) {
			b := src[i]
			if b == ' ' || b == '\t' || b == '\n' || b == ';' || b == '&' ||
				b == '|' || b == '(' || b == ')' || b == '\'' || b == '"' {
				break
			}
			i++
		}
		word := string(src[start:i])
		if _, isOpener := closes[word]; isOpener {
			stack = append(stack, opener{word, curLine})
			continue
		}
		if n := len(stack); n > 0 && closes[stack[n-1].kw] == word {
			stack = stack[:n-1]
		}
	}
	if len(stack) == 0 {
		return "", 0, false
	}
	return stack[0].kw, stack[0].line, true
}

func compoundEOFParseError(src []byte, text string) (eofLine int, construct string, ok bool) {
	for _, keyword := range []string{"if", "while", "until", "for", "case"} {
		if text == fmt.Sprintf("`%s` statement must end with `%s`", keyword, compoundEndKeyword(keyword)) {
			line := bytes.Count(src, []byte("\n")) + 1
			if strings.TrimSpace(nthLine(src, line)) != "" {
				line++
			}
			return line, keyword, true
		}
	}
	return 0, "", false
}

// braceGroupEOF reports whether pe is bash's fatal "unclosed `{` command group
// at end of file" parse error. Bash aborts the whole input (status 2) with
//
//	<prefix>: line <eofLine>: syntax error: unexpected end of file from `{' command on line <openLine>
//
// openLine is the line of the unterminated `{` (pe.Pos); eofLine is the line
// past the last source line (matching bash's report point). A `${...}`
// parameter-expansion EOF carries the same "matching `}'" text but anchors
// pe.Pos at the `$`, so checking the byte at pe.Pos excludes it.
func braceGroupEOF(src []byte, pe syntax.ParseError) (openLine, eofLine int, ok bool) {
	if pe.Text != "unexpected EOF while looking for matching `}'" {
		return 0, 0, false
	}
	off := offsetBeforePos(src, pe.Pos)
	if off < 0 || off >= len(src) || src[off] != '{' {
		return 0, 0, false
	}
	openLine = int(pe.Pos.Line())
	eofLine = bytes.Count(src, []byte("\n")) + 1
	if strings.TrimSpace(nthLine(src, eofLine)) != "" {
		eofLine++
	}
	return openLine, eofLine, true
}

// backtickComsubUnclosedQuote recognises a backtick command substitution whose
// body has an unterminated quote — mvdan/sh reports it as `reached "`" without
// closing quote `X` (a backtick closing the substitution hit while still
// inside quote X). bash treats this as a runtime command-substitution error:
// it prints the diagnostic, the substitution yields nothing, the enclosing
// command runs anyway, and the shell exits 0. This returns the source with the
// offending `...` span excised (so the remainder can run) plus bash's exact
// stderr line; ok=false for any other parse error.
func backtickComsubUnclosedQuote(src []byte, pe syntax.ParseError, shellName string) (stripped []byte, msg string, ok bool) {
	if !strings.HasPrefix(pe.Text, "reached") || !strings.Contains(pe.Text, "without closing quote") {
		return nil, "", false
	}
	// The "reached" token must be a backtick (shown as "`" in the message),
	// i.e. we hit the end of a backtick substitution mid-quote.
	if !strings.Contains(pe.Text, "\"\x60\"") {
		return nil, "", false
	}
	quote := unclosedQuoteChar(pe.Text)
	if quote == 0 {
		return nil, "", false
	}
	pos := offsetBeforePos(src, pe.Pos)
	if pos <= 0 || pos > len(src) {
		return nil, "", false
	}
	open := bytes.LastIndexByte(src[:pos], '`')
	if open < 0 {
		return nil, "", false
	}
	rel := bytes.IndexByte(src[pos:], '`')
	if rel < 0 {
		return nil, "", false
	}
	closeIdx := pos + rel
	// bash counts the error line from the start of the command substitution.
	line := bytes.Count(src[open:pos], []byte("\n")) + 1
	msg = fmt.Sprintf("%s: command substitution: line %d: unexpected EOF while looking for matching `%c'",
		shellName, line, quote)
	stripped = append(append([]byte{}, src[:open]...), src[closeIdx+1:]...)
	return stripped, msg, true
}

// unclosedQuoteChar pulls the unterminated quote char out of mvdan/sh's
// "… without closing quote `X`" message (the byte between the final pair of
// backticks). Returns 0 when the message doesn't match that shape.
func unclosedQuoteChar(text string) byte {
	i := strings.LastIndexByte(text, '`')
	if i < 1 {
		return 0
	}
	j := strings.LastIndexByte(text[:i], '`')
	if j < 0 || i-j != 2 {
		return 0
	}
	return text[j+1]
}

func compoundEndKeyword(keyword string) string {
	switch keyword {
	case "if":
		return "fi"
	case "case":
		return "esac"
	default:
		return "done"
	}
}

func arithForHeader(src string) (string, bool) {
	forIdx := strings.Index(src, "for")
	if forIdx < 0 {
		return "", false
	}
	open := strings.Index(src[forIdx:], "((")
	if open < 0 {
		return "", false
	}
	open += forIdx
	close := strings.Index(src[open+2:], "))")
	if close < 0 {
		return "", false
	}
	close += open + 2
	return src[open : close+2], true
}

// rewriteParserErrorText rewrites mvdan/sh's parser error messages
// into bash 5.3's canonical wording when the pattern is recognisable.
// Falls back to the original text otherwise.
func rewriteParserErrorText(src string, pe syntax.ParseError) string {
	if text, ok := declareInvalidIdentifierText(src, pe); ok {
		return text
	}
	// An unterminated array subscript running off the end of the input
	// (`a[`, `a[5 + `): our parser reports "`[` must be followed by an
	// expression" (an incomplete parse), but bash reports the matching-`]'
	// EOF wording and omits the source-line echo. Mirror bash.
	if pe.Incomplete && pe.Text == "`[` must be followed by an expression" {
		return "unexpected EOF while looking for matching `]'"
	}
	// A stray `)` where bash expects a command: our parser reports "`)` can
	// only be used to close a subshell"; bash reports it as a generic
	// unexpected-token error (and aborts the whole input — see
	// fatalRecoveredParseError).
	if pe.Text == "`)` can only be used to close a subshell" {
		return "syntax error near unexpected token `)'"
	}
	if strings.Contains(pe.Text, "nested parameter expansions") {
		if subst := nestedBadSubstSource(src, pe.Pos); subst != "" {
			subst = strings.ReplaceAll(subst, "$'", "'")
			return subst + ": bad substitution"
		}
		return "bad substitution"
	}
	// An arithmetic `for` header closed with a single `)` instead of `))`
	// (`for((;;)`). Our parser reports the unmatched `((`; bash names the
	// offending tail, which runs from the last `;` in the header to the
	// stray `)` -- both `for((;;)` and `for((i=0;i<3;)` report `;)'.
	if strings.HasPrefix(pe.Text, "reached `)` without matching `((`") {
		if i := strings.LastIndexByte(src, ';'); i >= 0 {
			if j := strings.IndexByte(src[i:], ')'); j >= 0 {
				return fmt.Sprintf("syntax error near `%s'", src[i:i+j+1])
			}
		}
	}
	// `X` can only immediately follow a statement -- bash reports the
	// stray token generically (`echo a; ;`).
	if strings.HasSuffix(pe.Text, "` can only immediately follow a statement") {
		tok := strings.TrimSuffix(strings.TrimPrefix(pe.Text, "`"), "` can only immediately follow a statement")
		return fmt.Sprintf("syntax error near unexpected token `%s'", tok)
	}
	// A `${` running off the end of the input: our parser calls the empty
	// name invalid, bash reports the unmatched brace.
	if pe.Text == "invalid parameter name" && strings.HasSuffix(strings.TrimRight(src, " \t\n"), "${") {
		return "unexpected EOF while looking for matching `}'"
	}
	if pe.Text == "invalid parameter name" {
		if subst := nestedBadSubstSource(src, pe.Pos); subst != "" && subst != "${" {
			subst = strings.ReplaceAll(subst, "$'", "'")
			return subst + ": bad substitution"
		}
	}
	if strings.Contains(pe.Text, "cannot be followed by a word") {
		if subst := nestedBadSubstSource(src, pe.Pos); subst != "" {
			subst = strings.ReplaceAll(subst, "$'", "'")
			return subst + ": bad substitution"
		}
	}
	// Bash escalates a partial-arithmetic parse to "missing `))`"
	// instead of naming the inner token — match that for `((` blocks
	// before any of the per-message rewrites below.
	if text := arithmeticParseErrorText(src, pe); text != "" {
		return text
	}
	if insideUnclosedArith(src, pe.Pos) {
		return "unexpected EOF while looking for matching `)'"
	}
	if commandSubstOpenBefore([]byte(src), pe.Pos) &&
		strings.Contains(pe.Text, "statement must end with") {
		return "syntax error near unexpected token `)'"
	}
	if commandSubstOpenBefore([]byte(src), pe.Pos) &&
		strings.Contains(pe.Text, "must be followed by a statement list") {
		return "syntax error near unexpected token `)'"
	}
	if commandSubstOpenBefore([]byte(src), pe.Pos) &&
		strings.Contains(pe.Text, "must be followed by `do`") {
		return "syntax error near unexpected token `done' while looking for matching `)'"
	}
	if commandSubstOpenBefore([]byte(src), pe.Pos) || strings.Contains(src, "$(") {
		switch {
		case strings.Contains(pe.Text, "`done` can only"):
			return "syntax error near unexpected token `done' while looking for matching `)'"
		case strings.Contains(pe.Text, "`esac` can only"):
			return "syntax error near unexpected token `esac' while looking for matching `)'"
		case strings.Contains(pe.Text, "`;;` can only"),
			strings.Contains(pe.Text, "`in` can only"):
			return "syntax error near unexpected token `in' while looking for matching `)'"
		}
	}
	// A reserved word used as a command (`in`, `do`, `fi`, ...). Our parser
	// explains which construct owns it; bash just names the token. This has
	// to stay below the command-substitution cases above, which report the
	// same keywords with the "while looking for matching `)'" suffix.
	if kw, ok := reservedWordOnlyError(pe.Text); ok {
		return fmt.Sprintf("syntax error near unexpected token `%s'", kw)
	}
	switch {
	case pe.Text == "statements must be separated by &, ; or a newline",
		pe.Text == "array element values must be words",
		strings.Contains(pe.Text, "a command can only contain words and redirects"),
		strings.Contains(pe.Text, "`=(` process substitutions"),
		strings.Contains(pe.Text, "must be followed by"),
		strings.Contains(pe.Text, "must follow a name"):
		if strings.Contains(pe.Text, "`=(` process substitutions") {
			return "syntax error near unexpected token `('"
		}
		if pe.Text == "array element values must be words" {
			if tok := arrayElementErrorTokenAt(src, pe.Pos); tok != "" {
				return fmt.Sprintf("syntax error near unexpected token `%s'", tok)
			}
		}
		// For `case`/`for`/`select` follow-errors the parser anchors the
		// position at the keyword itself ("`case x` must be followed by
		// `in`") but bash reports the actually-offending token (the one
		// it found instead). Skip over the construct's preamble to find
		// that token in the source.
		skipWords := tokensToSkip(pe.Text)
		if tok := offendingTokenAfter(src, pe.Pos, skipWords); tok != "" {
			return fmt.Sprintf("syntax error near unexpected token `%s'", tok)
		}
		if tok := offendingTokenAt(src, pe.Pos); tok != "" {
			return fmt.Sprintf("syntax error near unexpected token `%s'", tok)
		}
	case strings.HasPrefix(pe.Text, "reached EOF without matching"):
		// Map our `${`/`$(`/`{` matching-error wording to bash's.
		if strings.Contains(pe.Text, "`$(`") || strings.Contains(pe.Text, "`(`") {
			return "unexpected EOF while looking for matching `)'"
		}
		if strings.Contains(pe.Text, "`${`") || strings.Contains(pe.Text, "`{`") {
			return "unexpected EOF while looking for matching `}'"
		}
	case pe.Text == "unclosed quote":
		return "unexpected EOF while looking for matching `\"'"
	}
	return pe.Text
}

func declareInvalidIdentifierText(src string, pe syntax.ParseError) (string, bool) {
	if pe.Text != "invalid var name" {
		return "", false
	}
	line := strings.TrimSpace(nthLine([]byte(src), int(pe.Pos.Line())))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	name := fields[0]
	switch name {
	case "declare", "export", "local", "readonly", "typeset":
	default:
		return "", false
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		return fmt.Sprintf("%s: `%s': not a valid identifier", name, field), true
	}
	return "", false
}

func arrayElementErrorTokenAt(src string, pos syntax.Pos) string {
	off := offsetBeforePos([]byte(src), pos)
	if off < 0 || off >= len(src) {
		return ""
	}
	rest := src[off:]
	if strings.HasPrefix(rest, "<>") {
		return "<>"
	}
	return ""
}

func arithmeticParseErrorText(src string, pe syntax.ParseError) string {
	if strings.Contains(pe.Text, "`~` must be followed by an expression") {
		line := strings.TrimSpace(nthLine([]byte(src), int(pe.Pos.Line())))
		expr := "~"
		if strings.Contains(line, "$(( ~ ))") || strings.Contains(line, "(( ~ ))") {
			expr = "~ "
		}
		return fmt.Sprintf("%s: arithmetic syntax error: operand expected (error token is %q)", expr, expr)
	}
	if !strings.Contains(pe.Text, "not a valid arithmetic operator:") {
		return ""
	}
	line := strings.TrimSpace(nthLine([]byte(src), int(pe.Pos.Line())))
	if line == "" {
		return ""
	}
	expr, command, spaced := arithmeticSourceExpr(line)
	if expr == "" {
		return ""
	}
	token := parseErrorBacktickToken(pe.Text)
	if token == "" {
		return ""
	}
	errToken := token
	if idx := strings.Index(expr, token); idx >= 0 {
		errToken = strings.TrimLeft(expr[idx:], " \t")
		if strings.ContainsAny(errToken, " \t") {
			errToken = strings.TrimRight(errToken, " \t") + " "
		} else if strings.Contains(errToken, "=") {
			errToken += " "
		}
	}
	if command {
		sep := ":"
		if spaced {
			sep = " :"
		}
		return fmt.Sprintf("((: %s%s arithmetic syntax error in expression (error token is %q)", expr, sep, errToken)
	}
	return fmt.Sprintf("%s: arithmetic syntax error in expression (error token is %q)", expr, errToken)
}

func arithmeticSourceExpr(line string) (expr string, command, spaced bool) {
	if strings.HasPrefix(line, "((") {
		if end := strings.LastIndex(line, "))"); end >= 0 {
			raw := line[2:end]
			return strings.TrimSpace(raw), true, strings.TrimRight(raw, " \t") != raw
		}
	}
	if start := strings.Index(line, "$(("); start >= 0 {
		if end := strings.LastIndex(line[start+3:], "))"); end >= 0 {
			return strings.TrimSpace(line[start+3 : start+3+end]), false, false
		}
	}
	if eq := strings.IndexByte(line, '='); eq > 0 {
		prefix := line[:eq]
		if open := strings.IndexByte(prefix, '['); open > 0 && strings.HasSuffix(prefix, "]") {
			return strings.TrimSpace(prefix[open+1 : len(prefix)-1]), false, false
		}
	}
	return "", false, false
}

func parseErrorBacktickToken(text string) string {
	start := strings.LastIndex(text, "`")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(text[:start], "`")
	if end < 0 {
		return ""
	}
	return text[end+1 : start]
}

func nestedBadSubstSource(src string, pos syntax.Pos) string {
	off := offsetBeforePos([]byte(src), pos)
	if off <= 0 || off > len(src) {
		return ""
	}
	start := strings.LastIndex(src[:off], "${")
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '$':
			if i+1 < len(src) && src[i+1] == '{' {
				depth++
				i++
			}
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					return src[start : i+1]
				}
			}
		case '\n':
			return ""
		}
	}
	return ""
}

// insideUnclosedArith reports whether pos sits inside an `(( ... ))`
// arithmetic command whose matching `))` is missing in the source up
// to that point.
func insideUnclosedArith(src string, pos syntax.Pos) bool {
	col := int(pos.Col())
	line := int(pos.Line())
	if line <= 0 || col <= 0 {
		return false
	}
	curLine := 1
	end := 0
	for ; end < len(src) && curLine < line; end++ {
		if src[end] == '\n' {
			curLine++
		}
	}
	end += col - 1
	if end > len(src) {
		end = len(src)
	}
	prefix := src[:end]
	// Count `((` and `))` occurrences before pos. If `((` > `))` we
	// are inside an unclosed arith block. This is conservative — it
	// ignores `((` inside strings/comments — but good enough for the
	// bashy CLI's error-message remap.
	open := strings.Count(prefix, "((")
	close := strings.Count(prefix, "))")
	return open > close
}

// unexpectedTokenAbort recognises the parse errors bash 5.3 reports as a fatal
// "syntax error near unexpected token `X'" and aborts the whole input on
// (status 2) — the up-front sibling of syntax.DbracketParseError, for the
// non-`[[ ]]` shapes. It renders the two-line bash diagnostic:
//
//	<prefix>: line N: syntax error near unexpected token `X'
//	<prefix>: line N: `<offending source line>'
//
// Returns ok=false for any other ParseError so the recovery path is unchanged.
func unexpectedTokenAbort(err error, src []byte, prefix string) (string, bool) {
	var pe syntax.ParseError
	if !errors.As(err, &pe) {
		return "", false
	}
	token, ok := unexpectedTokenName(string(src), pe)
	if !ok {
		return "", false
	}
	line := int(pe.Pos.Line())
	// bash reads-parses-executes one complete command (terminated by a newline)
	// at a time, so any complete statement on an EARLIER line runs before it
	// reaches — and aborts on — the offending one. Aborting the whole input up
	// front would wrongly skip that output, so only fire when nothing executable
	// precedes the error line. (Clauses on the SAME line as the error are part
	// of the failing parse unit and never run, exactly as in bash.)
	if completeStmtBeforeLine(src, line) {
		return "", false
	}
	msg := fmt.Sprintf("%s: line %d: syntax error near unexpected token `%s'", prefix, line, token)
	if srcLine := nthLine(src, line); srcLine != "" {
		msg += fmt.Sprintf("\n%s: line %d: `%s'", prefix, line, srcLine)
	}
	return msg, true
}

// unexpectedTokenName returns the token bash names in its "unexpected token"
// diagnostic for the parse errors unexpectedTokenAbort handles, or ok=false
// when pe is some other error. Two families:
//
//   - a `case` with no subject word (`case` / `case x` followed by a non-word):
//     bash names the token it found instead — a `newline` for `case<newline>`;
//   - a loop/`if`/`case` reserved word (`do`, `done`, `then`, `elif`, `fi`,
//     `esac`, `;;`) used outside the construct it belongs to: bash names the
//     reserved word itself.
//
// Inside an open command substitution bash reports the matching-`)' variant
// instead, so those are declined here and left to the recovery path.
func unexpectedTokenName(src string, pe syntax.ParseError) (string, bool) {
	text := pe.Text
	if kw, ok := reservedWordOnlyError(text); ok {
		if commandSubstOpenBefore([]byte(src), pe.Pos) {
			return "", false
		}
		// done/esac/;; have a recovery-path rewrite to the matching-`)' wording
		// whenever the source merely contains a `$(`; leave those to it so this
		// abort doesn't clash with established behavior.
		switch kw {
		case "done", "esac", "in", ";;":
			if strings.Contains(src, "$(") {
				return "", false
			}
		}
		return kw, true
	}
	if text == "`case` must be followed by a word" ||
		(strings.HasPrefix(text, "`case ") && strings.Contains(text, "must be followed by `in`")) {
		tok := offendingTokenAfter(src, pe.Pos, tokensToSkip(text))
		if tok == "" {
			tok = "newline"
		}
		return tok, true
	}
	return "", false
}

// completeStmtBeforeLine reports whether src has a complete statement that ends
// on a line strictly before errLine — i.e. a command bash would have run before
// reaching the offending line. It parses up to the first parse error (the
// statement stream stops there) and checks the statements yielded along the way.
func completeStmtBeforeLine(src []byte, errLine int) bool {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	for stmt, err := range parser.StmtsSeq(bytes.NewReader(src)) {
		if stmt != nil && int(stmt.End().Line()) < errLine {
			return true
		}
		if err != nil {
			break
		}
	}
	return false
}

// reservedWordOnlyError reports whether text is one of the engine's
// "%#q can only be used …" diagnostics for a misplaced loop/`if`/`case`
// reserved word, returning the bare keyword (without the `%#q` backticks).
func reservedWordOnlyError(text string) (string, bool) {
	for _, kw := range []string{"do", "done", "then", "elif", "fi", "esac", "in", ";;"} {
		if strings.HasPrefix(text, "`"+kw+"` can only be used ") {
			return kw, true
		}
	}
	return "", false
}

// tokensToSkip returns how many words must be skipped past pe.Pos in
// the source to land on the token bash would name as the offender.
// For `case x must be followed by in` we must skip 2 words (`case`
// and the subject); for `for must be followed by a literal` only 1
// (`for`); etc. Returns 0 when no special skipping is needed.
func tokensToSkip(text string) int {
	switch {
	case strings.HasPrefix(text, "`case ") && strings.Contains(text, "must be followed by `in`"):
		return 2
	case strings.HasPrefix(text, "`case` must be followed by"):
		return 1
	case strings.HasPrefix(text, "`for` must be followed by"),
		strings.HasPrefix(text, "`select` must be followed by"):
		return 1
	case strings.Contains(text, "` must be followed by `in`, `do`, `;`, or a newline"):
		// `for foo` / `select foo` -- skip kw + name.
		return 2
	case strings.HasPrefix(text, "`if` must be followed by"),
		strings.HasPrefix(text, "`while` must be followed by"),
		strings.HasPrefix(text, "`until` must be followed by"),
		strings.HasPrefix(text, "`then` must be followed by"),
		strings.HasPrefix(text, "`do` must be followed by"),
		strings.HasPrefix(text, "`{` must be followed by"):
		// Our parser anchors at the keyword ("`if` must be followed by a
		// statement list") but bash names the token it actually tripped
		// on -- the `;` in `if; then; fi`. Skip the keyword to reach it.
		return 1
	}
	return 0
}

// offendingTokenAfter advances `skip` whitespace-delimited words past
// pos in src and returns the next token starting after the last skip,
// in the same shape as offendingTokenAt. Used to find bash's notion of
// the offender when the parser anchored its position at the start of
// the construct (`case x`, `for`, `for foo`, …).
func offendingTokenAfter(src string, pos syntax.Pos, skip int) string {
	if skip <= 0 {
		return ""
	}
	col := int(pos.Col())
	line := int(pos.Line())
	if line <= 0 || col <= 0 {
		return ""
	}
	curLine := 1
	i := 0
	for ; i < len(src) && curLine < line; i++ {
		if src[i] == '\n' {
			curLine++
		}
	}
	i += col - 1
	if i >= len(src) {
		return ""
	}
	skipWord := func() {
		// skip leading whitespace
		for ; i < len(src); i++ {
			c := src[i]
			if c != ' ' && c != '\t' {
				break
			}
		}
		// consume one bash-style word/operator
		if i >= len(src) {
			return
		}
		switch src[i] {
		case ')', '(', '|', '&', ';', '<', '>', '`':
			i++
			return
		}
		for ; i < len(src); i++ {
			c := src[i]
			if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '<' || c == '>' || c == '(' || c == ')' || c == '`' {
				break
			}
		}
	}
	for n := 0; n < skip; n++ {
		skipWord()
	}
	// skip whitespace before the offender
	for ; i < len(src); i++ {
		c := src[i]
		if c != ' ' && c != '\t' {
			break
		}
	}
	if i >= len(src) {
		return ""
	}
	switch src[i] {
	case ')', '(', '|', '&', ';', '<', '>', '`':
		return string(src[i])
	}
	start := i
	for ; i < len(src); i++ {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '<' || c == '>' || c == '(' || c == ')' || c == '`' {
			break
		}
	}
	return src[start:i]
}

// offendingTokenAt extracts a single bash-style token (operator or
// word) starting at the given position in src. Used by the parser-
// error rewriter to fill in `… unexpected token \`X' …`.
func offendingTokenAt(src string, pos syntax.Pos) string {
	col := int(pos.Col())
	line := int(pos.Line())
	if line <= 0 || col <= 0 {
		return ""
	}
	curLine := 1
	i := 0
	for ; i < len(src) && curLine < line; i++ {
		if src[i] == '\n' {
			curLine++
		}
	}
	i += col - 1
	if i >= len(src) {
		return ""
	}
	switch src[i] {
	case ')', '(', '|', '&', ';', '<', '>', '`':
		return string(src[i])
	}
	start := i
	for ; i < len(src); i++ {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '<' || c == '>' || c == '(' || c == ')' || c == '`' {
			break
		}
	}
	return src[start:i]
}

// nthLine returns the 1-indexed line `n` of src with the trailing
// newline stripped, or "" when n is out of range.
func nthLine(src []byte, n int) string {
	if n <= 0 {
		return ""
	}
	cur := 1
	start := 0
	for i := range len(src) {
		if src[i] == '\n' {
			if cur == n {
				return string(src[start:i])
			}
			cur++
			start = i + 1
		}
	}
	if cur == n {
		return string(src[start:])
	}
	return ""
}

// runPath opens and executes a script file, mirroring bash's
// open_shell_script (shell.c). Diagnostics and exit codes match bash:
//   - missing file  -> "<argv0>: <name>: No such file or directory" (127)
//   - a directory   -> "<script>: <script>: Is a directory"          (126)
//   - a binary file  -> "<script>: <script>: cannot execute binary file" (126)
//
// Before the file is opened the error prefix is the shell name (argv0);
// once opened, $0 becomes the script path and is used as the prefix.
func runPath(r *interp.Runner, path string) error {
	shellName := os.Args[0]
	orig := path
	// The operand is in the shell's spelling — on Windows that is the MSYS
	// drive form bashy itself hands out for $HOME, $TEMP and pwd (/c/Users/…),
	// which the OS cannot open as given. Join relative operands to the
	// runner's cwd too: ShellPathToOS only converts their spelling and would
	// leave os.Open resolving them against the embedding process's cwd.
	f, err := os.Open(pathconv.JoinAbs(r.Dir, path))
	if err != nil {
		// Bash falls back to a $PATH search for a bare (no-slash) name.
		if !strings.Contains(path, "/") {
			if resolved, lerr := interp.LookPathDir(r.Dir, r.Env, path); lerr == nil {
				path = resolved
				// LookPathDir returns the shell's spelling. A child Bashy process
				// keeps a POSIX PATH (for example /bin:/usr/bin), so a bare
				// script operand may resolve to /bin/ls on Windows. Open through
				// the same mount conversion as the original operand.
				f, err = os.Open(pathconv.JoinAbs(r.Dir, path))
			}
		}
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "%s: %s: No such file or directory\n", shellName, orig)
				return interp.ExitStatus(127)
			}
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", shellName, orig, fileErrorReason(err))
			return interp.ExitStatus(126)
		}
	}
	defer f.Close()
	// $0 is now the script path; bash reports the remaining errors with it.
	if info, serr := f.Stat(); serr == nil && info.IsDir() {
		fmt.Fprintf(os.Stderr, "%s: %s: Is a directory\n", path, path)
		return interp.ExitStatus(126)
	}
	sample := make([]byte, 80)
	n, _ := f.Read(sample)
	if checkBinaryFileSample(sample[:n]) {
		fmt.Fprintf(os.Stderr, "%s: %s: cannot execute binary file\n", path, path)
		return interp.ExitStatus(126)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return run(r, f, path)
}

// checkBinaryFileSample mirrors bash's check_binary_file (general.c).
func checkBinaryFileSample(sample []byte) bool {
	if len(sample) >= 4 && sample[0] == 0x7f && sample[1] == 'E' && sample[2] == 'L' && sample[3] == 'F' {
		return true
	}
	nline := 1
	if len(sample) >= 2 && sample[0] == '#' && sample[1] == '!' {
		nline = 2
	}
	for _, c := range sample {
		if c == '\n' {
			nline--
			if nline == 0 {
				return false
			}
		}
		if c == 0 {
			return true
		}
	}
	return false
}

// fileErrorReason renders a non-ENOENT open error the way bash's
// file_error does: the bare strerror text (e.g. "Permission denied").
func fileErrorReason(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		msg := errno.Error()
		if len(msg) > 0 {
			return strings.ToUpper(msg[:1]) + msg[1:]
		}
	}
	return err.Error()
}
