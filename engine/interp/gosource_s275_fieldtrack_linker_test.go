//go:build full

package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The two sources are the executable package of fixedbugs/issue20014.go's
// runindir recipe. Both GetY methods exist, but neither is reachable from main.
func TestS275FieldTrackLinkerReport(t *testing.T) {
	const mainSource = `package main
import ("sort"; "strings"; "issue20014.dir/a")
func main() {
 samePackage(); crossPackage()
 var fields []string
 for _, line := range strings.Split(fieldTrackInfo, "\n") {
  if line != "" { fields = append(fields, strings.Split(line, "\t")[0]) }
 }
 sort.Strings(fields)
 for _, field := range fields { println(field) }
}
type T struct { X int ` + "`go:\"track\"`" + `; Y int ` + "`go:\"track\"`" + `; Z int }
func (t *T) GetX() int { return t.X }
func (t *T) GetY() int { return t.Y }
func (t *T) GetZ() int { return t.Z }
func samePackage() { var t T; println(t.GetX()); println(t.GetZ()) }
func crossPackage() { var t a.T; println(t.GetX()); println(t.GetZ()) }
var fieldTrackInfo string
`
	const depSource = `package a
type T struct { X int ` + "`go:\"track\"`" + `; Y int ` + "`go:\"track\"`" + `; Z int }
func (t *T) GetX() int { return t.X }
func (t *T) GetY() int { return t.Y }
func (t *T) GetZ() int { return t.Z }
`
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(mainSource)}}, gosource.Options{
		RunMain: true, ImportBase: "issue20014.dir", Packages: []gosource.PackageSpec{{
			Path: "issue20014.dir/a", Sources: []gosource.Source{{Name: "a/a.go", Data: []byte(depSource)}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, experiment, flags, goflags, want string
	}{
		{"enabled", "fieldtrack", "-k=main.fieldTrackInfo", "", "0\n0\n0\n0\nissue20014.dir/a.T.X\nmain.T.X\n"},
		{"goflags", "fieldtrack", "", "-ldflags=-k=main.fieldTrackInfo", "0\n0\n0\n0\nissue20014.dir/a.T.X\nmain.T.X\n"},
		{"experiment-off", "", "-k=main.fieldTrackInfo", "", "0\n0\n0\n0\n"},
		{"experiment-negated", "nofieldtrack", "-k=main.fieldTrackInfo", "", "0\n0\n0\n0\n"},
		{"linker-flag-off", "fieldtrack", "", "", "0\n0\n0\n0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()),
				interp.Env(expand.ListEnviron("GOEXPERIMENT="+tc.experiment, "GOFLAGS="+tc.goflags)),
				interp.GoSourceLinkFlags(tc.flags), interp.StdIO(nil, &output, &output))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), program.File); err != nil {
				t.Fatalf("Run: %v; output=%q", err, output.String())
			}
			if got := output.String(); got != tc.want {
				t.Fatalf("output=%q, want %q", got, tc.want)
			}
		})
	}
	t.Run("unreachable-package-method", func(t *testing.T) {
		withoutCrossPackage := strings.Replace(mainSource, "samePackage(); crossPackage()", "samePackage()", 1)
		program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(withoutCrossPackage)}}, gosource.Options{
			RunMain: true, ImportBase: "issue20014.dir", Packages: []gosource.PackageSpec{{
				Path: "issue20014.dir/a", Sources: []gosource.Source{{Name: "a/a.go", Data: []byte(depSource)}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()),
			interp.Env(expand.ListEnviron("GOEXPERIMENT=fieldtrack")),
			interp.GoSourceLinkFlags("-k=main.fieldTrackInfo"), interp.StdIO(nil, &output, &output))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(context.Background(), program.File); err != nil {
			t.Fatalf("Run: %v; output=%q", err, output.String())
		}
		if got, want := output.String(), "0\n0\nmain.T.X\n"; got != want {
			t.Fatalf("output=%q, want %q", got, want)
		}
	})
}
