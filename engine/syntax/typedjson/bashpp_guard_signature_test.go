package typedjson_test

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"reflect"
	"strings"
	"testing"
)

func TestBashPPGuardSignatureRoundTrip(t *testing.T) {
	const src = "func first(xs []string) string {\n if xs == nil || len(xs) == 0 { return \"\" }\n return xs[0]\n}\nfunc invoke(fn func() int) int {\n return fn()\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "guard.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := typedjson.Encode(&out, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BashPPFuncType") || !strings.Contains(out.String(), "BashPPCall") {
		t.Fatal("missing signature or expression-call tag")
	}
	decoded, err := typedjson.Decode(strings.NewReader(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(file, decoded) {
		t.Fatal("typed JSON changed concrete signature or nested call")
	}
}
