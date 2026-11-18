// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package lower_test

import "testing"

// ${name:+word} over a known typed binding selects the alternate exactly when
// the binding's projected text is non-empty, matching the interpreter's cell
// view: a set non-null binding substitutes the word, an empty one substitutes
// nothing. Parity is proven by the shared compiled/interpreted diff.
func TestAlternateExpansionOverTypedBindings(t *testing.T) {
	out, _ := execute(t, compile(t, `value := "v"
empty := ""
echo "${value:+set}:${empty:+set}:${value:+}"
`))
	if out != "set::\n" {
		t.Fatalf("output = %q", out)
	}
}
