//go:build full

package interp_test

import (
	"github.com/bashsharp/bashsharp/engine/gosource"
	"strings"
	"testing"
)

// Sprint: #243; Story: #673; Story-ID: f24307569417
func TestS243LinkedDotImportedTypeIdentity(t *testing.T) {
	const mainSrc = `package main
import "./p"
func main() { println(p.OS() != "") }
`
	const depSrc = `package p
import . "go/build"
type LocalContext = Context
func identity(c LocalContext) Context { return c }
func OS() string { return identity(Default).GOOS }
`
	_, stderr := runGoSourceMultiPackage(t, "s243linkeddot", mainSrc, "test/p", "p.go", depSrc)
	if stderr != "true\n" {
		t.Fatalf("linked dot-import type lost identity: %q", stderr)
	}
}

func TestS243CheckedInitializerScope(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
var n = len(<-c)
var c = func() chan *[3]int {
 c := make(chan *[3]int, 2)
 c <- nil
 c <- new([3]int)
 return c
}()
func main() { fmt.Println(n, <-c != nil) }
`, nil, "")
}

func TestS243CheckedInitializerCycleRejected(t *testing.T) {
	_, err := gosource.Parse(strings.NewReader(`package main
var c = func() int { return c }()
func main() {}
`), "cycle.go", gosource.Options{RunMain: true})
	if err == nil {
		t.Fatal("real initializer self-dependency accepted")
	}
}

func TestS243ReceivePointerComparisonConsumesOnce(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
func main() {
 c := make(chan *int, 3)
 c <- nil
 c <- new(int)
 c <- nil
 close(c)
 fmt.Println(<-c == nil, <-c != nil, <-c == nil, <-c == nil)
 boxed := make(chan any, 2)
 boxed <- nil
 boxed <- (*int)(nil)
 close(boxed)
 fmt.Println(<-boxed == nil, <-boxed == nil, <-boxed == nil)
}
`, nil, "")
}
