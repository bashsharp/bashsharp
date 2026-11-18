//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestS281ManagerPromotedReflectReceiver(t *testing.T) {
	differGoSource(t, `package main
import("fmt";"reflect")
type base struct { Flag bool; Calls int }
func(b *base) Bounded()bool { b.Calls++;return b.Flag }
type Name struct { base }
func main(){
 n:=&Name{base:base{Flag:true}}
 m:=reflect.ValueOf(n).MethodByName("Bounded")
 fmt.Println(m.Call(nil)[0].Bool(),n.Calls)
 n.Flag=false
 fmt.Println(m.Call(nil)[0].Bool(),n.Calls)
}
`, nil, "")
}

func TestS281PromotedReflectReceiverGuards(t *testing.T) {
	differGoSource(t, `package main
import("fmt";"reflect")
type base struct { Flag bool; Calls int }
func(b *base) Add(n int)int { b.Calls += n; return b.Calls }
func(b *base) Nil()bool { return b == nil }
func(b base) Snapshot()bool { return b.Flag }
type mid struct { *base }
type outer struct { mid }
type left struct{}
func(*left) Clash(){}
type right struct{}
func(*right) Clash(){}
type ambiguous struct { *left; *right }
func main(){
 b:=&base{Flag:true}; o:=&outer{mid:mid{base:b}}
 add:=reflect.ValueOf(o).MethodByName("Add")
 fmt.Println(add.Call([]reflect.Value{reflect.ValueOf(2)})[0].Int(),b.Calls)
 fmt.Println(add.Call([]reflect.Value{reflect.ValueOf(3)})[0].Int(),b.Calls)
 snapshot:=reflect.ValueOf(o).MethodByName("Snapshot");o.Flag=false
 fmt.Println(snapshot.Call(nil)[0].Bool(),b.Calls)
 n:=&mid{}
 fmt.Println(reflect.ValueOf(n).MethodByName("Nil").Call(nil)[0].Bool())
 fmt.Println(reflect.ValueOf(&ambiguous{}).MethodByName("Clash").IsValid())
}
`, nil, "")
}

func TestS281MappedPromotedReflectReceiver(t *testing.T) {
	depSrc := `package p
import "reflect"
type miniExpr struct{ bounded bool; calls int }
func (m *miniExpr) Bounded() bool { m.calls++; return m.bounded }
type Name struct{ miniExpr }
func Run() (bool, int, bool, int) {
 n := &Name{miniExpr: miniExpr{bounded: true}}
 m := reflect.ValueOf(n).MethodByName("Bounded")
 first := m.Call(nil)[0].Bool()
 firstCalls := n.calls
 n.bounded = false
 second := m.Call(nil)[0].Bool()
 return first, firstCalls, second, n.calls
}`
	mainSrc := `package main
import ("fmt"; "./p")
func main() { fmt.Println(p.Run()) }`
	out, stderr := runGoSourceMultiPackage(t, "s281-promoted-reflect", mainSrc, "test/p", "p.go", depSrc)
	if stderr != "" || out != "true 1 false 2\n" {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
}
