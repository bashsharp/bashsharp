// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func TestBashPPMethodDispatch(t *testing.T) {
	const src = `type Count int
func (v Count) Show(prefix string) {
 echo "$prefix:$v"
}
func (p *Count) Pointer() {
 echo "ptr:$p"
}
func relay(v Count) {
 v.Show(relay)
}
func makeCount() Count {
 return 11
}
var v Count = 7
v.Show(direct)
v.Pointer()
relay(v)
x := makeCount()
x.Show(result)
var q *Count = 9
q.Show(deref)
(*Count).Pointer(q)
f := v.Show
f(value)
pf := v.Pointer
pf()
Count.Show(v, expression)
func later() {
 defer v.Show(deferred)
}
later()
var p *Count
func (p *Count) NilOK() {
 if [ -z "$p" ]; then echo nil; fi
}
p.NilOK()
`
	qt.Assert(t, qt.Equals(runBashPPFunc(t, src), "direct:7\nptr:7\nrelay:7\nresult:11\nderef:9\nptr:9\nvalue:7\nptr:7\nexpression:7\ndeferred:7\nnil\n"))
}

func TestBashPPLocalMethodSelectorWinsOverImport(t *testing.T) {
	const src = `import fmt "fmt"
type T int
func (v T) Println() {
 echo local
}
var fmt T = 1
fmt.Println()
`
	qt.Assert(t, qt.Equals(runBashPPFunc(t, src), "local\n"))
}

func TestBashPPNamedTypeIdentityPersistsAcrossRuns(t *testing.T) {
	var output strings.Builder
	r := bashPPRunner(t, &output, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, "type T int\nfunc (v T) Show(label string) { echo \"$label:$v\"; }\nvar v T = 4\n")
	bashPPRun(t, r, "v.Show(session)\n")
	qt.Assert(t, qt.Equals(output.String(), "session:4\n"))
}

func TestBashPPMethodDeclarationDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"undefined receiver", "func (v Missing) M() { return; }\n", "invalid receiver type Missing"},
		{"alias receiver", "type Alias = int\nfunc (v Alias) M() { return; }\n", "cannot define methods on an alias"},
		{"duplicate across method sets", "type T int\nfunc (v T) M() { return; }\nfunc (p *T) M() { return; }\n", "method T.M redeclared"},
		{"nil pointer value method", "type T int\nfunc (v T) M() { return; }\nvar p *T\np.M()\n", "value method M called using nil *T pointer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "methods.bpp")
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &output, &output))
			qt.Assert(t, qt.IsNil(err))
			err = r.Run(context.Background(), f)
			var status interp.ExitStatus
			qt.Assert(t, qt.IsTrue(errors.As(err, &status)))
			qt.Assert(t, qt.StringContains(output.String(), tc.want))
		})
	}
}

func TestBashPPMethodPOSIXRuntimeGate(t *testing.T) {
	const src = "type T int\nfunc (v T) M() { return; }\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP), syntax.PosixMode(true)).Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))
	var output strings.Builder
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.WithPosixMode(true), interp.StdIO(nil, nil, &output))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.ErrorMatches(r.Run(context.Background(), f), `exit status 2`))
}
