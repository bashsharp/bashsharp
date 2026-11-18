// Outside-corpus reproducer: method expressions on an imported package's
// named types — an interface (reflect.Type.NumMethod), a pointer receiver
// ((*bytes.Buffer).WriteString) and a value receiver (time.Duration.String)
// — used as values, called directly, and assigned to a declared func type.
package main

import (
	"bytes"
	"fmt"
	"reflect"
	"time"
)

type foo struct{}

func (foo) X() {}

func main() {
	numMethod := reflect.Type.NumMethod
	fmt.Println(numMethod(reflect.TypeOf(foo{})))
	m := reflect.Type.Method(reflect.TypeOf(foo{}), 0)
	fmt.Println(m.Name)

	write := (*bytes.Buffer).WriteString
	var b bytes.Buffer
	n, err := write(&b, "hi")
	fmt.Println(b.String(), n, err)

	var text func(time.Duration) string = time.Duration.String
	fmt.Println(text(90 * time.Second))

	byName := reflect.Type.MethodByName
	_, ok := byName(reflect.TypeOf(foo{}), "X")
	fmt.Println(ok)
}
