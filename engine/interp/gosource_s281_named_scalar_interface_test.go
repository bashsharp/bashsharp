//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestS281NamedScalarInterfaceStoredInStructSlice(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/token"
)

type atPos token.Pos

func (p atPos) Pos() token.Pos { return token.Pos(p) }

type positioner interface {
	Pos() token.Pos
}

type desc struct {
	posn positioner
	msg  string
}

type errList struct {
	desc []desc
}

func (e *errList) addf(at positioner, msg string) {
	e.desc = append(e.desc, desc{at, msg})
}

func (e *errList) msg() string {
	out := ""
	for i := range e.desc {
		p := &e.desc[i]
		if i > 0 && p.posn.Pos().IsValid() {
			out += fmt.Sprintf("%d:", p.posn.Pos())
		}
		out += p.msg
	}
	return out
}

func main() {
	var e errList
	e.addf(atPos(0), "first")
	e.addf(atPos(7), " second")
	fmt.Println(e.msg())
}
`
	differGoSource(t, source, nil, "")
}

func TestS281ImportedNamedScalarInterfaceStoredInStructSlice(t *testing.T) {
	out, stderr, err := runGoSourcePackages(t, `package main

import "test/p"

func main() {
	p.Run()
}
`, map[string]string{"a.go": `package p

import (
	"fmt"
	"go/token"
)

type atPos token.Pos

func (p atPos) Pos() token.Pos { return token.Pos(p) }

type positioner interface {
	Pos() token.Pos
}

type desc struct {
	posn positioner
	msg  string
}

type errList struct {
	desc []desc
}

func (e *errList) addf(at positioner, msg string) {
	e.desc = append(e.desc, desc{at, msg})
}

func (e *errList) msg() string {
	out := ""
	for i := range e.desc {
		p := &e.desc[i]
		if i > 0 && p.posn.Pos().IsValid() {
			out += fmt.Sprintf("%d:", p.posn.Pos())
		}
		out += p.msg
	}
	return out
}

func Run() {
	var e errList
	e.addf(atPos(0), "first")
	e.addf(atPos(7), " second")
	fmt.Println(e.msg())
}
`})
	if err != nil || out != "first7: second\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS281ComputedFunctionReceiverEvaluatedOnce(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
type Holder struct{ Fn func() int }
var calls int
func makeHolder() Holder { calls++; return Holder{Fn: func() int { return 7 }} }
func main() { fmt.Println(makeHolder().Fn(), calls) }
`, nil, "")
}

func TestS281ComputedFunctionIndexEvaluatedOnce(t *testing.T) {
	differGoSource(t, `package main
import "fmt"
type Holder struct{ Fn func() int }
var calls int
func index() int { calls++; return 0 }
func main() { values := []Holder{{Fn: func() int { return 9 }}}; fmt.Println(values[index()].Fn(), calls) }
`, nil, "")
}
