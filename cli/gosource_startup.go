// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/scanner"

	"github.com/bashsharp/bashsharp/front"

	"github.com/qiangli/bash/interp"
)

// resolveStartupGoSource validates the captured selection once the Bash++ and
// POSIX profiles are known. It is called from runAll before any input is
// looked at, so a refusal cannot reach a shell code path.
func resolveStartupGoSource() error {
	if startupGoSourceErr != nil {
		return startupGoSourceErr
	}
	if !startupGoSourceSel.Requested() {
		return nil
	}
	binary := front.BashPPBinaryBash
	if AgentOSBashPPDefault {
		binary = front.BashPPBinaryBashy
	}
	res, err := front.ResolveGoSource(startupGoSourceSel, front.GoSourceContext{
		Binary: binary,
		BashPP: startupBashPP.Enabled,
		// Every spelling of the POSIX selection counts: the flag, `-o posix`,
		// the inherited environment, and the argv0=sh certification route.
		Posix:         commandLinePosixMode() || effectiveStartupPosix(),
		HasOperand:    *command == "" && !*readStdin && flag.NArg() > 0,
		ShellOnlyMode: startupShellOnlyMode(),
	})
	if err != nil {
		return err
	}
	startupGoSource = res
	return nil
}

// startupShellOnlyMode names the selected mode that only exists for shell
// input, or "" when none is selected. These modes build the SHELL parser
// before any input dispatch happens, which is how `--source=go --pretty-print
// x.go` used to report a shell parse error ("a command can only contain words
// and redirects") for a perfectly good Go program.
func startupShellOnlyMode() string {
	switch {
	case *pretty:
		return "--pretty-print"
	case *dumpPO:
		return "--dump-po-strings"
	case *dumpStrs || *dumpShort:
		return "--dump-strings"
	}
	return ""
}

// goSourceDiagnosticText renders one of Bashy's own Go-source errors: a
// refusal already names the shell, anything else (an unreadable file, say)
// gets the shell's name added.
func goSourceDiagnosticText(err error) string {
	msg := err.Error()
	if !front.IsGoSourceError(err) && !strings.HasPrefix(msg, "bashy: ") {
		msg = "bashy: " + msg
	}
	return msg
}

// goSourceFailure reports one of Bashy's own Go-source errors and exits 2,
// bash's usage-error status. There is no path from here back into shell
// parsing: a refused or unreadable Go input is the end of the invocation.
func goSourceFailure(err error) error {
	if err == nil {
		return nil
	}
	fmt.Fprintln(os.Stderr, goSourceDiagnosticText(err))
	return interp.ExitStatus(2)
}

// goSourceLoadFailure reports the FRONT END's diagnostic verbatim. Go's
// file:line:col text is what a differential harness compares against the Go
// toolchain, so Bashy adds no prefix that would have to be stripped again.
// Bashy's own refusals — notably "no front end in this build" — still get the
// shell's name, because they are not something the Go toolchain ever says.
func goSourceLoadFailure(err error) error {
	if front.IsGoSourceError(err) {
		return goSourceFailure(err)
	}
	fmt.Fprintln(os.Stderr, err)
	return interp.ExitStatus(2)
}

// packagePrefixReader limits scanner read-ahead to its current token. The
// captured bytes are replayed unchanged when the input belongs to the shell.
type packagePrefixReader struct {
	reader io.Reader
	prefix bytes.Buffer
	err    error
}

func (r *packagePrefixReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	n, err := r.reader.Read(p)
	r.prefix.Write(p[:n])
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func scanGoPackagePrefix(reader io.Reader) (bool, io.Reader, error) {
	prefix := &packagePrefixReader{reader: reader}
	var scan scanner.Scanner
	scan.Init(prefix)
	scan.Mode = scanner.ScanIdents | scanner.ScanComments | scanner.SkipComments
	scan.Error = func(*scanner.Scanner, string) {} // The selected frontend owns diagnostics.
	selected := scan.Scan() == scanner.Ident && scan.TokenText() == "package"
	return selected, io.MultiReader(bytes.NewReader(prefix.prefix.Bytes()), reader), prefix.err
}

// leadingGoPackage uses the standard scanner only to select the existing
// frontend; it neither rewrites input nor implements Go grammar.
func leadingGoPackage(src []byte) bool {
	selected, _, _ := scanGoPackagePrefix(bytes.NewReader(src))
	return selected
}

// collectPackageGoSource stops reading shell stdin as soon as its first token
// identifies it. Only a selected Go compilation unit is collected in full.
func collectPackageGoSource(operand, command string, stdin io.Reader) (front.GoSourceInput, io.Reader, bool, error) {
	if operand == "" && command == "" && stdin != nil {
		selected, replay, err := scanGoPackagePrefix(stdin)
		if err != nil || !selected {
			return front.GoSourceInput{}, replay, false, err
		}
		in, err := front.CollectGoSources(front.GoSourceResolution{}, "", "", replay)
		return in, nil, true, err
	}
	in, err := front.CollectGoSources(front.GoSourceResolution{}, operand, command, stdin)
	if err != nil {
		return front.GoSourceInput{}, stdin, false, err
	}
	return in, stdin, len(in.Files) == 1 && leadingGoPackage(in.Files[0].Data), nil
}

// runGoSourceInvocation runs one `--source=go` invocation end to end.
//
// Shell startup files are deliberately not loaded, and neither are the login
// shell's exit hooks: a Go program's semantics are the Go specification's, and
// a ~/.bashyrc that defined a function or an alias must not be able to change
// what it means — nor may a ~/.bash_logout append shell effects to it after
// main returns. `bashy --login --bashpp --source=go p.go` runs p.go, and
// nothing else.
func runGoSourceInvocation() error {
	operand := ""
	if *command == "" && !*readStdin && flag.NArg() > 0 && len(startupGoSource.Files) == 0 {
		operand = flag.Arg(0)
	}
	var stdin io.Reader
	if operand == "" && *command == "" && len(startupGoSource.Files) == 0 {
		stdin = os.Stdin
	}
	in, err := front.CollectGoSources(startupGoSource, operand, *command, stdin)
	if err != nil {
		return goSourceFailure(err)
	}
	return runGoSourceInput(in, operand)
}

// commandFlagGiven reports whether -c was spelled, including `-c ""`, which
// *command alone cannot distinguish from absence.
var commandFlagGiven = func() bool {
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == "c" })
	return given
}

// runContentCheck is --check with no --source: the input itself selects the
// language, a Go compilation unit through the Go front end and anything else
// through the Bash# shell check. Neither builds a runner or runs a body.
func runContentCheck() error {
	commandSet := commandFlagGiven()
	operand := ""
	if !commandSet && !*readStdin && flag.NArg() > 0 {
		operand = flag.Arg(0)
	}
	if !commandSet && operand != "" && operand != "-" {
		if info, err := os.Stat(operand); err == nil && info.IsDir() {
			return goSourceFailure(fmt.Errorf("--check: %s: is a directory; select Go packages with --source=go", operand))
		}
	}
	in, err := front.CollectCheckInput(operand, *command, commandSet, os.Stdin)
	if err != nil {
		return goSourceFailure(err)
	}
	if in.IsGoUnit() {
		startupGoSource = front.GoSourceResolution{Enabled: true, Check: true}
		return runGoSourceInput(in, operand)
	}
	if err := front.CheckShellInput(in); err != nil {
		return goSourceLoadFailure(err)
	}
	return nil
}

func runGoSourceInput(in front.GoSourceInput, operand string) error {
	// --check is semantic-only. `-n` / `-o noexec` means the same thing for
	// this input, so a build-only corpus row may spell either. RunMain stays
	// false so the loaded program carries no entry calls at all — the absence
	// of the calls, not a later branch, is what makes "executes nothing" true.
	noExec := startupGoSource.Check || cmdlineNoExec() || AgentOSCommandLineNoExec(resolvedStartupPosix())
	packages, err := front.ReadGoSourcePackages(startupGoSource.Packages)
	if err != nil {
		return goSourceFailure(err)
	}
	prog, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: !noExec, Dir: in.Dir, GoVersion: startupGoSource.GoVersion,
		TestBuiltins: startupGoSource.TestBuiltins, CheckerBranchErrors: startupGoSource.CheckerBranchErrors, CheckAfterSyntaxErrors: startupGoSource.CheckAfterSyntaxErrors,
		Packages: packages, ImportBase: startupGoSource.ImportBase, ImportPath: startupGoSource.ImportPath, TestMain: startupGoSource.TestMain})
	if err != nil {
		return goSourceLoadFailure(err)
	}
	if startupGoSource.List {
		if err := front.WriteGoSourceList(os.Stdout, prog.Resolutions); err != nil {
			return err
		}
		return nil
	}
	if noExec {
		return nil
	}
	r, err := newRunner()
	if err != nil {
		return err
	}
	argv0 := in.Files[0].Name
	posArgs := flag.Args()
	if operand != "" && len(posArgs) > 0 {
		// The operand is the program, not one of its arguments — the same
		// split the existing `--bashpp script args…` interface uses.
		posArgs = posArgs[1:]
	}
	if *command != "" && len(posArgs) > 0 {
		argv0 = posArgs[0]
		posArgs = posArgs[1:]
	}
	if err := interp.WithArgv0(argv0)(r); err != nil {
		return err
	}
	// The module context travels with the SOURCE, never with the working
	// directory. See [front.GoSourceModuleDir].
	if front.GoSourceModuleDir != nil && in.Dir != "" {
		if err := front.GoSourceModuleDir(in.Dir)(r); err != nil {
			return err
		}
	}
	// The program's DECLARED identity (and the test-main fact) travel to the
	// runtime resolver, where cmd/go's internal-visibility rule is decided
	// on them (Sprint 165 D8); without an identity nothing changes.
	if startupGoSource.ImportPath != "" {
		if err := interp.GoSourceIdentity(startupGoSource.ImportPath, startupGoSource.TestMain)(r); err != nil {
			return err
		}
	}
	if len(posArgs) > 0 {
		if err := interp.Params(append([]string{"--"}, posArgs...)...)(r); err != nil {
			return err
		}
	}
	r.Reset()
	return r.Run(context.Background(), prog.File)
}
