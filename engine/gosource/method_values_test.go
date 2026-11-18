package gosource_test

import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"strings"
	"testing"
)

func TestGoMethodValueMetadata(t *testing.T) {
	const source = `package main
 type S struct{F func()int}
 func(s S)Read()int{return 1}
 func makeS()S{return S{}}
 func main(){s:=S{};f:=s.Read;g:=makeS().Read;h:=s.F;_,_,_=f,g,h}`
	p, err := gosource.Parse(strings.NewReader(source), "methods.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	verify := func(n syntax.Node) {
		methods, addressable, fields := 0, 0, 0
		syntax.Walk(n, func(n syntax.Node) bool {
			if sel, ok := n.(*syntax.BashPPSelectorExpr); ok && sel.FuncType != nil {
				if sel.MethodValue {
					methods++
					if sel.ReceiverAddressable {
						addressable++
					}
				} else {
					fields++
				}
			}
			return true
		})
		if methods != 2 || addressable != 1 || fields != 1 {
			t.Fatalf("methods=%d addressable=%d function fields=%d", methods, addressable, fields)
		}
	}
	verify(p.File)
	var b bytes.Buffer
	if err := typedjson.Encode(&b, p.File); err != nil {
		t.Fatal(err)
	}
	decoded, err := typedjson.Decode(&b)
	if err != nil {
		t.Fatal(err)
	}
	verify(decoded)
}
