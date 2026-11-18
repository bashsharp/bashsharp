package typedjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPConstGroupTypedJSON(t *testing.T) {
	const src = "const (\n\tA = iota\n\tB\n\tC int = iota + 2\n)\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "const.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc.String(), `"Type":"BashPPConstGroup"`) || !strings.Contains(enc.String(), `"Iota":2`) {
		t.Fatalf("encoding = %s", enc.String())
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := syntax.NewPrinter().Print(&out, node); err != nil || out.String() != src {
		t.Fatalf("round trip = %q, %v", out.String(), err)
	}
}
