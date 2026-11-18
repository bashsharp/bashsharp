//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestGoSourceS281ReflectedTypedNilPointerInterface(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

type T struct{ X int }

func main() {
	v := reflect.New(reflect.TypeOf((*T)(nil))).Elem().Interface()
	p := v.(*T)
	fmt.Println(v == nil, p == nil)
}
`
	differGoSource(t, source, nil, "")
}

func TestGoSourceS281NativeInterfaceTypedNilPointerFieldTuple(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

type T struct{ X int }
type Box struct{ V any }

func main() {
	box := Box{V: reflect.New(reflect.TypeOf((*T)(nil))).Elem().Interface()}
	p := box.V.(*T)
	fmt.Println(box.V == nil, p == nil)
}
`
	differGoSource(t, source, nil, "")
}

func TestGoSourceS281NativeInterfaceNilComparisonGuards(t *testing.T) {
	const source = `package main

import (
	"bytes"
	"fmt"
	"reflect"
)

type T struct{ X int }
type Alias = any
type Box struct{ V Alias }
type Nested struct{ Box Box }

var calls int

func once(v any) any {
	calls++
	return v
}

func compareSelf(v any) {
	defer func() { fmt.Println(recover() != nil) }()
	fmt.Println(v == v)
}

func main() {
	var concreteSlice []int
	var concreteMap map[string]int
	var concretePointer *bytes.Buffer
	fmt.Println(concreteSlice == nil, nil == concreteSlice, concreteSlice != nil, nil != concreteSlice)
	fmt.Println(concreteMap == nil, nil == concreteMap, concreteMap != nil, nil != concreteMap)
	fmt.Println(concretePointer == nil, nil == concretePointer, concretePointer != nil, nil != concretePointer)

	typedPointer := reflect.New(reflect.TypeOf((*T)(nil))).Elem().Interface()
	box := Box{V: typedPointer}
	nested := Nested{Box: box}
	var nilInterface any
	typedSlice := reflect.ValueOf([]int(nil)).Interface()
	typedMap := reflect.ValueOf(map[string]int(nil)).Interface()
	byteBuffer := reflect.New(reflect.TypeOf((*bytes.Buffer)(nil))).Elem().Interface()
	boxedSlice := Box{V: typedSlice}
	boxedMap := Box{V: typedMap}
	boxedPointer := Box{V: byteBuffer}

	fmt.Println(box.V == nil, nil == box.V, box.V != nil, nil != box.V)
	fmt.Println(nilInterface == nil, nil == nilInterface, nilInterface != nil, nil != nilInterface)
	fmt.Println(typedSlice == nil, nil == typedSlice, typedSlice != nil, nil != typedSlice)
	fmt.Println(typedMap == nil, nil == typedMap, typedMap != nil, nil != typedMap)
	fmt.Println(byteBuffer == nil, nil == byteBuffer, byteBuffer != nil, nil != byteBuffer)
	fmt.Println(boxedSlice.V == nil, nil == boxedSlice.V, boxedSlice.V != nil, nil != boxedSlice.V)
	fmt.Println(boxedMap.V == nil, nil == boxedMap.V, boxedMap.V != nil, nil != boxedMap.V)
	fmt.Println(boxedPointer.V == nil, nil == boxedPointer.V, boxedPointer.V != nil, nil != boxedPointer.V)
	fmt.Println(nested.Box.V == nil, nil != nested.Box.V)
	fmt.Println(once(box.V) == box.V, calls)
	p := box.V.(*T)
	fmt.Println(p == nil, nil == p)
	compareSelf(typedSlice)
}
`
	differGoSource(t, source, nil, "")
}
