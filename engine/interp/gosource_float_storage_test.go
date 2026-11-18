// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceFloatStorage(t *testing.T) {
	for name, source := range map[string]string{
		"tuple": `package main
func values()(string,float64){return "ok", 0.375}
func main(){s,f:=values();if s!="ok" || f!=0.375 {panic("tuple")}}`,
		"named_tuple": `package main
type Measure float32
func values()(Measure,Measure){return Measure(0.1),Measure(0.2)}
func main(){a,b:=values();c:=a;want:=float32(0.1);if float32(c)!=want || b!=Measure(0.2){panic("named tuple")}}`,
		"generic_minimum": `package main
func minimum[T interface{~float64|~string}](xs []T)T{n:=xs[0];for _,x:=range xs[1:]{if x<n{n=x}};return n}
func main(){if minimum([]float64{6.7,2.3,9.1})!=2.3 || minimum([]string{"3/8","1/4"})!="1/4" {panic("minimum")}}`,
		"local": `package main
func main(){a:=0.375;b:=a;c:=b+0.125;if c!=0.5 {panic("local")}}`,
		"generic_collection": `package main
func sum[T interface{~float32|~float64}](xs []T)T{var n T;for _,x:=range xs {n=n+x};return n}
func main(){xs:=[]float64{0.375,0.125};if sum(xs)!=0.5 {panic("sum")}}`,
		"rounding": `package main
var value=float64(float32(0.01))
func main(){if value!=0.00999999977648258209228515625 {panic("rounding")}}`,
		"strings": `package main
func identity[T interface{~string}](x T)T{return x}
func main(){s:="3/8";if identity(s)!="3/8" || s+"1"!="3/81" {panic("string")}}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestGoSourceFloatRejectsStrings(t *testing.T) {
	for _, expression := range []string{`float64("3/8")`, `float32("0.375")`} {
		source := "package main\nfunc main(){_=" + expression + "}"
		if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
			t.Fatalf("accepted %s", expression)
		}
	}
}
