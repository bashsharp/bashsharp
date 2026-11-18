// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

func TestBashPPTypeDeclClassified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, name, typ        string
		alias                bool
		start, end           uint
		nameStart, typeStart uint
	}{
		{"type T int", "T", "int", false, 0, 10, 5, 7},
		{"type ID = string", "ID", "string", true, 0, 16, 5, 10},
		{"echo before\ntype LongName = pkgType", "LongName", "pkgType", true, 12, 35, 17, 28},
		{"type A [2+1]int", "A", "[2+1]int", false, 0, 15, 5, 7},
		{"type S = []string", "S", "[]string", true, 0, 17, 5, 9},
		{"type M map[string][]int", "M", "map[string][]int", false, 0, 23, 5, 7},
		{"type P *Point", "P", "*Point", false, 0, 13, 5, 7},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			for _, posix := range []bool{false, true} {
				for _, mode := range bashppReadModes {
					f, err := bashppParseAs(LangBashPP, tc.in, posix, mode.wrap)
					if err != nil {
						t.Fatalf("parse (%s, posix=%v): %v", mode.name, posix, err)
					}
					d := bashppLastDecl(t, f)
					if d.Site != StartTypeDecl || d.Kw.Value != "type" || d.Name.Value != tc.name ||
						d.DeclType == nil || d.DeclType.Value != tc.typ || d.Alias != tc.alias || len(d.Init) != 0 {
						t.Fatalf("declaration = %#v", d)
					}
					spans := []struct {
						name       string
						n          Node
						start, end uint
					}{
						{"declaration", d, tc.start, tc.end},
						{"keyword", d.Kw, tc.start, tc.start + 4},
						{"name", d.Name, tc.nameStart, tc.nameStart + uint(len(tc.name))},
						{"underlying type", d.DeclType, tc.typeStart, tc.typeStart + uint(len(tc.typ))},
					}
					for _, span := range spans {
						if gotStart, gotEnd := span.n.Pos().Offset(), span.n.End().Offset(); gotStart != span.start || gotEnd != span.end {
							t.Errorf("%s span = [%d,%d), want [%d,%d)", span.name, gotStart, gotEnd, span.start, span.end)
						}
					}
					var out strings.Builder
					if err := NewPrinter().Print(&out, f); err != nil || out.String() != tc.in+"\n" {
						t.Errorf("print = %q, %v; want %q", out.String(), err, tc.in+"\n")
					}
				}
			}
		})
	}
}

func TestBashPPTypeDeclFallback(t *testing.T) {
	t.Parallel()
	cases := []string{
		"type if int", "type T return",
		"type T =", "type T == int", "type T = = int", "type T int extra", "type T = int extra",
		"type T struct {",
		// Comma-separated members are not the accepted Bash# enum grammar;
		// they remain an ordinary shell command rather than being guessed at.
		"type Color enum { Red, Green }",
		"type T int >out", ">out type T int", "e=1 type T int",
		"type $name int", "type T $underlying", "type T ${underlying}", "type T $(underlying)",
		`type "T" int`, `type T "int"`, "type T *", "type T a/b", "type T int=other",
	}
	for _, keyword := range bashppGoKeywords {
		cases = append(cases, "type "+keyword+" int", "type T "+keyword)
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			bashppCheckIdentical(t, in)
		})
	}
}
