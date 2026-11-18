package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bashsharp/bashsharp/front"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
)

// SessionIO carries the per-request stdio/env/dir for a command run inside a
// warm `bashy serve` session. One serve process builds a fresh runner per
// request from this, so a session-routed `bashy -c "…"` behaves identically to
// a cold `bashy -c "…"` while skipping the process- and package-init tax.
type SessionIO struct {
	Command string    // the -c command string
	Dir     string    // working directory ("" = server cwd)
	Env     []string  // caller environment (replaces os.Environ())
	Stdin   io.Reader // caller stdin
	Stdout  io.Writer // caller stdout
	Stderr  io.Writer // caller stderr
}

// SessionWireExec is the per-session AgentOS execution wiring seam. It has the
// same shape as AgentOSWireExec, but lets embedders avoid process-global hooks.
type SessionWireExec func([]interp.RunnerOption, bool, []string, io.Reader, io.Writer, io.Writer) []interp.RunnerOption

// SessionConfig supplies all shell-flavor choices which were historically
// read from CLI package globals. The command-line server uses the global-backed
// defaults below; embedding packages should pass explicit values.
type SessionConfig struct {
	WireExec               SessionWireExec
	Preamble               func() string
	SuppressedForkBuiltins []string
	// ExecProcessGroups starts each external command in its own process
	// group so a cancelled request (a harness wall time) kills the command's
	// whole tree, not only its direct child — see [interp.ExecProcessGroups].
	ExecProcessGroups bool
}

func defaultSessionWireExec(opts []interp.RunnerOption, _ bool, _ []string, stdin io.Reader, stdout, stderr io.Writer) []interp.RunnerOption {
	return append(opts, interp.StdIO(stdin, stdout, stderr))
}

func commandLineSessionConfig() SessionConfig {
	return SessionConfig{
		WireExec:               AgentOSWireExec,
		Preamble:               AgentOSPreamble,
		SuppressedForkBuiltins: SuppressedForkBuiltins,
	}
}

// NewSessionRunner builds a runner for one warm-session request. It mirrors
// newRunner's `-c` command path faithfully — same coreutils userland wiring
// (AgentOSWireExec), same bash version vars, PATH default, exported-function
// import and AgentOS preamble — but takes its stdio/env/dir explicitly instead
// of reading process globals, so a single process can serve many callers.
func NewSessionRunner(io SessionIO) (*interp.Runner, error) {
	return NewSessionRunnerWithConfig(io, commandLineSessionConfig())
}

// NewSessionRunnerWithConfig is NewSessionRunner with request-local shell
// wiring. It never consults the mutable AgentOS CLI hook globals.
func NewSessionRunnerWithConfig(io SessionIO, config SessionConfig) (*interp.Runner, error) {
	wireExec := config.WireExec
	if wireExec == nil {
		wireExec = defaultSessionWireExec
	}
	preamble := config.Preamble
	if preamble == nil {
		preamble = func() string { return "" }
	}
	startupPosix := startupPosixForEnv(io.Env)
	lookup := func(name string) (string, bool) {
		for _, entry := range io.Env {
			if key, value, ok := strings.Cut(entry, "="); ok && key == name {
				return value, true
			}
		}
		return "", false
	}
	resolution, err := front.ResolveBashPP(front.BashPPSelector{
		Binary: front.BashPPBinaryBashy, Args: []string{"bashy", "-c", io.Command},
		LookupEnv: lookup, Posix: startupPosix,
	})
	if err != nil {
		return nil, err
	}
	// SHLVL from the CALLER's environment (not the serve process).
	shlvl := 0
	for _, kv := range io.Env {
		if strings.HasPrefix(kv, "SHLVL=") {
			fmt.Sscanf(kv[len("SHLVL="):], "%d", &shlvl)
		}
	}
	shlvl++

	envVars := make([]string, 0, len(io.Env)+2)
	envVars = append(envVars, shellStartupEnv(io.Env)...)
	if !hasEnvKey(io.Env, "POSIXLY_CORRECT") && hasEnvKey(io.Env, "POSIX_PEDANTIC") {
		envVars = append(envVars, "POSIXLY_CORRECT=y")
	}
	envVars = append(envVars, fmt.Sprintf("SHLVL=%d", shlvl))
	if !hasEnvKey(io.Env, "PATH") {
		envVars = append(envVars, "PATH="+defaultPathValue)
	}
	env := withBashVersionVars(withStartupShellVar(expand.ListEnviron(envVars...)), io.Env)

	var r *interp.Runner
	opts := []interp.RunnerOption{
		interp.Lang(resolution.LangVariant()),
		interp.Interactive(false),
		interp.CommandString(true),
		interp.StandardInput(false),
		interp.Env(env),
		interp.GoSourceEnv(io.Env),
		interp.WithBashCompatErrors(true),
		interp.PromptExpand(func(s string) string {
			return expandPrompt(s, func(name string) string { return r.Env.Get(name).String() }, 0, 0, startupPosix)
		}),
	}
	if io.Dir != "" {
		opts = append(opts, interp.Dir(io.Dir))
	}
	if startupPosix {
		opts = append(opts, interp.Params("-o", "posix"))
	}
	// Same in-process coreutils + code-intel userland the cold path gets.
	opts = wireExec(opts, startupPosix, io.Env, io.Stdin, io.Stdout, io.Stderr)
	if config.ExecProcessGroups {
		opts = append(opts, interp.ExecProcessGroups(true))
	}
	if len(config.SuppressedForkBuiltins) > 0 {
		opts = append(opts, interp.WithDisabledBuiltins(config.SuppressedForkBuiltins...))
	}
	r, err = interp.New(opts...)
	if err != nil {
		return nil, err
	}
	importBashFuncs(r)
	registerDefaultFuncs(r, preamble())
	return r, nil
}

// RunSessionCommand parses and runs the session request's command string in a
// freshly built runner, returning the shell exit status. It is the warm-session
// analogue of the cold `-c` path.
func RunSessionCommand(ctx context.Context, io SessionIO) int {
	return RunSessionCommandWithConfig(ctx, io, commandLineSessionConfig())
}

// RunSessionCommandWithConfig is RunSessionCommand with explicit per-request
// shell wiring, suitable for embedders which cannot depend on CLI globals.
func RunSessionCommandWithConfig(ctx context.Context, io SessionIO, config SessionConfig) int {
	return RunSessionCommandResultWithConfig(ctx, io, config).ExitCode
}

// SessionResult is the non-lossy termination result of a session command.
type SessionResult struct {
	ExitCode int
	Signaled bool
	Signal   string
}

// RunSessionCommandResultWithConfig is RunSessionCommandWithConfig with typed
// foreground-signal information retained for in-process embedders.
func RunSessionCommandResultWithConfig(ctx context.Context, io SessionIO, config SessionConfig) SessionResult {
	r, err := NewSessionRunnerWithConfig(io, config)
	if err != nil {
		fmt.Fprintln(io.Stderr, "bashy: session:", err)
		return SessionResult{ExitCode: 1}
	}
	if err := interp.WithBashSource([]byte(io.Command))(r); err != nil {
		fmt.Fprintln(io.Stderr, "bashy:", err)
		return SessionResult{ExitCode: 1}
	}
	runErr := runStatementStream(ctx, r, []byte(io.Command), r.LangVariant(), "bashy")
	if signaled, ok := r.LastSignaledStatus(); ok {
		return SessionResult{ExitCode: int(signaled.Status), Signaled: true, Signal: signaled.SignalName}
	}
	var es interp.ExitStatus
	if errors.As(runErr, &es) {
		return SessionResult{ExitCode: int(es)}
	}
	if runErr != nil {
		return SessionResult{ExitCode: 1}
	}
	return SessionResult{}
}

// hasEnvKey reports whether env contains a KEY=… entry for the given key.
func hasEnvKey(env []string, key string) bool {
	pfx := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, pfx) {
			return true
		}
	}
	return false
}
