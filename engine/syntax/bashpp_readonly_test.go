// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPDeepReadonlyFixtureParses(t *testing.T) {
	const src = `type Metadata struct { Name string }
type Config struct { Meta Metadata; Ports []int; Labels map[string]string }
cfg := Config{
    Meta: Metadata{Name: "prod"},
    Ports: []int{80, 443},
    Labels: map[string]string{"tier": "edge"},
}
readonly cfg
alias := cfg
printf '%s:%d:%s\n' alias.Meta.Name alias.Ports[1] alias.Labels["tier"]
`
	for _, mode := range []struct {
		name string
		read func() io.Reader
	}{
		{"whole", func() io.Reader { return strings.NewReader(src) }},
		{"one-byte", func() io.Reader { return iotest.OneByteReader(strings.NewReader(src)) }},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f, err := NewParser(Variant(LangBashPP)).Parse(mode.read(), "deep-read.bpp")
			if err != nil {
				t.Fatal(err)
			}
			if len(f.Stmts) != 6 {
				t.Fatalf("got %d statements, want 6", len(f.Stmts))
			}
			for _, index := range []int{0, 1} {
				decl, ok := f.Stmts[index].Cmd.(*BashPPDecl)
				if !ok || decl.DeclType.Value != "struct" || len(decl.StructFields) == 0 ||
					!decl.Lbrace.IsValid() || !decl.Rbrace.IsValid() {
					t.Fatalf("statement %d = %#v, want positioned struct declaration", index, f.Stmts[index].Cmd)
				}
			}
			if _, ok := f.Stmts[2].Cmd.(*BashPPShortDecl); !ok {
				t.Fatalf("composite short declaration = %T", f.Stmts[2].Cmd)
			}
			if _, ok := f.Stmts[4].Cmd.(*BashPPShortDecl); !ok {
				t.Fatalf("alias short declaration = %T", f.Stmts[4].Cmd)
			}
			seenDecls, seenFields := 0, 0
			Walk(f, func(node Node) bool {
				switch node.(type) {
				case *BashPPDecl:
					seenDecls++
				case *BashPPField:
					seenFields++
				}
				return true
			})
			if seenDecls != 2 || seenFields != 4 {
				t.Fatalf("Walk saw %d declarations and %d fields", seenDecls, seenFields)
			}
			var printed strings.Builder
			if err := NewPrinter().Print(&printed, f); err != nil {
				t.Fatal(err)
			}
			again, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(printed.String()), "")
			if err != nil {
				t.Fatalf("printer output does not parse: %v\n%s", err, printed.String())
			}
			var second strings.Builder
			_ = NewPrinter().Print(&second, again)
			if second.String() != printed.String() {
				t.Fatalf("printer is not stable:\nfirst  %q\nsecond %q", printed.String(), second.String())
			}
		})
	}
}

func TestBashPPReadonlyMutationAST(t *testing.T) {
	for _, src := range []string{`cfg = map[string]int{"port": 443}`, `cfg["nested"]["port"] = 443`, `cfg["ports"][0] = 8080`, `cfg.Name = "dev"`} {
		t.Run(src, func(t *testing.T) {
			for _, oneByte := range []bool{false, true} {
				var reader io.Reader = strings.NewReader(src)
				if oneByte {
					reader = iotest.OneByteReader(reader)
				}
				f, err := NewParser(Variant(LangBashPP)).Parse(reader, "")
				if err != nil {
					t.Fatal(err)
				}
				assign, ok := f.Stmts[0].Cmd.(*BashPPAssign)
				if !ok {
					t.Fatalf("got %T", f.Stmts[0].Cmd)
				}
				if assign.Pos().Offset() != 0 || assign.End().Offset() != uint(len(src)) || assign.Eq.Offset() != uint(strings.Index(src, "=")) {
					t.Fatalf("positions = [%d,%d), eq %d", assign.Pos().Offset(), assign.End().Offset(), assign.Eq.Offset())
				}
				visited := 0
				Walk(assign, func(Node) bool { visited++; return true })
				if visited < 3 {
					t.Fatalf("Walk visited only %d nodes", visited)
				}
				var out strings.Builder
				_ = NewPrinter().Print(&out, f)
				if out.String() != src+"\n" {
					t.Fatalf("print = %q", out.String())
				}
			}
		})
	}
}

func TestBashPPReadonlyNearMissStaysShell(t *testing.T) {
	bashppCheckIdentical(t, "read-only cfg")
	bashppCheckDiagnostic(t, "x := map[string]int{\necho ordinary\n", "invalid := binding")
	bashppCheckDiagnostic(t, "x := map[string]int{\necho ordinary\n}\n", "invalid := binding")
}
