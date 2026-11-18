//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Retained reflect.Value selections over interpreter-owned pointer storage.
// Every admitted behavior is compared with the unchanged program built and
// run by the pinned native Go toolchain.

import (
	"strings"
	"testing"
)

func TestS281RetainedReflectSelectionSemantics(t *testing.T) {
	for name, body := range map[string]string{
		"repeated scalar read":                `v := reflect.ValueOf(n).Elem().FieldByName("Class"); fmt.Println(v.Int()); n.Class = 9; fmt.Println(v.Int())`,
		"selected element keeps backing":      `v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0); n.Body = []int{8}; fmt.Println(v.Int())`,
		"selected element sees backing write": `v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0); n.Body[0] = 9; fmt.Println(v.Int())`,
		"selected slice follows header":       `v := reflect.ValueOf(n).Elem().FieldByName("Body"); n.Body = []int{8, 9}; fmt.Println(v.Len(), v.Index(0).Int())`,
		"index panic is eager":                `defer func() { fmt.Println("caught", recover() != nil) }(); _ = reflect.ValueOf(n).Elem().FieldByName("Body").Index(10); fmt.Println("after")`,
		"field panic is eager":                `defer func() { fmt.Println("caught", recover() != nil) }(); _ = reflect.ValueOf(n).Elem().Field(99); fmt.Println("after")`,
	} {
		t.Run(name, func(t *testing.T) {
			source := `package main
import ("fmt"; "reflect")
type node struct { Class int; Body []int }
func (n *node) String() string { return "node" }
func main() { n := &node{Class: 1, Body: []int{1}}; ` + body + ` }
`
			differGoSource(t, source, nil, "")
		})
	}
}

func TestS281ReflectValueSnapshotAndCallOnce(t *testing.T) {
	for name, source := range map[string]string{
		"ValueOf value is a snapshot": `package main
import ("fmt"; "reflect")
type node struct { Class int }
func main() { n := node{Class: 1}; v := reflect.ValueOf(n).Field(0); n.Class = 9; fmt.Println(v.Int(), n.Class) }
`,
		"Call selection is not replayed": `package main
import ("fmt"; "reflect")
type node struct { Class int }
func (n *node) Next() *node { n.Class++; return &node{Class: n.Class} }
func main() { n := &node{Class: 1}; v := reflect.ValueOf(n).MethodByName("Next").Call(nil)[0].Elem().Field(0); fmt.Println(v.Int(), v.Int(), n.Class) }
`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestS281DetachedRetainedReflectWriteRefuses(t *testing.T) {
	source := `package main
import "reflect"
type node struct { Class int; Body []int }
func (n *node) String() string { return "node" }
func main() {
	n := &node{Body: []int{1}}
	old := n.Body
	v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0)
	n.Body = []int{8}
	v.SetInt(9)
	println(old[0])
}
`
	_, stderr, err := runGoSource(t, "detached-reflect-write", source)
	if err == nil || !strings.Contains(err.Error()+stderr, "no longer reachable from its original pointer") {
		t.Fatalf("expected detached-write refusal, got err=%v stderr=%q", err, stderr)
	}
}

func TestS281RetainedReflectSliceBackingSemantics(t *testing.T) {
	for name, main := range map[string]string{
		"detached original alias write": `n := &node{Body: []int{1, 2}}; old := n.Body; v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0); n.Body = []int{8}; old[0] = 9; fmt.Println(v.Int())`,
		"shifted overlapping alias":     `n := &node{Body: []int{1, 2}}; v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(1); n.Body = n.Body[1:]; n.Body[0] = 9; fmt.Println(v.Int())`,
		"capacity tail reslice":         `n := &node{Body: append(make([]int, 0, 3), 1, 2, 3)[:1]}; v := reflect.ValueOf(n).Elem().FieldByName("Body"); fmt.Println(v.Slice(0, 3).Index(2).Int())`,
		"restricted capacity alias":     `n := &node{Body: []int{1, 2}}; v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0); n.Body = n.Body[:1:1]; n.Body[0] = 9; fmt.Println(v.Int())`,
		"widened earlier alias":         `whole := []int{1, 2, 3}; n := &node{Body: whole[1:]}; v := reflect.ValueOf(n).Elem().FieldByName("Body").Index(0); n.Body = whole; whole[1] = 9; fmt.Println(v.Int())`,
	} {
		t.Run(name, func(t *testing.T) {
			source := `package main
import ("fmt"; "reflect")
type node struct { Body []int }
func (n *node) String() string { return "node" }
func main() { ` + main + ` }
`
			differGoSource(t, source, nil, "")
		})
	}
}
