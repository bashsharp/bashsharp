//go:build full

package interp_test

import "testing"

func TestS243AggregateScalarRepresentation(t *testing.T) {
	source := `package main
import (
	"fmt"
	"reflect"
)

type Recv <-chan int
type Count int64
type Number complex128
type SmallNumber complex64

func appendOne[S ~[]E, E any](dst S, value E) S { return append(dst, value) }

func main() {
	z := complex(5.0, 7.0)
	fmt.Println(reflect.DeepEqual(append([]complex128{}, z), []complex128{z}))
	fmt.Println(reflect.DeepEqual(appendOne([]Count{}, Count(9)), []Count{9}))
	fmt.Println(reflect.DeepEqual(appendOne([]Number{}, Number(z)), []Number{Number(z)}))
	fmt.Printf("%T %T\n", []Number{Number(z)}, map[string]SmallNumber{"n": SmallNumber(z)})
	fmt.Println(reflect.DeepEqual(appendOne([]SmallNumber{}, SmallNumber(z)), []SmallNumber{SmallNumber(z)}))
	recv := make(Recv)
	values := appendOne([]Recv{recv}, recv)
	fmt.Println(len(values), values[0] == recv, values[1] == recv)
}`
	typedSendThreeModes(t, source)
}
