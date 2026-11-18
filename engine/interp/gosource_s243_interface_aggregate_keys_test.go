//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Sprint: #243; Story: #673; Story-ID: f24307569417
//
// An aggregate returned as interface{} by reflect is the issue65957 shape:
// its dynamic [4]int32 type is comparable and must remain a map key. The
// controls also pin the interface equality rules used by convT2X: dynamic
// type identity, recursive value equality, incomparable values, pointers,
// and nil interfaces are all distinct cases.
func TestS243InterfaceAggregateKeyAndEqualityThreeModes(t *testing.T) {
	typedSendThreeModes(t, `package main
import (
	"fmt"
	"reflect"
)
type A [4]int32
type S struct { A A; N int }
func main() {
	m := map[any]int{}
	k := reflect.New(reflect.ArrayOf(4, reflect.TypeOf(int32(0)))).Elem().Interface()
	m[k] = 7
	m[A{0, 0, 0, 0}]++
	fmt.Println(len(m), m[k], m[A{}])
	ch := make(chan any, 1)
	ch <- A{1, 2, 3, 4}
	got := <-ch
	fmt.Println(got == A{1, 2, 3, 4}, got != A{1, 2, 3, 5})

	var ia any = A{1, 2, 3, 4}
	fmt.Println(ia == A{1, 2, 3, 4}, ia == A{1, 2, 3, 5})
	var is any = S{A: A{1, 2, 3, 4}, N: 5}
	fmt.Println(is == S{A: A{1, 2, 3, 4}, N: 5})
	p := &S{N: 9}; var ip any = p
	fmt.Println(ip == p, ip == (*S)(nil), any((*S)(nil)) == nil)
	func() { defer func() { fmt.Println(recover() != nil) }(); var x any = []int{1}; _ = x == x }()
}`)
}

func TestS243OriginalAggregateInterfaceRoots(t *testing.T) {
	t.Run("issue65957", func(t *testing.T) {
		dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue65957.dir")
		main, err := os.ReadFile(filepath.Join(dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		dep, err := os.ReadFile(filepath.Join(dir, "a.go"))
		if err != nil {
			t.Fatal(err)
		}
		out, stderr := runGoSourceMultiPackage(t, "issue65957", string(main), "test/a", "a.go", string(dep))
		if out != "" || stderr != "" {
			t.Fatalf("unexpected output %q %q", out, stderr)
		}
	})
}
