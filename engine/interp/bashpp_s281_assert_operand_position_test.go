// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// An assertion-operand diagnostic must name the source position of the
// operand expression, like every other Bash++ diagnostic. Without it a
// failure in a large interpreted program — interpreted go/types, say —
// reports only the bare code and leaves nothing to look at.
func TestS809AssertOperandDiagnosticCarriesPosition(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"type assertion",
			"func main() {\n\tn := 1\n\tv := n.(int)\n\techo $v\n}\nmain()\n",
			"operand.bpp: line 3: BASHPP-EASSERT-OPERAND: type assertion operand is not an interface\n",
		},
		{
			"type switch",
			"func main() {\n\tn := 1\n\tswitch v := n.(type) {\n\tcase int:\n\t\techo $v\n\t}\n}\nmain()\n",
			"operand.bpp: line 3: BASHPP-ETYPESWITCH-OPERAND: n is not an interface\n",
		},
		{
			"impossible assertion",
			"type T int\nfunc (v T) M(s string) { }\ntype U int\ntype I interface { M(string) }\nfunc main() {\n\tvar v T = 1\n\tvar i I = v\n\tx, ok := i.(U)\n\techo $x $ok\n}\nmain()\n",
			"operand.bpp: line 8: BASHPP-EASSERT-IMPOSSIBLE: U cannot be asserted from I\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "operand.bpp")
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
			if err := r.Run(context.Background(), f); err == nil {
				t.Fatal("assertion unexpectedly succeeded")
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}
