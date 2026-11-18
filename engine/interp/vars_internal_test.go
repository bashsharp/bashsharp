// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashExportedFuncValueHeredocRedirect(t *testing.T) {
	src := "fff() { ed ${TMPDIR}/foo <<ENDOFINPUT >/dev/null\n/^name/d\nw\nq\nENDOFINPUT\naa=1\n}\nf() { echo f-x; echo f-y; } >&2"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	fn, ok := file.Stmts[0].Cmd.(*syntax.FuncDecl)
	if !ok {
		t.Fatalf("parsed command is %T, want *syntax.FuncDecl", file.Stmts[0].Cmd)
	}
	got := bashExportedFuncValue("fff", fn.Body)
	if !strings.Contains(got, "ed ${TMPDIR}/foo <<ENDOFINPUT > /dev/null\n") {
		t.Fatalf("exported function value lost heredoc redirect line:\n%s", got)
	}
	if strings.Contains(got, "\\\n") {
		t.Fatalf("exported function value should not contain printer continuations:\n%s", got)
	}
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("fff "+got), ""); err != nil {
		t.Fatalf("exported function value does not reparse: %v\n%s", err, got)
	}

	fn, ok = file.Stmts[1].Cmd.(*syntax.FuncDecl)
	if !ok {
		t.Fatalf("parsed command is %T, want *syntax.FuncDecl", file.Stmts[1].Cmd)
	}
	got = bashExportedFuncValue("f", fn.Body)
	if !strings.HasSuffix(got, "} 1>&2") {
		t.Fatalf("exported function value lost function-level redirect:\n%s", got)
	}
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("f "+got), ""); err != nil {
		t.Fatalf("exported redirected function value does not reparse: %v\n%s", err, got)
	}
}
