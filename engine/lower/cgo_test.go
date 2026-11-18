package lower

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPrepareCgoCheckerFileKeepsPseudoPackageSeparate(t *testing.T) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "cgo.go", `package p
import "C"
type C struct{ X int }
var _ C
func f() { C.free(nil) }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepareCgoCheckerFile(file)
	var out bytes.Buffer
	if err := format.Node(&out, fs, file); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "type __lower_go_C struct") || !strings.Contains(text, "var _ __lower_go_C") || !strings.Contains(text, "C.free(nil)") {
		t.Fatalf("checker rewrite:\n%s", text)
	}
}
