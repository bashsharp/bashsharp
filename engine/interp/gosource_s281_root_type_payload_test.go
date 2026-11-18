//go:build full

package interp_test

import "testing"

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

func TestGoSourceS281RootOwnedTypeFuncPayloadThroughNativeSortAndFmt(t *testing.T) {
	differGoSource(t, `package main
import (
	"bytes"
	"fmt"
	"sort"
)

type Type struct {
	extra   any
	methods []*Field
}
type Func struct {
	Name   string
	Params []*Type
}
type Field struct {
	Type *Type
	Name string
}

func (f *Func) Format(s fmt.State, verb rune) {
	if f == nil {
		fmt.Fprint(s, "<nil-func>")
		return
	}
	fmt.Fprintf(s, "Func(%s/%d)", f.Name, len(f.Params))
}

func mustFunc(t *Type) *Func {
	f, ok := t.extra.(*Func)
	fmt.Printf("assert %T %t\n", t.extra, ok)
	if !ok || f == nil {
		panic("missing *Func payload")
	}
	return f
}

func main() {
	left := &Type{}
	right := &Type{}
	fn := &Func{Name: "root", Params: []*Type{left, right}}
	root := &Type{extra: fn}
	root.methods = []*Field{
		{Type: right, Name: "beta"},
		{Type: root, Name: "alpha"},
		{Type: left, Name: "beta"},
	}

	phaseFunc, phaseOK := root.extra.(*Func)
	fmt.Printf("phase %T %T %t\n", root.extra, phaseFunc, phaseOK)
	fmt.Println("phase-eq", phaseFunc == fn)
	sort.SliceStable(root.methods, func(i, j int) bool {
		root.methods[i].Type.extra = fn
		root.methods[j].Type.extra = fn
		return root.methods[i].Name < root.methods[j].Name
	})
	stable := mustFunc(root.methods[0].Type)
	fmt.Println("stable", root.methods[0].Name, stable == fn, root.methods[1].Type.extra == fn)

	sort.Slice(root.methods, func(i, j int) bool {
		root.methods[i].Type.extra = fn
		root.methods[j].Type.extra = fn
		return root.methods[i].Name > root.methods[j].Name
	})
	unstable := mustFunc(root.methods[len(root.methods)-1].Type)
	fmt.Println("slice", root.methods[0].Name, unstable == fn)

	fmt.Println("format", fmt.Sprintf("%v", mustFunc(root)))
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%v", mustFunc(root))
	fmt.Println("fprintf", buf.String())

	var nilFunc *Func
	nilPointer := &Type{extra: nilFunc}
	typedNil, ok := nilPointer.extra.(*Func)
	fmt.Println("nil-pointer", ok, typedNil == nil, fmt.Sprintf("%v", typedNil))
	var nilValue any
	_, ok = nilValue.(*Func)
	fmt.Println("nil-value", ok)
}`, nil, "")
}
