//go:build full

package lower_test

import "testing"

func TestPublicNativeMethodDispatch(t *testing.T) {
	source := `type Count int
func (v Count) Show(prefix string) {
 echo "$prefix:$v"
}
func (p *Count) Pointer() {
 echo "ptr:$p"
}
func relay(v Count) {
 v.Show(relay)
}
func makeCount() Count {
 return 11
}
var v Count = 7
v.Show(direct)
v.Pointer()
relay(v)
x := makeCount()
x.Show(result)
var q *Count = 9
q.Show(deref)
(*Count).Pointer(q)
f := v.Show
f(value)
pf := v.Pointer
pf()
Count.Show(v, expression)
func later() {
 defer v.Show(deferred)
}
later()
var p *Count
func (p *Count) NilOK() {
 if [ -z "$p" ]; then echo nil; fi
}
p.NilOK()
`
	execute(t, compile(t, source))
}
