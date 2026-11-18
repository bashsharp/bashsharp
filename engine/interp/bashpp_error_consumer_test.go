package interp_test

import "testing"

func TestBashPPPredeclaredErrorInterface(t *testing.T) {
	source := `type Failure string
func (f Failure) Error() string { return f }
type RichError interface { error }
func main() {
 var empty error
 emptyNil := empty == nil
 var failure Failure = "broken"
 var err error = failure
 message := err.Error()
 original, ok := err.(Failure)
 var embedded RichError = failure
 nested := embedded.Error()
 println(emptyNil, message, original, ok, nested)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "true broken broken true broken\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
func TestBashPPErrorInterfaceTypedNil(t *testing.T) {
	source := `type Failure string
func (f Failure) Error() string { return f }
func main() {
 var p *Failure = nil
 var e error = p
 equal := e == nil
 _, ok := e.(*Failure)
 println(equal, ok)
}
main()
`
	out, stderr, err := runBashSharpCall(t, source)
	if err != nil || stderr != "" || out != "false true\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
