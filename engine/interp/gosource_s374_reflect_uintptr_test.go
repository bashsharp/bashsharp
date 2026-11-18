//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS374ReflectUintptrPointerRoundTrip(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
 "runtime"
 "unsafe"
)
func value(x int) reflect.Value { runtime.GC(); return reflect.ValueOf(&x) }
func check(p, q unsafe.Pointer) { fmt.Println(*(*int)(p), *(*int)(q)) }
func main() {
 check(unsafe.Pointer(value(1).Pointer()), unsafe.Pointer(value(2).Pointer()))
 check(unsafe.Pointer(reflect.Value.Pointer(value(1))), unsafe.Pointer(reflect.Value.Pointer(value(2))))
}

`, nil, "")
}

func TestS374Issue15329Corpus(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue15329.go"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(source), nil, "")
}
