//go:build full

package lower_test

import "testing"

// Extends the refusal finding recorded with callables_closure_results_test.go
// in 02b17a92. A host callable returning zero does not prove source exit status
// or the shell-visible spelling of a binding whose initializer was refused.
// These agentic controls remain outside the Go-profile row denominator.
func TestAgenticRefusedResultAcceptance(t *testing.T) {
	const direct = `agentic func assist() int {
 return 3
}
`
	const escaped = direct + `func Escalating() func {
 agentic {
  return func() int { return assist() }
 }
}
`
	const denial = "assist: agentic action requires an explicit agentic { ...; } scope\n"
	const missing = "assignment mismatch: 1 variable(s) but 0 value(s)\n"
	cases := []struct{ name, declarations, body, sourceOut, sourceErr string }{
		{"direct_failure_status", direct, " b := assist()\n", "", denial + missing},
		{"direct_missing_result_bytes", direct, " b := assist()\n echo \"b=$b\"\n", "b=\n", denial + missing},
		{"escaped_failure_status", escaped, " f := Escalating()\n b := f()\n", "", denial},
		{"escaped_missing_result_bytes", escaped, " f := Escalating()\n b := f()\n echo \"b=$b\"\n", "b=\n", denial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.declarations + "func main() {\n" + tc.body + "}\nmain()\n"
			wantOut, wantErr, wantStatus := genericMethodOracle(t, source)
			t.Logf("source stdout=%q stderr=%q status=%d", wantOut, wantErr, wantStatus)
			if wantOut != tc.sourceOut || wantErr != tc.sourceErr || wantStatus != 2 {
				t.Fatal("refused-result source oracle changed")
			}
			result := compile(t, source)
			out, stderr, status := runArtifact(t, result.Result)
			t.Logf("artifact stdout=%q stderr=%q status=%d", out, stderr, status)
			if out != wantOut || stderr != wantErr || status != wantStatus {
				t.Fatalf("refused callable result differs: artifact=(%q,%q,%d) source=(%q,%q,%d)", out, stderr, status, wantOut, wantErr, wantStatus)
			}
		})
	}
}
