// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/qiangli/bash/execbudget"
	"github.com/qiangli/bash/pathconv"
	"github.com/qiangli/bash/winmode"
)

// HandlerCtx returns the [HandlerContext] value stored in ctx,
// which is used when calling handler functions.
// It panics if ctx has no HandlerContext stored.
func HandlerCtx(ctx context.Context) HandlerContext {
	hc, ok := ctx.Value(handlerCtxKey{}).(HandlerContext)
	if !ok {
		panic("interp.HandlerCtx: no HandlerContext in ctx")
	}
	return hc
}

type handlerCtxKey struct{}

// devNetworkRedirectCtxKey marks an open made for a shell redirection. Bash's
// /dev/tcp and /dev/udp pseudo-paths are redirection-only; in particular,
// `source /dev/tcp/...` remains an ordinary file open. Keeping this marker on
// the context also lets custom OpenHandlers retain their normal authority over
// the operation while DefaultOpenHandler provides Bash's default behavior.
type devNetworkRedirectCtxKey struct{}

// execReplacingCtxKey marks the one handler invocation produced by an exec
// builtin. A bgProc-wide flag is insufficient for pipelines: a concurrently
// running non-exec component must not overwrite the replacement's result.
type execReplacingCtxKey struct{}

// execStartOverrideCtxKey is an internal test seam for observing the command
// selected by the exec path without creating a Windows process.
type execStartOverrideCtxKey struct{}

func (r *Runner) devNetworkRedirectsEnabled() bool {
	switch r.Dialect() {
	case syntax.LangBash, syntax.LangBashPP, syntax.LangBats:
		return true
	default:
		return false
	}
}

// standardUtilsPath is the default search path used by `command -p` to
// find the POSIX standard utilities, mirroring bash's STANDARD_UTILS_PATH
// (config-top.h) when confstr(_CS_PATH) is unavailable. Bashy owns the
// leading /opt/bashy/bin segment: its bundled coreutils resolve before any
// host-provided binaries of the same name, giving `command -p` a stable,
// platform-portable result regardless of what the host distro ships in
// /bin, /usr/bin, /sbin, or /usr/sbin. It is a var rather than a const
// solely so tests can point it at a fixture directory instead of the real
// path.
var standardUtilsPath = "/opt/bashy/bin:/bin:/usr/bin:/sbin:/usr/sbin"

type handlerKind int

const (
	_                  handlerKind = iota
	handlerKindExec                // [ExecHandlerFunc]
	handlerKindCall                // [CallHandlerFunc]
	handlerKindOpen                // [OpenHandlerFunc]
	handlerKindReadDir             // [ReadDirHandlerFunc2]
)

// HandlerContext is the data passed to all the handler functions via [context.WithValue].
// It contains some of the current state of the [Runner].
type HandlerContext struct {
	// Agentic permits cooperating tools to use explicitly requested Bash++
	// assistance. It grants no provider, network, or spending permissions.
	Agentic bool
	runner  *Runner // for internal use only, e.g. [HandlerContext.Builtin]

	// kind records which type of handler this context was built for.
	kind handlerKind

	// Env is a read-only version of the interpreter's environment,
	// including environment variables, global variables, and local function
	// variables.
	Env expand.Environ

	// Dir is the interpreter's current directory.
	Dir string

	// Pos is the source position which relates to the operation,
	// such as a [syntax.CallExpr] when calling an [ExecHandlerFunc].
	// It may be invalid if the operation has no relevant position information.
	Pos syntax.Pos

	// TODO(v4): use an os.File for stdin below directly.

	// Stdin is the interpreter's current standard input reader.
	// It is always an [*os.File], but the type here remains an [io.Reader]
	// due to backwards compatibility.
	Stdin io.Reader
	// Stdout is the interpreter's current standard output writer.
	Stdout io.Writer
	// Stderr is the interpreter's current standard error writer.
	Stderr io.Writer

	// ExecAs is the argv[0] override requested via "exec -a NAME CMD".
	// It is empty for all other calls. Handlers that launch a real
	// process should propagate it as the first element of the spawned
	// argv, leaving the executable lookup in args[0] unchanged. The
	// [DefaultExecHandler] honours this field; custom handlers may
	// ignore it.
	ExecAs string

	// ExecClearEnv is set for "exec -c", requesting that the spawned
	// process starts with an empty environment.
	ExecClearEnv bool

	// ExecReplace is set for the exec builtin, requesting that the default
	// handler replace the current process rather than fork and wait.
	ExecReplace bool
}

// DryRun reports whether the runner's non-POSIX `dryrun` option is currently on
// (see [EnableDryRunOption]). Exec and open handlers read it to report-and-skip
// instead of executing/mutating. It is always false unless a host enabled the
// option, and tracks `set -o dryrun` / `set +o dryrun` at runtime.
func (hc HandlerContext) DryRun() bool {
	return hc.runner != nil && hc.runner.dryRun
}

// Async reports whether the handler was invoked from within an asynchronous
// list (`cmd &`), including subshells nested inside one. Such a command runs
// concurrently with the shell's foreground statements and may outlive the
// script, so middleware that brackets a command's output (buffering stdout
// until the handler returns) must not apply that window to it: the
// foreground's later writes would be held until the background job exits,
// and lost entirely if the shell exits first.
func (hc HandlerContext) Async() bool {
	return hc.runner != nil && hc.runner.asyncList
}

// Umask reports the Runner's current virtual file-creation mask. Custom
// in-process command handlers use this value to apply the same permissions an
// external command would inherit at the fork boundary, without changing the
// process-wide umask shared by other Runners.
func (hc HandlerContext) Umask() os.FileMode {
	if hc.runner == nil {
		return 0
	}
	return os.FileMode(hc.runner.umask)
}

// SignalIgnored reports whether the named signal currently has the ignored
// disposition — either set via an empty "trap SIG" action, or inherited
// as startup-ignored (see [Runner.isStartupIgnored]) — mirroring
// [Runner.carrierSignalDisposition]. It returns false for:
//   - a nil HandlerContext.runner;
//   - an unrecognized name, including the pseudo-signals EXIT/ERR/DEBUG/
//     RETURN, which normalize but have no real signal disposition;
//   - KILL and STOP, which are always uncatchable regardless of trap state;
//   - the default disposition (no trap installed) and a caught disposition
//     (a non-empty trap handler).
//
// The name accepts bare ("PIPE"), SIG-prefixed ("SIGPIPE"), and numeric
// specs, normalized through the same logic the trap builtin uses. No
// mutable trap state is exposed; the trapCallbacks read is guarded by
// sigMu, matching the existing pattern in carrier.go.
//
// ExecHandlers use this to decide whether to propagate SIG_IGN to a spawned
// child process: an ignored disposition is inherited across execve, while a
// caught disposition is reset to the default.
func (hc HandlerContext) SignalIgnored(name string) bool {
	if hc.runner == nil {
		return false
	}
	sig := normalizeSignal(name)
	if sig == "" {
		return false
	}
	if _, ok := signalByName(sig); !ok {
		return false
	}
	if sig == "KILL" || sig == "STOP" {
		return false
	}
	if hc.runner.isStartupIgnored(sig) {
		return true
	}
	hc.runner.sigMu.Lock()
	defer hc.runner.sigMu.Unlock()
	cb, ok := hc.runner.trapCallbacks[sig]
	return ok && cb == ""
}

// CallHandlerFunc is a handler which runs on every [syntax.CallExpr].
// It is called once variable assignments and field expansion have occurred.
// The context includes a [HandlerContext] value.
//
// The call's arguments are replaced by what the handler returns,
// and then the call is executed by the Runner as usual.
// The args slice is never empty.
// At this time, returning an empty slice without an error is not supported.
//
// This handler is similar to [ExecHandlerFunc], but has two major differences:
//
// First, it runs for all simple commands, including function calls and builtins.
//
// Second, it is not expected to execute the simple command, but instead to
// allow running custom code which allows replacing the argument list.
// Shell builtins touch on many internals of the Runner, after all.
//
// Returning a non-nil error will halt the [Runner] and will be returned via the API.
type CallHandlerFunc func(ctx context.Context, args []string) ([]string, error)

// TODO: consistently treat handler errors as non-fatal by default,
// but have an interface or API to specify fatal errors which should make
// the shell exit with a particular status code.

// ExecHandlerFunc is a handler which executes simple commands.
// It is called for all [syntax.CallExpr] nodes
// where the first argument is neither a declared function nor a builtin.
// The args slice is never empty.
// The context includes a [HandlerContext] value.
//
// Returning a nil error means a zero exit status.
// Other exit statuses can be set by returning or wrapping a [NewExitStatus] error,
// and such an error is returned via the API if it is the last statement executed.
// Any other error will halt the [Runner] and will be returned via the API.
type ExecHandlerFunc func(ctx context.Context, args []string) error

// DefaultExecHandler returns the [ExecHandlerFunc] used by default.
// It finds binaries in PATH and executes them.
// When context is cancelled, an interrupt signal is sent to running processes.
// killTimeout is a duration to wait before sending the kill signal.
// A negative value means that a kill signal will be sent immediately.
//
// On Windows, the kill signal is always sent immediately,
// because Go doesn't currently support sending Interrupt on Windows.
// [Runner] defaults to a killTimeout of 2 seconds.
func DefaultExecHandler(killTimeout time.Duration) ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := HandlerCtx(ctx)
		execDir := shellPathToOS(hc.Dir, hc.Dir)
		lookupDir := hc.Dir
		if hc.runner != nil {
			hc.runner.ensureDirFile(execDir)
			execDir = runnerExecDir(hc.runner, execDir)
			lookupDir = shellPathFromOS(execDir)
		}
		// CreateProcess does not consider extensionless files executable,
		// but bash does: it attempts execve and then applies its ENOEXEC
		// script fallback. findExecutable tries the bare name after the
		// PATHEXT list for exactly that reason, so there is no second,
		// unfiltered lookup here — one would run the files the executable
		// rule just refused (exec3.sub's `exec ./x.sh`, a 0644 file).
		path, err := LookPathDir(lookupDir, hc.Env, args[0])
		if err != nil {
			err = shellVisibleLookupError(err, lookupDir, hc.Dir)
			if hc.runner != nil && hc.runner.bashCompatErrors {
				// Bash 5.3: a command name containing a slash is not
				// looked up in $PATH; it goes straight to execve, which
				// reports the underlying errno (shell_execve in
				// execute_cmd.c): a directory is `Is a directory` (126),
				// a missing path is `No such file or directory` (127),
				// and an unexecutable file is `Permission denied` (126).
				// A bare name not found in $PATH stays `command not
				// found` (127).
				cmd := bashDiagnosticWord(args[0])
				msg := fmt.Sprintf("%s%s: command not found\n",
					hc.runner.bashErrPrefix(hc.Pos), cmd)
				code := 127
				if lookPathHasPath(args[0], runtime.GOOS == "windows") {
					reason, c := classifyExecPath(lookupDir, args[0])
					msg = fmt.Sprintf("%s%s: %s\n",
						hc.runner.bashErrPrefix(hc.Pos), cmd, reason)
					code = c
				}
				fmt.Fprint(hc.Stderr, msg)
				hc.runner.reportError("exec", hc.Pos, args[0], msg, uint8(code))
				return ExitStatus(code)
			} else {
				msg := err.Error()
				fmt.Fprintln(hc.Stderr, msg)
				if hc.runner != nil {
					hc.runner.reportError("exec", hc.Pos, args[0], msg, 127)
				}
			}
			return ExitStatus(127)
		}
		execPath := shellPathToOS(lookupDir, path)
		if runtime.GOOS == "windows" && !filepath.IsAbs(execPath) {
			// CreateProcess resolves a relative application path against the
			// PARENT's cwd, not Cmd.Dir, so `./bin/x.exe` after a `cd` inside
			// the shell was "not found". Anchor the path handed to the OS;
			// argv[0], diagnostics and "_" keep the spelling the shell resolved.
			execPath = filepath.Join(execDir, execPath)
		}
		if runtime.GOOS == "windows" {
			// The lookup keeps the operand's spelling when the hit came
			// through PATHEXT (type, hash and $_ say /tmp/bash, not
			// /tmp/bash.exe); the OS runs the file itself.
			execPath = execFileWithExt(execPath, pathExts(hc.Env), func(p string) bool {
				_, err := os.Stat(p)
				return err == nil
			})
		}
		scriptPath := execPath
		if lookupDir != hc.Dir {
			// On Linux, a retained cwd is represented by /proc/self/fd/N.
			// os/exec resolves a relative Cmd.Path before applying Cmd.Dir,
			// so a relative explicit path or PATH entry would otherwise be
			// interpreted from the host process's cwd. Anchor only the path
			// passed to execve; argv[0], diagnostics, and the exported "_"
			// value keep the spelling the shell resolved.
			execPath = anchorExecPath(lookupDir, execPath)
		}
		diagnosticScriptPath := execPath
		if !shellPathAbs(diagnosticScriptPath) {
			// Cmd.Start resolves a relative executable against Cmd.Dir, but
			// missingShebangInterpreter runs in this process. Anchor the
			// diagnostic read to the runner's logical directory as well;
			// otherwise Darwin's ENOENT for a missing shebang interpreter can
			// leak as the raw os/exec error whenever the host cwd differs.
			diagnosticScriptPath = shellPathJoinAbs(lookupDir, diagnosticScriptPath)
		}
		longExecFile, longExecPath, err := openLongExecPath(execPath)
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(126)
		}
		if longExecFile != nil {
			defer longExecFile.Close()
			diagnosticScriptPath = fmt.Sprintf("/proc/self/fd/%d", longExecFile.Fd())
		}
		envCommandPath := path
		if lookupDir != hc.Dir && strings.HasPrefix(path, lookupDir+"/") {
			envCommandPath = shellPathJoinAbs(hc.Dir, strings.TrimPrefix(path, lookupDir+"/"))
		}
		cmdArgs := args
		if hc.ExecAs != "" {
			cmdArgs = append([]string{hc.ExecAs}, args[1:]...)
		}
		execPath, cmdArgs, missingInterp, execCleanup := preparePlatformExec(lookupDir, execPath, diagnosticScriptPath, cmdArgs)
		if execCleanup != nil {
			// An alias made for an extensionless PE image (exec_pe.go)
			// lives until the command has been waited for.
			defer execCleanup()
		}
		if missingInterp != "" && hc.runner != nil && hc.runner.bashCompatErrors {
			fmt.Fprintf(hc.Stderr, "%s: %s: %s: bad interpreter: No such file or directory\n",
				hc.runner.filename, args[0], missingInterp)
			return ExitStatus(126)
		}
		// Windows spells argv bytes that are not UTF-8 as lone surrogates on
		// the UTF-16 command line (argv_wtf8.go); the identity elsewhere.
		cmdArgs = execArgsForOS(cmdArgs)
		// What the child sees beyond stdio is platform-shaped: ExtraFiles
		// plus BASHY_INHERITED_FDS on Unix; on Windows no ExtraFiles at
		// all, and inheritable handles only for a child that is this
		// same binary (exec_fds_*.go). execPath is final here: a shebang
		// may have redirected it to the interpreter (or to this binary).
		fds, err := prepareChildFds(hc.runner, execPath)
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(1)
		}
		defer func() { fds.finish() }()
		extraFiles := fds.extraFiles
		// A retained Linux cwd is addressed through /proc/self/fd/N. A shebang
		// interpreter reopens that pathname after execve, when the runner's
		// close-on-exec cwd descriptor would normally be gone. Export a private
		// duplicate in the next ExtraFiles slot and rewrite only the internal
		// exec path to its stable child descriptor number. For ordinary relative
		// paths, the ENOEXEC fallback keeps scriptPath's shell-visible spelling;
		// paths beyond PATH_MAX must instead be reopened through the descriptor.
		var retainedExecDir *os.File
		if longExecPath {
			childFD := 3 + len(extraFiles)
			extraFiles = append(extraFiles, longExecFile)
			execPath = fmt.Sprintf("/proc/self/fd/%d", childFD)
			// If execve reports ENOEXEC, the fallback shell must reopen the
			// same descriptor path; the original pathname is still too long.
			scriptPath = execPath
		} else if lookupDir != hc.Dir && hc.runner.dirFile != nil && !shellPathAbs(path) {
			retainedExecDir, err = dupRunnerDir(hc.runner.dirFile)
			if err != nil {
				fmt.Fprintln(hc.Stderr, err)
				return ExitStatus(1)
			}
			defer retainedExecDir.Close()
			childFD := 3 + len(extraFiles)
			extraFiles = append(extraFiles, retainedExecDir)
			execPath = anchorExecPath(fmt.Sprintf("/proc/self/fd/%d", childFD), path)
		}
		var env []string
		if hc.ExecClearEnv {
			env = []string{}
		} else {
			// The child is a native process: hand it $TEMP, $HOME, PATH
			// entries and friends in the OS spelling (C:\…) on Windows.
			env = nativeExecEnvForChild(hc.runner.execEnvWithFuncs(), execPath)
		}
		// GNU Bash always injects `_` into an external command's environment
		// as the resolved command pathname, even though `_` is not exported as
		// a shell variable (variables.c:put_command_name_into_env).
		env = setExecEnvValue(env, "_", envCommandPath)
		// Identity checks run in this process, before exec: execPath may now
		// name an ExtraFiles descriptor that exists only in the child.
		if parent := hc.runner.childParentPIDBridge(ctx, diagnosticScriptPath, cmdArgs[0]); parent != "" {
			env = setExecEnvValue(env, bashyParentPIDEnv, parent)
		}
		env = append(env, fds.env...)
		// Go's runtime replaces a closed standard descriptor with /dev/null
		// before a Go utility's main function can inspect it. Carry fd 1's
		// shell-visible state only to the installed Coreutils image, which can
		// consume this private hint before dispatch. An explicitly opened
		// /dev/null is not a closed descriptor and must not acquire the hint.
		env = removeExecEnvValue(env, execFD1ClosedEnv)
		if isClosedFdWriter(hc.Stdout) && isInstalledCoreutilsExecutable(execPath) {
			env = setExecEnvValue(env, execFD1ClosedEnv, "1")
		}
		// `exec CMD` is execve everywhere it can be, and execve inherits
		// SIG_IGN. On Windows nothing is replaced: the shell starts CMD and
		// exits in its place, and a bashy ignore lives in this process's own
		// signal bus rather than in a kernel disposition, so there is nothing
		// for CreateProcess to carry. Bridge it by name instead, the way the
		// ENOEXEC self re-exec below does — a child bashy strips the variable
		// and treats the set as ignored-on-entry (trap.tests/trap1.sub).
		if hc.ExecReplace && runtime.GOOS == "windows" && hc.runner != nil {
			if ign := hc.runner.hardIgnoreEnvValue(); ign != "" {
				env = setExecEnvValue(env, BashyHardIgnoreEnv, ign)
			}
		}
		// Check the original exported payload before the owned-child frame
		// shortens native argv/env. True Unix exec replacement bypasses Start.
		if err := execBudgetRefusal(execPath, cmdArgs, env); err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(126)
		}
		owned, handoffArgs, handoffEnv, handoffFiles, err := prepareOwnedExec(hc.runner, execPath, cmdArgs, env, extraFiles)
		if err != nil {
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(126)
		}
		if owned != nil {
			defer owned.close()
			cmdArgs, env, extraFiles = handoffArgs, handoffEnv, handoffFiles
		}
		hc.runner.closeClosedInheritedFdsOnExec()
		// If stdin is the in-memory script-source reader, back it with a
		// seekable temp file: os/exec eagerly drains a non-File stdin, which
		// would consume the next script line even for a command that never
		// reads stdin (e.g. `echo`). With a real fd the child reads only what
		// it wants; afterwards advance the consumed offset by its actual read
		// position so a reading command (`cat`) still consumes the script.
		execStdin := hc.Stdin
		if _, ok := hc.Stdin.(badFdReader); ok {
			// fd 0 was explicitly closed (`cmd <&-`). Give the child a
			// genuinely closed descriptor so its read fails with EBADF, the
			// way bash leaves the inherited fd closed.
			execStdin, err = closedExecFile()
			if err != nil {
				fmt.Fprintln(hc.Stderr, err)
				return ExitStatus(1)
			}
		}
		execStdout := unwrapPipelineWriter(hc.Stdout)
		if isClosedFdWriter(hc.Stdout) {
			// Passing badFdWriter directly makes os/exec create a live pipe
			// and copy into the sentinel. The child then incorrectly observes
			// fd 1 as open until it writes. Pass an invalid *os.File instead;
			// os/exec represents that as a descriptor closed before execve.
			execStdout, err = closedExecFile()
			if err != nil {
				fmt.Fprintln(hc.Stderr, err)
				return ExitStatus(1)
			}
		}
		execStderr := unwrapPipelineWriter(hc.Stderr)
		if isClosedFdWriter(hc.Stderr) {
			execStderr, err = closedExecFile()
			if err != nil {
				return ExitStatus(1)
			}
		}
		if sr, ok := hc.Stdin.(*scriptStdinReader); ok && hc.runner != nil {
			if f, base := sr.seekableFile(); f != nil {
				execStdin = f
				defer func() {
					if pos, serr := f.Seek(0, io.SeekCurrent); serr == nil {
						hc.runner.stdinSourceOffset = max(hc.runner.stdinSourceOffset, base+int(pos))
					}
					f.Close()
					os.Remove(f.Name())
				}()
			}
		}
		proxyReplace := false
		var replacement *execReplacementAttempt
		if hc.ExecReplace && hc.runner != nil {
			state := hc.runner.execReplacement
			state.mu.Lock()
			if pending := state.pending; pending != nil && state.owner == hc.runner {
				state.mu.Unlock()
				return pending.status()
			}
			proxyReplace = hc.runner.hasLiveBackgroundJobs()
			if proxyReplace {
				replacement = &execReplacementAttempt{ready: make(chan struct{})}
				state.current.Store(replacement)
			}
			state.mu.Unlock()
			if replacement != nil {
				defer state.current.CompareAndSwap(replacement, nil)
			}
		}
		if hc.ExecReplace && !proxyReplace {
			if replaced, err := execReplace(ctx, execPath, cmdArgs, env, execStdin, execStdout, execStderr, owned); replaced {
				return err
			}
		}
		cmd := exec.Cmd{
			Path:       execPath,
			Args:       cmdArgs,
			Env:        env,
			Dir:        execDir,
			Stdin:      execStdin,
			Stdout:     execStdout,
			Stderr:     execStderr,
			ExtraFiles: extraFiles,
		}
		if fds.sysAttr != nil {
			fds.sysAttr(&cmd)
		}
		if owned != nil && owned.apply != nil {
			owned.apply(&cmd)
		}
		prepareBackgroundJobCmd(ctx, &cmd)
		foregroundTTY := prepareForegroundJobCmd(ctx, hc.runner, &cmd)
		defer func() {
			if foregroundTTY != nil {
				_ = foregroundTTY.restore()
			}
		}()
		startingFallback := false
		ownGroup := hc.runner != nil && hc.runner.execProcessGroups
		startCmd := func() error {
			var startErr error
			if ownGroup {
				ownGroup = setOwnProcessGroup(&cmd)
			}
			if refusal := execBudgetRefusalCmd(&cmd); refusal != nil && (owned == nil || startingFallback) {
				startErr = refusal
			} else if start, ok := ctx.Value(execStartOverrideCtxKey{}).(func(*exec.Cmd) error); ok {
				startErr = start(&cmd)
			} else if hc.runner != nil {
				startErr = hc.runner.startExecCmdWithUmask(ctx, &cmd, hc.runner.umask)
			} else {
				startErr = cmd.Start()
			}
			// The child (if any) holds its own copies now; release what
			// only the Start needed.
			fds.started()
			if owned != nil && owned.started != nil {
				owned.started()
			}
			if startErr != nil && foregroundTTY != nil {
				// Foreground may transfer the terminal before exec fails.
				// Restore it before preparing an ENOEXEC fallback, which must
				// observe the shell owning the terminal and acquire a fresh fd.
				restoreErr := foregroundTTY.restore()
				foregroundTTY = nil
				if restoreErr != nil {
					return fmt.Errorf("restore terminal after failed start: %w", restoreErr)
				}
			}
			return startErr
		}
		// Job control off leaves this command in the shell's own pgrp (see
		// prepareForegroundJobCmd), so terminal INT/QUIT reach the shell too;
		// guard them for the run's full extent, including pipeline components,
		// the ENOEXEC self-reexec fallback below, and the wait further down.
		stopSignalGuard := hc.runner.guardUnmonitoredForegroundSignals(ctx)
		defer stopSignalGuard()

		err = startCmd()
		// POSIX/bash: when execve fails with ENOEXEC (the file
		// has no shebang and isn't a recognised binary), the
		// shell falls back to running the file as a shell
		// script. Re-invoke our own bashy binary on the file so
		// the script's traps/locals/etc. all work in the same
		// shell.
		if err != nil && isExecFormatError(err) {
			selfBin, lookupErr := os.Executable()
			if lookupErr == nil {
				// Keep the shell's invocation name while executing the same
				// physical binary. A multicall executable selects its shell
				// route from argv[0], not from the executable path.
				newArgs := append([]string{os.Args[0], scriptPath}, execArgsForOS(args[1:])...)
				// Re-exec'ing our own shell on a no-shebang script: carry the
				// parent's hard-ignored signals across so the child shell
				// treats them as ignored-on-entry, matching how bash inherits
				// SIG_IGN through execve (trap.tests/trap1.sub).
				reExecEnv := env
				if hc.runner != nil {
					if ign := hc.runner.hardIgnoreEnvValue(); ign != "" {
						reExecEnv = setExecEnvValue(append([]string(nil), env...), BashyHardIgnoreEnv, ign)
					}
				}
				// The target is now our own binary: on Windows that is
				// where the descriptor handoff applies even if the first
				// attempt aimed elsewhere. Unix keeps the prepared set.
				reExecFds, fdErr := prepareSelfReexecFds(hc.runner, fds)
				if fdErr != nil {
					fmt.Fprintln(hc.Stderr, fdErr)
					return ExitStatus(1)
				}
				if reExecFds != fds {
					// Drop the first attempt's fd announcement, if any,
					// in favour of the re-exec's.
					reExecEnv = replaceExecEnvEntries(reExecEnv, fds.env, reExecFds.env)
					fds = reExecFds
					extraFiles = fds.extraFiles
				}
				cmd = exec.Cmd{
					Path:       selfBin,
					Args:       newArgs,
					Env:        reExecEnv,
					Dir:        execDir,
					Stdin:      execStdin,
					Stdout:     execStdout,
					Stderr:     execStderr,
					ExtraFiles: extraFiles,
				}
				if fds.sysAttr != nil {
					fds.sysAttr(&cmd)
				}
				prepareBackgroundJobCmd(ctx, &cmd)
				foregroundTTY = prepareForegroundJobCmd(ctx, hc.runner, &cmd)
				startingFallback = true
				err = startCmd()
			}
		}
		if replacement != nil {
			if err == nil {
				replacement.pid.Store(int64(cmd.Process.Pid))
			}
			close(replacement.ready)
		}
		if err != nil && hc.runner != nil && hc.runner.bashCompatErrors {
			if interp, ok := missingShebangInterpreter(diagnosticScriptPath); ok {
				fmt.Fprintf(hc.Stderr, "%s: %s: %s: bad interpreter: No such file or directory\n",
					hc.runner.filename, args[0], interp)
				return ExitStatus(126)
			}
		}
		if err == nil {
			publishBgPid(ctx, cmd.Process.Pid)
			stopReplacementStopWatch := func() {}
			stopReplacementSignalForward := func() {}
			if proxyReplace {
				stopReplacementStopWatch = watchExecReplacementStops(cmd.Process.Pid)
				stopReplacementSignalForward = forwardExecReplacementSignals(cmd.Process.Pid)
			}
			defer stopReplacementStopWatch()
			defer func() { stopReplacementSignalForward() }()
			// With [ExecProcessGroups] the child leads its own group, and
			// the whole group is signalled: descendants must not outlive the
			// cancellation or keep the output pipe (and so Wait) open.
			signal := func(sig os.Signal) { signalExecCmd(&cmd, sig, ownGroup) }
			stopf := context.AfterFunc(ctx, func() {
				if killTimeout <= 0 || runtime.GOOS == "windows" {
					signal(os.Kill)
					return
				}
				signal(execCancelSignal(ctx))
				// TODO: don't sleep in this goroutine if the program
				// stops itself with the signal above.
				time.Sleep(killTimeout)
				signal(os.Kill)
			})
			defer stopf()

			var childUser, childSys time.Duration
			err, childUser, childSys = waitExecCmd(ctx, &cmd)
			// Fold this external child's CPU into the enclosing `time` clause.
			// waitExecCmd reports it from ProcessState on the ordinary path and
			// directly from Wait4 rusage on the Unix job-control path.
			if hc.runner != nil {
				hc.runner.accumulateChildCPU(childUser, childSys)
			}
			// Stop intercepting the proxy's signals before translating the
			// replacement's terminal wait status below. Otherwise relaying the
			// child's death signal back to this process is caught here and sent
			// to an already-dead child instead of reaching the proxy's parent.
			stopReplacementSignalForward()
			stopReplacementSignalForward = func() {}
			standaloneReplacement := false
			if hc.runner != nil {
				_, standaloneReplacement = hc.runner.sigReset.(OSSignalResetter)
			}
			if replacement != nil && standaloneReplacement {
				// A real shell's asynchronous child which addressed $$ survives
				// execve. Let that particular in-process observer finish its
				// post-kill bookkeeping before this host exits.
				if bg := replacement.observer.Load(); bg != nil {
					<-bg.done
				}
			}
			if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil {
				replacing, _ := ctx.Value(execReplacingCtxKey{}).(bool)
				if replacing {
					// The carrier may have died to deliver a signal, but an exec'd
					// child can catch that signal and choose its own exit status. Save
					// the actual wait result before the context-cancellation path below
					// translates a signaled child back into ctx.Err().
					code := -1
					switch waitErr := err.(type) {
					case nil:
						code = 0
					case *exec.ExitError:
						code = waitErr.ExitCode()
						if status, ok := execWaitStatus(&cmd, waitErr); ok && status.Signaled() {
							code = 128 + int(status.Signal())
						}
					}
					if code >= 0 {
						bg.execResult.Store(int32(code) + 1)
					}
				}
			}
			// A reaped foreground child runs the SIGCHLD trap once, like a
			// reaped background job (bash waitchld). Background execs are
			// skipped here — their owning bgProc goroutine fires the trap on
			// completion, so firing here too would double-count. Gated on
			// job-control (set -m), matching bash's foreground-child reaping.
			if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg == nil &&
				hc.runner != nil && hc.runner.monitorActive() {
				hc.runner.notifyChildReaped()
			}
		}
		switch err := err.(type) {
		case *exec.ExitError:
			// execWaitStatus is the kernel wait status on Unix; on Windows it
			// decodes the bashy signal marker (waitstatus_windows.go) and on
			// Plan9 it never reports a signal.
			if status, ok := execWaitStatus(&cmd, err); ok && status.Signaled() {
				// With job control or an owned exec group, the foreground
				// command does not share the shell's signal-delivery group.
				// Background jobs have their own runner/notification boundary.
				if hc.runner != nil && !ownGroup && !hc.runner.monitorActive() {
					if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg == nil {
						if _, name, ok := signalByNumber(int(status.Signal())); ok {
							hc.runner.waitForForegroundGroupSignalReceipt(ctx, name)
						}
					}
				}
				if proxyReplace {
					if _, standalone := hc.runner.sigReset.(OSSignalResetter); standalone {
						return relayExecReplacementSignal(status.Signal())
					}
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// #25/#26: a foreground external command killed by a fatal
				// signal in a non-interactive shell prints a bash-style status
				// line to stderr in default mode; POSIX mode stays silent,
				// deferring to wait/jobs. Background `&` jobs have their own
				// notification path (bgProcCtxKey is non-nil for them), so only
				// foreground execs notify here.
				if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg == nil && hc.runner != nil {
					hc.runner.notifyForegroundSignalDeath(hc.Stderr, hc.Pos, cmd.Process.Pid, status, args)
				}
				signal := int(status.Signal())
				name := ""
				if _, short, ok := signalByNumber(signal); ok {
					name = "SIG" + short
				}
				return SignaledStatus{
					Status:     ExitStatus(128 + signal),
					Signal:     signal,
					SignalName: name,
				}
			}
			return ExitStatus(err.ExitCode())
		case *exec.Error:
			// did not start
			fmt.Fprintf(hc.Stderr, "%v\n", err)
			return ExitStatus(127)
		case *os.PathError:
			// The program was found but execve could not start it (for
			// example, because the host denies execution). This is a command
			// failure, not a fatal interpreter error: bash reports it and
			// continues with the next command.
			fmt.Fprintln(hc.Stderr, err)
			return ExitStatus(126)
		default:
			return err
		}
	}
}

// execBudgetRefusal is shared by true execve replacement and os/exec Start.
// The latter also checks re-exec fallback commands, whose argv/env can differ.
func execBudgetRefusal(path string, args, env []string) error {
	if !execbudget.OverBashyArgMax(args, env) {
		return nil
	}
	return &os.PathError{Op: "fork/exec", Path: path, Err: execBudgetErr()}
}

// execBudgetRefusalCmd handles os/exec's nil-Env inheritance and nil-Args
// fallback for launch paths outside DefaultExecHandler.
func execBudgetRefusalCmd(cmd *exec.Cmd) error {
	args := cmd.Args
	if len(args) == 0 {
		args = []string{cmd.Path}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	return execBudgetRefusal(cmd.Path, args, env)
}

func startExecCmdWithinBudget(cmd *exec.Cmd) error {
	if err := execBudgetRefusalCmd(cmd); err != nil {
		return err
	}
	return cmd.Start()
}

// shellVisibleLookupError removes the retained Linux cwd descriptor from a
// lookup diagnostic. The descriptor is an implementation detail: a command
// after `cd /` must report `/missing`, not `/proc/self/fd/N/missing`.
func shellVisibleLookupError(err error, lookupDir, shellDir string) error {
	if lookupDir == shellDir {
		return err
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		return err
	}
	prefix := strings.TrimSuffix(lookupDir, "/") + "/"
	if !strings.HasPrefix(pathErr.Path, prefix) {
		return err
	}
	visible := *pathErr
	visible.Path = shellPathJoinAbs(shellDir, strings.TrimPrefix(pathErr.Path, prefix))
	return &visible
}

// anchorExecPath makes a relative executable path relative to dir without
// cleaning either operand. In particular, dir may be /proc/self/fd/N and path
// may begin with ../; the kernel must follow the fd symlink before resolving
// that parent component.
func anchorExecPath(dir, path string) string {
	if shellPathAbs(path) {
		return path
	}
	if strings.HasSuffix(dir, "/") {
		return dir + path
	}
	return dir + "/" + path
}

// execCancelSignal returns the signal which caused a background job's
// carrier to cancel its in-process runner. Ordinary context cancellation
// retains the historical interrupt-first shutdown, but a carrier signal must
// reach the represented external child unchanged. In particular, translating
// TERM into INT lets an INT-ignoring child run for killTimeout before KILL,
// long enough to mutate state after the shell has reported a successful kill.
func execCancelSignal(ctx context.Context) os.Signal {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg != nil {
		if n := int(bg.killedSignal.Load()); n > 0 {
			if sig, _, ok := signalByNumber(n); ok {
				return signalForOS(sig)
			}
		}
	}
	return os.Interrupt
}

func setExecEnvValue(env []string, name, value string) []string {
	prefix := name + "="
	found := false
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if found {
				continue
			}
			kv = prefix + value
			found = true
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}

func removeExecEnvValue(env []string, name string) []string {
	prefix := name + "="
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// replaceExecEnvEntries returns a copy of env without the variables named
// by the old NAME=VALUE entries and with the new ones appended.
func replaceExecEnvEntries(env, old, new []string) []string {
	drop := make(map[string]bool, len(old))
	for _, kv := range old {
		name, _, _ := strings.Cut(kv, "=")
		drop[name] = true
	}
	out := make([]string, 0, len(env)+len(new))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return append(out, new...)
}

func missingShebangInterpreter(path string) (string, bool) {
	data, err := readShebangProbe(path)
	if err != nil {
		return "", false
	}
	interp, _, ok := parseShebang(data)
	if !ok {
		return "", false
	}
	if shellPathAbs(interp) {
		if _, err := os.Stat(shellPathToOS(path, interp)); err == nil {
			return "", false
		}
		return interp, true
	}
	if _, err := exec.LookPath(interp); err == nil {
		return "", false
	}
	return interp, true
}

// classifyExecPath inspects a command name that contains a slash and
// failed PATH lookup, returning the bash-style diagnostic reason and exit
// code that execve would have produced (shell_execve in execute_cmd.c):
//   - missing path  -> "No such file or directory" (127, EX_NOTFOUND)
//   - a directory   -> "Is a directory"            (126, EX_NOEXEC)
//   - not runnable   -> "Permission denied"         (126, EX_NOEXEC)
func classifyExecPath(dir, file string) (string, int) {
	target := file
	target = shellPathJoinAbs(dir, target)
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "No such file or directory", 127
		}
		if os.IsPermission(err) {
			return "Permission denied", 126
		}
		if reason, ok := posixErrorText(err); ok {
			return reason, 127
		}
		return "No such file or directory", 127
	}
	if info.IsDir() {
		return "Is a directory", 126
	}
	return "Permission denied", 126
}

// checkBinaryFile mirrors bash's check_binary_file (general.c): a sample
// is "binary" if it begins with the ELF magic, or has a NUL byte before
// the end of its first line (first two lines if it starts with `#!`).
func checkBinaryFile(sample []byte) bool {
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

// isBinarySource reports whether content should be rejected by the source
// builtin as a binary file: check_binary_file on the first 80 bytes, or
// more than 256 NUL bytes total (the FEVAL_BUILTIN guard in evalfile.c).
func isBinarySource(content []byte) bool {
	sample := content
	if len(sample) > 80 {
		sample = sample[:80]
	}
	if checkBinaryFile(sample) {
		return true
	}
	nulls := 0
	for _, c := range content {
		if c == 0 {
			nulls++
			if nulls > 256 {
				return true
			}
		}
	}
	return false
}

func checkStat(dir, file string, checkExec bool) (string, error) {
	target := file
	target = lookupStatPath(dir, target)
	// A Windows process-substitution pipe is answered from its name. It is
	// not a filesystem node: every CreateFile on \\.\pipe\<name> —
	// os.Stat's included — takes the consumer's one connection to the
	// server instance, so `. <(cmd)`, which looks the path up here before
	// opening it, would eat its own rendezvous and then find the pipe
	// busy. r.stat sidesteps it the same way.
	info, statErr, ok := procSubstPipeStatErr("stat", target)
	if ok && statErr != nil {
		// A pipe the shell has already released is gone, like the unlinked
		// FIFO of a finished substitution on Unix.
		return "", statErr
	}
	if !ok {
		var err error
		info, err = os.Stat(target)
		if err != nil {
			info, err = statLongPath(target, err)
			if err != nil {
				return "", err
			}
		}
	}
	m := info.Mode()
	if m.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if checkExec && runtime.GOOS != "windows" && (m&0o111 == 0 || !canExec(target)) {
		return "", fmt.Errorf("permission denied")
	}
	// Return the input form (`./e`, `bin/foo`, `/abs/path`) so
	// callers like `type -P` mirror bash's output, which echoes
	// the path as it appeared in $PATH rather than the absolute
	// form derived from the runner's cwd.
	return file, nil
}

// lookupStatPath preserves relative parent components when dir is the retained
// Linux cwd handle. filepath.Join would clean /proc/self/fd/N/../program into
// /proc/self/fd/program before the kernel can follow the N symlink.
func lookupStatPath(dir, path string) string {
	if strings.HasPrefix(dir, "/proc/self/fd/") && !shellPathAbs(path) {
		return anchorExecPath(dir, path)
	}
	return shellPathJoinAbs(dir, path)
}

func winHasExt(file string) bool {
	i := strings.LastIndex(file, ".")
	if i < 0 {
		return false
	}
	return strings.LastIndexAny(file, `:\/`) < i
}

// windowsExecutableFile reports whether a file Windows can see is one the
// shell may run. Windows has no execute bit — os.Chmod only toggles the
// read-only attribute, so a `chmod +x` leaves nothing a stat can read back
// — and the rule is the one Cygwin applies on a filesystem without POSIX
// ACLs: an extension Windows itself runs (PATHEXT), a name with no
// extension at all (how a POSIX tool or script is spelled), or a file whose
// first bytes say it is a program or a script. exec3.sub writes `echo bar`
// into x.sh with a plain redirect and wants `exec ./x.sh` refused, the way
// a 0644 file is refused on Unix — and a redirect records no mode, so the
// name rule still decides that one.
func windowsExecutableFile(target string, exts []string) bool {
	// A mode somebody recorded is the answer, whatever the name says: the
	// corpus ships .sub scripts 755 and runs them directly, and `chmod +x`
	// on any name must mean what it says (winmode).
	if info, err := os.Stat(target); err == nil {
		if applied := winmode.Apply(target, info); winmode.Recorded(applied) {
			perm := applied.Mode().Perm()
			owner, ownerKnown := winmode.IsOwner(target)
			if ownerKnown && owner {
				return perm&0o100 != 0
			}
			group, groupKnown := winmode.InGroup(target)
			if groupKnown && group {
				return perm&0o010 != 0
			}
			// A readable owner/group identity gives the other class. If
			// Windows withheld either SID, retain the prior any-X fallback.
			if ownerKnown && groupKnown {
				return perm&0o001 != 0
			}
			return perm&0o111 != 0
		}
	}
	if !winHasExt(target) {
		return true
	}
	lower := strings.ToLower(target)
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return hasExecutableMagic(target)
}

// hasExecutableMagic reports whether a file begins with a run-me marker: a
// #! shebang, or the MZ (DOS/PE) or \x7fELF image header.
func hasExecutableMagic(target string) bool {
	f, err := os.Open(target)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [4]byte
	n, _ := io.ReadFull(f, buf[:])
	head := buf[:n]
	return bytes.HasPrefix(head, []byte("#!")) ||
		bytes.HasPrefix(head, []byte("MZ")) ||
		bytes.HasPrefix(head, []byte("\x7fELF"))
}

// findExecutable returns the path to an existing executable file.
func findExecutable(dir, file string, exts []string) (string, error) {
	if len(exts) == 0 {
		// non-windows
		return checkStat(dir, file, true)
	}
	if winHasExt(file) {
		if found, err := checkStat(dir, file, true); err == nil {
			if !windowsExecutableFile(lookupStatPath(dir, found), exts) {
				// Stop here rather than falling through to the PATHEXT
				// loop: the caller must see EACCES (126), not the "not
				// found" that would become `command not found` (127).
				return "", fmt.Errorf("permission denied")
			}
			return found, nil
		}
	}
	for _, e := range exts {
		if _, err := checkStat(dir, file+e, true); err == nil {
			// A hit through PATHEXT keeps the operand's spelling — the
			// POSIX-spelled /bin/sh and a bare name joined onto its PATH
			// element alike: `type -p bash` prints /tmp/bash, `hash`
			// records it and $_ carries it, as bash on Cygwin shows the
			// file. The exec handler resolves the file to run from that
			// spelling (execFileWithExt), and os/exec appends the
			// extension itself.
			return file, nil
		}
	}
	if !winHasExt(file) {
		// A name with no extension at all is not a Windows program, but it
		// is how a POSIX tool or script is spelled, and execFileWithExt is
		// already willing to run one: type5.sub does `touch e; chmod +x e`
		// and then wants `type -p e` to report it. PATHEXT has had its turn
		// above, so a foo.exe beside a plain foo still wins.
		if _, err := checkStat(dir, file, true); err == nil &&
			windowsExecutableFile(lookupStatPath(dir, file), exts) {
			return file, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// execFileWithExt returns the file to run for a path in the shell's display
// spelling: the path itself when it exists, else the first path+ext in
// PATHEXT order that does. The Windows lookup reports the spelling without
// the suffix (findExecutable); the shebang probe, the self-identity check
// and CreateProcess need the file.
func execFileWithExt(path string, exts []string, exists func(string) bool) string {
	if exists(path) {
		return path
	}
	for _, e := range exts {
		if exists(path + e) {
			return path + e
		}
	}
	return path
}

// findFile returns the path to an existing file.
func findFile(dir, file string, _ []string) (string, error) {
	return checkStat(dir, file, false)
}

// findReadableFile returns the path to an existing readable file.
func findReadableFile(dir, file string, _ []string) (string, error) {
	path, err := checkStat(dir, file, false)
	if err != nil {
		return "", err
	}
	target := shellPathJoinAbs(dir, path)
	if runtime.GOOS != "windows" && !canRead(target) {
		return "", fmt.Errorf("permission denied")
	}
	return path, nil
}

// LookPath is deprecated; see [LookPathDir].
func LookPath(env expand.Environ, file string) (string, error) {
	return LookPathDir(env.Get("PWD").String(), env, file)
}

// LookPathDir is similar to [os/exec.LookPath], with the difference that it uses the
// provided environment. env is used to fetch relevant environment variables
// such as PWD and PATH.
//
// If no error is returned, the returned path must be valid.
func LookPathDir(cwd string, env expand.Environ, file string) (string, error) {
	return lookPathDir(cwd, env, file, findExecutable)
}

// findAny defines a function to pass to [lookPathDir].
type findAny = func(dir string, file string, exts []string) (string, error)

func lookPathDir(cwd string, env expand.Environ, file string, find findAny) (string, error) {
	return lookPathDirMode(cwd, env, file, find, runtime.GOOS == "windows")
}

func lookPathDirMode(cwd string, env expand.Environ, file string, find findAny, windows bool) (string, error) {
	if find == nil {
		panic("no find function found")
	}

	pathList := splitLookPath(env.Get("PATH").String(), windows)
	if len(pathList) == 0 {
		pathList = []string{""}
	}
	exts := pathExtsMode(env, windows)
	if lookPathHasPath(file, windows) {
		return find(cwd, file, exts)
	}
	for _, elem := range pathList {
		var path string
		switch elem {
		case "", ".":
			// Bash reports a command found via an empty or "." PATH
			// element as "./name" (findcmd.c). filepath.Join(".", name)
			// would clean the "./" away, so build it directly — on Windows
			// too, where lookPathJoin ends up in filepath.Join and did
			// exactly that: type5.sub sets PATH= and wants `type -p e` to
			// print "./e", not "e". This also guarantees the result carries
			// a slash, which `type -p`/`-P` and command-path output rely on.
			path = "./" + file
		default:
			path = lookPathJoin(elem, file, windows)
		}
		if f, err := find(cwd, path, exts); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("%q: executable file not found in $PATH", file)
}

func splitLookPath(path string, windows bool) []string {
	return pathconv.SplitPathList(path, windows)
}

// lookPathHasPath reports whether a command word names a path, which bash
// hands to execve as is, rather than a name to search on $PATH. On Windows
// a separator or a leading drive spec ("C:") counts; a colon anywhere else
// is an ordinary character of the name, so a function called `<(:)` that
// is not found is "command not found", not "No such file or directory".
func lookPathHasPath(file string, windows bool) bool {
	if windows {
		if strings.ContainsAny(file, `\/`) {
			return true
		}
		return len(file) >= 2 && file[1] == ':' && isASCIILetter(file[0])
	}
	return strings.Contains(file, `/`)
}

func isASCIILetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func lookPathJoin(dir, file string, windows bool) string {
	if windows && strings.HasPrefix(dir, "/") {
		// A POSIX-spelled PATH element (/bin, /usr/bin) yields a
		// POSIX-spelled hit (/bin/sh): the mounts resolve it when it is
		// stat'ed or executed, and `type -p`/`hash` echo it as bash does.
		if strings.HasSuffix(dir, "/") {
			return dir + file
		}
		return dir + "/" + file
	}
	if !windows || runtime.GOOS == "windows" {
		return filepath.Join(dir, file)
	}
	if strings.HasSuffix(dir, `\`) || strings.HasSuffix(dir, `/`) {
		return dir + file
	}
	return dir + `\` + file
}

// scriptFromPathDir is similar to [LookPathDir], with the difference that it looks
// for readable scripts rather than executable programs.
func scriptFromPathDir(cwd string, env expand.Environ, file string) (string, error) {
	windows := runtime.GOOS == "windows"
	if lookPathHasPath(file, windows) {
		return findFile(cwd, file, pathExtsMode(env, windows))
	}
	return lookPathDir(cwd, env, file, findReadableFile)
}

func pathExts(env expand.Environ) []string {
	return pathExtsMode(env, runtime.GOOS == "windows")
}

func pathExtsMode(env expand.Environ, windows bool) []string {
	if !windows {
		return nil
	}
	pathext := env.Get("PATHEXT").String()
	if pathext == "" {
		return []string{".com", ".exe", ".bat", ".cmd"}
	}
	var exts []string
	for e := range strings.SplitSeq(strings.ToLower(pathext), `;`) {
		if e == "" {
			continue
		}
		if e[0] != '.' {
			e = "." + e
		}
		exts = append(exts, e)
	}
	return exts
}

// OpenHandlerFunc is a handler which opens files.
// It is called for all files that are opened directly by the shell,
// such as in redirects, except for named pipes created by process substitutions.
// The context includes a [HandlerContext] value.
// Files opened by executed programs are not included.
//
// The path parameter may be relative to the current directory,
// which can be fetched via [HandlerCtx].
//
// Use a return error of type [*os.PathError] to have the error printed to
// stderr and the exit status set to 1.
// Any other error will halt the [Runner] and will be returned via the API.
//
// Note that implementations which do not return [os.File] will cause
// extra files and goroutines for input redirections; see [StdIO].
type OpenHandlerFunc func(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error)

// TODO: paths passed to [OpenHandlerFunc] should be cleaned.

// DefaultOpenHandler returns the [OpenHandlerFunc] used by default.
// It uses [os.OpenFile] to open files and net.Dialer for Bash's marked
// /dev/tcp and /dev/udp redirection pseudo-paths.
//
// For the sake of portability, /dev/null opens NUL on Windows.
func DefaultOpenHandler() OpenHandlerFunc {
	return func(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
		if ctx.Value(devNetworkRedirectCtxKey{}) != nil {
			if network, address, ok := devNetworkRedirect(path); ok {
				return openDevNetwork(ctx, path, network, address)
			}
		}
		mc := HandlerCtx(ctx)
		if console, ok := devTTYConsolePath(path, flag); ok {
			return openPath(ctx, console, flag&^(os.O_CREATE|os.O_TRUNC|os.O_EXCL), perm)
		}
		if runtime.GOOS == "windows" && path == "/dev/null" {
			path = "NUL"
			// Note that even though https://go.dev/issue/71752 was resolved for Windows,
			// the workaround here seems to still be required for Wine as of 10.14.
			// TODO(mvdan): Why? Is this Wine's fault?
			flag &^= os.O_TRUNC
			return openPath(ctx, path, flag, perm)
		}
		return openPathAt(ctx, mc.Dir, path, flag, perm)
	}
}

// devNetworkRedirect recognizes Bash's /dev/{tcp,udp}/HOST/PORT paths. The
// port is deliberately left as text: net.Dial accepts both numeric ports and
// the service names that Bash accepts through getaddrinfo.
func devNetworkRedirect(path string) (network, address string, ok bool) {
	for _, network = range [...]string{"tcp", "udp"} {
		prefix := "/dev/" + network + "/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		host, port, found := strings.Cut(path[len(prefix):], "/")
		if !found || host == "" || port == "" || strings.Contains(port, "/") {
			return "", "", false
		}
		return network, net.JoinHostPort(host, port), true
	}
	return "", "", false
}

// openDevNetwork connects a Bash network pseudo-path without involving a
// subprocess. File returns a duplicate descriptor, which lets the existing
// numbered-fd tables preserve a bidirectional `exec N<>/dev/tcp/...` exactly
// as they do for ordinary files.
func openDevNetwork(ctx context.Context, path, network, address string) (io.ReadWriteCloser, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, &os.PathError{Op: "connect", Path: path, Err: err}
	}
	fileConn, ok := conn.(interface{ File() (*os.File, error) })
	if !ok {
		_ = conn.Close()
		return nil, &os.PathError{Op: "connect", Path: path, Err: fmt.Errorf("%s connection has no file descriptor", network)}
	}
	f, err := fileConn.File()
	closeErr := conn.Close()
	if err != nil {
		return nil, &os.PathError{Op: "connect", Path: path, Err: err}
	}
	if closeErr != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "connect", Path: path, Err: closeErr}
	}
	return f, nil
}

// devNetworkErrorReason unwraps net.OpError and os.SyscallError down to the
// portable syscall wording ("connection refused", "network is unreachable",
// and so on), matching Bash instead of Go's full dial operation string.
func devNetworkErrorReason(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			message := err.Error()
			if message == "" {
				return message
			}
			return strings.ToUpper(message[:1]) + message[1:]
		}
		err = next
	}
}

// TODO(v4): if this is kept in v4, it most likely needs to use [io/fs.DirEntry] for efficiency

// ReadDirHandlerFunc is a handler which reads directories. It is called during
// shell globbing, if enabled.
//
// Deprecated: use [ReadDirHandlerFunc2], which uses [fs.DirEntry].
type ReadDirHandlerFunc func(ctx context.Context, path string) ([]fs.FileInfo, error)

// ReadDirHandlerFunc2 is a handler which reads directories. It is called during
// shell globbing, if enabled.
// The context includes a [HandlerContext] value.
type ReadDirHandlerFunc2 func(ctx context.Context, path string) ([]fs.DirEntry, error)

// DefaultReadDirHandler returns the [ReadDirHandlerFunc] used by default.
// It makes use of [ioutil.ReadDir].
func DefaultReadDirHandler() ReadDirHandlerFunc {
	return func(ctx context.Context, path string) ([]fs.FileInfo, error) {
		return ioutil.ReadDir(path)
	}
}

// DefaultReadDirHandler2 returns the [ReadDirHandlerFunc2] used by default.
// It uses [os.ReadDir].
func DefaultReadDirHandler2() ReadDirHandlerFunc2 {
	return func(ctx context.Context, path string) ([]fs.DirEntry, error) {
		if ctx == nil {
			return os.ReadDir(path)
		}
		hc, ok := ctx.Value(handlerCtxKey{}).(HandlerContext)
		if !ok {
			return os.ReadDir(path)
		}
		if hc.runner != nil {
			hc.runner.ensureDirFile(hc.Dir)
			relative := path
			if shellPathAbs(path) {
				if rel, err := filepath.Rel(hc.Dir, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					relative = rel
				} else {
					relative = ""
				}
			}
			if relative != "" {
				if entries, ok, err := readRunnerDir(hc.runner.dirFile, relative); ok {
					return entries, err
				}
			}
		}
		path = shellPathJoinAbs(hc.Dir, path)
		return os.ReadDir(path)
	}
}

// StatHandlerFunc is a handler which gets a file's information.
// The context includes a [HandlerContext] value.
type StatHandlerFunc func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error)

// DefaultStatHandler returns the [StatHandlerFunc] used by default.
// It makes use of [os.Stat] and [os.Lstat], depending on followSymlinks.
//
// A Windows FIFO is a marker file naming a named pipe (fifo_marker.go), so
// this is also where such a path picks up the type POSIX gives a FIFO —
// [fifoMarkerStat], the identity everywhere else. Doing it here rather than
// at each predicate is what keeps `test -p`, `test -f` and the open of the
// same path telling one story.
//
// This is the shell's one reader of a POSIX mode. Windows has no mode bits,
// so a mode set by chmod lives in the file's ACL ([winmode]) and io/fs
// cannot see it: os.Stat reports 0666 or 0444 off the read-only attribute
// and nothing else. Substituting the recorded mode here rather than at each
// predicate is what makes `test -x`, `test -r`, `test -g` and every other
// caller of FileInfo.Mode agree with each other, and with the chmod that
// wrote the mode. Everywhere but Windows this is the identity.
func DefaultStatHandler() StatHandlerFunc {
	windows := runtime.GOOS == "windows"
	lstat, stat := recordedMode(os.Lstat), recordedMode(os.Stat)
	return func(ctx context.Context, path string, followSymlinks bool) (fs.FileInfo, error) {
		if info, ok := devTTYStat(path); ok {
			return info, nil
		}
		path = shellPathJoinAbs(handlerDir(ctx), path)
		if !followSymlinks {
			info, err := lstat(path)
			if err != nil {
				return statExeFallback(lstat, path, err, windows)
			}
			return fifoMarkerStat(path, info), nil
		}
		info, err := stat(path)
		if err != nil {
			info, err = statLongPath(path, err)
			if err != nil {
				return statExeFallback(stat, path, err, windows)
			}
		}
		return fifoMarkerStat(path, info), nil
	}
}

// recordedMode wraps a stat function so that the mode recorded in a file's
// ACL, if there is one, replaces the mode the platform derived from the
// file attributes. It wraps the .exe retry too, so an extensionless path
// that resolves to path.exe reports the mode recorded for the .exe.
func recordedMode(stat func(string) (fs.FileInfo, error)) func(string) (fs.FileInfo, error) {
	if !winmode.Supported {
		return stat
	}
	return func(path string) (fs.FileInfo, error) {
		info, err := stat(path)
		if err != nil {
			return info, err
		}
		return winmode.Apply(path, info), nil
	}
}

// exeRetryPath returns path+".exe" when a missing path could be the
// extensionless spelling of a Windows executable: the last component has
// no extension and the path does not end in a separator.
func exeRetryPath(path string) (string, bool) {
	if path == "" || winHasExt(path) {
		return "", false
	}
	if last := path[len(path)-1]; last == '/' || last == '\\' || last == ':' {
		return "", false
	}
	return path + ".exe", true
}

// statExeFallback gives a stat of an extensionless path Cygwin's .exe
// transparency on Windows: when path does not exist but path.exe does,
// the .exe's info is returned, so `[ -x /bin/sh ]`, `cd /bin/sh` (Not a
// directory) and `cp /bin/sh .` see the file the way bash's scripts
// expect. Any other error, and every non-Windows host, returns err as is.
func statExeFallback(stat func(string) (fs.FileInfo, error), path string, err error, windows bool) (fs.FileInfo, error) {
	if !windows || !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	exe, ok := exeRetryPath(path)
	if !ok {
		return nil, err
	}
	info, exeErr := stat(exe)
	if exeErr != nil {
		return nil, err
	}
	return info, nil
}

func handlerDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	hc, _ := ctx.Value(handlerCtxKey{}).(HandlerContext)
	return hc.Dir
}
