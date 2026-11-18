package interp_test

import (
	"strings"
	"testing"
)

func TestBashPPPositionedCallConsumers(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"live-length-guard", `func first(xs []string) string {
 if xs == nil || len(xs) == 0 { return "empty" }
 value := xs[0]
 return value
}
func main() {
 empty := []string{}
 populated := []string{"first"}
 a := first(empty)
 b := first(populated)
 println(a, b)
}
main()
`, "empty first\n"},
		{"concrete-callback-return", `func invoke(fn func() int) int {
 return fn()
}
func main() {
 f := func() int { return 7 }
 result := invoke(f)
 println(result)
}
main()
`, "7\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.source)
			if err != nil || stderr != "" || out != tc.want {
				t.Fatalf("out=%q stderr=%q err=%v want=%q", out, stderr, err, tc.want)
			}
		})
	}
}

func TestBashPPPositionedReturnCallTuple(t *testing.T) {
	source := `func pair() (int, string) { return 7, "seven" }
func forwarded() (int, string) {
 return pair()
}
func main() {
 number, text := forwarded()
 println(number, text)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "7 seven\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
func TestBashPPConcreteCallbackSignatureMismatch(t *testing.T) {
	source := `func invoke(fn func() int) int {
 return fn()
}
func main() {
 wrong := func() string { return "wrong" }
 invoke(wrong)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err == nil || out != "" || !strings.Contains(stderr, "BASHPP-EARG-FUNCTYPE:") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
func TestBashPPNilSliceLengthGuardExecutes(t *testing.T) {
	source := `func main() {
 var xs []int
 if xs == nil || len(xs) == 0 {
  println("empty")
 }
 populated := []int{3}
 if populated != nil && cap(populated) > 0 {
  println("populated")
 }
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "empty\npopulated\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestBashPPPositionedScalarReturnGuard(t *testing.T) {
	source := `func positive(p *int) bool {
 return p != nil && *p > 0
}
func empty(xs []int) bool {
 return xs == nil || len(xs) == 0
}
func main() {
 var missing *int
 pointer := new(int)
 *pointer = 3
 no := positive(missing)
 yes := positive(pointer)
 var xs []int
 populated := []int{3}
 a := empty(xs)
 b := empty(populated)
 println(no, yes, a, b)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "false true true false\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
