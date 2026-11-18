package interp_test

import (
	"fmt"
	"testing"
)

func TestBashPPScalarCallShortCircuit(t *testing.T) {
	const source = `func touched(label string) bool {
 println(label)
 return true
}
func number(value int) int { return value }
func main() {
 a := false && touched("skip-and")
 b := true || touched("skip-or")
 c := true && touched("and")
 d := false || touched("or")
 sum := 1 + number(2 + 3)
 println(a, b, c, d, sum)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "and\nor\nfalse true true true 6\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
func TestBashPPScalarCallNoArgumentAndClosure(t *testing.T) {
	const source = `func main() {
 f := func() bool { println("called"); return true }
 skipped := false && f()
 taken := true && f()
 println(skipped, taken)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "called\nfalse true\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPScalarCallPreservesExitAndPanic(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantErr string
		status              int
	}{
		{"exit", "exit 7", "", 7},
		{"panic", `panic("boom")`, "panic: boom\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, statement := range []string{"result := true && value()", "return true && value()"} {
				source := "func value() bool { " + tc.body + " }\nfunc main() bool {\n " + statement + "\n println(\"after\")\n return true\n}\nmain()\n"
				out, stderr, err := runBashSharpCall(t, source)
				if out != "" || stderr != tc.wantErr || err == nil || err.Error() != fmt.Sprintf("exit status %d", tc.status) {
					t.Fatalf("%s out=%q stderr=%q err=%v", statement, out, stderr, err)
				}
			}
		})
	}
}

func TestBashPPScalarCallBooleanResultTypes(t *testing.T) {
	const source = `type Flag bool
func flag() Flag { return true }
func number() int { println("must-not-run"); return 1 }
func main() {
 value := true && flag()
 println(value)
 invalid := true || number()
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if out != "true\n" || stderr != "BASHPP-EEXPR-OPERAND: logical operand must be boolean\n" || err == nil {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
