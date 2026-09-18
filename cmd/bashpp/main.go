// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

// Command bashpp is the Bash++ language's own front door: it runs a Bash++
// program (a .bpp script, -c, stdin, or an unchanged Go program under
// --source=go), checks it, lists its import resolutions, or transpiles it to
// ordinary Go — over the sh engine alone, with no AgentOS surface. It takes
// the same invocation vocabulary bashy fronts, so a harness pinned to
// `bashy --bashpp --source=go …` can point BASHPP_TOOL here unchanged.
//
//	bashpp [--bashpp] [--source=go] [go-source flags] [-c CMD | FILE] [ARGS...]
//	bashpp transpile --bashpp [--source=go] INPUT -o OUTPUT.go [--map MAP]
//	bashpp --version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/qiangli/bashpp/front"
	"github.com/qiangli/bashpp/transpile"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// version is the language front's own version; the commit comes from the
// build's VCS stamp so `go build` in a checkout needs no ldflags.
const version = "0.1.0-dev"

func main() {
	front.Prog = "bashpp"
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) > 1 {
		switch args[1] {
		case "--version":
			fmt.Printf("bashpp, Bash++ (GNU Bash 5.3 superset), version %s (%s)\n", version, commit())
			return 0
		case "transpile":
			return transpile.Main(args[2:])
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		}
	}
	rest, sel, err := front.StripGoSourceInvocationFlags(args)
	if err != nil {
		return failure(err)
	}
	var (
		command    string
		commandSet bool
		noExec     bool
		operands   []string
	)
	for i := 1; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "--":
			operands = append(operands, rest[i+1:]...)
			i = len(rest)
		case arg == "-c":
			if i+1 >= len(rest) {
				return failure(front.Errorf("-c: option requires an argument"))
			}
			command, commandSet = rest[i+1], true
			operands = append(operands, rest[i+2:]...)
			i = len(rest)
		case arg == "-n":
			noExec = true
		case arg == "--bashpp", arg == "--bash++":
			// Bash++ is this program's only dialect; the selector is accepted
			// so bashy-shaped invocations run unchanged.
		case arg == "--no-bashpp":
			return failure(front.Errorf("--no-bashpp: this is the Bash++ front door; use bash or bashy for Classic"))
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			operands = append(operands, rest[i:]...)
			i = len(rest)
		default:
			return failure(front.Errorf("%s: unknown option", arg))
		}
	}
	operand := ""
	if !commandSet && len(operands) > 0 {
		operand, operands = operands[0], operands[1:]
	}
	res, err := front.ResolveGoSource(sel, front.GoSourceContext{
		Binary:     front.BashPPBinaryBashy,
		BashPP:     true,
		HasOperand: operand != "",
	})
	if err != nil {
		return failure(err)
	}
	if res.Enabled {
		return runGoSource(res, operand, command, commandSet, noExec, operands)
	}
	return runShell(operand, command, commandSet, noExec, operands)
}

// runShell parses and runs a Bash++ program from a file, -c or stdin.
func runShell(operand, command string, commandSet, noExec bool, args []string) int {
	var (
		src  io.Reader
		name string
	)
	switch {
	case commandSet:
		src, name = strings.NewReader(command), "bashpp"
		if len(args) > 0 {
			name, args = args[0], args[1:]
		}
	case operand != "" && operand != "-":
		f, err := os.Open(operand)
		if err != nil {
			return failure(err)
		}
		defer f.Close()
		src, name = f, operand
	default:
		src, name = os.Stdin, "bashpp"
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBashPP))
	if noExec {
		if _, err := parser.Parse(src, name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	r, err := newRunner(name, args)
	if err != nil {
		return failure(err)
	}
	// Like bash (and bashy's stream loop), the program runs statement by
	// statement: a statement's runtime error is reported by the engine and
	// the script goes on, its exit status being the LAST statement's; a
	// syntax error is reported where it is reached, after everything before
	// it has run. bashy's bash-format diagnostics and its recovery past a
	// syntax error are its own Bash 5.3 compatibility layer, not this front's.
	ctx := context.Background()
	r.Reset()
	var runErr error
	for stmt, perr := range parser.StmtsSeq(src) {
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			return 2
		}
		runErr = r.Run(ctx, stmt)
		if r.Exited() {
			break
		}
	}
	if err := r.Run(ctx, &syntax.File{}); err != nil && runErr == nil {
		runErr = err
	}
	return exit(runErr)
}

// runGoSource mirrors bashy's --source=go path: collect the original bytes,
// load them through the Go front end, and run the positioned Bash++ AST.
func runGoSource(res front.GoSourceResolution, operand, command string, commandSet, noExec bool, args []string) int {
	var stdin io.Reader
	if operand == "" && !commandSet && len(res.Files) == 0 {
		stdin = os.Stdin
	}
	in, err := front.CollectGoSources(res, operand, command, stdin)
	if err != nil {
		return failure(err)
	}
	noExec = noExec || res.Check
	packages, err := front.ReadGoSourcePackages(res.Packages)
	if err != nil {
		return failure(err)
	}
	prog, err := front.LoadGoSource(in, front.GoSourceOptions{
		RunMain: !noExec, Dir: in.Dir, GoVersion: res.GoVersion, TestBuiltins: res.TestBuiltins,
		CheckerBranchErrors: res.CheckerBranchErrors, CheckAfterSyntaxErrors: res.CheckAfterSyntaxErrors,
		Packages: packages, ImportBase: res.ImportBase, ImportPath: res.ImportPath, TestMain: res.TestMain,
	})
	if err != nil {
		// The front end's diagnostic is printed verbatim: its file:line:col
		// text is what a differential harness compares against Go's.
		if front.IsGoSourceError(err) {
			return failure(err)
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if res.List {
		if err := front.WriteGoSourceList(os.Stdout, prog.Resolutions); err != nil {
			return failure(err)
		}
		return 0
	}
	if noExec {
		return 0
	}
	argv0 := in.Files[0].Name
	if commandSet && len(args) > 0 {
		argv0, args = args[0], args[1:]
	}
	r, err := newRunner(argv0, args)
	if err != nil {
		return failure(err)
	}
	if front.GoSourceModuleDir != nil && in.Dir != "" {
		if err := front.GoSourceModuleDir(in.Dir)(r); err != nil {
			return failure(err)
		}
	}
	if res.ImportPath != "" {
		if err := interp.GoSourceIdentity(res.ImportPath, res.TestMain)(r); err != nil {
			return failure(err)
		}
	}
	r.Reset()
	return exit(r.Run(context.Background(), prog.File))
}

func newRunner(argv0 string, args []string) (*interp.Runner, error) {
	environ := os.Environ()
	opts := []interp.RunnerOption{
		interp.Lang(syntax.LangBashPP),
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.Env(expand.ListEnviron(environ...)),
		interp.GoSourceEnv(environ),
		interp.WithBashCompatErrors(true),
		interp.WithSignalResetter(interp.OSSignalResetter{}),
		interp.WithStandaloneSignalDefaults(),
		interp.WithArgv0(argv0),
	}
	if len(args) > 0 {
		opts = append(opts, interp.Params(append([]string{"--"}, args...)...))
	}
	return interp.New(opts...)
}

// exit maps a run result onto the process status the way a shell does.
func exit(err error) int {
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

// failure reports a refusal and returns bash's usage-error status.
func failure(err error) int {
	msg := err.Error()
	if !front.IsGoSourceError(err) && !strings.HasPrefix(msg, front.Prog+": ") {
		msg = front.Prog + ": " + msg
	}
	fmt.Fprintln(os.Stderr, msg)
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	return 2
}

func commit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "unknown"
}

const usage = `usage: bashpp [--bashpp] [--source=go] [go-source flags] [-n] [-c CMD | FILE] [ARGS...]
       bashpp transpile --bashpp [--source=go] INPUT -o OUTPUT.go [--map MAP]
       bashpp --version

Runs a Bash++ program: a .bpp script, a -c command, stdin, or — with
--source=go — an unchanged Go program. --check validates without running;
--go-list prints the import resolutions. transpile lowers to ordinary Go.
`
