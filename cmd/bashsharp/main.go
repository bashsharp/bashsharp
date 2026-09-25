// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

// Command bashsharp is the Bash# language's own front door: it runs a Bash++
// program (a .bsh script, -c, stdin, or an unchanged Go program under
// --source=go), checks it, lists its import resolutions, or transpiles it to
// ordinary Go — over the sh engine alone, with no AgentOS surface. It takes
// the same invocation vocabulary bashy fronts, so a harness pinned to
// `bashy --bashsharp --source=go …` can point BASHPP_TOOL here unchanged.
//
//	bashsharp [--bashsharp] [--source=go] [go-source flags] [-c CMD | FILE] [ARGS...]
//	bashsharp transpile --bashsharp [--source=go] INPUT -o OUTPUT.go [--map MAP] [--standalone [--force]]
//	bashsharp --version
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/bashsharp/bashsharp/front"
	"github.com/bashsharp/bashsharp/transpile"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// version is the language front's own version; the commit comes from the
// build's VCS stamp so `go build` in a checkout needs no ldflags.
const version = "0.1.0-dev"

func main() {
	front.Prog = "bashsharp"
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) > 1 {
		switch args[1] {
		case "--version":
			fmt.Printf("bashsharp, Bash# (GNU Bash 5.3 superset), version %s (%s)\n", version, commit())
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
		// sawDoubleDash: operands after `--` are program arguments even
		// beside --go-file (the refusal is for a second PROGRAM operand).
		sawDoubleDash bool
	)
	for i := 1; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "--":
			sawDoubleDash = true
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
		case arg == "--bashsharp", arg == "--bashpp", arg == "--bash++":
			// Bash# is this program's only dialect; the selector (and its
			// Bash++-era alias spellings) is accepted so bashy-shaped
			// invocations run unchanged.
		case arg == "--no-bashsharp", arg == "--no-bashpp":
			return failure(front.Errorf("%s: this is the Bash# front door; use bash or bashy for Classic", arg))
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			operands = append(operands, rest[i:]...)
			i = len(rest)
		default:
			return failure(front.Errorf("%s: unknown option", arg))
		}
	}
	// The program is the first operand — unless --go-file already named the
	// program's files, in which case every operand is a program ARGUMENT
	// (`--go-file=args.go -- arg1 arg2`), exactly as bashy reads them.
	operand := ""
	if !commandSet && len(sel.Files) == 0 && len(operands) > 0 {
		operand, operands = operands[0], operands[1:]
	}
	res, err := front.ResolveGoSource(sel, front.GoSourceContext{
		Binary:     front.BashPPBinaryBashy,
		BashPP:     true,
		HasOperand: !commandSet && len(operands) > 0 && !sawDoubleDash,
	})
	if err != nil {
		return failure(err)
	}
	if res.Enabled {
		return runGoSource(res, args, operand, command, commandSet, noExec, operands)
	}
	return runShell(operand, command, commandSet, noExec, operands)
}

// runShell parses and runs a Bash# program from a file, -c or stdin.
func runShell(operand, command string, commandSet, noExec bool, args []string) int {
	var (
		src  io.Reader
		name string
	)
	switch {
	case commandSet:
		src, name = strings.NewReader(command), "bashsharp"
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
		src, name = os.Stdin, "bashsharp"
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return failure(err)
	}
	parser := syntax.NewParser(syntax.Variant(syntax.LangBashPP))
	file, perr := parser.Parse(bytes.NewReader(data), name)
	if noExec {
		if perr != nil {
			fmt.Fprintln(os.Stderr, perr)
			return 2
		}
		return 0
	}
	r, err := newRunner(name, args)
	if err != nil {
		return failure(err)
	}
	// The engine reports diagnostics as `<name>: line N: …` and echoes the
	// offending source the way bash does when it knows the script's bytes.
	if err := interp.WithIncrementalFilename(name)(r); err != nil {
		return failure(err)
	}
	if err := interp.WithBashSource(data)(r); err != nil {
		return failure(err)
	}
	ctx := context.Background()
	r.Reset()
	if perr == nil {
		return exit(r.Run(ctx, file))
	}
	// Like bash, everything before a syntax error still runs; the error is
	// then reported where it is reached and the script ends with status 2.
	// bashy's bash-format diagnostics and its recovery PAST the error are its
	// own Bash 5.3 compatibility layer, not this front's.
	prefix := &syntax.File{Name: name}
	for stmt, err := range parser.StmtsSeq(bytes.NewReader(data)) {
		if err != nil {
			break
		}
		prefix.Stmts = append(prefix.Stmts, stmt)
	}
	if len(prefix.Stmts) > 0 {
		if err := r.Run(ctx, prefix); err != nil || r.Exited() {
			if r.Exited() {
				return exit(err)
			}
		}
	}
	fmt.Fprintln(os.Stderr, perr)
	return 2
}

// runGoSource mirrors bashy's --source=go path: collect the original bytes,
// load them through the Go front end, and run the positioned Bash++ AST.
func runGoSource(res front.GoSourceResolution, invocation []string, operand, command string, commandSet, noExec bool, args []string) int {
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
		CheckerBranchErrors: res.CheckerBranchErrors, CheckAfterSyntaxErrors: res.CheckAfterSyntaxErrors, GoTypesParserDiagnostics: res.GoTypesParserDiagnostics,
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
	var runnerOpts []interp.RunnerOption
	if res.TestMain {
		plan, err := currentGoSourceReexecPlan(invocation)
		if err != nil {
			return failure(err)
		}
		runnerOpts = append(runnerOpts, interp.GoSourceReexecPlan(plan...))
	}
	r, err := newRunner(argv0, args, runnerOpts...)
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

func newRunner(argv0 string, args []string, runnerOpts ...interp.RunnerOption) (*interp.Runner, error) {
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
	opts = append(opts, runnerOpts...)
	return interp.New(opts...)
}

// currentGoSourceReexecPlan returns the launcher argv used by generated Go
// test mains. Its final -- makes the interpreter append the child argv without
// re-parsing it as Bashsharp input.
func currentGoSourceReexecPlan(invocation []string) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	return goSourceReexecPlan(executable, invocation), nil
}

// goSourceReexecPlan preserves the Go source invocation that identifies the
// test program and its packages. The original test arguments are deliberately
// omitted: the runner appends its child argv after the final --.
func goSourceReexecPlan(executable string, invocation []string) []string {
	plan := []string{executable}
	for _, arg := range invocation[1:] {
		if arg == "--" {
			break
		}
		plan = append(plan, arg)
	}
	return append(plan, "--")
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

const usage = `usage: bashsharp [--bashsharp] [--source=go] [go-source flags] [-n] [-c CMD | FILE] [ARGS...]
       bashsharp transpile --bashsharp [--source=go] INPUT -o OUTPUT.go [--map MAP] [--standalone [--force]]
       bashsharp --version

Runs a Bash# program: a .bsh script, a -c command, stdin, or — with
--source=go — an unchanged Go program. --check validates without running;
--go-list prints the import resolutions. transpile lowers to ordinary Go.
`
