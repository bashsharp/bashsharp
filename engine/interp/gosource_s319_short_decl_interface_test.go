//go:build full

package interp_test

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5

import "testing"

// go/types resolver.go reads `if importer := check.conf.Importer; ...` and
// then asserts `importer.(ImporterFrom)`. The field's static type is a local
// interface holding a dependency-owned value; the short-declared variable
// must keep that interface identity, as `var r Reader = c.R` already does.
func TestS319ShortDeclKeepsInterfaceOfDependencyValue(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"strings"
)

type Reader interface{ Read([]byte) (int, error) }
type ReaderAt interface {
	Reader
	ReadAt([]byte, int64) (int, error)
}
type Closer interface{ Close() error }
type config struct{ R Reader }

func main() {
	c := &config{R: strings.NewReader("x")}
	if r := c.R; r == nil {
		fmt.Println("nil")
	} else if ra, ok := r.(ReaderAt); ok {
		fmt.Println("readerat", ra != nil)
	}
	r := c.R
	_, closer := r.(Closer)
	fmt.Println(closer)
	var empty config
	e := empty.R
	fmt.Println(e == nil)
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil || got.stdout != "readerat true\nfalse\ntrue\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
