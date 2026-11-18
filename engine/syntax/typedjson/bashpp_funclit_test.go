// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

// TestBashPPFuncLitRoundTrip covers the Sprint 114 nodes on the JSON surface.
//
// A node type absent from typedjson's table encodes and then fails to DECODE,
// which is the worst place to find out: the tree is already on the wire. The
// literal, the variadic field and the spread call each add state — a nested
// signature, an Ellipsis position — that only a decode-and-reprint can prove
// survived.
func TestBashPPFuncLitRoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		"func empty() { }\n",
		"go func() { }()\n",
		"go func() { echo hi; }()\n",
		"greet := func(who string) {\n\techo \"hi $who\"\n}\n",
		"n := func() int {\n\treturn 1\n}()\n",
		"func(n int) {\n\techo $n\n}(1)\n",
		"func f() {\n\tdefer func(seen int) {\n\t\techo $seen\n\t}($v)\n}\n",
		"func mk(base int) func {\n\treturn func(extra int) int {\n\t\treturn $((base + extra))\n\t}\n}\n",
		"func tag(p string, rest ...int) {\n\techo $p\n}\n",
		"sum(xs...)\n",
	} {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var enc strings.Builder
			if err := typedjson.Encode(&enc, f); err != nil {
				t.Fatal(err)
			}
			node, err := typedjson.Decode(strings.NewReader(enc.String()))
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if err := syntax.NewPrinter().Print(&out, node); err != nil {
				t.Fatal(err)
			}
			if out.String() != src {
				t.Fatalf("round trip = %q, want %q", out.String(), src)
			}
		})
	}
}
