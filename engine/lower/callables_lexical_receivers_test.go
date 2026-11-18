//go:build full

package lower

import (
	"context"
	"testing"
	"time"
)

func TestLexicalPointerReceiverProfileArtifact(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	want := runMethodEngine(t, ctx, source)
	result, err := Compile(parseMethodFixture(t, source), Options{Entry: "Execute"})
	if err != nil {
		t.Fatal(err)
	}
	got := runMethodArtifact(t, ctx, result.Source)
	if got != want {
		t.Fatalf("artifact=%+v source=%+v", got, want)
	}
}
