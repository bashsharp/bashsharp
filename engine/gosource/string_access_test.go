package gosource_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestStringAccessKeepsGoType(t *testing.T) {
	program, err := gosource.Parse(strings.NewReader(`package main
const s="hé"
func text()string{return s}
func main(){println(s[0],s[1:],text()[0],text()[1:])}`), "string-access.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	syntax.Walk(program.File, func(node syntax.Node) bool {
		switch expr := node.(type) {
		case *syntax.BashPPIndexExpr:
			seen++
			if !expr.GoString {
				t.Error("string index lost its Go type")
			}
		case *syntax.BashPPSliceExpr:
			seen++
			if !expr.GoString {
				t.Error("string slice lost its Go type")
			}
		}
		return true
	})
	if seen != 4 {
		t.Fatalf("saw %d string accesses", seen)
	}
}
