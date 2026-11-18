package interp

import (
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"strings"

	"testing"
)

func TestGoSourceReaderLocalBufferProof(t *testing.T) {
	for _, tc := range []struct {
		name, body, extra string
		want              bool
	}{
		{"literal_range", `for i:=range p{p[i]='A'};return len(p),nil`, "", true},
		{"alias", `q:=p;for i:=range q{q[i]='A'};return len(p),nil`, "", false},
		{"retained", `saved=p;for i:=range p{p[i]='A'};return len(p),nil`, `var saved []byte`, false},
		{"call", `for i:=range p{p[i]=byte(next())};return len(p),nil`, `func next()int{return 65}`, false},
		{"defer", `defer func(){p[0]='B'}();for i:=range p{p[i]='A'};return len(p),nil`, "", false},
		{"concurrent", `go func(){p[0]='B'}();for i:=range p{p[i]='A'};return len(p),nil`, "", false},
		{"receiver", `r.n++;for i:=range p{p[i]='A'};return len(p),nil`, "", false},
		{"early_return", `for i:=range p{p[i]='A';return 1,nil};return len(p),nil`, "", false},
		{"index_rebound", `for i:=range p{i=0;p[i]='A'};return len(p),nil`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := `package main;type reader struct{n int};` + tc.extra + `
func(r reader)Read(p []byte)(int,error){` + tc.body + `};func main(){}`
			p, err := gosource.Parse(strings.NewReader(source), "original.go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var decl *syntax.BashPPFuncDecl
			syntax.Walk(p.File, func(n syntax.Node) bool {
				if d, ok := n.(*syntax.BashPPFuncDecl); ok && d.Name.Value == "Read" {
					decl = d
				}
				return true
			})
			if got := bashPPReaderLocalBufferProof(decl); got != tc.want {
				t.Fatalf("proof=%v want%v; AST %#v", got, tc.want, decl)
			}
			if tc.want {
				r := &Runner{}
				f := &bashPPFunc{decl: decl, scope: newBashPPScope(nil)}
				if !r.bashPPReaderLocalBufferAllowed(f) {
					t.Fatal("valid method refused")
				}
				for _, name := range []string{"len", "nil"} {
					f.scope.entries[name] = &bashPPCell{}
					if r.bashPPReaderLocalBufferAllowed(f) {
						t.Fatalf("shadowed %s accepted", name)
					}
					delete(f.scope.entries, name)
				}
			}
		})
	}
}
