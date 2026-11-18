// Package lower emits ordinary Go from the positioned Bash++ syntax tree.
package lower

import (
	"fmt"
	"go/types"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const DefaultRuntime = "github.com/bashsharp/bashsharp/engine/lower/shellrt"
const (
	CodeUnsupported = "LOWER-EUNSUPPORTED"
	CodeType        = "LOWER-ETYPE"
	CodeUndefined   = "LOWER-EUNDEFINED"
	CodeExpr        = "LOWER-EEXPR"
	CodeResult      = "LOWER-ERESULT"
	CodeBridge      = "LOWER-EBRIDGE"
)

type Options struct {
	Package, Runtime, Origin string
	// Library emits one runtime-free Go file per original Go source file.
	// It requires a Go-source tree and an empty Entry. Package must name the
	// source package; callers lower an external test package as a separate unit.
	Library bool
	// Dir is the source directory used to resolve module imports.
	// Empty uses the current working directory; Origin remains source identity.
	Dir string
	// Entry optionally names an exported entry accepting runtime SessionOptions.
	// Empty preserves the hygienic private entry and runtime-free native units.
	Entry string
	// Importer, when set, replaces the module importer built from Dir, so a
	// caller that type-checked the source against an explicit package map
	// (gosource.Options.Packages) can lower it against the same map. Nil
	// keeps the on-disk module/GOPATH policy.
	Importer types.Importer
}
type Result struct {
	Sources []syntax.SourceFile
	Source  []byte
	Package string
	Entry   string // emitted callable entry name, or empty for a runtime-free unit
	Imports []string
	// ForeignImports are immutable lazy package plans. Compilation selects an
	// environment but never imports the referenced modules.
	ForeignImports []polyglot.ImportPlan
	Origin         string
	Mappings       []Mapping
	// Files holds per-origin output when Options.Library is set. Source is empty
	// in that mode so a caller cannot accidentally compile a partial flat unit.
	Files []FileResult
}

// FileResult is one generated file in a library result.
type FileResult struct {
	Name     string
	Source   []byte
	Imports  []string
	Mappings []Mapping
}
type Mapping struct {
	Source        string
	SourceOffset  uint
	GoLine, GoCol int
	Pos           syntax.Pos
	Node          string
}

func (r *Result) LookupLine(line int) (Mapping, bool) {
	for _, m := range r.Mappings {
		if m.GoLine == line {
			return m, true
		}
	}
	return Mapping{}, false
}

type Diagnostic struct {
	Code, Msg, Node string
	Source          string
	Pos             syntax.Pos

	// Text, when non-empty, is the exact public rendering of this diagnostic,
	// including any `<origin>: line N: ` prefix it carries.
	//
	// It exists because the source surface is not uniform: some diagnostics are
	// written with an origin/line prefix and some without, and one of them
	// carries no code at all. Reconstructing that from Code/Msg/Pos would mean
	// either forging a code or encoding per-diagnostic rendering rules in every
	// consumer. A formatter that wants the source bytes prints Text; a
	// formatter that wants the lowering rendering ignores it and keeps using
	// Code, Msg and Pos, which are always populated as before.
	Text string
}

func (d Diagnostic) Error() string {
	if d.Text != "" {
		return d.Text
	}
	if d.Source != "" {
		return fmt.Sprintf("%s:%d:%d: %s: %s", d.Source, d.Pos.Line(), d.Pos.Col(), d.Code, d.Msg)
	}
	return fmt.Sprintf("%d:%d: %s: %s", d.Pos.Line(), d.Pos.Col(), d.Code, d.Msg)
}

type ErrorList []Diagnostic

func (e ErrorList) Error() string {
	a := make([]string, len(e))
	for i, d := range e {
		a[i] = d.Error()
	}
	return strings.Join(a, "\n")
}
