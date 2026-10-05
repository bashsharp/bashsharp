// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package front

import (
	"bytes"
	"io"
	"text/scanner"
)

// LeadingGoPackage reports whether src opens with a Go package clause. The
// standard scanner only selects the front end; it neither rewrites the input
// nor implements Go grammar, and the selected front end owns every diagnostic.
func LeadingGoPackage(src []byte) bool {
	var scan scanner.Scanner
	scan.Init(bytes.NewReader(src))
	scan.Mode = scanner.ScanIdents | scanner.ScanComments | scanner.SkipComments
	scan.Error = func(*scanner.Scanner, string) {}
	return scan.Scan() == scanner.Ident && scan.TokenText() == "package"
}

// IsGoUnit reports whether the collected input is one Go compilation unit.
func (in GoSourceInput) IsGoUnit() bool {
	return len(in.Files) == 1 && LeadingGoPackage(in.Files[0].Data)
}

// ShellCheck is the Bash# semantic check of one shell program. A binary wires
// it (transpile does); nil means "no Bash# front end in this build" and is a
// refusal, never a silent pass.
var ShellCheck func(GoSourceInput) error

// CheckShellInput runs [ShellCheck]: the Bash# parse plus the engine's static
// obligations, with no runner built, so nothing in the program is executed.
func CheckShellInput(in GoSourceInput) error {
	if len(in.Files) != 1 {
		return Errorf("--check: expected exactly one input")
	}
	if ShellCheck == nil {
		return Errorf("--check: the Bash# shell check is not available in this build")
	}
	return ShellCheck(in)
}

// VerbatimError makes an error whose text is already final: it is printed
// as-is, with no program-name prefix, like the Go front end's file:line:col
// diagnostics.
func VerbatimError(msg string, err error) error {
	return &goSourceError{msg: msg, err: err}
}

// CollectCheckInput collects the one input of a content-selected --check.
// stdin is read only when no -c was given and the operand is absent or "-";
// an explicit empty -c is an empty program and never touches stdin.
func CollectCheckInput(operand, command string, commandSet bool, stdin io.Reader) (GoSourceInput, error) {
	if commandSet {
		if command == "" {
			return GoSourceInput{Files: []GoSourceFile{{Name: "-c"}}, Dir: goSourceWorkingDir()}, nil
		}
		return CollectGoSources(GoSourceResolution{}, "", command, nil)
	}
	if operand != "" && operand != "-" {
		stdin = nil
	}
	return CollectGoSources(GoSourceResolution{}, operand, "", stdin)
}
