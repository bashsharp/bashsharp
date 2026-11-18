// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runBashPPEnum(t *testing.T, src string) (string, error) {
	t.Helper()
	var output strings.Builder
	r := bashPPRunner(t, &output, interp.Lang(syntax.LangBashPP))
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), f)
	return output.String(), err
}

func TestBashPPEnumFixtures(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"exhaustive", "type Color enum { Red; Green }\nfunc label(c Color) string {\n switch c {\n case Red:\n  return \"red\"\n case Green:\n  return \"green\"\n }\n}\nprintf '%s\\n' label(Green)\n", "green\n"},
		{"default", "type Color enum { Red; Green; Blue }\nfunc label(c Color) string {\n switch c {\n case Red:\n  return \"red\"\n default:\n  return \"other\"\n }\n}\nprintf '%s\\n' label(Blue)\n", "other\n"},
		{"nested", "type Color enum { Red; Green }\ntype Shade enum { Light; Dark }\nfunc label(c Color, s Shade) string {\n switch c {\n case Red:\n  return \"red\"\n case Green:\n  switch s {\n  case Light:\n   return \"light-green\"\n  case Dark:\n   return \"dark-green\"\n  }\n }\n}\nprintf '%s\\n' label(Green, Dark)\n", "dark-green\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runBashPPEnum(t, tc.src)
			if err != nil || got != tc.want {
				t.Fatalf("output=%q error=%v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestBashPPEnumDiagnostics(t *testing.T) {
	tests := []struct{ src, want string }{
		{"type Color enum { Red; 1Blue }\n", "BASHPP-EENUM-MEMBER: enum member \"1Blue\" must be an identifier\n"},
		{"type Color enum { Red; Red }\n", "BASHPP-EENUM-DUPLICATE: enum Color declares member \"Red\" more than once\n"},
		{"type Color enum { Red; Green }\nfunc label(c Color) string {\n switch c {\n case Red:\n  return \"red\"\n }\n}\n", "BASHPP-EENUM-NONEXHAUSTIVE: switch on Color is missing member Green or a default arm\n"},
		{"type Color enum { Red; Green }\nc := Color(9)\n", "BASHPP-EENUM-VALUE: 9 is not a member of Color\n"},
	}
	for _, tc := range tests {
		got, err := runBashPPEnum(t, tc.src)
		if err == nil || got != tc.want {
			t.Errorf("source %q: output=%q error=%v, want %q", tc.src, got, err, tc.want)
		}
	}
}
