//go:build full

package interp

import "testing"

func TestS319ManagerOpaqueAliasMutationRefuses(t *testing.T) {
	source := `package dep
import "example.com/foreign"
type Expr interface { Eval(func() bool) bool }
type Root struct { Next Expr }
func (r *Root) Eval(cb func() bool) bool {
    foreign.Replace(&r.Next)
    return r.Next.Eval(cb)
}
type Leaf struct{}
func (*Leaf) Eval(cb func() bool) bool { return cb() }
`
	ok, reason := dependencyCallbackProveMethodSource(t, source, "Root", "Eval", []string{"Root", "Leaf"}, []int{0})
	if ok {
		t.Fatalf("opaque alias mutation admitted; reason=%q", reason)
	}
}
