//go:build full

package interp_test

import "testing"

func TestS374Issue27695ReflectedReceiver(t *testing.T) {
	const source = `package main
import (
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"
)
type Stt struct { Data interface{} }
type My struct { b byte }
func (m *My) Run(raw []byte) (Stt, error) { return Stt{Data: "hello"}, nil }
func main() {
	debug.SetGCPercent(1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				f := reflect.ValueOf(&My{}).MethodByName("Run")
				method := f.Interface().(func([]byte) (Stt, error))
				got, err := method(nil)
				if err != nil || got.Data != "hello" { panic("wrong result") }
			}
		}()
	}
	wg.Wait()
	fmt.Println("ok")
}`
	differGoSource(t, source, nil, "")
}
