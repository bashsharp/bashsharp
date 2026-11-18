//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoSourceOriginalNilThreeModes(t *testing.T) {
	for name, want := range map[string]string{
		"interface-values-with-nil.go.txt": "17037492cd56a27644f2056b86e4e89c30926e51808c9806de37be4f12d287a7",
		"exercise-errors.go.txt":           "faa69218d18d0b588bea07ccdc1dcbc26667e545ed94f0c4168ad8a4ae7c6c24",
		"errors-solution.go.txt":           "d41cb89d4e7c6fc70f8295fa933a95b50b058a6566b7a731f57ca41fb0894418",
	} {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata/gosource-nil", name))
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(source)) != want {
				t.Fatal("original bytes changed")
			}
			typedSendThreeModes(t, string(source))
		})
	}
}

func TestGoSourceNilThreeModes(t *testing.T) {
	cases := map[string]string{
		"aggregate_typed_nil":            `package main;import "fmt";type T struct{N int};type Box struct{Payload any};func main(){var i any=(*T)(nil);b:=Box{i};fmt.Printf("%T %v %t\n",b.Payload,b.Payload,i==nil)}`,
		"dynamic_alias_identity":         `package main;import "fmt";type T struct{N int};type Alias=T;func main(){var p *T;var q *Alias;var i any=p;var j any=q;fmt.Println(i==j);var a any=byte(1);var b any=uint8(1);fmt.Println(a==b)}`,
		"typed_pointer_interface_assert": `package main;import "fmt";type T struct{N int};func main(){var p *T;var i any=p;var j any;fmt.Printf("%T %v %t %t\n",i,i,i==nil,j==nil);q,ok:=i.(*T);fmt.Println(ok,q==nil);_,wrong:=i.(string);fmt.Println(wrong);fmt.Println(i==j)}`,
		"nil_results":                    `package main;import "fmt";type T struct{N int};func pointer()*T{return nil};func iface()any{return nil};func err()error{return nil};func main(){p:=pointer();i:=iface();e:=err();fmt.Printf("%T %v %t %T %v %t %T %v %t\n",p,p,p==nil,i,i,i==nil,e,e,e==nil)}`,
		"live_pointer_parameter":         `package main;import "fmt";type T struct{N int};func read(p *T){fmt.Println(p==nil,p.N)};func main(){p:=&T{7};read(p);fmt.Println(p.N)}`,
		"explicit_typed_nil":             `package main;import "fmt";type T struct{N int};func main(){p:=(*T)(nil);var i any=p;fmt.Printf("%T %v %t\n",i,i,i==nil)}`,
		"nil_slice_map_results":          `package main;import "fmt";func slice()[]int{return nil};func table()map[string]int{return nil};func main(){s:=slice();m:=table();fmt.Printf("%T %v %t %T %v %t\n",s,s,s==nil,m,m,m==nil)}`,
		"nil_parameter":                  `package main;import "fmt";type T struct{N int};func inspect(p *T,i any){fmt.Printf("%T %t %T %t\n",p,p==nil,i,i==nil)};func main(){inspect(nil,nil)}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceNilReset(t *testing.T) {
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ source, want string }{
		{`package main;import "fmt";type T struct{N int};func main(){var p *T;var i any=p;fmt.Printf("%T %v %t\n",i,i,i==nil)}`, "*main.T <nil> false\n"},
		{`package main;import "fmt";type T struct{S string};func main(){p:=&T{"fresh"};fmt.Printf("%T %v %t\n",p,p,p==nil)}`, "*main.T &{fresh} false\n"},
	} {
		runner.Reset()
		out.Reset()
		errout.Reset()
		p, err := gosource.Parse(strings.NewReader(test.source), "unchanged.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = runner.Run(ctx, p.File)
		cancel()
		if err != nil || out.String() != test.want || errout.Len() != 0 {
			t.Fatalf("reset: %v %q/%q", err, out.String(), errout.String())
		}
	}
}
