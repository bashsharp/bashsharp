// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

// Mixed Bash++ reserves unquoted import. Only the supported interpreted
// string form is accepted; raw/shell quoting is a positioned import error.
// Quoting the command name or using command import preserves shell semantics.
// Selected Go compilation units separately keep Go raw-string semantics.
func TestBashPPImportClaimsOnlyInterpretedStrings(t *testing.T) {
	for _, test := range []struct {
		src    string
		claim  bool
		reason string
	}{
		{"import \"fmt\"\n", true, "interpreted string is the claimed Go form"},
		{"import `fmt`\n", false, "backquote is command substitution in shell"},
		{"import 'fmt'\n", false, "single quotes are shell quoting"},
		{"import $'fmt'\n", false, "ANSI-C quoting is shell quoting"},
		{"import \"fm\"\"t\"\n", false, "adjacent concatenation is not a Go import path"},
		{"import fmt\n", false, "a bare word is the documented near miss"},
		{"import neturl `net/url`\n", false, "an aliased raw string is not claimed either"},
	} {
		t.Run(test.reason, func(t *testing.T) {
			f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(test.src), "t")
			if !test.claim {
				assertImportReservedError(t, test.src, false)
				assertImportReservedError(t, test.src, true)
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", test.src, err)
			}
			_, claimed := f.Stmts[0].Cmd.(*BashPPImport)
			if claimed != test.claim {
				t.Fatalf("%q claimed=%v, want %v (%s)", test.src, claimed, test.claim, test.reason)
			}
		})
	}
}

// Grouped near misses also diagnose the reserved marker, while Classic's
// existing rejection remains independent of the Bash++ diagnostic.
func TestBashPPGroupedRawStringReservedError(t *testing.T) {
	for _, src := range []string{
		"import (\n\t`fmt`\n)\n",
		"import (\n\t'fmt'\n)\n",
	} {
		bashErr := parseErrText(t, LangBash, src)
		if bashErr == "" {
			t.Fatalf("%q: expected Classic Bash to reject the shape", src)
		}
		assertImportReservedError(t, src, false)
		assertImportReservedError(t, src, true)
	}
	// The interpreted form is the one Bash++ claims, and Classic still rejects it.
	const claimed = "import (\n\t\"fmt\"\n)\n"
	if got := parseErrText(t, LangBash, claimed); got == "" {
		t.Fatal("Classic Bash unexpectedly accepted a grouped import")
	}
	if got := parseErrText(t, LangBashPP, claimed); got != "" {
		t.Fatalf("Bash++ rejected its own grouped import: %v", got)
	}
}

func parseErrText(t *testing.T, lang LangVariant, src string) string {
	t.Helper()
	_, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "t")
	if err == nil {
		return ""
	}
	return err.Error()
}
