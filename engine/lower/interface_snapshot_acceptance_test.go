package lower_test

import "testing"

// This is a derived executable control, not a verbatim fixture extraction.
// interp/bashpp_embed_test.go:TestBashPPEmbeddedInterfaceValueAndReferenceSemantics
// supplies the embedded interface and dynamic pointer source forms.
// interp/bashpp_collection_internal_test.go:TestBashPPSubshellCloneOwnsEmbeddedInterfaceDynamicPointer
// supplies the parent/child isolation obligation but constructs cells directly.
// Here the actual language subshell performs and observes the mutation.
func TestEmbeddedInterfacePointerSubshellAcceptance(t *testing.T) {
	const source = `type Speaker interface { Speak(); Set(int) }
type Voice struct { N int }
func (v Voice) Speak() { printf '%s:' v.N }
func (p *Voice) Set(n int) { p.N = n }
type Outer struct { Speaker }
func main() {
 p := new(Voice)
 p.N = 3
 var reference Speaker = p
 h := Outer{Speaker: reference}
 (
  h.Set(4)
  h.Speak()
 )
 h.Speak()
}
main()
`
	out, stderr, status := genericMethodOracle(t, source)
	t.Logf("source stdout=%q stderr=%q status=%d", out, stderr, status)
	if out != "4:3:" || stderr != "" || status != 0 {
		t.Fatal("embedded interface child mutation violated the public graph isolation obligation")
	}
	execute(t, compile(t, source))
}
