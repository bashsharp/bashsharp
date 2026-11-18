package lower_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The public selector-key negatives. Each program is rejected by the source
// engine before it produces output, so the compiler must reject it with the
// same bytes rather than with a lowering-shaped position/code rendering.
//
// No expectation below is written down: every case is compared against what
// the interpreter in this checkout prints for the same source, so a wording,
// code or prefix change in interp/bashpp_struct.go moves both sides together.
var promotedSelectorKeyDiagnosticCases = []struct {
	id, origin, source string

	// promoted marks the cases the promoted-literal lowering seam itself
	// rejects (lower/promoted_literal.go). The rest reach the structural
	// profile check first and are here to pin that this file did not change
	// their rendering.
	promoted bool
}{
	{
		id:       "ambiguous",
		origin:   "selector-key.bpp",
		promoted: true,
		source: `type A struct { X int }
type B struct { X int }
type Outer struct { A; B }
func main() {
 bad := Outer{X: 1}
 printf '%s' "$bad"
}
main()
`,
	},
	{
		id:       "pointer traversal",
		origin:   "selector-key.bpp",
		promoted: true,
		source: `type Leaf struct { X int }
type Outer struct { *Leaf }
func main() {
 bad := Outer{X: 1}
 printf '%s' "$bad"
}
main()
`,
	},
	{
		id:       "parent then promoted",
		origin:   "selector-key.bpp",
		promoted: true,
		source: `type Leaf struct { X int }
type Outer struct { Leaf }
func main() {
 bad := Outer{Leaf: Leaf{}, X: 1}
 printf '%s' "$bad"
}
main()
`,
	},
	{
		id:       "promoted then parent",
		origin:   "selector-key.bpp",
		promoted: true,
		source: `type Leaf struct { X int }
type Outer struct { Leaf }
func main() {
 bad := Outer{X: 1, Leaf: Leaf{}}
 printf '%s' "$bad"
}
main()
`,
	},
	{
		// The passing control: rejected by the profile check, already exact.
		id:     "dotted key is not Go syntax",
		origin: "selector-key.bpp",
		source: `type Leaf struct { X int }
type Outer struct { Leaf }
func main() {
 bad := Outer{Leaf.X: 1}
 printf '%s' "$bad"
}
main()
`,
	},
	{
		// The negative control for the prefix itself. BASHPP-ESTRUCT-MIXED is
		// the one keyed-literal rejection the source engine writes without an
		// origin/line prefix, so a blanket prefix here would break it.
		id:     "keyed and positional",
		origin: "selector-key.bpp",
		source: `type Outer struct { A int; B int }
func main() {
 bad := Outer{A: 1, 2}
 printf '%s' "$bad"
}
main()
`,
	},
}

func parsePromotedSelectorKey(t *testing.T, source, origin string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), origin)
	qt.Assert(t, qt.IsNil(err))
	return file
}

// runPromotedSelectorKeySource is the oracle: the source engine's own stderr
// and exit status for the program under test.
func runPromotedSelectorKeySource(t *testing.T, source, origin string) (string, string, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.WithBashCompatErrors(true),
		interp.StdIO(nil, &out, &stderr),
		interp.Dir(t.TempDir()),
		interp.Env(expand.ListEnviron("PATH=/no-tools")),
	)
	qt.Assert(t, qt.IsNil(err))
	status := 0
	if err := runner.Run(context.Background(), parsePromotedSelectorKey(t, source, origin)); err != nil {
		var exit interp.ExitStatus
		if !errors.As(err, &exit) {
			t.Fatalf("run %s: %v", origin, err)
		}
		status = int(exit)
	}
	return out.String(), stderr.String(), status
}

// renderPromotedSelectorKey is what a product formatter prints: one diagnostic
// per line, preferring the recorded public text.
func renderPromotedSelectorKey(list lower.ErrorList) string {
	var b strings.Builder
	for _, d := range list {
		b.WriteString(d.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// TestPromotedLiteralSelectorKeyDiagnosticText pins the whole public contract
// for these rejections: no result, the source engine's exact stderr bytes
// (origin, line, code and message), and the source engine's exit status.
func TestPromotedLiteralSelectorKeyDiagnosticText(t *testing.T) {
	for _, tc := range promotedSelectorKeyDiagnosticCases {
		t.Run(tc.id, func(t *testing.T) {
			stdout, stderr, status := runPromotedSelectorKeySource(t, tc.source, tc.origin)
			qt.Assert(t, qt.Equals(stdout, ""))
			qt.Assert(t, qt.Equals(status, 2))

			file := parsePromotedSelectorKey(t, tc.source, tc.origin)
			result, err := lower.Compile(file, lower.Options{Origin: tc.origin})

			// A rejected program yields diagnostics and no partial output.
			qt.Assert(t, qt.IsNil(result))
			var list lower.ErrorList
			qt.Assert(t, qt.ErrorAs(err, &list))
			qt.Assert(t, qt.Equals(len(list), 1))

			// The rendering a CLI performs must be the source bytes.
			qt.Assert(t, qt.Equals(renderPromotedSelectorKey(list), stderr))
			qt.Assert(t, qt.Equals(list[0].Text, strings.TrimSuffix(stderr, "\n")))

			// Code, Msg and Pos stay populated for consumers that keep using
			// the lowering rendering instead of Text.
			qt.Assert(t, qt.StringContains(stderr, list[0].Code+": "+list[0].Msg))
			qt.Assert(t, qt.IsTrue(list[0].Pos.IsValid()))
		})
	}
}

// TestPromotedLiteralSelectorKeyPrefixIsPerDiagnostic pins which of these
// rejections carry an `<origin>: line N: ` prefix. The source engine applies it
// through r.bashErrPrefix per diagnostic, so this is not a formatter-wide rule
// that could be moved into Diagnostic.Error.
func TestPromotedLiteralSelectorKeyPrefixIsPerDiagnostic(t *testing.T) {
	for _, tc := range promotedSelectorKeyDiagnosticCases {
		t.Run(tc.id, func(t *testing.T) {
			_, stderr, _ := runPromotedSelectorKeySource(t, tc.source, tc.origin)
			file := parsePromotedSelectorKey(t, tc.source, tc.origin)
			_, err := lower.Compile(file, lower.Options{Origin: tc.origin})
			var list lower.ErrorList
			qt.Assert(t, qt.ErrorAs(err, &list))
			qt.Assert(t, qt.Equals(len(list), 1))

			prefix := tc.origin + ": line "
			qt.Assert(t, qt.Equals(strings.HasPrefix(list[0].Text, prefix), strings.HasPrefix(stderr, prefix)))
			if strings.HasPrefix(stderr, prefix) {
				// The prefixed line number is the diagnostic's own line.
				qt.Assert(t, qt.StringContains(list[0].Text, prefix+strconv.Itoa(int(list[0].Pos.Line()))+": "))
			}
		})
	}
}

// TestPromotedLiteralSelectorKeyRejectsWithoutOrigin pins the empty-origin
// fallback: the source engine names an unnamed script "bash", and the compiler
// must not invent an empty prefix instead.
func TestPromotedLiteralSelectorKeyRejectsWithoutOrigin(t *testing.T) {
	for _, tc := range promotedSelectorKeyDiagnosticCases {
		if !tc.promoted {
			continue
		}
		t.Run(tc.id, func(t *testing.T) {
			_, stderr, _ := runPromotedSelectorKeySource(t, tc.source, "")
			file := parsePromotedSelectorKey(t, tc.source, "")
			result, err := lower.Compile(file, lower.Options{})
			qt.Assert(t, qt.IsNil(result))
			var list lower.ErrorList
			qt.Assert(t, qt.ErrorAs(err, &list))
			qt.Assert(t, qt.Equals(renderPromotedSelectorKey(list), stderr))
		})
	}
}
