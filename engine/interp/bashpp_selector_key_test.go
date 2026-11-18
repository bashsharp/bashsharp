// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func TestBashPPStructPromotedSelectorKeys(t *testing.T) {
	const src = `type Leaf struct { Depth int; Label string }
type Habitat struct { Leaf }
type Gopher struct { Name string; Habitat }
func main() {
 g := Gopher{Name: "x", Depth: 3, Label: "deep"}
 printf '%s:%s:%s' g.Name g.Depth g.Label
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "x:3:deep"))
}

func TestBashPPStructSelectorKeyDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"ambiguous",
			"type A struct { X int }\ntype B struct { X int }\ntype Outer struct { A; B }\nfunc main() {\n bad := Outer{X: 1}\n printf '%s' \"$bad\"\n}\nmain()\n",
			"BASHPP-ESTRUCT-AMBIGUOUS",
		},
		{
			"pointer traversal",
			"type Leaf struct { X int }\ntype Outer struct { *Leaf }\nfunc main() {\n bad := Outer{X: 1}\n printf '%s' \"$bad\"\n}\nmain()\n",
			"BASHPP-ESTRUCT-KEY-POINTER",
		},
		{
			"parent then promoted",
			"type Leaf struct { X int }\ntype Outer struct { Leaf }\nfunc main() {\n bad := Outer{Leaf: Leaf{}, X: 1}\n printf '%s' \"$bad\"\n}\nmain()\n",
			"BASHPP-ESTRUCT-KEY-CONFLICT",
		},
		{
			"promoted then parent",
			"type Leaf struct { X int }\ntype Outer struct { Leaf }\nfunc main() {\n bad := Outer{X: 1, Leaf: Leaf{}}\n printf '%s' \"$bad\"\n}\nmain()\n",
			"BASHPP-ESTRUCT-KEY-CONFLICT",
		},
		{
			"dotted key is not Go syntax",
			"type Leaf struct { X int }\ntype Outer struct { Leaf }\nfunc main() {\n bad := Outer{Leaf.X: 1}\n printf '%s' \"$bad\"\n}\nmain()\n",
			"BASHPP-ESTRUCT-KEY",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(test.src), "selector-key.bpp")
			qt.Assert(t, qt.IsNil(err))
			var stdout, stderr strings.Builder
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true), interp.StdIO(nil, &stdout, &stderr))
			qt.Assert(t, qt.IsNil(err))
			err = runner.Run(context.Background(), file)
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
			qt.Assert(t, qt.StringContains(stderr.String(), "selector-key.bpp: line "))
			qt.Assert(t, qt.StringContains(stderr.String(), test.want))
		})
	}
}
