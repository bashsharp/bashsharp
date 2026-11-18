//go:build full

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b
//
// Wave 2: collection/builtin carrier repairs measured by the Go-source corpus
// roots chanlinear, issue19359, issue19710, issue22605 and issue23734.
//
//   - A channel argument to append keeps its identity, as a composite literal
//     element already did (root chanlinear).
//   - A deferred value builtin's captured collection arguments survive the
//     block that ran the defer statement (root issue19710).
//   - Hashing an unhashable map key — through delete or a compound map
//     update, with the key's static or dynamic type unhashable — is Go's
//     recoverable runtime panic, with no diagnostic printed past the recover
//     (roots issue19359, issue23734).
//   - An array-typed map key spelled as a variable or a type assertion is
//     read whole instead of failing the scalar element fallback (root
//     issue22605's interpreter-side link).
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestStory460W2AppendChannelElement(t *testing.T) {
	// The appended element must be the SAME channel: a send through the slice
	// element is received through the original variable.
	src := `package main
import "fmt"
func main() {
	d := make(chan int, 1)
	var a []chan int
	a = append(a, d)
	a[0] <- 7
	fmt.Println(<-d)
}`
	out, stderr, err := runGoSource(t, "story460w2chan", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "7\n"))
}

func TestStory460W2DeferredBuiltinArgOutlivesBlock(t *testing.T) {
	// The loop body's scope is gone when the defers run; the captured map
	// argument must still name the same map, and the deletes must land in it.
	src := `package main
import "fmt"
func f() map[int]bool {
	m := map[int]bool{}
	for i := 0; i < 3; i++ {
		m[i] = true
		defer delete(m, i)
	}
	return m
}
func main() {
	fmt.Println(len(f()))
}`
	out, stderr, err := runGoSource(t, "story460w2defer", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "0\n"))
}

func TestStory460W2DeleteUnhashableKeyPanics(t *testing.T) {
	// The key's static type is unhashable; delete panics with Go's runtime
	// error instead of aborting the interpreter.
	src := `package main
import "fmt"
func main() {
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	m := map[interface{}]int{}
	k := []string{"a"}
	delete(m, k)
	fmt.Println("unreachable")
}`
	out, stderr, err := runGoSource(t, "story460w2delpanic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "recovered: runtime error: hash of unhashable type []string\n"))
}

func TestStory460W2UnhashableInterfaceKeyQuietRecover(t *testing.T) {
	// The dynamic type behind an interface key is unhashable: both a compound
	// map update and a delete panic recoverably, and NOTHING is printed to
	// stderr past the recover.
	src := `package main
import "fmt"
func add(m map[interface{}]int, key interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("add failed: %v", r)
		}
	}()
	m[key] += 1
	return nil
}
func del(m map[interface{}]int, key interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("del failed: %v", r)
		}
	}()
	delete(m, key)
	return nil
}
func main() {
	m := make(map[interface{}]int)
	fmt.Println(add(m, []int{1}))
	fmt.Println(del(m, []int{1}))
	fmt.Println(add(m, "ok"), m["ok"])
}`
	out, stderr, err := runGoSource(t, "story460w2quiet", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "add failed: runtime error: hash of unhashable type []int\ndel failed: runtime error: hash of unhashable type []int\n<nil> 1\n"))
}

func TestStory460W2ArrayMapKeyForms(t *testing.T) {
	// An array key spelled as a variable hashes by value (a distinct equal
	// array reads the entry back), and a type assertion key is read whole.
	src := `package main
import "fmt"
func main() {
	m := map[[2]string]int{}
	a := [2]string{"x", "y"}
	m[a] = 7
	b := [2]string{"x", "y"}
	var i interface{} = [2]string{"x", "y"}
	fmt.Println(m[b], m[i.([2]string)])
}`
	out, stderr, err := runGoSource(t, "story460w2arraykey", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "7 7\n"))
}
