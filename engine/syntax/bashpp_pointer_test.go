// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"reflect"
	"strings"
	"testing"
)

const bashppPointerSource = `type Count int
type Ptr *int
func main() {
	var nilp *Count
	var named Ptr
	named = new(int)
	x := 1
	p := &x
	y := *p
	*p = 2
	q := new([]int)
}
`

func TestBashPPPointerASTStreamingWalkAndPrint(t *testing.T) {
	parse := func(chunk int) *File {
		r := strings.NewReader(bashppPointerSource)
		var in interface{ Read([]byte) (int, error) } = r
		if chunk == 1 {
			in = &pointerOneByteReader{r: r}
		}
		f, err := NewParser(Variant(LangBashPP)).Parse(in, "pointer.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered, oneByte := parse(0), parse(1)
	if !reflect.DeepEqual(buffered, oneByte) {
		t.Fatal("buffered and one-byte pointer trees differ")
	}
	want := map[string]bool{"*syntax.BashPPPointerType": false, "*syntax.BashPPAddressExpr": false, "*syntax.BashPPDerefExpr": false, "*syntax.BashPPNewExpr": false, "*syntax.BashPPAssign": false}
	Walk(buffered, func(n Node) bool {
		if n != nil {
			if _, ok := want[reflect.TypeOf(n).String()]; ok {
				want[reflect.TypeOf(n).String()] = n.Pos().IsValid() && n.End().IsValid()
			}
		}
		return true
	})
	for kind, seen := range want {
		if !seen {
			t.Fatalf("Walk missed positioned %s", kind)
		}
	}
	var out strings.Builder
	if err := NewPrinter().Print(&out, buffered); err != nil {
		t.Fatal(err)
	}
	reparsed, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(out.String()), "pointer.bpp")
	if err != nil || !reflect.DeepEqual(buffered, reparsed) {
		t.Fatalf("pointer print round trip failed: %v\n%s", err, out.String())
	}
}

type pointerOneByteReader struct{ r *strings.Reader }

func (r *pointerOneByteReader) Read(p []byte) (int, error) { return r.r.Read(p[:1]) }

func TestBashPPPointerFallback(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, _ := NewParser(Variant(lang)).Parse(strings.NewReader("p := &x\n"), "")
		found := false
		if f != nil {
			Walk(f, func(n Node) bool { _, found = n.(*BashPPAddressExpr); return !found })
		}
		if found {
			t.Fatalf("%v produced a typed address expression", lang)
		}
	}
	f, _ := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("p := &x\n"), "")
	found := false
	if f != nil {
		Walk(f, func(n Node) bool { _, found = n.(*BashPPAddressExpr); return !found })
	}
	if found {
		t.Fatal("top-level Class-E address expression was claimed")
	}
}
