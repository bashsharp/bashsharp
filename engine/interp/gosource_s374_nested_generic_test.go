//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS374NestedGenericWorkerRegistration(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
func F[A any]() {
 type T[B any] struct { X A; Y B }
 fmt.Println(reflect.TypeOf(T[int]{}), reflect.TypeOf(T[string]{}))
}

func main() { F[int](); F[string]() }
`, nil, "")
}

func TestS374NestedGenericCorpus(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "typeparam", "nested.go"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(source), nil, "")
}

func TestS374NestedGenericForwardedInstantiation(t *testing.T) {
	differGoSource(t, `package main
import ("fmt"; "reflect")
func F[A any]() { type T[B any] struct { X A; Y B }; fmt.Println(reflect.TypeOf(T[A]{})) }
func G[C any]() { F[C]() }
func main() { G[int](); G[string]() }
`, nil, "")
}
