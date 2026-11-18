package gosource_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint #209, Story #464: errorcheck's -e=0 uses gc's default ten-error
// cap. The reproducer is issue20298's shape without depending on the corpus
// file: more than ten package-level type errors, all accepted by the parser.
func TestSprint209ErrorcheckDefaultErrorLimit(t *testing.T) {
	src := `// errorcheck -e=0
package p

var a string = 1
var b string = 2
var c string = 3
var d string = 4
var e string = 5
var f string = 6
var g string = 7
var h string = 8
var i string = 9
var j string = 10
var k string = 11
`
	_, err := gosource.Load([]gosource.Source{{Name: "limit.go", Data: []byte(src)}}, gosource.Options{})
	if err == nil {
		t.Fatal("Load succeeded, want diagnostics")
	}
	var list gosource.ErrorList
	if !errors.As(err, &list) {
		t.Fatalf("err = %T %[1]v, want ErrorList", err)
	}
	if len(list) != 11 {
		t.Fatalf("len = %d, want ten errors plus the cap:\n%v", len(list), err)
	}
	if got, want := list[9].Error(), "limit.go:13:16: cannot use 10 (untyped int constant) as string value in variable declaration"; got != want {
		t.Fatalf("tenth diagnostic:\n got %q\nwant %q", got, want)
	}
	if got, want := list[10].Error(), "limit.go:13:16: too many errors"; got != want {
		t.Fatalf("cap diagnostic:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(err.Error(), "limit.go:14:16") {
		t.Fatalf("diagnostics were not capped:\n%v", err)
	}
}

func TestSprint209ExactDefaultErrorLimitHasNoCapDiagnostic(t *testing.T) {
	src := `// errorcheck -e=0
package p
var a string = 1
var b string = 2
var c string = 3
var d string = 4
var e string = 5
var f string = 6
var g string = 7
var h string = 8
var i string = 9
var j string = 10
`
	_, err := gosource.Load([]gosource.Source{{Name: "limit.go", Data: []byte(src)}}, gosource.Options{})
	var list gosource.ErrorList
	if !errors.As(err, &list) || len(list) != 10 || strings.Contains(err.Error(), "too many errors") {
		t.Fatalf("diagnostics = %d, %v; want exactly ten without cap marker", len(list), err)
	}
}
