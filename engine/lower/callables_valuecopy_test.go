//go:build full

package lower_test

import "testing"

// Exact public interfaces/value-copy-assertion-zero.bpp source.
func TestNativeValueCopyAssertion(t *testing.T) {
	source := `type Box struct { N int }
func (v Box) Show(prefix string) { echo "$prefix:${v.N}"; }
type Shower interface { Show(string) }
type Other int
func (v Other) Show(prefix string) { echo "$prefix:$v"; }
func main() {
	var box Box = Box{N: 1}
	var i Shower = box
	box.N = 9
	stored := i.(Box)
	printf 'copy:%s\n' stored.N
	zero, ok := i.(Other)
	echo "zero:$zero:$ok"
	p := &box
	var pi Shower = p
	q := pi.(*Box)
	q.N = 12
	printf 'pointer:%s\n' box.N
}
main()
`
	execute(t, compile(t, source))
}

func TestNativeBadSubstitutionContinuation(t *testing.T) {
	source := `type Box struct { N int }
func (v Box) Show(prefix string) { echo "$prefix:${v.N}"; echo after; }
func main() { var v Box = Box{N: 1}; v.Show("x"); echo end; }
main()
`
	execute(t, compile(t, source))
}
