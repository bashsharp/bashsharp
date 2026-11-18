package interp_test

// Sprint: #118; Story: #3; Story-ID: fa07603b71dc

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// The gosource short-decl converter special-cased type-conversion calls on
// the RHS of `:=` but not the predeclared `new` builtin, so `v := new(Vertex)`
// fell through to a generic call node and always failed with
// BASHPP-EBUILTIN-TYPE, even though BashPPNewExpr evaluation already worked
// for every other spelling of `new`.
func TestGoSourceShortDeclNew(t *testing.T) {
	source := `package main
import "fmt"
type Vertex struct{ X, Y int }
func main() {
	v := new(Vertex)
	fmt.Println(v.X)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "new_struct.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNil(err))

	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	qt.Assert(t, qt.IsNil(err))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", errout.String()))
	qt.Assert(t, qt.Equals(errout.String(), ""))
	qt.Assert(t, qt.Equals(out.String(), "0\n"))
}
