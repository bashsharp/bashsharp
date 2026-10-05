// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

package transpile

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/front"
	"mvdan.cc/sh/v3/lower"
	"mvdan.cc/sh/v3/syntax"
)

func init() { front.ShellCheck = checkShell }

// checkShell is the semantic check of a Bash# shell program: the Bash#
// grammar parse followed by the engine's static obligations
// (lower.CheckBashSharp). It builds no runner. Diagnostics are rendered
// file:line:col as final text.
func checkShell(in front.GoSourceInput) error {
	name, data := in.Files[0].Name, in.Files[0].Data
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(bytes.NewReader(data), name)
	if err != nil {
		return front.VerbatimError(err.Error(), err)
	}
	err = lower.CheckBashSharp(file)
	if err == nil {
		return nil
	}
	var list lower.ErrorList
	if !errors.As(err, &list) {
		return front.VerbatimError(fmt.Sprintf("%s: %v", name, err), err)
	}
	lines := make([]string, len(list))
	for i, d := range list {
		lines[i] = fmt.Sprintf("%s:%d:%d: %s: %s", name, d.Pos.Line(), d.Pos.Col(), d.Code, d.Msg)
	}
	return front.VerbatimError(strings.Join(lines, "\n"), err)
}
