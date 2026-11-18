// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestExpandArithmText(t *testing.T) {
	env := ListEnviron("key=abc")
	cfg := &Config{Env: env}
	got, err := cfg.expandArithmText("assoc[$key]++")
	if err != nil {
		t.Fatal(err)
	}
	if want := "assoc[abc]++"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandedAssocSubscriptError(t *testing.T) {
	err := expandedAssocSubscriptError("assoc[x],b[$(echo uname >&2)]++")
	if err == nil {
		t.Fatal("expected error")
	}
	var arithErr *ArithmError
	if !errors.As(err, &arithErr) {
		t.Fatalf("got %T, want *ArithmError", err)
	}
	if got, want := arithErr.Text, `assoc[x],b[$(echo uname >&2)]++`; got != want {
		t.Fatalf("got text %q, want %q", got, want)
	}
	if got, want := err.Error(), `error token is "],b[$(echo uname >&2)"`; !strings.Contains(got, want) {
		t.Fatalf("got %q, want to contain %q", got, want)
	}

	err = expandedAssocSubscriptError("0],b[1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.As(err, &arithErr) {
		t.Fatalf("got %T, want *ArithmError", err)
	}
	if got, want := arithErr.Text, `0\],b\[1`; got != want {
		t.Fatalf("got text %q, want %q", got, want)
	}
	if got, want := err.Error(), `error token is "\],b\[1"`; !strings.Contains(got, want) {
		t.Fatalf("got %q, want to contain %q", got, want)
	}
}

// bash echoes an offending arithmetic token verbatim between literal
// double quotes without escaping control bytes. A token that runs to
// the end of a line (`2  # not a comment`) keeps its trailing newline
// rather than printing `\n`.
func TestArithmInvalidOperatorTokenKeepsNewline(t *testing.T) {
	p := syntax.NewParser()
	file, err := p.Parse(strings.NewReader("echo $((\n1 + 2  # not a comment\n))"), "")
	if err != nil {
		t.Fatal(err)
	}
	call := file.Stmts[0].Cmd.(*syntax.CallExpr)
	arithm := call.Args[1].Parts[0].(*syntax.ArithmExp)
	_, err = Arithm(&Config{Env: ListEnviron()}, arithm.X)
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "error token is \"# not a comment\n\""; !strings.Contains(got, want) {
		t.Fatalf("got %q, want to contain %q", got, want)
	}
	if strings.Contains(err.Error(), `\n`) {
		t.Fatalf("error token should keep a literal newline, got %q", err.Error())
	}
}
